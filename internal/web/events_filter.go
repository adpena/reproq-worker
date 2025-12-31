package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"reproq-worker/internal/events"
)

type eventFilter struct {
	queue    string
	workerID string
	taskID   *int64
}

func parseEventFilter(r *http.Request) (eventFilter, error) {
	query := r.URL.Query()
	filter := eventFilter{
		queue:    strings.TrimSpace(query.Get("queue")),
		workerID: strings.TrimSpace(query.Get("worker_id")),
	}
	if val := strings.TrimSpace(query.Get("task_id")); val != "" {
		parsed, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return eventFilter{}, fmt.Errorf("invalid task_id")
		}
		filter.taskID = &parsed
	}
	return filter, nil
}

func (f eventFilter) Matches(event events.Event) bool {
	if f.queue != "" && event.Queue != f.queue {
		return false
	}
	if f.workerID != "" && event.WorkerID != f.workerID {
		return false
	}
	if f.taskID != nil && event.TaskID != *f.taskID {
		return false
	}
	return true
}
