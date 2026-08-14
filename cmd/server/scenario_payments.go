package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
)

// The three scenarios about a payment, and the one about a borrower. Each is
// chosen for what it makes VISIBLE, and each reaches its outcome naturally: the
// refusal below is the clearing house's own, and nothing here names a code.

// scenarioFunding is what a scenario's payer is given, and scenarioAmount is
// what they send. Funding is far larger, so nothing here is ever refused for
// want of money unless that is the point.
const (
	scenarioFunding ledger.Amount = 500_000
	scenarioAmount  ledger.Amount = 120_000
)

var scenarioPayment = Scenario{
	ID:   "payment",
	Name: "A payment between two banks",
	Description: "Opens an account at Aurora Bank and one at Nordhaven Bank, funds the payer, " +
		"and carries one credit transfer the whole way: uploaded at the cut-off, cleared, " +
		"netted, settled in central-bank money and released to the payee's bank.",
	Run: func(ctx context.Context, d *Deployment) error {
		return run(ctx, d, func(r *runner) {
			payer, payee := r.twoCustomers("Alice Andersson", "Niklas Nyborg")

			p := r.submit(r.sct(payer.bank, payer.acct, payee.bank, payee.acct,
				scenarioAmount, "SCT-DEMO-1", "Rent"))
			// One day carries it the whole way: cut-off, clearing, the clearing
			// house's own cut-off, settlement, release and collection.
			r.clearingDay()

			r.mustReach(p.ID, payment.Settled)
		})
	},
}

var scenarioRejected = Scenario{
	ID:   "rejected",
	Name: "A payment that misses the cut-off",
	Description: "The same opening, and then a credit transfer handed over after the clearing " +
		"house has shut its window for the day. Its bank uploads it anyway, the clearing house " +
		"has no cycle to take it into and refuses it, and the payer gets their money back.",
	Run: func(ctx context.Context, d *Deployment) error {
		return run(ctx, d, func(r *runner) {
			payer, payee := r.twoCustomers("Aaron Apstorp", "Nora Nilsson")

			// The day, stepped as far as the clearing house's own cut-off, which is
			// where every open window is netted and closed. Nothing here names a
			// reason code: what follows is refused because the window is shut.
			r.step(phaseKey("clearing-house cut-off"))

			p := r.submit(r.sct(payer.bank, payer.acct, payee.bank, payee.acct,
				scenarioAmount, "SCT-DEMO-2", "Deposit, flat viewing"))
			// The bank's own cut-off, the clearing house's refusal, and the pacs.002
			// carrying it back — which is the collection that refunds the payer.
			r.carry()
			r.mustReach(p.ID, payment.Rejected)

			// And the day is finished, so the deployment is left on a fresh one with
			// its windows open rather than mid-way through a closed one.
			r.day()
		})
	},
}

var scenarioReturned = Scenario{
	ID:   "returned",
	Name: "A settled payment returned",
	Description: "A credit transfer carried to finality, and then sent back: an R-transaction. " +
		"The reserves move the other way at the settlement agent, both banks book their mirrors, " +
		"and both customers end where they started.",
	Run: func(ctx context.Context, d *Deployment) error {
		return run(ctx, d, func(r *runner) {
			payer, payee := r.twoCustomers("Bella Bruno", "Chloé Caron")

			p := r.submit(r.sct(payer.bank, payer.acct, payee.bank, payee.acct,
				scenarioAmount, "SCT-DEMO-3", "Invoice 2025-77"))
			r.clearingDay()
			r.mustReach(p.ID, payment.Settled)

			// Finality is the return's precondition, which is why this is two days and
			// not one: nothing can be sent back before it has arrived.
			r.returnPayment(p.ID, "Debtor dispute — unauthorised transfer")
			r.clearingDay()
			r.mustReach(p.ID, payment.Returned)
		})
	},
}

var scenarioArrears = Scenario{
	ID:   "arrears",
	Name: "A borrower falls behind",
	Description: "Opens a term loan at Banca Verde, disburses it into the borrower's account, and " +
		"advances the business date past two instalments nobody pays. Interest accrues over every " +
		"day in between, weekends included, which is what day-count conventions are for.",
	Run: func(ctx context.Context, d *Deployment) error {
		return run(ctx, d, func(r *runner) {
			bank := r.bank(verdeBIC)
			borrower := r.open(bank, "Bruno Bianchi")
			// Funded and lodged before anything is lent: cash paid in over the
			// counter is what the bank places on reserve, and a borrower who can
			// repay nothing at all teaches less than one who chooses not to.
			r.fund(bank, borrower, scenarioFunding)
			r.lodge(bank, scenarioFunding)

			opened := r.d.clock.Now()
			firstDue := opened.AddDate(0, 1, 0)
			loan := must(bank.Lending.OpenTermLoan(r.ctx, "Bruno Car Loan", scenarioAsset,
				300_000, 90_000, interest.ACT365, lending.Annuity, 24))
			must(bank.Lending.Disburse(r.ctx, loan.ID, r.position(bank, borrower), firstDue, "Car loan payout"))

			// One month plus twenty days past the first instalment: comfortably inside
			// the 30-59 bucket however the calendar months involved fall.
			r.days(daysBetween(opened, opened.AddDate(0, 2, 20)))

			facility := must(bank.Lending.GetFacility(r.ctx, loan.ID))
			if facility.Arrears.DaysPastDue == 0 {
				panic(scenarioErr{errors.New(
					"the loan is current after two missed instalments; no end of day computed its arrears")})
			}
		})
	},
}

// ---------------------------------------------------------------------------
// What the four have in common
// ---------------------------------------------------------------------------

// customer is one party to a scenario's payment: which bank holds the account,
// and the account.
type customer struct {
	bank *payment.Bank
	acct deposit.Account
}

// twoCustomers opens a funded payer at Aurora Bank and a payee at Nordhaven
// Bank, which is every scenario's opening. The payer's cash is lodged onto
// reserve because a bank cannot settle out of its vault.
func (r *runner) twoCustomers(payerName, payeeName string) (customer, customer) {
	payerBank, payeeBank := r.bank(auroraBIC), r.bank(nordBIC)

	payer := customer{bank: payerBank, acct: r.open(payerBank, payerName)}
	r.fund(payerBank, payer.acct, scenarioFunding)
	r.lodge(payerBank, scenarioFunding)

	return payer, customer{bank: payeeBank, acct: r.open(payeeBank, payeeName)}
}

// mustReach fails the scenario if the payment did not end where the scenario
// says it does. A scenario that returned nil having produced nothing is exactly
// the defect the whole arrangement exists to prevent.
func (r *runner) mustReach(id payment.PaymentID, want payment.PaymentStatus) {
	got := r.payment(id)
	if got.Status != want {
		panic(scenarioErr{fmt.Errorf("%s is %v after the day that should have left it %v", id, got.Status, want)})
	}
}

// daysBetween is how many whole days one instant is after another.
func daysBetween(from, to time.Time) int {
	return int(to.Sub(from) / (24 * time.Hour))
}
