# Expansion Roadmap

What is left to build, in the order it should be built.

This file is forward-looking. What each shipped sub-project decided, reversed and
learned is in its spec under `docs/specs/` and in `git log`; the index at the
bottom is only enough to resolve a cross-reference by number. A design record
holds the decision, the alternative it rejected and what that cost, and there is
no separate ruling record.

**Status legend:** `todo` · `spec` (design agreed, spec written) · `plan`
(implementation plan written) · `wip`

## Goal and framing

The repository stays a **teaching reference**, but the backend should be
polished and behave like a real core banking system: no toy shortcuts in the
accounting core, correct invariants enforced at the boundary, and every new
domain carried through all layers the way the existing ones are.

Three constraints apply to everything below.

- **Domain knowledge stays consistent across layers.** `README.md` is
  authoritative; `CONTEXT.md`, `web/src/components/hint-content.ts`, the quiz
  chapters under `web/src/lib/quiz/chapters/`, and the schema comments in
  `store/sqlite/schema/` all have to move with it. A decision that outlives its
  sub-project goes in its design record. See `CLAUDE.md`.
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
- `iso20022`, `ebics` — the messages, and the file transport that carries them.
  `ebics` carries bytes and knows no ISO 20022: order types, order ids, return
  codes and one ordered download queue per enrolled subscriber. Nothing is ever
  pushed at a member bank.
- `calendar` — the TARGET settlement calendar and the deployment's clock. Which
  days money can move on, and what day this deployment thinks it is.
- `cmd/server` — the three institutions, the composition root and the business
  day. They are this deployment's orchestration rather than a library some other
  system could use, which is what puts them in a command; which institutions a
  deployment has is that deployment's business and not a library's.
- `payment/flow` — the four conversations between institutions, in the order the
  messages travel, for a caller that IS the deployment: the seed and the suites.
- `payment/recon` — the reconciliation harness, test-only by convention: the one
  instrument that opens every institution's database at once, precisely because
  no institution in the system may. Its narrow sibling is `payment.Network.Reconcile`,
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
2. **The build sequence** — the payments and settlement arc, §1 through §8,
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

`ledger/book.go:635-708` builds mirrored entries and writes straight to
`tx.PutTransaction`, bypassing the empty-transaction guard, the `Amount <= 0`
guard, `validateBalance`, `tx.LockAccounts` and `checkSufficientBalance`.
Reversing a receipt into an Asset account whose balance has since been spent
posts a negative Asset balance that `PostTransaction` refuses. `book_test.go`
only reverses Liability↔Liability legs, which is why nothing has caught it.

The double-reversal half of this is closed — `MarkReversed` is conditional in the
store, so two concurrent reversals cannot both succeed. The balance check is not.

### Interest accrues on interest earned the same day

Both capitalisations post `ValueDate: date` — `deposit/register.go:1391` and
`lending/accrual.go:280` — so the span ending on that day is re-priced over a
balance that already includes the charge. Each site states the consequence and
neither claims it is right.

The fix is value-date capitalisation to the *next* day, and it changes interest
figures, so seed expectations, api tests and possibly README and quiz claims move
with it. **It is a domain decision and must not ride along inside a refactor** —
which is why the shared-accrual extraction stopped short of it.

### `GetPaymentByEndToEndID` is ambiguous by construction

`payments_end_to_end_idx` is not unique, so two payments may hold the same
non-empty reference, and the read answers with `ORDER BY seq LIMIT 1` — first
inserted wins, silently. Contrast the ledger's equivalent, where the index *is*
unique and the collision is a documented sentinel. Either make the ambiguity a
sentinel too, or pin the current behaviour with a test that builds two payments
claiming one reference; today nothing does.

### The seed closes a business day the deployment has not reached

`AdvanceDay` runs each bank's end of day for the day it is LEAVING and then moves
the clock (`beforeClock`'s end-of-day phase, then the pivot). `seed/seed.go:226`
`runDays` does the opposite: `b.day()` advances first, and `RunEndOfDay` is then
called with `b.clock.Now()`, the day just arrived at. Over N days the seed closes
D+1…D+N where a deployment closes D…D+N-1.

It is **not** a correctness defect today — the advancement guards
(`deposit/register.go:1183`, `lending/accrual.go:57`,
`DayCount.Days(LastAccrualDate, date) <= 0`) make the re-close a no-op. Two things
are wrong anyway: `runDays`' own comment claims the seed's accrual moves *"exactly
as a running day would produce them"*, which this makes false; and the first
operator advance after a boot re-closes a day the seed already closed, so it
accrues nothing on any seeded facility.

Fixing it moves interest figures, so it is a domain decision and does not ride
along inside a refactor — the same reason the capitalisation defect above is
filed rather than fixed.

### `api/respond.go:21` puts a raw `err.Error()` in 500 bodies

Where `recoverPanic` is careful to emit a fixed string. One of the two is wrong
about what a 500 may disclose.

---

## The build sequence

**21, EBICS and the business day, has shipped**, and it was first for two
reasons that now hold for everything below: it replaced the transport every item
here is built on, and the business date it brought is a prerequisite the other
four had each been paying for separately.

1. **7c, the message log**, and the per-phase stepping that makes it worth
   watching. The last piece of sub-project 7, and it makes every flow already
   shipped visible. Cheaper after 21, which gives it files to show rather than
   one message per payment.
2. **Scenarios, and a deployment that starts blank.** The seed becomes a base
   state plus triggerable scenarios, every one of which drives the real doors.
3. **A scheduled business day.** A time of day, a cursor, intraday windows and a
   clock that runs — one concept, and 2 has to land first so it has a base date
   to choose.
4. **Instant payments.** The one place the settlement orchestrator has to grow.
5. **Card transactions.** Additive on top of holds and the existing net path.
6. **Reserve adequacy.** Wanted by both 4 and 5, and worth little before either.
7. **Crypto**, then **FX** — the two undecided-scope domains, largest last.

### 1. 7c, the message log — `wip`

[`2026-08-13-the-message-log-design.md`](specs/2026-08-13-the-message-log-design.md).
The last of sub-project 7, and the only reason §7 is not `done`. Envelopes
persisted so a payment screen can show the XML that actually moved, carried
through README, hints and quiz — plus the network view the deployment serves and
the per-phase stepping that makes it worth watching.

Cheap relative to what it exposes: every flow already shipped becomes something a
reader can watch rather than infer. It is also the natural home for anything that
wants to explain a `pacs.002` reason code at the point it was returned.

**Tasks 1–5 have shipped**: the journal records the take as well as the put,
`messages` is a table in all three schemas written at every send and every
receive, each listener serves its own institution's log with the file behind it,
every phase of a business day is a door beside the clock, and the deployment
serves the mesh — a snapshot and an event stream. Tasks 6 and 7 are what is left,
the graph and the document viewer, and both are frontend.

Two things found while scoping it changed what it has to build, and the first of
them is built.

**The journal records the take as well as the put** — task 1.
`node.FileMoved` carries a movement, and every place a file is taken journals
one: both `Collect`s, and both hosts' `Work`, where an upload comes out of the
order log it has been resting in. So a put with no take means one thing, which
is a file waiting in a queue nobody has come for — the gap settle-before-release
exists to teach, and the thing the graph draws.

**The push channel narrows a standing claim** — task 5. A watcher's request does
not return, so this process writes to somebody who did not ask; the README says
so beside its *no background goroutines* claim, and §3's ticker is the second and
larger narrowing. What it buys is that a page is not a poller, which matters
exactly when a clock starts running without a human behind it.

**Nothing could be stepped one hop** — task 4. `csm.Work` was reachable only
from `AdvanceDay`, so a reader could submit or run a whole day and nothing in
between. Every phase the day declares is now a door on the operator surface,
named and never parameterised, and the clock stays where it stood. What it costs
is that stepping and advancing overlap with nothing recording how far the day
has got, which is an argument for §3's re-entrancy audit and cursor.

### 2. Scenarios, and a deployment that starts blank — `spec`

[`2026-08-13-scenarios-and-a-blank-slate-design.md`](specs/2026-08-13-scenarios-and-a-blank-slate-design.md).
Boot leaves banks provisioned, subscribed and prefunded and nothing else; a
payment, a rejection, a return and a borrower in arrears are scenarios an
operator triggers. Every scenario drives the doors an operator has and never
`payment/flow`, whose bypass has already produced one silent defect.

Wants a capital injection that does not exist: vault cash enters only through a
customer deposit, so a bank with no customers has nothing to lodge.

### 3. A scheduled business day — `spec`

[`2026-08-13-a-scheduled-business-day-design.md`](specs/2026-08-13-a-scheduled-business-day-design.md).
Intraday windows, a clock that runs at an accelerated rate, and a discrete
advance that triggers everything due since the last one — one missing concept
behind all three, which README's transport section and
the rule that the deployment owns the clock each name independently:
there is no time of day within a settlement day.

Overturns two standing claims — that no background goroutine runs under this
process, and that a business day is a sequence of *untimed* phases. The largest piece is a re-entrancy answer
for every phase, since a catch-up may re-run one.

### 4. Instant payments — `todo`

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

### 5. Card transactions — `todo`

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

### 6. Reserve adequacy — `todo`

Check a bank's reserves before its net settlement is allowed to post. Motivated
by both of the two items above and worth little before either: today every
settlement the clearing house instructs is either posted or refused whole by the
agent, and a bank that cannot fund its position is a case the seed cannot build.

Adjacent and deliberately absent, worth pricing here: liquidity management, and
the `camt.051` that would pull liquidity back the other way. **This system lodges
cash and never withdraws it**, which is the current reason `camt.051` is refused
by name in `iso20022/doc.go`.

### 7. Crypto — `todo`

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

### 8. FX / exchange — `todo`

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
each is a self-contained afternoon-to-a-week.

### Where a payment is — the lifecycle trail and the hub that holds it — `spec`

[`2026-08-11-payment-whereabouts-design.md`](specs/2026-08-11-payment-whereabouts-design.md).
Web only, and it adds no route, no column and no status: a payment resting in its
bank's hub is rendered nowhere, on any screen, in any persona, and
`GET /payments/pending` has never been called by the frontend. A reader who
initiates a payment and looks for it at the clearing house is told nothing, and
the cycles screen cannot distinguish the cycle that is accepting payments from
the two a day's last phase just opened empty.

Four tasks: a trail per institution's copy read from that institution's own
audit, the pending section on a bank's own screen, three toasts and two empty
states that name the act that moves an instruction, and the `accepting` badge on
the cycles table. Everything it renders is already answerable today.

`hint-content.ts` explains all of it well and behind a `?` the reader has no
reason to open; the defect is placement, not content, so no hint body changes.

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

### The overdraft reclassification — the target account now exists

An overdrawn customer's drawn balance is a loan and advance to that customer, and
a real bank's ledger says so with a nightly journal onto an Asset-side
receivable. This system posts no such thing: the balance stays in the customer's
position within the deposit control account, merely negative, and the Asset-side
total is aggregated `Σ max(0, −balance)` when asked. The README's *Overdraft* states this, and
`deposit.TestTotals_OverdraftsAreDerivedAndNothingIsPosted` pins it.

The reason it is absent has changed, which is why this entry is worth reading
again. It was that there was no target account to post *to* that was not one per
customer — the thing the chart of accounts is kept clear of. Since §20 there is
one: a `Loans and Advances to Customers (<asset>)` control account, with the same
subsidiary dimension on both legs, is an ordinary line to add. What is left is not
the journal but the third decision below.

Three domain decisions, none of them the journal itself:

- **Gross, per account, never netted.** A bank may not net debit balances against
  credit ones, so this is one reclassified position per overdrawn account and not
  one journal for the book's net figure. `deposit.Totals` already says why.
- **How it unwinds.** An account that returns to credit must give the
  classification back, and an incremental journal double-counts the first time a
  backdated posting changes whether a past day was overdrawn. The shape that is
  already right elsewhere is re-derivation: reverse the standing reclass, re-post
  from the value-dated balance, same as `interest.Recompute` does with accrual.
- **What happens to `Totals`.** This is the decision, and it is not optional.
  Once the reclass posts, the Asset control account and `Totals.Overdrafts` are
  two copies of one number — exactly the drift this repository is built without.
  So either `Totals` starts *reading* the control account, or the journal is not
  built. Both cannot stand, and a change that lands the posting while leaving
  `Totals` deriving is the defect this entry exists to prevent.

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

`PostTransactionTx` assigns `bookingDate = s.now()` when the caller supplies
none, and every layer below the deployment reads a clock rather than being handed
a date.

Booking dates should advance by an explicit, logged business-date roll rather
than by the server clock crossing midnight — assigned once at command acceptance
and carried with the command, so **nothing downstream calls `today()` for
accounting purposes**. The worked case this gets wrong today is a transfer
submitted after cut-off before a holiday weekend; `lending.AddMonths` and the
schedule generator will silently disagree with any calendar.

Pairs with the `Day` type under *Structural work* — same problem approached from
the code side, and doing either alone leaves the other's cost in place.

**The roll half shipped with §21**, which gave the deployment a clock, a TARGET
calendar and an explicit advance that runs a business day — see
the rule that the deployment owns the clock. So the first paragraph
above is out of date in one respect and kept because the rest of it is not: there
IS a business date now, and the worked case — a transfer submitted after cut-off
before a holiday weekend — is buildable in the seed.

What stays here is the **threading**: the date assigned at command acceptance and
carried, so nothing downstream calls `today()` for accounting purposes. §21 did
not do it, and made it worth doing — a rule about what nothing downstream may
call needs a roll event to be a rule about, and now there is one to enforce it
against.

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
win, and none is urgent.

### A `Day` type in `ledger`

**Not `BusinessDate`.** That term is taken, and correctly: the business date is
what day the DEPLOYMENT thinks it is, one per deployment, and
`cmd/server.BusinessDate` is it. Most of the values below are not that — a
booking date, a value date, an effective-from and an as-of are all backdatable,
and `CONTEXT.md` already rules that a booking date must not be called a business
date. What is wanted is the day-granular VALUE those are carried in.

`ledger` treats value date as first class. `deposit` and `lending` thread a bare
`time.Time` through 38 exported signatures under four different names, and pay
for it:

- **Six independent truncations of one value in a single call chain.**
- **Two hand-rolled comparison functions**, because `==` on `time.Time` compares
  monotonic reading and location. Both carry a paragraph explaining why.
- **`deposit.SnapshotDateKey` is a fourth day-identity rule** and does not
  `.UTC()` first, so it disagrees with `DayStart` for non-UTC input.
- **`Disburse`, `Draw` and `CaptureHold` take no date at all** and post at
  `p.now()`, so they cannot be backdated — while `Repay`, `RefundInterest`,
  `Accrue` and both capitalisations can.

A day-granular, UTC-normalised-at-construction `ledger.Day` with `Start()`,
`Next()`, `Key()` and value equality. Thread the type, not the instant, and a
missing date becomes a compile error. Cheaper today than at any later point.

### A caller cannot see which order a listing comes back in — `todo`

Found while refusing phase 5 of the derived-balances work, which had assumed
this was a claim written twice. It is written once, in the wrong place: there
are **46 `ORDER BY` clauses** in `store/sqlite` and the domain's store
interfaces state an order in exactly two — `deposit.Tx.ListSnapshotsForAccount`
and `deposit.HoldLister`. Everywhere else the order a caller may rely on is
readable only from the SQL, or from a `storetest` case asserting it.

The fix is to state it, not to move it: sorting in Go what an index already
returns sorted is a cost with no rule behind it. One line per listing, in the
interface, and the store's implementation does not restate it. Most follow one
convention — `<a time column> ASC NULLS FIRST, seq` — and the deviations
(`day_key DESC`, `position`, `bic`, `scheme, value`, `slot, product, asset`)
are the ones worth naming individually.

Nothing enforces it afterwards, which is the same standing as the comment
budget: it holds while it is applied in review.

### `Scheme` — an interface with seven constant returns

Eight methods, seven of them constant returns; `SCT` and `SDD` are fourteen
one-line methods returning literals, and the entire behavioural content is two
`Validate` bodies plus `validateFunds`. `RegisterScheme` has no external caller.
The real invariant was already hoisted *out* of `Validate`, so the interface is
known to be the wrong place for it.

**Do not act on this before instant payments.** `SettlementModel()` had no
consumer at all when this was written and now has exactly one, a DTO field —
nothing branches on it. A third scheme with a genuinely different settlement
model is what would let the seam earn itself, and §3 of the sequence is that
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
- **Two places render a failure as something other than a failure**: the back
  office's `BalanceCard` (`?? 0` on error) and `bank/[pid]/layout.tsx` (the whole
  console gated on `useParticipant` with no loading/error split).

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
Reachable for the first time but unclaimed: a `pacs.028` status request. Batched
bulks and a cutoff timer were listed here too and are §21's task 7, claimed;
`pacs.002`'s `GrpSts: PART` is built and reached, by §21's task 8, at the clearing
house — the only institution that still rejects anything once a cycle settles
before its files are released.

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

**A thin GL, in the half that stores a second figure**: per-key-per-day
aggregation tables, period close, a closed-period lock, and the subledger-to-GL
reconciliation those three make necessary. Control accounts and the posting-rules
mapping are not deferred with them — both shipped in §20 — and separating the two
was the whole of it: a control account implies a stored control figure only if
you store one. What stays here is the stored copy and the toil that follows it.

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
| 1 | Multi-asset ledger core | [`2026-07-27-multi-asset-ledger-design.md`](specs/2026-07-27-multi-asset-ledger-design.md) |
| 2 | Lending — `interest`, `lending`, three products | [`2026-07-27-lending-design.md`](specs/2026-07-27-lending-design.md) |
| 3 | Crypto | *above* |
| 4 | FX / exchange | *above* |
| 5 | Account addressing — `(scheme, value)` identifiers | [`2026-07-31-account-addressing-design.md`](specs/2026-07-31-account-addressing-design.md) |
| 6a | Operator-split API — one listener per institution | [`2026-07-31-operator-split-api-design.md`](specs/2026-07-31-operator-split-api-design.md) |
| 6b | Role-scoped web UI — four personas | [`2026-07-31-role-scoped-web-ui-design.md`](specs/2026-07-31-role-scoped-web-ui-design.md) |
| 7a | The `iso20022` package | [`2026-07-31-iso20022-messages-design.md`](specs/2026-07-31-iso20022-messages-design.md) |
| 7b | The mesh and its N+2 actors | [`2026-08-01-iso20022-mesh-design.md`](specs/2026-08-01-iso20022-mesh-design.md) |
| 7c | The message log | *above* |
| 8 | Per-entity stores — N+2 databases, three shapes | [`2026-08-02-db-per-entity-design.md`](specs/2026-08-02-db-per-entity-design.md) |
| 9 | One store, and it is SQLite | [`2026-08-03-sqlite-only-store-design.md`](specs/2026-08-03-sqlite-only-store-design.md) |
| 19 | Reconciliation a bank can do for itself | [`2026-08-09-bank-reconciliation-design.md`](specs/2026-08-09-bank-reconciliation-design.md) |
| 20 | The subsidiary ledger — customer accounts leave the chart of accounts, and the trial balance that measures it | [`2026-08-10-subsidiary-ledger-design.md`](specs/2026-08-10-subsidiary-ledger-design.md) |
| 21 | EBICS batches, the institutions in `cmd/server`, and the business day | [`2026-08-11-ebics-and-the-business-day-design.md`](specs/2026-08-11-ebics-and-the-business-day-design.md) |

Sub-project 20 shipped the trial balance with it: the report is the acceptance
test for the third task, and a row count bounded by the institution rather than
by the customer base is the observable point of the whole change.

Sub-projects 3, 4 and 7c are the only numbers with work left; §9 was not foreseen
and landed inside §8's task sequence, which is why the numbering skips. The last
row is a TASK number rather than a sub-project one: 19 continues §8's task
sequence, as the work that sub-project deliberately left, and it is listed here
because it has a spec of its own to resolve a reference to.

**The unnumbered records are the rest of what has shipped**, each one topic taken
from a comparison or a review rather than a sub-project. They are listed here
because nothing else in this file points at them.

| | Record |
|---|---|
| The audit log | [`2026-07-26-audit-log-postgres-design.md`](specs/2026-07-26-audit-log-postgres-design.md) |
| Value-dated balances | [`2026-07-28-value-dated-balance-design.md`](specs/2026-07-28-value-dated-balance-design.md) |
| Effective-dated terms | [`2026-07-30-effective-dated-terms-design.md`](specs/2026-07-30-effective-dated-terms-design.md) |
| The product catalogue | [`2026-07-30-product-catalogue-design.md`](specs/2026-07-30-product-catalogue-design.md) |
| Learner-facing docs name no repo symbol | [`2026-08-10-domain-only-learner-docs-design.md`](specs/2026-08-10-domain-only-learner-docs-design.md) |
| Real IBANs and the routing directory | [`2026-08-10-real-ibans-and-routing-directory-design.md`](specs/2026-08-10-real-ibans-and-routing-directory-design.md) |
| Admission is deletable | [`2026-08-11-delete-admission-design.md`](specs/2026-08-11-delete-admission-design.md) |
| What an institution holds when the process ends | [`2026-08-12-held-files-durability-design.md`](specs/2026-08-12-held-files-durability-design.md) |
| A store per institution | [`2026-08-12-store-per-institution-design.md`](specs/2026-08-12-store-per-institution-design.md) |
| The derived balances leave the store seam | [`2026-08-12-derived-balances-off-the-store-seam-design.md`](specs/2026-08-12-derived-balances-off-the-store-seam-design.md) |
| The interbank conversation has an owner | [`2026-08-13-the-interbank-conversation-design.md`](specs/2026-08-13-the-interbank-conversation-design.md) |
| A package per institution | [`2026-08-13-a-package-per-institution-design.md`](specs/2026-08-13-a-package-per-institution-design.md) |
| The routing table is collected, not read | [`2026-08-13-a-roster-download-order-type-design.md`](specs/2026-08-13-a-roster-download-order-type-design.md) |

Three more shipped with no design record of their own — the business day as a
declared sequence, one type per institution, and the `X` / `XTx` doubling. Those,
cross-layer composition, the transport deepening and the on-us book transfer are
in `git log` alone.
