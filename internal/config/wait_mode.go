package config

import (
	"errors"
	"strings"
)

// WaitMode selects optional hints; SQL claims remain authoritative in both modes.
type WaitMode string

const (
	WaitPoll   WaitMode = "poll"
	WaitNotify WaitMode = "notify"
)

func (m *WaitMode) String() string { return string(*m) }

func (m *WaitMode) Set(value string) error {
	switch normalized := WaitMode(strings.ToLower(strings.TrimSpace(value))); normalized {
	case WaitPoll, WaitNotify:
		*m = normalized
		return nil
	default:
		return errors.New("wait mode must be poll or notify; notify requires a PostgreSQL session endpoint")
	}
}
