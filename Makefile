.PHONY: build test test-integration up down migrate help

# Default target
help:
	@echo "Available commands:"
	@echo "  build            - Build the reproq binary"
	@echo "  test             - Run unit tests"
	@echo "  test-integration - Start DB and run integration tests"
	@echo "  up               - Start docker-compose services"
	@echo "  down             - Stop docker-compose services"
	@echo "  migrate          - Run database migrations"

build:
	go build -o reproq ./cmd/reproq

test:
	go test -v ./internal/...

up:
	docker-compose up -d db
	@echo "Waiting for DB to be ready..."
	@for i in {1..10}; do pg_isready -h localhost -p 5432 -U user && break || sleep 1; done

test-integration: up
	-export DATABASE_URL="postgres://user:pass@localhost:5432/reproq?sslmode=disable" && \
	cat migrations/*.up.sql | psql "postgres://user:pass@localhost:5432/reproq?sslmode=disable" && \
	go test -v ./internal/queue
	$(MAKE) down
