# Design — sub-project 8: a store per entity

Branch `spec/db-per-entity`, worktree `~/Git/cbs-db-per-entity`, based on `main`
at `a050287`.

7b made the entity boundary **observable and nameable without enforcing it**.
This sub-project enforces it: each institution gets its own store, and no
operation posts in a book that is not the acting entity's.

The specification is already in the tree as measurements. The book recorder in
`mesh/books_test.go` says which books each actor reaches;
`TestWhichBooksTheCentralBankReachesWhenItSettles` was written to fail loudly
the day the mirror leg stops being local. **That failure is the signal this
sub-project has started, not a regression.** `bankOps` / `csmOps` /
`settlementOps` are the per-entity method lists, complete and mechanically
verified.

## Decisions taken, and what they cost

Four questions were settled before any design, and each picked the more
expensive answer:

1. **Full isolation.** Every entity owns its own copy of every row it needs. A
   payment exists as separate rows in the payer's bank's store, the clearing
   house's store and the payee's bank's store, each with an independently
   evolving status. Not "books split, network rows shared".
2. **One branch, make it true then enforce it.** Every domain change lands while
   one store is still underneath, so the existing recorder measures each step;
   the split itself comes late and is then mostly mechanical.
3. **camt.053 per settlement**, as the final task.
4. **Three store shapes — the boundary in the DDL.** Not one shape opened N+2
   times. This roughly doubles the sub-project and is the reason there are six
   tasks rather than three.

## Architecture: the three store shapes

The point of putting the boundary in the DDL is that each shape states something
a shared schema hides.

**`bank.Store`**, one instance per member bank. Ledger, deposit register,
product, lending; its own payment rows carrying *its own* status; the mandates it
holds as creditor bank; a settlement-position row per cycle; audit. **No cycles,
no settlements, no participant roster.** A bank does not know the network
exists. It knows its customers, its counterparties as they arrive in messages,
and the clearing house's address.

**`csm.Store`**, the clearing house. The membership roster (BIC to routing), its
own payment rows, cycles, audit. **No ledger, no deposit register, no lending,
no product.** The clearing house has no books at all. That is true today and
invisible today — only `TestTheCSMTouchesOnlyTheNetworkBook` asserts it — and
after this it is a fact about the schema.

**`centralbank.Store`**, the settlement agent. Its own ledger — settlement
accounts, one per member per asset — its member list, settlement rows, audit.
**No deposit register, no payments, no cycles.** The central bank has no
customers, and it settles from the instruction rather than from a batch it
holds.

`ledger.NetworkBook` has nowhere to live and disappears. Each store owns exactly
one `BookID` and **refuses any other**, which turns every crossing from a silent
not-found into a loud error and keeps the book recorder meaningful rather than
vacuous.

That happens **in Task 18 and not before**. Tasks 14–17 run on one store, so
`NetworkBook` is still present in every measurement below that names it; the
expectations that still carry it are correct for the task that moves them and are
rewritten again when the split lands.

## The five crossings

The handoff lists three domain problems. There are five; two were found while
reading the code for this spec.

| crossing | today | after |
|---|---|---|
| `partyTx` (`translate.go:562`) reads the counterparty's register for a name | the submitting bank reaches the other bank's book, on the happy path, every time | counterparty **BIC, name and address** travel on `InitiatePaymentRequest` — the payer types them, as they do in life |
| `ResolveIdentifierTx` (`system.go:1944`) sweeps every member | the receiving bank reaches every bank's book | the receiving bank resolves in **its own register only**, answering AC01. The sweep was always "the honest shape at four banks" |
| `SettleCycleTx` (`system.go:795`) posts in every book | one `Store.Update` holding every member's accounts | the central bank posts **only its own netting transaction** and is final. Each bank posts its mirror leg and its creditor legs locally, on advice |
| `ReturnPaymentTx` (`system.go:1723`) posts in three books | one `Store.Update` | the central bank reverses reserves from the pacs.004; each bank posts its own compensating leg locally |
| `AddParticipantTx` (`system.go:367`) writes into `CentralBankBook` | one `Store.Update`, "so a bank can never exist without the accounts it needs" | admission is a conversation: the central bank opens the settlement account, the clearing house adds the routing entry, the bank's chart is created in its own store |

The two that were not in the handoff are **admission** and the reason the return
cannot survive isolation unchanged, below.

## Settlement, and the unreconciled position

Settlement is final at the central bank. The banks catch up afterwards, and the
interval between is the unreconciled position. That is not a modelling
convenience: in the EU it is the Settlement Finality Directive, and every real
participant reconciles after the fact rather than participating in an atomic
commit.

The flow becomes:

1. The clearing house nets the batch in its own store and sends the pacs.009.
2. The central bank checks each net payer's reserve and posts **one** netting
   transaction in its own book. It answers ACCP, or RJCT/AM04. Final either way.
3. The central bank sends each member a **camt.053** for that member's reserve
   account: the entries, and the closing balance.
4. The clearing house fans its existing per-payment pacs.002 out to each bank —
   the itemised clearing report it already sends.
5. Each bank, in its own store and its own unit of work, posts its mirror leg
   from the statement and its creditor legs from the payment advices.

A bank therefore reconciles **two independent advices from two different
institutions against one balance**. Its clearing suspense returns to zero only if
the central bank's reserve movement and the clearing house's payment list agree;
its reserve mirror equals the statement's closing balance only if it booked
correctly. Neither check needs a cross-store read, which is what makes them legal
under isolation.

### Why camt.053 and not camt.054

A camt.054 notification carries entries and no balance, so it can drive the
posting but can never detect a wrong one. A camt.053 carries `Ntry` — what to
book — and `Bal/CLBD` — whether you booked it right. Nostro reconciliation in the
field is balance-anchored for exactly this reason. One message family covers both
jobs.

### What the statement does and does not catch

| failure | caught by |
|---|---|
| the advice never arrives | **not reachable.** `mesh/doc.go` is explicit that delivery is exactly-once and in order, because the transport is a queue in one process |
| the bank's local posting fails | the per-cycle settlement-position row, whose status stays `Advised` |
| the bank posts the **wrong amount** | the statement's closing balance, and nothing else |

The third row is why this task exists. It is the shape of defect 7b kept finding
— a test that still passes with the bug reinstated — and under isolation no
entity may look in another's store to find it.

#### The second row's mechanism was wrong, and shipped before it was caught (2026-08-03)

Left as written, per this file's convention for pre-ruling wording, and corrected
here rather than in the row.

"the per-cycle settlement-position row, whose status stays `Advised`" is not what
happens. `payment.PostSettlementAdviceTx` writes the row and posts the mirror leg
in **one unit of work**, so a failed posting rolls the row back with it and a
successful one supersedes `AdviceAdvised` with `AdvicePosted` before the commit.
`store/mem` restores its pre-`fn` snapshot on error and `store/pg` issues a
`ROLLBACK`; nothing survives. No committed row says `Advised` on the settlement
path at all — the only arm that would is the zero-movement guard, which the
central bank never reaches, because it sends no statement for a position of zero.

**The code is right and the prose was wrong.** Splitting the write from the
posting so that an `Advised` row could outlive a failure would be strictly worse:
a bank's mirror leg and its record of having made it must be atomic, or a bank
can post and fail to record, and a row asserting a booking that never happened is
worse than no row.

What the second failure is actually caught by, today, is **nothing** — which
moves it into the same column as the third and makes it Task 19's too. A bank
that was told and could not book shows as a clearing suspense that has not
returned to zero with **no** advice row against the cycle, and that is
indistinguishable in the store from a bank that was never told. Telling them
apart needs the stored closing balance, which is exactly what row three says.

This wording reached Task 15b.1's brief, `payment/system.go`, `payment/types.go`,
`store/pg/schema/0001_init.sql`, `mesh/doc.go`, `mesh/bank.go`,
`mesh/centralbank.go`, `mesh/settlement_test.go`, `README.md`, the hint registry
and two quiz chapters before a review caught it. All are corrected.

### The unclaimed-balances account falls out

Once a bank posts its own creditor legs, a payee's closed account fails **one
payment at one bank** instead of the whole batch. So `CheckCreditTx` can finally
be called at settlement and the residual goes to unclaimed balances. The rulings
recorded at `system.go:875-881` and `system.go:1688-1722` stop being rulings and
become a fix. This is the answer the handoff says "belongs to the next
sub-project"; it belongs to Task 15 specifically, and it is a consequence of the
split rather than an independent feature.

## The return

The central bank has no payment rows, so it cannot look up whose settlement
accounts to move. `iso20022/pacs004.go:163` records that **`OrgnlTxRef` is
deliberately absent**, which is exactly where `DbtrAgt` and `CdtrAgt` would live,
and `mesh/doc.go` states the consequence without connecting it to the split: *"a
pacs.004 names no parties at all."* `OrgnlTxRef` must be implemented on pacs.004
or the return cannot survive isolation.

### A failed clawback is direction-dependent

`ReturnPaymentTx` always claws back from `p.Creditor`. Whether that is the
returning bank's own customer flips with the scheme's direction, and so does the
correct behaviour.

**Push (SCT).** The returning bank is the payee's bank, which is also the
creditor's bank — the same institution. A beneficiary bank Returns within ten
banking days; where the originator bank asks later it is a *Recall*, answered by
a Response to Recall that may be negative (`CUST`, `INSU`, `ARDT`, `AC04`,
`NOAS`, `LEGL`). No bank force-takes money from a customer who has spent it. So
**the returning bank checks its own customer before it composes the pacs.004 and
refuses locally if it cannot fund it** — the shape `MD01` already has here,
"refused there and then, at the payee's own bank, as an error to the caller and
not as a message". No new account, and no message is sent.

**Pull (SDD refund).** The returning bank is the *payer's* bank; the clawback
lands on the biller at a different institution. The payer's eight-week refund
right is unconditional, so the creditor's bank must honour it whether or not the
creditor can pay, and bears the credit risk. That is why creditor banks vet their
creditors and can demand collateral or an indemnity. Here the bank **does** force
the posting: the creditor's account goes overdrawn, and a closed account or a
shortfall lands in a new **`Returns Receivable`** asset account.

Today `ReturnPaymentTx` debits `creditorGL` with no check in either direction, so
the push-side refusal is new behaviour and is falsifiable: a settled SCT whose
payee has spent the money must stop producing a pacs.004.

### Task 16's shape, settled 2026-08-03

The section above states the rule and leaves the mechanism open. The mechanism,
decided before any code and recorded here so that the plan does not get to invent
it:

**Two legs, and which bank owns each never changes.** The **clawback** is always
at the **creditor's** bank, out of `Payment.CreditorLegAccount`. The **refund** is
always at the **debtor's** bank, into `Payment.Debtor.Account`. What flips with
the scheme's direction is only which of the two the *returning* bank is holding:

| | the returning bank posts, before it sends | the other bank posts, after finality |
|---|---|---|
| push (SCT) | the clawback — **refusable**, so no pacs.004 ever exists | the refund — always postable |
| pull (SDD refund) | the refund — always postable | the clawback — **forced**; a shortfall lands in `Returns Receivable` |

So the direction-dependence above is one rule rather than two cases: **a bank can
refuse a leg only if it posts it before it sends.** The section above says the
returning bank *checks* before it composes the message; this sharpens that to
*posts*, and the sharpening is the whole of why the refusal binds. A check that
is not a posting can be outrun by the customer between the check and the credit,
which under a split settlement is a window that opens after finality and cannot
be closed by refusing. `Returns Receivable` is needed
exactly on the pull side, and the reason is structural rather than stipulated —
the bank that must force the posting is the one that first hears about the return
after it is already final.

**The order on the wire.** The returning bank posts its own leg against its
clearing suspense and refuses there if it cannot; sends the pacs.004, now carrying
`OrgnlTxRef`; the clearing house relays it to the settlement agent. The central
bank reads the parties **off the message**, reverses reserves in its own book and
is final either way. It sends a camt.053 to **both** banks — before the answer,
for the reason `mesh.centralBank.advise` already records — and answers the
clearing house, which forwards the status to the returning bank and relays the
pacs.004 to the other bank. Each bank books its reserve mirror from the statement
and its customer leg from the payment message, exactly as Task 15 has it, so
suspense returning to zero is the reconciliation on this path too.

**Which act sets `Returned`** is the *second* leg — the other bank's — in both
directions. That is `PostCreditorLegTx`'s shape reused: the final customer leg is
what the status is about, and one row takes one transition.

**A rejected return must unwind.** The returning bank has already posted when the
answer arrives, so `bank.receiveReturnStatus` stops being a log line: an RJCT
reverses the local leg. This is the price of the binding refusal and is worth it —
the alternative moves a customer's money on a check that finality can outrun.

**New in the domain.** `ParticipantAccounts.ReturnsReceivable`, a `ledger.Asset`
per asset — the bank's claim on a biller it paid out for. `SettlementAdvice`
generalises to a key of `(Book, Reference, Asset)`, `Reference` being a cycle id
or a payment id: a return's statement names a payment and there is otherwise no
row for a bank to record having booked it, and Task 19 should reconcile one shape
rather than two. **No discriminator beside it** — ids are unique across the store,
so a `kind` column would be a field nothing reads, which is the defect class this
repository keeps finding.

The settlement agent's **dedupe for a redelivered pacs.004** needs no row either,
and that is worth stating because it looks like it should. It can no longer lean
on `ErrInvalidStateTransition` from a payment row it will not have, but
`ledger.Book.PostTransactionTx` already refuses a repeated idempotency key with
`ErrDuplicateIdempotencyKey`, and the reversal posts under one derived from the
payment. The sentinel is renamed at the domain boundary and dead-lettered in the
mesh exactly as the state-transition refusal is today.

**The measurement** is the table's — `allBooks()` → `[CentralBankBook, Network]` —
plus a counterpart for the banks, as Task 15 needed. Task 15's lesson applies: the
counterpart is the one whose *name* will claim the opposite of its measurement if
it is not rewritten.

## Rulings this sub-project reverses

Each must be written down as a reversal. A reversed ruling that looks like an
oversight is worse than the original ruling.

1. **The camt family is "deliberately absent"** (`iso20022/doc.go:88`). Reversed
   for camt.053. The ruling was made when no institution in this system needed to
   be *told* about a movement on an account it does not hold; the split creates
   the first one that does.
2. **`OrgnlTxRef` is "deliberately absent"** (`pacs004.go:163`). Reversed **on
   pacs.004 only**. The ruling is recorded in full on pacs.002's
   `PaymentTransactionStatus` and merely referenced from pacs.004, so reversing
   it for one message and not the other is deliberate and must say so.
3. **"There is one migration"** (`CLAUDE.md`). Becomes one migration *per shape*,
   three files. The original rationale — no database is deployed — survives; the
   sentence does not, and `CLAUDE.md` is part of the change.

## Tasks

Six. Larger than 7b's four, which is stated here rather than discovered at task
four.

Tasks 14–17 land **while one store is still underneath**, so every domain change
is measured by the existing recorder before the safety net is removed.

| # | task | the measurement it moves |
|---|---|---|
| 14 | **The message carries the parties.** Counterparty BIC/name/address on `InitiatePaymentRequest`; the receiving bank resolves in its own register only | `TestWhichBooksEachBankActuallyReaches`: the **submitting** bank only. `TestWhichBooksEachBankActuallyReaches`: payer's bank `[debtor, creditor, Network]` → `[debtor, Network]`; `TestWhichBooksEachBankReachesInAPull`: submitting payee's bank → `[creditor, Network]`. The **receiver's** set does not move here — see below |
| 15 | **Settlement becomes a conversation.** Central bank posts only its own netting transaction; banks post mirror and creditor legs locally on advice; settlement-position row; unclaimed balances; `CheckCreditTx` at settlement | `TestWhichBooksTheCentralBankReachesWhenItSettles`: `allBooks()` → `[CentralBankBook, Network]`. **The loud failure the handoff predicted** |
| 16 | **Return becomes a conversation.** `OrgnlTxRef` on pacs.004; central bank reverses reserves from the message; banks post their own compensating legs; the direction-dependent clawback rule and `Returns Receivable` | `TestWhichBooksAReturnReaches`: `allBooks()` → `[CentralBankBook, Network]` |
| 17 | **Admission becomes a conversation.** Central bank opens the settlement account, clearing house adds the routing entry, the bank's chart is created locally | `TestWritingAParticipantTouchesNoBankBook` gains a counterpart; the orphan-participant defect carried into `main` must be confronted rather than carried again |
| 18 | **Split the stores.** Three shapes × `mem` × `pg`; three storetest suites; N+2 instances; per-entity payment rows; the roster moves to the clearing house; seed, `api` and `cmd/server` rewiring; foreign-`BookID` refusal | the recorder stops being the only enforcement; a crossing becomes an error |
| 19 | **camt.053 and reconciliation.** The statement, the closing-balance check, break detection | breaks become detectable in-system rather than only by the test suite |

Task 18 is the largest single item. If it runs long it splits into **18a**
(shapes and storetest) and **18b** (wiring and per-entity rows).

### camt.053 moved from Task 19 to Task 15, and why (2026-08-02)

Task 15's mirror leg needs an advice from the CENTRAL BANK, and the spec's own
settlement flow says so: the bank posts "its mirror leg from the statement and its
creditor legs from the payment advices". That split is load-bearing rather than
incidental — "suspense returns to zero only if the central bank's reserve movement
and the clearing house's payment list agree" is a check between two SENDERS, and
if both legs came from the clearing house there would be nothing to reconcile.

So the message family landed as Task 15a and the conversation as 15b. Task 19
keeps the reconciliation it is named for: the closing-balance check against
Bal/CLBD, break detection, and the surface that makes a break visible to an
operator rather than only to the test suite. `SettlementAdvice.ClosingBalance` is
already stored and already unread; Task 19 is what reads it.

### The name crossing has a second door, and Task 14 does not close it

Found during Task 14.5's review, and recorded here rather than left to be
rediscovered.

Task 14 closes the counterparty name read on the **message-building** path:
`partyTx` is gone and `partiesOf` reads nothing. But `GET /directory` still
resolves an IBAN and returns the account holder's **name**, by calling
`p.Deposit.GetAccount(...)` on the resolved participant and reading `acct.Name`
(`api/handlers_directory.go:56-64`). That is the payer's bank reading the payee's
bank's register for the payee's name, over HTTP instead of through a message. The
web send form calls it and displays the result.

It is consistent with deferring the sweep to Task 18 — the endpoint is
`ResolveIdentifier` with a name join on top, and both die together — but the claim
"a payer's bank can no longer learn the payee's name" is true of the mesh and
false of the HTTP surface until then. Removing `Name` and `Asset` from
`directoryEntryDTO` belongs with the sweep's removal in Task 18.

The consequence for the teaching layers is immediate and is Task 14.6's: the send
form renders the resolved payee name one line above the fields that ask the payer
to type it, so the UI currently demonstrates the negation of the fact the branch
exists to establish. Routing needs the bank, not the name, so the minimal honest
fix is to render the bank alone.

### The receiving bank's measurement belongs to Task 18, not Task 14

Found while writing Task 14's plan, and corrected here rather than left for the
implementer to hit.

Task 14 changes the receiving bank's *behaviour* — it resolves its own customer
and records the counterparty from what the message says, retiring an AC01 that was
never this bank's to make, since a debtor IBAN nobody holds is a statement about
the **sending** bank's customer. That change is real and testable.

But it does not move the receiver's book set. Resolution runs through
`ResolveIdentifierTx`, which sweeps every member's register, and narrowing it to
"this bank's register" requires the bank to know which register is its own.
`payment.Network` has no identity to answer that with, and supplying one is Task
18's design. Pre-empting it in Task 14 would be a guess of exactly the kind
`ops.go`'s own doc warns against — an interface written ahead of its callers that
then looks authoritative.

So `TestWhichBooksEachBankActuallyReaches`'s receiver assertion moves from
`bankBooks()` to `[creditor]` in **Task 18**, with its pull mirror.

## What Task 18 changes that is not a store

- **Per-entity payment rows.** `bank.receiveStatus` today "deliberately reads the
  payment row and trusts it over the message it just received"
  (`mesh/doc.go:9-13`), which the package doc names as the shared store showing
  through. Under isolation the row it reads is its own, so what it trusts is its
  own prior belief rather than another entity's. The behaviour is kept and its
  justification changes; the doc must change with it.
- **The roster.** `ListParticipants` is the clearing house's, and no bank calls
  it. A bank learns counterparties from messages.
- **`InitiatePaymentRequest` loses the debtor participant.** A bank's own
  customers are its own, so the debtor participant is always the receiving
  entity and naming it is redundant.

## Testing

- **`storetest` becomes three conformance suites**, one per shape. CLAUDE.md's
  rule holds three times over: `store/pg` must never accept or refuse a write
  differently from `store/mem`.
- **Postgres: one schema per entity in one database**, so `TEST_DATABASE_URL`
  keeps working unchanged and `make test-pg` stays as broken as it already is
  (it uses Docker, which does not work on this machine).
- **The book recorder survives and gets stronger.** With foreign `BookID`s
  refused, a crossing is an error rather than a silent miss.
- **A test-level reconciliation harness** is the successor instrument: it opens
  all N+2 stores and asserts the network balances in aggregate — the one thing no
  entity is permitted to do, and the direct analogue of what the recorder was to
  7b.
- **Every measurement move is watched failing first.** 7b's discipline, restated
  because it is the one that found everything: for every behavioural claim, break
  the code, watch that exact test fail, restore. A test whose failure nobody has
  observed is not evidence.

## Documentation layers

Per CLAUDE.md, domain content is duplicated by design and a correction in one
layer must be checked in the others. This sub-project adds domain content that
none of them currently carries:

- The clearing house has no ledger; the central bank has no customers; a bank has
  no cycles.
- Settlement is final at the central bank and the banks reconcile afterwards.
- The creditor's bank carries the refund risk on a direct debit, which is why it
  vets its creditors.

That means `README.md` (authoritative), `web/src/components/hint-content.ts`,
the quiz chapters — 9 (clearing and settlement), 11 (payment schemes), 12 (SEPA),
15 and 16 (persistence) — and the schema files, which are now three.

## Verification

Unchanged from the handoff, and all of it must stay green:

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./...                                                            # store/mem
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

A `[[wiki-link]]` to a key missing from `hint-content.ts` throws at runtime and
takes every route down while `next build` stays green. Run `npm run test`, and
load a page.

## What this sub-project does not do

- **No golden file is schema-validated**, here or anywhere. `iso20022/testdata/xsd/`
  is absent for licensing reasons and `TestGoldenFilesValidateAgainstTheSchema`
  skips every subtest. The camt.053 added in Task 19 inherits that gap. A skip is
  not a pass.
- **No periodic statement.** camt.053 is sent per settlement, not end-of-day, so
  there is no mechanism that would catch an advice which never arrived. That case
  is unreachable in this transport, and if the mesh ever gained a lossy transport
  it would become reachable and invisible.
- **No two-phase commit anywhere.** It was considered and rejected: it would
  recreate the cross-entity atomicity this sub-project exists to remove, and
  real settlement does not work that way.
