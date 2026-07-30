# Product Catalogue — Design

The third topic taken from `cbs-vs-book.md`, and the successor its predecessor
declared out of scope in as many words: *"No `product`/`product_version` table,
no content hash, no non-overlapping-interval exclusion constraint, no
pinned-vs-floating parameter binding (§24.1.3), no overlays (§24.1.4), no
resolution log. These are per-instance terms: one timeline per account and per
facility, not a catalogue shared across them."*
(`2026-07-30-effective-dated-terms-design.md:70`)

That spec made configuration immutable. This one makes it *shared*. They are
different properties and the second is not implied by the first: an account whose
own terms timeline is a perfect audit record still has no answer to "what product
is this?", and repricing a book of ten thousand accounts still means ten thousand
writes with no artefact naming the decision that caused them.

## Goal

Today — and after the effective-dated terms work lands — every term an account has
is a term about *that account*. `OpenAccount` takes an overdraft limit
(`deposit/register.go:139`) and `SetOverdraftTerms` takes a rate, an unarranged
rate and a day-count convention (`:240`), and the timeline those writes build is
per-`AccountID` (`deposit/store.go:54-56`). Nothing in the system records that
fifty accounts are all *the same product* priced the same way, so:

- **A product-wide reprice is not expressible.** It is N per-account writes with
  no shared cause, and afterwards nothing distinguishes "the Basic Current Account
  rate moved to 14.9%" from fifty coincidences.
- **There is no artefact to name.** §24.1.1's argument is that a financial
  calculation is a function of account state, event history *and configuration*.
  The previous spec made the third input immutable per instance; it did not make
  it a *thing* with a name, a version and a publication event.
- **The pinned/floating distinction has nowhere to live.** A rate is a price the
  bank sets for a product. A limit is an underwriting decision about one customer.
  Today both are the same four fields on the same row, which is why
  `SetOverdraftTerms` takes them in one call and why raising one customer's limit
  looks identical to repricing the bank's whole overdraft book.

This design adds a catalogue: named products, each with an effective-dated
timeline of immutable published versions, that accounts **bind to** — with the
pricing group floating and the limit pinned, and a per-account overlay for the
negotiated exception.

The plain-language version, which is also the answer to the question that started
this: an account is opened *from* a product, and the product is a template only in
the sense that the parts of it which should keep changing after the account exists
keep changing, and the parts which shouldn't don't.

### Out of scope, deliberately

- **Lending.** `lending.Facility` keeps the `FacilityTerms` timeline the previous
  spec gives it and binds to nothing. The reasoning is in *Deposit first* below.
- **Retroactive publication.** Catalogue versions are forward-dated only. See
  *Forward-only publication* — this is the one place the design is deliberately
  less capable than the book.
- **The resolution log.** §24.1's resolution log records, per calculation, which
  version was used. Here the pair (`ProductID` on the account timeline,
  `EffectiveFrom` on the version timeline) makes the resolution re-derivable from
  stored rows, which is the property the log exists to provide. A log would make
  it *cheap* to query rather than possible; that is a checkpointing concern and
  belongs with the successor the previous spec already named.
- **Maker-checker on publication.** Publishing a version is the highest-blast-
  radius write in the system even when forward-dated. Dual control is comparison
  item 17 and is not touched here; forward-only publication is the mitigation this
  spec ships instead.
- **Fee schedules, tiered and bonus rates, capitalisation frequency.** All of them
  are product parameters in a real catalogue and none of them exist anywhere in
  this repo yet (`cbs-vs-book.md` §4.4). A catalogue that carries only the
  parameters the code actually reads is the honest version; widening it is a
  deposit-products topic, not a catalogue topic.
- **Multi-book catalogue sharing.** Products are book-scoped, like every other
  entity here. The same product ID in two books is two products.

### Sequencing

**Blocked on `spec/effective-dated-terms`.** This design writes into and resolves
against the `OverdraftTerms` timeline that branch creates. Building it against the
current mutable columns would be work thrown away.

**`BusinessDate` (architecture review candidate 5) applies with more force here.**
The previous spec counted four day-granular `time.Time` parameters and an
`EffectiveFrom` field whose whole contract is a comment
(`2026-07-30-effective-dated-terms-design.md:105`). This spec adds
`Version.EffectiveFrom`, `GetProductVersionAsOf`, and three `effectiveFrom`
parameters on the split register methods. It is a recommendation, not a
precondition: the design works either way and pays interest if it ships without.

## Decisions

### Deposit first, and `Kind` shipped anyway

Only `deposit.Account` binds to a product in this pass. `Kind` exists on `Product`
from the first commit, with one case implemented, so lending slots in later by
adding a case and one resolution site rather than by reopening the concept.

Three reasons, in order of weight. The risk in this change is the **resolution
path**, not the catalogue table: the accrual walk stops reading one row per day and
starts merging three sources, and it must still reproduce a past day exactly.
Proving that once, on the path that already tangles with the value-dated series and
the snapshots, is worth more than proving it twice. Second, revolving lines are the
same merge with a different pinned group — a commitment limit is also an
underwriting decision — so they add duplication risk before the first merge is
settled. Third, and decisively: a term loan **cannot float at all**. The previous
spec refuses repricing a disbursed term loan with `ErrScheduleWouldDiverge`, so a
term-loan product version is a pure origination default — a different binding
model. Two binding semantics under one type in one spec is exactly the ambiguity
that makes a spec rot.

### The floating group is a type that cannot hold a limit

```go
// package product

// OverdraftPricing is the FLOATING group: what the product charges.
type OverdraftPricing struct {
	Rate           interest.Rate
	UnarrangedRate interest.Rate
	DayCount       interest.DayCount
}
```

There is no limit field, and the absence is the design rather than an omission. A
limit is an underwriting decision about one customer's creditworthiness; a rate is
a price the bank publishes. Making the catalogue *unable to express* a limit means
"the limit cannot float" is a fact about the type, checked by the compiler, instead
of a rule in a comment that a later `PutProductVersion` caller has to remember.

The same three fields, as a pointer, are the per-account overlay. That is not a
coincidence to be tidied away — the overlay is by definition "this customer's price
instead of the product's", so it is the product's price type or it is a second
thing that must be kept in step with it.

`interest.DayCount` floating with the product is worth defending, since it looks
more like a code path than a price. It is neither: *"the convention is a product
parameter, never a code path chosen by product type"* (§7.4.1, quoted at
`cbs-vs-book.md:84`) is already this repo's stated position and
`interest/daycount.go:10` already says so in the code. A catalogue is where a
product parameter belongs.

### The account timeline gains no sibling

```go
// deposit.OverdraftTerms, after the catalogue
type OverdraftTerms struct {
	AccountID      AccountID
	EffectiveFrom  time.Time
	OverdraftLimit ledger.Amount             // PINNED: always instance-owned
	Pricing        *product.OverdraftPricing // nil == float from the product version
	CreatedAt      time.Time
}
```

The obvious shape for overlays is a second table — `account_pricing_overlays`,
its own `Put`/`List` pair, its own day key, its own ordering contract. It is the
wrong shape. A limit change and a negotiated rate are both *"this account's terms
changed on day D"*, they are ordered against each other, and only one of them can
be in force on a day. Two tables would make "the terms in force on D" a join whose
uniqueness is a validation rule again — which is the exact property `TermsDayKey`
was introduced to make structural. One row type keeps `termsAt`'s binary search,
the ascending-order contract, the upsert-by-day-key identity and the
`ErrTermsNotFound` semantics all untouched.

`nil` meaning *float* rather than *interest-free* is the one place this shape can
be misread, and it is why the mixed state is refused: all three pricing fields set
is an overlay, all three unset is floating, and anything between is
`ErrInvalidRate` from `Validate`. In `store/pg` the three columns become nullable
with no `CHECK`, so per `CLAUDE.md` the meaning goes in a `COMMENT ON COLUMN`: a
missing constraint is invisible in a schema dump, and so is the difference between
"NULL means free" and "NULL means ask the product".

### `ProductID` is not nullable, and every account has one

If `ProductID` were optional, a row with `Pricing: nil` on a productless account
would have nothing to float to, and `Resolve` would need an error case for a state
the model should not be able to reach. So `OpenAccount` takes a product, the seed
defines a catalogue, and the invariant is that **every deposit account was opened
from a product**. This is also true of every real bank, which is a good sign.

It is not free. `OpenAccount`'s signature changes, `api`'s create-account DTO
changes, `seed` must define products before it opens accounts, and
`payment.Registry` — which opens customer accounts at `payment/participant.go:128`
— must be configured with one. That last is the same idiom it already uses for
`CustomerSubledger`, not a new one.

### Header row separate from versions

```go
type Product struct {
	ID   ID
	Name string
	Kind Kind
	// Retired stops NEW accounts being opened from it; existing accounts keep
	// resolving against its versions forever.
	Retired   bool
	CreatedAt time.Time
}

// Version is a row, never updated once published.
type Version struct {
	ProductID     ID
	EffectiveFrom time.Time // ledger.DayStart-truncated, the same day axis as terms
	Overdraft     OverdraftPricing
	Hash          string    // over the identity and the pricing fields
	PublishedAt   time.Time // zero == draft: editable, and never resolvable
	CreatedAt     time.Time
}
```

A product needs a name before it has a price, so the header cannot be derived from
its versions. `Kind` is on the header because a product does not change kind, and
putting it on each version would permit a timeline that is a current account in
March and a term loan in April.

`Retired` is a product going off sale, which is not the same event as its accounts
losing their price — the accounts sold from a withdrawn product keep resolving
against its last version for decades. A bank that could not express that would have
to keep dead products on sale.

`VersionDayKey(day)` is `TermsDayKey`'s twin, for the same reason: keying a version
by `(ProductID, day)` makes "the version in force on D" unique by construction, so
the book's non-overlapping-interval exclusion constraint has nothing left to
enforce. The alternative — a `tstzrange` and a `GiST` exclusion constraint — is
better in a single-backend bank and unavailable here, because `store/mem` cannot
implement it and `store/storetest` would have nothing to pin.

### Draft, publish, and a hash that something reads

`PublishedAt` zero means draft: editable through the same keyed upsert, and
invisible to `GetProductVersionAsOf`. Publishing freezes the row, and a write to a
published key is refused with `ErrVersionPublished`.

Without drafts, "immutable" would mean a typo in a rate is permanent, which is the
kind of rule that gets worked around with a direct SQL `UPDATE` — and the
workaround is the thing being defended against.

The refusal lives in the **register, not in a `CHECK`**. That is this repo's
dual-store position (`cbs-vs-book.md:143`) and `storetest` pins both stores as
plain upserts, so a constraint in one of them would be a divergence the conformance
suite is built to prevent.

Which leaves the hole a domain-layer refusal cannot cover: a direct `UPDATE` to a
published row. `Hash` is computed at publish over the identity and the pricing
fields, and **verified at resolution** — a mismatch is `ErrHashMismatch` and the
accrual run fails rather than pricing a day from a row that was edited behind the
system's back. Storing a hash nothing reads would be theatre; the verification is
what makes it a control. The cost is a hash per version per accrual run, over
timelines with single-digit row counts.

### Forward-only publication

`PublishVersion` refuses an `EffectiveFrom` before today, with
`ErrRetroactivePublish`.

The previous spec's whole point was that a terms change may be backdated, and the
accrual posts the delta rather than rewriting history. That is right for an
account. It is not right for a catalogue, because the blast radius is different in
kind: one row, published effective three months ago, moves interest that has
already been charged on every account bound to the product, and the only control on
it is an audit event nobody reads until a customer complains. Maker-checker is the
real answer and it is out of scope (comparison item 17).

So retroactivity stays where its blast radius is one customer: the per-account
`Pricing` overlay, which may be backdated exactly as the previous spec allows, and
whose delta the existing recompute already posts. A bank correcting a
mispublished product rate does it as a set of per-account overlays — which is
laborious, and should be, because it is a set of individual decisions about money
already taken from named customers.

This is the one place this design is deliberately weaker than the book. Stating it
here so the README does not over-claim.

### Resolution is a pure function, and the hot path does not use it

```go
// deposit/terms.go, beside termsAt
type EffectiveTerms struct {
	Limit   ledger.Amount
	Pricing product.OverdraftPricing
}

// Resolve merges the timelines for one day. No I/O, ascending inputs, binary
// search on both — termsAt's contract, twice.
//
// versions is keyed by product because ChangeProduct means one account's life
// can span several products, and a day resolves against the product in force on
// THAT day. A single slice would silently price pre-migration days from the new
// product, which is the bug this whole design exists to prevent.
func Resolve(rows []OverdraftTerms, versions map[product.ID][]product.Version, day time.Time) (EffectiveTerms, error)
```

Order of precedence, for one day: the account's row supplies the limit, always;
the account's row supplies the pricing if its overlay is set; otherwise the
product version in force on that day does. `EffectiveTerms` is in-memory only,
never stored — the previous spec's *resolved, never cached* rule
(`2026-07-30-effective-dated-terms-design.md:122`) applied one level further out,
and for the same reason: a cached copy is a second place the truth can be wrong.

The accrual run loads each account's terms rows as it already does, plus the
version timeline **once per distinct product** into a per-run map. A book of ten
thousand accounts on three products does three extra reads for the whole run, and
the day loop gains one binary search and one hash check. `interest.perDay`'s note
still holds: the cost is arithmetic, not I/O.

The path that runs constantly does not pay even that. `balanceTx`,
`availableTx` and `CheckWithdrawal` need only the **limit**, and the limit is on
the account's own row, so `GetOverdraftTermsAsOf` still answers them with one
read and no catalogue lookup. This is a second dividend from pinning the limit,
and it is worth stating because the reverse design — a floating limit — would put
a product read on every withdrawal check in the system.

`Resolve` returns an error rather than a bool, unlike `termsAt`: the failure modes
are now several (no terms row for the day, no published version for the day, hash
mismatch) and collapsing them into `false` would lose the one thing an operator
needs to know.

### The register splits along the pinned/floating seam

`SetOverdraftTerms` conflates the two groups in one call, and after this change
that is no longer one operation:

```go
SetOverdraftLimit(ctx, id, limit, effectiveFrom)            // pinned group
SetOverdraftPricingOverlay(ctx, id, pricing, effectiveFrom) // nil clears, back to floating
ChangeProduct(ctx, id, productID, effectiveFrom)            // migrate between products
```

`ChangeProduct` is what the whole design buys. It writes a forward-dated row like
any other change, so an account that moved from Basic to Premium in April still
answers "what did this account's product say on 15 July 2027" — and answers it
with the *previous* product's version for days before April. A migration is not a
rewrite.

Because `ProductID` now varies over the account's life, it belongs on the terms
row rather than on `Account`: an account-level column would be a cached current
value contradicting the timeline the moment a future-dated `ChangeProduct` is
entered, which is the `Account.Rate` mistake with a new name.

The catalogue's own writes go through a `product.Catalogue` register —
`CreateProduct`, `DraftVersion`, `PublishVersion`, `RetireProduct` — each audited
through the existing `appendAuditTx` pattern with new `ledger.Event*` constants,
because a published price is precisely the kind of fact an auditor asks who
entered and when.

## Data model

`product`:

| Type | Identity | Notes |
| --- | --- | --- |
| `Product` | `(book, ID)` | name, `Kind`, `Retired`, `CreatedAt` |
| `Version` | `(book, ProductID, VersionDayKey(EffectiveFrom))` | pricing, `Hash`, `PublishedAt`, `CreatedAt` |

`deposit.OverdraftTerms` gains `ProductID` and `Pricing *product.OverdraftPricing`;
it keeps `OverdraftLimit`, `EffectiveFrom`, `CreatedAt` and its
`(book, AccountID, TermsDayKey)` identity. `deposit.Account` gains nothing — the
binding is on the timeline, not the row.

## Store interface

`product.Tx` embeds `ledger.Tx`; `deposit.Tx` embeds `product.Tx` rather than
`ledger.Tx` directly. One concrete transaction in `store/mem` and `store/pg` then
spans all three layers, so `OpenAccount` can validate a product and the accrual
walk can read versions inside the unit of work they already have — the same
argument `deposit.Tx` already makes for embedding `ledger.Tx` (`deposit/store.go:20-22`).

```go
type Tx interface {
	ledger.Tx

	PutProduct(ctx context.Context, book ledger.BookID, p Product) error
	GetProduct(ctx context.Context, book ledger.BookID, id ID) (Product, error)
	ListProducts(ctx context.Context, book ledger.BookID) ([]Product, error)

	PutProductVersion(ctx context.Context, book ledger.BookID, v Version) error
	// ListProductVersions returns the whole timeline, drafts included, ascending
	// by effective day: the accrual run wants it in one read and resolves in Go.
	ListProductVersions(ctx context.Context, book ledger.BookID, id ID) ([]Version, error)
	// GetProductVersionAsOf returns the greatest PUBLISHED version not after day.
	GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id ID, day time.Time) (Version, error)
}
```

Contract notes for implementers, pinned by `storetest.RunProduct`:

- `PutProduct` is an upsert keyed by ID. `PutProductVersion` is an upsert keyed by
  `(ProductID, VersionDayKey(v.EffectiveFrom))` — two rows for the same product and
  the same effective day are the same row, and the later one wins.
- Stores never refuse a write on `PublishedAt` grounds. Freezing a published
  version is the register's job; a store that refused would diverge from the other
  one, and `RunProduct` asserts that both accept the write.
- `GetProduct` returns `ErrProductNotFound`, `GetProductVersionAsOf` returns
  `ErrVersionNotFound` when no published version precedes the day. Neither is an
  aggregate: an unknown product has no version, which is an error rather than a
  zero row that would read as a real free product.
- `ListProducts` orders by `CreatedAt` then insertion sequence, the rule every
  listing here follows. `ListProductVersions` orders by effective day ascending —
  not a convenience, since `Resolve` binary-searches the slice.
- The store truncates nothing; callers pass an already-`DayStart`-ed instant.

### Schema

`0001_init.sql` — one migration, per `CLAUDE.md`, since no database is deployed:

- `products` and `product_versions`, the latter with a `day_key` column and a
  composite primary key, mirroring `snapshots` and the new `overdraft_terms`.
- `overdraft_terms` gains `product_id` (not null) and nullable `rate`,
  `unarranged_rate`, `day_count`.
- `COMMENT ON COLUMN` on the nullable triple: all three null means the pricing
  floats from the product version in force that day; all three set is a negotiated
  overlay; the mixed state is refused by `OverdraftTerms.Validate` and not by a
  constraint, for the dual-store reason the four `asset` columns already document.
- `COMMENT ON COLUMN` on `product_versions.published_at` and `.hash`: why a draft
  is invisible to resolution, and that the hash is verified on read rather than
  merely stored.

## Consumers

| File | Change |
| --- | --- |
| `deposit/register.go` | `OpenAccount` takes a `product.ID`; `SetOverdraftTerms` splits into `SetOverdraftLimit`, `SetOverdraftPricingOverlay`, `ChangeProduct` |
| `deposit/terms.go` | `Resolve`, `EffectiveTerms`; `termsAt` unchanged |
| `deposit/register.go` accrual | `overdraftAccrual` takes `EffectiveTerms`; the walk loads versions once per product |
| `product/*.go` (new) | types, `Catalogue` register, errors, `VersionDayKey` |
| `store/mem`, `store/pg` | six methods, one migration |
| `store/storetest` | `RunProduct`; `RunDeposit` gains the product-bound cases |
| `payment/participant.go:128` | `Registry` gains a configured `ProductID` beside `CustomerSubledger` |
| `seed/seed.go:199,400` | defines a catalogue; Bruno's negotiated rate becomes an overlay |
| `api/handlers_deposit.go:20` | the `overdraft-terms` route splits with the register; new `GET/POST /products`, `POST /products/{id}/versions`, `POST .../publish` |

The seed change is worth a note beyond mechanics: `seed/seed.go:400` currently
calls `SetOverdraftTerms` to give Bruno a rate, and under this design that call
becomes an overlay — which is what it always *meant* in the story. A second
published version on the Basic product, effective mid-story, is added so the
seeded data exercises a product-level reprice; the interest figures move and the
seed's assertions move with them.

### Documentation, across every layer

Per `CLAUDE.md`, a schema change is a documentation change:

- `README.md` — the overdraft sections gain the pinned/floating distinction, and
  *Persistence* gains the two tables.
- `web/src/components/hint-content.ts` — new keys before any `[[wiki-link]]` can
  reference them: product version, pinned vs floating, pricing overlay. A link to
  a missing key takes every route down in dev and `next build` stays green, so
  `npm run test` is the gate.
- `web/src/lib/quiz/chapters/08-account-lifecycle-and-overdraft.ts` and
  `18-interest-overdrafts-and-arrears.ts` — questions on binding, on why a limit
  cannot float, and on forward-only publication. This is a **rebalance, not an
  append**: `diversity.test.ts` holds each chapter to 18–22 questions, ≥8 distinct
  `concept` tags, no tag more than 3× and all three difficulty tiers.
- `docs/cbs-vs-book.md` item 4.3 — from open to partly closed: effective-dated
  record, per-group binding and overlays done; exclusion constraint, resolution log
  and maker-checker open by choice, each with its reason.

## Testing

- **`Resolve`, table-driven and pure.** An overlay that starts and later stops; a
  `ChangeProduct` mid-life resolving the old product for earlier days; a day before
  the account's first row; a draft version ignored in favour of the published one
  before it; no published version at all.
- **Accrual across a product-level reprice.** Figures computed from the timeline
  rather than copied from current seed output — the previous spec's rule, for the
  same reason.
- **The limit never floats.** A published version cannot change an account's
  available balance. This is the test that pins the central claim, and it is
  cheap: assert `GetBalance` is untouched across a publish.
- **A tampered version is refused.** Write a published row through the store with
  edited pricing, then run the accrual: `ErrHashMismatch`, and no posting. This is
  the property the design claims and the only test that proves it.
- **Register refusals.** Retroactive publish; writing to a published version;
  opening from a retired product, *and* that its existing accounts still resolve;
  `Kind` mismatch; the mixed nullable pricing triple.
- **Conformance.** `storetest.RunProduct` under both `go test ./...` and
  `TEST_DATABASE_URL=… go test ./...`; `store/pg` must not accept or refuse a write
  differently from `store/mem`.

## Failure modes

| Failure | Symptom | Guard |
| --- | --- | --- |
| Version edited in place in the database | Past days re-derive at a price nobody published | `Hash` verified at resolution; accrual fails |
| Product retired while accounts hold it | Accounts unpriced, accrual stops silently | `Retired` gates `OpenAccount` only, never resolution |
| Mixed nullable pricing triple | An account priced from two sources on one day | `OverdraftTerms.Validate`, plus a column comment saying why no `CHECK` |
| `nil` pricing read as interest-free | Whole book silently free | `Resolve` errors rather than defaulting; no zero-value fallback anywhere |
| Publication effective in the past | Interest already charged moves on every account at once | `ErrRetroactivePublish`; overlays are the retroactive tool |
| Cached `ProductID` on `Account` | Contradicts the timeline after a future-dated `ChangeProduct` | Binding lives on the terms row; `Account` gains no column |
