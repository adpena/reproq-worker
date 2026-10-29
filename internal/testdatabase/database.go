// Package testdatabase owns opt-in destructive integration-test custody guards.
// Production worker packages do not import it.
package testdatabase

import (
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
)

// Destructive queue tests never use the ambient application DATABASE_URL.
func URL(tb testing.TB) string {
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
	if err := ValidateURL(dsn); err != nil {
		tb.Fatal(err)
	}
	return dsn
}

func ValidateURL(dsn string) error {
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
