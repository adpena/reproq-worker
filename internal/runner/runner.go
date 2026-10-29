package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"reproq-worker/internal/config"
	"reproq-worker/internal/events"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/queue"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	tasksProcessed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reproq_tasks_processed_total",
		Help: "Total number of tasks processed",
	}, []string{"status", "queue"})
)

type Runner struct {
	cfg       *config.Config
	queue     QueueService
	executor  executor.IExecutor
	validator *executor.Validator
	logger    *slog.Logger
	publisher events.Publisher
	wg        sync.WaitGroup
	pool      chan struct{} // Concurrency limiter
	queueIdx  int
}

type QueueService interface {
	RegisterWorker(ctx context.Context, id, hostname string, concurrency int, queues []string, version string) error
	Reclaim(ctx context.Context) (int64, error)
	UpdateWorkerHeartbeat(ctx context.Context, id string) error
	Claim(ctx context.Context, workerID string, queueName string, leaseSeconds int, priorityAgingFactor float64) (*queue.TaskRun, error)
	Heartbeat(ctx context.Context, resultID int64, workerID, leaseToken string, leaseSeconds int) (bool, error)
	CompleteSuccess(ctx context.Context, resultID int64, workerID, leaseToken string, returnJSON json.RawMessage, logsURI *string) error
	CompleteFailure(ctx context.Context, resultID int64, workerID, leaseToken string, errorsJSON json.RawMessage, retry bool, nextRun time.Time, logsURI *string) (queue.TaskStatus, error)
}

func New(cfg *config.Config, q QueueService, exec executor.IExecutor, logger *slog.Logger, publisher events.Publisher) *Runner {
	if publisher == nil {
		publisher = events.NoopPublisher{}
	}
	return &Runner{
		cfg:       cfg,
		queue:     q,
		executor:  exec,
		validator: executor.NewValidator(cfg.AllowedTaskModules),
		logger:    logger,
		publisher: publisher,
		pool:      make(chan struct{}, cfg.MaxConcurrency),
	}
}

func (r *Runner) Start(ctx context.Context) error {
	if err := r.executor.CheckCompatibility(ctx); err != nil {
		return fmt.Errorf("executor startup compatibility check failed: %w", err)
	}
	r.logger.Info("Starting reproq worker", "concurrency", r.cfg.MaxConcurrency, "queues", r.cfg.QueueNames)

	hostname, _ := os.Hostname()
	if err := r.queue.RegisterWorker(ctx, r.cfg.WorkerID, hostname, r.cfg.MaxConcurrency, r.cfg.QueueNames, r.cfg.Version); err != nil {
		r.logger.Warn("Failed to register worker", "error", err)
	}

	if r.cfg.ReclaimIntervalSeconds > 0 {
		reclaimStart := time.Now()
		if reclaimed, err := r.queue.Reclaim(ctx); err != nil {
			r.logger.Warn("Failed to reclaim expired tasks", "error", err)
		} else if reclaimed > 0 {
			r.logger.Info("Reclaimed expired tasks", "count", reclaimed)
		}
		dbOpDuration.WithLabelValues("reclaim").Observe(time.Since(reclaimStart).Seconds())

		go func() {
			ticker := time.NewTicker(time.Duration(r.cfg.ReclaimIntervalSeconds) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reclaimStart := time.Now()
					reclaimed, err := r.queue.Reclaim(ctx)
					if err != nil {
						r.logger.Warn("Failed to reclaim expired tasks", "error", err)
					} else if reclaimed > 0 {
						r.logger.Info("Reclaimed expired tasks", "count", reclaimed)
					}
					dbOpDuration.WithLabelValues("reclaim").Observe(time.Since(reclaimStart).Seconds())
				}
			}
		}()
	}

	// Worker Heartbeat
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.queue.UpdateWorkerHeartbeat(ctx, r.cfg.WorkerID); err != nil {
					r.logger.Warn("Failed to update worker heartbeat", "error", err)
				}
			}
		}
	}()

	// Simple round-robin or fair polling across configured queues
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Shutdown requested, waiting for tasks...")
			return r.waitForTasks()
		case r.pool <- struct{}{}:
			// Acquired a slot in the pool
			task, err := r.poll(ctx)
			if err != nil {
				<-r.pool // Release slot
				if errors.Is(err, queue.ErrNoTasks) {
					r.sleep(ctx)
					continue
				}
				r.logger.Error("Polling error", "error", err)
				r.sleep(ctx)
				continue
			}

			r.wg.Add(1)
			go r.runTask(ctx, task)
		}
	}
}

func (r *Runner) waitForTasks() error {
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	default:
	}
	shutdownTimeout := r.cfg.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = config.DefaultConfig().ShutdownTimeout
	}
	timer := time.NewTimer(shutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("tasks did not stop within shutdown timeout %s", shutdownTimeout)
	}
}

func (r *Runner) poll(ctx context.Context) (*queue.TaskRun, error) {
	if len(r.cfg.QueueNames) == 0 {
		return nil, errors.New("no queues configured")
	}

	start := r.queueIdx % len(r.cfg.QueueNames)
	for i := 0; i < len(r.cfg.QueueNames); i++ {
		idx := (start + i) % len(r.cfg.QueueNames)
		queueName := r.cfg.QueueNames[idx]
		claimStart := time.Now()
		task, err := r.queue.Claim(
			ctx,
			r.cfg.WorkerID,
			queueName,
			r.cfg.LeaseSeconds,
			r.cfg.PriorityAgingFactor,
		)
		claimElapsed := time.Since(claimStart)
		claimDuration.WithLabelValues(queueName).Observe(claimElapsed.Seconds())
		dbOpDuration.WithLabelValues("claim").Observe(claimElapsed.Seconds())
		if err == nil {
			r.queueIdx = (idx + 1) % len(r.cfg.QueueNames)
			tasksClaimed.WithLabelValues(queueName).Inc()
			if !task.EnqueuedAt.IsZero() {
				queueWaitTime.WithLabelValues(queueName).Observe(time.Since(task.EnqueuedAt).Seconds())
			}
			return task, nil
		}
		if !errors.Is(err, queue.ErrNoTasks) {
			return nil, err
		}
	}

	r.queueIdx = (start + 1) % len(r.cfg.QueueNames)
	return nil, queue.ErrNoTasks
}

func (r *Runner) sleep(ctx context.Context) {
	backoffRange := r.cfg.PollMaxBackoff - r.cfg.PollMinBackoff
	backoff := r.cfg.PollMinBackoff
	if backoffRange > 0 {
		backoff = time.Duration(rand.Int63n(int64(backoffRange))) + r.cfg.PollMinBackoff
	}
	select {
	case <-time.After(backoff):
	case <-ctx.Done():
	}
}

func (r *Runner) runTask(ctx context.Context, task *queue.TaskRun) {
	defer func() {
		<-r.pool
		r.wg.Done()
	}()

	logger := r.logger.With("result_id", task.ResultID, "spec_hash", task.SpecHash)

	// The executor imports spec_json.task_path. A denormalized projection may
	// accelerate queue queries, but must never authorize a different task.
	payload, specErr := task.ExecutionPayload(r.cfg.MaxPayloadBytes)
	specTaskPath := ""
	if specErr == nil {
		specTaskPath, specErr = r.extractTaskPath(payload)
	}
	if specErr == nil && task.TaskPath != nil && *task.TaskPath != specTaskPath {
		specErr = errors.New("task_path projection does not match spec_json.task_path")
	}
	if specErr != nil {
		err := specErr
		logger.Error("Invalid task spec", "error", err)
		completionCtx, compCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer compCancel()
		if status, ack := r.failTask(completionCtx, task, err, "invalid_spec", "", false, nil); ack {
			r.recordFailureCompletion(task, status)
			r.publishEvent("error", "task_invalid", "invalid task spec", task, "", map[string]string{"reason": "invalid_spec", "status": string(status)})
		}
		return
	}
	if err := r.validator.Validate(specTaskPath); err != nil {
		logger.Warn("Task rejected by validator", "task_path", specTaskPath, "error", err)
		completionCtx, compCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer compCancel()
		if status, ack := r.failTask(completionCtx, task, err, "security_violation", specTaskPath, false, nil); ack {
			r.recordFailureCompletion(task, status)
			r.publishEvent("warn", "task_rejected", "task rejected by validator", task, specTaskPath, map[string]string{"reason": "security_violation", "status": string(status)})
		}
		return
	}

	logger.Info("Executing task", "task_path", specTaskPath)
	r.publishEvent("info", "task_started", "task started", task, specTaskPath, nil)

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var cancelRequested atomic.Bool

	// Heartbeat
	if r.cfg.HeartbeatSeconds > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(r.cfg.HeartbeatSeconds) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-execCtx.Done():
					return
				case <-ticker.C:
					heartbeatStart := time.Now()
					cancelled, err := r.queue.Heartbeat(execCtx, task.ResultID, r.cfg.WorkerID, task.LeaseToken, r.cfg.LeaseSeconds)
					dbOpDuration.WithLabelValues("heartbeat").Observe(time.Since(heartbeatStart).Seconds())
					if err != nil {
						logger.Error("Heartbeat failed", "error", err)
						cancel() // Stop execution
						return
					}
					if cancelled {
						if cancelRequested.CompareAndSwap(false, true) {
							logger.Warn("Remote cancellation requested")
						}
						cancel()
						return
					}
				}
			}
		}()
	}

	execStart := time.Now()
	timeout := r.cfg.ExecTimeout
	if task.TimeoutSeconds > 0 {
		timeout = time.Duration(task.TimeoutSeconds) * time.Second
	}
	env, stdout, stderr, err := r.executor.Execute(execCtx, executor.Claim{ResultID: task.ResultID, Attempt: task.Attempts, WorkerID: r.cfg.WorkerID, LeaseToken: task.LeaseToken}, payload, timeout)
	cancel() // Stop heartbeat work before attempting a terminal transition.
	execDuration.WithLabelValues(task.QueueName).Observe(time.Since(execStart).Seconds())

	completionCtx, compCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer compCancel()

	logsURI := r.persistExecutionLogs(task, stdout, stderr)

	if cancelRequested.Load() {
		logger.Warn("Task cancelled by request")
		errObj, _ := json.Marshal(map[string]interface{}{
			"kind":      "cancelled",
			"message":   "Task cancelled by request",
			"task_path": specTaskPath,
			"at":        time.Now(),
			"worker_id": r.cfg.WorkerID,
		})
		completeStart := time.Now()
		completionStatus, completionErr := r.queue.CompleteFailure(completionCtx, task.ResultID, r.cfg.WorkerID, task.LeaseToken, errObj, false, time.Now(), logsURI)
		dbOpDuration.WithLabelValues("complete_failure").Observe(time.Since(completeStart).Seconds())
		if err := completionErr; err != nil {
			logger.Error("Failed to mark cancellation", "error", err)
			return
		}
		r.recordFailureCompletion(task, completionStatus)
		r.publishEvent("warn", "task_cancelled", "task cancelled by request", task, specTaskPath, map[string]string{
			"reason": "cancel_requested", "status": string(completionStatus),
		})
		return
	}

	// A normal task exception exits nonzero with an explicit failure envelope.
	// Preserve its classification, while every other process failure is an
	// infrastructure error and can never be accepted as task success.
	if env == nil && err == nil {
		err = errors.New("executor returned no result")
	}
	isTaskException := env != nil && !env.Ok && errors.Is(err, executor.ErrProcessExit) && !errors.Is(err, executor.ErrProtocol)
	if err != nil && !isTaskException {
		logger.Error("Execution pipeline error", "error", err)
		// Internal infrastructure error or timeout
		errObj, _ := json.Marshal(map[string]interface{}{
			"kind":    "infra_error",
			"message": err.Error(),
			"at":      time.Now(),
		})
		completeStart := time.Now()
		shouldRetry := task.Attempts < task.MaxAttempts
		completionStatus, completionErr := r.queue.CompleteFailure(completionCtx, task.ResultID, r.cfg.WorkerID, task.LeaseToken, errObj, shouldRetry, time.Now().Add(10*time.Second), logsURI)
		dbOpDuration.WithLabelValues("complete_failure").Observe(time.Since(completeStart).Seconds())
		if err := completionErr; err != nil {
			logger.Error("Failed to mark infra error", "error", err)
			return
		}
		r.recordFailureCompletion(task, completionStatus)
		r.publishEvent("error", "task_infra_error", "execution infrastructure error", task, specTaskPath, map[string]string{
			"reason": "infra_error", "status": string(completionStatus),
		})
		return
	}

	if env.Ok {
		completeStart := time.Now()
		completionErr := r.queue.CompleteSuccess(completionCtx, task.ResultID, r.cfg.WorkerID, task.LeaseToken, env.Return, logsURI)
		dbOpDuration.WithLabelValues("complete_success").Observe(time.Since(completeStart).Seconds())
		if errors.Is(completionErr, queue.ErrCancelRequested) {
			if status, ack := r.failTask(completionCtx, task, completionErr, "cancelled", specTaskPath, false, logsURI); ack {
				r.recordFailureCompletion(task, status)
				r.publishEvent("warn", "task_cancelled", "task cancelled by request", task, specTaskPath, nil)
			}
			return
		}
		if err := completionErr; err != nil {
			logger.Error("Failed to mark success", "error", err)
			return
		}
		logger.Info("Task successful")
		r.recordCompletion(task, "success")
		r.publishEvent("info", "task_success", "task completed", task, specTaskPath, nil)
	} else {
		logger.Warn("Task failed", "msg", env.Message)
		errObj, _ := json.Marshal(map[string]interface{}{
			"kind":            "app_error",
			"exception_class": env.ExceptionClass,
			"traceback":       env.Traceback,
			"message":         env.Message,
			"attempt":         task.Attempts,
			"at":              time.Now(),
			"worker_id":       r.cfg.WorkerID,
		})

		shouldRetry := task.Attempts < task.MaxAttempts

		nextRun := time.Now().Add(retryBackoff(task.Attempts))
		completeStart := time.Now()
		completionStatus, completionErr := r.queue.CompleteFailure(completionCtx, task.ResultID, r.cfg.WorkerID, task.LeaseToken, errObj, shouldRetry, nextRun, logsURI)
		dbOpDuration.WithLabelValues("complete_failure").Observe(time.Since(completeStart).Seconds())
		if err := completionErr; err != nil {
			logger.Error("Failed to mark failure", "error", err)
			return
		}
		r.recordFailureCompletion(task, completionStatus)
		eventType := "task_failed"
		level := "error"
		message := "task failed"
		metadata := map[string]string{
			"exception_class": env.ExceptionClass, "status": string(completionStatus),
		}
		if completionStatus == queue.StatusReady {
			eventType = "task_retry"
			level = "warn"
			message = "task failed; retrying"
			metadata["next_run"] = nextRun.Format(time.RFC3339Nano)
		}
		if completionStatus == queue.StatusCancelled {
			eventType = "task_cancelled"
			level = "warn"
			message = "task cancelled by request"
		}
		if env.Message != "" {
			metadata["error_message"] = env.Message
		}
		r.publishEvent(level, eventType, message, task, specTaskPath, metadata)
	}
}

func retryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 30 * time.Second
	}
	if attempt >= 8 {
		return time.Hour
	}
	return (30 * time.Second) << uint(attempt-1)
}

func (r *Runner) recordFailureCompletion(task *queue.TaskRun, status queue.TaskStatus) {
	if status == queue.StatusCancelled {
		r.recordCompletion(task, "cancelled")
	} else {
		r.recordCompletion(task, "failure")
	}
}

func (r *Runner) recordCompletion(task *queue.TaskRun, status string) {
	tasksProcessed.WithLabelValues(status, task.QueueName).Inc()
	tasksCompleted.WithLabelValues(task.QueueName, status).Inc()
}

func (r *Runner) publishEvent(level, eventType, message string, task *queue.TaskRun, taskPath string, metadata map[string]string) {
	if r.publisher == nil || task == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]string{}
	}
	if taskPath != "" {
		metadata["task_path"] = taskPath
	}
	if task.PeriodicName != nil {
		metadata["periodic_name"] = *task.PeriodicName
	}
	if task.ScheduledFor != nil {
		metadata["scheduled_for"] = task.ScheduledFor.UTC().Format(time.RFC3339Nano)
	}
	if task.PeriodicProvider != nil {
		metadata["scheduler_provider"] = *task.PeriodicProvider
	}
	if task.Attempts > 0 {
		metadata["attempt"] = strconv.Itoa(task.Attempts)
	}
	if task.MaxAttempts > 0 {
		metadata["max_attempts"] = strconv.Itoa(task.MaxAttempts)
	}
	if _, ok := metadata["role"]; !ok && r.cfg.ProcessRole != "" {
		metadata["role"] = r.cfg.ProcessRole
	}
	r.publisher.Publish(events.Event{
		Timestamp: time.Now(),
		Level:     level,
		Type:      eventType,
		Message:   message,
		Queue:     task.QueueName,
		TaskID:    task.ResultID,
		WorkerID:  r.cfg.WorkerID,
		Metadata:  metadata,
	})
}

func (r *Runner) extractTaskPath(specJSON json.RawMessage) (string, error) {
	if len(specJSON) == 0 {
		return "", errors.New("spec_json is empty")
	}
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return "", err
	}
	var taskPath string
	if err := json.Unmarshal(spec["task_path"], &taskPath); err != nil || taskPath == "" {
		return "", errors.New("spec_json missing task_path")
	}
	return taskPath, nil
}

func (r *Runner) failTask(ctx context.Context, task *queue.TaskRun, err error, kind string, taskPath string, shouldRetry bool, logsURI *string) (queue.TaskStatus, bool) {
	payload := map[string]interface{}{
		"kind":    kind,
		"message": err.Error(),
		"at":      time.Now(),
	}
	if taskPath != "" {
		payload["task_path"] = taskPath
	}
	errObj, _ := json.Marshal(payload)
	completeStart := time.Now()
	status, completionErr := r.queue.CompleteFailure(ctx, task.ResultID, r.cfg.WorkerID, task.LeaseToken, errObj, shouldRetry, time.Now(), logsURI)
	dbOpDuration.WithLabelValues("complete_failure").Observe(time.Since(completeStart).Seconds())
	if completionErr != nil {
		r.logger.Error("Failed to acknowledge task failure", "result_id", task.ResultID, "error", completionErr)
	}
	return status, completionErr == nil
}

func (r *Runner) persistExecutionLogs(task *queue.TaskRun, stdout string, stderr string) *string {
	if r.cfg.LogsDir == "" {
		return nil
	}
	if stdout == "" && stderr == "" {
		return nil
	}

	if err := os.MkdirAll(r.cfg.LogsDir, 0o700); err != nil {
		r.logger.Warn("Failed to create logs dir", "error", err, "logs_dir", r.cfg.LogsDir)
		return nil
	}

	filename := fmt.Sprintf("task-%d-attempt-%d.log", task.ResultID, task.Attempts)
	path := filepath.Join(r.cfg.LogsDir, filename)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		r.logger.Warn("Failed to open logs file", "error", err, "path", path)
		return nil
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	_, err = writer.WriteString("STDOUT:\n")
	if err == nil {
		_, err = writer.WriteString(stdout)
	}
	if err == nil && !strings.HasSuffix(stdout, "\n") {
		_, err = writer.WriteString("\n")
	}
	if err == nil {
		_, err = writer.WriteString("STDERR:\n")
	}
	if err == nil {
		_, err = writer.WriteString(stderr)
	}
	if err == nil && !strings.HasSuffix(stderr, "\n") {
		_, err = writer.WriteString("\n")
	}
	if err != nil {
		r.logger.Warn("Failed to persist task logs", "error", err, "path", path)
		return nil
	}
	if err := writer.Flush(); err != nil {
		r.logger.Warn("Failed to flush task logs", "error", err, "path", path)
		return nil
	}

	return &path
}
