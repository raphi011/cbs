# A Core Banking System

A simplified but functionally complete Go library modeling the core accounting engine of a bank. Intended as a reference implementation for learning and prototyping — not for production use.

State lives behind a store interface with two implementations: `store/mem`, an in-memory reference implementation that needs no setup, and `store/pg`, which persists everything to Postgres. The Postgres backend is entirely optional — with no `DATABASE_URL` the server runs on `store/mem` — and it exists mostly as *curriculum*: it is where a double-entry ledger meets relational tables, and where a single process-wide mutex stops doing your concurrency control for you. See [Persistence](#persistence) for what that turns out to involve.

## Four-Layer Architecture

The system is split into four layers, each in its own package, all of them resting on the first:

1. **`ledger` — the general ledger.** The pure, double-entry accounting core: ledgers, subledgers, accounts, multi-legged transactions, postings, on-demand book balances, and an immutable audit log. Its top-level type is `ledger.Book`. It knows nothing about customers' account status, holds, available balance, or snapshots.

2. **`deposit` — the demand-deposit (DDA) layer.** Layered on top of a `ledger.Book`, this is the customer-facing checking/current-account layer. Its top-level type is `deposit.Register`. It adds account **status and lifecycle**, **overdraft limits** and the credit terms priced on them, authorization **holds** and the **available balance** they reduce, and end-of-day **snapshots**. Each deposit account wraps a backing Liability GL account; the deposit layer never stores money itself — every movement of value is a real posting in the underlying `ledger.Book`.

3. **`lending` — the credit layer.** Layered on the same `ledger.Book` and deliberately **not** on `deposit`: a term loan does not need a current account behind it. Its top-level type is `lending.Portfolio`, and it owns credit **facilities** (term loans and revolving lines), their **amortization schedules**, daily **accrual**, **capitalization** and **arrears**. The third credit product — the arranged overdraft — lives in `deposit` instead, for reasons the [Lending](#lending) section is mostly about.

4. **`payment` — the interbank payment network.** Each participant bank gets its own `ledger.Book` plus a `deposit.Register` and a `lending.Portfolio` over it; funds and status checks for a payment run through that deposit layer, while the multi-leg GL postings (debtor leg, creditor leg, reserve movements) live in the payment layer. Its top-level type is `payment.Network`.

There is a fifth package, `interest`, and it is deliberately **not** a layer: pure day-count, accrual and rounding arithmetic with no store, no `ledger.Book` and no notion of an account. Both credit-bearing layers use it, which is what keeps either from reimplementing a convention or a rounding rule.

The sections below are organized around these layers: general-ledger concepts first, then the deposit-layer concepts (holds, available balance, account lifecycle, snapshots, overdraft), then lending (facilities, amortization, accrual, arrears), then settlement and the payment network.

## Table of Contents

- [Core Banking Concepts](#core-banking-concepts)
- [Accounting Foundations](#accounting-foundations)
  - [Assets: What an Account Is Denominated In](#assets-what-an-account-is-denominated-in)
  - [Double-Entry Bookkeeping](#double-entry-bookkeeping)
    - [The Invariant Is Per Asset](#the-invariant-is-per-asset)
    - [Foreign Exchange, and Why the Ledger Needs No Rates](#foreign-exchange-and-why-the-ledger-needs-no-rates)
  - [Chart of Accounts](#chart-of-accounts)
    - [Asset](#asset--things-the-bank-owns-or-is-owed)
    - [Liability](#liability--things-the-bank-owes-to-others)
    - [Equity](#equity--the-owners-residual-interest)
    - [Revenue](#revenue--income-the-bank-earns)
    - [Expense](#expense--costs-the-bank-incurs)
    - [Accounts Are Per Asset](#accounts-are-per-asset)
  - [Ledger and Subledger Hierarchy](#ledger-and-subledger-hierarchy)
  - [Amounts and Precision](#amounts-and-precision)
    - [Why Scale Is Capped at Nine](#why-scale-is-capped-at-nine)
- [Transactions](#transactions)
  - [Entries, Legs, and Postings](#entries-legs-and-postings)
  - [Booking Date vs. Value Date](#booking-date-vs-value-date)
  - [Balance Types](#balance-types)
  - [Backdated Postings Correct Themselves](#backdated-postings-correct-themselves)
    - [Recording the Refund and Paying It Are Two Operations](#recording-the-refund-and-paying-it-are-two-operations)
  - [Multi-Legged Transactions](#multi-legged-transactions)
  - [Holds (Authorization / Pending Transactions)](#holds-authorization--pending-transactions)
  - [Idempotency](#idempotency)
  - [Transaction Reversal](#transaction-reversal)
- [Account Lifecycle](#account-lifecycle)
  - [Account States](#account-states)
    - [What This Implementation Actually Enforces](#what-this-implementation-actually-enforces)
  - [State Transitions](#state-transitions)
  - [Overdraft](#overdraft)
- [Lending](#lending)
  - [Three Products, and Why the Third Is Not a Facility](#three-products-and-why-the-third-is-not-a-facility)
  - [Two GL Accounts Per Facility](#two-gl-accounts-per-facility)
  - [Daily Accrual and the Precision Split](#daily-accrual-and-the-precision-split)
    - [The Recompute Window Opens at Inception](#the-recompute-window-opens-at-inception)
  - [Two Amortization Methods](#two-amortization-methods)
  - [Repayment: Against What Accrued, Not the Schedule](#repayment-against-what-accrued-not-the-schedule)
  - [Arrears and Non-Performing](#arrears-and-non-performing)
- [Settlement Cycles](#settlement-cycles)
  - [What Is Settlement](#what-is-settlement)
  - [Common Settlement Cycles](#common-settlement-cycles)
  - [Settlement and the Ledger](#settlement-and-the-ledger)
- [Payments, Clearing, and Settlement (the `payment` package)](#payments-clearing-and-settlement-the-payment-package)
  - [Why a Separate Package](#why-a-separate-package)
  - [The Multi-Bank Model](#the-multi-bank-model)
    - [Addressing](#addressing)
  - [Admission: A Bank Exists Before It Joins a Scheme](#admission-a-bank-exists-before-it-joins-a-scheme)
    - [What Admission Is Not](#what-admission-is-not)
  - [Payment Schemes](#payment-schemes)
    - [A Scheme Declares Its Asset](#a-scheme-declares-its-asset)
    - [Cross-Currency Payments Are Two Operations](#cross-currency-payments-are-two-operations)
  - [The Payment Lifecycle](#the-payment-lifecycle)
  - [Posting Choreography: SEPA Credit Transfer](#posting-choreography-sepa-credit-transfer)
  - [Settlement Is Final at the Central Bank, and the Banks Catch Up](#settlement-is-final-at-the-central-bank-and-the-banks-catch-up)
    - [Each Institution Knows Only What Its Own Job Needs](#each-institution-knows-only-what-its-own-job-needs)
    - [A Bank Reconciles Two Advices from Two Institutions Against One Balance](#a-bank-reconciles-two-advices-from-two-institutions-against-one-balance)
    - [Unclaimed Balances](#unclaimed-balances)
  - [Netting: A Worked Example](#netting-a-worked-example)
  - [SEPA Direct Debit and Returns](#sepa-direct-debit-and-returns)
    - [Posting Choreography: A Return](#posting-choreography-a-return)
    - [Returns Receivable](#returns-receivable)
  - [Deliberate Simplifications](#deliberate-simplifications)
  - [Next Work](#next-work)
- [Reporting and Compliance](#reporting-and-compliance)
  - [End-of-Day Snapshots](#end-of-day-snapshots)
  - [Audit Trail](#audit-trail)
- [Statements](#statements)
  - [Derived from the Ledger, Not a Separate Account Ledger](#derived-from-the-ledger-not-a-separate-account-ledger)
  - [What Appears on a Statement](#what-appears-on-a-statement)
  - [Why Transactions and Balances May Not Reconcile](#why-transactions-and-balances-may-not-reconcile)
- [Persistence](#persistence)
  - [Two Stores, One Conformance Suite](#two-stores-one-conformance-suite)
  - [The Ledger as Relational Tables](#the-ledger-as-relational-tables)
  - [The Asset Dimension in the Schema](#the-asset-dimension-in-the-schema)
    - [The Constraint That Is Missing on Purpose](#the-constraint-that-is-missing-on-purpose)
  - [A Balance Is an Aggregate, Not a Column](#a-balance-is-an-aggregate-not-a-column)
  - [The Unit of Work](#the-unit-of-work)
  - [Three Races a Single Mutex Was Hiding](#three-races-a-single-mutex-was-hiding)
  - [Migrations](#migrations)
  - [Running Against Postgres](#running-against-postgres)
- [Usage Example](#usage-example)

## Core Banking Concepts

A core banking system is the backbone of a financial institution. It is the "system of record" for all financial activity — every deposit, withdrawal, transfer, loan disbursement, and fee charge flows through it. The concepts below explain how this system models real-world banking.

## Accounting Foundations

### Assets: What an Account Is Denominated In

An **asset**, in the sense used throughout the rest of this document, is a unit of value that accounts are denominated in: euro, dollars, bitcoin. It is a `ledger.AssetDef`:

```go
type AssetDef struct {
    Code  AssetCode  // "EUR", "USD", "BTC" — the natural key
    Name  string     // "Euro"
    Scale uint8      // decimal places: 2 for EUR, 8 for BTC
    Class AssetClass // Fiat | Crypto
}
```

> **A note on the name.** `AssetDef`, not `Asset`, because `Asset` is already taken: it is the `AccountType` constant for the asset side of the balance sheet ([below](#asset--things-the-bank-owns-or-is-owed)). That constant keeps the name — it is the accounting term, and it appears in every chart of accounts ever written. The two senses are genuinely different things: an account's *type* says whether it is something the bank owns or owes, and its *asset* says what kind of money it is counted in. A euro deposit and a bitcoin deposit are both Liability accounts.
>
> `AssetClass` (`Fiat` or `Crypto`) carries no behaviour at all today. It exists so a chart of accounts or a UI can tell a currency from a token without pattern-matching on the code.

Three properties do most of the work.

**Assets are defined in code, not stored.** The known assets are a package-level list in `ledger` (`ledger.LookupAsset`, `ledger.Assets`), not rows in a table. An asset *definition* is a fact about the world rather than per-bank state: "BTC has 8 decimal places" is true in every book that ever mentions BTC, and two books disagreeing about it would be a bug, not a feature — storing it once per book is what makes that disagreement representable in the first place.

The system already draws this line elsewhere. A [payment scheme](#a-scheme-declares-its-asset) is a Go type with an `Asset()` method, registered in code; its *settlements* are rows. Assets work the same way: the definition is code, and everything denominated in one — accounts, deposit accounts, a bank's [per-asset plumbing accounts](#accounts-are-per-asset) — is a row that names it.

An earlier design here had a writable per-book registry, and it is worth saying why it was removed rather than quietly dropped. It promised something the rest of the system could not deliver. A bank's per-asset plumbing is provisioned once and never extended: suspense, reserve, unclaimed balances and returns receivable are created in the bank's own book when it is [founded](#admission-a-bank-exists-before-it-joins-a-scheme), and the settlement account is the central bank's, opened when the scheme answers the bank's application. Neither moment comes round again, so an asset registered afterwards produced a real customer account that could never settle, and the failure surfaced as a `404` on funding. Adding an asset is a code change, which is the honest shape of it: the reserve plumbing, the schemes that quote it and the chart of accounts all have to move together.

What *is* per bank is which assets it operates in — that is `bank_assets`, decided when the bank is founded.

**An account is denominated in exactly one asset, fixed at creation.** It is chosen when the account is created and never changes afterwards. This is how real core banking systems work — an account number and its currency are inseparable, which is why IBANs are per-currency and why "my euro account" and "my dollar account" are two accounts and not two views of one.

**That is what keeps a balance a scalar.** If a single account could hold several assets, its balance would be a *map*, and every balance query, every statement line, every end-of-day snapshot and every available-balance calculation would have to carry an asset alongside the number, forever. Fixing the asset to the account pushes that dimension into the chart of accounts, where it costs one more account instead of one more type parameter on everything.

The `deposit` layer follows the GL: a `deposit.Account` stores its asset too, copied from the backing GL account. It is the one fact this system stores twice on purpose — the GL account's asset is immutable, so the two cannot drift, and deriving it would turn every listing of deposit accounts into a join for a value that can never change. See [The Asset Dimension in the Schema](#the-asset-dimension-in-the-schema).

There is no default anywhere: every account names its asset, and one naming a code the system does not know fails with `ErrAssetNotFound`. Every entry in the list is held to `ledger.MaxAssetScale` — see [Why Scale Is Capped at Nine](#why-scale-is-capped-at-nine). That the check is a *domain* rule enforced by `ledger.Book`, rather than a constraint in the database, turns out to have consequences for the schema — see [The Asset Dimension in the Schema](#the-asset-dimension-in-the-schema).

### Double-Entry Bookkeeping

The most fundamental principle in banking is double-entry bookkeeping, codified in 15th-century Italy (by Luca Pacioli in 1494, though Italian merchants had practised it since the 13th–14th centuries) and still the foundation of all modern accounting. The rule is simple:

> Every transaction must have equal debits and credits.

This means money never appears or disappears — it always moves from one account to another. When a customer deposits €100 cash:

- **Debit:** Bank's Cash account (asset increases — the bank has more cash)
- **Credit:** Customer's Deposit account (liability increases — the bank owes the customer more)

When a customer transfers €50 to another customer:

- **Debit:** Sender's Deposit account (liability decreases)
- **Credit:** Receiver's Deposit account (liability increases)

The balanced nature of double-entry provides a built-in error-detection mechanism: if debits don't equal credits, something is wrong.

#### The Invariant Is Per Asset

The rule as Pacioli stated it assumes something he never had to say out loud: that everything in the book is counted in the same money. Once a book holds more than one [asset](#assets-what-an-account-is-denominated-in), the invariant needs one more clause:

> Every transaction must have equal debits and credits **within each asset**.

The global version is not a weaker check — it is a *broken* one, and seeing exactly why means being precise about what it compares. An `Amount` is an integer in its asset's [minor units](#amounts-and-precision) and carries nothing else; a global check sums those integers with no idea which asset each belongs to. So it is satisfied by any two legs whose **integers** match, whatever those integers are worth.

Ten billion is ten billion:

```
Debit  Cash EUR (Asset)         10_000_000_000    // €100,000,000.00
Credit Customer BTC (Liability) 10_000_000_000    // ₿100.00000000
                                ──────────────
Total debits − total credits:                0    ✓ by the old rule
```

It passes. The bank has booked one hundred million euro of cash against an obligation of one hundred bitcoin — worth roughly €6.5 million at the rate the [FX example below](#foreign-exchange-and-why-the-ledger-needs-no-rates) implies. Some **€93 million has appeared out of nothing**, and the engine that waved it through had no idea it was pricing anything, because it was not pricing anything: it was comparing two integers.

Notice what actually did the damage. It is *not* that the two legs were unequal in value — the check never looks at value, and could not, since it has no rate. It is that the integers were equal while the assets were not, and EUR and BTC differ in scale by a factor of a million (2 places against 8). Change either asset's scale and the very same posting becomes a different fiction. The rate this transaction implies is not merely wrong; it is an artefact of two unrelated unit conventions that no one chose and nothing records.

Per asset, there is no rate to get wrong. The check sums debits and credits *within* each asset and requires each sum to net to zero:

```go
// ledger.validateBalance, in essence
net := map[AssetCode]Amount{}
for _, e := range entries {
    asset := accounts[e.AccountID].Asset   // an entry's asset is its account's
    if e.Direction == Debit { net[asset] += e.Amount } else { net[asset] -= e.Amount }
}
// every asset must net to zero
```

A failure returns `ErrUnbalancedAsset` **wrapped together with** `ErrUnbalancedTransaction`, and names the offending asset. Both sentinels match under `errors.Is`, so a caller that only wants to know "did this balance?" checks the general one and a caller that wants to report which asset broke checks the specific one, without either having to know about the other.

This is the whole reason the ledger never needs to know what anything is worth. A single-asset check needs no prices because there is only one thing to price; a per-asset check needs no prices because the assets never meet inside one balancing sum. A *global* multi-asset check would need prices, and that is precisely the design that puts an exchange rate in the middle of an accounting engine.

#### Foreign Exchange, and Why the Ledger Needs No Rates

Foreign exchange is **not implemented** — there are no rates anywhere in this system and no trade operation. What follows is the shape the ledger was designed to accommodate, not something you can call today.

The per-asset invariant rules out the obvious posting. A customer selling 100 EUR for bitcoin cannot be recorded as one transaction with a euro leg and a bitcoin leg, because that is exactly the posting rejected above. Instead each asset balances through a **position account** of its own asset, and the trade is two ordinary, individually balanced postings:

```
EUR side — balances in EUR:
  Debit  Customer EUR deposit (Liability)   10000      // the customer's euros leave
  Credit EUR Position                       10000      // the bank takes them on

BTC side — balances in BTC:
  Debit  BTC Position                      153000      // the bank gives bitcoin up
  Credit Customer BTC deposit (Liability)  153000      // the customer receives it
```

Neither posting mentions the other, and neither mentions a rate. The rate is expressed *entirely* in the choice of the two amounts — `10000` cents against `153000` satoshi — and that choice is made above the ledger, by whatever quoted the price. The ledger records the consequence of a price; it never decides one. Which of the five [account types](#chart-of-accounts) a position account should be is a modelling decision that belongs to the FX work itself; what matters here is that there is one per asset and that it is the counterparty to every trade leg in that asset.

The balance of a position account is then a number worth having in its own right: it is the bank's **open position** in that asset — how much of it the bank is long or short as a result of the trades it has done. A bank that has bought and sold the same amount of bitcoin is *flat*, and its BTC position account reads zero. A bank that has been buying is long, and holds a real exposure to the price moving against it.

Note what is *not* in the ledger under this scheme: the profit or loss on trading. It appears only when the positions are revalued at a current rate, which needs a price and therefore lives above the ledger too — in the same layer that quoted the trade in the first place.

### Chart of Accounts

The chart of accounts organizes all accounts into five fundamental types, derived from the accounting equation:

```
Assets = Liabilities + Equity + (Revenue - Expenses)
```

The five types split into two groups: three permanent accounts on the **balance sheet** (a snapshot of the bank's financial position at a point in time) and two temporary accounts on the **income statement** (activity over a period). At year-end, net income flows into equity, connecting the two:

```mermaid
graph TD
    subgraph "BALANCE SHEET (permanent)"
        A["<b>ASSETS</b><br/>Cash, Loans, Securities<br/><i>Debit normal</i>"]
        L["<b>LIABILITIES</b><br/>Deposits, Borrowings<br/><i>Credit normal</i>"]
        E["<b>EQUITY</b><br/>Capital, Retained Earnings<br/><i>Credit normal</i>"]
    end

    subgraph "INCOME STATEMENT (temporary)"
        R["<b>REVENUE</b><br/>Interest on loans, Fees<br/><i>Credit normal</i>"]
        X["<b>EXPENSES</b><br/>Interest on deposits, Salaries<br/><i>Debit normal</i>"]
    end

    A -- "= Liabilities + Equity<br/>+ (Revenue − Expenses)" --> L
    L -- + --> E
    R -- "minus" --> X
    X -- "Net Income<br/>flows into Equity<br/>at year-end" --> E
```

Each type has a "normal balance" — the direction that increases it:

| Type | Normal Balance | Description | Examples |
|------|---------------|-------------|----------|
| **Asset** | Debit | Things the bank owns or is owed | Cash, loans to customers, securities, real estate |
| **Liability** | Credit | Things the bank owes to others | Customer deposits, borrowings, bonds payable |
| **Equity** | Credit | Owner's residual interest | Paid-in capital, retained earnings |
| **Revenue** | Credit | Income earned | Interest income, fee income, trading gains |
| **Expense** | Debit | Costs incurred | Interest expense, salaries, rent, provisions |

#### Asset — Things the Bank Owns or Is Owed

An asset is anything of value that the bank controls. The most intuitive example is cash sitting in the vault — that is clearly something the bank owns. But assets also include money that other people owe *to* the bank. When a bank gives a customer a $200,000 mortgage, the bank doesn't lose $200,000 — it *converts* one asset (cash) into another asset (a loan receivable). The customer now owes the bank $200,000 plus interest, and that obligation is valuable.

Common asset accounts in a bank:
- **Cash / reserves** — physical currency and balances held at the central bank
- **Loans to customers** — mortgages, personal loans, credit card balances
- **Securities** — government bonds, corporate bonds, and other investments the bank holds
- **Interbank lending** — short-term loans to other banks (e.g., overnight lending)

Assets have a **debit normal balance**, meaning a debit increases an asset and a credit decreases it. When the bank receives a $500 cash deposit from a customer, its cash (asset) increases by a $500 debit — but as we'll see, the other side of that entry is a liability.

#### Liability — Things the Bank Owes to Others

A liability is an obligation the bank has to pay someone else. The most important liability for a bank is **customer deposits**. When a customer deposits $500 into their checking account, the bank now *owes* that customer $500 on demand. The customer's account balance is, from the bank's perspective, a debt.

This is often the most counterintuitive part: the money in your checking account is the bank's liability, not its asset. The bank has the cash (asset), but it owes that cash back to you (liability).

Common liability accounts in a bank:
- **Customer deposits** — checking accounts, savings accounts, certificates of deposit
- **Borrowings** — money the bank has borrowed from other banks or the central bank
- **Bonds payable** — debt securities the bank has issued to raise capital

Liabilities have a **credit normal balance**. A credit increases a liability and a debit decreases it. When a customer deposits $500, the bank credits the customer's deposit account (liability goes up) and debits its cash account (asset goes up). Both sides increase — the bank has more cash *and* owes more to the customer.

#### Equity — The Owner's Residual Interest

Equity is what's left over after you subtract liabilities from assets. It represents the shareholders' stake in the bank. If a bank has $100M in assets and $92M in liabilities, the equity is $8M.

Equity accounts don't change as frequently as the others. They mainly move when the bank issues new shares, buys back shares, or at year-end when net profit (revenue minus expenses) is rolled into retained earnings.

Common equity accounts:
- **Paid-in capital** — money shareholders invested when buying the bank's stock
- **Retained earnings** — accumulated profits the bank has kept rather than distributing as dividends

Equity has a **credit normal balance**. Profits increase equity (credit), losses decrease it (debit).

#### Revenue — Income the Bank Earns

Revenue accounts track money flowing into the business from its operations. For a bank, the primary source of revenue is interest charged on loans — the bank lends money at a higher rate than it pays on deposits, and the difference (the *net interest margin*) is how banks make most of their money.

Common revenue accounts:
- **Interest income** — interest earned on loans, mortgages, and securities
- **Fee income** — account maintenance fees, ATM fees, wire transfer fees, overdraft fees
- **Trading gains** — profits from buying and selling securities

Revenue has a **credit normal balance**. When a customer pays $50 in monthly interest on a loan, the bank credits interest income (revenue goes up) and debits the customer's loan account (the loan balance — an asset — decreases because the customer has repaid part of it) or debits cash (if the payment comes from outside the bank).

At year-end, revenue accounts are "closed" — their balances are zeroed out and the net profit is transferred into retained earnings (equity).

#### Expense — Costs the Bank Incurs

Expense accounts track the costs of running the bank. Just as revenue increases the owners' stake, expenses decrease it.

Common expense accounts:
- **Interest expense** — interest paid to depositors and bondholders (this is the bank's biggest cost)
- **Salaries and benefits** — compensation for employees
- **Provisions for loan losses** — money set aside in case borrowers default
- **Operating costs** — rent, technology, compliance, legal

Expenses have a **debit normal balance**. When the bank pays $10 in monthly interest to a savings account customer, it debits interest expense (expense goes up) and credits the customer's deposit account (liability goes up — the bank now owes the customer $10 more).

Like revenue, expense accounts are closed at year-end into retained earnings.

#### Accounts Are Per Asset

The five types say what an account *is*. The [asset](#assets-what-an-account-is-denominated-in) says what it is *counted in*, and the two dimensions are independent: a euro deposit and a bitcoin deposit are both Liability accounts, and a euro cash account and a bitcoin wallet are both Asset accounts.

Because an account is bound to exactly one asset, a bank that operates in three currencies does not have one Cash account holding three kinds of money. It has three Cash accounts:

```
Bank Assets (subledger)
├── 100.100.001  Cash EUR   (Asset,     EUR)
├── 100.100.002  Cash USD   (Asset,     USD)
└── 100.100.003  Cash BTC   (Asset,     BTC)

Customer Deposits (subledger)
├── 200.100.001  Alice EUR  (Liability, EUR)
└── 200.100.002  Alice BTC  (Liability, BTC)
```

So the chart of accounts of a multi-asset bank is, roughly, its single-asset chart multiplied by the assets it operates in. That sounds like duplication and is in fact the point: it is what makes "the bank's euro cash" a thing with a balance you can read, rather than one component of a vector that some caller has to know how to project. Alice, likewise, has two accounts rather than one account with two balances — which is why in practice a customer with a foreign-currency account has a second IBAN.

### Ledger and Subledger Hierarchy

Accounts are organized into a two-level hierarchy:

```
General Ledger
├── Customer Deposits (subledger)
│   ├── Alice Checking (Liability)
│   ├── Bob Checking (Liability)
│   └── ... 50,000 more accounts
├── Loans (subledger)
│   ├── Loan #12345 (Asset)
│   └── ...
├── Bank Assets (subledger)
│   └── Cash Vault (Asset)
└── Revenue (subledger)
    └── Fee Income (Revenue)
```

- **Ledger:** The top-level book. A bank typically has a General Ledger (GL) that contains all accounts. Large banks may also have separate ledgers for different business units or legal entities.

- **Subledger:** A subdivision of a ledger that groups related accounts. For example, under the General Ledger you might have subledgers for "Customer Deposits", "Loans", "Interbank", "Fee Income", etc. Subledgers allow the GL to show summary totals while the subledger contains the individual account detail.

In practice, the General Ledger might show one line item for "Total Customer Deposits" ($10M), while the Customer Deposits subledger contains 50,000 individual customer accounts that sum to that total.

Not every subledger is seeded. `lending.Portfolio` creates a **`Payables`** subledger, and an **`Interest Refunds Payable: <facility> (<asset>)`** (Liability) account inside it, the first time a backdated correction needs to refund interest but a facility's drawn principal cannot absorb the whole remainder — see [Backdated Postings Correct Themselves](#backdated-postings-correct-themselves). Nothing in the seed data ever walks that path, so a freshly seeded ledger has no `Payables` subledger until one is created lazily.

That folder is also the one place this system keeps a **subsidiary ledger**: one account per facility that has ever been over-refunded, with the folder's total as the control figure. Every other lazily-created account here is one per asset — `Interest Income (EUR)` serves every facility in the book — and the difference is what each balance has to answer. Interest income is a bank-wide total that nothing needs split by borrower. A refund payable is a debt to *one* borrower that somebody eventually has to pay back, so pooled it would hold a single number that cannot say who is owed what, and a payout against it would be unbounded: nothing would stop it paying one borrower out of another's money, because the ledger's balance check guards Asset and Expense accounts only and a Liability is never caught by it.

### Amounts and Precision

All monetary amounts are integers in the smallest unit of their [asset](#assets-what-an-account-is-denominated-in) — cents for EUR and USD, satoshi for BTC. This is the approach Stripe, most banks and most payment processors use, generalized here to any asset rather than to a single currency.

It avoids floating-point precision entirely. `0.1 + 0.2` is `0.30000000000000004` in IEEE 754, and a system whose whole job is adding money cannot afford an error that accumulates. With integers there is no rounding at all.

How many decimal places an asset's minor unit has is its **scale**, and scale is a property of the *asset*, not of the system:

| Display | Internal | Asset | Scale |
|---------|----------|-------|-------|
| €100.50 | 10050 | EUR | 2 |
| €1,234.56 | 123456 | EUR | 2 |
| ₿1.00000000 | 100000000 | BTC | 8 |
| ₿0.00000001 | 1 | BTC | 8 |

The same integer therefore means quite different things in different assets: `100000000` is one bitcoin, and it is also a million euro. Nothing can be rendered, and nothing can be compared, without knowing which asset it belongs to — which is one more reason the asset lives on the account rather than being passed around beside the number.

The caller is responsible for converting to and from display format.

#### Why Scale Is Capped at Nine

`ledger.Amount` is an `int64`, and `ledger.MaxAssetScale` is **9**. No known asset may exceed it.

The cap is arithmetic, not preference. A signed 64-bit integer tops out at 9,223,372,036,854,775,807 — about 9.22 × 10¹⁸. Divide that by the scale to get the largest amount the type can hold:

| Scale | Asset | Largest representable amount |
|-------|-------|------------------------------|
| 2 | EUR | ~92 quadrillion € |
| 8 | BTC | ~92 billion ₿ (the entire 21M supply is 2.1 × 10¹⁵ satoshi) |
| 9 | the cap | ~9.2 billion whole units |
| 18 | ETH (wei) | **9.2 units** |

At 18 decimal places — the native precision of ether and of most ERC-20 tokens — an `int64` holds 9.2 ETH. Not 9.2 billion, not 9.2 million: nine. Native 18-decimal precision and an `int64` amount are mutually exclusive, and the only real decision is which of the two to give up.

This system keeps the `int64` and caps the scale, so an 18-decimal asset can be carried only at reduced precision — listed at 9 places, say, discarding the smallest nine. That is a real limitation and it is stated here rather than discovered later. The important part is that the bound is **checked, not assumed**: because the assets are a list in code rather than runtime input, the check is a test over that list (`TestKnownAssetsRespectMaxScale`) that fails the build the moment someone adds an entry that would silently overflow.

`Amount` is a defined type (`type Amount int64`) rather than an alias for exactly this reason. If the trade-off is ever revisited and amounts widen to 128 bits, the compiler points at every site that has to change instead of quietly accepting a bare `int64`.

## Transactions

### Entries, Legs, and Postings

A few words are used interchangeably throughout this document and the code, so it is worth pinning them down up front:

- **Entry:** A single debit or credit to one account. This is the `ledger.Entry` type — it carries an account, an amount, and a `Direction` (`Debit` or `Credit`). It is the smallest unit of bookkeeping.

- **Leg:** A synonym for *entry*. The word is used when emphasizing that a transaction has several sides — a "two-legged" transfer has one debit entry and one credit entry, while a "multi-legged" transaction has three or more. "Leg" and "entry" refer to exactly the same thing; there is no separate `Leg` type.

- **Posting:** The act of recording a transaction in the ledger (the verb, as in "to post a transaction" via `PostTransaction`). Loosely, "a posting" is also used to mean an entry that has been recorded. Posted transactions are immutable — they are never edited or deleted, only reversed (see [Transaction Reversal](#transaction-reversal)).

- **Transaction:** A balanced set of entries (legs) that are posted together as one atomic unit. Within any transaction, **total debits = total credits** (see [Multi-Legged Transactions](#multi-legged-transactions)).

In the `payment` layer two specific legs get their own names: the **debtor leg** is the entry that moves money out of the payer's account (posted by the payer's *own* bank — at submission for a push, on receipt of the collection for a [pull](#sepa-direct-debit-and-returns)), and the **creditor leg** is the entry that delivers money into the payee's account (posted by the payee's *own* bank, once the clearing house has told it the cycle settled). Both are ordinary ledger entries — the names just identify which side of a cross-bank payment they represent. See [The Payment Lifecycle](#the-payment-lifecycle).

> **Trial balance:** Referenced in a few places below, this is the list of every account's balance at a point in time. Because every transaction balances, the sum of all debit balances must equal the sum of all credit balances — a trial balance that does not sum to zero signals a bookkeeping error.

### Booking Date vs. Value Date

Every transaction carries two dates:

- **Booking Date:** The date/time when the transaction was recorded in the system. This is the "system date" or "processing date". It determines when the transaction appears in audit trails and system reports.

- **Value Date:** The date when the transaction takes economic effect. This determines when interest starts accruing, when funds become available, and which business day "owns" the transaction. The value date may be in the past (back-dated) or future (forward-dated) relative to the booking date.

#### When Value Date Differs from Booking Date

In many real-world scenarios the two dates can diverge by days or even weeks:

- **Weekend/holiday processing:** A wire transfer received Friday evening is booked immediately (Booking Date: Friday 7:00 PM) but funds are only available on the next business day (Value Date: Monday). Over a long holiday weekend this gap can stretch to 4–5 days.

- **Check deposits:** A customer deposits a check on Monday and the bank records it right away (Booking Date: Monday). However, the check must clear through the interbank settlement network, so the value date might be Wednesday or Thursday depending on the clearing cycle. Until then the funds don't accrue interest and may not be available for withdrawal.

- **Back-dated corrections:** An operations team discovers on March 5 that a corporate payment should have settled on February 28. The correction is booked today (Booking Date: March 5) but given a value date of February 28 so that interest calculations for the intervening days are correct. Without this, the customer would lose several days of interest.

- **Forward-dated standing orders:** A customer schedules a rent payment for the 1st of next month. The bank may book the instruction today (Booking Date: January 20) but assign a value date of February 1, when the money actually moves and interest implications begin.

- **Securities settlement:** A stock trade executed on Monday (trade date T) typically settles on the next business day (T+1). The cash leg is booked on Monday but value-dated to Tuesday when ownership and funds actually transfer.

Interest is calculated based on value dates, not booking dates. This distinction is critical for accurate financial calculations — using the wrong date can mean customers earn too much or too little interest, and regulatory balance reports would be incorrect.

#### Who Decides the Value Date

The value date is not set by a single actor — it depends on the transaction type:

- **Automated rules** handle the majority of cases. The bank's system assigns the value date based on predefined policies per product and payment channel (e.g., domestic wires get same-day value, checks get T+2, international transfers get T+1 to T+3 depending on the corridor).

- **Payment networks** can dictate it. SWIFT messages for international transfers include a value date field set by the sending bank that the receiving bank is expected to honor. Securities settlement follows market conventions like T+2 that both parties agree to.

- **The customer** influences it for scheduled payments and standing orders — they choose when the payment should take effect, which becomes the value date.

- **Operations staff** set it manually for corrections and adjustments, deciding the appropriate value date based on when the economic event actually occurred or should have occurred.

- **Regulation** constrains all of the above. Laws like the US Expedited Funds Availability Act (Reg CC) set maximum hold periods for check deposits, putting an upper bound on how far the value date can lag behind the booking date.

In practice, most core banking systems have a rules engine upstream of the ledger that determines the value date automatically before posting the transaction.

### Balance Types

A single account carries three distinct balances at any point in time:

- **Value-date balance** (also called the **interest-bearing balance**): The balance computed from entries whose value date has passed. This is what the bank uses to calculate interest. It is `ledger.Book.ValueDateBalance(ctx, accountID, asOf)` — the balance endpoint's one non-test caller — and it is also exactly what `Tx.ValueDatedSeries` opens on at the start of its window; both interest engines read the series rather than calling `ValueDateBalance` directly.

  A day boundary here is a UTC day, and a day's interest accrues on that day's **closing** balance: entries value-dated on the day itself count. A business date is a date, so an end-of-day run at 23:00 covers the same day as one at 09:00 — `ledger.DayStart` is where that rule lives, and it lives in Go rather than in either store, because two stores that each decide what a day is are one DST-adjacent edge case away from disagreeing.

- **Book balance** (also called the **ledger balance**): The balance computed from all posted transactions based on their booking date, regardless of value date. It reflects everything that has been recorded in the system.

- **Available balance**: The book balance minus any active holds. This is what ATMs and point-of-sale terminals check to decide whether a transaction should be approved.

For example, a single account might show all three balances simultaneously:

| Balance | Amount | What drives it |
|---------|--------|----------------|
| Value-date balance | $9,500 | Only entries whose value date has passed |
| Book balance | $10,000 | All posted transactions |
| Available balance | $9,200 | Book balance minus $800 in active holds |

The value-date balance can be lower than the book balance if a forward-dated transaction has been booked but its value date has not yet arrived. It can be higher if a back-dated correction added economic value to a past date.

### Backdated Postings Correct Themselves

A posting can arrive value-dated to a day that has already been accrued for — a salary credit booked Friday and value-dated Wednesday. The interest charged for Wednesday and Thursday was computed on a balance that is now known to be wrong.

Accrual handles this without reversing anything. Every run recomputes the interest for its whole window — every day since inception — from the account's value-dated movement series, and posts the change in the rounded value against what it posted last time. A backdated entry moves a historical day's balance, the recomputed gross moves with it, and the next run's delta is the true-up. Nothing is rewound and no earlier accrual is reversed: each of those was a correct statement of what the ledger knew when it was made, and the correction is a new, linked event.

A negative true-up credits the accrued-interest receivable. If interest has already been capitalised out of it, the receivable cannot absorb the whole correction — that money has left it — so the remainder is refunded to the customer's account, or credited to principal on a loan. When the drawn principal cannot absorb the whole remainder either — including when it is already zero — whatever is left over is credited to a lazily-created **`Interest Refunds Payable: <facility> (<asset>)`** account in a new **`Payables`** subledger: the borrower is still owed that money, and the bank cannot simply keep it. Neither the subledger nor the account exists until this path creates them — the seed data never touches it, so a fresh seeded ledger has no `Payables` subledger at all.

#### Recording the Refund and Paying It Are Two Operations

Only the first of them happens in the accrual. The correction runs inside an end-of-day batch, over every facility in the book, with no borrower account in hand and no business choosing one — so it records the obligation and stops. `Portfolio.RefundInterest` is the other half:

```
                        Dr  Interest Refunds Payable: Alice Home Loan (EUR)   4_932
                          Cr Alice's current account                            4_932
```

It is the mirror of a repayment, and every difference between the two follows from the money running the other way.

- **It touches neither principal, nor the receivable, nor the schedule.** A repayment allocates across two accounts and then across instalments, because it settles a debt the schedule is a plan for. This settles a debt with no schedule and no components: the correction already decided what the borrower owes, crediting principal as far as principal could absorb, and only the overflow reached the payable. Putting any of this refund back onto the loan would undo that split and hand the borrower money the correction had already spent reducing what they owed. Arrears are untouched for the same reason — they are a pure function of a schedule this does not change.
- **A closed facility can still be refunded.** `Repay` refuses `ErrFacilityClosed`; this deliberately does not. Closed means the *borrower* owes nothing and no more will be lent — a statement about their obligations, not the bank's. A bank that discovers it overcharged interest on a loan settled last year still owes that money, and refusing to pay it because the contract is over would strand the obligation in a Liability account with nothing left that could ever discharge it: the facility accrues no more, is never billed again, and takes no more repayments.
- **The amount is bounded rather than clamped.** Over-refunding would post cleanly — the ledger's balance check guards Asset and Expense accounts only — and leave the payable *negative*, an account asserting that the borrower owes the bank a refund. Partial refunds are fine; paying out more than was ever owed is `ErrInvalidAmount`. It is bounded rather than silently clamped because nothing here runs inside an end-of-day batch, which is precisely why the correction upstream clamps and this does not: there is no batch to take down by refusing.

`Portfolio.ListRefundsPayable` is the worklist — every borrower the bank still owes, across the whole book, closed facilities included. Leaving those out would hide exactly the obligations nothing else surfaces.

The window starts at **inception** — account opening for a deposit overdraft, origination for a lending facility — rather than at the last repricing. That is what effective-dated product terms buy: because the limit and the rates are rows on a timeline rather than columns on the record, each day is re-derived at the terms that were actually in force *on it*, so reaching back across a repricing prices the earlier days at the earlier rate instead of at today's. A back-value landing before a repricing is therefore trued up over *every* day it takes effect on, rather than only over the days from the repricing forward — which is where a mutable-terms window stopped. See [The Recompute Window Opens at Inception](#the-recompute-window-opens-at-inception) for what that costs and what would remove the cost.

### Multi-Legged Transactions

While simple transfers involve two entries (one debit, one credit), real-world transactions often require more legs:

- **Fee split:** A $100 payment might be split into $97 to the merchant and $3 to the fee income account.

- **Loan disbursement:** Disbursing a $10,000 loan might involve crediting the customer's deposit account, debiting the loan receivable account, and debiting an origination fee from the deposit account with a corresponding credit to fee revenue.

- **Interest accrual:** Posting monthly interest on a savings account involves debiting the bank's interest expense account and crediting the customer's deposit account, possibly with a separate leg for tax withholding going to a liability account.

In all cases, the invariant holds: **total debits = total credits**.

### Holds (Authorization / Pending Transactions)

> Holds, the available balance, account status, and snapshots all live in the **`deposit`** package (`deposit.Register`), not in the general ledger. The pure `ledger.Book` only knows about posted transactions and book balances. This section describes deposit-layer behavior.

Holds model the "auth-capture" flow common in card payments and other scenarios where funds must be reserved before a final amount is known:

1. **Authorization:** When a customer swipes their debit card at a gas pump, the bank places a hold (e.g., $100) on the account. The book balance is unchanged, but the available balance drops by $100.

2. **Capture:** When the customer finishes pumping ($45 of gas), the hold is captured for the actual amount. The hold is removed and a real transaction is posted for $45.

3. **Release:** If the transaction is cancelled (e.g., the customer drives away without pumping), the hold is released and the available balance is restored.

The difference between book balance and available balance is significant. In the deposit layer a customer's money is the book balance of the backing Liability GL account, and the available balance accounts for both active holds and any overdraft limit:

```
Book Balance      = book balance of the backing GL account
Available Balance = Book Balance - Active Holds + Overdraft Limit
```

Holds typically have an expiration time. If not captured within that window, they automatically stop affecting the available balance.

#### Holds Are Off-Ledger

Unsettled holds do not exist as ledger entries. The general ledger only records posted transactions — actual debits and credits that have settled. A hold is an operational concept tracked by the `deposit` layer alone; it doesn't move money and doesn't appear in the general ledger or trial balance.

A hold only touches the ledger when it is **captured** — at that point `deposit.Register` posts a real transaction into the underlying `ledger.Book` with proper debits and credits (debiting the customer's Liability GL account, crediting the counterparty). If the hold is **released**, nothing ever hits the ledger; from an accounting perspective it's as if it never happened.

This is exactly why holds live one layer up from the ledger: the ledger stays a pure record of settled value, and the deposit layer owns the operational state. The ledger is only involved when a hold is captured and converted into a real transaction.

### Idempotency

In distributed systems, clients may retry requests due to timeouts or network failures. Without idempotency, a retry could cause the same transaction to be posted twice.

The idempotency key mechanism prevents this:

1. The client generates a unique key (e.g., a UUID) for each logical operation and includes it in the request.
2. If the system receives a request with a key it has already processed, it returns an error instead of creating a duplicate.
3. The client can then look up the original transaction by the key.

### Transaction Reversal

In banking, posted transactions are never deleted. The ledger is an immutable record. To correct an error, a new "reversal" transaction is posted that exactly offsets the original:

- Every debit in the original becomes a credit in the reversal.
- Every credit in the original becomes a debit in the reversal.
- The net effect on all accounts is zero.

The original transaction is marked as "Reversed" for reporting purposes, and the reversal transaction carries a reference to the original.

## Account Lifecycle

> Account status and its lifecycle are a **`deposit`**-package concern (`deposit.Account` and `deposit.Register`), not a general-ledger one. The `ledger.Book` does not track account status; a GL account simply exists. The `deposit.Register` adds the status machine (`Active`, `Dormant`, `Frozen`, `Closed`) and enforces the transitions below via `Freeze`, `Unfreeze`, `MarkDormant`, `Reactivate`, and `Close`.

In a real banking system, accounts are not simply created and then used forever. They go through a series of states that govern what operations are permitted.

### Account States

| State | Description | Allowed Operations |
|-------|-------------|-------------------|
| **Active** | Normal operating state. The account is open and fully functional. | All: debits, credits, holds, statements |
| **Dormant** | No customer-initiated activity for an extended period (typically 12–24 months, varies by jurisdiction). The bank may charge dormancy fees. | Credits only (incoming payments reactivate the account). Debits and new holds blocked until reactivated. |
| **Frozen** | Temporarily restricted, usually due to a court order, fraud investigation, or regulatory action. | View balance only. All debits, credits, and holds blocked. The freeze may be partial (e.g., allowing credits but blocking debits). |
| **Closed** | Permanently shut down. The account no longer accepts any transactions. | None. Balance must be zero before closing. Historical data retained for audit and regulatory purposes. |

#### What This Implementation Actually Enforces

The table above is the real-world model. This system implements one specific reading of it, and the two differ in one place worth naming:

| State | Money out (withdrawal, new hold) | Money in (credit) |
|---|---|---|
| **Active** | permitted | permitted |
| **Dormant** | `ErrAccountDormant` | permitted |
| **Frozen** | `ErrAccountFrozen` | **permitted** |
| **Closed** | `ErrAccountClosed` | `ErrAccountClosed` |

**The freeze here is a debit block, not a full freeze.** That is the partial freeze the table's `Frozen` row mentions, and it is the common retail case: a garnishment or a fraud investigation stops the customer taking money out while their salary keeps arriving. A **sanctions** freeze is the other kind — funds must not be accepted into the account at all — and one boolean status cannot express both. Separating them is a debit-block and credit-block pair, which real systems carry and this one does not.

**The two directions are asymmetric on purpose, and not symmetric functions.** `CheckWithdrawalTx` takes an amount and can fail for want of money; `CheckCreditTx` takes none and cannot. The only question a credit can answer is whether this account is still somewhere money may land, so `Closed` is the sole state that refuses one.

**`Closed` refusing credits is what keeps `Close`'s own invariant true.** Closing requires a zero balance. A credit landing afterwards leaves a Closed account holding money that no withdrawal can reach, that closing again cannot clear because `Closed` is terminal, and that contradicts the precondition `Close` had just enforced — stranded money, not a restriction.

The enforcement point is worth being precise about, because this layer has **no credit method of its own**. Money reaches a deposit account's GL account from the layers above: a bank funding a customer, a settlement's creditor leg, a lending counterparty. Each posts straight into the general ledger, which knows nothing about account status by design. So the guard is a *callable* check rather than something the register can impose — `Register.CheckCreditTx`, the mirror of `CheckWithdrawalTx` — and it binds only where a caller invokes it. Funding does. See [Next Work](#next-work) for the path that does not.

### State Transitions

```
                  ┌─────────────────────────────────┐
                  │                                 │
                  ▼                                 │
  ┌──────────┐  inactivity  ┌──────────┐  customer  │
  │  Active  │ ──────────▶  │ Dormant  │ ──request──┘
  │          │ ◀──────────  │          │
  └──────────┘  reactivate  └──────────┘
       │                         │
       │ freeze                  │ freeze
       ▼                         ▼
  ┌──────────┐              ┌──────────┐
  │  Frozen  │              │  Frozen  │
  └──────────┘              └──────────┘
       │                         │
       │ unfreeze                │ unfreeze
       ▼                         ▼
  ┌──────────┐              ┌──────────┐
  │  Active  │              │ Dormant  │
  └──────────┘              └──────────┘
       │
       │ close (balance = 0)
       ▼
  ┌──────────┐
  │  Closed  │  (terminal state)
  └──────────┘
```

Key rules:

- **Active → Dormant:** Triggered automatically after a configurable inactivity period. The bank must typically notify the customer before the transition.
- **Dormant → Active:** Any customer-initiated transaction or explicit reactivation request returns the account to Active.
- **Any → Frozen:** Can happen at any time due to legal or fraud reasons. The previous state is preserved so the account returns to it when unfrozen.
- **Active → Closed:** Only permitted when the balance is zero. Requires all pending holds to be resolved and all scheduled payments to be cancelled.
- **Closed is terminal:** A closed account cannot be reopened. If the customer wants to bank again, a new account must be created.

### Overdraft

An overdraft occurs when a transaction would cause an account's available balance to go below zero. Banks handle this in several ways:

- **Hard decline:** The transaction is rejected outright. This is the simplest model and is what this implementation uses for Asset and Expense accounts — the system returns `ErrInsufficientBalance` if a debit would push the available balance negative.

- **Arranged overdraft (credit facility):** The customer has a pre-agreed overdraft limit. Transactions are permitted as long as the negative balance does not exceed this limit. For example, an account with a $0 balance and a $500 overdraft limit can process debits up to $500. Interest is typically charged on the overdrawn amount at a higher rate than standard lending.

- **Unarranged overdraft:** The bank may choose to honor a transaction that exceeds the arranged limit (or where no arrangement exists) as a courtesy, but at significantly higher fees. Regulations in many jurisdictions cap these fees.

From a ledger perspective, overdraft is straightforward: the book balance of a liability account goes negative (from the bank's perspective, the customer now owes the bank rather than the bank owing the customer). The overdraft limit is a business rule enforced *before* posting to the ledger — the ledger itself is simply recording the economic reality.

```
Account balance:        $200   (bank owes customer $200)
Overdraft limit:        $500
Transaction:           -$600   (debit of $600)
New balance:           -$400   (customer owes bank $400)
Available for further: $100    (limit $500, used $400)
```

The available balance calculation with overdraft becomes:

```
Available Balance = Book Balance + Overdraft Limit - Active Holds
```

The limit, both rates and the day count are **effective-dated**. They are not columns on the account: they are rows on a small per-account timeline, one per repricing, each carrying the day it takes effect, and a repricing appends a row rather than editing what an earlier day already said. So the available-balance formula above resolves a limit *as of a day* rather than reading one off the account, and "what could this customer spend, and what were they charged, on 15 July?" is a question with a stable answer. See [Daily Accrual and the Precision Split](#daily-accrual-and-the-precision-split) for why that matters to more than an auditor.

#### Pinned and floating: which parameter comes from where

Those parameters do not all come from the same place, and the split is deliberate:

- The **rate** (and the day-count convention that goes with it) *floats* with the **product**. An account is opened *from* a named catalogue entry — "Basic Current Account" — whose price is its own effective-dated timeline of immutable published versions. Publishing one version reprices every account bound to that product, per day, with no write to any account at all.
- The **limit** is *pinned* to the account. It never comes from the catalogue.

The reason is that these are different kinds of decision. **A rate is a price the bank publishes; a limit is an underwriting decision about one customer's creditworthiness.** Repricing the overdraft book and raising one customer's limit are not the same operation, and a model in which they are the same four fields on the same row makes them look like one. Here the distinction is enforced by the types rather than by a rule someone has to remember: the catalogue's pricing record has no limit field, so "the limit does not float" is a fact the compiler checks.

There is a second dividend. Every withdrawal check needs the limit and nothing else, and the limit is on the account's own row — so that path still answers in one read, with no catalogue lookup. A floating limit would have put a product read on every withdrawal check in the system.

A customer can be given a **negotiated rate** instead of the product's: a *pricing overlay*, carried on the account's own terms timeline rather than in a table of its own, so that setting one and clearing one are ordinary rows ordered against limit changes and product migrations like any other. While an overlay is in force it outranks the product, so a reprice published underneath it does not reach that customer; clearing it puts the account back on the product at whatever the product costs *by then*. No overlay means *float*, and specifically not interest-free — an interest-free account is an overlay with a zero rate, which is a real product and a different, deliberate statement.

Moving an account **between products** is a forward-dated row like any other, which is the point: the days before it still resolve against the product that priced them. A migration is not a rewrite.

#### Forward-only publication, and what this system does not have

A catalogue version can only be published **effective today or later**. A version effective in the past would move interest already charged on every account bound to the product at once, and the audit log would be the only control on it. The real answer to that is a four-eyes maker-checker regime, and **this system does not have one** — so it refuses the operation instead. This is deliberately less capable than a real core banking system, and worth stating plainly rather than glossing.

Retroactivity therefore stays where its blast radius is one named customer: the per-account pricing overlay, which may be backdated exactly as any other terms row may be, and whose delta the accrual posts as ordinary correction interest. Correcting a mispublished product rate is a set of per-account overlays — which is laborious, and should be, because it is a set of individual decisions about money already taken from named people.

A published version is frozen, and a **content hash** over its identity and its pricing is stamped at publication and **verified every time a day is priced**. A hash that were merely stored would be theatre; verifying it on read is what makes it a control on the one hole a domain-layer refusal cannot cover — a direct `UPDATE` to a published row. A mismatch fails the accrual rather than pricing a day from a row nobody published.

Taking a product **off sale** (retiring it) does not unprice anything: the accounts already sold from it keep resolving against its versions for as long as they live. A bank that could not express that would have to keep dead products on sale.

An overdraft is not free once a rate is set on it. Interest accrues **daily** on the drawn (negative) balance — the arranged rate up to the limit, and the higher **unarranged rate** on any amount beyond it, so that exceeding the limit is never cheaper than staying inside it. The unarranged rate is a *surcharge*, and an account priced without one charges the arranged rate on the excess instead: its absence never means the money beyond the limit is free, which would invert the whole point of having a limit. It is **charged to the account monthly** — capitalized into the debit balance the same way a credit facility's interest is capitalized (see [Daily Accrual and the Precision Split](#daily-accrual-and-the-precision-split)) — which is what makes an overdraft **compound**: next month accrues on a balance that already includes this month's interest.

The load-bearing point is what does *not* happen. Once a customer is overdrawn, the bank's balance-sheet classification of that money flips: what was a liability (money the bank owes the customer) is now, from the bank's perspective, an asset (money the customer owes the bank). A real bank's general ledger reflects this with a posting — a nightly sweep or reclassification journal that moves the drawn amount onto an Asset-side receivable. **This system posts no such thing.** The overdrawn balance stays exactly where it always was, in the customer's Liability GL account, merely negative; and the Asset-side total — "how much is drawn on overdraft across the book" — is computed by aggregating `Σ max(0, −balance)` over every deposit account, the same on-demand aggregation that produces "total customer deposits". It is not stored, and no transaction ever posts the **drawn amount** to an Asset account (`deposit.TestTotals_OverdraftsAreDerivedAndNothingIsPosted` pins exactly this, over accounts with no rate set). The interest on that drawn amount is a different matter and does post: once a rate is set, each day's accrual debits an Asset account — the account's own accrued-interest receivable — because interest earned and not yet collected genuinely is a receivable. What never happens is the *balance* being reclassified.

The reason is the same one that keeps a balance from being a stored column anywhere else in this system: the drawn amount **has no independent existence**. It is the negative balance of the customer's own account, read by sign — the same fact, viewed the other way — not a second fact that happens to agree with it. Storing it separately, even as a "derived" mirror kept in sync by a sweep, would be exactly the drift a unified ledger is built without: two numbers that are supposed to agree and that nothing but discipline keeps agreeing. See `docs/deposit-accounts-vs-subledger.md` §7 for the general pattern (a control account maintained by reconciliation, versus a total computed by aggregation) and the [Lending](#lending) section below for why this is also the reason an overdraft has no facility record of its own.

## Lending

> Lending is a **`deposit`**-and-**`lending`**-package concern (`deposit.Register` for the overdraft's terms, `lending.Portfolio` for the other two products), built on top of the same `ledger.Book` — the pure day-count and accrual math lives in a third package, `interest`, that neither of them owns exclusively.

### Three Products, and Why the Third Is Not a Facility

This system extends credit to a customer three ways:

1. **Term loan** — a fixed principal, disbursed once, repaid on a schedule generated at disbursement.
2. **Revolving line** — a reusable commitment the customer draws down and repays repeatedly, billed in cycles rather than against a fixed schedule.
3. **Arranged overdraft** — a limit that lets a current account's balance go negative, covered above.

The first two are `lending.Facility` records: each wraps two Asset GL accounts of its own (below), and its drawn principal is the book balance of one of them — a fact of its own, existing whether or not the customer holds any other account, because a €10,000 five-year loan is €10,000 owed independently of anything else. (A facility does not *store* that principal any more than anything else here stores a balance; what it stores is the contract — see below.)

The overdraft is deliberately **not** a third facility. Its "drawn principal" is the current account's own negative balance, read by sign; giving it a facility record would store a number that already exists elsewhere, which is precisely the duplication this system's unified ledger design exists to avoid. That is also why an overdraft has no amortization schedule and no commitment record: `Commitment` and a schedule are contract artifacts a term loan and a revolving line need and an on-demand, repayable-any-time overdraft does not.

### Two GL Accounts Per Facility

Every `lending.Facility` carries exactly two Asset accounts:

- **Principal** — what is owed on drawn money.
- **Accrued interest receivable** — interest earned and not yet collected.

They are separate because a repayment allocates **interest before principal**, and one account cannot express a split between two things it does not distinguish. Disbursing a term loan or drawing a revolving line debits Principal (and credits the customer's account, or wherever the money goes); a day's accrual debits the receivable and credits an Interest Income (Revenue) account; a repayment credits the receivable first, then whatever remains credits Principal.

`Drawn` is never a stored field: it is the principal account's book balance, the same discipline every other derived balance in this system follows. `AccruedInterest` (in whole minor units) is `Minor()` of the facility's own exact accrued-interest record — which the receivable account's balance always equals, by the invariant the next section is about, so the two figures agree to the cent while the record is the one being read. `Commitment` (a term loan's original principal, or a revolving line's limit) is stored outright, because it is a fact about the contract rather than a fact about postings so far.

### Daily Accrual and the Precision Split

Interest accrues once per business day, on the **drawn** principal (an undrawn commitment costs nothing), under a day-count convention — `ACT/365`, `ACT/360`, or `30/360` — that is a real term of the contract, not an implementation detail: the same balance at the same rate accrues a different amount under each.

A day's interest on a real balance is mostly fraction, and rounding it away daily is a measurable annual error. So a facility (and an overdraft) holds its accrued interest twice, at two precisions, and the split is the point:

- The **record** holds it in **micro-minor-units** (`interest.Accrued`, minor units × 1,000,000) — exact, never rounded.
- The **ledger** holds `Accrued.Minor()` — the record rounded to a whole minor unit, half away from zero — because a posting cannot be a fraction of a cent.

Every day's posting is the **change** in the rounded value, not the day's raw interest, so the ledger and the record can never drift apart. A €10,000 five-year loan at 6% (`ACT/365`) accrues 164.383561 cents a day, exactly:

| Day | Cumulative accrued (micro-minor-units) | `Minor()` (cents) | Posted that day |
| --- | --- | --- | --- |
| 1 | 164,383,561 | 164 | 164 |
| 2 | 328,767,122 | 329 | 165 |
| 15 | 2,465,753,415 | 2,466 | 165 (up from day 14's 2,301) |

Most days post 164; some post 165 as the rounding ticks over — the record is exact throughout, and the ledger only ever holds its rounding. A revolving line's interest is not just held this way but **capitalized**: `ChargeInterest` charges the rounded receivable into principal at the end of a billing cycle, clearing it back toward zero and folding this cycle's interest into what next cycle accrues on — which is what makes a revolving balance **compound**. Charging the rounded figure rather than the exact one leaves the record with a residue of up to half a minor unit either way; a €1,000 draw at 18% accrues 1,479.452040 cents over 30 days, of which only 1,479 is capitalized (rounding down), leaving the record at **+452,040** micro-minor-units. `Minor()` of a residue that size is 0, so the ledger never sees it and the next cycle's accrual absorbs it — but that is not true of *every* residue: at an EXACT half of a minor unit, `Minor()` rounds away from zero to ±1 even though the receivable is already back to zero. That is why `Close`/`CloseTx`, on both `deposit` and `lending`, test the receivable's own ledger balance rather than the record — the record may legitimately disagree with the ledger by a sub-minor-unit amount, which is the entire reason it is kept at higher precision.

#### The Recompute Window Opens at Inception

Accrual is a **recomputation**, not an increment: every run re-derives the account's or facility's whole history from its value-dated balance and posts the change in the rounded value. That is what makes a backdated posting correct itself — the days it takes effect over are re-derived with it in place, the gross moves, and the difference is posted as a true-up.

How far back "whole history" reaches is decided by the product terms. While terms were mutable columns, the window had to start at the last repricing: reaching further would have re-derived old days at *today's* rate. That bound was an accident of the storage, and it had a cost the customer could not see. A repricing closed the old window out and opened a new one at itself, so a back-value landing behind it was trued up only **from the repricing forward** — the window's opening balance is value-dated, so the posting did move it — while the days between where it took effect and the repricing kept the interest computed without it, permanently. **The repricing was a line the correction stopped at.**

Because terms are now a timeline, every day can be re-derived at the terms that were actually in force on it, and the window opens at account opening (or, for a facility, at origination). The days before the first advance accrue nothing on their own, because the drawn balance across them is zero.

The cost of that is real and is accepted deliberately: every nightly run re-derives every day the account has ever had, so a ten-year-old account is roughly 3,650 day-iterations a night. The cost is arithmetic rather than I/O — the balance series is still one query over the window — and at this scale it is nothing. **Checkpointing** is what removes it, and the end-of-day snapshots this system already writes and never reads are the raw material; see [End-of-Day Snapshots](#end-of-day-snapshots) and *Next Work*.

### Two Amortization Methods

A term loan's schedule is generated once, at disbursement, as a plan — one row per instalment with its own principal and interest due — chosen at opening between two methods:

- **Annuity** — every instalment has the same total payment; the interest share falls and the principal share rises as the balance amortizes.
- **Equal principal** — every instalment repays the same principal; the total payment falls as interest on a shrinking balance falls with it.

Under either method the **last instalment** is not computed the same way as the rest: it repays whatever principal is actually left, so that the scheduled principal across every row sums to the disbursed principal exactly, however rounding fell along the way, rather than leaving a stray cent unaccounted for.

A revolving line has no upfront schedule — there is nothing to generate one from until the customer draws. Its instalments are appended one per billing cycle, each one that cycle's interest charged plus a minimum-payment share of the drawn balance.

### Repayment: Against What Accrued, Not the Schedule

A repayment settles interest before principal — but the *interest* it settles is what actually **accrued**, not what the schedule projected for that instalment. On the €10,000 loan above, the schedule's first-month interest is a flat twelfth of 6%: €50.00. Thirty days of `ACT/365` accrual come to **€49.32**, not €50.00 — the calendar month is 30 or 31 actual days, never exactly a twelfth of a 365-day year. Paying the scheduled €193.33 instalment credits €49.32 to the receivable (what was actually earned) and the remaining €144.01 to principal — 68 cents more principal than the schedule's own €143.33 assumed, because the plan overstated the interest by exactly that much.

This is exactly why the **30/360** day-count convention exists: under it, every calendar month is defined to be precisely a twelfth of a year, so the scheduled interest and the accrued interest always agree to the cent and a repayment never has to reconcile the two. Under `ACT/365` or `ACT/360` they generally do not, and the difference — small, and different every month — is absorbed silently by the principal portion of each payment. The schedule stays a **plan the facility is checked against**, never a statement of what is actually owed; that fact is always read from the accrued-interest record.

### Arrears and Non-Performing

A facility's delinquency is **computed from its schedule**, not stored as a stream of events: days-past-due is the calendar-day age of the *oldest* instalment that is still due and unpaid, and paying that instalment is what moves the clock forward — a borrower who is permanently one payment behind stays visibly one payment behind rather than looking current between due dates. Those days sort into five bands: `Current`, `1-29`, `30-59`, `60-89`, and `90+`.

**Non-performing** is set once days-past-due reaches 90, and it **marks only**: nothing about how interest accrues, how it posts, or what the schedule says changes because of it. Non-accrual accounting (where a non-performing loan stops recognizing interest into income) and expected-credit-loss provisioning are both real next steps for a production system and both deliberately out of scope here — see `docs/expansion-roadmap.md`.

## Settlement Cycles

### What Is Settlement

Settlement is the process by which a transaction becomes final and irrevocable — when ownership of funds actually transfers between parties. The distinction between *executing* a transaction and *settling* it is fundamental to banking.

When Alice sends Bob $100 via bank transfer, several things happen in sequence:

1. **Initiation:** Alice's bank debits her account and sends a payment instruction.
2. **Clearing:** The payment networks validate, match, and route the instruction. Net positions between banks are calculated.
3. **Settlement:** The actual movement of funds between banks occurs, typically through central bank accounts. The transaction is now final.

The time between initiation and settlement is the **settlement cycle**.

### Common Settlement Cycles

| Payment Type | Typical Cycle | Details |
|-------------|--------------|---------|
| **Card payments** | T+1 to T+2 | The merchant's bank receives funds 1–2 business days after the transaction. The cardholder sees an immediate hold, then a posted transaction after settlement. |
| **ACH / Direct Debit** | T+1 to T+2 | Batched and settled through the Automated Clearing House. Same-day ACH is available but not universal. |
| **Wire transfers (domestic)** | T+0 (same day) | Settled in real-time or near-real-time through systems like Fedwire (US) or CHAPS (UK). Irrevocable once sent. |
| **Wire transfers (international)** | T+1 to T+3 | Routed through correspondent banks via SWIFT. Each intermediary adds latency. |
| **Check deposits** | T+1 to T+5 | Varies significantly. Reg CC (US) sets maximum hold periods. The bank may grant provisional credit before final settlement. |
| **Securities (stocks, bonds)** | T+1 | Most equity markets settled T+2 historically but moved to T+1 in 2024. |
| **Real-time payments** | Instant | Systems like FedNow (US), Faster Payments (UK), and SEPA Instant (EU) settle in seconds, 24/7. |

### Settlement and the Ledger

Settlement cycles directly affect the booking date vs. value date distinction described earlier:

- **Booking date** is typically when the bank initiates or receives the payment instruction.
- **Value date** is when settlement occurs and funds become economically available.

During the settlement window, the transaction exists in a **pending** state. The bank has recorded the intent (booking) but the funds have not yet moved (settlement). This gap creates several practical considerations:

- **Interest accrual** starts on the value date, not the booking date. A check deposited on Friday does not earn interest over the weekend if the value date is Monday.

- **Counterparty risk** exists during the settlement window. If the sending bank fails between initiation and settlement, the funds may not arrive. This is why settlement finality matters — it marks the point at which the transaction can no longer be unwound.

- **Holds bridge the gap** between initiation and settlement. When a customer deposits a check, the bank may place a hold that expires once settlement is confirmed, preventing the customer from spending funds that have not yet arrived.

- **Reconciliation** of nostro/vostro accounts (the accounts banks maintain with each other) happens as part of the settlement process. Discrepancies between expected and actual settlements must be investigated and resolved.

## Payments, Clearing, and Settlement (the `payment` package)

The `ledger` package described above is a single bank's book of record. But a *payment* — sending money from one person's account to another's — usually crosses a boundary between two banks, and that is where clearing and settlement live. The `payment` package builds that world on top of the ledger so the mechanics are concrete and testable.

### Why a Separate Package

A payment system is not the same thing as a general ledger. The ledger answers "what does this bank owe and own?"; the payment system answers "how does money actually move between banks?". Keeping them in separate packages mirrors how real institutions are organised — the payment rails (SEPA, card networks, RTGS) sit *above* each bank's accounting system and instruct it. The `payment` package depends on `ledger` and orchestrates postings into it; the ledger has no knowledge of payments.

### The Multi-Bank Model

The key design choice is that **each participant bank keeps its own `ledger.Book`** (with a `deposit.Register` over it for its customer accounts), and there is **one more `ledger.Book` for the central bank**. Banks never write into each other's books — they only meet at the central bank, where each admitted member holds a **reserve account** the central bank opened for it. This is what makes the difference between clearing and settlement real rather than abstract:

- **Clearing** is the exchange and *netting* of payment instructions. The banks agree on who owes whom. No central-bank money moves.
- **Settlement** is the movement of reserves between banks at the central bank. This is the moment of *finality*.

**"Never writes" is the claim, and it used to be narrower than "never reaches".** A submitting bank used to read a book that was not its own, on the happy path of every submission: the `pacs.008` it sends has to name the payee, and `payment.partyTx` read that name out of the *payee's bank's* register. The same crossing ran the other way for a collection. It was a shared-store artefact, and it was measured rather than assumed — `mesh.TestWhichBooksEachBankActuallyReaches` asserts the exact set of books each actor touches, and the crossing was found by that measurement rather than predicted.

What closed it was a domain change, not a transport one, and it is the fact a real payment network runs on: **a payer's bank knows the payee's name because the payer typed it onto the instruction, not because building the instruction can look it up** — banks hold no register of each other's customers for a payment to consult. (This system's own directory, `GET /directory`, can resolve a payee's name across the network before any instruction exists — it's what lets a send form show which bank an IBAN belongs to — but that answer never reaches the payment: what gets stored is what was typed, not what was resolved.) `InitiatePaymentRequest` now carries the counterparty's **name** alongside the account; `Payment.DebtorDetails`/`CreditorDetails` (`payment.PartyDetails{Agent, Name}`) store what each side asserts, filled from the submitting bank's own register for its own side and taken verbatim from the request for the other; and `SubmitPaymentTx` refuses an unnamed counterparty outright (`ErrCounterpartyNotNamed`) rather than filling the gap by reading someone else's book. `payment.partyTx` no longer exists — building an outbound message reads nothing across the boundary it crosses.

**The name, and only the name.** The other half of `PartyDetails` is the counterparty's **agent** — its bank's BIC — and that one is *derived*, not asserted: `SubmitPaymentTx` reads the **bank row** for the participant the payment already names and ignores whatever the caller supplied. The reason is the one element's job. The agent goes out as `CdtrAgt`/`DbtrAgt`, and the clearing house **routes on it** with no store read of its own, so a payer allowed to type it is a payer allowed to choose which bank receives the payment — measured doing exactly that: a push whose creditor agent named the payer's own bank came straight back to its sender, and a pull whose debtor agent named the collector had the *collecting* bank post the debit in the payer's bank's book. `mesh.TestAWrongCounterpartyAgentDoesNotMisroute` is the pin. This is also what a real network does — SEPA has been IBAN-only since 2016, and the originating bank derives routing rather than trusting a BIC somebody typed. Reading the bank row is not a crossing: `banks` rows are network-scoped and belong to no single book, so the measured book sets are unchanged. **Routing needs the bank, not the name; the payer supplies the name, not the bank.**

What is still open is a different crossing, running the other way: the RECEIVING bank resolves an inbound address by sweeping every member's register (`ResolveIdentifierTx`), because this network keeps no central index of addresses — `GET /directory` above is that same sweep with an HTTP route on it, not a service that holds the answer — and answering "whose IBAN is this" means asking each bank in turn. Narrowing that sweep is [sub-project 8's](docs/expansion-roadmap.md) remaining work here.

Each participant's chart of accounts holds:

| Account | Type | Purpose |
|---|---|---|
| Customer deposits | Liability | What the bank owes each customer. |
| Clearing Suspense | Liability | In-transit funds that have left a customer but not yet settled between banks. Returns to zero once this bank has booked **both** its halves of the cut-off — the mirror leg from the central bank's `camt.053` and its creditor legs from the clearing house's advices — which is *after* settlement, not at it. |
| Reserve at Central Bank | Asset | The bank's claim on the central bank. **Mirrors** the bank's reserve account in the central-bank ledger (the classic nostro/vostro reconciliation), and moves when this bank books the `camt.053` it is sent — which is after the cut-off has already settled, so the two sides are equal only once it has. |
| Unclaimed Balances | Liability | Where a credit goes when the payee's account will not take it — closed, and therefore terminal. The bank still owes the money, to whoever eventually claims it, which is why it is a liability and not the bank's own asset. |
| Returns Receivable | Asset | A claim on a biller, opened when this bank has to honour a [return](#sepa-direct-debit-and-returns) it cannot fund out of the biller's own closed account. The opposite class from Unclaimed Balances above it, and deliberately so: that one is money the bank owes to somebody it cannot name, this is money owed **to** the bank by somebody it can. |

#### Addressing

An account has exactly one **internal number** — `deposit.AccountID`, the key the bank uses to find its own row — and a set, possibly empty, of **external identifiers**: the addresses a counterparty quotes to pay it. The two are never interchangeable; `AccountID` is never handed to anyone outside the bank, and an account nobody pays from outside it needs no identifier at all.

The set is plural because addressing is not one scheme. A SEPA transfer quotes an IBAN; a UK payment quotes a sort code plus account number; US ACH a routing number plus account number; instant schemes route through a proxy alias — phone or email; a card PAN is an alias for a funding account and nothing more. ISO 20022 models exactly this: `CashAccountIdentification` is a *choice* between `IBAN` and a generic `Othr` (identification, scheme name, issuer) triple, not an IBAN field with everything else bolted on — which is why `Identifier` here is a `(scheme, value)` pair rather than one field per kind.

**The scheme decides which kind addresses it**, `Scheme.AddressedBy() deposit.IdentifierScheme`, exactly as `Scheme.Asset()` decides what it settles in. A leg whose account carries no identifier in that scheme is refused (`ErrUnaddressableAccount`), and so is a quoted identifier that is not one of the account's *in that scheme* (`ErrIdentifierMismatch`) — so an account holding both an IBAN and a card PAN cannot have a SEPA transfer accepted against its PAN. Both checks run **per leg**, in the same place and for the same reason as the [asset check](#a-scheme-declares-its-asset): each bank runs them over its own customer's account, and neither bank runs them over the other's.

**A payment records the address it was reached by, whether or not the caller supplied one.** When a leg quotes nothing, the bank that owns that leg fills in the account's single identifier in the scheme's scheme; when the account holds several, it refuses (`ErrAmbiguousAddress`) rather than picking by list order, and the caller names one. The submitting bank does this for its own side, at submission (`SubmitPaymentTx`); the far side's is the *other* bank's to fill in, when the message reaches it (`AcceptInboundTx`), because `addressFor` runs per leg, at the bank that owns that leg — the account-owning bank is the authority on how its own account is addressed, not the bank that merely quoted it. In practice the far address is always already there on the HTTP path — the request has to quote one for the message to be buildable at all, and a push that quotes none is refused `422` — so what `AcceptInboundTx` re-derives is the value that was quoted. The back-fill is where it has to be; it is simply not a state this API can reach. Storing the address rather than deriving it later is what makes a settled payment immune to a subsequent address change — and it is why a **mandate** compares its parties by `(participant, account)` only, `PartyRef.SameParty`: a mandate authorises debits from an *account*, so reissuing the debtor's IBAN — a remove plus an add, which moves neither balance nor history — leaves every mandate on it working.

**Uniqueness stops at the bank.** A `deposit.Register` spans one book, and that is the correct line, not a shortcut: a bank-issued identifier is globally unique by construction — an IBAN carries its bank's code, a PAN its BIN — while a proxy alias carries no issuer at all, which is why phone and email need a *separate* central lookup service (SEPA's Proxy Lookup Service, India's UPI) and why this system has none.

`payment.Network.ResolveIdentifier` sweeps every member bank and refuses ambiguity rather than taking the first hit — the same rule settlement applies when it refuses to default a cycle's asset. The write-time half of this is a separate check, inside the bank: `deposit.Register.checkIdentifierFreeTx` refuses a second account at the *same* bank taking an identifier another one there already holds (`ErrIdentifierTaken`), deliberately with no `UNIQUE` constraint behind it — a constraint one store could hold and the other, a Go map, could not would let `store/mem` and `store/pg` disagree. It runs through `ListDepositAccountsByIdentifier`, so it inherits that lookup's comparison rule *whole*: two identifiers are the same address when their schemes are equal and their values are equal after compaction, which for an IBAN means `SE89-AURORA-1001` and `SE89AURORA1001` are one address and the second spelling is refused. That is the point rather than a side effect — an account holding two spellings of one address resolves either way, so no lookup would ever complain about it. `ResolveIdentifier` is what makes that safe **for routing**: the missing constraint lets a race create a within-bank duplicate, and looking that address up — through `GET /directory`, or anywhere a payment starts from an address — refuses rather than guessing, exactly as it does for a duplicate across two banks that no single register could ever have seen coming. It is a claim about the *directory*, and only about it: `SubmitPaymentTx` is given an account id and never resolves anything, so two accounts colliding on one address both stay payable by id. That is the right answer rather than a gap — the accounts are real and distinct, and it is the address that is ambiguous, not either of them — but it is why the refusal belongs to the lookup and not to the payment.

**An address is compared in its canonical form, not literally — and for an IBAN those differ.** An IBAN is transmitted and canonically stored without separators and *displayed* in groups; this system stores the readable form (`SE89-AURORA-1001`) because a mod-97 check digit was refused for the same reason, while an IBAN inside a `pacs.008` carries none (`SE89AURORA1001`). They are one address, and compaction does not run backwards, so the only way to match them is to strip the separators from **both** sides: `deposit.Identifier.MatchValue`, which `ListDepositAccountsByIdentifier` uses in both stores and which `storetest` pins so the Go map and the SQL cannot drift apart. Without it a bank emits an address it then cannot resolve — every seeded account unreachable from a message it produced itself. The rule is the IBAN scheme's alone: nothing else here has a display form, and stripping punctuation out of a card PAN would merge two addresses a scheme keeps apart. What is stored and what a payment records are untouched; only the comparison is canonical.

The central-bank ledger holds one **Reserve: \<Bank\> (\<asset\>)** liability account per *admitted member* per asset (the central bank owes each member its reserves in each kind of money it issues), plus a balancing **Settlement Assets (\<asset\>)** account, also one per asset, used when reserves are funded. The central bank opens those reserve accounts itself, one per asset, when it answers a bank's [application to join](#admission-a-bank-exists-before-it-joins-a-scheme) — a bank that has been founded and not admitted has none.

**The asset dimension runs through both sides.** Every internal account above exists **once per [asset](#assets-what-an-account-is-denominated-in) the participant operates in** — on the commercial bank's books *and* on the central bank's. A bank clearing both a euro scheme and a dollar one holds two clearing-suspense accounts and two reserve accounts, and the central bank holds two matching reserve liabilities for it, because a GL account is bound to a single asset and because netting a euro position against a dollar one is not a smaller number, it is a meaningless one. The euro reserves a central bank has issued are not backed by the dollars it has issued, so even the balancing Settlement Assets account splits per asset.

The account names carry the asset in parentheses — `Reserve at Central Bank (EUR)`, `Reserve: Aurora Bank (EUR)`, `Settlement Assets (EUR)` — which is what keeps a chart of accounts holding several of each readable. `Bank.AccountsFor(asset)` resolves one bank's set for one asset; a bank that does not operate in an asset gets `ErrParticipantAssetNotFound`. `BankAccounts.Settlement` is the central-bank leg of that set, and it is per asset like the rest — and unlike the rest it names an account in *another institution's* book, which is why it is empty on a bank the scheme has not answered for yet. See [Admission](#admission-a-bank-exists-before-it-joins-a-scheme).

Settlement resolves that set from the **cycle's** asset, which comes from the cycle's scheme, once for the whole batch. A member holding a net position but no accounts in that asset fails the entire settlement before anything is posted — exactly the treatment an underfunded member gets. There is deliberately no fallback: defaulting to euro would settle a dollar cycle in the wrong money, and doing so quietly, in the one place in the system where money becomes final.

### Admission: A Bank Exists Before It Joins a Scheme

**Founding a bank and admitting it to a scheme are two different things, and the second one can be refused.** A bank with a licence has a book, a chart of accounts, its per-asset plumbing accounts and a deposit product to sell — that is `payment.FoundBankTx`, it is the bank's own unit of work, and what comes out of it is `Founded`. Membership of a scheme comes later, from two other institutions, and until it does the bank is a working bank that is in no scheme.

**This reverses a ruling this system used to make, and the reversal is the domain content.** Admission was once `AddParticipantTx`: one unit of work that wrote the bank's chart of accounts, its settlement accounts in the *central bank's* book and its row in the *clearing house's* roster, all together — justified, in as many words, "so a bank can never exist without the accounts it needs". That guarantee is not one a real admission has. A bank is licensed and built by its own supervisor long before any scheme has heard of it; joining a scheme is an application to somebody else, and an application that cannot be refused is not an application. The atomic write was buying a consistency the domain does not have, and it was buying it by letting one institution write in two others' books. It is deleted.

What a founded bank can do is its own book, and that part is unrestricted: it opens customer accounts, publishes products, adds ledgers. What it cannot do is anything that needs another institution. **It cannot fund a customer account** — this is the one people guess wrong, because cash paid in raises the bank's reserve at the central bank in the same unit of work, and there is no reserve to raise until a settlement agent has opened one. `POST /deposits` answers `422` naming the *membership*, not the account.

**A bank's settlement account is opened by the central bank, in the central bank's own book.** The bank does not hold it and never did — it is `Reserve: <Bank> (<asset>)`, a liability of the central bank's, numbered in the central bank's chart of accounts, and the bank's own row records only its *number*. That is an account holder knowing their IBAN, not an account holder holding a ledger. The two records are deliberately separate rows with separate owners: `payment.SettlementMember`, keyed by BIC, is the central bank's record of an account it holds; `BankAccounts.Settlement` is the bank's note of what it was told.

**The scheme's routing directory is the clearing house's, and it says who may be *addressed* — not who exists.** `payment.RosterEntry` carries a BIC, the assets that member clears in, and the reference of the admission that put it there. It carries no account of any kind, because a clearing house holding one would be holding the means to reach into a bank's ledger; and it carries no name, because the message it is written from does not deliver one. A bank absent from it exists perfectly well. It is simply not somewhere this scheme will send a message.

**Admission is a sequence, and the order is the point.** The settlement account is opened first and the routing entry is written second, because a scheme will not route to a member that cannot settle:

```
joining bank  --acmt.007-->  clearing house  --acmt.007-->  central bank
joining bank  <--acmt.010--  clearing house  <--acmt.010--  central bank
```

Read left to right: the **joining bank** founds itself synchronously and then applies, one `acmt.007` per asset — the schema's `Acct/Ccy` is one currency per request, so a bank joining in two assets applies twice, and a single process id (`Refs/PrcId`) on every message is what says the two are one admission. The **clearing house** relays and holds nothing; the only thing it refuses before relaying is a BIC already in its roster under a *different* admission. The **central bank** opens one account per asset in its own book, writes its own member row keyed by BIC, and answers `acmt.010` — or `acmt.011`, which is a refusal in prose rather than a code, because `acmt` carries no status-reason code set. The **clearing house** then writes its routing entry *from that acknowledgement* and only then forwards it, so a bank told it is a member is one the scheme can already route to. The **bank** records the account numbers it has been told and becomes `Member`.

Four units of work at three institutions, so `POST /members` answers **`202 Accepted`** with a founded bank rather than `201 Created` with a member: whether the scheme accepts is decided elsewhere and arrives as a message. An operator learns the answer by reading the bank back. An interrupted admission therefore leaves a founded bank rather than an orphan, and calling `POST /members` again on the same BIC re-drives it — nothing is founded twice.

**One caveat, measured rather than assumed.** "A founded bank cannot be paid" is the intent and is not enforced. Routing in this system is the mesh's actor table, not the clearing house's roster, so a payment addressed to a founded bank that still has an actor is submitted (`202`), relayed, accepted and cleared like any other — and the *cut-off* is where it fails, because the clearing house cannot name a non-member in the `pacs.009` it sends the central bank. That takes the **whole cycle** down, not just the one payment: every other member's payments stay `Cleared`, their payees unpaid and their payers' money sitting in suspense. There is no remedy that does not involve admitting the bank — [`POST /cycles/{id}/settle`](#the-clearing-house--8082) fails in exactly the same place, and rejecting the offending payment is refused `invalid payment state transition`. Admitting it makes the whole cycle settle on the next instruction, so nothing is lost, and the thing that makes a bank genuinely unreachable in this model is losing its actor (a restart rebuilds actors from the roster, so a founded bank gets none) — a property of the transport rather than a refusal anybody makes.

#### What Admission Is Not

Two things here are the closest true shape rather than the actual one, and both are worth knowing before quoting any of it as a fact about the real world.

**Scheme membership travels on no message at all.** Joining SEPA is contractual — an adherence agreement, signed. There is no ISO 20022 message for it and this system does not invent one. What travels here is the *settlement-account request*, and the routing entry falls out of its acknowledgement, which is why the clearing house ends up writing a row from a message it neither sent nor was addressed on.

**A real central bank does not open an RTGS account this way either.** `acmt` is eBAM — electronic Bank Account Management — designed for a corporate opening an account at its bank. A bank's account at its central bank is *reference data*: in TARGET it is CRDM static data and `reda` messages, set up by the central bank's own operations rather than requested over a payment network. What this system models with `acmt` is the **sequence and the ownership** — who may open which account, in whose book, and in what order — and those are real. The envelope carrying them is not.

### Payment Schemes

Different payment products behave differently, so the package abstracts them behind a `Scheme` interface:

```go
type Scheme interface {
    ID() SchemeID
    Direction() SchemeDirection       // Push (payer initiates) | Pull (payee initiates)
    SettlementModel() SettlementModel // Net (batched) | Gross (instant, per-payment)
    Asset() ledger.AssetCode          // the one asset this scheme carries
    AddressedBy() deposit.IdentifierScheme // the kind of address it routes on
    RequiresMandate() bool
    AllowsReturn() bool
    SettlementDelay() time.Duration   // determines the clearing-suspense leg's value date

    // The two halves of validation, split by which bank can see the answer.
    Validate(ctx context.Context, p *Payment, sc SchemeContext) error        // the DEBTOR's bank: funds
    ValidateMandate(ctx context.Context, p *Payment, sc SchemeContext) error // the CREDITOR's bank: the mandate
}
```

Two schemes ship today, both net-settled:

- **SEPA Credit Transfer (`SCT`)** — a **push** payment: the payer's bank initiates and sends the funds (T+1). Maps to the ISO 20022 `pacs.008` message.
- **SEPA Direct Debit (`SDD`)** — a **pull** payment: the payee's bank collects funds from the payer under a previously signed **mandate** (T+2). Maps to `pacs.003`.

**Both of them allow returns.** `AllowsReturn()` reports `true` for `SCT` as well as `SDD`, and `PostReturnLegTx` refuses only a scheme that reports `false` — so a settled credit transfer can be sent back here, which is the domain's rule and not a slip: a credit transfer return is a real SEPA R-transaction, sent by the *beneficiary's* bank when it cannot apply the funds. What separates the two schemes is who has cause to ask for one, not whether the scheme permits it; see [SEPA Direct Debit and Returns](#sepa-direct-debit-and-returns).

Crucially, money always flows **debtor → creditor** regardless of who *initiates*. This is why the same posting machinery serves both schemes: `postDebtorLegTx` is one function, and whoever runs it is the debtor's bank.

What `Direction` governs is bigger than it used to be, and it is worth stating exactly, because three separate things hang off it:

- **Which bank submits.** A push is submitted by the payer's bank, a pull by the payee's — because a collection is the payee asking for what it is owed. The bank's `POST /payments` refuses a submission from the wrong end.
- **Which half runs where.** `SubmitPaymentTx` runs the *submitting* bank's half and `AcceptInboundTx` runs the other one. For a push that means the debtor half at submission and the creditor half on receipt; for a pull it is the other way round, so **a collection posts nothing at all when it is submitted** and the debtor leg is posted by the payer's bank when the `pacs.003` reaches it. That is the only moment any actor in this system has been able to look at the account being collected from, which is why a collection refused for want of funds can only ever be refused there.
- **Whether a mandate is required**, and — because in SEPA the *creditor* holds the mandate — that it is the creditor's bank that checks it. On a pull that is synchronous, at submission, so a revoked mandate comes back to the caller as a `4xx` and never as a message: this system never *rejects* a collection with `MD01`, though a debtor's bank may still *return* a settled one with it.

**What no direction answers is a payment that never leaves one bank**, and that one is refused outright. When the payer and the payee are customers of the *same* institution — an **on-us** payment — nothing crosses between banks: no interbank obligation comes into existence, so there is no position for a clearing house to net, no reserves for a settlement agent to move, and no `camt.053` that could tell a bank anything about a book it already holds. A real bank recognises the beneficiary as its own and moves the money between two of its own deposit accounts; the payment never reaches a scheme at all. So `Mesh.Submit` refuses it (`mesh.ErrOnUsPayment`, `422` from either `POST /payments`), before the submitting bank's half has run and before any row exists.

That refusal is about the **route** and not about the payment: a transfer between two customers of one bank is an ordinary product, and the honest statement is that this system does not offer it yet. Submitted to clearing anyway it produced three different wrong answers, one per institution — a cycle whose only payment netted to zero settled nothing and stranded at `Cleared`; a return sent that bank two statements of the *same* reserve account under the same reference, so its own mirror moved by an amount the central bank's record of it did not; and the returning bank, being both parties, held both legs and refused its own customer's unconditional refund. Each is a symptom of an instruction that should never have been handed to a clearing house, which is why one refusal at the door replaces three patches further in.

Other **net-settled** schemes drop in by implementing `Scheme` and registering it — the orchestrator does not change. **Instant** and **card** schemes need a little more wiring; see [Next Work](#next-work).

#### A Scheme Declares Its Asset

`SCT` and `SDD` both return `EUR`, and that is not a simplification. SEPA is the **Single Euro Payments Area**; its schemes carry euro and nothing else, and a "SEPA dollar transfer" is not a thing that exists. A scheme in another currency is another scheme, with its own rulebook, its own clearing cycles and its own settlement arrangements — which is exactly why `Asset()` sits on the scheme rather than on the payment.

**Each account is checked against the scheme's asset by its own bank**, and a mismatch is `ErrAssetMismatch`. The check runs in `debtorSideTx` and `creditorSideTx` rather than inside a scheme's own `Validate`, so that it runs for every scheme unconditionally: a scheme whose `Validate` does something other than check funds — a future card scheme placing a hold, say — would otherwise skip it silently, and the failure would be invisible until it settled.

**The check used to be one comparison and is now two, and that is worth stating rather than hiding.** It went in when both accounts were read inside one function, at the one moment both ends of the payment were in view — and the message layer removed that moment. No actor sees both accounts now: the payer's bank reads the payer's account and the payee's bank reads the payee's, each an institution apart, and each compares its own against the scheme's asset. That is strictly *weaker* — two banks could each hold a conforming account in a scheme neither is entitled to — and it is strictly what a real bank can do. What is not weaker is the ledger's own catch, described next; it was always the backstop and it still is.

**The ledger cannot catch this when the debtor leg is posted, which is the interesting part.** A payment is never one posting. The debtor leg is a transaction in the payer's bank's book; the creditor leg is a separate transaction in the payee's bank's book, written later, and by a different institution. Give Alice a euro account and Bob a bitcoin one, and the payer's bank produces exactly one posting:

```
Bank A (debtor leg):
                        Debit  Alice EUR       3000     ← balances in EUR ✓
                        Credit Suspense EUR    3000
```

That is impeccable double-entry. It balances within its asset and passes `validateBalance` without complaint — and nothing in it contains the claim that some posting in another bank's book is its other half. No bank in this system holds both ends at once; that is what makes the ledger's catch late rather than absent.

**It is caught eventually, and that is the argument for `ErrAssetMismatch`, not against it.** After the cut-off has settled, the **payee's own bank** comes to post the creditor leg and builds it from *its own* suspense account in the scheme's asset — `creditor.AccountsFor(scheme.Asset())`, in `PostCreditorLegTx`, which is that bank's act and not the settlement agent's — so what actually gets posted is:

```
Bank B (creditor leg, on the settlement advice):
                        Debit  Suspense EUR    3000     ← EUR
                        Credit Bob BTC         3000     ← BTC — does not balance
```

The euro side and the bitcoin side net to 3000 and −3000 in their own assets, so `validateBalance` refuses it with `ErrUnbalancedAsset`. The ledger does its job. It just does it far too late: the payer was debited hours earlier, the cut-off has settled and the reserves have moved, and the error that comes back names an unbalanced asset rather than the payment that caused it. Someone then has to work backwards from an imbalance to a payment initiated hours earlier. (It used to be worse still. While the creditor leg was posted inside the settlement agent's unit of work, **one mismatched payment failed the entire clearing cycle** and every other member's positions with it. The leg is the payee's bank's own act now, so the failure is confined to the one payment at the one bank — which is the same argument that made the closed-account check affordable.)

`ErrAssetMismatch` turns that late, batch-wide, misattributed failure into an immediate, correctly attributed one — refused by whichever bank owns the offending leg, at the moment that bank first reads the account and before it has written anything.

The general shape of the rule: an invariant is enforceable where the whole of it is visible, and *cheapest* where it is visible earliest. Pushing this one down into the ledger would not make it stricter — it would only move the discovery to the point of maximum damage.

#### Cross-Currency Payments Are Two Operations

A consequence of the above: paying euro *into* a bitcoin account is not a payment this system can perform, and `ErrAssetMismatch` is the honest answer rather than a gap.

In the real world it is not one operation either. Sending euro to a dollar account is a euro payment plus a **foreign-exchange trade**, and somebody — the sender's bank, the recipient's bank, or a correspondent in between — does the trade, quotes a rate, and takes a spread on it. The reason the transfer has a worse rate than the one on the news is that the trade is a separate, priced transaction that has been bundled into the payment's presentation.

Modelling it therefore needs the [FX machinery](#foreign-exchange-and-why-the-ledger-needs-no-rates) — position accounts, a rate source, a spread booked to revenue — none of which exists here. What does exist is the refusal, which is the part that keeps the books correct in the meantime.

### The Payment Lifecycle

Every payment travels through an explicit state machine:

```
Initiated ──▶ Accepted ──▶ Cleared ──▶ Settled
     │            │                        │
     └────┬───────┘                        ▼
          ▼                             Returned
      Rejected
```

Every one of those arrows is drawn by a *named institution*, and no two adjacent ones by the same:

- **Initiated** is where a payment starts and where it can sit for a while — it is an observable state, not a moment. The **submitting bank** has run its own half and sent the instruction, and nobody else has looked at it yet. For a push that half posts the **debtor leg**, so the payer's money is already in their bank's clearing suspense while the payment reads `Initiated`; for a pull it posts nothing, and the debtor leg is posted by the *payer's* bank when the collection reaches it — still leaving the payment `Initiated`. The debtor leg's two sides value-date differently: the customer's side to the debit itself (PSD2 Art. 87(2): no earlier than the debit), the suspense side to the settlement date.
- **Initiated → Accepted** is the **clearing house's** act and nobody else's. It takes a payment both banks have now looked at into the open cycle for its scheme, and only then is it `Accepted`. If no cycle is open the payment is refused — `ErrCycleNotOpen`, which on the wire is a `pacs.002` carrying **`TM01`, "invalid cut-off time"**. That refusal belongs to the clearing house because the cut-off does: a bank that turned its own customer's instruction away because the CSM had no window open would be answering a question it was never asked. It is not a `4xx` on the customer's request, because by the time it is decided that request has long been answered.
- **Accepted → Cleared** is the clearing house again: the cycle reaches its cut-off and net positions are computed. Nothing is posted.
- **Cleared → Settled** takes **three institutions, of which two post — in two units of work**. The central bank moves the reserves, because the clearing house *asked* — closing a cycle sends a `pacs.009` — and a net payer who cannot cover is answered `RJCT`/`AM04`, after which nothing is posted at all: the cycle stays `Closed` with no settlement against it and every payment stays exactly where the cut-off left it. That settlement is **final** either way, and it is not what marks a payment `Settled`. The clearing house then tells the **payee's bank**, per payment, and *that* bank posts the **creditor leg** out of its own clearing suspense into its own customer's account — which is the write that transitions the payment. So a payment is `Cleared` with the reserves already moved for as long as its bank has not acted, and that interval is the **unreconciled position** the message layer makes visible rather than hides.
- **Rejected** is reachable from `Initiated` *and* from `Accepted`, and it is **two halves in two units of work**. The clearing house marks the payment `Rejected` and drops it from any cycle; the *payer's bank* then reverses the debtor leg, on receiving the `pacs.002`. Between the two the rejection has **half-happened**: the payment reads `Rejected` and the customer's money is still in suspense. That is the first operation in this system that can be caught in the middle, and it is deliberately not hidden — a reversal that fails has nobody to answer, so it surfaces as a mesh dead letter rather than as a quiet lie to the payer. Closing it for real needs a way to carry an unreconciled position, which this system does not have.
  A rejected **collection** can be told to *two* banks, and only a collection ever is: the bank waiting for the answer is the payee's, and the bank holding the money is the payer's. "One decision, one message" is not a property of this system, because those can be two different institutions. The condition is exactly the money — the payer's bank gets its own `pacs.002` only when it has already **posted the debtor leg**. The commonest refusal of all does not meet it: a payer's bank with no funds refuses the collection *itself*, with `AM04`, before posting anything, so there is nothing to give back and the rejection is one message again.
- **Returned**: after settlement, a SEPA R-transaction unwinds the flow, and it is **three acts at three institutions** joined by messages. It is sent by the bank that *received* the original instruction — the payee's bank on a push, the payer's on a pull — and that bank posts the leg it owns **before** it sends, which is what leaves it able to refuse. The **reserve** half is the central bank's, because unwinding a settled payment moves reserves back and no member bank moves those, and it is final the moment it commits. The other bank posts **after** finality, on the `pacs.004` the clearing house relays to it once the return has settled, and cannot refuse — there is nothing left to refuse. The status becomes `Returned` when the second of the two customer legs lands. See [Posting Choreography: A Return](#posting-choreography-a-return).

### Posting Choreography: SEPA Credit Transfer

Alice (at Bank A) pays Bob (at Bank B) €30.00 (`3000` cents).

**1. Initiation** — in Bank A's ledger:

```
Bank A:  Debit  Alice (Liability)            3000     // Alice's deposit falls, value-dated to the debit itself
         Credit Clearing Suspense (Liability) 3000    // Bank A now owes the network, value-dated to settlement
```

The two entries take effect on different days. PSD2 Art. 87(2) puts the payer's debit value date no earlier than the moment the money leaves — value-dating it to settlement instead would hand Alice the settlement delay's worth of interest-free credit. The clearing-suspense leg carries the settlement date because that is when Bank A's position against the scheme actually settles.

This is why an entry carries a value date of its own, and not only its transaction: `ledger.Entry.ValueDate` is genuinely not derivable from the parent, unlike an entry's asset (which is always its account's, and so would only create disagreement if stored twice). `PostTransaction` resolves a zero one to the transaction's, so every stored leg carries a concrete date and no reader has to fall back.

The transaction-level date here is the **settlement** one, which makes the pair worth reading carefully: it is the payment's own value date, and it is the *wrong* answer for the leg a customer cares about. Reporting only it — which the transaction API used to do — told a client that Alice's account took effect at settlement when it took effect on the day she was debited. `entryDTO` therefore carries `valueDate` per leg in both directions, and it is the only field on the wire that can express a posting whose legs deliberately disagree.

**2. Clearing (cut-off)** — net positions computed; no money moves. Here `net[A] = -3000`, `net[B] = +3000`.

**3. Settlement** — **four** ledger transactions in **four** units of work, belonging to **three** institutions: the central bank posts one, each bank posts its own mirror leg, and the payee's bank posts the creditor leg on top. Only the first is settlement; the rest are the members catching up. The order below is the order the messages arrive in, and for Bank B that ordering is load-bearing rather than presentational — the `camt.053` reaches it before the `pacs.002`, which is what puts money in the suspense the creditor leg then draws on.

```
Central Bank:  Debit  Reserve: Bank A (Liability) 3000   // A's reserves fall
               Credit Reserve: Bank B (Liability) 3000   // B's reserves rise
                                                         // ← FINAL here

Bank A, on its camt.053 (net payer):
               Debit  Clearing Suspense 3000             // suspense clears to zero
               Credit Reserve at CB     3000             // A's reserve asset falls with it

Bank B, on its camt.053 (net receiver):
               Debit  Reserve at CB     3000             // B's reserve asset rises
               Credit Clearing Suspense 3000             // and its suspense rises with it

Bank B, on the pacs.002/ACSC for this payment:
               Debit  Clearing Suspense 3000             // creditor leg: release...
               Credit Bob (Liability)   3000             // ...funds to Bob
```

Note which way each suspense moves, because it is easy to get backwards: clearing suspense is a *liability*, so the net payer's `Debit` **lowers** it and the net receiver's `Credit` **raises** it. Both move in the same direction as that bank's reserve. The receiver's rising first is precisely the point — reverse the two messages and Bank B pays Bob out of a suspense the cut-off has not yet credited, which commits (the ledger does not guard liabilities against going negative) and which for that interval has B's own books saying it lent Bob the money.

(For this single inbound payment Bank B's two Clearing Suspense legs net to zero — B never posted a debtor leg here. They are shown because across a full netted cycle Bank B's *own* outgoing payments would have credited its suspense, so clearing it at the cut-off is what actually happens; in isolation it is just bookkeeping symmetry.)

Once **all four** have posted, both banks' suspense accounts are back to zero and each bank's **Reserve at Central Bank** asset equals the central bank's **Reserve: \<Bank\>** liability — the books reconcile. Between the first transaction and the last they do not, and that interval is real; see below.

### Settlement Is Final at the Central Bank, and the Banks Catch Up

The postings above belong to three institutions — the central bank, Bank A and Bank B — and they do not happen at the same moment, nor in the same unit of work. (Counted per *payment* rather than per cut-off, as [the lifecycle](#the-payment-lifecycle) does, the `Cleared → Settled` arrow involves three institutions of which two post: the clearing house asks and posts nothing, the central bank settles, and the payee's bank posts the creditor leg that marks the payment `Settled`. Bank A's mirror leg above is the third institution's own unit of work at the same cut-off.) The central bank's posting is one unit of work and it is **final**: once it commits, the money has moved between the banks, and nothing a member does next unwinds it — including failing to book its own half. The members are *told*, and book afterwards.

That is not a modelling convenience. In the EU it is the **Settlement Finality Directive** (98/26/EC), whose subject is exactly this moment — when a transfer order becomes irrevocable, so that one participant's insolvency cannot reach back into a batch that has already settled. A system that unwound a settled cycle because one member's local posting failed would be modelling something no RTGS is permitted to do.

The central bank's answer is final either way. A net payer that cannot cover its position is refused **before anything is posted** — `RJCT`/`AM04` — and the cycle stays `Closed` with no settlement against it and every payment exactly where the cut-off left it.

**The interval between the central bank's commit and a member's booking is the unreconciled position**, and where it shows is the bank's **clearing suspense**: settlement has happened and the suspense has not returned to zero.

What records the other side of it is the member's own `SettlementAdvice` row, and it is worth being exact about what that row does and does not say, because this document said the wrong thing about it first. `PostSettlementAdviceTx` writes the row and posts the mirror leg in **one unit of work**, so the two commit together or neither does. The row therefore means *this bank booked this cut-off* — which is what makes a redelivered statement a no-op, and what a reconciliation reads. It is not a trace of a failure: a posting that fails takes the row back with it and leaves nothing. So the unreconciled position is the **absence** of a row against a suspense that has not cleared, and a bank that was told and could not book looks exactly like one that was never told at all.

That is the right design rather than a limitation. Booking the leg and recording that you booked it must be atomic, or a bank can post and fail to record; a row asserting a booking that never happened is worse than no row. (`AdviceAdvised` — "told, not yet booked" — exists in the type and no committed row says it today. It becomes meaningful only if the write and the posting ever stop sharing a unit of work.) Telling the two absences apart is what the stored closing balance is for, and nothing reads it yet: that is Task 19.

**A return is the same shape, one payment wide.** None of the paragraphs above is about cut-offs in particular; they are about an institution being final and the members catching up, and a [return](#sepa-direct-debit-and-returns) does exactly that. `SettleReturnTx` reverses the reserves in the central bank's own book and is final there; it states both members' accounts in a `camt.053` apiece; each bank books its own reserve mirror afterwards, in its own unit of work, and writes its own `SettlementAdvice` row for it. So a return has an unreconciled position of its own, and it shows in the same place — a clearing suspense that has not returned to zero with no advice row against the reference. What differs is only what the reference *is*: a cycle id at a cut-off, a payment id here. A member cannot tell one from the other and has no reason to; both name a row in an institution it does not share a database with. What a return does **not** leave behind at the settlement agent is a row: a cut-off writes a `Settlement`, and a return writes nothing at all, because the only durable trace it needs is the idempotency key on the reserve reversal, which is what makes a redelivered `pacs.004` `ErrReturnAlreadySettled` instead of a second payout.

#### Each Institution Knows Only What Its Own Job Needs

The **clearing house has no ledger**. `CloseCycleTx` posts nowhere: it marks each payment `Cleared`, writes the net positions onto the cycle, and then *instructs*, because discharging those positions moves central-bank money and no clearing house may do that. There is no clearing-house book of accounts in this system — the cycle and payment rows it writes are network-scoped rows, not postings.

The **central bank has no customers**. Its book holds one `Reserve: <Bank> (<asset>)` liability per member per asset and the balancing `Settlement Assets (<asset>)` — no deposit accounts and no payees. It answers about a *cycle* and has no way to look an individual payment up, which is why the per-payment `ACSC` fan-out is the clearing house's job and not its.

A **bank has no cycles**. A member never *reads* the cycle row — `bankOps`, the narrowed interface a bank's handler holds, has no method that returns one. (It handles an opaque *reference*: `PostSettlementAdvice` takes an `AdvisedMovement` carrying the `Reference` the statement quoted — a cycle id today, and, once a return can settle on its own, a payment id, which a member cannot tell apart from a cycle id and has no reason to.) A member learns of a cut-off from the `camt.053` addressed to it and from one `pacs.002` per payment, and its own record of that cut-off is the `settlement_advices` row keyed by `(book_id, reference, asset)` — the first payment-layer table **keyed by** book. There is deliberately no foreign key from it to anything: the reference names a row in an institution the member does not share a database with, in either direction, and a constraint here would encode exactly the sharing that splitting the stores removes.

#### A Bank Reconciles Two Advices from Two Institutions Against One Balance

Two senders tell a bank about the same cut-off, and they tell it different things:

- the **central bank** says what its reserve moved by — a `camt.053` statement of that bank's reserve account, which is what the mirror leg is booked from;
- the **clearing house** says which payments settled — one `pacs.002`/`ACSC` per payment, which is what each creditor leg is booked from.

```
Bank B, a net receiver, over one cut-off:
  camt.053  (central bank)     Debit  Reserve at CB    → Credit Clearing Suspense
  pacs.002  (clearing house)   Credit Bob's deposit    → Debit  Clearing Suspense
  ─────────────────────────────────────────────────────────────
  Clearing suspense back to zero  ✓  only if the two agree
```

Its clearing suspense returns to zero **only if the two agree**, and that is what makes the split load-bearing rather than incidental. Two senders make a check possible; if both advices came from one institution the bank would be checking a sender against itself and there would be nothing to reconcile. It is the classic nostro/vostro check — the bank's reserve asset against the central bank's reserve liability — with the payment list as a second, independent witness.

#### Unclaimed Balances

Money that arrives for an account that cannot receive it goes to the receiving bank's **Unclaimed Balances (\<asset\>)** account. It is a *liability*, because the bank still owes the money — to whoever eventually claims it — exactly as it owes a deposit. Every bank gets one per asset it operates in, created in its own book when it is [founded](#admission-a-bank-exists-before-it-joins-a-scheme).

The case it exists for is a payee who closes their account between their bank's acceptance of the payment and the cut-off. `Closed` is the one status that refuses a credit, for the reason given under [account states](#what-this-implementation-actually-enforces): a credit landing on a closed account leaves it holding money no withdrawal can reach and no second close can clear.

```
Bank B posts its creditor leg; Bob's account is Closed:
  Debit  Clearing Suspense (Liability)   3000
  Credit Unclaimed Balances (Liability)  3000    ← not Bob's closed account
```

The payment still reaches `Settled`, because it did: the reserves moved and Bob's bank has been paid. Whether **Bob** has been paid is a different question, and it is between the bank and Bob.

**Which account it landed in is recorded on the payment.** `Payment.CreditorLegAccount` (`payments.creditor_leg_account`) holds the account the creditor leg actually credited — Bob's GL account normally, Unclaimed Balances here — and it is written at the moment the leg posts. This paragraph used to say the opposite, that the destination was "not a fact about the payment," and a return proved otherwise: `ReturnPaymentTx` debited Bob's GL account for money that had never been credited to it, leaving his **closed** account at minus 3000 with the Unclaimed liability still standing. It cannot be worked out later — an account open at settlement and closed afterwards looks exactly like one closed at settlement — so it is stored rather than re-derived. See [Next Work](#next-work).

**Having somewhere for it to go is what made the check affordable.** `CheckCreditTx` existed all along and settlement did not call it: while the creditor leg was posted inside the settlement agent's one unit of work, refusing a credit took the whole cut-off down for one retail customer, and a `Cleared` payment has no route out of the cycle it is in. Refusing was worse than stranding, so it stranded and the ruling was recorded rather than fixed. Two things changed it — the account, and the split that lets one payment at one bank fail on its own.

**The same account catches the same case on the return path**, for the same two reasons. `PostReturnLegTx` asks `CheckCreditTx` before it credits the payer, and a payer who has closed their account since the payment settled is refunded into their own bank's Unclaimed Balances instead. It became affordable at the moment the return stopped being one unit of work over three institutions, because that is what gave the payer's bank an act of its own to decide in. The diversion happens on a **closed account and on nothing else**: a store failure is not a statement about the account, and money must not be routed on a failure nobody can classify. The mirror case — money the bank has to take *back* and cannot — is [Returns Receivable](#returns-receivable), and it is an asset rather than a liability for the reason given there.

### Netting: A Worked Example

Netting is the whole point of clearing. Suppose in one cycle:

- Alice (Bank A) → Bob (Bank B): `30000`
- Bob (Bank B) → Alice (Bank A): `10000`

The **customers** move by the gross amounts (Alice −30000 +10000, Bob −10000 +30000), but the **banks settle only the net**:

```
net[A] = -30000 + 10000 = -20000   // Bank A pays 20000 net
net[B] = +30000 - 10000 = +20000   // Bank B receives 20000 net
```

Only `20000` of central-bank reserves moves, not `40000`. Net positions always sum to zero, which is exactly why the central-bank settlement transaction balances.

### SEPA Direct Debit and Returns

A direct debit is a **pull**: the creditor (e.g. a utility) collects from the debtor. Before any collection, the debtor signs a **mandate** authorising that specific creditor. Mechanically the postings are identical to a credit transfer (debtor → creditor), because the money flows the same way.

**The collection is submitted by the *creditor's* bank**, and that one sentence is what a push and a pull actually differ by. A collection is the payee asking for what it is owed, so the payee's bank is the one that puts the `pacs.003` on the wire — and its submission **moves nothing at all**. The account being collected from belongs to another bank's customer, and this bank has never seen it. What it checks is its own half: the payee's account, and the **mandate**, because in SEPA the mandate is a document the *creditor* holds. The package checks it exists, is active, matches both parties, and stays within its amount limit — otherwise the collection is refused right there, at the payee's own bank, as an error to the caller (`ErrMandateRequired`, `ErrMandateRevoked`, `ErrMandateExceeded`, …) rather than as a message to anybody.

**The debtor's bank posts the debtor leg**, on receiving the `pacs.003`. That is the first moment any actor in this system has been able to look at the account being collected from, which is why a collection refused for want of funds can only ever be refused there — and why the payer's money moves *after* the collection was submitted rather than at the moment of it. The rule that covers both schemes, and that neither one on its own would tell you, is that **the debtor's bank posts the debtor leg**: which bank that is never changes, and the direction only decides whether it is the one submitting or the one answering.

A settled payment can be **returned** (a SEPA R-transaction), and *both* schemes permit it — `AllowsReturn()` is `true` on each, and `PostReturnLegTx` asks only that and whether the payment is `Settled` — plus one thing about the *money*, which direction decides. The bank that sends the return posts its own leg **before** it sends, so it can still refuse: on a credit transfer that is the payee's bank holding the clawback, and a payee who has spent the money stops the return dead, with no `pacs.004` ever composed. The bank that posts **after** finality cannot refuse, because there is nothing left to refuse — on a collection that is the creditor's bank, which takes the money back off its biller whether or not the biller can afford it, and funds the shortfall out of its own [returns receivable](#returns-receivable) if the biller's account has closed. A collection comes back when the debtor disputes it or the funds are not there; a credit transfer comes back when the beneficiary's bank cannot apply it — a closed account, a name that does not match. What this system does **not** model is the debtor's SEPA **refund** right, the 8-week no-questions-asked claim on a settled collection: there is no return window here at all, and a payer who merely changes their mind about a credit transfer would in SEPA need a *recall* (`camt.056`), which the beneficiary may refuse and which this system does not implement. A return posts compensating transactions that move the funds back from the creditor to the debtor across the central bank, unwinding the original flow — but not in one act and not at one institution: the returning bank posts its own leg and sends a `pacs.004`, the settlement agent reverses the reserves and states both accounts in a `camt.053`, and the clearing house relays the `pacs.004` to the other bank once the return is final so that bank can post the leg it owns. Both customers' balances are restored where both accounts can still take the postings; where one cannot — a payer who has closed their account since, a biller who has closed theirs — the money lands in that bank's [unclaimed balances](#unclaimed-balances) or comes out of its [returns receivable](#returns-receivable) rather than stranding in an account nothing can reach. The return is *sent* by the bank that received the original instruction — which for a collection is the payer's bank, and for a credit transfer is the payee's — and the *reserve* movement is the central bank's, because a settled payment coming back moves reserves and no member bank moves those. An `RJCT` from the settlement agent makes the returning bank reverse the leg it had already posted — and the return can then be **asked again**, which matters because the commonest `RJCT` here is `AM04`: the other bank was short of reserves at that moment, and a shortfall somebody can cover is not a payer who has lost their refund right. The reversed leg's transaction id stays on the payment rather than being cleared, because it is what the retry's idempotency key is derived from; what it therefore stops meaning is "this bank's leg stands". Whether the leg still stands is in the ledger, on the transaction, and that is what `PostReturnLegTx` reads before it decides it has nothing to post. Reading the id alone was a return that ran its whole conversation — reserves reversed, the other bank's customer clawed back, `ACSC` on the wire — around a refund that no longer existed.

#### Posting Choreography: A Return

Two customer legs and one reserve reversal, in three books, and **which bank owns each leg never changes**. The **clawback** is always at the **creditor's** bank, out of the account its creditor leg actually credited (`Payment.CreditorLegAccount`). The **refund** is always at the **debtor's** bank, into the payer's account. Direction decides one thing and only one: which of the two the *returning* bank is holding — and therefore which bank posts before it sends and which posts after finality.

| | the returning bank posts this, **before** it sends | the other bank posts this, **after** finality |
|---|---|---|
| **push** (SCT) — the payee's bank returns | the **clawback**, and it is **refusable**: a payee who has spent the money stops the return dead and no `pacs.004` is ever composed | the **refund**, which is always postable — the payer's bank is paying money out, and Unclaimed Balances catches the payer who has closed their account |
| **pull** (SDD) — the payer's bank returns | the **refund**, which is always postable | the **clawback**, and it is **forced**: [Returns Receivable](#returns-receivable) catches the biller who has closed theirs |

The rule that generates the whole table is one sentence: **a bank can refuse a leg only if it posts it before it sends.** The returning bank's refusal costs nothing — nothing is posted, no message is built, and the caller is told, which is why it is a Go error and never a `pacs.002`. The other bank has nothing left to refuse by the time it hears: the reserves have already moved. So `Returns Receivable` is needed on the pull side and nowhere else, and that is not a stipulation about direct debits — it falls out of *which* bank hears about the return only after it is already final.

The order the acts run in, and what each institution puts on the wire:

```
1. the returning bank posts its own leg                 ← the last moment anyone can say no
   payee's bank on a push:  Debit  Bob (or Unclaimed)   → Credit Clearing Suspense
   payer's bank on a pull:  Debit  Clearing Suspense    → Credit Alice
2. --pacs.004-->  clearing house  --pacs.004-->  central bank      (OrgnlTxRef names both agents)
3. the central bank reverses the reserves                          ← FINAL here
   Debit  Reserve: creditor's bank   → Credit Reserve: debtor's bank
4. --camt.053-->  BOTH banks        ← sent BEFORE the answer, and that matters
5. --pacs.002-->  clearing house  --pacs.002-->  the returning bank
6. the clearing house releases the pacs.004 it held  -->  the other bank

   each bank then takes its own inbox in order: it books its reserve mirror
   from (4), and the other bank goes on to post its customer leg from (6) —
   the second leg, and what takes the payment to Returned
```

Three things in that sequence are load-bearing. The **statements go out before the answer**, because step 6 is provoked by the answer and the relayed `pacs.004` is what makes the other bank post the one customer leg still outstanding. On a **push** that leg is the **refund**, and it draws on the very clearing suspense the statement in step 4 credits — get the order wrong and a bank pays its customer out of a suspense the return has not yet funded. On a **pull** it is the **clawback**, which *credits* that bank's suspense while its own statement debits it, so nothing there is drawn on and the order costs that direction nothing. One order goes out in both directions, and it is the one the push needs. The **clearing house holds the `pacs.004`** between steps 2 and 6 rather than relaying it straight through, because a bank that posted its leg against a return the settlement agent then refused would have moved a customer's money for nothing; on an `RJCT` the held message is dropped and only the answer goes out. That hold is the only state any actor in the mesh keeps between messages, it is in memory, and a restart loses it — recorded rather than fixed, because a durable version of it belongs in a clearing-house store that does not exist yet. And the settlement agent **reads the parties off the message**, out of `OrgnlTxRef`, not out of a payment row: it never saw the payment clear and holds none.

#### Returns Receivable

**Returns Receivable (\<asset\>)** is the mirror of [Unclaimed Balances](#unclaimed-balances), and its class is the whole point: it is an **asset**. Unclaimed Balances is money the bank *owes* to somebody it cannot identify. This is money owed *to* the bank by somebody it has identified perfectly well — a biller whose account could not fund a clawback the bank had no choice but to honour. Same kind of event, a credit reversed after the bank has already paid out, landing on opposite sides of the balance sheet according to whether the bank knows who owes whom. Every bank gets one per asset, created in its own book when it is [founded](#admission-a-bank-exists-before-it-joins-a-scheme).

It is reached in exactly one case: the clawback is **forced** (so the bank is on the pull side, hearing about the return only after finality) *and* the biller's account is **closed**. A biller who has spent the money but still has an account simply goes overdrawn — the ledger does not refuse a `Liability` going negative, and an overdrawn biller is a debt the bank collects from a customer it still has. A closed account is the one case with nowhere on the account to put the debit, because a posting into a closed account strands.

```
Bank B forces the clawback; the biller's account is Closed:
  Debit  Returns Receivable (Asset)      3000    ← the bank funds the refund itself
  Credit Clearing Suspense (Liability)   3000    ← and it goes back across the network anyway
```

**This is the credit risk a creditor's bank takes on when it onboards a biller**, and it is why real creditor banks vet their creditors, demand collateral or an indemnity, and price the relationship accordingly. The debtor's eight-week SEPA refund right is *unconditional*: the payer's bank does not ask the biller whether it can afford it, and the network does not either. The creditor's bank stands behind its customer's collections whether or not that customer is still solvent, still trading, or still a customer. Nothing in this system spreads that risk — there is no collateral, no indemnity and no limit — so `Returns Receivable` is where it accumulates, visible as an asset the bank has to go and collect.

### Deliberate Simplifications

This is a learning model, not a production processor. The simplifications are intentional:

- **Six ISO 20022 messages, and what is missing is named rather than implied.** The interbank messages are real: `pacs.008` (credit transfer), `pacs.003` (collection), `pacs.002` (status), `pacs.004` (return), `pacs.009` (the settlement instruction) and `camt.053` (the statement of a member's reserve account, which is what it books its mirror leg from), each wrapped in a `head.001` business application header, marshalled to XML and *parsed on arrival* — which is what makes `FF01`, the rejection a receiver sends when it cannot read what it was given, a reachable failure mode rather than a decoration. Nothing but bytes crosses between the institutions. What is absent: **`pain.001`/`pain.008`** customer initiation (a customer's instruction arrives over this system's own REST API instead), the rest of the **`camt`** reporting family — including the `camt.054` a bank would learn of an individual credit from, and the `admi.002` a real receiver would answer an unreadable file with — **`camt.056`/`pacs.007`** recalls and reversals, **runtime XSD validation**, and **message signing**. A golden-file schema check does exist — `TestGoldenFilesValidateAgainstTheSchema`, run by `make test-schemas` — but **it cannot be run as the tree stands**: it needs `xmllint` and the XSDs under `iso20022/testdata/xsd`, and that directory is deliberately not committed (the schemas are ISO's to redistribute, not this repository's to vendor — see `iso20022/testdata/README.md`). So a plain `go test` skips every one of its subtests, and `make test-schemas`, which sets `ISO20022_REQUIRE_SCHEMAS=1` precisely to turn each skip into a failure, **fails**. Until someone fetches the schemas, nothing here is checked against a real one. There is also no **batching** of customer payments: a `pacs.008` or `pacs.003` this system builds carries exactly one transaction, and one arriving with several is refused — which is why `pacs.002`'s `PART` group status, built by `groupStatusOf` in `payment/translate.go`, is never produced. The settlement `pacs.009` is not an exception to that so much as a different thing: it carries one leg per member with a **non-zero** position, because netting produces exactly one position per member and a member that nets to zero has nothing to discharge.
  Two elisions inside the messages are worth naming, because both are places where a real element has no source in this system. **`CdtrSchmeId`** — the Creditor Identifier a `pacs.003` must carry — is filled with the creditor's IBAN, because this system has no Creditor Identifier at all; that is a larger elision than `MndtRltdInf/DtOfSgntr`, which at least maps a real date (the mandate's `CreatedAt`) rather than substituting a different kind of thing. And the `FF01` status this system sends for an unreadable message carries **`NOTPROVIDED` three times** — the original message id, its definition identifier and the end-to-end reference — because a message nobody could parse has no readable references, and the `admi.002` a real network answers that with, which refers back by nothing, does not exist here.
- **No identifier format validation.** An IBAN's mod-97 check digit is not verified and neither is its length or country code; a BIC's structure is checked but its existence is not. Addresses resolve by lookup, not by parsing. The seed's readable `SE89-AURORA-1001` is the reason: a real check digit would put opaque digits in every worked example in this document. The lookup is *literal* with exactly one exception, and the exception is forced by the readable form: an IBAN is compared with its display separators removed from both sides (`deposit.Identifier.MatchValue`), because the canonical IBAN on a payment message carries none and `SE89-AURORA-1001` and `SE89AURORA1001` are one address. No other scheme has a display form, and none of them is normalised.
- **Many assets, but no exchange between them.** Accounts are denominated in one of the [known assets](#assets-what-an-account-is-denominated-in) — EUR, USD, BTC — and transactions [balance per asset](#the-invariant-is-per-asset), so the multi-asset accounting is real rather than a currency label on a single-currency system. What is missing is everything to do with *converting* one asset into another: there are no exchange rates anywhere in the system, no FX trade operation, no position accounts, and consequently no [cross-currency payment](#cross-currency-payments-are-two-operations) — a payment whose two ends differ in asset is refused with `ErrAssetMismatch` rather than converted, because converting it would require a rate, and a rate is a *price*: something a ledger records the consequences of and never decides. Two further limits are worth stating plainly: asset scale is [capped at 9 decimal places](#why-scale-is-capped-at-nine), so an 18-decimal asset such as ether cannot be held at its native precision; and the set of assets is a list in Go, so adding one is a deploy rather than an API call.
- **The settlement window is one database transaction — the central bank's, and only the central bank's.** `SettleCycle` moves the netted reserves inside one `Store.Update`: if any net payer cannot cover its position, nothing is posted at all. That is what a real RTGS calls a **settlement window** — an interval in which the settlement agent holds the participants' reserve accounts, checks that every payer can cover, and posts the whole batch or none of it. A real system also has to decide what to do next (queue the batch, run a liquidity-saving optimisation, unwind the defaulter, tap intraday credit); here the batch simply fails and can be retried once the member is funded. What is no longer a simplification is who asks for it: the window is **instructed**, by a `pacs.009` the clearing house sends at the cut-off, and the central bank can **refuse** it — `RJCT`/`AM04`, the same code a debtor's bank sends about a customer's empty account, said about a bank instead.

  **What the window no longer spans is the members**, and that is the simplification that has actually been removed rather than restated. Both legs in a member's own book used to be inside it. The **mirror leg** — a bank's suspense moving against its own reserve — is the member's own posting now: the settlement agent returns one statement per member, sends it as a `camt.053`, and the member books the leg from what it was told. The **creditor leg** — releasing a payee's money out of that payee's bank's suspense — is that bank's own, posted when the clearing house advises it per payment that the cycle settled. The refusal moved with the mirror leg: a member's settlement account at the central bank is a *liability*, which the ledger does not guard against going negative, so `SettleCycleTx` checks each net payer's reserve itself and answers the same `AM04`. What is left is that the interval between the central bank's commit and a member's is real and observable — it is the **unreconciled position**, which is what the `SettlementAdvice` row exists to record.
- **Returns settle immediately** rather than being batched into a later R-cycle, and no return window is enforced at all — `PostReturnLegTx` asks whether the scheme allows returns and whether the payment is `Settled`, and nothing asks how long ago it settled. What is *not* a simplification any more is the shape: a return is three institutions' acts joined by messages, exactly as a cut-off is, and the settlement agent's unit of work holds the reserve reversal and nothing in any member's book. See [A Return](#posting-choreography-a-return).

### Next Work

Two schemes are designed for but not yet implemented. They are the reason the `Scheme` interface carries a `SettlementModel` (Net/Gross) and the reason authorise/capture now lives in the `deposit` layer — the abstraction is in place; what remains is the wiring noted below.

- **Instant payments** (SEPA Inst, FedNow) — real-time **gross** settlement, 24/7. Each payment settles individually and immediately instead of being batched into a clearing cycle. (*Instant* and *gross* are independent properties: UK Faster Payments feels instant to customers but actually settles on a deferred **net** basis, so it is not a gross-settlement example.) This needs:
  - a `Scheme` returning `SettlementModel() == Gross` with a near-zero `SettlementDelay`; and
  - a settlement path that branches on `SettlementModel()` — for `Gross`, post the debtor leg, the central-bank reserve move, and the creditor leg in one shot per payment, with no netting and no cut-off.

  This is the one place the orchestrator genuinely has to grow: today `SettleCycle` only implements the netted path, so a `Gross` scheme would need a `SettleNow`-style sibling (or a branch inside settlement).

- **Card transactions** — an **authorise → capture → clear → settle** flow. The authorisation is a `deposit` **hold** (`CreateHold`) that reserves the cardholder's available balance; capture (`CaptureHold`) turns it into the debtor leg; clearing and settlement then reuse the existing net machinery, since card networks net much like SEPA. This slots in cleanly now that holds live in the `deposit` layer: a card scheme drives `deposit` holds while the `payment` network clears and settles the captured amounts.

Both motivate a natural follow-on: **reserve-adequacy** checks before a bank's net settlement is allowed to post.

A third gap is of a different kind, because it is a payment this system currently cannot make at all rather than a scheme it has not wired up:

- **The on-us book transfer** — a payment between two customers of the *same* bank. [Payment Schemes](#payment-schemes) explains why `Mesh.Submit` refuses one: nothing crosses between banks, so there is no obligation to net, no reserves to move and no statement to send, and a payment that never leaves one bank has no business in a clearing system. What is missing is the other half of that sentence — what a real bank does *instead*, which is to recognise the beneficiary as its own and post both customer legs in **its own book, in one unit of work**, with no message, no suspense and no counterparty. It is the only payment in this document where a single institution legitimately holds both legs at once, which makes it the exception that shows why every other payment needs a conversation: there is no second institution to disagree with, so there is nothing to reconcile and nothing to strand. Refusing it was the honest answer to an on-us payment that had been reaching the clearing house and diverging its reserve mirror; giving it somewhere to go is the work that remains.

Separately from the scheme wiring above, two more entries are worth listing, and neither is a gap any longer — one closed and one became a deliberate trade. They are kept because a reader who remembers them as gaps needs to be told, and because what closed them is the interesting part:

- **Money that cannot reach a customer goes to that bank's unclaimed balances, on both paths.** This entry used to read "a refund into a closed payer's account still strands", and it no longer does — it is kept here, under a lead that says so, because the two halves closed one task apart and the second is recent. The settlement half: `PostCreditorLegTx` — the payee's bank's own act, extracted out of `SettleCycleTx` — calls `CheckCreditTx` before it releases a settled payment out of suspense, and a payee whose account was **closed** between their bank's answer and the cut-off is credited to that bank's **Unclaimed Balances (\<asset\>)** instead. The payment still reaches `Settled`, because it did: the reserves moved and the payee's bank has been paid. Whether the **customer** has been paid is left open, and it is between the bank and its customer. What made the check affordable was not the check — it was having somewhere for the money to go, plus the split that lets one payment at one bank fail without taking the cut-off down with it. (A *frozen* payee being credited is still deliberate, not a gap: the freeze here is a debit block.) The mirror case on the **return** path is closed too, and this entry used to say it was not: `PostReturnLegTx` checks before it credits the payer, and a payer who has closed their account since the payment settled is credited to their bank's **Unclaimed Balances (\<asset\>)** instead. What made that affordable was the same thing — the return is no longer one unit of work over three institutions, so the payer's bank has an act of its own to decide in.

  This paragraph used to end by calling the diversion "not a fact about the payment," and that was the shape of a second, worse bug on the same path. Once a settled payment's money could land somewhere other than the payee, `ReturnPaymentTx`'s claw back — which debited the payee's GL account unconditionally — was debiting an account that had never been credited. Measured: the payee's **closed** account at minus the amount, the Unclaimed Balances liability never released, and the creditor bank's reserves paid back out all the same. Two liabilities netting to zero, so the book balanced and no ledger guard fired; a deposit going negative is a `Liability` going negative, which `checkSufficientBalance` does not refuse. It is closed by recording the account the creditor leg actually credited **on the payment** (`Payment.CreditorLegAccount`, `payments.creditor_leg_account`) and clawing back from there. It cannot be re-derived at return time: an account open at settlement and closed afterwards is indistinguishable from one closed at settlement, so re-checking the status would claw back from the wrong account in exactly the case the field exists for. Note what the return of a diverted payment does and does not do — it does not make the payee whole, because a payee whose account was closed at the cut-off never held this money; it gives the money back to the payer, out of the account actually holding it, and releases the bank's Unclaimed liability in doing so.
- **The recompute window is unbounded, now by choice.** It opens at account opening for a deposit overdraft and at origination for a lending facility, and nothing resets it — so a long-lived account or facility walks more days of arithmetic every night. That bound used to be an accident: it happened to fall at the last repricing, because mutable terms forced it there. Now it is a deliberate trade — a window at inception is what makes a backdated posting correct across a repricing — and its successor is named: **checkpointing**, anchored to a ledger sequence position rather than a wall clock, and invalidated by a backdated posting. The end-of-day snapshots this system already writes are the raw material a checkpoint would read from.

## Reporting and Compliance

### End-of-Day Snapshots

> Snapshots are captured by the **`deposit`** layer (`deposit.Register.TakeEndOfDaySnapshot` / `GetSnapshot`), since they record the three-part deposit balance (book, holds, available). The pure `ledger.Book` computes book balances on demand and does not store snapshots.

At the end of each business day, the system captures a snapshot of each account's balance. In the model this document describes, these snapshots would serve multiple purposes:

- **Interest accrual:** daily interest is calculated on the end-of-day balance. For a savings account earning 4% APR, the daily interest on a $10,000 balance is: $10,000 * 0.04 / 365 = $1.10.

- **Statement generation:** monthly statements show the balance at the end of each day, transaction activity, and opening/closing balances.

- **Regulatory reporting:** banks must report their positions to regulators. End-of-day balances are the standard reporting granularity.

- **Performance optimization:** instead of replaying all transactions from account creation, balance queries could start from the most recent snapshot and only replay subsequent transactions.

None of this is implemented yet. Interest accrual reads `Tx.ValueDatedSeries` fresh from the entry list on every run — its opening figure is exactly `ValueDateBalance` at the window's start — rather than reading a stored snapshot, and no balance query of any kind consults one either — see [A Balance Is an Aggregate, Not a Column](#a-balance-is-an-aggregate-not-a-column). What is captured today is the raw material a checkpoint would read from.

### Audit Trail

The audit trail is an immutable, append-only log of every mutation in the system. Nothing is ever deleted from the audit trail. Every event is recorded with:

- A monotonic sequence number, assigned by the store
- A unique event ID
- A timestamp
- The scope — which layer produced the event
- The book it belongs to
- The event type
- The entity affected
- The full event payload, as it was at the time

All four layers write to the same log, told apart by **scope**:

| Scope | Book | Events |
| --- | --- | --- |
| `ledger` | one bank's, or the central bank's | ledger, subledger and account creation; transaction posting; reversal |
| `deposit` | one bank's | account opened, frozen, unfrozen, closed, dormant, reactivated; hold created, released, captured; end-of-day snapshot |
| `payment` | the network's | the four acts of an admission — participant added, settlement account opened, member admitted, membership recorded; mandate created, revoked; payment initiated, accepted, cleared, settled, rejected, returned; cycle opened, closed, settled |
| `lending` | one bank's | facility opened, disbursed, drawn, accrued, charged, repaid, arrears changed, closed |

Admission is four events rather than one because it is four units of work at three institutions: the bank founds itself, the settlement agent opens it an account in its own book, the clearing house writes its routing entry, and the bank records what it was told. `participant.added` is the FOUNDING alone — its payload is a bank whose settlement account numbers are still empty, because at the moment it is written no settlement agent has opened one — and `membership.recorded` is where those numbers enter the log at all. The two the other institutions write are keyed by the bank's **BIC**, because that is the only identifier either of them has ever been told.

An event is always written **inside the transaction of the operation it describes**, so a rolled-back operation leaves no record claiming it happened. A settlement that fails on an underfunded member therefore writes no `cycle.settled`. It writes no `payment.settled` either, but for a different reason and not because the two share a transaction: `payment.settled` is the **payee's bank's**, appended by `PostCreditorLegTx` in that bank's own unit of work, and a cut-off that never settled produces no advice for any bank to act on.

Because the log is append-only and unbounded, every audit endpoint is **paged**: `?limit=` (default 100, capped at 1000) and `?before=<seq>`, which is an exclusive upper cursor on the sequence number, plus `?type=` and `?entity=` to narrow. A page is the newest events below the cursor, handed back oldest-first, so paging walks backwards while each page still reads chronologically. The sequence number is a store-global total order rather than a per-book counter, so a cursor is only meaningful when replayed against the same filter that produced it.

The audit trail provides:

- Regulatory compliance (banks must maintain complete records)
- Forensic investigation capability
- System debugging and incident response
- Independent balance verification (replay events to recompute balances)

## Statements

A bank statement is a periodic report (typically monthly) summarizing all activity and balances on a customer's account. Statements rely on both the booking date and value date of each transaction.

### Derived from the Ledger, Not a Separate Account Ledger

This system is **ledger-first**: a customer's deposit account keeps no private ledger of its own. It is *backed by* a single general-ledger account (`DepositAccount.glAccount`) — a **liability** under the bank's customer subledger. A per-account statement is therefore not read from a dedicated store; it is **derived from the general ledger** by filtering for the transactions that touch the backing account and projecting each one onto the single leg that affects this account:

1. **Filter** the general ledger to transactions with an entry on the backing account.
2. **Project** each transaction onto that leg. The *other* legs are the **contra** — where the money came from or went to (often a clearing-suspense account while a payment is in flight).
3. **Sign by normal balance.** The backing account is a liability, so a **credit raises** the customer's balance and a **debit lowers** it. A consumer statement shows this as `+` / `−`; the underlying entry is still an ordinary debit or credit.
4. **Accumulate a running balance** from oldest to newest. The final running balance must equal the account's **book balance** — a built-in reconciliation check.

Two consequences follow directly from this design:

- **Holds never appear on the statement.** A hold is a deposit-layer authorization that posts nothing to the ledger until it is captured (see [Holds](#holds-authorization--pending-transactions)). A ledger-derived statement shows only real book movements.
- **A reversal is its own transaction.** Reversing a posting creates a new, equal-and-opposite transaction (see [Transaction Reversal](#transaction-reversal)); the original and the reversal both appear as separate lines that net to zero, rather than the original line disappearing.

Because a payment's debtor leg is posted by the payer's own bank (into clearing suspense) while the creditor leg is posted by the payee's own bank **after settlement** (see [The Payment Lifecycle](#the-payment-lifecycle)), an outgoing payment appears on the *payer's* statement well before the payee is credited — with the contra showing the suspense account. *When* it appears differs by scheme, and that difference is real rather than cosmetic: a credit transfer the payer instructed shows up the moment their bank takes the instruction in, while a direct debit collected from them shows up when their bank *receives* the collection, which is after the payee's bank submitted it and not at the same instant.

### What Appears on a Statement

- **Transaction listing:** All transactions with a **booking date** within the statement period, ordered chronologically. This is what the customer recognizes as their activity — "I made this payment on Feb 3rd, I got paid on Feb 15th."

- **Opening and closing balances:** Calculated using the **value date**. The opening balance is the end-of-day value-date balance from the last day of the prior period. The closing balance reflects all transactions whose value date falls within the statement period.

- **Daily balances:** The value-date balance at the end of each day, used for interest accrual and regulatory purposes.

### Why Transactions and Balances May Not Reconcile

Because booking dates and value dates can differ, the listed transactions may not perfectly "add up" to the balance change shown on the statement:

- A transaction **booked on February 25** with a **value date of March 1** would appear in February's transaction listing but would not affect February's closing balance.

- A transaction **booked on January 31** with a **value date of February 1** would not appear in February's transaction listing but would affect February's opening balance.

Most retail bank statements show both dates per transaction when they differ, so the customer can see why the figures may not seem to reconcile at first glance.

In the model this document describes, a statement's opening and closing figures would therefore be value-dated while its listing is booking-dated, both read from the entry list directly. The one statement projection this repository actually has, `web/src/lib/statement.ts`, builds neither figure: it sorts entries by value date, stamps each row's displayed date with that value date, and reduces the whole account history into a single running balance with no period boundary at all — there is no separate opening or closing figure yet, and no booking-date filtering of the listing either. End-of-day snapshots are not what would supply them: they record the ordinary **booking-date** book balance, and nothing reads them back — see [End-of-Day Snapshots](#end-of-day-snapshots). In a system that had built the checkpointing they exist for, a statement's daily balances would come from them.

## Persistence

Every layer reaches state through a **store interface** — `ledger.Store`, `deposit.Store`, `payment.Store` — never through a map it owns. There are two implementations:

| | `store/mem` | `store/pg` |
|---|---|---|
| State | Go maps behind one `sync.RWMutex` | Postgres tables |
| Setup | none | a `DATABASE_URL` |
| Survives a restart | no | yes |
| Dependencies | stdlib only | `jackc/pgx` |

`store/mem` is the reference implementation: where the two ever disagree, `mem` is right by definition and `pg` is wrong. That is not a preference — it is what makes the pair teachable, because it means every difference between them is a *database* concern rather than a domain one.

> **On dependencies.** The library core — `ledger`, `deposit`, `payment`, `api`, `seed`, `store/mem` — is standard library only. `store/pg` is the single exception, and it is why the module has a dependency at all. The driver is compiled into the binary either way, but with no `DATABASE_URL` it is never dialed: the default build needs no setup whatsoever.

### Two Stores, One Conformance Suite

`store/storetest` is a single suite that both implementations must pass. It is the only thing standing between "two backends" and "two subtly different systems", and it is run twice:

```bash
go test ./...                          # store/mem — zero setup
TEST_DATABASE_URL=… go test ./...      # the same suites, on store/pg
```

The rule it enforces is sharper than "both work": **`store/pg` must never accept or refuse a write that `store/mem` handles differently.** That rule has real consequences in the schema. There is no `UNIQUE (book_id, name)` on ledgers, subledgers or accounts, however tempting it looks — the domain does not hold a uniqueness invariant on names (two customers called "John Smith" at one bank is not an error), so the constraint would make Postgres reject a write the in-memory store accepts. Validation belongs in the domain layer; the store is a per-table key/value store that happens to be relational.

It cuts the other way too, and that direction is easier to miss. `store/mem` is a map of Go strings and will hold any byte sequence at all; Postgres will not hold a NUL in a `text` column (SQLSTATE 22021) or in a `jsonb` string (22P05), nor anything that is not valid UTF-8. So `POST /members` with `{"name":"Ban\u0000k"}` — legal JSON — created a bank on one backend and returned a 500 carrying a raw SQLSTATE on the other. The fix is not a check in `store/pg`; it is `ledger.ValidateText`, one domain rule applied to every caller-supplied string that reaches a store, whether as a value it stores or as a key it looks a row up by:

> **Text must be valid UTF-8 and free of control characters.** That covers names, descriptions, reject and return reasons, idempotency keys, end-to-end ids, IBANs, metadata keys and values, and identifiers a request supplies for lookup. Length is unbounded and every printable Unicode character is allowed; `Crédit Soleil`, `三菱UFJ銀行` and `🏦` are all fine.

The rule is drawn at control characters rather than at "the two things Postgres refuses" on purpose: a rule that can only be stated by naming a database is not a domain rule, and no field in this list has a legitimate use for a tab, a newline or an ANSI escape. Identifiers arriving in a URL rather than in a request body are screened at the API edge instead — they pass through no domain constructor — which is why `GET /reserves/bank%001` is a 400 on both backends rather than a 404 on one and a 500 on the other.

The same argument covers the errors a store returns. `ErrDuplicateIdempotencyKey` is a documented answer, so a caller may handle it and go on using the same unit of work; in Postgres any error aborts the transaction, so `store/pg` runs the statement behind that sentinel inside a `SAVEPOINT`. Wherever a SQLSTATE becomes a domain sentinel, the sentinel has to cost the caller one statement rather than the whole transaction — because that is what it costs on `store/mem`.

### The Ledger as Relational Tables

The accounting model maps onto tables almost mechanically, and the mapping is where the shape of double-entry becomes visible:

```
ledgers ─┬─▶ subledgers ─┬─▶ accounts
         │               │
transactions ─▶ entries ─┘      entries.account_id names an account
```

A transaction is a **parent row** and its legs are **child rows**. That is the relational statement of "a posting has two or more balanced entries": one-to-many, not two columns. Serializing the legs into a JSON blob would store the same bytes and lose the only thing worth having — the ability to index and sum entries *by account*, which is what every balance query does.

Four details carry more weight than they look like they should:

- **Primary keys are composite: `(book_id, id)`.** Chart-of-accounts numbers are unique *within a book*, not globally, so two participants both holding `200.100.001` is normal rather than a collision. A single-column key would force global numbering and destroy the chart of accounts as a readable, per-bank structure. The cost is that every query must carry `WHERE book_id = $1`, and the failure mode when one is missing is the quiet kind: not an error, not an empty result, but another bank's rows mixed in and looking plausible. (The `payment` layer's own entities — banks, payments, mandates, cycles, settlements — belong to no single bank, so they live in a network-wide book and are keyed by id alone.)

- **`entries` needs an explicit `position` column.** `Transaction.Entries` is an ordered slice; a relational table is a set. Without a stored position the legs come back in whatever order the plan produces, and the order is visible — on statements, and in the multi-leg settlement postings.

- **Listings are `ORDER BY created_at, seq` — never `ORDER BY id`.** IDs are counter-derived strings, so `dep_10` sorts before `dep_8` as text and a customer list silently reorders itself the moment a counter crosses a power of ten. `seq` is a monotonic integer assigned on insert and deliberately *not* touched by the upsert branch, so editing a row does not move it to the end of its own list.

- **Identity counters are ordinary rows, not Postgres `SEQUENCE`s.** A sequence survives a rollback on purpose — which would burn a transaction number on a failed posting. A counter row rolls back with its caller and stays gap-free. It also, as a side effect, serializes any two write transactions in the same book that both allocate an ID.

- **A timeline is keyed by `(parent, day)`, and that is where an exclusion constraint went.** Effective-dated rows — `overdraft_terms`, `facility_terms`, `product_versions` — carry a `day_key` text column in their primary key, so "the row in force on day D" is unique *by construction* rather than by a validation rule. The textbook relational answer is a `tstzrange` with a `GiST` exclusion constraint enforcing non-overlap; it is better in a single-backend bank and unavailable here, because `store/mem` cannot implement a range exclusion and `store/storetest` would then have nothing to pin. A day key is a rule both stores can hold to identically, and an ISO day sorts lexicographically, so a string comparison *is* a day comparison.

Two tables carry the product catalogue: **`products`** (the named entry: name, kind, whether it is retired) and **`product_versions`** (what it cost from one day onwards, keyed `(book_id, product_id, day_key)`). A version's `published_at` being NULL means *draft* — editable, and invisible to pricing — and its `hash` is verified on every read that prices a day, not merely stored. Neither fact is visible in a schema dump, so both are recorded as `COMMENT ON COLUMN` in the migration itself. `overdraft_terms` gains a `product_id` and its three pricing columns become nullable: all three NULL means the pricing floats from the product, all three set is a negotiated overlay, and the mixed state is refused by the domain rather than by a `CHECK` — for the same dual-store reason as everything else here, and with the same kind of column comment saying so, because "NULL means free" and "NULL means ask the product" look identical in a dump.

`store/pg/schema/0001_init.sql` is the whole schema, in one file, and its comments say why each of these is the way it is. There is exactly one migration: this repository has no deployed databases — every Postgres it meets is a throwaway container or a per-test schema, both of which migrate from empty — so the asset dimension was folded into `0001` rather than layered on top of it as history nobody will ever replay. `migrate.go`'s own doc comment explains why the usual "a shipped migration is immutable" rule is suspended here, and under what condition it would have to come back.

### The Asset Dimension in the Schema

**There is no `assets` table.** [Asset definitions live in Go](#assets-what-an-account-is-denominated-in), so the schema holds no row saying what EUR *is* — only rows denominated in it. What the asset dimension costs the schema is therefore four columns and one child table, not a lookup table and five foreign keys.

Three decisions in how the asset spreads from there.

**The asset is on `accounts`, and deliberately not on `entries`.** An entry's asset is always its account's, so a column on `entries` would store the same fact a second time and create the one thing a second copy always creates: the possibility that the two disagree. `PostTransaction` derives it instead, and derivation is free here — the accounts have already been loaded to check [sufficient balance](#overdraft), so the per-asset balance check adds no reads at all.

**The two subtype tables — `deposit_accounts.asset` and `facilities.asset` — are the exception, duplicated on purpose.** A deposit account's backing GL account, and every one of a facility's — the two it is opened with plus the refunds-payable account it may later be given — are all created in the asset the row records and cannot change asset afterwards, so the copies cannot drift; deriving them would turn every listing into a join for a value that can never change. `store/storetest` asserts the copies always agree — `DepositAccountAssetMatchesItsGLAccount` and `FacilityAssetMatchesItsGLAccounts` — which is what makes the duplication safe rather than merely convenient.

**A bank's internal accounts are a child table, not columns.** Suspense, reserve, unclaimed balances, returns receivable and the bank's settlement account at the central bank are `bank_assets`, keyed `(bank_id, asset)`, rather than a column apiece on `banks`, because each of those accounts is denominated in exactly one asset and [a bank operating in two of them needs two of each](#the-multi-bank-model). Keying it this way makes adding a scheme in a new asset a *data* change rather than a schema change — and it makes adding a *kind* of account cheap too, which has already happened: `returns_receivable` joined the row when the return path needed somewhere to book a forced clawback. One column here is one account per asset automatically; the same account hung off `banks` would have needed a column per asset the bank could ever operate in.

`bank_assets.settlement` is the one column in that table naming an id its owner did not allocate, and the schema says so in a `COMMENT ON COLUMN`. Every other account there was created in the bank's own book and numbered by it; this one is the *central bank's*, and this row is the account holder's note of the number.

**Admission writes three rows, one per institution, and each has exactly one writer.** `banks` is the bank's own record of itself, carrying its status and the admission it accepted. `settlement_members` (with `settlement_member_accounts` under it) is the central bank's record of an account it opened. `roster_entries` (with `roster_entry_assets`) is the clearing house's record of where to send a message. They were one row until [admission became a conversation](#admission-a-bank-exists-before-it-joins-a-scheme), and the reason they had to come apart is what the settlement agent used to do with the single row: it read the account it was about to post to off the *clearing house's* record. That is a read across an institutional boundary on the settlement path, so a settlement agent given a database of its own would have had nothing to settle from.

The two rows that are not the bank's are keyed by **BIC**, not by a bank id. Neither institution allocates or is ever told one — what a message carries is a BIC — so a bank id in either of them would be an identifier its owner had no way to have learnt and no way to check. The clearing house's row goes further and carries no account identifier *and no name*: routing is an address, and the `acmt.010` it writes the row from identifies the owner with an `OrganisationIdentification29`, which has a BIC and no name element at all.

#### The Constraint That Is Missing on Purpose

Four columns hold an asset code out of a set of three known values, and there is deliberately **no `CHECK`** restricting any of them. Adding the "missing" one breaks the conformance suite.

The argument is the one from [Two Stores, One Conformance Suite](#two-stores-one-conformance-suite), pointed at a new case. A store is a per-table key/value layer; "the asset must be one the system knows" is a **domain** rule, and `ledger.Book.CreateAccountTx` enforces it against `ledger.LookupAsset` before it creates an account — precisely where "the parent must exist" already lives for ledgers and subledgers. Postgres could express that rule as a constraint and `store/mem` could not, so neither does: a `CHECK` here would make `store/pg` refuse a write `store/mem` performs, and the two stores accepting and refusing the same writes is the whole property `store/storetest` exists to hold. The subtest is `ParentReferencesAreNotEnforced`, whose fixtures write accounts with no asset set at all. An earlier composite FK on `subledgers (book_id, ledger_id)` broke that same subtest and was removed for the same reason.

There is a second, more ordinary reason. The known assets are a one-line change to a Go slice; a `CHECK` enumerating them would make every such change a migration, and a database that had missed one would refuse writes the application considers valid.

`0001_init.sql` writes this reasoning into the database itself with `COMMENT ON COLUMN`, next to the columns it explains. That is not ceremony. The absence of a constraint is invisible: the next author reads the schema, sees four TEXT columns holding `'EUR'` and `'BTC'`, concludes someone forgot, and helpfully adds a constraint. A comment on the column is the only place that warning can sit where it will actually be read.

### A Balance Is an Aggregate, Not a Column

There is no `balance` column anywhere in the schema. A book balance is computed on demand by summing the account's entries, signed by normal balance — which makes the account's normal direction a **parameter** of the query rather than a constant in it:

```sql
SELECT COALESCE(SUM(CASE WHEN direction = $3 THEN amount ELSE -amount END), 0)
  FROM entries
 WHERE book_id = $1 AND account_id = $2;   -- $3 = the account's normal direction
```

Hardcoding `debit` there is the easy mistake, and it is only half wrong, which is what makes it hard to see: it is correct for every Asset and Expense account and it negates every Liability, Equity and Revenue one. Alice's checking account in the walkthrough below is a **Liability** — a customer deposit is money the bank owes — so a debit-hardcoded query would report its 75000 as −75000.

A stored balance is a **cache of a derivable fact**. It has to be updated in lockstep with every posting, forever, by every code path — and the first bug, crash or concurrent write that updates one without the other leaves a number that no one can reconcile against the history. Deriving it means the append-only entry list is the single source of truth and the balance cannot disagree with it, because it *is* the entry list.

Two consequences follow:

- The query does **not** filter on transaction status. A [reversal](#transaction-reversal) is a new, equal-and-opposite posting rather than a deletion, so both sets of entries are summed and net to zero. Excluding reversed transactions would double-count the correction.
- Reading a balance costs an aggregate over every entry the account has ever had. The remedy is not to add the column back; it is to **checkpoint**. That is precisely what an [end-of-day snapshot](#end-of-day-snapshots) is for: a query would start from the nearest snapshot and replay only what came after it.

That checkpointing is described here and not built: `deposit.Snapshot` is written by `TakeEndOfDaySnapshot` and read only by `GetSnapshot` and `ListSnapshots`. No balance query consults one, and a backdated posting does not invalidate the snapshots it falsifies.

### The Unit of Work

Every mutation runs inside one atomic scope: `BEGIN`, do all of it, `COMMIT` — or `ROLLBACK`, and it is as if nothing happened. The audit event is written inside the **same** scope as the operation it describes, so a rolled-back operation leaves no record claiming it happened.

The scope has to span all three layers, because the operations do. A payee's bank paying its own customer posts in the ledger, moves a deposit balance and transitions the payment row:

```
PostCreditorLeg:
  BEGIN
    creditor leg in this bank's book            (ledger layer)
    deposit balance follows                     (deposit layer)
    payment marked Settled                      (payment layer)
    audit event appended
  COMMIT      ← all of it, or none of it
```

What the scope may **not** span is more than one institution, and the two flows that move money between banks are where that shows. Settling a cut-off used to be one `Store.Update` holding the central bank's reserves and every member's creditor leg at once. It is now the central bank's own scope for the reserves, plus one of that member's own for every leg a member books afterwards — the mirror leg from its `camt.053`, each creditor leg from the clearing house's advice — joined by messages, with each institution committing only what is in its own book. A **return** is the same rule applied to one payment: the returning bank's leg, the settlement agent's reserve reversal, and the other bank's leg are three scopes at three institutions, and the reserve mirrors each bank books afterwards are more. See [SEPA Credit Transfer](#posting-choreography-sepa-credit-transfer) and [A Return](#posting-choreography-a-return) above for what each scope actually contains.

A partial success there would leave money that had left one bank without arriving at another, and no retry could tell which half had happened. This is why one concrete transaction type implements all three layers' `Tx` interfaces — `payment.Tx` embeds `deposit.Tx`, which embeds `ledger.Tx` — rather than each layer owning its own.

It is also why every mutating method comes in a **pair**: the plain one (`PostTransaction`) opens a unit of work; the `…Tx` one (`PostTransactionTx`) joins the caller's. Calling the plain one from inside an open unit of work is **refused** by both stores rather than allowed, because the alternatives are worse in different ways depending on the backend. Under `store/mem` the mutex is not reentrant and nesting would deadlock. Under `store/pg` a nested `Update` would quietly take a *second* connection from the pool and run a *separate* transaction — so its writes would commit even when the outer ones rolled back, and deep enough nesting exhausts the pool instead. Both refusing it means the single most likely mistake in this codebase behaves identically on either store.

### Three Races a Single Mutex Was Hiding

Under `store/mem` one process-wide mutex makes every unit of work atomic *and* serialized, which is a great deal more than atomicity — it hands out mutual exclusion for free. Three read-then-write races are invisible under it and have to be closed explicitly the moment the state moves into a real database.

**1. A balance check followed by the posting that depends on it.** Two withdrawals both read a balance of 1000, both conclude that 600 is affordable, and together they overdraw the account. The check has to be locked to the write:

```sql
SELECT id FROM accounts
 WHERE book_id = $1 AND id = ANY($2::text[])
 ORDER BY id
   FOR UPDATE;          -- held until COMMIT
```

`ORDER BY id` is load-bearing rather than cosmetic: two transactions locking overlapping account sets in *different* orders would each hold a row the other wants, which is a deadlock. One agreed order turns that into a queue.

**2. An idempotency key checked before it is written.** *Look, then insert* is not a check — two concurrent retries both look, both find nothing, and both post. The fix is to make the check and the write one statement and let the database decide:

```sql
CREATE UNIQUE INDEX transactions_idempotency_key_idx
    ON transactions (book_id, idempotency_key)
    WHERE idempotency_key <> '';
```

The insert is attempted; the loser gets a unique violation, which is translated back into `ledger.ErrDuplicateIdempotencyKey`. There is no window between deciding and acting because there is no separate decision.

**3. A reversal read, compared, and then written.** Two concurrent reversals both read `Posted` and both write, reversing the transaction twice. Folding the condition into the `WHERE` makes the write itself the decision, with the row count as the answer:

```sql
UPDATE transactions SET status = $3
 WHERE book_id = $1 AND id = $2 AND status = $4;
-- 0 rows affected → already reversed, or never existed
```

All three are the same rule: **never read a value, decide, and then write the decision.** Make the write the decision; where that is impossible, take the lock first.

> A note for anyone writing a test for this. A concurrency test proves nothing unless every *earlier* serialization point has been paid off first. In this schema there are three — the shared per-book ID counter row, the `books` row created on demand by the first write naming a book, and the locks above — and an operation that allocates an ID before reaching the statement under test has already been serialized by the counter, so its "race" cannot interleave. Two tests in `store/pg` passed against deliberately broken implementations for exactly this reason before they were rewritten to drive the store directly.

### Migrations

`store/pg/migrate.go` is about forty lines rather than a migration-tool dependency: an applied-set table, `go:embed`ded `.sql` files applied in filename order, one transaction each, under a Postgres advisory lock so two processes cannot run DDL at once. The interesting part of a schema is the schema.

It has one limitation worth stating plainly: **the applied-set keys on filename, with no checksum.** A file whose name is already in `schema_migrations` is skipped without being read, so editing a shipped `.sql` file in place changes nothing on a database that has already run it, and the two silently disagree from then on. A production tool stores a hash of every file and refuses to start when one no longer matches. This one does not, because there are no deployed databases here — every Postgres it meets is a throwaway container or a per-test schema, both of which migrate from empty. The rule that keeps it harmless is the ordinary one: **a shipped migration is immutable**; a schema change is a new file with the next number, never an edit to an old one.

### Running Against Postgres

```bash
make dev-pg                                    # container + backend + frontend, on Postgres
make test-pg                                   # the whole Go suite, on Postgres
make db-down                                   # stop it, delete the data
```

`dev-pg` (and `run-pg`, its production-build twin) starts the container, waits
for it to accept connections, and then runs the ordinary `dev` target with a
DSN. The pieces are still there to be used directly:

```bash
make db-up                                     # postgres:16 via docker-compose
DATABASE_URL=postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable go run ./cmd/server
```

`pg.Open` connects and then applies the embedded migrations, so a fresh database is usable immediately. Seeding is **idempotent** — `seed.Populate` builds the sample scenario against an empty system and returns without touching a populated one — which is what makes a restart against Postgres a no-op rather than a second copy of every bank. `POST /admin/reset` clears the store and rebuilds the scenario, on either backend.

The DSN is redacted before it is logged. A connection string arrives from the environment with a real credential in it, and a log line is the easiest place in a system to leak one.

Two properties are deliberately kept, and both are easy to break by accident:

- **Nothing requires a database.** `go test ./...`, `make dev` and `make run` all work on a fresh checkout with no setup. Postgres is opt-in via `DATABASE_URL` (server) or `TEST_DATABASE_URL` (tests).
- **Both runs must stay green.** A change that passes only one of them has, by definition, made the two stores diverge.

## Usage Example

Working directly with the general ledger. A `Book` is a view over a store and a
book identity — swapping `mem.New(time.Now)` for `pg.Open(ctx, dsn, time.Now)`
is the only change needed to make all of this outlive the process:

```go
ctx := context.Background()
store := mem.New(time.Now)
defer store.Close()

book := ledger.NewBook(store, "bank-a", time.Now)

// Set up the chart of accounts
gl, _ := book.CreateLedger(ctx, "General Ledger")
deposits, _ := book.CreateSubledger(ctx, gl.ID, "Customer Deposits")
revenue, _ := book.CreateSubledger(ctx, gl.ID, "Revenue")

// Create accounts. The last argument is the asset, fixed for the account's life.
alice, _ := book.CreateAccount(ctx, deposits.ID, "Alice Checking", ledger.Liability, "EUR")
bob, _ := book.CreateAccount(ctx, deposits.ID, "Bob Checking", ledger.Liability, "EUR")
fees, _ := book.CreateAccount(ctx, revenue.ID, "Transfer Fees", ledger.Revenue, "EUR")

// Transfer €100 from Alice to Bob with a €2 fee. The whole posting — the
// balance checks, the three entries and the audit event — is one unit of work.
// All three accounts are EUR, so the transaction balances within that asset.
book.PostTransaction(ctx, ledger.PostTransactionRequest{
    IdempotencyKey: "transfer-001",
    Description:    "Transfer from Alice to Bob",
    Entries: []ledger.Entry{
        {AccountID: alice.ID, Amount: 10200, Direction: ledger.Debit},
        {AccountID: bob.ID, Amount: 10000, Direction: ledger.Credit},
        {AccountID: fees.ID, Amount: 200, Direction: ledger.Credit},
    },
})

// Read a book balance on demand. Nothing stores it; see Persistence.
aliceBal, _ := book.BookBalance(ctx, alice.ID)
```

Note that customer deposit accounts are **Liability** accounts (the bank owes the customer). Debiting Alice's Liability account decreases it (she has less money), and crediting Bob's increases it (he has more money).

Adding the deposit layer for status, holds, and available balance. The register
takes the *same* store, presented as a `deposit.Store`, so both layers address
the same data inside the same transaction:

```go
dep := store.Deposit()   // the same store, presented as a deposit.Store
reg := deposit.NewRegister(dep, book, book.ID(), time.Now)

// Open a customer deposit account (creates a backing Liability GL account in
// the given asset; the deposit account records the same asset)
acct, _ := reg.OpenAccount(ctx, deposits.ID, "Carol Checking", "EUR", 0 /* no overdraft */)

// Place and then capture a €30 authorization hold
hold, _ := reg.CreateHold(ctx, deposit.CreateHoldRequest{AccountID: acct.ID, Amount: 3000})
reg.CaptureHold(ctx, hold.ID, fees.ID, 2500, "Card capture")

bal, _ := reg.GetBalance(ctx, acct.ID) // bal.Book, bal.Holds, bal.Available
```

To span both layers atomically — say, a hold capture that must commit with a
ledger posting of your own — open the unit of work yourself and drive the `…Tx`
methods with the resulting transaction:

```go
dep.Update(ctx, func(ctx context.Context, tx deposit.Tx) error {
    if _, err := reg.CaptureHoldTx(ctx, tx, hold.ID, fees.ID, 2500, "Card capture"); err != nil {
        return err // nothing is committed
    }
    _, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{ /* … */ })
    return err
})
```

## REST API

A JSON/HTTP server in `cmd/server` exposes the whole system over REST, so a frontend (e.g. a React app) can drive it. It is built on the standard library only — the module's single dependency, `jackc/pgx`, is used by the optional Postgres store in `store/pg` and by nothing else, so the default in-memory build still needs no setup at all.

### One binary, one listener per entity

There is no single API. Each entity gets a **listener of its own**, bound to its own identity: one per member bank, one for the central bank, one for the clearing house. That is what makes the scoping the rest of this system models *structural* rather than a convention — a bank's API cannot name another bank, because there is nowhere in it to put the name.

```bash
go run ./cmd/server        # :8081 central bank, :8082 clearing house, :8083+ one per bank

# The same server, on Postgres. State then survives a restart.
DATABASE_URL=postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable go run ./cmd/server
```

**One binary, one process.** What multiplies is listeners, not artefacts: there is no `cmd/bank`, no build matrix, and `make dev` starts a single Go process — it just answers on six ports over one shared `payment.Network`.

That is not a convenience. `store/mem` is a map behind a mutex in one process's memory, so four bank *processes* would be four disconnected universes: a payment from Aurora to Verde would post into an Aurora that Verde has never heard of. Postgres is strictly optional here (`go test ./...`, `make dev` and `make run` all work with no database), so the split cannot require one.

A flag once ran a single entity in its own process against a shared Postgres, which is the real topology. It is gone, and what took its place is the `mesh`: the institutions are now separate **actors**, each with its own goroutine and inbox, and one reaches another only by sending it a message. They still share this process, this store and this clock — the mesh models the separation, it does not deploy it — so a listener started alone would serve its API while no message could reach it, and the failure would surface far from its cause. A genuine split is a larger job than a flag, and it is being done rather than described: settlement no longer posts into any book but the central bank's, because each member books its own halves on advice. What is left is that every actor still shares one `Store`, and splitting it means a reconciliation-break concept this system is only starting to have — a member's own `SettlementAdvice` row, and its *absence* against a clearing suspense that has not returned to zero, are the first of it.

**Ports are static, and admission is not provisioning.** A bank created at runtime through `POST /members` gets a store row, a chart of accounts and a product — and **no listener until the process restarts**. That is a decision rather than a limitation: admitting a member to a payment network is an operational act, and an API call that instantly yielded a running bank would teach the wrong thing. Its reserve accounts are not part of that call either: they are the central bank's to open, and they exist once the scheme has [answered the application](#admission-a-bank-exists-before-it-joins-a-scheme).

The transport layer (handlers, DTOs, error mapping) lives in the `api` package and contains no business logic — it decodes requests, calls the domain methods, and encodes responses, rendering the domain's integer enums as strings (`"status": "Settled"`, `"class": "Fiat"`) while keeping amounts as integer minor units. Every request that creates an account names its **asset** explicitly, and there is no default: opening a deposit account without one is a `400`, not a euro account. The single exception is `POST /members`, whose optional `assets` array defaults to `["EUR"]` — a default for which assets a *bank joins with*, not for the asset of any account.

### The central bank — `:8081`

The settlement layer. Reserves move in its book and nowhere else.

| Method & path | Operation |
|---|---|
| `GET /reserves`, `GET /reserves/{pid}` | every bank's reserves, or one bank's — one row per asset |
| `POST /members` | found a bank and apply to the scheme for it. `202` with a **founded** bank; the settlement account this listener opens for it is written when the application reaches this actor as an `acmt.007`, and the bank learns its number from the answer |
| `GET /cycles`, `GET /cycles/{cid}` | the cycles it is instructed to settle, and their net positions |
| `GET /settlements`, `GET /settlements/{sid}` | what it settled |
| `GET /audit` | the central bank's own log |
| `POST /admin/reset` | clear the store and rebuild the sample dataset |

**There is no `POST /settlements`, and its absence is the shape of what the message layer changed.** Settling used to be an operator's act: a human opened this console and pressed a button on a cycle somebody else had closed, with nothing between the two consoles but the two operators. The instruction between them is now modelled — the clearing house reaches a cut-off, sends a **`pacs.009`** carrying the closed cycle's net positions, and the central bank answers a `pacs.002`: `ACSC`, or `RJCT`/`AM04` when a net payer's reserve cannot cover its position. A route that let a human settle beside that would be a second way to settle the same cycle, racing the first.

So the reads are what is left, and they are what the console watches settlement *with*: a cycle that is still `Closed` with no settlement against it is an instruction the central bank refused, and the net positions beside `GET /reserves` are why. The refusal's code travels in a message between two actors and is stored nowhere, which is exactly what an operator's console has to be able to reconstruct.

What an operator then *does* about it is on the clearing house — `POST /cycles/{id}/settle`, which re-sends the instruction and settles nothing itself. Fund the short member here, then ask there.

### The clearing house — `:8082`

The CSM. It sees every payment in the network, which is its job rather than a leak.

| Method & path | Operation |
|---|---|
| `GET /members` | the routing roster |
| `POST` / `GET /payments`, `POST /payments/{id}/reject\|return` | interbank payments |
| `POST` / `GET /cycles`, `POST /cycles/{id}/close` | clearing cycles |
| `POST /cycles/{id}/settle` | re-send the `pacs.009` for a cycle the central bank refused |
| `GET /settlements`, `GET /settlements/{sid}` | settlements (reading is not doing) |
| `POST` / `GET /mandates`, `POST /mandates/{id}/revoke` | direct-debit mandates |
| `GET /schemes`, `GET /directory`, `GET /assets` | schemes, address resolution, known assets |
| `GET /payments/audit` | the payment layer's log |

`GET /members` here is the **routing roster** — who this clearing house will send a message to — and it is a different question from the `POST /members` on the central bank's listener, which founds a bank and applies to the scheme for it. A single `POST`/`GET /participants` used to make the two look like one. The roster is now written by this institution, from the settlement agent's acknowledgement, and it answers "who may be addressed" rather than "who exists": a founded bank is absent from it and is a perfectly real bank.

**`POST /cycles/{id}/settle` does not settle**, and it is on this operator rather than the central bank for that reason. It rebuilds the closed cycle's `pacs.009` from its stored net positions and sends it again; the settlement agent decides, with the reserves as they are now. It exists because a refusal was otherwise terminal — a cycle stays `Closed` with no settlement, its payments `Cleared`, every payer debited into their own bank's clearing suspense and every payee unpaid, and no other route moves any of it: closing wants an open cycle, rejecting wants an `Initiated` or `Accepted` payment, returning wants a settled one. The operator funds the short member and asks again. Asking twice is safe: a cycle that is not `Closed` is refused here before a message is built, and a second instruction that got past that is refused by the settlement agent's own guard.

**Funding is the remedy for one of the two refusals, and not for the other.** `AM04` — a net payer short of reserves — is what the paragraph above describes, and money fixes it. The other is a cycle carrying a payment to a bank the scheme never admitted: the `pacs.009` cannot be *built*, because the net positions cannot all be turned into BICs, and this route fails identically with no money involved. Funding does nothing, and nothing else moves it either — `POST /payments/{id}/reject` on the offending payment is refused `invalid payment state transition`. The remedy is to admit the bank; the cycle then settles whole, and no money is lost in the meantime. See [Admission](#admission-a-bank-exists-before-it-joins-a-scheme), which records how a payment reaches a founded bank at all.

### A member bank — `:8083`, `:8084`, …

Everything that used to sit under `/participants/{pid}/…`, with the segment gone. The port carries the identity.

| Method & path | Operation |
|---|---|
| `GET /me` | the bank this listener is |
| `POST /deposits` | fund a customer account |
| `POST` / `GET /deposit-accounts` | open / list customer accounts |
| `GET /deposit-accounts/{did}/balance` | book / holds / available balance |
| `POST /deposit-accounts/{did}/status` | lifecycle action (freeze / unfreeze / markDormant / reactivate) |
| `POST` / `GET .../holds`, `POST /holds/{hid}/release\|capture` | authorization holds |
| `POST` / `DELETE .../identifiers` | issue or withdraw an account's external address |
| `POST` / `GET /transactions`, `.../{tid}/reversal` | general-ledger postings. Each entry carries its own optional `valueDate` in both directions — omitted on input it inherits the transaction's, and on output it is always the leg's own resolved date, which is [not always the transaction's](#posting-choreography-sepa-credit-transfer) |
| `GET /accounts/{aid}/balance` | book and value-dated balance for a GL account; `?asOf=` (RFC 3339, default now) picks the day the value-dated figure is computed through |
| `POST` / `GET /facilities`, `.../disbursement`, `.../draws`, `.../repayments`, `.../interest-charge`, `.../schedule` | credit facilities |
| `POST /facilities/{fid}/interest-refunds`, `GET /interest-refunds-payable` | interest the bank charged and never earned |
| `POST` / `GET /products`, `.../versions`, `.../publish`, `.../retire` | the product catalogue |
| `POST /end-of-day` | run the day's accrual |
| `GET /audit`, `GET /deposit-audit` | this bank's own logs |
| `GET /payments`, `GET /payments/{payid}` | **its own legs only** |
| `POST /payments` | accept a customer's instruction — `202` and a `paymentId` |
| `GET /directory`, `GET /assets` | resolve a payee's address; known assets |

Two of those are new, and both were impossible before. **`GET /payments` is narrowed to the bank's own legs** — what it sent and what it received. The unnarrowed list showed every bank its competitors' customers, counterparties and amounts, and narrowing it needs a caller identity that a single shared server does not have. A payment this bank is not party to answers `404` rather than `403`: it does not exist as far as this API is concerned, and a `403` would confirm the id names something real.

**`POST /payments` is where a customer's instruction lands.** A retail client must never talk to the clearing house — it has no CSM connection in the real thing either — so submission goes to its own bank, which forwards it. The answer is `202 Accepted` with a `paymentId` rather than the payment itself, and the outcome is read back from `GET /payments/{id}`. That is the shape a real CSM imposes: it answers with a `pacs.002` later, not by return value.

**Which bank may submit is the scheme's direction, and this listener enforces it.** A credit transfer is submitted by the *payer's* bank and a direct debit by the *payee's*, so a `pacs.003` posted to the payer's port is refused — the payer's bank is the one that will *answer* that collection, not the one that raises it. A scheme nobody has registered is refused before that rule is reached, rather than falling through to the push case and being told which bank submits a scheme that does not exist. Like everything else here this is **scoping, not authorization**: it says which instructions this listener is for, and verifies nothing about who is calling it.

`GET /assets` is on all three listeners and `GET /directory` on two, deliberately. An asset definition is a compiled-in constant every operator needs to render money at the right scale, and duplicating a constant is not duplicating state; a bank is a scheme participant with genuine directory access. A test holds that allowlist, so a third accidental overlap fails.

This is **scoping, not authorization**. Nothing verifies that the caller on a bank's port is that bank; the port is the claim. What it removes is the ability to reach another operator's data by editing a URL, because that URL does not exist on the port you are talking to.

Domain sentinel errors are mapped to HTTP status codes (`404` not found, `409` conflict/duplicate, `422` business-state violation, `400` malformed input) and returned as `{"error": "..."}`.

Example — a SEPA credit transfer end to end, across three listeners:

```bash
CSM=http://localhost:8082; CB=http://localhost:8081; H='-H Content-Type:application/json'

# A bank's own listener. Its port is its identity, so no path names the bank —
# and a bank admitted through POST /members has no listener until the process
# restarts, which is why this walks the two the sample dataset started with.
BANK_A=http://localhost:8083; BANK_B=http://localhost:8084
A=$(curl -s $BANK_A/me | jq -r .id); B=$(curl -s $BANK_B/me | jq -r .id)

# A deposit account is sold FROM a product and SEPA routes on IBANs, so neither
# the product nor the address is optional: an account with no identifier cannot
# be a party to a credit transfer at all.
PRD_A=$(curl -s $BANK_A/products | jq -r '.[0].id')
PRD_B=$(curl -s $BANK_B/products | jq -r '.[0].id')
ALICE=$(curl -s $H -X POST $BANK_A/deposit-accounts -d "{\"name\":\"Alice\",\"asset\":\"EUR\",
  \"productId\":\"$PRD_A\",\"identifiers\":[{\"scheme\":\"IBAN\",\"value\":\"SE89-AURORA-9001\"}]}" | jq -r .id)
BOB=$(curl -s $H -X POST $BANK_B/deposit-accounts -d "{\"name\":\"Bob\",\"asset\":\"EUR\",
  \"productId\":\"$PRD_B\",\"identifiers\":[{\"scheme\":\"IBAN\",\"value\":\"IT60-VERDE-9001\"}]}" | jq -r .id)
curl -s $H -X POST $BANK_A/deposits -d "{\"account\":\"$ALICE\",\"amount\":100000,\"description\":\"opening\"}"

# Alice instructs HER OWN BANK, on its own listener. A retail client has no CSM
# connection in the real thing and none here; the clearing house serves the same
# path for an operator's console, and pointing a customer's instruction at it
# would teach the wrong route.
#
# The answer is `202` and a `paymentId` — nothing else, because there is nothing
# else true yet: the payer's bank has run its own half and sent a `pacs.008`, and
# nobody has looked at it. Bob's IBAN is quoted because the message has to carry
# one — an address in another bank's register is exactly what Alice's bank cannot
# look up.
PAY=$(curl -s $H -X POST $BANK_A/payments -d "{\"scheme\":\"sepa.ct\",
  \"debtor\":{\"participant\":\"$A\",\"account\":\"$ALICE\"},
  \"creditor\":{\"participant\":\"$B\",\"account\":\"$BOB\",
    \"identifier\":{\"scheme\":\"IBAN\",\"value\":\"IT60-VERDE-9001\"}},\"amount\":25000}" | jq -r .paymentId)

# Ask again for the outcome. By now Bob's bank has answered and the clearing
# house has taken the payment into its scheme's open cycle, so this reads
# `Accepted` and names the cycle. (`POST /cycles` opens one, but a scheme may
# only have one open at a time — against the seeded dataset it answers "already
# open for scheme".)
curl -s $CSM/payments/$PAY | jq -r .status          # Accepted
CYC=$(curl -s $CSM/payments/$PAY | jq -r .cycleId)

# The cut-off. Netting is the clearing house's act and moves nothing; the
# `pacs.009` it then sends is what asks the central bank to discharge the net
# positions, and the central bank does that in an actor of its own.
curl -s $H -X POST $CSM/cycles/$CYC/close | jq -r .status   # Closed

curl -s $BANK_A/deposit-accounts/$ALICE/balance   # book 75000 — the debtor leg
curl -s $BANK_B/deposit-accounts/$BOB/balance     # book 25000 — settled
curl -s $CB/cycles/$CYC | jq -r '.status, .settlementId'    # Settled, set_…

# If that had said `Closed` with no settlement, the central bank refused the
# instruction — a net payer short of reserves, answered `AM04`. Fund the member
# and ask the clearing house to instruct it again. It settles nothing itself: it
# rebuilds the same `pacs.009`, and the settlement agent decides again.
# curl -s $H -X POST $CSM/cycles/$CYC/settle | jq -r .status  # Closed, and asked again
```

**The walkthrough now runs all the way to Bob being paid, and what carries it there is the message layer rather than any of these calls.** Four institutions ran seven units of work between the `POST` and the balance: Alice's bank debited her and sent a `pacs.008`, Bob's bank checked his address and answered `pacs.002`/`ACCP`, the clearing house took the payment into a cycle and told Alice's bank, and — at the cut-off — the central bank discharged the net positions on a `pacs.009`, sent each bank a `camt.053` of its own reserve account, and both banks booked their mirror legs from it; then the clearing house turned the settlement into a per-payment `ACSC`, and **Bob's bank** posted the creditor leg into Bob's account on hearing it. None of that is a function call: every hop is bytes in an inbox, handled by one goroutine per institution.

**And the reads can be early too.** `# Accepted`, `# book 25000` and `Settled` are each issued straight after the request that provoked them, so each is racing four goroutines against a `curl` process starting up — in practice the mesh wins by a wide margin, but nothing here guarantees it. If a status still says `Initiated`, or a cycle still says `Closed` with no settlement, nothing has gone wrong and nothing is lost: ask again. That is what "read it back" means in a system where the answer arrives at another institution.

**Which means every one of these responses describes a moment rather than an outcome.** `POST /payments` answers `202` and a payment that is `Initiated`, in no cycle, unseen by the payee's bank — a truthful description of the only work that had finished when the response was written. `POST /cycles/{cid}/close` answers `200` and a `Closed` cycle with net positions and no settlement on it, because the settlement agent has not been asked yet. Only the reads afterwards say what became of either, which is why `POST /payments` hands back an identifier to ask about. A refusal decided by *this* caller's own bank — no funds, an account that is not theirs — still comes back as a `4xx` on the request that caused it; a refusal decided three hops away cannot, and lands on the payment's own row instead. Watch either on the central bank's console: a closed cycle with no settlement against it is an instruction the central bank refused, and `POST /cycles/{cid}/settle` on the clearing house is what an operator does about it once the short member is funded.

> Without `DATABASE_URL` the server is **in-memory**: all state resets on restart, and `POST /admin/reset` rebuilds the sample dataset at any time. With one, it runs on `store/pg` and the data outlives the process (see [Persistence](#persistence)). Either way it is a learning and prototyping tool, not a production service.
