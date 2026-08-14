package main

import (
	"context"
	"fmt"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/node"
	"github.com/raphi011/cbs/payment"
)

// What every scenario is written against: one door an operator has per helper,
// and deliberately nothing that reaches past one. A payment built on
// payment/flow reads as settled and appears in no message log and on no graph.

// scenarioAsset is what every scenario below is denominated in.
const scenarioAsset ledger.AssetCode = "EUR"

// The four banks the base state founds, named where a scenario picks two.
const (
	auroraBIC iso20022.BIC = "AURODEFFXXX"
	verdeBIC  iso20022.BIC = "VERDITMMXXX"
	nordBIC   iso20022.BIC = "NORDSESSXXX"
	soleilBIC iso20022.BIC = "SOLEFRPPXXX"
)

// A runner is one scenario in progress: the deployment it drives and the
// context it drives it in.
type runner struct {
	ctx context.Context
	d   *Deployment
}

// scenarioErr marks a panic raised by must or check, so it can be told apart
// from every other panic that might unwind through a scenario.
type scenarioErr struct{ err error }

func (e scenarioErr) Error() string { return e.err.Error() }
func (e scenarioErr) Unwrap() error { return e.err }

// run drives one scenario body, converting its must/check panic into an error.
// A scenario's steps are hardcoded, so a failure is a programming bug and the
// alternative is six hundred lines of error handling that says nothing.
func run(ctx context.Context, d *Deployment, body func(*runner)) (err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		se, ok := r.(scenarioErr)
		if !ok {
			panic(r)
		}
		err = fmt.Errorf("scenario: %w", se.err)
	}()
	body(&runner{ctx: ctx, d: d})
	return nil
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(scenarioErr{err})
	}
	return v
}

func check(err error) {
	if err != nil {
		panic(scenarioErr{err})
	}
}

// ---------------------------------------------------------------------------
// One bank's own acts
// ---------------------------------------------------------------------------

// bank is one member's own row, with its ledger, deposit, lending and catalogue
// handles bound. It is that bank's back office, which is a door an operator has.
func (r *runner) bank(bic iso20022.BIC) *payment.Bank {
	pid := payment.ParticipantID(bic)
	node := must(r.d.Bank(r.ctx, pid))
	return must(node.Network().GetBank(r.ctx, pid))
}

// open opens a customer account on the bank's default product.
func (r *runner) open(p *payment.Bank, name string) deposit.Account {
	return r.openOverdraft(p, name, 0)
}

// openOverdraft opens one with an arranged overdraft limit.
func (r *runner) openOverdraft(p *payment.Bank, name string, limit ledger.Amount) deposit.Account {
	return must(p.Deposit.OpenAccount(r.ctx, name, scenarioAsset, p.ProductID, limit))
}

// fund takes cash in over the counter: the customer gets a balance and the bank
// holds the notes.
func (r *runner) fund(p *payment.Bank, acct deposit.Account, amount ledger.Amount) {
	check(must(r.d.Bank(r.ctx, p.ID)).Network().Deposit(r.ctx, p.ID, acct.ID, amount, "Opening deposit"))
}

// lodge places that cash on reserve, THROUGH THE WIRE: a camt.050 to the
// settlement agent, credited when the day reaches its work and advised back to
// the member when the day reaches the collection.
func (r *runner) lodge(p *payment.Bank, amount ledger.Amount) {
	must(r.d.Lodge(r.ctx, p.BIC, scenarioAsset, amount))
}

// position is one account as a posting names it.
func (r *runner) position(p *payment.Bank, acct deposit.Account) ledger.Position {
	return must(p.Deposit.Position(r.ctx, acct.ID))
}

// ---------------------------------------------------------------------------
// The payment path
// ---------------------------------------------------------------------------

// ref builds a PartyRef from an account's own IBAN, so the same account always
// produces an identical one.
func (r *runner) ref(acct deposit.Account) payment.PartyRef {
	out := payment.PartyRef{Account: acct.ID}
	for _, ident := range acct.Identifiers {
		if ident.Scheme == deposit.IdentifierIBAN {
			out.Identifier = ident
			break
		}
	}
	return out
}

// sct is a credit transfer, named from both ends. A customer's own instruction
// names only the counterparty's side; a scenario is standing outside both banks
// and says which is which.
func (r *runner) sct(dp *payment.Bank, d deposit.Account, cp *payment.Bank, c deposit.Account,
	amount ledger.Amount, e2e, desc string) payment.InitiatePaymentRequest {

	return payment.InitiatePaymentRequest{
		Scheme:          payment.SchemeSEPACT,
		Debtor:          r.ref(d),
		Creditor:        r.ref(c),
		Amount:          amount,
		EndToEndID:      e2e,
		Description:     desc,
		DebtorDetails:   payment.PartyDetails{Agent: dp.BIC, Name: d.Name},
		CreditorDetails: payment.PartyDetails{Agent: cp.BIC, Name: c.Name},
	}
}

// sdd is a direct debit. The SUBMITTER is the payee's bank, so the request names
// the debtor's side as the counterparty; see sct.
func (r *runner) sdd(dp *payment.Bank, d deposit.Account, cp *payment.Bank, c deposit.Account,
	amount ledger.Amount, mandate payment.MandateID, e2e, desc string) payment.InitiatePaymentRequest {

	return payment.InitiatePaymentRequest{
		Scheme:          payment.SchemeSEPADD,
		Debtor:          r.ref(d),
		Creditor:        r.ref(c),
		Amount:          amount,
		MandateID:       mandate,
		EndToEndID:      e2e,
		Description:     desc,
		DebtorDetails:   payment.PartyDetails{Agent: dp.BIC, Name: d.Name},
		CreditorDetails: payment.PartyDetails{Agent: cp.BIC, Name: c.Name},
	}
}

// submit hands an instruction to the bank the scheme makes the submitter, which
// runs its own half and puts the payment in its hub. Nothing has left that bank
// when this returns.
func (r *runner) submit(req payment.InitiatePaymentRequest) payment.Payment {
	return must(r.d.Submit(r.ctx, req))
}

// carry moves the morning's files and stops before anything settles: every
// member's cut-off, the clearing house's work over what they uploaded, and each
// member collecting the answers.
func (r *runner) carry() {
	check(node.JoinProblemDetails(r.d.carryToClearing(r.ctx)))
}

// reject is the clearing house declining a payment on the operator's say-so. The
// payer's bank is TOLD, and gives the money back when it next collects.
func (r *runner) reject(id payment.PaymentID, reason string) {
	must(r.d.ClearingHouse().Reject(r.ctx, id, iso20022.StatusReasonNotSpecifiedAgentGenerated, reason))
}

// returnPayment sends a settled payment back. The money moves later, at the
// settlement agent, which is why every caller runs a day after it.
func (r *runner) returnPayment(id payment.PaymentID, reason string) {
	check(r.d.Return(r.ctx, id, iso20022.ReturnReasonNotSpecifiedAgentGenerated, reason))
}

// ---------------------------------------------------------------------------
// The clock
// ---------------------------------------------------------------------------

// day runs a whole business day and moves the date, exactly as the operator's
// own button does. The lock is already held: see RunScenario.
func (r *runner) day() {
	if _, err := r.d.advanceDay(r.ctx); err != nil {
		panic(scenarioErr{err})
	}
}

// days runs that many of them.
func (r *runner) days(n int) {
	for range n {
		r.day()
	}
}

// clearingDay runs a day that CLEARS. On a weekend or a TARGET closure a day
// accrues interest and moves nothing else, so a scenario carrying a payment has
// to reach an open day rather than assume the next one is.
func (r *runner) clearingDay() {
	for !r.d.BusinessDate().SettlementDay {
		r.day()
	}
	r.day()
}

// step runs the business day as far as one named phase without moving the
// clock, which is the same door the phase stepper on the lobby opens.
func (r *runner) step(phase string) {
	if _, err := r.d.runThrough(r.ctx, phase); err != nil {
		panic(scenarioErr{err})
	}
}

// payment is the CLEARING HOUSE's copy, which is the only place to learn what
// became of one: a bank holds its own leg and nothing else.
func (r *runner) payment(id payment.PaymentID) payment.Payment {
	return must(r.d.ClearingHouse().Network().GetPayment(r.ctx, id))
}
