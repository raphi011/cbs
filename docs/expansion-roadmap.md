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
schemes declare their asset and each leg of a payment is validated against it by the bank that holds that leg's account;
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
  therefore went into initiation itself rather than into a scheme's `Validate`,
  so it runs for every scheme rather than only those that check funds.
  *Superseded in part by 7b:* the second half of that reasoning — "at the one
  moment both ends are in view and neither is written" — described a moment the
  message layer removed. `InitiatePaymentTx` split into a debtor bank's half and
  a creditor bank's half, so no actor reads both accounts and each bank compares
  its own leg against the scheme's asset. That is strictly weaker (two banks
  could each hold a conforming account in a scheme neither is entitled to) and
  strictly what a real bank can do. Where the check runs and why it is not in
  `Validate` are unchanged; only the claim about a single moment is.
- **The asset check is a domain rule, so the schema gets no constraint.**
  `accounts.asset` deliberately has no `CHECK` restricting it to the known
  codes; Postgres could express "the asset must be one the system knows" and
  `store/mem` could not, and the conformance subtest
  `ParentReferencesAreNotEnforced` is what holds the line. A `CHECK` would also
  turn a one-line change to a Go slice into a migration. `0001_init.sql` records
  the reasoning in the database with `COMMENT ON COLUMN`, because the absence of
  a constraint is invisible in a schema dump.

### 2. Lending — `done`

Spec: [`superpowers/specs/2026-07-27-lending-design.md`](superpowers/specs/2026-07-27-lending-design.md)
Plan: [`superpowers/plans/2026-07-27-lending.md`](superpowers/plans/2026-07-27-lending.md)

Loan accounts on the Asset side, disbursement, amortization schedules, interest
accrual, repayment allocation (interest before principal), delinquency and
non-performing states.

Three products ship together — term loan, revolving credit line, and the
existing arranged overdraft given a rate — because the third is what stops the
abstraction from being loan-shaped by accident.

**Deferred to their own future work, explicitly out of scope for this
sub-project:** non-accrual and NPL accounting (a non-performing facility today
keeps accruing into income exactly as a performing one does — `NonPerforming`
marks only); expected-credit-loss provisioning and IFRS 9 staging; write-off;
fees; early-repayment penalties; and restructuring, including the
capitalization of unpaid interest on a delinquent term loan (today's
`ChargeInterest` refuses a term loan outright with `ErrWrongFacilityKind`,
precisely because folding unpaid interest into principal on a signed,
scheduled contract is a restructuring decision this sub-project does not make
for you).

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

*After sub-project 2:* lending sharpens the scope rather than changing it.
`interest` — day counts, accrual, the sub-minor-unit `Accrued` type — is
asset-agnostic: it takes a `ledger.Amount` and a `Rate` and knows nothing about
what the amount is denominated in. A crypto-denominated term loan or revolving
line therefore needs no new arithmetic at all, only an asset within the scale
cap of 9 already established above — the same boundary that bounds custody
balances bounds a facility's principal and its accrued-interest receivable too.

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

*After sub-project 2:* lending surfaced an FX exposure that already exists and
is simply unrecorded. A facility's principal and interest accounts are
denominated in one asset (like every account); nothing stops a bank from
disbursing a loan in an asset it does not fund itself in — a euro-book bank
writing a USD-denominated facility, say. That mismatch is a real FX exposure
today, on the `main` branch, whether or not anyone models it, because this
sub-project's per-asset position accounts are exactly the mechanism that would
make it representable: fund the facility through a position account in the
lending asset instead of directly, and the bank's short (or long) position in
that asset becomes a number someone can see rather than an unrecorded fact
about how the loan happened to be booked.

### 5. Account addressing — `done`

Spec: [`superpowers/specs/2026-07-31-account-addressing-design.md`](superpowers/specs/2026-07-31-account-addressing-design.md)
Plan: [`superpowers/plans/2026-07-31-account-addressing.md`](superpowers/plans/2026-07-31-account-addressing.md)

A deposit account gains a set of external identifiers — `(scheme, value)`, with `IBAN` the one scheme shipped —
distinct from its internal `AccountID`; `payment.Scheme` declares which
identifier scheme addresses it, the way it already declares its asset; and
`payment.Network.ResolveIdentifier` turns an address into a `PartyRef`.

Discovered as a prerequisite of sub-project 6 rather than planned: a customer
sends money to an IBAN, and today "IBANs are free-form labels" with nothing to
look one up in. The naive fix — an `iban` column on `deposit.Account` — was
rejected for putting a euro-area retail standard inside the CASA layer; ISO
20022's `CashAccountIdentification` choice is the shape adopted instead, which
is also what makes a later card PAN a data change rather than a rework.

Deliberately out of scope: format validation including the mod-97 check digit
(it would make the seed's readable `SE89-AURORA-1001` illegal and cost more
teaching than it buys), a proxy-alias registry, a second identifier scheme, and
BIC-level addressing.

### 6a. Operator-split API — `done`

Spec: [`superpowers/specs/2026-07-31-operator-split-api-design.md`](superpowers/specs/2026-07-31-operator-split-api-design.md)

Each entity gets its own listener bound to its own identity: one per member bank, one for the central bank, one for the clearing
house. Shared store, split API — every listener still talks to the same `Store`,
because `SettleCycleTx` posts into the central bank's book and every
participant's inside one `Store.Update`, and splitting that is a far larger
sub-project needing a reconciliation-break concept.

58 of the 84 routes carry `/participants/{pid}`; the segment becomes the port
and disappears. It also fixes two defects the single server could not express:
settling moves to the central bank, where it belongs, and a bank's
`GET /payments` stops listing every other bank's.

The deployment unit is the **listener, not the process** — `store/mem` is one
process's memory, so N processes would be N disconnected universes, and
Postgres-optional is load-bearing. One binary runs every listener by default;
`-entity` ran one per process and refused to start without a DSN, saying why.
(7b removed the flag — see below. One binary runs every listener, full stop.)

Out of scope: splitting the store, splitting the call graph (inter-operator
messaging is 7b's), authn/authz, and dynamic port allocation — a bank admitted
at runtime gets no listener until restart, which is a decision about what
admission means rather than a limitation.

### 6b. Role-scoped web UI — `done`

Spec: [`superpowers/specs/2026-07-31-role-scoped-web-ui-design.md`](superpowers/specs/2026-07-31-role-scoped-web-ui-design.md)
Plan: [`docs/superpowers/plans/2026-07-31-role-scoped-web-ui-6b.md`](superpowers/plans/2026-07-31-role-scoped-web-ui-6b.md)

Replaces the single unified dashboard with identities you switch between.
Depends on 5 for the customer's send form and on 6a for everything else: after
the split a persona maps to a *backend*, so **four** personas ship — central
bank, clearing house, bank back office, bank customer — and the scoping is
structural rather than navigational.

Still out of scope and recorded in the spec rather than dropped: real
authn/authz (the port is the claim; nothing verifies the caller), a
card-processor persona, and the customer's mandate and credit screens. The
scheme-operator persona is no longer among them — 6a gave it a backend, so it
ships.

Loose ends the whole-branch review parked, recorded rather than dropped. One
crosses into 7b and is called out there too: **7b deletes the central bank's
`POST /settlements`**, which `/central-bank`'s settle console calls through
`useSettleCycle` → `api.settleCycle` → `cb("/settlements")` — a string, so
neither `tsc` nor `next build` will notice, and the screen 404s at runtime.
Whoever executes 7b Task 12 decides where that action goes. The rest are the
web's own: **the two provisioning predicates are still untested** — `probeSettled`
and `useIsProvisioned` are pure functions of query state, both shipped wrong once
and were fixed mid-plan, and neither is reachable by the node-only vitest until
it is lifted out of `hooks.ts`; the same is true of `/api/operators`' aggregation
and the proxy's `resolve()` and roster cache, where "a failed read must not be
cached" is a correctness claim with no test, against this plan's own rule that
route-handler logic belongs in a plain `.ts` module. **The lobby still reads the
central bank's listener** (`useReserves` in `app/page.tsx`): after the clearing
house's subtraction it is the last cross-persona read in the app, and unlike the
identity picker it carries no comment admitting that it is scaffolding.
**`/clearing-house` and the lobby disagree about the same predicate** — the
participant cards link optimistically while the probe is in flight, where the
lobby holds a skeleton until it settles. And three places still render a failure
as something other than a failure, all in code this sub-project did not own: the
back office's `BalanceCard` (`?? 0` on error), `bank/[pid]/layout.tsx` (the whole
console gated on `useParticipant` with no loading/error split), and the customer
activity screen's *Try again*, which refetches the statement when it was the
account that failed. Cosmetics — a duplicate nav icon, a decorative wordmark at
4.05:1, cmdk's fuzzy scorer over-matching — are in the branch's review history and
deliberately not here.

### 7. ISO 20022 interbank messaging — `wip`

7a and 7b are **done**; 7c is `todo`, which is what keeps this section `wip`
rather than `done`.

Specs: [`7a`](superpowers/specs/2026-07-31-iso20022-messages-design.md) ·
[`7b`](superpowers/specs/2026-08-01-iso20022-mesh-design.md)
Plans: [`7b`](superpowers/plans/2026-08-01-iso20022-mesh.md)

Retired the largest remaining payments fiction — *"No ISO 20022 message
parsing"*, in the README's *Deliberate Simplifications* — by making the message
the interface between banks: participant banks, the clearing house and the
central bank are goroutines exchanging marshalled `pacs.008` / `pacs.003` /
`pacs.002` / `pacs.004` / `pacs.009` over channels, wrapped in a `head.001`
business application header. Nothing but bytes crosses between them, so a
message is parsed on arrival and `FF01` is a reachable failure mode rather than
a decoration.

Three sub-projects, each with its own spec, plan and branch:

- **7a, the `iso20022` package** — `done`. The four messages, the envelope, the
  codec and the external code sets, in a package that imports nothing from this
  repository.
- **7b, the mesh and the actors** — `done`. One goroutine per entity over
  unbounded in-process queues carrying marshalled bytes, `Participant.BIC` and
  routing by `BICFI`, `InitiatePaymentTx` split into a debtor bank's half and a
  creditor bank's half with the other half coming back as a `pacs.002`, and
  `api/` genuinely asynchronous behind the `202 Accepted` plus status query 6a
  already shaped the client around. Settlement stays one atomic `Store.Update`
  at the central bank, now **instructed** by a `pacs.009` the clearing house
  sends when it closes a cycle — the fifth message, and the one 7a did not ship.
  `-entity` is gone, as this section's transport argument below concluded it
  should be.
- **7c, the message log** — `todo`. Envelopes persisted in both stores so a
  payment screen can show the XML that actually moved, plus the README, hint and
  quiz layers.

Depends on 5, which supplies `PartyRef.Identifier` and `Scheme.AddressedBy()`.
Reopens exactly one of 5's deferrals, and only because 5's stated reason for it
was that nothing needed a BIC: `DbtrAgt`/`CdtrAgt` are mandatory in the EPC
`pacs.008`, so a message cannot be written without one.

**7b's transport was settled in its own spec, and this section's earlier
argument for it was wrong.** It reasoned from the transport to the topology —
"channels do not cross processes, therefore one binary" — when 6a had already
settled the topology, which should constrain the transport rather than the
reverse. It also posed a false dilemma: 7b need not "either refuse `-entity` or
carry a second transport", because HTTP works in *both* modes, every listener
being bound on localhost regardless of how many processes hold them.

The decision is nonetheless **channels**, on a scope ground rather than a
technical one: this system is to run in one process, so the capability sockets
would preserve is one nobody will use. **`-entity` is removed** rather than
refused — it exists solely for the multi-process topology, and a listener that
serves its API while no message can reach it would fail far from its cause. The
shared-store reasoning behind 6a's DSN refusal survives the flag, as prose in
`README.md`. All sending goes through a single function rather than a
`Transport` interface, so a later move to sockets stays contained without paying
for an abstraction with one implementor.

Deliberately out of scope across all three: `pain.001`/`pain.008` customer
initiation, the `camt` reporting family, `camt.056`/`pacs.007` recalls and
reversals, runtime XSD validation, and message signing. 7b's spec carries the
full list, grouped by where the work would land — including the things the mesh
makes reachable for the first time: batched bulks (which is what would exercise
`pacs.002`'s `GrpSts: PART`, built in 7a and unused in 7b), instant payments
over the existing `Gross` settlement model, a `pacs.028` status request, a
cutoff timer, and liquidity management.

### 8. Per-entity stores — `todo`

Each entity — every participant bank, the central bank, the clearing house —
gets its **own `Store`**, so that no code path can reach another entity's books
except by sending it a message.

One binary, N listeners, N stores. Separate *processes* were never what made
this hard — 6a settled the deployment unit as the listener — and separate
**stores** are the whole cost, as well as the whole lesson.

6a is the reason this is now tractable rather than speculative. It already gives
each entity its own surface and its own identity; what it deliberately did not
split is the one thing left, and it said so: *"out of scope: splitting the
store"*.

**What it costs.** `SettleCycleTx` today "moves the netted reserves, mirrors
them in each bank's own books and pays out every creditor inside one
`Store.Update`" (`payment/doc.go`, *Deliberate simplifications*). With per-entity
stores there is no such transaction. `Network.bind` and the live
`Ledger`/`Deposit`/`Lending`/`Catalogue` handles on `Participant` become
impossible to construct, which is the point rather than a casualty.

**What it teaches, and what the current model gets wrong.** Real settlement *is*
atomic — in the central bank's own ledger, because the settlement agent holds
every reserve account there. What is emphatically *not* atomic is each
commercial bank mirroring that movement into its own books: the bank learns of
it afterwards, from a `camt.054`, and if its mirroring fails the settlement is
still **final**. The bank has a reconciliation break, not a rollback. The
present shared transaction teaches the opposite, and this is the one place the
model is misleading rather than merely simplified.

So the sub-project's real deliverable is a concept the repository does not have:
an unreconciled position, and what a bank does with one.

**Dependencies.** 6a, for the per-entity API. 7b, for seams that are already
message-shaped — attempted before 7b this is a redesign, attempted after it a
mechanical extraction. 7b makes that claim checkable rather than hopeful: it
ships narrowed per-actor interfaces and a book-access recorder whose test
assertions *are* this sub-project's specification of the split.

**The end state is separate databases**, not merely separate `Store` values —
one DSN per entity. Postgres-optional survives it (each entity's store may still
be `store/mem`), and so does `store/storetest`, which is already per-store.

**Survives unchanged:** Postgres-optional (each store may still be `store/mem`,
so `make dev` and `go test ./...` need no database), and `store/storetest`,
which is already per-store and conforms each one independently.

**Out of scope:** separate processes, two-phase commit, and any distributed
transaction manager. If settlement cannot be made final in one book and mirrored
asynchronously into the others, the design is wrong rather than under-powered.

## Log

| Date | Entry |
| --- | --- |
| 2026-07-27 | Roadmap created. Ordering agreed: multi-asset → lending → crypto → FX. Framing agreed: teaching reference, production-grade backend. |
| 2026-07-27 | Sub-project 1 design agreed and spec written. |
| 2026-07-27 | Sub-project 1 **done**. Assets, per-asset balancing, scheme assets, per-asset participant accounts, migrations `0002`–`0006`, API and web carried through, and the docs updated across all four layers (README, hints, quiz chapter 16, schema comments). Every decision in the spec shipped as designed. Learned: the payment-layer asset check is justified more strongly than the spec argued (a payment is two postings in two books, each valid on its own); the asset check being a domain rule is what forbids a constraint on `accounts.asset`; and the scale cap of 9 is a hard scoping constraint on sub-project 3, while the "can a position account go negative?" question is a hard one on sub-project 4. FX position accounts still look like the right shape. |
| 2026-07-27 | Sub-project 2 design agreed and spec written. Scope widened to three products (term loan, revolving line, arranged overdraft); impairment deferred. Settled: an overdrawn account gets no loan account, because its drawn amount is the negative balance viewed by sign and has no independent existence — the Asset-side figure is a derived aggregate, as "total customer deposits" already is. Rejected a reclassification journal and an EOD sweep (the latter models a linked-loan product, not an arranged overdraft) and rejected restructuring deposits into control accounts as a regression. Packaging follows: pure `interest`, overdraft terms in `deposit` (CASA, not Loans — and a single `lending` owning the limit would cycle through `deposit`), `lending` for the two real-drawdown products. Interest is held at sub-minor-unit precision and posted daily as the delta of the rounded value; repayment allocates against actual accrued interest, not the schedule, which is why 30/360 exists. |
| 2026-07-27 | Sub-project 1 **reworked**, in two parts. (1) The book-scoped `assets` table is gone; the definitions are a package-level list in `ledger`, the way schemes are Go types. The table was writable but unhonourable — a bank's plumbing accounts are provisioned when it joins, so an asset registered later gave a customer account that could never settle, surfacing as a 404 on funding. `GET /assets` replaces the per-participant registry endpoints; migrations `0002`–`0006` were folded into `0001` (no deployed databases; `migrate.go`'s own doc comment sanctions it). (2) The whole-branch review's findings: the euro-to-bitcoin counter-example was **wrong in five places** — the ledger does catch it, at settlement, and what it cannot catch is the payment at *initiation*; that correction is now pinned by a test rather than by argument. Also: N+1 round trips in two listing endpoints, an asset on both balance responses, mismatched-asset mandates refused, `ErrParticipantAssetNotFound` → 422, and the web form that silently reinterpreted an overdraft limit at a new scale. Learned: this is the **second** counter-example on this branch that argued the opposite of what it claimed. Prose that asserts what code does needs a test, not a careful reading. |
| 2026-07-28 | Sub-project 2 **done**. `interest` (day counts, daily accrual, the sub-minor-unit `Accrued` type), overdraft terms and daily accrual on `deposit.Account`, and a new `lending` package for the term loan and revolving line — each a facility with two Asset GL accounts (principal, accrued-interest receivable), an amortization schedule (annuity or equal principal), interest accrual and capitalization, repayment allocated interest-before-principal against what actually accrued, and arrears computed from the schedule with a `NonPerforming` marker at 90+ days. Both `payment.Participant.RunEndOfDay` batches (deposit and lending) run in one unit of work; API, web, seed data and docs (README, hints, quiz chapters 17–18, schema comments) carried through as designed. Every decision in the spec shipped as designed; deferred work (non-accrual/NPL accounting, ECL provisioning, write-off, fees, early-repayment penalties, restructuring) is recorded above rather than silently dropped. Learned: four arithmetic worked examples in the plan and spec were wrong (a 30-day €10,000-at-6% accrual is €49.32 not €49.31; a revolving line's capitalization residue is *positive* — interest rounds down, not up — so it is +452,040 micro-minor-units, not −547,960; a 64-day accrual totals 10,521, not 10,520; and the `interest` package's overflow-example constant has a transposed digit) — all four caught only once tests were written against the actual arithmetic, not by re-reading the worked examples. And the load-bearing point of the whole sub-project — that an overdrawn account gets no loan account because its drawn amount is a sign-flipped view of an existing balance, not a second fact — is exactly the kind of claim that needs a pinned test (`deposit.TestTotals_OverdraftsAreDerivedAndNothingIsPosted`) rather than a comment, for the same reason sub-project 1's counter-example needed one. |
| 2026-07-31 | Sub-projects **5 (account addressing)** and **6 (role-scoped web UI)** designed and specs written, in that build order. 6 was the request — make the web app realistic by scoping it to roles rather than showing every screen to everyone — and 5 fell out of it: a customer sends money to an IBAN, and accounts have none. Settled for 5: identifiers are a plural `(scheme, value)` set on the account, not an `iban` field, because a field names SEPA inside the CASA layer and cannot express a card PAN arriving later (ISO 20022 `CashAccountIdentification` is the same choice); the scheme declares what addresses it and `InitiatePaymentTx` enforces it, in the same place and for the same reason as the cross-asset check; uniqueness stops at the bank, which is the widest scope a register can see and also the correct line — a bank-issued identifier is globally unique by construction, a proxy alias is not, which is exactly why the alias registry is deferred rather than merely omitted; that rule is a *domain* rule and therefore gets no `UNIQUE` in `store/pg`, because the exemption the schema grants for `UNIQUE (book_id, name)` ("the race is already closed, one layer up") does not apply — nothing serializes two concurrent adds, so a constraint would fire in Postgres and not in memory, and `IdentifierUniquenessIsNotEnforced` pins that the way `ParentReferencesAreNotEnforced` already does; and an ambiguous resolution is an error, not the first hit, following the settlement rule about not defaulting quietly — which is also what closes the residual within-bank race, at read time. No mod-97 validation: it would make the seed's readable IBANs illegal. Settled for 6: three personas on persona-prefixed routes with three genuinely different shells (the customer loses the sidebar entirely), one flat identity picker because a persona without its context addresses nothing, a lobby that is always the root, and the concepts panel kept in all three shells — a little realism traded for the thing the repository exists for. No authn/authz: the scoping is navigational, and every endpoint stays reachable by URL. |
| 2026-07-31 | Sub-project 5 **done**. The `(scheme, value)` identifier set on `deposit.Account`, per-bank uniqueness in `deposit.Register` with no `UNIQUE` constraint, `payment.Network.ResolveIdentifier` refusing ambiguity rather than the first hit, `Scheme.AddressedBy()` enforced in `InitiatePaymentTx` beside the cross-asset check, `PartyRef.IBAN` → `PartyRef.Identifier` (stored, not derived), `GET /directory`, and no format validation all shipped as designed — `SE89-AURORA-1001` is still legal. Five things the plan or spec did not survive contact with: (1) a `UNIQUE (participant_id, scheme, value)` constraint was in the spec's working draft and was dropped during spec review, before the spec was ever committed — the `UNIQUE (book_id, name)` exemption ("the race is already closed, one layer up") does not apply, because nothing serializes two concurrent `AddIdentifier` calls, and `storetest.IdentifierUniquenessIsNotEnforced` now pins that the way `ParentReferencesAreNotEnforced` already does; the parent `FOREIGN KEY` does stay, under the exemption for child tables the store writes both sides of. (2) The store method changed shape: the spec's `FindDepositAccountByIdentifier(...) (Account, error)` shipped as `ListDepositAccountsByIdentifier(...) ([]Account, error)`, because "an address resolves to exactly one account" is a domain rule and the store holds none — `Register.ResolveIdentifier` turns 0/1/many into not-found/the account/ambiguous, and the network's sweep applies the identical rule across banks. (3) `PartyRef` turned out to be stored on **both** payments and mandates, which the plan did not anticipate — the rename forced a schema change to both tables, one `iban` column each becoming `debtor_identifier_scheme`/`_value` and `creditor_identifier_scheme`/`_value`, and the compiler found it, not the plan. (4) The task ordering left the tree red for one task: enforcement (Task 7) landed before the request DTO could express an identifier (Task 8), so nine `api` tests failed with 500 in between — a sequencing lesson for future plans, that enforcement and the means of satisfying it belong in the same task. (5) Two review-caught defects of a kind this roadmap already records: `storetest`'s `mandate()` fixture carried no identifier, so the conformance suite only ever round-tripped `mandates.debtor_identifier_scheme`/`_value` and `creditor_identifier_scheme`/`_value` as empty strings — exactly the case a scheme/value transposition would not surface — fixed with real, distinct identifiers on both sides plus an assertion-bearing subtest; and the new README/hint prose asserted that `Network.ResolveIdentifier`'s refusal "lives in `deposit.Register`", when the thing actually in `Register` is the different, write-time `checkIdentifierFreeTx` — the **third** time on this project that confidently-worded prose asserted something the code did not do, after the euro-to-bitcoin counter-example and the lending worked examples. Prose that asserts what code does needs a test, not a careful reading, keeps earning its line. Also found, unrelated to this sub-project: `payment.Participant.ProductID`, added six commits earlier on `main`, had never been wired into `store/pg` — `PutParticipant` never wrote it, `participantColumns` never read it — so `store/mem` was the only store that remembered it; it surfaced as `"productId is required"` from the API and `"product not found"` from the seed, nowhere near the store that lost it, while `./product` and the conformance suite stayed green because `ParticipantRoundTripsAndDropsLiveHandles` asserted every persisted field except that one (it does now). Fixed on `fix/pg-participant-product-id` (`eb0c7e4`), merged into this branch (`5a04ca4`), and on `main` since. `go test ./...` green with no `DATABASE_URL` and against Postgres; `web`'s `npm run test`, `typecheck` and `lint` all green. |
| 2026-07-31 | Sub-project 5 **reviewed**, whole-branch, before merge. Six findings, all in the seam the branch opened between an account's id and its address. (1) The one that mattered: `SDD.Validate` compared a payment to its mandate with whole-struct `PartyRef` equality, and `PartyRef` had just gained the quoted identifier — so a remove-then-add reissue, the operation `deposit/register.go` advertises as moving neither balance nor history, left **no** quoting that worked and no `UpdateMandate` to repair it. `PartyRef.SameParty` now compares `(participant, account)` only, because a mandate authorises debits from an *account* and the address is a record of one payment's route. The lesson, and it is a new one: **when the compiler finds an unanticipated consumer of a changed type, re-run the design's failure analysis against it.** The compiler *did* find `PartyRef` on mandates — the previous log row records it as deviation (3) — but only the schema consequence was chased (two `iban` columns became four), never the behavioural one, and the spec's *Failure modes* section never asked what a mutable identifier does to a mandate. It asks now. (2) The address was optional on the way in and stayed empty on the way to storage, so "a payment records the address it was sent to" held only for callers who volunteered one — and no `api` test ever did; only `seed` did. Initiation now back-fills the account's single identifier in the scheme's scheme, and refuses (`ErrAmbiguousAddress`, 422) rather than choosing between several. Back-filling is what makes `SameParty` strictly necessary rather than merely correct. (3) `store/mem` handed out and stored `Account` verbatim, so its `Identifiers` slice was shared with the caller both ways, breaking `mem.go`'s own "Put deep-copies in, Get deep-copies out" invariant that `clone()`'s one-level snapshot rests on. (4) And the same write read back differently on the two stores — `[X, X]` kept two in memory and collapsed to one in Postgres, `[ZZ, AA]` kept insertion order in memory and came back sorted from Postgres, `[]` was non-nil in one and nil in the other — every case invisible because **every identifier fixture in the conformance suite held exactly one identifier**, which round-trips through any ordering or dedupe rule at all. A one-element fixture is not a fixture for a collection. (5) `checkAddressable` never checked that the quoted address was in the *scheme's* scheme, only that the account held it somewhere: unreachable while `IdentifierIBAN` is the only member, and therefore exactly the check that would have been silently wrong on the first day a card PAN arrived — which the design names as its load-bearing extensibility claim. (6) Three documented claims had no test: the within-bank half of the ambiguity refusal at the network layer, the 409/422 status codes for the addressing errors, and the debtor leg of any addressability check at all (every existing test used the creditor leg). Also narrowed, in README, hint and schema comment: the read-time ambiguity check is a claim about **routing** — `InitiatePaymentTx` takes an account id and never resolves, so two accounts colliding on one address both stay payable by id, which is right rather than a gap. And an unflagged deviation from the previous row, recorded here with the others: `GET /participants/{pid}/deposit-accounts/{did}/identifiers` was specced and never built — identifiers ride the account DTO instead, which is the better call for a set that is part of the aggregate, but it belonged in the deviation list. |
| 2026-07-31 | Sub-project 6 **split into 6a and 6b**, and its spec re-opened rather than amended. The request: give each entity its own port, so personas map to backends instead of route prefixes. Settled that the split lands as its **own sub-project built first**, not folded into 6 — the "otherwise you write it twice" argument is cured by *ordering*, not by merging, and the two halves fail differently enough (a correctness change checked by Go tests; a navigation change checked by `tsc` and clicking) that one branch would put a failing conformance test and a dead React link in one review. The load-bearing decision is that **the deployment unit is the listener, not the process**: `store/mem` is one process's memory, so four bank processes would be four disconnected universes and a payment between two banks would post into systems that cannot see each other; since Postgres-optional is load-bearing (`CLAUDE.md`), the default is one binary holding every listener over one `*payment.Network`, and `-entity` (one per process, the real topology) *refuses to start without a DSN* with an error explaining why — a refusal worth more as teaching than the split itself. Rejected making `store/mem` shareable across processes: that is a database, and it would make the reference implementation the more complicated of the two stores. Also settled: the API surface splits but **the call graph does not** — every listener keeps a full `Network` over the shared store, the clearing house does not HTTP-call the central bank to settle, it simply has no settle route; inter-operator messaging is 7b's and a lo-fi version now is the same build-it-twice trap. Two defects the single server could not express are fixed as a consequence rather than as an errand: `POST /cycles/{cid}/settle` becomes `POST /settlements` on the **central bank** (settlement moves reserves in the central bank's own book; a clearing house that could do that would be a central bank), and a bank's `GET /payments` narrows to its own legs, answering **404 rather than 403** for a payment it is not party to, because a 403 confirms the id. Static ports, stated as a decision: a bank admitted at runtime via `POST /members` gets reserve accounts and no listener until restart, because admitting a member to a network is an operational act and modelling it as an API call that instantly yields a running bank teaches the wrong thing; 6b renders such a bank as *awaiting provisioning*. Seeding and `/admin/reset` go to the central bank, since `Server.Reset`'s mutex is per-process and its own doc comment already admits two servers sharing a database can race — more than one owner would make that routine. The roster splits along the seam it was hiding: `POST /members` is the central bank's (admission opens central-bank accounts), `GET /members` the clearing house's (routing needs to know who is reachable). `/assets` is served by every listener, because duplicating a compiled-in constant is not duplicating state, and `GET /directory` by the bank as well as the CSM, because a customer's browser must not hold a clearing-house connection. For 6b the consequence is a **fourth persona**: the scheme-operator deferral is superseded, since leaving the CSM's screens on the central bank's console would put a visible backend boundary underneath a fiction — the central bank stops seeing every individual payment, and the README's clearing-versus-settlement distinction becomes visible in the UI for the first time. The customer's send form is reshaped around `202 Accepted` + `{paymentId}` + a status read, not because today's handler is asynchronous but because 7b converts it; shaping the client that way now costs one request and saves a rewrite. Three counts in the original spec were wrong and are corrected: the API is 84 routes, not ~60, of which **58** carry `/participants/{pid}`; the route move touches ~25 link sites *plus* ~35 quiz `explore.href` values governed by an `EXPLORE_ROUTES` allowlist that nothing checked against real pages; and `[[available-balance]]` is not a hint key (`balance-available` is) — a dangling wiki-link takes every dev route down. Flagged for 7: 7b already specifies one **goroutine** per entity over channels, which after 6a wants to be an HTTP POST to a counterparty's address — a better 7b, and a real revision to a spec on another branch. The plan at `c91c092` is partly invalidated and will be re-planned after 6a rather than patched. |
| 2026-07-31 | Sub-project 6a **done**. Three operator surfaces over one shared `Network`, `cmd/server` starting one `http.Server` per entity (`:8081` central bank, `:8082` clearing house, `:8083`+ one per member bank in registration order), settle moved to the central bank as `POST /settlements`, a bank's `GET /payments` narrowed to its own legs, a bank's `POST /payments` answering `202` + `{paymentId}`, `-entity` with the DSN refusal, and the web data layer re-pointed. Both stores green throughout; `store/storetest` untouched, as designed. The mechanical core was smaller than expected and for the reason the plan predicted: only **two** places in `api/` read `{pid}` off the path, so binding a listener to a participant on a shallow `Server` copy left all 58 bank handler bodies untouched — the split is one helper plus a re-registration. Seven deviations, five of them worth carrying forward. (1) **Task 2 could not be additive as planned.** Stripping `/participants/{pid}` in the four shared register functions breaks the combined `Routes()`, so "add the surfaces alongside the old mux" is not a state the code can be in. A transitional `router.strip` view kept that commit green and was deleted in the next one; the alternative was a single enormous commit carrying the mux split *and* the test re-point. The lesson is narrower than "plans are wrong": **a plan that asserts a commit is additive should name the thing that would make it not so**, and this one did not check whether the registrations were shared. (2) **`allowedOverlaps` reached six, not the four the plan predicted.** `GET /audit` was the unforeseen one — a bank's ledger log and the central bank's own log are the same pattern on different operators — and it is a *good* consistency rather than a collision: `/audit` means "this operator's own log" everywhere, and the allowlist records that reasoning rather than tolerating it. (3) **The `api/server_test.go` re-point was scripted**, 353 call sites across 2,596 lines, deriving each call's operator from its path. It produced exactly **one** genuine failure — `TestAuditRoutesAreScoped`, where re-keying a map by name left a later loop still comparing against the old path — plus two tables the script correctly skipped and a hand-written struct literal it could not parse. A test catching the refactor's own bug is the system working; worth recording that the scripted 99% was safe and the 1% it declined to touch was exactly the part that needed judgement. (4) **The `-entity` wiring shipped as a no-op.** A scripted `.replace()` matched nothing — the variable had been renamed in an earlier task — so the flag parsed, compiled, and started all six listeners anyway; only a live probe caught it. Fixed twice over: the narrowing became `resolveEntities`, a function a test can hold, and **a scripted edit that matches nothing must assert rather than pass silently**. That `if` in `main` was untestable by construction, which is why it could rot. (5) **The web data layer moved in 6a rather than 6b**, as the plan flagged: leaving the frontend unable to reach any endpoint for a whole sub-project would mean 6a did not end in working software. (6) The proxy **derives a bank's port from the clearing house's roster** rather than requiring a configured table, mirroring `plan()`, so `make dev` stays a zero-configuration single command; `BACKENDS` overrides it. The spec had called for configuration only, and the derived default is the better trade for a repository whose point is that `make dev` just works. (7) `GET /me` reuses `handleGetParticipant` unchanged rather than gaining a sibling — it already resolved through `s.participant`, which on a bank's listener reads the bound identity. Also worth noting for 6b: the seed's participant ids are `bank_1`, `bank_3`, `bank_5`, `bank_7` — not contiguous — so nothing may infer a port from an id, only from roster position. |
| 2026-08-01 | Sub-project **7a done** — the `iso20022` package, on `main` before 7b's spec was written. Four interbank messages, each answering for itself which definition it follows (`MessageDefinitionIdentifier`): `pacs.008.001.08` (credit transfer), `pacs.003.001.08` (collection), `pacs.002.001.10` (status report) and `pacs.004.001.09` (return); the `head.001.001.02` business application header; the codec — `Marshal`/`Unmarshal` over a `Document` registry keyed by definition identifier alone, the namespace being a value that registration asserts against the type's own and dispatch cross-checks against the root element's, validating the whole tree before it emits, because `encoding/xml` cannot express `xsd:choice` and an invalid choice must be a Go error rather than a document a counterparty rejects; the external code sets (`SettlementMethod`, `ChargeBearer`, `ServiceLevel`, `LocalInstrument`, `SequenceType`, `GroupStatus`, `TransactionStatus`, `StatusReason`, `ReturnReason`); and the primitives — `BIC`, `IBAN` (compacted through its display separators, deliberately **not** mod-97 checksummed, so the seed's readable `SE89-AURORA-1001` stays legal), `ActiveCurrencyAndAmount` (which refuses to round: `Minor` returns an error rather than losing a digit) and `ISODate`/`ISODateTime`. The outer `<Envelope>` is this repository's own stand-in for a clearing-house wrapper and is named as such: what is standard is the header and the document, not their packaging. **The package imports nothing from this repository** — a `ledger.Amount` in here would quietly falsify "these are the standard's types" — and that rule is a test (`imports_test.go`) rather than a line in a plan's verification block. It therefore shipped with **no translator at all**: `payment/translate.go` is 7b's, because the conversion boundary belongs on the payment side. Four things went differently, all of them this roadmap's standing lesson again. (1) The package doc asserted the boundary "lives in `payment/translate.go`" while that file did not exist — the **fifth** time this project shipped prose asserting what the code does. (2) Two of the three EPC Implementation Guidelines claims that now carry an IG index — the charge bearer, the settlement method, remittance information — were **false** until a review fetched the guidelines; the IBAN-only and euro-only claims carry no citation, and the doc says so rather than claiming every claim is cited. (3) Two guards shipped in a state where they could not fail, and were given a way to. (4) The receive path refused a BOM, invented a `CreDt` and leaked `io.EOF`. **Known gap, then and still:** the golden files round-trip under `go test`, but the schema check runs `xmllint` against the real XSDs only if `iso20022/testdata/xsd/` is populated, and it is not in this tree — every one of those subtests **skips**, which is not coverage; `ISO20022_REQUIRE_SCHEMAS=1` (`make test-schemas`) is what turns the skip into a failure, and fetching the XSDs is what would make it pass. Handed to 7b: the transport question, and four open decisions, of which 7b's design closed two (`MndtRltdInf.DtOfSgntr` maps `Mandate.CreatedAt`; `Payment` gains a `RejectCode`) and left `OrgnlTxRef` unmodelled on purpose. |
| 2026-08-01 | Sub-project **7b design agreed and spec written**, three of its five load-bearing decisions against what this roadmap said. (1) **Transport.** The section's own argument was backwards — it reasoned from the transport to the topology ("channels do not cross processes, therefore one binary") when 6a had already settled the topology, and it posed a false dilemma in "either refuse `-entity` or carry a second transport", since HTTP works in *both* modes: every listener is bound on localhost whether one process holds them or five. Channels win anyway, on the scope ground that this system is to run in one process, so the capability sockets preserve is one nobody will use. `-entity` is therefore **removed** rather than refused, and all sending goes through a single function rather than a `Transport` interface, so a later move to sockets is contained without paying for a one-implementor abstraction. (2) **The central bank gains a fifth message.** The roadmap made it an actor; 7a shipped four messages and none of them is a settlement instruction, so it had nothing to send or receive. 7b adds `pacs.009.001.08` and reuses `pacs.002` for the answer — one new type buying a full request/response, and leaving `camt.054` to sub-project 8 where it teaches the unreconciled position. **Closing a cycle emits the instruction**, so the central bank's `POST /settlements` is deleted; this reads as a reversal of 6a and is that principle applied more exactly — the CSM instructs, the central bank decides and posts, and can answer `RJCT`/`AM04` when a net payer cannot cover, which is better than today's Go error to whoever clicked settle. (3) **"The creditor-account check moves out of `InitiatePaymentTx`" is half the story**, true for SCT and backwards for SDD. The rule that covers both: `InitiatePaymentTx` splits into a **debtor bank's half** and a **creditor bank's half**, and `Scheme.Direction()` decides which runs at submission — so a pull posts *nothing* at submission and the debtor's bank posts the debtor leg when it accepts the collection. This exposed a live defect: `api/handlers_bank_payment.go:86` requires the debtor to be at the submitting bank on *every* submission, which is the wrong bank for a direct debit, invisible until now because one process validated both ends regardless of who called. `SDD.Validate` splits along the same seam and along who holds what in SEPA — the creditor's bank validates the mandate synchronously, the debtor's bank validates funds asynchronously. Also settled: hub-and-spoke via the clearing house (a bank never addresses another bank); `Drain`, a quiescence barrier that returns dead letters joined, so every draining test asserts nothing was silently eaten, and which the seed and `/admin/reset` use too; unbounded queues rather than buffered channels, because a fixed buffer between two actors that message each other is a deadlock and here they do; `Initiated` becoming an observable state for the first time, with the existing transition table already permitting exactly what the mesh needs; and `ErrCycleNotOpen` becoming an `RJCT`/`TM01` from the CSM rather than a 422 at submission, which is what `InvalidCutOffTime` means. Two of 7a's four open decisions are closed — `MndtRltdInf.DtOfSgntr` maps `Mandate.CreatedAt` rather than adding a field with no independent source, and `Payment.RejectReason` gains a `RejectCode`, without which the code is computed and immediately thrown away; `OrgnlTxRef` **stays** unmodelled, because in hub-and-spoke the CSM holds the payment row and correlates on `OrgnlEndToEndId`, so no receiver reconstructs anything. Sub-project 8 stays **after** 7b for three reasons — the tables cannot be partitioned until the choreography says who holds what, 8's actual deliverable is a `camt.054` and so needs a message layer, and splitting first leaves the tree red across a whole sub-project because `SettleCycleTx` cannot compile against N stores. But the honest weakness of that ordering is that with a shared store a cross-entity read compiles and every test passes, so 7b adds two mechanisms that make the boundary enforcement rather than convention: **narrowed per-actor interfaces** (`bankOps`, `csmOps`, `settlementOps`, all satisfied by `*payment.Network` today, so a bank handler calling `SettleCycleTx` does not compile) and a **book-access recorder** — every `ledger.Tx` method takes `book BookID` as its first argument, so a decorator over `payment.Tx` records what each handler touched with no plumbing, and those assertions *are* sub-project 8's specification written a sub-project early. |
| 2026-08-01 | Sub-project 6b **done**. Identity is derived from the pathname and persisted nowhere; `identity.ts`'s `homeFor`, `navFor` and `backendFor` sit on one switch over `Identity`, so a persona can never name a backend it does not belong to, and a customer resolves to their *bank's* listener rather than one of their own. The plan called for four persona shells plus a plain one; what shipped is **three** shell components covering five arrangements. The central bank, the clearing house and a bank's back office differ only by which `Identity` they are handed — `PersonaShell` already held it — so they share one `ConsoleShell({ identity, children })` rather than being three copies of the same wiring; the customer keeps its own `CustomerShell` because the arrangement genuinely differs (no left panel, a tab strip, a narrower column); and `plain-shell` is the lobby's and Learn's, as designed. The split had left every persona console with no in-app way back to `/`; the rule that shipped puts the brand wordmark in the topbar and hands it to whichever shell has no sidebar to carry it (`ShellFrame`'s `DesktopShell` clones the topbar with `showBrand={!sidebar}`), so Learn stays reachable from the lobby rather than being bolted onto a persona's nav. One flat picker of complete identities, because a persona without its context addresses nothing; the lobby stays at the root always, trading a repeat visitor's extra click for a newcomer's orientation; and the concepts rail survives into the customer shell against retail realism, because teaching outranks the fiction. Two collisions with 6a's surface, both settled as the plan intended: the central bank gained four read routes — `GET /cycles`, `GET /cycles/{cid}`, `GET /settlements`, `GET /settlements/{sid}` — because an operator that settles must be able to read the instruction it is settling; and the awaiting-provisioning badge is served by a Next-side `/api/operators` probe (`src/lib/api/backend-url.ts`, shared with the proxy) because 6a's derived port table left nothing a client could read directly. Three findings, numbers checked against the tree: the route move touched 27 link literals in components and pages plus 33 of the quiz's 40 `explore.href` values across 8 chapter files — the rewrite script's own rule counts sum to exactly that split; `explore.href` was typed `string` and is now `(typeof EXPLORE_ROUTES)[number]`, with the new `nav-integrity.test.ts` holding the chapters under the compiler and the allowlist against the route tree; and `StatementTable` needed a `retail` variant, because reusing it whole would have put the bank's chart of accounts and a GL drill-down link inside the customer's own statement. Two things the plan did not foresee, both the same shape as 6a's own lessons. `nav-integrity.test.ts` shipped with a self-liquidating `PENDING` set: Task 4 built the test asserting routes Tasks 9 and 12 had not built yet, a contradiction the plan created for itself; the fix was an exemption list plus a fourth test asserting every exemption is *still* unbuilt, so a task that lands its route without deleting its line goes red instead of the carve-out rotting permanent — Tasks 9 and 12 each deleted their line, and the set is empty now. And the lobby's provisioning gate needed a second pass: gating `useIdentityDirectory`'s per-bank queries on `useIsProvisioned` alone still fired requests against an un-provisioned bank before `/api/operators` had answered, and the cached 502 (`retry: false`) blanked the whole lobby; the gate now waits for the probe to have *settled* — answered or failed — falling back to the same optimism `useIsProvisioned` already applies on failure, and scopes the page's error state to the roster query rather than the page. Two screens shipped conflating "still fetching" with "genuinely zero" and were caught and corrected: the bank home's reserve and deposit cards, and the customer's balance — "a figure that is not known yet is not zero" is a lesson this sub-project paid for twice, though the send screen was written to it from the start. And, recorded either way as asked: the scripted rewrite's asserted counts — all 13 rules — matched their expected match counts on the first run, no rule needing correction. That is the direct test of 6a's first lesson (a scripted edit that matches nothing must assert, not pass silently), and a plan that predicted its own tree correctly is worth recording as such. |
| 2026-08-01 | Sub-project **7b done** (`7a`, the `iso20022` package, was already on `main`; 7c is still `todo`, which is why section 7 reads `wip`). The message is the interface: `mesh` owns N+2 actors — one goroutine per member bank, one clearing house, one central bank — each with an unbounded inbox, and nothing but **marshalled bytes** crosses between them, so a message is parsed on arrival and `FF01` is a reachable failure mode rather than a decoration. Four flows ship — the credit transfer, the direct debit, the cut-off and the return — and a rejection is a branch of the first two rather than a fifth. `-entity` is gone; `POST /settlements` is gone; `api` answers `202` with a `paymentId` and means it. **Eight deviations from this plan, all of them recorded rather than folded in silently.** (1) **`InitiatePaymentTx` split three ways, not two.** The spec said the creditor-account check "moves out of `InitiatePaymentTx`", which is true for a push and backwards for a pull. What shipped is `SubmitPaymentTx` (the submitting bank's half — the debtor half on a push, the creditor half on a pull, posting *nothing* on a pull), `AcceptInboundTx` (the receiving bank's half, which is the one that moves money on a pull) and `AcceptAtCSMTx` (the clearing house's, and the only one that makes a payment `Accepted`). The rule that covers both directions, and that neither flow on its own would tell you, is that **the debtor's bank posts the debtor leg**. (2) **Signatures.** `CreditTransferMessage` and `DirectDebitMessage` gained a `ctx` the plan's signature did not have — they read the store, and `context.Background()` inside a store read is the thing to avoid; `ReturnMessage` shipped as a `*Network` method although it reads nothing, because the amount's scale comes from the scheme's asset and only the `Network` holds the scheme registry. (3) **A rejection is two halves and can half-happen.** `RejectPayment`/`RejectPaymentTx` are deleted; `RejectAtCSMTx` marks the payment and `ReverseDebtorLegTx` gives the payer their money back, in two units of work by two institutions. Between them the payment is `Rejected` with the customer's money still in clearing suspense — the first operation in this repository that can be caught in the middle. A reversal that fails has nobody to answer, so it becomes a mesh dead letter rather than a quiet lie. (4) **A rejected pull can send TWO `pacs.002`.** "One decision, one message" is no longer a property of this system, because the bank waiting for the answer (the payee's) and the bank holding the money (the payer's) are different institutions; dropping the second recipient strands the payer's money against a payment nothing will reverse. The condition is exactly the money — `csm.tell` sends the second only when the payer's bank has already posted the debtor leg, so a collection that bank refused *itself* for want of funds (`AM04`, before any posting) is one message again. (5) **Two book invariants the plan wrote before the code were both wrong, in the same direction, and were corrected by measurement rather than by argument.** `TestABankHandlerTouchesOnlyItsOwnBook` could never have passed: a submitting bank reads the *counterparty's* deposit register, because a `pacs.008` names the payee and `payment.partyTx` fetches that name — so **"no bank reaches another bank's book" is false today**, and closing it is a domain change (the request would have to carry the counterparty's name), not a transport one. `TestTheCentralBankTouchesOnlyTheCentralBankBook` could never have passed either: settlement's mirror and creditor legs post into the members' books. Both tests are renamed to what they measure. A third drafted name, `TestSeedDrainsBeforeListening`, rested on a false premise and was corrected before it shipped. (6) **`ErrCycleNotOpen` moved and changed kind.** It is the clearing house's refusal now, not a `422` at submission, and on the wire it is a `pacs.002` carrying **TM01** — the cut-off is the clearing house's, and no bank refuses its own customer's instruction on account of it. (7) **Settlement is instructed.** Closing a cycle emits a `pacs.009`; the central bank decides and can answer **RJCT/AM04**, leaving the cycle `Closed` with no settlement against it and every payment exactly where the cut-off left it. A refusal is fanned out to nobody, because nothing was posted and there is nothing truthful to tell a bank. (8) **Seed first, then start.** `mesh.Start` reads the participant roster exactly once and `seed.Populate` calls `AddParticipant` directly, so starting the mesh before the seed leaves every seeded bank with no actor. **What was learned.** The signature failure of this branch, again and at scale: *prose that asserts what code does needs a test, not a careful reading*. One test on Task 11 took four rounds, and each of the first three fixed the previous round's overclaim; three separate comments were found claiming a property their own test could not fail on; a book-access recorder built to enforce the boundary had three holes, **two of them introduced by the fix to the previous hole**. Two mechanisms are what caught most of it: the **narrowed per-actor interfaces** (`bankOps`, `csmOps`, `settlementOps`) — which narrow by *method* and therefore do **not** close `GetParticipant`, which hands back live book-bound handles, so no "the compiler prevents this" claim about them is safe — and the **book-access recorder**, whose assertions are sub-project 8's specification written a sub-project early and which found the counterparty-register crossing nobody predicted. Also worth carrying: `Drain` before `Stop` (Stop cuts chains, so an undrained shutdown ends payments halfway); the fuzz target found two real translator bugs and a hand-written probe a third the fuzzer could not reach (`ledger` holds BTC at scale 8, which `ActiveCurrencyAndAmount` cannot express); and an `AM04` on a collection needed a **second** reason table, `borrowedReasons`, because `deposit.ErrInsufficientAvailable` is not a `payment` sentinel — with the residual, stated rather than hidden, that nothing forces the *next* borrowed foreign error into it. **Known gaps recorded, not fixed:** the async creditor back-fill is unreachable over HTTP (a push quoting no creditor address is refused `422`), and `ErrSchemeUnsupportedReturn` is documented but untested. |
