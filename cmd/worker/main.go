package main

import (
	"context"
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
	// 1. Config
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 2. Logging
	logger := logging.Init(cfg.WorkerID)
	logger.Info("Initializing worker...", "config", cfg)

	// 3. Database
	ctx := context.Background()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// 4. Components
	qService := queue.NewService(pool)
	execService := executor.New(cfg.PythonCommand)
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