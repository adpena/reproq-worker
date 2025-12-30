package queue

import (
	"context"
	"fmt"
	"time"
)

type RateLimit struct {
	Key            string
	TokensPerSec   float64
	BurstSize      int
	CurrentTokens  float64
	LastRefilledAt time.Time
}

func (s *Service) SetRateLimit(ctx context.Context, key string, tokensPerSecond float64, burstSize int) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if tokensPerSecond < 0 {
		return fmt.Errorf("tokens_per_second must be >= 0")
	}
	if burstSize <= 0 {
		return fmt.Errorf("burst_size must be > 0")
	}

	query := `
		INSERT INTO rate_limits (key, tokens_per_second, burst_size, current_tokens, last_refilled_at)
		VALUES ($1, $2, $3, $3, NOW())
		ON CONFLICT (key) DO UPDATE SET
			tokens_per_second = EXCLUDED.tokens_per_second,
			burst_size = EXCLUDED.burst_size,
			current_tokens = EXCLUDED.current_tokens,
			last_refilled_at = NOW()
	`
	_, err := s.pool.Exec(ctx, query, key, tokensPerSecond, burstSize)
	return err
}

func (s *Service) ListRateLimits(ctx context.Context) ([]RateLimit, error) {
	query := `
		SELECT key, tokens_per_second, burst_size, current_tokens, last_refilled_at
		FROM rate_limits
		ORDER BY key
	`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var limits []RateLimit
	for rows.Next() {
		var rl RateLimit
		if err := rows.Scan(&rl.Key, &rl.TokensPerSec, &rl.BurstSize, &rl.CurrentTokens, &rl.LastRefilledAt); err != nil {
			return nil, err
		}
		limits = append(limits, rl)
	}
	return limits, rows.Err()
}

func (s *Service) DeleteRateLimit(ctx context.Context, key string) (int64, error) {
	if key == "" {
		return 0, fmt.Errorf("key is required")
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM rate_limits WHERE key = $1", key)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
