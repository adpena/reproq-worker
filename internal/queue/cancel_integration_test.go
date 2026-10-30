package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func cancellationCounter(t *testing.T, outcome string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "reproq_cancellation_transitions_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "outcome" && label.GetValue() == outcome {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestPendingCancellationRetiresCurrentAndLegacyWithoutRewritingIdentity(t *testing.T) {
	for _, status := range []string{"READY", "WAITING"} {
		for _, version := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/version%d", status, version), func(t *testing.T) {
				ctx, pool, service, name := ownedFencedQueue(t)
				id := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
				if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET status=$1,spec_version=$2,wait_count=1,leased_by='retained-old-owner',leased_until=clock_timestamp()-interval '1 day' WHERE result_id=$3", status, version, id); err != nil {
					t.Fatal(err)
				}
				var identity string
				if err := pool.QueryRow(ctx, "SELECT jsonb_build_array(spec_json,spec_hash,spec_version,canonical_spec_text,attempts)::text FROM public.task_runs WHERE result_id=$1", id).Scan(&identity); err != nil {
					t.Fatal(err)
				}
				before := cancellationCounter(t, "cancelled")
				if changed, err := service.RequestCancel(ctx, id); err != nil || changed != 1 {
					t.Fatalf("pending cancellation failed: %d %v", changed, err)
				}
				var valid bool
				var afterIdentity, row string
				if err := pool.QueryRow(ctx, `SELECT status='CANCELLED' AND cancel_requested AND lease_token IS NULL
                  AND leased_by IS NULL AND leased_until IS NULL AND run_after IS NULL AND wait_count=0
                  AND finished_at IS NOT NULL AND failed_at IS NOT NULL AND jsonb_array_length(errors_json)=1,
                  jsonb_build_array(spec_json,spec_hash,spec_version,canonical_spec_text,attempts)::text,to_jsonb(tr)::text
                  FROM public.task_runs tr WHERE result_id=$1`, id).Scan(&valid, &afterIdentity, &row); err != nil || !valid || identity != afterIdentity {
					t.Fatal("pending cancellation rewrote identity, spent an attempt or retained a claim")
				}
				if changed, err := service.RequestCancel(ctx, id); err != nil || changed != 0 {
					t.Fatal("repeated pending cancellation was not idempotent")
				}
				var second string
				if err := pool.QueryRow(ctx, "SELECT to_jsonb(tr)::text FROM public.task_runs tr WHERE result_id=$1", id).Scan(&second); err != nil || row != second {
					t.Fatal("terminal cancellation changed history on repetition")
				}
				if cancellationCounter(t, "cancelled")-before != 1 {
					t.Fatal("cancellation metric did not follow exactly one committed transition")
				}
			})
		}
	}
}

func TestPendingCancellationFinalizesWorkflowOnce(t *testing.T) {
	for _, status := range []string{"READY", "WAITING"} {
		t.Run(status, func(t *testing.T) {
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
			if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET workflow_id=$1,status=$2,spec_version=0 WHERE result_id=$3", workflow, status, member); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET workflow_id=$1,status='WAITING',wait_count=1,leased_by='stale-owner',leased_until=clock_timestamp()-interval '1 day' WHERE result_id=$2", workflow, callback); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan int64, 2)
			errors := make(chan error, 2)
			for range 2 {
				go func() { <-start; changed, err := service.RequestCancel(ctx, member); results <- changed; errors <- err }()
			}
			close(start)
			if total := <-results + <-results; total != 1 {
				t.Fatalf("concurrent cancellation finalized %d times", total)
			}
			for range 2 {
				if err := <-errors; err != nil {
					t.Fatal(err)
				}
			}
			var valid bool
			if err := pool.QueryRow(ctx, `SELECT
              (SELECT status='CANCELLED' AND spec_version=0 AND attempts=0 FROM public.task_runs WHERE result_id=$1)
              AND(SELECT status='FAILED' AND wait_count=0 AND leased_by IS NULL AND leased_until IS NULL AND lease_token IS NULL AND jsonb_array_length(errors_json)=1 FROM public.task_runs WHERE result_id=$2)
              AND(SELECT status='FAILED' AND failure_count=1 FROM public.workflow_runs WHERE workflow_id=$3)`, member, callback, workflow).Scan(&valid); err != nil || !valid {
				t.Fatal("pending cancellation duplicated or stranded workflow finalization")
			}
		})
	}
}

func TestCancellationLeavesTerminalHistoryUntouched(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	for ordinal, status := range []string{"SUCCESSFUL", "FAILED", "CANCELLED"} {
		id := insertFencedFixture(t, pool, name, ordinal, nil, nil, 0)
		if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET status=$1,spec_version=0 WHERE result_id=$2", status, id); err != nil {
			t.Fatal(err)
		}
		var before, after string
		if err := pool.QueryRow(ctx, "SELECT to_jsonb(tr)::text FROM public.task_runs tr WHERE result_id=$1", id).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if changed, err := service.RequestCancel(ctx, id); err != nil || changed != 0 {
			t.Fatal("terminal cancellation was not a no-op")
		}
		if err := pool.QueryRow(ctx, "SELECT to_jsonb(tr)::text FROM public.task_runs tr WHERE result_id=$1", id).Scan(&after); err != nil || before != after {
			t.Fatal("terminal cancellation rewrote historical state")
		}
	}
}

func TestCancellationRacesWithClaimAndCompletionRemainFenced(t *testing.T) {
	for _, race := range []string{"claim", "success"} {
		t.Run(race, func(t *testing.T) {
			ctx, pool, service, name := ownedFencedQueue(t)
			for iteration := range 12 {
				id := insertFencedFixture(t, pool, name, iteration, nil, nil, 0)
				var owned *TaskRun
				var err error
				if race == "success" {
					owned, err = service.Claim(ctx, "racing-owner", name, 60, 0)
					if err != nil {
						t.Fatal(err)
					}
				}
				start := make(chan struct{})
				cancelResult := make(chan error, 1)
				operationResult := make(chan error, 1)
				go func() { <-start; _, err := service.RequestCancel(ctx, id); cancelResult <- err }()
				go func() {
					<-start
					if race == "claim" {
						owned, err = service.Claim(ctx, "racing-owner", name, 60, 0)
					} else {
						err = service.CompleteSuccess(ctx, id, "racing-owner", owned.LeaseToken, json.RawMessage(`7`), nil)
					}
					operationResult <- err
				}()
				close(start)
				if err := <-cancelResult; err != nil {
					t.Fatal(err)
				}
				err = <-operationResult
				if err != nil && !errors.Is(err, ErrNoTasks) && !errors.Is(err, ErrCancelRequested) {
					t.Fatal(err)
				}
				var status string
				if err := pool.QueryRow(ctx, "SELECT status FROM public.task_runs WHERE result_id=$1", id).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status == "RUNNING" {
					if err := service.CompleteSuccess(ctx, id, "racing-owner", owned.LeaseToken, json.RawMessage(`7`), nil); !errors.Is(err, ErrCancelRequested) {
						t.Fatal("cancellation lost a pending acknowledgment race")
					}
					if status, err := service.CompleteFailure(ctx, id, "racing-owner", owned.LeaseToken, json.RawMessage(`{"kind":"cancelled"}`), false, time.Now(), nil); err != nil || status != StatusCancelled {
						t.Fatal("cooperative cancellation did not finalize")
					}
				}
				var valid bool
				if err := pool.QueryRow(ctx, "SELECT status IN('CANCELLED','SUCCESSFUL') AND lease_token IS NULL AND leased_by IS NULL AND leased_until IS NULL AND attempts<=1 FROM public.task_runs WHERE result_id=$1", id).Scan(&valid); err != nil || !valid {
					t.Fatal("claim/completion race left an unfenced or nonterminal outcome")
				}
			}
		})
	}
}
