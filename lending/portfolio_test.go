package lending_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/store/mem"
)

const bookID ledger.BookID = "bank"

// mutableClock is a test clock a test can move. Most of this package runs on the
// frozen one below, which is the stronger fixture — but a terms row records WHEN
// it was entered as well as when it takes effect, and an advance takes its value
// date from the clock, so a test about a facility's life over time has to be able
// to move it.
type mutableClock struct{ at time.Time }

func (c *mutableClock) set(t time.Time) { c.at = t }
func (c *mutableClock) now() time.Time  { return c.at }

// newTestPortfolio returns a portfolio over a fresh in-memory store, the book
// it composes with, a subledger to file facilities in, and a Liability account
// to disburse into — standing in for a customer's current account.
func newTestPortfolio(t *testing.T) (*lending.Portfolio, *ledger.Book, ledger.SubledgerID, ledger.AccountID) {
	t.Helper()
	return newTestPortfolioOn(t, func() time.Time { return time.Date(2025, time.January, 15, 9, 0, 0, 0, time.UTC) })
}

// newTestPortfolioOn is newTestPortfolio on a caller-supplied clock, for the
// tests where WHEN an operation happened is the thing under test.
func newTestPortfolioOn(t *testing.T, clock func() time.Time) (*lending.Portfolio, *ledger.Book, ledger.SubledgerID, ledger.AccountID) {
	t.Helper()
	ctx := context.Background()

	store := mem.New(clock)
	book := ledger.NewBook(store, bookID, clock)
	portfolio := lending.NewPortfolio(store.Lending(), book, bookID, clock)

	gl, err := book.CreateLedger(ctx, "GL")
	if err != nil {
		t.Fatalf("CreateLedger: %v", err)
	}
	sub, err := book.CreateSubledger(ctx, gl.ID, "Customer Deposits")
	if err != nil {
		t.Fatalf("CreateSubledger: %v", err)
	}
	customer, err := book.CreateAccount(ctx, sub.ID, "Alice Current", ledger.Liability, "EUR")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return portfolio, book, sub.ID, customer.ID
}

func TestOpenTermLoan_CreatesTwoAssetAccountsAndNoSchedule(t *testing.T) {
	ctx := context.Background()
	p, book, sub, _ := newTestPortfolio(t)

	loan, err := p.OpenTermLoan(ctx, sub, "Alice Home Loan", "EUR", 1_000_000, 60_000, interest.ACT365, lending.Annuity, 60)
	if err != nil {
		t.Fatalf("OpenTermLoan: %v", err)
	}

	if loan.Status != lending.Pending {
		t.Errorf("status = %s, want Pending", loan.Status)
	}
	if loan.Commitment != 1_000_000 {
		t.Errorf("commitment = %d, want 1000000", loan.Commitment)
	}

	// Two accounts, both Asset, both in the loan's own asset. Separate because
	// repayment allocates to interest before principal.
	for _, tt := range []struct {
		label string
		id    ledger.AccountID
	}{{"principal", loan.PrincipalGL}, {"interest", loan.InterestGL}} {
		gl, err := book.GetAccount(ctx, tt.id)
		if err != nil {
			t.Fatalf("GetAccount(%s): %v", tt.label, err)
		}
		if gl.Type != ledger.Asset {
			t.Errorf("%s account type = %s, want Asset", tt.label, gl.Type)
		}
		if gl.Asset != "EUR" {
			t.Errorf("%s account asset = %s, want EUR", tt.label, gl.Asset)
		}
	}
	if loan.PrincipalGL == loan.InterestGL {
		t.Fatal("principal and interest share one account")
	}

	// Nothing is drawn and there is no schedule until disbursement: the first
	// due date is not known at opening, and a schedule for money that was never
	// paid out would be a plan to repay nothing.
	drawn, err := p.Drawn(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if drawn != 0 {
		t.Errorf("drawn before disbursement = %d, want 0", drawn)
	}
	schedule, err := p.Schedule(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 0 {
		t.Errorf("instalments before disbursement = %d, want 0", len(schedule))
	}
}

func TestOpenTermLoan_Rejects(t *testing.T) {
	ctx := context.Background()
	p, _, sub, _ := newTestPortfolio(t)

	for _, tt := range []struct {
		name      string
		principal ledger.Amount
		rate      interest.Rate
		months    int
		want      error
	}{
		{"zero principal", 0, 60_000, 60, lending.ErrInvalidAmount},
		{"negative principal", -1, 60_000, 60, lending.ErrInvalidAmount},
		{"negative rate", 1_000_000, -1, 60, lending.ErrInvalidRate},
		{"zero term", 1_000_000, 60_000, 0, lending.ErrInvalidTerm},
		{"negative term", 1_000_000, 60_000, -1, lending.ErrInvalidTerm},
		// The term is an allocation — BuildSchedule writes a row per month —
		// so an unbounded one is an unbounded allocation driven by a request
		// field. Fifty years is the last term accepted.
		{"a term past the cap", 1_000_000, 60_000, lending.MaxTermMonths + 1, lending.ErrInvalidTerm},
		{"an absurd term", 1_000_000, 60_000, 100_000_000, lending.ErrInvalidTerm},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.OpenTermLoan(ctx, sub, "Loan", "EUR", tt.principal, tt.rate, interest.ACT365, lending.Annuity, tt.months)
			assertErrorIs(t, err, tt.want)
		})
	}

	// The cap itself is accepted — the bound is on absurdity, not on long
	// mortgages — so the guard cannot quietly become off-by-one.
	if _, err := p.OpenTermLoan(ctx, sub, "Fifty Year", "EUR", 1_000_000, 60_000, interest.ACT365, lending.Annuity, lending.MaxTermMonths); err != nil {
		t.Fatalf("OpenTermLoan at MaxTermMonths: %v", err)
	}

	// An unknown asset is refused by the ledger before anything is written,
	// rather than opening a facility whose accounts could not be created.
	_, err := p.OpenTermLoan(ctx, sub, "Loan", "XYZ", 1_000_000, 60_000, interest.ACT365, lending.Annuity, 60)
	assertErrorIs(t, err, ledger.ErrAssetNotFound)
}

func TestDisburse_PostsPrincipalAndGeneratesTheSchedule(t *testing.T) {
	ctx := context.Background()
	p, book, sub, customer := newTestPortfolio(t)

	loan, err := p.OpenTermLoan(ctx, sub, "Alice Home Loan", "EUR", 1_000_000, 60_000, interest.ACT365, lending.Annuity, 60)
	if err != nil {
		t.Fatalf("OpenTermLoan: %v", err)
	}

	firstDue := time.Date(2025, time.February, 15, 0, 0, 0, 0, time.UTC)
	txn, err := p.Disburse(ctx, loan.ID, customer, firstDue, "Home loan advance")
	if err != nil {
		t.Fatalf("Disburse: %v", err)
	}
	if len(txn.Entries) != 2 {
		t.Fatalf("disbursement posted %d entries, want 2", len(txn.Entries))
	}

	// Dr loan principal (Asset) / Cr the customer's current account
	// (Liability): the bank's claim on the borrower rises, and so does what it
	// owes them, because the money is now in their account.
	principal, err := book.BookBalance(ctx, loan.PrincipalGL)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if principal != 1_000_000 {
		t.Errorf("loan principal = %d, want 1000000", principal)
	}
	customerBalance, err := book.BookBalance(ctx, customer)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if customerBalance != 1_000_000 {
		t.Errorf("customer balance = %d, want 1000000", customerBalance)
	}

	after, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if after.Status != lending.Active {
		t.Errorf("status = %s, want Active", after.Status)
	}
	// Disbursement no longer touches LastAccrualDate, and it is still zero here.
	//
	// It used to be set to the clock, because disbursement was where the accrual
	// window opened. The window now opens at ORIGINATION and never moves, so
	// there is nothing for a disbursement to open and LastAccrualDate is only the
	// advancement guard's high-water mark, set by the first accrual run. Money
	// not yet paid out still earns nothing — but by arithmetic, because the drawn
	// series is zero across those days, rather than by a date on the row.
	if !after.LastAccrualDate.IsZero() {
		t.Errorf("last accrual date = %v, want zero: disbursement does not open a window", after.LastAccrualDate)
	}
	// The terms the schedule was generated at are resolvable, and the timeline
	// holds exactly the opening row written at origination.
	terms, err := p.FacilityTermsHistory(ctx, loan.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline length at disbursement", len(terms), 1)
	assertEqual(t, "opening row rate", terms[0].Rate, interest.Rate(60_000))
	if !after.MaturityAt.Equal(lending.AddMonths(firstDue, 59)) {
		t.Errorf("maturity = %v, want %v", after.MaturityAt, lending.AddMonths(firstDue, 59))
	}

	schedule, err := p.Schedule(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 60 {
		t.Fatalf("instalments = %d, want 60", len(schedule))
	}
	if schedule[0].Total() != 19_333 {
		t.Errorf("first instalment = %d, want 19333", schedule[0].Total())
	}

	// A term loan's principal is fixed and paid out once. A second
	// disbursement would be a different loan.
	_, err = p.Disburse(ctx, loan.ID, customer, firstDue, "again")
	assertErrorIs(t, err, lending.ErrAlreadyDisbursed)

	// And drawing is not what a term loan does.
	_, err = p.Draw(ctx, loan.ID, customer, 1_000, "draw")
	assertErrorIs(t, err, lending.ErrWrongFacilityKind)
}

func TestDraw_RespectsTheCommitmentAndRepeats(t *testing.T) {
	ctx := context.Background()
	p, book, sub, customer := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, sub, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	if line.Status != lending.Pending {
		t.Errorf("status = %s, want Pending", line.Status)
	}

	if _, err := p.Draw(ctx, line.ID, customer, 100_000, "First draw"); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	// A revolving line is drawn repeatedly — that is what revolving means.
	if _, err := p.Draw(ctx, line.ID, customer, 150_000, "Second draw"); err != nil {
		t.Fatalf("second Draw: %v", err)
	}

	drawn, err := p.Drawn(ctx, line.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if drawn != 250_000 {
		t.Errorf("drawn = %d, want 250000", drawn)
	}

	principal, err := book.BookBalance(ctx, line.PrincipalGL)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if principal != drawn {
		t.Errorf("drawn %d is not the principal account balance %d", drawn, principal)
	}

	// The commitment is a limit, and one more cent is over it.
	_, err = p.Draw(ctx, line.ID, customer, 1, "over the limit")
	assertErrorIs(t, err, lending.ErrLimitExceeded)

	_, err = p.Draw(ctx, line.ID, customer, 0, "nothing")
	assertErrorIs(t, err, lending.ErrInvalidAmount)

	// Disbursing is not what a revolving line does.
	_, err = p.Disburse(ctx, line.ID, customer, time.Now(), "disburse")
	assertErrorIs(t, err, lending.ErrWrongFacilityKind)

	// A line has no up-front schedule; its instalments are its billing cycles.
	schedule, err := p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 0 {
		t.Errorf("instalments on a fresh line = %d, want 0", len(schedule))
	}
}

// TestDisburse_LeavesTheAccrualWindowAlone is the successor to a test that
// pinned a clamp this change deletes.
//
// ErrAlreadyDisbursed is guarded on drawn principal, not on status, so a term
// loan repaid in full and not closed can be disbursed a second time. End-of-day
// takes its date from the caller, so accrual can legitimately have run through a
// date ahead of the wall clock by then. Disbursement USED to reopen the recompute
// window at the clock and zero AccruedGross with it, which on that path would
// have left the window behind an already-charged span and charged it twice — so
// it clamped forward.
//
// Neither figure is touched at all now: the window opens at origination and never
// moves, so there is no boundary for a day to fall between and nothing for a
// clamp to protect. This asserts exactly that, on the same awkward path — the
// deleted clamp's own scenario, with the assertion moved from "it clamped" to
// "there is nothing to clamp". What the re-accrual then charges is
// TestReDisbursementChargesTheSpanBeforeTheRepayment's subject.
func TestDisburse_LeavesTheAccrualWindowAlone(t *testing.T) {
	ctx := context.Background()
	p, _, loan, customer := disbursedLoan(t)

	// An end-of-day for a date five years ahead of the test clock.
	ahead := day(2030, time.January, 1)
	assertNoError(t, p.Accrue(ctx, loan.ID, ahead))
	charged, err := p.GetFacility(ctx, loan.ID)
	assertNoError(t, err)

	// Repay it to zero, which is what unlocks a second disbursement.
	owed, err := p.Outstanding(ctx, loan.ID)
	assertNoError(t, err)
	_, err = p.Repay(ctx, loan.ID, customer, owed, ahead, "full")
	assertNoError(t, err)
	settled, err := p.GetFacility(ctx, loan.ID)
	assertNoError(t, err)

	_, err = p.Disburse(ctx, loan.ID, customer, day(2030, time.March, 15), "second advance")
	assertNoError(t, err)

	after, err := p.GetFacility(ctx, loan.ID)
	assertNoError(t, err)
	assertDate(t, "LastAccrualDate across a re-disbursement", after.LastAccrualDate, ahead)
	assertEqual(t, "AccruedGross across a re-disbursement", after.AccruedGross, charged.AccruedGross)
	assertEqual(t, "Accrued across a re-disbursement", after.Accrued, settled.Accrued)

	// And the timeline still holds one row, the opening one: disbursing does not
	// reprice anything.
	rows, err := p.FacilityTermsHistory(ctx, loan.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline length after a re-disbursement", len(rows), 1)
}

// SetFacilityTerms refuses a term loan that has a generated schedule, and allows
// a revolving line (which has none) and an undisbursed term loan (which has none
// yet).
//
// Refusing is better than documenting a divergence nobody would see: a repriced
// term loan whose schedule still reflected the old rate would let the final
// instalment silently absorb the difference, unnoticed until maturity.
func TestSetFacilityTermsRefusesADisbursedTermLoan(t *testing.T) {
	ctx := context.Background()

	// Disbursed term loan: refused, and the timeline is unchanged. The fixture's
	// subledger comes back too, so the two facilities below are opened in the
	// same book rather than in one of their own.
	p, _, sub, loan, _ := disbursedLoanIn(t)
	_, err := p.SetFacilityTerms(ctx, loan.ID, 90_000, interest.ACT365, ledger.DayStart(loan.OpenedAt))
	if !errors.Is(err, lending.ErrScheduleWouldDiverge) {
		t.Errorf("repricing a disbursed term loan: got %v, want ErrScheduleWouldDiverge", err)
	}
	rows, err := p.FacilityTermsHistory(ctx, loan.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline after a refused repricing", len(rows), 1)

	// Undisbursed term loan: allowed, because there is no schedule yet — and the
	// schedule generated later uses the newer rate, which is right and may still
	// surprise someone reading only the origination record.
	pending := openUndisbursedTermLoan(t, p, sub)
	_, err = p.SetFacilityTerms(ctx, pending.ID, 90_000, interest.ACT365, ledger.DayStart(pending.OpenedAt))
	assertNoError(t, err)

	// Revolving line: allowed, because it has no schedule to diverge from. Its
	// instalments are billing cycles appended one at a time, and a cycle already
	// billed is a statement of what WAS charged rather than a plan.
	line := openLine(t, p, sub)
	_, err = p.SetFacilityTerms(ctx, line.ID, 200_000, interest.ACT365, ledger.DayStart(line.OpenedAt))
	assertNoError(t, err)
}

// SetFacilityTerms refuses a FUTURE-DATED repricing on a term loan, even one
// with no schedule at all — and that is not a second-guess of the schedule
// guard, it is the case that guard cannot see.
//
// DisburseTx pins the schedule to the row in force ON THE DISBURSEMENT DAY. A row
// effective after that day is invisible to the schedule and reached by the
// accrual anyway, so allowing it would produce exactly the divergence
// ErrScheduleWouldDiverge exists to make unreachable:
//
//	open at 6% -> reprice to 24% effective day 30 -> disburse on day 0
//	-> schedule pinned at 6%, accrual steps to 24% on day 30, and the loan now
//	   HAS a schedule so it can never be repriced back
//
// The second half of this test is the arithmetic of that divergence, run against
// a revolving line — which is allowed to be future-dated, having no schedule —
// so the figures are not hypothetical: they are what a term loan would have done.
func TestSetFacilityTermsRefusesAFutureDatedTermLoanRepricing(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	dayN := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	const (
		opening interest.Rate = 60_000  // 6%
		dear    interest.Rate = 240_000 // 24%
	)

	clock := &mutableClock{at: dayN(0)}
	p, _, sub, customer := newTestPortfolioOn(t, clock.now)

	loan, err := p.OpenTermLoan(ctx, sub, "Alice Home Loan", "EUR",
		1_000_000, opening, interest.ACT365, lending.Annuity, 12)
	assertNoError(t, err)

	// Undisbursed, no schedule — and still refused, because the row would be
	// effective past the day any schedule would be pinned at.
	_, err = p.SetFacilityTerms(ctx, loan.ID, dear, interest.ACT365, dayN(30))
	assertErrorIs(t, err, lending.ErrScheduleWouldDiverge)
	rows, err := p.FacilityTermsHistory(ctx, loan.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline after a refused future-dated repricing", len(rows), 1)
	assertEqual(t, "the rate in force is untouched", rows[0].Rate, opening)

	// Effective TODAY is allowed, and effective in the PAST is allowed: neither
	// can be a day the schedule fails to see, because the schedule is pinned at
	// the disbursement day and the clock only moves forward from here.
	_, err = p.SetFacilityTerms(ctx, loan.ID, dear, interest.ACT365, dayN(0))
	assertNoError(t, err)
	_, err = p.SetFacilityTerms(ctx, loan.ID, dear, interest.ACT365, dayN(-5))
	assertNoError(t, err)

	// And the allowed repricing really did reach the schedule: 24% on €10,000 is
	// a first month's scheduled interest of 1_000_000 × 240_000 / 12_000_000 =
	// 20_000, against 5_000 at the rate the loan was opened at.
	_, err = p.Disburse(ctx, loan.ID, customer, lending.AddMonths(dayN(0), 1), "advance")
	assertNoError(t, err)
	schedule, err := p.Schedule(ctx, loan.ID)
	assertNoError(t, err)
	assertEqual(t, "first instalment interest at the repriced rate", schedule[0].Interest, ledger.Amount(20_000))

	// Now the loan has a schedule, so it is refused on the first guard too —
	// including for a date that would have been fine a moment ago.
	_, err = p.SetFacilityTerms(ctx, loan.ID, opening, interest.ACT365, dayN(0))
	assertErrorIs(t, err, lending.ErrScheduleWouldDiverge)

	// A revolving line keeps future-dating, which is scheduled repricing for
	// free: it has no schedule for a later row to get ahead of.
	line := openLine(t, p, sub)
	_, err = p.SetFacilityTerms(ctx, line.ID, dear, interest.ACT365, dayN(30))
	assertNoError(t, err)
	lineRows, err := p.FacilityTermsHistory(ctx, line.ID)
	assertNoError(t, err)
	assertEqual(t, "a line's timeline takes a future-dated row", len(lineRows), 2)
}

// A zero effectiveFrom means TODAY on the portfolio's clock, the same mapping
// deposit.SetOverdraftTermsTx makes.
//
// It is load-bearing rather than tidy. An unmapped zero time day-truncates to
// 0001-01-01, sorts to the FRONT of the timeline, and becomes the day accrual
// opens its recompute window at — so one such row turns every nightly run into a
// walk over two millennia of days for that facility. Reading the INJECTED clock
// rather than the wall clock is the other half: api and seed run frozen, and a
// wall-clock day would be a future-dated row nothing those runs ever price at.
func TestSetFacilityTermsMapsAZeroEffectiveDateToToday(t *testing.T) {
	ctx := context.Background()

	clock := &mutableClock{at: day(2025, time.March, 10)}
	p, _, sub, _ := newTestPortfolioOn(t, clock.now)
	line := openLine(t, p, sub)

	// Part-way through a later day, so the truncation is exercised too and the
	// row cannot collide with the opening one's day key.
	clock.set(time.Date(2025, time.March, 20, 14, 30, 0, 0, time.UTC))

	row, err := p.SetFacilityTerms(ctx, line.ID, 90_000, interest.ACT365, time.Time{})
	assertNoError(t, err)
	assertDate(t, "a zero effective date resolves to today", row.EffectiveFrom, day(2025, time.March, 20))

	// And it lands at the END of the timeline rather than in front of the
	// opening row, which is what year 1 would have done.
	rows, err := p.FacilityTermsHistory(ctx, line.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline length after a zero-dated repricing", len(rows), 2)
	assertDate(t, "the opening row is still first", rows[0].EffectiveFrom, day(2025, time.March, 10))
	assertDate(t, "the new row is last", rows[1].EffectiveFrom, day(2025, time.March, 20))
}

// A line stays repriceable after its first billing cycle, which is what keying
// the guard on Kind before the schedule buys: a charged line DOES have
// instalment rows, and a guard that only counted them would have locked every
// line the moment it was billed once.
func TestSetFacilityTermsAllowsALineThatHasBeenBilled(t *testing.T) {
	ctx := context.Background()
	p, _, sub, customer := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, sub, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	assertNoError(t, err)
	_, err = p.Draw(ctx, line.ID, customer, 100_000, "First draw")
	assertNoError(t, err)
	charge, err := p.ChargeInterest(ctx, line.ID, day(2025, time.January, 15))
	assertNoError(t, err)
	if !charge.Billed() {
		t.Fatal("the cycle was not billed, so this test proves nothing")
	}
	schedule, err := p.Schedule(ctx, line.ID)
	assertNoError(t, err)
	assertEqual(t, "instalment rows on a billed line", len(schedule), 1)

	_, err = p.SetFacilityTerms(ctx, line.ID, 200_000, interest.ACT365, day(2025, time.February, 1))
	assertNoError(t, err)
	rows, err := p.FacilityTermsHistory(ctx, line.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline on a repriced line", len(rows), 2)
}

// A closed facility cannot be repriced, and an unknown one is not found. Both
// guards are ahead of the schedule check, so neither can be reached by way of it.
func TestSetFacilityTermsRejects(t *testing.T) {
	ctx := context.Background()
	p, _, sub, customer := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, sub, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	assertNoError(t, err)

	// A negative rate is not a product, whatever the facility's state.
	_, err = p.SetFacilityTerms(ctx, line.ID, -1, interest.ACT365, day(2025, time.January, 15))
	assertErrorIs(t, err, lending.ErrInvalidRate)

	_, err = p.Draw(ctx, line.ID, customer, 100_000, "draw")
	assertNoError(t, err)
	_, err = p.Repay(ctx, line.ID, customer, 100_000, day(2025, time.January, 15), "settle")
	assertNoError(t, err)
	assertNoError(t, p.Close(ctx, line.ID))

	_, err = p.SetFacilityTerms(ctx, line.ID, 200_000, interest.ACT365, day(2025, time.February, 1))
	assertErrorIs(t, err, lending.ErrFacilityClosed)

	_, err = p.SetFacilityTerms(ctx, "fac_nope", 200_000, interest.ACT365, day(2025, time.February, 1))
	assertErrorIs(t, err, lending.ErrFacilityNotFound)
}

// openUndisbursedTermLoan opens a term loan and leaves it Pending: no money out,
// and therefore no schedule.
func openUndisbursedTermLoan(t *testing.T, p *lending.Portfolio, sub ledger.SubledgerID) lending.Facility {
	t.Helper()
	loan, err := p.OpenTermLoan(context.Background(), sub, "Bob Home Loan", "EUR",
		1_000_000, 60_000, interest.ACT365, lending.Annuity, 60)
	assertNoError(t, err)
	return loan
}

// openLine opens an undrawn revolving line.
func openLine(t *testing.T, p *lending.Portfolio, sub ledger.SubledgerID) lending.Facility {
	t.Helper()
	line, err := p.OpenRevolvingLine(context.Background(), sub, "Bob Line", "EUR",
		250_000, 180_000, interest.ACT365, 20_000)
	assertNoError(t, err)
	return line
}

func TestPortfolio_UnknownFacility(t *testing.T) {
	ctx := context.Background()
	p, _, _, customer := newTestPortfolio(t)

	_, err := p.GetFacility(ctx, "fac_nope")
	assertErrorIs(t, err, lending.ErrFacilityNotFound)

	_, err = p.Draw(ctx, "fac_nope", customer, 100, "draw")
	assertErrorIs(t, err, lending.ErrFacilityNotFound)

	_, err = p.Disburse(ctx, "fac_nope", customer, time.Now(), "disburse")
	assertErrorIs(t, err, lending.ErrFacilityNotFound)
}

func assertErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got error %v, want %v", err, target)
	}
}

// assertNoError, assertEqual and assertDate mirror the in-package helpers in
// schedule_test.go. They are duplicated rather than shared because that file is
// `package lending` and this one is `package lending_test`, and a test helper is
// not worth an exported symbol.
func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", label, got, want)
	}
}

func assertDate(t *testing.T, label string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s: got %v, want %v", label, got, want)
	}
}
