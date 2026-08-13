# Design — a scheduled business day, and a clock that runs

Based on `main` at `94e5996`. It needs
[scenarios and a blank slate](2026-08-13-scenarios-and-a-blank-slate-design.md)
to have landed, for a reason given under *What a fresh deployment starts at*.

It amends the rule that the deployment owns the clock and extends
the rule that a business day is a declared sequence. Neither is
reversed, and saying which parts survive is half of what this record is for.

## Three asks, and they are one missing concept

Three things were wanted, separately:

1. **Intraday clearing windows.** SEPA runs several settlement cycles a business
   day; this engine runs one.
2. **A clock that moves on its own**, at an accelerated rate, so a deployment can
   be left running.
3. **Discrete advances that trigger everything that would have happened in
   between.**

The codebase has already written down what all three need, twice and
independently. README's transport section:

> There is **one cut-off in a business day**, where SEPA runs several settlement
> cycles. Adding more is a loop over a list of times, and the calendar has to
> exist before a time of day within it means anything. **There is no time of day
> within a settlement day at all.**

And the rule that the deployment owns the clock, under *Not decided
here*, in the same words.

So: one concept, three consequences.

## The design

**A business day is a schedule of timed phases; a cursor says how far it has
run; and advancing anything at all is `RunUntil(t)`.**

### The schedule

`beforeClock` and `afterClock` gain a time of day. The declaration stays exactly
what the rule that a business day is a declared sequence made it —
a list of named phases in the order they run, with `settlementOnly` on each and
`only(...)` deriving every subset by naming phases rather than writing an order.
What changes is that each entry carries **when**, and that a day may declare the
clearing phases more than once.

The clock move stops being a pivot in a function and becomes the last event of a
day. That is the one structural change to the rule that a business day is a declared sequence, and it is what lets a single
runner cross a date boundary without special-casing it.

### The cursor, and why it has to be durable

`RunUntil(t)` runs **every scheduled event whose instant is at or before `t` and
after the cursor**, in order, then leaves the cursor at the last one it
completed.

`AdvanceDay` becomes `RunUntil(end of today)` — a caller, not a second path.
`CarryToClearing` becomes `RunUntil(this morning's clearing)`. The derived
sequences the suites use stay derived.

The cursor is **persisted**, and this is not optional. `business-date` today
holds one RFC3339 line and is written through a temp-file-and-rename; it becomes
a small record holding the instant **and the last completed event**. Without it a
process killed part-way through a catch-up re-runs phases it already ran, and
with a year of accelerated time behind it that is not a theoretical window.

### Phase re-entrancy is the risk, and it is stated rather than hoped for

A catch-up may re-run a phase after a crash, and today nothing says whether that
is safe. Some phases are guarded by accident of their own design —
`RunCutoff` empties the hub, `csm.Work` walks a pending list, and the end-of-day
advancement guards (`deposit/register.go:1183`, `lending/accrual.go:57`) make a
re-close a no-op. None of that is written down as a rule, and the roadmap already
carries a live defect in exactly this area: *the seed closes a business day the
deployment has not reached*, whose only reason for being harmless is those same
guards.

**Every phase gets a stated answer and a test.** Running a phase twice from the
same cursor position produces the same books as running it once, or the phase is
fixed. This is the largest single piece of work in the sub-project and it should
be budgeted as such.

### Intraday windows

With a time of day, a scheme carries a **timetable**: the instants at which its
open cycle is cut off. The day declares the clearing phases once per window.

**The `cycles` table does not change**, and that is worth stating because it
looks like it should. One *open* cycle per scheme at a time is still true — the
cut-offs are simply more frequent — so `cycles_open_idx ... WHERE status = 0`
keyed by scheme stays correct, and `opened_at` / `closed_at` already carry the
instants. What changes is how many rows a day produces.

`Scheme.SettlementDelay()` and `SettlementModel()` acquire their first real
consumers here; today `SettlementModel()` is read by one DTO field and nothing
branches on it.

Settle-before-release is restated **per window rather
than per day**. Same rule, finer grain, and it is where a defect would hide: a
release belonging to window 2 must not go out against window 1's settlement.

### The driver, and the seam that keeps the tests honest

One goroutine, in `cmd/server`'s `main` alone, ticking `RunUntil(businessNow())`
where business time advances at a configured multiple of wall time.

**Nothing below `cmd/server` starts it, and no test starts it.** The suites call
`RunUntil` directly, exactly as they call `AdvanceDay` today. This is a rule and
not a habit: the rule that the deployment owns the clock's cleanest consequence is that there is *"no quiescence
loop, no deadline, no poll and no non-deterministic assertion anywhere"*, and the
first test that starts the driver ends that property permanently.

### What a fresh deployment starts at

This is why the blank slate has to land first.

`seed.BaseDate` is `2025-09-15`, roughly a year behind wall time. A persistent
deployment booting with a running clock would catch up some 330 business days of
phases and every bank's end of day — and the interest recompute window is
**unbounded by design**, opening at account opening and never resetting, so the
catch-up is quadratic in days rather than linear. There is no version of a
real-time clock that survives that.

With a blank slate there is no history to anchor, so the base state is built at
whatever instant the deployment starts and the question dissolves. The scenarios
that want history advance the clock themselves, at accelerated rate, and pay for
exactly the days they use.

## What is amended, and what survives

**The rule that the deployment owns the clock is amended, not
reversed.** Its core is untouched: the deployment owns one clock, `time.Now`
appears nowhere below it, the date lives in no institution's database, and
advancing is not moving a number. Two things change, and both are recorded here
because a later reader will otherwise find a rejected alternative in the running
code:

- *"The system has no background goroutines at all"* narrows to **none below
  `cmd/server`**.
- Two rejected alternatives return in modified form. **A background ticker** was
  refused because *"a suite running against a fake clock cannot also run against
  a real one"* — which stays true, and is why the driver is excluded from the
  test path by construction rather than by discipline. **A wall clock with a
  seeded offset** was refused because it *"leaves no roll event"* — which no
  longer follows: the roll is a scheduled event like every other, and it is
  emitted whether a ticker or an operator triggered it.

**The rule that a business day is a declared sequence extends.** A
business day is a declared sequence of **timed** phases. Everything it ruled
holds: a phase returns what it could not do, the runner is the only thing that
writes a problem, `settlementOnly` is answered at declaration, and a subset is
derived by naming phases. `TestTheDayRunsItsPhasesInOrder` and
`TestEverySequenceRunsThePhasesInTheDaysOwnOrder` are the guards this refactor is
performed under.

**Settle-before-release is restated at a finer
grain**, per window.

## What it costs

**Determinism is now a structural property rather than a free one.** Today it is
free: nothing runs unless a request runs it. Afterwards it is preserved by one
seam, and the seam has to be defended in review.

**A day is no longer one call.** `POST /clock/day` currently runs a whole day
synchronously and returns a report of it. With windows it still can, but the
report grows and a caller who advances several days gets a much larger object.
The message log this pairs with is the better answer to "what happened", and the
report should stop being the place a reader looks.

**Catch-up after a long outage is unbounded work in one request.** A deployment
left down for a week comes back and runs a week. Nothing here caps it, and the
cap — if one is wanted — is a decision with a domain consequence: a business day
that is skipped is not the same as one that is run.

## What this does not do

- **No per-institution clocks.** One deployment, one business date. the rule that the deployment owns the clock's
  rejection of a `business_date` row in any institution's database stands.
- **No threading of the date into the accounting layers.** The roadmap's *`Day`
  type in `ledger`* item is still the item, and putting a compiler-guided rename
  across four packages inside a clock refactor is the same mistake the rule that the deployment owns the clock
  declined to make inside a transport swap.
- **No time zone.** The business instant is UTC, as it is now.
- **Nothing about gross settlement.** Instant payments are §2, and a scheme
  settling per payment is a different branch from a scheme cutting off more
  often.

## Phasing

Each phase is separately shippable and the order is not negotiable.

1. **Time of day, the schedule, and the cursor.** Still operator-triggered, still
   one cut-off a day, no behaviour change at all. `AdvanceDay` becomes
   `RunUntil`. The existing phase-order tests are the guard, and a pure refactor
   is what they can guard.
2. **The re-entrancy audit.** Every phase, an answer and a test. Independent of
   phase 3 and worth landing before anything can re-run a phase for real.
3. **Intraday windows.** A scheme timetable, several cut-offs a day, settle-before-release
   restated per window. This is where the documentation carry lands.
4. **The driver.** The ticker, the acceleration factor, and the flag that turns
   it on. Smallest of the four, and only safe last.

Doing 4 before 2 is the trap the whole phasing exists to prevent: a ticker over
an unaudited phase set with no cursor.

## Documentation layers

Phase 3 is a domain change and moves all of them. `README.md`'s transport
section, its explicit *one cut-off in a business day* concession, and the
Persistence section; `CONTEXT.md`, which gains **window** and has to rule on
window versus cycle versus cut-off; `hint-content.ts`; the quiz chapters on
clearing cycles and settlement; and the clearing house's cycles screen, whose
`accepting` badge becomes considerably more useful once there are several a day.

Phases 1, 2 and 4 move no learner-facing layer: a cursor is machinery, and an
accelerated clock is a property of this app rather than of banking.

## Verification

`go test ./...` at every phase, plus:

- **The phase-order tests survive phase 1 unchanged.** If they need editing, the
  refactor changed behaviour and is not the refactor it claims to be.
- **A re-run test per phase**, phase 2's deliverable.
- **A crash-and-resume test**: a cursor left mid-day, a fresh process, and the
  books equal to the uninterrupted run. This is the one that justifies persisting
  the cursor, and it needs a file-backed store — the ephemeral one hides
  read-then-write defects, which `TestTheRetryBudgetOutlastsASlowWriter` already
  records.
- **`payment/recon` clean across a window boundary**, phase 3's acceptance test:
  two windows settled in one day, and no institution's books disagreeing with
  another's.
- **No test starts the driver.** Enforced by the driver living in `main` and
  nothing exporting it.
