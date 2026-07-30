# Product Catalogue Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deposit accounts are opened from a named product whose pricing is an effective-dated timeline of immutable published versions, with the overdraft limit pinned per account and a per-account overlay for a negotiated rate.

**Architecture:** A new leaf package `product` holds the catalogue (`Product`, `Version`, `OverdraftPricing`) and its own store interface; `deposit.Tx` embeds `product.Tx` so one concrete transaction spans both. `deposit.OverdraftTerms` gains `ProductID` and an optional `*product.OverdraftPricing` overlay, and a new pure `deposit.Resolve` merges the two timelines for one day. Nothing is cached: what an account cost on a day is re-derived from rows every time.

**Tech Stack:** Go 1.25 (no new dependencies — `crypto/sha256` and `encoding/hex` are stdlib), Postgres via pgx, `store/mem` + `store/pg` behind `store/storetest`, Next.js web app for the domain-content layers.

## Global Constraints

- **Blocked on `spec/effective-dated-terms`.** Tasks 4–8 of `docs/superpowers/plans/2026-07-30-effective-dated-terms.md` must be merged first. This plan assumes: `deposit.OverdraftTerms` exists as a row with `EffectiveFrom`/`TermsDayKey`, `deposit.Account` has lost `OverdraftLimit`/`Rate`/`UnarrangedRate`/`DayCount`/`TermsEffectiveFrom`, `overdraftAccrual(book ledger.Amount, t OverdraftTerms, from, to time.Time)` takes a terms row, `deposit.AccountWithTerms` exists, and `SetOverdraftTerms` takes a trailing `effectiveFrom time.Time` and returns `(OverdraftTerms, error)`.
- **`go test ./...` must pass with no database.** `TEST_DATABASE_URL=… go test ./...` (`make test-pg`) must pass too, and `store/pg` must never accept or refuse a write that `store/mem` does not. `store/storetest` is the enforcement.
- **One migration.** All DDL goes into `store/pg/schema/0001_init.sql`. No database is deployed, so there is no second migration file.
- **No `CHECK` constraints, no cross-table foreign keys.** Validation lives in the domain layer; the two stores must agree. Facts a schema dump cannot show go in `COMMENT ON COLUMN`.
- **Rates are millionths.** `interest.Rate` is an integer with `interest.RateScale`; never a float, anywhere, including over the wire.
- **Timelines are ascending by day key; listings are `ORDER BY created_at, seq`.** Never order by ID.
- **Effective dates are days.** `ledger.DayStart`-truncated by the caller before reaching a store; keyed through `product.VersionDayKey` / `deposit.TermsDayKey`.
- **Publication is forward-only.** `PublishVersion` refuses an `EffectiveFrom` before today. Retroactive repricing is a per-account overlay, never a catalogue write.
- **A `[[wiki-link]]` to a key absent from `web/src/components/hint-content.ts` takes every dev route down** and `next build` still passes. `npm run test` is the gate, and it scans quiz explanations too.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions, ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty tiers.

---

## File Structure

**New:**

| File | Responsibility |
| --- | --- |
| `product/doc.go` | Package doc: what a catalogue is, what floats and what does not, why the limit is absent from `OverdraftPricing` |
| `product/types.go` | `ID`, `Kind`, `OverdraftPricing`, `Product`, `Version`, `VersionDayKey`, hashing, `VersionAt` |
| `product/errors.go` | The package's sentinels |
| `product/store.go` | `Store`, `Tx`, the implementer contract notes |
| `product/catalogue.go` | The `Catalogue` register: create, draft, publish, retire, and the reads |
| `product/types_test.go` | Hash determinism, `VersionAt`, validation |
| `product/catalogue_test.go` | The register's refusals and the audit trail |
| `store/mem/tx_product.go` | The in-memory implementation |
| `store/pg/tx_product.go` | The Postgres implementation |
| `store/storetest/product.go` | `RunProduct`, the conformance suite |

**Modified:** `deposit/terms.go` (fields, `Resolve`, `EffectiveTerms`), `deposit/store.go` (embed `product.Tx`), `deposit/register.go` (open from a product, the split setters, the accrual merge), `deposit/errors.go`, `store/mem/mem.go`, `store/pg/pg.go`, `store/pg/schema/0001_init.sql`, `store/storetest/deposit.go`, `store/testenv/testenv.go` (a third store view), `ledger/audit.go` (scope + four events), `payment/store.go`, `payment/system.go`, `payment/participant.go`, `api/*`, `seed/seed.go`, `README.md`, `web/src/components/hint-content.ts`, `web/src/lib/quiz/chapters/08-*.ts`, `web/src/lib/quiz/chapters/18-*.ts`, `web/src/lib/types.ts`, `docs/cbs-vs-book.md`.

---

### Task 1: The `product` package core

Pure values, no store, no I/O. Everything here is a function of its arguments, which is what makes the catalogue testable without a database.

**Files:**
- Create: `product/doc.go`, `product/types.go`, `product/errors.go`
- Test: `product/types_test.go`

**Interfaces:**
- Consumes: `interest.Rate`, `interest.DayCount`, `ledger.DayStart`, `ledger.ValidateText`.
- Produces:
  - `type ID string`, `type Kind int`, `const CurrentAccount Kind = iota`, `func (k Kind) String() string`
  - `type OverdraftPricing struct { Rate, UnarrangedRate interest.Rate; DayCount interest.DayCount }` and `func (p OverdraftPricing) Validate() error`
  - `type Product struct { ID ID; Name string; Kind Kind; Retired bool; CreatedAt time.Time }` and `func (p Product) Validate() error`
  - `type Version struct { ProductID ID; EffectiveFrom time.Time; Overdraft OverdraftPricing; Hash string; PublishedAt time.Time; CreatedAt time.Time }`
  - `func (v Version) Published() bool`, `func (v Version) ComputeHash() string`, `func (v Version) VerifyHash() error`, `func (v Version) Validate() error`
  - `func VersionDayKey(day time.Time) string`
  - `func VersionAt(rows []Version, day time.Time) (Version, error)`
  - Sentinels: `ErrProductNotFound`, `ErrVersionNotFound`, `ErrProductRetired`, `ErrVersionPublished`, `ErrRetroactivePublish`, `ErrHashMismatch`, `ErrKindMismatch`, `ErrInvalidRate`

- [ ] **Step 1: Write the failing tests**

Create `product/types_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./product/...`
Expected: FAIL — the package does not exist (`no Go files in .../product`).

- [ ] **Step 3: Write `product/errors.go`**

```go
package product

import "errors"

// Sentinel errors returned by the Catalogue and by the value types. Callers use
// errors.Is, the same convention every other layer here follows.
var (
	// ErrProductNotFound is returned when a product ID matches no product in
	// the book.
	ErrProductNotFound = errors.New("product not found")

	// ErrVersionNotFound is returned when no PUBLISHED version is in force on
	// a day. A draft is not a candidate, so a product whose only version is a
	// draft has none in force on any day.
	ErrVersionNotFound = errors.New("no published product version in force on that day")

	// ErrProductRetired is returned when a retired product is used to open or
	// migrate an account. It never stops RESOLUTION: the accounts already sold
	// from a withdrawn product keep pricing against its last version for as
	// long as they live, and a bank that could not express that would have to
	// keep dead products on sale.
	ErrProductRetired = errors.New("product is retired")

	// ErrVersionPublished is returned when a published version is written to.
	// A published version is the configuration a past calculation used, so
	// editing one is editing history — which is the whole thing this package
	// exists to prevent.
	ErrVersionPublished = errors.New("product version is published and cannot be changed")

	// ErrRetroactivePublish is returned when a version would be published
	// effective before today.
	//
	// It would reprice every account bound to the product retroactively, moving
	// interest that has already been charged to customers, with the audit log as
	// the only control on it. Retroactivity stays where its blast radius is one
	// customer: the per-account pricing overlay. See the design doc.
	ErrRetroactivePublish = errors.New("a product version cannot be published effective in the past")

	// ErrHashMismatch is returned when a version's stored hash does not match
	// its content — a row edited behind the system's back, since no code path
	// can produce one. Resolution fails rather than pricing a day from it.
	ErrHashMismatch = errors.New("product version hash does not match its content")

	// ErrKindMismatch is returned for an unknown Kind, and when a product of
	// one kind is used where another is required — opening a current account
	// from a term-loan product, say.
	ErrKindMismatch = errors.New("wrong product kind for this operation")

	// ErrInvalidRate is returned for a negative rate, and for an unarranged
	// rate with no arranged one. It mirrors deposit.ErrInvalidRate, because the
	// two are the same rule about the same three numbers.
	ErrInvalidRate = errors.New("invalid product pricing")
)
```

- [ ] **Step 4: Write `product/types.go`**

```go
package product

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// ID identifies a product within a book. A defined type, not an alias, so the
// compiler prevents passing a deposit.AccountID where a product is expected.
type ID string

// Kind is what sort of instance a product may be bound to. It is on the product
// rather than on each version because a product does not change kind: a
// timeline that is a current account in March and a term loan in April is not a
// product, it is two.
type Kind int

const (
	// CurrentAccount prices a deposit account's arranged overdraft. It is the
	// only kind implemented; the credit kinds bind nothing yet, and a term loan
	// never will float — see the design doc.
	CurrentAccount Kind = iota
)

func (k Kind) String() string {
	switch k {
	case CurrentAccount:
		return "CurrentAccount"
	default:
		return "Unknown"
	}
}

// OverdraftPricing is the FLOATING parameter group: what a product charges on a
// drawn overdraft.
//
// There is no limit field here, and the absence is the design rather than an
// omission. A limit is an underwriting decision about one customer's
// creditworthiness; a rate is a price the bank publishes. Making the catalogue
// UNABLE to express a limit means "the limit does not float" is a fact the
// compiler checks, instead of a rule a later caller has to remember.
//
// The same three fields, as a pointer, are a deposit account's negotiated
// overlay. That is not duplication to be tidied away: an overlay is by
// definition "this customer's price instead of the product's", so it is the
// product's price type or it is a second type that must be kept in step.
type OverdraftPricing struct {
	// Rate is the annual rate on the drawn balance up to the account's limit.
	// Zero makes the whole overdraft interest-free, which is a real product.
	Rate interest.Rate
	// UnarrangedRate applies beyond the limit. It is an optional SURCHARGE, not
	// a switch: zero means Rate applies throughout, never that the excess is
	// free. See Validate for the one refused combination.
	UnarrangedRate interest.Rate
	// DayCount is a product parameter, not a code path chosen by product type.
	// It floats with the price because it is part of the price: 12% on ACT/365
	// and 12% on 30/360 are different products.
	DayCount interest.DayCount
}

// Validate reports whether this is a price. It is deposit.OverdraftTerms's rule
// about the same three numbers, so a catalogue version and an account overlay
// are held to one standard.
func (p OverdraftPricing) Validate() error {
	if p.Rate < 0 || p.UnarrangedRate < 0 {
		return ErrInvalidRate
	}
	if p.Rate == 0 && p.UnarrangedRate > 0 {
		return ErrInvalidRate
	}
	return nil
}

// Product is a catalogue entry: a named thing accounts are opened from.
//
// It is separate from its versions because a product needs a name before it has
// a price, and because listing the catalogue should not mean grouping a version
// table by product.
type Product struct {
	ID   ID
	Name string
	Kind Kind

	// Retired takes the product off sale: no new account may be opened from it
	// and no account may be migrated to it. It does not affect resolution — see
	// ErrProductRetired.
	Retired bool

	CreatedAt time.Time
}

// Validate checks the fields a store cannot.
func (p Product) Validate() error {
	if err := ledger.ValidateText("name", p.Name); err != nil {
		return err
	}
	if p.Kind.String() == "Unknown" {
		return fmt.Errorf("%w: kind %d", ErrKindMismatch, p.Kind)
	}
	return nil
}

// Version is what a product cost from one day onwards: a ROW, one per
// repricing, never changed once published.
//
// # Two dates, and what the pair means
//
// CreatedAt is when the version was drafted; EffectiveFrom is the first day it
// prices. PublishedAt is a third and it is a lifecycle fact rather than a date
// about money: a version with no PublishedAt is a draft, editable and invisible
// to resolution, which is what stops "immutable" from meaning "a typo in a rate
// is permanent" — and a rule worked around with a manual UPDATE is the thing
// being defended against.
//
// EffectiveFrom is day-granular, ledger.DayStart-truncated by the caller, on
// the same day axis accrual and deposit.OverdraftTerms already use.
type Version struct {
	ProductID     ID
	EffectiveFrom time.Time // day-truncated; the first day this version prices

	Overdraft OverdraftPricing

	// Hash is over the identity and the pricing, computed at publication and
	// VERIFIED at resolution. Storing a hash nothing reads would be theatre;
	// the verification is what makes it a control on the one hole a
	// domain-layer refusal cannot cover — a direct UPDATE to a published row.
	Hash string

	PublishedAt time.Time // zero == draft
	CreatedAt   time.Time
}

// Published reports whether this version prices anything. A draft never does.
func (v Version) Published() bool { return !v.PublishedAt.IsZero() }

// ComputeHash is the content hash: the identity and the pricing, and
// deliberately NOT PublishedAt or CreatedAt.
//
// Publication stamps a time onto a row whose content did not change, so a hash
// covering it would differ before and after publishing and could never be
// verified. The day KEY is hashed rather than the instant, for the same reason
// the row is keyed by day: two rows differing only within a day are the same
// row, and an upsert must not look like tampering.
//
// The "v1|" prefix is a format version. A future field changes the canonical
// form, and every stored hash would then fail to verify; the prefix is what
// lets that be a migration rather than a mystery.
func (v Version) ComputeHash() string {
	canonical := fmt.Sprintf("v1|%s|%s|%d|%d|%d",
		v.ProductID, VersionDayKey(v.EffectiveFrom),
		int64(v.Overdraft.Rate), int64(v.Overdraft.UnarrangedRate), int64(v.Overdraft.DayCount))
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyHash reports whether this row still holds the content it was published
// with. An unhashed row (a draft) verifies as a mismatch, which is correct:
// only a published version is ever verified, and a published one always has a
// hash.
func (v Version) VerifyHash() error {
	if v.Hash != v.ComputeHash() {
		return fmt.Errorf("%w: product %s effective %s",
			ErrHashMismatch, v.ProductID, VersionDayKey(v.EffectiveFrom))
	}
	return nil
}

// Validate checks the fields a store cannot.
func (v Version) Validate() error {
	if v.ProductID == "" {
		return ErrProductNotFound
	}
	return v.Overdraft.Validate()
}

// VersionDayKey is the day a version is identified by within its product.
//
// A version is identified by (product, day), so a second version drafted for
// the same effective day replaces the first — which makes "the version in force
// on day D" unique by construction rather than by a validation rule, and leaves
// the book's non-overlapping-interval exclusion constraint with nothing to
// enforce. It is deposit.TermsDayKey's twin, and store implementations must
// derive their key with it: mem uses it as a map key, pg as the value of
// product_versions.day_key and as the column the as-of lookup compares on.
func VersionDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// VersionAt is the published version in force on a day: the last one whose
// EffectiveFrom is not after it, with its hash verified.
//
// rows must be ascending by EffectiveFrom, which is what
// ListProductVersions returns.
//
// Drafts are skipped rather than treated as boundaries. That distinction
// matters: an operator drafting next quarter's price must not change what today
// costs, so the row BEFORE a draft stays in force through it.
//
// It returns an error rather than a bool because the failures are now several
// and an operator needs to know which — a day before the product had any price
// is not the same event as a row that was edited in the database.
func VersionAt(rows []Version, day time.Time) (Version, error) {
	d := ledger.DayStart(day)
	// The first index effective strictly after d; the candidates are before it.
	i := sort.Search(len(rows), func(i int) bool {
		return rows[i].EffectiveFrom.After(d)
	})
	for ; i > 0; i-- {
		v := rows[i-1]
		if !v.Published() {
			continue
		}
		if err := v.VerifyHash(); err != nil {
			return Version{}, err
		}
		return v, nil
	}
	return Version{}, ErrVersionNotFound
}
```

- [ ] **Step 5: Write `product/doc.go`**

```go
// Package product is the catalogue: named products, each with an
// effective-dated timeline of immutable published versions, that instances are
// opened from and priced by.
//
// # What floats and what does not
//
// A product carries the parameters the BANK sets: the arranged rate, the
// unarranged surcharge, the day-count convention. It cannot carry an overdraft
// limit, because a limit is an underwriting decision about one customer — see
// OverdraftPricing, where the absence is enforced by the type rather than by a
// rule.
//
// An instance therefore has three sources for its terms on a day, and
// deposit.Resolve merges them in this order: the account's own row supplies the
// limit, always; the account's row supplies the pricing if it carries a
// negotiated overlay; otherwise the product version in force on that day does.
// Nothing is cached. What an account cost on a day is re-derived from rows,
// which is the property the whole design exists for.
//
// # Why this package imports nothing of the domain layers
//
// It imports interest and ledger and nothing else, so deposit may import it —
// and so lending may too, later, without the import cycle that a catalogue
// living inside deposit would create the moment the credit side needed one.
//
// # What is deliberately absent
//
// No content-addressed identity beyond the tamper hash, no pinned-vs-floating
// binding beyond the two groups above, no parameter overlays other than the
// per-account pricing one, no resolution log, and no maker-checker. Publication
// is forward-only, which is this package's answer to the control problem a
// retroactive reprice creates. docs/superpowers/specs/2026-07-30-product-catalogue-design.md
// argues each of those, and cbs-vs-book.md item 4.3 is the comparison they come
// from.
package product
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./product/...`
Expected: PASS (all of `TestHashCoversPricingAndNotLifecycleTimes`, `TestHashIsDayGranular`, `TestVersionAtPicksTheLastPublishedRowNotAfterTheDay`, `TestVersionAtIgnoresDrafts`, `TestVersionAtRefusesATamperedRow`, `TestPricingValidate`, `TestProductValidate`).

- [ ] **Step 7: Check the whole tree still builds**

Run: `go build ./... && go vet ./product/...`
Expected: no output.

- [ ] **Step 8: Commit**

```bash
git add product/
git commit -m "feat(product): add the catalogue value types and their resolver

A product is a named catalogue entry; a version is what it cost from one day
onwards, keyed by (product, day) so the row in force is unique by construction.
OverdraftPricing holds the floating group and cannot hold a limit, which makes
'the limit does not float' a fact the compiler checks. VersionAt skips drafts
and verifies the content hash, so a row edited in the database stops a read
rather than pricing a day.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Product storage — interface, both stores, conformance

**Files:**
- Create: `product/store.go`, `store/mem/tx_product.go`, `store/pg/tx_product.go`, `store/storetest/product.go`
- Modify: `store/mem/mem.go`, `store/pg/pg.go`, `store/pg/schema/0001_init.sql`, `store/mem/mem_test.go`, `store/pg/pg_test.go`

**Interfaces:**
- Consumes: Task 1's types; `ledger.Tx`, `ledger.BookID`.
- Produces:
  - `product.Store` with `Update`/`View`/`Reset`/`Close` over `product.Tx`
  - `product.Tx` embedding `ledger.Tx` plus `PutProduct`, `GetProduct`, `ListProducts`, `PutProductVersion`, `ListProductVersions`, `GetProductVersionAsOf`
  - `func (s *mem.Store) Product() product.Store`, `func (s *pg.Store) Product() product.Store`
  - `func storetest.RunProduct(t *testing.T, newStore func(*testing.T) product.Store)`

- [ ] **Step 1: Write `product/store.go`**

```go
package product

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Store owns the catalogue's persistent state. Declared here, by the consumer,
// and implemented by store/mem and store/pg — so the store packages import the
// domain packages and never the reverse.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds ledger.Tx so that one concrete transaction spans the catalogue and
// the ledger — and, because deposit.Tx in turn embeds this one, so that opening
// an account can validate its product and write its first terms row in a single
// unit of work.
//
// Products are book-scoped like every other entity here: the same ID in two
// books is two products, because a catalogue belongs to a bank.
type Tx interface {
	ledger.Tx

	PutProduct(ctx context.Context, book ledger.BookID, p Product) error
	GetProduct(ctx context.Context, book ledger.BookID, id ID) (Product, error)
	ListProducts(ctx context.Context, book ledger.BookID) ([]Product, error)

	// The product's version timeline. Two readers rather than one, because the
	// two callers want different things: the accrual walk wants the whole
	// timeline in one read and resolves per day in Go, while an operator view
	// wants the single version in force now and should not pay for history.
	// It is the same split deposit's terms methods make, for the same reason.
	PutProductVersion(ctx context.Context, book ledger.BookID, v Version) error
	ListProductVersions(ctx context.Context, book ledger.BookID, id ID) ([]Version, error)
	GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id ID, day time.Time) (Version, error)
}

// Contract notes for implementers — storetest.RunProduct pins all of these:
//
//   - PutProduct is an upsert keyed by (book, ID). PutProductVersion is an
//     upsert keyed by (book, ProductID, VersionDayKey(v.EffectiveFrom)): two
//     versions drafted for the same product and the same effective day are the
//     same row, and the later one wins.
//   - A store NEVER refuses a write on PublishedAt grounds. Freezing a
//     published version is the Catalogue's job, in the domain layer, for the
//     dual-store reason every other validation lives there — a store that
//     refused would accept a different set of writes from the other one.
//   - GetProduct returns ErrProductNotFound and GetProductVersionAsOf returns
//     ErrVersionNotFound when the row is missing. Wrapping is fine; errors.Is
//     is what the suite checks.
//   - GetProductVersionAsOf considers PUBLISHED rows only, and returns the
//     greatest effective day not after `day`. Unlike ActiveHoldTotal it is not
//     an aggregate: a product with no published version has no price, and
//     reporting a zero row would read as a real interest-free product.
//   - Neither reader verifies the hash. VersionAt does, in the domain, so that
//     one rule lives in one place and a store cannot disagree with it.
//   - ListProducts orders by CreatedAt ascending, ties broken by the row's
//     monotonic insertion sequence — ORDER BY created_at, seq, the rule every
//     listing here follows. Never break ties on the ID.
//   - ListProductVersions orders by effective day ASCENDING and includes
//     drafts. Ascending is not a convenience: VersionAt binary-searches the
//     slice it is handed. Drafts are included because the domain decides what a
//     draft means, and because an operator view needs to see one.
//   - The store truncates nothing. Callers pass an already-DayStart-ed instant
//     and both stores key on VersionDayKey of it.
//   - Writes roll back with the surrounding Update, catalogue rows and ledger
//     rows together — that is what Tx embedding ledger.Tx exists for.
```

- [ ] **Step 2: Write the conformance suite**

Create `store/storetest/product.go`. Read `store/storetest/deposit.go`'s head first for the local helpers (`bookA`, `bookB`, `assertEqual`, `day`, `early`) and follow them exactly:

```go
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// RunProduct runs the catalogue conformance suite against a store. Every
// product.Store implementation must pass it identically.
//
// It talks only to product.Store and product.Tx — never to product.Catalogue —
// so what it pins is the storage contract: book scoping, not-found sentinels,
// listing order, upsert identity, and the two things a store could get subtly
// wrong without any other test noticing, which are that the as-of lookup skips
// drafts and that a returned row's pricing is a copy.
func RunProduct(t *testing.T, newStore func(*testing.T) product.Store) {
	t.Helper()

	// Helpers local to this suite.
	version := func(id product.ID, from time.Time, rate interest.Rate, publishedAt time.Time) product.Version {
		v := product.Version{
			ProductID: id, EffectiveFrom: from,
			Overdraft:   product.OverdraftPricing{Rate: rate, DayCount: interest.ACT365},
			PublishedAt: publishedAt, CreatedAt: early,
		}
		v.Hash = v.ComputeHash()
		return v
	}

	t.Run("ProductRoundTrip", func(t *testing.T) {
		s := openProduct(t, newStore)

		want := product.Product{
			ID: "prd_1", Name: "Basic Current Account",
			Kind: product.CurrentAccount, Retired: false, CreatedAt: early,
		}
		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			return tx.PutProduct(ctx, bookA, want)
		})
		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			got, err := tx.GetProduct(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "product", got, want)
			return nil
		})

		// Retiring is an upsert, and it must not move the row in its listing:
		// seq is assigned on insert and left alone by the update branch.
		retired := want
		retired.Retired = true
		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if err := tx.PutProduct(ctx, bookA, product.Product{
				ID: "prd_2", Name: "Premium", Kind: product.CurrentAccount, CreatedAt: early,
			}); err != nil {
				return err
			}
			return tx.PutProduct(ctx, bookA, retired)
		})
		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			list, err := tx.ListProducts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "products", len(list), 2)
			assertEqual(t, "first is still prd_1", string(list[0].ID), "prd_1")
			assertEqual(t, "prd_1 is retired", list[0].Retired, true)
			return nil
		})
	})

	t.Run("GetOnMissingRowsReturnsSentinels", func(t *testing.T) {
		s := openProduct(t, newStore)

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if _, err := tx.GetProduct(ctx, bookA, "prd_nope"); !errors.Is(err, product.ErrProductNotFound) {
				t.Errorf("GetProduct: %v, want ErrProductNotFound", err)
			}
			if _, err := tx.GetProductVersionAsOf(ctx, bookA, "prd_nope", day(1)); !errors.Is(err, product.ErrVersionNotFound) {
				t.Errorf("GetProductVersionAsOf: %v, want ErrVersionNotFound", err)
			}
			// A listing of nothing is empty, not an error: it is a listing, and
			// ListProducts of an empty book is the same shape.
			list, err := tx.ListProductVersions(ctx, bookA, "prd_nope")
			if err != nil {
				return err
			}
			assertEqual(t, "versions of an unknown product", len(list), 0)
			return nil
		})
	})

	t.Run("VersionTimelineIsAscendingAndUpsertsByDay", func(t *testing.T) {
		s := openProduct(t, newStore)

		jan, mar, jun := day(1), day(60), day(150)

		// Written out of order on purpose: the ORDER BY is the contract, not
		// the insertion order.
		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			for _, v := range []product.Version{
				version("prd_1", jun, 300_000, jun),
				version("prd_1", jan, 100_000, jan),
				version("prd_1", mar, 200_000, mar),
			} {
				if err := tx.PutProductVersion(ctx, bookA, v); err != nil {
					return err
				}
			}
			// Same product ID, other book: two products, and neither read
			// below may see this.
			return tx.PutProductVersion(ctx, bookB, version("prd_1", jan, 999_000, jan))
		})

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "versions", len(rows), 3)
			assertEqual(t, "first", int64(rows[0].Overdraft.Rate), int64(100_000))
			assertEqual(t, "second", int64(rows[1].Overdraft.Rate), int64(200_000))
			assertEqual(t, "third", int64(rows[2].Overdraft.Rate), int64(300_000))

			other, err := tx.ListProductVersions(ctx, bookB, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "book B is its own product", len(other), 1)
			assertEqual(t, "book B rate", int64(other[0].Overdraft.Rate), int64(999_000))

			as, err := tx.GetProductVersionAsOf(ctx, bookA, "prd_1", day(59))
			if err != nil {
				return err
			}
			assertEqual(t, "the day before a repricing", int64(as.Overdraft.Rate), int64(100_000))

			as, err = tx.GetProductVersionAsOf(ctx, bookA, "prd_1", mar)
			if err != nil {
				return err
			}
			assertEqual(t, "on the boundary", int64(as.Overdraft.Rate), int64(200_000))

			if _, err := tx.GetProductVersionAsOf(ctx, bookA, "prd_1", day(0)); !errors.Is(err, product.ErrVersionNotFound) {
				t.Errorf("before the first version: %v, want ErrVersionNotFound", err)
			}
			return nil
		})

		// A second version for the same effective DAY replaces the first.
		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			return tx.PutProductVersion(ctx, bookA, version("prd_1", mar.Add(11*time.Hour), 250_000, mar))
		})
		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "still three versions", len(rows), 3)
			assertEqual(t, "the day's version was replaced", int64(rows[1].Overdraft.Rate), int64(250_000))
			return nil
		})
	})

	// A store that returned drafts from the as-of lookup would price accounts
	// from a version nobody published, and every other subtest here would still
	// pass. It is the one behaviour in this suite worth its own case.
	t.Run("AsOfSkipsDraftsAndListIncludesThem", func(t *testing.T) {
		s := openProduct(t, newStore)

		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if err := tx.PutProductVersion(ctx, bookA, version("prd_1", day(1), 100_000, day(1))); err != nil {
				return err
			}
			// Drafted, never published: PublishedAt is the zero time.
			return tx.PutProductVersion(ctx, bookA, version("prd_1", day(30), 900_000, time.Time{}))
		})

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the draft is listed", len(rows), 2)
			assertEqual(t, "and is recognisable as one", rows[1].Published(), false)

			as, err := tx.GetProductVersionAsOf(ctx, bookA, "prd_1", day(45))
			if err != nil {
				return err
			}
			assertEqual(t, "the published row before the draft stays in force",
				int64(as.Overdraft.Rate), int64(100_000))
			return nil
		})
	})

	// store/mem holds Go values in maps, so a reader could be handed the very
	// row a later writer mutates. store/pg cannot do that at all, which is
	// exactly why the suite has to say the two behave the same.
	t.Run("ReadRowsAreCopies", func(t *testing.T) {
		s := openProduct(t, newStore)

		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			return tx.PutProductVersion(ctx, bookA, version("prd_1", day(1), 100_000, day(1)))
		})

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			rows[0].Overdraft.Rate = 42
			return nil
		})
		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the stored rate", int64(rows[0].Overdraft.Rate), int64(100_000))
			return nil
		})
	})

	t.Run("WritesRollBackWithTheLedgersOwn", func(t *testing.T) {
		s := openProduct(t, newStore)
		boom := errors.New("boom")

		err := s.Update(context.Background(), func(ctx context.Context, tx product.Tx) error {
			if err := tx.PutProduct(ctx, bookA, product.Product{
				ID: "prd_1", Name: "Basic", CreatedAt: early,
			}); err != nil {
				return err
			}
			if err := tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "200.cust.001", SubledgerID: "cust", Name: "Anna",
				Type: ledger.Liability, Asset: "EUR",
			}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("Update: %v, want boom", err)
		}

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if _, err := tx.GetProduct(ctx, bookA, "prd_1"); !errors.Is(err, product.ErrProductNotFound) {
				t.Errorf("product survived the rollback: %v", err)
			}
			if _, err := tx.GetAccount(ctx, bookA, "200.cust.001"); err == nil {
				t.Error("GL account survived the rollback")
			}
			return nil
		})
	})

	t.Run("ResetClearsCatalogueState", func(t *testing.T) {
		s := openProduct(t, newStore)

		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if err := tx.PutProduct(ctx, bookA, product.Product{
				ID: "prd_1", Name: "Basic", CreatedAt: early,
			}); err != nil {
				return err
			}
			return tx.PutProductVersion(ctx, bookA, version("prd_1", day(1), 100_000, day(1)))
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			list, err := tx.ListProducts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "products after reset", len(list), 0)
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "versions after reset", len(rows), 0)
			return nil
		})
	})
}

// openProduct, updateProduct and viewProduct mirror openDeposit/updateDeposit/
// viewDeposit in deposit.go: one store per subtest, closed when it ends, and a
// t.Fatalf on any error the subtest did not expect.
func openProduct(t *testing.T, newStore func(*testing.T) product.Store) product.Store {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func updateProduct(t *testing.T, s product.Store, fn func(context.Context, product.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func viewProduct(t *testing.T, s product.Store, fn func(context.Context, product.Tx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}
```

- [ ] **Step 3: Run the suite to verify it fails**

Add the entry points first — `store/mem/mem_test.go`, beside `TestDepositConformance`:

```go
// TestProductConformance runs the catalogue half of the suite against the same
// implementation, through the product.Store view of the store.
func TestProductConformance(t *testing.T) {
	storetest.RunProduct(t, func(t *testing.T) product.Store {
		return mem.New(func() time.Time { return time.Unix(0, 0).UTC() }).Product()
	})
}
```

and in `store/pg/pg_test.go`'s `TestConformance`, after the deposit line:

```go
	storetest.RunProduct(t, func(t *testing.T) product.Store { return newStore(t).Product() })
```

Run: `go build ./... && go test ./store/...`
Expected: FAIL to compile — `mem.Store` has no field or method `Product`.

- [ ] **Step 4: Add the in-memory state**

In `store/mem/mem.go`, beside `overdraftTerms` in the state struct:

```go
	// The catalogue's state. products is keyed by ID; productVersions is keyed
	// by (product, effective day) rather than nested per product, so an upsert
	// is a single map assignment — the same shape overdraftTerms has, for the
	// same reason.
	products        map[ledger.BookID]map[product.ID]product.Product
	productVersions map[ledger.BookID]map[versionKey]product.Version
```

Beside `termsKey`:

```go
// versionKey identifies one product version within a book: the product and the
// effective day, which is the composite primary key store/pg holds.
type versionKey struct {
	productID product.ID
	dayKey    string
}
```

Beside `kindOverdraftTerms`:

```go
	kindProduct        rowKind = "products"
	kindProductVersion rowKind = "product_versions"
```

In the state constructor beside `overdraftTerms:`:

```go
		products:        make(map[ledger.BookID]map[product.ID]product.Product),
		productVersions: make(map[ledger.BookID]map[versionKey]product.Version),
```

and in the clone beside it:

```go
		products:        cloneNested(s.products),
		productVersions: cloneNested(s.productVersions),
```

Then the store view, beside `Deposit()`:

```go
// Product returns this store as a product.Store.
func (s *Store) Product() product.Store { return productStore{s} }

// productStore re-types Store's Update and View; Reset and Close are promoted
// unchanged from the embedded *Store.
type productStore struct{ *Store }

// compile-time check that the adapter satisfies the interface it exists for.
var _ product.Store = productStore{}

func (p productStore) Update(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return p.Store.Update(ctx, func(ctx context.Context, t ledger.Tx) error {
		return fn(ctx, t.(product.Tx))
	})
}

func (p productStore) View(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return p.Store.View(ctx, func(ctx context.Context, t ledger.Tx) error {
		return fn(ctx, t.(product.Tx))
	})
}
```

- [ ] **Step 5: Write `store/mem/tx_product.go`**

```go
package mem

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// ---------------------------------------------------------------------------
// The product catalogue
// ---------------------------------------------------------------------------

func (t *tx) PutProduct(ctx context.Context, book ledger.BookID, p product.Product) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindProduct, string(p.ID))
	bucket(t.state.products, book)[p.ID] = p
	return nil
}

func (t *tx) GetProduct(ctx context.Context, book ledger.BookID, id product.ID) (product.Product, error) {
	p, ok := t.state.products[book][id]
	if !ok {
		return product.Product{}, product.ErrProductNotFound
	}
	return p, nil
}

// ListProducts orders by CreatedAt then insertion sequence, the rule every
// listing here follows.
func (t *tx) ListProducts(ctx context.Context, book ledger.BookID) ([]product.Product, error) {
	out := make([]product.Product, 0, len(t.state.products[book]))
	for _, p := range t.state.products[book] {
		out = append(out, p)
	}
	sortRows(t.state, out, book, kindProduct, func(p product.Product) (time.Time, string) {
		return p.CreatedAt, string(p.ID)
	})
	return out, nil
}

// PutProductVersion upserts under (product, effective day): a second version
// drafted for the same day replaces the first, which is what makes "the version
// in force on day D" unique by construction rather than by a validation rule.
//
// It does NOT refuse a write to a published row. Freezing a published version
// is product.Catalogue's job: a store that refused would accept a different set
// of writes from store/pg, which is the one divergence this package must never
// introduce.
func (t *tx) PutProductVersion(ctx context.Context, book ledger.BookID, v product.Version) error {
	if err := t.write(); err != nil {
		return err
	}
	key := versionKey{productID: v.ProductID, dayKey: product.VersionDayKey(v.EffectiveFrom)}
	t.state.insertSeq(book, kindProductVersion, versionSeqID(key))
	bucket(t.state.productVersions, book)[key] = v
	return nil
}

// ListProductVersions returns a product's whole timeline, drafts included,
// ascending by effective day. Ascending is load-bearing: product.VersionAt
// binary-searches the slice this returns.
func (t *tx) ListProductVersions(ctx context.Context, book ledger.BookID, id product.ID) ([]product.Version, error) {
	out := make([]product.Version, 0)
	for key, v := range t.state.productVersions[book] {
		if key.productID == id {
			out = append(out, v)
		}
	}
	// The effective day is already a total order within a product, so the
	// insertion sequence is the tie-break only for form.
	sortRows(t.state, out, book, kindProductVersion, func(v product.Version) (time.Time, string) {
		return v.EffectiveFrom, versionSeqID(versionKey{
			productID: v.ProductID, dayKey: product.VersionDayKey(v.EffectiveFrom),
		})
	})
	return out, nil
}

// GetProductVersionAsOf is the PUBLISHED version in force on a day: the
// greatest effective day not after it, skipping drafts.
//
// It scans rather than binary-searching, because the map is unordered and
// building the ordered slice to search would cost more than the scan. A product
// with no published version before the day has no price, which is
// ErrVersionNotFound rather than a zero row that would read as a real
// interest-free product.
//
// It does not verify the hash. product.VersionAt does, in the domain, so that
// one rule lives in one place.
func (t *tx) GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id product.ID, day time.Time) (product.Version, error) {
	want := product.VersionDayKey(day)
	var (
		best  product.Version
		found bool
	)
	for key, v := range t.state.productVersions[book] {
		if key.productID != id || key.dayKey > want || !v.Published() {
			continue
		}
		if !found || key.dayKey > product.VersionDayKey(best.EffectiveFrom) {
			best, found = v, true
		}
	}
	if !found {
		return product.Version{}, product.ErrVersionNotFound
	}
	return best, nil
}

// versionSeqID renders a version's composite key as the string sortRows and
// insertSeq identify rows by.
func versionSeqID(k versionKey) string { return string(k.productID) + "/" + k.dayKey }
```

- [ ] **Step 6: Run the mem half of the suite**

Run: `go test ./store/mem/...`
Expected: PASS. If `ReadRowsAreCopies` fails, the state clone is not reaching the new maps — check the `cloneNested` lines from Step 4.

- [ ] **Step 7: Add the Postgres tables**

In `store/pg/schema/0001_init.sql`, immediately after the `overdraft_terms` block and its index (currently ending at the `overdraft_terms_account_idx` line) and before the `-- The lending layer` banner:

```sql
-- ---------------------------------------------------------------------------
-- The product catalogue
-- ---------------------------------------------------------------------------

-- A catalogue entry: the named product an account is opened FROM. Separate from
-- its versions because a product needs a name before it has a price, and
-- because listing the catalogue should not mean grouping a version table.
--
-- product_versions.product_id carries no foreign key, for the reason
-- subledgers.ledger_id does not: "the parent must exist" is a domain rule, and
-- product.Catalogue enforces it. A constraint here would make store/pg reject
-- writes store/mem accepts.
CREATE TABLE products (
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id         TEXT NOT NULL,
    name       TEXT NOT NULL,
    kind       SMALLINT NOT NULL,
    retired    BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ,
    seq        BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

-- What a product cost from one day onwards: one row per repricing, never
-- changed once published.
--
-- The primary key is (book_id, product_id, day_key), which is where the book's
-- non-overlapping-interval exclusion constraint went. Keying by DAY makes "the
-- version in force on D" unique by construction, so there is no interval to
-- exclude — and unlike a tstzrange with a GiST exclusion constraint, store/mem
-- can implement the same rule with a map key. It is the discipline
-- overdraft_terms and snapshots already follow.
CREATE TABLE product_versions (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    product_id      TEXT NOT NULL,
    day_key         TEXT NOT NULL,
    effective_from  TIMESTAMPTZ,
    rate            BIGINT NOT NULL,
    unarranged_rate BIGINT NOT NULL,
    day_count       SMALLINT NOT NULL,
    hash            TEXT NOT NULL,
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, product_id, day_key)
);

COMMENT ON COLUMN product_versions.published_at IS
    'NULL means DRAFT: the row is editable and is invisible to resolution, so the published version before it stays in force through it. Non-NULL freezes the row — product.Catalogue refuses a write to it, and the refusal is in the domain layer rather than a CHECK because store/mem must refuse exactly the same writes.';

COMMENT ON COLUMN product_versions.hash IS
    'sha256 over the identity and the pricing (see product.Version.ComputeHash), computed at publication and VERIFIED on every read that prices a day. It is the only control on the one hole a domain-layer refusal cannot cover: a direct UPDATE to a published row. A mismatch fails the accrual rather than pricing a day from a row nobody published.';

-- Index 4: the timeline read the accrual makes once per product per run, and
-- the bounded as-of lookup an operator view makes. Both filter on
-- (book_id, product_id) and order by day_key, so one index serves both. Not
-- redundant with the primary key for the reason Index 3 is not.
CREATE INDEX product_versions_product_idx ON product_versions (book_id, product_id, day_key);
```

Renumber the index comments that follow, so the numbering still reads in file order: `-- Index 4:` (facility terms) becomes `-- Index 5:`, `-- Index 5:` becomes `-- Index 6:`, `-- Index 6:` becomes `-- Index 7:`, and `-- Indexes 7 and 8:` becomes `-- Indexes 8 and 9:`.

Verify: `grep -n "^-- Index" store/pg/schema/0001_init.sql` — the numbers must read 2, 3, 4, 5, 6, 7, "8 and 9" in file order.

Then add both tables to the truncate list in `store/pg/pg.go`'s `tables` var, after `"overdraft_terms"`:

```go
	"products", "product_versions",
```

- [ ] **Step 8: Write `store/pg/tx_product.go`**

```go
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// ---------------------------------------------------------------------------
// The product catalogue
// ---------------------------------------------------------------------------

func (t *tx) PutProduct(ctx context.Context, book ledger.BookID, p product.Product) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO products (book_id, id, name, kind, retired, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (book_id, id) DO UPDATE SET
			name       = EXCLUDED.name,
			kind       = EXCLUDED.kind,
			retired    = EXCLUDED.retired,
			created_at = EXCLUDED.created_at`,
		string(book), string(p.ID), p.Name, int16(p.Kind), p.Retired, nullTime(p.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put product %s: %w", p.ID, err)
	}
	return nil
}

const productColumns = `id, name, kind, retired, created_at`

func scanProduct(row interface{ Scan(...any) error }) (product.Product, error) {
	var (
		out     product.Product
		kind    int16
		created *time.Time
	)
	if err := row.Scan(&out.ID, &out.Name, &kind, &out.Retired, &created); err != nil {
		return product.Product{}, err
	}
	out.Kind = product.Kind(kind)
	out.CreatedAt = readTime(created)
	return out, nil
}

func (t *tx) GetProduct(ctx context.Context, book ledger.BookID, id product.ID) (product.Product, error) {
	row := t.tx.QueryRow(ctx, `
		SELECT `+productColumns+` FROM products WHERE book_id = $1 AND id = $2`,
		string(book), string(id))
	out, err := scanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return product.Product{}, product.ErrProductNotFound
	}
	if err != nil {
		return product.Product{}, fmt.Errorf("pg: get product %s: %w", id, err)
	}
	return out, nil
}

func (t *tx) ListProducts(ctx context.Context, book ledger.BookID) ([]product.Product, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT `+productColumns+` FROM products WHERE book_id = $1
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("pg: list products: %w", err)
	}
	defer rows.Close()

	out := make([]product.Product, 0)
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list products: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PutProductVersion upserts under (product, effective day). The day key is
// derived with product.VersionDayKey — the same function store/mem keys its map
// with, and the same one GetProductVersionAsOf compares against.
//
// It does not refuse a write to a published row; see store/mem's twin for why.
func (t *tx) PutProductVersion(ctx context.Context, book ledger.BookID, v product.Version) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO product_versions (
			book_id, product_id, day_key, effective_from,
			rate, unarranged_rate, day_count, hash, published_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (book_id, product_id, day_key) DO UPDATE SET
			effective_from  = EXCLUDED.effective_from,
			rate            = EXCLUDED.rate,
			unarranged_rate = EXCLUDED.unarranged_rate,
			day_count       = EXCLUDED.day_count,
			hash            = EXCLUDED.hash,
			published_at    = EXCLUDED.published_at,
			created_at      = EXCLUDED.created_at`,
		string(book), string(v.ProductID), product.VersionDayKey(v.EffectiveFrom),
		nullTime(v.EffectiveFrom),
		int64(v.Overdraft.Rate), int64(v.Overdraft.UnarrangedRate), int16(v.Overdraft.DayCount),
		v.Hash, nullTime(v.PublishedAt), nullTime(v.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put product version %s/%s: %w",
			v.ProductID, product.VersionDayKey(v.EffectiveFrom), err)
	}
	return nil
}

// productVersionColumns is the select list both readers use, for the reason
// overdraftTermsColumns exists: it stops the two from scanning different column
// sets, which is a whole class of "it round-trips one way".
const productVersionColumns = `
	product_id, effective_from, rate, unarranged_rate, day_count,
	hash, published_at, created_at`

func scanProductVersion(row interface{ Scan(...any) error }) (product.Version, error) {
	var (
		out                          product.Version
		rate, unarranged             int64
		dayCount                     int16
		effective, publishedAt, created *time.Time
	)
	if err := row.Scan(&out.ProductID, &effective, &rate, &unarranged, &dayCount,
		&out.Hash, &publishedAt, &created); err != nil {
		return product.Version{}, err
	}
	out.Overdraft = product.OverdraftPricing{
		Rate:           interest.Rate(rate),
		UnarrangedRate: interest.Rate(unarranged),
		DayCount:       interest.DayCount(dayCount),
	}
	out.EffectiveFrom = readTime(effective)
	out.PublishedAt = readTime(publishedAt)
	out.CreatedAt = readTime(created)
	return out, nil
}

// ListProductVersions returns the whole timeline, drafts included, ascending by
// day_key — an ISO day and therefore lexicographically ordered. Ascending is
// load-bearing: product.VersionAt binary-searches the slice this returns.
func (t *tx) ListProductVersions(ctx context.Context, book ledger.BookID, id product.ID) ([]product.Version, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT `+productVersionColumns+`
		FROM product_versions WHERE book_id = $1 AND product_id = $2
		ORDER BY day_key ASC, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("pg: list product versions: %w", err)
	}
	defer rows.Close()

	out := make([]product.Version, 0)
	for rows.Next() {
		v, err := scanProductVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list product versions: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetProductVersionAsOf is the published version in force on a day. It compares
// day_key rather than effective_from so that the bound is a DAY on both sides,
// and filters on published_at so a draft is invisible — the row before it stays
// in force through it.
func (t *tx) GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id product.ID, day time.Time) (product.Version, error) {
	row := t.tx.QueryRow(ctx, `
		SELECT `+productVersionColumns+`
		FROM product_versions
		WHERE book_id = $1 AND product_id = $2 AND day_key <= $3 AND published_at IS NOT NULL
		ORDER BY day_key DESC
		LIMIT 1`, string(book), string(id), product.VersionDayKey(day))
	out, err := scanProductVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return product.Version{}, product.ErrVersionNotFound
	}
	if err != nil {
		return product.Version{}, fmt.Errorf("pg: product version for %s as of %s: %w",
			id, product.VersionDayKey(day), err)
	}
	return out, nil
}
```

Then the store view in `store/pg/pg.go`, beside `Deposit()` — same shape as `store/mem`'s in Step 4 (`productStore struct{ *Store }`, the `var _ product.Store` check, and the two re-typed methods).

- [ ] **Step 9: Run both halves**

Run: `go test ./... && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./store/pg/...`
Expected: PASS. With no `TEST_DATABASE_URL` the pg suite skips, which is the load-bearing property — say so if the run reports skips.

- [ ] **Step 10: Commit**

```bash
git add product/store.go store/mem store/pg store/storetest
git commit -m "feat(store): persist the product catalogue in both stores

Two tables, keyed (book, product, day) so the version in force on a day is
unique by construction rather than by an exclusion constraint store/mem could
not implement. The as-of reader skips drafts; neither reader verifies the hash,
because product.VersionAt does that in the domain. RunProduct pins all of it,
including that a store never refuses a write to a published row — freezing one
is the Catalogue's job, so that both stores accept exactly the same writes.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: The `Catalogue` register

**Files:**
- Create: `product/catalogue.go`, `product/catalogue_test.go`
- Modify: `ledger/audit.go`

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces:
  - `func NewCatalogue(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time) *Catalogue`
  - `func (c *Catalogue) CreateProduct(ctx context.Context, name string, kind Kind) (Product, error)`
  - `func (c *Catalogue) DraftVersion(ctx context.Context, id ID, effectiveFrom time.Time, pricing OverdraftPricing) (Version, error)`
  - `func (c *Catalogue) PublishVersion(ctx context.Context, id ID, effectiveFrom time.Time) (Version, error)`
  - `func (c *Catalogue) RetireProduct(ctx context.Context, id ID) (Product, error)`
  - `func (c *Catalogue) GetProduct(ctx context.Context, id ID) (Product, error)`
  - `func (c *Catalogue) ListProducts(ctx context.Context) ([]Product, error)`
  - `func (c *Catalogue) Versions(ctx context.Context, id ID) ([]Version, error)`
  - `func (c *Catalogue) VersionInForce(ctx context.Context, id ID, day time.Time) (Version, error)`
  - `ledger.ScopeProduct`, `ledger.EventProductCreated`, `ledger.EventProductRetired`, `ledger.EventProductVersionDrafted`, `ledger.EventProductVersionPublished`

- [ ] **Step 1: Add the audit scope and events**

In `ledger/audit.go`, add to the `Scope` block:

```go
	ScopeProduct Scope = "product"
```

and a new group in the event constants, after the ScopeDeposit group:

```go
	// ScopeProduct. A published price is exactly the kind of fact an auditor
	// asks who entered and when, so every catalogue write is logged — including
	// the draft, because the gap between drafting and publishing is where a
	// four-eyes control would sit if this system had one.
	EventProductCreated           = "product.created"
	EventProductRetired           = "product.retired"
	EventProductVersionDrafted    = "product.version_drafted"
	EventProductVersionPublished  = "product.version_published"
```

- [ ] **Step 2: Write the failing tests**

Create `product/catalogue_test.go`:

```go
package product_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/mem"
)

// mutableClock lets a test move time forward, which the forward-only
// publication rule makes necessary: "before today" is a question about now.
type mutableClock struct{ at time.Time }

func (c *mutableClock) now() time.Time  { return c.at }
func (c *mutableClock) set(t time.Time) { c.at = t }

func newTestCatalogue(t *testing.T, clock func() time.Time) *Catalogue {
	t.Helper()
	s := mem.New(clock)
	t.Cleanup(func() { _ = s.Close() })
	book := ledger.NewBook(s, "bank", clock)
	return NewCatalogue(s.Product(), book, "bank", clock)
}

func TestCreateDraftPublish(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: day(10)}
	c := newTestCatalogue(t, clock.now)

	p, err := c.CreateProduct(ctx, "Basic Current Account", CurrentAccount)
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if p.ID == "" {
		t.Fatal("CreateProduct returned no ID")
	}
	if p.Retired {
		t.Error("a new product is retired")
	}

	// A draft prices nothing: it is invisible to resolution until published.
	drafted, err := c.DraftVersion(ctx, p.ID, day(20), OverdraftPricing{Rate: 120_000, DayCount: 0})
	if err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}
	if drafted.Published() {
		t.Error("a draft is published")
	}
	if drafted.Hash != "" {
		t.Error("a draft carries a hash; the hash is computed at publication")
	}
	if _, err := c.VersionInForce(ctx, p.ID, day(25)); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("a draft priced a day: %v", err)
	}

	// A draft may be edited — that is what it is for.
	if _, err := c.DraftVersion(ctx, p.ID, day(20), OverdraftPricing{Rate: 149_000}); err != nil {
		t.Fatalf("redrafting the same day: %v", err)
	}

	pub, err := c.PublishVersion(ctx, p.ID, day(20))
	if err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}
	if !pub.Published() {
		t.Error("PublishVersion returned an unpublished row")
	}
	if pub.Hash != pub.ComputeHash() {
		t.Error("the published hash does not verify")
	}
	if int64(pub.Overdraft.Rate) != 149_000 {
		t.Errorf("published rate = %d, want the redrafted 149000", pub.Overdraft.Rate)
	}

	// And a published version is frozen.
	if _, err := c.DraftVersion(ctx, p.ID, day(20), OverdraftPricing{Rate: 1}); !errors.Is(err, ErrVersionPublished) {
		t.Errorf("editing a published version: %v, want ErrVersionPublished", err)
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(20)); !errors.Is(err, ErrVersionPublished) {
		t.Errorf("republishing: %v, want ErrVersionPublished", err)
	}

	clock.set(day(21))
	got, err := c.VersionInForce(ctx, p.ID, day(21))
	if err != nil {
		t.Fatalf("VersionInForce: %v", err)
	}
	if int64(got.Overdraft.Rate) != 149_000 {
		t.Errorf("in force = %d, want 149000", got.Overdraft.Rate)
	}
}

// Publication is forward-only. A version effective in the past would reprice
// every account bound to the product retroactively, moving interest already
// charged, with the audit log as the only control. Retroactivity is a
// per-account overlay instead — see the design doc.
func TestPublishRefusesThePast(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: day(10)}
	c := newTestCatalogue(t, clock.now)

	p, err := c.CreateProduct(ctx, "Basic", CurrentAccount)
	if err != nil {
		t.Fatalf("CreateProduct: %v", err)
	}
	if _, err := c.DraftVersion(ctx, p.ID, day(5), OverdraftPricing{Rate: 120_000}); err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}

	if _, err := c.PublishVersion(ctx, p.ID, day(5)); !errors.Is(err, ErrRetroactivePublish) {
		t.Fatalf("publishing effective in the past: %v, want ErrRetroactivePublish", err)
	}

	// Today is fine — a version effective today prices today onwards, and
	// today has not been accrued yet.
	if _, err := c.DraftVersion(ctx, p.ID, day(10), OverdraftPricing{Rate: 120_000}); err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(10)); err != nil {
		t.Fatalf("publishing effective today: %v", err)
	}
}

func TestRetireTakesAProductOffSaleWithoutUnpricingIt(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: day(10)}
	c := newTestCatalogue(t, clock.now)

	p, _ := c.CreateProduct(ctx, "Basic", CurrentAccount)
	if _, err := c.DraftVersion(ctx, p.ID, day(10), OverdraftPricing{Rate: 120_000}); err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(10)); err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}

	retired, err := c.RetireProduct(ctx, p.ID)
	if err != nil {
		t.Fatalf("RetireProduct: %v", err)
	}
	if !retired.Retired {
		t.Fatal("RetireProduct did not retire")
	}

	// The accounts already sold from it keep resolving, for as long as they
	// live. A bank that could not express that would keep dead products on sale.
	clock.set(day(400))
	if _, err := c.VersionInForce(ctx, p.ID, day(400)); err != nil {
		t.Fatalf("a retired product stopped pricing: %v", err)
	}
}

func TestUnknownProductAndBadPricingAreRefused(t *testing.T) {
	ctx := context.Background()
	c := newTestCatalogue(t, (&mutableClock{at: day(10)}).now)

	if _, err := c.DraftVersion(ctx, "prd_nope", day(10), OverdraftPricing{Rate: 1}); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("drafting against an unknown product: %v", err)
	}
	if _, err := c.PublishVersion(ctx, "prd_nope", day(10)); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("publishing an unknown product: %v", err)
	}

	p, _ := c.CreateProduct(ctx, "Basic", CurrentAccount)
	if _, err := c.DraftVersion(ctx, p.ID, day(10), OverdraftPricing{UnarrangedRate: 350_000}); !errors.Is(err, ErrInvalidRate) {
		t.Error("an unarranged rate with no arranged one was accepted")
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(10)); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("publishing a day with no draft: %v, want ErrVersionNotFound", err)
	}
	if _, err := c.CreateProduct(ctx, "", CurrentAccount); err == nil {
		t.Error("an unnamed product was accepted")
	}
}

// Every catalogue write is in the audit log, under its own scope, because a
// published price is a fact an auditor asks who entered and when.
func TestCatalogueWritesAreAudited(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: day(10)}
	s := mem.New(clock.now)
	t.Cleanup(func() { _ = s.Close() })
	book := ledger.NewBook(s, "bank", clock.now)
	c := NewCatalogue(s.Product(), book, "bank", clock.now)

	p, _ := c.CreateProduct(ctx, "Basic", CurrentAccount)
	if _, err := c.DraftVersion(ctx, p.ID, day(10), OverdraftPricing{Rate: 120_000}); err != nil {
		t.Fatalf("DraftVersion: %v", err)
	}
	if _, err := c.PublishVersion(ctx, p.ID, day(10)); err != nil {
		t.Fatalf("PublishVersion: %v", err)
	}
	if _, err := c.RetireProduct(ctx, p.ID); err != nil {
		t.Fatalf("RetireProduct: %v", err)
	}

	events, err := book.Audit(ctx, ledger.AuditFilter{Scope: ledger.ScopeProduct})
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	want := []string{
		ledger.EventProductCreated,
		ledger.EventProductVersionDrafted,
		ledger.EventProductVersionPublished,
		ledger.EventProductRetired,
	}
	if len(events) != len(want) {
		t.Fatalf("audit events = %d, want %d", len(events), len(want))
	}
	for i, w := range want {
		if events[i].Type != w {
			t.Errorf("event %d = %s, want %s", i, events[i].Type, w)
		}
	}
}
```

Check `ledger.AuditFilter`'s field names and `book.Audit`'s signature before running — match the file, and if the filter takes a different shape, filter in Go instead.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./product/...`
Expected: FAIL — `undefined: NewCatalogue`.

- [ ] **Step 4: Write `product/catalogue.go`**

```go
package product

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Catalogue is the register over a book's products. It owns no state: products
// and versions live in a Store, exactly as the ledger's rows do, and the
// Catalogue keeps only the store handle, the book it audits through, the BookID
// both are scoped to, and its clock.
//
// # What it refuses, and why the refusals are here
//
// A store accepts any write. The rules that make a catalogue trustworthy — a
// published version is frozen, publication is forward-only, an unknown product
// has no versions — are all enforced here, in the domain layer, because
// store/mem and store/pg must accept exactly the same writes and a constraint
// in one of them would be a divergence. It is the same position the schema
// takes about text and about parent references.
type Catalogue struct {
	store  Store
	gl     *ledger.Book
	bookID ledger.BookID
	clock  func() time.Time
}

// NewCatalogue creates a catalogue over the given store for one book.
//
// id must be book.ID(): the catalogue's rows and the book's audit events are
// scoped by the same BookID. Share the clock with the book so that audit
// timestamps and effective dates line up across layers.
func NewCatalogue(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time) *Catalogue {
	return &Catalogue{store: store, gl: book, bookID: id, clock: clock}
}

func (c *Catalogue) now() time.Time { return c.clock() }

// today is the day publication is measured against. Day-granular, because
// EffectiveFrom is.
func (c *Catalogue) today() time.Time { return ledger.DayStart(c.now()) }

// appendAuditTx mirrors deposit.Register.appendAuditTx: the payload is
// marshalled now rather than held by reference, so a later mutation cannot
// rewrite history, and the event is stamped with this book's ID.
func (c *Catalogue) appendAuditTx(ctx context.Context, tx Tx, eventType, entityID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit %s: marshal payload: %w", eventType, err)
	}
	id, err := tx.NextID(ctx, c.bookID, "evt")
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, ledger.AuditEvent{
		ID:         id,
		BookID:     c.bookID,
		Scope:      ledger.ScopeProduct,
		Type:       eventType,
		EntityID:   entityID,
		Payload:    raw,
		OccurredAt: c.now(),
	})
}

// CreateProduct adds a catalogue entry. It has no price yet: DraftVersion and
// PublishVersion give it one, and until then no account can be opened from it.
//
// Returns ledger.ErrInvalidText for an unusable name and ErrKindMismatch for an
// unknown kind.
func (c *Catalogue) CreateProduct(ctx context.Context, name string, kind Kind) (Product, error) {
	var out Product
	err := c.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		id, err := tx.NextID(ctx, c.bookID, "prd")
		if err != nil {
			return err
		}
		p := Product{ID: ID(id), Name: name, Kind: kind, CreatedAt: c.now()}
		if err := p.Validate(); err != nil {
			return err
		}
		if err := tx.PutProduct(ctx, c.bookID, p); err != nil {
			return err
		}
		if err := c.appendAuditTx(ctx, tx, ledger.EventProductCreated, id, p); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// DraftVersion writes an unpublished version for one effective day, or replaces
// the draft already there.
//
// A draft prices nothing: resolution skips it, so the published version before
// it stays in force through its day. That is what stops immutability from
// meaning a typo in a rate is permanent — and a rule worked around with a
// manual UPDATE is the thing this package defends against.
//
// effectiveFrom is truncated to a day. It may be in the past: only PUBLICATION
// is forward-only, and refusing a backdated draft would only mean the refusal
// arrived at a less useful moment.
//
// Returns ErrProductNotFound, ErrVersionPublished if that day is already
// published, and ErrInvalidRate.
func (c *Catalogue) DraftVersion(ctx context.Context, id ID, effectiveFrom time.Time, pricing OverdraftPricing) (Version, error) {
	var out Version
	err := c.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetProduct(ctx, c.bookID, id); err != nil {
			return err
		}
		day := ledger.DayStart(effectiveFrom)

		existing, err := c.versionOnTx(ctx, tx, id, day)
		if err != nil {
			return err
		}
		if existing.Published() {
			return fmt.Errorf("%w: %s effective %s", ErrVersionPublished, id, VersionDayKey(day))
		}

		v := Version{
			ProductID:     id,
			EffectiveFrom: day,
			Overdraft:     pricing,
			CreatedAt:     c.now(),
		}
		if err := v.Validate(); err != nil {
			return err
		}
		if err := tx.PutProductVersion(ctx, c.bookID, v); err != nil {
			return err
		}
		if err := c.appendAuditTx(ctx, tx, ledger.EventProductVersionDrafted, string(id), v); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// PublishVersion freezes the draft for one effective day and stamps its content
// hash. From then on it prices every account bound to the product whose own row
// carries no overlay, for every day from effectiveFrom until the next published
// version.
//
// # Forward-only
//
// A version effective before today is refused with ErrRetroactivePublish. It
// would move interest already charged on every account bound to the product at
// once, and the audit log would be the only control on it; maker-checker is the
// real answer and this system has none. Retroactive repricing stays where its
// blast radius is one customer: deposit's per-account pricing overlay, which
// may be backdated and whose delta the accrual posts as ordinary correction
// interest. Correcting a mispublished rate is therefore laborious, and should
// be — it is a set of individual decisions about money already taken from named
// customers.
//
// Returns ErrProductNotFound, ErrVersionNotFound if that day has no draft,
// ErrVersionPublished if it is already published, and ErrRetroactivePublish.
func (c *Catalogue) PublishVersion(ctx context.Context, id ID, effectiveFrom time.Time) (Version, error) {
	var out Version
	err := c.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetProduct(ctx, c.bookID, id); err != nil {
			return err
		}
		day := ledger.DayStart(effectiveFrom)
		if day.Before(c.today()) {
			return fmt.Errorf("%w: %s effective %s, today is %s",
				ErrRetroactivePublish, id, VersionDayKey(day), VersionDayKey(c.today()))
		}

		v, err := c.versionOnTx(ctx, tx, id, day)
		if err != nil {
			return err
		}
		if v.ProductID == "" {
			return fmt.Errorf("%w: %s effective %s", ErrVersionNotFound, id, VersionDayKey(day))
		}
		if v.Published() {
			return fmt.Errorf("%w: %s effective %s", ErrVersionPublished, id, VersionDayKey(day))
		}

		v.PublishedAt = c.now()
		v.Hash = v.ComputeHash()
		if err := tx.PutProductVersion(ctx, c.bookID, v); err != nil {
			return err
		}
		if err := c.appendAuditTx(ctx, tx, ledger.EventProductVersionPublished, string(id), v); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// RetireProduct takes a product off sale: no new account may be opened from it
// and no account may be migrated to it.
//
// It does not unprice anything. The accounts already sold from a withdrawn
// product keep resolving against its versions for as long as they live, which
// is why Retired is checked at OpenAccount and never at resolution.
//
// Returns ErrProductNotFound.
func (c *Catalogue) RetireProduct(ctx context.Context, id ID) (Product, error) {
	var out Product
	err := c.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		p, err := tx.GetProduct(ctx, c.bookID, id)
		if err != nil {
			return err
		}
		p.Retired = true
		if err := tx.PutProduct(ctx, c.bookID, p); err != nil {
			return err
		}
		if err := c.appendAuditTx(ctx, tx, ledger.EventProductRetired, string(id), p); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// GetProduct returns one catalogue entry. Returns ErrProductNotFound.
func (c *Catalogue) GetProduct(ctx context.Context, id ID) (Product, error) {
	var out Product
	err := c.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetProduct(ctx, c.bookID, id)
		return err
	})
	return out, err
}

// ListProducts returns the whole catalogue, retired entries included: a
// withdrawn product is still the product some accounts are on.
func (c *Catalogue) ListProducts(ctx context.Context) ([]Product, error) {
	var out []Product
	err := c.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListProducts(ctx, c.bookID)
		return err
	})
	return out, err
}

// Versions returns a product's whole timeline, oldest first, drafts included.
// It is the point of making the catalogue effective-dated: the history is
// inspectable rather than merely recoverable by replaying the audit log.
//
// Returns ErrProductNotFound.
func (c *Catalogue) Versions(ctx context.Context, id ID) ([]Version, error) {
	var out []Version
	err := c.store.View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetProduct(ctx, c.bookID, id); err != nil {
			return err
		}
		var err error
		out, err = tx.ListProductVersions(ctx, c.bookID, id)
		return err
	})
	return out, err
}

// VersionInForce is the published version pricing a day, with its hash
// verified.
//
// Returns ErrProductNotFound, ErrVersionNotFound for a day the product had no
// published price on, and ErrHashMismatch for a row edited in the database.
func (c *Catalogue) VersionInForce(ctx context.Context, id ID, day time.Time) (Version, error) {
	var out Version
	err := c.store.View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetProduct(ctx, c.bookID, id); err != nil {
			return err
		}
		v, err := tx.GetProductVersionAsOf(ctx, c.bookID, id, ledger.DayStart(day))
		if err != nil {
			return err
		}
		if err := v.VerifyHash(); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// versionOnTx is the version stored for exactly one effective day, published or
// draft, or a zero Version if that day has none.
//
// It reads the timeline rather than using GetProductVersionAsOf, because
// as-of is the wrong question here twice over: it skips drafts, and it returns
// the row in force on the day rather than the row FOR the day. A draft for the
// 20th must be found when the 1st is published, and must not be mistaken for
// one when the 20th is asked about.
func (c *Catalogue) versionOnTx(ctx context.Context, tx Tx, id ID, day time.Time) (Version, error) {
	rows, err := tx.ListProductVersions(ctx, c.bookID, id)
	if err != nil {
		return Version{}, err
	}
	want := VersionDayKey(day)
	for _, v := range rows {
		if VersionDayKey(v.EffectiveFrom) == want {
			return v, nil
		}
	}
	return Version{}, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./product/... ./ledger/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add product/catalogue.go product/catalogue_test.go ledger/audit.go
git commit -m "feat(product): add the Catalogue register and its audit trail

Create, draft, publish, retire — each audited under a new ScopeProduct, because
a published price is a fact an auditor asks who entered and when. A draft is
editable and prices nothing; publishing freezes the row and stamps its hash.
Publication is forward-only: a retroactive version would move interest already
charged on every account bound to the product at once, so retroactivity stays
with the per-account overlay, where the blast radius is one customer.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Deposit terms rows learn about products

The row gains a `ProductID` and an optional pricing overlay, and `Resolve` merges the two timelines. Nothing reads the new fields yet — that is Task 5 — so this task lands the storage and the resolver together, which is the only way the round-trip is testable on its own.

**Files:**
- Modify: `deposit/terms.go`, `deposit/store.go`, `deposit/errors.go`
- Modify: `store/mem/tx_deposit.go`, `store/pg/tx_deposit.go`, `store/pg/schema/0001_init.sql`
- Modify: `store/storetest/deposit.go`
- Test: `deposit/terms_test.go`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  - `deposit.OverdraftTerms` with `ProductID product.ID` and `Pricing *product.OverdraftPricing` (its `Rate`, `UnarrangedRate` and `DayCount` fields are gone)
  - `type EffectiveTerms struct { ProductID product.ID; Limit ledger.Amount; Pricing product.OverdraftPricing }`
  - `func Resolve(rows []OverdraftTerms, versions map[product.ID][]product.Version, day time.Time) (EffectiveTerms, error)`
  - `func anyPriced(rows []OverdraftTerms, versions map[product.ID][]product.Version) bool` — unexported, replacing the one-argument form
  - `deposit.ErrProductRequired`
  - `deposit.Tx` embeds `product.Tx`

- [ ] **Step 1: Write the failing tests**

Add to `deposit/terms_test.go`. Check its package clause first and match it — it tests unexported `termsAt`, so it is an internal test (`package deposit`), and the helpers below assume that.

```go
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
}

func TestValidateRequiresAProductAndACompletePricing(t *testing.T) {
	base := OverdraftTerms{AccountID: "dep_1", EffectiveFrom: day(0), ProductID: "prd_basic", OverdraftLimit: 1}

	if err := base.Validate(); err != nil {
		t.Fatalf("a floating row: %v", err)
	}
	noProduct := base
	noProduct.ProductID = ""
	if err := noProduct.Validate(); !errors.Is(err, ErrProductRequired) {
		t.Errorf("no product: %v, want ErrProductRequired", err)
	}
	bad := base
	bad.Pricing = &product.OverdraftPricing{UnarrangedRate: 350_000}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("unarranged with no arranged: %v, want ErrInvalidRate", err)
	}
	negative := base
	negative.OverdraftLimit = -1
	if err := negative.Validate(); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("negative limit: %v, want ErrInvalidAmount", err)
	}
}
```

If the file has no `day` helper, add `func day(n int) time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n) }` beside `publishedVersion`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./deposit/ -run 'TestResolve|TestAnyPriced|TestValidateRequires'`
Expected: FAIL to compile — `undefined: Resolve`, `undefined: ErrProductRequired`, and `OverdraftTerms` has no field `ProductID`.

- [ ] **Step 3: Add the sentinel**

In `deposit/errors.go`, after `ErrTermsNotFound`:

```go
	// ErrProductRequired is returned for a terms row that names no product.
	//
	// Every deposit account is opened FROM a product, so a row without one
	// could not resolve a floating pricing: there would be nothing to float to.
	// Refusing it is what keeps Resolve from needing a case for a state the
	// model should not be able to reach.
	ErrProductRequired = errors.New("overdraft terms must name a product")
```

- [ ] **Step 4: Extend the row**

In `deposit/terms.go`, add the `product` and `fmt` imports, then replace the four pricing fields:

```go
type OverdraftTerms struct {
	AccountID     AccountID
	EffectiveFrom time.Time // day-truncated; the first day these terms apply

	// ProductID is the catalogue entry this account is on from this day. It is
	// on the ROW rather than on Account because it varies over the account's
	// life: migrating between products is an ordinary forward-dated row, and a
	// column on Account would contradict the timeline the moment a future-dated
	// migration was entered — the Account.Rate mistake with a new name.
	ProductID product.ID

	// OverdraftLimit is the positive amount the balance may go below zero by;
	// 0 means none.
	//
	// It is PINNED: it is on this row for every day of the account's life and
	// never resolves from the product, because a limit is an underwriting
	// decision about one customer rather than a price the bank publishes.
	// product.OverdraftPricing cannot express one at all, which is what makes
	// this a fact about the types rather than a rule to remember.
	OverdraftLimit ledger.Amount

	// Pricing is this customer's NEGOTIATED price, or nil to float with the
	// product version in force on the day.
	//
	// nil means float and specifically NOT interest-free: a zero-rate overlay
	// is a real interest-free product and a different, deliberate statement.
	// Validate refuses the states in between, and store/pg's three nullable
	// columns carry a COMMENT saying the same, because the difference between
	// "NULL means free" and "NULL means ask the product" is invisible in a
	// schema dump.
	Pricing *product.OverdraftPricing

	CreatedAt time.Time // when the row was entered, not when it takes effect
}
```

and rewrite `Validate`:

```go
// Validate reports whether these terms are a product. The rules are held on the
// row so that a store round trip and a register call are checked against one
// standard.
//
// The pricing rules are product.OverdraftPricing's, because an overlay and a
// catalogue version are the same three numbers and must be judged the same way.
// The error is re-wrapped in this layer's sentinel so that a caller of the
// deposit layer — and the api's status mapping — has one thing to check.
func (t OverdraftTerms) Validate() error {
	if t.ProductID == "" {
		return ErrProductRequired
	}
	if t.OverdraftLimit < 0 {
		return ErrInvalidAmount
	}
	if t.Pricing == nil {
		return nil
	}
	if err := t.Pricing.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRate, err)
	}
	return nil
}
```

- [ ] **Step 5: Write the resolver**

Replace `anyPriced` in `deposit/terms.go` with `EffectiveTerms`, `Resolve` and the two-argument `anyPriced`:

```go
// EffectiveTerms is what an account's overdraft actually costs on one day: the
// merge of its own row and the product version in force.
//
// It is in-memory only and is never stored. That is the rule the terms timeline
// already follows one level down — current terms are resolved, never cached on
// the row — for the same reason: a second copy of a derivable fact is a second
// place it can be wrong.
type EffectiveTerms struct {
	// ProductID is which catalogue entry priced this day. It is what makes
	// "what did this account's product say on 15 July 2027?" answerable rather
	// than merely recoverable.
	ProductID product.ID
	Limit     ledger.Amount
	Pricing   product.OverdraftPricing
}

// Resolve merges the timelines for one day.
//
// Order of precedence: the account's own row supplies the limit, always; the
// account's row supplies the pricing if it carries an overlay; otherwise the
// product version in force on that day does.
//
// versions is keyed by product because an account's life can span several — a
// ChangeProduct is a forward-dated row like any other — and a day resolves
// against the product in force on THAT day. A single slice would silently price
// pre-migration days from the new product, which is the class of bug this whole
// design exists to prevent. Extra keys are harmless: an accrual run loads one
// entry per distinct product across every account it touches and hands the same
// map to every call.
//
// rows must be ascending by EffectiveFrom, and so must each slice of versions.
//
// The three failures are worth telling apart, which is why this returns an error
// where termsAt returns a bool: ErrTermsNotFound for a day before the account's
// first row, product.ErrVersionNotFound for a floating day whose product had no
// published price, and product.ErrHashMismatch for a version edited in the
// database.
func Resolve(rows []OverdraftTerms, versions map[product.ID][]product.Version, day time.Time) (EffectiveTerms, error) {
	row, ok := termsAt(rows, day)
	if !ok {
		return EffectiveTerms{}, ErrTermsNotFound
	}
	out := EffectiveTerms{ProductID: row.ProductID, Limit: row.OverdraftLimit}
	if row.Pricing != nil {
		out.Pricing = *row.Pricing
		return out, nil
	}
	v, err := product.VersionAt(versions[row.ProductID], day)
	if err != nil {
		return EffectiveTerms{}, err
	}
	out.Pricing = v.Overdraft
	return out, nil
}

// anyPriced reports whether any day in the account's life could carry a
// non-zero rate. It is what keeps a never-priced account from reading a
// value-dated series every night.
//
// A row with an overlay is judged on the overlay alone: a zero-rate overlay is
// a definite statement that those days are free. A FLOATING row has to look
// through to its product, which is the case the old account-level `Rate <= 0`
// guard could not express at all.
//
// It errs towards running. A product version outside the account's life still
// counts, because bounding each row's span against the next row's day would be
// a second, subtler copy of Resolve — and the cost of being wrong this way is
// one unnecessary series read, not a wrong number.
func anyPriced(rows []OverdraftTerms, versions map[product.ID][]product.Version) bool {
	for _, r := range rows {
		if r.Pricing != nil {
			if r.Pricing.Rate > 0 {
				return true
			}
			continue
		}
		for _, v := range versions[r.ProductID] {
			if v.Published() && v.Overdraft.Rate > 0 {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 6: Embed `product.Tx` in `deposit.Tx`**

In `deposit/store.go`:

```go
// Tx embeds product.Tx — and, through it, ledger.Tx — so one concrete
// transaction spans all three layers. That is what makes CaptureHold (a hold
// write plus a GL posting) a single unit of work, and what lets OpenAccount
// validate the product an account is being opened from and write the account's
// first terms row without leaving it.
type Tx interface {
	product.Tx
```

`store/mem` and `store/pg` already satisfy the wider interface: their single `*tx` carries every method set, which is the whole reason for this shape.

- [ ] **Step 7: Run the resolver tests**

Run: `go test ./deposit/ -run 'TestResolve|TestAnyPriced|TestValidateRequires'`
Expected: PASS. `./deposit/` as a whole will not build until Task 5 rewrites `register.go`'s callers — that is expected here, and `-run` keeps this task's own tests honest in the meantime.

- [ ] **Step 8: Persist the two new fields**

Schema — `overdraft_terms` gains `product_id` and its three pricing columns lose `NOT NULL`:

```sql
CREATE TABLE overdraft_terms (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    account_id      TEXT NOT NULL,
    day_key         TEXT NOT NULL,
    effective_from  TIMESTAMPTZ,
    product_id      TEXT NOT NULL,
    overdraft_limit BIGINT NOT NULL,
    rate            BIGINT,
    unarranged_rate BIGINT,
    day_count       SMALLINT,
    created_at      TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, account_id, day_key)
);

COMMENT ON COLUMN overdraft_terms.product_id IS
    'The catalogue entry this account is on from this day. On the row rather than on deposit_accounts because it varies over the account life: migrating between products is an ordinary forward-dated row, and a column on the account would contradict the timeline the moment a future-dated migration was entered.';

COMMENT ON COLUMN overdraft_terms.rate IS
    'NULL in all three pricing columns means the pricing FLOATS: resolve it from the product version in force on this day. All three set is a negotiated overlay for this one customer. NULL does NOT mean interest-free - a zero-rate overlay is a real interest-free product and a different, deliberate statement. The mixed state is refused by deposit.OverdraftTerms.Validate rather than by a CHECK, because store/mem must refuse exactly the same rows; this comment exists because neither the rule nor the meaning of NULL is visible in a schema dump.';
```

`store/pg/tx_deposit.go` — `overdraftTermsColumns` becomes `account_id, effective_from, product_id, overdraft_limit, rate, unarranged_rate, day_count, created_at` (the constant exists so the two readers cannot scan different column sets, so it is the only place the list changes). `PutOverdraftTerms` writes `product_id` in both branches and the pricing as three nullable values:

```go
	// A floating row stores three NULLs rather than three zeros: zero is a real
	// interest-free price, and conflating the two would silently make every
	// floating account free.
	var rate, unarranged *int64
	var dayCount *int16
	if row.Pricing != nil {
		r, u := int64(row.Pricing.Rate), int64(row.Pricing.UnarrangedRate)
		d := int16(row.Pricing.DayCount)
		rate, unarranged, dayCount = &r, &u, &d
	}
```

and `scanOverdraftTerms` reconstructs the overlay only when all three are present:

```go
	var (
		out                deposit.OverdraftTerms
		rate, unarranged   *int64
		dayCount           *int16
		effective, created *time.Time
	)
	if err := row.Scan(&out.AccountID, &effective, &out.ProductID, &out.OverdraftLimit,
		&rate, &unarranged, &dayCount, &created); err != nil {
		return deposit.OverdraftTerms{}, err
	}
	if rate != nil && unarranged != nil && dayCount != nil {
		out.Pricing = &product.OverdraftPricing{
			Rate:           interest.Rate(*rate),
			UnarrangedRate: interest.Rate(*unarranged),
			DayCount:       interest.DayCount(*dayCount),
		}
	}
```

`store/mem/tx_deposit.go` — `PutOverdraftTerms` copies the pointee before storing:

```go
	// The overlay is a pointer, and this store hands the row itself to every
	// reader. Copying the POINTEE on write is what stops a caller that keeps
	// its argument from later rewriting stored history in place — which
	// store/pg cannot do at all, and the two must behave the same.
	if row.Pricing != nil {
		pricing := *row.Pricing
		row.Pricing = &pricing
	}
```

- [ ] **Step 9: Extend the conformance suite**

In `store/storetest/deposit.go`, give every existing `deposit.OverdraftTerms` literal a `ProductID` and move its rate/day-count into a `Pricing` overlay (or drop them, leaving the row floating — do both across the suite so each shape is exercised), then add:

```go
	// The overlay is the deposit layer's only pointer field, and the only place
	// a store can conflate "float from the product" with "interest-free". Both
	// stores must round-trip the distinction, and neither may hand a reader a
	// pointer into its own state.
	t.Run("OverdraftTermsPricingOverlayRoundTrip", func(t *testing.T) {
		s := openDeposit(t, newStore)

		overlay := product.OverdraftPricing{Rate: 90_000, UnarrangedRate: 350_000, DayCount: interest.Thirty360}
		free := product.OverdraftPricing{}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, row := range []deposit.OverdraftTerms{
				{AccountID: "dep_1", EffectiveFrom: day(1), ProductID: "prd_basic", OverdraftLimit: 500},
				{AccountID: "dep_1", EffectiveFrom: day(10), ProductID: "prd_basic", OverdraftLimit: 500, Pricing: &overlay},
				{AccountID: "dep_1", EffectiveFrom: day(20), ProductID: "prd_basic", OverdraftLimit: 500, Pricing: &free},
			} {
				if err := tx.PutOverdraftTerms(ctx, bookA, row); err != nil {
					return err
				}
			}
			return nil
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "rows", len(rows), 3)

			if rows[0].Pricing != nil {
				t.Error("a floating row came back with a pricing")
			}
			assertEqual(t, "product", string(rows[0].ProductID), "prd_basic")

			if rows[1].Pricing == nil {
				t.Fatal("the overlay was dropped")
			}
			assertEqual(t, "overlay rate", int64(rows[1].Pricing.Rate), int64(90_000))
			assertEqual(t, "overlay unarranged", int64(rows[1].Pricing.UnarrangedRate), int64(350_000))
			assertEqual(t, "overlay day count", int(rows[1].Pricing.DayCount), int(interest.Thirty360))

			if rows[2].Pricing == nil {
				t.Fatal("a zero-rate overlay came back as floating; free and floating are different")
			}
			assertEqual(t, "free overlay rate", int64(rows[2].Pricing.Rate), int64(0))

			// Mutating what a reader was handed must not change stored state.
			rows[1].Pricing.Rate = 1
			return nil
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the stored overlay rate", int64(rows[1].Pricing.Rate), int64(90_000))
			return nil
		})
	})
```

- [ ] **Step 10: Run the store suites**

Run: `go test ./store/... ./product/... && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./store/pg/...`
Expected: PASS. `./deposit/`, `./api/`, `./seed/` and `./payment/` do not build until Task 5; nothing else may be red.

- [ ] **Step 11: Commit**

```bash
git add deposit store/mem store/pg store/storetest
git commit -m "feat(deposit): resolve overdraft terms against a product timeline

OverdraftTerms gains ProductID and an optional pricing overlay in place of its
three pricing fields; Resolve merges the two timelines for one day, taking the
limit from the account always, the overlay when present, and the product version
in force otherwise. A floating row stores three NULLs rather than three zeros,
because zero is a real interest-free price and conflating them would make every
floating account free.

The register does not compile against this yet; the next commit rewrites it.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Accounts are opened from a product

**This task cannot be split.** The moment `OpenAccount` takes a product, `payment`, `api` and `seed` stop compiling, and the split setters replace `SetOverdraftTerms` in the same breath. Everything below lands in one commit; the steps are still bite-sized.

**Files:**
- Modify: `deposit/register.go`, `deposit/register_test.go`
- Modify: `payment/participant.go`, `payment/store.go`, `payment/system.go`
- Modify: `api/dto_deposit.go`, `api/handlers_deposit.go`, `api/errors.go`, `api/server_test.go`
- Modify: `seed/seed.go`

**Interfaces:**
- Consumes: Task 4's `Resolve`, `EffectiveTerms`, `anyPriced`; Task 3's `product.Catalogue`.
- Produces:
  - `func (r *Register) OpenAccount(ctx context.Context, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, productID product.ID, overdraftLimit ledger.Amount) (Account, error)` and `…Tx`
  - `func (r *Register) SetOverdraftLimit(ctx context.Context, id AccountID, limit ledger.Amount, effectiveFrom time.Time) (OverdraftTerms, error)` and `…Tx`
  - `func (r *Register) SetOverdraftPricingOverlay(ctx context.Context, id AccountID, pricing *product.OverdraftPricing, effectiveFrom time.Time) (OverdraftTerms, error)` and `…Tx`
  - `func (r *Register) ChangeProduct(ctx context.Context, id AccountID, productID product.ID, effectiveFrom time.Time) (OverdraftTerms, error)` and `…Tx`
  - `deposit.AccountWithTerms{Account Account; Terms EffectiveTerms}` (its `Terms` field changes type)
  - `func overdraftAccrual(book ledger.Amount, t EffectiveTerms, from, to time.Time) interest.Accrued`
  - `type versionCache map[product.ID][]product.Version` (unexported)
  - `payment.Participant.Catalogue *product.Catalogue`, `payment.Participant.ProductID product.ID`
  - `SetOverdraftTerms` is **gone**
  - New audit events: `ledger.EventOverdraftLimitSet`, `ledger.EventOverdraftPricingOverlaid`, `ledger.EventAccountProductChanged` (drop `EventOverdraftTermsSet`)

- [ ] **Step 1: Write the acceptance tests**

Add to `deposit/register_test.go`. It is `package deposit_test` and dot-imports `deposit`, so `Account` and the sentinels are unqualified — match the file. These four are the capabilities the catalogue exists for; they need a helper that builds a register with a catalogue over the same store:

First, `store/testenv`'s `Store` interface gains the third view, beside `Deposit()` and `Payment()` — that is what lets these tests run against `store/pg` too, which is the whole point of that package:

```go
type Store interface {
	ledger.Store
	Deposit() deposit.Store
	Payment() payment.Store
	Product() product.Store
}
```

Then the helper, beside `newTestRegisterOn` (`deposit/register_test.go:62`), sharing one store between both layers because that is what makes an account and its product one unit of work:

```go
// newTestRegisterWithCatalogue is newTestRegisterOn plus a catalogue over the
// same store and book, because after this change an account cannot be opened
// without a product to open it from.
func newTestRegisterWithCatalogue(t *testing.T, clock func() time.Time) (*Register, *product.Catalogue, *ledger.Book, ledger.SubledgerID) {
	t.Helper()
	ctx := context.Background()
	store := testenv.New(t, clock)
	book := ledger.NewBook(store, "bank", clock)
	reg := NewRegister(store.Deposit(), book, book.ID(), clock)
	cat := product.NewCatalogue(store.Product(), book, book.ID(), clock)

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	deposits, err := book.CreateSubledger(ctx, gl.ID, "Customer Deposits")
	assertNoError(t, err)

	return reg, cat, book, deposits.ID
}
```

```go
// One published version reprices every account bound to the product, per day,
// with no per-account write at all. This is the capability the catalogue exists
// for, and nothing before it could express the change.
func TestPublishingAVersionRepricesEveryFloatingAccount(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, book, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic Current Account", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000, DayCount: interest.ACT365})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)

	const drawn ledger.Amount = 100_000
	one, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	two, err := reg.OpenAccount(ctx, sub, "Bella", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, one, drawn, day(0), day(0))
	overdrawValueDated(t, book, sub, two, drawn, day(0), day(0))

	// Repriced from day 30, published on day 10 — forward-dated, which is the
	// only direction the catalogue allows.
	clock.set(day(10))
	_, err = cat.DraftVersion(ctx, basic.ID, day(30), product.OverdraftPricing{Rate: 180_000, DayCount: interest.ACT365})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(30))
	assertNoError(t, err)

	clock.set(day(60))
	for _, acct := range []Account{one, two} {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(60)))
	}

	// Thirty days at 12%, thirty at 18% — for both accounts, from one row.
	want := expectedFromTimeline(day(0), 60, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			if d < 30 {
				return 120_000
			}
			return 180_000
		})
	for _, acct := range []Account{one, two} {
		got, err := reg.GetAccount(ctx, acct.ID)
		assertNoError(t, err)
		assertEqual(t, "accrued for "+string(acct.ID), got.AccruedGross, want)
	}
}

// A negotiated rate is one customer's price instead of the product's, and it
// outranks a product reprice underneath it. Clearing it puts the account back on
// the product — at whatever the product costs by then, not at what it cost when
// the overlay was set.
func TestAnOverlayOutranksTheProductAndClearsBackToIt(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, book, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	for _, v := range []struct {
		from time.Time
		rate interest.Rate
	}{{day(0), 120_000}, {day(30), 180_000}} {
		_, err = cat.DraftVersion(ctx, basic.ID, v.from, product.OverdraftPricing{Rate: v.rate, DayCount: interest.ACT365})
		assertNoError(t, err)
		_, err = cat.PublishVersion(ctx, basic.ID, v.from)
		assertNoError(t, err)
	}

	const drawn ledger.Amount = 100_000
	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, drawn, day(0), day(0))

	// Negotiated 9% from day 10, cleared from day 40.
	_, err = reg.SetOverdraftPricingOverlay(ctx, acct.ID,
		&product.OverdraftPricing{Rate: 90_000, DayCount: interest.ACT365}, day(10))
	assertNoError(t, err)
	_, err = reg.SetOverdraftPricingOverlay(ctx, acct.ID, nil, day(40))
	assertNoError(t, err)

	clock.set(day(50))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(50)))

	want := expectedFromTimeline(day(0), 50, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			switch {
			case d < 10:
				return 120_000 // floating
			case d < 40:
				return 90_000 // negotiated, through the day-30 reprice
			default:
				return 180_000 // cleared, back onto the repriced product
			}
		})
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued", got.AccruedGross, want)
}

// The limit is pinned: publishing a version cannot change what a customer may
// spend. This is the central claim of the design, and it is cheap to pin.
func TestPublishingAVersionCannotMoveTheAvailableBalance(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, _, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	before, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)

	clock.set(day(1))
	_, err = cat.DraftVersion(ctx, basic.ID, day(2), product.OverdraftPricing{Rate: 900_000})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(2))
	assertNoError(t, err)

	clock.set(day(3))
	after, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "available", after.Available, before.Available)

	// And raising the limit is a per-account decision that does move it.
	_, err = reg.SetOverdraftLimit(ctx, acct.ID, 800_000, day(3))
	assertNoError(t, err)
	raised, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "available after a limit rise", raised.Available, before.Available+300_000)
}

// A version edited in the database stops the accrual rather than pricing a day
// from a row nobody published. This is the property the content hash exists for
// and the only test that proves it.
func TestATamperedVersionStopsTheAccrual(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, book, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000, DayCount: interest.ACT365})
	assertNoError(t, err)
	published, err := cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, 100_000, day(0), day(0))

	// Straight through the store, as a manual UPDATE would: the pricing moves
	// and the hash is left behind.
	tampered := published
	tampered.Overdraft.Rate = 900_000
	assertNoError(t, reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.PutProductVersion(ctx, book.ID(), tampered)
	}))

	clock.set(day(10))
	err = reg.AccrueOverdraft(ctx, acct.ID, day(10))
	if !errors.Is(err, product.ErrHashMismatch) {
		t.Fatalf("AccrueOverdraft = %v, want ErrHashMismatch", err)
	}
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "nothing was accrued", got.AccruedGross, interest.Accrued(0))
}

func TestOpenAccountRefusesABadProduct(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, _, sub := newTestRegisterWithCatalogue(t, clock.now)

	if _, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, "prd_nope", 0); !errors.Is(err, product.ErrProductNotFound) {
		t.Errorf("unknown product: %v, want ErrProductNotFound", err)
	}

	// A product with no published version has no price, so an account opened
	// from it could not resolve a single day. Refusing here is what stops that
	// turning into an accrual failure weeks later.
	unpriced, err := cat.CreateProduct(ctx, "Unpriced", product.CurrentAccount)
	assertNoError(t, err)
	if _, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, unpriced.ID, 0); !errors.Is(err, product.ErrVersionNotFound) {
		t.Errorf("unpriced product: %v, want ErrVersionNotFound", err)
	}

	// Retired takes it off sale without unpricing the accounts already on it —
	// which TestRetireTakesAProductOffSaleWithoutUnpricingIt pins in product.
	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)
	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, basic.ID, 0)
	assertNoError(t, err)
	_, err = cat.RetireProduct(ctx, basic.ID)
	assertNoError(t, err)

	if _, err := reg.OpenAccount(ctx, sub, "Bella", testAsset, basic.ID, 0); !errors.Is(err, product.ErrProductRetired) {
		t.Errorf("retired product: %v, want ErrProductRetired", err)
	}
	if _, err := reg.GetAccountWithTerms(ctx, acct.ID); err != nil {
		t.Errorf("an account on a retired product stopped resolving: %v", err)
	}
}

// Migrating between products is a forward-dated row like any other, so the days
// before it still resolve against the product that priced them.
func TestChangeProductPricesEachDayFromTheProductInForceOnIt(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, book, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	premium, err := cat.CreateProduct(ctx, "Premium", product.CurrentAccount)
	assertNoError(t, err)
	for _, p := range []struct {
		id   product.ID
		rate interest.Rate
	}{{basic.ID, 120_000}, {premium.ID, 70_000}} {
		_, err = cat.DraftVersion(ctx, p.id, day(0), product.OverdraftPricing{Rate: p.rate, DayCount: interest.ACT365})
		assertNoError(t, err)
		_, err = cat.PublishVersion(ctx, p.id, day(0))
		assertNoError(t, err)
	}

	const drawn ledger.Amount = 100_000
	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, drawn, day(0), day(0))

	_, err = reg.ChangeProduct(ctx, acct.ID, premium.ID, day(20))
	assertNoError(t, err)

	clock.set(day(40))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(40)))

	want := expectedFromTimeline(day(0), 40, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			if d < 20 {
				return 120_000
			}
			return 70_000
		})
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued", got.AccruedGross, want)
}

// A limit change carries the account's other terms forward, because each row is
// a complete statement of the account's own terms from its day. Losing the
// overlay here would silently reprice the customer.
func TestALimitChangeKeepsTheOverlayAndTheProduct(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, _, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000, DayCount: interest.ACT365})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	_, err = reg.SetOverdraftPricingOverlay(ctx, acct.ID,
		&product.OverdraftPricing{Rate: 90_000, DayCount: interest.ACT365}, day(5))
	assertNoError(t, err)

	clock.set(day(10))
	row, err := reg.SetOverdraftLimit(ctx, acct.ID, 800_000, day(10))
	assertNoError(t, err)
	if row.Pricing == nil {
		t.Fatal("the limit change dropped the negotiated overlay")
	}
	assertEqual(t, "overlay rate carried forward", int64(row.Pricing.Rate), int64(90_000))
	assertEqual(t, "product carried forward", string(row.ProductID), string(basic.ID))
	assertEqual(t, "the new limit", row.OverdraftLimit, ledger.Amount(800_000))
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./deposit/`
Expected: FAIL to compile — `OpenAccount` takes 5 arguments, `SetOverdraftPricingOverlay` is undefined.

- [ ] **Step 3: Replace the audit events**

In `ledger/audit.go`'s ScopeDeposit group, replace `EventOverdraftTermsSet` with the three writes that took its place:

```go
	// The three ways an account's own terms row changes, kept apart because they
	// are three different decisions: what the bank will lend this customer, what
	// this customer was promised instead of the list price, and which product
	// they are on. One event type for all three would make a rate change and a
	// limit change indistinguishable in the log.
	EventOverdraftLimitSet         = "overdraft.limit_set"
	EventOverdraftPricingOverlaid  = "overdraft.pricing_overlaid"
	EventAccountProductChanged     = "account.product_changed"
```

- [ ] **Step 4: Open from a product**

In `deposit/register.go`, `OpenAccountTx` gains the parameter, validates the product, and writes the opening row as floating:

```go
// OpenAccount opens a new customer deposit account, on a product.
//
// productID is the catalogue entry the account is priced by. Every account has
// one: a floating terms row with no product would have nothing to float to, and
// making that unreachable is cheaper than handling it. The product must exist,
// must not be Retired, must be of Kind CurrentAccount, and must have a published
// version in force today — an account opened from an unpriced product could not
// resolve a single day, and refusing here is what stops that surfacing as an
// accrual failure weeks later.
//
// overdraftLimit is a positive amount the account may go below zero by; 0 means
// no overdraft. It is NOT part of the product: a limit is an underwriting
// decision about this customer, so it is passed per account and stays on the
// account's own timeline for life.
//
// The account's opening terms row is floating — its pricing resolves from the
// product — which is what makes a later product-wide reprice reach it.
//
// Returns product.ErrProductNotFound, product.ErrProductRetired,
// product.ErrKindMismatch, product.ErrVersionNotFound, and any error from the
// underlying ledger.
func (r *Register) OpenAccountTx(ctx context.Context, tx Tx, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, productID product.ID, overdraftLimit ledger.Amount) (Account, error) {
	if err := ledger.ValidateText("name", name); err != nil {
		return Account{}, err
	}
	if err := r.checkOpenableProductTx(ctx, tx, productID); err != nil {
		return Account{}, err
	}
	... // the GL account, the ID and the Account literal are unchanged

	opening := OverdraftTerms{
		AccountID:      acct.ID,
		EffectiveFrom:  ledger.DayStart(acct.CreatedAt),
		ProductID:      productID,
		OverdraftLimit: overdraftLimit,
		CreatedAt:      acct.CreatedAt,
	}
	...
}
```

and the shared check, beside it:

```go
// checkOpenableProductTx is the product validation OpenAccount and
// ChangeProduct share: it must exist, be on sale, be the right kind, and have a
// price today.
//
// Retired is checked HERE and never at resolution, which is the distinction that
// lets a product go off sale without the accounts sold from it losing their
// price — see product.ErrProductRetired.
func (r *Register) checkOpenableProductTx(ctx context.Context, tx Tx, id product.ID) error {
	p, err := tx.GetProduct(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	if p.Retired {
		return fmt.Errorf("%w: %s", product.ErrProductRetired, id)
	}
	if p.Kind != product.CurrentAccount {
		return fmt.Errorf("%w: %s is a %s", product.ErrKindMismatch, id, p.Kind)
	}
	v, err := tx.GetProductVersionAsOf(ctx, r.bookID, id, ledger.DayStart(r.now()))
	if err != nil {
		return err
	}
	return v.VerifyHash()
}
```

- [ ] **Step 5: Split the setter into three**

Delete `SetOverdraftTerms` and `SetOverdraftTermsTx` and write the three that replace them, plus the helper they share:

```go
// SetOverdraftLimit changes what this customer may go overdrawn by, from a day.
//
// It is the PINNED half of the old SetOverdraftTerms: a limit is an underwriting
// decision about one customer and never comes from the catalogue. The pricing
// and the product are carried forward from the row in force on effectiveFrom,
// because each row is a complete statement of the account's own terms from its
// day — dropping the overlay here would silently reprice a customer who was
// promised a rate.
//
// effectiveFrom may be backdated or future-dated. A backdated limit does not
// move interest by itself, but it does change which part of a drawn balance was
// inside the limit on those days, so the next recompute trues the difference up
// as ordinary delta interest.
//
// Returns ErrAccountNotFound, ErrAccountClosed, ErrInvalidAmount, and
// ErrTermsNotFound for a day before the account existed.
func (r *Register) SetOverdraftLimitTx(ctx context.Context, tx Tx, id AccountID, limit ledger.Amount, effectiveFrom time.Time) (OverdraftTerms, error) {
	return r.appendTermsTx(ctx, tx, id, effectiveFrom, ledger.EventOverdraftLimitSet,
		func(row *OverdraftTerms) { row.OverdraftLimit = limit })
}

// SetOverdraftPricingOverlay gives this customer a negotiated price instead of
// the product's, from a day — or clears one, putting the account back on the
// product.
//
// pricing nil means FLOAT, not free. An account cleared back onto its product
// pays whatever the product costs by then, not what it cost when the overlay was
// set; a genuinely interest-free account is a zero-rate overlay, which is a
// different and deliberate statement.
//
// This is where retroactivity lives. A backdated overlay moves interest already
// charged to ONE customer, and the delta is posted rather than the history
// rewritten. The catalogue refuses the same thing (product.ErrRetroactivePublish)
// precisely because its blast radius is every account on the product.
//
// Returns ErrAccountNotFound, ErrAccountClosed, ErrInvalidRate, and
// ErrTermsNotFound.
func (r *Register) SetOverdraftPricingOverlayTx(ctx context.Context, tx Tx, id AccountID, pricing *product.OverdraftPricing, effectiveFrom time.Time) (OverdraftTerms, error) {
	return r.appendTermsTx(ctx, tx, id, effectiveFrom, ledger.EventOverdraftPricingOverlaid,
		func(row *OverdraftTerms) {
			if pricing == nil {
				row.Pricing = nil
				return
			}
			// Copied, so a caller keeping its argument cannot rewrite a stored
			// row's price afterwards.
			p := *pricing
			row.Pricing = &p
		})
}

// ChangeProduct migrates an account onto another product, from a day.
//
// It is a forward-dated row like any other, which is the point: the days before
// it still resolve against the product that priced them, so "what did this
// account's product say on 15 July 2027?" survives a migration. A migration is
// not a rewrite.
//
// A negotiated overlay is carried forward untouched. A rate promised to a
// customer is a promise, and dropping it as a side effect of a migration would
// reprice them without a decision; clearing it is an explicit
// SetOverdraftPricingOverlay(nil, day) call, so each method changes one thing.
//
// Returns ErrAccountNotFound, ErrAccountClosed, ErrTermsNotFound, and the
// product refusals checkOpenableProductTx makes.
func (r *Register) ChangeProductTx(ctx context.Context, tx Tx, id AccountID, productID product.ID, effectiveFrom time.Time) (OverdraftTerms, error) {
	if err := r.checkOpenableProductTx(ctx, tx, productID); err != nil {
		return OverdraftTerms{}, err
	}
	return r.appendTermsTx(ctx, tx, id, effectiveFrom, ledger.EventAccountProductChanged,
		func(row *OverdraftTerms) { row.ProductID = productID })
}

// appendTermsTx is the one write the three setters share: read the row in force
// on the effective day, change the one thing this caller changes, and append the
// result as a new row.
//
// Carrying the in-force row forward is what makes each row a complete statement
// of the account's terms rather than a diff — which is what lets termsAt answer
// with one row and no accumulation, and what makes an out-of-order sequence of
// backdated changes mean something definite.
//
// It appends and never edits: the row it read stays exactly as it was, so a past
// day still re-derives at the terms that were in force on it.
func (r *Register) appendTermsTx(ctx context.Context, tx Tx, id AccountID, effectiveFrom time.Time, event string, change func(*OverdraftTerms)) (OverdraftTerms, error) {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return OverdraftTerms{}, err
	}
	if acct.Status == Closed {
		return OverdraftTerms{}, ErrAccountClosed
	}

	day := ledger.DayStart(effectiveFrom)
	row, err := tx.GetOverdraftTermsAsOf(ctx, r.bookID, id, day)
	if err != nil {
		return OverdraftTerms{}, err
	}
	row.EffectiveFrom = day
	row.CreatedAt = r.now()
	change(&row)

	if err := row.Validate(); err != nil {
		return OverdraftTerms{}, err
	}
	if err := tx.PutOverdraftTerms(ctx, r.bookID, row); err != nil {
		return OverdraftTerms{}, err
	}
	if err := r.appendAuditTx(ctx, tx, event, string(id), row); err != nil {
		return OverdraftTerms{}, err
	}
	return row, nil
}
```

Add the three plain wrappers (`SetOverdraftLimit`, `SetOverdraftPricingOverlay`, `ChangeProduct`), each a single `r.store.Update` around its `…Tx`, in the shape every other pair in the file uses.

Note what is gone: the receivable creation that used to hang off `rate > 0`. No setter knows the rate any more — a floating account's price is in the catalogue — so it moves to the accrual, in Step 6.

- [ ] **Step 6: Resolve in the accrual**

`accrueOverdraftAccountTx` gains a cache parameter and resolves per day:

```go
// versionCache is one accrual run's product timelines, loaded on first use and
// reused for every account after it.
//
// A book of ten thousand accounts on three products therefore does three reads
// for the whole run rather than ten thousand. It is passed in rather than held
// on the Register because a Register owns no state — the same reason its clock
// is a function and not a time.
type versionCache map[product.ID][]product.Version

// loadForTerms fills the cache with every product the given rows name.
func (r *Register) loadForTerms(ctx context.Context, tx Tx, rows []OverdraftTerms, cache versionCache) error {
	for _, row := range rows {
		if _, ok := cache[row.ProductID]; ok {
			continue
		}
		versions, err := tx.ListProductVersions(ctx, r.bookID, row.ProductID)
		if err != nil {
			return err
		}
		cache[row.ProductID] = versions
	}
	return nil
}

func (r *Register) accrueOverdraftAccountTx(ctx context.Context, tx Tx, acct Account, date time.Time, cache versionCache) error {
	if acct.Status == Closed {
		return nil
	}

	rows, err := tx.ListOverdraftTermsForAccount(ctx, r.bookID, acct.ID)
	if err != nil {
		return err
	}
	if err := r.loadForTerms(ctx, tx, rows, cache); err != nil {
		return err
	}
	if !anyPriced(rows, cache) {
		return nil
	}

	window := rows[0].EffectiveFrom

	// The advancement guard resolves on `date` because the day count is a terms
	// field and the conventions disagree about whether a window advanced — under
	// Thirty360 the 31st collapses onto the 30th. `date` is the convention the
	// customer's product is on for the day being accrued.
	current, err := Resolve(rows, cache, date)
	if errors.Is(err, ErrTermsNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Pricing.DayCount.Days(acct.LastAccrualDate, date) <= 0 {
		return nil
	}

	series, err := r.gl.SeriesTx(ctx, tx, acct.GLAccount, window, date)
	if err != nil {
		return err
	}

	// A Period cannot return an error, so a resolution failure inside the walk
	// is captured and checked before anything is applied. It must abort the run
	// rather than accrue zero for the day: a tampered version and a free day are
	// not the same event, and posting the second for the first would hide it.
	var resolveErr error
	next, delta := interest.Recompute(series, window, date,
		interest.State{Accrued: acct.Accrued, Gross: acct.AccruedGross},
		func(balance ledger.Amount, from, to time.Time) interest.Accrued {
			day, err := Resolve(rows, cache, from)
			if err != nil {
				if !errors.Is(err, ErrTermsNotFound) && resolveErr == nil {
					resolveErr = err
				}
				return 0
			}
			return overdraftAccrual(balance, day, from, to)
		})
	if resolveErr != nil {
		return resolveErr
	}

	acct.Accrued = next.Accrued
	... // unchanged from here
```

and, immediately before `interestIncomeTx` in the `delta != 0` path:

```go
	// The receivable is created on the first day that actually accrues, not when
	// a rate is set: a floating account's rate lives in the catalogue, so no
	// register call knows it any more. It is reused forever afterwards, including
	// when the rate goes back to zero, because the account may already hold
	// accrued interest and discarding the receivable would strand it.
	if acct.InterestGL == "" {
		receivable, err := r.ensureReceivableTx(ctx, tx, acct)
		if err != nil {
			return err
		}
		acct.InterestGL = receivable
	}
```

`overdraftAccrual` takes the resolved terms:

```go
func overdraftAccrual(book ledger.Amount, t EffectiveTerms, from, to time.Time) interest.Accrued {
	drawn := -book
	if drawn <= 0 {
		return 0
	}
	arranged := drawn
	if arranged > t.Limit {
		arranged = t.Limit
	}
	total := interest.Accrue(arranged, t.Pricing.Rate, t.Pricing.DayCount, from, to)
	if excess := drawn - arranged; excess > 0 {
		rate := t.Pricing.UnarrangedRate
		if rate == 0 {
			rate = t.Pricing.Rate
		}
		total += interest.Accrue(excess, rate, t.Pricing.DayCount, from, to)
	}
	return total
}
```

Both callers pass a cache: `AccrueOverdraftTx` passes a fresh `versionCache{}` (one account, so there is nothing to share), and `RunEndOfDayTx` builds one before its loop and passes the same one to every account — which is the whole reason the type exists.

- [ ] **Step 7: Resolve in the read methods**

`AccountWithTerms.Terms` becomes `EffectiveTerms`, and `GetAccountWithTerms`/`ListAccountsWithTerms` resolve instead of reading one row. `OverdraftTermsHistory` keeps returning raw rows — the history is the rows, and an overlay that was later cleared is exactly what a reader wants to see there. Add one sentence to each doc comment saying which of the two it is. `balanceTx` needs **no change**: it reads the limit, the limit is on the account's own row, and `GetOverdraftTermsAsOf` still answers it in one read with no catalogue lookup — which is the dividend from pinning the limit and is worth a sentence in its comment.

- [ ] **Step 8: Thread the product through `payment`**

`payment.Participant` gains two fields:

```go
	// ProductID is the catalogue entry OpenCustomerAccount opens accounts from,
	// configured per participant exactly as CustomerSubledger is. A bank's
	// onboarding has to name a product, because every account is opened from one.
	ProductID product.ID

	// Catalogue is a live handle like Ledger and Deposit, bound by Network.bind.
	Catalogue *product.Catalogue `json:"-"`
```

`payment/store.go` gains a `productView` beside `depositView` (identical shape, `var _ product.Store = productView{}`), `payment.Network` gains a `products productView` field set in `NewNetwork`, and `bind` gains:

```go
	p.Catalogue = product.NewCatalogue(s.products, p.Ledger, p.BookID, s.clock)
```

`OpenCustomerAccount` passes the participant's product:

```go
	return p.Deposit.OpenAccount(ctx, p.CustomerSubledger, name, asset, p.ProductID, 0)
```

`payment.Tx` needs no change: it embeds `deposit.Tx`, which now embeds `product.Tx`. Where `AddParticipant` builds the record, give it a product: create the participant's default product and publish its first version in the same unit of work, or take a `product.ID` as a parameter — read `AddParticipantTx` and choose whichever fits its existing shape, then say which in the commit message.

- [ ] **Step 9: Update the api**

- `openDepositAccountRequest` gains `ProductID string \`json:"productId"\``; the handler refuses an empty one with `writeBadRequest(w, "productId is required")` before calling the register.
- The overdraft-terms route splits: `POST …/overdraft-limit`, `POST …/overdraft-pricing` (a null `pricing` clears the overlay), `POST …/product`. Each takes an `effectiveFrom` like the terms route already does.
- `depositAccountDTO` carries `productId`, and its pricing fields come from `EffectiveTerms` with a `pricingSource` of `"product"` or `"negotiated"` — a client that cannot tell a floating rate from a promised one cannot show a customer why theirs did not move.
- The terms-history DTO gains `productId` and `floating: true` for a row with no overlay.
- `api/errors.go` maps the new sentinels: `product.ErrProductNotFound` → 404; `product.ErrProductRetired`, `product.ErrKindMismatch`, `product.ErrVersionPublished`, `product.ErrRetroactivePublish`, `deposit.ErrProductRequired` → 422 (well-formed requests against state that refuses them, the `ErrWrongFacilityKind` category); `product.ErrVersionNotFound` → 404; `product.ErrInvalidRate` → 400 beside `deposit.ErrInvalidRate`; `product.ErrHashMismatch` → **500**, because it means stored data is corrupt rather than that the caller did anything wrong.
- `web/src/lib/types.ts` and the two forms/pages that read `overdraftRate`/`overdraftLimit`/`dayCount` follow the DTO: `web/src/app/participants/[pid]/deposit-accounts/[did]/page.tsx` and `web/src/components/forms/open-deposit-account-form.tsx` (the form needs a product select fed by the catalogue list endpoint from Task 6 — until then, a text field is acceptable and Task 6 replaces it).

- [ ] **Step 10: Update the seed**

`seed/seed.go`: build a catalogue before opening anything. The minimum that compiles and runs — Task 7 tells the fuller story:

```go
// products defines each bank's catalogue. Every account is opened from a
// product, so this runs before any of them.
func (b *builder) products(p *payment.Participant) product.ID {
	basic := must(p.Catalogue.CreateProduct(b.ctx, "Basic Current Account", product.CurrentAccount))
	from := ledger.DayStart(b.clock.now())
	must(p.Catalogue.DraftVersion(b.ctx, basic.ID, from,
		product.OverdraftPricing{Rate: 150_000, UnarrangedRate: 350_000, DayCount: interest.ACT365}))
	must(p.Catalogue.PublishVersion(b.ctx, basic.ID, from))
	return basic.ID
}
```

`open`/`openOverdraft` pass it, and `lendingShowcase`'s `SetOverdraftTerms` call at `seed/seed.go:400` becomes what it always meant — a negotiated rate for one customer:

```go
	// Bruno's rate is negotiated rather than the product's: 15% arranged, 35%
	// beyond the limit, for him alone. It is an overlay on his own timeline, so
	// a later product-wide reprice will not move it.
	must(verde.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 150_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		b.clock.now()))
	must(verde.Deposit.SetOverdraftLimit(ctx, bruno.ID, 50_000, b.clock.now()))
```

The comment at `seed/seed.go:436` about `TermsEffectiveFrom` is already stale after the terms work; rewrite it to say the window opens at inception.

- [ ] **Step 11: Run everything**

Run: `go build ./... && go test ./... && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./...`
Expected: PASS. Interest figures in `seed/seed_test.go` and `api/server_test.go` will have moved — re-derive them from the timeline (`expectedFromTimeline`) rather than copying whatever the code now prints, which is the rule the terms plan set for the same reason.

Then the web app: `cd web && npm run test && npm run build`, and load a page — a `[[wiki-link]]` to a hint key that does not exist yet takes every dev route down while leaving the build green.

- [ ] **Step 12: Commit**

```bash
git add deposit payment api seed ledger web/src/lib/types.ts web/src/app web/src/components
git commit -m "feat(deposit): open accounts from a product and resolve per day

OpenAccount takes a product.ID and writes a floating opening row, so a published
version reprices every account bound to it. SetOverdraftTerms splits into
SetOverdraftLimit (pinned, per customer), SetOverdraftPricingOverlay (negotiated,
and where retroactivity lives) and ChangeProduct (a forward-dated migration that
leaves earlier days priced by the product that priced them).

The accrual resolves each day against both timelines, loading product versions
once per distinct product per run, and a version whose hash does not verify
aborts the run rather than accruing zero. The receivable now appears on the first
day that actually accrues, because no register call knows the rate any more.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: The catalogue over HTTP

**Files:**
- Create: `api/dto_product.go`, `api/handlers_product.go`
- Modify: `api/server.go`, `api/server_test.go`
- Modify: `web/src/lib/types.ts`, `web/src/components/forms/open-deposit-account-form.tsx`

**Interfaces:**
- Consumes: Task 3's `product.Catalogue`, Task 5's `payment.Participant.Catalogue`.
- Produces: `GET|POST /participants/{pid}/products`, `GET /participants/{pid}/products/{prid}`, `GET|POST /participants/{pid}/products/{prid}/versions`, `POST /participants/{pid}/products/{prid}/versions/{day}/publish`, `POST /participants/{pid}/products/{prid}/retire`.

- [ ] **Step 1: Write the failing test**

Add to `api/server_test.go`, following the shape of the existing route tests there (they drive a live `*Server` over `httptest`):

```go
// The catalogue over the wire, end to end: create, draft, publish, open an
// account from it, and see the resolved rate on the account.
func TestProductCatalogueRoutes(t *testing.T) {
	srv, _ := newTestServer(t) // whatever the file's helper is called
	pid := seedParticipant(t, srv)

	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	postJSON(t, srv, "/participants/"+pid+"/products", map[string]any{
		"name": "Basic Current Account", "kind": "CurrentAccount",
	}, http.StatusCreated, &created)
	if created.ID == "" {
		t.Fatal("no product id")
	}

	// A draft prices nothing, so an account cannot be opened yet.
	postJSON(t, srv, "/participants/"+pid+"/products/"+created.ID+"/versions", map[string]any{
		"effectiveFrom": "2026-07-30T00:00:00Z",
		"rate":          120_000,
		"dayCount":      "ACT365",
	}, http.StatusCreated, nil)
	postJSON(t, srv, "/participants/"+pid+"/deposit-accounts", map[string]any{
		"name": "Bruno", "asset": "EUR", "productId": created.ID,
	}, http.StatusNotFound, nil) // product.ErrVersionNotFound

	postJSON(t, srv, "/participants/"+pid+"/products/"+created.ID+"/versions/2026-07-30/publish",
		nil, http.StatusOK, nil)

	// Republishing is refused, and the status says "state, not syntax".
	postJSON(t, srv, "/participants/"+pid+"/products/"+created.ID+"/versions/2026-07-30/publish",
		nil, http.StatusUnprocessableEntity, nil)

	var acct struct {
		ID            string `json:"id"`
		ProductID     string `json:"productId"`
		OverdraftRate int64  `json:"overdraftRate"`
		PricingSource string `json:"pricingSource"`
	}
	postJSON(t, srv, "/participants/"+pid+"/deposit-accounts", map[string]any{
		"name": "Bruno", "asset": "EUR", "productId": created.ID, "overdraftLimit": 50_000,
	}, http.StatusCreated, &acct)
	if acct.ProductID != created.ID {
		t.Errorf("productId = %q, want %q", acct.ProductID, created.ID)
	}
	if acct.OverdraftRate != 120_000 || acct.PricingSource != "product" {
		t.Errorf("rate = %d from %q, want 120000 from the product", acct.OverdraftRate, acct.PricingSource)
	}

	// A negotiated rate shows as such, which is the distinction a customer
	// service screen needs.
	postJSON(t, srv, "/participants/"+pid+"/deposit-accounts/"+acct.ID+"/overdraft-pricing", map[string]any{
		"effectiveFrom": "2026-07-30T00:00:00Z",
		"pricing":       map[string]any{"rate": 90_000, "dayCount": "ACT365"},
	}, http.StatusOK, &acct)
	if acct.OverdraftRate != 90_000 || acct.PricingSource != "negotiated" {
		t.Errorf("rate = %d from %q, want 90000 negotiated", acct.OverdraftRate, acct.PricingSource)
	}

	// Retroactive publication is refused over the wire too.
	postJSON(t, srv, "/participants/"+pid+"/products/"+created.ID+"/versions", map[string]any{
		"effectiveFrom": "2020-01-01T00:00:00Z", "rate": 1, "dayCount": "ACT365",
	}, http.StatusCreated, nil)
	postJSON(t, srv, "/participants/"+pid+"/products/"+created.ID+"/versions/2020-01-01/publish",
		nil, http.StatusUnprocessableEntity, nil)
}
```

Adapt the helper names to whatever `api/server_test.go` already uses; do not introduce a second style of test helper in that file.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./api/ -run TestProductCatalogueRoutes`
Expected: FAIL — 404 from the mux on `/products`.

- [ ] **Step 3: Write the DTOs**

`api/dto_product.go`, following `dto_lending.go`'s conventions exactly — rates cross the wire as millionths with `rateScale` beside them, and enums cross as strings parsed on the way in:

```go
// productDTO is a catalogue entry. Kind crosses as a string for the reason
// facilityDTO's does: an integer whose meaning a client learns from
// documentation is an integer a client renders wrong.
type productDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Retired   bool      `json:"retired"`
	CreatedAt time.Time `json:"createdAt"`
}

// productVersionDTO carries the publication state explicitly rather than
// leaving a client to infer it from a zero timestamp, and carries the hash
// because a client showing an operator "this is what was published" should be
// able to show what it is pinned to.
type productVersionDTO struct {
	ProductID      string     `json:"productId"`
	EffectiveFrom  time.Time  `json:"effectiveFrom"`
	Rate           int64      `json:"rate"`
	UnarrangedRate int64      `json:"unarrangedRate"`
	RateScale      int64      `json:"rateScale"`
	DayCount       string     `json:"dayCount"`
	Published      bool       `json:"published"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	Hash           string     `json:"hash"`
	CreatedAt      time.Time  `json:"createdAt"`
}
```

plus `toProductDTO`, `toProductVersionDTO`, a `createProductRequest{Name, Kind}`, a `draftVersionRequest{EffectiveFrom, Rate, UnarrangedRate, DayCount}`, and `kindFromString` beside `dayCountFromString` (`api/dto_lending.go:348`) so an unknown kind is a 400 rather than a silent `CurrentAccount`.

- [ ] **Step 4: Write the handlers and register the routes**

`api/handlers_product.go` with a `registerProductRoutes(mux *http.ServeMux)` called from `server.go` beside `registerDepositRoutes`. The publish route takes the effective day in the path as an ISO day (`{day}`), parsed with `time.Parse("2006-01-02", …)`, because that is the version's identity and a body carrying it would let a client publish a different version from the one the URL names.

- [ ] **Step 5: Run the api tests**

Run: `go test ./api/`
Expected: PASS.

- [ ] **Step 6: Feed the web form from the catalogue**

`web/src/lib/types.ts` gains `Product` and `ProductVersion`; `open-deposit-account-form.tsx`'s product field becomes a select populated from `GET /participants/{pid}/products`, filtering out `retired` entries — a form that offers a withdrawn product produces a 422 the user cannot act on.

Run: `cd web && npm run test && npm run build`

- [ ] **Step 7: Commit**

```bash
git add api web
git commit -m "feat(api): expose the product catalogue

Create, list, draft, publish and retire over HTTP, with the version's effective
day in the publish path because that day is the version's identity. The deposit
account DTO gains productId and a pricingSource of product|negotiated, because a
client that cannot tell a floating rate from a promised one cannot show a
customer why theirs did not move.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Seed a repriced product and a migrated account

The seed is the web app's data and the README's worked example. After this task it demonstrates the three things the catalogue added, so that a reader can see them without writing a test.

**Files:**
- Modify: `seed/seed.go`, `seed/seed_test.go`

**Interfaces:**
- Consumes: Tasks 5 and 6.
- Produces: no new API. Seeded state: two products per bank, a published mid-story reprice, one account migrated between products, and one account with a negotiated overlay.

- [ ] **Step 1: Extend the catalogue the seed builds**

Replace Task 5's minimal `products` helper with two products and a scheduled reprice:

```go
// products defines a bank's catalogue: a Basic Current Account every customer is
// opened on, and a Premium one an account can be migrated to.
//
// Basic is repriced part-way through the story — a second published version,
// effective from a day the seed later runs past. That is the case no per-account
// write can produce and the reason the catalogue exists: one row moves the price
// for every account bound to it, and the accounts' own rows are untouched.
func (b *builder) products(p *payment.Participant) catalogue {
	from := ledger.DayStart(b.clock.now())

	basic := must(p.Catalogue.CreateProduct(b.ctx, "Basic Current Account", product.CurrentAccount))
	b.publish(p, basic.ID, from, product.OverdraftPricing{
		Rate: 120_000, UnarrangedRate: 350_000, DayCount: interest.ACT365,
	})
	// The reprice: 12% to 14.9% from day 30. Published now, effective then —
	// forward-only, which is the only direction PublishVersion allows.
	b.publish(p, basic.ID, from.AddDate(0, 0, 30), product.OverdraftPricing{
		Rate: 149_000, UnarrangedRate: 350_000, DayCount: interest.ACT365,
	})

	premium := must(p.Catalogue.CreateProduct(b.ctx, "Premium Current Account", product.CurrentAccount))
	b.publish(p, premium.ID, from, product.OverdraftPricing{
		Rate: 70_000, UnarrangedRate: 250_000, DayCount: interest.ACT365,
	})

	return catalogue{basic: basic.ID, premium: premium.ID}
}

// catalogue is the two product IDs the rest of the seed needs.
type catalogue struct{ basic, premium product.ID }

// publish drafts and publishes in one step, which is what every seeded version
// wants: the draft state is a thing an operator passes through, not a thing the
// demo data should sit in.
func (b *builder) publish(p *payment.Participant, id product.ID, from time.Time, pricing product.OverdraftPricing) {
	must(p.Catalogue.DraftVersion(b.ctx, id, from, pricing))
	must(p.Catalogue.PublishVersion(b.ctx, id, from))
}
```

- [ ] **Step 2: Migrate one account, and leave one negotiated**

In `lendingShowcase`, beside Bruno's overlay from Task 5:

```go
	// Bella is migrated onto Premium part-way through, effective a fortnight in.
	// Her earlier days keep pricing at Basic's rate — a migration is a
	// forward-dated row, not a rewrite — which is what the deposit page's terms
	// history shows.
	must(verde.Deposit.ChangeProduct(ctx, bella.ID, b.cat(verde).premium, b.clock.now().AddDate(0, 0, 14)))
```

Thread the per-participant catalogue IDs however `builder` already threads per-participant state (a map keyed by `ParticipantID` beside `b.ibans` is the existing idiom). Bruno keeps his negotiated overlay, so the seeded data holds all three cases side by side: floating through a reprice (Alice), negotiated and therefore unmoved by it (Bruno), and migrated (Bella).

- [ ] **Step 3: Assert the story in the seed test**

`seed/seed_test.go` gains one assertion per case — that Alice's resolved rate is the repriced one, Bruno's is his negotiated one, and Bella's is Premium's. Compute expected interest with the same day-by-day helper the deposit tests use; do not copy figures out of a run.

- [ ] **Step 4: Run it**

Run: `go test ./seed/ ./api/ && make dev` (or `make run`) and load the deposit pages for all three customers.
Expected: PASS, and three visibly different rates with the same limit story as before.

- [ ] **Step 5: Commit**

```bash
git add seed
git commit -m "feat(seed): demonstrate a product reprice, an overlay and a migration

Basic Current Account is repriced from day 30 by one published version, which
moves Alice's rate and leaves Bruno's negotiated one alone; Bella is migrated to
Premium a fortnight in, and her earlier days still price at Basic. The three
cases sit side by side in the seeded data so the behaviour is visible in the web
app without writing a test.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: The domain claims, across every layer

`CLAUDE.md`'s rule: the banking content is duplicated by design across the README, the hint content, the quiz and the schema comments, and a schema change is a documentation change. The schema comments landed with their tasks; this task does the other three, plus the comparison document this work came from.

**Files:**
- Modify: `README.md`, `web/src/components/hint-content.ts`
- Modify: `web/src/lib/quiz/chapters/08-account-lifecycle-and-overdraft.ts`, `web/src/lib/quiz/chapters/18-interest-overdrafts-and-arrears.ts`
- Modify: `docs/cbs-vs-book.md`

**Interfaces:**
- Consumes: everything.
- Produces: no code. Three new hint keys, quiz coverage, and item 4.3 moved from open to partly closed.

- [ ] **Step 1: Add the hint keys first**

`web/src/components/hint-content.ts` gains three entries, before anything links to them — a `[[wiki-link]]` to a missing key throws under `RootLayout` and takes every dev route down while `next build` stays green:

- `product-catalogue` — a product is a named catalogue entry with an effective-dated timeline of immutable published versions; accounts are opened *from* one; a draft prices nothing; publication is forward-only.
- `pinned-vs-floating` — the rate floats with the product so one row reprices a whole book; the limit is pinned per account because it is an underwriting decision about one customer, and the catalogue type cannot express one. Link `[[overdraft|overdraft limit]]` and `[[overdraft-interest]]`.
- `pricing-overlay` — one customer's negotiated price instead of the product's; nil means float, not free; it is where a *backdated* reprice is allowed, because its blast radius is one customer.

Cross-link them from the existing `overdraft-interest` and `overdraft` bodies, and from `day-count` (the convention is a product parameter, which is now literally true of a `product.Version`).

- [ ] **Step 2: Rebalance the two quiz chapters**

Not an append: `diversity.test.ts` holds each chapter to 18–22 questions, ≥8 distinct `concept` tags, no tag more than 3×, and all three tiers. Add questions tagged `product-catalogue` and `pinned-vs-floating` and drop or retag the weakest existing questions to stay inside the limits. Content worth asking about:

- Which of these moves when the bank publishes a new version, and which does not (the limit does not).
- A customer with a negotiated rate: does a product reprice reach them? (No — until the overlay is cleared.)
- Why publication is forward-only, and what the retroactive tool is instead.
- What a draft version prices (nothing — the published version before it stays in force).
- An account migrated between products in April: what priced its March? (The old product.)

Run: `cd web && npm run test`

- [ ] **Step 3: README**

The overdraft sections gain the pinned/floating distinction and the sentence that makes it concrete — *a limit is an underwriting decision about one customer, a rate is a price the bank publishes* — and *Persistence* gains `products` and `product_versions` with the day-key argument for why there is no exclusion constraint. Say plainly that publication is forward-only and that this is deliberately less capable than a maker-checker regime would be, so the README does not over-claim.

- [ ] **Step 4: Close out the comparison item**

`docs/cbs-vs-book.md` item 4.3 (`:180`) moves from open to **partly closed**, with the reasons: effective-dated record done (the previous topic), per-parameter-group pinned/floating binding done, per-account overlays done, content hash done and *verified on read*; exclusion constraint replaced by the day key, resolution log deferred to checkpointing, maker-checker deferred as comparison item 17, and retroactive publication refused rather than controlled. Update the summary table row at the top of the file to match.

- [ ] **Step 5: Run the full gate**

Run: `go test ./... && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./... && cd web && npm run test && npm run build`
Expected: PASS. Then load a page in `make dev` and click a `[[product-catalogue]]` link — the runtime guard for missing keys only fires in the browser.

- [ ] **Step 6: Commit**

```bash
git add README.md web/src docs/cbs-vs-book.md
git commit -m "docs: teach the catalogue across every layer

Three hint keys (product catalogue, pinned vs floating, pricing overlay), quiz
coverage in chapters 8 and 18 rebalanced to stay inside the diversity limits, the
README's overdraft and persistence sections, and cbs-vs-book item 4.3 moved from
open to partly closed with each deferral named.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Failure modes to keep in view while implementing

| Failure | Symptom | The guard, and where it is |
| --- | --- | --- |
| A published version edited in the database | Past days re-derive at a price nobody published | `Version.VerifyHash` inside `product.VersionAt`, reached by every `Resolve`; the accrual returns the error rather than accruing zero (Task 5, Step 6) |
| `nil` pricing read as interest-free | A whole book silently free | `Resolve` has no zero-value fallback: a floating row with no version is `ErrVersionNotFound`. The nullable triple carries a `COMMENT ON COLUMN` saying so (Task 4) |
| A mixed nullable triple | An account priced from two sources on one day | `OverdraftTerms.Validate`, and `scanOverdraftTerms` builds an overlay only when all three are present (Task 4) |
| A limit that floats | A published version changes what a customer may spend | `product.OverdraftPricing` has no limit field, so it cannot be expressed; `TestPublishingAVersionCannotMoveTheAvailableBalance` pins it (Task 5) |
| A retired product unprices its accounts | Accrual stops silently for everyone on a withdrawn product | `Retired` is checked in `checkOpenableProductTx` only, never in resolution (Task 5, Step 4) |
| A cached `ProductID` on `Account` | Contradicts the timeline after a future-dated `ChangeProduct` | The binding is on the terms row; `Account` gains no column (Task 4) |
| A single version slice across a migration | Pre-migration days priced by the new product | `Resolve` takes a map keyed by product; `TestResolveAcrossAProductMigration` pins it (Task 4) |
| A per-account product read in the nightly batch | Ten thousand reads where three would do | `versionCache`, built once in `RunEndOfDayTx` and passed to every account (Task 5, Step 6) |
| A catalogue read on the withdrawal path | Every `CheckWithdrawal` pays for a product lookup | `balanceTx` reads the limit, which is on the account's own row — unchanged, and worth a comment saying why (Task 5, Step 7) |
| A store that refuses a published write | `store/pg` and `store/mem` diverge | The freeze is in `Catalogue.DraftVersion`; `RunProduct` asserts both stores accept the write (Task 2) |
