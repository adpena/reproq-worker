package queue

import "testing"

func TestTaskStatusValues(t *testing.T) {
	tests := map[string]struct {
		got  TaskStatus
		want TaskStatus
	}{
		"ready":      {got: StatusReady, want: "READY"},
		"running":    {got: StatusRunning, want: "RUNNING"},
		"waiting":    {got: StatusWaiting, want: "WAITING"},
		"successful": {got: StatusSuccessful, want: "SUCCESSFUL"},
		"failed":     {got: StatusFailed, want: "FAILED"},
	}

	for name, tt := range tests {
		if tt.got != tt.want {
			t.Fatalf("%s: expected %q, got %q", name, tt.want, tt.got)
		}
	}
}
