# Chapter 12 — Snapshots, Audit Trails, and Statements

We've followed money from a single balanced entry all the way to final settlement
across an interbank network. What remains is the bank's memory: how it captures
balances at the close of each day, how it keeps an unbreakable record of everything
that ever happened, and how all of it is distilled into the one artifact the
customer actually sees — the monthly statement. This closing chapter is about
turning a living ledger into trustworthy reports.

## End-of-day snapshots

A bank computes balances constantly, but at the close of each business day it does
something special: it captures a **snapshot** of every account's balance and stores
it. A snapshot is a frozen record of where things stood at end of day — and it earns
its keep in several ways at once:

- **Interest accrual.** Daily interest is calculated on the end-of-day balance. For
  a savings account earning 4% a year, the day's interest on a $10,000 balance is
  `$10,000 × 0.04 / 365 = $1.10`. Without a captured end-of-day figure, there's
  nothing to accrue against.

- **Statement generation.** Monthly statements show the balance at the end of each
  day, alongside activity and opening/closing balances. The snapshots are the raw
  material.

- **Regulatory reporting.** Banks must report their positions to regulators, and
  end-of-day balances are the standard reporting granularity.

- **Performance.** Rather than replaying every transaction since an account was
  opened to answer "what's the balance?", the system can start from the most recent
  snapshot and replay only the transactions since. The snapshot is a checkpoint.

Which balance does a snapshot record? The *value-date* balance from Chapter 7 — the
economically real one — because that's the balance interest and regulation care
about. This is the booking-vs-value distinction (Chapter 6) determining, once again,
which number is the one that counts. In a layered system, snapshots are naturally a
job for the deposit layer that tracks the three-part balance, not the pure ledger
that merely computes book balances on demand.

## The audit trail

Underneath every snapshot and every report lies the bank's most fundamental record:
the **audit trail**. It is an immutable, append-only log of every change the system
has ever made. Nothing is ever deleted from it. Every account opening, every posted
transaction, every hold placed or released, every reversal, every snapshot — each
is recorded with:

- a unique event identifier,
- a timestamp,
- the type of event,
- the entity it affected, and
- the full details of what happened.

This is the same append-only philosophy we met with ledger immutability in
Chapter 5, generalized from transactions to *every* mutation in the system. And it
buys the bank four things it cannot do without:

- **Regulatory compliance** — banks are legally required to keep complete records.
- **Forensic investigation** — when something looks wrong, the trail shows exactly
  what happened and in what order.
- **Debugging and incident response** — the same visibility serves engineers.
- **Independent verification** — because every mutation is logged, balances can be
  *recomputed from scratch* by replaying the events, and checked against what the
  system currently reports. If they ever disagree, you have caught a bug or a
  corruption. It is the trial balance of Chapter 2, taken to its logical end: not
  just "do the debits equal the credits?" but "does the entire current state follow
  from the recorded history?"

An immutable log that lets you rebuild the present from the past is the strongest
form of the trustworthiness this whole book has been building toward.

## Statements: the customer's view

Finally we reach the one report a customer actually reads: the **bank statement**, a
periodic (usually monthly) summary of activity and balances on their account. The
statement is where the abstractions of this book surface in plain sight — and where
the two clocks of Chapter 6 produce their most visible, most-complained-about
effect.

A statement shows three things:

- **A transaction listing** — every transaction with a *booking date* in the period,
  in chronological order. This is the activity the customer recognizes: "I paid this
  on the 3rd, I got paid on the 15th." Booking date is used here because it's the
  date the customer associates with *doing* the transaction.

- **Opening and closing balances** — computed using the *value date*. The opening
  balance is the end-of-day value-date balance from the last day of the prior period;
  the closing balance reflects every transaction whose value date falls within this
  period.

- **Daily balances** — the value-date balance at the end of each day, used for
  interest and regulatory purposes. (These are exactly the snapshots from the start
  of this chapter.)

## Why the statement may not seem to add up

Here is the subtle finish. Because the transaction *listing* uses booking date while
the *balances* use value date, the listed transactions may not perfectly reconcile
with the balance change shown — and this is correct behavior, not a bug:

- A transaction **booked February 25** with a **value date of March 1** appears in
  February's listing (you did it in February) but does *not* affect February's
  closing balance (it isn't economically real until March).

- A transaction **booked January 31** with a **value date of February 1** does *not*
  appear in February's listing (you did it in January) but *does* affect February's
  opening balance (it became real on February 1).

So a careful customer adding up February's listed transactions may not arrive at
February's balance change — because some listed activity hasn't taken value yet, and
some value that landed in February was for activity listed in January. This is why
most retail statements print *both* dates whenever they differ: it's the bank showing
its work, so the apparent mismatch resolves into sense. The closing balance and the
daily balances come from the value-date snapshots precisely because those are the
figures that drive interest and that regulators expect — the same reason snapshots
record the value-date balance in the first place.

## Where we've arrived

We began with a single rule — every transaction has equal debits and credits, so
money is only ever moved, never created — and a single reframing — that your deposit
is the bank's promise to you. From those two seeds grew everything since: the five
account types and their normal balances; the integer minor units that keep every
cent exact; the append-only transactions that are corrected only by reversal; the two
clocks of booking and value date; the three balances and the holds that bridge them;
the lifecycle that governs an account from opening to closure; and finally the
interbank machinery that moves value between banks through reserves at a central
bank, clearing the net and settling with finality.

What ties it all together is a single ambition that has run through every chapter: to
build a record of money so disciplined that it is *provably* correct, *completely*
auditable, and *trustworthy* down to the last cent. That discipline — not any
particular piece of software — is what a core banking system really is.

---

*Previous: [Chapter 11 — Payment Schemes](11-payment-schemes.md)*
*Back to the [Table of Contents](README.md)*
