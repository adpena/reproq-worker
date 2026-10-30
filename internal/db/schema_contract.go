package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reproq-worker/internal/protocol"
	"slices"

	"github.com/jackc/pgx/v5"
)

// This is a wire compatibility check, never a schema initializer. The only DDL
// authority is reproq-django's committed migration named below.

var contractRelationsV2 = []string{
	"periodic_tasks", "rate_limits", "reproq_queue_controls", "reproq_workers",
	"task_runs", "workflow_runs", "reproq_schema_contract", "django_migrations",
}

var contractCapabilitiesV2 = []string{
	"lease-token-v1", "canonical-spec-v1", "periodic-occurrence-v1", "managed-six-tables-v1", "periodic-enqueue-v1", "scheduler-provider-v1",
}

var ErrSchemaContract = errors.New("incompatible ReproQ schema; apply committed reproq-django migrations on the selected queue database")

type contractReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// RequireSchemaContract binds the real connection's name resolution as well as
// its marker and ledger. Temporary or shadow relations cannot certify a queue.
func RequireSchemaContract(ctx context.Context, connection contractReader) error {
	var namespace string
	if err := connection.QueryRow(ctx, "SELECT pg_catalog.current_schema()").Scan(&namespace); err != nil {
		return fmt.Errorf("%w: namespace check: %v", ErrSchemaContract, err)
	}
	if namespace != "public" {
		return fmt.Errorf("%w: only the public schema is qualified for v2", ErrSchemaContract)
	}
	rows, err := connection.Query(ctx, `
		SELECT requested.name,n.nspname,c.relkind::text,c.relpersistence::text,c.relispartition
		FROM pg_catalog.unnest($1::text[]) AS requested(name)
		LEFT JOIN pg_catalog.pg_class c ON c.oid=pg_catalog.to_regclass(requested.name)
		LEFT JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
	`, contractRelationsV2)
	if err != nil {
		return fmt.Errorf("%w: relation check: %v", ErrSchemaContract, err)
	}
	for rows.Next() {
		var name string
		var schema, kind, persistence *string
		var partition *bool
		if err := rows.Scan(&name, &schema, &kind, &persistence, &partition); err != nil {
			rows.Close()
			return fmt.Errorf("%w: relation scan: %v", ErrSchemaContract, err)
		}
		if schema == nil || *schema != "public" || kind == nil || *kind != "r" ||
			persistence == nil || *persistence != "p" || partition == nil || *partition {
			rows.Close()
			return fmt.Errorf("%w: %s must resolve to an ordinary permanent public table", ErrSchemaContract, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("%w: relation iteration: %v", ErrSchemaContract, err)
	}
	rows.Close()
	rows, err = connection.Query(ctx, `
		SELECT singleton_id,version,contract_id,migration_identity,capabilities
		FROM public.reproq_schema_contract LIMIT 2
	`)
	if err != nil {
		return fmt.Errorf("%w: marker check: %v", ErrSchemaContract, err)
	}
	count := 0
	for rows.Next() {
		count++
		var singleton, version int
		var contract, migration string
		var raw json.RawMessage
		if err := rows.Scan(&singleton, &version, &contract, &migration, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("%w: marker scan: %v", ErrSchemaContract, err)
		}
		var capabilities []string
		if err := json.Unmarshal(raw, &capabilities); err != nil || singleton != 1 || version != protocol.SchemaVersion ||
			contract != protocol.SchemaID || migration != "reproq_django."+protocol.SchemaMigration ||
			!slices.Equal(capabilities, contractCapabilitiesV2) {
			rows.Close()
			return fmt.Errorf("%w: unsupported marker or capabilities", ErrSchemaContract)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("%w: marker iteration: %v", ErrSchemaContract, err)
	}
	rows.Close()
	if count != 1 {
		return fmt.Errorf("%w: exactly one marker is required", ErrSchemaContract)
	}
	var priorRecorded, currentRecorded int
	if err := connection.QueryRow(ctx, `
		SELECT count(*) FILTER(WHERE name='0002_schema_contract_v1'),count(*) FILTER(WHERE name=$1)
		FROM public.django_migrations WHERE app='reproq_django'
	`, protocol.SchemaMigration).Scan(&priorRecorded, &currentRecorded); err != nil || priorRecorded != 1 || currentRecorded != 1 {
		return fmt.Errorf("%w: matching migration history is required", ErrSchemaContract)
	}

	var language, volatility, parallel, result, bodyHash string
	var definer bool
	var settings []string
	var defaults int
	var arguments []string
	err = connection.QueryRow(ctx, `SELECT l.lanname,p.prosecdef,p.provolatile::text,p.proparallel::text,p.proconfig,
      pg_catalog.pg_get_function_result(p.oid),pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(p.prosrc,'UTF8')),'hex'),p.pronargdefaults,p.proargnames
      FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_language l ON l.oid=p.prolang
      WHERE p.oid=pg_catalog.to_regprocedure('public.reproq_enqueue_periodic_occurrence(text,timestamp with time zone,text)')
    `).Scan(&language, &definer, &volatility, &parallel, &settings, &result, &bodyHash, &defaults, &arguments)
	if err != nil || language != "plpgsql" || definer || volatility != "v" || parallel != "u" ||
		!slices.Equal(settings, []string{"search_path=pg_catalog, pg_temp"}) || result != "TABLE(result_id bigint, inserted boolean)" || bodyHash != protocol.PeriodicFunctionBodySHA256 || defaults != 0 || !slices.Equal(arguments, []string{"p_name", "p_scheduled_for", "p_provider", "result_id", "inserted"}) {
		return fmt.Errorf("%w: immutable periodic enqueue primitive is missing or incompatible", ErrSchemaContract)
	}

	return nil
}
