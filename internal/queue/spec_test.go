package queue

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func canonicalFixtureTask(t *testing.T) *TaskRun {
	t.Helper()
	canonical := readContractFile(t, "spec_hash.json")
	path, lock, key := "contracts.example_task", "user:123", "account:42"
	digest := sha256.Sum256([]byte(canonical))
	return &TaskRun{SpecVersion: 1, CanonicalSpecText: &canonical, SpecHash: hex.EncodeToString(digest[:]),
		SpecJSON: json.RawMessage(canonical), TaskPath: &path, BackendAlias: "default", QueueName: "default", Priority: 5, LockKey: &lock,
		ConcurrencyKey: &key, ConcurrencyLimit: 2, MaxAttempts: 3, TimeoutSeconds: 900}
}

func TestExecutionUsesStoredCanonicalBytesAndRefusesLegacyWithoutRewritingHistory(t *testing.T) {
	task := canonicalFixtureTask(t)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, task.SpecJSON, "", "  "); err != nil {
		t.Fatal(err)
	}
	task.SpecJSON = pretty.Bytes()
	actual, err := task.ExecutionPayload(1024 * 1024)
	if err != nil || !bytes.Equal(actual, []byte(*task.CanonicalSpecText)) {
		t.Fatalf("canonical bytes changed: %v", err)
	}
	task.SpecVersion = 0
	task.SpecHash = "unmodified_historical_hash"
	task.CanonicalSpecText = nil
	actual, err = task.ExecutionPayload(1024 * 1024)
	if !errors.Is(err, ErrLegacySpec) || actual != nil || !bytes.Equal(task.SpecJSON, pretty.Bytes()) || task.SpecHash != "unmodified_historical_hash" {
		t.Fatalf("legacy execution was accepted or historical identity changed: %v", err)
	}
}

func TestCanonicalSpecRejectsHashPayloadAndExecutionProjectionDrift(t *testing.T) {
	for name, mutate := range map[string]func(*TaskRun){
		"hash":           func(task *TaskRun) { task.SpecHash = strings.Repeat("0", 64) },
		"payload":        func(task *TaskRun) { task.SpecJSON = json.RawMessage(`{"task_path":"forbidden.task"}`) },
		"path":           func(task *TaskRun) { path := "forbidden.task"; task.TaskPath = &path },
		"queue":          func(task *TaskRun) { task.QueueName = "unlimited" },
		"lock":           func(task *TaskRun) { task.LockKey = nil },
		"concurrency":    func(task *TaskRun) { task.ConcurrencyLimit = 0 },
		"timeout":        func(task *TaskRun) { task.TimeoutSeconds = 1 },
		"budget":         func(task *TaskRun) { task.MaxAttempts = 100 },
		"future_version": func(task *TaskRun) { task.SpecVersion = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			task := canonicalFixtureTask(t)
			mutate(task)
			if _, err := task.ExecutionPayload(1024 * 1024); err == nil {
				t.Fatal("inconsistent task accepted")
			}
		})
	}
}

func TestJSONSemanticComparisonPreservesExactNumbersWithoutExpandingExponents(t *testing.T) {
	for _, test := range []struct {
		left, right string
		equal       bool
	}{
		{`{"n":1e20}`, `{"n":100000000000000000000.00}`, true},
		{`{"n":9007199254740993}`, `{"n":9007199254740992}`, false},
		{`{"n":-0e999999999999999999999}`, `{"n":0}`, true},
		{`{"n":1e999999999999999999999}`, `{"n":10e999999999999999999998}`, true},
		{`{"n":1e999999999999999999999}`, `{"n":1e999999999999999999998}`, false},
		{`{"n":"0"}`, `{"n":0}`, false},
	} {
		left, err := readSpecJSON([]byte(test.left))
		if err != nil {
			t.Fatal(err)
		}
		right, err := readSpecJSON([]byte(test.right))
		if err != nil {
			t.Fatal(err)
		}
		if equalJSON(left, right) != test.equal {
			t.Fatalf("incorrect exact JSON equality: %s %s", test.left, test.right)
		}
	}
	for _, raw := range []string{`{"task_path":"allowed","task_path":"forbidden"}`, `{"a":{"n":1,"n":2}}`, `{} {}`, strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)} {
		if _, err := readSpecJSON([]byte(raw)); err == nil {
			t.Fatal("ambiguous or unbounded JSON accepted")
		}
	}
}
