# Design — a cursor for the business day

Based on `main` at `2ba8650`. It lands the cursor
[the scheduled business day](2026-08-13-a-scheduled-business-day-design.md)
specifies under *The cursor, and why it has to be durable*, ahead of the rest of
that sub-project, and it retires the cost
[the message log](2026-08-13-the-message-log-design.md) records under *What it
costs is that stepping and advancing overlap*.

Nothing here needs the blank slate that record's other phases wait on: a cursor
is machinery, it moves no learner-facing layer, and it does not touch the phase
declaration.

## The defect, stated once

**Nothing records how far a business day has got.**

`RunPhase` runs the phase it is named whatever has already happened, and
`AdvanceDay` runs the whole of `beforeClock` whatever has already happened. So an
operator who steps the clearing and then advances the day runs the clearing
twice, and neither the engine nor the reader can say which phases of today are
behind them. The clock file holds one RFC3339 line and there is nowhere else to
look: the message log is deliberately barred from carrying a phase, because a
business day's phases are the deployment's declared sequence and an institution
does not know a deployment exists (`bank/0001_init.sql`, the `messages` table).

What makes this survivable today rather than broken is that every phase is
drain-shaped by accident of its own design — `RunCutoff` empties the hub,
`csm.Work`, `csm.Collect` and `cb.Work` walk pending lists, `CloseOpenCycles`
finds nothing open — and that end of day is guarded by `LastAccrualDate`. That
guard is the precedent this record follows: how far the work has got is
RECORDED, and the record is what makes a second run a no-op.

## The design

**The clock's record gains a marker naming how far the current day has got.
`AdvanceDay` runs the phases after it, and completing a phase in turn moves it.**

### The marker is on the clock's own record, and it is opaque there

`business-date` becomes one JSON line holding the instant and the marker:

```json
{"instant":"2026-05-18T09:00:00Z","reached":"clearing"}
```

**One record and one write**, because a second file would be a second write and a
process killed between them would leave a date and a marker that disagree — the
one failure the temp-file-and-rename exists to rule out.

`calendar` does not interpret the marker. It is a string the deployment records
and reads back, and the calendar's own claim stays what it was: which days money
can move on, and what day this deployment thinks it is. A phase is
`cmd/server`'s concept, and putting `phaseID` in `calendar` would put the
deployment's declared sequence inside the package every institution reads the
date from.

**A file holding a bare RFC3339 line is read as an instant nothing has run on**,
so a directory written by an earlier process opens rather than failing. A file
holding neither is still refused: resetting to the anchor would put the clock
behind books that already hold later entries.

### The marker moves only in turn

`RunPhase` records the phase it ran **only when that phase is the one the day
would have run next**. A phase run out of turn runs, and moves nothing.

This is the whole safety argument. A marker that jumped to whatever was last
stepped would let `AdvanceDay` skip the phases *before* it: stepping `release`
alone would leave a day that never settled and then declined to release. Out of
turn, the phase is an extra run the operator asked for — which is what a door has
always been — and the day still runs it in its place.

"Next" is read from the day's EFFECTIVE sequence, which is `beforeClock` narrowed
by `settlementOnly` for the date the clock stands on. So on a day the scheme is
shut, `end of day` is the first phase and stepping `clearing` moves nothing —
consistent with `settlementOnly` staying reported rather than enforced, because
the phase still runs.

### It covers the phases before the roll, and only those

`afterClock` — today just `open cycles` — runs on the date the roll landed on, so
it cannot be progress within the date being left. The marker therefore ranges
over `beforeClock`, `Advance` clears it as part of the same write that moves the
date, and `AdvanceDay` always runs `afterClock`. An operator who steps
`open cycles` by hand and then advances runs it twice; `OpenCycles` is a no-op
when nothing is open, which is what the phase's own comment already says.

### A derived sequence moves nothing

`CarryToClearing`, `Subscribe` and the sequences the suites compose run phases
without recording any, and `runPhases` is untouched. A door is an OPERATOR
stepping the declared day; a derived sequence is a caller composing its own run
out of the same phases, and it has already decided what it wants. Recording from
one would make `carryToClearing` — which holds a narrowed collection the day does
not declare — claim progress through a sequence it is not walking.

### What a failed write does

`Reach` writes a file, so it can fail. When it does, `AdvanceDay` stops where it
is, does not move the clock, and returns the error beside the report of what it
moved — the same shape a failed `Advance` already has. The marker then names one
phase less than actually ran, so the resumed day re-runs that one: **the marker
can only ever under-report**, which is the direction the drain-shaped phases
already tolerate.

### The surface

- `PhaseDTO` gains `completed` — whether the day the clock stands on has run this
  phase. Not `ran`: `PhaseReportDTO.ran` is the DATE a phase ran on, and one
  object carrying both under one name reads as a contradiction.
- `DayReportDTO` gains `phases` — the keys this call ran, in order. A report is
  what *this call* did, which is the rule the doors were built on, and a day that
  skipped four phases had no way to say so.
- `GET /clock` is unchanged. The phase listing is where progress is read, because
  progress is per-phase and a count would be a second way to derive it.
- No new SSE event name is strictly needed — the watcher invalidates every query
  on any movement — but `end of day` moves no file, so a phase completing is
  pushed as `phase`. Otherwise the one phase that moves nothing is the one whose
  progress a second tab never sees.

## What this does not do

- **No time of day, and no `RunUntil`.** The phases stay untimed and the day
  stays a declared sequence. `AdvanceDay` becoming a caller of `RunUntil(t)`, the
  intraday windows and the clock that runs are the scheduled business day's, and
  when they land the marker becomes a position in a schedule rather than a
  position in a list.
- **No re-entrancy audit.** Every phase's second run being a no-op stays an
  accident of each phase's own design, unstated and untested. The cursor makes
  the accident matter less — the day no longer re-runs what was stepped — and the
  audit stays where the roadmap has it. This record's own failed-write path
  depends on it, which is worth stating plainly rather than hiding.
- **The seed still closes a day the deployment has not reached.** A seed that set
  the marker would fix the roadmap's filed defect, and it moves interest figures,
  so it stays the domain decision it is filed as.
- **Nothing refuses a door.** A phase can still be stepped out of turn, twice, or
  on a day the scheme is shut. The marker records what happened; it is not a
  gate.

## Verification

- `calendar`: the record round-trips; a bare RFC3339 line reads as an instant
  with nothing reached; a file holding neither is still refused; `Advance` and
  `Rewind` clear the marker, and a reopened directory sees the clearing.
- `cmd/server`: stepping the day's first phases and then advancing runs only the
  rest, read off `DayReport.Phases`; a phase stepped out of turn leaves the
  marker where it was and the day runs it in its place; a day the scheme is shut
  begins at `end of day`; the listing marks exactly the phases that ran.
- A restart: a file-backed deployment steps four phases, the process ends, and
  the next one advances the remaining seven. `restartable.boot` opens the clock
  over the directory it opens the databases in, which is what makes the harness
  what its name claims.

## Documentation layers

`README.md`'s transport section and `CLAUDE.md` describe the business day as a
declared sequence; both stay true. The learner-facing layers name no phase
door and are untouched: how far a day has got is machinery, and the domain claim
they carry — that a day has an order, and that settlement precedes release — is
the one the cursor protects.

## Alternatives rejected

- **A set of completed phases rather than a position.** It records an
  out-of-order step truthfully, and then `AdvanceDay` skips it: a release that
  ran before its settlement would be skipped after the settlement, which is the
  one ordering the whole system is built to preserve. A position cannot express
  the out-of-order run, and that is the point.
- **An in-memory marker on the `Deployment`.** Two thirds of the change for none
  of the durability, and the spec's argument holds at this size too: a process
  killed part-way through a day is exactly when the answer matters.
- **A `phase` column on `messages`.** The schema already refuses it, in writing,
  and it would make every institution record something only the deployment knows.
- **A second file beside `business-date`.** No atomicity with the date.
