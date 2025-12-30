-- Disable default global rate limit to avoid throttling by default.
INSERT INTO rate_limits (key, tokens_per_second, burst_size, current_tokens, last_refilled_at)
VALUES ('global', 0, 1, 0, NOW())
ON CONFLICT (key) DO UPDATE SET
    tokens_per_second = 0,
    current_tokens = 0,
    last_refilled_at = NOW();
