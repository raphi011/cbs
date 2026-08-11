# Spec — GL-account detail view + descriptive account names

**Date:** 2026-06-24
**Status:** Approved design, ready for implementation plan
**Area:** `web/` frontend only — no backend change

## Goal & teaching point

Make every GL account (`acct_*`) a **navigable, named thing**. Today an account id
like `acct_11` appears only as inert monospace text (`IdText`) in the customer
statement and transactions — you cannot click it, and it shows an opaque id rather
than the account's real name. This spec adds:

1. A generic **GL-account detail route** `…/accounts/[aid]` — name · type · context ·
   balance · an **account ledger** (statement-style, signed by the account's normal
   balance, reconciling to its book balance), with per-row expand to the raw
   double-entry. Works for **any** account type, not just liabilities.
2. **Descriptive names** everywhere an account is referenced — resolve `acct_*` → its
   chart-of-accounts `name`, rendered as `id · name`, with the id kept as the
   clickable anchor into the new route.

**Teaching point.** In this ledger-first model the GL account *is* the account; a
`DepositAccount` is a thin product wrapper pointing at one (`DepositAccount.glAccount`).
The new page makes that concrete and, crucially, makes **normal balance** visible:
open a liability (a customer deposit) and an asset (the central-bank reserve) side by
side and watch the sign convention flip — the same projection, signed by the account's
type, with the running total reconciling to the account's real book balance.

## Scope

**In:**
- Generalize `projectStatement(...)` to be **normal-balance-aware** (sign by the
  account's `AccountType`, not a hard-coded liability assumption). The existing
  customer statement becomes the `Liability` special case — behavior unchanged.
- New route `…/participants/[pid]/accounts/[aid]/page.tsx`: header (name · type badge ·
  id · ledger/subledger context · role note for well-known accounts · "backing account
  for deposit X" backlink when applicable), a **balance card** (`getBookBalance`), and
  the **account-ledger** view (reusing the statement table + per-row double-entry
  expand).
- **Name resolution + `AccountRef`** — a small shared component rendering `IdText` +
  ` · {name}`, where the id is a link to the account route. Curated role-words
  (suspense/reserve/settlement) retained for the participant's 3 well-known accounts;
  real chart-of-accounts names for everything else.
- **Link wiring (this pass):** statement contra + expanded legs, the new page's own
  contra/legs, and the **Ledger page** `AccountRow` (the natural browse → drill path).

**Out (YAGNI — future specs, builds nothing now):**
- Wiring `AccountRef` into the **transactions page** legs (easy follow-up).
- Editing/renaming accounts from the new page.
- Interest accrual, fees, statement periods, value-dating, multi-currency / IBAN.

## Decisions (resolved during brainstorming)

| # | Decision |
|---|---|
| Account ledger view | **Option A — normal-balance-aware running balance** (signed by `AccountType`), reconciling to the account's `getBookBalance`. Per-row expand keeps the raw double-entry ("honest GL") view on demand, so A ⊇ raw-legs. |
| Name display | **`id · name`** everywhere; the **id stays visible as the clickable anchor** (matches today's `acct_7 · suspense`). |
| Well-known accounts | **Keep curated role-words** (suspense/reserve/settlement) for the participant's 3 special accounts; resolve real names for all others. The *role* teaches more than the literal name in a statement context. |
| Link reach | **Statement (contra + legs) + new page + Ledger page rows** this pass; transactions page is a follow-up. |
| Backend | **No change.** All data already available. |

## Architecture & data flow

**No backend change.** Everything is a client-side composition of existing endpoints:

- `getBookBalance(pid, aid)` → the account's book balance (already exists).
- `listTransactions(pid, aid)` → GL transactions touching the account; already filters
  by account (same dependency the customer statement uses, just with `aid` instead of
  `account.glAccount`).
- `getAllAccounts(pid)` / `useAllAccounts` → flat chart of accounts with
  ledger/subledger context; already built and cached for the pickers. Source of the
  account's `name`, `type`, and context, and of the id → name lookup map.
- `listDepositAccounts(pid)` / `useDepositAccounts` → to find whether some
  `DepositAccount.glAccount === aid` (the "backing account for deposit X" backlink).

### The core change — normal-balance-aware projection

`web/src/lib/statement.ts` currently hard-codes the liability sign
(`statement.ts:60-63`): `delta = Credit ? +amount : −amount`. Generalize it:

- Thread the account's `type: AccountType` into `projectStatement`.
- Sign by the existing **`NORMAL_BALANCE`** table already in
  `web/src/lib/enums.ts:64` (`Asset/Expense → Debit`, `Liability/Equity/Revenue →
  Credit`): `delta = (e.direction === NORMAL_BALANCE[type]) ? +amount : −amount`.
- The customer statement caller passes `type: 'Liability'` → identical behavior to
  today (Credit = +). This removes a latent bug (the projection silently mis-signs any
  non-liability account) rather than working around it.
- `runningBalance` now reconciles to **any** account's `getBookBalance`, not just a
  deposit's `book`.

Signature sketch (final shape decided in the plan):
`projectStatement(txs, accountId, { type, knownAccounts })`.

### New hooks (thin)

- `useGLAccount(pid, aid)` — derive the `AccountWithContext` for `aid` from
  `useAllAccounts` (no new endpoint).
- `useAccountStatement(pid, aid, type)` — `useTransactions(pid, aid)` + the generalized
  `projectStatement`, mirroring the existing `useStatement`.

### Components

- **`AccountRef`** (new, shared) — renders `IdText(id)` + ` · {label}`, the id linking
  to `/participants/{pid}/accounts/{aid}`. `label` = curated word for a well-known
  account, else resolved chart-of-accounts name, else just the id if unresolved.
  Replaces the ad-hoc `IdText` + label rendering in the statement table's `ContraCell`
  and leg list, and is reused on the new page and the Ledger page rows.
- **Account detail page** — composes header + balance card + account-ledger. Reuses
  `StatementTable` (already supports the contra cell + per-row double-entry expand),
  `AccountTypeBadge`, `Money`, `IdText`, `Card`, `ErrorState`, `Skeleton`.

## Error / edge handling

- **Unknown / not-found `aid`** — `getAllAccounts` won't contain it → render
  `ErrorState` ("account not found") rather than a half-populated page.
- **Account with no transactions** — empty-but-valid ledger (the projection already
  handles `[]`); balance card still shows book (0).
- **Asset vs liability sign** — covered by the `NORMAL_BALANCE` table; the running
  balance must tie out to `getBookBalance` for each type (asserted in tests).
- **Unresolved name** (account list still loading / id absent) — `AccountRef` falls
  back to the bare id, never blocks rendering.
- **Well-known account as the page subject** — header shows its curated role
  ("participant's suspense account") in addition to name + type.
- **Loading** — `Skeleton` per section (matches the statement sub-route pattern).

## Testing

No FE test runner; logic lives in pure functions + manual browser verification.

- **Extend `web/src/lib/statement.test.ts`:**
  - Asset account (normal balance = Debit): a Debit **increases** the running balance;
    final running balance reconciles to the (separately computed) book.
  - Liability account: unchanged from today (regression guard).
  - Spot-check sign via `NORMAL_BALANCE` for each `AccountType`.
- **Manual (per `web/CLAUDE.md`):** seed participant → deposit → fund + a couple
  transactions. Verify: `acct_11` (liability) statement unchanged and reconciles; open
  the **reserve** (asset) via a leg link and confirm its running balance reconciles
  with the *opposite* sign convention; names resolve as `id · name`; Ledger rows and
  statement legs link through; unknown id → clean error.
- **Gates:** `npm run typecheck && npm run lint && npm run build` all clean.

## Files touched (anticipated)

- `web/src/lib/statement.ts` — normal-balance-aware projection (core).
- `web/src/lib/statement.test.ts` — asset/liability cases.
- `web/src/lib/api/hooks.ts` (+ `query-keys.ts` if a new key is warranted) —
  `useGLAccount`, `useAccountStatement`.
- `web/src/components/account-ref.tsx` — new shared `AccountRef`.
- `web/src/components/statement/statement-table.tsx` — use `AccountRef` in `ContraCell`
  + leg list.
- `web/src/app/participants/[pid]/accounts/[aid]/page.tsx` — new route.
- `web/src/app/participants/[pid]/ledger/page.tsx` — link `AccountRow` via `AccountRef`.
- `web/src/components/hint-content.ts` — `?` copy for the normal-balance / running-
  balance teaching note, if added.

## Repo state

- Branched off `main` as `gl-account-view`. (`web/HANDOFF-customer-accounts.md` is the
  prior feature's handoff, still untracked — leave it.)
- `NORMAL_BALANCE` (enums.ts:64), `getBookBalance`, `listTransactions(pid, aid)`,
  `getAllAccounts`/`useAllAccounts`, and the `StatementTable` expand already exist — the
  build is mostly composition, with the projection generalization as the one real
  logic change.
