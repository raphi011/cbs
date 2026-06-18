# Chapter 6 — Booking Date vs. Value Date

Ask someone when a transaction "happened" and they'll give you one date. Banking
needs two. The gap between the moment a transaction is *recorded* and the moment
it takes *economic effect* is not a technicality — it is woven through interest
calculations, fund availability, regulatory reports, and risk. This chapter is
about those two clocks and why a bank must keep both.

## Two dates on every transaction

Every transaction carries two dates:

- **Booking date** — when the transaction was recorded in the system. This is the
  "system date" or "processing date." It determines when the transaction shows up
  in audit trails and operational reports. It answers: *when did we write this
  down?*

- **Value date** — when the transaction takes economic effect. This determines
  when interest starts or stops accruing, when funds become available, and which
  business day "owns" the money. It answers: *when does this money actually
  count?*

For a simple cash deposit handed to a teller, the two coincide — recorded and
effective at the same instant. But in a great many real situations they diverge,
sometimes by days or weeks, and the value date can fall either *after* the booking
date (the common case) or even *before* it.

## When the two dates diverge

A handful of everyday scenarios show why the second date is unavoidable:

- **Weekend and holiday processing.** A wire that arrives Friday evening is booked
  immediately — booking date Friday, 7:00 PM — but the funds only take effect on
  the next business day, so the value date is Monday. Across a long holiday weekend
  the gap can stretch to four or five days.

- **Check deposits.** A customer deposits a check on Monday and the bank records it
  at once (booking date Monday). But the check has to clear through the interbank
  system, so its value date might be Wednesday or Thursday. Until the value date
  arrives, the money earns no interest and may not be available to spend.

- **Back-dated corrections.** On March 5, an operations team realizes a corporate
  payment should have taken effect on February 28. They book the correction today —
  booking date March 5 — but give it a value date of February 28, so that interest
  for the intervening days is computed correctly. Here the value date is *earlier*
  than the booking date. Without this, the customer would lose several days of
  interest.

- **Forward-dated standing orders.** A customer schedules rent for the 1st of next
  month. The bank may record the instruction today (booking date January 20) but
  assign a value date of February 1, when the money actually moves. Here the value
  date is in the *future*.

- **Securities settlement.** A stock trade executed Monday (trade date, "T")
  typically settles the next business day ("T+1"). The cash leg is booked Monday but
  value-dated to Tuesday, when ownership and funds actually change hands.

The unifying principle is this: **interest, availability, and reporting follow the
value date, not the booking date.** Use the wrong one and customers earn too much
or too little interest, and regulatory balance reports come out wrong. A check
deposited Friday does not earn a weekend's interest if its value date is Monday —
and getting that right requires the system to remember both dates for every
transaction.

## Who decides the value date

The value date isn't chosen by any single actor; it depends on the type of
transaction, and several parties have a hand in it:

- **Automated rules** handle the vast majority. The bank's systems assign a value
  date from predefined policies per product and channel — domestic wires get
  same-day value, checks get T+2, international transfers get T+1 to T+3 depending
  on the corridor. In practice a rules engine sits upstream of the ledger and
  stamps the value date before the transaction is ever posted.

- **Payment networks** can dictate it. A SWIFT message for an international
  transfer carries a value-date field set by the sending bank, which the receiving
  bank is expected to honor. Securities markets impose conventions like T+1 that
  both sides agree to in advance.

- **The customer** influences it for scheduled payments and standing orders — they
  choose when a payment should take effect, and that becomes its value date.

- **Operations staff** set it by hand for corrections and adjustments, choosing the
  date when the economic event actually occurred or should have.

- **Regulation** bounds all of the above. Rules such as the US Expedited Funds
  Availability Act (Regulation CC) cap how long a bank may hold check funds,
  putting a legal ceiling on how far the value date can lag the booking date.

The takeaway for anyone building or reasoning about a banking system: by the time a
transaction reaches the ledger, its value date has usually already been decided by
a layer of policy above it. The ledger's job is to faithfully record both dates;
the *choice* of value date is a business decision made upstream.

## A glimpse ahead: the same gap, named differently

This two-date distinction reappears under other names later in the book. When we
look at balances in the next chapter, we'll find that an account has *different
balances depending on which date you compute it from*. When we reach clearing and
settlement in Part IV, we'll see that the booking-vs-value gap is precisely the
window between *initiating* a payment and *settling* it — the span during which the
money has been promised but has not yet, finally, moved. The two clocks introduced
here are the same two clocks that govern how money moves between banks; we're just
meeting them first in their simplest form.

---

*Previous: [Chapter 5 — Transactions and Postings](05-transactions-and-postings.md)*
*Next: [Chapter 7 — Balances and Holds](07-balances-and-holds.md)*
