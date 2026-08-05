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

## The crossings

The handoff lists three domain problems. Two more were found while reading the
code for this spec, and a sixth while designing Task 17.

This heading counted them until 2026-08-05. It stopped, for the reason Task 16's
documentation sweep paid to learn: **a count goes stale and a description of what
happens does not.** The table below is the list; the heading is not a second one.

The "today" column is as this spec was written; the state column records what has
since happened, per crossing, with the task that did it.

| crossing | today | after | state |
|---|---|---|---|
| `partyTx` (`translate.go:562`) reads the counterparty's register for a name | the submitting bank reaches the other bank's book, on the happy path, every time | counterparty **BIC, name and address** travel on `InitiatePaymentRequest` — the payer types them, as they do in life | **closed — Task 14.** `partyTx` no longer exists; the agent is derived from the roster rather than asserted, which the "after" column had not anticipated |
| `ResolveIdentifierTx` (`system.go:1944`) sweeps every member | the receiving bank reaches every bank's book | the receiving bank resolves in **its own register only**, answering AC01. The sweep was always "the honest shape at four banks" | **open — Task 18** |
| `SettleCycleTx` (`system.go:795`) posts in every book | one `Store.Update` holding every member's accounts | the central bank posts **only its own netting transaction** and is final. Each bank posts its mirror leg and its creditor legs locally, on advice | **closed — Task 15.** As designed, plus `PostSettlementAdviceTx` and `PostCreditorLegTx` as the members' own acts |
| `ReturnPaymentTx` (`system.go:1723`) posts in three books | one `Store.Update` | the central bank reverses reserves from the pacs.004; each bank posts its own compensating leg locally | **closed — Task 16.** `ReturnPaymentTx` was deleted; it is `SettleReturnTx` (reserves only, reading no payment row), `PostReturnLegTx` (each bank's own customer leg) and `ReverseReturnLegTx`. The clearing house **holds** the pacs.004 until the return is final, which the design had not anticipated |
| `AddParticipantTx` (`system.go:367`) writes into `CentralBankBook` | one `Store.Update`, "so a bank can never exist without the accounts it needs" | admission is a conversation: the central bank opens the settlement account, the clearing house adds the routing entry, the bank's chart is created in its own store | **open — Task 17** |
| `DepositTx` (`system.go:578`) posts in the bank's book **and** `CentralBankBook` | one `Store.Update`, and the only way reserves are ever funded in this system | the model is wrong before it is split: cash paid in does not move central-bank reserves. **Vault cash** in the bank's own book, and a separate **lodgement** — a real conversation, camt.050 in TARGET2 — that moves them | **open.** Re-routed but not closed at Task 17; it **must** close at Task 18, which cannot build with it. Found 2026-08-05 |

The two that were not in the handoff are **admission** and the reason the return
cannot survive isolation unchanged, below.

### The sixth was invisible to every instrument this sub-project has (2026-08-05)

`DepositTx` has posted in two books since before this spec, and neither the
recorder nor `ops.go` could ever have said so. The recorder watches mesh actors,
and funding a reserve does not arrive in an inbox — `api.handleFundDeposit` and
`seed` call the domain directly, exactly as admission does. `ops.go` narrows what
a *handler* may name, and this has no handler.

That is worth more than the crossing itself: **both instruments are blind to any
operation that never becomes a message**, and the two open crossings left after
Task 17 are both of that kind. Task 18's reconciliation harness is the successor
instrument for the same reason.

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

> Written before Task 16 and kept as the design record. Every present-tense
> claim about `ReturnPaymentTx` below describes the code as it stood then; that
> function was deleted at Task 16e and the crossing is marked closed in the table
> above. What was decided here is what shipped — see "Task 16's shape" below,
> which is the part that became the plan.

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

## Admission

`AddParticipantTx` (`payment/system.go:405`) is one unit of work that writes
three institutions' things: the bank's chart of accounts and its default deposit
product in the bank's own book, one settlement account per asset in
`CentralBankBook`, and the roster row under `NetworkBook`. Its own doc gives the
reason — *"so a bank can never exist without the accounts it needs"* — and that
reason is precisely what isolation takes away. A bank that cannot exist without
another institution's accounts is a bank that cannot be admitted across a store
boundary.

**`Participant` is itself a cross-entity aggregate, and that is the deeper
problem.** `ParticipantAccounts.Settlement` is the CENTRAL BANK's account id,
carried on the roster row; `SettleCycleTx`, `SettleReturnTx`, `ReserveBalance`
and `DepositTx` all read it there. The settlement agent keeps no record of its
own members — it borrows the clearing house's. No book is touched by that read,
so the recorder cannot see it, and it is the thing that would leave Task 18 with
a central bank unable to settle.

**The orphan-participant defect is an ordering, not a gap.**
`api.handleAddParticipant` writes the row and *then* asks the mesh for the
address. The address is the only thing in the whole operation that can clash. So
the irreversible step runs first and the refusable step second, which is exactly
why a refusal leaves a bank in the roster that can neither pay nor be paid, with
no way back. The handler documents the consequence honestly and at length; what
it does not say is that reversing the two would remove it.

### Task 17's shape, settled 2026-08-05

**Admission is a relayed conversation, and it is the return's topology.** The
bank composes the request, the clearing house relays it to the settlement agent,
the settlement agent acts in its own book and answers, and the clearing house
writes its own row from the answer and forwards it. Task 16 built that shape for
the pacs.004; this reuses it rather than inventing a second one.

```
1  operator --> Mesh.Admit(name, bic, assets)
     reserve the BIC at the mesh        <- the only clashable thing, taken first
     the bank's OWN unit of work: book, chart of accounts, default product,
       Status = Founded
     on failure: drop the actor again; nothing was written
2  bank            --acmt.007-->  clearing house
3  clearing house  --acmt.007-->  central bank      (relayed, header replaced)
4  central bank: one settlement account per asset in its OWN book,
                 its OWN member row keyed by BIC.  Idempotent per BIC.
5  central bank    --acmt.010-->  clearing house
6  clearing house: writes the routing entry FROM the acknowledgement.
                   The bank is now routable.
7  clearing house  --acmt.010-->  bank    Status = Member, settlement
                                          references recorded
```

`acmt.011` comes back the same way on a refusal, and the bank stays `Founded`.

**The clearing house holds nothing across the relay, and that is the point of
contrast.** `csm.held` — Task 16's in-memory hold — does not survive a restart,
and the branch carries that as a known defect. Nothing needs holding here,
because the acknowledgement names the bank, its assets and its accounts. It is
the same discovery Task 16 made about `OrgnlTxRef` one flow over: a message that
carries its own parties needs no state behind it.

#### Founding is a state, not a stage

`Mesh.Admit` is one door, and its synchronous half founds the bank exactly as
`Mesh.Submit`'s synchronous half runs the submitting bank's own. It returns with
the bank `Founded`; what the scheme thinks arrives later, as a message.

**Founded and not yet a member is legitimate.** A bank has a licence and a core
banking system before it joins a scheme. Such a bank can open customer accounts
and cannot pay or be paid — which is not a broken state but a true one, and it is
what makes the orphan defect go away rather than move. An interrupted admission
leaves a founded bank; re-calling `Admit` re-applies rather than re-founds.

The ordering closes the rest. The address is reserved at the mesh **before**
anything is written, and the actor is dropped again if the write fails: an
in-memory rollback is reliable, and a rollback of a committed unit of work is
not. That is the same argument `Mesh.AddBank` already makes internally for its
bank index, applied one layer out.

**A BIC that is already taken is two different situations and `Admit` must tell
them apart.** If the address belongs to a bank this mesh founded and the roster
has no entry for it, the operator is retrying an interrupted admission: nothing
is founded a second time and the `acmt.007` goes out again. If it belongs to
anybody else — a member, or an actor that is not a bank — it is refused. Getting
this wrong in either direction is a defect with a name: refuse the first and a
founded bank can never join, accept the second and admission overwrites an
institution.

**There are two states and not three.** `Founded` and `Member`. An `Applied`
state between them would record that the request is out, and nothing would read
it — the same field-nothing-reads this sub-project already refused for the advice
row's `kind` discriminator, and the defect class this repository keeps finding.
What "the request is out" is worth knowing for is a stuck admission, which is
Task 19's kind of question and needs the reconciliation instrument rather than a
column.

#### The rows

`payment.Participant` is dissolved. Three rows, each written by exactly one
entity, each already the shape of the store it moves into at Task 18:

| row | owner | holds |
|---|---|---|
| `Bank` | the bank | `BookID`, `CustomerSubledger`, `ProductID`, its four internal accounts per asset, `Status`, and the settlement references it learned from the acknowledgement |
| `SettlementMember` | the central bank | BIC → name, and one settlement account per asset |
| `RosterEntry` | the clearing house | BIC → name, assets, admitted-at. Routing, and nothing else |

`SettleCycleTx`, `SettleReturnTx` and `ReserveBalance` read the central bank's
own row instead of `p.Assets[].Settlement`. Routing and `ListParticipants` read
the roster entry.

**The identifier between institutions is the BIC, and only the BIC.** The bank's
own id is its own; neither of the other two rows carries it. The operator console
enumerates banks by holding every store, which is what it already does and what
the reconciliation harness under *Testing* is designed around — it is outside the
entity boundary by construction, and it is the only thing that is.

#### The prize is in `ops.go`

`GetParticipant` is the hole `ops.go` names about itself. It "hands back another
bank's live handles", and the file records that closing it "needs a narrower
return, which is payment's to give and sub-project 8's to want".

With `Participant` dissolved there is nothing to hand over. A handler asking
about a counterparty gets a roster entry; live ledger and deposit handles exist
only on a bank's own record. That crossing closes as a consequence of the split,
and **no other task on this sub-project was going to close it** — Task 18 moves
rows between stores and would have moved this one intact.

#### Two authorities on one address, resolved

The mesh's actor map refuses a taken BIC today. After this the roster refuses it
too, and two answers to one question is one too many. The resolution: **the
roster is the domain's truth and the actor map is the transport's.** The clearing
house refuses a duplicate BIC with `acmt.011` *before* it relays, and the mesh's
refusal becomes what it should always have been — a statement about
connectivity, not about membership.

#### The messages, and what is not true about them

`acmt.007.001` AccountOpeningRequest, `acmt.010.001` AccountRequestAcknowledgement
and `acmt.011.001` AccountRequestRejection. Real messages, verified against the
ISO 20022 catalogue rather than recalled; the rejection is `acmt.011` and not
`acmt.012`, which is a different message.

The family is the right shape — an account holder asking an account servicer to
open an account — and this use of it is **not** how the real thing works, which
has to be said in the package rather than discovered. `acmt` is eBAM, designed
for a corporate opening an account at its bank. A bank's RTGS account at its
central bank is created through reference data, not through eBAM: in TARGET it is
CRDM static data and `reda` messages. This system models admission as a
conversation because the *sequence and the ownership* are what it is teaching —
who may open which account, and in whose book — and the message family that
carries it is the closest true one rather than the actual one.

Scheme membership is contractual in life and is not messaged at all. What the
`acmt.007` carries here is the settlement-account request; the routing entry
falls out of its acknowledgement, which is why the clearing house writes that row
from a message it did not originate.

##### The schema carries one currency per request, and that decides the refusal (2026-08-05)

Left as written above, per this file's convention for pre-ruling wording. Task
17a read the three XSDs rather than trusting the plan's predicted structs, and
the schema contradicted them in thirteen places. One of the thirteen changes
this section's design.

`acmt.007`'s `Acct/Ccy` is `minOccurs="1" maxOccurs="1"`: **one currency per
request.** A bank clearing a euro and a dollar scheme sends two `acmt.007`s, not
one naming two currencies. The acknowledgement is the asymmetric half —
`AccountForAction1` is unbounded — so one `acmt.010` lists every account the
servicer holds for that BIC. That is the standard's own shape: eBAM opens one
account per request, and a bank with accounts in two currencies really does ask
twice.

The consequence is not the extra message. It is that **`Refs/PrcId` — mandatory
on all three messages — is the conversation's only correlator**, because the
acknowledgement carries no back-reference to the request at all (the rejection
does, at `RjctdReqId`; the acknowledgement does not). One process id per
admission, echoed by every message in it.

**That is what makes the pre-relay refusal above implementable.** `Mesh.Admit`
reserves the address at the mesh before anything is written or sent, so an
impostor never gets a message onto the wire; the only requests that can reach
the clearing house on a BIC already in its roster are **the same bank's second
asset** and **an operator re-driving an interrupted admission**. A refusal keyed
on "is this BIC in the roster" would therefore refuse exactly the two cases it
must allow, and never fire on the one it exists for. Keyed on the admission's
process id it separates them: same admission, relay; different admission,
`acmt.011` before relaying.

So `RosterEntry` carries the admission reference beside its routing fields. Its
reader is the clearing house's refusal, which is what keeps it from being the
field-nothing-reads this sub-project has twice refused. The alternative was to
tell two institutions apart by the legal name on the message, which is a weaker
claim than this system makes anywhere else.

`AdmitMemberTx` writes-or-extends the roster entry rather than refusing every
second acknowledgement, and `ErrBICAlreadyAdmitted` means *a different
admission* on a taken BIC. A single-asset admission still puts four messages on
the wire, which is what the flow above describes.

#### `DepositTx` is re-routed and not fixed

The sixth crossing has to be touched, because the field it reads disappears. It
reads the **bank's own** record of its settlement account instead — the reference
the bank learned from the acknowledgement, which a real account holder knows the
way it knows its own IBAN.

**That leaves the read legitimate and the crossing exactly where it always was:
the posting.** `DepositTx` writes an entry in `CentralBankBook`, and no
re-routing of a lookup changes that. The re-route must not be written up as a
fix, and the paragraph that describes it must name the posting as what remains.
What Task 17 adds is the measurement — a recorder test pinning that funding
reaches two books today — so the crossing fails loudly at the split rather than
quietly, which is the discipline every other crossing here has had.

**Which entity reads which record is decided by whose question it is**, and the
three readers of the settlement account split three ways:

| reader | reads | why |
|---|---|---|
| `SettleCycleTx`, `SettleReturnTx` | the central bank's `SettlementMember` | the settlement agent asking about its own book. This is the read the new row exists for |
| `DepositTx` | the bank's own `Assets[asset].Settlement` | an account holder quoting its own account number |
| `ReserveBalance` | the central bank's `SettlementMember` | the operator console asking the central bank about its own book, from outside the boundary by construction |

`SettleCycleTx` still has to turn a cycle's net positions — keyed by
participant — into BICs before it can ask, and it does that through the roster.
**That is a second crossing inside the first and Task 17 does not close it**: the
pacs.009's legs already carry BICs, so closing it means the settlement agent
settling from the message rather than from the cycle row, which is the gap
`mesh.centralBank`'s own doc already records as sub-project 8's and Task 18's.

#### The measurements

- **`TestWhichBooksAdmissionReaches`**, the counterpart the Tasks table asks for.
  Today one call reaches `[the bank's book, CentralBankBook, NetworkBook]`. After:
  the bank reaches its own book, the central bank reaches `CentralBankBook`, the
  clearing house reaches `NetworkBook`. Watched failing first.
- **`TestWritingAParticipantTouchesNoBankBook`** gains entries in
  `structCarriedBooks` for both new rows. That table fails on a candidate it does
  not decide, so neither can slip through undecided.
- **`TestFundingAReserveReachesTwoBooks`**, pinning crossing 6 as a fact.

Task 15's lesson applies to the first of those: the counterpart is the one whose
*name* will claim the opposite of its measurement if it is not rewritten.

#### The sub-tasks

Letters, not decimals. `17.1`/`17.2`/`17.3` are sub-project 9's SQLite swap and
must not collide.

| | |
|---|---|
| 17a | `iso20022`: acmt.007 / acmt.010 / acmt.011, goldens, `validate()` |
| 17b | the three-way row split, and `ops.go`'s hole closing with it |
| 17c | the three domain acts; `AddParticipantTx` kept as a transitional composition |
| 17d | the mesh conversation; `AddParticipantTx` deleted |
| 17e | `api`, `seed` and `web` rewiring; `DepositTx` re-routed; crossing 6 measured |
| 17f | the documentation sweep |

**The boundary between 17b and 17e is the store line, and it is stated here
because it is the one place two tasks could each assume the other has it.** 17b
owns `payment` and `store` — the three types, the `Tx` methods, `store/mem`,
`store/pg`, `storetest` and `0001_init.sql` — and makes only the mechanical edits
that keep `api`, `seed` and `web` compiling. 17e owns the *rewiring* the
conversation makes necessary: the endpoint's new answer, the DTOs, the seed's
admission, the web types. If 17b runs long it splits at the same line — the types
and `payment`, then the stores and their conformance suite.

**17c keeps the old call alive on purpose.** Task 16 learned that deleting it
early leaves the branch un-buildable between two tasks, so neither can be
verified or reviewed on its own — and the transitional composition turns every
existing admission test into a check that three acts equal what one did. Every
comment written for it must announce its own expiry, and 17d's reviewer is to
treat a surviving "Task 17d will…" as a finding.

#### What Task 17 does not do

- It does not close crossing 2 (`ResolveIdentifierTx`) or crossing 6
  (`DepositTx`). Both stay open with named owners.
- It does not model vault cash or a lodgement. That is the honest fix for
  crossing 6 and it is its own task, deliberately not folded into the task that
  adds a message family and dissolves the central domain type.
- It does not split the stores. Admission is a conversation on one store, and the
  recorder measures it there — the same order Tasks 14 to 16 ran in.

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
4. **"Package iso20022 implements the SEPA interbank messages"**
   (`iso20022/doc.go:1`). Reversed by Task 17. `acmt` is neither interbank nor
   SEPA: the EPC profiles no part of it, so the package's whole "the standard is
   a superset and a scheme narrows it" framing does not apply to the three
   messages admission adds, and the doc must say which claims are the scheme's
   and which are now only the standard's.
5. **A bank "can never exist without the accounts it needs"**
   (`AddParticipantTx`). Reversed by Task 17, and it is the reversal that carries
   the domain content: a founded bank without a settlement account is a real
   thing, and the guarantee the atomic write bought was never one a real
   admission has.

`iso20022/doc.go`'s message list opens with **"Six."** and Task 17 makes it nine.
That is the count-goes-stale defect in the file that will teach it hardest, and
17f rewrites the sentence as a description rather than incrementing it.

## Tasks

Six. Larger than 7b's four, which is stated here rather than discovered at task
four.

Tasks 14–17 land **while one store is still underneath**, so every domain change
is measured by the existing recorder before the safety net is removed.

| # | task | the measurement it moves |
|---|---|---|
| 14 | **The message carries the parties.** Counterparty BIC/name/address on `InitiatePaymentRequest`; the receiving bank resolves in its own register only | `TestWhichBooksEachBankActuallyReaches`: the **submitting** bank only. `TestWhichBooksEachBankActuallyReaches`: payer's bank `[debtor, creditor, Network]` → `[debtor, Network]`; `TestWhichBooksEachBankReachesInAPull`: submitting payee's bank → `[creditor, Network]`. The **receiver's** set does not move here — see below |
| 15 | **Settlement becomes a conversation.** Central bank posts only its own netting transaction; banks post mirror and creditor legs locally on advice; settlement-position row; unclaimed balances; `CheckCreditTx` at settlement | `TestWhichBooksTheCentralBankReachesWhenItSettles`: `allBooks()` → `[CentralBankBook, Network]`. **The loud failure the handoff predicted** |
| 16 | **Return becomes a conversation.** `OrgnlTxRef` on pacs.004; central bank reverses reserves from the message; banks post their own compensating legs; the direction-dependent clawback rule and `Returns Receivable` | `TestWhichBooksAReturnReaches`: `allBooks()` → **`[CentralBankBook]`**. This prediction said `[CentralBankBook, Network]` and was wrong: `SettleReturnTx` writes no row and appends no audit event, so the settlement agent never allocates a network id. The trail is not lost — `EventPaymentReturned` lands from `PostReturnLegTx`, and each member writes its own `SettlementAdvice` row from its camt.053 |
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

### The store underneath becomes SQLite, between Task 17 and Task 18 (2026-08-03)

`store/pg` and `store/mem` are both being replaced by one backend,
`store/sqlite`, on the cgo-free `modernc.org/sqlite`. That is sub-project 9 —
`specs/2026-08-03-sqlite-only-store-design.md` — landing as Tasks **17.1 / 17.2 /
17.3**, after admission and before the split. The numbering was chosen so that
nothing here renumbers.

The rows above are left as written, per this file's convention. Three of them
mean something different afterwards, and the differences all make Task 18
smaller:

1. **Task 18 is three shapes × one backend, not × two.** Six implementations
   become three. This is the main reason the swap goes first: porting a backend
   after the split is three times the work of porting it before.
2. **"Postgres: one schema per entity in one database", under *Testing*, is
   gone** — with `TEST_DATABASE_URL` and `make test-pg`. It was a compromise
   taken to keep the Postgres test path working, and namespacing is a weaker
   thing than this sub-project's own thesis asks for. Under SQLite it is **one
   file per entity**, or one named `mode=memory&cache=shared` database per entity
   under test, which is isolation of the kind the DDL boundary was meant to
   express.
3. **"`storetest` becomes three conformance suites" is half wrong from 17.3
   onward.** With one implementation there is nothing to conform *to*. It becomes
   three shape suites — the same suite reused across `bank`, `csm` and
   `centralbank` — and its doc comment has to stop claiming otherwise.

What does not change: the shapes, the crossings, the measurements, and the rule
that every measurement move is watched failing first.

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
