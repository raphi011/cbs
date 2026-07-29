package ledger

import "time"

// DayStart is UTC midnight of t's day.
//
// A business date is a date. An end-of-day run at 23:00 must cover the same day
// as one at 09:00, and a value date carrying a time of day must land in the same
// bucket as one that does not. Every day boundary in this system is computed
// here, in Go, and passed to the store as an ordinary timestamp bound — rather
// than each store truncating for itself, which is one DST-adjacent edge case
// away from store/pg and store/mem disagreeing about which day an entry is in.
func DayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// NextDay is the exclusive end of t's day: DayStart of the following day.
func NextDay(t time.Time) time.Time {
	return DayStart(t).AddDate(0, 0, 1)
}
