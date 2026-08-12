package main

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
)

// The settlement flow end to end: a cut-off, an instruction, a discharge, and the
// news travelling back to the bank that started the payment. Two assertions on the
// answer, because they say different things: ACSC is the point of finality, and the
// ABSENT reason is what says this is not a rejection — a pacs.002 carries StsRsnInf
// only when something was refused.
func TestClosingACycleSettlesItThroughTheCentralBank(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	h.closeCycle(t)
	h.work(t)

	got := h.payment(t, p.ID)
	if got.Status != payment.Settled {
		t.Fatalf("status = %v, want Settled", got.Status)
	}
	h.assertLastStatusTo(t, h.debtorBIC, "" /* no code */)
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusSettlementCompleted)
}

// A net payer who cannot cover becomes RJCT/AM04 on the settlement
// instruction. It is not a Go error returned to whoever clicked
// settle, which is not something a clearing house can act on.
func TestANetPayerWhoCannotCoverIsRejectedOnTheInstruction(t *testing.T) {
	h := newHarnessWithAnUnfundedReserve(t)
	h.submitCreditTransfer(t)
	h.work(t)

	h.closeCycle(t)
	h.work(t)

	h.assertLastTxStatusTo(t, h.cfg.ClearingHouseBIC, iso20022.TransactionStatusRejected)
	h.assertLastStatusTo(t, h.cfg.ClearingHouseBIC, iso20022.StatusReasonInsufficientFunds)
}

// A cut-off the settlement agent refuses releases NOTHING, and that is what settling
// before releasing is FOR. The payments are Cleared, no reserves have moved, and the
// bank that would be paid has never heard of them — so no customer is credited
// against money that is not there, and there is nothing to unwind when the operator
// funds the short member and instructs again.
func TestARefusedCutOffReleasesNothing(t *testing.T) {
	h := newHarnessWithAnUnfundedReserve(t)
	p := h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	if got := h.payment(t, p.ID); got.Status != payment.Cleared {
		t.Fatalf("payment status = %v, want Cleared — the cut-off has to have failed for this test to say anything", got.Status)
	}
	if files := h.filesOfTypeTo(t, h.creditorBIC, "pacs.008.001.08"); len(files) != 0 {
		t.Errorf("the payee's bank was handed %d instruction files for a cut-off that did not settle, want 0", len(files))
	}
	if _, err := h.bank(h.creditorBIC).GetPayment(context.Background(), p.ID); err == nil {
		t.Error("the payee's bank holds a copy of a payment nobody has settled")
	}
	if bal := h.balance(t, h.creditorPID, h.creditorAcct.ID); bal != 0 {
		t.Errorf("the payee holds %d against a refused cut-off, want 0", bal)
	}
}

// A payment the operator rejects out of an open cycle is CUT OUT of the share its
// neighbours travel in. The share is built when the file is taken in and released
// when the cycle settles, and an operator can reject in between — the one window in
// which a held file can go out of date. A bank handed a rejected transaction would
// credit a payee out of a suspense the reserves never reached.
func TestARejectedPaymentIsCutOutOfTheShareItsNeighboursTravelIn(t *testing.T) {
	h := newHarness(t)
	kept := h.submit(t, h.smallTransfer(t, "invoice 1"))
	pulled := h.submit(t, h.smallTransfer(t, "invoice 2"))
	h.cutOff(t, h.debtorBIC)
	h.work(t)

	if _, err := h.dep.ClearingHouse().Reject(context.Background(), pulled.ID,
		iso20022.StatusReasonDuplication, "cancelled by the payer"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	h.closeCycle(t)
	h.work(t)

	files := h.filesOfTypeTo(t, h.creditorBIC, "pacs.008.001.08")
	if len(files) != 1 {
		t.Fatalf("the payee's bank was handed %d files, want 1", len(files))
	}
	body := files[0].Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf
	if len(body.CdtTrfTxInf) != 1 || body.CdtTrfTxInf[0].PmtId.TxId != string(kept.ID) {
		t.Fatalf("the released share carries %d transactions, want only %s", len(body.CdtTrfTxInf), kept.ID)
	}
	if body.GrpHdr.NbOfTxs != "1" {
		t.Errorf("the share of one asserts NbOfTxs=%q", body.GrpHdr.NbOfTxs)
	}
	// And the payee was paid once, for the transaction that survived.
	if bal := h.balance(t, h.creditorPID, h.creditorAcct.ID); bal != harnessAmount/2 {
		t.Errorf("the payee holds %d, want %d", bal, harnessAmount/2)
	}
	if got := h.payment(t, pulled.ID); got.Status != payment.Rejected {
		t.Errorf("the rejected payment is %v", got.Status)
	}
}

// TestARefusedSettlementCanBeInstructedAgain walks the whole way out of the one state
// this system has no other exit from: a cycle Closed with no settlement, its payments
// Cleared, the payer debited into their own bank's clearing suspense and the payee
// unpaid. No object transitions out of it — CloseCycleTx wants an open cycle,
// RejectAtCSMTx an Initiated or Accepted payment, PostReturnLegTx a settled one — so
// without a way to re-instruct, every payer's money is stranded.
//
// It asserts the stuck state first, as state rather than as a status code, since the
// AM04 is on the wire and in no store. The payee is paid at the end, which is the
// only assertion that says the money arrived rather than that a status changed.
func TestARefusedSettlementCanBeInstructedAgain(t *testing.T) {
	h := newHarnessWithAnUnfundedReserve(t)
	p := h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	// The stuck state, in the four places it shows. Whether a cut-off settled is read
	// off the CYCLE's status and, at the agent, off whether it holds a settlement for
	// that cycle. The cycle cannot name the settlement's id: that is the agent's own
	// row number and nothing on the wire carries it back.
	stuck := h.creditTransferCycle(t)
	if stuck.Status != payment.CycleClosed {
		t.Fatalf("cycle %s is %v, want Closed", stuck.ID, stuck.Status)
	}
	if _, err := h.cb().GetSettlementByCycleID(context.Background(), stuck.ID); !errors.Is(err, payment.ErrSettlementNotFound) {
		t.Fatalf("the settlement agent answers %v for a cycle it refused to settle, want it to hold none", err)
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

	// The operator's remedy: fund the short member, which is TWO acts. A deposit raises
	// the customer's balance and leaves the bank holding vault cash, so on its own it
	// would not unstick this cycle — the central bank's book is what settlement reads.
	// The lodgement is what puts the reserve behind the bank, and it is a real
	// camt.050/camt.025 round trip. A bank settles out of central bank money, and
	// getting some is a conversation.
	if err := h.bank(h.debtorBIC).Deposit(context.Background(), h.debtorPID, h.debtorAcct.ID, harnessAmount, "Reserve top-up"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	h.lodge(t, h.debtorBIC, "EUR", harnessAmount)

	// And ask again. This is the route POST /cycles/{cid}/settle reaches.
	if _, err := h.dep.ClearingHouse().Settle(context.Background(), stuck.ID); err != nil {
		t.Fatalf("Settle %s: %v", stuck.ID, err)
	}
	h.work(t)

	settled := h.creditTransferCycle(t)
	if settled.Status != payment.CycleSettled {
		t.Fatalf("cycle %s is %v, want Settled", settled.ID, settled.Status)
	}
	if _, err := h.cb().GetSettlementByCycleID(context.Background(), settled.ID); err != nil {
		t.Fatalf("the settlement agent holds no settlement for %s: %v", settled.ID, err)
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
// Verified rather than assumed: the count of instructions the central bank has been
// handed says no second pacs.009 went out, and the settlement count says no second
// discharge happened. A guard that only changed the error text would pass an
// assertion on the error alone.
func TestReSettlingASettledCycleIsRefused(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	cyc := h.creditTransferCycle(t)
	if cyc.Status != payment.CycleSettled {
		t.Fatalf("cycle is %v, want Settled before the second ask means anything", cyc.Status)
	}
	instructionsBefore := h.instructionsSentTo(h.cfg.CentralBankBIC)

	_, err := h.dep.ClearingHouse().Settle(context.Background(), cyc.ID)
	if !errors.Is(err, payment.ErrCycleNotClosed) {
		t.Fatalf("Settle on a settled cycle = %v, want ErrCycleNotClosed", err)
	}
	if got := h.instructionsSentTo(h.cfg.CentralBankBIC); got != instructionsBefore {
		t.Fatalf("a refused re-settle sent %d instructions, want the original %d", got, instructionsBefore)
	}

	settlements, err := h.cb().ListSettlements(context.Background())
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	if len(settlements) != 1 {
		t.Fatalf("%d settlements, want the one the cut-off instructed", len(settlements))
	}
}

// TestASecondSettlementInstructionPostsNothing is the guard BEHIND that one: the
// settlement agent's, on a message csm.settle would not have built. Two operators
// racing past the CycleClosed check would each send a pacs.009, and this is what
// happens to the second — the same thing that happens to a redelivered one, the two
// being indistinguishable at the receiver. SettleCycleTx refuses with
// ErrCycleNotClosed and receiveSettlement reports rather than answering RJCT, because
// telling the clearing house a cycle was rejected when it settled would be a lie.
//
// The money assertion is the point: the reserves moved once, the posting carrying the
// idempotency key "<cycle>:settle".
func TestASecondSettlementInstructionPostsNothing(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	cyc := h.creditTransferCycle(t)
	payeeBefore := h.balance(t, h.creditorPID, h.creditorAcct.ID)

	// The instruction the cut-off sent, replayed verbatim into the settlement
	// agent's download queue. Re-sent by the clearing house, because that is who the
	// central bank accepts one from.
	raw := h.lastMessageOfTypeTo(t, h.cfg.CentralBankBIC, "pacs.009.001.08")
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, raw)

	err := h.workErr(t)
	if err == nil || !strings.Contains(err.Error(), "was told to settle") {
		t.Fatalf("the day after a replayed instruction = %v, want a reported problem naming the repeat", err)
	}

	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != payeeBefore {
		t.Fatalf("the payee holds %d after a replayed instruction, want the unchanged %d", got, payeeBefore)
	}
	settlements, err := h.cb().ListSettlements(context.Background())
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	if len(settlements) != 1 {
		t.Fatalf("%d settlements after a replayed instruction, want 1", len(settlements))
	}
	// And the one settlement it holds is still the one it made for this cycle.
	// The link is the settlement agent's own — Settlement.CycleID — because the
	// clearing house's copy of the cycle cannot name a settlement it was never
	// told the id of.
	if settlements[0].CycleID != cyc.ID {
		t.Fatalf("the surviving settlement names cycle %q, want %q", settlements[0].CycleID, cyc.ID)
	}
}

// creditTransferCycle is the fixture's SEPA credit-transfer cycle, read back by
// scheme rather than by index: every fixture opens a direct-debit cycle beside it,
// and which one ListCycles puts first is not something these tests should depend on.
func (h *harness) creditTransferCycle(t *testing.T) payment.ClearingCycle {
	t.Helper()
	for _, c := range h.cycles(t) {
		if c.Scheme == payment.SchemeSEPACT {
			return c
		}
	}
	t.Fatal("no sepa.ct cycle in this fixture")
	return payment.ClearingCycle{}
}

// TestEachMemberBooksTheStatementItWasSent is what the camt.053 is FOR, measured at
// the far end: after a cut-off each member holds its own record of it, posted, with
// the closing balance the settlement agent asserted. Three things per member, each
// ruling out a different way of getting this wrong:
//
//   - Status is Posted and MirrorTx names a transaction. Asserting the row is NOT at
//     Advised is what stops a row recording nothing passing for a booking. It is not
//     the state a failure leaves: the row and the leg are one unit of work.
//   - Movement is SIGNED and opposite at the two banks. It travels as a magnitude plus
//     CdtDbtInd, so a member that lost the sign in transit would post its mirror leg
//     backwards — and the two banks are the only pair that can show it.
//   - ClosingBalance equals what the central bank's book says the reserve account
//     stands at. camt.053 was chosen over camt.054 precisely because it carries a
//     balance to check a posting against.
func TestEachMemberBooksTheStatementItWasSent(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

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
		reserve, err := h.cb().ReserveBalance(context.Background(), iso20022.BIC(member.pid), "EUR")
		if err != nil {
			t.Fatalf("ReserveBalance %s: %v", member.pid, err)
		}
		if advice.ClosingBalance != reserve {
			t.Errorf("%s was told its reserve closed at %d; the central bank's book says %d",
				member.name, advice.ClosingBalance, reserve)
		}
	}
}

// advice is one bank's own record of a cut-off, read out of that bank's own DATABASE.
// Through the store rather than a Network method, because there is no such method: an
// advice is a member's own row. The store has to be that member's own for the same
// reason — settlement_advices is in the bank shape and in no other. The unit of work
// is opened on a bare context, so the recorder attributes the read to no actor.
func (h *harness) advice(t *testing.T, id payment.ParticipantID, reference string) payment.SettlementAdvice {
	t.Helper()
	ctx := context.Background()
	member := h.bank(iso20022.BIC(id))
	p, err := member.GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	var out payment.SettlementAdvice
	if err := member.Store().View(ctx, func(ctx context.Context, tx payment.BankTx) error {
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
	h := newHarnessWithTwoAssets(t)
	h.submitCreditTransfer(t)
	h.submitCreditTransferInUSD(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	if got := h.instructionsSentTo(h.cfg.CentralBankBIC); got != 2 {
		t.Fatalf("sent %d settlement instructions, want one per asset", got)
	}
}

// TestASettledCollectionIsAnnouncedToThePayeesBank is the release's other direction,
// and the one that says who is actually waiting. On a push the bank that submitted
// and the bank owed an answer are the same, so an advice that simply addressed the
// payer would look right. On a PULL they are opposite: the payee's bank sent the
// collection and has been waiting since.
//
// Both banks hear from the clearing house at the cut-off and they hear different
// things. The submitter gets a pacs.002 saying ACSC. The payer's bank gets the
// pacs.003 itself, for the first time, because until the cycle settled there was
// nothing safe to hand it: that file is both its instruction and its news.
//
// The counts catch a release that sent the same thing twice — both guards make a
// second one a no-op, so the money would be right and only the conversation wrong.
func TestASettledCollectionIsAnnouncedToThePayeesBank(t *testing.T) {
	h := newHarness(t)
	p := h.submitDirectDebit(t)
	h.work(t)

	// Counted from here, so the ACCP this bank was already sent when it
	// submitted is not in the total.
	before := h.statusesSentTo(h.creditorBIC)
	h.closeCycle(t)
	h.work(t)

	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("status = %v, want Settled", got.Status)
	}
	h.assertLastTxStatusTo(t, h.creditorBIC, iso20022.TransactionStatusSettlementCompleted)
	if got := h.statusesSentTo(h.creditorBIC) - before; got != 1 {
		t.Errorf("the payee's bank was sent %d statuses over the cut-off; it is the submitter AND the creditor's bank, which is one message", got)
	}
	// The payer's bank was sent the collection and no status at all. It asked
	// nothing, so there is nothing to answer it with; what it needs is the
	// instruction, and one is what it got.
	if got := h.messagesSentTo(h.debtorBIC, "pacs.003.001.08"); got != 1 {
		t.Errorf("the payer's bank was handed %d collections over the cut-off, want 1", got)
	}
	if got := h.statusesSentTo(h.debtorBIC); got != 0 {
		t.Errorf("the payer's bank was sent %d statuses; it never asked this network anything", got)
	}
	// And it executed what it was handed, which is the last posting the payment
	// needs and the only one that touches the payer's account.
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.Status != payment.Settled {
		t.Errorf("the payer's own copy is %v after the cut-off, want Settled", got.Status)
	}
}

// TestEachSettlementInstructionCarriesOneAssetsLegs is what the count above cannot
// see: two instructions is the right number for two assets AND for two cycles that
// happened to share one. This reads the messages — each carries the legs of exactly
// one asset, and between them they carry both.
//
// What that rules out is a single instruction whose legs are half in euro and half in
// dollars, which a pacs.009 can express (ReadSettlement reads each leg's own
// currency, deliberately) and which would net a euro position against a dollar one.
func TestEachSettlementInstructionCarriesOneAssetsLegs(t *testing.T) {
	h := newHarnessWithTwoAssets(t)
	h.submitCreditTransfer(t)
	h.submitCreditTransferInUSD(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

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

// TestASettlementInstructionRunsBetweenBanksAndTheCentralBank is the shape of what
// the clearing house asks for, read off the wire. A netted position has no
// counterparty among the banks — that is what netting destroys — so every leg runs
// between one member and the SETTLEMENT AGENT. Getting the direction backwards would
// settle the cycle the wrong way round while every total still balanced.
func TestASettlementInstructionRunsBetweenBanksAndTheCentralBank(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

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

// TestAnEmptyCycleInstructsNothing is the other half of the cut-off. Every
// credit-transfer test closes an untouched direct-debit window alongside the one it
// filled, so this path runs constantly and no other assertion looks at it: a clearing
// house that sent an empty instruction would be caught only by the codec refusing to
// build a pacs.009 with no transactions. Nothing to discharge is not a failure — the
// cycle closes, records positions that are all zero, and stays Closed.
func TestAnEmptyCycleInstructsNothing(t *testing.T) {
	h := newHarness(t)
	h.closeCycle(t) // both windows are open and neither has a payment in it
	h.work(t)

	if got := h.instructionsSentTo(h.cfg.CentralBankBIC); got != 0 {
		t.Errorf("closing two empty cycles sent %d settlement instructions, want none", got)
	}
	for _, c := range h.cycles(t) {
		if c.Status != payment.CycleClosed {
			t.Errorf("cycle %s (%s) is %v after the cut-off, want Closed", c.ID, c.Scheme, c.Status)
		}
		if _, err := h.cb().GetSettlementByCycleID(context.Background(), c.ID); !errors.Is(err, payment.ErrSettlementNotFound) {
			t.Errorf("the settlement agent answers %v for cycle %s; there was nothing in it to settle", err, c.ID)
		}
	}
}

// TestARefusedSettlementLeavesTheCycleClosedAndThePaymentsCleared measures what the
// RJCT costs. The batch fails WHOLE, SettleCycleTx being one unit of work, so the
// assertion is on the state everywhere: no settlement recorded, the cycle still
// Closed, every payment still Cleared with the payer's money in its own bank's
// clearing suspense. That is why the console can name the reason without one being
// stored — the net position and the reserve are both right there.
//
// It also pins what receiveSettlementStatus does NOT do: no bank is told, and the
// day's report would say so if one were.
func TestARefusedSettlementLeavesTheCycleClosedAndThePaymentsCleared(t *testing.T) {
	h := newHarnessWithAnUnfundedReserve(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	h.closeCycle(t)
	h.work(t)

	if got := h.payment(t, p.ID); got.Status != payment.Cleared {
		t.Errorf("after a refused settlement the payment is %v, want Cleared", got.Status)
	}
	if got := h.suspense(t, h.debtorPID); got != harnessAmount {
		t.Errorf("the payer's bank holds %d in suspense, want %d — nothing settled, so nothing left it", got, harnessAmount)
	}
	settlements, err := h.cb().ListSettlements(context.Background())
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
// Six hops, of which two are the chain a reader might expect — the instruction and
// its answer — and then two fan-outs addressed to two different sets of banks:
//
//   - one camt.053 per MEMBER whose position moved, from the CENTRAL BANK: a statement
//     of that member's own reserve account, and what it books its mirror leg from.
//   - one pacs.002 per PAYMENT per BANK THAT HAS SOMETHING TO DO ABOUT IT, from the
//     CLEARING HOUSE: the bank that submitted, and the CREDITOR's bank, which has a
//     leg to post. On this push those are two institutions; on a pull they are one.
//     That fan-out could not be the central bank's — it is answering about a cycle
//     and holds no method that could turn one into payments.
//
// It is a SET plus the orderings that are forced. The three files of the chain are
// each built by the institution that collected the one before. The statements are
// not: they go into other banks' queues, and a queue has no order against another.
//
// Three relations survive: the INSTRUCTION crosses first; the agent's pacs.002 crosses
// before the clearing house's; and the PAYEE's BANK's camt.053 crosses before the ACSC
// addressed to that same bank. The last is the load-bearing one and is not a chain
// argument: it is what lets the payee's bank's creditor legs draw on a suspense the
// camt.053 has already credited.
func TestTheMessagesACutOffPutsOnTheWire(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)

	h.rec.reset()
	before := h.messagesSeen()
	h.closeCycle(t)
	h.work(t)

	type hop struct {
		from, to iso20022.BIC
		msgDef   string
	}
	instruction := hop{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "pacs.009.001.08"}
	answer := hop{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"}
	fanOut := hop{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.002.001.10"}
	// The two the payee's bank receives, and the pair whose order is forced. The
	// INSTRUCTION is what that bank is handed at the cut-off and what causes the
	// POSTING; the statement is what credits the suspense that posting draws on.
	payeeStatement := hop{h.cfg.CentralBankBIC, h.creditorBIC, "camt.053.001.08"}
	payeeFile := hop{h.cfg.ClearingHouseBIC, h.creditorBIC, "pacs.008.001.08"}
	want := map[hop]int{
		instruction:    1,
		answer:         1,
		fanOut:         1,
		payeeStatement: 1,
		payeeFile:      1,
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
	// The load-bearing pair, and not a chain argument: both are handled by the PAYEE'S
	// BANK, which collects from the settlement agent before the clearing house. The
	// statement credits the clearing suspense; the released instruction is what makes
	// the creditor leg draw on it.
	if order[payeeStatement] > order[payeeFile] {
		t.Errorf("the payee's bank handled its released instruction at %d and its own camt.053 at %d; "+
			"the statement credits the suspense the creditor leg draws on and must come first",
			order[payeeFile], order[payeeStatement])
	}
}

// TestASettlementInstructionNamingTwoCyclesIsRefused guards the one assumption the
// central bank makes about the message it is handed. A pacs.009 can carry legs from
// several cycles — SettlementLeg holds a reference apiece precisely so it can — and
// this system's clearing house never sends one. So the receiver CHECKS it rather than
// assuming: settling the first cycle and dropping the second would leave a closed
// cycle nobody ever settles and nobody ever hears about. Injected, since no actor here
// emits this message.
func TestASettlementInstructionNamingTwoCyclesIsRefused(t *testing.T) {
	h := newHarness(t)
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
	h.work(t)

	h.assertLastTxStatusTo(t, h.cfg.ClearingHouseBIC, iso20022.TransactionStatusRejected)
	if got := h.lastStatusTo(t, h.cfg.ClearingHouseBIC); !strings.Contains(statusText(got), "cyc_two") {
		t.Errorf("the refusal does not name the second cycle: %v", statusText(got))
	}
	// And nothing was settled on the strength of the leg it could read.
	settlements, err := h.cb().ListSettlements(context.Background())
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	if len(settlements) != 0 {
		t.Errorf("%d settlements recorded for an instruction this actor refused", len(settlements))
	}
}

// TestOnlyThePayeesBankPaysThePayee pins which of the two banks told about a settled
// payment posts the CREDITOR's leg. On a push the clearing house tells both: the
// payer's bank because it has been waiting for the answer, the payee's because it has
// a leg. If the payer's bank posted it, the payee would be credited in the wrong
// institution's book.
//
// Both banks have a row to advance and only one has a leg, so SettleAtBankTx makes
// the status unconditional and the posting conditional; a bank with no creditor leg
// is not turned away. The claim is therefore on the LEDGER: the payer's bank ends the
// cut-off having posted nothing for this payment, however many times it is asked.
//
// A refusal is still made, by a bank party to NEITHER side, and it is the STORE that
// makes it — an institution never sent this payment holds no row for it, which cannot
// be got past by a bank that knows the id.
func TestOnlyThePayeesBankPaysThePayee(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	// The payee was paid, by its own bank, and the leg is named on that bank's
	// own copy.
	if got := h.bankPayment(t, h.creditorBIC, p.ID); got.CreditorLegAccount == "" {
		t.Error("the payee's bank names no account for the creditor leg; it is the bank that posts one")
	}
	// The payer's bank recorded the settlement and posted no customer leg for
	// it, which is the whole of what a bank on that side does.
	payer := h.bankPayment(t, h.debtorBIC, p.ID)
	if payer.Status != payment.Settled {
		t.Errorf("the payer's bank's own copy is %v after the cut-off, want Settled", payer.Status)
	}
	if payer.CreditorLegAccount != "" {
		t.Errorf("the payer's bank names %q as the creditor leg's account; the payee banks elsewhere", payer.CreditorLegAccount)
	}

	// And asking it again changes nothing — no second row, no posting, no error
	// a caller has to interpret.
	h.rec.reset()
	if _, err := h.bank(h.debtorBIC).SettleAtBank(context.Background(), p.ID); err != nil {
		t.Errorf("the payer's bank got %v asking about its own settled payment, want no error", err)
	}
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != harnessAmount {
		t.Errorf("the payee holds %d after the payer's bank asked again, want %d", got, harnessAmount)
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

// A cut-off in which every position cancels settles, and its files are released. Two
// banks pay each other the same amount inside one cycle, so netting leaves both owing
// nothing. There is no leg to send and therefore no pacs.009, and unless the clearing
// house discharges such a cut-off itself the batch has nowhere to go: no settlement
// agent answers for a file nobody uploaded.
//
// The measure is what a payee can spend, not what a status column says: each customer
// ends the day with what they started with, which is only true if both instructions
// were released AND applied. recon.Check is the other half.
func TestACutOffThatNetsToNothingSettlesAndReleasesItsFiles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// The payee's own opening balance. This fixture funds one side, and a cycle
	// that nets to nothing needs both banks to be paying.
	if err := h.bank(h.creditorBIC).Deposit(ctx, h.creditorPID, h.creditorAcct.ID, harnessFunding, "Opening deposit"); err != nil {
		t.Fatalf("funding the payee: %v", err)
	}

	there := h.submitCreditTransfer(t)
	back := h.submit(t, h.reverseCreditTransfer(t))

	h.day(t)

	for _, p := range []payment.Payment{there, back} {
		if got := h.payment(t, p.ID); got.Status != payment.Settled {
			t.Errorf("%s is %v after the day that netted its cycle to nothing, want Settled", p.ID, got.Status)
		}
	}
	// Each customer is back where they started, which is what makes this a cycle
	// that netted to nothing rather than two payments that went nowhere.
	if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding {
		t.Errorf("the first payer holds %d, want %d", got, harnessFunding)
	}
	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != harnessFunding {
		t.Errorf("the second payer holds %d, want %d", got, harnessFunding)
	}
	recon.Check(t, h.nets)
}

// And the settlement agent is never asked about it, which the balances above cannot
// say. A cut-off with nothing to discharge is settled by the institution that netted
// it: no instruction uploaded, no reserve moved, no settlement row anywhere. A
// clearing house that sent a pacs.009 of zero-amount legs would move no money either,
// and this is what tells the two apart.
func TestACutOffThatNetsToNothingInstructsNobody(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.bank(h.creditorBIC).Deposit(ctx, h.creditorPID, h.creditorAcct.ID, harnessFunding, "Opening deposit"); err != nil {
		t.Fatalf("funding the payee: %v", err)
	}
	h.submitCreditTransfer(t)
	h.submit(t, h.reverseCreditTransfer(t))

	before := h.centralBankTransactionCount(t)
	h.day(t)

	if got := h.centralBankTransactionCount(t); got != before {
		t.Errorf("the settlement agent posted %d transactions for a cut-off that netted to nothing, want none", got-before)
	}
	settlements, err := h.cb().ListSettlements(ctx)
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	if len(settlements) != 0 {
		t.Errorf("the settlement agent holds %d settlements, and nobody instructed one", len(settlements))
	}
}

// A cut-off whose instructions this institution could not hand over is refused before
// the reserves move. Settling one would be final at the settlement agent and reach no
// receiving bank, leaving a payee unpaid with the money standing in their own bank's
// clearing suspense and no act able to move it.
//
// The state is BUILT, and that is what it costs to reach. No deployment this
// repository builds hands over a cut-off that cannot be released, and nor does a
// restart, the shares being rows in the clearing house's own database. What is left is
// a process that ends between accepting a file's transactions into a cycle and
// recording the share behind them — two units of work, in that order — so that is what
// is composed here.
//
// It goes through CloseCycle because that is the door an operator reaches first, and
// it refuses at the seam that matters. Settle is asked afterwards, since
// re-instructing is the one route out of a closed-and-undischarged cycle and must not
// be a way around this. It asserts the WHOLE of what the refusal preserves.
func TestACutOffThatCouldNotBeReleasedIsRefusedBeforeTheReservesMove(t *testing.T) {
	ctx := context.Background()
	s := newAPIHarness(t)
	csm := s.dep.ClearingHouse()

	cycles, err := s.nets.ClearingHouse().ListCycles(ctx)
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	var target payment.ClearingCycle
	for _, c := range cycles {
		if c.Status == payment.CycleOpen && len(c.PaymentIDs) > 0 {
			target = c
			break
		}
	}
	if target.ID == "" {
		t.Fatal("the seeded dataset holds no open cycle with payments in it; this test has nothing to lose the shares of")
	}
	files, err := csm.ops.ListHeldFiles(ctx, target.ID)
	if err != nil {
		t.Fatalf("ListHeldFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("the clearing house held no share for %s before this test dropped them; there was nothing to lose", target.ID)
	}
	// ONE cut-off's shares rather than every cut-off's, which is what leaves the
	// OTHER open cycle holding its own — a day that refused everything would say
	// nothing about refusing this one.
	dropEveryShare(t, csm, target.ID)

	settlementsBefore, err := s.nets.CentralBank().ListSettlements(ctx)
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}

	switch _, err := csm.CloseCycle(ctx, target.ID); {
	case err == nil:
		t.Fatalf("%s was instructed for settlement; the shares behind its payments are gone, so no receiving bank could have been told", target.ID)
	case !errors.Is(err, payment.ErrCycleNotReleasable):
		t.Fatalf("closing %s failed with %v, want ErrCycleNotReleasable", target.ID, err)
	}

	// And the way OUT of a closed cycle is shut too. Re-instructing is what an
	// operator does about a cut-off the agent refused, and it must not be a way
	// round this one.
	switch _, err := csm.Settle(ctx, target.ID); {
	case err == nil:
		t.Fatalf("%s was re-instructed for settlement, and the shares behind it are still gone", target.ID)
	case !errors.Is(err, payment.ErrCycleNotReleasable):
		t.Fatalf("re-instructing %s failed with %v, want ErrCycleNotReleasable", target.ID, err)
	}

	// And a business day does not reach it either. It settles the OTHER cut-off
	// the seed left open, which is the point of asserting per cycle below rather
	// than on a total: a day that refused everything would prove nothing.
	if _, err := s.dep.AdvanceDay(ctx); err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}

	after, err := s.nets.ClearingHouse().GetCycle(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetCycle: %v", err)
	}
	if after.Status != payment.CycleClosed {
		t.Errorf("%s is %v after being refused, want Closed — the cut-off left it there and nothing here may move it", target.ID, after.Status)
	}
	for _, id := range target.PaymentIDs {
		p, err := s.nets.ClearingHouse().GetPayment(ctx, id)
		if err != nil {
			t.Fatalf("GetPayment %s: %v", id, err)
		}
		if p.Status != payment.Cleared {
			t.Errorf("payment %s is %v, want Cleared; a refused settlement moves no payment", id, p.Status)
		}
	}
	settlementsAfter, err := s.nets.CentralBank().ListSettlements(ctx)
	if err != nil {
		t.Fatalf("ListSettlements: %v", err)
	}
	for _, st := range settlementsAfter {
		if st.CycleID == target.ID {
			t.Errorf("the settlement agent discharged %s; the refusal was supposed to come before the reserves", target.ID)
		}
	}
	if len(settlementsAfter) <= len(settlementsBefore) {
		t.Errorf("the settlement agent holds %d settlements and held %d; the day settled nothing at all, so refusing this one says nothing",
			len(settlementsAfter), len(settlementsBefore))
	}
}

// And a cut-off in which every position cancels is refused on the same ground. Such a
// batch instructs nothing, and the clearing house discharges it itself, which makes
// the release the only thing standing between two payers' money and their payees. A
// cut-off whose shares are gone would go Settled with no instruction reaching either
// receiving bank. So the refusal is asked before the legs are counted: an empty
// instruction is not the same thing as nothing owed. Both operator doors are tried.
func TestACutOffThatNetsToNothingAndCannotBeReleasedIsRefusedToo(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Both banks paying, which is what makes the positions cancel. See
	// TestACutOffThatNetsToNothingSettlesAndReleasesItsFiles.
	if err := h.bank(h.creditorBIC).Deposit(ctx, h.creditorPID, h.creditorAcct.ID, harnessFunding, "Opening deposit"); err != nil {
		t.Fatalf("funding the payee: %v", err)
	}
	there := h.submitCreditTransfer(t)
	back := h.submit(t, h.reverseCreditTransfer(t))
	h.work(t)

	csm := h.dep.ClearingHouse()
	target := openCycleHolding(t, h, there.ID)
	if !slices.Contains(target.PaymentIDs, back.ID) {
		t.Fatalf("%s and %s are in different cycles; nothing here would net to nothing", there.ID, back.ID)
	}

	// The state this is about: the payments are in the cut-off and the shares
	// behind them are not. See the test above for what reaches it.
	files, err := csm.ops.ListHeldFiles(ctx, target.ID)
	if err != nil {
		t.Fatalf("ListHeldFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("the clearing house held no share for %s; there was nothing to lose", target.ID)
	}
	dropEveryShare(t, csm, target.ID)

	switch _, err := csm.CloseCycle(ctx, target.ID); {
	case err == nil:
		t.Fatalf("%s was cut off without complaint; it nets to nothing, so nothing else would ever hand its payments over", target.ID)
	case !errors.Is(err, payment.ErrCycleNotReleasable):
		t.Fatalf("closing %s failed with %v, want ErrCycleNotReleasable", target.ID, err)
	}
	switch _, err := csm.Settle(ctx, target.ID); {
	case err == nil:
		t.Fatalf("%s was re-instructed, and the shares behind it are still gone", target.ID)
	case !errors.Is(err, payment.ErrCycleNotReleasable):
		t.Fatalf("re-instructing %s failed with %v, want ErrCycleNotReleasable", target.ID, err)
	}

	// And the sweep that exists for cut-offs nobody will answer for does not
	// reach it either, which is the state the refusal is protecting: a payment
	// that goes Settled here is one nothing can hand over afterwards.
	if _, err := h.dep.AdvanceDay(ctx); err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	for _, id := range []payment.PaymentID{there.ID, back.ID} {
		p, err := h.net.GetPayment(ctx, id)
		if err != nil {
			t.Fatalf("GetPayment %s: %v", id, err)
		}
		if p.Status != payment.Cleared {
			t.Errorf("payment %s is %v after the day, want Cleared; a refused cut-off settles nothing", id, p.Status)
		}
	}
}

// A share this institution could not hand over is still held after the cut-off
// settles, and the ones it did hand over are gone.
//
// The hand-over and the discharge of the obligation behind it are two units of work —
// a download queue is the transport's table — so what stands between them is the order
// they run in. A share deleted whatever became of the queueing is an obligation
// destroyed against reserves that have already moved.
//
// It is asserted per share: one bank's enrolment is taken away between the cut-off and
// the release, and the other bank's share must NOT survive, because a share left
// behind is released again by a redelivered answer.
func TestAShareThatCouldNotBeHandedOverIsStillHeld(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	third, carla := h.aThirdBank(t)
	reachable := h.submit(t, h.creditTransferToAccount(t, carla, "invoice 44"))
	stranded := h.submitCreditTransfer(t)
	h.work(t)

	csm := h.dep.ClearingHouse()
	target := openCycleHolding(t, h, stranded.ID)
	if !slices.Contains(target.PaymentIDs, reachable.ID) {
		t.Fatalf("%s and %s are in different cycles; this test needs one cut-off holding both", stranded.ID, reachable.ID)
	}
	h.closeCycle(t)

	// The payee's bank loses its enrolment between the cut-off and the release,
	// so the share standing for it has nowhere to go. Enrolment is what creates a
	// queue, which is why this is the failure a receiving bank can really have.
	csm.host.Reset()
	for _, bic := range []iso20022.BIC{h.debtorBIC, third.BIC} {
		csm.host.Enrol(ebics.SubscriberID(bic))
	}

	if err := h.workErr(t); err == nil {
		t.Fatal("the release reported nothing; a share it could not hand over is a fault the day has to carry")
	}

	files, err := csm.ops.ListHeldFiles(ctx, target.ID)
	if err != nil {
		t.Fatalf("ListHeldFiles: %v", err)
	}
	held := make(map[iso20022.BIC]bool, len(files))
	for _, f := range files {
		held[f.Destination] = true
	}
	if !held[h.creditorBIC] {
		t.Errorf("%s's share of %s is gone and it was never handed over; the payment is in nobody's hands and the reserves have moved",
			h.creditorBIC, target.ID)
	}
	if held[third.BIC] {
		t.Errorf("%s's share of %s is still held after being handed over; a redelivered answer would queue it a second time",
			third.BIC, target.ID)
	}
}

// dropEveryShare composes the state a process ending between accepting a file's
// transactions into a cycle and recording the shares behind them would leave: a
// cut-off holding payments no output file stands for. One share at a time, because
// that is the only way to discharge one.
func dropEveryShare(t *testing.T, c *ClearingHouse, id payment.CycleID) {
	t.Helper()
	ctx := context.Background()
	files, err := c.ops.ListHeldFiles(ctx, id)
	if err != nil {
		t.Fatalf("ListHeldFiles: %v", err)
	}
	for _, f := range files {
		if err := c.ops.DropHeldFile(ctx, id, f.Seq); err != nil {
			t.Fatalf("DropHeldFile %s/%d: %v", id, f.Seq, err)
		}
	}
}

// openCycleHolding is the open cut-off one payment was accepted into.
func openCycleHolding(t *testing.T, h *harness, id payment.PaymentID) payment.ClearingCycle {
	t.Helper()
	cycles, err := h.net.ListCycles(context.Background())
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	for _, c := range cycles {
		if c.Status == payment.CycleOpen && slices.Contains(c.PaymentIDs, id) {
			return c
		}
	}
	t.Fatalf("no open cycle holds %s", id)
	return payment.ClearingCycle{}
}
