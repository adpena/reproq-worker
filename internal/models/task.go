package models

import (
	"encoding/json"
	"time"
)

type TaskStatus string

const (
	StatusPending    TaskStatus = "PENDING"
	StatusRunning    TaskStatus = "RUNNING"
	StatusSuccessful TaskStatus = "SUCCESSFUL"
	StatusFailed     TaskStatus = "FAILED"
	StatusRetrying   TaskStatus = "RETRYING"
	StatusCancelled  TaskStatus = "CANCELLED"
)

type TaskRun struct {
	ID           int64           `db:"id"`
	SpecHash     string          `db:"spec_hash"`
	QueueName    string          `db:"queue_name"`
	Status       TaskStatus      `db:"status"`
	Priority     int             `db:"priority"`
	RunAfter     time.Time       `db:"run_after"`
	LeasedUntil  *time.Time      `db:"leased_until"`
	WorkerID     *string         `db:"worker_id"`
	PayloadJSON  json.RawMessage `db:"payload_json"`
	ResultJSON   json.RawMessage `db:"result_json"`
	ErrorJSON    json.RawMessage `db:"error_json"`
	AttemptCount int             `db:"attempt_count"`
	MaxAttempts  int             `db:"max_attempts"`
	CreatedAt    time.Time       `db:"created_at"`
	UpdatedAt    time.Time       `db:"updated_at"`
	StartedAt    *time.Time      `db:"started_at"`
	CompletedAt  *time.Time      `db:"completed_at"`
}
