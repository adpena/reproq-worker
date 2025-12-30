package logging

import (
	"log/slog"
	"testing"
)

func TestShouldRedactKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{key: "payload_json", want: true},
		{key: "Spec_JSON", want: true},
		{key: "authorization", want: true},
		{key: "api_token", want: true},
		{key: "password", want: true},
		{key: "spec_hash", want: false},
		{key: "task_path", want: false},
	}

	for _, tt := range tests {
		if got := shouldRedactKey(tt.key); got != tt.want {
			t.Fatalf("expected shouldRedactKey(%q)=%v, got %v", tt.key, tt.want, got)
		}
	}
}

func TestRedactAttrGroups(t *testing.T) {
	attr := slog.Group("task", slog.String("payload_json", "secret"), slog.String("task_path", "safe"))
	redacted := redactAttr(attr)

	group := redacted.Value.Group()
	if len(group) != 2 {
		t.Fatalf("expected 2 group attrs, got %d", len(group))
	}

	if group[0].Value.String() != redactedValue {
		t.Fatalf("expected payload_json to be redacted, got %q", group[0].Value.String())
	}
	if group[1].Value.String() != "safe" {
		t.Fatalf("expected task_path to stay, got %q", group[1].Value.String())
	}
}
