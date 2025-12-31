package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"reproq-worker/internal/events"
)

func TestEventFilterMatches(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/events?queue=default&worker_id=w1&task_id=42", nil)
	filter, err := parseEventFilter(req)
	if err != nil {
		t.Fatalf("parse filter: %v", err)
	}
	event := events.Event{
		Queue:    "default",
		WorkerID: "w1",
		TaskID:   42,
	}
	if !filter.Matches(event) {
		t.Fatalf("expected filter to match")
	}
	if filter.Matches(events.Event{Queue: "other", WorkerID: "w1", TaskID: 42}) {
		t.Fatalf("expected queue mismatch to fail")
	}
	if filter.Matches(events.Event{Queue: "default", WorkerID: "w2", TaskID: 42}) {
		t.Fatalf("expected worker mismatch to fail")
	}
	if filter.Matches(events.Event{Queue: "default", WorkerID: "w1", TaskID: 7}) {
		t.Fatalf("expected task mismatch to fail")
	}
}

func TestEventFilterInvalidTaskID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/events?task_id=not-a-number", nil)
	if _, err := parseEventFilter(req); err == nil {
		t.Fatalf("expected error for invalid task_id")
	}
}
