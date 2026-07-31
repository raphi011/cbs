package product_test

import (
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	. "github.com/raphi011/cbs/product"
)

func day(n int) time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
}

// pricing is the shorthand every test below builds a version from.
func pricing(rate interest.Rate) OverdraftPricing {
	return OverdraftPricing{Rate: rate, DayCount: interest.ACT365}
}

// published is a version already through PublishVersion, hash and all. Tests
// that care about drafts build them by hand.
func published(id ID, from time.Time, rate interest.Rate) Version {
	v := Version{ProductID: id, EffectiveFrom: from, Overdraft: pricing(rate), PublishedAt: from}
	v.Hash = v.ComputeHash()
	return v
}

// The hash is over the identity and the pricing, and NOT over PublishedAt or
// CreatedAt: publishing stamps a time onto a row whose content did not change,
// so a hash covering it would differ before and after publication and could
// never be verified.
func TestHashCoversPricingAndNotLifecycleTimes(t *testing.T) {
	base := published("prd_1", day(0), 120_000)

	same := base
	same.PublishedAt = day(9)
	same.CreatedAt = day(9)
	if same.ComputeHash() != base.Hash {
		t.Fatalf("hash moved with a lifecycle time: %s != %s", same.ComputeHash(), base.Hash)
	}

	for name, mutate := range map[string]func(v *Version){
		"rate":       func(v *Version) { v.Overdraft.Rate = 130_000 },
		"unarranged": func(v *Version) { v.Overdraft.UnarrangedRate = 350_000 },
		"dayCount":   func(v *Version) { v.Overdraft.DayCount = interest.Thirty360 },
		"product":    func(v *Version) { v.ProductID = "prd_2" },
		"effective":  func(v *Version) { v.EffectiveFrom = day(1) },
	} {
		edited := base
		mutate(&edited)
		if edited.ComputeHash() == base.Hash {
			t.Errorf("%s: hash did not move", name)
		}
		if err := edited.VerifyHash(); !errors.Is(err, ErrHashMismatch) {
			t.Errorf("%s: VerifyHash = %v, want ErrHashMismatch", name, err)
		}
	}
}

// The hash is a day-granular fact like everything else on the row: two rows
// whose EffectiveFrom differ only within a day are the same row, so they must
// hash the same or an upsert would look like tampering.
func TestHashIsDayGranular(t *testing.T) {
	a := published("prd_1", day(0), 120_000)
	b := a
	b.EffectiveFrom = day(0).Add(17 * time.Hour)
	if b.ComputeHash() != a.Hash {
		t.Fatal("hash moved within a day")
	}
}

func TestVersionAtPicksTheLastPublishedRowNotAfterTheDay(t *testing.T) {
	rows := []Version{
		published("prd_1", day(0), 100_000),
		published("prd_1", day(30), 200_000),
		published("prd_1", day(60), 300_000),
	}

	for _, tc := range []struct {
		day  int
		want interest.Rate
	}{{0, 100_000}, {29, 100_000}, {30, 200_000}, {59, 200_000}, {60, 300_000}, {900, 300_000}} {
		got, err := VersionAt(rows, day(tc.day))
		if err != nil {
			t.Fatalf("day %d: %v", tc.day, err)
		}
		if got.Overdraft.Rate != tc.want {
			t.Errorf("day %d: rate = %d, want %d", tc.day, got.Overdraft.Rate, tc.want)
		}
	}

	if _, err := VersionAt(rows, day(-1)); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("before the first row: %v, want ErrVersionNotFound", err)
	}
	if _, err := VersionAt(nil, day(0)); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("no rows: %v, want ErrVersionNotFound", err)
	}
}

// A draft is invisible, and invisible means the row BEFORE it stays in force —
// not that the day has no version. An operator drafting next quarter's price
// must not change what today costs.
func TestVersionAtIgnoresDrafts(t *testing.T) {
	draft := Version{ProductID: "prd_1", EffectiveFrom: day(30), Overdraft: pricing(900_000)}
	rows := []Version{published("prd_1", day(0), 100_000), draft}

	got, err := VersionAt(rows, day(45))
	if err != nil {
		t.Fatalf("VersionAt: %v", err)
	}
	if got.Overdraft.Rate != 100_000 {
		t.Fatalf("rate = %d, want the published 100000", got.Overdraft.Rate)
	}

	if _, err := VersionAt([]Version{draft}, day(45)); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("only a draft: %v, want ErrVersionNotFound", err)
	}
}

// Resolution is where the hash earns its place: a row edited in the database
// stops the read rather than pricing a day from it.
func TestVersionAtRefusesATamperedRow(t *testing.T) {
	tampered := published("prd_1", day(0), 100_000)
	tampered.Overdraft.Rate = 999_000 // as if by UPDATE, leaving the old hash

	if _, err := VersionAt([]Version{tampered}, day(10)); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("VersionAt = %v, want ErrHashMismatch", err)
	}
}

// The one refused pricing combination, and it is the deposit layer's rule moved
// onto the catalogue so a product and an overlay are held to one standard: an
// unarranged rate with no arranged one prices only the excess, making the money
// drawn beyond the limit dearer than nothing while the facility inside it is
// free.
func TestPricingValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		in   OverdraftPricing
		want error
	}{
		"zero is a real interest-free product": {OverdraftPricing{}, nil},
		"arranged only":                        {OverdraftPricing{Rate: 120_000}, nil},
		"both":                                 {OverdraftPricing{Rate: 120_000, UnarrangedRate: 350_000}, nil},
		"negative arranged":                    {OverdraftPricing{Rate: -1}, ErrInvalidRate},
		"negative unarranged":                  {OverdraftPricing{Rate: 1, UnarrangedRate: -1}, ErrInvalidRate},
		"unarranged with no arranged":          {OverdraftPricing{UnarrangedRate: 350_000}, ErrInvalidRate},
	} {
		if err := tc.in.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

func TestProductValidate(t *testing.T) {
	if err := (Product{ID: "prd_1", Name: "Basic Current Account"}).Validate(); err != nil {
		t.Fatalf("valid product: %v", err)
	}
	if err := (Product{ID: "prd_1", Name: ""}).Validate(); err == nil {
		t.Error("empty name accepted")
	}
	if err := (Product{ID: "prd_1", Name: "X", Kind: Kind(7)}).Validate(); !errors.Is(err, ErrKindMismatch) {
		t.Error("unknown kind accepted")
	}
}
