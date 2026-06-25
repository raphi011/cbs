# Chapter 11 — Payment Schemes

We now have the stage — independent bank ledgers meeting at a central bank — and
the cast of accounts that money moves through. This final chapter of Part IV brings
on the actual choreography. We'll see why banks settle the *net* of their payments
rather than each one individually, follow a real payment posting by posting, and
then meet the abstract *scheme model* — the small set of axes along which any
payment product differs — that the named schemes in the chapters to follow all
share. SEPA's credit transfer and direct debit come first, in Chapter 12; card
transactions get their own treatment in Chapter 13.

## Netting: the whole point of clearing

Start with the question the last chapter deferred: why net? Imagine a single
clearing cycle with just two payments between two banks:

- Alice (Bank A) → Bob (Bank B): **$300**
- Bob (Bank B) → Alice (Bank A): **$100**

The *customers* must move by the full, gross amounts — Alice is $300 poorer and
$100 richer, Bob the reverse. Their deposit accounts reflect every cent of both
payments. But the *banks* don't need to shuffle reserves twice. They compute each
bank's net position:

```
net[A] = −300 + 100 = −200    // Bank A owes 200, net
net[B] = +300 − 100 = +200    // Bank B is owed 200, net
```

Only **$200** of central-bank reserves actually moves, not $400. Across a real
cycle with millions of payments, this collapses an enormous gross flow into a tiny
net one — vastly less reserve money has to change hands, which is faster, cheaper,
and less risky. This is precisely why clearing exists as a separate step before
settlement: clearing is where the netting happens.

There's also a beautiful self-checking property here. The net positions of all
banks in a cycle *always sum to zero* — every dollar one bank owes is a dollar
another is owed. That is exactly what guarantees the central-bank settlement
transaction will balance in the double-entry sense of Chapter 2. Netting and the
balance rule are two views of the same conservation law.

## Following the postings: a credit transfer

Let's make a single payment fully concrete. Alice at Bank A pays Bob at Bank B
€30.00 — `3000` in minor units (Chapter 4). Watch the accounts from Chapter 10 move.

**1. Initiation** — recorded in Bank A's ledger only, value-dated to the settlement
date:

```
Bank A:  Debit  Alice (Liability)             3000   // Alice's deposit falls
         Credit Clearing Suspense (Liability) 3000   // Bank A now owes the network
```

Alice's money has left her account, but it hasn't reached Bob — it's parked in
Bank A's clearing suspense, the "in flight" account. The transaction balances:
3000 debit, 3000 credit.

**2. Clearing** — at the cut-off, net positions are computed. With just this
payment, `net[A] = −3000` and `net[B] = +3000`. No money moves; this is pure
agreement.

**3. Settlement** — three balanced transactions make the money final:

```
Central Bank:  Debit  Reserve: Bank A (Liability) 3000   // A's reserves fall
               Credit Reserve: Bank B (Liability) 3000   // B's reserves rise

Bank A:        Debit  Clearing Suspense 3000             // suspense clears to zero
               Credit Reserve at CB     3000             // A's reserve asset falls in step

Bank B:        Debit  Clearing Suspense 3000             // release the funds…
               Credit Bob (Liability)   3000             // …into Bob's account
               Debit  Reserve at CB     3000             // B's reserve asset rises
               Credit Clearing Suspense 3000             // and its suspense clears
```

When the dust settles, both banks' clearing-suspense accounts are back to zero —
nothing is "in flight" any more — and each bank's *Reserve at Central Bank* asset
again equals the central bank's matching *Reserve: \<Bank\>* liability. The
mirror-image entries agree; the books reconcile (Chapter 10); the payment is final.
Trace any single account and you'll find its debits and credits balance, and trace
the whole system and you'll find no value was created or destroyed — it simply moved
from Alice to Bob by way of reserves at the central bank.

## Schemes: different products, shared machinery

"Bank transfer" is not one thing. A salary credit, a utility's monthly collection,
an instant peer-to-peer payment, and a card purchase are all "payments," but they
behave differently: who starts them, whether the payer pre-authorized them, how
fast they settle, whether they can be clawed back. Banking captures these
differences as distinct **payment schemes**, each with its own rules.

What's elegant is that the schemes vary along just a few axes, and underneath they
share the same posting machinery we just traced. The axes that matter:

- **Direction — push or pull.** In a *push* payment, the payer's bank initiates and
  sends the funds. In a *pull* payment, the payee's bank initiates and *collects*
  from the payer. This governs who starts the flow and whether prior authorization
  is needed — but, crucially, money always ends up flowing **debtor → creditor**
  regardless of who pushed the button. That's why the same postings serve both.

- **Settlement model — net or gross.** A *net* scheme batches payments into clearing
  cycles and settles the net, as we just saw. A *gross* scheme settles each payment
  individually and immediately, with no netting and no waiting for a cut-off.

- **Mandate required?** Some schemes require the payer to have signed a standing
  authorization (a *mandate*) before the payee may pull funds.

- **Returns allowed?** Some schemes let a settled payment be clawed back within a
  window.

- **Settlement delay** — how long after booking the funds take economic effect,
  i.e. the value date (Chapter 6) the postings carry.

In our reference library, these axes are literally an interface that each scheme
implements; the network orchestrator drives any scheme without knowing its
specifics, and adding a new product means describing it along these axes rather than
rewriting the engine. The named schemes that follow — SEPA in Chapter 12, card flows
in Chapter 13 — each slot into this model rather than standing apart from it.

## What's deliberately left out, and what comes next

It's worth being explicit that a *learning* model of all this makes deliberate
simplifications — the kind a production processor cannot. The reference library, for
instance, uses one currency and no foreign exchange; routes payments by simple
identifiers rather than validating real IBANs and BICs; stands in a plain payment
record for ISO 20022 messages; settles returns immediately rather than in a later
batch; and serializes whole operations under a lock instead of using the locked
settlement windows or two-phase commit a real real-time gross settlement system
would demand. None of these change the *concepts* in this chapter; they just keep the
model small enough to read in an afternoon.

One further scheme shows where the same foundation naturally extends:

- **Instant payments** (SEPA Instant, FedNow) are real-time *gross*
  settlement, 24/7. Each payment settles individually and immediately rather than
  waiting for a clearing cycle — the "gross" settlement model named above. (One trap:
  not every *instant* scheme is gross — UK Faster Payments feels instant to customers
  but settles on a deferred *net* basis behind the scenes.) The
  abstraction already anticipates this; what it needs is a settlement path that, for
  a gross scheme, posts the debtor leg, the central-bank reserve move, and the
  creditor leg in one shot per payment, with no netting and no cut-off.

Card transactions get their own chapter next (Chapter 13).

Instant payments and card transactions alike motivate enforcing the account *states* of Chapter 8 on the
debit path — an account in a restricted state should block payment initiation — and
checking that a bank actually holds enough reserves before its net settlement is
allowed to post. The themes of the whole book, in other words, converge here: the
balance rule, the two clocks, holds, account states, and immutable correction all
reappear in the machinery that moves money between banks.

With that, the scheme model is in place. Chapter 12 puts it to work on the two SEPA
products — the credit transfer push and the direct debit pull — and Chapter 13
extends it to card flows.

