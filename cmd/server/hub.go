package main

import (
	"sync"

	"github.com/raphi011/cbs/api"
)

// Every browser watching this deployment work. It is the journal's second
// SUBSCRIBER and never a second consumer — take empties the journal and this
// does not — so a report and a broadcast never take events from each other.

// watcherBuffer is how far behind a watcher may fall before it is dropped. A
// dropped watcher's channel closes, its browser reconnects and reads the mesh
// again, which is a whole picture rather than a stale one.
const watcherBuffer = 256

type hub struct {
	mu       sync.Mutex
	next     int64
	watchers map[int64]chan api.StreamEvent
}

// watch adds a watcher and hands back the channel it reads and the release that
// removes it.
func (h *hub) watch() (<-chan api.StreamEvent, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.watchers == nil {
		h.watchers = map[int64]chan api.StreamEvent{}
	}
	h.next++
	id, ch := h.next, make(chan api.StreamEvent, watcherBuffer)
	h.watchers[id] = ch
	return ch, func() { h.drop(id) }
}

// drop removes one watcher. Dropping one twice is dropping it once, which is
// what lets a lagging watcher be dropped here and released by its own handler.
func (h *hub) drop(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, held := h.watchers[id]; held {
		delete(h.watchers, id)
		close(ch)
	}
}

// publish tells every watcher, and drops the ones that cannot keep up rather
// than blocking the institution that is reporting. Nothing an institution does
// may wait on a browser.
func (h *hub) publish(e api.StreamEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.watchers {
		select {
		case ch <- e:
		default:
			delete(h.watchers, id)
			close(ch)
		}
	}
}

// watching is how many watchers this hub holds.
func (h *hub) watching() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.watchers)
}
