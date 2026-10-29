package queue

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func acknowledgedCounter(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

func historyAge() *time.Duration { age := 24 * time.Hour; return &age }
func ageHistory(t *testing.T, f *terminalFixture, ids ...int64) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, `UPDATE public.task_runs SET finished_at=clock_timestamp()-interval '4 days',
 started_at=clock_timestamp()-interval '4 days',enqueued_at=clock_timestamp()-interval '4 days',updated_at=clock_timestamp()-interval '4 days'
 WHERE result_id=ANY($1::bigint[])`, ids)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHistoryAgeFieldStatusAndCountLimits(t *testing.T) {
	for _, field := range []string{"finished_at", "started_at", "enqueued_at", "updated_at"} {
		t.Run(field, func(t *testing.T) {
			f := newTerminalFixture(t)
			first := f.node(t, "FAILED", nil, nil, false)
			second := f.node(t, "FAILED", nil, nil, false)
			success := f.node(t, "SUCCESSFUL", nil, nil, false)
			ageHistory(t, f, first, second, success)
			options := PruneHistoryOptions{Statuses: []TaskStatus{StatusFailed}, OlderThan: historyAge(), Field: field, Limit: 1, DryRun: true}
			before := acknowledgedCounter(t, prunedTasks.WithLabelValues("history"))
			count, err := f.service.PruneHistory(f.ctx, options)
			if err != nil || count != 1 {
				t.Fatalf("bounded dryrun: %d %v", count, err)
			}
			if f.status(t, first) != "FAILED" || f.status(t, second) != "FAILED" {
				t.Fatal("dryrun wrote rows")
			}
			if acknowledgedCounter(t, prunedTasks.WithLabelValues("history")) != before {
				t.Fatal("dry run incremented acknowledged deletions")
			}
			large := options
			large.Limit = 1 << 33
			if count, err := f.service.PruneHistory(f.ctx, large); err != nil || count != 2 {
				t.Fatal("history limit was narrowed to int32", err)
			}
			options.DryRun = false
			count, err = f.service.PruneHistory(f.ctx, options)
			if err != nil || count != 1 {
				t.Fatalf("bounded delete: %d %v", count, err)
			}
			if acknowledgedCounter(t, prunedTasks.WithLabelValues("history")) != before+1 {
				t.Fatal("acknowledged deletion metric disagrees with committed rows")
			}
			var remaining int
			if err := f.pool.QueryRow(f.ctx, "SELECT count(*) FROM public.task_runs WHERE result_id=ANY($1::bigint[])", []int64{first, second}).Scan(&remaining); err != nil || remaining != 1 {
				t.Fatal("history limit exceeded", err)
			}
			if f.status(t, success) != "SUCCESSFUL" {
				t.Fatal("unselected status deleted")
			}
		})
	}
}

func TestHistorySkipsLockedParentScopeBeforeConcurrentChildRetry(t *testing.T) {
	f := newTerminalFixture(t)
	parent := f.node(t, "FAILED", nil, nil, false)
	child := f.node(t, "FAILED", &parent, nil, false)
	other := f.node(t, "FAILED", nil, nil, false)
	ageHistory(t, f, parent, child, other)
	if _, err := f.pool.Exec(f.ctx, "UPDATE public.task_runs SET finished_at=finished_at-interval '1 day' WHERE result_id=$1", parent); err != nil {
		t.Fatal(err)
	}
	held, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.Background()) }()
	var blocker uint32
	if err := held.QueryRow(f.ctx, "SELECT pg_backend_pid() FROM public.task_runs WHERE result_id=$1 FOR UPDATE", child).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	retryDone := make(chan error, 1)
	go func() {
		count, err := f.service.RetryFailedTask(f.ctx, child)
		if err == nil && count != 1 {
			err = errors.New("owned child was not retried")
		}
		retryDone <- err
	}()
	waitForSQLCondition(t, f.ctx, f.pool, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND $1=ANY(pg_catalog.pg_blocking_pids(pid)))", blocker)
	start := time.Now()
	count, err := f.service.PruneHistory(f.ctx, PruneHistoryOptions{Statuses: []TaskStatus{StatusFailed}, OlderThan: historyAge(), Limit: 1})
	if err != nil || count != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("locked tree prevented unrelated progress: %d %v", count, err)
	}
	if f.status(t, parent) != "FAILED" {
		t.Fatal("locked parent history deleted")
	}
	if err := held.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-retryDone; err != nil {
		t.Fatal(err)
	}
	var preserved bool
	if err := f.pool.QueryRow(f.ctx, "SELECT status='READY' AND parent_id=$1 FROM public.task_runs WHERE result_id=$2", parent, child).Scan(&preserved); err != nil || !preserved {
		t.Fatal("retry lost its parent relationship", err)
	}
	var exists bool
	if err := f.pool.QueryRow(f.ctx, "SELECT EXISTS(SELECT 1 FROM public.task_runs WHERE result_id=$1)", other).Scan(&exists); err != nil || exists {
		t.Fatal("unrelated row not deleted", err)
	}
}

func TestHistoryRetainsCallbackWhileHeaderRunsAndNullAgeFields(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	header := f.node(t, "READY", nil, &workflow, false)
	f.claim(t, header)
	callback := f.node(t, "WAITING", nil, &workflow, false)
	f.summary(t, workflow, 1, callback)
	if _, err := f.service.RequestCancel(f.ctx, callback); err != nil {
		t.Fatal(err)
	}
	ageHistory(t, f, callback)
	empty := f.node(t, "FAILED", nil, nil, false)
	for _, dry := range []bool{true, false} {
		count, err := f.service.PruneHistory(f.ctx, PruneHistoryOptions{OlderThan: historyAge(), DryRun: dry})
		if err != nil || count != 0 {
			t.Fatalf("active callback/null timestamp selected: %d %v", count, err)
		}
	}
	if f.status(t, callback) != "CANCELLED" || f.status(t, empty) != "FAILED" || f.status(t, header) != "RUNNING" {
		t.Fatal("retention corrupted active workflow")
	}
}

type historyQueryTrace struct{ calls atomic.Int64 }

func (trace *historyQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	trace.calls.Add(1)
	return ctx
}
func (trace *historyQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestHistoryDeletesDeepTerminalChainInBatchesWithoutLeafRescans(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	var parent *int64
	for i := 0; i < transitionBatchSize+2; i++ {
		id := f.node(t, "FAILED", parent, &workflow, false)
		parent = &id
	}
	ageHistory(t, f, f.ids...)
	trace := &historyQueryTrace{}
	configuration := f.pool.Config()
	configuration.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(f.ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service, err := NewService(f.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	before := trace.calls.Load()
	count, err := service.PruneHistory(f.ctx, PruneHistoryOptions{OlderThan: historyAge()})
	calls := trace.calls.Load() - before
	if err != nil || count != int64(len(f.ids)) {
		t.Fatalf("deep history stranded: %d %v", count, err)
	}
	if calls > 30 {
		t.Fatalf("history used %d client queries for1002rows instead of bounded batches", calls)
	}
}

func TestPendingCycleCancellationUsesOneFiniteOrderedScope(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	first := f.node(t, "WAITING", nil, &workflow, false)
	second := f.node(t, "WAITING", &first, &workflow, false)
	if _, err := f.pool.Exec(f.ctx, "UPDATE public.task_runs SET parent_id=$1 WHERE result_id=$2", second, first); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for _, id := range []int64{first, second} {
		go func(id int64) { _, err := f.service.RequestCancel(f.ctx, id); done <- err }(id)
	}
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cycle scope did not terminate")
		}
	}
	if strings.Contains(f.status(t, first), "WAIT") || strings.Contains(f.status(t, second), "WAIT") {
		t.Fatal("cycle left pending rows")
	}
	var errors int
	if err := f.pool.QueryRow(f.ctx, "SELECT sum(jsonb_array_length(errors_json)) FROM public.task_runs WHERE result_id=ANY($1::bigint[])", f.ids).Scan(&errors); err != nil || errors != 2 {
		t.Fatal("cycle was terminalized more than once", err)
	}
}

func TestSuccessfulParentDoesNotLockDistantWaitingDescendants(t *testing.T) {
	f := newTerminalFixture(t)
	workflow := f.workflow(t)
	first := f.node(t, "READY", nil, &workflow, false)
	previous := first
	for i := 0; i < 32; i++ {
		previous = f.node(t, "WAITING", &previous, &workflow, false)
	}
	claim := f.claim(t, first)
	held, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(context.Background()) }()
	if _, err := held.Exec(f.ctx, "SELECT result_id FROM public.task_runs WHERE result_id=$1 FOR UPDATE", previous); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- f.service.CompleteSuccess(f.ctx, first, "owned-terminal-worker", claim.LeaseToken, []byte(`true`), nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = held.Rollback(context.Background())
		<-done
		t.Fatal("success recursively locked distant chain")
	}
	if f.status(t, first) != "SUCCESSFUL" || f.status(t, previous) != "WAITING" {
		t.Fatal("normal success damaged distant history")
	}
}

func TestUnknownStatusesAndTheirHistoryAreRetained(t *testing.T) {
	for _, relation := range []string{"expired-self", "child", "member"} {
		t.Run(relation, func(t *testing.T) {
			f := newTerminalFixture(t)
			var unknown, parent int64
			switch relation {
			case "expired-self":
				unknown = f.node(t, "FUTURE_PENDING", nil, nil, true)
				parent = unknown
			case "child":
				parent = f.node(t, "FAILED", nil, nil, true)
				unknown = f.node(t, "FUTURE_PENDING", &parent, nil, false)
			case "member":
				workflow := f.workflow(t)
				parent = f.node(t, "FAILED", nil, &workflow, true)
				unknown = f.node(t, "FUTURE_PENDING", nil, &workflow, false)
			}
			ageHistory(t, f, parent)
			for _, dry := range []bool{true, false} {
				count, err := f.service.PruneExpired(f.ctx, 1, dry)
				if err != nil || count != 0 {
					t.Fatalf("unknown dependency/row expired: %d %v", count, err)
				}
				count, err = f.service.PruneHistory(f.ctx, PruneHistoryOptions{OlderThan: historyAge(), DryRun: dry})
				if err != nil || count != 0 {
					t.Fatalf("unknown dependency history deleted: %d %v", count, err)
				}
			}
			if f.status(t, unknown) != "FUTURE_PENDING" {
				t.Fatal("unknown state was interpreted")
			}
			if parent != unknown && f.status(t, parent) != "FAILED" {
				t.Fatal("needed parent history changed")
			}
		})
	}
}
