package main

import (
	"context"
	"strings"
	"testing"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/storetest"
)

// The two entry points no file provokes: an operator rejecting a payment the
// clearing house is holding, and a bank joining a deployment that is already
// running.
//
// Both are the shape Deployment.Submit, ClearingHouse.CloseCycle and
// Deployment.Return already have — synchronous, on the caller's goroutine, an
// error the caller can be answered with — because an instruction from outside a
// business day is not a file collected from a queue, and the layer that
// instructs has somebody to tell.

// An operator's rejection is TWO halves in two actors, and only the first one
// has anybody to answer.
//
// The clearing house's half is what Reject returns: the payment is Rejected and
// out of its cycle, then and there. Giving the payer their money back is their
// own bank's act, in their own bank's book, and it happens when the pacs.002
// gets there. Before the mesh, api ran both in one transaction and no caller
// could tell the two apart.
//
// # How that is measured, and how it deliberately is not
//
// Not by reading the suspense between the two calls. Nothing races here — the
// pacs.002 sits in the payer's bank's download queue until that bank collects —
// so "unchanged the moment Reject returned" would be true and would say nothing
// about who is entitled to change it.
//
// It is measured by WHICH BOOKS each actor's units of work reached, which the
// recording store answers exactly and without a clock. A clearing house that
// refunded the payer itself would have had to post in the payer's bank's book,
// and its set would say so. This is the rejection-shaped case of
// TestTheCSMTouchesOnlyItsOwnBook.
func TestAnOperatorRejectionRefundsThePayerOnlyOnceTheMessageArrives(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	rejected, err := h.dep.ClearingHouse().Reject(context.Background(), p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "card lost")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.Status != payment.Rejected {
		t.Fatalf("Reject returned %v, want Rejected", rejected.Status)
	}
	h.work(t)

	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("clearing suspense = %d after draining, want 0 — the pacs.002 is what tells the payer's bank to give the money back", bal)
	}
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding {
		t.Errorf("payer's balance = %d after the rejection, want %d", bal, harnessFunding)
	}
	// The refund was posted by the payer's BANK and by nobody else. The clearing
	// house reached the network book, where a payment row lives, and no bank's.
	//
	// What that catches, exactly: a clearing house that WROTE in a member's book.
	// No interface can close that, because any posting method takes its book as an
	// ordinary argument. Adding a write to the payer's bank in csm.reject fails
	// this line.
	//
	// What it does not catch is a reject that forgot withActor. The clearing
	// house's own goroutine has already written the network book carrying this
	// payment, so the set contains it either way — worth saying, because the
	// natural reading of this line is that it holds the attribution too.
	assertBooksTouched(t, "the clearing house", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
		[]ledger.BookID{payment.ClearingHouseBook})
	// The code the operator named travels, rather than being replaced by
	// whatever the clearing house would have said on its own behalf.
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusRejected)
	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonNotSpecifiedAgentGenerated)
}

// A payment the clearing house cannot reject is refused SYNCHRONOUSLY, and
// nothing is sent.
//
// It is the answerable half of the split ruling 4 of this task's brief is about:
// a refusal decided inside the clearing house's own unit of work is one the
// operator who asked can be told about, with a status code, in the response to
// their request. What cannot come back that way is anything a counterparty
// decides — which for a rejection is the refund, three hops and one actor later.
func TestAnOperatorRejectionOfASettledPaymentIsRefusedAndSendsNothing(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)
	before := h.messagesSeen()

	if _, err := h.dep.ClearingHouse().Reject(context.Background(), p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "too late"); err == nil {
		t.Fatal("Reject accepted a settled payment; the money has already moved between banks")
	}

	h.work(t)
	if n := h.messagesSeen() - before; n != 0 {
		t.Errorf("%d messages were sent by a rejection that was refused, want 0", n)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Errorf("payment is %v after a refused rejection, want Settled", got.Status)
	}
}

// A bank the deployment does not know about is reachable in BOTH directions once
// AddBank has been called, and unreachable until then.
//
// # Why the fixture builds it without the deployment
//
// The state this is about is a bank the deployment has not READ: rows written by
// something other than its own door. storetest.Admit builds exactly that — three
// institutions' rows, and no place in the network.
//
// Such a bank is not slow, it is unreachable: it cannot pay, because
// Deployment.Submit holds no view of it to hand its customer's instruction to,
// and it cannot be paid, because the clearing house holds no download queue under
// its BIC to route a pacs.008 into.
//
// Both directions are asserted, because they fail for different reasons and one
// of them can be fixed without the other: the bank index answers "can it pay"
// and enrolment answers "can it be paid".
func TestABankAdmittedAfterStartCanPayAndBePaid(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	joiner, err := storetest.Admit(ctx, h.nets, "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	if err != nil {
		t.Fatalf("admitting Nordhaven: %v", err)
	}
	acct := h.openCustomer(t, joiner, "Nora", "EUR", 0)
	if err := h.bank(joiner.BIC).Deposit(ctx, joiner.ID, acct.ID, harnessFunding, "Opening deposit"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	out := payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      payment.PartyRef{Account: acct.ID, Identifier: acct.Identifiers[0]},
		Creditor:    h.creditorRef(h.creditorIBAN),
		Amount:      harnessAmount,
		Description: "invoice 44",
		// Push: the creditor is the counterparty, so the request must name it —
		// name and BIC both, as everywhere else.
		CreditorDetails: payment.PartyDetails{Agent: h.creditorBIC, Name: h.creditorAcct.Name},
		DebtorDetails:   payment.PartyDetails{Agent: joiner.BIC}}

	// In the roster, and nowhere in this deployment.
	if _, err := h.dep.Submit(ctx, out); err == nil {
		t.Fatal("Submit accepted an instruction for a bank this deployment holds no view of; nothing could ever have uploaded it")
	} else if !strings.Contains(err.Error(), "holds no bank at") {
		t.Errorf("Submit refused with %v, want a refusal naming the bank it does not hold", err)
	}

	if err := h.dep.AddBank(context.Background(), joiner); err != nil {
		t.Fatalf("AddBank: %v", err)
	}

	sent, err := h.dep.Submit(ctx, out)
	if err != nil {
		t.Fatalf("Submit after AddBank: %v", err)
	}
	h.work(t)
	if got := h.payment(t, sent.ID); got.Status != payment.Accepted {
		t.Fatalf("the joining bank's own payment is %v, want Accepted", got.Status)
	}

	// And the other direction: a pacs.008 addressed to the new bank has a queue
	// to land in, and the bank answers it.
	in := h.creditTransferRequest(t)
	in.Creditor = payment.PartyRef{
		Account:    acct.ID,
		Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: addressOf(t, acct)},
	}
	in.CreditorDetails = payment.PartyDetails{Agent: joiner.BIC, Name: acct.Name}
	in.CreditorDetails = payment.PartyDetails{Agent: joiner.BIC, Name: acct.Name}
	received, err := h.dep.Submit(ctx, in)
	if err != nil {
		t.Fatalf("Submit to the joining bank: %v", err)
	}
	h.work(t)
	if got := h.payment(t, received.ID); got.Status != payment.Accepted {
		t.Fatalf("the payment addressed to the joining bank is %v, want Accepted", got.Status)
	}
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusAccepted)
}
