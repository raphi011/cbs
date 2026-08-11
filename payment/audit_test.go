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
//
// It is three reads of three books, because each institution keeps its own
// payment-scope log in its own database — a mandate is the creditor bank's, a
// cycle is the clearing house's, a settlement is the central bank's. See
// payment.Network.book.
//
// So the trail these tests are about is spread across N+2 logs, and this reads
// them all and CONCATENATES them, institution by institution: the clearing
// house, the settlement agent, then each bank, each in its own order.
//
// # There is no total order to sort into
//
// Each institution has its own database and therefore its own counter, so two
// events from two institutions carry Seq numbers that mean "third thing this
// bank did" and "third thing the clearing house did". Comparing them produces an
// order nothing in the system has; sorted anyway, the trail comes out
// interleaved by accident of how busy each institution had been.
//
// What replaces it is the only cross-institution order this system can honestly
// state: NONE. There is no global clock and no shared log, and that is the
// finding rather than a limitation of the fixture — an auditor holding three
// banks' logs and a clearing house's has exactly this problem, and answers it
// with the MESSAGES, which carry causality the counters do not.
//
// So the concatenation is a grouping, and every assertion built on it reads as
// "this institution recorded these things, in this order; that one recorded
// those". Where a test's subject IS one institution's log, it should read that
// one directly rather than the whole — see institutionAudit.
func paymentAudit(t *testing.T, sys *testSystem, entity string) []ledger.AuditEvent {
	t.Helper()
	var events []ledger.AuditEvent
	for _, r := range auditReaders(t, sys) {
		events = append(events, institutionAudit(t, r, entity)...)
	}
	return events
}

// institutionAudit is ONE institution's payment-scope log, in its own order.
//
// It is the read every assertion about ordering has to be built on now, because
// an institution's Seq is a total order within its own database and nothing
// orders two databases against each other. See paymentAudit.
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
	return institutionAudit(t, auditReader{net: sys.Network, book: ClearingHouseBook}, entity)
}

func cbAudit(t *testing.T, sys *testSystem, entity string) []ledger.AuditEvent {
	t.Helper()
	return institutionAudit(t, auditReader{net: sys.cb(), book: CentralBankBook}, entity)
}

// auditReader is one institution's log: the network to ask, and the book its own
// rows are written under.
//
// The book is carried rather than derived because Network.book is unexported and
// this is an external test package — which is the right way round for a test:
// naming the three books here means a change to that three-way answer breaks
// this file instead of silently narrowing what these assertions read.
type auditReader struct {
	net  *Network
	book ledger.BookID
}

// auditReaders is every institution whose payment-scope log is part of the
// system's trail: the clearing house, the settlement agent, and every member
// bank that exists at the moment it is called.
//
// The banks came from the clearing house's ListBanks, which is a read that did
// not survive the store split — a clearing house holds no banks table. The
// roster cannot replace it either: a FOUNDED bank has no roster entry and does
// have a log, and TestFoundingABankIsAudited is about exactly that bank.
//
// What replaces it is Stores.Banks — every bank whose DATABASE exists, which
// includes the founded and unadmitted one. That is the composition root's
// question rather than an institution's, and a test fixture assembling the whole
// system's trail is the composition root; no institution has this list and none
// should.
func auditReaders(t *testing.T, sys *testSystem) []auditReader {
	t.Helper()
	bics, err := sys.stores.Banks(context.Background())
	assertNoError(t, err)
	out := []auditReader{
		{net: sys.Network, book: ClearingHouseBook},
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
//
// storetest.Admit subscribes to the routing directory after admitting, which is
// a fifth act and no part of an admission; the event it appends is keyed by the
// subscribing bank's own BIC, so it answers the same entity filter as the two
// events about that bank's row. A case that listed it would be asserting how the
// helper is composed rather than what the acts under test do. The cases that ARE
// about a refresh assert on it directly.
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
// records one payment.cleared per payment plus one cycle.closed, and settling it
// records one cycle.settled plus one payment.settled per payment. A cycle with
// two payments is the smallest fixture that can tell "once" from "once per
// payment" apart.
//
// # The ORDER across the cut-off is out of reach
//
// cycle.settled is the settlement agent's, and each payment.settled is a payee's
// bank's, appended when that bank posts its own creditor leg on the clearing
// house's advice. They are two institutions' acts in two databases, so there is
// no merged list to read an order off.
//
// There is no list to read it off now. The two events are in two DATABASES with
// two counters, so nothing here can order them — see paymentAudit. The claim
// they carried (a payee is not paid before the reserves move) is a claim about
// FILES and is measured where the files are, in cmd/server; what this test
// measures is what each institution's own log contains and in what order, which
// is the strongest statement four separate logs support.
//
// # So it asserts four trails, and their differences are the subject
//
// A settlement agent that has never heard of a payment, a clearing house that
// never sees three of an admission's four acts, and two banks whose logs are the
// same shape as each other and say nothing about each other. Each of those is a
// separation the one shared log could not have shown.
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
	//
	// The clearing house's is the only one that sees the flow as a flow — it
	// admits both members, opens and closes the cut-off and takes each payment
	// into it — and even it does not see an admission's other three acts.
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
	// granted, and it took part in both payments — as the submitter of one and
	// the receiver of the other. TWO payment.initiated per bank, because a bank's
	// log opens with its own initiation for every payment it holds a row for
	// (SubmitPaymentTx and AcceptInboundTx both append one); the distinction
	// between the two roles is not in the event and is not meant to be.
	//
	// No cycle and no settlement. A member is told the outcome of a cut-off and
	// posts against it; it does not run one.
	//
	// The directory refreshes this fixture's admissions leave behind are filtered
	// out — see ofType. They are neither an admission's act nor a payment's, and a
	// list that included them would be asserting how storetest.Admit is composed.
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
	// recorded what the scheme told it. The other two acts of its admission are
	// keyed by its BIC — and a bank's id IS its BIC, so what separates them is the
	// LOG THEY ARE IN. See TestEachActOfAnAdmissionLeavesItsOwnAuditEvent.
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
// The mirror leg is the member's own act, so SettleCycleTx checks each net
// payer's reserve ITSELF, above the netting transaction and with only reads
// behind it. Against this fixture the refusal is therefore a clean no-op:
// nothing was written, so nothing was rolled back. What the assertions below
// measure — a refused cut-off leaves no trace, which is what makes asking again
// safe once the member is funded — is still worth pinning, and a settlement that
// started appending before it checked would fail them.
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
//
// Three trails, because a rejection is three institutions' acts and each records
// its own: the clearing house decides, and each bank records the decision on its
// own copy — the payer's giving the money back. See RejectAtBankTx, and the
// reject helper, which plays all three.
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
// events about a BANK is the stored row. Ledger and Deposit are handles over the
// store, not data — serializing them would put an empty object in an immutable
// log and suggest the row has columns it does not have.
//
// Both, because there are two of them now and they carry the same type: the bank
// founds itself and later records what the scheme told it. A check on one of the
// two would leave the other free to leak a handle.
//
// Both are in the BANK's own log, which is what selects them: a bank's id is its
// BIC, so the two events the other institutions wrote about the same address
// answer the same entity filter and are a different type carrying a different
// payload. See TestEachActOfAnAdmissionLeavesItsOwnAuditEvent.
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
//
// # What it is for
//
// The audit log is this system's only immutable record, and admission stopped
// being one unit of work. participant.added is written by the bank founding
// ITSELF, so its payload is a bank with no settlement account numbers on
// it — honest about what founding did and silent about everything after. Without
// the other three, the settlement account numbers an admission produces would
// exist in no immutable record at all: the bank's row carries the current value
// and a row is not a log.
//
// # What each event has to be about
//
// The two the other institutions wrote are keyed by the BIC because that is the
// only identifier those institutions have — neither of them has ever been told
// this system's bank ids. The bank's own two are keyed by its id, and they are
// its own row before and after.
//
// # The log each event is in is what separates them
//
// A bank's ParticipantID IS its BIC (see AsBank), so all four events are keyed by
// one string and filtering by entity cannot tell the institutions apart. What
// can is WHOSE DATABASE each event is in: the bank's own log holds the two it
// wrote, the settlement agent's holds the account it opened, the clearing
// house's holds the roster entry it made. That is a stronger separation than a
// key — a key is a convention and this is a boundary — and it is why the four
// assertions below are four reads rather than one.
//
// There is no ORDER between them. Each institution's log orders its own acts and
// nothing orders one against another (see paymentAudit), so "participant.added,
// then settlement_account.opened, then member.admitted, then
// membership.recorded" is not a sequence this system can state. What survives is
// that each act happened, in the log of the institution that performed it, and
// that each carries its result.
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

	// The bank's own two, in its own log and in its own order: it founded itself
	// and later recorded what the scheme told it. The subscription that follows in
	// this fixture is a fifth act and no part of an admission (storetest.Admit),
	// so it is dropped rather than listed.
	assertEqual(t, "the bank's own trail",
		eventTypes(withoutRefreshes(bankAudit(t, sys, testBIC, ""))),
		ledger.EventParticipantAdded+" "+ledger.EventMembershipRecorded)
	// The settlement agent allocated a bank code and opened the account. Two
	// events because they are two registers: a reserve account is at a central
	// bank and a Bankleitzahl comes from a registry, and this institution is
	// standing in for both.
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
