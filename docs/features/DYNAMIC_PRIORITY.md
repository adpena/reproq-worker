# Feature: Dynamic Priority (Planned)

## Status
🚧 **Under Construction** 🚧

Configuration options exist (`PriorityAgingFactor`), but the `Claim` query does not yet implement the aging formula.

## Problem Statement
High-priority tasks can "starve" low-priority ones if the high-priority queue is never empty. Conversely, some tasks should gain priority as they wait longer.

This is a classic fairness problem in schedulers (see Linux CFS, Sidekiq weighted queues, and CPU aging algorithms).

## Proposed Solution
- Implement "Priority Aging" in the SQL query.
- Calculate an "Effective Priority" = `priority + (age_in_seconds / constant)`.

This makes low-priority tasks "bubble up" over time without removing priority control.

## Implementation Plan
- [ ] Logic: Update the `ORDER BY` clause in `internal/queue/Service.Claim`.
- [x] Config: Add `PRIORITY_AGING_FACTOR` to `config.go`.

## Why It Matters
- **Fairness**: Prevents low-priority jobs from never running.
- **SLA protection**: Ensures long-waiting tasks eventually execute.
- **Operational safety**: Keeps "background" queues from being permanently starved by hot paths.

## References
- CPU Scheduling algorithms (Aging).
 - Sidekiq weighted queues & job aging.
