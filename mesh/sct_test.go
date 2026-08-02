package mesh

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The happy path, and the shape of every test in this package: submit, drain,
// assert. No sleeps, no Eventually, no retry — if any of those is needed, the
// Drain design has failed and that is the bug.
func TestCreditTransferReachesAcceptedThroughTheCSM(t *testing.T) {
	h := newMeshHarness(t)

	p := h.submitCreditTransfer(t)
	if p.Status != payment.Initiated {
		t.Fatalf("submit returned %v, want Initiated — the creditor's bank has not answered yet", p.Status)
	}

	h.drain(t)

	got := h.payment(t, p.ID)
	if got.Status != payment.Accepted {
		t.Fatalf("after draining, status = %v, want Accepted", got.Status)
	}
	if got.CycleID == "" {
		t.Error("accepted payment is in no cycle; the CSM adds it on ACCP")
	}
}

// The chain is four messages, and naming them is the point of the package: the
// payer's bank tells the clearing house, the clearing house tells the payee's
// bank, the payee's bank answers, and the clearing house answers the payer's
// bank. Nothing here is a function call across a boundary.
//
// It is asserted as a count and a route rather than as an exact byte sequence,
// because the identifiers each hop assigns are the sender's own and pinning them
// would be pinning the id scheme, not the flow.
func TestTheCreditTransferChainIsFourMessages(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)

	want := []struct {
		from, to iso20022.BIC
		msgDef   string
	}{
		{h.debtorBIC, h.cfg.ClearingHouseBIC, "pacs.008.001.08"},
		{h.cfg.ClearingHouseBIC, h.creditorBIC, "pacs.008.001.08"},
		{h.creditorBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"},
		{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.002.001.10"},
	}
	h.mu.Lock()
	seen := append([]tappedMessage(nil), h.seen...)
	h.mu.Unlock()

	if len(seen) != len(want) {
		t.Fatalf("the mesh carried %d messages, want %d", len(seen), len(want))
	}
	for i, w := range want {
		env, err := iso20022.Unmarshal(seen[i].raw)
		if err != nil {
			t.Fatalf("message %d does not parse: %v", i, err)
		}
		if seen[i].from != w.from || seen[i].to != w.to || env.AppHdr.MsgDefIdr != w.msgDef {
			t.Errorf("hop %d is %s -> %s (%s), want %s -> %s (%s)",
				i, seen[i].from, seen[i].to, env.AppHdr.MsgDefIdr, w.from, w.to, w.msgDef)
		}
	}
}

func TestCreditTransferToAnUnknownAccountComesBackAsAC01(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransferTo(t, unknownIBAN)
	h.drain(t)

	got := h.payment(t, p.ID)
	if got.Status != payment.Rejected {
		t.Fatalf("status = %v, want Rejected", got.Status)
	}
	if got.RejectCode != iso20022.StatusReasonIncorrectAccountNumber {
		t.Errorf("reject code = %q, want AC01", got.RejectCode)
	}
	// And the money came back. A rejection that left the payer short would be
	// worse than no rejection at all.
	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("clearing suspense = %d after a rejection, want 0", bal)
	}
}

// The rejection reaches the payer's bank as a message, not as a return value.
// It is the pacs.002 that makes the money come back, so the payer's bank has to
// receive one carrying the code.
func TestARejectedCreditTransferIsAnsweredToThePayersBank(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransferTo(t, unknownIBAN)
	h.drain(t)

	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonIncorrectAccountNumber)
}

func TestCreditTransferWithNoOpenCycleIsTM01(t *testing.T) {
	h := newMeshHarnessWithNoOpenCycle(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	got := h.payment(t, p.ID)
	if got.RejectCode != iso20022.StatusReasonInvalidCutOffTime {
		t.Errorf("reject code = %q, want TM01 — no cycle open IS an invalid cut-off time", got.RejectCode)
	}
	// The refusal is the clearing house's, and it comes after the payee's bank
	// has already said yes: no bank refuses its own customer's instruction
	// because the CSM has no window open. The payer still gets their money back.
	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("clearing suspense = %d after a TM01, want 0", bal)
	}
}

// FF01 must be reachable AND answerable. An unparseable envelope hides its own
// header, but the queue carries the sender beside the bytes, so the receiver
// can still reply.
func TestAMalformedEnvelopeIsAnsweredWithFF01(t *testing.T) {
	h := newMeshHarness(t)
	h.injectRaw(t, h.debtorBIC, h.cfg.ClearingHouseBIC, []byte("<Envelope><nonsense/>"))
	h.drain(t)
	// The CSM could not parse it, so there is no payment to reject; what it
	// can do is tell the sender the file was invalid. Assert the debtor bank
	// received a pacs.002 carrying FF01.
	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonInvalidFileFormat)
}

// Submit runs the submitting bank's half synchronously, so a submission that
// fails inside its unit of work fails at the caller — and, because the send
// happens after the commit, it sends nothing.
func TestARolledBackSubmitSendsNothing(t *testing.T) {
	h := newMeshHarness(t)
	// A submission that fails inside its unit of work — an amount of zero.
	req := h.creditTransferRequest(t)
	req.Amount = 0
	if _, err := h.mesh.Submit(context.Background(), req); err == nil {
		t.Fatal("Submit accepted a zero amount")
	}
	h.drain(t)
	if got := h.messagesSeen(); got != 0 {
		t.Fatalf("a rolled-back submission sent %d messages, want 0", got)
	}
}

// A message type an actor has no handler for is a dead letter and not a shrug.
//
// pacs.004 is a return, which is Task 13's: a bank that received one today would
// have nothing to do with it, and swallowing it would make the missing half look
// like a working one. The same argument covers pacs.009 (settlement, Task 12) —
// this asserts the shape once rather than twice.
func TestAMessageAnActorHasNoHandlerForIsADeadLetter(t *testing.T) {
	h := newMeshHarness(t)
	env, err := h.net.ReturnMessage(h.submitCreditTransfer(t), iso20022.ReturnReasonNotSpecifiedAgentGenerated, "no reason",
		payment.MessageContext{From: h.cfg.ClearingHouseBIC, To: h.debtorBIC, MsgID: "rtn-1", Now: testTime})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	if err := h.mesh.send(h.cfg.ClearingHouseBIC, h.debtorBIC, env); err != nil {
		t.Fatalf("send: %v", err)
	}

	err = h.drainErr(t)
	if err == nil {
		t.Fatal("Drain was clean; the bank swallowed a message it has no handler for")
	}
	if !strings.Contains(err.Error(), "pacs.004") {
		t.Errorf("dead letter %q does not name the message the bank could not handle", err)
	}
}

// A status report about a payment whose payer banks elsewhere is refused.
//
// ReverseDebtorLegTx posts in the book of the payment's OWN debtor bank, whoever
// runs it — so a bank that acted on a misrouted rejection would reverse a debit
// in another bank's ledger. Nothing in the flow produces one, because the
// clearing house addresses a status to the payment's debtor bank and no other;
// this is the guard that makes that a property of the receiver too, rather than
// only of the router.
//
// The payment it names really is Rejected, so the other guard in that handler —
// a bank reverses only what this network's record calls rejected — cannot be
// what refuses it. See TestABankRefusesToReverseAPaymentThatIsNotRejected.
func TestABankRefusesAStatusAboutAnotherBanksPayment(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransferTo(t, unknownIBAN)
	h.drain(t)
	if got := h.payment(t, p.ID); got.Status != payment.Rejected {
		t.Fatalf("the fixture payment is %v, want Rejected", got.Status)
	}
	h.rec.reset()

	// The same rejection the clearing house sent the payer's bank, sent to the
	// payee's bank instead.
	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: "orig-1", MsgDefIdr: "pacs.008.001.08"},
		[]payment.TransactionStatusReport{{
			TxID:   string(p.ID),
			Status: iso20022.TransactionStatusRejected,
			Code:   iso20022.StatusReasonIncorrectAccountNumber,
			Text:   "misrouted",
		}},
		payment.MessageContext{From: h.cfg.ClearingHouseBIC, To: h.creditorBIC, MsgID: "sts-x", Now: testTime})
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	if err := h.mesh.send(h.cfg.ClearingHouseBIC, h.creditorBIC, env); err != nil {
		t.Fatalf("send: %v", err)
	}

	err = h.drainErr(t)
	if err == nil {
		t.Fatal("Drain was clean; the payee's bank acted on a rejection of somebody else's customer's debit")
	}
	// Named, and not merely non-nil. Without the guard the reversal is attempted
	// and fails on the ledger's own refusal to reverse a transaction twice, so a
	// test that asked only whether the drain was dirty would pass on a handler
	// that had already reached into the payer's book to find out.
	if !strings.Contains(err.Error(), string(h.creditorBIC)) || !strings.Contains(err.Error(), string(h.debtorBIC)) {
		t.Errorf("dead letter %q does not say which bank refused it, and whose payment it was about", err)
	}
	// And it reached nobody's ledger while refusing.
	assertBooksTouched(t, "the payee's bank, refusing a misrouted rejection",
		h.booksTouchedBy(h.creditorBIC), nil)
}

// A bank reverses only a payment this network's record calls Rejected.
//
// ReverseDebtorLegTx does not load the payment and does not look at its status —
// its own doc says so, and says the caller establishes the decision. In the mesh
// the caller is this handler, and a pacs.002 is not on its own a decision: a
// rejection naming a payment that is on its way to settlement would reverse a
// live debit, taking the money back off a payee who is going to be paid anyway.
func TestABankRefusesToReverseAPaymentThatIsNotRejected(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: "orig-1", MsgDefIdr: "pacs.008.001.08"},
		[]payment.TransactionStatusReport{{
			TxID:   string(p.ID),
			Status: iso20022.TransactionStatusRejected,
			Code:   iso20022.StatusReasonIncorrectAccountNumber,
			Text:   "not a decision this network made",
		}},
		payment.MessageContext{From: h.cfg.ClearingHouseBIC, To: h.debtorBIC, MsgID: "sts-y", Now: testTime})
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	if err := h.mesh.send(h.cfg.ClearingHouseBIC, h.debtorBIC, env); err != nil {
		t.Fatalf("send: %v", err)
	}

	if err := h.drainErr(t); err == nil {
		t.Fatal("Drain was clean; the payer's bank reversed the debit of a payment that is still Accepted")
	}
	if bal := h.suspense(t, h.debtorPID); bal != harnessAmount {
		t.Errorf("payer's clearing suspense = %d, want %d — the debit was reversed anyway", bal, harnessAmount)
	}
}

// A second copy of a status the clearing house has already acted on is dead-
// lettered, not answered.
//
// This is reachable: a queue redelivers, and by the time the duplicate arrives
// the payment is Accepted, so AcceptAtCSMTx answers ErrInvalidStateTransition.
// That sentinel is classified in payment's reasonTable with the EMPTY code
// precisely because it must never reach a counterparty — it says this system
// tried an illegal transition, which is a defect here and not a judgement about
// anyone's instruction. Handing it to ReasonFor would turn it into MS03 and
// reject, on the wire, a payment that was in fact accepted.
func TestARedeliveredAcceptanceIsDeadLetteredAndNotRejected(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: "orig-1", MsgDefIdr: "pacs.008.001.08"},
		[]payment.TransactionStatusReport{{TxID: string(p.ID), Status: iso20022.TransactionStatusAccepted}},
		payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "sts-dup", Now: testTime})
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	if err := h.mesh.send(h.creditorBIC, h.cfg.ClearingHouseBIC, env); err != nil {
		t.Fatalf("send: %v", err)
	}

	err = h.drainErr(t)
	if !errors.Is(err, payment.ErrInvalidStateTransition) {
		t.Fatalf("Drain = %v, want the illegal transition as a dead letter", err)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("the duplicate moved the payment to %v; it was already Accepted", got.Status)
	}
}

// A payment addressed to a bank the mesh cannot route to is RC01, and it is the
// clearing house that says so — the only party that holds the routing table.
//
// The message is a doctored copy of one the payer's bank really sent, so the
// payment it names has already been accepted; the payer's bank therefore refuses
// to act on the rejection when it arrives, which is
// TestABankRefusesToReverseAPaymentThatIsNotRejected doing its job on an
// injection. What this test asserts is the code the clearing house put on the
// wire, and that the money did not move.
func TestACreditTransferForABankTheMeshCannotRouteToIsRC01(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	env, err := h.net.CreditTransferMessage(context.Background(), p,
		payment.MessageContext{From: h.debtorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "ct-x", Now: testTime})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	// A BIC no actor answers to.
	env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf.CdtTrfTxInf[0].CdtrAgt =
		iso20022.BranchAndFinancialInstitution{FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "NOSUCHFFXXX"}}
	if err := h.mesh.send(h.debtorBIC, h.cfg.ClearingHouseBIC, env); err != nil {
		t.Fatalf("send: %v", err)
	}
	// The dead letter this leaves is asserted rather than discarded, and it has
	// to be exactly this one: the payer's bank refusing to reverse a payment its
	// own network records as Accepted. Throwing the drain away would let any
	// other failure in the chain — the clearing house unable to build its answer,
	// a send refused — pass unnoticed underneath the assertion below.
	err = h.drainErr(t)
	if err == nil {
		t.Fatal("Drain was clean; the payer's bank acted on a rejection of a payment that is still Accepted")
	}
	if !strings.Contains(err.Error(), "records as Accepted") || !strings.Contains(err.Error(), string(h.debtorBIC)) {
		t.Errorf("dead letter %q is not the payer's bank refusing to reverse an accepted payment", err)
	}

	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonBankIdentifierIncorrect)
	if bal := h.suspense(t, h.debtorPID); bal != harnessAmount {
		t.Errorf("payer's clearing suspense = %d, want %d", bal, harnessAmount)
	}
}

// A second copy of a credit transfer the payee's bank has already answered is
// dead-lettered, not answered again.
//
// Same sentinel and same reasoning as the clearing house's duplicate, one hop
// earlier: by the time the redelivery arrives the payment is no longer
// Initiated, AcceptInboundTx refuses it with ErrInvalidStateTransition, and
// turning that into a pacs.002 would reject on the wire a payment this bank
// accepted. What the bank must not do is answer twice with two different
// answers.
func TestARedeliveredCreditTransferIsDeadLetteredAtThePayeesBank(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	// The pacs.008 the clearing house relayed, sent a second time.
	var relayed []byte
	h.mu.Lock()
	for _, m := range h.seen {
		if m.to == h.creditorBIC {
			relayed = m.raw
		}
	}
	h.mu.Unlock()
	if relayed == nil {
		t.Fatal("the payee's bank was never sent a pacs.008")
	}
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.creditorBIC, relayed)

	err := h.drainErr(t)
	if !errors.Is(err, payment.ErrInvalidStateTransition) {
		t.Fatalf("Drain = %v, want the illegal transition as a dead letter", err)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("the redelivery moved the payment to %v; it was already Accepted", got.Status)
	}
}
