package lending

import (
	"sort"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// FacilityTerms is what a facility's credit cost from one day onwards: one row
// per repricing, appended rather than editing what an earlier day already said.
type FacilityTerms struct {
	FacilityID    FacilityID
	EffectiveFrom time.Time // day-truncated; the first day these terms apply
	Rate          interest.Rate
	DayCount      interest.DayCount
	CreatedAt     time.Time // when the row was entered, not when it takes effect
}

// TermsDayKey is the day a terms row is identified by within its facility.
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
// after it. rows must be ascending, which is what ListFacilityTerms returns.
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

// anyPriced reports whether any row in the timeline carries a non-zero rate.
func anyPriced(rows []FacilityTerms) bool {
	for _, r := range rows {
		if r.Rate > 0 {
			return true
		}
	}
	return false
}
