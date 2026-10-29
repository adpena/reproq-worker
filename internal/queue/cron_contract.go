package queue

import (
	"fmt"
	"strconv"
	"strings"
)

// Restrict the upstream parser to the portable five-field contract shared with
// the SDK. robfig/cron additionally accepts '?' and signed/overwide steps that
// the contract excludes. It remains responsible for names, ranges and bounds.
func portableCronExpression(expression string) (string, error) {
	fields := strings.Fields(expression)
	prefix := ""
	if len(fields) > 0 && (strings.HasPrefix(fields[0], "TZ=") || strings.HasPrefix(fields[0], "CRON_TZ=")) {
		prefix, fields = fields[0], fields[1:]
		if strings.HasSuffix(prefix, "=") {
			return "", fmt.Errorf("timezone prefix requires a named zone")
		}
	}
	if len(fields) != 5 {
		return "", fmt.Errorf("portable cron requires five fields")
	}
	widths := [...]int{60, 24, 31, 12, 7}
	for index, field := range fields {
		for _, character := range field {
			if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || strings.ContainsRune("*,-/", character) {
				continue
			}
			return "", fmt.Errorf("unsupported portable cron field syntax")
		}
		for _, item := range strings.Split(field, ",") {
			if item == "" {
				return "", fmt.Errorf("cron list components cannot be empty")
			}
			parts := strings.Split(item, "/")
			base := parts[0]
			if base != "*" && strings.Contains(base, "*") {
				return "", fmt.Errorf("wildcard cannot be used as a cron range endpoint")
			}
			if len(parts) > 2 {
				return "", fmt.Errorf("cron field has multiple steps")
			}
			if len(parts) == 2 {
				if base != "*" && !strings.Contains(base, "-") {
					return "", fmt.Errorf("cron step requires a wildcard or explicit ascending range")
				}
				step, err := strconv.Atoi(parts[1])
				if err != nil || step <= 0 || step > widths[index] {
					return "", fmt.Errorf("cron step must be positive and bounded by its field width")
				}
			}
		}
	}
	if prefix != "" {
		fields = append([]string{prefix}, fields...)
	}
	return strings.Join(fields, " "), nil
}
