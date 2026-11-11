package db

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLowMemoryPoolClearsBothConnectionMinimums(t *testing.T) {
	parsed, err := pgxpool.ParseConfig("postgres://localhost/reproq_test_pool?pool_min_conns=4&pool_min_idle_conns=3")
	if err != nil {
		t.Fatal(err)
	}
	normal, lowMemory := parsed.Copy(), parsed.Copy()
	configurePool(normal, false)
	configurePool(lowMemory, true)
	if normal.MinConns != 2 || normal.MinIdleConns != 3 {
		t.Fatalf("normal connection minimums = (%d, %d)", normal.MinConns, normal.MinIdleConns)
	}
	if lowMemory.MinConns != 0 || lowMemory.MinIdleConns != 0 {
		t.Fatalf("low memory connection minimums = (%d, %d)", lowMemory.MinConns, lowMemory.MinIdleConns)
	}
	if lowMemory.MaxConns != normal.MaxConns || lowMemory.AfterConnect == nil {
		t.Fatal("low memory mode must preserve pool capacity and the schema guard")
	}
}
