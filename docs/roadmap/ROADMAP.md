# Reproq Worker Roadmap

## Scope and intent
This roadmap converts current gaps and partial implementations into a sequenced delivery plan.
Detailed items live in `docs/roadmap/BACKLOG.md`.

## Guiding goals
- Preserve strict worker <-> Django contract stability.
- Prioritize correctness and operational safety before new surface area.
- Keep CLI and docs in parity with shipped functionality.

## Phase 0: Core correctness and CLI parity (P0)
Goal: remove user-facing footguns and align runtime metadata.

Deliverables
- Fix worker DSN handling so `reproq worker --dsn` works without `DATABASE_URL`. (BL-001)
- Use the worker Version constant when registering workers. (BL-002)
- Add `WAITING` to the TaskStatus enum to reflect workflows. (BL-003)
- Implement queue round-robin and make multi-queue configuration real. (BL-004)
- Add `reproq cancel` CLI and a DB update path for `cancel_requested`. (BL-005)

## Phase 1: Operability and safety (P1)
Goal: improve triage, observability, and security posture.

Deliverables
- Dead letter queue (DLQ) + triage CLI and schema fields. (BL-006)
- Wire health/metrics HTTP server and config. (BL-007)
- Enforce task path allow-list validation. (BL-008)
- Redact sensitive payloads from logs. (BL-009)
- Add replay by spec_hash for deterministic replays. (BL-010)
- Resolve CLI/documentation parity for listed commands. (BL-011)

## Phase 2: Productization and UX (P2)
Goal: make the worker easier to operate at scale and easier to debug.

Deliverables
- Persist stdout/stderr or artifact URIs for executions. (BL-012)
- Implement retention/cleanup using `expires_at`. (BL-013)
- Decide on and implement missing CLI utilities (schedule/verify/loadgen/torture). (BL-014)

## Sequencing notes
- Items that add or change schema require a migration and cross-project model sync.
- Items touching the JSON protocol must be coordinated with `reproq-django`.
- Each phase should include docs updates and CLI help alignment.
