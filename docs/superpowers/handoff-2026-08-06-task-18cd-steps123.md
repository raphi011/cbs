# Handoff — Task 18c/18d, steps 1–4 landed, `spec/sqlite-only-store`

**Everything is committed and `go build ./...` is clean.** Five commits sit on top
of `aa4e647`:

```
f3b7b82 RunPayment splits along row owners, and store/sqlite is green
1614596 a payment row is not the same columns in every shape
20296da the test packages take a store set, except mesh's
7627eee step 4: the shared suites take a store per book
60beb54 steps 1-3: one store per institution, and the tree builds
```

This supersedes `handoff-2026-08-06-task-18cd-step0.md` as the entry point. Its
three regressions and its "decisions taken that were not in any plan bullet" all
still stand and are not repeated; its remaining-work list is what this document
replaces.

## Where the tree is, measured

```
go build ./...    clean
gofmt             clean
go vet ./...      one package fails: mesh (books_test.go, 82 ledger.NetworkBook refs)
```

| package | state |
|---|---|
| `ledger`, `deposit`, `product`, `lending`, `interest`, `iso20022` | **green** |
| `store/sqlite` | **green** — all five conformance suites, all four race suites |
| `payment` | builds, **11 failures** |
| `api` | builds, **21 failures** |
| `seed` | builds, **13 failures** |
| `cmd/server` | builds, **1 failure** |
| `mesh` | **does not build** |

**Forty-five of the forty-six failures are ONE cause.** Run them and read the
error: `sqlite: this store's schema holds no such table: this is the csm shape
and it has no banks table`. That is `ListBanks` or `GetBank` reached through the
clearing house's network (once, the central bank's). It is the plan's own step-3
item — *38 `ListBanks`/`GetBank` call sites* — and `inShape` is doing exactly
what its doc says it exists to do: making every one of them fail loudly.

The forty-sixth is `api`'s `TestAdmissionDuringAShutdownIsRefusedWithoutTheRemedy`,
`payment: opening member bank nope` — a fixture passing a BIC that is not one,
now that the value names a file.

## What landed

### `payment.Stores`, and `Networks` over it

```go
type Stores interface {
    Bank(ctx context.Context, bic iso20022.BIC) (Store, error)
    Banks(ctx context.Context) ([]iso20022.BIC, error)
    ClearingHouse() Store
    CentralBank() Store
    Reset(ctx context.Context) error
    Close() error
}
```

`Banks` is **not in the plan** and is the honest replacement for the clearing
house's `ListBanks` at the composition root: the set of banks is the set of
databases, and that INCLUDES the founded-but-unadmitted bank the roster
deliberately omits. `cmd/server`'s listener plan is the caller. Nothing in the
domain calls it and its doc says nothing should.

`Networks.Bank` gains a context and an error; the other two do not, and the
asymmetry is documented in one place — the two institutions exist before any bank
does, so neither can fail to be there. `Networks.Store()` is gone; `Stores()`
replaces it and `api.Server.Reset` is its caller.

### `store/sqlite.Set`

`OpenSet(ctx, dir, clock)`. An empty dir means N+2 **separate** ephemeral
databases, not one shared one. A named dir is scanned for `*.db` at startup, so a
restart picks up exactly the banks the last process left; `Bank` opens or creates
one, which is what founding does. Handles are cached and the mutex is the point:
on an ephemeral database a second `Open` would mint a second random memdb name and
the bank would forget everything it had written.

`cmd/server`'s `-database` is a **DIRECTORY**. That is a user-visible CLI change.

### Three crossings closed

- **`Mesh.Admit` founds through the APPLICANT's network**, not the clearing
  house's. So does `storetest.Admit`. Both carried "same fate at 18c" against
  them; the fate arrived, because a bank's id is its BIC and the store set mints
  the database from the address the caller already passed in.
- **`joinRoster` reads the roster and nothing else.** The actor it registers is
  named by its ADDRESS — a roster entry carries no name, and the bank's name is
  in the bank's own database now.
- **`Mesh.banks` is re-keyed by BIC**, which took `claimAddress`'s sweep (it now
  returns a bool; there was nothing left to sweep for) and `Lodge`'s participant
  argument with it.

### The seed is no longer one unit of work per composite

`initiate`, `reject`, `returnPayment` and `settle` each ran several institutions'
halves inside one `Update`. They cannot: two institutions is two databases. Each
half is its own unit of work now and the docs say what that costs. `advise` is a
new shared helper for the statement loop the last two share.

### A payment row is not the same columns in every shape

The bank keeps the legs and no `cycle_id`; the clearing house keeps `cycle_id`
and no legs. The Go side had one column list, one value list and one scan list
and wrote `cycle_id` into a table that has none. They are now one description
read three ways.

**A real defect fell out of it.** `PutMandate`'s hand-written `?,?,?…` had been
ONE SHORT since Task 18b added `debtor_agent` — twelve values for thirteen
columns, every mandate write, on every store — and nothing failed on it until the
shapes split. Fixed with the same mechanism rather than by adding a question mark.

### The shared suites take a store per book

`RunLedger`/`RunDeposit`/`RunProduct`/`RunLending`/`RunPayment` take
`func(*testing.T, ledger.BookID) X`. A suite wanting a second book asks for a
second STORE. Every isolation case is now two databases that cannot see each
other rather than two `book_id` values, which is a stronger claim than the
composite primary key it used to rest on.

`bookC` is **deleted**: it existed for `AuditPagingIsScopedToItsFilter`, which
needed a book whose events were payment-scoped so the pager had gaps. The gaps
come from the SCOPES and always did.

`AuditFilterByScopeTypeEntityAndBefore` loses its "BookID narrows to one book"
dimension, which a one-book store cannot have, and gains the refusal that
replaces it: **a filter naming another institution's book must fail rather than
answer with the empty page that reads as end-of-data.** storetest names no
dialect, so what it asserts is that there IS an error.

### `RunRaces` splits three ways, `RunPayment` splits three ways

- `RunClearingHouseRaces`, `RunCentralBankRaces` — one institution's store each,
  built with `payment.NewNetwork` rather than the factory.
- `RunSystemRaces` — takes `payment.Stores`. **This deviates from the plan**,
  which said the two multi-institution cases "become mesh-level". A suite
  parameterised on the domain's own `Stores` interface names no dialect and no
  table, so it stays in `storetest` where its measurements are written down.
- `RunPayment` / `RunClearingHousePayment` / `RunCentralBankPayment`, along row
  owners. Two cases lost something real and say so where they are: the rollback
  case can no longer claim a cycle rolls back with a payment, and the listing
  order case orders one institution's rows at a time.

### `store/sqlite`'s own tests

`TestSchemaArgumentsReachSqliteMaster` is one case per shape. The csm's two
arguments are the ones worth having: a foreign key that cannot be written because
the table it would point at is another institution's, and the audit log's absent
one argued in the shape with no `books` table at all.

`TestExactlyOneUniqueIndex` becomes `TestExactlyOneUniqueIndexPerShapeThatHasABook`
— bank 1, centralbank 1, **csm 0**. The clearing house has no `transactions`
table, so `SQLITE_CONSTRAINT_UNIQUE` is unraisable in its database and that is
asserted rather than left implied.

### `mesh`'s recorder decorates the SET

`recordingStore` → `recordingStores`, implementing `payment.Stores`; every handle
it hands out shares one recorder, so "which books did this actor touch" is still
one question with one answer. Its doc records what the split cost it: the
crossing it was invented for — one bank reading another's ledger through a method
it legitimately holds — is no longer expressible, because no database can answer
that read. What it kept is attribution, which is what every assertion in
`books_test.go` is actually about.

## What remains, in the order it has to happen

### 1. The `ListBanks`/`GetBank` sites (45 of the 46 failures)

**Do this first.** It is the largest single win in the task and it is
mechanical-ish once each site is decided. Four production sites and a handful of
fixtures:

| site | what it is | what it should become |
|---|---|---|
| `seed/seed.go:113` | `nets.ClearingHouse().ListBanks` — Populate's idempotency check | the roster: `ListRosterEntries`, non-empty ⇒ already seeded |
| `payment/system.go:2696` | `settlementLegsTx`'s `tx.ListBanks` | the central bank's OWN register — `ListSettlementMembers`. Its doc already names itself as one of the sites the split has to answer for, and says what stays on the bank's row (`AccountsFor`) and why. **Read that comment before changing it**: dropping the asset check silently would break `TestSettleCycleFailsWhenParticipantLacksTheAsset` |
| `api/handlers_participant.go:107` | `GET /participants` on the clearing house's surface | the roster, or the store set; the answer decides whether the console lists members or banks |
| `api/handlers_participant.go:235` | `GET /reserves` on the CENTRAL BANK's surface | `ListSettlementMembers` — the settlement agent's own register, which is what `reserveRows` actually needs |
| `api/handlers_participant.go:255` | `GET /reserves/{pid}` | `GetSettlementMember` |
| `payment/system.go:4846` | `ReserveBalance`'s `tx.GetBank` | same — the member row, not the bank row |

Test fixtures with the same cause:

- `payment/audit_test.go`'s `auditReaders` — its doc already says the roster
  cannot replace it (a FOUNDED bank has no entry and does have a log, and
  `TestFoundingABankIsAudited` is about that bank). Use `sys.stores.Banks(ctx)`,
  which is `payment.Stores.Banks` and is exactly the right answer. The
  `testSystem` already carries `stores` for this.
- `payment/system_test.go`'s `takeCashIn` calls `sys.Deposit(...)` on the
  CLEARING HOUSE's network. It should be `sys.bank(p.BIC).Deposit(...)`. This is
  the same defect class as `initiate`'s, found and fixed at step 0.

### 2. `mesh/books_test.go` — 82 `ledger.NetworkBook` references

The plan's step 6 and the one piece that is not mechanical. The constant is
deleted; each reference is a per-assertion decision about which institution's
book an act should now touch. The block above `TestTheCSMTouchesOnlyTheNetworkBook`
(around line 676) is a long argument about what `NetworkBook` meant for the
recorder and is the thing to read first — it explains why a BANK's expected set
was `[NetworkBook, its own book]` and what the `[its own book]` answer costs.

`mesh` will not build until this is done, so nothing in `mesh` can be measured.

### 3. Per-entity payment rows — the plan's step 5, and the largest remaining

**The store half is done** (per-shape columns). The domain half is not, and it is
a genuine design change rather than wiring:

- `bank.receiveCreditTransfer` and `receiveDirectDebit` build a
  `CreditTransferRequest`/`DirectDebitRequest` from the message and **discard
  it**, then call `AcceptInbound(ctx, id)` — which loads the payment by the id
  the message carries. That worked because both banks read one payment row out of
  one store. Both handlers' docs say this in as many words and call it "sub-project
  8's whole subject". The receiving bank has to CREATE its own row from the
  message.
- `csm.receivePayment` routes the pacs.008/pacs.003 on. It must write the
  clearing house's own copy, and `AcceptAtCSM(ctx, id)` must stop assuming one is
  there.
- The `PaymentID` is minted by the submitter and carried in `PmtId/TxId`, so each
  institution writes its own row under that id — nothing needs to be allocated.

Until this lands, `seed`'s `initiate` will fail at its second half (the receiver
has no row) even after step 1, and `RunSystemRaces`'s payment assertion reads the
payer's bank rather than the clearing house — the comment there says so.

### 4. `web/` has not been touched

The API's wire format changed at step 0 and `web/src` is unchanged and will be
wrong: `partyRefDTO` lost `participant`; `paymentDTO` gained `debtorAgent` and
`creditorAgent`; `mandateDTO` gained `debtorAgent`; `createMandateRequest`
requires `debtorAgent`; `directoryEntryDTO`'s `participant` became `agent`; and
every one of those values is a BIC where it used to be a `bank_1`-style id. Plus
`web/src/lib/identity.ts`, its test, and the `/bank/{pid}` route segment.

### 5. The documentation sweep (18e), which has grown

- **`CLAUDE.md` says "there is one migration" and names `0001_init.sql` twice.**
  There are three, one per shape, under `store/sqlite/schema/{bank,csm,centralbank}/`.
  The comment-placement rule it teaches is unchanged and still load-bearing; what
  is stale is the count and the paths. `TestSchemaArgumentsReachSqliteMaster` is
  three cases now.
- **`CLAUDE.md`'s "One store, and it needs no setup"** is now N+2 stores that need
  no setup. `store/testenv` has two entry points (`New` for one bank's store,
  `NewSet` for the system) and the reason is worth carrying over: the four layers
  below payment have no idea what an institution is.
- `README.md:1445` and `:1475` name the deleted file.
- `deposit/terms.go`, `deposit/types.go`, `deposit/store.go`, `payment/system.go`
  and `api/handlers_audit.go` carry doc-comment pointers at it.
- `payment/store.go:36`/`:109` said rows "live under ledger.NetworkBook" — the
  first is fixed, check the second.
- `api/surface.go:105` still says the participant listing "answers ListBanks".
- `mesh/doc.go:9-13` says the actors share one store and a bank "CAN look the
  answer up". They do not and it cannot.

## Six things not to rediscover

1. **`go test ./store/... ./ledger/ ./deposit/ ./product/ ./lending/` is the green
   subset.** Run it after any store change; it is fast and it is the only thing
   currently proving the schemas and the shared suites.
2. **`inShape` is the instrument, not the obstacle.** Every one of the 45
   failures is a crossing the plan predicted. Do not add a table to a shape to
   make a test pass.
3. **A `-run` filter matching no test prints `ok`.** When watching a guard fail,
   confirm the break applied *and* that the test ran.
4. **`payment.Stores.Banks` is the only enumeration of banks that survives.** If
   a site needs "every bank", it is either this (deployment) or the roster
   (membership) — and those two answer differently for a founded, unadmitted
   bank, which is the case that decides which one a site wants.
5. **`storetest` must not import an implementation**, because every
   implementation's tests import `storetest`. `RunSystemRaces` takes
   `payment.Stores`, which is the domain's interface, not `*sqlite.Set`.
6. **The ephemeral set really is N+2 separate databases.** `sqlite.Set` gives each
   a random memdb name of its own, so a test that appears to share rows between
   two institutions is a bug in the code under test rather than in the fixture.
