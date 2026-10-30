.PHONY: build release-build container-build test test-integration bench vet lint lint-install check check-toolchain up down help

BENCHTIME ?= 100x
GOLANGCI_LINT_VERSION := v2.15.0
export GOENV := off
export GOWORK := off
export GOTOOLCHAIN := local

# Default target
help:
	@echo "Available commands:"
	@echo "  build            - Build the reproq binary"
	@echo "  release-build    - Build five checksummed candidates (OUT=new directory, optional TAG=existing tag)"
	@echo "  container-build  - Package canonical artifacts locally (ARTIFACTS=directory IMAGE=name, optional PLATFORM=linux/arch)"
	@echo "  test             - Run all module unit tests without database access"
	@echo "  test-integration - Test a prepared loopback reproq_test_* DB via REPROQ_TEST_DATABASE_URL"
	@echo "  bench            - Benchmark owned claim fixtures in that same dedicated test DB"
	@echo "  check            - Unit tests, go vet, and golangci-lint (local-first gate)"
	@echo "  lint-install     - Build the pinned linter with the selected Go patch into .local/bin"
	@echo "  up               - Start docker-compose services"
	@echo "  down             - Stop docker-compose services"

check-toolchain:
	@bash scripts/check_tools.sh

build: check-toolchain
	go build -o reproq ./cmd/reproq

release-build:
	@test -n "$(OUT)" || { echo "Set OUT to a new output directory." >&2; exit 2; }
	@if test -n "$(TAG)"; then scripts/build_releases.sh --tag "$(TAG)" --output "$(OUT)"; else scripts/build_releases.sh --unreleased --output "$(OUT)"; fi

container-build:
	@test -n "$(ARTIFACTS)" -a -n "$(IMAGE)" || { echo "Set ARTIFACTS to the canonical bundle and IMAGE to a local image name." >&2; exit 2; }
	@if test -n "$(PLATFORM)"; then bash scripts/build_container.sh --artifacts "$(ARTIFACTS)" --image "$(IMAGE)" --platform "$(PLATFORM)"; else bash scripts/build_container.sh --artifacts "$(ARTIFACTS)" --image "$(IMAGE)"; fi

test: check-toolchain
	@DATABASE_URL= REPROQ_DATABASE_URL= REPROQ_TEST_DATABASE_URL= REPROQ_REQUIRE_TEST_DATABASE= go test -short -v ./...

vet: check-toolchain
	go vet ./...

lint-install: check-toolchain
	GOBIN="$(CURDIR)/.local/bin" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint: check-toolchain
	@bash scripts/check_tools.sh --linter "$(GOLANGCI_LINT_VERSION)"
	golangci-lint run

check: test vet lint

up:
	docker compose up -d db
	@echo "Waiting for DB to be ready..."
	@for i in {1..10}; do pg_isready -h localhost -p 5432 -U user && break || sleep 1; done

test-integration: check-toolchain
	@test -n "$$REPROQ_TEST_DATABASE_URL" || { echo "Set REPROQ_TEST_DATABASE_URL to a prepared loopback reproq_test_* database." >&2; exit 2; }
	@REPROQ_REQUIRE_TEST_DATABASE=1 go test -count=1 -p=1 -v ./internal/queue ./cmd/reproq

bench: check-toolchain
	@test -n "$$REPROQ_TEST_DATABASE_URL" || { echo "Set REPROQ_TEST_DATABASE_URL to a prepared loopback reproq_test_* database." >&2; exit 2; }
	@REPROQ_REQUIRE_TEST_DATABASE=1 go test -run '^$$' -bench '^BenchmarkClaim$$' -benchmem -benchtime="$(BENCHTIME)" ./internal/queue
