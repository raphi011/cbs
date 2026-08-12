package sqlite

import "time"

// timeLayout is how every instant is stored.
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
