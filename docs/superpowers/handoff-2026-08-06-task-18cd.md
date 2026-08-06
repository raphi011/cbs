# Handoff — Task 18c and 18d merged, part landed, `spec/sqlite-only-store`

**Nothing is committed. The tree does not build.** Two packages fail —
`store/storetest` (2 lines) and `cmd/server` (1 line) — and `store/testenv`
fails behind them. That is expected and it is where the work stopped, not a
defect: the store layer is done and the N+2 wiring is not started.

This supersedes `handoff-2026-08-06-task-18c.md` as the entry point. That
document's defect list, reversal list and verification recipe all still stand
and are not repeated here.

## Three rulings changed the task, and the user made all three

Each was put to the user, because each contradicts the plan's letter and each
decides work that cannot be undone cheaply. The third is in its own section
below, because it is the largest and because the question that produced it
looked like plumbing.

1. **18c and 18d are ONE commit.** The handoff said the single migration "dies
   at 18c". It cannot die at 18c and leave a green tree: `mesh`, `api`, `seed`,
   `cmd/server` and two of `RunRaces`' four cases all need one database holding
   every table, and three shape files with genuinely per-shape argument text
   cannot be unioned back into one. The options were to keep a transitional
   monolith for one commit, to compose the shapes from shared fragments, or to
   pull 18d's wiring forward. **The user chose to pull 18d forward.** There is no
   monolith and there is no fourth shape.
2. **The one-book refusal lands now, and the two-book suite cases are
   restated.** The plan listed the refusal under both 18c and 18d, and it cannot
   land under 18c as written: seven shared-suite cases use two books in one
   store (`SameIDsInDifferentBooks`, `NextIDSharesOneCounterPerBook`,
   `NextSubledgerBlockStepsBy100PerBook`, `GetOnMissingRowsReturnsSentinels`,
   `DuplicateIdempotencyKeyRejected`, `AuditFilterByScopeTypeEntityAndBefore`,
   `AuditPagingIsScopedToItsFilter`), and the plan's own global constraint says a
   case that has to move is a defect in the split until proven otherwise. The
   options were to defer the book refusal to 18d and land a shape refusal now, or
   to land both and rewrite the seven cases as two-store cases. **The user chose
   both.** `SameIDsInDifferentBooks` becomes two bank stores that cannot see each
   other; the rest take the store's own book.

3. **A bank's `ParticipantID` is its BIC.** See *The third ruling* below. It is
   in no plan bullet and no spec paragraph, and the split cannot be finished
   without deciding it one way or the other, because the counterparty's
   participant id has no source once each bank holds only its own row.

**Both sentinels exist and are implemented.** `ErrNotInThisShape` was not in any
plan bullet and is forced by `payment.Tx` being one interface over three
schemas — see below.

## The plan's four open questions, three answered by measurement

Answered by reading the code, not by reasoning about the design:

1. **What does `RunRaces` mean across three stores? It splits 2/2.**
   `ConcurrentAdmissionsOfOneBICAdmitOne` drives `csm.AdmitMemberTx` and touches
   `roster_entries` alone — pure `csm`. `ConcurrentSettlementAccountOpenings-`
   `KeepEveryAsset` drives `cb.OpenSettlementAccountTx` and touches
   `settlement_members` plus `CentralBankBook` accounts — pure `centralbank`.
   The other two (`ConcurrentAdmissionsAgreeOnOneCentralBank`,
   `ConcurrentSubmissionsOfOneReferenceAcceptOne`) drive `storetest.Admit` across
   all three institutions and have **nowhere to run as store suites**. They
   become mesh-level. `RunConcurrentTxRaces`' five cases are all
   `ledger.NewBook(s, "bank", …)` and `tx.PutTransaction(ctx, "bank", …)` —
   pure GL, so they run against the bank shape and the central bank's.
   **Do not delete any of them to make the tree compile.**
2. **Does the bank hold a settlement-position row? No.**
   `settlement_positions` is written and read only through
   `PutSettlement`/`GetSettlement`; every reader is the central bank's or the
   operator console's, and `mesh/bank.go` names a position nowhere. The spec's
   "a settlement-position row per cycle" means `settlement_advices`, which the
   bank already owns. The table is `centralbank`'s and the argument is recorded
   inside the statement.
3. **Where does the audit log live? It is already per entity.** `/audit` has
   been bound per listener since api gained one port per institution — a bank's
   port reads its own book, the central bank's reads its own,
   `/payments/audit` on the clearing house's reads the payment scope. There is no
   cross-entity view to fan out. The split made the endpoints honest rather than
   changing them; the only code change was `api/handlers_audit.go` swapping
   `ledger.NetworkBook` for `payment.ClearingHouseBook`.
4. **`store/testenv.Store`** is unresolved and is now entangled with the wiring
   below. It aliases `storetest.Store`, which names four layers; three shapes
   want three narrower aliases, and `testenv.New` cannot keep its signature
   because it has no way to know which shape or book to open.

## What landed, measured against the working tree

| | |
|---|---|
| `store/sqlite/schema/bank/0001_init.sql` | 1716 lines, **23 tables**, 10 declared indexes |
| `store/sqlite/schema/csm/0001_init.sql` | 469 lines, **7 tables**, 4 declared indexes |
| `store/sqlite/schema/centralbank/0001_init.sql` | 504 lines, **12 tables**, 4 declared indexes |
| `store/sqlite/schema/0001_init.sql` | **deleted** (`git rm`) |
| book guard `t.own` | inserted in **56** methods |
| shape guard `t.inShape` | inserted in **75** methods |

23 + 7 + 12 = 42 statements over **31 distinct tables**, which is exactly the
set the deleted file had. Each of the three was executed into `:memory:` and its
`sqlite_master` read back; all three apply cleanly.

**Nothing was dropped from the old file by accident.** A flow-and-diff over the
comment text — sentences flowed across line breaks first, because a claim split
across one is invisible to a line-based match — found 74 of the original's 506
sentences absent verbatim, and each of the 74 is a sentence deliberately
rewritten or relocated. The check script is worth re-running after any further
edit; it is four lines of Python over the two texts and it is the only thing
standing between this and a silently lost argument.

### Where the spanning arguments went

The rule is the plan's: an argument spanning two shapes is written once, in the
shape whose absent constraint it is about, and the other names it.

- **`bank` is canonical** for: the seq allocation rule (`ledgers.seq`), the
  absent CHECK on an asset code (`accounts.asset` — now naming all five bank
  columns plus `settlement_member_accounts.asset` and
  `roster_entry_assets.asset`), the absent parent foreign key and the
  child-table exemption (`subledgers`), the absent `UNIQUE (book_id, name)`'s
  domain half (`ledgers`), the three rows one admission writes (`banks`), the
  agent-column rule (`payments.creditor_agent`), and the per-institution audit
  log (`audit_events`).
- **`centralbank` is canonical** for the find-or-create race behind the chart of
  accounts (`ledgers`), which is the largest single argument in the old file and
  the one that goes the other way — the chart it is about is the settlement
  agent's. `bank`'s `ledgers` names it and adds that nothing in a bank's book is
  resolved by find-or-create at all.
- **`csm` is canonical** for nothing and names four.

### Three schema decisions that are new domain content

- **The bank shape's `payments` has no `cycle_id`.** Verified by grep:
  `mesh/bank.go` touches a cycle nowhere, and `p.CycleID = cycle.ID` has exactly
  one writer, `AcceptAtCSMTx`, which is the clearing house's. "A bank has no
  cycles" is now a fact about the schema. **This is a `payment.Payment`
  round-trip change and the Go side is not done** — see the remaining work.
- **The csm shape's `payments` has no leg or return columns.** `debtor_leg_tx`,
  `creditor_leg_tx`, `creditor_leg_account`, `return_clawback_tx`,
  `return_refund_tx` are the bank's. **One reader has to change and has not
  yet**: `mesh/csm.go:796`, in `csm.tell`, reads `p.DebtorLegTx != ""` to decide
  whether the payer's bank is holding money against a rejected pull. It must
  decide from the scheme's direction and the status it carried the payment to,
  which are facts it owns.
- **The return pair stops marking which leg is second.** `return_clawback_tx`
  and `return_refund_tx` used to sit in one row both banks read, and the leg that
  found the other's id already written was the one that took the payment to
  Returned. Each bank now fills exactly one of them, in its own database. The
  ruling written into the schema: **there is no second leg to detect** — each
  institution takes its own row to Returned on its own terms, and the state the
  marker produced is the clearing house's to hold on its copy. **The Go side is
  not done.**

### The Go that landed

- **`sqlite.Shape`** — `Bank`, `CSM`, `CentralBank`, each a schema directory plus
  the set of tables it holds. There is deliberately no fourth.
- **`sqlite.Open(ctx, shape, book, path, clock)`** — both new arguments are
  required and refused when empty.
- **`sqlite.ErrNotThisStoresBook`** — `t.own(book)`, called FIRST in every method
  taking a book, before the read-only check and before `ensureBook`. The
  ordering is load-bearing and the comment says why: after `write()` a View still
  sweeps a foreign book, and after `ensureBook` the store has already INSERTED a
  row for it.
- **`sqlite.ErrNotInThisShape`** — `t.inShape(table)`, called before `own`. This
  one is in no plan bullet and is forced: Go cannot give the clearing house a
  `payment.Tx` without `PutBank` on it, so every shape implements every method
  and two thirds have no table. Without the sentinel a crossing would surface as
  the driver string `no such table: banks`, which nothing above the store can
  match on.
- **`payment.ClearingHouseBook`** and **`Network.book()`** — the three-way answer
  to every "which book?" that was `ledger.NetworkBook`. Bank → its own id,
  clearing house → the new constant, central bank → `CentralBankBook`.
- **`ledger.NetworkBook` is deleted**, with a paragraph where it stood saying
  what it meant and why it was deleted rather than renamed.
- **`FoundBankTx` no longer allocates the bank id.** It reads `s.self()`. See
  the chicken-and-egg below, which is the single most important open design
  point.
- **`Store.Reset`** empties `shape.holds` rather than a hand-kept list of 31.

## The third ruling: a bank's ParticipantID is its BIC

**The user made this one too, and it is the largest single change in the task.**

It came out of a question that looked like plumbing — a bank's database is named
by its id, and a counter-derived id would have to be allocated from a counter
inside the database that value names, which is the one place that cannot supply
it. That knot is why `mesh.Mesh.Admit` and `store/storetest.Admit` founded a bank
**through the clearing house's network**; both were carried as deferrals with
"same fate at 18c" against them.

Siting the counter is the wrong fix. **`ParticipantID` was doing two jobs and
only one survives isolation:** this bank's own name for itself, and the
network's ADDRESS for it. Eight non-test sites do the second —

```
mesh/mesh.go:1419   mesh/csm.go:797,811,1136,1281,1377   mesh/bank.go:659,667
```

— and every one goes through `payment/list.go:136`, which is literally
`GetBank(pid)` → `GetRosterEntry(bank.BIC)`. That is a read into a database the
caller does not hold, the code already calls it a crossing (`list.go:155-161`,
`mesh/bank.go:654-658`), and the BIC-keyed replacement already exists with five
callers. Worse, no message has ever carried a participant id, so **there was no
source for the value those eight readers start from.**

Take the second job away and the first distinguishes nothing: a bank's database
holds one bank, so the id would be a constant — `bank_1` in every database. An
id that is always the same string is not an identity.

**So there is one identifier.** The file is `AURODEFFXXX.db`, the `banks` row id
is `AURODEFFXXX`, the `BookID` is `AURODEFFXXX`, the route is
`/bank/AURODEFFXXX`. Nothing allocates it, no counter needs siting, and the
chicken and egg dissolves rather than being worked around, because a joining bank
arrives already knowing its BIC.

Two objections were raised against this and neither survives the split, which is
recorded because both look decisive on the way past. *`banks.bic`'s documented
"no UNIQUE"* is vacuous over a one-row table; what actually refuses two banks on
one address is `roster_entries`' primary key at the clearing house, answering
`ErrBICAlreadyAdmitted`. *Correcting a BIC would rename a database* — nothing in
this system corrects one, as `payments.creditor_agent`'s own comment says.

### What the ruling already changed

In the schemas:

- `banks` loses **`bic` and `book_id`**; `id` is the BIC and is the book. The
  argument is in the statement.
- `payments` loses **`debtor_participant` and `creditor_participant`** in BOTH
  shapes. They named each party's bank as an id beside an agent column naming the
  same bank as a BIC; two columns that cannot differ are not two facts. The agent
  columns survive because they are what goes on the wire.
- `mandates` loses `creditor_participant` (always this bank) and renames
  `debtor_participant` to **`debtor_agent`** — it is an address the collection is
  sent to, not a party this institution can look up.
- `settlement_positions.participant_id` becomes **`bic`**. Its previous comment
  flagged that converting one to the other "is a lookup this institution has no
  table for"; that comment was the defect describing itself.
- `cycles.net_positions` is BIC-keyed, so the figures the clearing house computes
  and the key `settlement_members` uses are finally the same value.

In Go: `FoundBankTx` allocates nothing and refuses an application whose BIC is
not this store's; `Network.book()`, `AsBank` and `payment/identity.go` say why.

### What it still costs, and what is not decided

- **`payment.PartyRef` loses `Participant`.** The counterparty's bank is the
  agent BIC beside it. This is wider than 18d's bullet, which only said
  `InitiatePaymentRequest` loses the *debtor* participant.
- **A wrong counterparty agent and a wrong counterparty stop being
  distinguishable**, because there is one value to be wrong. Nothing is weakened
  — the message still goes to the bank it names, which answers AC01 — but
  `mesh/books_test.go`'s
  `TestAWrongCounterpartyAgentIsRefusedByTheBankItNames` is about a disagreement
  between the two and there is no longer one to construct. Rewrite it as a wrong
  counterparty.
- **`bank_1` literals move.** Seed fixtures, `/bank/{pid}` routes,
  `web/src/lib/identity.ts` and its test, and roughly a dozen Go test files.
  The route segment should become the BIC — a port is already one institution
  and the BIC is what the operator sees everywhere else.
- **Undecided, deliberately:** whether `ParticipantID` should survive as a type
  at all now that its value is a BIC, or whether every use becomes
  `iso20022.BIC`. Two types over one value is defensible — they say different
  things at a call site, and `iso20022.BIC` is where the format rule lives — and
  the note is on `AsBank`. Revisit once the wiring settles; do not decide it
  while the tree is red.
- **`payments.debtor_agent`/`creditor_agent` keep `DEFAULT ''`** and probably
  should not, now that the agent IS the party's bank rather than a late addition.
  Left alone because it is a one-line change that wants the Go beside it.

### The store set, which is now simple

```go
type Stores interface {
    Bank(ctx context.Context, bic iso20022.BIC) (Store, error)
    ClearingHouse() Store
    CentralBank() Store
}
```

No `Provision`, no minting, no error-returning id allocation. `Networks.Bank`
still gains an error return (18 call sites) because opening a database can fail.
Restart needs no counter: the set of banks is the set of files, and it agrees
with the clearing house's roster by construction — including the founded-but-
unadmitted bank, which the roster deliberately omits.

**`cmd/server`'s `-database` becomes a DIRECTORY.** That is a user-visible CLI
change and it is in no plan bullet.

## What remains, in the order it has to happen

0. **The eight `GetRosterEntry(ParticipantID)` sites become
   `GetRosterEntryByBIC`**, and `payment/list.go:136` is deleted. Do this FIRST
   and on its own: it is mechanical, it is the third ruling's whole point, and
   it closes the two crossings named on `Mesh.clearingHouse`'s doc without
   touching the wiring. `payment.PartyRef` loses `Participant` in the same step.
1. **`payment.Stores` and `Networks`** — the interface above. Everything after
   this is blocked on it.
2. **`store/testenv` opens N+2**, and open question 4 gets answered as part of
   it.
3. **`seed`, `api`, `cmd/server`, `mesh`** hold one store per entity. **38
   non-test `ListBanks`/`GetBank` call sites** each become a roster read at the
   clearing house, a message, or an error — including the two named on
   `Mesh.clearingHouse`'s own doc, `joinRoster`'s `ListBanks` and `Admit`'s
   `GetBank`/`FoundBank`. The `csm` shape has no `banks` table, so **`inShape`
   makes every one of them fail loudly**; that is the intended way to find them.
4. **`storetest`** — three runs, `RunPayment` split along row owners, `RunRaces`
   split 2/2 per the answer above, the seven two-book cases restated, and
   `storetest.Admit` stops founding through the clearing house. Two live
   `ledger.NetworkBook` references remain at `storetest.go:1524` and `:1536`.
5. **Per-entity payment rows** — the two readers named above, plus
   `InitiatePaymentRequest` losing the debtor participant and `ListParticipants`
   becoming the clearing house's. `mesh/doc.go:9-13` says the opposite of what
   `bank.receiveStatus` will then mean.
6. **`mesh/books_test.go`** — 82 `NetworkBook` references. The receiver's set
   moves from `bankBooks()` to `[creditor]` with its pull mirror, which is the
   measurement Task 14 deferred to here.
7. **Three `TestSchemaArgumentsReachSqliteMaster`**, and
   `TestExactlyOneUniqueIndex` becomes three with **different numbers**: bank 1,
   centralbank 1, **csm 0**. The csm shape has no unique index at all, so
   `SQLITE_CONSTRAINT_UNIQUE` is unraisable there — asserted rather than left
   implied.
8. **Watch every guard fail.** Both new sentinels, the three-way `book()`, the
   `payments` column removals. Remember that a `-run` filter matching no test
   prints `ok`: confirm the break applied *and* that the test ran.
9. **The documentation sweep**, which is 18e's and now has more to carry:
   `CLAUDE.md`'s *there is one migration* becomes one per shape (the file names
   `0001_init.sql` twice and both are stale), `README.md:1445` and `:1475` name
   the deleted file, and `deposit/terms.go`, `deposit/types.go`,
   `deposit/store.go`, `payment/system.go` and `api/handlers_audit.go` carry
   doc-comment pointers at it. `payment/store.go:36` and `:109` still say rows
   "live under ledger.NetworkBook".

## Two things not to rediscover

- **`go build ./...` reports only two packages.** `mesh`, `api` and `seed` build
  today, which is misleading: they compile because nothing in them calls
  `sqlite.Open` and nothing outside their tests names `NetworkBook`. They are
  entirely unwired and every one of their test suites will fail the moment
  `testenv` changes.
- **The schema-execution check is three lines of Go and worth keeping.** Execute
  a file into a `memdb` database and read `sqlite_master` back. It is how the
  table counts above were taken and how `mandates.asset` was verified at 18b;
  guessing whether a comment reached the dump is the failure mode this whole
  comment-placement rule exists for.
