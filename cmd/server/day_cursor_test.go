package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/api"
)

// How far the day has got, and what that changes. The doors suite asserts what
// an operator can reach; this one asserts that the day KNOWS what was reached,
// which is what stops advancing it from running the clearing a second time.

// keysOf renders what a call ran as the lines a golden comparison reads.
func keysOf(list []phase) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, phaseKey(p.name))
	}
	return out
}

// completedOn is the phases the listing says the day has run.
func completedOn(t *testing.T, s *server) []string {
	t.Helper()
	var out []string
	for _, p := range phasesOn(t, s) {
		if p.Completed {
			out = append(out, p.Key)
		}
	}
	return out
}

// TestAdvancingADayRunsOnlyWhatWasNotSteppedIsTheWholePoint: the day is
// finished, not started again.
func TestAdvancingADayRunsOnlyWhatWasNotStepped(t *testing.T) {
	s := newServer(t, nil)

	for _, key := range []string{"publish", "refresh", "bank-cut-off", "clearing"} {
		step(t, s, key)
	}
	if got, want := completedOn(t, s),
		[]string{"publish", "refresh", "bank-cut-off", "clearing"}; !slices.Equal(got, want) {
		t.Errorf("the day has run %s, want %s", strings.Join(got, " → "), strings.Join(want, " → "))
	}

	report, err := s.dep.AdvanceDay(t.Context())
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	want := []string{
		"clearing-house-cut-off", "discharge", "settlement", "release", "collection",
		"end-of-day", "open-cycles",
	}
	if got := keysOf(report.Phases); !slices.Equal(got, want) {
		t.Errorf("advancing ran %s, want %s", strings.Join(got, " → "), strings.Join(want, " → "))
	}

	// And the day it landed on has run nothing, so the next advance is a whole day
	// again.
	if got := completedOn(t, s); len(got) != 0 {
		t.Errorf("the new day has already run %s", strings.Join(got, " → "))
	}
}

// TestNamingALaterPhaseRunsTheOnesBeforeIt is the safety rule that makes the
// door reach forward at all: settling before releasing is the one order the
// system exists to keep, so a door named ahead of the marker runs the day up to
// it rather than skipping to it.
func TestNamingALaterPhaseRunsTheOnesBeforeIt(t *testing.T) {
	s := newServer(t, nil)

	report := step(t, s, "release")
	want := []string{
		"publish", "refresh", "bank-cut-off", "clearing",
		"clearing-house-cut-off", "discharge", "settlement", "release",
	}
	if got := report.Phases; !slices.Equal(got, want) {
		t.Errorf("naming release ran %s, want %s", strings.Join(got, " → "), strings.Join(want, " → "))
	}
	if !report.Phase.Completed {
		t.Error("the day ran up to release and does not count release as behind it")
	}
	if got := completedOn(t, s); !slices.Equal(got, want) {
		t.Errorf("the day has run %s, want %s", strings.Join(got, " → "), strings.Join(want, " → "))
	}
}

// TestAPhaseTheDayHasRunIsRunAloneAndMovesNothing: the door stays open on a
// phase behind the marker, and asking twice runs it twice — but a marker that
// moved BACK would put the phases between it and the day's edge up for a second
// run, which is the skip this rule exists to refuse.
func TestAPhaseTheDayHasRunIsRunAloneAndMovesNothing(t *testing.T) {
	s := newServer(t, nil)
	step(t, s, "clearing")

	report := step(t, s, "publish")
	if got, want := report.Phases, []string{"publish"}; !slices.Equal(got, want) {
		t.Errorf("re-running publish ran %s, want %s", strings.Join(got, " → "), strings.Join(want, " → "))
	}
	if !report.Phase.Completed {
		t.Error("publish is behind the marker and the report says the day has not run it")
	}

	want := []string{"publish", "refresh", "bank-cut-off", "clearing"}
	if got := completedOn(t, s); !slices.Equal(got, want) {
		t.Errorf("the day has run %s, want %s", strings.Join(got, " → "), strings.Join(want, " → "))
	}
	day, err := s.dep.AdvanceDay(t.Context())
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	if got := keysOf(day.Phases); slices.Contains(got, "clearing") {
		t.Errorf("advancing ran %s, which clears a second time", strings.Join(got, " → "))
	}
}

// TestSteppingTheWholeDayLeavesAdvancingWithOnlyTheRoll pins the far end of the
// rule. The clock is what nothing but advancing moves, so a fully stepped day is
// an advance that runs one phase: the one after the roll.
func TestSteppingTheWholeDayLeavesAdvancingWithOnlyTheRoll(t *testing.T) {
	s := newServer(t, nil)

	for _, p := range phasesOn(t, s) {
		if p.AfterClock {
			continue
		}
		step(t, s, p.Key)
	}

	report, err := s.dep.AdvanceDay(t.Context())
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	if got, want := keysOf(report.Phases), []string{"open-cycles"}; !slices.Equal(got, want) {
		t.Errorf("advancing a stepped day ran %s, want %s",
			strings.Join(got, " → "), strings.Join(want, " → "))
	}
	if report.Next.Date.Equal(report.Ran.Date) {
		t.Error("the clock did not move, and advancing is the only thing that moves it")
	}
}

// TestADayTheSchemeIsShutOnBeginsAtItsEndOfDay: what is due next is read from
// the day's EFFECTIVE sequence, so a phase the day would not run cannot be the
// one it is waiting for.
func TestADayTheSchemeIsShutOnBeginsAtItsEndOfDay(t *testing.T) {
	s := newServer(t, nil)
	for i := 0; s.dep.BusinessDate().SettlementDay; i++ {
		if i == 7 {
			t.Fatal("a week of advances reached no day the scheme is shut on")
		}
		if _, err := s.dep.AdvanceDay(t.Context()); err != nil {
			t.Fatalf("AdvanceDay: %v", err)
		}
	}

	// The clearing runs — settlementOnly is reported and not enforced — and it is
	// not progress through a day that does not declare it.
	if step(t, s, "clearing").Phase.Completed {
		t.Error("the clearing counts as progress through a day the scheme is shut on")
	}
	if !step(t, s, "end-of-day").Phase.Completed {
		t.Error("end of day is the first phase of a day nothing settles on, and stepping it was not progress")
	}

	report, err := s.dep.AdvanceDay(t.Context())
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	if got, want := keysOf(report.Phases), []string{"open-cycles"}; !slices.Equal(got, want) {
		t.Errorf("advancing ran %s, want %s", strings.Join(got, " → "), strings.Join(want, " → "))
	}
}

// TestResettingLeavesADayNothingHasRunOn. A rebuilt dataset with a marker still
// on it would be a fresh deployment whose first advance skipped the clearing.
func TestResettingLeavesADayNothingHasRunOn(t *testing.T) {
	s := newServer(t, nil)
	step(t, s, "publish")

	if err := s.dep.Reset(t.Context()); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := completedOn(t, s); len(got) != 0 {
		t.Errorf("a reset deployment has already run %s", strings.Join(got, " → "))
	}
}

// TestTheListingMarksTheDayAndNotThePhasesAfterTheRoll: open cycles runs on the
// date the roll landed on, so it is never progress through the date being left.
func TestTheListingMarksNothingAfterTheRoll(t *testing.T) {
	s := newServer(t, nil)
	for _, p := range phasesOn(t, s) {
		if p.AfterClock {
			step(t, s, p.Key)
		}
	}
	for _, p := range phasesOn(t, s) {
		if p.AfterClock && p.Completed {
			t.Errorf("%q ran after the roll and counts as progress through the day before it", p.Key)
		}
	}
}

// TestEveryPhaseThatMovesTheDayIsPushed is why the stream carries a phase at
// all: end of day moves no file, so a watcher told only about files would never
// hear that the day had got past it. This deployment holds no banks, which is
// what makes that phase move nothing whatsoever.
func TestEveryPhaseThatMovesTheDayIsPushed(t *testing.T) {
	s := newServer(t, nil)
	events, release := s.dep.hub.watch()
	defer release()

	var want []string
	for _, p := range phasesOn(t, s) {
		if p.AfterClock {
			continue
		}
		step(t, s, p.Key)
		want = append(want, p.Key)
	}

	var seen []string
	for len(events) > 0 {
		e := <-events
		if e.Name != api.EventPhase {
			continue
		}
		p, ok := e.Data.(api.PhaseDTO)
		if !ok {
			t.Fatalf("a phase event carried %T, want a PhaseDTO", e.Data)
		}
		if !p.Completed {
			t.Errorf("%q was pushed as not completed", p.Key)
		}
		seen = append(seen, p.Key)
	}
	if !slices.Equal(seen, want) {
		t.Errorf("the stream said %s ran, want %s", strings.Join(seen, " → "), strings.Join(want, " → "))
	}
	if !slices.Contains(seen, "end-of-day") {
		t.Error("end of day moved no file and was never pushed, which is the case this exists for")
	}
}
