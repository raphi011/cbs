# Chapter 7 — Balances and Holds

"What's my balance?" sounds like a question with one answer. It isn't. A single
account has several balances at the same moment, each computed differently and each
used for a different purpose. Understanding why is the key to a string of everyday
mysteries: why your available funds differ from your statement balance, why a gas
station "holds" $100 when you bought $40 of fuel, and why the money you just
deposited isn't spendable yet. This chapter untangles the balances, then explains
the mechanism — the *hold* — that sits behind most of the confusion.

## One account, three balances

At any instant, an account carries three distinct balances:

- **Book balance** (also called the *ledger balance*). The balance computed from
  every posted transaction, by booking date, regardless of value date. It reflects
  everything that has been recorded. This is the figure most people think of as
  "the balance."

- **Value-date balance** (also called the *interest-bearing balance*). The balance
  computed only from transactions whose value date has already passed — the
  economic reality of the account. This is what the bank uses to calculate
  interest, generate end-of-day snapshots, and produce regulatory reports. It is the
  balance that "really counts" today.

- **Available balance.** The book balance minus anything currently reserved by
  holds (and adjusted for any overdraft limit, which we'll meet in the next
  chapter). This is what an ATM or a point-of-sale terminal checks to decide
  whether to approve a transaction — the money you can actually spend right now.

These three can all differ at once. A single account might show:

| Balance | Amount | What drives it |
|---------|--------|----------------|
| Value-date balance | $9,500 | only transactions whose value date has passed |
| Book balance | $10,000 | all posted transactions |
| Available balance | $9,200 | book balance minus $800 of active holds |

The value-date balance sits *below* the book balance here because a forward-dated
transaction has been booked but hasn't reached its value date yet — recorded, but
not yet economically real. (It can also sit *above* the book balance, if a
back-dated correction added value to a past date.) The available balance sits below
both because $800 is currently reserved. Each number is correct; they just answer
different questions. This is the booking-vs-value distinction from Chapter 6,
showing up as a difference you can read right off the account.

## Holds: reserving money before the amount is known

The available balance differs from the book balance because of **holds**, and holds
exist to solve a specific real-world problem: sometimes you must *reserve* funds
before you know the final amount.

The classic case is paying at the pump. The sequence runs:

1. **Authorization.** You swipe your card at a gas station. The bank doesn't yet
   know how much fuel you'll buy, so it places a hold — say $100 — on your account.
   Your *book balance* is unchanged; no money has moved. But your *available
   balance* drops by $100, so you can't simultaneously spend that money elsewhere.

2. **Capture.** You finish pumping $45 of fuel. The hold is *captured* for the
   actual amount: the $100 reservation is removed and a real $45 transaction is
   posted to the ledger.

3. **Release.** If the sale is cancelled instead — you drive off without pumping —
   the hold is simply *released*. The reservation disappears and your available
   balance is restored, with no trace in the ledger.

This same authorization–capture pattern underlies card payments generally, hotel
and rental-car deposits, and any flow where funds must be earmarked before the
final figure is settled.

## Holds live off the ledger

Here is the subtle and important part. An active hold **is not a ledger
transaction.** The ledger, as we've insisted since Chapter 2, records only posted
transactions — actual debits and credits that have settled and that balance. A hold
moves no money. It is an *operational* reservation, tracked alongside the ledger,
not within it. It never appears in the general ledger and never disturbs the trial
balance.

A hold touches the ledger at exactly one moment: when it is **captured**. At that
point — and only then — a real, balanced transaction is posted for the actual
amount: the customer's deposit liability is debited and the counterparty credited,
just like any other transaction from Chapter 5. If the hold is **released** instead,
*nothing ever reaches the ledger*; from an accounting standpoint it is as though it
never happened.

This separation is deliberate and clarifying. The ledger stays a pure record of
*settled value* — every entry in it is real, final money that moved. The
operational state of money that is merely *promised or reserved* lives one layer
up. In our reference library this is an explicit architectural seam: the pure
ledger core knows only posted transactions and book balances, while a deposit layer
on top of it owns account status, holds, and the available balance they reduce.
The ledger only gets involved when a hold is captured and becomes a real
transaction. Keeping the two apart is what lets the ledger remain provably
consistent while the messier, more operational world of authorizations does its
work without ever corrupting the books.

## The shape of the available balance

We can now write the available balance as a formula. Ignoring overdraft for the
moment (the next chapter adds it), it is:

```
Available balance = Book balance − Active holds
```

Holds also typically carry an expiration. If a hold isn't captured within its
window, it stops affecting the available balance automatically — which is why a
pending charge you never completed eventually "falls off" and your spendable funds
return on their own.

With balances and holds in hand, we've covered how money behaves within a single
account at a single point in time. The next chapter zooms out to the account's
whole life — the states it passes through from opening to closure — and to what
happens when someone tries to spend money that isn't there.

---

*Previous: [Chapter 6 — Booking Date vs. Value Date](06-booking-date-vs-value-date.md)*
*Next: [Chapter 8 — The Account Lifecycle and Overdraft](08-account-lifecycle-and-overdraft.md)*
