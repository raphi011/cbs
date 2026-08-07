# Task 18 — split the stores

**Goal:** three store shapes — `bank`, `csm`, `centralbank` — each with its own
schema, its own database, and exactly one `BookID` it will answer for. N+2
instances at runtime and in every test. `ledger.NetworkBook` is deleted. A
crossing stops being something the recorder reports and becomes an error.

**Design:** `docs/superpowers/specs/2026-08-02-db-per-entity-design.md`, plus its
dated section *The store underneath becomes SQLite* (2026-08-03), which is what
makes this three shapes × **one** backend. Where this plan and the spec disagree
on a *decision*, the spec wins. Where they disagree on a *number*, this plan
measured it.

**Method:** `docs/superpowers/2026-08-06-avoidable-review-cycles.md`. Two of its
rules govern this document. A plan specifies behaviour, interfaces and tests and
**contains no comment text** — where a doc has to make an argument it is named in
one line here and written from the code that ends up existing. And every
measurement below was taken by writing this plan; none is inherited.

---

## What was measured before this plan was written

Sizing, on `spec/sqlite-only-store` at `2ed68ad`:

| | |
|---|---|
| `store/sqlite/schema/0001_init.sql` | 1669 lines, **32 tables**, 12 indexes (1 unique, 3 partial) |
| `store/sqlite` Go | 4801 lines across 12 files |
| `store/storetest` | 7117 lines across 8 files |
| `payment.Tx` | **26 methods**; `payment.Store` 4 |
| `ledger.NetworkBook` | **117 references** in 14 files — 83 of them in `mesh/books_test.go` |
| `ledger.CentralBankBook` | 36 references |
| `payment.NewNetwork` | **23 call sites**, and it takes exactly **one** `Store` |
| the seed | 4 banks, so **N+2 = 6** stores per test that builds the scenario |

Two things that follow from the last two rows and set this task's shape:

- **`payment.Network` has no identity.** `NewNetwork(store, clock)` builds one
  object holding one store and a `centralBank` book over it (`system.go:123-138`).
  Every act in the package — a bank's, the clearing house's, the settlement
  agent's — runs against that one object. Giving each entity a store means
  giving the type an answer to *which entity am I*, and that is the design
  decision this task turns on. The spec says so in as many words under *The
  receiving bank's measurement belongs to Task 18*.
- **`mesh/books_test.go` is a third of the work.** 83 of the 117 `NetworkBook`
  references are the recorder's expectations. They are not noise: each one is a
  measurement this sub-project moved deliberately, and each has to move again.

## Corrections to the spec, each measured

| the spec says | the code says |
|---|---|
| the two crossings still open are `ResolveIdentifierTx` and `DepositTx` | there is a **third**, and it is on the happy path: `SubmitPaymentTx` reads the counterparty's `Bank` row for its BIC (`system.go:2683`), and `checkPartyTx` reads it again for the counterparty's book (`:4186`). Bank rows are network-scoped today, which is why the recorder does not see it; under the split each bank holds only its own row and neither read has an answer |
| `storetest` becomes three **conformance** suites | half wrong from 17.3, as the spec's own dated section says; the doc comment already says *shared suite*. What is left for this task is running it three times, not renaming it |
| Postgres: one schema per entity in one database | gone with `store/pg`. One SQLite **file** per entity, or one ephemeral database per entity under test — `store/testenv.New` already gives each `Open` a random name, so N+2 instances need no naming scheme |
| the bank's shape holds "a settlement-position row per cycle" | `settlement_positions` is a child of `settlements`, which is the central bank's. Either the bank gets a row of its own shape or the sentence means `settlement_advices`, which the bank already owns. **Unresolved in the spec and this plan does not guess** — see the open question below |
| Verification runs `TEST_DATABASE_URL=… go test ./...` | expired at 17.1. One Go run |

And one the spec is right about, checked because the whole task rests on it:
`mesh/ops.go`'s three narrowed interfaces really are "already the list of what
each entity may reach". Every method on `bankOps`, `csmOps` and `settlementOps`
is called by a handler in `mesh`, and there are no others — its own doc says the
interfaces grew method by method rather than being written ahead of callers.

## The ruling this task needs, and it is not in the spec

**`ledger.NetworkBook` is deleted, and its identity counter has to go somewhere.**

Every read-then-write ordering in this repository allocates from it:
`payment.admissionSequenceTx`, `SubmitPaymentTx`'s duplicate-reference check,
`FoundBankTx`, and the find-or-create behind the central bank's chart of
accounts. Task 17.3 measured what those orderings are worth on `store/sqlite` —
with each of them removed, `storetest.RunRaces` passes ten runs out of ten, on
the ephemeral store and on a WAL file — because SQLite admits one writer and
`Store.Update` re-runs a loser against the winner's committed row. So today they
are a second guard and the suite cannot see them go.

That changes here, and in which direction depends on a decision this task makes:

- If each entity's counter moves into **that entity's own store**, every act's
  allocation and the row it decides from stay in one database and both guards
  survive untouched. Each admission act belongs to exactly one institution —
  `OpenSettlementAccountTx` to the central bank, `AdmitMemberTx` to the clearing
  house, `RecordMembershipTx` to the bank — so this is the shape that falls out.
- If any act's allocation and its read land in **two** stores, neither guard
  spans them, because two databases is two transactions and there is no retry
  that can make one of them see the other.

**The ruling: the counter follows the row.** An act allocates from the store it
is about to write. Where an act cannot satisfy that, it is a crossing and has to
become a message, which is the sub-project's thesis rather than an exception to
it. `store/storetest`'s `ConcurrentReadThenWriteOnOneKeyAgrees` and `RunRaces`
are what state the shape a replacement has to keep, and both stay.

## Global constraints

Task 17.0's and 17.1's constraints carry unchanged and are not restated. In
particular: **this plan carries no comment text**, every measurement is watched
failing first, and a correction's explanation is verified as hard as the
correction.

Specific to this task:

- **One backend, three shapes.** `store/sqlite` is the only implementation and
  stays the only one. A shape is a schema plus the subset of the store methods
  that schema can answer — not a second driver.
- **Each store owns one `BookID` and refuses every other**, with a named
  sentinel. This is the task's central new guard: it turns a crossing from a
  silent not-found into a loud error, and it is what lets the recorder's
  expectations shrink rather than merely move.
- **A storetest case that has to move is a defect in the split** until proven
  otherwise. Shapes narrow *which* suites a store runs, never what a suite
  asserts. If `RunPayment` needs a case removed for the `csm` shape, the case is
  in the wrong suite and the fix is to split the suite, not to weaken it.
- **The reconciliation harness is the successor instrument**, and it is the only
  thing in the repository permitted to open all N+2 stores at once. Nothing in
  `api`, `mesh`, `seed` or `payment` may hold more than one.
- **Two of the nine races do not bite on one run.** `ConcurrentAdmissions-`
  `AgreeOnOneCentralBank` and `ConcurrentMarkReversedOnlyOneWins`. Run anything
  that rests on either at `-count=10`.
- **The ephemeral store hides read-then-write defects.** A retry's read blocks
  there until the winner commits; only a file under WAL lets a reader past an
  uncommitted writer. Anything measured about ordering opens a file.

---

## Sequencing, and why this is five sub-tasks and not two

The spec allows an 18a/18b split "if it runs long". It will: 32 tables, 26
interface methods, 20 non-test call sites that stop having an answer, and 83
recorder expectations. Two commits would put a failing conformance case and a
dead API route in one review, which is the shape the sub-project's own log
records as the reason 6 became 6a and 6b.

Five, and the first two land no split at all:

| # | lands | why here |
|---|---|---|
| 18a | **The three crossings close, on one store.** `ResolveIdentifierTx`'s sweep, `DepositTx`'s two-book posting, and the counterparty-BIC read | The spec says the split "cannot build with" `DepositTx`. Doing these first means each is measured by the recorder that still works |
| 18b | **`payment.Network` gains an identity**, still over one store | The one design change the split cannot be done without, isolated so its review is about the interface and not about SQL |
| 18c | **Three shapes and three schemas**, `storetest` run three times, nothing rewired | The store change, reviewable against the suites alone |
| 18d | **The wiring**: N+2 instances in `testenv`, `seed`, `api`, `cmd/server`, `mesh`; `NetworkBook` deleted; foreign-`BookID` refusal | Where the recorder's 83 expectations move |
| 18e | **The reconciliation harness**, and the documentation sweep | The successor instrument, and the layers |

Each is a commit. A bisect should be able to land between any two.

---

## 18a — the three crossings close, on one store

Nothing about the store changes here. Each of these is a domain change whose
effect the existing recorder can still measure, which is the whole reason they
go first.

- [ ] **`ResolveIdentifierTx` stops sweeping.** Today it calls `tx.ListBanks`
      (`system.go:4123`) and resolves an address across every bank's register.
      The receiving bank resolves in **its own register only** and answers AC01
      otherwise. It has no way to know which register is its own until 18b, so
      this step takes the bank as an argument and 18b removes the argument.
- [ ] **`GET /directory` loses `Name` and `Asset` from `directoryEntryDTO`.**
      The endpoint is `ResolveIdentifier` with a name join on top
      (`api/handlers_directory.go:51-64`), and the two die together. The web send
      form renders the bank alone — the spec's Task 14.6 note already made this
      the minimal honest fix and it was deferred to here.
- [ ] **`DepositTx` stops posting in `CentralBankBook`.** Cash paid in becomes
      **vault cash** in the bank's own book. Moving reserves becomes a separate
      **lodgement** — a conversation, camt.050 in TARGET2. The spec says the
      split cannot build with the current shape, and it is the crossing neither
      the recorder nor `ops.go` can see, because funding never becomes a message.
- [ ] **The counterparty's BIC stops coming off its `Bank` row.** `SubmitPaymentTx`
      reads `tx.GetBank(counterparty)` for `counterparty.Agent`
      (`system.go:2683`) and `checkPartyTx` reads it again for the counterparty's
      book (`:4186`). Neither has an answer once a bank holds only its own row.
      The submitting bank has the BIC on the request already (Task 14); the
      receiving bank is the only one that needs the book, and it is its own.

**Arguments the docs must make:** why a lodgement is a conversation and a deposit
is not; why resolving in one's own register is the honest shape rather than a
narrowing; what a bank still learns about a counterparty and from where.

**Watch it fail:** for each of the four, the recorder expectation that shrinks —
break the change, watch that exact assertion fail, restore. `DepositTx`'s has no
recorder expectation today, so it needs one written first, and writing it is how
the crossing becomes visible at all.

---

## 18b — `payment.Network` gains an identity

- [ ] `NewNetwork` takes **which entity this is**. A bank's network knows its
      `ParticipantID`, its `BookID` and its BIC; the clearing house's and the
      central bank's know theirs. 23 call sites.
- [ ] Every act that today takes a participant argument to decide *whose* book to
      touch reads it from the identity instead, or is refused. The three
      interfaces in `mesh/ops.go` are the enumeration of what each identity may
      reach, and they are checked against rather than re-derived.
- [ ] `ResolveIdentifierTx` loses 18a's argument.
- [ ] `centralBank` stops being a field on every `Network`. Only the settlement
      agent's has one.

**Arguments the docs must make:** why the identity is constructor state rather
than a per-call argument; what a `Network` without a `centralBank` book cannot do
and how that is enforced.

**Watch it fail:** `TestTheCSMTouchesOnlyTheNetworkBook` and the recorder's
per-actor sets. A bank's network built with a clearing house's identity must fail
a test, not merely behave oddly.

---

## 18c — three shapes, three schemas

- [ ] **Three schema files**, one per shape, replacing `0001_init.sql`. This is
      where CLAUDE.md's *there is one migration* rule finally dies; it becomes
      one migration **per shape** and the original rationale — no database is
      deployed — survives the sentence.
- [ ] **The table assignment.** Derived from the spec's three paragraphs and
      checked against `ops.go` and the suites rather than asserted:

  | shape | tables |
  |---|---|
  | `bank` | `books`, `ledgers`, `subledgers`, `accounts`, `transactions`, `entries`, `deposit_accounts`, `deposit_account_identifiers`, `holds`, `snapshots`, `overdraft_terms`, `products`, `product_versions`, `facilities`, `installments`, `facility_terms`, `banks` (its own row only), `bank_assets`, `mandates`, `payments`, `settlement_advices`, `audit_events`, `id_sequences` |
  | `csm` | `roster_entries`, `roster_entry_assets`, `payments`, `cycles`, `cycle_payments`, `audit_events`, `id_sequences` |
  | `centralbank` | `books`, `ledgers`, `subledgers`, `accounts`, `transactions`, `entries`, `settlement_members`, `settlement_member_accounts`, `settlements`, `settlement_positions`, `audit_events`, `id_sequences` |

  `csm` has **no ledger at all**, which is true today and asserted by exactly one
  test; after this it is a fact about the schema. `centralbank` has no deposit
  register and no payments. `banks` appears once and holds one row.

- [ ] **Every argument in `0001_init.sql` goes to the shape it is about**, and
      an argument spanning two shapes goes in the one whose absent constraint it
      is about, with the other naming it. Copying is how one fact ends up in nine
      places. The comment-placement rule is unchanged and now applies three
      times: an argument about something a schema does NOT do lives inside the
      statement it concerns, because SQLite drops a comment above one.
- [ ] **`TestSchemaArgumentsReachSqliteMaster` becomes three**, one per shape,
      and each names the arguments its own schema carries.
- [ ] **`storetest` runs three times.** `bank` runs `RunLedger`, `RunDeposit`,
      `RunProduct`, `RunLending` and the bank half of `RunPayment`; `csm` and
      `centralbank` run their own halves. **`RunPayment` splits along the row
      owners** — this is the one suite change the split forces, and it is a
      split rather than a weakening.
- [ ] **`RunRaces` and `RunConcurrentTxRaces`** are the open question of this
      step: their cases drive `payment.Network` across what are now three stores.
      See the open question below.
- [ ] **A foreign `BookID` is refused**, with a sentinel, by every method that
      takes one.

**Arguments the docs must make:** why a shape is a schema and not a driver; what
the one-book rule buys that the recorder could not; why `csm` has no ledger.

**Watch it fail:** hand a `bank` store a `centralbank` book id and watch the
refusal fire. Move one argument in one schema to column 0 and watch only that
shape's `sqlite_master` test fail.

**Verification for this step specifically:** dump `sqlite_master` from each of the
three migrated databases and diff the *arguments* against the source, as 17.1b
did — the method that verified the whole file is worth reusing. Report which
arguments you checked, not which patterns you grepped. Task 17.3 checked all 1156
indented comment lines this way and found none missing; the same check across
three files is the acceptance criterion here.

---

## 18d — the wiring

- [ ] **`store/testenv` opens N+2.** One per bank plus the clearing house's and
      the central bank's. No naming scheme: `sqlite.Open` already generates a
      random name for every empty path, which is why 17.3 left the scheme out.
- [ ] **`seed`, `api`, `cmd/server` and `mesh` hold one store each per entity.**
      20 non-test call sites reach `ListBanks` or `GetBank` today; each either
      becomes a roster read at the clearing house, a message, or an error.
- [ ] **`ledger.NetworkBook` is deleted.** 117 references. The identity counter
      follows the row, per the ruling above.
- [ ] **Per-entity payment rows.** `bank.receiveStatus` keeps its behaviour and
      loses its justification: the row it trusts over the message is now its own
      prior belief rather than another entity's, and `mesh/doc.go:9-13` says the
      opposite today.
- [ ] **`InitiatePaymentRequest` loses the debtor participant.** A bank's own
      customers are its own, so naming the debtor participant is redundant.
- [ ] **`ListParticipants` is the clearing house's**, and no bank calls it.
- [ ] **The recorder's 83 expectations move**, and the receiver's set finally
      moves from `bankBooks()` to `[creditor]` with its pull mirror — the
      measurement Task 14 deferred here because `payment.Network` had no identity
      to narrow with.

**Arguments the docs must make:** what a bank knows about the network and how it
learned it; why the recorder gets stronger rather than redundant when a crossing
is already an error.

**Watch it fail:** every recorder expectation that shrinks. And the one-book
refusal: with it removed, at least one of the moved expectations must go back to
passing for the wrong reason, or the refusal is not doing the work claimed.

---

## 18e — the reconciliation harness, and the sweep

- [x] **A test-level harness that opens all N+2 stores** and asserts the network
      balances in aggregate: every bank's suspense, every reserve at the central
      bank, and the clearing house's cycles agreeing about the same payments. It
      is the one thing no entity may do, and it is the direct successor to what
      the recorder was to 7b — the instrument for defects that never become a
      message, which is the class the spec records as invisible to everything
      this sub-project had.
- [x] **The documentation sweep.** `README.md` (authoritative), then
      `hint-content.ts`, quiz chapters **9, 11, 12, 15 and 16**, and the three
      schema files. New domain content none of the layers carries: the clearing
      house has no ledger; the central bank has no customers; a bank has no
      cycles; settlement is final at the central bank and the banks reconcile
      afterwards; the creditor's bank carries the refund risk on a direct debit.
- [x] **`CLAUDE.md`**: *there is one migration* becomes one per shape.
- [x] **Five rulings are reversed** and each is written as a reversal, per the
      spec's list: the camt family's absence (camt.053 only), `OrgnlTxRef`'s
      absence (pacs.004 only), one migration, `iso20022`'s "SEPA interbank"
      framing, and a bank never existing without its accounts.

**Sweep method, and it is the expensive one:** grep only to *locate*, never to
conclude; flow comment blocks before matching, because a claim split across a
line break is invisible to a line-based pattern; grep for the sentence you
**deleted**, not the one you wrote; treat mirrored pairs as one artefact; and
after correcting a claim, re-read every passage in that file whose subject is it.
Report which files you read in full. Task 17f cost five rounds on a single claim
by doing the opposite, and Task 17.3's sweep found one live copy after four
successive greps because the wording varied.

---

## Open questions, to be answered before the step that needs them

1. **What does `RunRaces` mean across three stores?** Its cases drive
   `payment.Network` through an admission and a payment, which are now three
   institutions with three databases and messages between them. Either it becomes
   a mesh-level suite, or it splits into three per-shape suites that race one
   institution's acts each, or it is replaced by the reconciliation harness.
   Answer it in 18c by running it, not by reasoning — the same way 17.1d
   established whether SQLite met `RunConcurrentTxRaces`' precondition. **Do not
   delete it to make 18c compile.**
2. **Does the bank hold a settlement-position row?** The spec's bank paragraph
   says so and the schema's `settlement_positions` is a child of `settlements`,
   which is the central bank's. Resolve against what reads it before 18c assigns
   the table.
3. **Where does the audit log live?** Each shape has `audit_events`, so
   `GET /audit` becomes per-entity and the operator console's cross-entity view
   becomes a fan-out. That is a real API change and it is not in the spec's task
   list.
4. **What happens to `store/testenv.Store`?** It aliases `storetest.Store`, which
   names four layers. Three shapes need three narrower aliases or none at all.

## Verification

```bash
gofmt -l . && go vet ./... && go build ./... && go test ./... -count=1
go test ./store/sqlite/ -count=10 -run 'TestRaces|TestConcurrentTxRaces'
go test ./mesh/ ./api/ ./cmd/... ./store/sqlite/ -race -count=1
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

One Go run. `web/node_modules` and `iso20022/testdata/xsd/` are not in git; a
fresh worktree needs `npm ci` and a copy of the schemas, or the web line cannot
run and `make test-schemas` prints PASS having skipped everything. Docker does not
work on this machine and nothing needs it.

## What this task does not do

- **No new backend.** One implementation, three shapes.
- **No separate processes.** The mesh models the separation; it does not deploy
  it. `-entity` is gone and stays gone.
- **No two-phase commit.** It would recreate the cross-entity atomicity this
  sub-project exists to remove, and real settlement does not work that way.
- **No reconciliation surface.** The harness is a test instrument. Making a break
  visible to an operator is Task 19, which is also what finally reads
  `SettlementAdvice.ClosingBalance`.
- **No schema validation of golden files.** `iso20022/testdata/xsd/` is absent for
  licensing reasons and every subtest skips. A skip is not a pass.
