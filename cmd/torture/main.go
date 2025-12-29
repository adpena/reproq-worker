package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"reproq-worker/internal/db"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	count := flag.Int("count", 1000, "Number of tasks to enqueue")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("DSN required")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	fmt.Printf("🔨 Starting torture test: Enqueuing %d tasks...\n", *count)

	var wg sync.WaitGroup
	batchSize := 100
	
	for i := 0; i < *count; i += batchSize {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			for j := start; j < start+batchSize && j < *count; j++ {
				spec := fmt.Sprintf(`{"task_path": "test.task", "args": [%d]}`, j)
				hash := fmt.Sprintf("%064d", j)
				_, err := pool.Exec(ctx, "
					INSERT INTO task_runs (spec_hash, queue_name, spec_json, status)
					VALUES ($1, 'default', $2, 'READY')
				")
				if err != nil {
					fmt.Printf("Insert error: %v\n", err)
				}
			}
		}(i)
	}

	wg.Wait()
	fmt.Println("✅ Enqueued successfully. Monitor the Django Admin to watch progress.")
}
