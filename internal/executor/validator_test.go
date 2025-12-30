package executor

import (
	"testing"
)

func TestValidator(t *testing.T) {
	v := NewValidator([]string{"myapp.tasks.", "shared."})

	tests := []struct {
		path    string
		wantErr bool
	}{
		{"myapp.tasks.send_email", false},
		{"shared.cleanup", false},
		{"other.malicious_task", true},
		{"myapp.tasks_bypass", true}, // Prefix check safety
	}

	for _, tt := range tests {
		err := v.Validate(tt.path)
		if (err != nil) != tt.wantErr {
			t.Errorf("Validate(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
		}
	}
}

func TestValidatorAllowAllWildcard(t *testing.T) {
	v := NewValidator([]string{"*"})
	paths := []string{
		"accounts.tasks.notify_deploy_success_task",
		"any.module.path",
	}
	for _, path := range paths {
		if err := v.Validate(path); err != nil {
			t.Fatalf("expected wildcard to allow %q, got %v", path, err)
		}
	}
}
