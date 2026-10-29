package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOutputBudgetFitsACompleteFailureEnvelope(t *testing.T) {
	if err := ValidateExecutionLimits(1, 12, 1, time.Second); err == nil {
		t.Fatal("unusable output budget accepted")
	}
	if err := ValidateExecutionLimits(1, 13, 1, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorByteBudgetsRejectCoercionAndNonpositiveValues(t *testing.T) {
	for _, name := range []string{"max_payload_bytes", "max_stdout_bytes", "max_stderr_bytes"} {
		for _, value := range []string{"1.5", "1.0", "0", "-1", "true", "\"1024\""} {
			for _, extension := range []string{"yaml", "toml"} {
				t.Run(name+"/"+value+"/"+extension, func(t *testing.T) {
					data := fmt.Sprintf("worker:\n  %s: %s\n", name, value)
					if extension == "toml" {
						data = fmt.Sprintf("[worker]\n%s = %s\n", name, value)
					}
					path := filepath.Join(t.TempDir(), "reproq."+extension)
					if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
						t.Fatal(err)
					}
					file, err := LoadFileConfig(path)
					if err == nil {
						err = ApplyFileConfig(DefaultConfig(), file)
					}
					if err == nil {
						t.Fatal("invalid byte budget accepted")
					}
				})
			}
		}
	}
}

func TestYAMLByteBudgetValidationResolvesAliasesAndMerges(t *testing.T) {
	for _, data := range []string{
		"value: &cap 1.5\nworker:\n  max_stdout_bytes: *cap\n",
		"defaults: &defaults\n  max_stdout_bytes: 1.5\nworker:\n  <<: *defaults\n",
	} {
		path := filepath.Join(t.TempDir(), "reproq.yaml")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFileConfig(path); err == nil {
			t.Fatal("indirect float budget accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "reproq.yaml")
	if err := os.WriteFile(path, []byte("worker:\n  max_stdout_bytes: 12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	if err := ApplyFileConfig(cfg, file); err != nil {
		t.Fatal(err)
	}
	if cfg.MaxStdoutBytes != 12345 {
		t.Fatal("integer byte cap was not preserved")
	}
}
