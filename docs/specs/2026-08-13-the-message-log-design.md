# Design — the message log, the network view, and stepping a payment

Based on `main` at `94e5996`. This is roadmap §1, the last piece of sub-project
7. It is the successor
[the payment whereabouts design](2026-08-11-payment-whereabouts-design.md) names
under *What this does not do* — *"the trail is the scaffolding a message viewer
hangs off, one document per step"* — and it should land after it for that reason.

## The defect, stated once

**Nothing in this system keeps a record of the messages it sent and received.**

Five gaps, and the last two are why the first three matter.

1. **A member bank keeps nothing at all.** EBICS has no push, so a bank hosts
   nothing and holds neither queue nor order log. What it uploaded and what it
   collected exist nowhere once the call returns.
2. **The two hosts keep uploads only.** `ebics_orders.payload` persists every
   file a subscriber sent, for the life of the database. Nothing records what a
   host *sent out*.
3. **A collected file is deleted.** `ebics_queue` rows go when the subscriber
   takes them, which is correct for a queue and total for a record.
4. **The journal records puts and never collects.** `node.Journal` sees every
   upload and every enqueue, in phase order, and `DayReport` carries them — but
   `bank.Collect` journals nothing. So the deployment can say when a file was
   *addressed* to a bank and not when that bank took it, and the gap between
   those two is what settle-before-release exists to
   teach.
5. **No order id is linked to the payments inside it.** A `pacs.008` carries N
   transactions. Without the join, "where has this payment been" cannot reach
   "and here is the file that carried it".

And one that is not about records at all:

6. **Nothing can be stepped one hop.** `POST /payments/cutoff` on a bank's
   listener produces that bank's `pacs.008` and is rendered nowhere; after it the
   file sits in the clearing house's order log, because `csm.Work` is reachable
   only from `AdvanceDay` (`cmd/server/day.go:220`, its one call site). A reader
   can submit, or run an entire day. There is nothing in between, so any
   animation of a day is eleven phases at once.

## The shape

Four pieces. The first is per institution, the second is the deployment's, the
third is what makes it worth watching, and the fourth is where it is rendered.

### 1. A `messages` table, in all three schemas

One row per envelope an institution **sent or received**, written by that
institution about its own traffic. Direction, counterparty, order type, the
business-application-header message id, the phase it moved in, the payload, and
a child table naming the payments inside it.

A bank has no other record of anything; the two hosts gain the outbound half and
the parsed half they never had.

**It duplicates `ebics_orders.payload` at the two hosts, and that is accepted.**
The same bytes will sit twice in one database for an inbound file. The two
tables have different owners, lifetimes and readers: `ebics_orders` is the
TRANSPORT's — opaque bytes, an order status, and the HAC a subscriber downloads —
and `messages` is the INSTITUTION's, parsed and linked to payments. The
transport must not read a domain table and the domain must not depend on the
transport's, which is the argument `csm/0001_init.sql` already makes for
`held_files` against `ebics_orders`: *"The two coincide in bytes for one hop and
mean different things."*

The alternative — a nullable payload that means "look in `ebics_orders` under
this order id" — was rejected. It is a conditional foreign key, it is true only
at an institution that happens to be a host, and it makes every reader ask which
kind of institution it is reading.

### 2. The mesh view belongs to the deployment, and is served

`GET /messages` on each listener, mirroring `/audit`: one institution, its own
traffic, no crossing.

The **mesh** — every institution at once — is served from the operator surface
beside `POST /clock/day` and `POST /admin/reset`, not assembled in the browser
from N+2 listeners.

This looks like it contradicts
[the whereabouts design](2026-08-11-payment-whereabouts-design.md)'s decision 2,
which refused exactly such a merge. It does not, and the distinction is the
domain's rather than the code's:

- **A payment's STATUS is a copy**, and three copies in three databases
  legitimately disagree. Merging them would be the app quietly asserting they do
  not, and it would have to invent an order between two `seq` counters that have
  none. That refusal stands and this sub-project does not touch it.
- **A file crossing is an EVENT both endpoints observed.** There is one truth
  about whether a `pacs.008` went from one bank to the clearing house, and the
  deployment is the party that watched it happen — the same standing
  `payment/recon` has, and for the same stated reason: it may open every
  institution at once *precisely because no institution may*.

Serving it rather than fanning out in the browser is what keeps "may see
everything" in the type system. The `Operator` interface is where the
deployment's own acts already live.

**A listing is an index and a document is a resource.** `GET /messages` answers
each message's header, the payments it named and the file's SIZE, and never the
bytes. Rows are never deleted — see *What it costs* — so a page of a log that
grows forever must not carry the files in it, and a reader listing a payment's
documents wants to know which files carried it rather than to be handed all of
them. `GET /messages/{seq}` is where one document is read. The seq it takes is
one the listing named rather than one a caller composed: it counts a single
institution's own traffic, so the same number on another listener names that
institution's message or nothing at all.

**Ordering is the deployment's schedule position**, not anybody's `seq`. Within
one institution `seq` orders its own rows; between institutions nothing does,
and the honest answer is that the deployment ran the phases in a declared order
(the rule that a business day is a declared sequence) and that order
is the timeline. This is the same cost the whereabouts design priced, resolved
the only way it can be.

### 3. Stepping, and the events that make it live

**Every phase becomes an operator act.** The day already declares them
(`beforeClock`, `afterClock`); what is missing is a door per phase, on the
operator surface, so a reader can run the clearing and stop. This is the
deployment's, so it needs no institution to change, and
the rule that a business day is a declared sequence's ruling holds:
a phase is named, never parameterised, and a caller composes derived runs around
it rather than splicing.

**The doors sit with the clock**, on the settlement agent's listener beside
`POST /clock/day`, because they are the same act at a finer grain: `GET
/clock/phases` is the declaration and `POST /clock/phases/{phase}` runs one of
them. Five things follow from "named, never parameterised", and each was a
decision:

- **A key is the phase's own name spelt for a URL** — lowercased, spaces
  hyphenated. Not a second identifier declared beside the name, which would be
  one more thing to keep in step; the listing is where a client reads them, and
  two phases spelling alike is a panic at boot rather than a door that shadows
  another.
- **A phase a SEQUENCE holds and the day does not declare gets no door.** The
  narrowed collection (`collectClearingHouseOnly`) is cut to fit
  `carryToClearing` and is the one phase in the codebase the day does not
  declare; offering it would be offering a splice.
- **`settlementOnly` is reported, not enforced.** It stays what it already is —
  `AdvanceDay`'s question — because a caller that named a phase has decided it
  wants it, which is the existing rule for a derived sequence and not a new one.
- **No door for the clock move.** Stepping every phase of a day leaves the date
  where it was, and only `POST /clock/day` moves it. Making the roll an event
  among the others is [the scheduled business day](2026-08-13-a-scheduled-business-day-design.md)'s
  one structural change, and pre-empting it here would land it without the
  cursor that makes it safe.
- **Each door drains the journal and reports what it moved**, exactly as
  `AdvanceDay` and `CarryToClearing` already do. So a report is what *this call*
  did, and a day advanced after some phases were stepped reports only the rest.

**What it costs is that stepping and advancing overlap.** A phase run by hand
and then a whole day runs that phase twice, and nothing refuses it, because
nothing records how far the day has got. The guards that make this survivable
today are the ones the scheduled business day's *phase re-entrancy* section
names — and this is the change that first puts a re-run within reach of a
browser, which is an argument for that record's phase 2 rather than against this
door.

**`Collect` is journalled.** Gap 4 above. A file is put into a queue in one event
and taken out in another, and the graph renders a queued-and-uncollected file as
a dot resting on the wire at the receiving bank's end. That gap *is*
settle-before-release, drawn.

**SSE, and it is the first push channel in this codebase.** Replaying the day
report is enough while nothing moves without a request. It stops being enough
under [the scheduled business day](2026-08-13-a-scheduled-business-day-design.md):
once a clock ticks on its own, phases fire with nobody watching, and a page that
only replays what *this tab* triggered is wrong most of the time.

`node.Journal` is already the deployment's sink for every file and every
outcome. The broadcast is a second subscriber on it — no institution changes, and
nothing below `cmd/server` learns that a browser exists.

### 4. It is rendered in the operator's frame

The graph goes in the chrome — the top-right rail that already holds the concept
panel, above it — and in a full-size view on the lobby.

**That frame is already established as the operator's rather than the persona's**,
and the rule that the deployment owns the clock is where:

> Every screen shows the date, because it has to. […] A customer advancing the
> world is a fiction no retail bank has; it survives because the identity picker
> two inches away already lets you become the central bank. **That topbar is the
> operator's frame around a persona rather than the persona's own chrome.**

So the mesh needs no new argument. It is the same class of object as *Advance
day*: a power no participant has, in the frame around the persona rather than
inside it. What it must not do is leak into the persona's own screens — a bank's
payments table showing another bank's traffic would be the crossing this
placement avoids.

`ShellFrame` already renders a collapsible right rail on desktop and a bottom
sheet on mobile, so the rail exists and gains a section.

## What it costs

**Every message is stored forever.** Rows are never deleted, as in
`ebics_orders` and `audit_events`, and a deployment left running under an
accelerated clock writes a file per bank per window per day. `Reset` clears it
with everything else. Nothing here bounds it, and if a bound is wanted it is a
retention decision with a domain consequence rather than a tuning knob.

**A `messages` table is a schema change in three files**, so it is a
documentation change: chapters 15 and 16, the README's *Persistence* section, and
the schema comments themselves, which carry the domain argument.

**The push channel is a one-way door.** the rule that the deployment owns the clock's *"no background goroutines"*
already narrows under the scheduled business day; an SSE hub narrows it again.
Both are the same narrowing and should be stated once, wherever it lands first.

## What this does not do

- **No omniscient payment trail.** The whereabouts design's decision 2 stands:
  a payment's status trail is read from one institution's audit and never
  merged. The message log adds the DOCUMENTS to each step of that trail; it does
  not stitch two institutions' opinions together.
- **No XSD validation of what is shown.** `iso20022/testdata/xsd/` is absent for
  licensing reasons and every subtest skips. Rendering a document is not
  validating it, and the message viewer must not imply otherwise.
- **No editing or replaying a message.** A log is a record.
- **No new hint keys.** `bulk-file`, `download-queue`, `payment-hub`,
  `payment-lifecycle` and `clearing-vs-settlement` already carry the mechanism;
  what changes is where the reader meets it.

## Tasks

1. **`Collect` journals**, and `FileMoved` grows a direction so a put and a take
   are two events rather than one. Smallest task, independent of the rest, and
   it fixes a report that is wrong today.
2. **The `messages` table** in three schemas, its child table, the store methods,
   and `storetest` coverage. Written at every send and every receive.
3. **`GET /messages` per listener**, plus the payment join so a payment detail
   page can ask which files carried it.
4. **The phase doors**, on the operator surface.
5. **`GET /network/flow`** and the SSE endpoint, both on the operator surface,
   fed from the journal.
6. **The graph**: the rail section, the lobby view, and the movement list that
   clicks through to a payment's trail.
7. **The document viewer**, hung off the whereabouts design's `PaymentTrail`.

Tasks 1–3 are the sub-project. 4 is what makes 6 worth building, and 7 is what
§7c was originally about.

## Documentation layers

`README.md`'s transport section and *Persistence*; the three `0001_init.sql`
files, whose comments are the domain argument for the relational mapping; the
quiz chapters on the transport and the clearing cycle. `CONTEXT.md` gains
**message log** only if the term is used in the UI.

`hint-content.ts` and the quiz chapters name no repo symbol, so what they may say
is that an institution keeps a record of the files it sent and received — never a
table, a route or a component.

## Verification

`go test ./...`, `npm run test`, and:

- **Every file that moves is logged at both ends.** Asserted over a whole
  business day: the count of sends at one institution equals the count of
  receives at its counterparty, for every pair.
- **A payment reaches its own documents.** From a payment id to the `pacs.008`
  that carried it, at each institution that holds a copy, on a seeded scenario.
- **A queued-and-uncollected file is distinguishable from a delivered one**,
  which is task 1's acceptance test and the thing the graph draws.
- **No institution can read another's messages.** The listener test: a bank's
  `GET /messages` answers only its own, and there is no route on any
  institution's surface that answers the mesh.
- **`payment/recon` unaffected.** The log records; it decides nothing.
