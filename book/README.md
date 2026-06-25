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

## Download

Ready-to-read editions are committed alongside the source and can be downloaded
directly:

- 📖 [**EPUB**](how-money-moves.epub) — for Kobo, Apple Books, Calibre, and most e-readers
- 📄 [**PDF**](how-money-moves.pdf) — for printing or reading on any device

Both are rebuilt from the Markdown chapters with [`build.sh`](build.sh) (see
[Building the book](#building-the-book)).

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
12. [SEPA: Credit Transfers and Direct Debits](12-sepa.md)
13. [Card Transactions](13-card-transactions.md)

**Part V — Records and Reporting**
14. [Snapshots, Audit Trails, and Statements](14-snapshots-audit-and-statements.md)

## Building the book

The chapters are written as Markdown so they are easy to read and edit. The two
downloadable editions above are rebuilt from them with `build.sh`:

```bash
./build.sh          # both EPUB and PDF (default)
./build.sh epub     # EPUB only — requires pandoc
./build.sh pdf      # PDF only  — requires pandoc + a LaTeX engine (default: tectonic)
```

From the repository root the same builds are available as `make book`,
`make epub`, and `make pdf`.

The build takes the chapters in this directory, adds a cover/title and the
preface, and produces a validated EPUB3 and PDF with a working table of contents
and one navigable chapter per file.

### Design notes

- **Markdown is canonical.** The `.md` chapters are the source of truth; the EPUB
  and PDF are generated artifacts. The reading order lives in exactly one place —
  the `CHAPTERS` list in [`build.sh`](build.sh) — so reordering or inserting a
  chapter is a one-line change.
- **Diagrams are ASCII**, kept in fenced code blocks so they render as-is in a
  monospace block on any e-reader — no pre-processing needed. There is no Mermaid
  in the book; if a future chapter adds some, it must be pre-rendered to SVG/PNG
  at build time, because e-readers and the LaTeX PDF path can't run it.

### Verifying a build

After building, confirm the EPUB is well-formed:

```bash
unzip -l how-money-moves.epub   # mimetype must be the first entry...
unzip -v how-money-moves.epub   # ...and Stored (0% compression)
epubcheck how-money-moves.epub  # should report zero errors
```

Pandoc's EPUB output passes `epubcheck` out of the box. For a final render check,
open either edition in Calibre or Apple Books and confirm the table of contents
lists all fourteen chapters, chapter-to-chapter navigation works, tables render,
and the ASCII diagrams stay aligned.
