package lending

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// loansSubledgerName is where facilities' principal and receivable accounts are
// filed. Its own folder, not the customer-deposit one: that folder is what a
// bank's deposit total aggregates over, and an Asset account in it would be a
// standing invitation to sum the wrong rows.
const loansSubledgerName = "Loans and Advances"

// incomeSubledgerName and interestIncomeName mirror the deposit layer's. The
// two are deliberately not shared: lending does not import deposit, and a
// shared constant would be the first thread of exactly that dependency. Both
// resolve the same account by name, so a bank ends up with one interest-income
// account per asset however its interest was earned.
const incomeSubledgerName = "Income"

func interestIncomeName(asset ledger.AssetCode) string {
	return "Interest Income (" + string(asset) + ")"
}

// payablesSubledgerName is where the bank's own obligations to customers are
// filed. Its own folder rather than Income or Loans and Advances: those two are
// aggregated as revenue and as assets, and a Liability filed in either would be
// summed with the wrong sign by anything that totals a folder.
const payablesSubledgerName = "Payables"

func interestRefundPayableName(asset ledger.AssetCode) string {
	return "Interest Refunds Payable (" + string(asset) + ")"
}

// Portfolio is the lending layer over a general ledger. It manages credit
// facilities, their schedules, their interest and their arrears.
//
// It has the same shape as deposit.Register: it owns no state of its own, keeps
// only the store handle, the ledger.Book it composes with, the BookID both are
// scoped to, and its clock. See the package doc for units of work and for why
// an arranged overdraft is not managed here.
type Portfolio struct {
	store  Store
	gl     *ledger.Book
	bookID ledger.BookID
	clock  func() time.Time
}

// NewPortfolio creates a lending portfolio over the given store, layered on the
// given general ledger.
//
// id must be book.ID(): the portfolio's rows and the book's rows are scoped by
// the same BookID, which is what lets one Tx read both. Share the clock with
// the backing ledger.Book so audit timestamps line up across layers.
func NewPortfolio(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time) *Portfolio {
	return &Portfolio{store: store, gl: book, bookID: id, clock: clock}
}

// Store returns the underlying store, so a caller spanning several layers can
// open the Update itself and drive each layer's …Tx methods with the result.
func (p *Portfolio) Store() Store { return p.store }

// BookID returns the book this portfolio is scoped to.
func (p *Portfolio) BookID() ledger.BookID { return p.bookID }

func (p *Portfolio) now() time.Time { return p.clock() }

// appendAuditTx records an immutable lending-scope event through the
// transaction, so an audit event never outlives an operation that rolled back.
// The payload is marshalled now rather than held by reference, so later
// mutation of the facility cannot rewrite history.
func (p *Portfolio) appendAuditTx(ctx context.Context, tx Tx, eventType, entityID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit %s: marshal payload: %w", eventType, err)
	}
	id, err := tx.NextID(ctx, p.bookID, "evt")
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, ledger.AuditEvent{
		ID:         id,
		BookID:     p.bookID,
		Scope:      ledger.ScopeLending,
		Type:       eventType,
		EntityID:   entityID,
		Payload:    raw,
		OccurredAt: p.now(),
	})
}

// ---------------------------------------------------------------------------
// Opening
// ---------------------------------------------------------------------------

// OpenTermLoan opens a term loan: a fixed principal, disbursed once, amortized
// over termMonths instalments.
//
// It creates the loan's two Asset GL accounts and records the facility as
// Pending. It does NOT generate the schedule and does not move any money: the
// first due date is not known until the money is paid out, and a schedule for
// money that was never disbursed is a plan to repay nothing.
//
// principal is the committed amount; rate is the annual rate; dc is the
// day-count convention interest accrues under.
//
// termMonths is bounded at MaxTermMonths as well as below at 1. The term is an
// ALLOCATION: BuildSchedule writes one instalment row per month, so an
// unbounded term is an unbounded allocation driven by a request field —
// {"termMonths": 100000000} would try to build a hundred million rows and take
// the process down past where recoverPanic can turn it into a 500. Fifty years
// is longer than any amortizing retail loan (a Japanese multi-generation
// mortgage tops out near it), so nothing real is refused.
//
// Returns ErrInvalidAmount, ErrInvalidRate, ErrInvalidTerm, and any error from
// the ledger — ledger.ErrSubledgerNotFound, or ledger.ErrAssetNotFound if the
// asset is not one the system knows.
func (p *Portfolio) OpenTermLoan(ctx context.Context, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, principal ledger.Amount, rate interest.Rate, dc interest.DayCount, method AmortMethod, termMonths int) (Facility, error) {
	var out Facility
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.OpenTermLoanTx(ctx, tx, subledger, name, asset, principal, rate, dc, method, termMonths)
		return err
	})
	return out, err
}

// MaxTermMonths is the longest term a loan may be opened for: 600 months, or
// fifty years. See OpenTermLoan for why a term needs an upper bound at all.
const MaxTermMonths = 600

// OpenTermLoanTx is OpenTermLoan within a caller-supplied unit of work.
func (p *Portfolio) OpenTermLoanTx(ctx context.Context, tx Tx, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, principal ledger.Amount, rate interest.Rate, dc interest.DayCount, method AmortMethod, termMonths int) (Facility, error) {
	if termMonths <= 0 || termMonths > MaxTermMonths {
		return Facility{}, ErrInvalidTerm
	}
	return p.openTx(ctx, tx, Facility{
		Kind: TermLoan, Name: name, Asset: asset, Commitment: principal,
		Rate: rate, DayCount: dc, Method: method, TermMonths: termMonths,
	}, subledger)
}

// OpenRevolvingLine opens a revolving credit line: a commitment that may be
// drawn and repaid repeatedly.
//
// minPayment is the share of drawn principal added to each billing cycle's
// minimum payment, on top of the interest charged that cycle. It is a
// dimensionless Fraction rather than a Rate, because it is not per annum.
//
// A line has no maturity and no up-front schedule; its instalments are its
// billing cycles, appended by ChargeInterest.
func (p *Portfolio) OpenRevolvingLine(ctx context.Context, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, limit ledger.Amount, rate interest.Rate, dc interest.DayCount, minPayment interest.Fraction) (Facility, error) {
	var out Facility
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.OpenRevolvingLineTx(ctx, tx, subledger, name, asset, limit, rate, dc, minPayment)
		return err
	})
	return out, err
}

// OpenRevolvingLineTx is OpenRevolvingLine within a caller-supplied unit of work.
func (p *Portfolio) OpenRevolvingLineTx(ctx context.Context, tx Tx, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, limit ledger.Amount, rate interest.Rate, dc interest.DayCount, minPayment interest.Fraction) (Facility, error) {
	if minPayment < 0 {
		return Facility{}, ErrInvalidRate
	}
	return p.openTx(ctx, tx, Facility{
		Kind: RevolvingLine, Name: name, Asset: asset, Commitment: limit,
		Rate: rate, DayCount: dc, MinPayment: minPayment,
	}, subledger)
}

// openTx is the shared body of both openers: validate, create the two GL
// accounts, write the facility, record the event.
func (p *Portfolio) openTx(ctx context.Context, tx Tx, f Facility, subledger ledger.SubledgerID) (Facility, error) {
	if err := ledger.ValidateText("name", f.Name); err != nil {
		return Facility{}, err
	}
	if f.Commitment <= 0 {
		return Facility{}, ErrInvalidAmount
	}
	if f.Rate < 0 {
		return Facility{}, ErrInvalidRate
	}

	// The facility's own accounts go in a Loans and Advances subledger under
	// whichever ledger the caller's subledger belongs to, so a bank ends up
	// with one such folder however many facilities it writes.
	callerSub, err := tx.GetSubledger(ctx, p.bookID, subledger)
	if err != nil {
		return Facility{}, err
	}
	loans, err := p.gl.EnsureSubledgerTx(ctx, tx, callerSub.LedgerID, loansSubledgerName)
	if err != nil {
		return Facility{}, err
	}

	principalGL, err := p.gl.CreateAccountTx(ctx, tx, loans.ID,
		"Loan Principal: "+f.Name+" ("+string(f.Asset)+")", ledger.Asset, f.Asset)
	if err != nil {
		return Facility{}, err
	}
	interestGL, err := p.gl.CreateAccountTx(ctx, tx, loans.ID,
		"Accrued Interest: "+f.Name+" ("+string(f.Asset)+")", ledger.Asset, f.Asset)
	if err != nil {
		return Facility{}, err
	}

	id, err := tx.NextID(ctx, p.bookID, "fac")
	if err != nil {
		return Facility{}, err
	}

	f.ID = FacilityID(id)
	f.PrincipalGL = principalGL.ID
	f.InterestGL = interestGL.ID
	f.Status = Pending
	f.OpenedAt = p.now()
	f.Arrears = Arrears{Bucket: Current}

	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return Facility{}, err
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityOpened, string(f.ID), f); err != nil {
		return Facility{}, err
	}
	return f, nil
}

// ---------------------------------------------------------------------------
// Advancing money
// ---------------------------------------------------------------------------

// Disburse pays out a term loan's principal in full and generates its schedule.
//
//	Dr  Loan principal (Asset)      1_000_000
//	  Cr counterparty                 1_000_000
//
// counterparty is any GL account in the facility's asset — a customer's current
// account, or the vault for a cash advance. This layer does not know what a
// deposit account is, so a disbursement that must also respect one's status is
// orchestrated a layer up, through the same Tx.
//
// firstDue is the due date of the first instalment; the rest follow monthly,
// clamped to the end of the month. Accrual starts from the disbursement date:
// money not yet paid out earns nothing.
//
// Returns ErrFacilityNotFound, ErrFacilityClosed, ErrWrongFacilityKind for a
// revolving line, ErrAlreadyDisbursed for a second call while principal is
// still outstanding, and any ledger error — notably
// ledger.ErrUnbalancedTransaction if the counterparty is in a different
// asset, which is how a cross-asset disbursement is caught.
func (p *Portfolio) Disburse(ctx context.Context, id FacilityID, counterparty ledger.AccountID, firstDue time.Time, description string) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.DisburseTx(ctx, tx, id, counterparty, firstDue, description)
		return err
	})
	return out, err
}

// DisburseTx is Disburse within a caller-supplied unit of work.
func (p *Portfolio) DisburseTx(ctx context.Context, tx Tx, id FacilityID, counterparty ledger.AccountID, firstDue time.Time, description string) (ledger.Transaction, error) {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if f.Status == Closed {
		return ledger.Transaction{}, ErrFacilityClosed
	}
	if f.Kind != TermLoan {
		return ledger.Transaction{}, ErrWrongFacilityKind
	}
	drawn, err := p.drawnTx(ctx, tx, f)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if drawn != 0 {
		return ledger.Transaction{}, ErrAlreadyDisbursed
	}

	glTx, err := p.advanceTx(ctx, tx, f, counterparty, f.Commitment, description)
	if err != nil {
		return ledger.Transaction{}, err
	}

	schedule := BuildSchedule(f, f.Commitment, firstDue)
	for _, inst := range schedule {
		inst.FacilityID = f.ID
		if err := tx.PutInstallment(ctx, p.bookID, inst); err != nil {
			return ledger.Transaction{}, err
		}
	}

	f.Status = Active
	// Disbursement opens the accrual window. Nothing was owed before it, so
	// there is no earlier span for the recompute to reach back over and none
	// can fall between two windows: this is the first one.
	//
	// "First" is not quite guaranteed, though: the guard above is on drawn
	// principal, not on status, so a term loan repaid in full and not closed
	// can be disbursed again. If interest had by then been accrued through a
	// date ahead of the wall clock — end-of-day takes its date from the caller
	// — reopening the window at now would put it BEHIND a charged span with
	// AccruedGross zeroed, and the next run would add that span to Accrued a
	// second time. Clamping forward keeps LastAccrualDate's documented
	// never-backwards invariant true, and is a no-op on the ordinary first
	// advance, where LastAccrualDate is still zero.
	now := p.now()
	if now.Before(f.LastAccrualDate) {
		now = f.LastAccrualDate
	}
	f.LastAccrualDate = now
	f.TermsEffectiveFrom = now
	f.AccruedGross = 0
	if n := len(schedule); n > 0 {
		f.MaturityAt = schedule[n-1].DueDate
	}
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return ledger.Transaction{}, err
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityDisbursed, string(f.ID), map[string]any{
		"facility_id":    string(f.ID),
		"amount":         f.Commitment,
		"transaction_id": string(glTx.ID),
		"installments":   len(schedule),
	}); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, nil
}

// Draw advances part of a revolving line's commitment. It may be called
// repeatedly — that is what revolving means — as long as drawn principal stays
// within the commitment.
//
// Returns ErrFacilityNotFound, ErrFacilityClosed, ErrWrongFacilityKind for a
// term loan, ErrInvalidAmount, and ErrLimitExceeded.
func (p *Portfolio) Draw(ctx context.Context, id FacilityID, counterparty ledger.AccountID, amount ledger.Amount, description string) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.DrawTx(ctx, tx, id, counterparty, amount, description)
		return err
	})
	return out, err
}

// DrawTx is Draw within a caller-supplied unit of work.
func (p *Portfolio) DrawTx(ctx context.Context, tx Tx, id FacilityID, counterparty ledger.AccountID, amount ledger.Amount, description string) (ledger.Transaction, error) {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if f.Status == Closed {
		return ledger.Transaction{}, ErrFacilityClosed
	}
	if f.Kind != RevolvingLine {
		return ledger.Transaction{}, ErrWrongFacilityKind
	}
	if amount <= 0 {
		return ledger.Transaction{}, ErrInvalidAmount
	}

	drawn, err := p.drawnTx(ctx, tx, f)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if drawn+amount > f.Commitment {
		return ledger.Transaction{}, ErrLimitExceeded
	}

	glTx, err := p.advanceTx(ctx, tx, f, counterparty, amount, description)
	if err != nil {
		return ledger.Transaction{}, err
	}

	if f.Status == Pending {
		f.Status = Active
		// A line accrues from its first draw, not from opening: an undrawn
		// commitment costs the borrower nothing. That first draw is therefore
		// also where the recompute window opens, and a later draw leaves it
		// alone — the window spans the whole drawn history, which is the point.
		//
		// No clamp is needed here, unlike in DisburseTx: this branch only runs
		// while the line is still Pending, which means nothing has ever been
		// advanced and so nothing has ever been accrued. LastAccrualDate is
		// zero, and now cannot be before it.
		now := p.now()
		f.LastAccrualDate = now
		f.TermsEffectiveFrom = now
		f.AccruedGross = 0
		if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
			return ledger.Transaction{}, err
		}
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityDrawn, string(f.ID), map[string]any{
		"facility_id":    string(f.ID),
		"amount":         amount,
		"transaction_id": string(glTx.ID),
	}); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, nil
}

// advanceTx posts money out of the bank and onto a facility's principal.
func (p *Portfolio) advanceTx(ctx context.Context, tx Tx, f Facility, counterparty ledger.AccountID, amount ledger.Amount, description string) (ledger.Transaction, error) {
	if description == "" {
		description = "Advance: " + f.Name
	}
	return p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		BookingDate: p.now(),
		ValueDate:   p.now(),
		Entries: []ledger.Entry{
			{AccountID: f.PrincipalGL, Amount: amount, Direction: ledger.Debit},
			{AccountID: counterparty, Amount: amount, Direction: ledger.Credit},
		},
	})
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

// GetFacility retrieves a facility. Returns ErrFacilityNotFound.
func (p *Portfolio) GetFacility(ctx context.Context, id FacilityID) (Facility, error) {
	var out Facility
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetFacility(ctx, p.bookID, id)
		return err
	})
	return out, err
}

// ListFacilities returns every facility in the book, oldest first.
func (p *Portfolio) ListFacilities(ctx context.Context) ([]Facility, error) {
	var out []Facility
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListFacilities(ctx, p.bookID)
		return err
	})
	return out, err
}

// Schedule returns a facility's instalments in contract order. A facility with
// no schedule — a term loan before disbursement, a line before its first
// billing cycle — returns an empty slice rather than an error.
func (p *Portfolio) Schedule(ctx context.Context, id FacilityID) ([]Installment, error) {
	var out []Installment
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListInstallments(ctx, p.bookID, id)
		return err
	})
	return out, err
}

// Drawn is the outstanding principal on a facility.
//
// It is DERIVED — the book balance of the principal account — rather than
// stored on the facility, for the same reason no balance is stored anywhere
// else in this system: a second copy of a number is a second thing that can be
// wrong. Returns ErrFacilityNotFound.
func (p *Portfolio) Drawn(ctx context.Context, id FacilityID) (ledger.Amount, error) {
	var out ledger.Amount
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		f, err := tx.GetFacility(ctx, p.bookID, id)
		if err != nil {
			return err
		}
		out, err = p.drawnTx(ctx, tx, f)
		return err
	})
	return out, err
}

// drawnTx is Drawn against a facility the caller has already loaded. The
// principal account is an Asset, so its normal balance is Debit.
func (p *Portfolio) drawnTx(ctx context.Context, tx Tx, f Facility) (ledger.Amount, error) {
	return tx.BookBalance(ctx, p.bookID, f.PrincipalGL, ledger.Debit)
}

// drawnSeriesTx is the value-dated history of what the borrower owed over
// [from, to]. Like drawnTx it reads PrincipalGL, whose normal balance is Debit
// because a loan is an Asset; unlike drawnTx it returns each day's own figure
// rather than today's, which is what lets the accrual re-derive a past day with
// a posting that only reached the ledger after it.
//
// The bounds are snapped here: from is inclusive and to is exclusive, so a
// window that is to accrue THROUGH to must read the day to falls in.
func (p *Portfolio) drawnSeriesTx(ctx context.Context, tx Tx, f Facility, from, to time.Time) (ledger.Series, error) {
	return tx.ValueDatedSeries(ctx, p.bookID, f.PrincipalGL, ledger.Debit,
		ledger.DayStart(from), ledger.NextDay(to))
}

// receivableTx is the book balance of a facility's accrued-interest
// receivable. It is an Asset like the principal account, created alongside it
// in openTx, so it is never empty and its normal balance is Debit too.
func (p *Portfolio) receivableTx(ctx context.Context, tx Tx, f Facility) (ledger.Amount, error) {
	return tx.BookBalance(ctx, p.bookID, f.InterestGL, ledger.Debit)
}
