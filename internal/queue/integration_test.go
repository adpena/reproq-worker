package queue

import (
	"context"
	"encoding/json"
	"os"
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

	s := NewService(pool)

	// 1. Enqueue Task
	specJSON := `{"task": "myapp.tasks.test", "val": 1}`
	specHash := "testhash"
	_, err = pool.Exec(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, payload_json, status, run_after)
		VALUES ($1, 'default', $2, 'PENDING', NOW())
	`, specHash, specJSON)
	if err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}

	// 2. Claim Task
	workerID := "test-worker-1"
	task, err := s.Claim(ctx, "default", workerID, 5*time.Second)
	if err != nil {
		t.Fatalf("failed to claim: %v", err)
	}
	if task.WorkerID == nil || *task.WorkerID != workerID {
		t.Errorf("expected workerID %s, got %v", workerID, task.WorkerID)
	}

	// 3. Heartbeat
	err = s.Heartbeat(ctx, task.ID, 10*time.Second)
	if err != nil {
		t.Errorf("heartbeat failed: %v", err)
	}

	// 4. Complete Success with Fencing (Correct Worker)
	err = s.CompleteSuccess(ctx, task.ID, workerID, json.RawMessage(`{"res":"ok"}`), "stdout", "", 0)
	if err != nil {
		t.Errorf("completion failed: %v", err)
	}

	// 5. Verify Fencing (Wrong Worker tries to update completed task)
	err = s.CompleteSuccess(ctx, task.ID, "wrong-worker", json.RawMessage(`{}`), "", "", 0)
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
	pool.Exec(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, payload_json, status, run_after, max_attempts)
		VALUES ('retryhash', 'default', '{}', 'PENDING', NOW(), 2)
	`)

	// Claim and Fail (Attempt 1)
	task, _ := s.Claim(ctx, "default", "w1", 1*time.Minute)
	err = s.CompleteFailure(ctx, task.ID, "w1", []byte("err"), "out", "err", 1, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// Verify status is PENDING (for retry)
	var status string
	pool.QueryRow(ctx, "SELECT status FROM task_runs WHERE id = $1", task.ID).Scan(&status)
	if status != "PENDING" {
		t.Errorf("expected PENDING for retry, got %s", status)
	}

	// Claim and Fail (Attempt 2 - exhausted)
	task, _ = s.Claim(ctx, "default", "w2", 1*time.Minute)
	err = s.CompleteFailure(ctx, task.ID, "w2", []byte("err"), "out", "err", 1, false, time.Now())
	
	pool.QueryRow(ctx, "SELECT status FROM task_runs WHERE id = $1", task.ID).Scan(&status)
	if status != "FAILED" {
		t.Errorf("expected FAILED after exhaustion, got %s", status)
	}
}
