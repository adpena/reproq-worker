package queue

import (
	"context"
	"fmt"
	"os"
	"reproq-worker/internal/db"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func BenchmarkClaim(b *testing.B) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		b.Skip("DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()

	service := NewService(pool)

	// Prepare data
	b.StopTimer()
	setupTasks(b, pool, b.N)
	b.StartTimer()

	leaseSeconds := int((5 * time.Minute) / time.Second)
	for i := 0; i < b.N; i++ {
		_, err := service.Claim(ctx, "bench-worker", "default", leaseSeconds)
		if err != nil {
			// If we run out of tasks, it's fine for the bench to show limits
			if err == ErrNoTasks {
				break
			}
			b.Fatal(err)
		}
	}
}

func setupTasks(b *testing.B, pool *pgxpool.Pool, n int) {
	ctx := context.Background()
	// Clear table
	_, _ = pool.Exec(ctx, "DELETE FROM task_runs")

	for i := 0; i < n; i++ {
		query := `
			INSERT INTO task_runs (spec_hash, queue_name, payload_json, status, run_after)
			VALUES ($1, 'default', '{}', 'PENDING', NOW())
		`
		_, err := pool.Exec(ctx, query, fmt.Sprintf("hash-%d", i))
		if err != nil {
			b.Fatal(err)
		}
	}
}
