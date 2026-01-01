package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestResolveConfigPathDefault(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(orig)
	})

	path := filepath.Join(dir, "reproq.yaml")
	if err := os.WriteFile(path, []byte("dsn: postgres://example"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := ResolveConfigPath([]string{})
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if got != "reproq.yaml" {
		t.Fatalf("expected reproq.yaml, got %q", got)
	}
}

func TestLoadFileConfigYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reproq.yaml")
	content := `
dsn: postgres://user:pass@localhost:5432/db
worker:
  queues:
    - default
    - high
  concurrency: 4
  poll_min_backoff: "250ms"
  poll_max_backoff: "2s"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DSN != "postgres://user:pass@localhost:5432/db" {
		t.Fatalf("expected DSN to be set, got %q", cfg.DSN)
	}
	if cfg.Worker.Concurrency == nil || *cfg.Worker.Concurrency != 4 {
		t.Fatalf("expected concurrency 4, got %v", cfg.Worker.Concurrency)
	}
	wantQueues := []string{"default", "high"}
	if !reflect.DeepEqual(cfg.Worker.Queues, wantQueues) {
		t.Fatalf("expected queues %v, got %v", wantQueues, cfg.Worker.Queues)
	}
}

func TestApplyFileConfigOverrides(t *testing.T) {
	cfg := DefaultConfig()
	fileCfg := &FileConfig{
		Worker: WorkerFileConfig{
			ProcessRole:         "scheduler",
			Queues:              []string{"alpha", "beta"},
			Concurrency:         intPtr(3),
			PollMinBackoff:      "150ms",
			PollMaxBackoff:      "1s",
			ExecTimeout:         "2m",
			ShutdownTimeout:     "12s",
			PriorityAgingFactor: floatPtr(120.0),
		},
	}

	if err := ApplyFileConfig(cfg, fileCfg); err != nil {
		t.Fatalf("apply file config: %v", err)
	}
	if cfg.MaxConcurrency != 3 {
		t.Fatalf("expected concurrency 3, got %d", cfg.MaxConcurrency)
	}
	if cfg.PollMinBackoff != 150*time.Millisecond {
		t.Fatalf("expected poll min 150ms, got %v", cfg.PollMinBackoff)
	}
	if cfg.PollMaxBackoff != 1*time.Second {
		t.Fatalf("expected poll max 1s, got %v", cfg.PollMaxBackoff)
	}
	if cfg.ExecTimeout != 2*time.Minute {
		t.Fatalf("expected exec timeout 2m, got %v", cfg.ExecTimeout)
	}
	if cfg.ShutdownTimeout != 12*time.Second {
		t.Fatalf("expected shutdown timeout 12s, got %v", cfg.ShutdownTimeout)
	}
	if cfg.ProcessRole != "scheduler" {
		t.Fatalf("expected process role scheduler, got %q", cfg.ProcessRole)
	}
	if cfg.PriorityAgingFactor != 120.0 {
		t.Fatalf("expected priority aging 120, got %v", cfg.PriorityAgingFactor)
	}
	if !reflect.DeepEqual(cfg.QueueNames, []string{"alpha", "beta"}) {
		t.Fatalf("expected queues [alpha beta], got %v", cfg.QueueNames)
	}
}

func TestApplyFileConfigInvalidDuration(t *testing.T) {
	cfg := DefaultConfig()
	fileCfg := &FileConfig{
		Worker: WorkerFileConfig{
			PollMinBackoff: "nope",
		},
	}
	if err := ApplyFileConfig(cfg, fileCfg); err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestApplyFileConfigInvalidBackoffRange(t *testing.T) {
	cfg := DefaultConfig()
	fileCfg := &FileConfig{
		Worker: WorkerFileConfig{
			PollMinBackoff: "5s",
			PollMaxBackoff: "1s",
		},
	}
	if err := ApplyFileConfig(cfg, fileCfg); err == nil {
		t.Fatal("expected error for invalid backoff range")
	}
}

func intPtr(val int) *int {
	return &val
}

func floatPtr(val float64) *float64 {
	return &val
}
