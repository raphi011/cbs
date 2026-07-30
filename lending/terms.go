package lending

import (
	"sort"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// FacilityTerms is what a facility's credit cost from one day onwards: one row
// per repricing, never overwritten.
//
// It is the mirror of deposit.OverdraftTerms and exists for the same reasons —
// see there for the full argument. What differs is what is NOT here. A
// facility's Commitment is not effective-dated: drawing is refused against the
// limit in force at the moment of the draw, and no past day's arithmetic
// depends on what it used to be. Neither are Method, TermMonths or MinPayment,
// which feed BuildSchedule rather than the accrual; see the package doc and
// ErrScheduleWouldDiverge for why the divergence that would create is made
// unreachable rather than merely documented.
type FacilityTerms struct {
	FacilityID    FacilityID
	EffectiveFrom time.Time // day-truncated; the first day these terms apply
	Rate          interest.Rate
	DayCount      interest.DayCount
	CreatedAt     time.Time // when the row was entered, not when it takes effect
}

// TermsDayKey is the day a terms row is identified by within its facility. It
// is deposit.TermsDayKey's mirror, and deliberately not shared with it: lending
// does not import deposit, and a shared constant would be the first thread of
// exactly that dependency.
func TermsDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// Validate reports whether these terms are a product. A zero rate is a real
// product — an interest-free facility — so only a negative one is refused.
func (t FacilityTerms) Validate() error {
	if t.Rate < 0 {
		return ErrInvalidRate
	}
	return nil
}

// termsAt is the row in force on a day: the last one whose EffectiveFrom is not
// after it. rows must be ascending, which is what ListFacilityTermsForFacility
// returns. See deposit.termsAt for why this is a binary search rather than a
// cursor advanced alongside the accrual walk.
func termsAt(rows []FacilityTerms, day time.Time) (FacilityTerms, bool) {
	d := ledger.DayStart(day)
	i := sort.Search(len(rows), func(i int) bool {
		return rows[i].EffectiveFrom.After(d)
	})
	if i == 0 {
		return FacilityTerms{}, false
	}
	return rows[i-1], true
}

// anyPriced reports whether any row in the timeline carries a non-zero rate. It
// is what keeps a never-priced facility from reading a drawn series every
// night; see deposit.anyPriced for why the old account-level rate guard could
// not survive as one.
func anyPriced(rows []FacilityTerms) bool {
	for _, r := range rows {
		if r.Rate > 0 {
			return true
		}
	}
	return false
}
