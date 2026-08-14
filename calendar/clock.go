package calendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// clockFile is what the business date is written to, beside the databases.
const clockFile = "business-date"

// Clock is the deployment's business date and how far into it the deployment has
// got. Every institution reads the date, no institution owns it, and advancing
// it is what makes a business day happen — see the package doc for why it is in
// none of the N+2 databases.
type Clock struct {
	mu  sync.Mutex
	now time.Time
	// reached is an opaque marker the deployment records and this package never
	// interprets: how far into the current day it has got, empty for a day
	// nothing has run on. See docs/specs/2026-08-14-a-day-cursor-design.md.
	reached string

	// path is the file the date is written to, or "" for a clock that keeps it
	// in memory and forgets it on exit.
	path string
}

// record is the clock as one line of JSON, which is what makes the date and the
// marker one write: a process killed between two would leave them disagreeing.
type record struct {
	Instant string `json:"instant"`
	Reached string `json:"reached,omitempty"`
}

// NewClock returns an ephemeral clock frozen at start. It writes nothing, so a
// restart begins at start again — which is what `go test ./...` and a server
// with no -database want, and is the same bargain the ephemeral store makes.
func NewClock(start time.Time) *Clock {
	return &Clock{now: start.UTC()}
}

// OpenClock is the clock of the deployment whose databases live in dir: the
// date the last process left, or start if dir holds none yet. An empty dir is
// NewClock, because there is nowhere to put it.
func OpenClock(dir string, start time.Time) (*Clock, error) {
	if dir == "" {
		return NewClock(start), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("calendar: clock directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, clockFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Clock{now: start.UTC(), path: path}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("calendar: reading the business date from %s: %w", path, err)
	}

	rec, err := parse(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("calendar: %s does not hold a business date: %w", path, err)
	}
	t, err := time.Parse(time.RFC3339Nano, rec.Instant)
	if err != nil {
		return nil, fmt.Errorf("calendar: %s does not hold a business date: %w", path, err)
	}
	return &Clock{now: t.UTC(), reached: rec.Reached, path: path}, nil
}

// parse reads the file either way round. A bare RFC3339 line is a record of an
// instant nothing has run on, which is what a directory an earlier process wrote
// holds; anything else is refused rather than reset to the anchor, which would
// put the clock behind books holding later entries.
func parse(text string) (record, error) {
	if !strings.HasPrefix(text, "{") {
		return record{Instant: text}, nil
	}
	var rec record
	if err := json.Unmarshal([]byte(text), &rec); err != nil {
		return record{}, err
	}
	return rec, nil
}

// Now is the instant the deployment is at.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Reached is how far into the current day the deployment has got, as the marker
// it last recorded, and empty for a day nothing has run on.
func (c *Clock) Reached() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reached
}

// Reach records how far into the current day the deployment has got. An empty
// marker is a day nothing has run on, which is what a caller that has just
// rebuilt everything records.
func (c *Clock) Reach(marker string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.write(c.now, marker); err != nil {
		return err
	}
	c.reached = marker
	return nil
}

// Advance moves the clock on by one calendar day, keeping the time of day, and
// returns where it landed. A weekend or a holiday is advanced onto like any
// other day; IsSettlementDay is what decides whether anything clears on it.
//
// The new day has run nothing, and that is part of the same write: a date moved
// without its marker cleared would be a day already half over.
func (c *Clock) Advance() (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	next := c.now.AddDate(0, 0, 1)
	if err := c.write(next, ""); err != nil {
		return c.now, err
	}
	c.now, c.reached = next, ""
	return c.now, nil
}

// Rewind refreezes the clock at start, on a day nothing has run on.
func (c *Clock) Rewind(start time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.write(start.UTC(), ""); err != nil {
		return err
	}
	c.now, c.reached = start.UTC(), ""
	return nil
}

// write records the instant and the marker together, and is a no-op on an
// ephemeral clock. It writes a temporary file and renames it over the real one,
// so a process killed mid-write leaves the previous record behind rather than
// half of one.
func (c *Clock) write(t time.Time, marker string) error {
	if c.path == "" {
		return nil
	}

	line, err := json.Marshal(record{Instant: t.UTC().Format(time.RFC3339Nano), Reached: marker})
	if err != nil {
		return fmt.Errorf("calendar: rendering the business date: %w", err)
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(line, '\n'), 0o644); err != nil {
		return fmt.Errorf("calendar: writing the business date to %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("calendar: recording the business date at %s: %w", c.path, err)
	}
	return nil
}
