package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	case "triage":
		runTriage(os.Args[2:])
	case "limit":
		runLimit(os.Args[2:])
	case "cancel":
		runCancel(os.Args[2:])
	case "beat":
		runBeat(os.Args[2:])
	case "schedule":
		runSchedule(os.Args[2:])
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
	fs := flag.NewFlagSet("torture", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	fs.Parse(args)
	fmt.Println("Torture test would run here against DSN:", *dsn)
}

func runBeat(args []string) {
	fs := flag.NewFlagSet("beat", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	interval := fs.Duration("interval", 10*time.Second, "Polling interval")
	fs.Parse(args)

	if *dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	log.Printf("Starting reproq beat (polling every %v)", *interval)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-sigChan:
			log.Println("Beat shutting down...")
			return
		case <-ticker.C:
			count, err := q.EnqueueDuePeriodicTasks(ctx)
			if err != nil {
				log.Printf("Error enqueuing periodic tasks: %v", err)
			} else if count > 0 {
				log.Printf("Enqueued %d periodic tasks", count)
			}
		}
	}
}

func runSchedule(args []string) {
	fs := flag.NewFlagSet("schedule", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	name := fs.String("name", "", "Task name")
	cron := fs.String("cron", "", "Cron expression (e.g. '*/5 * * * *')")
	task := fs.String("task", "", "Task path")
	payload := fs.String("payload", "{}", "JSON payload")
	queueName := fs.String("queue", "default", "Queue name")
	fs.Parse(args)

	if *dsn == "" || *name == "" || *cron == "" || *task == "" {
		log.Fatal("DSN, --name, --cron, and --task are required")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	err = q.UpsertPeriodicTask(ctx, queue.PeriodicTask{
		Name:        *name,
		CronExpr:    *cron,
		TaskPath:    *task,
		PayloadJSON: json.RawMessage(*payload),
		QueueName:   *queueName,
		Enabled:     true,
		MaxAttempts: 3,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Successfully scheduled periodic task: %s\n", *name)
}

func runCancel(args []string) {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	taskID := fs.Int64("id", 0, "Task ID to cancel")
	fs.Parse(args)

	if *dsn == "" || *taskID == 0 {
		log.Fatal("DATABASE_URL and --id are required")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	if err := q.RequestCancellation(ctx, *taskID); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Successfully requested cancellation for task %d\n", *taskID)
}

func runLimit(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: reproq limit <set> [args]")
		os.Exit(1)
	}

	fs := flag.NewFlagSet("limit", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	key := fs.String("key", "", "Limit key (e.g. 'global' or 'queue:default')")
	rate := fs.Float64("rate", 10.0, "Tokens per second")
	burst := fs.Int("burst", 20, "Burst size")
	fs.Parse(args[1:])

	if *dsn == "" || *key == "" {
		log.Fatal("DATABASE_URL and --key are required")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	query := `
		INSERT INTO rate_limits (key, tokens_per_second, burst_size, current_tokens)
		VALUES ($1, $2, $3, $3)
		ON CONFLICT (key) DO UPDATE
		SET tokens_per_second = EXCLUDED.tokens_per_second,
		    burst_size = EXCLUDED.burst_size,
		    current_tokens = LEAST(rate_limits.current_tokens, EXCLUDED.burst_size)
	`
	_, err = pool.Exec(ctx, query, *key, *rate, *burst)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Successfully set limit for %s: %.2f tokens/sec (burst %d)\n", *key, *rate, *burst)
}

func runTriage(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: reproq triage <list|retry> [args]")
		os.Exit(1)
	}

	fs := flag.NewFlagSet("triage", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	limit := fs.Int("limit", 20, "Limit for list")
	taskID := fs.Int64("id", 0, "Task ID for retry")
	fs.Parse(args[1:])

	if *dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)

	switch args[0] {
	case "list":
		tasks, err := q.ListFailed(ctx, *limit)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%-10s %-20s %-30s %-20s\n", "ID", "Queue", "Last Error", "Failed At")
		for _, t := range tasks {
			failedAt := "N/A"
			if t.FailedAt != nil {
				failedAt = t.FailedAt.Format(time.RFC3339)
			}
			errMsg := "N/A"
			if t.LastError != nil {
				errMsg = *t.LastError
				if len(errMsg) > 27 {
					errMsg = errMsg[:27] + "..."
				}
			}
			fmt.Printf("%-10d %-20s %-30s %-20s\n", t.ID, t.QueueName, errMsg, failedAt)
		}
	case "retry":
		if *taskID == 0 {
			log.Fatal("Task --id is required for retry")
		}
		if err := q.RetryFailed(ctx, *taskID); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Successfully scheduled task %d for retry\n", *taskID)
	default:
		fmt.Printf("unknown triage command: %s\n", args[0])
	}
}
