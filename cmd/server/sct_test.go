package main

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
	h := newHarness(t)

	p := h.submitCreditTransfer(t)
	if p.Status != payment.Initiated {
		t.Fatalf("submit returned %v, want Initiated — the creditor's bank has not answered yet", p.Status)
	}

	h.work(t)

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
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)

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
		t.Fatalf("the network carried %d files, want %d", len(seen), len(want))
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
	h := newHarness(t)
	p := h.submitCreditTransferTo(t, unknownIBANAt(h.creditor))
	h.work(t)

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
	h := newHarness(t)
	h.submitCreditTransferTo(t, unknownIBANAt(h.creditor))
	h.work(t)

	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonIncorrectAccountNumber)
}

func TestCreditTransferWithNoOpenCycleIsTM01(t *testing.T) {
	h := newHarnessWithNoOpenCycle(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

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
	h := newHarness(t)
	h.injectRaw(t, h.debtorBIC, h.cfg.ClearingHouseBIC, []byte("<Envelope><nonsense/>"))
	h.work(t)
	// The CSM could not parse it, so there is no payment to reject; what it
	// can do is tell the sender the file was invalid. Assert the debtor bank
	// received a pacs.002 carrying FF01.
	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonInvalidFileFormat)
}

// Submit runs the submitting bank's half synchronously, so a submission that
// fails inside its unit of work fails at the caller — and, because the send
// happens after the commit, it sends nothing.
func TestARolledBackSubmitSendsNothing(t *testing.T) {
	h := newHarness(t)
	// A submission that fails inside its unit of work — an amount of zero.
	req := h.creditTransferRequest(t)
	req.Amount = 0
	if _, err := h.dep.Submit(context.Background(), req); err == nil {
		t.Fatal("Submit accepted a zero amount")
	}
	h.work(t)
	if got := h.messagesSeen(); got != 0 {
		t.Fatalf("a rolled-back submission sent %d messages, want 0", got)
	}
}

// A message type an actor has no handler for is a dead letter and not a shrug.
//
// A pacs.004 is not usable as the example: it travels to a bank after finality
// and that bank posts its own leg from it, so it is a message every bank in this
// system has a handler for.
//
// A pacs.009 takes its place, and it is the same kind of thing rather than a
// substitute chosen for convenience. A settlement instruction is addressed to
// the SETTLEMENT AGENT and to nobody else: it names net positions between
// members and the central bank, and a member bank has nothing it could do with
// one. So one arriving at a bank is a bug in whoever sent it, and swallowing it
// would make a half this system does not have look like one it does.
//
// Every message definition this system's actors emit now has an arm at a member
// bank except this one, which is why it is the last example available. A future
// task that gives banks a pacs.009 arm has to find another — or conclude that
// this assertion has run out of subject and say so.
func TestAFileAnInstitutionHasNoHandlerForIsReported(t *testing.T) {
	h := newHarness(t)
	env, err := payment.SettlementMessage(
		[]payment.SettlementLeg{{
			From: h.debtorBIC, To: h.cfg.CentralBankBIC,
			Amount: harnessAmount, Asset: "EUR", Reference: "cyc_never",
		}},
		payment.MessageContext{From: h.cfg.ClearingHouseBIC, To: h.debtorBIC, MsgID: "stl-1", Now: testTime})
	if err != nil {
		t.Fatalf("SettlementMessage: %v", err)
	}
	h.upload(t, h.cfg.ClearingHouseBIC, h.debtorBIC, env)

	err = h.workErr(t)
	if err == nil {
		t.Fatal("Drain was clean; the bank swallowed a message it has no handler for")
	}
	if !strings.Contains(err.Error(), "pacs.009") {
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
	h := newHarness(t)
	p := h.submitCreditTransferTo(t, unknownIBANAt(h.creditor))
	h.work(t)
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
	h.upload(t, h.cfg.ClearingHouseBIC, h.creditorBIC, env)

	err = h.workErr(t)
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

// A bank reverses a debit once, and never after the money has moved between
// banks. Those are the two refusals it still has, and this measures the second.
//
// ReverseDebtorLegTx does not load the payment and does not look at its status —
// its own doc says so, and says the caller establishes the decision. The caller
// is payment.RejectAtBankTx now, and what it establishes the decision against is
// THIS BANK's own copy: a rejection of a payment already recorded as Settled or
// Rejected is refused, because the first would take back a debit that has
// already funded a reserve movement and the second would reverse a leg that is
// already reversed.
//
// # A forged rejection cannot be told from a real one
//
// Each institution holds a copy nobody else can write, so "does this network
// call it rejected" is not a question a bank can ask — the decider's own record
// is in another database. A genuine post-acceptance rejection and a forged one
// are the same bytes from the same address about a payment in the same state.
//
// Refusing on Accepted was tried and is wrong on the domain as well as on the
// data. A rejection is a pre-SETTLEMENT act, not a pre-acceptance one: a cut-off
// that finds a participant short of its position rejects payments that have been
// in a cycle for hours, and a bank that answered "I was already told ACCP" would
// be refusing the ordinary case. See payment.Network.transition.
//
// What replaces it is not here, because it cannot be: a real network answers
// forgery with the CHANNEL — an authenticated message, non-repudiable, on a
// closed network — and with reconciliation against the cycle reports afterwards.
// What this system CAN do it does one hop earlier, at the institution that would
// have to be lying: the clearing house never sends an RJCT about a payment it has
// not rejected. See csm.relayRecorded, which rejects its own copy in the same
// breath as the RC01 it answers with.
func TestABankRefusesToReverseAPaymentThatIsNotRejected(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)
	h.rec.reset()

	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: "orig-1", MsgDefIdr: "pacs.008.001.08"},
		[]payment.TransactionStatusReport{{
			TxID:   string(p.ID),
			Status: iso20022.TransactionStatusRejected,
			Code:   iso20022.StatusReasonIncorrectAccountNumber,
			Text:   "a decision that arrived after finality",
		}},
		payment.MessageContext{From: h.cfg.ClearingHouseBIC, To: h.debtorBIC, MsgID: "sts-y", Now: testTime})
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	h.upload(t, h.cfg.ClearingHouseBIC, h.debtorBIC, env)

	err = h.workErr(t)
	if err == nil {
		t.Fatal("Drain was clean; the payer's bank reversed the debit of a payment that has settled")
	}
	// Named, and not merely non-nil. The refusal has to be the STATE one and not
	// the ledger's own refusal to reverse a transaction twice, which would mean
	// the bank had already reached its customer's account to find out.
	if !strings.Contains(err.Error(), "records as Settled") || !strings.Contains(err.Error(), string(h.debtorBIC)) {
		t.Errorf("dead letter %q is not the payer's bank refusing to reverse a settled payment", err)
	}
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding-harnessAmount {
		t.Errorf("payer's balance = %d, want %d — the settled debit was reversed anyway",
			bal, harnessFunding-harnessAmount)
	}
	assertBooksTouched(t, "the payer's bank, refusing a rejection that arrived after finality",
		h.booksTouchedBy(h.debtorBIC), nil)
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
func TestARedeliveredAcceptanceIsReportedAndNotRejected(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: "orig-1", MsgDefIdr: "pacs.008.001.08"},
		[]payment.TransactionStatusReport{{TxID: string(p.ID), Status: iso20022.TransactionStatusAccepted}},
		payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "sts-dup", Now: testTime})
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	h.upload(t, h.creditorBIC, h.cfg.ClearingHouseBIC, env)

	err = h.workErr(t)
	// Matched on the TEXT and not with errors.Is: a day's report is prose an
	// operator reads, and Problem.Detail does not wrap the sentinel.
	if err == nil || !strings.Contains(err.Error(), payment.ErrInvalidStateTransition.Error()) {
		t.Fatalf("the day reported %v, want the illegal transition as a problem", err)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("the duplicate moved the payment to %v; it was already Accepted", got.Status)
	}
}

// A payment addressed to a bank the clearing house cannot route to is RC01, and it is the
// clearing house that says so — the only party that holds the routing table.
//
// # The message names a payment nobody has seen, and it has to
//
// A doctored copy of a status the payer's bank really sent, id and all, would
// make the RC01 an answer about a payment already ACCEPTED and sitting in an open
// cycle — the shape of defect the clearing house refuses to produce, because a
// bank acting on such a status reverses a debit that is funding a settlement and
// no state its own copy could be in would let it tell that message from a
// rejection this network really made (payment.RejectAtBankTx). The replay is
// refused as a DUPLICATE before anything goes on the wire — see
// csm.relayRecorded — so keeping the original id would measure that refusal
// instead of the routing table.
//
// So the transaction id is doctored too, and the file is then what a forged
// instruction is: a well-formed pacs.008 for a payment no institution holds. The
// clearing house records it, cannot route it, answers RC01, and marks ITS OWN
// copy rejected — which is the property this test gained, because a clearing
// house whose row said Initiated while its message said RJCT is the same
// inconsistency one layer up.
//
// The payer's bank then dead-letters the answer, and that is correct rather than
// incidental: it is being told about a payment it never submitted and holds no
// row for. It is asserted rather than discarded so that a failure anywhere else
// in the chain cannot hide underneath the assertions below.
func TestACreditTransferForABankTheClearingHouseCannotRouteToIsRC01(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	env, err := h.net.CreditTransferMessage([]payment.Payment{p},
		payment.MessageContext{From: h.debtorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "ct-x", Now: testTime})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	const forged = payment.PaymentID("pay_AURODEFFXXX_9001")
	tx := &env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf.CdtTrfTxInf[0]
	tx.PmtId.TxId = string(forged)
	// A BIC no actor answers to.
	tx.CdtrAgt = iso20022.BranchAndFinancialInstitution{
		FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "NOSUCHFFXXX"}}
	h.upload(t, h.debtorBIC, h.cfg.ClearingHouseBIC, env)
	err = h.workErr(t)
	if err == nil {
		t.Fatal("Drain was clean; the payer's bank acted on a rejection of a payment it never sent")
	}
	if !strings.Contains(err.Error(), string(forged)) || !strings.Contains(err.Error(), string(h.debtorBIC)) {
		t.Errorf("dead letter %q is not the payer's bank refusing a rejection of a payment it holds no row for", err)
	}

	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonBankIdentifierIncorrect)
	// The clearing house's own copy says what its message said.
	if got := h.payment(t, forged); got.Status != payment.Rejected {
		t.Errorf("the clearing house records the unroutable payment as %v; it answered RC01 about it", got.Status)
	}
	// And the real payment, whose id the forgery is derived from, is untouched.
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("%s is %v after a forged message about another payment, want Accepted", p.ID, got.Status)
	}
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
func TestARedeliveredCreditTransferIsReportedAtThePayeesBank(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

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

	err := h.workErr(t)
	// Matched on the TEXT and not with errors.Is: a day's report is prose an
	// operator reads, and Problem.Detail does not wrap the sentinel.
	if err == nil || !strings.Contains(err.Error(), payment.ErrInvalidStateTransition.Error()) {
		t.Fatalf("the day reported %v, want the illegal transition as a problem", err)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("the redelivery moved the payment to %v; it was already Accepted", got.Status)
	}
}

// TestAnOnUsPaymentIsRefusedBeforeItReachesAClearingHouse is the boundary this
// this transport does not have, and the thing it refuses is not an error in the message.
//
// # Why an on-us payment is not a clearing payment
//
// Both customers bank at the same institution, so paying one from the other is
// that bank moving money between two of its own deposit accounts. Nothing leaves
// it. No reserves move, because a reserve is what one bank owes another through
// the central bank and this bank owes itself nothing; there is nothing for a
// clearing house to net, nothing for a settlement agent to settle, and no
// camt.053 that could tell a bank about its own book. A real scheme never sees
// one: the payer's bank recognises the beneficiary as its own and books the
// transfer internally.
//
// Each of the three institutions does something incoherent with an on-us
// payment. The clearing house nets a position of zero and drops the payment out
// of the settlement instruction, so a cycle holding nothing but an on-us payment
// strands at Cleared for ever. The settlement agent's return path sends the SAME
// bank two camt.053s about the SAME account under the same reference, differing
// only in sign, and the second is swallowed by the advice row the first wrote —
// so that bank's reserve mirror moves by the full amount while the central
// bank's record of it does not, and its clearing suspense goes permanently
// negative. The returning bank is both parties, so it holds both legs and
// refuses its own customer's unconditional refund.
//
// Every one of those is a symptom of a payment that should never have been
// submitted to clearing, which is why the refusal is here — at the one door
// every submission comes through — and not three patches further in.
//
// # What it does NOT refuse
//
// A book transfer between two customers of one bank is a real product and this
// system offers it — deposit.Register.TransferTx, which is the register's own
// act and reaches no transport at all. The refusal is a statement about the wrong
// ROUTE and not about the payment being illegitimate, so a caller reading it
// knows which of the two to ask for instead.
func TestAnOnUsPaymentIsRefusedBeforeItReachesAClearingHouse(t *testing.T) {
	// Both directions, because the submitting bank differs — a push is submitted
	// by the payer's bank and a collection by the payee's — and on-us is the one
	// arrangement in which those are the same institution. A guard that read the
	// submitter rather than the two parties would pass one of these.
	for _, tc := range []struct {
		name   string
		scheme payment.SchemeID
	}{
		{"a credit transfer", payment.SchemeSEPACT},
		{"a collection", payment.SchemeSEPADD},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			// A second customer at the PAYER's bank, so both parties are that
			// bank's.
			other := h.openCustomer(t, h.debtor, "Carla", "EUR", 0)
			otherRef := payment.PartyRef{Account: other.ID, Identifier: other.Identifiers[0]}
			// A mandate, so that a collection is refused for being on-us and not
			// for being unauthorised. Without it SDD.ValidateMandate would refuse
			// first and this test would pass on a network with no boundary at all.
			// Both parties are the payer's bank's, so it is also the creditor's
			// bank and the one that records the mandate.
			mandate, err := h.bank(h.debtorBIC).CreateMandate(ctx, h.debtorBIC, h.debtorRef(), otherRef, 0)
			if err != nil {
				t.Fatalf("CreateMandate: %v", err)
			}

			before := h.messagesSeen()
			_, err = h.dep.Submit(ctx, payment.InitiatePaymentRequest{
				Scheme:          tc.scheme,
				MandateID:       mandate.ID,
				Debtor:          h.debtorRef(),
				Creditor:        otherRef,
				Amount:          harnessAmount,
				Description:     "one bank, both customers",
				CreditorDetails: payment.PartyDetails{Agent: h.debtorBIC, Name: other.Name},
				DebtorDetails:   payment.PartyDetails{Agent: h.debtorBIC, Name: h.debtorAcct.Name},
			})
			if !errors.Is(err, payment.ErrOnUsPayment) {
				t.Fatalf("Submit = %v, want it refused as an on-us payment", err)
			}
			// The refusal names the bank, because a caller holding several
			// participants has to be told which one is both ends.
			if !strings.Contains(err.Error(), string(h.debtorPID)) {
				t.Errorf("the refusal reads %q and does not name the bank that is both parties", err)
			}

			// Refused BEFORE anything happened, not unwound afterwards: no
			// payment row, no debit, no message. The submitting bank's half runs
			// synchronously inside Submit, so a guard placed after it would have
			// left a row behind.
			payments, err := h.net.ListPayments(ctx)
			if err != nil {
				t.Fatalf("ListPayments: %v", err)
			}
			if len(payments) != 0 {
				t.Errorf("the refused on-us submission left %d payments behind, want none", len(payments))
			}
			if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding {
				t.Errorf("the payer holds %d after the refused submission, want the %d they started with", got, harnessFunding)
			}
			if got := len(h.messagesFrom(before)); got != 0 {
				t.Errorf("the refused on-us submission put %d messages on the wire, want none", got)
			}
		})
	}
}
