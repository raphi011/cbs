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

// interestRefundPayableName names a facility's own refunds-payable account, and
// is keyed on the facility rather than only on its asset — unlike
// interestIncomeName above, which is one account per asset however many
// facilities feed it.
//
// The difference is what each balance has to answer. Interest income is a
// bank-wide total and nothing ever needs it split by borrower. A refund payable
// is a debt to ONE borrower that somebody eventually has to pay back, so the
// figure only means anything per facility: pooled, the account holds one number
// that cannot say who is owed what, and a discharge against it is unbounded —
// it could pay one borrower out of another's money and still balance, because a
// Liability is never caught by the sufficiency check.
//
// It is a subsidiary ledger, in other words, with the Payables folder's total as
// the control figure. It shares the naming of a facility's other two accounts
// ("Loan Principal: …", "Accrued Interest: …") because it is the same kind of
// thing: an account belonging to one facility.
func interestRefundPayableName(name string, asset ledger.AssetCode) string {
	return "Interest Refunds Payable: " + name + " (" + string(asset) + ")"
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
// day-count convention interest accrues under. The last two are written as the
// facility's OPENING TERMS ROW rather than onto the facility itself — terms are
// effective-dated, and this is the row in force from origination onwards. Use
// SetFacilityTerms to reprice afterwards.
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
		Method: method, TermMonths: termMonths,
	}, rate, dc, subledger)
}

// OpenRevolvingLine opens a revolving credit line: a commitment that may be
// drawn and repaid repeatedly.
//
// minPayment is the share of drawn principal added to each billing cycle's
// minimum payment, on top of the interest charged that cycle. It is a
// dimensionless Fraction rather than a Rate, because it is not per annum.
//
// rate and dc become the line's opening terms row, exactly as in OpenTermLoan;
// SetFacilityTerms reprices it afterwards, and a line may be repriced freely
// because it has no generated schedule to diverge from.
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
		MinPayment: minPayment,
	}, rate, dc, subledger)
}

// openTx is the shared body of both openers: validate, create the two GL
// accounts, write the facility and its opening terms row, record the event.
//
// rate and dc are parameters rather than fields on f because they are not fields
// on a Facility at all: they are what the facility's first FacilityTerms row
// carries.
func (p *Portfolio) openTx(ctx context.Context, tx Tx, f Facility, rate interest.Rate, dc interest.DayCount, subledger ledger.SubledgerID) (Facility, error) {
	if err := ledger.ValidateText("name", f.Name); err != nil {
		return Facility{}, err
	}
	if f.Commitment <= 0 {
		return Facility{}, ErrInvalidAmount
	}
	if err := (FacilityTerms{Rate: rate}).Validate(); err != nil {
		return Facility{}, err
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

	// Every facility gets a terms row from origination, so the recompute window
	// starts uniform across both credit layers and the timeline answers for every
	// day the facility has existed. Days before the first advance accrue zero
	// anyway, because drawn is zero across them, so no "first advance opens the
	// window" state is needed.
	opening := FacilityTerms{
		FacilityID:    f.ID,
		EffectiveFrom: ledger.DayStart(f.OpenedAt),
		Rate:          rate,
		DayCount:      dc,
		CreatedAt:     f.OpenedAt,
	}
	if err := tx.PutFacilityTerms(ctx, p.bookID, opening); err != nil {
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
// counterparty is any position in the facility's asset — a customer's current
// account, which is an obligor under a control account, or the vault for a cash
// advance, which is a plain account named with Total(). This layer does not know
// what a deposit account is, so a disbursement that must also respect one's
// status is orchestrated a layer up, through the same Tx.
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
func (p *Portfolio) Disburse(ctx context.Context, id FacilityID, counterparty ledger.Position, firstDue time.Time, description string) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.DisburseTx(ctx, tx, id, counterparty, firstDue, description)
		return err
	})
	return out, err
}

// DisburseTx is Disburse within a caller-supplied unit of work.
func (p *Portfolio) DisburseTx(ctx context.Context, tx Tx, id FacilityID, counterparty ledger.Position, firstDue time.Time, description string) (ledger.Transaction, error) {
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

	// The schedule is generated from the rate in force ON THE DISBURSEMENT DAY,
	// resolved from the timeline rather than read off the facility. That is the
	// moment the plan is pinned to, and a term loan repriced BEFORE disbursement
	// is disbursed on the newer rate — see SetFacilityTerms, which refuses the
	// repricing once a schedule exists precisely because the two could then
	// diverge.
	terms, err := tx.GetFacilityTermsAsOf(ctx, p.bookID, f.ID, ledger.DayStart(p.now()))
	if err != nil {
		return ledger.Transaction{}, err
	}
	schedule := BuildSchedule(f, f.Commitment, terms.Rate, firstDue)
	for _, inst := range schedule {
		inst.FacilityID = f.ID
		if err := tx.PutInstallment(ctx, p.bookID, inst); err != nil {
			return ledger.Transaction{}, err
		}
	}

	f.Status = Active
	// Disbursement does not open the accrual window: the window opens at
	// origination and never moves, so there is no boundary for a day to fall
	// between and no span for a clamp to skip. A re-disbursement therefore cannot
	// reopen the window behind a span already charged, and the span between a full
	// repayment and a re-disbursement is charged like any other: it was drawn, so
	// it was owed.
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
func (p *Portfolio) Draw(ctx context.Context, id FacilityID, counterparty ledger.Position, amount ledger.Amount, description string) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.DrawTx(ctx, tx, id, counterparty, amount, description)
		return err
	})
	return out, err
}

// DrawTx is Draw within a caller-supplied unit of work.
func (p *Portfolio) DrawTx(ctx context.Context, tx Tx, id FacilityID, counterparty ledger.Position, amount ledger.Amount, description string) (ledger.Transaction, error) {
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
		// A line accrues nothing before its first draw — an undrawn commitment costs
		// the borrower nothing — but that is arithmetic rather than state: the
		// recompute opens at origination and the drawn series is zero across every
		// day before this posting, so those days re-derive to zero on their own.
		f.Status = Active
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
func (p *Portfolio) advanceTx(ctx context.Context, tx Tx, f Facility, counterparty ledger.Position, amount ledger.Amount, description string) (ledger.Transaction, error) {
	if description == "" {
		description = "Advance: " + f.Name
	}
	return p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		BookingDate: p.now(),
		ValueDate:   p.now(),
		Entries: []ledger.Entry{
			{AccountID: f.PrincipalGL, Amount: amount, Direction: ledger.Debit},
			{AccountID: counterparty.Account, Subsidiary: counterparty.Subsidiary, Amount: amount, Direction: ledger.Credit},
		},
	})
}

// ---------------------------------------------------------------------------
// Repricing
// ---------------------------------------------------------------------------

// SetFacilityTerms reprices a facility from a given day.
//
// It appends a row to the facility's terms timeline rather than overwriting
// anything, so the rate that was in force on any past day stays resolvable and
// the next recompute re-derives each day at its own. effectiveFrom may be
// backdated or future-dated: a backdated row is picked up by the next run the
// same way a backdated posting is, and the difference is posted as ordinary
// delta interest, while a future-dated row is inert until the runs reach it.
//
// A ZERO effectiveFrom means today on the PORTFOLIO'S clock, the same mapping
// deposit.SetOverdraftTerms makes and for the same reason: it follows
// ledger.Book.PostTransaction's zero-BookingDate precedent, and it has to be
// the injected clock rather than the wall clock because api and seed run on a
// frozen one — a wall-clock day would be a future-dated row nothing those runs
// ever price at. Leaving the zero unmapped would be worse still: it truncates
// to 0001-01-01, sorts first in the timeline, and becomes the day accrual opens
// its recompute window at, which is two millennia of days walked per facility
// per nightly run.
//
// The risk in the first of those is real and is not hidden: a retroactive
// repricing MOVES INTEREST THAT HAS ALREADY BEEN CHARGED TO A BORROWER, and the
// audit log is the only control on it. Every call appends an
// EventFacilityTermsSet event carrying the row, effective date and entry date
// alike.
//
// The value returned is the row that was WRITTEN, which is not necessarily the
// row in force now — a future-dated repricing returns terms nothing is being
// priced at yet. Use GetFacilityWithTerms for the row in force today.
//
// # Why a disbursed term loan is refused
//
// A term loan's instalment schedule is generated once, at disbursement, from
// the rate in force then, and stored as rows. If accrual followed the timeline
// and the schedule did not, a repricing would make the plan and the actual
// accrual diverge — beyond the ordinary plan-versus-actual divergence this
// package already teaches, which 30/360 exists to keep small — and the final
// instalment would silently absorb the difference. Nobody would notice until
// maturity.
//
// So this returns ErrScheduleWouldDiverge for a term loan that has a generated
// schedule, and allows repricing freely on a revolving line (which has none)
// and on an undisbursed term loan (which has none yet). Refusing is better than
// documenting a divergence nobody would see; regenerating a schedule against
// repayments already posted against it needs versioned schedule rows and
// open-item allocation, and is its own topic.
//
// The first guard is on the SCHEDULE rather than on status or on drawn
// principal, which is what keeps a revolving line repriceable after its first
// billing cycle has appended instalment rows: a line's instalments are cycles
// already billed — statements of what WAS charged — not a plan a repricing could
// put out of step. Keying on Kind first is what draws that line.
//
// # Why a term loan may not be repriced into the FUTURE either
//
// "It has no schedule yet" is not enough on its own. DisburseTx pins the
// schedule to the row in force ON THE DISBURSEMENT DAY, so a row effective after
// that day is one the schedule cannot see and the accrual will reach anyway:
//
//	open at 6% -> reprice to 24% effective day 30 (no schedule yet, so allowed)
//	-> disburse on day 0 -> the schedule is pinned at 6%, and on day 30 the
//	accrual steps to 24% with no instalment reflecting it
//
// That is exactly the divergence this error exists to make unreachable, arrived
// at through the gap between the two guards, and it is worse than the case above
// because the loan then has a schedule and can no longer be repriced back. So a
// term loan additionally refuses a repricing effective AFTER the day it is
// entered. A revolving line keeps future-dating — scheduled repricing for free —
// because it has no schedule for a later row to get ahead of.
//
// The invariant that buys is: when DisburseTx pins the schedule, no row exists
// effective after the disbursement day, so termsAt resolves to the schedule's own
// rate for that day and EVERY day after it, for as long as the loan cannot be
// repriced again. It rests on the clock moving forward — a row entered today and
// effective no later than today is effective no later than any subsequent
// disbursement day — which is why the check lives here, at entry, where the
// mistake is, rather than at disbursement, where refusing would leave a facility
// that can never be disbursed at all.
//
// A term loan repriced before disbursement, effective on or before the day it is
// entered, is still allowed and still changes the schedule generated later. That
// is right and may still surprise someone reading only the origination record;
// both rows are in the audit log.
//
// Returns ErrFacilityNotFound, ErrFacilityClosed, ErrInvalidRate and
// ErrScheduleWouldDiverge.
func (p *Portfolio) SetFacilityTerms(ctx context.Context, id FacilityID, rate interest.Rate, dc interest.DayCount, effectiveFrom time.Time) (FacilityTerms, error) {
	var out FacilityTerms
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.SetFacilityTermsTx(ctx, tx, id, rate, dc, effectiveFrom)
		return err
	})
	return out, err
}

// SetFacilityTermsTx is SetFacilityTerms within a caller-supplied unit of work.
func (p *Portfolio) SetFacilityTermsTx(ctx context.Context, tx Tx, id FacilityID, rate interest.Rate, dc interest.DayCount, effectiveFrom time.Time) (FacilityTerms, error) {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return FacilityTerms{}, err
	}
	if f.Status == Closed {
		return FacilityTerms{}, ErrFacilityClosed
	}

	now := p.now()
	if effectiveFrom.IsZero() {
		effectiveFrom = now
	}
	from := ledger.DayStart(effectiveFrom)
	if f.Kind == TermLoan {
		schedule, err := tx.ListInstallments(ctx, p.bookID, id)
		if err != nil {
			return FacilityTerms{}, err
		}
		// Two ways a term loan's plan and its accrual can come apart, and both
		// are refused: a schedule that already exists, and a row that would be
		// effective past the day a schedule is pinned at. See the doc comment —
		// the second is the one reachable only through the gap between them.
		if len(schedule) > 0 {
			return FacilityTerms{}, ErrScheduleWouldDiverge
		}
		if from.After(ledger.DayStart(now)) {
			return FacilityTerms{}, ErrScheduleWouldDiverge
		}
	}

	row := FacilityTerms{
		FacilityID:    id,
		EffectiveFrom: from,
		Rate:          rate,
		DayCount:      dc,
		CreatedAt:     now,
	}
	if err := row.Validate(); err != nil {
		return FacilityTerms{}, err
	}
	if err := tx.PutFacilityTerms(ctx, p.bookID, row); err != nil {
		return FacilityTerms{}, err
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityTermsSet, string(id), row); err != nil {
		return FacilityTerms{}, err
	}
	return row, nil
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

// FacilityTermsHistory returns a facility's whole terms timeline, oldest first.
// It is the point of making terms effective-dated: the history is inspectable
// rather than merely recoverable by replaying the audit log.
//
// Returns ErrFacilityNotFound.
func (p *Portfolio) FacilityTermsHistory(ctx context.Context, id FacilityID) ([]FacilityTerms, error) {
	var out []FacilityTerms
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetFacility(ctx, p.bookID, id); err != nil {
			return err
		}
		var err error
		out, err = tx.ListFacilityTerms(ctx, p.bookID, id)
		return err
	})
	return out, err
}

// GetFacilityWithTerms returns a facility alongside the terms in force today.
// Returns ErrFacilityNotFound, and ErrTermsNotFound only for a facility that
// somehow has no opening row.
func (p *Portfolio) GetFacilityWithTerms(ctx context.Context, id FacilityID) (FacilityWithTerms, error) {
	var out FacilityWithTerms
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		f, err := tx.GetFacility(ctx, p.bookID, id)
		if err != nil {
			return err
		}
		terms, err := tx.GetFacilityTermsAsOf(ctx, p.bookID, id, ledger.DayStart(p.now()))
		if err != nil {
			return err
		}
		out = FacilityWithTerms{Facility: f, Terms: terms}
		return nil
	})
	return out, err
}

// ListFacilitiesWithTerms is GetFacilityWithTerms over the whole book, in ONE
// unit of work. Resolving each facility through its own View would make a
// listing N units of work over a store that refuses to nest them at all.
func (p *Portfolio) ListFacilitiesWithTerms(ctx context.Context) ([]FacilityWithTerms, error) {
	var out []FacilityWithTerms
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		facilities, err := tx.ListFacilities(ctx, p.bookID)
		if err != nil {
			return err
		}
		today := ledger.DayStart(p.now())
		out = make([]FacilityWithTerms, 0, len(facilities))
		for _, f := range facilities {
			terms, err := tx.GetFacilityTermsAsOf(ctx, p.bookID, f.ID, today)
			if err != nil {
				return err
			}
			out = append(out, FacilityWithTerms{Facility: f, Terms: terms})
		}
		return nil
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

// drawnTx is Drawn against a facility the caller has already loaded.
//
// These three helpers name WHICH of a facility's GL accounts answers a question;
// the ledger decides how to sign it. Passing ledger.Debit directly would be an
// assertion about an account type made from outside the layer that owns it, and
// would be silently sign-flipped by any change to AccountType.NormalBalance().
func (p *Portfolio) drawnTx(ctx context.Context, tx Tx, f Facility) (ledger.Amount, error) {
	return p.gl.BookBalanceTx(ctx, tx, f.PrincipalGL.Total())
}

// drawnSeriesTx is the value-dated history of what the borrower owed over
// [from, to]. Like drawnTx it reads PrincipalGL; unlike drawnTx it returns each
// day's own figure rather than today's, which is what lets the accrual re-derive
// a past day with a posting that only reached the ledger after it.
//
// The window bounds are snapped by SeriesTx, in the ledger, alongside the store
// contract that requires them.
func (p *Portfolio) drawnSeriesTx(ctx context.Context, tx Tx, f Facility, from, to time.Time) (ledger.Series, error) {
	return p.gl.SeriesTx(ctx, tx, f.PrincipalGL.Total(), from, to)
}

// receivableTx is the book balance of a facility's accrued-interest receivable.
// It is created alongside the principal account in openTx, so it is never empty.
func (p *Portfolio) receivableTx(ctx context.Context, tx Tx, f Facility) (ledger.Amount, error) {
	return p.gl.BookBalanceTx(ctx, tx, f.InterestGL.Total())
}
