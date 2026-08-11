# Resettable ledger state + comprehensive seed data

**Date:** 2026-06-23
**Status:** Approved (design)

## Goal

1. Reset the entire application state by swapping out the single state root, so a
   user can return the system to a known-good state on demand.
2. Ship comprehensive, consistent, valid sample data covering every domain
   concept (multiple banks, deposit accounts, holds, snapshots, mandates,
   payments across their full lifecycle, clearing cycles, settlements, and the
   general-ledger primitives).

## Decisions (locked)

- **Reset trigger:** HTTP endpoint **plus** a web button.
- **Reset result:** always reloads the comprehensive sample data (no empty
  variant).
- **Boot:** the server starts pre-loaded with the sample data.
- **Seed timestamps:** deterministic — a fixed base date that advances through
  the scenario; live operations performed *after* a reset use real `time.Now`.

## Background: current architecture

- `payment.Network` is already the single state root. It owns the scheme
  registry, all participants (each carrying its own `ledger.Book` +
  `deposit.Register`), payments, mandates, clearing cycles, settlements, and the
  central-bank `ledger.Book`.
- Every `Book`/`Register` a network creates is threaded the **same**
  `clock func() time.Time` (`NewNetworkWithClock`). This shared clock is the
  lever for coherent dates across banks.
- The only place the network is "held" is `api.Server.net *payment.Network`,
  referenced 26 times (directly and via the `s.participant()` helper).
- Both SEPA schemes allow returns. SCT: push, no mandate, T+1. SDD: pull,
  requires an active matching mandate, T+2.
- Customer accounts open with overdraft 0 via `Participant.OpenCustomerAccount`;
  opening with an overdraft limit uses `Deposit.OpenAccount(subledger, name,
  limit)` directly (the seed has access to the exported `CustomerSubledger`).
- Frontend: same-origin proxy at `/api/[...path]`, a single `request()` fetch
  wrapper, TanStack Query with a key factory, `ConfirmAction`/`Button` and
  `sonner` toasts as reusable primitives.

## Component 1 — the state seam (backend)

Make `api.Server` hold the network behind an atomic pointer and a factory:

```go
type Server struct {
    state    atomic.Pointer[payment.Network] // the live network
    newState func() *payment.Network          // factory: builds a fresh seeded network
    log      *slog.Logger
}
```

- `NewServer(newState func() *payment.Network, log *slog.Logger) *Server` calls
  `newState()` once to populate the initial state.
- Private accessor `func (s *Server) network() *payment.Network { return s.state.Load() }`.
  Replace the 26 `s.net` references (in `server.go`, `handlers_participant.go`,
  `handlers_payment.go`) with `s.network()`.
- `func (s *Server) Reset() { s.state.Store(s.newState()) }`.

**Why `atomic.Pointer`:** lock-free reads (no per-request lock), single atomic
swap. A request that already loaded the old network finishes against it
consistently (still a valid object, GC'd once unreferenced); new requests get
the fresh network. Injecting `newState` as a factory keeps the dependency arrow
one-way (`seed → payment`, `api → payment`, `main` wires them); `api` never
imports `seed`.

## Component 2 — the `seed` package + deterministic clock

New top-level package `github.com/raphi011/ledger/seed`, depending on
`payment`/`deposit`/`ledger`. Public surface:

```go
// Network builds a fresh, fully populated payment.Network on a deterministic
// clock, then switches the clock to live (time.Now) before returning.
func Network() *payment.Network
```

Internal controllable clock:

- Frozen at a fixed base date (`2025-09-15 09:00 UTC`) while building.
- Advances through scenario steps (so booking dates < value dates look real).
- Flipped **live** (returns `time.Now`) before `Network()` returns, so all books
  and registers — which captured the clock at construction — switch to real time
  for any post-reset operation.

Consequences: seed data has stable, reproducible dates **and IDs** (`bank_1`,
`dep_1`, …). Stable IDs mean the frontend's selected participant survives a
reset. Operations done after a reset run in real time.

## Component 3 — the sample scenario

Four banks, funded customers, covering every concept. Built in a valid order;
funding amounts sized so every debtor's available balance covers its outgoing
payments and holds (validity by construction).

- **4 participants (banks):** each automatically gets its GL, clearing suspense,
  reserve, and central-bank reserve account.
- **Deposit accounts:** 2–3 customers per bank, including one **Frozen**, one
  **Dormant**, one **Closed** (opened then closed at zero balance), and one with
  an **overdraft limit**.
- **Funding:** deposits that fund customers and raise each bank's central-bank
  reserve.
- **Holds:** one **Active** (reduces available), one **Released**, one
  **Captured** (posts a real intrabank GL transaction).
- **Snapshots:** end-of-day snapshots for one account across two business days
  (clock advances between).
- **Mandates:** two **Active** + one **Revoked**.
- **Payments across the full lifecycle:** several **Settled** SCT (push) and
  **Settled** SDD (pull, via mandate); one **Returned** (settled SDD then
  R-transaction); one **Rejected** (before clearing); a couple **Accepted** in a
  still-**Open** cycle; a couple **Cleared** in a **Closed-not-settled** cycle.
- **Clearing cycles & settlements:** one settled SCT cycle, one settled SDD
  cycle (→ two Settlements with non-trivial net positions), one Closed cycle,
  one Open cycle.
- **GL primitives (one showcase bank):** add an **Equity** account
  (capitalization transaction) and a **Revenue** account (fee transaction), plus
  one explicit **reversal** — so Asset/Liability/Equity/Revenue account types and
  manual transactions/reversals all appear directly.

## Component 4 — reset endpoint + frontend button

- **Backend:** `POST /admin/reset` → `s.Reset()` → `200 {"status":"reset"}`,
  registered in `Routes()`.
- **`main()`:** `api.NewServer(seed.Network, log)` — boots seeded.
- **Frontend:** replace the static `ResetNote` in `app-shell.tsx` with a reset
  button (sidebar + mobile drawer). Click → `ConfirmAction` → `POST /admin/reset`
  → `queryClient.clear()` (or invalidate all) + a `sonner` toast. Uses existing
  primitives and the established data-layer pattern. A small reset hook may live
  under `web/src/lib/api/`.

## Component 5 — testing

- `seed/seed_test.go`: build `seed.Network()` and assert invariants — required
  balances non-negative, central-bank net positions sum to zero, payment counts
  per status match expectations, two independent builds produce identical IDs,
  balances, and per-status payment counts (determinism), and the clock went live
  (a post-build timestamp is near real `time.Now`, not the base date).
- `api`: a test that `POST /admin/reset` returns 200 and yields fresh seeded
  state; update `newTestServer` to the new `NewServer(factory, log)` signature
  (tests still build empty networks for their own scenarios).

## Files touched

- `api/server.go` — state seam, accessor, `Reset`, route registration.
- `api/handlers_participant.go`, `api/handlers_payment.go` — `s.net` →
  `s.network()`.
- `api/server_test.go` — updated `NewServer` signature.
- **new** `seed/seed.go` (+ `clock.go`, `seed_test.go`, `doc.go`).
- `cmd/server/main.go` — wire `seed.Network`.
- `web/src/components/app-shell.tsx` — reset button.
- possibly a small reset hook under `web/src/lib/api/`.

## Non-goals

- Persistence / durable storage (state stays in memory).
- Partial reset or per-entity deletion.
- An "empty" reset variant.
- Configurable seed contents at runtime.
