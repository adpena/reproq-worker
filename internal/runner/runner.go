package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"os"
	"reproq-worker/internal/config"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/queue"
	"sync"
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
	wg        sync.WaitGroup
	pool      chan struct{} // Concurrency limiter
	queueIdx  int
}

type QueueService interface {
	RegisterWorker(ctx context.Context, id, hostname string, concurrency int, queues []string, version string) error
	Reclaim(ctx context.Context, maxAttemptsDefault int) (int64, error)
	UpdateWorkerHeartbeat(ctx context.Context, id string) error
	Claim(ctx context.Context, workerID string, queueName string, leaseSeconds int, priorityAgingFactor float64) (*queue.TaskRun, error)
	Heartbeat(ctx context.Context, resultID int64, workerID string, leaseSeconds int) (bool, error)
	CompleteSuccess(ctx context.Context, resultID int64, workerID string, returnJSON json.RawMessage) error
	CompleteFailure(ctx context.Context, resultID int64, workerID string, errorsJSON json.RawMessage, retry bool, nextRun time.Time) error
}

func New(cfg *config.Config, q QueueService, exec executor.IExecutor, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:       cfg,
		queue:     q,
		executor:  exec,
		validator: executor.NewValidator(cfg.AllowedTaskModules),
		logger:    logger,
		pool:      make(chan struct{}, cfg.MaxConcurrency),
	}
}

func (r *Runner) Start(ctx context.Context) error {
	r.logger.Info("Starting reproq worker", "concurrency", r.cfg.MaxConcurrency, "queues", r.cfg.QueueNames)

	hostname, _ := os.Hostname()
	if err := r.queue.RegisterWorker(ctx, r.cfg.WorkerID, hostname, r.cfg.MaxConcurrency, r.cfg.QueueNames, r.cfg.Version); err != nil {
		r.logger.Warn("Failed to register worker", "error", err)
	}

	if r.cfg.ReclaimIntervalSeconds > 0 {
		if reclaimed, err := r.queue.Reclaim(ctx, r.cfg.MaxAttemptsDefault); err != nil {
			r.logger.Warn("Failed to reclaim expired tasks", "error", err)
		} else if reclaimed > 0 {
			r.logger.Info("Reclaimed expired tasks", "count", reclaimed)
		}

		go func() {
			ticker := time.NewTicker(time.Duration(r.cfg.ReclaimIntervalSeconds) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reclaimed, err := r.queue.Reclaim(ctx, r.cfg.MaxAttemptsDefault)
					if err != nil {
						r.logger.Warn("Failed to reclaim expired tasks", "error", err)
					} else if reclaimed > 0 {
						r.logger.Info("Reclaimed expired tasks", "count", reclaimed)
					}
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
			r.wg.Wait()
			return nil
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
		claimDuration.WithLabelValues(queueName).Observe(time.Since(claimStart).Seconds())
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
	backoff := time.Duration(rand.Int63n(int64(r.cfg.PollMaxBackoff-r.cfg.PollMinBackoff))) + r.cfg.PollMinBackoff
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

	specTaskPath, err := r.extractTaskPath(task.SpecJSON)
	if err != nil {
		logger.Error("Invalid task spec", "error", err)
		completionCtx, compCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer compCancel()
		r.failTask(completionCtx, task, err, "invalid_spec", "", false)
		tasksProcessed.WithLabelValues("failure", task.QueueName).Inc()
		tasksCompleted.WithLabelValues(task.QueueName, "failure").Inc()
		return
	}
	if err := r.validator.Validate(specTaskPath); err != nil {
		logger.Warn("Task rejected by validator", "task_path", specTaskPath, "error", err)
		completionCtx, compCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer compCancel()
		r.failTask(completionCtx, task, err, "security_violation", specTaskPath, false)
		tasksProcessed.WithLabelValues("failure", task.QueueName).Inc()
		tasksCompleted.WithLabelValues(task.QueueName, "failure").Inc()
		return
	}

	logger.Info("Executing task", "task_path", specTaskPath)

	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()

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
					cancelled, err := r.queue.Heartbeat(ctx, task.ResultID, r.cfg.WorkerID, r.cfg.LeaseSeconds)
					if err != nil {
						logger.Error("Heartbeat failed", "error", err)
						cancel() // Stop execution
						return
					}
					if cancelled {
						logger.Warn("Remote cancellation requested")
						cancel()
						return
					}
				}
			}
		}()
	}

	execStart := time.Now()
	env, _, _, err := r.executor.Execute(execCtx, task.ResultID, task.Attempts, task.SpecJSON, r.cfg.ExecTimeout)
	execDuration.WithLabelValues(task.QueueName).Observe(time.Since(execStart).Seconds())

	completionCtx, compCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer compCancel()

	if err != nil {
		logger.Error("Execution pipeline error", "error", err)
		// Internal infrastructure error or timeout
		errObj, _ := json.Marshal(map[string]interface{}{
			"kind":    "infra_error",
			"message": err.Error(),
			"at":      time.Now(),
		})
		r.queue.CompleteFailure(completionCtx, task.ResultID, r.cfg.WorkerID, errObj, true, time.Now().Add(10*time.Second))
		tasksCompleted.WithLabelValues(task.QueueName, "failure").Inc()
		return
	}

	if env.Ok {
		logger.Info("Task successful")
		tasksProcessed.WithLabelValues("success", task.QueueName).Inc()
		tasksCompleted.WithLabelValues(task.QueueName, "success").Inc()
		r.queue.CompleteSuccess(completionCtx, task.ResultID, r.cfg.WorkerID, env.Return)
	} else {
		logger.Warn("Task failed", "msg", env.Message)
		tasksProcessed.WithLabelValues("failure", task.QueueName).Inc()
		tasksCompleted.WithLabelValues(task.QueueName, "failure").Inc()
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

		// Exponential backoff: 2^attempt * base_delay (e.g. 30s)
		// attempt_index 0 -> 30s
		// attempt_index 1 -> 60s
		// attempt_index 2 -> 120s
		attemptIndex := task.Attempts - 1
		if attemptIndex < 0 {
			attemptIndex = 0
		}
		backoffSeconds := (1 << uint(attemptIndex)) * 30
		if backoffSeconds > 3600 { // Cap at 1 hour
			backoffSeconds = 3600
		}

		nextRun := time.Now().Add(time.Duration(backoffSeconds) * time.Second)
		r.queue.CompleteFailure(completionCtx, task.ResultID, r.cfg.WorkerID, errObj, shouldRetry, nextRun)
	}
}

type taskSpec struct {
	TaskPath string `json:"task_path"`
}

func (r *Runner) extractTaskPath(specJSON json.RawMessage) (string, error) {
	if len(specJSON) == 0 {
		return "", errors.New("spec_json is empty")
	}
	var spec taskSpec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return "", err
	}
	if spec.TaskPath == "" {
		return "", errors.New("spec_json missing task_path")
	}
	return spec.TaskPath, nil
}

func (r *Runner) failTask(ctx context.Context, task *queue.TaskRun, err error, kind string, taskPath string, shouldRetry bool) {
	payload := map[string]interface{}{
		"kind":    kind,
		"message": err.Error(),
		"at":      time.Now(),
	}
	if taskPath != "" {
		payload["task_path"] = taskPath
	}
	errObj, _ := json.Marshal(payload)
	_ = r.queue.CompleteFailure(ctx, task.ResultID, r.cfg.WorkerID, errObj, shouldRetry, time.Now())
}
