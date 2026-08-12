package ledger

import "time"

// DayStart is UTC midnight of t's day.
func DayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// NextDay is the exclusive end of t's day: DayStart of the following day.
func NextDay(t time.Time) time.Time {
	return DayStart(t).AddDate(0, 0, 1)
}
