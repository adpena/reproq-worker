package db

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reproq-worker/internal/protocol"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A scripted reader fails each real validation stage, not a parallel validator.
type errorReader struct {
	fail       string
	queryCount int
	rowCount   int
}
type scriptRow struct{ scan func(...any) error }

func (r scriptRow) Scan(dest ...any) error { return r.scan(dest...) }

type scriptRows struct {
	pgx.Rows
	next bool
	scan func(...any) error
}

func (r *scriptRows) Next() bool {
	if !r.next {
		r.next = true
		return true
	}
	return false
}
func (r *scriptRows) Close()                 {}
func (r *scriptRows) Err() error             { return nil }
func (r *scriptRows) Scan(dest ...any) error { return r.scan(dest...) }
func (r *errorReader) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	r.rowCount++
	step := []string{"namespace", "history", "function"}[r.rowCount-1]
	return scriptRow{func(d ...any) error {
		if r.fail == step {
			return io.EOF
		}
		switch step {
		case "namespace":
			*d[0].(*string) = "public"
		case "history":
			*d[0].(*int) = 1
			*d[1].(*int) = 1
		}
		return nil
	}}
}
func (r *errorReader) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	r.queryCount++
	step := []string{"relations", "marker"}[r.queryCount-1]
	if r.fail == step {
		return nil, io.EOF
	}
	return &scriptRows{scan: func(d ...any) error {
		if r.fail == step+"-scan" {
			return io.EOF
		}
		if step == "relations" {
			name, schema, kind, persistence := "task_runs", "public", "r", "p"
			partition := false
			*d[0].(*string) = name
			*d[1].(**string) = &schema
			*d[2].(**string) = &kind
			*d[3].(**string) = &persistence
			*d[4].(**bool) = &partition
		} else {
			*d[0].(*int) = 1
			*d[1].(*int) = protocol.SchemaVersion
			*d[2].(*string) = protocol.SchemaID
			*d[3].(*string) = "reproq_django." + protocol.SchemaMigration
			raw, _ := json.Marshal(contractCapabilitiesV2)
			*d[4].(*json.RawMessage) = raw
		}
		return nil
	}}, nil
}
func TestSchemaValidationPreservesTransportCauseAtEveryRead(t *testing.T) {
	for _, step := range []string{"namespace", "relations", "relations-scan", "marker", "marker-scan", "history", "function"} {
		err := RequireSchemaContract(context.Background(), &errorReader{fail: step})
		if !errors.Is(err, ErrSchemaContract) || !errors.Is(err, io.EOF) {
			t.Fatalf("%s discarded validation or transport cause: %v", step, err)
		}
	}
}
