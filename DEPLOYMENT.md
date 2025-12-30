# Reproq Deployment & Release Guide

This document outlines the strategy for building and releasing the Go worker binaries to support the `reproq install` command.

## 1. Release Naming Convention
To ensure the `reproq-django` install command can find the correct binary, assets in GitHub Releases must follow this pattern:
`reproq-{os}-{arch}` (optionally with `.exe` for Windows).

Example filenames:
- `reproq-darwin-arm64`
- `reproq-darwin-amd64`
- `reproq-linux-amd64`
- `reproq-linux-arm64`
- `reproq-windows-amd64.exe`

## 2. Automated Build Script
Use the existing `scripts/build_releases.sh` to automate cross-compilation:

```bash
#!/bin/bash
VERSION=$(git describe --tags)
platforms=("darwin/amd64" "darwin/arm64" "linux/amd64" "linux/arm64" "windows/amd64")

for platform in "${platforms[@]}"
do
    platform_split=(${platform//\// })
    GOOS=${platform_split[0]}
    GOARCH=${platform_split[1]}
    output_name="reproq-$GOOS-$GOARCH"
    if [ $GOOS = "windows" ]; then
        output_name+='.exe'
    fi

    echo "Building $output_name..."
    env GO111MODULE=on GOOS=$GOOS GOARCH=$GOARCH go build -tags prod -o "dist/$output_name" ./cmd/reproq
done
```

## 3. GitHub Actions Integration
The `.github/workflows/release.yml` should be implemented to:
1.  Trigger on a new tag (e.g., `v*`).
2.  Run the build script.
3.  Create a GitHub Release and upload all files from `dist/` as assets.

## 4. Database Migrations
If you manage the schema directly from this repo, apply the SQL migrations in order.

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
Use this checklist when exposing `/metrics` or `/healthz` in production.

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
