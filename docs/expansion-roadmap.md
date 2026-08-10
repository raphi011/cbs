# Expansion Roadmap

What is left to build, in the order it should be built.

This file is forward-looking. What each shipped sub-project decided, reversed and
learned is in its spec under `superpowers/specs/` and in `git log`; the index at
the bottom is only enough to resolve a cross-reference by number.

**Status legend:** `todo` · `spec` (design agreed, spec written) · `plan`
(implementation plan written) · `wip`

## Goal and framing

The repository stays a **teaching reference**, but the backend should be
polished and behave like a real core banking system: no toy shortcuts in the
accounting core, correct invariants enforced at the boundary, and every new
domain carried through all layers the way the existing ones are.

Three constraints apply to everything below.

- **Domain knowledge stays consistent across layers.** `README.md` is
  authoritative; `web/src/components/hint-content.ts`, the quiz chapters under
  `web/src/lib/quiz/chapters/`, and the schema comments in
  `store/sqlite/schema/` all have to move with it. See `CLAUDE.md`.
- **Nothing requires setup.** A fresh checkout runs the whole suite with no
  database, no Docker and no toolchain. Every new entity still needs to be in
  `store/storetest`.
- **Nothing cross-checks the SQL.** There is one store implementation, so
  anything that would need proving against a second is proved before it lands.

## Where the code stands

- `ledger` — double-entry GL, multi-asset, balancing per asset. `ledger.Book`.
- `deposit` — DDA: status and lifecycle, overdraft limits, holds, available
  balance, end-of-day snapshots, and the book transfer between two of one bank's
  own accounts. `deposit.Register`.
- `interest`, `lending`, `product` — accrual, the two drawdown facilities, and
  the effective-dated product catalogue.
- `payment` — one institution's handle on the interbank network. SEPA CT and DD,
  `Initiated → Accepted → Cleared → Settled` **per copy**. `payment.Networks`
  mints one `Network` per institution.
- `iso20022`, `mesh` — the messages, and the N+2 actors that exchange them.
- `payment/recon` — the reconciliation harness, test-only by convention: the one
  instrument that opens every institution's database at once, precisely because
  no actor in the system may. Its narrow sibling is `payment.Network.Reconcile`,
  one bank over its own database, which reports **breaks** on the reserve and
  **positions with ages** on the clearing suspense, because only the first of the
  two accounts closes into an identity from inside.
- `api`, `store/sqlite`, `store/storetest`, `store/testenv`, `seed`, `web`.

Two numbers that bound the work below: **`Amount` is `int64` and an asset's
scale is capped at 9**, and a deployment is **N+2 databases over three schema
shapes** (`bank`, `csm`, `centralbank`), one `0001` migration apiece.

## How this list is ordered

Four groups, and only the first two are sequenced against each other.

1. **Defects** — small, verified against the current tree, and each one is a
   thing the system gets wrong rather than a thing it does not have.
2. **The build sequence** — the payments and settlement arc, §1 through §6,
   where each item genuinely wants the one before it.
3. **Domain gaps worth building** — a standing catalogue, ranked by value per
   unit of effort. Take from the top; nothing here blocks anything there.
4. **Structural work** — deepening opportunities in the code itself, ranked. Each
   is a refactor with a stated win, and none is urgent.

Then the loose ends, then what is deferred on purpose.

---

## Defects

Each verified against the current tree rather than carried on trust.

### `ReverseTransactionTx` skips every check `PostTransactionTx` makes

`ledger/book.go:787-856` builds mirrored entries and writes straight to
`tx.PutTransaction`, bypassing the empty-transaction guard, the `Amount <= 0`
guard, `validateBalance`, `tx.LockAccounts` and `checkSufficientBalance`.
Reversing a receipt into an Asset account whose balance has since been spent
posts a negative Asset balance that `PostTransaction` refuses. `book_test.go`
only reverses Liability↔Liability legs, which is why nothing has caught it.

The double-reversal half of this is closed — `MarkReversed` is conditional in the
store, so two concurrent reversals cannot both succeed. The balance check is not.

### One value-date defect, documented twice and fixed nowhere

`deposit/register.go:1892` and `lending/accrual.go:554` each describe interest
accruing on interest earned the same day, and each points at the other. The fix
both comments propose — value-date capitalisation to the *next* day — changes
interest figures, so seed expectations, api tests and possibly README and quiz
claims move with it.

**It is a domain decision and must not ride along inside a refactor.** That is
why the shared-accrual extraction stopped short of it.

### `GetPaymentByEndToEndID` is ambiguous by construction

`payments_end_to_end_idx` is not unique, so two payments may hold the same
non-empty reference, and the read answers with `ORDER BY seq LIMIT 1` — first
inserted wins, silently. Contrast the ledger's equivalent, where the index *is*
unique and the collision is a documented sentinel. Either make the ambiguity a
sentinel too, or pin the current behaviour with a test that builds two payments
claiming one reference; today nothing does.

### `respond.go:20` puts a raw `err.Error()` in 500 bodies

Where `recoverPanic` is careful to emit a fixed string. One of the two is wrong
about what a 500 may disclose.

---

## The build sequence

1. **7c, the message log.** The last piece of sub-project 7, and it makes every
   flow already shipped visible.
2. **Instant payments.** The one place the settlement orchestrator has to grow.
3. **Card transactions.** Additive on top of holds and the existing net path.
4. **Reserve adequacy.** Wanted by both 2 and 3, and worth little before either.
5. **Crypto**, then **FX** — the two undecided-scope domains, largest last.

### The on-us book transfer — `done`

`deposit.Register.TransferTx`, and `POST /transfers` on a bank's own port. It is
the `deposit` layer's act rather than the `payment` layer's, because nothing
about it needs to know what an institution is: it touches the two customers' own
GL accounts, and a register spans one book. `Mesh.Submit` refuses the on-us
*payment* unchanged — the two are different products and the payer chooses one.

What it does NOT cover is the standing order: a transfer that happens on a date
has a lifecycle of its own, and nothing here holds an instruction between the day
it is given and the day it runs.

Two defects found under sub-project 8 remain reachable only through an
arrangement `Mesh.Submit` refuses, and this work did not change that —
`AcceptInboundTx`'s witness is the ROW, which is false when the same bank also
submitted, and `PostReturnLegTx` decides which leg is last by position in a
conversation one institution does not have. Both are closed and both are still
proved through the store rather than through a path a caller can reach.

### 1. 7c, the message log — `todo`

The last of sub-project 7, and the only reason §7 is not `done`. Envelopes
persisted so a payment screen can show the XML that actually moved, carried
through README, hints and quiz.

Cheap relative to what it exposes: every flow already shipped becomes something a
reader can watch rather than infer. It is also the natural home for anything that
wants to explain a `pacs.002` reason code at the point it was returned.

### 2. Instant payments — `todo`

Real-time **gross** settlement, 24/7. Each payment settles individually and
immediately instead of being batched into a clearing cycle. (*Instant* and
*gross* are independent properties: UK Faster Payments feels instant to customers
and settles on a deferred **net** basis, so it is not a gross-settlement
example.)

The abstraction is in place and unused — `Scheme` already carries
`SettlementModel` (Net/Gross) and a `SettlementDelay`, and the only consumer of
`SettlementModel()` today is a DTO field. What is needed is a scheme returning
`Gross` with a near-zero delay, and a settlement path that branches on it: post
the debtor leg, the central-bank reserve move and the creditor leg in one shot
per payment, with no netting and no cut-off.

This is the one place the orchestrator genuinely has to grow. `SettleCycle` only
implements the netted path, so `Gross` needs a `SettleNow`-style sibling or a
branch inside settlement — and the choice between those is the spec's to make.

**One design question to settle rather than inherit.** This system is
unconditionally *debit-before-submit*: the payer's account moves when the payment
is submitted. The alternative — earmark at submission, post on settlement — is
the right shape for instant rails, and adopting it for a `Gross` scheme while
keeping the current shape for `Net` is a real fork in the payment layer, not a
detail. The earmark primitive already exists: it is a `deposit` hold.

### 3. Card transactions — `todo`

An **authorise → capture → clear → settle** flow. The authorisation is a
`deposit` hold (`CreateHold`) reserving the cardholder's available balance;
capture (`CaptureHold`) turns it into the debtor leg; clearing and settlement
reuse the existing net machinery, since card networks net much like SEPA.

Slots in without new settlement machinery now that holds live in `deposit`: a
card scheme drives holds while the `payment` network clears and settles the
captured amounts. A card-processor persona in the web app is the matching
front-end work, recorded as out of scope by 6b and unclaimed since.

Wants the **hold expiry sweep** below first, or ships a hold state machine that
is less honest than it looks.

### 4. Reserve adequacy — `todo`

Check a bank's reserves before its net settlement is allowed to post. Motivated
by both of the two items above and worth little before either: today every
settlement the clearing house instructs is either posted or refused whole by the
agent, and a bank that cannot fund its position is a case the seed cannot build.

Adjacent and deliberately absent, worth pricing here: liquidity management, and
the `camt.051` that would pull liquidity back the other way. **This system lodges
cash and never withdraws it**, which is the current reason `camt.051` is refused
by name in `iso20022/doc.go`.

### 5. Crypto — `todo`

Scope deliberately undecided. Ranges from custody balances denominated in
non-fiat units — nearly free today, BTC is a known asset at scale 8 and works —
through to on-chain settlement and stablecoin rails, which is a
payment-network-sized project of its own. Pin the range down before the spec.

**The scale cap of 9 is a hard boundary on the scope**, and it lands on the most
obvious next asset: ether and most ERC-20 tokens are 18-decimal, and `int64`
holds 9.2 ETH at native precision. Any scope including the Ethereum family
chooses one of:

- hold them at reduced precision (list at scale 9, discard the last nine places)
  and document the truncation at the boundary; or
- widen `Amount` to a 128-bit type — a compiler-guided change rather than an
  audit, which is why `Amount` is a defined type.

**Make that choice in the spec, not during implementation.**

Two things that shrink the work: a new scheme in a new asset is a data change
rather than a schema change, because a participant's internal accounts hang off a
child table keyed `(participant_id, asset)`; and `interest` is asset-agnostic, so
a crypto-denominated facility needs no new arithmetic — only an asset inside the
cap.

### 6. FX / exchange — `todo`

Trades against two assets, rate handling, spread recognized as revenue, position
and exposure accounts, settlement conventions.

Per-asset balancing makes the naive two-asset posting impossible, which forces
each side of a trade through a position account of its own asset, which is what
keeps rates out of the ledger. Three decisions the spec has to make rather than
discover:

- **What account type is a position account?** `checkSufficientBalance` refuses
  to let an **Asset** or **Expense** account go below zero, and a position
  account must be able to go negative — that is what being *short* an asset
  means. So it is modelled on the Liability/Equity side, or the balance rule
  needs an explicit exemption.
- **The spread is per asset too.** A revenue account is denominated like every
  other, so FX revenue is earned in a specific asset, and which one is a real
  decision: the asset sold, a reporting currency, or both via a further trade.
- **Revaluation is above the ledger.** Position balances are quantities, not
  values. Turning them into a P&L figure needs a rate, so it belongs in the layer
  that quotes trades and should stay out of `ledger`.

There is an FX exposure in the system today, unrecorded: nothing stops a bank
disbursing a facility in an asset it does not fund itself in. Funding the
facility through a position account in the lending asset is what would make that
short a number someone can see.

---

## Domain gaps worth building

Ranked by value per unit of effort. Nothing here blocks the sequence above, and
the first four are each a self-contained afternoon-to-a-week.

### Trial balance — designed, not built

Per book **and per asset**, because a single-asset total in a multi-asset ledger
balances by accident.

The shape is worth not re-deriving: `ledger/trialbalance.go`, a walk over
`tx.ListAccounts` inside ONE `View` summing each account restated debit-positive
— `TrialBalance{AsOf, Rows}`, `TrialBalanceRow{Asset, Debits, Credits,
InFlight}`, `Balanced() bool`, and `Book.TrialBalance` / `TrialBalanceTx`. No
store method and no schema change.

In a system where every posting is forced to balance, a trial balance can never
fail arithmetically — **which is exactly why it is worth computing.** It is a
control on the pipeline, not on the arithmetic, and it would catch a bad
migration or a direct store write that nothing currently catches.

### Reject future booking dates — designed, not built

Postings dated in the future are a different object — a scheduled instruction —
and must not sit in the ledger pretending to be facts. Today
`POST /participants/{pid}/transactions` accepts any caller-supplied
`BookingDate`, including tomorrow's.

Designed as a refusal inside `PostTransactionTx` (`ledger.ErrFutureBookingDate`)
plus an exported `Book.CheckBookingDate` that the two end-of-day batches
(`deposit.RunEndOfDayTx`, `lending.RunEndOfDayTx`) call ONCE before their loops
rather than per posting.

**The task order is the part worth keeping:** advance every fixture that books
into the future BEFORE the rule exists, so the fixture churn and the rule land as
separate, reviewable changes.

### Hold expiry sweep

`HoldStatus` has no expired state. An expired hold stops counting toward `Holds`
in `availableTx` but keeps `Active` status and emits no event. The arithmetic is
right; the state machine is less honest than it looks, and population-by-age
metering is impossible.

Expiry deserves active engineering rather than a comment in the schema: a sweep
that transitions state and emits `hold.expired`. Wanted by card transactions.

### A savings product

The one change that would make the deposit layer a real mirror of lending.
`interest` runs only on the asset side today — `deposit.AccrueOverdraft` and
`lending` both debit a receivable and credit interest income, the bank earning.
The liability-side accrual — debit interest **expense**, credit accrued interest
**payable**, then capitalise to the customer — has no implementation at all.

Cheap, because the arithmetic is asset-agnostic and already tested. It is also
what makes the README's claim that lending "is the mirror image of the deposit
layer" true rather than half-true.

### Allocation-order policy

`lending.RepayTx` hardcodes interest-before-principal. The order is statute:
§367(1) BGB gives **costs → interest → principal** as the default, and §497(3)
BGB inverts it for *consumer loans in default* — **costs → principal →
interest**, deliberately, so a struggling consumer's payments shrink the
interest-bearing base first. Default interest does not compound and is tracked
separately.

A hardcoded allocation order is a latent compliance defect: it is wrong for
someone the moment a second jurisdiction books its first consumer loan. The shape
that fixes it is typed open-item claim buckets with a priority policy pinned to
the product version. This is the sharpest available criticism of something the
repository actually implements, as opposed to omits.

### Idempotency should replay the outcome

Today a duplicate key returns `ErrDuplicateIdempotencyKey` and the caller is told
to look up the original transaction. That is defensible for a library, and it
does not give the caller the property idempotency exists for: an unconditionally
safe retry.

The shape: store the *outcome* — response, resulting journal id, and a hash of
the request payload. On a key hit with a matching hash, replay the stored
response; on a key hit with a different payload, reject loudly, because the
client has a bug. **Rejections are stored too** — if the first attempt failed on
insufficient funds, the retry of that key returns insufficient funds even if the
account has since been topped up. Anything else makes outcomes depend on retry
timing, which is the nondeterminism idempotency exists to kill.

### A business date

There is no business date, no roll event, no banking calendar and no TARGET or
holiday handling anywhere. `PostTransactionTx` assigns `bookingDate = s.now()`
when the caller supplies none.

Booking dates should advance by an explicit, logged business-date roll rather
than by the server clock crossing midnight — assigned once at command acceptance
and carried with the command, so **nothing downstream calls `today()` for
accounting purposes**. The worked case this gets wrong today is a transfer
submitted after cut-off before a holiday weekend; `lending.AddMonths` and the
schedule generator will silently disagree with any calendar.

Pairs with the `BusinessDate` type under *Structural work* — same problem
approached from the code side, and doing either alone leaves the other's cost
in place.

### Blocks as rows, not a status field

One `AccountStatus` (Active/Dormant/Frozen/Closed) cannot stack and cannot carry
direction. Blocks want to be rows with a typed reason, an authority, a legal
basis, a **scope** (single transaction / amount / whole account / whole customer)
and a **direction** (debit-only / credit-only / both) — because an amount-based
hold cannot express "no debits of any kind, but credits post normally", which is
what an investigation block usually wants, and because releasing one block must
not release the others.

The posting-attempt evaluation order this implies is half-implemented already:
lifecycle state → blocks → mandate/signatory → limits → available balance. It is
also what would let the freeze stop being *only* a debit block, which is
currently a deliberate limitation stated in the README.

### Fees

The README's own usage example posts a transfer fee by hand. The fee leg belongs
**in the same journal as the payment** — not as a second, later transaction that
might fail independently and leave revenue uncollected.

Worth having with it, because misclassifying adjustments as corrections quietly
falsifies both: a fee **reversal** debits fee income (the charge was an error); a
goodwill **waiver** debits a goodwill expense account (the charge was valid and
the bank chose to give it back).

### The remaining R-transactions

Reject (pre-clearing) and Return (post-settlement) are built. The full set is six
distinct flows with different initiators, timelines, messages and posting
consequences: reject, return, **refund**, **refusal/cancellation request**,
**reversal**, **recall**.

The rule the existing two already follow and that the missing four must keep: an
exception leg is a new linked event, never a reversal of the original. A naive
"reverse the original journal" on a recall returned net of a handling fee posts
the gross amount and leaves the fee as a hole in the clearing account that
reconciliation finds weeks later.

**`pacs.007` has a caller waiting for it.** Task 19c's ageing report finds
unclaimed balances a bank cannot clear, and on a **pull** the bank holding one is
the creditor's while `ReturnerOf` names the debtor's bank the returner —
correctly, since a `pacs.004` on a pull is the payer's bank's instrument and the
payer has asked for nothing. A reversal is what the creditor's bank actually
wants, and the report blocks the lot by name until there is one
(`TestAnUnclaimedBalanceOnAPullHasNoBankThatMayReturnIt`).

That test measures the second half too, and it is not a defect with a fix so much
as a boundary worth knowing: **`PostReturnLeg` does not refuse the creditor's
bank on a pull**, because that bank genuinely holds the clawback leg — the
refusal belongs to the returner and only on a push. Called with no `pacs.004`
behind it, it releases the unclaimed balance into that bank's own clearing
suspense and takes its copy to `Returned` while every other institution still
says `Settled`. The message is the authorisation, exactly as with a forged
`pacs.002`; what catches an act nobody asked for is the channel plus
reconciliation, and `payment/recon` reports it as a break where the bank's own
instrument can only report a position.

### Maker-checker, and an audit log with an actor

Large, and blocked on the authn that does not exist — `ledger.AuditEvent.Actor`
is documented as "empty until authentication exists". Two things are unmet by
construction until it does:

- **`POST /participants/{pid}/transactions` is an unrestricted free-form posting
  endpoint** — by construction it can do anything the ledger can represent, which
  makes it the single most dangerous capability in the bank and the first entry
  on any dual-control list. The implementation notes worth keeping for when it
  lands: self-approval blocked **at the API layer regardless of roles held**; the
  checker approves a **payload hash**, so any post-submission edit voids the
  approval; pending actions expire; rejections are first-class records.
- **Failed and rejected attempts are not recorded.** Audit events are written
  only inside the successful transaction — correct for accounting completeness,
  and the README argues it well. But a pattern of rejected self-approvals is
  exactly what an investigator needs to see, and it is precisely what a log wired
  only to the happy path misses. The resolution is two logs with different
  retention and access, joined by a command identifier — not one log doing both.

Publication already ships a weaker substitute worth not confusing with this:
**forward-only publication** of product versions, where a version effective
before today is refused outright, so a mispublished rate cannot move interest
already charged across a whole book.

---

## Structural work

Deepening opportunities in the code, ranked. Each is a refactor with a stated
win; none is urgent, and the first is the one the others keep tripping over.

### Collapse the `X` / `XTx` doubling

**77 `…Tx` sibling methods.** Nearly every one is a six-line closure with no
caller outside its own package: every domain operation exists twice, one form
opening a unit of work and its twin running inside one.

Nothing types the difference. The rule is prose and the failure mode is a
deadlock. **Solution: `Tx`-first** — every domain method takes a `Tx`, and one
`WithTx` helper above the domain opens and commits. The two colourings collapse
into one and nesting becomes unrepresentable.

Same cause, visible cost today: `handleListFacilities` opens two store
transactions *per facility* and reads each facility row three times, because
`Drawn` and `RefundPayableFor` each own their own `View`.

### Cross-layer composition needs an owner

`handleRepay` (`api/handlers_lending.go:274`) opens a `deposit.Store` transaction
inside an HTTP handler, then downcasts it with `tx.(lending.Tx)` and a
hand-written error string to reach the lending layer. `payment/bank.go:401` is
the same five lines with a different prefix. Both error paths are unreachable by
construction — `payment.Tx` embeds `lending.Tx` — so a static fact is checked at
runtime, twice. `deposit.Register.Store()` (`register.go:88`) exists only to
permit this.

The composition belongs on the type that holds the `payment.Store` whose `Tx`
already spans every layer. **That type has to be named first**: this was designed
as `Participant.Repay`, and `payment.Participant` was dissolved into `Bank`,
`SettlementMember` and `RosterEntry`. `Bank` is the candidate.

### A `BusinessDate` type in `ledger`

`ledger` treats value date as first class. `deposit` and `lending` thread a bare
`time.Time` through ~38 signatures under four different names, and pay for it:

- **Six independent truncations of one value in a single call chain.**
- **Two hand-rolled comparison functions**, because `==` on `time.Time` compares
  monotonic reading and location. Both carry a paragraph explaining why.
- **`deposit.SnapshotDateKey` is a fourth day-identity rule** and does not
  `.UTC()` first, so it disagrees with `DayStart` for non-UTC input.
- **`Disburse`, `Draw` and `CaptureHold` take no date at all** and post at
  `p.now()`, so they cannot be backdated — while `Repay`, `RefundInterest`,
  `Accrue` and both capitalisations can.

A day-granular, UTC-normalised-at-construction type with `Start()`, `NextDay()`,
`Key()` and value equality. Thread the type, not the instant, and a missing date
becomes a compile error. Cheaper today than at any later point.

### Deepen the transport module

65% of `api`'s handler lines are the same five steps — participant guard,
`decodeJSON`, domain call, `writeError`, `writeJSON` — and 28 of 70 handlers are
nothing else. The same six-line transaction tail appears nine times and the same
`YYYY-MM-DD` parse seven.

The DTO layer is not purely mapping either: `toFacilityDTO` re-implements
`lending.Outstanding`, the wire number comes from the `api` copy, and nothing
asserts the two agree; `entryAssets` does I/O from `dto_ledger.go`, contradicting
`api/doc.go`.

One typed handler adapter absorbing guard/decode/status/encode, plus
`writeTransaction` and `parseDate` absorbing the nine and seven copies, plus
`toFacilityDTO` calling `Outstanding` instead of redefining it.

### Move the derived balances off the store seam

The `Tx` seam carries ~57 methods, 70% of them Put/Get/List pass-through. The
cost is in the rest: eleven computations expressed twice, in two languages, with
contract prose as the only link — listing order, both balances, the series,
`ListTransactionsForAccount`, `ActiveHoldTotal`, both uniqueness claims,
`GetOpenCycle`, the subledger block. "Sign an entry by its account's normal
direction" is written five times. Adding `ValueDatedSeries` cost 88 lines of
implementation and 103 lines of shared-suite test.

**The reason this was filed as speculative has expired.** It traded away
Postgres's index-backed `SUM`, and there is no Postgres. What is left to weigh is
SQLite's aggregate against a Go scan over streamed entries — a measurement, not
an argument. Measure it before deciding, on a file rather than an ephemeral
database.

### `Scheme` — an interface with seven constant returns

Eight methods, seven of them constant returns; `SCT` and `SDD` are fourteen
one-line methods returning literals, and the entire behavioural content is two
`Validate` bodies plus `validateFunds`. `RegisterScheme` has no external caller.
The real invariant was already hoisted *out* of `Validate`, so the interface is
known to be the wrong place for it.

**Do not act on this before instant payments.** `SettlementModel()` had no
consumer at all when this was written and now has exactly one, a DTO field —
nothing branches on it. A third scheme with a genuinely different settlement
model is what would let the seam earn itself, and §2 of the sequence is that
scheme. Re-ask afterwards.

---

## Loose ends

Not sequenced. Each is small, each is recorded rather than dropped, and the
grouping is by what kind of debt it is.

**Correctness**

- **Four known defects carried since Task 18a.** The *count* is recorded and the
  enumeration is not. Recovering them from the branch history is step zero of
  whatever picks this up — `payment/recon` is now the instrument that would
  detect the last of them.
- **`csm.held` does not survive a restart.** Task 16's in-memory hold. The
  contrast that names the fix is one flow over: a message carrying its own
  parties needs no state behind it.
- **The proxy caches a failed read.** "A failed read must not be cached" is a
  correctness claim in `web/`'s route layer with no test, against 6b's own rule
  that route-handler logic belongs in a plain `.ts` module.
- **Three places render a failure as something other than a failure**: the back
  office's `BalanceCard` (`?? 0` on error), `bank/[pid]/layout.tsx` (the whole
  console gated on `useParticipant` with no loading/error split), and the
  customer activity screen's *Try again*, which refetches the statement when it
  was the account that failed.

**Test debt**

- **No golden file is schema-validated.** `iso20022/testdata/xsd/` is absent for
  licensing reasons and `TestGoldenFilesValidateAgainstTheSchema` skips every
  subtest. A skip is not a pass, and the `camt.053` inherits the gap.
- **`ErrSchemeUnsupportedReturn` is documented and untested.**
- **The async creditor back-fill is unreachable over HTTP** — a push quoting no
  creditor address is refused `422` — so the path it exists for has no test that
  drives it the way a caller would.
- **Two web provisioning predicates are untested.** `probeSettled` and
  `useIsProvisioned` are pure functions of query state, both shipped wrong once,
  and neither is reachable by the node-only vitest until it is lifted out of
  `hooks.ts`. Same for `/api/operators`' aggregation and the proxy's `resolve()`.

**Navigation and consistency**

- **`/clearing-house/settlements` reads the central bank's port.** A navigation
  decision, noted where it will be met rather than fixed in passing.
- **The lobby reads the central bank's listener** (`useReserves` in
  `app/page.tsx`) — after the clearing house's subtraction, the last
  cross-persona read in the app, and unlike the identity picker it carries no
  comment admitting it is scaffolding.
- **`/clearing-house` and the lobby disagree about the same predicate.** The
  participant cards link optimistically while the probe is in flight; the lobby
  holds a skeleton until it settles.

**Documentation debt**

- **The other half of the no-`CHECK` finding.** Neither the README nor the schema
  says that a single-backend *production* bank would be advised the opposite —
  defence in depth, deferred balance triggers, an application role holding INSERT
  and SELECT on posting tables and no UPDATE or DELETE, which is the first thing
  a competent IT auditor checks. The ruling here rests on a reason that never
  named a store (a `CHECK` puts the asset set in two places and makes adding one
  a migration); what is missing is the contrast, so a reader does not take
  "validation belongs in the domain layer" for the industry position.
- **Reversal value-dating is right and unexplained.** A reversal carries the
  original's value date and today's booking date, which is exactly the
  prescription — and the reason is not written down anywhere: the customer's
  value-dated balance series must look as if the error never happened.

**Deliberate trades with a named successor**

- **The recompute window is unbounded, by choice.** It opens at account opening
  for a deposit overdraft and at origination for a lending facility, and nothing
  resets it, so a long-lived account walks more days of arithmetic every night. A
  window at inception is what makes a backdated posting correct across a
  repricing. The successor is **checkpointing** — anchored to a ledger sequence
  position rather than a wall clock, invalidated by a backdated posting, reading
  the end-of-day snapshots the system already writes. It is also what would close
  the product catalogue's deferred resolution log.
- **`interest.Recompute` is not defensive.** On a window that has not advanced it
  hands the whole record back as a correction; both callers guard against it, so
  it is a documented precondition with a test rather than a live bug. Making the
  function defensive instead is a contract change, not a refactor.
- **`interest.AccrueSeries` is exported with no external caller.** It is the
  primitive `Recompute` is built from, independently meaningful, and has a
  270-line test suite of its own. Unexporting it is a separate mechanical change.

## Deferred by design

Recorded so a reader who expects them knows they were priced, not missed. None is
scheduled.

**Lending.** Non-accrual and NPL accounting — a non-performing facility keeps
accruing into income exactly as a performing one does, `NonPerforming` marks only
— expected-credit-loss provisioning and IFRS 9 staging, write-off versus
derecognition, forbearance with probation clocks, default interest as a separate
accrual stream, fees, early-repayment compensation, and restructuring.
Restructuring includes capitalizing unpaid interest on a delinquent term loan,
which `ChargeInterest` refuses outright with `ErrWrongFacilityKind`, precisely
because folding unpaid interest into principal on a signed, scheduled contract is
a decision this system does not make for you.

**Product catalogue.** Open by choice: the exclusion constraint (replaced by a
`(product, day)` key, which makes the row in force on a day unique by
construction rather than by a constraint), the resolution log (deferred to
checkpointing above), maker-checker on publication (replaced by forward-only
publication), and lending's binding — a term loan cannot float at all, so a
term-loan product version would be a pure origination default, and two binding
semantics under one type in one pass is the ambiguity that makes a design rot.
Also absent: fee schedules, tiered and bonus rates, and capitalisation frequency.
A catalogue carrying only the parameters the code reads is the honest version.

**Account addressing.** A proxy-alias registry and a second identifier scheme.
Two things have shipped. Format validation: an address is an ISO 13616 IBAN in
four countries, with mod-97-10 and, where the country has one of its own, Italy's
CIN or France's clé RIB. And **routing**: a bank code is allocated by a registry
at admission, published on the clearing house's roster, copied by each member
into its own `routing_directory`, and derived from at submission — so an
instruction carries an address and a name and names no bank at all.

What is still absent is the rest of the registry — eighty-odd countries, which is
licensed reference data — and **virtual IBANs**, where a PSP issues addresses
under another institution's bank-code range. That case breaks "the bank code
identifies the account holder's bank", which the whole routing design rests on,
and it is a live regulatory argument in SEPA rather than a hypothetical.

**Verification of Payee** is the natural sequel and is not here. Once a payer
stops typing a BIC, the NAME is the only thing they assert, which is exactly why
the Instant Payments Regulation made name-checking mandatory across SEPA in
October 2025. It has a real message pair — `acmt.023` IdentificationVerification-
Request and `acmt.024` IdentificationVerificationReport — and it would be the
first sanctioned crack in "no bank reads another bank's register": a bounded
question with a bounded answer. That is a rule reversal, and rule reversals get
their own commit.

**ISO 20022.** `pain.001`/`pain.008` customer initiation, `camt.056`/`pacs.007`
recalls and reversals, runtime XSD validation, and message signing. Two are
refused *specifically* rather than by family, in `iso20022/doc.go`: `camt.054`,
because a notification carries no balance and therefore cannot detect a wrong
posting, and `camt.051`, because this system lodges cash and never withdraws it.
Reachable for the first time but unclaimed: batched bulks — which is what would
exercise `pacs.002`'s `GrpSts: PART`, built and unused — a `pacs.028` status
request, and a cutoff timer.

**A party model.** No party, customer or account-holder entity; a
`deposit.Account` has a `Name string` and one person's accounts are not linked.
This is the most expensive omission here if it is ever wanted — retrofitting the
separation is one of the hardest migrations in banking, and everything downstream
depends on it: joint accounts as disposal rights on holder roles rather than an
account subtype, minors and guardians, powers of attorney, beneficial owners, the
deposit-guarantee single-customer view, per-customer-per-year interest
certificates over per-account postings, and customer-level limits, where
splitting one person into two parties doubles their effective limit. Deferred on
scope, not on merit.

**A thin GL** with per-key-per-day aggregation and a posting-rules engine, and
with it control accounts, period close, a closed-period lock and
subledger-to-GL reconciliation. This system is one unified ledger on purpose; see
the README's *Ledger and Subledger Hierarchy*, which states the trade.

**Everywhere else.** Real authn/authz — the port is the claim and nothing
verifies the caller. Separate processes, two-phase commit, and any distributed
transaction manager: if settlement cannot be made final in one book and mirrored
asynchronously into the others, the design is wrong rather than under-powered. A
second store backend — the rule *"where the two stores disagree, `store/mem` is
right by definition"* has no successor. Dynamic port allocation, so a bank
admitted at runtime gets no listener until restart, which is a decision about
what admission means. Regulatory reporting, withholding tax, certificates, GDPR
versus immutability, DORA, AML and sanctions, securities, and treasury.

## Shipped

One line apiece, for resolving a reference by number. The spec is the record.

| # | | Spec |
|---|---|---|
| 1 | Multi-asset ledger core | [`2026-07-27-multi-asset-ledger-design.md`](superpowers/specs/2026-07-27-multi-asset-ledger-design.md) |
| 2 | Lending — `interest`, `lending`, three products | [`2026-07-27-lending-design.md`](superpowers/specs/2026-07-27-lending-design.md) |
| 3 | Crypto | *above* |
| 4 | FX / exchange | *above* |
| 5 | Account addressing — `(scheme, value)` identifiers | [`2026-07-31-account-addressing-design.md`](superpowers/specs/2026-07-31-account-addressing-design.md) |
| 6a | Operator-split API — one listener per institution | [`2026-07-31-operator-split-api-design.md`](superpowers/specs/2026-07-31-operator-split-api-design.md) |
| 6b | Role-scoped web UI — four personas | [`2026-07-31-role-scoped-web-ui-design.md`](superpowers/specs/2026-07-31-role-scoped-web-ui-design.md) |
| 7a | The `iso20022` package | [`2026-07-31-iso20022-messages-design.md`](superpowers/specs/2026-07-31-iso20022-messages-design.md) |
| 7b | The mesh and its N+2 actors | [`2026-08-01-iso20022-mesh-design.md`](superpowers/specs/2026-08-01-iso20022-mesh-design.md) |
| 7c | The message log | *above* |
| 8 | Per-entity stores — N+2 databases, three shapes | [`2026-08-02-db-per-entity-design.md`](superpowers/specs/2026-08-02-db-per-entity-design.md) |
| 9 | One store, and it is SQLite | [`2026-08-03-sqlite-only-store-design.md`](superpowers/specs/2026-08-03-sqlite-only-store-design.md) |
| 19 | Reconciliation a bank can do for itself | [`2026-08-09-bank-reconciliation-design.md`](superpowers/specs/2026-08-09-bank-reconciliation-design.md) |

Sub-projects 3, 4 and 7c are the only numbers with work left; §9 was not foreseen
and landed inside §8's task sequence, which is why the numbering skips. The last
row is a TASK number rather than a sub-project one: 19 continues §8's task
sequence, as the work that sub-project deliberately left, and it is listed here
because it has a spec of its own to resolve a reference to.

Four other design records sit alongside these and are not numbered, because each
was one topic taken from a comparison rather than a sub-project:
`2026-07-26-audit-log-postgres-design.md`, `2026-07-28-value-dated-balance-design.md`,
`2026-07-30-effective-dated-terms-design.md` and
`2026-07-30-product-catalogue-design.md`.
