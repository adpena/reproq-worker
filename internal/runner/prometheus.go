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
)
