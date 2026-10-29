package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type terminalFixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	service   *Service
	ids       []int64
	workflows []string
}

func newTerminalFixture(t *testing.T) *terminalFixture {
	t.Helper()
	ctx, pool, service, _ := ownedFencedQueue(t)
	// These tests exercise database-wide maintenance. The integration URL points
	// to the explicitly disposable owned database, not application data.
	if _, err := pool.Exec(ctx, "DELETE FROM public.task_runs"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM public.workflow_runs"); err != nil {
		t.Fatal(err)
	}
	f := &terminalFixture{ctx: ctx, pool: pool, service: service}
	t.Cleanup(func() {
		if len(f.ids) > 0 {
			_, err := pool.Exec(context.Background(), "DELETE FROM public.task_runs WHERE result_id=ANY($1::bigint[])", f.ids)
			if err != nil {
				t.Error(err)
			}
		}
		for _, id := range f.workflows {
			_, err := pool.Exec(context.Background(), "DELETE FROM public.workflow_runs WHERE workflow_id=$1::uuid", id)
			if err != nil {
				t.Error(err)
			}
		}
	})
	return f
}
func (f *terminalFixture) workflow(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(f.ctx, "SELECT gen_random_uuid()::text").Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.workflows = append(f.workflows, id)
	return id
}
func (f *terminalFixture) node(t *testing.T, status string, parent *int64, workflow *string, expired bool) int64 {
	t.Helper()
	text := readContractFile(t, "spec_hash.json")
	hash := readContractFile(t, "spec_hash.sha256")
	var id int64
	err := f.pool.QueryRow(f.ctx, `WITH source AS(SELECT $1::text raw,($1::text)::jsonb body)
 INSERT INTO public.task_runs(spec_json,canonical_spec_text,spec_hash,spec_version,dedupe_active,backend_alias,queue_name,priority,task_path,
 max_attempts,timeout_seconds,lock_key,concurrency_key,concurrency_limit,run_after,status,wait_count,parent_id,workflow_id,expires_at)
 SELECT body,raw,$2,1,false,body->>'backend_alias',body->>'queue_name',(body->>'priority')::integer,body->>'task_path',
 (body->'exec'->>'max_attempts')::integer,(body->'exec'->>'timeout_seconds')::integer,body->>'lock_key',body->>'concurrency_key',
 (body->>'concurrency_limit')::integer,(body->>'run_after')::timestamptz,$3,CASE WHEN $3='WAITING' THEN 1 ELSE 0 END,$4::bigint,$5::uuid,
 CASE WHEN $6::boolean THEN clock_timestamp()-interval '1 hour' END FROM source RETURNING result_id`, text, hash, status, parent, workflow, expired).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	f.ids = append(f.ids, id)
	return id
}
func (f *terminalFixture) claim(t *testing.T, want int64) *TaskRun {
	t.Helper()
	task, err := f.service.Claim(f.ctx, "owned-terminal-worker", "default", 60, 0)
	if err != nil || task == nil || task.ResultID != want {
		t.Fatalf("claim %d: %v %v", want, task, err)
	}
	return task
}

func TestConcurrentHeaderFailureAndCancellationCountEachNewTerminalOnce(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	first := f.node(t, "READY", nil, &workflow, false)
	claim := f.claim(t, first)
	second := f.node(t, "READY", nil, &workflow, false)
	callback := f.node(t, "WAITING", nil, &workflow, false)
	f.summary(t, workflow, 2, callback)
	held, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.Background()) }()
	var blocker uint32
	if err := held.QueryRow(f.ctx, "SELECT pg_backend_pid() FROM public.task_runs WHERE result_id=$1 FOR UPDATE", callback).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	completed := 0
	defer func() {
		_ = held.Rollback(context.Background())
		for completed < 2 {
			select {
			case <-done:
				completed++
			case <-time.After(5 * time.Second):
				t.Error("concurrent transition cleanup did not finish")
				return
			}
		}
	}()
	go func() {
		_, err := f.service.CompleteFailure(f.ctx, first, "owned-terminal-worker", claim.LeaseToken, json.RawMessage(`{"kind":"owned_failure"}`), false, time.Now(), nil)
		done <- err
	}()
	go func() { _, err := f.service.RequestCancel(f.ctx, second); done <- err }()
	waitForSQLCondition(t, f.ctx, f.pool, `SELECT count(*)>=2 FROM pg_catalog.pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND($1=ANY(pg_catalog.pg_blocking_pids(pid)) OR EXISTS(
  SELECT 1 FROM unnest(pg_catalog.pg_blocking_pids(pid)) waiting(pid)
  WHERE $1=ANY(pg_catalog.pg_blocking_pids(waiting.pid))))`, blocker)
	if err := held.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-done:
			completed++
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent header transitions did not finish")
		}
	}
	success, failures, status := f.counts(t, workflow)
	if success != 0 || failures != 2 || status != "FAILED" || f.status(t, callback) != "FAILED" {
		t.Fatalf("concurrent counters: %d %d %s", success, failures, status)
	}
	var callbackErrors int
	if err := f.pool.QueryRow(f.ctx, "SELECT jsonb_array_length(errors_json) FROM public.task_runs WHERE result_id=$1", callback).Scan(&callbackErrors); err != nil || callbackErrors != 1 {
		t.Fatal("callback was terminalized more than once", err)
	}
}

func TestInvalidWorkflowCounterRollsBackRootAndDependencyTransitions(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	root := f.node(t, "READY", nil, &workflow, false)
	claim := f.claim(t, root)
	callback := f.node(t, "WAITING", nil, &workflow, false)
	f.summary(t, workflow, 0, callback)
	for _, success := range []bool{false, true} {
		var err error
		if success {
			err = f.service.CompleteSuccess(f.ctx, root, "owned-terminal-worker", claim.LeaseToken, json.RawMessage(`true`), nil)
		} else {
			_, err = f.service.CompleteFailure(f.ctx, root, "owned-terminal-worker", claim.LeaseToken, json.RawMessage(`{}`), false, time.Now(), nil)
		}
		if !errors.Is(err, ErrWorkflowCounters) || f.status(t, root) != "RUNNING" || f.status(t, callback) != "WAITING" {
			t.Fatal("invalid counters allowed a partial transition", err)
		}
		var unchanged bool
		if err := f.pool.QueryRow(f.ctx, `SELECT lease_token::text=$1 AND jsonb_array_length(errors_json)=0 FROM public.task_runs WHERE result_id=$2`, claim.LeaseToken, root).Scan(&unchanged); err != nil || !unchanged {
			t.Fatal("failed counter guard consumed the claim", err)
		}
	}
	before := acknowledgedCounter(t, reclaimedTasks.WithLabelValues(string(StatusFailed)))
	if _, err := f.pool.Exec(f.ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()-interval '1 minute' WHERE result_id=$1", root); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ReclaimWithOptions(f.ctx, ReclaimOptions{Action: "fail"})
	if !errors.Is(err, ErrWorkflowCounters) || result.Count != 0 || f.status(t, root) != "RUNNING" || f.status(t, callback) != "WAITING" {
		t.Fatal("reclaim reported rolled-back transitions", result, err)
	}
	if acknowledgedCounter(t, reclaimedTasks.WithLabelValues(string(StatusFailed))) != before {
		t.Fatal("rolled-back reclaim incremented acknowledged transitions")
	}
}

func TestReclaimUsesTerminalFinalizerOnlyForTerminalChainRoots(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic-retry", true: "terminal"}[terminal], func(t *testing.T) {
			f := newTerminalFixture(t)
			workflow := f.workflow(t)
			root := f.node(t, "READY", nil, &workflow, false)
			f.claim(t, root)
			child := f.node(t, "WAITING", &root, &workflow, false)
			grandchild := f.node(t, "WAITING", &child, &workflow, false)
			if _, err := f.pool.Exec(f.ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()-interval '1 minute' WHERE result_id=$1", root); err != nil {
				t.Fatal(err)
			}
			action := "requeue"
			if terminal {
				action = "fail"
			}
			result, err := f.service.ReclaimWithOptions(f.ctx, ReclaimOptions{Action: action, Limit: 1})
			if err != nil || result.Count != 1 {
				t.Fatalf("reclaim failed: %+v %v", result, err)
			}
			if terminal {
				if result.Failed != 1 || f.status(t, root) != "FAILED" || f.status(t, child) != "FAILED" || f.status(t, grandchild) != "FAILED" {
					t.Fatal("terminal reclaim stranded chain dependencies")
				}
			} else if result.Requeued != 1 || f.status(t, root) != "READY" || f.status(t, child) != "WAITING" || f.status(t, grandchild) != "WAITING" {
				t.Fatal("nonterminal reclaim propagated terminal state")
			}
		})
	}
}
func (f *terminalFixture) status(t *testing.T, id int64) string {
	t.Helper()
	var value string
	if err := f.pool.QueryRow(f.ctx, "SELECT status FROM public.task_runs WHERE result_id=$1", id).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func (f *terminalFixture) summary(t *testing.T, id string, expected int, callback int64) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, `INSERT INTO public.workflow_runs(workflow_id,expected_count,callback_result_id,status)VALUES($1::uuid,$2,$3,'RUNNING')`, id, expected, callback)
	if err != nil {
		t.Fatal(err)
	}
}
func (f *terminalFixture) counts(t *testing.T, id string) (int, int, string) {
	t.Helper()
	var success, failure int
	var status string
	if err := f.pool.QueryRow(f.ctx, "SELECT success_count,failure_count,status FROM public.workflow_runs WHERE workflow_id=$1::uuid", id).Scan(&success, &failure, &status); err != nil {
		t.Fatal(err)
	}
	return success, failure, status
}
func (f *terminalFixture) failure(t *testing.T, claim *TaskRun, retry bool) {
	t.Helper()
	_, err := f.service.CompleteFailure(f.ctx, claim.ResultID, "owned-terminal-worker", claim.LeaseToken, json.RawMessage(`{"kind":"owned_failure"}`), retry, time.Now().Add(time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestTerminalDependenciesCloseChainAndKeepNonterminalRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal", true: "retry"}[retry], func(t *testing.T) {
			f := newTerminalFixture(t)
			workflow := f.workflow(t)
			parent := f.node(t, "READY", nil, &workflow, false)
			child := f.node(t, "WAITING", &parent, &workflow, false)
			grandchild := f.node(t, "WAITING", &child, &workflow, false)
			unrelated := f.node(t, "READY", nil, nil, false)
			claim := f.claim(t, parent)
			f.failure(t, claim, retry)
			if retry {
				if f.status(t, parent) != "READY" || f.status(t, child) != "WAITING" || f.status(t, grandchild) != "WAITING" {
					t.Fatal("retry propagated terminal state")
				}
			} else {
				if f.status(t, parent) != "FAILED" || f.status(t, child) != "FAILED" || f.status(t, grandchild) != "FAILED" {
					t.Fatal("terminal subtree stranded")
				}
			}
			if f.status(t, unrelated) != "READY" {
				t.Fatal("unrelated task changed")
			}
			var preserved bool
			if err := f.pool.QueryRow(f.ctx, `SELECT bool_and(canonical_spec_text=$1 AND spec_hash=$2 AND spec_version=1 AND lease_token IS NULL)
   FROM public.task_runs WHERE result_id=ANY($3::bigint[])`, readContractFile(t, "spec_hash.json"), readContractFile(t, "spec_hash.sha256"), f.ids).Scan(&preserved); err != nil || !preserved {
				t.Fatal("source or fencing identity changed", err)
			}
		})
	}
}

func TestExpiryFinalizesChainAndChordBeforeDeletingRoot(t *testing.T) {
	for _, chord := range []bool{false, true} {
		t.Run(map[bool]string{false: "chain", true: "chord"}[chord], func(t *testing.T) {
			f := newTerminalFixture(t)
			workflow := f.workflow(t)
			root := f.node(t, "READY", nil, &workflow, true)
			var dependent int64
			if chord {
				dependent = f.node(t, "WAITING", nil, &workflow, false)
				f.summary(t, workflow, 1, dependent)
			} else {
				dependent = f.node(t, "WAITING", &root, &workflow, false)
			}
			count, err := f.service.PruneExpired(f.ctx, 1, false)
			if err != nil || count != 1 {
				t.Fatalf("expiry deletion: %d %v", count, err)
			}
			if f.status(t, dependent) != "FAILED" {
				t.Fatal("expiry left runnable dependency")
			}
			if chord {
				success, failure, status := f.counts(t, workflow)
				if success != 0 || failure != 1 || status != "FAILED" {
					t.Fatal("expiry did not count header once")
				}
			}
		})
	}
}

func TestRetainedExpiredCallbackDoesNotStarveUnrelatedExpiry(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	header := f.node(t, "READY", nil, &workflow, false)
	claim := f.claim(t, header)
	callback := f.node(t, "WAITING", nil, &workflow, true)
	f.summary(t, workflow, 1, callback)
	other := f.node(t, "READY", nil, nil, true)
	count, err := f.service.PruneExpired(f.ctx, 1, false)
	if err != nil || count != 1 {
		t.Fatalf("retained callback blocked progress: %d %v", count, err)
	}
	if f.status(t, callback) != "FAILED" || f.status(t, header) != "RUNNING" {
		t.Fatal("expired callback or active header state lost")
	}
	var exists bool
	if err := f.pool.QueryRow(f.ctx, "SELECT EXISTS(SELECT 1 FROM public.task_runs WHERE result_id=$1)", other).Scan(&exists); err != nil || exists {
		t.Fatal("unrelated expiry not deleted", err)
	}
	var token string
	if err := f.pool.QueryRow(f.ctx, "SELECT lease_token::text FROM public.task_runs WHERE result_id=$1", header).Scan(&token); err != nil || token != claim.LeaseToken {
		t.Fatal("active header fence changed")
	}
	success, failure, status := f.counts(t, workflow)
	if success != 0 || failure != 0 || status != "FAILED" {
		t.Fatal("callback counted as header")
	}
}

func TestWorkflowCountersIncludeAllNewHeadersAfterFailureAndDoNotRecount(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	first := f.node(t, "READY", nil, &workflow, false)
	second := f.node(t, "READY", nil, &workflow, false)
	third := f.node(t, "READY", nil, &workflow, false)
	callback := f.node(t, "WAITING", nil, &workflow, false)
	f.summary(t, workflow, 3, callback)
	f.failure(t, f.claim(t, first), false)
	secondClaim := f.claim(t, second)
	if err := f.service.CompleteSuccess(f.ctx, second, "owned-terminal-worker", secondClaim.LeaseToken, json.RawMessage(`true`), nil); err != nil {
		t.Fatal(err)
	}
	thirdClaim := f.claim(t, third)
	f.failure(t, thirdClaim, false)
	success, failure, status := f.counts(t, workflow)
	if success != 1 || failure != 2 || status != "FAILED" || f.status(t, callback) != "FAILED" {
		t.Fatalf("wrong counters: %d %d %s", success, failure, status)
	}
	if changed, err := f.service.RequestCancel(f.ctx, third); err != nil || changed != 0 {
		t.Fatal("repeated terminal cancel changed state")
	}
	if _, err := f.service.CompleteFailure(f.ctx, third, "owned-terminal-worker", thirdClaim.LeaseToken, json.RawMessage(`{}`), false, time.Now(), nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("stale completion accepted", err)
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE public.task_runs SET expires_at=clock_timestamp()-interval '1 hour' WHERE result_id=$1", first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PruneExpired(f.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	success, failure, _ = f.counts(t, workflow)
	if success != 1 || failure != 2 {
		t.Fatal("historical root counted twice")
	}
}

func TestTerminalWorkflowRetryRefusesAtomicallyAndReplayDetaches(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	member := f.node(t, "READY", nil, &workflow, false)
	f.failure(t, f.claim(t, member), false)
	ordinary := f.node(t, "READY", nil, nil, false)
	f.failure(t, f.claim(t, ordinary), false)
	if count, err := f.service.RetryFailedTask(f.ctx, member); !errors.Is(err, ErrWorkflowRetry) || count != 0 {
		t.Fatal("workflow in-place retry accepted", err)
	}
	if count, err := f.service.RetryAllFailedTasks(f.ctx); !errors.Is(err, ErrWorkflowRetry) || count != 0 {
		t.Fatal("mixed retry was partial", err)
	}
	if f.status(t, member) != "FAILED" || f.status(t, ordinary) != "FAILED" {
		t.Fatal("mixed refusal mutated members")
	}
	if count, err := f.service.RetryFailedTask(f.ctx, ordinary); err != nil || count != 1 {
		t.Fatal("ordinary retry refused", err)
	}
	replay, err := f.service.Replay(f.ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	f.ids = append(f.ids, replay)
	var detached bool
	if err := f.pool.QueryRow(f.ctx, `SELECT workflow_id IS NULL AND parent_id IS NULL AND wait_count=0 AND canonical_spec_text=$1 AND spec_version=1
  FROM public.task_runs WHERE result_id=$2`, readContractFile(t, "spec_hash.json"), replay).Scan(&detached); err != nil || !detached {
		t.Fatal("replay retained workflow membership", err)
	}
}
