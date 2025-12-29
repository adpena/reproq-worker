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
	cfg      *config.Config
	queue    *queue.Service
	executor executor.IExecutor
	logger   *slog.Logger
	wg       sync.WaitGroup
	pool     chan struct{} // Concurrency limiter
}

func New(cfg *config.Config, q *queue.Service, exec executor.IExecutor, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:      cfg,
		queue:    q,
		executor: exec,
		logger:   logger,
		pool:     make(chan struct{}, cfg.MaxConcurrency),
	}
}

func (r *Runner) Start(ctx context.Context) error {
	r.logger.Info("Starting reproq worker", "concurrency", r.cfg.MaxConcurrency)

	hostname, _ := os.Hostname()
	if err := r.queue.RegisterWorker(ctx, r.cfg.WorkerID, hostname, r.cfg.MaxConcurrency, r.cfg.QueueNames, "0.1.0"); err != nil {
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
	// For MVP, just poll the first queue. Future: round-robin r.cfg.QueueNames
	return r.queue.Claim(ctx, r.cfg.WorkerID, r.cfg.QueueNames[0], r.cfg.LeaseSeconds)
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
	logger.Info("Executing task")

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

	env, _, _, err := r.executor.Execute(execCtx, task.ResultID, task.Attempts, task.SpecJSON, r.cfg.ExecTimeout)

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
		return
	}

	if env.Ok {
		logger.Info("Task successful")
		tasksProcessed.WithLabelValues("success", task.QueueName).Inc()
		r.queue.CompleteSuccess(completionCtx, task.ResultID, r.cfg.WorkerID, env.Return)
	} else {
		logger.Warn("Task failed", "msg", env.Message)
		tasksProcessed.WithLabelValues("failure", task.QueueName).Inc()
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
