package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	flag.Parse()

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

	// 1. Stuck RUNNING tasks
	var stuckTasks int
	pool.QueryRow(ctx, "SELECT count(*) FROM task_runs WHERE status = 'RUNNING' AND leased_until < NOW()").Scan(&stuckTasks)
	if stuckTasks > 0 {
		fmt.Printf("[FAIL] Found %d stuck tasks with expired leases\n", stuckTasks)
	} else {
		fmt.Printf("[PASS] No stuck tasks with expired leases\n")
	}

	// 2. Max attempts exceeded
	var exceededAttempts int
	pool.QueryRow(ctx, "SELECT count(*) FROM task_runs WHERE attempt_count > max_attempts AND status != 'SUCCESSFUL'").Scan(&exceededAttempts)
	if exceededAttempts > 0 {
		fmt.Printf("[FAIL] Found %d tasks that exceeded max_attempts\n", exceededAttempts)
	} else {
		fmt.Printf("[PASS] No tasks exceeded max_attempts\n")
	}

	// 3. Premature execution
	var prematureTasks int
	pool.QueryRow(ctx, "SELECT count(*) FROM task_runs WHERE started_at < run_after").Scan(&prematureTasks)
	if prematureTasks > 0 {
		fmt.Printf("[FAIL] Found %d tasks executed before run_after\n", prematureTasks)
	} else {
		fmt.Printf("[PASS] No tasks executed prematurely\n")
	}

	// 4. Multiple executions (same result_id is not applicable here as we don't have result_id yet, but we can check if SUCCESSFUL tasks have attempt_count > 1 without failure logs?)
	// Actually, the request mentions result_id but our schema doesn't have it. We use spec_hash and id.
	// We can check if multiple tasks with same spec_hash are SUCCESSFUL if that was intended.
}
