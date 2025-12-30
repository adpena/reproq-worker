# Feature: Workflows (Chains & Groups)

## Status
✅ **Implemented**

Chains, groups, and chords are supported via `reproq-django` helpers and native worker dependency resolution.

## Overview
Complex business logic often requires tasks to run in a specific order (Chains) or a callback to run after a set of parallel tasks finishes (Groups/Chords).

## How it Works
1. **Django Side**: The `chain()` helper creates a sequence of `TaskRun` records.
   - The first task is `READY`.
   - Subsequent tasks are `WAITING` and have `parent_id` set to the previous task's `result_id`.
   - `wait_count` is set to 1.
2. **Chord Callbacks**:
   - `chord()` creates a group of parallel tasks plus a callback task.
   - Group tasks are `READY` with a shared `workflow_id`.
   - The callback task is `WAITING` with `wait_count = len(group)`.
3. **Worker Side**:
   - When a task completes successfully (`CompleteSuccess`), the worker executes a SQL update to find any dependents (`WHERE parent_id = ?`).
   - It decrements their `wait_count`.
   - If `wait_count` reaches 0, the dependent task transitions from `WAITING` to `READY`.
   - For chords, the worker also decrements the callback's `wait_count` based on `workflow_id`.

## Usage
See `reproq-django` documentation for Python API examples.

```python
from reproq_django.workflows import chain
chain(task_a, task_b).enqueue()
```
