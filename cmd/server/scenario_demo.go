package main

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/seed"
)

// The demo network: every account status, five facility states, settled and
// returned payments, and six left in flight. The largest scenario and the least
// specific, which is why it is last in the picker.
//
// Every payment goes through the doors above. One built on payment/flow reads
// as settled on every screen and names no file, which is the first thing a
// reader looks for.

var scenarioDemoNetwork = Scenario{
	ID:   "demo-network",
	Name: "Demo network",
	Description: "Fills all four banks: thirteen customers across every account status, " +
		"authorisation holds, direct-debit mandates, five credit facilities including one in " +
		"arrears, a general-ledger showcase, two settled clearing cycles, a returned direct " +
		"debit, six payments in flight and one refused. Advances the business date by about " +
		"eight months.",
	Run: func(ctx context.Context, d *Deployment) error {
		return run(ctx, d, func(r *runner) { r.demoNetwork() })
	},
}

func (r *runner) demoNetwork() {
	aurora, verde := r.bank(auroraBIC), r.bank(verdeBIC)
	nord, soleil := r.bank(nordBIC), r.bank(soleilBIC)

	// --- Customer accounts (each bank mints its own addresses) -------------
	alice := r.open(aurora, "Alice Andersson")
	aaron := r.open(aurora, "Aaron Apstorp")
	annie := r.open(aurora, "Annie Ahlberg")      // -> Dormant
	merchant := r.open(aurora, "Aurora Merchant") // hold-capture counterparty
	oldAcct := r.open(aurora, "Closed Account")   // -> Closed

	bruno := r.openOverdraft(verde, "Bruno Bianchi", 50_000) // 500.00 overdraft
	bella := r.open(verde, "Bella Bruno")
	bianca := r.open(verde, "Bianca Belli") // -> Frozen

	nora := r.open(nord, "Nora Nilsson")
	niklas := r.open(nord, "Niklas Nyborg")

	chloe := r.open(soleil, "Chloé Caron")
	claude := r.open(soleil, "Claude Clément")

	// --- Funding: cash in, which each bank holds as vault cash -------------
	r.fund(aurora, alice, 200_000)
	r.fund(aurora, aaron, 50_000)
	r.fund(aurora, annie, 30_000)
	r.fund(verde, bruno, 150_000)
	r.fund(verde, bella, 80_000)
	r.fund(verde, bianca, 40_000)
	r.fund(nord, nora, 300_000)
	r.fund(nord, niklas, 60_000)
	r.fund(soleil, chloe, 120_000)
	r.fund(soleil, claude, 90_000)

	// --- And each bank places it on reserve, over the wire ------------------
	// A camt.050 apiece. The credit is the settlement agent's to make and the
	// camt.025 that says so arrives when the day below reaches the collection.
	r.lodge(aurora, 280_000)
	r.lodge(verde, 270_000)
	r.lodge(nord, 360_000)
	r.lodge(soleil, 210_000)

	// --- Holds on Alice: active / released / captured ----------------------
	must(aurora.Deposit.CreateHold(r.ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 10_000, Description: "Card pre-authorisation (hotel)",
	}))
	released := must(aurora.Deposit.CreateHold(r.ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 5_000, Description: "Cancelled online order",
	}))
	check(aurora.Deposit.ReleaseHold(r.ctx, released.ID))
	captured := must(aurora.Deposit.CreateHold(r.ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 15_000, Description: "Card payment at Aurora Merchant",
	}))
	must(aurora.Deposit.CaptureHold(r.ctx, captured.ID, r.position(aurora, merchant), 0, "Captured: card payment"))

	// --- End-of-day snapshots for Alice across two business days -----------
	must(aurora.Deposit.TakeEndOfDaySnapshot(r.ctx, alice.ID, r.d.clock.Now()))
	r.day()
	must(aurora.Deposit.TakeEndOfDaySnapshot(r.ctx, alice.ID, r.d.clock.Now()))

	// --- Account status lifecycle ------------------------------------------
	check(aurora.Deposit.MarkDormant(r.ctx, annie.ID)) // Active -> Dormant
	check(aurora.Deposit.Close(r.ctx, oldAcct.ID))     // zero balance -> Closed
	check(verde.Deposit.Freeze(r.ctx, bianca.ID))      // Active -> Frozen

	// --- Mandates for SEPA Direct Debit ------------------------------------
	// Each is held at its CREDITOR's bank, which is the bank that collects on it.
	m1 := must(r.net(nord).CreateMandate(r.ctx, soleil.BIC, r.ref(chloe), r.ref(nora), 100_000))
	m2 := must(r.net(aurora).CreateMandate(r.ctx, verde.BIC, r.ref(bruno), r.ref(aaron), 0))
	m3 := must(r.net(soleil).CreateMandate(r.ctx, nord.BIC, r.ref(niklas), r.ref(claude), 25_000))
	check(r.net(soleil).RevokeMandate(r.ctx, m3.ID)) // revoked, for display

	// --- A settled SEPA Credit Transfer cycle ------------------------------
	// One day carries all three the whole way: each bank's cut-off uploads a
	// pacs.008, the clearing house nets what it took in, the settlement agent
	// moves the reserves and the output files are released.
	r.submit(r.sct(aurora, alice, nord, niklas, 25_000, "SCT-001", "Rent to N. Nyborg"))
	r.submit(r.sct(nord, nora, verde, bella, 40_000, "SCT-002", "Invoice 2025-77"))
	r.submit(r.sct(verde, bruno, soleil, chloe, 30_000, "SCT-003", "Consulting fee"))
	r.clearingDay()

	// --- A settled SEPA Direct Debit cycle (one will be returned) ----------
	r.submit(r.sdd(soleil, chloe, nord, nora, 20_000, m1.ID, "SDD-001", "Utility direct debit"))
	returned := r.submit(r.sdd(verde, bruno, aurora, aaron, 12_000, m2.ID, "SDD-002", "Gym membership"))
	r.clearingDay()

	// --- The R-transaction: the settled collection sent back ---------------
	r.returnPayment(returned.ID, "Debtor dispute — unauthorised collection")
	r.clearingDay()

	// --- The credit facilities, and the months they need -------------------
	// AFTER the history above and BEFORE the payments left in flight below: five
	// months of business days would carry every one of those to settlement, and
	// this scenario's whole point is that some of them are still moving.
	r.lendingShowcase(aurora, verde, nord, alice, bruno, bella, niklas)

	// --- The general-ledger primitives, on one bank ------------------------
	r.glShowcase(aurora, aaron)

	// --- The payments this scenario LEAVES in flight -----------------------
	r.submit(r.sct(aurora, aaron, soleil, claude, 8_000, "SCT-010", "Book order"))
	r.submit(r.sct(verde, bella, nord, niklas, 6_000, "SCT-011", "Shared dinner"))
	r.submit(r.sct(aurora, alice, verde, bella, 7_000, "SCT-020", "Birthday gift"))
	rejected := r.submit(r.sct(nord, nora, soleil, claude, 4_000, "SCT-021", "Deposit, flat viewing"))
	r.submit(r.sdd(soleil, chloe, nord, nora, 5_000, m1.ID, "SDD-010", "Monthly subscription"))
	r.submit(r.sdd(verde, bruno, aurora, aaron, 3_000, m2.ID, "SDD-011", "Gym membership"))

	// The morning's files move and nothing settles: each one is in its scheme's
	// open cycle, and the clearing house is holding a share to release when that
	// cycle is eventually cut off.
	r.carry()

	// --- And one of them refused out of the open cycle ---------------------
	r.reject(rejected.ID, "Payee's account details could not be confirmed")
	// The second carry is the pacs.002 reaching the payer's bank, which is the
	// act that gives the money back.
	r.carry()
}

// net is one bank's payment network, which is where a mandate lives: a mandate
// is a payment-layer row and not a deposit-layer one.
func (r *runner) net(p *payment.Bank) *payment.BankNetwork {
	return must(r.d.Bank(r.ctx, p.ID)).Network()
}

// lendingShowcase exercises every state a credit facility can be in: a priced
// overdraft, a term loan part-way through its schedule, a revolving line with a
// billed cycle, a delinquent loan, and an overdraft that is actually accruing
// rather than sitting at a limit that costs nothing.
func (r *runner) lendingShowcase(aurora, verde, nord *payment.Bank, alice, bruno, bella, niklas deposit.Account) {
	now := func() time.Time { return r.d.clock.Now() }

	// --- Bruno's overdraft, priced ------------------------------------------
	// He already has a 500.00 limit; this is what makes drawing on it cost him
	// something rather than nothing: 15% arranged, 35% beyond the limit.
	must(verde.Deposit.SetOverdraftPricingOverlay(r.ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 150_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		now()))
	must(verde.Deposit.SetOverdraftLimit(r.ctx, bruno.ID, 50_000, now()))

	// --- Bella, migrated onto Premium, effective a fortnight in -------------
	must(verde.Deposit.ChangeProduct(r.ctx, bella.ID, r.premiumAt(verde), now().AddDate(0, 0, 14)))

	// --- A term loan part-way through its schedule (Alice, Aurora) ----------
	// EUR 10,000, five years, 6%, annuity.
	t1 := now()
	firstDue := t1.AddDate(0, 1, 0)
	loan := r.openLoan(aurora, alice, "Alice Home Loan", 1_000_000, 60_000, 60, firstDue, "Home loan payout")
	alicePos := r.position(aurora, alice)

	r.days(daysBetween(t1, firstDue))
	sched := must(aurora.Lending.Schedule(r.ctx, loan.ID))
	must(aurora.Lending.Repay(r.ctx, loan.ID, alicePos, sched[0].Total(), now(), "Instalment 1"))

	secondDue := t1.AddDate(0, 2, 0)
	r.days(daysBetween(firstDue, secondDue))
	sched = must(aurora.Lending.Schedule(r.ctx, loan.ID))
	must(aurora.Lending.Repay(r.ctx, loan.ID, alicePos, sched[1].Total(), now(), "Instalment 2"))

	r.days(10) // a fresh accrual builds up; the third instalment is not yet due

	// --- A delinquent loan (Niklas, Nordhaven) ------------------------------
	// A smaller loan, disbursed and then left unpaid past two due dates: one
	// month plus twenty days past the first, comfortably inside the 30-59 bucket
	// however the calendar months involved fall.
	t3 := now()
	r.openLoan(nord, niklas, "Niklas Car Loan", 300_000, 90_000, 24, t3.AddDate(0, 1, 0), "Car loan payout")
	r.days(daysBetween(t3, t3.AddDate(0, 2, 20)))

	// --- Bruno, pushed into overdraft and accruing --------------------------
	// A card settlement pushes him into his priced overdraft, and it is a real
	// payment: submitted, uploaded, cleared and settled like any other.
	brunoBalance := must(verde.Deposit.GetBalance(r.ctx, bruno.ID))
	overdrawBy := ledger.Amount(20_000) // EUR 200 into the EUR 500 arranged limit
	r.submit(r.sct(verde, bruno, aurora, alice, brunoBalance.Book+overdrawBy, "SCT-030", "Card settlement"))

	r.days(45)
	must(verde.Deposit.ChargeOverdraftInterest(r.ctx, bruno.ID, now()))

	// --- Bruno, repriced mid-life -------------------------------------------
	// The arranged rate moves from 15% to 18%, effective TWENTY DAYS AGO — a
	// repricing agreed on one date and entered on another, which is the ordinary
	// case and the one mutable terms could not represent at all.
	must(verde.Deposit.SetOverdraftPricingOverlay(r.ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 180_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		now().AddDate(0, 0, -20)))

	r.days(15)

	// --- A revolving line, partly drawn and billed (Bella, Verde) -----------
	// EUR 2,500 limit, 18%, 2% minimum payment.
	line := r.openLine(verde, bella, "Bella Card Line", 250_000, 180_000, 20_000, 100_000, "Card line draw")
	r.days(30)
	must(verde.Lending.ChargeInterest(r.ctx, line.ID, now()))
}

// premiumAt is the second product the base state priced at every bank, found by
// name because a catalogue is book-scoped and each bank's is its own.
func (r *runner) premiumAt(p *payment.Bank) product.ID {
	for _, prd := range must(p.Catalogue.ListProducts(r.ctx)) {
		if prd.Name == seed.PremiumProduct {
			return prd.ID
		}
	}
	panic(scenarioErr{fmt.Errorf("%s prices no %q; the base state builds one at every bank", p.BIC, seed.PremiumProduct)})
}

// openLoan opens a term loan and disburses it in full into the borrower's own
// account, so the caller is left with a facility that has begun accruing.
func (r *runner) openLoan(p *payment.Bank, borrower deposit.Account, name string, principal ledger.Amount,
	rate interest.Rate, termMonths int, firstDue time.Time, description string) lending.Facility {

	loan := must(p.Lending.OpenTermLoan(r.ctx, name, scenarioAsset, principal, rate, interest.ACT365,
		lending.Annuity, termMonths))
	must(p.Lending.Disburse(r.ctx, loan.ID, r.position(p, borrower), firstDue, description))
	return must(p.Lending.GetFacility(r.ctx, loan.ID))
}

// openLine opens a revolving line and draws it once into the borrower's own
// account, so the caller is left with a facility carrying a balance.
func (r *runner) openLine(p *payment.Bank, borrower deposit.Account, name string, limit ledger.Amount,
	rate interest.Rate, minPayment interest.Fraction, draw ledger.Amount, description string) lending.Facility {

	line := must(p.Lending.OpenRevolvingLine(r.ctx, name, scenarioAsset, limit, rate, interest.ACT365, minPayment))
	must(p.Lending.Draw(r.ctx, line.ID, r.position(p, borrower), draw, description))
	return must(p.Lending.GetFacility(r.ctx, line.ID))
}

// glShowcase exercises the raw general-ledger primitives on one bank, so that
// the two account types founding does not create — Revenue and Expense — appear
// in the data beside a manual transaction and its reversal.
func (r *runner) glShowcase(p *payment.Bank, customer deposit.Account) {
	glID := must(p.Ledger.GetSubledger(r.ctx, p.CustomerSubledger)).LedgerID
	accts := must(p.AccountsFor(scenarioAsset))

	incomeSub := must(p.Ledger.CreateSubledger(r.ctx, glID, "Income"))
	feeIncome := must(p.Ledger.CreateAccount(r.ctx, incomeSub.ID, "Fee Income", ledger.Revenue, scenarioAsset))
	expenseSub := must(p.Ledger.CreateSubledger(r.ctx, glID, "Expenses"))
	opex := must(p.Ledger.CreateAccount(r.ctx, expenseSub.ID, "Operating Expenses", ledger.Expense, scenarioAsset))

	// Operating expense: Operating Expenses (expense) up, Vault Cash (asset)
	// down — the bank's OWN vault, which is where its capital was paid in.
	must(p.Ledger.PostTransaction(r.ctx, ledger.PostTransactionRequest{
		Description: "Office rent",
		Entries: []ledger.Entry{
			{AccountID: opex.ID, Amount: 2_000, Direction: ledger.Debit},
			{AccountID: accts.VaultCash, Amount: 2_000, Direction: ledger.Credit},
		},
	}))

	// Monthly account fee: customer deposit (liability) down, Fee Income
	// (revenue) up.
	pos := r.position(p, customer)
	fee := must(p.Ledger.PostTransaction(r.ctx, ledger.PostTransactionRequest{
		Description: "Monthly account fee",
		Entries: []ledger.Entry{
			{AccountID: pos.Account, Subsidiary: pos.Subsidiary, Amount: 500, Direction: ledger.Debit},
			{AccountID: feeIncome.ID, Amount: 500, Direction: ledger.Credit},
		},
	}))

	// Reversed as a goodwill waiver, which is what a reversal looks like.
	must(p.Ledger.ReverseTransaction(r.ctx, fee.ID, "Fee waived — goodwill"))
}
