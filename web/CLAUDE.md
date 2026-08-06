# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

## What this is

Self-contained, educational Next.js frontend (in `web/`, own `package.json`, npm) for the Go ledger banking backend at the repo root (`github.com/raphi011/ledger`). It exposes backend REST endpoints across four layers — general ledger → demand-deposit accounts → interbank payment network → lending — plus a central bank. The whole point is teaching: explanatory `?` hints everywhere and a "how money moves" narrative.

## Commands

```bash
# Backend (from repo root): :8080, ephemeral SQLite, resets on restart
go run ./cmd/server
# ...or against a file, where state survives a restart (see the root README)
DATABASE_URL=./cbs.db go run ./cmd/server
# ...or `make dev` from the repo root: backend + frontend, one command, no setup
# Frontend (from web/)
npm run dev          # http://localhost:3000
npm run typecheck    # tsc --noEmit — must be clean
npm run lint         # eslint — must be clean
npm run test         # vitest — quiz bank, statement projection, concept links
npm run build        # production build; final gate before committing
```

`npm run test` is a vitest suite over the pure logic only (`src/lib/**`, `src/components/concept-links.ts`); there is no component or E2E runner. It is not optional: a `[[wiki-link]]` pointing at a key that is not in `hint-content.ts` throws under `RootLayout` and takes **every** route down in dev, and because that guard is skipped when `NODE_ENV === "production"`, `npm run build` stays green while the dev app is dead. Verify UI changes by driving the app in a browser against a running backend — the backend has no auth and is seeded via the API.

Stack: Next.js 16 (App Router) · React 19 · Tailwind v4 (no config file; tokens in `globals.css`) · shadcn/ui on Radix (imported from the unified `radix-ui` package) · TanStack Query · sonner · next-themes.

## Architecture

**Proxy / no CORS by construction.** `src/app/api/[...path]/route.ts` forwards every request to the Go backend. The browser only ever calls same-origin `/api/...`, so CORS is impossible and a downed backend surfaces as a clean 502.

**There is no single backend.** Each entity has a listener of its own (see the operator-split API spec): `:8081` the central bank, `:8082` the clearing house, then one per member bank in registration order. A request therefore has to say which one it is for, and the first segment after `/api` is the operator key — `central-bank`, `clearing-house`, or `bank/<pid>`, which the proxy strips before forwarding. **Build those paths with `cb()`, `csm()` and `bank(pid, …)` from `src/lib/api/operator.ts`; never hand-write one.** A bank's port is resolved from its position in the clearing house's `GET /members` roster, mirroring `cmd/server`'s `plan()`, so `make dev` needs no configuration; `BACKENDS` (JSON, operator key → base URL) overrides it. A participant admitted at runtime has **no listener until the server restarts** — admission is not provisioning — and the proxy says so rather than hanging. The port derivation itself lives in `src/lib/api/backend-url.ts`, shared by the proxy and by `app/api/operators/route.ts`, which probes every operator so the lobby can tell an un-provisioned bank from a running one.

**Data layer grows in three files, one section per backend area:**
`src/lib/api/endpoints.ts` (one typed fn per route) → `src/lib/api/query-keys.ts` (key factory; ledger/deposit keys nest under `["participants", pid, …]` so one invalidate clears a subtree) → `src/lib/api/hooks.ts` (query/mutation hooks; mutations invalidate keys). `errors.ts` maps HTTP status → friendly text via `describeError`.

**Types & money.** `src/lib/types.ts` mirrors `api/dto.go` verbatim (exact JSON field names); enums in `src/lib/enums.ts` are the exact Go `String()` wire values. **All amounts are integers in the minor units of their asset** — cents for EUR, satoshi for BTC — and the asset's `scale` is what converts one to a human-readable major-unit string. `src/lib/money.ts` is the source of truth, and every formatter in it takes the asset it is rendering; there is no default scale, because assuming 2 renders 1 BTC as "1,000,000.00". `<MoneyInput>` edits major units and emits minor units at the given asset's scale, and resyncs its text when the asset changes or the parent clears the value — never mid-keystroke. Scales come from `useAssetLookup()`, one network-wide `GET /assets` shared by every caller (asset definitions live in Go, not in a per-book table). A code that has not resolved yet means **do not render a number**.

**Routing is by persona.** Who you are is the top-level structure, and it lines
up with the operator split: `/central-bank/…` (reserves, its own audit, and
settling a closed cycle), `/clearing-house/…` (payments, mandates, cycles,
settlements, schemes, directory), `/bank/[pid]/…` (one bank's back office,
including its own payment legs), `/customer/[pid]/[did]/…` (one deposit account,
retail-framed). `/` is a lobby and never redirects; `/learn/*` sits outside the
persona system. `src/lib/identity.ts` derives the `Identity` from the pathname
and owns `homeFor`/`navFor`/`backendFor` — the last being the operator key the
proxy routes on, which is why a customer resolves to their *bank's* listener and
not one of their own. **To add a section, add its entry to `navFor` and its
`page.tsx` in the same commit; `src/lib/nav-integrity.test.ts` holds the two
together, and also holds the quiz's `EXPLORE_ROUTES` against the route tree.**
Old `/participants/…` links are forwarded by `app/participants/[...rest]/page.tsx`.
The central bank, the clearing house and a bank's back office share one
`ConsoleShell` from `components/shell/`, parameterised by identity; the
customer's shell has no left panel, a different-enough arrangement that it
doesn't join that group. `plain-shell` is the lobby's and Learn's.

**Reusable primitives — don't rebuild these** (`src/components/`): `Hint` (the `?` popover, registry in `hint-content.ts`), `Money`/`MoneyInput`/`AmountCell`, `DataTable`, `EnumBadge`, `ConfirmAction`, `Combobox` + domain pickers in `pickers/` (`ParticipantPicker`, `DepositAccountPicker`, `GLAccountPicker`) — use these for ID entry, never free-text. `PageHeader`, `FieldLabel`, `IdText` (monospace ID display), `ErrorState`.

## Backend contract gotchas (cause real failures)

- **`DisallowUnknownFields()`** — send *only* the exact keys a request DTO defines; a stray key → 400.
- **`*time.Time` wants RFC3339.** `<input type="date">` gives `YYYY-MM-DD`; convert to `${date}T00:00:00.000Z` or send `null`. **Exception:** snapshot `date` is a plain `YYYY-MM-DD` string.
- **Funding requires an existing deposit account, and it is now TWO steps.** `POST /deposits` credits a deposit account and leaves the bank holding the cash as *vault cash*; it does **not** raise the central-bank reserve, which it used to do in step. `POST /lodgements` (`{asset, amount}`, answers **202**) is what puts that cash on reserve, because only the central bank can credit an account in the central bank's book — it is a `camt.050`/`camt.025` conversation, so the reserve is not up when the request resolves. Reserves start at 0 and a bank cannot settle out of vault cash, so the intro loop is: create participant → open deposit account → fund → **lodge**.
- **Next 16 async params.** In route handlers & dynamic pages `params` is a `Promise`; pages use client components + `useParams()`, the proxy awaits `ctx.params`.

## Conventions

- Match the existing shadcn/Tailwind design system; this is a refined, minimal UI — don't impose a new aesthetic.
- shadcn is a new major: components import from the unified `radix-ui` package. `Card` uses a `--card-spacing` token + `size="sm"` variant rather than ad-hoc padding.
- The shared `DialogContent` ignores outside-interactions whose pointer landed within the dialog's own box — this keeps a Radix `Select`/popover (which makes the dialog `pointer-events:none` while open) from dismissing the whole dialog. `Hint` owns its open state and calls `preventDefault`/`stopPropagation` so it's safe inside links and clickable rows.
