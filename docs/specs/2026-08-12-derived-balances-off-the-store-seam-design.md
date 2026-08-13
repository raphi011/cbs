# Design — moving the derived balances off the store seam

Based on `main` at `15ca75e` plus the six fixes that follow it, and on
[ADR-0007](../adr/0007-a-store-per-institution.md): there are three transaction
seams, one per institution, and one implementation under all three.

This is the roadmap's *Move the derived balances off the store seam*, which was
gated on a measurement. The measurement is in — `BenchmarkBookBalance`,
`store/sqlite/balance_bench_test.go` — and it does not defend the seam.

## The defect, stated once

**A computation on the seam is written twice: once in SQL, once in prose, with
nothing holding the two together.**

`ledger.Tx.ValueDatedSeries` carries eleven lines of contract about which entry
lands in which bucket and which lands in none. `store/sqlite` implements that
contract with `substr(e.value_date, 1, 10)` and a `NULL` that falls out of every
`GROUP BY`. Both are correct. Nothing in the build relates them, and
`CLAUDE.md` is explicit that nothing cross-checks the SQL — there is one
implementation, so anything needing proof against a second has to be proved
before it lands.

The cost is paid every time a rule is added. `ValueDatedSeries` cost 88 lines of
implementation and 103 lines of shared-suite test to say a thing the domain
could have said in fifteen. "Sign an entry by its account's normal direction" is
written five times across the store, once per aggregate, because SQL has no
place to put it once.

## What is actually derived, and what only looks it

Measured over the three seams, eleven methods carry a rule rather than a row.
They do not all belong on the same side of this, and separating them is the
first piece of work.

### Moves into the domain — a rule over rows

| method | the rule it carries |
| --- | --- |
| `ledger.Tx.BookBalance` | sign by normal direction; empty subsidiary means the whole account |
| `ledger.Tx.ValueDateBalance` | the same, bounded; a zero value date is in no bound |
| `ledger.Tx.ValueDatedSeries` | the same, bucketed by UTC day; days with no movement omitted |
| `ledger.Tx.SubsidiaryBalances` | the same, grouped; a subsidiary netting to zero is not a row |
| `ledger.Tx.ListTransactionsForPosition` | a leg matches, the whole transaction is returned |
| `deposit.Tx.ActiveHoldTotal` | active, and not expired strictly before now; `NULL` never expires |
| `payment.CsmTx.GetOpenCycle` | at most one open per scheme, earliest wins if that is ever false |
| the listing orders | which order a caller may rely on, stated once per listing |

Every one is a fold over rows the store can already stream. None of them needs
the database to be the thing that computes it, and each is stated in the domain's
prose already.

### Stays in the store — a constraint, not a computation

| mechanism | why it cannot move |
| --- | --- |
| `transactions_idempotency_key_idx` | a UNIQUE index refuses a second writer under concurrency; a domain check is a read-then-write and loses the race |
| `MarkReversed`'s conditional `UPDATE` | compare-and-set on a row, for the same reason — its own doc says a read-compare-write would race |
| `NextID`'s gap-free allocation | one counter per book, allocated inside the writing transaction |

These three are the reason this is not "delete all the SQL". A rule that must
hold across two concurrent transactions belongs where the transactions are, and
moving it into Go would be moving it somewhere it cannot be enforced. The
distinction to hold onto: **an aggregate is a computation, a uniqueness claim is
a constraint.** Only the first is expressed twice.

## The measurement

`BenchmarkBookBalance`, Apple M2, a file under WAL, both sides reading the same
rows through the same `(book_id, account_id)` index prefix:

| entries on the account | SQL `SUM` | the same sum in Go |
| ---------------------- | --------- | ------------------ |
| 100                    | 82 µs     | 85 µs              |
| 1 000                  | 424 µs    | 602 µs             |
| 10 000                 | 4.7 ms    | 6.7 ms             |
| 100 000                | 69 ms     | 85 ms              |

At a hundred entries the two are inside each other's noise, and a repeat run put
the Go scan ahead — at that size both are dominated by opening the unit of work
rather than by the sum. The aggregate pulls away past ten thousand entries on ONE
account, and it pulls away by about a fifth, because the row I/O is the cost
either way and only the addition moved.

The alternative this replaces was Postgres's index-backed `SUM`, and there is no
Postgres. What is being weighed is a fifth of a millisecond on accounts a hundred
times busier than any in the seed, against eleven rules written once.

### What phase 1 measured, and where the table above was wrong

**The Go column was a sketch, and the sketch selected two columns.** It read
`direction` and `amount`, which is all a balance needs. The seam yields an
`Entry`, so the real implementation selects five — `id`, `subsidiary_id`,
`amount`, `direction`, `value_date`. Re-measured with the fold as the shipped
code (`account_id` dropped from the projection, since the `WHERE` clause already
fixed it):

| entries on the account | SQL `SUM` | the fold over `ScanEntries` | the 2-column sketch |
| ---------------------- | --------- | --------------------------- | ------------------- |
| 100                    | 57 µs     | 132 µs                      | 72 µs               |
| 1 000                  | 421 µs    | 1.10 ms                     | 578 µs              |
| 10 000                 | 4.4 ms    | 11.3 ms                     | 6.0 ms              |
| 100 000                | 62 ms     | 135 ms                      | 80 ms               |

**It is a factor of 2.3, not a fifth, and it is flat across every size.** Split
three ways at ten thousand entries: the iterator itself costs nothing (13.4 ms
against 13.6 ms for the same loop written out), parsing the timestamp costs
1.5 ms, and the remaining ~5 ms is reading the three columns a balance does not
use. The cost of moving a balance into the domain is the cost of an `Entry`
being wider than a sum.

In absolute terms it is +75 µs on a hundred-entry account and +0.7 ms on a
thousand-entry one. The decision still rests on that being small against eleven
rules written once — but it rests on the absolute number, and no longer on the
ratio. `TrialBalanceTx` is where it compounds: one balance per account in the
chart, so a 200-account trial balance pays it 200 times.

## The shape

**The seam yields entries; the domain folds them.**

```go
// on ledger.Tx, replacing four aggregates
ScanEntries(ctx context.Context, book BookID, pos Position, f EntryFilter) iter.Seq2[Entry, error]
```

`EntryFilter` carries only what an index can serve — the position, and a
value-date range — because a predicate the index cannot answer is a full scan
whichever language evaluates it. Everything else is the fold's business.

Three properties this has to keep, and each is a way to get it wrong:

- **It streams.** A `[]Entry` at a hundred thousand rows trades a fifth of a
  millisecond for a materialised slice, which is a worse deal than the one being
  refused. The benchmark's Go side already scans row by row and is what the new
  implementation must not regress against.
- **The read transaction is held for the length of the fold.** Under WAL a
  reader blocks no writer, so this costs latency and not concurrency — but a
  fold that calls back into the store re-enters a transaction the store refuses
  to nest, and that is a build-time shape rather than a review note.
- **`ledger.DayStart` is the only day boundary.** The `substr(value_date, 1, 10)`
  in `ValueDatedSeries` is a second answer to "which day is this entry in"; the
  point of moving it is that it becomes the same answer `calendar` already
  imports `ledger` for.

## Phasing

Each phase leaves the tree green and deletes what it replaces. No phase is worth
starting without the one before it.

1. **`ScanEntries` and `BookBalance`.** — `done`. The narrowest complete slice:
   one new store method, one computation moved, one aggregate deleted.
   `ledger.BookBalance` is a package-level fold, so `payment/recon` and the
   trial balance reach it without a `Book`; `ledger.signed` is the sign rule,
   written once and used by the history fold too. `BenchmarkBookBalance` keeps
   the deleted SQL as a local helper and gained a `fold` arm — see the
   re-measurement above, which did not come out where this document said it
   would.
2. **`ValueDateBalance` and `ValueDatedSeries`.** — `done`. The day arithmetic
   left SQL: `substr(value_date, 1, 10)` and the `GROUP BY` are gone, and
   `ledger.DayStart` is the only day boundary there is. Both are package-level
   folds, and the series is still two scans — the opening and the window — which
   is what the SQL did, over the same rows. Two edges the SQL expressed by
   accident are now stated: a zero bound is before nothing, and a window that
   does not end after it starts holds no days.
3. **`SubsidiaryBalances`.** — `done`. A grouped fold over one scan of the
   pool; the `GROUP BY`, the `HAVING` and the `ORDER BY` are gone, and a
   subsidiary netting to zero is dropped in Go.
4. **`ActiveHoldTotal`.** — `done`, and without the hold scan this document
   asked for. `ListHoldsForAccount` already answers with the same rows, so the
   fold reads it and `deposit.Tx` LOSES a method rather than trading one for
   another. `deposit.Hold.ActiveAt` is the expiry rule, written once.
5. **`GetOpenCycle` and the listing orders.** — `not done`, and the reviewer
   this document predicted is the one writing this line.

   **`GetOpenCycle` stays in the store.** The fold would be a scan of
   `ListCycles`, which `LEFT JOIN`s `cycle_payments` — so every payment id of
   every cycle ever closed, read on a path that runs once per submission
   (`payment/system.go`, twice). That is not a fifth of a millisecond on a busy
   account; it is unbounded growth on the hot path, against an indexed
   single-row lookup. The rule it carries is one line and the cost of moving it
   is not comparable to the four that moved.

   **The listing orders are not written twice.** They were assumed to be prose
   here and `ORDER BY` there; measured, there are 46 `ORDER BY` clauses in the
   store and the domain's interfaces state an order in exactly one place
   (`deposit.Tx.ListSnapshotsForAccount`) plus the new `HoldLister`. So the
   defect is the opposite of the one filed: a caller cannot see the order at
   all, and the fix is to state it, not to move it — sorting in Go what an
   index already returns sorted is a cost with no rule behind it. Filed on the
   roadmap as its own item.
6. **The shared suite.** — `done`, and it removed 546 lines from `storetest`
   against ~390 added in `ledger` and `deposit`. Ten ledger subtests and the
   hold-total subtest were computations; they are now table tests over a fake
   scanner and a fake lister, with no database in them. What stayed is what a
   store must answer: which rows a scan yields for a position and a window,
   that a status and an expiry survive the round trip, ordering, refusals.
   `ledger.EntryScanner` and `deposit.HoldLister` are what made that possible —
   a fold that took a `Tx` would have needed a 22-method fake.

## What this does not do

**It does not touch the three constraints above.** A phase that removed the
unique index would be a different decision with a different argument, and the
argument is worse.

**It does not collapse `X` / `XTx`.** That is the roadmap's own item and it
crosses every layer; doing both at once would make a compiler-guided rename
indistinguishable from a behaviour change.

**It does not add a second store.** There is one, and the reason to prove a rule
before it lands is unchanged by where the rule lives — but a rule in Go is
reachable by a domain test with no database at all, which is the point.
