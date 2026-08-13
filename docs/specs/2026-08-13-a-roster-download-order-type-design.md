# Design — a roster download order type

Based on `main` at `a80282f`. This closes the one breach
[the package-per-institution design](2026-08-13-a-package-per-institution-design.md)
left open — its task 3, in the primary form the fallback stood in for — and
retires the consequence
the package-per-institution design
records under *One breach is left*.

## The defect, stated once

**A member bank's copy of the routing directory is filled by reading the
clearing house's rows in the same process.**

`Deployment.RefreshDirectory` calls `d.csm.Network().ListRosterEntries` and
hands what comes back to a `bank.Bank`. The DEPLOYMENT performs it, so no
institution's code crosses — which is why this is a breach and not a defect —
but the crossing is still there, one layer up, and it is the only thing on the
directory path that does not travel as a file. Everything else a member learns
from the clearing house it collects over EBICS.

## The design

**The clearing house PUBLISHES its routing table; every member COLLECTS it.**

Four pieces, and one of them is a domain claim rather than a mechanism.

### A published file is not a queue

`ebics.Server` answers three kinds of download today, and the third is the shape
this needs:

| download | answered from | consumed |
| --- | --- | --- |
| `C53`, `BTD` | the subscriber's queue | yes — the rows are deleted |
| `HAC` | the order log | no — it is a report |
| **`HRD`** | **a snapshot the host offers** | **no** |

A published file is the same bytes for every subscriber and collecting it empties
nothing. That is the whole difference from a queue, and it is what makes the
roster a *directory* rather than a message addressed to each member in turn.

`Server.Publish(t, payload)` replaces what the host offers; `Server.Published(sub, t)`
hands it over. Enrolment is still required — an outsider gets
`InvalidUserOrUserState` — because a published file is published *to the
subscribers*, not to the world.

**The bytes live in memory**, beside `subs` and cleared by the same `Reset`. Two
reasons: the roster is derivable from the clearing house's own rows at any
instant, so nothing is lost across a restart that republishes; and a table would
be a change to `csm/0001_init.sql`, which is a documentation change to chapters
15 and 16 and the README for state that is not an obligation. The enrolments are
already in-memory transport state rebuilt at boot from the roster rows, and this
is the same value derived from the same rows.

### `HRD`, and it is not ISO 20022

**This is the first file on this transport that is not an ISO 20022 message**,
and that is protocol-correct rather than a compromise. Real EBICS downloads a
whole family of host data that is not ISO 20022 either — `HPD`, `HKD`, `HTD` are
EBICS's own XML — and a scheme's routing table is that kind of file: parameter
data about the host, identical for every subscriber, carrying no payment and no
party.

Real EBICS defines no order type for a scheme's member directory, because in the
world a CSM distributes one out of band or under a type of its own. `HRD` is this
scheme's own type, in the `H` family the standard keeps for host data, and `HAA`
— which this repository does not implement — is how a subscriber would discover
that a host offers it.

The file is JSON: the transport's envelope is already JSON, and inventing an
`iso20022` document for something that is not an ISO 20022 message would be worse
than saying plainly that this one is not. `payment.RosterFile` builds it and
`payment.ReadRosterFile` reads it, which is `payment`'s existing shape — the
domain builds what goes on the wire and a node uploads it.

It carries the publisher, the instant, and the roster as it stood. A `RosterEntry`
travels whole — BIC, allocation, assets, admission reference, admitted-at — because
the file IS the roster; what the collecting bank keeps is still the two columns
`RefreshDirectoryTx` already writes.

### Publishing is an act, and the deployment orders it

`csm.ClearingHouse.PublishRoster` reads this institution's own roster and offers
it on its own host. It is called at three moments, all of them the deployment's:

- **at boot**, in `NewDeployment`, beside the enrolments and for their reason —
  in-memory transport state is rebuilt from the rows it is derived from;
- **when the roster changes**, in `AddBank`, so a bank admitted this morning is in
  the table its neighbours collect this morning;
- **at the start of every clearing day**, as the day's first phase.

The day gains a phase. `publish` runs before `refresh`, both settlement-only, and
the pair is what `Deployment.Subscribe` is:

```
publish   the clearing house offers the scheme's routing table
refresh   every member collects it and replaces its own copy
```

Two phases rather than one body doing both, because the ORDER is the content and
the day's phase list is where this repository states an order. `refresh` loops the
**subscribers** rather than every bank this deployment holds: a bank the roster
does not name has no queue at the clearing house and cannot collect from it,
which was invisible while the deployment did the reading.

### The bank's own act

`bank.Bank.RefreshDirectory(ctx)` collects `HRD` on the connection it already
dials, reads the file, and replaces its copy. It then satisfies
`api/bank.Institution` directly, so `bankConsole` and `Deployment.RefreshDirectory`
both disappear and `TakeDirectory` — which existed for the deployment to hand a
bank rows it had read — goes with them.

A clearing house publishing nothing is `bank.ErrNoRoutingTable` and the bank
**keeps the copy it has**. Stale is a state this system models; empty is not one
it should be put into by a host that has not published yet.

## What this does not do

**It does not change what a member keeps.** The copy is still an allocation and a
BIC per row, still replaced wholesale, still stale between two collections. Every
claim in `routing-directory` about staleness holds unchanged; what changes is how
the snapshot arrives.

**It does not touch the schemas.** N+2 before and after, and no new table.

**It does not remove `provision.Subscribe`.** That one delivers the roster over
`*payment.Networks` with no transport at all, and it is the only way
`store/storetest` — which has no hosts, no listeners and no deployment — can put
a directory in place. It is a deployment-level caller under
the interbank-conversation design, like the seed
and the suites, and it is not on the payment path.

## Verification

- `go build ./... && go vet ./... && gofmt -l . && go test -count=1 ./...`, and
  `cd web && npm run test && npm run typecheck && npm run lint`.
- **`Deployment.RefreshDirectory` does not exist**, and neither does
  `bankConsole`. That is the claim this work exists to make.
- **A bank routes from a stale copy.** The existing
  `TestABankAdmittedAfterTheLastRefreshCannotBePaidUntilTheNextOne` says it and
  keeps saying it through the transport.
- **A refresh is one file collected and it is not an ISO 20022 message.** The tap
  in `cmd/server`'s harness records both directions, so the assertion is
  direct — and it replaces the old one, which said a refresh put *nothing* on the
  wire.
- **A bank whose clearing house has published nothing keeps its copy**, and says
  so with `bank.ErrNoRoutingTable`.
- **Collecting does not consume.** Two members collect the same snapshot, and a
  member that collects twice gets it twice.

## Documentation layers

- `CLAUDE.md` — the *modular monolith* section's "One known breach remains"
  paragraph comes out.
- `docs/expansion-roadmap.md` — the *A roster download order type* entry moves out
  of *Structural work*.
- The learner-facing layers: `routing-directory` and `routing-roster` in
  `hint-content.ts`, and chapter 12's routing question. Both already teach "a
  member subscribes and pulls a snapshot", so what is new is that the snapshot is
  a **file collected over the same file transfer everything else travels on**, and
  that it is not a payment message. That is a bankable fact and it is the reason
  this is a domain change and not only a structural one.

## What landed

As designed. Three things worth knowing that the plan above does not say:

- **`Deployment.Subscribe` replaced `RefreshDirectory` on the seed's interface**,
  rather than the seed calling the two phases itself. `seed.Deployment` now asks
  for one deployment-level act — publish, then every member collects — which is
  `CarryToClearing`'s shape, and `seed_test`'s own deployment satisfies it with
  `provision.Subscribe`, having no host to publish on.
- **The day's chain tests grew two hops each.** `TestTheCreditTransferChainIs…`
  names every file that crosses in order, and the routing table is now two of
  them. They carry `notAMessage` where the others carry a message definition,
  which is the assertion that the file is not one.
- **`TakeDirectory` is gone rather than kept.** It existed for the deployment to
  hand a bank rows it had read; the collection is now the whole act, and the
  copy-taking is three lines inside it.

## Alternatives rejected

**Enqueue the roster to each member.** No new concept at all: the clearing house
puts a roster file in every member's queue and `BTD` collects it with everything
else. It makes the directory a message addressed to each member in turn, which is
the thing a published directory is not — and collecting it would consume it, so
"what is the current table" would have no answer a member could ask twice. It also
lands in the day's `collection` phase, at the far end of the day from where a
refresh belongs.

**A published-files table in the clearing house's schema.** Durable across a
restart, and it buys nothing: the roster is derivable from `roster_entries` at any
instant, and a republish at boot is one line. It would cost a migration to a
schema whose absences are teaching material.

**Keep `TakeDirectory` and have the deployment collect.** Strictly worse than the
fallback it would replace: the crossing would be gone but a bank would still not
perform its own subscription, and `api/bank.Institution` would still need an
adapter.
