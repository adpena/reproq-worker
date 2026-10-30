package config

import (
	"flag"
	"io"
	"os"
	"reflect"
	"testing"
)

func TestQueueDSNUsesNamespacedEnvironmentWithoutChangingBusinessDatabase(t *testing.T) {
	const business = "postgres://business.invalid/business"
	t.Setenv("DATABASE_URL", business)
	t.Setenv(QueueDatabaseEnv, "")
	configuration, err := Load()
	if err != nil || configuration.DatabaseURL != "" {
		t.Fatal("worker interpreted the Python business database as its queue")
	}
	configuration.DatabaseURL = "postgres://file.invalid/queue"
	t.Setenv(QueueDatabaseEnv, "postgres://queue.invalid/queue")
	if err := ApplyEnv(configuration); err != nil || configuration.DatabaseURL != "postgres://queue.invalid/queue" {
		t.Fatal("namespaced queue environment did not override file configuration")
	}
	flags := flag.NewFlagSet("owned", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configuration.BindFlags(flags)
	if err := flags.Parse([]string{"--dsn", "postgres://flag.invalid/queue"}); err != nil || configuration.DatabaseURL != "postgres://flag.invalid/queue" {
		t.Fatal("explicit queue flag did not override its environment")
	}
	if os.Getenv("DATABASE_URL") != business {
		t.Fatal("queue configuration replaced the Python business database")
	}
}

func TestPythonExecutableFlagPreservesDefaultAndExplicitFilePrecedence(t *testing.T) {
	for _, configured := range []string{"", "/operator environment/bin/python"} {
		for _, selected := range []string{"", "/selected environment/bin/python"} {
			t.Run(configured+"/"+selected, func(t *testing.T) {
				configuration := DefaultConfig()
				if err := ApplyFileConfig(configuration, &FileConfig{Worker: WorkerFileConfig{PythonBin: configured}}); err != nil {
					t.Fatal(err)
				}
				flags := flag.NewFlagSet("owned", flag.ContinueOnError)
				flags.SetOutput(io.Discard)
				configuration.BindFlags(flags)
				arguments := []string{}
				if selected != "" {
					arguments = []string{"--python-bin", selected}
				}
				if err := flags.Parse(arguments); err != nil {
					t.Fatal(err)
				}
				expected := "python3"
				if configured != "" {
					expected = configured
				}
				if selected != "" {
					expected = selected
				}
				if configuration.PythonBin != expected {
					t.Fatalf("executor Python = %q, expected %q", configuration.PythonBin, expected)
				}
			})
		}
	}
}

func TestLoadAllowsEmptyDatabaseURL(t *testing.T) {
	t.Setenv(QueueDatabaseEnv, "")
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
	t.Setenv(QueueDatabaseEnv, "")
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

func TestLoadLogsDirFromEnv(t *testing.T) {
	t.Setenv("REPROQ_LOGS_DIR", "/tmp/reproq-logs")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.LogsDir != "/tmp/reproq-logs" {
		t.Fatalf("expected logs dir to be set, got %q", cfg.LogsDir)
	}
}

func TestBindFlagsParsesLogsDir(t *testing.T) {
	cfg := &Config{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg.BindFlags(fs)

	if err := fs.Parse([]string{"--logs-dir", "/var/log/reproq"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.LogsDir != "/var/log/reproq" {
		t.Fatalf("expected logs dir /var/log/reproq, got %q", cfg.LogsDir)
	}
}
