package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"reproq-worker/internal/config"
	"reproq-worker/internal/db"
	"reproq-worker/internal/queue"
)

func runReclaim(arguments []string) {
	flags := flag.NewFlagSet("reclaim", flag.ExitOnError)
	dsn := config.PrivateStringFlag(flags, "dsn", os.Getenv(config.QueueDatabaseEnv), "PostgreSQL DSN (defaults to REPROQ_DATABASE_URL)")
	action := flags.String("action", "requeue", "Expired lease action: requeue|fail; stored attempt budget always applies")
	age := flags.Duration("older-than", 0, "Only leases expired longer than this duration")
	limit := flags.Int("limit", 0, "Maximum eligible rows (0 means unbounded)")
	includeNull := flags.Bool("include-null-lease", false, "Include legacy RUNNING rows with a NULL lease timestamp")
	dryRun := flags.Bool("dry-run", false, "Count eligible rows without changing them")
	asJSON := flags.Bool("json", false, "Return a row-free JSON summary")
	if err := flags.Parse(arguments); err != nil {
		log.Fatal(err)
	}
	if *dsn == "" || (*action != "requeue" && *action != "fail") || *age < 0 || *limit < 0 || flags.NArg() != 0 {
		log.Fatal("reclaim requires a DSN, action requeue|fail, nonnegative age/limit and no positional arguments")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	service, err := queue.NewService(ctx, pool)
	if err != nil {
		log.Fatal(err)
	}
	result, err := service.ReclaimWithOptions(ctx, queue.ReclaimOptions{Action: *action, OlderThan: *age, Limit: *limit, IncludeNullLease: *includeNull, DryRun: *dryRun})
	if err != nil {
		log.Fatal(err)
	}
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *dryRun {
		fmt.Printf("Would reclaim %d expired task(s)\n", result.Count)
		return
	}
	fmt.Printf("Reclaimed %d task(s): %d requeued, %d failed, %d cancelled\n", result.Count, result.Requeued, result.Failed, result.Cancelled)
}
