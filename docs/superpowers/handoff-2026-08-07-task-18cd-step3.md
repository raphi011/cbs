# Handoff — Task 18c/18d, step 3 landed, `spec/sqlite-only-store`

**Everything is committed, `go build ./...`, `gofmt` and `go vet ./...` are all
clean.** One commit sits on top of `f144c51`:

```
3722be4 step 3: every institution keeps its own payment row
```

This supersedes `handoff-2026-08-06-task-18cd-steps123.md` as the entry point.
Its section "Six things not to rediscover" still stands in full and is not
repeated except where step 3 changed one. Its remaining-work list is what this
document replaces.

## Where the tree is, measured

```
go build ./...    clean
gofmt             clean
go vet ./...      clean          <- this was two failing packages
```

| package | after step 2 | now |
|---|---|---|
| `ledger`, `deposit`, `product`, `lending`, `interest`, `iso20022` | green | **green** |
| `store/sqlite` | green | **green** |
| `cmd/server` | 1 | **green** |
| `mesh` | does not build (71) | **29** |
| `payment` | does not build | **10** |
| `api` | 18 | **7** |
| `seed` | 13 | **12** |

**111 failures, all one cause, become 58 across four packages.** They are no
longer one cause; the grouping is in "What remains" below and four of them are
one REAL DEFECT rather than fixture drift.

## What landed

### The payment id carries the bank that minted it

`pay_AURODEFFXXX_29`. This was not in the plan and it had to come first.

The counter is per book and per book only, so AURODEFFXXX minted `pay_29` and so
did every other bank — harmless while one payment was one row in one store,
because there was one allocator. Each institution now writes its own row under
the id the message carries, so the same string has to name the same payment in
N+2 databases that share no sequence. The clearing house writes both banks'
`pay_29` into one keyspace, and a bank writes its counterparty's id beside its
own; the second write would have been a silent UPDATE of somebody else's
payment.

`SubmitPaymentTx` puts its own address in the prefix. It is the one value that
is unique network-wide by construction — a bank's BIC is its id and the name of
its database — and it is what SEPA asks of an originating bank's reference. The
counter is untouched, so the number still doubles as that bank's creation order
and the serialization argument above the allocation still holds.

**UUIDs were considered and rejected**, and the reasons are worth not
re-litigating: the seed stops being reproducible (`TestDeterministicIDs`; v4 is
random and v7 is time-based on a frozen clock), the `NextID` write that makes
this transaction the database's writer *before* the duplicate-`EndToEndID` read
disappears, and the payment becomes the one id in the system you cannot read.
The one real argument for them — `pay_AURODEFFXXX_29` leaks the minter's
identity and row count to every counterparty — was judged a feature in a
teaching system.

### Three institutions, three rows

- **`AcceptInboundTx(ctx, tx, id, req)`** takes the REQUEST the receive handlers
  used to build and discard, and builds the receiving bank's row from it. Both
  handlers' docs said that gap was "sub-project 8's whole subject"; this is it.
- **`RecordRelayedTx`** is the clearing house recording what it routes, called
  from `relayCreditTransfer`/`relayDirectDebit`. `RecordRelayed(ctx, id, req)` is
  the same act without a message, for the seed.
- The witness for a redelivery is now **the row itself** in both places, which
  is strictly stronger than the `DebtorLegTx` it replaces: that one covered only
  the pull arm, so a redelivered pacs.008 re-ran the payee's account check and
  could answer ACCP and then AC01 about one message.

`resolveOwnPartyTx` is **deleted**, and its own doc said when: there is no
foreign ref to correct because there is no foreign row.

### Four acts that are one institution's half of what used to be one write

| act | what it replaces |
|---|---|
| `RejectAtBankTx` | `bank.receiveStatus` reading the row, checking it said Rejected, then calling `ReverseDebtorLeg`. The guard was the whole safety argument and it read a row the CLEARING HOUSE had written. |
| `SettleAtBankTx` | `PostCreditorLegTx`. Being the payee's bank was a PRECONDITION; it is a BRANCH now, because both banks hold a copy and only one holds a leg. The rename follows. |
| `SettleAtCSMTx` | `Settled`, which the payee's BANK used to write on the shared row. Also marks the clearing house's own cycle `CycleSettled`. |
| `CompleteReturnTx` | `Returned`, which the second customer leg used to write on the shared row. |

### `transition` is a method, and there are two tables

Accepted and Cleared are facts about a CYCLE. A bank has no cycles and its shape
has no `cycle_id` column, so a bank's table is `Initiated -> {Rejected,
Settled}` and `Settled -> {Returned}`.

**This is the part that is WRONG, and the tests found it.** See the defect
below.

The half of the argument that survives: the clearing house's machine got
STRICTER rather than looser, because nothing else in the network has an edge to
Accepted or Cleared any more.

### Two crossings closed on the way, both hidden behind the 111

- **`SettleCycleTx` read the clearing house's cycle row** for its positions, its
  asset and its "has the cut-off been reached" guard — in a database with no
  `cycles` table. It takes the LEGS now, the same shape `SettleReturnTx` already
  had, and refuses a redelivery out of its own settlement register
  (`ErrCycleAlreadySettled`, new, plus `GetSettlementByCycle` on `Tx`).
  `positionsIn` reads an instruction back into positions and derives the
  settlement agent as the address every leg has in common — which is well
  defined because a cycle's positions sum to zero.
- **`SettleReturnTx` read two bank rows** to learn back two BICs the instruction
  already carried. Neither read established membership; `settlementAccountTx`
  does that, out of the agent's own register.

`csm.settlementLegs` moved to `payment.SettlementLegsOf` because the seed needs
to produce exactly what the pacs.009 would carry.

### One thing is genuinely lost, and it is recorded where it happens

A clearing house **no longer knows what the settlement agent numbered its
settlement**. The id is the agent's own row number, allocated inside its own
unit of work, and the ACSC quotes the CYCLE because that is what the clearing
house asked about. `api`'s cycle DTO omits the field.
`TestARefusedSettlementIsRecoverableOverHTTP` and mesh's
`TestARefusedSettlementCanBeInstructedAgain` both assert the link and are the
two places to decide whether to put the id on the wire or to write the loss
down.

## THE DEFECT — do this first

**A bank's copy never records Accepted, so it cannot tell "not yet decided" from
"already in a cycle", and it reverses a live debit.**

Four assertions in `mesh/sct_test.go` are one cause and it is a money path:

```
sct_test.go:280  Drain was clean; the payer's bank reversed the debit of a
                 payment that is still Accepted
sct_test.go:357  dead letter is not the payer's bank refusing to reverse an
                 accepted payment
sct_test.go:362  payer's clearing suspense = 0, want 250000
sct_test.go:444  Drain = <nil>, want the payer's bank refusing to reverse an
                 accepted payment
```

The mechanism, end to end. `bank.receiveStatus`'s ACCP arm says an acceptance
"needs nothing from it" — true while the clearing house wrote Accepted onto the
row both banks were reading. It writes on its own row now, so the payer's bank's
copy stays Initiated all the way to settlement. `Network.transition`'s bank table
was then written to match that (`Initiated -> {Rejected, Settled}`), which makes
`RejectAtBankTx` accept a rejection of a payment the clearing house has already
taken into a cycle — and reverse the debit. `TestACreditTransferForABankTheMeshCannotRouteToIsRC01`
replays the pacs.008 with a mangled `CdtrAgt` and the suspense comes back 0.

**The fix is to give a bank's copy the Accepted state after all**, which reverses
the argument now written above `Network.transition`. That means:

1. `bank.receiveStatus`'s ACCP arm advances this bank's own copy. A new act on
   the same footing as `RejectAtBank`/`SettleAtBank` — call it `AcceptAtBank` —
   rather than a bare status write, so the guard lives with the transition.
2. **`csm.tell` and `csm.tellSettled` have to reach both copies, and today they
   reach one.** This is the second half of the same defect and it is not
   currently asserted anywhere:
   - `tellSettled`'s recipients are `[submitter]` plus the creditor if different.
     On a PULL the submitter IS the creditor's bank, so the DEBTOR's bank is
     never sent an ACSC — and it holds a copy with a posted debtor leg. Its row
     stays Initiated for ever. `TestAReturnedCollectionIsSentByThePayersBank`,
     `TestARefusedReturnUnwindsTheReturningBanksLeg` and
     `TestAReturnRetriedAfterAnUnwindRepaysThePayer` all fail with
     `cannot return ..., which this network records as Initiated`, which is this.
   - `tell`'s rejection arm sends to the payer's bank only when `payerAccepted &&
     debtorAgent != submitter`. On a PUSH the payee's bank that accepted and was
     then refused by the clearing house (TM01) is never told, and its copy stays
     Initiated. The existing `payerAccepted` conjunct generalises: tell the OTHER
     bank whenever the ANSWERING bank said ACCP, either direction — a bank that
     answered RJCT rolled its row back and has nothing to tell.

Do not weaken a guard to make these pass. The refusal the tests want —
`records as Accepted` — is the correct one.

## What remains, in the order it should happen

### 1. The defect above (4 mesh assertions + 3 return tests)

### 2. `mesh/books_test.go`'s want-lists — now measurable for the first time

Step 2 set them to one book each and could not verify them. They can be measured
now and **six of them are wrong in a consistent direction**: the recorder says a
bank touched only its own book and the want-list still carries `clearing-house`
beside it.

```
books_test.go:867   the payer's bank                    [AURODEFFXXX]  want [AURODEFFXXX clearing-house]
books_test.go:1004  the payee's bank, submitting a pull [VERDITMMXXX]  want [VERDITMMXXX clearing-house]
books_test.go:1327  the central bank, settling         [central-bank]  want [central-bank clearing-house]
books_test.go:1409  the payee's bank, booking          [VERDITMMXXX]  want [VERDITMMXXX clearing-house]
books_test.go:1564  the payer's bank, refunding        [AURODEFFXXX]  want [AURODEFFXXX clearing-house]
books_test.go:1501  the clearing house, on a return    [clearing-house] want []
return_test.go:36   the clearing house, carrying one   [clearing-house] want []
```

The first five lose `clearing-house`: an id allocation and an audit event are
drawn from the store the act is about to write, and that is the acting
institution's own now. The last two GAIN it, and that is new behaviour rather
than a correction — the clearing house writes its own payment row when it relays
(`RecordRelayedTx`) and marks it Returned when it is told (`CompleteReturnTx`),
where before it touched nothing on a return at all. Rewrite the prose to say so.

This is the "one measured pass" the previous handoff deferred here.

### 3. The audit sequences — one institution's log each now

`payment`'s 10, minus two reason-table entries:

- **`TestReasonTableCoversEverySentinel` / `TestReasonTableNamesMatchTheirValues`.**
  Two new sentinels, `ErrCycleAlreadySettled` and `ErrInvalidSettlement`, need
  entries. The first is the empty code with `ErrCycleNotClosed`'s argument — it
  describes this system's state, and `mesh`'s `centralBank.receiveSettlement`
  already dead-letters it. The second is a judgement about the INSTRUCTION, so
  `MS03` through the fallback is right, which is `cycleOf`'s shape.
- **Four audit tests** fail with `this is clearing-house's store, and
  "BANKDEFFXXX" is somebody else's book`. That is `auditReaders` in
  `payment/audit_test.go` — the fixture item the previous handoff already named,
  which should be `sys.stores.Banks(ctx)`.
- **`TestParticipantAuditPayloadDropsLiveHandles` / `TestEachActOfAnAdmissionLeavesItsOwnAuditEvent`**
  see the admission's four events in a different ORDER, because the reader now
  walks institutions rather than one log. Same fixture.
- **`TestARegisteredSchemeReachesEveryInstitutionsNetwork`** panics on
  `bank_1`, which is not a BIC. Fixture.

And the new fact these have to absorb: **each institution's log opens with its
own `payment.initiated`.** `AcceptInboundTx` and `RecordRelayedTx` both append
one. The old argument — "the payment's lifecycle has two facts, not three" — was
about ONE trail and there is no such trail; a bank whose log recorded a debtor
leg it posted but never recorded taking the payment on would start in the middle.

### 4. `api` — four routes still read through whichever institution bound

```
GET /payments/{id}   404   the payment is at three institutions now
GET /cycles/{cid}    500   centralbank shape has no cycles table
GET /settlements     500   csm shape has no settlements table
GET /reserves/{bic}  422   a founded, unadmitted bank
```

Plus `TestPaymentDTOsCarryAsset` (`POST /mandates` rejects `creditorAgent` — the
step-0 wire change), and `TestErrorMapping` panicking on the BIC `nope`, which is
the forty-sixth failure the previous handoff already named.

### 5. The seed and mesh fixtures

- Ten `seed` failures are one line: `listParticipants` in `seed_test.go` goes
  through the clearing house, which has no banks table. Same answer as
  `auditReaders`.
- `TestRejectedCollectionWasReversedInThePayersBank` and
  `TestSeedRejectIsOneUnitOfWork` read a debtor leg off the CLEARING HOUSE's
  copy, which by design has no leg columns. So do mesh's three `sdd_test.go`
  failures and `settlement_test.go:286` / `statement_test.go:117,298`
  (`GetSettlementAdvice` through `h.net`). `meshHarness.payment` and `h.net` are
  the clearing house; a test about a bank's own row needs `h.bank(bic)`.
- `TestOnlyThePayeesBankPaysThePayee` asserts `ErrNotThisBanksPayment` for the
  payer's bank. That precondition deliberately became a branch; rewrite the
  claim rather than the code.
- `TestAnAdmittedBankCanPayAndBePaid` swaps in the joiner's account and leaves
  `DebtorDetails.Agent` pointing at the harness's own bank, so `Submit` routes to
  the wrong bank — which finds ITS `dep_5` and refuses the quoted IBAN. A
  one-line fixture fix, and see the note below on why it is not the id defect
  again.
- `TestStartGivesAFoundedBankNoActor`, `TestStartRefusesTwoParticipantsWithOneBIC`,
  `TestStartGivesEveryParticipantAnActor`, `TestAddingABankOnAnotherBanksBICIsRefusedAndChangesNothing`,
  `TestAFoundedBankCanNeitherPayNorBePaid` — not read in detail. `FoundBank`
  through the clearing house's network is now `ErrNotThisInstitutionsAct`, which
  looks like the shape of at least two of them.

### 6. `web/` — untouched, and the previous handoff's list is unchanged

### 7. The documentation sweep (18e) — grown again

Everything the previous handoff listed, plus:

- `mesh/doc.go:9-13` still says the actors share one store and a bank "CAN look
  the answer up". Now emphatically false — this is the task that made it so.
- `payment/doc.go:46-50` describes `AcceptInboundTx` and `AcceptAtCSMTx` as
  halves of one payment's life.
- `README.md`, `hint-content.ts` and quiz chapters 9/11/12/15/16 have never been
  told a payment is three rows.
- `store/sqlite/schema/csm/0001_init.sql`'s payments-table comment says "one
  payment is now three of these in three databases" — which is TRUE as of this
  commit and was aspirational when written. Worth a pass to see what else in the
  three schema files is now describing the present rather than the plan.

## Five things not to rediscover

1. **`go test ./store/... ./ledger/ ./deposit/ ./product/ ./lending/ ./cmd/...`
   is the green subset, and `cmd/server` joined it.**
   `TestTheSeedLeavesNoPaymentHalfProcessed` passing is the strongest single
   signal that the three-row flow works end to end; if it breaks, something in
   the initiation/settlement chain is wrong rather than a fixture.
2. **A bank's copy of a payment has no `cycle_id` and no `CycleID`; the clearing
   house's has no legs.** Most of the remaining fixture failures are a test
   reading the wrong institution's copy for a column that institution does not
   have. Read the shape's schema comment before assuming a defect.
3. **Account ids collide across banks, and that is fine.** `dep_5` exists at
   every bank. It is only ever a key in its own database and the payment names
   the agent — so the only way to get hurt is to name the wrong agent, which is
   exactly what `TestAnAdmittedBankCanPayAndBePaid` does. It is NOT the payment-id
   problem returning.
4. **The clearing house records AFTER it relays, and the doc overstates why.**
   The argument is that a message it cannot route never becomes a payment — but
   `relay` swallows `ErrUnknownBIC` into an RC01 answer and returns `nil`, so the
   record IS attempted on that path. Either `relay` reports whether it routed, or
   the doc is corrected. The ordering itself is safe for the reason given (an
   actor handles its inbox serially, `Mesh.run`), and that part is sound.
5. **`inShape` is still the instrument, not the obstacle.** Every "no such table"
   in the list above is a crossing the plan predicted. Do not add a table to a
   shape to make a test pass.
