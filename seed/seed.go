package seed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/flow"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/provision"
)

// BaseDate anchors the deterministic seed timeline, and is therefore where a
// deployment holding no business date of its own begins: everything this
// scenario builds is dated relative to it, and the days an operator advances
// afterwards run on from where it left off.
var BaseDate = time.Date(2025, 9, 15, 9, 0, 0, 0, time.UTC)

// Dataset is the sample scenario together with the deployment clock it is built
// on.
type Dataset struct{ clock *calendar.Clock }

// New returns a Dataset built on clock.
func New(clock *calendar.Clock) *Dataset { return &Dataset{clock: clock} }

// Populate builds the full sample scenario (see the package doc) into the
// network's store, provisioning its banks and giving each one its place in the
// deployment it is given.
func (d *Dataset) Populate(ctx context.Context, nets *payment.Networks, dep Deployment) (err error) {
	if dep == nil {
		return errors.New("seed: no deployment, so no bank in this scenario could be admitted to the scheme")
	}

	// "Has anything been built here already", asked of the DEPLOYMENT rather than
	// of an institution.
	existing, err := nets.Stores().Banks(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}

	defer func() {
		if e := recoverBuild(recover()); e != nil {
			err = e
		}
	}()

	if err := d.clock.Rewind(BaseDate); err != nil {
		return err
	}
	b := &builder{
		ctx: ctx, nets: nets, dep: dep, clock: d.clock,
		cats: map[payment.ParticipantID]catalogue{},
	}
	b.build()
	return nil
}

// seedErr marks a panic raised by must or check, so it can be told apart from
// every other panic that might unwind through the builder.
type seedErr struct{ err error }

func (e seedErr) Error() string { return e.err.Error() }
func (e seedErr) Unwrap() error { return e.err }

// recoverBuild converts the builder's own must/check panic into an error and
// re-panics anything else.
func recoverBuild(r any) error {
	if r == nil {
		return nil
	}
	se, ok := r.(seedErr)
	if !ok {
		panic(r)
	}
	return fmt.Errorf("seed: %w", se.err)
}

// There is no Network() convenience constructor, and the reason is worth
// recording rather than rediscovering.

type builder struct {
	ctx context.Context
	// nets mints one payment.Network per institution, and the builder below names
	// which institution performs each act.
	nets *payment.Networks
	// dep is the running system: the acts the builder cannot perform for itself,
	// because each needs more than one institution. See Deployment.
	dep   Deployment
	clock *calendar.Clock
	// cats holds each bank's product IDs, keyed by participant: the
	// catalogue is book-scoped, so every bank has its own Basic and Premium and
	// the same name at two banks is two products.
	cats map[payment.ParticipantID]catalogue
}

// catalogue is the two product IDs the rest of the seed needs.
type catalogue struct{ basic, premium product.ID }

// bank, csm and cb are the three institutions' views, one call each.
func (b *builder) bank(bic iso20022.BIC) *payment.BankNetwork {
	return must(b.nets.Bank(b.ctx, payment.ParticipantID(bic)))
}
func (b *builder) csm() *payment.ClearingHouseNetwork { return b.nets.ClearingHouse() }
func (b *builder) cb() *payment.CentralBankNetwork    { return b.nets.CentralBank() }

// must returns v, panicking on a non-nil error. Seed data is hardcoded and
// deterministic, so any error is a programming bug that should fail loudly.
func must[T any](v T, err error) T {
	if err != nil {
		panic(seedErr{err})
	}
	return v
}

// must2 is must for a call returning two values and an error. SettleCycle is the
// only one, and a second generic beats spelling its error handling out.
func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(seedErr{err})
	}
	return a, b
}

// check panics on a non-nil error from a call that returns only an error.
func check(err error) {
	if err != nil {
		panic(seedErr{err})
	}
}

// provision writes one bank's three rows — its own, its settlement account in
// the central bank's book, its roster entry in the clearing house's — and gives
// it its place in the network.
func (b *builder) provision(name string, bic iso20022.BIC, country iban.Country, assets []ledger.AssetCode) *payment.Bank {
	bank := must(provision.Bank(b.ctx, b.nets, provision.BankSpec{
		Name: name, BIC: bic, Country: country, Assets: assets,
	}))
	check(b.dep.AddBank(b.ctx, bank))
	return bank
}

// subscribe is one bank pulling the scheme's routing directory, through the
// same door an operator's POST /directory/banks/refresh goes through.
func (b *builder) subscribe(p *payment.Bank) {
	must(b.dep.RefreshDirectory(b.ctx, p.BIC))
}

// seedAsset is the asset the whole sample scenario is denominated in.
const seedAsset ledger.AssetCode = "EUR"

// products prices a bank's catalogue: the Basic Current Account its onboarding
// created, and a Premium one an account can be migrated to.
func (b *builder) products(p *payment.Bank) {
	from := ledger.DayStart(b.clock.Now())

	b.publish(p, p.ProductID, from.AddDate(0, 0, 1), product.OverdraftPricing{
		Rate: 120_000, UnarrangedRate: 350_000, DayCount: interest.ACT365,
	})
	b.publish(p, p.ProductID, from.AddDate(0, 0, 30), product.OverdraftPricing{
		Rate: 149_000, UnarrangedRate: 350_000, DayCount: interest.ACT365,
	})

	premium := must(p.Catalogue.CreateProduct(b.ctx, "Premium Current Account", product.CurrentAccount))
	b.publish(p, premium.ID, from, product.OverdraftPricing{
		Rate: 70_000, UnarrangedRate: 250_000, DayCount: interest.ACT365,
	})

	b.cats[p.ID] = catalogue{basic: p.ProductID, premium: premium.ID}
}

// publish drafts and publishes in one step, which is what every seeded version
// wants: the draft state is a thing an operator passes through, not a thing the
// demo data should sit in.
func (b *builder) publish(p *payment.Bank, id product.ID, from time.Time, pricing product.OverdraftPricing) {
	must(p.Catalogue.DraftVersion(b.ctx, id, from, pricing))
	must(p.Catalogue.PublishVersion(b.ctx, id, from))
}

// open opens a customer account on the bank's Basic product and returns it.
func (b *builder) open(p *payment.Bank, name string) deposit.Account {
	return b.openOverdraft(p, name, 0)
}

// openOverdraft opens a customer account with an overdraft limit.
func (b *builder) openOverdraft(p *payment.Bank, name string, limit ledger.Amount) deposit.Account {
	return must(p.Deposit.OpenAccount(b.ctx, name, seedAsset, b.cats[p.ID].basic, limit))
}

// openLoan opens a term loan and disburses it in full into the borrower's own
// account, so the caller is left with a facility that has begun accruing.
func (b *builder) openLoan(p *payment.Bank, borrower deposit.Account, name string, principal ledger.Amount, rate interest.Rate, termMonths int, firstDue time.Time, description string) lending.Facility {
	loan := must(p.Lending.OpenTermLoan(b.ctx, name, seedAsset, principal, rate, interest.ACT365, lending.Annuity, termMonths))
	borrowerPos := must(p.Deposit.Position(b.ctx, borrower.ID))
	must(p.Lending.Disburse(b.ctx, loan.ID, borrowerPos, firstDue, description))
	return must(p.Lending.GetFacility(b.ctx, loan.ID))
}

// openLine opens a revolving line and draws it once into the borrower's own
// account, so the caller is left with a facility carrying a balance.
func (b *builder) openLine(p *payment.Bank, borrower deposit.Account, name string, limit ledger.Amount, rate interest.Rate, minPayment interest.Fraction, draw ledger.Amount, description string) lending.Facility {
	line := must(p.Lending.OpenRevolvingLine(b.ctx, name, seedAsset, limit, rate, interest.ACT365, minPayment))
	borrowerPos := must(p.Deposit.Position(b.ctx, borrower.ID))
	must(p.Lending.Draw(b.ctx, line.ID, borrowerPos, draw, description))
	return must(p.Lending.GetFacility(b.ctx, line.ID))
}

// day moves the business date on by one, and it is the only step this builder
// takes.
func (b *builder) day() { must(b.clock.Advance()) }

// runDays advances the clock a day at a time, driving RunEndOfDay through each
// one — the same entry point payment.Bank exposes to the API — so the
// seed's accrual and arrears move exactly as a running day would produce them.
func (b *builder) runDays(p *payment.Bank, days int) {
	for i := 0; i < days; i++ {
		b.day()
		check(p.RunEndOfDay(b.ctx, b.clock.Now()))
	}
}

// ref builds a PartyRef for a customer deposit account from the account's own
// IBAN identifier, so the same account always produces an identical PartyRef.
func (b *builder) ref(acct deposit.Account) payment.PartyRef {
	ref := payment.PartyRef{Account: acct.ID}
	for _, ident := range acct.Identifiers {
		if ident.Scheme == deposit.IdentifierIBAN {
			ref.Identifier = ident
			break
		}
	}
	return ref
}

// fund credits a deposit account with cash, which the bank then holds as vault
// cash.
func (b *builder) fund(p *payment.Bank, acct deposit.Account, amount ledger.Amount) {
	check(b.bank(p.BIC).Deposit(b.ctx, p.ID, acct.ID, amount, "Opening deposit"))
}

// lodge moves one bank's vault cash onto its reserve at the central bank: the
// member's own swap, and the settlement agent's credit that matches it.
func (b *builder) lodge(p *payment.Bank, amount ledger.Amount) {
	in, _ := must2(b.bank(p.BIC).LodgeReserves(b.ctx, seedAsset, amount, payment.MessageContext{
		From:  p.BIC,
		To:    b.dep.CentralBankBIC(),
		MsgID: fmt.Sprintf("seed-lodge-%s-%s", p.BIC, seedAsset),
		Now:   b.clock.Now(),
	}))
	must(b.cb().ReceiveLodgement(b.ctx, in))
}

// initiate is flow.Initiate: every institution's half of an initiation, leaving
// the payment Accepted in its scheme's open cycle and the submitting bank's own
// copy Accepted with it.
func (b *builder) initiate(req payment.InitiatePaymentRequest) payment.Payment {
	return must(flow.Initiate(b.ctx, b.nets, req))
}

// reject is flow.Reject: the clearing house's transition and each party's
// record of it, leaving the payment Rejected with the payer's money back in
// their account.
func (b *builder) reject(id payment.PaymentID, code iso20022.StatusReason, reason string) {
	must(flow.Reject(b.ctx, b.nets, id, code, reason))
}

// returnPayment is flow.Return, leaving the payment Returned, both customers
// put back where they were and both banks' clearing suspense at zero.
func (b *builder) returnPayment(id payment.PaymentID, reason string) {
	must(flow.Return(b.ctx, b.nets, id, reason))
}

// settle is flow.Settle, leaving the cycle Settled, every payment Settled and
// every bank's clearing suspense back at zero.
func (b *builder) settle(id payment.CycleID) {
	must(flow.Settle(b.ctx, b.nets, id, b.dep.CentralBankBIC()))
}

// initSCT submits a credit transfer.
func (b *builder) sct(dp *payment.Bank, d deposit.Account, cp *payment.Bank, c deposit.Account, amount ledger.Amount, e2e, desc string) payment.InitiatePaymentRequest {
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      b.ref(d),
		Creditor:    b.ref(c),
		Amount:      amount,
		EndToEndID:  e2e,
		Description: desc,
		// BOTH sides, where an instruction from a customer would name only the
		// counterparty's.
		DebtorDetails:   payment.PartyDetails{Agent: dp.BIC, Name: d.Name},
		CreditorDetails: payment.PartyDetails{Agent: cp.BIC, Name: c.Name},
	}
}

// sdd builds a direct debit. It is the SUBMITTING (creditor's) bank, so the
// request must name the counterparty: the name on the debtor's account, and the
// debtor's bank. See sct.
func (b *builder) sdd(dp *payment.Bank, d deposit.Account, cp *payment.Bank, c deposit.Account, amount ledger.Amount, mandate payment.MandateID, e2e, desc string) payment.InitiatePaymentRequest {
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPADD,
		Debtor:      b.ref(d),
		Creditor:    b.ref(c),
		Amount:      amount,
		MandateID:   mandate,
		EndToEndID:  e2e,
		Description: desc,
		// Both sides; see sct.
		DebtorDetails:   payment.PartyDetails{Agent: dp.BIC, Name: d.Name},
		CreditorDetails: payment.PartyDetails{Agent: cp.BIC, Name: c.Name},
	}
}

// submit hands an instruction to the deployment's own door, which is the other
// way a payment enters this scenario and the one that leaves a FILE behind it.
func (b *builder) submit(req payment.InitiatePaymentRequest) payment.Payment {
	return must(b.dep.Submit(b.ctx, req))
}

func (b *builder) build() {
	// --- Banks ------------------------------------------------------------- Each
	// bank joins the network as a euro bank, and joining is three rows in three
	// databases: founding opens its suspense and reserve accounts in its own book,
	// the central bank opens its settlement account in the central bank's, and the
	// clearing house puts it in the roster.
	euro := []ledger.AssetCode{seedAsset}
	// Each bank issues addresses in the country its BIC names, under a bank code
	// of its country's own width — eight digits in Germany, five in Italy and
	// France, three in Sweden.
	aurora := b.provision("Aurora Bank", "AURODEFFXXX", iban.DE, euro)
	verde := b.provision("Banca Verde", "VERDITMMXXX", iban.IT, euro)
	nord := b.provision("Nordhaven Bank", "NORDSESSXXX", iban.SE, euro)
	soleil := b.provision("Crédit Soleil", "SOLEFRPPXXX", iban.FR, euro)

	// --- Each bank subscribes to the routing directory ---------------------
	for _, p := range []*payment.Bank{aurora, verde, nord, soleil} {
		b.subscribe(p)
	}

	// --- Each bank's catalogue ---------------------------------------------
	// Before any account, because every deposit account is opened FROM a product:
	// a floating terms row with no product would have nothing to float to.
	for _, p := range []*payment.Bank{aurora, verde, nord, soleil} {
		b.products(p)
	}

	// --- Customer accounts (each bank mints its own addresses) -------------
	alice := b.open(aurora, "Alice Andersson")
	aaron := b.open(aurora, "Aaron Apstorp")
	annie := b.open(aurora, "Annie Ahlberg")      // -> Dormant
	merchant := b.open(aurora, "Aurora Merchant") // hold-capture counterparty
	oldAcct := b.open(aurora, "Closed Account")   // -> Closed

	bruno := b.openOverdraft(verde, "Bruno Bianchi", 50_000) // 500.00 overdraft
	bella := b.open(verde, "Bella Bruno")
	bianca := b.open(verde, "Bianca Belli") // -> Frozen

	nora := b.open(nord, "Nora Nilsson")
	niklas := b.open(nord, "Niklas Nyborg")

	chloe := b.open(soleil, "Chloé Caron")
	claude := b.open(soleil, "Claude Clément")

	// --- Funding: cash in, which each bank holds as vault cash -------------
	b.fund(aurora, alice, 200_000)
	b.fund(aurora, aaron, 50_000)
	b.fund(aurora, annie, 30_000)
	b.fund(verde, bruno, 150_000)
	b.fund(verde, bella, 80_000)
	b.fund(verde, bianca, 40_000)
	b.fund(nord, nora, 300_000)
	b.fund(nord, niklas, 60_000)
	b.fund(soleil, chloe, 120_000)
	b.fund(soleil, claude, 90_000)

	// --- Lodgement: each bank places that cash on reserve -------------------
	b.lodge(aurora, 280_000)
	b.lodge(verde, 270_000)
	b.lodge(nord, 360_000)
	b.lodge(soleil, 210_000)

	// --- Holds on Alice: active / released / captured ----------------------
	ctx := b.ctx
	must(aurora.Deposit.CreateHold(ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 10_000, Description: "Card pre-authorisation (hotel)",
	}))
	released := must(aurora.Deposit.CreateHold(ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 5_000, Description: "Cancelled online order",
	}))
	check(aurora.Deposit.ReleaseHold(ctx, released.ID))
	captured := must(aurora.Deposit.CreateHold(ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 15_000, Description: "Card payment at Aurora Merchant",
	}))
	merchantPos := must(aurora.Deposit.Position(ctx, merchant.ID))
	must(aurora.Deposit.CaptureHold(ctx, captured.ID, merchantPos, 0, "Captured: card payment"))

	// --- End-of-day snapshots for Alice across two business days -----------
	must(aurora.Deposit.TakeEndOfDaySnapshot(ctx, alice.ID, b.clock.Now()))
	b.day()
	must(aurora.Deposit.TakeEndOfDaySnapshot(ctx, alice.ID, b.clock.Now()))

	// --- Account status lifecycle ------------------------------------------
	check(aurora.Deposit.MarkDormant(ctx, annie.ID)) // Active -> Dormant
	check(aurora.Deposit.Close(ctx, oldAcct.ID))     // zero balance -> Closed
	check(verde.Deposit.Freeze(ctx, bianca.ID))      // Active -> Frozen

	// --- Mandates for SEPA Direct Debit ------------------------------------
	m1 := must(b.bank(nord.BIC).CreateMandate(b.ctx, soleil.BIC, b.ref(chloe), b.ref(nora), 100_000))
	m2 := must(b.bank(aurora.BIC).CreateMandate(b.ctx, verde.BIC, b.ref(bruno), b.ref(aaron), 0))
	m3 := must(b.bank(soleil.BIC).CreateMandate(b.ctx, nord.BIC, b.ref(niklas), b.ref(claude), 25_000))
	check(b.bank(soleil.BIC).RevokeMandate(b.ctx, m3.ID)) // revoked, for display

	// --- Phase A: a fully settled SEPA Credit Transfer cycle ---------------
	sct1 := must(b.csm().OpenCycle(b.ctx, payment.SchemeSEPACT))
	b.initiate(b.sct(aurora, alice, nord, niklas, 25_000, "SCT-001", "Rent to N. Nyborg"))
	b.initiate(b.sct(nord, nora, verde, bella, 40_000, "SCT-002", "Invoice 2025-77"))
	b.initiate(b.sct(verde, bruno, soleil, chloe, 30_000, "SCT-003", "Consulting fee"))
	must(b.csm().CloseCycle(b.ctx, sct1.ID))
	b.settle(sct1.ID)

	b.day()

	// --- Phase B: a settled SEPA Direct Debit cycle (one will be returned) --
	sdd1 := must(b.csm().OpenCycle(b.ctx, payment.SchemeSEPADD))
	b.initiate(b.sdd(soleil, chloe, nord, nora, 20_000, m1.ID, "SDD-001", "Utility direct debit"))
	returned := b.initiate(b.sdd(verde, bruno, aurora, aaron, 12_000, m2.ID, "SDD-002", "Gym membership"))
	must(b.csm().CloseCycle(b.ctx, sdd1.ID))
	b.settle(sdd1.ID)

	// --- Phase C: return the settled direct debit (an R-transaction) --------
	b.returnPayment(returned.ID, "Debtor dispute — unauthorised collection")

	b.day()

	// --- Phase D: the two cycles this build LEAVES OPEN ---------------------
	must(b.csm().OpenCycle(b.ctx, payment.SchemeSEPACT))
	must(b.csm().OpenCycle(b.ctx, payment.SchemeSEPADD))

	// --- Phase E: the payments in flight, through the deployment's door -----
	b.submit(b.sct(aurora, aaron, soleil, claude, 8_000, "SCT-010", "Book order"))
	b.submit(b.sct(verde, bella, nord, niklas, 6_000, "SCT-011", "Shared dinner"))
	b.submit(b.sct(aurora, alice, verde, bella, 7_000, "SCT-020", "Birthday gift"))
	rejected := b.submit(b.sct(nord, nora, soleil, claude, 4_000, "SCT-021", "Deposit, flat viewing"))
	b.submit(b.sdd(soleil, chloe, nord, nora, 5_000, m1.ID, "SDD-010", "Monthly subscription"))
	b.submit(b.sdd(verde, bruno, aurora, aaron, 3_000, m2.ID, "SDD-011", "Gym membership"))

	// --- Lending: credit facilities across the network ---------------------
	b.lendingShowcase(aurora, verde, nord, alice, bruno, bella, niklas)

	// --- General-ledger primitives showcase on Aurora ----------------------
	b.glShowcase(aurora, aaron)

	// --- Phase G: the morning's files move, and nothing settles -------------
	check(b.dep.CarryToClearing(b.ctx))

	// --- Phase H: an operator rejects one out of the open cycle -------------
	b.reject(rejected.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "Payee's account details could not be confirmed")
}

// lendingShowcase exercises every state a credit facility can be in: a priced
// overdraft, a term loan part-way through its schedule, a revolving line with a
// billed cycle, a delinquent loan, and an overdraft that is actually accruing
// rather than sitting at a limit that costs nothing.
func (b *builder) lendingShowcase(aurora, verde, nord *payment.Bank, alice, bruno, bella, niklas deposit.Account) {
	ctx := b.ctx

	// --- Bruno's overdraft, priced ------------------------------------------ He
	// already has a 500.00 limit (openOverdraft, above); this is what makes
	// drawing on it cost him something rather than nothing: 15% arranged, 35% on
	// anything drawn beyond the limit.
	must(verde.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 150_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		b.clock.Now()))
	must(verde.Deposit.SetOverdraftLimit(ctx, bruno.ID, 50_000, b.clock.Now()))

	// --- Bella, migrated onto Premium ---------------------------------------
	// Effective a fortnight in.
	must(verde.Deposit.ChangeProduct(ctx, bella.ID, b.cats[verde.ID].premium,
		b.clock.Now().AddDate(0, 0, 14)))

	// --- A term loan part-way through its schedule (Alice, Aurora) ---------- EUR
	// 10,000, five years, 6%, annuity.
	t1 := b.clock.Now()
	firstDue := t1.AddDate(0, 1, 0)
	loan := b.openLoan(aurora, alice, "Alice Home Loan", 1_000_000, 60_000, 60, firstDue, "Home loan payout")
	alicePos := must(aurora.Deposit.Position(ctx, alice.ID))

	b.runDays(aurora, int(firstDue.Sub(t1)/(24*time.Hour)))
	sched := must(aurora.Lending.Schedule(ctx, loan.ID))
	must(aurora.Lending.Repay(ctx, loan.ID, alicePos, sched[0].Total(), b.clock.Now(), "Instalment 1"))

	secondDue := t1.AddDate(0, 2, 0)
	b.runDays(aurora, int(secondDue.Sub(firstDue)/(24*time.Hour)))
	sched = must(aurora.Lending.Schedule(ctx, loan.ID))
	must(aurora.Lending.Repay(ctx, loan.ID, alicePos, sched[1].Total(), b.clock.Now(), "Instalment 2"))

	b.runDays(aurora, 10) // a fresh accrual builds up; the third instalment is not yet due

	// --- A delinquent loan (Niklas, Nordhaven) ------------------------------- A
	// smaller loan than Alice's, disbursed and then left unpaid past two due
	// dates: one month plus twenty days past the first instalment, comfortably
	// inside the 30-59 bucket however the calendar months involved fall.
	t3 := b.clock.Now()
	niklasFirstDue := t3.AddDate(0, 1, 0)
	b.openLoan(nord, niklas, "Niklas Car Loan", 300_000, 90_000, 24, niklasFirstDue, "Car loan payout")
	target := t3.AddDate(0, 2, 20)
	b.runDays(nord, int(target.Sub(t3)/(24*time.Hour)))

	// --- Bruno, pushed into overdraft and accruing -------------------------- A
	// card settlement pushes him into his priced overdraft.
	check(verde.RunEndOfDay(ctx, b.clock.Now()))

	brunoBalance := must(verde.Deposit.GetBalance(ctx, bruno.ID))
	overdrawBy := ledger.Amount(20_000) // EUR 200 into the EUR 500 arranged limit
	b.submit(b.sct(verde, bruno, aurora, alice, brunoBalance.Book+overdrawBy, "SCT-030", "Card settlement"))

	b.runDays(verde, 45)
	must(verde.Deposit.ChargeOverdraftInterest(ctx, bruno.ID, b.clock.Now()))

	// --- Bruno, repriced mid-life ------------------------------------------- The
	// arranged rate moves from 15% to 18%, effective TWENTY DAYS AGO — a repricing
	// agreed on one date and entered on another, which is the ordinary case and
	// the one mutable terms could not represent at all.
	must(verde.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 180_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		b.clock.Now().AddDate(0, 0, -20)))

	b.runDays(verde, 15)

	// --- A revolving line, partly drawn and billed (Bella, Verde) ----------- EUR
	// 2,500 limit, 18%, 2% minimum payment.
	line := b.openLine(verde, bella, "Bella Card Line", 250_000, 180_000, 20_000, 100_000, "Card line draw")
	b.runDays(verde, 30)
	must(verde.Lending.ChargeInterest(ctx, line.ID, b.clock.Now()))
}

// glShowcase exercises the raw general-ledger primitives on one bank so that
// all five account types (Asset, Liability, Equity, Revenue, Expense) and a
// manual transaction + reversal appear in the data.
func (b *builder) glShowcase(p *payment.Bank, customer deposit.Account) {
	ctx := b.ctx
	glID := must(p.Ledger.GetSubledger(ctx, p.CustomerSubledger)).LedgerID

	equitySub := must(p.Ledger.CreateSubledger(ctx, glID, "Equity"))
	shareCapital := must(p.Ledger.CreateAccount(ctx, equitySub.ID, "Share Capital", ledger.Equity, seedAsset))
	treasurySub := must(p.Ledger.CreateSubledger(ctx, glID, "Treasury"))
	vault := must(p.Ledger.CreateAccount(ctx, treasurySub.ID, "Vault Cash", ledger.Asset, seedAsset))
	incomeSub := must(p.Ledger.CreateSubledger(ctx, glID, "Income"))
	feeIncome := must(p.Ledger.CreateAccount(ctx, incomeSub.ID, "Fee Income", ledger.Revenue, seedAsset))
	expenseSub := must(p.Ledger.CreateSubledger(ctx, glID, "Expenses"))
	opex := must(p.Ledger.CreateAccount(ctx, expenseSub.ID, "Operating Expenses", ledger.Expense, seedAsset))

	// Capitalisation: Vault Cash (asset) up, Share Capital (equity) up.
	must(p.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Initial share capital",
		Entries: []ledger.Entry{
			{AccountID: vault.ID, Amount: 100_000, Direction: ledger.Debit},
			{AccountID: shareCapital.ID, Amount: 100_000, Direction: ledger.Credit},
		},
	}))

	// Operating expense: Operating Expenses (expense) up, Vault Cash (asset) down.
	must(p.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Office rent",
		Entries: []ledger.Entry{
			{AccountID: opex.ID, Amount: 2_000, Direction: ledger.Debit},
			{AccountID: vault.ID, Amount: 2_000, Direction: ledger.Credit},
		},
	}))

	// Monthly account fee: customer deposit (liability) down, Fee Income (revenue) up.
	customerPos := must(p.Deposit.Position(ctx, customer.ID))
	fee := must(p.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Monthly account fee",
		Entries: []ledger.Entry{
			{AccountID: customerPos.Account, Subsidiary: customerPos.Subsidiary, Amount: 500, Direction: ledger.Debit},
			{AccountID: feeIncome.ID, Amount: 500, Direction: ledger.Credit},
		},
	}))

	// Reverse the fee (goodwill waiver) to demonstrate a reversal.
	must(p.Ledger.ReverseTransaction(ctx, fee.ID, "Fee waived — goodwill"))
}
