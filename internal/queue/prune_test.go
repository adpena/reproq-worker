package queue

import (
	"context"
	"errors"
	"testing"
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

func TestValidatePruneLimitPreservesExplicitUnboundedAndPositiveBounds(t *testing.T) {
	for _, limit := range []int{0, 1, 1000} {
		if err := ValidatePruneLimit(limit); err != nil {
			t.Fatalf("valid prune limit %d rejected: %v", limit, err)
		}
	}
}
