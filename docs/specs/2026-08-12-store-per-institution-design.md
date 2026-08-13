# Design — separating the store by institution

**Built.** The ruling is the store-per-institution design; this
record is the plan it was built from and is kept for the measurements and the
method lists. Two things came out differently:
`SlotAccountTx` is called `ledger.SlotTx`, because `Book.SlotAccountTx` is
already a method and a parameter of the same name reads badly; and `OpenBank`
takes a `BookID` rather than a BIC, because the four layers below payment open a
bank's schema under a book of their own and know nothing of institutions.

---

Based on `main` at `04c1fca`, which is the rule of one type per institution
landed: `payment.Network` is now three types, one per institution, and an act
that belongs to one of them cannot be named through another's handle.

This is the same defect one layer down, and the rule of one type per institution's own Consequences section
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
that three documents and the rule that a queue is a table whose bytes stay opaque
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

## `ledger.Tx` splits too, and that is the finding

The obvious composition — `BankTx = ledger.Tx + deposit.Tx + …` — does not work,
and taking `ledger.Tx` apart is what says why.

Its 29 declared methods are three things, not one:

| | methods | who holds the tables |
| --- | --- | --- |
| the ledger proper | 22 | a bank and the settlement agent |
| slot accounts | 3 | **a bank alone** — `slot_accounts` is created only by the bank schema, and the other two say so in a comment about its absence |
| `NextID`, `Now`, `AppendAudit`, `ListAudit` | 4 | **all three** — `audit_events` and `id_sequences` are in every schema, and neither carries a guard |

The second row is a small thing. The third is not:

**The clearing house writes its audit trail through a ledger interface, and it
has no ledger.** `AppendAudit` reaches it today only because `payment.Tx` embeds
`deposit.Tx` embeds `product.Tx` embeds `ledger.Tx`. A `CsmTx` that embedded
`ledger.Tx` to keep its audit trail would be re-creating the defect this design
removes; one that did not embed it would lose the audit trail.

So there is a fourth shared capability, and it is the one every institution
needs: `CommonTx` — `NextID`, `Now`, `AppendAudit`, `ListAudit`. It comes out of
`ledger.Tx`, which means **this touches the `ledger` package and not only
`payment`.** That is the main scope surprise in this record.

## The design

**One `Tx` per institution, composed from capability interfaces.**

| capability | methods | held by | status |
| --- | --- | --- | --- |
| `CommonTx` | 4 | all three | **new**, out of `ledger.Tx` |
| `ledger.Tx` (narrowed) | 22 | bank, settlement agent | exists, loses 7 |
| `SlotAccountTx` | 3 | bank | **new**, out of `ledger.Tx` |
| `ebics.Tx` | 8 | clearing house, settlement agent | exists, correct already |
| `PaymentRowsTx` | 4 | bank, clearing house | **new**, out of `payment.Tx` |
| `deposit.Tx` + `product.Tx` + `lending.Tx` | 29 | bank | exist, correct already |
| bank's own payment rows | 12 | bank | out of `payment.Tx` |
| clearing house's own | 14 | clearing house | out of `payment.Tx` |
| settlement agent's own | 12 | settlement agent | out of `payment.Tx` |

```
BankTx        = CommonTx + ledger.Tx + SlotAccountTx + deposit.Tx + product.Tx
                         + lending.Tx + PaymentRowsTx + 12
CsmTx         = CommonTx + ebics.Tx + PaymentRowsTx + 14
CentralBankTx = CommonTx + ledger.Tx + ebics.Tx + 12
```

Reach from one `*sqlite.Store`, which today exposes all 108 whatever shape it was
opened as:

| | today | after |
| --- | --- | --- |
| a bank | 108 | **74** |
| the clearing house | 108 | **30** |
| the settlement agent | 108 | **46** |

`payment.Store` splits the same way — `Update`/`View` differ only in the `Tx`
they hand the closure — and `Stores.Bank`, `Stores.ClearingHouse` and
`Stores.CentralBank` already return one per institution, so their three
signatures change and the call sites follow. This is the shape the rule of one type per institution used and
it worked: 83 receivers moved, and the compiler found every crossing.

**`ebics` needs no work beyond the constructor.** `Set.ClearingHouseEBICS` and
`Set.CentralBankEBICS` already hand it to those two and to nobody else; what is
wrong is only that `(*sqlite.Store).EBICS()` exists on all three, which typed
constructors remove.

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
the rule of one type per institution's move applied to the
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

## The method lists

Derived from the tree, not from judgement: each name below is a method whose
`inShape` guard names a table, bucketed by which schemas create that table. A new
method appearing in none of these lists is one nobody classified.

**`CommonTx` (4) — out of `ledger.Tx`, held by all three**

`NextID` · `Now` · `AppendAudit` · `ListAudit`

**`SlotAccountTx` (3) — out of `ledger.Tx`, a bank's alone**

`GetSlotAccount` · `ListSlotAccounts` · `PutSlotAccount`

**`ledger.Tx` after narrowing (22) — a bank's and the settlement agent's**

`BookBalance` · `GetAccount` · `GetLedger` · `GetSubledger` · `GetTransaction` ·
`GetTransactionByIdempotencyKey` · `ListAccounts` · `ListLedgers` ·
`ListSubledgers` · `ListTransactions` · `ListTransactionsForPosition` ·
`LockAccounts` · `MarkReversed` · `NextAccountSeq` · `NextSubledgerBlock` ·
`PutAccount` · `PutLedger` · `PutSubledger` · `PutTransaction` ·
`SubsidiaryBalances` · `ValueDateBalance` · `ValueDatedSeries`

**`PaymentRowsTx` (4) — out of `payment.Tx`, a bank's and the clearing house's**

`GetPayment` · `GetPaymentByEndToEndID` · `ListPayments` · `PutPayment`

**A bank's own, out of `payment.Tx` (12)**

`GetBank` · `GetDirectoryEntry` · `GetMandate` · `GetSettlementAdvice` ·
`ListBanks` · `ListDirectoryEntries` · `ListMandates` · `ListSettlementAdvices` ·
`PutBank` · `PutMandate` · `PutSettlementAdvice` · `ReplaceRoutingDirectory`

**The clearing house's own, out of `payment.Tx` (14)**

`AddHeldFile` · `DeleteHeldFile` · `DeleteHeldReturn` · `GetCycle` ·
`GetHeldReturn` · `GetOpenCycle` · `GetRosterEntry` · `GetRosterEntryByIssuer` ·
`ListCycles` · `ListHeldFiles` · `ListRosterEntries` · `PutCycle` ·
`PutHeldReturn` · `PutRosterEntry`

**The settlement agent's own, out of `payment.Tx` (12)**

`GetBankCode` · `GetBankCodeForBIC` · `GetSettlement` · `GetSettlementByCycle` ·
`GetSettlementMember` · `ListBankCodes` · `ListSettlementMembers` ·
`ListSettlements` · `NextBankCodeSerial` · `PutBankCode` · `PutSettlement` ·
`PutSettlementMember`

**`ebics.Tx` (8) — already right, the clearing house's and the settlement agent's**

`AddOrder` · `AddQueuedFile` · `AnswerOrder` · `DeleteQueuedFiles` ·
`ListAcknowledgements` · `ListPendingOrders` · `ListQueuedFiles` · `NextOrderSeq`

The remaining 29 bank-only methods are already their own interfaces and move
whole: `deposit.Tx` (15), `product.Tx` (6), `lending.Tx` (8).

## Tasks

Ordered, and 1–4 land together: none is a win on its own, task 1 is a pure
no-op until task 3 uses it, and task 4 is what lets task 5 delete the guard
outright rather than leave it alive on the `Open` seam.

1. **Split `ledger.Tx`.** `CommonTx` (4) and `SlotAccountTx` (3) come out;
   `ledger.Tx` keeps 22 and embeds `CommonTx`. `ledger.Book` gains
   `SlotAccountTx` where it uses those three. Everything still compiles because
   every current holder embeds all of it.
2. **Extract `PaymentRowsTx`** (4) from `payment.Tx`, embedded straight back in.
   No behaviour change.
3. **Three `Tx` interfaces and three `Store` interfaces** in `payment`, composed
   as the table above. `payment.Tx` and `payment.Store` retire as names.
   `Stores`' three methods change return type; the compiler finds the rest. The
   `sqlite` side keeps ONE `tx` struct and one `Store` struct — three interfaces
   over one implementation.
4. **Typed constructors.** `OpenBank(ctx, bic, path, clock)`,
   `OpenClearingHouse(ctx, path, clock)`, `OpenCentralBank(ctx, path, clock)`
   replace `Open`'s `Shape` parameter and its `book` argument. `Set` builds each
   through its own. `(*sqlite.Store).EBICS()` moves to the two types that have a
   queue.
5. **Delete `inShape` and `ErrNotInThisShape`** — 104 call sites and the error.
   `Shape` stays unexported-in-effect, carrying the migration directory and
   `holds` for `Reset`; `paymentLegs` and `paymentCycle` stay.
6. **Point the suite runners at their institution's store type** and correct the
   documentation below.

## Verification

Each of these is a command, not a judgement.

- `go build ./... && go vet ./... && go test ./...` and `gofmt -l .` — the
  baseline, green before and after.
- **The crossing is a build failure, per institution.** Three throwaway probes,
  the shape the rule of one type per institution used: a file naming `PutCycle` on a `BankTx`, `PutBank` on a
  `CsmTx`, `PutPayment` on a `CentralBankTx`. Each must fail to compile, and the
  probe is deleted after. Nothing else proves the win.
- **`ErrNotInThisShape` has no callers.**
  `grep -rn 'ErrNotInThisShape\|inShape' --include='*.go' .` returns nothing
  outside its own deletion.
- **`Reset` still empties every table.** `store/sqlite`'s existing reset cases
  cover this and must stay green without change — if one needed editing, `holds`
  was damaged.
- **The two column flags still branch.** `TestSchemaArgumentsReachSqliteMaster`
  and the payment round-trip cases in `RunPayment` and `RunClearingHousePayment`
  cover a bank's legs and the clearing house's cycle respectively.
- **Nothing gained a second implementation.** `grep -c 'func (t \*tx)'` over
  `store/sqlite/tx_*.go` should total what it did before, on one `tx` struct.

## Documentation layers

This is a store change, so the schema comments are domain content and move with
it (`CLAUDE.md`, *Domain knowledge stays consistent across layers*).

- `CLAUDE.md` — the *N+2 stores* section names `ErrNotInThisShape` as how a
  method reaching a table its schema does not create is refused. After task 5
  that refusal is a build failure and the sentence is wrong.
- `store/sqlite/schema/{bank,csm,centralbank}/0001_init.sql` — each argues about
  tables the shape does NOT hold. Those arguments stay true and gain a second
  mechanism; `bank/0001_init.sql` is the canonical home per `CLAUDE.md`.
- the rule that a queue is a table whose bytes stay opaque — leans on the
  shape refusal for the queue. Check whether its Consequences still hold when the
  queue is on two types and no third can name it.
- `README.md` *Persistence* — states the three-shape mapping.
- The learner-facing layers (`hint-content.ts`, quiz chapters 15–16) name no repo
  symbol, so they move only if a DOMAIN claim changes. None does here: which
  institution holds which table is unchanged, and only what enforces it moves.

## What this does not do

**It does not split the implementation.** One `tx` struct, one `Store` struct,
one body of SQL, three interfaces over them. See the rejected alternative below.

**It does not make `Shape` disappear.** `Reset` needs `holds`, the migrations
need `dir`, and `paymentLegs`/`paymentCycle` are finer than any interface.

**It does not touch the databases.** N+2 before and after, three schemas before
and after, no migration.

## Alternatives rejected

**Leave it and add the missing tests.** Candidate 4 of the review proposed
exactly this, and it is the cheapest thing on the board — assertions that
`ErrNotInThisShape` fires, converting a claim into a checked fact. It is
strictly weaker: a tested runtime refusal is still a runtime refusal, and the
whole argument of the rule of one type per institution is that the compiler should be holding this. It is
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
