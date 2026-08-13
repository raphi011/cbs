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

**A failed record never costs a file, at either end.** The write is LOGGED and
the act carries on, on the sending side and the receiving side both. Sending, a
caller told its upload failed would send the same instructions again and the
file has already arrived. Receiving, the reason is sharper: `Download` empties
the queue as it hands a file over, so a receive that returned on a failed record
would drop a settled file that exists nowhere else — reserves moved, and the
payee's bank never told. A log is a record of what happened and not a gate on
it.

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

**And a crossing is ordered by its SEND**, because it spans an interval rather
than standing at an instant: a total order over crossings does not exist, and
trying to build one from the two chains produces genuine cycles — A sends,
B sends back, and each end's log disagrees about which came first without either
being wrong. So the mesh orders by the send, with the sender and its own `seq`
breaking the tie the business date leaves (the clock does not move within a
day). The order things actually happened in is the STREAM's, which is the
journal's own; the snapshot carries state.

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

**It taps the journal at WRITE time, and that is the decision the rest of this
section falls out of.** `take` empties the journal and every report calls it, so
a broadcast that READ the journal would be a second consumer racing the report
for the same events. A watcher is told inside `File`, `Outcome` and `problem`
instead, under the journal's own lock, so it is told in the order the journal
was and nothing is taken from anybody. Reading a copy was rejected: it needs the
journal to keep what it has already reported, which is the one thing `take`
exists to prevent.

Four things follow, and each was a decision:

- **A slow watcher is dropped rather than waited for.** Nothing an institution
  does may block on a browser, so a full buffer closes that watcher's channel;
  its `EventSource` reconnects and reads the snapshot again, which is a whole
  picture rather than one with holes in it.
- **The stream is the day's report arriving without a request** — the same three
  shapes `POST /clock/day` and `POST /clock/phases/{phase}` answer with, one SSE
  event each. Inventing a mesh-shaped event was rejected: an order type is the
  TRANSPORT's fact and a message definition the DOCUMENT's, neither derives the
  other, and the two halves already pair on the order id.
- **A reset is not an event on this stream.** The journal is its only feed, and a
  reset is an operator's act from a tab that knows it made one. A second feed
  would be a second concept for one button.
- **The mesh reads every institution's whole log rather than a page of each.**
  The delivered half of a crossing can be far newer than the sent half, so a
  window per institution would report a delivered file as resting on a wire. The
  limit is applied to the CROSSINGS, and it never drops one still resting: a
  queue nobody has come for is what this view exists to show.

**The two halves of a crossing pair on the order id, and on the message id
where there is none.** A published file is minted no order id — the roster is
the one such file today — so the only thing its two ends hold in common is the
header its sender put on it. A half with neither pairs with nothing and is
rendered as the one-ended crossing it is, rather than merging with every other
half that also has neither.

**A wire is named by the parts its ends play** — a subscriber dials a host, and
nothing is ever pushed the other way — so a bank the scheme has not admitted is
in the mesh and on no wire at all. That is the difference between holding a
database and being reachable, drawn.

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

**The rail section is DESKTOP's, and the lobby's view is the whole of it on a
phone.** The rail's mobile form is a sheet a reader opens to have a concept
explained, and a live drawing of the network is a different object than the one
that sheet is for. The lobby carries the full-size view at every width instead.

**The lobby suppresses the rail section**, because one screen shows one mesh.

**The phase doors are IN the panel and not beside the day's button.** Stepping is
only interesting next to the thing it moves: the whole point of the pair is that
an operator opens a door and watches a file appear on a wire. *Advance day* stays
in the topbar, where it is the same act at the coarser grain.

**A file is drawn at the end it is travelling TO**, which is not the end that
dialled. One wire carries both directions — a bank uploads to its clearing house
over the connection it collects from — so an uploaded `pacs.008` waits at the
clearing house and a `pacs.002` nobody has fetched waits at the bank. Drawing
both at the host's end would have put settle-before-release at the wrong end of
the picture, which is the one thing this view exists to show.

**Only a RESTING file is drawn.** A delivered crossing is in the list beneath and
not a dot, because a graph that kept every file that ever crossed would fill up
and stop saying anything about now.

**A wire is a click, and it narrows the list beneath.** That is the only
interaction the drawing has: it is a picture with a filter, not a console.

### 5. What the list cannot claim

**There is no chronology inside a day, and the list must not imply one.** A
deployment's clock does not move within a business day, so every crossing on one
day carries the same instant; the mesh breaks that tie with the sender's address
and its own `seq`, which groups a sender's traffic in the order that sender sent
it. Reversing that to get "newest first" produces reverse-alphabetical-by-sender
wearing a timeline's clothes.

So the list keeps the mesh's grouping and imposes only what is true: the files
still in flight first, then the most recent DAY first. And each row shows the day
rather than the time, because rendering 09:00 on every row of a day claims a
precision the timeline does not have.

### 6. The push channel needed two things nothing had

Both were found by driving a real browser against a real binary, and neither is
visible from a test.

**The Next proxy buffered every response.** `app/api/[...path]/route.ts` ended in
`await upstream.text()`, which holds an event stream until its connection closes
and then delivers a day at once. It forwards `upstream.body` as a stream now, and
carries the origin's `Cache-Control` and `X-Accel-Buffering` across — a stream is
only a stream if nothing between the ends decides to hold it. The request's abort
signal goes upstream too, so a watcher that navigates away releases the one it
left open.

**An open stream must say so on opening.** A proxy holds the head of a response
until its first byte, so a watcher of a quiet deployment sat unconnected — and
reported itself dead — until something moved. `api.Stream` writes an SSE comment
immediately, which is what makes the channel open rather than merely accepted.
`no-transform` sits beside `X-Accel-Buffering` for the same class of reason: one
tells a compressing proxy not to hold the stream, the other tells nginx.

Neither is a heartbeat. Nothing here keeps a long-idle connection alive against an
intermediary's idle timeout; on loopback there is none, and adding a ticker is a
change with an argument of its own.

## What it costs

**Every message is stored forever.** Rows are never deleted, as in
`ebics_orders` and `audit_events`, and a deployment left running under an
accelerated clock writes a file per bank per window per day. `Reset` clears it
with everything else. Nothing here bounds it, and if a bound is wanted it is a
retention decision with a domain consequence rather than a tuning knob.

**A `messages` table is a schema change in three files**, so it is a
documentation change: chapters 15 and 16, the README's *Persistence* section, and
the schema comments themselves, which carry the domain argument.

**The push channel is a one-way door**, and the narrowing lands HERE. The rule
that the deployment owns the clock claims *"no background goroutines under this
process at all"*; a connection held open on a request's own goroutine is the
first thing that is not one, and the scheduled business day's ticker will be the
second. Stated once, in the README beside that claim: none runs BELOW
`cmd/server`, and an institution still does nothing nobody asked it to.

**A mesh read is every row every institution holds.** That is what a truthful
answer about what is still in flight costs, and it is the read `payment/recon`
already makes at whole-deployment scale. It is also why the stream carries the
events themselves: a page that re-fetched the snapshot per file would pay it
once per movement.

**What it does NOT read is the files.** The mesh and every listing want a
file's SIZE, so the filter leaves the payload unread and the store answers
`length(payload)` instead — which does not read a blob. Only `GET
/messages/{seq}`, one document at a time, is handed the bytes. The same read
takes the payments a whole page carried in ONE query rather than one per row:
the mesh's page is every row in the database, so a query per row is a query per
file that ever crossed.

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
5. **`GET /network/flow`** and the SSE endpoint, both on the operator surface.
   The snapshot pairs every institution's log into the crossings both ends
   observed; the stream is the journal's own events, and a second subscriber.
6. **The graph**: the rail section, the lobby view, and the movement list that
   clicks through to a payment's trail. The phase doors landed with it, because
   4 is what makes 6 worth watching and neither is worth much alone.
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

**`CONTEXT.md` does not gain "message log", and task 6 is what decided it.** The
term names ONE institution's record of its own traffic, and what the UI shows is
the mesh — every institution's halves paired into the crossings both ends
observed, which is not any institution's log. The screen says *network*, a line
is a *wire*, and what travels one is a *file*. A per-institution log reaching the
UI would be the change that makes the term due.

No new hint keys either: `payment-hub`, `download-queue` and `bulk-file` carry
the mechanism, and the graph is where a reader now meets them.

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
- **A broadcast does not consume what a report needs**, and neither takes the
  other's events. Asserted over a stepped phase: what a watcher was told and what
  the phase reported are the same list.
- **A file resting on a wire is never paged out**, whatever the limit, and a
  watcher that falls too far behind is dropped rather than waited for.
- **`payment/recon` unaffected.** The log records; it decides nothing.

The graph's own rules are pure functions in `web/src/lib/network-graph.ts` and
tested there, which is the only mechanical guard the frontend has: a bank the
scheme has not admitted draws no wire, a wire to the settlement agent bows far
enough to clear the clearing house at 3, 4 and 5 banks, the two directions of
travel rest at opposite ends, and a crossing between two ends that share no wire
is dropped rather than drawn.

What no test reaches is the rendering, and there is still no component runner.
The drawing, the doors and the stream were driven in a browser against a real
binary instead: a payment initiated and a phase stepped from OUTSIDE the browser
moved the picture with no interaction in it, and the payment's two copies
disagreed on screen — `Initiated` at its bank, `Accepted` at the clearing house —
with the `pacs.002` that would settle the argument drawn resting on the wire.
A narrow viewport was not verified in a browser; the rail is desktop-only by
construction and the lobby's view is a responsive grid.
