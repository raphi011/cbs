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
- `store/sqlite/schema/{bank,csm,centralbank}/0001_init.sql` — the relational
  mapping, and the whole schema. There are **three files and one migration
  apiece**, because Task 18 gave each kind of institution a database of its own:
  a member bank's, the clearing house's, the settlement agent's. Each is `0001`
  and nothing is layered on top, because no database is deployed — every one this
  repository meets is ephemeral or a throwaway file, and both migrate from empty.

  Their comments are domain content, not implementation notes: which key is
  composite and why, why no balance is stored, why `entries` needs an ordering
  column, why the audit table has no foreign key, why no `asset` column in any of
  the three carries a `CHECK`. **What is ABSENT is the substance**, and it is
  what the split added: a bank's `payments` has no `cycle_id`, the clearing
  house's `cycles` has no `settlement_id`, the settlement agent has no payments
  table and no customers at all. Chapters 15 and 16 and the README's
  _Persistence_ section teach exactly these claims, so a schema change is a
  documentation change.

  `bank/0001_init.sql` is the canonical home for the arguments that span two
  files or three — the seq allocation rule, the absent `CHECK` on an asset code,
  the absent parent foreign key — and the others name them at the point they
  apply rather than restating them. Copying is how one fact ends up in nine
  places and then in three versions.

  **Where a comment goes is load-bearing here and it was not under Postgres.**
  SQLite stores a statement's text in `sqlite_master`, so a comment INSIDE the
  parentheses reaches a schema dump and one ABOVE the statement is dropped
  silently. Every argument about something the schema does NOT do therefore
  lives inside the statement it concerns — that is what `COMMENT ON COLUMN` used
  to buy for free, and an absent constraint has no column to hang one from.
  `TestSchemaArgumentsReachSqliteMaster` fails if one moves back to column 0, and
  it is three cases now, one per shape.

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

## N+2 stores, and they need no setup

`go test ./...`, `make dev` and `make run` need no database and there is no
second run to keep green. `store/pg` is gone, and with it `TEST_DATABASE_URL`,
`docker-compose.yml` and the `-pg` Makefile targets; `store/mem` is gone too.
What replaced both is `store/sqlite`, on `modernc.org/sqlite` — real SQLite
transpiled to Go, so the module gained Go dependencies and lost every external
one.

**How many databases a deployment has is N+2**: one per member bank, the
clearing house's, and the settlement agent's. No statement spans two of them, a
bank reading another bank's rows finds nothing, and a method reaching for a table
its institution's schema does not create is refused by name
(`sqlite.ErrNotInThisShape`). That is what Task 18 bought, and it is why the
`banks` table lives only in a bank's own database while `settlements` lives only
in the central bank's.

`store/testenv` has **two entry points** because there are two kinds of suite.
`New` opens ONE member bank's database, which is what the `ledger`, `deposit`,
`product` and `lending` suites want: none of those four layers knows what an
institution is, so a set of databases would be more than they have anything to
say about. `NewSet` opens the whole system, which is what `payment`, `mesh`,
`api`, `seed` and `cmd/server` want, because every one of them drives more than
one institution.

**Nothing in the domain may read across two institutions**, and the instrument
that checks the books agree anyway is `payment/recon` — the reconciliation
harness, which opens all N+2 databases at once precisely because no actor in the
system may. It is test-only by convention: `mesh/recon_test.go` calibrates it
against five deliberately broken states and `seed` runs it over the widest
deployment. If a change could make two institutions' books disagree, that is the
test to add.

**Nothing cross-checks the SQL any more.** `store/mem` was the oracle
`store/sqlite` was certified against and `store/pg` was the oracle before it;
both are gone, so anything that needs proving against a second implementation has
to be proved before it lands. What replaces the cross-check for the two failures
that are otherwise silent is a guard each, in `store/sqlite/sqlite_test.go`:
foreign keys are really enforced, and there is exactly one non-primary-key unique
index.

`store/storetest` is no longer a conformance suite and its doc comment says so.
It is the shared suite — written against `Store` and `Tx`, naming no table and no
dialect — and Task 18's three store shapes each run it.

Three things about the surviving store are worth knowing before changing
anything near it:

- **A real `BEGIN`/`COMMIT`.** "These writes commit or roll back together" is a
  claim about the code now rather than a side effect of one process-wide mutex,
  and it is true on a fresh checkout with no Docker.
- **Each `0001_init.sql` records its reasoning INSIDE each statement's
  parentheses**, because SQLite drops a comment that sits above one. See the
  section above.
- **The ephemeral store hides read-then-write defects.** On it a retry's read
  blocks until the winner commits, so a loser reaches the domain's guard however
  the code underneath behaves; only a file, under WAL, lets a reader past an
  uncommitted writer. Anything you measure about ordering must open a file.
  `TestTheRetryBudgetOutlastsASlowWriter` is the one that does.
