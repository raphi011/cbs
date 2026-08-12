# ADR-0005: A business day is a declared sequence, and a subset is derived from it

**Status:** accepted
**Date:** 2026-08-12
**From:** architecture review — the business day had no type

## Context

The order of a business day was **statement order inside one function**
(`Deployment.clear`), and three other places re-wrote a subset of that order by
hand: `CarryToClearing` for the seed, `workThrough` for the flow suite, and one
restart test composing the settlement and the release. Two of the four also
restated, in prose, the ordering argument the first one made.

Nothing failed if the order moved. `day_test.go` asserted no sequence, and the
suites that would notice — settlement, returns, the chain tests — assert on
balances and statuses, which a reordering can preserve right up until the day a
phase is inserted between two that were adjacent for a reason.
[ADR-0002](0002-settle-before-release.md)'s ruling was in that position: restated
in fourteen files and checked by none.

Two further costs came with it. A phase's problems were collected by the caller
wrapping each call in `journal.problem(...)`, and Go lets an expression statement
discard a `[]Problem` — so a phase added without the wrapper compiled and lost
everything it could not do. And which phases a weekend still runs was readable
only from a doc comment.

## Decision

**A business day is two declared lists of named phases with the clock move
between them, and every other sequence is derived from those lists by naming
phases — never by writing an order.**

- `beforeClock` and `afterClock` declare the phases in the order they run. The
  clock move is the **pivot**, not a phase: it returns the next date, which the
  report carries, and an error the caller is told about rather than a line in a
  report.
- A phase **returns** what it could not do. The runner is the only thing that
  writes a problem to the journal, so a phase cannot forget to — it has no
  journal to reach and no way to drop what it hands back. Whether those problems
  are journalled or answered to a caller is the runner's decision.
- `settlementOnly` on the phase says whether a day nothing settles on runs it.
  Only a full day asks; a derived sequence names its phases outright, its caller
  having already decided.
- `only(list, ids...)` selects by name and walks the declared list, so a subset
  runs phases in the day's own relative order or does not run. An id naming no
  phase in the list panics, and every caller is a package-level variable, so it
  panics at init or never.

**An operator's cycle close is an intervention between phases, not a phase.** It
closes one cycle by id through the institution; the day's clearing-house cut-off
closes every open one. A sequence that wants it composes two derived runs around
it rather than splicing it in.

## Consequences

**The ordering is assertable.** `TestTheDayRunsItsPhasesInOrder` is the golden
sequence and fails on an insertion, a deletion and a reordering alike.
`TestEverySequenceRunsThePhasesInTheDaysOwnOrder` holds every subset to the day's
relative order, including the two that live in test files — which is where the
order was previously free to drift, because a fixture that re-writes the list can
prove something about a deployment nobody could deploy.

**What a weekend runs is readable off the declaration.** Exactly one phase before
the clock is not `settlementOnly` — every bank's end of day, because interest
accrues over a weekend. Adding a phase now forces an answer to "does this run on
a Saturday?", which was previously a question you could fail to ask.

**A dropped problem is unrepresentable.** `runPhases` is the only caller of
`phase.run` and the only place a phase's problems are collected.

**`CarryToClearing` still drains the journal, for a different reason.** Its
phases hand their problems back rather than journalling them, so what it drains
is the **files and outcomes** the institutions record from inside those phases. A
first day's report carrying six uploads and nothing else would describe the
build's last minute and none of the rest of it.

## What it costs

**The collection is one phase, not three.** A day's collection visits each bank
in turn and takes all three of its queues before the next bank takes any. Three
phases would take one queue across every bank before starting the next, which
reverses that nesting — and the nesting is observable, being the order files move
in. The guarantee the collection exists to keep is satisfied either way, because
it is about one bank's mirror leg against that same bank's creditor legs. So the
nesting is not correctness, changing it is a domain decision, and it did not ride
along here.

**One step in the package is not a phase of a day.** Carrying to clearing needs
the collection narrowed to the one queue that holds anything before a cycle
settles, and with the collection undivided there is no phase to name. It is
declared beside the day as `collectClearingHouseOnly` and appended rather than
selected. It should stay the only one: each addition is a place a sequence
stopped being derived from the day, and the phase test reads it as the phase it
narrows so the subsequence check still means something.

## Alternatives rejected

**Parameterised phases, or splicing a foreign act into a sequence.** It would let
carrying to clearing pass its own collection arguments and let the settle helpers
insert the operator's close, at the price of making the list a script engine —
and a parameter is exactly the freedom that lets two callers of one phase disagree
about what it does.

**Arbitrary selection by the caller instead of named sequences.** A test could
then assemble any subset, which reintroduces under a nicer interface the drift
this removes. The rejected option S3 in the held-files durability design named
the same hazard from the other end: a seam that exists to hold a day open
mid-run is *"a seam shaped like a debugger"*.

**Leaving it as statement order and adding a test that asserts outcomes.** The
outcomes already have tests. What had none was the order, and an outcome test
cannot fail for a reordering that still reconciles.
