# Chapter 11 — Payment Schemes

We now have the stage — independent bank ledgers meeting at a central bank — and
the cast of accounts that money moves through. This final chapter of Part IV brings
on the actual choreography. We'll see why banks settle the *net* of their payments
rather than each one individually, follow a real payment posting by posting, and
then meet the different *schemes* — credit transfers, direct debits, and the
instant and card flows on the horizon — that all reuse the same underlying machinery.

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
rewriting the engine. The two schemes that ship today are both net-settled and worth
meeting by name.

## SEPA Credit Transfer — a push

A **credit transfer** is the salary-and-invoice workhorse: the payer's bank
initiates and *pushes* funds to the payee. No mandate is needed — you're sending
your own money. In the European SEPA system this is the *SEPA Credit Transfer*
(SCT), settling at T+1, and it corresponds to the ISO 20022 `pacs.008` interbank
message. The worked example above *was* a credit transfer; there is nothing more to
it mechanically.

(The mention of `pacs.008` points at a real-world detail worth noting: actual
interbank payments are carried as standardized ISO 20022 XML messages with codes
like `pacs.008` for a credit transfer and `pacs.003` for a direct debit. A learning
model can stand in a simple payment record for those messages and just *name* the
message each scheme corresponds to, without parsing real ISO 20022.)

## SEPA Direct Debit — a pull, with a mandate

A **direct debit** runs the other way. The payee — a utility, a gym, a streaming
service — *pulls* funds from the payer's account. Since someone reaching into your
account is obviously dangerous, a direct debit requires a **mandate**: a standing
authorization you sign in advance, naming the specific creditor allowed to collect
and often capping the amount.

Before any collection, the scheme checks that mandate: that it exists, is still
active (not revoked), matches both the payer and the named payee, and stays within
its amount limit. Fail any check and the payment is rejected outright — with
specific reasons like "mandate required," "mandate revoked," or "amount exceeds the
mandate." In SEPA this is the *SEPA Direct Debit* (SDD), settling at T+2, mapping to
the ISO 20022 `pacs.003` message.

Here's the payoff of the shared-machinery design: once the mandate checks pass, the
*postings for a direct debit are identical to a credit transfer* — debtor →
creditor, through clearing suspense and central-bank reserves, exactly as above. The
money flows the same way; only the rules about *who may initiate* and *what
authorization is required* differ. That's the whole reason schemes can be an
abstraction over one posting engine rather than separate systems.

## Returns: unwinding a settled payment

Direct debits introduce a wrinkle the credit transfer didn't have: they can be
**returned**. If the payer disputes a collection or turns out to have lacked funds,
a *return* (in SEPA, an "R-transaction") reverses the flow even after settlement. It
posts compensating transactions that move the funds back from creditor to debtor
across the central bank, fully unwinding the original and restoring both customers'
balances — the cross-bank cousin of the reversal from Chapter 5. Where an ordinary
reversal undoes a posting in one ledger, a return has to undo a flow that crossed
several ledgers and the central bank, but the principle is the same: you don't erase
history, you post a new, balancing transaction that cancels it.

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

Two further schemes show where the same foundation naturally extends:

- **Instant payments** (SEPA Instant, FedNow, Faster Payments) are real-time *gross*
  settlement, 24/7. Each payment settles individually and immediately rather than
  waiting for a clearing cycle — the "gross" settlement model named above. The
  abstraction already anticipates this; what it needs is a settlement path that, for
  a gross scheme, posts the debtor leg, the central-bank reserve move, and the
  creditor leg in one shot per payment, with no netting and no cut-off.

- **Card transactions** are an *authorize → capture → clear → settle* flow — and we
  already have its first two steps. The authorization is exactly the *hold* of
  Chapter 7: it reserves the cardholder's available balance. The capture turns that
  hold into the debtor leg of a payment, and from there clearing and settlement
  reuse the very same net machinery, because card networks net much like SEPA does.

Both extensions also motivate enforcing the account *states* of Chapter 8 on the
debit path — a frozen account should block a card authorization — and checking that a
bank actually holds enough reserves before its net settlement is allowed to post.
The themes of the whole book, in other words, converge here: the balance rule, the
two clocks, holds, account states, and immutable correction all reappear in the
machinery that moves money between banks.

With that, we've followed money from a single entry in one bank's book all the way
to final settlement across an interbank network. The last part of the book steps
back from movement to *records*: how a bank captures, audits, and reports on
everything we've watched happen.

---

*Previous: [Chapter 10 — The Interbank Network](10-the-interbank-network.md)*
*Next: [Chapter 12 — Snapshots, Audit Trails, and Statements](12-snapshots-audit-and-statements.md)*
