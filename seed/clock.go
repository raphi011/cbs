package seed

import (
	"sync"
	"time"
)

// clock is a controllable time source for deterministic seeding. While frozen
// it returns a fixed instant that the seed builder advances step by step, which
// keeps generated IDs, booking dates and value dates reproducible. Once live it
// delegates to time.Now, so any operation performed after seeding (for example
// via the API after a reset) is timestamped in real time.
type clock struct {
	mu   sync.Mutex
	t    time.Time
	live bool
}

// newClock returns a clock frozen at start.
func newClock(start time.Time) *clock { return &clock{t: start} }

// now returns the current time: the frozen instant while building, or the real
// wall-clock time once goLive has been called.
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		return time.Now()
	}
	return c.t
}

// advance moves the frozen clock forward. It has no effect once live.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		return
	}
	c.t = c.t.Add(d)
}

// goLive switches the clock to real wall-clock time.
func (c *clock) goLive() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live = true
}

// rewind refreezes the clock at start.
//
// Reseeding after a store reset has to replay the same timeline, or the sample
// data's booking dates, value dates and snapshot dates would drift a little
// further from baseDate with every reset — and the dataset would stop being the
// fixed reference the tests and the UI describe.
func (c *clock) rewind(start time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = start
	c.live = false
}
