package mesh

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The settlement flow end to end: a cut-off, an instruction, a discharge, and
// the news travelling back to the bank that started the payment.
//
// Two assertions on the answer rather than one, because they say different
// things. ACSC is the transaction status — the point of finality, and the first
// moment in this package's flows at which a payee's bank has actually been paid.
// The absent reason is what says this is not a rejection: a pacs.002 carries a
// StsRsnInf only when something was refused, so "no code" is the observable form
// of "nothing went wrong". See assertLastStatusTo.
func TestClosingACycleSettlesItThroughTheCentralBank(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	h.closeCycle(t)
	h.drain(t)

	got := h.payment(t, p.ID)
	if got.Status != payment.Settled {
		t.Fatalf("status = %v, want Settled", got.Status)
	}
	h.assertLastStatusTo(t, h.debtorBIC, "" /* no code */)
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusSettlementCompleted)
}

// A net payer who cannot cover becomes RJCT/AM04 on the settlement
// instruction. Before the mesh this was a Go error returned to whoever clicked
// settle, which is not something a clearing house can act on.
func TestANetPayerWhoCannotCoverIsRejectedOnTheInstruction(t *testing.T) {
	h := newMeshHarnessWithAnUnfundedReserve(t)
	h.submitCreditTransfer(t)
	h.drain(t)

	h.closeCycle(t)
	h.drain(t)

	h.assertLastTxStatusTo(t, h.cfg.ClearingHouseBIC, iso20022.TransactionStatusRejected)
	h.assertLastStatusTo(t, h.cfg.ClearingHouseBIC, iso20022.StatusReasonInsufficientFunds)
}

// TestARefusedSettlementCanBeInstructedAgain walks the whole way out of the one
// state this system had no exit from.
//
// The refusal above is where the previous test stops, and where the system used
// to stop too. What it leaves is a cycle Closed with no settlement, its payments
// Cleared, the payer debited into their own bank's clearing suspense and the
// payee unpaid — and no transition out of that for ANY object: CloseCycleTx
// wants an open cycle, RejectAtCSMTx an Initiated or Accepted payment,
// PostReturnLegTx a settled one. SettleCycle's only non-seed caller is the
// pacs.009 handler, and the only sender of a pacs.009 was a cut-off, which
// needs an open cycle. Terminal, with every payer's money stranded.
//
// So this asserts the stuck state first — as state, not as a status code, since
// the AM04 is on the wire and in no store — then funds the short member and
// asks the clearing house to instruct settlement again. The payee is paid at the
// end, which is the only assertion that says the money actually arrived rather
// than that a status changed.
func TestARefusedSettlementCanBeInstructedAgain(t *testing.T) {
	h := newMeshHarnessWithAnUnfundedReserve(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	// The stuck state, in the four places it shows.
	stuck := h.creditTransferCycle(t)
	if stuck.Status != payment.CycleClosed || stuck.SettlementID != "" {
		t.Fatalf("cycle %s is %v with settlement %q, want Closed with none", stuck.ID, stuck.Status, stuck.SettlementID)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Cleared {
		t.Fatalf("payment status = %v, want Cleared", got.Status)
	}
	if got := h.suspense(t, h.debtorPID); got != harnessAmount {
		t.Fatalf("the payer's bank holds %d in suspense, want %d — the money left the payer and settled nowhere", got, harnessAmount)
	}
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != 0 {
		t.Fatalf("the payee holds %d, want 0 — nothing has settled", got)
	}

	// The operator's remedy: fund the short member, which since Task 18a is TWO
	// acts rather than one.
	//
	// A deposit raises the customer's balance and leaves the bank holding vault
	// cash. It no longer raises the reserve, so on its own it would not unstick
	// this cycle at all — the central bank's book is what settlement reads, and a
	// deposit never reaches it. The lodgement is what puts the reserve behind the
	// bank, and it is a real camt.050/camt.025 round trip.
	//
	// That the remedy needs both is the point rather than a detail of the fixture.
	// A bank cannot settle out of cash in its own drawer; it settles out of central
	// bank money, and getting some is a conversation.
	if err := h.net.Deposit(context.Background(), h.debtorPID, h.debtorAcct.ID, harnessAmount, "Reserve top-up"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	h.lodge(t, h.debtorPID, "EUR", harnessAmount)

	// And ask again. This is the route POST /cycles/{cid}/settle reaches.
	if _, err := h.mesh.Settle(context.Background(), stuck.ID); err != nil {
		t.Fatalf("Settle %s: %v", stuck.ID, err)
	}
	h.drain(t)

	settled := h.creditTransferCycle(t)
	if settled.Status != payment.CycleSettled || settled.SettlementID == "" {
		t.Fatalf("cycle %s is %v with settlement %q, want Settled with one", settled.ID, settled.Status, settled.SettlementID)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("payment status = %v, want Settled", got.Status)
	}
	if got := h.suspense(t, h.debtorPID); got != 0 {
		t.Fatalf("the payer's bank still holds %d in suspense after settlement, want 0", got)
	}
	// The one that matters: the payee has the money.
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != harnessAmount {
		t.Fatalf("the payee holds %d, want %d", got, harnessAmount)
	}
	// And the bank that submitted was told, by the same fan-out a first-time
	// settlement uses. A recovery that paid the payee and told nobody would
	// leave the submitting bank waiting for ever on an instruction that had in
	// fact completed.
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusSettlementCompleted)
}

// TestReSettlingASettledCycleIsRefused is the first of the two guards that make
// asking twice safe, and the one that stops a second message ever being built.
//
// Verified rather than assumed: the count of settlement instructions the central
// bank has been handed is what says no second pacs.009 went out, and the
// settlement count is what says no second discharge happened. A guard that only
// changed the error text would pass an assertion on the error alone.
func TestReSettlingASettledCycleIsRefused(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	cyc := h.creditTransferCycle(t)
	if cyc.Status != payment.CycleSettled {
		t.Fatalf("cycle is %v, want Settled before the second ask means anything", cyc.Status)
	}
	instructionsBefore := h.instructionsSentTo(h.cfg.CentralBankBIC)

	_, err := h.mesh.Settle(context.Background(), cyc.ID)
	if !errors.Is(err, payment.ErrCycleNotClosed) {
		t.Fatalf("Settle on a settled cycle = %v, want ErrCycleNotClosed", err)
	}
	if got := h.instructionsSentTo(h.cfg.CentralBankBIC); got != instructionsBefore {
		t.Fatalf("a refused re-settle sent %d instructions, want the original %d", got, instructionsBefore)
	}

	settlements, err := h.net.ListSettlements(context.Background())
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	if len(settlements) != 1 {
		t.Fatalf("%d settlements, want the one the cut-off instructed", len(settlements))
	}
}

// TestASecondSettlementInstructionPostsNothing is the guard BEHIND that one: the
// settlement agent's, on a message csm.settle would not have built.
//
// Two operators racing past the CycleClosed check above would each send a
// pacs.009, and this is what happens to the second — the same thing that happens
// to a redelivered one, since a queue can hand the same instruction over twice
// and neither case is distinguishable at the receiver. SettleCycleTx refuses it
// with ErrCycleNotClosed and receiveSettlement dead-letters it rather than
// answering RJCT, because telling the clearing house a cycle was rejected when
// it in fact settled would be a lie.
//
// The money assertion is the point: the reserves moved once. Behind the state
// machine the central bank's posting carries the idempotency key
// "<cycle>:settle", so even a defect that got past the guard could not post
// twice.
func TestASecondSettlementInstructionPostsNothing(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	cyc := h.creditTransferCycle(t)
	payeeBefore := h.balance(t, h.creditorPID, h.creditorAcct.ID)

	// The instruction the cut-off sent, replayed verbatim into the settlement
	// agent's inbox. Re-sent by the clearing house, because that is who the
	// central bank accepts one from.
	raw := h.lastMessageOfTypeTo(t, h.cfg.CentralBankBIC, "pacs.009.001.08")
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, raw)

	err := h.drainErr(t)
	if err == nil || !strings.Contains(err.Error(), "was told to settle") {
		t.Fatalf("draining after a replayed instruction = %v, want a dead letter naming the repeat", err)
	}

	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != payeeBefore {
		t.Fatalf("the payee holds %d after a replayed instruction, want the unchanged %d", got, payeeBefore)
	}
	settlements, err := h.net.ListSettlements(context.Background())
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	if len(settlements) != 1 {
		t.Fatalf("%d settlements after a replayed instruction, want 1", len(settlements))
	}
	after := h.creditTransferCycle(t)
	if after.SettlementID != cyc.SettlementID {
		t.Fatalf("the cycle now names settlement %q, want the original %q", after.SettlementID, cyc.SettlementID)
	}
}

// creditTransferCycle is the fixture's SEPA credit-transfer cycle, read back.
//
// By scheme rather than by index: every fixture here opens a direct-debit cycle
// beside it, and which one ListCycles puts first is not something these tests
// should depend on.
func (h *meshHarness) creditTransferCycle(t *testing.T) payment.ClearingCycle {
	t.Helper()
	for _, c := range h.cycles(t) {
		if c.Scheme == payment.SchemeSEPACT {
			return c
		}
	}
	t.Fatal("no sepa.ct cycle in this fixture")
	return payment.ClearingCycle{}
}

// TestEachMemberBooksTheStatementItWasSent is what the camt.053 is FOR, measured
// at the far end: after a cut-off each member holds its own record of it, posted,
// with the closing balance the settlement agent asserted.
//
// TestEachBankBooksItsOwnSettlementAndNoOtherBooks says which book each bank
// touched; this says what it wrote there, and the two together are the whole of
// the move. Three things per member, and each rules out a different way of
// getting this wrong:
//
//   - Status is Posted and MirrorTx names a transaction. Advised is the other
//     value the type carries, and asserting the row is NOT at it is what stops a
//     row recording nothing from passing for a booking. It is not the state a
//     failure leaves behind: the row and the leg are one unit of work, so a
//     failure leaves no row at all — see payment.AdviceAdvised.
//   - Movement is SIGNED and opposite at the two banks. It travels as a magnitude
//     plus CdtDbtInd and is reassembled by payment.ReadStatement, so a member that
//     lost the sign in transit would post its mirror leg backwards — and the two
//     banks are the only pair that can show it, since one of them is the net payer.
//   - ClosingBalance equals what the central bank's book actually says the
//     reserve account stands at. That is the assertion the whole message
//     definition exists to make checkable: camt.053 was chosen over camt.054
//     precisely because it carries a balance to check a posting against, and a
//     statement quoting the wrong one would be worse than none.
func TestEachMemberBooksTheStatementItWasSent(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	cyc := h.creditTransferCycle(t)
	if cyc.Status != payment.CycleSettled {
		t.Fatalf("cycle is %v, want Settled — there is no statement to have been sent otherwise", cyc.Status)
	}

	for _, member := range []struct {
		name string
		pid  payment.ParticipantID
		want ledger.Amount
	}{
		{"the payer's bank", h.debtorPID, -harnessAmount},
		{"the payee's bank", h.creditorPID, harnessAmount},
	} {
		advice := h.advice(t, member.pid, string(cyc.ID))
		if advice.Status != payment.AdvicePosted || advice.MirrorTx == "" {
			t.Errorf("%s holds an advice that is %v with mirror %q, want Posted with a transaction",
				member.name, advice.Status, advice.MirrorTx)
		}
		if advice.Movement != member.want {
			t.Errorf("%s booked a movement of %d, want %d", member.name, advice.Movement, member.want)
		}
		reserve, err := h.net.ReserveBalance(context.Background(), member.pid, "EUR")
		if err != nil {
			t.Fatalf("ReserveBalance %s: %v", member.pid, err)
		}
		if advice.ClosingBalance != reserve {
			t.Errorf("%s was told its reserve closed at %d; the central bank's book says %d",
				member.name, advice.ClosingBalance, reserve)
		}
	}
}

// advice is one bank's own record of a cut-off, read out of that bank's book.
//
// Through the store rather than through a Network method, because there is no
// such method: an advice is a member's own row and nothing in this system reads
// another institution's yet. The unit of work is opened on a bare context, so the
// recorder attributes the read to no actor and it cannot spoil a per-actor set.
func (h *meshHarness) advice(t *testing.T, id payment.ParticipantID, reference string) payment.SettlementAdvice {
	t.Helper()
	ctx := context.Background()
	p, err := h.net.GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	var out payment.SettlementAdvice
	if err := h.net.Store().View(ctx, func(ctx context.Context, tx payment.Tx) error {
		out, err = tx.GetSettlementAdvice(ctx, p.BookID, reference, "EUR")
		return err
	}); err != nil {
		t.Fatalf("GetSettlementAdvice for %s in %s: %v", id, reference, err)
	}
	return out
}

// One instruction per asset. SettleCycleTx settles per asset, and a message
// mixing currencies in one IntrBkSttlmAmt would not be expressible.
func TestOneSettlementInstructionPerAsset(t *testing.T) {
	h := newMeshHarnessWithTwoAssets(t)
	h.submitCreditTransfer(t)
	h.submitCreditTransferInUSD(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	if got := h.instructionsSentTo(h.cfg.CentralBankBIC); got != 2 {
		t.Fatalf("sent %d settlement instructions, want one per asset", got)
	}
}

// TestASettledCollectionIsAnnouncedToThePayeesBank is the fan-out's other
// direction, and it is the one that says who is actually waiting.
//
// On a push the bank that submitted and the bank that is owed an answer are the
// payer's, and they are the same bank, so a fan-out that simply addressed the
// payer would look right. On a PULL they are opposite: the payee's bank sent the
// collection and has been waiting since, and the payer's bank answered it long
// ago and has nothing outstanding. Settlement is the moment the payee's bank is
// actually paid, and this message is what tells it to pay its own customer, so
// it closes the thing it started and starts the last posting the payment needs.
//
// The payer's bank is told NOTHING, and that is the assertion beside it. The
// last thing it heard was the clearing house's ACCP, and a system that announced
// settlement to every party to a payment would be one where "who is waiting for
// this" had stopped meaning anything.
//
// # ONE message, not two, and that is the pull half of the direction rule
//
// The fan-out addresses two roles — the bank that submitted, and the CREDITOR's
// bank, which has the leg to post. On a push those are two institutions and the
// clearing house sends twice. Here they are one, so it sends once: the payee's
// bank submitted the collection and is also the creditor. The count is asserted
// because nothing else would catch a fan-out that sent the same advice twice —
// PostCreditorLegTx's Settled guard makes the second one a no-op, so the money
// would still be right and only the conversation would be wrong. See
// csm.tellSettled.
func TestASettledCollectionIsAnnouncedToThePayeesBank(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitDirectDebit(t)
	h.drain(t)

	// Counted from here, so the ACCP this bank was already sent when it
	// submitted is not in the total.
	before := h.statusesSentTo(h.creditorBIC)
	h.closeCycle(t)
	h.drain(t)

	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("status = %v, want Settled", got.Status)
	}
	h.assertLastTxStatusTo(t, h.creditorBIC, iso20022.TransactionStatusSettlementCompleted)
	if got := h.statusesSentTo(h.creditorBIC) - before; got != 1 {
		t.Errorf("the payee's bank was sent %d statuses over the cut-off; it is the submitter AND the creditor's bank, which is one message", got)
	}
	if got := h.statusesSentTo(h.debtorBIC); got != 0 {
		t.Errorf("the payer's bank was sent %d statuses; on a pull it answered the collection and is waiting for nothing", got)
	}
}

// TestEachSettlementInstructionCarriesOneAssetsLegs is what the count above
// cannot see.
//
// Two instructions is the right number for two assets AND for two cycles that
// happened to share one, so the count on its own does not distinguish "grouped
// by asset" from "one per cut-off". This reads the messages: each carries the
// legs of exactly one asset, and between them they carry both.
//
// What that rules out is the arrangement this whole design exists to avoid — a
// single instruction whose legs are half in euro and half in dollars, which a
// pacs.009 is perfectly capable of expressing (payment.ReadSettlement reads each
// leg's own currency, deliberately) and which would net a euro position against
// a dollar one at the settlement agent.
func TestEachSettlementInstructionCarriesOneAssetsLegs(t *testing.T) {
	h := newMeshHarnessWithTwoAssets(t)
	h.submitCreditTransfer(t)
	h.submitCreditTransferInUSD(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	var assets []ledger.AssetCode
	for _, doc := range h.instructionsTo(t, h.cfg.CentralBankBIC) {
		legs, err := payment.ReadSettlement(doc)
		if err != nil {
			t.Fatalf("ReadSettlement: %v", err)
		}
		if len(legs) != 2 {
			t.Fatalf("an instruction carries %d legs; two banks with a position apiece is two", len(legs))
		}
		if legs[0].Asset != legs[1].Asset {
			t.Fatalf("one instruction mixes %s and %s; a cycle settles in one asset", legs[0].Asset, legs[1].Asset)
		}
		assets = append(assets, legs[0].Asset)
	}
	if len(assets) != 2 || assets[0] == assets[1] {
		t.Fatalf("the instructions cover %v, want one for each of the two assets", assets)
	}
}

// TestASettlementInstructionRunsBetweenBanksAndTheCentralBank is the shape of
// what the clearing house asks for, read off the wire.
//
// A netted position has no counterparty among the banks — that is what netting
// destroys — so every leg runs between one member and the SETTLEMENT AGENT: out
// of the net payer, into the net receiver. Reading it back from the bytes is the
// point: this is the one place in the system where the multilateral net becomes
// a set of bilateral instructions, and getting the direction backwards would
// settle the cycle the wrong way round while every total still balanced.
func TestASettlementInstructionRunsBetweenBanksAndTheCentralBank(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	docs := h.instructionsTo(t, h.cfg.CentralBankBIC)
	if len(docs) != 1 {
		t.Fatalf("the central bank was sent %d instructions, want one for the one cycle with payments in it", len(docs))
	}
	legs, err := payment.ReadSettlement(docs[0])
	if err != nil {
		t.Fatalf("ReadSettlement: %v", err)
	}

	want := []payment.SettlementLeg{
		{From: h.debtorBIC, To: h.cfg.CentralBankBIC, Amount: harnessAmount, Asset: "EUR"},
		{From: h.cfg.CentralBankBIC, To: h.creditorBIC, Amount: harnessAmount, Asset: "EUR"},
	}
	if len(legs) != len(want) {
		t.Fatalf("the instruction carries %d legs, want %d", len(legs), len(want))
	}
	for i, leg := range legs {
		if leg.From != want[i].From || leg.To != want[i].To || leg.Amount != want[i].Amount || leg.Asset != want[i].Asset {
			t.Errorf("leg %d is %+v, want %+v", i, leg, want[i])
		}
		// Every leg names the cycle it discharges, which is the only thing the
		// central bank reads out of this message. A leg without it would be an
		// instruction to move money for no stated reason.
		if leg.Reference == "" {
			t.Errorf("leg %d names no cycle; that is what the settlement agent settles by", i)
		}
	}
}

// TestAnEmptyCycleInstructsNothing is the other half of the cut-off.
//
// Every credit-transfer test here closes an untouched direct-debit window
// alongside the one it filled, so this path runs constantly — but no assertion
// looks at it, and a clearing house that sent an empty instruction would be
// caught only by the codec refusing to build a pacs.009 with no transactions,
// which is a failure in the wrong place saying the wrong thing.
//
// Nothing to discharge is not a failure. The cycle closes, records positions
// that are all zero, and stays Closed for ever, because there is no settlement
// for it to reach.
func TestAnEmptyCycleInstructsNothing(t *testing.T) {
	h := newMeshHarness(t)
	h.closeCycle(t) // both windows are open and neither has a payment in it
	h.drain(t)

	if got := h.instructionsSentTo(h.cfg.CentralBankBIC); got != 0 {
		t.Errorf("closing two empty cycles sent %d settlement instructions, want none", got)
	}
	for _, c := range h.cycles(t) {
		if c.Status != payment.CycleClosed {
			t.Errorf("cycle %s (%s) is %v after the cut-off, want Closed", c.ID, c.Scheme, c.Status)
		}
		if c.SettlementID != "" {
			t.Errorf("cycle %s settled as %s; there was nothing in it to settle", c.ID, c.SettlementID)
		}
	}
}

// TestARefusedSettlementLeavesTheCycleClosedAndThePaymentsCleared is what the
// RJCT actually costs, measured rather than inferred from the message.
//
// The batch fails WHOLE — SettleCycleTx is one unit of work — so the assertion
// is on the state everywhere: no settlement recorded, the cycle still Closed,
// and every payment still Cleared with the payer's money still in its own bank's
// clearing suspense. That is the state the central bank's console reads, and it
// is why the console can name the reason without one being stored: the net
// position and the reserve are both right there.
//
// It also pins the thing csm.receiveSettlementStatus decided NOT to do. No bank
// is told, and the drain would say so if one were: bank.receiveStatus refuses a
// rejection of a payment this network records as Cleared, so a fan-out here
// would come back as a dead letter at both banks rather than as a quiet mistake.
func TestARefusedSettlementLeavesTheCycleClosedAndThePaymentsCleared(t *testing.T) {
	h := newMeshHarnessWithAnUnfundedReserve(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	h.closeCycle(t)
	h.drain(t)

	if got := h.payment(t, p.ID); got.Status != payment.Cleared {
		t.Errorf("after a refused settlement the payment is %v, want Cleared", got.Status)
	}
	if got := h.suspense(t, h.debtorPID); got != harnessAmount {
		t.Errorf("the payer's bank holds %d in suspense, want %d — nothing settled, so nothing left it", got, harnessAmount)
	}
	settlements, err := h.net.ListSettlements(context.Background())
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	if len(settlements) != 0 {
		t.Errorf("%d settlements recorded; the batch failed whole", len(settlements))
	}
	for _, c := range h.cycles(t) {
		if c.Status != payment.CycleClosed {
			t.Errorf("cycle %s (%s) is %v, want Closed — a refused instruction does not settle it", c.ID, c.Scheme, c.Status)
		}
	}
}

// TestTheMessagesACutOffPutsOnTheWire names the conversation, the way
// TestTheCreditTransferChainIsFourMessages names the push.
//
// It was TestTheSettlementChainIsTwoMessages, and the name outran the code the
// moment this task gave the ACSC a second recipient: six hops, of which only two
// are the chain the name described. Renamed for the reason every other rename in
// this package had — the name is the string a failure prints and a reader greps
// for, and one that claims more than it measures is worse than none.
//
// Two messages between the institutions — the instruction and its answer — and
// then two fan-outs from them, which are two different things addressed to two
// different sets of banks:
//
//   - one camt.053 per MEMBER whose position moved, from the CENTRAL BANK. It is
//     a statement of that member's own reserve account, and it is what the member
//     books its mirror leg from (bank.receiveStatement). Both banks get one here,
//     because both had a non-zero net position.
//   - one pacs.002 per PAYMENT per BANK THAT HAS SOMETHING TO DO ABOUT IT, from
//     the CLEARING HOUSE. That is the bank that submitted, which is waiting for
//     the answer to its instruction, and the CREDITOR's bank, which has a leg to
//     post: settlement moved the reserves, and the payee is paid when its own
//     bank releases the money out of its own suspense. On this push those are two
//     institutions and there are two messages for the one payment; on a pull they
//     are one and there is one. That fan-out could not be the central bank's: it
//     is answering about a cycle, and it holds no method that could turn one into
//     payments.
//
// # It is a SET, plus the three orderings that are actually forced
//
// The three messages this used to assert formed a chain — each was sent by the
// handler of the one before, so their whole order was forced. The statements are
// not in that chain: they go to other actors' inboxes, and those goroutines run
// concurrently with the clearing house's. The tap fires in Mesh.dispatch, on the
// RECEIVING actor's goroutine, so what this test observes is handling order and
// not send order, and a positional assertion over all six would be flaky rather
// than strict.
//
// Three relations survive that, and all three are asserted because "it is a set"
// would give away more than the concurrency takes:
//
//   - the INSTRUCTION is handled first, because every other message here is sent
//     from the handler that receives it;
//   - the central bank's pacs.002 to the clearing house is handled BEFORE the
//     clearing house's pacs.002 to the submitting bank, because the second is
//     sent from the first's handler. Same argument, one hop further along.
//   - the PAYEE's BANK's camt.053 is handled before the ACSC addressed to that
//     same bank. This one is not a chain argument and it is the load-bearing
//     one; see below.
//
// # Why that pair CAN be asserted when the set cannot be ordered
//
// Not merely "both go to one actor". That is necessary and not sufficient — two
// messages racing into one inbox from two goroutines would arrive in either
// order. What forces this pair is a happens-before chain, and it is worth
// spelling out because the obvious way to try to break it does not:
//
//  1. Mesh.send pushes onto the target's queue SYNCHRONOUSLY, in the sender's
//     own goroutine (mesh.sendRaw).
//  2. centralBank.receiveSettlement calls advise before answer, so the camt.053
//     is pushed into the payee's bank's queue before the pacs.002 is pushed into
//     the clearing house's.
//  3. The ACSC does not exist until the clearing house HANDLES that pacs.002,
//     which cannot happen before step 2 pushed it.
//  4. So the camt.053 is already in the payee's bank's queue before the ACSC is
//     created, and Mesh.run is one goroutine popping that queue FIFO with
//     Mesh.dispatch firing the tap immediately before the handler.
//
// Deterministic, then, not lucky — and h.seen's relative index for two messages
// to the same actor is exactly that actor's handling order.
//
// A reader who tries to falsify this by simply swapping advise and answer will
// find the test still passes, and should not conclude the assertion is weak.
// Swapping makes the pair a RACE rather than an inversion — the central bank
// pushes the camt.053 while the clearing house is being scheduled to produce the
// ACSC — and the central bank wins that race almost always. Inverting it takes a
// delay between the answer and the advice, and with one the assertion fails
// every run.
//
// What it pins is the reason centralBank.advise sends the statements before it
// answers the clearing house. The payee's bank is a net RECEIVER here, so its
// camt.053 CREDITS the clearing suspense that the creditor leg then draws on;
// the ACSC is what makes it draw. Reverse the two and that bank pays its
// customer out of a suspense the cut-off has not yet credited — which commits,
// because suspense is a Liability and the ledger does not guard those, and which
// for that interval has the bank's own books saying it lent its customer the
// money.
//
// The PAYER's bank receives a pair too — a camt.053 and, because it submitted,
// the same ACSC — and the same chain orders it. It is not asserted because
// nothing depends on it: that bank has no leg to post from the ACSC, so the
// order buys it nothing. What remains genuinely undetermined is the
// INTERLEAVING across actors: where either of the payer's bank's two messages
// falls relative to either of the payee's bank's.
func TestTheMessagesACutOffPutsOnTheWire(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)

	h.rec.reset()
	before := h.messagesSeen()
	h.closeCycle(t)
	h.drain(t)

	type hop struct {
		from, to iso20022.BIC
		msgDef   string
	}
	instruction := hop{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "pacs.009.001.08"}
	answer := hop{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"}
	fanOut := hop{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.002.001.10"}
	// The two the payee's bank receives, and the pair whose order is forced.
	// The creditor's bank's copy of the advice is the second message a push
	// produces for one payment, and it is the one that causes a POSTING; the
	// statement is what credits the suspense that posting draws on. See
	// bank.receiveStatus and centralBank.advise.
	payeeStatement := hop{h.cfg.CentralBankBIC, h.creditorBIC, "camt.053.001.08"}
	payeeAdvice := hop{h.cfg.ClearingHouseBIC, h.creditorBIC, "pacs.002.001.10"}
	want := map[hop]int{
		instruction:    1,
		answer:         1,
		fanOut:         1,
		payeeStatement: 1,
		payeeAdvice:    1,
		{h.cfg.CentralBankBIC, h.debtorBIC, "camt.053.001.08"}: 1,
	}
	h.mu.Lock()
	seen := append([]tappedMessage(nil), h.seen[before:]...)
	h.mu.Unlock()

	got := map[hop]int{}
	order := map[hop]int{}
	for i, m := range seen {
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			t.Fatalf("message %d does not parse: %v", i, err)
		}
		h := hop{m.from, m.to, env.AppHdr.MsgDefIdr}
		got[h]++
		order[h] = i
		if i == 0 && h != instruction {
			t.Errorf("the cut-off's first message is %s -> %s (%s), want the instruction %s -> %s (%s)",
				h.from, h.to, h.msgDef, instruction.from, instruction.to, instruction.msgDef)
		}
	}
	if !maps.Equal(got, want) {
		t.Fatalf("the cut-off put %v on the wire, want %v", got, want)
	}
	// A chain pair. The clearing house sends the per-payment status FROM the
	// handler of the central bank's answer, so it cannot be handled before it.
	if order[answer] > order[fanOut] {
		t.Errorf("the clearing house's status to %s was handled at %d and the central bank's answer to it at %d; "+
			"the first is sent from the second's handler and cannot precede it",
			h.debtorBIC, order[fanOut], order[answer])
	}
	// The load-bearing pair, and not a chain argument: both of these are handled
	// by the PAYEE'S BANK, one goroutine popping one FIFO queue, so their order
	// here is that bank's handling order. The statement credits the clearing
	// suspense; the advice is what makes the creditor leg draw on it. The other
	// way round and that bank pays its customer out of a suspense the cut-off
	// has not credited yet. centralBank.advise sends before it answers for
	// exactly this reason.
	if order[payeeStatement] > order[payeeAdvice] {
		t.Errorf("the payee's bank handled the ACSC at %d and its own camt.053 at %d; "+
			"the statement credits the suspense the creditor leg draws on and must come first",
			order[payeeAdvice], order[payeeStatement])
	}
}

// TestASettlementInstructionNamingTwoCyclesIsRefused is the guard on the one
// assumption the central bank makes about the message it is handed.
//
// A pacs.009 can carry legs from several cycles — payment.SettlementLeg holds a
// reference apiece precisely so it can, and payment.ReadSettlement reads them —
// and this system's clearing house never sends one, because a cut-off closes one
// cycle. So the receiver states that as a requirement it CHECKS rather than an
// assumption it makes: settling the first cycle it saw and dropping the second
// would leave a closed cycle nobody ever settles and nobody ever hears about.
//
// Injected rather than provoked, because no actor in this mesh emits this
// message. That is what injectRaw is for.
func TestASettlementInstructionNamingTwoCyclesIsRefused(t *testing.T) {
	h := newMeshHarness(t)
	env, err := payment.SettlementMessage([]payment.SettlementLeg{
		{From: h.debtorBIC, To: h.cfg.CentralBankBIC, Amount: harnessAmount, Asset: "EUR", Reference: "cyc_one"},
		{From: h.cfg.CentralBankBIC, To: h.creditorBIC, Amount: harnessAmount, Asset: "EUR", Reference: "cyc_two"},
	}, payment.MessageContext{
		From: h.cfg.ClearingHouseBIC, To: h.cfg.CentralBankBIC, MsgID: "CSM-mixed", Now: testTime,
	})
	if err != nil {
		t.Fatalf("SettlementMessage: %v", err)
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, raw)
	h.drain(t)

	h.assertLastTxStatusTo(t, h.cfg.ClearingHouseBIC, iso20022.TransactionStatusRejected)
	if got := h.lastStatusTo(t, h.cfg.ClearingHouseBIC); !strings.Contains(statusText(got), "cyc_two") {
		t.Errorf("the refusal does not name the second cycle: %v", statusText(got))
	}
	// And nothing was settled on the strength of the leg it could read.
	settlements, err := h.net.ListSettlements(context.Background())
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	if len(settlements) != 0 {
		t.Errorf("%d settlements recorded for an instruction this actor refused", len(settlements))
	}
}

// TestOnlyThePayeesBankPaysThePayee pins which of the two banks told about a
// settled payment may act on it.
//
// On a push the clearing house tells both: the payer's bank because it has been
// waiting for the answer to its instruction, the payee's bank because it has a
// leg to post. If the payer's bank posted it, the payee would be credited in the
// wrong institution's book — the exact crossing this sub-project removes, arrived
// at from the other direction.
//
// It is asserted at the DOMAIN layer, where the refusal lives, because that is
// where it can be made to fail: the mesh never hands the payer's bank's id to a
// payment it does not own, so a mesh-level test would pass with the guard gone.
func TestOnlyThePayeesBankPaysThePayee(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	// The payer's bank asking to post the payee's leg is refused, whatever else
	// is true of the payment.
	_, err := h.net.PostCreditorLeg(context.Background(), h.debtor.ID, p.ID)
	if !errors.Is(err, payment.ErrNotThisBanksPayment) {
		t.Errorf("the payer's bank got %v, want ErrNotThisBanksPayment", err)
	}
}

// statusText is the free text beside the codes of a pacs.002, joined. It is the
// part no code can say, and a refusal that named neither cycle would be one an
// operator could not act on.
func statusText(doc *iso20022.Pacs002) string {
	var out []string
	for _, s := range doc.FIToFIPmtStsRpt.TxInfAndSts {
		if s.StsRsnInf != nil {
			out = append(out, s.StsRsnInf.AddtlInf)
		}
	}
	return strings.Join(out, "; ")
}
