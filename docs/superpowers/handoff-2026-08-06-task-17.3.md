# Handoff — sub-project 9 complete, `spec/sqlite-only-store`, Task 18 next

16 commits ahead of `main` (`48f2cdb`), unpushed; 108 files, +7376/−7072.
Everything below was verified against the code at `261be19`, not against a plan.

Sub-project 9 — *one store, and it is SQLite* — is **done**. `store/pg` and
`store/mem` are both gone and `store/sqlite` is the only implementation. Task 18
is next and **its plan is written**:
`docs/superpowers/plans/2026-08-06-task-18-split-the-stores.md`.

## Verification, run by the controller — not by a subagent

All five lines green at `261be19`:

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./... -count=1                                              # 11.2s wall
go test ./store/sqlite/ -count=10 -run 'TestRaces|TestConcurrentTxRaces'   # 4.7s
go test ./mesh/ ./api/ ./cmd/... ./store/sqlite/ -race -count=1
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

**There is one Go run.** The `TEST_DATABASE_URL` line is gone — see the expiring
rules below.

The `-race` run takes about **four minutes**: mesh 39.8s, api 168.8s,
cmd/server 13.1s, store/sqlite 13.8s. It exceeds a two-minute command timeout, so
run it in the background or raise the timeout rather than concluding it hung.

`make test-schemas` passes with **0 skips** (27 subtests) on this checkout.
The web suite is 180 tests in 13 files.

## Two things a fresh worktree needs, and neither is in git

Unchanged from Task 17's handoff and still true:

1. **The ISO 20022 schemas.** `iso20022/testdata/xsd/` holds all ten and is
   gitignored. Without them `make test-schemas` skips every subtest and **still
   prints PASS**. `cp -R ~/Git/cbs/iso20022/testdata/xsd <worktree>/iso20022/testdata/`.
2. **`web/node_modules`.** `cd web && npm ci`. Without it `tsc`, `eslint` and
   `vitest` are all absent, so the web line cannot run at all — and an agent may
   report a run it made somewhere else. That has happened on this project.

**Docker does not work on this machine, and nothing needs it any more.**

## Rules that expire here, stated rather than dropped from a copied list

Task 17's handoff retired two rules at 17.1 because the spec put that obligation
on it explicitly. This is the same obligation one document later.

**Expired, and already gone from the tree:**

- **`TEST_DATABASE_URL`.** Nothing reads it. The variable is inert; `make
  test-pg`, `make db-up`/`db-down`, `make dev-pg`/`run-pg` and
  `docker-compose.yml` no longer exist. Do not add a second Go run.
- **"`store/mem` is the reference implementation; where the two disagree, `mem`
  is right by definition."** Gone wholesale, with `store/mem`.
- **"`store/pg` must never accept or refuse a write that `store/mem` handles
  differently."** This was the rule most of the repository's schema arguments
  were written from. It has no successor, and every argument that rested on it
  has been re-justified from the domain — see below.
- **"`store/storetest` is a conformance suite."** It is the **shared suite**, and
  its own doc comment says so. There is nothing left to conform *to*.

**Still true, and it dies inside Task 18:**

- **"There is one migration. Edit `store/sqlite/schema/0001_init.sql` in place."**
  It survives until **18c**, which gives each of the three store shapes its own
  file. Until then, edit in place. Note the path: it is `store/sqlite`'s, not
  `store/pg`'s.

## What 17.3 did, and the larger half was the prose

**The code half is four files.** `store/mem` deleted; `testenv.New` returns
`*sqlite.Store` and `OpenSQLite` collapsed into it. Returning the concrete type
is a consequence of there being one implementation: `storetest.Store` names four
layers and not five, so while two existed the interface was the ceiling and
`lending/portfolio_test.go` opened `store/mem` by hand to get above it.
`store/mem/mem_test.go`'s cases all have twins in
`store/sqlite/conformance_test.go`, so nothing was lost with the file.

**The sweep is 77 files.** The rule applied to every claim: a sentence saying
`store/pg` needed a `SAVEPOINT` and SQLite does not is history and stays, in the
past tense; a sentence saying a `CHECK` here would make one store refuse what the
other performs is a live argument that lost its subject and is re-justified from
the domain. The spec's *Rulings reversed* table had already done that work for six
of them and was used rather than re-derived.

The canonical re-justifications, each stated once and pointed at from the rest:

- **`accounts.asset`** carries the schema's whole argument for why no column
  enumerates a set that lives in Go. The dual-store half expired; the half that
  never mentioned a second store — a `CHECK` makes a one-line change to a Go
  slice a **migration**, with the database's copy deciding — is now all of it.
- **Absent constraints generally:** enforcing a domain rule twice means enforcing
  it in two places that answer differently. The constraint fires first, and as a
  constraint violation, where the domain would have named what it refused.
- **`ledger.ValidateText`** keeps README:1271's own replacement reason — a rule
  that can only be stated by naming a database is not a domain rule. SQLite holds
  a NUL and invalid UTF-8 as happily as `store/mem` did, so nothing below the API
  refuses them and the rule is enforced there or nowhere.
- **`day_key`** keeps its ruling with a *stronger* reason: SQLite has no range
  exclusion either, so it is no longer "one backend could not" but "no backend
  here can".
- **The `SAVEPOINT`-per-sentinel rule** has no successor. `ledger.Tx` states the
  promise it was protecting; SQLite rolls back the failed statement.

**The schema's arguments still reach the database.** Dumped `sqlite_master` from
a migrated database and diffed every argument against the source: **all 1156
indented comment lines present, 0 missing.** That is the method 17.1b specified
and it is the acceptance criterion Task 18 inherits, three times over.

## The measurement that matters most to Task 18

**The read-then-write ordering guards no longer decide anything on this
backend**, and the suite cannot see them go.

Measured in the real configuration (retry in place), with a temporary
file-backed copy of `RunRaces` beside the existing ephemeral one:

| break | memdb | WAL file |
|---|---|---|
| `payment.admissionSequenceTx` returns nil | PASS 10/10, all 4 cases | PASS 10/10 |
| `SubmitPaymentTx`'s `NextID` moved behind the duplicate check | PASS 10/10 | PASS 10/10 |

Both were `store/pg` divergences: 50-of-60 and 8-of-8. SQLite admits one writer,
so a loser is refused at its write and `Store.Update` re-runs the unit of work
against the winner's committed row.

**What that does NOT say.** With `isTransient` stubbed false, every failure in
either probe is a raw `SQLITE_BUSY` rather than a divergence, so the probe cannot
separate "the ordering saved it" from "the retry saved it" once the retry is
gone. The table above is exactly what was measured and the comments say no more
than that. The orderings stay.

**A correction that is part of this handoff.** The sweep first wrote, in six
places, that Task 18 puts the counter and the row into different databases and
used that to explain why the orderings stay. I had not established it and it is
probably false: each admission act belongs to exactly one institution, so if
`ledger.NetworkBook`'s counter becomes each entity's own the ordering survives
untouched. `2ed68ad` replaces the assertion with the question. **Task 18's plan
answers it: the counter follows the row.**

## Things to carry into Task 18, each measured

- **Nothing cross-checks the SQL any more.** `store/mem` was the oracle
  `store/sqlite` was certified against and `store/pg` was the oracle before it.
  Anything needing proof against a second implementation gets proved before it
  lands. What replaces the cross-check for the two failures that are otherwise
  silent is a guard each in `store/sqlite/sqlite_test.go`: foreign keys really
  enforced, and exactly one non-primary-key unique index.
- **The ephemeral store hides a class of defect.** A retry's read blocks on
  `memdb` until the winner commits, so a loser reaches the domain guard however
  the store behaves underneath — `LockAccounts` emptied passes
  `ConcurrentPostingsCannotBothSpendTheSameBalance` ten runs out of ten. Only WAL
  lets a reader past an uncommitted writer. **Anything about read-then-write
  ordering must open a file.** `TestTheRetryBudgetOutlastsASlowWriter` and
  `TestConcurrentPostingsOnAFileReachTheDomainGuard` are the two that do.
- **Two of the nine races do not bite on one run** —
  `ConcurrentAdmissionsAgreeOnOneCentralBank` and
  `ConcurrentMarkReversedOnlyOneWins`. Use `-count=10` for anything resting on
  either.
- **A `[]byte` is a BLOB and a `STRICT TEXT` column refuses it (3091).** Every
  JSON helper returns `string`.
- **Every `INSERT` allocates its own `seq` as `MAX(seq)+1`, and no `ON CONFLICT`
  branch may touch it.** `storetest` covers ledgers, subledgers and accounts; the
  other tables are still uncovered, and three schemas will be three chances to
  get it wrong.
- **`sqlite_master` holds only comments inside a statement's parentheses.**
  `TestSchemaArgumentsReachSqliteMaster` names four arguments and fails if one
  moves to column 0.
- **`LockAccounts` is a documented no-op** in `store/sqlite`, and a write-taking
  version was implemented, raced through `ledger.Book.PostTransactionTx` on a WAL
  file at four hold lengths, and measured at identical outcomes. Its doc records
  that; do not reintroduce it on the assumption that a lock must be doing
  something.

## Known defects and deferrals carried

Unchanged from Task 17's handoff and none of them touched by sub-project 9:

- **`payment/system.go`'s deposit refusal says "is founded and not yet
  admitted"**, false in the partly-admitted state. Task 19's.
- **A partly-completed multi-asset admission cannot be re-driven.** Task 19's.
- **`csm.held` does not survive a restart.**
- **A cycle that nets to zero strands every payment in it, for ever** — still the
  most serious thing nobody owns.
- **An on-us payment has nowhere to go.**
- ~25 positional "option C/D" references in quiz explanations.
- `iso20022`'s `GenericAccountIdentification1/Id` presence-only validation.

New, and **carried deliberately rather than fixed inside a documentation sweep**:

- **A third open crossing, on the happy path.** `SubmitPaymentTx` reads the
  counterparty's `Bank` row for its BIC (`payment/system.go:2683`) and
  `checkPartyTx` reads it again for the counterparty's book (`:4186`). Bank rows
  are network-scoped today, which is exactly why the recorder cannot see it;
  under the split a bank holds only its own row and neither read has an answer.
  It is not in the spec's crossing table. **Task 18a closes it.**
- **`docs/cbs-vs-book.md` item 5 is half closed.** The dual-store half of the
  no-`CHECK` argument is gone; what is still open is that neither README nor
  schema says a single-backend production bank would be advised the opposite
  (deferred balance triggers, revoked `UPDATE`/`DELETE` grants).

## What has NOT run on this branch

**No whole-branch review.** Task 15's precedent is the reason to say so plainly:
the whole-branch review is what found a Critical money bug that per-task review
structurally cannot see, and Task 17's found four. Sixteen commits, 108 files,
and a documentation sweep that rewrote arguments in 77 of them is exactly the
shape that review exists for. It should run before this merges.

The sweep's own highest-risk output is the prose, and the method that produced it
is `docs/superpowers/2026-08-06-avoidable-review-cycles.md`'s: grep only to
*locate*, never to conclude; flow comment blocks before matching; grep for the
sentence you **deleted**. That found one live copy (`product/store.go`'s
"dual-store reason") after four earlier greps had missed it because the wording
varied — which is the file's second mechanism, caught rather than shipped.

**Files read in full during the sweep**, so the next reviewer knows what was
skimmed: the sub-project 9 spec, the 17.1 plan, the process file, `CLAUDE.md`,
`web/CLAUDE.md`, `store/testenv/testenv.go`, `store/mem/mem_test.go`,
`store/sqlite/sqlite_test.go`, `store/sqlite/conformance_test.go`,
`0001_init.sql`'s header and conventions through line 300, and README's
*Persistence* chapter. Everywhere else, every comment block containing a hit was
read with ±22/14 lines of context and the file re-grepped for the deleted
phrasing. `store/storetest/storetest.go` is 2042 lines and was read to 1199.

## What the next task is

**Task 18 — split the stores**, and the plan is written:
`docs/superpowers/plans/2026-08-06-task-18-split-the-stores.md`.

Five sub-tasks rather than the spec's two, because 32 tables, 26 interface
methods, 20 non-test call sites that stop having an answer and 83 recorder
expectations in one file would put a failing conformance case and a dead API
route in one review. **18a and 18b land no split at all** — the three crossings
close and `payment.Network` gains an identity while the recorder that measures
them still works.

Three corrections to the db-per-entity spec are in the plan, each measured, and
one ruling the spec does not have: **`ledger.NetworkBook` is deleted and the
counter follows the row.** Four open questions are listed there and are to be
answered by running things rather than by reasoning — the first of them,
what `RunRaces` means across three stores, in the same way 17.1d established
whether SQLite met `RunConcurrentTxRaces`' precondition.

Read the plan's *Global constraints* before starting. The one that has cost this
project most: **a plan specifies behaviour, interfaces and tests, and never
contains comment text.** Where a doc must make an argument, the plan names it in
one line and the implementer writes the prose from the code that ended up
existing.
