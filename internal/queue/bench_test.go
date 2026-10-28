package queue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reproq-worker/internal/db"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func BenchmarkClaim(b *testing.B) {
	b.StopTimer()
	dsn := testDatabaseURL(b)
	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)
	queueName, _ := seedBenchmarkTasks(b, pool, b.N)
	service := NewService(pool)
	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		task, err := service.Claim(ctx, "bench-worker", queueName, 300, 0)
		if err != nil {
			b.Fatalf("claim %d of %d failed: %v", i+1, b.N, err)
		}
		if task == nil || task.QueueName != queueName || task.Status != StatusRunning {
			b.Fatalf("claim %d did not return the owned RUNNING task", i+1)
		}
	}
	b.StopTimer()
	var claimed int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM task_runs WHERE queue_name=$1 AND spec_json->>'benchmark_owner'=$1 AND status='RUNNING'", queueName).Scan(&claimed); err != nil {
		b.Fatal(err)
	}
	if claimed != b.N {
		b.Fatalf("database has %d owned RUNNING tasks, want %d completed claims", claimed, b.N)
	}
	b.ReportMetric(float64(b.N), "claims")
}

func seedBenchmarkTasks(tb testing.TB, pool *pgxpool.Pool, n int) (string, func()) {
	tb.Helper()
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		tb.Fatal(err)
	}
	owner := "reproq_test_bench_" + hex.EncodeToString(nonce)
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := pool.Exec(ctx, "DELETE FROM task_runs WHERE queue_name=$1 AND spec_json->>'benchmark_owner'=$1", owner)
		if err != nil {
			tb.Errorf("failed cleaning owned benchmark tasks: %v", err)
		}
	}
	tb.Cleanup(cleanup)
	source := pgx.CopyFromSlice(n, func(i int) ([]any, error) {
		spec, err := json.Marshal(map[string]any{
			"v": 1, "task_path": "contracts.benchmark_task", "args": []int{i},
			"kwargs": map[string]any{}, "benchmark_owner": owner,
		})
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(spec)
		return []any{fmt.Sprintf("%x", hash), owner, string(spec), "READY", "contracts.benchmark_task"}, nil
	})
	count, err := pool.CopyFrom(context.Background(), pgx.Identifier{"task_runs"},
		[]string{"spec_hash", "queue_name", "spec_json", "status", "task_path"}, source)
	if err != nil {
		tb.Fatal(err)
	}
	if count != int64(n) {
		tb.Fatalf("seeded %d tasks, want %d", count, n)
	}
	return owner, cleanup
}

func TestBenchmarkFixtureCleanupPreservesOtherQueues(t *testing.T) {
	dsn := testDatabaseURL(t)
	pool, err := db.NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	otherQueue, _ := seedBenchmarkTasks(t, pool, 1)
	ownedQueue, cleanup := seedBenchmarkTasks(t, pool, 3)
	cleanup()
	var owned, other int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM task_runs WHERE queue_name=$1", ownedQueue).Scan(&owned); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM task_runs WHERE queue_name=$1", otherQueue).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if owned != 0 || other != 1 {
		t.Fatalf("cleanup left owned=%d and other=%d, want 0 and 1", owned, other)
	}
}
