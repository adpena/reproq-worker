# Reproq Deployment & Release Guide

This document outlines the strategy for building and releasing the Go worker binaries to support the `reproq install` command.

## 1. Release Naming Convention
To ensure the `reproq-django` install command can find the correct binary, assets in GitHub Releases must follow this pattern:
`reproq-{os}-{arch}` (optionally with `.exe` for Windows).

The canonical builder records its supported platforms and exact asset names in
the output manifest and checksum set.

## 2. Canonical artifacts

Follow the [README release instructions](README.md#-installation).
`scripts/build_releases.sh` is the sole build-policy and platform-list authority;
`go.mod` selects the exact compiler patch and the CLI's `Version` constant selects
the worker version. Use `make build` only for a local development binary.

## 3. Manual release workflow

The existing `.github/workflows/release.yml` runs only by manual dispatch. It
requires a separately reviewed existing tag matching the source and creates an
unpublished draft from canonical artifacts. It refuses an existing release.
Review local checks, artifacts, checksums, and runtime evidence before a separate
publication decision. It does not publish automatically when a tag is pushed.

## Runtime container

After building a canonical bundle from the clean committed source:

```bash
make container-build ARTIFACTS=dist/candidate IMAGE=reproq-worker:local
# Optional explicit Docker target; the manifest determines supported platforms:
make container-build ARTIFACTS=dist/candidate IMAGE=reproq-worker:local PLATFORM=linux/arm64
```

The wrapper verifies the manifest, checksum set, binary build metadata, and exact
source association before sending an owned minimal context to Docker. Docker's
`TARGETOS`/`TARGETARCH` choose an artifact listed in that manifest. The runtime-only
Dockerfile verifies copied bytes again and includes their build manifest. The
official Alpine runtime is pinned by digest; updating it requires registry and
CA-bundle verification. These commands load an image locally and never push it.

The image retains a shell, public CA bundle, and SQL migrations. As with the old
image, it contains no Django project or Python environment: install the canonical
worker in your project's own runtime for real Python task execution.

For the existing local mock worker and database:

```bash
REPROQ_CONTAINER_IMAGE=reproq-worker:local docker compose up -d worker
```

Compose uses a local image and refuses registry pulls. `make up` still starts only
the local database. Apply the baseline schema deliberately before starting a
worker; these commands do not run production migrations. The obsolete `loadgen`
service was removed because its `/app/loadgen` executable never existed. The
separate development tool is `cmd/torture` (see [GEMINI.md](GEMINI.md#torture-tool-separate-binary)).

## 4. Database Migrations
If you manage the schema directly from this repo, apply the baseline SQL migration:

```bash
psql "$DATABASE_URL" -f migrations/000001_baseline.up.sql
```

Notes:
- The baseline uses `CREATE INDEX CONCURRENTLY`, so run it outside a transaction wrapper.
- The `task_runs_task_path_not_empty` check is created as `NOT VALID` to avoid long table locks.
  Validate it after backfilling `task_path` with:
  `ALTER TABLE task_runs VALIDATE CONSTRAINT task_runs_task_path_not_empty;`
- For existing data, backfill in batches (for example using `result_id` windows) before validating.

## 5. Production Supervision
The preferred method for managing Reproq in production is using **systemd**.

### Automated Setup (Recommended)
Reproq Django includes a management command that automatically generates optimized service files for your specific project environment:

```bash
python manage.py reproq systemd
```

### Manual Setup
If you prefer to write your own unit files...

## 6. Metrics/Health Hardening Runbook
Use this checklist when exposing `/metrics`, `/healthz`, or `/events` in production.

1. Bind to localhost or a private interface:
   - `--metrics-addr 127.0.0.1:9090`
2. Require bearer auth on the endpoint:
   - `METRICS_AUTH_TOKEN=...` or `--metrics-auth-token ...`
3. Restrict access by IP or CIDR when possible:
   - `METRICS_ALLOW_CIDRS=127.0.0.1/32,10.0.0.0/8`
4. Enable TLS (and optionally mTLS):
   - `--metrics-tls-cert /path/cert.pem --metrics-tls-key /path/key.pem`
   - Add `--metrics-tls-client-ca /path/ca.pem` to require client certificates.
5. Keep unauthorized rate limiting enabled:
   - Defaults are `--metrics-auth-limit 30` and `--metrics-auth-window 1m`.
6. If public access is required, front with a reverse proxy:
   - Enforce TLS and IP allow-lists at the proxy/ingress.
7. Validate the endpoint from the intended network only:
   - `curl -H "Authorization: Bearer <token>" http://127.0.0.1:9090/healthz`
8. If you must conserve memory, set `LOW_MEMORY_MODE=1` to disable the metrics/health/events server entirely.
