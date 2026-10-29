package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"reproq-worker/internal/db"
	"reproq-worker/internal/testdatabase"
)

func TestHistoryCLIValidatesBeforeOpeningDatabase(t *testing.T) {
	for _, arguments := range [][]string{
		{"--statuses", "READY"}, {"--limit", "-1"}, {"--older-than", "-1s"},
		{"--older-than", "999999999999999999999us"}, {"--field", "created_at"}, {"unexpected"},
	} {
		output, err := cliChild(t, "prune", "postgres://owned@127.0.0.1:1/reproq_test_never_contact", append([]string{"history"}, arguments...)...)
		if err == nil || strings.Contains(string(output), "failed to") {
			t.Fatalf("invalid history request contacted a database or succeeded: %s", output)
		}
	}
	if _, err := cliChild(t, "prune", "", "unknown"); err == nil {
		t.Fatal("unknown pruning silently succeeded")
	}
}

func TestHistoryCLIExactJSONCountsAndBusinessDSNIsolation(t *testing.T) {
	dsn := testdatabase.URL(t)
	t.Setenv("DATABASE_URL", "postgres://business@127.0.0.1:1/business_must_not_be_used")
	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	name := fmt.Sprintf("owned-history-cli-%d", time.Now().UnixNano())
	// Terminal version0 history remains inspectable/prunable; it is never claimed.
	_, err = pool.Exec(ctx, `INSERT INTO public.task_runs(spec_hash,spec_json,task_path,status,queue_name,finished_at)
      SELECT repeat('a',64),'{"task_path":"owned.history.task"}'::jsonb,'owned.history.task','FAILED',$1,clock_timestamp()-interval '30 days' FROM generate_series(1,3)`, name)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, "DELETE FROM public.task_runs WHERE queue_name=$1", name)
	for _, dry := range []bool{true, false} {
		arguments := []string{"history", "--statuses", "FAILED", "--older-than", "86400000000us", "--field", "finished_at", "--limit", "1", "--json"}
		if dry {
			arguments = append(arguments, "--dry-run")
		}
		output, err := cliChild(t, "prune", dsn, arguments...)
		if err != nil {
			t.Fatalf("actual history command failed: %s", output)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(output, &result); err != nil || len(result) != 2 {
			t.Fatalf("history response is not one exact JSON object: %s", output)
		}
		var count int64
		var actualDry bool
		if json.Unmarshal(result["count"], &count) != nil || json.Unmarshal(result["dry_run"], &actualDry) != nil || count != 1 || actualDry != dry {
			t.Fatalf("wrong history count: %s", output)
		}
	}
	var remaining int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.task_runs WHERE queue_name=$1", name).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatal("history command changed more than its acknowledged limit", err)
	}
}
