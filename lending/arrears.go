package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// ArrearsFor computes a facility's delinquency from its schedule.
//
// It is a pure function of the schedule and a date, which is what makes arrears
// recomputable rather than a sequence of events to be replayed: a payment
// arriving late, a schedule corrected, an end-of-day re-run, all just produce
// the right answer next time.
//
// The clock starts at the OLDEST instalment that still has an unpaid amount and
// whose due date has passed. Paying that one moves the clock forward to the
// next unpaid instalment; it does not reset it. That is what makes a borrower
// who is permanently one instalment behind stay visibly one instalment behind.
//
// The scan visits every instalment rather than stopping at the first one not
// yet due, because Seq order does not always track DueDate order. A term
// loan's does — BuildSchedule assigns both together — but a revolving line's
// cycles are appended by ChargeInterestTx with a due date taken from the
// caller's billing date, not from Seq: a backdated charge produces a
// later-Seq cycle whose due date is EARLIER than the cycle before it.
// Stopping at the first not-yet-due row in Seq order would then walk past a
// genuinely overdue cycle sitting behind it and silently report the facility
// as current. Schedules are tens of rows, so scanning all of them costs
// nothing.
//
// Days past due are ALWAYS actual calendar days, whatever day-count convention
// the facility accrues interest under. A 30/360 loan is not 33/360 days late —
// delinquency is a fact about the calendar, and every regulatory and management
// report counts it that way.
//
// An instalment due today is not late. NonPerforming is set at 90+ days, and it
// MARKS ONLY: it changes no accounting. Non-accrual and provisioning are
// recorded as future work in docs/expansion-roadmap.md.
func ArrearsFor(schedule []Installment, asOf time.Time) Arrears {
	var oldest *Installment
	for i := range schedule {
		inst := &schedule[i]
		if inst.Outstanding() <= 0 {
			continue
		}
		if calendarDays(inst.DueDate, asOf) <= 0 {
			// Not yet due. Unlike Seq, this is not necessarily the newest
			// instalment, so a later one in the slice may still be overdue.
			continue
		}
		if oldest == nil || inst.DueDate.Before(oldest.DueDate) {
			oldest = inst
		}
	}
	if oldest == nil {
		return Arrears{Bucket: Current}
	}
	days := calendarDays(oldest.DueDate, asOf)
	return Arrears{
		DaysPastDue:     days,
		Bucket:          BucketFor(days),
		NonPerforming:   days >= 90,
		OldestUnpaidDue: oldest.DueDate,
	}
}

// sameArrears reports whether two Arrears describe the same state.
//
// It is not `==`. Arrears holds a time.Time, and == on a time.Time compares the
// monotonic reading and the location as well as the instant — so two values
// naming the same date can compare unequal depending on where they came from.
// Here that would mean an unchanged facility being rewritten and re-audited
// every single end-of-day, which is a log nobody can read and a write nobody
// needs.
func sameArrears(a, b Arrears) bool {
	return a.DaysPastDue == b.DaysPastDue &&
		a.Bucket == b.Bucket &&
		a.NonPerforming == b.NonPerforming &&
		a.OldestUnpaidDue.Equal(b.OldestUnpaidDue)
}

// calendarDays counts whole days between two dates, discarding the time of day
// on both ends so that an end-of-day run at 23:00 and one at 09:00 agree.
func calendarDays(from, to time.Time) int {
	f := from.UTC()
	t := to.UTC()
	fd := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.UTC)
	td := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return int(td.Sub(fd).Hours() / 24)
}

// RecomputeArrears reads a facility's schedule and updates its arrears.
//
// Returns ErrFacilityNotFound.
func (p *Portfolio) RecomputeArrears(ctx context.Context, id FacilityID, asOf time.Time) (Arrears, error) {
	var out Arrears
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.RecomputeArrearsTx(ctx, tx, id, asOf)
		return err
	})
	return out, err
}

// RecomputeArrearsTx is RecomputeArrears within a caller-supplied unit of work.
func (p *Portfolio) RecomputeArrearsTx(ctx context.Context, tx Tx, id FacilityID, asOf time.Time) (Arrears, error) {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return Arrears{}, err
	}
	return p.recomputeArrearsFacilityTx(ctx, tx, f, asOf)
}

// recomputeArrearsFacilityTx is RecomputeArrearsTx against a facility the
// caller has already loaded. It writes only when something changed, so an
// unchanged facility does not produce a daily audit event saying so.
func (p *Portfolio) recomputeArrearsFacilityTx(ctx context.Context, tx Tx, f Facility, asOf time.Time) (Arrears, error) {
	schedule, err := tx.ListInstallments(ctx, p.bookID, f.ID)
	if err != nil {
		return Arrears{}, err
	}
	next := ArrearsFor(schedule, asOf)
	if sameArrears(next, f.Arrears) {
		return next, nil
	}

	previous := f.Arrears
	f.Arrears = next
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return Arrears{}, err
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityArrears, string(f.ID), map[string]any{
		"facility_id":    string(f.ID),
		"from_bucket":    previous.Bucket.String(),
		"to_bucket":      next.Bucket.String(),
		"days_past_due":  next.DaysPastDue,
		"non_performing": next.NonPerforming,
	}); err != nil {
		return Arrears{}, err
	}
	return next, nil
}

// RunEndOfDay accrues interest on every facility in the book for one business
// date, then recomputes each one's arrears.
//
// It does not charge interest and does not capitalize. Billing a revolving
// line's cycle is a monthly event on its own calendar, and which day that falls
// on is a product decision this layer has no opinion about; a caller runs
// ChargeInterest when its calendar says to.
//
// Closed facilities and those with no rate are skipped rather than errored.
//
// This is one of two end-of-day entry points — deposit.Register has the other,
// for overdraft accrual — because the two layers own different products and
// neither imports the other. payment.Participant.RunEndOfDay calls both, and is
// what the API exposes, so a caller cannot run one without the other by
// accident.
func (p *Portfolio) RunEndOfDay(ctx context.Context, date time.Time) error {
	return p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return p.RunEndOfDayTx(ctx, tx, date)
	})
}

// RunEndOfDayTx is RunEndOfDay within a caller-supplied unit of work, so a
// participant can run its deposit and lending batches in one transaction.
func (p *Portfolio) RunEndOfDayTx(ctx context.Context, tx Tx, date time.Time) error {
	facilities, err := tx.ListFacilities(ctx, p.bookID)
	if err != nil {
		return err
	}
	for _, f := range facilities {
		if f.Status == Closed {
			continue
		}
		if err := p.accrueFacilityTx(ctx, tx, f, date); err != nil {
			return err
		}
		// Re-read: accrual has just written the facility, and the arrears pass
		// must not overwrite that with a stale copy. This is the kind of thing
		// that only shows up as interest quietly vanishing on days when a
		// facility also changed bucket.
		fresh, err := tx.GetFacility(ctx, p.bookID, f.ID)
		if err != nil {
			return err
		}
		if _, err := p.recomputeArrearsFacilityTx(ctx, tx, fresh, date); err != nil {
			return err
		}
	}
	return nil
}
