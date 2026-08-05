# Handoff — sub-project 8 Task 16, on `spec/db-per-entity`, ready to merge

`spec/db-per-entity` is at `f2db0e1`, 25 commits ahead of `main` (`b52761e`).
50 files, +5311/−891.

**The two things that blocked this branch on 2026-08-03 are both closed**
(2026-08-05). Task 16f — the documentation sweep — landed at `41c52d1`/`5ebdeac`,
and the whole-branch review ran. It earned its keep: **two Critical money
defects**, neither visible to any per-task review, both reproduced by probe on
`store/mem` *and* `store/pg`, both reachable through the HTTP API. They are at
_What the whole-branch review found_ below, with the reasoning behind the fix
each got, because in both cases the alternative was tempting and wrong.

Everything in this file was verified against the code at `f2db0e1`, not against
a plan.

## Verification, run by the controller at `f2db0e1`

Not by a subagent. All five green:

```bash
gofmt -l . && go vet ./... && go build ./...          # clean
go test ./... -count=1                                 # store/mem, all packages ok
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./... -count=1
go test ./mesh/ ./api/ ./cmd/... -race -count=1
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test   # 179 passed
```

The Postgres run took 23–39s on the seven store-touching packages against
2.5–5s on `store/mem`, which is how you can tell it did not skip. **`make
test-pg` uses Docker and does not work on this machine** — use the explicit
`TEST_DATABASE_URL`.

## Read this first

`docs/superpowers/specs/2026-08-02-db-per-entity-design.md`, section
`## The return`. It carries `### Task 16's shape, settled 2026-08-03`, written
before any code, and it is the authority. The plan is
`docs/superpowers/plans/2026-08-03-task-16-return-becomes-a-conversation.md`.

### Added after this handoff was written (2026-08-03)

The pointer above is section-scoped and is no longer enough. Read **also**
`### The store underneath becomes SQLite, between Task 17 and Task 18` in that
same spec — it is in the Tasks area, not in `## The return`, so following the
line above literally will miss it.

**`store/pg` and `store/mem` are both being replaced by one backend**,
`store/sqlite`, on the cgo-free `modernc.org/sqlite`. That is sub-project 9,
specced at `docs/superpowers/specs/2026-08-03-sqlite-only-store-design.md`,
landing as Tasks **17.1 / 17.2 / 17.3** — after Task 17, before Task 18. The
numbering was chosen so that nothing in this file renumbers.

**Nothing else in this handoff is wrong for Task 17.** Both rules at the bottom
that the swap eventually reverses — "There is **one migration**. Edit
`0001_init.sql` in place" and the `TEST_DATABASE_URL` verification line — are
still true while admission is being built. They expire at 17.1, and it is **Task
17's own handoff** that must say so rather than copy this file's rule list
forward.

Two things it does change, both small:

- **Task 18 becomes three shapes × one backend, not × two**, and its isolation is
  one database *file* per entity rather than one Postgres schema per entity. If
  anything in Task 17 would be designed around Task 18's store, design it around
  that.
- **Task 16f sweeps `0001_init.sql`'s comments, and 17.1 then translates that
  file whole.** Not wasted — the comments are the content and they carry across —
  but where 16f has the choice, write the *reasoning* rather than the Postgres
  mechanism. Postgres spellings will not survive; the arguments will.

## What Task 16 did

**The return stopped being one institution's act.** `ReturnPaymentTx` posted
three compensating transactions in one `Store.Update` — the payer refunded at
their bank, the payee clawed back at theirs, the reserves reversed at the central
bank — two of them in member banks' books, made by the settlement agent.

Now:

1. the returning bank posts **its own leg** against its clearing suspense, and
   **refuses here** if it cannot fund it;
2. it sends the pacs.004, which now carries `OrgnlTxRef` naming both agents;
3. the clearing house relays it to the settlement agent **and holds it**;
4. the central bank reads the parties **off the message**, reverses reserves in
   its own book, and is final either way;
5. it sends a camt.053 to **both** banks — before the answer — and answers the
   clearing house;
6. the clearing house forwards the status to the returning bank and **releases
   the held pacs.004** to the other bank;
7. each bank books its reserve mirror from the statement and its customer leg
   from the payment message, locally, each in its own unit of work.

### The one rule, and why the direction-dependence is not a special case

The **clawback** is always at the **creditor's** bank, out of
`Payment.CreditorLegAccount`. The **refund** is always at the **debtor's** bank.
Direction decides only which of the two the *returning* bank is holding:

| | returning bank posts, before it sends | other bank posts, after finality |
|---|---|---|
| push (SCT) | the clawback — **refusable**, so no pacs.004 exists | the refund — always postable |
| pull (SDD) | the refund — always postable | the clawback — **forced**; `Returns Receivable` |

So: **a bank can refuse a leg only if it posts it before it sends.**
`Returns Receivable` is needed on the pull side and nowhere else, because the
bank that must force the posting is the one that first hears about the return
after it is already final. That is not a stipulation; it falls out.

### The measurement moved, and the plan's guess was wrong

| test | was | is |
|---|---|---|
| `TestWhichBooksAReturnReaches` | `allBooks()` | **`[CentralBankBook]`** |
| (new) `TestEachBankBooksItsOwnReturnAndNoOtherBooks` | — | per-bank, measured |

The plan predicted `[CentralBankBook, Network]`. **It was measured, not copied,
and `NetworkBook` is absent**: `SettleReturnTx` writes no row and appends no
audit event, so the settlement agent never allocates a network id. That is not a
lost audit trail — `EventPaymentReturned` still lands, from `PostReturnLegTx`'s
`Returned` branch, and each member still writes a `SettlementAdvice` row from its
camt.053.

## The crossings: closed by Tasks 14, 15 and 16; two still standing

| # | crossing | state |
|---|---|---|
| 1 | `partyTx` reads the counterparty's register | **CLOSED** (Task 14) |
| 2 | `ResolveIdentifierTx` sweeps every member's register | open — Task 18 |
| 3 | `SettleCycleTx` posts in every book | **CLOSED** (Task 15) |
| 4 | `ReturnPaymentTx` posts in three books | **CLOSED** |
| 5 | `AddParticipantTx` writes into `CentralBankBook` | open — Task 17 |

## What the whole-branch review found

It found what per-task review cannot see: both defects were made by one task's
value and read wrongly by another's. Both were reproduced by probe, on both
stores, before anything was changed. **The reviewer built probes and ran them
rather than reading** — the same method that found Task 15's Critical, and the
second time in two sub-projects it has been the only thing that worked.

Clean under probe, for the record, so the next reviewer knows what is already
covered: money conservation in both directions across every account in every
book; the return of a payment that settled into `Unclaimed`; a payer who closed
their account after settlement; the forced clawback into `Returns Receivable`;
the push-side refusal; redelivery of all five return-path messages; and a
late RJCT against a completed return.

### C1 — a return retried after an RJCT unwind never repaid the payer

`ReverseReturnLegTx` left the leg's transaction id on the payment and argued:
*"there is no reader for whom a stale id here decides anything."* There was one.
`PostReturnLegTx`'s `p.ReturnRefundTx == ""` case read a **`Reversed`**
transaction's id as "already posted" and took the idempotent-redelivery arm —
while the rest of the conversation ran to completion. Measured: the biller
clawed back 250000, the payer repaid **nothing**, 250000 stranded in the
returning bank's clearing suspense for ever, the row reading `Returned` and the
network having answered ACSC. AM04 is exactly the retriable RJCT, and re-calling
`Mesh.Return` is this branch's own documented route out of the failed-send seam.

**Fixed by fixing the reader, not the writer** (`66b044c`). `PostReturnLegTx`
now asks the ledger whether the leg *stands* (`legStandsTx`) rather than whether
a field is non-empty, and the retry's posting derives its idempotency key from
the reversed attempt (`returnLegKey`) so the ledger does not refuse the repost.
No new column.

Refusing a second `Return` outright was considered and rejected: it trades a
money bug for a payer who can never be repaid, and returns are retriable in the
scheme. **The rejected option was the smaller diff. That is not a reason.**

Re-review probed both legs — the push side independently, forcing AM04 by
draining the payee bank's settlement account through a second customer — and
measured the payer repaid, the counterparty clawed back, both clearing suspenses
back to zero. Reverting the reader reproduces the Critical; removing `replacing`
from `returnLegKey` reddens both tests, so both halves are load-bearing.

### C2 — an on-us return silently lost half the reserve reversal

`SettleReturnTx` always emits two statements, one per side. With one bank on both
sides they are two statements for the same `(Book, Reference, Asset)` differing
only in sign, and `PostSettlementAdviceTx` dedupes the second away. Measured: the
bank's reserve mirror 250000 below the central bank's record of it, and its
clearing suspense permanently at −250000. **I1** was the same root — `mayRefuse`
was true for *both* legs when one bank is both parties, so an on-us pull refused
its own customer's unconditional eight-week refund, inverting the branch's
headline rule.

**Fixed by refusing an on-us payment where it enters the mesh** (`b13127d`):
`mesh.ErrOnUsPayment` in `Mesh.Submit`, 422 at the API. An on-us payment never
reaches a clearing house in life — both customers bank at the same institution,
so there are no reserves to move, nothing to net and no `camt.053` to send.
Special-casing `SettleReturnTx` was rejected: it treats the symptom of a payment
that should never have been submitted to clearing.

`mayRefuse` became a property of the **leg** regardless of that boundary —
`Push && returner` (`f6250b4`) — so the rule is stated rather than emergent, and
does not depend on the boundary check holding. Re-review probed all four
combinations plus on-us and confirmed the push-side clawback refusal, which the
whole design rests on, still fires.

**What this leaves the system without: two customers of the same bank cannot pay
each other at all.** That is now an openly recorded hole rather than a silently
diverging one. See _Follow-up work this branch created_.

## What each task did, with its review history

Every task was implemented by a fresh subagent, reviewed, and fixed until its
review came back clean. Eight fix rounds in total; **seven of the eight were at
least partly about prose asserting something the code does not do.**

| task | commits | rounds |
|---|---|---|
| 16a `OrgnlTxRef` + `ReadReturn` | `50fef43`, `30fe59f` | 1 |
| 16b `Returns Receivable` | `6a96a46`, `557eac1` | 1 |
| 16c advice keyed by reference | `2284c3e`, `389cbf2` | 1 |
| 16d the domain splits into three acts | `54e8920`, `349fc96`, `58e5d43`, `0344f78` | 2 |
| 16e the mesh conversation | `f074043`, `d274779`, `4ac2bcc` | 1 |
| 16f the documentation sweep | `41c52d1`, `5ebdeac` | 1 |
| final fix wave (C1, C2, I1, M1, M3) | `66b044c`, `b13127d`, `f6250b4`, `f2db0e1` | — |

`d95d318` is a plan correction, not a task — see _Decisions taken during
execution_.

16f's one fix round is worth its own line, because it is the lesson repeating
inside the task that exists to stop it: the sweep **added** a chapter 9 question
to teach the return's finality, and that question scored as correct *"each bank
posts its own reserve mirror and its own customer leg"* after finality. Six other
layers had the ordering right. The returning bank posts its customer leg before
the pacs.004 exists; after finality exactly one bank has a leg left to post. A
question written to carry the branch's headline fact stated its inverse.

## Decisions taken during execution, and why

**1. Task 16d kept `ReturnPaymentTx` alive as a transitional composition.** The
plan said to delete it and let the mesh fail to compile until 16e. That was
wrong: it leaves the branch un-buildable between two tasks, so neither could be
verified or reviewed on its own. It became a composition of the three new acts
instead — and that turned every existing return test into a check that the three
acts compose to what the atomic version did. 16e deleted it. Recorded at
`d95d318`.

**2. The advice row has no `kind` discriminator.** The spec first said it would.
Ids are unique across the store, so a `kind` column would have been a field
nothing reads — the defect class this repository keeps finding. Spec amended
before the plan quoted it.

**3. `SettlementStatement.StatementRef` is an untyped `string`.** On the return
path there is no settlement row — the value is `<payment>:rtr` — and a typed
`SettlementID` holding a value that is not any row's key is a quiet lie.

**4. The clearing house holds the pacs.004 until it has an ACSC.** Relaying it
earlier would have the other bank post a customer leg against a return that might
still be refused. This is the first mutable actor state in `mesh` (`csm.held`).
Its safety rests on one-goroutine-per-actor, which was **verified true** rather
than assumed: `mesh/mesh.go:347-351` and `:387-389` launch exactly one `go m.run`
per actor, `addActors` refuses a duplicate BIC, `run` dispatches sequentially,
and `held` is touched only under `handle`. A test now asserts
`closeCycle`/`settle`/`reject` leave it alone.

**5. `SettleReturnTx` checks redelivery *before* funding.** The plan's order made
a redelivered return answer AM04. Pinned by a test that fails if the order is
reversed.

**6. `Mesh.Return` was not widened to carry the payment beside the error.** The
half-happened state — a bank that posted its leg and then failed to send — has a
route out: re-calling `Mesh.Return` re-enters `PostReturnLegTx`'s idempotent
third case, rebuilds the message and sends. No double posting, no strand. That
sentence is now where the error is raised.

## Gaps this task closed that were not asked for

- **The debtor-side stranding gap**, open since before Task 15 and recorded at
  length in `ReturnPaymentTx`'s old doc: a refund into a payer's account closed
  after settlement stranded for ever, and refusing traded one stranding for
  another. The split made `CheckCreditTx` affordable there exactly as Task 15 did
  on the creditor side. The refund now diverts to `Unclaimed` on
  `deposit.ErrAccountClosed` — **and only on that**, because `glAccountTx`
  collapses every store failure into one value and money must not be routed on a
  failure nobody can classify.
- **A reachable money bug in `ReadReturn`**, found by the 16e implementer outside
  its brief: a pacs.004 with an empty `OrgnlTxId` would have settled real
  reserves between two real banks under the idempotency key `":return-settle"`,
  for a payment nobody could name. Guarded in the reader **and** in
  `SettleReturnTx`, so the reader's guard is defence in depth rather than the
  only line.
- **`ReverseReturnLegTx` refuses unless `p.Status == Settled`** — i.e. unless the
  second leg has not landed. Without it, an RJCT arriving after a completed
  return would undo one customer leg and leave the other standing: the payee made
  whole while the payer keeps the refund.

## Known defects and deferrals carried on this branch

- **`csm.held` does not survive a restart.** A return settled at the central bank
  with its pacs.004 still held is a payment with one leg posted and the other
  never — no route out. Documented where the state lives. This is the same shape
  Task 15 carried into `main` for `Cleared`-after-`Settled`.
- **`delete(c.held, id)` happens before `releaseReturn`**, so a failed release is
  unrecoverable. Unreachable in this transport, documented beside its neighbours.
- **A failed camt.053 send suppresses the answer for the whole return**, because
  `advise` returns on the first failing send — the same defect Task 15 recorded
  for the settlement path, now reachable from a second flow. Unreachable only
  because this transport cannot lose a message.
- **A push clawback refusal now reaches `ReasonFor` as AM04 about the returning
  bank's own customer**, which a push return could never produce before. The
  mapping is right; a mesh test now exercises it.
- **A redelivered pacs.004 after the return has completed dead-letters rather
  than being a no-op**, because `p.Status != Settled` fires ahead of the
  already-posted arm. Money-safe — nothing moves — but it contradicts
  `PostReturnLegTx`'s own stated design, and `PostCreditorLegTx` handles the same
  shape the other way. Unreachable in this transport. Measured unchanged by C1's
  fix, which touches that switch.
- **`api/mesh_test.go` hardcodes seed-allocated `dep_` ids** and had to be
  updated when Task 16b added a fourth account per bank. It will shift again on
  the next account. Those tests should read ids from the seed rather than assert
  them.
- **`seed` reaches `payment.SubmitPaymentTx` directly** and so bypasses the on-us
  refusal at `Mesh.Submit`. It builds only cross-bank payments today, so nothing
  is wrong — but `Mesh.Submit`'s doc claim to be the one door every submission
  comes through is true of the mesh and not of the tree.
- **No golden file has ever been schema-validated here.** `pacs004.xml` grew
  `OrgnlTxRef` and nothing checks its position in the sequence, because
  `TestGoldenFilesValidateAgainstTheSchema` skips every subtest —
  `iso20022/testdata/xsd/` is absent for licensing reasons. **A skip is not a
  pass.**
- **The mid-flight rollback at the settlement seam remains unwitnessed**, carried
  from Task 15.

## Follow-up work this branch created or uncovered

Neither of these is Task 17's, and neither should be picked up inside another
task. Both are money that stops.

### 1. An on-us payment has nowhere to go

Refusing on-us at `Mesh.Submit` closed C2 and I1 honestly, but it means two
customers of the same bank cannot pay each other **at all**. The right shape is
that on-us is not a clearing payment but a **book transfer**: the bank
recognises the counterparty as its own at submission and posts both legs in its
own book, one unit of work, no message, no reserves. That is a real product and
its own task.

### 2. A cycle that nets to zero strands every payment in it, for ever

Found by the fix-wave re-reviewer while checking a claim in the fix report — and
it **disproves** that claim, so do not read the fix report's third concern as
settled. The on-us refusal closed only the on-us route to this. Without any
on-us payment: two offsetting cross-bank credit transfers in one cycle net both
members to zero, `csm.settlementLegs` emits nothing, and **both payments strand
at `Cleared` for ever** — both payers debited, neither payee credited, 250000
stuck in each bank's clearing suspense.

Pre-existing, outside this branch's diff and outside every finding, so it was
correctly left alone here. It is the most serious thing in this file that nobody
owns.

## The process lesson this task cost the most

**A grep for one phrasing is not a sweep of a claim, and reporting it as one is
how a false statement survives a review.**

Task 16d was sent back twice for the same finding. The claim was "a return posts
three transactions in one unit of work" — false, because the transitional
composition made five. The first fix corrected five cited call sites by grepping
the exact phrase; a re-review found two more in *the same file*, plus one in
`mesh/doc.go`. The implementer's own diagnosis was right: it had grepped a
phrasing and reported it as a sweep of the claim.

What worked on the third attempt was changing the instruction: **do not grep;
read whole every comment block whose subject is the R-transaction, and report
which files you read in full rather than which pattern you searched.** A list of
files read is checkable. A grep pattern is not. That pass found six survivors —
three the review had not cited — including one that avoided the number entirely
("the atomic three") and one that was wrong in a different way (`the two CUSTOMER
legs` land in members' books, when the two reserve mirrors do too).

Two corollaries worth carrying into Task 17:

- **A count in a comment goes stale; a description of what happens does not.**
  Every one of these claims was a number. They were rewritten as acts.
- **A comment written during a transition should announce its own expiry.** The
  16d docs said "the transitional composition Task 16e deletes", and 16e's
  reviewer was told to treat any surviving "Task 16e will…" as a finding. That
  worked — none survived.

And one from 16e, which is the older lesson in a new costume: **writing a test
for the new guard caught a fixture that had been passing for the wrong reason.**
With the short bank on the creditor side, `SettleReturnTx`'s funding check
refused first, so the test passed against a settlement agent with no id guard at
all. The fixture now funds the creditor's bank and asserts the refusal was *not*
`ErrInsufficientBalance`. The trap is written into the test's own doc.

### And the one the whole-branch review taught, which is now twice in two sub-projects

**Reading a diff cannot find these. Both Criticals were found by a reviewer that
built a probe and ran it, and both were confirmed fixed the same way.**

Two sharper forms of it, both earned here:

- **A comment that argues there is no reader is a claim about every call site,
  and nothing checks it.** C1 is exactly one such sentence — *"there is no reader
  for whom a stale id here decides anything"* — and the reader was thirty lines
  away in the same file. When a doc comment's argument is *"nobody reads this in
  state X"*, that is not documentation, it is an unverified whole-codebase
  assertion. Either find the readers and name them, or do not lean on it.
- **"Untested; nothing in the repository constructs one" is a description of the
  test suite, not of the code.** I1 sat in the ledger under exactly that wording,
  deferred as a minor. The reviewer constructed one in a single test and it was
  reachable through the HTTP API. A defect deferred because no test builds the
  input is deferred on the weakest possible ground.

A third, about severity: **the reviewer ruled C2's sibling M1 "Minor — but fix
before merge"**, because it was the only known-false claim left in the layer the
branch had just rewritten. Severity and merge-blocking are separate questions.

## What the next task is

**Task 17 — admission becomes a conversation.** `AddParticipantTx` writes into
`CentralBankBook`: the central bank should open the settlement account, the
clearing house should add the routing entry, and the bank's chart should be
created in its own store. `TestWritingAParticipantTouchesNoBankBook` gains a
counterpart, and the orphan-participant defect carried into `main` must be
confronted rather than carried again.

Note for whoever takes it: Task 16b added a fourth account to the per-asset loop
in `AddParticipantTx` and that shifted `store/mem`'s shared per-book id counter,
moving hard-coded ids in `api/mesh_test.go`. Task 17 rewrites that loop entirely.

## The rules that cost the most when forgotten

- A `[[wiki-link]]` to a key missing from `hint-content.ts` throws at runtime and
  takes **every** route down while `next build` stays green. `npm run test`
  catches it in hint bodies *and* quiz explanations. Run it, and load a page.
- **No test here can catch a web rendering regression.** Require a screenshot for
  web changes, and look at it.
- `rm -rf web/.next` before `npm run typecheck`.
- Never hand-write a backend path; use `cb()` / `csm()` / `bank(pid, …)`.
- `diversity.test.ts` holds each chapter to 18–22 questions, ≥8 distinct
  `concept` tags, no tag more than 3×, all three difficulty tiers. **Chapter 9 is
  at 22.**
- There is **one migration**. Edit `0001_init.sql` in place.
- **Run the verification yourself before merging.** A subagent reporting it green
  is a claim, not evidence. The Postgres run is checkable without trusting
  anyone: 20–39s on the store-touching packages against 1–5s on `store/mem`. A
  fast run skipped, and a skip is not a pass.
- **Budget for the whole-branch review, and require probes.** It has now found a
  Critical on two consecutive sub-projects, both invisible to a clean per-task
  history, both found by running code rather than reading it.
