package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
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
	b := &builder{ctx: ctx, net: net, clock: d.clock, ibans: map[deposit.AccountID]string{}}
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
	// ibans assigns one canonical IBAN per deposit account so a PartyRef for an
	// account is identical wherever it appears. The SDD scheme matches a payment
	// to its mandate by PartyRef equality, and PartyRef includes the IBAN field —
	// so a mandate and its direct debits must reference the account the same way.
	ibans map[deposit.AccountID]string
}

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

// open opens a customer account, records its canonical IBAN, and returns it.
func (b *builder) open(p *payment.Participant, name, iban string) deposit.Account {
	a := must(p.OpenCustomerAccount(b.ctx, name))
	b.ibans[a.ID] = iban
	return a
}

// openOverdraft opens a customer account with an overdraft limit (the
// participant helper only opens with zero overdraft) and records its IBAN.
func (b *builder) openOverdraft(p *payment.Participant, name, iban string, limit ledger.Amount) deposit.Account {
	a := must(p.Deposit.OpenAccount(b.ctx, p.CustomerSubledger, name, limit))
	b.ibans[a.ID] = iban
	return a
}

// ref builds a PartyRef for a customer deposit account using its canonical IBAN,
// so the same account always produces an identical PartyRef.
func (b *builder) ref(p *payment.Participant, acct deposit.Account) payment.PartyRef {
	return payment.PartyRef{Participant: p.ID, Account: acct.ID, IBAN: b.ibans[acct.ID]}
}

// fund credits a deposit account with cash and raises the bank's reserve.
func (b *builder) fund(p *payment.Participant, acct deposit.Account, amount ledger.Amount) {
	check(b.net.Deposit(b.ctx, p.ID, acct.ID, amount, "Opening deposit"))
}

func (b *builder) initSCT(dp *payment.Participant, d deposit.Account, cp *payment.Participant, c deposit.Account, amount ledger.Amount, e2e, desc string) payment.Payment {
	return must(b.net.InitiatePayment(b.ctx, payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      b.ref(dp, d),
		Creditor:    b.ref(cp, c),
		Amount:      amount,
		EndToEndID:  e2e,
		Description: desc,
	}))
}

func (b *builder) initSDD(dp *payment.Participant, d deposit.Account, cp *payment.Participant, c deposit.Account, amount ledger.Amount, mandate payment.MandateID, e2e, desc string) payment.Payment {
	return must(b.net.InitiatePayment(b.ctx, payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPADD,
		Debtor:      b.ref(dp, d),
		Creditor:    b.ref(cp, c),
		Amount:      amount,
		MandateID:   mandate,
		EndToEndID:  e2e,
		Description: desc,
	}))
}

func (b *builder) build() {
	// --- Banks -------------------------------------------------------------
	aurora := must(b.net.AddParticipant(b.ctx, "Aurora Bank"))
	verde := must(b.net.AddParticipant(b.ctx, "Banca Verde"))
	nord := must(b.net.AddParticipant(b.ctx, "Nordhaven Bank"))
	soleil := must(b.net.AddParticipant(b.ctx, "Crédit Soleil"))

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
	must(b.net.RejectPayment(b.ctx, reject.ID, "Insufficient mandate coverage"))

	// --- Phase F: an open SCT cycle with an accepted payment ----------------
	must(b.net.OpenCycle(b.ctx, payment.SchemeSEPACT))
	b.initSCT(aurora, alice, verde, bella, 7_000, "SCT-020", "Birthday gift")

	// --- General-ledger primitives showcase on Aurora ----------------------
	b.glShowcase(aurora, aaron)
}

// glShowcase exercises the raw general-ledger primitives on one bank so that all
// five account types (Asset, Liability, Equity, Revenue, Expense) and a manual
// transaction + reversal appear in the data. Liability is already present via the
// bank's customer-deposit and suspense GL accounts; this adds the other four.
func (b *builder) glShowcase(p *payment.Participant, customer deposit.Account) {
	ctx := b.ctx
	glID := must(p.Ledger.GetSubledger(ctx, p.CustomerSubledger)).LedgerID

	equitySub := must(p.Ledger.CreateSubledger(ctx, glID, "Equity"))
	shareCapital := must(p.Ledger.CreateAccount(ctx, equitySub.ID, "Share Capital", ledger.Equity))
	treasurySub := must(p.Ledger.CreateSubledger(ctx, glID, "Treasury"))
	vault := must(p.Ledger.CreateAccount(ctx, treasurySub.ID, "Vault Cash", ledger.Asset))
	incomeSub := must(p.Ledger.CreateSubledger(ctx, glID, "Income"))
	feeIncome := must(p.Ledger.CreateAccount(ctx, incomeSub.ID, "Fee Income", ledger.Revenue))
	expenseSub := must(p.Ledger.CreateSubledger(ctx, glID, "Expenses"))
	opex := must(p.Ledger.CreateAccount(ctx, expenseSub.ID, "Operating Expenses", ledger.Expense))

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
