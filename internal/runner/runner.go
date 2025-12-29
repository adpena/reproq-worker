package runner

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"reproq-worker/internal/config"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/models"
	"reproq-worker/internal/queue"
	"time"
)

type Runner struct {
	cfg      *config.Config
	queue    *queue.Service
	executor *executor.Executor
	logger   *slog.Logger
}

func New(cfg *config.Config, q *queue.Service, exec *executor.Executor, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:      cfg,
		queue:    q,
		executor: exec,
		logger:   logger,
	}
}

func (r *Runner) Start(ctx context.Context) error {
	r.logger.Info("Starting worker runner", "queue", r.cfg.QueueName)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Worker shutting down")
			return nil
		case <-ticker.C:
			// Loop until no tasks are left, then wait for ticker
			for {
				if ctx.Err() != nil {
					return nil
				}
				
				processed, err := r.processNext(ctx)
				if err != nil {
					if !errors.Is(err, queue.ErrNoTasks) {
						r.logger.Error("Error processing task", "error", err)
					}
					// If error or no tasks, break inner loop and wait for ticker
					break
				}
				if !processed {
					break
				}
				// If processed successfully, try to get another one immediately (drain queue)
			}
		}
	}
}

func (r *Runner) processNext(ctx context.Context) (bool, error) {
	// 1. Claim
	// Lease duration: generous 5 minutes, renewed by heartbeat
	leaseDuration := 5 * time.Minute
	task, err := r.queue.Claim(ctx, r.cfg.QueueName, r.cfg.WorkerID, leaseDuration)
	if err != nil {
		if errors.Is(err, queue.ErrNoTasks) {
			return false, nil // No work
		}
		return false, err
	}

	logger := r.logger.With("task_id", task.ID, "spec_hash", task.SpecHash)
	logger.Info("Claimed task", "attempt", task.AttemptCount)

	// 2. Start Heartbeat
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	
	go r.runHeartbeat(hbCtx, task.ID, leaseDuration)

	// 3. Execute
	// Use a shorter timeout for execution than the lease if desired, or same.
	// Let's assume max execution time is 1 hour for now, or configurable.
	execTimeout := 1 * time.Hour 
	result, execErr := r.executor.Execute(ctx, task.PayloadJSON, execTimeout)

	// 4. Handle Result
	if execErr != nil {
		logger.Error("Execution infrastructure failed", "error", execErr)
		// This is an internal error (e.g. can't start process), likely retryable
		return true, r.handleFailure(ctx, task, nil, "", "", -1, true)
	}

	if result.ExitCode == 0 {
		logger.Info("Task completed successfully")
		if err := r.queue.CompleteSuccess(ctx, task.ID, result.JSONResult, result.Stdout, result.Stderr, result.ExitCode); err != nil {
			logger.Error("Failed to mark success", "error", err)
			return true, err
		}
	} else {
		logger.Warn("Task execution failed", "exit_code", result.ExitCode, "stderr", result.Stderr)
		// Determine retry logic
		shouldRetry := task.AttemptCount < task.MaxAttempts
		return true, r.handleFailure(ctx, task, result.ErrorJSON, result.Stdout, result.Stderr, result.ExitCode, shouldRetry)
	}

	return true, nil
}

func (r *Runner) runHeartbeat(ctx context.Context, taskID int64, duration time.Duration) {
	ticker := time.NewTicker(duration / 3) // Renew every 1/3 of lease
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.queue.Heartbeat(ctx, taskID, duration); err != nil {
				r.logger.Error("Heartbeat failed", "task_id", taskID, "error", err)
				// If heartbeat fails, we might be losing the lock. 
				// In a strict system, we might want to cancel execution.
			}
		}
	}
}

func (r *Runner) handleFailure(ctx context.Context, task *models.TaskRun, errorJSON []byte, stdout, stderr string, exitCode int, shouldRetry bool) error {
	// Simple exponential backoff: 2^attempts * 1 second
	backoff := time.Duration(math.Pow(2, float64(task.AttemptCount))) * time.Second
	nextRun := time.Now().Add(backoff)

	return r.queue.CompleteFailure(ctx, task.ID, errorJSON, stdout, stderr, exitCode, shouldRetry, nextRun)
}
