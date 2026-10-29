package queue

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reproq-worker/internal/testdatabase"
	"testing"
	"time"

	"reproq-worker/internal/db"

	"github.com/jackc/pgx/v5/pgxpool"
)

func ownedFencedQueue(t *testing.T) (context.Context, *pgxpool.Pool, *Service, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := db.NewPool(ctx, testdatabase.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := fmt.Sprintf("fenced-%d-%s", time.Now().UnixNano(), t.Name())
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM public.task_runs WHERE queue_name=$1", name); err != nil {
			t.Error(err)
		}
	})
	return ctx, pool, newTestService(t, pool), name
}

func insertFencedFixture(t *testing.T, pool *pgxpool.Pool, queue string, ordinal int, lock, key *string, limit int) int64 {
	t.Helper()
	var id int64
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(queue, ordinal))))
	err := pool.QueryRow(context.Background(), `INSERT INTO public.task_runs(spec_version,spec_hash,queue_name,spec_json,lock_key,concurrency_key,concurrency_limit)
	 VALUES (1,$1,$2,'{"task_path":"owned.tasks.task"}',$3,$4,$5) RETURNING result_id`, hash, queue, lock, key, limit).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFreshLeaseTokenRejectsSameWorkerStaleAttemptAndExpiredWrites(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	id := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
	first, err := service.Claim(ctx, "same-worker", name, 60, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResultID != id || first.LeaseToken == "" {
		t.Fatal("missing claimed identity")
	}
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET leased_until=clock_timestamp()-interval '1 second' WHERE result_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(ctx, id, "same-worker", first.LeaseToken, 60); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease renewed: %v", err)
	}
	if err := service.CompleteSuccess(ctx, id, "same-worker", first.LeaseToken, json.RawMessage(`"old"`), nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired success accepted: %v", err)
	}
	if _, err := service.Reclaim(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := service.Claim(ctx, "same-worker", name, 60, 60)
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseToken == second.LeaseToken || second.Attempts != 2 {
		t.Fatal("claim identity was reused")
	}
	if _, err := service.Heartbeat(ctx, id, "same-worker", first.LeaseToken, 60); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale heartbeat accepted: %v", err)
	}
	if _, err := service.CompleteFailure(ctx, id, "same-worker", first.LeaseToken, json.RawMessage(`{"message":"stale"}`), false, time.Now(), nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale failure accepted: %v", err)
	}
	if err := service.CompleteSuccess(ctx, id, "same-worker", first.LeaseToken, json.RawMessage(`"old"`), nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale success accepted: %v", err)
	}
	if err := service.CompleteSuccess(ctx, id, "same-worker", second.LeaseToken, json.RawMessage(`"current"`), nil); err != nil {
		t.Fatal(err)
	}
	var status, value string
	var cleared bool
	if err := pool.QueryRow(ctx, "SELECT status,return_json::text,lease_token IS NULL AND leased_by IS NULL AND leased_until IS NULL FROM public.task_runs WHERE result_id=$1", id).Scan(&status, &value, &cleared); err != nil {
		t.Fatal(err)
	}
	if status != "SUCCESSFUL" || value != `"current"` || !cleared {
		t.Fatal("new owner result or lease cleanup lost")
	}
}

func TestManualRetryDoesNotReviveOldClaimAtTheSameAttemptNumber(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	id := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET max_attempts=1 WHERE result_id=$1", id); err != nil {
		t.Fatal(err)
	}
	first, err := service.Claim(ctx, "reused-worker", name, 60, 0)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := service.CompleteFailure(ctx, id, "reused-worker", first.LeaseToken, json.RawMessage(`{"message":"first"}`), true, time.Now(), nil); err != nil || status != StatusFailed {
		t.Fatalf("stored budget ignored: %s %v", status, err)
	}
	if changed, err := service.RetryFailedTask(ctx, id); err != nil || changed != 1 {
		t.Fatalf("manual retry failed: %v", err)
	}
	second, err := service.Claim(ctx, "reused-worker", name, 60, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Attempts != second.Attempts || first.LeaseToken == second.LeaseToken {
		t.Fatal("retry fixture did not isolate resettable attempt identity")
	}
	if err := service.CompleteSuccess(ctx, id, "reused-worker", first.LeaseToken, json.RawMessage(`"stale"`), nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("manual retry revived stale claim: %v", err)
	}
	if err := service.CompleteSuccess(ctx, id, "reused-worker", second.LeaseToken, json.RawMessage(`"fresh"`), nil); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationWinsTheCompletionRaceAndClearsClaim(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	id := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
	task, err := service.Claim(ctx, "cancel-worker", name, 60, 0)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := service.RequestCancel(ctx, id); err != nil || changed != 1 {
		t.Fatal(err)
	}
	if cancelled, err := service.Heartbeat(ctx, id, "cancel-worker", task.LeaseToken, 60); err != nil || !cancelled {
		t.Fatalf("cancellation not signaled: %v", err)
	}
	if err := service.CompleteSuccess(ctx, id, "cancel-worker", task.LeaseToken, json.RawMessage(`"late success"`), nil); !errors.Is(err, ErrCancelRequested) {
		t.Fatalf("canceled success accepted: %v", err)
	}
	if status, err := service.CompleteFailure(ctx, id, "cancel-worker", task.LeaseToken, json.RawMessage(`{"kind":"cancelled"}`), true, time.Now(), nil); err != nil || status != StatusCancelled {
		t.Fatalf("cancellation requeued: %s %v", status, err)
	}
	var cleared bool
	if err := pool.QueryRow(ctx, "SELECT lease_token IS NULL AND leased_by IS NULL AND leased_until IS NULL AND run_after IS NULL FROM public.task_runs WHERE result_id=$1", id).Scan(&cleared); err != nil || !cleared {
		t.Fatal("cancelled claim retained lease")
	}
}

func TestConcurrentClaimsRespectSharedKeysWithBothAgingModes(t *testing.T) {
	for _, aging := range []float64{0, 60} {
		for _, mode := range []string{"lock", "concurrency", "mixed-zero-cap"} {
			t.Run(fmt.Sprintf("%s/aging%g", mode, aging), func(t *testing.T) {
				ctx, pool, service, name := ownedFencedQueue(t)
				for iteration := range 12 {
					key := fmt.Sprintf("%s-%d", name, iteration)
					var lock, concurrency *string
					if mode == "lock" {
						lock = &key
					} else {
						concurrency = &key
					}
					for ordinal := range 2 {
						limit := 1
						if mode == "mixed-zero-cap" && ordinal == 0 {
							limit = 0
						}
						insertFencedFixture(t, pool, name, iteration*2+ordinal, lock, concurrency, limit)
					}
					start := make(chan struct{})
					results := make(chan error, 2)
					for worker := range 2 {
						go func() {
							<-start
							_, err := service.Claim(ctx, fmt.Sprint("worker-", worker), name, 60, aging)
							results <- err
						}()
					}
					close(start)
					claimed := 0
					for range 2 {
						err := <-results
						if err == nil {
							claimed++
						} else if !errors.Is(err, ErrNoTasks) {
							t.Fatal(err)
						}
					}
					if claimed != 1 {
						t.Fatalf("same-key simultaneous claims=%d, want1", claimed)
					}
					if _, err := pool.Exec(ctx, "DELETE FROM public.task_runs WHERE queue_name=$1", name); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestSharedKeyUsesStrongestActiveOrCandidateConcurrencyCap(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		active    []int
		candidate int
		canStart  bool
	}{
		{"active-one-candidate-two", []int{1}, 2, false},
		{"active-one-candidate-zero", []int{1}, 0, false},
		{"active-two-candidate-one", []int{2}, 1, false},
		{"active-zero-candidate-one", []int{0}, 1, false},
		{"active-zero-candidate-two", []int{0}, 2, true},
		{"all-zero-unbounded", []int{0, 0}, 0, true},
		{"active-two-candidate-zero-room", []int{2}, 0, true},
		{"active-two-candidate-zero-full", []int{2, 0}, 0, false},
		{"lowering-cap-with-two-active", []int{0, 2}, 1, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, pool, service, name := ownedFencedQueue(t)
			key := name + "-key"
			active := []*TaskRun{}
			for ordinal, limit := range scenario.active {
				insertFencedFixture(t, pool, name, ordinal, nil, &key, limit)
				task, err := service.Claim(ctx, "active-worker", name, 60, 0)
				if err != nil {
					t.Fatal("cannot prepare compositional active constraint:", err)
				}
				active = append(active, task)
			}
			id := insertFencedFixture(t, pool, name, len(active), nil, &key, scenario.candidate)
			task, err := service.Claim(ctx, "candidate-worker", name, 60, 0)
			if scenario.canStart {
				if err != nil || task.ResultID != id {
					t.Fatalf("available strongest cap refused candidate: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrNoTasks) || task != nil {
				t.Fatalf("candidate bypassed the strongest active/candidate constraint: %+v %v", task, err)
			}
			for ordinal, member := range active {
				if err := service.CompleteSuccess(ctx, member.ResultID, "active-worker", member.LeaseToken, json.RawMessage(`null`), nil); err != nil {
					t.Fatal(err)
				}
				if scenario.candidate == 1 && ordinal < len(active)-1 {
					if _, err := service.Claim(ctx, "candidate-worker", name, 60, 0); !errors.Is(err, ErrNoTasks) {
						t.Fatal("lowered capacity started before enough active members finished")
					}
				}
			}
			if task, err := service.Claim(ctx, "candidate-worker", name, 60, 0); err != nil || task.ResultID != id {
				t.Fatalf("candidate remained blocked after active constraints cleared: %v", err)
			}
		})
	}
}

func TestContendedKeyDoesNotBlockAnUnrelatedClaim(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	blocked, other := name+"-blocked", name+"-other"
	insertFencedFixture(t, pool, name, 0, nil, &blocked, 1)
	want := insertFencedFixture(t, pool, name, 1, nil, &other, 1)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(pg_catalog.hashtextextended($1,0))", "reproq:concurrency:"+blocked); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	task, err := service.Claim(bounded, "independent-worker", name, 60, 0)
	if err != nil || task.ResultID != want {
		t.Fatalf("unrelated claim blocked by key lock: %v", err)
	}
}

func TestActualConnectionRejectsMarkerLedgerAndRelationShadowing(t *testing.T) {
	ctx, pool, _, _ := ownedFencedQueue(t)
	for _, statement := range []string{
		"UPDATE public.reproq_schema_contract SET version=1",
		"DELETE FROM public.django_migrations WHERE app='reproq_django' AND name='0002_schema_contract_v1'",
		"WITH missing AS(DELETE FROM public.django_migrations WHERE app='reproq_django' AND name='0003_periodic_enqueue_v1') INSERT INTO public.django_migrations(app,name,applied) SELECT app,name,applied FROM public.django_migrations WHERE app='reproq_django' AND name='0002_schema_contract_v1'",
		"WITH missing AS(DELETE FROM public.django_migrations WHERE app='reproq_django' AND name='0002_schema_contract_v1') INSERT INTO public.django_migrations(app,name,applied) SELECT app,name,applied FROM public.django_migrations WHERE app='reproq_django' AND name='0003_periodic_enqueue_v1'",
		"CREATE TEMP TABLE task_runs(id integer) ON COMMIT DROP",
		"SET LOCAL search_path=pg_catalog",
		"ALTER FUNCTION public.reproq_enqueue_periodic_occurrence(text,timestamp with time zone,text) SECURITY DEFINER",
		"ALTER FUNCTION public.reproq_enqueue_periodic_occurrence(text,timestamp with time zone,text) SET search_path=public,pg_catalog",
	} {
		t.Run(statement, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
			if err := db.RequireSchemaContract(ctx, tx); !errors.Is(err, db.ErrSchemaContract) {
				t.Fatalf("uncertified connection accepted: %v", err)
			}
		})
	}
	if err := db.RequireSchemaContract(ctx, pool); err != nil {
		t.Fatal("negative probes escaped rollback:", err)
	}
}
