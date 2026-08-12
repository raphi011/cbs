# payment — measured findings

Evidence behind claims made in `payment`. See [README](README.md) for what
belongs here.

## An acknowledgement quoting no admission defeats both guards

Behind `TestAnAcknowledgementQuotingNoAdmissionIsRefusedByBothActs`. Measured on
the acts, with the four guard lines removed:

    PROBE1 after an ack with no ref: err=<nil> ref="" settlement="200.100.001"
    PROBE2 forged ack err=<nil> -> settlement="acc_bogus" ref="someone-elses-admission"
    PROBE3 second institution quoting an empty ref on the same BIC: err=<nil>

An empty `Bank.AdmissionRef` means "this bank has accepted nothing yet", which is
what lets a founded bank take the first acknowledgement naming it. An empty
`RosterEntry.AdmissionRef` compares equal to any other empty one. So the first
line is a RESET — an admitted bank indistinguishable from one that has accepted
nothing — the second is the overwrite that reset reopens, and the third is the
same hole seen by the clearing house: two institutions on one BIC, both quoting
nothing, comparing equal.

## An acknowledgement naming no account burns the bank's admission reference

Behind `TestAnUnusableAcknowledgementIsRefusedByBothActs`.

    PROBE6  bank, ack with NO accounts:   err=<nil> ref="impostor-adm" settlement=""
    PROBE6b the REAL ack then arrives:    err=…recorded its membership under "impostor-adm"…
    PROBE7  roster, ack with NO accounts: err=<nil> entry={BIC:… Assets:[] AdmissionRef:impostor-adm}
    PROBE7b the REAL ack then arrives:    err=…is admitted under "impostor-adm"…

The row it writes has nothing in it, which reads like doing nothing. Measured, it
records a membership that settles through no account and a roster entry that
clears in no scheme, and then refuses the true acknowledgement for ever after —
by the admission-reference guard each of those rows now carries.

The two owner rows of that table, an absent BIC and a malformed one, were both
measured writing a roster entry keyed by a BIC nothing can address. Keying a row
by a value is not checking it.

## An account MOVED under the admission's own reference

The third arm, behind `TestABankRefusesAnAcknowledgementThatWouldLeaveItWrong`.
`ErrBankAlreadyAdmitted` compares the reference and says nothing when the two
match, so the loop wrote whatever arrived. Measured on a healthy member:

- `DepositTx` then answered a bare "account not found";
- `ReserveBalance` went on reporting the healthy reserve, off the settlement
  agent's untouched row;
- re-driving the admission was refused as "already a member".

## Returning a diverted payment was a money bug

Behind `TestReturningAPaymentThatSettledIntoUnclaimedBalancesReleasesTheLiability`.

The diversion to unclaimed balances makes "the payee's bank was paid" and "the
payee was paid" two different facts. A return that debited the payee's GL account
debited one that, for a diverted payment, had never been credited. Measured
before `Payment.CreditorLegAccount` existed:

    AFTER SETTLE:  bankB unclaimed=30000  bob=0      bankB reserve=30000
    AFTER RETURN:  bankB unclaimed=30000  bob=-30000 bankB reserve=0

Three things wrong at once. Bob's CLOSED account held minus 30000 — an overdraft
nobody granted, on an account that can neither be drawn on nor closed again. The
unclaimed-balances liability was never released, so the bank still owed money it
had just paid back. And the reserves went out anyway, which is the half that made
the other two invisible: two liabilities that net to zero keep the book balanced,
so no ledger guard fires, and `checkSufficientBalance` does not refuse a
Liability going negative — that is what an overdrawn deposit IS.

The fix is a fact recorded at settlement rather than re-derived at return:
`PostCreditorLegTx` writes the account it actually credited onto the payment. It
cannot be re-derived, and that is the substance — an account open at settlement
and closed afterwards looks identical, today, to one closed at settlement.

## The settlement amount bound

Behind `TestSettlementMessageAmountBound`. The bound is **9,223,372,036,854,775**
minor units, which is `MaxInt64/1000`. The thousand is `iso20022`'s `Validate`
padding a two-decimal fraction out to five places before parsing it as an int64.

It is NOT the standard's `totalDigits="18"`. A legal seventeen-digit amount is
refused anyway, and that over-refusal is asserted so it is a tested property
rather than a surprise. Widening `Validate` to the standard's real ceiling would
flip that case.

A test at `math.MaxInt64` proves nothing here: it is orders of magnitude past
every candidate threshold, so it passes whatever the real bound is. A number
stated in prose needs a test that would fail if the number were wrong, which
means testing the pair astride it.

## Guarantees this system does not have

- **A cross-bank IBAN collision is representable and nothing refuses it.**
  Uniqueness is enforced per bank, the widest scope a register can see. Only a
  sweep over every bank's register could see both, and no bank may make one. The
  collision takes a duplicate ALLOCATION — a defect in an allocation rather than
  in a register — which is why the instrument that catches it (`payment/recon`)
  has to open every institution's database at once. See
  `TestACrossBankCollisionTakesADuplicateAllocation`.
- **A repeated scalar child inside one envelope is LAST-WINS.** `encoding/xml`
  silently replaces a second `BizMsgIdr` or `MsgDefIdr` rather than refusing it,
  so two banks reading the same bytes with different parsers can disagree about
  which value was sent, and this codec takes the last. Every actor here decodes
  through the same function, so they agree with each other; interoperability with
  a foreign parser is not what the envelope is for.
- **The mid-flight rollback claim is unwitnessed at a cut-off.** No unit of work
  there reaches two institutions. The claim is carried by
  `TestAFailedReversalRollsBackTheWholeRejection`, on the seam that still has a
  multi-write unit of work.
