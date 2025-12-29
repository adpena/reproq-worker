package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"math/rand"
	"reproq-worker/internal/config"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/queue"
	"sync"
	"time"
)

type Runner struct {
	cfg       *config.Config
	queue     *queue.Service
	executor  executor.IExecutor
	validator *executor.Validator
	logger    *slog.Logger
	wg        sync.WaitGroup
	metrics   Metrics
}

func New(cfg *config.Config, q *queue.Service, exec executor.IExecutor, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:       cfg,
		queue:     q,
		executor:  exec,
		validator: executor.NewValidator(nil), // Default validation
		logger:    logger,
	}
}

func (r *Runner) Start(ctx context.Context) error {
	r.logger.Info("Starting worker runner", "queue", r.cfg.QueueName)
	defer r.metrics.Report()

	// Start background lease reaper
	go r.runReaper(ctx)

	// Add jitter to poll interval to avoid thundering herd
	pollJitter := time.Duration(rand.Intn(200)) * time.Millisecond
	ticker := time.NewTicker(r.cfg.PollInterval + pollJitter)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Worker received shutdown signal, waiting for tasks to finish...", "timeout", r.cfg.ShutdownTimeout)
			
			// Graceful shutdown with timeout
			waitDone := make(chan struct{})
			go func() {
				r.wg.Wait()
				close(waitDone)
			}()

			select {
			case <-waitDone:
				r.logger.Info("All tasks finished cleanly")
			case <-time.After(r.cfg.ShutdownTimeout):
				r.logger.Warn("Shutdown timeout reached, some tasks may be interrupted")
			}
			return nil
		case <-ticker.C:
			for {
				if ctx.Err() != nil {
					break
				}
				
				processed, err := r.processNext(ctx)
				if err != nil {
					if !errors.Is(err, queue.ErrNoTasks) {
						r.logger.Error("Error processing task", "error", err)
					}
					break
				}
				if !processed {
					break
				}
			}
		}
	}
}

func (r *Runner) runReaper(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := r.queue.ReapExpiredLeases(ctx)
			if err != nil {
				r.logger.Error("Failed to reap expired leases", "error", err)
			} else if count > 0 {
				r.logger.Info("Reaped expired leases", "count", count)
			}
		}
	}
}

func (r *Runner) processNext(ctx context.Context) (bool, error) {
	// 1. Claim
	startClaim := time.Now()
	task, err := r.queue.Claim(ctx, r.cfg.QueueName, r.cfg.WorkerID, r.cfg.LeaseDuration)
	if err != nil {
		if errors.Is(err, queue.ErrNoTasks) {
			return false, nil
		}
		return false, err
	}
	duration := time.Since(startClaim)
	r.metrics.RecordClaim(duration)
	tasksClaimed.WithLabelValues(r.cfg.QueueName).Inc()
	claimDuration.WithLabelValues(r.cfg.QueueName).Observe(duration.Seconds())

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.executeTask(ctx, task, r.cfg.LeaseDuration)
	}()

	return true, nil
}

func (r *Runner) executeTask(parentCtx context.Context, task *queue.TaskRun, leaseDuration time.Duration) {
	logger := r.logger.With("task_id", task.ID, "spec_hash", task.SpecHash)
	
	// Security: Validate RunSpec before execution
	var payload struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(task.PayloadJSON, &payload); err == nil && payload.Task != "" {
		if err := r.validator.Validate(payload.Task); err != nil {
			logger.Error("Security validation failed", "error", err)
			r.queue.CompleteFailure(context.Background(), task.ID, r.cfg.WorkerID, []byte(err.Error()), "", "", -1, false, time.Now())
			tasksCompleted.WithLabelValues(r.cfg.QueueName, "security_error").Inc()
			return
		}
	}

	logger.Info("Processing task", "attempt", task.AttemptCount)

	execCtx, cancelExec := context.WithCancel(parentCtx)
	defer cancelExec()

	startExec := time.Now()
	
	go func() {
		if err := r.runHeartbeat(execCtx, task.ID, leaseDuration); err != nil {
			logger.Error("Heartbeat failure or lease lost, cancelling execution", "error", err)
			cancelExec() 
		}
	}()

	execTimeout := 1 * time.Hour 
	result, execErr := r.executor.Execute(execCtx, task.PayloadJSON, execTimeout)

	completionCtx, completionCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer completionCancel()

	if execErr != nil {
		if errors.Is(execErr, context.Canceled) {
			logger.Warn("Task execution cancelled (likely due to lease loss or shutdown)")
			tasksCompleted.WithLabelValues(r.cfg.QueueName, "cancelled").Inc()
		} else {
			logger.Error("Execution infrastructure failed", "error", execErr)
			tasksCompleted.WithLabelValues(r.cfg.QueueName, "infra_error").Inc()
		}
		r.handleFailure(completionCtx, task, nil, "", "", -1, true)
		r.metrics.RecordFailure()
		return
	}

	totalExecDuration := time.Since(startExec)
	queueWait := time.Since(task.CreatedAt)

	if result.ExitCode == 0 {
		logger.Info("Task completed successfully")
		if err := r.queue.CompleteSuccess(completionCtx, task.ID, r.cfg.WorkerID, result.JSONResult, result.Stdout, result.Stderr, result.ExitCode); err != nil {
			logger.Error("Failed to mark success (fencing error?)", "error", err)
		}
		r.metrics.RecordSuccess(queueWait, totalExecDuration)
		tasksCompleted.WithLabelValues(r.cfg.QueueName, "success").Inc()
		execDuration.WithLabelValues(r.cfg.QueueName).Observe(totalExecDuration.Seconds())
		queueWaitTime.WithLabelValues(r.cfg.QueueName).Observe(queueWait.Seconds())
	} else {
		logger.Warn("Task execution failed", "exit_code", result.ExitCode)
		shouldRetry := task.AttemptCount < task.MaxAttempts
		if err := r.handleFailure(completionCtx, task, result.ErrorJSON, result.Stdout, result.Stderr, result.ExitCode, shouldRetry); err != nil {
			logger.Error("Failed to mark failure", "error", err)
		}
		
		if shouldRetry {
			r.metrics.RecordRetry()
			tasksCompleted.WithLabelValues(r.cfg.QueueName, "retry").Inc()
		} else {
			r.metrics.RecordFailure()
			tasksCompleted.WithLabelValues(r.cfg.QueueName, "failure").Inc()
		}
	}
}

func (r *Runner) runHeartbeat(ctx context.Context, taskID int64, duration time.Duration) error {
	ticker := time.NewTicker(duration / 3) // Renew every 1/3 of lease
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.queue.Heartbeat(ctx, taskID, duration); err != nil {
				return err
			}
		}
	}
}

func (r *Runner) handleFailure(ctx context.Context, task *queue.TaskRun, errorJSON []byte, stdout, stderr string, exitCode int, shouldRetry bool) error {
	// Simple exponential backoff: 2^attempts * 1 second
	backoff := time.Duration(math.Pow(2, float64(task.AttemptCount))) * time.Second
	nextRun := time.Now().Add(backoff)

	return r.queue.CompleteFailure(ctx, task.ID, r.cfg.WorkerID, errorJSON, stdout, stderr, exitCode, shouldRetry, nextRun)
}
