package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	DatabaseURL  string
	WorkerID     string
	PollInterval time.Duration
	QueueName    string
	PythonCommand []string
}

func Load() (*Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	workerID := os.Getenv("WORKER_ID")
	if workerID == "" {
		hostname, _ := os.Hostname()
		workerID = fmt.Sprintf("worker-%s-%d", hostname, time.Now().Unix())
	}

	pollIntervalStr := os.Getenv("POLL_INTERVAL")
	pollInterval := 1 * time.Second
	if pollIntervalStr != "" {
		if d, err := time.ParseDuration(pollIntervalStr); err == nil {
			pollInterval = d
		}
	}

	queueName := os.Getenv("QUEUE_NAME")
	if queueName == "" {
		queueName = "default"
	}
	
	pythonPath := os.Getenv("PYTHON_PATH")
	if pythonPath == "" {
		pythonPath = "python3"
	}
	// We assume the python module/script is also configured or hardcoded for the executor?
	// The architecture says: "python -m myproject.task_executor".
	// Let's allow passing the full base command via env var or just default to something.
	// We'll add a generic TASK_EXECUTOR_CMD env var which is a comma-separated list or just use a default.
	// For simplicity, let's assume "python3", "-m", "task_executor" if not set.
	baseCmd := []string{pythonPath, "-m", "task_executor"}
	
	return &Config{
		DatabaseURL:   dbURL,
		WorkerID:      workerID,
		PollInterval:  pollInterval,
		QueueName:     queueName,
		PythonCommand: baseCmd,
	}, nil
}
