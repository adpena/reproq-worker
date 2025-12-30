package web

import (
	"sync"
	"time"
)

const (
	DefaultAuthLimit      = 30
	DefaultAuthWindow     = time.Minute
	DefaultAuthMaxEntries = 1000
)

type authLimiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	maxEntries  int
	entries     map[string]*authEntry
	lastCleanup time.Time
}

type authEntry struct {
	count       int
	windowStart time.Time
	lastSeen    time.Time
}

func newAuthLimiter(limit int, window time.Duration, maxEntries int) *authLimiter {
	if limit <= 0 {
		limit = DefaultAuthLimit
	}
	if window <= 0 {
		window = DefaultAuthWindow
	}
	if maxEntries <= 0 {
		maxEntries = DefaultAuthMaxEntries
	}
	return &authLimiter{
		limit:      limit,
		window:     window,
		maxEntries: maxEntries,
		entries:    make(map[string]*authEntry),
	}
}

func (l *authLimiter) allow(key string, now time.Time) bool {
	if l == nil {
		return true
	}
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.shouldCleanup(now) {
		l.cleanup(now)
	}

	entry := l.entries[key]
	if entry == nil {
		entry = &authEntry{
			windowStart: now,
			lastSeen:    now,
		}
		l.entries[key] = entry
	}

	if now.Sub(entry.windowStart) >= l.window {
		entry.count = 0
		entry.windowStart = now
	}

	entry.lastSeen = now
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	return true
}

func (l *authLimiter) shouldCleanup(now time.Time) bool {
	if len(l.entries) > l.maxEntries {
		return true
	}
	if l.lastCleanup.IsZero() {
		return true
	}
	return now.Sub(l.lastCleanup) >= l.window
}

func (l *authLimiter) cleanup(now time.Time) {
	staleCutoff := now.Add(-2 * l.window)
	for key, entry := range l.entries {
		if entry.lastSeen.Before(staleCutoff) {
			delete(l.entries, key)
		}
	}

	if len(l.entries) > l.maxEntries {
		excess := len(l.entries) - l.maxEntries
		for key := range l.entries {
			delete(l.entries, key)
			excess--
			if excess <= 0 {
				break
			}
		}
	}
	l.lastCleanup = now
}
