package sqlite

import "time"

// timeLayout is how every instant is stored.
//
// TEXT rather than an integer epoch, and this exact layout rather than any
// RFC3339, because the property being bought is that a string comparison IS a
// chronological comparison. README already makes that argument for day keys —
// an ISO day sorts lexicographically, so ORDER BY on the key orders by date —
// and this applies it to full timestamps.
//
// Both halves of the layout are load-bearing and neither is obvious:
//
//   - UTC, always. A value written at +02:00 sorts by its digits, not by its
//     instant, so one row stored with an offset puts a listing silently out of
//     order — the row is not wrong, the ORDER BY is.
//   - Nine fractional digits, always, including the zeros. Go's RFC3339Nano
//     drops trailing zeros, so "…:00.5Z" and "…:00.45Z" compare in the wrong
//     order as strings. A fixed width is what makes the digits comparable.
//
// Nullability is unchanged from store/pg: NULL is Go's zero time.Time, which
// several fields use as "unset". SQLite sorts NULL first in an ASC ordering,
// which is what the existing NULLS FIRST convention wants.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatTime renders an instant for storage. See timeLayout.
func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTime reads an instant back.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// nowText is the applied-at stamp migrations record. It reads the wall clock
// rather than the store's, because a migration is not a domain event and a
// frozen test clock would make every migration claim to have run at the epoch.
func nowText() string { return formatTime(time.Now()) }
