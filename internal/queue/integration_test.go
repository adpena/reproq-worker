package queue

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQueueIntegration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set, skipping integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("failed to connect to DB: %v", err)
	}
	defer pool.Close()

	// Cleanup
	pool.Exec(ctx, "DELETE FROM task_runs")
	pool.Exec(ctx, "DELETE FROM reproq_workers")

	s := NewService(pool)

	// 1. Enqueue Task
	specJSON := `{"task_path": "myapp.tasks.test", "args": [1], "kwargs": {}}`
	specHash := "testhash64000000000000000000000000000000000000000000000000000000" // 64 chars
	var resultID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after)
		VALUES ($1, 'default', $2, 'READY', NOW())
		RETURNING result_id
	`, specHash, specJSON).Scan(&resultID)
	if err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}

	// 2. Claim Task
	workerID := "test-worker-1"
	task, err := s.Claim(ctx, workerID, "default", 300, 0)
	if err != nil {
		t.Fatalf("failed to claim: %v", err)
	}
	if task.ResultID != resultID {
		t.Errorf("expected resultID %d, got %d", resultID, task.ResultID)
	}
	if task.LeasedBy == nil || *task.LeasedBy != workerID {
		t.Errorf("expected leased_by %s, got %v", workerID, task.LeasedBy)
	}

	// 3. Heartbeat
	cancelled, err := s.Heartbeat(ctx, task.ResultID, workerID, 600)
	if err != nil {
		t.Errorf("heartbeat failed: %v", err)
	}
	if cancelled {
		t.Error("expected cancelled=false, got true")
	}

	// 4. Complete Success with Fencing (Correct Worker)
	err = s.CompleteSuccess(ctx, task.ResultID, workerID, json.RawMessage(`{"res":"ok"}`))
	if err != nil {
		t.Errorf("completion failed: %v", err)
	}

	// 5. Verify Fencing (Wrong Worker tries to update completed task)
	err = s.CompleteSuccess(ctx, task.ResultID, "wrong-worker", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected fencing error for wrong worker, got nil")
	}
}

func TestRetryLifecycle(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	pool.Exec(ctx, "DELETE FROM task_runs")
	s := NewService(pool)

	// Enqueue with max_attempts = 2
	var id int64
	specHash := "retryhash" + strings.Repeat("0", 55)
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, max_attempts)
		VALUES ($1, 'default', '{}', 'READY', NOW(), 2)
		RETURNING result_id
	`, specHash).Scan(&id)
	if err != nil {
		t.Fatalf("failed to enqueue retry task: %v", err)
	}

	// Claim and Fail (Attempt 1)
	task, err := s.Claim(ctx, "w1", "default", 60, 0)
	if err != nil {
		t.Fatalf("failed to claim task for retry: %v", err)
	}
	if task == nil {
		t.Fatal("expected task, got nil")
	}
	err = s.CompleteFailure(ctx, task.ResultID, "w1", json.RawMessage(`{"err":"msg"}`), true, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}

	// Verify status is READY (for retry)
	var status string
	pool.QueryRow(ctx, "SELECT status FROM task_runs WHERE result_id = $1", task.ResultID).Scan(&status)
	if status != "READY" {
		t.Errorf("expected READY for retry, got %s", status)
	}

	// Claim and Fail (Attempt 2 - exhausted)
	task, err = s.Claim(ctx, "w2", "default", 60, 0)
	if err != nil {
		t.Fatalf("failed to claim retry task attempt 2: %v", err)
	}
	if task == nil {
		t.Fatal("expected task on attempt 2, got nil")
	}
	err = s.CompleteFailure(ctx, task.ResultID, "w2", json.RawMessage(`{"err":"msg"}`), false, time.Now())

	pool.QueryRow(ctx, "SELECT status FROM task_runs WHERE result_id = $1", task.ResultID).Scan(&status)
	if status != "FAILED" {
		t.Errorf("expected FAILED after exhaustion, got %s", status)
	}
}

func TestPriorityAgingClaim(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	pool.Exec(ctx, "DELETE FROM task_runs")
	s := NewService(pool)

	// Case 1: Aging disabled, higher priority wins.
	var highID int64
	var lowID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, priority, enqueued_at)
		VALUES ($1, 'default', '{}', 'READY', NOW(), 10, NOW())
		RETURNING result_id
	`, "aging_high"+strings.Repeat("1", 54)).Scan(&highID)
	if err != nil {
		t.Fatalf("failed to enqueue high priority task: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, priority, enqueued_at)
		VALUES ($1, 'default', '{}', 'READY', NOW(), 0, NOW() - INTERVAL '120 seconds')
		RETURNING result_id
	`, "aging_low"+strings.Repeat("2", 55)).Scan(&lowID)
	if err != nil {
		t.Fatalf("failed to enqueue low priority task: %v", err)
	}

	task, err := s.Claim(ctx, "w-aging-1", "default", 60, 0)
	if err != nil {
		t.Fatalf("failed to claim without aging: %v", err)
	}
	if task.ResultID != highID {
		t.Errorf("expected high priority task %d, got %d", highID, task.ResultID)
	}

	// Cleanup for aging-enabled case.
	pool.Exec(ctx, "DELETE FROM task_runs")

	// Case 2: Aging enabled, older low-priority wins.
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, priority, enqueued_at)
		VALUES ($1, 'default', '{}', 'READY', NOW(), 10, NOW())
		RETURNING result_id
	`, "aging_high2"+strings.Repeat("3", 53)).Scan(&highID)
	if err != nil {
		t.Fatalf("failed to enqueue high priority task: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, priority, enqueued_at)
		VALUES ($1, 'default', '{}', 'READY', NOW(), 0, NOW() - INTERVAL '120 seconds')
		RETURNING result_id
	`, "aging_low2"+strings.Repeat("4", 54)).Scan(&lowID)
	if err != nil {
		t.Fatalf("failed to enqueue low priority task: %v", err)
	}

	task, err = s.Claim(ctx, "w-aging-2", "default", 60, 10)
	if err != nil {
		t.Fatalf("failed to claim with aging: %v", err)
	}
	if task.ResultID != lowID {
		t.Errorf("expected aged low priority task %d, got %d", lowID, task.ResultID)
	}
}

func TestPeriodicTasks(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	pool.Exec(ctx, "DELETE FROM task_runs")
	pool.Exec(ctx, "DELETE FROM periodic_tasks")
	s := NewService(pool)

	// Create a periodic task (every minute)
	pt := PeriodicTask{
		Name:        "test_pt",
		CronExpr:    "* * * * *",
		TaskPath:    "myapp.pt",
		PayloadJSON: json.RawMessage(`{"a": 1}`),
		QueueName:   "default",
		Priority:    0,
		MaxAttempts: 3,
		Enabled:     true,
	}
	err = s.UpsertPeriodicTask(ctx, pt)
	if err != nil {
		t.Fatalf("failed to upsert periodic task: %v", err)
	}

	// Force it to be due
	pool.Exec(ctx, "UPDATE periodic_tasks SET next_run_at = NOW() - INTERVAL '1 minute'")

	// Enqueue due tasks
	n, err := s.EnqueueDuePeriodicTasks(ctx)
	if err != nil {
		t.Fatalf("failed to enqueue due tasks: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 task enqueued, got %d", n)
	}

	// Verify task exists in task_runs
	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM task_runs WHERE status = 'READY'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 task in task_runs, got %d", count)
	}

	// Verify next_run_at updated
	var nextRun time.Time
	pool.QueryRow(ctx, "SELECT next_run_at FROM periodic_tasks WHERE name = 'test_pt'").Scan(&nextRun)
	if !nextRun.After(time.Now()) {
		t.Error("expected next_run_at to be in the future")
	}
}

func TestWorkerRegistration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	pool.Exec(ctx, "DELETE FROM reproq_workers")
	s := NewService(pool)

	err = s.RegisterWorker(ctx, "w1", "host1", 10, []string{"q1"}, "1.0.0")
	if err != nil {
		t.Fatalf("failed to register worker: %v", err)
	}

	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM reproq_workers WHERE worker_id = 'w1'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 worker, got %d", count)
	}

	err = s.UpdateWorkerHeartbeat(ctx, "w1")
	if err != nil {
		t.Errorf("failed to update heartbeat: %v", err)
	}
}
