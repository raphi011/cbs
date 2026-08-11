package calendar_test

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/calendar"
)

func TestEasterAcrossADecadeAndAtItsBounds(t *testing.T) {
	tests := []struct {
		year int
		want time.Time
	}{
		{2020, date(2020, time.April, 12)},
		{2021, date(2021, time.April, 4)},
		{2022, date(2022, time.April, 17)},
		{2023, date(2023, time.April, 9)},
		{2024, date(2024, time.March, 31)},
		{2025, date(2025, time.April, 20)},
		{2026, date(2026, time.April, 5)},
		{2027, date(2027, time.March, 28)},
		{2028, date(2028, time.April, 16)},
		{2029, date(2029, time.April, 1)},
		{2030, date(2030, time.April, 21)},

		// The two extremes of the rule, which is what a decade of ordinary
		// years cannot exercise: 25 April is the latest Easter can fall and
		// 22 March the earliest.
		{2038, date(2038, time.April, 25)},
		{2285, date(2285, time.March, 22)},
	}
	for _, tt := range tests {
		if got := calendar.Easter(tt.year); !got.Equal(tt.want) {
			t.Errorf("Easter(%d) = %s, want %s", tt.year, day(got), day(tt.want))
		}
	}
}

func TestEasterIsAlwaysASunday(t *testing.T) {
	for year := 1583; year <= 2200; year++ {
		if wd := calendar.Easter(year).Weekday(); wd != time.Sunday {
			t.Fatalf("Easter(%d) falls on a %s", year, wd)
		}
	}
}
