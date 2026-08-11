package calendar_test

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/calendar"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func day(t time.Time) string { return t.Format("2006-01-02") }

// TestTheSixHolidaysAcrossADecade walks every year from 2020 to 2030 and asserts
// that each of the six closing days is named and shut. The two movable ones are
// derived from Easter, which its own suite pins to known dates, so a computus
// that drifted would fail there and be caught here as well.
func TestTheSixHolidaysAcrossADecade(t *testing.T) {
	for year := 2020; year <= 2030; year++ {
		easter := calendar.Easter(year)
		cases := []struct {
			on   time.Time
			want calendar.Holiday
		}{
			{date(year, time.January, 1), calendar.NewYearsDay},
			{easter.AddDate(0, 0, -2), calendar.GoodFriday},
			{easter.AddDate(0, 0, 1), calendar.EasterMonday},
			{date(year, time.May, 1), calendar.LabourDay},
			{date(year, time.December, 25), calendar.ChristmasDay},
			{date(year, time.December, 26), calendar.BoxingDay},
		}
		for _, c := range cases {
			got, ok := calendar.HolidayOn(c.on)
			if !ok {
				t.Errorf("%s: no holiday, want %s", day(c.on), c.want)
				continue
			}
			if got != c.want {
				t.Errorf("%s: holiday %s, want %s", day(c.on), got, c.want)
			}
			if calendar.IsSettlementDay(c.on) {
				t.Errorf("%s (%s): a settlement day", day(c.on), c.want)
			}
		}

		// Easter Sunday closes TARGET as every Sunday does, and is not on the
		// list of named days.
		if _, ok := calendar.HolidayOn(easter); ok {
			t.Errorf("%s: Easter Sunday is named a holiday", day(easter))
		}
	}
}

func TestAWeekendIsShutAndIsNotAHoliday(t *testing.T) {
	saturday := date(2026, time.September, 19)
	sunday := date(2026, time.September, 20)

	for _, d := range []time.Time{saturday, sunday} {
		if calendar.IsSettlementDay(d) {
			t.Errorf("%s (%s): a settlement day", day(d), d.Weekday())
		}
		if h, ok := calendar.HolidayOn(d); ok {
			t.Errorf("%s (%s): named %s", day(d), d.Weekday(), h)
		}
	}
}

// TestANationalHolidayIsASettlementDay is the distinction the whole calendar
// rests on: TARGET is the Eurosystem's, not any member state's.
func TestANationalHolidayIsASettlementDay(t *testing.T) {
	// German Unity Day, on a Friday. Every branch in Germany is shut and the
	// settlement agent is open.
	unityDay := date(2025, time.October, 3)

	if !calendar.IsSettlementDay(unityDay) {
		t.Errorf("%s: shut, want a settlement day", day(unityDay))
	}
}

// TestNoSubstituteDayWhenAHolidayFallsOnAWeekend pins the difference from the UK
// and US calendars, which move an observance to the following Monday.
func TestNoSubstituteDayWhenAHolidayFallsOnAWeekend(t *testing.T) {
	// 25 December 2021 is a Saturday and 26 December a Sunday, so the year
	// loses both closing days.
	monday := date(2021, time.December, 27)

	if !calendar.IsSettlementDay(monday) {
		t.Errorf("%s: shut, want a settlement day", day(monday))
	}
}

func TestNextSettlementDayCrossesTheLongestClosure(t *testing.T) {
	tests := []struct {
		name string
		from time.Time
		want time.Time
	}{
		// Good Friday, the weekend and Easter Monday are four days shut, the
		// longest run TARGET has.
		{"Easter 2026", date(2026, time.April, 2), date(2026, time.April, 7)},

		// An ordinary Friday, over an ordinary weekend.
		{"a weekend", date(2026, time.September, 18), date(2026, time.September, 21)},

		// Called on a settlement day it still moves: the answer is strictly
		// after the day asked about.
		{"midweek", date(2026, time.September, 16), date(2026, time.September, 17)},

		// The time of day is not part of the question.
		{"an evening", time.Date(2026, time.September, 18, 23, 30, 0, 0, time.UTC), date(2026, time.September, 21)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calendar.NextSettlementDay(tt.from); !got.Equal(tt.want) {
				t.Errorf("NextSettlementDay(%s) = %s, want %s", day(tt.from), day(got), day(tt.want))
			}
		})
	}
}

// TestAddSettlementDaysCountsARulebookWindow is the case payment.ReturnWindowDays
// documents itself as getting wrong: a credit that arrives on a Thursday is due
// back on the Tuesday, where three calendar days would call it overdue on the
// Sunday.
func TestAddSettlementDaysCountsARulebookWindow(t *testing.T) {
	thursday := date(2026, time.June, 11)

	if got, want := calendar.AddSettlementDays(thursday, 3), date(2026, time.June, 16); !got.Equal(want) {
		t.Errorf("Thursday + 3 settlement days = %s, want %s", day(got), day(want))
	}
	if got, want := thursday.AddDate(0, 0, 3), date(2026, time.June, 14); !got.Equal(want) || calendar.IsSettlementDay(got) {
		t.Errorf("Thursday + 3 calendar days = %s, want the shut %s", day(got), day(want))
	}
}

func TestAddSettlementDays(t *testing.T) {
	tests := []struct {
		name string
		from time.Time
		n    int
		want time.Time
	}{
		// Zero is the day itself, open or shut — D+0 is not a business-day
		// question.
		{"zero on a settlement day", date(2026, time.September, 16), 0, date(2026, time.September, 16)},
		{"zero on a Sunday", date(2026, time.September, 20), 0, date(2026, time.September, 20)},

		// Counting from a shut day starts at the next open one, so a Saturday
		// and the Friday before it are one day apart, not two.
		{"one from a Saturday", date(2026, time.September, 19), 1, date(2026, time.September, 21)},
		{"one from the Friday", date(2026, time.September, 18), 1, date(2026, time.September, 21)},

		// Backwards, over the same weekend.
		{"back over a weekend", date(2026, time.September, 21), -1, date(2026, time.September, 18)},

		// Five settlement days is eight calendar days here, because the run
		// holds a weekend and 1 May.
		{"a week across Labour Day", date(2026, time.April, 28), 5, date(2026, time.May, 6)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calendar.AddSettlementDays(tt.from, tt.n); !got.Equal(tt.want) {
				t.Errorf("AddSettlementDays(%s, %d) = %s, want %s", day(tt.from), tt.n, day(got), day(tt.want))
			}
		})
	}
}
