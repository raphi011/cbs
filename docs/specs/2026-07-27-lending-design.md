# Lending — Design

Sub-project 2 of the expansion tracked in `docs/expansion-roadmap.md`. It adds
credit to a system that so far only takes deposits and moves them: term loans,
revolving credit lines, and interest on the arranged overdraft that until now was
a limit and nothing more.

## Goal

The general ledger already carries Asset accounts, and `deposit` is the proof
that a product layer can wrap a Liability account without storing money itself.
Lending is the mirror image, and the best available evidence that the GL really
generalizes: a loan wraps an Asset account, accrues interest daily, amortizes on
a schedule, and falls into arrears when an instalment goes unpaid.

Three products ship together, because the third one is what stops the
abstraction from being loan-shaped by accident:

- **Term loan** — a fixed principal disbursed once, amortized over a schedule.
- **Revolving credit line** — a limit that can be drawn and repaid repeatedly.
- **Arranged overdraft** — the existing `deposit` feature, given a rate and
  daily accrual.

Out of scope, deliberately, and recorded as future work in the roadmap:
non-accrual and NPL accounting, expected-credit-loss provisioning and IFRS 9
staging, write-off, arrangement and late fees, early-repayment penalties, and
restructuring. Arrears are *tracked* here; what a bank does about them is a
domain of its own.

## Decisions

Settled during design. As with sub-project 1, the reasoning matters more than the
outcome, because several of these constrain what comes after.

### An overdrawn current account gets no loan account

This is the decision the whole design turns on, and the intuition it contradicts
is a good one: an account in overdraft *is* a loan, so surely it should appear on
the Asset side of the balance sheet like one.

It should — but not by posting anything.

**What real banks do.** Historically the general ledger and the deposit system
were separate systems, because a general ledger held a few thousand accounts and
a retail bank had a few million customers. Customer detail lived in a deposit
**subledger**; the GL held one **control account**, "Customer Deposits", with a
single stored total, and the deposit system fed it a summary every night. The
Asset/Liability split falls out of that summarization step: when the nightly feed
is built, accounts are bucketed by the *sign* of their balance — credit balances
roll into customer deposits (Liability), debit balances into loans and advances
(Asset). Two totals instead of one. No posting ever touches the customer's own
account.

The price of that architecture is that the same number is written down twice —
the sum of the detail, and the GL's stored control balance — so they can drift,
and banks run a subledger-to-GL reconciliation every night to prove they haven't.

**What this repo does instead**, and has documented since well before this
sub-project (`docs/deposit-accounts-vs-subledger.md`, section 7): one ledger. A
customer account *is* a GL account, the subledger is a folder, there is no
control account, and "total customer deposits" is computed by aggregation when
asked. Nothing is stored twice, so nothing can drift and the reconciliation is
unnecessary rather than automated.

Which means the Asset-side split is not something a control account *provides*.
It is something summarization happens to do on the way past, and here
summarization is a query:

```
Total customer deposits = Σ over the Customer Deposits subledger of  max(0,  balance)
Total overdrafts drawn  = Σ over the Customer Deposits subledger of  max(0, −balance)
```

The identical accounting fact, derived rather than stored, exactly as the deposit
total already is.

So: **the drawn amount of an arranged overdraft has no independent existence.**
It *is* the negative balance of the customer's Liability account, viewed by sign.
Giving it its own GL account would store a number that already exists elsewhere,
which is precisely the drift this codebase's design forbids.

Rejected, and why:

- **A reclassification journal** (`Dr Overdrafts drawn / Cr customer account`,
  reversed next morning). In a real bank this hides inside summarization; here
  there is no summarization to hide in, so the journal necessarily moves the
  customer's own balance. The customer's on-screen balance would jump to zero
  overnight.
- **An EOD sweep to a facility account** — a permanent drawdown bringing the
  deposit account to zero, with the customer-facing balance redefined as
  `deposit − drawn`. Coherent, and it is genuinely how *linked-loan* and *sweep*
  products behave. But it models a different product: a US overdraft line of
  credit or a savings-transfer arrangement, not an arranged overdraft, where the
  account simply runs negative. It would also falsify what the README, hints and
  quiz chapter 8 currently teach.
- **Restructuring deposits into a subledger with control accounts.** This would
  make the reclassification fall out naturally, at the cost of reintroducing the
  duplication and the nightly reconciliation the repo deliberately eliminated. A
  regression dressed as realism.

There is a second, independent argument for the same answer: **in real core
banking, arranged overdraft is a current-account (CASA) feature, not a Loans
module product.** Temenos, Finacle and Mambu all put it there; loans and credit
lines are separate modules. The packaging decision below follows from this.

### The three products share arithmetic, not plumbing

A single `lending` package owning all three was the obvious first shape, and it
does not survive contact with the dependency graph. `deposit.CheckWithdrawalTx`
needs the overdraft limit to compute available balance, and `payment` calls it —
so a `lending` that owned the limit would produce `deposit → lending → deposit`.

The escapes are all worse than the problem. Leaving `OverdraftLimit` on
`deposit.Account` while its rate and accrued interest live in `lending` splits one
product across two packages. Breaking the cycle with an injected
`OverdraftLimiter` interface works, but introduces a seam whose only purpose is
packaging.

And forcing all three into one `Facility` type would assert a unification the
decision above just rejected: two of these products have a drawn amount that
exists independently, and one does not.

So the shared machinery is the arithmetic, in a package with no store and no
book:

- **`interest`** — day-count conventions, accrual, the high-precision accrued
  type and its rounding. Pure functions.
- **`deposit`** — grows the overdraft rate and its accrual, next to the limit it
  already owns.
- **`lending`** — term loans and revolving lines, each with its own GL accounts.

The cost is two end-of-day entry points instead of one, which is also what a real
bank has: each module runs its own batch.

### Interest is held at sub-minor-unit precision and posted daily

A day's interest on a small balance is mostly fraction. Bruno's €50 overdraft at
15% accrues €0.020548 a day; rounding that to €0.02 daily and discarding the rest
is a 2.4% annual error on the interest. No bank does that. Real core systems hold
accrued interest at higher precision than the posting currency and round only
when it is charged.

That precision is kept here, on the account or facility record. But the accrual is
still **posted to the GL every day**, as the change in the *rounded* accrued
value:

| day | exact (cents) | rounded | posted |
| --- | --- | --- | --- |
| 1 | 2.0548 | 2 | 2 |
| 2 | 4.1096 | 4 | 2 |
| 3 | 6.1644 | 6 | 2 |
| … | | | |
| 15 | 30.822 | 31 | 2 |

The GL balance always equals `round(accrued)`, the record holds the exact figure,
nothing drifts, and a day where the rounding does not tick posts nothing at all.

The invariant this maintains is that the receivable's GL balance always equals
`round(accrued)`. Capitalization charges exactly that GL balance and subtracts it
from the record — which can leave the record **negative** by up to half a minor
unit, because the charge was rounded up from what had been earned. Bruno's 30
days accrue 61.64 cents and 62 are charged, leaving −0.36. That is correct, not a
bug: `round(−0.36)` is still 0, so the invariant holds, and the next day's
accrual absorbs the residue. Truncating instead would leak a fraction on every
cycle.

Posting only on capitalization was considered and rejected: between charge dates
the accrued interest would be a real asset existing nowhere in the ledger,
understating both assets and income on every intermediate date. That is the same
defect as an unrecorded overdraft, and it gets the same answer.

Real banks post their daily accrual to the GL *in aggregate*, per product or
portfolio, keeping per-account detail in the subledger. Here that would be a
stored control number whose detail lives elsewhere — Pattern A again — so the
accrual is posted per account, to that account's own receivable.

**Why the record duplicates a GL balance at all**, given everything above: it
does not. It holds the residue the ledger structurally *cannot* represent,
because `ledger.Amount` is an integer of minor units. This is the same
justification holds already have for living outside the ledger.

### Repayment allocates against actual accrued interest, not the schedule

Interest before principal, where "interest" means what has actually accrued — not
the interest column of the amortization schedule.

The two differ, and the difference is instructive. A €10,000 loan at 6% has a
scheduled first-month interest of €50.00 (one twelfth of 6%), but 30 days of
ACT/365 accrual is €49.31. Allocating against the schedule would post interest
that had not been earned. Allocating against the accrual leaves the principal
portion to absorb the difference, which is what real systems do — the schedule is
a plan for due dates and amounts, not a statement of fact.

This is also exactly why the 30/360 convention exists: under it a 30-day month
accrues precisely one twelfth, and plan and actual agree. Shipping both
conventions makes that visible rather than a footnote.

### Both amortization methods, and the schedule is stored

Annuity (equal total instalment) and equal-principal (declining instalment) are
both offered per loan. The generators differ by a few lines, and having both
shows the schedule is a product parameter rather than a law of lending.

The schedule is generated at disbursement and **stored**, which is a deliberate
exception to this repo's derive-don't-store rule. It is a contract artifact; and
the moment a loan is rescheduled or partially prepaid the schedule depends on
history rather than on terms, so it cannot be recomputed from the loan.

### Money moves against a generic counterparty

`lending` takes a `counterparty ledger.AccountID` for disbursement, drawdown and
repayment, mirroring `deposit.CaptureHold`. It does not import `deposit`.

A repayment from a customer's current account is therefore orchestrated one layer
up: `deposit.CheckWithdrawalTx` then `lending.RepayTx` through the same `Tx`.
That is the multi-layer pattern `Register.Store()` already exists for, and it
keeps a cash disbursement from the vault as expressible as one into an account.

## Design

### `interest`

A new package with no store, no book, and no dependency beyond `ledger.Amount`
and `time`.

```go
// Fraction is a dimensionless proportion in millionths: 1_000_000 is 100%.
type Fraction int64

// Rate is an annual interest rate in millionths: 1_000_000 is 100%,
// 150_000 is 15%, 33_750 is 3.375%.
//
// Basis points alone are too coarse. Retail rates are quoted in eighths of a
// percent, so 3.375% is 337.5 bp and would not be an integer.
//
// A defined type distinct from Fraction, though both are millionths: a rate is
// per annum and a fraction is not, and the compiler should refuse to swap them.
type Rate int64

// Accrued is an amount of interest in micro-minor-units: ledger.Amount × 1e6.
//
// It exists because ledger.Amount is an integer of minor units and a day's
// interest on a small balance is mostly fraction — €50 at 15% accrues 2.0548
// cents a day. Accrued holds what the ledger structurally cannot.
type Accrued int64

type DayCount int
const (
    ACT365   DayCount = iota // actual days over 365 — most retail
    ACT360                   // actual days over 360 — EUR money market, US commercial
    Thirty360                // 30-day months over 360 — US mortgages, bonds
)

func Accrue(balance ledger.Amount, r Rate, dc DayCount, from, to time.Time) Accrued
func (a Accrued) Minor() ledger.Amount // round half-up
```

`Rate` and `Accrued` share the scale `1e6` deliberately. The accrual is

```
Accrued = balance × Rate × days / yearDays
```

with no intermediate rescaling — the `×1e6` of the accrued scale and the `÷1e6`
of the rate scale cancel. It is therefore exact in `int64`, and cannot overflow
for any balance this system can hold: the largest intermediate is
`balance × Rate`, which at a €10 billion balance and a 100% rate is `1e18`,
inside `int64`'s `9.22e18`. Multi-day gaps accrue day by day rather than in one
multiplication, which the end-of-day batch does anyway.

`Thirty360` needs its own day computation (the 30-day-month adjustment); the
other two use actual days.

### `deposit`

`Account` gains credit terms, alongside the `OverdraftLimit` it already carries:

```go
Rate           interest.Rate     // arranged rate; 0 means no interest
UnarrangedRate interest.Rate     // applied to any balance beyond the limit
DayCount       interest.DayCount
Accrued        interest.Accrued
LastAccrualDate time.Time
InterestGL     ledger.AccountID  // per-account Asset: accrued interest receivable
```

A zero rate means no interest, matching the existing convention that a zero limit
means no overdraft.

New operations:

- `SetOverdraftTerms(id, limit, rate, unarrangedRate, dayCount)` — there is no
  setter for the limit today; this is it. It lazily creates the account's
  interest-receivable GL account the first time a non-zero rate is set.
- `AccrueOverdraft(id, date)` / `…Tx` — accrue one account for one day.
- `ChargeOverdraftInterest(id, date)` / `…Tx` — capitalize the rounded receivable
  into the account.
- `RunEndOfDay(date)` / `…Tx` — accrue every overdrawn account.

The accrual base is `max(0, −bookBalance)`: the **book** balance, not available,
because a hold is not borrowed money. It is tiered — the arranged rate up to the
limit, the unarranged rate on the excess — which makes the arranged/unarranged
distinction the README already describes into behaviour rather than prose. An
account can exceed its limit despite the withdrawal gate, because a direct GL
posting bypasses the deposit layer and because capitalizing interest on a
fully-drawn overdraft pushes it over by itself.

`Balance` is unchanged. `Available = Book − Holds + OverdraftLimit` still holds,
and `Book` is still the GL account's balance. Nothing already taught becomes
false.

### `lending`

`lending.Portfolio` has the same shape as `deposit.Register` and
`payment.Network`: a store handle, the `ledger.Book` it composes with, the
`BookID` both are scoped to, a clock, and a `…Tx` twin on every mutator that
never opens a second unit of work.

```go
type FacilityID string
type FacilityKind int   // TermLoan | RevolvingLine
type AmortMethod int    // Annuity | EqualPrincipal
type FacilityStatus int // Pending | Active | Closed

type Facility struct {
    ID          FacilityID
    Kind        FacilityKind
    Name        string
    Asset       ledger.AssetCode
    PrincipalGL ledger.AccountID // Asset — drawn principal
    InterestGL  ledger.AccountID // Asset — accrued interest receivable
    Commitment  ledger.Amount    // what the bank has committed: a loan's
                                 // original principal, a line's limit
    Rate        interest.Rate
    DayCount    interest.DayCount
    Method      AmortMethod    // term loan only
    MinPayment  interest.Fraction // revolving line only: share of drawn principal
                                  // added to each cycle's minimum payment
    Accrued     interest.Accrued
    LastAccrualDate time.Time
    Arrears     Arrears
    Status      FacilityStatus
    OpenedAt    time.Time
    MaturityAt  time.Time // zero for an open-ended revolving line
}

type Bucket int // Current | D1_29 | D30_59 | D60_89 | D90Plus

type Arrears struct {
    DaysPastDue     int
    Bucket          Bucket
    NonPerforming   bool      // marks only; non-accrual is future work
    OldestUnpaidDue time.Time
}

type Installment struct {
    FacilityID          FacilityID
    Seq                 int
    DueDate             time.Time
    Principal, Interest ledger.Amount // the plan
    PaidPrincipal, PaidInterest ledger.Amount
}
```

`Installment` serves both kinds. A term loan's rows are all generated at
disbursement. A revolving line has no schedule to generate up front, so it
appends **one row per billing cycle** when `ChargeInterest` runs: the minimum
payment due, being the interest just charged plus a configured percentage of
drawn principal. That is how a revolving facility actually falls into arrears —
by missing a minimum payment, not an amortization instalment — and it lets one
arrears implementation serve both products instead of two.

Two GL accounts per facility, both Asset, both in the facility's own asset. They
have to be separate: repayment allocation is interest-before-principal, and a
single account could not express the split.

Operations:

- `OpenTermLoan(subledger, name, asset, principal, rate, dayCount, method, termMonths, firstDue)`
- `OpenRevolvingLine(subledger, name, asset, limit, rate, dayCount)`
- `Disburse(id, counterparty, amount, description)` — term loan, once, in full.
- `Draw(id, counterparty, amount, description)` — revolving line; refuses beyond
  the limit.
- `Repay(id, counterparty, amount, date, description)` — allocates to accrued
  interest first, then principal; marks instalments paid.
- `Accrue(id, date)` — one facility, one day.
- `ChargeInterest(id, date)` — revolving line: capitalize the rounded receivable
  into drawn principal and append the cycle's minimum-payment instalment. Term
  loans settle interest through their scheduled instalments and do not
  capitalize.
- `Close(id)` — refuses a facility with a non-zero principal or receivable.
- `RunEndOfDay(date)` — accrue every active facility and recompute arrears.

`Status` is `Pending` from opening until the first disbursement or draw, `Active`
once anything is drawn, and `Closed` after `Close`.

Arrears are recomputed from the schedule at end of day: the oldest instalment
with an unpaid amount and a due date in the past sets `DaysPastDue`, which
selects the bucket; `NonPerforming` is set at 90+. The flag changes no
accounting — non-accrual is future work — but it is what every real delinquency
report is keyed on.

**An arranged overdraft has no days-past-due**, deliberately. Arrears count from
a missed due date, and an overdraft has none: it is repayable on demand, and the
sanction for exceeding the limit is the unarranged rate rather than a
delinquency bucket. This is another place where the three products are genuinely
different rather than uniformly different, and the docs should say so.

### Composition

`payment.Participant` binds a `Lending` alongside `Ledger` and `Deposit`, and
grows a `RunEndOfDay(date)` that calls both its register's and its portfolio's.
That is the composition point the API already reaches for. There is no scheduler:
the caller advances the clock, exactly as today.

### The legs, worked

**Term loan** — €10,000 over 5 years at 6%, annuity, ACT/365, disbursed to Alice:

```
disburse      Dr Loan principal        1_000_000   Cr Alice current account  1_000_000
day 1         Dr Interest receivable         164   Cr Interest income              164
day 2         Dr Interest receivable         165   Cr Interest income              165
instalment 1  Dr Alice current account    19_333   Cr Interest receivable        4_931
                                                    Cr Loan principal            14_402
```

Day 1's exact accrual is 164.3836 cents; day 2's cumulative 328.767 rounds to
329, so the delta posted is 165. The instalment clears €49.31 of interest — what
accrued — rather than the schedule's €50.00, and the principal portion absorbs
the difference.

**Arranged overdraft** — Bruno holds €50 and pays €100, arranged rate 15%
ACT/365:

```
payment       Dr Bruno current account    10_000   Cr Clearing suspense       10_000
```

Bruno's GL account is now −5_000. There is no second posting: the drawn amount
*is* that balance.

```
day 1         Dr Interest receivable           2   Cr Interest income              2
month end     Dr Bruno current account        62   Cr Interest receivable         62
```

Bruno is now at −5_062. Charging interest to the account is what makes an
overdraft compound, and it is the monthly event the customer actually sees.

### Stores, schema, conformance

`store/mem/tx_lending.go` and `store/pg/tx_lending.go` implement the new
`lending.Tx`; `deposit.Tx` is unchanged in shape but its account row grows the
new columns. Tables are folded into `0001_init.sql` rather than added as a
migration — no database is deployed, and `migrate.go`'s own doc comment sanctions
it. Schema comments carry the domain reasoning as they already do, including why
`facilities.accrued` is not a money column and why the schedule is stored.

`store/storetest/lending.go` joins the conformance suite. `store/mem` and
`store/pg` must remain indistinguishable through it, and both runs — with and
without `TEST_DATABASE_URL` — must stay green.

A new audit scope `ledger.ScopeLending` with `FacilityOpened`, `Disbursed`,
`Drawn`, `Accrued`, `InterestCharged`, `Repaid`, `ArrearsChanged` and
`FacilityClosed`. `deposit` gains `OverdraftTermsSet`, `OverdraftAccrued` and
`OverdraftInterestCharged` in its existing scope.

### API and web

REST endpoints for opening a facility, listing facilities, reading one with its
schedule, drawing, repaying, and triggering end of day for a participant. DTOs
mirror the existing ones, including the asset on every amount. Rates are carried
as their millionths integer with the scale documented, the same way amounts are.

Web: a facility list per participant, a facility page with its amortization
schedule and an arrears badge, overdraft terms on the deposit account page, and
the derived "overdrafts drawn" total on the participant's balance-sheet view.

### Seed

The sample scenario gains a term loan part-way through its schedule, a revolving
line partly drawn, one loan in the 30–59 day bucket, and an overdraft rate on
Bruno — whose overdraft the seed already opens. The clock is advanced far enough
that accrual and a capitalization are both visible in the data rather than
theoretical.

## Documentation

Per `CLAUDE.md`, the domain content moves across all four layers together:

- **`README.md`** — a new Lending section covering the three products, accrual,
  amortization and arrears; and a correction to the existing Overdraft section,
  which currently stops at "the ledger simply records the economic reality" and
  must now also say that interest accrues on the debit balance and that the
  Asset-side classification is derived by aggregation rather than posted.
- **`web/src/components/hint-content.ts`** — keys for the new concepts, and an
  update to the existing `overdraft` key. Every `[[wiki-link]]` must resolve.
- **Quiz** — two new chapters, 17 *Lending and Amortization* and 18 *Interest,
  Overdrafts and Arrears*, each meeting `diversity.test.ts` (18–22 questions, ≥8
  distinct concepts, no tag more than 3×, all three difficulty tiers), and both
  slugs added to `index.test.ts`.
- **`store/pg/schema/0001_init.sql`** — comments for the new tables and columns,
  including a `COMMENT ON COLUMN` for the accrued-interest scale, since a scale
  carried in an integer column is invisible in a schema dump.
- **`docs/expansion-roadmap.md`** — sub-project 2 to `done`, with a log entry, and
  the out-of-scope items recorded as future work.

## Testing

- **`interest`** — table tests per day-count convention; a property test that a
  year of day-by-day accrual equals the annual figure within one minor unit; and
  overflow bounds at the largest balance the system can hold.
- **`deposit`** — tiered accrual across the limit boundary, accrual base is book
  and not available, capitalization compounds, idempotency per `(account, date)`,
  and that the receivable's GL balance equals `round(accrued)` across a
  capitalization that leaves a negative residue.
- **`lending`** — schedule generation for both methods (including that the
  instalments sum to principal plus interest exactly, with the rounding residue
  landing on the final instalment); allocation order; limit enforcement on draw;
  accrual idempotency; DPD bucket transitions across boundaries.
- **Conformance** — the new suite green on both stores.
- **One test pins the claim this branch keeps getting wrong in prose**: that an
  overdrawn account produces no Asset-side posting, and that the derived
  aggregate equals the sum of negative balances over the subledger. Two
  counter-examples on the previous branch argued the opposite of what they
  claimed; prose asserting what code does needs a test, not a careful reading.

## Risks

- **Two end-of-day entry points can drift apart.** `deposit.RunEndOfDay` and
  `lending.RunEndOfDay` are independent, and a caller can run one without the
  other. `Participant.RunEndOfDay` calling both is the mitigation, and the API
  exposes only that.
- **Idempotency is per `(entity, date)` and depends on the caller's date.**
  Re-running with a different date double-accrues. The date is the business day,
  taken from the clock the whole system shares, and `LastAccrualDate` refuses to
  go backwards.
- **The stored schedule is the one place a number is written twice.** It is
  justified above, but it is genuinely the exception, and a prepayment that
  updates principal without updating the schedule would make the two disagree.
  Arrears read the schedule, so the disagreement would surface as wrong
  delinquency rather than a wrong balance.
- **Lending in an asset the bank funds itself in another is an unrecorded FX
  exposure.** The ledger cannot catch it — each posting balances in its own
  asset — and sub-project 4 is where it becomes representable. Until then it
  exists and is simply not recorded, which the README should say rather than
  leave to be discovered.
