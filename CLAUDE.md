# CLAUDE.md

Guidance for Claude Code when working in this repository (a core banking system
plus a quiz and web UI).

The teaching book _How Money Moves_ that used to live in `book/` has moved to the
_Lead Engineer's Field Guide_ (the `second-brain` repo, Part IX) and is no longer
maintained here.

## Domain knowledge stays consistent across layers

The banking/accounting/payments content is duplicated, by design, across:

- `README.md` — the authoritative source.
- `web/src/components/hint-content.ts` — distilled from the README.
- `web/src/lib/quiz/chapters/*.ts` — the 18-chapter quiz.
- `store/pg/schema/0001_init.sql` — the relational mapping, and the whole
  schema: there is one migration, because no database is deployed and the asset
  dimension was folded in rather than layered on. Its comments are domain
  content, not implementation notes: which key is composite and why, why no
  balance is stored, why `entries` needs an ordering column, why the audit table
  has no foreign key, why the four `asset` columns carry no `CHECK` (recorded
  with `COMMENT ON COLUMN`, in the database, because a missing constraint is
  invisible in a schema dump). Chapters 15 and 16 and the README's _Persistence_
  section teach exactly these claims, so a schema change is a documentation
  change.

When you correct a domain fact in one layer, check and fix the same claim in the
others.

Two mechanical rules that the build will not catch for you:

- A `[[wiki-link]]` to a key that is not in `hint-content.ts` throws at runtime
  under `RootLayout` and takes **every** route in the dev app down. It is
  guarded on `NODE_ENV !== "production"`, so `next build` stays green. `npm run
  test` catches it — in hint bodies *and* in quiz explanations, which the
  runtime guard does not scan (see `concept-links.test.ts`). Run it, and load a
  page.
- `web/src/lib/quiz/diversity.test.ts` holds each chapter to 18–22 questions,
  ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty
  tiers.

## One store, and it needs no setup

`go test ./...`, `make dev` and `make run` need no database and there is no
second run to keep green. `store/pg` is gone, and with it `TEST_DATABASE_URL`,
`docker-compose.yml` and the `-pg` Makefile targets; what replaced it is
`store/sqlite`, on `modernc.org/sqlite` — real SQLite transpiled to Go, so the
module gained Go dependencies and lost every external one.

`store/mem` is still here and still the default `store/testenv` hands the suites,
for one more task: it is the oracle `store/sqlite` was certified against, and
Task 17.3 deletes it. Until then `store/storetest` holds both to the same
answers, and neither may accept or refuse a write the other does not.

Two things are worth knowing while both exist, because they are the reason the
swap happened at all. `store/sqlite` runs a real `BEGIN`/`COMMIT`, so "these
writes commit or roll back together" is a claim about the code rather than a
side effect of `store/mem`'s single process-wide mutex — and it is now true on a
fresh checkout with no Docker. And `store/sqlite/schema/0001_init.sql` records
its reasoning INSIDE each statement's parentheses, because SQLite drops a comment
that sits above one; a comment moved to column 0 leaves the database silently,
which is what `TestSchemaArgumentsReachSqliteMaster` exists to catch.
