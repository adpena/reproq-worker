# Reproq Worker Production Security Checklist

## Build and release
- [ ] Build with `-tags prod` to disable `--payload-mode inline`.
- [ ] Embed the correct worker `Version` and verify with `reproq --version`.
- [ ] Publish platform binaries only after running `go test ./internal/...`.

## Secrets and configuration
- [ ] Use `METRICS_AUTH_TOKEN` when exposing `/metrics` or `/healthz`.
- [ ] Set `METRICS_ALLOW_CIDRS` to restrict metrics/health to trusted networks.
- [ ] Use `METRICS_TLS_CERT` and `METRICS_TLS_KEY` to serve metrics over HTTPS; add `METRICS_TLS_CLIENT_CA` to require mTLS.
- [ ] Keep metrics auth rate limiting enabled; tune via `METRICS_AUTH_LIMIT` and `METRICS_AUTH_WINDOW`.
- [ ] Store DSNs and tokens in a secrets manager (not shell history).
- [ ] Set `ALLOWED_TASK_MODULES` to the minimal allow-list for your deployment.

## Network exposure
- [ ] Bind metrics/health to `127.0.0.1` or a private interface.
- [ ] Enforce ingress ACLs or a reverse proxy when public exposure is required.
- [ ] Restrict worker egress at the container or host firewall layer.
- [ ] Deny outbound traffic by default and allow only required destinations (DB, storage, third-party APIs).
- [ ] Block cloud metadata endpoints (for example `169.254.169.254`) unless explicitly required.

## Logging and data handling
- [ ] Prefer `--payload-mode stdin` or `file` for sensitive payloads.
- [ ] Verify log redaction covers payload and secret-like keys.
- [ ] Set log retention and access controls for production logs.

## Database and runtime safety
- [ ] Ensure DB user permissions are least-privilege for worker operations.
- [ ] Monitor for repeated task failures and triage via `reproq triage`.
- [ ] Validate that `reproq cancel` is restricted to trusted operators.

## Observability
- [ ] Scrape Prometheus metrics from a secured endpoint.
- [ ] Alert on worker liveness and high failure rates.
- [ ] Track queue latency (enqueue to start) and execution duration.
