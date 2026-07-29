package ledger_test

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
)

func TestDayStartDiscardsTimeOfDay(t *testing.T) {
	morning := time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 3, 15, 23, 0, 0, 0, time.UTC)
	want := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	if got := ledger.DayStart(morning); !got.Equal(want) {
		t.Errorf("DayStart(morning) = %v, want %v", got, want)
	}
	if got := ledger.DayStart(evening); !got.Equal(want) {
		t.Errorf("DayStart(evening) = %v, want %v", got, want)
	}
}

func TestDayStartNormalisesToUTC(t *testing.T) {
	// 2026-03-15 01:00 +02:00 is 2026-03-14 23:00 UTC, so its UTC day is the 14th.
	zone := time.FixedZone("CEST", 2*60*60)
	got := ledger.DayStart(time.Date(2026, 3, 15, 1, 0, 0, 0, zone))
	want := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("DayStart = %v, want %v", got, want)
	}
}

func TestNextDayIsExclusiveEnd(t *testing.T) {
	got := ledger.NextDay(time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC))
	want := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("NextDay = %v, want %v", got, want)
	}
}
