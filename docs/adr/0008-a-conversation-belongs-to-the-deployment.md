# ADR-0008: A conversation between institutions belongs to the deployment

**Status:** accepted
**Date:** 2026-08-13
**From:** architecture review — the interbank choreography had no owner

## Context

[ADR-0006](0006-one-type-per-institution.md) and
[ADR-0007](0007-a-store-per-institution.md) gave every act an owner: one type
per institution, one store per institution, and a crossing that does not
compile. What neither of them gave an owner to is the **order** the acts go in.

A payment's route through three institutions — the submitting bank's leg, the
receiving bank's, the clearing house's copy and its decision, the submitting
bank told what was decided — is a sequence, and every caller that drove it wrote
the sequence out. There were two: `seed`, which builds the sample deployment,
and `payment`'s own suite.

They had drifted. The seed reached `AcceptAtBank` and the suite stopped at
`AcceptAtCSM`, so the same payment was `Accepted` at its submitting bank in the
seed data and `Initiated` in every test that built one. `seed/seed.go` named
that hazard in its own comment — *"the same payment with two different histories
depending on which built it"* — and the fix had gone into one copy. Two more
came out of writing it once: the seed relayed the request rather than the
payment, dropping the payer's back-filled address on the way to the other two
institutions, and a rejection reached the submitting bank alone.

The deliberate absence was real and is not what drifted. No institution has a
method that plays another, and the suite's comments say so at each helper. The
absence just left nowhere for the order to live.

## Decision

**The sequence belongs to a caller that IS the deployment, in a package of its
own.** `payment/flow` holds the four conversations — `Initiate`, `Reject`,
`Return`, `Settle` — over `*payment.Networks`.

- **Every act stays the institution's own, in its own unit of work.** The
  package opens no transaction, holds no store and takes no lock. What it owns
  is the order, which is what ADR-0003 and ADR-0004 require of anything spanning
  two institutions.
- **No institution may call it**, and nothing on the transport path does. In a
  deployment each institution performs its own half when its own EBICS transport
  hands it something; `flow` is what a caller with no transport stands in with.
- **`provision` is the precedent**, and the same shape: a package that writes one
  bank's row at each of three institutions, over `*payment.Networks`, driven
  only over a whole deployment. `payment/recon` is the stricter sibling — the one
  view no institution may have.

**Not a method on `payment`.** A symbol there that drove three institutions in
sequence would be reachable from a holder of one, which is the crossing ADR-0006
and ADR-0007 made unrepresentable a layer down. A subpackage costs the suite
nothing: it is `package payment_test` already.

## Consequences

**A drift between two drivers becomes impossible rather than caught.** The seed
and the suite build the same payment because they call the same function. What
is left to disagree about is the fixtures, not the history.

**A bank's audit trail gained an event, and the trails stopped being one
shape.** The submitting bank records that it was told, so a trail now depends on
which bank submitted — asserted per bank in `payment/audit_test.go` where one
list was asserted twice.

**The direct-call path and the transport path are still two paths.** `flow`
hands the receiving bank its instruction at initiation, where a deployment hands
it at release; that difference predates this record and this record does not
close it. What it does do is give it ONE place to be stated.

**A fifth conversation goes here, not into a fixture.** The test that reaches
for a sequence no institution owns is the signal that this package is short one.
