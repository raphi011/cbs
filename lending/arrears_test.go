package lending_test

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

func TestArrearsFor(t *testing.T) {
	due := day(2025, time.February, 15)
	unpaid := []lending.Installment{
		{Seq: 1, DueDate: due, Principal: 14_333, Interest: 5_000},
		{Seq: 2, DueDate: lending.AddMonths(due, 1), Principal: 14_405, Interest: 4_928},
	}
	paid := []lending.Installment{
		{Seq: 1, DueDate: due, Principal: 14_333, Interest: 5_000, PaidPrincipal: 14_333, PaidInterest: 5_000},
		{Seq: 2, DueDate: lending.AddMonths(due, 1), Principal: 14_405, Interest: 4_928},
	}

	tests := []struct {
		name        string
		schedule    []lending.Installment
		asOf        time.Time
		wantDays    int
		wantBucket  lending.Bucket
		wantNonPerf bool
	}{
		{"no schedule at all", nil, day(2025, time.March, 20), 0, lending.Current, false},
		{"nothing due yet", unpaid, day(2025, time.February, 1), 0, lending.Current, false},
		{"due today is not late", unpaid, due, 0, lending.Current, false},
		{"one day late", unpaid, day(2025, time.February, 16), 1, lending.D1_29, false},
		{"the day before the 30 boundary", unpaid, day(2025, time.March, 16), 29, lending.D1_29, false},
		{"exactly 30 days", unpaid, day(2025, time.March, 17), 30, lending.D30_59, false},
		{"33 days", unpaid, day(2025, time.March, 20), 33, lending.D30_59, false},
		{"the day before the 90 boundary", unpaid, day(2025, time.May, 15), 89, lending.D60_89, false},
		{"exactly 90 days is non-performing", unpaid, day(2025, time.May, 16), 90, lending.D90Plus, true},

		// Arrears are measured from the OLDEST unpaid instalment. Paying the
		// first one moves the clock to the second, it does not reset it.
		//
		// The oldest unpaid instalment (Seq 2) is due 15 March (one month after
		// the first). Five days after that is 20 March.
		{"the oldest unpaid one sets the clock", paid, day(2025, time.March, 20), 5, lending.D1_29, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lending.ArrearsFor(tt.schedule, tt.asOf)
			if got.DaysPastDue != tt.wantDays {
				t.Errorf("days past due = %d, want %d", got.DaysPastDue, tt.wantDays)
			}
			if got.Bucket != tt.wantBucket {
				t.Errorf("bucket = %s, want %s", got.Bucket, tt.wantBucket)
			}
			if got.NonPerforming != tt.wantNonPerf {
				t.Errorf("non-performing = %v, want %v", got.NonPerforming, tt.wantNonPerf)
			}
		})
	}

	// Days past due are ALWAYS actual calendar days, whatever convention the
	// loan accrues under. A 30/360 loan is not 33/360 days late.
	got := lending.ArrearsFor(unpaid, day(2025, time.March, 20))
	if !got.OldestUnpaidDue.Equal(due) {
		t.Errorf("oldest unpaid due = %v, want %v", got.OldestUnpaidDue, due)
	}
	current := lending.ArrearsFor(paid[:1], day(2025, time.March, 20))
	if !current.OldestUnpaidDue.IsZero() {
		t.Errorf("a current facility has an oldest unpaid date: %v", current.OldestUnpaidDue)
	}
}

// TestArrearsFor_RevolvingLineOutOfOrderCycles is a regression test for a scan
// that used to assume Seq order tracked DueDate order. A revolving line's
// cycles are appended by Seq, but ChargeInterestTx takes each cycle's due date
// from the caller's billing date — a backdated charge produces a later-Seq
// cycle with an EARLIER due date than the one before it. The oldest UNPAID due
// date must win regardless of where it sits in the slice.
func TestArrearsFor_RevolvingLineOutOfOrderCycles(t *testing.T) {
	ctx := context.Background()
	p, _, sub, customer := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, sub, "Out of order line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	if _, err := p.Draw(ctx, line.ID, customer, 100_000, "Draw"); err != nil {
		t.Fatalf("Draw: %v", err)
	}

	// Cycle 1 (Seq 1), billed 1 June: due 1 July — in the future relative to
	// the asOf date used below.
	if _, err := p.ChargeInterest(ctx, line.ID, day(2025, time.June, 1)); err != nil {
		t.Fatalf("ChargeInterest (cycle 1): %v", err)
	}
	// Cycle 2 (Seq 2), backdated to 1 February: due 1 March — EARLIER than
	// cycle 1's due date, despite the higher Seq.
	if _, err := p.ChargeInterest(ctx, line.ID, day(2025, time.February, 1)); err != nil {
		t.Fatalf("ChargeInterest (cycle 2): %v", err)
	}

	schedule, err := p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 2 {
		t.Fatalf("cycles = %d, want 2", len(schedule))
	}
	if !schedule[0].DueDate.Equal(day(2025, time.July, 1)) {
		t.Fatalf("cycle 1 (Seq %d) due date = %v, want 1 July", schedule[0].Seq, schedule[0].DueDate)
	}
	if !schedule[1].DueDate.Equal(day(2025, time.March, 1)) {
		t.Fatalf("cycle 2 (Seq %d) due date = %v, want 1 March", schedule[1].Seq, schedule[1].DueDate)
	}

	// asOf 1 April: cycle 1 (Seq 1, due 1 July) is not yet due. Cycle 2
	// (Seq 2, due 1 March, LATER in the slice than cycle 1) is overdue. March
	// has 31 days, so 1 March to 1 April is 31 calendar days — the 30-59
	// bucket, not Current.
	got := lending.ArrearsFor(schedule, day(2025, time.April, 1))
	if got.DaysPastDue != 31 {
		t.Errorf("days past due = %d, want 31", got.DaysPastDue)
	}
	if got.Bucket != lending.D30_59 {
		t.Errorf("bucket = %s, want 30-59", got.Bucket)
	}
	if got.NonPerforming {
		t.Error("non-performing = true, want false")
	}
	if !got.OldestUnpaidDue.Equal(day(2025, time.March, 1)) {
		t.Errorf("oldest unpaid due = %v, want 1 March", got.OldestUnpaidDue)
	}
}

func TestRunEndOfDay_AccruesAndMovesFacilitiesIntoArrears(t *testing.T) {
	ctx := context.Background()
	p, _, loan, _ := disbursedLoan(t)

	// Run every day from disbursement to 20 March without paying anything.
	for d := day(2025, time.January, 16); !d.After(day(2025, time.March, 20)); d = d.AddDate(0, 0, 1) {
		if err := p.RunEndOfDay(ctx, d); err != nil {
			t.Fatalf("RunEndOfDay %v: %v", d, err)
		}
	}

	got, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if got.Arrears.DaysPastDue != 33 {
		t.Errorf("days past due = %d, want 33", got.Arrears.DaysPastDue)
	}
	if got.Arrears.Bucket != lending.D30_59 {
		t.Errorf("bucket = %s, want 30-59", got.Arrears.Bucket)
	}
	// Delinquency is not a lifecycle state: a loan in arrears is still Active.
	if got.Status != lending.Active {
		t.Errorf("status = %s, want Active", got.Status)
	}
	// And it is still accruing, because non-accrual is future work.
	if got.Accrued <= 0 {
		t.Errorf("a delinquent loan stopped accruing: %d", got.Accrued)
	}

	// Interest kept accruing for 64 days at 164.383561 cents: 64 ×
	// 164_383_561 = 10_520_547_904 micro-minor-units, which is
	// 10520.547904 minor units — rounding half away from zero to 10_521.
	if want := ledger.Amount(10_521); got.Accrued.Minor() != want {
		t.Errorf("accrued = %d, want %d", got.Accrued.Minor(), want)
	}
}

func TestRunEndOfDay_PayingClearsTheArrears(t *testing.T) {
	ctx := context.Background()
	p, _, loan, customer := disbursedLoan(t)

	for d := day(2025, time.January, 16); !d.After(day(2025, time.March, 20)); d = d.AddDate(0, 0, 1) {
		if err := p.RunEndOfDay(ctx, d); err != nil {
			t.Fatalf("RunEndOfDay: %v", err)
		}
	}
	before, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if before.Arrears.Bucket == lending.Current {
		t.Fatal("the loan was never in arrears")
	}

	// Two instalments are overdue by now; pay both.
	schedule, err := p.Schedule(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	owed := schedule[0].Outstanding() + schedule[1].Outstanding()
	if _, err := p.Repay(ctx, loan.ID, customer, owed, day(2025, time.March, 20), "Catch up"); err != nil {
		t.Fatalf("Repay: %v", err)
	}

	if err := p.RunEndOfDay(ctx, day(2025, time.March, 21)); err != nil {
		t.Fatalf("RunEndOfDay: %v", err)
	}
	after, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if after.Arrears.Bucket != lending.Current {
		t.Errorf("bucket after catching up = %s, want Current", after.Arrears.Bucket)
	}
	if after.Arrears.DaysPastDue != 0 {
		t.Errorf("days past due after catching up = %d, want 0", after.Arrears.DaysPastDue)
	}
}

func TestRunEndOfDay_IsIdempotentAndSkipsClosedFacilities(t *testing.T) {
	ctx := context.Background()
	p, _, loan, _ := disbursedLoan(t)

	date := day(2025, time.January, 16)
	if err := p.RunEndOfDay(ctx, date); err != nil {
		t.Fatalf("RunEndOfDay: %v", err)
	}
	after, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}

	if err := p.RunEndOfDay(ctx, date); err != nil {
		t.Fatalf("second RunEndOfDay: %v", err)
	}
	again, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if again.Accrued != after.Accrued {
		t.Errorf("accrued after a re-run = %d, want %d", again.Accrued, after.Accrued)
	}
}

// A facility that is current must write no arrears events at all, however many
// days the batch runs.
//
// Once past a due date DaysPastDue climbs every day, so an event a day is
// correct there — a bank does track the count daily. The stable case is the
// current one, and it is where a broken comparison shows: Arrears carries a
// time.Time, and == on a time.Time compares the monotonic reading and location
// as well as the instant, so a stored value that round-trips through Postgres
// with a different location would look changed every day and rewrite the row.
func TestRunEndOfDay_ACurrentFacilityWritesNoArrearsEvents(t *testing.T) {
	ctx := context.Background()
	p, _, _, _ := disbursedLoan(t)

	// Every day from disbursement up to the day before the first instalment
	// falls due on 15 February.
	for d := day(2025, time.January, 16); d.Before(day(2025, time.February, 15)); d = d.AddDate(0, 0, 1) {
		if err := p.RunEndOfDay(ctx, d); err != nil {
			t.Fatalf("RunEndOfDay %v: %v", d, err)
		}
	}

	arrearsEvents := func() int {
		t.Helper()
		var n int
		err := p.Store().View(ctx, func(ctx context.Context, tx lending.Tx) error {
			events, err := tx.ListAudit(ctx, ledger.AuditFilter{
				Scope: ledger.ScopeLending, Type: ledger.EventFacilityArrears,
			})
			n = len(events)
			return err
		})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		return n
	}

	if got := arrearsEvents(); got != 0 {
		t.Errorf("a current facility wrote %d arrears events over 30 days, want 0", got)
	}

	// The day after the instalment falls due, it writes exactly one.
	if err := p.RunEndOfDay(ctx, day(2025, time.February, 16)); err != nil {
		t.Fatalf("RunEndOfDay: %v", err)
	}
	if got := arrearsEvents(); got != 1 {
		t.Errorf("falling into arrears wrote %d events, want 1", got)
	}
}
