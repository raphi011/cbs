# Design — the subsidiary ledger: customer accounts leave the chart of accounts

Branch `spec/subsidiary-ledger`, based on `3237ec0`.

A customer's account is not a general-ledger account in any core banking system
this repository is modelled on. Here it is one: `deposit_accounts.gl_account`
points at a row in `accounts`, and so do `facilities.principal_gl`,
`facilities.interest_gl`, `facilities.refund_gl` and
`deposit_accounts.interest_gl`. A customer holding a current account and a term
loan is five rows in the chart of accounts.

This moves them out, and it does so **without** reintroducing the stored second
copy that `docs/expansion-roadmap.md`'s *A thin GL* bullet defers. The
arrangement is a control account whose balance is still an aggregation.

Like Tasks 20 and 21, this overturns an argument the repository has already
written down and won, in four layers. The overturning is most of the work, and
§*What becomes false* is the part to review hardest.

## The claim being overturned, and the one that survives

`README.md:385` says:

> In the classic design the general ledger and the deposit subledger are
> genuinely separate systems. […] Because the control figure is written down
> independently of the detail, the two can drift […] So the bank runs a
> subledger-to-GL reconciliation every day.

and answers it with:

> Here there is one ledger. A deposit account is a pointer to a GL account, a
> subledger is a folder, and "total customer deposits" is computed by
> aggregation when asked.

The **answer** survives verbatim. What does not survive is the premise it
shares with the objection: that a control account necessarily implies a *stored*
control figure. It does not. The two properties are independent, and this
repository has been paying for the first in order to avoid the second.

| | classic | here, today | here, after |
|---|---|---|---|
| customer accounts in the chart of accounts | no | **yes** | no |
| control figure stored independently of detail | **yes** | no | no |
| subledger-to-GL reconciliation needed | **yes** | no | no |
| chart of accounts bounded by the institution | **yes** | no | **yes** |

The right-hand column is the whole of this task. Row 2 is what the README
defends and it is untouched.

There is a second claim, smaller and load-bearing in a different way, at
`ledger/book.go:333`. `EnsureSubledgerTx` justifies resolving by name on every
call with "a chart of accounts that has tens of rows, not millions." That is an
aspiration in the current design and a fact in this one.

## Decisions taken, and what they cost

1. **A subsidiary is a dimension on an entry, not a second entries table.**
   `entries` gains `subsidiary_id`. A posting's target is always a GL account; a
   customer is a value in a column beside it. A customer's balance is that
   account's balance with a `WHERE`, and the control account's balance is the
   same sum with the `WHERE` dropped — so `Σ(detail) == control` is not a
   nightly proof but the same statement twice. **Cost:** every balance read in
   `ledger` grows a key it did not have, and `checkSufficientBalance` becomes
   the one place a defect here would be silent. See §*The sufficiency check*.

2. **`ledger` does not learn what a customer is.** `Entry.Subsidiary` is an
   opaque string the layer above supplies — `dep_42`, `fac_7` — with no foreign
   key and no table, exactly as `entries.account_id` already carries none and
   for the same reason. **Cost:** nothing in the store refuses a subsidiary that
   names nothing. That is the existing stance on parent references, not a new
   licence; `ParentReferencesAreNotEnforced` in `store/storetest` is the test
   that holds it.

3. **A control account refuses an unqualified entry, and a plain account refuses
   a qualified one.** `Account.Control bool`, checked in `PostTransactionTx`. An
   entry against `Customer Deposits (EUR)` with no subsidiary would be money in
   the pool belonging to nobody, and an entry against `Vault Cash (EUR)` with a
   subsidiary would be a dimension nothing reads. Both are silent today and
   neither is recoverable after the fact. **Cost:** two refusals a caller has to
   get right, and the flag is a property the compiler cannot state — so it is
   written inside the `accounts` parentheses, where the schema's absent-rule
   arguments live.

4. **Which control account a flow posts to becomes data.** The string literals
   in `deposit/register.go:341,351` and `lending/portfolio.go:24-27` — resolved
   today by `EnsureAccountTx` on name equality, with the same two literals
   duplicated across two packages that must not import each other — become a
   mapping keyed by `(product, slot, asset)`. **Cost:** a misconfigured mapping
   is a new failure class, so a slot declares its required `AccountType` and the
   mapping is validated on write. This is the half of the roadmap's
   *posting-rules engine* that carries no stored figure with it.

5. **The bank's own accounts stay plain.** Clearing Suspense, Reserve at Central
   Bank, Vault Cash, Unclaimed Balances and Returns Receivable
   (`payment/system.go:937-958`) get no subsidiary. What is left in the chart of
   accounts after this task is therefore *the bank's own positions plus its
   control lines*, which is what a chart of accounts is. **Cost:** Unclaimed
   Balances is arguably a control account with a claimant dimension — money the
   bank owes to somebody not yet identified — and it is not made one here. Named
   rather than missed.

6. **No migration.** Every database this repository meets is ephemeral or a
   throwaway file and migrates from empty, so `0001_init.sql` is edited in
   place in all three shapes rather than layered on. **Cost:** none that is
   real, and it removes the single largest risk this change would carry
   anywhere else. It is also why the task order below can move one layer at a
   time without a compatibility window.

## The shape

### `ledger`

```go
// Position is what a balance is asked for. Subsidiary is empty for an account
// that holds no per-obligor detail.
type Position struct {
    Account    AccountID
    Subsidiary string
}

type Entry struct {
    ID         EntryID
    AccountID  AccountID
    Subsidiary string   // new
    Amount     Amount
    Direction  Direction
    ValueDate  time.Time
}

type Account struct {
    // …
    Control bool // refuses an entry carrying no Subsidiary
}
```

`BookBalance`, `ValueDateBalance` and `Series` take a `Position` rather than an
`AccountID`, so there is one balance API and not two. `validateBalance` is
untouched: debits equal credits per asset regardless of the dimension.

### Schema

```sql
CREATE TABLE entries (
    -- subsidiary_id is the obligor a leg belongs to within a control account:
    -- a deposit account, a facility. It is '' on an account that pools nothing.
    --
    -- It carries no foreign key and there is no table of subsidiaries, for the
    -- reason account_id carries none: the ledger does not know what a customer
    -- is, and the layer that does supplies a string.
    --
    -- What is ABSENT is the second entries table. The classic split stores the
    -- detail apart from the control figure and reconciles them nightly; here
    -- the control figure is this column's absence from the WHERE clause, so
    -- Σ(detail) == control is one statement read two ways and cannot drift.
    subsidiary_id  TEXT NOT NULL DEFAULT '',
    …
);

CREATE INDEX entries_account_idx ON entries (
    -- subsidiary_id sits between account_id and value_date so that
    -- (book_id, account_id) remains a usable prefix: a control account's own
    -- balance reads the whole pool and must not pay for the dimension.
    book_id, account_id, subsidiary_id, value_date
);
```

`accounts` gains `control INTEGER NOT NULL DEFAULT 0`, with the two refusals
argued inside its parentheses. `deposit_accounts` drops `gl_account` and
`interest_gl`; `facilities` drops `principal_gl`, `interest_gl` and `refund_gl`.

Note for whoever writes these: `TestSchemaArgumentsReachSqliteMaster` fails if
an argument about something the schema does not do moves to column 0.

## The sufficiency check

`Book.checkSufficientBalance` (`ledger/book.go:697`) guards Asset and Expense
accounts against going negative. Under pooling the control account is **never**
negative while a subsidiary under it is, so a check that keeps reading the
account is a guard that silently stops guarding — every Asset-side facility
would become unbounded, and nothing in the double-entry invariant would notice.

It must read the `Position`. This lands in task 1 below, with its own tests,
**before any caller moves**, because it is the one defect in this design that
produces no error and no imbalance.

The Liability side is unaffected: a customer's overdraft is checked in
`deposit`, against its own limit, and that read simply grows the dimension.

## What folds back in

Two constructs exist solely to avoid a control account, and both are deleted
rather than ported.

**Per-account receivables.** `deposit/register.go:546` gives every deposit
account its own `Accrued Interest: <name> (<asset>)` because "a shared account
would be a stored total whose per-customer detail lives in `Account.Accrued` — a
control account, and the duplication this codebase is built without." Under this
design a shared one duplicates nothing: the per-customer detail *is* the entries
under the dimension. `ensureReceivableTx` goes, and with it one lazily created
account per interest-bearing customer. The same argument is in the schema, on
`deposit_accounts.interest_gl`.

**`Payables`.** `lending/portfolio.go:31-52` and `README.md:390` call this "the
exception that proves the rule" — a genuine subsidiary ledger with the folder's
total as the control figure, earned because a pooled number "cannot say who is
owed what" and a discharge against it would be unbounded. Both objections are
answered by the dimension, and by the sufficiency check reading it. It stops
being an exception. `interestRefundPayableName` and the lazily created `Payables`
subledger go; `Facility.RefundGL` becomes a slot.

That two independent places in this codebase invented per-obligor accounts to
dodge a control account is the strongest evidence the dimension is the missing
primitive.

## What stays out

- **A stored thin GL** — per-key-per-day aggregation, period close, a
  closed-period lock, subledger-to-GL reconciliation. Still deferred, and this
  task does not bring it closer; it removes the reason someone would reach for
  it. `docs/expansion-roadmap.md`'s bullet is split rather than struck.
- **The overdraft reclassification journal.** `README.md:724` states that no
  transaction ever posts the drawn amount to an Asset account, and that the
  Asset-side total is `Σ max(0, −balance)` over deposit accounts. After this
  task a reclass journal becomes *expressible* — control to control, both sides
  named — for the first time. It is still not built, and the README's paragraph
  needs rewriting to say so on the new grounds rather than the old ones.
  `deposit.TestTotals_OverdraftsAreDerivedAndNothingIsPosted` continues to hold.
  It has its own roadmap entry, *The overdraft reclassification*, which carries
  the three domain decisions — chiefly that landing the posting while leaving
  `Totals` deriving would put one number in two places.
- **Cash versus accrual as a toggle.** That needs the GL derived from a
  separate record of transactions, which is design B and is not this.
- **A party model.** Unchanged and still the most expensive omission in the
  repository. A subsidiary is an account-shaped obligor, not a person.

## Task order

Each lands green, and only 3 and 4 change any figure a test asserts.

1. **`ledger` gains the dimension.** `Position`, `Entry.Subsidiary`,
   `Account.Control`, the two posting refusals, subsidiary-scoped balance and
   series reads, and the sufficiency check on `Position`. Pure addition: every
   existing account is non-control and every subsidiary is `''`, so the suite is
   unchanged. Schema and `store/storetest` move with it.
2. **The trial balance.** Designed and unbuilt at `docs/expansion-roadmap.md:265`
   — `ledger/trialbalance.go`, per book and per asset, no store method, no schema
   change. Built here because it is the acceptance test for step 3: the row count
   drops from one-per-customer to one-per-control-line, and that is the whole
   observable point of this task.
3. **`deposit` onto control plus subsidiary.** `Account.GLAccount` and
   `Account.InterestGL` go; postings at `register.go:1197,1788,1954` and
   `transfer.go:92-93` name a control account and the account's own id. `Totals`
   stays a query by sign, now over one account's dimension rather than over a
   folder of accounts.
4. **`lending`**, and the deletion of the `Payables` machinery.
5. **The slot mapping as data**, replacing name-equality resolution.
6. **`web` and `api`.** `api/dto_deposit.go:29,56` lose `glAccount` and
   `interestGlAccount` — a wire break, and the only one. The chart of accounts
   lists GL accounts only; a control account's page drills into its
   subsidiaries; `accounts/[aid]`'s `backingDeposit` reverse lookup and
   `GLAccountPicker`'s unfiltered `useAllAccounts` both go.
7. **Docs**, below.

## What becomes false

Every one of these states the current design as a positive claim, so none fails
a build and all four layers have to move together.

| where | what it says now |
|---|---|
| `README.md:383-391` | *A Control Account, or an Aggregation* — the section this task rewrites rather than deletes. The conclusion survives; the premise that a control account implies a stored figure does not. |
| `README.md:373-375` | the GL "might show one line item for Total Customer Deposits while the subledger contains 50,000 individual accounts" — after this task it does show exactly that, and the sentence describing it as classic-only is wrong. |
| `README.md:724` | the overdraft reclassification, absent for a reason that changes. |
| `README.md:923` | the chart-of-accounts table, whose first row is `Customer deposits` and now means a control account rather than a per-customer one. |
| `web/src/components/hint-content.ts:119-129` | the ledger/subledger hierarchy hint, same claim distilled. |
| quiz `03-the-chart-of-accounts.ts:152` | quizzes the control-account contrast **directly**. The answer changes. |
| quiz `05-transactions-and-postings.ts:60` | likewise, "what does the General Ledger show for this subledger?". |
| quiz chapters 15 and 16 | the relational mapping, which is one of the four layers. |
| `store/sqlite/schema/bank/0001_init.sql` | `deposit_accounts.interest_gl` and `facilities`' three GL columns, and their arguments. |
| `deposit/types.go:191` | `Totals` — "this system has no summarization step to hide it in, because it has no control accounts". |
| `deposit/register.go:341-351,546` | the receivable and income account names and the argument for per-account receivables. |
| `lending/portfolio.go:24-52` | the duplicated literals and the `Payables` exception. |
| `ledger/book.go:333` | "tens of rows, not millions" — true rather than aspirational; worth keeping and worth not restating. |
| `docs/expansion-roadmap.md:702` | the *A thin GL* bullet, split. |
| `store/storetest/deposit.go:31` | `DepositAccountAssetMatchesItsGLAccount`, which no longer has a GL account to match. |

The learner-facing layers name no repo symbol; that rule is unchanged and the
rewrites above are subject to it. A control account and a subsidiary ledger are
both banker's vocabulary, so they may be named as such.

## Tests worth having that do not exist

- A control account refuses an entry with no subsidiary; a plain account refuses
  one with a subsidiary. Both directions, because only one of them is the
  obvious way round.
- The sufficiency check catches an Asset-side subsidiary going negative while
  its control account is comfortably positive. This is the test that would have
  caught the silent version of this design.
- `Σ(subsidiary balances) == control balance`. It cannot fail arithmetically,
  which is exactly why it is worth computing — the same argument the trial
  balance is built on. It is a control on the pipeline, and it would catch a
  direct store write or a bad fixture.
- A statement over one subsidiary returns that customer's postings and no other
  customer's, over a control account holding several.

## Open, and worth deciding before task 1

- **Does `Unclaimed Balances` become a control account with a claimant
  dimension?** It is the one institutional account whose balance is owed to
  someone. Deferred above; if it is wanted, it is cheapest in task 1.
- **Does a subsidiary get a display name in the ledger, or is every rendering a
  join through `deposit_accounts`?** A name in `ledger` would be a second copy
  of `deposit_accounts.name` and this repository has one such copy already
  (`asset`), justified because it cannot drift. A name can.
