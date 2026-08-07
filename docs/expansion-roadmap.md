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
  `store/sqlite/schema/` all have to move with it. See `CLAUDE.md`.
- **Nothing requires setup.** A fresh checkout runs the whole suite with no
  database, no Docker and no toolchain. Every new entity added below still needs
  to be in `store/storetest`.

  **This bullet used to read "Postgres stays optional", and Task 17.3 retired
  it** (sub-project 9 below). `store/pg` and
  `store/mem` were replaced by a single backend, `store/sqlite`, on the cgo-free
  `modernc.org/sqlite`. The property underneath survives and survives more
  cheaply, because one backend that needs nothing beats two backends of which one
  did. What ended is conformance *between implementations*: `store/storetest` is
  now the shared suite each of Task 18's three store shapes runs, and its doc
  comment says so.

## Where the code stands today

- `ledger` — pure double-entry GL: ledgers, subledgers, accounts (Asset,
  Liability, Equity, Revenue, Expense), multi-leg balanced transactions,
  on-demand balances, idempotency, reversal, audit log. `ledger.Book`.
- `deposit` — DDA layer: account status/lifecycle, overdraft limits, holds and
  available balance, end-of-day snapshots. Each deposit account wraps a backing
  Liability GL account and stores no money itself. `deposit.Register`.
- `payment` — one institution's handle on the interbank network: SEPA CT (push)
  and SEPA DD (pull), `Initiated → Accepted → Cleared → Settled` **per copy**,
  and net settlement posted by the settlement agent in its own book alone.
  `payment.Network`, minted per institution by `payment.Networks`.

  **This bullet used to say "participants … inside one store transaction" and
  sub-project 8 retired both halves.** `payment.Participant` is three rows now,
  one per institution (`Bank`, `SettlementMember`, `RosterEntry`), and there is
  no transaction spanning two of them: settlement is final at the central bank
  and every other institution books afterwards, from a message.
- `interest`, `lending`, `product` — the sub-project 2 layers.
- `iso20022` and `mesh` — the messages, and the N+2 actors that exchange them.
- `payment/recon` — the reconciliation harness, test-only by convention: the one
  instrument that opens every institution's database at once, precisely because
  no actor in the system may.
- `api`, `store/sqlite`, `store/storetest`, `store/testenv`, `seed`, `web`.

**The asset dimension is in.** The known assets are a package-level list of
`ledger.AssetDef`s in code (code, name, scale, class — `ledger.LookupAsset`),
not rows; every GL account is denominated in exactly one, fixed at creation;
`deposit.Account` carries the same asset; transactions balance **per asset**;
schemes declare their asset and each leg of a payment is validated against it by the bank that holds that leg's account;
participants hold one suspense/reserve/settlement set per asset they joined
with. **The schema is three `0001_init.sql` files, one per institution shape**
(`bank`, `csm`, `centralbank`) — sub-project 8 is what made it three, and the
reason each is still a single `0001` is unchanged and is that no database is
deployed. See `README.md` and quiz chapter 16.

## Sub-projects

Each gets its own spec → plan → implementation cycle. They are listed in
build order — except §9, which was not foreseen and landed *inside* §8's task
sequence, for the reason recorded there.

### 1. Multi-asset ledger core — `done`

Spec: [`superpowers/specs/2026-07-27-multi-asset-ledger-design.md`](superpowers/specs/2026-07-27-multi-asset-ledger-design.md)

Introduced an asset dimension: assets as first-class definitions (code, name,
scale, class), an asset on every GL account, per-asset balance enforcement in
`Book.PostTransaction`, and the corresponding deposit, payment, schema, store,
conformance, API, web and documentation changes.

A follow-up removed the book-scoped `assets` *table* it had shipped with. The
table was writable at runtime and nothing downstream could honour a runtime
asset — a bank's plumbing accounts are provisioned once and never extended,
its own at founding and its settlement account when the scheme answers — so it
promised a capability the system could not deliver, and the mismatch surfaced as
a reachable 404 on funding. The definitions moved into Go, where schemes already
live. What stayed per bank is `bank_assets`: which assets a bank operates in.

Foundational — it touched `ledger.Account`, `Book.PostTransaction`,
`deposit.OpenAccount`, the participant account triple, the stores that existed
then, `store/storetest`, the API DTOs and the web app. Doing lending first would have
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
  message layer removed. `InitiatePaymentTx` split three ways — a submitting
  bank's half, a receiving bank's half and the clearing house's — so no actor
  reads both accounts and each bank compares
  its own leg against the scheme's asset. That is strictly weaker (two banks
  could each hold a conforming account in a scheme neither is entitled to) and
  strictly what a real bank can do. Where the check runs and why it is not in
  `Validate` are unchanged; only the claim about a single moment is.
  *Superseded again by sub-project 8's Task 15:* "settlement is all-or-nothing,
  so one bad payment fails the whole cycle" is no longer true either. The
  creditor leg is the payee's bank's own posting now, made in its own unit of
  work on the clearing house's per-payment advice, so a cross-asset payment
  fails at that one bank and the cut-off settles around it. What still holds is
  the argument the sentence was making: the failure is late and names an
  imbalance rather than a payment, which is why the check is at initiation.
- **The asset check is a domain rule, so the schema gets no constraint.**
  `accounts.asset` deliberately has no `CHECK` restricting it to the known
  codes; Postgres could express "the asset must be one the system knows" and
  `store/mem` could not, and the conformance subtest
  `ParentReferencesAreNotEnforced` is what held the line. A `CHECK` would also
  turn a one-line change to a Go slice into a migration. `0001_init.sql` records
  the reasoning in the database, because the absence of a constraint is invisible
  in a schema dump.

  *Task 17.3 removed the first reason and kept the ruling.* With `store/mem`
  gone, "Postgres could express it and a Go map could not" stopped being an
  argument — the migration reason never mentioned either store, and it is now the
  whole reason. The subtest still holds the line, because its fixtures write
  accounts with no asset set at all. SQLite has no `COMMENT ON`, so the last
  sentence's mechanism changed too: the reasoning is inside the `CREATE TABLE`,
  where `sqlite_master` keeps it in the stored statement text, and
  `TestSchemaArgumentsReachSqliteMaster` fails if it moves out.

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

The deployment unit is the **listener, not the process** — `store/mem` was one
process's memory, so N processes would have been N disconnected universes, and
Postgres-optional was load-bearing. One binary runs every listener by default;
`-entity` ran one per process and refused to start without a DSN, saying why.

*Task 17.3 reverses the constraint, in a direction nobody asked for.* A SQLite
file under WAL is shared between processes, so `-entity`-per-process stops
needing a server — the DSN it refuses to start without becomes a path. That is
recorded, not acted on: nothing below depends on it, and the listener is still
the deployment unit.
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
crossed into 7b and is now **decided and executed**: 7b deleted the central
bank's `POST /settlements`, which `/central-bank`'s settle console called
through `useSettleCycle` → `api.settleCycle` → `cb("/settlements")` — a string,
so neither `tsc` nor `next build` noticed and the screen 404'd at runtime. The
action did not move to the central bank. Settling is performed on instruction
(`mesh.centralBank`), and what an operator has instead is
`POST /cycles/{cid}/settle` on the **clearing house**, which re-sends the
`pacs.009` for a cycle the settlement agent refused; it moves nothing itself.
`useSettleCycle` is re-pointed at it. The rest are the
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
  routing by `BICFI`, `InitiatePaymentTx` split **three ways** — the submitting
  bank's half, the receiving bank's half (which comes back as a `pacs.002`) and
  the clearing house's, the only one that makes a payment `Accepted` — and
  `api/` genuinely asynchronous behind the `202 Accepted` plus status query 6a
  already shaped the client around. Settlement stays one atomic `Store.Update`
  at the central bank, now **instructed** by a `pacs.009` the clearing house
  sends when it closes a cycle — the fifth message, and the one 7a did not ship.
  A refused settlement is re-instructed by `POST /cycles/{cid}/settle` on the
  clearing house, which rebuilds the same `pacs.009`; without it an `AM04` was
  terminal, with every payer debited and every payee unpaid.
  `-entity` is gone, as this section's transport argument below concluded it
  should be.
- **7c, the message log** — `todo`. Envelopes persisted so a payment screen can
  show the XML that actually moved, plus the README, hint and quiz layers.

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

*"The `camt` reporting family" is a reversal, not a stale sentence, and it was
reversed three times by sub-project 8.* **`camt.053`** landed at Task 15, because
the unreconciled position needs a statement carrying a **balance** — a
notification (`camt.054`, which the earlier plan named) carries none and
therefore cannot detect a wrong posting. **`camt.050` and `camt.025`** landed at
18a, because a bank lodging cash at the settlement agent is a real conversation
and the alternative was a store write across two institutions. What is still
refused, and refused *specifically* rather than by family, is recorded in
`iso20022/doc.go`: `camt.054` for the reason above, and `camt.051` because it
pulls liquidity back the other way and **this system lodges cash and never
withdraws it**.

### 8. Per-entity stores — `done`

Spec: [`superpowers/specs/2026-08-02-db-per-entity-design.md`](superpowers/specs/2026-08-02-db-per-entity-design.md)
Plans: [`14`](superpowers/plans/2026-08-02-task-14-message-carries-the-parties.md) ·
[`15`](superpowers/plans/2026-08-02-task-15-settlement-becomes-a-conversation.md) ·
[`16`](superpowers/plans/2026-08-03-task-16-return-becomes-a-conversation.md) ·
[`17`](superpowers/plans/2026-08-05-task-17-admission-becomes-a-conversation.md) ·
[`18`](superpowers/plans/2026-08-06-task-18-split-the-stores.md)

Five tasks, numbered 14–18 because they continue 7b's numbering, with sub-project
9 (§9 below) landing between Task 17 and Task 18 because the split wanted one
backend underneath it rather than two.

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

*Both happened, and neither happened at the store split.* `SettleCycleTx`'s one
`Store.Update` went at **Task 15**, three tasks before any store was separated —
which is the ordering lesson of the whole sub-project: the domain change is what
costs, and the store split is what stops it being reversible by accident. The
live handles went at **Task 17**, where `payment.Participant` was dissolved into
three rows held by three institutions — `Bank`, `SettlementMember` (the agent's,
keyed by BIC) and `RosterEntry` — and no method on `bankOps`, `csmOps` or
`settlementOps` returns a `*ledger.Book`, `*deposit.Register` or
`*product.Catalogue` any more. What the paragraph did not price is that removing
a transaction is not the same as removing the *crossings*, and the count kept
growing. The spec's table named five; `DepositTx` posting in two books was added
at Task 17, a counterparty `Bank`-row read on the payment happy path at 17.3, and
`CreateMandateTx` plus its HTTP twin at 18a. All of them closed **before** any
store was separated, at 18a and 18b — deliberately, because a crossing that still
compiles is one the split merely hides behind a missing table.

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

*The concept shipped; the second half of the sentence did not, and that is Task
19.* An unreconciled position is a modelled state from Task 15 onward — a
clearing suspense that has not returned to zero with no `SettlementAdvice` row
against the cycle — and 18e's `payment/recon` is the instrument that names one.
It distinguishes two findings that read alike and are not: a **break** is two
books that disagree with nothing in the system able to make them agree again; an
**unreconciled position** is money legitimately in flight, which is what the
Settlement Finality Directive is about, and a harness that called it a break
could not be run against a live network. What is still absent is *what a bank
does with one*: `payment/recon` is the OMNISCIENT observer, which no institution
may be, and `SettlementAdvice.ClosingBalance` — the figure that lets a bank check
its own books against what it was told without reading anybody else's store — is
written and has no reader.

*Superseded by sub-project 8's Task 15, and this is the paragraph that came
true.* "`SettleCycleTx` today moves the netted reserves, mirrors them in each
bank's own books and pays out every creditor inside one `Store.Update`" is a
description of code that no longer exists, and it went **before** the stores
split rather than with them: the central bank posts only its own netting
transaction, each member books its mirror leg from a statement, and each payee's
bank posts its own creditor leg on a per-payment advice. What this section said
the model got wrong is now what the model teaches — a member that fails to book
has a reconciliation break and not a rollback, visible as a clearing suspense
that has not returned to zero with no `SettlementAdvice` row against the cycle.
(That row commits in the same unit of work as the mirror leg it records, so a
failed booking leaves none; an `Advised` row is not what a break looks like, and
several layers said it was until the review of Task 15b.4.) Two details landed
differently. The message is a
**`camt.053`**, not a `camt.054`, because a notification carries no balance and
therefore cannot detect a wrong posting; and "the settlement agent holds every
reserve account there" is exact only about the central bank's own book, which is
where each member's position is recorded on that side. `Network.bind` and the
live handles on `Participant` survive, because Task 18 is what removes them.
*(A third detail, recorded here because this sentence was the live one: it was
**Task 17** that removed them, not Task 18, and it removed `Participant` with
them — dissolved into `Bank`, `SettlementMember` and `RosterEntry`. Admission had
to stop being one unit of work before the handles had anywhere left to hang.)*

**Dependencies.** 6a, for the per-entity API. 7b, for seams that are already
message-shaped — attempted before 7b this is a redesign, attempted after it a
mechanical extraction. 7b makes that claim checkable rather than hopeful: it
ships narrowed per-actor interfaces and a book-access recorder whose test
assertions *are* this sub-project's specification of the split.

*The dependency held; "a mechanical extraction" did not, and it is worth being
honest about which half was right.* Attempting this before 7b would have been a
redesign, exactly as written — every seam the split lands on was already
message-shaped, and the recorder's assertions really were the specification, to
the point that two of them **could never have passed** and had to be renamed to
what they measure. What was not mechanical is everything downstream of the seams:
Task 18 alone is five sub-tasks rather than the spec's two, because 31 tables, 26
interface methods, 20 non-test call sites that stop having an answer and 83
recorder expectations in one file do not fit in one reviewable commit.

**The end state is separate databases**, not merely separate `Store` values —
one SQLite file per entity, or one ephemeral database per entity in a test. The
no-setup property survives it, and so does `store/storetest`, which is already
per-store and becomes per-shape.

**Survives unchanged:** needing no setup (`make dev` and `go test ./...` open
ephemeral databases), and `store/storetest`, which is already per-store and runs
against each shape independently.

*Both shipped, with one number worth stating precisely.* It is **N+2 databases
over three schema shapes** — `bank` (23 tables), `csm` (7) and `centralbank`
(12), one migration apiece — because every member bank's database is the same
shape as every other's. A method reaching for a table its institution's schema
does not create is refused **by name**, `sqlite.ErrNotInThisShape`, which is what
makes a surviving crossing a message rather than a nil dereference. `store/mem`'s
disappearance (§9) is what turned "three shapes × two backends" into "three
shapes × one".

**Out of scope:** separate processes, two-phase commit, and any distributed
transaction manager. If settlement cannot be made final in one book and mirrored
asynchronously into the others, the design is wrong rather than under-powered.

### 9. One store, and it is SQLite — `done`

Spec: [`superpowers/specs/2026-08-03-sqlite-only-store-design.md`](superpowers/specs/2026-08-03-sqlite-only-store-design.md)
Plans: [`17.0`](superpowers/plans/2026-08-06-task-17.0-the-races-become-a-suite.md) ·
[`17.1`](superpowers/plans/2026-08-06-task-17.1-store-sqlite.md)

**Not on the original list**, and it interrupts sub-project 8 rather than
following it. `store/pg` and `store/mem` are both deleted; `store/sqlite`, on the
cgo-free `modernc.org/sqlite`, is the only implementation. It is numbered 9 while
landing as Tasks **17.0–17.3** — after 8's Task 17, before its Task 18 — so that
nothing in flight renumbered.

**Why there and not later.** Task 18 gives each institution its own database.
Held until after it, that is three schema shapes written twice, once per backend,
and one of the two needs Docker. Doing it first made 18 three shapes × one.

**Why 17.0 came before 17.1.** The nine race tests lived in `store/pg/pg_test.go`
and would have gone with the file. Four moved into `store/storetest` and five
became re-instantiable against any backend permitting two open transactions —
and the timing was the whole argument, because **a race test can only be watched
failing on a store with real MVCC.** `store/mem` serialises and SQLite serialises
writers, so after 17.3 no race test written for this repository can be
demonstrated capable of failing. 17.0 is the last point at which the suite that
certifies `store/sqlite` could itself be certified.

**What it bought.** The no-setup property, more cheaply: one backend needing
nothing beats two of which one did. `TEST_DATABASE_URL`, `docker-compose.yml` and
the `-pg` Makefile targets are gone, and there is one Go run rather than two.
A real `BEGIN`/`COMMIT` — "these writes commit or roll back together" became a
claim about the code instead of a side effect of one process-wide mutex.

**What it cost, and both halves are live constraints.** *Nothing cross-checks the
SQL any more*: `store/mem` was the oracle `store/sqlite` was certified against
and `store/pg` was the oracle before it, so anything needing proof against a
second implementation is proved before it lands. What replaces the cross-check
for the two failures that are otherwise silent is a guard each in
`store/sqlite/sqlite_test.go` — foreign keys really enforced, and exactly one
non-primary-key unique index. And *`store/storetest` stopped being a conformance
suite*, which its own doc comment now says: there is nothing left to conform to,
so it is the shared suite that each of Task 18's three shapes runs.

**The mechanical rule this created, and it outlived the swap.** SQLite has no
`COMMENT ON`, and `sqlite_master` stores a statement's text — so a comment
**inside** a statement's parentheses reaches a schema dump and one above it is
dropped silently. Every argument about something a schema does *not* do therefore
lives inside the statement it concerns, which is what `COMMENT ON COLUMN` used to
buy for free and what an absent constraint has no column to hang one from.
`TestSchemaArgumentsReachSqliteMaster` fails if one moves back to column 0. The
translation was verified the same way rather than read: all **1156** indented
comment lines dumped from a migrated `sqlite_master` and diffed against source, 0
missing — the acceptance criterion Task 18 then inherited three times over.

**One finding worth more than the swap.** *The ephemeral store hides
read-then-write defects.* On `memdb` a retry's read blocks until the winner
commits, so a loser reaches the domain's guard however the code underneath
behaves — `LockAccounts` emptied passes its race ten runs out of ten. Only a
file, under WAL, lets a reader past an uncommitted writer. Anything measured
about ordering has to open one.

**Out of scope, and it stays so:** a second backend. The rule *"where the two
stores disagree, `store/mem` is right by definition"* has no successor, and every
schema argument that rested on it was re-justified from the domain rather than
carried.

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
| 2026-08-01 | Sub-project **7b done** (`7a`, the `iso20022` package, was already on `main`; 7c is still `todo`, which is why section 7 reads `wip`). The message is the interface: `mesh` owns N+2 actors — one goroutine per member bank, one clearing house, one central bank — each with an unbounded inbox, and nothing but **marshalled bytes** crosses between them, so a message is parsed on arrival and `FF01` is a reachable failure mode rather than a decoration. Four flows ship — the credit transfer, the direct debit, the cut-off and the return — and a rejection is a branch of the first two rather than a fifth. `-entity` is gone; `POST /settlements` is gone; `api` answers `202` with a `paymentId` and means it. **Eight deviations from this plan, all of them recorded rather than folded in silently.** (1) **`InitiatePaymentTx` split three ways, not two.** The spec said the creditor-account check "moves out of `InitiatePaymentTx`", which is true for a push and backwards for a pull. What shipped is `SubmitPaymentTx` (the submitting bank's half — the debtor half on a push, the creditor half on a pull, posting *nothing* on a pull), `AcceptInboundTx` (the receiving bank's half, which is the one that moves money on a pull) and `AcceptAtCSMTx` (the clearing house's, and the only one that makes a payment `Accepted`). The rule that covers both directions, and that neither flow on its own would tell you, is that **the debtor's bank posts the debtor leg**. (2) **Signatures.** `CreditTransferMessage` and `DirectDebitMessage` gained a `ctx` the plan's signature did not have — they read the store, and `context.Background()` inside a store read is the thing to avoid (*superseded by sub-project 8's Task 14:* they read the store no longer — both counterparty names travel on the instruction and `partiesOf` reads nothing, so both signatures LOST the `ctx` again, and the `…MessageTx` variants that shared a caller's transaction are deleted; `ReturnMessage`'s reasoning below is the one that now covers all three); `ReturnMessage` shipped as a `*Network` method although it reads nothing, because the amount's scale comes from the scheme's asset and only the `Network` holds the scheme registry. (3) **A rejection is two halves and can half-happen.** `RejectPayment`/`RejectPaymentTx` are deleted; `RejectAtCSMTx` marks the payment and `ReverseDebtorLegTx` gives the payer their money back, in two units of work by two institutions. Between them the payment is `Rejected` with the customer's money still in clearing suspense — the first operation in this repository that can be caught in the middle. A reversal that fails has nobody to answer, so it becomes a mesh dead letter rather than a quiet lie. (4) **A rejected pull can send TWO `pacs.002`.** "One decision, one message" is no longer a property of this system, because the bank waiting for the answer (the payee's) and the bank holding the money (the payer's) are different institutions; dropping the second recipient strands the payer's money against a payment nothing will reverse. The condition is exactly the money — `csm.tell` sends the second only when the payer's bank has already posted the debtor leg, so a collection that bank refused *itself* for want of funds (`AM04`, before any posting) is one message again. (5) **Two book invariants the plan wrote before the code were both wrong, in the same direction, and were corrected by measurement rather than by argument.** `TestABankHandlerTouchesOnlyItsOwnBook` could never have passed: a submitting bank reads the *counterparty's* deposit register, because a `pacs.008` names the payee and `payment.partyTx` fetches that name — so **"no bank reaches another bank's book" is false today**, and closing it is a domain change (the request would have to carry the counterparty's name), not a transport one. *Superseded in part by sub-project 8's Task 14:* the prediction held and the domain change is the one that was made — `partyTx` is deleted, the counterparty's NAME travels on the instruction, and the submitting bank's measured set is `[own, network]` in both scheme directions. It closed the SUBMITTING half only: the receiving bank still sweeps every member's register to resolve one IBAN (`ResolveIdentifierTx`), so the sentence stays true of that half and Task 18 is what would close it. *(Closed by sub-project 8's Task 18a: `ResolveIdentifierTx` takes the asking bank and answers out of that bank's own register alone, so the sentence is now false of both halves and "no bank reaches another bank's book" holds on the payment path. Two consequences worth carrying — the counterparty's BIC had to become an ASSERTION again, reversing Task 14's derivation, because narrowing the resolution is what makes a wrongly-named bank refuse with `AC01` instead of misrouting; and a cross-bank address collision is no longer observable by anything.)* Task 14 also had to take back what it first handed over — the counterparty's *agent* travelled on the instruction too for a while, unverified, and the clearing house routes on it; it is derived from the roster now, which is a network-scoped read and therefore no bank's book. `TestTheCentralBankTouchesOnlyTheCentralBankBook` could never have passed either: settlement's mirror and creditor legs post into the members' books. Both tests are renamed to what they measure. A third drafted name, `TestSeedDrainsBeforeListening`, rested on a false premise and was corrected before it shipped. (6) **`ErrCycleNotOpen` moved and changed kind.** It is the clearing house's refusal now, not a `422` at submission, and on the wire it is a `pacs.002` carrying **TM01** — the cut-off is the clearing house's, and no bank refuses its own customer's instruction on account of it. (7) **Settlement is instructed.** Closing a cycle emits a `pacs.009`; the central bank decides and can answer **RJCT/AM04**, leaving the cycle `Closed` with no settlement against it and every payment exactly where the cut-off left it. A refusal is fanned out to nobody, because nothing was posted and there is nothing truthful to tell a bank. (8) **Seed first, then start.** `mesh.Start` reads the participant roster exactly once and `seed.Populate` calls `AddParticipant` directly, so starting the mesh before the seed leaves every seeded bank with no actor. **What was learned.** The signature failure of this branch, again and at scale: *prose that asserts what code does needs a test, not a careful reading*. One test on Task 11 took four rounds, and each of the first three fixed the previous round's overclaim; three separate comments were found claiming a property their own test could not fail on; a book-access recorder built to enforce the boundary had three holes, **two of them introduced by the fix to the previous hole**. Two mechanisms are what caught most of it: the **narrowed per-actor interfaces** (`bankOps`, `csmOps`, `settlementOps`) — which narrow by *method* and therefore do **not** close `GetParticipant`, which hands back live book-bound handles, so no "the compiler prevents this" claim about them is safe — and the **book-access recorder**, whose assertions are sub-project 8's specification written a sub-project early and which found the counterparty-register crossing nobody predicted. Also worth carrying: `Drain` before `Stop` (Stop cuts chains, so an undrained shutdown ends payments halfway); the fuzz target found two real translator bugs and a hand-written probe a third the fuzzer could not reach (`ledger` holds BTC at scale 8, which `ActiveCurrencyAndAmount` cannot express); and an `AM04` on a collection needed a **second** reason table, `borrowedReasons`, because `deposit.ErrInsufficientAvailable` is not a `payment` sentinel — with the residual, stated rather than hidden, that nothing forces the *next* borrowed foreign error into it. **Known gaps recorded, not fixed:** the async creditor back-fill is unreachable over HTTP (a push quoting no creditor address is refused `422`), and `ErrSchemeUnsupportedReturn` is documented but untested. |
| 2026-08-02 | Sub-project 8 **Task 14 done** — *the message carries the parties*, and the sub-project's design is on `main` (`specs/2026-08-02-db-per-entity-design.md`): four decisions taken before any code, three rulings reversed, and **five crossings** the split has to close. Two of the five were not on the previous handoff's list, because reading the code for the spec is what found them — admission is a cross-entity atomic write, and the return cannot survive isolation at all until `OrgnlTxRef` exists on pacs.004, since a pacs.004 names no parties and a central bank with no payment rows cannot tell whose settlement accounts to move. Task 14 closed the first: `payment.partyTx` is deleted, `partiesOf` is a package function that reads nothing, and **building an outbound message performs no I/O at all** — the payee's NAME travels on the request. Both measurements moved and both were watched failing: the payer's bank from `[debtor, creditor, Network]` to `[debtor, Network]`, and a pull's submitting payee's bank to `[creditor, Network]`; `assertBooksTouched` compares whole sets, so these say what the bank did **not** reach. **The whole-branch review caught the regression per-task review structurally could not.** Once the counterparty's BIC moved onto the request it was payer-supplied, validated for BIC *format* only and never compared to the named participant's real BIC — while the clearing house routes on it, and before Task 14 `partyTx` had read the real participant so the two could not disagree. Measured with mesh probes rather than argued: a push carrying the payer's own BIC as the creditor agent reached `Accepted` with the payer's bank touching exactly the set the branch had just moved off; a pull carrying the collector's BIC had **the collecting bank post the debit in the payer's bank's book**, with the payer's bank never seeing the collection. Ruling: **the agent is derived from the roster entry for the named participant and only the NAME stays asserted by the payer** — which is what real SEPA does (IBAN-only since 2016, the originating bank derives routing and never trusts a payer-supplied BIC) and what the spec already said. The fixture banks had to be given **distinct BICs** before the pinning test could fail at all. Also fixed, pre-existing: `checkPartyTx` flattened *any* `GetParticipant`/`GetDepositAccount` error into a domain sentinel, and it runs inside `AcceptInboundTx` whose errors reach `ReasonFor` — so a dropped database connection at the receiving bank **reported AC01 to the sender**, telling another bank its customer's IBAN was bad when it was not, and on a push the payer's debit was then reversed. Learned, and it set the method for everything after: **five of six sub-tasks needed a fix round, and in every one the Go was right on the first attempt and the prose was wrong.** One took five rounds, and the cause is structural rather than careless — a reviewer names one false sentence, the fix corrects that one, and the correction makes the *next* instance visible, because duplicated documentation hides a shared error until something forces the layers apart. So **a domain-claim correction is a sweep, not an edit**: grep every layer for every phrasing in the same pass — README, hints, all 18 quiz chapters, the schema comments, and Go/TS comments **including test files**, which round 5's own confirming grep found two instances in and no earlier sweep had covered. |
| 2026-08-03 | Sub-project 8 **Task 15 done** — *settlement becomes a conversation*, and the largest domain change in the sub-project. One `Store.Update` was doing the work of three; now the clearing house nets and sends the pacs.009, the central bank checks each net payer's reserve **in its own book**, posts **one** netting transaction and is **final either way** (ACSC, or RJCT/AM04) while reading no payment at all, sends a **camt.053** to each member with a non-zero position, and each bank books its mirror leg from the statement and its creditor legs from per-payment advices, locally, each in its own unit of work. The interval between finality and booking is the **unreconciled position**, and it is not a modelling convenience — in the EU it is the Settlement Finality Directive. Measurements moved and were watched failing: the central bank from `allBooks()` to `[CentralBankBook, Network]`, and a renamed `TestEachBankBooksItsOwnSettlementAndNoOtherBooks` from `{}` for both banks to a per-bank set, because both banks now book during the cut-off and the old test's *name* claimed the opposite of its measurement. **The finding this task existed to protect and nearly lost: AM04 lived in the mirror leg.** Measured, not reasoned — a member's settlement account in the central bank's book is a `ledger.Liability`, `checkSufficientBalance` guards only `Asset` and `Expense`, so the refusal that made an underfunded net payer come back RJCT/AM04 came from the **member's own book**, where `Reserve at Central Bank` is an Asset. Moving the leg without moving the check would have settled a broke member's cycle clean, with the shortfall surfacing later at the bank as a dead letter; the mutation was watched doing exactly that. The check is explicit in `SettleCycleTx` now, which is the settlement agent declining to extend uncollateralised intraday credit — the decision that institution exists to make, at last living there. **A ruling became a fix**: an `Unclaimed Balances` Liability per bank per asset, because once a bank posts its own creditor legs a payee's closed account fails **one payment at one bank** instead of the whole batch, so `CheckCreditTx` became affordable — and only `deposit.ErrAccountClosed` diverts, everything else being a store failure, since money must not be routed on a failure nobody can classify. **The whole-branch review found a Critical that per-task review cannot see**: returning a payment that settled into Unclaimed clawed back from `creditorGL` — the payee's own account, which never received the money — leaving the closed account at −30000, the Unclaimed liability never released and the bank 30000 of reserves out of pocket, with **the books still balancing** (two liabilities netting to zero) so no ledger guard fired and no test noticed. Fixed by recording `Payment.CreditorLegAccount`; deriving it at return time was rejected because an account open at settlement and closed afterwards is indistinguishable from one closed at settlement. Learned: **the prose outruns the code and it is inherited verbatim from the plan** — six of eight review rounds were comments asserting things the code does not do, and a plan is written before the code exists, so every comment it dictates is a prediction that ships as a lie with a reviewer's blessing; the fix belongs in the plan-writing step. And *"watched failing" has a failure mode of its own*: an ordering assertion could not be falsified by the obvious mutation because the central bank usually wins the race, so **when a mutation does not fail, the assertion is wrong until proven otherwise — but so is the mutation.** |
| 2026-08-05 | Sub-project 8 **Task 16 done** — *the return becomes a conversation*. `ReturnPaymentTx` posted three compensating transactions in one `Store.Update`, two of them in member banks' books and made by the settlement agent. Now the returning bank posts **its own leg** and **refuses here** if it cannot fund it, sends a pacs.004 carrying `OrgnlTxRef` naming both agents, the clearing house relays it and **holds it**, the central bank reads the parties off the message and reverses reserves in its own book and is final either way, camt.053s go to both banks before the answer, and the held pacs.004 is released only once there is an ACSC. **The one rule, and the direction-dependence falls out of it rather than being stipulated:** the clawback is always at the *creditor's* bank out of `Payment.CreditorLegAccount` and the refund is always at the *debtor's*; direction decides only which of the two the returning bank is holding; so **a bank can refuse a leg only if it posts it before it sends**, and `Returns Receivable` is needed on the pull side and nowhere else, because the bank forced to post is the one that first hears about the return after it is already final. The measurement moved to **`[CentralBankBook]`** and the plan had predicted `[CentralBankBook, Network]` — measured, not copied: `SettleReturnTx` writes no row and appends no audit event, so the agent never allocates a network id. **Two Criticals from the whole-branch review, both found by building a probe and running it**, which is now the second consecutive sub-project where reading the diff found neither. C1: a return retried after an RJCT unwind never repaid the payer — `ReverseReturnLegTx` left the leg's transaction id on the payment under a comment arguing *"there is no reader for whom a stale id here decides anything"*, and the reader was thirty lines away in the same file, taking the idempotent-redelivery arm on a **`Reversed`** transaction while the rest of the conversation ran to completion; measured, the biller clawed back 250000, the payer repaid nothing and the money stranded for ever against a row reading `Returned`. **Fixed by fixing the reader, not the writer** — ask the ledger whether the leg *stands* rather than whether a field is non-empty — and refusing a second `Return` outright was rejected because it trades a money bug for a payer who can never be repaid: *the rejected option was the smaller diff, and that is not a reason.* C2: an on-us return silently lost half the reserve reversal, because two statements for the same `(Book, Reference, Asset)` differing only in sign dedupe to one; fixed by refusing on-us **where it enters the mesh**, since an on-us payment never reaches a clearing house in life, rather than special-casing the symptom. That leaves an **openly recorded hole**: two customers of the same bank cannot pay each other at all, and the right shape is a book transfer, which is its own task. Also uncovered here and owned by nobody: **a cycle that nets to zero strands every payment in it, for ever** — both payers debited, neither payee credited, and it is pre-existing rather than this branch's. Learned: **a grep for one phrasing is not a sweep of a claim, and reporting it as one is how a false statement survives a review** — what worked on the third attempt was changing the instruction to *do not grep; read whole every comment block whose subject is this, and report which files you read in full*, because a list of files read is checkable and a grep pattern is not. Two corollaries: **a count in a comment goes stale and a description of what happens does not** (every false claim here was a number, and they were rewritten as acts), and **a comment written during a transition should announce its own expiry.** And two sharper forms of the standing lesson: a comment that argues *there is no reader* is an unverified whole-codebase assertion, and *"untested; nothing in the repository constructs one"* is a description of the test suite rather than of the code — the reviewer constructed one in a single test and it was reachable through the HTTP API. |
| 2026-08-06 | Sub-project 8 **Task 17 done** — *admission becomes a conversation*, closing the fifth and last crossing on the spec's table. `AddParticipantTx` is deleted; `Mesh.Admit` reserves the BIC, founds the bank in the bank's own unit of work and returns, the scheme's answer arriving later as a message; the bank sends one `acmt.007` **per asset**, the clearing house relays and **holds nothing**, the central bank opens one settlement account per asset in its own book and answers `acmt.010`/`acmt.011`, and the clearing house writes its routing entry *from that acknowledgement*. `payment.Participant` is dissolved into **`Bank`** (the bank's), **`SettlementMember`** (the agent's, keyed by BIC) and **`RosterEntry`** (the clearing house's), and `ops.go`'s live-handle hole closed with it — no method on `bankOps`, `csmOps` or `settlementOps` returns a `*ledger.Book`, `*deposit.Register` or `*product.Catalogue`. **Four rulings the schemas and the probes forced**, each written up as a dated ruling because a reversed ruling that reads like an oversight is worse than the original: one `acmt.007` per asset, because `Acct/Ccy` is `maxOccurs="1"` and both spec and plan had assumed a currency list; `Refs/PrcId` is the conversation's **only** correlator, which is what makes the clearing house's pre-relay refusal implementable at all — keyed on the BIC it would refuse both the same bank's second asset and an operator re-drive, and fire on neither; the roster **carries no name**, because `acmt.010` identifies the owner with a BIC and nothing else, so carrying one would mean holding state across the relay and re-importing `csm.held`'s restart defect; and *"cannot pay or be paid"* was half enforced. That last is the Critical, and it is the **third consecutive sub-project** in which the whole-branch review found one invisible to a clean per-task history, by building a probe: an **arranged overdraft** or a **loan disbursement** puts spendable money in a founded bank's customer with no deposit, and `FoundBankTx` gives internal accounts in every asset so the debtor leg posts — measured, the payment reached `Cleared`, its `pacs.002` **dead-lettered** so nobody was told, and the cut-off then failed, **stranding every other member's payments in that cycle**. Fixed with one sentinel refused in two places, and the two-guard shape is load-bearing rather than belt-and-braces: with the door bypassed the paying direction is `Rejected` but the status dead-letters, because `csm.tell` addresses through the roster too. **And the schema check ran for the first time on this branch**, finding that **every camt.053 this system had emitted was invalid** on two counts — six elements out of sequence *under a comment asserting the field order was the schema's*, and `BkTxCd`, the one mandatory child of an entry, missing outright — both having survived a per-task review, a documentation sweep and a whole-branch review with probes. **No probe finds that class. Only the schema does.** The process cost has its own file, `docs/superpowers/2026-08-06-avoidable-review-cycles.md`: twenty-three fix rounds, **every one carrying at least one false claim written inside a correction**, of which five found real money defects and eighteen were claims drifting from code. Its three mechanisms — plans specify behaviour and tests and **never comment text**; one canonical statement per domain fact with every other layer linking to it; and `storetest` drives `payment`'s acts — are the answer to a lesson the previous sub-project had already recorded in prose and watched recur anyway, which is itself the argument for making them mechanical. |
| 2026-08-06 | Sub-project **9 done** — *one store, and it is SQLite*, which was not on this roadmap and interrupts sub-project 8 rather than following it. `store/pg` and `store/mem` are both deleted and `store/sqlite`, on the cgo-free `modernc.org/sqlite`, is the only implementation. Numbered 9 while landing as Tasks **17.0–17.3**, between 8's Task 17 and its Task 18, so that nothing in flight renumbered. **Why there:** Task 18 gives each institution its own database, and held until afterwards that is three schema shapes written twice, once per backend, one of which needs Docker. **Why 17.0 first:** the nine race tests lived in `store/pg/pg_test.go` and would have gone with the file — four moved into `store/storetest` and five became re-instantiable against any backend permitting two open transactions — and the timing *was* the argument, because **a race test can only be watched failing on a store with real MVCC.** `store/mem` serialises and SQLite serialises writers, so after 17.3 no race test written for this repository can be demonstrated capable of failing; 17.0 is the last point at which the suite that certifies `store/sqlite` could itself be certified. **The code half is four files and the sweep is 77**, under one rule: a sentence saying `store/pg` needed a `SAVEPOINT` and SQLite does not is history and stays in the past tense, while a sentence saying a `CHECK` here would make one store refuse what the other performs is a live argument that lost its subject and is re-justified from the domain. `accounts.asset` carries the canonical version — a `CHECK` makes a one-line change to a Go slice a **migration**, with the database's copy deciding — and the general form is that enforcing a domain rule twice means enforcing it in two places that answer differently, the constraint firing first and as a constraint violation where the domain would have named what it refused. **What it bought:** one Go run rather than two, no `TEST_DATABASE_URL`, no `docker-compose.yml`, no `-pg` targets, and a real `BEGIN`/`COMMIT` — *"these writes commit or roll back together"* became a claim about the code instead of a side effect of one process-wide mutex. **What it cost, and both halves are live constraints:** *nothing cross-checks the SQL any more*, `store/mem` having been the oracle `store/sqlite` was certified against and `store/pg` the oracle before it, so anything needing proof against a second implementation is proved before it lands — what replaces the cross-check for the two otherwise-silent failures is a guard each in `store/sqlite/sqlite_test.go`, foreign keys really enforced and exactly one non-primary-key unique index; and **`store/storetest` stopped being a conformance suite**, which its own doc comment now says, there being nothing left to conform to. **The mechanical rule this created outlived the swap:** SQLite has no `COMMENT ON` and `sqlite_master` stores a statement's text, so a comment INSIDE the parentheses reaches a schema dump and one ABOVE the statement is dropped silently — every argument about something a schema does *not* do therefore lives inside the statement it concerns, `TestSchemaArgumentsReachSqliteMaster` fails if one moves back to column 0, and the translation was verified by dumping `sqlite_master` from a migrated database and diffing **all 1156** indented comment lines against source, 0 missing. **One finding worth more than the swap:** the ephemeral store hides read-then-write defects, because on `memdb` a retry's read blocks until the winner commits, so a loser reaches the domain's guard however the code underneath behaves — `LockAccounts` emptied passes its race ten runs out of ten. Only a file under WAL lets a reader past an uncommitted writer, so anything measured about ordering has to open one. Also corrected within the handoff itself, rather than shipped: the sweep first wrote in six places that Task 18 would put the id counter and the row into different databases, and used that to explain why the read-then-write orderings stay — it had not been established and is probably false, so the assertion was replaced with the question, and Task 18's plan answered it (**the counter follows the row**, and `ledger.NetworkBook` is deleted). |
| 2026-08-07 | Sub-project 8 **done** — **Task 18, split the stores.** N+2 databases: one per member bank, the clearing house's, the settlement agent's, over **three schema shapes** (`bank` 23 tables, `csm` 7, `centralbank` 12, one `0001` migration apiece). No statement spans two of them, a bank reading another bank's rows finds nothing, and a method reaching for a table its institution's schema does not create is refused **by name**, `sqlite.ErrNotInThisShape` — which is what makes a surviving crossing a message rather than a nil dereference. Five sub-tasks and not the spec's two, because 31 tables, 26 interface methods, 20 non-test call sites that stop having an answer and 83 recorder expectations in one file would have put a failing conformance case and a dead API route in one review. **18a and 18b land no split at all**, deliberately: the last crossings close and `payment.Network` gains an identity while the recorder that measures them still works, because a crossing that still compiles is one the split merely hides behind a missing table. 18a closed `ResolveIdentifierTx`'s sweep (a bank resolves out of its own register alone — which is what makes a wrongly-named bank refuse `AC01` instead of misrouting, and which cost the reversal of Task 14's derivation: **the counterparty's BIC is asserted again**), `DepositTx`'s posting into the central bank's book (a new `Treasury` subledger and `Vault Cash` per asset, appended *after* the existing two so every account number stayed where it was), and made the lodgement a real conversation — **camt.050 + camt.025**, whose shape forced the design: a `camt.025` carries **no amount**, so a bank cannot post from the receipt and either posts first or remembers the request in the actor, which is `csm.held`'s shape and its restart defect; the bank posts first, and `TestTheReceiptCarriesNoAmount` pins the absence so a field added later fails a test rather than quietly invalidating the argument. 18b made `payment.Network` **one institution's handle** — `Networks` mints one per institution and each api listener holds its own, the ruling being that a single shared `Network` behind `boundPID` meant `GET /directory` on two banks' ports resolving in the same register the moment identity became constructor state. 18c/18d split the schema and rewired, and the four open questions were answered by running rather than reasoning: `RunRaces` became `RunSystemRaces` plus a per-shape suite each; `settlement_positions` is the **agent's** and a bank holds `settlement_advices` instead; the audit log is **per institution**, so `seq` is each institution's own counter, `auditEventDTO` carries its book, `GET /payments/audit` is one route per institution, and **there is no cross-institution total order** — a finding rather than a gap, since an auditor holding four logs answers it with the MESSAGES; and `store/testenv` gained **two entry points**, `New` for the four layers that do not know what an institution is and `NewSet` for the five suites that drive more than one. **Five findings that were not test churn**, each a hole the split opened that nothing else would have found: two on-us defects reachable only through an arrangement `Mesh.Submit` refuses (`AcceptInboundTx`'s witness is the ROW, which is false when the same bank also submitted, so a collection settled and could be returned out of money nobody took; and `PostReturnLegTx` decides which leg is last by position in a conversation that one institution does not have) — found by a test whose own doc argues that **a rule holding only because no caller builds the counter-example is a rule nobody is keeping**, and it was right; `payment.Settlement` had to carry its own **asset**, because resolving it settlement → cycle → scheme is a chain out of the agent's database into the clearing house's; three API routes were on the wrong console, invisible while one store served every institution; and the audit `seq` change above. 18e added **`payment/recon`**, the reconciliation harness — five checks, opening all N+2 databases at once to ask the questions no institution may ask, calibrated in `mesh/recon_test.go` against three controls and five damaged states written behind every institution's back (through the store, because **none of those states is reachable through the domain, which is the whole reason the harness exists**), and run over the widest deployment the repository builds. It reports **two kinds of finding and only one is a defect**: a *break* is two books that disagree with nothing able to make them agree again; an *unreconciled position* is money legitimately in flight, and a harness that called it a break could not be run against a live network. Its one piece of domain knowledge rather than a read is that a return leaves the agent nothing durable but an idempotency key, so the movement is derived from the clearing house's copy by the rule that a return sends the money back to the payer — **the debtor's bank gains and the creditor's loses, on a pull exactly as on a push** — and with the signs flipped both banks break by twice the amount in opposite directions, which is how it was checked. 18e also swept all four layers and wrote **twelve reversals** where the old claim lived, the last of which was *"there is one migration"*: now one per shape, with its reason intact — no database is deployed, which is why each is still a single `0001`; **what changed is not the migration policy but how many schemas it governs.** Three domain facts none of the layers had ever carried are now in all of README, hints and quiz: a payment is **three rows in three databases that legitimately disagree**, so the lifecycle diagram is a state machine per COPY and `Initiated → Settled` is legal at a bank and impossible at the clearing house; there is no combined audit log and no order between two of them; and **a cycle does not name its settlement**, the id being allocated inside the agent's own unit of work in its own database while the `pacs.002` quotes the CYCLE. Learned, beyond the standing lessons: **a `--- FAIL` count is a floor and not a total until the package runs to the end** — a panicking fixture takes the test binary with it, and both intermediate counts here were low for that reason, one of them by sixty-five; **`inShape` is the instrument, not the obstacle**, so a "no such table" is a crossing and never a reason to add a table to a shape; **a test whose subject was designed out should be re-aimed and not deleted**, and the re-aim should assert the new shape hard enough that a regression to the old one fails; and the method that found the README's broken walkthrough, which no test in this repository could have caught because the example is prose — **run it.** Carried, and none is Task 18's: `/clearing-house/settlements` still reads the central bank's port (a navigation decision, noted where it will be met); a bank's own reconciliation against `SettlementAdvice.ClosingBalance` is **Task 19's**, because `payment/recon` is the omniscient observer that no institution may be; and four known defects since 18a, of which the harness is now the instrument that would detect the last. |
