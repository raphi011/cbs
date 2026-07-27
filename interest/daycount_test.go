package interest

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestDayCount_Days(t *testing.T) {
	tests := []struct {
		name     string
		dc       DayCount
		from, to time.Time
		want     int
	}{
		// A 31-day month is 31 actual days but 30 under the 30/360 fiction.
		{"ACT365 Jan to Feb", ACT365, date(2025, time.January, 15), date(2025, time.February, 15), 31},
		{"ACT360 Jan to Feb", ACT360, date(2025, time.January, 15), date(2025, time.February, 15), 31},
		{"30/360 Jan to Feb", Thirty360, date(2025, time.January, 15), date(2025, time.February, 15), 30},

		// February is short in actual days and still 30 under 30/360.
		{"ACT365 Feb to Mar", ACT365, date(2025, time.February, 15), date(2025, time.March, 15), 28},
		{"30/360 Feb to Mar", Thirty360, date(2025, time.February, 15), date(2025, time.March, 15), 30},

		{"one day", ACT365, date(2025, time.January, 15), date(2025, time.January, 16), 1},
		{"same day", ACT365, date(2025, time.January, 15), date(2025, time.January, 15), 0},
		{"backwards is negative", ACT365, date(2025, time.January, 16), date(2025, time.January, 15), -1},

		// The time of day is not part of a business date: an accrual run at
		// 23:00 covers the same day as one run at 09:00.
		{"time of day is ignored", ACT365,
			time.Date(2025, time.January, 15, 23, 0, 0, 0, time.UTC),
			time.Date(2025, time.January, 16, 9, 0, 0, 0, time.UTC), 1},

		// The 31st collapses onto the 30th, which is what makes every
		// 30/360 month exactly a twelfth of a year.
		{"30/360 month ends", Thirty360, date(2025, time.January, 31), date(2025, time.February, 28), 28},
		{"30/360 full year", Thirty360, date(2025, time.January, 15), date(2026, time.January, 15), 360},
		{"ACT365 full year", ACT365, date(2025, time.January, 15), date(2026, time.January, 15), 365},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dc.Days(tt.from, tt.to); got != tt.want {
				t.Errorf("%s.Days = %d, want %d", tt.dc, got, tt.want)
			}
		})
	}
}

func TestDayCount_YearDays(t *testing.T) {
	for _, tt := range []struct {
		dc   DayCount
		want int
	}{{ACT365, 365}, {ACT360, 360}, {Thirty360, 360}} {
		if got := tt.dc.YearDays(); got != tt.want {
			t.Errorf("%s.YearDays = %d, want %d", tt.dc, got, tt.want)
		}
	}
}
