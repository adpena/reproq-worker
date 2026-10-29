package queue

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var claimBlocked = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "reproq_claim_blocked_total",
	Help: "Claim candidates skipped because a shared key or rate budget is unavailable",
}, []string{"reason"})

var reclaimedTasks = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "reproq_reclaimed_tasks_total",
	Help: "Expired task lease transitions acknowledged by PostgreSQL",
}, []string{"status"})

var periodicOccurrences = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "reproq_go_periodic_occurrences_total",
	Help: "Committed Go beat occurrence results; native pg_cron is observed through stored task provenance",
}, []string{"outcome"})

var cancellationTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "reproq_cancellation_transitions_total",
	Help: "Operator cancellation requests or pending cancellations acknowledged by PostgreSQL",
}, []string{"outcome"})

var prunedTasks = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "reproq_pruned_tasks_total",
	Help: "Task history deletions acknowledged by PostgreSQL; dry runs are excluded",
}, []string{"policy"})
