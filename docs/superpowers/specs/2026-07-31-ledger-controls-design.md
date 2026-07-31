# Ledger Controls — Design

The fourth topic taken from `cbs-vs-book.md`, and the first that is not one
feature. Three of the comparison's open items share a theme — a control the book
asks for, at the strength this repo can honestly hold to:

- **Item 6, the trial balance** (`cbs-vs-book.md:260`): a check that, in a system
  where every posting is forced to balance, *cannot fail arithmetically* — which
  is why §5.2.1 says it is worth computing.
- **Item 9, future booking dates** (`:263`): §4.1.3 forbids them outright, and
  `PostTransactionTx` accepts any instant a caller supplies, including tomorrow's.
- **Item 5, the no-`CHECK` position** (`:257`): the last docs-only item on the
  list. The README argues the repo's position well and never says that a
  single-backend production bank would be advised the opposite.

They belong on one branch because they are the same argument seen three ways:
where a rule is enforced, and what enforcing it there does and does not buy.

## Goal

Nothing in this repo answers "does this book balance?" Every posting is validated
per asset at `validateBalance` (`ledger/book.go:661`), so the answer is yes by
construction for everything posted *through the ledger* — and there is no answer
at all for anything that reached the store another way: a migration, a seed, a
`storetest` fixture, a hand-run `UPDATE`. §5.2.1's point is that this is exactly
why the check earns its place: it is a control on the **pipeline**, not on the
arithmetic.

Nothing refuses a booking date in the future either. `PostTransactionTx` takes
`req.BookingDate` as given (`ledger/book.go:587`), and four HTTP endpoints —
end-of-day, interest-charge on both layers, repayments, refunds — pass a
caller-typed `YYYY-MM-DD` straight down. An operator who types next month's date
posts a fact dated next month, and every report that trusts booking dates now
disagrees with the calendar. §4.1.3: *"Postings dated in the future are a
different object — a scheduled instruction — and must not sit in the ledger
pretending to be facts."*

## Out of scope, deliberately

Recorded here so the README does not over-claim once this ships:

- **Independent recomputation.** The trial balance built here shares
  `BookBalance` with the thing it checks (see *It shares the aggregate*, below).
  §5.4.2's control — a second path, a `GROUP BY` over `entries` computing the same
  totals with different code — is the successor, and is what would also turn the
  N per-account reads into one.
- **Roll-forward.** `closing == opening + movement` is the other half of §5.2.1.
  Here both sides would come from the same store aggregate, so it would assert
  little beyond that subtraction works. It becomes worth building on top of the
  independent recomputation above, not before it.
- **A booking-date bound.** No store aggregate restricts by booking date, so the
  debit and credit columns are always *as the book stands now*. Adding
  `BookBalanceAsOf` is a store-interface change in both backends with its own
  conformance cases; it is a report feature, and the control does not need it.
  `web/src/lib/statement.ts` has the same limitation for the same reason.
- **A business date and a date roll.** §4.2.4 wants the booking date assigned once
  at command acceptance from a business-calendar service, with *"nothing
  downstream ever calling `today()` for accounting purposes"*. This spec refuses
  the future; it does not introduce a business date, a roll event or a banking
  calendar. That remains the architecture review's `BusinessDate` candidate.
- **A closed-period lock.** Refusing a booking date *older* than some bound is the
  other half of §4.2.4 and needs a period-close mechanism this repo has no part
  of. Backdating stays free, which is load-bearing: the accrual recompute and
  every backdated-correction test depend on it.
- **Blocking manual postings.** `POST /participants/{pid}/transactions` stays an
  unrestricted free-form endpoint. Maker-checker is comparison item 17.

## Decisions

### The trial balance is per book, per asset, and reports rather than errors

```go
// package ledger

// TrialBalance is the debit and credit totals of every account in the book,
// grouped by asset, plus the value-dated gap. See Balanced.
type TrialBalance struct {
	// AsOf bounds the InFlight column ONLY — see the type's doc comment.
	AsOf time.Time
	Rows []TrialBalanceRow // one per asset holding an account, ascending by code
}

type TrialBalanceRow struct {
	Asset    AssetCode
	Debits   Amount // Σ over accounts of the debit-positive balance, where positive
	Credits  Amount // …and its magnitude where negative
	InFlight Amount // the same sum value-dated at AsOf, signed and net
}

// Balanced reports whether every row's Debits equal its Credits. A false answer
// means something reached the store without passing validateBalance.
func (t TrialBalance) Balanced() bool
```

`TrialBalanceTx` walks `tx.ListAccounts(ctx, book)` — which already spans the
whole book rather than one subledger — and calls `BookBalanceTx` per account,
converting each to a debit-positive figure through
`Account.Type.NormalBalance()`: a balance in the account's normal direction is a
debit for Asset and Expense, a credit for the rest. `Debits` accumulates the
positive ones and `Credits` the magnitude of the negative ones. This is
`deposit.TotalsTx`'s shape (`deposit/register.go:1781`), one layer down and
without the deposit filter, so it introduces no pattern the repo does not use.

**No store method, no schema change, and therefore no `storetest` case for a new
contract.** That is a deliberate property and not an accident of scope: a control
that needs a schema change to run is a control that cannot be added to a system
that already has the bug.

It returns a report and not an error. An imbalance is a finding an operator has
to look at, not a failure of the call, and the caller decides what a false
`Balanced()` means — `go test` fails, the API renders it, a future job could
alarm on it.

### It shares the aggregate it checks, and the spec says so

The comparison's phrasing, *"catches a bad migration or a direct store write"*
(`cbs-vs-book.md:175`), is the accurate claim, and it is narrower than "checks
the ledger". Summing `BookBalance` across every account uses the very aggregate
whose correctness is in question, so a bug *inside* that aggregate — a `WHERE`
that drops reversed entries, a sign flip per direction — cancels itself out of
both columns and the trial balance still reads zero.

What it does catch is everything that never passed `validateBalance`: entries
written directly through `Tx` (migrations, seeds, fixtures, `psql`), an account
whose type was changed after it had entries, a leg pointing at an account in
another book. That is a real class of bug nothing currently catches, and it is
the class §5.2.1 is about.

Saying this in the README is part of the deliverable. A control described as
stronger than it is, is worse than no control.

### The value-dated column is allowed to be non-zero, and that is the number worth looking at

`payment/system.go:1095-1096` posts one transaction whose two legs carry
different value dates on purpose: the debtor's GL leg takes effect now, and the
clearing-suspense leg takes effect at settlement. This is §4.2.1's PSD2 case and
it is correct. It also means that **between those two dates the value-dated sum
of that asset is not zero**, by exactly the amount in flight.

So `InFlight` is a figure, not a failure: the same walk using
`ValueDateBalanceTx` at `AsOf`, summed debit-positive across every account and
left signed. Zero means every posted leg's counter-leg has taken effect too.
Non-zero is the net of legs whose economic effect has landed while their
counterparts have not — §3.1.4's *"money in flight between consistency domains
must sit, visibly, in a named account"*, arrived at from the other direction: the
account is already named, and this is the report that makes the amount visible.

The alternative — asserting the value-dated sum is zero as well — would be a
control that fires on correct behaviour, and would be quietly disabled within a
week.

### `AsOf` bounds one column, and the doc comment leads with it

There is no way to bound the booking columns by date without a new store
aggregate, which is out of scope above. Rather than pretend, the field is
documented as bounding `InFlight` only, and the method's parameter is named for
what it does. A zero `AsOf` means now, the repo's existing convention for a zero
date (`ledger/book.go:588`).

The asymmetry is a teaching point rather than an embarrassment: it is what
"balances are aggregates over entries, and entries carry two dates" looks like
when only one of the two has an index behind it.

### One `View`, or a false break

The whole report runs inside a single `View`. N per-account reads taken outside
one unit of work can straddle a concurrent posting and show a break that never
existed — the exact write-skew shape §22.3.2 describes, in a reader. In
`store/pg` this is one transaction; in `store/mem` it is the read lock. A trial
balance that reports spurious breaks is worse than none, because the first two
get investigated and the third gets ignored.

### A booking date may not name a later day than today

```go
var ErrFutureBookingDate = errors.New("ledger: booking date is in the future")

// CheckBookingDate refuses a booking date naming a later day than the Book's
// clock does. Day granularity, not instant: an end-of-day batch legitimately
// posts at 23:59, and a caller's clock may run a second ahead of ours.
func (s *Book) CheckBookingDate(d time.Time) error
```

The rule is `DayStart(d).After(DayStart(s.now()))`, checked in
`PostTransactionTx` once `bookingDate` has been resolved — so a zero booking date,
which becomes `now`, passes trivially, and every layer that posts through the
ledger inherits the refusal.

Day granularity rather than instant granularity is the point of the rule: §4.1.3
objects to a posting *dated* tomorrow, not to one whose timestamp is a moment
ahead of the server's. An instant-level check would refuse an end-of-day run
that stamps 23:59:59 and would make the ledger sensitive to clock skew it has no
other reason to care about.

Backdating is untouched. It is the mechanism the accrual recompute is built on.

### The refusal is a ledger rule with one batch pre-check

`PostTransactionTx` is the backstop — the same argument the idempotency claim
makes for living in the schema: the ledger is the last thing between a caller and
a stored fact, so the rule is stated there whatever else validates first.

But `deposit.RunEndOfDayTx` (`deposit/register.go:1749`) and
`lending.RunEndOfDayTx` (`lending/arrears.go:172`) each loop over every account or
facility in the book. Discovering a bad date at the third facility rolls the whole
`Update` back — nothing is half-posted — but the error names a facility when the
fault is in the command. So both call `CheckBookingDate` once before their loop.
`payment.Participant.RunEndOfDay` (`payment/participant.go:160`) drives both and
inherits the check from each.

One rule, one implementation, three call sites, and no copy of it in `api`: the
handlers map the sentinel to 400 exactly as they map every other domain error.

This is the opposite of the clamp instinct — the design considered clamping a
future date back to today so a batch could never die. That is what `DisburseTx`
used to do to `LastAccrualDate`, and it is what item 11 removed: a clamp turns an
operator's mistake into a silently different result. Refusing tells them.

### Item 5: the no-`CHECK` position is a consequence, not the industry position

*The Asset Dimension in the Schema* (`README.md:1179`) argues correctly that the
asset columns carry no `CHECK` because a constraint would make `store/pg` refuse a
write `store/mem` accepts, and `store/storetest` exists to prevent exactly that
divergence. What neither it nor *Two Stores, One Conformance Suite* says is what a
bank with one backend would do — and `README.md:1141` ends on *"validation belongs
in the domain layer; the store is a per-table key/value store that happens to be
relational"*, which reads as a general position rather than as this repo's price
for a property it chose.

It is not. §4.1.5 wants `CHECK (direction IN ('D','C'))` and
`CHECK (amount_minor > 0)` and a **deferred constraint trigger** enforcing the
balance invariant *"so the database guarantees it even against buggy or malicious
writers"*; §4.1.4 wants triggers refusing mutation of booked journals; §22.2.3
wants the application role to hold `INSERT` and `SELECT` on posting tables and no
`UPDATE` or `DELETE`, because *"the grant list is the enforcement, and it is also
the first thing a competent IT auditor checks"*.

The added paragraph goes in *Two Stores, One Conformance Suite*, immediately after
the sentence that needs qualifying, and says all three: what a single-backend
production system should do, and that the repo's position is the price of a
property it chose rather than a claim about where validation belongs in general.
*The Asset Dimension in the Schema* gains one sentence pointing at it, since that
is where a reader meets the missing constraint. `README.md:1173` already voices
this exact shape for the day key versus a `GiST` exclusion constraint, so the
paragraph cross-links rather than re-argues.

This also gives the trial balance somewhere honest to sit in the same document:
it is the control you compute when the database is not permitted to enforce the
invariant for you.

## Surface

| Layer | Change |
| --- | --- |
| `ledger/trialbalance.go` (new) | `TrialBalance`, `TrialBalanceRow`, `Balanced`, `Book.TrialBalance`, `Book.TrialBalanceTx` |
| `ledger/book.go` | `CheckBookingDate`; the refusal in `PostTransactionTx` after date defaults |
| `ledger/errors.go` | `ErrFutureBookingDate` |
| `deposit/register.go`, `lending/arrears.go` | one `CheckBookingDate` call at the top of each `RunEndOfDayTx` |
| `api/handlers_ledger.go`, `api/dto_ledger.go` | `GET /participants/{pid}/trial-balance?asOf=YYYY-MM-DD`; `ErrFutureBookingDate` → 400 |
| `web` | a trial-balance card on `participants/[pid]/ledger`, its endpoint, hook and query key |
| `README.md` | a *Trial Balance* section under *Reporting and Compliance*; the booking-date rule in *Booking Date vs. Value Date*; the counterpoint paragraph in *Two Stores, One Conformance Suite*, pointed at from *The Asset Dimension in the Schema* |
| `web/src/components/hint-content.ts` | keys for trial balance, in-flight value-date gap, future booking date — written before any `[[wiki-link]]` references them |
| `web/src/lib/quiz/chapters/02-double-entry-bookkeeping.ts`, `06-booking-date-vs-value-date.ts` | a rebalance, not an append: `diversity.test.ts` holds each chapter to 18–22 questions, ≥8 concept tags, no tag more than 3×, all three tiers |
| `docs/cbs-vs-book.md` | items 5, 6 and 9 move to closed, with what each did and did not build |

No store interface change, no migration, and therefore no `COMMENT ON COLUMN`
work and no new `storetest` contract. The existing suites must stay green under
both `go test ./...` and `TEST_DATABASE_URL=… go test ./...`.

## Testing

- **Zero on a seeded book**, per asset, and `Balanced()` true. The seed exercises
  multi-asset postings, so this is the multi-currency case as well.
- **A break is detected.** Write an unbalanced pair of entries directly through
  `Tx` — the only way to create one — and assert the asset's columns differ by
  the amount. This is the test that proves the control catches what it claims to.
- **An overdrawn account lands in the debit column.** A Liability deposit account
  with a debit balance appears under `Debits`, which is §7.1.2's overdraft
  argument as a report rather than a paragraph.
- **`InFlight` tracks a payment.** Using the split-value-date posting: non-zero
  and equal to the payment amount before the settlement value date, zero at a
  date after it. The value-dated column has no other test that could fail.
- **Future booking dates.** Refused at tomorrow; accepted at today-23:59:59, at
  today, and at any past date. A future-dated `RunEndOfDay` on each layer is
  refused with **no transaction written** — assert the count, not just the error.
  The API returns 400.
- **Nothing else regressed.** The first task of the plan measures how many
  existing tests post a future booking date today; the answer changes no
  decision here but sizes the churn.

## Failure modes

| Failure | Symptom | Guard |
| --- | --- | --- |
| Trial balance read outside one unit of work | Spurious breaks under concurrent posting; the report loses credibility | Whole walk inside one `View`; `TrialBalanceTx` for callers who already have a `Tx` |
| The report is believed to check the aggregate | A store-side balance bug is assumed impossible because the totals tie | Stated in the type's doc comment, in the README section and in this spec |
| Value-dated column asserted to be zero | Correct in-flight payments read as breaks | `InFlight` is a signed figure; only `Debits == Credits` is an assertion |
| `AsOf` read as bounding the whole report | An operator believes they are seeing last month's book | The field's doc comment leads with the limitation; the README section repeats it |
| Clock skew refuses a legitimate posting | Batches fail at midnight boundaries | Day granularity, not instant |
| A batch dies mid-loop on a bad date | The error names a facility rather than the command | `CheckBookingDate` before the loop in both `RunEndOfDayTx` |
| Future-date rule copied into `api` | Two statements of one rule, drifting | The handlers map the sentinel; the rule exists once, on `Book` |
