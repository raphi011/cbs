# Chapter 2 — Double-Entry Bookkeeping

In the last chapter we met the accounting equation — assets equal liabilities
plus equity — and claimed that a bank never lets it break. This chapter is about
the technique that makes that promise keepable. It is the single most important
idea in all of accounting, and once it clicks, everything else in this book
follows from it.

## One rule

The technique is called *double-entry bookkeeping*. It was codified by the Italian
mathematician Luca Pacioli in 1494, drawing on methods Venetian merchants had
already used for two centuries, and it has been the foundation of serious
accounting ever since. The rule is almost absurdly simple:

> Every transaction must have equal debits and credits.

That's it. Every time money moves, it is recorded in (at least) two places: one
side records where the value came from, the other where it went. The two sides
are always equal. The consequence is profound:

> Money never appears or disappears. It only moves from one account to another.

There is no operation in a correct banking system that simply "creates" a balance
out of nothing. Value is always conserved, because every increase somewhere is
matched by a corresponding entry somewhere else.

## Debits and credits

The two directions an entry can take are called **debit** and **credit**. These
are the most misunderstood words in finance, mostly because everyday language has
them backwards. When your bank tells you it has "credited your account," your
balance went up — but that's because, as we saw, your account is the bank's
*liability*, and (as we'll see) credits increase liabilities.

For now, resist the urge to attach "debit = decrease" or "credit = increase" to
the words. Debit and credit are just the two sides of an entry — left and right,
by long convention. Whether a debit *increases* or *decreases* a particular
account depends on what kind of account it is, which is the subject of the next
chapter. The only rule you need right now is the balance rule: within any
transaction, the debits and the credits are equal.

## Two examples

Consider a customer depositing $100 in cash. Two things change: the bank has more
cash, and the bank owes the customer more. So we record two entries:

- **Debit** the bank's Cash account — an asset increases; the bank has more cash.
- **Credit** the customer's Deposit account — a liability increases; the bank
  owes the customer more.

Debit $100, credit $100. Balanced. And notice it matches the intuition from
Chapter 1: the bank got richer in cash *and* deeper in debt, both by $100.

Now consider one customer transferring $50 to another customer at the same bank:

- **Debit** the sender's Deposit account — the bank owes the sender less; their
  liability balance falls.
- **Credit** the receiver's Deposit account — the bank owes the receiver more;
  their liability balance rises.

Debit $50, credit $50. Balanced again. The bank's *total* liabilities didn't
change — it still owes the same amount in total — the obligation just moved from
one customer to another. No cash left the building.

## Error detection for free

The reason double-entry has survived five centuries unchanged is that the balance
rule is also an error-detection mechanism, built into the structure of the records
themselves.

Because every individual transaction balances, the sum of *all* debits ever
recorded must equal the sum of *all* credits ever recorded. If you list every
account and its balance — a report called a **trial balance** — the debit
balances must equal the credit balances. If they don't, you *know*, with
certainty, that something is wrong: an entry was dropped, a number was fat-
fingered, a transaction was only half-recorded. You may not yet know *where* the
error is, but you know one exists. A system that stored only a single balance per
customer could never give you that guarantee.

This is why a banking system enforces the balance rule at the moment a
transaction is recorded, and refuses to record one that doesn't balance. A
transaction whose debits and credits are unequal isn't a transaction with a small
problem — it's a corruption of the entire record, and it is rejected outright. In
the accounting core of our reference library, this is the first and most jealously
guarded invariant: a set of entries is summed by direction, and if total debits
do not equal total credits the posting is refused before it can touch the books.

## Why "double"

It's worth pausing on the word *double*. The "double" doesn't mean exactly two
entries — many real transactions have three or more, as we'll see in Chapter 5.
It means every transaction is recorded from *two perspectives*: where value came
from and where it went. A loan disbursement, a fee split, an interest payment —
each may touch several accounts, but every one of them records both sides of the
movement, and every one of them balances.

With the balance rule in hand, we have the *form* of every transaction. What we
don't yet have is a way to organize the accounts those transactions touch, or to
know whether a debit to a given account means "more" or "less." That requires a
classification of accounts — the chart of accounts — which is where we go next.

---

*Previous: [Chapter 1 — What a Bank Is](01-what-a-bank-is.md)*
*Next: [Chapter 3 — The Chart of Accounts](03-the-chart-of-accounts.md)*
