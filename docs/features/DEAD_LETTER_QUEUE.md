# Feature: Dead Letter Queue (DLQ) & Triage

## Problem Statement
When tasks exhaust all retries, they are marked as `FAILED`. Currently, there is no easy way for operators to inspect failures in bulk, fix payloads, and retry them.

## Proposed Solution
- Maintain terminal `FAILED` tasks in the `task_runs` table.
- Implement a `reproq triage` command to list, inspect, and bulk-retry failed tasks.
- Add a `last_error` column for quick summary inspection.

## Implementation Checklist
- [x] Migration: Add `last_error` and `failed_at` columns.
- [x] CLI: Implement `reproq triage list`.
- [x] CLI: Implement `reproq triage inspect <id>`.
- [x] CLI: Implement `reproq triage retry <id|--all>`.
- [x] Logic: Ensure retried tasks get a fresh `attempt_count`.

## Usage
```bash
# List failed tasks
./reproq triage list --dsn "postgres://..." --limit 50

# Inspect a failed task
./reproq triage inspect --dsn "postgres://..." --id 12345

# Retry a failed task
./reproq triage retry --dsn "postgres://..." --id 12345

# Retry all failed tasks
./reproq triage retry --dsn "postgres://..." --all
```

## References
- Inspired by Celery's Flower and Sidekiq's Retries/Dead tabs.
