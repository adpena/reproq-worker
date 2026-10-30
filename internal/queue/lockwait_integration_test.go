package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestLeaseFencesRecheckAfterWaitingForUnchangedRowLock(t *testing.T) {
	for _, operation := range []string{"heartbeat", "success", "failure"} {
		t.Run(operation, func(t *testing.T) {
			ctx, pool, service, name := ownedFencedQueue(t)
			id := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
			task, err := service.Claim(ctx, "lockwait-owner", name, 60, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()+interval '1 second' WHERE result_id=$1", id); err != nil {
				t.Fatal(err)
			}
			var before string
			if err := pool.QueryRow(ctx, "SELECT to_jsonb(tr)::text FROM public.task_runs tr WHERE result_id=$1", id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			locker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Rollback(context.Background())
			var blocker int
			if err := locker.QueryRow(ctx, "SELECT pg_backend_pid() FROM public.task_runs WHERE result_id=$1 FOR UPDATE", id).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "heartbeat":
					_, err = service.Heartbeat(ctx, id, "lockwait-owner", task.LeaseToken, 60)
				case "success":
					err = service.CompleteSuccess(ctx, id, "lockwait-owner", task.LeaseToken, json.RawMessage(`7`), nil)
				case "failure":
					_, err = service.CompleteFailure(ctx, id, "lockwait-owner", task.LeaseToken, json.RawMessage(`{"kind":"owned"}`), false, time.Now(), nil)
				}
				finished <- err
			}()
			blocked := false
			for deadline := time.Now().Add(750 * time.Millisecond); time.Now().Before(deadline); {
				if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND $1=ANY(pg_catalog.pg_blocking_pids(pid)))", blocker).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !blocked {
				t.Fatal("test never observed the mutator waiting on the unchanged row lock")
			}
			for {
				var expired bool
				if err := pool.QueryRow(ctx, "SELECT leased_until<=clock_timestamp() FROM public.task_runs WHERE result_id=$1", id).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := locker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-finished; !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("expired claim wrote after an unchanged row-lock wait: %v", err)
			}
			var after string
			if err := pool.QueryRow(ctx, "SELECT to_jsonb(tr)::text FROM public.task_runs tr WHERE result_id=$1", id).Scan(&after); err != nil || before != after {
				t.Fatal("expired writer mutated task/lease/result history")
			}
		})
	}
}
