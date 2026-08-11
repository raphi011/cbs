# Value-Dated Balance and Retroactive Accrual — Design

The first topic taken from `cbs-vs-book.md`, the comparison of this repo against
*Building a Modern Core Banking Platform*. It closes the largest gap between what
the README claims and what the code does: the README describes a value-date
balance as one of three balances every account carries, and nothing anywhere
computes one.

## Goal

`ledger.Transaction` has carried a `ValueDate` since the beginning, and every
layer sets it thoughtfully — `payment` value-dates its legs to the scheme's
settlement delay, `deposit` and `lending` value-date accruals to the accrual day.
Every posting path sets it, and nothing reads it. The only balance the system can
compute is `BookBalance`, a sum over every entry regardless of value date, and
both interest engines consume it.

The book is blunt about that arrangement: *"Interest engines do not consume
booked balances; they consume value-dated balances… Value-dated computation is
not an optimisation; it is the correctness baseline."* (§4.2.2)

This design makes the value date load-bearing. Three things follow, and they are
one change rather than three because none of them stands up alone:

- **An entry carries its own value date**, because the two legs of one event can
  legitimately value-date differently, and in this repo one already should.
- **A value-dated balance exists**, both as a point query and as a per-day series.
- **Accrual recomputes rather than increments**, so a posting that arrives
  backdated corrects the interest that was already charged.

### Out of scope, deliberately

Recorded here so the README does not over-claim once this ships:

- **Back-value across a terms change.** The recompute window starts at the last
  repricing (see *Decisions*), so a posting backdated before that point is not
  retro-corrected. Widening the window to account inception is what effective-dated
  terms would buy, and that is the next topic on the list, not this one.
- **Snapshot invalidation.** `deposit.Snapshot` is still written and never read,
  and a backdated posting still does not invalidate the snapshots it falsifies.
  The book's §22.2.5 discipline — anchor a snapshot to a ledger sequence position,
  never a wall clock, and invalidate on backdated postings — remains open.
- **Business dates.** There is still no business-date roll, no banking calendar,
  no cut-off, and no TARGET holiday handling. `RunEndOfDay(ctx, date)` accrues for
  whatever date the caller passes.
- **Bounds on backdating.** `PostTransaction` accepts any value date, arbitrarily
  far in the past or future. The book forbids future *booking* dates (§4.1.3);
  that is a separate item and not touched here.

## Decisions

### The value date belongs on the entry

`ledger.Entry` gains `ValueDate time.Time`. A zero value means "the transaction's",
resolved once inside `PostTransactionTx` at the same point `BookingDate` is
defaulted, so every stored entry carries a concrete date and no reader ever has
to fall back to the transaction.

This is the one place where the repo's own "the asset lives on the account, not
the entry, because a second copy can disagree" reasoning cuts the other way. An
entry's asset genuinely is its account's, so storing it twice could only create
disagreement. An entry's value date is genuinely *not* its transaction's: §4.1.5
puts `value_date` on the posting for exactly this reason, and §4.2.1 gives the
case — an inbound credit whose customer leg must value-date to receipt while the
settlement leg carries the settlement date.

The repo has that case already, and it is not hypothetical. `payment.Initiate`
posts the debtor leg as `Dr customer GL / Cr clearing suspense`, both value-dated
`now + SettlementDelay`. Under PSD2 Art. 87(2) the payer's debit value date is
*no earlier than* the moment the amount is debited, so the customer leg should be
value-dated today. Today's book-balance accrual gets that right by accident,
because it ignores value dates entirely. Point accrual at a *transaction*-level
value-dated balance and the customer stops paying overdraft interest for the
settlement delay — a two-day interest-free ride that does not exist today.

So the entry-level value date is not a cheap-now-expensive-later schema bet here.
It is what stops this change from regressing.

### Retroactive accrual recomputes; it does not detect and repair

Two shapes were considered.

**Detect and repair** finds entries value-dated before the account's last accrual
but posted after it, reverses the affected accrual transactions, rewinds
`LastAccrualDate`, and re-runs forward. It needs three things this repo does not
have: a posted-at ordering on entries, a link from each accrual posting back to
the day it covered, and reversal handling. It also reverses postings that were
correct when they were made, which reads badly in an audit log — and the book's
own position on exception legs (§12.4.3) is that they are new linked events, not
reversals of the original.

**Recompute and true up** recomputes gross interest over the whole window on
every run and posts the difference against what has already been posted. A
backdated entry changes a historical day's balance, gross moves, and the next
run's delta trues it up. No detection, no reversal, no rewind.

The second one wins on a point that matters more than either: it *is* the idiom
the repo already uses. `delta := acct.Accrued.Minor() - before` is already "post
the change in the rounded value" — the mechanism that makes drift structurally
impossible. The only change is where the new value comes from. And it turns
`Accrued` from incrementally accumulated state into a recomputed aggregate, which
is the same argument as *A Balance Is an Aggregate, Not a Column*, applied one
layer up.

### The recompute window starts at the last repricing

A recompute needs to know the terms in force on each historical day.
`SetOverdraftTermsTx` overwrites `OverdraftLimit`, `Rate`, `UnarrangedRate` and
`DayCount` in place, and `overdraftAccrual` reads all four. So an unbounded
recompute would re-derive every past day at *today's* rate and post a true-up for
the difference — rewriting accrual history every time an account is repriced.
That is strictly worse than the current incremental code, which at least accrued
each day at the rate in force that day.

The window therefore starts at `TermsEffectiveFrom`, set whenever terms are set.
Prior accrual is frozen: never recomputed, never rewritten. Back-value corrects
within the current terms period, which is the overwhelmingly common case, and a
repricing draws a line the recompute does not cross.

This is a bound, not a solution, and it names its own successor. Effective-dated
terms (a small history record resolving the four parameters at a date) is what
widens the window to account inception, and it is the next topic.

### Days are UTC, closing balances, and the existing convention

The repo already decided this in `interest.truncateToDay`: *"The time of day is
discarded on both ends: a business date is a date, so an end-of-day run at 23:00
must cover the same day as one at 09:00."* Nothing here changes it.

Day *D*'s interest accrues on the **end-of-day** value-dated balance — entries
value-dated on or before *D* count. That matches current behaviour, where today's
accrual reads a balance that already includes entries value-dated today, and it
is the conventional banking rule.

The convention this pins down that the current code leaves implicit: accruing
from `L` to `D` covers days `L+1 … D`, exactly `DayCount.Days(L, D)` of them, and
each accrues on *its own* closing balance. Today's code applies a single balance
across the whole period, which is only correct when the period is one day. On
daily runs the two agree exactly, so `RunEndOfDay` behaviour is unchanged; the
recompute is strictly more correct whenever the period is longer.

## Data model

Three new fields and one new column. Everything here is additive; nothing
existing changes type or meaning, and `Accrued` in particular keeps exactly the
semantics it has today.

### `ledger.Entry.ValueDate`

```go
type Entry struct {
	ID        EntryID
	AccountID AccountID
	Amount    Amount
	Direction Direction
	ValueDate time.Time // zero means the transaction's
}
```

`entries` gains `value_date TIMESTAMPTZ NOT NULL`, and a comment saying why this
column duplicates nothing — in contrast to `asset`, which the schema deliberately
keeps only on `accounts`. Domain content, per `CLAUDE.md`.

`entries_account_idx` becomes `(book_id, account_id, value_date)`. Same prefix, so
every existing `BookBalance` lookup keeps its index and both new queries get one.

### Two fields on `deposit.Account` and `lending.Facility`

```go
TermsEffectiveFrom time.Time        // start of the recompute window
AccruedGross       interest.Accrued // gross interest over the window, at micro precision
```

`LastAccrualDate` stays, with its meaning shifted from "incremented to" to
"recomputed through". `Accrued` keeps its exact current meaning — unsettled
interest, decremented by capitalisation and repayment — so every existing
capitalisation and repayment path is untouched.

`SetOverdraftTermsTx` sets `TermsEffectiveFrom = r.now()` and resets
`AccruedGross = 0`. Lending does the same at origination and on any terms change,
via `p.now()`. A zero `TermsEffectiveFrom` means "not accruing yet", which
preserves the current zero-`LastAccrualDate` bootstrap semantics.

Note that `Charged` needs no field of its own. It is `AccruedGross.Minor()` minus
the receivable's ledger balance whenever anyone wants it, and no code does.

## Store interface

Two new `ledger.Tx` methods beside `BookBalance`, obeying its rules: reversed
transactions are included, because a reversal posts its own mirrored entries and
those are what cancel the original; and an account with no entries is `0`, not
`ErrAccountNotFound`, because every aggregate here behaves that way.

```go
// ValueDateBalance is BookBalance restricted to entries whose value date falls
// on or before asOf.
ValueDateBalance(ctx context.Context, book BookID, id AccountID, normal Direction, asOf time.Time) (Amount, error)

// ValueDatedSeries returns the balance carried into from, plus the net movement
// on each value date in [from, to] that has any.
ValueDatedSeries(ctx context.Context, book BookID, id AccountID, normal Direction, from, to time.Time) (Series, error)
```

```go
type DayMovement struct {
	Day    time.Time // UTC midnight
	Amount Amount    // signed by the account's normal direction
}

type Series struct {
	Opening   Amount        // balance value-dated strictly before From
	Movements []DayMovement // ascending; days with no movement omitted
}
```

Two primitives rather than one, because they have different consumers: the point
query answers the README's third balance and the API; the series feeds accrual.
Each is a few lines in each store.

### Day truncation is decided in Go, not in SQL

`ledger` converts `asOf`, `from` and `to` into half-open UTC-midnight bounds
before calling the store, and both stores compare raw timestamps against those
bounds. This is deliberate. If `store/pg` truncated with
`date_trunc('day', … AT TIME ZONE 'UTC')` while `store/mem` truncated in Go, the
two would be one DST-adjacent edge case away from disagreeing — precisely the
divergence `store/storetest` exists to prevent.

The series still needs a `GROUP BY` on a truncated day inside SQL, since it
buckets rather than filters. That one is pinned by conformance tests that feed
value dates carrying non-zero times.

## Accrual

### One new pure function in `interest`

```go
// Period is interest on a constant balance over [from, to) — the shape both
// layers already have.
type Period func(balance ledger.Amount, from, to time.Time) Accrued

// AccrueSeries splits [from, to] at each value date on which the balance moved
// and sums Period over the constant-balance runs between.
func AccrueSeries(s ledger.Series, from, to time.Time, p Period) Accrued
```

`lending` passes a closure over `interest.Accrue`. `deposit` passes one over
`overdraftAccrual`, so the tiered arranged/unarranged pricing is unchanged —
`overdraftAccrual` is refactored to take `from, to` explicitly instead of reading
`acct.LastAccrualDate` off the record.

Cost is O(movements), not O(days). Splitting a period into runs changes the
result slightly against a single call, because `Accrue` truncates its integer
division per call; the difference is at micro-minor-unit precision, and it is the
*more* correct answer, because the balance genuinely differed across the runs.

### The run

```
newGross := AccrueSeries(series over [TermsEffectiveFrom, D], terms)
before   := Accrued.Minor()
Accrued  += newGross - AccruedGross     // micro-precision change in gross
AccruedGross = newGross
delta    := Accrued.Minor() - before
post delta if non-zero
LastAccrualDate = D
```

A positive delta posts as it does today — `Dr accrued interest receivable /
Cr interest income`. A zero delta posts nothing and writes the audit event alone,
also as today: the rounding did not tick, and the ledger refuses a zero-amount
entry anyway.

The delta is taken from `Accrued`, not from gross, because the invariant the repo
maintains is that the receivable holds `Accrued.Minor()` — and the residue shifts
where rounding lands. Structurally this is today's code with the increment
replaced by the change in recomputed gross.

Capitalisation still compounds correctly and needs no special handling: it posts
`Dr customer GL / Cr receivable`, so it moves the customer's balance on its own
value date, and every recomputed day after it accrues on the larger drawn amount.
That is what makes an overdraft compound, and the recompute picks it up for free
because the capitalisation posting is inside the window.

### Negative deltas

A backdated credit means less interest was owed than was posted, so `delta` can
be negative. The ledger refuses non-positive amounts, so the layer posts `|delta|`
with the directions swapped:

```
Dr  Interest income
  Cr Accrued interest receivable
```

A distinct audit event — `EventOverdraftAccrualCorrected` /
`EventFacilityAccrualCorrected` — rather than reusing the accrual event, so a
correction is visible as one in the log.

### Clamp and refund

If interest has already been capitalised out of the receivable, a negative
true-up can exceed what the receivable still holds. Crediting it further would
drive an Asset account negative, and `checkSufficientBalance` refuses — inside
`RunEndOfDay`, which would fail the whole book's batch.

The correction clamps to the receivable and refunds the remainder, in one
balanced transaction:

```
Dr  Interest income                    |delta|
  Cr Accrued interest receivable         min(|delta|, receivable balance)
  Cr Customer deposit GL / PrincipalGL   remainder
```

Deposit refunds to the customer's GL account: the money was taken from them, so
it goes back. Lending credits `PrincipalGL`, reducing what the borrower owes.

Lending's refund therefore feeds the next day's accrual basis, since `drawn` is
`PrincipalGL`'s balance. That is correct — the borrower owes less, so less
interest accrues — but it means the refund leg must post *after* the true-up
within a run, and the feedback needs a two-consecutive-day test to pin it.

This path never errors. An over-corrected account must not stop a book's
end-of-day batch.

## Consumers

### `payment.Initiate` splits its legs

| Leg | Value date | Why |
|---|---|---|
| `debtorGL` (customer) debit | `now` | PSD2 Art. 87(2) — no earlier than the debit itself |
| `debtorAccts.Suspense` credit | `p.ValueDate` (T+n) | the bank's clearing position settles then |

`Transaction.ValueDate` stays `p.ValueDate`; the customer leg overrides it.

`SettleCycleTx`'s creditor leg needs no split: suspense-out and customer-in fall
on the same day there.

### API

`GET /participants/{pid}/accounts/{aid}/balance` gains `valueDateBalance`
alongside `bookBalance`, with an optional `?asOf=` defaulting to now. One field
and one query parameter, which is enough to make the balance triple observable —
which is what the README promises and cannot currently deliver.

### Documentation, across every layer

Per `CLAUDE.md`, a domain fact corrected in one layer is corrected in all of them.
This change corrects two.

**The value-date balance exists.** README *Balance Types* becomes true and gains
the day convention. A new subsection covers recompute-and-true-up and states its
bound plainly: back-value corrects within the current terms period, and widening
that to inception is what effective-dated terms would buy.

**The debtor leg's customer side value-dates to the debit, not to settlement.**
This is a correction, not an addition — `hint-content.ts:445-446` currently
teaches `Debtor leg value date = T + settlement delay`, and line 489 repeats it.
The affected keys are `settlement-delay`, `debtor-leg`, `payment-lifecycle`,
`booking-date` and `value-date`; the affected quiz chapters are 06
(booking-date-vs-value-date), 11 (payment-schemes) and 12 (sepa). Any question
whose correct answer depends on both legs sharing a value date is rewritten
rather than deleted, so chapter counts and `diversity.test.ts` stay satisfied.

Also swept while in there: README *Next Work* still lists account-status
enforcement on the debit path as future work, and `payment/scheme.go:85` has done
it since the lending merge.

`0001_init.sql` gets the `entries.value_date` comment described above.

## Testing

### The acceptance test is a diff

`seed/seed.go` drives multi-day scenarios through `RunEndOfDay` and its comments
quote concrete figures. Run the seed before and after and diff the postings.

The expectation is that **nothing moves**, and the reasoning is worth writing
down because it is what makes the diff a meaningful test rather than a vague one:

- Every non-payment posting in the seed value-dates to its booking date, so the
  value-dated balance and the book balance are the same number for every account
  interest accrues on.
- The SCT debtor leg is the only divergence, and the split cancels it: the
  customer leg moves from `T+n` to `now`, which is where the book balance already
  put it. Bruno is overdrawn on the same day either way.
- Bruno's terms are set before Verde's first `RunEndOfDay`, so
  `TermsEffectiveFrom` opens the window earlier than the old zero-`LastAccrualDate`
  bootstrap did. Those extra days accrue zero anyway, because the SCT that
  overdraws him has not posted yet and `overdraftAccrual` returns `0` on a
  non-overdrawn balance.

One difference is real but sub-minor-unit. Forty-five daily runs today perform
forty-five truncating integer divisions inside `Accrue`; the recompute performs
one per constant-balance run — about three, for Bruno. The recomputed figure is
therefore the more accurate one, by up to a few tens of micro-minor-units. That
is invisible at minor-unit precision unless it happens to tip a rounding
boundary. If a seed figure does move by exactly one minor unit, this is why, and
the comment at `seed/seed.go:435-453` (≈ EUR 3.77 on EUR 203.70 at 15% ACT/365)
gets recomputed rather than the design questioned.

Any other movement means the recompute is not the no-op on daily runs that it
claims to be.

The same comment explains the zero-`LastAccrualDate` bootstrap by name, so it is
rewritten to describe `TermsEffectiveFrom` regardless of whether its figure moves.

### Unit coverage

**`interest`** — `AccrueSeries` over a constant balance equals `Accrue` over the
same period; a mid-period movement splits into two runs; 30/360 weights across
Jan 31 and Feb 28/29; an empty series; movements on the first and last day;
negative balances, which is every deposit overdraft case.

**`ledger`** — an entry's value date defaults to the transaction's; explicit
per-leg value dates round-trip; `ValueDateBalance` at the boundary (an entry
value-dated exactly `asOf` counts, one at `asOf + 1ns` does not).

**`deposit` / `lending`** — daily runs produce postings identical to today's; a
backdated credit posted after accrual yields a negative true-up on the next run;
a backdated debit a positive one; a true-up exceeding the receivable clamps and
refunds; a terms change freezes prior accrual rather than rewriting it; lending's
refund feeds the following day's basis.

**`payment`** — the two legs carry different value dates, and the customer's
value-dated balance moves today while the suspense account's moves at T+n.

### Conformance

New `store/storetest` cases, run against both backends: entry value date
defaulting; the `asOf` boundary; reversed transactions counted; an unknown
account is `0`; one transaction whose two legs carry different value dates lands
them on different days; and for the series — the opening balance, omitted gaps,
ascending order, and bucketing of a value date that carries a time component.

## Failure modes

Each is defined here rather than left to be discovered.

| Condition | Behaviour |
|---|---|
| Negative true-up exceeds the receivable | Clamp to the receivable, refund the remainder. Never an error. |
| Lending refund changes the accrual basis | Refund posts after the true-up within a run; next run picks it up. |
| `Rate == 0` | Accrues nothing, posts nothing, as today. |
| `TermsEffectiveFrom` is zero | Not accruing yet — preserves today's bootstrap. |
| `to` before `from` | Existing `Days(...) <= 0` guard returns early. |
| Backdated posting before `TermsEffectiveFrom` | Not corrected. Documented, not silent. |
