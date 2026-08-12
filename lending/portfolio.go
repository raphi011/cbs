package lending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// loansSubledgerName is where facilities' principal and receivable lines are
// filed.
const loansSubledgerName = "Loans and Advances"

// incomeSubledgerName and interestIncomeName mirror the deposit layer's. The
// two are deliberately not shared: lending does not import deposit, and a
// shared constant would be the first thread of exactly that dependency.
const incomeSubledgerName = "Income"

// payablesSubledgerName is where the bank's own obligations to borrowers are
// filed.
const payablesSubledgerName = "Payables"

// The four slots this layer posts to.
var (
	principalSlot  = ledger.Slot{Key: "lending.principal", Type: ledger.Asset, Control: true}
	receivableSlot = ledger.Slot{Key: "lending.interest_receivable", Type: ledger.Asset, Control: true}
	payableSlot    = ledger.Slot{Key: "lending.interest_refunds_payable", Type: ledger.Liability, Control: true}
	incomeSlot     = ledger.Slot{Key: "lending.interest_income", Type: ledger.Revenue, ByProduct: true}
)

// The names the first facility in an asset opens those four lines under: the
// bootstrap, and not the resolution.
func loanPrincipalName(asset ledger.AssetCode) string {
	return "Loan Principal (" + string(asset) + ")"
}

func accruedInterestName(asset ledger.AssetCode) string {
	return "Accrued Interest (" + string(asset) + ")"
}

func interestRefundPayableName(asset ledger.AssetCode) string {
	return "Interest Refunds Payable (" + string(asset) + ")"
}

func interestIncomeName(asset ledger.AssetCode) string {
	return "Interest Income (" + string(asset) + ")"
}

// Portfolio is the lending layer over a general ledger. It manages credit
// facilities, their schedules, their interest and their arrears.
type Portfolio struct {
	store  Store
	gl     *ledger.Book
	bookID ledger.BookID
	clock  func() time.Time

	// customers is the subledger this portfolio hangs its own folders off.
	customers ledger.SubledgerID
}

// NewPortfolio creates a lending portfolio over the given store, layered on the
// given general ledger.
func NewPortfolio(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time, customers ledger.SubledgerID) *Portfolio {
	return &Portfolio{store: store, gl: book, bookID: id, clock: clock, customers: customers}
}

// Store returns the underlying store, so a caller spanning several layers can
// open the Update itself and drive each layer's …Tx methods with the result.
func (p *Portfolio) Store() Store { return p.store }

// BookID returns the book this portfolio is scoped to.
func (p *Portfolio) BookID() ledger.BookID { return p.bookID }

func (p *Portfolio) now() time.Time { return p.clock() }

// appendAuditTx records an immutable lending-scope event through the
// transaction, so an audit event never outlives an operation that rolled back.
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
func (p *Portfolio) OpenTermLoan(ctx context.Context, name string, asset ledger.AssetCode, principal ledger.Amount, rate interest.Rate, dc interest.DayCount, method AmortMethod, termMonths int) (Facility, error) {
	var out Facility
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.OpenTermLoanTx(ctx, tx, name, asset, principal, rate, dc, method, termMonths)
		return err
	})
	return out, err
}

// MaxTermMonths is the longest term a loan may be opened for: 600 months, or
// fifty years. See OpenTermLoan for why a term needs an upper bound at all.
const MaxTermMonths = 600

// OpenTermLoanTx is OpenTermLoan within a caller-supplied unit of work.
func (p *Portfolio) OpenTermLoanTx(ctx context.Context, tx Tx, name string, asset ledger.AssetCode, principal ledger.Amount, rate interest.Rate, dc interest.DayCount, method AmortMethod, termMonths int) (Facility, error) {
	if termMonths <= 0 || termMonths > MaxTermMonths {
		return Facility{}, ErrInvalidTerm
	}
	return p.openTx(ctx, tx, Facility{
		Kind: TermLoan, Name: name, Asset: asset, Commitment: principal,
		Method: method, TermMonths: termMonths,
	}, rate, dc)
}

// OpenRevolvingLine opens a revolving credit line: a commitment that may be
// drawn and repaid repeatedly.
func (p *Portfolio) OpenRevolvingLine(ctx context.Context, name string, asset ledger.AssetCode, limit ledger.Amount, rate interest.Rate, dc interest.DayCount, minPayment interest.Fraction) (Facility, error) {
	var out Facility
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.OpenRevolvingLineTx(ctx, tx, name, asset, limit, rate, dc, minPayment)
		return err
	})
	return out, err
}

// OpenRevolvingLineTx is OpenRevolvingLine within a caller-supplied unit of work.
func (p *Portfolio) OpenRevolvingLineTx(ctx context.Context, tx Tx, name string, asset ledger.AssetCode, limit ledger.Amount, rate interest.Rate, dc interest.DayCount, minPayment interest.Fraction) (Facility, error) {
	if minPayment < 0 {
		return Facility{}, ErrInvalidRate
	}
	return p.openTx(ctx, tx, Facility{
		Kind: RevolvingLine, Name: name, Asset: asset, Commitment: limit,
		MinPayment: minPayment,
	}, rate, dc)
}

// openTx is the shared body of both openers: validate, ensure the asset's chart
// lines, write the facility and its opening terms row, record the event.
func (p *Portfolio) openTx(ctx context.Context, tx Tx, f Facility, rate interest.Rate, dc interest.DayCount) (Facility, error) {
	if err := ledger.ValidateText("name", f.Name); err != nil {
		return Facility{}, err
	}
	if f.Commitment <= 0 {
		return Facility{}, ErrInvalidAmount
	}
	if err := (FacilityTerms{Rate: rate}).Validate(); err != nil {
		return Facility{}, err
	}

	// The first facility in an asset puts that asset's four lines in the chart of
	// accounts; the ten thousandth adds nothing to it.
	if err := p.ensureChartTx(ctx, tx, f.Asset); err != nil {
		return Facility{}, err
	}

	id, err := tx.NextID(ctx, p.bookID, "fac")
	if err != nil {
		return Facility{}, err
	}

	f.ID = FacilityID(id)
	f.Status = Pending
	f.OpenedAt = p.now()
	f.Arrears = Arrears{Bucket: Current}

	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return Facility{}, err
	}

	// Every facility gets a terms row from origination, so the recompute window
	// starts uniform across both credit layers and the timeline answers for every
	// day the facility has existed.
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
	// resolved from the timeline rather than read off the facility.
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
	// between and no span for a clamp to skip.
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
	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return ledger.Transaction{}, err
	}
	return p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		BookingDate: p.now(),
		ValueDate:   p.now(),
		Entries: []ledger.Entry{
			{AccountID: at.Principal.Account, Subsidiary: at.Principal.Subsidiary, Amount: amount, Direction: ledger.Debit},
			{AccountID: counterparty.Account, Subsidiary: counterparty.Subsidiary, Amount: amount, Direction: ledger.Credit},
		},
	})
}

// ---------------------------------------------------------------------------
// Repricing
// ---------------------------------------------------------------------------

// SetFacilityTerms reprices a facility from a given day.
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
		// Two ways a term loan's plan and its accrual can come apart, and both are
		// refused: a schedule that already exists, and a row that would be effective
		// past the day a schedule is pinned at.
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
func (p *Portfolio) drawnTx(ctx context.Context, tx Tx, f Facility) (ledger.Amount, error) {
	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return 0, err
	}
	return p.gl.BookBalanceTx(ctx, tx, at.Principal)
}

// drawnSeriesTx is the value-dated history of what the borrower owed over
// [from, to].
func (p *Portfolio) drawnSeriesTx(ctx context.Context, tx Tx, f Facility, from, to time.Time) (ledger.Series, error) {
	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return ledger.Series{}, err
	}
	return p.gl.SeriesTx(ctx, tx, at.Principal, from, to)
}

// receivableTx is the book balance of a facility's share of the accrued-interest
// receivable — never the pool's, which holds what every other borrower owes too.
func (p *Portfolio) receivableTx(ctx context.Context, tx Tx, f Facility) (ledger.Amount, error) {
	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return 0, err
	}
	return p.gl.BookBalanceTx(ctx, tx, at.Receivable)
}

// FacilityPositions is where one facility's money is: what it has drawn, what
// it owes in interest, and what the bank owes it back.
type FacilityPositions struct {
	Principal  ledger.Position
	Receivable ledger.Position
	Payable    ledger.Position
}

// Positions is FacilityPositions for one facility. Returns ErrFacilityNotFound,
// and ledger.ErrAccountNotFound if the bank holds no lines for its asset.
func (p *Portfolio) Positions(ctx context.Context, id FacilityID) (FacilityPositions, error) {
	var out FacilityPositions
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		f, err := tx.GetFacility(ctx, p.bookID, id)
		if err != nil {
			return err
		}
		out, err = p.accountsTx(ctx, tx, f)
		return err
	})
	return out, err
}

// accountsTx resolves all three from the slot mapping. openTx mapped them when
// the first facility in the asset was opened, so ErrSlotNotMapped here is a
// chart that has been tampered with rather than a first use — which is why
// these RESOLVE and only openTx ensures.
func (p *Portfolio) accountsTx(ctx context.Context, tx Tx, f Facility) (FacilityPositions, error) {
	subsidiary := string(f.ID)
	principal, err := p.gl.SlotPositionTx(ctx, tx, "", principalSlot, f.Asset, subsidiary)
	if err != nil {
		return FacilityPositions{}, err
	}
	receivable, err := p.gl.SlotPositionTx(ctx, tx, "", receivableSlot, f.Asset, subsidiary)
	if err != nil {
		return FacilityPositions{}, err
	}
	payable, err := p.gl.SlotPositionTx(ctx, tx, "", payableSlot, f.Asset, subsidiary)
	if err != nil {
		return FacilityPositions{}, err
	}
	return FacilityPositions{Principal: principal, Receivable: receivable, Payable: payable}, nil
}

// ledgerIDTx resolves the ledger this portfolio's folders hang off.
func (p *Portfolio) ledgerIDTx(ctx context.Context, tx Tx) (ledger.LedgerID, error) {
	sub, err := tx.GetSubledger(ctx, p.bookID, p.customers)
	if err != nil {
		return "", err
	}
	return sub.LedgerID, nil
}

// ensureChartTx opens the four lines an asset's facilities post to and maps
// this layer's slots onto them, on the first facility opened in that asset.
func (p *Portfolio) ensureChartTx(ctx context.Context, tx Tx, asset ledger.AssetCode) error {
	switch _, err := p.gl.SlotAccountTx(ctx, tx, "", principalSlot, asset); {
	case err == nil:
		return nil
	case !errors.Is(err, ledger.ErrSlotNotMapped):
		return err
	}

	ledgerID, err := p.ledgerIDTx(ctx, tx)
	if err != nil {
		return err
	}
	loans, err := p.gl.EnsureSubledgerTx(ctx, tx, ledgerID, loansSubledgerName)
	if err != nil {
		return err
	}
	for _, line := range []struct {
		slot      ledger.Slot
		subledger ledger.SubledgerID
		name      string
	}{
		{principalSlot, loans.ID, loanPrincipalName(asset)},
		{receivableSlot, loans.ID, accruedInterestName(asset)},
	} {
		account, err := p.gl.EnsureControlAccountTx(ctx, tx, line.subledger, line.name, line.slot.Type, asset)
		if err != nil {
			return err
		}
		if err := p.gl.MapSlotTx(ctx, tx, "", line.slot, asset, account.ID); err != nil {
			return err
		}
	}
	payables, err := p.gl.EnsureSubledgerTx(ctx, tx, ledgerID, payablesSubledgerName)
	if err != nil {
		return err
	}
	payable, err := p.gl.EnsureControlAccountTx(ctx, tx, payables.ID, interestRefundPayableName(asset), payableSlot.Type, asset)
	if err != nil {
		return err
	}
	if err := p.gl.MapSlotTx(ctx, tx, "", payableSlot, asset, payable.ID); err != nil {
		return err
	}
	incomeSub, err := p.gl.EnsureSubledgerTx(ctx, tx, ledgerID, incomeSubledgerName)
	if err != nil {
		return err
	}
	income, err := p.gl.EnsureAccountTx(ctx, tx, incomeSub.ID, interestIncomeName(asset), incomeSlot.Type, asset)
	if err != nil {
		return err
	}
	return p.gl.MapSlotTx(ctx, tx, "", incomeSlot, asset, income.ID)
}

// incomeTx resolves the bank's interest-income line for an asset. Plain rather
// than control: what the bank has earned is the bank's own and stands in for
// nobody.
func (p *Portfolio) incomeTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (ledger.AccountID, error) {
	return p.gl.SlotAccountTx(ctx, tx, "", incomeSlot, asset)
}
