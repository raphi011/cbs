package lending

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// ArrearsFor computes a facility's delinquency from its schedule.
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
		// Re-read: accrual has just written the facility, and the arrears pass must
		// not overwrite that with a stale copy.
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
