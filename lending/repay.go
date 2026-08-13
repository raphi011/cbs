package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/internal/unit"
	"github.com/raphi011/cbs/ledger"
)

// Repay applies a payment to a facility, interest before principal.
func (p *Portfolio) Repay(ctx context.Context, id FacilityID, counterparty ledger.Position, amount ledger.Amount, date time.Time, description string) (ledger.Transaction, error) {
	return unit.Run(ctx, p.store.Update, func(ctx context.Context, tx Tx) (ledger.Transaction, error) {
		return p.RepayTx(ctx, tx, id, counterparty, amount, date, description)
	})
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
	// changed — so recompute now rather than leaving a borrower who has caught up
	// showing yesterday's bucket until the next end-of-day.
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
// applies to an account's balance and its own interest receivable.
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
