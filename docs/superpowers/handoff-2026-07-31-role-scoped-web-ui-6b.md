# Handoff: sub-project 6b, the role-scoped web UI

Paste the block below into a fresh session in `~/Git/cbs`.

---

I want to build sub-project **6b** from `docs/expansion-roadmap.md`: the
**role-scoped web UI**. The design is agreed and committed —
`docs/superpowers/specs/2026-07-31-role-scoped-web-ui-design.md`. Read it first;
it is the requirements, and its decisions were made deliberately, so treat a
disagreement as something to raise rather than to route around. It was revised
against 6a, so read the sections marked *revised for 6a* as replacing what the
document originally said.

**Start by writing the implementation plan** with the `superpowers:writing-plans`
skill. Do not start implementing until the plan exists and I have reviewed it.

There is an older plan at `docs/superpowers/plans/2026-07-31-role-scoped-web-ui.md`
(commit `c91c092`). It is **partly superseded and must be rewritten, not
patched** — it predates the operator split and assumes three personas and a
single backend. Its header says which tasks survive. Two findings in it are
still correct and still cost real work, so carry them into the new plan:

- The route move touches ~25 link sites in components/pages **plus ~35
  `explore.href` values across seven quiz chapter files**, governed by
  `EXPLORE_ROUTES` in `web/src/lib/quiz/index.ts`, which `quiz/index.test.ts`
  already enforces. Nothing checks that an allowlisted route names a real page;
  the spec's nav-integrity test must.
- `StatementTable` renders an `AccountRef` per contra leg and an expandable
  "Underlying GL transaction" panel linking into `/bank/[pid]/accounts/[aid]`.
  Reused whole in the customer's Activity screen that leaks the bank's chart of
  accounts and navigates out of the persona. It needs a `retail` variant.

## What just landed that you build on

**Sub-project 6a, the operator-split API, is done** (`spec/operator-split-api`,
12 commits, unmerged and unpushed). Read
`docs/superpowers/specs/2026-07-31-operator-split-api-design.md` for the
reasoning; the facts you need:

- **Each entity has its own listener.** One binary, one process by default:
  `:8081` central bank, `:8082` clearing house, `:8083`+ one per member bank in
  roster order. `make dev` is unchanged as a command.
- **The web proxy routes by operator.** `/api/<operator>/<path>`, where operator
  is `central-bank`, `clearing-house` or `bank/<pid>`. Build those paths with
  `cb()`, `csm()`, `bank(pid, …)` from `web/src/lib/api/operator.ts` — never
  hand-write one.
- **`web/src/lib/api/endpoints.ts` is already fully re-pointed.** 6b does not
  redo that. What it adds is the *directory* endpoint and hook, which do not
  exist yet in the web layer at all.
- Route changes that matter to the UI: `GET /me` replaces
  `GET /participants/{pid}`; `POST /members` (central bank) admits a bank and
  `GET /members` (clearing house) is the roster; `POST /settlements` on the
  central bank takes `{"cycleId": …}` and replaces `POST /cycles/{cid}/settle`.
- **A bank's `GET /payments` returns only its own legs**, and `GET /payments/{id}`
  is 404 for a payment it is not party to. `/bank/[pid]/payments` is a new screen
  that could not have existed before.
- **A bank's `POST /payments` accepts a customer instruction** and answers
  `202 Accepted` with `{"paymentId": …}`; the outcome comes from
  `GET /payments/{id}`. The customer send form must be built around that shape —
  not a synchronous "payment created" response — because 7b makes it genuinely
  asynchronous.
- **A bank serves `GET /directory`.** The customer's payee lookup goes there,
  never to the clearing house: a retail client has no CSM connection.
- **Ports are static; admission is not provisioning.** A bank created at runtime
  has a store row and no listener until restart. The proxy answers 502 with a
  message saying so. The lobby and picker must render such a bank as *awaiting
  provisioning* rather than offering a dead console.
- The seed's participant ids are `bank_1`, `bank_3`, `bank_5`, `bank_7` —
  **not contiguous**. Nothing may infer a port from an id, only from roster
  position.

## Things about this repo that will bite you

- **A `[[wiki-link]]` to a key absent from `web/src/components/hint-content.ts`
  throws at runtime under `RootLayout` and takes _every_ route in the dev app
  down, while `next build` stays green.** `cd web && npm run test` catches it, in
  hint bodies *and* quiz explanations. Run it, and load a page in each shell.
  The key for the available balance is **`balance-available`** — the spec's prose
  once said `available-balance`, which is not a key.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions,
  ≥8 distinct concept tags, no tag more than 3×, all three difficulty tiers.
- **Vitest is node-environment, `src/**/*.test.ts` only** — no `.tsx`, no DOM.
  `lucide-react` and `next/navigation` both import cleanly under it (verified).
- **`go test ./...` must stay green with no `DATABASE_URL` and against Postgres.**
  6b is frontend-only, but the rule is absolute.
- The web deps may need `cd web && npm install` in a fresh worktree.
- Domain facts are duplicated across four layers on purpose (`CLAUDE.md`):
  `README.md` is authoritative, then `hint-content.ts`, the quiz chapters, the
  schema comments. 6b moves screens, not facts — but `web/CLAUDE.md`'s Routing
  paragraph is stale and must be corrected.

## Environment

- **Postgres is a local Homebrew `postgresql@18` service**, not the Docker
  container: `postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable`. Start it
  with `brew services start postgresql@18`. The `cbs` role and database already
  exist. The docker-compose `cbs-pg` container exists but is stopped, and colima
  was flapping badly — prefer the local server, and set `TEST_DATABASE_URL`
  directly rather than using `make test-pg`.

## Three lessons from 6a worth carrying in

All three cost rework, and the third is now the fourth time this project has hit
it:

1. **A scripted edit that matches nothing must fail, not pass silently.** The
   `-entity` narrowing was written into `main`, compiled, and did nothing — a
   Python `.replace()` matched zero times because a variable had been renamed in
   an earlier task. Only a live probe caught it. Assert on every scripted
   replacement. 6b will script the route move, so this applies directly.
2. **A plan that calls a commit "additive" should name what would make it not
   so.** 6a's plan asserted the surfaces could land alongside the old mux; they
   could not, because the registrations were shared. It cost a throwaway
   abstraction to recover.
3. **Prose that asserts what code does needs a test, not a careful reading.**
   Four times now on this project.

Ask me before starting if anything in the spec reads as wrong rather than merely
unexpected.
