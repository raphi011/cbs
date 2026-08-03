# Handoff — sub-project 8 Task 16, on `spec/db-per-entity`, NOT merged

`spec/db-per-entity` is at `4ac2bcc`, 14 commits ahead of `main` (`cd5056e`).
41 files, +4108/−844.

**This branch is not ready to merge, and the two reasons are not leftovers.**
Task 16f — the documentation sweep — was never started, and no whole-branch
review has run. On Task 15 the whole-branch review is what found the Critical
money bug that per-task review structurally cannot see. Both are below under
_What is left_.

Everything else in this file was verified against the code at `4ac2bcc`, not
against a plan.

## Verification, run by the controller at `4ac2bcc`

Not by a subagent. All five green:

```bash
gofmt -l . && go vet ./... && go build ./...          # clean
go test ./...                                          # store/mem, all packages ok
TEST_DATABASE_URL="postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable" go test ./... -count=1
go test ./mesh/ ./api/ ./cmd/... -race -count=1
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test   # 179 passed
```

The Postgres run took 20–32s per package against 1–2s on `store/mem`, which is
how you can tell it did not skip. **`make test-pg` uses Docker and does not work
on this machine** — use the explicit `TEST_DATABASE_URL`.

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

## The crossing count: four closed, one open

| # | crossing | state |
|---|---|---|
| 1 | `partyTx` reads the counterparty's register | **CLOSED** (Task 14) |
| 2 | `ResolveIdentifierTx` sweeps every member's register | open — Task 18 |
| 3 | `SettleCycleTx` posts in every book | **CLOSED** (Task 15) |
| 4 | `ReturnPaymentTx` posts in three books | **CLOSED** |
| 5 | `AddParticipantTx` writes into `CentralBankBook` | open — Task 17 |

## What is left, and why both block merge

### 1. Task 16f — the documentation sweep. Unstarted.

The brief is at
`.superpowers/sdd/2026-08-03-task-16-return-becomes-a-conversation/task-16f-brief.md`
(git-ignored; regenerate with the plan's `task-brief` script if it is gone).

Three domain facts no layer currently carries:

- a return is settled by the settlement agent and **booked by each bank
  locally**, so a return has an unreconciled position exactly as a cut-off does;
- **the creditor's bank carries the refund risk on a direct debit**, which is why
  it vets its creditors and why `Returns Receivable` exists on one side only;
- a bank can **refuse** a return it would have to fund only when it is the
  returning bank; otherwise the refusal comes too late to matter.

Layers: `README.md` (authoritative) → `web/src/components/hint-content.ts` → quiz
chapters 9, 11, 12, 15, 16 → `0001_init.sql`'s comments.

**Known already-outstanding items for that sweep**, found during 16b and 16c and
deliberately deferred:

- `README.md` and `hint-content.ts` list a bank's internal accounts and **do not
  yet mention `Returns Receivable`**. Tasks 16c and 16e touched both files for
  other reasons, so they are *partially* swept — which is worse than untouched,
  because two layers now agree about the advice key and disagree about the chart
  of accounts.
- **Chapter 9 is at 22 questions, the maximum `diversity.test.ts` allows.** A new
  question there means replacing one.
- Open all five chapters, including ones this change does not obviously touch.
  Chapter 10 contradicted Task 15's headline concept and scored a false statement
  correct, having never been opened by any task in that sub-project.

And two mechanical traps: a `[[wiki-link]]` to a key missing from
`hint-content.ts` throws at runtime and takes **every** route down while
`next build` stays green (`npm run test` catches it in hint bodies *and* quiz
explanations); and **no test in this repository can catch a web rendering
regression** — take a screenshot and look at it.

### 2. The whole-branch review. Not run.

Dispatch it on the most capable model over `git merge-base main HEAD`..`HEAD`.
Point it at the deferred-minor list below. On Task 15 this review found a
Critical money bug — a return clawing back from an account that never received
the money — created by one task and left unhandled by another, and it was found
by a reviewer that **built a probe and ran it rather than reading**.

## What each task did, with its review history

Every task was implemented by a fresh subagent, reviewed, and fixed until its
review came back clean. Seven fix rounds in total; **every single one of them was
at least partly about prose asserting something the code does not do.**

| task | commits | rounds |
|---|---|---|
| 16a `OrgnlTxRef` + `ReadReturn` | `50fef43`, `30fe59f` | 1 |
| 16b `Returns Receivable` | `6a96a46`, `557eac1` | 1 |
| 16c advice keyed by reference | `2284c3e`, `389cbf2` | 1 |
| 16d the domain splits into three acts | `54e8920`, `349fc96`, `58e5d43`, `0344f78` | 2 |
| 16e the mesh conversation | `f074043`, `d274779`, `4ac2bcc` | 1 |

`d95d318` is a plan correction, not a task — see _Decisions taken during
execution_.

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
- **An on-us payment** (one bank on both sides) makes `mayRefuse` true for *both*
  legs, so an on-us pull's clawback is refusable — the opposite of "forced on a
  pull". Untested; nothing in the repository constructs an on-us payment.
- **A push clawback refusal now reaches `ReasonFor` as AM04 about the returning
  bank's own customer**, which a push return could never produce before. The
  mapping is right; a mesh test now exercises it.
- **`api/mesh_test.go` hardcodes seed-allocated `dep_` ids** and had to be
  updated when Task 16b added a fourth account per bank. It will shift again on
  the next account. Those tests should read ids from the seed rather than assert
  them.
- **`TestParticipantHasAccountsPerAsset`** does not pin `ReturnsReceivable`'s
  type alongside `Unclaimed`'s.
- **No golden file has ever been schema-validated here.** `pacs004.xml` grew
  `OrgnlTxRef` and nothing checks its position in the sequence, because
  `TestGoldenFilesValidateAgainstTheSchema` skips every subtest —
  `iso20022/testdata/xsd/` is absent for licensing reasons. **A skip is not a
  pass.**
- **The mid-flight rollback at the settlement seam remains unwitnessed**, carried
  from Task 15.

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
  is a claim, not evidence.
