package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"reproq-worker/internal/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoTasks = errors.New("no tasks available")
var ErrLeaseLost = errors.New("task lease expired or belongs to another claim")
var ErrCancelRequested = errors.New("owned task claim has a cancellation request")
var ErrLegacySpec = errors.New("legacy spec_version 0 cannot be claimed, retried or replayed by this runtime; drain queued legacy work with the old runtime before cutover or perform an explicitly audited reconciliation; historical payloads and hashes are unchanged")

func requireCurrentSpecVersion(version int) error {
	if version == 0 {
		return ErrLegacySpec
	}
	if version != 1 {
		return fmt.Errorf("unsupported task spec_version %d; install a compatible runtime before claiming, retrying or replaying", version)
	}
	return nil
}

// Internal SQL expressions only. A shared key obeys the strongest positive
// constraint contributed by the candidate or any currently active member.
// A zero limit contributes no cap, but still counts and participates in locking.
func concurrencyCapacitySQL(keyExpression, limitExpression string) string {
	return fmt.Sprintf(`(%[1]s IS NULL OR %[1]s='' OR (
      SELECT count(*)<COALESCE(LEAST(CASE WHEN %[2]s>0 THEN %[2]s END,
        min(CASE WHEN active.concurrency_limit>0 THEN active.concurrency_limit END)),count(*)+1)
      FROM public.task_runs active WHERE active.status='RUNNING'
        AND active.leased_until>clock_timestamp() AND active.concurrency_key=%[1]s))`, keyExpression, limitExpression)
}

type Service struct {
	pool *pgxpool.Pool
}

func NewService(ctx context.Context, pool *pgxpool.Pool) (*Service, error) {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Release()
	if err := db.RequireSchemaContract(ctx, connection.Conn()); err != nil {
		return nil, err
	}
	return &Service{pool: pool}, nil
}

// Claim atomically reserves the candidate's shared keys, then rechecks ownership
// in a fresh READ COMMITTED statement before transitioning the row. Advisory
// locks never share the selection statement's earlier MVCC snapshot.
func (s *Service) Claim(ctx context.Context, workerID string, queueName string, leaseSeconds int, priorityAgingFactor float64) (*TaskRun, error) {
	if strings.TrimSpace(workerID) == "" || leaseSeconds <= 0 {
		return nil, errors.New("worker owner and positive lease duration are required")
	}
	if math.IsNaN(priorityAgingFactor) || math.IsInf(priorityAgingFactor, 0) || priorityAgingFactor < 0 {
		return nil, errors.New("priority aging factor must be finite and nonnegative")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	excluded := []int64{}
	for range 32 {
		if _, err := tx.Exec(ctx, "SAVEPOINT claim_candidate"); err != nil {
			return nil, err
		}
		var id int64
		var taskPath string
		var lockKey, concurrencyKey *string
		var concurrencyLimit, specVersion int
		err := tx.QueryRow(ctx, `
			SELECT tr.result_id,COALESCE(tr.task_path,tr.spec_json->>'task_path'),tr.lock_key,tr.concurrency_key,tr.concurrency_limit,tr.spec_version
			FROM public.task_runs tr
			WHERE tr.status='READY' AND tr.attempts<tr.max_attempts AND tr.queue_name=$1
			 AND NOT (tr.result_id=ANY($3::bigint[]))
			 AND COALESCE(tr.run_after,'-infinity'::timestamptz)<=clock_timestamp()
			 AND (tr.expires_at IS NULL OR tr.expires_at>clock_timestamp())
			 AND NOT EXISTS(SELECT 1 FROM public.reproq_queue_controls qc WHERE qc.queue_name=$1 AND qc.paused)
			 AND (tr.lock_key IS NULL OR tr.lock_key='' OR NOT EXISTS(
			  SELECT 1 FROM public.task_runs active WHERE active.status='RUNNING'
			   AND active.leased_until>clock_timestamp() AND active.lock_key=tr.lock_key))
			 AND `+concurrencyCapacitySQL("tr.concurrency_key", "tr.concurrency_limit")+`
			ORDER BY CASE WHEN $2::double precision>0 THEN tr.priority+
			 GREATEST(EXTRACT(EPOCH FROM(clock_timestamp()-tr.enqueued_at)),0)/NULLIF($2,0)
			 ELSE tr.priority END DESC,tr.enqueued_at,tr.result_id
			LIMIT 1 FOR UPDATE OF tr SKIP LOCKED
		`, queueName, priorityAgingFactor, excluded).Scan(&id, &taskPath, &lockKey, &concurrencyKey, &concurrencyLimit, &specVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoTasks
		}
		if err != nil {
			return nil, err
		}
		if err := requireCurrentSpecVersion(specVersion); err != nil {
			return nil, err
		}
		excluded = append(excluded, id)
		keys := []string{}
		if lockKey != nil && *lockKey != "" {
			keys = append(keys, "reproq:lock:"+*lockKey)
		}
		if concurrencyKey != nil && *concurrencyKey != "" {
			keys = append(keys, "reproq:concurrency:"+*concurrencyKey)
		}
		slices.Sort(keys)
		eligible := true
		for _, key := range keys {
			var locked bool
			if err := tx.QueryRow(ctx, "SELECT pg_catalog.pg_try_advisory_xact_lock(pg_catalog.hashtextextended($1,0))", key).Scan(&locked); err != nil {
				return nil, err
			}
			if !locked {
				claimBlocked.WithLabelValues("key_lock").Inc()
				eligible = false
				break
			}
		}
		if eligible {
			// This statement begins after the key locks, so it sees any prior
			// claimant that committed while the candidate was being selected.
			err = tx.QueryRow(ctx, `
			 SELECT ($1::text IS NULL OR $1='' OR NOT EXISTS(
			  SELECT 1 FROM public.task_runs WHERE status='RUNNING' AND leased_until>clock_timestamp() AND lock_key=$1))
			 AND `+concurrencyCapacitySQL("$2::text", "$3::integer"), lockKey, concurrencyKey, concurrencyLimit).Scan(&eligible)
			if err != nil {
				return nil, err
			}
			if !eligible {
				claimBlocked.WithLabelValues("key_capacity").Inc()
			}
		}
		if eligible {
			// The rate-limit row itself arbitrates simultaneous token consumers;
			// PostgreSQL rechecks this UPDATE after a competing row writer commits.
			var allowed bool
			err = tx.QueryRow(ctx, `
			 WITH chosen AS (
			  SELECT key FROM public.rate_limits WHERE tokens_per_second>0 AND burst_size>0
			   AND key=ANY($1::text[])
			  ORDER BY array_position($1::text[],key) LIMIT 1
			 ), consumed AS (
			  UPDATE public.rate_limits SET current_tokens=LEAST(burst_size,current_tokens+
			   (EXTRACT(EPOCH FROM(clock_timestamp()-last_refilled_at))*tokens_per_second))-1,last_refilled_at=clock_timestamp()
			  WHERE key=(SELECT key FROM chosen) AND LEAST(burst_size,current_tokens+
			   (EXTRACT(EPOCH FROM(clock_timestamp()-last_refilled_at))*tokens_per_second))>=1 RETURNING key
			 ) SELECT NOT EXISTS(SELECT 1 FROM chosen) OR EXISTS(SELECT 1 FROM consumed)
			`, []string{"task:" + taskPath, "queue:" + queueName, "global"}).Scan(&allowed)
			if err != nil {
				return nil, err
			}
			eligible = allowed
			if !allowed {
				claimBlocked.WithLabelValues("rate_limit").Inc()
			}
		}
		if !eligible {
			if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT claim_candidate"); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT claim_candidate"); err != nil {
				return nil, err
			}
			continue
		}
		var task TaskRun
		err = tx.QueryRow(ctx, `
		 UPDATE public.task_runs SET status='RUNNING',started_at=COALESCE(started_at,clock_timestamp()),
		  last_attempted_at=clock_timestamp(),attempts=attempts+1,
		  leased_until=clock_timestamp()+($2::integer*interval '1 second'),leased_by=$3::text,
		  lease_token=pg_catalog.gen_random_uuid(),
		  worker_ids=COALESCE(worker_ids,'[]'::jsonb)||jsonb_build_array($3::text),updated_at=clock_timestamp()
		 WHERE result_id=$1 AND status='READY' AND attempts<max_attempts
		  AND COALESCE(run_after,'-infinity'::timestamptz)<=clock_timestamp()
		  AND (expires_at IS NULL OR expires_at>clock_timestamp())
		  AND NOT EXISTS(SELECT 1 FROM public.reproq_queue_controls qc WHERE qc.queue_name=task_runs.queue_name AND qc.paused)
		 RETURNING result_id,backend_alias,queue_name,priority,spec_json,spec_hash,status,enqueued_at,
		  attempts,max_attempts,timeout_seconds,lock_key,concurrency_key,concurrency_limit,leased_until,leased_by,task_path,
		  lease_token::text,canonical_spec_text,spec_version,dedupe_active,periodic_name,scheduled_for,periodic_provider
		`, id, leaseSeconds, workerID).Scan(&task.ResultID, &task.BackendAlias, &task.QueueName, &task.Priority, &task.SpecJSON, &task.SpecHash,
			&task.Status, &task.EnqueuedAt, &task.Attempts, &task.MaxAttempts, &task.TimeoutSeconds, &task.LockKey, &task.ConcurrencyKey,
			&task.ConcurrencyLimit, &task.LeasedUntil, &task.LeasedBy, &task.TaskPath, &task.LeaseToken, &task.CanonicalSpecText,
			&task.SpecVersion, &task.DedupeActive, &task.PeriodicName, &task.ScheduledFor, &task.PeriodicProvider)
		if errors.Is(err, pgx.ErrNoRows) {
			// Rate-limit arbitration can wait. Eligibility must still hold after
			// every wait; refund token consumption and skip this candidate.
			if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT claim_candidate"); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT claim_candidate"); err != nil {
				return nil, err
			}
			claimBlocked.WithLabelValues("eligibility").Inc()
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &task, nil
	}
	claimBlocked.WithLabelValues("scan_budget").Inc()
	return nil, ErrNoTasks
}

// Lock by identity first: PostgreSQL can evaluate a volatile UPDATE predicate
// before waiting on an unchanged row lock. The fence must be a fresh statement.
func lockClaimRow(ctx context.Context, tx pgx.Tx, resultID int64) error {
	var lockedID int64
	err := tx.QueryRow(ctx, "SELECT result_id FROM public.task_runs WHERE result_id=$1 FOR UPDATE", resultID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

// Heartbeat cannot renew an expired lease or a different claim, even when a
// restarted worker reuses the same worker ID and attempt number.
func (s *Service) Heartbeat(ctx context.Context, resultID int64, workerID, leaseToken string, leaseSeconds int) (bool, error) {
	if workerID == "" || leaseToken == "" || leaseSeconds <= 0 {
		return false, ErrLeaseLost
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockClaimRow(ctx, tx, resultID); err != nil {
		return false, err
	}
	var cancelled bool
	err = tx.QueryRow(ctx, `
        UPDATE public.task_runs SET
          leased_until=CASE WHEN cancel_requested THEN leased_until ELSE clock_timestamp()+($1::integer*interval '1 second') END,
          updated_at=clock_timestamp()
        WHERE result_id=$2 AND leased_by=$3 AND lease_token=$4::uuid
          AND status='RUNNING' AND leased_until>clock_timestamp()
        RETURNING cancel_requested
    `, leaseSeconds, resultID, workerID, leaseToken).Scan(&cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrLeaseLost
	}
	if err != nil {
		return false, err
	}
	return cancelled, tx.Commit(ctx)
}

// CompleteSuccess marks the task as SUCCESSFUL and triggers dependents.
func (s *Service) CompleteSuccess(ctx context.Context, resultID int64, workerID, leaseToken string, returnJSON json.RawMessage, logsURI *string) error {
	if workerID == "" || leaseToken == "" {
		return ErrLeaseLost
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if _, err := lockTransitionScope(ctx, tx, []int64{resultID}, false, false); err != nil {
		return err
	}

	// 1. Finalize the task
	query := `
		UPDATE public.task_runs
		SET status = 'SUCCESSFUL',
		    finished_at = clock_timestamp(),
		    return_json = $1,
		    last_error = NULL,
		    failed_at = NULL,
		    leased_until = NULL,
		    leased_by = NULL,
		    lease_token = NULL,
		    logs_uri = COALESCE($4, logs_uri),
		    updated_at = clock_timestamp()
		WHERE result_id = $2 AND leased_by = $3 AND lease_token=$5::uuid AND status = 'RUNNING' AND leased_until>clock_timestamp() AND NOT cancel_requested
	`
	res, err := tx.Exec(ctx, query, returnJSON, resultID, workerID, logsURI, leaseToken)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		var cancelled bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.task_runs WHERE result_id=$1 AND leased_by=$2
          AND lease_token=$3::uuid AND status='RUNNING' AND leased_until>clock_timestamp() AND cancel_requested)`, resultID, workerID, leaseToken).Scan(&cancelled)
		if err != nil {
			return err
		}
		if cancelled {
			return ErrCancelRequested
		}
		return ErrLeaseLost
	}

	// 2. Trigger dependents
	// Decrement wait_count for direct children (chains).
	triggerQuery := `
		UPDATE public.task_runs
		SET wait_count = wait_count - 1,
		    status = CASE
				WHEN wait_count - 1 <= 0 THEN 'READY'
				ELSE 'WAITING'
			END,
		    updated_at = NOW()
		WHERE parent_id = $1 AND status = 'WAITING'
	`
	_, err = tx.Exec(ctx, triggerQuery, resultID)
	if err != nil {
		return fmt.Errorf("failed to trigger dependents: %w", err)
	}

	if err := completeWorkflowSuccess(ctx, tx, resultID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// CompleteFailure marks the task as FAILED or requeues it.
func (s *Service) CompleteFailure(ctx context.Context, resultID int64, workerID, leaseToken string, errorObj json.RawMessage, shouldRetry bool, nextRunAfter time.Time, logsURI *string) (TaskStatus, error) {
	if workerID == "" || leaseToken == "" {
		return "", ErrLeaseLost
	}
	lastError := summarizeError(errorObj)
	query := `
		WITH completion AS (
			SELECT result_id,
			       CASE WHEN cancel_requested THEN 'CANCELLED' WHEN $1::boolean AND attempts < max_attempts THEN 'READY' ELSE 'FAILED' END AS target_status
			FROM public.task_runs
			WHERE result_id = $4 AND leased_by = $5 AND lease_token=$8::uuid AND status = 'RUNNING' AND leased_until>clock_timestamp()
		)
		UPDATE public.task_runs tr
		SET status = completion.target_status,
		    finished_at = CASE WHEN completion.target_status IN ('FAILED','CANCELLED') THEN clock_timestamp() ELSE NULL END,
		    errors_json = tr.errors_json || $2::jsonb,
		    run_after = CASE WHEN completion.target_status IN ('FAILED','CANCELLED') THEN NULL ELSE $3::timestamptz END,
		    last_error = CASE WHEN completion.target_status IN ('FAILED','CANCELLED') THEN $6 ELSE NULL END,
		    failed_at = CASE WHEN completion.target_status IN ('FAILED','CANCELLED') THEN clock_timestamp() ELSE NULL END,
		    leased_until = NULL,
		    leased_by = NULL,
		    lease_token = NULL,
		    logs_uri = COALESCE($7, tr.logs_uri),
		    updated_at = clock_timestamp()
		FROM completion
		WHERE tr.result_id = completion.result_id
		RETURNING tr.status
	`
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if _, err := lockTransitionScope(ctx, tx, []int64{resultID}, true, false); err != nil {
		return "", err
	}

	var status TaskStatus
	err = tx.QueryRow(ctx, query, shouldRetry, errorObj, nextRunAfter, resultID, workerID, lastError, logsURI, leaseToken).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLeaseLost
	}
	if err != nil {
		return "", err
	}

	if status == StatusFailed || status == StatusCancelled {
		if err := finalizeTerminalDependencies(ctx, tx, []int64{resultID}, []int64{resultID}); err != nil {
			return "", err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return status, nil
}

var ErrInvalidPruneLimit = errors.New("prune limit must be nonnegative (0 = no limit)")

// ValidatePruneLimit rejects invalid bounds before the CLI opens a connection or
// the service accesses its pool. Zero explicitly requests an unbounded prune.
func ValidatePruneLimit(limit int) error {
	if limit < 0 {
		return ErrInvalidPruneLimit
	}
	return nil
}

// Replay creates a new READY task from an existing one.
func (s *Service) Replay(ctx context.Context, resultID int64) (int64, error) {
	_, newID, err := s.replaySelected(ctx, "result_id=$1", resultID)
	return newID, err
}

// ReplayBySpecHash creates a new READY task from the most recent matching spec_hash.
func (s *Service) ReplayBySpecHash(ctx context.Context, specHash string) (int64, int64, error) {
	return s.replaySelected(ctx, "spec_hash=$1", specHash)
}

// Only these internal predicates select a source. Lock it before checking the
// version so no parallel edit can relabel a legacy row between check and copy.
func (s *Service) replaySelected(ctx context.Context, predicate string, value any) (int64, int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sourceID int64
	var version int
	if err := tx.QueryRow(ctx, "SELECT result_id,spec_version FROM public.task_runs WHERE "+predicate+" ORDER BY result_id DESC LIMIT 1 FOR UPDATE", value).Scan(&sourceID, &version); err != nil {
		return 0, 0, err
	}
	if err := requireCurrentSpecVersion(version); err != nil {
		return 0, 0, err
	}
	var newID int64
	err = tx.QueryRow(ctx, `INSERT INTO public.task_runs
        (backend_alias,queue_name,priority,spec_json,spec_hash,task_path,status,max_attempts,timeout_seconds,
         lock_key,concurrency_key,concurrency_limit,canonical_spec_text,spec_version,dedupe_active,periodic_provider)
        SELECT backend_alias,queue_name,priority,spec_json,spec_hash,task_path,'READY',max_attempts,timeout_seconds,
         lock_key,concurrency_key,concurrency_limit,canonical_spec_text,spec_version,dedupe_active,periodic_provider
        FROM public.task_runs WHERE result_id=$1 RETURNING result_id`, sourceID).Scan(&newID)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return sourceID, newID, nil
}

func (s *Service) RegisterWorker(ctx context.Context, id, hostname string, concurrency int, queues []string, version string) error {
	queuesJSON, err := json.Marshal(queues)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO public.reproq_workers (worker_id, hostname, concurrency, queues, version, started_at, last_seen_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, NOW(), NOW())
		ON CONFLICT (worker_id) DO UPDATE
		SET hostname = EXCLUDED.hostname,
		    concurrency = EXCLUDED.concurrency,
		    queues = EXCLUDED.queues,
		    version = EXCLUDED.version,
		    last_seen_at = NOW()
	`
	_, err = s.pool.Exec(ctx, query, id, hostname, concurrency, queuesJSON, version)
	return err
}

func (s *Service) UpdateWorkerHeartbeat(ctx context.Context, id string) error {
	query := `UPDATE public.reproq_workers SET last_seen_at = NOW() WHERE worker_id = $1`
	_, err := s.pool.Exec(ctx, query, id)
	return err
}
