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
// The base is the DRAWN principal — each day's own VALUE-DATED closing balance
// of the principal account, not today's applied backwards. An open but undrawn
// commitment costs the borrower nothing, which is why a facility can sit at
// Pending accruing zero indefinitely.
//
// A gap of several days is therefore exact rather than approximate: every day in
// the span accrues on the balance that was actually in force on it, which is
// what a bank does.
//
// # Idempotency, and how a backdated posting is corrected
//
// LastAccrualDate never moves backwards, so re-running an end-of-day for a date
// already covered is a no-op rather than a second day's interest. It is also a
// no-op by arithmetic: the same date over the same history produces the same
// gross and therefore a zero delta.
//
// A posting which arrives backdated is trued up by the NEXT day's run rather
// than by rewinding this one. Each run recomputes the whole of the facility's
// LIFE from the value-dated drawn balance — the window opens at the facility's
// opening terms row — so the days the posting takes effect over are re-derived
// with it in place; the difference between the new total and the old one is what
// gets posted. Interest that turns out never to have been owed comes back as a
// correction — see correctFacilityAccrualTx, which is where a facility differs
// from a deposit account: what the receivable cannot absorb comes off principal,
// and what principal cannot absorb either becomes a payable.
//
// Because the window opens at origination rather than at the first advance or
// the last repricing, a backdated posting is trued up WHEREVER it lands,
// including on days before a repricing: each of those days is re-derived at the
// terms that were actually in force on it, not at today's. It is also what
// charges the span between a full repayment and a re-disbursement.
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
//
// Like the deposit layer's overdraft accrual, this is a recomputation rather
// than an increment: every run re-derives the whole terms window from the
// facility's value-dated drawn balance and posts the change in the rounded
// value. On an ordinary day that is the same delta the incremental version
// posted, arrived at differently. The difference shows when a repayment or an
// advance lands backdated: the days it takes effect over are re-derived with it
// in place, gross moves, and the delta trues up what was charged on the old
// figure. No accrual is reversed and no date is rewound — the original posting
// was a correct statement of what the ledger knew at the time.
//
// The terms are resolved PER DAY from the facility's timeline rather than read
// off the facility, which is what lets the window reach back past a repricing
// without re-deriving an earlier day at a rate that was never in force on it.
func (p *Portfolio) accrueFacilityTx(ctx context.Context, tx Tx, f Facility, date time.Time) error {
	if f.Status == Closed {
		return nil
	}

	// The whole timeline, in one read, resolved per day in Go below. The three
	// guards below are separate because lumping them together is how this would
	// acquire a bug:
	//
	//   - Status == Closed is unchanged, above.
	//   - TermsEffectiveFrom.IsZero() meant "nothing advanced, so no window",
	//     and there is always a window now: the opening row. The days before the
	//     first advance are re-derived at a drawn balance of zero and accrue
	//     nothing, which is the same answer the guard gave, arrived at by
	//     arithmetic instead of state.
	//   - Rate <= 0 cannot survive as an early return, because an early return
	//     skips the whole run and a zero rate is now a property of a DAY. A
	//     facility interest-free for its first year and priced thereafter is a
	//     case the previous model could not express at all. Two things replace
	//     it: the closure returns zero for a day whose resolved rate is zero,
	//     and the run is skipped entirely when NO row carries a non-zero rate —
	//     a scan over rows already in memory, which is what keeps a never-priced
	//     facility from reading a drawn series every night.
	rows, err := tx.ListFacilityTerms(ctx, p.bookID, f.ID)
	if err != nil {
		return err
	}
	if !anyPriced(rows) {
		return nil
	}

	// The window opens at the earliest row, which is the opening row, which is
	// origination. Every nightly run therefore re-derives every day the facility
	// has had: O(days) per facility per night, accepted deliberately at this
	// scale. The cost is arithmetic rather than I/O — the series is still one
	// query over the window — and checkpointing is the named successor.
	window := rows[0].EffectiveFrom

	// The advancement guard resolves its day count on `date`, because after this
	// change there is no single DayCount to ask: it is a terms field, and the
	// conventions genuinely disagree about whether a window advanced. Under
	// Thirty360 the 31st collapses onto the 30th, so Days(30th, 31st) is zero
	// while ACT365 says one — a run on the 31st is a no-op under one convention
	// and a real day under the other.
	//
	// `date` is exactly the right day, and for a sharper reason than "the day
	// being accrued": a span is named by its END (see the closure below), so the
	// last span this run adds is [date-1, date), which is the span NAMED date.
	// termsAt(rows, date) is therefore the very row that will price that span.
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
			// perDay has already cut the window to single days before any Period
			// runs, so this closure is a function of the DAY as well as the
			// balance. The day is `to`, not `from`: interest.AccrueSeries names a
			// span by its END date, because a movement value-dated V ends the
			// preceding run at V-1 and so first bites on [V-1, V). That is why
			// the pre-existing TestAccrue_CorrectsABackdatedRepayment calls that
			// span "day 3" for a repayment value-dated day 3.
			//
			// Resolving terms on `to` is what puts the rate on the same day axis
			// as the drawn balance it is charged against. On `from` they would be
			// a day apart — day D's rate applied to day D+1's balance — and a
			// repricing effective day 30 would not bite until day 31.
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
//
// It is not a reversal. The original accrual was a correct statement of what
// the ledger knew at the time, and reversing it would say otherwise; this is a
// new, linked event that posts what actually changed.
//
// The credit goes to the receivable as far as it reaches. Anything beyond it is
// interest the borrower has already settled, and it is credited to PRINCIPAL:
// they have paid the bank money they did not owe, so they now owe less of what
// they borrowed. That reduces the accrual basis, so the following days accrue
// on the smaller figure — the one feedback loop in this design, and the correct
// outcome.
//
// # Why the credit to principal is clamped, and where the rest goes
//
// Principal and the receivable are both Assets, and the ledger refuses a
// posting that would drive either below zero — inside an end-of-day batch that
// takes the whole book's run down with it. So the credit to principal is
// bounded by what is actually still drawn.
//
// Past that bound the borrower has overpaid the bank outright: they owe nothing
// and are owed money. That is a LIABILITY, and it goes to this facility's own
// interest-refunds-payable account, which is what the bank owing a customer
// money looks like in a ledger. It needs no knowledge of what a deposit account
// is — no more than interest income does — and being a Liability it can never
// be refused by the sufficiency check, so the remainder always has somewhere
// correct to go.
//
// Recording the obligation and discharging it are two operations, and only the
// first happens here: this runs inside an end-of-day batch, which has no
// borrower account to pay into and no business choosing one. RefundInterest is
// the second half — see refund.go, and RefundPayable for reading what is still
// owed.
//
// The alternative — posting nothing and keeping the difference — leaves
// interest income overstated by money the bank is not owed, with no account
// anywhere recording the obligation. That is a bank quietly keeping a
// customer's overpayment.
//
// # Why everything the receivable did not absorb comes off the record
//
// Only the part the receivable absorbed has left the record — that is the
// account Accrued mirrors, and the invariant interest/accrue.go states for
// every caller here is that the two are equal. Everything else has been settled
// somewhere the record does not track: against principal, or into the payable.
// So Accrued is credited back by exactly that, and the record goes on holding
// what the receivable holds.
//
// Leaving it on the record instead would be worse than untidy. A facility
// carrying a permanent negative Accrued is not a liability at all — it is a
// discount coupon, dischargeable only by swallowing the next interest the
// borrower genuinely owes, which hands them the money a second time. And a
// facility repaid in full is then closed, stranding it. api/dto_lending.go
// reports that record straight through.
//
// It takes f by pointer for that reason and writes the facility itself. Only
// this function knows the split.
func (p *Portfolio) correctFacilityAccrualTx(ctx context.Context, tx Tx, f *Facility, at FacilityPositions, income ledger.AccountID, amount ledger.Amount, date time.Time) error {
	receivable, err := p.receivableTx(ctx, tx, *f)
	if err != nil {
		return err
	}
	absorbed := min(amount, max(receivable, 0))

	// offRecord is everything the receivable could not take. It leaves the
	// record whatever happens to it; the only question is which account settles
	// it, and it is split between the two below. deposit's equivalent adds back
	// its `refund` because there the remainder goes one place and nothing
	// clamps it; here it goes two, and `refund` is only the clamped first part.
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
		// Under the facility's own id, which is what makes the obligation
		// findable afterwards by something that is not this function: the
		// payable line pools every borrower's, and this one's is the balance
		// under its subsidiary.
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
//
// The two halves are independent, and that is the whole reason this is a struct
// rather than a transaction: a cycle whose accrual has not yet reached a whole
// minor unit bills an instalment with no posting behind it, and a cycle on a
// line that is undrawn and owes nothing does neither. A caller that saw only
// the transaction could not tell those two apart, and would report "nothing
// happened" while the borrower's schedule gained a row.
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
// Nothing accrued and nothing drawn bills nothing, and a zero-value Charge is
// returned rather than an error: a cycle on an undrawn line is an ordinary
// outcome.
//
// # One cycle, once
//
// A cycle whose due date is already on the schedule is refused with
// ErrCycleAlreadyBilled. Unlike accrual — which LastAccrualDate makes a no-op
// on a re-run — billing appends a row and capitalizes the receivable, so a
// retried request would leave the borrower owing two minimum payments for one
// cycle. deposit.ChargeOverdraftInterest is a no-op on a second call for the
// same reason; the two credit layers behave the same way on the same operation.
//
// The guard is the DUE DATE, not chronology: billing cycles out of order is
// explicitly supported (a backdated cycle produces a later Seq with an earlier
// due date, which is exactly the case ArrearsFor scans the whole schedule for).
//
// Because AddMonths clamps to the last day of the target month, charging on
// the 29th, 30th and 31st of January all compute a due date of 28 February, so
// a second charge inside that month-end window is refused as already-billed
// even though no cycle actually collided. A normal monthly cadence never lands
// there; an operator retrying a mistimed charge should pick a date a day either
// side of the clamp rather than the same day again.
//
// Returns ErrFacilityNotFound, ErrFacilityClosed, ErrWrongFacilityKind,
// ErrCycleAlreadyBilled.
func (p *Portfolio) ChargeInterest(ctx context.Context, id FacilityID, date time.Time) (Charge, error) {
	var out Charge
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.ChargeInterestTx(ctx, tx, id, date)
		return err
	})
	return out, err
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
		// Compared by calendar day rather than by instant: a due date that has
		// been through a store carries whatever location that store hands back,
		// and two calls on the same business date must collide however their
		// timestamps were built.
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
		// Value-dated at date, which means the day ENDING on it is re-priced at
		// the capitalised balance: the recompute walks the value-dated drawn
		// series, and this debit is in it from date onwards, so the span
		// [date-1, date] is derived over a principal that already includes the
		// charge. That is interest on interest earned the same day. deposit
		// does the same at its own capitalisation, and it is sub-minor per
		// cycle, so it is recorded here rather than corrected: value-dating to
		// the NEXT day would be the fix.
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
		// Charging the rounded receivable leaves the record off by up to half a
		// minor unit, in either direction. Minor() of that residue rounds to
		// zero — except at an EXACT half, where it rounds away from zero to ±1
		// even though the receivable itself is already back to zero, which is
		// why CloseTx tests the receivable's book balance rather than the
		// record. Ordinarily the next day's accrual absorbs the residue as the
		// drawn balance moves again.
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
