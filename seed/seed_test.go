package seed

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/payment"
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
	payments := net.ListPayments()
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
