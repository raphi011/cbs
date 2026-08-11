package ebics

import "testing"

func TestOrderIDsAreMintedInTheProtocolsFormat(t *testing.T) {
	tests := []struct {
		n    int
		want OrderID
	}{
		{0, "A000"},
		{1, "A001"},
		{9, "A009"},

		// Base 36: the digits run out and the letters carry on.
		{10, "A00A"},
		{35, "A00Z"},
		{36, "A010"},
		{36 * 36, "A100"},

		// The leading character is a letter of its own, so a host's ids are
		// legible in the order it allocated them.
		{36 * 36 * 36, "B000"},
		{orderIDSpace - 1, "ZZZZ"},

		// It wraps, as the protocol's does.
		{orderIDSpace, "A000"},
	}
	for _, tt := range tests {
		if got := mintOrderID(tt.n); got != tt.want {
			t.Errorf("mintOrderID(%d) = %s, want %s", tt.n, got, tt.want)
		}
	}
}

func TestEveryMintedOrderIDIsDistinctUntilItWraps(t *testing.T) {
	seen := map[OrderID]int{}
	for n := range 5000 {
		id := mintOrderID(n)
		if first, dup := seen[id]; dup {
			t.Fatalf("mintOrderID(%d) = %s, already minted at %d", n, id, first)
		}
		if len(id) != 4 {
			t.Fatalf("mintOrderID(%d) = %q, want four characters", n, id)
		}
		seen[id] = n
	}
}

func TestASubscriberIsRecognisedThroughTheHeadersUntidiness(t *testing.T) {
	if got := normaliseSubscriber("  aurodeffxxx \n"); got != "AURODEFFXXX" {
		t.Errorf("normaliseSubscriber = %q, want AURODEFFXXX", got)
	}
}
