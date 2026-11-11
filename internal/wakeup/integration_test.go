package wakeup

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"reproq-worker/internal/testdatabase"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresCommitRollbackBroadcastAndReconnect(t *testing.T) {
	dsn := testdatabase.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	emitter, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer emitter.Close(context.Background())
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(context.Background())
	const barrier = Channel + "_test_barrier"
	if _, err = observer.Exec(ctx, "LISTEN "+Channel+"; LISTEN "+barrier); err != nil {
		t.Fatal(err)
	}
	checkBarrier := func() {
		t.Helper()
		if _, err := observer.Exec(ctx, "SELECT pg_notify($1,'')", barrier); err != nil {
			t.Fatal(err)
		}
		notification, err := observer.WaitForNotification(ctx)
		if err != nil || notification.Channel != barrier {
			t.Fatalf("uncommitted/rolled-back notification preceded barrier: %v, %v", notification, err)
		}
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("reproq-wakeup-test-%d", time.Now().UnixNano())
	query := parsed.Query()
	query.Set("application_name", name)
	query.Set("pool_min_conns", "0")
	query.Set("pool_max_conns", "1")
	parsed.RawQuery = query.Encode()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := Open(ctx, parsed.String(), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dsn, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	expectWake(t, first)
	expectWake(t, second)
	tx, err := emitter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT pg_notify($1,'')", Channel); err != nil {
		t.Fatal(err)
	}
	// A committed marker on a separately subscribed session orders the check;
	// no sleep or immediate empty-channel assertion stands in for delivery.
	checkBarrier()
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	checkBarrier()
	tx, err = emitter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT pg_notify($1,'')", Channel); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	expectWake(t, first)
	expectWake(t, second)
	if notification, err := observer.WaitForNotification(ctx); err != nil || notification.Channel != Channel {
		t.Fatalf("committed hint missing from ordered observer: %v, %v", notification, err)
	}
	var pid int
	if err = emitter.QueryRow(ctx, "SELECT pid FROM pg_stat_activity WHERE application_name=$1", name).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = emitter.Exec(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	expectWake(t, first) // A successful resubscription forces a durable inspection even without another insert.
	if _, err = emitter.Exec(ctx, "SELECT pg_notify($1,'')", Channel); err != nil {
		t.Fatal(err)
	}
	expectWake(t, first)
}
