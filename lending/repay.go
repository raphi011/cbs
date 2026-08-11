package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// Repay applies a payment to a facility, interest before principal.
//
//	Dr  counterparty                          19_333
//	  Cr Accrued interest receivable (Asset)     4_932
//	  Cr Loan principal (Asset)                  14_401
//
// # Interest means what accrued, not what the schedule projected
//
// The schedule's Interest column for that instalment is €50.00 — one twelfth of
// 6% on €10,000. Thirty days of ACT/365 accrual is €49.32. Allocating against
// the schedule would credit €50.00 of interest income that had not been earned;
// allocating against the accrual leaves the principal portion to absorb the
// difference, which is what real systems do. The schedule is a plan for due
// dates and amounts, not a statement of fact.
//
// This is exactly why the 30/360 convention exists: under it a calendar month
// accrues precisely a twelfth, and plan and actual agree to the cent.
//
// # Two allocations, one payment
//
// The LEDGER allocation is by component — receivable first, then principal —
// because those are two accounts and the payment has to say how much of it
// belongs to each.
//
// The SCHEDULE allocation is by instalment, oldest first, filling each one's
// interest then its principal until the payment runs out. That is separate
// bookkeeping about which instalments are satisfied, and it is what arrears
// reads. Keeping them separate is what lets a revolving line — whose interest
// is capitalized into principal each cycle, so nothing is left in the
// receivable — still have its minimum payments marked paid.
//
// Arrears are recomputed from the updated schedule before this returns, so a
// borrower who has just caught up is current immediately rather than at the
// next end-of-day. Recomputing is cheap and total — see ArrearsFor — which is
// what makes doing it at both moments consistent rather than duplicated.
//
// counterparty is any position in the facility's asset. This layer does not
// know what a deposit account is, so a repayment that must also respect one's
// status and available balance is orchestrated a layer up, calling
// deposit.CheckWithdrawalTx and RepayTx through the same Tx.
//
// Returns ErrFacilityNotFound, ErrFacilityClosed, ErrInvalidAmount for a
// non-positive amount or one exceeding what is owed, and ErrNothingOutstanding.
func (p *Portfolio) Repay(ctx context.Context, id FacilityID, counterparty ledger.Position, amount ledger.Amount, date time.Time, description string) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.RepayTx(ctx, tx, id, counterparty, amount, date, description)
		return err
	})
	return out, err
}

// RepayTx is Repay within a caller-supplied unit of work.
func (p *Portfolio) RepayTx(ctx context.Context, tx Tx, id FacilityID, counterparty ledger.Position, amount ledger.Amount, date time.Time, description string) (ledger.Transaction, error) {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if f.Status == Closed {
		return ledger.Transaction{}, ErrFacilityClosed
	}
	if amount <= 0 {
		return ledger.Transaction{}, ErrInvalidAmount
	}

	drawn, err := p.drawnTx(ctx, tx, f)
	if err != nil {
		return ledger.Transaction{}, err
	}
	receivable := f.Accrued.Minor()
	if receivable < 0 {
		receivable = 0
	}
	owed := drawn + receivable
	if owed <= 0 {
		return ledger.Transaction{}, ErrNothingOutstanding
	}
	// Overpaying a loan is a refund, not a repayment. Letting it through would
	// drive the principal account negative, which the ledger refuses anyway —
	// but with an error naming an imbalance rather than the mistake.
	if amount > owed {
		return ledger.Transaction{}, ErrInvalidAmount
	}

	toInterest := receivable
	if toInterest > amount {
		toInterest = amount
	}
	toPrincipal := amount - toInterest

	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return ledger.Transaction{}, err
	}
	entries := []ledger.Entry{{AccountID: counterparty.Account, Subsidiary: counterparty.Subsidiary, Amount: amount, Direction: ledger.Debit}}
	if toInterest > 0 {
		entries = append(entries, ledger.Entry{AccountID: at.Receivable.Account, Subsidiary: at.Receivable.Subsidiary, Amount: toInterest, Direction: ledger.Credit})
	}
	if toPrincipal > 0 {
		entries = append(entries, ledger.Entry{AccountID: at.Principal.Account, Subsidiary: at.Principal.Subsidiary, Amount: toPrincipal, Direction: ledger.Credit})
	}
	if description == "" {
		description = "Repayment: " + f.Name
	}
	glTx, err := p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		BookingDate: date,
		ValueDate:   date,
		Entries:     entries,
	})
	if err != nil {
		return ledger.Transaction{}, err
	}

	f.Accrued -= interest.FromMinor(toInterest)
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return ledger.Transaction{}, err
	}
	if err := p.applyToScheduleTx(ctx, tx, f, amount); err != nil {
		return ledger.Transaction{}, err
	}
	// Arrears are a pure function of the schedule, and the schedule has just
	// changed — so recompute now rather than leaving a borrower who has caught
	// up showing yesterday's bucket until the next end-of-day. It runs in this
	// same unit of work, on the facility record this method has already
	// written, so the recompute cannot survive a repayment that rolls back.
	if _, err := p.recomputeArrearsFacilityTx(ctx, tx, f, date); err != nil {
		return ledger.Transaction{}, err
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityRepaid, string(f.ID), map[string]any{
		"facility_id":    string(f.ID),
		"amount":         amount,
		"to_interest":    toInterest,
		"to_principal":   toPrincipal,
		"transaction_id": string(glTx.ID),
	}); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, nil
}

// applyToScheduleTx marks instalments satisfied, oldest first, filling each
// one's interest then its principal until the payment runs out.
//
// This is bookkeeping about the plan, not about the ledger — the money has
// already been posted by component. It is what arrears reads, which is why an
// unallocated payment would leave a facility permanently delinquent even though
// its balance was falling.
func (p *Portfolio) applyToScheduleTx(ctx context.Context, tx Tx, f Facility, amount ledger.Amount) error {
	schedule, err := tx.ListInstallments(ctx, p.bookID, f.ID)
	if err != nil {
		return err
	}
	remaining := amount
	for _, inst := range schedule {
		if remaining <= 0 {
			return nil
		}
		if inst.Outstanding() <= 0 {
			continue
		}
		if owed := inst.Interest - inst.PaidInterest; owed > 0 {
			pay := min(owed, remaining)
			inst.PaidInterest += pay
			remaining -= pay
		}
		if owed := inst.Principal - inst.PaidPrincipal; owed > 0 && remaining > 0 {
			pay := min(owed, remaining)
			inst.PaidPrincipal += pay
			remaining -= pay
		}
		if err := tx.PutInstallment(ctx, p.bookID, inst); err != nil {
			return err
		}
	}
	// A payment larger than everything scheduled is not an error: a borrower
	// may settle a loan early, and the remainder has already reduced the
	// principal in the ledger.
	return nil
}

// OutstandingOf is what a facility owes, from the two figures it is made of:
// drawn principal plus the receivable.
//
// A NEGATIVE receivable contributes nothing. It arises from a backdated
// correction that overshot — the bank owes the borrower interest back — and
// that debt runs the other way, so netting it in here would report a smaller
// loan rather than an obligation. What discharges it is RefundInterest.
//
// It takes the figures rather than reading them because its two callers hold
// them already and hold them differently: Outstanding below reads inside its own
// unit of work, and the wire renderer is handed them by a caller that resolved
// a whole listing at once. A rule with two arithmetics is a rule that can differ
// by route.
func OutstandingOf(drawn, receivable ledger.Amount) ledger.Amount {
	if receivable < 0 {
		receivable = 0
	}
	return drawn + receivable
}

// Outstanding is everything a facility owes. Returns ErrFacilityNotFound.
func (p *Portfolio) Outstanding(ctx context.Context, id FacilityID) (ledger.Amount, error) {
	var out ledger.Amount
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		f, err := tx.GetFacility(ctx, p.bookID, id)
		if err != nil {
			return err
		}
		drawn, err := p.drawnTx(ctx, tx, f)
		if err != nil {
			return err
		}
		out = OutstandingOf(drawn, f.Accrued.Minor())
		return nil
	})
	return out, err
}

// Close ends a facility. It refuses one that still owes anything — drawn
// principal OR accrued interest — which is the same rule deposit.CloseTx
// applies to an account's balance and its own interest receivable. A closed
// facility that still had a balance would be money owed to a bank by a contract
// the bank says is over, and a stranded receivable would be recognized income
// no one can ever collect.
//
// The interest test is the receivable's own book balance, not Accrued.Minor(),
// for the same reason deposit.CloseTx tests its receivable that way: a
// capitalization or repayment residue is bounded by half a minor unit and
// Minor() of it rounds to zero, except at an EXACT half, where Minor() rounds
// away from zero to ±1 even though the receivable itself is already cleared.
// Testing the record there would lock a fully-settled facility shut forever.
//
// Closed is terminal — no further drawing, no further repayment, and no further
// accrual.
//
// Returns ErrFacilityNotFound and ErrFacilityNotEmpty.
func (p *Portfolio) Close(ctx context.Context, id FacilityID) error {
	return p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return p.CloseTx(ctx, tx, id)
	})
}

// CloseTx is Close within a caller-supplied unit of work.
func (p *Portfolio) CloseTx(ctx context.Context, tx Tx, id FacilityID) error {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return err
	}
	if f.Status == Closed {
		return ErrFacilityClosed
	}
	drawn, err := p.drawnTx(ctx, tx, f)
	if err != nil {
		return err
	}
	if drawn != 0 {
		return ErrFacilityNotEmpty
	}
	receivable, err := p.receivableTx(ctx, tx, f)
	if err != nil {
		return err
	}
	if receivable != 0 {
		return ErrFacilityNotEmpty
	}

	f.Status = Closed
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return err
	}
	return p.appendAuditTx(ctx, tx, ledger.EventFacilityClosed, string(f.ID), f)
}
