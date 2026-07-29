# Effective-Dated Product Terms — Design

The second topic taken from `cbs-vs-book.md`. The previous one named this one as
its successor in as many words: *"This is a bound, not a solution, and it names
its own successor. Effective-dated terms (a small history record resolving the
four parameters at a date) is what widens the window to account inception, and it
is the next topic."*

It is also the one place where the repo's own governing principle is broken by
the repo's own code. Every other item on the comparison's list is a thing not
built; this is a thing built the wrong way round.

## Goal

`SetOverdraftTermsTx` overwrites `OverdraftLimit`, `Rate`, `UnarrangedRate` and
`DayCount` in place (`deposit/register.go:297-300`). `overdraftAccrual` reads all
four (`:1227-1237`). So an accrual posted six months ago cannot be reproduced
from stored state: the inputs it used are gone, overwritten by the current ones.
The audit event carries the whole `Account` payload, so the history is
*recoverable* by replaying the log — but it is not *resolvable*, and nothing does
it.

The book's §24.1.1 puts it as well as it can be put: every financial calculation
is a function of account state, event history and configuration; the first two are
immutable and replayable here; *"if the third input can be edited in place, that
entire investment is undermined, because 'what did this account's product say on
15 July 2027?' no longer has a stable answer."*

There is a second consequence, and it is the sharper one because it is a live
defect rather than an audit weakness. Because terms are mutable, the recompute
window has to start at the last repricing — a wider window would re-derive past
days at today's rate, which is strictly worse than the incremental code it
replaced. So **a backdated posting only trues up interest within the current
terms window.** Back-value landing before the last repricing is silently never
corrected. The retroactive-accrual machinery the previous topic built stops
working at a line the customer cannot see and did not agree to.

This design makes product terms an effective-dated timeline, resolved per accrual
day. Three things follow:

- **Terms become rows, not columns.** A `(account, effective_from)` record per
  repricing, never overwritten.
- **The recompute window opens at account inception**, because every day can now
  be re-derived at the terms that were actually in force on it.
- **A terms change carries its own effective date**, which may be backdated or
  future-dated, and is honoured by the next recompute exactly as a backdated
  posting already is.

The change is net-subtractive in the interesting places. A surprising amount of
the existing code exists only to protect a window that moves, and the window
stops moving.

### Out of scope, deliberately

Recorded here so the README does not over-claim once this ships:

- **Checkpointing.** The window now opens at inception, so a nightly run
  re-derives every day the account has ever had. That is `O(days)` per account
  per night, accepted deliberately at this scale, and it is what makes this
  spec's successor the snapshot-as-checkpoint work (comparison items 2 and 6).
  §22.2.5's two disciplines — anchor a checkpoint to a ledger sequence position
  rather than a wall clock, and invalidate on backdated postings — remain open.
- **Schedule inputs.** `Method`, `TermMonths` and `MinPayment` stay mutable
  columns on `lending.Facility`. They feed `BuildSchedule`, which runs once at
  activation and stores instalment rows; repricing them mid-life means
  regenerating a schedule that already has repayments posted against it, which
  needs versioned schedule rows and open-item allocation. That is its own topic.
  See *Repricing a term loan is refused* below for how this spec keeps the
  divergence unreachable rather than merely documented.
- **The product catalogue.** No `product`/`product_version` table, no content
  hash, no non-overlapping-interval exclusion constraint, no pinned-vs-floating
  parameter binding (§24.1.3), no overlays (§24.1.4), no resolution log. These
  are **per-instance terms**: one timeline per account and per facility, not a
  catalogue shared across them. The book's full machinery is far beyond this
  repo's scope and the effective-dated record is not.
- **Maker-checker on a retroactive repricing.** A backdated terms row moves
  interest that has already been charged. That is the design, and the delta is
  posted rather than the history rewritten — but the audit log is the only
  control on it. Dual control is comparison item 17 and is not touched here.
- **Deposit credit interest.** The timeline is asset-side only, because that is
  all the deposit layer accrues. A savings product is comparison item 14.

### Sequencing against the architecture review

`docs/architecture-review-2026-07-30.md` landed the same day as this spec and two
of its candidates touch this work. Neither blocks it; both change what it should
cost, so the interaction is recorded rather than discovered.

- **Candidate 2, phase A — done, and this spec depends on it.** `interest.Recompute`
  with its unexported `perDay` walk is what makes terms-per-day a change to a
  closure rather than a change to two accrual engines. Written against the
  pre-refactor tree this design would have been substantially larger. Its
  candidate table already names *"terms-window reset —
  `register.go:276-279,306-308` · `portfolio.go:332-338,412-415"* as duplicated
  machinery across the two layers; those are exactly the lines *What deletes
  itself* removes, so this spec finishes a duplication the review only catalogued.
- **Candidate 2, phase B — not done, and it collides on test expectations.** The
  open value-date defect (interest accruing on interest earned the same day) is
  fixed by value-dating capitalisation to the next day, which *changes interest
  figures* and therefore moves seed assertions, api tests and README/quiz claims.
  This spec also changes interest figures, for a different reason. Whichever lands
  second pays to re-derive the other's expected numbers. They should not be
  interleaved, and the acceptance tests here should use figures computed from the
  timeline rather than copied from current seed output.
- **Candidate 5, `BusinessDate` — worth doing first.** This spec adds four
  signatures taking a bare day-granular `time.Time` (`GetOverdraftTermsAsOf`,
  `termsAt`, the two `effectiveFrom` parameters) plus an `EffectiveFrom` field
  whose entire contract is *"day-granular, UTC-normalised, compared by value"* —
  which is the type the review proposes, restated informally for the fifth time.
  The review's own argument applies with extra force here: *"Cheapest now, on this
  branch; more expensive every week the value-date work continues without it."*
  Landing `BusinessDate` first would let `EffectiveFrom` be a type that cannot be
  constructed wrong, instead of a `time.Time` with a comment asking callers to
  truncate. This is a recommendation, not a precondition: the design works either
  way, and if it ships first it adds to a debt the review has already priced.

## Decisions

### Terms are rows, and the four fields leave the account

`deposit.Account` loses `OverdraftLimit`, `Rate`, `UnarrangedRate`, `DayCount`
and `TermsEffectiveFrom`. `lending.Facility` loses `Rate`, `DayCount` and
`TermsEffectiveFrom`. Current terms are **resolved**, never cached on the row.

Keeping a cached copy of the current terms on the account is the obvious
shortcut and it is the one the repo already argues against, in the schema, about
a different field: *"An entry's asset is always its account's, so a column on
`entries` would store the same fact a second time and create the one thing a
second copy always creates: the possibility that the two disagree."*
(`README.md:1145`). A cached `Account.Rate` is that column.

The storage shape follows the existing idiom rather than inventing one.
Instalments, holds and snapshots are all separate rows reached through their own
`Put`/`List` pair; terms are the fourth of the same kind. The alternative —
a slice on the row, `jsonb` in `store/pg` — is cheaper to build and puts an
opaque blob where the schema is meant to teach a relational mapping, which
`CLAUDE.md` makes a documentation change rather than an implementation detail.

### `EffectiveFrom` is a day, and `CreatedAt` sits beside it

Accrual iterates whole UTC days. `interest.perDay` is explicit about it —
*"Interest here has always been accrued a business day at a time, and every
receivable in every book was built up that way"* (`interest/recompute.go:86-91`)
— and the day boundary itself comes from `ledger.DayStart`. Terms changing
part-way through a day would therefore have no well-defined meaning: the day is
the unit the arithmetic is expressed in. `EffectiveFrom` is
`ledger.DayStart`-truncated by the caller before it reaches a store, on the same
day axis accrual already uses.

Both dates are stored, and the pair is worth naming for what it is: **this is
the booking-date/value-date distinction applied to configuration.** When a
repricing was entered and when it takes economic effect are different questions,
for exactly the reasons the README already gives for money. The book reaches for
a "resolution log" to recover the same property; this repo has the shape already
and should say so, because it is the cheapest piece of teaching in the change.

### The window opens at inception, and the cost is accepted

With terms resolvable per day, the recompute can start at the beginning of the
account's life and re-derive each day at the terms in force on it. That is what
makes back-value correct across a repricing, which is the capability this spec
exists to add.

It also means every nightly run re-derives every day. A ten-year-old account is
roughly 3,650 day-iterations a night. `interest.perDay`'s own note applies —
*"The cost is arithmetic, not I/O… this loop adds no round trips"*
(`interest/recompute.go:99-100`) — and the series is still one query over the
window. At the scale this repo runs at that is nothing, and the honest framing is
the one the previous spec used about its own bound: this is a deliberate cost
with a named successor, not an oversight. Checkpointing is what removes it, and
the snapshots the repo already writes and never reads are the raw material.

Note what this does *not* do to the README's *Next Work* bullet about the
unbounded window. That bullet is **reframed, not closed.** Today the window's
bound is an accident — it happens to be the last repricing, because that is
where mutation forced it. After this change the bound is a deliberate choice
with a stated cost. The growth is still there.

### A terms change may be backdated or future-dated

`SetOverdraftTerms` takes an explicit `effectiveFrom`. Neither direction is
restricted.

Backdating costs almost nothing once the window opens at inception, because the
recompute already re-derives every day: a terms row inserted behind the current
date is picked up by the next run the same way a backdated posting is, and the
difference is posted as ordinary delta interest. Nothing is rewritten and both
terms rows stay in the audit log. This is the case a bank actually has — a
repricing agreed on the 1st and entered on the 15th — and refusing it would
leave the agreed date with no representation.

Future-dating falls out for free and gives scheduled repricing: a row effective
next month is inert until the runs reach it.

The risk is real and belongs in *Failure modes*: a retroactive repricing moves
interest that has already been charged to a customer, with no dual control on
the change.

### Every account and facility has a timeline from birth

`OpenAccountTx` writes an opening terms row effective at opening, carrying the
limit it was passed and zero rates. `lending.openTx` — the shared body of both
openers (`lending/portfolio.go:197`) — does the same with its rate.

This is cleaner than treating "no rows" as a state the resolver has to model. It
makes the window start uniform across both layers, it means `termsAt` answers for
every day the account has existed, and it costs nothing to specify because
`interest.DayCount`'s zero value is already `ACT365` — the opening row needs no
invented default, and an account opened before any pricing existed keeps exactly
the behaviour it has today (a zero rate accrues nothing, and a zero rate makes
the whole overdraft interest-free).

The one consequence is that `GetOverdraftTermsAsOf` can return a sentinel
honestly: the only way to miss is to ask about a day before the account existed.

### Repricing a term loan is refused

Adding a write path for facility terms creates a problem that cannot exist
today, because nothing reprices a facility.

A term loan's instalment schedule is generated once at activation, from the rate
in force then, and stored as rows. If accrual follows a timeline and the schedule
does not, a repricing makes the plan and the actual accrual diverge — beyond the
ordinary plan-versus-actual divergence the repo already teaches, which 30/360
exists to keep small — and the final instalment silently absorbs the difference.
Nobody would notice until maturity.

So `SetFacilityTerms` **refuses a term loan that has a generated schedule**, with
`ErrScheduleWouldDiverge`, and allows repricing freely on a revolving line (which
has no schedule) and on an undisbursed term loan (which has none yet). Refusing
is better than documenting a divergence nobody would see, and the error is where
the reason gets stated: schedule regeneration against posted repayments is a
separate topic.

This also sharpens an existing teaching point from the other side.
`BuildSchedule` reads `f.Rate` (`lending/schedule.go:53,57`) and gains an
explicit rate argument, which makes visible what was implicit: **the schedule is
a plan pinned to the terms in force at activation.** The out-of-scope note above
and this decision are the same sentence approached from opposite ends.

### `AccruedGross` becomes whole-life; the accrual guard keeps its contract

`AccruedGross` stops being a per-window figure that resets at a repricing and
becomes the total interest the account has ever produced — recomputed from
scratch on every run, never reset, and never decremented, because capitalisation
moves `Accrued` and not gross. It reaches `interest.Recompute` as
`interest.State{Accrued: …, Gross: …}` and comes back as `next.Gross`; the caller
does no arithmetic on it and does not start doing any.

Overflow is not worth engineering around: `interest.Accrued` is int64
micro-minor-units, a €10,000 overdraft at 10% produces on the order of 1e11 a
year, and int64 holds 9.2e18.

`LastAccrualDate` and its guard stay, and it is worth being exact about why,
because "the recompute is idempotent now" is a tempting and wrong reason to drop
it. `interest.Recompute` documents a precondition: *"[from, to] must cover at
least one day. An empty or backwards window accrues a gross of zero, and since
the returned Accrued is prior.Accrued + gross - prior.Gross, a caller that
recomputes an empty window over a non-zero prior state is told to give the whole
record back… Every caller therefore guards first"*
(`interest/recompute.go:55-67`). The guard is part of that contract, not an
optimisation, and `TestRecomputeOnAnUnadvancedWindowGivesTheRecordBack` pins the
consequence of removing it. What *does* change is that the guard no longer also
carries the job of preventing a second charge for a day already accrued: with a
whole-life recompute the same date produces the same gross and therefore a zero
delta. Same invariant, one fewer reason, and the comment on the field should stop
claiming the one it has lost.

### What deletes itself

Worth stating as a decision because it is the shape of the diff, and because
each deletion is a piece of defensive machinery whose reason for existing goes
away rather than code being dropped on a hunch.

| Location | Goes | Because |
|---|---|---|
| `deposit/register.go:259-308` | pre-accrual of the outgoing span at the outgoing terms; the `now.Before(acct.LastAccrualDate)` clamp (`:276-278`); the account re-read (`:285`); `TermsEffectiveFrom`/`AccruedGross`/`LastAccrualDate` reassignment (`:306-308`) | the window no longer moves, so there is no boundary for a day to fall between |
| `lending/portfolio.go:318-337`, `:414` | the same clamp, and the window reopen on re-disbursement and on a draw | same |
| `deposit/register.go:1073`, `lending/accrual.go:82` | the `TermsEffectiveFrom.IsZero()` guards | replaced by "no terms row in force on this day" |

The second row closes a README *Next Work* bullet as a side effect, and the
mechanism is worth recording because it is a case of a general fix subsuming a
special one. The dropped span is: last accrual at `L`, repayment to zero at `R`,
re-disbursement at `D`. Principal *was* drawn across `L→R`, so real interest
accrued there, and clamping `LastAccrualDate` forward to `D` skips it — interest
never charged at all. A whole-life recompute over the value-dated drawn series
includes `L→R` naturally and charges it, while `R→D` accrues nothing because
drawn is zero across it. No special case, and no clamp.

`InterestGL` stays on the account rather than moving to the terms row: there is
one receivable per account, it is created the first time a non-zero rate is set,
and it must survive a rate returning to zero because the account may already
hold accrued interest that discarding the receivable would strand.

## Data model

```go
// deposit
type OverdraftTerms struct {
	AccountID      AccountID
	EffectiveFrom  time.Time // day-truncated; the day these terms first apply
	OverdraftLimit ledger.Amount
	Rate           interest.Rate
	UnarrangedRate interest.Rate
	DayCount       interest.DayCount
	CreatedAt      time.Time // when the row was entered, not when it takes effect
}

// lending
type FacilityTerms struct {
	FacilityID    FacilityID
	EffectiveFrom time.Time
	Rate          interest.Rate
	DayCount      interest.DayCount
	CreatedAt     time.Time
}
```

Key `(book_id, account_id, effective_from)`, composite per the repo's idiom.
`Put` is an upsert, which makes "the terms in force on day *D*" unique by
construction rather than by a validation rule: two entries for the same account
on the same day are the same row, and the later one wins.

Validation stays where it is today and moves to the row: a negative limit is
`ErrInvalidAmount`, a negative rate is `ErrInvalidRate`, and an unarranged rate
with no arranged one is `ErrInvalidRate` — the combination that would price only
the excess, making the money drawn beyond the limit dearer than nothing while
leaving the facility inside it free.

## Store interface

Three methods per layer, not two:

```go
// deposit.Tx
PutOverdraftTerms(ctx context.Context, book ledger.BookID, t OverdraftTerms) error
ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]OverdraftTerms, error)
GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id AccountID, day time.Time) (OverdraftTerms, error)
```

`lending.Tx` gets the mirror trio.

The third exists because the two callers want different things. Accrual wants
the whole timeline in one read and resolves per day in Go. `balanceTx` wants
exactly one row — the limit in force now — and should not pay for history on a
path that runs on every withdrawal check. A bounded lookup as a store method is
already this repo's idiom: `ActiveHoldTotal` and `ValueDateBalance` are both
aggregates with bounds, for the same reason.

`List` returns ascending by `EffectiveFrom`. `GetOverdraftTermsAsOf` returns the
row with the greatest `EffectiveFrom <= day`, or `ErrTermsNotFound` if the day
precedes the account's opening row.

**Day truncation stays in Go.** Callers pass an already-`DayStart`ed instant and
neither store decides what a day is — the rule the previous spec set down, for
the reason it gave: two stores that each decide what a day is are one
DST-adjacent edge case away from disagreeing.

### Schema

Still one migration, per `CLAUDE.md`. Two new tables; four columns dropped from
`deposit_accounts`, two from `facilities`.

The comments are domain content, not implementation notes, and carry: why the
primary key is composite; why `effective_from` is a day-truncated timestamp
rather than a date type, and that Go did the truncating; why `created_at` is
stored beside it and what the pair means; and why there is **no `CHECK`** on the
rate or day-count columns. That last one is the `COMMENT ON COLUMN` pattern the
repo already established for the four asset columns, applied to a new case: a
`CHECK` enumerating valid day counts would make `store/pg` refuse a write
`store/mem` performs, and would turn a one-line change to a Go constant into a
migration.

## Accrual

Both layers already call `interest.Recompute`, which takes a `Period` — the
product's interest over one span — and applies it **a day at a time** across each
run of constant balance, via `perDay` (`interest/recompute.go:101-110`). That
per-day walk is not a detail a caller may opt out of; `Recompute`'s own
documentation calls it *"the single most load-bearing convention in the accrual
engine"* (`:50-53`).

So the terms-resolution point is the `Period` closure, and because `perDay`
calls it with one day at a time, its `from` **is** the day:

```go
// deposit/register.go:1084-1090, today
next, delta := interest.Recompute(series, acct.TermsEffectiveFrom, date,
	interest.State{Accrued: acct.Accrued, Gross: acct.AccruedGross},
	func(balance ledger.Amount, from, to time.Time) interest.Accrued {
		return overdraftAccrual(balance, acct, from, to)
	})

// after
next, delta := interest.Recompute(series, window, date,
	interest.State{Accrued: acct.Accrued, Gross: acct.AccruedGross},
	func(balance ledger.Amount, from, to time.Time) interest.Accrued {
		return overdraftAccrual(balance, termsAt(rows, from), from, to)
	})
```

`interest` is therefore **unchanged**. The package already owns the per-day
decomposition and already passes each day's endpoints to the `Period`; all this
change does is make the `Period` a function of the day as well as the balance,
which is what a `Period` closure is for.

The two layers differ slightly in shape and both need the same treatment.
Deposit's `Period` delegates to `overdraftAccrual(balance, acct, from, to)`
(`:1221`), which reads the limit, both rates and the day count — so it takes an
`OverdraftTerms` instead of an `Account`. Lending's `Period` calls
`interest.Accrue(drawn, f.Rate, f.DayCount, from, to)` inline
(`lending/accrual.go:96-98`), so it reads a `FacilityTerms` directly.

`termsAt(rows []OverdraftTerms, day time.Time)` returns the last row with
`EffectiveFrom <= day`, by `sort.Search`. Binary search rather than a cursor
advanced alongside the walk: the runs and the days within them are both ascending
today, so a cursor would work and would silently break the first time anything
called the closure out of order. Rows are loaded **once per accrual run**, so the
cost is one store read and `O(days log rows)` of arithmetic on top of a walk that
is already `O(days)`.

The window opens at the earliest terms row's `EffectiveFrom`, which is the
opening row, which is inception.

For lending the window opens at origination, and days before the first advance
accrue zero because drawn is zero across them. That is why the "first advance
opens the window" state can go: it was an optimisation expressed as a guard, and
the series already gives the right answer without it.

### The three guards, individually

The guard trio at the top of each accrual (`deposit/register.go:1069-1078`,
`lending/accrual.go:79-88`) does not survive as a trio, and lumping the three
together is how this change would acquire a bug. Each needs its own answer.

- **`Status == Closed`** is unchanged.
- **`TermsEffectiveFrom.IsZero()`** (`:1073`, `:82`) is deleted. It meant "no
  window", and there is always a window now: the opening row.
- **`Rate <= 0`** (`:1069`, `:79`) is deleted as an early return and does *not*
  simply move into the closure, because an early return skips the whole run. A
  zero rate is now a property of a **day**, not of an account, and an account
  unpriced for its first year and priced thereafter is a case the current model
  cannot express at all. Two things replace it: the closure returns zero for a
  day whose resolved rate is zero, and the run is skipped entirely when **no row
  in the timeline carries a non-zero rate** — a scan over rows already in memory,
  which keeps a never-priced account from reading a series every night.

The fourth guard is the subtle one, because it now depends on the thing it is
guarding. `DayCount.Days(LastAccrualDate, date) <= 0` enforces
`interest.Recompute`'s documented precondition, and after this change there is no
single `DayCount` to ask: it is a terms field. **The guard resolves the terms in
force on `date` and uses that day count.**

That choice needs stating because the conventions genuinely disagree about
whether a window advanced. Under `Thirty360` the 31st collapses onto the 30th, so
`Days(30th, 31st)` is zero while `ACT365` says one — meaning a run on the 31st is
a no-op under one convention and a real day under the other. Resolving on `date`
is the right answer: it is the convention the customer's product is on for the day
being accrued, and it is the same figure the walk itself will use for that day.

### `Thirty360` and terms boundaries

An earlier draft of this design worried that a terms boundary introduces a new
split point in the window, and that 30/360's non-additivity across a run
boundary (`interest/series.go:47-56`) would therefore make a repricing gain or
lose a day's interest. **It does not, and the reason is worth recording.**

`perDay` has already cut the window to single days before any `Period` runs. One
day is the finest cut there is, so a terms boundary adds no split that was not
already there, and the total is already the one that was actually charged rather
than a whole-window figure. `Recompute` makes this argument itself: *"the 31st
collapses onto the 30th and accrues nothing. What 'the interest over this window'
comes to there depends on how the window is cut, so the only answer worth
reproducing is the one that was actually charged — day by day"*
(`interest/recompute.go:93-96`).

What remains is smaller and still worth a test: under `Thirty360` the 31st
accrues nothing, so a terms row effective on the 31st has no effect until the 1st.
That is correct — it follows from the convention, not from this change — but it is
surprising enough that someone will read it as a bug.

## Consumers

| Where | Change |
|---|---|
| `deposit/register.go:152` | `OpenAccountTx` writes the opening terms row |
| `deposit/register.go:240-313` | `SetOverdraftTermsTx` gains `effectiveFrom`, appends instead of overwriting, loses the window dance at `:259-308` |
| `deposit/register.go:912` | `balanceTx` resolves the current limit via `GetOverdraftTermsAsOf`; `availableTx` (`:918`) delegates to it and needs no change of its own |
| `deposit/register.go:1068-1090` | accrual loads the timeline; guards move as described above |
| `deposit/register.go:1221` | `overdraftAccrual` takes `OverdraftTerms` rather than `Account` |
| `lending/portfolio.go:197` | `openTx` writes the opening row for both openers |
| `lending/portfolio.go:318-337,414` | clamps and window reopens deleted |
| `lending/portfolio.go` (new) | `SetFacilityTerms`, refusing a disbursed term loan with `ErrScheduleWouldDiverge` |
| `lending/accrual.go:78-99` | as deposit |
| `lending/schedule.go:53,57` | `BuildSchedule` takes an explicit rate |
| `api/dto_deposit.go:25,46,49,122` | DTO fields become resolved-as-of-now |
| `api/handlers_deposit.go:47,175` | request gains `effectiveFrom` |
| `api` (new) | `GET /accounts/{id}/overdraft-terms` |
| `store/pg/tx_deposit.go:60,91`, `tx_lending.go:68,111` | column removal, new tables |
| `store/pg/schema/0001_init.sql` | two tables, comments as above |
| `seed/seed.go:400` | effective date on the call, plus a second row demonstrating a repricing |
| `seed/seed.go:436` | the comment about the window opening becomes false and goes |

`TotalsTx` is deliberately absent from that list: it buckets accounts by the
sign of their balance and never reads the limit (`deposit/register.go:1379-1401`).

Two additions are part of this change rather than after it. The point of the
work is that the timeline is inspectable, and the cheapest proof is to show it —
hence the endpoint and a small web view. And seeding a second terms row means
the dev app demonstrates back-value across a repricing on load, which also gives
the acceptance test a fixture that exists in the running system.

### Documentation, across every layer

Per `CLAUDE.md`, a domain change is a documentation change in four places:

- **`README.md`** — *Overdraft* (the limit and rate are effective-dated, and the
  available-balance formula resolves one); *Interest Accrual* (the window opens
  at inception; terms resolve per day); *Next Work* (delete the effective-dated
  terms bullet and the re-disbursement bullet; reframe the unbounded-window
  bullet as a deliberate cost and name checkpointing as its successor).
- **`web/src/components/hint-content.ts`** — keys `overdraft` and
  `overdraft-interest`. Note that `:200` already states the recompute-and-true-up
  behaviour without qualification; this change makes that sentence true rather
  than window-bounded, so it needs checking, not rewriting.
- **Quiz** — chapters 08 (*account lifecycle and overdraft*) and 18 (*interest,
  overdrafts and arrears*) carry the claims that move; 07 (*balances and holds*)
  and 17 (*lending and amortization*) need checking for the available-balance
  formula and the schedule-pinning point.
- **`store/pg/schema/0001_init.sql`** — as above.

Both mechanical rules apply: a `[[wiki-link]]` to a missing key takes every dev
route down, and `npm run test` catches it in hint bodies *and* quiz
explanations, which the runtime guard does not scan.

## Testing

The acceptance tests are the capabilities that fail on today's code. The first
two are the reason this spec exists.

1. **Back-value across a repricing trues up.** Priced at `R1` from day 0,
   repriced to `R2` effective day 30. On day 45, post a transaction value-dated
   day 10. The next run re-derives days 10–30 **at `R1`**, with the new balance
   in place, and posts the delta. Today nothing happens, because the window
   starts at day 30.
2. **Re-disbursement charges the `L→R` span.** Disburse, accrue to `L`, repay in
   full at `R`, re-disburse at `D`, accrue. Interest for `L→R` is charged, and
   `R→D` contributes nothing. Fails today.
3. **Whole-life accrual equals the sum of its periods.** Days 0–30 at `R1` plus
   30–60 at `R2`, computed in one run, equals the two computed separately. Pins
   per-day resolution rather than one rate applied across the window.
4. **A backdated terms row posts a delta and rewrites nothing.** Entered day 45,
   effective day 30: the next run charges 30–45 at the new rate as a delta, and
   both terms rows are in the audit log.
5. **A future-dated terms row is inert** until its date, then applies.
6. **An account unpriced then priced.** Zero rate for the first year, non-zero
   after: the first year accrues nothing and the second accrues at the new rate.
   The current model cannot express this at all, since `Rate` is per-account.
7. **Idempotence.** Re-running end-of-day for the same date posts zero.
8. **The guard still holds.** An unadvanced window is refused before a series is
   read, per `interest.Recompute`'s precondition — the existing
   `TestRecomputeOnAnUnadvancedWindowGivesTheRecordBack` behaviour must not
   become reachable from either layer.
9. **The advancement guard resolves its day count on `date`.** A facility on
    `Thirty360` run for the 31st is a no-op, because `Days(30th, 31st)` is zero
    under that convention; the same facility on `ACT365` accrues a day. Pins the
    resolution choice, which is otherwise invisible.
10. **A never-priced account reads no series.** Every row in the timeline carries
    a zero rate, so the run is skipped before a store read — asserted on the
    store, not on the balance.
11. **`SetFacilityTerms` refuses a disbursed term loan** and allows a revolving
    line and an undisbursed term loan.
12. **`termsAt` units:** before the first row, exactly on a boundary, between
    rows, after the last row, a single row, and a backdated insert landing out of
    order.
13. **`Thirty360`, terms effective on a 31st.** The row takes effect on the 1st,
    because the 31st accrues nothing under the convention. Pins the documented
    collapse rather than asserting it away.

### Conformance

`store/storetest` covers both new entities: round trip; `List` ordering ascending
by `EffectiveFrom`; upsert on a repeated `(account, effective_from)`;
`GetOverdraftTermsAsOf` at four positions — before the first row returning
`ErrTermsNotFound`, exactly on a boundary returning that row, between rows
returning the earlier one, after the last returning the last; and `Reset`
clearing both tables.

Both runs stay green: `go test ./...` with no database, and
`TEST_DATABASE_URL=… go test ./...` against `store/pg`. A change that passes only
one of them has, by definition, made the two stores diverge.

`npm run test` for the wiki-link guard, and a page actually loaded.

## Failure modes

- **A retroactive repricing moves interest already charged to a customer.** By
  design, and as a delta rather than a rewrite — but the audit log is the only
  control on it. A rate entered wrong and corrected backwards changes what the
  customer owes, and nothing requires a second pair of eyes. Comparison item 17.
- **Nightly cost grows for the life of the account.** `O(days since opening)`
  per account per run, accepted, with checkpointing as the named successor. The
  first thing that would hurt is a nightly run across a large book.
- **A backdated repricing can drive a large negative delta.** The refund path
  (`correctOverdraftAccrualTx`, `deposit/register.go:1157`) already handles a
  negative delta by settling part of the record in cash, and a retroactive rate
  *cut* across years of history is a bigger negative than anything that path has
  seen. Its bounds hold — the previous topic's *Clamp and refund* decision — but
  this change widens the input range and the tests should say so.
- **`Thirty360` terms effective on a 31st appear to be ignored** for a day. It
  follows from the convention, not from this change, and it is pinned by test.
- **A term loan repriced before disbursement changes its schedule silently.**
  Allowed, because there is no schedule yet — but the schedule generated later
  uses the newer rate, which is right and may still surprise someone reading only
  the origination record. The audit log carries both rows.
- **Plan and actual still diverge on a revolving line after a repricing.** There
  is no schedule to disagree with, so nothing is wrong, but the minimum payment
  is computed from `MinPayment` and the interest charged follows the timeline;
  the two move independently and always did.
