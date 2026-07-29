# Architecture review — 2026-07-30

Deepening opportunities found by walking the Go packages on branch
`value-dated-balance`. Vocabulary is deliberate: **module** (anything with an
interface and an implementation), **interface** (everything a caller must know,
not just the type signature), **seam** (where an interface lives), **depth**
(behaviour per unit of interface), **leverage** (what callers gain), **locality**
(what maintainers gain).

Measured: 21,333 non-test Go lines, 15,492 test lines (of which 3,912 sit in
`store/storetest/*.go`, test code in non-`_test.go` files).

There are no ADRs in this repo. The load-bearing decisions live in doc comments
(`lending/doc.go:11-31`, `lending/portfolio.go:19-23`) and in `CLAUDE.md`. Two
candidates below push against one of those; both say so.

This is also a teaching repo — README, quiz and hint layers restate the domain —
so deleting an abstraction is a documentation change in four layers, not just a
refactor.

## Candidates, in recommended order

### 1. Give `Book` read methods at the `Tx` seam — **Strong** · DONE

`ledger/book.go:25-31,867-901` · `deposit/register.go:930-949` ·
`lending/portfolio.go:506-528` · `lending/refund.go:71-76` ·
`payment/system.go:1312`

`Book` doubles every *mutating* method into a `…Tx` form (`book.go:25-31`
promises the pattern) but gives reads none. Five packages therefore re-derive
"read the account for its normal direction, then aggregate" behind seven private
helpers — and four of them skip the account read and hard-code `ledger.Debit` /
`ledger.Credit` against accounts their own package created. A change to
`AccountType.NormalBalance()` silently sign-flips those four balances instead of
failing.

The `DayStart(from), NextDay(to)` window snapping is duplicated with it
(`deposit/register.go:948`, `lending/portfolio.go:520`).

**Solution.** Add `BookBalanceTx`, `ValueDateBalanceTx` and `SeriesTx` to
`Book`, with the snapping inside `SeriesTx`. The sign rule lives only in
`AccountType.NormalBalance()`.

**Wins.** Locality: one derivation. Deletes deposit's two pass-through helpers
outright; strips the hard-coded direction from lending's four (they stay, because
naming *which* GL account is real domain knowledge). Prerequisite for
candidate 2.

Also fixed a false doc claim: `book.go:880` said `ValueDateBalance` is "the
balance the book's interest engines consume". Neither engine calls it — both go
through the series. README:467 already said so correctly; only the Go doc comment
was stale.

**As implemented.** `BookBalanceTx`, `ValueDateBalanceTx` and `SeriesTx` added to
`Book`, with `BookBalance` / `ValueDateBalance` now delegating to their `Tx`
forms. `SeriesTx` has no plain form — every consumer is already inside a unit of
work, and an unused wrapper is surface with no caller to justify it.

Deviation from the sketch above: deposit's two helpers (`bookBalanceTx`,
`valueDatedSeriesTx`) were pure duplication of `Book`'s two steps and are gone.
Lending's four (`drawnTx`, `drawnSeriesTx`, `receivableTx`, `refundPayableTx`)
were kept — naming *which* GL account answers a question is real domain
knowledge — but their bodies now call `Book` and no longer name a direction. So
the count is 2 deleted, 4 stripped, not 7 deleted.

`ledger.Debit` / `ledger.Credit` now appear in a derived read in exactly one
place, `ledger/book.go`, plus `store/storetest`, which tests the store contract
below `Book` deliberately. Verified green on both stores.

### 2. One accrual engine, not two — **Strong** · PHASE A DONE

> **Correction to this entry.** It originally claimed "~250 duplicated lines
> absorbed". That was too optimistic, and reading the two paths closely showed
> why: the recompute core is genuinely shared, but the correction split and the
> capitalisation are *semantically* divergent, and `lending/accrual.go:209-211`
> already says so — "deposit's equivalent adds back its `refund` because there
> the remainder goes one place and nothing clamps it; here it goes two". The real
> shareable surface is ~50–60 lines of logic plus ~60 of duplicated prose. See
> *Phases* below.


`deposit/register.go:1016-1387` · `lending/accrual.go:78-567` ·
`interest/series.go`

Implemented twice, structurally line-for-line:

| concern | deposit | lending |
| --- | --- | --- |
| recompute-and-post-delta (10 steps, same order) | `register.go:1105-1154` | `accrual.go:90-140` |
| guards before it | `register.go:1094-1103` | `accrual.go:79-88` |
| day-by-day walk | `register.go:1250-1257` | `accrual.go:296-303` |
| correction split | `register.go:1183-1227` | `accrual.go:200-269` |
| capitalisation | `register.go:1341-1387` | `accrual.go:471-567` |
| terms-window reset | `register.go:276-279,306-308` | `portfolio.go:332-338,412-415` |

The clamp is even spelled two ways for the same arithmetic
(`register.go:1188-1194` vs `accrual.go:205`), and the doc comments above each
block are near-verbatim paraphrases.

Worst consequence: **the same value-date defect is documented twice and fixed
nowhere.** `register.go:1354-1360` and `accrual.go:512-519` describe interest
accruing on interest earned the same day, each pointing at the other. One bug,
two edits, no compiler help.

**Solution.** Deepen `interest/`, which already owns `AccrueSeries` and the
scale rules: add `PerDay(Period) Period` and a `Recompute` returning the delta
and correction split. Each caller supplies only its `Period` — the one thing
that genuinely differs (tiered vs flat).

**Pushes against a documented decision.** `lending/portfolio.go:19-23`: "a
shared constant would be the first thread of exactly that dependency." That
argument forbids `lending → deposit`. It does not forbid a third module, which is
where `ledger.DayStart` and `interest.AccrueSeries` already sit.

#### Phases

**A — done.** `interest.Recompute(series, from, to, prior State, daily Period)
(State, ledger.Amount)` in the new `interest/recompute.go`, with the day-at-a-time
walk as an unexported `perDay`. Both callers now pass only their one-day `Period`
— deposit's tiered `overdraftAccrual`, lending's flat `interest.Accrue` — and the
gross/delta bookkeeping, the day walk and the two ~30-line duplicate comment
blocks are gone from both. `dailyOverdraftAccrual` and `dailyFacilityAccrual`
deleted. No behaviour change: `seed`, which asserts concrete interest figures
across 30-day runs, passes unchanged on both stores.

The extraction surfaced a sharp edge that was previously unreachable: on a window
that has not advanced, gross is 0 and `prior.Accrued + 0 - prior.Gross` hands the
whole record back as a correction. Both callers guard against it with
`Days(LastAccrualDate, date) <= 0`, so this was never live. It is now a
documented precondition with a test that pins the consequence, rather than a
silent short-circuit — a short-circuit would hide a caller that had lost track of
its own window. **Open question for later: make `Recompute` defensive instead.**
That is a contract change, not a refactor, so it was left alone.

**B — not done, needs a decision.** The value-date defect above. The fix both
comments propose (value-date capitalisation to the *next* day) changes interest
figures, so seed expectations, api tests and possibly README/quiz claims move
with it. It is a domain decision and should not ride along inside a refactor.

**C — recommended against.** Unifying the correction split and the
capitalisation. ~3–4 days, and the result takes three account parameters and a
clamp policy. Duplication that is already documented as *intentionally*
divergent is not the duplication worth removing. Lending's capitalisation is 97
lines to deposit's 47, and only the first ~15 overlap: it lists installments,
computes the next due date with `AddMonths`, can fail with
`ErrCycleAlreadyBilled`, and compounds into `PrincipalGL`.

**Left exported with no external caller:** `interest.AccrueSeries`. It is the
primitive `Recompute` is built from, it is independently meaningful, and it has a
270-line test suite of its own. Unexporting it is a separate mechanical change.

### 3. Collapse the `X` / `XTx` doubling — **Strong**

`deposit/register.go` (22) · `lending/*.go` (15) · `payment/system.go` (10) ·
`ledger/book.go` (5) · `store/mem/mem.go:47-54` · `store/pg/pg.go:62-71`

81 `…Tx` sibling methods, 47 of them exported. Nearly every one is a 6-line
closure with no caller outside its own package. Every domain operation exists
twice: one form opens a unit of work, its twin runs inside one.

Nothing types the difference. The rule is prose (`register.go:38-42`,
`lending/doc.go:43-45`) and the failure mode is a deadlock; `store/mem` calls it
"the single most likely mistake in this codebase" and catches it with a `context`
value and an error string.

**Solution.** `Tx`-first: every domain method takes a `Tx`; one `WithTx` helper
above the domain opens and commits. The two colourings collapse into one and
nesting becomes unrepresentable.

Same cause, visible cost: `handleListFacilities` opens two store transactions
*per facility* and reads each facility row three times, because `Drawn` and
`RefundPayableFor` each own their own `View`.

### 4. Cross-layer composition belongs to `Participant` — **Strong**

`api/handlers_lending.go:250-266` · `payment/participant.go:143-166` ·
`payment/store.go:36-63` · `deposit/register.go:85`

`handleRepay` opens a `deposit.Store` transaction inside an HTTP handler, then
downcasts it with `tx.(lending.Tx)` and a hand-written error string to reach the
lending layer. `payment/participant.go:151` contains the same five lines with a
different package prefix. Both error paths are unreachable by construction —
`payment.Tx` embeds `lending.Tx` — so a static fact is being checked at runtime,
twice. `Register.Store()` exists only to permit this.

**Solution.** `Participant.Repay(...)` beside `Participant.RunEndOfDay`, holding
the `payment.Store` whose `Tx` already spans every layer. Deletes 26 of
`handleRepay`'s 45 lines, both downcasts, and `Register.Store()`.

### 5. A `BusinessDate` type in `ledger` — **Worth exploring**

38 signatures across `deposit/` and `lending/` · `ledger/dates.go` ·
`lending/arrears.go:69-92` · `lending/accrual.go:501-504` · `deposit/store.go:55`

`ledger` treats value date as first class (`Entry.ValueDate`, `Series`,
`ValueDatedSeries`). `deposit` and `lending` thread a bare `time.Time` through 38
signatures under four different names (`date` ×23, `asOf` ×5, `from`/`to` ×5
pairs, `firstDue` ×3), and pay for it:

- Six independent truncations of one value in a single call chain
  (`register.go:948`, `portfolio.go:520`, `interest/series.go:61-62`,
  `interest/daycount.go:60`, `book.go:898`, `arrears.go:89-90`).
- Two hand-rolled comparison functions, because `==` on `time.Time` compares
  monotonic reading and location: `sameArrears` (`arrears.go:69-82`) and the
  calendar-day compare at `accrual.go:501-504`. Both carry a paragraph
  explaining why.
- `deposit.SnapshotDateKey` is a fourth day-identity rule and does not `.UTC()`
  first, so it disagrees with `DayStart` for non-UTC input.
- `Disburse`, `Draw` and `CaptureHold` take no date at all and post at
  `p.now()`, so they cannot be backdated — while `Repay`, `RefundInterest`,
  `Accrue` and both capitalisations can.

**Solution.** A day-granular, UTC-normalised-at-construction `BusinessDate` with
`Start()`, `NextDay()`, `Key()` and value equality. Thread the type, not the
instant. Missing dates become compile errors.

Cheapest now, on this branch; more expensive every week the value-date work
continues without it.

### 6. Deepen the transport module — **Worth exploring**

`api/handlers_*.go` (70 handlers, 1,306 lines) · `api/dto_lending.go:80-83` ·
`api/errors.go:25-116` · `api/respond.go:20`

855 of 1,306 handler lines (65%) are the same five steps: participant guard,
`decodeJSON`, domain call, `writeError`, `writeJSON`. 28 of 70 handlers are
nothing else. The same 6-line transaction tail appears 9 times and the same
`YYYY-MM-DD` parse 7 times.

The DTO layer is not purely mapping: `toFacilityDTO` (`dto_lending.go:80-83`)
re-implements `lending.Outstanding` (`repay.go:191-210`), the wire number comes
from the api copy, and nothing asserts the two agree. `entryAssets` does I/O
from `dto_ledger.go`, contradicting `doc.go:9-12`.

1,229 lines of pure DTO mapping have no test that isn't a full HTTP round trip,
and ~970 of `server_test.go`'s 2,169 lines rebuild a domain scenario over HTTP to
assert "…and the status is 422".

**Solution.** One typed handler adapter (`func(*Participant, Req) (int, any,
error)`) absorbing guard/decode/status/encode; `writeTransaction` and `parseDate`
absorbing the 9 and 7 copies; `toFacilityDTO` calling `Outstanding` instead of
redefining it.

Also: `respond.go:20` puts raw `err.Error()` in 500 bodies, where
`recoverPanic` is careful to emit a fixed string.

### 7. Nine adapters that only re-type a callback — **Worth exploring**

`payment/store.go:121-159` · `store/mem/mem.go:392,423,455` ·
`store/pg/pg.go:319,332,354`

Four packages declare a character-identical `Store` interface. Because Go allows
a type one `Update` method, nine adapters exist purely to re-type the callback —
36 lines, zero behaviour. Three of them (in `mem`) use unchecked type assertions
that panic where the other six return an error.

**Solution.** One generic re-type helper with a single checked conversion. Keep
`payment`'s three views — they encode the "cannot be talking to different
databases" invariant (`store.go:118-120`) — but stop hand-writing the body.

### 8. Move the derived balances off the store seam — **Speculative**

`ledger/store.go:28-127` (25 methods) · `+10` deposit · `+5` lending · `+17`
payment · `store/mem/tx.go:281-357` · `store/pg/tx_ledger.go:631-711`

The `Tx` seam carries 57 methods, 70% of them Put/Get/List pass-through. The
cost is in the 17 that carry behaviour: eleven computations are expressed twice,
in two languages, with contract prose as the only link — listing order, both
balances, the series, `ListTransactionsForAccount`, `ActiveHoldTotal`, both
uniqueness claims, `GetOpenCycle`, the subledger block. "Sign an entry by its
account's normal direction" is written five times.

Adding `ValueDatedSeries` cost 88 lines of implementation and 103 lines of
conformance test across 5 files — the test was larger than both implementations
combined.

**Solution.** Narrow the seam toward row access plus one entry-scan primitive
and derive balances, series and hold totals once, in `ledger`.

**Cost, stated plainly.** This trades away Postgres's index-backed `SUM`;
balances would be computed in Go over streamed entries. That is a real
regression on the `pg` path, and why this is Speculative.

**Pushes against a stated load-bearing property.** `CLAUDE.md`: two stores, one
conformance suite, and `store/pg` must never accept or refuse a write
differently from `store/mem`. Chapters 15–16 and the README's *Persistence*
section teach exactly these claims.

### 9. `Scheme` — an interface with seven constant returns — **Speculative**

`payment/scheme.go` (148 lines, no test) · `payment/types.go:58-64` ·
`payment/system.go:138-150,961-967`

`Scheme` has 8 methods; 7 are constant returns. `SCT` and `SDD` are 14 one-line
methods returning literals; the entire behavioural content is two `Validate`
bodies plus `validateFunds`. `RegisterScheme` has no external caller,
`SchemeContext` no external reference, and `SettlementModel()` no consumer at all
— `SettleCycleTx` never branches on it. `api` reads only `ID()` and `Asset()`.

The real invariant was already hoisted *out* of `Validate`
(`system.go:961-967`), i.e. the interface is known to be the wrong place for it.

**Deletion test:** complexity does not reappear across callers — no caller
registers a scheme. It reappears only in the two `Validate` bodies, which is
where it belongs.

**Why Speculative.** `payment/doc.go:42-53` names two unimplemented schemes and
the README/quiz/hint layers restate the scheme model. The interface may be
carrying pedagogical weight the deletion test cannot see. The alternative worth
weighing is implementing a third scheme and letting the seam earn itself.

## Two bugs found on the way

Not architecture, but real, and independent of every candidate above.

1. **`GetPaymentByEndToEndID` diverges between the two stores.**
   `payments_end_to_end_idx` is *not* unique
   (`store/pg/schema/0001_init.sql:407-408`), so two payments may hold the same
   non-empty reference. `store/mem` keeps a last-writer-wins index
   (`mem/tx_payment.go:87-89`); `store/pg` runs `ORDER BY seq LIMIT 1`
   (`pg/tx_payment.go:291`), so first-inserted wins. No conformance test covers
   two distinct payments claiming one reference —
   `storetest/payment.go:375` tests one claimed id plus two empty ones, and
   `:416` re-puts the *same* id. Contrast the ledger's equivalent, where the
   index *is* unique and the collision is a documented sentinel.

2. **`ReverseTransactionTx` skips every check `PostTransactionTx` makes.**
   `ledger/book.go:777-846` builds mirrored entries and writes straight to
   `tx.PutTransaction` (`:831-836`), bypassing `ErrEmptyTransaction` (`:503`),
   the `Amount <= 0` guard (`:509`), `validateBalance` (`:562`),
   `tx.LockAccounts` (`:569`) and `checkSufficientBalance` (`:574`). Reversing a
   receipt into an Asset account whose balance has since been spent posts a
   negative Asset balance that `PostTransaction` refuses at `:712`.
   `book_test.go:665-782` only reverses Liability↔Liability legs.

Also worth a look: `LockAccounts` is a no-op in `store/mem`
(`mem/tx.go:168-170`), so omitting it passes every default-path test and only
corrupts money under `store/pg` concurrency. The single guard
(`pg_test.go:160`) is skipped without `TEST_DATABASE_URL`.
