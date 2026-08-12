package calendar

import (
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

// Clock is the deployment's business date. Every institution reads it, no
// institution owns it, and advancing it is what makes a business day happen —
// see the package doc for why it is in none of the N+2 databases.
type Clock struct {
	mu  sync.Mutex
	now time.Time

	// path is the file the date is written to, or "" for a clock that keeps it
	// in memory and forgets it on exit.
	path string
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

	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("calendar: %s does not hold a business date: %w", path, err)
	}
	return &Clock{now: t.UTC(), path: path}, nil
}

// Now is the instant the deployment is at.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock on by one calendar day, keeping the time of day, and
// returns where it landed. A weekend or a holiday is advanced onto like any
// other day; IsSettlementDay is what decides whether anything clears on it.
func (c *Clock) Advance() (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	next := c.now.AddDate(0, 0, 1)
	if err := c.write(next); err != nil {
		return c.now, err
	}
	c.now = next
	return c.now, nil
}

// Rewind refreezes the clock at start.
func (c *Clock) Rewind(start time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.write(start.UTC()); err != nil {
		return err
	}
	c.now = start.UTC()
	return nil
}

// write records t, and is a no-op on an ephemeral clock. It writes a temporary
// file and renames it over the real one, so a process killed mid-write leaves
// the previous date behind rather than half of one.
func (c *Clock) write(t time.Time) error {
	if c.path == "" {
		return nil
	}

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(t.UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
		return fmt.Errorf("calendar: writing the business date to %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("calendar: recording the business date at %s: %w", c.path, err)
	}
	return nil
}
