package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// DayCount is the convention for turning a pair of dates into a fraction of a
// year.
type DayCount int

const (
	// ACT365 counts actual elapsed days over a 365-day year. Most retail
	// lending, and sterling money markets.
	ACT365 DayCount = iota
	// ACT360 counts actual elapsed days over a 360-day year, so a year of
	// daily accrual comes to 365/360 of the nominal rate. Euro money markets
	// and US commercial lending.
	ACT360
	// Thirty360 treats every month as 30 days and every year as 360. US mortgages
	// and most bonds.
	Thirty360
)

func (d DayCount) String() string {
	switch d {
	case ACT365:
		return "ACT/365"
	case ACT360:
		return "ACT/360"
	case Thirty360:
		return "30/360"
	default:
		return "Unknown"
	}
}

// YearDays is the denominator of the year fraction.
func (d DayCount) YearDays() int {
	if d == ACT365 {
		return 365
	}
	return 360
}

// Days counts the days between two dates under this convention. It is negative
// when to precedes from, which is what lets a caller detect an accrual that
// would run backwards rather than silently accruing nothing.
func (d DayCount) Days(from, to time.Time) int {
	if d == Thirty360 {
		return thirty360Days(from, to)
	}
	f := truncateToDay(from)
	t := truncateToDay(to)
	return int(t.Sub(f).Hours() / 24)
}

func truncateToDay(t time.Time) time.Time { return ledger.DayStart(t) }

// thirty360Days is the US/NASD form of the convention: the 31st collapses onto
// the 30th, which is what makes every month exactly 30 days.
func thirty360Days(from, to time.Time) int {
	f := truncateToDay(from)
	t := truncateToDay(to)
	d1, d2 := f.Day(), t.Day()
	if d1 == 31 {
		d1 = 30
	}
	if d2 == 31 && d1 == 30 {
		d2 = 30
	}
	return 360*(t.Year()-f.Year()) + 30*(int(t.Month())-int(f.Month())) + (d2 - d1)
}
