package queue

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func insertPeriodicFixture(t *testing.T, pool *pgxpool.Pool, name, provider, expression string) time.Time {
	t.Helper()
	canonical := readContractFile(t, "periodic_spec.json")
	hash := readContractFile(t, "periodic_spec.sha256")
	if fmt.Sprintf("%x", sha256.Sum256([]byte(canonical))) != hash {
		t.Fatal("SDK-generated periodic fixture identity changed")
	}
	var nominal time.Time
	err := pool.QueryRow(context.Background(), `
	 WITH spec AS(SELECT $1::text AS canonical,$1::jsonb AS payload)
	 INSERT INTO public.periodic_tasks(name,cron_expr,task_path,payload_json,queue_name,priority,max_attempts,
	  concurrency_key,concurrency_limit,next_run_at,canonical_spec_text,canonical_spec_hash,spec_version,scheduler_provider)
	 SELECT $3,$4,payload->>'task_path',jsonb_build_object('args',payload->'args','kwargs',payload->'kwargs'),
	  payload->>'queue_name',(payload->>'priority')::integer,(payload#>>'{exec,max_attempts}')::integer,
	  payload->>'concurrency_key',(payload->>'concurrency_limit')::integer,
	  date_trunc('minute',clock_timestamp())-interval '1 minute',canonical,$2,1,$5 FROM spec
	 RETURNING next_run_at`, canonical, hash, name, expression, provider).Scan(&nominal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM public.task_runs WHERE periodic_name=$1", name)
		_, _ = pool.Exec(context.Background(), "DELETE FROM public.periodic_tasks WHERE name=$1", name)
	})
	return nominal
}

func enqueueOccurrence(t *testing.T, pool *pgxpool.Pool, name string, slot time.Time, provider string) (int64, bool) {
	t.Helper()
	var id int64
	var inserted bool
	if err := pool.QueryRow(context.Background(), "SELECT result_id,inserted FROM public.reproq_enqueue_periodic_occurrence($1,$2,$3)", name, slot, provider).Scan(&id, &inserted); err != nil {
		t.Fatal(err)
	}
	return id, inserted
}

func TestBothProvidersUseOneImmutableSpecAndIdempotentOccurrence(t *testing.T) {
	for _, provider := range []string{"go", "pg_cron"} {
		t.Run(provider, func(t *testing.T) {
			ctx, pool, _, name := ownedFencedQueue(t)
			nominal := insertPeriodicFixture(t, pool, name, provider, "* * * * *")
			id, inserted := enqueueOccurrence(t, pool, name, nominal, provider)
			if !inserted {
				t.Fatal("new occurrence was not inserted")
			}
			var text, hash, actualProvider, backend string
			var version int
			var semantic bool
			if err := pool.QueryRow(ctx, "SELECT canonical_spec_text,spec_hash,periodic_provider,backend_alias,spec_version,spec_json=canonical_spec_text::jsonb FROM public.task_runs WHERE result_id=$1", id).Scan(&text, &hash, &actualProvider, &backend, &version, &semantic); err != nil {
				t.Fatal(err)
			}
			if text != readContractFile(t, "periodic_spec.json") || hash != readContractFile(t, "periodic_spec.sha256") || actualProvider != provider || backend != "default" || version != 1 || !semantic {
				t.Fatal("provider reserialized or lost canonical task provenance")
			}
			if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET status='SUCCESSFUL' WHERE result_id=$1", id); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "UPDATE public.periodic_tasks SET enabled=false,scheduler_provider='unassigned' WHERE name=$1", name); err != nil {
				t.Fatal(err)
			}
			duplicate, inserted := enqueueOccurrence(t, pool, name, nominal, provider)
			if inserted || duplicate != id {
				t.Fatal("historical occurrence identity was lost after completion/provider handover")
			}
		})
	}
}

func TestPeriodicHashDoesNotCoalesceOverlappingOccurrences(t *testing.T) {
	ctx, pool, _, name := ownedFencedQueue(t)
	nominal := insertPeriodicFixture(t, pool, name, "go", "* * * * *")
	first, inserted := enqueueOccurrence(t, pool, name, nominal, "go")
	if !inserted {
		t.Fatal("first occurrence missing")
	}
	previous := nominal.Add(-time.Minute)
	if _, err := pool.Exec(ctx, "UPDATE public.periodic_tasks SET next_run_at=$1 WHERE name=$2", previous, name); err != nil {
		t.Fatal(err)
	}
	second, inserted := enqueueOccurrence(t, pool, name, previous, "go")
	if !inserted || first == second {
		t.Fatal("overlapping occurrences were deduplicated by definition hash")
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.task_runs WHERE periodic_name=$1 AND status='READY' AND spec_hash=$2", name, readContractFile(t, "periodic_spec.sha256")).Scan(&count); err != nil || count != 2 {
		t.Fatal("canonical hash no longer identifies both distinct occurrences")
	}
}

func TestConcurrentProvidersCallsKeepOneOccurrenceAcrossAllStatuses(t *testing.T) {
	for _, provider := range []string{"go", "pg_cron"} {
		t.Run(provider, func(t *testing.T) {
			ctx, pool, _, name := ownedFencedQueue(t)
			nominal := insertPeriodicFixture(t, pool, name, provider, "* * * * *")
			type result struct {
				id       int64
				inserted bool
				err      error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			for range 2 {
				go func() {
					<-start
					var r result
					r.err = pool.QueryRow(ctx, "SELECT result_id,inserted FROM public.reproq_enqueue_periodic_occurrence($1,$2,$3)", name, nominal, provider).Scan(&r.id, &r.inserted)
					results <- r
				}()
			}
			close(start)
			first, second := <-results, <-results
			if first.err != nil || second.err != nil || first.id != second.id || first.inserted == second.inserted {
				t.Fatalf("duplicate occurrence race: %+v %+v", first, second)
			}
			for _, status := range []string{"FAILED", "CANCELLED", "SUCCESSFUL"} {
				if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET status=$1 WHERE result_id=$2", status, first.id); err != nil {
					t.Fatal(err)
				}
				id, inserted := enqueueOccurrence(t, pool, name, nominal, provider)
				if inserted || id != first.id {
					t.Fatal("terminal occurrence was recreated")
				}
			}
		})
	}
}

func TestPeriodicValidationNeverAdvancesOrInsertsAnInvalidDefinition(t *testing.T) {
	for name, statement := range map[string]string{
		"payload":    "UPDATE public.periodic_tasks SET payload_json='{}' WHERE name=$1",
		"projection": "UPDATE public.periodic_tasks SET concurrency_limit=100 WHERE name=$1",
		"hash":       "UPDATE public.periodic_tasks SET canonical_spec_hash=repeat('0',64) WHERE name=$1",
		"legacy":     "UPDATE public.periodic_tasks SET spec_version=0 WHERE name=$1",
		"disabled":   "UPDATE public.periodic_tasks SET enabled=false WHERE name=$1",
		"owner":      "UPDATE public.periodic_tasks SET scheduler_provider='pg_cron' WHERE name=$1",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, pool, service, key := ownedFencedQueue(t)
			nominal := insertPeriodicFixture(t, pool, key, "go", "* * * * *")
			if _, err := pool.Exec(ctx, statement, key); err != nil {
				t.Fatal(err)
			}
			var id int64
			var inserted bool
			if err := pool.QueryRow(ctx, "SELECT result_id,inserted FROM public.reproq_enqueue_periodic_occurrence($1,$2,'go')", key, nominal).Scan(&id, &inserted); err == nil {
				t.Fatal("invalid periodic source accepted")
			}
			if name != "disabled" && name != "owner" {
				if count, err := service.EnqueueDuePeriodicTasks(ctx); err == nil || count != 0 {
					t.Fatal("invalid Go definition was advanced")
				}
			}
			var unchanged bool
			if err := pool.QueryRow(ctx, "SELECT next_run_at=$1 AND last_run_at IS NULL AND NOT EXISTS(SELECT 1 FROM public.task_runs WHERE periodic_name=$2) FROM public.periodic_tasks WHERE name=$2", nominal, key).Scan(&unchanged); err != nil || !unchanged {
				t.Fatal("invalid definition changed schedule/task state")
			}
		})
	}
}

func TestGoBeatSelectsOnlyItsProviderAndCoalescesFromDatabaseClock(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	nominal := insertPeriodicFixture(t, pool, name, "go", "CRON_TZ=America/Chicago 0 0 * * *")
	other := name + "-pgcron"
	otherTime := insertPeriodicFixture(t, pool, other, "pg_cron", "* * * * *")
	if count, err := service.EnqueueDuePeriodicTasks(ctx); err != nil || count != 1 {
		t.Fatalf("Go scheduling failed: %d %v", count, err)
	}
	var advanced, otherUntouched, nominalKept bool
	if err := pool.QueryRow(ctx, "SELECT next_run_at>clock_timestamp(),last_run_at=$1 FROM public.periodic_tasks WHERE name=$2", nominal, name).Scan(&advanced, &nominalKept); err != nil || !advanced || !nominalKept {
		t.Fatal("Go nominal identity or next occurrence lost")
	}
	if err := pool.QueryRow(ctx, "SELECT next_run_at=$1 AND last_run_at IS NULL FROM public.periodic_tasks WHERE name=$2", otherTime, other).Scan(&otherUntouched); err != nil || !otherUntouched {
		t.Fatal("Go beat mutated another provider")
	}
	if count, err := service.EnqueueDuePeriodicTasks(ctx); err != nil || count != 0 {
		t.Fatal("Go repeated an already advanced occurrence")
	}
	var spec json.RawMessage
	if err := pool.QueryRow(ctx, "SELECT spec_json FROM public.task_runs WHERE periodic_name=$1", name).Scan(&spec); err != nil {
		t.Fatal(err)
	}
	if value, err := readSpecJSON(spec); err != nil || !equalJSON(value, mustReadPeriodicJSON(t)) {
		t.Fatal("Go-generated occurrence mutated SDK args/provenance")
	}
}

func TestOccurrencePrimitiveRejectsInvalidTimesAndAmbiguousCanonicalJSON(t *testing.T) {
	for _, invalid := range []string{"future", "nonminute", "duplicate", "nested-duplicate", "deep"} {
		t.Run(invalid, func(t *testing.T) {
			ctx, pool, _, name := ownedFencedQueue(t)
			nominal := insertPeriodicFixture(t, pool, name, "pg_cron", "* * * * *")
			slot := nominal
			want := ""
			switch invalid {
			case "future":
				slot = nominal.Add(24 * time.Hour)
				want = "future periodic occurrence"
			case "nonminute":
				slot = nominal.Add(time.Second)
				want = "UTC trigger minute"
			default:
				canonical := readContractFile(t, "periodic_spec.json")
				switch invalid {
				case "duplicate":
					canonical = `{"v":1,` + canonical[1:]
				case "nested-duplicate":
					canonical = strings.Replace(canonical, `"kwargs":{`, `"kwargs":{"zero":0,`, 1)
				case "deep":
					canonical = strings.Replace(canonical, `"kwargs":{`, `"kwargs":{"deep":`+strings.Repeat("[", 65)+"0"+strings.Repeat("]", 65)+",", 1)
				}
				if _, err := pool.Exec(ctx, `UPDATE public.periodic_tasks SET canonical_spec_text=$1,
                  canonical_spec_hash=encode(sha256(convert_to($1,'UTF8')),'hex'),
                  payload_json=jsonb_build_object('args',$1::jsonb->'args','kwargs',$1::jsonb->'kwargs') WHERE name=$2`, canonical, name); err != nil {
					t.Fatal(err)
				}
				want = "duplicate keys or excessive nesting"
			}
			var id int64
			var inserted bool
			err := pool.QueryRow(ctx, "SELECT result_id,inserted FROM public.reproq_enqueue_periodic_occurrence($1,$2,'pg_cron')", name, slot).Scan(&id, &inserted)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("invalid occurrence was not rejected at its contract boundary: %v", err)
			}
			var unchanged bool
			if err := pool.QueryRow(ctx, "SELECT next_run_at=$1 AND last_run_at IS NULL AND NOT EXISTS(SELECT 1 FROM public.task_runs WHERE periodic_name=$2) FROM public.periodic_tasks WHERE name=$2", nominal, name).Scan(&unchanged); err != nil || !unchanged {
				t.Fatal("rejected occurrence mutated schedule or queue")
			}
		})
	}
}

func mustReadPeriodicJSON(t *testing.T) any {
	t.Helper()
	value, err := readSpecJSON([]byte(readContractFile(t, "periodic_spec.json")))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
