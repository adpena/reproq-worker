# Reproq Worker Agent Guidelines

## Main Authority and Task Closeout

- `origin/main` is the sole integration authority. Inspect live remote main, existing branches/worktrees, dirty changes and ownership before work. Prefer one checkout with disjoint agent file ownership. Temporary isolation requires an explicit owner/base/purpose; do not create recovery checkouts or chains of dependent review branches.
- Promptly land coherent changes after independent adversarial review and applicable checks. Merge or fast-forward to `main`, push and verify the live remote commit. Release publication and consumer deployment remain separate gates.
- Before ending a normal task turn, leave one checkout on `main`, local `HEAD` and `main` equal to live `origin/main`, a clean working tree and no remaining task branches locally or remotely. Close completed PRs and remove superseded branches/worktrees during the task.
- Reconcile unique commits and dirty/untracked work before removal. Preserve required source data, provenance, qualification receipts and deliverables in canonical locations and verify custody. Never discard unfinished/other-owned work or published tags/releases/assets to force a clean result.
- Run TEA Data's canonical read-only `scripts/check_workspace_closeout.py` across task repositories and workspace clones. Standalone clones without that tool must perform equivalent Git inventory, cleanliness, branch and live-remote checks. Audit failures remain unresolved work; the auditor does not merge/delete or certify tests. Report exact retained state and authorization/authentication/ownership blockers when safe closure cannot proceed.
- Keep this policy synchronized across the four TEA Data/ReproQ repositories, without competing cleanup scripts or destructive automatic hooks.

## Landing Review Gate

Use the [Elephant & Goldfish Model](https://github.com/google/elephant-goldfish-model) for every planned change before implementation. Scale the design to the change. Give one fresh Goldfish the draft, applicable standards, and the context needed to challenge it. Record the decisions and use the acceptance criteria to choose checks. Run reviewers one at a time.

Require independent adversarial review of the exact landing diff. After each repair that affects correctness, data integrity, security, or operations, review the repair and adjacent paths that could repeat the failure. Repeat until the exact diff has no open actionable findings. Reuse checks whose inputs still match. A separate broader audit may look for independent failure modes beyond the changed paths.

As an agent working on `reproq-worker`, you must adhere to the following standards:

## 1. Observability First
- Every new feature should include relevant Prometheus metrics.
- Latency and throughput of core loops (polling, execution) must be tracked.
- System telemetry (memory usage, goroutine counts) should be exposed for the TUI.

## 2. Reliability & Safety
- **Fencing**: Heartbeat and completion must verify worker ownership, the per-claim UUID token, `RUNNING` status and an unexpired database-clock lease. Clear the token on every exit from `RUNNING`; cancellation wins a late success.
- **Schema authority**: Committed Django migrations own all queue DDL and the periodic enqueue primitive. Do not add Go schema creation, migrations or periodic spec serialization. Validate schema/protocol compatibility before queue work.
- **Database roles**: Use `REPROQ_DATABASE_URL` for the Go queue. Preserve the executor's business `DATABASE_URL`; never replace it with the queue DSN.
- **Graceful Shutdown**: Ensure all tasks have time to finish or checkpoint during worker shutdown.
- **Security**: Validate all task paths against the allowed modules list.

## 3. Performance
- Use atomic database operations (CTEs) to minimize round-trips.
- Monitor and optimize `SKIP LOCKED` query performance.
- Ensure the event broker doesn't become a bottleneck under high task volume.

## 4. Testing & Verification
- Run `make test` and `make test-integration` for every change.
- Verify that metrics are correctly incremented in relevant tests.
- Use `go vet` and `golangci-lint`.
