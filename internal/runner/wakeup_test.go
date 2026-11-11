package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reproq-worker/internal/config"
	"reproq-worker/internal/executor"
	"reproq-worker/internal/queue"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

type testWakeSource struct {
	wake     chan struct{}
	failures chan error
}

func (s *testWakeSource) Wakeups() <-chan struct{} { return s.wake }
func (s *testWakeSource) Failures() <-chan error   { return s.failures }

func TestIdleWaitWakesButNotificationsCannotBypassErrorBackoff(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.PollMinBackoff = 0
	cfg.PollMaxBackoff = time.Hour
	s := &testWakeSource{make(chan struct{}, 1), make(chan error, 1)}
	r := New(cfg, &fakeQueue{}, &fakeExecutor{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	r.SetWakeSource(s)
	before := counterValue(idleWaits.WithLabelValues("notification"))
	s.wake <- struct{}{}
	r.sleep(context.Background(), true)
	if counterValue(idleWaits.WithLabelValues("notification"))-before != 1 {
		t.Fatal("missing wake metric")
	}
	s.wake <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.sleep(ctx, false)
	if len(s.wake) != 1 {
		t.Fatal("ordinary hint bypassed database error backoff")
	}
}

func TestTimerRecoversMissingHints(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.PollMinBackoff = 0
	cfg.PollMaxBackoff = 0
	r := New(cfg, &fakeQueue{}, &fakeExecutor{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	before := counterValue(idleWaits.WithLabelValues("timer"))
	r.sleep(context.Background(), true)
	if counterValue(idleWaits.WithLabelValues("timer"))-before != 1 {
		t.Fatal("missing recovery timer")
	}
}

func TestTerminalListenerFailureInterruptsFullCapacityAndWait(t *testing.T) {
	for _, full := range []bool{false, true} {
		cfg := config.DefaultConfig()
		cfg.MaxConcurrency = 1
		cfg.ReclaimIntervalSeconds = 0
		cfg.PollMinBackoff = time.Hour
		cfg.PollMaxBackoff = time.Hour
		r := New(cfg, &fakeQueue{}, &fakeExecutor{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		s := &testWakeSource{make(chan struct{}, 1), make(chan error, 1)}
		r.SetWakeSource(s)
		if full {
			r.pool <- struct{}{}
		}
		failure := errors.New("listener rejected")
		s.failures <- failure
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := r.Start(ctx)
		cancel()
		if !errors.Is(err, failure) {
			t.Fatalf("full=%t terminal failure not returned: %v", full, err)
		}
	}
}

func counterValue(counter interface{ Write(*dto.Metric) error }) float64 {
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		panic(err)
	}
	return metric.GetCounter().GetValue()
}

func TestNotificationHonorsConfiguredMinimumProbeDelay(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.PollMinBackoff = 20 * time.Millisecond
	cfg.PollMaxBackoff = time.Hour
	r := &Runner{cfg: cfg, wakeups: make(chan struct{}, 1)}
	s := make(chan struct{}, 1)
	s <- struct{}{}
	r.wakeups = s
	started := time.Now()
	r.sleep(context.Background(), true)
	if time.Since(started) < cfg.PollMinBackoff {
		t.Fatal("notification bypassed minimum probe delay")
	}
}

type interruptedQueue struct {
	fakeQueue
	stage   string
	entered chan struct{}
	once    sync.Once
}

func (q *interruptedQueue) block(ctx context.Context, stage string) error {
	if q.stage != stage {
		return nil
	}
	q.once.Do(func() { close(q.entered) })
	<-ctx.Done()
	return ctx.Err()
}
func (q *interruptedQueue) RegisterWorker(ctx context.Context, _ string, _ string, _ int, _ []string, _ string) error {
	return q.block(ctx, "register")
}
func (q *interruptedQueue) ReclaimQueues(ctx context.Context, _ []string) (int64, error) {
	return 0, q.block(ctx, "reclaim")
}
func (q *interruptedQueue) Claim(ctx context.Context, _ string, _ string, _ int, _ float64) (*queue.TaskRun, error) {
	if err := q.block(ctx, "claim"); err != nil {
		return nil, err
	}
	return nil, queue.ErrNoTasks
}
func (q *interruptedQueue) HasDueTasks(ctx context.Context, _ []string) (bool, error) {
	return false, q.block(ctx, "probe")
}

func TestListenerFailureCancelsInFlightQueueOperations(t *testing.T) {
	for _, stage := range []string{"register", "reclaim", "claim", "probe"} {
		t.Run(stage, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.PollMinBackoff = 0
			cfg.PollMaxBackoff = 0
			q := &interruptedQueue{stage: stage, entered: make(chan struct{})}
			r := New(cfg, q, &fakeExecutor{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			s := &testWakeSource{make(chan struct{}, 1), make(chan error, 1)}
			r.SetWakeSource(s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- r.Start(ctx) }()
			select {
			case <-q.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("queue operation not reached")
			}
			failure := errors.New("listener rejected")
			s.failures <- failure
			select {
			case err := <-done:
				if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("terminal failure did not cancel blocked SQL")
			}
		})
	}
}

type oneClaimQueue struct {
	fakeQueue
	task *queue.TaskRun
}

func (q *oneClaimQueue) Claim(context.Context, string, string, int, float64) (*queue.TaskRun, error) {
	if q.task == nil {
		return nil, queue.ErrNoTasks
	}
	task := q.task
	q.task = nil
	return task, nil
}

type cancelOnListenerExecutor struct {
	fakeExecutor
	entered chan struct{}
	stopped chan struct{}
}

func (e *cancelOnListenerExecutor) Execute(ctx context.Context, _ executor.Claim, _ json.RawMessage, _ time.Duration) (*executor.ResultEnvelope, string, string, error) {
	close(e.entered)
	<-ctx.Done()
	close(e.stopped)
	return nil, "", "", ctx.Err()
}

func TestListenerFailureCancelsAndJoinsActiveTaskAtFullCapacity(t *testing.T) {
	task := ownedTask(t)
	sealRunnerFixture(t, task)
	cfg := config.DefaultConfig()
	cfg.MaxConcurrency = 1
	cfg.ReclaimIntervalSeconds = 0
	cfg.AllowedTaskModules = []string{"allowed."}
	cfg.QueueNames = []string{task.QueueName}
	exec := &cancelOnListenerExecutor{entered: make(chan struct{}), stopped: make(chan struct{})}
	r := New(cfg, &oneClaimQueue{task: task}, exec, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	s := &testWakeSource{make(chan struct{}, 1), make(chan error, 1)}
	r.SetWakeSource(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	select {
	case <-exec.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("executor not started")
	}
	failure := errors.New("listener rejected")
	s.failures <- failure
	select {
	case err := <-done:
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active task not joined")
	}
	select {
	case <-exec.stopped:
	default:
		t.Fatal("returned before executor cancellation")
	}
	if len(r.pool) != 0 {
		t.Fatal("execution slot leaked")
	}
}
