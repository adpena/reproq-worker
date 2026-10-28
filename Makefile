.PHONY: build release-build test test-integration bench vet lint check up down help

BENCHTIME ?= 100x

# Default target
help:
	@echo "Available commands:"
	@echo "  build            - Build the reproq binary"
	@echo "  release-build    - Build five checksummed candidates (OUT=new directory, optional TAG=existing tag)"
	@echo "  test             - Run all module unit tests without database access"
	@echo "  test-integration - Test a prepared loopback reproq_test_* DB via REPROQ_TEST_DATABASE_URL"
	@echo "  bench            - Benchmark owned claim fixtures in that same dedicated test DB"
	@echo "  check            - Unit tests, go vet, and golangci-lint (local-first gate)"
	@echo "  up               - Start docker-compose services"
	@echo "  down             - Stop docker-compose services"

build:
	go build -o reproq ./cmd/reproq

release-build:
	@test -n "$(OUT)" || { echo "Set OUT to a new output directory." >&2; exit 2; }
	@if test -n "$(TAG)"; then scripts/build_releases.sh --tag "$(TAG)" --output "$(OUT)"; else scripts/build_releases.sh --unreleased --output "$(OUT)"; fi

test:
	@DATABASE_URL= REPROQ_TEST_DATABASE_URL= REPROQ_REQUIRE_TEST_DATABASE= go test -short -v ./...

vet:
	go vet ./...

lint:
	golangci-lint run

check: test vet lint

up:
	docker-compose up -d db
	@echo "Waiting for DB to be ready..."
	@for i in {1..10}; do pg_isready -h localhost -p 5432 -U user && break || sleep 1; done

test-integration:
	@test -n "$$REPROQ_TEST_DATABASE_URL" || { echo "Set REPROQ_TEST_DATABASE_URL to a prepared loopback reproq_test_* database." >&2; exit 2; }
	@REPROQ_REQUIRE_TEST_DATABASE=1 go test -count=1 -v ./internal/queue

bench:
	@test -n "$$REPROQ_TEST_DATABASE_URL" || { echo "Set REPROQ_TEST_DATABASE_URL to a prepared loopback reproq_test_* database." >&2; exit 2; }
	@REPROQ_REQUIRE_TEST_DATABASE=1 go test -run '^$$' -bench '^BenchmarkClaim$$' -benchmem -benchtime="$(BENCHTIME)" ./internal/queue
