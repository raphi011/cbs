# Chapter 12 — SEPA: Credit Transfers and Direct Debits

Chapter 11 gave us the shared machinery — clearing suspense, central-bank reserves,
and the netting cycle — and the axes that distinguish one scheme from another: push
or pull, net or gross, mandate required, returns allowed. Now we meet the two
net-settled SEPA schemes by name. The worked example in Chapter 11 *was* a SEPA
credit transfer; the postings there are the postings here, and there is no need to
repeat them. This chapter layers on the scheme-specific rules: who may initiate,
what authorization is required, and what happens when a settled payment needs to be
unwound.

## SEPA Credit Transfer — a push

A **credit transfer** is the salary-and-invoice workhorse: the payer's bank
initiates and *pushes* funds to the payee. No mandate is needed — you're sending
your own money. In the European SEPA system this is the *SEPA Credit Transfer*
(SCT), settling at T+1, and it corresponds to the ISO 20022 `pacs.008` interbank
message. The worked example in Chapter 11 *was* a credit transfer; there is nothing
more to it mechanically.

(The mention of `pacs.008` points at a real-world detail worth noting: actual
interbank payments are carried as standardized ISO 20022 XML messages with codes
like `pacs.008` for a credit transfer and `pacs.003` for a direct debit. A learning
model can stand in a simple payment record for those messages and just *name* the
message each scheme corresponds to, without parsing real ISO 20022.)

## SEPA Direct Debit — a pull, with a mandate

A **direct debit** runs the other way. The payee — a utility, a gym, a streaming
service — *pulls* funds from the payer's account. Since someone reaching into your
account is obviously dangerous, a direct debit requires a **mandate**: a standing
authorization you sign in advance, naming the specific creditor allowed to collect
and often capping the amount.

Before any collection, the scheme checks that mandate: that it exists, is still
active (not revoked), matches both the payer and the named payee, and stays within
its amount limit. Fail any check and the payment is rejected outright — with
specific reasons like "mandate required," "mandate revoked," or "amount exceeds the
mandate." In SEPA this is the *SEPA Direct Debit* (SDD), mapping to
the ISO 20022 `pacs.003` message. This model settles it at a flat T+2; real SDD Core
instead settles on the collection's *due date* (presented at least one business day
ahead), but the fixed delay keeps the model simple.

Here's the payoff of the shared-machinery design: once the mandate checks pass, the
*postings for a direct debit are identical to a credit transfer* — debtor →
creditor, through clearing suspense and central-bank reserves, exactly as in Chapter 11. The
money flows the same way; only the rules about *who may initiate* and *what
authorization is required* differ. That's the whole reason schemes can be an
abstraction over one posting engine rather than separate systems.

## Returns: unwinding a settled payment

Direct debits introduce a wrinkle the credit transfer didn't have: they can be
**returned**. If the payer disputes a collection or turns out to have lacked funds,
a *return* — one of SEPA's several "R-transactions" (the family also includes
*refunds*, which a debtor can claim no-questions-asked within 8 weeks, and bank-initiated
*reversals*) — reverses the flow even after settlement. It
posts compensating transactions that move the funds back from creditor to debtor
across the central bank, fully unwinding the original and restoring both customers'
balances — the cross-bank cousin of the reversal from Chapter 5. Where an ordinary
reversal undoes a posting in one ledger, a return has to undo a flow that crossed
several ledgers and the central bank, but the principle is the same: you don't erase
history, you post a new, balancing transaction that cancels it.

SEPA moves money from debtor to creditor by agreement between two parties — a payer
and a payee, governed by a mandate when the payee pulls. Cards introduce a different
*shape* entirely: a four-party network (cardholder, merchant, issuing bank, acquiring
bank) and a two-step life of authorize-then-settle that spans Chapter 13.
