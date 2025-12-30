# Feature: Rate Limiting (Planned)

## Status
🚧 **Under Construction** 🚧

The database schema (`rate_limits` table) exists, but the enforcement logic in the worker is currently unimplemented.

## Problem Statement
Users need to prevent overwhelming external APIs or internal resources. A burst of 10,000 tasks should not all attempt to execute simultaneously if the target API only allows 5 requests per second.

## Proposed Solution
- Create a `rate_limits` table to define tokens/intervals.
- Update the `Claim` query to check if a task's `queue_name` or `task_path` has exceeded its allocated limit in the current window using a Token Bucket algorithm.

Typical designs mirror Sidekiq Enterprise and Celery's per-task rate limits: a shared token bucket per key, with "burst" allowance for short spikes.

## Implementation Plan
- [x] Migration: Create `rate_limits` table (`key`, `tokens_per_second`, `burst_size`).
- [ ] Logic: Update `internal/queue/Service.Claim` to incorporate rate limit checks via SQL.
- [ ] CLI: Add `reproq limit set <key> <rate>` command.

## Why It Matters
- **API protection**: Prevents thundering herds against rate-limited providers.
- **Cost control**: Avoids surges that trigger billing spikes.
- **Stability**: Keeps internal services from being overwhelmed by batch spikes.

## References
- Token Bucket algorithm.
- Sidekiq Enterprise Rate Limiting.
 - Celery task rate limits.
