package mesh

import (
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
// TestTheSettlementChainIsTwoMessages the cut-off. It is what PINS the routing
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
// actor can say. The clearing house carries the return and takes it into no
// cycle: TestAReturnIsExecutedByTheCentralBank measures that it posts nothing at
// all while doing so.
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
	first := h.messagesFrom(before)[0]
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

	err := h.drainErr(t)
	if !errors.Is(err, payment.ErrInvalidStateTransition) {
		t.Fatalf("Drain = %v, want the illegal transition as a dead letter", err)
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
		name   string
		doctor func(*iso20022.Pacs004)
	}{
		{
			// Two returns in one message, correctly counted. Nothing about it
			// is malformed; it is simply more than this actor acts on.
			name: "two returns in one message",
			doctor: func(d *iso20022.Pacs004) {
				d.PmtRtr.TxInf = append(d.PmtRtr.TxInf, d.PmtRtr.TxInf[0])
				d.PmtRtr.GrpHdr.NbOfTxs = "2"
			},
		},
		{
			// One transaction arrived and the sender says there were two. That
			// is a transaction lost in transit, which for a return is a payer
			// who never gets their money back.
			name: "a count that does not match what arrived",
			doctor: func(d *iso20022.Pacs004) {
				d.PmtRtr.GrpHdr.NbOfTxs = "2"
			},
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
			if got := h.lastStatusTo(t, h.creditorBIC); !strings.Contains(statusText(got), "one payment per message") {
				t.Errorf("the refusal does not say what this agent refused: %q", statusText(got))
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
