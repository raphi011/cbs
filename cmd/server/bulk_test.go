package main

import (
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// What a bulk network does that a message-per-payment network cannot: hold
// instructions until a cut-off, carry a file of many, sort one file into
// several, and answer a file with one report per transaction.
//
// Every test here is about a COUNT — how many files crossed, how many
// transactions each carried, how many reports came back — because that is the
// whole of what changes when a batch of one becomes a batch. The flows
// themselves are sct_test.go's and sdd_test.go's and are unchanged.

const pacs008 = "pacs.008.001.08"

// smallTransfer is the harness's credit transfer at half the usual amount, so
// that three of them fit inside one payer's funding.
//
// Three rather than two, because two cannot distinguish a file built in the
// hub's order from one built in reverse: a batch has to be longer than its own
// symmetry before the order of it is an assertion.
func (h *harness) smallTransfer(t *testing.T, reference string) payment.InitiatePaymentRequest {
	t.Helper()
	req := h.creditTransferToAccount(t, h.creditorAcct, reference)
	req.Amount = harnessAmount / 2
	return req
}

// A submission goes nowhere. It is the first thing a reader of this package has
// to know, and it is what the 202 on POST /payments now means.
func TestAnInstructionWaitsInTheHubUntilACutOff(t *testing.T) {
	h := newHarness(t)

	p := h.submitCreditTransfer(t)
	if got := h.messagesSeen(); got != 0 {
		t.Fatalf("a submission put %d files on the wire; a bulk network sends at a cut-off and not before", got)
	}
	pending := h.pending(t, h.debtorBIC)
	if len(pending) != 1 || pending[0].ID != p.ID {
		t.Fatalf("the payer's bank is holding %v, want just %s", idsOf(pending), p.ID)
	}
	// And the counterparty has not been told, which is the difference the hub
	// makes visible: the payment is Initiated, and so is a payment that has been
	// uploaded and not yet answered.
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.Status != payment.Initiated {
		t.Errorf("the payer's bank records %v before the cut-off, want Initiated", got.Status)
	}

	orders := h.cutOff(t, h.debtorBIC)
	if len(orders) != 1 {
		t.Fatalf("the cut-off uploaded %d files, want 1", len(orders))
	}
	if got := len(h.pending(t, h.debtorBIC)); got != 0 {
		t.Errorf("the hub still holds %d instructions after a cut-off; it is emptied into the file", got)
	}
	if files := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, pacs008); len(files) != 1 {
		t.Fatalf("the clearing house was handed %d credit-transfer files, want 1", len(files))
	}
}

// A morning's instructions become ONE file. This is the claim decision 4 of the
// design is about, and the one a batch of one cannot make.
func TestACutOffPutsAMorningsInstructionsInOneFile(t *testing.T) {
	h := newHarness(t)

	var ids []payment.PaymentID
	for _, ref := range []string{"invoice 1", "invoice 2", "invoice 3"} {
		ids = append(ids, h.submit(t, h.smallTransfer(t, ref)).ID)
	}
	h.cutOff(t, h.debtorBIC)

	files := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, pacs008)
	if len(files) != 1 {
		t.Fatalf("three instructions became %d files, want 1", len(files))
	}
	body := files[0].Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf
	if len(body.CdtTrfTxInf) != 3 {
		t.Fatalf("the file carries %d transactions, want 3", len(body.CdtTrfTxInf))
	}
	// NbOfTxs is what the sender ASSERTS, and a receiver holds it to it. A file
	// of three claiming one is a truncated file, which is the failure the element
	// exists to catch.
	if body.GrpHdr.NbOfTxs != "3" {
		t.Errorf("the file asserts NbOfTxs=%q over three transactions", body.GrpHdr.NbOfTxs)
	}
	// The transactions are in the order the customers handed them in. A hub is a
	// queue, and two runs that ordered a file differently would build two
	// different files out of one morning.
	for i, want := range ids {
		if got := body.CdtTrfTxInf[i].PmtId.TxId; got != string(want) {
			t.Errorf("transaction %d is %s, want %s — the file is not in the order the hub took them", i, got, want)
		}
	}
}

// One file per SCHEME, because the scheme decides the message definition and the
// asset every amount in the file is denominated in.
//
// The two-asset fixture is what makes this observable from ONE bank: a euro
// credit transfer and a dollar one are both pushes submitted by the payer's
// bank, so both are in the same hub, and nothing but the scheme separates them.
func TestACutOffBuildsOneFilePerScheme(t *testing.T) {
	h := newHarnessWithTwoAssets(t)

	h.submitCreditTransfer(t)
	h.submitCreditTransferInUSD(t)

	orders := h.cutOff(t, h.debtorBIC)
	if len(orders) != 2 {
		t.Fatalf("two schemes were cut off into %d files, want 2", len(orders))
	}
	files := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, pacs008)
	if len(files) != 2 {
		t.Fatalf("the clearing house was handed %d files, want 2", len(files))
	}
	assets := map[string]int{}
	for _, env := range files {
		body := env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf
		if len(body.CdtTrfTxInf) != 1 {
			t.Fatalf("a file carries %d transactions; each scheme had one instruction", len(body.CdtTrfTxInf))
		}
		assets[body.CdtTrfTxInf[0].IntrBkSttlmAmt.Ccy]++
	}
	if assets["EUR"] != 1 || assets["USD"] != 1 {
		t.Errorf("the cut-off produced %v, want one file in each asset", assets)
	}
}

// The fan-out: one file in, one file per receiving bank out.
//
// This is what a clearing house is FOR, and it is the act no member could
// perform for itself — a submitting bank addresses its payees and has no way to
// reach any bank but this one, so somebody has to sort the morning's file by
// creditor agent. A two-bank network cannot tell sorting from forwarding.
func TestTheClearingHouseSortsAFileByCreditorAgent(t *testing.T) {
	h := newHarness(t)
	third, carla := h.aThirdBank(t)

	h.submit(t, h.creditTransferToAccount(t, h.creditorAcct, "to Banca Verde"))
	h.submit(t, h.creditTransferToAccount(t, carla, "to Banco Tercero"))
	h.cutOff(t, h.debtorBIC)

	if files := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, pacs008); len(files) != 1 {
		t.Fatalf("the payer's bank uploaded %d files, want 1 carrying both payees", len(files))
	}
	h.work(t)

	for _, want := range []struct {
		to        iso20022.BIC
		reference string
	}{
		{h.creditorBIC, "to Banca Verde"},
		{third.BIC, "to Banco Tercero"},
	} {
		files := h.filesOfTypeTo(t, want.to, pacs008)
		if len(files) != 1 {
			t.Fatalf("%s was handed %d files, want exactly its own share", want.to, len(files))
		}
		body := files[0].Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf
		if len(body.CdtTrfTxInf) != 1 {
			t.Fatalf("%s was handed %d transactions, want only the one addressed to it", want.to, len(body.CdtTrfTxInf))
		}
		if got := body.CdtTrfTxInf[0].RmtInf.Ustrd; got != want.reference {
			t.Errorf("%s was handed %q; the sort sent it another bank's payment", want.to, got)
		}
		// The COUNT is re-asserted for the file that was actually sent. The
		// clearing house is this file's sender, and a receiving bank holds a sender
		// to its own count — a share of two claiming NbOfTxs=2 is a file the
		// receiver refuses.
		if body.GrpHdr.NbOfTxs != "1" {
			t.Errorf("%s was handed a share of one asserting NbOfTxs=%q", want.to, body.GrpHdr.NbOfTxs)
		}
		// And the MsgId is still the submitting bank's, which is what lets the
		// answer be matched to the original all the way home.
		if body.GrpHdr.MsgId == "" {
			t.Errorf("%s was handed a file with no message id to refer back to", want.to)
		}
	}
}

// A file whose transactions did not all go the same way comes back PART.
//
// payment's groupStatusOf has always computed it and has never been able to see
// a mixed file, because until a file could carry more than one transaction there
// was nothing for a group status to summarise. This is the test that gives it
// one: two credit transfers to the same bank, one payee it holds and one it does
// not.
func TestAMixedFileIsAnsweredPART(t *testing.T) {
	h := newHarness(t)

	good := h.submit(t, h.creditTransferToAccount(t, h.creditorAcct, "payable"))
	bad := h.submit(t, h.creditTransferRequestTo(t, unknownIBANAt(h.creditor)))
	h.cutOff(t, h.debtorBIC)
	h.work(t)

	// ONE answer, carrying both decisions. A receiving bank is handed a batch and
	// reports on a batch.
	answers := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, "pacs.002.001.10")
	if len(answers) != 1 {
		t.Fatalf("the payee's bank answered with %d status files, want 1", len(answers))
	}
	report := answers[0].Document.(*iso20022.Pacs002).FIToFIPmtStsRpt
	if got := report.OrgnlGrpInfAndSts.GrpSts; got != iso20022.GroupStatusPartiallyAccepted {
		t.Errorf("a file with one acceptance and one refusal is %q, want PART", got)
	}

	_, reports := payment.ReadStatus(answers[0].Document.(*iso20022.Pacs002))
	if len(reports) != 2 {
		t.Fatalf("the answer carries %d reports, want one per transaction", len(reports))
	}
	byID := map[string]payment.TransactionStatusReport{}
	for _, r := range reports {
		byID[r.TxID] = r
	}
	if got := byID[string(good.ID)].Status; got != iso20022.TransactionStatusAccepted {
		t.Errorf("the payable transaction was answered %v, want ACCP", got)
	}
	if got := byID[string(bad.ID)].Code; got != iso20022.StatusReasonIncorrectAccountNumber {
		t.Errorf("the unknown payee was answered %q, want AC01", got)
	}

	// And each payment ended where its own answer sent it, which is what a
	// per-transaction outcome is for: a file is not accepted or rejected, its
	// transactions are.
	if got := h.payment(t, good.ID); got.Status != payment.Accepted {
		t.Errorf("the payable transfer is %v, want Accepted", got.Status)
	}
	if got := h.payment(t, bad.ID); got.Status != payment.Rejected {
		t.Errorf("the unpayable transfer is %v, want Rejected", got.Status)
	}
	// The rejected payer got their money back and the accepted one did not: one
	// amount is still in the clearing suspense, waiting for the cut-off.
	if bal := h.suspense(t, h.debtorPID); bal != harnessAmount {
		t.Errorf("the payer's clearing suspense = %d, want %d — one of the two was reversed", bal, harnessAmount)
	}
}

// A bulk collection is the pull's half of the same rule, and it is a separate
// test because the direction changes which bank posts.
//
// Two collections in one file, one payable and one not, and the refusal is the
// PAYER's bank's own: it is the only institution that can see the account being
// collected from. So a mixed pacs.003 proves something a mixed pacs.008 cannot —
// that the bank answering per transaction is also POSTING per transaction.
func TestABulkCollectionIsAnsweredPerTransaction(t *testing.T) {
	h := newHarness(t)

	payable := h.submitDirectDebit(t)
	// A second collection for more than the payer has left. The first takes
	// harnessAmount out of an account holding harnessFunding, so a collection for
	// the whole of the funding overdraws it once the first has posted.
	over := h.directDebitRequest(t)
	over.Amount = harnessFunding
	over.Description = "subscription 8"
	unpayable := h.submit(t, over)

	h.cutOff(t, h.creditorBIC)
	files := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, "pacs.003.001.08")
	if len(files) != 1 {
		t.Fatalf("two collections became %d files, want 1", len(files))
	}
	h.work(t)

	if got := h.payment(t, payable.ID); got.Status != payment.Accepted {
		t.Errorf("the payable collection is %v, want Accepted", got.Status)
	}
	got := h.payment(t, unpayable.ID)
	if got.Status != payment.Rejected {
		t.Fatalf("the collection that overdraws its payer is %v, want Rejected", got.Status)
	}
	if got.RejectCode != iso20022.StatusReasonInsufficientFunds {
		t.Errorf("reject code = %q, want AM04 — the payer's own bank is the only one that can say it", got.RejectCode)
	}
	// The payer was debited once and only once. A file answered as a whole would
	// have posted both legs or neither.
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding-harnessAmount {
		t.Errorf("the payer's balance = %d, want %d", bal, harnessFunding-harnessAmount)
	}
}

// A batch of many settles as a batch of many. The counts above say the files are
// right; this says the money is.
func TestAFileOfManyReachesFinality(t *testing.T) {
	h := newHarness(t)

	var ids []payment.PaymentID
	for _, ref := range []string{"invoice 1", "invoice 2", "invoice 3"} {
		ids = append(ids, h.submit(t, h.smallTransfer(t, ref)).ID)
	}
	h.day(t)

	for _, id := range ids {
		if got := h.payment(t, id); got.Status != payment.Settled {
			t.Errorf("%s is %v after a business day, want Settled", id, got.Status)
		}
	}
	// Three amounts moved from one bank to the other, netted into one position
	// and settled once. The payee holds all three and neither bank's clearing
	// suspense is holding anything.
	if bal := h.balance(t, h.creditorPID, h.creditorAcct.ID); bal != 3*(harnessAmount/2) {
		t.Errorf("the payee's balance = %d, want %d", bal, 3*(harnessAmount/2))
	}
	for _, pid := range []payment.ParticipantID{h.debtorPID, h.creditorPID} {
		if bal := h.suspense(t, pid); bal != 0 {
			t.Errorf("%s's clearing suspense = %d after settlement, want 0", pid, bal)
		}
	}
}
