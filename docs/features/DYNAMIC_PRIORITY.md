# Feature: Dynamic Priority (Implemented)

## Status
✅ **Implemented**

## Problem Statement
High-priority tasks can "starve" low-priority ones if the high-priority queue is never empty. Conversely, some tasks should gain priority as they wait longer.

This is a classic fairness problem in schedulers (see Linux CFS, Sidekiq weighted queues, and CPU aging algorithms).

## Proposed Solution
- Implement "Priority Aging" in the SQL query.
- Calculate an "Effective Priority" = `priority + (age_in_seconds / constant)`.

This makes low-priority tasks "bubble up" over time without removing priority control.

## Implementation Plan
- [x] Logic: Update the `ORDER BY` clause in `internal/queue/Service.Claim`.
- [x] Config: Add `PRIORITY_AGING_FACTOR` to `config.go`.

## Configuration
Set `PRIORITY_AGING_FACTOR` (seconds per priority point) or pass `--priority-aging-factor`. Smaller values age faster.

Example:
```
PRIORITY_AGING_FACTOR=60
```

Effective priority is computed as:
```
priority + (age_seconds / PRIORITY_AGING_FACTOR)
```

## Why It Matters
- **Fairness**: Prevents low-priority jobs from never running.
- **SLA protection**: Ensures long-waiting tasks eventually execute.
- **Operational safety**: Keeps "background" queues from being permanently starved by hot paths.

## References
- CPU Scheduling algorithms (Aging).
 - Sidekiq weighted queues & job aging.
