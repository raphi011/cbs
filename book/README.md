# How Money Moves

*An introduction to the accounting and mechanics of banking: double-entry
bookkeeping, deposits, clearing, and settlement.*

Most people use banks every day without ever seeing how they work on the inside.
We deposit a paycheck, tap a card, send a transfer — and money seems to simply
appear in one place and vanish from another. But underneath that smooth surface
is a centuries-old discipline of bookkeeping, a precise vocabulary, and a set of
mechanisms for moving value between institutions that almost never fail and
almost never lose a cent.

This book explains that machinery from the ground up. It assumes no accounting
background. It starts with the single rule that all of banking is built on —
that money never appears or disappears, it only moves — and follows that rule
all the way out to the interbank networks where banks settle their debts with
one another through accounts at a central bank.

The examples are concrete and the numbers add up, because the ideas here are also
implemented as a small, working banking library. Where it helps, the text shows
how a real system models a concept. But the subject of this book is the domain,
not any particular program: what banks track, why they track it that way, and how
money actually moves.

## What you'll learn

- Why the money in your checking account is the bank's *debt*, not its asset.
- How a 500-year-old technique — double-entry bookkeeping — makes errors
  detectable by construction.
- The difference between the day a transaction is *recorded* and the day it
  takes *economic effect*, and why banks need both.
- Why your "available balance" can differ from your "current balance," and what a
  hold really is.
- What actually happens, account by account, when you send money to someone at a
  different bank — and why "clearing" and "settlement" are not the same thing.
- How banks move millions of payments while only transferring the *net* between
  themselves.

## Table of Contents

**Part I — The Foundations of Bank Accounting**
1. [What a Bank Is](01-what-a-bank-is.md)
2. [Double-Entry Bookkeeping](02-double-entry-bookkeeping.md)
3. [The Chart of Accounts](03-the-chart-of-accounts.md)
4. [Ledgers, Subledgers, and Money](04-ledgers-subledgers-and-money.md)

**Part II — Transactions and Time**
5. [Transactions and Postings](05-transactions-and-postings.md)
6. [Booking Date vs. Value Date](06-booking-date-vs-value-date.md)
7. [Balances and Holds](07-balances-and-holds.md)

**Part III — Accounts Over a Lifetime**
8. [The Account Lifecycle and Overdraft](08-account-lifecycle-and-overdraft.md)

**Part IV — Moving Money Between Banks**
9. [Clearing and Settlement](09-clearing-and-settlement.md)
10. [The Interbank Network](10-the-interbank-network.md)
11. [Payment Schemes](11-payment-schemes.md)

**Part V — Records and Reporting**
12. [Snapshots, Audit Trails, and Statements](12-snapshots-audit-and-statements.md)

## Reading this on an e-reader

These chapters are written as Markdown so they are easy to read and edit. A
separate plan, [`PLAN-epub.md`](PLAN-epub.md), describes how to package them into
an EPUB3 file for e-readers.
