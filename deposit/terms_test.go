package deposit

import (
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/product"
)

func termsDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// sampleTerms is a row with a NEGOTIATED price, so that the termsAt cases below
// stay about resolving a timeline rather than about the catalogue. A floating
// row would make every one of them depend on a product's versions too.
func sampleTerms(from time.Time, rate interest.Rate) OverdraftTerms {
	return OverdraftTerms{
		AccountID: "dep_1", EffectiveFrom: from, ProductID: "prd_basic", OverdraftLimit: 50_000,
		Pricing: &product.OverdraftPricing{Rate: rate, DayCount: interest.ACT365},
	}
}

func day(n int) time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
}

// publishedVersion is a catalogue version as PublishVersion leaves it: hashed,
// so Resolve's verification passes.
func publishedVersion(id product.ID, from time.Time, rate interest.Rate) product.Version {
	v := product.Version{
		ProductID: id, EffectiveFrom: from,
		Overdraft:   product.OverdraftPricing{Rate: rate, DayCount: interest.ACT365},
		PublishedAt: from,
	}
	v.Hash = v.ComputeHash()
	return v
}

// The six positions termsAt has to answer for. Nothing else in the system
// resolves a timeline, so a cursor-style bug here would show up only as an
// interest figure nobody can reproduce.
func TestTermsAtResolvesEveryPosition(t *testing.T) {
	rows := []OverdraftTerms{
		sampleTerms(termsDay(2025, time.January, 1), 100_000),
		sampleTerms(termsDay(2025, time.March, 1), 200_000),
		sampleTerms(termsDay(2025, time.June, 1), 300_000),
	}

	cases := []struct {
		name string
		day  time.Time
		want interest.Rate
		ok   bool
	}{
		{"before the first row", termsDay(2024, time.December, 31), 0, false},
		{"exactly on the first boundary", termsDay(2025, time.January, 1), 100_000, true},
		{"between rows", termsDay(2025, time.February, 14), 100_000, true},
		{"exactly on a later boundary", termsDay(2025, time.March, 1), 200_000, true},
		{"the day before a boundary", termsDay(2025, time.May, 31), 200_000, true},
		{"after the last row", termsDay(2030, time.January, 1), 300_000, true},
	}
	for _, c := range cases {
		got, ok := termsAt(rows, c.day)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && got.Pricing.Rate != c.want {
			t.Errorf("%s: rate = %d, want %d", c.name, got.Pricing.Rate, c.want)
		}
	}
}

// A time of day on the query must not move the answer. Accrual iterates whole
// UTC days, and a caller that has not truncated is asking about the same day.
func TestTermsAtIgnoresTheTimeOfDay(t *testing.T) {
	rows := []OverdraftTerms{sampleTerms(termsDay(2025, time.March, 1), 200_000)}

	if _, ok := termsAt(rows, time.Date(2025, time.February, 28, 23, 59, 0, 0, time.UTC)); ok {
		t.Error("late on the day before resolved the row")
	}
	got, ok := termsAt(rows, time.Date(2025, time.March, 1, 0, 0, 1, 0, time.UTC))
	if !ok || got.Pricing.Rate != 200_000 {
		t.Errorf("a second into the effective day: %v", ok)
	}
}

func TestTermsAtOnASingleRow(t *testing.T) {
	rows := []OverdraftTerms{sampleTerms(termsDay(2025, time.January, 1), 100_000)}

	if _, ok := termsAt(rows, termsDay(2024, time.December, 31)); ok {
		t.Error("resolved a day before the only row")
	}
	got, ok := termsAt(rows, termsDay(2025, time.January, 1))
	if !ok || got.Pricing.Rate != 100_000 {
		t.Errorf("on the boundary: %v", ok)
	}
}

func TestTermsAtOnAnEmptyTimeline(t *testing.T) {
	if _, ok := termsAt(nil, termsDay(2025, time.January, 1)); ok {
		t.Error("resolved against no rows at all")
	}
}

// termsAt takes rows ALREADY ascending — that is the store's contract — and
// this pins that a backdated insert reaching the resolver in ascending order
// (which is what List returns after the store has sorted it) resolves the same
// as if it had always been there.
func TestTermsAtAfterABackdatedInsert(t *testing.T) {
	rows := []OverdraftTerms{
		sampleTerms(termsDay(2025, time.January, 1), 100_000),
		sampleTerms(termsDay(2025, time.February, 1), 250_000), // entered last, effective in the middle
		sampleTerms(termsDay(2025, time.March, 1), 200_000),
	}
	got, ok := termsAt(rows, termsDay(2025, time.February, 15))
	if !ok || got.Pricing.Rate != 250_000 {
		t.Errorf("mid-February: %v", ok)
	}
}

// A zero-rate OVERLAY is a definite statement that those days are free, so it
// does not make the run happen; a floating row on a priced product does, and
// that is the case the old account-level `Rate <= 0` guard could not express.
func TestAnyPricedLooksThroughToTheProduct(t *testing.T) {
	free := product.OverdraftPricing{}
	floating := []OverdraftTerms{{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic"}}
	overlaid := []OverdraftTerms{{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic", Pricing: &free}}

	priced := map[product.ID][]product.Version{"prd_basic": {publishedVersion("prd_basic", day(0), 120_000)}}
	unpriced := map[product.ID][]product.Version{"prd_basic": {publishedVersion("prd_basic", day(0), 0)}}

	if !anyPriced(floating, priced) {
		t.Error("a floating row on a priced product read as unpriced")
	}
	if anyPriced(floating, unpriced) {
		t.Error("a floating row on a free product read as priced")
	}
	if anyPriced(overlaid, priced) {
		t.Error("a zero-rate overlay read as priced")
	}
	// A non-zero overlay is priced on its own account, whatever the product says.
	negotiated := product.OverdraftPricing{Rate: 90_000}
	if !anyPriced([]OverdraftTerms{{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic", Pricing: &negotiated}}, unpriced) {
		t.Error("a negotiated rate on a free product read as unpriced")
	}
}

func TestTermsDayKeyIsTheUTCCalendarDay(t *testing.T) {
	// Two instants on the same UTC day produce one key, which is what makes
	// the row identity a DAY rather than a moment.
	morning := time.Date(2025, time.March, 1, 9, 0, 0, 0, time.UTC)
	night := time.Date(2025, time.March, 1, 23, 30, 0, 0, time.UTC)
	if TermsDayKey(morning) != TermsDayKey(night) {
		t.Errorf("keys differ within one day: %q vs %q", TermsDayKey(morning), TermsDayKey(night))
	}
	if got := TermsDayKey(morning); got != "2025-03-01" {
		t.Errorf("TermsDayKey = %q, want 2025-03-01", got)
	}
	// ISO days sort lexicographically, which is the whole reason the key is
	// rendered this way: the store compares it as a string and gets a day
	// comparison for free, with no date arithmetic in any dialect.
	if !(TermsDayKey(termsDay(2025, time.February, 28)) < TermsDayKey(termsDay(2025, time.March, 1))) {
		t.Error("day keys do not sort lexicographically")
	}
}

func TestValidateRequiresAProductAndACompletePricing(t *testing.T) {
	base := OverdraftTerms{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic", OverdraftLimit: 1}

	// A floating row is complete: its price comes from the product.
	if err := base.Validate(); err != nil {
		t.Fatalf("a floating row: %v", err)
	}
	noProduct := base
	noProduct.ProductID = ""
	if err := noProduct.Validate(); !errors.Is(err, ErrProductRequired) {
		t.Errorf("no product: %v, want ErrProductRequired", err)
	}
	negativeLimit := base
	negativeLimit.OverdraftLimit = -1
	if err := negativeLimit.Validate(); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("negative limit: %v, want ErrInvalidAmount", err)
	}

	// The overlay's own rules are product.OverdraftPricing's, re-wrapped in this
	// layer's sentinel so a deposit caller has one thing to check.
	for name, pricing := range map[string]product.OverdraftPricing{
		"negative arranged":   {Rate: -1},
		"negative unarranged": {Rate: 1, UnarrangedRate: -1},
		// The one combination that would price ONLY the excess, making the money
		// drawn beyond the limit dearer than nothing while leaving the facility
		// inside it free.
		"unarranged with no arranged": {UnarrangedRate: 350_000},
	} {
		bad := base
		bad.Pricing = &pricing
		if err := bad.Validate(); !errors.Is(err, ErrInvalidRate) {
			t.Errorf("%s: %v, want ErrInvalidRate", name, err)
		}
	}

	// A zero-rate overlay is a real product: an interest-free overdraft, and a
	// different statement from a nil overlay.
	free := base
	free.Pricing = &product.OverdraftPricing{}
	if err := free.Validate(); err != nil {
		t.Errorf("interest-free overdraft: %v, want nil", err)
	}
}

// A floating row takes its pricing from the product version in force on the
// day, not from the version in force when the row was written. That is the
// point: one published version reprices every account bound to it, per day,
// without touching a single account row.
func TestResolveFloatsWithTheProductVersion(t *testing.T) {
	rows := []OverdraftTerms{
		{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic", OverdraftLimit: 50_000},
	}
	versions := map[product.ID][]product.Version{
		"prd_basic": {
			publishedVersion("prd_basic", day(0), 120_000),
			publishedVersion("prd_basic", day(30), 180_000),
		},
	}

	for _, tc := range []struct {
		day  int
		want interest.Rate
	}{{0, 120_000}, {29, 120_000}, {30, 180_000}, {365, 180_000}} {
		got, err := Resolve(rows, versions, day(tc.day))
		if err != nil {
			t.Fatalf("day %d: %v", tc.day, err)
		}
		if got.Pricing.Rate != tc.want {
			t.Errorf("day %d: rate = %d, want %d", tc.day, got.Pricing.Rate, tc.want)
		}
		if got.Limit != 50_000 {
			t.Errorf("day %d: limit = %d, want the account's own 50000", tc.day, got.Limit)
		}
	}
}

// An overlay is this customer's price instead of the product's, and it wins for
// as long as its row is in force. Setting one, and later clearing it, are two
// ordinary rows on the account's own timeline.
func TestResolvePrefersAnOverlayAndFallsBackWhenCleared(t *testing.T) {
	negotiated := product.OverdraftPricing{Rate: 90_000, DayCount: interest.ACT365}
	rows := []OverdraftTerms{
		{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic", OverdraftLimit: 50_000},
		{AccountID: "dep_1", EffectiveFrom: day(10), ProductID: "prd_basic", OverdraftLimit: 50_000, Pricing: &negotiated},
		{AccountID: "dep_1", EffectiveFrom: day(40), ProductID: "prd_basic", OverdraftLimit: 50_000},
	}
	versions := map[product.ID][]product.Version{
		"prd_basic": {
			publishedVersion("prd_basic", day(0), 120_000),
			publishedVersion("prd_basic", day(30), 180_000),
		},
	}

	for _, tc := range []struct {
		day  int
		want interest.Rate
		why  string
	}{
		{9, 120_000, "floating, before the overlay"},
		{10, 90_000, "the overlay starts"},
		{35, 90_000, "the overlay outranks a product reprice under it"},
		{40, 180_000, "the overlay is cleared, back to the product"},
	} {
		got, err := Resolve(rows, versions, day(tc.day))
		if err != nil {
			t.Fatalf("day %d (%s): %v", tc.day, tc.why, err)
		}
		if got.Pricing.Rate != tc.want {
			t.Errorf("day %d (%s): rate = %d, want %d", tc.day, tc.why, got.Pricing.Rate, tc.want)
		}
	}
}

// The limit never floats. It is on the account's own row for every day of its
// life, because a limit is an underwriting decision about one customer — and
// product.OverdraftPricing cannot even express one.
func TestResolveTakesTheLimitFromTheAccountAlways(t *testing.T) {
	rows := []OverdraftTerms{
		{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic", OverdraftLimit: 50_000},
		{AccountID: "dep_1", EffectiveFrom: day(20), ProductID: "prd_basic", OverdraftLimit: 200_000},
	}
	versions := map[product.ID][]product.Version{
		"prd_basic": {publishedVersion("prd_basic", day(0), 120_000)},
	}

	before, err := Resolve(rows, versions, day(19))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Resolve(rows, versions, day(20))
	if err != nil {
		t.Fatal(err)
	}
	if before.Limit != 50_000 || after.Limit != 200_000 {
		t.Fatalf("limits = %d then %d, want 50000 then 200000", before.Limit, after.Limit)
	}
	if before.Pricing.Rate != after.Pricing.Rate {
		t.Error("a limit change moved the rate")
	}
}

// An account migrated between products resolves each day against the product in
// force on THAT day. A single flat slice of versions could not express this,
// which is why Resolve takes a map keyed by product.
func TestResolveAcrossAProductMigration(t *testing.T) {
	rows := []OverdraftTerms{
		{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic", OverdraftLimit: 50_000},
		{AccountID: "dep_1", EffectiveFrom: day(50), ProductID: "prd_premium", OverdraftLimit: 50_000},
	}
	versions := map[product.ID][]product.Version{
		"prd_basic":   {publishedVersion("prd_basic", day(0), 120_000)},
		"prd_premium": {publishedVersion("prd_premium", day(0), 70_000)},
	}

	before, err := Resolve(rows, versions, day(49))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Resolve(rows, versions, day(50))
	if err != nil {
		t.Fatal(err)
	}
	if before.Pricing.Rate != 120_000 || before.ProductID != "prd_basic" {
		t.Errorf("before the migration: %s at %d", before.ProductID, before.Pricing.Rate)
	}
	if after.Pricing.Rate != 70_000 || after.ProductID != "prd_premium" {
		t.Errorf("after the migration: %s at %d", after.ProductID, after.Pricing.Rate)
	}
}

func TestResolveFailures(t *testing.T) {
	rows := []OverdraftTerms{
		{AccountID: "dep_1", EffectiveFrom: day(10), ProductID: "prd_basic", OverdraftLimit: 50_000},
	}
	versions := map[product.ID][]product.Version{
		"prd_basic": {publishedVersion("prd_basic", day(10), 120_000)},
	}

	// A day before the account's first row: the account did not exist.
	if _, err := Resolve(rows, versions, day(9)); !errors.Is(err, ErrTermsNotFound) {
		t.Errorf("before the opening row: %v, want ErrTermsNotFound", err)
	}
	// A floating row whose product has no published version for the day.
	if _, err := Resolve(rows, map[product.ID][]product.Version{}, day(10)); !errors.Is(err, product.ErrVersionNotFound) {
		t.Errorf("no versions: %v, want ErrVersionNotFound", err)
	}
	// A tampered version stops the read rather than pricing the day.
	tampered := publishedVersion("prd_basic", day(10), 120_000)
	tampered.Overdraft.Rate = 999_000
	if _, err := Resolve(rows, map[product.ID][]product.Version{"prd_basic": {tampered}}, day(10)); !errors.Is(err, product.ErrHashMismatch) {
		t.Errorf("tampered version: %v, want ErrHashMismatch", err)
	}
}
