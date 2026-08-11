# ADR-0002: The clearing house settles before it releases

**Status:** accepted
**Date:** 2026-08-11
**Sub-project:** 21, task 8 — [EBICS and the business day](../specs/2026-08-11-ebics-and-the-business-day-design.md)

## Context

`docs/sepa-real-world.md:207` states the order plainly:

> the cycle settles in T2 before STEP2 releases the output files

This system did the opposite. A receiving bank was handed a payment as soon as
the clearing house had validated it, answered `ACCP` or `RJCT` out of its own
register, and was told much later that the cycle had settled. The bank had looked
at the payment, and could refuse it, before any money existed behind it.

That is not a small reordering. It decides which institution can say *no*, and
therefore which ISO 20022 code every account-level problem carries.

## Decision

**A receiving bank is handed its instructions only once the funds behind them are
final.**

The clearing house sorts each submitted file by creditor agent and **holds** each
receiving bank's share. Nothing is queued for a receiving bank until the cycle
carrying it has settled at the settlement agent, and a cut-off the agent refuses
releases nothing at all. Three phases of the business day, in this order and for
this reason:

```
3  csm  net every open cycle -> upload the settlement instruction
4  cb   settle whole-or-nothing -> statement per member -> answer the csm
5  csm  collect the answer -> RELEASE the output files
```

## Consequences

**No bank ever credits a customer against a batch that might still fail.**
Reversing the order invents settlement risk that nothing in the system creates
otherwise: a net payer short of reserves is refused *after* several banks have
already paid their customers, and no message recovers money that is in a
customer's account.

**A receiving bank cannot reject, and its refusals become returns.** By the time
it looks at a payment the money is in its own clearing suspense. So `AC01` for an
address it does not hold and `AM04` for a payer it cannot collect from are
`pacs.004` **returns**, not `pacs.002` rejections — which is what those codes are
in SEPA, and for exactly this reason. The rule book's own division is *before
settlement, reject; after settlement, return*, and this ordering is what puts
every receiving bank's objection on the second side of that line.

**The only institution that rejects anything is the clearing house,** and it does
so before any money has moved: an unroutable destination (`RC01`), a member its
roster does not admit for the scheme's asset, a scheme with no cut-off window
open (`TM01`).

**A payee whose account is closed is taken on rather than sent back.** There is no
answer of *no* left to give, so the receiving bank parks the money — unclaimed
balances on a push, returns receivable on a pull — and then asks for a return. A
refusal that discriminated instead would have made the entire unclaimed-balances
ageing feature unreachable. The refusal still bites at the **submitting** bank,
where it is about that bank's own customer.

**A bookkeeping failure must not become a return.** A return sends money back
across a network, so it may only be sent for a judgement about the *instruction*
— a closed account, a shortfall — and never for a dropped database connection.
`ReasonFor` cannot tell the two apart, because it always produces a code and
`MS03` is its floor; `payment.Answerable` is the discrimination, and the receiving
bank's handler branches on it.

**The share is cut twice: built at ingestion, and cut again at release.** What is
held is a set of *positions in the submitter's document*, not a rendered file, so
a payment an operator rejects out of an open cycle is filtered out when the rest
of its cycle settles. Holding a rendered file would release a transaction the
network had already told a bank it would not carry.

**A cut-off with nothing to settle is discharged by the clearing house itself.**
Release is gated on the settlement agent's answer, and there are cut-offs it is
never asked about: a cycle whose members' positions all cancel, and one that took
nothing in, have no leg to send, and a settlement instruction with no transaction
is not a message. Nothing would ever release them, so the institution that netted
the batch settles it where it stands — phase 3b, before the settlement agent is
worked at all. No reserve moves and none needs to: every position is zero, so
each bank's clearing suspense is emptied by the payments it receives in the same
batch. **No settlement is recorded anywhere against such a cycle**, which the
reconciliation harness has to know before it reads a settled cycle with no
settlement against it as a break (`payment.NetsToNothing`). A cycle with a
position to discharge is never settled this way, whatever became of its
instruction: one the agent refused is the operator's to re-instruct.

**A settled direct debit is where the argument is clearest.** The payer's bank
was net-debited at the cut-off whatever its customer's balance turns out to be,
so `AM04` — a shortfall no other institution can see — is discovered with the
money already gone. It cannot refuse. It stands in for the payer out of its own
pocket and asks for the money back.

## What it costs

**The held files are not durable.** They are in the clearing house's memory,
keyed by cycle, and a restart between settlement and release loses them: the
reserves have moved and no receiving bank is ever handed the instructions it has
to apply. This is recorded rather than fixed, because the fix is a table in that
institution's own database and not a workaround anywhere else. The absence is
argued inside `csm/0001_init.sql`'s `cycles` statement. The same is true of a
`pacs.004` held between its upload to the settlement agent and the answer coming
back.

**One more round trip before a payee is paid.** The chain is eight files rather
than six, and a payee's bank learns of a payment strictly later than it used to.
That is the real system's shape and not a cost to be optimised away.

## Alternatives rejected

**Release on validation, settle afterwards, and unwind on failure.** It needs a
way to claw back credits already made to customers at several banks at once — an
unwind across N institutions' books, which is precisely what settlement finality
exists to make unnecessary. The EU's Settlement Finality Directive (98/26/EC) has
this as its subject.

**Release on validation and let receiving banks reject.** This is what the system
did. It kept `AC01` as a rejection, which is wrong about SEPA, and it made
"accepted by the payee's bank" a state that meant nothing — the money behind it
had not moved and might never.

**Hold the rendered output file rather than positions in the submitter's.**
Simpler to release, and it releases transactions an operator has since rejected.
