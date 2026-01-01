package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL string

	WorkerID string

	ProcessRole string

	Version string

	QueueNames []string

	AllowedTaskModules []string

	MaxConcurrency int

	PollMinBackoff time.Duration

	PollMaxBackoff time.Duration

	LeaseSeconds int

	HeartbeatSeconds int

	ReclaimIntervalSeconds int

	PythonBin string

	ExecutorModule string

	PayloadMode string // "stdin", "file", "inline"

	MaxPayloadBytes int

	MaxStdoutBytes int

	MaxStderrBytes int

	LogsDir string

	ExecTimeout time.Duration

	MaxAttemptsDefault int

	ShutdownTimeout time.Duration

	PriorityAgingFactor float64 // How many seconds of waiting equals 1 priority point

}

func (c *Config) BindFlags(fs *flag.FlagSet) {

	fs.StringVar(&c.DatabaseURL, "dsn", c.DatabaseURL, "Database connection string")

	fs.StringVar(&c.WorkerID, "worker-id", c.WorkerID, "Unique worker ID")

	fs.StringVar(&c.ProcessRole, "process-role", c.ProcessRole, "Process role label for event metadata")

	fs.Var(&stringSliceFlag{target: &c.QueueNames, parser: parseQueueList}, "queues", "Comma-separated queue names")

	fs.Var(&stringSliceFlag{target: &c.AllowedTaskModules, parser: parseCommaList}, "allowed-task-modules", "Comma-separated allowed task module prefixes")

	fs.IntVar(&c.MaxConcurrency, "concurrency", c.MaxConcurrency, "Max concurrent tasks")

	fs.IntVar(&c.LeaseSeconds, "lease-seconds", c.LeaseSeconds, "Lease duration")

	fs.IntVar(&c.HeartbeatSeconds, "heartbeat-seconds", c.HeartbeatSeconds, "Heartbeat interval")

	fs.IntVar(&c.ReclaimIntervalSeconds, "reclaim-interval-seconds", c.ReclaimIntervalSeconds, "Reclaim interval for expired leases (0 to disable)")

	fs.StringVar(&c.PayloadMode, "payload-mode", c.PayloadMode, "Payload mode: stdin|file|inline")

	fs.Float64Var(&c.PriorityAgingFactor, "priority-aging-factor", c.PriorityAgingFactor, "Seconds of waiting per priority point (0 to disable)")

	fs.StringVar(&c.LogsDir, "logs-dir", c.LogsDir, "Directory to persist stdout/stderr logs (empty disables)")

}

func DefaultConfig() *Config {

	hostname, _ := os.Hostname()

	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())

	return &Config{

		DatabaseURL: "",

		WorkerID: workerID,

		ProcessRole: "worker",

		QueueNames: []string{"default"},

		AllowedTaskModules: []string{},

		MaxConcurrency: 10,

		PollMinBackoff: 100 * time.Millisecond,

		PollMaxBackoff: 5 * time.Second,

		LeaseSeconds: 300,

		HeartbeatSeconds: 60,

		ReclaimIntervalSeconds: 60,

		PythonBin: "python3",

		ExecutorModule: "reproq_django.executor",

		PayloadMode: "stdin",

		MaxPayloadBytes: 1024 * 1024,

		MaxStdoutBytes: 1024 * 1024,

		MaxStderrBytes: 1024 * 1024,

		LogsDir: "",

		ExecTimeout: 1 * time.Hour,

		MaxAttemptsDefault: 3,

		ShutdownTimeout: 30 * time.Second,

		PriorityAgingFactor: 60.0,
	}

}

func ApplyEnv(c *Config) error {

	if val := os.Getenv("DATABASE_URL"); val != "" {
		c.DatabaseURL = val
	}

	if val := os.Getenv("WORKER_ID"); val != "" {
		c.WorkerID = val
	}

	if val := os.Getenv("REPROQ_PROCESS_ROLE"); val != "" {
		c.ProcessRole = val
	}

	if val := os.Getenv("QUEUE_NAMES"); val != "" {
		c.QueueNames = parseQueueList(val)
	}

	if val := os.Getenv("ALLOWED_TASK_MODULES"); val != "" {
		c.AllowedTaskModules = parseCommaList(val)
	}

	if val := os.Getenv("REPROQ_LOGS_DIR"); val != "" {
		c.LogsDir = val
	}

	if val := os.Getenv("PRIORITY_AGING_FACTOR"); val != "" {
		parsed, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("invalid PRIORITY_AGING_FACTOR: %w", err)
		}
		c.PriorityAgingFactor = parsed
	}

	return nil

}

func Load() (*Config, error) {

	c := DefaultConfig()
	if err := ApplyEnv(c); err != nil {
		return nil, err
	}

	return c, nil

}

type stringSliceFlag struct {
	target *[]string
	parser func(string) []string
}

func (s *stringSliceFlag) String() string {
	if s == nil || s.target == nil {
		return ""
	}
	return strings.Join(*s.target, ",")
}

func (s *stringSliceFlag) Set(val string) error {
	if s.parser != nil {
		*s.target = s.parser(val)
	} else {
		*s.target = parseCommaList(val)
	}
	return nil
}

func parseCommaList(input string) []string {
	parts := strings.Split(input, ",")
	queues := make([]string, 0, len(parts))
	for _, part := range parts {
		queue := strings.TrimSpace(part)
		if queue != "" {
			queues = append(queues, queue)
		}
	}
	if len(queues) == 0 {
		return []string{}
	}
	return queues
}

func parseQueueList(input string) []string {
	queues := parseCommaList(input)
	if len(queues) == 0 {
		return []string{"default"}
	}
	return queues
}
