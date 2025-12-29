# Feature: Cron-like Scheduling (Beat)

## Problem Statement
Users need to run tasks at regular intervals (e.g., every Monday at 8 AM) without managing external cron jobs.

## Proposed Solution
- Implement a `periodic_tasks` table with cron expressions.
- Create a `reproq beat` subcommand that monitors these specs and enqueues new `task_runs` when the schedule is due.

## Implementation Checklist
- [ ] Migration: Create `periodic_tasks` table (`name`, `cron_expr`, `task_path`, `payload`, `last_run_at`).
- [ ] Logic: Implement cron parser and "next run" calculator.
- [ ] CLI: Implement `reproq beat` loop.

## References
- `robfig/cron` (Go library).
- Celery Beat.
