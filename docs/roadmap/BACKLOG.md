# Reproq Worker Backlog

## Priority definitions
P0: correctness or contract issues, CLI breaks, or partial implementations that mislead users.
P1: operational safety, observability, and security posture improvements.
P2: productization, UX, and longer-term features.

## Progress log
- 2025-12-29: Completed BL-001 (worker DSN handling in `reproq worker`).
- 2025-12-29: Completed BL-002 (worker version registration).
- 2025-12-29: Completed BL-003 (add WAITING to TaskStatus enum).
- 2025-12-29: Completed BL-004 (multi-queue config + round-robin polling).
- 2025-12-29: Completed BL-005 (cancel CLI + cancellation update path).
- 2025-12-29: Completed BL-006 (DLQ + triage tooling).
- 2025-12-29: Completed BL-007 (health/metrics server wiring).
- 2025-12-29: Completed BL-008 (task module allow-list validation).
- 2025-12-29: Completed BL-009 (log redaction + health endpoint hardening).
- 2025-12-29: Completed BL-009a (metrics auth + binding control).
- 2025-12-29: Observability sweep (claim/execution/queue wait metrics wired).
- 2025-12-29: Security sweep (metrics auth logging, inline payload warning, security review doc).
- 2025-12-29: Security sweep (unauthorized rate limiting, prod inline payload block, security checklist).
- 2025-12-29: Security sweep (metrics auth rate limit config + runbook guidance).
- 2025-12-29: Security sweep (metrics/health CIDR allow-list).
- 2025-12-30: Completed BL-010 (replay by spec_hash).
- 2025-12-30: Completed BL-011 (CLI/docs parity audit).
- 2025-12-30: Completed BL-012 (persist stdout/stderr via logs_uri).
- 2025-12-30: Completed BL-013 (expires_at pruning command).
- 2025-12-30: Completed BL-014 (utility command decision: keep torture separate).
- 2025-12-30: Added YAML/TOML config file support and templates for worker/beat.
- 2025-12-30: Completed security review follow-ups (metrics TLS/mTLS support, egress policy guidance, checklist update).

## Backlog items

### BL-001: Fix worker DSN handling in `reproq worker`
| Field | Value |
| --- | --- |
| Priority | P0 |
| Type | Bug |
| Impact | Worker cannot start with `--dsn` unless `DATABASE_URL` is set |
| Status | Completed (2025-12-29) |
| Touchpoints | `cmd/reproq/main.go`, `internal/config/config.go` |

Problem
`config.Load()` hard-fails when `DATABASE_URL` is missing, but `reproq worker --dsn` parses flags after config load.

Proposed solution
Allow the worker to parse `--dsn` before requiring the environment variable, or relax `config.Load()` to accept empty `DatabaseURL` that can be overridden by flags.

Acceptance criteria
- `reproq worker --dsn <dsn>` starts successfully with no `DATABASE_URL` env var.
- `DATABASE_URL` still works as a default when `--dsn` is omitted.
- Invalid DSN still fails fast with a clear error.

### BL-002: Use the Version constant for worker registration
| Field | Value |
| --- | --- |
| Priority | P0 |
| Type | Bug |
| Impact | Worker registration shows stale version data |
| Status | Completed (2025-12-29) |
| Touchpoints | `cmd/reproq/main.go`, `internal/queue/queue.go` |

Problem
`RegisterWorker` uses a hard-coded version string.

Proposed solution
Pass `Version` from `cmd/reproq/main.go` into `RegisterWorker`.

Acceptance criteria
- `reproq --version` and `reproq_workers.version` report the same value.

### BL-003: Add `WAITING` to TaskStatus
| Field | Value |
| --- | --- |
| Priority | P0 |
| Type | Correctness |
| Impact | Enum does not reflect workflow usage |
| Status | Completed (2025-12-29) |
| Touchpoints | `internal/queue/models.go` |

Problem
Workflows set `status = 'WAITING'`, but the enum omits the value.

Proposed solution
Add `StatusWaiting TaskStatus = "WAITING"` to the enum.

Acceptance criteria
- Enum includes `WAITING`.
- Any Django-side model sync is updated if required.

### BL-004: Implement queue round-robin and real multi-queue config
| Field | Value |
| --- | --- |
| Priority | P0 |
| Type | Feature |
| Impact | Multi-queue config exists but is ignored |
| Status | Completed (2025-12-29) |
| Touchpoints | `internal/config/config.go`, `internal/runner/runner.go` |

Problem
`QueueNames` exists but only the first queue is polled, and there is no flag/env to set it.

Proposed solution
Add a queue list flag/env (for example `--queues a,b,c`) and implement round-robin or fair polling in the runner.

Acceptance criteria
- Users can configure multiple queues.
- The runner polls queues in a fair round-robin order.
- Unit or integration coverage for multi-queue claim behavior.

### BL-005: Add `reproq cancel` CLI and DB update path
| Field | Value |
| --- | --- |
| Priority | P0 |
| Type | Feature |
| Impact | Cancellation feature is incomplete without a control path |
| Status | Completed (2025-12-29) |
| Touchpoints | `cmd/reproq/main.go`, `internal/queue/queue.go`, `docs/features/CANCELLATION.md` |

Problem
`cancel_requested` is implemented, but there is no CLI or API to set it.

Proposed solution
Add a queue method to mark `cancel_requested = TRUE` for a RUNNING task, and expose it via `reproq cancel --id <result_id>`.

Acceptance criteria
- `reproq cancel --id` sets the flag for running tasks.
- Worker detects the flag and cancels execution (existing behavior).
- CLI returns a clear status when a task is not running or does not exist.

### BL-006: Dead letter queue (DLQ) and triage CLI
| Field | Value |
| --- | --- |
| Priority | P1 |
| Type | Feature |
| Impact | Operators cannot inspect and retry failures at scale |
| Status | Completed (2025-12-29) |
| Touchpoints | `migrations/*.sql`, `cmd/reproq/main.go`, `internal/queue/queue.go`, `docs/features/DEAD_LETTER_QUEUE.md` |

Problem
There is no DLQ flow or triage tooling, despite the design doc.

Proposed solution
Add `last_error` and `failed_at` columns, implement triage CLI commands (list/inspect/retry), and ensure retries reset attempts appropriately.

Acceptance criteria
- Migration adds `last_error` and `failed_at`.
- `reproq triage list` shows recent failures with summary data.
- `reproq triage inspect <id>` shows full error payload.
- `reproq triage retry <id|--all>` re-enqueues correctly.
- Django models are updated if schema changes are shared.

### BL-007: Wire health and metrics HTTP server
| Field | Value |
| --- | --- |
| Priority | P1 |
| Type | Feature |
| Impact | Health/metrics endpoint is present but unused |
| Status | Completed (2025-12-29) |
| Touchpoints | `internal/web/server.go`, `cmd/reproq/main.go`, `internal/config/config.go` |

Problem
A health/metrics server exists but is not started by the worker.

Proposed solution
Start `internal/web` in `runWorker`, or merge it with the current metrics port handling using a single address.

Acceptance criteria
- `/healthz` returns 200 when DB is reachable.
- `/metrics` exposes Prometheus metrics.
- Config flag/env can control the address or disable the server.

### BL-008: Enforce task path allow-list validation
| Field | Value |
| --- | --- |
| Priority | P1 |
| Type | Security |
| Impact | Allow-list validator exists but is never applied |
| Status | Completed (2025-12-29) |
| Touchpoints | `internal/executor/validator.go`, `internal/runner/runner.go`, `internal/config/config.go` |

Problem
The validator is not wired into the execution path.

Proposed solution
Parse `task_path` from spec JSON and validate before execution, with a configurable allow-list.

Acceptance criteria
- Invalid task path fails fast with a clear error.
- Allow-list can be configured via flags/env.
- Failures are reported in `errors_json` with a security-specific kind.

### BL-009: Redact sensitive payloads from logs
| Field | Value |
| --- | --- |
| Priority | P1 |
| Type | Observability |
| Impact | Security posture requires payload redaction in logs |
| Status | Completed (2025-12-29) |
| Touchpoints | `internal/runner/runner.go`, `internal/logging/*` |

Problem
Log hygiene is specified but redaction is not implemented.

Proposed solution
Implement a redaction strategy for any log fields containing payload JSON or secrets.

Acceptance criteria
- Sensitive fields are redacted consistently.
- Redaction is tested or documented.

### BL-010: Replay by spec_hash
| Field | Value |
| --- | --- |
| Priority | P1 |
| Type | Feature |
| Impact | Deterministic replay is only possible by result_id |
| Touchpoints | `cmd/reproq/main.go`, `internal/queue/queue.go` |
| Status | Completed (2025-12-30) |

Problem
Replay is only supported by result ID, not by spec hash.

Proposed solution
Add a replay path that takes a spec_hash and re-enqueues the most recent matching run.

Acceptance criteria
- `reproq replay --spec-hash <hash>` re-enqueues a task.
- Clear errors for missing hashes or ambiguous matches.

### BL-011: CLI and documentation parity audit
| Field | Value |
| --- | --- |
| Priority | P1 |
| Type | Doc/UX |
| Impact | Docs and project notes list commands that do not exist |
| Touchpoints | `README.md`, `docs/index.html`, `GEMINI.md`, `cmd/reproq/main.go` |
| Status | Completed (2025-12-30) |

Problem
Docs and notes reference commands such as `triage`, `schedule`, `verify`, `loadgen`, and `cancel` that are not in the CLI.

Proposed solution
Audit references and either implement the missing commands or remove them from docs and marketing pages.

Acceptance criteria
- CLI help, README, and docs are aligned with shipped commands.
- Any removed commands are documented as future work in the roadmap.

Outcome
- CLI/docs parity updated; missing utility commands remain out of scope and are tracked as a closed decision.

### BL-012: Persist stdout/stderr or artifact URIs
| Field | Value |
| --- | --- |
| Priority | P2 |
| Type | Feature |
| Impact | Debugging is harder without task output capture |
| Touchpoints | `internal/executor/executor.go`, `internal/runner/runner.go`, `internal/queue/queue.go` |
| Status | Completed (2025-12-30) |

Problem
Executor output is discarded, and `logs_uri`/`artifacts_uri` are never set.

Proposed solution
Store stdout/stderr in a durable location or capture references in the DB.

Acceptance criteria
- Task output is accessible after completion.
- Output size limits are enforced.

### BL-013: Retention and cleanup using `expires_at`
| Field | Value |
| --- | --- |
| Priority | P2 |
| Type | Feature |
| Impact | DB grows unbounded without task cleanup |
| Touchpoints | `internal/queue/queue.go`, new migration or worker job |
| Status | Completed (2025-12-30) |

Problem
`expires_at` exists but is unused.

Proposed solution
Add a cleanup job that deletes or archives expired task runs.

Acceptance criteria
- Expired tasks are removed or archived safely.
- Behavior is documented and configurable.

### BL-014: Decide on remaining CLI utilities (schedule/verify/loadgen/torture)
| Field | Value |
| --- | --- |
| Priority | P2 |
| Type | Feature/UX |
| Impact | Project notes mention utilities that are not in the main binary |
| Touchpoints | `cmd/reproq/main.go`, `cmd/torture/main.go`, `GEMINI.md` |
| Status | Completed (2025-12-30) |

Problem
Utility commands are referenced but not consistently shipped.

Proposed solution
Either add these commands to the primary CLI, or remove the references and keep them as separate tools.

Acceptance criteria
- Clear decision and consistent packaging.
- Docs and release artifacts match the decision.

Decision
- Keep `cmd/torture` as a separate binary; do not add `schedule`, `verify`, or `loadgen` to the main CLI.
