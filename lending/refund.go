package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// This file is the other half of correctFacilityAccrualTx.

// RefundPayable is what the bank still owes one borrower in interest it took
// and never earned: the facility it arose on, and the balance the
// refunds-payable line holds under it.
type RefundPayable struct {
	FacilityID FacilityID
	// Name and Asset are the facility's, copied so a caller listing every
	// outstanding refund can render it without a lookup per row.
	Name  string
	Asset ledger.AssetCode
	// Account is the Liability control account the obligation sits in. The
	// subsidiary under it is FacilityID above, so a client reading a statement for
	// this one refund has both halves.
	Account ledger.AccountID
	// Amount is what is still owed, in the asset's minor units. Always positive:
	// a facility owing nothing is not listed at all.
	Amount ledger.Amount
	// FacilityStatus is the facility's, and may be Closed. A refund outlives the
	// lending contract — see RefundInterestTx.
	FacilityStatus FacilityStatus
}

// RefundPayableFor is what the bank owes one borrower back. It is 0 for a
// facility no correction has ever overshot on, which is the overwhelming
// majority: nothing has been posted under it.
func (p *Portfolio) RefundPayableFor(ctx context.Context, id FacilityID) (ledger.Amount, error) {
	var out ledger.Amount
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		f, err := tx.GetFacility(ctx, p.bookID, id)
		if err != nil {
			return err
		}
		out, err = p.refundPayableTx(ctx, tx, f)
		return err
	})
	return out, err
}

// refundPayableTx is RefundPayableFor against a facility the caller has already
// loaded.
func (p *Portfolio) refundPayableTx(ctx context.Context, tx Tx, f Facility) (ledger.Amount, error) {
	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return 0, err
	}
	return p.gl.BookBalanceTx(ctx, tx, at.Payable)
}

// ListRefundsPayable returns every outstanding interest refund in the book,
// oldest facility first. A facility owing nothing is omitted, so an empty
// result means the bank owes no borrower anything — which is the ordinary case.
func (p *Portfolio) ListRefundsPayable(ctx context.Context) ([]RefundPayable, error) {
	var out []RefundPayable
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.ListRefundsPayableTx(ctx, tx)
		return err
	})
	return out, err
}

// ListRefundsPayableTx is ListRefundsPayable within a caller-supplied unit of
// work.
func (p *Portfolio) ListRefundsPayableTx(ctx context.Context, tx Tx) ([]RefundPayable, error) {
	facilities, err := tx.ListFacilities(ctx, p.bookID)
	if err != nil {
		return nil, err
	}
	out := make([]RefundPayable, 0)
	for _, f := range facilities {
		at, err := p.accountsTx(ctx, tx, f)
		if err != nil {
			return nil, err
		}
		owed, err := p.gl.BookBalanceTx(ctx, tx, at.Payable)
		if err != nil {
			return nil, err
		}
		if owed <= 0 {
			continue
		}
		out = append(out, RefundPayable{
			FacilityID:     f.ID,
			Name:           f.Name,
			Asset:          f.Asset,
			Account:        at.Payable.Account,
			Amount:         owed,
			FacilityStatus: f.Status,
		})
	}
	return out, nil
}

// RefundInterest pays a borrower back interest the bank charged and never
// earned, discharging what a backdated correction left in the facility's
// refunds-payable account.
func (p *Portfolio) RefundInterest(ctx context.Context, id FacilityID, counterparty ledger.Position, amount ledger.Amount, date time.Time, description string) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.RefundInterestTx(ctx, tx, id, counterparty, amount, date, description)
		return err
	})
	return out, err
}

// RefundInterestTx is RefundInterest within a caller-supplied unit of work.
func (p *Portfolio) RefundInterestTx(ctx context.Context, tx Tx, id FacilityID, counterparty ledger.Position, amount ledger.Amount, date time.Time, description string) (ledger.Transaction, error) {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if amount <= 0 {
		return ledger.Transaction{}, ErrInvalidAmount
	}
	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return ledger.Transaction{}, err
	}
	owed, err := p.gl.BookBalanceTx(ctx, tx, at.Payable)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if owed <= 0 {
		return ledger.Transaction{}, ErrNoRefundOutstanding
	}
	// Bounded rather than clamped: a caller asking to pay out more than the bank
	// owes has the figure wrong, and silently paying less would report success for
	// an amount that was never posted.
	if amount > owed {
		return ledger.Transaction{}, ErrInvalidAmount
	}

	if description == "" {
		description = "Interest refund: " + f.Name
	}
	glTx, err := p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		BookingDate: date,
		ValueDate:   date,
		Entries: []ledger.Entry{
			{AccountID: at.Payable.Account, Subsidiary: at.Payable.Subsidiary, Amount: amount, Direction: ledger.Debit},
			{AccountID: counterparty.Account, Subsidiary: counterparty.Subsidiary, Amount: amount, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return ledger.Transaction{}, err
	}

	// The facility itself is not written: nothing on the record changed.
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityInterestRefunded, string(f.ID), map[string]any{
		"facility_id":    string(f.ID),
		"amount":         amount,
		"remaining":      owed - amount,
		"account":        at.Payable.String(),
		"counterparty":   counterparty.String(),
		"transaction_id": string(glTx.ID),
	}); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, nil
}
