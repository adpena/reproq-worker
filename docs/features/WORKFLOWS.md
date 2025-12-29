# Feature: Task Chaining & Workflows

## Problem Statement
Complex business logic often requires tasks to run in a specific order (Chains) or a callback to run after a set of parallel tasks finishes (Groups/Chords).

## Proposed Solution
- Add a `parent_id` or `depends_on` mechanism.
- Tasks in a chain stay in a `WAITING` state until their parent is `SUCCESSFUL`.

## Implementation Checklist
- [ ] Migration: Add `parent_id` column and `WAITING` status to `task_status` enum.
- [ ] Logic: Update `Claim` to exclude `WAITING` tasks.
- [ ] Logic: Update `CompleteSuccess` to trigger downstream tasks (transition from `WAITING` to `PENDING`).
- [ ] API: Add helper for enqueuing chains.

## References
- Celery Canvas (Chains, Groups, Chords).
