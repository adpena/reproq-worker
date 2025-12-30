#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required. Install Go and re-run this script."
  exit 1
fi

export GO111MODULE=on
export GOCACHE="${GOCACHE:-$root/.gocache}"

echo "Downloading Go modules..."
go mod download

echo "Running unit tests..."
go test ./internal/...

echo "Building reproq worker..."
go build -o reproq ./cmd/reproq

echo "Done. Binary available at ./reproq"
