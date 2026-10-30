package queue

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const transitionBatchSize = 1000

// Topology is immutable after the producer's atomic insert. UNION carries only
// IDs, so cycles terminate without retaining a path array for every descendant.
// Failed/cancelled bridges let maintenance repair an older stranded subtree;
// successful history is never a failure propagation edge.
const terminalScopeSQL = `WITH RECURSIVE terminal_scope(result_id) AS (
 SELECT result_id FROM public.task_runs WHERE result_id=ANY($1::bigint[])
 UNION
 SELECT next.result_id FROM terminal_scope scope CROSS JOIN LATERAL (
  SELECT child.result_id FROM public.task_runs child
   WHERE child.parent_id=scope.result_id AND child.status IN ('WAITING','FAILED','CANCELLED')
  UNION ALL
  SELECT callback.result_id FROM public.task_runs member
   JOIN public.workflow_runs workflow ON workflow.workflow_id=member.workflow_id
   JOIN public.task_runs callback ON callback.result_id=workflow.callback_result_id
   WHERE member.result_id=scope.result_id AND callback.result_id<>member.result_id
    AND callback.status IN ('WAITING','READY','FAILED','CANCELLED')
 ) next
), lock_scope(result_id) AS (
 SELECT result_id FROM terminal_scope
 UNION
 SELECT result_id FROM public.task_runs WHERE parent_id=ANY($1::bigint[])
) `

const directScopeSQL = `WITH terminal_scope(result_id) AS (
 SELECT result_id FROM public.task_runs WHERE result_id=ANY($1::bigint[])
 UNION
 SELECT result_id FROM public.task_runs WHERE parent_id=ANY($1::bigint[])
 UNION
 SELECT callback.result_id FROM public.task_runs member
 JOIN public.workflow_runs workflow ON workflow.workflow_id=member.workflow_id
 JOIN public.task_runs callback ON callback.result_id=workflow.callback_result_id
 WHERE member.result_id=ANY($1::bigint[]) AND callback.result_id<>member.result_id
), lock_scope(result_id) AS(SELECT result_id FROM terminal_scope) `

// Lock the entire task scope, including FK SET NULL targets for pruning, before
// touching a workflow counter or a terminal root. Heartbeats do not need this
// scope and retain their independent single-row ownership fence.
func lockTransitionScope(ctx context.Context, tx pgx.Tx, roots []int64, recursive, skipLocked bool) (bool, error) {
	if len(roots) == 0 {
		return true, nil
	}
	if len(roots) > transitionBatchSize {
		return false, errors.New("transition root batch exceeds its bound")
	}
	scopeSQL := directScopeSQL
	if recursive {
		scopeSQL = terminalScopeSQL
	}
	suffix := ""
	if skipLocked {
		suffix = " SKIP LOCKED"
	}
	var expected, locked int64
	err := tx.QueryRow(ctx, scopeSQL+`, locked AS MATERIALIZED (
 SELECT task.result_id FROM public.task_runs task JOIN lock_scope USING(result_id)
 ORDER BY task.result_id FOR UPDATE OF task`+suffix+`
 ) SELECT (SELECT count(*) FROM lock_scope),(SELECT count(*) FROM locked)`, roots).Scan(&expected, &locked)
	if err != nil || (skipLocked && expected != locked) {
		return false, err
	}
	err = tx.QueryRow(ctx, scopeSQL+`, workflows AS (
 SELECT DISTINCT task.workflow_id FROM public.task_runs task JOIN terminal_scope USING(result_id)
 WHERE task.workflow_id IS NOT NULL
 ), locked AS MATERIALIZED (
 SELECT workflow.workflow_id FROM public.workflow_runs workflow JOIN workflows USING(workflow_id)
 ORDER BY workflow.workflow_id FOR UPDATE OF workflow`+suffix+`
 ) SELECT (SELECT count(*) FROM public.workflow_runs workflow JOIN workflows USING(workflow_id)),
          (SELECT count(*) FROM locked)`, roots).Scan(&expected, &locked)
	if err != nil || (skipLocked && expected != locked) {
		return false, err
	}
	return true, nil
}

// Each bounded batch owns one transaction. A contended scope releases ALL of
// its partial locks before bisecting; disjoint scopes can still make progress.
// No successful batch keeps a workflow lock while acquiring another task set.
func withAvailableTransitionScopes(ctx context.Context, pool *pgxpool.Pool, roots []int64, recursive bool, apply func(pgx.Tx, []int64) (func(), error)) error {
	if len(roots) == 0 {
		return nil
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	available, err := lockTransitionScope(ctx, tx, roots, recursive, true)
	if err != nil {
		return err
	}
	if !available {
		if err := tx.Rollback(ctx); err != nil {
			return err
		}
		if len(roots) == 1 {
			return nil
		}
		middle := len(roots) / 2
		if err := withAvailableTransitionScopes(ctx, pool, roots[:middle], recursive, apply); err != nil {
			return err
		}
		return withAvailableTransitionScopes(ctx, pool, roots[middle:], recursive, apply)
	}
	committed, err := apply(tx, roots)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if committed != nil {
		committed()
	}
	return nil
}
