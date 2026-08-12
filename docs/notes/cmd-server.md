# cmd/server — measured findings

Evidence behind claims made in `cmd/server`. See [README](README.md) for what
belongs here.

## The payer's bank collects from the settlement agent before the clearing house

`TestTheMessagesAReturnPutsOnTheWire` asserts that the payer's bank's `camt.053`
crosses before the `pacs.004` addressed to the same bank. The two are in
different queues at different institutions, so nothing about the order they were
written in survives the trip; what forces the pair is the BANK's own collection
order — the settlement agent first, then the clearing house — which is a decision
each bank makes about its own operations. `AdvanceDay`'s phase 7 is where it is
declared.

**Falsification.** Swapping `CentralBank.advise`'s two sends — the statements and
the answer — leaves the test PASSING. The order the settlement agent wrote the
two files in decides nothing. Swapping the BANK's two collections inverts the
pair, and the assertion then fails every run. A reader trying to break the test
should reach for the second, not the first.

**What the order buys.** The payer's bank receives the reserves back, so its
`camt.053` CREDITS the clearing suspense that its refund then draws on, and the
relayed `pacs.004` is what makes it draw. Reversed, that bank pays its customer
out of a suspense the return has not yet credited. That commits — suspense is a
Liability and `ledger.Book.checkSufficientBalance` guards only Asset and Expense
accounts — and for that interval the bank's own books say it lent its customer
the money.

The payee's bank receives a pair too, ordered by the same chain. It is not
asserted because nothing depends on it: that bank posted its clawback before any
of this. What remains genuinely undetermined is the interleaving across actors.

## A retried return read its own unwound leg as "already posted"

Behind `TestAReturnRetriedAfterAnUnwindRepaysThePayer`.

After an RJCT, `ReverseReturnLegTx` reverses the returning bank's posting but the
leg id was left on the payment. The retry then read that id as "this bank has
already posted", so `PostReturnLegTx` answered the redelivery arm without
posting. The conversation ran to completion around a refund that did not exist:

    the biller was clawed back
    the payer was repaid nothing
    250,000 stranded in the returning bank's clearing suspense
    an ACSC on the wire saying it had all worked

Nothing sweeps a clearing suspense, so the stranded amount is stranded for ever.
The status, the `pacs.002` and the message tap all look identical to a return
that worked, which is why that test asserts three balances and a suspense rather
than a status.

## The addressing refusals were answered 422 with the payer already debited

Behind `TestPaymentAddressingRefusalsAre422`.

Three of that test's five refusals returned 422 while the payer had already been
debited. The submitting bank committed the debtor leg in one unit of work and
built its `pacs.008` in a second, and an unaddressable payee fails the second. So
a request the API reported as refused moved 1000 out of Alice's account into a
clearing suspense, against a payment nobody would ever answer. No file had been
uploaded, so there was nothing in any day's report, and a client that retried the
refusal drained the account.

The status codes alone were green throughout. `payment.TakeInstruction` is what
makes the leg and the message one unit of work; the balance assertions are what
would have seen it.

## Deleting each addressing guard flips 422 to 500

Behind `TestPostPaymentRefusesEachWayAnAddressFails`. Re-derived by mutation on
the code as it stands, deleting each guard in `payment/system.go` and
`payment/directory.go` and rerunning:

| guard removed | outcome |
|---|---|
| no counterparty name | 422 → 500. Nothing else in `SubmitPaymentTx` rejects an empty name — `ledger.ValidateText` permits `""` — so the request proceeds into building the outbound `pacs.008` and fails its own mandatory-element check (`iso20022.ErrMissingElement`, `Cdtr/Nm`), for which `api/errors.go` has no entry. |
| a bank code this copy has no entry for | 422 → 500 on `Cdtr/CdtrAgt`: the derivation returns nothing, the message cannot be rendered without a BIC, and the failure lands after the payer's leg would have posted — which is why the refusal is at submission and not at message-building. |
| an address no directory here covers | the same, one branch earlier. |
| an address that resolves (the control) | unaffected. |

## Concurrent resets left several copies of some entities and none of others

Behind `TestConcurrentResetsLeaveExactlyOneDataset`. Eight resets against a
durable store produced **twelve participants where there should have been four**.

A reset is a clear followed by a rebuild and neither half is inside the other's
unit of work — the rebuild is dozens of separate ones. Two overlapping resets
interleave: the second clears over the first's half-built scenario, and the first
then finishes writing on top of the second's.

## The overdraft accrual arithmetic

Behind `TestEndOfDayAccruesBothFacilityAndOverdraftInterest` and the terms tests.

€500 at 12% ACT/365 is `50_000 × 120_000 / 365 = 16_438_356` micro-minor-units a
day. Two days is `32_876_712`, which rounds to 33 cents.

The accrual window opens when the ACCOUNT is opened — the opening terms row every
account gets — not on the first end-of-day. That is what makes the first day
count: an increment that took its first run as the baseline charged nothing for
it, dropping a day of interest on every account ever priced.

## Guarantees this system does not have

- **A cross-bank IBAN collision is representable and nothing refuses it.**
  Uniqueness is enforced per bank, which is the widest scope a register can see.
  Only a sweep over every bank's register could see both, and no bank may make
  one. The collision takes a duplicate ALLOCATION, which is a defect in an
  allocation rather than in a register — which is why the instrument that catches
  it (`payment/recon`) has to open every institution's database at once. See
  `payment`'s `TestACrossBankCollisionTakesADuplicateAllocation`.
- **There is no test for a misrouted payment, and the absence is a result.** An
  instruction carries an address and a name and has no field for a bank: the
  submitting side comes from the port, and the counterparty's is derived from the
  counterparty's IBAN through the submitting bank's own copy of the routing
  directory. There is nothing to type wrongly.
- **The mid-flight rollback claim is unwitnessed at a cut-off.** No unit of work
  there reaches two institutions, so there is no seam at which a unit of work
  writes in one institution's book and then discovers a problem in another's. The
  claim is carried by `payment`'s
  `TestAFailedReversalRollsBackTheWholeRejection`, on the seam that still has a
  multi-write unit of work.
