package config

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestLoadAllowsEmptyDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PRIORITY_AGING_FACTOR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg.DatabaseURL != "" {
		t.Fatalf("expected empty DatabaseURL, got %q", cfg.DatabaseURL)
	}
}

func TestLoadInvalidPriorityAgingFactor(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PRIORITY_AGING_FACTOR", "not-a-number")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid PRIORITY_AGING_FACTOR")
	}
}

func TestLoadQueueNamesFromEnv(t *testing.T) {
	t.Setenv("QUEUE_NAMES", "alpha, beta,,gamma")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(cfg.QueueNames, want) {
		t.Fatalf("expected %v, got %v", want, cfg.QueueNames)
	}
}

func TestBindFlagsParsesQueues(t *testing.T) {
	cfg := &Config{
		QueueNames: []string{"default"},
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg.BindFlags(fs)

	if err := fs.Parse([]string{"--queues", "one,two"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := []string{"one", "two"}
	if !reflect.DeepEqual(cfg.QueueNames, want) {
		t.Fatalf("expected %v, got %v", want, cfg.QueueNames)
	}
}

func TestLoadAllowedTaskModulesFromEnv(t *testing.T) {
	t.Setenv("ALLOWED_TASK_MODULES", "foo.bar., baz.,,")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := []string{"foo.bar.", "baz."}
	if !reflect.DeepEqual(cfg.AllowedTaskModules, want) {
		t.Fatalf("expected %v, got %v", want, cfg.AllowedTaskModules)
	}
}

func TestBindFlagsParsesAllowedTaskModules(t *testing.T) {
	cfg := &Config{
		AllowedTaskModules: []string{},
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg.BindFlags(fs)

	if err := fs.Parse([]string{"--allowed-task-modules", "pkg.one.,pkg.two."}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := []string{"pkg.one.", "pkg.two."}
	if !reflect.DeepEqual(cfg.AllowedTaskModules, want) {
		t.Fatalf("expected %v, got %v", want, cfg.AllowedTaskModules)
	}
}
