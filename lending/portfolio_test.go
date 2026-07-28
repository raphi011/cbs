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

// newTestPortfolio returns a portfolio over a fresh in-memory store, the book
// it composes with, a subledger to file facilities in, and a Liability account
// to disburse into — standing in for a customer's current account.
func newTestPortfolio(t *testing.T) (*lending.Portfolio, *ledger.Book, ledger.SubledgerID, ledger.AccountID) {
	t.Helper()
	ctx := context.Background()
	clock := func() time.Time { return time.Date(2025, time.January, 15, 9, 0, 0, 0, time.UTC) }

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
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := p.OpenTermLoan(ctx, sub, "Loan", "EUR", tt.principal, tt.rate, interest.ACT365, lending.Annuity, tt.months)
			assertErrorIs(t, err, tt.want)
		})
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
	// Accrual starts from disbursement, not from opening: money not yet paid
	// out earns nothing, so the clock is set here and not at OpenTermLoan.
	if after.LastAccrualDate.IsZero() {
		t.Error("last accrual date was not set at disbursement")
	}
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
