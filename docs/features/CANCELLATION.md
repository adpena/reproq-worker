# Feature: Remote Control & Cancellation

## Problem Statement
Operators need to stop a long-running task immediately without killing the entire worker process.

## Proposed Solution
- Add a `cancel_requested` boolean to `task_runs`.
- The `Heartbeat` loop in the worker checks this flag every few seconds.
- If true, the `Runner` cancels the task's `execCtx`, which kills the sub-process.

## Implementation Checklist
- [x] Migration: Add `cancel_requested` column.
- [x] CLI: Implement `reproq cancel --id <task_id>`.
- [x] Logic: Update `internal/runner.runHeartbeat` to check the flag and trigger cancellation.

## Usage
```bash
./reproq cancel --dsn "postgres://..." --id 12345
```

Notes:
- Cancellation is only honored for `RUNNING` tasks.
- The worker checks `cancel_requested` during heartbeats and cancels the subprocess.

## References
- Kubernetes `terminationGracePeriod`.
- Celery `revoke(terminate=True)`.
