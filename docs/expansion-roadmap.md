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

**Next in build order, ahead of 3 and 4.** A deposit account gains a set of
external identifiers — `(scheme, value)`, with `IBAN` the one scheme shipped —
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

### 6. Role-scoped web UI — `spec`

Spec: [`superpowers/specs/2026-07-31-role-scoped-web-ui-design.md`](superpowers/specs/2026-07-31-role-scoped-web-ui-design.md)

Replaces the single unified dashboard with identities you switch between:
central bank operator, bank back office, and bank customer, each on
persona-prefixed routes with its own shell. Depends on 5 for the customer's send
form.

Purely frontend by design: the API stays open and unchanged, and the scoping is
navigational rather than enforced. Real authn/authz, a scheme-operator
(clearing house) persona, a card-processor persona, and the customer's mandate
and credit screens are all recorded in the spec as out of scope rather than
dropped.

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
| 2026-07-31 | Sub-project 5 **done**. The `(scheme, value)` identifier set on `deposit.Account`, per-bank uniqueness in `deposit.Register` with no `UNIQUE` constraint, `payment.Network.ResolveIdentifier` refusing ambiguity rather than the first hit, `Scheme.AddressedBy()` enforced in `InitiatePaymentTx` beside the cross-asset check, `PartyRef.IBAN` → `PartyRef.Identifier` (stored, not derived), `GET /directory`, and no format validation all shipped as designed — `SE89-AURORA-1001` is still legal. Five things the plan or spec did not survive contact with: (1) a `UNIQUE (participant_id, scheme, value)` constraint was in the spec's working draft and was dropped during spec review, before the spec was ever committed — the `UNIQUE (book_id, name)` exemption ("the race is already closed, one layer up") does not apply, because nothing serializes two concurrent `AddIdentifier` calls, and `storetest.IdentifierUniquenessIsNotEnforced` now pins that the way `ParentReferencesAreNotEnforced` already does; the parent `FOREIGN KEY` does stay, under the exemption for child tables the store writes both sides of. (2) The store method changed shape: the spec's `FindDepositAccountByIdentifier(...) (Account, error)` shipped as `ListDepositAccountsByIdentifier(...) ([]Account, error)`, because "an address resolves to exactly one account" is a domain rule and the store holds none — `Register.ResolveIdentifier` turns 0/1/many into not-found/the account/ambiguous, and the network's sweep applies the identical rule across banks. (3) `PartyRef` turned out to be stored on **both** payments and mandates, which the plan did not anticipate — the rename forced a schema change to both tables, one `iban` column each becoming `debtor_identifier_scheme`/`_value` and `creditor_identifier_scheme`/`_value`, and the compiler found it, not the plan. (4) The task ordering left the tree red for one task: enforcement (Task 7) landed before the request DTO could express an identifier (Task 8), so nine `api` tests failed with 500 in between — a sequencing lesson for future plans, that enforcement and the means of satisfying it belong in the same task. (5) Two review-caught defects of a kind this roadmap already records: `storetest`'s `mandate()` fixture carried no identifier, so the conformance suite only ever round-tripped `mandates.debtor_identifier_scheme`/`_value` and `creditor_identifier_scheme`/`_value` as empty strings — exactly the case a scheme/value transposition would not surface — fixed with real, distinct identifiers on both sides plus an assertion-bearing subtest; and the new README/hint prose asserted that `Network.ResolveIdentifier`'s refusal "lives in `deposit.Register`", when the thing actually in `Register` is the different, write-time `checkIdentifierFreeTx` — the **third** time on this project that confidently-worded prose asserted something the code did not do, after the euro-to-bitcoin counter-example and the lending worked examples. Prose that asserts what code does needs a test, not a careful reading, keeps earning its line. Also found, unrelated to this sub-project: `payment.Participant.ProductID`, added six commits earlier on `main`, had never been wired into `store/pg` — `PutParticipant` never wrote it, `participantColumns` never read it — so `store/mem` was the only store that remembered it; it surfaced as `"productId is required"` from the API and `"product not found"` from the seed, nowhere near the store that lost it, while `./product` and the conformance suite stayed green because `ParticipantRoundTripsAndDropsLiveHandles` asserted every persisted field except that one (it does now). Fixed on `fix/pg-participant-product-id` (`eb0c7e4`), merged into this branch (`5a04ca4`), still unmerged to `main`. `go test ./...` green with no `DATABASE_URL` and against Postgres; `web`'s `npm run test`, `typecheck` and `lint` all green. |
| 2026-07-31 | Sub-project 5 **reviewed**, whole-branch, before merge. Six findings, all in the seam the branch opened between an account's id and its address. (1) The one that mattered: `SDD.Validate` compared a payment to its mandate with whole-struct `PartyRef` equality, and `PartyRef` had just gained the quoted identifier — so a remove-then-add reissue, the operation `deposit/register.go` advertises as moving neither balance nor history, left **no** quoting that worked and no `UpdateMandate` to repair it. `PartyRef.SameParty` now compares `(participant, account)` only, because a mandate authorises debits from an *account* and the address is a record of one payment's route. The lesson, and it is a new one: **when the compiler finds an unanticipated consumer of a changed type, re-run the design's failure analysis against it.** The compiler *did* find `PartyRef` on mandates — the previous log row records it as deviation (3) — but only the schema consequence was chased (two `iban` columns became four), never the behavioural one, and the spec's *Failure modes* section never asked what a mutable identifier does to a mandate. It asks now. (2) The address was optional on the way in and stayed empty on the way to storage, so "a payment records the address it was sent to" held only for callers who volunteered one — and no `api` test ever did; only `seed` did. Initiation now back-fills the account's single identifier in the scheme's scheme, and refuses (`ErrAmbiguousAddress`, 422) rather than choosing between several. Back-filling is what makes `SameParty` strictly necessary rather than merely correct. (3) `store/mem` handed out and stored `Account` verbatim, so its `Identifiers` slice was shared with the caller both ways, breaking `mem.go`'s own "Put deep-copies in, Get deep-copies out" invariant that `clone()`'s one-level snapshot rests on. (4) And the same write read back differently on the two stores — `[X, X]` kept two in memory and collapsed to one in Postgres, `[ZZ, AA]` kept insertion order in memory and came back sorted from Postgres, `[]` was non-nil in one and nil in the other — every case invisible because **every identifier fixture in the conformance suite held exactly one identifier**, which round-trips through any ordering or dedupe rule at all. A one-element fixture is not a fixture for a collection. (5) `checkAddressable` never checked that the quoted address was in the *scheme's* scheme, only that the account held it somewhere: unreachable while `IdentifierIBAN` is the only member, and therefore exactly the check that would have been silently wrong on the first day a card PAN arrived — which the design names as its load-bearing extensibility claim. (6) Three documented claims had no test: the within-bank half of the ambiguity refusal at the network layer, the 409/422 status codes for the addressing errors, and the debtor leg of any addressability check at all (every existing test used the creditor leg). Also narrowed, in README, hint and schema comment: the read-time ambiguity check is a claim about **routing** — `InitiatePaymentTx` takes an account id and never resolves, so two accounts colliding on one address both stay payable by id, which is right rather than a gap. And an unflagged deviation from the previous row, recorded here with the others: `GET /participants/{pid}/deposit-accounts/{did}/identifiers` was specced and never built — identifiers ride the account DTO instead, which is the better call for a set that is part of the aggregate, but it belonged in the deviation list. |
