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

	QueueNames      []string

	MaxConcurrency  int

	PollMinBackoff  time.Duration

	PollMaxBackoff  time.Duration

	LeaseSeconds    int

	HeartbeatSeconds int

	

	PythonBin       string

	ExecutorModule  string

	PayloadMode     string // "stdin", "file", "inline"

	MaxPayloadBytes int

	MaxStdoutBytes  int

	MaxStderrBytes  int

	ExecTimeout     time.Duration

	

	MaxAttemptsDefault int

	ShutdownTimeout    time.Duration

	HealthAddr         string        // HTTP address for health/metrics

	PriorityAgingFactor float64       // How many seconds of waiting equals 1 priority point

}



func (c *Config) BindFlags(fs *flag.FlagSet) {

	fs.StringVar(&c.DatabaseURL, "dsn", c.DatabaseURL, "Database connection string")

	fs.StringVar(&c.WorkerID, "worker-id", c.WorkerID, "Unique worker ID")

	fs.IntVar(&c.MaxConcurrency, "concurrency", c.MaxConcurrency, "Max concurrent tasks")

	fs.IntVar(&c.LeaseSeconds, "lease-seconds", c.LeaseSeconds, "Lease duration")

	fs.IntVar(&c.HeartbeatSeconds, "heartbeat-seconds", c.HeartbeatSeconds, "Heartbeat interval")

	fs.StringVar(&c.PayloadMode, "payload-mode", c.PayloadMode, "Payload mode: stdin|file|inline")

}



func Load() (*Config, error) {

	dbURL := os.Getenv("DATABASE_URL")

	if dbURL == "" {

		return nil, fmt.Errorf("DATABASE_URL required")

	}



	workerID := os.Getenv("WORKER_ID")

	if workerID == "" {

		hostname, _ := os.Hostname()

		workerID = fmt.Sprintf("%s-%d", hostname, os.Getpid())

	}



	healthAddr := os.Getenv("HEALTH_ADDR")

	if healthAddr == "" {

		healthAddr = ":8080"

	}



	c := &Config{

		DatabaseURL:        dbURL,

		WorkerID:           workerID,

		QueueNames:         []string{"default"},

		MaxConcurrency:     10,

		PollMinBackoff:     100 * time.Millisecond,

		PollMaxBackoff:     5 * time.Second,

		LeaseSeconds:       300,

		HeartbeatSeconds:   60,

		PythonBin:          "python3",

		ExecutorModule:     "reproq_django.executor",

		PayloadMode:        "stdin",

		MaxPayloadBytes:    1024 * 1024,

		MaxStdoutBytes:     1024 * 1024,

		MaxStderrBytes:     1024 * 1024,

		ExecTimeout:        1 * time.Hour,

		MaxAttemptsDefault: 3,

		ShutdownTimeout:    30 * time.Second,

		HealthAddr:         healthAddr,

		PriorityAgingFactor: 60.0,

	}



	return c, nil

}




