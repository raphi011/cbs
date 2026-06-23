# Chapter 10 — The Interbank Network

The last chapter left us with a puzzle: a cross-bank payment has to move value
between two ledgers, but no bank may write in another's books. The solution is one
of the quiet marvels of the financial system — a shared meeting point where banks
hold reserves and settle with one another. This chapter builds that structure piece
by piece and shows how it makes the abstract distinction between clearing and
settlement into something concrete you can follow account by account.

## Each bank keeps its own book; they meet at the center

The structural choice that makes everything work is this: **every bank keeps its own
ledger**, complete and private, holding its own customers' accounts. And there is
**one additional ledger for a central bank**. Banks never write into one another's
books. They meet in exactly one place — the central bank — where each holds a
**reserve account**.

That single design decision is what turns "clearing" and "settlement" from vague
words into distinct, observable events:

- **Clearing** is the exchange and *netting* of payment instructions. The banks
  compare what they owe each other and agree on the net figures. No central-bank
  money moves during clearing — it is pure bookkeeping about who owes whom.

- **Settlement** is the movement of *reserves* between banks at the central bank.
  This is the moment of *finality* from Chapter 9 — when value actually,
  irrevocably changes hands.

Because the only place banks' money actually moves is the central bank, settlement
has a single, unambiguous location and moment. Everything before it is just
agreement; settlement is the act.

## The accounts each bank keeps

To participate, a bank's chart of accounts (Chapter 3) needs three accounts beyond
its ordinary customer deposits:

| Account | Type | Purpose |
|---------|------|---------|
| **Customer deposits** | Liability | What the bank owes each customer (Chapters 1–3). |
| **Clearing suspense** | Liability | A holding account for in-transit funds that have left a customer but not yet settled between banks. It returns to zero once a cycle settles. |
| **Reserve at central bank** | Asset | The bank's claim on the central bank. It moves only at settlement, and it *mirrors* the bank's reserve account held in the central bank's own ledger. |

The **clearing suspense** account is the bookkeeping home for money in flight.
When a customer's money leaves their account at initiation but hasn't yet reached
the other bank, it can't simply vanish — double-entry forbids that. It rests in
clearing suspense, which is precisely the ledger's representation of "promised but
not yet settled." Once the cycle settles, suspense empties back to zero.

The **reserve at central bank** is the bank's own record of the reserves it holds
centrally. Here we meet the *nostro/vostro* relationship from Chapter 9 made
concrete: this asset on the bank's books is the mirror image of a corresponding
liability on the central bank's books.

## The central bank's book

The central bank keeps a ledger too, and from its point of view the relationship is
inverted. For each participating bank it holds a **reserve liability** — because the
reserves a bank parks at the central bank are money the *central bank owes that
bank*. (Symmetry with Chapter 1: a deposit is always a liability of the institution
holding it, and a bank's reserves are, in effect, its deposit at the central bank.)
It also keeps a balancing account used when reserves are first funded.

So the same pile of reserves appears twice, in mirror image:

```
   Bank A's ledger                         Central Bank's ledger
   ┌─────────────────────────┐             ┌─────────────────────────┐
   │ Reserve at Central Bank │  ◀───────▶  │ Reserve: Bank A          │
   │ (Asset — A's claim)     │   mirror    │ (Liability — owed to A)  │
   └─────────────────────────┘             └─────────────────────────┘
```

When these two move in lockstep — A's asset rising exactly as the central bank's
"owed to A" liability rises — the books *reconcile*. Reconciliation, which sounded
like an operational chore in Chapter 9, is really just the requirement that these
mirror-image entries always agree. If they ever diverged, someone would have a
record of money that the other side doesn't, and that is exactly the error
double-entry exists to make impossible.

## Following a payment through the structure

We can now narrate a cross-bank payment in terms of these accounts, leaving the
exact debits and credits for the next chapter. Alice (Bank A) pays Bob (Bank B):

1. **Initiation, in Bank A's book only.** Alice's deposit is debited; the money
   lands in Bank A's clearing suspense. Alice's balance has fallen, but no
   central-bank money has moved and Bob has nothing yet. Bank A now owes "the
   network" the amount sitting in suspense.

2. **Clearing.** At a cut-off time, the network tots up all the instructions
   exchanged in this cycle and computes each bank's *net* position. Still no money
   moves. (Why net rather than gross is the heart of the next chapter.)

3. **Settlement, at the central bank.** Reserves move between the banks' reserve
   accounts at the central bank — A's down, B's up — and each bank mirrors that
   move in its own "reserve at central bank" asset. Bank B releases the funds from
   *its* clearing suspense into Bob's deposit account. Afterwards, both banks'
   suspense accounts are back to zero, and each bank's reserve asset again equals
   the central bank's matching liability. The books reconcile, and the payment is
   final.

Notice what this structure achieves. At no point did either bank touch the other's
ledger. Bank A only ever wrote in Bank A's book; Bank B only in Bank B's; the
central bank only in its own. They coordinated entirely through reserves at a shared
third party they all trust. That is the whole trick of interbank money movement —
and it scales from two banks to thousands without changing shape.

## Why a separate layer

It's worth stepping back to see why this is modeled as something *above* the
single-bank ledger rather than baked into it. A ledger answers "what does this bank
own and owe?" A payment system answers "how does money move *between* banks?" They
are different questions, and real institutions keep them separate: the payment rails
— SEPA, the card networks, the central bank's real-time settlement system — sit
*above* each bank's accounting system and send it instructions. The ledger has no
idea payments exist; it just faithfully records the postings it's told to make. Our
reference library mirrors this exactly: a payment network orchestrates postings into
each participant's independent ledger, while the ledgers themselves remain blissfully
unaware of the network above them.

With the structure in place, one question remains: why do banks settle the *net*
rather than every payment one by one, and what do the actual postings look like?
That is the subject of the final chapter of Part IV.

---

*Previous: [Chapter 9 — Clearing and Settlement](09-clearing-and-settlement.md)*
*Next: [Chapter 11 — Payment Schemes](11-payment-schemes.md)*
