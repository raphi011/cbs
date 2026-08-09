# Design — Task 19: reconciliation a bank can do for itself

Branch `spec/bank-reconciliation`, based on `7bf48d4`.

Numbered 19 because it continues sub-project 8's task sequence, and named as
that sub-project's deliberate remainder in four places in
[`2026-08-02-db-per-entity-design.md`](2026-08-02-db-per-entity-design.md).

Nothing here is a new capability. Every figure this task needs is already
written, already persisted and already unread: `SettlementAdvice.ClosingBalance`,
`ListSettlementAdvices` (no production caller), `AdvisedMovement.ValueDate`
(carried and unread), and the `Unclaimed Balances (<asset>)` account, which
`SettleAtBankTx` fills and only an operator who already knows the payment id can
empty. The whole task is readers, and one metadata key.

`payment/recon` is the instrument this narrows. It opens all N+2 databases at
once **precisely because no institution may**, and everything below has to work
from one database and the messages that arrived at it. Where it cannot, that is
a finding, not a licence.

## Decisions taken, and what they cost

1. **The bank's own reconciliation is a `payment.Network` method.** A `Network`
   is one institution's handle and every method on it is an act that institution
   may perform (`payment/recon`'s package doc says so from the other side, as its
   reason for *not* being one). This is such an act. It lands in a new file,
   `payment/reconcile.go`, in package `payment`.
2. **The ageing walk is a `ledger` primitive with no payment knowledge in it.**
   "Decompose a balance into dated lots" is arithmetic over one account's
   entries; which account and what counts as too old is the payment layer's.
   Same shape as the designed-but-unbuilt trial balance: a new file, no store
   method, no schema change.
3. **The return of an unclaimed balance needs no new flow.** On a push, the bank
   holding the balance *is* the returner, and `clawbackTx` already debits
   `Unclaimed` with no customer check. What is missing is the report that finds
   the balance. On a pull the bank holding it is **not** the returner, and the
   instrument that would be is `pacs.007`, deferred by design. That is the
   finding; see below.
4. **A reconciliation run is not durable as rows.** No findings table. A run that
   finds a break appends an audit event in the acting institution's own log,
   which exists in all three shapes and is already ordered. Cost: there is no
   history of "this break was open for three days", only "a run at T found it".
5. **No business date, and no dependency on the one the roadmap wants.** The
   ageing report measures whole days from an entry's booking date, and what
   counts as overdue is a caller's parameter. Cost is stated under *The clock*.

## The claim that is not true, and it is in four layers

`README.md`, `2026-08-02-db-per-entity-design.md` and the hint registry all say
some form of: *a bank that was told and could not book looks exactly like one
that was never told, and telling the two absences apart from inside the bank is
what the stored closing balance is for.*

The first half is right. The second is not, and it does not become right by
being implemented.

A closing balance only ever arrives **on a statement the bank holds**. Both
absences leave no row and no statement. Suppose a bank has booked a statement
closing at 500 and the agent then moves its reserve to 300:

- if the message was lost, the bank holds nothing newer, its own reserve reads
  500, and the newest closing balance it holds reads 500 — they agree;
- if the message arrived and the posting failed, the whole unit of work rolled
  back, the bank holds nothing newer, and the two figures agree in exactly the
  same way.

So the closing balance distinguishes nothing here. What it *does* detect is
sharper and worth having, and it is a different sentence:

| failure | detected from inside one bank by |
|---|---|
| a mirror leg posted in the wrong direction or amount | the advice's `ClosingBalance` against the reserve's running balance at that leg |
| a reserve moved by something with no statement behind it | an entry on `Reserve` that is neither a mirror leg nor a lodgement |
| an advice row claiming a posting that is not in the book | its `MirrorTx` absent from the reserve's history |
| a statement **missed, and then superseded** by one that did arrive | the same running-balance check, at the later statement |
| **the last statement never arriving** | **nothing.** It is undetectable from inside, and stays so |

The last row is the roadmap's own note about there being no periodic statement,
arrived at from the other direction. It is unreachable in this transport —
`mesh/doc.go` is explicit that delivery is exactly-once and in order — and it
becomes reachable *and invisible* the moment the mesh gains a lossy one. This
task does not build a periodic statement. It writes the row down and asserts it
with a test, so that the next reader does not have to rediscover it.

The correction moves through all four layers as a fact change; see
*Documentation layers*.

## The reserve identity, and it closes exactly

Exactly two things post to a member bank's `Reserve at Central Bank (<asset>)`:

- the **mirror leg** of an advice (`PostSettlementAdviceTx`), which is recorded
  by a `settlement_advices` row naming the transaction it posted (`MirrorTx`);
- the **lodgement** leg (`LodgeReservesTx`), which is this bank's own act and
  posts **before** the agent does, deliberately, because a `camt.025` carries no
  amount to post from.

There is no third, and `payment/recon` reads only the balance, so nothing today
notices a third appearing. That gives one bank, over its own database, a closed
identity per asset:

```
for each row of the Reserve account's history, in book order:
    it is some advice's MirrorTx      -> movement == advice.Movement
                                      -> running  == advice.ClosingBalance
    or it carries a lodgement ref     -> this bank's own act; nothing to check
    or it is a BREAK

and every advice row's MirrorTx appears in that history, or it is a BREAK.
```

Two consequences worth stating because they are not obvious:

- **`Reserve − (the newest booked ClosingBalance)` is never negative and is
  exactly the lodgements posted since**, which the bank can enumerate from its
  own book. An unbooked *later* movement leaves no statement, so it does not move
  either side of that subtraction. So the residual after subtracting those
  lodgements is zero or it is a break, with no "in flight" excuse available.
- **A bank now catches, alone, the damage `TestTheHarnessCatchesAReserveMirror
  ThatDiverged` needed five databases for.** That test posts a reserve movement
  with no statement behind it. Under this identity it is an entry on `Reserve`
  in neither class, and the bank's own run names it. The harness stays: it
  catches the *other* member's side and the agent's, which this cannot.

**The one line of new code in the domain** is `{"lodgement_ref": in.Ref}` on
`LodgeReservesTx`'s posting. Transactions already carry metadata and already
store it; classifying by parsing an idempotency key would work and is refused,
because a key is a uniqueness claim and not a label, and reading one as a label
is how a rename becomes a silent reclassification.

## The suspense side is an ageing and NOT an identity

There is no closed-form identity for clearing suspense from inside one bank, and
the reason is structural rather than missing work: **the mirror leg is netted**.
One cut-off produces one movement per member covering every payment in the
batch, with no per-payment identity on it — deliberately, because a member holds
no cycle and could not resolve one. So a bank cannot decompose its own suspense
balance into the payments that put it there.

What it can do is what an operations team does: age the balance FIFO over the
account's own entries. Oldest lots carry the balance; a netted mirror leg
consumes the oldest customer legs first, which is both the conventional rule and
the one that reads correctly here — a settlement discharges the batch that
opened before it.

So the suspense report yields **positions with ages and no breaks**, and that is
the honest shape. A position older than the scheme's `SettlementDelay` plus a
margin is escalated in the report and is still a position: from inside the bank
nothing proves it wrong. The distinction `payment/recon` draws survives the
narrowing with the balance of the two changed — the reserve side produces breaks
this bank can stand behind, the suspense side produces positions only.

**Unclaimed balances are the contrast, and it is why they are aged separately.**
Every credit into `Unclaimed` is one payment's diverted creditor leg, carrying
`payment_id` in its metadata (`paymentMetadata`, written by `SettleAtBankTx` in
both arms). So that account's ageing is per payment and exact, where suspense's
is FIFO lots. One primitive, two accounts, two grades of answer, and the
difference is a fact about the postings rather than about the report.

## The clock

Three banking business days for SCT needs a business date, and there is none —
no roll event, no calendar, no holiday handling. That is its own roadmap item
and this task does not take a dependency on it.

**The ageing report measures whole days from an entry's booking date, and takes
the window as a parameter.** `payment` states the SCT default (3 days) in one
place, as calendar days, with the approximation named.

What that costs, exactly: a balance that arrives on a Thursday is flagged on
Sunday where the rulebook would flag it on Tuesday. The error is always *early*,
never late.

**And it costs nothing operationally, because the window gates no act.** A
receiving bank may return an unapplicable credit at any time; the rulebook window
is a deadline on its obligation, not permission to start. `Mesh.Return` is
already available for the whole life of a settled payment and this task does not
put a clock in front of it. The window exists only to decide which line of a
report is printed in bold — which is exactly the amount of correctness a
calendar-day approximation can carry.

Making the clock a parameter rather than a constant is also what lets the
business-date work, when it lands, replace one call site instead of auditing the
domain for `today()`.

## The unclaimed return, and the pull path that has no returner

On a **push**, everything needed is built. The payee's bank holds the unclaimed
balance, `ReturnerOf` names it the returner, `PostReturnLegTx` accepts a settled
payment, and `clawbackTx` debits `Unclaimed` with no customer check —
"the payee never received this money, and the bank is releasing the obligation it
took on". `TestReturningAPaymentThatSettledIntoUnclaimedBalancesReleasesThe
Liability` already proves the posting. The gap is that nothing *finds* the
payment: an operator has to already know its id.

On a **pull**, the bank holding the unclaimed balance is the **creditor's** bank
(the biller's account closed between its answer and the cut-off), and
`ReturnerOf` names the **debtor's** bank the returner. So the bank stuck with the
money cannot initiate the return, and it should not be able to: a `pacs.004` on a
pull is the payer's bank's instrument, and the payer has not asked for anything.

The instrument the creditor's bank actually wants is a **Reversal** — `pacs.007`,
the creditor side sending collected funds back — and it is deferred by design in
`iso20022/doc.go` along with `camt.056`. This task does not build it. It reports
the balance, names the reason it cannot be cleared, and asserts the gap with a
test rather than a paragraph, so that the R-transactions item in the roadmap
inherits a failing-by-construction case instead of a note.

**What a return does to the payment's status is unchanged**, at every
institution's copy: the second leg sets `Returned` by the existing rule, and a
payment whose creditor leg went to `Unclaimed` reaches it exactly as one that
reached the payee does. Nothing about the payment is different; only where the
money was.

## Is a run durable

**No findings table.** Three reasons, and the second is the one that decides it:

- a new entity has to be in `store/storetest` and in exactly one shape, and it
  would be the bank's;
- **a finding is a pure function of the books at a moment, so a stored finding is
  a cache that can disagree with the books** — which is precisely the defect
  class this sub-project exists to remove, re-introduced by the instrument built
  to detect it;
- what makes a finding durable and actionable already exists per institution and
  is already ordered: the audit log.

So a run appends `reconciliation.run` and one `reconciliation.break` per finding,
in the acting institution's own log, and returns the report. **The read and the
appends are one unit of work**, which is what makes the finding a claim about a
consistent snapshot rather than about five reads taken while a cut-off committed
underneath them.

That makes the run a read-then-write, which is the one thing in this repository
the ephemeral store hides: on it a retry's read blocks until the winner commits.
So the test that a concurrent settlement cannot make a run lie **opens a file**.

## New in the domain

`ledger/history.go` — no store method, no schema change, built on the existing
`Tx.ListTransactionsForAccount`:

```go
type HistoryRow struct {
    Transaction TransactionID
    BookingDate, ValueDate time.Time
    Movement    Amount            // signed in the account's normal direction
    Running     Amount
    Description string
    Metadata    map[string]string
}
type AccountHistory struct {
    Account AccountID
    Normal  Direction
    Rows    []HistoryRow
    Closing Amount
}
func (b *Book) AccountHistory(ctx, id) (AccountHistory, error)
func (b *Book) AccountHistoryTx(ctx, tx, id) (AccountHistory, error)

type Lot struct {
    Transaction TransactionID
    Since       time.Time
    Days        int
    Amount      Amount
    Description string
    Metadata    map[string]string
}
type Ageing struct { Account AccountID; AsOf time.Time; Balance Amount; Lots []Lot }
func (h AccountHistory) AgeAt(asOf time.Time) Ageing   // pure; no I/O
```

`AgeAt` is a method on the history and does no I/O, so the FIFO rule is testable
with no store at all — and one read answers both questions, because the
running-balance check and the ageing walk want the same rows in the same order.

Two rules `AccountHistory` inherits and must state: **reversed transactions
count**, because a reversal posts its own mirrored entries and those are what
cancel the original (`BookBalance`'s rule, and a history that dropped them would
not sum to the balance); and the order is the store's listing order — creation
instant then insertion sequence — which is the only total order a book has.

`payment/reconcile.go` — on `*Network`, one institution's own act:

```go
func (s *Network) Reconcile(ctx, asset) (Reconciliation, error)   // and a Tx sibling
func (s *Network) AgeClearingSuspense(ctx, asset, window) (Ageing, error)
func (s *Network) AgeUnclaimedBalances(ctx, asset, window) (Ageing, error)
```

`Reconciliation` carries the reserve figure, the newest booked closing balance
and its reference, `Breaks []Finding`, and `Positions []Position` — the same two
kinds `payment/recon` reports, under the same rule that only the first is a
defect.

**Named nothing like `payment/recon`.** `Network.Reconcile` and
`recon.Reconcile` are one word apart and answer different questions from
different numbers of databases; the doc on each names the other and says which.

`api`, on a bank's own listener only — the port carries the identity, and there
is nowhere on these routes to name another bank:

```
POST /reconciliation                 ?asset=EUR — a run, and it is an act
GET /settlement-advices              the stored camt.053, read back at last
GET /clearing-suspense/ageing        ?asset=EUR&window=3d
GET /unclaimed-balances/ageing       ?asset=EUR&window=3d
```

## Testing

The method is `mesh/recon_test.go`'s and for its reason: an instrument that has
never been shown a break is one nobody has any reason to believe. Take a network
that reconciles, put **one** thing wrong in it behind the institution's back, and
check that the run says so and says which account. None of these states is
reachable through the domain — which is the whole point — so the fixtures write
rows and post entries directly.

The difference from the harness's fixtures is that the damage now has to be
inside **one** bank, because that is all the instrument can see.

| test | what it damages, and what must be said |
|---|---|
| `TestABanksOwnBooksReconcile` | the control. Nothing damaged; no breaks, no positions, on a drained network |
| `TestABankCatchesItsMirrorLegPostedAgainstTheWrongAmount` | an advice row whose `Movement` disagrees with the leg its `MirrorTx` names |
| `TestABankCatchesAReserveMovedWithNoStatementBehindIt` | the harness's `…MirrorThatDiverged` damage, caught from inside one database |
| `TestABankCatchesAnAdviceClaimingAPostingThatIsNotThere` | an advice row whose `MirrorTx` names nothing |
| `TestAMissedStatementIsCaughtByTheNextOne` | delete one advice row and its leg, then book the next; the running balance must not match |
| `TestTheLastStatementNeverArrivingIsUndetectableFromInside` | the negative result, asserted. The run is clean and the harness is not |
| `TestALodgementInFlightIsNotABreak` | a lodgement posted after the newest advice. Reserve exceeds the closing balance legitimately |
| `TestSuspenseAgeingIsFIFOAndANettedLegConsumesTheOldest` | the FIFO rule, against a netted mirror leg spanning two payments |
| `TestUnclaimedBalancesAreAgedPerPaymentAndSuspenseIsNot` | the two grades of answer, in one fixture |
| `TestAnUnclaimedBalanceOnAPullHasNoBankThatMayReturnIt` | the `pacs.007` gap, asserted rather than written down |
| `TestReturningAnAgedUnclaimedBalanceClearsIt` | the report finds it, `Mesh.Return` clears it, the liability goes to zero |
| `TestAConcurrentSettlementDoesNotMakeAReconciliationRunLie` | **opens a file.** The run is read-then-write; the ephemeral store hides the class |
| `TestABanksOwnRunAgreesWithTheHarness` | over `seed`'s widest deployment: every break this finds, `payment/recon` finds too |

The last one is the calibration that matters most and is also the one that can
only ever hold in one direction. A bank sees a subset, so agreement means *no
break this reports is absent from the harness's report* — never the converse, and
the test asserts the containment rather than equality, with the reason on it.

Prose asserting what code does needs a test. Every claim in this document that
is not about a decision is in the table above, including the two negative ones.

## Documentation layers

The fact that moves is the closing-balance claim, and it moves in all four:

- **`README.md`** — *Settlement Is Final at the Central Bank, and the Banks Catch
  Up*. The sentence "Telling the two absences apart from inside the bank is what
  the stored closing balance is for" is replaced by what the closing balance does
  catch, and by the row it does not. The *Unclaimed Balances* section gains the
  return window and the pull path's absent returner. *Persistence* gains nothing:
  there is no schema change.
- **`web/src/components/hint-content.ts`** — the same correction wherever the
  hint bodies carry it, and a key for the ageing report if one is introduced. A
  `[[wiki-link]]` to a key not in the registry throws under `RootLayout` and
  takes every route down; `npm run test` catches it in hint bodies *and* in quiz
  explanations, and a page still has to be loaded.
- **the quiz** — chapter 9 (clearing and settlement), 10 (the interbank network),
  12 (SEPA, for the return window) and 14 (snapshots, audit and statements, for
  what a statement can and cannot catch). `diversity.test.ts` holds each chapter
  to 18–22 questions, ≥8 concept tags, none more than 3×, all three tiers, so a
  replaced question keeps its tag budget.
- **the schema** — `store/sqlite/schema/bank/0001_init.sql`,
  `settlement_advices`. Its comment already says "Task 19 is the reconciliation
  that does"; that becomes a statement of what reads it, and the `closing_balance`
  paragraph gains the same correction as the README. **Inside the statement's
  parentheses**, where it already is.
- **`2026-08-02-db-per-entity-design.md`** — a dated subsection under *What the
  statement does and does not catch*, per that file's own convention of
  correcting below rather than rewriting the row.
- **`payment/types.go`** — `SettlementAdvice`'s "ClosingBalance is stored and
  nothing checks it yet" heading, and `AdvisedMovement.ValueDate`'s "carried and
  unread" note, which stays carried and unread unless 19b uses it (it does not;
  see below).
- **`payment/store.go`** — "`ListSettlementAdvices` has NO production caller"
  and "`SettlementAdvice.ClosingBalance` is read by nothing" both stop being
  true.

## Tasks

**19a — `ledger/history.go`.** `AccountHistory`, `AgeAt`, and their tests. No
other package changes. Lands alone because the FIFO rule is worth reviewing
without a payment fixture in front of it.

**19b — the reserve reconciliation.** `payment/reconcile.go`,
`Network.Reconcile`, the `lodgement_ref` metadata, the audit events, and the
first production reader of `ListSettlementAdvices`. Everything in the table above
down to `…LodgementInFlight…`.

> **Landed 2026-08-09**, with two departures from the plan above, both small.
> The clearing suspense is reported as a `Position` carrying its ageing by
> `Reconcile` itself, rather than waiting for 19c: it is the same read and the
> same 19a primitive, and a run that named breaks and said nothing about money in
> flight would have been the wrong shape to review. What 19c is left with is the
> ageing reports as reports — the window, the unclaimed account, and the pull
> path's gap. And `payment/reconcile.go` carries `Finding` and `Position`, which
> the plan above did not name; `Finding` is `recon.Break`'s narrower sibling and
> is keyed by ACCOUNT rather than by prose, because a bank's own finding always
> has one to name and the harness's cross-institution ones do not.
>
> `payment/store.go` and `payment/types.go` had their now-false claims corrected
> with the change that falsified them rather than in 19e. The four documentation
> layers are still 19e's.

**19c — the two ageing reports and the window.** `AgeClearingSuspense`,
`AgeUnclaimedBalances`, the SCT default stated once, and the pull path's asserted
gap.

> **Landed 2026-08-09.** `payment/ageing.go`, `ReturnWindowDays = 3`, and one
> `AgeingReport` type that `Reconciliation.Positions` reuses, so the two reports
> say it once. Three things came out different from the plan:
>
> - **Only ONE account gets a window, and it is not a parameter.** A clearing
>   suspense has no rulebook deadline — what discharges it is a conversation, and
>   a conversation has no due date this bank could hold anybody to — so its lots
>   carry `Deadline: 0` and the report ages without judging. Inventing a suspense
>   window would have been inventing a rulebook. The unclaimed account's window
>   is resolved PER LOT from the payment's own scheme rather than passed in,
>   which is exact and cost nothing: `paymentMetadata` already carries the scheme
>   on every leg.
> - **Two return legs were carrying no payment metadata**, alone among the
>   payment layer's postings, so an unclaimed balance arising from a refund could
>   not be attributed at all. `clawbackTx` and `refundTx` now carry
>   `paymentMetadata` like every other leg. Same argument as `lodgement_ref`: the
>   identity belongs on the posting, and the alternative was parsing a
>   description.
> - **The pull finding is bigger than "there is no `pacs.007`".** See the
>   R-transactions item in the roadmap: `PostReturnLeg` does not refuse the
>   creditor's bank on a pull, and called with no `pacs.004` behind it it strands
>   the money in that bank's own suspense with its copy at `Returned`. The report
>   blocks the lot rather than offering the obvious method, and the test measures
>   what the obvious method does so a later reader does not reach for it.

**19d — `api`.** The four routes, DTOs, and the surface test. Bank listener only.
The run is a **`POST /reconciliation`** and not a GET: it appends to the audit
log, so it is an act, and a GET that writes is a lie about the method. The three
reads beside it stay GETs.

**19e — the documentation sweep.** All four layers plus the two spec files, in
one change, after the code is green.

## Verification

`go build ./...`, `gofmt`, `go vet ./...`, `go test ./...` — no database, no
setup, no second run to keep green. `npm run test` and a loaded page for the web
layer. `payment/recon` over `seed`'s widest deployment stays green throughout:
this task adds a second instrument and changes nothing the first one measures.

## What this task does NOT do

Recorded so a reader who expects them knows they were priced.

- **No periodic statement**, so a `camt.053` that never arrives stays
  undetectable from inside the bank. Unreachable in this transport; reachable and
  invisible the day the mesh gets a lossy one. It is the one row of the table
  above with nothing in the right-hand column, and it is asserted rather than
  fixed.
- **No `pacs.007`**, so an unclaimed balance on a **pull** is reported and cannot
  be cleared by the bank holding it. The R-transactions item in the roadmap is
  where it lands.
- **No business date and no banking calendar.** Calendar days, always early, and
  the window gates no act.
- **No findings table**, so there is no history of how long a break stood — only
  the audit event each run leaves.
- **No `AdvisedMovement.ValueDate` change.** Using it would alter every mirror-leg
  transaction this system stores and settles a domain question — whether a
  bank's reserve mirror takes effect on the agent's value date or on the day the
  bank booked it — that has a reconciliation consequence of its own. This task
  reads the books it finds; it does not re-date them.
- **No web console panel.** The API makes the reports reachable; a bank-operator
  screen for them is 6b's kind of work and is unclaimed.
- **No change to `payment/recon`.** It keeps its five databases and its two kinds
  of finding. This task is the narrow instrument beside it, not its replacement,
  and the containment test is what keeps the two honest about which is which.
