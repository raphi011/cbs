package calendar

import "time"

// Easter is Easter Sunday of year, in the Gregorian reckoning the Western
// churches keep and the Eurosystem's calendar follows. The Orthodox date, which
// is the same feast computed on the Julian calendar, is usually a different day
// and closes nothing here.
//
// Two of TARGET's six holidays hang off it — Good Friday two days before, Easter
// Monday the day after — so a settlement calendar cannot be a list of fixed
// dates. The date itself is a lunar rule: the Sunday after the first
// ecclesiastical full moon on or after 21 March, where "ecclesiastical" means a
// tabulated moon rather than the astronomical one. It is bounded by 22 March and
// 25 April.
//
// The algorithm is the anonymous Gregorian computus (Meeus, Jones, Butcher). Its
// intermediate values are deliberately left with the published one-letter names:
// they stand for nothing a better name could say, and inventing names for them
// would make the arithmetic look like it had a meaning to follow. It is exact
// for any year from 1583, the first full year of the Gregorian calendar.
func Easter(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451

	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1

	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}
