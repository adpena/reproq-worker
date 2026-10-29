package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reproq-worker/internal/config"
	"reproq-worker/internal/protocol"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Re-execute this test binary as a real protocol subprocess. No Python or
// platform-specific shell is needed for the process/protocol regression tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv("REPROQ_EXECUTOR_TEST_HELPER"); mode != "" {
		runExecutorHelper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runExecutorHelper(mode string) {
	if strings.HasPrefix(mode, "capabilities") {
		payload := protocol.Capabilities{SDKVersion: "owned-test", SchemaID: protocol.SchemaID, SchemaVersion: protocol.SchemaVersion, ExecutorProtocol: protocol.ExecutorVersion}
		if mode == "capabilities-schema1" {
			payload.SchemaVersion = 1
			payload.SchemaID = "reproq-schema-v1"
		}
		if mode == "capabilities-protocol1" {
			payload.ExecutorProtocol = 1
		}
		if mode == "capabilities-duplicate" {
			fmt.Print(`{"sdk_version":"a","sdk_version":"b"}`)
			return
		}
		if mode == "capabilities-secret" {
			fmt.Fprint(os.Stderr, "OWNED_PROBE_SECRET_CANARY")
			fmt.Print("OWNED_PROBE_SECRET_CANARY")
			os.Exit(1)
		}
		data, _ := json.Marshal(payload)
		fmt.Println(string(data))
		if mode == "capabilities-overflow" {
			fmt.Print(strings.Repeat(" ", capabilityResponseLimit))
		}
		if mode == "capabilities-exit" {
			os.Exit(7)
		}
		return
	}
	switch mode {
	case "database-env":
		data, _ := json.Marshal(map[string]any{"ok": true, "return": map[string]string{
			"business": os.Getenv("DATABASE_URL"), "queue": os.Getenv(config.QueueDatabaseEnv),
		}})
		fmt.Println(string(data))
	case "invalid-json":
		fmt.Print("not JSON")
	case "exit-success":
		fmt.Println(`{"ok":true,"return":7}`)
		os.Exit(7)
	case "exit-failure":
		fmt.Println(`{"ok":false,"exception_class":"ValueError","message":"task failed"}`)
		os.Exit(1)
	case "large-stderr":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), 256*1024))
		fmt.Println(`{"ok":true,"return":"drained"}`)
	case "overflow-prefix":
		fmt.Print(`{"ok":true,"return":0}` + strings.Repeat(" ", 100) + `{"ok":false}`)
	case "flags":
		data, _ := json.Marshal(map[string]any{"ok": true, "return": os.Args[1:]})
		fmt.Println(string(data))
	case "descendant":
		ignoreTestTermination()
		for {
			_ = os.WriteFile(os.Getenv("REPROQ_TEST_HEARTBEAT"), []byte(strconv.FormatInt(time.Now().UnixNano(), 10)), 0o600)
			_ = os.WriteFile(os.Getenv("REPROQ_TEST_CHILD_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600)
			time.Sleep(10 * time.Millisecond)
		}
	case "tree-block", "tree-exit":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "REPROQ_EXECUTOR_TEST_HELPER=descendant")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(90)
		}
		fmt.Println(`{"ok":true}`)
		if mode == "tree-block" {
			time.Sleep(30 * time.Second)
		}
	default:
		os.Exit(91)
	}
}

func TestExecutorPreservesSeparateBusinessAndQueueDatabaseEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://business.invalid/database-a")
	t.Setenv(config.QueueDatabaseEnv, "postgres://queue.invalid/database-b")
	e := helperExecutor(t, "database-env")
	result, _, _, err := e.Execute(context.Background(), testClaim(), json.RawMessage(`{}`), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]string
	if err := json.Unmarshal(result.Return, &actual); err != nil || actual["business"] != os.Getenv("DATABASE_URL") || actual["queue"] != os.Getenv(config.QueueDatabaseEnv) {
		t.Fatal("executor changed the business/queue database roles")
	}
}

func TestProviderCapabilityProbeRequiresCompatibleCompleteSuccessfulResponse(t *testing.T) {
	for _, mode := range []string{"capabilities", "capabilities-schema1", "capabilities-protocol1", "capabilities-duplicate", "capabilities-overflow", "capabilities-exit", "capabilities-secret"} {
		t.Run(mode, func(t *testing.T) {
			e := helperExecutor(t, mode)
			err := e.CheckCompatibility(context.Background())
			if (err == nil) != (mode == "capabilities") {
				t.Fatalf("incorrect provider compatibility: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "OWNED_PROBE_SECRET_CANARY") {
				t.Fatal("capability probe leaked provider output")
			}
		})
	}
}

func helperExecutor(t *testing.T, mode string) *ShellExecutor {
	t.Helper()
	t.Setenv("REPROQ_EXECUTOR_TEST_HELPER", mode)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &ShellExecutor{PythonBin: binary, ExecutorModule: "test.helper", PayloadMode: "stdin",
		MaxPayloadBytes: 1024, MaxStdoutBytes: 1024, MaxStderrBytes: 1024}
}

func TestExecutorRejectsNonpositiveLimits(t *testing.T) {
	for _, field := range []string{"payload", "stdout", "stderr", "timeout"} {
		for _, value := range []int{0, -1} {
			t.Run(fmt.Sprintf("%s/%d", field, value), func(t *testing.T) {
				e := helperExecutor(t, "flags")
				timeout := time.Second
				switch field {
				case "payload":
					e.MaxPayloadBytes = value
				case "stdout":
					e.MaxStdoutBytes = value
				case "stderr":
					e.MaxStderrBytes = value
				case "timeout":
					timeout = time.Duration(value)
				}
				if env, _, _, err := e.Execute(context.Background(), testClaim(), json.RawMessage(`{}`), timeout); err == nil || env != nil {
					t.Fatalf("invalid limit accepted: env=%+v err=%v", env, err)
				}
			})
		}
	}
}

func TestExecutorPassesAuthoritativeByteLimits(t *testing.T) {
	e := helperExecutor(t, "flags")
	e.MaxPayloadBytes, e.MaxStdoutBytes = 123, 567
	env, _, _, err := e.Execute(context.Background(), testClaim(), json.RawMessage(`{}`), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(env.Return, &args); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--max-output-bytes 567") || !strings.Contains(joined, "--max-payload-bytes 123") {
		t.Fatalf("configured byte caps were not delegated: %s", joined)
	}
	claim := testClaim()
	if !strings.Contains(joined, "--worker-id "+claim.WorkerID) || !strings.Contains(joined, "--lease-token "+claim.LeaseToken) {
		t.Fatal("fenced ownership was not delegated to the provider")
	}
}

func TestExecutorRejectsUnfencedClaimsBeforeStarting(t *testing.T) {
	for name, mutate := range map[string]func(*Claim){
		"missing_owner": func(c *Claim) { c.WorkerID = "" },
		"missing_token": func(c *Claim) { c.LeaseToken = "" },
		"invalid_uuid":  func(c *Claim) { c.LeaseToken = "not-a-uuid" },
		"nil_uuid":      func(c *Claim) { c.LeaseToken = "00000000-0000-0000-0000-000000000000" },
		"attempt":       func(c *Claim) { c.Attempt = 0 },
		"result":        func(c *Claim) { c.ResultID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			claim := testClaim()
			mutate(&claim)
			e := helperExecutor(t, "flags")
			if result, stdout, stderr, err := e.Execute(context.Background(), claim, json.RawMessage(`{}`), time.Second); err == nil || result != nil || stdout != "" || stderr != "" {
				t.Fatal("unfenced provider invocation accepted")
			}
		})
	}
}

func TestExecutorNonzeroExitOverridesSuccess(t *testing.T) {
	for _, mode := range []string{"exit-success", "exit-failure"} {
		t.Run(mode, func(t *testing.T) {
			e := helperExecutor(t, mode)
			env, _, _, err := e.Execute(context.Background(), testClaim(), json.RawMessage(`{}`), time.Second)
			if !errors.Is(err, ErrProcessExit) {
				t.Fatalf("expected exit error: %v", err)
			}
			if env != nil && env.Ok {
				t.Fatal("nonzero exit accepted as success")
			}
			if mode == "exit-failure" && (env == nil || env.ExceptionClass != "ValueError") {
				t.Fatalf("lost task exception classification: %+v", env)
			}
		})
	}
}

func TestExecutorOverflowCannotBecomeValidPrefix(t *testing.T) {
	e := helperExecutor(t, "overflow-prefix")
	e.MaxStdoutBytes = len(`{"ok":true,"return":0}`)
	env, _, _, err := e.Execute(context.Background(), testClaim(), json.RawMessage(`{}`), time.Second)
	if env != nil || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("truncated prefix accepted: env=%+v err=%v", env, err)
	}
}

func TestExecutorDrainsStderrAndCountsRawBytes(t *testing.T) {
	e := helperExecutor(t, "large-stderr")
	e.MaxStderrBytes = 5
	env, _, stderr, err := e.Execute(context.Background(), testClaim(), json.RawMessage(`{}`), time.Second)
	if err != nil || env == nil || !env.Ok {
		t.Fatalf("draining failed: env=%+v err=%v", env, err)
	}
	if stderr != "xxxxx\n[TRUNCATED retained_bytes=5 dropped_bytes=262139]" {
		t.Fatalf("incorrect log accounting: %q", stderr)
	}
}

func TestBoundedBufferDrainsAndRendersUTF8(t *testing.T) {
	for _, input := range [][]byte{[]byte("a你好world"), {0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		b := newBoundedBuffer(5)
		n, err := io.Copy(b, bytes.NewReader(input))
		if err != nil || n != int64(len(input)) {
			t.Fatalf("short write while draining: n=%d err=%v", n, err)
		}
		text := strings.Split(b.String(), "\n[TRUNCATED")[0]
		if !utf8.ValidString(text) || len(text) > 5 {
			t.Fatalf("unbounded/invalid text prefix: %q", text)
		}
		if b.dropped() != int64(len(input)-5) {
			t.Fatalf("incorrect raw-byte count: %d", b.dropped())
		}
	}
}

func TestResultProtocolRequiresOneUnambiguousEnvelope(t *testing.T) {
	for _, data := range []string{"", "null", "[]", `{}`, `{"ok":null}`, `{"ok":"true"}`, `{"ok":true,"ok":false}`, `{"ok":false,"OK":true}`, `{"ok":true} {"ok":false}`, `{"ok":true} trailing`, "{\"ok\":true,\"message\":\"\xff\"}"} {
		if env, err := parseResult([]byte(data)); env != nil || !errors.Is(err, ErrProtocol) {
			t.Errorf("accepted %q: %+v, %v", data, env, err)
		}
	}
	if env, err := parseResult([]byte("{\"ok\":true,\"return\":\"你好\",\"future_field\":1}\n")); err != nil || !env.Ok {
		t.Fatalf("valid extensible UTF-8 protocol rejected: %+v %v", env, err)
	}
}

func TestExecutorBoundsWaitAndTerminatesOwnedDescendants(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "leader-exit"} {
		t.Run(mode, func(t *testing.T) {
			help := "tree-block"
			if mode == "leader-exit" {
				help = "tree-exit"
			}
			e := helperExecutor(t, help)
			dir := t.TempDir()
			pidFile, heartbeat := filepath.Join(dir, "child-pid"), filepath.Join(dir, "heartbeat")
			t.Setenv("REPROQ_TEST_CHILD_PID", pidFile)
			t.Setenv("REPROQ_TEST_HEARTBEAT", heartbeat)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := 10 * time.Second
			if mode == "deadline" {
				timeout = 2 * time.Second
			}
			done := make(chan error, 1)
			started := time.Now()
			go func() { _, _, _, err := e.Execute(ctx, testClaim(), json.RawMessage(`{}`), timeout); done <- err }()
			var pid int
			for deadline := time.Now().Add(1500 * time.Millisecond); time.Now().Before(deadline); {
				if data, err := os.ReadFile(pidFile); err == nil {
					pid, _ = strconv.Atoi(string(data))
					if pid > 0 {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				cancel()
				<-done
				t.Fatal("owned descendant did not start")
			}
			t.Cleanup(func() {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			})
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				want := context.Canceled
				if mode == "deadline" {
					want = context.DeadlineExceeded
				}
				if mode == "leader-exit" {
					want = exec.ErrWaitDelay
				}
				if !errors.Is(err, want) {
					t.Fatalf("expected %v, got %v", want, err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("executor blocked on descendant's pipe")
			}
			if time.Since(started) > timeout+2*time.Second {
				t.Fatal("execution exceeded bounded cleanup allowance")
			}
			before, err := os.ReadFile(heartbeat)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
			after, err := os.ReadFile(heartbeat)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("owned descendant continued after executor returned")
			}
		})
	}
}
