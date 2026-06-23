# Educational Next.js Frontend for the Ledger Banking System

> Approved implementation plan. Build target: a self-contained Next.js app in
> `web/`. To continue: start a session on branch
> `claude/banking-frontend-educational-ve4db6` with npm egress enabled
> (`registry.npmjs.org` reachable) and implement this plan, verifying
> `npm run typecheck` / `lint` / dev build before committing.

## Context

The repo (`github.com/raphi011/ledger`) is a Go banking backend that recently
grew a complete REST API (~53 endpoints, defined in `api/handlers_*.go` and
`api/dto.go`) spanning three layers — general ledger → demand-deposit accounts →
interbank payment network — plus a central bank. It serves JSON on `:8080`, is
in-memory (state resets on restart), has no auth, and already sets permissive
CORS. There is **no frontend** today.

We want a really nice-looking Next.js frontend that (1) exposes **all** backend
operations, (2) **proxies** every request through Next.js so the browser only
ever talks to its own origin (no CORS by construction), and (3) is
**educational**: explanatory hints everywhere, especially small "?" buttons that
reveal a popover on click *and* tap (must work on mobile, not hover-only).

The README (`README.md`, ~53KB) is the authoritative source for the teaching
copy (double-entry, account types, holds, account lifecycle, payment lifecycle,
clearing vs settlement, netting).

**Decisions (confirmed with user):** Next.js App Router + TypeScript + Tailwind +
shadcn/ui (Radix); full coverage of all operations; self-contained app in `web/`
with its own `package.json` (npm).

## Approach

A self-contained Next.js app in `web/` with its own toolchain (Go module is
untouched — Go ignores non-`.go` trees). Add `web/node_modules`, `web/.next`,
`web/.env.local` to gitignore.

### Proxy (no CORS by construction)
A catch-all **Route Handler** at `web/src/app/api/[...path]/route.ts` (preferred
over `next.config` rewrites because it lets us normalize errors, log, strip
hop-by-hop headers, and return a clean 502 when the backend is down):
- Exports `GET`/`POST`/`DELETE`/`OPTIONS`, `export const dynamic = "force-dynamic"`.
- `BACKEND = process.env.BACKEND_URL ?? "http://localhost:8080"`.
- Rebuilds target from `params.path` joined with `/` + `request.nextUrl.search`
  (preserves `?account=`, `?entity=`, `?date=`).
- Forwards method/`Content-Type`/body; returns upstream status + body verbatim
  as JSON so the client's `{error}` parsing is uniform. On fetch failure →
  `502 {"error":"Backend unreachable at <url>"}`.
- The browser-side client always calls relative `/api/<backend-path>`; no
  client env var, so the Go origin is never exposed → CORS is impossible.

### Educational "?" hint (core deliverable)
`web/src/components/hint.tsx` built on Radix **Popover** (shadcn `popover`), NOT
Tooltip — Popover opens on click/tap (`pointerdown`), is keyboard/SR accessible,
and dismisses on outside-tap/Escape.
- Renders a small `?` icon button (lucide `HelpCircle`), `type="button"`,
  `aria-label`, `onClick` calls `preventDefault()` so it never submits a host form.
- API: `<Hint id="double-entry" />` (looks up `{title, body}` in the registry) or
  `<Hint title="…">ad-hoc JSX</Hint>`. `PopoverContent` max-width ~320px with
  `collisionPadding` to stay on-screen on mobile.
- `web/src/components/hint-content.ts`: a typed registry (`HintKey = keyof typeof
  hintContent`) of short paragraphs distilled from `README.md`: `double-entry`,
  `account-type-*`, `normal-balance`, `ledger-vs-subledger`, `amount-cents`,
  `idempotency-key`, `reversal`, `booking-date`, `value-date`, `balance-book`,
  `balance-holds`, `balance-available`, `overdraft`, `hold-*`, `account-status`,
  `scheme-direction-*`, `settlement-model-*`, `requires-mandate`, `allows-return`,
  `settlement-delay`, `mandate`, `payment-lifecycle`, `debtor-leg`, `creditor-leg`,
  `clearing-vs-settlement`, `netting`, `net-positions`, `reserve-account`,
  `central-bank-reserves`, `audit-trail`, `snapshot`.
- A `FieldLabel` (with `hint` prop) and `PageHeader` (optional `hint`) so every
  form field, section title, and table column header can carry a `?`.

### Data layer
- `web/src/lib/types.ts` — exact mirror of `api/dto.go` (verbatim JSON field names:
  `glAccount`, `customerSubledger`, `suspenseAccount`, `reserveAccount`,
  `settlementAccount`, `netPositions`, `debtorLegTx`, `creditorLegTx`, …). All
  amounts are integer **cents**. Request bodies must match DTOs exactly — the
  backend uses `DisallowUnknownFields()`, so never send extra keys.
- `web/src/lib/enums.ts` — string unions (`AccountType`, `Direction`,
  `DepositStatusAction = freeze|unfreeze|markDormant|reactivate`, statuses) + label
  and color maps; `<EnumBadge>`.
- `web/src/lib/api/client.ts` — single `request<T>(method, path, body?)` fetch
  wrapper hitting `"/api"+path`; parses `{error}` and throws `ApiError`; handles
  204/empty bodies; query-string helper dropping `undefined`.
- `web/src/lib/api/endpoints.ts` — one typed function per route (covering every
  endpoint in the map below).
- `web/src/lib/api/hooks.ts` + `query-keys.ts` — TanStack Query queries/mutations
  with a key factory; mutations invalidate affected keys (e.g. `postTransaction`
  invalidates that participant's transactions + account balances; `settleCycle`
  invalidates the cycle, settlements, payments, central-bank reserves).
- `web/src/lib/api/errors.ts` + `components/error-state.tsx` — `ApiError{status}`
  and `messageForStatus`: 400 "check highlighted fields", 404 "not found — state
  is in-memory and resets on restart", 409 "duplicate key / already applied",
  422 "not allowed in current state (insufficient funds, frozen, invalid
  transition)"; always also show the descriptive server `error` string. Mutations
  → `sonner` toast; queries → inline `<ErrorState>`; forms → top-of-form alert.
- `web/src/lib/money.ts` + `money-input.tsx` — cents are source of truth:
  `formatCents`, `formatSigned` (±, colored), `parseDollars` (`Math.round(x*100)`,
  NaN guard); `<MoneyInput>` edits major units but emits integer cents; `<Money>`
  and `<AmountCell>` (right-aligned, sign-colored) for display. EUR default (SEPA).
- `web/src/lib/dates.ts` — RFC3339 + `YYYY-MM-DD` (snapshot date) helpers.
- `web/src/lib/idempotency.ts` — `crypto.randomUUID()` key generator.

### Information architecture (network-wide vs participant-scoped)
`AppShell` sidebar with two groups. **Network** (no pid): Dashboard `/`, Payments,
Mandates, Cycles, Settlements, Central Bank, Schemes (reference/onboarding page).
**Participant** (under `/participants/[pid]/…`, enabled once selected): Overview,
General Ledger (accounts, transactions, audit), Deposit Accounts (DDA detail with
holds/snapshots/status, deposit audit), Funding. A topbar
`participant-switcher.tsx` lists `GET /participants`, persists to localStorage,
reflected in URL; `[pid]/layout.tsx` validates pid via `GET /participants/{pid}`
and renders sub-nav tabs (404 → friendly "not found"). Network-wide objects
(payments/cycles/settlements/mandates) are global because a payment spans two
participants.

### Endpoint → screen coverage (all 53)
| Screen | Endpoints |
|---|---|
| Participants | `POST/GET /participants`, `GET /participants/{pid}` |
| Funding | `POST /participants/{pid}/deposits` |
| Schemes | `GET /schemes` |
| Central Bank | `GET /central-bank/reserves`, `/reserves/{pid}`, `/audit` |
| Ledger tree | ledgers, subledgers, accounts CRUD + `GET .../accounts/{aid}/balance` |
| Transactions | `POST/GET .../transactions`, `GET .../{tid}`, `POST .../{tid}/reversal` |
| Ledger Audit | `GET .../audit?entity=` |
| Deposit accounts | `POST/GET .../deposit-accounts`, `GET .../{did}`, `/balance`, `POST .../status`, `DELETE .../{did}` |
| Holds | `POST/GET .../holds`, `GET /participants/{pid}/holds/{hid}`, `POST .../release`, `/capture` |
| Snapshots | `POST/GET .../snapshots` |
| Deposit Audit | `GET .../deposit-audit` |
| Mandates | `POST/GET /mandates`, `GET /{mid}`, `POST /{mid}/revoke` |
| Payments | `POST/GET /payments`, `GET /{payid}`, `POST .../reject`, `/return` |
| Cycles | `POST/GET /cycles`, `GET /{cid}`, `POST .../close`, `/settle` |
| Settlements | `GET /settlements`, `/settlements/{sid}` |

### Notable forms
`forms/post-transaction-form.tsx` (dynamic legs, live debit=credit indicator,
auto idempotency key, optional booking/value dates + metadata),
`initiate-payment-form.tsx` (scheme-aware: mandate field required iff
`requiresMandate`; return enabled iff `allowsReturn`), `create-mandate-form.tsx`,
`open-deposit-account-form.tsx`, `create-hold-form.tsx`, `capture-hold-form.tsx`,
`fund-participant-form.tsx`, and a generic `confirm-action.tsx` dialog for
reject/return/revoke/close/settle.

## Build order (milestones)
- **M0 Scaffold:** `create-next-app` (TS/App Router/Tailwind) + shadcn init; proxy
  route, `client.ts`, `types.ts`, `enums.ts`, `providers.tsx`, `AppShell`. Smoke
  test `GET /api/participants` through the proxy.
- **M1 Educational primitives:** `Hint` + registry, money helpers/inputs,
  `EnumBadge`, `data-table`, `error-state`, `PageHeader`, `CopyId`.
- **M2 Participants + Central Bank + Schemes:** create/fund a participant, read
  reserves, schemes reference — the intro loop.
- **M3 Ledger layer:** accounts tree, transactions + post/reversal, audit.
- **M4 Deposit layer:** accounts, status/close, holds (create/release/capture),
  snapshots, deposit audit.
- **M5 Payments/Clearing/Settlement:** mandates, payments (reject/return), cycles
  (open/close/settle, net-positions table), settlements.
- **M6 Dashboard + polish:** aggregations, "how money moves" explainer, loading/
  empty/error states, responsive mobile drawer, a11y pass, dark mode.

## Critical backend files to mirror (read-only references)
- `api/dto.go` — authoritative JSON field names, optional fields, enum strings, request DTOs.
- `api/errors.go` — which sentinels map to 400/404/409/422 (drives `errors.ts`).
- `api/handlers_participant.go`, `handlers_ledger.go`, `handlers_deposit.go`, `handlers_payment.go` — full route/method list.
- `README.md` — source copy for every `hint-content.ts` entry.
- `cmd/server/main.go` — confirms `PORT`/`:8080` default and in-memory reset (proxy default + "data resets" UX note).

## Verification
1. **Backend:** `go run ./cmd/server` (listens on `:8080`); confirm `go test ./...` still passes (frontend changes shouldn't affect Go).
2. **Frontend:** `cd web && npm install && npm run dev` → `http://localhost:3000`.
   `npm run typecheck` (tsc --noEmit) and `npm run lint` are clean.
3. **Proxy / no CORS:** in the browser devtools Network tab, confirm requests go to
   same-origin `/api/...` (not `:8080`) and succeed; stop the Go server and confirm
   a clean 502 "Backend unreachable" surfaces.
4. **End-to-end teaching loop:** create a participant → fund it → open a deposit
   account → check book/holds/available → create a hold → capture it → initiate a
   SEPA payment between two participants → open/close/settle a clearing cycle →
   view the settlement net positions and central-bank reserves.
5. **Hints on mobile:** with devtools device emulation (touch), tap a "?" button and
   confirm the popover opens on tap and dismisses on outside-tap/Escape.
6. **Money round-trip:** type `30.00` in a `MoneyInput`, confirm the API receives
   `3000` cents and the value renders back as `30.00`.
