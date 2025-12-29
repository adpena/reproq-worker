PROJECT: Deterministic Go Worker + Scalable Queue Backend for Django 6.0 Tasks

ROLE:
You are an expert Go systems engineer building a production-grade background job runner.
This system will act as an execution backend for Django 6.0's new Tasks API.
It must combine:
- a Celery/RQ-like distributed queue
- deterministic / replayable execution semantics
- strong observability and operational safety

HIGH-LEVEL GOAL:
Build a Go service that:
1. Polls a Postgres-backed queue of task runs
2. Claims tasks using SKIP LOCKED semantics
3. Executes Django task callables via a Python executor
4. Records results, errors, provenance, and artifacts
5. Supports retries, leases, priorities, and scheduling (run_after)
6. Enables replay by immutable RunSpec hash

THIS REPO CONTAINS:
- The Go worker service only
- No Django or task-definition code
- No Python task logic beyond invoking an executor command

NON-GOALS:
- Do NOT implement Django-side enqueue logic
- Do NOT design a web UI
- Do NOT require Kafka, Celery, or Airflow

CORE CONCEPTS:
- RunSpec: immutable JSON definition of a task run
- spec_hash: sha256 hash of canonical RunSpec JSON
- task_runs table: queue + result storage
- Deterministic execution wrapper (container or process-based)

REQUIRED FEATURES:

1. DATABASE + QUEUE
- Use Postgres as the sole queue backend
- Use SELECT ... FOR UPDATE SKIP LOCKED
- Respect:
  - queue_name
  - priority (higher runs first)
  - run_after (scheduled execution)
- Implement leases with heartbeats (leased_until)

2. TASK CLAIMING
- Atomic claim + state transition READY -> RUNNING
- Record:
  - worker_id
  - started_at / last_attempted_at
- Prevent double execution

3. EXECUTION
- Execute tasks via an external Python process:
  python -m myproject.task_executor --task-path ... --payload-json ...
- Capture:
  - stdout/stderr
  - JSON result envelope
  - exit code
- Support timeout enforcement

4. RESULT HANDLING
- On success:
  - status = SUCCESSFUL
  - store return_json
- On failure:
  - status = FAILED
  - append structured error to errors_json
- Track attempts count
- Track worker_ids list

5. RETRIES
- Max attempts configurable per RunSpec
- Failed tasks re-queued until attempts exhausted

6. DETERMINISM & REPLAY
- Compute spec_hash from canonical JSON
- Store spec_json verbatim
- Provide a CLI or function to re-enqueue a run from spec_hash or result_id

7. OBSERVABILITY
- Structured logging
- Worker identity
- Clear error reporting
- Log execution lifecycle events

TECHNICAL CONSTRAINTS:
- Go 1.22+
- pgx or database/sql
- No external queue systems
- Clean package structure
- Production-grade error handling

DELIVERABLES:
- Go module with:
  - worker loop
  - DB access layer
  - executor abstraction
  - retry + lease logic
- SQL schema migrations
- README explaining:
  - architecture
  - execution flow
  - how it integrates with Django Tasks
  - how determinism/replay works

QUALITY BAR:
This should feel like a serious alternative to Celery/RQ:
- boring, predictable, debuggable
- safe under concurrency
- easy to reason about in production

Assume this will be deployed in Kubernetes or systemd environments.
