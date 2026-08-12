package main

import (
	"slices"
	"strings"
	"testing"
)

// The day's order is the thing this file holds. Every other suite asserts on
// what a day PRODUCED — a balance, a status, a queue — and every one of them
// would stay green if two phases swapped places and the money still arrived. A
// reordering that costs nothing today is what settlement risk looks like the day
// a phase is added between them, so the sequence is written out here and
// compared, the way the route table is dumped and compared.

// namesOf renders a sequence as the lines a golden comparison reads.
func namesOf(list []phase) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.name)
	}
	return out
}

// TestTheDayRunsItsPhasesInOrder is the golden sequence. It fails on an
// insertion, a deletion and a reordering alike, which is the point: each of the
// three is a domain decision, and none of them should be able to arrive as a
// side effect of a change to a phase's body.
//
// ADR-0002 is two lines of it — the clearing-house cut-off, then the settlement,
// then the release. Before this list existed that ruling was statement order in
// one function and prose in fourteen files, and nothing failed if it moved.
func TestTheDayRunsItsPhasesInOrder(t *testing.T) {
	want := []string{
		"refresh",
		"bank cut-off",
		"clearing",
		"clearing-house cut-off",
		"discharge",
		"settlement",
		"release",
		"collection",
		"end of day",
	}
	if got := namesOf(beforeClock); !slices.Equal(got, want) {
		t.Errorf("the day before the clock moves:\n got %s\nwant %s",
			strings.Join(got, " → "), strings.Join(want, " → "))
	}
	if got, want := namesOf(afterClock), []string{"open cycles"}; !slices.Equal(got, want) {
		t.Errorf("the day after the clock moves:\n got %s\nwant %s",
			strings.Join(got, " → "), strings.Join(want, " → "))
	}
}

// TestOnlyTheEndOfDaySurvivesAWeekend pins what a day nothing settles on does.
//
// Interest accrues over a weekend, which is the entire reason day-count
// conventions exist, and nothing else moves — no cut-off, no clearing, no
// settlement. Before settlementOnly existed that answer was readable only from
// AdvanceDay's doc comment.
func TestOnlyTheEndOfDaySurvivesAWeekend(t *testing.T) {
	got := namesOf(onSettlementDay(beforeClock, false))
	if want := []string{"end of day"}; !slices.Equal(got, want) {
		t.Errorf("a day nothing settles on runs %s, want %s",
			strings.Join(got, " → "), strings.Join(want, " → "))
	}
	if got := namesOf(onSettlementDay(afterClock, false)); len(got) != 1 {
		t.Errorf("opening the next day's cycles is not a settlement-day act, got %v", got)
	}
}

// TestEverySequenceRunsThePhasesInTheDaysOwnOrder is the guarantee the derived
// sequences buy, stated directly.
//
// A sequence NAMES the phases it wants and never the order it wants them in, so
// a subset runs them in the day's relative order or it does not run. What this
// asserts is that the property holds for every sequence in the package —
// including the two that live in test files, which is where it was previously
// free to drift: workThrough re-wrote the list, and re-writing is what lets a
// fixture prove something about a deployment nobody could deploy.
func TestEverySequenceRunsThePhasesInTheDaysOwnOrder(t *testing.T) {
	for _, s := range []struct {
		name  string
		list  []phase
		phase []string
	}{
		{"carry to clearing", carryToClearingPhases, []string{
			"bank cut-off", "clearing", "collection · clearing house BTD"}},
		{"work through", workThroughPhases, []string{
			"bank cut-off", "clearing", "settlement", "release", "collection"}},
		{"release without collection", releaseWithoutCollectionPhases, []string{
			"settlement", "release"}},
	} {
		t.Run(s.name, func(t *testing.T) {
			got := namesOf(s.list)
			if !slices.Equal(got, s.phase) {
				t.Errorf("selects %s, want %s",
					strings.Join(got, " → "), strings.Join(s.phase, " → "))
			}
			// And the property the golden list above cannot state for itself:
			// whatever this sequence holds, it holds in the day's order.
			//
			// The narrowed collection stands for the day's, which is the one
			// step in this package that is not a phase of a day. Reading it as
			// the phase it narrows is what lets the check below still mean
			// something for a carry — it sits where the day's collection sits,
			// and it is last there for the same reason.
			if !isSubsequence(asDayPhases(got), namesOf(beforeClock)) {
				t.Errorf("%s is not a subsequence of the day: %s",
					s.name, strings.Join(got, " → "))
			}
		})
	}
}

// TestOnlyRefusesAPhaseTheDayDoesNotDeclare covers the failure that would
// otherwise be silent: a sequence naming a phase that is not in the list it
// selects from would quietly run one phase fewer.
func TestOnlyRefusesAPhaseTheDayDoesNotDeclare(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("selecting an after-clock phase from the before-clock list was accepted")
		}
	}()
	only(beforeClock, phaseBankCutoff, phaseOpenCycles)
}

// asDayPhases reads the narrowed collection as the day phase it narrows.
//
// It is one entry long and it should stay that way: every other step of every
// sequence in this package IS a phase of a day, and each addition here is a
// place a sequence stopped being derived from the day.
func asDayPhases(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == collectClearingHouseOnly.name {
			n = "collection"
		}
		out = append(out, n)
	}
	return out
}

// isSubsequence reports whether every element of got appears in want, in order.
func isSubsequence(got, want []string) bool {
	i := 0
	for _, w := range want {
		if i < len(got) && got[i] == w {
			i++
		}
	}
	return i == len(got)
}
