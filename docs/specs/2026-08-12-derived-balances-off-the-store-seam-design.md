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

1. **`ScanEntries` and `BookBalance`.** The narrowest complete slice: one new
   store method, one computation moved, one aggregate deleted. Re-run
   `BenchmarkBookBalance` — the Go side of it becomes the real implementation,
   so the benchmark stops comparing an implementation against a sketch.
2. **`ValueDateBalance` and `ValueDatedSeries`.** The day arithmetic leaves SQL.
   This is where the bulk of the prose contract is deleted.
3. **`SubsidiaryBalances`.** Needs a grouped fold and the zero-netting rule; the
   `HAVING` goes.
4. **`ActiveHoldTotal`.** `deposit.Tx` grows a hold scan; the expiry rule becomes
   the deposit layer's, which is whose it is.
5. **`GetOpenCycle` and the listing orders.** Smallest and last, because they are
   the ones a reviewer is most likely to argue are fine where they are.
6. **The shared suite.** Cases in `store/storetest` that pin a computation become
   domain tests in `ledger`, `deposit` and `payment`; what stays in `storetest`
   is what a store must answer — rows, order, refusals. This phase removes more
   lines than the other five together and must not be folded into them, because
   a test that moves in the same commit as the code it tests proves nothing.

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
