# Reproq Worker Agent Guidelines

As an agent working on `reproq-worker`, you must adhere to the following standards:

## 1. Observability First
- Every new feature should include relevant Prometheus metrics.
- Latency and throughput of core loops (polling, execution) must be tracked.
- System telemetry (memory usage, goroutine counts) should be exposed for the TUI.

## 2. Reliability & Safety
- **Fencing**: Always verify the `worker_id` and `RUNNING` status when updating task states.
- **Graceful Shutdown**: Ensure all tasks have time to finish or checkpoint during worker shutdown.
- **Security**: Validate all task paths against the allowed modules list.

## 3. Performance
- Use atomic database operations (CTEs) to minimize round-trips.
- Monitor and optimize `SKIP LOCKED` query performance.
- Ensure the event broker doesn't become a bottleneck under high task volume.

## 4. Testing & Verification
- Run `make test` and `make test-integration` for every change.
- Verify that metrics are correctly incremented in relevant tests.
- Use `go vet` and `golangci-lint`.