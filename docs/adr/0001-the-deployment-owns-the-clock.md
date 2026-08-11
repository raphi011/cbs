# ADR-0001: The deployment owns the clock

**Status:** accepted
**Date:** 2026-08-11
**Sub-project:** 21 — [EBICS and the business day](../../specs/2026-08-11-ebics-and-the-business-day-design.md)

## Context

Every layer in this system already took a clock: `ledger.Book`, `deposit.Register`,
`payment.Network` and `store/sqlite.Set` each hold a `func() time.Time`. The seed
froze one, advanced it a day at a time to build a scenario with history, and then
called `goLive()` — after which everything a user did was stamped with wall time.

So one screen showed a sample dataset dated about a year in the past beside
anything the user had just done, dated today, and nothing on the screen said which
was which. A user who cannot tell a seeded date from a live one reads every date
on every screen wrong.

Separately, `payment/ageing.go` priced a gap it could not close:

> The rulebook figure is THREE BANKING BUSINESS DAYS (EPC SCT, D+3), and this is
> calendar days because there is no business date in this system — no roll event,
> no calendar and no holiday handling.

Both are the same missing thing. There was no answer to "what day does this
deployment think it is", so every layer invented one from `time.Now`.

## Decision

**The deployment owns a single clock, and advancing it runs a business day.**

Three parts, and the third is what makes the first two more than a tidy-up.

1. **One clock, and `time.Now` appears nowhere in the running system.** The
   stores, the networks and every message header read the same instant. `goLive`
   is deleted; `rewind` stays, because a reset must replay the same timeline.

2. **It lives in no institution's database.** A business date is a fact about the
   *deployment*. A bank's database holding one would be that bank's opinion about
   what day it is, and two banks could then disagree — a state no clearing system
   has and this one should not be able to reach. It is a small file beside the
   databases in the `-database` directory, read at boot and written on each
   advance. With no directory it starts at the seed's base date every time, which
   is the same bargain the ephemeral store makes and is what keeps
   `go test ./...` free of setup.

3. **Advancing it is not moving a number.** `POST /clock/day` runs the whole
   business day — every bank's cut-off, the clearing house's validation and
   netting, settlement, release, every bank's collection and every bank's end of
   day — synchronously, on one goroutine, and returns when the day is done.

The calendar it advances against is **TARGET's**: weekends plus six named days,
two of which are computed from Easter. A day the scheme is shut still advances
the date and still runs each bank's end of day, because interest accrues over a
weekend. The clock moves one *calendar* day and never skips to the next
settlement day.

## Consequences

**The system has no background goroutines at all.** No actor loops, no inboxes,
no in-flight counter, no download ticker. HTTP requests queue work; advancing the
day drains it. That is simpler than a set of concurrent institutions *and* more
faithful to bulk clearing, which is genuinely store-and-forward against a cut-off
clock — and the two rarely point the same way.

**The test suite's waiting device became a business concept.** Every flow test is
"submit, advance a day, assert". There is no quiescence loop, no deadline, no
poll and no non-deterministic assertion anywhere. The same call runs in the
suite, in the seed and behind a button in the UI.

**A state that was previously unobservable is now stable.** After `POST /payments`
and before the day runs, a payment is *provably* still `Initiated`, in no cycle
and unseen by the payee's bank. It used to be a race to look at; it is now a
state that rests until somebody advances the day.

**Every screen shows the date, because it has to.** The topbar carries the
business date and the button that advances it on *every* shell — the three
consoles, the lobby, Learn, and a customer's account. A customer advancing the
world is a fiction no retail bank has; it survives because the identity picker
two inches away already lets you become the central bank. That topbar is the
operator's frame around a persona rather than the persona's own chrome.

**`ReturnWindowDays` counts in business days now,** which closes the gap
`payment/ageing.go` documented against itself.

**The day is one-way.** Only a reset rewinds it. Both routes take the same lock,
because there is one deployment behind all N+2 ports.

## Alternatives rejected

**A wall clock with a seeded offset.** Cheaper, and it keeps the two dates a year
apart while hiding the discrepancy behind arithmetic. It also leaves no roll
event, so "nothing downstream calls `today()` for accounting purposes" stays
unenforceable.

**A background ticker advancing the day on a timer.** It buys realism and pays in
tests that fail at random: a suite running against a fake clock cannot also run
against a real one. It would also reintroduce the goroutine this decision
removes.

**A `business_date` row in the settlement agent's database.** The settlement
agent decides which days money moves on, so this looks like the calendar's
natural home. It would make the date a fact one institution could change under
the others, and a member bank reading it would be reading another institution's
row — the exact crossing the store split exists to refuse. The absence is argued
inside `centralbank/0001_init.sql`'s `transactions` statement, where a reader
would go looking for it.

## Not decided here

**Threading the date through the accounting layers.** The roadmap's *`BusinessDate`
type in `ledger`* item asks for the date to be assigned once at command acceptance
and carried with the command, so nothing downstream calls `today()`. That is a
day-granular type through ~38 signatures in `deposit` and `lending`, and doing it
inside a transport swap would put a compiler-guided rename across four packages
in the middle of one. What changes is that the item stops being speculative: once
a roll event exists, that rule has something to be enforced against.

**Intraday cycles.** SEPA runs several settlement cycles per business day; the
engine runs one. Adding more is a loop over a list of cut-off times, and the
calendar has to exist before a time of day within it means anything.
