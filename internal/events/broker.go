package events

import (
	"sync"
	"time"
)

const (
	defaultBufferSize       = 200
	defaultSubscriberBuffer = 50
)

type Event struct {
	Timestamp time.Time         `json:"ts"`
	Level     string            `json:"level"`
	Type      string            `json:"type"`
	Message   string            `json:"msg"`
	Queue     string            `json:"queue,omitempty"`
	TaskID    int64             `json:"task_id,omitempty"`
	WorkerID  string            `json:"worker_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Publisher interface {
	Publish(Event)
}

type NoopPublisher struct{}

func (NoopPublisher) Publish(Event) {}

type Broker struct {
	mu        sync.RWMutex
	subs      map[int]chan Event
	nextID    int
	buffer    []Event
	bufferCap int
}

func NewBroker(bufferSize int) *Broker {
	if bufferSize <= 0 {
		bufferSize = defaultBufferSize
	}
	return &Broker{
		subs:      map[int]chan Event{},
		bufferCap: bufferSize,
	}
}

func (b *Broker) Publish(event Event) {
	if b == nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	b.mu.Lock()
	if b.bufferCap > 0 {
		if len(b.buffer) < b.bufferCap {
			b.buffer = append(b.buffer, event)
		} else {
			copy(b.buffer, b.buffer[1:])
			b.buffer[len(b.buffer)-1] = event
		}
	}
	subs := make([]chan Event, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- event:
		default:
		}
	}
}

func (b *Broker) Subscribe() (<-chan Event, func(), []Event) {
	if b == nil {
		return nil, func() {}, nil
	}
	ch := make(chan Event, defaultSubscriberBuffer)
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	snapshot := append([]Event(nil), b.buffer...)
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
	return ch, cancel, snapshot
}
