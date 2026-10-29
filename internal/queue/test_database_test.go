package queue

import (
	"context"
	"reproq-worker/internal/testdatabase"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDatabaseSafetyGuard(t *testing.T) {
	for _, tc := range []struct {
		name string
		dsn  string
		ok   bool
	}{
		{"IPv4", "postgres://test@127.0.0.1/reproq_test_guard", true},
		{"localhost", "postgres://test@localhost/reproq_test_guard", true},
		{"IPv6", "postgres://test@[::1]/reproq_test_guard", true},
		{"remote", "postgres://test@example.invalid/reproq_test_guard", false},
		{"application", "postgres://test@127.0.0.1/teadata_dev", false},
		{"default", "postgres://test@localhost/reproq", false},
		{"empty suffix", "postgres://test@127.0.0.1/reproq_test_", false},
		{"socket", "host=/tmp dbname=reproq_test_guard", false},
		{"remote fallback", "host=127.0.0.1,example.invalid dbname=reproq_test_guard", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testdatabase.ValidateURL(tc.dsn) == nil; got != tc.ok {
				t.Fatalf("guard accepted=%v, want %v", got, tc.ok)
			}
		})
	}
}

func testLeaseToken(tb testing.TB, pool *pgxpool.Pool, id int64) string {
	tb.Helper()
	var token string
	if err := pool.QueryRow(context.Background(), "SELECT lease_token::text FROM public.task_runs WHERE result_id=$1", id).Scan(&token); err != nil {
		tb.Fatal(err)
	}
	return token
}

func newTestService(tb testing.TB, pool *pgxpool.Pool) *Service {
	tb.Helper()
	service, err := NewService(context.Background(), pool)
	if err != nil {
		tb.Fatal(err)
	}
	return service
}

// Low-level SQL fixtures explicitly mark spec_version=1 to test queue state,
// locks and fencing independently of execution. They do not qualify canonical
// SDK serialization; that boundary uses the exact contracts/*.json artifacts.
