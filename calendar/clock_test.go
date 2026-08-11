package calendar_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/calendar"
)

// start is an instant with a time of day, because keeping one is a property of
// the clock worth testing rather than an accident of the anchor.
var start = time.Date(2026, time.September, 17, 9, 0, 0, 0, time.UTC)

// TestAnEphemeralClockForgetsItsTimeline is the bargain the ephemeral store
// makes, and the reason `go test ./...` needs no setup: with nowhere to write
// the date, every process begins at the anchor.
func TestAnEphemeralClockForgetsItsTimeline(t *testing.T) {
	c, err := calendar.OpenClock("", start)
	if err != nil {
		t.Fatalf("OpenClock: %v", err)
	}
	if got := c.Now(); !got.Equal(start) {
		t.Errorf("Now = %s, want %s", got, start)
	}
	if _, err := c.Advance(); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	restarted, err := calendar.OpenClock("", start)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if got := restarted.Now(); !got.Equal(start) {
		t.Errorf("a restart is at %s, want %s", got, start)
	}
}

func TestAdvanceMovesOneCalendarDayAndKeepsTheTimeOfDay(t *testing.T) {
	c := calendar.NewClock(start)

	got, err := c.Advance()
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	want := time.Date(2026, time.September, 18, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Advance = %s, want %s", got, want)
	}
	if now := c.Now(); !now.Equal(got) {
		t.Errorf("Now = %s, want %s", now, got)
	}
}

// TestAdvanceLandsOnTheWeekendRatherThanSkippingIt is the division of labour the
// package doc states: the clock lives through every day and the calendar says
// which of them clear.
func TestAdvanceLandsOnTheWeekendRatherThanSkippingIt(t *testing.T) {
	friday := time.Date(2026, time.September, 18, 9, 0, 0, 0, time.UTC)
	c := calendar.NewClock(friday)

	saturday, err := c.Advance()
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if saturday.Weekday() != time.Saturday {
		t.Fatalf("Advance from a Friday = %s, a %s", day(saturday), saturday.Weekday())
	}
	if calendar.IsSettlementDay(saturday) {
		t.Errorf("%s: a settlement day", day(saturday))
	}
	if next := calendar.NextSettlementDay(saturday); !next.Equal(date(2026, time.September, 21)) {
		t.Errorf("NextSettlementDay = %s, want the Monday", day(next))
	}
}

// TestARestartedDeploymentResumesRatherThanRewinding opens a real directory,
// because the timeline surviving a process is the whole point of persisting it
// and an in-memory clock cannot be asked the question.
func TestARestartedDeploymentResumesRatherThanRewinding(t *testing.T) {
	dir := t.TempDir()

	first, err := calendar.OpenClock(dir, start)
	if err != nil {
		t.Fatalf("OpenClock: %v", err)
	}
	for range 3 {
		if _, err := first.Advance(); err != nil {
			t.Fatalf("Advance: %v", err)
		}
	}

	second, err := calendar.OpenClock(dir, start)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	want := start.AddDate(0, 0, 3)
	if got := second.Now(); !got.Equal(want) {
		t.Errorf("a restarted deployment is at %s, want %s", got, want)
	}
}

func TestAnEmptyDirectoryStartsAtTheAnchor(t *testing.T) {
	c, err := calendar.OpenClock(t.TempDir(), start)
	if err != nil {
		t.Fatalf("OpenClock: %v", err)
	}
	if got := c.Now(); !got.Equal(start) {
		t.Errorf("Now = %s, want %s", got, start)
	}
}

func TestTheClockDirectoryIsCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "databases")

	c, err := calendar.OpenClock(dir, start)
	if err != nil {
		t.Fatalf("OpenClock: %v", err)
	}
	if _, err := c.Advance(); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory was not created: %v", err)
	}
}

// TestTheClockFileIsNotMistakenForADatabase guards the one collision the
// directory has: sqlite.OpenSet reads the set of member banks off it.
func TestTheClockFileIsNotMistakenForADatabase(t *testing.T) {
	dir := t.TempDir()

	c, err := calendar.OpenClock(dir, start)
	if err != nil {
		t.Fatalf("OpenClock: %v", err)
	}
	if _, err := c.Advance(); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") {
			t.Errorf("the clock left %s, which reads as an institution's database", e.Name())
		}
	}
}

// TestADateThatCannotBeReadStopsStartup pins the refusal: resetting to the
// anchor would put the clock behind books that already hold later entries.
func TestADateThatCannotBeReadStopsStartup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "business-date"), []byte("last Tuesday\n"), 0o644); err != nil {
		t.Fatalf("writing the clock file: %v", err)
	}

	if _, err := calendar.OpenClock(dir, start); err == nil {
		t.Error("OpenClock accepted a file that holds no date")
	}
}

func TestRewindReplaysTheSameTimeline(t *testing.T) {
	dir := t.TempDir()

	c, err := calendar.OpenClock(dir, start)
	if err != nil {
		t.Fatalf("OpenClock: %v", err)
	}
	for range 5 {
		if _, err := c.Advance(); err != nil {
			t.Fatalf("Advance: %v", err)
		}
	}
	if err := c.Rewind(start); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if got := c.Now(); !got.Equal(start) {
		t.Errorf("Now = %s, want %s", got, start)
	}

	// The rewind is recorded, so the next process replays the timeline too
	// rather than resuming the one that was abandoned.
	reopened, err := calendar.OpenClock(dir, start.AddDate(1, 0, 0))
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if got := reopened.Now(); !got.Equal(start) {
		t.Errorf("a restart after a rewind is at %s, want %s", got, start)
	}
}
