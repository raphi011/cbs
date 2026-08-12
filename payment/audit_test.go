package payment_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/storetest"
)

// paymentAudit reads the payment-scope audit trail of the WHOLE SYSTEM,
// optionally narrowed to one entity.
func paymentAudit(t *testing.T, sys *testSystem, entity string) []ledger.AuditEvent {
	t.Helper()
	var events []ledger.AuditEvent
	for _, r := range auditReaders(t, sys) {
		events = append(events, institutionAudit(t, r, entity)...)
	}
	return events
}

// institutionAudit is ONE institution's payment-scope log, in its own order.
func institutionAudit(t *testing.T, r auditReader, entity string) []ledger.AuditEvent {
	t.Helper()
	got, err := r.net.ListAudit(context.Background(), ledger.AuditFilter{
		BookID:   r.book,
		Scope:    ledger.ScopePayment,
		EntityID: entity,
	})
	assertNoError(t, err)
	return got
}

// bankAudit is one member bank's own log, by address.
func bankAudit(t *testing.T, sys *testSystem, bic iso20022.BIC, entity string) []ledger.AuditEvent {
	t.Helper()
	net := sys.bank(bic)
	b, err := net.GetBank(context.Background(), ParticipantID(bic))
	assertNoError(t, err)
	return institutionAudit(t, auditReader{net: net, book: b.BookID}, entity)
}

// csmAudit and cbAudit are the two named institutions' own logs.
func csmAudit(t *testing.T, sys *testSystem, entity string) []ledger.AuditEvent {
	t.Helper()
	return institutionAudit(t, auditReader{net: sys.ClearingHouseNetwork, book: ClearingHouseBook}, entity)
}

func cbAudit(t *testing.T, sys *testSystem, entity string) []ledger.AuditEvent {
	t.Helper()
	return institutionAudit(t, auditReader{net: sys.cb(), book: CentralBankBook}, entity)
}

// auditReader is one institution's log: the network to ask, and the book its
// own rows are written under.
type auditReader struct {
	// net is an interface and not one of the three institution types because
	// this fixture asks all three the same question. Reading one's own trail is
	// the rare act that is every institution's, so it is the only method here.
	net interface {
		ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error)
	}
	book ledger.BookID
}

// auditReaders is every institution whose payment-scope log is part of the
// system's trail: the clearing house, the settlement agent, and every member
// bank that exists at the moment it is called.
func auditReaders(t *testing.T, sys *testSystem) []auditReader {
	t.Helper()
	bics, err := sys.stores.Banks(context.Background())
	assertNoError(t, err)
	out := []auditReader{
		{net: sys.ClearingHouseNetwork, book: ClearingHouseBook},
		{net: sys.cb(), book: CentralBankBook},
	}
	for _, bic := range bics {
		net := sys.bank(bic)
		b, err := net.GetBank(context.Background(), ParticipantID(bic))
		assertNoError(t, err)
		out = append(out, auditReader{net: net, book: b.BookID})
	}
	return out
}

// auditBooks is just the books, for the membership check on each event.
func auditBooks(t *testing.T, sys *testSystem) []ledger.BookID {
	t.Helper()
	var out []ledger.BookID
	for _, r := range auditReaders(t, sys) {
		out = append(out, r.book)
	}
	return out
}

// withoutRefreshes drops the directory refreshes a fixture's admissions left
// behind.
func withoutRefreshes(events []ledger.AuditEvent) []ledger.AuditEvent {
	out := make([]ledger.AuditEvent, 0, len(events))
	for _, e := range events {
		if e.Type != ledger.EventDirectoryRefreshed {
			out = append(out, e)
		}
	}
	return out
}

func eventTypes(events []ledger.AuditEvent) string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return strings.Join(out, " ")
}

// TestPaymentAuditCoversTheNettingFlow pins the fan-out events: closing a cycle
// records one payment.cleared per payment plus one cycle.closed, and settling
// it records one cycle.settled plus one payment.settled per payment.
func TestPaymentAuditCoversTheNettingFlow(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	fundAccount(t, ctx, sys, b, depositAccount(t, ctx, b, bob), 50000)

	var payments []PaymentID
	st := runCycle(t, sys, SchemeSEPACT, func() {
		p1, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
		p2, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Account: bob}, Creditor: PartyRef{Account: alice},
			CreditorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
			DebtorDetails:   PartyDetails{Agent: b.BIC}})
		assertNoError(t, err)
		payments = []PaymentID{p1.ID, p2.ID}
	})

	// FOUR trails, one per institution, and each is asserted whole and in its own
	// order. There is no fifth list that is all of them: see paymentAudit on why
	// the concatenation is a grouping and not a sequence.
	assertEqual(t, "the clearing house's trail", eventTypes(csmAudit(t, sys, "")),
		strings.Join([]string{
			ledger.EventMemberAdmitted, // Bank A into the roster
			ledger.EventMemberAdmitted, // Bank B
			ledger.EventCycleOpened,
			ledger.EventPaymentInitiated, // payment 1, relayed to it
			ledger.EventPaymentAccepted,
			ledger.EventPaymentInitiated, // payment 2
			ledger.EventPaymentAccepted,
			ledger.EventPaymentCleared, // one per payment in the cycle
			ledger.EventPaymentCleared,
			ledger.EventCycleClosed,
			ledger.EventPaymentSettled, // one per payment, on its advice
			ledger.EventPaymentSettled,
		}, " "))

	// The settlement agent's is five lines long: it allocated a bank code and
	// opened an account for each member, and it discharged one cut-off. It has
	// never heard of a payment, which is what netting means.
	assertEqual(t, "the settlement agent's trail", eventTypes(cbAudit(t, sys, "")),
		strings.Join([]string{
			ledger.EventBankCodeAllocated,
			ledger.EventSettlementAccountOpened,
			ledger.EventBankCodeAllocated,
			ledger.EventSettlementAccountOpened,
			ledger.EventCycleSettled,
		}, " "))

	// Each bank's is the same shape as the other's, and it is the shape of what a
	// bank actually did: it founded itself, it recorded the membership it was
	// granted, and it took part in both payments — as the submitter of one and the
	// receiver of the other.
	eachBank := strings.Join([]string{
		ledger.EventParticipantAdded,
		ledger.EventMembershipRecorded,
		ledger.EventPaymentInitiated,
		ledger.EventPaymentInitiated,
		ledger.EventPaymentSettled,
		ledger.EventPaymentSettled,
	}, " ")
	assertEqual(t, "Bank A's trail", eventTypes(withoutRefreshes(bankAudit(t, sys, a.BIC, ""))), eachBank)
	assertEqual(t, "Bank B's trail", eventTypes(withoutRefreshes(bankAudit(t, sys, b.BIC, ""))), eachBank)

	// Every event names the entity it is about, so ?entity= is usable — and the
	// answer differs by institution, which is the point. The clearing house holds
	// a payment's whole lifecycle; a bank holds the two moments it acted in.
	for _, pid := range payments {
		assertEqual(t, "the clearing house on "+string(pid), eventTypes(csmAudit(t, sys, string(pid))),
			strings.Join([]string{
				ledger.EventPaymentInitiated,
				ledger.EventPaymentAccepted,
				ledger.EventPaymentCleared,
				ledger.EventPaymentSettled,
			}, " "))
		bothEnds := ledger.EventPaymentInitiated + " " + ledger.EventPaymentSettled
		assertEqual(t, "Bank A on "+string(pid), eventTypes(bankAudit(t, sys, a.BIC, string(pid))), bothEnds)
		assertEqual(t, "Bank B on "+string(pid), eventTypes(bankAudit(t, sys, b.BIC, string(pid))), bothEnds)
	}

	// A bank's own two, keyed by its own id: it founded itself, and later it
	// recorded what the scheme told it.
	assertEqual(t, "Bank A's own trail for itself",
		eventTypes(withoutRefreshes(bankAudit(t, sys, a.BIC, string(a.ID)))),
		ledger.EventParticipantAdded+" "+ledger.EventMembershipRecorded)
	assertEqual(t, "the clearing house on Bank A", eventTypes(csmAudit(t, sys, string(a.ID))),
		ledger.EventMemberAdmitted)
	assertEqual(t, "the settlement agent on Bank A", eventTypes(cbAudit(t, sys, string(a.ID))),
		ledger.EventBankCodeAllocated+" "+ledger.EventSettlementAccountOpened)
	// And Bank B's log says nothing about Bank A at all, which is the claim the
	// one shared log could not make.
	assertEqual(t, "Bank B on Bank A", eventTypes(bankAudit(t, sys, b.BIC, string(a.ID))), "")

	assertEqual(t, "the clearing house on the cycle", eventTypes(csmAudit(t, sys, string(st.CycleID))),
		ledger.EventCycleOpened+" "+ledger.EventCycleClosed)
	assertEqual(t, "the settlement agent on the cycle", eventTypes(cbAudit(t, sys, string(st.CycleID))),
		ledger.EventCycleSettled)

	// Payment-scope events carry a payload snapshot, and each is written under the
	// book of the institution whose act it was — one of the three, never a shared
	// one, because there is no shared book left. See paymentAudit.
	books := auditBooks(t, sys)
	for _, e := range paymentAudit(t, sys, "") {
		if !slices.Contains(books, e.BookID) {
			t.Fatalf("%s is logged under %q, which is no institution's book", e.Type, e.BookID)
		}
		assertEqual(t, "scope of "+e.Type, e.Scope, ledger.ScopePayment)
		if len(e.Payload) == 0 {
			t.Fatalf("%s carries no payload", e.Type)
		}
		if e.Seq == 0 {
			t.Fatalf("%s carries no seq", e.Type)
		}
	}

	// A bank's own books keep their own logs; the network's trail must not
	// appear in them, and theirs must not appear in the network's.
	bankTrail, err := a.Ledger.GetAuditLog(ctx)
	assertNoError(t, err)
	if len(bankTrail) == 0 {
		t.Fatal("bank A has no ledger-scope audit trail")
	}
	for _, e := range bankTrail {
		if e.Scope == ledger.ScopePayment {
			t.Fatalf("payment-scope event %s leaked into bank A's ledger log", e.Type)
		}
	}
}

// TestARefusedSettlementLeavesNoAuditTrail is why the events are written on the
// operation's own transaction rather than through a wrapper that opens its own.
func TestARefusedSettlementLeavesNoAuditTrail(t *testing.T) {
	ctx := context.Background()
	sys, cycleID := newClosedCycleWithUnderfundedMember(t)

	before := paymentAudit(t, sys, "")
	if len(before) == 0 {
		t.Fatal("fixture produced no audit events")
	}

	if _, _, err := sys.settleCycle(ctx, cycleID); err == nil {
		t.Fatal("SettleCycle succeeded, want failure on the underfunded member")
	}

	after := paymentAudit(t, sys, "")
	assertEqual(t, "audit events after a failed settlement", len(after), len(before))
	for _, e := range after {
		if e.Type == ledger.EventCycleSettled || e.Type == ledger.EventPaymentSettled {
			t.Fatalf("%s for %s survived a refused settlement", e.Type, e.EntityID)
		}
	}

	// The successful part of the fixture is still on record, so the refusal cost
	// the failed unit of work and nothing else.
	assertEqual(t, "cycle trail", eventTypes(paymentAudit(t, sys, string(cycleID))),
		strings.Join([]string{ledger.EventCycleOpened, ledger.EventCycleClosed}, " "))
}

// TestRejectedPaymentIsAudited covers the branch the happy path never reaches,
// and pins that a rejected payment's own initiation events survive: the
// rejection is a later unit of work, not a rollback of the initiation.
func TestRejectedPaymentIsAudited(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 5000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)

	_, err = reject(ctx, sys, p.ID, iso20022.StatusReasonDuplication, "AM05")
	assertNoError(t, err)

	assertEqual(t, "the clearing house on the rejected payment", eventTypes(csmAudit(t, sys, string(p.ID))),
		strings.Join([]string{
			ledger.EventPaymentInitiated,
			ledger.EventPaymentAccepted,
			ledger.EventPaymentRejected,
		}, " "))
	// Each bank's own copy: it took the payment on, and it was told the payment
	// died. No payment.accepted — that is the clearing house's act and no bank
	// records another institution's.
	bothBanks := ledger.EventPaymentInitiated + " " + ledger.EventPaymentRejected
	assertEqual(t, "the payer's bank on the rejected payment",
		eventTypes(bankAudit(t, sys, a.BIC, string(p.ID))), bothBanks)
	assertEqual(t, "the payee's bank on the rejected payment",
		eventTypes(bankAudit(t, sys, b.BIC, string(p.ID))), bothBanks)
}

// TestFailedInitiationLeavesNoAuditTrail is the mirror image: an instruction
// that never became a payment must leave no record claiming it did.
func TestFailedInitiationLeavesNoAuditTrail(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	before := len(paymentAudit(t, sys, ""))
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 999999, // more than Alice has
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	if err == nil {
		t.Fatal("initiation succeeded, want an insufficient-funds failure")
	}

	after := paymentAudit(t, sys, "")
	assertEqual(t, "audit events after a failed initiation", len(after), before)
	for _, e := range after {
		if e.Type == ledger.EventPaymentInitiated || e.Type == ledger.EventPaymentAccepted {
			t.Fatalf("%s survived a rejected instruction", e.Type)
		}
	}
}

// TestMandateEventsAreAudited covers the two mandate events.
func TestMandateEventsAreAudited(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	m, err := sys.bank(b.BIC).CreateMandate(ctx,
		a.BIC,
		PartyRef{Account: alice},
		PartyRef{Account: bob}, 50000)
	assertNoError(t, err)
	assertNoError(t, sys.bank(b.BIC).RevokeMandate(ctx, m.ID))

	assertEqual(t, "mandate trail", eventTypes(paymentAudit(t, sys, string(m.ID))),
		strings.Join([]string{ledger.EventMandateCreated, ledger.EventMandateRevoked}, " "))
}

// TestReturnedPaymentIsAudited covers the R-transaction branch.
func TestReturnedPaymentIsAudited(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	m, err := sys.bank(b.BIC).CreateMandate(ctx,
		a.BIC,
		PartyRef{Account: alice},
		PartyRef{Account: bob}, 0)
	assertNoError(t, err)

	var payID PaymentID
	runCycle(t, sys, SchemeSEPADD, func() {
		p, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 20000, MandateID: m.ID,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertNoError(t, err)
		payID = p.ID
	})

	_, err = returnWholePayment(ctx, sys, payID, "AC04")
	assertNoError(t, err)

	assertEqual(t, "the clearing house on the returned payment", eventTypes(csmAudit(t, sys, string(payID))),
		strings.Join([]string{
			ledger.EventPaymentInitiated,
			ledger.EventPaymentAccepted,
			ledger.EventPaymentCleared,
			ledger.EventPaymentSettled,
			ledger.EventPaymentReturned,
		}, " "))
	// Each bank recorded the payment and its settlement, and each recorded the
	// return: a return is posted at both ends, unlike a rejection's refund.
	bothBanks := strings.Join([]string{
		ledger.EventPaymentInitiated,
		ledger.EventPaymentSettled,
		ledger.EventPaymentReturned,
	}, " ")
	assertEqual(t, "the payer's bank on the returned payment",
		eventTypes(bankAudit(t, sys, a.BIC, string(payID))), bothBanks)
	assertEqual(t, "the payee's bank on the returned payment",
		eventTypes(bankAudit(t, sys, b.BIC, string(payID))), bothBanks)
}

// TestParticipantAuditPayloadDropsLiveHandles pins that the payload of both
// events about a BANK is the stored row.
func TestParticipantAuditPayloadDropsLiveHandles(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	p, err := storetest.Admit(ctx, sys.nets, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)

	// The directory refresh is in this log too, and it is dropped rather than
	// asserted: its payload is a snapshot of other institutions rather than this
	// bank's own row, and what this case is about is the two events whose payload
	// IS the row.
	events := withoutRefreshes(bankAudit(t, sys, testBIC, string(p.ID)))
	assertEqual(t, "events about the bank's own row", eventTypes(events),
		ledger.EventParticipantAdded+" "+ledger.EventMembershipRecorded)

	for _, e := range events {
		payload := string(e.Payload)
		for _, field := range []string{`"Ledger"`, `"Deposit"`} {
			if strings.Contains(payload, field) {
				t.Fatalf("%s payload carries %s: %s", e.Type, field, payload)
			}
		}
		if !strings.Contains(payload, `"BookID"`) {
			t.Fatalf("%s payload is missing BookID: %s", e.Type, payload)
		}
	}
}

// TestEachActOfAnAdmissionLeavesItsOwnAuditEvent is carried requirement 2, made
// falsifiable.
func TestEachActOfAnAdmissionLeavesItsOwnAuditEvent(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	p, err := storetest.Admit(ctx, sys.nets, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)

	// The bank's own two, in its own log and in its own order: it founded itself
	// and later recorded what the scheme told it.
	assertEqual(t, "the bank's own trail",
		eventTypes(withoutRefreshes(bankAudit(t, sys, testBIC, ""))),
		ledger.EventParticipantAdded+" "+ledger.EventMembershipRecorded)
	// The settlement agent allocated a bank code and opened the account.
	assertEqual(t, "the settlement agent's trail", eventTypes(cbAudit(t, sys, "")),
		ledger.EventBankCodeAllocated+" "+ledger.EventSettlementAccountOpened)
	// The clearing house put the address in the roster.
	assertEqual(t, "the clearing house's trail", eventTypes(csmAudit(t, sys, "")),
		ledger.EventMemberAdmitted)

	// And the settlement account number is in the log, twice: once as the
	// account the servicer opened, once as the reference the bank was told.
	settlement := string(p.Assets["EUR"].Settlement)
	if settlement == "" {
		t.Fatal("the admitted bank has no settlement reference; this test is measuring the wrong thing")
	}
	for _, e := range paymentAudit(t, sys, "") {
		switch e.Type {
		case ledger.EventSettlementAccountOpened, ledger.EventMembershipRecorded:
			if !strings.Contains(string(e.Payload), settlement) {
				t.Errorf("%s does not name the settlement account %s: %s", e.Type, settlement, e.Payload)
			}
		case ledger.EventParticipantAdded:
			if strings.Contains(string(e.Payload), settlement) {
				t.Errorf("%s names a settlement account; founding happens before one exists: %s", e.Type, e.Payload)
			}
		}
	}
}
