package config

import (
	"flag"
	"fmt"
	"os"
	"time"
)

type Config struct {
	DatabaseURL     string
	WorkerID        string
	PollInterval    time.Duration
	QueueName       string
	PythonCommand   []string
	ExecMode        string        // "shell" or "mock"
	ExecSleep       time.Duration // Sleep duration for mock executor
	LeaseDuration   time.Duration // How long a worker claims a task for
	ShutdownTimeout     time.Duration // How long to wait for tasks to finish on shutdown
	HealthAddr          string        // HTTP address for health/metrics
	PriorityAgingFactor float64       // How many seconds of waiting equals 1 priority point
}

func (c *Config) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.DatabaseURL, "dsn", c.DatabaseURL, "Database connection string")
	fs.StringVar(&c.WorkerID, "worker-id", c.WorkerID, "Unique worker ID")
	fs.DurationVar(&c.PollInterval, "poll-interval", c.PollInterval, "Interval to poll for tasks")
	fs.StringVar(&c.QueueName, "queue", c.QueueName, "Queue name to process")
	fs.StringVar(&c.ExecMode, "exec-mode", c.ExecMode, "Execution mode (shell|mock)")
	fs.DurationVar(&c.ExecSleep, "exec-sleep", c.ExecSleep, "Sleep duration for mock mode")
	fs.DurationVar(&c.LeaseDuration, "lease-duration", c.LeaseDuration, "Initial task lease duration")
	fs.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", c.ShutdownTimeout, "Time to wait for tasks on shutdown")
	fs.StringVar(&c.HealthAddr, "health-addr", c.HealthAddr, "HTTP address for health/metrics")
	fs.Float64Var(&c.PriorityAgingFactor, "priority-aging-factor", c.PriorityAgingFactor, "Seconds of waiting for 1 priority point (0 to disable)")
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
	baseCmd := []string{pythonPath, "-m", "task_executor"}

	execMode := os.Getenv("EXEC_MODE")
	if execMode == "" {
		execMode = "shell"
	}

	execSleep := 100 * time.Millisecond
	if sleepStr := os.Getenv("EXEC_SLEEP"); sleepStr != "" {
		if d, err := time.ParseDuration(sleepStr); err == nil {
			execSleep = d
		}
	}
	
	leaseDuration := 5 * time.Minute
	shutdownTimeout := 30 * time.Second
	healthAddr := ":8080"
	if addr := os.Getenv("HEALTH_ADDR"); addr != "" {
		healthAddr = addr
	}
	priorityAgingFactor := 60.0 // Default: 1 priority point per minute

	return &Config{
		DatabaseURL:         dbURL,
		WorkerID:            workerID,
		PollInterval:        pollInterval,
		QueueName:           queueName,
		PythonCommand:       baseCmd,
		ExecMode:            execMode,
		ExecSleep:           execSleep,
		LeaseDuration:       leaseDuration,
		ShutdownTimeout:     shutdownTimeout,
		HealthAddr:          healthAddr,
		PriorityAgingFactor: priorityAgingFactor,
	}, nil
}
