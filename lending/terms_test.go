package lending

import (
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
)

// day is declared in schedule_test.go, in this same package.

func sampleTerms(from time.Time, rate interest.Rate) FacilityTerms {
	return FacilityTerms{
		FacilityID: "fac_1", EffectiveFrom: from, Rate: rate, DayCount: interest.ACT365,
	}
}

func TestFacilityTermsAtResolvesEveryPosition(t *testing.T) {
	rows := []FacilityTerms{
		sampleTerms(day(2025, time.January, 1), 60_000),
		sampleTerms(day(2025, time.March, 1), 90_000),
		sampleTerms(day(2025, time.June, 1), 120_000),
	}
	cases := []struct {
		name string
		day  time.Time
		want interest.Rate
		ok   bool
	}{
		{"before the first row", day(2024, time.December, 31), 0, false},
		{"exactly on the first boundary", day(2025, time.January, 1), 60_000, true},
		{"between rows", day(2025, time.February, 14), 60_000, true},
		{"exactly on a later boundary", day(2025, time.March, 1), 90_000, true},
		{"the day before a boundary", day(2025, time.May, 31), 90_000, true},
		{"after the last row", day(2030, time.January, 1), 120_000, true},
	}
	for _, c := range cases {
		got, ok := termsAt(rows, c.day)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && got.Rate != c.want {
			t.Errorf("%s: rate = %d, want %d", c.name, got.Rate, c.want)
		}
	}
}

func TestFacilityTermsAtIgnoresTheTimeOfDay(t *testing.T) {
	rows := []FacilityTerms{sampleTerms(day(2025, time.March, 1), 90_000)}
	if _, ok := termsAt(rows, time.Date(2025, time.February, 28, 23, 59, 0, 0, time.UTC)); ok {
		t.Error("late on the day before resolved the row")
	}
	if got, ok := termsAt(rows, time.Date(2025, time.March, 1, 0, 0, 1, 0, time.UTC)); !ok || got.Rate != 90_000 {
		t.Errorf("a second into the effective day: got %d, %v", got.Rate, ok)
	}
}

func TestFacilityTermsAtOnASingleRowAndOnNone(t *testing.T) {
	one := []FacilityTerms{sampleTerms(day(2025, time.January, 1), 60_000)}
	if _, ok := termsAt(one, day(2024, time.December, 31)); ok {
		t.Error("resolved a day before the only row")
	}
	if got, ok := termsAt(one, day(2025, time.January, 1)); !ok || got.Rate != 60_000 {
		t.Errorf("on the boundary: got %d, %v", got.Rate, ok)
	}
	if _, ok := termsAt(nil, day(2025, time.January, 1)); ok {
		t.Error("resolved against no rows at all")
	}
}

func TestFacilityTermsAtAfterABackdatedInsert(t *testing.T) {
	rows := []FacilityTerms{
		sampleTerms(day(2025, time.January, 1), 60_000),
		sampleTerms(day(2025, time.February, 1), 150_000), // entered last, effective in the middle
		sampleTerms(day(2025, time.March, 1), 90_000),
	}
	if got, ok := termsAt(rows, day(2025, time.February, 15)); !ok || got.Rate != 150_000 {
		t.Errorf("mid-February: got %d, %v", got.Rate, ok)
	}
}

func TestFacilityAnyPriced(t *testing.T) {
	unpriced := []FacilityTerms{sampleTerms(day(2025, time.January, 1), 0)}
	if anyPriced(unpriced) {
		t.Error("a zero-rate timeline reported as priced")
	}
	if !anyPriced(append(unpriced, sampleTerms(day(2025, time.June, 1), 60_000))) {
		t.Error("a timeline ending in a non-zero rate reported as unpriced")
	}
}

func TestFacilityTermsDayKeyIsTheUTCCalendarDay(t *testing.T) {
	morning := time.Date(2025, time.March, 1, 9, 0, 0, 0, time.UTC)
	night := time.Date(2025, time.March, 1, 23, 30, 0, 0, time.UTC)
	if TermsDayKey(morning) != TermsDayKey(night) {
		t.Errorf("keys differ within one day: %q vs %q", TermsDayKey(morning), TermsDayKey(night))
	}
	if got := TermsDayKey(morning); got != "2025-03-01" {
		t.Errorf("TermsDayKey = %q, want 2025-03-01", got)
	}
	if !(TermsDayKey(day(2025, time.February, 28)) < TermsDayKey(day(2025, time.March, 1))) {
		t.Error("day keys do not sort lexicographically")
	}
}

func TestFacilityTermsValidation(t *testing.T) {
	negative := sampleTerms(day(2025, time.January, 1), -1)
	if err := negative.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("negative rate: got %v, want ErrInvalidRate", err)
	}
	free := sampleTerms(day(2025, time.January, 1), 0)
	if err := free.Validate(); err != nil {
		t.Errorf("zero-rate facility: got %v, want nil", err)
	}
}
