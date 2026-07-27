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

**The asset dimension is in.** The known assets are a package-level list of
`ledger.AssetDef`s in code (code, name, scale, class — `ledger.LookupAsset`),
not rows; every GL account is denominated in exactly one, fixed at creation;
`deposit.Account` carries the same asset; transactions balance **per asset**;
schemes declare their asset and both legs of a payment are validated against it;
participants hold one suspense/reserve/settlement set per asset they joined
with. The whole schema is `0001_init.sql`. See `README.md` and quiz chapter 16.

## Sub-projects

Each gets its own spec → plan → implementation cycle. They are listed in
build order.

### 1. Multi-asset ledger core — `done`

Spec: [`superpowers/specs/2026-07-27-multi-asset-ledger-design.md`](superpowers/specs/2026-07-27-multi-asset-ledger-design.md)

Introduced an asset dimension: assets as first-class definitions (code, name,
scale, class), an asset on every GL account, per-asset balance enforcement in
`Book.PostTransaction`, and the corresponding deposit, payment, schema, store,
conformance, API, web and documentation changes.

A follow-up removed the book-scoped `assets` *table* it had shipped with. The
table was writable at runtime and nothing downstream could honour a runtime
asset — a bank's plumbing accounts are provisioned when it joins the network —
so it promised a capability the system could not deliver, and the mismatch
surfaced as a reachable 404 on funding. The definitions moved into Go, where
schemes already live. What stayed per bank is `participant_assets`: which assets
a bank operates in.

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
  is never one posting: at initiation only the debtor leg exists, and it is
  impeccable double-entry within one asset. The ledger *does* catch the mismatch
  eventually — at settlement, where the creditor leg is built from the
  creditor's suspense account in the scheme's asset and comes out unbalanced —
  but settlement is all-or-nothing, so one bad payment fails the whole cycle and
  the error names an imbalance rather than the payment behind it. The check
  therefore went into `InitiatePaymentTx` itself rather than into a scheme's
  `Validate`, so it runs for every scheme rather than only those that check
  funds, at the one moment both ends are in view and neither is written.
- **The asset check is a domain rule, so the schema gets no constraint.**
  `accounts.asset` deliberately has no `CHECK` restricting it to the known
  codes; Postgres could express "the asset must be one the system knows" and
  `store/mem` could not, and the conformance subtest
  `ParentReferencesAreNotEnforced` is what holds the line. A `CHECK` would also
  turn a one-line change to a Go slice into a migration. `0001_init.sql` records
  the reasoning in the database with `COMMENT ON COLUMN`, because the absence of
  a constraint is invisible in a schema dump.

### 2. Lending — `plan`

Spec: [`superpowers/specs/2026-07-27-lending-design.md`](superpowers/specs/2026-07-27-lending-design.md)
Plan: [`superpowers/plans/2026-07-27-lending.md`](superpowers/plans/2026-07-27-lending.md)

Loan accounts on the Asset side, disbursement, amortization schedules, interest
accrual, repayment allocation (interest before principal), delinquency and
non-performing states.

Three products ship together — term loan, revolving credit line, and the
existing arranged overdraft given a rate — because the third is what stops the
abstraction from being loan-shaped by accident. Deferred to their own future
work: non-accrual/NPL accounting, ECL provisioning and IFRS 9 staging,
write-off, fees, early-repayment penalties, restructuring.

The design's load-bearing decision is that **an overdrawn current account gets no
loan account.** The drawn amount is the negative balance of the customer's
Liability account viewed by sign; it has no independent existence, so storing it
would create exactly the drift the unified-ledger design eliminates. The
Asset-side classification is a derived aggregate,
`Σ max(0, −balance)` over the Customer Deposits subledger — the same query shape
"total customer deposits" already is. In a real bank that split falls out of
subledger-to-GL summarization, which this repo has no equivalent of, and
deliberately so (`docs/deposit-accounts-vs-subledger.md`, section 7).

That decision drives the packaging: a pure `interest` package (day counts,
accrual, the sub-minor-unit accrued type), overdraft terms staying in `deposit`
where the limit already lives — which is also where real core banking puts them,
in the CASA module rather than Loans — and a new `lending` package for the two
products whose drawn amount is real. A single `lending` owning all three would
have produced `deposit → lending → deposit`.

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
known asset with scale 8 and works today. But the **scale cap of 9 is a hard
boundary on the scope**, and it lands squarely on the most obvious next asset:
ether and most ERC-20 tokens are 18-decimal, and `int64` holds 9.2 ETH at native
precision. So any crypto scope that includes the Ethereum family has to choose
one of:

- hold them at reduced precision (list at scale 9, discard the last nine
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
| 2026-07-27 | Sub-project 1 **done**. Assets, per-asset balancing, scheme assets, per-asset participant accounts, migrations `0002`–`0006`, API and web carried through, and the docs updated across all four layers (README, hints, quiz chapter 16, schema comments). Every decision in the spec shipped as designed. Learned: the payment-layer asset check is justified more strongly than the spec argued (a payment is two postings in two books, each valid on its own); the asset check being a domain rule is what forbids a constraint on `accounts.asset`; and the scale cap of 9 is a hard scoping constraint on sub-project 3, while the "can a position account go negative?" question is a hard one on sub-project 4. FX position accounts still look like the right shape. |
| 2026-07-27 | Sub-project 2 design agreed and spec written. Scope widened to three products (term loan, revolving line, arranged overdraft); impairment deferred. Settled: an overdrawn account gets no loan account, because its drawn amount is the negative balance viewed by sign and has no independent existence — the Asset-side figure is a derived aggregate, as "total customer deposits" already is. Rejected a reclassification journal and an EOD sweep (the latter models a linked-loan product, not an arranged overdraft) and rejected restructuring deposits into control accounts as a regression. Packaging follows: pure `interest`, overdraft terms in `deposit` (CASA, not Loans — and a single `lending` owning the limit would cycle through `deposit`), `lending` for the two real-drawdown products. Interest is held at sub-minor-unit precision and posted daily as the delta of the rounded value; repayment allocates against actual accrued interest, not the schedule, which is why 30/360 exists. |
| 2026-07-27 | Sub-project 1 **reworked**, in two parts. (1) The book-scoped `assets` table is gone; the definitions are a package-level list in `ledger`, the way schemes are Go types. The table was writable but unhonourable — a bank's plumbing accounts are provisioned when it joins, so an asset registered later gave a customer account that could never settle, surfacing as a 404 on funding. `GET /assets` replaces the per-participant registry endpoints; migrations `0002`–`0006` were folded into `0001` (no deployed databases; `migrate.go`'s own doc comment sanctions it). (2) The whole-branch review's findings: the euro-to-bitcoin counter-example was **wrong in five places** — the ledger does catch it, at settlement, and what it cannot catch is the payment at *initiation*; that correction is now pinned by a test rather than by argument. Also: N+1 round trips in two listing endpoints, an asset on both balance responses, mismatched-asset mandates refused, `ErrParticipantAssetNotFound` → 422, and the web form that silently reinterpreted an overdraft limit at a new scale. Learned: this is the **second** counter-example on this branch that argued the opposite of what it claimed. Prose that asserts what code does needs a test, not a careful reading. |
