package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/mem"
)

// baseDate anchors the deterministic seed timeline. Everything built before the
// clock goes live is dated relative to this instant.
var baseDate = time.Date(2025, 9, 15, 9, 0, 0, 0, time.UTC)

// Dataset is the sample scenario together with the deterministic clock it is
// built on.
//
// The clock has to be owned here rather than passed in, because seeding
// controls it: it is frozen at baseDate and advanced step by step while the
// scenario is built, then switched to real time so that anything done
// afterwards — through the API, say — is timestamped live. Rebuilding after a
// store reset rewinds it, so a reset restores the dataset rather than a version
// of it shifted forward in time.
type Dataset struct{ clock *clock }

// New returns a Dataset with its clock frozen at baseDate.
func New() *Dataset { return &Dataset{clock: newClock(baseDate)} }

// Now is the time source every layer built over the dataset's store must read.
// Hand it to the store and to payment.NewNetwork so that booking dates, value
// dates and audit timestamps all come from the same clock.
func (d *Dataset) Now() time.Time { return d.clock.now() }

// Populate builds the full sample scenario (see the package doc) into the
// network's store.
//
// It is idempotent: a store that already holds participants is left alone. That
// is what makes it safe to call on every boot — against a database that
// outlives the process, seeding unconditionally would stack a second copy of
// the scenario on top of the first at every restart.
//
// # Why the probe is "any participants", and not "the whole scenario"
//
// The probe asks whether the store has content of its own, not whether this
// exact dataset is complete, and the distinction matters because the two have
// different false positives. A completeness check — "are there settlements? are
// there mandates?" — would refuse to boot against a Postgres database in which
// the user had created a couple of banks through the API and nothing else,
// which is an ordinary way to use this system and not a fault at all. There is
// no marker that separates "seeded and then edited" from "never seeded" without
// tagging rows, and a tag is a schema change in aid of a situation that should
// not arise.
//
// It should not arise because the half-built store this probe used to paper
// over had one cause: POST /admin/reset truncating and then losing its context
// before it reseeded. That is fixed where it belongs, in the handler, by
// running the reset on a context the client cannot cancel — see
// api.Server.handleReset. What remains is a process killed mid-seed, which
// leaves a partial dataset that this probe will indeed skip; the answer to that
// is to reset, which is now a single call that finishes.
//
// The clock goes live on every path out of Populate, including the one that
// built nothing. That is not a detail: the second process to open a store that
// outlives the first has a Dataset whose clock is still frozen at baseDate, and
// if the skip returned without releasing it, every payment, account, hold,
// snapshot and audit event that process went on to write would be timestamped
// 2025-09-15. Freezing the clock is a property of building the scenario, not of
// the Dataset.
//
// The scenario is hardcoded, so a failure while building it is a programming
// bug rather than a runtime condition, and the builder says so by panicking.
// Populate turns the builder's own panic back into an error at the package
// boundary, since its caller — the server's reset handler — has an error to
// report and a request to answer. Any other panic is re-raised with its stack
// intact; see recoverBuild.
func (d *Dataset) Populate(ctx context.Context, net *payment.Network) (err error) {
	// Registered first, so it runs last: whether the scenario was built now,
	// was already there, or failed halfway, the clock is released before
	// Populate returns.
	defer d.clock.goLive()

	existing, err := net.ListParticipants(ctx)
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

	d.clock.rewind(baseDate)
	b := &builder{
		ctx: ctx, net: net, clock: d.clock,
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
//
// The distinction matters because the builder drives four packages: a nil map
// write or a nil dereference in payment, deposit, ledger or store/mem is a bug,
// and flattening it into "seed: runtime error: invalid memory address" —
// returned as a 500 from POST /admin/reset with the stack discarded — would
// hide exactly the failures worth seeing. Only the errors the builder chose to
// raise are turned into errors.
//
// It takes the recovered value rather than calling recover itself, because
// recover only works when called directly by a deferred function.
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

// Network builds a fresh in-memory payment.Network populated with the sample
// scenario. It is the convenience form of New plus Populate, for tests and
// examples; the server wires the two together itself, because it needs to
// repopulate the same network after a reset.
func Network() *payment.Network {
	d := New()
	store := mem.New(d.Now)
	net := payment.NewNetwork(store.Payment(), d.Now)
	if err := d.Populate(context.Background(), net); err != nil {
		panic(err.Error()) // already prefixed with "seed: "
	}
	return net
}

type builder struct {
	ctx   context.Context
	net   *payment.Network
	clock *clock
	// cats holds each bank's product IDs, keyed by participant: the
	// catalogue is book-scoped, so every bank has its own Basic and Premium and
	// the same name at two banks is two products.
	cats map[payment.ParticipantID]catalogue
}

// catalogue is the two product IDs the rest of the seed needs.
type catalogue struct{ basic, premium product.ID }

// must returns v, panicking on a non-nil error. Seed data is hardcoded and
// deterministic, so any error is a programming bug that should fail loudly.
//
// The panic value is a seedErr rather than a string so that Populate can tell
// it apart from a genuine runtime panic and leave that one alone.
func must[T any](v T, err error) T {
	if err != nil {
		panic(seedErr{err})
	}
	return v
}

// check panics on a non-nil error from a call that returns only an error.
func check(err error) {
	if err != nil {
		panic(seedErr{err})
	}
}

// seedAsset is the asset the whole sample scenario is denominated in.
//
// The scenario is a SEPA one, and SEPA is a euro scheme, so every account it
// opens is a euro account. It is spelled out at each site rather than left to
// a default: the demo data is euro because the banks in it are euro banks, not
// because nobody said.
const seedAsset ledger.AssetCode = "EUR"

// products prices a bank's catalogue: the Basic Current Account its onboarding
// created, and a Premium one an account can be migrated to.
//
// Basic is the product AddParticipant already made — every bank gets one,
// because a bank with no product cannot open an account — and its first
// published version is interest-free, which is what a bank that has not yet
// decided a price has. This adds the two versions that give it one:
//
//   - 12% from day 1, the day after the bank opened.
//   - 14.9% from day 30, published now and effective then. That is the case no
//     per-account write can produce and the reason the catalogue exists: one
//     row moves the price for every account bound to it, and the accounts' own
//     rows are untouched.
//
// Both are forward-dated, which is the only direction PublishVersion allows.
func (b *builder) products(p *payment.Participant) {
	from := ledger.DayStart(b.clock.now())

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
func (b *builder) publish(p *payment.Participant, id product.ID, from time.Time, pricing product.OverdraftPricing) {
	must(p.Catalogue.DraftVersion(b.ctx, id, from, pricing))
	must(p.Catalogue.PublishVersion(b.ctx, id, from))
}

// open opens a customer account on the bank's Basic product, records its
// canonical IBAN, and returns it.
//
// It goes through the register rather than p.OpenCustomerAccount because that
// helper opens from the participant's configured default, and this seed has
// retired that one in favour of a priced catalogue of its own.
func (b *builder) open(p *payment.Participant, name, iban string) deposit.Account {
	return b.openOverdraft(p, name, iban, 0)
}

// openOverdraft opens a customer account with an overdraft limit and gives it
// the IBAN as its own identifier, so it is resolvable through
// Register.ResolveIdentifier rather than merely labelled, and so b.ref can
// read it straight back off the account rather than a second copy. The limit
// is per account and the PRICE is not: it comes from the Basic product, so the
// day-30 reprice above reaches every account opened here without touching one
// of them.
func (b *builder) openOverdraft(p *payment.Participant, name, iban string, limit ledger.Amount) deposit.Account {
	ident := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: iban}
	return must(p.Deposit.OpenAccount(b.ctx, p.CustomerSubledger, name, seedAsset, b.cats[p.ID].basic, limit, ident))
}

// openLoan opens a term loan and disburses it in full into the borrower's own
// account, so the caller is left with a facility that has begun accruing.
func (b *builder) openLoan(p *payment.Participant, borrower deposit.Account, name string, principal ledger.Amount, rate interest.Rate, termMonths int, firstDue time.Time, description string) lending.Facility {
	loan := must(p.Lending.OpenTermLoan(b.ctx, p.CustomerSubledger, name, seedAsset, principal, rate, interest.ACT365, lending.Annuity, termMonths))
	borrowerGL := must(p.Deposit.GetAccount(b.ctx, borrower.ID)).GLAccount
	must(p.Lending.Disburse(b.ctx, loan.ID, borrowerGL, firstDue, description))
	return must(p.Lending.GetFacility(b.ctx, loan.ID))
}

// openLine opens a revolving line and draws it once into the borrower's own
// account, so the caller is left with a facility carrying a balance.
func (b *builder) openLine(p *payment.Participant, borrower deposit.Account, name string, limit ledger.Amount, rate interest.Rate, minPayment interest.Fraction, draw ledger.Amount, description string) lending.Facility {
	line := must(p.Lending.OpenRevolvingLine(b.ctx, p.CustomerSubledger, name, seedAsset, limit, rate, interest.ACT365, minPayment))
	borrowerGL := must(p.Deposit.GetAccount(b.ctx, borrower.ID)).GLAccount
	must(p.Lending.Draw(b.ctx, line.ID, borrowerGL, draw, description))
	return must(p.Lending.GetFacility(b.ctx, line.ID))
}

// runDays advances the clock a day at a time, driving RunEndOfDay through each
// one — the same entry point payment.Participant exposes to the API — so the
// seed's accrual and arrears move exactly as a running day would produce them.
func (b *builder) runDays(p *payment.Participant, days int) {
	for i := 0; i < days; i++ {
		b.clock.advance(24 * time.Hour)
		check(p.RunEndOfDay(b.ctx, b.clock.now()))
	}
}

// ref builds a PartyRef for a customer deposit account from the account's own
// IBAN identifier, so the same account always produces an identical PartyRef.
func (b *builder) ref(p *payment.Participant, acct deposit.Account) payment.PartyRef {
	ref := payment.PartyRef{Participant: p.ID, Account: acct.ID}
	for _, ident := range acct.Identifiers {
		if ident.Scheme == deposit.IdentifierIBAN {
			ref.Identifier = ident
			break
		}
	}
	return ref
}

// fund credits a deposit account with cash and raises the bank's reserve.
func (b *builder) fund(p *payment.Participant, acct deposit.Account, amount ledger.Amount) {
	check(b.net.Deposit(b.ctx, p.ID, acct.ID, amount, "Opening deposit"))
}

// initiate runs all three halves of an initiation — the submitting bank's, the
// receiving bank's and the clearing house's — in one unit of work, leaving the
// payment Accepted in its scheme's open cycle.
//
// The seed is one process building a scenario, so it plays every actor; the
// mesh is what makes them separate. Composing the three halves here rather
// than calling one method that does all three is the point of the split: there
// is no such method, precisely so that no caller can validate both ends of a
// payment by accident.
func (b *builder) initiate(req payment.InitiatePaymentRequest) payment.Payment {
	var out payment.Payment
	check(b.net.Store().Update(b.ctx, func(ctx context.Context, tx payment.Tx) error {
		p, err := b.net.SubmitPaymentTx(ctx, tx, req)
		if err != nil {
			return err
		}
		if err := b.net.AcceptInboundTx(ctx, tx, p.ID); err != nil {
			return err
		}
		out, err = b.net.AcceptAtCSMTx(ctx, tx, p.ID)
		return err
	}))
	return out
}

// reject runs both halves of a rejection — the clearing house's transition and
// the payer's bank's reversal of its own leg — in one unit of work, leaving the
// payment Rejected with the payer's money back in their account.
//
// Split for the same reason initiate is: there is no method that plays both
// actors. Sharing the Tx keeps the seed's outcome the one it has always built —
// the whole rejection or none of it — which in the mesh is exactly what the two
// actors do not share. See RejectAtCSMTx on what that opens.
func (b *builder) reject(id payment.PaymentID, code iso20022.StatusReason, reason string) {
	check(b.net.Store().Update(b.ctx, func(ctx context.Context, tx payment.Tx) error {
		rejected, err := b.net.RejectAtCSMTx(ctx, tx, id, code, reason)
		if err != nil {
			return err
		}
		return b.net.ReverseDebtorLegTx(ctx, tx, rejected, reason)
	}))
}

func (b *builder) initSCT(dp *payment.Participant, d deposit.Account, cp *payment.Participant, c deposit.Account, amount ledger.Amount, e2e, desc string) payment.Payment {
	return b.initiate(payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      b.ref(dp, d),
		Creditor:    b.ref(cp, c),
		Amount:      amount,
		EndToEndID:  e2e,
		Description: desc,
	})
}

func (b *builder) initSDD(dp *payment.Participant, d deposit.Account, cp *payment.Participant, c deposit.Account, amount ledger.Amount, mandate payment.MandateID, e2e, desc string) payment.Payment {
	return b.initiate(payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPADD,
		Debtor:      b.ref(dp, d),
		Creditor:    b.ref(cp, c),
		Amount:      amount,
		MandateID:   mandate,
		EndToEndID:  e2e,
		Description: desc,
	})
}

func (b *builder) build() {
	// --- Banks -------------------------------------------------------------
	// Each bank joins the network as a euro bank: AddParticipant registers EUR
	// in its book and in the central bank's, and opens its suspense, reserve
	// and settlement accounts in it.
	euro := []ledger.AssetCode{seedAsset}
	aurora := must(b.net.AddParticipant(b.ctx, "Aurora Bank", "AURODEFFXXX", euro))
	verde := must(b.net.AddParticipant(b.ctx, "Banca Verde", "VERDITMMXXX", euro))
	nord := must(b.net.AddParticipant(b.ctx, "Nordhaven Bank", "NORDSESSXXX", euro))
	soleil := must(b.net.AddParticipant(b.ctx, "Crédit Soleil", "SOLEFRPPXXX", euro))

	// --- Each bank's catalogue ---------------------------------------------
	// Before any account, because every deposit account is opened FROM a
	// product: a floating terms row with no product would have nothing to
	// float to.
	for _, p := range []*payment.Participant{aurora, verde, nord, soleil} {
		b.products(p)
	}

	// --- Customer accounts (each gets a canonical IBAN) --------------------
	alice := b.open(aurora, "Alice Andersson", "SE89-AURORA-1001")
	aaron := b.open(aurora, "Aaron Apstorp", "SE89-AURORA-1002")
	annie := b.open(aurora, "Annie Ahlberg", "SE89-AURORA-1003")      // -> Dormant
	merchant := b.open(aurora, "Aurora Merchant", "SE89-AURORA-1004") // hold-capture counterparty
	oldAcct := b.open(aurora, "Closed Account", "SE89-AURORA-1005")   // -> Closed

	bruno := b.openOverdraft(verde, "Bruno Bianchi", "IT60-VERDE-2001", 50_000) // 500.00 overdraft
	bella := b.open(verde, "Bella Bruno", "IT60-VERDE-2002")
	bianca := b.open(verde, "Bianca Belli", "IT60-VERDE-2003") // -> Frozen

	nora := b.open(nord, "Nora Nilsson", "NO93-NORD-3001")
	niklas := b.open(nord, "Niklas Nyborg", "NO93-NORD-3002")

	chloe := b.open(soleil, "Chloé Caron", "FR76-SOLEIL-4001")
	claude := b.open(soleil, "Claude Clément", "FR76-SOLEIL-4002")

	// --- Funding (also raises each bank's central-bank reserve) ------------
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

	b.clock.advance(2 * time.Hour)

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
	merchantGL := must(aurora.Deposit.GetAccount(ctx, merchant.ID)).GLAccount
	must(aurora.Deposit.CaptureHold(ctx, captured.ID, merchantGL, 0, "Captured: card payment"))

	// --- End-of-day snapshots for Alice across two business days -----------
	must(aurora.Deposit.TakeEndOfDaySnapshot(ctx, alice.ID, b.clock.now()))
	b.clock.advance(24 * time.Hour)
	must(aurora.Deposit.TakeEndOfDaySnapshot(ctx, alice.ID, b.clock.now()))

	// --- Account status lifecycle ------------------------------------------
	check(aurora.Deposit.MarkDormant(ctx, annie.ID)) // Active -> Dormant
	check(aurora.Deposit.Close(ctx, oldAcct.ID))     // zero balance -> Closed
	check(verde.Deposit.Freeze(ctx, bianca.ID))      // Active -> Frozen

	// --- Mandates for SEPA Direct Debit ------------------------------------
	m1 := must(b.net.CreateMandate(b.ctx, b.ref(soleil, chloe), b.ref(nord, nora), 100_000))
	m2 := must(b.net.CreateMandate(b.ctx, b.ref(verde, bruno), b.ref(aurora, aaron), 0))
	m3 := must(b.net.CreateMandate(b.ctx, b.ref(nord, niklas), b.ref(soleil, claude), 25_000))
	check(b.net.RevokeMandate(b.ctx, m3.ID)) // revoked, for display

	b.clock.advance(1 * time.Hour)

	// --- Phase A: a fully settled SEPA Credit Transfer cycle ---------------
	sct1 := must(b.net.OpenCycle(b.ctx, payment.SchemeSEPACT))
	b.initSCT(aurora, alice, nord, niklas, 25_000, "SCT-001", "Rent to N. Nyborg")
	b.initSCT(nord, nora, verde, bella, 40_000, "SCT-002", "Invoice 2025-77")
	b.initSCT(verde, bruno, soleil, chloe, 30_000, "SCT-003", "Consulting fee")
	must(b.net.CloseCycle(b.ctx, sct1.ID))
	must(b.net.SettleCycle(b.ctx, sct1.ID))

	b.clock.advance(24 * time.Hour)

	// --- Phase B: a settled SEPA Direct Debit cycle (one will be returned) --
	sdd1 := must(b.net.OpenCycle(b.ctx, payment.SchemeSEPADD))
	b.initSDD(soleil, chloe, nord, nora, 20_000, m1.ID, "SDD-001", "Utility direct debit")
	returned := b.initSDD(verde, bruno, aurora, aaron, 12_000, m2.ID, "SDD-002", "Gym membership")
	must(b.net.CloseCycle(b.ctx, sdd1.ID))
	must(b.net.SettleCycle(b.ctx, sdd1.ID))

	// --- Phase C: return the settled direct debit (an R-transaction) --------
	must(b.net.ReturnPayment(b.ctx, returned.ID, "Debtor dispute — unauthorised collection"))

	b.clock.advance(24 * time.Hour)

	// --- Phase D: a closed-but-not-settled SCT cycle (payments stay Cleared) -
	sct2 := must(b.net.OpenCycle(b.ctx, payment.SchemeSEPACT))
	b.initSCT(aurora, aaron, soleil, claude, 8_000, "SCT-010", "Book order")
	b.initSCT(verde, bella, nord, niklas, 6_000, "SCT-011", "Shared dinner")
	must(b.net.CloseCycle(b.ctx, sct2.ID))

	// --- Phase E: an open SDD cycle with an accepted and a rejected payment --
	must(b.net.OpenCycle(b.ctx, payment.SchemeSEPADD))
	b.initSDD(soleil, chloe, nord, nora, 5_000, m1.ID, "SDD-010", "Monthly subscription")
	reject := b.initSDD(verde, bruno, aurora, aaron, 3_000, m2.ID, "SDD-011", "Disputed charge")
	// An operator-initiated rejection carries no more specific reason code
	// than MS03 — the same choice the reject handler makes, for the same
	// reason: nothing here is the system detecting a condition of its own.
	//
	// Both halves, in one unit of work, as with initiate: the clearing house
	// transitions the payment and drops it from the cycle, the payer's bank
	// reverses the leg it posted. There is no method that does both, so nobody
	// plays the clearing house and the payer's bank without saying so.
	b.reject(reject.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "Insufficient mandate coverage")

	// --- Phase F: an open SCT cycle with an accepted payment ----------------
	must(b.net.OpenCycle(b.ctx, payment.SchemeSEPACT))
	b.initSCT(aurora, alice, verde, bella, 7_000, "SCT-020", "Birthday gift")

	// --- Lending: credit facilities across the network ---------------------
	b.lendingShowcase(aurora, verde, nord, alice, bruno, bella, niklas)

	// --- General-ledger primitives showcase on Aurora ----------------------
	b.glShowcase(aurora, aaron)
}

// lendingShowcase exercises every state the credit sub-project introduces: a
// priced overdraft, a term loan part-way through its schedule, a revolving
// line with a billed cycle, a delinquent loan, and an overdraft that is
// actually accruing rather than sitting at a limit that costs nothing. This is
// the data the web app's facility pages, its arrears badge, and Bruno's
// deposit page read.
func (b *builder) lendingShowcase(aurora, verde, nord *payment.Participant, alice, bruno, bella, niklas deposit.Account) {
	ctx := b.ctx
	b.clock.advance(1 * time.Hour)

	// --- Bruno's overdraft, priced ------------------------------------------
	// He already has a 500.00 limit (openOverdraft, above); this is what makes
	// drawing on it cost him something rather than nothing: 15% arranged, 35%
	// on anything drawn beyond the limit.
	//
	// It is an OVERLAY rather than a product price, which is what it always
	// meant: a rate negotiated with Bruno, on his own timeline. Verde's product
	// is repriced separately below, and that reprice does not move him — which
	// is the whole point of the distinction and is visible on his account page.
	must(verde.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 150_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		b.clock.now()))
	must(verde.Deposit.SetOverdraftLimit(ctx, bruno.ID, 50_000, b.clock.now()))

	// --- Bella, migrated onto Premium ---------------------------------------
	// Effective a fortnight in. Her earlier days keep pricing at Basic's rate —
	// a migration is a forward-dated row, not a rewrite — which is what the
	// deposit page's terms history shows.
	//
	// The seeded data now holds all three cases side by side: floating through
	// the day-30 reprice (everyone else), negotiated and therefore unmoved by
	// it (Bruno), and migrated (Bella).
	must(verde.Deposit.ChangeProduct(ctx, bella.ID, b.cats[verde.ID].premium,
		b.clock.now().AddDate(0, 0, 14)))

	// --- A term loan part-way through its schedule (Alice, Aurora) ----------
	// EUR 10,000, five years, 6%, annuity. Disbursed, then run day by day
	// through two monthly instalments paid on time, then a little further so a
	// fresh accrual is visible without reaching the third instalment. The
	// result: accrued interest, a partly-paid schedule, Current arrears.
	t1 := b.clock.now()
	firstDue := t1.AddDate(0, 1, 0)
	loan := b.openLoan(aurora, alice, "Alice Home Loan", 1_000_000, 60_000, 60, firstDue, "Home loan payout")
	aliceGL := must(aurora.Deposit.GetAccount(ctx, alice.ID)).GLAccount

	b.runDays(aurora, int(firstDue.Sub(t1)/(24*time.Hour)))
	sched := must(aurora.Lending.Schedule(ctx, loan.ID))
	must(aurora.Lending.Repay(ctx, loan.ID, aliceGL, sched[0].Total(), b.clock.now(), "Instalment 1"))

	secondDue := t1.AddDate(0, 2, 0)
	b.runDays(aurora, int(secondDue.Sub(firstDue)/(24*time.Hour)))
	sched = must(aurora.Lending.Schedule(ctx, loan.ID))
	must(aurora.Lending.Repay(ctx, loan.ID, aliceGL, sched[1].Total(), b.clock.now(), "Instalment 2"))

	b.runDays(aurora, 10) // a fresh accrual builds up; the third instalment is not yet due

	// --- A delinquent loan (Niklas, Nordhaven) -------------------------------
	// A smaller loan than Alice's, disbursed and then left unpaid past two due
	// dates: one month plus twenty days past the first instalment, comfortably
	// inside the 30-59 bucket however the calendar months involved fall.
	b.clock.advance(1 * time.Hour)
	t3 := b.clock.now()
	niklasFirstDue := t3.AddDate(0, 1, 0)
	b.openLoan(nord, niklas, "Niklas Car Loan", 300_000, 90_000, 24, niklasFirstDue, "Car loan payout")
	target := t3.AddDate(0, 2, 20)
	b.runDays(nord, int(target.Sub(t3)/(24*time.Hour)))

	// --- Bruno, pushed into overdraft and accruing --------------------------
	// A card settlement pushes him into his priced overdraft. His accrual
	// window opens at ACCOUNT OPENING — the opening terms row every account
	// gets — and every end-of-day re-derives every day since, each at the
	// terms that were in force on it. Nothing has run Verde's end-of-day yet
	// (Niklas's story runs on Nordhaven's book, not Verde's), so this call
	// recomputes his whole life to date, and all of it accrues zero: the days
	// before SetOverdraftTerms ran carry the opening row's zero rate, and the
	// days between that and the SCT below recompute against a drawn balance of
	// zero, because he isn't overdrawn yet. The SCT
	// overdraws him immediately: its debtor leg posts in the debtor bank's half of
	// submission, so
	// the balance moves right away without its clearing cycle needing to
	// close or settle. Then 45 days pass and interest accrues, a charge
	// capitalizes it, a backdated repricing lands (see below), and 15 more
	// days build a fresh accrual on top of both.
	//
	// That is not the figure this phase ends on, though. RunEndOfDay drives a
	// participant's whole book, not one facility, and Bella's line below
	// shares this same book (Verde). Her own runDays(verde, 30) keeps Bruno's
	// overdraft accruing for another 30 days after this phase returns, so the
	// accrued interest the seed actually produces reflects 45 days
	// post-capitalization, all at the 18% the repricing below has already put
	// in force by then, not the 15 this phase runs on its own. The repricing
	// also reaches backwards: twenty-ONE of the days that already elapsed
	// before the charge — the spans ending 2026-03-14 through 2026-04-03
	// inclusive — accrued at 15% when they ran and are repriced to 18% by the
	// very next end-of-day. That is twenty-one, not the twenty-day gap it
	// looks like, because a span is named by its END date: the span ending on
	// the effective day itself is already priced at the new rate. Bruno's
	// final figure blends both rates over both balances it accrued on — EUR
	// 200.00 before the charge, EUR 203.78 after — for a total ≈ EUR 4.87, not
	// the single-rate ≈ EUR 3.77 a pure-15% run would give. Changing Bella's
	// 30-day span, or the repricing's twenty-day offset, changes Bruno's
	// final number.
	//
	// It joins the SCT cycle Phase F left open (only one may be open per
	// scheme at a time) rather than opening a second one, which is also why it
	// stays Accepted like SCT-020 rather than Settled.
	b.clock.advance(1 * time.Hour)
	check(verde.RunEndOfDay(ctx, b.clock.now()))

	brunoBalance := must(verde.Deposit.GetBalance(ctx, bruno.ID))
	overdrawBy := ledger.Amount(20_000) // EUR 200 into the EUR 500 arranged limit
	b.initSCT(verde, bruno, aurora, alice, brunoBalance.Book+overdrawBy, "SCT-030", "Card settlement")

	b.runDays(verde, 45)
	must(verde.Deposit.ChargeOverdraftInterest(ctx, bruno.ID, b.clock.now()))

	// --- Bruno, repriced mid-life -------------------------------------------
	// The arranged rate moves from 15% to 18%, effective TWENTY DAYS AGO — a
	// repricing agreed on one date and entered on another, which is the
	// ordinary case and the one mutable terms could not represent at all.
	//
	// This is the seed's demonstration of the whole change. The next end-of-day
	// re-derives twenty-two days at 18% — the spans ending 2026-03-14 through
	// 2026-04-04 — twenty-one of them previously charged at 15% and the
	// twenty-second (the span ending on the new day itself) never priced
	// before, and posts the difference as ordinary delta interest. Nothing is
	// rewritten and all three terms rows — the zero-rate opening row, 15%, and
	// 18% — stay on the timeline, which the account page now renders.
	//
	// Before terms were effective-dated this could not happen: the recompute
	// window would have been reset to today and the twenty-one days behind it
	// would have kept the old rate forever.
	must(verde.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 180_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		b.clock.now().AddDate(0, 0, -20)))

	b.runDays(verde, 15)

	// --- A revolving line, partly drawn and billed (Bella, Verde) -----------
	// EUR 2,500 limit, 18%, 2% minimum payment. Drawn EUR 1,000, accrued for a
	// month, then charged: the accrued interest capitalizes into principal and
	// the cycle's minimum payment is billed. This runs last among Verde's
	// stories, and nothing runs after it, so the billed cycle — due a month
	// out — stays Current rather than aging past due the way it would if it
	// ran before Bruno's own accrual instead of after.
	//
	// Its own 30-day accrual lands on that same book, though: RunEndOfDay
	// drives a participant's whole book, not one facility, so this call also
	// carries Bruno's overdraft forward another 30 days — which is where the
	// 45 days behind his final figure above come from, not the 15 his own
	// phase runs.
	b.clock.advance(1 * time.Hour)
	line := b.openLine(verde, bella, "Bella Card Line", 250_000, 180_000, 20_000, 100_000, "Card line draw")
	b.runDays(verde, 30)
	must(verde.Lending.ChargeInterest(ctx, line.ID, b.clock.now()))
}

// glShowcase exercises the raw general-ledger primitives on one bank so that all
// five account types (Asset, Liability, Equity, Revenue, Expense) and a manual
// transaction + reversal appear in the data. Liability is already present via the
// bank's customer-deposit and suspense GL accounts; this adds the other four.
func (b *builder) glShowcase(p *payment.Participant, customer deposit.Account) {
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
	customerGL := must(p.Deposit.GetAccount(ctx, customer.ID)).GLAccount
	fee := must(p.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Monthly account fee",
		Entries: []ledger.Entry{
			{AccountID: customerGL, Amount: 500, Direction: ledger.Debit},
			{AccountID: feeIncome.ID, Amount: 500, Direction: ledger.Credit},
		},
	}))

	// Reverse the fee (goodwill waiver) to demonstrate a reversal.
	must(p.Ledger.ReverseTransaction(ctx, fee.ID, "Fee waived — goodwill"))
}
