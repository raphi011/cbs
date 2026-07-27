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
- `web/src/lib/quiz/chapters/*.ts` — the 15-chapter quiz.
- `store/pg/schema/0001_init.sql` — the relational mapping. Its comments are
  domain content, not implementation notes: which key is composite and why, why
  no balance is stored, why `entries` needs an ordering column, why the audit
  table has no foreign key. Chapter 15 and the README's _Persistence_ section
  teach exactly these claims, so a schema change is a documentation change.

When you correct a domain fact in one layer, check and fix the same claim in the
others.

Two mechanical rules that the build will not catch for you:

- A `[[wiki-link]]` to a key that is not in `hint-content.ts` throws at runtime
  under `RootLayout` and takes **every** route in the dev app down. It is
  guarded on `NODE_ENV !== "production"`, so `next build` stays green. `npm run
  test` catches it; run it, and load a page.
- `web/src/lib/quiz/diversity.test.ts` holds each chapter to 18–22 questions,
  ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty
  tiers.

## Postgres is optional, and that is a load-bearing property

`go test ./...`, `make dev` and `make run` need no database: with no
`DATABASE_URL` everything runs on `store/mem`. `TEST_DATABASE_URL=… go test
./...` (or `make test-pg`) runs the *same* suites against `store/pg`. Both runs
must stay green, and `store/pg` must never accept or refuse a write differently
from `store/mem` — `store/storetest` is the conformance suite that enforces it.
