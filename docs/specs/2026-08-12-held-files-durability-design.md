# Design — what an institution is holding when the process ends

Branch `spec/held-files-durability`, based on `main` at `cd5865c`.

A reader opened the central bank console on a freshly seeded deployment and found
three cycles sitting under **Settlement instructions**, `Closed`, neither the
oldest nor the newest. They were the seed's, they had been there since the build,
and no business day would ever move them. The one control the app offers —
*Instruct again* on the clearing house's cycle page — settled them at the
settlement agent and delivered nothing: reserves final, two payees' banks still
reading `Initiated`, the money standing in those banks' clearing suspense with no
act in this system able to move it.

The refusal that makes that unreachable has landed (`ClearingHouse.unhandable`,
`payment.ErrCycleNotReleasable`). This is the sub-project behind it: the state
that made it possible, which is not one map and not the seed's fault alone.

## The defect, stated once

**An institution's obligations live in its process memory, and a process ends.**

Between one business day and the next, the clearing house is holding files it owes
to banks, a bank is holding instructions it owes to the network, and the EBICS
host is holding queues neither of them has collected. None of it is in any of the
N+2 databases. A restart is a bank run in the literal sense: everything owed and
not yet handed over stops existing, while the money that moved against it stays
moved.

## What is in memory, and what each loss costs

| Held | Where | Lost on restart | What it costs |
| --- | --- | --- | --- |
| Output shares, close → settle | ~~`ClearingHouse.output`~~ `held_files` | ~~every receiving bank's share of a cut-off~~ nothing | **closed by phase 2** |
| A `pacs.004` uploaded, answer pending | ~~`ClearingHouse.held`~~ `held_returns` | ~~the return's second hop~~ nothing | **closed by phase 2** |
| Instructions with committed debtor legs | `Bank.hub` | the file that was going to be built | customers debited, money in clearing suspense, no file ever uploaded |
| Files released and not yet collected | `ebics.Server.subs[…].queue` | a settled cut-off's output files | identical to row 1, on the far side of the release the guard permits |
| Every order ever uploaded | `ebics.Server.orders` | the order log | HAC answers nothing; the duplicate check at ingestion loses its memory |

Rows 1–4 are the same loss wearing four hats: an obligation without a record.
Row 5 is an audit trail, which is a different kind of bad.

**Rows 1 and 2 are closed; rows 3–5 are not.** The sharpest thing to understand
before choosing scope was that the guard could close row 1 and could never close
row 4, and it stayed true when the tables landed: the shares survive a restart
now, and a restart *after* settlement and before the receiving banks collect
still loses the files with the reserves already moved, with nothing refusing
anything — the shares were present, the release happened, the queue is simply
gone. Phase 2 moved the cliff a few seconds later in the same day. Phase 3 is
what removes it.

## Why the share cannot just be a rendered file

`csm/0001_init.sql`'s `cycles` statement already argues this, and it is the
constraint the table has to be designed around:

> Those shares are POSITIONS in the submitters' documents, in memory, keyed by
> cycle — so that a payment an operator rejects out of an open cycle is cut out of
> the share when the rest of it settles, which a rendered file could not do.

A share is built at ingestion (`takeRecorded`) and cut again at release
(`releaseFiles`), against what `SettleAtCSM` actually settled. Between those two
moments a payment can leave the cycle. So what is persisted is the **submitting
bank's own document plus the positions within it**, not the file that will
eventually be handed over — and `pendingFile.part`, today a closure over the
parsed document, becomes a function of a stored one.

## The shape

Two tables in the clearing house's schema, named where the absence is currently
argued:

```sql
CREATE TABLE held_files (
    -- The cycle whose settlement releases this share. No foreign key to cycles:
    -- see bank/0001_init.sql on absent parents.
    cycle_id     TEXT    NOT NULL,
    seq          INTEGER NOT NULL,  -- build order; shares release in it
    destination  TEXT    NOT NULL,  -- the receiving bank's BIC
    msg_def_idr  TEXT    NOT NULL,
    -- The SUBMITTER's document, unrendered and uncut, because the share is cut
    -- again at release against what actually settled.
    document     BLOB    NOT NULL,
    PRIMARY KEY (cycle_id, seq)
) STRICT;

CREATE TABLE held_file_transactions (
    cycle_id    TEXT    NOT NULL,
    seq         INTEGER NOT NULL,
    position    INTEGER NOT NULL,  -- index within document
    payment_id  TEXT    NOT NULL,
    PRIMARY KEY (cycle_id, seq, position)
) STRICT;
```

`held_returns` is the same shape one row wide: the `pacs.004` document, the
returning bank, keyed by the payment it names.

The reasoning goes **inside the parentheses**, per the repo rule: SQLite keeps a
statement's text in `sqlite_master` and drops what sits above it.

## The seed is a separate fix, and it is the cheap one

This is worth stating flatly because the roadmap currently implies the opposite:
**persisting shares does not reach the seed's three cycles, and the seed's fix
does not need persistence.**

The seed played every institution and uploaded nothing, so no share was ever
built for a seeded payment — there was nothing for a table to have kept. And the
shares it *would* build live in the same process that then serves the app, so an
in-memory map is enough for them to survive until the first advance.

Three candidate shapes for the seed's in-flight fixture:

**S1 — in-flight is `Initiated`, in the submitting bank's hub.** The seed submits
through `Deployment.Submit` and stops. No share is needed because no file has been
uploaded; the first advance carries, clears, settles and delivers it correctly.
Cheapest, and the state the *payment whereabouts* sub-project already taught the
app to render. Costs the dataset both `Accepted` and `Cleared`.

**S2 — in-flight is `Accepted`, in the open cycle, uploaded for real.** The seed
needs one new act on `seed.Deployment` — the file-moving phases of a day without
the settling ones — after which the payments sit in the open cycle with shares
behind them. The first advance settles and delivers them properly, which is
exactly what the current seed's own comment says it could not risk. Keeps
`Accepted` where a reader meets it. **Chosen; see _What phase 1 actually cost_.**

**S3 — keep closed-and-unsettled cycles, with shares.** Requires holding a day
back between phases 3 and 4, which is a seam shaped like a debugger. It also
re-manufactures a state that in a running deployment only follows an operator
error or a refusal, and hands it to every new reader as the normal case. Rejected.

Under S2 the guard never fires in the demo, and the three dead cycles stop being
shipped.

## What phase 1 actually cost

S2 landed, and four things about it were not visible from the design above. Each
is a fact a later phase has to work with rather than a regret.

**It is TWO acts on `seed.Deployment`, not one.** A share is built from an
uploaded file, so the payment has to reach a bank's hub before anything can carry
it — which is `Submit`. `CarryToClearing` is the second, and it runs three
phases: every member's cut-off, the clearing house's work, and each member
collecting the answers. That last one is phase 6 of a day taken out of turn, and
it has to be there: without it a submitting bank sits at `Initiated` where a
carried payment reaches `Accepted`.

**`cmd/server`'s startup order inverted.** `main` seeded and then planned the
listeners, because the plan is the set of banks and seeding is what founds them.
A seed that uploads needs the clearing house answering on its own address first,
so `plan` split into `hostPlan` and `bankPlan`: bind the two institutions, seed,
bind the banks. Nothing is ever pushed at a member bank, which is what makes that
order available at all.

**The rejected payment had to become a push.** The seed rejected a collection out
of its cycle, and the assertion behind it was that the payer's bank reversed the
leg it had posted. On a pull that bank posts at `AcceptInbound`, which under
settle-before-release happens after finality — so a collection rejected before it
is one nobody has posted anything for, and the reversal had nothing to find. The
fixture now rejects a credit transfer (`SCT-021`), where the submitter IS the
payer's bank and the reversal is real. The dataset gained a payment; the rejected
collection's mandate story is unchanged, since `SDD-011` still uses it.

**`Cleared` left the dataset.** It was the status of the five payments in the
dead cycles. Nothing but a settlement moves a `Cleared` payment, so shipping one
is shipping a payment an operator has to rescue; it is still reachable in a
running deployment by closing a cycle by hand, which is where `cmd/server`
measures it. The seed's in-flight payments are `Accepted` and the first advance
carries all six to `Settled`.

Two tests inverted rather than moved: `TestTheBuildLeavesNoPaymentInAnOpenCycle`
became `TestEveryPaymentInAnOpenCycleWasUploaded`, and
`TestACutOffThatCouldNotBeReleasedIsRefusedBeforeTheReservesMove` now builds its
subject — it drops one cycle's shares to model the restart, because the seed no
longer hands it a broken cut-off for free.

## What phase 2 actually cost

The three tables landed close to the shape above. Five things about them were not
visible from it.

**The share carries the whole uploaded ENVELOPE, and `msg_def_idr` went away.**
`iso20022.Marshal` takes an envelope, so there is no way to render a bare
document and nothing to put in a `document BLOB` that could be read back. What is
stored is the submitting bank's file exactly as it arrived; the header on it is
that bank's and is dropped at release, which builds this institution's own. The
definition column went with it — it is on the stored header AND on the document,
and the codec refuses a file where those disagree, so a third copy could only
drift.

**One file addressed to three banks is stored three times.** The share is the
unit that is built, cut and released, so normalising the bytes out from under it
would need a file identity nothing in the conversation supplies. It is argued in
the statement rather than fixed.

**The shares are written in a SECOND unit of work, after the accepts.** A share
names the cut-off that will release it and which cut-off that is is the
acceptance's answer, so the two cannot be one transaction without inverting the
whole of `takeRecorded`. That decides how a process ending between them fails,
and the order chosen is the one `unhandable` already refuses: payments in a cycle
with no share behind them. The other order fails silently.

**The lock went.** `outputMu` existed because a business day and an HTTP request
both reached the map. A store transaction orders them now, and `ClearingHouse`
has no state at all.

**Two tests inverted rather than moved.**
`TestACutOffThatCouldNotBeReleasedIsRefusedBeforeTheReservesMove` used to model a
restart by emptying the map; a restart no longer produces that state, so it now
composes the crash-between-two-units-of-work case by dropping one cut-off's
shares. `TestTheClearingHousesOtherCallersLeaveTheHeldReturnsAlone` was about an
unlocked field and is now about a pending obligation no cut-off may sweep — the
same three routes, a different reason.

The ruling is [ADR-0003](../adr/0003-an-institutions-obligations-live-in-its-database.md),
which is what a later reader needs before moving any of this back into memory.

## Phasing

1. **The seed (S2).** ~~Independent of everything below and fixes what the reader
   actually hit.~~ **Done.** Two acts on `seed.Deployment`, the build's closing
   section rewritten, `main`'s startup order split around the seed, and the suite
   re-baselined against the new statuses.
2. **`held_files` + `held_returns`.** ~~Schema, store methods, `takeRecorded` and
   `releaseFiles` and `relayReturn` reading and writing them.~~ **Done.** Three
   tables, six `payment.Tx` methods behind six on `csmOps`, and the restart test.
   `unhandable` stays — it is now the guard on a defect that should no longer
   occur, which is where a guard belongs. See _What phase 2 actually cost_.
3. **The EBICS host.** Queues and the order log, without which 1 and 2 leave the
   cliff standing on the far side of the release. Largest, and the one that turns
   `DATABASE_URL` into a deployment that genuinely resumes.

Each phase is shippable and each leaves the system better than it found it. What
must not happen is 2 alone, announced as "the clearing house is durable now".

## What this does not do

`Bank.hub` (row 3) is one member bank's own database and its own sub-project. It
belongs with phase 3 by kind and is separable by institution; naming it here is
what stops phase 2 reading as the whole story.

## How it is proved

- A restart test: build a deployment against a file-backed store, close a cycle,
  drop the process, reopen, settle, and assert every receiving bank was handed
  its instructions. This is the only test that can fail for the reason this
  sub-project exists, and it is
  `cmd/server`'s `TestACutOffSettledAfterARestartStillReachesEveryReceivingBank`.
  The operator has to **re-instruct** after the restart, because the `pacs.009`
  the cut-off uploaded was sitting in the settlement agent's download queue and
  that queue is phase 3's. What the test proves is that the clearing house's own
  obligations survive, not that the deployment resumes.
- `payment/recon`'s `partiesHoldTheirCopy` over the same deployment, which finds
  in the books what the test above asserts on the wire.
- The seed's own suite, re-baselined, plus `TestTheSeededNetworkReconciles` —
  which under S2 reports an unreconciled position for the banks holding in-flight
  payments and nothing else. It does, and only for the banks that PUSHED one: a
  collection in flight has taken nobody's money, because the bank that posts on a
  pull has not heard of it yet.

## Documentation this moves

Done with phase 2. The schema comment quoted above stopped being an argument for
an absence and became a pointer to three tables; `README.md`'s *Persistence*
section lost the clearing house from its "not stored" table and gained the pair
of tables in its three-schemas paragraph; quiz chapter 15's "which of these get a
table" question gained a share as a correct answer, and the `bulk-file` hint
gained the paragraph about what waits between the sort and the release.
`CONTEXT.md`'s *Share* and *Held return* entries no longer say "in memory", and
the *Payment hub* entry now says it is the last one that is.
[ADR-0003](../adr/0003-an-institutions-obligations-live-in-its-database.md) is
the ruling, and ADR-0002's *What it costs* points at it instead of recording the
defect.
