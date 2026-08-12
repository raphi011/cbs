# Design — separating the store by institution

Based on `main` at `04c1fca`, which is [ADR-0006](../adr/0006-one-type-per-institution.md)
landed: `payment.Network` is now three types, one per institution, and an act
that belongs to one of them cannot be named through another's handle.

This is the same defect one layer down, and ADR-0006's own Consequences section
names it: `sqlite.ErrNotInThisShape` refuses at runtime what a type refuses at
compile time above it.

## The defect, stated once

**`payment.Tx` is one interface over three schemas.**

Its own doc admits why the refusal exists:

> It exists because `payment.Tx` is ONE interface over three schemas. Go has no
> way to give the clearing house a `Tx` without `PutBank` on it, so every shape
> implements every method and two thirds of them have nothing to write to.

The sharpest statement of it: **the clearing house's transaction type carries
`GetDepositAccount`, `PutFacility` and `PutProductVersion`.** A clearing house
has no customers, no products, no loans and no book of accounts at all.

`payment.Tx` is 100 methods — 42 its own, and 58 embedded:

| embedded | methods | who actually holds the tables |
| --- | --- | --- |
| `ledger.Tx` | 29 | a bank and the settlement agent |
| `deposit.Tx` | 15 | a bank alone |
| `product.Tx` | 6 | a bank alone |
| `lending.Tx` | 8 | a bank alone |
| `payment.Tx`'s own | 42 | all three, in three disjoint groups |

Every shape implements all 100. 104 method bodies open with an `inShape` call
that answers `ErrNotInThisShape` when the table is not there.

**And nothing checks it.** Emptying `inShape` to `return nil` leaves the entire
suite green — there is not one assertion that the refusal fires. The mechanism
that three documents and [ADR-0004](../adr/0004-a-queue-is-a-table-and-stays-opaque.md)
lean on is, today, a claim.

## What the partition actually is

Measured over the 104 guarded methods, by which schemas hold the tables each one
touches:

| group | methods | tables |
| --- | --- | --- |
| bank only | 44 | deposit accounts, holds, snapshots, products, facilities, mandates, directory, `banks`, advices |
| **bank + settlement agent** | 22 | the ledger — `books`, `accounts`, `transactions`, `entries` |
| clearing house only | 14 | `cycles`, roster, held files, held returns |
| settlement agent only | 12 | `settlements`, members, bank codes |
| **clearing house + settlement agent** | 8 | `ebics_queue`, `ebics_orders` |
| **bank + clearing house** | 4 | `payments` |

Nothing spans all three except `audit_events` and `id_sequences`, which every
schema holds and which carry no guard at all — correctly, because there is no
crossing to refuse.

The three overlaps are not leftovers. Each is one coherent capability that two
institutions genuinely share, and two of them are **already separate interfaces
in the tree**: `ledger.Tx` and `ebics.Tx`. Only the payment rows are not.

## The design

**One `Tx` per institution, composed from capability interfaces.**

```
BankTx        = ledger.Tx + deposit.Tx + lending.Tx + PaymentRowsTx + bankOnly(44)
CsmTx         = ebics.Tx  + PaymentRowsTx + clearingHouseOnly(14)
CentralBankTx = ledger.Tx + ebics.Tx     + settlementAgentOnly(12)
```

Reach, against 100-for-everyone today:

| | today | after |
| --- | --- | --- |
| a bank | 100 | **70** |
| the clearing house | 100 | **26** |
| the settlement agent | 100 | **42** |

`payment.Store` splits the same way — `Update`/`View` differ only in the `Tx`
they hand the closure — and `Stores.Bank`, `Stores.ClearingHouse` and
`Stores.CentralBank` already return one per institution, so their three
signatures change and the call sites follow. This is the shape ADR-0006 used and
it worked: 83 receivers moved, and the compiler found every crossing.

**`PaymentRowsTx` is the one new capability.** Four methods over the `payments`
table, shared by a bank and the clearing house because each keeps its own copy of
every payment it is a party to. It is the store-layer twin of the 15 acts
ADR-0006 left on the network core, and for the same reason.

## `ErrNotInThisShape` goes, and `Open` is why

Splitting `Tx` alone would leave the error exactly one caller, and it is worth
naming because it is the reason to go further rather than a reason to keep it.

`Open` takes the shape as a RUNTIME VALUE and returns one `*Store` type whatever
it is handed. So three `Tx` interfaces mean three accessors on that one type,
with nothing connecting `Open(…, CSM, …)` to which accessor a caller may then
use. Open the clearing house's shape, ask for the bank's store, and every
bank-only method reaches a table that is not there. One mismatch at one seam —
but a runtime one, which is the thing this design exists to remove.

**So the shape becomes a constructor rather than a parameter**, which is
[ADR-0006](../adr/0006-one-type-per-institution.md)'s move applied to the
discriminator one layer down:

```go
OpenBank(ctx, bic, path, clock)      (*BankStore, error)
OpenClearingHouse(ctx, path, clock)  (*ClearingHouseStore, error)
OpenCentralBank(ctx, path, clock)    (*CentralBankStore, error)
```

`inShape` and `ErrNotInThisShape` then have **no callers at all** and both go.
It is cheap: 12 `Open` call sites, three of them outside tests.

It also retires a pairing that is currently only a convention. Every call site
today pairs a shape with a fixed book — `CSM` with `ClearingHouseBook`,
`CentralBank` with `CentralBankBook`, `Bank` with the BIC — and nothing enforces
it. `OpenClearingHouse` needs no book argument, and `OpenBank` derives the book
from the BIC, which is the same string (see `payment.AsBank`).

**`Shape` itself survives, as an internal value.** It carries the migration
directory and `holds`, which is what `Reset` empties. `Reset` never returned this
error and does not need it.

## What no type can express, and must stay

`Shape.paymentLegs` and `Shape.paymentCycle` are the two places where two shapes
hold the SAME table with DIFFERENT columns: a bank's `payments` carries legs and
no cycle, the clearing house's carries a cycle and no legs. They are column-list
branches inside the payment statements (`tx_payment.go`), not `inShape` calls, so
they never produced `ErrNotInThisShape` and nothing above removes them.

So the honest claim after this is: **the type refuses a table an institution does
not have; the implementation still decides which columns it writes.**

Whether those two flags should instead become two `PutPayment` implementations on
two store types is deliberately left open. It would replace a bool branch with a
second copy of one statement, and `CLAUDE.md`'s "nothing cross-checks the SQL" is
an argument for having less SQL rather than more. The branch stays unless
something else argues it down.

## `storetest` — the constraint, and a correction

This was the part expected to be expensive. It is not, and the reason is a claim
that turns out to be false.

**`CLAUDE.md`, `store/storetest/storetest.go` and
`store/sqlite/conformance_test.go` all say the three store shapes each run the
shared suite. They do not.** `RunLedger`, `RunDeposit`, `RunProduct`, `RunLending`
and `RunPayment` are all handed a BANK-shape store by
`conformance_test.go`'s `newBank`. The clearing house and the settlement agent
are exercised by `RunClearingHousePayment` and `RunCentralBankPayment`, which are
**separate function bodies**, not the same suite re-run. About four fifths of
`storetest` runs against the bank shape and no other.

That is a documentation defect worth fixing on its own. For this design it is
good news: the suite is **already organised per institution**, so splitting the
interface splits along a line the tests are already drawn on. `RunPayment` takes
a `BankTx` store, `RunClearingHousePayment` a `CsmTx` store,
`RunCentralBankPayment` a `CentralBankTx` store, and each stops being able to
name what its institution does not have.

`storetest` remains what `CLAUDE.md` says it is — the shared suite, written
against interfaces, naming no table and no dialect. What changes is that "shared"
becomes true of the capability suites (`ledger`, `deposit`, `product`, `lending`,
payment rows) and the institution suites stop pretending to be shared.

## Phasing

1. **Extract `PaymentRowsTx`.** Four methods out of `payment.Tx` into their own
   interface, embedded back into it. No behaviour change, nothing else moves.
2. **Three `Tx` interfaces and three `Store` interfaces**, composed as above,
   with `payment.Tx` retired as a name. `Stores`' three methods change return
   type; the compiler finds the rest. This is the phase that is mostly mechanical
   and entirely compiler-guided.
3. **Typed constructors, and delete the guard.** `OpenBank`,
   `OpenClearingHouse` and `OpenCentralBank` replace `Open`'s shape parameter, so
   the 104 `inShape` calls and `ErrNotInThisShape` go together rather than the
   calls going and the error surviving on one seam. `Shape` stays internal, for
   the migration directory and `Reset`; `paymentLegs`/`paymentCycle` stay.
4. **Correct the three documents** that claim three shapes run one suite, and
   point the suite runners at their institution's store type.

Phase 3 is where the win is banked and phase 1 is a no-op on its own, so the
order matters: 1 and 2 land together or not at all.

## Alternatives rejected

**Leave it and add the missing tests.** Candidate 4 of the review proposed
exactly this, and it is the cheapest thing on the board — assertions that
`ErrNotInThisShape` fires, converting a claim into a checked fact. It is
strictly weaker: a tested runtime refusal is still a runtime refusal, and the
whole argument of ADR-0006 is that the compiler should be holding this. It is
worth doing FIRST if phase 2 is not going to start soon, and worth skipping if it
is, because phase 3 deletes the error outright rather than most of it.

**Split `Tx` and keep `Open`'s shape parameter.** This is the version that leaves
`ErrNotInThisShape` alive on one seam, and it is where this record first landed.
Three interfaces over a store whose shape is still chosen at runtime means a
guard nothing else needs, kept for a crossing three constructors remove.

**One `Tx` with the institution as a type parameter.** Go generics cannot vary a
method SET by parameter, only the types inside one, so this expresses nothing the
current interface does not.

**Three separate store packages.** There is one implementation of every method
and it is shared by all three shapes — that is what makes a crossing "a table
that is not there" rather than a second driver's worth of behaviour to keep in
step. Splitting the packages would split the implementation too, and
`CLAUDE.md`'s "nothing cross-checks the SQL" is an argument against having more
of it, not less.
