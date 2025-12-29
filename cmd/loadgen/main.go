package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN")
	numTasks := flag.Int("tasks", 1000, "Number of tasks to enqueue")
	queues := flag.String("queues", "default,high,low", "Comma-separated list of queues")
	priorityDist := flag.String("priority-dist", "-10,0,10", "Comma-separated list of priorities")
	runAfterPercent := flag.Int("run-after-percent", 10, "Percentage of tasks with run_after in the future")
	payloadSize := flag.Int("payload-size", 100, "Size of payload in bytes")
	seed := flag.Int64("seed", time.Now().UnixNano(), "Random seed")

	flag.Parse()

	if *dsn == "" {
		log.Fatal("DATABASE_URL is required via -dsn or env")
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
	start := time.Now()

	for i := 0; i < *numTasks; i++ {
		queue := queueList[r.Intn(len(queueList))]
		priority := priorityList[r.Intn(len(priorityList))]
		
		runAfter := time.Now()
		if r.Intn(100) < *runAfterPercent {
			runAfter = runAfter.Add(time.Duration(r.Intn(3600)) * time.Second)
		}

		payload := make([]byte, *payloadSize)
		r.Read(payload)
		specJSON := fmt.Sprintf(`{"data": "%x"}`, payload)
		
		hash := sha256.Sum256([]byte(specJSON))
		specHash := hex.EncodeToString(hash[:])

		query := `
			INSERT INTO task_runs (spec_hash, queue_name, payload_json, priority, run_after, max_attempts)
			VALUES ($1, $2, $3, $4, $5, $6)
		`
		_, err := pool.Exec(ctx, query, specHash, queue, specJSON, priority, runAfter, 3)
		if err != nil {
			log.Fatalf("Failed to insert task: %v", err)
		}

		if (i+1)%100 == 0 {
			fmt.Printf(".")
		}
	}

	fmt.Println()
	log.Printf("Done in %v", time.Since(start))
}
