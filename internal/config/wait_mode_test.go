package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWaitModePrecedenceAndInvalidValues(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.WaitMode != WaitPoll {
		t.Fatal("notification listener enabled by default")
	}
	value := "notify"
	if err := ApplyFileConfig(cfg, &FileConfig{Worker: WorkerFileConfig{WaitMode: &value}}); err != nil || cfg.WaitMode != WaitNotify {
		t.Fatal(err)
	}
	t.Setenv("REPROQ_WAIT_MODE", " POLL ")
	if err := ApplyEnv(cfg); err != nil || cfg.WaitMode != WaitPoll {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("owned", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg.BindFlags(fs)
	if err := fs.Parse([]string{"--wait-mode", "NOTIFY"}); err != nil || cfg.WaitMode != WaitNotify {
		t.Fatal(err)
	}
	for _, value := range []string{"", "false", "automatic", "secret-invalid"} {
		t.Setenv("REPROQ_WAIT_MODE", value)
		if err := ApplyEnv(cfg); err == nil || !strings.Contains(err.Error(), "poll or notify") || strings.Contains(err.Error(), "secret-invalid") {
			t.Fatalf("invalid mode not rejected safely: %v", err)
		}
	}
}

func TestWaitModeLoadsYAMLAndTOML(t *testing.T) {
	for _, item := range []struct{ name, body string }{{"config.yaml", "worker:\n  wait_mode: notify\n"}, {"config.toml", "[worker]\nwait_mode = 'notify'\n"}} {
		p := filepath.Join(t.TempDir(), item.name)
		if err := os.WriteFile(p, []byte(item.body), 0600); err != nil {
			t.Fatal(err)
		}
		file, err := LoadFileConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		cfg := DefaultConfig()
		if err = ApplyFileConfig(cfg, file); err != nil || cfg.WaitMode != WaitNotify {
			t.Fatal(err)
		}
	}
}
