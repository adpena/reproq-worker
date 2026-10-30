package queue

import (
	"context"
	"testing"
	"time"
)

func TestReclaimOptionsKeepExpiryAgeAndLimitFencesAndDryRunIsReadOnly(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	ids := []int64{}
	for ordinal := range 3 {
		id := insertFencedFixture(t, pool, name, ordinal, nil, nil, 0)
		task, err := service.Claim(ctx, "reclaim-owner", name, 60, 0)
		if err != nil || task.ResultID != id {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()-interval '2 hours' WHERE result_id=$1", ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()-interval '30 minutes' WHERE result_id=$1", ids[1]); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := pool.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(t) ORDER BY result_id)::text FROM public.task_runs t WHERE result_id=ANY($1::bigint[])", ids).Scan(&before); err != nil {
		t.Fatal(err)
	}
	result, err := service.ReclaimWithOptions(ctx, ReclaimOptions{Action: "requeue", OlderThan: time.Hour, Limit: 1, DryRun: true, IncludeNullLease: true})
	if err != nil || result.Count != 1 || !result.DryRun {
		t.Fatalf("bad dry-run result: %+v %v", result, err)
	}
	var after string
	if err := pool.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(t) ORDER BY result_id)::text FROM public.task_runs t WHERE result_id=ANY($1::bigint[])", ids).Scan(&after); err != nil || after != before {
		t.Fatal("dry run changed task/lease metadata")
	}
	result, err = service.ReclaimWithOptions(ctx, ReclaimOptions{Action: "requeue", OlderThan: time.Hour, Limit: 1})
	if err != nil || result.Count != 1 || result.Requeued != 1 {
		t.Fatalf("bad limited reclaim: %+v %v", result, err)
	}
	var exact bool
	if err := pool.QueryRow(ctx, `SELECT
	 (SELECT status='READY' AND lease_token IS NULL AND leased_until IS NULL AND leased_by IS NULL FROM public.task_runs WHERE result_id=$1)
	 AND(SELECT status='RUNNING' AND lease_token IS NOT NULL FROM public.task_runs WHERE result_id=$2)
	 AND(SELECT status='RUNNING' AND leased_until>clock_timestamp() FROM public.task_runs WHERE result_id=$3)`, ids[0], ids[1], ids[2]).Scan(&exact); err != nil || !exact {
		t.Fatal("limited reclaim dropped an age/status/ownership fence")
	}
}

func TestReclaimedTerminalMemberUsesTheSameWorkflowFinalizationAsCompletion(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	member := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
	callback := insertFencedFixture(t, pool, name, 1, nil, nil, 0)
	var workflow string
	if err := pool.QueryRow(ctx, "INSERT INTO public.workflow_runs(workflow_id,expected_count,callback_result_id) VALUES(gen_random_uuid(),1,$1) RETURNING workflow_id::text", callback).Scan(&workflow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM public.workflow_runs WHERE workflow_id=$1", workflow)
	})
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET workflow_id=$1,max_attempts=1 WHERE result_id=$2", workflow, member); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET workflow_id=$1,status='WAITING',wait_count=1 WHERE result_id=$2", workflow, callback); err != nil {
		t.Fatal(err)
	}
	task, err := service.Claim(ctx, "workflow-reclaim", name, 60, 0)
	if err != nil || task.ResultID != member {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()-interval '1 minute' WHERE result_id=$1", member); err != nil {
		t.Fatal(err)
	}
	result, err := service.ReclaimWithOptions(ctx, ReclaimOptions{Action: "requeue"})
	if err != nil || result.Failed < 1 {
		t.Fatalf("exhausted reclaim did not terminalize member: %+v %v", result, err)
	}
	var finalized bool
	if err := pool.QueryRow(ctx, `SELECT
	 (SELECT status='FAILED' AND lease_token IS NULL FROM public.task_runs WHERE result_id=$1)
	 AND(SELECT status='FAILED' AND wait_count=0 FROM public.task_runs WHERE result_id=$2)
	 AND(SELECT status='FAILED' AND failure_count=1 FROM public.workflow_runs WHERE workflow_id=$3)`, member, callback, workflow).Scan(&finalized); err != nil || !finalized {
		t.Fatal("reclaim left a workflow or callback waiting forever")
	}
}

func TestExpiredCancellationIsNeverRequeued(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	id := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
	if _, err := service.Claim(ctx, "cancel-reclaimer", name, 60, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequestCancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()-interval '1 minute' WHERE result_id=$1", id); err != nil {
		t.Fatal(err)
	}
	result, err := service.ReclaimWithOptions(ctx, ReclaimOptions{Action: "requeue"})
	if err != nil || result.Cancelled < 1 {
		t.Fatalf("expired cancellation was requeued: %+v %v", result, err)
	}
	var cancelled bool
	if err := pool.QueryRow(ctx, "SELECT status='CANCELLED' AND lease_token IS NULL AND leased_until IS NULL AND leased_by IS NULL FROM public.task_runs WHERE result_id=$1", id).Scan(&cancelled); err != nil || !cancelled {
		t.Fatal("cancelled claim remains writable")
	}
}
