// Package wakeup implements optional, lossy arrival hints. Only SQL authorizes work.
package wakeup

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"reproq-worker/internal/db"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Channel carries no payload, task identity, queue name or authorization.
const Channel = "reproq_wake_v1"

var (
	connections = promauto.NewGauge(prometheus.GaugeOpts{Name: "reproq_listener_connections", Help: "Connected notification sessions outside the query pool"})
	activity    = promauto.NewCounterVec(prometheus.CounterOpts{Name: "reproq_notification_events_total", Help: "Notification listener activity, with fixed bounded event labels"}, []string{"event"})
	errRejected = errors.New("notification listener rejected schema, authorization or session configuration; verify the queue endpoint and migrations, or select poll mode")
)

type notificationConn interface {
	WaitForNotification(context.Context) (*pgconn.Notification, error)
	Close(context.Context) error
}

type connector func(context.Context) (notificationConn, error)

// Listener's reader goroutine exclusively owns the session. Close cancels and joins it.
type Listener struct {
	wake   chan struct{}
	errors chan error
	done   chan struct{}
	cancel context.CancelFunc
}

func Open(ctx context.Context, dsn string, logger *slog.Logger) (*Listener, error) {
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errRejected
	}
	return open(ctx, func(ctx context.Context) (notificationConn, error) {
		// Parse using the pool's grammar, but open only a dedicated session. Pool
		// options must not become PostgreSQL server runtime parameters.
		conn, err := pgx.ConnectConfig(ctx, parsed.ConnConfig.Copy())
		if err != nil {
			return nil, err
		}
		if err = db.RequireSchemaContract(ctx, conn); err == nil {
			_, err = conn.Exec(ctx, "LISTEN "+Channel)
		}
		if err != nil {
			closeConnection(conn)
			return nil, err
		}
		return conn, nil
	}, logger, reconnectDelay)
}

func open(parent context.Context, connect connector, logger *slog.Logger, delay func(int) time.Duration) (*Listener, error) {
	ctx, cancel := context.WithCancel(parent)
	conn, err := boundedConnect(ctx, connect)
	if err != nil {
		cancel()
		// Driver errors can include credentials or DSNs. Never expose their text.
		return nil, errRejected
	}
	l := &Listener{wake: make(chan struct{}, 1), errors: make(chan error, 1), done: make(chan struct{}), cancel: cancel}
	connections.Inc()
	l.signal("subscribed") // LISTEN committed before the first durable inspection.
	go l.read(ctx, conn, connect, logger, delay)
	return l, nil
}

func (l *Listener) Wakeups() <-chan struct{} { return l.wake }
func (l *Listener) Failures() <-chan error   { return l.errors }
func (l *Listener) Close()                   { l.cancel(); <-l.done }

func boundedConnect(ctx context.Context, connect connector) (notificationConn, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return connect(bounded)
}

func closeConnection(conn notificationConn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

func (l *Listener) signal(event string) {
	activity.WithLabelValues(event).Inc()
	select {
	case l.wake <- struct{}{}:
	default:
		activity.WithLabelValues("coalesced").Inc()
	}
}

func (l *Listener) read(ctx context.Context, conn notificationConn, connect connector, logger *slog.Logger, delay func(int) time.Duration) {
	defer close(l.done)
	defer func() {
		if conn != nil {
			closeConnection(conn)
			connections.Dec()
		}
	}()
	attempt := 0
	for {
		if conn != nil {
			notification, err := conn.WaitForNotification(ctx)
			if err == nil {
				// Ignore unexpected channels and non-protocol payloads without logging them.
				if notification != nil && notification.Channel == Channel && notification.Payload == "" {
					l.signal("received")
				}
				continue
			}
			closeConnection(conn)
			connections.Dec()
			conn = nil
			if ctx.Err() != nil {
				return
			}
			if !retryable(err) {
				l.fail()
				return
			}
			activity.WithLabelValues("disconnected").Inc()
			logger.Warn("Notification connection lost; bounded polling remains active")
		}
		timer := time.NewTimer(delay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		var err error
		conn, err = boundedConnect(ctx, connect)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !retryable(err) {
				l.fail()
				return
			}
			activity.WithLabelValues("reconnect_failed").Inc()
			if attempt < 5 {
				attempt++
			}
			continue
		}
		connections.Inc()
		attempt = 0
		logger.Info("Notification session subscribed; inspecting durable work")
		l.signal("reconnected")
	}
}

func (l *Listener) fail() {
	activity.WithLabelValues("rejected").Inc()
	l.errors <- errRejected
}

func reconnectDelay(attempt int) time.Duration {
	ceiling := 250 * time.Millisecond << min(attempt, 4)
	return ceiling/2 + time.Duration(rand.Int63n(int64(ceiling/2)))
}

func retryable(err error) bool {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		return len(postgres.Code) >= 2 && postgres.Code[:2] == "08" || postgres.Code == "57P01" || postgres.Code == "57P02" || postgres.Code == "57P03" || postgres.Code == "53300"
	}
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return false
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded)
}
