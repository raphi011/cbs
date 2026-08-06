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
//   - UTC, always, which formatTime imposes rather than the layout: the same
//     instant written at +02:00 renders with a later hour, and it sorts by its
//     digits and not by its instant, so one row stored with an offset puts a
//     listing silently out of order — the row is not wrong, the ORDER BY is.
//   - Nine fractional digits, always, INCLUDING when they are all zero. This is
//     the half that is easy to get wrong twice over, so it is worth stating what
//     the hazard is and is not. Go's RFC3339Nano drops trailing zeros, and for
//     two values that both HAVE a fraction that is harmless — ".45" sorts before
//     ".5" correctly, because a decimal fraction written without trailing zeros
//     still compares lexicographically as it does numerically. What breaks is a
//     whole second, which RFC3339Nano renders with no fraction at all:
//     "…:00Z" against "…:00.045Z" compares 'Z' with '.', and 'Z' is the larger
//     byte, so the EARLIER instant sorts last. A fixed width has no such seam.
//     Measured: TestOrderingIsChronologicalWithinOneSecond fails on exactly that
//     pair and on nothing else.
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
