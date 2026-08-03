package mesh

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// A return is routed to the CENTRAL BANK, not the CSM. payment.SettleReturnTx
// moves reserves, and the actor that owns reserve movement is the settlement
// agent — payment/doc.go already records that returns settle immediately rather
// than through a later R-cycle, so a return genuinely is a settlement act here.
//
// The clearing house's empty book set is the other half, and it says MORE than
// it used to: this actor now runs three hops on this flow rather than two, and
// still posts nothing. See TestWhichBooksAReturnReaches.
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

// TestTheMessagesAReturnPutsOnTheWire names the conversation, the way
// TestTheMessagesACutOffPutsOnTheWire names the cut-off and
// TestTheCreditTransferChainIsFourMessages the push. It is what PINS the
// routing decisions this flow makes; the status assertions elsewhere in this
// file cannot see who sent what to whom.
//
// It was TestTheReturnChainIsFourMessages, and the name outran the code the
// moment a return stopped being one institution's act: seven messages, of which
// only four are the chain the name described. Renamed for the reason every other
// rename in this package had — the name is the string a failure prints and a
// reader greps for, and one that claims more than it measures is worse than
// none.
//
// # Seven messages, and each one is somebody having to be told
//
//	payee's bank  --pacs.004-->  clearing house  --pacs.004-->  central bank
//	                             both banks      <--camt.053--  central bank
//	payee's bank  <--pacs.002--  clearing house  <--pacs.002--  central bank
//	payer's bank  <--pacs.004--  clearing house
//
// The PAYEE's bank starts it, because a return is sent by the bank that
// RECEIVED the original instruction — the SEPA rule book's own division, and
// the opposite end from the one that submitted. See returnerOf, and
// TestAReturnedCollectionIsSentByThePayersBank for the other direction.
//
// It goes to the CENTRAL BANK, because a return moves reserves. That is the
// whole argument for this flow existing at the settlement agent rather than at
// the clearing house, and payment/doc.go records the consequence: returns settle
// immediately here rather than being netted in a later R-cycle.
//
// It goes THROUGH the clearing house rather than bank-to-central-bank directly.
// A member bank in this mesh addresses the clearing house and nothing else, and
// the routing table lives in one actor — which is why RC01 is a thing only that
// actor can say. That claim is this test's and not the book assertions': point
// the returning bank straight at the central bank and the return still happens,
// and every book assertion stays green, because an actor that is skipped touches
// no book either.
//
// The two CAMT.053s are the reserve movement stated to the two banks whose
// accounts moved, exactly as at a cut-off — a return is a cut-off of one payment
// as far as reserves are concerned. Each books its own mirror leg from its own.
//
// The PAYER's bank is now in TWO of the seven, and it used to be in none of
// four. It is sent the pacs.004 because it holds the leg the returning bank does
// not — the refund into its own customer's account — and it is sent that message
// LAST, out of the handler of the settlement agent's ACSC, because the clearing
// house holds it until the return is final. See csm.relayReturn.
//
// # It is a SET, plus the orderings that are actually forced
//
// The tap fires in Mesh.dispatch, on the RECEIVING actor's goroutine, so what
// this test observes is handling order and not send order. Messages to different
// actors race, and a positional assertion over all seven would be flaky rather
// than strict. Three relations survive that:
//
//   - the returning bank's pacs.004 is handled FIRST, because every other
//     message here is sent from the handler of one before it;
//   - the central bank's pacs.002 to the clearing house is handled BEFORE both
//     messages the clearing house sends, because both are sent from its handler;
//   - the PAYER's BANK's camt.053 is handled before the pacs.004 addressed to
//     that same bank. This one is not a chain argument and it is the
//     load-bearing one.
//
// # Why that pair CAN be asserted when the set cannot be ordered
//
// Not merely "both go to one actor" — two messages racing into one inbox from
// two goroutines would arrive in either order. What forces this pair is a
// happens-before chain:
//
//  1. Mesh.send pushes onto the target's queue SYNCHRONOUSLY, in the sender's
//     own goroutine (mesh.sendRaw).
//  2. centralBank.receiveReturn calls advise before answer, so both camt.053s
//     are pushed into the banks' queues before the pacs.002 is pushed into the
//     clearing house's.
//  3. The relayed pacs.004 does not exist until the clearing house HANDLES that
//     pacs.002, which cannot happen before step 2 pushed it.
//  4. So the camt.053 is already in the payer's bank's queue before the relay is
//     created, and Mesh.run is one goroutine popping that queue FIFO.
//
// A reader who tries to falsify this by simply swapping advise and answer will
// find the test still passes, and should not conclude the assertion is weak.
// Swapping makes the pair a RACE rather than an inversion — the central bank
// pushes the camt.053 while the clearing house is being scheduled to produce the
// relay — and the central bank wins that race almost always. Inverting it takes
// a delay between the answer and the advice, and with one the assertion fails
// every run. That is the same trap TestTheMessagesACutOffPutsOnTheWire records,
// and it was verified here the same way.
//
// What it pins is why centralBank.advise sends the statements before it answers.
// The payer's bank receives the reserves back, so its camt.053 CREDITS the
// clearing suspense that its refund then draws on; the relayed pacs.004 is what
// makes it draw. Reverse the two and that bank pays its customer out of a
// suspense the return has not yet credited — which commits, because suspense is
// a Liability and the ledger does not guard those, and which for that interval
// has the bank's own books saying it lent its customer the money.
//
// The PAYEE's bank receives a pair too — its own camt.053 and the ACSC — and the
// same chain orders it. It is not asserted because nothing depends on it: that
// bank posted its clawback before any of this and has nothing to post from
// either message. What remains genuinely undetermined is the INTERLEAVING across
// actors.
func TestTheMessagesAReturnPutsOnTheWire(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	before := h.messagesSeen()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.drain(t)

	type hop struct {
		from, to iso20022.BIC
		msgDef   string
	}
	asked := hop{h.creditorBIC, h.cfg.ClearingHouseBIC, "pacs.004.001.09"}
	relayed := hop{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "pacs.004.001.09"}
	answer := hop{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"}
	told := hop{h.cfg.ClearingHouseBIC, h.creditorBIC, "pacs.002.001.10"}
	// The two the PAYER's bank receives, and the pair whose order is forced. The
	// statement credits the suspense; the relayed return is what makes the refund
	// draw on it. See centralBank.advise.
	payerStatement := hop{h.cfg.CentralBankBIC, h.debtorBIC, "camt.053.001.08"}
	payerReturn := hop{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.004.001.09"}
	want := map[hop]int{
		asked:          1,
		relayed:        1,
		answer:         1,
		told:           1,
		payerStatement: 1,
		payerReturn:    1,
		{h.cfg.CentralBankBIC, h.creditorBIC, "camt.053.001.08"}: 1,
	}

	got := map[hop]int{}
	order := map[hop]int{}
	seen := h.messagesFrom(before)
	for i, m := range seen {
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			t.Fatalf("message %d does not parse: %v", i, err)
		}
		hp := hop{m.from, m.to, env.AppHdr.MsgDefIdr}
		got[hp]++
		order[hp] = i
		if i == 0 && hp != asked {
			t.Errorf("the return's first message is %s -> %s (%s), want the returning bank's %s -> %s (%s)",
				hp.from, hp.to, hp.msgDef, asked.from, asked.to, asked.msgDef)
		}
	}
	if !maps.Equal(got, want) {
		t.Fatalf("the return put %v on the wire, want %v", got, want)
	}
	// Chain pairs. Both of the clearing house's outbound messages are sent from
	// the handler of the central bank's answer, so neither can precede it.
	for _, later := range []struct {
		what string
		h    hop
	}{{"its status to the bank that asked", told}, {"the return it relayed onward", payerReturn}} {
		if order[answer] > order[later.h] {
			t.Errorf("the clearing house handled %s at %d and the central bank's answer at %d; "+
				"the first is sent from the second's handler and cannot precede it",
				later.what, order[later.h], order[answer])
		}
	}
	// The load-bearing pair, and not a chain argument: both are handled by the
	// PAYER'S BANK, one goroutine popping one FIFO queue, so their order here is
	// that bank's handling order. The statement credits the clearing suspense;
	// the relayed return is what makes the refund draw on it. The other way round
	// and that bank pays its customer out of a suspense the return has not
	// credited yet. centralBank.receiveReturn advises before it answers for
	// exactly this reason.
	if order[payerStatement] > order[payerReturn] {
		t.Errorf("the payer's bank handled the relayed return at %d and its own camt.053 at %d; "+
			"the statement credits the suspense the refund draws on and must come first",
			order[payerReturn], order[payerStatement])
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
// # The payee's bank used to be told NOTHING, and now it is told twice
//
// That silence was this test's sharpest assertion and it was a measurement of a
// system in which the settlement agent clawed the biller back inside its own
// unit of work: the bank whose customer lost the money learned by reading a
// payment row it shared with everybody. The old comment said as much — "a real
// network would have to tell it" — and that is what Task 16e did.
//
// It is told twice, and the two messages are two different things:
//
//   - a camt.053, because its settlement account is one of the two the reserve
//     reversal moved. On a pull that bank is the one PAYING the reserves back.
//   - the pacs.004 itself, relayed by the clearing house once the return is
//     final, because on a pull the CREDITOR's bank holds the clawback — and it
//     cannot refuse it. The payer's refund right is unconditional, so a biller
//     who cannot fund it goes overdrawn or leaves a Returns Receivable behind.
//     See payment.PostReturnLegTx.
//
// So the two directions are no longer a silence and a message. They are the same
// seven messages with the banks swapped, which is what makes returnerOf a rule:
// the SAME bank composes the pacs.004 in both, and it is the far end from the
// submitter both times. What flips is only which of the two legs it is holding.
//
// The clawback is asserted at the biller's own balance, because that is where a
// pull return differs from a push and the message count cannot see it.
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
	if got := h.messagesSeen() - before; got != 7 {
		t.Errorf("returning a collection took %d messages, want 7", got)
	}
	// The payee's bank is told, and told the two things it has a use for. Both
	// are asserted rather than counted, because "it received two messages" would
	// pass on a system that sent it the answer twice.
	var statements, returns int
	for _, m := range h.messagesFrom(before) {
		if m.to != h.creditorBIC {
			continue
		}
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			t.Fatalf("a message to the payee's bank does not parse: %v", err)
		}
		switch env.AppHdr.MsgDefIdr {
		case "camt.053.001.08":
			statements++
		case "pacs.004.001.09":
			returns++
		default:
			t.Errorf("the payee's bank was sent a %s during the return; it asked for nothing",
				env.AppHdr.MsgDefIdr)
		}
	}
	if statements != 1 || returns != 1 {
		t.Errorf("the payee's bank got %d statements and %d returns, want one of each — "+
			"the statement moves its reserve mirror and the return makes it claw its biller back",
			statements, returns)
	}
	// And the clawback actually landed, at the bank that could not refuse it.
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != 0 {
		t.Errorf("the biller holds %d after the refund was returned, want 0", got)
	}
}

// TestABankRefusesToReturnAPaymentThatHasNotSettled is the guard the returning
// bank makes before any message exists, and the reason it is not left to the
// settlement agent.
//
// payment.PostReturnLegTx refuses it too, with ErrInvalidStateTransition — and
// that sentinel is the one payment's reasonTable gives the empty code, because
// it describes a defect in this system rather than a judgement about anyone's
// instruction. An actor that received it must dead-letter it, so a return sent
// for a payment that has not settled would be answered by nobody at all and the
// operator who asked for it would hear nothing. Refused here, it is the caller's
// answer, and it names the status — which is the whole of what the handler's own
// guard adds over the domain's.
//
// And nothing goes on the wire, which is the second half of the claim: a
// refusal that had already sent the pacs.004 would have the settlement agent
// settling reserves for a payment this bank knew was not returnable. That half
// got sharper rather than staler, because the settlement agent no longer reads
// the payment at all: it acts on what the message says, so nothing downstream
// would have caught this one.
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
// twice. The second copy names a payment whose return this network has already
// settled, SettleReturnTx answers ErrReturnAlreadySettled, and turning that into
// a pacs.002 would tell the returning bank that a return which in fact happened
// was rejected — MS03, through ReasonFor's fallback, which is what reasonTable's
// empty code exists to forbid. Dead letter, and no status at all.
//
// The SENTINEL changed here and the discrimination did not, which is the thing
// worth pinning. It used to be ErrInvalidStateTransition, off the payment row
// the settlement agent transitioned. That actor holds no payment row on this
// path any more — it acts on the message — so what catches the redelivery is
// the idempotency key on the reserve reversal, in the central bank's own
// ledger. Both sentinels carry the empty code in reasonTable, for the same
// reason: they describe this system's state and not the sender's message.
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
	if err := h.drainErr(t); !errors.Is(err, payment.ErrReturnAlreadySettled) {
		t.Errorf("Drain = %v, want the already-settled return as a dead letter", err)
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
//     customer's money. payment.PostReturnLegTx writes the reason into both —
//     the clawback in one bank's book, the refund in the other's — which is
//     what makes the return legible on a statement months later.
//
// The CENTRAL BANK's own leg is asserted NOT to carry it, and that is not
// pedantry: it is the sentence in payment.ReturnReason's doc that would
// otherwise be wrong. payment.SettleReturnTx describes the reserve reversal as
// the settlement it is, so the reason reaches the two customer legs and not the
// third posting.
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
		{"the payer's refund", h.debtorPID, ":return-refund"},
		{"the payee's clawback", h.creditorPID, ":return-claw"},
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
//
// # The injection now skips a half, and the payment stops at Settled
//
// This used to assert Returned, and the change is a consequence of the return
// becoming a conversation rather than a weakening of the test. Sending a
// doctored pacs.004 into the clearing house puts the message on the wire WITHOUT
// the returning bank's own act behind it — that bank never posted its clawback,
// because bank.returnPayment is what does that and this test does not call it.
// So the far leg lands, the near one never existed, and the payment cannot reach
// Returned: it takes both, and payment.PostReturnLegTx is what says so.
//
// That leaves this test measuring exactly what it is named for — the reason
// reaching a customer's ledger — and it is asserted on the one leg the message
// really did cause. A test that still wanted Returned here would have to fake
// the returning bank's posting, which is the fixture asserting the system's
// behaviour to itself.
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

	if got := h.postingByKey(t, h.debtorPID, string(p.ID)+":return-refund").Description; !strings.Contains(got, prtry) {
		t.Errorf("the payer's refund is described as %q, want it to carry the proprietary reason %q", got, prtry)
	}
	// One leg, so the row stops short of Returned. Asserted rather than left
	// out, because "the status did not change" and "the far bank never got the
	// message" would otherwise look the same from here.
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Errorf("status = %v, want Settled — this return had no returning bank behind it, so one leg is missing",
			got.Status)
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
// by OrgnlTxId, which is the element this message does not have.
//
// WHAT refuses it moved too, and that one is a money guard rather than a lookup.
// The settlement agent used to fail on the payment row it could not find; it
// reads no payment row now, so payment.ReadReturn refuses the message instead —
// the reserve reversal's idempotency key is derived from the payment id, and an
// empty one would move reserves under a key every nameless return shares. See
// TestReadReturnRefusesATransactionThatNamesNoPayment. The outcome
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

// TestAPayeeWhoSpentTheMoneyStopsTheReturnOnTheWire is the observable half of
// the return's one rule — a bank can refuse a leg only if it posts it before it
// sends — and it is a refusal a push could not produce at all until Task 16e.
//
// payment.TestAPayeeWhoSpentTheMoneyStopsTheReturnBeforeItIsSent measures the
// domain call. This measures what the MESH does with it, which is three things
// that test cannot see: the caller of Mesh.Return gets the refusal, NOTHING goes
// on the wire, and the code the refusal carries is AM04 about the returning
// bank's OWN customer.
//
// That last one is the part worth stating. AM04 in this system has meant a
// payer's empty account (a debtor's bank refusing a collection) and a bank's
// empty reserve (the settlement agent refusing a cut-off). This is a third
// speaker: the bank that RECEIVED a credit transfer, saying its own beneficiary
// has spent the money and cannot be made to give it back. No bank force-takes
// money from a customer who has spent it — and before the returning bank held a
// leg of its own, this system had no way to say so.
//
// Nothing on the wire is the half that makes the refusal binding rather than
// merely reported. A bank that had sent the pacs.004 first and discovered the
// shortfall afterwards would have the reserves already reversed and no way back:
// the settlement agent does not read the payment and would have no reason to
// refuse.
func TestAPayeeWhoSpentTheMoneyStopsTheReturnOnTheWire(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)
	h.spendTheCredit(t)
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != 0 {
		t.Fatalf("the payee holds %d after spending it, want 0 — this test is about a clawback that cannot be funded", got)
	}

	before := h.messagesSeen()
	err := h.returnErr(p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	if !errors.Is(err, deposit.ErrInsufficientAvailable) {
		t.Fatalf("Return = %v, want the payee's own bank refusing to fund the clawback", err)
	}
	if got := payment.ReasonFor(err); got != iso20022.StatusReasonInsufficientFunds {
		t.Errorf("the refusal maps to %q, want AM04 — this is a beneficiary who cannot repay", got)
	}

	h.drain(t)
	if got := h.messagesSeen(); got != before {
		t.Errorf("a refused return put %d messages on the wire, want none", got-before)
	}
	// And nothing moved, which is what "refused by not posting" means. The
	// payment is where it was, the payee is not overdrawn, and the suspense that
	// would have held the clawback is flat.
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Errorf("the refused return moved the payment to %v", got.Status)
	}
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != 0 {
		t.Errorf("the payee holds %d after the refused return, want 0", got)
	}
	if got := h.suspense(t, h.creditorPID); got != 0 {
		t.Errorf("the payee's bank's suspense holds %d after the refused return, want 0", got)
	}
}

// TestARefusedReturnUnwindsTheReturningBanksLeg is the price of that binding
// refusal, and the reason payment.ReverseReturnLegTx exists at all.
//
// The returning bank posts BEFORE it sends, so by the time an answer arrives it
// has already moved its own customer's money. An RJCT leaves that posting
// standing against a return that will not happen — a customer looking at a
// balance nobody can explain — so this handler unwinds it. Before Task 16e there
// was nothing to unwind and this test could not have been written.
//
// # Getting a genuine RJCT takes a real shortfall, and the PULL is where one fits
//
// The refusal has to come from the settlement agent AFTER a returning bank has
// really posted, so a doctored message will not do: an injected pacs.004 has no
// returning bank behind it. What is left is the one thing SettleReturnTx
// decides — whether the CREDITOR's bank can cover the reserve reversal — and the
// direction that reaches it is the pull:
//
//   - a collection settles, so the payee's bank has the reserves and the payee
//     has the money;
//   - the payee spends it, which takes both away;
//   - the payer's bank returns the collection. It holds the REFUND, which is
//     unconditional and always postable, so it posts and sends;
//   - the central bank finds the payee's bank short and answers AM04.
//
// On a push the same shortfall would have stopped the return one step earlier,
// at the clawback — see TestAPayeeWhoSpentTheMoneyStopsTheReturnOnTheWire — which
// is the two halves of the rule meeting.
//
// # What the refusal must leave behind
//
// The payer is back where the collection left them, the refund posting is in the
// book marked Reversed rather than deleted, and the payee's bank is never sent
// the pacs.004 at all. That last one is what the clearing house holding the
// message buys: a bank that had been relayed it on arrival would have clawed its
// biller back for a return the network then refused, with no message in this
// flow that would ever tell it.
func TestARefusedReturnUnwindsTheReturningBanksLeg(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledCollection(t)
	h.spendTheCredit(t)
	// Where everybody is before the return: the payer paid the collection and
	// was paid it straight back by the biller spending it, so they are whole and
	// the biller is empty — and so is the biller's bank's settlement account.
	if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding {
		t.Fatalf("the payer holds %d before the return, want %d", got, harnessFunding)
	}

	before := h.messagesSeen()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonNoMandate, "the debtor disputes the mandate")
	h.drain(t)

	// Refused, and refused with the code a reserve shortfall carries.
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusRejected)
	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonInsufficientFunds)

	// The payment is exactly where it was.
	got := h.payment(t, p.ID)
	if got.Status != payment.Settled {
		t.Errorf("the refused return left the payment at %v, want Settled", got.Status)
	}
	if got.ReturnClawbackTx != "" {
		t.Errorf("the payee's bank posted a clawback (%s) for a return that was refused", got.ReturnClawbackTx)
	}
	// The refund is REVERSED rather than absent. The id stays on the payment
	// because this bank did post; the ledger is where "it no longer stands" is
	// recorded. See payment.ReverseReturnLegTx.
	if got.ReturnRefundTx == "" {
		t.Fatal("the returning bank posted no refund at all; this test is about unwinding one")
	}
	refund := h.postingByKey(t, h.debtorPID, string(p.ID)+":return-refund")
	if refund.Status != ledger.Reversed {
		t.Errorf("the refund is %v, want Reversed — an RJCT leaves no leg standing", refund.Status)
	}

	// And the money is back where the refused return found it.
	if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding {
		t.Errorf("the payer holds %d after the refused return, want %d", got, harnessFunding)
	}
	if got := h.suspense(t, h.debtorPID); got != 0 {
		t.Errorf("the returning bank's suspense holds %d after the unwind, want 0", got)
	}

	// The other bank was never told, because the clearing house held the message
	// until it had an ACSC and never got one.
	for _, m := range h.messagesFrom(before) {
		if m.to != h.creditorBIC {
			continue
		}
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			t.Fatalf("a message to the payee's bank does not parse: %v", err)
		}
		if env.AppHdr.MsgDefIdr == "pacs.004.001.09" {
			t.Errorf("the payee's bank was relayed the return of %s, which the settlement agent refused", p.ID)
		}
	}
}
