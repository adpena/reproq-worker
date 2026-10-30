package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPruneExpiredRejectsNegativeLimitBeforePoolAccess(t *testing.T) {
	service := &Service{}
	for _, dryRun := range []bool{false, true} {
		count, err := service.PruneExpired(context.Background(), -1, dryRun)
		if count != 0 || !errors.Is(err, ErrInvalidPruneLimit) {
			t.Fatalf("negative limit accepted before pool access: count=%d error=%v", count, err)
		}
	}
}

func TestHistoryInvalidOptionsRefuseBeforePoolAccess(t *testing.T) {
	negativeAge := -time.Second
	for _, options := range []PruneHistoryOptions{
		{Limit: -1}, {OlderThan: &negativeAge}, {Field: "created_at"},
		{Statuses: []TaskStatus{}}, {Statuses: []TaskStatus{StatusRunning}},
		{Statuses: []TaskStatus{StatusReady}}, {Statuses: []TaskStatus{StatusFailed, StatusWaiting}},
	} {
		for _, dry := range []bool{false, true} {
			options.DryRun = dry
			count, err := (&Service{}).PruneHistory(context.Background(), options)
			if err == nil || count != 0 {
				t.Fatal("invalid history options reached a pool")
			}
		}
	}
}

func TestValidatePruneLimitPreservesExplicitUnboundedAndPositiveBounds(t *testing.T) {
	for _, limit := range []int{0, 1, 1000} {
		if err := ValidatePruneLimit(limit); err != nil {
			t.Fatalf("valid prune limit %d rejected: %v", limit, err)
		}
	}
}
