# Design — the interbank conversation gets an owner

Based on `main` at `21c804c`, and on the architecture review of 12 August, whose
third card this is. The two cards before it have landed —
[the business day](2026-08-11-ebics-and-the-business-day-design.md) as a declared
sequence (the rule that a business day is a declared sequence) and
one type per institution (the rule of one type per institution,
the store-per-institution design) — and this one is what they
leave exposed: the acts are each owned now, and the ORDER they go in is not.

## The defect, stated once

**A payment's route through three institutions is written out by every caller
that drives it, and the copies have drifted.**

Two callers drive it by calling the domain directly: `seed`, which builds the
sample deployment, and `payment`'s own suite. Each wrote the conversation out,
and `seed/seed.go` names the hazard in its own comment —

> the same payment with two different histories depending on which built it,
> which is exactly the divergence a sample dataset must not have

— which was true when it was written and is still true, because the fix went
into one copy.

| conversation | `seed` | the suite | they disagree on |
| --- | --- | --- | --- |
| initiate | 5 acts, ~31 lines | 4 acts, ~18 lines | the submitting bank is never told |
| reject | ~8 lines | ~26 lines | the suite skips a bank holding no copy |
| return | ~31 lines | ~58 lines | only the suite guards `AllowsReturn` |
| settle | ~29 lines | ~49 lines, three helpers | one bank on both sides is settled once, or twice |

**The submitter's copy is `Accepted` in the seed data and `Initiated` in every
test that builds a payment.** The suite stops at `AcceptAtCSM`; the seed goes on
to `AcceptAtBank`, which is what the running deployment does when the ACCP
arrives.

That divergence was measured rather than argued about. Adding the fifth act to
the suite's helper leaves the whole `payment` suite green except **three audit
expectations**, and each of the three gains a `payment.accepted` on the
submitting bank's trail:

```
Bank A's trail: got  … payment.initiated payment.accepted payment.initiated …
                want … payment.initiated                  payment.initiated …
```

No test was asserting a different rule. The suite was under-driving the
conversation, and then auditing what it had failed to drive.

### The role rules, counted

Underneath the conversation is one rule with three names — which of a payment's
two agents is the submitting side — and it is written six times.

| rule | owned | copies |
| --- | --- | --- |
| the submitting side | — | `cmd/server/messages.go:73`, `seed/seed.go:266`, `payment/system_test.go:4418` |
| the receiving side | `payment.ReturnerOf` | `cmd/server/messages.go:81`, `payment/system_test.go:4409`, `payment/recon/recon.go:637` |
| the other agent | — | five inline `if other == x { other = y }` |

`payment/scheme.go:76` already wrote the argument for owning them — *"Two copies
of that rule would be free to drift"* — and applied it to one.
`cmd/server/messages.go:90` is the worked example for the other two: a local
name delegating to `payment`, so the rule is in one place and the call site
still reads in the vocabulary of the act.

**`ReturnerOf` and the receiver rule are the same body.** Pull returns the
debtor's agent, push the creditor's. That is not a coincidence to be collapsed
away — the party whose bank sends a settled payment back IS the party that
received it — but it does mean there is one rule here and not three, and its
complement.

## What the review got wrong

The card counts three implementations and names `cmd/server/bank.go` as the
third. It is not one. Since sub-project 21 the deployment's institutions reach
these acts off **files**: `AcceptAtBank` at `bank.go:471` fires on a `pacs.002`
carrying ACCP, and each institution performs its own half when its own transport
hands it something. There is no sequence written down there to drift.

So the real path is not a copy of the conversation — it is the thing the two
direct-call drivers stand in for. That changes what the owner is for, and who
may call it.

## The shape

### The role rules

Four exported functions in `payment/scheme.go`, beside `ReturnerOf`:

```go
// SubmitterOf is the party whose bank hands a payment to the clearing house.
func SubmitterOf(scheme Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC

// ReceiverOf is the bank a released file is addressed to: the other one.
func ReceiverOf(scheme Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC

// ReturnerOf is the party whose bank sends a settled payment back, which is the
// receiving side under the name of a different act.
func ReturnerOf(scheme Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC

// CounterpartyOf is a payment's other agent, and the same one when a bank is both.
func CounterpartyOf(self, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC
```

They take a `Scheme` rather than a `SchemeID` because the resolution is the
caller's: a bank resolves in its own registry and the clearing house in its own,
and a rule that looked the scheme up itself would have to be told whose registry
to look in.

`payment/recon` keeps its own function, because the BIC is half of what it
returns — the other half is what that bank was never able to do with the
instruction it did not get — and it delegates the half that is the rule.

### The conversation

A new package, `payment/flow`, over `*payment.Networks`:

```go
func Initiate(ctx context.Context, nets *payment.Networks, req payment.InitiatePaymentRequest) (payment.Payment, error)
func Reject(ctx context.Context, nets *payment.Networks, id payment.PaymentID, code iso20022.StatusReason, reason string) (payment.Payment, error)
func Return(ctx context.Context, nets *payment.Networks, id payment.PaymentID, reason string) (payment.Payment, error)
func Settle(ctx context.Context, nets *payment.Networks, id payment.CycleID, agent iso20022.BIC) error
```

**Every act stays in its own unit of work**, which is the whole of what
the held-files durability design and
the rule that a queue is a table whose bytes stay opaque require: this
package calls the institutions in order and opens nothing itself. It holds no
store, no transaction and no lock.

**Who may call it is the point.** A caller that IS the deployment — the seed,
the suites — and no institution. `provision` is the precedent and the same
shape: a package that writes one bank's row at each of three institutions, over
`*payment.Networks`, with a suite that says it can only be driven over a whole
deployment. `payment/recon` is the other, and stricter — the one view no
institution may have.

**Why not in `payment`.** A `payment` symbol that drove three institutions in
sequence would be reachable from a holder of one, which is exactly the crossing
the rule of one type per institution and the store-per-institution design made unrepresentable one layer down. The suite is
`package payment_test`, so a subpackage costs it nothing.

## Phasing

Each phase leaves the tree green and deletes what it replaces.

1. **The role rules.** — `done`. The four are in `payment/scheme.go` and the six
   copies are gone; `cmd/server` keeps its local names as delegations, the way
   it already did for the returner, and `recon` keeps its own because the BIC is
   half of what it answers. A seventh copy turned up in `seed_test.go`, keyed
   off a scheme id literal rather than the direction. `payment/scheme_test.go`
   is the rule's own suite, including the deployment where one bank is both
   sides.
2. **`Initiate` and `Reject`.** — `done`, and the measurement above is what
   happened: three audit expectations, each gaining the submitting bank's
   acceptance. Two further drifts came out of writing the sequence once — the
   seed relayed the request rather than the payment, dropping the payer's
   back-filled address, and its rejection reached the submitting bank alone. The
   suite's trails are now asserted per bank, because which bank submitted
   decides where the acceptance falls.
3. **`Return` and `Settle`.** — `done`. `runCycle` keeps the open-submit-close it
   is for and hands the rest over, so the legs check that decides whether there
   is anything to settle sits with the settlement. `returnTheWholeWay` is
   `returnWholePayment` for a fixture that cannot carry on past a failure, and
   the three helpers the split drives — `bookTheAdvices`, `payTheCreditors`,
   `settleCycle` — stay, because the tests about the split call the halves
   directly.
4. **The ruling and the docs.** — `done`.
   the interbank-conversation design,
   `CLAUDE.md`'s store section, and the roadmap entry.

## What this does not do

**It does not touch the transport path.** `cmd/server`'s institutions keep
reaching these acts off the files they are handed, one half each. `flow` is not
on that path and nothing in `cmd/server`'s production code will import it.

**It does not give `payment` a method that plays two institutions.** The
deliberate absence its comments record stays; what changes is that the callers
who need the sequence share one instead of five.

**It does not collapse the receiver and the returner into one name.** They are
one rule and two acts, and a single name would have to be wrong about one of
them at every call site.
