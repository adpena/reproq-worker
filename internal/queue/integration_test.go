package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func TestRateLimitingClaim(t *testing.T) {
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
	pool.Exec(ctx, "DELETE FROM rate_limits")
	s := NewService(pool)

	specJSON := `{"task_path":"myapp.tasks.rate","args":[],"kwargs":{}}`
	_, err = pool.Exec(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, priority, enqueued_at)
		VALUES ($1, 'default', $2, 'READY', NOW(), 0, NOW())
	`, "rate_limit"+strings.Repeat("5", 54), specJSON)
	if err != nil {
		t.Fatalf("failed to enqueue task: %v", err)
	}

	// No tokens available -> claim should skip.
	_, err = pool.Exec(ctx, `
		INSERT INTO rate_limits (key, tokens_per_second, burst_size, current_tokens, last_refilled_at)
		VALUES ('queue:default', 1, 1, 0, NOW())
	`)
	if err != nil {
		t.Fatalf("failed to insert rate limit: %v", err)
	}

	task, err := s.Claim(ctx, "w-rate-1", "default", 60, 0)
	if err == nil || task != nil {
		t.Fatalf("expected rate limit to block claim, got task=%v err=%v", task, err)
	}
	if !errors.Is(err, ErrNoTasks) {
		t.Fatalf("expected ErrNoTasks due to rate limit, got %v", err)
	}

	// Add a token -> claim should succeed.
	_, err = pool.Exec(ctx, `
		UPDATE rate_limits
		SET current_tokens = 1, last_refilled_at = NOW()
		WHERE key = 'queue:default'
	`)
	if err != nil {
		t.Fatalf("failed to update rate limit: %v", err)
	}

	task, err = s.Claim(ctx, "w-rate-2", "default", 60, 0)
	if err != nil {
		t.Fatalf("expected claim to succeed, got %v", err)
	}

	var tokens float64
	err = pool.QueryRow(ctx, "SELECT current_tokens FROM rate_limits WHERE key = 'queue:default'").Scan(&tokens)
	if err != nil {
		t.Fatalf("failed to read tokens: %v", err)
	}
	if tokens != 0 {
		t.Errorf("expected tokens to decrement to 0, got %v", tokens)
	}
}

func TestWorkflowChordRelease(t *testing.T) {
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
	pool.Exec(ctx, "DELETE FROM rate_limits")
	s := NewService(pool)

	workflowID := "11111111-1111-1111-1111-111111111111"

	var id1 int64
	var id2 int64
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, workflow_id, leased_by, started_at)
		VALUES ($1, 'default', '{}', 'RUNNING', NOW(), $2, 'w1', NOW())
		RETURNING result_id
	`, "wf1"+strings.Repeat("a", 61), workflowID).Scan(&id1)
	if err != nil {
		t.Fatalf("failed to insert task 1: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, workflow_id, leased_by, started_at)
		VALUES ($1, 'default', '{}', 'RUNNING', NOW(), $2, 'w1', NOW())
		RETURNING result_id
	`, "wf2"+strings.Repeat("b", 61), workflowID).Scan(&id2)
	if err != nil {
		t.Fatalf("failed to insert task 2: %v", err)
	}

	var callbackID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, workflow_id, wait_count)
		VALUES ($1, 'default', '{}', 'WAITING', NOW(), $2, 2)
		RETURNING result_id
	`, "wf_cb"+strings.Repeat("c", 59), workflowID).Scan(&callbackID)
	if err != nil {
		t.Fatalf("failed to insert callback task: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO workflow_runs (workflow_id, expected_count, success_count, failure_count, callback_result_id, status)
		VALUES ($1, 2, 0, 0, $2, 'RUNNING')
	`, workflowID, callbackID)
	if err != nil {
		t.Fatalf("failed to insert workflow run: %v", err)
	}

	if err := s.CompleteSuccess(ctx, id1, "w1", json.RawMessage(`{"ok": true}`)); err != nil {
		t.Fatalf("complete success task 1: %v", err)
	}

	var wfStatus string
	var successCount int
	var failureCount int
	err = pool.QueryRow(ctx, `
		SELECT status, success_count, failure_count FROM workflow_runs
		WHERE workflow_id = $1
	`, workflowID).Scan(&wfStatus, &successCount, &failureCount)
	if err != nil {
		t.Fatalf("failed to read workflow after first completion: %v", err)
	}
	if successCount != 1 {
		t.Errorf("expected success_count 1, got %d", successCount)
	}
	if failureCount != 0 {
		t.Errorf("expected failure_count 0, got %d", failureCount)
	}

	var callbackStatus string
	err = pool.QueryRow(ctx, `
		SELECT status FROM task_runs
		WHERE result_id = $1
	`, callbackID).Scan(&callbackStatus)
	if err != nil {
		t.Fatalf("failed to read callback status after first completion: %v", err)
	}
	if callbackStatus != "WAITING" {
		t.Errorf("expected callback status WAITING, got %s", callbackStatus)
	}

	if err := s.CompleteSuccess(ctx, id2, "w1", json.RawMessage(`{"ok": true}`)); err != nil {
		t.Fatalf("complete success task 2: %v", err)
	}

	err = pool.QueryRow(ctx, `
		SELECT status, success_count, failure_count FROM workflow_runs
		WHERE workflow_id = $1
	`, workflowID).Scan(&wfStatus, &successCount, &failureCount)
	if err != nil {
		t.Fatalf("failed to read workflow after second completion: %v", err)
	}
	if successCount != 2 {
		t.Errorf("expected success_count 2, got %d", successCount)
	}
	if failureCount != 0 {
		t.Errorf("expected failure_count 0, got %d", failureCount)
	}

	err = pool.QueryRow(ctx, `
		SELECT status FROM task_runs
		WHERE result_id = $1
	`, callbackID).Scan(&callbackStatus)
	if err != nil {
		t.Fatalf("failed to read callback status after second completion: %v", err)
	}
	if callbackStatus != "READY" {
		t.Errorf("expected callback status READY, got %s", callbackStatus)
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

func TestRequestCancel(t *testing.T) {
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

	specHash := "cancelhash" + strings.Repeat("0", 54)
	_, err = pool.Exec(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after)
		VALUES ($1, 'default', '{}', 'READY', NOW())
	`, specHash)
	if err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}

	task, err := s.Claim(ctx, "w-cancel-1", "default", 60, 0)
	if err != nil {
		t.Fatalf("failed to claim task: %v", err)
	}

	updated, err := s.RequestCancel(ctx, task.ResultID)
	if err != nil {
		t.Fatalf("request cancel failed: %v", err)
	}
	if updated != 1 {
		t.Fatalf("expected 1 row updated, got %d", updated)
	}

	var cancelRequested bool
	err = pool.QueryRow(ctx, "SELECT cancel_requested FROM task_runs WHERE result_id = $1", task.ResultID).Scan(&cancelRequested)
	if err != nil {
		t.Fatalf("failed to read cancel_requested: %v", err)
	}
	if !cancelRequested {
		t.Fatal("expected cancel_requested to be true")
	}

	updated, err = s.RequestCancel(ctx, task.ResultID+9999)
	if err != nil {
		t.Fatalf("request cancel on missing task failed: %v", err)
	}
	if updated != 0 {
		t.Fatalf("expected 0 rows updated, got %d", updated)
	}
}

func TestTriageLifecycle(t *testing.T) {
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

	specHash := "triagehash" + strings.Repeat("1", 54)
	_, err = pool.Exec(ctx, `
		INSERT INTO task_runs (spec_hash, queue_name, spec_json, status, run_after, max_attempts)
		VALUES ($1, 'default', '{"task_path":"myapp.tasks.triage","args":[]}', 'READY', NOW(), 1)
	`, specHash)
	if err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}

	task, err := s.Claim(ctx, "w-triage-1", "default", 60, 0)
	if err != nil {
		t.Fatalf("failed to claim task: %v", err)
	}

	err = s.CompleteFailure(ctx, task.ResultID, "w-triage-1", json.RawMessage(`{"message":"boom"}`), false, time.Now())
	if err != nil {
		t.Fatalf("failed to complete failure: %v", err)
	}

	items, err := s.ListFailedTasks(ctx, 10, "default")
	if err != nil {
		t.Fatalf("list failed tasks: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 failed task, got %d", len(items))
	}
	if items[0].LastError == nil || *items[0].LastError != "boom" {
		t.Fatalf("expected last_error boom, got %v", items[0].LastError)
	}
	if items[0].FailedAt == nil {
		t.Fatal("expected failed_at to be set")
	}

	detail, err := s.InspectFailedTask(ctx, task.ResultID)
	if err != nil {
		t.Fatalf("inspect failed task: %v", err)
	}
	if len(detail.SpecJSON) == 0 || len(detail.ErrorsJSON) == 0 {
		t.Fatal("expected spec_json and errors_json to be populated")
	}

	updated, err := s.RetryFailedTask(ctx, task.ResultID)
	if err != nil {
		t.Fatalf("retry failed task: %v", err)
	}
	if updated != 1 {
		t.Fatalf("expected 1 task retried, got %d", updated)
	}

	var status string
	var attempts int
	var lastError sql.NullString
	var failedAt sql.NullTime
	err = pool.QueryRow(ctx, `
		SELECT status, attempts, last_error, failed_at
		FROM task_runs
		WHERE result_id = $1
	`, task.ResultID).Scan(&status, &attempts, &lastError, &failedAt)
	if err != nil {
		t.Fatalf("read retried task: %v", err)
	}
	if status != "READY" {
		t.Fatalf("expected status READY, got %s", status)
	}
	if attempts != 0 {
		t.Fatalf("expected attempts reset to 0, got %d", attempts)
	}
	if lastError.Valid || failedAt.Valid {
		t.Fatalf("expected last_error and failed_at to be cleared, got %v %v", lastError, failedAt)
	}
}
