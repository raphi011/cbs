# Chapter 8 — The Account Lifecycle and Overdraft

So far an account has been a static thing: it exists, money flows through it,
balances are computed. But a real account has a *life*. It is opened, used, perhaps
forgotten, perhaps frozen by a court, and eventually closed — and at each stage,
different operations are permitted. This chapter follows that lifecycle, then turns
to the question every account eventually raises: what happens when someone tries to
take out more than is there?

## Accounts have states

A bank account isn't simply created and then usable forever. It moves through a
small set of well-defined states, and the state governs what you're allowed to do
with it. There are four:

| State | What it means | Allowed operations |
|-------|---------------|--------------------|
| **Active** | Normal operating state; open and fully functional. | Everything: debits, credits, holds, statements. |
| **Dormant** | No customer activity for a long period (often 12–24 months, varying by jurisdiction). The bank may charge dormancy fees. | Credits only — an incoming payment reactivates the account. Debits and new holds are blocked until it's reactivated. |
| **Frozen** | Temporarily restricted, usually by court order, fraud investigation, or regulatory action. | View balance only. Debits, credits, and holds are blocked. A freeze can be partial — e.g. allowing credits but blocking debits. |
| **Closed** | Permanently shut. Accepts no transactions. | None. The balance must be zero before closing. Historical data is retained for audit and regulation. |

These states exist because banking is a regulated activity with obligations beyond
the customer's wishes. Dormancy rules protect forgotten money (and eventually route
it to the state as unclaimed property). Freezes let the bank comply with law
enforcement and protect victims of fraud. Closure has to guarantee that no value is
stranded. The state machine encodes all of that as enforceable rules rather than
leaving it to ad-hoc checks.

## How accounts move between states

The transitions between states are themselves governed by rules:

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

The rules that matter most:

- **Active → Dormant** happens automatically after a configurable period of
  inactivity. The bank usually must notify the customer before the switch.

- **Dormant → Active** is triggered by any customer-initiated activity, or by an
  explicit reactivation request. This is why dormant accounts still accept credits:
  an incoming payment is exactly the kind of activity that should wake the account
  up.

- **Any → Frozen** can happen at any moment for legal or fraud reasons. Crucially,
  the *previous* state is remembered, so that when the freeze is lifted the account
  returns to where it was — an account frozen while dormant goes back to dormant,
  not to active.

- **Active → Closed** is permitted only when the balance is exactly zero, with all
  holds resolved and all scheduled payments cancelled. You cannot close an account
  that still owes or is owed money; the value has to go somewhere first.

- **Closed is terminal.** A closed account is never reopened. A customer who wants
  to bank again gets a brand-new account. This permanence is what makes "closed" a
  reliable statement for audits and regulators.

A system models this as a true state machine, with each transition checking the
current state and the relevant precondition (a zero balance to close, a remembered
prior state to unfreeze) before it's allowed. Like the immutability of the ledger
in Chapter 5, the value is in making the rules *enforced by construction* rather
than hoped for.

## Overdraft: spending money that isn't there

An **overdraft** occurs when a transaction would push an account's available
balance below zero. There's more than one way a bank can respond, and the choice is
a business policy, not an accounting one:

- **Hard decline.** The transaction is simply rejected. This is the strictest model
  — if a debit would push the available balance negative, the system refuses it and
  returns an "insufficient funds" error. It is what our reference library does for
  the account types where going negative makes no economic sense.

- **Arranged overdraft (a credit facility).** The customer has a pre-agreed
  overdraft limit, and transactions are allowed as long as the negative balance
  stays within it. An account at $0 with a $500 limit can be debited up to $500.
  Interest is charged on the overdrawn amount, usually at a higher rate than normal
  lending — because an overdraft *is* a short-term loan.

- **Unarranged overdraft.** The bank may, as a courtesy, honor a transaction that
  exceeds the arranged limit (or where none exists), but typically at much higher
  fees. Many jurisdictions cap those fees by regulation.

From a pure ledger standpoint, an overdraft is nothing special. The book balance of
a liability deposit account simply goes negative — and a negative liability means
the relationship has flipped: the customer now owes the bank rather than the bank
owing the customer. The accounting just records this economic reality. The
*overdraft limit* is a business rule enforced **before** posting; the ledger itself
doesn't police limits, it faithfully records whatever results.

A worked example makes the interaction with the available balance concrete:

```
Account balance:        $200    (bank owes the customer $200)
Overdraft limit:        $500
Transaction:           −$600    (a debit of $600)
New balance:           −$400    (the customer now owes the bank $400)
Available for further:  $100    (limit $500, of which $400 is used)
```

Folding overdraft into the formula from the previous chapter, the available balance
becomes:

```
Available balance = Book balance + Overdraft limit − Active holds
```

The overdraft limit *raises* the floor of what's spendable; active holds *lower*
it. Between the lifecycle states that gate which operations are even permitted, and
the overdraft policy that decides how far a balance may fall, an account's behavior
is fully specified — within a single bank. The next part of the book leaves the
single bank behind and asks the harder question: how does money move *between*
banks at all?

---

*Previous: [Chapter 7 — Balances and Holds](07-balances-and-holds.md)*
*Next: [Chapter 9 — Clearing and Settlement](09-clearing-and-settlement.md)*
