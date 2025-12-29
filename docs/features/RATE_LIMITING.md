# Feature: Global & Per-Task Rate Limiting

## Problem Statement
Users need to prevent overwhelming external APIs or internal resources. A burst of 10,000 tasks should not all attempt to execute simultaneously if the target API only allows 5 requests per second.

## Proposed Solution
- Create a `rate_limits` table to define tokens/intervals.
- Update the `Claim` query to check if a task's `queue_name` or `task_path` has exceeded its allocated limit in the current window.

## Implementation Checklist
- [x] Migration: Create `rate_limits` table (`key`, `tokens_per_second`, `burst_size`).
- [x] Logic: Update `internal/queue/Service.Claim` to incorporate rate limit checks via SQL.
- [x] CLI: Add `reproq limit set <key> <rate>` command.

## References
- Token Bucket algorithm.
- Sidekiq Enterprise Rate Limiting.
