# Handoff: the loose ends left on the multi-asset branch

Paste this whole file as the opening message of a fresh conversation.

---

You are picking up work in a Go core-banking system at `/Users/raphaelgruber/Git/cbs`,
on branch `docs/multi-asset-expansion`, currently at commit `a6411d7`. The working
tree is clean and every suite is green on both stores.

## What already happened

A 17-commit branch added a **multi-asset dimension** to a ledger that was
previously, implicitly, single-currency. A follow-up (`a6411d7`) then did two
things:

1. **Deleted the book-scoped asset registry table.** Asset definitions are now a
   package-level list in Go — `ledger.LookupAsset` / `ledger.Assets`, holding EUR
   (scale 2, Fiat), USD (2, Fiat) and BTC (8, Crypto) — the same way payment
   schemes are Go types rather than rows. The table was writable at runtime but
   nothing downstream could honour a runtime-defined asset, so it promised a
   capability the system could not deliver. `GET /assets` replaces the
   per-participant registry endpoints. Migrations `0002`–`0006` were folded into
   `0001_init.sql` and deleted.
2. **Fixed every finding from the whole-branch review**, including a critical one:
   four documentation layers and a code comment claimed the ledger *structurally
   cannot* catch a euro-to-bitcoin payment. It can — at settlement, where it fails
   the entire clearing cycle. What it cannot catch is the payment at *initiation*.
   That correction is now pinned by
   `TestCrossAssetPaymentSurvivesInitiationAndFailsTheWholeCycle`.

**Read `a6411d7`'s commit message first.** It is long on purpose and explains every
decision, including three judgment calls (BTC in the list, adding `GET /assets`,
`ErrAssetNotFound` moving 404 → 400) that you should not re-open without reason.

The full review ledger, with every finding and decision, is at
`.superpowers/sdd/2026-07-27-multi-asset-ledger/progress.md` — read it. Note that
directory is gitignored, so it is on disk but not in the history.

## Your job

**None of what follows is blocking.** The branch is mergeable as it stands. These
are the items that were logged as deferred minors and never picked up, plus three
defects found while verifying `a6411d7` in a browser. They are listed roughly in
descending order of "a user would notice this".

### 1. The quiz shuffle seed is `Date.now()`, so every chapter hydrates wrong

`web/src/components/quiz/quiz-runner.tsx:31`:

```ts
const [seed, setSeed] = useState(() => Date.now());
```

The server renders one shuffle, the client another, so React logs a **recoverable
hydration error** on every `/learn/<chapter>` page and regenerates the tree. It
surfaces as a `TypeBadge` mismatch (`web/src/components/quiz/type-badge.tsx:20`) —
the server's first question was a different `kind` than the client's — but the
badge is a symptom, not the cause. This is verbatim one of the cases Next.js's own
error text names: *"Variable input such as `Date.now()` or `Math.random()` which
changes each time it's called."*

The fix is to not shuffle during SSR: seed on mount in an effect, or render the
unshuffled order on the server and shuffle after hydration, or make the page
client-only. Pick whichever fits this app's conventions; **read
`web/node_modules/next/dist/docs/` before deciding** — per `web/AGENTS.md` this is
not the Next.js you know.

This was pre-existing before `a6411d7`; it is listed first because it fires on
every chapter load, which is the app's main teaching surface.

### 2. A wiki-link inside a fenced code block renders as raw markdown

`web/src/components/hint-content.ts:529` — inside the `clearing-vs-settlement`
hint's code block:

```
  Banks clear their [[clearing-suspense]] accounts
```

`preprocessConceptMarkdown` (`web/src/components/concept-links.ts:20`) rewrites
every `[[…]]` in a body with a single regex, including ones inside fenced blocks,
so the hint panel shows the literal string
`[Clearing suspense](concept:clearing-suspense)` in monospace. Visible in the
concept panel on the dashboard.

Exactly one occurrence today, so there are two honest fixes: teach
`preprocessConceptMarkdown` to skip fenced regions (and add a test), or delete the
link from that one code block. The first is worth more — a code block is where an
author is *most* likely to write something that looks like a wiki-link — but say
which you chose and why.

### 3. "Encountered a script tag while rendering React component"

Next.js dev overlay reports this at `web/src/components/providers.tsx:31`
(`<ThemeProvider>`), with a call stack of `script` → `<anonymous>` → `Providers` →
`RootLayout`.

**Verify before fixing.** The shape of that stack — an anonymous script directly
under the provider tree — strongly suggests a browser extension injecting a
`<script>` into the DOM before React hydrates, not application code. Reproduce it
in a clean Chrome profile with all extensions disabled first. If it does not
reproduce, close it as an environment artifact and note that in the ledger; do not
change `providers.tsx` to work around someone's extension.

### 4. A numeric quiz question can only be labelled in cents or dollars

`web/src/lib/quiz/types.ts:27` — `unit?: "cents" | "dollars"`, consumed at
`web/src/components/quiz/question-card.tsx:251` and
`web/src/components/quiz/quiz-result.tsx:18`, the latter formatting with a hardcoded
`$`. A numeric question whose answer is in satoshi has no correct label, and the
type makes writing one impossible.

The rest of the app resolved this by carrying an asset code and looking its scale
up (`useAssetLookup`, `src/lib/money.ts`). The natural shape here is an optional
asset code on the question, falling back to today's behaviour. Check
`web/src/lib/quiz/diversity.test.ts` still passes: it holds each chapter to 18–22
questions, ≥8 distinct `concept` tags, no tag more than 3×, and all three
difficulty tiers.

### 5. Two stale comments that now say the opposite of the code

- `web/src/app/payments/page.tsx:16-20` — "the scheme itself carries no scale on
  the wire (schemeDTO has no asset field)". It has had one since task 7
  (`api/dto_payment.go`'s `toSchemeDTO`), and the surrounding paragraph then
  explains that the scale therefore has to come from the debtor's participant —
  which stopped being true in `a6411d7`, where `useAssetLookup()` is network-wide
  and takes no participant at all. The whole comment is now wrong twice over.
- `payment/system_test.go:778` — "the creditor's account **lives in** a different
  participant's book". Nothing forbids `Debtor.Participant == Creditor.Participant`;
  "may live" is the accurate phrasing. (This one was logged as a deferred minor
  back in task 5 and is genuinely trivial.)

### 6. Test-quality items logged and never done

- `payment/system_test.go:752` — `TestPaymentRejectsAccountNotInSchemeAsset` is the
  *creditor* case but reads as the generic one, sitting directly above
  `TestPaymentRejectsDebtorAccountNotInSchemeAsset`. Renaming it
  `…CreditorAccount…` makes the pair self-documenting.
- `ledger/asset_test.go` — `newAccountIn` calls `newSubledger` per invocation, so
  the FX test spreads four accounts across four subledgers. Harmless, but a
  single-subledger trade would model an actual FX book faithfully.
- `api/server_test.go` — nothing distinguishes `{"assets":[]}` (explicit empty
  array) from an absent `assets` field on `POST /participants`. Both are meant to
  mean `["EUR"]`, and it is correct by inspection (`len == 0` catches both), but
  that is the kind of "correct by inspection" that stops being true silently.

### 7. Two efficiency nits in the payment layer

Both are correctness-neutral; fix them only if you are in the file anyway.

- `payment/system.go:346` — `AddParticipantTx`'s per-asset loop calls
  `centralBankAssetsAccountTx`, which calls `centralBankChartTx` (line 241) on
  every iteration. Idempotent, just repeated work, and it also re-lists the whole
  central-bank chart of accounts each time.
- `store/pg/tx_payment.go:63` — `participant_assets` child rows are inserted by
  iterating a Go **map**, so `BIGSERIAL seq` is assigned in random order. Harmless
  today because the result folds back into a map, but `seq` means real ordering
  everywhere else in this schema, and `store/mem` has no equivalent randomness.
  Sorting the codes before inserting costs one line.

### 8. Documentation consistency

- `README.md:131,136` — the double-entry introduction still uses `$100` and `$50`,
  immediately above material that is otherwise euro throughout. Logged in task 8
  and never done.
- `web/src/lib/quiz/chapters/16-multi-asset-accounting.ts:252` — question `ch16-q17`
  discusses FX position accounts with no "not implemented" caveat. `ch16-q10`
  carries one, and every other FX mention on the branch is framed as intent. Per
  `docs/expansion-roadmap.md` FX is sub-project 4 and unstarted.

## Notes, not bugs

Do not "fix" these; they are recorded so you do not rediscover them as defects.

- `ledger.Book.GetAccounts` aborts an entire listing on one bad account reference
  rather than returning a partial map. Deliberate — a caller-visible error should
  not depend on where in the batch the bad ID fell.
- `schemeAsset` (`api/dto_payment.go:213`) returns `""` for an unregistered scheme
  ID. Unreachable today (a cycle can only open against a known scheme) and it fails
  soft into the unresolved-asset UI branch, never a formatted number.
- `ErrParticipantAssetNotFound` is **reachable** and maps to 422 on purpose:
  `Book.CreateAccount` validates an asset against the global list, not against what
  its bank clears in, so a EUR-only bank can open a BTC customer account and then
  fail to fund it. That is the intended shape — the account exists, the asset
  exists, this bank simply holds no plumbing for it.

## Constraints you must hold to

From `CLAUDE.md` and `web/CLAUDE.md` — read both.

- **Postgres is optional and that is load-bearing.** `go test ./...` with no
  `DATABASE_URL` runs everything on `store/mem`; `make test-pg` runs the *same*
  suites against `store/pg`. Both must be green. `store/pg` must never accept or
  refuse a write differently from `store/mem` — `store/storetest` enforces it.
- **There is one migration.** `0001_init.sql` is the whole schema. `migrate.go`'s
  immutability rule is suspended for this repository only because no database is
  deployed; if that ever changes, the rule comes back and the next schema change is
  `0002`.
- **No default asset at any boundary.** A silent fallback to a base currency is the
  specific bug this whole feature exists to prevent.
- **Domain content is duplicated by design** across `README.md` (authoritative),
  `web/src/components/hint-content.ts`, the quiz chapters, and the SQL schema
  comments. Correcting a fact in one means correcting it in all. In this repo the
  documentation is the product — a wrong factual claim is a defect, not a nitpick.
- A `[[wiki-link]]` to a key absent from `hint-content.ts` **throws under
  `RootLayout` and takes down every route in the dev app**, while `next build` stays
  green. `npm run test` now checks hint bodies *and* quiz explanations
  (`concept-links.test.ts`), but still load a page.
- Don't weaken, skip or delete a test to make something pass.
- **Verify prose against code, not against reasoning.** Two counter-examples on this
  branch argued the opposite of what they claimed, and both survived a careful read.
  If you write a claim about what the code does, pin it with a test.

## Verification

```bash
gofmt -l . && go build ./... && go vet ./... && go test ./...   # store/mem
docker compose down -v && make test-pg                          # store/pg, from empty
cd web && npm run test && npm run typecheck && npm run lint
make dev                                                        # then actually load a page
```

Items 1–4 need a browser check, not just a green build. For item 1, load
`/learn/16-multi-asset-accounting` and confirm the Next.js dev overlay reports zero
issues; for item 2, open any concept panel that shows the clearing-vs-settlement
hint and read the code block.

## Reference material

- **Commit `a6411d7`** — the rework, with the reasoning for every decision.
- **Full review ledger:** `.superpowers/sdd/2026-07-27-multi-asset-ledger/progress.md`
  (gitignored; on disk only)
- Plan: `docs/superpowers/plans/2026-07-27-multi-asset-ledger.md`
- Design spec: `docs/superpowers/specs/2026-07-27-multi-asset-ledger-design.md`
- Roadmap for the wider expansion (lending, crypto, FX): `docs/expansion-roadmap.md`
- `docs/superpowers/HANDOFF-asset-registry-rework.md` — **superseded by this file.**
  Every item in it is done. Delete it once you have read enough of it to be sure.

Two notes from the roadmap worth carrying into any future FX work: a position
account **cannot** be an `Asset` account, because `checkSufficientBalance` forbids
Asset and Expense balances going negative (`ledger/types.go:62`) and a position
account must be able to; and `validateBalance` accumulates into an `Amount`, so a
transaction mixing a scale-9 asset near the int64 ceiling with anything else can
overflow the accumulator before the zero check runs.
