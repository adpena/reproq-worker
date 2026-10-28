package queue

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Destructive queue tests never use the ambient application DATABASE_URL.
func testDatabaseURL(tb testing.TB) string {
	tb.Helper()
	required := os.Getenv("REPROQ_REQUIRE_TEST_DATABASE") == "1"
	if testing.Short() {
		if required {
			tb.Fatal("explicit integration checks cannot run with -short")
		}
		tb.Skip("database integration check excluded by -short")
	}
	dsn := os.Getenv("REPROQ_TEST_DATABASE_URL")
	if dsn == "" {
		if required {
			tb.Fatal("REPROQ_TEST_DATABASE_URL is required for integration checks")
		}
		tb.Skip("REPROQ_TEST_DATABASE_URL not set")
	}
	if err := validateTestDatabaseURL(dsn); err != nil {
		tb.Fatal(err)
	}
	return dsn
}

func validateTestDatabaseURL(dsn string) error {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid test database configuration")
	}
	isLoopback := func(host string) bool {
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	}
	if !isLoopback(config.ConnConfig.Host) {
		return errors.New("test database host must be explicit loopback")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if !isLoopback(fallback.Host) {
			return errors.New("test database fallback hosts must all be loopback")
		}
	}
	name := config.ConnConfig.Database
	if !strings.HasPrefix(name, "reproq_test_") || name == "reproq_test_" {
		return errors.New("test database name must use the dedicated reproq_test_ prefix")
	}
	return nil
}

func TestDatabaseSafetyGuard(t *testing.T) {
	for _, tc := range []struct {
		name string
		dsn  string
		ok   bool
	}{
		{"IPv4", "postgres://test@127.0.0.1/reproq_test_guard", true},
		{"localhost", "postgres://test@localhost/reproq_test_guard", true},
		{"IPv6", "postgres://test@[::1]/reproq_test_guard", true},
		{"remote", "postgres://test@example.invalid/reproq_test_guard", false},
		{"application", "postgres://test@127.0.0.1/teadata_dev", false},
		{"default", "postgres://test@localhost/reproq", false},
		{"empty suffix", "postgres://test@127.0.0.1/reproq_test_", false},
		{"socket", "host=/tmp dbname=reproq_test_guard", false},
		{"remote fallback", "host=127.0.0.1,example.invalid dbname=reproq_test_guard", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateTestDatabaseURL(tc.dsn) == nil; got != tc.ok {
				t.Fatalf("guard accepted=%v, want %v", got, tc.ok)
			}
		})
	}
}
