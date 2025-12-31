package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	defaultInterval = 2 * time.Second
	queryTimeout    = 2 * time.Second
)

var (
	queueDepthGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reproq_queue_depth",
		Help: "Number of queued tasks (READY/WAITING).",
	})
	tasksRunningGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reproq_tasks_running",
		Help: "Number of running tasks.",
	})
	workerCountGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reproq_workers",
		Help: "Number of registered workers.",
	})
	concurrencyInUseGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reproq_concurrency_in_use",
		Help: "Number of concurrency slots currently in use.",
	})
	concurrencyLimitGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reproq_concurrency_limit",
		Help: "Total concurrency capacity across workers.",
	})
)

func StartCollector(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, logger *slog.Logger) {
	if interval <= 0 {
		interval = defaultInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := collectTaskMetrics(ctx, pool); err != nil {
				logWarn(logger, "Queue metrics collection failed", err)
			}
			if err := collectWorkerMetrics(ctx, pool); err != nil {
				logWarn(logger, "Worker metrics collection failed", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func collectTaskMetrics(ctx context.Context, pool *pgxpool.Pool) error {
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	rows, err := pool.Query(queryCtx, `
		SELECT status, COUNT(*)
		FROM task_runs
		WHERE status IN ('READY', 'WAITING', 'WAITING_CALLBACK', 'RUNNING')
		GROUP BY status
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ready int64
	var waiting int64
	var waitingCallback int64
	var running int64

	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return err
		}
		switch status {
		case "READY":
			ready = count
		case "WAITING":
			waiting = count
		case "WAITING_CALLBACK":
			waitingCallback = count
		case "RUNNING":
			running = count
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	queueDepthGauge.Set(float64(ready + waiting + waitingCallback))
	tasksRunningGauge.Set(float64(running))
	concurrencyInUseGauge.Set(float64(running))
	return nil
}

func collectWorkerMetrics(ctx context.Context, pool *pgxpool.Pool) error {
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var workers int64
	var concurrency int64
	if err := pool.QueryRow(queryCtx, `
		SELECT COUNT(*), COALESCE(SUM(concurrency), 0)
		FROM reproq_workers
	`).Scan(&workers, &concurrency); err != nil {
		return err
	}

	workerCountGauge.Set(float64(workers))
	concurrencyLimitGauge.Set(float64(concurrency))
	return nil
}

func logWarn(logger *slog.Logger, message string, err error) {
	if logger == nil || err == nil {
		return
	}
	logger.Warn(message, "error", err)
}
