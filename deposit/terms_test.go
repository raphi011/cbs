package deposit

import (
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
)

func termsDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func sampleTerms(from time.Time, rate interest.Rate) OverdraftTerms {
	return OverdraftTerms{
		AccountID: "dep_1", EffectiveFrom: from, OverdraftLimit: 50_000,
		Rate: rate, DayCount: interest.ACT365,
	}
}

// The six positions termsAt has to answer for. Nothing else in the system
// resolves a timeline, so a cursor-style bug here would show up only as an
// interest figure nobody can reproduce.
func TestTermsAtResolvesEveryPosition(t *testing.T) {
	rows := []OverdraftTerms{
		sampleTerms(termsDay(2025, time.January, 1), 100_000),
		sampleTerms(termsDay(2025, time.March, 1), 200_000),
		sampleTerms(termsDay(2025, time.June, 1), 300_000),
	}

	cases := []struct {
		name string
		day  time.Time
		want interest.Rate
		ok   bool
	}{
		{"before the first row", termsDay(2024, time.December, 31), 0, false},
		{"exactly on the first boundary", termsDay(2025, time.January, 1), 100_000, true},
		{"between rows", termsDay(2025, time.February, 14), 100_000, true},
		{"exactly on a later boundary", termsDay(2025, time.March, 1), 200_000, true},
		{"the day before a boundary", termsDay(2025, time.May, 31), 200_000, true},
		{"after the last row", termsDay(2030, time.January, 1), 300_000, true},
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

// A time of day on the query must not move the answer. Accrual iterates whole
// UTC days, and a caller that has not truncated is asking about the same day.
func TestTermsAtIgnoresTheTimeOfDay(t *testing.T) {
	rows := []OverdraftTerms{sampleTerms(termsDay(2025, time.March, 1), 200_000)}

	if _, ok := termsAt(rows, time.Date(2025, time.February, 28, 23, 59, 0, 0, time.UTC)); ok {
		t.Error("late on the day before resolved the row")
	}
	got, ok := termsAt(rows, time.Date(2025, time.March, 1, 0, 0, 1, 0, time.UTC))
	if !ok || got.Rate != 200_000 {
		t.Errorf("a second into the effective day: got %d, %v", got.Rate, ok)
	}
}

func TestTermsAtOnASingleRow(t *testing.T) {
	rows := []OverdraftTerms{sampleTerms(termsDay(2025, time.January, 1), 100_000)}

	if _, ok := termsAt(rows, termsDay(2024, time.December, 31)); ok {
		t.Error("resolved a day before the only row")
	}
	got, ok := termsAt(rows, termsDay(2025, time.January, 1))
	if !ok || got.Rate != 100_000 {
		t.Errorf("on the boundary: got %d, %v", got.Rate, ok)
	}
}

func TestTermsAtOnAnEmptyTimeline(t *testing.T) {
	if _, ok := termsAt(nil, termsDay(2025, time.January, 1)); ok {
		t.Error("resolved against no rows at all")
	}
}

// termsAt takes rows ALREADY ascending — that is the store's contract — and
// this pins that a backdated insert reaching the resolver in ascending order
// (which is what List returns after the store has sorted it) resolves the same
// as if it had always been there.
func TestTermsAtAfterABackdatedInsert(t *testing.T) {
	rows := []OverdraftTerms{
		sampleTerms(termsDay(2025, time.January, 1), 100_000),
		sampleTerms(termsDay(2025, time.February, 1), 250_000), // entered last, effective in the middle
		sampleTerms(termsDay(2025, time.March, 1), 200_000),
	}
	got, ok := termsAt(rows, termsDay(2025, time.February, 15))
	if !ok || got.Rate != 250_000 {
		t.Errorf("mid-February: got %d, %v", got.Rate, ok)
	}
}

func TestAnyPriced(t *testing.T) {
	unpriced := []OverdraftTerms{
		sampleTerms(termsDay(2025, time.January, 1), 0),
		sampleTerms(termsDay(2025, time.March, 1), 0),
	}
	if anyPriced(unpriced) {
		t.Error("a timeline of zero rates reported as priced")
	}
	priced := append(append([]OverdraftTerms{}, unpriced...), sampleTerms(termsDay(2025, time.June, 1), 150_000))
	if !anyPriced(priced) {
		t.Error("a timeline ending in a non-zero rate reported as unpriced")
	}
}

func TestTermsDayKeyIsTheUTCCalendarDay(t *testing.T) {
	// Two instants on the same UTC day produce one key, which is what makes
	// the row identity a DAY rather than a moment.
	morning := time.Date(2025, time.March, 1, 9, 0, 0, 0, time.UTC)
	night := time.Date(2025, time.March, 1, 23, 30, 0, 0, time.UTC)
	if TermsDayKey(morning) != TermsDayKey(night) {
		t.Errorf("keys differ within one day: %q vs %q", TermsDayKey(morning), TermsDayKey(night))
	}
	if got := TermsDayKey(morning); got != "2025-03-01" {
		t.Errorf("TermsDayKey = %q, want 2025-03-01", got)
	}
	// ISO days sort lexicographically, which is the whole reason the key is
	// rendered this way: both stores compare it as a string and get a day
	// comparison for free.
	if !(TermsDayKey(termsDay(2025, time.February, 28)) < TermsDayKey(termsDay(2025, time.March, 1))) {
		t.Error("day keys do not sort lexicographically")
	}
}

func TestOverdraftTermsValidation(t *testing.T) {
	base := sampleTerms(termsDay(2025, time.January, 1), 150_000)

	negativeLimit := base
	negativeLimit.OverdraftLimit = -1
	if err := negativeLimit.Validate(); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("negative limit: got %v, want ErrInvalidAmount", err)
	}

	negativeRate := base
	negativeRate.Rate = -1
	if err := negativeRate.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("negative rate: got %v, want ErrInvalidRate", err)
	}

	negativeUnarranged := base
	negativeUnarranged.UnarrangedRate = -1
	if err := negativeUnarranged.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("negative unarranged rate: got %v, want ErrInvalidRate", err)
	}

	// The one combination that would price ONLY the excess, making the money
	// drawn beyond the limit dearer than nothing while leaving the facility
	// inside it free.
	excessOnly := base
	excessOnly.Rate = 0
	excessOnly.UnarrangedRate = 350_000
	if err := excessOnly.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("unarranged with no arranged: got %v, want ErrInvalidRate", err)
	}

	// A zero rate throughout is a real product: an interest-free overdraft.
	free := base
	free.Rate = 0
	free.UnarrangedRate = 0
	if err := free.Validate(); err != nil {
		t.Errorf("interest-free overdraft: got %v, want nil", err)
	}
}
