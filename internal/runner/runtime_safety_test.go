package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"reproq-worker/internal/config"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/queue"

	"github.com/prometheus/client_golang/prometheus"
)

type outcomeQueue struct {
	validationQueue
	successCalled bool
	completionErr error
}

func (q *outcomeQueue) CompleteSuccess(context.Context, int64, string, string, json.RawMessage, *string) error {
	q.successCalled = true
	return q.completionErr
}

func (q *outcomeQueue) CompleteFailure(ctx context.Context, id int64, worker, token string, payload json.RawMessage, retry bool, next time.Time, logs *string) (queue.TaskStatus, error) {
	status, _ := q.validationQueue.CompleteFailure(ctx, id, worker, token, payload, retry, next, logs)
	return status, q.completionErr
}

type outcomeExecutor struct {
	called  bool
	timeout time.Duration
	env     *executor.ResultEnvelope
	err     error
}

func (e *outcomeExecutor) Execute(_ context.Context, _ executor.Claim, _ json.RawMessage, timeout time.Duration) (*executor.ResultEnvelope, string, string, error) {
	e.called, e.timeout = true, timeout
	return e.env, "", "", e.err
}

func runOwnedTask(t *testing.T, q *outcomeQueue, e *outcomeExecutor, task *queue.TaskRun) *recordingPublisher {
	t.Helper()
	sealRunnerFixture(t, task)
	cfg := &config.Config{WorkerID: "test-worker", MaxPayloadBytes: 1024 * 1024, MaxConcurrency: 1, AllowedTaskModules: []string{"allowed."}, ExecTimeout: time.Hour}
	publisher := &recordingPublisher{}
	r := New(cfg, q, e, slog.New(slog.NewTextHandler(io.Discard, nil)), publisher)
	r.pool <- struct{}{}
	r.wg.Add(1)
	r.runTask(context.Background(), task)
	return publisher
}

// Synthetic unit-only envelopes let fake executors exercise runner control
// paths without the retired v0 transport. SDK canonical-byte qualification is
// covered separately by contracts fixtures and real integration receipts.
func sealRunnerFixture(t *testing.T, task *queue.TaskRun) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(task.SpecJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if task.BackendAlias == "" {
		task.BackendAlias = "default"
	}
	if task.TaskPath == nil {
		if path, ok := payload["task_path"].(string); ok {
			task.TaskPath = &path
		}
	}
	payload["v"], payload["backend_alias"], payload["queue_name"] = 1, task.BackendAlias, task.QueueName
	payload["args"], payload["kwargs"], payload["takes_context"] = []any{}, map[string]any{}, false
	payload["priority"], payload["lock_key"] = task.Priority, task.LockKey
	payload["concurrency_key"], payload["concurrency_limit"] = task.ConcurrencyKey, task.ConcurrencyLimit
	payload["run_after"], payload["provenance"] = nil, map[string]any{"code_ref": nil, "pip_lock_hash": nil}
	payload["exec"] = map[string]any{"timeout_seconds": task.TimeoutSeconds, "max_attempts": task.MaxAttempts}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	task.SpecVersion, task.CanonicalSpecText, task.SpecJSON = 1, &text, encoded
	task.SpecHash = fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func ownedTask(t *testing.T) *queue.TaskRun {
	t.Helper()
	return &queue.TaskRun{ResultID: 1, QueueName: t.Name(), SpecJSON: json.RawMessage(`{"task_path":"allowed.task"}`), Attempts: 1, MaxAttempts: 3}
}

func TestRunnerUsesPositiveTaskTimeoutOverDefault(t *testing.T) {
	for _, seconds := range []int{0, -1, 2, 7200} {
		task := ownedTask(t)
		task.TimeoutSeconds = seconds
		e := &outcomeExecutor{env: &executor.ResultEnvelope{Ok: true}}
		runOwnedTask(t, &outcomeQueue{}, e, task)
		want := time.Hour
		if seconds > 0 {
			want = time.Duration(seconds) * time.Second
		}
		if e.timeout != want {
			t.Fatalf("task timeout %d: got %v want %v", seconds, e.timeout, want)
		}
	}
}

func TestRunnerInfrastructureFailureHonorsAttemptBudget(t *testing.T) {
	for _, attempts := range []int{1, 3, 4} {
		task := ownedTask(t)
		task.Attempts = attempts
		q := &outcomeQueue{}
		runOwnedTask(t, q, &outcomeExecutor{err: context.DeadlineExceeded}, task)
		if !q.failureCalled || q.retry != (attempts < task.MaxAttempts) {
			t.Fatalf("attempts=%d max=%d called=%v retry=%v", attempts, task.MaxAttempts, q.failureCalled, q.retry)
		}
	}
}

func TestRunnerValidatesExecutedPathAndRejectsMismatchedProjection(t *testing.T) {
	for _, spec := range []string{`{"task_path":"forbidden.task"}`, `{}`, `{"TASK_PATH":"allowed.task"}`, `{"task_path":null}`} {
		task := ownedTask(t)
		projection := "allowed.task"
		task.TaskPath, task.SpecJSON = &projection, json.RawMessage(spec)
		q, e := &outcomeQueue{}, &outcomeExecutor{env: &executor.ResultEnvelope{Ok: true}}
		runOwnedTask(t, q, e, task)
		if e.called || !q.failureCalled || q.retry {
			t.Fatalf("mismatched payload executed: %s", spec)
		}
	}
}

func metricValue(t *testing.T, name, queueName, status string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["queue"] == queueName && labels["status"] == status {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestRunnerAcknowledgesTransitionsBeforeOutcomeMetricsAndEvents(t *testing.T) {
	for _, mode := range []string{"success", "failure", "infra", "invalid"} {
		for _, acknowledged := range []bool{false, true} {
			t.Run(mode+"/"+map[bool]string{true: "ack", false: "rejected"}[acknowledged], func(t *testing.T) {
				task := ownedTask(t)
				q := &outcomeQueue{}
				if !acknowledged {
					q.completionErr = errors.New("ownership lost")
				}
				e := &outcomeExecutor{env: &executor.ResultEnvelope{Ok: mode == "success"}}
				status := "failure"
				if mode == "success" {
					status = "success"
				}
				if mode == "infra" {
					e.env, e.err = nil, errors.New("executor unavailable")
				}
				if mode == "invalid" {
					task.SpecJSON = json.RawMessage(`{}`)
				}
				before := metricValue(t, "reproq_tasks_completed_total", task.QueueName, status)
				beforeProcessed := metricValue(t, "reproq_tasks_processed_total", task.QueueName, status)
				publisher := runOwnedTask(t, q, e, task)
				want := float64(0)
				if acknowledged {
					want = 1
				}
				if delta := metricValue(t, "reproq_tasks_completed_total", task.QueueName, status) - before; delta != want {
					t.Fatalf("completion metric advanced %v want %v", delta, want)
				}
				if delta := metricValue(t, "reproq_tasks_processed_total", task.QueueName, status) - beforeProcessed; delta != want {
					t.Fatalf("processed metric advanced %v want %v", delta, want)
				}
				if !acknowledged && publisher.last.Type != "" && publisher.last.Type != "task_started" {
					t.Fatalf("published unacknowledged outcome: %s", publisher.last.Type)
				}
				if acknowledged && (publisher.last.Type == "" || publisher.last.Type == "task_started") {
					t.Fatal("acknowledged outcome was not published")
				}
			})
		}
	}
}

func TestRunnerPreservesTaskExceptionFromFailedExecutorProcess(t *testing.T) {
	task, q := ownedTask(t), &outcomeQueue{}
	e := &outcomeExecutor{env: &executor.ResultEnvelope{Ok: false, ExceptionClass: "ValueError"}, err: executor.ErrProcessExit}
	runOwnedTask(t, q, e, task)
	if !strings.Contains(string(q.failureJSON), `"kind":"app_error"`) {
		t.Fatalf("exception misclassified: %s", q.failureJSON)
	}
}

func TestRunnerBoundsShutdownWait(t *testing.T) {
	r := &Runner{cfg: &config.Config{ShutdownTimeout: 20 * time.Millisecond}}
	r.wg.Add(1)
	defer r.wg.Done()
	started := time.Now()
	if err := r.waitForTasks(); err == nil {
		t.Fatal("unresponsive execution did not fail bounded shutdown")
	}
	if time.Since(started) > time.Second {
		t.Fatal("shutdown exceeded its bound")
	}
}

func TestRetryBackoffCapsBeforeShifting(t *testing.T) {
	for _, attempt := range []int{8, 63, 1000000} {
		if got := retryBackoff(attempt); got != time.Hour {
			t.Fatalf("attempt=%d got=%v", attempt, got)
		}
	}
	if got := retryBackoff(2); got != time.Minute {
		t.Fatal(got)
	}
}

func (e *outcomeExecutor) CheckCompatibility(context.Context) error { return nil }

type incompatibleExecutor struct{ fakeExecutor }

func (e *incompatibleExecutor) CheckCompatibility(context.Context) error {
	return errors.New("owned incompatible provider")
}

func TestProviderMismatchStopsBeforeRegistrationOrClaims(t *testing.T) {
	configuration := config.DefaultConfig()
	q := &fakeQueue{}
	r := New(configuration, q, &incompatibleExecutor{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := r.Start(context.Background()); err == nil {
		t.Fatal("incompatible provider started a worker")
	}
	if q.registerCalls != 0 {
		t.Fatal("worker registered before provider compatibility was known")
	}
}
