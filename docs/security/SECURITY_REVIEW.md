# Security and Observability Review

## Scope
This review covers the Go worker runtime, metrics/health endpoints, and logging.

## Current controls
- Task module allow-list validation (`ALLOWED_TASK_MODULES`).
- Log redaction for payload and secret-like keys.
- Metrics/health endpoints can be bound to localhost, protected with bearer auth, and restricted by CIDR allow-list.
- DB lease fencing protects from zombie task updates.

## Recent hardening
- `/healthz` avoids leaking DB error details.
- HTTP server timeouts added to the metrics/health server.
- Unauthorized metrics/health requests are logged with path/method/remote address.
- Unauthorized metrics/health requests are rate-limited per remote host (configurable via `METRICS_AUTH_LIMIT`, `METRICS_AUTH_WINDOW`, `METRICS_AUTH_MAX_ENTRIES`).
- Metrics/health endpoint can be served over TLS with optional mTLS client certificate verification.
- Production builds (`-tags prod`) reject `--payload-mode inline`.

## Risks and recommendations
### Metrics and health exposure
Risk: `/metrics`, `/healthz`, and `/events` can expose operational data when bound publicly.
Recommendation:
- Bind to `127.0.0.1` or restrict at the network layer.
- Use `--metrics-auth-token` (or `metrics.auth_token` in config) when exposure is required.
- Use `METRICS_ALLOW_CIDRS` to restrict access to trusted ranges.
- Consider mTLS for metrics in regulated environments.

### Payload handling
Risk: `--payload-mode inline` puts payload in process args (visible in process listings).
Recommendation:
- Prefer `stdin` or `file` for sensitive payloads.
- Build with `-tags prod` to disable inline mode.

### Network egress
Risk: task executors can reach any network destination by default.
Recommendation:
- Enforce egress policies at the container or host firewall level.
- Consider a future worker-level egress allow-list.

### Rate-limited endpoints and auth
Risk: metrics/health endpoints can be probed repeatedly.
Recommendation:
- In-process rate limiting is enabled for unauthorized hits, but prefer reverse proxy or ingress controls.
- Add rate limiting at the reverse proxy or ingress.

### Egress policy baseline
Recommendation:
- Deny outbound traffic by default and allow only required destinations (DB, object storage, third-party APIs).
- Block cloud metadata IPs (for example `169.254.169.254`) unless explicitly required.
- If tasks need internal services, allow the minimal private CIDRs instead of broad `0.0.0.0/0`.
- Document any exceptions so operators can audit outbound connectivity.

## Follow-up items
- [x] Keep `docs/security/SECURITY_CHECKLIST.md` in sync with new features.
- [x] Evaluate mTLS support for the metrics endpoint (implemented via `METRICS_TLS_*`).
- [x] Define an egress policy baseline for worker hosts (documented above).
