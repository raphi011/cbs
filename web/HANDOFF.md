# Handoff — Educational Next.js Ledger Frontend

Continuation notes for building the rest of `PLAN.md`. Read this + `PLAN.md`
together. Last commit before handoff: **M0–M3 complete** (`web/` app).

## Status

| Milestone | State |
|---|---|
| M0 Scaffold (proxy, types, client, shell) | ✅ done, verified |
| M1 Educational primitives (Hint, money, table, badges) | ✅ done, verified |
| M2 Participants + Central Bank + Schemes | ✅ done, verified (desktop + mobile screenshots) |
| M3 General ledger (accounts tree, transactions, reversal, audit) | ✅ done, verified e2e |
| M4 Deposit layer (accounts, holds, snapshots, funding) | ⬜ next |
| M5 Payments / Clearing / Settlement | ⬜ |
| M6 Dashboard polish + dark mode | ⬜ |

## Run & verify

```bash
# backend (repo root) — listens on :8080, in-memory, resets on restart
go run ./cmd/server
# frontend
cd web && npm run dev          # http://localhost:3000
npm run typecheck              # tsc --noEmit  (must be clean)
npm run lint                   # eslint        (must be clean)
```

Stack: **Next.js 16.2 (App Router) · React 19 · Tailwind v4 · shadcn/ui (Radix) ·
TanStack Query · sonner**. Tooling chosen by `create-next-app`/`shadcn` — newer
than `PLAN.md` assumed.

## Architecture (already built)

- **Proxy** `src/app/api/[...path]/route.ts` forwards every request to the Go
  backend (`BACKEND_URL` env, default `http://localhost:8080`). Browser only
  hits same-origin `/api/...` → **CORS impossible**. Backend down → clean 502.
- **Data layer** grows in three files, one section per backend area:
  `src/lib/api/endpoints.ts` (one typed fn per route) →
  `src/lib/api/query-keys.ts` (key factory, ledger keys nested under
  `["participants", pid, …]` so one invalidate clears a subtree) →
  `src/lib/api/hooks.ts` (query/mutation hooks; mutations invalidate keys).
- **Types** `src/lib/types.ts` mirror `api/dto.go` verbatim; enums in
  `src/lib/enums.ts`. Amounts are integer **cents** everywhere.
- **Participant-scoped pages** live under `src/app/participants/[pid]/`. To add a
  section, append to the `tabs` array in `[pid]/layout.tsx` and add the page.

## Reusable primitives (don't rebuild these)

- `<Hint id="…">` — click/tap `?` popover. **Registry already contains M4/M5
  copy** (`holds`, `hold-capture`, `hold-release`, `account-status`,
  `balance-book/holds/available`, `overdraft`, `snapshot`, `payment-lifecycle`,
  `debtor-leg`, `creditor-leg`, `netting`, `net-positions`, `mandate`,
  `requires-mandate`, `allows-return`, `settlement-*`, `clearing-suspense`, …).
  See `src/components/hint-content.ts`.
- `<MoneyInput valueCents onChangeCents>` / `<Money cents>` / `<AmountCell>` —
  edit major units, emit cents. `src/lib/money.ts`.
- `<DataTable columns rows rowKey>` — loading/empty states + per-column hints.
- `<EnumBadge value>` / `<AccountTypeBadge>` / `<DirectionBadge>`.
- `<ConfirmAction>` — generic confirm dialog with optional text field. **Use for
  M5 reject/return/revoke/close/settle and M4 status/close.**
- `<CopyId id>`, `<PageHeader>`, `<FieldLabel hint=…>`, `<ErrorState>`.
- Mutations: `toast.success/error(describeError(err))`; `describeError` maps
  400/404/409/422/502 to friendly text (see `src/lib/api/errors.ts`).

## Gotchas (learned the hard way)

1. **Next 16 async params.** Route handlers & dynamic pages: `params` is a
   `Promise`. Proxy awaits `ctx.params`. Pages use client components +
   `useParams()`. Don't read `params.pid` synchronously in a server component.
2. **`DisallowUnknownFields()`** on the backend — send *only* the exact keys a
   request DTO defines. Sending a stray key → 400.
3. **`*time.Time` wants RFC3339.** `<input type="date">` gives `YYYY-MM-DD`;
   convert to `${date}T00:00:00.000Z` or send `null`. **Exception:** snapshot
   `date` is a plain `"YYYY-MM-DD"` string (not RFC3339).
4. **Funding needs an existing deposit account.** `POST
   /participants/{pid}/deposits` `{account=<did>, amount, description}` credits a
   deposit account *and* raises the bank's central-bank reserve in step.
   Reserves start at 0 and are seeded this way. So the real intro loop is
   **create participant → open deposit account (M4) → fund**.
5. **Account IDs are copy-paste** in the transaction form (free-text leg inputs).
   A select would need to walk ledgers→subledgers→accounts (N+1). Grab IDs via
   `CopyId` on the General ledger tab. Same applies to M4/M5 forms that take
   account IDs — consider the same copy-paste approach or build a `useAllAccounts`
   tree-walker if you want dropdowns.
6. **In-memory backend** — all state resets on restart. The UI says so.
7. **shadcn is a new major.** init: `npx shadcn@latest init -b radix -p nova -y`;
   add: `npx shadcn@latest add <comp> -y`. Components import from the unified
   `radix-ui` package. Tailwind v4 → no config file, tokens in `globals.css`.

## Remaining work

### M4 — Deposit layer (next)
Endpoints (types already in `types.ts`, hints already written):
- `POST/GET /participants/{pid}/deposit-accounts`, `GET .../{did}`,
  `GET .../{did}/balance` (→ `Balance {book,holds,available}`),
  `POST .../{did}/status` (`{action: freeze|unfreeze|markDormant|reactivate}`),
  `DELETE .../{did}` (close; needs zero balance).
- Holds: `POST/GET .../{did}/holds` (create `{amount, expiresAt?, description?}`),
  `GET /participants/{pid}/holds/{hid}`, `POST .../{hid}/release`,
  `POST .../{hid}/capture` (`{counterparty, amount, description}`).
- Snapshots: `POST/GET .../{did}/snapshots` (POST `{date:"YYYY-MM-DD"}`).
- `GET /participants/{pid}/deposit-audit`.
- **Funding**: `fundDeposit` endpoint already exists in `endpoints.ts`; add a
  `useFundDeposit` hook invalidating the deposit balance + `reserves()` +
  `centralBankAudit()`.

Add a **"Deposit accounts"** tab (and optionally **"Funding"**) to
`[pid]/layout.tsx`. Pages: deposit-account list + open form; detail showing the
three-part balance (book/holds/available + overdraft hints), status actions
(`ConfirmAction`), close (DELETE), holds list with create/release/capture,
snapshots list + take-snapshot, deposit audit. Forms:
`open-deposit-account-form`, `create-hold-form`, `capture-hold-form`,
`fund-participant-form`. **This completes the create→fund→hold→capture loop.**

### M5 — Payments / Clearing / Settlement (network-level pages: `/payments`,
`/mandates`, `/cycles`, `/settlements` — nav links already exist)
Endpoints: mandates `POST/GET /mandates`, `GET /{mid}`, `POST /{mid}/revoke`;
payments `POST/GET /payments`, `GET /{payid}`, `POST .../reject`, `.../return`;
cycles `POST/GET /cycles` (open `{scheme}`), `GET /{cid}`, `POST .../close`,
`.../settle`; settlements `GET /settlements`, `GET /{sid}`.
**Check `api/handlers_payment.go` for the reject/return/revoke request bodies**
(reason vs empty) before wiring — not yet confirmed. Key form:
`initiate-payment-form` is scheme-aware (mandate field required iff
`scheme.requiresMandate`; debtor/creditor are `PartyRef {participant, account,
iban?}`). Cycle detail shows the net-positions table (`netting`/`net-positions`
hints). `settle` invalidates the cycle, settlements, payments, and
`reserves()`/`centralBankAudit()`.

### M6 — Polish
Dashboard aggregations + "how money moves" explainer; **dark mode** (`next-themes`
is already installed — add a `ThemeProvider` in `providers.tsx` + a toggle in the
topbar; `globals.css` already has `.dark` tokens); a11y pass; final
`typecheck && lint && build`; run the full teaching loop end-to-end.

## Verification done so far
M3 e2e (via proxy): created Asset+Liability accounts → posted balanced
Debit/Credit → `201 Posted` → book balance correct → reversal `201` with
`reversalOf` → unbalanced post → `400`. typecheck + lint clean at every milestone.
