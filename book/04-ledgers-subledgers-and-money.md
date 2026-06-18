# Chapter 4 — Ledgers, Subledgers, and Money

A real bank has millions of accounts. The five types from the last chapter tell us
what each account *means*, but not how to organize them, and not how to write down
the amounts they hold. This short chapter closes both gaps — first the structure
that keeps millions of accounts navigable, then the deceptively important question
of how to represent a sum of money so that it is never wrong by a cent.

## A two-level hierarchy

Accounts are organized into a two-level tree: a **ledger** at the top, divided
into **subledgers**, each of which holds the individual accounts.

```
General Ledger
├── Customer Deposits (subledger)
│   ├── Alice Checking      (Liability)
│   ├── Bob Checking        (Liability)
│   └── … 50,000 more accounts
├── Loans (subledger)
│   ├── Loan #12345         (Asset)
│   └── …
├── Bank Assets (subledger)
│   └── Cash Vault          (Asset)
└── Revenue (subledger)
    └── Fee Income          (Revenue)
```

A **ledger** is the top-level book. A bank typically has one *General Ledger* (the
"GL") containing all of its accounts. Very large institutions may keep separate
ledgers for different legal entities or business units, but conceptually the
ledger is the complete book of record for one set of accounts.

A **subledger** is a subdivision that groups related accounts: one for customer
deposits, one for loans, one for interbank balances, one for fee income, and so
on. The point of subledgers is summarization. The General Ledger can report a
single line — "Total Customer Deposits: $10,000,000" — while the Customer Deposits
subledger underneath it holds the 50,000 individual accounts that add up to that
figure. Management and regulators look at the summary; a customer service rep
looks at one account in the subledger. Same data, two levels of zoom.

This hierarchy is purely organizational. It changes nothing about the accounting
rules: every account still has a type and a normal balance, every transaction
still balances. The tree just makes a vast number of accounts comprehensible.

## Representing money: integers, not decimals

Now the question that sinks more naive financial systems than any other: how do
you store an amount of money?

The tempting answer is a decimal or floating-point number — `100.50` dollars. It
is also the wrong answer. Floating-point numbers cannot represent most decimal
fractions exactly; `0.10 + 0.20` famously does not equal `0.30` in binary floating
point. In a system that must conserve value to the cent across billions of
operations, those tiny errors are unacceptable — they accumulate, they make trial
balances fail to balance, and they are maddening to track down.

The industry-standard solution is simple and exact: **store every amount as an
integer number of the smallest unit of the currency.** For US dollars that unit is
the cent; for euros, the euro cent; for currencies with no minor unit, the whole
unit. An amount is just a count of those minor units.

| Display | Stored as | Unit |
|---------|-----------|------|
| $100.50 | `10050` | cents |
| $1,234.56 | `123456` | cents |
| $10,000.00 | `1000000` | cents |

This is exactly the approach Stripe, most banks, and most payment processors use.
Integer arithmetic is exact: there is no rounding, no representable-fraction
problem, no drift. Adding two amounts can never introduce an error, and a column
of integers always sums to precisely the right total.

The cost is a small, deliberate division of responsibility: the *core* deals only
in integer minor units, and converting to and from a human-friendly display string
(`"$100.50"`) is the job of whatever sits at the edge — the UI, the report
generator, the statement renderer. The accounting engine never sees a dollar sign
and never sees a decimal point. In our reference library this is made explicit:
the amount type is simply a 64-bit integer of minor units, and every balance,
every entry, and every payment is counted in those units.

A 64-bit integer of cents can represent sums up to roughly 92 quadrillion dollars
before overflowing — comfortably more than any real balance — while keeping every
single cent exact. For the rare cases that need fractional cents (some interest
accruals, some FX), systems extend the same idea by choosing an even smaller unit
(say, millionths), never by reaching for floating point.

## Why these two choices travel together

The hierarchy and the integer representation are both in service of the same goal:
a record that is *exactly* correct and *easy to reason about* at any scale. The
subledger structure means you can always drill from a summary down to the
individual entries that justify it. The integer amounts mean that when you do, the
numbers will add up to the penny. Together they make the next idea possible — the
idea that the record is so trustworthy you never edit it, you only ever append to
it. That brings us to transactions themselves.

---

*Previous: [Chapter 3 — The Chart of Accounts](03-the-chart-of-accounts.md)*
*Next: [Chapter 5 — Transactions and Postings](05-transactions-and-postings.md)*
