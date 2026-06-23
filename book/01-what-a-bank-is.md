# Chapter 1 — What a Bank Is

Before we can talk about debits, credits, or settlement, we need a clear picture
of what a bank actually *is* — not as a building with a vault, but as an
economic machine. The surprising thing about banks is that almost everything they
hold and almost everything they owe is just a record. The records *are* the bank.

## A bank is a system of record

At its heart, a bank is the authoritative record of who owns what money. Every
deposit, withdrawal, transfer, loan, and fee is an entry in that record. When
people in the industry talk about a *core banking system*, they mean exactly
this: the system of record for all of an institution's financial activity. It is
the backbone everything else hangs from — the mobile app, the ATM network, the
card rails, the regulatory reports — all of them are ultimately reading from or
writing to this one source of truth.

This is why correctness matters so much. A social network can tolerate a lost
"like." A bank cannot tolerate a lost cent. The entire discipline of bank
accounting exists to make sure that the record is always internally consistent
and that errors are caught the moment they happen rather than discovered months
later.

## Money, from the bank's point of view, is a promise

Here is the first idea that trips up almost everyone. When you deposit $500 in
cash at your bank, that $500 does not "belong to you" in any account the bank
keeps for you. The physical cash becomes the bank's property — it goes into the
bank's vault and the bank can lend it out. What you have in return is a *claim*:
the bank now owes you $500, payable on demand.

So the balance you see in your checking account is, from the bank's perspective,
a debt the bank owes you. It is a liability — an obligation. This is not a
metaphor or an accounting trick; it is the literal legal and economic reality. A
deposit is a loan you make to the bank.

This single reframing — *your money is the bank's promise to pay you* —
unlocks most of what follows. Once you see deposits as the bank's obligations,
the whole structure of bank accounting starts to make sense.

## Owning and owing: the two sides of a bank

Everything a bank holds falls into one of two broad categories:

- **Things the bank owns or is owed** — its *assets*. The cash in the vault and
  at the central bank, the loans it has made (because borrowers owe it money),
  the securities it holds.
- **Things the bank owes to others** — its *liabilities*. Customer deposits,
  money it has borrowed from other banks, bonds it has issued.

The gap between the two — what would be left for the owners if the bank sold
every asset and paid off every debt — is the *equity*, the shareholders' stake.

That relationship is the foundation of all bank accounting, and it always holds:

```
Assets = Liabilities + Equity
```

If a bank has $100 million in assets and owes $92 million, then $8 million of
equity belongs to its owners. This equation is never allowed to break. Every
single thing a bank does — taking a deposit, making a loan, charging a fee,
paying interest — is recorded in a way that keeps both sides equal. The next two
chapters are entirely about *how* that balance is maintained, transaction by
transaction.

## A worked intuition: taking a deposit

Watch what happens to the equation when that customer deposits $500 in cash:

- The bank's **cash** (an asset) goes up by $500.
- The bank's **deposit owed to the customer** (a liability) goes up by $500.

```
Assets        Liabilities + Equity
 +$500    =    +$500
```

Both sides rose by the same amount, so the equation still balances. Notice that
the bank is now *richer in cash* and *deeper in debt* at the same time — and
that's completely normal. A bank growing its deposits is taking on more
obligations and more assets in lockstep. The art of banking is what it does with
those assets in between: lending them out at a higher rate than it pays you,
earning the difference.

## Why this is worth modelling carefully

It would be possible to build a banking system that just stores a single number
per customer — their balance — and adds to it or subtracts from it. Plenty of
toy systems do exactly that. But real banks don't, and for good reasons that the
rest of this book will draw out:

- A single number can't tell you *why* a balance changed, or prove that it
  changed for a legitimate reason.
- A single number can't distinguish money that has been *recorded* from money
  that has actually *arrived* — a distinction that turns out to be central to
  how interest, availability, and risk work.
- A single number gives you no way to detect when something has gone wrong.

The accounting techniques in the coming chapters solve all three problems at
once. They are old — the core of the method was written down in fifteenth-century
Italy — but they have never been improved on, because they get something exactly
right. That is where we turn next.

---

*Next: [Chapter 2 — Double-Entry Bookkeeping](02-double-entry-bookkeeping.md)*
