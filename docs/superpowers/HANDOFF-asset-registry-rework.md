# Handoff: replace the asset registry table with a hardcoded asset list

Paste this whole file as the opening message of a fresh conversation.

---

You are picking up work in a Go core-banking system at `/Users/raphaelgruber/Git/cbs`,
on branch `docs/multi-asset-expansion`, currently at commit `bbd3bd5`. The working
tree is clean and every suite is green.

## What already happened

A completed 17-commit feature branch added a **multi-asset dimension** to a ledger
that was previously, implicitly, single-currency. It shipped in eight reviewed
tasks:

1. `ledger.Amount` became a defined `int64` rather than a type alias.
2. A **book-scoped asset registry** — `ledger.AssetDef{Code, Name, Scale, Class}`
   stored in an `assets` table, one row per book.
3. The asset threaded through every layer that creates an account: GL accounts,
   deposit accounts, and each participant's per-asset suspense / reserve /
   settlement account set.
4. **The double-entry invariant became per asset.** `validateBalance` sums debits
   and credits within each asset and returns `ErrUnbalancedAsset` wrapped together
   with `ErrUnbalancedTransaction` via multi-`%w`, so `errors.Is` matches at either
   level and pre-existing callers keep working.
5. Payment schemes declare their asset (SEPA CT and DD are EUR) and both legs of a
   payment are validated against it in `InitiatePaymentTx`.
6. HTTP: asset registry endpoints, `asset` on response DTOs, and `Book.GetAccounts`
   so rendering a listing costs one round trip rather than one per account.
7. The web app became scale-driven — it previously divided by 100 everywhere, which
   renders 1 BTC as `1,000,000.00`.
8. Documentation: README, `hint-content.ts`, quiz chapter 16, schema comments.

Everything above stays **except** the registry table. See below.

## Your job

Two independent pieces of work. Do them in one branch; they can be separate commits.

### Piece 1 — delete the asset registry table, hardcode the asset list

**Why.** The registry is writable at runtime, but nothing downstream can honour a
runtime-defined asset. A participant's suspense / reserve / settlement accounts are
provisioned only when it joins the network, from a **hardcoded EUR/USD switch**
(`Network.assetDef`, `payment/system.go:371`). So today:

```
POST /participants/alpha/assets  {"code":"BTC",...}   -> 201
POST /participants/alpha/deposit-accounts {"asset":"BTC"}  -> 201
POST /participants/alpha/deposits                     -> 404
```

The customer account is real and can never hold money, and the 404 reads as
"participant not found". The table promises a capability the rest of the system
cannot deliver.

**The decision (made by the repo owner):** an asset definition is a fact about the
world, not per-bank state — "BTC has 8 decimals" is true everywhere. The codebase
already treats the analogous case this way: `SchemeSEPACT` and `SchemeSEPADD` are Go
types with an `Asset()` method, registered in code, not rows in a table. Assets
become the same.

**What to delete:**

- the `assets` table
- `ledger.Tx`'s `PutAsset` / `GetAsset` / `ListAssets`, and their implementations in
  `store/mem` and `store/pg`
- the conformance cases in `store/storetest` that cover them
- `Book.CreateAsset` / `CreateAssetTx` / `GetAsset` / `ListAssets`, `ErrDuplicateAsset`,
  the `EventAssetCreated` audit event, and `payment`'s `ensureAsset`
- the registry HTTP endpoints (`api/handlers_asset.go`) and the web code that calls
  them

**What replaces it:** one package-level list of known assets in Go. `Network.assetDef`'s
switch stops shadowing a table and simply becomes *the* list. Decide where it belongs —
`ledger` is the natural home since `ledger.Book` must validate an account's asset
without knowing anything about payment networks.

**What stays, unchanged:**

- `Account.Asset` and `deposit.Account.Asset` — per-account data, still columns
- `participant_assets` — the per-asset plumbing accounts
- per-asset balancing, `ErrUnbalancedAsset` and its double wrap
- `MaxAssetScale = 9` and the reason for it: `Amount` is an `int64`, max ≈9.22e18,
  which is only 9.2 units at 18 decimal places
- scheme assets and the both-legs check in `InitiatePaymentTx`
- creating an account with an unregistered asset must still fail — it validates
  against the list instead of the table

**Migrations — explicit instruction from the repo owner, and it overrides the usual rule.**
Do **not** add a migration that drops the table. Nothing is deployed. Fold the
multi-asset schema into `0001_init.sql` and delete `0002`–`0006`. A fresh database
should get the right schema in one file with no historical cruft.

This contradicts `migrate.go`'s "a shipped migration is immutable" rule, but that
same doc comment sanctions it: *"this repository has no deployed databases: every
Postgres it meets is a throwaway container or a per-test schema, both of which
migrate from empty."* Keep the schema comments — per `CLAUDE.md` they are domain
content, not implementation notes. Two of them need rewriting rather than moving:
`0003`'s explanation of why the asset lives on `accounts` and not `entries` (still
true, keep it), and `0006`'s explanation of why there is no foreign key to `assets`
(now moot — the table is gone; delete it rather than leaving a comment about a table
that does not exist).

Current migration files:

```
0001_init.sql               375 lines   <- fold everything into this
0002_assets.sql              21         <- delete (the table itself)
0003_account_asset.sql       28         <- fold: accounts.asset column
0004_deposit_account_asset   16         <- fold: deposit_accounts.asset column
0005_participant_assets.sql  33         <- fold: participant_assets table
0006_asset_fk_rationale.sql  33         <- delete (comments about a gone table)
```

### Piece 2 — fix the outstanding review findings

These came from the final whole-branch review and are independent of Piece 1. The
full ledger with rationale is at
`.superpowers/sdd/2026-07-27-multi-asset-ledger/progress.md` — read it.

**CRITICAL — a teaching claim that four layers agree on and the code contradicts.**

`payment/errors.go:89`, `README.md:703-712`, `web/src/components/hint-content.ts:871-877`,
`web/src/lib/quiz/chapters/16-multi-asset-accounting.ts:202`, and a comment at
`payment/system_test.go:756` all assert that the ledger **structurally cannot** catch
a euro-to-bitcoin payment, illustrated with a creditor leg of `Debit Suspense BTC /
Credit Bob BTC`.

That posting is unreachable. `SettleCycleTx` resolves the creditor's suspense account
with `creditor.AccountsFor(asset)` where `asset := scheme.Asset()` — EUR
(`payment/system.go:757`). The real entries are a **EUR** suspense debit against a
**BTC** credit, so `validateBalance` returns `ErrUnbalancedAsset`. The ledger does
catch it.

The accurate version keeps the lesson and is better pedagogy: the ledger cannot catch
it **at initiation** — the debtor leg alone (`Debit Alice EUR / Credit Suspense EUR`)
is impeccable double-entry, and nothing in that posting contains the claim that a
second posting elsewhere is its other half. It surfaces only at settlement, as an
`ErrUnbalancedAsset` that takes down the **entire clearing cycle** for one bad payment.
`ErrAssetMismatch` is what turns a late, batch-wide, misattributed failure into an
immediate, correctly-attributed one.

Fix all five sites together — `payment/errors.go` is the source the others paraphrase —
and re-check that quiz question's distractors afterwards, because the restatement
changes which options are true. Note this is the *second* counter-example on this
branch that argued the opposite of what it claimed; the first was caught and fixed,
and this one was missed by the same pass. Verify the arithmetic and the reachability
of any example you write.

**IMPORTANT — N+1 round trips in two listing endpoints.** `mandateAsset`
(`api/handlers_payment.go:16-27`) does a `GetParticipant` **plus** a
`Deposit.GetAccount` — two `store.View`s, two full `BEGIN…COMMIT` on `store/pg` — and
`handleListMandates` calls it once per row (`:92`). `handleListSettlements` calls
`settlementAsset` (`:30-37`) once per row (`:267`), each a `GetCycle` plus a fresh
`ListSchemes()`. `GET /mandates` went from 1 round trip to 2N+1. This is the same
defect that was already found and fixed in `toTransactionDTO` via `Book.GetAccounts` —
apply the same batch-resolve pattern here.

**IMPORTANT — `CreateMandateTx` holds both accounts and doesn't check they agree**
(`payment/system.go:478-484`). `checkPartyTx` was changed to return the account
precisely so callers could read its asset; this caller discards both. A mandate with a
EUR debtor and a BTC creditor is creatable, and its `MaxAmount` renders in an asset the
creditor cannot receive. Not exploitable — `InitiatePaymentTx` makes such a mandate
unusable — but two lines fix it.

**IMPORTANT — the two balance endpoints return a bare integer with no asset**
(`api/handlers_ledger.go:193`, `api/dto_deposit.go:60-63`). Every other DTO on the
branch gained an `asset` field. These didn't, so a client needs a second request for
the account and a third for the scale before it can render a number. This is the root
cause of a web-side symptom: `accounts/[aid]/page.tsx:51` and
`deposit-accounts/[did]/page.tsx:305` render an indefinite skeleton, because the asset
arrives on a later request than the number it scales.

**IMPORTANT — `ErrParticipantAssetNotFound` maps to 404** (`api/errors.go:41`), so
three endpoints answer "not found" when the truth is "this bank holds no accounts in
that asset": `POST /cycles/{id}/settle`, `POST /participants/{pid}/deposits`, and
`GET /central-bank/reserves/{pid}`. 422 matches the sibling underfunded-member failure.
Piece 1 may make this unreachable — check, and if so delete rather than remap.

**IMPORTANT (web) — a value is silently reinterpreted at a new scale.**
`web/src/components/forms/open-deposit-account-form.tsx:31,40,93-95,106-112`: `asset`
is a free-text input and `overdraft` (integer minor units) is never reset when it
changes. Type `5.00` under EUR (→ `500`), switch the asset to a scale-8 code, and the
form renders `500` as `0.00000500` and POSTs `overdraftLimit: 500`. This is the exact
class of bug the feature exists to prevent, inside one form.
`post-transaction-form.tsx:105-111` handles the analogous case correctly by nulling
the amount; this form doesn't.

**IMPORTANT (web) — stale input text when a parent nulls the value at an unchanged
scale.** `web/src/components/money.tsx:100-106`: the resync effect is keyed on
`[asset.code, asset.scale]` only, so switching a transaction leg between two accounts
of the **same** asset nulls `leg.amount` but leaves the box showing the old text. The
user sees a filled amount, `ready` is false, and Post is disabled with no explanation.
The effect's own comment claims it covers this case; it does not.

**IMPORTANT (web) — the account picker never shows an asset.**
`web/src/components/pickers/gl-account-picker.tsx:21-27` renders name, id, type badge
and ledger keywords, but not the asset — not even as a search keyword. In a multi-asset
chart the user chooses between "Cash EUR" and "Cash BTC" blind. This is what makes a
mixed-asset transaction constructible in the UI.

**Also worth fixing while you're there** (all logged as deferred minors in
`progress.md`, none blocking):

- `AddParticipantTx` doesn't de-duplicate its `assets` slice: `["EUR","EUR"]` creates
  six accounts, keeps three, and orphans three in the chart of accounts. Piece 1 may
  delete this parameter entirely — check first.
- `ledger/asset_test.go:171` is a near-tautology: `if !strings.Contains(err, "EUR") &&
  !strings.Contains(err, "BTC")` passes whenever the error names *either* asset. The
  `order` slice in `validateBalance` exists to make the offending asset deterministic,
  so assert the specific one.
- `hint-content.ts:596-597` renders the balancing account as `Settlement Assets (EUR)`,
  but `payment/system.go:82` deliberately keeps `cbAssetsName` stable across assets.
  Either the docs or the naming should move; the code's stability argument only binds
  the one account the backfill created.
- `web/src/components/concept-links.ts:39-50` — `validateConceptContent()` scans only
  hint bodies, **not** quiz explanations, so a green `npm run test` does not prove a
  chapter's `[[wiki-links]]` resolve. `CLAUDE.md` implies it does. Widening this guard
  would have caught a real class of breakage.
- `post-transaction-form.tsx:73-79` — the `balanced` badge sums raw minor units across
  assets, so a mixed-asset transaction shows green "Balanced ✓". The server rejects it,
  so no bad data — but this app's own quiz chapter 16 teaches that exact bug as the
  reason per-asset balancing exists.
- three different shapes of "asset unresolved" in the web app: indefinite skeleton,
  `—`, and bare `null`. Pick one.

## Constraints you must hold to

From `CLAUDE.md` and `web/CLAUDE.md` — read both.

- **Postgres is optional and that is load-bearing.** `go test ./...` with no
  `DATABASE_URL` runs everything on `store/mem`; `make test-pg` runs the *same* suites
  against `store/pg`. Both must be green. `store/pg` must never accept or refuse a
  write differently from `store/mem` — `store/storetest` is the conformance suite that
  enforces it.
- **No default asset at any boundary.** A silent fallback to a base currency is the
  specific bug this whole feature exists to prevent.
- **Domain content is duplicated by design** across `README.md` (authoritative),
  `web/src/components/hint-content.ts`, the quiz chapters, and the SQL schema comments.
  Correcting a fact in one means correcting it in all. In this repo the documentation
  is the product — a wrong factual claim is a defect, not a nitpick.
- A `[[wiki-link]]` to a key absent from `hint-content.ts` **throws under `RootLayout`
  and takes down every route in the dev app**, while `next build` stays green. Load a
  page; don't trust the build.
- `web/src/lib/quiz/diversity.test.ts` holds each chapter to 18–22 questions, ≥8
  distinct `concept` tags, no tag more than 3×, and all three difficulty tiers.
- Don't weaken, skip or delete a test to make something pass.

## Verification

```bash
go build ./... && go vet ./... && go test ./...   # store/mem
make test-pg                                       # store/pg, same suites
cd web && npm run test && npm run typecheck && npm run lint
make dev                                           # then actually load a page
```

Chapter 16 and the README changes need a browser check, not just a green build.

## Reference material

- Plan: `docs/superpowers/plans/2026-07-27-multi-asset-ledger.md`
- Design spec with the reasoning behind each decision:
  `docs/superpowers/specs/2026-07-27-multi-asset-ledger-design.md`
- Roadmap for the wider expansion (lending, crypto, FX):
  `docs/expansion-roadmap.md`
- **Full review ledger — every finding, deferred item and decision, with rationale:**
  `.superpowers/sdd/2026-07-27-multi-asset-ledger/progress.md`
- Per-task implementer reports and review diffs are alongside it in that directory.

Two notes from the roadmap worth carrying into any future FX work, since Piece 1
touches the same area: a position account **cannot** be an `Asset` account, because
`checkSufficientBalance` forbids Asset and Expense balances going negative
(`ledger/types.go:62`) and a position account must be able to; and `validateBalance`
accumulates into an `Amount`, so a transaction mixing a scale-9 asset near the int64
ceiling with anything else can overflow the accumulator before the zero check runs.
