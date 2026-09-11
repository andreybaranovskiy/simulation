package runner

import (
	"runtime"
	"sync"
)

// EventType names what happened to a run.
type EventType string

const (
	EventQueued   EventType = "queued"
	EventStarted  EventType = "started"
	EventProgress EventType = "progress"
	EventFinished EventType = "finished"
)

// Event is one update about a run, delivered to whoever is watching.
type Event struct {
	Type       EventType `json:"type"`
	RunID      string    `json:"runId"`
	ScenarioID string    `json:"scenarioId,omitempty"`
	ProjectID  string    `json:"projectId,omitempty"`
	Status     string    `json:"status,omitempty"`

	Progress float64 `json:"progress,omitempty"`
	SimTime  float64 `json:"simTime,omitempty"`
	Entities uint64  `json:"entities,omitempty"`
	Records  uint64  `json:"records,omitempty"`

	Error string `json:"error,omitempty"`
}

// Broker fans run events out to subscribers.
//
// Publishing never blocks. A simulation must not be slowed, or worse stalled,
// because a browser tab stopped reading its progress stream; a subscriber that
// cannot keep up loses updates instead. Progress events are snapshots of the
// same value, so a dropped one costs nothing. Finished events matter more, and
// the API guards against a missed one by reading the run row when a stream
// opens and again when it closes.
type Broker struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]*subscription
	closed bool
}

type subscription struct {
	ch chan Event
	// projectID narrows a subscription to one project, so a user watching one
	// project is not woken by every run on the server.
	projectID string
}

func NewBroker() *Broker {
	return &Broker{subs: make(map[int]*subscription)}
}

// Subscribe returns a channel of events and a function to stop listening.
// An empty projectID receives everything.
func (b *Broker) Subscribe(projectID string) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++

	// The buffer absorbs a burst from several runs reporting at once without
	// dropping anything a subscriber would notice.
	sub := &subscription{ch: make(chan Event, 64), projectID: projectID}
	b.subs[id] = sub

	return sub.ch, func() { b.unsubscribe(id) }
}

func (b *Broker) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub, ok := b.subs[id]
	if !ok {
		return
	}
	delete(b.subs, id)
	close(sub.ch)
}

// Publish delivers an event to every matching subscriber, dropping it for any
// whose buffer is full.
func (b *Broker) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}

	for _, sub := range b.subs {
		if sub.projectID != "" && e.ProjectID != "" && sub.projectID != e.ProjectID {
			continue
		}

		select {
		case sub.ch <- e:
		default:
			// This subscriber is behind. Dropping is the right answer: the
			// alternative is stalling a simulation to wait for a browser.
		}
	}
}

// Subscribers reports how many streams are open, for the health endpoint.
func (b *Broker) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Close ends every subscription.
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true

	for id, sub := range b.subs {
		delete(b.subs, id)
		close(sub.ch)
	}
}

// exeSuffix is the platform's executable extension, used to find simrunner.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
