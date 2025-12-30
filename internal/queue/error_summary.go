package queue

import (
	"encoding/json"
	"fmt"
)

const maxLastErrorLen = 1024

func summarizeError(errorObj json.RawMessage) string {
	if len(errorObj) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(errorObj, &payload); err == nil {
		if msg, ok := payload["message"]; ok {
			return truncateString(fmt.Sprintf("%v", msg), maxLastErrorLen)
		}
		if kind, ok := payload["kind"]; ok {
			return truncateString(fmt.Sprintf("%v", kind), maxLastErrorLen)
		}
	}

	return truncateString(string(errorObj), maxLastErrorLen)
}

func truncateString(value string, maxLen int) string {
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}
