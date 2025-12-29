# Feature: Dead Letter Queue (DLQ) & Triage

## Problem Statement
When tasks exhaust all retries, they are marked as `FAILED`. Currently, there is no easy way for operators to inspect failures in bulk, fix payloads, and retry them.

## Proposed Solution
- Maintain terminal `FAILED` tasks in the `task_runs` table.
- Implement a `reproq triage` command to list, inspect, and bulk-retry failed tasks.
- Add a `last_error` column for quick summary inspection.

## Implementation Checklist
- [ ] Migration: Add `last_error` and `failed_at` columns.
- [ ] CLI: Implement `reproq triage list`.
- [ ] CLI: Implement `reproq triage inspect <id>`.
- [ ] CLI: Implement `reproq triage retry <id|--all>`.
- [ ] Logic: Ensure retried tasks get a fresh `attempt_count`.

## References
- Inspired by Celery's Flower and Sidekiq's Retries/Dead tabs.
