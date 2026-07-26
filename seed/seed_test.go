package seed

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/mem"
)

func TestNetworkShape(t *testing.T) {
	ctx := context.Background()
	net := Network()

	if got := len(listParticipants(t, ctx, net)); got != 4 {
		t.Fatalf("participants = %d, want 4", got)
	}
	mandates, err := net.ListMandates(ctx)
	if err != nil {
		t.Fatalf("list mandates: %v", err)
	}
	if got := len(mandates); got != 3 {
		t.Fatalf("mandates = %d, want 3", got)
	}
	cycles, err := net.ListCycles(ctx)
	if err != nil {
		t.Fatalf("list cycles: %v", err)
	}
	if got := len(cycles); got != 5 {
		t.Fatalf("cycles = %d, want 5", got)
	}
	settlements, err := net.ListSettlements(ctx)
	if err != nil {
		t.Fatalf("list settlements: %v", err)
	}
	if got := len(settlements); got != 2 {
		t.Fatalf("settlements = %d, want 2", got)
	}

	// Deposit accounts: 5 + 3 + 2 + 2 = 12 across the four banks.
	total := 0
	for _, p := range listParticipants(t, ctx, net) {
		accts, err := p.Deposit.ListAccounts(ctx)
		if err != nil {
			t.Fatalf("list deposit accounts: %v", err)
		}
		total += len(accts)
	}
	if total != 12 {
		t.Fatalf("deposit accounts = %d, want 12", total)
	}
}

func TestPaymentStatusCoverage(t *testing.T) {
	ctx := context.Background()
	net := Network()
	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	if got := len(payments); got != 10 {
		t.Fatalf("total payments = %d, want 10", got)
	}
	byStatus := map[payment.PaymentStatus]int{}
	for _, p := range payments {
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
	ctx := context.Background()
	net := Network()
	seen := map[deposit.AccountStatus]bool{}
	for _, p := range listParticipants(t, ctx, net) {
		accts, err := p.Deposit.ListAccounts(ctx)
		if err != nil {
			t.Fatalf("list deposit accounts: %v", err)
		}
		for _, a := range accts {
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
	ctx := context.Background()
	net := Network()
	var sum int64
	for _, p := range listParticipants(t, ctx, net) {
		bal, err := net.ReserveBalance(ctx, p.ID)
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
	ctx := context.Background()
	a := Network()
	b := Network()

	pa, pb := listParticipants(t, ctx, a), listParticipants(t, ctx, b)
	if len(pa) != len(pb) {
		t.Fatalf("participant counts differ: %d vs %d", len(pa), len(pb))
	}
	for i := range pa {
		if pa[i].ID != pb[i].ID || pa[i].Name != pb[i].Name {
			t.Fatalf("participant %d differs: %v/%v vs %v/%v", i, pa[i].ID, pa[i].Name, pb[i].ID, pb[i].Name)
		}
	}

	xa, xb := listPayments(t, ctx, a), listPayments(t, ctx, b)
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
	ctx := context.Background()
	net := Network()
	first := listParticipants(t, ctx, net)[0]
	accts, err := first.Deposit.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list deposit accounts: %v", err)
	}
	if len(accts) == 0 {
		t.Fatal("first participant has no accounts")
	}
	ref := payment.PartyRef{Participant: first.ID, Account: accts[0].ID}

	// A mutation after build must be timestamped in real time, not at baseDate.
	m, err := net.CreateMandate(ctx, ref, ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(m.CreatedAt) > time.Minute {
		t.Fatalf("mandate CreatedAt = %v, expected ~now (clock did not go live)", m.CreatedAt)
	}
}

// listParticipants and listPayments keep the ctx/error plumbing out of the
// assertions above.
func listParticipants(t *testing.T, ctx context.Context, net *payment.Network) []*payment.Participant {
	t.Helper()
	parts, err := net.ListParticipants(ctx)
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	return parts
}

func listPayments(t *testing.T, ctx context.Context, net *payment.Network) []payment.Payment {
	t.Helper()
	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	return payments
}

// Populate must be safe to call again. The server calls it on every boot, and
// against a store that outlives the process an unconditional seed would stack a
// second copy of the scenario on top of the first.
func TestPopulateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d := New()
	store := mem.New(d.Now)
	net := payment.NewNetwork(store.Payment(), d.Now)

	if err := d.Populate(ctx, net); err != nil {
		t.Fatalf("first Populate: %v", err)
	}
	participants, payments := listParticipants(t, ctx, net), listPayments(t, ctx, net)

	if err := d.Populate(ctx, net); err != nil {
		t.Fatalf("second Populate: %v", err)
	}
	if got := len(listParticipants(t, ctx, net)); got != len(participants) {
		t.Fatalf("participants after reseeding = %d, want %d", got, len(participants))
	}
	if got := len(listPayments(t, ctx, net)); got != len(payments) {
		t.Fatalf("payments after reseeding = %d, want %d", got, len(payments))
	}
	assertClockIsLive(t, d, "after a second Populate on the same Dataset")

	// The case the idempotent skip exists for: a second process opening a store
	// that outlived the first. Its Dataset is brand new, so its clock starts
	// frozen at baseDate and Populate builds nothing — and if the skip returned
	// without releasing the clock, everything this process went on to write
	// would be timestamped 2025-09-15.
	second := New()
	secondNet := payment.NewNetwork(store.Payment(), second.Now)
	if err := second.Populate(ctx, secondNet); err != nil {
		t.Fatalf("Populate from a second process: %v", err)
	}
	if got := len(listParticipants(t, ctx, secondNet)); got != len(participants) {
		t.Fatalf("participants seen by the second process = %d, want %d", got, len(participants))
	}
	assertClockIsLive(t, second, "after an idempotent skip in a second process")

	// And the observable consequence, not just the clock reading: a row written
	// after the skip must carry a live timestamp.
	acct, err := listParticipants(t, ctx, secondNet)[0].OpenCustomerAccount(ctx, "Opened after the skip")
	if err != nil {
		t.Fatalf("open account after the skip: %v", err)
	}
	if age := time.Since(acct.CreatedAt); age > time.Minute {
		t.Fatalf("account opened after the skip is dated %v (%v ago), expected ~now", acct.CreatedAt, age)
	}
}

// assertClockIsLive checks that a Dataset's clock has been released to real
// time rather than left frozen at baseDate.
func assertClockIsLive(t *testing.T, d *Dataset, when string) {
	t.Helper()
	if age := time.Since(d.Now()); age > time.Minute {
		t.Fatalf("clock %s reads %v (%v ago), expected ~now — the seed clock never went live", when, d.Now(), age)
	}
}

// Populate recovers the builder's own must/check panic and nothing else. A nil
// dereference in payment, deposit, ledger or store/mem is a bug: flattening it
// into a seed error would return it as a 500 with the stack thrown away.
func TestRecoverBuildOnlyCatchesSeedErrors(t *testing.T) {
	if err := recoverBuild(nil); err != nil {
		t.Fatalf("recoverBuild(nil) = %v, want nil", err)
	}

	boom := errors.New("boom")
	err := recoverBuild(seedErr{boom})
	if err == nil {
		t.Fatal("recoverBuild did not convert a seedErr into an error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("recoverBuild(seedErr) = %v, want it to wrap %v", err, boom)
	}
	if !strings.HasPrefix(err.Error(), "seed: ") {
		t.Fatalf("recoverBuild(seedErr) = %q, want it prefixed with \"seed: \"", err.Error())
	}

	// Anything else keeps going, with its original value.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("recoverBuild swallowed a panic that was not the builder's")
			}
			if r != "a runtime bug" {
				t.Fatalf("re-panicked with %v, want the original value", r)
			}
		}()
		_ = recoverBuild("a runtime bug")
	}()
}

// must and check are what recoverBuild recognises, so the panic value they
// raise is part of the contract rather than an implementation detail.
func TestMustAndCheckPanicWithSeedErr(t *testing.T) {
	boom := errors.New("boom")

	cases := map[string]func(){
		"check": func() { check(boom) },
		"must":  func() { must("", boom) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s did not panic", name)
				}
				se, ok := r.(seedErr)
				if !ok {
					t.Fatalf("%s panicked with %T, want seedErr", name, r)
				}
				if !errors.Is(se, boom) {
					t.Fatalf("%s panicked with %v, want it to wrap %v", name, se, boom)
				}
			}()
			fn()
		})
	}
}

// A reset must restore the dataset, not a version of it shifted forward in
// time. Populate rewinds its clock, so the second build reproduces the first
// exactly — IDs, statuses, amounts and booking dates.
func TestPopulateAfterResetRebuildsTheSameDataset(t *testing.T) {
	ctx := context.Background()
	d := New()
	store := mem.New(d.Now)
	net := payment.NewNetwork(store.Payment(), d.Now)

	if err := d.Populate(ctx, net); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	before := listPayments(t, ctx, net)

	if err := store.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := len(listParticipants(t, ctx, net)); got != 0 {
		t.Fatalf("participants after reset = %d, want 0", got)
	}

	if err := d.Populate(ctx, net); err != nil {
		t.Fatalf("Populate after reset: %v", err)
	}
	after := listPayments(t, ctx, net)

	if len(after) != len(before) {
		t.Fatalf("payments after reset = %d, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Status != before[i].Status ||
			after[i].Amount != before[i].Amount || !after[i].BookingDate.Equal(before[i].BookingDate) {
			t.Fatalf("payment %d differs after a reset: %+v vs %+v", i, after[i], before[i])
		}
	}
}
