package queue

import (
	"encoding/json"
	"testing"
)

func TestParsePeriodicPayloadEnvelope(t *testing.T) {
	raw := json.RawMessage(`{"args":[1,"a"],"kwargs":{"k":"v"}}`)
	argsRaw, kwargsRaw, err := parsePeriodicPayload(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var args []any
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		t.Fatalf("args unmarshal: %v", err)
	}
	if len(args) != 2 || args[0].(float64) != 1 || args[1].(string) != "a" {
		t.Fatalf("unexpected args: %#v", args)
	}
	var kwargs map[string]any
	if err := json.Unmarshal(kwargsRaw, &kwargs); err != nil {
		t.Fatalf("kwargs unmarshal: %v", err)
	}
	if kwargs["k"].(string) != "v" {
		t.Fatalf("unexpected kwargs: %#v", kwargs)
	}
}

func TestParsePeriodicPayloadArgsOnly(t *testing.T) {
	raw := json.RawMessage(`{"args":[true]}`)
	argsRaw, kwargsRaw, err := parsePeriodicPayload(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var args []any
	_ = json.Unmarshal(argsRaw, &args)
	if len(args) != 1 || args[0].(bool) != true {
		t.Fatalf("unexpected args: %#v", args)
	}
	var kwargs map[string]any
	_ = json.Unmarshal(kwargsRaw, &kwargs)
	if len(kwargs) != 0 {
		t.Fatalf("expected empty kwargs, got: %#v", kwargs)
	}
}

func TestParsePeriodicPayloadKwargsOnly(t *testing.T) {
	raw := json.RawMessage(`{"kwargs":{"a":2}}`)
	argsRaw, kwargsRaw, err := parsePeriodicPayload(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var args []any
	_ = json.Unmarshal(argsRaw, &args)
	if len(args) != 0 {
		t.Fatalf("expected empty args, got: %#v", args)
	}
	var kwargs map[string]any
	_ = json.Unmarshal(kwargsRaw, &kwargs)
	if kwargs["a"].(float64) != 2 {
		t.Fatalf("unexpected kwargs: %#v", kwargs)
	}
}

func TestParsePeriodicPayloadArray(t *testing.T) {
	raw := json.RawMessage(`["x", 3]`)
	argsRaw, kwargsRaw, err := parsePeriodicPayload(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var args []any
	_ = json.Unmarshal(argsRaw, &args)
	if len(args) != 2 || args[0].(string) != "x" {
		t.Fatalf("unexpected args: %#v", args)
	}
	var kwargs map[string]any
	_ = json.Unmarshal(kwargsRaw, &kwargs)
	if len(kwargs) != 0 {
		t.Fatalf("expected empty kwargs, got: %#v", kwargs)
	}
}

func TestParsePeriodicPayloadObject(t *testing.T) {
	raw := json.RawMessage(`{"flag":true}`)
	argsRaw, kwargsRaw, err := parsePeriodicPayload(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var args []any
	_ = json.Unmarshal(argsRaw, &args)
	if len(args) != 0 {
		t.Fatalf("expected empty args, got: %#v", args)
	}
	var kwargs map[string]any
	_ = json.Unmarshal(kwargsRaw, &kwargs)
	if kwargs["flag"].(bool) != true {
		t.Fatalf("unexpected kwargs: %#v", kwargs)
	}
}

func TestParsePeriodicPayloadInvalid(t *testing.T) {
	_, _, err := parsePeriodicPayload(json.RawMessage(`"nope"`))
	if err == nil {
		t.Fatal("expected error for invalid payload")
	}
}

func TestPeriodicSpecJSONKeys(t *testing.T) {
	key := "user:1"
	spec := periodicSpec{
		TaskPath:         "app.tasks.sample",
		Args:             json.RawMessage(`[1]`),
		Kwargs:           json.RawMessage(`{"k":"v"}`),
		QueueName:        "default",
		Priority:         2,
		ConcurrencyKey:   &key,
		ConcurrencyLimit: 3,
		PeriodicName:     "nightly",
		ScheduledAt:      "2025-01-01T00:00:00Z",
		Exec: execSpec{
			TimeoutSeconds: 900,
			MaxAttempts:    4,
		},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	keys := []string{
		"task_path",
		"args",
		"kwargs",
		"queue_name",
		"priority",
		"concurrency_key",
		"concurrency_limit",
		"periodic_name",
		"scheduled_at",
		"exec",
	}
	for _, k := range keys {
		if _, ok := payload[k]; !ok {
			t.Fatalf("missing key %q in spec payload", k)
		}
	}
}
