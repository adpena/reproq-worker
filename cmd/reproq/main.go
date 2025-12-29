package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"reproq-worker/internal/config"
	"reproq-worker/internal/db"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/logging"
	"reproq-worker/internal/queue"
	"reproq-worker/internal/runner"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "worker":
		runWorker(os.Args[2:])
	case "replay":
		runReplay(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("usage: reproq <worker|replay> [args]")
}

func runWorker(args []string) {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	cfg.BindFlags(fs)
	fs.Parse(args)

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
