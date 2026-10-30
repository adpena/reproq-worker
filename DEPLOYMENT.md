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

The image retains a shell and public CA bundle. As with the old
image, it contains no Django project or Python environment: install the canonical
worker in your project's own runtime for real Python task execution.

For the existing local mock worker and database:

```bash
REPROQ_CONTAINER_IMAGE=reproq-worker:local docker compose up -d worker
```

Compose uses a local image and refuses registry pulls. `make up` still starts only
the local database. Apply committed ReproQ Django migrations before starting a
worker; these commands do not run production migrations. The obsolete `loadgen`
service was removed because its `/app/loadgen` executable never existed.
Use SDK enqueue APIs to generate canonical workloads and `make bench` for owned
Go claim benchmarks; the retired synthetic Go producer bypassed that authority.

## 4. Database Migrations
ReproQ Django's committed migrations are the sole schema authority. From the
configured Django project, migrate the selected queue database:

```bash
uv run python manage.py migrate reproq_django --database <queue-alias>
```

Existing databases created by the retired Go SQL or partial historical Django
migrations require [guarded schema adoption](https://github.com/adpena/reproq-django#6-run-migrations)
before ordinary forward migrations. Drain old workers before activating fencing.
Drain queued legacy `spec_version=0` work with the old runtime before cutover,
or perform an explicitly audited reconciliation. The new worker refuses a
selected legacy task without spending an attempt; replay/retry also refuse it.
This is a deployment gate and can block a queue until an operator resolves it.
Historical terminal rows remain readable with their original bytes and hashes.
The worker performs no DDL and refuses a missing or incompatible schema contract,
including temporary or shadow relations that could retarget queue operations.

## 5. Production Supervision
The preferred method for managing Reproq in production is using **systemd**.

### Automated Setup (Recommended)
Reproq Django includes a management command that automatically generates optimized service files for your specific project environment:

```bash
uv run python manage.py reproq systemd
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

## Unreleased executor interpreter binding

Deploy matching reviewed Go and Django SDK artifacts for the unreleased
[`--python-bin` contract](README.md#unreleased-executor-python-selection). The
Django SDK binds the UV-selected interpreter by default while preserving explicit
file/CLI choices, and verifies `worker_python_bin:1` before startup. Portable
configuration can omit `worker.python_bin`; no machine path is written by init.
Keep one outer `uv run` environment for Django, web and Python task executors.
