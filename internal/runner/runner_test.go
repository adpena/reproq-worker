package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"reproq-worker/internal/config"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/queue"
)

type fakeQueue struct {
	registeredVersion string
	registerCalls     int
}

func (f *fakeQueue) RegisterWorker(ctx context.Context, id, hostname string, concurrency int, queues []string, version string) error {
	f.registerCalls++
	f.registeredVersion = version
	return nil
}

func (f *fakeQueue) Reclaim(ctx context.Context, maxAttemptsDefault int) (int64, error) {
	return 0, nil
}

func (f *fakeQueue) UpdateWorkerHeartbeat(ctx context.Context, id string) error {
	return nil
}

func (f *fakeQueue) Claim(ctx context.Context, workerID string, queueName string, leaseSeconds int, priorityAgingFactor float64) (*queue.TaskRun, error) {
	return nil, queue.ErrNoTasks
}

func (f *fakeQueue) Heartbeat(ctx context.Context, resultID int64, workerID string, leaseSeconds int) (bool, error) {
	return false, nil
}

func (f *fakeQueue) CompleteSuccess(ctx context.Context, resultID int64, workerID string, returnJSON json.RawMessage, logsURI *string) error {
	return nil
}

func (f *fakeQueue) CompleteFailure(ctx context.Context, resultID int64, workerID string, errorsJSON json.RawMessage, retry bool, nextRun time.Time, logsURI *string) error {
	return nil
}

type fakeExecutor struct{}

func (f *fakeExecutor) Execute(ctx context.Context, resultID int64, attempt int, payload json.RawMessage, timeout time.Duration) (*executor.ResultEnvelope, string, string, error) {
	return nil, "", "", nil
}

type validationQueue struct {
	failureCalled bool
	failureJSON   json.RawMessage
	retry         bool
}

func (v *validationQueue) RegisterWorker(ctx context.Context, id, hostname string, concurrency int, queues []string, version string) error {
	return nil
}

func (v *validationQueue) Reclaim(ctx context.Context, maxAttemptsDefault int) (int64, error) {
	return 0, nil
}

func (v *validationQueue) UpdateWorkerHeartbeat(ctx context.Context, id string) error {
	return nil
}

func (v *validationQueue) Claim(ctx context.Context, workerID string, queueName string, leaseSeconds int, priorityAgingFactor float64) (*queue.TaskRun, error) {
	return nil, queue.ErrNoTasks
}

func (v *validationQueue) Heartbeat(ctx context.Context, resultID int64, workerID string, leaseSeconds int) (bool, error) {
	return false, nil
}

func (v *validationQueue) CompleteSuccess(ctx context.Context, resultID int64, workerID string, returnJSON json.RawMessage, logsURI *string) error {
	return nil
}

func (v *validationQueue) CompleteFailure(ctx context.Context, resultID int64, workerID string, errorsJSON json.RawMessage, retry bool, nextRun time.Time, logsURI *string) error {
	v.failureCalled = true
	v.failureJSON = errorsJSON
	v.retry = retry
	return nil
}

type recordingExecutor struct {
	called bool
}

func (r *recordingExecutor) Execute(ctx context.Context, resultID int64, attempt int, payload json.RawMessage, timeout time.Duration) (*executor.ResultEnvelope, string, string, error) {
	r.called = true
	return nil, "", "", nil
}

type cancelQueue struct {
	failureCalled bool
	failureJSON   json.RawMessage
	retry         bool
}

func (c *cancelQueue) RegisterWorker(ctx context.Context, id, hostname string, concurrency int, queues []string, version string) error {
	return nil
}

func (c *cancelQueue) Reclaim(ctx context.Context, maxAttemptsDefault int) (int64, error) {
	return 0, nil
}

func (c *cancelQueue) UpdateWorkerHeartbeat(ctx context.Context, id string) error {
	return nil
}

func (c *cancelQueue) Claim(ctx context.Context, workerID string, queueName string, leaseSeconds int, priorityAgingFactor float64) (*queue.TaskRun, error) {
	return nil, queue.ErrNoTasks
}

func (c *cancelQueue) Heartbeat(ctx context.Context, resultID int64, workerID string, leaseSeconds int) (bool, error) {
	return true, nil
}

func (c *cancelQueue) CompleteSuccess(ctx context.Context, resultID int64, workerID string, returnJSON json.RawMessage, logsURI *string) error {
	return nil
}

func (c *cancelQueue) CompleteFailure(ctx context.Context, resultID int64, workerID string, errorsJSON json.RawMessage, retry bool, nextRun time.Time, logsURI *string) error {
	c.failureCalled = true
	c.failureJSON = errorsJSON
	c.retry = retry
	return nil
}

type blockingExecutor struct{}

func (b *blockingExecutor) Execute(ctx context.Context, resultID int64, attempt int, payload json.RawMessage, timeout time.Duration) (*executor.ResultEnvelope, string, string, error) {
	<-ctx.Done()
	return nil, "", "", ctx.Err()
}

type roundRobinQueue struct {
	tasks  map[string][]*queue.TaskRun
	claims []string
}

func (r *roundRobinQueue) RegisterWorker(ctx context.Context, id, hostname string, concurrency int, queues []string, version string) error {
	return nil
}

func (r *roundRobinQueue) Reclaim(ctx context.Context, maxAttemptsDefault int) (int64, error) {
	return 0, nil
}

func (r *roundRobinQueue) UpdateWorkerHeartbeat(ctx context.Context, id string) error {
	return nil
}

func (r *roundRobinQueue) Claim(ctx context.Context, workerID string, queueName string, leaseSeconds int, priorityAgingFactor float64) (*queue.TaskRun, error) {
	r.claims = append(r.claims, queueName)
	if len(r.tasks[queueName]) == 0 {
		return nil, queue.ErrNoTasks
	}
	task := r.tasks[queueName][0]
	r.tasks[queueName] = r.tasks[queueName][1:]
	return task, nil
}

func (r *roundRobinQueue) Heartbeat(ctx context.Context, resultID int64, workerID string, leaseSeconds int) (bool, error) {
	return false, nil
}

func (r *roundRobinQueue) CompleteSuccess(ctx context.Context, resultID int64, workerID string, returnJSON json.RawMessage, logsURI *string) error {
	return nil
}

func (r *roundRobinQueue) CompleteFailure(ctx context.Context, resultID int64, workerID string, errorsJSON json.RawMessage, retry bool, nextRun time.Time, logsURI *string) error {
	return nil
}

func TestRunnerRegistersVersion(t *testing.T) {
	cfg := &config.Config{
		WorkerID:               "worker-1",
		Version:                "test-version",
		QueueNames:             []string{"default"},
		MaxConcurrency:         0,
		PollMinBackoff:         10 * time.Millisecond,
		PollMaxBackoff:         20 * time.Millisecond,
		LeaseSeconds:           60,
		HeartbeatSeconds:       0,
		ReclaimIntervalSeconds: 0,
		MaxAttemptsDefault:     3,
		ExecTimeout:            time.Second,
		PriorityAgingFactor:    0,
	}

	q := &fakeQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := New(cfg, q, &fakeExecutor{}, logger, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if q.registerCalls != 1 {
		t.Fatalf("expected RegisterWorker to be called once, got %d", q.registerCalls)
	}
	if q.registeredVersion != cfg.Version {
		t.Fatalf("expected version %q, got %q", cfg.Version, q.registeredVersion)
	}
}

func TestPollRoundRobin(t *testing.T) {
	cfg := &config.Config{
		WorkerID:            "worker-1",
		QueueNames:          []string{"q1", "q2", "q3"},
		LeaseSeconds:        60,
		PriorityAgingFactor: 0,
	}

	q := &roundRobinQueue{
		tasks: map[string][]*queue.TaskRun{
			"q1": {&queue.TaskRun{QueueName: "q1"}},
			"q2": {&queue.TaskRun{QueueName: "q2"}},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := New(cfg, q, &fakeExecutor{}, logger, nil)

	ctx := context.Background()
	task, err := r.poll(ctx)
	if err != nil {
		t.Fatalf("expected task, got error %v", err)
	}
	if task.QueueName != "q1" {
		t.Fatalf("expected q1, got %q", task.QueueName)
	}

	task, err = r.poll(ctx)
	if err != nil {
		t.Fatalf("expected task, got error %v", err)
	}
	if task.QueueName != "q2" {
		t.Fatalf("expected q2, got %q", task.QueueName)
	}

	_, err = r.poll(ctx)
	if !errors.Is(err, queue.ErrNoTasks) {
		t.Fatalf("expected ErrNoTasks, got %v", err)
	}

	if len(q.claims) < 3 || q.claims[2] != "q3" {
		t.Fatalf("expected q3 to be tried first after rotation, got %v", q.claims)
	}
}

func TestRunnerRejectsUnauthorizedTask(t *testing.T) {
	cfg := &config.Config{
		WorkerID:            "worker-1",
		QueueNames:          []string{"default"},
		AllowedTaskModules:  []string{"allowed."},
		MaxConcurrency:      1,
		LeaseSeconds:        60,
		HeartbeatSeconds:    0,
		ExecTimeout:         time.Second,
		PriorityAgingFactor: 0,
	}

	q := &validationQueue{}
	exec := &recordingExecutor{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := New(cfg, q, exec, logger, nil)

	r.pool <- struct{}{}
	r.wg.Add(1)

	task := &queue.TaskRun{
		ResultID:  1,
		QueueName: "default",
		SpecJSON:  json.RawMessage(`{"task_path":"forbidden.task"}`),
		SpecHash:  "hash",
	}
	r.runTask(context.Background(), task)

	if !q.failureCalled {
		t.Fatal("expected CompleteFailure to be called")
	}
	if q.retry {
		t.Fatal("expected failure to be terminal (no retry)")
	}
	if exec.called {
		t.Fatal("expected executor not to be called")
	}

	var payload map[string]any
	if err := json.Unmarshal(q.failureJSON, &payload); err != nil {
		t.Fatalf("invalid failure payload: %v", err)
	}
	if payload["kind"] != "security_violation" {
		t.Fatalf("expected kind security_violation, got %v", payload["kind"])
	}
}

func TestPersistExecutionLogs(t *testing.T) {
	logsDir := t.TempDir()
	cfg := &config.Config{
		LogsDir:        logsDir,
		MaxConcurrency: 1,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := New(cfg, &fakeQueue{}, &fakeExecutor{}, logger, nil)

	task := &queue.TaskRun{ResultID: 42, Attempts: 2}
	logsURI := r.persistExecutionLogs(task, "stdout-text", "stderr-text")
	if logsURI == nil {
		t.Fatal("expected logs URI to be set")
	}

	data, err := os.ReadFile(*logsURI)
	if err != nil {
		t.Fatalf("failed to read logs file: %v", err)
	}
	contents := string(data)
	if !strings.Contains(contents, "STDOUT:\nstdout-text") {
		t.Fatalf("stdout not persisted, got %q", contents)
	}
	if !strings.Contains(contents, "STDERR:\nstderr-text") {
		t.Fatalf("stderr not persisted, got %q", contents)
	}
}

func TestSleepHandlesZeroBackoffRange(t *testing.T) {
	cfg := &config.Config{
		PollMinBackoff: 0,
		PollMaxBackoff: 0,
	}
	r := &Runner{cfg: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	r.sleep(ctx)
}

func TestRunnerHandlesCancellation(t *testing.T) {
	cfg := &config.Config{
		WorkerID:            "worker-1",
		QueueNames:          []string{"default"},
		AllowedTaskModules:  []string{"allowed."},
		MaxConcurrency:      1,
		LeaseSeconds:        60,
		HeartbeatSeconds:    1,
		ExecTimeout:         5 * time.Second,
		PriorityAgingFactor: 0,
	}

	q := &cancelQueue{}
	exec := &blockingExecutor{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := New(cfg, q, exec, logger, nil)

	r.pool <- struct{}{}
	r.wg.Add(1)

	task := &queue.TaskRun{
		ResultID:    1,
		QueueName:   "default",
		SpecJSON:    json.RawMessage(`{"task_path":"allowed.task"}`),
		SpecHash:    "hash",
		Attempts:    1,
		MaxAttempts: 1,
	}
	r.runTask(context.Background(), task)

	if !q.failureCalled {
		t.Fatal("expected CompleteFailure to be called for cancellation")
	}
	if q.retry {
		t.Fatal("expected cancellation to be terminal (no retry)")
	}

	var payload map[string]any
	if err := json.Unmarshal(q.failureJSON, &payload); err != nil {
		t.Fatalf("invalid cancellation payload: %v", err)
	}
	if payload["kind"] != "cancelled" {
		t.Fatalf("expected kind cancelled, got %v", payload["kind"])
	}
}
