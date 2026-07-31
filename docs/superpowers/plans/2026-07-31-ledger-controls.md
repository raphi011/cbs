# Ledger Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-book, per-asset trial balance; refuse booking dates that name a later day than today; and say in the README that the no-`CHECK` position is a consequence of the dual-store design rather than the industry position.

**Architecture:** The trial balance is one new file in `ledger` — a walk over `tx.ListAccounts` summing each account's balance restated debit-positive, inside a single `View`. No store method, no schema change. The booking-date rule is a three-line refusal in `PostTransactionTx` plus an exported `Book.CheckBookingDate` that the two end-of-day batches call once before their loops. The README paragraph is prose.

**Tech Stack:** Go 1.x (no new dependencies), Postgres via `store/pg`, Next.js + React Query + shadcn/ui in `web`, Vitest for the web tests.

## Global Constraints

- `go test ./...` (store/mem) and `TEST_DATABASE_URL=… go test ./...` (store/pg) must both pass at the end of every task. `make test-pg` runs the second.
- No store-interface change, no migration, no `storetest` contract in this branch. If a task seems to need one, stop and ask.
- The README is the authoritative source for domain claims; `web/src/components/hint-content.ts` and `web/src/lib/quiz/chapters/*.ts` must agree with it (`CLAUDE.md`).
- A `[[wiki-link]]` to a key absent from `hint-content.ts` throws at runtime under `RootLayout` and takes every dev route down; `next build` stays green. `cd web && npm run test` catches it in hint bodies *and* quiz explanations. Write the hint key before the link that points at it.
- `web/src/lib/quiz/diversity.test.ts` holds each chapter to 18–22 questions, ≥8 distinct `concept` tags, no tag more than 3×, and all three difficulty tiers. Chapter 02 currently has 20 questions (`double-entry`, `normal-balance`, `reversal` each already at 3×); chapter 06 has 21 (`booking-date`, `value-date` each already at 3×).
- Commit after every task. Message style: `feat(ledger): …`, `fix(…)`, `test(…)`, `docs: …` — imperative, lower case, no trailing period.
- Every commit ends with the trailer `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`.

---

## File Structure

**Created**

- `ledger/trialbalance.go` — `TrialBalance`, `TrialBalanceRow`, `Balanced`, `Book.TrialBalance`, `Book.TrialBalanceTx`, `debitPositive`. Its own file because it is a report over the whole book rather than part of the posting path, and `ledger/book.go` is already 990 lines.
- `ledger/trialbalance_test.go` — the report's tests, in `package ledger_test` like every other test in this package.
- `web/src/components/trial-balance-card.tsx` — the card the ledger page renders.

**Modified**

- `ledger/book.go` — `CheckBookingDate`, and its call inside `PostTransactionTx`.
- `ledger/errors.go` — `ErrFutureBookingDate`.
- `ledger/book_test.go` — the booking-date tests.
- `deposit/register.go`, `lending/arrears.go` — one pre-check each at the top of `RunEndOfDayTx`.
- `api/handlers_ledger.go`, `api/dto_ledger.go`, `api/errors.go`, `api/server_test.go` — the endpoint, its DTO, the 400 mapping.
- `web/src/lib/types.ts`, `web/src/lib/api/endpoints.ts`, `web/src/lib/api/hooks.ts`, `web/src/lib/api/query-keys.ts`, `web/src/app/participants/[pid]/ledger/page.tsx` — the wiring.
- `README.md` — a *Trial Balance* section, the booking-date rule, the single-backend counterpoint.
- `web/src/components/hint-content.ts`, `web/src/lib/quiz/chapters/02-double-entry-bookkeeping.ts`, `web/src/lib/quiz/chapters/06-booking-date-vs-value-date.ts`.
- `docs/cbs-vs-book.md` — items 5, 6 and 9 to closed.
- Test fixtures in `lending`, `deposit`, `payment`, `api` — see Tasks 6–8.

---

### Task 1: The trial balance in `ledger`

**Files:**
- Create: `ledger/trialbalance.go`
- Create: `ledger/trialbalance_test.go`

**Interfaces:**
- Consumes: `Tx.ListAccounts(ctx, book)`, `Tx.BookBalance(ctx, book, id, normal)`, `Tx.ValueDateBalance(ctx, book, id, normal, before)`, `NextDay`, `AccountType.NormalBalance()` — all existing.
- Produces: `ledger.TrialBalance{AsOf time.Time; Rows []TrialBalanceRow}`, `ledger.TrialBalanceRow{Asset AssetCode; Debits, Credits, InFlight Amount}`, `(TrialBalance).Balanced() bool`, `(*Book).TrialBalance(ctx, asOf time.Time) (TrialBalance, error)`, `(*Book).TrialBalanceTx(ctx, tx Tx, asOf time.Time) (TrialBalance, error)`.

- [ ] **Step 1: Write the failing tests**

Create `ledger/trialbalance_test.go`:

```go
package ledger_test

import (
	"context"
	"testing"
	"time"

	. "github.com/raphi011/cbs/ledger"
)

// A book built only through PostTransaction cannot fail this check — which is
// the reason to compute it. The check is on everything that reaches the store
// another way; see the bypass test below.
func TestTrialBalance_TiesOnABookBuiltThroughThePostingPath(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 100_000, Direction: Debit},
			{AccountID: alice.ID, Amount: 100_000, Direction: Credit},
		},
		Description: "deposit",
	})
	assertNoError(t, err)

	tb, err := book.TrialBalance(ctx, time.Time{})
	assertNoError(t, err)
	assertEqual(t, "rows", len(tb.Rows), 1)
	assertEqual(t, "asset", tb.Rows[0].Asset, testAsset)
	assertEqual(t, "debits", tb.Rows[0].Debits, Amount(100_000))
	assertEqual(t, "credits", tb.Rows[0].Credits, Amount(100_000))
	assertEqual(t, "in flight", tb.Rows[0].InFlight, Amount(0))
	if !tb.Balanced() {
		t.Fatal("a book built only through PostTransaction did not balance")
	}
}

// A zero asOf means now, the same convention a zero BookingDate follows.
func TestTrialBalance_ZeroAsOfMeansNow(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)

	tb, err := book.TrialBalance(ctx, time.Time{})
	assertNoError(t, err)
	if !tb.AsOf.Equal(testClock()) {
		t.Fatalf("asOf = %v, want the book's clock %v", tb.AsOf, testClock())
	}
}

// The only way to create an imbalance is to write past PostTransaction, which
// is what a migration, a fixture or a hand-run UPDATE does. This is the class
// of bug the control exists for.
func TestTrialBalance_CatchesAWriteThatBypassedThePostingPath(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	err := book.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.PutTransaction(ctx, book.ID(), Transaction{
			ID: "tx_bypass",
			Entries: []Entry{
				{ID: "ent_bypass_a", AccountID: cash.ID, Amount: 30_000, Direction: Debit, ValueDate: testClock()},
				{ID: "ent_bypass_b", AccountID: alice.ID, Amount: 20_000, Direction: Credit, ValueDate: testClock()},
			},
			BookingDate: testClock(),
			ValueDate:   testClock(),
			CreatedAt:   testClock(),
		})
	})
	assertNoError(t, err)

	tb, err := book.TrialBalance(ctx, time.Time{})
	assertNoError(t, err)
	if tb.Balanced() {
		t.Fatal("the trial balance tied over an unbalanced write")
	}
	assertEqual(t, "debits", tb.Rows[0].Debits, Amount(30_000))
	assertEqual(t, "credits", tb.Rows[0].Credits, Amount(20_000))
}

// An overdrawn liability holds a DEBIT balance, and belongs in the debit
// column. This is the overdraft's GL treatment as a report: the account does
// not move in the subledger, the mapping reads its side.
func TestTrialBalance_PutsAContraryBalanceInTheOppositeColumn(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, _, feeIncome := setupChartOfAccounts(t, book)

	// Alice pays a fee she has no balance for: her liability goes debit.
	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 40_000, Direction: Debit},
			{AccountID: feeIncome.ID, Amount: 40_000, Direction: Credit},
		},
		Description: "account fee",
	})
	assertNoError(t, err)

	tb, err := book.TrialBalance(ctx, time.Time{})
	assertNoError(t, err)
	// Alice is the only account carrying a debit-side balance.
	assertEqual(t, "debits", tb.Rows[0].Debits, Amount(40_000))
	assertEqual(t, "credits", tb.Rows[0].Credits, Amount(40_000))
}

// Two legs of one transaction may value-date differently — the shape a payment
// posts, debiting the customer today and settling days later. Between the two
// dates the value-dated sum is not zero, and the difference is the amount in
// flight. It is a figure, not a fault.
func TestTrialBalance_ReportsTheValueDateGapWhileMoneyIsInFlight(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	// Fund the bank's cash first: an Asset account may not go negative.
	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 100_000, Direction: Debit},
			{AccountID: alice.ID, Amount: 100_000, Direction: Credit},
		},
		Description: "deposit",
	})
	assertNoError(t, err)

	settle := testClock().AddDate(0, 0, 2)
	_, err = book.PostTransaction(ctx, PostTransactionRequest{
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 50_000, Direction: Debit, ValueDate: testClock()},
			{AccountID: cash.ID, Amount: 50_000, Direction: Credit, ValueDate: settle},
		},
		Description: "outbound transfer",
	})
	assertNoError(t, err)

	today, err := book.TrialBalance(ctx, testClock())
	assertNoError(t, err)
	assertEqual(t, "debits today", today.Rows[0].Debits, Amount(50_000))
	assertEqual(t, "credits today", today.Rows[0].Credits, Amount(50_000))
	assertEqual(t, "in flight today", today.Rows[0].InFlight, Amount(50_000))

	after, err := book.TrialBalance(ctx, settle)
	assertNoError(t, err)
	assertEqual(t, "in flight after settlement", after.Rows[0].InFlight, Amount(0))
	if !after.Balanced() {
		t.Fatal("the booking columns stopped tying")
	}
}

// One row per asset that has an account, ascending by code, whether or not the
// account has ever been posted to.
func TestTrialBalance_GroupsByAssetAscending(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	_, _, _, _ = setupChartOfAccounts(t, book)

	gl, err := book.CreateLedger(ctx, "Crypto")
	assertNoError(t, err)
	sub, err := book.CreateSubledger(ctx, gl.ID, "Wallets")
	assertNoError(t, err)
	_, err = book.CreateAccount(ctx, sub.ID, "BTC Wallet", Asset, "BTC")
	assertNoError(t, err)

	tb, err := book.TrialBalance(ctx, time.Time{})
	assertNoError(t, err)
	assertEqual(t, "rows", len(tb.Rows), 2)
	assertEqual(t, "first asset", tb.Rows[0].Asset, AssetCode("BTC"))
	assertEqual(t, "second asset", tb.Rows[1].Asset, testAsset)
	assertEqual(t, "btc debits", tb.Rows[0].Debits, Amount(0))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./ledger/ -run TestTrialBalance -v`
Expected: compile failure — `book.TrialBalance undefined`.

- [ ] **Step 3: Write the implementation**

Create `ledger/trialbalance.go`:

```go
package ledger

import (
	"context"
	"sort"
	"time"
)

// TrialBalance is every account in the book totalled into a debit and a credit
// column, grouped by asset.
//
// In a ledger that refuses an unbalanced posting the columns cannot disagree
// arithmetically, and that is the reason to compute them: it makes the check a
// control on the PIPELINE rather than on the arithmetic. What it catches is
// what never passed PostTransaction at all — a migration, a seed, a test
// fixture, a hand-run UPDATE.
//
// What it does not catch is a bug inside the aggregate it uses. Both columns
// are built from the same store-side sum, so a fault in that sum cancels itself
// out of both and the totals still tie. The stronger form — recomputing the
// totals from the entries by a second code path, which is what an auditor calls
// an independent recomputation — is deliberately not what this is.
type TrialBalance struct {
	// AsOf bounds the InFlight column ONLY. Debits and Credits are always as
	// the book stands now, because no store aggregate restricts by booking
	// date and adding one is a store-interface change this report does not
	// need.
	AsOf time.Time

	// Rows carries one entry per asset that has an account, ascending by code,
	// whether or not that account has ever been posted to.
	Rows []TrialBalanceRow
}

// TrialBalanceRow is one asset's totals.
type TrialBalanceRow struct {
	Asset   AssetCode
	Debits  Amount
	Credits Amount

	// InFlight is the same sum value-dated at AsOf, signed and net: positive
	// when more debits than credits have taken economic effect.
	//
	// It is a figure and not a fault. The two legs of one transaction may
	// value-date differently — an outbound payment debits the customer on the
	// day it is debited while the clearing leg settles days later — so between
	// those dates this is exactly the amount in flight. Asserting it to be zero
	// would be a control that fires on correct behaviour.
	InFlight Amount
}

// Balanced reports whether every row's debits equal its credits.
//
// A false answer means something reached the store without passing
// PostTransaction's per-asset balance check.
func (t TrialBalance) Balanced() bool {
	for _, r := range t.Rows {
		if r.Debits != r.Credits {
			return false
		}
	}
	return true
}

// TrialBalance computes the trial balance. A zero asOf means now, the same
// convention a zero BookingDate follows.
func (s *Book) TrialBalance(ctx context.Context, asOf time.Time) (TrialBalance, error) {
	var out TrialBalance
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.TrialBalanceTx(ctx, tx, asOf)
		return err
	})
	return out, err
}

// TrialBalanceTx is TrialBalance within a caller-supplied unit of work.
//
// The whole walk belongs in ONE unit of work. Per-account reads taken outside
// one can straddle a concurrent posting and report a break that never existed,
// and a control that cries wolf is one nobody reads twice.
//
// It asks the store for each balance directly rather than going through
// BookBalanceTx, because it already holds the account and would otherwise read
// every one of them a second time for its type.
func (s *Book) TrialBalanceTx(ctx context.Context, tx Tx, asOf time.Time) (TrialBalance, error) {
	if asOf.IsZero() {
		asOf = s.now()
	}
	accounts, err := tx.ListAccounts(ctx, s.id)
	if err != nil {
		return TrialBalance{}, err
	}

	rows := make(map[AssetCode]*TrialBalanceRow, 4)
	for _, a := range accounts {
		normal := a.Type.NormalBalance()

		booked, err := tx.BookBalance(ctx, s.id, a.ID, normal)
		if err != nil {
			return TrialBalance{}, err
		}
		valued, err := tx.ValueDateBalance(ctx, s.id, a.ID, normal, NextDay(asOf))
		if err != nil {
			return TrialBalance{}, err
		}

		row, ok := rows[a.Asset]
		if !ok {
			row = &TrialBalanceRow{Asset: a.Asset}
			rows[a.Asset] = row
		}
		// Restated debit-positive before being summed. A balance signed by its
		// own account's normal direction cannot be added to one signed by
		// another's: a liability holding a credit balance and an asset holding
		// a debit balance are both "positive" and belong in opposite columns.
		if d := debitPositive(booked, normal); d > 0 {
			row.Debits += d
		} else {
			row.Credits += -d
		}
		row.InFlight += debitPositive(valued, normal)
	}

	out := TrialBalance{AsOf: asOf, Rows: make([]TrialBalanceRow, 0, len(rows))}
	for _, r := range rows {
		out.Rows = append(out.Rows, *r)
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Asset < out.Rows[j].Asset })
	return out, nil
}

// debitPositive restates a balance signed by its account's normal direction as
// one signed debit-positive, which is the only orientation in which balances
// from different account types can be added together.
func debitPositive(balance Amount, normal Direction) Amount {
	if normal == Debit {
		return balance
	}
	return -balance
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./ledger/ -run TestTrialBalance -v`
Expected: all six PASS.

- [ ] **Step 5: Pin it on the seeded book**

The tests above build their own two-account chart. The seed is the multi-asset, multi-participant case, and it is the one that would catch a seed script writing something the ledger would have refused. Add to `seed/seed_test.go`:

```go
// Every seeded book ties. The seed builds its scenario through the domain
// layers rather than through the store, so this must hold — and it is exactly
// the check that would catch it if a future seeding shortcut stopped doing so.
func TestSeededBooksBalance(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	for _, p := range listParticipants(t, ctx, net) {
		tb, err := p.Ledger.TrialBalance(ctx, time.Time{})
		if err != nil {
			t.Fatalf("trial balance for %s: %v", p.ID, err)
		}
		if len(tb.Rows) == 0 {
			t.Fatalf("participant %s has no accounts at all", p.ID)
		}
		for _, row := range tb.Rows {
			if row.Debits != row.Credits {
				t.Errorf("participant %s, %s: debits %d != credits %d",
					p.ID, row.Asset, row.Debits, row.Credits)
			}
		}
	}
}
```

Run: `go test ./seed/ -run TestSeededBooksBalance -v`
Expected: PASS. If it fails, do not adjust the test — a seeded book that does not tie is the control doing its job, and the seed is what needs looking at.

- [ ] **Step 6: Run both backends**

Run: `go test ./... && make test-pg`
Expected: every package ok on both. If `make test-pg` is not available (no Postgres), run `TEST_DATABASE_URL=… go test ./ledger/... ./seed/...` per `README.md` *Running Against Postgres* and say so in the commit body.

- [ ] **Step 7: Commit**

```bash
git add ledger/trialbalance.go ledger/trialbalance_test.go seed/seed_test.go
git commit -m "feat(ledger): total the book into a per-asset trial balance

A control on the posting pipeline rather than on the arithmetic: every
posting balances by construction, so what this catches is what reached the
store another way. The value-dated column is a figure and not an assertion,
because one transaction's legs may legitimately take effect on different
days.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: The trial balance over HTTP

**Files:**
- Modify: `api/handlers_ledger.go` (route list at :10-27, new handler after `handleBookBalance`)
- Modify: `api/dto_ledger.go` (beside `accountBalanceDTO` at :61)
- Modify: `api/server_test.go`

**Interfaces:**
- Consumes: `(*ledger.Book).TrialBalance(ctx, asOf)` from Task 1; `s.participant(w, r)`, `writeJSON`, `writeBadRequest`, `writeError` — existing.
- Produces: `GET /participants/{pid}/trial-balance?asOf=<RFC3339>` returning `{asOf, balanced, rows:[{asset, debits, credits, inFlight}]}`.

- [ ] **Step 1: Write the failing test**

Add to `api/server_test.go`:

```go
func TestTrialBalanceEndpoint(t *testing.T) {
	h := newTestServer(t)

	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sl := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	cash := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+sl+"/accounts",
		`{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+sl+"/accounts",
		`{"name":"Alice","type":"Liability","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	doJSON(t, h, "POST", "/participants/"+pid+"/transactions",
		`{"description":"deposit","entries":[`+
			`{"accountId":"`+cash+`","amount":100000,"direction":"Debit"},`+
			`{"accountId":"`+alice+`","amount":100000,"direction":"Credit"}]}`,
		http.StatusCreated)

	got := doJSON(t, h, "GET", "/participants/"+pid+"/trial-balance", "", http.StatusOK)
	if got["balanced"] != true {
		t.Fatalf("balanced = %v, want true", got["balanced"])
	}
	rows, ok := got["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("rows = %v, want one", got["rows"])
	}
	row := rows[0].(map[string]any)
	if row["asset"] != "EUR" || row["debits"] != float64(100000) || row["credits"] != float64(100000) {
		t.Fatalf("row = %v", row)
	}
	if row["inFlight"] != float64(0) {
		t.Fatalf("inFlight = %v, want 0", row["inFlight"])
	}
}

func TestTrialBalanceEndpoint_RejectsAMalformedAsOf(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, h, "GET", "/participants/"+pid+"/trial-balance?asOf=15-01-2025", "", http.StatusBadRequest)
}
```

The account-creation bodies above match `TestCreateAccountRequiresAsset` (`api/server_test.go:214`); check the response field names against a neighbouring test before running, and add no new helpers.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./api/ -run TestTrialBalanceEndpoint -v`
Expected: FAIL with 404 (no route registered).

- [ ] **Step 3: Add the DTO**

In `api/dto_ledger.go`, after `accountBalanceDTO`:

```go
// trialBalanceDTO is the book totalled per asset. `balanced` is the assertion —
// debits equal credits in every asset — and `inFlight` is a figure rather than
// a fault: one transaction's legs may take economic effect on different days,
// and between them the value-dated sum is off by the amount in flight.
type trialBalanceDTO struct {
	AsOf     time.Time            `json:"asOf"`
	Balanced bool                 `json:"balanced"`
	Rows     []trialBalanceRowDTO `json:"rows"`
}

// trialBalanceRowDTO carries integers in each asset's minor units, like every
// other amount on the wire; the asset code beside them is what gives them a
// scale.
type trialBalanceRowDTO struct {
	Asset    string `json:"asset"`
	Debits   int64  `json:"debits"`
	Credits  int64  `json:"credits"`
	InFlight int64  `json:"inFlight"`
}

func toTrialBalanceDTO(tb ledger.TrialBalance) trialBalanceDTO {
	rows := make([]trialBalanceRowDTO, len(tb.Rows))
	for i, r := range tb.Rows {
		rows[i] = trialBalanceRowDTO{
			Asset:    string(r.Asset),
			Debits:   int64(r.Debits),
			Credits:  int64(r.Credits),
			InFlight: int64(r.InFlight),
		}
	}
	return trialBalanceDTO{AsOf: tb.AsOf, Balanced: tb.Balanced(), Rows: rows}
}
```

- [ ] **Step 4: Add the route and handler**

In `api/handlers_ledger.go`, add to `registerLedgerRoutes` after the balance route:

```go
	mux.HandleFunc("GET /participants/{pid}/trial-balance", s.handleTrialBalance)
```

And the handler, after `handleBookBalance`:

```go
func (s *Server) handleTrialBalance(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	// Parsed before anything is read, like handleBookBalance's asOf: a
	// malformed timestamp is a 400 whatever the store says.
	//
	// A zero value is passed straight through rather than defaulted to
	// time.Now() here, because the book's own clock is the right default and a
	// seeded system's clock is not the wall clock.
	var asOf time.Time
	if raw := r.URL.Query().Get("asOf"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeBadRequest(w, "asOf must be an RFC 3339 timestamp")
			return
		}
		asOf = parsed
	}
	tb, err := p.Ledger.TrialBalance(r.Context(), asOf)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTrialBalanceDTO(tb))
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./api/ -run TestTrialBalance -v`
Expected: both PASS.

- [ ] **Step 6: Run the suite and commit**

Run: `go test ./...`

```bash
git add api/handlers_ledger.go api/dto_ledger.go api/server_test.go
git commit -m "feat(api): expose the trial balance

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: The trial balance on the ledger page

**Files:**
- Modify: `web/src/lib/types.ts`
- Modify: `web/src/lib/api/endpoints.ts`
- Modify: `web/src/lib/api/query-keys.ts`
- Modify: `web/src/lib/api/hooks.ts`
- Create: `web/src/components/trial-balance-card.tsx`
- Modify: `web/src/app/participants/[pid]/ledger/page.tsx`

**Interfaces:**
- Consumes: `GET /participants/{pid}/trial-balance` from Task 2; `Money`, `useAssetLookup`, `Card`/`CardHeader`/`CardTitle`/`CardContent`, `Skeleton`, `Hint` — all existing.
- Produces: `TrialBalance` and `TrialBalanceRow` types, `api.getTrialBalance`, `qk.trialBalance`, `useTrialBalance(pid)`, `<TrialBalanceCard pid={pid} />`.

- [ ] **Step 1: Add the types**

In `web/src/lib/types.ts`, beside the other ledger types:

```ts
// One asset's totals. `inFlight` is the value-dated gap: the net of legs whose
// economic effect has landed while their counter-legs' has not. Non-zero is
// normal while a payment is between initiation and settlement.
export interface TrialBalanceRow {
  asset: string;
  debits: number;
  credits: number;
  inFlight: number;
}

export interface TrialBalance {
  asOf: string;
  balanced: boolean;
  rows: TrialBalanceRow[];
}
```

- [ ] **Step 2: Add the endpoint, key and hook**

In `web/src/lib/api/endpoints.ts`, after `getBookBalance`:

```ts
// asOf governs only `inFlight`; the debit and credit columns are always as the
// book stands now. Omitted, the backend uses the book's own clock.
export function getTrialBalance(pid: string, asOf?: string): Promise<TrialBalance> {
  return request("GET", `/participants/${pid}/trial-balance${qs({ asOf })}`);
}
```

Add `TrialBalance` to that file's existing `import type { … } from "../types"` list.

In `web/src/lib/api/query-keys.ts`, inside `qk`, beside `accountBalance`:

```ts
  trialBalance: (pid: string) =>
    ["participants", pid, "trial-balance"] as const,
```

In `web/src/lib/api/hooks.ts`, after `useAccountBalance`:

```ts
export function useTrialBalance(pid: string) {
  return useQuery({
    queryKey: qk.trialBalance(pid),
    queryFn: () => api.getTrialBalance(pid),
    enabled: pid !== "",
  });
}
```

- [ ] **Step 3: Write the card**

Create `web/src/components/trial-balance-card.tsx`:

```tsx
"use client";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Hint } from "@/components/hint";
import { Money } from "@/components/money";
import { useAssetLookup, useTrialBalance } from "@/lib/api/hooks";

// The trial balance, per asset. Debits and credits must agree — every posting
// balances by construction, so a disagreement means something reached the store
// without passing the ledger. `In flight` is the value-dated gap and is
// expected to be non-zero while a payment sits between initiation and
// settlement.
export function TrialBalanceCard({ pid }: { pid: string }) {
  const { data, isLoading } = useTrialBalance(pid);
  const { byCode } = useAssetLookup();

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2 text-base">
          Trial balance
          <Hint id="trial-balance" />
        </CardTitle>
        {data ? (
          <span
            className={
              data.balanced
                ? "text-xs text-muted-foreground"
                : "text-xs font-medium text-destructive"
            }
          >
            {data.balanced ? "Balanced" : "Out of balance"}
          </span>
        ) : null}
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <Skeleton className="h-10 w-full" />
        ) : !data || data.rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">No accounts yet.</p>
        ) : (
          <div className="divide-y text-sm">
            <div className="grid grid-cols-4 gap-2 pb-1 text-xs text-muted-foreground">
              <span>Asset</span>
              <span className="text-right">Debits</span>
              <span className="text-right">Credits</span>
              <span className="text-right">In flight</span>
            </div>
            {data.rows.map((row) => {
              const asset = byCode.get(row.asset);
              return (
                <div key={row.asset} className="grid grid-cols-4 gap-2 py-1.5">
                  <span className="font-medium">{row.asset}</span>
                  <span className="text-right">
                    {asset ? <Money amount={row.debits} asset={asset} /> : row.debits}
                  </span>
                  <span className="text-right">
                    {asset ? <Money amount={row.credits} asset={asset} /> : row.credits}
                  </span>
                  <span className="text-right">
                    {asset ? (
                      <Money amount={row.inFlight} asset={asset} signed />
                    ) : (
                      row.inFlight
                    )}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
```

- [ ] **Step 4: Render it on the ledger page**

In `web/src/app/participants/[pid]/ledger/page.tsx`, import it beside the other component imports:

```tsx
import { TrialBalanceCard } from "@/components/trial-balance-card";
```

and render it inside `LedgerPage`'s outer `<div className="space-y-4">`, immediately after the header `<div className="flex items-center justify-between">…</div>` and before the `ledgers.error ? …` block:

```tsx
      <TrialBalanceCard pid={pid} />
```

- [ ] **Step 5: Add the hint key the card links to**

`<Hint id="trial-balance" />` will not type-check until the key exists — `HintKey` is derived from the registry. Add it to `web/src/components/hint-content.ts`, beside the other balance keys:

```ts
  "trial-balance": {
    title: "Trial balance",
    body: `A **trial balance** totals every account in the book into a debit column and a credit column, per asset. The two must agree.

In this system it cannot fail arithmetically — [[double-entry]] is checked on every posting, so nothing unbalanced can be posted. That is the reason to compute it: it is a control on everything that reached the store *without* going through the ledger — a migration, a seed script, a hand-run \`UPDATE\`.

It is grouped per asset for the same reason the balance check is: amounts are integers in their asset's minor units, so summing across assets would add numbers that mean different things.

An overdrawn customer account appears in the *debit* column: it is a liability holding a debit balance, and each account's balance is restated debit-positive before it is summed.

Note what it does not prove. Both columns are built from the same balance aggregate the rest of the system reads, so a bug *inside* that aggregate cancels out of both and the totals still tie. Proving that would take a second, independent computation over the entries.`,
  },
```

Confirm `double-entry` is the exact existing key before writing the link — an unresolved `[[…]]` takes every dev route down while `next build` stays green.

- [ ] **Step 6: Verify**

Run: `cd web && npm run lint && npm run test && npm run build`
Expected: all green. Then start the app (`make dev`) and load `/participants/<pid>/ledger` — the card renders with the seeded book's totals and `Balanced`, and the `?` popover opens.

- [ ] **Step 7: Commit**

```bash
git add web/src/lib/types.ts web/src/lib/api/endpoints.ts web/src/lib/api/query-keys.ts web/src/lib/api/hooks.ts web/src/components/trial-balance-card.tsx web/src/components/hint-content.ts "web/src/app/participants/[pid]/ledger/page.tsx"
git commit -m "feat(web): show the trial balance on the ledger page

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: The trial balance and the no-`CHECK` counterpoint, across every layer

**Files:**
- Modify: `README.md` (new section under *Reporting and Compliance* at :1026; counterpoint paragraph in *Two Stores, One Conformance Suite* after :1141; a sentence in *The Asset Dimension in the Schema* at :1179)
- Modify: `web/src/components/hint-content.ts`
- Modify: `web/src/lib/quiz/chapters/02-double-entry-bookkeeping.ts`

**Interfaces:**
- Consumes: the `trial-balance` hint key added in Task 3. This is the `CLAUDE.md` layer-consistency sweep for Task 1 and for comparison item 5.
- Produces: hint key `value-date-gap`; two new questions in chapter 02 tagged `concept: "trial-balance"`.

- [ ] **Step 1: Write the hint entry**

In `web/src/components/hint-content.ts`, beside the `trial-balance` key added in Task 3:

```ts
  "value-date-gap": {
    title: "The value-date gap",
    body: `A transaction's two legs can carry **different value dates**: an outbound payment debits the customer today, while the bank's clearing leg takes effect at settlement.

So while a payment is in flight, the book balances by [[booking-date]] and does *not* balance by [[value-date]] — and the difference is exactly the money in flight. On the [[trial-balance]] this appears as a signed figure rather than a failure.

Asserting it to be zero would be a control that fires on correct behaviour. The right treatment is the one the ledger already uses: the money sits visibly in a named clearing account, and the gap is reported.`,
  },
```

Check that `booking-date` and `value-date` are the exact existing keys before writing the links — every `[[…]]` must resolve or the dev app throws on every route. `trial-balance` comes from Task 3.

- [ ] **Step 2: Add the quiz questions**

Chapter 02 is at 20 questions with `double-entry`, `normal-balance` and `reversal` each already used 3×, so two questions on a *new* tag fit within all four diversity rules (22 questions, +1 tag, tag used 2×). Append to `web/src/lib/quiz/chapters/02-double-entry-bookkeeping.ts`:

```ts
    {
      kind: "mc",
      id: "ch2-q21",
      difficulty: "core",
      concept: "trial-balance",
      prompt:
        "Every posting in this system is checked to balance before it is stored. Why compute a trial balance at all?",
      options: [
        "It checks what reached the database without going through the ledger",
        "It recomputes each account's balance more accurately than the ledger does",
        "It is required before an end-of-day snapshot can be taken",
        "It converts every asset to a single reporting currency for comparison",
      ],
      answer: 0,
      explanation:
        "A [[trial-balance]] that cannot fail arithmetically is still worth running, because it is a control on the pipeline rather than on the arithmetic: migrations, seed scripts and hand-run SQL all reach the tables without passing the posting path.",
    },
    {
      kind: "mc",
      id: "ch2-q22",
      difficulty: "challenge",
      concept: "trial-balance",
      prompt:
        "A trial balance is computed by summing every account's balance. What does that shared code path mean for what it proves?",
      options: [
        "A bug inside the balance aggregate cancels out of both columns and stays hidden",
        "Nothing — the totals are computed from the entries directly",
        "It makes the check stricter, since the same code is exercised twice",
        "It means the check can only be trusted for a single asset at a time",
      ],
      answer: 0,
      explanation:
        "Both columns come from the same aggregate, so a fault in that sum lands in both and the totals still tie. Catching that takes an independent recomputation — a second code path over the entries. The [[trial-balance]] here is honest about which of the two it is.",
    },
```

- [ ] **Step 3: Write the README section**

Add to `README.md` under *Reporting and Compliance*, after *End-of-Day Snapshots* and before *Audit Trail*:

```markdown
### Trial Balance

`Book.TrialBalance` totals every account in the book into a debit column and a credit column, per asset. `GET /participants/{pid}/trial-balance` returns it, and the ledger page shows it.

The columns cannot disagree arithmetically. Every posting is checked per asset before it is stored, so nothing unbalanced can be posted — which is exactly why the check is worth running. It is a control on the **pipeline**, not on the arithmetic: what it catches is what reached the store another way. A migration, a seed script, a test fixture writing entries directly through a `Tx`, a hand-run `UPDATE` — none of those pass `PostTransaction`, and all of them can leave a book that no longer ties.

Be precise about what it does *not* prove. Both columns are built from the same store-side balance aggregate the rest of the system reads, so a bug inside that aggregate lands in both columns and cancels out. Proving the aggregate right would take an independent recomputation — the same totals by a second code path over the entries — which this is not.

Each account's balance is restated **debit-positive** before being summed. A balance signed by its own account's normal direction cannot be added to one signed by another's: a liability holding a credit balance and an asset holding a debit balance are both "positive" and belong in opposite columns. One consequence is worth seeing on screen: an overdrawn customer account — a Liability holding a *debit* balance — lands in the debit column, which is the [overdraft](#overdraft) treatment as a report rather than a paragraph.

The report also carries an **in-flight** figure per asset: the same sum, value-dated. It is not required to be zero, and normally is not. The two legs of one transaction may value-date differently — an outbound payment debits the customer on the day it is debited while the bank's clearing leg takes effect at settlement — so between those two dates the value-dated sum is off by exactly the amount in flight. That is a figure to report, not a break to fix; a control that fires on correct behaviour is one nobody reads twice.

Two limits, stated rather than hidden. The `asOf` parameter bounds the in-flight column **only** — the debit and credit columns are always as the book stands now, because no store aggregate restricts by booking date. And there is no roll-forward check (`closing == opening + movement`), because here both of its sides would come from the same aggregate; it becomes worth building on top of an independent recomputation, not before one.
```

- [ ] **Step 4: Write the counterpoint paragraph (comparison item 5)**

In `README.md`, in *Two Stores, One Conformance Suite*, insert immediately after the paragraph ending "Validation belongs in the domain layer; the store is a per-table key/value store that happens to be relational." (`:1141`):

```markdown
That sentence is this repo's position, and it is worth being clear that it is a *consequence* rather than the industry's default. A bank running one backend would be advised the opposite, and for good reasons: `CHECK (direction IN ('D','C'))` and `CHECK (amount_minor > 0)` on the posting tables, a deferred constraint trigger enforcing the per-asset balance invariant so the database holds it even against a buggy or malicious writer, triggers refusing any mutation of a booked journal, and an application role granted `INSERT` and `SELECT` on those tables and no `UPDATE` or `DELETE` — a grant list being the one piece of enforcement an auditor can read without trusting a line of application code. Defence in depth is the right default when there is one store to defend.

What buys the trade here is the conformance suite: a constraint that only one backend can hold would make `store/pg` refuse a write `store/mem` accepts, which is the single thing `store/storetest` exists to prevent. The same reasoning appears again below, for the [day key that replaced an exclusion constraint](#the-ledger-as-relational-tables), and it is why [the asset columns carry no `CHECK`](#the-asset-dimension-in-the-schema). It is also why the [trial balance](#trial-balance) earns its place: it is the control you compute when the database is not permitted to enforce the invariant for you.
```

Then, in *The Asset Dimension in the Schema*, add one sentence at the end of the paragraph that explains the missing `CHECK`:

```markdown
This is the dual-store trade described under [Two Stores, One Conformance Suite](#two-stores-one-conformance-suite) — a single-backend production system would be advised the opposite.
```

Verify the anchor slugs match the actual heading text before committing; GitHub-style anchors are the lower-cased heading with spaces replaced by hyphens and punctuation dropped.

- [ ] **Step 5: Verify**

Run: `cd web && npm run test`
Expected: `concept-links.test.ts` and `diversity.test.ts` green. Then `npm run build`, then load a page under `make dev` and open the trial-balance hint popover.

- [ ] **Step 6: Commit**

```bash
git add README.md web/src/components/hint-content.ts web/src/lib/quiz/chapters/02-double-entry-bookkeeping.ts
git commit -m "docs: teach the trial balance, and say what the no-CHECK position costs

The comparison's item 5: the README argued the dual-store position without
ever saying that a single-backend bank would be advised the opposite —
CHECKs, a deferred balance trigger, no UPDATE or DELETE grant. It says so
now, and the trial balance is the control that stands in for them.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Prove the fixtures, before the rule exists

Tasks 6–8 migrate 49 tests whose fixtures freeze a clock at 2025-01-15 and then simulate months forward. This task builds the one-command probe those tasks verify against, so a fixture migration can be checked *before* the rule that requires it lands.

**Files:**
- Create: `scratch/future-booking-probe.patch` — **not committed**; write it under the scratchpad directory instead if you prefer, and delete it after Task 8.

- [ ] **Step 1: Write the probe**

Save this as a file you can apply and revert (it is the exact rule from Task 8, with a throwaway error):

```diff
--- a/ledger/book.go
+++ b/ledger/book.go
@@
 	valueDate := req.ValueDate
 	if valueDate.IsZero() {
 		valueDate = bookingDate
 	}
+	if DayStart(bookingDate).After(DayStart(now)) {
+		return Transaction{}, errors.New("PROBE: future booking date")
+	}
```

- [ ] **Step 2: Confirm it reproduces the known failure set**

Run:

```bash
git apply scratch/future-booking-probe.patch
go test ./... 2>&1 | grep -c '^--- FAIL'
git checkout ledger/book.go
```

Expected: `49`. If the number differs, the tree has moved since this plan was written — record the new list with `go test ./... 2>&1 | grep '^--- FAIL'` and work from that, rather than from the names below.

- [ ] **Step 3: No commit**

Nothing to commit. The probe is scaffolding for the next three tasks.

---

### Task 6: Advance the `lending` fixtures

26 tests. Their fixture clock reads 2025-01-15 09:00 and never moves, while the tests disburse on 15 February and run end-of-day into March. Under the rule those are future-dated postings, so the fixtures have to advance their clock as the simulated date advances — which is also what makes them honest.

**Files:**
- Modify: `lending/portfolio_test.go` (`newTestPortfolio` at :30-33, `mutableClock` at :22-26)
- Modify: `lending/accrual_test.go`, `lending/arrears_test.go`, `lending/repay_test.go`, `lending/refund_test.go`, `lending/terms_test.go` as the failures dictate

**Interfaces:**
- Consumes: the existing `mutableClock` (`lending/portfolio_test.go:22`) and `newTestPortfolioOn(t, clock func() time.Time)` (`:36`).
- Produces: `newTestPortfolio` returning its clock, so any test can move it. Signature after this task:
  `newTestPortfolio(t *testing.T) (*lending.Portfolio, *ledger.Book, ledger.SubledgerID, ledger.AccountID)` stays as it is; add
  `newTestPortfolioClocked(t *testing.T) (*lending.Portfolio, *ledger.Book, ledger.SubledgerID, ledger.AccountID, *mutableClock)`.

- [ ] **Step 1: Add the clocked fixture**

In `lending/portfolio_test.go`, beside `newTestPortfolio`:

```go
// newTestPortfolioClocked is newTestPortfolio on a clock the test can move.
//
// A test that simulates weeks of a facility's life has to move it: an accrual
// or a repayment dated after the clock is a posting dated in the future, which
// the ledger refuses. Advancing the clock to the day being simulated is what
// the batch would really do.
func newTestPortfolioClocked(t *testing.T) (*lending.Portfolio, *ledger.Book, ledger.SubledgerID, ledger.AccountID, *mutableClock) {
	t.Helper()
	clock := &mutableClock{at: time.Date(2025, time.January, 15, 9, 0, 0, 0, time.UTC)}
	p, book, sub, customer := newTestPortfolioOn(t, clock.now)
	return p, book, sub, customer, clock
}
```

- [ ] **Step 2: Migrate `disbursedLoanIn`, the fixture most tests build on**

In `lending/accrual_test.go`, change `disbursedLoanIn` to build on the clocked portfolio and to move the clock to the disbursement day before disbursing, returning the clock so its callers can keep moving it. Every caller signature changes with it — update `disbursedLoan` and each test that uses either.

The mechanical rule for the whole task, applied everywhere:

> Before any call that takes a date and posts — `Disburse`, `Draw`, `Accrue`, `Repay`, `ChargeInterest`, `RefundInterest`, `RunEndOfDay` — set the clock to that date. Inside a day loop, set it once per iteration. Never move the clock backwards; a test that needs a *backdated* posting keeps the clock ahead and passes the earlier date as the argument, which is exactly what backdating is.

A day loop becomes:

```go
	for d := day(2025, time.January, 16); !d.After(day(2025, time.March, 20)); d = d.AddDate(0, 0, 1) {
		clock.set(d)
		if err := p.RunEndOfDay(ctx, d); err != nil {
			t.Fatalf("RunEndOfDay %v: %v", d, err)
		}
	}
```

- [ ] **Step 3: Work the failure list down**

These 26 must go green. Run one at a time with `go test ./lending/ -run '^TestName$' -v`:

`TestAccrue_CorrectionClampsToWhatTheFacilityOwes`, `TestAccrue_CorrectionRefundsToPrincipal`, `TestAccrue_CorrectsABackdatedRepayment`, `TestAccrue_IgnoresAForwardValueDatedAdvance`, `TestAccrue_IsIdempotentAndOnlyOnDrawnPrincipal`, `TestAccrue_PostsTheDeltaOfTheRoundedValue`, `TestAccrue_RefundFeedsTheFollowingDaysBasis`, `TestChargeInterest_CapitalizesAndBillsTheCycle`, `TestChargeInterest_NegativeResidueStaysInStepWithTheLedger`, `TestChargeInterest_RefusesACycleAlreadyBilled`, `TestClose_RefusesAnUnsettledReceivable`, `TestClose_SucceedsOnAnExactHalfMinorUnitResidue`, `TestDisburse_LeavesTheAccrualWindowAlone`, `TestDisburseAndDraw_RefuseAClosedFacility`, `TestListRefundsPayable_ListsOnlyWhatIsOwed`, `TestRefundInterest_DischargesThePayable`, `TestRefundInterest_PaysPartially`, `TestRefundInterest_RefusesMoreThanIsOwed`, `TestRefundInterest_SurvivesTheFacilityClosing`, `TestRepay_AllocatesAgainstAccruedNotTheSchedule`, `TestRepay_InterestBeforePrincipalOnAPartialPayment`, `TestRepayAndClose_FullSettlement`, `TestRunEndOfDay_AccruesAndMovesFacilitiesIntoArrears`, `TestRunEndOfDay_ACurrentFacilityWritesNoArrearsEvents`, `TestRunEndOfDay_IsIdempotentAndSkipsClosedFacilities`, `TestRunEndOfDay_PayingClearsTheArrears`.

**Do not change an assertion to make a test pass.** Moving a fixture's clock must not move an interest figure: the accrual reads value-dated balances and the dates being accrued do not change. If a *number* moves, stop — that is a real behaviour change and it needs reporting, not accommodating. If a test asserts `CreatedAt` equals the old frozen instant, that assertion is about *when the row was entered* and the correct fix is to expect the day the clock now reads at that point.

- [ ] **Step 4: Verify green both with and without the rule**

Run:

```bash
go test ./lending/...
git apply scratch/future-booking-probe.patch && go test ./lending/... ; git checkout ledger/book.go
```

Expected: PASS both times. The second run is the point of the task.

- [ ] **Step 5: Commit**

```bash
git add lending/
git commit -m "test(lending): advance the fixture clock with the day being simulated

A test that disburses in February and runs end-of-day into March on a clock
frozen at 15 January is posting into the future. Nothing about the accrual
changes: the dates being priced are the same, and the figures with them.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Advance the `deposit`, `payment` and `api` fixtures

23 tests, same rule, three packages. `deposit` already has `mutableClock` and `newTestRegisterOn`; `payment` and `api` build their clock inline and need a variant.

**Files:**
- Modify: `deposit/register_test.go`, `deposit/terms_accrual_test.go` (and any sibling the failures name)
- Modify: `payment/system_test.go` (`newTestNetwork` at :29-33)
- Modify: `api/server_test.go` (`newServer` at :34-46)

**Interfaces:**
- Consumes: `mutableClock` (`deposit/register_test.go:45`), `newTestRegisterOn` (`:67`).
- Produces: `newTestNetworkOn(t *testing.T, clock func() time.Time) *payment.Network` in `payment/system_test.go`; `newServerOn(t *testing.T, populate func(context.Context, *payment.Network) error, clock func() time.Time) *Server` and `newTestServerOn(t *testing.T, clock func() time.Time) http.Handler` in `api/server_test.go`.

- [ ] **Step 1: `deposit` — 17 tests**

Each already has `newTestRegisterOn` available, so the change is `clock := &mutableClock{at: …}`, build with `clock.now`, and `clock.set(d)` before every dated call, exactly as `deposit/register_test.go:1460` already does.

`TestAccrueOverdraft_ExcessFallsBackToTheArrangedRate`, `TestAccrueOverdraft_IsIdempotentPerDate`, `TestAccrueOverdraft_PostsTheDeltaOfTheRoundedValue`, `TestAccrueOverdraft_TiersAtTheLimit`, `TestARepricingPricesItsOwnEffectiveDay`, `TestChargeOverdraftInterest_CapitalizesAndLeavesANegativeResidue`, `TestChargeOverdraftInterest_RefusesClosedAccount`, `TestClose_RefusesAStrandedReceivable`, `TestClose_SucceedsOnAnExactHalfMinorUnitResidue`, `TestOverdraftAccrualCorrectsABackdatedCredit`, `TestOverdraftAccrualCorrectsABackdatedDebit`, `TestOverdraftAccrualUnderThirty360SkipsThe31st`, `TestOverdraftCorrectionRefundsWhatTheReceivableCannotAbsorb`, `TestOverdraftRepricingDoesNotRewindTheAccrualWindow`, `TestRunEndOfDay_AccruesEveryOverdrawnAccount`, `TestSettingOverdraftTermsAccruesAndResetsNothing`, `TestTheReceivableAppearsOnTheFirstAccruingDay`.

Run: `go test ./deposit/...`, then the probe check from Task 6 Step 4 against `./deposit/...`.

- [ ] **Step 2: `payment` — 1 test**

Add the clocked constructor to `payment/system_test.go`:

```go
// newTestNetworkOn is newTestNetwork on a caller-supplied clock, for the tests
// that run a batch for a day later than the frozen one.
func newTestNetworkOn(t *testing.T, clock func() time.Time) *Network {
	t.Helper()
	store := testenv.New(t, clock)
	return NewNetwork(store.Payment(), clock)
}
```

Then in `TestParticipantRunEndOfDay_DrivesBothLayers` (`payment/system_test.go:1010`), build the network on a `mutableClock` — copy the four-line type from `lending/portfolio_test.go:22` if `payment` has none — and set it before each of the two runs:

```go
	clock.set(start)
	assertNoError(t, bank.RunEndOfDay(ctx, start))
	next := start.AddDate(0, 0, 1)
	clock.set(next)
	assertNoError(t, bank.RunEndOfDay(ctx, next))
```

Run: `go test ./payment/...`, then the probe check.

- [ ] **Step 3: `api` — 5 tests**

Add clocked constructors to `api/server_test.go`:

```go
// newServerOn is newServer on a caller-supplied clock, for the tests that drive
// a batch or an accrual for a day later than the frozen one.
func newServerOn(t *testing.T, populate func(context.Context, *payment.Network) error, clock func() time.Time) *Server {
	t.Helper()
	store := testenv.New(t, clock)
	net := payment.NewNetwork(store.Payment(), clock)
	if populate != nil {
		if err := populate(context.Background(), net); err != nil {
			t.Fatalf("populate: %v", err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(net, populate, log)
}

func newTestServerOn(t *testing.T, clock func() time.Time) http.Handler {
	t.Helper()
	return newServerOn(t, nil, clock).Routes()
}
```

Refactor `newServer` to delegate to `newServerOn` with the frozen clock, so there is one construction path. Then migrate: `TestChargeInterestOnRevolvingLine`, `TestChargeOverdraftInterestEndpoint`, `TestEndOfDayAccruesBothFacilityAndOverdraftInterest`, `TestInterestRefundIsListedAndDischargeable`, `TestRepayFromDepositAccount` — each builds the handler on a `mutableClock` and sets it to the date it puts in the request body.

Run: `go test ./api/...`, then the probe check.

- [ ] **Step 4: Verify the whole tree, with and without the rule**

```bash
go test ./...
git apply scratch/future-booking-probe.patch && go test ./... ; git checkout ledger/book.go
```

Expected: `ok` for every package, both times. This is the gate for Task 8.

- [ ] **Step 5: Commit**

```bash
git add deposit/ payment/ api/
git commit -m "test(deposit,payment,api): advance the fixture clocks with the simulated day

Same change as the lending fixtures: a batch run for a day the clock has not
reached is a posting dated in the future.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Refuse a booking date in the future

**Files:**
- Modify: `ledger/errors.go`
- Modify: `ledger/book.go` (new method beside `PostTransaction` at :494; the check inside `PostTransactionTx` after the date defaults at :585-594)
- Modify: `ledger/book_test.go`
- Modify: `deposit/register.go` (`RunEndOfDayTx` at :1749)
- Modify: `lending/arrears.go` (`RunEndOfDayTx` at :172)
- Modify: `api/errors.go` (the 400 group)
- Modify: `api/server_test.go`, `deposit/register_test.go`, `lending/arrears_test.go`

**Interfaces:**
- Consumes: `DayStart` (`ledger/dates.go:13`), `Book.now()`, `r.gl` (`deposit.Register`), `p.gl` (`lending.Portfolio`).
- Produces: `ledger.ErrFutureBookingDate`, `(*Book).CheckBookingDate(d time.Time) error`.

- [ ] **Step 1: Write the failing tests**

Add to `ledger/book_test.go`:

```go
func TestPostTransaction_RefusesABookingDateInTheFuture(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		BookingDate: testClock().AddDate(0, 0, 1),
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10_000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10_000, Direction: Credit},
		},
		Description: "tomorrow's deposit",
	})
	assertError(t, err, ErrFutureBookingDate)
}

// The rule is about the DAY, not the instant: an end-of-day batch stamps 23:59
// and a caller's clock may run a moment ahead of ours.
func TestPostTransaction_AcceptsTodayAndThePast(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	for _, d := range []time.Time{
		time.Date(2025, 1, 15, 23, 59, 59, 0, time.UTC), // end of the clock's own day
		testClock().AddDate(0, -1, 0),                   // a month back
	} {
		_, err := book.PostTransaction(ctx, PostTransactionRequest{
			BookingDate: d,
			Entries: []Entry{
				{AccountID: cash.ID, Amount: 10_000, Direction: Debit},
				{AccountID: alice.ID, Amount: 10_000, Direction: Credit},
			},
			Description: "deposit",
		})
		assertNoError(t, err)
	}
}

// Value dates are NOT bounded. Forward value-dating is how a settlement leg is
// posted, and the payment layer depends on it.
func TestPostTransaction_AllowsAValueDateInTheFuture(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		ValueDate: testClock().AddDate(0, 0, 3),
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10_000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10_000, Direction: Credit},
		},
		Description: "settles on Thursday",
	})
	assertNoError(t, err)
}
```

Add to `deposit/register_test.go`:

```go
func TestRunEndOfDay_RefusesAFutureDateBeforeItPostsAnything(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, prd, 50_000)
	assertNoError(t, err)
	_ = acct

	before, err := book.ListTransactions(ctx)
	assertNoError(t, err)

	err = reg.RunEndOfDay(ctx, fixedTime.AddDate(0, 0, 1))
	assertError(t, err, ledger.ErrFutureBookingDate)

	after, err := book.ListTransactions(ctx)
	assertNoError(t, err)
	assertEqual(t, "transactions written", len(after), len(before))
}
```

Add the mirror to `lending/arrears_test.go`:

```go
func TestRunEndOfDay_RefusesAFutureDateBeforeItPostsAnything(t *testing.T) {
	ctx := context.Background()
	p, book, _, _ := newTestPortfolio(t)

	before, err := book.ListTransactions(ctx)
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}

	err = p.RunEndOfDay(ctx, day(2025, time.January, 16))
	if !errors.Is(err, ledger.ErrFutureBookingDate) {
		t.Fatalf("err = %v, want ErrFutureBookingDate", err)
	}

	after, err := book.ListTransactions(ctx)
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("wrote %d transactions on a refused run", len(after)-len(before))
	}
}
```

And to `api/server_test.go`:

```go
func TestEndOfDayEndpoint_RefusesAFutureDate(t *testing.T) {
	h := newTestServer(t)
	pid := createParticipant(t, h, "Bank")

	rec := do(t, h, "POST", "/participants/"+pid+"/end-of-day", `{"date":"2025-01-16"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run, one package at a time (`-run` applies to the whole invocation, so these cannot be combined):

```bash
go test ./ledger/ -run 'TestPostTransaction_(RefusesABookingDate|AcceptsToday|AllowsAValueDate)' -v
go test ./deposit/ -run TestRunEndOfDay_RefusesAFutureDate -v
go test ./lending/ -run TestRunEndOfDay_RefusesAFutureDate -v
go test ./api/ -run TestEndOfDayEndpoint_RefusesAFutureDate -v
```

Expected: the first three fail to compile (`ErrFutureBookingDate` undefined); the API one fails on status, because a future-dated run currently succeeds.

- [ ] **Step 3: Add the sentinel**

In `ledger/errors.go`, inside the `var (…)` block:

```go
	// ErrFutureBookingDate is returned when a posting's booking date names a
	// later day than the Book's clock does.
	//
	// A booking date is a claim about when something was recorded, and nothing
	// is recorded before it happens. A future-dated instruction is a different
	// object — a scheduled payment, a standing order — and belongs wherever
	// those are kept, not in a ledger of facts.
	//
	// Value dates are deliberately not bounded: forward value-dating is how a
	// settlement leg is posted.
	ErrFutureBookingDate = errors.New("booking date is in the future")
```

- [ ] **Step 4: Add the rule**

In `ledger/book.go`, above `PostTransaction`:

```go
// CheckBookingDate refuses a booking date that names a later day than this
// Book's clock does.
//
// Day granularity, not instant: an end-of-day batch legitimately stamps 23:59,
// and a caller's clock may run a moment ahead of ours. What is being refused is
// a posting DATED tomorrow, not one whose timestamp is a second early.
//
// It is exported because the end-of-day batches above this layer want to refuse
// a bad date once, before their loop, rather than at whichever account happens
// to post first. The rule itself lives here and only here — PostTransactionTx
// applies it to every posting whatever else validated upstream, on the same
// principle that puts the idempotency claim in the store rather than in a
// caller's memory.
//
// Backdating is untouched, and load-bearing: the accrual recompute re-derives
// past days on purpose.
func (s *Book) CheckBookingDate(d time.Time) error {
	if DayStart(d).After(DayStart(s.now())) {
		return ErrFutureBookingDate
	}
	return nil
}
```

In `PostTransactionTx`, immediately after the `valueDate` defaulting block:

```go
	// Refuse a posting dated in the future. The value date above is
	// deliberately not checked: a leg may take economic effect later, and the
	// payment layer's settlement leg always does.
	if err := s.CheckBookingDate(bookingDate); err != nil {
		return Transaction{}, err
	}
```

- [ ] **Step 5: Add the two batch pre-checks**

At the top of `deposit.Register.RunEndOfDayTx`, before `tx.ListDepositAccounts`:

```go
	// One check for the command, before the loop. Without it a future-dated run
	// is refused by whichever account posts first, and the error names an
	// account when the fault is in the date. Nothing is half-posted either way —
	// the whole Update rolls back — but "which account?" is the wrong question
	// to leave an operator holding.
	if err := r.gl.CheckBookingDate(date); err != nil {
		return err
	}
```

The same at the top of `lending.Portfolio.RunEndOfDayTx`, before `tx.ListFacilities`, with `p.gl`. `payment.Participant.RunEndOfDay` drives both and inherits the refusal from each; it needs no change.

- [ ] **Step 6: Map the sentinel to 400**

In `api/errors.go`, add to the `http.StatusBadRequest` group:

```go
		// A booking date in the future is a bad field value, not a state
		// conflict: the ledger holds facts, and tomorrow is not one yet.
		errors.Is(err, ledger.ErrFutureBookingDate),
```

- [ ] **Step 7: Run everything**

Run: `go test ./... && make test-pg`
Expected: every package ok on both backends. If any test outside the four new ones fails, Tasks 6–7 missed a fixture — fix it there, in that package, by moving the clock, not by relaxing the rule.

- [ ] **Step 8: Commit and delete the probe**

```bash
rm -f scratch/future-booking-probe.patch
git add ledger/ deposit/ lending/ api/
git commit -m "feat(ledger): refuse a booking date that names a later day than today

A posting dated tomorrow is a scheduled instruction, not a fact, and this
ledger holds facts. The rule lives on Book so the two end-of-day batches can
refuse the command before their loop rather than at whichever account posts
first. Value dates stay unbounded: a settlement leg takes effect later by
design.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: The booking-date rule across every layer, and close the comparison items

**Files:**
- Modify: `README.md` (*Booking Date vs. Value Date* at :424; *Next Work* at :1007 if it names any of these)
- Modify: `web/src/components/hint-content.ts` (the existing `booking-date` entry)
- Modify: `web/src/lib/quiz/chapters/06-booking-date-vs-value-date.ts`
- Modify: `docs/cbs-vs-book.md` (the status table)

**Interfaces:**
- Consumes: nothing in code.
- Produces: one new question in chapter 06 tagged `concept: "future-booking-date"`.

- [ ] **Step 1: README**

In *Booking Date vs. Value Date*, add after the paragraph that defines the booking date:

```markdown
A booking date may be in the past and may not be in the future. Backdating is ordinary — a correction discovered on Thursday for an event that happened on Monday books today and value-dates Monday, and the accrual re-derives every day the correction touches. A booking date in the *future* is refused (`ErrFutureBookingDate`), because a booking date is a claim about when something was recorded and nothing is recorded before it happens. A future-dated instruction — a standing order, a scheduled payment — is a different object and belongs wherever those are kept.

The rule is drawn at the **day**, not the instant: an end-of-day batch legitimately stamps 23:59, and a caller's clock may run a moment ahead of the book's. Value dates are deliberately not bounded at all, because forward value-dating is exactly how a settlement leg is posted.

The check lives in `PostTransactionTx`, so every layer inherits it. Both end-of-day batches also call `Book.CheckBookingDate` once before their loop — not a second rule, the same one, asked early so a bad date fails the *command* rather than whichever account happens to post first.
```

- [ ] **Step 2: Hint**

Extend the existing `booking-date` entry in `hint-content.ts` with a closing paragraph (do not create a second key):

```
A booking date can be backdated and cannot be future-dated: it records when something was booked, and nothing is booked before it happens. A [[value-date]], by contrast, is free to be in the future — that is how a settlement leg is posted.
```

- [ ] **Step 3: Quiz**

Chapter 06 has 21 questions, so exactly one may be added, and `booking-date` and `value-date` are both already at 3× — so it must carry a new tag. Append to `web/src/lib/quiz/chapters/06-booking-date-vs-value-date.ts`:

```ts
    {
      kind: "mc",
      id: "ch6-q22",
      difficulty: "core",
      concept: "future-booking-date",
      prompt:
        "An operator runs the end-of-day batch with tomorrow's date. What should a ledger do?",
      options: [
        "Refuse it: a booking date records when something was booked, and nothing is booked before it happens",
        "Accept it, since the postings will be correct by tomorrow anyway",
        "Accept it but mark the transactions pending until the date arrives",
        "Accept it and set the value date to today to compensate",
      ],
      answer: 0,
      explanation:
        "A [[booking-date]] in the future is a scheduled instruction wearing a fact's clothes. Backdating is ordinary and stays allowed; forward *value* dating stays allowed too, because that is how a settlement leg is posted. Only the booking date is bounded.",
    },
```

- [ ] **Step 4: Close the comparison items**

In `docs/cbs-vs-book.md`, in the status table, replace the three rows:

```markdown
| 5. No-`CHECK` position is a dual-store consequence | **closed** — *Two Stores, One Conformance Suite* now says what a single-backend bank would be advised instead: `CHECK`s on the posting tables, a deferred constraint trigger for the balance invariant, no `UPDATE`/`DELETE` grant, and triggers refusing mutation of a booked journal. The repo's position is stated as the price of conformance rather than as the industry's default | `README.md` *Two Stores, One Conformance Suite* |
| 6. Trial balance | **closed, at the strength it can honestly claim** — `Book.TrialBalance` totals the book per asset, debit-positive, inside one unit of work, and reports the value-dated gap as a figure rather than a failure. It shares `BookBalance` with the thing it checks, so it is a control on the pipeline and on direct writes, not on the aggregate; §5.4.2's independent recomputation, and the roll-forward that would rest on it, stay open by choice | `ledger/trialbalance.go` |
| 9. Reject future booking dates | **closed** — `PostTransactionTx` refuses a booking date naming a later day than the Book's clock, and both end-of-day batches ask `CheckBookingDate` before their loop so a bad date fails the command. Day granularity, not instant; value dates stay unbounded. It found 49 tests simulating months forward on a frozen clock, which is the same defect in fixture form | `ledger/book.go`, `deposit/register.go`, `lending/arrears.go` |
```

Also update the count in the paragraph above the table ("Seven of its findings have since been closed") to match, and add a line to the two-item list beneath it recording what item 9 turned up:

```markdown
- **Item 9 was a three-line rule and a 49-test migration.** The lending and
  deposit fixtures froze a clock at 15 January 2025 and then simulated into
  March, so every accrual and batch in them was a future-dated posting by the
  new rule's own definition. Advancing each fixture's clock to the day it
  simulates changed no interest figure — the days being priced never moved —
  which is the evidence that the rule found a fixture defect rather than
  causing one.
```

- [ ] **Step 5: Verify**

Run: `cd web && npm run test && npm run build`, then `go test ./...` one last time, then load a page under `make dev` and open the booking-date hint.

- [ ] **Step 6: Commit**

```bash
git add README.md web/src/components/hint-content.ts web/src/lib/quiz/chapters/06-booking-date-vs-value-date.ts docs/cbs-vs-book.md
git commit -m "docs: teach the booking-date rule, and close comparison items 5, 6 and 9

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## Failure modes to keep in view while implementing

| Failure | Symptom | What to do |
| --- | --- | --- |
| A fixture migration moves an interest figure | An assertion about accrued interest fails after only the clock moved | Stop. The days being priced did not change, so a moved figure is a real behaviour change — report it rather than updating the expectation |
| The rule lands before the fixtures | 49 unrelated failures in one commit, unreviewable | Tasks 6–7 precede Task 8, and each verifies under the probe patch |
| `InFlight` asserted to be zero somewhere | A correct in-flight payment reads as a break | Only `Debits == Credits` is ever asserted; `InFlight` is reported |
| A `[[wiki-link]]` written before its hint key | Every dev route throws; `next build` stays green and hides it | `npm run test` before any commit touching hints or quiz text |
| The trial balance walk moved outside one `View` | Intermittent false breaks under concurrent posting | `TrialBalanceTx` is the only place the walk lives; `TrialBalance` wraps it in one `View` |
| A store method added for the booking-date column | Dual-store conformance work this branch did not scope | The `asOf` parameter bounds `InFlight` only, by design — see the spec |
