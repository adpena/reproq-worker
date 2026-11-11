package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a new PostgreSQL connection pool.
func NewPool(ctx context.Context, connString string) (*pgxpool.Pool, error) {
	return newPool(ctx, connString, false)
}

// NewLowMemoryPool verifies connectivity without maintaining a connection floor.
func NewLowMemoryPool(ctx context.Context, connString string) (*pgxpool.Pool, error) {
	return newPool(ctx, connString, true)
}

func newPool(ctx context.Context, connString string, lowMemory bool) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	configurePool(config, lowMemory)

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}

func configurePool(config *pgxpool.Config, lowMemory bool) {
	config.MaxConns = 20
	config.MinConns = 2
	if lowMemory {
		config.MinConns = 0
		config.MinIdleConns = 0
	}
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		return RequireSchemaContract(ctx, connection)
	}
}
