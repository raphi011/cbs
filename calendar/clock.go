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
//
// It carries no .db suffix, which is what keeps sqlite.OpenSet from taking it
// for an institution: that function reads the set of member banks off the
// directory listing, and a bank is a *.db whose stem is a BIC.
const clockFile = "business-date"

// Clock is the deployment's business date. Every institution reads it, no
// institution owns it, and advancing it is what makes a business day happen —
// see the package doc for why it is in none of the N+2 databases.
//
// Its Now method is the clock func() time.Time that ledger.Book,
// deposit.Register, payment.Network and sqlite.Set each take, so a deployment
// built over one of these has exactly one time source and time.Now appears
// nowhere below it.
//
// It is safe for concurrent use because it has to be: an operator advances the
// day on one connection while requests on N+1 others are reading the date.
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

// OpenClock is the clock of the deployment whose databases live in dir: the date
// the last process left, or start if dir holds none yet. An empty dir is
// NewClock, because there is nowhere to put it.
//
// A file that cannot be read as a date stops startup rather than resetting to
// start. Silently rewinding would put the clock behind books that already hold
// entries dated later, and every accrual, ageing report and value date after
// that would be computed against a day that has already been lived through.
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
//
// A persistence failure leaves the clock where it was, so a caller that gets an
// error has not half-advanced a day.
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
//
// A store reset reseeds, and reseeding has to replay the same timeline: without
// this the sample data's booking dates, value dates and snapshot dates would
// drift a little further from their anchor with every reset, and the dataset
// would stop being the fixed reference the tests and the UI describe.
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
