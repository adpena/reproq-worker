package executor

import (
	"fmt"
	"strings"
)

// Validator enforces security policies on task execution.
type Validator struct {
	AllowedModules []string
}

func NewValidator(allowed []string) *Validator {
	if len(allowed) == 0 {
		// Default safe baseline: only allow tasks from myapp.tasks or generic tasks package
		allowed = []string{"myapp.tasks.", "tasks."}
	}
	return &Validator{AllowedModules: allowed}
}

// Validate checks if the task module path is authorized.
func (v *Validator) Validate(taskPath string) error {
	for _, m := range v.AllowedModules {
		if m == "*" {
			return nil
		}
		if strings.HasPrefix(taskPath, m) {
			return nil
		}
	}
	return fmt.Errorf("security violation: unauthorized task module path: %s", taskPath)
}
