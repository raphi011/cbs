package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
)

// ---------------------------------------------------------------------------
// Calibrating the reconciliation harness
// ---------------------------------------------------------------------------

// reconciled is a two-bank network that has carried one credit transfer to
// finality and drained: both banks booked their halves of the cut-off, both
// hold a Settled copy, and every suspense is back to zero.
func reconciled(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.settledPayment(t)
	h.work(t)
	return h
}

// TestASettledNetworkReconciles is the control, and without it every other test
// in this file proves nothing: a harness that reported a break on a healthy
// network would "catch" all five damaged ones too.
func TestASettledNetworkReconciles(t *testing.T) {
	h := reconciled(t)

	report := recon.Check(t, h.nets)

	for _, u := range report.Unreconciled {
		t.Errorf("%s (%s) holds %d in clearing suspense after a settled payment: %d in flight, unbooked %v",
			u.Bank, u.Asset, u.Suspense, len(u.InFlight), u.Unbooked)
	}
}

// TestAReturnedNetworkReconciles carries the control through the one flow that
// moves reserves BACKWARDS.
func TestAReturnedNetworkReconciles(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)
	h.returnPayment(t, p.ID, iso20022.ReturnReasonDuplication, "sent twice")
	h.work(t)

	report := recon.Check(t, h.nets)

	for _, u := range report.Unreconciled {
		t.Errorf("%s (%s) holds %d in clearing suspense after a completed return: %d in flight, unbooked %v",
			u.Bank, u.Asset, u.Suspense, len(u.InFlight), u.Unbooked)
	}
}

// TestATwoAssetNetworkReconciles is the control again with two of everything.
func TestATwoAssetNetworkReconciles(t *testing.T) {
	h := newHarnessWithTwoAssets(t)
	h.settledPayment(t)
	h.work(t)

	recon.Check(t, h.nets)
}

// TestABookTransferReconcilesAndMovesNothingBetweenInstitutions is the control
// for the one act that stays inside a member bank.
func TestABookTransferReconcilesAndMovesNothingBetweenInstitutions(t *testing.T) {
	ctx := context.Background()
	h := reconciled(t)
	payee := h.openCustomer(t, h.debtor, "Aaron", "EUR", 0)
	reserve := h.accounts(t, h.debtorBIC, "EUR").Reserve

	before, err := h.debtor.Ledger.BookBalance(ctx, reserve.Total())
	if err != nil {
		t.Fatalf("reading %s's reserve: %v", h.debtorBIC, err)
	}
	posted := h.centralBankTransactionCount(t)

	if _, err := h.debtor.Deposit.Transfer(ctx, h.debtorAcct.ID, payee.ID, 1000, "rent"); err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	// Drained, because the claim is about messages that do not exist. Reading
	// the books while a pacs.008 was still in flight would pass either way.
	h.work(t)

	recon.Check(t, h.nets)

	if got := h.centralBankTransactionCount(t); got != posted {
		t.Errorf("the settlement agent posted %d transactions over a book transfer, want %d", got-posted, 0)
	}
	after, err := h.debtor.Ledger.BookBalance(ctx, reserve.Total())
	if err != nil {
		t.Fatalf("reading %s's reserve: %v", h.debtorBIC, err)
	}
	if after != before {
		t.Errorf("the payer's bank's reserve moved by %d over a book transfer, want 0", after-before)
	}
}

// TestTheHarnessCatchesAReserveMirrorThatDiverged is the nostro/vostro check:
// one account, two books, two institutions, and no act in this system able to
// compare them.
func TestTheHarnessCatchesAReserveMirrorThatDiverged(t *testing.T) {
	h := reconciled(t)
	accts := h.accounts(t, h.debtorBIC, "EUR")

	// Debit Reserve, Credit Unclaimed Balances: this bank's claim on the central
	// bank rises by a thousand it was never credited.
	h.postBehindTheBanksBack(t, h.debtorBIC, "a reserve nobody credited",
		ledger.Entry{AccountID: accts.Reserve, Amount: 1000, Direction: ledger.Debit},
		ledger.Entry{AccountID: accts.Unclaimed, Subsidiary: "dep_nobody", Amount: 1000, Direction: ledger.Credit})

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, member(h.debtorBIC, "EUR"), "the bank's own reserve says")
}

// TestTheHarnessCatchesASuspenseNothingWillSettle is the most serious finding
// this harness can make and the one no institution is placed to make at all: a
// clearing suspense holding money that has left a customer, with no payment in
// flight to deliver it and no reserve movement outstanding to discharge it.
func TestTheHarnessCatchesASuspenseNothingWillSettle(t *testing.T) {
	h := reconciled(t)
	accts := h.accounts(t, h.debtorBIC, "EUR")

	// Credit Suspense, Debit Returns Receivable: money in transit that no payment
	// is behind.
	h.postBehindTheBanksBack(t, h.debtorBIC, "in transit to nowhere",
		ledger.Entry{AccountID: accts.ReturnsReceivable, Amount: 1000, Direction: ledger.Debit},
		ledger.Entry{AccountID: accts.Suspense, Amount: 1000, Direction: ledger.Credit})

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, member(h.debtorBIC, "EUR"), "nothing in this system will settle it")
}

// TestTheHarnessCatchesACutOffOnlyTheClearingHouseThinksSettled holds the two
// rows one cut-off leaves in two databases against each other.
func TestTheHarnessCatchesACutOffOnlyTheClearingHouseThinksSettled(t *testing.T) {
	h := reconciled(t)

	h.putCycle(t, payment.ClearingCycle{
		ID:     "cyc_never_settled",
		Scheme: payment.SchemeSEPACT,
		Status: payment.CycleSettled,
		NetPositions: map[iso20022.BIC]ledger.Amount{
			h.debtorBIC: -1000, h.creditorBIC: 1000,
		},
	})

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, "the clearing house and the settlement agent",
		"holds no settlement against it")
}

// TestTheHarnessCatchesAnAdmissionThatHalfHappened is the walk
// payment.BankAccounts.Settlement asks for by name.
func TestTheHarnessCatchesAnAdmissionThatHalfHappened(t *testing.T) {
	h := reconciled(t)

	h.putRosterEntry(t, payment.RosterEntry{
		BIC:    "GHOSTDEFFXXX",
		Assets: []ledger.AssetCode{"EUR"},
	})

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, "the clearing house", "this deployment has no bank at")
}

// TestTheHarnessCatchesABankProvisionedInEveryBookButItsOwn is the gap
// provisioning cannot close, measured.
func TestTheHarnessCatchesABankProvisionedInEveryBookButItsOwn(t *testing.T) {
	h := reconciled(t)

	row := *h.getBank(t, payment.ParticipantID(h.debtorBIC))
	for asset, accts := range row.Assets {
		accts.Settlement = ""
		row.Assets[asset] = accts
	}
	row.AdmissionRef = ""
	h.putBank(t, row)

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, string(h.debtorBIC),
		"records no settlement account and the settlement agent holds accounts for it")
	assertBreakAbout(t, report, string(h.debtorBIC),
		"records no settlement account and the clearing house routes payments to it")
}

// TestTheHarnessCatchesABankQuotingAnAccountTheAgentDidNotOpen is the third row
// read rather than counted.
func TestTheHarnessCatchesABankQuotingAnAccountTheAgentDidNotOpen(t *testing.T) {
	h := reconciled(t)

	row := *h.getBank(t, payment.ParticipantID(h.debtorBIC))
	accts := row.Assets["EUR"]
	accts.Settlement = h.getSettlementMember(t, h.creditorBIC).Accounts["EUR"]
	row.Assets["EUR"] = accts
	h.putBank(t, row)

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, string(h.debtorBIC), "and the settlement agent holds")
}

// TestTheHarnessCatchesAPaymentTakenForABankTheRosterDoesNotCarry is
// payment.ErrBankNotAdmitted's counterpart, and it exists because that refusal
// is now unreachable by construction.
func TestTheHarnessCatchesAPaymentTakenForABankTheRosterDoesNotCarry(t *testing.T) {
	h := reconciled(t)
	outsider := h.admitWithoutTheRoster(t, "Nordhaven Bank", "NORDSESSXXX")

	taken := h.onlySettledPayment(t)
	taken.ID = "pay_for_a_non_member"
	taken.Status = payment.Accepted
	taken.CreditorDetails.Agent = outsider.BIC
	h.putClearingHousePayment(t, taken)

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, "the clearing house",
		"this scheme's roster does not carry")
	// And the bank itself, which is the other half of the same state: the agent
	// holds an account for an address the clearing house routes nothing to.
	assertBreakAbout(t, report, string(outsider.BIC),
		"the clearing house does not route to it")
}

// TestTheHarnessCatchesTwoBanksDisagreeingAboutOnePayment is the check that a
// payment being three rows does not license the three from saying different
// things.
func TestTheHarnessCatchesTwoBanksDisagreeingAboutOnePayment(t *testing.T) {
	h := reconciled(t)
	p := h.onlySettledPayment(t)

	own := h.bankPayment(t, h.debtorBIC, p.ID)
	own.Amount += 1
	h.putPayment(t, h.debtorBIC, own)

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, string(h.debtorBIC), "on the clearing house's")
}

// TestTheHarnessCatchesAnAccountAddressedUnderAnotherBanksCode is the routing
// invariant the whole IBAN-only design rests on, measured from outside every
// institution.
func TestTheHarnessCatchesAnAccountAddressedUnderAnotherBanksCode(t *testing.T) {
	h := reconciled(t)

	// The payee's OWN account, re-addressed under the payer's bank's allocation.
	acct := h.creditorAcct
	acct.Identifiers = []deposit.Identifier{{
		Scheme: deposit.IdentifierIBAN,
		Value:  mustMint(h.debtor.Issuer.Country, h.debtor.Issuer.BankCode, 424242),
	}}
	h.putDepositAccount(t, h.creditorBIC, h.creditor.BookID, acct)

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, string(h.creditorBIC), "which is allocated to "+string(h.debtorBIC))
}

// TestTheHarnessCatchesARosterPublishingAnAllocationTheRegistryDidNotMake is
// the second half of the same invariant, one register along.
func TestTheHarnessCatchesARosterPublishingAnAllocationTheRegistryDidNotMake(t *testing.T) {
	h := reconciled(t)

	entry := h.rosterEntry(t, h.creditorBIC)
	entry.Issuer.BankCode = h.debtor.Issuer.BankCode
	h.putRosterEntry(t, entry)

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, "the clearing house and the bank-code registry", "the registry allocated to "+string(h.debtorBIC))
}

// TestAStaleDirectoryIsReportedAndIsNotABreak is the case that must PASS, and
// it is the one this file exists to hold the line on.
func TestAStaleDirectoryIsReportedAndIsNotABreak(t *testing.T) {
	h := reconciled(t)

	// A third member, admitted and published, and nobody has pulled since.
	joiner := h.provision(t, "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	h.work(t)

	// It PASSES. recon.Check fails the test on every break, so calling it here is
	// the assertion: a network in which two members are two entries behind and a
	// third has never pulled at all is a network whose books agree.
	report := recon.Check(t, h.nets)

	// And it is reported, per member, with what each is missing.
	behind := map[iso20022.BIC]recon.StaleDirectory{}
	for _, d := range report.Stale {
		behind[d.Bank] = d
	}
	for _, bic := range []iso20022.BIC{h.debtorBIC, h.creditorBIC} {
		d, ok := behind[bic]
		if !ok {
			t.Fatalf("%s pulled before %s was admitted and is not reported as behind", bic, joiner.BIC)
		}
		if !slices.Contains(d.Missing, joiner.BIC) {
			t.Errorf("%s is reported behind by %v, want the newly admitted %s", bic, d.Missing, joiner.BIC)
		}
		if d.RefreshedAt.IsZero() {
			t.Errorf("%s has pulled a directory and is reported as never having done so", bic)
		}
	}
	// The joiner itself has never pulled: it is in the roster and holds no copy,
	// so it can be paid and cannot pay. A member is not given a directory by being
	// admitted.
	d, ok := behind[joiner.BIC]
	if !ok {
		t.Fatalf("%s has never pulled a directory and is not reported", joiner.BIC)
	}
	if !d.RefreshedAt.IsZero() {
		t.Errorf("%s is reported as having pulled at %s; nothing has pulled one for it", joiner.BIC, d.RefreshedAt)
	}

	// One request each, and the report is empty.
	h.subscribeAll(t)
	if got := reconcile(t, h.nets); len(got.Stale) != 0 {
		t.Errorf("after every member pulled, %d directories are still behind: %v", len(got.Stale), got.Stale)
	}
}

// TestAnUnbookedSettlementExplainsAReserveDivergence is what makes the mirror
// check more than a comparison of two numbers, and it is the pair to the test
// above it.
func TestAnUnbookedSettlementExplainsAReserveDivergence(t *testing.T) {
	h := reconciled(t)
	const cycle payment.CycleID = "cyc_camt053_lost"
	const moved ledger.Amount = 1000

	// The agent's own act: reserves move from the payer's bank to the payee's,
	// in the agent's book, and the agent records what it discharged.
	debtorReserve := h.getSettlementMember(t, h.debtorBIC).Accounts["EUR"]
	creditorReserve := h.getSettlementMember(t, h.creditorBIC).Accounts["EUR"]
	h.postInTheCentralBanksBook(t, "a cut-off whose statements were lost",
		ledger.Entry{AccountID: debtorReserve, Amount: moved, Direction: ledger.Debit},
		ledger.Entry{AccountID: creditorReserve, Amount: moved, Direction: ledger.Credit})
	h.putSettlement(t, payment.Settlement{
		ID:      "set_camt053_lost",
		CycleID: cycle,
		Asset:   "EUR",
		NetPositions: map[iso20022.BIC]ledger.Amount{
			h.debtorBIC: -moved, h.creditorBIC: moved,
		},
	})
	// And the clearing house's row for the same cut-off, because the pacs.002
	// answering the instruction is not the message that goes missing here.
	h.putCycle(t, payment.ClearingCycle{
		ID:     cycle,
		Scheme: payment.SchemeSEPACT,
		Status: payment.CycleSettled,
		NetPositions: map[iso20022.BIC]ledger.Amount{
			h.debtorBIC: -moved, h.creditorBIC: moved,
		},
	})

	report := reconcile(t, h.nets)
	for _, b := range report.Breaks {
		t.Errorf("a cut-off neither member has booked is an unreconciled position, not a break: %s", b)
	}

	// One bank now says it booked.
	h.putSettlementAdvice(t, h.debtorBIC, payment.SettlementAdvice{
		Book:      ledger.BookID(h.debtorBIC),
		Reference: string(cycle),
		Asset:     "EUR",
		Movement:  -moved,
		Status:    payment.AdvicePosted,
	})

	report = reconcile(t, h.nets)
	assertBreakAbout(t, report, member(h.debtorBIC, "EUR"), "the bank's own reserve says")
	for _, b := range report.Breaks {
		if strings.HasPrefix(b.Where, string(h.creditorBIC)) {
			t.Errorf("the other bank booked nothing and is still explained by the settlement register; %s", b)
		}
	}
}

// TestAnUnbookedReturnExplainsBothBanksReserves is the return path's half of
// the test above, and it is what holds the harness's one piece of domain
// knowledge to being right.
func TestAnUnbookedReturnExplainsBothBanksReserves(t *testing.T) {
	h := reconciled(t)
	p := h.onlySettledPayment(t)

	// The settlement agent's own act: the reserves go back. The creditor's bank
	// is the one that was paid at the cut-off, so it is the one that gives it up.
	debtorReserve := h.getSettlementMember(t, h.debtorBIC).Accounts["EUR"]
	creditorReserve := h.getSettlementMember(t, h.creditorBIC).Accounts["EUR"]
	h.postInTheCentralBanksBook(t, "a return whose statements were lost",
		ledger.Entry{AccountID: creditorReserve, Amount: p.Amount, Direction: ledger.Debit},
		ledger.Entry{AccountID: debtorReserve, Amount: p.Amount, Direction: ledger.Credit})

	// And the clearing house's copy, which is the only record of the return that
	// outlives the conversation and therefore the only thing the harness has to
	// derive from.
	returned := p
	returned.Status = payment.Returned
	h.putClearingHousePayment(t, returned)

	report := reconcile(t, h.nets)
	for _, b := range report.Breaks {
		t.Errorf("a return neither member has booked is an unreconciled position, not a break: %s", b)
	}
}

// ---------------------------------------------------------------------------
// The fixtures' hands inside the databases
// ---------------------------------------------------------------------------

// reconcile runs the harness and returns what it found without failing anything,
// which is what a test asserting ON a break needs. recon.Check is the other way
// round and is what every caller outside this file wants.
func reconcile(t *testing.T, nets *payment.Networks) *recon.Report {
	t.Helper()
	report, err := recon.Reconcile(context.Background(), nets)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return report
}

// member is the Where a break about one bank's own books carries: the address
// and the asset, because a bank operating in two of them keeps two of every
// account and a finding that named only the bank would not say which.
func member(bic iso20022.BIC, asset ledger.AssetCode) string {
	return string(bic) + " (" + string(asset) + ")"
}

// assertBreakAbout checks that the harness reported a break about the right
// books, saying the right thing.
func assertBreakAbout(t *testing.T, report *recon.Report, where, contains string) {
	t.Helper()
	for _, b := range report.Breaks {
		if b.Where == where && strings.Contains(b.What, contains) {
			return
		}
	}
	t.Errorf("no break about %q containing %q; the harness reported %d break(s):", where, contains, len(report.Breaks))
	for _, b := range report.Breaks {
		t.Errorf("  %s", b)
	}
}

// accounts is one bank's internal accounts for an asset, read out of that bank's
// own row.
func (h *harness) accounts(t *testing.T, bic iso20022.BIC, asset ledger.AssetCode) payment.BankAccounts {
	t.Helper()
	p := h.getBank(t, payment.ParticipantID(bic))
	accts, err := p.AccountsFor(asset)
	if err != nil {
		t.Fatalf("AccountsFor %s at %s: %v", asset, bic, err)
	}
	return accts
}

// postBehindTheBanksBack posts a balanced transaction into one member's own
// ledger that no act of that member's produced.
func (h *harness) postBehindTheBanksBack(t *testing.T, bic iso20022.BIC, desc string, entries ...ledger.Entry) {
	t.Helper()
	p := h.getBank(t, payment.ParticipantID(bic))
	if _, err := p.Ledger.PostTransaction(context.Background(), ledger.PostTransactionRequest{
		Entries: entries, Description: desc,
	}); err != nil {
		t.Fatalf("posting %q in %s's book: %v", desc, bic, err)
	}
}

// postInTheCentralBanksBook is the same for the settlement agent, whose book is
// reached through its own network and no other.
func (h *harness) postInTheCentralBanksBook(t *testing.T, desc string, entries ...ledger.Entry) {
	t.Helper()
	if _, err := h.cbBook(t).PostTransaction(context.Background(), ledger.PostTransactionRequest{
		Entries: entries, Description: desc,
	}); err != nil {
		t.Fatalf("posting %q in the central bank's book: %v", desc, err)
	}
}

// onlySettledPayment is the one payment this fixture carried to finality, read
// off the clearing house's copy.
func (h *harness) onlySettledPayment(t *testing.T) payment.Payment {
	t.Helper()
	payments, err := h.net.ListPayments(context.Background())
	if err != nil {
		t.Fatalf("ListPayments: %v", err)
	}
	var found []payment.Payment
	for _, p := range payments {
		if p.Status == payment.Settled {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		t.Fatalf("this fixture settled %d payments, want exactly 1", len(found))
	}
	return found[0]
}

// The row writes. Each opens the institution's OWN store and writes a row no act
// of that institution would have written, which is the only way to reach the
// states above; see the note at the top of this file.

func (h *harness) putCycle(t *testing.T, c payment.ClearingCycle) {
	t.Helper()
	h.writeCsm(t, func(ctx context.Context, tx payment.CsmTx) error {
		return tx.PutCycle(ctx, c)
	})
}

func (h *harness) putRosterEntry(t *testing.T, e payment.RosterEntry) {
	t.Helper()
	h.writeCsm(t, func(ctx context.Context, tx payment.CsmTx) error {
		return tx.PutRosterEntry(ctx, e)
	})
}

func (h *harness) putSettlement(t *testing.T, s payment.Settlement) {
	t.Helper()
	h.writeCentralBank(t, func(ctx context.Context, tx payment.CentralBankTx) error {
		return tx.PutSettlement(ctx, s)
	})
}

func (h *harness) putClearingHousePayment(t *testing.T, p payment.Payment) {
	t.Helper()
	h.writeCsm(t, func(ctx context.Context, tx payment.CsmTx) error {
		return tx.PutPayment(ctx, p)
	})
}

func (h *harness) putPayment(t *testing.T, bic iso20022.BIC, p payment.Payment) {
	t.Helper()
	h.write(t, h.store(t, bic), string(bic), func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutPayment(ctx, p)
	})
}

// putBank writes a member's own record of itself, into that member's own
// database. It is the only row here whose writer and whose subject are the same
// institution, which is what makes the states it reaches invisible from outside.
func (h *harness) putBank(t *testing.T, b payment.Bank) {
	t.Helper()
	h.write(t, h.store(t, b.BIC), string(b.BIC), func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutBank(ctx, b)
	})
}

func (h *harness) putDepositAccount(t *testing.T, bic iso20022.BIC, book ledger.BookID, a deposit.Account) {
	t.Helper()
	h.write(t, h.store(t, bic), string(bic), func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutDepositAccount(ctx, book, a)
	})
}

// rosterEntry is what the clearing house publishes about one member, read out of
// the clearing house's own store rather than through an actor.
func (h *harness) rosterEntry(t *testing.T, bic iso20022.BIC) payment.RosterEntry {
	t.Helper()
	e, err := h.net.GetRosterEntryByBIC(context.Background(), bic)
	if err != nil {
		t.Fatalf("reading %s's roster entry: %v", bic, err)
	}
	return e
}

func (h *harness) putSettlementAdvice(t *testing.T, bic iso20022.BIC, a payment.SettlementAdvice) {
	t.Helper()
	h.write(t, h.store(t, bic), string(bic), func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutSettlementAdvice(ctx, a.Book, a)
	})
}

func (h *harness) store(t *testing.T, bic iso20022.BIC) payment.BankStore {
	t.Helper()
	store, err := h.rec.Bank(context.Background(), bic)
	if err != nil {
		t.Fatalf("opening %s's store: %v", bic, err)
	}
	return store
}

// write, one per institution, because a row this harness plants goes into one
// database and there are three kinds.
func (h *harness) write(t *testing.T, store payment.BankStore, who string, fn func(context.Context, payment.BankTx) error) {
	t.Helper()
	if err := store.Update(context.Background(), fn); err != nil {
		t.Fatalf("writing into %s's database: %v", who, err)
	}
}

func (h *harness) writeCsm(t *testing.T, fn func(context.Context, payment.CsmTx) error) {
	t.Helper()
	if err := h.rec.ClearingHouse().Update(context.Background(), fn); err != nil {
		t.Fatalf("writing into the clearing house's database: %v", err)
	}
}

func (h *harness) writeCentralBank(t *testing.T, fn func(context.Context, payment.CentralBankTx) error) {
	t.Helper()
	if err := h.rec.CentralBank().Update(context.Background(), fn); err != nil {
		t.Fatalf("writing into the central bank's database: %v", err)
	}
}
