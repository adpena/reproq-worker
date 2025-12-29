-- Token bucket rate limiting table
CREATE TABLE rate_limits (
    key TEXT PRIMARY KEY,
    tokens_per_second REAL NOT NULL,
    burst_size INTEGER NOT NULL,
    current_tokens REAL NOT NULL,
    last_refilled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed some default limits
INSERT INTO rate_limits (key, tokens_per_second, burst_size, current_tokens)
VALUES ('global', 100, 200, 200);
