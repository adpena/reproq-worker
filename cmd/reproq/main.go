package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"reproq-worker/internal/config"
	"reproq-worker/internal/db"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/logging"
	"reproq-worker/internal/queue"
	"reproq-worker/internal/runner"
	"reproq-worker/internal/web"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: reproq <worker|loadgen|verify|torture> [args]")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "worker":
		runWorker(os.Args[2:])
	case "loadgen":
		runLoadgen(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "torture":
		runTorture(os.Args[2:])
	default:
		fmt.Printf("unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runWorker(args []string) {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	requeueID := fs.Int64("requeue-id", 0, "Re-enqueue a task by its ID")
	requeueHash := fs.String("requeue-hash", "", "Re-enqueue a task by its SpecHash")
	
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	cfg.BindFlags(fs)
	fs.Parse(args)

	logger := logging.Init(cfg.WorkerID)
	ctx := context.Background()
	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	qService := queue.NewService(pool)

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

	logger.Info("Starting reproq worker", "config", cfg)

	var execService executor.IExecutor
	if cfg.ExecMode == "mock" {
		execService = executor.NewMockExecutor(cfg.ExecSleep)
	} else {
		execService = executor.NewShellExecutor(cfg.PythonCommand)
	}
	
	runService := runner.New(cfg, qService, execService, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Health/Metrics server
	webServer := web.NewServer(pool, cfg.HealthAddr)
	go func() {
		logger.Info("Starting health/metrics server", "addr", cfg.HealthAddr)
		if err := webServer.Start(ctx); err != nil && err != http.ErrServerClosed {
			logger.Error("Web server failed", "error", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		logger.Info("Received signal, shutting down...", "signal", sig)
		cancel()
	}()

	if err := runService.Start(ctx); err != nil {
		logger.Error("Runner exited with error", "error", err)
		os.Exit(1)
	}
}

func runLoadgen(args []string) {
	fs := flag.NewFlagSet("loadgen", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	numTasks := fs.Int("tasks", 1000, "Number of tasks to enqueue")
	queues := fs.String("queues", "default,high,low", "Comma-separated list of queues")
	priorityDist := fs.String("priority-dist", "-10,0,10", "Comma-separated list of priorities")
	runAfterPercent := fs.Int("run-after-percent", 10, "Percentage of tasks with run_after in the future")
	payloadSize := fs.Int("payload-size", 100, "Size of payload in bytes")
	seed := fs.Int64("seed", time.Now().UnixNano(), "Random seed")
	fs.Parse(args)

	if *dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	r := rand.New(rand.NewSource(*seed))
	queueList := strings.Split(*queues, ",")
	priorityList := strings.Split(*priorityDist, ",")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	log.Printf("Enqueuing %d tasks...", *numTasks)
	for i := 0; i < *numTasks; i++ {
		q := queueList[r.Intn(len(queueList))]
		p := priorityList[r.Intn(len(priorityList))]
		
		runAfter := time.Now()
		if r.Intn(100) < *runAfterPercent {
			runAfter = runAfter.Add(time.Duration(r.Intn(3600)) * time.Second)
		}

		payload := make([]byte, *payloadSize)
		r.Read(payload)
		specJSON := fmt.Sprintf(`{"data": "%x"}`, payload)
		hash := sha256.Sum256([]byte(specJSON))
		specHash := hex.EncodeToString(hash[:])

		query := "INSERT INTO task_runs (spec_hash, queue_name, payload_json, priority, run_after, max_attempts) VALUES ($1, $2, $3, $4, $5, $6)"
		pool.Exec(ctx, query, specHash, q, specJSON, p, runAfter, 3)
	}
	log.Println("Done enqueuing")
}

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	fs.Parse(args)

	if *dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer pool.Close()

	var totalTasks int
	pool.QueryRow(ctx, "SELECT count(*) FROM task_runs").Scan(&totalTasks)
	fmt.Printf("Total tasks in DB: %d\n", totalTasks)

	var stuckTasks int
	pool.QueryRow(ctx, "SELECT count(*) FROM task_runs WHERE status = 'RUNNING' AND leased_until < NOW()").Scan(&stuckTasks)
	if stuckTasks > 0 {
		fmt.Printf("[FAIL] Found %d stuck tasks with expired leases\n", stuckTasks)
	} else {
		fmt.Printf("[PASS] No stuck tasks\n")
	}
}

func runTorture(args []string) {
	// Simple integration of torture logic
	fs := flag.NewFlagSet("torture", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	fs.Parse(args)
	// (Torture logic omitted for brevity in this example, but would follow the same pattern)
	fmt.Println("Torture test would run here against DSN:", *dsn)
}
