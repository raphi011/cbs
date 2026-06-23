# Handoff — Educational Next.js Ledger Frontend

Continuation notes for building the rest of `PLAN.md`. Read this + `PLAN.md`
together. Current state: **M0–M5 + searchable pickers complete and committed**.
**Next: M6 (dashboard polish + dark mode)** — see the bottom of this doc.

## Status

| Milestone | State |
|---|---|
| M0 Scaffold (proxy, types, client, shell) | ✅ done, verified |
| M1 Educational primitives (Hint, money, table, badges) | ✅ done, verified |
| M2 Participants + Central Bank + Schemes | ✅ done, verified (desktop + mobile screenshots) |
| M3 General ledger (accounts tree, transactions, reversal, audit) | ✅ done, verified e2e |
| M4 Deposit layer (accounts, holds, snapshots, funding) | ✅ done, verified e2e |
| M5 Payments / Clearing / Settlement | ✅ done, verified e2e |
| M6 Dashboard polish + dark mode | ⬜ next |

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
- `<ConfirmAction>` — generic confirm dialog with optional text field (used for
  reject/return/revoke/close/settle and deposit status/close).
- `<Combobox>` (`src/components/ui/combobox.tsx`) + domain pickers
  (`src/components/pickers/`): `ParticipantPicker`, `DepositAccountPicker(pid)`,
  `GLAccountPicker(pid)`. Searchable selects — use these, never free-text IDs.
- `<NetPositionsTable positions>` — signed per-participant nets (cycle/settlement).
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
5. ~~Account IDs are copy-paste.~~ **Resolved:** all ID-entry fields now use
   searchable dropdowns (`src/components/ui/combobox.tsx`, built on shadcn
   `command`/cmdk). Domain pickers in `src/components/pickers/`:
   `ParticipantPicker`, `DepositAccountPicker(pid)`, `GLAccountPicker(pid)`. The
   GL picker walks ledgers→subledgers→accounts via `useAllAccounts(pid)`
   (`getAllAccounts` in `endpoints.ts`, cached, refetch-on-mount). `PartyRef`
   fields cascade: choosing a participant scopes its account picker and clears a
   stale account. Used by post-transaction legs, capture-hold counterparty, and
   payment/mandate debtor/creditor.
6. **In-memory backend** — all state resets on restart. The UI says so.
7. **shadcn is a new major.** init: `npx shadcn@latest init -b radix -p nova -y`;
   add: `npx shadcn@latest add <comp> -y`. Components import from the unified
   `radix-ui` package. Tailwind v4 → no config file, tokens in `globals.css`.

## What's already built (M4 / M5 / pickers) — key decisions

**M4 — Deposit layer.** `deposit-accounts` list (per-row available balance) +
`deposit-accounts/[did]` detail (book/holds/available card, legal-only status
transitions + close via `ConfirmAction`, holds with create/release/capture,
end-of-day snapshots) + a `deposit-audit` tab. Funding is a **"Fund" button on
the detail page** (it targets a specific account), not a separate tab. Deposit
keys nest under `["participants", pid, "deposit-accounts"]` (one invalidate
clears the subtree); `useFundDeposit` also invalidates `reserves`/
`centralBankAudit`; `useCaptureHold` also invalidates the ledger (it posts a GL
tx). Only legal status transitions are shown per status (`STATUS_TRANSITIONS`).

**M5 — Payment network.** `/mandates` (list + create + revoke), `/payments`
(+`[payid]` detail: parties, legs, cycle link, scheme-aware reject/return),
`/cycles` (+`[cid]` detail: timeline, net-positions, payments, close & settle),
`/settlements` (+`[sid]`). Shared `net-positions-table`. Forms:
`party-ref-fields`, `create-mandate-form`, `initiate-payment-form` (scheme-aware:
mandate field shown+required only when `scheme.requiresMandate`), `open-cycle-form`.
Backend contracts: reject/return take `{reason}`; revoke/close/settle take **no
body**. Reject shown for `Initiated|Accepted|Cleared`; return only when
`Settled && scheme.allowsReturn`. Money-moving mutations use a broad
`invalidateNetwork` (payments/cycles/settlements/reserves/centralBankAudit + all
`["participants"]`) — simplest correct choice for the tiny in-memory dataset.

**Searchable pickers** (see gotcha #5) replace every free-text ID input.

## Next: M6 — Dashboard polish + dark mode

1. **Dark mode.** `next-themes` is installed and `globals.css` already has
   `.dark` tokens. Add a `ThemeProvider` (`attribute="class"`,
   `defaultTheme="system"`) in `src/components/providers.tsx`, then a toggle in
   the topbar (`app-shell.tsx` header, beside `ParticipantSwitcher`) — a shadcn
   `DropdownMenu` (light/dark/system) is the usual pattern (`dropdown-menu` is
   already added). Avoid the hydration flash (mounted-check or
   `suppressHydrationWarning` on `<html>`).
2. **Dashboard (`src/app/page.tsx`, currently a placeholder).** Aggregate from
   existing hooks: participant count, total reserves (`useReserves`), open
   cycles / in-flight payments (`useCycles`/`usePayments`), recent settlements.
   Add a **"How money moves"** explainer (create → fund → pay → clear → settle)
   wired with `<Hint>`s — the teaching centrepiece.
3. **a11y pass.** Dialog focus traps, color contrast in both themes, icon-button
   `aria-label`s. (Combobox keyboard nav already works via cmdk.)
4. **Final gate.** `npm run typecheck && npm run lint && npm run build` clean;
   re-run the full teaching loop end-to-end in the browser (create participant →
   open deposit account → fund → hold → capture → mandate → payment → cycle →
   settle).

## Verification done so far
M3–M5 verified e2e through the proxy: post/reverse (balanced ✓, unbalanced →
400), deposit hold/capture (available drops, book unchanged until capture),
payment → cycle → settle (reserves move by exactly the net positions). Pickers
verified in-browser (Playwright): type-to-filter + participant→account cascade.
`typecheck` + `lint` clean at every milestone.
