package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

type Metrics struct {
	mu sync.Mutex

	Claimed   int64
	Succeeded int64
	Failed    int64
	Retried   int64

	// Latencies in milliseconds
	ClaimLatencies    []int64
	QueueWaitLatencies []int64
	ExecLatencies      []int64
}

func (m *Metrics) RecordClaim(latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Claimed++
	m.ClaimLatencies = append(m.ClaimLatencies, latency.Milliseconds())
}

func (m *Metrics) RecordSuccess(queueWait, execTime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Succeeded++
	m.QueueWaitLatencies = append(m.QueueWaitLatencies, queueWait.Milliseconds())
	m.ExecLatencies = append(m.ExecLatencies, execTime.Milliseconds())
}

func (m *Metrics) RecordFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Failed++
}

func (m *Metrics) RecordRetry() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Retried++
}

func (m *Metrics) Report() {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Println("\n--- Load Test Report ---")
	fmt.Printf("Claimed:   %d\n", m.Claimed)
	fmt.Printf("Succeeded: %d\n", m.Succeeded)
	fmt.Printf("Failed:    %d\n", m.Failed)
	fmt.Printf("Retried:   %d\n", m.Retried)

	reportJSON := map[string]interface{}{
		"claimed":   m.Claimed,
		"succeeded": m.Succeeded,
		"failed":    m.Failed,
		"retried":   m.Retried,
		"latencies": map[string]interface{}{
			"claim":      summarize(m.ClaimLatencies),
			"queue_wait": summarize(m.QueueWaitLatencies),
			"exec":       summarize(m.ExecLatencies),
		},
	}

	if os.Getenv("REPORT_JSON") != "" {
		f, _ := os.Create(os.Getenv("REPORT_JSON"))
		json.NewEncoder(f).Encode(reportJSON)
		f.Close()
	}
}

func summarize(latencies []int64) map[string]int64 {
	if len(latencies) == 0 {
		return nil
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	
	return map[string]int64{
		"p50": latencies[len(latencies)*50/100],
		"p95": latencies[len(latencies)*95/100],
		"p99": latencies[len(latencies)*99/100],
	}
}
