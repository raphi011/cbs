# Avoidable review cycles — what Task 17 cost, and why

Task 17 (admission becomes a conversation) took **23 fix rounds** across six
sub-tasks plus a whole-branch fix wave. Every one of the 23 carried at least one
false claim written *inside* a correction.

This file is not a post-mortem of the task. It is a list of the mechanisms that
produced the rounds, with what to change. It is written because the same
mechanisms produced the previous sub-project's eight rounds, and its handoff
recorded them as lessons — in prose, which is exactly the form that did not
prevent the recurrence.

## Where the rounds went

| class | rounds | verdict |
|---|---|---|
| prose asserting something the code does not do | ~14 | avoidable |
| sweep-by-grep missing copies of one claim | 5 (all of 17f) | avoidable |
| a guard closing a hole and leaving its own "does not apply" value open | 5 | preventable by method |
| `store/pg` vs `store/mem` divergence | 3 | **real money defects** |
| a founded bank payable, stranding a whole cycle | 2 | **real money defect** |

Roughly **five rounds found genuine money defects**. The depth was not wasted:
three `store/pg`-only divergences and a whole-cycle stall would have shipped
without it. The other eighteen were claims drifting from code.

## The mechanisms, most expensive first

### 1. The plan contained pre-written doc comments

The plan carried roughly forty comment blocks, written before the code existed
and labelled as predictions. Implementers pasted them; reviewers found them
false; rounds followed.

This was **already known**. The previous sub-project's handoff records: *"seven
of the eight review rounds were at least partly about prose asserting something
the code does not do, much of it inherited verbatim from that plan."* The plan
for Task 17 repeated the practice and added a global constraint telling
implementers not to trust it — which is a warning label on a hazard rather than
the removal of one.

**Change: a plan specifies behaviour, interfaces and tests. It does not contain
comment text.** If a plan wants a doc to make a particular argument, it names the
argument in one line and lets the implementer write it from the code.

### 2. One fact lives in six to twelve places, enforced by nothing

17f spent five rounds correcting a single claim — that a bank's accounts are
provisioned "when it joins the network". Nine live copies were found across five
rounds, each round's grep missing the next because the wording varied: "joins the
network", "when it joins", "provisioned", "opens … reserve and settlement
accounts". One file asserted the claim in its opening sentence and retracted it
twelve lines later. Another — `api/dto_payment.go` — still asserted what its own
TypeScript mirror already carried a four-line retraction of.

`CLAUDE.md` names four layers that must stay in step. The real number is larger,
and nothing checks any of it.

**Change: one canonical statement per domain fact, referenced rather than
restated.** The wiki-link machinery already exists for hint content and is
enforced by `concept-links.test.ts`. Point the other layers at the same
mechanism, so a fact has one home and every other layer links to it.

**Until that exists**, the sweep method that worked is: grep only to *locate*
candidates and never to conclude; flow comment blocks before matching, because a
claim split across a line break is invisible to a line-based pattern; grep for
the sentence you **deleted**, not the one you wrote; treat mirrored pairs
(`api/dto_payment.go` ↔ `web/src/lib/types.ts`) as one artefact; and after
correcting a claim, re-read every passage **in that file** whose subject is it —
comments and rendered strings alike. Report which files you read in full, not
which patterns you searched.

### 3. `store/storetest` could not see the defect class it exists for

Three `store/pg`-vs-`store/mem` divergences were found on this branch, all the
same shape: a read-then-write on one key with no id allocation in front of it.
`store/mem` serialises every `Update` on one process-wide mutex; `store/pg` runs
READ COMMITTED, where both transactions read "not there" and both write.
Measured at 54–60 of 60 runs.

`storetest` is the conformance suite that exists to prevent exactly this, and it
was blind to all three, because its concurrency case is a synthetic shape over
`ledger.Tx` rather than over `payment`'s acts. Each divergence cost a probe
somebody had to think to write.

**Change: extend `storetest` to drive `payment`'s acts.** That converts the most
expensive class on this branch from "found if a reviewer writes the right probe"
into "caught by the suite".

### 4. A guard closes a hole and leaves its own "does not apply" value open

Five instances, three of them consecutive and each behind the last:

1. `AdmissionRef == ""` meant "accepted nothing", so an acknowledgement with no
   reference walked through the guard that reference was added for.
2. The empty `PrcId` behind it — refused by the reader, not by the acts.
3. The empty account map behind that — the account checks are a loop that never
   executes on an empty map.

**Change, and it is the one that worked:** derive the checks from the other
side's list instead of writing one by hand. `checkAcknowledgement` became a
correspondence table against `ReadAdmissionAcknowledgement` — refusal for
refusal — and an independent enumeration then found 7 of 7 covered. Extend the
same method to the *state* an act writes, not only the message it reads.

### 5. The fix's explanation was the next defect

About half the rounds were a correction whose *justification* was false. The
sharpest instances:

- A probe measured 0-of-60 divergence and a comment was written explaining why
  the code was safe. `pgxpool` holds one idle connection after setup, so the
  second racer was still opening TCP. Warm the pool: 60 of 60. **Never measure a
  store race on a cold connection pool.**
- A commit *shortened* a false sentence and left its false clause standing.
  **In a diff, a shortened false sentence looks exactly like a fixed one.**
- The sentence written to retire this repository's stale-count problem contained
  a miscount of the files it was counting.

**Change: when you write a comment explaining a correction, verify the
explanation as carefully as the correction.** And never write down a measurement
you did not take yourself — including one handed to you by a reviewer or a
controller.

### 6. Controller mistakes, recorded because they are the same class

- I passed a reviewer's measurement ("319 of 8000 mid-conversation reads
  returned 422") to an implementer. It became a permanent comment and did not
  reproduce at the shape described: 0 of 8000 on immediate reads, 280 of 43,626
  when polling. **Do not hand an agent a measurement it did not take and let it
  be written down as fact.**
- I wrote a dated spec ruling asserting that a founded bank "cannot pay", with
  two supporting reasons. The whole-branch probe falsified all three: an
  arranged overdraft or a loan disbursement funds a founded bank's customer, and
  `FoundBankTx` gives the bank internal accounts in every asset so the refusal
  never fires.
- I escalated several Minor findings into fix rounds. The process says park them
  in the ledger for the final review to triage. Doing that would have collapsed
  three or four rounds.

## What the depth bought, so it is not cut in the wrong place

Four money defects, none visible in a clean diff, all found by building a probe
and running it:

- `AdmitMemberTx`'s impostor refusal fired on `store/mem` and not on `store/pg`
  (0/60 vs 54/60), so the roster would name whichever transaction committed
  last — routing to the impostor.
- `OpenSettlementAccountTx` lost a settlement account on `store/pg` (60/60),
  orphaning a reserve account in the settlement agent's own book.
- A replayed `acmt.010` overwrote a member bank's settlement reference, leaving
  its own row disagreeing with the central bank's, with `DepositTx` reading the
  bank's row.
- A founded bank could pay and be paid, and either stranded **every other
  member's payments** in the cycle, with no remedy but admitting the bank.

The rule that produced all four: **reading a diff cannot find these.** Keep the
probes; make the cheap classes mechanical.

## The short version

1. Plans specify behaviour and tests, never comment text.
2. One canonical statement per domain fact; every other layer links to it.
3. `storetest` drives `payment`'s acts, with a warm pool.
4. Derive guards from the other side's refusal list; enumerate the inputs that
   make each guard vacuous.
5. Verify a correction's explanation as hard as the correction.
6. Minor findings go to the ledger, not into a fix round.
