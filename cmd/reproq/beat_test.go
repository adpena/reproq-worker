package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"reproq-worker/internal/db"
	"reproq-worker/internal/testdatabase"
)

func TestBeatOnceReportsRejectedScheduleAndLeavesItsStateUntouched(t *testing.T) {
	dsn := testdatabase.URL(t)
	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	name := fmt.Sprintf("cli-beat-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM public.task_runs WHERE periodic_name=$1", name)
		_, _ = pool.Exec(ctx, "DELETE FROM public.periodic_tasks WHERE name=$1", name)
	})
	raw, err := os.ReadFile("../../contracts/periodic_spec.json")
	if err != nil {
		t.Fatal(err)
	}
	canonical := strings.TrimSuffix(string(raw), "\n")
	sidecar, err := os.ReadFile("../../contracts/periodic_spec.sha256")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(sidecar))
	if fmt.Sprintf("%x", sha256.Sum256([]byte(canonical))) != hash {
		t.Fatal("SDK-produced periodic fixture bytes or checksum changed")
	}
	_, err = pool.Exec(ctx, `WITH spec AS(SELECT $1::text canonical,$1::jsonb payload)
      INSERT INTO public.periodic_tasks(name,cron_expr,task_path,payload_json,queue_name,priority,max_attempts,
        concurrency_key,concurrency_limit,next_run_at,canonical_spec_text,canonical_spec_hash,spec_version,scheduler_provider)
      SELECT $3,'* * * * *',payload->>'task_path',jsonb_build_object('args',payload->'args','kwargs',payload->'kwargs'),
        payload->>'queue_name',(payload->>'priority')::integer,(payload#>>'{exec,max_attempts}')::integer,
        payload->>'concurrency_key',(payload->>'concurrency_limit')::integer,
        date_trunc('minute',clock_timestamp())-interval '1 minute',canonical,$2,1,'go' FROM spec`, canonical, hash, name)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := cliChild(t, "beat", dsn, "--once"); err != nil {
		t.Fatalf("valid beat once failed: %s", output)
	}
	if _, err := pool.Exec(ctx, "UPDATE public.periodic_tasks SET cron_expr='invalid cron',next_run_at=clock_timestamp()-interval '2 minutes' WHERE name=$1", name); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := pool.QueryRow(ctx, "SELECT to_jsonb(p)::text FROM public.periodic_tasks p WHERE name=$1", name).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if output, err := cliChild(t, "beat", dsn, "--once"); err == nil || !strings.Contains(string(output), "require repair") {
		t.Fatalf("rejected beat once did not fail: %s", output)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT to_jsonb(p)::text,(SELECT count(*) FROM public.task_runs WHERE periodic_name=$1) FROM public.periodic_tasks p WHERE name=$1", name).Scan(&after, &count); err != nil || before != after || count != 1 {
		t.Fatal("rejected one-shot beat advanced or inserted work")
	}
}
