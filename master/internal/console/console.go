// Package console fans browser console sessions out to instance output.
package console

import (
	"sync"
)

// backlogCap bounds the retained output per instance (bytes).
const backlogCap = 64 * 1024

// Hub tracks console subscribers per instance.
type Hub struct {
	mu      sync.RWMutex
	subs    map[int64]map[chan string]struct{}
	backlog map[int64]*ring

	// Attach/Detach manage the master->agent console subscription. Optional.
	Attach func(instanceID int64)
	Detach func(instanceID int64)
}

// NewHub creates an empty console hub.
func NewHub() *Hub {
	return &Hub{
		subs:    map[int64]map[chan string]struct{}{},
		backlog: map[int64]*ring{},
	}
}

// Subscribe registers a subscriber and returns its channel plus the recent
// backlog snapshot.
func (h *Hub) Subscribe(instanceID int64) (<-chan string, string, func()) {
	ch := make(chan string, 256)

	h.mu.Lock()
	first := false
	set, ok := h.subs[instanceID]
	if !ok {
		set = map[chan string]struct{}{}
		h.subs[instanceID] = set
	}
	if len(set) == 0 {
		first = true
	}
	set[ch] = struct{}{}
	snapshot := ""
	if r, ok := h.backlog[instanceID]; ok {
		snapshot = r.snapshot()
	}
	attach := first && h.Attach != nil
	h.mu.Unlock()

	if attach {
		h.Attach(instanceID)
	}

	cancel := func() {
		h.mu.Lock()
		set, ok := h.subs[instanceID]
		if !ok {
			h.mu.Unlock()
			return
		}
		if _, ok := set[ch]; !ok {
			h.mu.Unlock()
			return
		}
		delete(set, ch)
		last := len(set) == 0
		if last {
			delete(h.subs, instanceID)
		}
		detach := last && h.Detach != nil
		h.mu.Unlock()
		if detach {
			h.Detach(instanceID)
		}
	}
	return ch, snapshot, cancel
}

// Broadcast appends data to the backlog and fans it out. Slow subscribers
// drop their unread data rather than blocking the instance.
func (h *Hub) Broadcast(instanceID int64, data string) {
	h.mu.Lock()
	r, ok := h.backlog[instanceID]
	if !ok {
		r = newRing(backlogCap)
		h.backlog[instanceID] = r
	}
	r.append(data)
	subs := make([]chan string, 0, len(h.subs[instanceID]))
	for ch := range h.subs[instanceID] {
		subs = append(subs, ch)
	}
	h.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- data:
		default: // drop for a lagging subscriber
		}
	}
}

// DropBacklog clears retained output for an instance (e.g. on delete).
func (h *Hub) DropBacklog(instanceID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.backlog, instanceID)
}

// ring is a fixed-capacity buffer keeping the tail.
type ring struct {
	buf []byte
	cap int
}

func newRing(capacity int) *ring { return &ring{cap: capacity} }

func (r *ring) append(s string) {
	r.buf = append(r.buf, s...)
	if len(r.buf) > r.cap {
		r.buf = r.buf[len(r.buf)-r.cap:]
	}
}

func (r *ring) snapshot() string {
	return string(r.buf)
}
