package payment_test

import (
	"context"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/storetest"
)

// paymentAudit reads the network's own audit trail, optionally narrowed to one
// entity.
func paymentAudit(t *testing.T, sys *testSystem, entity string) []ledger.AuditEvent {
	t.Helper()
	events, err := sys.ListAudit(context.Background(), ledger.AuditFilter{
		BookID:   ledger.NetworkBook,
		Scope:    ledger.ScopePayment,
		EntityID: entity,
	})
	assertNoError(t, err)
	return events
}

func eventTypes(events []ledger.AuditEvent) string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return strings.Join(out, " ")
}

// TestPaymentAuditCoversTheNettingFlow pins the fan-out events: closing a cycle
// records one payment.cleared per payment plus one cycle.closed, and settling it
// records one cycle.settled plus one payment.settled per payment. A cycle with
// two payments is the smallest fixture that can tell "once" from "once per
// payment" apart.
//
// # The ORDER across the cut-off is the measurement Task 15 moved
//
// payment.settled used to come BEFORE cycle.settled, because both were appended
// inside the settlement agent's one unit of work and the payments were done
// first. They are two institutions' acts now: cycle.settled is the settlement
// agent's, and each payment.settled is a payee's bank's, appended when that bank
// posts its own creditor leg on the clearing house's advice. So the settlement
// closes first and the payments follow it.
//
// A trail in which they ran the other way round would mean a payee had been paid
// before the reserves moved, which is what finality forbids and which no bank
// could have known to do.
func TestPaymentAuditCoversTheNettingFlow(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	fundAccount(t, ctx, sys, b, depositAccount(t, ctx, b, bob), 50000)

	var payments []PaymentID
	st := runCycle(t, sys, SchemeSEPACT, func() {
		p1, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		})
		assertNoError(t, err)
		p2, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Participant: b.ID, Account: bob}, Creditor: PartyRef{Participant: a.ID, Account: alice},
			CreditorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
		})
		assertNoError(t, err)
		payments = []PaymentID{p1.ID, p2.ID}
	})

	// Four events per admission, because an admission is four units of work at
	// three institutions and each writes its own. They are interleaved per bank
	// rather than grouped, because the acts run in order for one bank before the
	// next bank is admitted. See TestEachActOfAnAdmissionLeavesItsOwnAuditEvent.
	want := strings.Join([]string{
		ledger.EventParticipantAdded, // Bank A founds itself
		ledger.EventSettlementAccountOpened,
		ledger.EventMemberAdmitted,
		ledger.EventMembershipRecorded,
		ledger.EventParticipantAdded, // Bank B
		ledger.EventSettlementAccountOpened,
		ledger.EventMemberAdmitted,
		ledger.EventMembershipRecorded,
		ledger.EventCycleOpened,
		ledger.EventPaymentInitiated, // payment 1
		ledger.EventPaymentAccepted,
		ledger.EventPaymentInitiated, // payment 2
		ledger.EventPaymentAccepted,
		ledger.EventPaymentCleared, // one per payment in the cycle
		ledger.EventPaymentCleared,
		ledger.EventCycleClosed,
		ledger.EventCycleSettled,   // the settlement agent's, and it comes first
		ledger.EventPaymentSettled, // one per payment, each its own bank's
		ledger.EventPaymentSettled,
	}, " ")
	assertEqual(t, "network audit trail", eventTypes(paymentAudit(t, sys, "")), want)

	// Every event names the entity it is about, so ?entity= is usable.
	for _, pid := range payments {
		assertEqual(t, "trail for "+string(pid), eventTypes(paymentAudit(t, sys, string(pid))),
			strings.Join([]string{
				ledger.EventPaymentInitiated,
				ledger.EventPaymentAccepted,
				ledger.EventPaymentCleared,
				ledger.EventPaymentSettled,
			}, " "))
	}
	// The bank's OWN two, keyed by its own id: it founded itself, and later it
	// recorded what the scheme told it. The other two acts of its admission are
	// keyed by its BIC, because the institutions that wrote them know it by no
	// other name — see TestEachActOfAnAdmissionLeavesItsOwnAuditEvent.
	assertEqual(t, "trail for "+string(a.ID), eventTypes(paymentAudit(t, sys, string(a.ID))),
		ledger.EventParticipantAdded+" "+ledger.EventMembershipRecorded)
	assertEqual(t, "trail for the cycle", eventTypes(paymentAudit(t, sys, string(st.CycleID))),
		strings.Join([]string{ledger.EventCycleOpened, ledger.EventCycleClosed, ledger.EventCycleSettled}, " "))

	// Payment-scope events are network-scoped and carry a payload snapshot.
	for _, e := range paymentAudit(t, sys, "") {
		assertEqual(t, "book of "+e.Type, e.BookID, ledger.NetworkBook)
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
//
// A settlement that fails on an underfunded member must leave nothing behind —
// including its audit trail. An event that survived would be worse than no event
// at all: an immutable log asserting that money moved when it did not.
//
// # It was called TestAuditEventsRollBackWithTheOperation, and that name stopped
// being true
//
// The claim was a ROLLBACK, and it was the right description of the code that
// carried it: SettleCycleTx posted the central bank's netting transaction and
// then every member's mirror leg, so an underfunded member was discovered after
// the unit of work had already written, and the store had to undo it.
//
// The mirror leg is the member's own act since Task 15b.2, so SettleCycleTx
// checks each net payer's reserve ITSELF, above the netting transaction and with
// only reads behind it. Against this fixture the refusal is therefore a clean
// no-op: nothing was written, so nothing was rolled back, and the assertions
// below pass for a different reason than the one they were written for. They are
// kept because what they measure — a refused cut-off leaves no trace, which is
// what makes asking again safe once the member is funded — is still worth
// pinning, and because a settlement that started appending before it checked
// would fail them again.
//
// TestSettleCycleIsAtomic in system_test.go is the same correction on the
// balances rather than the trail, and it names the test that still carries the
// mid-flight rollback claim: TestAFailedReversalRollsBackTheWholeRejection.
func TestARefusedSettlementLeavesNoAuditTrail(t *testing.T) {
	ctx := context.Background()
	sys, cycleID := newClosedCycleWithUnderfundedMember(t)

	before := paymentAudit(t, sys, "")
	if len(before) == 0 {
		t.Fatal("fixture produced no audit events")
	}

	if _, _, err := sys.cb().SettleCycle(ctx, cycleID); err == nil {
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
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
	})
	assertNoError(t, err)

	_, err = reject(ctx, sys, p.ID, iso20022.StatusReasonDuplication, "AM05")
	assertNoError(t, err)

	assertEqual(t, "trail for the rejected payment", eventTypes(paymentAudit(t, sys, string(p.ID))),
		strings.Join([]string{
			ledger.EventPaymentInitiated,
			ledger.EventPaymentAccepted,
			ledger.EventPaymentRejected,
		}, " "))
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
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
	})
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

	m, err := sys.bank(b.ID).CreateMandate(ctx,
		PartyRef{Participant: a.ID, Account: alice},
		PartyRef{Participant: b.ID, Account: bob}, 50000)
	assertNoError(t, err)
	assertNoError(t, sys.bank(b.ID).RevokeMandate(ctx, m.ID))

	assertEqual(t, "mandate trail", eventTypes(paymentAudit(t, sys, string(m.ID))),
		strings.Join([]string{ledger.EventMandateCreated, ledger.EventMandateRevoked}, " "))
}

// TestReturnedPaymentIsAudited covers the R-transaction branch.
func TestReturnedPaymentIsAudited(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	m, err := sys.bank(b.ID).CreateMandate(ctx,
		PartyRef{Participant: a.ID, Account: alice},
		PartyRef{Participant: b.ID, Account: bob}, 0)
	assertNoError(t, err)

	var payID PaymentID
	runCycle(t, sys, SchemeSEPADD, func() {
		p, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 20000, MandateID: m.ID,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
			DebtorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
		})
		assertNoError(t, err)
		payID = p.ID
	})

	_, err = returnWholePayment(ctx, sys, payID, "AC04")
	assertNoError(t, err)

	assertEqual(t, "trail for the returned payment", eventTypes(paymentAudit(t, sys, string(payID))),
		strings.Join([]string{
			ledger.EventPaymentInitiated,
			ledger.EventPaymentAccepted,
			ledger.EventPaymentCleared,
			ledger.EventPaymentSettled,
			ledger.EventPaymentReturned,
		}, " "))
}

// TestParticipantAuditPayloadDropsLiveHandles pins that the payload of both
// events about a BANK is the stored row. Ledger and Deposit are handles over the
// store, not data — serializing them would put an empty object in an immutable
// log and suggest the row has columns it does not have.
//
// Both, because there are two of them now and they carry the same type: the bank
// founds itself and later records what the scheme told it. A check on one of the
// two would leave the other free to leak a handle.
func TestParticipantAuditPayloadDropsLiveHandles(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	p, err := storetest.Admit(ctx, sys.nets, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)

	events := paymentAudit(t, sys, string(p.ID))
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
//
// # What it is for
//
// The audit log is this system's only immutable record, and admission stopped
// being one unit of work. participant.added is written by the bank founding
// ITSELF, so its payload is a Founded bank with no settlement account numbers on
// it — honest about what founding did and silent about everything after. Without
// the other three, the settlement account numbers an admission produces would
// exist in no immutable record at all: the bank's row carries the current value
// and a row is not a log.
//
// # What each event has to be about
//
// The two BIC-keyed events are the other institutions' own records, and they are
// keyed by the BIC because that is the only identifier those institutions have —
// neither of them has ever been told this system's bank ids. The two id-keyed
// events are the bank's own row, before and after.
//
// The settlement account numbers are asserted on the events that must carry
// them, rather than only counting types: an event whose payload had lost the
// accounts would be a log entry that recorded the act and not its result, and a
// count would pass over it.
func TestEachActOfAnAdmissionLeavesItsOwnAuditEvent(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	p, err := storetest.Admit(ctx, sys.nets, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)

	assertEqual(t, "the whole admission's trail", eventTypes(paymentAudit(t, sys, "")),
		strings.Join([]string{
			ledger.EventParticipantAdded,
			ledger.EventSettlementAccountOpened,
			ledger.EventMemberAdmitted,
			ledger.EventMembershipRecorded,
		}, " "))

	// The two the other institutions wrote are keyed by the address, which is
	// the whole of what they know this bank by.
	assertEqual(t, "the trail under the bank's address", eventTypes(paymentAudit(t, sys, string(testBIC))),
		ledger.EventSettlementAccountOpened+" "+ledger.EventMemberAdmitted)

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
