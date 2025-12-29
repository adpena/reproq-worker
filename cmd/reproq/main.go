package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"reproq-worker/internal/config"
	"reproq-worker/internal/db"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/logging"
	"reproq-worker/internal/queue"
	"reproq-worker/internal/runner"
)

const Version = "0.0.109"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	if os.Args[1] == "--version" || os.Args[1] == "version" {
		fmt.Printf("reproq-worker version %s\n", Version)
		return
	}

	switch os.Args[1] {
	case "worker":
		runWorker(os.Args[2:])
	case "beat":
		runBeat(os.Args[2:])
	case "replay":
		runReplay(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("usage: reproq <worker|beat|replay|version> [args]")
}

func runWorker(args []string) {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	metricsPort := fs.Int("metrics-port", 0, "Port to serve Prometheus metrics (0 to disable)")
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	cfg.BindFlags(fs)
	fs.Parse(args)

	if *metricsPort > 0 {
		go func() {
			fmt.Printf("📊 Serving Prometheus metrics on :%d/metrics\n", *metricsPort)
			http.Handle("/metrics", promhttp.Handler())
			if err := http.ListenAndServe(fmt.Sprintf(":%d", *metricsPort), nil); err != nil {
				log.Printf("Metrics server error: %v", err)
			}
		}()
	}

	logger := logging.Init(cfg.WorkerID)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	exec := &executor.ShellExecutor{
		PythonBin:       cfg.PythonBin,
		ExecutorModule:  cfg.ExecutorModule,
		PayloadMode:     cfg.PayloadMode,
		MaxPayloadBytes: cfg.MaxPayloadBytes,
		MaxStdoutBytes:  cfg.MaxStdoutBytes,
		MaxStderrBytes:  cfg.MaxStderrBytes,
	}

	r := runner.New(cfg, q, exec, logger)
	if err := r.Start(ctx); err != nil {
		log.Fatal(err)
	}
}

func runBeat(args []string) {
	fs := flag.NewFlagSet("beat", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	interval := fs.Duration("interval", 30*time.Second, "Polling interval for periodic tasks")
	fs.Parse(args)

	if *dsn == "" {
		log.Fatal("DSN required (use --dsn or DATABASE_URL)")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	fmt.Printf("Starting reproq beat (interval: %v)...\n", *interval)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	// Run once immediately
	if n, err := q.EnqueueDuePeriodicTasks(ctx); err != nil {
		fmt.Printf("Error: %v\n", err)
	} else if n > 0 {
		fmt.Printf("Enqueued %d periodic tasks\n", n)
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Shutting down beat...")
			return
		case <-ticker.C:
			if n, err := q.EnqueueDuePeriodicTasks(ctx); err != nil {
				fmt.Printf("Error: %v\n", err)
			} else if n > 0 {
				fmt.Printf("Enqueued %d periodic tasks\n", n)
			}
		}
	}
}

func runReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	id := fs.Int64("id", 0, "Result ID to replay")
	fs.Parse(args)

	if *dsn == "" || *id == 0 {
		log.Fatal("DSN and --id required")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	q := queue.NewService(pool)
	newID, err := q.Replay(ctx, *id)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Requeued task %d as new result_id %d\n", *id, newID)
}
