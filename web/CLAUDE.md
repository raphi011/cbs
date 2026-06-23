# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

## What this is

Self-contained, educational Next.js frontend (in `web/`, own `package.json`, npm) for the Go ledger banking backend at the repo root (`github.com/raphi011/ledger`). It exposes all ~53 backend REST endpoints across three layers — general ledger → demand-deposit accounts → interbank payment network — plus a central bank. The whole point is teaching: explanatory `?` hints everywhere and a "how money moves" narrative.

## Commands

```bash
# Backend (from repo root): :8080, in-memory, resets on restart
go run ./cmd/server
# Frontend (from web/)
npm run dev          # http://localhost:3000
npm run typecheck    # tsc --noEmit — must be clean
npm run lint         # eslint — must be clean
npm run build        # production build; final gate before committing
```

There is no frontend test runner. Verify changes by driving the app in a browser (Playwright) against a running backend — the in-memory backend has no auth and is seeded via the API.

Stack: Next.js 16 (App Router) · React 19 · Tailwind v4 (no config file; tokens in `globals.css`) · shadcn/ui on Radix (imported from the unified `radix-ui` package) · TanStack Query · sonner · next-themes.

## Architecture

**Proxy / no CORS by construction.** `src/app/api/[...path]/route.ts` forwards every request to the Go backend (`BACKEND_URL`, default `http://localhost:8080`). The browser only ever calls same-origin `/api/...`, so CORS is impossible and a downed backend surfaces as a clean 502.

**Data layer grows in three files, one section per backend area:**
`src/lib/api/endpoints.ts` (one typed fn per route) → `src/lib/api/query-keys.ts` (key factory; ledger/deposit keys nest under `["participants", pid, …]` so one invalidate clears a subtree) → `src/lib/api/hooks.ts` (query/mutation hooks; mutations invalidate keys). `errors.ts` maps HTTP status → friendly text via `describeError`.

**Types & money.** `src/lib/types.ts` mirrors `api/dto.go` verbatim (exact JSON field names); enums in `src/lib/enums.ts` are the exact Go `String()` wire values. **All amounts are integer cents** — `src/lib/money.ts` is the source of truth (`<MoneyInput>` edits major units, emits cents).

**Routing.** Network-wide pages at `src/app/{payments,mandates,cycles,settlements,central-bank,schemes}` (global because a payment spans two participants). Participant-scoped pages under `src/app/participants/[pid]/`; to add a section, append to the `tabs` array in `[pid]/layout.tsx` and add the page.

**Reusable primitives — don't rebuild these** (`src/components/`): `Hint` (the `?` popover, registry in `hint-content.ts`), `Money`/`MoneyInput`/`AmountCell`, `DataTable`, `EnumBadge`, `ConfirmAction`, `Combobox` + domain pickers in `pickers/` (`ParticipantPicker`, `DepositAccountPicker`, `GLAccountPicker`) — use these for ID entry, never free-text. `PageHeader`, `FieldLabel`, `CopyId`, `ErrorState`.

## Backend contract gotchas (cause real failures)

- **`DisallowUnknownFields()`** — send *only* the exact keys a request DTO defines; a stray key → 400.
- **`*time.Time` wants RFC3339.** `<input type="date">` gives `YYYY-MM-DD`; convert to `${date}T00:00:00.000Z` or send `null`. **Exception:** snapshot `date` is a plain `YYYY-MM-DD` string.
- **Funding requires an existing deposit account.** `POST /participants/{pid}/deposits` credits a deposit account *and* raises the central-bank reserve in step; reserves start at 0. The intro loop is: create participant → open deposit account → fund.
- **Next 16 async params.** In route handlers & dynamic pages `params` is a `Promise`; pages use client components + `useParams()`, the proxy awaits `ctx.params`.

## Conventions

- Match the existing shadcn/Tailwind design system; this is a refined, minimal UI — don't impose a new aesthetic.
- shadcn is a new major: components import from the unified `radix-ui` package. `Card` uses a `--card-spacing` token + `size="sm"` variant rather than ad-hoc padding.
- The shared `DialogContent` ignores outside-interactions whose pointer landed within the dialog's own box — this keeps a Radix `Select`/popover (which makes the dialog `pointer-events:none` while open) from dismissing the whole dialog. `Hint` owns its open state and calls `preventDefault`/`stopPropagation` so it's safe inside links and clickable rows.
