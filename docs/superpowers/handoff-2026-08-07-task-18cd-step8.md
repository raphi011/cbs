# Handoff — Task 18c/18d, `mesh` is green, `payment` is halfway

**Everything is committed, `go build ./...`, `gofmt` and `go vet ./...` are all
clean.** Six commits sit on top of `3a1e393`:

```
e70adca step 4: a bank keeps its own copy's state, and the clearing house stops lying
9905ade step 5: the book want-lists, measured
c3df832 step 6: whose copy, and the settlement id nobody can name
994ebf5 step 7: mesh is green
6cf22a3 step 8 (wip 2): payment's fixtures learn which institution they are asking
9299828 step 8 (wip 3): the payee's account is closed at the payee's bank
```

This supersedes `handoff-2026-08-07-task-18cd-step3.md`.

## Where the tree is, measured

| package | at step 3 | now |
|---|---|---|
| `ledger`, `deposit`, `product`, `lending`, `interest`, `iso20022` | green | **green** |
| `store/sqlite`, `cmd/server` | green | **green** |
| `mesh` | 29 | **green** |
| `payment` | 10 visible | **47** |
| `api` | 7 | **7** |
| `seed` | 12 | **12** |

**`payment` went UP and that is the important number in this document.** A
fixture opening `bank_1`'s store panicked and took the test binary with it, so
everything declared after it in the package never ran. The handoff's "payment's
10" was ten failures plus a panic hiding sixty-five more. Nothing regressed;
what changed is that the package can be measured.

## THE DEFECT is fixed, and the handoff's proposed fix was half wrong

The four `sct` assertions and the three return tests are green. What landed is
not what the last handoff prescribed, and the difference is worth not
re-litigating.

### What was right

A bank's copy records **Accepted** (`AcceptAtBankTx`, on the same footing as
`RejectAtBankTx` and `SettleAtBankTx`), `csm.tell`'s second message goes to the
**other bank** rather than to the payer's, and `csm.tellSettled` reaches **both
agents**. The third of those is what the three return tests were: on a pull the
payer's bank was never told a payment settled, so its copy stayed Initiated for
ever and it then refused to return a payment it could not see had settled.

### What was wrong: the state is not a guard

The handoff said to make `Accepted -> Rejected` illegal at a bank, so that a
bank refuses a rejection of a payment already in a cycle. That is not
implementable and it is not correct:

- **Not implementable.** `TestAnOperatorRejectionRefundsThePayerOnlyOnceTheMessageArrives`
  rejects an ACCEPTED push payment, and the payer's bank is the submitter, so its
  copy is Accepted. A genuine post-acceptance rejection and a forged one are the
  same bytes from the same address about a payment in the same state. The shared
  row was the discrimination and it is gone.
- **Not correct.** A rejection is a pre-SETTLEMENT act, not a pre-acceptance one.
  The EPC rulebook defines a Reject as a transaction diverted before interbank
  settlement; STEP2 and RT1 reject after acceptance routinely, because a
  participant that fails to fund its position gets the cycle's transactions
  rejected back. A bank refusing on "I was already told ACCP" refuses the
  ordinary case.

So `Accepted -> Rejected` is legal at a bank, and the guards a bank still has are
**Settled** and **Rejected** — reversing after finality, and reversing twice.
What really stops a forged pacs.002 is the channel (authenticated, non-repudiable,
closed network) plus reconciliation, and that is stated in `Network.transition`
rather than faked.

### What replaced it: the clearing house stops lying

Both bogus RJCTs in those tests came from the CSM answering about a payment it
had **not** rejected. Both are closed at the source:

- **`relayRecorded` records BEFORE it routes**, because the row is also the
  DUPLICATE CHECK. A replayed instruction is refused at ingestion, as a real CSM
  refuses it, instead of being routed and then answered with a status naming the
  live payment. `c.relay` now reports whether it routed — the last handoff's open
  question — and when it did not, the RC01 and this institution's own copy are
  made to agree.
- **`refuseBulk` names the FILE and no transaction in it.** It quoted the first
  transaction's `PmtId`, which turned a file refused whole into a decision about a
  payment. Same shape as `Mesh.answerUnreadable`'s FF01, and a bank already skips
  a status naming no transaction.

`bank.receiveStatus`'s rejection arm lost its `GetPayment`, its `Scheme` lookup
and its two-BIC party check: a bank sent a decision about a payment it is no
party to holds no row for it, so the store refuses first. `bankOps` lost `Scheme`.

## Two decisions taken that the last handoff left open

### `ClearingCycle.SettlementID` is DELETED

The id is the settlement agent's own row number, allocated inside its own unit of
work in its own database, and what comes back is a pacs.002 quoting the CYCLE —
because the cycle is what the clearing house asked about. No message carries the
other number, so the column could only ever be empty. Gone from
`payment.ClearingCycle`, from `csm/0001_init.sql` (with its argument moved inside
the statement, as an absent column must), from `store/sqlite`, and from
`api.clearingCycleDTO`.

The link survives in the direction it can be kept: `Settlement.CycleID`, now
reachable as `Network.GetSettlementByCycleID`.

### `paymentAudit` no longer sorts by `Seq`

`Seq` was a store-GLOBAL sequence and is now each institution's own counter, so
sorting three of them together produces an order nothing in the system has. It
concatenates institution by institution; `institutionAudit` is the read every
ordering assertion has to be built on. **There is no cross-institution total
order and that is the finding** — an auditor holding four logs has exactly this
problem and answers it with the messages.

## What remains

### 1. `payment` — 47, and they are individual now

The mechanical groups are done (three new fixture helpers: `testSystem.submit`,
`mustUpdateAt`, `allBanks`; `mustGetBank` routes to the bank's own store). What
is left needs a decision each. The clusters:

- **The audit trails (5).** `TestPaymentAuditCoversTheNettingFlow`,
  `TestRejectedPaymentIsAudited`, `TestReturnedPaymentIsAudited`,
  `TestParticipantAuditPayloadDropsLiveHandles`,
  `TestEachActOfAnAdmissionLeavesItsOwnAuditEvent`. Every want-list is a single
  cross-institution sequence and has to become one per institution. Two new facts
  they must absorb: **each institution's log opens with its own
  `payment.initiated`** (`AcceptInboundTx` and `RecordRelayedTx` both append one),
  and **a bank's participant id IS its BIC**, so the two BIC-keyed admission
  events are no longer distinguishable by entity id from the bank's own — only by
  institution. `bankAudit`, `csmAudit` and `cbAudit` are already there for it.
- **`settleCycle` and the cycle state machine (4).** `SettleCycleTx` takes LEGS
  now and never reads the cycle, so `ErrCycleNotClosed` is unreachable from the
  agent: an open cycle has no positions, so `SettlementLegsOf` produces no legs
  and the answer is `ErrInvalidSettlement`. `TestStateMachineGuards/settle_before_close`
  wants the old sentinel. **Decide where "not closed" is refused now** — it is
  the clearing house's guard (it will not instruct an open cycle) and the agent
  cannot make it. `TestSettleCycleRollsBackEveryLayer` reads `settlements`
  through the clearing house.
- **The mandate fixtures (4).** `mandate not found`: a mandate is the CREDITOR
  bank's row, and these read it through another institution.
- **`message_test`'s round trips (7).** `participant not found: BANKDEFFXXX` and
  "debtor resolved to …, want it recorded rather than resolved" — the translated
  payment is initiated through the wrong institution, and the fixture asserts a
  resolution `ResolveIdentifierTx` no longer makes across banks.
- **The rest (~27)**, one at a time: `identifier already in use at this bank`,
  `Verde resolving Aurora's customer = <nil>`, `account does not belong to
  participant`, and similar — each is a fixture reaching one institution for
  another's row, but each needs reading.

### 2. `seed` — 12, and ten are one line

`listParticipants` in `seed_test.go` goes through the clearing house, which has
no banks table. Same answer as `auditReaders` and `allBanks`:
`sys.stores.Banks(ctx)`. `TestRejectedCollectionWasReversedInThePayersBank` and
`TestSeedRejectIsOneUnitOfWork` read a debtor leg off the CLEARING HOUSE's copy,
which by design has no leg columns — `meshHarness.bankPayment`'s problem, one
package over.

Also: the seed's `initiate` does **not** call `AcceptAtBank` on the submitter, so
seed copies sit at Initiated where the mesh's reach Accepted. Decide whether the
seed should play that act too. Its Phase E rejection currently works *because*
the submitter's copy is Initiated.

### 3. `api` — 7

```
GET /payments/{id}   404   the payment is at three institutions now
GET /cycles/{cid}    500   fixed for the settlement tests; check the rest
GET /reserves/{bic}  422   a founded, unadmitted bank
```
Plus `TestPaymentDTOsCarryAsset` (`POST /mandates` rejects `creditorAgent` — the
step-0 wire change) and `TestErrorMapping` panicking on the BIC `nope`.

### 4. `web/` — untouched

### 5. The documentation sweep (18e) — grown again

Everything the step-3 handoff listed, plus:

- `payment/doc.go` and `mesh/doc.go` on the three acts a bank does with what it is
  told (`AcceptAtBank` is new).
- `csm.relayRecorded`'s ordering argument replaces `relayCreditTransfer`'s, which
  said the opposite; check nothing else still cites the old order.
- The settlement id's disappearance: `README.md` and the quiz if either says a
  cycle names its settlement.
- `store/sqlite/schema/csm/0001_init.sql` gained a second absent-column argument.

## Six things not to rediscover

1. **`go test ./store/... ./ledger/ ./deposit/ ./product/ ./lending/ ./mesh/
   ./cmd/...` is the green subset, and `mesh` joined it.**
   `TestTheSeedLeavesNoPaymentHalfProcessed` passing is still the strongest
   single signal that the three-row flow works end to end.
2. **A payment's status differs per institution, legitimately.** Only the bank
   the ACCP was addressed to reaches Accepted — the submitter. The bank that
   ANSWERED the instruction goes Initiated -> Settled and is not wrong. Every
   assertion about a status has to say whose copy.
3. **`meshHarness.bankPayment` / `payment`'s `mustGetBank` are the routers.** The
   clearing house's copy has no leg columns; a bank's has no `cycle_id`. A test
   reading a leg off the clearing house fails saying the leg is missing rather
   than saying it asked the wrong institution.
4. **Account ids collide across banks and that is fine.** `dep_5` exists at every
   bank. The only way to get hurt is to name the wrong agent — which is exactly
   what `TestAnAdmittedBankCanPayAndBePaid` did.
5. **`inShape` is still the instrument, not the obstacle.** Every "no such table"
   is a crossing the plan predicted. Do not add a table to a shape to make a test
   pass.
6. **A test whose subject was designed out should be re-aimed, not deleted.**
   Three in `mesh` were: the clashing-BIC startup test (the state cannot be built,
   but the guard is real, so it is provoked at `addActors`), the bank-index clash
   (the index key became the address, so it asserts the handler), and the
   actor-name assertion (an actor is labelled by its ADDRESS, because a roster
   entry names nobody). Each kept its argument and changed what it measures.
