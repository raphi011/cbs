# Design — a package per institution, and the simulator above them

Based on `main` at `0cffdb6`. Tasks 1 and 2 have landed;
[ADR-0010](../adr/0010-an-institution-holds-what-it-does-the-deployment-holds-the-order.md)
is the ruling they produced. Task 3 has its fallback in place and its primary
form outstanding; task 4 is untouched and optional. See *What landed* below.

The goal is a **modular monolith**: one process that plays every institution,
built out of parts that each play exactly one. Reading what a clearing house does
should mean reading one package, and splitting the process into three binaries
later should be a composition change rather than a rewrite.

## Where the split already is

Six of the seven layers are already institution-scoped, and each has a ruling
behind it:

| layer | mechanism | ruling |
| --- | --- | --- |
| database | N+2, one per institution; no statement spans two | [ADR-0003](../adr/0003-an-institutions-obligations-live-in-its-database.md) |
| store | `OpenBank` / `OpenClearingHouse` / `OpenCentralBank` → three types | [ADR-0007](../adr/0007-a-store-per-institution.md) |
| domain | `BankNetwork` / `ClearingHouseNetwork` / `CentralBankNetwork`, 78 / 34 / 22 acts over 23 shared | [ADR-0006](../adr/0006-one-type-per-institution.md) |
| transport state | the EBICS queue and order log are tables in `csm/` and `centralbank/` | [ADR-0004](../adr/0004-a-queue-is-a-table-and-stays-opaque.md) |
| HTTP surface | `api/bank`, `api/csm`, `api/centralbank`, one `Institution` interface each | — |
| listener | one per institution, its own port, its own `/ebics` | — |
| **composition root** | **one `package main`, one `Deployment`, all three institutions** | **this record** |

**The institutions already talk over the wire.** `ebics.Client.do`
(`ebics/client.go:89`) builds an `http.Request` and POSTs it to a configured URL;
`deployment.go:133,193` hands each institution a client aimed at
`http://127.0.0.1:<port>/ebics`. Every file on the payment path — instructions
up, shares down, settlement instructions, returns, statements — already crosses a
network boundary that happens to loop back. Nothing on that path needs changing
to run three processes.

**The single-institution constructors already exist and production does not use
them.** `NewBankNetwork`, `NewClearingHouseNetwork` and `NewCentralBankNetwork`
(`payment/institutions.go:57-67`) are documented as "for a caller building ONE
institution over a store it already holds", and `sqlite.OpenBank` and its two
siblings (`store/sqlite/sqlite.go:154,165,173`) open one database. Today only
tests call them; `main.go:55` goes through `payment.NewNetworks` over the whole
`sqlite.Set`.

So this is not a decomposition. It is the last layer of a decomposition that is
otherwise done.

## The defect, stated once

**`cmd/server` is one package in which three institutions and their simulator are
interleaved, and the institutions hold a pointer back to the simulator.**

`Bank`, `ClearingHouse` and `CentralBank` each open with `d *Deployment`
(`bank.go:21`, `csm.go:20`, `centralbank.go:21`). `Deployment` holds all three
plus `nets *payment.Networks`, which is every store in the system. The cycle is
invisible because everything is in one package.

Measured: **53 reaches through that pointer**, and they are three different
things.

| kind | reaches | what |
| --- | --- | --- |
| the environment | **44** | `messageContext` 11 · `journal` 10 · `cfg` 10 · `log` 7 · `now` 3 · `nextMsgID` 3 |
| **the doors** | **4** | `Submit` 2 · `Return` 1 · `RefreshDirectory` 1 |
| **the operator console** | **5** | `Reset` · `AdvanceDay` · `BusinessDate` · `nets` ×2 |

The 44 are not coupling. They are a clock, a logger, an id minter, a report sink
and two BICs — a small value an institution can be handed at construction.
`cfg` is only ever `CentralBankBIC` (7) and `ClearingHouseBIC` (3): the two
addresses an institution dials, which it needs whether it shares a process with
them or not.

The other 9 are the real finding, and they are in nine named lines.

### The four doors: the simulator wearing an institution's name

- **`bank.go:54`** — `Bank.Submit` returns `b.d.Submit(ctx, req)`, and
  `Deployment.Submit` (`deployment.go:292-346`) reads the **clearing house's**
  scheme registry and roster, then routes to
  `d.member(submitterOf(scheme, debtorAgent, creditorAgent))`. **A customer
  POSTing to bank A's console can make bank B submit the instruction.**
  `api/bank/bank.go:27` exposes it. On a direct debit the submitting side is the
  payee's bank, so the routing is not a corner case — it is half the schemes.
- **`bank.go:82`** — `Bank.RefreshDirectory` returns
  `b.d.RefreshDirectory(ctx, b.bic)`, which reads the clearing house's live
  roster in-process and writes it into a bank's own directory. A bank reaching
  into the CSM's database is the one thing `CLAUDE.md` says nothing in this system
  may do; it is legal here only because the deployment performs it on the bank's
  behalf.
- **`csm.go:51,55`** — `ClearingHouse.Submit` and `.Return` delegate to the same
  two deployment doors. These are the **operator console**: a real clearing house
  submits nothing and returns nothing on its own say-so.

The first two are a bank's own surface doing something no bank does. The last two
are the deployment's act correctly identified — `api/csm/csm.go:20-22` already
says so in a comment — and served on the wrong receiver.

### The five console reaches: correct, and already admitted

All five are in `centralbank.go:45,51,58,64,71`, and `api/centralbank`'s
`Institution` interface already names them for what they are:

> An Institution is the settlement agent, as this surface needs it, **plus the
> two deployment-wide acts the package doc names.**

`Reset`, `AdvanceDay`, `BusinessDate` and the member listing are the simulator's,
served on the settlement agent's listener because that is where the operator's
console lives. That is a deliberate and stated arrangement. What is missing is
only that the type system does not distinguish it from the settlement agent's own
acts.

## The rule this record adds

[ADR-0008](../adr/0008-a-conversation-belongs-to-the-deployment.md) already
settles the same question one layer down: `payment/flow`'s four conversations
belong to "a caller that IS the deployment: `seed` and the suites, never an
institution and nothing on the transport path." `payment/recon` opens all N+2
databases "precisely because no institution in the system may."

**The business day is that same shape one layer up, and gets the same answer.**

> An institution's package holds what that institution really does. The
> deployment's package holds the order they do it in, and it is the one caller
> allowed to see all of them.

Which means the day engine, the phase ordering, the journal, `Reset` and the seed
**stay exactly where they are**. They are not a coupling to be removed; they are
the simulator, and a simulator that could not see every institution would not be
one. Splitting them would buy separate binaries at the cost of turning a
sequenced list into distributed coordination, and it is not what makes one
institution's logic hard to read.

What must change is only the **direction**: a node package must not know that a
`Deployment` exists.

## The design

**Three packages, one per institution, plus the simulator that drives them.**

```
node/bank          Bank        — its book, its hub, its two connections, its day's phases
node/csm           ClearingHouse — queueing, share-cutting, netting, release, the pacs.004 round trip
node/centralbank   CentralBank — settlement, lodgements, returns, the reserve book
cmd/server         Deployment  — the phases in order, the journal, the clock, Reset, the seed
```

`node/` rather than `internal/node/` because `seed` and `provision` are outside
`cmd` and drive institutions too; keeping the packages importable is the point.

**Each node takes an `Env` at construction**, which is the 44 reaches made into a
value:

```go
// Env is what an institution needs from the deployment it runs in and could get
// from any other: a clock, somewhere to log, somewhere to report, and the two
// addresses it dials.
type Env struct {
    Now              func() time.Time
    Log              *slog.Logger
    Journal          Journal          // an interface: file, outcome, problem
    NextMsgID        func(from iso20022.BIC) string
    CentralBankBIC   iso20022.BIC
    ClearingHouseBIC iso20022.BIC
}
```

`messageContext` becomes a method on `Env`, since it is exactly `NextMsgID` plus
`Now` plus the two addresses. `Journal` is an interface so the deployment keeps
owning the report and a node keeps knowing nothing about it.

`Env` lives in a package all three import — `node` itself, with the three as
`node/bank` and so on, or a small `node/env`. It names no institution, so it
carries no crossing.

**`ops.go` splits three ways and moves with its node.** `bankOps` (70 lines) to
`node/bank`, `csmOps` (50) to `node/csm`, `settlementOps` (15) to
`node/centralbank`. Each becomes unexported in its own package, which is stronger
than today: the interface is now declared beside its single consumer.

**`messages.go` splits by use.** `withActor`/`actorOf`, `orderTypeOf`,
`unreadable`, `notProvided`, `isAbout` and the three part-namers are used by more
than one node and go to the shared package with `Env`. `creditTransferPart`,
`directDebitPart` and `pick` are the clearing house's share-cutting alone and go
to `node/csm`.

**The day engine keeps calling concrete node types.** `day.go`'s phases already
read `d.csm.work(ctx)`, `d.cb.work(ctx)` and a loop over `d.banksInOrder()`. After
the move those are `*csm.ClearingHouse`, `*centralbank.CentralBank` and
`[]*bank.Bank`, and the methods they call become exported. Nothing about the
sequence changes.

**The operator console becomes the deployment's, on the deployment's type.**
`CentralBank.Reset`, `.AdvanceDay`, `.BusinessDate` and `.Members` move off the
node. `api/centralbank.Institution` splits into the settlement agent's four
methods and an `Operator` interface carrying the other four, and `cmd/server`
provides a small adapter implementing both — which is what it does today
implicitly, and the change is that the split is now visible in the types.
`ClearingHouse.Submit` and `.Return` move the same way, onto `api/csm`'s operator
half.

## What the doors become

This is the only part with a domain decision in it, and it is two decisions.

**A bank submits its own instruction, and refuses one that is not its.**

`Bank.Submit` becomes `b.submit` — today's body at `bank.go:124`, which takes the
instruction and puts it in this bank's hub. The three checks in
`Deployment.Submit` move onto the bank and are answered from **this bank's own
rows**:

| check | today reads | after reads |
| --- | --- | --- |
| the scheme exists | the CSM's registry | the bank's own registry — `schemes` is shared by every network `Networks` mints, and a real bank knows the schemes it has joined |
| on-us | the bank's own `ResolveIdentifier` | unchanged |
| both agents are members | the CSM's roster, live | **the bank's own routing directory**, which `RefreshDirectory` fills |
| which bank submits | `d.member(submitterOf(…))` | **refused** — an instruction whose submitting side is not this bank is not this bank's to take |

The last row is the change. `payment.SubmitterOf` still decides which side
submits; what goes is the deployment silently acting as the other bank. A console
that wants to submit as bank B posts to bank B's listener, which it can already
reach.

Reading the roster from the bank's own directory instead of the CSM's live rows
is more correct, not a compromise: a member bank checks the routing directory it
subscribes to, and a stale directory is a real condition this system should be
able to be in. `phaseRefresh` runs first in every settlement day
(`day.go:210-223`) precisely so it is fresh.

**A bank refreshes its directory by collecting it, not by reading the CSM's
database.**

`Bank.RefreshDirectory` becomes an EBICS download. The clearing house already
publishes the roster and the bank already dials it; what is missing is an order
type and a handler. This closes the one place where a bank reaches another
institution's rows.

If the order type turns out to be more than a phase's worth of work, the fallback
is to keep the direct call and move it to the **deployment**, alongside the other
simulator acts, so a `bank.Bank` still cannot perform it. That is strictly better
than today and does not block the rest.

## Tasks

Ordered. Task 1 is the whole readability win and changes no behaviour; tasks 2–3
are the domain decisions; task 4 is optional and later.

1. **Extract the three node packages.** Move `bank.go`, `csm.go`,
   `centralbank.go` and their share of `ops.go` and `messages.go`. Replace
   `d *Deployment` with `env Env`. Export the methods `day.go` calls. Move the
   nine simulator reaches onto `Deployment` unchanged for now — they keep
   working; they just stop being an institution's methods.
2. **The submit door.** `Bank.Submit` refuses another bank's instruction, and
   the scheme, on-us and membership checks are answered from the bank's own
   rows. `Deployment.Submit` keeps the routing prelude and becomes the operator
   console's act, on the operator surface.
3. **The directory door.** A roster download order type, or the fallback above.
4. **Optional, later: `cmd/bank`, `cmd/csm`, `cmd/centralbank`.** Each opens ONE
   database through the constructor that already exists, builds one node, serves
   one listener. `cmd/server` keeps importing all three and running them in one
   process. Nothing above forecloses this and nothing above requires it.

Tasks 1 and 4 are the modular monolith. Tasks 2 and 3 are what makes the parts
honest, and they are worth doing whether or not 4 ever happens.

## What this does not do

**It does not split the business day.** The phases, their order, the journal and
`AdvanceDay` stay in `cmd/server`, whole. See the rule above.

**It does not split `payment`.** ADR-0006 already narrows by TYPE, which is
stronger than by package: a bank's handle cannot name `SettleCycle`. Splitting
`payment` into `payment/bank` and `payment/csm` would break the shared `core`,
the `Payment` type, the scheme registry and `translate.go`, and buys nothing for
deployment — a binary cannot run what its types cannot name.

**It does not touch the databases, the stores or the schemas.** N+2 before and
after.

**It does not move the tests.** `cmd/server`'s 13,952 test lines drive the
deployment and stay with it. They exercise the nodes through the simulator, which
is what they are for.

## Verification

- `go build ./... && go vet ./... && go test ./...` and `gofmt -l .` — green
  before and after. **Green, and the "no test edited in task 1" clause was
  wrong**: `cmd/server`'s tests are `package main` and reach node internals in
  about thirty places (`.host`, `.ops`, `.bic`, a `CentralBank` composite
  literal, a mutated `*ebics.Client` field). Those edits are mechanical — an
  accessor for a field — and two tests that could not be made mechanical are
  named below.
- **The back-pointer is gone.** `grep -rn 'Deployment' node/` returns nothing.
  This is the claim task 1 exists to make, and it holds.
- **A node cannot reach another institution — and the PACKAGE is not what
  refuses it.** The probe was run and it COMPILES: `payment` is one package, so
  a file in `node/bank` can name a `ClearingHouseNetwork`. What a bank cannot do
  is get hold of one, which is ADR-0006's guarantee by TYPE and is unchanged.
  The packages buy readability and a one-way dependency, not a second wall.
- **A bank refuses another bank's instruction** (task 2).
  `TestABankRefusesAnInstructionTheOtherSideSubmits` posts a direct debit to the
  payer's bank, where the payee's bank is the submitter, asserts the refusal
  names both banks and that nothing reached either hub, then submits the same
  request through the payee's bank.
- **Nothing gained a second implementation.** 3,418 non-test lines across
  `cmd/server` plus `node/` against 3,267 before: +151, and all of it
  constructors, accessors, `Env`, three package docs and the two `Operator`
  interfaces.

## What landed

**Task 1 — the three node packages.** `node` holds `Env`, `Journal`, the three
journal types, `WithActor`/`ActorOf`, `OrderTypeOf`, `Unreadable`, `IsAbout`,
`NotProvided` and `JoinProblemDetails`. `node/bank`, `node/csm` and
`node/centralbank` hold one institution apiece, each with its own narrowed
`ops` interface declared beside its single consumer. `api/csm` and
`api/centralbank` each split into `Institution` and `Operator`; `cmd/server`'s
`console.go` is the one adapter that is both.

Three departures from the plan, each a judgement made against the code:

- **The three part-namers were not moved; they were deleted.**
  `submitterOf`/`receiverOf`/`returnerOf` were one-line delegations to
  `payment.SubmitterOf` and its two siblings. Re-exporting them from `node`
  would have been a second name for one rule, which `CLAUDE.md` names as how
  one fact becomes three versions. The six call sites now name `payment`
  directly, which is what `CLAUDE.md` already calls the rule.
- **`Bank.collect` became `Bank.Collect(ctx, host, orderType)`**, picking the
  connection by address and refusing one it has none to, rather than taking the
  `*ebics.Client` as an argument. A bank's two connections are the whole of its
  address book, and the day now says which host it is collecting from rather
  than reaching for the field.
- **Three settlement-agent tests moved to `node/centralbank`.**
  `TestAStoreFailureAtTheAgentIsNotARefusal`, `TestALodgementRefusalIsAJudgement`
  and `TestTheSettlementAgentCannotAnswerYesWithAReason` substitute the narrowed
  view and call unexported handlers, so they cannot be written from outside the
  package. Exporting a seam for them would have been production API existing for
  a test. Their fixture is now a host, an `Env` and `payment.LodgementMessage` —
  no seeded network at all — and what went on the wire is read off the queues.
  `TestTwoFilesThatCouldNotGoOutComeBackInTheOrderTheyWereTaken` stayed in
  `cmd/server` and now builds a second `bank.Bank` over the same database with a
  dead connection, instead of mutating a private field.

**Task 2 — the submit door.** As designed, plus `payment.ErrNotTheSubmittingAgent`
(MS03 on the wire, 422 on the surface). `Deployment.Submit` keeps the scheme
lookup, the on-us-by-address guard and the routing; the on-us guard stays there
because `SubmitterOf` would otherwise answer one of two addresses that are the
same one, and "this deployment holds no bank at X" is the wrong thing to say
about an instruction whose fault is that it stays put.

**Task 3 — the fallback, not the order type.** `Deployment.RefreshDirectory`
performs it and `bank.Bank` cannot, which is what the fallback below asks for.
The order type is more than a phase's work: it needs a published-file concept on
`ebics.Server` (the shape `HAC` already has), a message format for a roster —
the first file on this transport that is not ISO 20022 — a publishing act on the
clearing house, a day phase to order the two, and a domain claim for the
learner-facing layers. It is a sub-project, and it is on the roadmap as one.

## Documentation layers

- `CLAUDE.md` — gains a section stating the rule: an institution's package holds
  what it really does, the deployment's holds the order, and the dependency runs
  one way. The *N+2 stores* section's "nothing in the domain may read across two
  institutions" gains its counterpart one layer up.
- `docs/expansion-roadmap.md` — *Structural work*, linking this record.
- An ADR once task 1 lands, extending
  [ADR-0008](../adr/0008-a-conversation-belongs-to-the-deployment.md) from a
  conversation to a business day. Task 2's refusal deserves its own paragraph in
  it: which bank may submit an instruction is a domain claim, not a layout one.
- `README.md` — *Persistence* and the architecture sections name `cmd/server` as
  the three institutions. After task 1 it is the deployment and the three are
  `node/`.
- The learner-facing layers (`hint-content.ts`, the quiz chapters) name no repo
  symbol and move only if a DOMAIN claim changes. **Task 2 changes one**: a bank
  submits only instructions where it is the submitting agent, and a bank checks
  membership against the directory it subscribes to rather than the clearing
  house's live register. Both are bankable facts and both are teachable.

## Alternatives rejected

**Split the business day too, and run three binaries with an orchestrator over
HTTP.** This was the first shape of this record. It buys real separate processes
and costs the thing the day engine is: a sequenced list whose order is
load-bearing — `day.go:273` argues why the settlement agent must be collected
from first. Turning that into three services coordinating gives every phase
boundary a failure mode, in the one part of the system that is explicitly a
simulation of a business day rather than a model of one. Task 4 gets separate
binaries without it.

**Leave `cmd/server` as one package and only fix the doors.** Cheapest, and it
closes the two real defects. It leaves the thing that prompted this: three
institutions in one package where nothing declares which lines are whose, and a
back-pointer that makes the question unanswerable by reading.

**Consumer-defined interfaces instead of moving code** — each node declares what
it needs of the deployment, all three stay in `package main`. This is ADR-0006's
rejected alternative in the same position: correct, cheap, strictly weaker. It
narrows what a node may name and leaves one package holding everything, so
"read the clearing house" is still "read `cmd/server` and filter".

**One node package with three types** rather than three packages. Keeps the
shared helpers together and loses the boundary that does the work: `csm.go`'s
share-cutting would still be importable by a bank.
