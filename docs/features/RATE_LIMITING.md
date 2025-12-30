# Feature: Rate Limiting (Implemented)

## Status
✅ **Implemented**

## Problem Statement
Users need to prevent overwhelming external APIs or internal resources. A burst of 10,000 tasks should not all attempt to execute simultaneously if the target API only allows 5 requests per second.

## Proposed Solution
- Create a `rate_limits` table to define tokens/intervals.
- Update the `Claim` query to check if a task's `queue_name` or `task_path` has exceeded its allocated limit in the current window using a Token Bucket algorithm.

Typical designs mirror Sidekiq Enterprise and Celery's per-task rate limits: a shared token bucket per key, with "burst" allowance for short spikes.

## Implementation Plan
- [x] Migration: Create `rate_limits` table (`key`, `tokens_per_second`, `burst_size`).
- [x] Logic: Update `internal/queue/Service.Claim` to incorporate rate limit checks via SQL.
- [x] CLI: Add `reproq limit` commands.

## Keys
- `queue:<queue_name>`: Limit a single queue.
- `task:<task_path>`: Limit a specific task (overrides queue/global).
- `global`: Fallback if no queue/task-specific limit exists.

## CLI Examples
```
reproq limit set --key queue:default --rate 5 --burst 10
reproq limit set --key task:myapp.tasks.sync --rate 1 --burst 1
reproq limit ls
reproq limit rm --key queue:default
```

## Default Behavior
By default, global rate limiting is disabled. To enable throttling, set a positive `tokens_per_second` for `global`, a queue, or a task.
Use `--rate 0` or delete the row to disable a limit.

## Why It Matters
- **API protection**: Prevents thundering herds against rate-limited providers.
- **Cost control**: Avoids surges that trigger billing spikes.
- **Stability**: Keeps internal services from being overwhelmed by batch spikes.

## References
- Token Bucket algorithm.
- Sidekiq Enterprise Rate Limiting.
 - Celery task rate limits.
