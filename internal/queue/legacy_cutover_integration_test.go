package queue

import (
	"errors"
	"testing"
)

func TestLegacyCutoverRefusesClaimsReplayAndRetryWithoutMutatingHistory(t *testing.T) {
	ctx, pool, service, name := ownedFencedQueue(t)
	legacy := insertFencedFixture(t, pool, name, 0, nil, nil, 0)
	current := insertFencedFixture(t, pool, name, 1, nil, nil, 0)
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET spec_version=0,priority=100 WHERE result_id=$1", legacy); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(tr) ORDER BY result_id)::text FROM public.task_runs tr WHERE queue_name=$1", name).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := state()
	for _, aging := range []float64{0, 60} {
		if task, err := service.Claim(ctx, "cutover-worker", name, 60, aging); !errors.Is(err, ErrLegacySpec) || task != nil {
			t.Fatalf("legacy task was not refused before claim: %+v %v", task, err)
		}
		if state() != before {
			t.Fatal("legacy refusal spent an attempt or changed queue history")
		}
	}
	var hash string
	if err := pool.QueryRow(ctx, "SELECT spec_hash FROM public.task_runs WHERE result_id=$1", legacy).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	var countBefore, countAfter int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.task_runs").Scan(&countBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Replay(ctx, legacy); !errors.Is(err, ErrLegacySpec) {
		t.Fatalf("legacy replay accepted: %v", err)
	}
	if _, _, err := service.ReplayBySpecHash(ctx, hash); !errors.Is(err, ErrLegacySpec) {
		t.Fatalf("legacy hash replay accepted: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.task_runs").Scan(&countAfter); err != nil || countBefore != countAfter || before != state() {
		t.Fatal("legacy replay changed historical rows or inserted new work")
	}
	if _, err := pool.Exec(ctx, "UPDATE public.task_runs SET status='FAILED',attempts=3,last_error='retained historical failure' WHERE result_id=ANY($1::bigint[])", []int64{legacy, current}); err != nil {
		t.Fatal(err)
	}
	before = state()
	if _, err := service.InspectFailedTask(ctx, legacy); err != nil {
		t.Fatalf("terminal legacy history is no longer readable: %v", err)
	}
	if changed, err := service.RetryFailedTask(ctx, legacy); !errors.Is(err, ErrLegacySpec) || changed != 0 {
		t.Fatalf("legacy retry accepted: %d %v", changed, err)
	}
	if changed, err := service.RetryAllFailedTasks(ctx); !errors.Is(err, ErrLegacySpec) || changed != 0 {
		t.Fatalf("mixed legacy/current bulk retry was not atomic: %d %v", changed, err)
	}
	if state() != before {
		t.Fatal("refused retry consumed failure evidence or reset attempts")
	}
}
