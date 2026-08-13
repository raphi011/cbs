# ADR-0010: An institution's package holds what it does; the deployment's holds the order

**Status:** accepted
**Date:** 2026-08-13
**From:** [a package per institution](../specs/2026-08-13-a-package-per-institution-design.md)

## Context

Six layers were already institution-scoped, each with a ruling behind it: the
database ([ADR-0003](0003-an-institutions-obligations-live-in-its-database.md)),
the store type ([ADR-0007](0007-a-store-per-institution.md)), the domain handle
([ADR-0006](0006-one-type-per-institution.md)), the transport state
([ADR-0004](0004-a-queue-is-a-table-and-stays-opaque.md)), the HTTP surface and
the listener. The composition root was not: `cmd/server` was one package in
which three institutions and their simulator were interleaved, and each
institution opened with `d *Deployment`.

Measured, that pointer carried **53 reaches**, and they were three different
things. Forty-four were an environment any deployment could supply — a clock, a
logger, an id minter, a report sink, and the two addresses an institution dials
whether it shares a process with them or not. Five were the operator's console,
correctly identified and served on the settlement agent's receiver. Four were
doors, and two of those were defects rather than layout: a customer posting to
one bank's console could make **another** bank submit the instruction, and a
bank read the clearing house's roster in process.

The cycle was invisible because everything was in one package. Nothing declared
which lines were whose, so "read what the clearing house does" was "read
`cmd/server` and filter".

## Decision

**An institution's package holds what that institution really does. The
deployment's package holds the order they do it in, and it is the one caller
allowed to see all of them.**

This is [ADR-0008](0008-a-conversation-belongs-to-the-deployment.md)'s rule one
layer up, and it gets the same answer.

- **Three packages, one per institution**: `node/bank`, `node/csm`,
  `node/centralbank`. `node` itself carries what none of them owns — the order
  type a document travels under, the answer to a file that would not parse, the
  mark that says whose work a unit of work is.
- **Each node takes a `node.Env` at construction**: the forty-four reaches made
  into a value. The journal is an interface, so the report stays the
  deployment's and a node knows nothing about one. The claim to preserve is
  `grep -rn 'Deployment' node/`, which finds nothing.
- **The business day stays whole, in `cmd/server`.** The phases, their order,
  the journal, `Reset` and the seed are the simulator, and a simulator that
  could not see every institution would not be one. What changes is the
  DIRECTION, not the reach.
- **The deployment's acts are served through an `Operator` interface.**
  `api/csm` and `api/centralbank` each declare the institution's own acts and,
  separately, the console's; one adapter in `cmd/server` is both. `Reset`,
  `AdvanceDay`, `BusinessDate` and the member listing were already documented as
  the deployment's — now the type system says so.
- **Which bank may submit an instruction is a domain claim, not a layout one.**
  `bank.Bank.Submit` answers the scheme, the on-us and the membership questions
  from that bank's own rows and refuses an instruction the scheme has the other
  side send (`payment.ErrNotTheSubmittingAgent`). `payment.SubmitterOf` still
  decides which side that is; what goes is the deployment silently acting as the
  other bank. A console that wants to submit as bank B posts to bank B, which it
  can already reach.

**Not consumer-defined interfaces with all three left in `package main`.** That
is ADR-0006's rejected alternative in the same position: correct, cheap, and
strictly weaker. It narrows what a node may name and leaves one package holding
everything.

**Not three binaries with an orchestrator over HTTP.** That buys separate
processes and costs the thing the day engine is — a sequenced list whose order
is load-bearing — by giving every phase boundary a failure mode, in the one part
of the system that is explicitly a simulation of a business day rather than a
model of one. **And separate binaries are not wanted either.** One process is the
deployment this repository is; what the split buys is that reading one
institution is reading one package, not that anyone is going to run three.
The shape happens to survive — `node/` is importable and the
single-institution store and network constructors already exist — and that is
an observation about the arrangement rather than a plan.

## Consequences

**A membership check now reads what a bank has.** The submitting bank asks its
own routing directory rather than the clearing house's live register. That is
more correct, not a compromise: a member checks the directory it subscribes to,
and a stale directory becomes a state this system can be in and can teach.

**The one breach this ruling left is closed.** `RefreshDirectory` read the
clearing house's roster in process, so the DEPLOYMENT performed it and a
`bank.Bank` could not. The clearing house now publishes its roster on its own
host under `HRD` and a member collects it, which leaves nothing on the directory
path that is not a file — see
[the design record](../specs/2026-08-13-a-roster-download-order-type-design.md).
What the deployment keeps is the ORDER: publish, then collect.

**A node's own behaviour is testable without a deployment.** Three settlement
agent tests moved into `node/centralbank`, where the narrowed view they
substitute is in-package and the fixture is a host and an `Env` rather than a
seeded network.

**The compile-time guard is the TYPE, not the package.** A file in `node/bank`
can still name a `ClearingHouseNetwork`, because `payment` is one package;
what it cannot do is get hold of one. That is ADR-0006's guarantee, unchanged
— the packages buy readability and a one-way dependency, not a second wall.
