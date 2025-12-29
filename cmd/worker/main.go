package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"reproq-worker/internal/config"
	"reproq-worker/internal/db"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/logging"
	"reproq-worker/internal/queue"
	"reproq-worker/internal/runner"
	"syscall"
)

func main() {
	// CLI Flags
	requeueID := flag.Int64("requeue-id", 0, "Re-enqueue a task by its ID")
	requeueHash := flag.String("requeue-hash", "", "Re-enqueue a task by its SpecHash")
	flag.Parse()

	// 1. Config
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 2. Logging
	logger := logging.Init(cfg.WorkerID)
	// If simply running a CLI command, maybe reduce log noise? Keeping it standard for now.

	// 3. Database
	ctx := context.Background()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	qService := queue.NewService(pool)

	// Handle Requeue CLI commands
	if *requeueID != 0 {
		newID, err := qService.Requeue(ctx, *requeueID)
		if err != nil {
			logger.Error("Failed to requeue task", "id", *requeueID, "error", err)
			os.Exit(1)
		}
		logger.Info("Successfully requeued task", "old_id", *requeueID, "new_id", newID)
		return
	}

	if *requeueHash != "" {
		newID, err := qService.RequeueByHash(ctx, *requeueHash)
		if err != nil {
			logger.Error("Failed to requeue task", "hash", *requeueHash, "error", err)
			os.Exit(1)
		}
		logger.Info("Successfully requeued task", "hash", *requeueHash, "new_id", newID)
		return
	}

	logger.Info("Initializing worker...", "config", cfg)

	// 4. Components
	var execService executor.IExecutor
	if cfg.ExecMode == "mock" {
		execService = executor.NewMockExecutor(cfg.ExecSleep)
	} else {
		execService = executor.NewShellExecutor(cfg.PythonCommand)
	}
	
	runService := runner.New(cfg, qService, execService, logger)

	// 5. Signal Handling (Graceful Shutdown)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		logger.Info("Received signal, shutting down...", "signal", sig)
		cancel()
	}()

	// 6. Start Loop
	if err := runService.Start(ctx); err != nil {
		logger.Error("Runner exited with error", "error", err)
		os.Exit(1)
	}

	logger.Info("Worker stopped cleanly")
}