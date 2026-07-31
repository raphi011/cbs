# Handoff: sub-project 6, the role-scoped web UI

> **Superseded, 2026-07-31.** Sub-project 6 split in two after this was written.
> **6a, the operator-split API** (`specs/2026-07-31-operator-split-api-design.md`)
> gives each entity its own listener and is built first; **6b** is the web UI
> below, revised against it. Read both specs, and take this document only for
> the repo lore in *Things about this repo that will bite you* and *Two lessons
> from sub-project 5* — those still hold in full.
>
> What is now false here, corrected rather than deleted so the reasoning stays
> legible:
>
> - "**This sub-project is frontend-only**" — 6a is entirely backend. The web
>   work is still frontend, but it is no longer the whole of 6.
> - "**A customer send form … does not have to quote an identifier — but the
>   payee lookup still needs `/directory`**" — true, and it now calls the
>   *bank's* `GET /directory`, not the network's. A customer's browser must not
>   talk to the clearing house.
> - The implied persona count. There are **four**: central bank, clearing house,
>   bank, customer. The scheme-operator persona this handoff's spec deferred now
>   ships, because 6a gives it a backend.
> - "**Start by writing the implementation plan**" — done, at `c91c092`, and
>   since partly invalidated by 6a. Re-plan after 6a lands; do not patch it.

Paste this into a fresh session in `~/Git/cbs`.

---

I want to build sub-project 6 from `docs/expansion-roadmap.md`: the **role-scoped
web UI**. The design is already agreed and committed —
`docs/superpowers/specs/2026-07-31-role-scoped-web-ui-design.md`. Read it first;
it is the requirements, and its decisions were made deliberately, so treat a
disagreement as something to raise rather than to route around.

Start by writing the implementation plan with the `superpowers:writing-plans`
skill. Do not start implementing until the plan exists and I have reviewed it.

## What the spec settles, in one paragraph

The web app is one unified dashboard where every screen is reachable from a
single left nav, a bank is a *section* of the app rather than a place you are,
and a customer is a row in a back-office table rather than a point of view. That
flattens exactly the boundaries the rest of the repository takes care to model.
The replacement is **identities you switch between**: three personas — central
bank operator, bank back office, bank customer — on persona-prefixed routes
(`/central-bank/…`, `/bank/[pid]/…`, `/customer/[pid]/[did]/…`), each with its own
shell, chosen from one flat searchable identity picker in the topbar, with `/`
always a lobby.

Read the spec's *Out of scope* list before planning anything: no authn/authz (the
Go API stays open; the scoping is navigational), no scheme-operator persona, no
card-processor persona, no customer mandate or credit screens, no bank-scoped
mandates view, no products screen, and no party master — a customer identity
**is** one deposit account.

## What just landed that you should build on

Sub-project 5 (account addressing) merged to `main` today. It exists because of
this sub-project: a customer sends money to an IBAN, and the system had none.

- `GET /directory?scheme=IBAN&value=…` → `{participant, account, name, asset,
  identifier}`. This is what the customer's send form resolves a typed IBAN
  through before the payment is confirmed. 404 on a miss, 409 if two banks claim
  the address.
- `depositAccountDTO` now carries `identifiers: [{scheme, value}]`, and
  `web/src/lib/types.ts`'s `DepositAccount` has the matching field. Every seeded
  account has exactly one IBAN — the readable `SE89-AURORA-1001` kind.
- `POST /participants/{pid}/deposit-accounts` accepts `identifiers`;
  `POST`/`DELETE …/deposit-accounts/{did}/identifiers` add and remove them.
- `partyRefDTO` is `{participant, account, identifier?: {scheme, value}}` — the
  old free-form `iban` key is gone. `web/src/components/forms/party-ref-fields.tsx`
  already speaks the new shape.
- An SCT is refused unless both legs carry an IBAN, and initiation back-fills the
  stored address when the caller quotes none and the account has exactly one.
  A customer send form therefore does not have to quote an identifier — but the
  payee lookup still needs `/directory` to turn an IBAN into `(participant,
  account)`, because routing is by id.

## Things about this repo that will bite you if nobody tells you

- **A `[[wiki-link]]` to a key absent from `web/src/components/hint-content.ts`
  throws at runtime under `RootLayout` and takes _every_ route in the dev app
  down, while `next build` stays green.** `cd web && npm run test` catches it,
  in hint bodies *and* quiz explanations. Run it, and load a page.
- **`go test ./...` must stay green with no `DATABASE_URL` and against Postgres.**
  A `cbs-pg` container is usually running on
  `postgres://cbs:cbs@localhost:5432/cbs?sslmode=disable`; `make test-pg`'s
  `docker compose up` may hit a name conflict with it, so set
  `TEST_DATABASE_URL` directly instead. This sub-project is frontend-only, but
  the rule is absolute.
- **Domain facts are duplicated across four layers on purpose** — `README.md` is
  authoritative, then `hint-content.ts`, the quiz chapters, and the schema
  comments. Correct one, correct the others. See `CLAUDE.md`.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions,
  ≥8 distinct concept tags, no tag more than 3×, all three difficulty tiers.
- The web deps may need `cd web && npm install` in a fresh worktree.

## Two lessons from sub-project 5 worth carrying in

Both are recorded in the roadmap's log and both cost real rework:

1. **When the compiler finds an unanticipated consumer of a changed type, re-run
   the design's failure analysis against it — not just the schema.** Renaming
   `PartyRef.IBAN` turned out to touch mandates as well as payments. The compiler
   found the storage consequence; nobody re-asked what a *mutable address* does
   to a *mandate*, and the answer was that reissuing an identifier permanently
   killed every mandate on the account. It took the whole-branch review to catch.
2. **Prose that asserts what code does needs a test, not a careful reading.** This
   project has now shipped confidently-worded documentation that argued the
   opposite of the code four times.

## Suggested shape

The spec's own structure is close to a task breakdown: `lib/identity.ts` and the
route move first (with the `/participants/[...rest]` redirect), then the shell
split out of the 430-line `app-shell.tsx`, then the identity picker and lobby,
then the bank home, then the three customer screens. The riskiest mechanical part
is the route move — roughly 18 link sites — and the spec asks for a nav-integrity
test asserting every nav `href` resolves to a real route file, which is what
catches a dead link afterwards.

Ask me before starting if anything in the spec reads as wrong rather than merely
unexpected.
