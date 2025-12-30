package queue

import "context"

// RequestCancel flags a running task for cancellation.
func (s *Service) RequestCancel(ctx context.Context, resultID int64) (int64, error) {
	query := `
		UPDATE task_runs
		SET cancel_requested = TRUE, updated_at = NOW()
		WHERE result_id = $1 AND status = 'RUNNING'
	`
	tag, err := s.pool.Exec(ctx, query, resultID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
