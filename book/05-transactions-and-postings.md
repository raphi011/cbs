# Chapter 5 — Transactions and Postings

We have the balance rule, the five account types, and an exact way to write down
money. Now we can talk about the unit of work that ties them together: the
*transaction*. This chapter pins down the vocabulary that surrounds it — words
that practitioners use loosely and interchangeably — and then covers the three
properties that make a bank's transactions trustworthy: they can be larger than
two sides, they are safe to retry, and they are never erased.

## A precise vocabulary

A handful of words get used almost interchangeably in banking, which is fine once
you know they're synonyms but bewildering until then. Here is the precise set.

- **Entry** — a single debit or credit to one account. It carries an account, an
  amount, and a direction (debit or credit). It is the smallest unit of
  bookkeeping; everything else is built from entries.

- **Leg** — a synonym for *entry*. The word is used when emphasizing that a
  transaction has several sides: a "two-legged" transfer has one debit leg and one
  credit leg; a "multi-legged" transaction has three or more. A leg *is* an entry;
  there is no separate concept.

- **Posting** — the act of recording a transaction in the ledger (the verb: "to
  post a transaction"). Loosely, "a posting" can also mean an entry that has been
  recorded. The key fact about posted transactions is that they are *final*: they
  are never edited or deleted, only reversed (more on that below).

- **Transaction** — a balanced set of entries that are posted together as one
  atomic unit. "Atomic" means all-or-nothing: either every leg is recorded or none
  is. And within any transaction, **total debits equal total credits** — the
  balance rule from Chapter 2, now stated as a property of the transaction as a
  whole.

One more term you'll meet throughout the book: a **trial balance** is the list of
every account's balance at a moment in time. Because every transaction balances,
the debit balances across all accounts must equal the credit balances. A trial
balance that doesn't sum to zero is a proof that a bookkeeping error exists
somewhere — the self-checking property from Chapter 2, applied to the whole book.

## Transactions can have many legs

The textbook example of double-entry is a two-legged transfer: debit one account,
credit another, equal amounts. But real transactions frequently need more than two
legs, and the balance rule handles them with no modification — the *total* debits
must equal the *total* credits, however many legs there are.

A few common multi-legged shapes:

- **A fee split.** A customer sends $100, but $3 of it is a transfer fee. One debit
  of $100 from the sender; one credit of $97 to the recipient; one credit of $3 to
  the bank's fee-income account. Debits: 100. Credits: 97 + 3 = 100. Balanced.

- **A loan disbursement.** Paying out a $10,000 loan might credit the customer's
  deposit account, debit a loan-receivable asset, and — if there's an origination
  fee — debit the deposit account for the fee with a matching credit to fee
  revenue. Several legs, still balanced.

- **Interest accrual with tax.** Posting monthly interest might debit the bank's
  interest-expense account and credit the customer's deposit, with a third leg
  withholding tax into a liability account owed to the tax authority.

The invariant is the same every time: **total debits = total credits.** A system
enforces it by summing each direction at the moment of posting and refusing
anything that doesn't match — which is exactly what our reference library does
before it will write a transaction to the books. The number of legs is irrelevant;
the balance is not.

## Idempotency: making retries safe

Banks run on networks, and networks fail in the worst possible way: not by losing
a request outright, but by losing the *response*. A client sends "transfer $100,"
the transfer is recorded, and then the acknowledgement is lost on the way back. The
client, having heard nothing, retries. Without a defense, the bank posts the
transfer a second time and the customer is debited twice.

The defense is an **idempotency key**. The client attaches a unique identifier — a
UUID, say — to each logical operation. The rule is:

1. The client generates a unique key for each logical operation and includes it in
   the request.
2. If the system sees a key it has already processed, it refuses to create a
   duplicate and instead reports that the key was already used.
3. The client can then look up the original transaction by that key and learn what
   happened, rather than guessing.

The key turns an unreliable network into a safe one. A retry with the same key is
recognized as the *same* operation, not a new one, so retrying becomes harmless —
exactly the property you want when you can never be sure whether your first attempt
got through.

## Immutability and reversal

Here is a rule that surprises engineers coming from ordinary databases: in
banking, **posted transactions are never deleted, and never edited.** The ledger
is an immutable, append-only record. This isn't a limitation to work around; it's a
feature that underpins the whole system's trustworthiness. An auditor, a
regulator, or a forensic investigator must be able to see exactly what was recorded
and when, with no possibility that history was quietly rewritten.

So how do you fix a mistake? You don't reach back and change the bad entry. You
post a *new* transaction — a **reversal** — that exactly cancels the original:

- Every debit in the original becomes a credit in the reversal.
- Every credit in the original becomes a debit in the reversal.
- The net effect across all affected accounts is zero — it's as if the original
  never happened, economically, while remaining fully visible historically.

The original transaction is marked as "reversed" for reporting, and the reversal
carries a reference back to the transaction it undoes, so the relationship between
the mistake and its correction is permanent and auditable. Two true facts now live
in the record side by side: that the original posting was made, and that it was
later unwound. Nothing was hidden; the correction is itself part of the history.

This append-only discipline is the same instinct behind the integer amounts of
Chapter 4 and the trial balance of Chapter 2 — build the record so that it is
*provably* correct and *provably* complete, and never give yourself the ability to
quietly break that promise.

## What we still can't express

Everything in this chapter has treated a transaction as a single event at a single
moment. But banking runs on a subtler notion of time. The day a transaction is
*recorded* is often not the day it takes *economic effect* — and that gap, far
from being an edge case, drives interest, availability, and risk. That is the
subject of the next chapter.

---

*Previous: [Chapter 4 — Ledgers, Subledgers, and Money](04-ledgers-subledgers-and-money.md)*
*Next: [Chapter 6 — Booking Date vs. Value Date](06-booking-date-vs-value-date.md)*
