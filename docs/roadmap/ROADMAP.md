# ReproQ Worker Roadmap

## Current baseline

[Worker v0.0.146](https://github.com/adpena/reproq-worker/releases/tag/v0.0.146)
and [Django SDK 0.1.534](https://pypi.org/project/reproq-django/0.1.534/)
are the current release pair. Pin the explicit worker tag and the platform
artifact checksum. GitHub's Latest pointer remains at v0.0.144 for older
SDKs. The worker uses protocol 2 and checks the schema-v2 queue
contract before taking work. Committed Django migrations own that schema.
The Go queue uses `REPROQ_DATABASE_URL`, and Python tasks keep their separate
business `DATABASE_URL`. See [configuration and compatibility](../../README.md).

Worker v0.0.146 includes the expiry preview change from `98ace75`.
`reproq prune expired --dry-run` reports candidates for finalization or
deletion. An actual prune reports deletions.

## Next work

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
