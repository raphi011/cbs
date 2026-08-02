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
| 14 | **The message carries the parties.** Counterparty BIC/name/address on `InitiatePaymentRequest`; the receiving bank resolves in its own register only | `TestWhichBooksEachBankActuallyReaches`: payer's bank `[debtor, creditor, Network]` → `[debtor, Network]`; payee's bank `bankBooks()` → `[creditor]`. And `TestWhichBooksEachBankReachesInAPull`: the submitting payee's bank `[debtor, creditor, Network]` → `[creditor, Network]`; the answering payer's bank `bankBooks()` → `[debtor]` |
| 15 | **Settlement becomes a conversation.** Central bank posts only its own netting transaction; banks post mirror and creditor legs locally on advice; settlement-position row; unclaimed balances; `CheckCreditTx` at settlement | `TestWhichBooksTheCentralBankReachesWhenItSettles`: `allBooks()` → `[CentralBankBook, Network]`. **The loud failure the handoff predicted** |
| 16 | **Return becomes a conversation.** `OrgnlTxRef` on pacs.004; central bank reverses reserves from the message; banks post their own compensating legs; the direction-dependent clawback rule and `Returns Receivable` | `TestWhichBooksAReturnReaches`: `allBooks()` → `[CentralBankBook, Network]` |
| 17 | **Admission becomes a conversation.** Central bank opens the settlement account, clearing house adds the routing entry, the bank's chart is created locally | `TestWritingAParticipantTouchesNoBankBook` gains a counterpart; the orphan-participant defect carried into `main` must be confronted rather than carried again |
| 18 | **Split the stores.** Three shapes × `mem` × `pg`; three storetest suites; N+2 instances; per-entity payment rows; the roster moves to the clearing house; seed, `api` and `cmd/server` rewiring; foreign-`BookID` refusal | the recorder stops being the only enforcement; a crossing becomes an error |
| 19 | **camt.053 and reconciliation.** The statement, the closing-balance check, break detection | breaks become detectable in-system rather than only by the test suite |

Task 18 is the largest single item. If it runs long it splits into **18a**
(shapes and storetest) and **18b** (wiring and per-entity rows).

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
