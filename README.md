# In-Memory Core Banking System

A simplified but functionally complete Go library modeling the core accounting engine of a bank. Intended as a reference implementation for learning and prototyping — not for production use (which would require persistent storage, distributed transactions, etc.).

## Table of Contents

- [Core Banking Concepts](#core-banking-concepts)
- [Double-Entry Bookkeeping](#double-entry-bookkeeping)
- [Chart of Accounts](#chart-of-accounts)
- [Ledger and Subledger Hierarchy](#ledger-and-subledger-hierarchy)
- [Booking Date vs. Value Date](#booking-date-vs-value-date)
- [Holds (Authorization / Pending Transactions)](#holds-authorization--pending-transactions)
- [Multi-Currency](#multi-currency)
- [Multi-Legged Transactions](#multi-legged-transactions)
- [Idempotency](#idempotency)
- [Transaction Reversal](#transaction-reversal)
- [End-of-Day Snapshots](#end-of-day-snapshots)
- [Audit Trail](#audit-trail)
- [Amounts and Precision](#amounts-and-precision)
- [Usage Example](#usage-example)

## Core Banking Concepts

A core banking system is the backbone of a financial institution. It is the "system of record" for all financial activity — every deposit, withdrawal, transfer, loan disbursement, and fee charge flows through it. The concepts below explain how this system models real-world banking.

## Double-Entry Bookkeeping

The most fundamental principle in this system is double-entry bookkeeping, invented in 15th century Italy and still the foundation of all modern accounting. The rule is simple:

> Every transaction must have equal debits and credits.

This means money never appears or disappears — it always moves from one account to another. When a customer deposits $100 cash:

- **Debit:** Bank's Cash account (asset increases — the bank has more cash)
- **Credit:** Customer's Deposit account (liability increases — the bank owes the customer more)

When a customer transfers $50 to another customer:

- **Debit:** Sender's Deposit account (liability decreases)
- **Credit:** Receiver's Deposit account (liability increases)

The balanced nature of double-entry provides a built-in error-detection mechanism: if debits don't equal credits, something is wrong.

## Chart of Accounts

The chart of accounts organizes all accounts into five fundamental types, derived from the accounting equation:

```
Assets = Liabilities + Equity + (Revenue - Expenses)
```

Each type has a "normal balance" — the direction that increases it:

| Type | Normal Balance | Description | Examples |
|------|---------------|-------------|----------|
| **Asset** | Debit | Things the bank owns or is owed | Cash, loans to customers, securities, real estate |
| **Liability** | Credit | Things the bank owes to others | Customer deposits, borrowings, bonds payable |
| **Equity** | Credit | Owner's residual interest | Paid-in capital, retained earnings |
| **Revenue** | Credit | Income earned | Interest income, fee income, trading gains |
| **Expense** | Debit | Costs incurred | Interest expense, salaries, rent, provisions |

## Ledger and Subledger Hierarchy

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

## Booking Date vs. Value Date

Every transaction carries two dates:

- **Booking Date:** The date/time when the transaction was recorded in the system. This is the "system date" or "processing date". It determines when the transaction appears in audit trails and system reports.

- **Value Date:** The date when the transaction takes economic effect. This determines when interest starts accruing, when funds become available, and which business day "owns" the transaction. The value date may be in the past (back-dated) or future (forward-dated) relative to the booking date.

**Example:** A wire transfer received on Friday evening might have:
- Booking Date: Friday 7:00 PM (when the bank's system processed it)
- Value Date: Monday (next business day, when funds are available)

Interest is calculated based on value dates, not booking dates. This distinction is critical for accurate financial calculations.

### How Statements Use Both Dates

Customer statements use both dates for different purposes:

- **Transaction listing** is ordered by **booking date** — it reflects when each transaction was recorded, matching the chronological order the customer sees activity appear.
- **Balance calculations** (opening balance, closing balance, interest accrual) are based on **value date** — this determines the economic effect.

Most retail bank statements show both dates per transaction when they differ. The statement *period* itself (e.g., "January 1–31") and the running daily balances are driven by value date. A transaction booked on January 31 with a value date of February 1 would appear on the February statement for balance purposes, even though the customer sees it in their transaction feed on January 31.

This is why the end-of-day snapshots in this system use value date — they are the foundation for interest accrual and statement generation.

## Holds (Authorization / Pending Transactions)

Holds model the "auth-capture" flow common in card payments and other scenarios where funds must be reserved before a final amount is known:

1. **Authorization** (`CreateHold`): When a customer swipes their debit card at a gas pump, the bank places a hold (e.g., $100) on the account. The book balance is unchanged, but the available balance drops by $100.

2. **Capture** (`CaptureHold`): When the customer finishes pumping ($45 of gas), the hold is captured for the actual amount. The hold is removed and a real transaction is posted for $45.

3. **Release** (`ReleaseHold`): If the transaction is cancelled (e.g., the customer drives away without pumping), the hold is released and the available balance is restored.

The difference between book balance and available balance is significant:

```
Book Balance      = sum of all posted transactions
Available Balance = Book Balance - Active Holds
```

Holds typically have an expiration time. If not captured within that window, they automatically stop affecting the available balance.

## Multi-Currency

Accounts can participate in transactions in any currency. Balances are tracked independently per currency — there is no automatic FX conversion at the ledger level. This means an account can have balances in multiple currencies simultaneously (e.g., $1000 USD, EUR 800, JPY 50000).

This is the native-currency model. If reporting in a base currency is needed (e.g., for consolidated financial statements), that conversion happens at the reporting layer, not in the ledger.

## Multi-Legged Transactions

While simple transfers involve two entries (one debit, one credit), real-world transactions often require more legs:

- **Fee split:** A $100 payment might be split into $97 to the merchant and $3 to the fee income account.

- **FX transaction:** Buying EUR 100 for $110 involves debiting a EUR asset account, crediting a USD asset account, and potentially posting the FX margin to a revenue account.

- **Loan disbursement:** Disbursing a $10,000 loan might involve crediting the customer's deposit account, debiting the loan receivable account, and debiting an origination fee from the deposit account with a corresponding credit to fee revenue.

In all cases, the invariant holds: **total debits = total credits per currency**.

## Idempotency

In distributed systems, clients may retry requests due to timeouts or network failures. Without idempotency, a retry could cause the same transaction to be posted twice.

The idempotency key mechanism prevents this:

1. The client generates a unique key (e.g., a UUID) for each logical operation and includes it in the request.
2. If the system receives a request with a key it has already processed, it returns `ErrDuplicateIdempotencyKey` instead of creating a duplicate.
3. The client can then look up the original transaction by the key.

## Transaction Reversal

In banking, posted transactions are never deleted. The ledger is an immutable record. To correct an error, a new "reversal" transaction is posted that exactly offsets the original:

- Every debit in the original becomes a credit in the reversal.
- Every credit in the original becomes a debit in the reversal.
- The net effect on all accounts is zero.

The original transaction is marked as "Reversed" for reporting purposes, and the reversal transaction carries a reference to the original.

## End-of-Day Snapshots

At the end of each business day, the system captures a snapshot of each account's balance. These snapshots serve multiple purposes:

- **Interest accrual:** Daily interest is calculated on the end-of-day balance. For a savings account earning 4% APR, the daily interest on a $10,000 balance is: $10,000 * 0.04 / 365 = $1.10.

- **Statement generation:** Monthly statements show the balance at the end of each day, transaction activity, and opening/closing balances.

- **Regulatory reporting:** Banks must report their positions to regulators. End-of-day balances are the standard reporting granularity.

- **Performance optimization:** Instead of replaying all transactions from account creation, balance queries can start from the most recent snapshot and only replay subsequent transactions.

## Audit Trail

The audit trail is an immutable, append-only log of every mutation in the system. Nothing is ever deleted from the audit trail. Every account creation, transaction posting, hold creation, hold release, reversal, and snapshot is recorded with:

- A unique event ID
- A timestamp
- The event type
- The entity affected
- The full event payload

The audit trail provides:

- Regulatory compliance (banks must maintain complete records)
- Forensic investigation capability
- System debugging and incident response
- Independent balance verification (replay events to recompute balances)

## Amounts and Precision

All monetary amounts are represented as `int64` values in the smallest unit of the currency (e.g., cents for USD, pence for GBP, yen for JPY). This is the same approach used by Stripe, most banks, and payment processors.

This avoids floating-point precision issues entirely. For example:

| Display | Internal | Unit |
|---------|----------|------|
| $100.50 USD | `10050` | cents |
| EUR 1,234.56 | `123456` | cents |
| JPY 10,000 | `10000` | yen (no minor units) |

The caller is responsible for knowing the minor unit convention of each currency and converting to/from display format.

## Usage Example

```go
svc := ledger.NewService()

// Set up the chart of accounts
gl, _ := svc.CreateLedger("General Ledger")
deposits, _ := svc.CreateSubledger(gl.ID, "Customer Deposits")
revenue, _ := svc.CreateSubledger(gl.ID, "Revenue")

// Create accounts
alice, _ := svc.CreateAccount(deposits.ID, "Alice Checking", ledger.Liability)
bob, _ := svc.CreateAccount(deposits.ID, "Bob Checking", ledger.Liability)
fees, _ := svc.CreateAccount(revenue.ID, "Transfer Fees", ledger.Revenue)

// Transfer $100 from Alice to Bob with a $2 fee
svc.PostTransaction(ledger.PostTransactionRequest{
    IdempotencyKey: "transfer-001",
    Description:    "Transfer from Alice to Bob",
    Entries: []ledger.Entry{
        {AccountID: alice.ID, Amount: 10200, Currency: "USD", Direction: ledger.Debit},
        {AccountID: bob.ID, Amount: 10000, Currency: "USD", Direction: ledger.Credit},
        {AccountID: fees.ID, Amount: 200, Currency: "USD", Direction: ledger.Credit},
    },
})
```

Note that customer deposit accounts are **Liability** accounts (the bank owes the customer). Debiting Alice's Liability account decreases it (she has less money), and crediting Bob's increases it (he has more money).
