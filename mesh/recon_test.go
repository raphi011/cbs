package mesh

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
//
// payment/recon is an instrument, and an instrument that has never been shown a
// break is an instrument nobody has any reason to believe. Every test in this
// file therefore takes a network that reconciles, puts ONE thing wrong in it
// behind every institution's back, and checks that the harness says so — and
// says which books, because a break that named no institution would send its
// reader to five databases.
//
// This is the mesh's package because this is where a real network can be built
// and driven to finality. The harness itself names no fixture and could be run
// over any deployment; seed/seed_test.go runs it over the widest one.
//
// # Why the damage is done through the store and not through the domain
//
// Because none of these states is reachable through the domain, which is the
// whole reason the harness exists. A bank cannot post into another bank's
// ledger, a clearing house cannot write a settlement, and nothing in this system
// can make two institutions' books disagree on purpose. What CAN produce every
// one of these states is a message that was lost, a handler that failed halfway,
// or a defect in an act one institution performs alone — and what those have in
// common is that they leave rows, not conversations. So the fixtures write rows.

// reconciled is a two-bank network that has carried one credit transfer to
// finality and drained: both banks booked their halves of the cut-off, both hold
// a Settled copy, and every suspense is back to zero. It is the state each test
// below damages exactly once.
func reconciled(t *testing.T) *meshHarness {
	t.Helper()
	h := newMeshHarness(t)
	h.settledPayment(t)
	h.drain(t)
	return h
}

// TestASettledNetworkReconciles is the control, and without it every other test
// in this file proves nothing: a harness that reported a break on a healthy
// network would "catch" all five damaged ones too.
//
// It asserts the absence of unreconciled positions as well as of breaks, which
// is a claim about the FLOW rather than about the harness. Everything this
// fixture started is finished — the payment is Settled at both banks, both
// booked the reserve mirror from the camt.053 they were sent — so there is
// nothing left for a clearing suspense to be holding. A position appearing here
// means a message in the settlement conversation stopped being answered.
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
//
// A return is the same shape as a cut-off, one payment wide: the agent reverses
// the reserves in its own book and is final there, states both members'
// accounts in a camt.053 apiece, and each bank books its own mirror leg
// afterwards. Everything the harness checks therefore has a second form on this
// path, and a flow that reconciles after a cut-off can still leave a bank short
// after a return.
func TestAReturnedNetworkReconciles(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)
	h.returnPayment(t, p.ID, iso20022.ReturnReasonDuplication, "sent twice")
	h.drain(t)

	report := recon.Check(t, h.nets)

	for _, u := range report.Unreconciled {
		t.Errorf("%s (%s) holds %d in clearing suspense after a completed return: %d in flight, unbooked %v",
			u.Bank, u.Asset, u.Suspense, len(u.InFlight), u.Unbooked)
	}
}

// TestATwoAssetNetworkReconciles is the control again with two of everything.
//
// Every account this harness reads exists once per asset the bank operates in,
// and the whole reason for that is that a euro reserve says nothing about a
// dollar one and the two must never be added up. A harness that summed across
// assets would pass every test above and report a healthy network in which one
// currency was short exactly as much as the other was over, so the claim needs a
// fixture in which the two figures differ — which is what this one is: both
// banks clear in both, and only the euro side carries a payment.
func TestATwoAssetNetworkReconciles(t *testing.T) {
	h := newMeshHarnessWithTwoAssets(t)
	h.settledPayment(t)
	h.drain(t)

	recon.Check(t, h.nets)
}

// TestABookTransferReconcilesAndMovesNothingBetweenInstitutions is the control
// for the one act that stays inside a member bank.
//
// It is worth its own control because a book transfer is exactly what the
// refused on-us payment produced three wrong answers doing: a cycle that settled
// nothing, a reserve mirror moved by an amount the central bank never posted,
// and a bank on both sides of one return. Two of those three are findings this
// harness makes, so a transfer that reached the clearing route would show up
// here — and the check is that it does not.
//
// The three claims are separate. The books agree (recon.Check), the settlement
// agent posted nothing (its transaction count), and the payer's bank's claim on
// it is where it was (the reserve balance). A transfer that quietly netted
// against reserves would pass the first two.
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
	h.drain(t)

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
//
// The damage is the shape a defect in PostSettlementAdviceTx would leave — a
// bank's own reserve moved by something the central bank never posted. It is
// done as a posting rather than a row write because a reserve balance is derived
// from entries and there is no column to corrupt.
func TestTheHarnessCatchesAReserveMirrorThatDiverged(t *testing.T) {
	h := reconciled(t)
	accts := h.accounts(t, h.debtorBIC, "EUR")

	// Debit Reserve, Credit Unclaimed Balances: this bank's claim on the central
	// bank rises by a thousand it was never credited. The contra is Unclaimed
	// rather than vault cash because vault cash is an Asset the ledger will not
	// let go negative, and this fixture's payer lodged all of its.
	//
	// Unclaimed pools holders, so the leg names one. It names an account that
	// does not exist, which the ledger neither checks nor could: the break this
	// fixture builds is about the reserve, and a subsidiary that resolves to
	// nothing is as postable as one that resolves.
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
//
// The payer's own bank sees a balance and cannot tell that nothing is coming;
// the clearing house holds no ledger; the central bank holds no suspense. It
// takes all three books at once, which is the definition of this package.
func TestTheHarnessCatchesASuspenseNothingWillSettle(t *testing.T) {
	h := reconciled(t)
	accts := h.accounts(t, h.debtorBIC, "EUR")

	// Credit Suspense, Debit Returns Receivable: money in transit that no payment
	// is behind. Returns Receivable is the contra because it is an Asset rising,
	// which the ledger's sufficiency guard has nothing to say about, and because
	// nothing else in this harness reads it.
	h.postBehindTheBanksBack(t, h.debtorBIC, "in transit to nowhere",
		ledger.Entry{AccountID: accts.ReturnsReceivable, Amount: 1000, Direction: ledger.Debit},
		ledger.Entry{AccountID: accts.Suspense, Amount: 1000, Direction: ledger.Credit})

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, member(h.debtorBIC, "EUR"), "nothing in this system will settle it")
}

// TestTheHarnessCatchesACutOffOnlyTheClearingHouseThinksSettled holds the two
// rows one cut-off leaves in two databases against each other.
//
// It is the check that has no join behind it. A cycle carries no settlement id —
// the agent allocates that in its own unit of work and no message carries it
// back — so "was this cut-off discharged" is a question the clearing house
// cannot ask of anybody, and it believes what the pacs.002 told it. A cycle
// marked Settled with no settlement against it is what a lost or forged answer
// leaves, and the money it claims to have moved has not moved.
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
// payment.BankStatus asks for by name.
//
// That doc records why there is no Applied state between Founded and Member:
// nothing would read it, and what "the request is out" is worth knowing for is a
// STUCK admission — which needs a reconciliation walking both ends rather than a
// column on one of them. One admission writes three rows in three databases and
// each institution can see exactly one of them, so a bank routed to that no
// scheme ever admitted is invisible to all three.
func TestTheHarnessCatchesAnAdmissionThatHalfHappened(t *testing.T) {
	h := reconciled(t)

	h.putRosterEntry(t, payment.RosterEntry{
		BIC:    "GHOSTDEFFXXX",
		Assets: []ledger.AssetCode{"EUR"},
	})

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, "the clearing house", "this deployment has no bank at")
}

// TestTheHarnessCatchesTwoBanksDisagreeingAboutOnePayment is the check that a
// payment being three rows does not license the three from saying different
// things.
//
// The three copies legitimately differ — the clearing house's has no legs, a
// bank's has no cycle, and their statuses run ahead of each other — and this is
// the line under that: the amount and the scheme are what was instructed, and a
// bank whose copy says a different figure is a bank that will settle the wrong
// number and reconcile against its counterparty for ever.
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
//
// The bank code inside an IBAN IDENTIFIES THE BANK HOLDING THE ACCOUNT. Break
// that and a payer's bank derives the wrong agent and sends there — a payment
// routable to an institution that does not hold the payee, refused with AC01 for
// an address that exists somewhere. Nothing in the system can see it: the
// register that issued the code is the settlement agent's, the account is a
// member's, and neither may read the other.
//
// The damage is a plausible one rather than a nonsense value. The address is
// well-formed, its check digits verify, and it carries the OTHER member's
// allocation — which is exactly what a virtual IBAN is, and is why the design
// names them as out of scope. A fixture using a malformed value would be caught
// by iban.Parse and would prove nothing about the invariant.
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

// TestTheHarnessCatchesARosterPublishingAnAllocationTheRegistryDidNotMake is the
// second half of the same invariant, one register along.
//
// The roster is what every member COPIES, so a roster that disagrees with the
// registry puts the wrong pairing into every subscriber's directory at once. The
// clearing house learns the allocation from the acknowledgement that writes its
// row and cannot see the register it came from; the settlement agent has never heard of
// the roster. So the two parting company is a state neither can notice, which is
// what makes it this harness's.
func TestTheHarnessCatchesARosterPublishingAnAllocationTheRegistryDidNotMake(t *testing.T) {
	h := reconciled(t)

	entry := h.rosterEntry(t, h.creditorBIC)
	entry.Issuer.BankCode = h.debtor.Issuer.BankCode
	h.putRosterEntry(t, entry)

	report := reconcile(t, h.nets)
	assertBreakAbout(t, report, "the clearing house and the bank-code registry", "the registry allocated to "+string(h.debtorBIC))
}

// TestAStaleDirectoryIsReportedAndIsNotABreak is the case that must PASS, and it
// is the one this file exists to hold the line on.
//
// A member's routing directory is a copy of the roster, pulled by that member.
// Between two pulls it is behind whatever has been published since — which is the
// behaviour the subscription model was chosen for, and is why a bank admitted
// this morning cannot be paid by a bank that pulled yesterday. So the harness
// REPORTS the difference and passes.
//
// The opposite is an easy thing to write by accident: the roster and the copy are
// two tables holding one pairing, which is the shape of every break above. What
// separates them is that this pair has a path back to agreement and the path is a
// request somebody makes. A harness that failed here would assert the opposite of
// the design, and every fixture in the repository would then have to refresh
// every member after every admission to stay green — which is how the local-copy
// model would quietly become a live lookup.
func TestAStaleDirectoryIsReportedAndIsNotABreak(t *testing.T) {
	h := reconciled(t)

	// A third member, admitted and published, and nobody has pulled since.
	joiner := h.provision(t, "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	h.drain(t)

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
//
// The two books are ALLOWED to disagree, by exactly the movements the settlement
// agent has made and a member has not yet booked and by nothing else. That
// interval is the unreconciled position: settlement is final at the central bank
// the moment it commits, and what each member does afterwards is that member's
// own act in its own unit of work, which can fail on its own. A harness that
// called it a break would be one nobody could run against a real network, and a
// harness that ignored it would miss the movement that never gets booked at all.
//
// So this test does the same damage twice. First it moves reserves at the agent
// and records the cut-off at both institutions, which is the state a lost
// camt.053 leaves: neither bank booked, both mirrors diverge, and the harness
// stays silent because the settlement register accounts for every penny of it.
// Then it writes ONE bank's advice row without the mirror leg that must commit
// with it — a bank claiming to have booked what it has not — and the same
// divergence becomes a break, naming that bank and not the other.
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

	// One bank now says it booked. Its mirror leg is absent — the row and the
	// posting commit together or neither does (payment.PostSettlementAdviceTx),
	// so a row standing alone is precisely the lie that doc exists to prevent —
	// and the divergence it was excusing is left with nothing behind it.
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

// TestAnUnbookedReturnExplainsBothBanksReserves is the return path's half of the
// test above, and it is what holds the harness's one piece of domain knowledge
// to being right.
//
// A cut-off leaves the settlement agent a settlements row with a signed position
// per member, so what each bank still has to book is READ. A return leaves the
// agent nothing at all — see settlements in
// store/sqlite/schema/centralbank/0001_init.sql, which says so in as many words:
// the only durable trace a return needs is the idempotency key on the reversal.
// So the movement is DERIVED, from the clearing house's copy of the returned
// payment, by the rule that a return sends the money back to the payer: the
// debtor's bank gains and the creditor's bank loses, on a pull exactly as on a
// push, because the debtor is the payer under both schemes.
//
// This is the fixture in which that derivation is load-bearing. Both banks'
// mirrors diverge from the agent's and neither has booked, so the harness has to
// account for both from the payment alone — and it has to get the SIGNS right in
// opposite directions at once. Swap them and each bank breaks by twice the
// amount, which is what the control above cannot show: there both banks book,
// the divergence is zero, and a sign nobody uses is a sign nobody has checked.
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
	// derive from. Neither bank is told: no camt.053 arrives, so no advice row is
	// written and no mirror leg is posted.
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
//
// Both halves, because either alone passes on the wrong evidence: a match on the
// wording alone would accept a break naming the wrong institution, and a match
// on the institution alone would accept any of the five checks firing for any
// reason. It prints every break it did find, because "the harness said nothing"
// and "the harness said something else" are different failures and a bare count
// tells them apart too late.
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
func (h *meshHarness) accounts(t *testing.T, bic iso20022.BIC, asset ledger.AssetCode) payment.BankAccounts {
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
func (h *meshHarness) postBehindTheBanksBack(t *testing.T, bic iso20022.BIC, desc string, entries ...ledger.Entry) {
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
func (h *meshHarness) postInTheCentralBanksBook(t *testing.T, desc string, entries ...ledger.Entry) {
	t.Helper()
	if _, err := h.cbBook(t).PostTransaction(context.Background(), ledger.PostTransactionRequest{
		Entries: entries, Description: desc,
	}); err != nil {
		t.Fatalf("posting %q in the central bank's book: %v", desc, err)
	}
}

// onlySettledPayment is the one payment this fixture carried to finality, read
// off the clearing house's copy.
func (h *meshHarness) onlySettledPayment(t *testing.T) payment.Payment {
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

// The four row writes. Each opens the institution's OWN store and writes a row
// no act of that institution would have written, which is the only way to reach
// the states above; see the note at the top of this file.

func (h *meshHarness) putCycle(t *testing.T, c payment.ClearingCycle) {
	t.Helper()
	h.write(t, h.rec.ClearingHouse(), "the clearing house", func(ctx context.Context, tx payment.Tx) error {
		return tx.PutCycle(ctx, c)
	})
}

func (h *meshHarness) putRosterEntry(t *testing.T, e payment.RosterEntry) {
	t.Helper()
	h.write(t, h.rec.ClearingHouse(), "the clearing house", func(ctx context.Context, tx payment.Tx) error {
		return tx.PutRosterEntry(ctx, e)
	})
}

func (h *meshHarness) putSettlement(t *testing.T, s payment.Settlement) {
	t.Helper()
	h.write(t, h.rec.CentralBank(), "the central bank", func(ctx context.Context, tx payment.Tx) error {
		return tx.PutSettlement(ctx, s)
	})
}

func (h *meshHarness) putClearingHousePayment(t *testing.T, p payment.Payment) {
	t.Helper()
	h.write(t, h.rec.ClearingHouse(), "the clearing house", func(ctx context.Context, tx payment.Tx) error {
		return tx.PutPayment(ctx, p)
	})
}

func (h *meshHarness) putPayment(t *testing.T, bic iso20022.BIC, p payment.Payment) {
	t.Helper()
	h.write(t, h.store(t, bic), string(bic), func(ctx context.Context, tx payment.Tx) error {
		return tx.PutPayment(ctx, p)
	})
}

func (h *meshHarness) putDepositAccount(t *testing.T, bic iso20022.BIC, book ledger.BookID, a deposit.Account) {
	t.Helper()
	h.write(t, h.store(t, bic), string(bic), func(ctx context.Context, tx payment.Tx) error {
		return tx.PutDepositAccount(ctx, book, a)
	})
}

// rosterEntry is what the clearing house publishes about one member, read out of
// the clearing house's own store rather than through an actor.
func (h *meshHarness) rosterEntry(t *testing.T, bic iso20022.BIC) payment.RosterEntry {
	t.Helper()
	e, err := h.net.GetRosterEntryByBIC(context.Background(), bic)
	if err != nil {
		t.Fatalf("reading %s's roster entry: %v", bic, err)
	}
	return e
}

func (h *meshHarness) putSettlementAdvice(t *testing.T, bic iso20022.BIC, a payment.SettlementAdvice) {
	t.Helper()
	h.write(t, h.store(t, bic), string(bic), func(ctx context.Context, tx payment.Tx) error {
		return tx.PutSettlementAdvice(ctx, a.Book, a)
	})
}

func (h *meshHarness) store(t *testing.T, bic iso20022.BIC) payment.Store {
	t.Helper()
	store, err := h.rec.Bank(context.Background(), bic)
	if err != nil {
		t.Fatalf("opening %s's store: %v", bic, err)
	}
	return store
}

func (h *meshHarness) write(t *testing.T, store payment.Store, who string, fn func(context.Context, payment.Tx) error) {
	t.Helper()
	if err := store.Update(context.Background(), fn); err != nil {
		t.Fatalf("writing into %s's database: %v", who, err)
	}
}
