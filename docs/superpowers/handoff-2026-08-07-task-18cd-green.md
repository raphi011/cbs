# Handoff — Task 18c/18d, the tree is green

**`go build ./...`, `go vet ./...`, `gofmt` and `go test ./...` are all clean, and
so are `npx tsc --noEmit`, `npx eslint src`, `npm run test` and `npm run build`
in `web/`.** Fourteen commits sit on top of `7d818a7`, the last five of which are
the packages going green one at a time:

```
d080875 wip 4:  a fixture's two banks were one institution
8f7971e wip 5:  whose copy the address is back-filled on
e0a7fd6 wip 6:  a pull that names no collector was submitted at the payer's bank
aac0b7c wip 7:  four logs, and the return the fixture never finished
e690432 wip 8:  where "not closed" is refused now
ef40d24 wip 9:  the clearing house has to be holding a copy before it can refuse one
394df26 wip 10: the leg the reversal reverses is on the payer's bank's copy
f0e8b16 wip 11: a pacs.002 for a payment a bank never saw is a dead letter
26ab7bd wip 12: the on-us payment, which nothing else in the system will build
9243587 payment is green
5b053d1 seed is green
19a9478 api is green, and the whole tree with it
2bcebd3 the console follows the routes that moved
6355279 the seed's submitting bank is told its payment was accepted
```

This supersedes `handoff-2026-08-07-task-18cd-step8.md`.

## Where the tree is, measured

| package | at step 8 | now |
|---|---|---|
| `ledger`, `deposit`, `product`, `lending`, `interest`, `iso20022` | green | **green** |
| `store/sqlite`, `cmd/server`, `mesh` | green | **green** |
| `payment` | 47 (really 56, plus a panic) | **green** |
| `seed` | 12 | **green** |
| `api` | 7 (really 24, plus two panics) | **green** |
| `web` | untouched | **green, and its four stale paths fixed** |

Both of the earlier numbers were low for the same reason twice over: a fixture
panicking took the test binary with it and everything declared after it never
ran. Two of those panics were fixed by this branch and the count went UP before
it went down. **A `--- FAIL` count in this repository is a floor, not a total,
until the package runs to the end.**

## Five findings that are not test churn

These changed code, not fixtures, and each is a hole the split opened that
nothing else would have found.

### 1. The on-us payment, twice

Two defects reachable only through an arrangement `Mesh.Submit` refuses — one
institution at both ends of a payment. Found by
`TestAPullRefundIsHonouredWhenOneBankIsBothParties`, whose own doc argues that a
rule holding only because no caller builds the counter-example is a rule nobody
is keeping. It was right.

- **`AcceptInboundTx`'s witness is the ROW**, which works because the row did not
  exist before that half ran — and that is false when this bank also submitted.
  An unqualified "the row is here" read a submission as an answer, so on a pull
  the debtor leg was never posted, the payer was never debited, and the
  collection then settled, paid the payee and could be returned out of money
  nobody took. The old `DebtorLegTx` witness is back for that one case, in
  addition to the row rather than in place of it.
- **`PostReturnLegTx` decides which leg is last by POSITION IN THE
  CONVERSATION**, which needs a conversation. One bank that is both parties is
  "first" about both of its legs and nothing is coming to tell it otherwise —
  `SettleReturn` moves reserves between two members and there are none. For that
  arrangement it answers out of its own book: both legs written, return over.

### 2. `payment.Settlement` carries its ASSET

`api` resolved it settlement → its cycle → that cycle's scheme, which is a chain
from the settlement agent's row into the clearing house's database. The agent
never needed the trip: `positionsIn` refuses an instruction whose legs are not
all in one asset, so the batch it posts has exactly one. New column in
`store/sqlite/schema/centralbank/0001_init.sql`, argued inside the statement.
`settlementAsset`/`settlementAssets` are deleted, next to the note where
`mandateAsset` used to be — same crossing, one task later.

### 3. Three API routes were on the wrong console

Invisible while one store served every institution, a missing table afterwards.

- `GET /settlements` and `/settlements/{sid}` are the SETTLEMENT AGENT's and
  were on the clearing house's router too.
- `GET /cycles` and `/cycles/{cid}` are the CLEARING HOUSE's and were on the
  central bank's.
- `GET /payments/audit` is now **one per institution**, so the bank router gained
  `handleBankPaymentAudit` over that bank's own book. The clearing house's route
  serves the clearing house's log and does not mention a bank's mandates at all.

### 4. `auditEventDTO` carries its BOOK

`Seq` was a store-global sequence, so an event's number identified a log without
anyone being told. Every institution's counter starts at 1 now, so `seq 7` names
as many events as there are institutions.

### 5. Where "not closed" is refused — the step-8 handoff's open question

Both settlement guards changed hands and **neither is gone.**

- **"Not closed" is the CLEARING HOUSE's.** It owns the cut-off and will not
  build an instruction for an open cycle (`mesh/csm.settle`, where it is
  measured). What reaches the agent from an open cycle is an instruction with NO
  LEGS, which is `ErrInvalidSettlement`.
- **"Already settled" stayed with the agent** and changed its evidence, from the
  cycle's status to its own settlement register. That one had to stay: a
  redelivered pacs.009 reaches the agent with no clearing house between them.

`SettleCycle`'s own doc claimed the old refusal and now says this.

## Two tests renamed because their subject was designed out

Both pinned a unit of work spanning the clearing house and a bank, and Task 18c
IS the removal of those. Neither was deleted; each kept its argument and changed
what it measures.

- `TestAFailedReversalRollsBackTheWholeRejection` →
  `TestAFailedRejectionAtABankLeavesThatBanksCopyUntouched`
- `TestSeedRejectIsOneUnitOfWork` →
  `TestSeedRejectLeavesThePayersBankUntouchedWhenItsHalfFails`

What replaces the claim is the guarantee ONE institution can still make: a bank
that cannot give the money back does not record the rejection either. **The
clearing house's decision standing is asserted beside it in both**, so a future
unit of work quietly spanning two institutions fails these tests rather than
passing them.

`TestTheClearingHouseReadsTheSettlementItDidNotPerform` →
`TestTheClearingHouseLearnsSettlementFromItsOwnCycle`, for the third rename in
its life: the read it used is gone, and what it finds the outcome out with is
its own cycle, whose status the agent's ACSC moved.

## What remains

### 1. The documentation sweep (18e)

Nothing else is outstanding, so this is the whole of the next task. Everything
the step-8 handoff listed, plus:

- **`README.md` and the quiz on the settlement id**, if either says a cycle names
  its settlement. It does not and cannot: that id is allocated inside the
  settlement agent's own unit of work in its own database and no message carries
  it back. `web/src/lib/types.ts`'s `ClearingCycle` already says so.
- **`README.md` and the quiz on the audit trail.** There is no cross-institution
  order and no combined log. An auditor holding four logs has exactly the problem
  this system now has, and answers it with the MESSAGES. `payment/audit_test.go`'s
  `paymentAudit` is the long-form argument.
- **`store/sqlite/schema/centralbank/0001_init.sql` gained `settlements.asset`**,
  argued inside the statement as an absent-derivation must be. Chapters 15/16 and
  the README's *Persistence* section are where the same claim lives.
- `payment/doc.go` and `mesh/doc.go` on the three acts a bank does with what it
  is told (`AcceptAtBank` is new, and the seed plays it as of the last commit).
- `csm.relayRecorded`'s ordering argument replaces `relayCreditTransfer`'s, which
  said the opposite.
- **`AcceptInboundTx`'s on-us caveat and `PostReturnLegTx`'s** are both new
  paragraphs of domain content and are currently only in the code.

### 2. One deferred decision, written down where it will be met

`/clearing-house/settlements` in the web console now reads the CENTRAL BANK's
port, which is the one thing `web/src/lib/api/endpoints.ts` says a console must
not do. Moving the page under `/central-bank` is a navigation change with a nav
manifest, an identity test and two quiz chapters pointing at it — an
information-architecture decision rather than a routing fix. The note is in
`endpoints.ts` above `listSettlements`.

### 3. `payment.ErrNotAPartyToThisReturn` is nearly unreachable

A bank that is neither side of a return holds no row for the payment, so its own
store refuses first. The sentinel is what a bank answers about a payment it DOES
hold and is not a party to, which needs a forged instruction to reach. Not a
defect — the store is the stronger guard, because it cannot be got wrong by a
comparison — but worth knowing before anyone deletes it.

## Nine things not to rediscover

1. **`go test ./...` is the whole suite and it passes.** There is no green
   subset to talk about any more.
2. **A payment is THREE ROWS and they legitimately disagree.** Status, party
   addresses, leg ids — every assertion has to say whose copy. `mustGetPaymentAt`
   in `payment/system_test.go` is the router; `meshHarness.bankPayment` is
   `mesh`'s.
3. **A back-fill travels only DOWNSTREAM.** The payer's bank writes the payer's
   address before the instruction goes out, so it is on all three copies; the
   payee's bank writes the payee's on arrival, at the end of the line, and
   nothing carries it back. `TestInitiateBackFillsTheAddressOnBothLegs` is the
   long form.
4. **Each institution's log opens with its own `payment.initiated`.** A bank has
   TWO per payment it holds a row for — one as submitter, one as receiver — and
   that is the shape rather than a duplicate.
5. **A bank's participant id IS its BIC**, so the four events of an admission
   answer one entity filter. What tells them apart is the database each is in.
   `api`'s `bicOf` is the identity function now and says so.
6. **Account ids collide across banks.** Bank B's fifth account and bank A's
   fifth account are one string, and a fixture using one as "an account that
   bank does not hold" gets a happy resolution to the wrong customer. This bit
   `TestAMandateBelongsToItsCreditorsBankAndToNoOther` and is the reason its
   refusal is now provoked with a ref no register holds.
7. **`submitterOfReq` fails by name.** A request that names no submitting agent
   returns a sentinel address the store refuses to open, rather than falling back
   to `testBIC` — which silently submitted every SDD fixture at the payer's bank.
   It is a sentinel and not a panic because a panic hides every test after it.
8. **`inShape` is still the instrument, not the obstacle.** Every "no such table"
   is a crossing. Do not add a table to a shape to make a test pass.
9. **A test whose subject was designed out should be re-aimed, not deleted** —
   and the re-aim should assert the NEW shape hard enough that a regression to
   the old one fails. The three renames above all do; that is what makes them
   worth more than a deletion.
