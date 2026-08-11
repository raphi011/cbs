package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// This file is the other half of correctFacilityAccrualTx. That function records
// what the bank owes a borrower when a backdated posting shows it charged
// interest that was never earned and the borrower had already paid it in cash;
// this one lists those obligations and pays them back.
//
// They are deliberately separate operations. The correction runs inside an
// end-of-day batch, over every facility in the book, with no borrower account in
// hand and no business picking one. Paying money out is a decision someone makes
// about one facility, with a destination — so it is its own call, made when
// there is somebody to make it.

// RefundPayable is what the bank still owes one borrower in interest it took and
// never earned: the facility it arose on, and the balance the refunds-payable
// line holds under it.
//
// Amount is DERIVED — the balance of that position — rather than a figure kept
// on the facility, for the same reason Drawn is: a second copy of a number is a
// second thing that can be wrong. The facility stores neither the figure nor
// where it sits.
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
//
// Returns ErrFacilityNotFound.
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
// loaded. The refunds-payable line is a Liability, so its balance runs the
// opposite way to the facility's other two positions — which is the ledger's
// business, not this layer's, and is why the direction is not named here.
//
// It reads the position and not the account: the pool holds what the bank owes
// every other borrower too, and answering with that would tell one borrower they
// are owed the whole book's refunds.
func (p *Portfolio) refundPayableTx(ctx context.Context, tx Tx, f Facility) (ledger.Amount, error) {
	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return 0, err
	}
	return p.gl.BookBalanceTx(ctx, tx, at.Payable)
}

// ListRefundsPayable returns every outstanding interest refund in the book,
// oldest facility first. A facility owing nothing is omitted, so an empty result
// means the bank owes no borrower anything — which is the ordinary case.
//
// CLOSED facilities are included, and that is the point of listing rather than
// deriving this from the active portfolio. Close refuses a facility that still
// owes the BANK money; it says nothing about a facility the bank owes, because
// the lending contract ending does not cancel a debt running the other way. A
// listing that skipped closed facilities would make exactly those obligations
// invisible — the ones with no further accrual, statement or repayment to
// surface them.
//
// It reads one balance per facility, which is one per subsidiary under a single
// chart-of-accounts line.
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
//
//	Dr  Interest Refunds Payable (Liability), this borrower   4_932
//	  Cr counterparty                                          4_932
//
// It is the mirror of Repay, and every difference between the two follows from
// the money running the other way.
//
// counterparty is any position in the facility's asset — the borrower's
// current account, or the vault for a cash refund. This layer does not know what
// a deposit account is, so a refund that must also respect one's status is
// orchestrated a layer up, the same way Disburse's payout is.
//
// # It does not touch principal, the receivable, or the schedule
//
// A repayment allocates across two accounts and then across instalments, because
// it settles a debt the schedule is a plan for. This settles a debt that has no
// schedule and no components: the correction already decided what the borrower
// owes — it credited principal as far as principal could absorb, and only the
// overflow reached the payable. Allocating any of this refund back onto the loan
// would undo that split and hand the borrower money the correction had already
// used to reduce what they owe.
//
// Arrears are untouched for the same reason, and unlike Repay there is nothing to
// recompute: arrears are a pure function of the schedule, which this does not
// change.
//
// # A closed facility may still be refunded
//
// Repay refuses ErrFacilityClosed; this deliberately does not. Closed means the
// borrower owes nothing and no more will be lent — a statement about their
// obligations, not the bank's. A bank that discovers it overcharged interest on a
// loan settled last year still owes that money, and refusing to pay it because
// the contract is over would strand the obligation in a Liability account with
// nothing that could ever discharge it.
//
// # Why the amount is bounded
//
// A Liability is never caught by the ledger's sufficiency check — that guards
// Asset and Expense positions only — so an over-refund would post cleanly and
// leave this borrower's share of the payable NEGATIVE: a balance saying the
// borrower owes the bank a refund, which is not a thing. The pool would stay
// comfortably positive on other borrowers' money throughout, which is exactly
// the reading a control account must not be given. Partial refunds are allowed, because
// paying an obligation down in instalments is ordinary; paying out more than was
// ever owed is not.
//
// Returns ErrFacilityNotFound, ErrNoRefundOutstanding when the bank owes this
// facility nothing, and ErrInvalidAmount for an amount that is not positive or
// that exceeds what is owed.
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
	// owes has the figure wrong, and silently paying less would report success
	// for an amount that was never posted. Nothing here runs inside an
	// end-of-day batch, so there is no batch to take down by refusing — which is
	// exactly why the correction clamps and this does not.
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

	// The facility itself is not written: nothing on the record changed. Accrued
	// mirrors the RECEIVABLE, and the correction already took this money off it
	// when it pushed the overflow into the payable — adding it back here would
	// restate a receivable that was settled in cash.
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
