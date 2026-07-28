# Value-Dated Balance and Retroactive Accrual Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the value date load-bearing — give every entry its own value date, add a value-dated balance, and turn daily interest accrual from an incrementing counter into a recomputation that corrects itself when a backdated posting arrives.

**Architecture:** `ledger.Entry` gains a `ValueDate` defaulted from its transaction. Two new store aggregates — a point balance and a per-day movement series — join `BookBalance` behind the same rules. `interest.AccrueSeries` folds that series into gross interest over a window that starts at the last repricing. Each accrual run recomputes gross and posts the change in the rounded value, which is the idiom the repo already uses, so a backdated posting trues itself up on the next run with no reversal and no rewind.

**Tech Stack:** Go 1.x (no new dependencies), PostgreSQL via pgx, Next.js/TypeScript for the web layer.

**Spec:** `docs/superpowers/specs/2026-07-28-value-dated-balance-design.md`. Read it before starting; this plan implements it and does not repeat its reasoning.

## Global Constraints

- **Both stores stay green, and behave identically.** `make test` (in-memory) and `make test-pg` (Postgres) must both pass. `store/pg` must never accept or refuse a write that `store/mem` handles differently — `store/storetest` is the conformance suite that enforces it, and every new store method gets cases there.
- **One migration.** There is no deployed database. Schema changes edit `store/pg/schema/0001_init.sql` in place; do not add `0002_*.sql`.
- **Schema comments are domain content.** New columns get a `COMMENT`-style block comment explaining the domain reason, per `CLAUDE.md`.
- **Domain facts stay consistent across layers.** A claim corrected in `README.md` is corrected in `web/src/components/hint-content.ts` and `web/src/lib/quiz/chapters/*.ts` too.
- **A `[[wiki-link]]` to a key absent from `hint-content.ts` throws at runtime** and takes every dev route down. `npm run test` catches it in hint bodies *and* quiz explanations.
- **`web/src/lib/quiz/diversity.test.ts`** holds each chapter to 18–22 questions, ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty tiers. Rewrite questions rather than deleting them.
- **`gofmt` and `go vet` are gates** in `make test`.
- **Amounts are unsigned `ledger.Amount` with an explicit `Direction`.** The ledger refuses a zero or negative entry amount. A negative computed delta posts as `|delta|` with directions swapped, never as a negative amount.
- **Commit after every task.** Commit messages follow the repo's existing style: lowercase `type(scope): summary`, then prose explaining the reasoning.

---

### Task 1: Entry-level value date

**Files:**
- Modify: `ledger/types.go:194-199` (the `Entry` struct)
- Modify: `ledger/book.go:583-596` (date defaulting and entry ID assignment)
- Modify: `store/mem/tx.go` (no change needed — entries are stored by value; verify)
- Modify: `store/pg/tx_ledger.go:463-470` (entry INSERT), `:574-580` (entry scan), and the `transactionColumns` constant
- Modify: `store/pg/schema/0001_init.sql:166-179` (the `entries` table and its comment), `:182` (`entries_account_idx`)
- Test: `store/storetest/storetest.go`, `ledger/book_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `ledger.Entry.ValueDate time.Time` — zero on input means "the transaction's"; always concrete once stored.

- [ ] **Step 1: Write the failing conformance test**

Add to `store/storetest/storetest.go`, inside the same `t.Run` group as the other transaction cases:

```go
t.Run("EntryValueDateDefaultsToTransaction", func(t *testing.T) {
	ctx := context.Background()
	value := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	var got ledger.Transaction
	if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
		err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
			ID:        "txn_vd_default",
			ValueDate: value,
			Entries: []ledger.Entry{
				{ID: "ent_1", AccountID: "100.001.001", Amount: 500, Direction: ledger.Debit, ValueDate: value},
				{ID: "ent_2", AccountID: "200.001.001", Amount: 500, Direction: ledger.Credit, ValueDate: value},
			},
		})
		if err != nil {
			return err
		}
		got, err = tx.GetTransaction(ctx, bookA, "txn_vd_default")
		return err
	}); err != nil {
		t.Fatalf("put/get: %v", err)
	}
	for i, e := range got.Entries {
		if !e.ValueDate.Equal(value) {
			t.Errorf("entry %d value date = %v, want %v", i, e.ValueDate, value)
		}
	}
})

t.Run("EntriesKeepDivergentValueDates", func(t *testing.T) {
	ctx := context.Background()
	early := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	var got ledger.Transaction
	if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
		err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
			ID:        "txn_vd_split",
			ValueDate: late,
			Entries: []ledger.Entry{
				{ID: "ent_3", AccountID: "100.001.001", Amount: 500, Direction: ledger.Debit, ValueDate: early},
				{ID: "ent_4", AccountID: "200.001.001", Amount: 500, Direction: ledger.Credit, ValueDate: late},
			},
		})
		if err != nil {
			return err
		}
		got, err = tx.GetTransaction(ctx, bookA, "txn_vd_split")
		return err
	}); err != nil {
		t.Fatalf("put/get: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.Entries))
	}
	if !got.Entries[0].ValueDate.Equal(early) {
		t.Errorf("leg 0 value date = %v, want %v", got.Entries[0].ValueDate, early)
	}
	if !got.Entries[1].ValueDate.Equal(late) {
		t.Errorf("leg 1 value date = %v, want %v", got.Entries[1].ValueDate, late)
	}
})
```

And add to `ledger/book_test.go`:

```go
func TestPostTransactionDefaultsEntryValueDate(t *testing.T) {
	ctx := context.Background()
	b, _ := newTestBook(t)
	debit, credit := newTestAccounts(t, b)

	value := time.Date(2026, 5, 4, 9, 30, 0, 0, time.UTC)
	posted, err := b.PostTransaction(ctx, ledger.PostTransactionRequest{
		ValueDate: value,
		Entries: []ledger.Entry{
			{AccountID: debit, Amount: 1_000, Direction: ledger.Debit},
			{AccountID: credit, Amount: 1_000, Direction: ledger.Credit},
		},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	for i, e := range posted.Entries {
		if !e.ValueDate.Equal(value) {
			t.Errorf("entry %d value date = %v, want %v", i, e.ValueDate, value)
		}
	}
}

func TestPostTransactionKeepsExplicitEntryValueDate(t *testing.T) {
	ctx := context.Background()
	b, _ := newTestBook(t)
	debit, credit := newTestAccounts(t, b)

	txValue := time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)
	legValue := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	posted, err := b.PostTransaction(ctx, ledger.PostTransactionRequest{
		ValueDate: txValue,
		Entries: []ledger.Entry{
			{AccountID: debit, Amount: 1_000, Direction: ledger.Debit, ValueDate: legValue},
			{AccountID: credit, Amount: 1_000, Direction: ledger.Credit},
		},
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if !posted.Entries[0].ValueDate.Equal(legValue) {
		t.Errorf("explicit leg value date = %v, want %v", posted.Entries[0].ValueDate, legValue)
	}
	if !posted.Entries[1].ValueDate.Equal(txValue) {
		t.Errorf("defaulted leg value date = %v, want %v", posted.Entries[1].ValueDate, txValue)
	}
}
```

If `newTestBook`/`newTestAccounts` do not exist under those names in `ledger/book_test.go`, use whatever the file's existing tests use to build a book and two accounts — do not invent new helpers.

- [ ] **Step 2: Run the tests to verify they fail**

```
go test ./ledger/... ./store/... -run 'ValueDate' -v
```

Expected: FAIL — `unknown field ValueDate in struct literal of type ledger.Entry`.

- [ ] **Step 3: Add the field**

`ledger/types.go`, replacing the `Entry` struct:

```go
// Entry is one leg of a transaction: an amount in one direction against one
// account.
type Entry struct {
	ID        EntryID
	AccountID AccountID
	Amount    Amount
	Direction Direction

	// ValueDate is when this leg takes economic effect. Zero on input means
	// the transaction's; PostTransaction resolves it, so a stored entry always
	// carries a concrete date and no reader has to fall back to the parent.
	//
	// It lives here rather than only on the Transaction because the two legs of
	// one event can legitimately value-date differently — an outbound transfer
	// debits the customer on the day it is debited, while the bank's clearing
	// position settles days later. Unlike an entry's asset, which is always its
	// account's and so would only create disagreement if stored twice, an
	// entry's value date is genuinely not derivable from its transaction.
	ValueDate time.Time
}
```

- [ ] **Step 4: Resolve the default in the posting path**

`ledger/book.go`, in the "Assign IDs to entries" loop (currently lines 588-596), replace the loop body:

```go
	// Assign IDs to entries, and resolve each leg's value date.
	entries := make([]Entry, len(req.Entries))
	for i, e := range req.Entries {
		id, err := tx.NextID(ctx, s.id, "ent")
		if err != nil {
			return Transaction{}, err
		}
		e.ID = EntryID(id)
		if e.ValueDate.IsZero() {
			e.ValueDate = valueDate
		}
		entries[i] = e
	}
```

- [ ] **Step 5: Persist it in `store/pg`**

In `store/pg/tx_ledger.go`, the entry INSERT:

```go
	for i, e := range txn.Entries {
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO entries (book_id, transaction_id, position, id, account_id, amount, direction, value_date)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			string(book), string(txn.ID), i, string(e.ID), string(e.AccountID), e.Amount, int16(e.Direction),
			nullTime(e.ValueDate),
		); err != nil {
			return fmt.Errorf("pg: put transaction %s entry %d: %w", txn.ID, i, err)
		}
	}
```

Add `e.value_date` to the `transactionColumns` constant (find it near the top of the file; it lists the joined entry columns), then extend the scan in `queryTransactions`:

```go
		var (
			txn                    ledger.Transaction
			status                 int16
			booking, value, create *time.Time
			metadata               []byte
			entryID, accountID     *string
			amount                 *int64
			direction              *int16
			entryValue             *time.Time
		)
		if err := rows.Scan(
			&txn.ID, &txn.IdempotencyKey, &booking, &value, &status,
			&txn.Description, &metadata, &txn.ReversalOf, &create,
			&entryID, &accountID, &amount, &direction, &entryValue,
		); err != nil {
			return nil, fmt.Errorf("pg: query transactions: %w", err)
		}
```

and the entry append:

```go
		if entryID != nil {
			out[at].Entries = append(out[at].Entries, ledger.Entry{
				ID:        ledger.EntryID(*entryID),
				AccountID: ledger.AccountID(*accountID),
				Amount:    ledger.Amount(*amount),
				Direction: ledger.Direction(*direction),
				ValueDate: readTime(entryValue),
			})
		}
```

`store/mem` needs no change: it stores `ledger.Transaction` by value, so the new field rides along. Verify by reading `store/mem/tx.go`'s `PutTransaction`; if it copies entries field-by-field anywhere, add `ValueDate` there.

- [ ] **Step 6: Add the column and widen the index**

`store/pg/schema/0001_init.sql`, replacing the `entries` comment block and table:

```sql
-- entries.position is explicit because Transaction.Entries is an ordered slice
-- and a table has no order. entries.account_id carries no foreign key, for the
-- same reason accounts.subledger_id does not.
--
-- entries.value_date is the one date that is NOT a copy. An entry's asset is
-- always its account's, which is why accounts.asset exists and entries.asset
-- does not — a second copy could only disagree. An entry's value date is a
-- different case: the two legs of one event can legitimately take economic
-- effect on different days. An outbound transfer debits the payer's account on
-- the day it is debited, while the credit into the bank's clearing suspense
-- carries the interbank settlement date. Storing only transactions.value_date
-- would force one of those two to be wrong, and interest is computed from this
-- column.
CREATE TABLE entries (
    book_id        TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    position       INTEGER NOT NULL,
    id             TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    amount         BIGINT NOT NULL,
    direction      SMALLINT NOT NULL,
    value_date     TIMESTAMPTZ,
    PRIMARY KEY (book_id, transaction_id, position),
    FOREIGN KEY (book_id, transaction_id) REFERENCES transactions (book_id, id) ON DELETE CASCADE
);
```

and the index:

```sql
-- The value_date suffix serves the value-dated balance and the per-day movement
-- series. The (book_id, account_id) prefix is unchanged, so BookBalance keeps
-- the index it always had.
CREATE INDEX entries_account_idx ON entries (book_id, account_id, value_date);
```

`value_date` is nullable rather than `NOT NULL` deliberately: `store/mem` accepts a `PutTransaction` carrying a zero-valued entry date (it is a plain struct field), so a `NOT NULL` here would make `store/pg` refuse a write `store/mem` accepts. Resolution is `ledger.Book`'s job, and `PostTransaction` does it.

- [ ] **Step 7: Run the tests to verify they pass**

```
go test ./ledger/... ./store/... -run 'ValueDate' -v
make test
```

Expected: PASS. Then, if Docker is available:

```
make test-pg
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add ledger/types.go ledger/book.go ledger/book_test.go store/pg/tx_ledger.go store/pg/schema/0001_init.sql store/storetest/storetest.go
git commit -m "feat(ledger): give an entry its own value date

The two legs of one event can legitimately take economic effect on
different days, so an entry's value date is not derivable from its
transaction the way its asset is derivable from its account. Zero on
input still means the transaction's; PostTransaction resolves it once,
so every stored entry carries a concrete date."
```

---

### Task 2: `ValueDateBalance`

**Files:**
- Create: `ledger/dates.go`
- Modify: `ledger/store.go:82-84` (the `Tx` interface, beside `BookBalance`)
- Modify: `ledger/book.go:858-870` (beside `Book.BookBalance`)
- Modify: `store/mem/tx.go:272-296`, `store/pg/tx_ledger.go:618-638`
- Modify: `interest/daycount.go:65-68` (delegate `truncateToDay`)
- Test: `store/storetest/storetest.go`, `ledger/dates_test.go`

**Interfaces:**
- Consumes: `ledger.Entry.ValueDate` (Task 1).
- Produces:
  - `func ledger.DayStart(t time.Time) time.Time` — UTC midnight of `t`'s day.
  - `func ledger.NextDay(t time.Time) time.Time` — `DayStart(t)` plus 24h, i.e. the exclusive end of `t`'s day.
  - `Tx.ValueDateBalance(ctx, book BookID, id AccountID, normal Direction, before time.Time) (Amount, error)` — `before` is an **exclusive** bound the caller has already snapped to a UTC midnight.
  - `func (s *Book) ValueDateBalance(ctx context.Context, accountID AccountID, asOf time.Time) (Amount, error)`

- [ ] **Step 1: Write the failing tests**

Create `ledger/dates_test.go`:

```go
package ledger_test

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
)

func TestDayStartDiscardsTimeOfDay(t *testing.T) {
	morning := time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 3, 15, 23, 0, 0, 0, time.UTC)
	want := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	if got := ledger.DayStart(morning); !got.Equal(want) {
		t.Errorf("DayStart(morning) = %v, want %v", got, want)
	}
	if got := ledger.DayStart(evening); !got.Equal(want) {
		t.Errorf("DayStart(evening) = %v, want %v", got, want)
	}
}

func TestDayStartNormalisesToUTC(t *testing.T) {
	// 2026-03-15 01:00 +02:00 is 2026-03-14 23:00 UTC, so its UTC day is the 14th.
	zone := time.FixedZone("CEST", 2*60*60)
	got := ledger.DayStart(time.Date(2026, 3, 15, 1, 0, 0, 0, zone))
	want := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("DayStart = %v, want %v", got, want)
	}
}

func TestNextDayIsExclusiveEnd(t *testing.T) {
	got := ledger.NextDay(time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC))
	want := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("NextDay = %v, want %v", got, want)
	}
}
```

Add the conformance case to `store/storetest/storetest.go`:

```go
t.Run("ValueDateBalanceCountsOnlyEntriesBeforeTheBound", func(t *testing.T) {
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 4, d, 0, 0, 0, 0, time.UTC) }

	if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
		// Three debits on three consecutive days, one carrying a time of day.
		for i, when := range []time.Time{
			day(10),
			day(11).Add(23 * time.Hour),
			day(12),
		} {
			err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:        ledger.TransactionID("txn_vdb_" + strconv.Itoa(i)),
				ValueDate: when,
				Entries: []ledger.Entry{
					{ID: ledger.EntryID("ent_vdb_d" + strconv.Itoa(i)), AccountID: "900.001.001", Amount: 100, Direction: ledger.Debit, ValueDate: when},
					{ID: ledger.EntryID("ent_vdb_c" + strconv.Itoa(i)), AccountID: "900.001.002", Amount: 100, Direction: ledger.Credit, ValueDate: when},
				},
			})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		before time.Time
		want   ledger.Amount
	}{
		{day(10), 0},   // nothing value-dated before the 10th
		{day(11), 100}, // the 10th only
		{day(12), 200}, // the 10th and the 11th, time of day included
		{day(13), 300},
	}
	for _, c := range cases {
		var got ledger.Amount
		if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDateBalance(ctx, bookA, "900.001.001", ledger.Debit, c.before)
			return err
		}); err != nil {
			t.Fatalf("balance before %v: %v", c.before, err)
		}
		if got != c.want {
			t.Errorf("balance before %v = %d, want %d", c.before, got, c.want)
		}
	}
})

t.Run("ValueDateBalanceOfUnknownAccountIsZero", func(t *testing.T) {
	ctx := context.Background()
	var got ledger.Amount
	if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
		var err error
		got, err = tx.ValueDateBalance(ctx, bookA, "999.999.001", ledger.Debit,
			time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
		return err
	}); err != nil {
		t.Fatalf("balance: %v", err)
	}
	if got != 0 {
		t.Errorf("balance = %d, want 0", got)
	}
})
```

Add `"strconv"` to the file's imports if it is not already there. Reuse whatever account IDs the surrounding cases create; `900.001.001` / `900.001.002` are placeholders — if the fixture in this file uses different IDs, use those and adjust the expected numbers only if the fixture already posts to them.

- [ ] **Step 2: Run to verify failure**

```
go test ./ledger/... ./store/... -run 'DayStart|NextDay|ValueDateBalance' -v
```

Expected: FAIL — `undefined: ledger.DayStart`, `tx.ValueDateBalance undefined`.

- [ ] **Step 3: Add the date helpers**

Create `ledger/dates.go`:

```go
package ledger

import "time"

// DayStart is UTC midnight of t's day.
//
// A business date is a date. An end-of-day run at 23:00 must cover the same day
// as one at 09:00, and a value date carrying a time of day must land in the same
// bucket as one that does not. Every day boundary in this system is computed
// here, in Go, and passed to the store as an ordinary timestamp bound — rather
// than each store truncating for itself, which is one DST-adjacent edge case
// away from store/pg and store/mem disagreeing about which day an entry is in.
func DayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// NextDay is the exclusive end of t's day: DayStart of the following day.
func NextDay(t time.Time) time.Time {
	return DayStart(t).AddDate(0, 0, 1)
}
```

Then dedupe `interest/daycount.go` — replace its `truncateToDay` body so the two can never drift:

```go
func truncateToDay(t time.Time) time.Time { return ledger.DayStart(t) }
```

adding `"github.com/raphi011/cbs/ledger"` to that file's imports. (`interest` already depends on `ledger` for `ledger.Amount`, so this introduces no new edge. Confirm the import path matches the module path in `go.mod`.)

- [ ] **Step 4: Add the store method**

`ledger/store.go`, immediately after `BookBalance`:

```go
	// ValueDateBalance is BookBalance restricted to entries that take economic
	// effect before the bound: it aggregates entries whose value date is
	// strictly less than before.
	//
	// before is an exclusive UTC-midnight bound the caller has already snapped
	// with NextDay. Stores compare raw timestamps against it and do no
	// truncation of their own — see DayStart.
	//
	// It obeys BookBalance's two rules. Reversed transactions count, because a
	// reversal posts its own mirrored entries and those are what cancel the
	// original. An account with no entries is 0, including one that does not
	// exist; callers wanting ErrAccountNotFound read the account first.
	ValueDateBalance(ctx context.Context, book BookID, id AccountID, normal Direction, before time.Time) (Amount, error)
```

`store/mem/tx.go`, after `BookBalance`:

```go
// ValueDateBalance is BookBalance restricted to entries whose value date falls
// strictly before the bound. See ledger.Tx for the contract.
func (t *tx) ValueDateBalance(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction, before time.Time) (ledger.Amount, error) {
	var balance ledger.Amount
	for _, txn := range t.state.transactions[book] {
		for _, e := range txn.Entries {
			if e.AccountID != id || !e.ValueDate.Before(before) {
				continue
			}
			if e.Direction == normal {
				balance += e.Amount
			} else {
				balance -= e.Amount
			}
		}
	}
	return balance, nil
}
```

`store/pg/tx_ledger.go`, after `BookBalance`:

```go
// ValueDateBalance is BookBalance restricted to entries whose value date falls
// strictly before the bound. See ledger.Tx for the contract.
func (t *tx) ValueDateBalance(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction, before time.Time) (ledger.Amount, error) {
	var balance ledger.Amount
	err := t.tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN e.direction = $3 THEN e.amount ELSE -e.amount END), 0)
		FROM entries e
		WHERE e.book_id = $1 AND e.account_id = $2 AND e.value_date < $4`,
		string(book), string(id), int16(normal), before).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("pg: value date balance %s: %w", id, err)
	}
	return balance, nil
}
```

- [ ] **Step 5: Add the `Book` method**

`ledger/book.go`, after `BookBalance`:

```go
// ValueDateBalance computes an account's balance as of the end of asOf's day —
// the balance the book's interest engines consume.
//
// The book balance answers "what has been recorded"; this answers "what has
// taken economic effect". They differ whenever a posting is value-dated away
// from its booking date, which an outbound payment's clearing leg always is.
//
// Entries value-dated on asOf itself count: a day's interest accrues on that
// day's closing balance.
//
// Returns ErrAccountNotFound if the account does not exist.
func (s *Book) ValueDateBalance(ctx context.Context, accountID AccountID, asOf time.Time) (Amount, error) {
	var out Amount
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		acct, err := tx.GetAccount(ctx, s.id, accountID)
		if err != nil {
			return err
		}
		out, err = tx.ValueDateBalance(ctx, s.id, accountID, acct.Type.NormalBalance(), NextDay(asOf))
		return err
	})
	return out, err
}
```

- [ ] **Step 6: Run the tests**

```
go test ./ledger/... ./interest/... ./store/... -v
make test
```

Expected: PASS. Then `make test-pg` if Docker is available.

- [ ] **Step 7: Commit**

```bash
git add ledger/dates.go ledger/dates_test.go ledger/store.go ledger/book.go interest/daycount.go store/mem/tx.go store/pg/tx_ledger.go store/storetest/storetest.go
git commit -m "feat(ledger): compute a value-dated balance

The README has described this balance since the beginning and nothing
computed it. The book balance answers what has been recorded; this
answers what has taken economic effect, and the two differ whenever a
posting is value-dated away from its booking date.

Day boundaries are snapped in Go and passed to the store as ordinary
timestamp bounds, rather than each store truncating for itself: two
implementations that each decide what a day is are one DST-adjacent
edge case away from disagreeing, and disagreeing is the single thing
the store layer must never do."
```

---

### Task 3: `ValueDatedSeries`

**Files:**
- Modify: `ledger/types.go` (add `DayMovement`, `Series`)
- Modify: `ledger/store.go` (the `Tx` interface, after `ValueDateBalance`)
- Modify: `store/mem/tx.go`, `store/pg/tx_ledger.go`
- Test: `store/storetest/storetest.go`

**Interfaces:**
- Consumes: `Tx.ValueDateBalance`, `ledger.DayStart`, `ledger.NextDay` (Task 2).
- Produces:
  - `ledger.DayMovement{Day time.Time; Amount Amount}`
  - `ledger.Series{Opening Amount; Movements []DayMovement}`
  - `Tx.ValueDatedSeries(ctx, book BookID, id AccountID, normal Direction, from, to time.Time) (Series, error)` — `from` inclusive, `to` exclusive, both UTC midnights supplied by the caller.

- [ ] **Step 1: Write the failing conformance test**

Add to `store/storetest/storetest.go`:

```go
t.Run("ValueDatedSeriesBucketsByDayAndCarriesAnOpening", func(t *testing.T) {
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }

	// Debits of 100 on the 1st, 200 twice on the 5th (one with a time of day),
	// and 400 on the 9th. The window below starts on the 4th, so the 1st is
	// opening and the 9th is outside.
	posts := []struct {
		id     string
		when   time.Time
		amount ledger.Amount
	}{
		{"a", day(1), 100},
		{"b", day(5), 200},
		{"c", day(5).Add(17 * time.Hour), 200},
		{"d", day(9), 400},
	}
	if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
		for _, p := range posts {
			err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:        ledger.TransactionID("txn_vds_" + p.id),
				ValueDate: p.when,
				Entries: []ledger.Entry{
					{ID: ledger.EntryID("ent_vds_d" + p.id), AccountID: "901.001.001", Amount: p.amount, Direction: ledger.Debit, ValueDate: p.when},
					{ID: ledger.EntryID("ent_vds_c" + p.id), AccountID: "901.001.002", Amount: p.amount, Direction: ledger.Credit, ValueDate: p.when},
				},
			})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var got ledger.Series
	if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
		var err error
		got, err = tx.ValueDatedSeries(ctx, bookA, "901.001.001", ledger.Debit, day(4), day(9))
		return err
	}); err != nil {
		t.Fatalf("series: %v", err)
	}

	if got.Opening != 100 {
		t.Errorf("opening = %d, want 100", got.Opening)
	}
	if len(got.Movements) != 1 {
		t.Fatalf("movements = %d, want 1 (only the 5th; the 9th is outside the window)", len(got.Movements))
	}
	if !got.Movements[0].Day.Equal(day(5)) {
		t.Errorf("movement day = %v, want %v", got.Movements[0].Day, day(5))
	}
	if got.Movements[0].Amount != 400 {
		t.Errorf("movement amount = %d, want 400 (both of the 5th's postings, netted)", got.Movements[0].Amount)
	}
})

t.Run("ValueDatedSeriesSignsByNormalDirection", func(t *testing.T) {
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }

	// The credit side of the same postings, read with Credit as normal.
	var got ledger.Series
	if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
		var err error
		got, err = tx.ValueDatedSeries(ctx, bookA, "901.001.002", ledger.Credit, day(4), day(9))
		return err
	}); err != nil {
		t.Fatalf("series: %v", err)
	}
	if got.Opening != 100 {
		t.Errorf("opening = %d, want 100", got.Opening)
	}
	if len(got.Movements) != 1 || got.Movements[0].Amount != 400 {
		t.Errorf("movements = %+v, want one of 400", got.Movements)
	}

	// And read against the wrong normal, everything inverts.
	if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
		var err error
		got, err = tx.ValueDatedSeries(ctx, bookA, "901.001.002", ledger.Debit, day(4), day(9))
		return err
	}); err != nil {
		t.Fatalf("series: %v", err)
	}
	if got.Opening != -100 || got.Movements[0].Amount != -400 {
		t.Errorf("inverted series = %+v / opening %d, want -400 / -100", got.Movements, got.Opening)
	}
})

t.Run("ValueDatedSeriesOfEmptyWindowIsEmpty", func(t *testing.T) {
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }
	var got ledger.Series
	if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
		var err error
		got, err = tx.ValueDatedSeries(ctx, bookA, "901.001.001", ledger.Debit, day(6), day(9))
		return err
	}); err != nil {
		t.Fatalf("series: %v", err)
	}
	if got.Opening != 500 {
		t.Errorf("opening = %d, want 500 (the 1st and both of the 5th)", got.Opening)
	}
	if len(got.Movements) != 0 {
		t.Errorf("movements = %+v, want none", got.Movements)
	}
})
```

- [ ] **Step 2: Run to verify failure**

```
go test ./store/... -run 'ValueDatedSeries' -v
```

Expected: FAIL — `undefined: ledger.Series`.

- [ ] **Step 3: Add the types**

`ledger/types.go`, next to `Entry`:

```go
// DayMovement is an account's net movement on one value date, signed by the
// account's normal direction.
type DayMovement struct {
	Day    time.Time // UTC midnight
	Amount Amount
}

// Series is an account's value-dated balance history over a window: the balance
// carried into it, and the net movement on each day inside it that had any.
//
// It exists so that interest can be computed on each day's own closing balance
// without one query per day. Days with no movement are omitted, because the
// balance did not change on them — a consumer folds Opening forward and applies
// each movement as it reaches its day.
type Series struct {
	Opening   Amount
	Movements []DayMovement // ascending by Day
}
```

- [ ] **Step 4: Add the store method**

`ledger/store.go`, after `ValueDateBalance`:

```go
	// ValueDatedSeries returns the balance carried into from, plus the net
	// movement on each value date in [from, to) that had any.
	//
	// from and to are UTC midnights the caller has already snapped, to
	// exclusive. Movements are ascending and days with no movement are omitted.
	// Opening is exactly ValueDateBalance at from, and the same two rules apply:
	// reversed transactions count, and an unknown account is empty rather than
	// an error.
	ValueDatedSeries(ctx context.Context, book BookID, id AccountID, normal Direction, from, to time.Time) (Series, error)
```

`store/mem/tx.go`:

```go
// ValueDatedSeries buckets an account's entries by value date. See ledger.Tx
// for the contract.
func (t *tx) ValueDatedSeries(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction, from, to time.Time) (ledger.Series, error) {
	opening, err := t.ValueDateBalance(ctx, book, id, normal, from)
	if err != nil {
		return ledger.Series{}, err
	}

	byDay := make(map[time.Time]ledger.Amount)
	for _, txn := range t.state.transactions[book] {
		for _, e := range txn.Entries {
			if e.AccountID != id || e.ValueDate.Before(from) || !e.ValueDate.Before(to) {
				continue
			}
			day := ledger.DayStart(e.ValueDate)
			if e.Direction == normal {
				byDay[day] += e.Amount
			} else {
				byDay[day] -= e.Amount
			}
		}
	}

	out := ledger.Series{Opening: opening, Movements: make([]ledger.DayMovement, 0, len(byDay))}
	for day, amount := range byDay {
		out.Movements = append(out.Movements, ledger.DayMovement{Day: day, Amount: amount})
	}
	sort.Slice(out.Movements, func(i, j int) bool {
		return out.Movements[i].Day.Before(out.Movements[j].Day)
	})
	return out, nil
}
```

Add `"sort"` to the file's imports if absent.

Note that a day whose postings net to zero is still emitted. That is harmless — folding it changes nothing — and it keeps both stores' output identical without either having to filter.

`store/pg/tx_ledger.go`:

```go
// ValueDatedSeries buckets an account's entries by value date in SQL. See
// ledger.Tx for the contract.
//
// date_trunc runs on the value read as UTC, and the result is snapped again
// with ledger.DayStart on the way out, so the day a movement lands on is
// decided by the same rule store/mem uses rather than by the session timezone.
func (t *tx) ValueDatedSeries(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction, from, to time.Time) (ledger.Series, error) {
	opening, err := t.ValueDateBalance(ctx, book, id, normal, from)
	if err != nil {
		return ledger.Series{}, err
	}

	rows, err := t.tx.Query(ctx, `
		SELECT date_trunc('day', e.value_date AT TIME ZONE 'UTC') AS day,
		       SUM(CASE WHEN e.direction = $3 THEN e.amount ELSE -e.amount END)
		FROM entries e
		WHERE e.book_id = $1 AND e.account_id = $2
		  AND e.value_date >= $4 AND e.value_date < $5
		GROUP BY 1
		ORDER BY 1`,
		string(book), string(id), int16(normal), from, to)
	if err != nil {
		return ledger.Series{}, fmt.Errorf("pg: value dated series %s: %w", id, err)
	}
	defer rows.Close()

	out := ledger.Series{Opening: opening, Movements: make([]ledger.DayMovement, 0)}
	for rows.Next() {
		var (
			day    time.Time
			amount ledger.Amount
		)
		if err := rows.Scan(&day, &amount); err != nil {
			return ledger.Series{}, fmt.Errorf("pg: value dated series %s: %w", id, err)
		}
		out.Movements = append(out.Movements, ledger.DayMovement{
			Day:    ledger.DayStart(day.UTC()),
			Amount: amount,
		})
	}
	if err := rows.Err(); err != nil {
		return ledger.Series{}, fmt.Errorf("pg: value dated series %s: %w", id, err)
	}
	return out, nil
}
```

- [ ] **Step 5: Run the tests**

```
go test ./store/... -run 'ValueDatedSeries' -v
make test
```

Expected: PASS. Then `make test-pg`.

If the pg `date_trunc` scan produces a day one off from `store/mem`'s in `make test-pg`, that is the conformance suite doing its job — the fix is in the scan, not in the test.

- [ ] **Step 6: Commit**

```bash
git add ledger/types.go ledger/store.go store/mem/tx.go store/pg/tx_ledger.go store/storetest/storetest.go
git commit -m "feat(ledger): return an account's value-dated movement per day

Interest wants each day's own closing balance, and asking for a balance
per day would be one query per day. A single grouped aggregate returns
the balance carried into the window plus the days it moved, which is
enough to fold the whole series forward in Go.

Days that did not move are omitted rather than emitted as zero: the
balance did not change on them, and a consumer that folds forward does
not need to be told so."
```

---

### Task 4: `interest.AccrueSeries`

**Files:**
- Create: `interest/series.go`, `interest/series_test.go`

**Interfaces:**
- Consumes: `ledger.Series`, `ledger.DayMovement` (Task 3).
- Produces:
  - `type interest.Period func(balance ledger.Amount, from, to time.Time) Accrued`
  - `func interest.AccrueSeries(s ledger.Series, from, to time.Time, p Period) Accrued`

**The convention this encodes,** because it is the one thing in this task that is easy to get subtly wrong: accruing from `from` to `to` covers the day *slots* `from+1 … to`, which is exactly `DayCount.Days(from, to)` of them, and slot `d` accrues on `B(d)` — the balance including everything value-dated on `d` itself. On a daily run (`from = to - 1 day`) that is one slot, at today's closing balance, which is what the current code computes. Get this wrong by one and every seed figure moves.

- [ ] **Step 1: Write the failing tests**

Create `interest/series_test.go`:

```go
package interest_test

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// flat is the Period both product layers pass: one rate, one convention.
func flat(rate interest.Rate, dc interest.DayCount) interest.Period {
	return func(balance ledger.Amount, from, to time.Time) interest.Accrued {
		return interest.Accrue(balance, rate, dc, from, to)
	}
}

func TestAccrueSeriesConstantBalanceMatchesAccrue(t *testing.T) {
	from, to := day(2026, time.January, 1), day(2026, time.January, 31)
	s := ledger.Series{Opening: 100_000}

	got := interest.AccrueSeries(s, from, to, flat(50_000, interest.ACT365))
	want := interest.Accrue(100_000, 50_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (a series with no movement is one run)", got, want)
	}
}

func TestAccrueSeriesSingleDayUsesClosingBalance(t *testing.T) {
	// The daily-run case: one slot, at the balance including that day's movement.
	from, to := day(2026, time.January, 10), day(2026, time.January, 11)
	s := ledger.Series{
		Opening:   100_000,
		Movements: []ledger.DayMovement{{Day: to, Amount: 50_000}},
	}

	got := interest.AccrueSeries(s, from, to, flat(50_000, interest.ACT365))
	want := interest.Accrue(150_000, 50_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (day 11 accrues on day 11's closing balance)", got, want)
	}
}

func TestAccrueSeriesSplitsAtAMovement(t *testing.T) {
	from, to := day(2026, time.January, 1), day(2026, time.January, 11)
	// Balance is 100_000 for slots 2..5, then 150_000 for slots 6..11.
	s := ledger.Series{
		Opening:   100_000,
		Movements: []ledger.DayMovement{{Day: day(2026, time.January, 6), Amount: 50_000}},
	}

	p := flat(50_000, interest.ACT365)
	got := interest.AccrueSeries(s, from, to, p)
	want := p(100_000, day(2026, time.January, 2), day(2026, time.January, 6)) +
		p(150_000, day(2026, time.January, 6), day(2026, time.January, 12))
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d", got, want)
	}
}

func TestAccrueSeriesFoldsMovementsAtOrBeforeFromIntoOpening(t *testing.T) {
	from, to := day(2026, time.January, 10), day(2026, time.January, 12)
	s := ledger.Series{
		Opening:   100_000,
		Movements: []ledger.DayMovement{{Day: from, Amount: 50_000}},
	}

	got := interest.AccrueSeries(s, from, to, flat(50_000, interest.ACT365))
	want := interest.Accrue(150_000, 50_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (a movement on `from` is already in force for every slot)", got, want)
	}
}

func TestAccrueSeriesHandlesNegativeBalances(t *testing.T) {
	// Every overdraft case: the deposit layer negates the balance itself, but a
	// Period may legitimately be handed a negative one.
	from, to := day(2026, time.January, 1), day(2026, time.January, 11)
	s := ledger.Series{Opening: -100_000}

	got := interest.AccrueSeries(s, from, to, func(balance ledger.Amount, f, t2 time.Time) interest.Accrued {
		if balance >= 0 {
			return 0
		}
		return interest.Accrue(-balance, 150_000, interest.ACT365, f, t2)
	})
	want := interest.Accrue(100_000, 150_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d", got, want)
	}
}

func TestAccrueSeriesEmptyWindowAccruesNothing(t *testing.T) {
	d := day(2026, time.January, 10)
	s := ledger.Series{Opening: 100_000}

	if got := interest.AccrueSeries(s, d, d, flat(50_000, interest.ACT365)); got != 0 {
		t.Errorf("AccrueSeries over an empty window = %d, want 0", got)
	}
	if got := interest.AccrueSeries(s, d, d.AddDate(0, 0, -1), flat(50_000, interest.ACT365)); got != 0 {
		t.Errorf("AccrueSeries over a backwards window = %d, want 0", got)
	}
}

func TestAccrueSeriesThirty360TotalsAcrossAMonthEnd(t *testing.T) {
	// 30/360 collapses the 31st. Splitting a month into runs must not change
	// the month's total, only which slot the collapse lands on.
	from, to := day(2026, time.January, 1), day(2026, time.February, 1)
	p := flat(60_000, interest.Thirty360)

	whole := interest.AccrueSeries(ledger.Series{Opening: 1_000_000}, from, to, p)
	split := interest.AccrueSeries(ledger.Series{
		Opening:   1_000_000,
		Movements: []ledger.DayMovement{{Day: day(2026, time.January, 15), Amount: 0}},
	}, from, to, p)

	if whole != split {
		t.Errorf("split series = %d, whole = %d; a zero movement must not change the total", split, whole)
	}
}

func TestAccrueSeriesLeapDayIsADay(t *testing.T) {
	from, to := day(2028, time.February, 27), day(2028, time.March, 1)
	s := ledger.Series{Opening: 1_000_000}

	got := interest.AccrueSeries(s, from, to, flat(365_000, interest.ACT365))
	want := interest.Accrue(1_000_000, 365_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries across Feb 29 = %d, want %d", got, want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```
go test ./interest/... -run 'AccrueSeries' -v
```

Expected: FAIL — `undefined: interest.AccrueSeries`.

- [ ] **Step 3: Implement**

Create `interest/series.go`:

```go
package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Period is the interest a product's terms earn on a constant balance over
// [from, to). It is the shape both product layers already have — lending's is
// Accrue with the facility's rate and convention, deposit's is the tiered
// arranged/unarranged split — so AccrueSeries can drive either without knowing
// anything about pricing.
type Period func(balance ledger.Amount, from, to time.Time) Accrued

// AccrueSeries is interest over a window whose balance changed inside it.
//
// The window [from, to] covers the day slots from+1 … to — exactly
// DayCount.Days(from, to) of them — and slot d accrues on B(d), the balance
// including everything value-dated on d itself. A day's interest is earned on
// that day's closing balance, which is both the conventional rule and what the
// single-balance code it replaces computes when the window is one day long.
//
// The series is folded into runs of constant balance and Period is called once
// per run, so the cost is the number of days the balance moved rather than the
// number of days in the window. Splitting a window into runs changes the result
// slightly against one call over the whole of it, because Accrue truncates its
// integer division per call; the difference is in micro-minor-units, and it is
// the more accurate answer, since the balance genuinely differed across the
// runs.
func AccrueSeries(s ledger.Series, from, to time.Time, p Period) Accrued {
	if !to.After(from) {
		return 0
	}

	balance := s.Opening
	// The first slot is the day after from. A movement on from itself — or
	// before it, which a well-formed Series does not contain — is already in
	// force for every slot in the window.
	cursor := ledger.NextDay(from)
	i := 0
	for ; i < len(s.Movements) && !s.Movements[i].Day.After(from); i++ {
		balance += s.Movements[i].Amount
	}

	var total Accrued
	for ; i < len(s.Movements); i++ {
		m := s.Movements[i]
		if m.Day.After(to) {
			break
		}
		if m.Day.After(cursor) {
			total += p(balance, cursor, m.Day)
			cursor = m.Day
		}
		balance += m.Amount
	}
	return total + p(balance, cursor, ledger.NextDay(to))
}
```

- [ ] **Step 4: Run the tests**

```
go test ./interest/... -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add interest/series.go interest/series_test.go
git commit -m "feat(interest): accrue over a balance that moved

Accrue takes one balance and a period, which is right only when the
balance held for the whole of it. AccrueSeries folds a value-dated
movement series into runs of constant balance and calls a Period once
per run, so a window covering a month costs as many calls as the
balance had distinct values, not as many as it had days.

The convention it pins down: the window [from, to] covers slots
from+1 … to, and slot d accrues on d's closing balance. One day long,
that is what the single-balance code already computes."
```

---

### Task 5: Split the payment debtor leg's value dates

**Files:**
- Modify: `payment/system.go:1040-1052` (the debtor leg posting in `Initiate`)
- Test: `payment/system_test.go` (or whichever file holds `Initiate`'s tests — find it with `grep -rln "func Test.*Initiate" payment/`)

**Interfaces:**
- Consumes: `ledger.Entry.ValueDate` (Task 1).
- Produces: no new API. Behaviour: the customer's GL leg value-dates to `now`; the clearing-suspense leg keeps the scheme's settlement date.

**Do this before the accrual tasks.** With book-balance accrual it is a no-op, so it can land safely on its own. Land it after Task 6 and there is a commit in history where the customer stops paying overdraft interest for the settlement delay.

- [ ] **Step 1: Write the failing test**

Add to the payment test file:

```go
func TestInitiateValueDatesTheCustomerLegToTheDebit(t *testing.T) {
	ctx := context.Background()
	// Build the two-participant fixture the other Initiate tests in this file
	// use, and initiate one SCT.
	net, debtor, creditor, from, to := newTestNetwork(t)

	p, err := net.Initiate(ctx, payment.InitiateRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      from,
		Creditor:    to,
		Amount:      10_000,
		Description: "Rent",
	})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}

	posted, err := debtor.Ledger.GetTransaction(ctx, p.DebtorLegTx)
	if err != nil {
		t.Fatalf("get debtor leg: %v", err)
	}
	if !posted.ValueDate.Equal(p.ValueDate) {
		t.Errorf("transaction value date = %v, want the settlement date %v", posted.ValueDate, p.ValueDate)
	}

	debtorGL := must(debtor.Deposit.GetAccount(ctx, from.Account)).GLAccount
	var customer, suspense ledger.Entry
	for _, e := range posted.Entries {
		if e.AccountID == debtorGL {
			customer = e
		} else {
			suspense = e
		}
	}

	if !customer.ValueDate.Equal(posted.BookingDate) {
		t.Errorf("customer leg value date = %v, want the booking date %v (PSD2 Art. 87(2))",
			customer.ValueDate, posted.BookingDate)
	}
	if !suspense.ValueDate.Equal(p.ValueDate) {
		t.Errorf("suspense leg value date = %v, want the settlement date %v", suspense.ValueDate, p.ValueDate)
	}
	if customer.ValueDate.Equal(suspense.ValueDate) {
		t.Fatal("the two legs must not share a value date; that is the whole point")
	}
}
```

Replace `newTestNetwork` / `must` with whatever the file already uses; do not add helpers if equivalents exist.

- [ ] **Step 2: Run to verify failure**

```
go test ./payment/... -run 'CustomerLeg' -v
```

Expected: FAIL — both legs currently carry `p.ValueDate`.

- [ ] **Step 3: Split the legs**

`payment/system.go`, in `Initiate`, replacing the debtor-leg posting:

```go
	// The two legs of this one event take economic effect on different days,
	// which is why an entry carries its own value date.
	//
	// The customer's leg value-dates to the debit itself: PSD2 Art. 87(2) puts
	// the payer's debit value date no earlier than the moment the amount leaves
	// the account, and the money is gone from the moment this posts. Value-dating
	// it to settlement instead would hand the payer the settlement delay's worth
	// of interest-free credit, which is precisely what the article forbids.
	//
	// The clearing-suspense leg carries the settlement date, because that is
	// when the bank's position against the scheme actually settles.
	posted, err := debtor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":debit",
		Description:    p.Description,
		BookingDate:    now,
		ValueDate:      p.ValueDate,
		Metadata:       paymentMetadata(&p),
		Entries: []ledger.Entry{
			{AccountID: debtorGL, Amount: p.Amount, Direction: ledger.Debit, ValueDate: now},
			{AccountID: debtorAccts.Suspense, Amount: p.Amount, Direction: ledger.Credit, ValueDate: p.ValueDate},
		},
	})
```

- [ ] **Step 4: Run the tests**

```
go test ./payment/... -v
make test
```

Expected: PASS, with no other payment test disturbed — nothing reads entry value dates yet.

- [ ] **Step 5: Commit**

```bash
git add payment/system.go payment/system_test.go
git commit -m "fix(payment): value-date the payer's leg to the debit, not to settlement

Both legs of the debtor posting carried the scheme's settlement date.
For the clearing-suspense leg that is right — the bank's position
settles then. For the customer's leg it is wrong: PSD2 Art. 87(2) puts
the payer's debit value date no earlier than the moment the amount
leaves the account.

Nothing reads entry value dates yet, so this changes no balance today.
It is a prerequisite: point interest at a transaction-level value date
with both legs on T+n and the payer gets the settlement delay's worth
of interest-free credit."
```

---

### Task 6: Recompute the deposit overdraft accrual

**Files:**
- Modify: `deposit/types.go:64-110` (the `Account` struct)
- Modify: `deposit/register.go:240-279` (`SetOverdraftTermsTx`), `:955-1005` (`accrueOverdraftAccountTx`), `:1021-1039` (`overdraftAccrual`), and add a `valueDatedSeriesTx` helper beside `bookBalanceTx` at `:823-830`
- Modify: `ledger/audit.go` (or wherever `EventOverdraftAccrued` is declared — find it with `grep -rn "EventOverdraftAccrued" ledger/`)
- Modify: `deposit/store.go` (the layer's `Tx` interface, if it re-declares ledger methods — check whether it embeds `ledger.Tx`)
- Test: `deposit/register_test.go` (or the file holding the existing accrual tests)

**Interfaces:**
- Consumes: `Tx.ValueDatedSeries` (Task 3), `interest.AccrueSeries`, `interest.Period` (Task 4), `ledger.DayStart`/`NextDay` (Task 2).
- Produces:
  - `deposit.Account.TermsEffectiveFrom time.Time`, `deposit.Account.AccruedGross interest.Accrued`
  - `ledger.EventOverdraftAccrualCorrected` audit event

- [ ] **Step 1: Write the failing tests**

Add to the deposit test file:

```go
func TestOverdraftAccrualCorrectsABackdatedCredit(t *testing.T) {
	ctx := context.Background()
	r, gl, clock := newTestRegister(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	acct := must(r.OpenAccount(ctx, "Bruno", "EUR"))
	must0(r.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365))

	// Overdraw by EUR 200 on day 1, then accrue ten days.
	postTo(t, gl, acct.GLAccount, 20_000, ledger.Debit, start)
	for i := 1; i <= 10; i++ {
		must0(r.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
	}
	after := must(r.GetAccount(ctx, acct.ID)).Accrued.Minor()
	if after <= 0 {
		t.Fatalf("accrued nothing over ten overdrawn days: %d", after)
	}

	// A salary credit arrives on day 11, backdated to day 3: from day 3 the
	// account was never overdrawn, so most of that interest was never owed.
	postTo(t, gl, acct.GLAccount, 20_000, ledger.Credit, start.AddDate(0, 0, 3))

	must0(r.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 11)))
	corrected := must(r.GetAccount(ctx, acct.ID)).Accrued.Minor()
	if corrected >= after {
		t.Errorf("accrued after backdated credit = %d, want less than %d", corrected, after)
	}

	// And the receivable's ledger balance agrees with the record.
	recv := must(gl.BookBalance(ctx, must(r.GetAccount(ctx, acct.ID)).InterestGL))
	if recv != corrected {
		t.Errorf("receivable = %d, accrued = %d; the two must agree", recv, corrected)
	}
}

func TestOverdraftAccrualCorrectsABackdatedDebit(t *testing.T) {
	ctx := context.Background()
	r, gl, clock := newTestRegister(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	acct := must(r.OpenAccount(ctx, "Bruno", "EUR"))
	must0(r.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365))

	postTo(t, gl, acct.GLAccount, 20_000, ledger.Debit, start)
	for i := 1; i <= 10; i++ {
		must0(r.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
	}
	after := must(r.GetAccount(ctx, acct.ID)).Accrued.Minor()

	// A card settlement lands late, backdated to day 3: he was more overdrawn
	// than the ledger knew.
	postTo(t, gl, acct.GLAccount, 20_000, ledger.Debit, start.AddDate(0, 0, 3))

	must0(r.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 11)))
	corrected := must(r.GetAccount(ctx, acct.ID)).Accrued.Minor()
	if corrected <= after {
		t.Errorf("accrued after backdated debit = %d, want more than %d", corrected, after)
	}
}

func TestOverdraftAccrualIgnoresAForwardValueDatedDebit(t *testing.T) {
	ctx := context.Background()
	r, gl, clock := newTestRegister(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	acct := must(r.OpenAccount(ctx, "Bruno", "EUR"))
	must0(r.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365))

	// Booked today, value-dated three days out. It has not taken effect yet.
	postTo(t, gl, acct.GLAccount, 20_000, ledger.Debit, start.AddDate(0, 0, 5))

	must0(r.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))
	if got := must(r.GetAccount(ctx, acct.ID)).Accrued.Minor(); got != 0 {
		t.Errorf("accrued = %d on a debit that has not taken effect, want 0", got)
	}
}

func TestOverdraftAccrualFreezesPriorAccrualOnARepricing(t *testing.T) {
	ctx := context.Background()
	r, gl, clock := newTestRegister(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	acct := must(r.OpenAccount(ctx, "Bruno", "EUR"))
	must0(r.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365))

	postTo(t, gl, acct.GLAccount, 20_000, ledger.Debit, start)
	for i := 1; i <= 10; i++ {
		must0(r.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
	}
	atReprice := must(r.GetAccount(ctx, acct.ID)).Accrued.Minor()

	// Reprice to triple the rate on day 10, then accrue one more day.
	clock.set(start.AddDate(0, 0, 10))
	must0(r.SetOverdraftTerms(ctx, acct.ID, 50_000, 450_000, 0, interest.ACT365))

	after := must(r.GetAccount(ctx, acct.ID))
	if after.AccruedGross != 0 {
		t.Errorf("AccruedGross = %d after repricing, want 0 (a new window)", after.AccruedGross)
	}
	if after.Accrued.Minor() != atReprice {
		t.Errorf("accrued = %d after repricing, want %d unchanged; prior accrual is frozen, not rewritten",
			after.Accrued.Minor(), atReprice)
	}
}

func TestOverdraftCorrectionRefundsWhatTheReceivableCannotAbsorb(t *testing.T) {
	ctx := context.Background()
	r, gl, clock := newTestRegister(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	acct := must(r.OpenAccount(ctx, "Bruno", "EUR"))
	must0(r.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365))

	postTo(t, gl, acct.GLAccount, 20_000, ledger.Debit, start)
	for i := 1; i <= 30; i++ {
		must0(r.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
	}
	// Capitalise, emptying the receivable.
	must0(r.ChargeOverdraftInterest(ctx, acct.ID, start.AddDate(0, 0, 30)))
	if recv := must(gl.BookBalance(ctx, must(r.GetAccount(ctx, acct.ID)).InterestGL)); recv != 0 {
		t.Fatalf("receivable after capitalisation = %d, want 0", recv)
	}
	balBefore := must(gl.BookBalance(ctx, acct.GLAccount))

	// Now say he was never overdrawn at all: a credit backdated to day 1.
	postTo(t, gl, acct.GLAccount, 20_000, ledger.Credit, start)
	must0(r.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 31)))

	recv := must(gl.BookBalance(ctx, must(r.GetAccount(ctx, acct.ID)).InterestGL))
	if recv < 0 {
		t.Errorf("receivable = %d; the correction must clamp rather than drive an asset negative", recv)
	}
	balAfter := must(gl.BookBalance(ctx, acct.GLAccount))
	if balAfter <= balBefore {
		t.Errorf("customer balance %d -> %d; the unabsorbed correction must be refunded to them", balBefore, balAfter)
	}
}
```

`postTo` posts a one-legged-looking movement against the customer's GL account at a given value date; write it as a small helper in the test file if one does not exist:

```go
// postTo moves amount against acct at the given value date, with the bank's
// own settlement account as the contra leg, so the customer's balance moves
// without going through the deposit layer's own capture path.
func postTo(t *testing.T, gl *ledger.Book, acct ledger.AccountID, amount ledger.Amount, dir ledger.Direction, value time.Time) {
	t.Helper()
	contra := testContraAccount(t, gl) // an account of the opposite normal balance
	other := ledger.Credit
	if dir == ledger.Credit {
		other = ledger.Debit
	}
	if _, err := gl.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		Description: "test movement",
		ValueDate:   value,
		Entries: []ledger.Entry{
			{AccountID: acct, Amount: amount, Direction: dir},
			{AccountID: contra, Amount: amount, Direction: other},
		},
	}); err != nil {
		t.Fatalf("post: %v", err)
	}
}
```

Reuse the file's existing register/clock/contra fixtures rather than adding new ones; the names above are placeholders for whatever they are called.

- [ ] **Step 2: Run to verify failure**

```
go test ./deposit/... -run 'Backdated|Freezes|Refunds|Forward' -v
```

Expected: FAIL — `unknown field AccruedGross`, and the backdated cases produce no correction.

- [ ] **Step 3: Add the fields**

`deposit/types.go`, in `Account`, replacing the `Accrued` / `LastAccrualDate` block:

```go
	// Accrued is interest earned and not yet charged, at sub-minor-unit
	// precision. The general ledger holds Accrued.Minor() in InterestGL; this
	// field holds the residue the ledger cannot represent.
	Accrued interest.Accrued
	// AccruedGross is the interest the current terms window has produced in
	// total, recomputed from the account's value-dated balance on every run.
	// Accrued moves by the change in it, which is what makes a backdated
	// posting correct itself: the day it lands on is recomputed, gross moves,
	// and the next run posts the difference.
	//
	// Unlike Accrued it is never decremented by capitalisation. It resets to
	// zero whenever terms are set, because that is where the window starts.
	AccruedGross interest.Accrued
	// TermsEffectiveFrom is the start of the recompute window: the date the
	// current terms took effect.
	//
	// The window is bounded there rather than at account opening because the
	// terms are stored as mutable columns. Recomputing across a repricing would
	// re-derive every earlier day at today's rate and post the difference,
	// rewriting accrual history every time an account is repriced. Prior
	// accrual is frozen instead, and widening this window to account inception
	// is what an effective-dated terms record would buy.
	//
	// Zero means the account has no priced overdraft and accrues nothing.
	TermsEffectiveFrom time.Time
	// LastAccrualDate is the business date accrual has been recomputed through.
	// It never moves backwards, which is what makes an end-of-day re-run a
	// no-op rather than a second charge — and it is why a backdated posting is
	// corrected by the next day's run rather than the same day's.
	LastAccrualDate time.Time
```

- [ ] **Step 4: Open the window when terms are set**

`deposit/register.go`, in `SetOverdraftTermsTx`, replacing the four assignments:

```go
	acct.OverdraftLimit = limit
	acct.Rate = rate
	acct.UnarrangedRate = unarranged
	acct.DayCount = dc

	// A repricing starts a new recompute window. Prior accrual stays exactly
	// where it is — Accrued is untouched — and only days from here are
	// recomputed, so no past day is ever re-derived at a rate that was not in
	// force on it.
	now := r.now()
	acct.TermsEffectiveFrom = now
	acct.AccruedGross = 0
	acct.LastAccrualDate = now
```

- [ ] **Step 5: Make `overdraftAccrual` take its own period**

`deposit/register.go`, replacing the signature and the two `Accrue` calls (the doc comment above it stays as it is, with one sentence added):

```go
// It takes from and to explicitly rather than reading the account's
// LastAccrualDate, so that AccrueSeries can call it once per run of constant
// balance within one accrual window.
func overdraftAccrual(book ledger.Amount, acct Account, from, to time.Time) interest.Accrued {
	drawn := -book
	if drawn <= 0 {
		return 0
	}
	arranged := drawn
	if arranged > acct.OverdraftLimit {
		arranged = acct.OverdraftLimit
	}
	total := interest.Accrue(arranged, acct.Rate, acct.DayCount, from, to)
	if excess := drawn - arranged; excess > 0 {
		rate := acct.UnarrangedRate
		if rate == 0 {
			rate = acct.Rate
		}
		total += interest.Accrue(excess, rate, acct.DayCount, from, to)
	}
	return total
}
```

- [ ] **Step 6: Add the series helper**

`deposit/register.go`, beside `bookBalanceTx`:

```go
// valueDatedSeriesTx is the value-dated movement history of a GL account over
// [from, to], signed by the account's normal direction. It is the same two
// steps bookBalanceTx takes — read the account for its type, then aggregate —
// done through the caller's Tx.
func (r *Register) valueDatedSeriesTx(ctx context.Context, tx Tx, id ledger.AccountID, from, to time.Time) (ledger.Series, error) {
	gl, err := tx.GetAccount(ctx, r.bookID, id)
	if err != nil {
		return ledger.Series{}, err
	}
	return tx.ValueDatedSeries(ctx, r.bookID, id, gl.Type.NormalBalance(),
		ledger.DayStart(from), ledger.NextDay(to))
}
```

- [ ] **Step 7: Rewrite the accrual run**

`deposit/register.go`, replacing `accrueOverdraftAccountTx` entirely:

```go
// accrueOverdraftAccountTx is AccrueOverdraftTx against an account the caller
// has already loaded. RunEndOfDay lists every account and would otherwise read
// each one a second time.
//
// The accrual is a recomputation rather than an increment. Every run re-derives
// the whole terms window from the account's value-dated balance and posts the
// change in the rounded value — which is the same delta the incremental version
// posted, arrived at differently. The difference shows when a posting lands
// backdated: the day it takes effect on is recomputed with it in place, gross
// moves, and the delta trues up the interest that was charged on the old
// figure. No accrual is ever reversed and no date is ever rewound.
func (r *Register) accrueOverdraftAccountTx(ctx context.Context, tx Tx, acct Account, date time.Time) error {
	if acct.Rate <= 0 || acct.Status == Closed {
		return nil
	}
	// No priced overdraft has been set, so there is no window to accrue over.
	if acct.TermsEffectiveFrom.IsZero() {
		return nil
	}
	if acct.DayCount.Days(acct.LastAccrualDate, date) <= 0 {
		return nil
	}

	series, err := r.valueDatedSeriesTx(ctx, tx, acct.GLAccount, acct.TermsEffectiveFrom, date)
	if err != nil {
		return err
	}
	terms := acct
	gross := interest.AccrueSeries(series, acct.TermsEffectiveFrom, date,
		func(balance ledger.Amount, from, to time.Time) interest.Accrued {
			return overdraftAccrual(balance, terms, from, to)
		})

	before := acct.Accrued.Minor()
	acct.Accrued += gross - acct.AccruedGross
	acct.AccruedGross = gross
	acct.LastAccrualDate = date
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}

	delta := acct.Accrued.Minor() - before
	if delta == 0 {
		// The rounding did not tick. There is nothing to post, and a
		// zero-amount entry is refused by the ledger anyway.
		return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrued, string(acct.ID), acct)
	}

	income, err := r.interestIncomeTx(ctx, tx, acct)
	if err != nil {
		return err
	}
	if delta > 0 {
		if _, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			Description: "Overdraft interest accrued: " + acct.Name,
			BookingDate: date,
			ValueDate:   date,
			Entries: []ledger.Entry{
				{AccountID: acct.InterestGL, Amount: delta, Direction: ledger.Debit},
				{AccountID: income, Amount: delta, Direction: ledger.Credit},
			},
		}); err != nil {
			return err
		}
		return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrued, string(acct.ID), acct)
	}
	return r.correctOverdraftAccrualTx(ctx, tx, acct, income, -delta, date)
}

// correctOverdraftAccrualTx gives back interest that a backdated posting has
// shown was never owed. amount is positive.
//
// It is not a reversal. The original accrual was a correct statement of what
// the ledger knew at the time, and reversing it would say otherwise; this is a
// new, linked event that posts what actually changed.
//
// The credit goes to the receivable as far as the receivable can absorb it. If
// interest has already been capitalised out of it, the rest is money the
// customer has actually paid, so it is refunded to their account rather than
// driving an Asset balance negative — which the ledger would refuse, inside an
// end-of-day batch, taking the whole book's run down with it.
func (r *Register) correctOverdraftAccrualTx(ctx context.Context, tx Tx, acct Account, income ledger.AccountID, amount ledger.Amount, date time.Time) error {
	receivable, err := r.bookBalanceTx(ctx, tx, acct.InterestGL)
	if err != nil {
		return err
	}
	absorbed := amount
	if absorbed > receivable {
		absorbed = receivable
	}
	if absorbed < 0 {
		absorbed = 0
	}

	entries := []ledger.Entry{{AccountID: income, Amount: amount, Direction: ledger.Debit}}
	if absorbed > 0 {
		entries = append(entries, ledger.Entry{AccountID: acct.InterestGL, Amount: absorbed, Direction: ledger.Credit})
	}
	if refund := amount - absorbed; refund > 0 {
		entries = append(entries, ledger.Entry{AccountID: acct.GLAccount, Amount: refund, Direction: ledger.Credit})
	}

	if _, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Overdraft interest corrected: " + acct.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries:     entries,
	}); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrualCorrected, string(acct.ID), acct)
}
```

- [ ] **Step 8: Add the audit event**

`ledger/audit.go:48`, in the same `const` block. These are untyped string constants, so declare it the same way and keep the block's alignment (gofmt will fix the padding):

```go
	// EventOverdraftAccrualCorrected is a true-up of overdraft interest after a
	// backdated posting changed the balance a past day accrued on. Distinct
	// from EventOverdraftAccrued so a correction is visible as one in the log
	// rather than hiding inside the ordinary daily stream.
	EventOverdraftAccrualCorrected = "overdraft.accrual_corrected"
```

- [ ] **Step 9: Run the tests**

```
go test ./deposit/... -v
make test
```

Expected: PASS, **including every pre-existing deposit accrual test**. If one of the old tests fails, read it before changing anything: on daily runs this is meant to be a no-op, and a failure there is a bug in the recompute, not a stale expectation.

- [ ] **Step 10: Commit**

```bash
git add deposit/types.go deposit/register.go deposit/register_test.go ledger/audit.go
git commit -m "feat(deposit): recompute overdraft accrual instead of incrementing it

Interest accrued on the book balance, so a posting that arrived
backdated changed nothing about the days it should have changed. Every
run now re-derives the whole terms window from the account's value-dated
balance and posts the change in the rounded value — the same delta,
arrived at differently, except when a backdated posting has moved a past
day, where it trues up what was charged on the old figure.

No accrual is reversed and no date is rewound. The original posting was
a correct statement of what the ledger knew; the correction is a new
linked event.

A correction that the receivable cannot absorb — interest already
capitalised — is refunded to the customer rather than driving an Asset
balance negative, which would refuse inside end-of-day and take the
whole book's run down.

The window starts at the last repricing, because terms are still
mutable columns and recomputing across one would re-derive past days at
a rate that was not in force on them."
```

---

### Task 7: Recompute the lending accrual

**Files:**
- Modify: `lending/types.go:199-206` (the `Facility` struct)
- Modify: `lending/accrual.go:53-98` (`accrueFacilityTx`)
- Modify: `lending/portfolio.go:134`, `:164` (origination), `:291`, `:358` (first draw), and add a `valueDatedSeriesTx` helper beside `drawnTx` at `:445-452`
- Modify: the file declaring `EventFacilityAccrued`
- Test: `lending/accrual_test.go`

**Interfaces:**
- Consumes: everything Task 6 consumes, plus the shape Task 6 established.
- Produces: `lending.Facility.TermsEffectiveFrom time.Time`, `lending.Facility.AccruedGross interest.Accrued`, `ledger.EventFacilityAccrualCorrected`.

This is Task 6 applied to a flat rate instead of a tiered one. The one thing that differs is where an unabsorbed correction is refunded.

- [ ] **Step 1: Write the failing tests**

Add to `lending/accrual_test.go`:

```go
func TestFacilityAccrualCorrectsABackdatedRepayment(t *testing.T) {
	ctx := context.Background()
	p, gl, clock := newTestPortfolio(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	f := must(p.OpenTermLoan(ctx, /* the fixture's usual arguments */))
	must0(p.Draw(ctx, f.ID, 1_000_000, start, "Disbursement"))

	for i := 1; i <= 10; i++ {
		must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, i)))
	}
	after := must(p.GetFacility(ctx, f.ID)).Accrued.Minor()
	if after <= 0 {
		t.Fatalf("accrued nothing over ten drawn days: %d", after)
	}

	// A principal repayment posted late, value-dated day 3: less was owed from
	// day 3 onward than the accrual assumed.
	postTo(t, gl, must(p.GetFacility(ctx, f.ID)).PrincipalGL, 500_000, ledger.Credit, start.AddDate(0, 0, 3))

	must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, 11)))
	corrected := must(p.GetFacility(ctx, f.ID)).Accrued.Minor()
	if corrected >= after {
		t.Errorf("accrued after backdated repayment = %d, want less than %d", corrected, after)
	}
}

func TestFacilityAccrualIgnoresAForwardValueDatedDraw(t *testing.T) {
	ctx := context.Background()
	p, gl, clock := newTestPortfolio(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	f := must(p.OpenTermLoan(ctx, /* the fixture's usual arguments */))
	must0(p.Draw(ctx, f.ID, 1_000_000, start, "Disbursement"))
	must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, 1)))
	baseline := must(p.GetFacility(ctx, f.ID)).Accrued

	// A second advance value-dated five days out has not taken effect.
	postTo(t, gl, must(p.GetFacility(ctx, f.ID)).PrincipalGL, 1_000_000, ledger.Debit, start.AddDate(0, 0, 5))

	must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, 2)))
	oneMoreDay := must(p.GetFacility(ctx, f.ID)).Accrued

	// Day 2 accrued on the original 1_000_000, so the increment matches day 1's.
	if got, want := oneMoreDay-baseline, baseline; got != want {
		t.Errorf("second day accrued %d, want %d; the forward-dated draw has not taken effect", got, want)
	}
}

func TestFacilityCorrectionRefundsToPrincipal(t *testing.T) {
	ctx := context.Background()
	p, gl, clock := newTestPortfolio(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	f := must(p.OpenTermLoan(ctx, /* the fixture's usual arguments */))
	must0(p.Draw(ctx, f.ID, 1_000_000, start, "Disbursement"))
	for i := 1; i <= 30; i++ {
		must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, i)))
	}
	// Settle the accrued interest, emptying the receivable.
	must0(p.Repay(ctx, f.ID, /* funding account */, must(p.AccruedInterest(ctx, f.ID)), start.AddDate(0, 0, 30), "Interest"))
	if recv := must(gl.BookBalance(ctx, must(p.GetFacility(ctx, f.ID)).InterestGL)); recv != 0 {
		t.Fatalf("receivable after repayment = %d, want 0", recv)
	}

	principalBefore := must(gl.BookBalance(ctx, must(p.GetFacility(ctx, f.ID)).PrincipalGL))

	// The loan was repaid in full on day 1, backdated.
	postTo(t, gl, must(p.GetFacility(ctx, f.ID)).PrincipalGL, 1_000_000, ledger.Credit, start)
	must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, 31)))

	if recv := must(gl.BookBalance(ctx, must(p.GetFacility(ctx, f.ID)).InterestGL)); recv < 0 {
		t.Errorf("receivable = %d; the correction must clamp rather than drive an asset negative", recv)
	}
	principalAfter := must(gl.BookBalance(ctx, must(p.GetFacility(ctx, f.ID)).PrincipalGL))
	if principalAfter >= principalBefore {
		t.Errorf("principal %d -> %d; the unabsorbed correction must reduce what the borrower owes",
			principalBefore, principalAfter)
	}
}

func TestFacilityRefundFeedsTheFollowingDaysBasis(t *testing.T) {
	// The one feedback loop in the design: a refund credits PrincipalGL, which
	// is the accrual basis, so the next day accrues on less.
	ctx := context.Background()
	p, gl, clock := newTestPortfolio(t)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	f := must(p.OpenTermLoan(ctx, /* the fixture's usual arguments */))
	must0(p.Draw(ctx, f.ID, 1_000_000, start, "Disbursement"))
	for i := 1; i <= 30; i++ {
		must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, i)))
	}
	must0(p.Repay(ctx, f.ID, /* funding account */, must(p.AccruedInterest(ctx, f.ID)), start.AddDate(0, 0, 30), "Interest"))

	postTo(t, gl, must(p.GetFacility(ctx, f.ID)).PrincipalGL, 900_000, ledger.Credit, start)
	must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, 31)))
	drawnAfterCorrection := must(gl.BookBalance(ctx, must(p.GetFacility(ctx, f.ID)).PrincipalGL))

	beforeNextDay := must(p.GetFacility(ctx, f.ID)).Accrued
	must0(p.Accrue(ctx, f.ID, start.AddDate(0, 0, 32)))
	afterNextDay := must(p.GetFacility(ctx, f.ID)).Accrued

	want := interest.Accrue(drawnAfterCorrection, f.Rate, f.DayCount,
		start.AddDate(0, 0, 31), start.AddDate(0, 0, 32))
	if got := afterNextDay - beforeNextDay; got != want {
		t.Errorf("day 32 accrued %d, want %d on the post-refund principal %d", got, want, drawnAfterCorrection)
	}
}
```

Fill the `/* … */` placeholders from the fixtures the file already uses for `OpenTermLoan` and `Repay`; do not invent new fixture shapes.

- [ ] **Step 2: Run to verify failure**

```
go test ./lending/... -run 'Backdated|Forward|RefundsToPrincipal|FeedsThe' -v
```

Expected: FAIL — `unknown field AccruedGross`.

- [ ] **Step 3: Add the fields**

`lending/types.go`, replacing the `Accrued` / `LastAccrualDate` block:

```go
	// Accrued is interest earned and not yet settled, at sub-minor-unit
	// precision. InterestGL holds Accrued.Minor(); this holds the residue the
	// ledger cannot represent.
	Accrued interest.Accrued
	// AccruedGross is the interest the current terms window has produced in
	// total, recomputed from the facility's value-dated drawn balance on every
	// run. Accrued moves by the change in it, which is what lets a backdated
	// posting correct the interest charged on the day it takes effect. It is
	// never decremented by a repayment, and resets when terms are set.
	AccruedGross interest.Accrued
	// TermsEffectiveFrom is the start of the recompute window: where the
	// current terms took effect. Bounded there rather than at origination
	// because the terms are mutable columns — see the same field on
	// deposit.Account for the full reasoning.
	TermsEffectiveFrom time.Time
	// LastAccrualDate never moves backwards, which is what makes re-running an
	// end-of-day a no-op rather than a second day's interest, and why a
	// backdated posting is corrected by the next day's run.
	LastAccrualDate time.Time
```

- [ ] **Step 4: Open the window at first draw**

`lending/portfolio.go`, at both places that currently set `f.LastAccrualDate = p.now()` (lines 291 and 358 — the first advance on a term loan and on a revolving line):

```go
		now := p.now()
		f.LastAccrualDate = now
		f.TermsEffectiveFrom = now
		f.AccruedGross = 0
```

Nothing in `lending` reprices a facility today, so there is no second place to reset. If one is added later it sets the same three fields.

- [ ] **Step 5: Add the series helper**

`lending/portfolio.go`, beside `drawnTx`:

```go
// drawnSeriesTx is the value-dated history of what the borrower owes over
// [from, to]. Like drawnTx it reads PrincipalGL, whose normal balance is Debit
// because a loan is an Asset.
func (p *Portfolio) drawnSeriesTx(ctx context.Context, tx Tx, f Facility, from, to time.Time) (ledger.Series, error) {
	return tx.ValueDatedSeries(ctx, p.bookID, f.PrincipalGL, ledger.Debit,
		ledger.DayStart(from), ledger.NextDay(to))
}
```

- [ ] **Step 6: Rewrite the accrual run**

`lending/accrual.go`, replacing `accrueFacilityTx`:

```go
// accrueFacilityTx is AccrueTx against a facility the caller has already
// loaded. RunEndOfDay lists every facility and would otherwise read each twice.
//
// Like the deposit layer's overdraft accrual, this is a recomputation rather
// than an increment: every run re-derives the whole terms window from the
// facility's value-dated drawn balance and posts the change in the rounded
// value. A repayment or advance that arrives backdated therefore corrects the
// interest already charged on the days it takes effect over, without any
// accrual being reversed.
func (p *Portfolio) accrueFacilityTx(ctx context.Context, tx Tx, f Facility, date time.Time) error {
	if f.Status == Closed || f.Rate <= 0 {
		return nil
	}
	if f.TermsEffectiveFrom.IsZero() {
		// Nothing has been advanced yet, so there is no period to accrue over.
		return nil
	}
	if f.DayCount.Days(f.LastAccrualDate, date) <= 0 {
		return nil
	}

	series, err := p.drawnSeriesTx(ctx, tx, f, f.TermsEffectiveFrom, date)
	if err != nil {
		return err
	}
	terms := f
	gross := interest.AccrueSeries(series, f.TermsEffectiveFrom, date,
		func(balance ledger.Amount, from, to time.Time) interest.Accrued {
			if balance <= 0 {
				return 0
			}
			return interest.Accrue(balance, terms.Rate, terms.DayCount, from, to)
		})

	before := f.Accrued.Minor()
	f.Accrued += gross - f.AccruedGross
	f.AccruedGross = gross
	f.LastAccrualDate = date
	if err := tx.PutFacility(ctx, p.bookID, f); err != nil {
		return err
	}

	delta := f.Accrued.Minor() - before
	if delta == 0 {
		return p.appendAuditTx(ctx, tx, ledger.EventFacilityAccrued, string(f.ID), f)
	}

	income, err := p.interestIncomeTx(ctx, tx, f)
	if err != nil {
		return err
	}
	if delta > 0 {
		if _, err := p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			Description: "Interest accrued: " + f.Name,
			BookingDate: date,
			ValueDate:   date,
			Entries: []ledger.Entry{
				{AccountID: f.InterestGL, Amount: delta, Direction: ledger.Debit},
				{AccountID: income, Amount: delta, Direction: ledger.Credit},
			},
		}); err != nil {
			return err
		}
		return p.appendAuditTx(ctx, tx, ledger.EventFacilityAccrued, string(f.ID), f)
	}
	return p.correctFacilityAccrualTx(ctx, tx, f, income, -delta, date)
}

// correctFacilityAccrualTx gives back interest a backdated posting has shown was
// never owed. amount is positive.
//
// The credit goes to the receivable as far as it reaches. Anything beyond it is
// interest the borrower has already settled, and it is credited to principal —
// the borrower owes less. That reduces the accrual basis, so the following day
// accrues on the smaller figure, which is the correct outcome and the one
// feedback loop in this design.
func (p *Portfolio) correctFacilityAccrualTx(ctx context.Context, tx Tx, f Facility, income ledger.AccountID, amount ledger.Amount, date time.Time) error {
	receivable, err := p.accruedReceivableTx(ctx, tx, f)
	if err != nil {
		return err
	}
	absorbed := amount
	if absorbed > receivable {
		absorbed = receivable
	}
	if absorbed < 0 {
		absorbed = 0
	}

	entries := []ledger.Entry{{AccountID: income, Amount: amount, Direction: ledger.Debit}}
	if absorbed > 0 {
		entries = append(entries, ledger.Entry{AccountID: f.InterestGL, Amount: absorbed, Direction: ledger.Credit})
	}
	if refund := amount - absorbed; refund > 0 {
		entries = append(entries, ledger.Entry{AccountID: f.PrincipalGL, Amount: refund, Direction: ledger.Credit})
	}

	if _, err := p.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Interest corrected: " + f.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries:     entries,
	}); err != nil {
		return err
	}
	return p.appendAuditTx(ctx, tx, ledger.EventFacilityAccrualCorrected, string(f.ID), f)
}
```

`accruedReceivableTx` is the existing helper at `lending/portfolio.go:457` that returns `tx.BookBalance(ctx, p.bookID, f.InterestGL, ledger.Debit)`. Use its real name — check the file.

- [ ] **Step 7: Add the audit event**

`ledger/audit.go:70`, beside `EventFacilityAccrued` and in the same untyped form:

```go
	// EventFacilityAccrualCorrected is a true-up of facility interest after a
	// backdated posting changed the drawn balance a past day accrued on.
	EventFacilityAccrualCorrected = "facility.accrual_corrected"
```

- [ ] **Step 8: Run the tests**

```
go test ./lending/... -v
make test
```

Expected: PASS, including every pre-existing lending accrual, repayment, schedule and arrears test.

- [ ] **Step 9: Run the seed and diff it**

This is the acceptance test the spec names. Find how the seed is exercised (`grep -rn "seed\." cmd/ api/ --include=*.go` and look for a seed test); then:

```
go test ./seed/... -v
go test ./... 2>&1 | tail -40
```

Expected: PASS with no figure moved. If a seed figure has moved by exactly one minor unit, that is the truncation difference the spec predicts — recompute the figure in the comment at `seed/seed.go:435-453` and note it in the commit message. If a figure has moved by more than that, stop: the recompute is not the no-op on daily runs it is meant to be.

- [ ] **Step 10: Commit**

```bash
git add lending/types.go lending/accrual.go lending/portfolio.go lending/accrual_test.go ledger/audit.go
git commit -m "feat(lending): recompute facility accrual instead of incrementing it

The deposit layer's change, applied to a flat rate. Every run re-derives
the terms window from the facility's value-dated drawn balance and posts
the change in the rounded value, so a repayment or advance that arrives
backdated corrects the interest charged over the days it takes effect.

A correction the receivable cannot absorb credits principal: the
borrower has already settled that interest and now owes less. That
reduces the accrual basis, so the next day accrues on the smaller
figure — the one feedback loop in this design, and the correct outcome."
```

---

### Task 8: Expose the value-dated balance over the API

**Files:**
- Modify: `api/handlers_ledger.go:182-205` (`handleBookBalance`)
- Modify: `api/dto_ledger.go` (the balance response DTO)
- Test: `api/handlers_ledger_test.go`

**Interfaces:**
- Consumes: `Book.ValueDateBalance` (Task 2).
- Produces: `GET /participants/{pid}/accounts/{aid}/balance` gains `valueDateBalance` and an optional `?asOf=` (RFC 3339; defaults to now).

- [ ] **Step 1: Write the failing test**

```go
func TestBalanceEndpointReportsTheValueDatedBalance(t *testing.T) {
	// Post two movements: one value-dated today, one three days out.
	// The book balance carries both; the value-dated balance carries one.
	srv, pid, acct, gl := newTestServer(t)
	today := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	postTo(t, gl, acct, 10_000, ledger.Debit, today)
	postTo(t, gl, acct, 5_000, ledger.Debit, today.AddDate(0, 0, 3))

	var got struct {
		BookBalance      ledger.Amount `json:"bookBalance"`
		ValueDateBalance ledger.Amount `json:"valueDateBalance"`
	}
	getJSON(t, srv, "/participants/"+pid+"/accounts/"+acct+"/balance?asOf="+today.Format(time.RFC3339), &got)

	if got.BookBalance != 15_000 {
		t.Errorf("bookBalance = %d, want 15000", got.BookBalance)
	}
	if got.ValueDateBalance != 10_000 {
		t.Errorf("valueDateBalance = %d, want 10000 (the forward-dated movement has not taken effect)", got.ValueDateBalance)
	}
}

func TestBalanceEndpointRejectsAnUnparseableAsOf(t *testing.T) {
	srv, pid, acct, _ := newTestServer(t)
	status := getStatus(t, srv, "/participants/"+pid+"/accounts/"+acct+"/balance?asOf=not-a-date")
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}
```

Use the file's existing server/request helpers rather than the placeholder names above.

- [ ] **Step 2: Run to verify failure**

```
go test ./api/... -run 'ValueDated|UnparseableAsOf' -v
```

Expected: FAIL — the response has no `valueDateBalance`.

- [ ] **Step 3: Implement**

`api/dto_ledger.go`, on the balance response type:

```go
	// ValueDateBalance is the balance as of the end of the requested day,
	// counting only entries that have taken economic effect. It is what
	// interest is computed from, and it differs from BookBalance whenever a
	// posting is value-dated away from its booking date.
	ValueDateBalance ledger.Amount `json:"valueDateBalance"`
```

`api/handlers_ledger.go`, in `handleBookBalance`, after the existing `BookBalance` call:

```go
	asOf := time.Now()
	if raw := r.URL.Query().Get("asOf"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "asOf must be an RFC 3339 timestamp")
			return
		}
		asOf = parsed
	}
	valueDated, err := p.Ledger.ValueDateBalance(r.Context(), aid, asOf)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
```

and set `ValueDateBalance: valueDated` on the response. Use the file's real error-writing helpers — `writeError`/`writeLedgerError` are placeholders for whatever it already calls.

- [ ] **Step 4: Run the tests**

```
go test ./api/... -v
make test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add api/handlers_ledger.go api/dto_ledger.go api/handlers_ledger_test.go
git commit -m "feat(api): report the value-dated balance alongside the book balance

The README has described three balances an account carries and the API
could show two of them. asOf defaults to now and takes an RFC 3339
timestamp, so the balance that interest is computed from is observable
for any past day."
```

---

### Task 9: Make every layer's documentation say the same thing

**Files:**
- Modify: `README.md` — *Balance Types* (line ~456), *A Balance Is an Aggregate* (Persistence), *Next Work* (payments), *Table of Contents* if a heading is added
- Modify: `web/src/components/hint-content.ts` — keys `value-date`, `booking-date`, `settlement-delay` (~433-446), `debtor-leg` (~489), `payment-lifecycle` (~479), `book-balance` (~215)
- Modify: `web/src/lib/quiz/chapters/06-booking-date-vs-value-date.ts`, `11-payment-schemes.ts`, `12-sepa.ts`
- Modify: `seed/seed.go:435-453` (the comment naming the zero-`LastAccrualDate` bootstrap)
- Test: `cd web && npm run test`

**Interfaces:** none — documentation only.

**Two domain facts changed and must now agree everywhere:**

1. The value-dated balance exists, is computed from entry value dates, and is what interest accrues on.
2. A debtor leg's *customer* side value-dates to the debit; only the clearing-suspense side carries the settlement date.

- [ ] **Step 1: Correct `README.md` *Balance Types***

Replace the value-date bullet and add the convention:

```markdown
- **Value-date balance** (also called the **interest-bearing balance**): The balance computed from entries whose value date has passed. This is what the bank uses to calculate interest. It is `ledger.Book.ValueDateBalance(ctx, accountID, asOf)`, and both interest engines consume it.

A day boundary here is a UTC day, and a day's interest accrues on that day's **closing** balance: entries value-dated on the day itself count. A business date is a date, so an end-of-day run at 23:00 covers the same day as one at 09:00 — `ledger.DayStart` is where that rule lives, and it lives in Go rather than in either store, because two stores that each decide what a day is are one DST-adjacent edge case away from disagreeing.
```

Then add a subsection after it:

```markdown
### Backdated Postings Correct Themselves

A posting can arrive value-dated to a day that has already been accrued for — a salary credit booked Friday and value-dated Wednesday. The interest charged for Wednesday and Thursday was computed on a balance that is now known to be wrong.

Accrual handles this without reversing anything. Every run recomputes the interest for its whole terms window from the account's value-dated movement series, and posts the change in the rounded value against what it posted last time. A backdated entry moves a historical day's balance, the recomputed gross moves with it, and the next run's delta is the true-up. Nothing is rewound and no earlier accrual is reversed: each of those was a correct statement of what the ledger knew when it was made, and the correction is a new, linked event.

A negative true-up credits the accrued-interest receivable. If interest has already been capitalised out of it, the receivable cannot absorb the whole correction — that money has left it — so the remainder is refunded to the customer's account, or credited to principal on a loan.

The window starts at the last repricing, not at account opening. Product terms are still mutable columns, so recomputing across a repricing would re-derive every earlier day at today's rate and post the difference — rewriting accrual history every time an account is repriced. Prior accrual is frozen instead. Widening this window to account inception is what an effective-dated terms record would buy, and that is not built yet.
```

- [ ] **Step 2: Correct the *Next Work* and *A Balance Is an Aggregate* claims**

In the payments *Next Work* list, delete the account-status bullet (it has been done since the lending merge — `payment/scheme.go:85` calls `deposit.CheckWithdrawalTx`) and add:

```markdown
- **Effective-dated product terms.** `SetOverdraftTerms` overwrites the rate in place, so an accrual posted six months ago cannot be reproduced from stored state — only recovered by replaying the audit log, which nothing does. It is also what bounds the retroactive-accrual window to the last repricing rather than to account opening.
- **The creditor leg at settlement** bypasses the deposit layer: `SettleCycleTx` posts straight into the GL account, so a payee whose account was frozen or closed between initiation and settlement is credited anyway. The suspense account an unapplicable credit should land in already exists.
```

In *A Balance Is an Aggregate, Not a Column*, the checkpoint sentence still describes something unimplemented. Add one sentence after it:

```markdown
That checkpointing is described here and not built: `deposit.Snapshot` is written by `TakeEndOfDaySnapshot` and read only by `GetSnapshot` and `ListSnapshots`. No balance query consults one, and a backdated posting does not invalidate the snapshots it falsifies.
```

- [ ] **Step 3: Correct `hint-content.ts`**

In the `settlement-delay` body, replace the two-line block at ~445-446:

```
  Debtor leg, customer side  = T           (PSD2 Art. 87(2): no earlier than the debit)
  Debtor leg, suspense side  = T + settlement delay
  Creditor leg               = T + settlement delay
```

and add a sentence:

```
The two sides of the debtor posting take effect on different days, which is why a [[value-date]] lives on the entry and not only on the transaction. The payer's money is gone the moment it is debited; the bank's clearing position settles days later.
```

In `debtor-leg` (~489), replace "value-dated to the settlement date" with "value-dated to the debit itself on the customer's side, and to the settlement date on the clearing-suspense side".

In `payment-lifecycle` (~479), replace "value-dated to settlement date" with "the customer's side value-dated to the debit, the suspense side to settlement".

In `book-balance` (~215), the sentence about a forward-dated transaction is now demonstrable rather than hypothetical — leave the claim, and point it at the new API field:

```
It can also differ from the value-date balance: a forward-dated transaction is in the book balance before its economic effect begins. The balance endpoint returns both, and interest accrues on the value-dated one.
```

In `value-date` (~194-203), the back-dated-corrections bullet is now implemented — say so:

```
- **Back-dated corrections:** operations books today, value-dates to the correct past date. Interest accrual recomputes its window on the next run, so the days that correction covers are re-derived and the difference is posted as a true-up.
```

Every `[[link]]` used above must resolve to an existing key. Do not introduce a new bracketed term without adding its entry.

- [ ] **Step 4: Correct the quiz chapters**

In each of `06-booking-date-vs-value-date.ts`, `11-payment-schemes.ts` and `12-sepa.ts`, find every question whose correct answer asserts that both debtor legs share the settlement value date. Rewrite the question and its explanation rather than deleting it — `diversity.test.ts` holds each chapter to 18–22 questions, ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty tiers, so a deletion breaks the count.

Chapter 06 is the natural home for one new question on the recompute; if adding it pushes the chapter past 22, replace a weaker existing question rather than exceeding the bound.

- [ ] **Step 5: Correct the seed comment**

`seed/seed.go:435-453` explains Bruno's accrual by naming `accrueOverdraftAccountTx`'s zero-`LastAccrualDate` case. Rewrite that clause to name `TermsEffectiveFrom`: the window now opens when `SetOverdraftTerms` runs, and the days between that and the SCT accrue zero because he is not yet overdrawn — which is why the figure is unchanged.

- [ ] **Step 6: Run everything**

```
make test
```

Expected: PASS — Go suites, `npm run typecheck`, `npm run lint`, and `npm run test` (which is what catches a `[[wiki-link]]` to a missing key, in hint bodies *and* quiz explanations).

Then load a page, because the wiki-link guard is a runtime one:

```
make dev
```

and open the concept panel on a payments page.

- [ ] **Step 7: Commit**

```bash
git add README.md web/src/components/hint-content.ts web/src/lib/quiz/chapters seed/seed.go
git commit -m "docs: say the same thing about value dates in every layer

Two domain facts changed. The value-dated balance exists now, is
computed from entry value dates, and is what interest accrues on — the
README described it and nothing computed it. And a debtor leg's
customer side value-dates to the debit rather than to settlement, which
the hint content and three quiz chapters taught the other way.

Also swept: Next Work still listed account-status enforcement on the
debit path as future work, and payment/scheme.go has called
CheckWithdrawalTx since the lending merge. The snapshot-as-checkpoint
paragraph now says plainly that it describes something unbuilt."
```

---

## Self-Review

Run this before handing the plan to an implementer.

**Spec coverage.** Every section of `2026-07-28-value-dated-balance-design.md` maps to a task: entry value date → Task 1; `ValueDateBalance` and the Go-side day truncation → Task 2; `ValueDatedSeries` → Task 3; `AccrueSeries` and the slot convention → Task 4; the payment leg split → Task 5; deposit recompute, negative deltas, clamp-and-refund, the new audit event → Task 6; the same for lending plus the principal feedback loop → Task 7; the API field → Task 8; README, hint content, quiz, schema comment (Task 1) and seed comment → Task 9. The spec's four out-of-scope items are stated as such in Task 9's README edits rather than silently dropped.

**Type consistency.** `ValueDateBalance` takes `before` (exclusive) in the `Tx` interface and `asOf` (inclusive day) on `Book`; `NextDay` is the conversion, and it is used in Task 2's `Book` method, Task 6's `valueDatedSeriesTx`, and Task 7's `drawnSeriesTx`. `Series`/`DayMovement` are declared once in Task 3 and consumed unchanged in Tasks 4, 6 and 7. `Period` is declared in Task 4 and both closures in Tasks 6 and 7 match its signature. `AccruedGross` is `interest.Accrued` in both `deposit.Account` and `lending.Facility`.

**Known placeholders, and they are deliberate.** Test fixture names (`newTestRegister`, `newTestPortfolio`, `newTestNetwork`, `postTo`, `must`, `must0`, `writeError`) are marked in each task as "use whatever the file already has". They are not invented API — they are hooks into existing test scaffolding that varies by file, and every task says to check. The `/* the fixture's usual arguments */` markers in Task 7 are the only spots where an implementer must read a neighbouring test to fill in a call, and the task says so.
