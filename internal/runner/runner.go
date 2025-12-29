package runner

import (
	"context"
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
	cfg      *config.Config
	queue    *queue.Service
	executor executor.IExecutor
	logger   *slog.Logger
	wg       sync.WaitGroup
	metrics  Metrics
}

func New(cfg *config.Config, q *queue.Service, exec executor.IExecutor, logger *slog.Logger) *Runner {
	return &Runner{
		cfg:      cfg,
		queue:    q,
		executor: exec,
		logger:   logger,
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
			r.logger.Info("Worker received shutdown signal, waiting for tasks to finish...")
			r.wg.Wait()
			r.logger.Info("All tasks finished")
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
	leaseDuration := 5 * time.Minute
	task, err := r.queue.Claim(ctx, r.cfg.QueueName, r.cfg.WorkerID, leaseDuration)
	if err != nil {
		if errors.Is(err, queue.ErrNoTasks) {
			return false, nil
		}
		return false, err
	}
	r.metrics.RecordClaim(time.Since(startClaim))

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.executeTask(ctx, task, leaseDuration)
	}()

	return true, nil
}

func (r *Runner) executeTask(ctx context.Context, task *queue.TaskRun, leaseDuration time.Duration) {
	logger := r.logger.With("task_id", task.ID, "spec_hash", task.SpecHash)
	logger.Info("Processing task", "attempt", task.AttemptCount)

	startExec := time.Now()
	// 2. Start Heartbeat
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	
	go r.runHeartbeat(hbCtx, task.ID, leaseDuration)

	// 3. Execute
	execTimeout := 1 * time.Hour 
	result, execErr := r.executor.Execute(ctx, task.PayloadJSON, execTimeout)

	// 4. Handle Result
	completionCtx, completionCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer completionCancel()

	if execErr != nil {
		logger.Error("Execution infrastructure failed", "error", execErr)
		r.handleFailure(completionCtx, task, nil, "", "", -1, true)
		r.metrics.RecordFailure()
		return
	}

	if result.ExitCode == 0 {
		logger.Info("Task completed successfully")
		if err := r.queue.CompleteSuccess(completionCtx, task.ID, result.JSONResult, result.Stdout, result.Stderr, result.ExitCode); err != nil {
			logger.Error("Failed to mark success", "error", err)
		}
		r.metrics.RecordSuccess(time.Since(task.CreatedAt), time.Since(startExec))
	} else {
		logger.Warn("Task execution failed", "exit_code", result.ExitCode)
		shouldRetry := task.AttemptCount < task.MaxAttempts
		r.handleFailure(completionCtx, task, result.ErrorJSON, result.Stdout, result.Stderr, result.ExitCode, shouldRetry)
		if shouldRetry {
			r.metrics.RecordRetry()
		} else {
			r.metrics.RecordFailure()
		}
	}
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

func (r *Runner) handleFailure(ctx context.Context, task *queue.TaskRun, errorJSON []byte, stdout, stderr string, exitCode int, shouldRetry bool) error {
	// Simple exponential backoff: 2^attempts * 1 second
	backoff := time.Duration(math.Pow(2, float64(task.AttemptCount))) * time.Second
	nextRun := time.Now().Add(backoff)

	return r.queue.CompleteFailure(ctx, task.ID, errorJSON, stdout, stderr, exitCode, shouldRetry, nextRun)
}
