package wakeup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reproq-worker/internal/db"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	dto "github.com/prometheus/client_model/go"
)

type fakeConn struct {
	messages chan *pgconn.Notification
	errors   chan error
	closed   chan struct{}
	once     sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{make(chan *pgconn.Notification, 2), make(chan error, 1), make(chan struct{}), sync.Once{}}
}
func (f *fakeConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	select {
	case n := <-f.messages:
		return n, nil
	case err := <-f.errors:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (f *fakeConn) Close(context.Context) error { f.once.Do(func() { close(f.closed) }); return nil }

func expectWake(t *testing.T, l *Listener) {
	t.Helper()
	select {
	case <-l.Wakeups():
	case <-time.After(2 * time.Second):
		t.Fatal("no coalesced wake")
	}
}

func TestSubscriptionNotificationAndClose(t *testing.T) {
	f := newFakeConn()
	l, err := open(context.Background(), func(ctx context.Context) (notificationConn, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("unbounded connection attempt")
		}
		return f, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), reconnectDelay)
	if err != nil {
		t.Fatal(err)
	}
	expectWake(t, l)
	f.messages <- &pgconn.Notification{Channel: "unrelated", Payload: "private"}
	f.messages <- &pgconn.Notification{Channel: Channel}
	expectWake(t, l)
	l.Close()
	l.Close() // repeat close joins the same reader, with no second connection owner.
	select {
	case <-f.closed:
	default:
		t.Fatal("listener connection leaked")
	}
}

func TestHintsCoalesceAndMetricsHaveBoundedLabels(t *testing.T) {
	l := &Listener{wake: make(chan struct{}, 1)}
	before := counterValue(activity.WithLabelValues("coalesced"))
	for range 100 {
		l.signal("received")
	}
	if len(l.wake) != 1 || counterValue(activity.WithLabelValues("coalesced"))-before != 99 {
		t.Fatal("burst was not bounded/coalesced")
	}
}

func TestReconnectRetriesWrappedTransportThenInspectsDurableWork(t *testing.T) {
	first, next := newFakeConn(), newFakeConn()
	calls := 0
	l, err := open(context.Background(), func(context.Context) (notificationConn, error) {
		calls++
		switch calls {
		case 1:
			return first, nil
		case 2:
			return nil, fmt.Errorf("%w: marker read: %w", db.ErrSchemaContract, io.EOF)
		default:
			return next, nil
		}
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(int) time.Duration { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	expectWake(t, l)
	first.errors <- io.EOF
	expectWake(t, l)
	select {
	case <-first.closed:
	default:
		t.Fatal("old connection not closed before reconnect")
	}
}

func TestReconnectRejectsSemanticSchemaAndRedactsDriverErrors(t *testing.T) {
	for _, cause := range []error{db.ErrSchemaContract, &pgconn.PgError{Code: "28P01", Message: "password=secret"}} {
		f := newFakeConn()
		calls := 0
		var output bytes.Buffer
		l, err := open(context.Background(), func(context.Context) (notificationConn, error) {
			calls++
			if calls == 1 {
				return f, nil
			}
			return nil, cause
		}, slog.New(slog.NewTextHandler(&output, nil)), func(int) time.Duration { return 0 })
		if err != nil {
			t.Fatal(err)
		}
		expectWake(t, l)
		f.errors <- io.EOF
		select {
		case err := <-l.Failures():
			if !errors.Is(err, errRejected) || strings.Contains(err.Error(), "secret") {
				t.Fatal("unredacted or missing terminal error")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("schema/auth rejection silently retried")
		}
		l.Close()
		if strings.Contains(output.String(), "secret") {
			t.Fatal("driver text leaked")
		}
	}
}

func TestShutdownInterruptsReconnectDelayAndStartupErrorsAreSanitized(t *testing.T) {
	_, err := open(context.Background(), func(context.Context) (notificationConn, error) { return nil, errors.New("dsn=password-secret") }, slog.Default(), reconnectDelay)
	if !errors.Is(err, errRejected) || strings.Contains(err.Error(), "password-secret") {
		t.Fatal("unsafe startup error")
	}
	f := newFakeConn()
	entered := make(chan struct{})
	l, err := open(context.Background(), func(context.Context) (notificationConn, error) { return f, nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), func(int) time.Duration { close(entered); return time.Hour })
	if err != nil {
		t.Fatal(err)
	}
	expectWake(t, l)
	f.errors <- io.EOF
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no reconnect wait")
	}
	done := make(chan struct{})
	go func() { l.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked by reconnect timer")
	}
}

func counterValue(counter interface{ Write(*dto.Metric) error }) float64 {
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		panic(err)
	}
	return metric.GetCounter().GetValue()
}
