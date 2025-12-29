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
A `scripts/build_releases.sh` should be created to automate cross-compilation:

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
    env GOOS=$GOOS GOARCH=$GOARCH go build -o "dist/$output_name" ./cmd/reproq
done
```

## 3. GitHub Actions Integration
The `.github/workflows/release.yml` should be implemented to:
1.  Trigger on a new tag (e.g., `v*`).
2.  Run the build script.
3.  Create a GitHub Release and upload all files from `dist/` as assets.

## 4. Database Migrations
If you manage the schema directly from this repo, apply the SQL migrations in order.

For legacy installs that used array columns for worker metadata, run:
`migrations/000013_convert_worker_arrays_to_jsonb.up.sql` to convert
`task_runs.worker_ids` and `reproq_workers.queues` to JSONB.

## 5. Production Supervision
The preferred method for managing Reproq in production is using **systemd**.

### Automated Setup (Recommended)
Reproq Django includes a management command that automatically generates optimized service files for your specific project environment:

```bash
python manage.py reproq systemd
```

### Manual Setup
If you prefer to write your own unit files...
