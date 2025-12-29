# Feature: Dynamic Priority & Starvation Prevention

## Problem Statement
High-priority tasks can "starve" low-priority ones if the high-priority queue is never empty. Conversely, some tasks should gain priority as they wait longer.

## Proposed Solution
- Implement "Priority Aging" in the SQL query.
- Calculate an "Effective Priority" = `priority + (age_in_seconds / constant)`.

## Implementation Checklist
- [ ] Logic: Update the `ORDER BY` clause in `internal/queue/Service.Claim`.
- [ ] Config: Add `PRIORITY_AGING_FACTOR` to `config.go`.

## References
- CPU Scheduling algorithms (Aging).
