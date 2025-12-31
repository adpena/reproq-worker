package runner

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	tasksClaimed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reproq_tasks_claimed_total",
		Help: "Total number of tasks claimed by this worker",
	}, []string{"queue"})

	tasksCompleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reproq_tasks_completed_total",
		Help: "Total number of tasks completed",
	}, []string{"queue", "status"})

	claimDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "reproq_claim_duration_seconds",
		Help:    "Time taken to claim a task from the DB",
		Buckets: prometheus.DefBuckets,
	}, []string{"queue"})

	execDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "reproq_exec_duration_seconds",
		Help:    "Time taken to execute the task process",
		Buckets: prometheus.DefBuckets,
	}, []string{"queue"})

	queueWaitTime = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "reproq_queue_wait_duration_seconds",
		Help:    "Time task spent in queue before execution started",
		Buckets: prometheus.ExponentialBuckets(1, 2, 10),
	}, []string{"queue"})

	dbOpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "reproq_db_operation_duration_seconds",
		Help:    "Time spent on database operations",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation"})

	// Worker Resource Telemetry
	WorkerCPUUsage = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reproq_worker_cpu_usage_percent",
		Help: "Current CPU usage percentage of the worker process",
	})

	WorkerMemUsage = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reproq_worker_mem_usage_bytes",
		Help: "Current memory usage (RSS) of the worker process in bytes",
	})

	// DB Pool Telemetry
	DBPoolConnectionsInUse = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reproq_db_pool_connections_in_use",
		Help: "Number of active database connections in the pool",
	})

	DBPoolWaitCount = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reproq_db_pool_wait_count_total",
		Help: "Total number of times a connection was requested but not immediately available",
	})
)
