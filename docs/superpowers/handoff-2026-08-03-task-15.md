# Handoff — after sub-project 8 Task 15, merged to `main`

`main` is at `bbb7e9e`, 19 commits ahead of `76eca33`. Everything below was
verified against the code at that commit, not against a plan. The full Go suite,
the Postgres suite, the `-race` suite and the web suite were all run by the
controller — not only by a subagent — at the merge commit, and all were green.

## Read this first

`docs/superpowers/specs/2026-08-02-db-per-entity-design.md` is the sub-project's
design. It now carries a dated note under the Tasks table recording that camt.053
moved from Task 19 into Task 15a, and why.

## What Task 15 did

**Settlement stopped being one institution's act.** It was one `Store.Update`
doing the work of three: the central bank's netting transaction, a mirror leg in
**every** member's ledger, and a creditor leg in **every** payee's ledger.

Now:

1. the clearing house nets the batch and sends the pacs.009;
2. the central bank checks each net payer's reserve **in its own book**, posts
   **one** netting transaction there, and is **final either way** — ACSC, or
   RJCT/AM04. It reads no payment at all;
3. it sends each member with a non-zero position a **camt.053** statement of that
   member's reserve account;
4. the clearing house fans a per-payment ACSC pacs.002 to the submitter **and** to
   the creditor's bank — two messages on a push, one on a pull, deduplicated;
5. each bank books its mirror leg from the statement and its creditor legs from
   the payment advices, **locally**, each in its own unit of work.

The interval between 2 and 5 is the **unreconciled position**. It is real, not a
modelling convenience: in the EU it is the Settlement Finality Directive.

The measurement moved, and every move was watched failing first:

| test | was | is |
|---|---|---|
| `TestWhichBooksTheCentralBankReachesWhenItSettles` | `allBooks()` | `[CentralBankBook, Network]` |
| `TestEachBankBooksItsOwnSettlementAndNoOtherBooks` (was `TestNeitherBankTouchesABookWhileTheCycleSettles`) | `{}` for both banks | `[debtorBook]` / `[creditorBook, Network]` |

The second was **not** in the previous handoff's list. Both banks now book during
the cut-off, so the old test's *name* claimed the opposite of its measurement and
it had to be renamed — the same failure mode
`TestWhichBooksEachBankActuallyReaches` was renamed for.

## The crossing count: three closed, two open

| # | crossing | state |
|---|---|---|
| 1 | `partyTx` reads the counterparty's register for a name | **CLOSED** (Task 14) |
| 2 | `ResolveIdentifierTx` sweeps every member's register | open — Task 18 |
| 3 | `SettleCycleTx` posts in every book, one `Store.Update` | **CLOSED** |
| 4 | `ReturnPaymentTx` posts in three books | open — Task 16 |
| 5 | `AddParticipantTx` writes into `CentralBankBook` | open — Task 17 |

## The finding that this task existed to protect, and nearly lost

**AM04 lived in the mirror leg.** Measured, not reasoned about:

- a member's settlement account in `CentralBankBook` is a `ledger.Liability`
  (`payment/system.go`, the per-asset loop);
- `ledger.Book.checkSufficientBalance` guards **only** `Asset` and `Expense`
  accounts;
- so the refusal that made an underfunded net payer come back RJCT/AM04 came from
  the **mirror leg in the member's own book**, where `Reserve at Central Bank` is
  an `Asset`.

Moving the leg without moving the check would have settled a broke member's cycle
clean, with the shortfall surfacing later at the bank as a dead letter. The check
is now explicit in `SettleCycleTx`, before anything is posted, returning
`ledger.ErrInsufficientBalance` so `ReasonFor`'s `borrowedReasons` still maps it
to AM04 unchanged.

Refusing to take a member's reserve below zero is the central bank declining to
extend uncollateralised intraday credit. That is the decision a settlement agent
exists to make, and it now lives at the institution that makes it.

The mutation was watched: with the check deleted the cycle settles, the statements
go out, and the shortfall appears at the member bank as
`AURODEFFXXX could not book the settlement of cyc_7: insufficient available balance`
— never as RJCT/AM04 to the clearing house.

## Unclaimed balances, and a ruling that became a fix

`payment/system.go`'s `SettleCycleTx` and `ReturnPaymentTx` both recorded, at
length, that a payee who closes their account between acceptance and the cut-off
is credited into a `Closed` account and the money strands for ever — and that
neither fix was available, because refusing at settlement failed the whole batch
and a `Cleared` payment has no route out of its cycle.

The split made the check affordable. Each bank has an **Unclaimed Balances**
account per asset (a `ledger.Liability` — money the bank owes somebody it has not
yet identified), `PostCreditorLegTx` calls `CheckCreditTx`, and a closed account
diverts that one payment at that one bank. The payment still reaches `Settled`,
because it did: the reserves moved and the payee's bank has been paid. What is
unsettled is which of that bank's own accounts holds the money.

**Only `deposit.ErrAccountClosed` diverts.** Anything else from that path is a
store failure and is returned. That discrimination is the settlement-time twin of
the `checkPartyTx` defect Task 14 fixed, and the first review round here caught
that the guard as originally written **could not fire** — `glAccountTx` collapses
every `GetDepositAccount` error into `ErrAccountNotInParticipant`, so a dropped
connection would have sent a payee's money to unclaimed balances and committed the
payment `Settled`.

## The Critical bug the whole-branch review found, and why per-task review could not

**Returning a payment that settled into Unclaimed Balances clawed back from the
wrong account.** Task 15b.1 added the diversion; Task 15b.3 split the creditor
leg; neither touched `ReturnPaymentTx`, which debited `creditorGL` — the payee's
own account, which never received the money.

Measured before the fix:

```
AFTER SETTLE:  bankB unclaimed=30000  bob=0      bankB reserve=30000
AFTER RETURN:  bankB unclaimed=30000  bob=-30000 bankB reserve=0
```

The payee's **closed** account left at −30000, the Unclaimed liability never
released, and the bank 30000 of reserves out of pocket. **The books still
balanced** — two liabilities netting to zero — so no ledger guard fired and no
test noticed.

The fix records `Payment.CreditorLegAccount`, in both arms, round-tripped through
both stores and the schema; `ReturnPaymentTx` claws back from it. Deriving it at
return time was rejected and the reason is worth keeping: an account open at
settlement and closed afterwards is indistinguishable from one closed at
settlement, so the fact has to be recorded when the diversion happens.

**This is the shape of defect a per-task review structurally cannot see.** Budget
for the whole-branch review on every future task in this sub-project; on this one
it was the difference between shipping and not.

## Known defects and deferrals carried into `main`

- **The mid-flight rollback at the settlement seam is unwitnessed.** Each
  institution's posting is now its own unit of work, so the two tests that used to
  demonstrate a partway failure now fail before anything is written. This is
  recorded in the open at `payment/system_test.go` and `payment/audit_test.go`
  rather than hidden, and the claim still lives on
  `TestAFailedReversalRollsBackTheWholeRejection`.
- **`SettlementAdvice.Advised` is not committable through the settlement path.**
  The row and the mirror-leg posting are one unit of work, so a failed posting
  rolls the row back too. **The plan asserted the opposite** — that a failed
  posting leaves the row at `Advised` — and that false claim reached **eleven**
  places before a reviewer checked it against `store/mem/mem.go` and
  `store/pg/pg.go`. The unreconciled position is visible as a non-zero clearing
  suspense plus the **absence** of a row. Do not "fix" this by splitting the write
  from the post: that would let a bank post its mirror leg and fail to record it.
- **No operator or web surface shows an unreconciled position.** Task 19.
- **`SettlementAdvice.ClosingBalance` is stored and unread**, and
  `ListSettlementAdvices` has no production caller. Both are Task 19 scaffolding
  and both say so where they are declared.
- **A failed camt.053 send suppresses the ACSC for every bank in the cycle**, not
  only the unreachable one, because `advise` returns on the first failing send and
  `cb.answer` never runs. Documented in `mesh/centralbank.go` and `mesh/doc.go`.
  **Unreachable only because this transport cannot lose a message** — and
  sub-project 8's whole direction is a transport that can.
- **`SettleCycle` surfaces `ErrAccountNotInParticipant` rather than the underlying
  store error**, because `glAccountTx`'s collapse is shared with `checkPartyTx`.
  The money goes to the right place; the operator is told the wrong cause.
- **`closingBalanceIn` does not check the CLBD balance's own currency** against the
  entry's asset. Adversarial input only — a statement in the wrong asset resolves
  to a different settlement account and is refused by `ErrStatementNotForThisBank`.
- **Frozen and dormant payees are credited normally.** `requireCreditable` refuses
  `Closed` only. Deliberate, but no test pins it, so a future widening of
  `requireCreditable` would silently start routing frozen payees' money to
  unclaimed balances with nothing failing.
- **A payment stuck at `Cleared` after its cycle is `Settled` is terminal.** If a
  payee's bank never posts its creditor leg, the payment cannot be returned
  (`ReturnPaymentTx` wants `Settled`), rejected (`RejectAtCSMTx` wants `Initiated`)
  or re-cycled. The only remedy is retrying `PostCreditorLeg`.
- **`AdvisedMovement.ValueDate`** is built into `Ntry/ValDt`, parsed back, and never
  read. Recorded as carried-and-unread.
- **No golden file has ever been schema-validated here.** `camt053.xml` inherits
  the gap: `TestGoldenFilesValidateAgainstTheSchema` skips every subtest because
  `iso20022/testdata/xsd/` is absent for licensing reasons. A skip is not a pass.

## What the next sub-project is

**Task 16 — the return becomes a conversation.** `ReturnPaymentTx` still posts in
three books in one unit of work, two of which are member banks'.

Two things the spec already says, and one this task adds:

- **`OrgnlTxRef` must be implemented on pacs.004 or the return cannot survive
  isolation.** A pacs.004 names no parties, so a central bank with no payment rows
  cannot tell whose settlement accounts to move. That reverses a ruling recorded on
  pacs.002's `PaymentTransactionStatus` — **on pacs.004 only**, deliberately.
- **A failed clawback is direction-dependent.** On a push the returning bank is the
  payee's own bank and refuses locally if it cannot fund it; on a pull the payer's
  eight-week refund right is unconditional, so the creditor's bank must honour it
  and bears the credit risk — a new `Returns Receivable` asset account.
- **New:** `Payment.CreditorLegAccount` now records where the creditor leg landed,
  and `ReturnPaymentTx` reads it. Task 16 must carry that through the split — the
  returning bank knows where its own money went, and the central bank must not need
  to.

`TestWhichBooksAReturnReaches` is the tripwire: `allBooks()` → `[CentralBankBook,
Network]`. Same contract as this task's.

## Verification, exactly

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./...                                                            # store/mem
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

Postgres is local Homebrew — **`make test-pg` uses Docker and does not work
here.** The Postgres run is not optional: this task added two columns and a table.

**Run these yourself before merging.** A subagent reporting them green is a claim,
not evidence; on this branch one reported a Postgres run it could not have made
(no `TEST_DATABASE_URL` in its environment) and the suite passed by skipping.

## The process lessons, which cost the most

**1. The prose outruns the code, and it is the dominant defect class here.** Six
of the eight review rounds on this branch were comments asserting things the code
does not do — and almost all of them were inherited *verbatim from the plan*. The
plan is written before the code exists, so every comment it dictates is a
prediction; when the code lands differently, the comment ships as a lie with a
reviewer's blessing.

**The fix for next time is in the plan-writing step, not the review step.** Briefs
should carry the *reasoning* a comment must capture and far less pre-written
comment text, with an instruction to write the final prose from the code. Three
separate reviews passed over the `Advised`-row claim in Go and SQL before a fourth
caught it — and only because a later task promoted it into `README.md`, where
somebody finally checked it against `mem.go`.

**2. A test whose failure nobody has observed is not evidence — and "watched
failing" has a failure mode of its own.** Two guards on this branch were proved
unwitnessed by replacing them with `panic` and watching the whole suite stay
green. Separately, an ordering assertion could not be falsified by the obvious
mutation: swapping `advise` and `answer` leaves a race the central bank usually
wins, so only swap-plus-a-delay fails deterministically. That trap is now written
into the test's own doc. **When a mutation does not fail, the assertion is wrong
until proven otherwise — but so is the mutation.**

**3. The whole-branch review is where cross-task defects live.** Per-task review
is scoped to one diff by construction, so a bug created by task A and left
unhandled by task C is invisible to both. The Critical money bug on this branch
was exactly that shape, and it was found by a reviewer that built a probe and ran
it rather than reading.

## The rules that cost the most when forgotten

- A `[[wiki-link]]` to a key missing from `hint-content.ts` throws at runtime and
  takes **every** route down while `next build` stays green. `npm run test` catches
  it in hint bodies *and* quiz explanations. Run it, and load a page. (One
  implementer caught itself writing `[[payment-return]]` to a non-existent key on
  this branch; `npm run test` is what caught it.)
- **No test in this repository can catch a web rendering regression.** Require a
  screenshot for web changes, and look at it.
- `rm -rf web/.next` before `npm run typecheck`.
- Never hand-write a backend path; use `cb()` / `csm()` / `bank(pid, …)`.
- `web/src/lib/quiz/diversity.test.ts` holds each chapter to 18–22 questions, ≥8
  distinct `concept` tags, no tag more than 3×, and all three difficulty tiers.
  **Chapter 9 is now at 22 — the maximum.** A new question there means replacing
  one.
- Documentation is domain content. `README.md` is authoritative; `hint-content.ts`,
  the quiz chapters and `0001_init.sql`'s comments duplicate it **by design** and
  must agree with it and with the code. **A correction is a sweep, not an edit** —
  and the sweep must include Go and TS comments, test files, `docs/*.md`, and the
  quiz chapters the change did not obviously touch. Chapter 10 contradicted this
  branch's headline concept and scored a false statement correct, having never
  been opened by any task in it.
