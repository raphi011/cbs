# ADR-0006: One type per institution, and the identity guards what is left

**Status:** accepted
**Date:** 2026-08-12
**From:** architecture review — `payment.Network` was one type for three institutions

## Context

`payment.Network` carried all 107 exported acts of all three institutions.
Which of them a caller might legitimately perform was answered three ways, and
two of them at runtime:

- `Identity` refused another institution's act through `Network.self`,
  `Network.clearingHouse` and `Network.centralBankBook` — ten sites,
  `ErrNotThisInstitutionsAct`.
- `sqlite.ErrNotInThisShape` refused a table the institution's schema never
  created, which is what caught the acts no identity guard sits on.
- `cmd/server/ops.go` narrowed by METHOD at compile time — and only there.
  `Bank.Network()` handed the whole 107 to the HTTP surface, so `api/bank`
  could name `SettleCycle`; `seed`, `provision` and every test fixture likewise.

The compile-time mechanism covered one package and was bypassed by the accessor
that package exposed. Measured against the tree, the interfaces in `ops.go` were
also not reusable as the review supposed: `api/bank` shares 3 of its 17 methods
with `bankOps`, and `api/centralbank` shares NONE of its 5 with `settlementOps` —
one reads settlements, the other posts them.

## Decision

**An act belongs to the institution that performs it, and that institution is a
TYPE.**

`BankNetwork`, `ClearingHouseNetwork` and `CentralBankNetwork` each embed the
core, which keeps what all three do. 107 acts divide 53 / 27 / 15, leaving 15 on
the core: reading a payment, listing payments, the audit trail, the scheme
registry, the four message renderers, and the store and clock.

A bank's handle reaches 68 acts, the clearing house's 42, the settlement agent's
30. `Networks` mints one per institution and is the only thing that mints them.

**The embedded field is spelled `core`, not `Network`.** An embedded field takes
the name of its type, and the identity lives on the core; spelled `Network`, a
composite literal in any package could give a bank's handle the clearing house's
core and get a handle whose methods and whose identity disagree. `type core =
Network` makes that assembly this package's alone. `Network` stays exported, so
every reference to it in prose and in `docs/` remains true.

**The identity guards stay, and guard exactly that.** They are accessors as well
as guards — `self` returns the acting participant — so they could not be deleted
even if the crossing were closed. What they now refuse is the assembled handle,
which `export_test.go` hands to the four suites that measure them and to nothing
else.

**`ops.go` narrows further, and its subject changed.** It no longer says which
institution may act; it says which of one institution's acts a BUSINESS DAY
performs. A member bank reaches 68 and the day's phases need 19 — the other 49
are its customers'. Adding a method there is now a claim that the day performs
it.

## Consequences

**Four packages gained the narrowing one had.** `api`, `seed`, `provision` and
every test fixture are held by the same types `cmd/server` is. A bank surface
naming `SettleCycle` fails to build, which a probe confirms rather than a
comment asserting it.

**Four suites changed subject.** `identity_test.go`'s two tables,
`TestAgeingIsNotAnActTheOtherTwoInstitutionsCanPerform` and
`TestReconcileIsNotAnActTheOtherTwoInstitutionsCanPerform` asserted at runtime
what is now a build failure. Each drives the assembled handle instead, which is
the crossing that is left.

**Three fixtures are held by a method rather than a type.** `api.AuditPage` takes
an `AuditReader`; the audit fixture, the scheme-registry table and the listener
identity check each name the one question they ask of all three. Reading one's
own trail is the rare act that is every institution's, and saying so is clearer
than the concrete type was.

**`ErrNotInThisShape` was left the odd one out**, refusing at runtime what these
types refuse at compile time one layer up, because `payment.Tx` was one interface
over three schemas — the same shape this record removes from `Network`, still in
place underneath it. [ADR-0007](0007-a-store-per-institution.md) is that shape
removed, and the error is gone.

## What it costs

**The core is 15 methods three institutions share, and that is a judgement.**
`GetPayment` was on all three because every institution was said to keep its own
copy of a payment's row. The settlement agent does not, and
[ADR-0007](0007-a-store-per-institution.md) pushed that pair of readers down onto
the two institutions that do — three lines duplicated, against a handle that can
no longer name a row its schema does not hold.

**A zero-value handle is still constructible.** `BankNetwork{}` compiles
anywhere; its identity is `roleUnset`, so the first act refuses naming "no
institution". Removing that would need an unexported constructor and a nil check
at every use, which is a worse trade than the refusal.

## Alternatives rejected

**Moving `ops.go`'s three interfaces into `payment`,** which is what the review
proposed. Measured, the two consumers of each share almost nothing — 3 methods of
17 for a bank, 0 of 5 for the settlement agent — so one shared narrowing would
have been a union of two disjoint halves, and a union is how an interface stops
meaning anything.

**Consumer-defined view interfaces in each `api` package.** Correct, cheap, and
strictly weaker: it narrows what a surface may name and leaves `Network` one type
for three institutions, so `seed`, `provision` and every fixture keep the whole
107. It remains available ON TOP of this and is not ruled out.

**Renaming the core to `network` to unexport it.** It closes the same hole the
`core` alias does, at the price of 42 references outside the package, `README.md`
and two ADRs — a prose sweep for a hole only a deliberately assembled handle can
reach.
