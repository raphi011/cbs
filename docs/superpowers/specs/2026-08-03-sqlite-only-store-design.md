# Design — sub-project 9: one store, and it is SQLite

Tasks **17.1 / 17.2 / 17.3**, landing between Task 17 (admission becomes a
conversation) and Task 18 (split the stores) of the db-per-entity sub-project.

Decimal rather than `17a/b/c`: this repository uses letters for parts *of* a task
(`15a`, `15b.1`), and this is not part of admission. The numbering is chosen so
that nothing renumbers — `2026-08-02-db-per-entity-design.md` names "Task 18" in
six places and it stays Task 18.

`store/pg` and `store/mem` are both deleted. What replaces them is one backend,
`store/sqlite`, on `modernc.org/sqlite` — the real SQLite, transpiled to Go, no
cgo. The goal is stated plainly because it is easy to misstate: the module gains
Go dependencies (`modernc.org/libc`, `/mathutil`, `/memory`, `golang.org/x/sys`)
and loses every **external** one. No server, no Docker, no C toolchain, no
cross-compilation story.

## Why here, and not after Task 18

Three reasons, in order of weight.

**Task 18 is where the store multiplies.** It makes three store shapes —
`bank`, `csm`, `centralbank` — and with two backends that is six implementations
and three conformance suites. Porting a backend afterwards is three times the
work of porting it now, when there is one shape.

**The Postgres plan already pays a compromise that Task 18 would inherit.** The
db-per-entity spec settles on "one schema per entity in one database, so
`TEST_DATABASE_URL` keeps working" — which is namespacing, for a sub-project whose
thesis is that the boundary belongs in the DDL. Under SQLite it is one file per
entity, and the compromise is never designed rather than designed and later
discarded.

**Dropping `store/mem` halves Task 18**, and dropping it is only defensible once
the surviving backend needs no setup. That is the whole of the argument for
SQLite over Postgres here: `store/mem` exists because `go test ./...` on a fresh
checkout must need nothing, and a cgo-free file-or-memory database satisfies that
condition directly instead of by having a second implementation.

Tasks 16 and 17 are domain conversations on one store and are backend-agnostic.
They run first. They add DDL either way, and that DDL is small beside translating
the whole schema once.

**Where that leaves things when this spec is written.** Task 16 is implemented on
`spec/db-per-entity` at `4ac2bcc` and **not merged**: its documentation sweep
(16f) is unstarted and no whole-branch review has run, and its handoff is explicit
that neither is a leftover — on Task 15 the whole-branch review is what found a
Critical money bug that per-task review structurally cannot see. So the work
ahead of the swap is 16f, that review, the merge, and then Task 17. None of it is
affected by the backend, which is the point of doing it first.

One consequence for 16f specifically: it sweeps `0001_init.sql`'s comments along
with the other layers, and 17.1 then translates that file whole. That is not
wasted work — the comments are the content and they carry across — but 16f should
write the *reasoning*, not the Postgres mechanism, wherever it has the choice.

## The three tasks

The order inside the swap matters more than it looks.

| # | task |
|---|---|
| 17.1 | **Add `store/sqlite`.** All three backends green under `store/storetest`. Nothing is deleted |
| 17.2 | **Delete `store/pg`.** With it: `docker-compose.yml`, `make dev-pg/run-pg/test-pg/db-up/db-down`, `TEST_DATABASE_URL`, the `pgx` dependency |
| 17.3 | **Delete `store/mem`.** `testenv` opens SQLite unconditionally. The documentation sweep |

17.1 is first because `store/mem` and `store/pg` are the **oracles**. The
conformance suite's last job is to certify its own replacement; run in the other
order and the new backend is validated by nothing. This is the one sequencing
constraint in the sub-project that cannot be relaxed.

17.2 and 17.3 are separate commits because they reverse different rulings and
because a bisect should be able to land between them.

## The store

`database/sql` with the `modernc.org/sqlite` driver. Everything the connection
needs rides in the **DSN**, never in a post-`Open` `Exec`: the pool hands out many
connections and a one-shot statement configures exactly one of them, then looks
like it worked.

- **`_pragma=foreign_keys(1)`.** SQLite ignores `REFERENCES` unless this is on,
  per connection, silently. The schema has roughly twenty
  `REFERENCES … ON DELETE CASCADE`. A silent, per-connection, default-off
  constraint is the worst failure shape in this design and it gets a test of its
  own (below).
- **`_pragma=busy_timeout(…)`** and **`_pragma=journal_mode(WAL)`** for the
  file-backed case.

**Tests use `file:<unique>?mode=memory&cache=shared`**, not `:memory:`. A bare
`:memory:` gives *each pooled connection its own database*, which presents as a
store that forgets writes at random. A shared-cache memory database is discarded
when its last connection closes, so the store retains one connection for its
lifetime. The unique name is what isolates one test from another, and under Task
18 it becomes N+2 named databases per test — so the naming scheme belongs to
`store/testenv`, which is the package that already answers "which store does this
suite run against".

**The server** takes a file path, or empty for memory. `cmd/server`'s `-database`
flag keeps its name and its meaning changes: empty is still ephemeral and still
needs no setup, so `make dev` and `make run` are untouched.

## Errors, and one deletion

**`inSavepoint` goes away — all thirteen uses.** SQLite does not abort a
transaction on a constraint violation; the statement rolls back and the
transaction survives. The rule at `tx_ledger.go:66-75` and README:1273 — "any
statement whose SQLSTATE this package maps to a sentinel has to cost the caller
one statement rather than the whole transaction" — describes a *Postgres*
problem. Here it costs one statement natively. The rule was right, its mechanism
was Postgres's, and both go.

**`isUniqueViolationOn(err, "transactions_idempotency_key_idx")` has no direct
port.** SQLite names no index in its error. It does not need to: a primary-key
conflict returns extended code `SQLITE_CONSTRAINT_PRIMARYKEY` (1555) and a
unique-index conflict returns `SQLITE_CONSTRAINT_UNIQUE` (2067), and `pg.go:281`
already establishes that **the idempotency index is the only unique constraint in
the schema**. So 2067 alone identifies it — exactly as targeted as matching the
name, with no message parsing.

That is a whole-schema invariant standing in for a local one, so it gets a guard:
a test reading `sqlite_master` that asserts exactly one non-primary-key unique
index exists. Add a second and the sentinel silently starts answering
`ErrDuplicateIdempotencyKey` to an unrelated collision.

Mechanical, and nothing to decide: `pgx.ErrNoRows` → `sql.ErrNoRows` (about ten
sites); `isTransient`'s `40001`/`40P01` → `SQLITE_BUSY`/`SQLITE_LOCKED`, mostly
absorbed by `busy_timeout`; `ErrReadOnly` and `ErrNestedTransaction` are already
Go-side guards and do not move.

**`migrate.go` loses its advisory lock.** No SQLite equivalent, and none needed:
one writer. The rest of it — an applied-set table, `go:embed`ded `.sql` in
filename order, one transaction each — is unchanged, including the limitation it
documents about keying on filename with no checksum.

## The schema

**`STRICT` tables.** SQLite enforces no declared type otherwise, and a schema
that enforces nothing is worse than one with a coarser vocabulary. The cost is
real: `STRICT` permits only `INT`, `INTEGER`, `REAL`, `TEXT`, `BLOB` and `ANY`, so
`BIGINT`, `SMALLINT`, `TIMESTAMPTZ` and `JSONB` disappear as *spellings*. What
they were saying moves into comments and `CHECK`s.

**Times become `TEXT`: RFC3339, normalized to UTC, fixed fractional digits.** Not
an integer epoch, because this is README:1297's argument reused — "an ISO day
sorts lexicographically, so a string comparison *is* a day comparison" — applied
to full timestamps instead of day keys. The normalization is load-bearing: mixed
offsets or variable precision and lexicographic ordering stops being
chronological ordering, silently, in listings. Nullability is unchanged, and
`ORDER BY … NULLS FIRST` is supported (3.30+); SQLite also sorts NULL first in
`ASC` by default, which is what the existing convention wants.

**JSONB becomes `TEXT CHECK (json_valid(col))`.** JSON1 is compiled into modernc.
Worth noticing what happened: that `CHECK` is a constraint `store/mem` could
never have held. It is the first place dropping `store/mem` *buys* something
rather than costing something.

**`SERIAL` becomes an explicit `seq`.** `AUTOINCREMENT` works only on an
`INTEGER PRIMARY KEY`, and `seq` is not the key — book-scoped tables are keyed on
`(book_id, id)`. So `MAX(seq)+1` on insert, which is where README:1295 already
argues everything else belongs: "identity counters are ordinary rows, not
Postgres `SEQUENCE`s. A sequence survives a rollback on purpose." `seq` was the
one place that rule was not applied. Now it is, and `seq` becomes gap-free.
Single-writer semantics make `MAX+1` safe without further thought.

**Partial indexes** — three of them, on idempotency keys, end-to-end ids and open
cycles — are supported unchanged.

**`COMMENT ON COLUMN`, thirty-five of them, become comments inside the
`CREATE TABLE` parentheses.** `sqlite_master.sql` stores the text of the original
statement, so comments within a statement's span should survive into `.schema`
output — which is the whole property CLAUDE.md is protecting when it says the
reasoning is recorded "in the database, because a missing constraint is invisible
in a schema dump". **This is verified first, in 17.1, before the translation is
written.** If it does not hold, the ruling reverses for real and the sub-project
needs a different answer for it — the reasoning does not simply move to a
Markdown file, because the point was that it travels with the schema.

## Rulings reversed

Written down as reversals, per this repository's convention: a reversed ruling
that looks like an oversight is worse than the original ruling. The pattern is
that most *decisions* survive and their *reasons* change, which is itself the
finding — a rule that could only be stated by naming a database was never a
domain rule.

| ruling | outcome |
|---|---|
| **`store/mem` is the reference implementation; where the two disagree, `mem` is right by definition** (README:1252) | gone, wholesale |
| **No `UNIQUE (book_id, name)`** — "a constraint one store could hold and the other, a Go map, could not" (`0001_init.sql:38`, README:1265) | decision kept, re-justified from the domain: two customers called John Smith at one bank is not an error |
| **No `CHECK` on `accounts.asset`** (README:1319, roadmap:118-125) | decision kept: `storetest`'s `ParentReferencesAreNotEnforced` writes accounts with no asset at all, the rule lives in `ledger.Book.CreateAccountTx`, and a `CHECK` would turn a one-line change to a Go slice into a migration — that second reason never mentioned `store/mem` and is now the whole reason |
| **`day_key` rather than `tstzrange` + a GiST exclusion constraint** (README:1297) | kept, with a *stronger* reason: SQLite has no range exclusion either, so it is no longer "one backend could not" but "no backend can" |
| **`ledger.ValidateText`** — Postgres refuses NUL (22021) and invalid UTF-8, a Go map does not (README:1267-1271) | kept. SQLite stores both happily, so the divergence that prompted it is gone; README:1271 already wrote the replacement reason — "a rule that can only be stated by naming a database is not a domain rule" |
| **The `SAVEPOINT`-per-sentinel rule** (README:1273) | gone; the cost it was equalizing is not charged here |
| **"The module has a dependency at all" because of `store/pg`** (README:1254) | rewritten: more Go modules, zero external dependencies |
| **"There is one migration"** (CLAUDE.md) | already becoming one per shape in Task 18; this sub-project changes the dialect and the advisory lock with it |
| **`store/mem` is one process's memory, so N processes would be N disconnected universes, and Postgres-optional is load-bearing** (roadmap:297-300) | reversed in a direction nobody asked for: a SQLite file under WAL is shared between processes, so `-entity`-per-process no longer needs a server. Recorded, not acted on |

## Testing

**`store/storetest` stops being a conformance suite.** Its doc comment must say
so, or it claims something false. It survives as the shared suite that each of
Task 18's three shapes runs — cross-*shape*, not cross-*implementation*.

What is lost is that nothing independently cross-checks the SQL. Two guards
replace the part of that which mattered, both of them for failures that are
otherwise silent:

- **Foreign keys are enforced.** One test that a `REFERENCES` actually refuses an
  orphan and actually cascades a delete. A dropped `foreign_keys` pragma changes
  no test outcome anywhere else in the suite.
- **Exactly one non-primary-key unique index**, read from `sqlite_master`. The
  `SQLITE_CONSTRAINT_UNIQUE` → `ErrDuplicateIdempotencyKey` mapping depends on it.

Two things get strictly better and should be stated, because they are the
compensation for losing the second implementation:

- `store/testenv`'s doc says `store/pg` "is the only way the cross-layer
  atomicity tests mean anything: under a single process-wide mutex, *these writes
  commit or roll back together* is true whether or not the code arranged for it."
  Real `BEGIN`/`ROLLBACK` is now unconditional, on a fresh checkout, with no
  Docker and no Homebrew Postgres.
- `architecture-review-2026-07-30.md:336-339` flags `LockAccounts` as a no-op
  under `store/mem` whose absence "corrupts money under `store/pg` concurrency",
  guarded by a single test at `pg_test.go:160` that **skips without
  `TEST_DATABASE_URL`**. That guard becomes unconditional too.

**Every measurement is watched failing first**, unchanged from 7b and from
db-per-entity: break the code, watch that exact test fail, restore. This applies
with particular force to the two new guards, whose whole justification is that
they catch something no other test would.

### Verification

```bash
gofmt -l . && go vet ./... && go build ./...
go test ./...
go test ./mesh/ ./api/ ./cmd/... -race
cd web && rm -rf .next && npm run typecheck && npm run lint && npm run test
```

One Go run, not two. During 17.1 only, the `TEST_DATABASE_URL` run still exists
and must stay green — that is the task's entire point.

## Existing documents that change

**Amended when this spec lands**, because Task 16f and Task 17 execute before the
swap and would otherwise be written against the Postgres shape:

- **`specs/2026-08-02-db-per-entity-design.md`**, as a dated section rather than
  an edit, per its own convention: Task 18 becomes three shapes × **one**
  backend; "one schema per entity in one database, so `TEST_DATABASE_URL` keeps
  working" (:398-399) becomes one **file** per entity; "`storetest` becomes three
  conformance suites" (:394) becomes three shape suites.
- **`docs/expansion-roadmap.md`**: ":**Postgres stays optional**" and "needs
  conformance coverage, not just a `store/pg` implementation" (:23-25) are false
  after 17.3; the no-`CHECK`-on-asset argument (:118-125) is stated in the
  mem-versus-pg form this spec re-justifies; the deployment note (:297-300).

**Changed inside the tasks**, with the code: `CLAUDE.md` — both the "Postgres is
optional, and that is a load-bearing property" section and the one-migration
line — `README.md` (the *Persistence* chapter substantially, including the named
section *Two Stores, One Conformance Suite*), `web/src/components/hint-content.ts`,
quiz chapters 15 and 16, `web/src/components/reset-button.tsx`, the `Makefile`,
and `docker-compose.yml`.

**Not touched:** the seven handoffs, the twenty-four plans, and
`architecture-review-2026-07-30.md`. They are dated records of what was true when
they were written; this repository corrects forward.

That cuts against one thing, and it is worth naming rather than leaving to be
tripped over. `handoff-2026-08-03-task-16.md` is not only a record — its closing
*rules that cost the most when forgotten* is the live instruction set for whoever
takes Task 17, and two of its entries expire here: "**There is one migration.**
Edit `0001_init.sql` in place", and the `TEST_DATABASE_URL` line in its
verification block. Both stay as written. The obligation moves to **Task 17's
plan and its own handoff**, which must carry them forward corrected — an
expired rule in a handoff is exactly the "comment written during a transition
should announce its own expiry" lesson that same file records, pointed at itself.

The documentation half is the larger half. This is a curriculum change wearing a
backend change's clothes: the README's persistence argument is *built* on there
being two stores, and every schema decision it defends is defended by naming the
one that could not hold a constraint.

## What this sub-project does not do

- **No new domain behaviour.** Not one accept-or-refuse decision changes. If a
  storetest assertion has to move, that is a defect in the port until proven
  otherwise.
- **No split.** The three shapes are Task 18's. This lands one store, in SQLite.
- **No `zombiezen`/`ncruces` driver comparison.** `ncruces/go-sqlite3` (official
  SQLite on wazero) is also cgo-free and was considered; `modernc.org/sqlite` is
  chosen for being the mainstream `database/sql` driver and for adding no WASM
  runtime. Recorded so the choice does not look unexamined.
- **No performance work.** modernc is slower than a cgo SQLite, and SQLite is
  slower than Postgres at the entry `SUM`s that back every balance. Nothing here
  is sized for that, and the repository has ruled against a stored balance for
  reasons that have nothing to do with the backend.

  One side effect is worth recording rather than acting on:
  `architecture-review-2026-07-30.md:276-287` proposes deriving balances in `ledger`
  over streamed entries, prices it as "a real regression on the `pg` path", and
  marks it Speculative partly because it "pushes against a stated load-bearing
  property — two stores, one conformance suite". Both objections are about
  Postgres and about `store/mem`. After 17.3 neither exists, so that proposal
  becomes cheaper to *consider*; it does not become this sub-project's.
