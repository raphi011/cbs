# Chapter 9 — Clearing and Settlement

Everything so far has happened inside a single bank's books. But the payment you
care about most — sending money to someone at a *different* bank — crosses a
boundary that the previous chapters never had to. The money has to leave one
institution's ledger and arrive in another's, and the two banks do not, and must
not, write in each other's books. Bridging that gap is the job of *clearing* and
*settlement*, and the distinction between them is one of the most important — and
most misunderstood — ideas in banking.

## Executing is not the same as settling

When Alice sends Bob $100 by bank transfer, it feels instantaneous. Underneath,
three distinct things happen, usually at three different times:

1. **Initiation.** Alice's bank debits her account and creates a payment
   instruction — a message that says "move $100 from Alice to Bob at the other
   bank."

2. **Clearing.** The payment networks validate, match, and route that instruction
   alongside everyone else's. The banks work out who owes whom, and by how much. No
   money has actually moved between the banks yet.

3. **Settlement.** The actual funds move between the banks. Only now is the payment
   *final* and irrevocable.

The crucial separation is between *executing* a payment — recording the intent,
debiting Alice — and *settling* it — the real, final transfer of value between
institutions. The span between initiation and settlement is the **settlement
cycle**, and during it the payment exists in a kind of limbo: promised, recorded,
but not yet final.

This is the same booking-vs-value gap from Chapter 6, now playing out between
institutions. The booking date is when the bank recorded the instruction; the value
date is when settlement actually occurs and the funds become economically real.

## Settlement cycles: T-plus-something

How long is that limbo? It depends entirely on the payment type. The convention for
describing it is "T+n," where T is the trade or transaction day and n is the number
of business days until settlement:

| Payment type | Typical cycle | Notes |
|--------------|---------------|-------|
| **Card payments** | T+1 to T+2 | The merchant's bank receives funds 1–2 business days later. The cardholder sees an immediate *hold*, then a posted transaction after settlement. |
| **ACH / direct debit** | T+1 to T+2 | Batched and settled through an automated clearing house. Same-day options exist but aren't universal. |
| **Domestic wire transfers** | T+0 (same day) | Settled in real time through systems like Fedwire (US) or CHAPS (UK). Irrevocable once sent. |
| **International wires** | T+1 to T+3 | Routed through correspondent banks over SWIFT; each intermediary adds delay. |
| **Check deposits** | T+1 to T+5 | Highly variable; regulation (e.g. Reg CC in the US) caps the hold. Banks may grant provisional credit early. |
| **Securities (stocks, bonds)** | T+1 | Equity markets ran on T+2 for years, then moved to T+1 in 2024. |
| **Real-time payments** | Instant | Systems like FedNow (US), Faster Payments (UK), and SEPA Instant (EU) settle in seconds, 24/7. |

Notice the range: from *instant* to nearly a week. The cards example shows why the
holds of Chapter 7 exist — the immediate hold bridges the days until settlement, so
the cardholder can't spend money that's committed but not yet moved.

## What the gap costs, and how banks manage it

The settlement window isn't free. The fact that money is promised but not yet final
creates real consequences that the bank has to manage:

- **Interest follows the value date.** Accrual starts when settlement happens, not
  when the instruction was booked. A check deposited Friday earns no weekend
  interest if its value date is Monday. (Chapter 6, again.)

- **Counterparty risk lives in the window.** If the *sending* bank fails between
  initiation and settlement, the funds may never arrive. This is the deep reason
  settlement *finality* matters: it marks the precise instant after which the
  payment can no longer be unwound and the receiving side is truly safe. Everything
  before finality is, in principle, reversible.

- **Holds bridge the gap for customers.** As above, a deposit hold lets the bank
  record incoming funds without letting the customer spend them until settlement is
  confirmed — protecting the bank if the payment ultimately fails.

- **Reconciliation closes the loop.** Banks hold accounts *with each other* (called
  *nostro* and *vostro* accounts — Latin for "ours" and "yours"). After settlement,
  each side checks that what it expected to receive matches what actually arrived,
  and any discrepancy is investigated. We'll see these mirror-image accounts in
  detail in the next chapter.

## Why this needs more than one ledger

Here is the conceptual problem the rest of Part IV solves. A single ledger, of the
kind we've built up over the last eight chapters, is perfect for recording value
*within* one bank. But a cross-bank payment is precisely a movement *between* two
banks' separate ledgers — and neither bank can be allowed to reach into the other's
books and conjure a credit. So where does the money "exist" while it's in flight?
Who holds the authoritative record during the settlement window? And how do banks
that don't trust each other's ledgers agree on what moved?

The answer is a shared, trusted third party — a central bank — where every bank
holds reserves, and where settlement actually happens. That structure, and the
elegant way it makes "clearing" and "settlement" concrete rather than abstract, is
the subject of the next chapter.

---

*Previous: [Chapter 8 — The Account Lifecycle and Overdraft](08-account-lifecycle-and-overdraft.md)*
*Next: [Chapter 10 — The Interbank Network](10-the-interbank-network.md)*
