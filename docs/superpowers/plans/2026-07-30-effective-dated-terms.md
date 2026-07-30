# Effective-Dated Product Terms — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make product terms an effective-dated timeline resolved per accrual day, so the recompute window opens at account inception and a backdated posting trues up interest across a repricing.

**Architecture:** Terms stop being mutable columns on `deposit.Account` / `lending.Facility` and become rows —
one `(account, effective_from)` record per repricing, never overwritten, reached through a
`Put`/`List`/`GetAsOf` trio on the store, exactly as instalments, holds and snapshots already are.
Accrual loads a facility's or account's whole timeline once per run and resolves it inside the
`interest.Period` closure, which `interest.Recompute` already invokes one day at a time — so the
`interest` package is unchanged. Because every day can now be re-derived at the terms actually in
force on it, the recompute window opens at inception and the window-reset machinery in
`SetOverdraftTermsTx`, `DisburseTx` and `DrawTx` deletes itself.

**Tech Stack:** Go 1.x (no new dependencies), Postgres via pgx v5 (`store/pg`), maps+mutex
(`store/mem`), Next.js 16 / React 19 / TanStack Query (`web/`), vitest for the web suite.

## Global Constraints

Every task's requirements implicitly include this section.

- **Both store runs must stay green.** `go test ./...` with no `DATABASE_URL` (runs on `store/mem`)
  and `TEST_DATABASE_URL=… go test ./...` (or `make test-pg`, runs the same suites against
  `store/pg`). A change that passes only one has, by definition, made the two stores diverge.
- **`store/pg` must never accept or refuse a write `store/mem` performs.** `store/storetest` is the
  conformance suite that enforces it; new entities go in it.
- **One migration.** `store/pg/schema/0001_init.sql` is edited in place. No `0002_*.sql`. No
  database is deployed, so there is nothing to migrate forward.
- **Schema comments are domain content.** New tables and columns carry `COMMENT ON COLUMN` for
  anything a schema dump would not show: why the primary key is composite, why the day is a
  truncated timestamp plus a text day key, what `created_at` beside `effective_from` means, and why
  there is no `CHECK` on the rate or day-count columns.
- **Terms are rows, never `jsonb`.** A slice on the row is cheaper and is explicitly rejected: it
  puts an opaque blob where the schema is meant to teach a relational mapping.
- **Day truncation stays in Go.** Callers pass an already-`ledger.DayStart`ed instant; neither store
  decides what a day is. Two stores that each decide are one DST-adjacent edge case away from
  disagreeing.
- **`interest/` is not modified.** `Recompute` already applies the `Period` a day at a time via
  `perDay`; terms resolution is a change to a closure. If a task finds itself editing `interest/`,
  stop and re-read the spec's *Accrual* section.
- **Domain knowledge stays consistent across layers** (`CLAUDE.md`): `README.md`,
  `web/src/components/hint-content.ts`, `web/src/lib/quiz/chapters/*.ts` and
  `store/pg/schema/0001_init.sql` all carry the claims that move. Task 8 closes this out; earlier
  tasks that change a schema comment do it in the same commit as the schema change.
- **Two mechanical web rules the Go build will not catch:** a `[[wiki-link]]` to a key not in
  `hint-content.ts` throws under `RootLayout` and takes every dev route down (guarded on
  `NODE_ENV !== "production"`, so `next build` stays green) — `npm run test` catches it in hint
  bodies *and* quiz explanations; and `web/src/lib/quiz/diversity.test.ts` holds each chapter to
  18–22 questions, ≥8 distinct `concept` tags, no tag more than 3×, all three difficulty tiers.
- **Out of scope, deliberately** (do not build these; the spec records why): checkpointing;
  `Method`/`TermMonths`/`MinPayment` staying mutable columns; a product catalogue with versions,
  content hashes, exclusion constraints or overlays; maker-checker on a retroactive repricing;
  deposit credit interest.
- **`BusinessDate` is not a precondition.** The architecture review's candidate 5 would let
  `EffectiveFrom` be a type that cannot be constructed wrong. It is not built here; every date
  parameter is a `time.Time` documented as day-granular, UTC-normalised, compared by value.
- **Do not interleave with candidate 2 phase B** (value-dating capitalisation to the next day). Both
  change interest figures; whichever lands second pays to re-derive the other's expected numbers.
  Acceptance-test figures here are computed from the timeline, never copied from current seed output.

---

## File Structure

**New files**

| File | Responsibility |
|---|---|
| `deposit/terms.go` | `OverdraftTerms`, `TermsDayKey`, `Validate`, `termsAt`, `anyPriced` |
| `deposit/terms_test.go` | unit tests for the resolver and validation |
| `lending/terms.go` | `FacilityTerms`, `TermsDayKey`, `Validate`, `termsAt`, `anyPriced` |
| `lending/terms_test.go` | the mirror unit tests |
| `web/src/components/overdraft-terms-card.tsx` | the read-only timeline view |

**A note on test packages, because this directory is not uniform.** `deposit/register_test.go` and
`deposit/list_test.go` are `package deposit_test` with a **dot import** of `deposit`, so their
bodies read as though in-package; `deposit/export_test.go` (`package deposit`) re-exports the one
unexported thing they need. `lending/accrual_test.go`, `portfolio_test.go`, `repay_test.go`,
`refund_test.go` and `arrears_test.go` are `package lending_test` with an ordinary qualified import,
while `lending/schedule_test.go` is **`package lending`** — in-package, because it tests pure
functions and needs no store. The two new `terms_test.go` files follow `schedule_test.go`: they are
in-package (`package deposit` / `package lending`), because they test pure functions too, so they
call `termsAt` and `anyPriced` directly and **no `export_test.go` change is needed**. Note that
`lending/schedule_test.go` already defines a package-level `func day(y int, m time.Month, d int)`,
which `lending/terms_test.go` reuses rather than redeclares.

**Modified files** — what each one gains

| File | Change |
|---|---|
| `deposit/types.go` | `Account` loses `OverdraftLimit`, `Rate`, `UnarrangedRate`, `DayCount`, `TermsEffectiveFrom`; `AccruedGross` and `LastAccrualDate` doc comments change |
| `deposit/store.go` | three new `Tx` methods, contract notes |
| `deposit/errors.go` | `ErrTermsNotFound` |
| `deposit/export_test.go` | re-exports `termsAt` |
| `deposit/register.go` | opening row; `SetOverdraftTermsTx` appends; window dance deleted; `balanceTx` resolves; accrual resolves per day; `overdraftAccrual` takes terms; three new read methods |
| `lending/types.go` | `Facility` loses `Rate`, `DayCount`, `TermsEffectiveFrom`; comment changes |
| `lending/store.go`, `lending/errors.go` | mirror of deposit, plus `ErrScheduleWouldDiverge` |
| `lending/portfolio.go` | opening row in `openTx`; clamps and window reopens deleted; `SetFacilityTerms` |
| `lending/accrual.go` | accrual resolves per day |
| `lending/schedule.go` | `BuildSchedule` takes an explicit rate |
| `ledger/audit.go` | `EventFacilityTermsSet` |
| `store/mem/mem.go`, `tx_deposit.go`, `tx_lending.go` | two new maps, two row kinds, six methods |
| `store/pg/pg.go`, `tx_deposit.go`, `tx_lending.go` | two tables in `tables`, six methods, column removals |
| `store/pg/schema/0001_init.sql` | two tables with comments; seven columns and their comments dropped |
| `store/storetest/deposit.go`, `lending.go`, `storetest.go` | conformance for both entities; `Reset` covers them |
| `api/dto_deposit.go`, `handlers_deposit.go`, `dto_lending.go`, `handlers_lending.go` | resolved-as-of-now DTOs, `effectiveFrom` on the request, `GET …/overdraft-terms` |
| `seed/seed.go` | effective date on the call, a second row, the stale comment |
| `web/src/lib/types.ts`, `api/endpoints.ts`, `api/query-keys.ts`, `api/hooks.ts` | the terms timeline |
| `web/src/app/participants/[pid]/deposit-accounts/[did]/page.tsx` | mounts the card |
| `README.md`, `web/src/components/hint-content.ts`, `web/src/lib/quiz/chapters/{08,18}-*.ts` | the domain claims that move |

---

### Task 1: The deposit terms value type and its resolver

Pure Go, no store, no register. Everything downstream depends on these names.

**Files:**
- Create: `deposit/terms.go`
- Create: `deposit/terms_test.go`
- Modify: `deposit/errors.go`

**Interfaces:**
- Consumes: `interest.Rate`, `interest.DayCount`, `ledger.Amount`, `ledger.DayStart` — all existing.
- Produces:
  - `deposit.OverdraftTerms` struct with fields `AccountID AccountID`, `EffectiveFrom time.Time`,
    `OverdraftLimit ledger.Amount`, `Rate interest.Rate`, `UnarrangedRate interest.Rate`,
    `DayCount interest.DayCount`, `CreatedAt time.Time`
  - `func (t OverdraftTerms) Validate() error`
  - `func TermsDayKey(day time.Time) string`
  - `func termsAt(rows []OverdraftTerms, day time.Time) (OverdraftTerms, bool)` — unexported, and it
    stays that way: nothing outside this package resolves a timeline, because callers ask the store,
    which is bounded. `deposit/terms_test.go` is in-package and calls it directly.
  - `func anyPriced(rows []OverdraftTerms) bool` — likewise unexported
  - `deposit.ErrTermsNotFound`

- [ ] **Step 1: Write the failing tests**

Create `deposit/terms_test.go`. It is **in-package** (`package deposit`), following
`lending/schedule_test.go`: it tests pure functions, builds no store, and so has none of the import
cycle that pushed `register_test.go` out into `deposit_test`. That is also what lets it call the
unexported `termsAt` and `anyPriced` without an `export_test.go` re-export.

```go
package deposit

import (
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
)

func termsDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func sampleTerms(from time.Time, rate interest.Rate) OverdraftTerms {
	return OverdraftTerms{
		AccountID: "dep_1", EffectiveFrom: from, OverdraftLimit: 50_000,
		Rate: rate, DayCount: interest.ACT365,
	}
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
		if ok && got.Rate != c.want {
			t.Errorf("%s: rate = %d, want %d", c.name, got.Rate, c.want)
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
	if !ok || got.Rate != 200_000 {
		t.Errorf("a second into the effective day: got %d, %v", got.Rate, ok)
	}
}

func TestTermsAtOnASingleRow(t *testing.T) {
	rows := []OverdraftTerms{sampleTerms(termsDay(2025, time.January, 1), 100_000)}

	if _, ok := termsAt(rows, termsDay(2024, time.December, 31)); ok {
		t.Error("resolved a day before the only row")
	}
	got, ok := termsAt(rows, termsDay(2025, time.January, 1))
	if !ok || got.Rate != 100_000 {
		t.Errorf("on the boundary: got %d, %v", got.Rate, ok)
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
	if !ok || got.Rate != 250_000 {
		t.Errorf("mid-February: got %d, %v", got.Rate, ok)
	}
}

func TestAnyPriced(t *testing.T) {
	unpriced := []OverdraftTerms{
		sampleTerms(termsDay(2025, time.January, 1), 0),
		sampleTerms(termsDay(2025, time.March, 1), 0),
	}
	if anyPriced(unpriced) {
		t.Error("a timeline of zero rates reported as priced")
	}
	priced := append(append([]OverdraftTerms{}, unpriced...), sampleTerms(termsDay(2025, time.June, 1), 150_000))
	if !anyPriced(priced) {
		t.Error("a timeline ending in a non-zero rate reported as unpriced")
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
	// rendered this way: both stores compare it as a string and get a day
	// comparison for free.
	if !(TermsDayKey(termsDay(2025, time.February, 28)) < TermsDayKey(termsDay(2025, time.March, 1))) {
		t.Error("day keys do not sort lexicographically")
	}
}

func TestOverdraftTermsValidation(t *testing.T) {
	base := sampleTerms(termsDay(2025, time.January, 1), 150_000)

	negativeLimit := base
	negativeLimit.OverdraftLimit = -1
	if err := negativeLimit.Validate(); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("negative limit: got %v, want ErrInvalidAmount", err)
	}

	negativeRate := base
	negativeRate.Rate = -1
	if err := negativeRate.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("negative rate: got %v, want ErrInvalidRate", err)
	}

	negativeUnarranged := base
	negativeUnarranged.UnarrangedRate = -1
	if err := negativeUnarranged.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("negative unarranged rate: got %v, want ErrInvalidRate", err)
	}

	// The one combination that would price ONLY the excess, making the money
	// drawn beyond the limit dearer than nothing while leaving the facility
	// inside it free.
	excessOnly := base
	excessOnly.Rate = 0
	excessOnly.UnarrangedRate = 350_000
	if err := excessOnly.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("unarranged with no arranged: got %v, want ErrInvalidRate", err)
	}

	// A zero rate throughout is a real product: an interest-free overdraft.
	free := base
	free.Rate = 0
	free.UnarrangedRate = 0
	if err := free.Validate(); err != nil {
		t.Errorf("interest-free overdraft: got %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./deposit/ -run 'TestTerms|TestAnyPriced|TestOverdraftTermsValidation' -v`
Expected: FAIL to build — `undefined: OverdraftTerms`, `termsAt`, `anyPriced`, `TermsDayKey`, and
`ErrTermsNotFound` is not yet declared either.

- [ ] **Step 3: Add the sentinel**

In `deposit/errors.go`, inside the existing `var (…)` block, after `ErrSnapshotNotFound`:

```go
	// ErrTermsNotFound is returned when no overdraft terms are in force on a
	// day. Every account gets an opening terms row at OpenAccount, so the only
	// way to miss is to ask about a day before the account existed.
	ErrTermsNotFound = errors.New("no overdraft terms in force on that day")
```

- [ ] **Step 4: Write `deposit/terms.go`**

```go
package deposit

import (
	"sort"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// OverdraftTerms is what an account's arranged overdraft cost from one day
// onwards. It is a ROW, one per repricing, never overwritten — which is what
// makes "what did this account's product say on 15 July 2027?" a question with
// a stable answer, and what lets the accrual re-derive a past day at the terms
// that were actually in force on it.
//
// The four fields used to be mutable columns on Account. An accrual posted six
// months ago could not then be reproduced from stored state: the inputs it used
// were gone, overwritten by the current ones. Worse, it bounded the recompute
// window at the last repricing, so a backdated posting landing before it was
// silently never trued up.
//
// # Two dates, and what the pair means
//
// This is the booking-date/value-date distinction applied to configuration.
// CreatedAt is when the repricing was entered; EffectiveFrom is when it takes
// economic effect. They are different questions for exactly the reasons the
// README already gives for money, and both are kept.
//
// EffectiveFrom is day-granular: ledger.DayStart-truncated by the caller before
// it reaches a store, on the same day axis accrual already uses. Accrual
// iterates whole UTC days — interest.perDay is explicit about it — so terms
// changing part-way through a day would have no well-defined meaning: the day
// is the unit the arithmetic is expressed in.
type OverdraftTerms struct {
	AccountID     AccountID
	EffectiveFrom time.Time // day-truncated; the first day these terms apply

	// OverdraftLimit is the positive amount the balance may go below zero by;
	// 0 means none. It is here rather than on Account because the available
	// balance a customer was quoted last March is as much a fact about that
	// March as the rate they were charged.
	OverdraftLimit ledger.Amount
	// Rate is the annual rate on the drawn balance up to OverdraftLimit. Zero
	// makes the WHOLE overdraft interest-free, which is a real product.
	Rate interest.Rate
	// UnarrangedRate applies to any balance drawn beyond OverdraftLimit. It is
	// an optional SURCHARGE, not a switch: zero does not mean the excess is
	// free, it means Rate applies throughout. See Validate for the one
	// combination that is refused.
	UnarrangedRate interest.Rate
	DayCount       interest.DayCount

	CreatedAt time.Time // when the row was entered, not when it takes effect
}

// TermsDayKey is the day a terms row is identified by within its account.
//
// A terms row is identified by (account, day), so a second row entered for the
// same effective day replaces the first — which makes "the terms in force on
// day D" unique by construction rather than by a validation rule.
//
// Store implementations must derive the key of a row passed to
// PutOverdraftTerms with this function, so that GetOverdraftTermsAsOf finds it:
// mem uses it as a map key, pg as the value of the overdraft_terms.day_key
// column and as the column the as-of lookup compares on. It is the same
// discipline SnapshotDateKey follows, for the same reason — a lexicographically
// ordered ISO day is a total order the two stores cannot disagree about.
func TermsDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// Validate reports whether these terms are a product. The rules are the ones
// SetOverdraftTerms used to apply to its arguments, moved onto the row so that
// a store round trip and a register call are held to one standard.
//
// The refused combination is an unarranged rate with no arranged one: it would
// price only the excess, making the money drawn beyond the limit dearer than
// nothing while leaving the facility inside it free. That is not a product, it
// is a mistake.
func (t OverdraftTerms) Validate() error {
	if t.OverdraftLimit < 0 {
		return ErrInvalidAmount
	}
	if t.Rate < 0 || t.UnarrangedRate < 0 {
		return ErrInvalidRate
	}
	if t.Rate == 0 && t.UnarrangedRate > 0 {
		return ErrInvalidRate
	}
	return nil
}

// termsAt is the row in force on a day: the last one whose EffectiveFrom is not
// after it. rows must be ascending by EffectiveFrom, which is what
// ListOverdraftTermsForAccount returns.
//
// Binary search rather than a cursor advanced alongside the accrual walk. The
// runs and the days within them are both ascending today, so a cursor would
// work — and would silently break the first time anything called the Period
// closure out of order. Rows are loaded once per accrual run, so this is
// O(days log rows) of arithmetic on top of a walk that is already O(days), and
// no extra I/O at all.
//
// false means the day precedes the account's opening row, which for an account
// opened through OpenAccount means a day before the account existed.
func termsAt(rows []OverdraftTerms, day time.Time) (OverdraftTerms, bool) {
	d := ledger.DayStart(day)
	// The first index whose EffectiveFrom is strictly after d; the row in force
	// is the one before it.
	i := sort.Search(len(rows), func(i int) bool {
		return rows[i].EffectiveFrom.After(d)
	})
	if i == 0 {
		return OverdraftTerms{}, false
	}
	return rows[i-1], true
}

// anyPriced reports whether any row in the timeline carries a non-zero rate.
//
// It is what keeps a never-priced account from reading a value-dated series
// every night. A zero rate is now a property of a DAY rather than of an
// account, so the old `Rate <= 0` early return cannot survive as an
// account-level guard: an account unpriced for its first year and priced
// thereafter is a case the previous model could not express at all. Skipping
// the run entirely is only safe when NO day in the timeline is priced.
func anyPriced(rows []OverdraftTerms) bool {
	for _, r := range rows {
		if r.Rate > 0 {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./deposit/ -run 'TestTerms|TestAnyPriced|TestOverdraftTermsValidation' -v`
Expected: PASS, all eight tests.

Then `go build ./... && go vet ./... && gofmt -l .` — expected clean, with `gofmt -l` printing
nothing. Nothing consumes the new type yet, so `go test ./...` should also still pass.

- [ ] **Step 6: Commit**

```bash
git add deposit/terms.go deposit/terms_test.go deposit/errors.go
git commit -m "feat(deposit): add the effective-dated overdraft terms record

A row per repricing, never overwritten, plus the resolver that answers which
one was in force on a day. Nothing reads it yet."
```

---

### Task 2: Deposit terms storage — interface, both stores, conformance

**Files:**
- Modify: `deposit/store.go`
- Modify: `store/mem/mem.go` (state, clone, newState, row kind)
- Modify: `store/mem/tx_deposit.go`
- Modify: `store/pg/schema/0001_init.sql` (new table + comments)
- Modify: `store/pg/pg.go` (`tables`)
- Modify: `store/pg/tx_deposit.go`
- Modify: `store/storetest/deposit.go`
- Modify: `store/storetest/storetest.go` (the `Reset` coverage assertion, if the deposit reset
  subtest lives there — it is in `deposit.go`; check both and put the assertion where the existing
  hold/snapshot reset assertions are)

**Interfaces:**
- Consumes: `deposit.OverdraftTerms`, `deposit.TermsDayKey`, `deposit.ErrTermsNotFound` (Task 1).
- Produces, on `deposit.Tx`:
  - `PutOverdraftTerms(ctx context.Context, book ledger.BookID, t OverdraftTerms) error`
  - `ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]OverdraftTerms, error)`
  - `GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id AccountID, day time.Time) (OverdraftTerms, error)`

- [ ] **Step 1: Declare the three methods on the interface**

In `deposit/store.go`, inside `type Tx interface`, after the snapshot block:

```go
	// The account's effective-dated terms timeline. Three methods rather than
	// two, because the two callers want different things: accrual wants the
	// whole timeline in one read and resolves per day in Go, while balanceTx
	// wants exactly one row — the limit in force now — and should not pay for
	// history on a path that runs on every withdrawal check. A bounded lookup
	// as a store method is already this repo's idiom; ActiveHoldTotal and
	// ValueDateBalance are both aggregates with bounds, for the same reason.
	PutOverdraftTerms(ctx context.Context, book ledger.BookID, t OverdraftTerms) error
	ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]OverdraftTerms, error)
	GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id AccountID, day time.Time) (OverdraftTerms, error)
```

And append to the contract notes at the bottom of the file:

```go
//   - PutOverdraftTerms is an upsert keyed by (account, TermsDayKey(t.EffectiveFrom)),
//     the same identity PutSnapshot has. Two rows entered for the same account
//     and the same effective DAY are the same row, and the later one wins —
//     which is what makes "the terms in force on day D" unique by construction
//     rather than by a validation rule.
//   - ListOverdraftTermsForAccount orders by effective day ASCENDING, ties
//     broken by the row's insertion sequence for form. Ascending is not a
//     convenience: deposit.termsAt binary-searches the slice it is handed.
//   - GetOverdraftTermsAsOf returns the row with the greatest effective day not
//     after `day`, or ErrTermsNotFound when the day precedes the account's
//     first row. It is book- and account-scoped like everything else here, and
//     unlike ActiveHoldTotal it is NOT an aggregate: an unknown account has no
//     terms, which is ErrTermsNotFound rather than a zero row that would read
//     as a real interest-free product.
//   - The store truncates nothing. Callers pass an already-DayStart-ed instant
//     and both stores key on deposit.TermsDayKey of it.
```

- [ ] **Step 2: Write the conformance suite**

In `store/storetest/deposit.go`, add a new subtest inside `RunDeposit`, immediately after the
existing `OverdraftTermsRoundTrip` subtest:

```go
	// The terms timeline. Everything the accrual depends on is here: ordering
	// (termsAt binary-searches the slice List hands it), the day-granular
	// upsert identity, and the four positions the as-of lookup has to answer
	// for. A store that got any of them wrong would produce interest figures
	// nobody could reproduce, and no other subtest would notice.
	t.Run("OverdraftTermsTimeline", func(t *testing.T) {
		s := openDeposit(t, newStore)

		jan := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		mar := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
		jun := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

		row := func(from time.Time, rate interest.Rate) deposit.OverdraftTerms {
			return deposit.OverdraftTerms{
				AccountID: "dep_1", EffectiveFrom: from, OverdraftLimit: 50_000,
				Rate: rate, UnarrangedRate: rate * 2, DayCount: interest.Thirty360,
				CreatedAt: early,
			}
		}

		// Written out of order on purpose: the store owns the ordering, and a
		// caller may enter a backdated repricing at any time.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, r := range []deposit.OverdraftTerms{row(jun, 300_000), row(jan, 100_000), row(mar, 200_000)} {
				if err := tx.PutOverdraftTerms(ctx, bookA, r); err != nil {
					return err
				}
			}
			// A second book's rows must be invisible to the first.
			return tx.PutOverdraftTerms(ctx, bookB, deposit.OverdraftTerms{
				AccountID: "dep_1", EffectiveFrom: jan, Rate: 999_000, CreatedAt: early,
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "timeline length", len(rows), 3)
			assertEqual(t, "first row rate", rows[0].Rate, interest.Rate(100_000))
			assertEqual(t, "second row rate", rows[1].Rate, interest.Rate(200_000))
			assertEqual(t, "third row rate", rows[2].Rate, interest.Rate(300_000))
			for i := 1; i < len(rows); i++ {
				if !rows[i-1].EffectiveFrom.Before(rows[i].EffectiveFrom) {
					t.Fatalf("timeline not ascending at %d: %v then %v",
						i, rows[i-1].EffectiveFrom, rows[i].EffectiveFrom)
				}
			}
			// Every field round-trips, not just the rate: a dropped day count
			// is a product silently repriced onto another convention.
			assertEqual(t, "limit", rows[0].OverdraftLimit, ledger.Amount(50_000))
			assertEqual(t, "unarranged", rows[0].UnarrangedRate, interest.Rate(200_000))
			assertEqual(t, "day count", rows[0].DayCount, interest.Thirty360)
			assertEqual(t, "account id", string(rows[0].AccountID), "dep_1")
			if !rows[0].CreatedAt.Equal(early) {
				t.Errorf("created at: got %v, want %v", rows[0].CreatedAt, early)
			}
			if !rows[0].EffectiveFrom.Equal(jan) {
				t.Errorf("effective from: got %v, want %v", rows[0].EffectiveFrom, jan)
			}

			other, err := tx.ListOverdraftTermsForAccount(ctx, bookB, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "book-b timeline length", len(other), 1)
			assertEqual(t, "book-b rate is its own", other[0].Rate, interest.Rate(999_000))
			return nil
		})

		// The four as-of positions.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			_, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1", time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC))
			if !errors.Is(err, deposit.ErrTermsNotFound) {
				t.Errorf("before the first row: got %v, want ErrTermsNotFound", err)
			}

			onBoundary, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1", mar)
			if err != nil {
				return err
			}
			assertEqual(t, "on a boundary", onBoundary.Rate, interest.Rate(200_000))

			between, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1",
				time.Date(2025, 4, 15, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return err
			}
			assertEqual(t, "between rows takes the earlier", between.Rate, interest.Rate(200_000))

			after, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1",
				time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return err
			}
			assertEqual(t, "after the last row", after.Rate, interest.Rate(300_000))

			// An account with no rows at all is ErrTermsNotFound, not a zero
			// row that would read as a real interest-free product.
			if _, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_missing", mar); !errors.Is(err, deposit.ErrTermsNotFound) {
				t.Errorf("unknown account: got %v, want ErrTermsNotFound", err)
			}
			return nil
		})

		// Upsert on the same (account, effective DAY): the later row wins and
		// the timeline does not grow. The second write carries a time of day,
		// which must land on the same row — the identity is a day, not a moment.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			repriced := row(mar.Add(17*time.Hour), 250_000)
			return tx.PutOverdraftTerms(ctx, bookA, repriced)
		})
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "timeline length after upsert", len(rows), 3)
			assertEqual(t, "upserted rate", rows[1].Rate, interest.Rate(250_000))
			return nil
		})
	})
```

Add `"errors"` to `store/storetest/deposit.go`'s imports if it is not already there (it is — the
file already uses `errors.Is` for the not-found sentinels; confirm before editing).

Then extend the existing reset subtest in `store/storetest/deposit.go` — the one asserting
`"snapshots after reset"` — with a terms assertion, and seed a terms row alongside the hold and
snapshot it already writes:

```go
				terms, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
				if err != nil {
					return err
				}
				assertEqual(t, "overdraft terms after reset", len(terms), 0)
```

- [ ] **Step 3: Run the conformance suite to verify it fails**

Run: `go test ./store/mem/ -run 'TestMemDepositConformance/OverdraftTermsTimeline' -v`
Expected: FAIL to build — `tx.PutOverdraftTerms undefined`.

- [ ] **Step 4: Implement in `store/mem`**

In `store/mem/mem.go`:

- Add the key type next to `snapshotKey`:

```go
// termsKey identifies one overdraft terms row within a book: the account and
// the effective DAY, which is the same identity store/pg gets from a primary
// key on (book_id, account_id, day_key).
type termsKey struct {
	account deposit.AccountID
	dayKey  string
}
```

- Add the map to `state`, next to `snapshots`:

```go
	// overdraftTerms is the effective-dated terms timeline, keyed by (account,
	// effective day) rather than nested per account, so an upsert is a single
	// map assignment — the same shape snapshots has, for the same reason.
	overdraftTerms map[ledger.BookID]map[termsKey]deposit.OverdraftTerms
```

- Add `kindOverdraftTerms rowKind = "overdraft_terms"` to the `rowKind` const block.
- Add `overdraftTerms: make(map[ledger.BookID]map[termsKey]deposit.OverdraftTerms),` to `newState`.
- Add `overdraftTerms: cloneNested(s.overdraftTerms),` to `clone`.

In `store/mem/tx_deposit.go`, append a new section:

```go
// ---------------------------------------------------------------------------
// Effective-dated overdraft terms
// ---------------------------------------------------------------------------

// PutOverdraftTerms upserts under (account, effective day): a second row for
// the same day replaces the first, which is what makes "the terms in force on
// day D" unique by construction rather than by a validation rule.
func (t *tx) PutOverdraftTerms(ctx context.Context, book ledger.BookID, row deposit.OverdraftTerms) error {
	if err := t.write(); err != nil {
		return err
	}
	key := termsKey{account: row.AccountID, dayKey: deposit.TermsDayKey(row.EffectiveFrom)}
	t.state.insertSeq(book, kindOverdraftTerms, termsSeqID(key))
	bucket(t.state.overdraftTerms, book)[key] = row
	return nil
}

// ListOverdraftTermsForAccount returns an account's whole timeline, ascending
// by effective day. Ascending is load-bearing: deposit.termsAt binary-searches
// the slice this returns.
func (t *tx) ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.OverdraftTerms, error) {
	out := make([]deposit.OverdraftTerms, 0)
	for key, row := range t.state.overdraftTerms[book] {
		if key.account == id {
			out = append(out, row)
		}
	}
	// The effective day is already a total order within an account, so the
	// insertion sequence is the tie-break only for form, and to keep every
	// listing on one rule.
	sortRows(t.state, out, book, kindOverdraftTerms, func(row deposit.OverdraftTerms) (time.Time, string) {
		return row.EffectiveFrom, termsSeqID(termsKey{
			account: row.AccountID, dayKey: deposit.TermsDayKey(row.EffectiveFrom),
		})
	})
	return out, nil
}

// GetOverdraftTermsAsOf is the row in force on a day: the greatest effective
// day not after it.
//
// It scans rather than binary-searching, because the map is unordered and
// building the ordered slice to search would cost more than the scan. Unlike
// ActiveHoldTotal this is not an aggregate: an account with no rows before the
// day has no terms, and reporting a zero row would read as a real
// interest-free product rather than as an absence.
func (t *tx) GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id deposit.AccountID, day time.Time) (deposit.OverdraftTerms, error) {
	want := deposit.TermsDayKey(day)
	var (
		best  deposit.OverdraftTerms
		found bool
	)
	for key, row := range t.state.overdraftTerms[book] {
		if key.account != id || key.dayKey > want {
			continue
		}
		if !found || key.dayKey > deposit.TermsDayKey(best.EffectiveFrom) {
			best, found = row, true
		}
	}
	if !found {
		return deposit.OverdraftTerms{}, deposit.ErrTermsNotFound
	}
	return best, nil
}

// termsSeqID renders a terms row's composite key as the string sortRows and
// insertSeq identify rows by.
func termsSeqID(k termsKey) string { return string(k.account) + "/" + k.dayKey }
```

The `key.dayKey > want` comparison is a string compare on an ISO `YYYY-MM-DD` day, which is
lexicographically ordered — that is the whole reason `TermsDayKey` renders it that way.

- [ ] **Step 5: Run the mem conformance suite**

Run: `go test ./store/mem/ -run 'TestMemDepositConformance' -v`
Expected: PASS, including `OverdraftTermsTimeline`.

- [ ] **Step 6: Add the Postgres table**

In `store/pg/schema/0001_init.sql`, after the `snapshots` table and its index (before the
`-- The lending layer` banner):

```sql
-- An account's arranged overdraft terms from one day onwards: one row per
-- repricing, never overwritten.
--
-- These four values used to be mutable columns on deposit_accounts, and that is
-- the one place this schema broke its own rule. Every financial calculation
-- here is a function of account state, event history and configuration; the
-- first two are immutable and replayable, and a configuration that could be
-- edited in place undermined that entirely, because "what did this account's
-- product say on 15 July 2027?" had no stable answer. It also bounded the
-- interest recompute window at the last repricing, so a backdated posting
-- landing before it was silently never trued up.
--
-- These are PER-INSTANCE terms: one timeline per account, not a catalogue
-- shared across them. There is no product/product_version table, no content
-- hash, no pinned-versus-floating parameter binding and no overlays. Those are
-- the full machinery a real product engine needs and are far beyond this
-- schema's scope; the effective-dated record is not.
CREATE TABLE overdraft_terms (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    account_id      TEXT NOT NULL,
    day_key         TEXT NOT NULL,
    effective_from  TIMESTAMPTZ,
    overdraft_limit BIGINT NOT NULL,
    rate            BIGINT NOT NULL,
    unarranged_rate BIGINT NOT NULL,
    day_count       SMALLINT NOT NULL,
    created_at      TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, account_id, day_key)
);
```

and after the `holds_account_idx` index comment block, add:

```sql
-- Index 3: the timeline read that accrual makes once per account per run, and
-- the bounded as-of lookup balanceTx makes on every withdrawal check. Both
-- filter on (book_id, account_id) and order by day_key, so one index serves
-- both — the as-of lookup is an ORDER BY day_key DESC LIMIT 1 over the same
-- prefix. It is not redundant with the primary key: the PK's leading columns
-- are the same, so Postgres can in fact serve both from it, and this index is
-- declared anyway so that a future change to the primary key (an id column,
-- say) does not silently turn every accrual into a sequential scan.
CREATE INDEX overdraft_terms_account_idx ON overdraft_terms (book_id, account_id, day_key);
```

Then the column comments, appended in the `COMMENT ON COLUMN` section near
`deposit_accounts.interest_gl`:

```sql
COMMENT ON COLUMN overdraft_terms.day_key IS
    'The UTC calendar day these terms first apply, as YYYY-MM-DD, and part of '
    'the primary key: a terms row is identified by (account, DAY), so a second '
    'row entered for the same effective day replaces the first and "the terms '
    'in force on day D" is unique by construction rather than by a validation '
    'rule. Go does the truncating (deposit.TermsDayKey over ledger.DayStart) '
    'and this column is what both the listing and the as-of lookup order and '
    'compare on — an ISO day is lexicographically ordered, so a text compare '
    'is a day compare. The key is a day rather than a timestamp because '
    'accrual iterates whole UTC days: terms changing part-way through a day '
    'would have no well-defined meaning, since the day is the unit the '
    'arithmetic is expressed in. Neither store truncates for itself, which is '
    'one DST-adjacent edge case away from store/pg and store/mem disagreeing '
    'about which day a repricing landed in. Compare snapshots.date_key, which '
    'is the same pattern for the same reason.';

COMMENT ON COLUMN overdraft_terms.effective_from IS
    'The same day as day_key, as a timestamp, and the value Go reads back. It '
    'is stored beside the key rather than derived from it so that a reader of '
    'this table sees a date rather than a string, and so that ORDER BY '
    'effective_from and ORDER BY day_key can never disagree.';

COMMENT ON COLUMN overdraft_terms.created_at IS
    'When this repricing was ENTERED, as against effective_from, which is when '
    'it takes economic effect. The pair is the booking-date/value-date '
    'distinction applied to configuration, for exactly the reasons the README '
    'gives for money: a repricing agreed on the 1st and entered on the 15th is '
    'the ordinary case, and refusing it would leave the agreed date with no '
    'representation. Both directions are allowed — a row effective in the past '
    'is picked up by the next recompute the same way a backdated posting is, '
    'and one effective next month is inert until the runs reach it, which is '
    'scheduled repricing for free.';

COMMENT ON COLUMN overdraft_terms.rate IS
    'Annual interest rate on the arranged overdraft, in MILLIONTHS: 1000000 is '
    '100%, 150000 is 15% (interest.RateScale). Zero makes the WHOLE overdraft '
    'interest-free, which is a real product. unarranged_rate is the same scale '
    'and applies to any balance drawn beyond overdraft_limit; it is an optional '
    'SURCHARGE, so zero there means rate applies throughout rather than that '
    'the excess is free. There is deliberately NO CHECK on either column, and '
    'none on day_count: a CHECK enumerating the valid day-count conventions '
    'would make store/pg refuse a write store/mem performs — which '
    'store/storetest exists to prevent — and would turn a one-line change to a '
    'Go constant into a migration. This is the same reasoning recorded on the '
    'four asset columns, applied to a new case, and it is recorded in the '
    'database because a missing constraint is invisible in a schema dump.';
```

In `store/pg/pg.go`, add `"overdraft_terms"` to the `tables` slice, on the deposit line:

```go
	"deposit_accounts", "holds", "snapshots", "overdraft_terms",
```

- [ ] **Step 7: Implement in `store/pg`**

Append to `store/pg/tx_deposit.go`:

```go
// ---------------------------------------------------------------------------
// Effective-dated overdraft terms
// ---------------------------------------------------------------------------

// PutOverdraftTerms upserts under (account, effective day). The day key is
// derived with deposit.TermsDayKey — the same function store/mem keys its map
// with, and the same one GetOverdraftTermsAsOf compares against — so the two
// stores agree on which day a repricing landed in by construction.
func (t *tx) PutOverdraftTerms(ctx context.Context, book ledger.BookID, row deposit.OverdraftTerms) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO overdraft_terms (
			book_id, account_id, day_key, effective_from, overdraft_limit,
			rate, unarranged_rate, day_count, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (book_id, account_id, day_key) DO UPDATE SET
			effective_from  = EXCLUDED.effective_from,
			overdraft_limit = EXCLUDED.overdraft_limit,
			rate            = EXCLUDED.rate,
			unarranged_rate = EXCLUDED.unarranged_rate,
			day_count       = EXCLUDED.day_count,
			created_at      = EXCLUDED.created_at`,
		string(book), string(row.AccountID), deposit.TermsDayKey(row.EffectiveFrom),
		nullTime(row.EffectiveFrom), row.OverdraftLimit,
		int64(row.Rate), int64(row.UnarrangedRate), int16(row.DayCount), nullTime(row.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put overdraft terms %s/%s: %w",
			row.AccountID, deposit.TermsDayKey(row.EffectiveFrom), err)
	}
	return nil
}

// overdraftTermsColumns is the select list both readers use, for the reason
// depositAccountColumns exists: it stops the two from scanning different
// column sets, which is a whole class of "it round-trips one way".
const overdraftTermsColumns = `
	account_id, effective_from, overdraft_limit, rate, unarranged_rate,
	day_count, created_at`

func scanOverdraftTerms(row interface{ Scan(...any) error }) (deposit.OverdraftTerms, error) {
	var (
		out                   deposit.OverdraftTerms
		rate, unarranged      int64
		dayCount              int16
		effective, created    *time.Time
	)
	if err := row.Scan(&out.AccountID, &effective, &out.OverdraftLimit,
		&rate, &unarranged, &dayCount, &created); err != nil {
		return deposit.OverdraftTerms{}, err
	}
	out.Rate = interest.Rate(rate)
	out.UnarrangedRate = interest.Rate(unarranged)
	out.DayCount = interest.DayCount(dayCount)
	out.EffectiveFrom = readTime(effective)
	out.CreatedAt = readTime(created)
	return out, nil
}

// ListOverdraftTermsForAccount returns the whole timeline ascending by day_key,
// which is an ISO day and therefore lexicographically ordered. Ascending is
// load-bearing: deposit.termsAt binary-searches the slice this returns.
func (t *tx) ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.OverdraftTerms, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT `+overdraftTermsColumns+`
		FROM overdraft_terms WHERE book_id = $1 AND account_id = $2
		ORDER BY day_key ASC, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("pg: list overdraft terms: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.OverdraftTerms, 0)
	for rows.Next() {
		row, err := scanOverdraftTerms(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list overdraft terms: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetOverdraftTermsAsOf is the row in force on a day. It compares day_key
// rather than effective_from so that the bound is a DAY on both sides — the
// caller's instant is truncated by deposit.TermsDayKey in Go, and the column it
// is compared against was written the same way, so no timestamp arithmetic
// happens in the database at all.
func (t *tx) GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id deposit.AccountID, day time.Time) (deposit.OverdraftTerms, error) {
	row := t.tx.QueryRow(ctx, `
		SELECT `+overdraftTermsColumns+`
		FROM overdraft_terms
		WHERE book_id = $1 AND account_id = $2 AND day_key <= $3
		ORDER BY day_key DESC
		LIMIT 1`, string(book), string(id), deposit.TermsDayKey(day))
	out, err := scanOverdraftTerms(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.OverdraftTerms{}, deposit.ErrTermsNotFound
	}
	if err != nil {
		return deposit.OverdraftTerms{}, fmt.Errorf("pg: overdraft terms for %s as of %s: %w",
			id, deposit.TermsDayKey(day), err)
	}
	return out, nil
}
```

- [ ] **Step 8: Run both store suites**

```bash
go test ./store/... ./deposit/... -v -run 'Deposit|Terms'
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./store/pg/ -v -run 'Deposit|Terms'
```

Expected: PASS in both. If the pg run cannot connect, start the container (`make test-pg` brings it
up); a skipped pg run is **not** a pass — the constraint at the top of this plan requires both.

Then the whole tree: `go test ./...` — expected PASS (nothing consumes the new methods yet, and
`gofmt -l .` should be empty).

- [ ] **Step 9: Commit**

```bash
git add deposit/store.go store/mem store/pg store/storetest
git commit -m "feat(store): persist the overdraft terms timeline in both stores

Put/List/GetAsOf, keyed by (account, effective day) so the two stores cannot
disagree about which day a repricing landed in. Conformance covers ordering,
the day-granular upsert and the four as-of positions."
```

---

### Task 3: The lending terms timeline — value type and storage

The mirror of Tasks 1 and 2, in one task because it is mechanically derived from a pair a reviewer
has already gated. A facility's terms carry no limit: a facility's `Commitment` is not effective-dated
(nothing reprices it, and drawing is refused against it at the moment of the draw, not historically).

**Files:**
- Create: `lending/terms.go`, `lending/terms_test.go`
- Modify: `lending/store.go`, `lending/errors.go`
- Modify: `store/mem/mem.go`, `store/mem/tx_lending.go`
- Modify: `store/pg/schema/0001_init.sql`, `store/pg/pg.go`, `store/pg/tx_lending.go`
- Modify: `store/storetest/lending.go`

**Interfaces:**
- Consumes: `interest.Rate`, `interest.DayCount`, `ledger.DayStart`, `lending.FacilityID`.
- Produces:
  - `lending.FacilityTerms{FacilityID FacilityID; EffectiveFrom time.Time; Rate interest.Rate; DayCount interest.DayCount; CreatedAt time.Time}`
  - `func (t FacilityTerms) Validate() error`, `func TermsDayKey(day time.Time) string`
  - `func termsAt(rows []FacilityTerms, day time.Time) (FacilityTerms, bool)`, `func anyPriced(rows []FacilityTerms) bool` — both unexported, both reached directly by the in-package `lending/terms_test.go`
  - `lending.ErrTermsNotFound`, `lending.ErrScheduleWouldDiverge`
  - on `lending.Tx`:
    - `PutFacilityTerms(ctx context.Context, book ledger.BookID, t FacilityTerms) error`
    - `ListFacilityTerms(ctx context.Context, book ledger.BookID, id FacilityID) ([]FacilityTerms, error)` — named after `ListInstallments`, which is the facility-scoped listing precedent in this interface, rather than after deposit's `ListOverdraftTermsForAccount`, which is named after `ListHoldsForAccount`
    - `GetFacilityTermsAsOf(ctx context.Context, book ledger.BookID, id FacilityID, day time.Time) (FacilityTerms, error)`

- [ ] **Step 1: Write the failing unit tests**

Create `lending/terms_test.go`. It is **in-package** (`package lending`), following the
`lending/schedule_test.go` already in this directory, so it reaches `termsAt` and `anyPriced`
directly and no `export_test.go` is needed. `schedule_test.go` already declares a package-level
`func day(y int, m time.Month, d int) time.Time` in this package — reuse it, do not redeclare it.

It is the direct mirror of `deposit/terms_test.go` from Task 1, with two differences: the
constructor builds a `FacilityTerms` with `FacilityID: "fac_1"` and no limit or unarranged rate, and
the validation test keeps only the negative-rate case, because there is no limit and no unarranged
rate to combine wrongly.

```go
package lending

import (
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
)

// day is declared in schedule_test.go, in this same package.

func sampleTerms(from time.Time, rate interest.Rate) FacilityTerms {
	return FacilityTerms{
		FacilityID: "fac_1", EffectiveFrom: from, Rate: rate, DayCount: interest.ACT365,
	}
}

func TestFacilityTermsAtResolvesEveryPosition(t *testing.T) {
	rows := []FacilityTerms{
		sampleTerms(day(2025, time.January, 1), 60_000),
		sampleTerms(day(2025, time.March, 1), 90_000),
		sampleTerms(day(2025, time.June, 1), 120_000),
	}
	cases := []struct {
		name string
		day  time.Time
		want interest.Rate
		ok   bool
	}{
		{"before the first row", day(2024, time.December, 31), 0, false},
		{"exactly on the first boundary", day(2025, time.January, 1), 60_000, true},
		{"between rows", day(2025, time.February, 14), 60_000, true},
		{"exactly on a later boundary", day(2025, time.March, 1), 90_000, true},
		{"the day before a boundary", day(2025, time.May, 31), 90_000, true},
		{"after the last row", day(2030, time.January, 1), 120_000, true},
	}
	for _, c := range cases {
		got, ok := termsAt(rows, c.day)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && got.Rate != c.want {
			t.Errorf("%s: rate = %d, want %d", c.name, got.Rate, c.want)
		}
	}
}

func TestFacilityTermsAtIgnoresTheTimeOfDay(t *testing.T) {
	rows := []FacilityTerms{sampleTerms(day(2025, time.March, 1), 90_000)}
	if _, ok := termsAt(rows, time.Date(2025, time.February, 28, 23, 59, 0, 0, time.UTC)); ok {
		t.Error("late on the day before resolved the row")
	}
	if got, ok := termsAt(rows, time.Date(2025, time.March, 1, 0, 0, 1, 0, time.UTC)); !ok || got.Rate != 90_000 {
		t.Errorf("a second into the effective day: got %d, %v", got.Rate, ok)
	}
}

func TestFacilityTermsAtOnASingleRowAndOnNone(t *testing.T) {
	one := []FacilityTerms{sampleTerms(day(2025, time.January, 1), 60_000)}
	if _, ok := termsAt(one, day(2024, time.December, 31)); ok {
		t.Error("resolved a day before the only row")
	}
	if got, ok := termsAt(one, day(2025, time.January, 1)); !ok || got.Rate != 60_000 {
		t.Errorf("on the boundary: got %d, %v", got.Rate, ok)
	}
	if _, ok := termsAt(nil, day(2025, time.January, 1)); ok {
		t.Error("resolved against no rows at all")
	}
}

func TestFacilityTermsAtAfterABackdatedInsert(t *testing.T) {
	rows := []FacilityTerms{
		sampleTerms(day(2025, time.January, 1), 60_000),
		sampleTerms(day(2025, time.February, 1), 150_000), // entered last, effective in the middle
		sampleTerms(day(2025, time.March, 1), 90_000),
	}
	if got, ok := termsAt(rows, day(2025, time.February, 15)); !ok || got.Rate != 150_000 {
		t.Errorf("mid-February: got %d, %v", got.Rate, ok)
	}
}

func TestFacilityAnyPriced(t *testing.T) {
	unpriced := []FacilityTerms{sampleTerms(day(2025, time.January, 1), 0)}
	if anyPriced(unpriced) {
		t.Error("a zero-rate timeline reported as priced")
	}
	if !anyPriced(append(unpriced, sampleTerms(day(2025, time.June, 1), 60_000))) {
		t.Error("a timeline ending in a non-zero rate reported as unpriced")
	}
}

func TestFacilityTermsDayKeyIsTheUTCCalendarDay(t *testing.T) {
	morning := time.Date(2025, time.March, 1, 9, 0, 0, 0, time.UTC)
	night := time.Date(2025, time.March, 1, 23, 30, 0, 0, time.UTC)
	if TermsDayKey(morning) != TermsDayKey(night) {
		t.Errorf("keys differ within one day: %q vs %q", TermsDayKey(morning), TermsDayKey(night))
	}
	if got := TermsDayKey(morning); got != "2025-03-01" {
		t.Errorf("TermsDayKey = %q, want 2025-03-01", got)
	}
	if !(TermsDayKey(day(2025, time.February, 28)) < TermsDayKey(day(2025, time.March, 1))) {
		t.Error("day keys do not sort lexicographically")
	}
}

func TestFacilityTermsValidation(t *testing.T) {
	negative := sampleTerms(day(2025, time.January, 1), -1)
	if err := negative.Validate(); !errors.Is(err, ErrInvalidRate) {
		t.Errorf("negative rate: got %v, want ErrInvalidRate", err)
	}
	free := sampleTerms(day(2025, time.January, 1), 0)
	if err := free.Validate(); err != nil {
		t.Errorf("zero-rate facility: got %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lending/ -run 'TestFacilityTerms|TestFacilityAnyPriced' -v`
Expected: FAIL to build — `undefined: FacilityTerms`, `termsAt`, `anyPriced`, `TermsDayKey`.

- [ ] **Step 3: Add the sentinels**

In `lending/errors.go`, inside the `var (…)` block:

```go
	// ErrTermsNotFound is returned when no facility terms are in force on a
	// day. Every facility gets an opening terms row at origination, so the only
	// way to miss is to ask about a day before the facility existed.
	ErrTermsNotFound = errors.New("no facility terms in force on that day")

	// ErrScheduleWouldDiverge is returned when a term loan that already has a
	// generated schedule is repriced.
	//
	// A term loan's instalments are generated once, at disbursement, from the
	// rate in force then, and stored as rows. If accrual followed a timeline
	// and the schedule did not, the plan and the actual accrual would drift
	// apart — beyond the ordinary plan-versus-actual divergence this package
	// already teaches, which 30/360 exists to keep small — and the final
	// instalment would silently absorb the difference, unnoticed until
	// maturity. Regenerating a schedule against repayments already posted
	// against it needs versioned schedule rows and open-item allocation, which
	// is its own topic. Repricing is allowed freely on a revolving line (no
	// schedule) and on an undisbursed term loan (none yet).
	ErrScheduleWouldDiverge = errors.New("repricing a term loan with a generated schedule would diverge from it")
```

- [ ] **Step 4: Write `lending/terms.go`**

`lending/terms.go` mirrors `deposit/terms.go`. Full body:

```go
package lending

import (
	"sort"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// FacilityTerms is what a facility's credit cost from one day onwards: one row
// per repricing, never overwritten.
//
// It is the mirror of deposit.OverdraftTerms and exists for the same reasons —
// see there for the full argument. What differs is what is NOT here. A
// facility's Commitment is not effective-dated: drawing is refused against the
// limit in force at the moment of the draw, and no past day's arithmetic
// depends on what it used to be. Neither are Method, TermMonths or MinPayment,
// which feed BuildSchedule rather than the accrual; see the package doc and
// ErrScheduleWouldDiverge for why the divergence that would create is made
// unreachable rather than merely documented.
type FacilityTerms struct {
	FacilityID    FacilityID
	EffectiveFrom time.Time // day-truncated; the first day these terms apply
	Rate          interest.Rate
	DayCount      interest.DayCount
	CreatedAt     time.Time // when the row was entered, not when it takes effect
}

// TermsDayKey is the day a terms row is identified by within its facility. It
// is deposit.TermsDayKey's mirror, and deliberately not shared with it: lending
// does not import deposit, and a shared constant would be the first thread of
// exactly that dependency.
func TermsDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// Validate reports whether these terms are a product. A zero rate is a real
// product — an interest-free facility — so only a negative one is refused.
func (t FacilityTerms) Validate() error {
	if t.Rate < 0 {
		return ErrInvalidRate
	}
	return nil
}

// termsAt is the row in force on a day: the last one whose EffectiveFrom is not
// after it. rows must be ascending, which is what ListFacilityTermsForFacility
// returns. See deposit.termsAt for why this is a binary search rather than a
// cursor advanced alongside the accrual walk.
func termsAt(rows []FacilityTerms, day time.Time) (FacilityTerms, bool) {
	d := ledger.DayStart(day)
	i := sort.Search(len(rows), func(i int) bool {
		return rows[i].EffectiveFrom.After(d)
	})
	if i == 0 {
		return FacilityTerms{}, false
	}
	return rows[i-1], true
}

// anyPriced reports whether any row in the timeline carries a non-zero rate. It
// is what keeps a never-priced facility from reading a drawn series every
// night; see deposit.anyPriced for why the old account-level rate guard could
// not survive as one.
func anyPriced(rows []FacilityTerms) bool {
	for _, r := range rows {
		if r.Rate > 0 {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run the unit tests**

Run: `go test ./lending/ -run 'TestFacilityTerms|TestFacilityAnyPriced' -v`
Expected: PASS, all seven tests.

- [ ] **Step 6: Write the lending conformance subtest**

In `store/storetest/lending.go`, add a `FacilityTermsTimeline` subtest that is the direct mirror of
`OverdraftTermsTimeline` from Task 2, substituting `lending.FacilityTerms`, `"fac_1"`,
`tx.PutFacilityTerms`, `tx.ListFacilityTerms`, `tx.GetFacilityTermsAsOf` and
`lending.ErrTermsNotFound`, with rows carrying `Rate` 60_000 / 90_000 / 120_000 and
`DayCount: interest.ACT360`. Assert the same six things: ascending order, three-row length, every
field round-tripping, book scoping, the four as-of positions plus the unknown-facility case, and
the day-granular upsert (write the middle row again at `mar.Add(17*time.Hour)` with rate 100_000
and assert the timeline is still three long and the middle row is 100_000).

Extend the lending reset subtest the same way the deposit one was, asserting
`len(terms) == 0` after `Reset`.

- [ ] **Step 7: Run to verify it fails, then implement both stores**

Run: `go test ./store/mem/ -run 'Lending' -v` → FAIL to build.

`store/mem`: add `facilityTermsKey{facility lending.FacilityID; dayKey string}`, the
`facilityTerms map[ledger.BookID]map[facilityTermsKey]lending.FacilityTerms` state field,
`kindFacilityTerms rowKind = "facility_terms"`, the `newState` and `clone` entries, and the three
methods plus `facilityTermsSeqID` in `store/mem/tx_lending.go` — each the exact mirror of the
deposit implementation from Task 2 Step 4.

`store/pg`: add the table after `installments`:

```sql
-- A facility's credit terms from one day onwards: one row per repricing, never
-- overwritten. The mirror of overdraft_terms — see there for why terms are rows
-- at all — with two differences worth stating. There is no limit column,
-- because facilities.commitment is not effective-dated: drawing is refused
-- against the limit in force at the moment of the draw, and no past day's
-- arithmetic depends on what it used to be. And there is no method, term_months
-- or min_payment column, because those feed BuildSchedule rather than the
-- accrual: a term loan's instalments are generated once from the rate in force
-- at disbursement, so repricing one that already has a schedule is REFUSED
-- (lending.ErrScheduleWouldDiverge) rather than allowed to drift.
CREATE TABLE facility_terms (
    book_id        TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    facility_id    TEXT NOT NULL,
    day_key        TEXT NOT NULL,
    effective_from TIMESTAMPTZ,
    rate           BIGINT NOT NULL,
    day_count      SMALLINT NOT NULL,
    created_at     TIMESTAMPTZ,
    seq            BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, facility_id, day_key)
);

CREATE INDEX facility_terms_facility_idx ON facility_terms (book_id, facility_id, day_key);
```

with comments mirroring `overdraft_terms.day_key`, `.effective_from`, `.created_at` and `.rate`
(the rate comment carries the same no-`CHECK` reasoning). Add `"facility_terms"` to `tables` in
`store/pg/pg.go`, on the lending line. Add the three methods, `facilityTermsColumns` and
`scanFacilityTerms` to `store/pg/tx_lending.go`, mirroring Task 2 Step 7.

- [ ] **Step 8: Run both store suites**

```bash
go test ./... && gofmt -l .
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./store/pg/
```

Expected: PASS in both; `gofmt -l .` prints nothing.

- [ ] **Step 9: Commit**

```bash
git add lending store/mem store/pg store/storetest
git commit -m "feat(lending): add the facility terms timeline and its storage

The mirror of the deposit record, minus a limit — a commitment is not
effective-dated — and minus the schedule inputs, which is the divergence
ErrScheduleWouldDiverge exists to make unreachable."
```

---

### Task 4: Deposit — terms leave the account

**This is the plan's largest task and it cannot be split.** The moment `deposit.Account` loses
`Rate`, every reader breaks, so `register.go`, `store/pg/tx_deposit.go`, the schema, `api/` and
`seed/` all move in one commit or the tree does not compile. Splitting it would mean an
intermediate commit in which the columns and the rows both exist and can disagree — which is the
exact defect this whole change removes. The steps below are still bite-sized; the commit is one.

**Files:**
- Modify: `deposit/types.go`, `deposit/register.go`
- Modify: `store/pg/tx_deposit.go`, `store/pg/schema/0001_init.sql`
- Modify: `store/storetest/deposit.go`
- Modify: `api/dto_deposit.go`, `api/handlers_deposit.go`
- Modify: `seed/seed.go`
- Modify: `deposit/register_test.go`, `api/server_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1 and 2.
- Produces:
  - `deposit.AccountWithTerms{Account Account; Terms OverdraftTerms}`
  - `func (r *Register) SetOverdraftTerms(ctx context.Context, id AccountID, limit ledger.Amount, rate, unarranged interest.Rate, dc interest.DayCount, effectiveFrom time.Time) (OverdraftTerms, error)` — and `…Tx` with the same tail
  - `func (r *Register) OverdraftTermsHistory(ctx context.Context, id AccountID) ([]OverdraftTerms, error)`
  - `func (r *Register) GetAccountWithTerms(ctx context.Context, id AccountID) (AccountWithTerms, error)`
  - `func (r *Register) ListAccountsWithTerms(ctx context.Context) ([]AccountWithTerms, error)`
  - `func overdraftAccrual(book ledger.Amount, t OverdraftTerms, from, to time.Time) interest.Accrued`

- [ ] **Step 1: Write the acceptance tests**

Add to `deposit/register_test.go` (`package deposit_test`, which **dot-imports** `deposit`, so
`Account`, `OverdraftTerms` and the sentinels are written unqualified — match the file you are
adding to). These are the capabilities that fail on today's code; the first is the reason the change
exists.

Two shared helpers first, added beside the existing `overdrawBy` at `register_test.go:710`:

```go
// overdrawValueDated is overdrawBy with explicit dates. A BACK-DATED posting is
// one whose booking date is today and whose value date is a day already accrued
// through, which is the case every acceptance test below turns on and which
// overdrawBy — value-dated at the clock — cannot express.
func overdrawValueDated(t *testing.T, book *ledger.Book, sub ledger.SubledgerID, acct Account, amount ledger.Amount, booking, value time.Time) {
	t.Helper()
	ctx := context.Background()
	counterparty, err := book.CreateAccount(ctx, sub,
		"Counterparty "+string(acct.ID)+" "+value.Format("2006-01-02"), ledger.Liability, acct.Asset)
	assertNoError(t, err)
	_, err = book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "overdraw",
		BookingDate: booking,
		ValueDate:   value,
		Entries: []ledger.Entry{
			{AccountID: acct.GLAccount, Amount: amount, Direction: ledger.Debit},
			{AccountID: counterparty.ID, Amount: amount, Direction: ledger.Credit},
		},
	})
	assertNoError(t, err)
}

// expectedFromTimeline sums what the account SHOULD have accrued, day by day,
// from functions the test states rather than from the implementation under
// test. drawnOn and rateOn are the two things a timeline decides, and every
// acceptance figure below is computed this way — never copied out of what the
// code happens to print.
//
// It is deliberately the same shape as interest.perDay: one Accrue call per
// day, so the per-call integer truncation lands identically.
func expectedFromTimeline(start time.Time, days int, dc interest.DayCount,
	drawnOn func(d int) ledger.Amount, rateOn func(d int) interest.Rate) interest.Accrued {
	var total interest.Accrued
	for d := 0; d < days; d++ {
		from := start.AddDate(0, 0, d)
		total += interest.Accrue(drawnOn(d), rateOn(d), dc, from, from.AddDate(0, 0, 1))
	}
	return total
}
```

Then the acceptance tests. **Test 1 in full** — every later one is the same skeleton with a
different timeline, so it is written out once:

```go
// Back-value across a repricing trues up. Priced at R1 from day 0, repriced to
// R2 effective day 30. On day 45 a transaction lands value-dated day 10: the
// next run re-derives days 10-30 AT R1, with the new balance in place, and
// posts the delta.
//
// Nothing happens today, because the window starts at day 30 and the days the
// posting takes effect over are behind it — a line the customer cannot see and
// did not agree to. This test is the reason effective-dated terms exist.
func TestBackValueAcrossARepricingTruesUp(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	const (
		r1      interest.Rate = 120_000 // 12%
		r2      interest.Rate = 180_000 // 18%
		opening ledger.Amount = 100_000 // EUR 1,000 drawn from day 0
		extra   ledger.Amount = 100_000 // a second EUR 1,000, value-dated day 10
	)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r1, 0, interest.ACT365, day(0))
	assertNoError(t, err)

	overdrawValueDated(t, book, sub, acct, opening, day(0), day(0))

	// Thirty days at R1 on the opening draw, run as one end-of-day.
	clock.set(day(30))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(30)))

	// Repriced from day 30, entered on day 30 — an ordinary forward repricing.
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r2, 0, interest.ACT365, day(30))
	assertNoError(t, err)

	clock.set(day(45))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(45)))

	// The back-dated posting: it reaches the ledger on day 45 and takes
	// economic effect on day 10, which is twenty days BEHIND the repricing.
	overdrawValueDated(t, book, sub, acct, extra, day(45), day(10))

	clock.set(day(46))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(46)))

	// Days 0-9 on 1,000 at R1; days 10-29 on 2,000 at R1 — the span that is
	// silently never corrected today; days 30-45 on 2,000 at R2.
	want := expectedFromTimeline(day(0), 46, interest.ACT365,
		func(d int) ledger.Amount {
			if d < 10 {
				return opening
			}
			return opening + extra
		},
		func(d int) interest.Rate {
			if d < 30 {
				return r1
			}
			return r2
		})

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after the back-dated posting", got.Accrued, want)

	// The receivable holds Minor() of the record, which is the invariant every
	// caller in this package maintains — and the true-up was POSTED, not just
	// recorded on the row.
	receivable, err := book.BookBalance(ctx, got.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after the true-up", receivable, want.Minor())
}
```

The remaining nine reuse that skeleton — same `origin`/`day`/`mutableClock`/`newTestRegisterOn`
opening, same `expectedFromTimeline` for the expected figure, same two closing assertions on
`got.Accrued` and the receivable. Only the timeline differs:

```go
// Whole-life accrual equals the sum of its periods: days 0-30 at R1 plus days
// 30-60 at R2, computed in ONE run, equals the two computed separately. This
// pins per-day resolution rather than one rate applied across the window.
//
// Build two registers on the same clock and the same drawn amount. On the
// first, accrue once to day 60. On the second, accrue to day 30 and again to
// day 60. Assert the two accounts' Accrued are equal, and that both equal
// expectedFromTimeline over 60 days with rateOn switching at 30.
func TestWholeLifeAccrualEqualsTheSumOfItsPeriods(t *testing.T)

// A backdated terms row posts a delta and rewrites nothing.
//
// Priced at R1 from day 0, drawn from day 0, accrued through day 45. THEN
// SetOverdraftTerms is called with effectiveFrom day(30) while the clock reads
// day(45) — entered on the 45th, effective on the 30th. Accrue to day 46 and
// assert Accrued equals expectedFromTimeline over 46 days with rateOn switching
// at 30, i.e. days 30-44 have been re-priced retroactively as a delta.
//
// Then assert nothing was rewritten: reg.GetAuditLog contains TWO
// ledger.EventOverdraftTermsSet events for this account (filter on
// EntityID and Type), and reg.OverdraftTermsHistory returns three rows —
// opening, R1, R2 — with the R2 row's EffectiveFrom on day 30 and its CreatedAt
// on day 45. The pair of dates is the whole point.
func TestABackdatedTermsRowPostsADeltaAndRewritesNothing(t *testing.T)

// A future-dated terms row is inert until its date, then applies.
//
// Priced at R1 from day 0, drawn from day 0. On day 10, set terms at R2
// effective day(30). Accrue to day 20 and assert Accrued equals
// expectedFromTimeline over 20 days at R1 THROUGHOUT — the future row changed
// nothing. Then accrue to day 40 and assert it equals expectedFromTimeline over
// 40 days with rateOn switching at 30. Scheduled repricing, for free.
func TestAFutureDatedTermsRowIsInertUntilItsDate(t *testing.T)

// An account unpriced then priced. Open with a limit and NO SetOverdraftTerms
// call, so the only row is the opening one at a zero rate. Draw on day 0.
// Accrue to day 365 and assert Accrued is exactly 0 — the account had a
// facility and used it, free. Then price it at R1 effective day(365), accrue to
// day 400, and assert Accrued equals expectedFromTimeline over days 365-399
// only.
//
// The previous model could not express this at all: Rate was per-account, so
// pricing it would have re-derived the free year at the new rate or, with the
// window reset, thrown that year away.
func TestAnAccountUnpricedThenPricedAccruesOnlyAfterwards(t *testing.T)

// Idempotence. Priced, drawn, accrued to day 30. Record Accrued and the
// receivable balance, then call AccrueOverdraft for day(30) three more times
// and assert neither moved.
//
// The guard is still what refuses the re-run, but with a whole-life recompute
// the same date would produce the same gross and therefore a zero delta anyway
// — same invariant, one fewer reason behind it.
func TestRerunningEndOfDayForTheSameDatePostsNothing(t *testing.T)

// A never-priced account reads no series. Open an account and never price it,
// so every row in its timeline carries a zero rate. Drive AccrueOverdraftTx
// through a countingTx (below) for day(30) and assert the series counter is
// still 0 — the run is skipped BEFORE any store read, not merely producing
// zero. Asserted on the store, because "the balance did not move" would pass
// even if the loop ran.
func TestANeverPricedAccountReadsNoSeries(t *testing.T)
```

Three need their bodies written out, because their assertions are not on a figure.

```go
// The guard still holds: an unadvanced window is refused BEFORE a series is
// read, per interest.Recompute's documented precondition — "a caller that
// recomputes an empty window over a non-zero prior state is told to give the
// whole record back". The precondition survives this change and this is what
// pins that neither layer has started violating it.
func TestAnUnadvancedWindowIsRefusedBeforeReadingASeries(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 120_000, 0, interest.ACT365, day(0))
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, 100_000, day(0), day(0))

	clock.set(day(30))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(30)))
	before, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// Now re-run for a date the window has ALREADY reached. The record must
	// come back untouched, and no series must be read at all.
	var reads int
	assertNoError(t, reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		return reg.AccrueOverdraftTx(ctx, countingTx{Tx: tx, series: &reads}, acct.ID, day(30))
	}))
	assertEqual(t, "series reads on an unadvanced window", reads, 0)

	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after an unadvanced re-run", after.Accrued, before.Accrued)
	receivable, err := book.BookBalance(ctx, after.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after an unadvanced re-run", receivable, before.Accrued.Minor())
}

// The advancement guard resolves its day count on `date`.
//
// There is no single DayCount to ask any more — it is a terms field — and the
// conventions genuinely disagree about whether a window advanced. Under
// Thirty360 the 31st collapses onto the 30th, so Days(30th, 31st) is zero while
// ACT365 says one: a run on the 31st is a no-op under one convention and a real
// day under the other. Resolving on the day being accrued is the right answer,
// and it is otherwise invisible.
func TestTheAdvancementGuardResolvesItsDayCountOnTheAccrualDate(t *testing.T) {
	ctx := context.Background()
	the30th := time.Date(2025, time.January, 30, 0, 0, 0, 0, time.UTC)
	the31st := time.Date(2025, time.January, 31, 0, 0, 0, 0, time.UTC)

	run := func(dc interest.DayCount) interest.Accrued {
		t.Helper()
		clock := &mutableClock{at: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)}
		reg, book, sub := newTestRegisterOn(t, clock.now)
		acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
		assertNoError(t, err)
		_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 120_000, 0, dc, clock.now())
		assertNoError(t, err)
		overdrawValueDated(t, book, sub, acct, 100_000, clock.now(), clock.now())

		clock.set(the30th)
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, the30th))
		through30, err := reg.GetAccount(ctx, acct.ID)
		assertNoError(t, err)

		clock.set(the31st)
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, the31st))
		through31, err := reg.GetAccount(ctx, acct.ID)
		assertNoError(t, err)
		return through31.Accrued - through30.Accrued
	}

	if moved := run(interest.Thirty360); moved != 0 {
		t.Errorf("Thirty360 accrued %d on the 31st, want 0: the 31st collapses onto the 30th", moved)
	}
	if moved := run(interest.ACT365); moved <= 0 {
		t.Errorf("ACT365 accrued %d on the 31st, want a real day", moved)
	}
}

// Thirty360, terms effective on a 31st: the row takes effect on the 1st,
// because the 31st accrues nothing under the convention.
//
// That follows from the convention rather than from this change, and it is
// pinned rather than asserted away: it is surprising enough that someone will
// read it as a bug, and a test saying "this is correct and here is why" is
// cheaper than the investigation.
func TestUnderThirty360ATermsRowEffectiveOnA31stTakesEffectOnThe1st(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 120_000, 0, interest.Thirty360, clock.now())
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, 100_000, clock.now(), clock.now())

	the30th := time.Date(2025, time.January, 30, 0, 0, 0, 0, time.UTC)
	the31st := time.Date(2025, time.January, 31, 0, 0, 0, 0, time.UTC)
	feb1 := time.Date(2025, time.February, 1, 0, 0, 0, 0, time.UTC)

	clock.set(the30th)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, the30th))

	// A repricing effective on the 31st, entered on the 30th.
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 240_000, 0, interest.Thirty360, the31st)
	assertNoError(t, err)

	through30, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	clock.set(feb1)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, feb1))
	throughFeb1, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// One day of 30/360 accrued between the 30th and 1 February — the 31st
	// collapsed onto the 30th and accrued nothing — and that one day is the
	// 31st's, charged at the NEW rate, because the row was in force on it.
	want := interest.Accrue(100_000, 240_000, interest.Thirty360, the31st, feb1)
	assertEqual(t, "the 31st accrues one 30/360 day at the new rate",
		throughFeb1.Accrued-through30.Accrued, want)

	receivable, err := book.BookBalance(ctx, throughFeb1.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable", receivable, throughFeb1.Accrued.Minor())
}
```

Plus the store wrapper the two counter-based tests need. Check `ledger.Tx.ValueDatedSeries`'s exact
signature in `ledger/store.go` and match it verbatim; the wrapper satisfies `deposit.Tx` by
embedding, and only overrides the one method:

```go
// countingTx wraps a Tx and counts value-dated series reads, so a test can
// assert that a run was skipped BEFORE any I/O rather than that it happened to
// produce zero. The two are indistinguishable from the balance alone, and only
// one of them is the guard doing its job.
type countingTx struct {
	Tx
	series *int
}

func (t countingTx) ValueDatedSeries(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction, from, to time.Time) (ledger.Series, error) {
	*t.series++
	return t.Tx.ValueDatedSeries(ctx, book, id, normal, from, to)
}
```

Finally, the failure mode called out at the end of this plan — a backdated rate **cut** across a
long history drives a much larger negative delta than `correctOverdraftAccrualTx` has ever seen:

```go
// A retroactive rate CUT across a long history drives a large negative delta
// through the refund path. Its bounds hold — the credit to the receivable is
// clamped by what the receivable holds, and the rest is refunded in cash rather
// than driving an Asset balance negative, which the ledger would refuse inside
// an end-of-day batch and take the whole book's run down with it — but this
// change widens the input range by years, so the test says so.
//
// Price at 24% from day 0, drawn throughout, accrue to day 365, then CAPITALISE
// (ChargeOverdraftInterest) so the receivable is emptied and the interest has
// genuinely been charged to the customer. Then reprice to 2% effective day 0
// and accrue to day 366.
//
// Assert: the receivable's book balance is >= 0 at every point; the customer's
// GL balance has been credited the part the receivable could not absorb; and
// Accrued equals expectedFromTimeline at 2% over 366 days minus what
// capitalisation removed. Assert on the ledger balances rather than only on the
// record, because the invariant at risk is that the two agree.
func TestARetroactiveRateCutRefundsWithoutDrivingTheReceivableNegative(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./deposit/ -run 'TestBackValue|TestWholeLife|TestABackdated|TestAFutureDated|TestAnAccountUnpriced|TestRerunning|TestAnUnadvanced|TestTheAdvancement|TestANeverPriced|TestUnderThirty360|TestARetroactiveRateCut' -v`
Expected: FAIL to build — `SetOverdraftTerms` takes five arguments, not six, and
`OverdraftTermsHistory` / `AccrueOverdraftTx`-through-a-wrapper do not exist yet.

- [ ] **Step 3: Strip the five fields from `Account`**

In `deposit/types.go`, delete `OverdraftLimit`, `Rate`, `UnarrangedRate`, `DayCount` and
`TermsEffectiveFrom` and their comments, and rewrite the two that stay:

```go
	// AccruedGross is the interest this account has produced over its WHOLE
	// LIFE, recomputed from its value-dated balance on every run. Accrued moves
	// by the change in it, which is what makes a backdated posting correct
	// itself: the days it takes effect over are re-derived with it in place,
	// gross moves, and the next run posts the difference.
	//
	// Unlike Accrued it is never decremented by capitalisation, and — unlike
	// before terms were effective-dated — it is never reset. There is no window
	// to start any more: the recompute opens at the account's opening terms row
	// and every day is re-derived at the terms that were actually in force on
	// it, so a repricing needs no fresh baseline. That is what widened the
	// retroactive-accrual window from the last repricing to account inception.
	//
	// Overflow is not a concern worth engineering around: this is int64
	// micro-minor-units, a EUR 10,000 overdraft at 10% produces on the order of
	// 1e11 a year, and int64 holds 9.2e18.
	AccruedGross interest.Accrued
	// LastAccrualDate is the business date accrual has been recomputed through.
	//
	// It enforces interest.Recompute's documented precondition: [from, to] must
	// cover at least one day, and a caller that recomputes an empty window over
	// a non-zero prior state is told to give the whole record back. That is the
	// only job it has left. It used to also carry the job of preventing a second
	// charge for a day already accrued; with a whole-life recompute the same
	// date produces the same gross and therefore a zero delta, so the same
	// invariant now has one fewer reason behind it.
	LastAccrualDate time.Time
```

Add the pair type at the end of the file:

```go
// AccountWithTerms is an account alongside the overdraft terms in force on a
// day — today, for every caller here.
//
// It exists because terms are resolved rather than cached on the row, and the
// API renders both together. Resolving one account at a time would make a
// listing N units of work; this pair is what lets Register do it in one.
//
// The alternative — keeping a copy of the current terms on the account — is the
// obvious shortcut and is the one this repo already argues against, in the
// schema, about a different field: a second copy of a fact creates the one
// thing a second copy always creates, the possibility that the two disagree.
type AccountWithTerms struct {
	Account Account
	Terms   OverdraftTerms
}
```

- [ ] **Step 4: Write the opening terms row in `OpenAccountTx`**

In `deposit/register.go`, in `OpenAccountTx`, after `tx.PutDepositAccount` and before the audit
append:

```go
	// Every account gets a terms row from birth, carrying the limit it was
	// opened with and zero rates.
	//
	// This is cleaner than treating "no rows" as a state the resolver has to
	// model: it makes the recompute window start uniform, it means the timeline
	// answers for every day the account has existed, and it costs nothing to
	// specify — interest.DayCount's zero value is already ACT365, so the
	// opening row needs no invented default, and a zero rate accrues nothing.
	// An account opened before any pricing existed therefore keeps exactly the
	// behaviour it had.
	opening := OverdraftTerms{
		AccountID:      acct.ID,
		EffectiveFrom:  ledger.DayStart(acct.CreatedAt),
		OverdraftLimit: overdraftLimit,
		CreatedAt:      acct.CreatedAt,
	}
	if err := opening.Validate(); err != nil {
		return Account{}, err
	}
	if err := tx.PutOverdraftTerms(ctx, r.bookID, opening); err != nil {
		return Account{}, err
	}
```

and delete `OverdraftLimit: overdraftLimit,` from the `Account` literal above it.

- [ ] **Step 5: Rewrite `SetOverdraftTermsTx`**

Replace the whole body (currently `register.go:240-317`) with:

```go
// SetOverdraftTermsTx is SetOverdraftTerms within a caller-supplied unit of work.
func (r *Register) SetOverdraftTermsTx(ctx context.Context, tx Tx, id AccountID, limit ledger.Amount, rate, unarranged interest.Rate, dc interest.DayCount, effectiveFrom time.Time) (OverdraftTerms, error) {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return OverdraftTerms{}, err
	}
	if acct.Status == Closed {
		return OverdraftTerms{}, ErrAccountClosed
	}

	row := OverdraftTerms{
		AccountID:      id,
		EffectiveFrom:  ledger.DayStart(effectiveFrom),
		OverdraftLimit: limit,
		Rate:           rate,
		UnarrangedRate: unarranged,
		DayCount:       dc,
		CreatedAt:      r.now(),
	}
	if err := row.Validate(); err != nil {
		return OverdraftTerms{}, err
	}

	// The receivable is created on the first non-zero rate ever set and reused
	// afterwards, including when a rate goes back to zero: the account may
	// already hold accrued interest, and discarding the receivable would
	// strand it. It stays on the account rather than moving to the terms row
	// for the same reason — there is one receivable per account, not one per
	// repricing.
	if rate > 0 && acct.InterestGL == "" {
		receivable, err := r.ensureReceivableTx(ctx, tx, acct)
		if err != nil {
			return OverdraftTerms{}, err
		}
		acct.InterestGL = receivable
		if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
			return OverdraftTerms{}, err
		}
	}

	if err := tx.PutOverdraftTerms(ctx, r.bookID, row); err != nil {
		return OverdraftTerms{}, err
	}
	if err := r.appendAuditTx(ctx, tx, ledger.EventOverdraftTermsSet, string(id), row); err != nil {
		return OverdraftTerms{}, err
	}
	return row, nil
}
```

Everything at the old `:259-308` is gone: the pre-accrual of the outgoing span, the
`now.Before(acct.LastAccrualDate)` clamp, the account re-read, and the
`TermsEffectiveFrom`/`AccruedGross`/`LastAccrualDate` reassignment. Each of those existed only to
protect a window that moved, and the window no longer moves — there is no boundary for a day to
fall between.

Rewrite the doc comment above `SetOverdraftTerms` to say what it now does: it appends a row rather
than overwriting columns, `effectiveFrom` may be backdated or future-dated, a backdated row is
picked up by the next recompute the same way a backdated posting is and the difference is posted as
ordinary delta interest with nothing rewritten, a future-dated row is inert until the runs reach it,
and the returned value is the row that was written — which is not necessarily the row in force now.
Note the risk explicitly: **a retroactive repricing moves interest already charged to a customer,
and the audit log is the only control on it.**

Update the `SetOverdraftTerms` wrapper's signature and return type to match.

- [ ] **Step 6: Resolve the limit in `balanceTx`**

```go
// balanceTx computes an account's three balances within a unit of work.
//
// The overdraft limit is RESOLVED from the account's terms timeline rather than
// read off the row, because it is effective-dated like the rate beside it: what
// a customer could spend last March is as much a fact about that March as what
// they were charged for it.
//
// It is the bounded as-of lookup rather than the whole timeline, because this
// runs on every withdrawal check and should not pay for history —
// ActiveHoldTotal above is a bounded aggregate for the same reason.
//
// ErrTermsNotFound is propagated rather than treated as a zero limit. Every
// account gets an opening row at OpenAccount, so the only way to miss is to ask
// about a day before the account existed, and silently reporting a spendable
// balance of Book - Holds for an account that has a facility is the kind of
// wrong answer that reads as a working system.
func (r *Register) balanceTx(ctx context.Context, tx Tx, acct Account) (Balance, error) {
	book, err := r.gl.BookBalanceTx(ctx, tx, acct.GLAccount)
	if err != nil {
		return Balance{}, err
	}
	holds, err := tx.ActiveHoldTotal(ctx, r.bookID, acct.ID, r.now())
	if err != nil {
		return Balance{}, err
	}
	terms, err := tx.GetOverdraftTermsAsOf(ctx, r.bookID, acct.ID, ledger.DayStart(r.now()))
	if err != nil {
		return Balance{}, err
	}
	return Balance{
		Book:      book,
		Holds:     holds,
		Available: book - holds + terms.OverdraftLimit,
	}, nil
}
```

`availableTx` needs no change of its own — it delegates.

- [ ] **Step 7: Rewrite the accrual**

Replace `accrueOverdraftAccountTx`'s head (currently `:1068-1090`):

```go
func (r *Register) accrueOverdraftAccountTx(ctx context.Context, tx Tx, acct Account, date time.Time) error {
	if acct.Status == Closed {
		return nil
	}

	// The whole timeline, in one read, resolved per day in Go below. The three
	// guards that used to sit here do not survive as a trio, and lumping them
	// together is how this would acquire a bug:
	//
	//   - Status == Closed is unchanged, above.
	//   - TermsEffectiveFrom.IsZero() meant "no window", and there is always a
	//     window now: the opening row. It is replaced by "no terms row in force
	//     on this day", below.
	//   - Rate <= 0 cannot survive as an early return, because an early return
	//     skips the whole run and a zero rate is now a property of a DAY. An
	//     account unpriced for its first year and priced thereafter is a case
	//     the previous model could not express at all. Two things replace it:
	//     the closure returns zero for a day whose resolved rate is zero, and
	//     the run is skipped entirely when NO row carries a non-zero rate —
	//     a scan over rows already in memory, which is what keeps a
	//     never-priced account from reading a series every night.
	rows, err := tx.ListOverdraftTermsForAccount(ctx, r.bookID, acct.ID)
	if err != nil {
		return err
	}
	if !anyPriced(rows) {
		return nil
	}

	// The window opens at the earliest row, which is the opening row, which is
	// inception. Every nightly run therefore re-derives every day the account
	// has had: O(days) per account per night, accepted deliberately at this
	// scale. The cost is arithmetic rather than I/O — the series is still one
	// query over the window — and checkpointing is the named successor.
	window := rows[0].EffectiveFrom

	// The advancement guard resolves its day count on `date`, because after
	// this change there is no single DayCount to ask: it is a terms field, and
	// the conventions genuinely disagree about whether a window advanced. Under
	// Thirty360 the 31st collapses onto the 30th, so Days(30th, 31st) is zero
	// while ACT365 says one — a run on the 31st is a no-op under one convention
	// and a real day under the other. `date` is the right answer: it is the
	// convention the customer's product is on for the day being accrued, and it
	// is the same figure the walk itself will use for that day.
	current, ok := termsAt(rows, date)
	if !ok {
		return nil
	}
	if current.DayCount.Days(acct.LastAccrualDate, date) <= 0 {
		return nil
	}

	series, err := r.gl.SeriesTx(ctx, tx, acct.GLAccount, window, date)
	if err != nil {
		return err
	}
	next, delta := interest.Recompute(series, window, date,
		interest.State{Accrued: acct.Accrued, Gross: acct.AccruedGross},
		func(balance ledger.Amount, from, to time.Time) interest.Accrued {
			// perDay has already cut the window to single days before any
			// Period runs, so `from` IS the day: this closure is a function of
			// the day as well as the balance, which is what a Period is for.
			day, ok := termsAt(rows, from)
			if !ok {
				return 0
			}
			return overdraftAccrual(balance, day, from, to)
		})
```

The rest of the function (the `acct.Accrued = next.Accrued` block onwards) is unchanged.

Change `overdraftAccrual`'s signature and body to read the terms row:

```go
func overdraftAccrual(book ledger.Amount, t OverdraftTerms, from, to time.Time) interest.Accrued {
	drawn := -book
	if drawn <= 0 {
		return 0
	}
	arranged := drawn
	if arranged > t.OverdraftLimit {
		arranged = t.OverdraftLimit
	}
	total := interest.Accrue(arranged, t.Rate, t.DayCount, from, to)
	if excess := drawn - arranged; excess > 0 {
		rate := t.UnarrangedRate
		if rate == 0 {
			rate = t.Rate
		}
		total += interest.Accrue(excess, rate, t.DayCount, from, to)
	}
	return total
}
```

Its doc comment gains one sentence: it takes the terms in force on the day it is accruing rather
than the account, because the limit and both rates are effective-dated and the day it is called for
is the day whose terms apply.

Update `AccrueOverdraft`'s doc comment: the *Idempotency* section's claim that the run recomputes
"the whole terms window" becomes "the whole of the account's life"; add that a backdated posting is
now trued up wherever it lands, including before a repricing, because the day it takes effect on is
re-derived at the terms that were in force on it.

- [ ] **Step 8: Add the three read methods**

At the end of the *Account Management* section of `register.go`:

```go
// OverdraftTermsHistory returns an account's whole terms timeline, oldest
// first. It is the point of making terms effective-dated: the history is
// inspectable rather than merely recoverable by replaying the audit log.
//
// Returns ErrAccountNotFound.
func (r *Register) OverdraftTermsHistory(ctx context.Context, id AccountID) ([]OverdraftTerms, error) {
	var out []OverdraftTerms
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetDepositAccount(ctx, r.bookID, id); err != nil {
			return err
		}
		var err error
		out, err = tx.ListOverdraftTermsForAccount(ctx, r.bookID, id)
		return err
	})
	return out, err
}

// GetAccountWithTerms returns an account alongside the terms in force today.
// Returns ErrAccountNotFound, and ErrTermsNotFound only for an account that
// somehow has no opening row.
func (r *Register) GetAccountWithTerms(ctx context.Context, id AccountID) (AccountWithTerms, error) {
	var out AccountWithTerms
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
		if err != nil {
			return err
		}
		terms, err := tx.GetOverdraftTermsAsOf(ctx, r.bookID, id, ledger.DayStart(r.now()))
		if err != nil {
			return err
		}
		out = AccountWithTerms{Account: acct, Terms: terms}
		return nil
	})
	return out, err
}

// ListAccountsWithTerms is GetAccountWithTerms over the whole book, in ONE unit
// of work. Resolving each account through its own View would make a listing N
// units of work over a store whose mem implementation refuses to nest them.
func (r *Register) ListAccountsWithTerms(ctx context.Context) ([]AccountWithTerms, error) {
	var out []AccountWithTerms
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		accounts, err := tx.ListDepositAccounts(ctx, r.bookID)
		if err != nil {
			return err
		}
		today := ledger.DayStart(r.now())
		out = make([]AccountWithTerms, 0, len(accounts))
		for _, acct := range accounts {
			terms, err := tx.GetOverdraftTermsAsOf(ctx, r.bookID, acct.ID, today)
			if err != nil {
				return err
			}
			out = append(out, AccountWithTerms{Account: acct, Terms: terms})
		}
		return nil
	})
	return out, err
}
```

**`TotalsTx` is deliberately untouched.** It buckets accounts by the sign of their balance
(`register.go:1379-1401`) and never reads the limit, so nothing about it changes when the limit
becomes a row. Confirm that by reading it rather than assuming, and leave it alone.

- [ ] **Step 9: Drop the five columns from `store/pg` and the schema**

In `store/pg/tx_deposit.go`, remove `overdraft_limit`, `overdraft_rate`, `unarranged_rate`,
`day_count` and `terms_effective_from` from the `INSERT`, the `DO UPDATE SET`, the parameter list,
`depositAccountColumns`, and `scanDepositAccount` (including the now-unused locals). Renumber the
`$n` placeholders.

In `store/pg/schema/0001_init.sql`, remove those five columns from `CREATE TABLE deposit_accounts`
and delete the `COMMENT ON COLUMN deposit_accounts.terms_effective_from` and
`COMMENT ON COLUMN deposit_accounts.overdraft_rate` blocks (their content now lives on
`overdraft_terms`). Rewrite `COMMENT ON COLUMN deposit_accounts.accrued_gross` — it currently ends
"it resets to zero whenever terms are set, because that is where the window starts", which is now
false:

```sql
COMMENT ON COLUMN deposit_accounts.accrued_gross IS
    'What this account has accrued over its WHOLE LIFE, same scale as '
    'accrued_interest. Overdraft interest is recomputed rather than '
    'incremented: every end-of-day re-derives every day since the account''s '
    'opening terms row from its value-dated balance, and accrued_interest '
    'moves by the change in this column. That is what makes a backdated '
    'posting correct itself — the days it takes effect over are re-derived '
    'with it in place, this figure moves, and the next run posts the '
    'difference. Unlike accrued_interest it is never decremented by '
    'capitalization, and unlike before terms became effective-dated it is '
    'never RESET: there is no window to restart, because every day is '
    're-derived at the terms that were actually in force on it (see the '
    'overdraft_terms table). A store that dropped this column would re-derive '
    'the account''s whole life as a fresh delta every night and charge the '
    'same interest over and over.';
```

- [ ] **Step 10: Update the deposit conformance fixture**

In `store/storetest/deposit.go`, the `OverdraftTermsRoundTrip` subtest currently writes the five
removed fields onto a `deposit.Account`. Rewrite it as `AccrualStateRoundTrip`, keeping only what
still lives on the row — `Accrued`, `AccruedGross`, `LastAccrualDate`, `InterestGL` — and keeping
the comment that explains why they are the fields a store could plausibly drop unnoticed. The
timeline itself is covered by `OverdraftTermsTimeline` from Task 2.

- [ ] **Step 11: Update `api`**

`api/dto_deposit.go`:

```go
func toDepositAccountDTO(a deposit.Account, t deposit.OverdraftTerms) depositAccountDTO {
```

reading `t.OverdraftLimit`, `t.Rate`, `t.UnarrangedRate`, `t.DayCount.String()` and keeping the
rest from `a`. Add a paragraph to the type's doc comment: the four terms fields are **resolved as
of today** from the account's effective-dated timeline rather than read off the account row, so a
future-dated repricing is not visible here until it takes effect — `GET …/overdraft-terms` (Task 6)
is what shows the whole timeline.

Add `EffectiveFrom` to the request:

```go
// setOverdraftTermsRequest carries an account's arranged overdraft limit and
// credit terms. Rate and UnarrangedRate are millionths, the same wire
// convention facilityDTO uses for a lending rate.
//
// effectiveFrom is the day the terms take economic effect, RFC3339, and may be
// in the past or the future: a repricing agreed on the 1st and entered on the
// 15th is the ordinary case, and a row effective next month is inert until the
// end-of-day runs reach it. Absent or null means today, which is what every
// caller before this field existed meant. A backdated row moves interest that
// has already been charged; the audit log is the only control on that.
type setOverdraftTermsRequest struct {
	Limit          int64      `json:"limit"`
	Rate           int64      `json:"rate"`
	UnarrangedRate int64      `json:"unarrangedRate"`
	DayCount       string     `json:"dayCount"`
	EffectiveFrom  *time.Time `json:"effectiveFrom"`
}
```

`api/handlers_deposit.go`: `handleGetDepositAccount` and `handleDepositStatus` call
`p.Deposit.GetAccountWithTerms`; `handleListDepositAccounts` calls `p.Deposit.ListAccountsWithTerms`;
`handleOpenDepositAccount` follows its `OpenAccount` with a `GetAccountWithTerms`;
`handleSetOverdraftTerms` resolves `effectiveFrom` (nil → `time.Now().UTC()`; the server has no
injected clock on this path — check how `handleTakeSnapshot` gets its date and follow it), calls the
six-argument `SetOverdraftTerms`, then re-reads with `GetAccountWithTerms` for its 200 body so the
response shape is unchanged. `handleDepositBalance` reads only `acct.Asset` and needs no change.

- [ ] **Step 12: Update `seed`**

`seed/seed.go:400` gains the effective date:

```go
	must(verde.Deposit.SetOverdraftTerms(ctx, bruno.ID, 50_000, 150_000, 350_000, interest.ACT365, b.clock.now()))
```

`seed/seed.go:436-441`: the comment claiming "His accrual window already opened back when
SetOverdraftTerms ran, above — that is what sets TermsEffectiveFrom" is now false. Replace that
sentence with one saying the window opens at account opening and every day since is re-derived, and
that the days before the SCT accrue zero because the drawn balance those days recompute against is
zero. Keep the rest of the comment, which is still true. Task 7 adds the second terms row.

- [ ] **Step 13: Run the tests and fix the fallout**

```bash
go build ./... && go vet ./... && gofmt -l .
go test ./deposit/ -v
go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
```

Expected: PASS everywhere. Where an existing test's expected figure moves, **derive the new one
from the timeline** and say in the test's comment why it moved; do not adjust a number to whatever
the code now prints. `api/server_test.go`'s two overdraft-terms tests should keep their figures —
the account opens and is priced on the same test-clock day, so the window opening at inception is
the same day it opened at before — but verify rather than assume, and if the comment at
`TestChargeOverdraftInterestEndpoint` ("The accrual window opens when the terms are set") is now
inaccurate, correct it.

Also confirm `TestRecomputeOnAnUnadvancedWindowGivesTheRecordBack` in `interest/` still passes
untouched: `interest/` is not modified by this task.

- [ ] **Step 14: Commit**

```bash
git add deposit store/pg store/storetest api seed
git commit -m "feat(deposit): resolve overdraft terms per accrual day

The four terms fields leave Account and become a timeline. The recompute window
opens at inception, so a backdated posting trues up across a repricing — the
defect this change exists for — and the window-reset machinery in
SetOverdraftTermsTx deletes itself along with the boundary it protected."
```

---

### Task 5: Lending — terms leave the facility

Same shape as Task 4, same reason it is one commit. It additionally closes a README *Next Work*
bullet as a side effect.

**Files:**
- Modify: `lending/types.go`, `lending/portfolio.go`, `lending/accrual.go`, `lending/schedule.go`
- Modify: `ledger/audit.go`
- Modify: `store/pg/tx_lending.go`, `store/pg/schema/0001_init.sql`
- Modify: `store/storetest/lending.go`
- Modify: `api/dto_lending.go`, `api/handlers_lending.go`
- Modify: `lending/*_test.go`, `api/server_test.go`, `seed/seed.go` (compile only)

**Interfaces:**
- Consumes: everything from Task 3, plus `deposit.AccountWithTerms` as the naming precedent.
- Produces:
  - `lending.FacilityWithTerms{Facility Facility; Terms FacilityTerms}`
  - `func (p *Portfolio) SetFacilityTerms(ctx context.Context, id FacilityID, rate interest.Rate, dc interest.DayCount, effectiveFrom time.Time) (FacilityTerms, error)` — and `…Tx`
  - `func (p *Portfolio) FacilityTermsHistory(ctx context.Context, id FacilityID) ([]FacilityTerms, error)`
  - `func (p *Portfolio) GetFacilityWithTerms(ctx, id) (FacilityWithTerms, error)`, `ListFacilitiesWithTerms(ctx) ([]FacilityWithTerms, error)`
  - `func BuildSchedule(f Facility, principal ledger.Amount, rate interest.Rate, firstDue time.Time) []Installment`
  - `ledger.EventFacilityTermsSet = "facility.terms_set"`

- [ ] **Step 1: Write the acceptance tests**

Add to `lending/accrual_test.go` and `lending/portfolio_test.go` (both `package lending_test`, with
an ordinary qualified `lending.` import — match the files you are adding to, and note this differs
from the deposit tests, which dot-import). Reuse the existing `disbursedLoan` / `disbursedLoanIn`
helpers at the top of `accrual_test.go` rather than building a new harness; read them first.

The two that carry the interesting assertions, in full:

```go
// Re-disbursement charges the L->R span. Disburse, accrue to L, repay in full
// at R, re-disburse at D, accrue: interest for L->R is charged, and R->D
// contributes nothing.
//
// This fails today, and it is a README Next Work bullet. Principal WAS drawn
// across L->R, so real interest accrued there, and clamping LastAccrualDate
// forward to D skipped it — interest never charged at all, the mirror image of
// the double-charge the clamp was added to prevent. A whole-life recompute over
// the value-dated drawn series includes L->R naturally and charges it, while
// R->D accrues nothing because drawn is zero across it. No special case, and no
// clamp: a general fix subsuming a special one.
func TestReDisbursementChargesTheSpanBeforeTheRepayment(t *testing.T) {
	ctx := context.Background()
	p, book, loan, customer := disbursedLoan(t)

	// Read the origination day off the facility rather than assuming the
	// fixture's clock, so this test does not break when the fixture moves.
	origin := ledger.DayStart(loan.OpenedAt)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	// L: accrue through day 10 with the loan fully drawn.
	assertNoError(t, p.Accrue(ctx, loan.ID, day(10)))
	atL, err := p.GetFacility(ctx, loan.ID)
	assertNoError(t, err)
	if atL.Accrued <= 0 {
		t.Fatalf("nothing accrued by L: %d", atL.Accrued)
	}

	// R: repay principal AND accrued interest in full on day 15, so drawn goes
	// to zero. Use the repayment helper the repay tests already use; the point
	// is only that the principal balance reaches zero here.
	repayInFull(t, p, book, loan, customer, day(15))
	drawn, err := p.Drawn(ctx, loan.ID)
	assertNoError(t, err)
	assertEqual(t, "drawn after repaying in full", drawn, ledger.Amount(0))

	// D: re-disburse on day 40. The facility was never closed, so this is
	// allowed — the guard is on drawn principal, not on status.
	_, err = p.Disburse(ctx, loan.ID, customer, day(40).AddDate(0, 1, 0), "Re-disbursement")
	assertNoError(t, err)

	beforeReaccrual, err := p.GetFacility(ctx, loan.ID)
	assertNoError(t, err)

	assertNoError(t, p.Accrue(ctx, loan.ID, day(41)))
	after, err := p.GetFacility(ctx, loan.ID)
	assertNoError(t, err)

	// The span L->R was drawn and is now charged. Today it is skipped entirely,
	// so this assertion is the one that fails before the change.
	if after.Accrued <= beforeReaccrual.Accrued {
		t.Errorf("re-accrual moved Accrued from %d to %d; the L->R span was not charged",
			beforeReaccrual.Accrued, after.Accrued)
	}

	// And R->D contributes nothing, because drawn is zero across it. Assert it
	// directly: accrue to day 40 from a facility that has not been
	// re-disbursed and confirm the figure is the same as at R.
	//
	// (Build the second facility with disbursedLoan again and drive it through
	// the same L and R, then accrue to day(40) WITHOUT re-disbursing.)
	control := accrueControlThroughIdleSpan(t, day)
	assertEqual(t, "an idle span accrues nothing", control.idleSpanAccrual, interest.Accrued(0))

	// The receivable still holds Minor() of the record, which is the invariant
	// a whole-life recompute must not break.
	receivable, err := book.BookBalance(ctx, after.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable", receivable, after.Accrued.Minor())
}

// SetFacilityTerms refuses a term loan that has a generated schedule, and
// allows a revolving line (which has none) and an undisbursed term loan (which
// has none yet).
//
// Refusing is better than documenting a divergence nobody would see: a repriced
// term loan whose schedule still reflects the old rate would let the final
// instalment silently absorb the difference, unnoticed until maturity.
func TestSetFacilityTermsRefusesADisbursedTermLoan(t *testing.T) {
	ctx := context.Background()

	// Disbursed term loan: refused, and the timeline is unchanged.
	p, _, loan, _ := disbursedLoan(t)
	_, err := p.SetFacilityTerms(ctx, loan.ID, 90_000, interest.ACT365, ledger.DayStart(loan.OpenedAt))
	if !errors.Is(err, lending.ErrScheduleWouldDiverge) {
		t.Errorf("repricing a disbursed term loan: got %v, want ErrScheduleWouldDiverge", err)
	}
	rows, err := p.FacilityTermsHistory(ctx, loan.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline after a refused repricing", len(rows), 1)

	// Undisbursed term loan: allowed, because there is no schedule yet — and
	// the schedule generated later uses the newer rate, which is right and may
	// still surprise someone reading only the origination record.
	pending, sub := openUndisbursedTermLoan(t, p)
	_, err = p.SetFacilityTerms(ctx, pending.ID, 90_000, interest.ACT365, ledger.DayStart(pending.OpenedAt))
	assertNoError(t, err)
	_ = sub

	// Revolving line: allowed, because it has no schedule to diverge from. Its
	// instalments are billing cycles appended one at a time, and a cycle
	// already billed is a statement of what WAS charged rather than a plan.
	line := openLine(t, p)
	_, err = p.SetFacilityTerms(ctx, line.ID, 200_000, interest.ACT365, ledger.DayStart(line.OpenedAt))
	assertNoError(t, err)
}
```

`repayInFull`, `accrueControlThroughIdleSpan`, `openUndisbursedTermLoan` and `openLine` are small
helpers to write beside the tests. `repay_test.go` and `portfolio_test.go` already open lines and
undisbursed loans — lift those bodies rather than reinventing them, and check whether a suitable
helper already exists before adding one. `accrueControlThroughIdleSpan` builds a second facility
through the same disburse/accrue/repay sequence, accrues it to `day(40)` **without** re-disbursing,
and returns the difference between the figure at `R` and the figure at day 40 — which must be zero.

The remaining three reuse the same shape:

```go
// A revolving line repriced mid-life accrues each day at the rate in force on
// it, and back-value across the repricing trues up — the lending mirror of
// TestBackValueAcrossARepricingTruesUp.
//
// Open and draw a line at R1 on day 0. Accrue to day 30. Reprice to R2
// effective day 30 (allowed: no schedule). Accrue to day 45. Post a further
// draw value-dated day 10, booked day 45. Accrue to day 46. Assert Accrued
// equals the day-by-day sum over 46 days with drawn stepping at 10 and the rate
// stepping at 30, computed with interest.Accrue one day at a time — the same
// discipline expectedFromTimeline uses on the deposit side.
func TestARevolvingLineRepricedMidLifeAccruesPerDay(t *testing.T)

// A facility accrues nothing before its first advance, WITHOUT needing a
// "first advance opens the window" state.
//
// Open a revolving line and do not draw on it. Accrue to day 60 and assert
// Accrued is 0 and Status is still Pending. Then draw on day 60, accrue to day
// 90, and assert Accrued equals thirty days on the drawn amount — the days
// before the draw contributed nothing, because the drawn series is zero across
// them. That state was an optimisation expressed as a guard, and the series
// already gives the right answer without it.
func TestAFacilityAccruesNothingBeforeItsFirstAdvance(t *testing.T)

// BuildSchedule is pinned to the rate it is given.
//
// Call BuildSchedule twice on the same Facility value and the same principal
// and first due date, at two different rates, and assert the two schedules
// differ in their Interest column — which is what makes the schedule a PLAN
// PINNED TO THE TERMS IN FORCE AT ACTIVATION rather than something derived from
// a facility row that can now be repriced out from under it. Assert also that
// each schedule's principal column still sums to the principal exactly, which
// is the property assertScheduleRepaysExactly already holds it to.
//
// This one is in `lending/schedule_test.go`, which is package lending.
func TestBuildScheduleUsesTheRateItIsGiven(t *testing.T)
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./lending/ -run 'TestReDisbursement|TestSetFacilityTerms|TestARevolvingLine|TestAFacilityAccrues|TestBuildSchedule' -v`
Expected: FAIL to build.

- [ ] **Step 3: Strip the three fields from `Facility`**

In `lending/types.go`, delete `Rate`, `DayCount` and `TermsEffectiveFrom`. Rewrite `AccruedGross`'s
comment (whole-life, never reset, window opens at origination) and `LastAccrualDate`'s (precondition
enforcement only) as the deposit mirrors from Task 4 Step 3. Add:

```go
// FacilityWithTerms is a facility alongside the terms in force on a day —
// today, for every caller here. It is deposit.AccountWithTerms' mirror and
// exists for the same reason: terms are resolved rather than cached on the row,
// and a listing that resolved each one through its own unit of work would be N
// units of work.
type FacilityWithTerms struct {
	Facility Facility
	Terms    FacilityTerms
}
```

- [ ] **Step 4: Write the opening row in `openTx`, and delete the window machinery**

In `lending/portfolio.go`, `openTx` keeps taking the rate and day count on the `Facility` literal
the openers build — but since those fields are gone, change `openTx`'s signature to
`openTx(ctx context.Context, tx Tx, f Facility, rate interest.Rate, dc interest.DayCount, subledger ledger.SubledgerID) (Facility, error)`,
have both openers pass them through, validate with `FacilityTerms{Rate: rate}.Validate()` in place
of the current `if f.Rate < 0` check, and after `tx.PutFacility`:

```go
	// Every facility gets a terms row from origination, so the recompute window
	// starts uniform across both credit layers and the timeline answers for
	// every day the facility has existed. Days before the first advance accrue
	// zero anyway, because drawn is zero across them — which is why the "first
	// advance opens the window" state below can go: it was an optimisation
	// expressed as a guard, and the series already gives the right answer.
	opening := FacilityTerms{
		FacilityID:    f.ID,
		EffectiveFrom: ledger.DayStart(f.OpenedAt),
		Rate:          rate,
		DayCount:      dc,
		CreatedAt:     f.OpenedAt,
	}
	if err := tx.PutFacilityTerms(ctx, p.bookID, opening); err != nil {
		return Facility{}, err
	}
```

In `DisburseTx`, delete lines `:318-341`'s window block entirely except `f.Status = Active` and the
`MaturityAt` assignment — the clamp, `f.LastAccrualDate = now`, `f.TermsEffectiveFrom = now` and
`f.AccruedGross = 0` all go, along with the long comment justifying the clamp. Replace that comment
with a short one saying the window no longer moves, so there is no boundary for a day to fall
between and no span for a clamp to skip.

In `DrawTx`, delete the same three assignments from the `if f.Status == Pending` branch, leaving
only `f.Status = Active` and the `PutFacility`.

Resolve the schedule's rate at disbursement:

```go
	terms, err := tx.GetFacilityTermsAsOf(ctx, p.bookID, f.ID, ledger.DayStart(p.now()))
	if err != nil {
		return ledger.Transaction{}, err
	}
	schedule := BuildSchedule(f, f.Commitment, terms.Rate, firstDue)
```

- [ ] **Step 5: Add `SetFacilityTerms`**

New section in `lending/portfolio.go`, after *Advancing money*:

```go
// SetFacilityTerms reprices a facility from a given day.
//
// It appends a row to the facility's terms timeline rather than overwriting
// anything, so the rate that was in force on any past day stays resolvable and
// the next recompute re-derives each day at its own. effectiveFrom may be
// backdated or future-dated: a backdated row is picked up by the next run the
// same way a backdated posting is, and the difference is posted as ordinary
// delta interest, while a future-dated row is inert until the runs reach it.
//
// # Why a disbursed term loan is refused
//
// A term loan's instalment schedule is generated once, at disbursement, from
// the rate in force then, and stored as rows. If accrual followed the timeline
// and the schedule did not, a repricing would make the plan and the actual
// accrual diverge — beyond the ordinary plan-versus-actual divergence this
// package already teaches, which 30/360 exists to keep small — and the final
// instalment would silently absorb the difference. Nobody would notice until
// maturity.
//
// So this returns ErrScheduleWouldDiverge for a term loan that has a generated
// schedule, and allows repricing freely on a revolving line (which has none)
// and on an undisbursed term loan (which has none yet). Refusing is better than
// documenting a divergence nobody would see; regenerating a schedule against
// repayments already posted against it needs versioned schedule rows and
// open-item allocation, and is its own topic.
//
// A term loan repriced BEFORE disbursement is allowed and changes the schedule
// generated later, which is right and may still surprise someone reading only
// the origination record. Both rows are in the audit log.
//
// Returns ErrFacilityNotFound, ErrFacilityClosed, ErrInvalidRate and
// ErrScheduleWouldDiverge.
func (p *Portfolio) SetFacilityTerms(ctx context.Context, id FacilityID, rate interest.Rate, dc interest.DayCount, effectiveFrom time.Time) (FacilityTerms, error) {
	var out FacilityTerms
	err := p.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = p.SetFacilityTermsTx(ctx, tx, id, rate, dc, effectiveFrom)
		return err
	})
	return out, err
}

// SetFacilityTermsTx is SetFacilityTerms within a caller-supplied unit of work.
func (p *Portfolio) SetFacilityTermsTx(ctx context.Context, tx Tx, id FacilityID, rate interest.Rate, dc interest.DayCount, effectiveFrom time.Time) (FacilityTerms, error) {
	f, err := tx.GetFacility(ctx, p.bookID, id)
	if err != nil {
		return FacilityTerms{}, err
	}
	if f.Status == Closed {
		return FacilityTerms{}, ErrFacilityClosed
	}
	if f.Kind == TermLoan {
		schedule, err := tx.ListInstallments(ctx, p.bookID, id)
		if err != nil {
			return FacilityTerms{}, err
		}
		if len(schedule) > 0 {
			return FacilityTerms{}, ErrScheduleWouldDiverge
		}
	}

	row := FacilityTerms{
		FacilityID:    id,
		EffectiveFrom: ledger.DayStart(effectiveFrom),
		Rate:          rate,
		DayCount:      dc,
		CreatedAt:     p.now(),
	}
	if err := row.Validate(); err != nil {
		return FacilityTerms{}, err
	}
	if err := tx.PutFacilityTerms(ctx, p.bookID, row); err != nil {
		return FacilityTerms{}, err
	}
	if err := p.appendAuditTx(ctx, tx, ledger.EventFacilityTermsSet, string(id), row); err != nil {
		return FacilityTerms{}, err
	}
	return row, nil
}
```

Add to `ledger/audit.go`, in the lending block beside `EventFacilityOpened`:

```go
	EventFacilityTermsSet  = "facility.terms_set"
```

- [ ] **Step 6: Rewrite the lending accrual**

Replace `accrueFacilityTx`'s head (`lending/accrual.go:78-98`) with the mirror of Task 4 Step 7:
`Status == Closed` stays; `TermsEffectiveFrom.IsZero()` and `Rate <= 0` are replaced by the
timeline read plus `anyPriced`; `window := rows[0].EffectiveFrom`; the advancement guard resolves
its day count with `termsAt(rows, date)`; the closure resolves `termsAt(rows, from)` and calls
`interest.Accrue(drawn, day.Rate, day.DayCount, from, to)`, returning 0 when the day precedes the
first row.

Update the `Accrue` doc comment's *Idempotency* section the same way deposit's was.

- [ ] **Step 7: Give `BuildSchedule` an explicit rate**

```go
func BuildSchedule(f Facility, principal ledger.Amount, rate interest.Rate, firstDue time.Time) []Installment {
```

with `f.Rate` replaced by `rate` at the two sites (`:53`, `:57`). Add a paragraph to its doc
comment:

```go
// # The rate is an argument, and that is the point
//
// It used to be read off the facility. Taking it explicitly makes visible what
// was implicit: the schedule is a PLAN PINNED TO THE TERMS IN FORCE AT
// ACTIVATION. Interest now follows an effective-dated timeline and the schedule
// does not, which is exactly why repricing a term loan that already has one is
// refused — see ErrScheduleWouldDiverge. This argument and that error are the
// same sentence approached from opposite ends.
```

- [ ] **Step 8: Add the read methods, update `store/pg`, the schema and the fixture**

Add `FacilityTermsHistory`, `GetFacilityWithTerms` and `ListFacilitiesWithTerms` mirroring Task 4
Step 8.

In `store/pg/tx_lending.go`, remove `rate`, `day_count` and `terms_effective_from` from the
`INSERT`, `DO UPDATE SET`, parameter list, `facilityColumns` and `scanFacility`; renumber the
placeholders. In the schema, drop those three columns from `facilities`, delete the
`facilities.terms_effective_from` comment, and rewrite `facilities.rate`'s comment as
`facility_terms.rate`'s (Task 3) — keeping the `min_payment` sentence, which is about a column that
stays, by moving it onto a `COMMENT ON COLUMN facilities.min_payment`. Rewrite
`facilities.accrued_gross` the way `deposit_accounts.accrued_gross` was rewritten.

In `store/storetest/lending.go`, drop the three fields from the `FacilityRoundTripsEveryField`
fixture and its `check` closure, and drop the `undrawn terms effective from` assertion from the
open-ended-line subtest, keeping the `accrued gross` one.

- [ ] **Step 9: Update `api`**

`api/dto_lending.go`: `toFacilityDTO(f lending.Facility, t lending.FacilityTerms)` reading
`t.Rate` and `t.DayCount.String()`; the same resolved-as-of-today paragraph on the type's doc
comment. `api/handlers_lending.go`: the facility read handlers switch to
`GetFacilityWithTerms` / `ListFacilitiesWithTerms`.

**No new lending HTTP route.** The spec's *Consumers* table lists an endpoint for the deposit
timeline only, and `SetFacilityTerms` is deliberately Go-only surface for now: a facility repricing
UI has no design behind it, and adding a route to avoid the appearance of unused code would be
scope the spec explicitly did not take. It is exercised by the acceptance tests in Step 1.

- [ ] **Step 10: Run the tests and fix the fallout**

```bash
go build ./... && go vet ./... && gofmt -l .
go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
```

Expected: PASS. `seed/seed.go` needs no functional change here — the openers keep their `rate, dc`
parameters — but confirm it compiles. Existing lending accrual figures should not move for a
facility that is never repriced: the window opening at origination adds only days across which
drawn is zero. **Verify that**; if a figure does move, find out why before touching the expectation.

- [ ] **Step 11: Commit**

```bash
git add lending ledger/audit.go store/pg store/storetest api
git commit -m "feat(lending): resolve facility terms per accrual day

Terms become a timeline, the window opens at origination, and the clamps in
DisburseTx and DrawTx go with the boundary they protected — which also charges
the re-disbursement span the clamp used to skip, closing a README Next Work
bullet as a side effect of a general fix.

Repricing a term loan that already has a schedule is refused rather than
allowed to diverge from it silently."
```

---

### Task 6: Expose the timeline — endpoint, web view, hint key

The point of the change is that the timeline is inspectable, and the cheapest proof is to show it.

**Files:**
- Modify: `api/dto_deposit.go`, `api/handlers_deposit.go`, `api/server_test.go`
- Modify: `web/src/lib/types.ts`, `web/src/lib/api/endpoints.ts`, `query-keys.ts`, `hooks.ts`
- Create: `web/src/components/overdraft-terms-card.tsx`
- Modify: `web/src/app/participants/[pid]/deposit-accounts/[did]/page.tsx`
- Modify: `web/src/components/hint-content.ts`

**Interfaces:**
- Consumes: `deposit.Register.OverdraftTermsHistory` (Task 4).
- Produces: `GET /participants/{pid}/deposit-accounts/{did}/overdraft-terms` → `overdraftTermsDTO[]`
  with JSON keys `effectiveFrom`, `overdraftLimit`, `rate`, `unarrangedRate`, `rateScale`,
  `dayCount`, `createdAt`; the web `OverdraftTerms` interface, `useOverdraftTerms` hook, and the
  `effective-dated-terms` hint key.

- [ ] **Step 1: Write the failing API test**

In `api/server_test.go`:

```go
// TestOverdraftTermsTimelineEndpoint covers GET
// /participants/{pid}/deposit-accounts/{did}/overdraft-terms: the account's
// whole effective-dated timeline, oldest first, including the opening row
// every account gets at OpenAccount.
//
// It is the endpoint the change exists to make possible — before terms were
// rows, "what did this account's product say on 15 July?" had no answer to
// serve — so it also asserts that a FUTURE-dated row is on the timeline while
// the account's own resolved-as-of-today fields still show the current ones.
func TestOverdraftTermsTimelineEndpoint(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts",
		`{"name":"Alice","asset":"EUR","overdraftLimit":50000}`, http.StatusCreated)["id"].(string)

	// The opening row alone.
	first := doJSONArray(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did+"/overdraft-terms", "", http.StatusOK)
	assertEqual(t, "opening timeline length", len(first), 1)
	opening := first[0].(map[string]any)
	assertEqual(t, "opening limit", int64(opening["overdraftLimit"].(float64)), int64(50_000))
	assertEqual(t, "opening rate", int64(opening["rate"].(float64)), int64(0))

	// A priced row today, and a future-dated one.
	doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/overdraft-terms", `{
		"limit":50000,"rate":150000,"unarrangedRate":350000,"dayCount":"ACT/365",
		"effectiveFrom":"2025-01-15T00:00:00Z"
	}`, http.StatusOK)
	doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/overdraft-terms", `{
		"limit":50000,"rate":180000,"unarrangedRate":350000,"dayCount":"ACT/365",
		"effectiveFrom":"2099-01-01T00:00:00Z"
	}`, http.StatusOK)

	rows := doJSONArray(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did+"/overdraft-terms", "", http.StatusOK)
	assertEqual(t, "timeline length", len(rows), 3)
	last := rows[len(rows)-1].(map[string]any)
	assertEqual(t, "the future row is on the timeline", int64(last["rate"].(float64)), int64(180_000))
	assertEqual(t, "rate scale is on the wire", int64(last["rateScale"].(float64)), int64(interest.RateScale))

	// …but the account itself still reports the rate in force TODAY.
	acct := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "resolved as of today", int64(acct["overdraftRate"].(float64)), int64(150_000))
}
```

Check whether `api/server_test.go` already has a `doJSONArray` helper; if not, add one beside
`doJSON` that decodes into `[]any`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./api/ -run TestOverdraftTermsTimelineEndpoint -v`
Expected: FAIL — 404 on the GET.

- [ ] **Step 3: Add the DTO and the route**

In `api/dto_deposit.go`:

```go
// overdraftTermsDTO is one row of an account's effective-dated terms timeline.
//
// effectiveFrom is the day the row takes economic effect and createdAt is when
// it was entered: the booking-date/value-date distinction applied to
// configuration, and the reason a repricing agreed on the 1st and entered on
// the 15th can be recorded honestly. A row whose effectiveFrom is in the future
// appears here before it applies; the account's own resolved fields show the
// row in force today.
type overdraftTermsDTO struct {
	AccountID      string    `json:"accountId"`
	EffectiveFrom  time.Time `json:"effectiveFrom"`
	OverdraftLimit int64     `json:"overdraftLimit"`
	Rate           int64     `json:"rate"`
	UnarrangedRate int64     `json:"unarrangedRate"`
	RateScale      int64     `json:"rateScale"`
	DayCount       string    `json:"dayCount"`
	CreatedAt      time.Time `json:"createdAt"`
}

func toOverdraftTermsDTO(t deposit.OverdraftTerms) overdraftTermsDTO {
	return overdraftTermsDTO{
		AccountID:      string(t.AccountID),
		EffectiveFrom:  t.EffectiveFrom,
		OverdraftLimit: int64(t.OverdraftLimit),
		Rate:           int64(t.Rate),
		UnarrangedRate: int64(t.UnarrangedRate),
		RateScale:      interest.RateScale,
		DayCount:       t.DayCount.String(),
		CreatedAt:      t.CreatedAt,
	}
}
```

In `api/handlers_deposit.go`, register the route beside the existing POST and add the handler:

```go
	mux.HandleFunc("GET /participants/{pid}/deposit-accounts/{did}/overdraft-terms", s.handleListOverdraftTerms)
```

```go
// handleListOverdraftTerms returns an account's whole effective-dated terms
// timeline, oldest first — including the opening row every account gets at
// OpenAccount, which carries the limit it was opened with and zero rates.
func (s *Server) handleListOverdraftTerms(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	rows, err := p.Deposit.OverdraftTermsHistory(r.Context(), deposit.AccountID(r.PathValue("did")))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]overdraftTermsDTO, len(rows))
	for i, t := range rows {
		out[i] = toOverdraftTermsDTO(t)
	}
	writeJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 4: Run the API test**

Run: `go test ./api/ -v` → Expected: PASS. Then `go test ./...`.

- [ ] **Step 5: Add the hint key**

In `web/src/components/hint-content.ts`, add a new entry (alphabetical placement is not enforced;
put it next to `overdraft-interest`):

```ts
  "effective-dated-terms": {
    title: "Effective-dated terms",
    body: `A product's terms are a **timeline**, not a set of columns: one row per repricing, each carrying the day it takes effect, and none ever overwritten.

The reason is that every interest figure is a function of three things — account state, event history, and configuration. The first two are immutable and replayable here. If the third could be edited in place, that whole investment is undone, because "what did this account's product say on 15 July?" stops having a stable answer.

There is a sharper consequence than an audit weakness. While terms were mutable, the [[interest-accrual|accrual]] recompute could only reach back to the last repricing — reaching further would re-derive old days at *today's* rate. So a [[booking-date-vs-value-date|back-dated]] posting landing before the last repricing was silently never trued up. With a timeline, every day is re-derived at the terms actually in force on it, so the window opens at account inception and the correction always lands.

Each row carries **two** dates, and the pair is the [[booking-date-vs-value-date|booking-date/value-date]] distinction applied to configuration: when the repricing was *entered*, and when it takes *economic effect*. They can differ in either direction — a rate agreed on the 1st and keyed in on the 15th is backdated; a rate agreed for next month is future-dated and simply sits inert until the end-of-day runs reach it.`,
  },
```

Verify the two wiki-links it uses (`interest-accrual`, `booking-date-vs-value-date`) are real keys
before committing — grep `hint-content.ts` for each. If `booking-date-vs-value-date` is not the
actual key name, use whichever key the *Booking date vs. value date* hint is registered under.

- [ ] **Step 6: Wire the web data layer**

`web/src/lib/types.ts`, after `DepositAccount`:

```ts
// One row of an account's effective-dated overdraft terms timeline. rate and
// unarrangedRate are millionths of rateScale, the same convention
// DepositAccount uses. effectiveFrom is when the row takes economic effect and
// createdAt is when it was entered — the two can differ in either direction, so
// a row whose effectiveFrom is in the future is on this list before it applies.
export interface OverdraftTerms {
  accountId: string;
  effectiveFrom: string;
  overdraftLimit: number;
  rate: number;
  unarrangedRate: number;
  rateScale: number;
  dayCount: string;
  createdAt: string;
}
```

`endpoints.ts`, in the deposit section:

```ts
export function listOverdraftTerms(
  pid: string,
  did: string,
): Promise<OverdraftTerms[]> {
  return request(
    "GET",
    `/participants/${pid}/deposit-accounts/${did}/overdraft-terms`,
  );
}
```

`query-keys.ts`, beside `snapshots`:

```ts
  overdraftTerms: (pid: string, did: string) =>
    ["participants", pid, "deposit-accounts", did, "overdraft-terms"] as const,
```

`hooks.ts`:

```ts
export function useOverdraftTerms(pid: string, did: string) {
  return useQuery({
    queryKey: qk.overdraftTerms(pid, did),
    queryFn: () => api.listOverdraftTerms(pid, did),
    enabled: pid !== "" && did !== "",
  });
}
```

- [ ] **Step 7: Build the card and mount it**

Create `web/src/components/overdraft-terms-card.tsx` following `SnapshotsCard`'s shape exactly —
`Card`/`CardHeader`/`CardTitle`/`CardContent`, a `Column<OverdraftTerms>[]`, `DataTable` with
`isLoading`, `ErrorState` on error — but read-only (no form). Columns: **Effective from**
(`formatDate`), **Limit** (`<Money amount={t.overdraftLimit} asset={asset} />`), **Arranged**
(`formatRate(t.rate, t.rateScale)`), **Unarranged** (`formatRate(t.unarrangedRate, t.rateScale)`),
**Day count** (`t.dayCount`), **Entered** (`formatDate(t.createdAt)`). Title
`"Overdraft terms"` with `<Hint id="effective-dated-terms" />`. Empty text:
`"No terms yet."` — though in practice every account has an opening row, so an empty table means
something is wrong.

Import the same helpers the deposit account page already uses (`formatRate` from `@/lib/rate`,
`formatDate` from wherever `SnapshotsCard` gets it — check that page's import block and match).

Mount it in `web/src/app/participants/[pid]/deposit-accounts/[did]/page.tsx` between
`<SnapshotsCard …/>` and `<StatementCard …/>`:

```tsx
          <OverdraftTermsCard pid={pid} did={did} asset={asset} />
```

- [ ] **Step 8: Verify the web app**

```bash
cd web
npm run typecheck && npm run lint && npm run test && npm run build
```

Expected: all clean. `npm run test` is what catches a `[[wiki-link]]` to a missing key.

Then **load a page** — the mechanical rule at the top of this plan is not satisfied by a green
build. Start the backend (`go run ./cmd/server` from the repo root), `npm run dev`, and open a
seeded overdraft account (Bruno at Banca Verde) to confirm the card renders its timeline and the
hint popover opens.

- [ ] **Step 9: Commit**

```bash
git add api web
git commit -m "feat(api,web): expose the overdraft terms timeline

GET .../overdraft-terms plus a read-only card on the deposit account page. The
point of effective-dating terms is that the history is inspectable rather than
merely recoverable by replaying the audit log, and this is the cheapest proof."
```

---

### Task 7: Seed a demonstrated repricing

So the dev app shows back-value across a repricing on load, and the acceptance story has a fixture
that exists in the running system.

**Files:**
- Modify: `seed/seed.go`
- Modify: `seed/seed_test.go` (only if a golden figure moves)

**Interfaces:**
- Consumes: `deposit.Register.SetOverdraftTerms` with `effectiveFrom` (Task 4).
- Produces: no new API; Bruno's account gains a second terms row.

- [ ] **Step 1: Add the second terms row**

In `lendingShowcase`, after the existing 45-day run and the interest charge (`seed/seed.go:467-468`),
before the final 15-day run:

```go
	// --- Bruno, repriced mid-life -------------------------------------------
	// The arranged rate moves from 15% to 18%, effective TWENTY DAYS AGO — a
	// repricing agreed on one date and entered on another, which is the
	// ordinary case and the one mutable terms could not represent at all.
	//
	// This is the seed's demonstration of the whole change. The next end-of-day
	// re-derives every day since Bruno's account opened, charges the last
	// twenty of them at 18% instead of 15%, and posts the difference as
	// ordinary delta interest. Nothing is rewritten and both terms rows stay on
	// the timeline, which the account page now renders.
	//
	// Before terms were effective-dated this could not happen: the recompute
	// window would have been reset to today and the twenty days behind it would
	// have kept the old rate forever.
	must(verde.Deposit.SetOverdraftTerms(ctx, bruno.ID, 50_000, 180_000, 350_000,
		interest.ACT365, b.clock.now().AddDate(0, 0, -20)))
```

- [ ] **Step 2: Rewrite the stale comment block**

`seed/seed.go:436-455`'s explanation now needs to account for the repricing as well as for the
window opening at inception. The sentence about `TermsEffectiveFrom` was already corrected in Task
4; extend the paragraph about Bella's 30-day span with a note that Bruno's final accrued figure now
reflects two rates — 15% up to the repricing's effective date and 18% after it — so changing either
Bella's span or the repricing's twenty-day offset changes his number.

- [ ] **Step 3: Run the seed tests**

```bash
go test ./seed/ -v
go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
```

`TestPopulateIsIdempotent`, `TestDeterministicIDs` and `TestPopulateAfterResetRebuildsTheSameDataset`
must all pass — the repricing is deterministic because the seed's clock is. If any asserts a
specific accrued figure, re-derive it: 15% ACT/365 up to the effective day, 18% after.

- [ ] **Step 4: Verify in the running app**

Start the backend and `npm run dev`, open Bruno's account at Banca Verde, and confirm the terms card
shows three rows (opening, 15%, 18%) with the 18% row's *Effective from* twenty days before its
*Entered* date.

- [ ] **Step 5: Commit**

```bash
git add seed
git commit -m "feat(seed): demonstrate a backdated repricing on Bruno's overdraft

15% to 18%, effective twenty days before it was entered. The next end-of-day
charges those twenty days at the new rate as a delta — which mutable terms
could not have done, because the window would have reset to today."
```

---

### Task 8: The domain claims, across every layer

`CLAUDE.md` makes a domain change a documentation change in four places. The schema comments landed
with Tasks 2, 3, 4 and 5; this task closes the other three.

**Files:**
- Modify: `README.md`
- Modify: `web/src/components/hint-content.ts`
- Modify: `web/src/lib/quiz/chapters/08-account-lifecycle-and-overdraft.ts`
- Modify: `web/src/lib/quiz/chapters/18-interest-overdrafts-and-arrears.ts`

**Interfaces:**
- Consumes: the `effective-dated-terms` hint key added in Task 6 — quiz explanations may link to it.
- Produces: no code.

- [ ] **Step 1: README — *Overdraft***

In the *Overdraft* section (`README.md:645`), after the available-balance formula block, add:

```markdown
Both the limit and the rate are **effective-dated**. They are not columns on the account: they are
rows on a small per-account timeline, one per repricing, each carrying the day it takes effect, and
none ever overwritten. So the available-balance formula above resolves a limit *as of a day* rather
than reading one off the account, and "what could this customer spend, and what were they charged,
on 15 July?" is a question with a stable answer. See [Interest
Accrual](#daily-accrual-and-the-precision-split) for why that matters to more than an auditor.
```

- [ ] **Step 2: README — *Daily Accrual and the Precision Split***

Add a subsection after the precision-split table:

```markdown
#### The Recompute Window Opens at Inception

Accrual is a **recomputation**, not an increment: every run re-derives the account's or facility's
whole history from its value-dated balance and posts the change in the rounded value. That is what
makes a backdated posting correct itself — the days it takes effect over are re-derived with it in
place, the gross moves, and the difference is posted as a true-up.

How far back "whole history" reaches is decided by the product terms. While terms were mutable
columns, the window had to start at the last repricing: reaching further would have re-derived old
days at *today's* rate. That bound was an accident of the storage, and it had a cost the customer
could not see — a back-value landing before the last repricing was **never trued up at all**.

Because terms are now a timeline, every day can be re-derived at the terms that were actually in
force on it, and the window opens at account opening (or, for a facility, at origination). The days
before the first advance accrue nothing on their own, because the drawn balance across them is zero.

The cost of that is real and is accepted deliberately: every nightly run re-derives every day the
account has ever had, so a ten-year-old account is roughly 3,650 day-iterations a night. The cost is
arithmetic rather than I/O — the balance series is still one query over the window — and at this
scale it is nothing. **Checkpointing** is what removes it, and the end-of-day snapshots this system
already writes and never reads are the raw material; see [End-of-Day
Snapshots](#end-of-day-snapshots) and *Next Work*.
```

- [ ] **Step 3: README — *Next Work***

Three edits in the *Next Work* list:

1. **Delete** the *Effective-dated product terms* bullet outright.
2. **Delete** the *Re-disbursement drops the outstanding span instead of double-charging it* bullet:
   the clamp it describes is gone, and a whole-life recompute charges the span it skipped.
3. **Rewrite** the *The recompute window is unbounded* bullet, which is reframed rather than closed:

```markdown
- **The recompute window is unbounded, now by choice.** It opens at account opening for a deposit
  overdraft and at origination for a lending facility, and nothing resets it — so a long-lived
  account or facility walks more days of arithmetic every night. That bound used to be an accident:
  it happened to fall at the last repricing, because mutable terms forced it there. Now it is a
  deliberate trade — a window at inception is what makes a backdated posting correct across a
  repricing — and its successor is named: **checkpointing**, anchored to a ledger sequence position
  rather than a wall clock, and invalidated by a backdated posting. The end-of-day snapshots this
  system already writes are the raw material a checkpoint would read from.
```

- [ ] **Step 4: hint-content — `overdraft` and `overdraft-interest`**

`overdraft`: after the worked example block, add a sentence — the limit is
[[effective-dated-terms|effective-dated]], so the figure the formula uses is the one in force on the
day being asked about, not a column that can be edited out from under a past calculation.

`overdraft-interest`: the second paragraph already states the recompute-and-true-up behaviour
without qualification. It **needs checking rather than rewriting** — this change makes that sentence
true where it used to be window-bounded. Read it, confirm nothing in it is now wrong, and append:

```
Both the limit and the rates are [[effective-dated-terms|effective-dated]], which is what lets the recompute reach all the way back to account opening: every day is re-derived at the terms that were actually in force on it, so a back-dated posting is trued up wherever it lands — including before a repricing, which used to be a line the correction silently stopped at.
```

Check `web/src/components/concept-links.ts` for whether new keys need registering anywhere beyond
`hint-content.ts`; `npm run test` will say.

- [ ] **Step 5: Quiz — chapter 08**

Chapter 08 currently has 20 questions, so it may gain **two** (the band is 18–22). Its `overdraft`
tag is already at 3× (the cap), so the new questions must carry other tags. Add:

- A `truefalse`, difficulty `core`, tagged `balance-available`: *"An account's overdraft limit is a
  single value stored on the account, so changing it changes what the available balance was on every
  past day as well."* → **False**. Explanation: the limit is a row on an
  [[effective-dated-terms|effective-dated timeline]], one per repricing, so the available balance on
  a past day resolves the limit that was in force on it. A column would have made every past
  available-balance figure move the moment the limit was changed.
- A `multi`, difficulty `challenge`, tagged `booking-date-vs-value-date` (verify the exact tag string
  used elsewhere in the file before using it): *"A bank agrees a new overdraft rate with a customer
  on the 1st and an operator keys it in on the 15th. What does the system record?"* with the correct
  answer being **two dates on one terms row — the 1st as the day it takes effect, the 15th as the
  day it was entered — and the next end-of-day charges the 1st-to-15th span at the new rate as a
  delta**, and distractors: a single row dated the 15th with the first fortnight lost; a reversal of
  every accrual since the 1st followed by a re-posting; a refusal, because a rate cannot be
  backdated.

- [ ] **Step 6: Quiz — chapter 18**

Chapter 18 has 20 questions and may gain **two**. `repayment-allocation`, `day-count`,
`capitalization` and `accrued-interest` are all at the 3× cap; `interest-accrual` is at 1 and
`unarranged-rate` at 1, so tag the new pair `interest-accrual` (taking it to 3) and
`unarranged-rate` (to 2), or introduce a new tag — a new tag is safer and lifts the distinct-tag
count. Add:

- A `truefalse`, difficulty `core`, tagged `interest-accrual`: *"A posting that arrives value-dated
  to a day before the account's last repricing is trued up by the next accrual run, the same way any
  other back-dated posting is."* → **True**, and note that this is true *because* terms are
  effective-dated: with mutable terms the recompute window started at the last repricing, and a
  back-value landing behind it was silently never corrected.
- A `numeric`, difficulty `challenge`, tagged `interest-accrual`: an account overdrawn by a fixed
  amount, priced at one rate for a stated number of days and repriced to another for the rest,
  asking for the total accrual over the whole span in micro-minor-units. Compute the expected value
  as `days1 × drawn × rate1 / 365 + days2 × drawn × rate2 / 365` with each day's division truncated,
  matching `interest.Accrue`'s per-day integer arithmetic — **verify the figure by writing the same
  sum in a scratch Go test before putting it in the quiz**, because a wrong golden number in a quiz
  is a taught falsehood. The explanation says the total is per-day and per-terms-row, not one rate
  applied across the window.

- [ ] **Step 7: Verify every layer**

```bash
cd web
npm run test          # wiki-links in hint bodies AND quiz explanations; the diversity bounds
npm run typecheck && npm run lint && npm run build
```

Expected: clean. `diversity.test.ts` must still pass on both chapters — 18–22 questions, ≥8 distinct
tags, no tag over 3×, all three difficulty tiers present.

Then load the app and open both the *Learn* quiz for chapters 08 and 18 and Bruno's deposit account
page, to confirm the new hints resolve and the quiz renders.

- [ ] **Step 8: Final full verification**

```bash
cd /Users/raphaelgruber/Git/cbs
gofmt -l . && go vet ./... && go test ./...
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
cd web && npm run test && npm run typecheck && npm run lint && npm run build
```

Every one of these must pass. A change that passes only the mem run has, by definition, made the two
stores diverge.

- [ ] **Step 9: Commit**

```bash
git add README.md web/src/components/hint-content.ts web/src/lib/quiz
git commit -m "docs: effective-dated terms, across every layer

README (Overdraft, Daily Accrual, Next Work), the two hint keys, and two
questions each in chapters 08 and 18. Next Work loses the effective-dated-terms
and re-disbursement bullets and reframes the unbounded-window one as a
deliberate cost with checkpointing named as its successor."
```

---

## Failure modes to keep in view while implementing

These are the spec's, restated as things that should show up in a test or a comment rather than be
discovered later:

- **A retroactive repricing moves interest already charged to a customer.** By design, and as a
  delta rather than a rewrite — but the audit log is the only control on it. Dual control is out of
  scope. Say so in `SetOverdraftTerms`'s doc comment (Task 4 Step 5).
- **Nightly cost grows for the life of the account.** `O(days since opening)` per account per run.
  Named in the README (Task 8) and in the accrual comment (Task 4 Step 7).
- **A backdated repricing can drive a large negative delta.** `correctOverdraftAccrualTx` already
  handles a negative delta by settling part of the record in cash, but a retroactive rate *cut*
  across years of history is a bigger negative than that path has seen. Its bounds hold; **add a
  test in Task 4 that drives a large negative delta through it** and asserts the receivable is not
  driven below zero and the refund lands on the customer's account.
- **Under `Thirty360`, a terms row effective on a 31st appears to be ignored for a day.** It follows
  from the convention, not from this change. Pinned by test in Task 4 Step 1.
- **A term loan repriced before disbursement changes its schedule silently.** Allowed — there is no
  schedule yet — but the schedule generated later uses the newer rate. Recorded in
  `SetFacilityTerms`'s doc comment (Task 5 Step 5).
- **Plan and actual still diverge on a revolving line after a repricing.** There is no schedule to
  disagree with, so nothing is wrong: the minimum payment comes from `MinPayment` and the interest
  charged follows the timeline. They always moved independently.
