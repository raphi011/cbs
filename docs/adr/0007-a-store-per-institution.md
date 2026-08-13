# ADR-0007: A store per institution, and the shape is a constructor

**Status:** accepted
**Date:** 2026-08-12
**From:** [the store-per-institution design](../specs/2026-08-12-store-per-institution-design.md) — `payment.Tx` was one interface over three schemas

## Context

[ADR-0006](0006-one-type-per-institution.md) made an institution's ACTS a type
and named what it left behind: `sqlite.ErrNotInThisShape` refused at runtime what
the types refuse at compile time one layer up. This is that defect, one layer
down.

`payment.Tx` was 100 methods over three schemas — 42 its own, 58 embedded from
`deposit.Tx`, `lending.Tx` and through them `product.Tx` and `ledger.Tx`. Every
institution's store implemented every one of them, so **the clearing house's
transaction carried `GetDepositAccount`, `PutFacility` and `PutProductVersion`**.
A clearing house has no customers, no products, no loans and no book of accounts
at all.

104 method bodies opened with an `inShape` call answering `ErrNotInThisShape`
when the table was not there. **Nothing tested it.** Emptying `inShape` to
`return nil` left the entire suite green — there was not one assertion that the
refusal fired, so the mechanism three documents and
[ADR-0004](0004-a-queue-is-a-table-and-stays-opaque.md) leaned on was a claim.

`Open` took the shape as a runtime value and returned one `*Store` type whatever
it was handed, so even splitting the transaction interface would have left the
mismatch at that seam: open the clearing house's schema, ask for a bank's store,
and every bank-only method reaches a table that is not there.

## Decision

**One transaction type and one store type per institution, composed from the
capabilities two institutions genuinely share.**

```
BankTx        = deposit.Tx + lending.Tx + PaymentRowsTx + 12
CsmTx         = ledger.CommonTx + PaymentRowsTx + 14
CentralBankTx = ledger.Tx + 12
```

Three shared capabilities came out of the layers underneath, and the first is the
finding:

- **`ledger.CommonTx`** — `NextID`, `Now`, `AppendAudit`, `ListAudit`. Every
  institution has them, over `id_sequences` and `audit_events`, which every
  schema holds. **The clearing house keeps an audit trail and has no ledger**, so
  a `CsmTx` embedding `ledger.Tx` to reach the audit would have re-created the
  defect and one that did not would have lost the trail.
- **`ledger.SlotTx`** — the slot mapping, a bank's alone, since `slot_accounts`
  is in the bank schema only. `ledger.BankTx` is the pair a bank holds.
- **`payment.PaymentRowsTx`** — a party's own copy of the payments it is on: a
  bank's and the clearing house's, and no third institution's.

**The shape is a constructor rather than a parameter.** `OpenBank`,
`OpenClearingHouse` and `OpenCentralBank` each return the type whose methods that
schema can answer. Two take no book, because there is one clearing house and one
settlement agent and each answers for a constant.

`inShape` and `ErrNotInThisShape` are deleted outright.

## Consequences

**A crossing is a build failure.** `PutCycle` on a `BankTx`, `PutBank` on a
`CsmTx`, `PutPayment` or `GetDepositAccount` on a `CentralBankTx`: each fails to
compile. Reach from one store goes 108 methods to **69 / 21 / 34**, measured — `NumMethod`
on the three seams, which is how to re-take it.

**The implementation did not split.** One `tx` struct, one store body, one set of
statements, three interfaces over them, and one method fewer on `*tx` than
before: `inShape` itself. `CLAUDE.md`'s "nothing cross-checks the SQL" is an
argument for less SQL, not more.

**`Network` shed what only some institutions have.** The core keeps the clock,
the identity, the schemes and the audit trail. A payment's row is not that — the
settlement agent keeps none — so `GetPayment` and `ListPayments` are a method on
each of the two that do, and `CompleteReturnTx`, which both perform identically,
takes the shared capability as its argument. The four layer views moved to
`BankNetwork` and the reserve book to `CentralBankNetwork`.

**`Shape` survives and refuses nothing.** It carries the migration directory, the
table list `Reset` empties, and `paymentLegs`/`paymentCycle` — the two places
where two schemas hold the SAME table with different columns. So the honest claim
is: **the type refuses a table an institution does not have, and the
implementation still decides which columns it writes.**

**The mis-wired handles lost a case.** `export_test.go` can no longer give a
`BankNetwork` the clearing house's database, because the two store types differ.
What is left to measure is a handle whose methods and whose identity disagree,
which is what `Network.self` and `Network.centralBankBook` refuse.

**The recorder is three decorators.** `cmd/server/books_test.go` wraps one
transaction type per institution and walks one chain per institution, because
there is no single interface left to write one decorator against.

## What it costs

**Two accessors on a bank's store.** `Ledger()` is the `ledger.Store` a `Book`
takes — the ledger BOTH institutions that keep a book of accounts have — and
`BankLedger()` is the wider `ledger.BankStore` with the slot mapping. A `Book` is
shared with the settlement agent, so it takes the narrow one even for a bank; the
mapping is reached through the transaction a deposit or lending act already
holds.

**`OpenBank` takes a `BookID` and not a BIC**, which the design record had the
other way round. A bank IS its book and that id is its BIC, and `Set.bank` is
where the two meet — but the `ledger`, `deposit`, `product` and `lending` suites
open a bank's schema under a book of their own and know nothing of institutions.
Validating a BIC in the constructor would have made those four suites pretend to
be one.

**Eight small duplicated wrappers.** `GetPayment`, `ListPayments`,
`CompleteReturn` and `Store` are a method on each institution that has one rather
than one method on the core. The bodies are three lines each; what the
duplication buys is that a `CentralBankNetwork` cannot name them at all.

## Alternatives rejected

**Leave it and add the missing tests** — assertions that `ErrNotInThisShape`
fires, turning a claim into a checked fact. It is the cheapest thing on the board
and strictly weaker: a tested runtime refusal is still a runtime refusal, and
this record deletes the error rather than most of it.

**Split `Tx` and keep `Open`'s shape parameter.** Three interfaces over a store
whose schema is still chosen at runtime means one seam left where a mismatch is
possible, and a guard kept alive for a crossing three constructors remove.

**One `Tx` with the institution as a type parameter.** Go generics cannot vary a
method SET by parameter, only the types inside one.

**Three separate store packages.** There is one implementation of every method
and all three share it — that is what makes a crossing "a table that is not
there" rather than a second driver's worth of behaviour to keep in step.
Splitting the packages would split the SQL too.

**Two `PutPayment` implementations instead of `paymentLegs`/`paymentCycle`.**
Deliberately left open. It would replace a bool branch with a second copy of one
statement, and the branch stays unless something else argues it down.
