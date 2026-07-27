# Expansion Roadmap: Multi-Asset, Lending, Crypto, FX

Living tracking document for the expansion of the CBS from a single-currency
deposits sub-ledger into a multi-asset core that also covers lending, crypto and
exchange.

**Status legend:** `todo` · `spec` (design agreed, spec written) · `plan`
(implementation plan written) · `wip` · `done`

## Goal and framing

The repository stays a **teaching reference**, but the backend should be
polished and behave like a real core banking system. That means: no toy
shortcuts in the accounting core, correct invariants enforced at the boundary,
and every new domain carried through all layers the way the existing ones are.

Two consequences that apply to every sub-project below:

- **Domain knowledge stays consistent across layers.** `README.md` is
  authoritative; `web/src/components/hint-content.ts`, the quiz chapters under
  `web/src/lib/quiz/chapters/`, and the schema comments in
  `store/pg/schema/` all have to move with it. See `CLAUDE.md`.
- **Postgres stays optional.** `store/mem` and `store/pg` must remain
  indistinguishable through `store/storetest`. Every new entity added below
  needs conformance coverage, not just a `store/pg` implementation.

## Where the code stands today

- `ledger` — pure double-entry GL: ledgers, subledgers, accounts (Asset,
  Liability, Equity, Revenue, Expense), multi-leg balanced transactions,
  on-demand balances, idempotency, reversal, audit log. `ledger.Book`.
- `deposit` — DDA layer: account status/lifecycle, overdraft limits, holds and
  available balance, end-of-day snapshots. Each deposit account wraps a backing
  Liability GL account and stores no money itself. `deposit.Register`.
- `payment` — interbank network: participants, SEPA CT (push) and SEPA DD
  (pull), `Initiated → Accepted → Cleared → Settled`, net settlement against a
  central-bank book inside one store transaction. `payment.Network`.
- `api`, `store/mem`, `store/pg`, `store/storetest`, `seed`, `web`.

**There is no asset or currency dimension anywhere.** `ledger.Amount` is a bare
`int64` of minor units, `ledger.Entry` has no currency field, the `accounts` and
`entries` tables have no currency column, and `Book.Post` balances a transaction
globally rather than per asset. This is the single structural gap that
everything else waits on.

## Sub-projects

Each gets its own spec → plan → implementation cycle. They are listed in
build order.

### 1. Multi-asset ledger core — `spec`

Spec: [`superpowers/specs/2026-07-27-multi-asset-ledger-design.md`](superpowers/specs/2026-07-27-multi-asset-ledger-design.md)

Introduce an asset dimension: assets as first-class, book-scoped entities (code,
scale, class), an asset on every GL account, per-asset balance enforcement in
`Book.Post`, and the corresponding deposit, payment, schema, store, conformance,
API, web and documentation changes.

Foundational — it touches `ledger.Account`, `Book.Post`, `deposit.OpenAccount`,
the participant account triple, both stores, `store/storetest`, the API DTOs and
the web app. Doing lending first would mean doing lending twice.

Decisions settled (full reasoning in the spec):

- **One asset per GL account.** Balances stay scalar; matches per-currency IBANs.
- **Transactions balance per asset.** FX therefore runs through per-asset
  position accounts, keeping rates out of the ledger — this constrains
  sub-project 4.
- **`Amount` stays `int64`, asset scale capped at 9.** `int64` holds 9.2 ETH at
  18 decimals, so native wei precision and `int64` are mutually exclusive.
  `type Amount = int64` becomes a defined type so a later widening is
  compiler-guided.
- **No customer entity.** Multi-asset does not need one; a party master would be
  its own sub-project.
- **Schemes declare their asset.** SEPA CT and DD are EUR. The ledger cannot
  catch a EUR account paying a BTC account — each leg balances in its own
  asset — so the check lives in `payment`.

### 2. Lending — `todo`

Loan accounts on the Asset side, disbursement, amortization schedules, interest
accrual, repayment allocation (interest before principal), delinquency and
non-performing states.

Almost pure addition on top of the existing GL, and the mirror image of the
deposit layer: where `deposit` wraps a Liability account, lending wraps an Asset
account. It is the best available proof that the GL really generalizes, and the
richest teaching material per line of code.

### 3. Crypto — `todo`

Scope deliberately undecided. Ranges from custody balances denominated in
non-fiat units (small, once multi-asset lands) through to on-chain settlement
and stablecoin rails (a payment-network-sized project of its own). To be pinned
down before its spec.

### 4. FX / exchange — `todo`

Trades against two assets, rate handling, spread recognized as revenue, position
and exposure accounts, settlement conventions.

Depends directly on the transaction-balance decision made in sub-project 1.

## Log

| Date | Entry |
| --- | --- |
| 2026-07-27 | Roadmap created. Ordering agreed: multi-asset → lending → crypto → FX. Framing agreed: teaching reference, production-grade backend. |
| 2026-07-27 | Sub-project 1 design agreed and spec written. |
