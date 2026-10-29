package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"reproq-worker/internal/config"
	"reproq-worker/internal/db"
	"reproq-worker/internal/queue"
)

func runPruneHistory(arguments []string) {
	flags := flag.NewFlagSet("prune history", flag.ExitOnError)
	statuses := flags.String("statuses", "SUCCESSFUL,FAILED,CANCELLED", "Terminal statuses, comma-separated")
	age := flags.Duration("older-than", 7*24*time.Hour, "Minimum history age (Go duration, including integer us)")
	field := flags.String("field", "finished_at", "Age field: finished_at, started_at, enqueued_at, updated_at")
	limit := flags.Int("limit", 0, "Maximum deleted rows (0 means unbounded)")
	dryRun := flags.Bool("dry-run", false, "Count currently eligible history without changing rows")
	asJSON := flags.Bool("json", false, "Return exactly count and dry_run fields")
	if err := flags.Parse(arguments); err != nil {
		log.Fatal(err)
	}
	names := []queue.TaskStatus{}
	for _, value := range strings.Split(*statuses, ",") {
		names = append(names, queue.TaskStatus(strings.ToUpper(strings.TrimSpace(value))))
	}
	options := queue.PruneHistoryOptions{Statuses: names, OlderThan: age, Field: *field, Limit: *limit, DryRun: *dryRun}
	if err := queue.ValidatePruneHistoryOptions(options); err != nil {
		log.Fatal(err)
	}
	if flags.NArg() != 0 {
		log.Fatal("prune history accepts no positional arguments")
	}
	dsn := os.Getenv(config.QueueDatabaseEnv)
	if dsn == "" {
		log.Fatal("REPROQ_DATABASE_URL is required for history pruning")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	pool, err := db.NewPool(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	service, err := queue.NewService(ctx, pool)
	if err != nil {
		log.Fatal(err)
	}
	count, err := service.PruneHistory(ctx, options)
	if err != nil {
		log.Fatal(err)
	}
	if *asJSON {
		result := struct {
			Count  int64 `json:"count"`
			DryRun bool  `json:"dry_run"`
		}{count, *dryRun}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *dryRun {
		fmt.Printf("Currently eligible: %d terminal task(s)\n", count)
		return
	}
	fmt.Printf("Deleted %d terminal task(s)\n", count)
}
