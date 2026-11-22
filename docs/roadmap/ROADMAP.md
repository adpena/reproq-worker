# ReproQ Worker Roadmap

## Current baseline

[Worker v0.0.149](https://github.com/adpena/reproq-worker/releases/tag/v0.0.149)
and [Django SDK 0.1.536](https://pypi.org/project/reproq-django/0.1.536/)
are qualified together in the TEA Data app. SDK 0.1.536 defaults to v0.0.148.
Use the explicit v0.0.149 tag and the platform artifact checksum for that
pair. The SDK's installer metadata owns its default Worker pin.
The worker uses protocol 2 and checks the schema-v2 queue
contract before taking work. Committed Django migrations own that schema.
The Go queue uses `REPROQ_DATABASE_URL`, and Python tasks keep their separate
business `DATABASE_URL`. See [configuration and compatibility](../../README.md).

Worker v0.0.146 includes the expiry preview change from `98ace75`.
`reproq prune expired --dry-run` reports candidates for finalization or
deletion. An actual prune reports deletions.

## Available capabilities

Worker 0.0.147 introduced scoped automatic lease recovery, a read-only eligibility
probe for idle queues, and on-demand PostgreSQL connections in low memory mode.
Worker 0.0.148 and SDK 0.1.536 add optional
[notification-assisted waiting](../notification-waiting.md). Notifications retain
bounded recovery polling and require a dedicated PostgreSQL session; enabling
them is not automatically a memory or query-rate reduction. Consumer activation
and resource qualification remain separate from package availability.

Worker 0.0.149 checks live shared-key capacity in both the read-only idle probe
and Claim. A backlog blocked by a committed lease can
skip the empty claim transaction. Claim still acquires advisory locks and
rechecks capacity before taking work. The polling bounds cover completion,
expiry and missed notifications.

## Next work

Compare bounded polling and notification-assisted waiting on otherwise
identical workloads. Record queue delay, empty claims, database connections,
idle memory and recovery after missed notifications. Include overlapping
consumers and shared task keys before assigning a performance benefit.

1. For the next candidate, run `make check` with the Go patch in
   `go.mod` and the pinned linter. Run `make test-integration` against an owned
   loopback PostgreSQL test database prepared by the matching Django migrations.
   Keep the expiry preview and retained-callback regression checks in the gate.
2. Rehearse the candidate worker and SDK together. Record their exact source
   or artifact identities, protocol and schema checks, selected Python
   interpreter, and an end-to-end task receipt on the target runtime. Exercise
   the queue and business DSNs separately. [Executor contract](../../README.md#executor-python-selection)
3. When ready to release, build and verify the checksummed assets from a
   reviewed tag. Review the draft and its runtime evidence before publication.
   [Release procedure](../../DEPLOYMENT.md#3-manual-release-workflow)
4. After publication, update downstream pins and rehearse deployment and
   rollback with the matching worker and SDK. Confirm the active worker
   identity and a canary task before expanding the rollout.

## Completed work

The original phases for correctness, operability, security, and CLI coverage
are recorded in the [historical backlog](BACKLOG.md). It contains the completion
dates and acceptance criteria for BL-001 through BL-014.
