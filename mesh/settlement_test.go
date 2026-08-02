package mesh

import (
	"context"
	"errors"
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
// ReturnPaymentTx a settled one. SettleCycle's only non-seed caller is the
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

	// The operator's remedy: fund the short member. A deposit raises the
	// customer's balance and the bank's reserve together, which is what makes it
	// the fixture's way of putting reserves behind a bank that lent without any.
	if err := h.net.Deposit(context.Background(), h.debtorPID, h.debtorAcct.ID, harnessAmount, "Reserve top-up"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

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
// actually paid — the creditor leg posts here and nowhere else — so it is the
// message that closes the thing it started.
//
// The payer's bank is told NOTHING, and that is the assertion beside it. The
// last thing it heard was the clearing house's ACCP, and a system that announced
// settlement to every party to a payment would be one where "who is waiting for
// this" had stopped meaning anything.
func TestASettledCollectionIsAnnouncedToThePayeesBank(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitDirectDebit(t)
	h.drain(t)

	h.closeCycle(t)
	h.drain(t)

	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("status = %v, want Settled", got.Status)
	}
	h.assertLastTxStatusTo(t, h.creditorBIC, iso20022.TransactionStatusSettlementCompleted)
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

// TestTheSettlementChainIsTwoMessages names the conversation, the way
// TestTheCreditTransferChainIsFourMessages names the push.
//
// Two between the institutions — the instruction and its answer — and then one
// per PAYMENT out to the banks that submitted them. The fan-out is the clearing
// house's and could not be the central bank's: it is answering about a cycle,
// and it holds no method that could turn one into payments.
func TestTheSettlementChainIsTwoMessages(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)

	h.rec.reset()
	before := h.messagesSeen()
	h.closeCycle(t)
	h.drain(t)

	want := []struct {
		from, to iso20022.BIC
		msgDef   string
	}{
		{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "pacs.009.001.08"},
		{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"},
		{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.002.001.10"},
	}
	h.mu.Lock()
	seen := append([]tappedMessage(nil), h.seen[before:]...)
	h.mu.Unlock()

	if len(seen) != len(want) {
		t.Fatalf("the cut-off put %d messages on the wire, want %d", len(seen), len(want))
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
