# Feature: Remote Control & Cancellation

> Historical design note: this initial proposal covered cancellation of running
> tasks. It is not the current operator contract. See the
> [current cancellation contract](../../README.md#cancel) for supported states,
> completion fencing and workflow finalization.

## Original Problem Statement
Operators need to stop a long-running task immediately without killing the entire worker process.

## Original Proposed Solution
- Add a `cancel_requested` boolean to `task_runs`.
- The `Heartbeat` loop in the worker checks this flag every few seconds.
- If true, the `Runner` cancels the task's `execCtx`, which kills the sub-process.

## Original Implementation Checklist
- [x] Migration: Add `cancel_requested` column.
- [x] CLI: Implement `reproq cancel --id <task_id>`.
- [x] Logic: Update `internal/runner.runHeartbeat` to check the flag and trigger cancellation.

## Original CLI Example
```bash
./reproq cancel --dsn "postgres://..." --id 12345
```

Original proposal notes:
- This proposal covered only `RUNNING` tasks and heartbeat-driven subprocess cancellation.
- Current pending-task cancellation and its transition scope are documented in the README linked above.

## References
- Kubernetes `terminationGracePeriod`.
- Celery `revoke(terminate=True)`.
