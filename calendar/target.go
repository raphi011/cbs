package calendar

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Holiday is one of the six days the Eurosystem closes TARGET on, beyond the
// weekends it closes every week.
//
// A weekend is not a holiday. Both are days money cannot move on, and only one
// of them has a name; keeping them apart is what lets an operator console say
// "shut for Easter Monday" where it would otherwise say "shut".
type Holiday string

const (
	NewYearsDay  Holiday = "New Year's Day"
	GoodFriday   Holiday = "Good Friday"
	EasterMonday Holiday = "Easter Monday"
	LabourDay    Holiday = "Labour Day"
	ChristmasDay Holiday = "Christmas Day"

	// BoxingDay is named by its date and not by a name, because the Eurosystem's
	// list is: the day is Boxing Day in Ireland, St Stephen's Day in Italy and
	// the second Christmas day in Germany, and the calendar takes no side.
	BoxingDay Holiday = "26 December"
)

// HolidayOn names the TARGET holiday t falls on, if it falls on one. Easter
// Sunday is not among them — it is a Sunday, and TARGET is shut on those
// anyway.
func HolidayOn(t time.Time) (Holiday, bool) {
	d := ledger.DayStart(t)

	switch {
	case d.Month() == time.January && d.Day() == 1:
		return NewYearsDay, true
	case d.Month() == time.May && d.Day() == 1:
		return LabourDay, true
	case d.Month() == time.December && d.Day() == 25:
		return ChristmasDay, true
	case d.Month() == time.December && d.Day() == 26:
		return BoxingDay, true
	}

	// The two movable days are computed for d's own year, which is enough
	// because Easter is never within two days of a year boundary: the earliest
	// it can fall is 22 March and the latest 25 April.
	easter := Easter(d.Year())
	switch {
	case d.Equal(easter.AddDate(0, 0, -2)):
		return GoodFriday, true
	case d.Equal(easter.AddDate(0, 0, 1)):
		return EasterMonday, true
	}
	return "", false
}

// IsSettlementDay reports whether the settlement agent is open on t's day, which
// is whether money can move at all: no cycle nets, no net position settles and
// no output file is released on a day this answers false for.
//
// It does not gate a customer instruction. A bank accepts one on a Sunday and
// holds it for the next file, which is what a store-and-forward scheme against a
// cut-off clock does.
func IsSettlementDay(t time.Time) bool {
	d := ledger.DayStart(t)
	if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	_, holiday := HolidayOn(d)
	return !holiday
}

// NextSettlementDay is the first settlement day strictly after t, so calling it
// on a settlement day still moves.
//
// The loop needs no bound. The longest closure TARGET has is four days — Good
// Friday through Easter Monday, and 25 and 26 December against a weekend — so
// no year exists without a settlement day in it.
func NextSettlementDay(t time.Time) time.Time {
	d := ledger.DayStart(t).AddDate(0, 0, 1)
	for !IsSettlementDay(d) {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// AddSettlementDays is the day n settlement days after t, counting backwards for
// a negative n. Zero is t's own day whether or not the agent is open on it: D+0
// is not a business-day question.
//
// This is what a rulebook deadline is counted in. EPC SCT's return window is
// three BANKING BUSINESS DAYS, and a credit that arrives on a Thursday is due
// back the following Tuesday rather than on the Sunday three calendar days
// later — see payment.ReturnWindowDays for what that approximation costs while
// nothing counts in this.
func AddSettlementDays(t time.Time, n int) time.Time {
	d := ledger.DayStart(t)
	step := 1
	if n < 0 {
		step, n = -1, -n
	}
	for range n {
		d = d.AddDate(0, 0, step)
		for !IsSettlementDay(d) {
			d = d.AddDate(0, 0, step)
		}
	}
	return d
}
