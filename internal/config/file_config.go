package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

var defaultConfigFilenames = []string{
	"reproq.yaml",
	"reproq.yml",
	"reproq.toml",
	".reproq.yaml",
	".reproq.yml",
	".reproq.toml",
}

type FileConfig struct {
	DSN     string            `yaml:"dsn" toml:"dsn"`
	Worker  WorkerFileConfig  `yaml:"worker" toml:"worker"`
	Beat    BeatFileConfig    `yaml:"beat" toml:"beat"`
	Metrics MetricsFileConfig `yaml:"metrics" toml:"metrics"`
}

type WorkerFileConfig struct {
	DSN                    string   `yaml:"dsn" toml:"dsn"`
	WorkerID               string   `yaml:"worker_id" toml:"worker_id"`
	ProcessRole            string   `yaml:"process_role" toml:"process_role"`
	Queues                 []string `yaml:"queues" toml:"queues"`
	AllowedTaskModules     []string `yaml:"allowed_task_modules" toml:"allowed_task_modules"`
	Concurrency            *int     `yaml:"concurrency" toml:"concurrency"`
	PollMinBackoff         string   `yaml:"poll_min_backoff" toml:"poll_min_backoff"`
	PollMaxBackoff         string   `yaml:"poll_max_backoff" toml:"poll_max_backoff"`
	LeaseSeconds           *int     `yaml:"lease_seconds" toml:"lease_seconds"`
	HeartbeatSeconds       *int     `yaml:"heartbeat_seconds" toml:"heartbeat_seconds"`
	ReclaimIntervalSeconds *int     `yaml:"reclaim_interval_seconds" toml:"reclaim_interval_seconds"`
	PythonBin              string   `yaml:"python_bin" toml:"python_bin"`
	ExecutorModule         string   `yaml:"executor_module" toml:"executor_module"`
	PayloadMode            string   `yaml:"payload_mode" toml:"payload_mode"`
	MaxPayloadBytes        *int     `yaml:"max_payload_bytes" toml:"max_payload_bytes"`
	MaxStdoutBytes         *int     `yaml:"max_stdout_bytes" toml:"max_stdout_bytes"`
	MaxStderrBytes         *int     `yaml:"max_stderr_bytes" toml:"max_stderr_bytes"`
	LogsDir                string   `yaml:"logs_dir" toml:"logs_dir"`
	ExecTimeout            string   `yaml:"exec_timeout" toml:"exec_timeout"`
	MaxAttemptsDefault     *int     `yaml:"max_attempts_default" toml:"max_attempts_default"`
	ShutdownTimeout        string   `yaml:"shutdown_timeout" toml:"shutdown_timeout"`
	PriorityAgingFactor    *float64 `yaml:"priority_aging_factor" toml:"priority_aging_factor"`
}

type BeatFileConfig struct {
	DSN      string `yaml:"dsn" toml:"dsn"`
	Interval string `yaml:"interval" toml:"interval"`
}

type MetricsFileConfig struct {
	Addr           string   `yaml:"addr" toml:"addr"`
	Port           *int     `yaml:"port" toml:"port"`
	AuthToken      string   `yaml:"auth_token" toml:"auth_token"`
	AllowCIDRs     []string `yaml:"allow_cidrs" toml:"allow_cidrs"`
	AuthLimit      *int     `yaml:"auth_limit" toml:"auth_limit"`
	AuthWindow     string   `yaml:"auth_window" toml:"auth_window"`
	AuthMaxEntries *int     `yaml:"auth_max_entries" toml:"auth_max_entries"`
	TLSCert        string   `yaml:"tls_cert" toml:"tls_cert"`
	TLSKey         string   `yaml:"tls_key" toml:"tls_key"`
	TLSClientCA    string   `yaml:"tls_client_ca" toml:"tls_client_ca"`
}

func ResolveConfigPath(args []string) (string, error) {
	path, ok, err := parseConfigFlag(args)
	if err != nil {
		return "", err
	}
	if ok {
		return path, nil
	}
	if env := os.Getenv("REPROQ_CONFIG"); env != "" {
		return env, nil
	}
	for _, name := range defaultConfigFilenames {
		if fileExists(name) {
			return name, nil
		}
	}
	return "", nil
}

func LoadFileConfig(path string) (*FileConfig, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg FileConfig
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse yaml config: %w", err)
		}
	case ".toml":
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse toml config: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config extension: %s", filepath.Ext(path))
	}

	return &cfg, nil
}

func ApplyFileConfig(cfg *Config, fileCfg *FileConfig) error {
	if fileCfg == nil {
		return nil
	}

	if fileCfg.DSN != "" {
		cfg.DatabaseURL = fileCfg.DSN
	}

	if fileCfg.Worker.DSN != "" {
		cfg.DatabaseURL = fileCfg.Worker.DSN
	}
	if fileCfg.Worker.WorkerID != "" {
		cfg.WorkerID = fileCfg.Worker.WorkerID
	}
	if fileCfg.Worker.ProcessRole != "" {
		cfg.ProcessRole = fileCfg.Worker.ProcessRole
	}
	if len(fileCfg.Worker.Queues) > 0 {
		cfg.QueueNames = append([]string{}, fileCfg.Worker.Queues...)
	}
	if len(fileCfg.Worker.AllowedTaskModules) > 0 {
		cfg.AllowedTaskModules = append([]string{}, fileCfg.Worker.AllowedTaskModules...)
	}
	if fileCfg.Worker.Concurrency != nil {
		cfg.MaxConcurrency = *fileCfg.Worker.Concurrency
	}
	if fileCfg.Worker.PollMinBackoff != "" {
		parsed, err := parseDurationField("worker.poll_min_backoff", fileCfg.Worker.PollMinBackoff)
		if err != nil {
			return err
		}
		cfg.PollMinBackoff = parsed
	}
	if fileCfg.Worker.PollMaxBackoff != "" {
		parsed, err := parseDurationField("worker.poll_max_backoff", fileCfg.Worker.PollMaxBackoff)
		if err != nil {
			return err
		}
		cfg.PollMaxBackoff = parsed
	}
	if cfg.PollMaxBackoff < cfg.PollMinBackoff {
		return fmt.Errorf("worker.poll_max_backoff must be >= worker.poll_min_backoff")
	}
	if fileCfg.Worker.LeaseSeconds != nil {
		cfg.LeaseSeconds = *fileCfg.Worker.LeaseSeconds
	}
	if fileCfg.Worker.HeartbeatSeconds != nil {
		cfg.HeartbeatSeconds = *fileCfg.Worker.HeartbeatSeconds
	}
	if fileCfg.Worker.ReclaimIntervalSeconds != nil {
		cfg.ReclaimIntervalSeconds = *fileCfg.Worker.ReclaimIntervalSeconds
	}
	if fileCfg.Worker.PythonBin != "" {
		cfg.PythonBin = fileCfg.Worker.PythonBin
	}
	if fileCfg.Worker.ExecutorModule != "" {
		cfg.ExecutorModule = fileCfg.Worker.ExecutorModule
	}
	if fileCfg.Worker.PayloadMode != "" {
		cfg.PayloadMode = fileCfg.Worker.PayloadMode
	}
	if fileCfg.Worker.MaxPayloadBytes != nil {
		cfg.MaxPayloadBytes = *fileCfg.Worker.MaxPayloadBytes
	}
	if fileCfg.Worker.MaxStdoutBytes != nil {
		cfg.MaxStdoutBytes = *fileCfg.Worker.MaxStdoutBytes
	}
	if fileCfg.Worker.MaxStderrBytes != nil {
		cfg.MaxStderrBytes = *fileCfg.Worker.MaxStderrBytes
	}
	if fileCfg.Worker.LogsDir != "" {
		cfg.LogsDir = fileCfg.Worker.LogsDir
	}
	if fileCfg.Worker.ExecTimeout != "" {
		parsed, err := parseDurationField("worker.exec_timeout", fileCfg.Worker.ExecTimeout)
		if err != nil {
			return err
		}
		cfg.ExecTimeout = parsed
	}
	if fileCfg.Worker.MaxAttemptsDefault != nil {
		cfg.MaxAttemptsDefault = *fileCfg.Worker.MaxAttemptsDefault
	}
	if fileCfg.Worker.ShutdownTimeout != "" {
		parsed, err := parseDurationField("worker.shutdown_timeout", fileCfg.Worker.ShutdownTimeout)
		if err != nil {
			return err
		}
		cfg.ShutdownTimeout = parsed
	}
	if fileCfg.Worker.PriorityAgingFactor != nil {
		cfg.PriorityAgingFactor = *fileCfg.Worker.PriorityAgingFactor
	}

	return nil
}

func parseConfigFlag(args []string) (string, bool, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--config" || arg == "-config" {
			if i+1 >= len(args) {
				return "", true, fmt.Errorf("missing value for --config")
			}
			if args[i+1] == "" {
				return "", true, fmt.Errorf("missing value for --config")
			}
			return args[i+1], true, nil
		}
		if strings.HasPrefix(arg, "--config=") {
			value := strings.TrimPrefix(arg, "--config=")
			if value == "" {
				return "", true, fmt.Errorf("missing value for --config")
			}
			return value, true, nil
		}
	}
	return "", false, nil
}

func parseDurationField(field, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", field, err)
	}
	return parsed, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
