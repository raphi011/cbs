package mesh

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// A return is routed to the CENTRAL BANK, not the CSM. ReturnPaymentTx moves
// reserves, and the actor that owns reserve movement is the settlement agent —
// payment/doc.go already records that returns settle immediately rather than
// through a later R-cycle, so a return genuinely is a settlement act here.
func TestAReturnIsExecutedByTheCentralBank(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	h.rec.reset()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.drain(t)

	got := h.payment(t, p.ID)
	if got.Status != payment.Returned {
		t.Fatalf("status = %v, want Returned", got.Status)
	}
	assertBooksTouched(t, "the clearing house, carrying a return", h.booksTouchedBy(h.cfg.ClearingHouseBIC), nil)
}

// TestTheReturnChainIsFourMessages names the conversation, the way
// TestTheCreditTransferChainIsFourMessages names the push and
// TestTheMessagesACutOffPutsOnTheWire the cut-off. It is what PINS the routing
// decision this task made; the status assertion above cannot see who sent what
// to whom.
//
// Four messages, and the same four hops a credit transfer has — with a
// different bank at one end and a different institution at the other:
//
//	payee's bank  --pacs.004-->  clearing house  --pacs.004-->  central bank
//	payee's bank  <--pacs.002--  clearing house  <--pacs.002--  central bank
//
// Three decisions are visible in that shape and each was deliberate.
//
// The PAYEE's bank starts it, because a return is sent by the bank that
// RECEIVED the original instruction — the SEPA rule book's own division, and
// the opposite end from the one that submitted. See returnerOf, and
// TestAReturnedCollectionIsSentByThePayersBank for the other direction.
//
// It goes to the CENTRAL BANK, because a return moves reserves. That is the
// whole argument for this flow existing at the settlement agent rather than at
// the clearing house, and payment/doc.go already records the consequence:
// returns settle immediately here rather than being netted in a later R-cycle.
//
// It goes THROUGH the clearing house rather than bank-to-central-bank directly.
// A member bank in this mesh addresses the clearing house and nothing else, and
// the routing table lives in one actor — which is why RC01 is a thing only that
// actor can say.
//
// Those are two claims and they are measured by two different tests, which is
// worth stating because neither covers the other. THIS test, with
// TestAReturnedCollectionIsSentByThePayersBank beside it, is what says the
// clearing house is in the path at all: point the returning bank straight at
// the central bank and the return still happens, in two messages instead of
// four. What the book assertions say is the narrower thing — that the clearing
// house posts nothing while carrying it — and they stay green under exactly
// that mutation, because an actor that is skipped touches no book either.
//
// The PAYER's bank is in none of the four. Its customer is refunded by the
// settlement agent, in the same unit of work that moves the reserves, so there
// is nothing for it to do and nothing it is waiting for — the exact inverse of
// the rejected collection, where the bank holding the money and the bank waiting
// for the answer are different institutions and BOTH have to be told. "One
// decision, one message" is not a rule in this system; who is owed the news is.
func TestTheReturnChainIsFourMessages(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	before := h.messagesSeen()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.drain(t)

	want := []struct {
		from, to iso20022.BIC
		msgDef   string
	}{
		{h.creditorBIC, h.cfg.ClearingHouseBIC, "pacs.004.001.09"},
		{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "pacs.004.001.09"},
		{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"},
		{h.cfg.ClearingHouseBIC, h.creditorBIC, "pacs.002.001.10"},
	}
	seen := h.messagesFrom(before)
	if len(seen) != len(want) {
		t.Fatalf("the return put %d messages on the wire, want %d", len(seen), len(want))
	}
	for i, m := range seen {
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			t.Fatalf("message %d does not parse: %v", i, err)
		}
		if m.from != want[i].from || m.to != want[i].to || env.AppHdr.MsgDefIdr != want[i].msgDef {
			t.Errorf("message %d is %s -> %s (%s), want %s -> %s (%s)",
				i, m.from, m.to, env.AppHdr.MsgDefIdr, want[i].from, want[i].to, want[i].msgDef)
		}
	}
}

// TestAReturnPutsTheMoneyBackInThePayersAccount is what a return is FOR,
// asserted where the customers are rather than where the message is.
//
// Both accounts, because a return is not a refund out of thin air: the payee's
// bank claws the money back out of the payee's account and the payer's bank
// credits the payer, and the reserve leg between the two banks is what makes
// those two postings one act. A test that checked only the payer would pass on
// a system that paid them twice.
//
// The balances are read through each bank's own deposit register, which is the
// only place the answer exists — the payment row says Returned, and a status is
// not a balance.
func TestAReturnPutsTheMoneyBackInThePayersAccount(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)
	if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding-harnessAmount {
		t.Fatalf("the payer holds %d after a settled transfer, want %d", got, harnessFunding-harnessAmount)
	}
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != harnessAmount {
		t.Fatalf("the payee holds %d after a settled transfer, want %d", got, harnessAmount)
	}

	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.drain(t)

	if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding {
		t.Errorf("the payer holds %d after the return, want %d — the whole of it should be back", got, harnessFunding)
	}
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != 0 {
		t.Errorf("the payee holds %d after the return, want 0", got)
	}
}

// TestAReturnedCollectionIsSentByThePayersBank is the other direction, and it
// is the one that says returnerOf is a rule about ROLES rather than a habit of
// picking the creditor.
//
// A collection is submitted by the payee's bank and answered by the payer's, so
// the bank that received the instruction — and therefore the bank that returns
// it — is the payer's. That is the SEPA rule book's own division: a debtor bank
// returns a collection its customer disputes, which is what MD01 says here.
//
// The payee's bank is told NOTHING, which is the mirror of the push's silence
// towards the payer's bank and rests on the same rule: the answer goes to the
// bank that asked. It is the bank that gets the money taken back off it, so
// this is the sharper of the two silences and is measured rather than assumed —
// under a shared store it learns by reading the payment row, and a real network
// would have to tell it.
func TestAReturnedCollectionIsSentByThePayersBank(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledCollection(t)

	before := h.messagesSeen()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonNoMandate, "the debtor disputes the mandate")
	h.drain(t)

	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("status = %v, want Returned", got.Status)
	}
	sent := h.messagesFrom(before)
	if len(sent) == 0 {
		t.Fatal("returning the collection put nothing on the wire at all")
	}
	first := sent[0]
	if first.from != h.debtorBIC || first.to != h.cfg.ClearingHouseBIC {
		t.Errorf("the return starts at %s -> %s, want the payer's bank %s -> the clearing house",
			first.from, first.to, h.debtorBIC)
	}
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusSettlementCompleted)
	if got := h.messagesSeen() - before; got != 4 {
		t.Errorf("returning a collection took %d messages, want 4", got)
	}
	for _, m := range h.messagesFrom(before) {
		if m.to == h.creditorBIC {
			t.Errorf("the payee's bank was sent a message during the return; it asked for nothing")
		}
	}
}

// TestABankRefusesToReturnAPaymentThatHasNotSettled is the guard the returning
// bank makes before any message exists, and the reason it is not left to the
// settlement agent.
//
// ReturnPaymentTx would refuse it too, with ErrInvalidStateTransition — and
// that sentinel is the one payment's reasonTable gives the empty code, because
// it describes a defect in this system rather than a judgement about anyone's
// instruction. An actor that received it must dead-letter it, so a return sent
// for a payment that has not settled would be answered by nobody at all and the
// operator who asked for it would hear nothing. Refused here, it is the caller's
// answer.
//
// And nothing goes on the wire, which is the second half of the claim: a
// refusal that had already sent the pacs.004 would have the settlement agent
// dead-lettering a message about a payment this bank knew was not returnable.
func TestABankRefusesToReturnAPaymentThatHasNotSettled(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t) // accepted and in a cycle, but no cut-off has been reached

	before := h.messagesSeen()
	err := h.returnErr(p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	if !errors.Is(err, payment.ErrInvalidStateTransition) {
		t.Fatalf("Return = %v, want the illegal transition refused to the caller", err)
	}
	if !strings.Contains(err.Error(), "Accepted") {
		t.Errorf("the refusal %q does not say what this network records the payment as", err)
	}
	h.drain(t)
	if got := h.messagesSeen(); got != before {
		t.Errorf("a refused return put %d messages on the wire, want none", got-before)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("the refused return moved the payment to %v", got.Status)
	}
}

// TestARedeliveredReturnIsDeadLetteredAndNotAnswered is the same
// discrimination one hop further on, and it is the case that actually reaches
// the settlement agent.
//
// A queue redelivers, so the pacs.004 the clearing house carried can arrive
// twice. The second copy names a payment this network has already returned,
// ReturnPaymentTx answers ErrInvalidStateTransition, and turning that into a
// pacs.002 would tell the returning bank that a return which in fact happened
// was rejected — MS03, through ReasonFor's fallback, which is what reasonTable's
// empty code exists to forbid. Dead letter, and no status at all.
func TestARedeliveredReturnIsDeadLetteredAndNotAnswered(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.drain(t)

	// The pacs.004 the clearing house relayed, sent to the settlement agent a
	// second time.
	relayed := h.lastMessageOfTypeTo(t, h.cfg.CentralBankBIC, "pacs.004.001.09")
	answered := h.statusesSentTo(h.creditorBIC)
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, relayed)

	// Errorf and not Fatalf: "dead-lettered" and "not answered" are two claims,
	// and a settlement agent that answered this instead would break both at
	// once. Stopping at the first would leave the second unobserved in exactly
	// the case it exists for.
	if err := h.drainErr(t); !errors.Is(err, payment.ErrInvalidStateTransition) {
		t.Errorf("Drain = %v, want the illegal transition as a dead letter", err)
	}
	if got := h.statusesSentTo(h.creditorBIC); got != answered {
		t.Errorf("the redelivery produced %d further statuses to the bank that asked, want none", got-answered)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Errorf("the redelivery moved the payment to %v; it was already Returned", got.Status)
	}
	// And the payer was not refunded twice.
	if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding {
		t.Errorf("the payer holds %d after a redelivered return, want %d", got, harnessFunding)
	}
}

// TestAReturnTheSettlementAgentCannotActOnWholeIsRefused is the guard on the
// one assumption this actor makes about the message it is handed: that it is
// being asked to return exactly one payment.
//
// Two ways a message can break that, and both are refused rather than
// half-executed, for the reason cycleOf gives about a settlement instruction
// naming two cycles. Returning the first and dropping the rest would leave a
// payment somebody was told had been sent back and never was.
//
// Injected rather than provoked, because no actor in this mesh emits either:
// payment.ReturnMessage builds exactly one transaction and counts it. So each
// case is a real message, doctored — which is also why the refusal is asserted
// at the RETURNING BANK rather than at the clearing house. The whole path runs:
// the clearing house carries the doctored return, the settlement agent refuses
// it, and the answer comes back to the bank that asked.
func TestAReturnTheSettlementAgentCannotActOnWholeIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		doctor   func(*iso20022.Pacs004)
		wantText string
	}{
		{
			// Two returns in one message, correctly counted. Nothing about it
			// is malformed; it is simply more than this actor acts on.
			name: "two returns in one message",
			doctor: func(d *iso20022.Pacs004) {
				d.PmtRtr.TxInf = append(d.PmtRtr.TxInf, d.PmtRtr.TxInf[0])
				d.PmtRtr.GrpHdr.NbOfTxs = "2"
			},
			wantText: "TxInf carries 2",
		},
		{
			// One transaction arrived and the sender says there were two. That
			// is a transaction lost in transit, which for a return is a payer
			// who never gets their money back.
			name: "a count that does not match what arrived",
			doctor: func(d *iso20022.Pacs004) {
				d.PmtRtr.GrpHdr.NbOfTxs = "2"
			},
			wantText: `NbOfTxs says "2"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newMeshHarness(t)
			p := h.settledPayment(t)

			env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "account closed",
				payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-doctored", Now: testTime})
			if err != nil {
				t.Fatalf("ReturnMessage: %v", err)
			}
			tc.doctor(env.Document.(*iso20022.Pacs004))
			if err := h.mesh.send(h.creditorBIC, h.cfg.ClearingHouseBIC, env); err != nil {
				t.Fatalf("send: %v", err)
			}
			h.drain(t)

			// Answered, not dead-lettered: this is a judgement about the
			// message, and the bank that sent it can act on the answer.
			h.assertLastTxStatusTo(t, h.creditorBIC, iso20022.TransactionStatusRejected)
			// The exact refusal, not merely a refusal: the two checks refuse
			// different things and a bank reading the answer has to be able to
			// tell which, so a test that accepted either would pass on a
			// settlement agent that had lost one of them.
			if got := statusText(h.lastStatusTo(t, h.creditorBIC)); !strings.Contains(got, tc.wantText) {
				t.Errorf("the refusal is %q, want it to say %q", got, tc.wantText)
			}
			if got := h.payment(t, p.ID); got.Status != payment.Settled {
				t.Errorf("the payment is %v; a refused return returns nothing", got.Status)
			}
			if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != harnessAmount {
				t.Errorf("the payee holds %d; a refused return claws nothing back", got)
			}
		})
	}
}

// TestTheReturnsReasonTravelsFromTheAskingBankToTheLedgers is the datum a
// pacs.004 exists to carry, followed the whole way.
//
// Every other assertion in this file would pass on a system that returned the
// money for the wrong reason, or for none: two returns of the same payment
// under opposite codes move exactly the same amounts between exactly the same
// accounts. The reason is what tells a payer whose account was closed from a
// payer who disputed a mandate, and it is the only part of a return that is
// pure information.
//
// So it is asserted at both ends and in both directions of travel:
//
//   - ON THE WIRE, in the message the settlement agent acted on. That is the
//     copy the clearing house carried, so it also says the relay left the
//     document alone (csm.relay replaces the header and nothing else).
//   - IN THE BOOKS, in the descriptions of the two postings that move a
//     customer's money. payment.ReturnPaymentTx writes the reason into both,
//     which is what makes the return legible on a statement months later.
//
// The CENTRAL BANK's own leg is asserted NOT to carry it, and that is not
// pedantry: it is the sentence in payment.ReturnReason's doc that would
// otherwise be wrong. ReturnPaymentTx describes the reserve reversal as the
// settlement it is, so the reason reaches two of the three postings and not
// three.
func TestTheReturnsReasonTravelsFromTheAskingBankToTheLedgers(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	const text = "the beneficiary account was closed"
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, text)
	h.drain(t)

	tx := h.returnSentTo(t, h.cfg.CentralBankBIC).PmtRtr.TxInf[0]
	if tx.OrgnlTxId != string(p.ID) {
		t.Errorf("the return names %q, want the payment it is about (%s)", tx.OrgnlTxId, p.ID)
	}
	switch {
	case tx.RtrRsnInf == nil:
		t.Fatal("the return carries no reason at all; RtrRsnInf is what a pacs.004 is for")
	case tx.RtrRsnInf.Rsn.Cd == nil:
		t.Fatalf("the return carries no reason CODE: %+v", tx.RtrRsnInf.Rsn)
	case *tx.RtrRsnInf.Rsn.Cd != iso20022.ReturnReasonClosedAccountNumber:
		t.Errorf("the return says %s, want AC04 — the code the bank asked for", *tx.RtrRsnInf.Rsn.Cd)
	}
	if tx.RtrRsnInf.AddtlInf != text {
		t.Errorf("the return's free text is %q, want %q — the part no code can say", tx.RtrRsnInf.AddtlInf, text)
	}

	want := string(iso20022.ReturnReasonClosedAccountNumber) + ": " + text
	for _, leg := range []struct {
		what string
		who  payment.ParticipantID
		key  string
	}{
		{"the payer's refund", h.debtorPID, ":return-debit"},
		{"the payee's clawback", h.creditorPID, ":return-credit"},
	} {
		if got := h.postingByKey(t, leg.who, string(p.ID)+leg.key).Description; !strings.Contains(got, want) {
			t.Errorf("%s is described as %q, want it to carry %q", leg.what, got, want)
		}
	}

	// The reserve reversal is described as what it is, and carries no reason.
	cb, err := h.net.CentralBank().GetTransactionByIdempotencyKey(context.Background(), string(p.ID)+":return-settle")
	if err != nil {
		t.Fatalf("no reserve reversal for %s: %v", p.ID, err)
	}
	if strings.Contains(cb.Description, string(iso20022.ReturnReasonClosedAccountNumber)) {
		t.Errorf("the reserve reversal is described as %q; the reason reaches the two customer legs, not this one", cb.Description)
	}
}

// TestAProprietaryReturnReasonReachesTheLedgersToo is the other arm of the
// choice, and it is why payment.ReturnReason reads both.
//
// ReturnReasonChoice is an xsd:choice with a code and a PROPRIETARY text, and
// iso20022 refuses a return carrying neither. A code is what this system's own
// banks send, so the proprietary arm is only reachable from a counterparty that
// uses one — which is exactly what a real network is full of, since the arm
// exists for reasons the external code set has no member for. A settlement
// agent that read only the code would describe such a return as "returned" and
// throw away the only thing the sender said about it.
//
// Injected, because no actor in this mesh emits one: payment.ReturnMessage
// takes an iso20022.ReturnReason and puts it in Cd. The free text is left empty
// as well, so this covers the join's other arm at the same time — a reason with
// a code and no text.
func TestAProprietaryReturnReasonReachesTheLedgersToo(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "",
		payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-prtry", Now: testTime})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	prtry := "SCHEME-LOCAL-DISPUTE"
	rsn := &env.Document.(*iso20022.Pacs004).PmtRtr.TxInf[0].RtrRsnInf.Rsn
	rsn.Cd, rsn.Prtry = nil, &prtry
	if err := h.mesh.send(h.creditorBIC, h.cfg.ClearingHouseBIC, env); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.drain(t)

	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("status = %v, want Returned", got.Status)
	}
	if got := h.postingByKey(t, h.debtorPID, string(p.ID)+":return-debit").Description; !strings.Contains(got, prtry) {
		t.Errorf("the payer's refund is described as %q, want it to carry the proprietary reason %q", got, prtry)
	}
}

// TestAReturnThatNamesNoPaymentCannotBeAnswered pins the one refusal in this
// flow that reaches nobody, so that its cost is recorded rather than
// rediscovered.
//
// A pacs.004 may refer back by OrgnlEndToEndId alone: OrgnlTxId is optional in
// the schema and iso20022's ReturnTransaction.validate accepts either. This
// system identifies payments by OrgnlTxId, so such a message names no payment
// this network holds. The sender is told nothing and the payment is untouched;
// what is dead-lettered is the answer.
//
// # WHERE it dies has moved, and half the limit is closed
//
// This test used to record the settlement agent failing to BUILD the refusal:
// it quoted the payment id as the end-to-end reference as well as the
// transaction id, so a message with neither left the report with nothing to
// refer back by and the codec refused it. That double-quoting was itself the
// defect — it broke the convention every other per-payment status follows, so a
// bank could not match an ordinary answer against what it sent — and
// centralBank.answer now quotes the reference the RETURNING BANK gave
// (returnedEndToEnd).
//
// So the refusal is built and sent, and dies one hop later instead: the
// clearing house is what turns an answer back into a payment, and it looks up
// by OrgnlTxId, which is the element this message does not have. The outcome
// for the returning bank is exactly what it was — told nothing — and the second
// half of the limit is the one the old comment named: the clearing house would
// have to resolve a payment by its end-to-end reference, which is a lookup this
// system does not have.
//
// No actor in this mesh emits one — payment.ReturnMessage always writes
// OrgnlTxId — so it is injected.
func TestAReturnThatNamesNoPaymentCannotBeAnswered(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "no transaction id",
		payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-noid", Now: testTime})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	tx := &env.Document.(*iso20022.Pacs004).PmtRtr.TxInf[0]
	tx.OrgnlTxId, tx.OrgnlEndToEndId = "", "E2E-ONLY"
	if err := h.mesh.send(h.creditorBIC, h.cfg.ClearingHouseBIC, env); err != nil {
		t.Fatalf("send: %v", err)
	}

	answered := h.statusesSentTo(h.creditorBIC)
	err = h.drainErr(t)
	if err == nil {
		t.Fatal("Drain was clean; a return the settlement agent could neither execute nor answer went unreported")
	}
	if !strings.Contains(err.Error(), "naming no payment") || !strings.Contains(err.Error(), string(h.cfg.ClearingHouseBIC)) {
		t.Errorf("the dead letter %q is not the clearing house unable to say which payment the answer is about", err)
	}
	if got := h.statusesSentTo(h.creditorBIC); got != answered {
		t.Errorf("the bank was sent %d statuses about a return that names no payment; the answer cannot be built", got-answered)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Errorf("the payment is %v; nothing was returned", got.Status)
	}
}

// TestARefusedReturnNamesTheSettlementAgentAsTheOriginator is the one hop in
// this system where the sender of a status and the institution that decided it
// are different, and the message has to say so.
//
// The clearing house passes the settlement agent's answer about a return
// straight back to the bank that asked for one. Its own doc says it decides
// nothing there — it addresses the answer and adds nothing — and until
// MessageContext could express that, the pacs.002 it built stamped ITSELF as
// the originator of somebody else's refusal. Orgtr exists precisely to stop
// that (iso20022.StatusReasonInformation, EPC AT-R002): a returning bank
// reading it would open an investigation with a clearing house that never
// looked at its request.
//
// The refusal is provoked with a count the sender's own header contradicts,
// which is one of the two things centralBank.receiveReturn refuses outright.
// Any of its refusals would do; what this test is about is the ELEMENT, not the
// reason.
func TestARefusedReturnNamesTheSettlementAgentAsTheOriginator(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	// The returning bank on a push is the PAYEE's bank: the one that received
	// the original instruction. See returnerOf.
	env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "account closed",
		payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-x", Now: testTime})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	// A header that claims two transactions over one. The settlement agent
	// refuses the whole message rather than acting on the survivor.
	env.Document.(*iso20022.Pacs004).PmtRtr.GrpHdr.NbOfTxs = "2"
	if err := h.mesh.send(h.creditorBIC, h.cfg.ClearingHouseBIC, env); err != nil {
		t.Fatalf("send: %v", err)
	}
	h.drain(t)

	status := h.lastStatusTo(t, h.creditorBIC)
	if n := len(status.FIToFIPmtStsRpt.TxInfAndSts); n != 1 {
		t.Fatalf("the answer carries %d transactions, want 1", n)
	}
	tx := status.FIToFIPmtStsRpt.TxInfAndSts[0]
	if tx.TxSts != iso20022.TransactionStatusRejected {
		t.Fatalf("the return was answered %v, want RJCT — this test is about a refusal's originator", tx.TxSts)
	}
	if tx.StsRsnInf == nil || tx.StsRsnInf.Orgtr == nil || tx.StsRsnInf.Orgtr.Id == nil || tx.StsRsnInf.Orgtr.Id.OrgId == nil {
		t.Fatalf("the refusal names no originator: %#v", tx.StsRsnInf)
	}
	if got := tx.StsRsnInf.Orgtr.Id.OrgId.AnyBIC; got != h.cfg.CentralBankBIC {
		t.Fatalf("Orgtr = %q, want the settlement agent %q — the clearing house relayed this refusal and did not make it",
			got, h.cfg.CentralBankBIC)
	}
	// The header still says the clearing house SENT it, which is the other half
	// of the distinction: collapsing the two is the bug, not either value.
	env2, err := iso20022.Unmarshal(h.lastMessageOfTypeTo(t, h.creditorBIC, "pacs.002.001.10"))
	if err != nil {
		t.Fatalf("re-parsing the answer: %v", err)
	}
	if got := env2.AppHdr.Fr.FIId.FinInstnId.BICFI; got != h.cfg.ClearingHouseBIC {
		t.Fatalf("the answer was sent by %q, want the clearing house %q", got, h.cfg.ClearingHouseBIC)
	}
}

// TestTheSettlementAgentsAnswerQuotesTheReferenceTheBankSent is the convention
// every per-payment status in this mesh follows, asserted at the one actor that
// had drifted off it.
//
// A bank matches an answer to its instruction by comparing what it SENT with
// what came back. centralBank.answer quoted the payment id as the end-to-end
// reference as well as the transaction id, which matches nothing any bank ever
// sent — csm.endToEndOf is the same convention on the other side of the mesh,
// and payment's own helper is where it comes from.
//
// Both cases, because the convention has two halves and only one of them is
// visible in a fixture with no client reference: a payment that quotes one gets
// it back verbatim, and a payment that quotes none gets NOTPROVIDED. The second
// is the EPC's convention for "the payer gave no reference" and is what the
// pacs.008 carried on the way out.
func TestTheSettlementAgentsAnswerQuotesTheReferenceTheBankSent(t *testing.T) {
	for _, tc := range []struct{ name, e2e, want string }{
		{"a payment with a client reference", "INV-42", "INV-42"},
		{"a payment with none", "", notProvided},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMeshHarness(t)
			req := h.creditTransferRequest(t)
			req.EndToEndID = tc.e2e
			submitted, err := h.mesh.Submit(context.Background(), req)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			p := h.settle(t, submitted)

			// A refusal rather than a success, because a pacs.002 that is not a
			// rejection is the same shape in this element and the refusal is the
			// case a bank actually has to match against an exception queue.
			env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "account closed",
				payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-ref", Now: testTime})
			if err != nil {
				t.Fatalf("ReturnMessage: %v", err)
			}
			env.Document.(*iso20022.Pacs004).PmtRtr.GrpHdr.NbOfTxs = "2"
			if err := h.mesh.send(h.creditorBIC, h.cfg.ClearingHouseBIC, env); err != nil {
				t.Fatalf("send: %v", err)
			}
			h.drain(t)

			status := h.lastStatusTo(t, h.creditorBIC)
			tx := status.FIToFIPmtStsRpt.TxInfAndSts[0]
			if tx.OrgnlTxId != string(p.ID) {
				t.Fatalf("OrgnlTxId = %q, want the payment id %q", tx.OrgnlTxId, p.ID)
			}
			if tx.OrgnlEndToEndId != tc.want {
				t.Fatalf("OrgnlEndToEndId = %q, want %q — the payment id in this element matches nothing the bank sent",
					tx.OrgnlEndToEndId, tc.want)
			}
		})
	}
}

// TestTheSettlementAgentCannotAnswerYesWithAReason is the guard bank.answer has
// and centralBank.answer had drifted away from.
//
// A pacs.002 carries StsRsnInf only for a rejection (payment.statusReasonOf), so
// a cause passed beside SettlementCompleted would set a code and a text that the
// builder then silently drops — a message saying everything is fine, with the
// reason it was not deleted on the way out. No handler does it today; this is
// what stops the next one, and it is asserted directly because there is no
// message that provokes it.
func TestTheSettlementAgentCannotAnswerYesWithAReason(t *testing.T) {
	h := newMeshHarness(t)
	cb := &centralBank{m: h.mesh, ops: h.net, bic: h.cfg.CentralBankBIC}

	err := cb.answer(h.cfg.ClearingHouseBIC,
		payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided},
		notProvided, "cyc_x",
		iso20022.TransactionStatusSettlementCompleted,
		payment.ErrCycleNotFound)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	// The cycle id is invented, so whatever the clearing house makes of this
	// message it will have nothing to look up. The dead letter is taken rather
	// than asserted on: what is under test is the BYTES this actor put on the
	// wire, and the recipient's opinion of them is another test's business.
	_ = h.drainErr(t)

	status := h.lastStatusTo(t, h.cfg.ClearingHouseBIC)
	tx := status.FIToFIPmtStsRpt.TxInfAndSts[0]
	if tx.TxSts != iso20022.TransactionStatusRejected {
		t.Fatalf("an answer built with a cause reports %v, want RJCT — the reason is dropped on any other status", tx.TxSts)
	}
	if tx.StsRsnInf == nil || tx.StsRsnInf.Rsn.Cd == nil {
		t.Fatalf("the rejection carries no reason code: %#v", tx.StsRsnInf)
	}
}
