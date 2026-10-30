package queue

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func waitForSQLCondition(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arguments ...any) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var ready bool
		if err := pool.QueryRow(ctx, query, arguments...).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned database condition did not occur before its deadline")
}

func TestClaimRechecksExpiryAfterRateWaitRefundsTokensAndContinues(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	expired := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
	valid := insertFencedFixture(t, pool, name, 1, nil, nil, 0)
	path := fmt.Sprintf("owned.admission.task_%d", time.Now().UnixNano())
	key := "task:" + path
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET task_path=$1,spec_json=jsonb_build_object('task_path',$1::text),priority=100,expires_at=clock_timestamp()+interval '1 second' WHERE result_id=$2", path, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO public.rate_limits(key,tokens_per_second,burst_size,current_tokens,last_refilled_at) VALUES($1,1,10,10,clock_timestamp())", key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM public.rate_limits WHERE key=$1", key) })
	var before string
	if err := pool.QueryRow(ctx, "SELECT to_jsonb(tr)::text FROM public.task_runs tr WHERE result_id=$1", expired).Scan(&before); err != nil {
		t.Fatal(err)
	}
	locker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(context.Background())
	var blocker int
	if err := locker.QueryRow(ctx, "SELECT pg_backend_pid() FROM public.rate_limits WHERE key=$1 FOR UPDATE", key).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	type result struct {
		task *TaskRun
		err  error
	}
	finished := make(chan result, 1)
	go func() { task, err := service.Claim(ctx, "admission-owner", name, 60, 0); finished <- result{task, err} }()
	waitForSQLCondition(t, ctx, pool, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND $1=ANY(pg_catalog.pg_blocking_pids(pid)))", blocker)
	waitForSQLCondition(t, ctx, pool, "SELECT expires_at<=clock_timestamp() FROM public.task_runs WHERE result_id=$1", expired)
	if err := locker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	actual := <-finished
	if actual.err != nil || actual.task == nil || actual.task.ResultID != valid {
		t.Fatalf("expired candidate executed or starved unrelated work: %+v", actual)
	}
	var after string
	var tokens float64
	if err := pool.QueryRow(ctx, "SELECT to_jsonb(tr)::text FROM public.task_runs tr WHERE result_id=$1", expired).Scan(&after); err != nil || before != after {
		t.Fatal("expired admission spent an attempt or mutated task state")
	}
	if err := pool.QueryRow(ctx, "SELECT current_tokens FROM public.rate_limits WHERE key=$1", key).Scan(&tokens); err != nil || tokens != 10 {
		t.Fatal("rejected candidate consumed a rate token")
	}
}

func TestLimitedPruneSkipsUncommittedRunningClaimAndMakesProgress(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	claimed := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
	other := insertFencedFixture(t, pool, name, 1, nil, nil, 0)
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET expires_at=clock_timestamp()+CASE WHEN result_id=$1 THEN interval '500 milliseconds' ELSE interval '700 milliseconds' END WHERE result_id=ANY($2::bigint[])", claimed, []int64{claimed, other}); err != nil {
		t.Fatal(err)
	}
	claim, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Rollback(context.Background())
	if _, err := claim.Exec(ctx, "UPDATE public.task_runs SET status='RUNNING',leased_by='prune-owner',lease_token=gen_random_uuid(),leased_until=clock_timestamp()+interval '1 minute' WHERE result_id=$1", claimed); err != nil {
		t.Fatal(err)
	}
	waitForSQLCondition(t, ctx, pool, "SELECT bool_and(expires_at<=clock_timestamp()) FROM public.task_runs WHERE result_id=ANY($1::bigint[])", []int64{claimed, other})
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if count, err := service.PruneExpired(bounded, 1, false); err != nil || count != 1 {
		t.Fatalf("prune waited behind a claim or missed unrelated expired work: %d %v", count, err)
	}
	if err := claim.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var survives, removed bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.task_runs WHERE result_id=$1 AND status='RUNNING' AND lease_token IS NOT NULL),NOT EXISTS(SELECT 1 FROM public.task_runs WHERE result_id=$2)", claimed, other).Scan(&survives, &removed); err != nil || !survives || !removed {
		t.Fatal("limited prune removed a running claim or failed to prune the unrelated candidate")
	}
}
