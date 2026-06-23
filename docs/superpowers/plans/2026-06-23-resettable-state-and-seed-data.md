# Resettable State + Comprehensive Seed Data Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user reset the whole banking system to a known-good state at any time by atomically swapping the single state root, and ship a comprehensive, valid sample dataset that the system boots with and resets to.

**Architecture:** `payment.Network` is already the single in-memory state root. `api.Server` will hold it behind an `atomic.Pointer` plus a `newState func() *payment.Network` factory; reset = build a fresh network from the factory and atomically store it. A new `seed` package builds the sample network on a deterministic, controllable clock (frozen during build for reproducible IDs/dates, then switched to real time). `main` wires `seed.Network` as the factory; the frontend gets a reset button that calls `POST /admin/reset` and invalidates all caches.

**Tech Stack:** Go 1.24 (stdlib `sync/atomic`, `net/http`), Next.js 16 + React 19 + TanStack Query + shadcn/ui + sonner (frontend).

## Global Constraints

- Module path: `github.com/raphi011/ledger`. Go version: `go 1.24.7`.
- All monetary amounts are `int64` minor units (cents). Never use floats.
- Every `ledger.Book`/`deposit.Register`/`payment.Network` created in one logical run must share ONE `clock func() time.Time` (use `NewNetworkWithClock`).
- Backend handlers must keep accessing state via a single accessor, never a captured `*payment.Network`, so reset is observed by every handler.
- Frontend: amounts are integer cents; the browser only ever calls same-origin `/api/...`; reuse existing primitives (`Button`, `ConfirmAction`, `request`, TanStack Query, `sonner`); never send keys a request DTO doesn't define (`DisallowUnknownFields`). The reset request has no body.
- Frontend gates before commit: `npm run typecheck`, `npm run lint`, `npm run build` must all be clean.
- Seed data must be internally consistent and valid: every operation in the build must succeed; the builder panics on any error (seed is hardcoded and deterministic, so an error is a programming bug).

---

## File Structure

- `seed/clock.go` (new) — controllable deterministic clock (`frozen` → `live`).
- `seed/seed.go` (new) — `Network()` + the `builder` and the full sample scenario.
- `seed/seed_test.go` (new) — invariants, determinism, and clock-went-live tests.
- `seed/doc.go` (new) — package doc.
- `api/server.go` (modify) — `atomic.Pointer` state, `newState` factory, `network()` accessor, `Reset()`, admin route registration.
- `api/handlers_admin.go` (new) — `registerAdminRoutes` + `handleReset`.
- `api/handlers_participant.go`, `api/handlers_payment.go` (modify) — `s.net` → `s.network()`.
- `api/server_test.go` (modify) — new `NewServer` signature; add reset test.
- `cmd/server/main.go` (modify) — wire `seed.Network` as the factory.
- `web/src/lib/api/endpoints.ts` (modify) — `resetState()`.
- `web/src/lib/api/hooks.ts` (modify) — `useResetState()`.
- `web/src/components/reset-button.tsx` (new) — confirm dialog + reset + toast.
- `web/src/components/app-shell.tsx` (modify) — render `ResetButton` in sidebar + drawer.

---

### Task 1: Deterministic clock for the seed package

**Files:**
- Create: `seed/clock.go`
- Test: `seed/clock_test.go`

**Interfaces:**
- Produces: `newClock(start time.Time) *clock`; methods `(*clock).now() time.Time`, `(*clock).advance(d time.Duration)`, `(*clock).goLive()`. `now` is safe to pass as `func() time.Time`.

- [ ] **Step 1: Write the failing test**

Create `seed/clock_test.go`:

```go
package seed

import (
	"testing"
	"time"
)

func TestClockFrozenThenLive(t *testing.T) {
	start := time.Date(2025, 9, 15, 9, 0, 0, 0, time.UTC)
	c := newClock(start)

	if got := c.now(); !got.Equal(start) {
		t.Fatalf("frozen now = %v, want %v", got, start)
	}

	c.advance(24 * time.Hour)
	want := start.Add(24 * time.Hour)
	if got := c.now(); !got.Equal(want) {
		t.Fatalf("after advance now = %v, want %v", got, want)
	}

	c.goLive()
	got := c.now()
	if time.Since(got) > time.Minute || got.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("after goLive now = %v, want ~time.Now()", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./seed/ -run TestClockFrozenThenLive -v`
Expected: FAIL — build error, `undefined: newClock`.

- [ ] **Step 3: Write the implementation**

Create `seed/clock.go`:

```go
package seed

import (
	"sync"
	"time"
)

// clock is a controllable time source for deterministic seeding. While frozen
// it returns a fixed instant that the seed builder advances step by step, which
// keeps generated IDs, booking dates and value dates reproducible. Once live it
// delegates to time.Now, so any operation performed after seeding (for example
// via the API after a reset) is timestamped in real time.
type clock struct {
	mu   sync.Mutex
	t    time.Time
	live bool
}

// newClock returns a clock frozen at start.
func newClock(start time.Time) *clock { return &clock{t: start} }

// now returns the current time: the frozen instant while building, or the real
// wall-clock time once goLive has been called.
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live {
		return time.Now()
	}
	return c.t
}

// advance moves the frozen clock forward. It has no effect once live.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// goLive switches the clock to real wall-clock time, permanently.
func (c *clock) goLive() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.live = true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./seed/ -run TestClockFrozenThenLive -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add seed/clock.go seed/clock_test.go
git commit -m "Add deterministic clock for seed package"
```

---

### Task 2: `seed.Network()` — the full sample scenario

**Files:**
- Create: `seed/seed.go`, `seed/doc.go`
- Test: `seed/seed_test.go`

**Interfaces:**
- Consumes: `newClock`/`(*clock)` from Task 1; `payment.NewNetworkWithClock`, `payment.Network` methods (`AddParticipant`, `Deposit`, `CreateMandate`, `RevokeMandate`, `OpenCycle`, `InitiatePayment`, `CloseCycle`, `SettleCycle`, `ReturnPayment`, `RejectPayment`, `List*`, `ReserveBalance`); `payment.Participant` (`OpenCustomerAccount`, `Deposit`, `Ledger`, `CustomerSubledger`, `ID`); `deposit.Register` (`OpenAccount`, `CreateHold`, `ReleaseHold`, `CaptureHold`, `GetAccount`, `Freeze`, `MarkDormant`, `Close`, `TakeEndOfDaySnapshot`, `ListAccounts`); `ledger.Book` (`GetSubledger`, `CreateSubledger`, `CreateAccount`, `PostTransaction`, `ReverseTransaction`); constants `payment.SchemeSEPACT`, `payment.SchemeSEPADD`, `ledger.Asset/Liability/Equity/Revenue/Debit/Credit`.
- Produces: `seed.Network() *payment.Network`.

- [ ] **Step 1: Write the failing test**

Create `seed/seed_test.go`:

```go
package seed

import (
	"testing"
	"time"

	"github.com/raphi011/ledger/deposit"
	"github.com/raphi011/ledger/payment"
)

func TestNetworkShape(t *testing.T) {
	net := Network()

	if got := len(net.ListParticipants()); got != 4 {
		t.Fatalf("participants = %d, want 4", got)
	}
	if got := len(net.ListMandates()); got != 3 {
		t.Fatalf("mandates = %d, want 3", got)
	}
	if got := len(net.ListCycles()); got != 5 {
		t.Fatalf("cycles = %d, want 5", got)
	}
	if got := len(net.ListSettlements()); got != 2 {
		t.Fatalf("settlements = %d, want 2", got)
	}

	// Deposit accounts: 5 + 3 + 2 + 2 = 12 across the four banks.
	total := 0
	for _, p := range net.ListParticipants() {
		total += len(p.Deposit.ListAccounts())
	}
	if total != 12 {
		t.Fatalf("deposit accounts = %d, want 12", total)
	}
}

func TestPaymentStatusCoverage(t *testing.T) {
	net := Network()
	byStatus := map[payment.PaymentStatus]int{}
	for _, p := range net.ListPayments() {
		byStatus[p.Status]++
	}
	want := map[payment.PaymentStatus]int{
		payment.Settled:  4,
		payment.Returned: 1,
		payment.Cleared:  2,
		payment.Accepted: 2,
		payment.Rejected: 1,
	}
	for status, n := range want {
		if byStatus[status] != n {
			t.Errorf("status %v = %d, want %d", status, byStatus[status], n)
		}
	}
}

func TestAccountStatusCoverage(t *testing.T) {
	net := Network()
	seen := map[deposit.AccountStatus]bool{}
	for _, p := range net.ListParticipants() {
		for _, a := range p.Deposit.ListAccounts() {
			seen[a.Status] = true
		}
	}
	for _, st := range []deposit.AccountStatus{deposit.Active, deposit.Dormant, deposit.Frozen, deposit.Closed} {
		if !seen[st] {
			t.Errorf("missing account status %v in seed data", st)
		}
	}
}

func TestReservesConserved(t *testing.T) {
	net := Network()
	var sum int64
	for _, p := range net.ListParticipants() {
		bal, err := net.ReserveBalance(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if bal < 0 {
			t.Errorf("participant %s reserve negative: %d", p.ID, bal)
		}
		sum += bal
	}
	// Total reserves equal total funded; settlements and returns only move
	// reserves between participants.
	const wantFunded = 1_120_000
	if sum != wantFunded {
		t.Fatalf("sum of reserves = %d, want %d", sum, wantFunded)
	}
}

func TestDeterministicIDs(t *testing.T) {
	a := Network()
	b := Network()

	pa, pb := a.ListParticipants(), b.ListParticipants()
	if len(pa) != len(pb) {
		t.Fatalf("participant counts differ: %d vs %d", len(pa), len(pb))
	}
	for i := range pa {
		if pa[i].ID != pb[i].ID || pa[i].Name != pb[i].Name {
			t.Fatalf("participant %d differs: %v/%v vs %v/%v", i, pa[i].ID, pa[i].Name, pb[i].ID, pb[i].Name)
		}
	}

	xa, xb := a.ListPayments(), b.ListPayments()
	if len(xa) != len(xb) {
		t.Fatalf("payment counts differ: %d vs %d", len(xa), len(xb))
	}
	for i := range xa {
		if xa[i].ID != xb[i].ID || xa[i].Status != xb[i].Status || xa[i].Amount != xb[i].Amount {
			t.Fatalf("payment %d differs across builds", i)
		}
	}
}

func TestClockWentLive(t *testing.T) {
	net := Network()
	accts := net.ListParticipants()[0].Deposit.ListAccounts()
	if len(accts) == 0 {
		t.Fatal("first participant has no accounts")
	}
	ref := payment.PartyRef{Participant: net.ListParticipants()[0].ID, Account: accts[0].ID}

	// A mutation after build must be timestamped in real time, not at baseDate.
	m, err := net.CreateMandate(ref, ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(m.CreatedAt) > time.Minute {
		t.Fatalf("mandate CreatedAt = %v, expected ~now (clock did not go live)", m.CreatedAt)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./seed/ -run TestNetworkShape -v`
Expected: FAIL — build error, `undefined: Network`.

- [ ] **Step 3: Write the package doc**

Create `seed/doc.go`:

```go
// Package seed builds a comprehensive, internally consistent sample payment
// network for the core-banking system: four banks, customer deposit accounts in
// every lifecycle status, authorization holds, end-of-day snapshots, direct-debit
// mandates, and payments spanning the full lifecycle (settled, returned, cleared,
// accepted and rejected) across settled, closed and open clearing cycles.
//
// The scenario is built on a deterministic clock so IDs and dates are
// reproducible across runs; the clock is switched to real wall-clock time before
// the network is returned, so operations performed afterwards (for example via
// the API after a reset) are timestamped live. It is the factory the server boots
// from and resets to.
package seed
```

- [ ] **Step 4: Write the implementation**

Create `seed/seed.go`:

```go
package seed

import (
	"fmt"
	"time"

	"github.com/raphi011/ledger"
	"github.com/raphi011/ledger/deposit"
	"github.com/raphi011/ledger/payment"
)

// baseDate anchors the deterministic seed timeline. Everything built before the
// clock goes live is dated relative to this instant.
var baseDate = time.Date(2025, 9, 15, 9, 0, 0, 0, time.UTC)

// Network builds a fresh payment.Network populated with the full sample scenario
// (see the package doc) on a frozen, deterministic clock, then switches the
// clock to real time before returning so later operations are timestamped live.
func Network() *payment.Network {
	c := newClock(baseDate)
	net := payment.NewNetworkWithClock(c.now)
	b := &builder{net: net, clock: c, ibans: map[deposit.AccountID]string{}}
	b.build()
	c.goLive()
	return net
}

type builder struct {
	net   *payment.Network
	clock *clock
	// ibans assigns one canonical IBAN per deposit account so a PartyRef for an
	// account is identical wherever it appears. The SDD scheme matches a payment
	// to its mandate by PartyRef equality, and PartyRef includes the IBAN field —
	// so a mandate and its direct debits must reference the account the same way.
	ibans map[deposit.AccountID]string
}

// must returns v, panicking on a non-nil error. Seed data is hardcoded and
// deterministic, so any error is a programming bug that should fail loudly.
func must[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("seed: %v", err))
	}
	return v
}

// check panics on a non-nil error from a call that returns only an error.
func check(err error) {
	if err != nil {
		panic(fmt.Sprintf("seed: %v", err))
	}
}

// open opens a customer account, records its canonical IBAN, and returns it.
func (b *builder) open(p *payment.Participant, name, iban string) deposit.Account {
	a := must(p.OpenCustomerAccount(name))
	b.ibans[a.ID] = iban
	return a
}

// openOverdraft opens a customer account with an overdraft limit (the
// participant helper only opens with zero overdraft) and records its IBAN.
func (b *builder) openOverdraft(p *payment.Participant, name, iban string, limit ledger.Amount) deposit.Account {
	a := must(p.Deposit.OpenAccount(p.CustomerSubledger, name, limit))
	b.ibans[a.ID] = iban
	return a
}

// ref builds a PartyRef for a customer deposit account using its canonical IBAN,
// so the same account always produces an identical PartyRef.
func (b *builder) ref(p *payment.Participant, acct deposit.Account) payment.PartyRef {
	return payment.PartyRef{Participant: p.ID, Account: acct.ID, IBAN: b.ibans[acct.ID]}
}

// fund credits a deposit account with cash and raises the bank's reserve.
func (b *builder) fund(p *payment.Participant, acct deposit.Account, amount ledger.Amount) {
	check(b.net.Deposit(p.ID, acct.ID, amount, "Opening deposit"))
}

func (b *builder) initSCT(dp *payment.Participant, d deposit.Account, cp *payment.Participant, c deposit.Account, amount ledger.Amount, e2e, desc string) payment.Payment {
	return must(b.net.InitiatePayment(payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      b.ref(dp, d),
		Creditor:    b.ref(cp, c),
		Amount:      amount,
		EndToEndID:  e2e,
		Description: desc,
	}))
}

func (b *builder) initSDD(dp *payment.Participant, d deposit.Account, cp *payment.Participant, c deposit.Account, amount ledger.Amount, mandate payment.MandateID, e2e, desc string) payment.Payment {
	return must(b.net.InitiatePayment(payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPADD,
		Debtor:      b.ref(dp, d),
		Creditor:    b.ref(cp, c),
		Amount:      amount,
		MandateID:   mandate,
		EndToEndID:  e2e,
		Description: desc,
	}))
}

func (b *builder) build() {
	// --- Banks -------------------------------------------------------------
	aurora := must(b.net.AddParticipant("Aurora Bank"))
	verde := must(b.net.AddParticipant("Banca Verde"))
	nord := must(b.net.AddParticipant("Nordhaven Bank"))
	soleil := must(b.net.AddParticipant("Crédit Soleil"))

	// --- Customer accounts (each gets a canonical IBAN) --------------------
	alice := b.open(aurora, "Alice Andersson", "SE89-AURORA-1001")
	aaron := b.open(aurora, "Aaron Apstorp", "SE89-AURORA-1002")
	annie := b.open(aurora, "Annie Ahlberg", "SE89-AURORA-1003")    // -> Dormant
	merchant := b.open(aurora, "Aurora Merchant", "SE89-AURORA-1004") // hold-capture counterparty
	oldAcct := b.open(aurora, "Closed Account", "SE89-AURORA-1005")  // -> Closed

	bruno := b.openOverdraft(verde, "Bruno Bianchi", "IT60-VERDE-2001", 50_000) // 500.00 overdraft
	bella := b.open(verde, "Bella Bruno", "IT60-VERDE-2002")
	bianca := b.open(verde, "Bianca Belli", "IT60-VERDE-2003") // -> Frozen

	nora := b.open(nord, "Nora Nilsson", "NO93-NORD-3001")
	niklas := b.open(nord, "Niklas Nyborg", "NO93-NORD-3002")

	chloe := b.open(soleil, "Chloé Caron", "FR76-SOLEIL-4001")
	claude := b.open(soleil, "Claude Clément", "FR76-SOLEIL-4002")

	// --- Funding (also raises each bank's central-bank reserve) ------------
	b.fund(aurora, alice, 200_000)
	b.fund(aurora, aaron, 50_000)
	b.fund(aurora, annie, 30_000)
	b.fund(verde, bruno, 150_000)
	b.fund(verde, bella, 80_000)
	b.fund(verde, bianca, 40_000)
	b.fund(nord, nora, 300_000)
	b.fund(nord, niklas, 60_000)
	b.fund(soleil, chloe, 120_000)
	b.fund(soleil, claude, 90_000)

	b.clock.advance(2 * time.Hour)

	// --- Holds on Alice: active / released / captured ----------------------
	must(aurora.Deposit.CreateHold(deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 10_000, Description: "Card pre-authorisation (hotel)",
	}))
	released := must(aurora.Deposit.CreateHold(deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 5_000, Description: "Cancelled online order",
	}))
	check(aurora.Deposit.ReleaseHold(released.ID))
	captured := must(aurora.Deposit.CreateHold(deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 15_000, Description: "Card payment at Aurora Merchant",
	}))
	merchantGL := must(aurora.Deposit.GetAccount(merchant.ID)).GLAccount
	must(aurora.Deposit.CaptureHold(captured.ID, merchantGL, 0, "Captured: card payment"))

	// --- End-of-day snapshots for Alice across two business days -----------
	must(aurora.Deposit.TakeEndOfDaySnapshot(alice.ID, b.clock.now()))
	b.clock.advance(24 * time.Hour)
	must(aurora.Deposit.TakeEndOfDaySnapshot(alice.ID, b.clock.now()))

	// --- Account status lifecycle ------------------------------------------
	check(aurora.Deposit.MarkDormant(annie.ID)) // Active -> Dormant
	check(aurora.Deposit.Close(oldAcct.ID))     // zero balance -> Closed
	check(verde.Deposit.Freeze(bianca.ID))      // Active -> Frozen

	// --- Mandates for SEPA Direct Debit ------------------------------------
	m1 := must(b.net.CreateMandate(b.ref(soleil, chloe), b.ref(nord, nora), 100_000))
	m2 := must(b.net.CreateMandate(b.ref(verde, bruno), b.ref(aurora, aaron), 0))
	m3 := must(b.net.CreateMandate(b.ref(nord, niklas), b.ref(soleil, claude), 25_000))
	check(b.net.RevokeMandate(m3.ID)) // revoked, for display

	b.clock.advance(1 * time.Hour)

	// --- Phase A: a fully settled SEPA Credit Transfer cycle ---------------
	sct1 := must(b.net.OpenCycle(payment.SchemeSEPACT))
	b.initSCT(aurora, alice, nord, niklas, 25_000, "SCT-001", "Rent to N. Nyborg")
	b.initSCT(nord, nora, verde, bella, 40_000, "SCT-002", "Invoice 2025-77")
	b.initSCT(verde, bruno, soleil, chloe, 30_000, "SCT-003", "Consulting fee")
	must(b.net.CloseCycle(sct1.ID))
	must(b.net.SettleCycle(sct1.ID))

	b.clock.advance(24 * time.Hour)

	// --- Phase B: a settled SEPA Direct Debit cycle (one will be returned) --
	sdd1 := must(b.net.OpenCycle(payment.SchemeSEPADD))
	b.initSDD(soleil, chloe, nord, nora, 20_000, m1.ID, "SDD-001", "Utility direct debit")
	returned := b.initSDD(verde, bruno, aurora, aaron, 12_000, m2.ID, "SDD-002", "Gym membership")
	must(b.net.CloseCycle(sdd1.ID))
	must(b.net.SettleCycle(sdd1.ID))

	// --- Phase C: return the settled direct debit (an R-transaction) --------
	must(b.net.ReturnPayment(returned.ID, "Debtor dispute — unauthorised collection"))

	b.clock.advance(24 * time.Hour)

	// --- Phase D: a closed-but-not-settled SCT cycle (payments stay Cleared) -
	sct2 := must(b.net.OpenCycle(payment.SchemeSEPACT))
	b.initSCT(aurora, aaron, soleil, claude, 8_000, "SCT-010", "Book order")
	b.initSCT(verde, bella, nord, niklas, 6_000, "SCT-011", "Shared dinner")
	must(b.net.CloseCycle(sct2.ID))

	// --- Phase E: an open SDD cycle with an accepted and a rejected payment --
	must(b.net.OpenCycle(payment.SchemeSEPADD))
	b.initSDD(soleil, chloe, nord, nora, 5_000, m1.ID, "SDD-010", "Monthly subscription")
	reject := b.initSDD(verde, bruno, aurora, aaron, 3_000, m2.ID, "SDD-011", "Disputed charge")
	must(b.net.RejectPayment(reject.ID, "Insufficient mandate coverage"))

	// --- Phase F: an open SCT cycle with an accepted payment ----------------
	must(b.net.OpenCycle(payment.SchemeSEPACT))
	b.initSCT(aurora, alice, verde, bella, 7_000, "SCT-020", "Birthday gift")

	// --- General-ledger primitives showcase on Aurora ----------------------
	b.glShowcase(aurora, aaron)
}

// glShowcase exercises the raw general-ledger primitives on one bank so that all
// five account types and a manual transaction + reversal appear in the data.
func (b *builder) glShowcase(p *payment.Participant, customer deposit.Account) {
	glID := must(p.Ledger.GetSubledger(p.CustomerSubledger)).LedgerID

	equitySub := must(p.Ledger.CreateSubledger(glID, "Equity"))
	shareCapital := must(p.Ledger.CreateAccount(equitySub.ID, "Share Capital", ledger.Equity))
	treasurySub := must(p.Ledger.CreateSubledger(glID, "Treasury"))
	vault := must(p.Ledger.CreateAccount(treasurySub.ID, "Vault Cash", ledger.Asset))
	incomeSub := must(p.Ledger.CreateSubledger(glID, "Income"))
	feeIncome := must(p.Ledger.CreateAccount(incomeSub.ID, "Fee Income", ledger.Revenue))

	// Capitalisation: Vault Cash (asset) up, Share Capital (equity) up.
	must(p.Ledger.PostTransaction(ledger.PostTransactionRequest{
		Description: "Initial share capital",
		Entries: []ledger.Entry{
			{AccountID: vault.ID, Amount: 100_000, Direction: ledger.Debit},
			{AccountID: shareCapital.ID, Amount: 100_000, Direction: ledger.Credit},
		},
	}))

	// Monthly account fee: customer deposit (liability) down, Fee Income (revenue) up.
	customerGL := must(p.Deposit.GetAccount(customer.ID)).GLAccount
	fee := must(p.Ledger.PostTransaction(ledger.PostTransactionRequest{
		Description: "Monthly account fee",
		Entries: []ledger.Entry{
			{AccountID: customerGL, Amount: 500, Direction: ledger.Debit},
			{AccountID: feeIncome.ID, Amount: 500, Direction: ledger.Credit},
		},
	}))

	// Reverse the fee (goodwill waiver) to demonstrate a reversal.
	must(p.Ledger.ReverseTransaction(fee.ID, "Fee waived — goodwill"))
}
```

- [ ] **Step 5: Run the full seed test suite to verify it passes**

Run: `go test ./seed/ -v`
Expected: PASS for all of `TestClockFrozenThenLive`, `TestNetworkShape`, `TestPaymentStatusCoverage`, `TestAccountStatusCoverage`, `TestReservesConserved`, `TestDeterministicIDs`, `TestClockWentLive`.

If `TestReservesConserved` fails on the total, recompute `wantFunded` as the exact sum of the `b.fund(...)` amounts and update the constant; do not change funding amounts to chase the number unless a payment validity error appears.

- [ ] **Step 6: Commit**

```bash
git add seed/seed.go seed/doc.go seed/seed_test.go
git commit -m "Add seed package with comprehensive sample scenario"
```

---

### Task 3: Make `api.Server` state swappable + `POST /admin/reset`

**Files:**
- Modify: `api/server.go`
- Create: `api/handlers_admin.go`
- Modify: `api/handlers_participant.go`, `api/handlers_payment.go` (mechanical `s.net` → `s.network()`)
- Modify: `api/server_test.go`

**Interfaces:**
- Consumes: `payment.Network` (unchanged).
- Produces: `NewServer(newState func() *payment.Network, log *slog.Logger) *Server`; `(*Server).network() *payment.Network`; `(*Server).Reset()`; route `POST /admin/reset` → `200 {"status":"reset"}`.

- [ ] **Step 1: Replace the `s.net` field with an atomic pointer + factory in `api/server.go`**

Replace the `Server` struct and `NewServer` (lines ~14–26) with:

```go
// Server holds the application state behind an atomic pointer so the whole state
// can be swapped in one step (see Reset). The state is a single payment.Network,
// the root of the whole object graph (each participant bank owns its own ledger
// and deposit register). Handlers read it via network() so a reset is observed by
// every in-flight and future request. newState builds a fresh network for the
// initial boot and for every reset.
type Server struct {
	state    atomic.Pointer[payment.Network]
	newState func() *payment.Network
	log      *slog.Logger
}

// NewServer builds a Server whose state is produced by newState. newState is
// called once now for the initial state and again on every Reset. If log is nil,
// the default slog logger is used.
func NewServer(newState func() *payment.Network, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{newState: newState, log: log}
	s.state.Store(newState())
	return s
}

// network returns the live network. Cheap, lock-free, safe for concurrent use.
func (s *Server) network() *payment.Network { return s.state.Load() }

// Reset atomically replaces the entire application state with a freshly built
// one. In-flight requests that already loaded the previous network finish against
// it consistently; new requests see the fresh state.
func (s *Server) Reset() { s.state.Store(s.newState()) }
```

Add `"sync/atomic"` to the import block.

- [ ] **Step 2: Register the admin route and update the `participant` helper in `api/server.go`**

In `Routes()`, add the admin route registration alongside the others:

```go
	s.registerAdminRoutes(mux)
```

In the `participant` helper, change `s.net.GetParticipant(pid)` to `s.network().GetParticipant(pid)`.

- [ ] **Step 3: Mechanically replace remaining `s.net` references**

Run from the repo root:

```bash
sed -i '' 's/s\.net\./s.network()./g' api/handlers_participant.go api/handlers_payment.go
```

Verify none remain:

```bash
grep -rn 's\.net\.' api/ ; echo "exit: $?"
```

Expected: no matches (grep exit 1).

- [ ] **Step 4: Create the admin handler `api/handlers_admin.go`**

```go
package api

import "net/http"

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/reset", s.handleReset)
}

// handleReset swaps the entire application state for a freshly built one and
// reports success. The request takes no body.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	s.Reset()
	s.log.Info("application state reset")
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}
```

- [ ] **Step 5: Update `api/server_test.go` to the new `NewServer` signature**

Replace `newTestServer` (lines ~18–23) with:

```go
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	newState := func() *payment.Network {
		return payment.NewNetworkWithClock(func() time.Time { return fixedTime })
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(newState, log).Routes()
}
```

- [ ] **Step 6: Add a reset behavior test in `api/server_test.go`**

Append:

`/participants` returns a JSON array, so assert on the raw body via the `do`
helper (not `doJSON`, which decodes a JSON object).

```go
func TestAdminReset(t *testing.T) {
	h := newTestServer(t)

	// emptyList reports whether the /participants body is the empty array.
	emptyList := func() bool {
		b := strings.TrimSpace(do(t, h, "GET", "/participants", "").Body.String())
		return b == "[]"
	}

	// Create a participant, confirm it's present.
	doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)
	if emptyList() {
		t.Fatal("expected one participant before reset, got empty list")
	}

	// Reset rebuilds state from the factory (empty in tests).
	doJSON(t, h, "POST", "/admin/reset", "", http.StatusOK)

	if !emptyList() {
		t.Fatal("expected empty participants after reset")
	}
}
```

This test uses `strings`; add `"strings"` to the test file's import block if it
isn't already imported.

- [ ] **Step 7: Run the api test suite**

Run: `go test ./api/ -v`
Expected: PASS, including `TestAdminReset`, `TestDepositFlow`, `TestSCTEndToEnd`, `TestErrorMapping`.

- [ ] **Step 8: Build everything and vet**

Run: `go build ./... && go vet ./...`
Expected: no output (success). `cmd/server/main.go` will fail to build here because it still calls the old `NewServer` signature — that is fixed in Task 4. If `go build ./...` fails ONLY in `cmd/server`, proceed; otherwise fix the reported package.

- [ ] **Step 9: Commit**

```bash
git add api/server.go api/handlers_admin.go api/handlers_participant.go api/handlers_payment.go api/server_test.go
git commit -m "Make api.Server state swappable and add POST /admin/reset"
```

---

### Task 4: Boot the server pre-seeded

**Files:**
- Modify: `cmd/server/main.go`

**Interfaces:**
- Consumes: `seed.Network` (Task 2), `api.NewServer(newState, log)` (Task 3).

- [ ] **Step 1: Wire the seed factory in `cmd/server/main.go`**

Replace the network/server construction (lines ~30–31):

```go
	net := payment.NewNetwork()
	srv := api.NewServer(net, log)
```

with:

```go
	srv := api.NewServer(seed.Network, log)
```

Update the imports: remove `"github.com/raphi011/ledger/payment"` if it becomes unused, and add `"github.com/raphi011/ledger/seed"`.

Update the package doc comment near the top so it reflects the new behavior — change the sentence about state to:

```go
// It builds a single in-memory payment.Network seeded with a comprehensive
// sample dataset (multiple banks, accounts, payments, clearing cycles and
// settlements) and serves it over HTTP. State lives only in memory and can be
// reset to the sample dataset at runtime via POST /admin/reset; it also resets on
// restart. This is a learning and prototyping tool, not a production service.
```

- [ ] **Step 2: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no output (success across all packages now).

- [ ] **Step 3: Smoke-test the running server**

Run (in one shell):

```bash
go run ./cmd/server &
SERVER_PID=$!
sleep 1
curl -s localhost:8080/participants | head -c 400
echo
curl -s -X POST localhost:8080/admin/reset
echo
curl -s localhost:8080/payments | head -c 200
echo
kill $SERVER_PID
```

Expected: `/participants` returns a non-empty JSON array of four banks (`Aurora Bank`, `Banca Verde`, `Nordhaven Bank`, `Crédit Soleil`); `/admin/reset` returns `{"status":"reset"}`; `/payments` returns a non-empty JSON array.

- [ ] **Step 4: Commit**

```bash
git add cmd/server/main.go
git commit -m "Boot the server pre-seeded with sample data"
```

---

### Task 5: Frontend reset button

**Files:**
- Modify: `web/src/lib/api/endpoints.ts`
- Modify: `web/src/lib/api/hooks.ts`
- Create: `web/src/components/reset-button.tsx`
- Modify: `web/src/components/app-shell.tsx`

**Interfaces:**
- Consumes: `request` from `@/lib/api/client`; `useMutation`/`useQueryClient` (already imported in hooks.ts); `ConfirmAction` from `@/components/forms/confirm-action`; `Button` from `@/components/ui/button`; `describeError` from `@/lib/api/errors`; `toast` from `sonner`.
- Produces: `resetState(): Promise<void>`; `useResetState()`; `<ResetButton />`.

- [ ] **Step 1: Add the endpoint function in `web/src/lib/api/endpoints.ts`**

Append at the end of the file:

```ts
// --- Admin ----------------------------------------------------------------

// resetState wipes the in-memory backend and reloads the built-in sample
// dataset. The request has no body and returns {status:"reset"} (ignored).
export function resetState(): Promise<void> {
  return request<void>("POST", "/admin/reset");
}
```

- [ ] **Step 2: Add the mutation hook in `web/src/lib/api/hooks.ts`**

First confirm the imports at the top of the file already include `useMutation`, `useQueryClient`, and the endpoints import. Then append at the end of the file:

```ts
// Reset the whole backend to the sample dataset, then invalidate every query so
// the UI refetches the fresh state.
export function useResetState() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: resetState,
    onSuccess: () => qc.invalidateQueries(),
  });
}
```

Ensure `resetState` is part of the existing `import { ... } from "./endpoints";` (or `"@/lib/api/endpoints"`) statement at the top of `hooks.ts`; add it to that import list if the file imports endpoints by name.

- [ ] **Step 3: Create the reset button `web/src/components/reset-button.tsx`**

```tsx
"use client";

import { RotateCcw } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { ConfirmAction } from "@/components/forms/confirm-action";
import { useResetState } from "@/lib/api/hooks";
import { describeError } from "@/lib/api/errors";

// Sidebar action: confirm, then reset the backend to the sample dataset. The
// in-memory backend has no undo, so this is a destructive confirm.
export function ResetButton() {
  const reset = useResetState();
  return (
    <div className="px-3">
      <ConfirmAction
        destructive
        title="Reset all data?"
        description="This wipes the in-memory state and reloads the built-in sample dataset (banks, accounts, payments, clearing cycles and settlements). Anything you created will be lost."
        confirmLabel="Reset data"
        pending={reset.isPending}
        onConfirm={async () => {
          try {
            await reset.mutateAsync();
            toast.success("Data reset to the sample dataset");
          } catch (e) {
            toast.error(describeError(e));
            throw e; // keep the dialog open on failure
          }
        }}
        trigger={
          <Button variant="outline" size="sm" className="w-full justify-start gap-2">
            <RotateCcw className="size-4" />
            Reset data
          </Button>
        }
      />
    </div>
  );
}
```

If `describeError` is not exported from `@/lib/api/errors`, replace `describeError(e)` with `e instanceof Error ? e.message : "Reset failed"` and drop that import.

- [ ] **Step 4: Render `ResetButton` in `web/src/components/app-shell.tsx`**

Add the import near the other component imports:

```tsx
import { ResetButton } from "./reset-button";
```

Delete the `ResetNote` function (lines ~79–85) and replace both `<ResetNote />` usages (in the desktop sidebar footer and the mobile drawer footer) with `<ResetButton />`.

- [ ] **Step 5: Typecheck, lint, build**

Run from `web/`:

```bash
npm run typecheck && npm run lint && npm run build
```

Expected: all three clean (no type errors, no lint errors, successful production build).

- [ ] **Step 6: Manual verification against a running backend**

With `go run ./cmd/server` running (repo root) and `npm run dev` running (`web/`): open http://localhost:3000, confirm the sidebar shows a "Reset data" button, click it, confirm the dialog, and verify a success toast appears and the dashboard still shows the sample banks (IDs are stable, so the selected participant survives). Create a participant, reset, and confirm it disappears.

- [ ] **Step 7: Commit**

```bash
git add web/src/lib/api/endpoints.ts web/src/lib/api/hooks.ts web/src/components/reset-button.tsx web/src/components/app-shell.tsx
git commit -m "Add reset-data button to the frontend"
```

---

## Self-Review

**Spec coverage:**
- State seam (atomic pointer + factory + accessor + Reset) → Task 3. ✓
- `seed` package + deterministic clock that goes live → Tasks 1–2. ✓
- Sample scenario (4 banks; accounts in every status + overdraft; holds active/released/captured; two-day snapshots; active/revoked mandates; payments Settled/Returned/Cleared/Accepted/Rejected; settled/closed/open cycles; 2 settlements; GL Equity/Revenue + manual tx + reversal) → Task 2 build(). ✓
- `POST /admin/reset` + boot seeded → Tasks 3–4. ✓
- Frontend button (ConfirmAction + invalidate all + toast) → Task 5. ✓
- Tests: seed invariants/determinism/clock-live → Task 2; reset over HTTP + signature update → Task 3. ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code; the one conditional fallback (describeError) gives an explicit alternative.

**Type consistency:** `newState func() *payment.Network` matches `NewServer` and `seed.Network`'s signature (`func() *payment.Network`). `network()`/`Reset()` names used consistently. Frontend `resetState`/`useResetState`/`ResetButton` names match across files. `must`/`check`/`open`/`openOverdraft`/`ref`/`fund`/`initSCT`/`initSDD`/`glShowcase` are all defined in Task 2 and used only there. Every account is referenced via `b.ref(...)` so a `PartyRef` (including its IBAN) is identical wherever an account appears — required for SDD mandate matching.

**Known assumptions to validate during execution:**
- `describeError` export in `web/src/lib/api/errors.ts` (fallback provided).
- `hooks.ts` already imports `useMutation`/`useQueryClient` and imports endpoints by name (Step 2 says to confirm/extend).
- The reserves total constant (1,120,000) equals the exact sum of funding amounts (Step 5 of Task 2 says how to reconcile).
