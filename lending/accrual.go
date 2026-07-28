package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// Accrue accrues interest on a facility's drawn principal up to a business
// date, and posts the day's income to the general ledger.
//
// The mechanism is the same as deposit's overdraft accrual, for the same
// reasons. The facility record holds exact interest in micro-minor-units; the
// ledger holds Accrued.Minor() of it in the facility's receivable account. So
// the posting is the CHANGE in the rounded value, not the period's exact
// interest:
//
//	Dr  Accrued interest receivable (Asset)   165
//	  Cr Interest income (Revenue)              165
//
// A day on which the rounding does not tick posts nothing, which is why this
// returns no transaction.
//
// The base is the DRAWN principal, derived from the principal account's
// balance. An open but undrawn commitment costs the borrower nothing, which is
// why a facility can sit at Pending accruing zero indefinitely.
//
// LastAccrualDate never moves backwards, so re-running an end-of-day for a date
// already covered is a no-op rather than a second day's interest. A gap of
// several days accrues at the CURRENT drawn balance for the whole span, which
// is exact only if nothing was drawn or repaid in between; a bank accrues on
// each day's closing balance, and running this daily makes the two identical.
//
// Returns ErrFacilityNotFound.
func (p *Portfolio) Accrue(ctx context.Context, id FacilityID, date time.Time) error {
	return p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return p.AccrueTx(ctx, tx, id, date)
	})
}

// AccrueTx is Accrue within a caller-supplied unit of work.
func (p *Portfolio) AccrueTx(ctx context.Context, tx Tx, id FacilityID, date time.Time) error {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return err
	}
	return p.accrueFacilityTx(ctx, tx, f, date)
}

// accrueFacilityTx is AccrueTx against a facility the caller has already
// loaded. RunEndOfDay lists every facility and would otherwise read each twice.
func (p *Portfolio) accrueFacilityTx(ctx context.Context, tx Tx, f Facility, date time.Time) error {
	if f.Status == Closed || f.Rate <= 0 {
		return nil
	}
	if f.LastAccrualDate.IsZero() {
		// Nothing has been advanced yet, so there is no period to accrue over.
		return nil
	}
	if f.DayCount.Days(f.LastAccrualDate, date) <= 0 {
		return nil
	}

	drawn, err := p.drawnTx(ctx, tx, f)
	if err != nil {
		return err
	}

	before := f.Accrued.Minor()
	f.Accrued += interest.Accrue(drawn, f.Rate, f.DayCount, f.LastAccrualDate, date)
	f.LastAccrualDate = date
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return err
	}

	delta := f.Accrued.Minor() - before
	if delta == 0 {
		return p.appendAuditTx(ctx, tx, ledger.EventFacilityAccrued, string(f.ID), f)
	}

	income, err := p.interestIncomeTx(ctx, tx, f)
	if err != nil {
		return err
	}
	if _, err := p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Interest accrued: " + f.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries: []ledger.Entry{
			{AccountID: f.InterestGL, Amount: delta, Direction: ledger.Debit},
			{AccountID: income, Amount: delta, Direction: ledger.Credit},
		},
	}); err != nil {
		return err
	}
	return p.appendAuditTx(ctx, tx, ledger.EventFacilityAccrued, string(f.ID), f)
}

// interestIncomeTx resolves the bank's interest-income account for a
// facility's asset, creating it and its subledger on first use.
func (p *Portfolio) interestIncomeTx(ctx context.Context, tx Tx, f Facility) (ledger.AccountID, error) {
	principal, err := tx.GetAccount(ctx, p.bookID, f.PrincipalGL)
	if err != nil {
		return "", err
	}
	loansSub, err := tx.GetSubledger(ctx, p.bookID, principal.SubledgerID)
	if err != nil {
		return "", err
	}
	sub, err := p.gl.EnsureSubledgerTx(ctx, tx, loansSub.LedgerID, incomeSubledgerName)
	if err != nil {
		return "", err
	}
	income, err := p.gl.EnsureAccountTx(ctx, tx, sub.ID, interestIncomeName(f.Asset), ledger.Revenue, f.Asset)
	if err != nil {
		return "", err
	}
	return income.ID, nil
}

// AccruedInterest is a facility's receivable in whole minor units — the balance
// the ledger holds, and the amount a repayment settles before touching
// principal. Returns ErrFacilityNotFound.
func (p *Portfolio) AccruedInterest(ctx context.Context, id FacilityID) (ledger.Amount, error) {
	var out ledger.Amount
	err := p.store.View(ctx, func(ctx context.Context, tx Tx) error {
		f, err := tx.GetFacility(ctx, p.bookID, id)
		if err != nil {
			return err
		}
		out = f.Accrued.Minor()
		return nil
	})
	return out, err
}

// ChargeInterest closes a revolving line's billing cycle: it capitalizes the
// accrued interest into drawn principal and bills the cycle's minimum payment.
//
//	Dr  Loan principal (Asset)                 1_479
//	  Cr Accrued interest receivable (Asset)     1_479
//
// Capitalizing is what makes a revolving balance compound — next cycle accrues
// on a principal that now includes this cycle's interest — and it is why the
// receivable comes back to zero rather than growing forever.
//
// The cycle's instalment is then appended: interest charged, plus MinPayment of
// the new drawn balance. That is the minimum payment a card statement shows,
// and it is what a revolving facility falls into arrears by missing. A line has
// no schedule generated up front because there is nothing to generate one from;
// this is where its instalments come from.
//
// # Why a term loan cannot be charged
//
// A term loan settles interest through its scheduled instalments. Capitalizing
// it into principal would silently re-amortize a signed contract, so this
// returns ErrWrongFacilityKind. Capitalizing unpaid interest on a delinquent
// term loan is a real product feature, and it is part of the restructuring work
// deferred in docs/expansion-roadmap.md.
//
// Nothing accrued and nothing drawn bills nothing, and a zero-value Transaction
// is returned rather than an error: a cycle on an undrawn line is an ordinary
// outcome.
//
// Returns ErrFacilityNotFound, ErrFacilityClosed, ErrWrongFacilityKind.
func (p *Portfolio) ChargeInterest(ctx context.Context, id FacilityID, date time.Time) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.ChargeInterestTx(ctx, tx, id, date)
		return err
	})
	return out, err
}

// ChargeInterestTx is ChargeInterest within a caller-supplied unit of work.
func (p *Portfolio) ChargeInterestTx(ctx context.Context, tx Tx, id FacilityID, date time.Time) (ledger.Transaction, error) {
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

	drawn, err := p.drawnTx(ctx, tx, f)
	if err != nil {
		return ledger.Transaction{}, err
	}
	charge := f.Accrued.Minor()
	if charge <= 0 && drawn <= 0 {
		return ledger.Transaction{}, nil
	}

	var glTx ledger.Transaction
	if charge > 0 {
		glTx, err = p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			Description: "Interest charged: " + f.Name,
			BookingDate: date,
			ValueDate:   date,
			Entries: []ledger.Entry{
				{AccountID: f.PrincipalGL, Amount: charge, Direction: ledger.Debit},
				{AccountID: f.InterestGL, Amount: charge, Direction: ledger.Credit},
			},
		})
		if err != nil {
			return ledger.Transaction{}, err
		}
		// Charging the rounded receivable leaves the record off by up to half a
		// minor unit, in either direction. Minor() of the residue is still 0, so
		// record and ledger stay in step and the next day's accrual absorbs it.
		f.Accrued -= interest.FromMinor(charge)
		drawn += charge
	}

	existing, err := tx.ListInstallments(ctx, p.bookID, f.ID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	cycle := Installment{
		FacilityID: f.ID,
		Seq:        len(existing) + 1,
		DueDate:    AddMonths(date, 1),
		Principal:  f.MinPayment.Of(drawn),
		Interest:   charge,
	}
	if err := tx.PutInstallment(ctx, p.bookID, cycle); err != nil {
		return ledger.Transaction{}, err
	}
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return ledger.Transaction{}, err
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityCharged, string(f.ID), map[string]any{
		"facility_id":    string(f.ID),
		"amount":         charge,
		"transaction_id": string(glTx.ID),
		"installment":    cycle.Seq,
		"minimum":        cycle.Total(),
		"residue":        int64(f.Accrued),
	}); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, nil
}
