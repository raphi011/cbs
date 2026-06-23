# Chapter 3 — The Chart of Accounts

We now know that every transaction balances, and that an entry is either a debit
or a credit. What we're still missing is the meaning: when we debit an account,
does its balance go up or down? The answer depends entirely on what *kind* of
account it is. Classifying accounts into a handful of types — the *chart of
accounts* — is what gives debits and credits their meaning.

## Five types, from one equation

Every account a bank keeps belongs to one of five fundamental types. They are not
arbitrary; they fall directly out of an expanded version of the accounting
equation from Chapter 1:

```
Assets = Liabilities + Equity + (Revenue − Expenses)
```

That extra term — revenue minus expenses — is the bank's profit, which belongs to
the owners and so adds to equity. Pull the five nouns out of that equation and you
have the five account types:

| Type | What it represents | Examples |
|------|--------------------|----------|
| **Asset** | Things the bank owns or is owed | Cash, loans to customers, securities, real estate |
| **Liability** | Things the bank owes to others | Customer deposits, borrowings, bonds payable |
| **Equity** | The owners' residual interest | Paid-in capital, retained earnings |
| **Revenue** | Income the bank earns | Interest income, fee income, trading gains |
| **Expense** | Costs the bank incurs | Interest expense, salaries, rent, loan-loss provisions |

These five split into two families that answer two different questions.

The three **permanent** accounts — assets, liabilities, equity — describe the
bank's *position*: a snapshot of what it owns and owes at a single instant. The
report that lays them out is the **balance sheet**, and it is governed by
`Assets = Liabilities + Equity`. These accounts persist year after year; your
deposit balance doesn't reset on January 1st.

The two **temporary** accounts — revenue and expenses — describe the bank's
*activity* over a period: how much it earned and spent across a month or a year.
The report is the **income statement**. At year-end these accounts are "closed":
their balances are totalled into the year's profit, that profit is rolled into
equity (specifically retained earnings), and they reset to zero to start counting
the next period afresh. That closing step is the hinge connecting the two reports
— activity over a period flows into position at a point in time.

```
            BALANCE SHEET (permanent, a snapshot)
            ┌───────────────────────────────────────────┐
            │  ASSETS  =  LIABILITIES  +  EQUITY         │
            │  (owned)    (owed)          (owners' stake) │
            └───────────────────────────────▲───────────┘
                                            │ net income
                                            │ rolled in at year-end
            INCOME STATEMENT (temporary)    │
            ┌───────────────────────────────┴───────────┐
            │  REVENUE  −  EXPENSES  =  profit            │
            │  (earned)    (spent)                        │
            └───────────────────────────────────────────┘
```

## Normal balances

Now we can give debits and credits their meaning. Each account type has a
**normal balance** — the direction that *increases* it. The opposite direction
decreases it.

| Type | Normal balance | A debit… | A credit… |
|------|----------------|----------|-----------|
| **Asset** | Debit | increases | decreases |
| **Expense** | Debit | increases | decreases |
| **Liability** | Credit | decreases | increases |
| **Equity** | Credit | decreases | increases |
| **Revenue** | Credit | decreases | increases |

There is a neat symmetry worth memorizing: **assets and expenses are debit-
normal; liabilities, equity, and revenue are credit-normal.** The two "left
side of the equation" things (what you own, what you spend) increase with debits;
the three "right side" things (what you owe, the owners' stake, what you earn)
increase with credits. This is the whole reason the balance rule keeps the
equation intact: a balanced transaction nudges both sides equally.

This table is the Rosetta Stone for everything that follows. When you read "debit
Alice's deposit account," you can now decode it: a deposit account is a liability,
liabilities are credit-normal, so a debit *decreases* it — Alice has less money.
When you read "credit interest income," that's revenue, credit-normal, so the
bank earned more.

The rest of this chapter walks each type in turn, because the intuitions behind
them are where most confusion lives.

## Asset — things the bank owns or is owed

An asset is anything of value the bank controls. The most intuitive example is
cash in the vault — clearly something the bank owns. But assets also include money
that *other people owe the bank*. This is the part people miss. When a bank gives
a customer a $200,000 mortgage, it doesn't lose $200,000 — it *converts* one asset
(cash) into another (a loan receivable). The customer now owes the bank $200,000
plus interest, and that promise to repay is itself a valuable asset.

Typical asset accounts:

- **Cash and reserves** — physical currency and balances held at the central bank.
- **Loans to customers** — mortgages, personal loans, credit-card balances.
- **Securities** — government and corporate bonds and other investments.
- **Interbank lending** — short-term loans to other banks, such as overnight
  lending.

Assets are debit-normal: a debit increases an asset, a credit decreases it. When
the bank receives a $500 cash deposit, its cash increases by a $500 debit — and,
as we keep seeing, the other side of that entry is a liability.

## Liability — things the bank owes

A liability is an obligation to pay someone else. For a bank, the most important
liability by far is **customer deposits**. When you deposit $500 into checking,
the bank now owes you $500 on demand. Your balance, from the bank's point of view,
is a debt.

This is the single most counterintuitive fact in retail banking, so it bears
repeating from Chapter 1: the money in your checking account is the bank's
liability, not its asset. The bank holds the cash (an asset) but owes it back to
you (a liability).

Typical liability accounts:

- **Customer deposits** — checking, savings, certificates of deposit.
- **Borrowings** — money the bank has borrowed from other banks or the central
  bank.
- **Bonds payable** — debt the bank has issued to raise capital.

Liabilities are credit-normal: a credit increases them, a debit decreases them.
When a customer deposits $500, the bank *credits* the deposit liability (it owes
more) and *debits* cash (it holds more). Both sides increase — more cash, more
debt — and the books balance.

## Equity — the owners' residual interest

Equity is what's left after subtracting liabilities from assets: the
shareholders' stake. A bank with $100M in assets and $92M in liabilities has $8M
of equity. It is a *residual* — it's defined by the other two, not measured
directly.

Equity moves less often than the other accounts. It changes when the bank issues
or buys back shares, and at year-end when net profit (revenue minus expenses) is
rolled into retained earnings.

- **Paid-in capital** — what shareholders invested when buying the bank's stock.
- **Retained earnings** — accumulated profits kept rather than paid out as
  dividends.

Equity is credit-normal: profits increase it (credit), losses decrease it
(debit).

## Revenue — income the bank earns

Revenue accounts track money flowing in from operations. A bank's primary revenue
is interest on loans: it lends at a higher rate than it pays on deposits, and the
difference — the *net interest margin* — is how banks make most of their money.

- **Interest income** — earned on loans, mortgages, and securities.
- **Fee income** — account fees, ATM fees, wire fees, overdraft fees.
- **Trading gains** — profits from buying and selling securities.

Revenue is credit-normal. When a customer pays $50 of interest on a loan, the bank
*credits* interest income (it earned more) and *debits* either the loan balance
(the customer repaid part of an asset) or cash (if the money came from outside the
bank). At year-end, revenue is closed into retained earnings.

## Expense — costs the bank incurs

Expense accounts track the cost of running the bank. Just as revenue increases the
owners' stake, expenses decrease it.

- **Interest expense** — interest paid to depositors and bondholders; usually the
  bank's single largest cost.
- **Salaries and benefits** — employee compensation.
- **Loan-loss provisions** — money set aside against expected defaults.
- **Operating costs** — rent, technology, compliance, legal.

Expenses are debit-normal. When the bank pays a saver $10 of monthly interest, it
*debits* interest expense (a cost was incurred) and *credits* the customer's
deposit (it now owes $10 more). Like revenue, expense accounts close into retained
earnings at year-end.

## Putting it together

With the five types and their normal balances, you can now read any transaction in
this book and predict its effect. The classification isn't bureaucracy — it's the
layer of meaning that turns the mechanical balance rule of Chapter 2 into a true
description of the bank's economic reality. Next we'll see how all these accounts
are organized in practice, and how the amounts in them are represented so that not
a single cent is ever lost to rounding.

---

*Previous: [Chapter 2 — Double-Entry Bookkeeping](02-double-entry-bookkeeping.md)*
*Next: [Chapter 4 — Ledgers, Subledgers, and Money](04-ledgers-subledgers-and-money.md)*
