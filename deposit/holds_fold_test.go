// This file is in package deposit rather than deposit_test: ActiveHoldTotal
// takes a HoldLister and needs no store, so nothing here reaches store/sqlite
// and there is no import cycle to avoid.
package deposit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// holdList is a HoldLister over a fixed slice, narrowed by account the way the
// store's WHERE clause narrows it.
type holdList struct {
	holds []Hold
	err   error
}

func (h holdList) ListHoldsForAccount(_ context.Context, _ ledger.BookID, id AccountID) ([]Hold, error) {
	if h.err != nil {
		return nil, h.err
	}
	out := make([]Hold, 0, len(h.holds))
	for _, held := range h.holds {
		if held.AccountID == id {
			out = append(out, held)
		}
	}
	return out, nil
}

var (
	holdNow       = time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	holdYesterday = holdNow.Add(-24 * time.Hour)
	holdTomorrow  = holdNow.Add(24 * time.Hour)
)

func heldFor(id HoldID, account AccountID, amount ledger.Amount, status HoldStatus, expires time.Time) Hold {
	return Hold{ID: id, AccountID: account, Amount: amount, Status: status, ExpiresAt: expires}
}

// The holds one account has, one of each way a hold can fail to count.
func holdFixture() []Hold {
	return []Hold{
		heldFor("hld_1", "dep_1", 100, HoldActive, time.Time{}),
		heldFor("hld_2", "dep_1", 200, HoldActive, holdTomorrow),
		heldFor("hld_3", "dep_1", 400, HoldReleased, time.Time{}),
		heldFor("hld_4", "dep_1", 800, HoldCaptured, time.Time{}),
		heldFor("hld_5", "dep_1", 1600, HoldActive, holdYesterday),
		heldFor("hld_6", "dep_2", 3200, HoldActive, time.Time{}),
	}
}

func TestActiveHoldTotalCountsOnlyWhatReducesTheAvailableBalance(t *testing.T) {
	l := holdList{holds: holdFixture()}
	ctx := context.Background()

	cases := []struct {
		label   string
		account AccountID
		now     time.Time
		want    ledger.Amount
	}{
		// Released, captured and already-expired holds are all out.
		{"active and unexpired only", "dep_1", holdNow, 300},
		{"another account's holds are its own", "dep_2", holdNow, 3200},
		// Like a balance, an unknown account is zero rather than an error.
		{"an account with no holds", "dep_nope", holdNow, 0},
		// Expiry is against the now passed in, not a clock the fold reads:
		// rewind past hld_5's expiry and it counts again.
		{"before every expiry", "dep_1", holdYesterday.Add(-time.Hour), 1900},
		// A hold expiring exactly at now has not expired yet.
		{"exactly at an expiry", "dep_1", holdTomorrow, 300},
	}
	for _, c := range cases {
		got, err := ActiveHoldTotal(ctx, l, "book", c.account, c.now)
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		if got != c.want {
			t.Errorf("%s = %d, want %d", c.label, got, c.want)
		}
	}
}

func TestActiveHoldTotalStopsOnTheListersError(t *testing.T) {
	boom := errors.New("the listing failed")
	_, err := ActiveHoldTotal(context.Background(), holdList{err: boom}, "book", "dep_1", holdNow)
	if !errors.Is(err, boom) {
		t.Errorf("returned %v, want the lister's error", err)
	}
}
