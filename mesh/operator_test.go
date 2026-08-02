package mesh

import (
	"context"
	"strings"
	"testing"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The two entry points Task 14 needed and no message provokes: an operator
// rejecting a payment the clearing house is holding, and a bank joining a mesh
// that is already running.
//
// Both are the shape Mesh.Submit, Mesh.CloseCycle and Mesh.Return already have —
// synchronous, on the caller's goroutine, an error the caller can be answered
// with — because an instruction from outside the mesh is not a message arriving
// in an inbox, and the layer that instructs has somebody to tell.

// joinerIBAN addresses the customer of the bank that joins after Start. A third
// address, because an IBAN identifies an ACCOUNT: reusing either of the
// harness's two would be refused by the register, which holds one address on one
// account.
const joinerIBAN = "SE4550000000058398257466"

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
// Not by reading the suspense between the two calls, which is what a first draft
// did: the payer's bank actor runs concurrently with this goroutine, so
// "unchanged the moment Reject returned" is a race that happens to pass rather
// than an assertion. Nothing in this package may be timed.
//
// It is measured by WHICH BOOKS each actor's units of work reached, which the
// recording store answers exactly and without a clock. A clearing house that
// refunded the payer itself would have had to post in the payer's bank's book,
// and its set would say so. This is the rejection-shaped case of
// TestTheCSMTouchesOnlyTheNetworkBook.
func TestAnOperatorRejectionRefundsThePayerOnlyOnceTheMessageArrives(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	rejected, err := h.mesh.Reject(context.Background(), p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "card lost")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.Status != payment.Rejected {
		t.Fatalf("Reject returned %v, want Rejected", rejected.Status)
	}
	h.drain(t)

	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("clearing suspense = %d after draining, want 0 — the pacs.002 is what tells the payer's bank to give the money back", bal)
	}
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding {
		t.Errorf("payer's balance = %d after the rejection, want %d", bal, harnessFunding)
	}
	// The refund was posted by the payer's BANK and by nobody else. The clearing
	// house reached the network book, where a payment row lives, and no bank's.
	assertBooksTouched(t, "the clearing house", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
		[]ledger.BookID{ledger.NetworkBook})
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
	h := newMeshHarness(t)
	p := h.settledPayment(t)
	before := h.messagesSeen()

	if _, err := h.mesh.Reject(context.Background(), p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "too late"); err == nil {
		t.Fatal("Reject accepted a settled payment; the money has already moved between banks")
	}

	h.drain(t)
	if n := h.messagesSeen() - before; n != 0 {
		t.Errorf("%d messages were sent by a rejection that was refused, want 0", n)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Errorf("payment is %v after a refused rejection, want Settled", got.Status)
	}
}

// A bank admitted after the mesh started is reachable in BOTH directions, and
// only once AddBank has been called.
//
// Start reads the participant roster once. Every bank that joins later — which
// in a running system is every bank a human admits, and after a reseed is every
// bank there is — has a participant row, a chart of accounts and a reserve
// account, and no actor. That bank is not slow, it is unreachable: it cannot
// pay, because Mesh.Submit has no actor to hand its customer's instruction to,
// and it cannot be paid, because the clearing house has nothing under its BIC to
// route a pacs.008 to.
//
// Both directions are asserted, because they fail for different reasons and one
// of them can be fixed without the other: the bank index answers "can it pay"
// and the actor table answers "can it be paid".
func TestABankAdmittedAfterStartCanPayAndBePaid(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	joiner, err := h.net.AddParticipant(ctx, "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	if err != nil {
		t.Fatalf("AddParticipant: %v", err)
	}
	acct := h.openCustomer(t, joiner, "Nora", "EUR", 0, joinerIBAN)
	if err := h.net.Deposit(ctx, joiner.ID, acct.ID, harnessFunding, "Opening deposit"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	out := payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      payment.PartyRef{Participant: joiner.ID, Account: acct.ID, Identifier: acct.Identifiers[0]},
		Creditor:    h.creditorRef(creditorIBAN),
		Amount:      harnessAmount,
		Description: "invoice 44",
	}

	// In the roster, not in the mesh.
	if _, err := h.mesh.Submit(ctx, out); err == nil {
		t.Fatal("Submit accepted an instruction for a bank with no actor; nothing could ever have answered it")
	} else if !strings.Contains(err.Error(), "no bank actor") {
		t.Errorf("Submit refused with %v, want a refusal naming the missing actor", err)
	}

	if err := h.mesh.AddBank(joiner); err != nil {
		t.Fatalf("AddBank: %v", err)
	}

	sent, err := h.mesh.Submit(ctx, out)
	if err != nil {
		t.Fatalf("Submit after AddBank: %v", err)
	}
	h.drain(t)
	if got := h.payment(t, sent.ID); got.Status != payment.Accepted {
		t.Fatalf("the joining bank's own payment is %v, want Accepted", got.Status)
	}

	// And the other direction: a pacs.008 addressed to the new bank has an
	// inbox to land in, and the bank answers it.
	in := h.creditTransferRequest(t)
	in.Creditor = payment.PartyRef{
		Participant: joiner.ID,
		Account:     acct.ID,
		Identifier:  deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: joinerIBAN},
	}
	received, err := h.mesh.Submit(ctx, in)
	if err != nil {
		t.Fatalf("Submit to the joining bank: %v", err)
	}
	h.drain(t)
	if got := h.payment(t, received.ID); got.Status != payment.Accepted {
		t.Fatalf("the payment addressed to the joining bank is %v, want Accepted", got.Status)
	}
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusAccepted)
}

// Two banks on one BIC is refused at admission, and the mesh is left as it was.
//
// It is the same refusal Start makes about a roster it cannot route, arriving
// one bank at a time: the actor table would keep the second and drop the first,
// and the dropped bank's goroutine would read an inbox nothing could address.
// The bank INDEX must be left alone too — a Submit that found the newcomer under
// the old bank's actor would sign one bank's instruction with another's identity.
func TestAddingABankOnAnotherBanksBICIsRefusedAndChangesNothing(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	clash, err := h.net.AddParticipant(ctx, "Aurora Bank (again)", h.debtorBIC, euroOnly)
	if err != nil {
		t.Fatalf("AddParticipant: %v", err)
	}
	if err := h.mesh.AddBank(clash); err == nil {
		t.Fatal("AddBank accepted a second bank on an address another bank already answers to")
	}

	h.mesh.mu.Lock()
	_, indexed := h.mesh.banks[clash.ID]
	h.mesh.mu.Unlock()
	if indexed {
		t.Error("the refused bank is in the mesh's bank index; a refusal must leave it as it found it")
	}
}
