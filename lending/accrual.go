package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/internal/unit"
	"github.com/raphi011/cbs/ledger"
)

// Accrue accrues interest on a facility's drawn principal up to a business
// date, and posts the day's income to the general ledger.
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
	if f.Status == Closed {
		return nil
	}

	// The whole timeline, in one read, resolved per day in Go below. The guards
	// stay separate because lumping them together is how this acquires a bug.
	rows, err := tx.ListFacilityTerms(ctx, p.bookID, f.ID)
	if err != nil {
		return err
	}
	if !anyPriced(rows) {
		return nil
	}

	// The window opens at the earliest row, which is the opening row, which is
	// origination.
	window := rows[0].EffectiveFrom

	// The advancement guard resolves its day count on `date`, because after this
	// change there is no single DayCount to ask: it is a terms field, and the
	// conventions genuinely disagree about whether a window advanced.
	current, ok := termsAt(rows, date)
	if !ok {
		return nil
	}
	if current.DayCount.Days(f.LastAccrualDate, date) <= 0 {
		return nil
	}

	series, err := p.drawnSeriesTx(ctx, tx, f, window, date)
	if err != nil {
		return err
	}
	next, delta := interest.Recompute(series, window, date,
		interest.State{Accrued: f.Accrued, Gross: f.AccruedGross},
		func(drawn ledger.Amount, from, to time.Time) interest.Accrued {
			// perDay has already cut the window to single days before any Period runs,
			// so this closure is a function of the DAY as well as the balance.
			day, ok := termsAt(rows, to)
			if !ok {
				return 0
			}
			return interest.Accrue(drawn, day.Rate, day.DayCount, from, to)
		})

	f.Accrued = next.Accrued
	f.AccruedGross = next.Gross
	f.LastAccrualDate = date

	if delta == 0 {
		// The rounding did not tick. There is nothing to post, and the ledger
		// refuses a zero-amount entry anyway.
		if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
			return err
		}
		return p.appendAuditTx(ctx, tx, ledger.EventFacilityAccrued, string(f.ID), f)
	}

	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return err
	}
	income, err := p.incomeTx(ctx, tx, f.Asset)
	if err != nil {
		return err
	}
	// A correction can settle part of the record against principal, which moves
	// Accrued again, so it owns the write — the same shape ChargeInterestTx has
	// for the same reason. Only it knows what was actually given back.
	if delta < 0 {
		return p.correctFacilityAccrualTx(ctx, tx, &f, at, income, -delta, date)
	}

	if _, err := p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Interest accrued: " + f.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries: []ledger.Entry{
			{AccountID: at.Receivable.Account, Subsidiary: at.Receivable.Subsidiary, Amount: delta, Direction: ledger.Debit},
			{AccountID: income, Amount: delta, Direction: ledger.Credit},
		},
	}); err != nil {
		return err
	}
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return err
	}
	return p.appendAuditTx(ctx, tx, ledger.EventFacilityAccrued, string(f.ID), f)
}

// correctFacilityAccrualTx gives back interest a backdated posting has shown
// was never owed. amount is positive.
func (p *Portfolio) correctFacilityAccrualTx(ctx context.Context, tx Tx, f *Facility, at FacilityPositions, income ledger.AccountID, amount ledger.Amount, date time.Time) error {
	receivable, err := p.receivableTx(ctx, tx, *f)
	if err != nil {
		return err
	}
	absorbed := min(amount, max(receivable, 0))

	// offRecord is everything the receivable could not take.
	offRecord := amount - absorbed

	refund := offRecord
	if refund > 0 {
		drawn, err := p.drawnTx(ctx, tx, *f)
		if err != nil {
			return err
		}
		refund = min(refund, max(drawn, 0))
	}
	payable := offRecord - refund

	// Credits total amount however it splits, so this always balances and
	// always posts: amount is positive by contract.
	entries := []ledger.Entry{{AccountID: income, Amount: amount, Direction: ledger.Debit}}
	if absorbed > 0 {
		entries = append(entries, ledger.Entry{AccountID: at.Receivable.Account, Subsidiary: at.Receivable.Subsidiary, Amount: absorbed, Direction: ledger.Credit})
	}
	if refund > 0 {
		entries = append(entries, ledger.Entry{AccountID: at.Principal.Account, Subsidiary: at.Principal.Subsidiary, Amount: refund, Direction: ledger.Credit})
	}
	if payable > 0 {
		// Under the facility's own id, which is what makes the obligation findable
		// afterwards by something that is not this function: the payable line pools
		// every borrower's, and this one's is the balance under its subsidiary.
		entries = append(entries, ledger.Entry{AccountID: at.Payable.Account, Subsidiary: at.Payable.Subsidiary, Amount: payable, Direction: ledger.Credit})
	}

	glTx, err := p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Interest corrected: " + f.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries:     entries,
	})
	if err != nil {
		return err
	}

	f.Accrued += interest.FromMinor(offRecord)
	if err := tx.PutFacility(ctx, p.bookID, *f); err != nil {
		return err
	}
	// The full split, so an operator reading one event can reconstruct the
	// posting without deriving a figure: absorbed + refund + payable = amount.
	return p.appendAuditTx(ctx, tx, ledger.EventFacilityAccrualCorrected, string(f.ID), map[string]any{
		"facility_id":    string(f.ID),
		"amount":         amount,
		"absorbed":       absorbed,
		"refund":         refund,
		"payable":        payable,
		"transaction_id": string(glTx.ID),
		"residue":        int64(f.Accrued),
	})
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

// Charge is the outcome of billing one of a revolving line's cycles.
type Charge struct {
	// Transaction is the capitalization posting. It is the zero value when
	// nothing was posted — the cycle had not accrued a whole minor unit of
	// interest to charge — which is an outcome, not an error.
	Transaction ledger.Transaction
	// Installment is the cycle that was billed. It is the zero value — Seq 0,
	// which no real instalment has — when no cycle was billed at all.
	Installment Installment
}

// Billed reports whether a cycle was appended to the schedule.
func (c Charge) Billed() bool { return c.Installment.Seq != 0 }

// Posted reports whether interest was capitalized into principal.
func (c Charge) Posted() bool { return c.Transaction.ID != "" }

// ChargeInterest closes a revolving line's billing cycle: it capitalizes the
// accrued interest into drawn principal and bills the cycle's minimum payment.
func (p *Portfolio) ChargeInterest(ctx context.Context, id FacilityID, date time.Time) (Charge, error) {
	return unit.Run(ctx, p.store.Update, func(ctx context.Context, tx Tx) (Charge, error) {
		return p.ChargeInterestTx(ctx, tx, id, date)
	})
}

// ChargeInterestTx is ChargeInterest within a caller-supplied unit of work.
func (p *Portfolio) ChargeInterestTx(ctx context.Context, tx Tx, id FacilityID, date time.Time) (Charge, error) {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return Charge{}, err
	}
	if f.Status == Closed {
		return Charge{}, ErrFacilityClosed
	}
	if f.Kind != RevolvingLine {
		return Charge{}, ErrWrongFacilityKind
	}

	drawn, err := p.drawnTx(ctx, tx, f)
	if err != nil {
		return Charge{}, err
	}
	charge := f.Accrued.Minor()
	if charge <= 0 && drawn <= 0 {
		return Charge{}, nil
	}

	// Read the schedule BEFORE anything is posted: the duplicate check has to
	// refuse the retry rather than roll it back, and a caller that had already
	// seen a second capitalization posted would not be helped by an error.
	existing, err := tx.ListInstallments(ctx, p.bookID, f.ID)
	if err != nil {
		return Charge{}, err
	}
	due := AddMonths(date, 1)
	for _, inst := range existing {
		// Compared by calendar day rather than by instant: a due date that has been
		// through a store carries whatever location that store hands back, and two
		// calls on the same business date must collide however their timestamps were
		// built.
		if calendarDays(inst.DueDate, due) == 0 {
			return Charge{}, ErrCycleAlreadyBilled
		}
	}

	at, err := p.accountsTx(ctx, tx, f)
	if err != nil {
		return Charge{}, err
	}

	var glTx ledger.Transaction
	if charge > 0 {
		// Value-dated at date, which means the day ENDING on it is re-priced at the
		// capitalised balance: the recompute walks the value-dated drawn series, and
		// this debit is in it from date onwards, so the span [date-1, date] is
		// derived over a principal that already includes the charge.
		glTx, err = p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			Description: "Interest charged: " + f.Name,
			BookingDate: date,
			ValueDate:   date,
			Entries: []ledger.Entry{
				{AccountID: at.Principal.Account, Subsidiary: at.Principal.Subsidiary, Amount: charge, Direction: ledger.Debit},
				{AccountID: at.Receivable.Account, Subsidiary: at.Receivable.Subsidiary, Amount: charge, Direction: ledger.Credit},
			},
		})
		if err != nil {
			return Charge{}, err
		}
		// Charging the rounded receivable leaves the record off by up to half a minor
		// unit, in either direction.
		f.Accrued -= interest.FromMinor(charge)
		drawn += charge
	}

	cycle := Installment{
		FacilityID: f.ID,
		Seq:        len(existing) + 1,
		DueDate:    due,
		Principal:  f.MinPayment.Of(drawn),
		Interest:   charge,
	}
	if err := tx.PutInstallment(ctx, p.bookID, cycle); err != nil {
		return Charge{}, err
	}
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return Charge{}, err
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityCharged, string(f.ID), map[string]any{
		"facility_id":    string(f.ID),
		"amount":         charge,
		"transaction_id": string(glTx.ID),
		"installment":    cycle.Seq,
		"minimum":        cycle.Total(),
		"residue":        int64(f.Accrued),
	}); err != nil {
		return Charge{}, err
	}
	return Charge{Transaction: glTx, Installment: cycle}, nil
}
