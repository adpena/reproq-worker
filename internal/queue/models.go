package queue

import (
	"encoding/json"
	"time"
)

type TaskStatus string

const (
	StatusReady      TaskStatus = "READY"
	StatusRunning    TaskStatus = "RUNNING"
	StatusWaiting    TaskStatus = "WAITING"
	StatusSuccessful TaskStatus = "SUCCESSFUL"
	StatusFailed     TaskStatus = "FAILED"
)

type TaskRun struct {
	ResultID        int64           `db:"result_id"`
	BackendAlias    string          `db:"backend_alias"`
	QueueName       string          `db:"queue_name"`
	Priority        int             `db:"priority"`
	RunAfter        *time.Time      `db:"run_after"`
	SpecJSON        json.RawMessage `db:"spec_json"`
	SpecHash        string          `db:"spec_hash"`
	TaskPath        *string         `db:"task_path"`
	Status          TaskStatus      `db:"status"`
	EnqueuedAt      time.Time       `db:"enqueued_at"`
	StartedAt       *time.Time      `db:"started_at"`
	LastAttemptedAt *time.Time      `db:"last_attempted_at"`
	FinishedAt      *time.Time      `db:"finished_at"`
	Attempts        int             `db:"attempts"`
	MaxAttempts     int             `db:"max_attempts"`
	TimeoutSeconds  int             `db:"timeout_seconds"`
	LockKey         *string         `db:"lock_key"`
	WorkerIDs       json.RawMessage `db:"worker_ids"`

	ReturnJSON      json.RawMessage `db:"return_json"`
	ErrorsJSON      json.RawMessage `db:"errors_json"`
	LeasedUntil     *time.Time      `db:"leased_until"`
	LeasedBy        *string         `db:"leased_by"`
	LogsURI         *string         `db:"logs_uri"`
	ArtifactsURI    *string         `db:"artifacts_uri"`
	ExpiresAt       *time.Time      `db:"expires_at"`
	LastError       *string         `db:"last_error"`
	FailedAt        *time.Time      `db:"failed_at"`
	CreatedAt       time.Time       `db:"created_at"`
	UpdatedAt       time.Time       `db:"updated_at"`
	CancelRequested bool            `db:"cancel_requested"`
}
