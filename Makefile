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
	go test -v ./internal/executor/...

up:
	docker-compose up -d db
	@echo "Waiting for DB to be ready..."
	@sleep 5

down:
	docker-compose down

migrate:
	@echo "Migrations should be run via your preferred tool or manually."
	@echo "Example: cat migrations/*.up.sql | docker-compose exec -T db psql -U user -d reproq"

test-integration: up
	-export DATABASE_URL="postgres://user:pass@localhost:5432/reproq?sslmode=disable" && \
	cat migrations/*.up.sql | psql "postgres://user:pass@localhost:5432/reproq?sslmode=disable" && \
	go test -v ./internal/queue/...
	$(MAKE) down
