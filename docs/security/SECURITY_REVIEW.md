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
- Production builds (`-tags prod`) reject `--payload-mode inline`.

## Risks and recommendations
### Metrics and health exposure
Risk: `/metrics` and `/healthz` can expose operational data when bound publicly.
Recommendation:
- Bind to `127.0.0.1` or restrict at the network layer.
- Use `METRICS_AUTH_TOKEN` when exposure is required.
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

## Follow-up items
- Keep `docs/security/SECURITY_CHECKLIST.md` in sync with new features.
- Evaluate mTLS support for the metrics endpoint.
- Define an egress policy for worker hosts.
