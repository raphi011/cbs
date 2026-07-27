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

**The asset dimension is in.** Assets are book-scoped `ledger.AssetDef`s (code,
name, scale, class); every GL account is denominated in exactly one, fixed at
creation; `deposit.Account` carries the same asset; transactions balance **per
asset**; schemes declare their asset and both legs of a payment are validated
against it; participants hold one suspense/reserve/settlement set per asset.
Migrations `0002`–`0006`. See `README.md` and quiz chapter 16.

## Sub-projects

Each gets its own spec → plan → implementation cycle. They are listed in
build order.

### 1. Multi-asset ledger core — `done`

Spec: [`superpowers/specs/2026-07-27-multi-asset-ledger-design.md`](superpowers/specs/2026-07-27-multi-asset-ledger-design.md)

Introduced an asset dimension: assets as first-class, book-scoped entities (code,
scale, class), an asset on every GL account, per-asset balance enforcement in
`Book.PostTransaction`, and the corresponding deposit, payment, schema, store,
conformance, API, web and documentation changes.

Foundational — it touched `ledger.Account`, `Book.PostTransaction`,
`deposit.OpenAccount`, the participant account triple, both stores,
`store/storetest`, the API DTOs and the web app. Doing lending first would have
meant doing lending twice.

Decisions settled (full reasoning in the spec), all of which shipped as designed:

- **One asset per GL account.** Balances stay scalar; matches per-currency IBANs.
- **Transactions balance per asset.** FX therefore runs through per-asset
  position accounts, keeping rates out of the ledger — this constrains
  sub-project 4.
- **`Amount` stays `int64`, asset scale capped at 9.** `int64` holds 9.2 ETH at
  18 decimals, so native wei precision and `int64` are mutually exclusive.
  `Amount` became a defined type so a later widening is compiler-guided.
- **No customer entity.** Multi-asset did not need one; a party master would be
  its own sub-project.
- **Schemes declare their asset.** SEPA CT and DD are EUR. The ledger cannot
  catch a EUR account paying a BTC account — each leg balances in its own
  asset — so the check lives in `payment`.

Two things the implementation sharpened, both worth carrying forward:

- **Why the ledger cannot catch a cross-asset payment is stronger than the spec
  said.** It is not merely that "each leg balances in its own asset". A payment
  is never one posting: the debtor leg is a transaction in the payer's bank's
  book and the creditor leg a separate one in the payee's. Both are impeccable
  double-entry within their own asset. The error is in the *claim that the two
  are halves of one payment* — a claim no ledger can see. The check therefore
  went into `InitiatePaymentTx` itself rather than into a scheme's `Validate`,
  so it runs for every scheme rather than only those that check funds.
- **The registry check is a domain rule, so the schema gets no foreign key.**
  `accounts.asset` deliberately has no FK to `assets`; Postgres could express
  "the asset must be registered" and `store/mem` could not, and the conformance
  subtest `ParentReferencesAreNotEnforced` is what holds the line. Migration
  `0006` records the reasoning in the database with `COMMENT ON COLUMN`, because
  the absence of a constraint is invisible in a schema dump.

### 2. Lending — `todo`

Loan accounts on the Asset side, disbursement, amortization schedules, interest
accrual, repayment allocation (interest before principal), delinquency and
non-performing states.

Almost pure addition on top of the existing GL, and the mirror image of the
deposit layer: where `deposit` wraps a Liability account, lending wraps an Asset
account. It is the best available proof that the GL really generalizes, and the
richest teaching material per line of code.

*After sub-project 1:* essentially unchanged in shape. A loan account now names
an asset like any other, which is one more argument on `OpenLoan` and nothing
else. One thing to be deliberate about rather than surprised by: lending in an
asset the bank funds itself in a *different* asset is an FX exposure, whether or
not anyone models it. If lending lands before FX, that exposure exists and is
simply unrecorded — worth saying so in the docs rather than discovering it.

### 3. Crypto — `todo`

Scope deliberately undecided. Ranges from custody balances denominated in
non-fiat units (small, once multi-asset lands) through to on-chain settlement
and stablecoin rails (a payment-network-sized project of its own). To be pinned
down before its spec.

*After sub-project 1:* the small end of that range is now nearly free — BTC is a
registered asset with scale 8 and works today. But the **scale cap of 9 is a hard
boundary on the scope**, and it lands squarely on the most obvious next asset:
ether and most ERC-20 tokens are 18-decimal, and `int64` holds 9.2 ETH at native
precision. So any crypto scope that includes the Ethereum family has to choose
one of:

- hold them at reduced precision (register at scale 9, discard the last nine
  places) and document the truncation at the boundary; or
- widen `Amount` to a 128-bit type — which the defined type makes a
  compiler-guided change rather than an audit, and which is the reason `Amount`
  was made a defined type in the first place.

That choice should be made *in the spec*, not during implementation.

A second, cheaper finding: a new scheme in a new asset is a data change, not a
schema change, because participants hold their internal accounts in a
`participant_assets` child table keyed `(participant_id, asset)`.

### 4. FX / exchange — `todo`

Trades against two assets, rate handling, spread recognized as revenue, position
and exposure accounts, settlement conventions.

Depends directly on the transaction-balance decision made in sub-project 1.

*After sub-project 1 — does the position-account approach still look right?*
**Yes, and more strongly than before.** Per-asset balancing is now real code, and
it does exactly what the design predicted: it makes the naive two-asset posting
impossible, which forces each side of a trade through a position account of its
own asset, which is what keeps rates out of the ledger entirely. Nothing in the
implementation argued against it, and the alternative — a global check plus a
conversion — is now visibly the design that puts a price inside an accounting
engine.

Three things the implementation adds to the FX spec's inbox:

- **What account type is a position account?** This is now a real constraint
  rather than a modelling preference. `checkSufficientBalance` refuses to let an
  **Asset** or **Expense** account go below zero, and a position account must be
  able to go negative — that is precisely what being *short* an asset means. So
  a position account cannot simply be an Asset account. Either it is modelled on
  the Liability/Equity side, or the balance rule needs an explicit exemption.
  Decide this in the spec; it is the kind of thing that is very annoying to
  discover halfway through.
- **The spread is per asset too.** "Spread recognized as revenue" means a revenue
  account, and revenue accounts are denominated like every other account. FX
  revenue is therefore earned in a specific asset, and which one is a real
  decision (the asset sold? a reporting currency? both, via a further trade).
- **Revaluation is explicitly above the ledger.** Position balances are quantities,
  not values. Turning them into a P&L figure needs a rate, so it belongs in the
  same layer that quotes trades — not in `ledger`, and this should stay true.

## Log

| Date | Entry |
| --- | --- |
| 2026-07-27 | Roadmap created. Ordering agreed: multi-asset → lending → crypto → FX. Framing agreed: teaching reference, production-grade backend. |
| 2026-07-27 | Sub-project 1 design agreed and spec written. |
| 2026-07-27 | Sub-project 1 **done**. Assets, per-asset balancing, scheme assets, per-asset participant accounts, migrations `0002`–`0006`, API and web carried through, and the docs updated across all four layers (README, hints, quiz chapter 16, schema comments). Every decision in the spec shipped as designed. Learned: the payment-layer asset check is justified more strongly than the spec argued (a payment is two postings in two books, each valid on its own); the registry check being a domain rule is what forbids a foreign key on `accounts.asset`; and the scale cap of 9 is a hard scoping constraint on sub-project 3, while the "can a position account go negative?" question is a hard one on sub-project 4. FX position accounts still look like the right shape. |
