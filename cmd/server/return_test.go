package main

import (
	"bytes"
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

// A return is routed to the CENTRAL BANK, not the CSM: payment.SettleReturnTx moves
// reserves, and the actor that owns reserve movement is the settlement agent.
//
// The clearing house's book set is the other half — that this actor POSTS nothing on
// a flow it runs three hops of. It holds its own book because being told a return
// went through is a fact it records on its own copy, and ClearingHouseBook is a
// row-book with no accounts in it.
func TestAReturnIsExecutedByTheCentralBank(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)

	h.rec.reset()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.work(t)

	got := h.payment(t, p.ID)
	if got.Status != payment.Returned {
		t.Fatalf("status = %v, want Returned", got.Status)
	}
	assertBooksTouched(t, "the clearing house, carrying a return",
		h.booksTouchedBy(h.cfg.ClearingHouseBIC), []ledger.BookID{payment.ClearingHouseBook})
}

// TestTheMessagesAReturnPutsOnTheWire names the conversation and PINS its routing
// decisions; the status assertions elsewhere cannot see who sent what to whom.
//
// Seven messages, of which four are the chain a reader might expect:
//
//	payee's bank  --pacs.004-->  clearing house  --pacs.004-->  central bank
//	                             both banks      <--camt.053--  central bank
//	payee's bank  <--pacs.002--  clearing house  <--pacs.002--  central bank
//	payer's bank  <--pacs.004-->  clearing house
//
// The PAYEE's bank starts it, because a return is sent by the bank that RECEIVED the
// original instruction. It goes to the CENTRAL BANK because a return moves reserves,
// and THROUGH the clearing house because a member bank uploads a pacs.004 there and
// nowhere else — a claim only this test makes, since an institution that is skipped
// touches no book either. The PAYER's bank is in two of the seven: it holds the leg
// the returning bank does not, and is told LAST, the clearing house holding the
// relay until the return is final.
//
// It is a SET plus the orderings that are forced. The tap fires when a file CROSSES,
// so what is observed is delivery order, and files in two institutions' queues have
// no order between them.
//
// Three relations survive: the returning bank's pacs.004 is uploaded FIRST; the
// agent's pacs.002 crosses before both files the clearing house queues; and the
// PAYER's BANK's camt.053 crosses before the pacs.004 addressed to that same bank.
// The last is the load-bearing one, and it is not a chain argument — the two are in
// different queues at different institutions. What forces it is the BANK's own
// collection order, the settlement agent first.
//
// That is why centralBank.advise sends the statements before it answers: the payer's
// bank's camt.053 CREDITS the clearing suspense its refund then draws on. Reversed,
// that bank pays its customer out of a suspense the return has not credited — which
// commits, suspense being a Liability the ledger does not guard.
func TestTheMessagesAReturnPutsOnTheWire(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)

	before := h.messagesSeen()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.work(t)

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
	// PAYER'S BANK, one goroutine popping one FIFO queue. The statement credits the
	// clearing suspense; the relayed return makes the refund draw on it.
	if order[payerStatement] > order[payerReturn] {
		t.Errorf("the payer's bank handled the relayed return at %d and its own camt.053 at %d; "+
			"the statement credits the suspense the refund draws on and must come first",
			order[payerReturn], order[payerStatement])
	}
}

// TestAReturnPutsTheMoneyBackInThePayersAccount is what a return is FOR, asserted
// where the customers are. Both accounts, because a return is not a refund out of
// thin air: the payee's bank claws the money back and the payer's bank credits the
// payer, and a test that checked only the payer would pass on a system that paid
// them twice. Read through each bank's own register, the only place the answer
// exists — a status is not a balance.
func TestAReturnPutsTheMoneyBackInThePayersAccount(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)
	if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding-harnessAmount {
		t.Fatalf("the payer holds %d after a settled transfer, want %d", got, harnessFunding-harnessAmount)
	}
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != harnessAmount {
		t.Fatalf("the payee holds %d after a settled transfer, want %d", got, harnessAmount)
	}

	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.work(t)

	if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding {
		t.Errorf("the payer holds %d after the return, want %d — the whole of it should be back", got, harnessFunding)
	}
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != 0 {
		t.Errorf("the payee holds %d after the return, want 0", got)
	}
}

// TestAReturnedCollectionIsSentByThePayersBank is the other direction, and the one
// that says returnerOf is a rule about ROLES rather than a habit of picking the
// creditor. A collection is submitted by the payee's bank and answered by the
// payer's, so the bank that returns it is the payer's — the SEPA rule book's own
// division, which is what MD01 says here.
//
// The payee's bank is told twice, and the two messages are different things:
//
//   - a camt.053, because its settlement account is one of the two the reserve
//     reversal moved. On a pull that bank is the one PAYING the reserves back.
//   - the pacs.004 itself, relayed once the return is final, because on a pull the
//     CREDITOR's bank holds the clawback and cannot refuse it. The payer's refund
//     right is unconditional, so a biller who cannot fund it goes overdrawn or
//     leaves a Returns Receivable behind.
//
// So the two directions are the same seven messages with the banks swapped: the SAME
// bank composes the pacs.004 in both, the far end from the submitter both times, and
// what flips is only which leg it holds.
func TestAReturnedCollectionIsSentByThePayersBank(t *testing.T) {
	h := newHarness(t)
	p := h.settledCollection(t)

	before := h.messagesSeen()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonNoMandate, "the debtor disputes the mandate")
	h.work(t)

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

// TestABankCannotReturnAPaymentItHasNotBeenHanded: the bank that returns a payment
// is the one that RECEIVED the instruction, and it receives that only once the cycle
// carrying it has settled. So before the cut-off there is no copy at that bank to
// return FROM, and the refusal is "this bank has never heard of this payment" rather
// than "this payment is in the wrong state".
//
// The state guard is still reached, by a payment that has already been RETURNED —
// asking twice. Refused at the door it is the caller's answer and names the status;
// payment.PostReturnLegTx's ErrInvalidStateTransition carries the empty code, so an
// institution that received such a return could only report it.
//
// Nothing goes on the wire either way: a refusal that had already sent the pacs.004
// would have the settlement agent settling reserves for a payment this bank knew was
// not returnable, and nothing downstream would catch it.
func TestABankCannotReturnAPaymentItHasNotBeenHanded(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t) // accepted and in a cycle, but no cut-off has been reached

	before := h.messagesSeen()
	err := h.returnErr(p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	if !errors.Is(err, payment.ErrPaymentNotFound) {
		t.Fatalf("Return = %v, want the payee's bank saying it holds no such payment", err)
	}
	h.work(t)
	if got := h.messagesSeen(); got != before {
		t.Errorf("a refused return put %d messages on the wire, want none", got-before)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("the refused return moved the payment to %v", got.Status)
	}
}

// TestABankRefusesToReturnAPaymentTwice is the state guard bank.returnPayment makes
// before any message exists: a payment that is already Returned is the one shape a
// returning bank can be asked about and must decline, and the refusal names the
// status this BANK records — it holds its own copy and can read no other.
func TestABankRefusesToReturnAPaymentTwice(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.work(t)
	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("the fixture payment is %v, want Returned — there is no second return to refuse otherwise", got.Status)
	}

	before := h.messagesSeen()
	err := h.returnErr(p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	if !errors.Is(err, payment.ErrInvalidStateTransition) {
		t.Fatalf("Return = %v, want the illegal transition refused to the caller", err)
	}
	if !strings.Contains(err.Error(), "Returned") {
		t.Errorf("the refusal %q does not say what this BANK records the payment as", err)
	}
	h.work(t)
	if got := h.messagesSeen(); got != before {
		t.Errorf("a refused return put %d messages on the wire, want none", got-before)
	}
}

// TestARedeliveredReturnIsReportedAndNotAnswered is the same discrimination one hop
// on, and the case that actually reaches the settlement agent.
//
// A queue redelivers, so the pacs.004 can arrive twice. SettleReturnTx answers
// ErrReturnAlreadySettled, and turning that into a pacs.002 would tell the returning
// bank that a return which in fact happened was rejected. Dead letter, no status.
// The agent holds no payment row here, so what catches the redelivery is the
// idempotency key on the reserve reversal, in its own ledger.
func TestARedeliveredReturnIsReportedAndNotAnswered(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.work(t)

	// The pacs.004 the clearing house relayed, sent to the settlement agent a
	// second time.
	relayed := h.lastMessageOfTypeTo(t, h.cfg.CentralBankBIC, "pacs.004.001.09")
	answered := h.statusesSentTo(h.creditorBIC)
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, relayed)

	// Errorf and not Fatalf: "reported" and "not answered" are two claims, and an
	// agent that answered instead would break both at once. Matched on the TEXT rather
	// than errors.Is, because a day's report is prose and the sentinel does not survive
	// into Problem.Detail as a wrapped error.
	if err := h.workErr(t); err == nil || !strings.Contains(err.Error(), payment.ErrReturnAlreadySettled.Error()) {
		t.Errorf("the day reported %v, want the already-settled return as a problem", err)
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

// TestAReturnTheSettlementAgentCannotActOnWholeIsRefused guards the one assumption
// this actor makes about the message it is handed: that it is being asked to return
// exactly one payment. Two ways a message can break that, both refused rather than
// half-executed — returning the first and dropping the rest would leave a payment
// somebody was told had been sent back and never was.
//
// Injected rather than provoked, since payment.ReturnMessage builds exactly one
// transaction. The refusal is asserted at the RETURNING BANK, so the whole path
// runs: the clearing house carries the doctored return and the answer comes back.
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
			h := newHarness(t)
			p := h.settledPayment(t)

			env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "account closed",
				payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-doctored", Now: testTime})
			if err != nil {
				t.Fatalf("ReturnMessage: %v", err)
			}
			tc.doctor(env.Document.(*iso20022.Pacs004))
			h.upload(t, h.creditorBIC, h.cfg.ClearingHouseBIC, env)
			h.work(t)

			// Answered, not reported: this is a judgement about the
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

// TestTheReturnsReasonTravelsFromTheAskingBankToTheLedgers follows the datum a
// pacs.004 exists to carry the whole way. Every other assertion in this file would
// pass on a system that returned the money for the wrong reason, or for none: two
// returns of the same payment under opposite codes move the same amounts between the
// same accounts.
//
// Asserted at both ends: ON THE WIRE, in the message the agent acted on — which also
// says the relay left the document alone — and IN THE BOOKS, in the descriptions of
// the two postings that move a customer's money.
//
// The CENTRAL BANK's own leg is asserted NOT to carry it, which is the sentence in
// payment.ReturnReason's doc that would otherwise be wrong: the reserve reversal is
// a settlement, so the reason reaches the two customer legs and not the third.
func TestTheReturnsReasonTravelsFromTheAskingBankToTheLedgers(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)

	const text = "the beneficiary account was closed"
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, text)
	h.work(t)

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
	cb, err := h.cbBook(t).GetTransactionByIdempotencyKey(context.Background(), string(p.ID)+":return-settle")
	if err != nil {
		t.Fatalf("no reserve reversal for %s: %v", p.ID, err)
	}
	if strings.Contains(cb.Description, string(iso20022.ReturnReasonClosedAccountNumber)) {
		t.Errorf("the reserve reversal is described as %q; the reason reaches the two customer legs, not this one", cb.Description)
	}
}

// TestAProprietaryReturnReasonReachesTheLedgersToo is the other arm of the choice,
// and why payment.ReturnReason reads both.
//
// ReturnReasonChoice is an xsd:choice with a code and a PROPRIETARY text, and
// iso20022 refuses a return carrying neither. A code is what this system's own banks
// send, so the proprietary arm is reachable only from a counterparty that uses one —
// which a real network is full of, the arm existing for reasons the external code
// set has no member for. An agent reading only the code would throw away the only
// thing the sender said. The free text is left empty as well, covering the join's
// other arm at the same time.
//
// The injection puts the message on the wire WITHOUT the returning bank's own act
// behind it: that bank never posted its clawback. So the far leg lands and the near
// one never existed, and the cost is asserted on the RETURNING BANK's own copy.
//
// Not on its STATUS: each institution holds its own copy and each moves on what IT
// did or was told, so Returned is correct everywhere — the return did settle — and
// none of it is evidence about the leg. The leg is. A clawback is a posting in the
// payee's bank's own ledger and its id is a column on that bank's own copy.
func TestAProprietaryReturnReasonReachesTheLedgersToo(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)

	env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "",
		payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-prtry", Now: testTime})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	prtry := "SCHEME-LOCAL-DISPUTE"
	rsn := &env.Document.(*iso20022.Pacs004).PmtRtr.TxInf[0].RtrRsnInf.Rsn
	rsn.Cd, rsn.Prtry = nil, &prtry
	h.upload(t, h.creditorBIC, h.cfg.ClearingHouseBIC, env)
	h.work(t)

	if got := h.postingByKey(t, h.debtorPID, string(p.ID)+":return-refund").Description; !strings.Contains(got, prtry) {
		t.Errorf("the payer's refund is described as %q, want it to carry the proprietary reason %q", got, prtry)
	}
	// One leg, and the missing one is the returner's own clawback. Asserted
	// rather than left out, because "the near leg was never posted" and "the far
	// bank never got the message" would otherwise look the same from here.
	if got := h.bankPayment(t, h.creditorBIC, p.ID); got.ReturnClawbackTx != "" {
		t.Errorf("the payee's bank posted a clawback (%s); this return had no returning bank behind it",
			got.ReturnClawbackTx)
	}
}

// TestAReturnThatNamesNoPaymentCannotBeAnswered pins the one refusal in this flow
// that reaches nobody, so its cost is recorded rather than rediscovered.
//
// A pacs.004 may refer back by OrgnlEndToEndId alone: OrgnlTxId is optional in the
// schema and ReturnTransaction.validate accepts either. This system identifies
// payments by OrgnlTxId, so such a message names no payment this network holds. The
// sender is told nothing and the payment is untouched; what is reported is the
// answer.
//
// centralBank.answer quotes the reference the RETURNING BANK gave rather than the
// payment id, because quoting the payment id as the end-to-end reference too would
// break the convention every other per-payment status follows. So the refusal is
// built and sent and dies one hop later: the clearing house turns an answer back
// into a payment by OrgnlTxId, the element this message lacks.
//
// WHAT refuses it is a money guard rather than a lookup: the reserve reversal's
// idempotency key is derived from the payment id, and an empty one would move
// reserves under a key every nameless return shares. Injected, since
// payment.ReturnMessage always writes OrgnlTxId.
func TestAReturnThatNamesNoPaymentCannotBeAnswered(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)

	env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "no transaction id",
		payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-noid", Now: testTime})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	tx := &env.Document.(*iso20022.Pacs004).PmtRtr.TxInf[0]
	tx.OrgnlTxId, tx.OrgnlEndToEndId = "", "E2E-ONLY"
	h.upload(t, h.creditorBIC, h.cfg.ClearingHouseBIC, env)

	answered := h.statusesSentTo(h.creditorBIC)
	err = h.workErr(t)
	if err == nil {
		t.Fatal("the day was clean; a return the settlement agent could neither execute nor answer went unreported")
	}
	if !strings.Contains(err.Error(), "naming no payment") || !strings.Contains(err.Error(), string(h.cfg.ClearingHouseBIC)) {
		t.Errorf("the reported problem %q is not the clearing house unable to say which payment the answer is about", err)
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
	h := newHarness(t)
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
	h.upload(t, h.creditorBIC, h.cfg.ClearingHouseBIC, env)
	h.work(t)

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

// TestTheSettlementAgentsAnswerQuotesTheReferenceTheBankSent is the convention every
// per-payment status follows, asserted at the one actor that had drifted off it. A
// bank matches an answer to its instruction by comparing what it SENT with what came
// back, and the payment id as an end-to-end reference matches nothing any bank sent.
//
// Both cases, because the convention has two halves and only one is visible in a
// fixture with no client reference: a payment that quotes one gets it back verbatim,
// and a payment that quotes none gets NOTPROVIDED — the EPC's convention for "the
// payer gave no reference", and what the pacs.008 carried on the way out.
func TestTheSettlementAgentsAnswerQuotesTheReferenceTheBankSent(t *testing.T) {
	for _, tc := range []struct{ name, e2e, want string }{
		{"a payment with a client reference", "INV-42", "INV-42"},
		{"a payment with none", "", notProvided},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			req := h.creditTransferRequest(t)
			req.EndToEndID = tc.e2e
			submitted, err := h.dep.Submit(context.Background(), req)
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
			h.upload(t, h.creditorBIC, h.cfg.ClearingHouseBIC, env)
			h.work(t)

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

// TestTheSettlementAgentCannotAnswerYesWithAReason: a pacs.002 carries StsRsnInf
// only for a rejection, so a cause passed beside SettlementCompleted sets a code and
// a text the builder then silently drops — a message saying everything is fine, with
// the reason it was not deleted on the way out. No handler does it today; this is
// what stops the next one, asserted directly because no message provokes it.
func TestTheSettlementAgentCannotAnswerYesWithAReason(t *testing.T) {
	h := newHarness(t)
	cb := &CentralBank{d: h.dep, net: h.cb(), ops: h.cb(), bic: h.cfg.CentralBankBIC, host: h.dep.CentralBank().host}

	err := cb.answer(context.Background(), h.cfg.ClearingHouseBIC,
		payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided},
		notProvided, "cyc_x",
		iso20022.TransactionStatusSettlementCompleted,
		payment.ErrCycleNotFound)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	// The cycle id is invented, so whatever the clearing house makes of this
	// message it will have nothing to look up. Its problem is taken rather than
	// asserted on: what is under test is the BYTES this institution queued, and
	// the recipient's opinion of them is another test's business.
	_ = h.workErr(t)

	status := h.lastStatusTo(t, h.cfg.ClearingHouseBIC)
	tx := status.FIToFIPmtStsRpt.TxInfAndSts[0]
	if tx.TxSts != iso20022.TransactionStatusRejected {
		t.Fatalf("an answer built with a cause reports %v, want RJCT — the reason is dropped on any other status", tx.TxSts)
	}
	if tx.StsRsnInf == nil || tx.StsRsnInf.Rsn.Cd == nil {
		t.Fatalf("the rejection carries no reason code: %#v", tx.StsRsnInf)
	}
}

// TestAPayeeWhoSpentTheMoneyStopsTheReturnOnTheWire is the observable half of the
// return's one rule: a bank can refuse a leg only if it posts it before it sends.
// payment's own test measures the domain call; this measures three things it cannot
// see — the caller of Deployment.Return gets the refusal, NOTHING goes on the wire,
// and the code is AM04 about the returning bank's OWN customer.
//
// That last is worth stating. AM04 here has meant a payer's empty account and a
// bank's empty reserve; this is a third speaker, the bank that RECEIVED a credit
// transfer saying its own beneficiary has spent the money and cannot be made to give
// it back.
//
// Nothing on the wire is what makes the refusal binding rather than reported: a bank
// that had sent the pacs.004 first would have the reserves already reversed and no
// way back, the settlement agent not reading the payment at all.
func TestAPayeeWhoSpentTheMoneyStopsTheReturnOnTheWire(t *testing.T) {
	h := newHarness(t)
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

	h.work(t)
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
// refusal, and why payment.ReverseReturnLegTx exists. The returning bank posts
// BEFORE it sends, so an RJCT leaves that posting standing against a return that
// will not happen.
//
// A genuine RJCT takes a real shortfall, and a doctored message will not do: an
// injected pacs.004 has no returning bank behind it. What is left is the one thing
// SettleReturnTx decides — whether the CREDITOR's bank can cover the reversal — and
// the direction that reaches it is the pull:
//
//   - a collection settles, so the payee's bank has the reserves and the payee the
//     money;
//   - the payee spends it, which takes both away;
//   - the payer's bank returns the collection. It holds the REFUND, which is
//     unconditional and always postable, so it posts and sends;
//   - the central bank finds the payee's bank short and answers AM04.
//
// On a push the same shortfall stops the return one step earlier, at the clawback.
//
// What the refusal must leave behind: the payer back where the collection left them,
// the refund posting marked Reversed rather than deleted, and the payee's bank never
// sent the pacs.004 at all. That last is what the clearing house holding the message
// buys — a bank relayed it on arrival would have clawed its biller back for a return
// the network then refused, with no message that would ever tell it.
func TestARefusedReturnUnwindsTheReturningBanksLeg(t *testing.T) {
	h := newHarness(t)
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
	h.work(t)

	// Refused, and refused with the code a reserve shortfall carries.
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusRejected)
	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonInsufficientFunds)

	// The payment is exactly where it was, at every institution that holds one.
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Errorf("the refused return left the clearing house's copy at %v, want Settled", got.Status)
	}
	// Each leg is read off the bank whose ledger it would be in, which on a PULL
	// puts the refund at the returner and the clawback at the other bank. See
	// harness.bankPayment.
	if got := h.bankPayment(t, h.creditorBIC, p.ID); got.ReturnClawbackTx != "" {
		t.Errorf("the payee's bank posted a clawback (%s) for a return that was refused", got.ReturnClawbackTx)
	}
	// The refund is REVERSED rather than absent. The id stays on the payment
	// because this bank did post; the ledger is where "it no longer stands" is
	// recorded. See payment.ReverseReturnLegTx.
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.ReturnRefundTx == "" {
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

// TestAReturnRetriedAfterAnUnwindRepaysThePayer is the half the unwind exists for.
// AM04 is a SHORTFALL — the counterparty's bank was short at that moment, and
// somebody can cover it — so the return is asked again, and this is the only test
// that asks it. The payer's eight-week refund right does not expire because the
// biller's bank was briefly empty.
//
// It asserts on the MONEY, because a return that runs to completion and sets Returned
// looks identical from the status, the pacs.002 and the tap whether or not the payer
// was repaid:
//
//   - the PAYER is up by the amount. That is what a return is.
//   - the BILLER is down by it. On a pull the biller's bank is FORCED into the
//     clawback after finality, so an empty biller goes overdrawn.
//   - the returning bank's CLEARING SUSPENSE is back to zero. It is the account that
//     would hold the difference if exactly one half happened, and an amount stranded
//     there is stranded for ever: nothing sweeps it.
func TestAReturnRetriedAfterAnUnwindRepaysThePayer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p := h.settledCollection(t)
	// The biller spends what it collected, which empties its BANK's settlement
	// account at the central bank. That is the condition the settlement agent
	// answers AM04 to: the reserves it is asked to reverse are not there.
	h.spendTheCredit(t)

	h.returnPayment(t, p.ID, iso20022.ReturnReasonNoMandate, "the debtor disputes the mandate")
	h.work(t)

	if unwound := h.payment(t, p.ID); unwound.Status != payment.Settled {
		t.Fatalf("the refused return left the payment at %v, want Settled — this test retries from there", unwound.Status)
	}
	unwound := h.bankPayment(t, h.debtorBIC, p.ID)
	if unwound.ReturnRefundTx == "" {
		t.Fatal("the returning bank posted no refund at all; this test is about retrying one that was unwound")
	}
	if got := h.postingByKey(t, h.debtorPID, string(p.ID)+":return-refund"); got.Status != ledger.Reversed {
		t.Fatalf("the first refund is %v, want Reversed — this test retries a return whose leg no longer stands", got.Status)
	}

	// What makes the retry askable: the biller pays cash in over the counter, and the
	// bank then places that cash on reserve. Two acts — a deposit reaches the bank's
	// vault and no institution but that bank, and what replenishes a RESERVE is a
	// lodgement, the reserve account being in the central bank's book.
	if err := h.bank(h.creditorBIC).Deposit(ctx, h.creditorPID, h.creditorAcct.ID, harnessAmount, "cash in over the counter"); err != nil {
		t.Fatalf("funding the biller's bank so the return can be retried: %v", err)
	}
	h.lodge(t, h.creditorBIC, "EUR", harnessAmount)

	payerBefore := h.balance(t, h.debtorPID, h.debtorAcct.ID)
	billerBefore := h.balance(t, h.creditorPID, h.creditorAcct.ID)

	h.returnPayment(t, p.ID, iso20022.ReturnReasonNoMandate, "the debtor disputes the mandate")
	h.work(t)

	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Errorf("the retried return left the payment at %v, want Returned", got.Status)
	}
	if got, want := h.balance(t, h.debtorPID, h.debtorAcct.ID), payerBefore+harnessAmount; got != want {
		t.Errorf("the payer holds %d after the retried return, want %d — a return that completes repays the payer", got, want)
	}
	if got, want := h.balance(t, h.creditorPID, h.creditorAcct.ID), billerBefore-harnessAmount; got != want {
		t.Errorf("the biller holds %d after the retried return, want %d — the clawback is forced after finality", got, want)
	}
	if got := h.suspense(t, h.debtorPID); got != 0 {
		t.Errorf("the returning bank's clearing suspense holds %d after the retried return, want 0 — an amount left there is stranded for ever", got)
	}

	// The retry is a SECOND posting and not a revival of the reversed one: the
	// ledger has no way to un-reverse a transaction, so a return that repaid the
	// payer must have a standing leg of its own.
	retried := h.bankPayment(t, h.debtorBIC, p.ID)
	if retried.ReturnRefundTx == unwound.ReturnRefundTx {
		t.Errorf("the payment still names %s as its refund, which is Reversed; a retry posts a new leg", retried.ReturnRefundTx)
	}
	if got := h.posting(t, h.debtorPID, retried.ReturnRefundTx); got.Status != ledger.Posted {
		t.Errorf("the refund the returned payment names is %v, want Posted", got.Status)
	}
}

// TestTheClearingHousesOtherCallersLeaveTheHeldReturnsAlone turns an invariant about
// held_returns into a measurement. A return waiting for the settlement agent's answer
// is the one obligation this institution carries that belongs to NO cut-off, and the
// three methods reached from outside a business day — closeCycle, settle and reject
// — each sweep something it is holding. A cut-off that dropped a pending return would
// leave the other bank never told to post its leg, with the reserves about to move
// under a batch that has nothing to do with it.
//
// It drives all three through the MESH rather than calling them directly, so what is
// measured is the whole of what each does, and leaves a return in flight across each
// so that there is something to disturb.
func TestTheClearingHousesOtherCallersLeaveTheHeldReturnsAlone(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)
	ctx := context.Background()

	// A real pacs.004 is built and carried, so the row placed below is the value this
	// actor really holds rather than a shape invented by the test. It cannot be LEFT in
	// flight — every return this deployment carries is answered, and an answer drops the
	// row — so the flow runs to completion and one row is put back by hand.
	env, err := h.net.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "account closed",
		payment.MessageContext{From: h.creditorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "rtn-held", Now: testTime})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	h.upload(t, h.creditorBIC, h.cfg.ClearingHouseBIC, env)
	h.work(t)
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	held := payment.HeldReturn{PaymentID: "pay_sentinel", ReturnedBy: h.creditorBIC, File: raw}
	if err := h.net.HoldReturn(ctx, held); err != nil {
		t.Fatalf("HoldReturn: %v", err)
	}

	// A cut-off, a re-instruction, and an operator's rejection: the three
	// entry points that do not arrive in a download queue.
	if _, err := h.net.OpenCycle(context.Background(), payment.SchemeSEPACT); err != nil {
		t.Fatalf("OpenCycle: %v", err)
	}
	rejected := h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)
	for _, c := range h.cycles(t) {
		if c.Status == payment.CycleSettled {
			if _, err := h.dep.ClearingHouse().Settle(context.Background(), c.ID); err == nil {
				t.Errorf("re-settling %s was accepted; this test wants the refusal path walked", c.ID)
			}
		}
	}
	if _, err := h.dep.ClearingHouse().Reject(context.Background(), rejected.ID,
		iso20022.StatusReasonNotSpecifiedAgentGenerated, "operator says no"); err == nil {
		t.Log("the rejection was accepted; either way the map is what is measured")
	}
	h.work(t)

	got, err := h.net.GetHeldReturn(ctx, "pay_sentinel")
	if err != nil {
		t.Fatalf("closeCycle, settle or reject dropped a held return: %v", err)
	}
	if got.ReturnedBy != held.ReturnedBy || !bytes.Equal(got.File, held.File) {
		t.Error("a held return was rewritten by one of the three routes that run outside a business day; none of them may touch another conversation's message")
	}
}
