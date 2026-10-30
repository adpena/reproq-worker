package queue

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrWorkflowCounters = errors.New("workflow counters disagree with their expected header count; inspect workflow history before retrying")

// Caller holds the complete ordered transition scope. Only fresh terminal
// transitions contribute to counters; historical failed roots may still close
// an old stranded subtree without counting the same root again.
func finalizeTerminalDependencies(ctx context.Context, tx pgx.Tx, roots, newlyTerminal []int64) error {
	if len(roots) == 0 {
		return nil
	}
	var invalid int64
	err := tx.QueryRow(ctx, terminalScopeSQL+`, blocked AS (
 UPDATE public.task_runs task SET status='FAILED',wait_count=0,
  finished_at=clock_timestamp(),failed_at=clock_timestamp(),updated_at=clock_timestamp(),
  leased_by=NULL,leased_until=NULL,lease_token=NULL,run_after=NULL,
  last_error='dependency_failed: A prerequisite did not complete successfully',
  errors_json=task.errors_json||jsonb_build_object('kind','dependency_failed',
   'message','A prerequisite did not complete successfully','at',clock_timestamp(),
   'failed_result_id',COALESCE(task.parent_id,(SELECT min(root.result_id) FROM public.task_runs root
     WHERE root.result_id=ANY($1::bigint[]) AND root.workflow_id=task.workflow_id)))
 FROM terminal_scope scope WHERE task.result_id=scope.result_id
  AND NOT task.result_id=ANY($1::bigint[]) AND task.status IN ('WAITING','READY')
 RETURNING task.result_id,task.workflow_id
 ), fresh AS (
 SELECT result_id,workflow_id FROM public.task_runs WHERE result_id=ANY($2::bigint[])
 UNION SELECT result_id,workflow_id FROM blocked
 ), affected AS (
 SELECT workflow_id FROM fresh WHERE workflow_id IS NOT NULL
 UNION
 SELECT task.workflow_id FROM public.task_runs task JOIN terminal_scope USING(result_id)
  WHERE task.status IN ('FAILED','CANCELLED') AND task.workflow_id IS NOT NULL
 ), changes AS (
 SELECT workflow.workflow_id,count(fresh.result_id) FILTER(WHERE fresh.result_id<>workflow.callback_result_id) AS failures
 FROM public.workflow_runs workflow JOIN affected USING(workflow_id)
 LEFT JOIN fresh USING(workflow_id) GROUP BY workflow.workflow_id
 ), invalid AS (
 SELECT workflow.workflow_id FROM public.workflow_runs workflow JOIN changes USING(workflow_id)
 WHERE workflow.expected_count<0 OR workflow.success_count<0 OR workflow.failure_count<0
  OR workflow.success_count::bigint+workflow.failure_count+changes.failures>workflow.expected_count
 ), updated AS (
 UPDATE public.workflow_runs workflow SET failure_count=workflow.failure_count+changes.failures,
  status='FAILED',updated_at=clock_timestamp() FROM changes
 WHERE workflow.workflow_id=changes.workflow_id AND NOT EXISTS(SELECT 1 FROM invalid)
 RETURNING workflow.workflow_id
 ) SELECT count(*) FROM invalid`, roots, newlyTerminal).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid != 0 {
		return ErrWorkflowCounters
	}
	return nil
}

// The same already-locked counter authority counts each successful header even
// after another header failed; callback completion never counts as a header.
func completeWorkflowSuccess(ctx context.Context, tx pgx.Tx, resultID int64) error {
	var invalid int64
	var callback *int64
	var status *string
	err := tx.QueryRow(ctx, `WITH target AS (
 SELECT workflow.* FROM public.workflow_runs workflow JOIN public.task_runs task USING(workflow_id)
 WHERE task.result_id=$1
 ), invalid AS (
 SELECT workflow_id FROM target WHERE expected_count<0 OR success_count<0 OR failure_count<0
  OR success_count::bigint+failure_count+CASE WHEN callback_result_id<>$1 THEN 1 ELSE 0 END>expected_count
  OR(callback_result_id=$1 AND status<>'FAILED' AND(success_count<>expected_count OR failure_count<>0))
 ), updated AS (
 UPDATE public.workflow_runs workflow SET
  success_count=workflow.success_count+CASE WHEN workflow.callback_result_id<>$1 THEN 1 ELSE 0 END,
  status=CASE WHEN workflow.status='FAILED' THEN 'FAILED'
   WHEN workflow.callback_result_id=$1 THEN 'SUCCESSFUL'
   WHEN workflow.success_count+1=workflow.expected_count AND workflow.failure_count=0 THEN 'WAITING_CALLBACK'
   ELSE workflow.status END,updated_at=clock_timestamp()
 FROM target WHERE workflow.workflow_id=target.workflow_id AND NOT EXISTS(SELECT 1 FROM invalid)
 RETURNING workflow.callback_result_id,workflow.status
 ) SELECT (SELECT count(*) FROM invalid),(SELECT callback_result_id FROM updated),(SELECT status FROM updated)`, resultID).Scan(&invalid, &callback, &status)
	if err != nil {
		return err
	}
	if invalid != 0 {
		return ErrWorkflowCounters
	}
	if callback != nil && status != nil && *status == "WAITING_CALLBACK" {
		_, err = tx.Exec(ctx, `UPDATE public.task_runs SET status='READY',wait_count=0,updated_at=clock_timestamp()
   WHERE result_id=$1 AND status='WAITING'`, *callback)
	}
	return err
}
