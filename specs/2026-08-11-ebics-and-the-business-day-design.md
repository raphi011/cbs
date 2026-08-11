# Design — sub-project 21: EBICS batches, the institutions in `cmd/server`, and the business day

Branch `spec/ebics-and-the-business-day`, based on `main` at `576de64`.

7b built the mesh: N+2 actors, each with an inbox and a goroutine, exchanging one
ISO 20022 message per payment. It made **the message the interface**, and that was
the point of it — before it, a bank learned the fate of a payment by calling a
function that returned it.

This sub-project keeps that and replaces everything underneath it. `docs/sepa-real-world.md`
is in this tree and contradicts the mesh on every mechanical claim it makes:

> **Bulk** is store-and-forward file transfer against a cutoff clock. EBICS has no
> push at all — the bank *polls* for its downloads.
> — `docs/sepa-real-world.md:451`

A payment network is not a set of actors messaging each other. It is a set of banks
that accumulate instructions until a cutoff, upload a **file** carrying thousands of
them, and come back later to collect their results. Nothing is pushed at a member
bank, ever. The mesh models the shape of interbank messaging; this models the shape
of interbank *clearing*, which is a different and less obvious thing.

Three deliveries, and the third is what makes the first two cheap:

1. **The top level becomes library, and `cmd/server` becomes glue.** The three
   institutions get a struct apiece, with their own routes and their own EBICS side.
2. **The transport becomes EBICS.** `mesh` and `mesh/wire` are deleted.
3. **The deployment owns a clock, and advancing it runs a business day.**

## Decisions taken, and what they cost

Eight questions were settled before any design. Every one of them picked the more
expensive answer, and the last three are what turn this from a transport swap into a
sub-project.

1. **Real HTTP, not an in-process bus.** Institutions talk over the listeners they
   already have. The cheap answer — reshape the bus into upload/download and keep it
   in-process — would have kept `wire`'s guarantees, and those guarantees are exactly
   what is wrong: exactly-once, in-order, and a send that always succeeds.
2. **Pull, not push.** The CSM and the central bank hold a download queue per
   subscriber; a bank collects. The cheap answer keeps the current flows nearly
   intact. It also keeps a callback into a member bank, which no CSM has.
3. **`api` splits three ways.** `api/bank`, `api/csm`, `api/centralbank`, each over
   an interface *it* declares. The cheap answer — one `Server` with three router
   methods — is what exists, and it is why `Server.net` is nil-able and why
   `Server.as` exists.
4. **Real bulk files.** One file, N transactions, one status report with N outcomes.
   The cheap answer is a batch of one, which preserves every per-payment assertion in
   the suite and teaches nothing about accumulation.
5. **Settle before release, but as a later task.** `docs/sepa-real-world.md:207` says
   *"the cycle settles in T2 before STEP2 releases the output files"*, and this
   repository relays first and settles last. Correcting it moves AC01 from a reject
   to a return and reorders `payment`'s statuses, so it is task 8 rather than task 6.
6. **EBICS envelope, order types and `HAC`; no crypto.** The three key pairs, the
   half-offline INI/HIA enrolment and VEU distributed signature are named as out of
   scope rather than omitted silently.
7. **A cutoff the operator drives.** The bank's hub accumulates; a cutoff builds the
   file. Not a size trigger and not a wall-clock timer.
8. **The deployment owns the clock, advancing it runs the whole day, and the
   calendar is TARGET's.** This is the one that was not asked for and is the largest
   single simplification in the sub-project. See below.

## The claim everything else rests on

**With an operator-driven business day, the system has no background goroutines at
all.**

No actor goroutines, no inboxes, no in-flight counter, no download ticker. HTTP
requests queue work; advancing the day drains it, synchronously, on one goroutine.

That is simpler than the mesh *and* more faithful, and the two are not usually the
same direction. Bulk clearing genuinely is store-and-forward against a cutoff clock;
the concurrency in `mesh/wire` was modelling a liveness that bulk SEPA does not have.

It also disposes of the hardest problem the transport swap creates. `mesh.Drain`
blocks until nothing is in flight and returns the dead letters, and it is what lets
every test in this repository say "submit, drain, assert" without waiting for a
duration. Take the bus away and something has to replace it — a quiescence loop, a
fixed point, a poll with a deadline, all of them test devices with no domain meaning.
`AdvanceDay` returns when the day is done. The waiting device becomes a business
concept, and it is the same call in the suite, in the seed and behind a button in the
UI.

## What the top level is for, and what `cmd/server` is for

The rule: **a top-level package is a library some other system could use; `cmd/server`
is this deployment's orchestration.** Two packages currently fail it.

`mesh` is three things wearing one name — a transport (`mesh/wire`), the three
institutions' behaviour (`bank.go`, `csm.go`, `centralbank.go`, `ops.go`), and a
composition root (`mesh.go`, `doors.go`). Only the first is a library.

`api` is already institution-shaped — `centralBankRouter`, `clearingHouseRouter`,
`bankRouter` — but the three share one `Server`, so the type has a `net` field that
is nil until a surface is chosen and an `as` method that rebinds it. That is a
composition root pretending to be a request handler.

### The map

| Package | Role |
| --- | --- |
| `calendar` | **new.** The TARGET settlement calendar and the deployment clock |
| `ebics` | **new.** The transport: order types, order ids, return codes, ordered per-subscriber download queues, a `Server` that is an `http.Handler`, a `Client`. Carries bytes and knows no ISO 20022 |
| `payment` | bulk read and write of `pacs.008`/`pacs.003`; gains `ErrOnUsPayment` |
| `api` | shared plumbing only: `router.go`, `respond.go`, `errors.go`, `middleware.go`, `dto_*.go` |
| `api/bank`, `api/csm`, `api/centralbank` | **new.** Handlers and routes per institution, each over an interface it declares |
| `seed` | drives a `seed.Deployment` interface instead of `*mesh.Mesh` |
| `provision` | also enrols the bank as an EBICS subscriber at the CSM and the central bank |
| `mesh`, `mesh/wire` | **deleted** |

`cmd/server`:

| File | Holds |
| --- | --- |
| `main.go` | flags, open the stores, build the deployment, start the listeners, signals, teardown — **nothing else** |
| `deployment.go` | the `Deployment`: stores, networks, clock, the three institution sets, the EBICS wiring |
| `day.go` | the business-day engine |
| `bank.go` | `type Bank` — its routes, its EBICS client, its payment hub, its receive-side behaviour |
| `csm.go` | `type ClearingHouse` — its routes, its EBICS server, validation, netting, output files |
| `centralbank.go` | `type CentralBank` — its routes, its EBICS server, settlement, advice |
| `listeners.go` | the listener plan, largely as it is |

`deployment.go` and `day.go` are two files beyond the three institutions, and the
reason is that neither belongs to an institution. A business day is the *deployment's*
act — it drives all N+2 — and putting it in `main.go` is what would stop `main.go`
being init and teardown.

### The api split, and the interface each package declares

Each sub-package declares what it needs and `cmd/server`'s struct satisfies it. This
is what makes the HTTP layer testable without a deployment: `api/bank`'s suite drives
a fake.

```go
// api/bank
type Institution interface {
    Network() *payment.Network
    BIC() iso20022.BIC
    Submit(context.Context, payment.InitiatePaymentRequest) (payment.Payment, error)
    Cutoff(context.Context) (ebics.OrderID, error)
    Pending(context.Context) ([]payment.Payment, error)
    Return(context.Context, payment.PaymentID, iso20022.ReturnReason, string) error
    Lodge(context.Context, ledger.AssetCode, ledger.Amount) (payment.LodgementInstruction, error)
    RefreshDirectory(context.Context) ([]payment.DirectoryEntry, error)
}
```

`api` keeps every DTO, the error mapping, `respond.go`, the middleware and the router,
and imports none of the three. The dependency runs one way: the three import `api`.

`handleListAssets` is on all three surfaces today and becomes a helper in `api` that
each of the three registers, which is the one piece of genuine sharing in the split.

## The transport: EBICS, and the actor table disappears

### Who dials whom

The subscriber is the client. That is the whole of the topology:

```
member bank  --> CSM              (CCT, CDD, pacs.004 up; pacs.002, pacs.004 down)
member bank  --> central bank     (camt.050 up; camt.053, camt.025 down)
CSM          --> central bank     (pacs.009, pacs.004 up; pacs.002 down)
```

The central bank never dials anybody. Nothing is pushed at a member bank. **Three
base URLs are configured in the entire system**, against an actor table that today
holds N+2 entries and has to be kept in step with the roster through `AddBank`,
`ForgetBanks` and `JoinRoster`.

Those three methods go, and so does everything that exists to keep the table honest:
`ErrAddressTaken`, `wire.Claim`/`Release`, `Bus.Forget`, and `Mesh.ForgetBanks`'s
argument about an actor whose index entry is missing being unforgettable for the life
of the process.

### The download queue is the routing table

This is the part worth stating on its own. The CSM routes an output file to a
creditor agent **by putting it in that subscriber's download queue**. There is no
address to resolve, no URL to look up, and no table that can disagree with the roster
— because enrolment is what creates the queue, and enrolment is part of provisioning.

`ErrUnknownBIC` becomes "that BIC is not a subscriber here", answered off the roster
the clearing house already holds, and RC01 is still what goes on the wire. What
changes is that the refusal is now made by the institution whose judgement it is,
from a row it owns, rather than by a transport that knows nothing about membership.

`mesh/doc.go`'s long section *"A bank the roster does not name, and what this
transport does not refuse"* describes a gap between reachability and membership that
this closes: they become the same fact. The two refusals that section argues for —
at `Mesh.Submit`'s door and again at `AcceptAtCSMTx` — both survive, because both are
about a *payment* rather than about a connection.

### Order types and the envelope

EBICS 2.x order types, because they are legible and `docs/sepa-real-world.md:499`
names them. EBICS 3.0's BTF descriptor is noted there as the successor and is out of
scope.

| Order type | Direction | Carries |
| --- | --- | --- |
| `CCT` | upload | a `pacs.008` file |
| `CDD` | upload | a `pacs.003` file |
| `CRT` | upload | a `pacs.004` file |
| `CST` | upload | a `pacs.002` file |
| `CSI` | upload | a `pacs.009` settlement instruction |
| `CLD` | upload | a `camt.050` lodgement |
| `C53` | download | statements |
| `HAC` | download | the acknowledgement file: order id, status, and what the receiver made of it |
| `BTD` | download | everything else in the subscriber's queue, in order |

An upload answers **technically** and immediately: a return code and a minted order
id, or a refusal. It does not answer what the file *means*. That is the seam this
sub-project is for — `EBICS_OK` says the file arrived and parsed, and the business
answer is a `pacs.002` the subscriber collects on a later download.

`HAC` is what makes that seam visible rather than merely true. A subscriber that
uploaded a file and has not yet been told anything about its contents can ask for its
order's status, and get "received, not yet processed" as an answer distinct from
"accepted" and from "no such order".

### What the header stands in for

Subscriber identity is a request header. It is not authentication and the code says
so, in one comment, naming what a real one has: `A006` signs the order, `E002`
encrypts the payload, `X002` authenticates the request, and enrolment is INI/HIA with
a hand-signed paper letter carrying the key hashes
(`docs/sepa-real-world.md:468`–`498`).

This is the same posture `api/surface.go` already takes about ports — *"This is
scoping, not authorization. Nothing verifies that the caller on a bank's port is that
bank; the port is the claim."* The header is the claim. Stating it once, at the door,
is what keeps a reader from inferring that file transfer between banks is
unauthenticated by nature; it is in fact the most heavily authenticated hop in the
whole chain, and this system models none of it.

### Two connections share no ordering, and the bank chooses one

`mesh/doc.go` argues at length that the `camt.053` goes out **before** the ACSC, and
that the ordering is load-bearing: the statement carries the mirror leg and the ACSC
carries the creditor leg, and a happens-before chain through one queue is what
guarantees it.

There is no such chain any more. The statement is in the **central bank's** queue for
that member and the ACSC is in the **CSM's**, and two connections have no shared
order. What replaces the guarantee is the bank's own download job: **it collects from
the central bank first, then from the CSM.**

That is more honest than what it replaces. The ordering was never a property of the
network; it was a property of there being one queue. Now it is a decision a bank
makes about its own operations, in one place, and a bank that made the other decision
would post creditor legs against a suspense whose mirror leg had not yet moved. The
comment goes where `centralBank.advise`'s does today.

### What EBICS makes reachable, and what it makes unreachable

Reachable for the first time:

- **An upload that genuinely fails.** `wire`'s doc says plainly that *"a send to a
  live actor always succeeds"* and that "the counterparty is down" is not
  expressible. Over HTTP it is: a refused connection, a 500, a timeout. The
  uploading institution keeps its file and its unit of work is unaffected, because
  the send has always been outside it.
- **A bank that never downloads.** Its customers never learn the fate of anything.
  This is a real operational failure with a real remedy, and it has no analogue in a
  system where results are pushed.
- **A file that arrives and cannot be processed.** The uploader is told
  `EBICS_OK` — it arrived — and the business refusal comes back on a later download,
  or does not come back at all if the receiver could not get that far.

Unreachable, and it is a loss worth naming: **the dead-letter path**. `wire`'s
accounting exists because an actor handler has nobody to return an error to. Under
pull, an institution processing a downloaded file is running its own day and has
somewhere to put the failure. See the day report below.

Not modelled either way, and stated so a reader does not infer otherwise: the
store-and-forward transports under real bulk clearing (SWIFT FIN, FileAct) carry
delivery guarantees and non-repudiation. This one carries an HTTP status.

## The business day

### Advancing the clock runs the day

Every layer in this repository already takes `clock func() time.Time` — `ledger/book.go:92`,
`deposit/register.go:116`, `payment/system.go:225`, `store/sqlite/set.go:99`.
`seed/clock.go` already freezes one, advances it and rewinds it. `seed/seed.go:380`'s
`runDays` already advances a day at a time and drives `RunEndOfDay` through each one.

What is missing is that the clock is the **seed's private** clock, and
`Populate` calls `goLive()` when it finishes. So the sample dataset is dated at
`baseDate` and everything a user does afterwards is stamped with wall time, a year
apart, on one screen.

The deployment owns the clock. Advancing it does not move a number; it **runs a
business day**.

### The schedule

`cmd/server/day.go`, one function, in this order:

```
banks   payment-hub cutoff -> one file per scheme -> upload CCT / CDD
csm     download, validate, record, sort by creditor agent -> output files
banks   download -> ACCP or RJCT per transaction -> upload CST
csm     download, clear the accepted into the open cycle
csm     cycle cut-off -> net -> upload CSI to the central bank
cb      settle whole-or-nothing -> camt.053 per member -> answer the csm
csm     download the answer -> ACSC fan-out per payment
banks   download the CENTRAL BANK first, then the csm -> mirror leg, then creditor legs
banks   RunEndOfDay — accrual, arrears
clock   -> the next TARGET settlement day
```

Being one explicit list is itself a deliverable. The four flows are currently spread
across three actor files and are legible only by reading `mesh/doc.go`'s narration of
them; here the order of the day *is* the code. It is also why task 8 — settle before
release — is a reorder of nine lines rather than a redesign.

Two things it deliberately does not do. It does not run a cycle on a non-settlement
day, and it does not interleave: each phase completes for every institution before
the next begins. Real clearing is exactly this batched, and a system that
interleaved would be inventing concurrency it does not have.

### The day report replaces the dead letters

`AdvanceDay` returns a report: the files uploaded and their order ids, the
per-transaction outcomes, and whatever any institution could not process.

This is strictly better than what it replaces. `Drain` returns dead letters *joined*,
which is a string; a report is a value the operator console renders, the suite
asserts against, and the seed checks. It is also the thing that makes the day
legible in the UI — a learner can watch a payment move through nine phases instead of
observing that it has arrived.

### Non-settlement days

Weekends and TARGET holidays advance the date and run `RunEndOfDay` — interest
accrues over a weekend, which is the entire reason day-count conventions exist — but
no cutoff, no clearing and no settlement runs.

This closes a gap the repository already documents against itself.
`payment/ageing.go:16` prices it:

> The rulebook figure is THREE BANKING BUSINESS DAYS (EPC SCT, D+3), and this is
> calendar days because there is no business date in this system — no roll event, no
> calendar and no holiday handling. What the approximation costs is exactly one
> thing: a balance that arrives on a Thursday is called overdue on Sunday where the
> rulebook would call it overdue on Tuesday.

After this there is a business date, and `ReturnWindowDays` counts in it.

The holiday table is TARGET's six: New Year's Day, Good Friday, Easter Monday, 1 May,
25 and 26 December. Two of them are computed from Easter, which is a fixed algorithm
and not a data file.

### The clock belongs to the deployment, not to any institution

It is in **none** of the N+2 databases, and that is not an oversight to be tidied
away. A business date is a fact about the *deployment*; a bank's database holding one
would be a bank's opinion about what day it is, and the schema comments in
`store/sqlite/schema/` are explicit that what is absent from each shape is the
substance.

So: a small file beside the databases in the `-database` directory, read at boot and
written on each advance. With no directory — the ephemeral default — it starts at
`baseDate` every time, which is exactly today's behaviour and is what makes
`go test ./...` need no setup.

`goLive` is deleted. `rewind` stays, because a reset must replay the same timeline.

### The operator surface

`POST /clock/day` and `GET /clock`, on the **central bank's** port, under the
argument `api/surface.go:61` already makes for `GET /members` and `POST /admin/reset`:

> What justifies it is the same thing that justifies POST /admin/reset sitting here:
> listing the banks a deployment holds and rebuilding them are one operator's acts
> over a DEPLOYMENT, and a deployment is not an institution.

Advancing the day is the same kind of act, and it takes the same lock `Reset` takes.
`POST /end-of-day` on a bank's own port (`api/handlers_lending.go:33`) survives as
that bank's own act; the day engine calls the same `payment.Bank.RunEndOfDay`.

### The date is in the topbar, and so is the button

`web/src/components/shell/topbar.tsx`, on **every** shell — the three consoles, the
lobby, Learn and a customer's account — showing the business date, the closure when
there is one (*"TARGET closed, Easter Monday"*, which is what `calendar.HolidayOn`
returns a name for), and an **Advance day** button beside it.

It is not decoration and it is part of task 6 rather than a later nicety. Deleting
`goLive` is what makes it load-bearing: today everything after seeding is stamped
with wall time, and after task 6 every date on every screen is the deployment's,
about a year from the wall clock. A user who cannot see which day the deployment is
on cannot tell a stale seed date from today's, and every screen is then read wrong.

Both routes stay on the central bank's port and the topbar addresses it explicitly
from wherever it is standing. That is exactly what `ResetButton` does, and
`sidebar-nav.tsx` already rules it *"correct rather than awkward wherever you happen
to be standing"*. Spreading `GET /clock` across all N+2 surfaces would buy nothing
once the button beside it is dialling the central bank anyway.

Two consequences, ruled here rather than mid-task:

- **A customer's screen gets it too**, and a customer advancing the world is a
  fiction no retail bank has. It survives because the identity picker two inches
  away already lets you become the central bank: that topbar is the operator's
  frame around a persona rather than the persona's own chrome, and it is the one
  place in the app that is honestly outside the fiction. `ResetButton` never faced
  this because the console sidebar it lives in does not render for a customer.
- **The day is one-way.** Only a reset rewinds it, which puts the button nearer
  `Reset` than to anything else on the screen — and it still takes no confirm: it
  destroys nothing, and a modal on the app's central teaching interaction would be
  paid for on every click. What the toast carries is the day report; the date
  changing in place is the acknowledgement. It disables across every shell while
  pending, because there is one deployment behind all N+2 tabs and it takes the same
  lock `Reset` takes.

On a non-settlement day the button still advances. Nothing clears, the readout says
why, and that is the lesson rather than a state to be prevented.

## The batch file

### The hub accumulates; a cutoff builds the file

`POST /payments` validates, posts the debtor leg, marks the payment `Initiated` and
**queues it for the next file** — the same 202 it answers today, now meaning
something more specific. `GET /payments/pending` is what is waiting.
`POST /payments/cutoff` builds one file per scheme, uploads it, and answers with the
order id.

The customer-facing half of the door does not change: a submission that fails this
bank's own checks is still refused synchronously, which is what
`mesh/doors.go` exists to guarantee and why `api` can answer 422 rather than 202
followed by a rejection nobody can be told about.

### One file in, M files out

The CSM receives one file carrying N transactions from one bank and **sorts by
creditor agent**, producing one output file per receiving bank. That fan-out is what
a clearing house is for, and it is invisible in a system where every message carries
one payment.

Each receiving bank answers per transaction, in one `pacs.002` covering the file it
received. The CSM correlates those back to the submitters, which is a group-by on
rows it already holds.

### `GrpSts: PART` becomes reachable

`docs/expansion-roadmap.md` lists it under what is *"reachable for the first time but
unclaimed: batched bulks — which is what would exercise `pacs.002`'s `GrpSts: PART`,
built and unused — a `pacs.028` status request, and a cutoff timer."*

Two of those three are claimed here. `payment.groupStatusOf` already computes `PART`
for a mixed file and has never been able to see one. `pacs.028` is not claimed.

### What `payment` has to change

The one-payment-per-message rule is **not** the mesh's. It is the domain's:

- `payment.onlyTransaction` (`payment/translate.go:1389`) — *"this system reads one
  payment per message"*. Becomes an iteration; `checkNbOfTxs` stays exactly as it is,
  because it checks what the **sender asserted** and a receiver that recomputed it
  would never notice a truncated file (`iso20022/pacs008.go:53`).
- `CreditTransferRequest` / `DirectDebitRequest` become plural, returning a request
  and a `PaymentID` per transaction.
- `CreditTransferMessage` / `DirectDebitMessage` take `[]Payment`.
- `StatusMessage` already takes `[]TransactionStatusReport`; `ReadStatus`,
  `ReadReturn` and `ReadSettlement` already return slices. **Half the work is already
  done and unreachable**, which is a good sign about where the boundary was drawn.
- `mesh/csm.go:396`'s `refuseBulk` is deleted. It exists only to refuse a file with
  more than one transaction, and its error text is a promise this sub-project breaks
  on purpose.

`ErrOnUsPayment` moves from `mesh` to `payment`, beside `ErrBankNotAdmitted`. It is a
statement about a payment's route, `api/errors.go` maps it to 422, and after this
change there is no `mesh` for it to live in.

## Enforcing the boundary rather than trusting it

`mesh/doc.go` names four mechanisms that narrow what one institution may reach, and a
fifth that checks the result. All five survive; three are unchanged and two are
re-expressed.

| Mechanism | Narrows by | After |
| --- | --- | --- |
| `mesh/ops.go`'s three interfaces | method | moves to `cmd/server` beside the institution each belongs to. A `Bank` that called `SettleCycleTx` still does not compile |
| `payment.Network` identity | institution | unchanged. Each institution is built over the network of the institution it *is* |
| the store | database | unchanged. `sqlite.ErrNotThisStoresBook` and `ErrNotInThisShape` |
| the book recorder in `mesh/books_test.go` | measurement | moves to `cmd/server`. `TestWhichBooksEachBankActuallyReaches` and its siblings are the reason task 5 moves the tests before task 6 changes them |
| `payment/recon` | agreement | unchanged as a package. **Recalibrated**, not deleted — see below |

`payment/recon` opens all N+2 databases at once precisely because no actor may, and
`mesh/recon_test.go` calibrates it against five deliberately broken states. Those five
tests move to `cmd/server` in task 5 and are re-proved in task 6. If a change could
make two institutions' books disagree, that is still the test to add — and a
transport swap is exactly such a change.

One genuinely new boundary question: the EBICS server holds download queues in
memory, and a queue is one institution holding bytes addressed to another. It is not
a crossing — the bytes are opaque to the holder, which is the same property
`wire` had and the reason both packages carry bytes rather than structs — but it is
worth an explicit test that the CSM cannot read a payment out of a queue instead of
out of its own rows.

## Roadmap items this absorbs, and two it deliberately does not

**Absorbed.** *A business date* (`docs/expansion-roadmap.md`, under *Domain depth*)
asks for exactly the roll event this builds: *"Booking dates should advance by an
explicit, logged business-date roll rather than by the server clock crossing
midnight"*, and names the worked case it gets wrong — a transfer submitted after
cut-off before a holiday weekend. That case becomes buildable in the seed.

Also absorbed: the deferred note that batched bulks and a cutoff timer are *"reachable
for the first time but unclaimed"*. Task 7 claims both. `pacs.028`, listed beside
them, is not claimed.

**Not absorbed, and the distinction matters.** The same entry asks for the date to be
*"assigned once at command acceptance and carried with the command, so nothing
downstream calls `today()` for accounting purposes"*. That is threading, not rolling,
and it is the *`BusinessDate` type in `ledger`* item under *Structural work* — a
day-granular UTC-normalised type through ~38 signatures in `deposit` and `lending`.
This sub-project builds the roll and the calendar and leaves the threading where it
is. Doing both at once would put a compiler-guided rename across four packages inside
a transport swap.

What changes for that item is that it stops being speculative: once a roll event
exists, "nothing downstream calls `today()`" is a rule with something to enforce it
against.

**Not absorbed, and it is a near miss worth naming.** *Deepen the transport module*
observes that 65% of `api`'s handler lines are the same five steps and that 28 of 70
handlers are nothing else. Task 4 moves those 5k lines, which is the cheapest moment
this repository will ever have to collapse them — and bundling the two would make a
mechanical move unreviewable. Task 4 stays a move. The item is worth doing
immediately after it, against three smaller packages, which is easier than doing it
against one large one.

## Rulings this sub-project reverses

Stated here rather than discovered mid-task.

- **"Delivery is exactly-once and in order"** (`mesh/wire/doc.go`). No longer. An
  upload can fail and be retried, and two connections share no order.
- **"No test here waits for a duration, and Drain is why"** (`mesh/doc.go`). Still
  true, and for a different reason: `AdvanceDay` is synchronous.
- **"The clock is literally one"** (`mesh/doc.go`). Still true and now stronger — it
  is the deployment's, and `time.Now` appears nowhere in the running system.
- **"A bank's own copy of the roster is possibly behind"** (`mesh/doors.go`,
  `RefreshDirectory`). Unchanged, and it gains a natural cadence: the refresh becomes
  a phase of the day rather than a route somebody remembers to call.
- **"Ports are static, and so is the set of banks"** (`cmd/server/listeners.go`).
  Unchanged. A bank provisioned into a running deployment still gets no listener until
  restart, and that is still a decision about what joining a network means.

## Tasks

Nine. Each leaves `go test ./...` and the web suite green.

Tasks 1–4 land **while the mesh is still underneath**, so every step is measured by
the recorder and the existing suite before the transport is removed. That is sub-project
8's shape and it worked.

| # | Task | Size |
| --- | --- | --- |
| 1 | **`calendar`.** TARGET settlement calendar, Easter computation, and a persistable `Clock`. Nothing uses it yet | ~300 |
| 2 | **`ebics`.** Envelope, order types, order ids, return codes, ordered per-subscriber queues, `Server` as an `http.Handler`, `Client`, `HAC`. Nothing uses it yet | ~700 |
| 3 | **`payment` goes bulk.** Plural request readers, bulk message builders, `onlyTransaction` deleted, `checkNbOfTxs` kept, `ErrOnUsPayment` moved in. The mesh keeps sending files of one, so the suite is unmoved | ~400 |
| 4 | **`api` splits three ways.** `api/bank`, `api/csm`, `api/centralbank` over declared interfaces; `api` keeps the plumbing. Still driven by the mesh | ~5k moved |
| 5 | **The institutions move to `cmd/server`,** still on `mesh/wire`. `mesh`'s flow tests, the book recorder and the recon calibration move with them | 2.8k + 11k tests |
| 6 | **The transport swaps.** `ebics` in, `mesh/wire` out; `deployment.go` and `day.go` appear; `mesh` is deleted; `seed` is reworked against `seed.Deployment`; `goLive` is deleted, and the topbar gains the business date and the button that advances it | ~2k rewritten |
| 7 | **True bulk.** Hub accumulation, `POST /payments/cutoff`, N-transaction files, CSM fan-out per creditor agent, per-transaction status, `refuseBulk` deleted, `GrpSts: PART` reachable | ~1.5k |
| 8 | **Settle before release.** The CSM releases output files only once the cycle is final; an account-level problem becomes a `pacs.004` return rather than a `pacs.002` reject | ~1k |
| 9 | **Documentation across every layer** | large |

**Task 5 is where this design is worth re-reading before starting.** It is the
largest single item and the first that cannot be reverted cheaply; if it runs long it
splits into **5a** (the three structs, mechanically, tests still in `mesh`) and **5b**
(the tests move).

Task 9 is listed last and is not done last. Each of tasks 6, 7 and 8 changes a claim
the learner-facing layers make, and `CLAUDE.md` is explicit that those move together;
task 9 is the sweep that catches what the per-task edits missed, plus the ADR.

## Testing

**What replaces "submit, drain, assert".** Every flow test becomes "submit, advance a
day, assert". That is fewer moving parts, not more: no `Observe` gate, no deadline, no
`Stuck()` diagnostic, and no non-deterministic assertion anywhere in either package.

`mesh.Config.Observe` exists for one reason — `api`'s
`TestSubmitAnswers202AndThePaymentIsNotYetAccepted`, which needs to look at the
system mid-conversation and cannot, because reading the store races the actors. Under
a synchronous day there is no race: after `POST /payments` and before `AdvanceDay`,
the payment is *provably* still `Initiated`. The gate is deleted.

**New suites.**

- `calendar` — the six TARGET holidays across a decade, Easter, the weekend roll, and
  the persistence round-trip.
- `ebics` — order id minting, queue ordering, a download that empties a queue exactly
  once, `HAC` distinguishing "unknown order" from "received, not yet processed" from
  "answered", and a subscriber that is not enrolled.
- `cmd/server` — the four flows, the day report, and the five recon states.

**Suites that must be seen to fail and then pass.** The book recorder's expectations
do not change in task 5 and must not; if they move, something crossed a boundary
during a move that was supposed to be mechanical. In task 6 the *institution* that
performs each act is unchanged, so the recorder's expectations should again not move
— that is the strongest single check that the transport swap changed only the
transport.

**A file, not the ephemeral store.** `TestTheRetryBudgetOutlastsASlowWriter` opens a
file because the ephemeral store hides read-then-write defects. The clock's
persistence needs the same treatment: a deployment restarted mid-timeline, proving the
clock resumes rather than rewinding.

## Failure modes

| Failure | Today | After |
| --- | --- | --- |
| counterparty unreachable | not expressible | an upload error; the file is kept |
| a bank never collects its results | not expressible | its customers are never told; visible in the day report |
| a receiver cannot process a file | dead letter, returned by `Drain` | recorded in the day report against the order id |
| a statement that cannot be delivered | `centralBank.advise` returns on the first send it cannot make, suppressing every member after it | the statement sits in the member's queue until collected; the fan-out cannot be truncated by one unreachable bank |
| two institutions' books disagree | `payment/recon` | unchanged |
| a net payer short of reserves | AM04, cycle stays `Closed`, operator funds and calls `Settle` | unchanged |

The fourth row is a genuine improvement and is worth calling out. `mesh/doc.go`'s
section *"What an undelivered statement suppresses"* enumerates three costs of
`advise` returning early — the member is never advised, every member after it is never
advised, and the ACSC is never sent — and then observes that none of it is reachable
because delivery always succeeds. Under pull it is not merely unreachable, it is
structurally impossible: writing to a queue cannot fail on the recipient's account.

## Documentation 21 owns

The domain claims that change are: push becomes pull, one message per payment becomes
a file of many, and a business calendar appears. Every layer states at least one of
those.

- **`README.md`** — the authoritative source. Its interbank, clearing, settlement and
  persistence sections.
- **`CONTEXT.md`** — new terms needing a disambiguation line and an `_Avoid_` list:
  EBICS, order type, order id, download queue, cutoff, business date, TARGET day,
  settlement day. One line each, no argument.
- **`web/src/components/hint-content.ts`** — distilled from the README. Names **no
  repo symbol**, but does carry the vocabulary a banker would recognise: `EBICS`,
  `CCT`, `C53`, `HAC`, `pacs.008`, TARGET.
- **`web/src/lib/quiz/chapters/`** — 09 clearing-and-settlement, 10 the-interbank-network,
  11 payment-schemes and 12 sepa carry the claims that change; 14 and 15 touch
  statements and persistence. `diversity.test.ts` holds each to 18–22 questions, at
  least 8 distinct `concept` tags, no tag more than 3×, and all three tiers.
- **`store/sqlite/schema/{bank,csm,centralbank}/0001_init.sql`** — *what is absent is
  the substance*. The download queues are not stored and the clock is in no
  institution's database, and both belong inside the statement they concern, because
  SQLite drops a comment above one. `TestSchemaArgumentsReachSqliteMaster` is the
  guard.
- **`docs/adr/`** — referenced by `CLAUDE.md` and does not exist. This sub-project
  earns the first record: *the deployment owns the clock*.
- **`docs/sepa-real-world.md`** — currently untracked, and effectively this spec's
  source. It should be committed before task 1.
- **`docs/expansion-roadmap.md`** — sub-project 21 entered, and the deferred note
  about batched bulks and a cutoff timer marked as claimed.

## What this sub-project does not do

**EBICS's security half.** No `A006`, `E002` or `X002`, no INI/HIA enrolment with its
deliberately offline paper step, no VEU distributed signature. The last of those is
the most interesting thing in the protocol — four-eyes in the transport rather than in
an application — and it is a sub-project of its own.

**EBICS 3.0.** Order types stay the three-letter codes. BTF is named in
`docs/sepa-real-world.md` and not built.

**Separate processes.** One process, N+2 listeners, as today. Real HTTP between them
makes separate processes *possible* for the first time, which is worth noting and not
worth doing: it buys nothing a reader learns from and costs a deployment story.

**Dynamic port allocation.** A bank provisioned into a running deployment still gets
its rows and no listener until restart.

**Instant payments.** Sub-project 2's, and this makes it easier rather than harder:
`docs/sepa-real-world.md` is clear that instant is a *different system* precisely
because it is message-based and settles gross, and after this the batch path is
honestly batched, so the contrast is real instead of cosmetic.

**`pacs.028`.** A status request. `HAC` covers the order-level question; the
transaction-level one stays unclaimed.

**Intraday cycles.** SEPA runs several settlement cycles per business day. The day
engine runs one. Adding more is a loop over a list of cutoff times and is deliberately
not in scope — the calendar has to exist before a time-of-day within it means
anything.
