// These tests are in package ledger rather than ledger_test, which the rest of
// the package's tests must use: a fold takes an EntryScanner and needs no
// store, so there is no import of store/testenv to make a cycle.
package ledger

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"
)

// scanner is an EntryScanner over a fixed slice: it applies the position and
// the window itself, exactly as the store's WHERE clause does, so a fold is
// tested against the same narrowing the store performs.
type scanner struct {
	entries []Entry
	// err is yielded instead of the last entry, for the folds' error path.
	err error
}

func (s scanner) ScanEntries(_ context.Context, _ BookID, pos Position, f EntryFilter) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		if s.err != nil {
			yield(Entry{}, s.err)
			return
		}
		for _, e := range s.entries {
			if e.AccountID != pos.Account {
				continue
			}
			if pos.Subsidiary != "" && e.Subsidiary != pos.Subsidiary {
				continue
			}
			// A zero value date is SQL NULL, which compares to neither bound.
			if !f.From.IsZero() && (e.ValueDate.IsZero() || e.ValueDate.Before(f.From)) {
				continue
			}
			if !f.To.IsZero() && (e.ValueDate.IsZero() || !e.ValueDate.Before(f.To)) {
				continue
			}
			if !yield(e, nil) {
				return
			}
		}
	}
}

const (
	foldAccount  AccountID = "100.100.001"
	foldPooled   AccountID = "200.100.001"
	foldElsewere AccountID = "100.100.002"
)

func foldDay(d int) time.Time { return time.Date(2026, 4, d, 0, 0, 0, 0, time.UTC) }

// foldEntries is the fixture every case below reads: two accounts, one of them
// pooling two subsidiaries, and one entry carrying no value date.
func foldEntries() []Entry {
	return []Entry{
		{ID: "e1", AccountID: foldAccount, Amount: 1000, Direction: Debit, ValueDate: foldDay(10)},
		{ID: "e2", AccountID: foldAccount, Amount: 250, Direction: Credit, ValueDate: foldDay(11).Add(23 * time.Hour)},
		{ID: "e3", AccountID: foldAccount, Amount: 40, Direction: Debit, ValueDate: foldDay(11)},
		{ID: "e4", AccountID: foldAccount, Amount: 7, Direction: Debit},
		{ID: "e5", AccountID: foldElsewere, Amount: 999, Direction: Debit, ValueDate: foldDay(10)},
		{ID: "e6", AccountID: foldPooled, Subsidiary: "dep_1", Amount: 1000, Direction: Credit, ValueDate: foldDay(10)},
		{ID: "e7", AccountID: foldPooled, Subsidiary: "dep_2", Amount: 250, Direction: Credit, ValueDate: foldDay(10)},
		{ID: "e8", AccountID: foldPooled, Subsidiary: "dep_1", Amount: 40, Direction: Credit, ValueDate: foldDay(12)},
		// dep_3 is paid back to nothing, which is not the same as never having
		// been a subsidiary.
		{ID: "e9", AccountID: foldPooled, Subsidiary: "dep_3", Amount: 500, Direction: Credit, ValueDate: foldDay(10)},
		{ID: "e10", AccountID: foldPooled, Subsidiary: "dep_3", Amount: 500, Direction: Debit, ValueDate: foldDay(12)},
	}
}

func TestBookBalanceSignsByNormalDirectionAndCountsEveryEntry(t *testing.T) {
	s := scanner{entries: foldEntries()}
	ctx := context.Background()

	cases := []struct {
		label  string
		pos    Position
		normal Direction
		want   Amount
	}{
		// 1000 - 250 + 40 + 7, the undated entry included: a book balance has no
		// window at all.
		{"debit-normal", foldAccount.Total(), Debit, 797},
		// The same entries read as a liability: every sign flips.
		{"credit-normal", foldAccount.Total(), Credit, -797},
		{"the whole pool", foldPooled.Total(), Credit, 1290},
		{"one subsidiary", foldPooled.For("dep_1"), Credit, 1040},
		{"a subsidiary repaid to zero", foldPooled.For("dep_3"), Credit, 0},
		{"a subsidiary nothing was posted for", foldPooled.For("dep_9"), Credit, 0},
		{"an account nothing was posted to", AccountID("999.999.999").Total(), Debit, 0},
	}
	for _, c := range cases {
		got, err := BookBalance(ctx, s, "book", c.pos, c.normal)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		if got != c.want {
			t.Errorf("%s = %d, want %d", c.label, got, c.want)
		}
	}
}

// The control figure IS the detail with the subsidiary dropped, so this cannot
// fail arithmetically — it fails if the two ever stop reading the same entries.
func TestTheSubsidiariesSumToTheirControlAccount(t *testing.T) {
	s := scanner{entries: foldEntries()}
	ctx := context.Background()

	whole, err := BookBalance(ctx, s, "book", foldPooled.Total(), Credit)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := SubsidiaryBalances(ctx, s, "book", foldPooled, Credit)
	if err != nil {
		t.Fatal(err)
	}

	var detail Amount
	var listed string
	for _, r := range rows {
		detail += r.Balance
		listed += " " + r.Subsidiary
	}
	if detail != whole {
		t.Errorf("the detail sums to %d, the control is %d", detail, whole)
	}
	// dep_3 nets to zero and is not a row; the rest come back in order.
	if listed != " dep_1 dep_2" {
		t.Errorf("subsidiaries =%s, want dep_1 dep_2", listed)
	}
	if rows[0].Balance != 1040 {
		t.Errorf("dep_1 = %d, want 1040", rows[0].Balance)
	}
}

func TestValueDateBalanceCountsOnlyEntriesBeforeTheBound(t *testing.T) {
	s := scanner{entries: foldEntries()}
	ctx := context.Background()

	cases := []struct {
		before time.Time
		want   Amount
	}{
		{foldDay(10), 0},    // nothing takes effect before the 10th
		{foldDay(11), 1000}, // the 10th only
		{foldDay(12), 790},  // the 11th's two entries, time of day included
		{foldDay(13), 790},  // and nothing after: the undated entry is in no bound
		{time.Time{}, 0},    // nothing at all is before the zero time
	}
	for _, c := range cases {
		got, err := ValueDateBalance(ctx, s, "book", foldAccount.Total(), Debit, c.before)
		if err != nil {
			t.Fatalf("before %v: %v", c.before, err)
		}
		if got != c.want {
			t.Errorf("balance before %v = %d, want %d", c.before, got, c.want)
		}
	}
}

func TestValueDatedSeriesBucketsByDayAndCarriesAnOpening(t *testing.T) {
	s := scanner{entries: foldEntries()}
	ctx := context.Background()

	got, err := ValueDatedSeries(ctx, s, "book", foldAccount.Total(), Debit, foldDay(11), foldDay(13))
	if err != nil {
		t.Fatal(err)
	}
	if got.Opening != 1000 {
		t.Errorf("opening = %d, want 1000", got.Opening)
	}
	// One day, not two: both of the 11th's entries land in the same bucket
	// whatever time of day they carry, and the undated entry lands in none.
	if len(got.Movements) != 1 {
		t.Fatalf("movements = %v, want one day", got.Movements)
	}
	if !got.Movements[0].Day.Equal(foldDay(11)) {
		t.Errorf("day = %v, want %v", got.Movements[0].Day, foldDay(11))
	}
	if got.Movements[0].Amount != -210 {
		t.Errorf("movement = %d, want -210", got.Movements[0].Amount)
	}
}

func TestValueDatedSeriesOrdersItsDaysAndSignsByNormalDirection(t *testing.T) {
	s := scanner{entries: foldEntries()}
	ctx := context.Background()

	got, err := ValueDatedSeries(ctx, s, "book", foldPooled.Total(), Credit, foldDay(10), foldDay(13))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Movements) != 2 {
		t.Fatalf("movements = %v, want two days", got.Movements)
	}
	if !got.Movements[0].Day.Before(got.Movements[1].Day) {
		t.Errorf("days came back %v, want ascending", got.Movements)
	}
	// The 10th: three credits into a credit-normal pool. The 12th: one credit
	// and one debit, which is where the sign shows.
	if got.Movements[0].Amount != 1750 || got.Movements[1].Amount != -460 {
		t.Errorf("movements = %v, want 1750 then -460", got.Movements)
	}
}

// A window with no start takes in every dated entry and opens at zero, and the
// undated ones are still in none of its days.
func TestValueDatedSeriesWithNoStartOpensAtZero(t *testing.T) {
	s := scanner{entries: foldEntries()}
	ctx := context.Background()

	got, err := ValueDatedSeries(ctx, s, "book", foldAccount.Total(), Debit, time.Time{}, foldDay(13))
	if err != nil {
		t.Fatal(err)
	}
	if got.Opening != 0 {
		t.Errorf("opening = %d, want 0", got.Opening)
	}
	// The 10th and the 11th; the undated entry is not a third day.
	if len(got.Movements) != 2 {
		t.Fatalf("movements = %v, want two days", got.Movements)
	}
	if got.Movements[0].Amount != 1000 || got.Movements[1].Amount != -210 {
		t.Errorf("movements = %v, want 1000 then -210", got.Movements)
	}
}

func TestValueDatedSeriesOfAnAccountNothingWasPostedTo(t *testing.T) {
	s := scanner{entries: foldEntries()}

	got, err := ValueDatedSeries(context.Background(), s, "book",
		AccountID("999.999.999").Total(), Debit, foldDay(10), foldDay(13))
	if err != nil {
		t.Fatal(err)
	}
	if got.Opening != 0 || len(got.Movements) != 0 {
		t.Errorf("series = %+v, want an empty one", got)
	}
}

func TestValueDatedSeriesOfAWindowThatHoldsNoDays(t *testing.T) {
	s := scanner{entries: foldEntries()}
	ctx := context.Background()

	cases := []struct {
		label    string
		from, to time.Time
	}{
		{"a window with no entries in it", foldDay(20), foldDay(21)},
		{"a window that ends where it starts", foldDay(11), foldDay(11)},
		{"a window that ends before it starts", foldDay(13), foldDay(11)},
		{"a window with no end", foldDay(11), time.Time{}},
	}
	for _, c := range cases {
		got, err := ValueDatedSeries(ctx, s, "book", foldAccount.Total(), Debit, c.from, c.to)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		if len(got.Movements) != 0 {
			t.Errorf("%s: movements = %v, want none", c.label, got.Movements)
		}
		// The opening is still the balance carried in, which is what makes an
		// empty window different from an empty account.
		if got.Opening != openingOf(t, s, c.from) {
			t.Errorf("%s: opening = %d", c.label, got.Opening)
		}
	}
}

// openingOf is the opening the case above expects, computed the one way the
// series itself computes it.
func openingOf(t *testing.T, s scanner, before time.Time) Amount {
	t.Helper()
	got, err := ValueDateBalance(context.Background(), s, "book", foldAccount.Total(), Debit, before)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A reversal is a second, opposite posting rather than a deletion, so every
// fold nets the two and none of them filters on status — which is why no fold
// takes a status at all.
func TestEveryFoldNetsAReversalRatherThanDroppingIt(t *testing.T) {
	ctx := context.Background()
	s := scanner{entries: []Entry{
		{ID: "e1", AccountID: foldAccount, Amount: 10_000, Direction: Debit, ValueDate: foldDay(10)},
		{ID: "e2", AccountID: foldAccount, Amount: 10_000, Direction: Credit, ValueDate: foldDay(10)},
	}}

	book, err := BookBalance(ctx, s, "book", foldAccount.Total(), Debit)
	if err != nil {
		t.Fatal(err)
	}
	if book != 0 {
		t.Errorf("book balance = %d, want 0", book)
	}

	// The reversal carries the ORIGINAL's value date, so the day it lands on
	// nets to zero rather than the window showing a spike and a correction.
	series, err := ValueDatedSeries(ctx, s, "book", foldAccount.Total(), Debit, foldDay(10), foldDay(11))
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Movements) != 1 || series.Movements[0].Amount != 0 {
		t.Errorf("movements = %v, want one day of zero", series.Movements)
	}
}

// Every fold gives up on the first error rather than folding past it, because a
// partial sum is a wrong number and not a smaller one.
func TestAFoldStopsOnTheScannersError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("the scan failed")
	s := scanner{entries: foldEntries(), err: boom}

	for _, c := range []struct {
		label string
		run   func() error
	}{
		{"BookBalance", func() error {
			_, err := BookBalance(ctx, s, "book", foldAccount.Total(), Debit)
			return err
		}},
		{"ValueDateBalance", func() error {
			_, err := ValueDateBalance(ctx, s, "book", foldAccount.Total(), Debit, foldDay(13))
			return err
		}},
		{"ValueDatedSeries", func() error {
			_, err := ValueDatedSeries(ctx, s, "book", foldAccount.Total(), Debit, foldDay(10), foldDay(13))
			return err
		}},
		{"SubsidiaryBalances", func() error {
			_, err := SubsidiaryBalances(ctx, s, "book", foldPooled, Credit)
			return err
		}},
	} {
		if err := c.run(); !errors.Is(err, boom) {
			t.Errorf("%s returned %v, want the scanner's error", c.label, err)
		}
	}
}

// The sign rule, on its own, because five folds share it and each of them would
// otherwise be the place it is tested.
func TestAnEntrySignsByTheAccountsNormalDirection(t *testing.T) {
	debit := Entry{Amount: 100, Direction: Debit}
	credit := Entry{Amount: 100, Direction: Credit}

	for _, c := range []struct {
		e      Entry
		normal Direction
		want   Amount
	}{
		{debit, Debit, 100},
		{debit, Credit, -100},
		{credit, Credit, 100},
		{credit, Debit, -100},
	} {
		if got := signed(c.e, c.normal); got != c.want {
			t.Errorf("%s entry on a %s-normal account = %d, want %d",
				c.e.Direction, c.normal, got, c.want)
		}
	}
}
