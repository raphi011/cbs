package seed

import (
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// baseDate anchors the deterministic seed timeline. Everything built before the
// clock goes live is dated relative to this instant.
var baseDate = time.Date(2025, 9, 15, 9, 0, 0, 0, time.UTC)

// Network builds a fresh payment.Network populated with the full sample scenario
// (see the package doc) on a frozen, deterministic clock, then switches the
// clock to real time before returning so later operations are timestamped live.
func Network() *payment.Network {
	c := newClock(baseDate)
	net := payment.NewNetworkWithClock(c.now)
	b := &builder{net: net, clock: c, ibans: map[deposit.AccountID]string{}}
	b.build()
	c.goLive()
	return net
}

type builder struct {
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
func must[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("seed: %v", err))
	}
	return v
}

// check panics on a non-nil error from a call that returns only an error.
func check(err error) {
	if err != nil {
		panic(fmt.Sprintf("seed: %v", err))
	}
}

// open opens a customer account, records its canonical IBAN, and returns it.
func (b *builder) open(p *payment.Participant, name, iban string) deposit.Account {
	a := must(p.OpenCustomerAccount(name))
	b.ibans[a.ID] = iban
	return a
}

// openOverdraft opens a customer account with an overdraft limit (the
// participant helper only opens with zero overdraft) and records its IBAN.
func (b *builder) openOverdraft(p *payment.Participant, name, iban string, limit ledger.Amount) deposit.Account {
	a := must(p.Deposit.OpenAccount(p.CustomerSubledger, name, limit))
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
	check(b.net.Deposit(p.ID, acct.ID, amount, "Opening deposit"))
}

func (b *builder) initSCT(dp *payment.Participant, d deposit.Account, cp *payment.Participant, c deposit.Account, amount ledger.Amount, e2e, desc string) payment.Payment {
	return must(b.net.InitiatePayment(payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      b.ref(dp, d),
		Creditor:    b.ref(cp, c),
		Amount:      amount,
		EndToEndID:  e2e,
		Description: desc,
	}))
}

func (b *builder) initSDD(dp *payment.Participant, d deposit.Account, cp *payment.Participant, c deposit.Account, amount ledger.Amount, mandate payment.MandateID, e2e, desc string) payment.Payment {
	return must(b.net.InitiatePayment(payment.InitiatePaymentRequest{
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
	aurora := must(b.net.AddParticipant("Aurora Bank"))
	verde := must(b.net.AddParticipant("Banca Verde"))
	nord := must(b.net.AddParticipant("Nordhaven Bank"))
	soleil := must(b.net.AddParticipant("Crédit Soleil"))

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
	must(aurora.Deposit.CreateHold(deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 10_000, Description: "Card pre-authorisation (hotel)",
	}))
	released := must(aurora.Deposit.CreateHold(deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 5_000, Description: "Cancelled online order",
	}))
	check(aurora.Deposit.ReleaseHold(released.ID))
	captured := must(aurora.Deposit.CreateHold(deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 15_000, Description: "Card payment at Aurora Merchant",
	}))
	merchantGL := must(aurora.Deposit.GetAccount(merchant.ID)).GLAccount
	must(aurora.Deposit.CaptureHold(captured.ID, merchantGL, 0, "Captured: card payment"))

	// --- End-of-day snapshots for Alice across two business days -----------
	must(aurora.Deposit.TakeEndOfDaySnapshot(alice.ID, b.clock.now()))
	b.clock.advance(24 * time.Hour)
	must(aurora.Deposit.TakeEndOfDaySnapshot(alice.ID, b.clock.now()))

	// --- Account status lifecycle ------------------------------------------
	check(aurora.Deposit.MarkDormant(annie.ID)) // Active -> Dormant
	check(aurora.Deposit.Close(oldAcct.ID))     // zero balance -> Closed
	check(verde.Deposit.Freeze(bianca.ID))      // Active -> Frozen

	// --- Mandates for SEPA Direct Debit ------------------------------------
	m1 := must(b.net.CreateMandate(b.ref(soleil, chloe), b.ref(nord, nora), 100_000))
	m2 := must(b.net.CreateMandate(b.ref(verde, bruno), b.ref(aurora, aaron), 0))
	m3 := must(b.net.CreateMandate(b.ref(nord, niklas), b.ref(soleil, claude), 25_000))
	check(b.net.RevokeMandate(m3.ID)) // revoked, for display

	b.clock.advance(1 * time.Hour)

	// --- Phase A: a fully settled SEPA Credit Transfer cycle ---------------
	sct1 := must(b.net.OpenCycle(payment.SchemeSEPACT))
	b.initSCT(aurora, alice, nord, niklas, 25_000, "SCT-001", "Rent to N. Nyborg")
	b.initSCT(nord, nora, verde, bella, 40_000, "SCT-002", "Invoice 2025-77")
	b.initSCT(verde, bruno, soleil, chloe, 30_000, "SCT-003", "Consulting fee")
	must(b.net.CloseCycle(sct1.ID))
	must(b.net.SettleCycle(sct1.ID))

	b.clock.advance(24 * time.Hour)

	// --- Phase B: a settled SEPA Direct Debit cycle (one will be returned) --
	sdd1 := must(b.net.OpenCycle(payment.SchemeSEPADD))
	b.initSDD(soleil, chloe, nord, nora, 20_000, m1.ID, "SDD-001", "Utility direct debit")
	returned := b.initSDD(verde, bruno, aurora, aaron, 12_000, m2.ID, "SDD-002", "Gym membership")
	must(b.net.CloseCycle(sdd1.ID))
	must(b.net.SettleCycle(sdd1.ID))

	// --- Phase C: return the settled direct debit (an R-transaction) --------
	must(b.net.ReturnPayment(returned.ID, "Debtor dispute — unauthorised collection"))

	b.clock.advance(24 * time.Hour)

	// --- Phase D: a closed-but-not-settled SCT cycle (payments stay Cleared) -
	sct2 := must(b.net.OpenCycle(payment.SchemeSEPACT))
	b.initSCT(aurora, aaron, soleil, claude, 8_000, "SCT-010", "Book order")
	b.initSCT(verde, bella, nord, niklas, 6_000, "SCT-011", "Shared dinner")
	must(b.net.CloseCycle(sct2.ID))

	// --- Phase E: an open SDD cycle with an accepted and a rejected payment --
	must(b.net.OpenCycle(payment.SchemeSEPADD))
	b.initSDD(soleil, chloe, nord, nora, 5_000, m1.ID, "SDD-010", "Monthly subscription")
	reject := b.initSDD(verde, bruno, aurora, aaron, 3_000, m2.ID, "SDD-011", "Disputed charge")
	must(b.net.RejectPayment(reject.ID, "Insufficient mandate coverage"))

	// --- Phase F: an open SCT cycle with an accepted payment ----------------
	must(b.net.OpenCycle(payment.SchemeSEPACT))
	b.initSCT(aurora, alice, verde, bella, 7_000, "SCT-020", "Birthday gift")

	// --- General-ledger primitives showcase on Aurora ----------------------
	b.glShowcase(aurora, aaron)
}

// glShowcase exercises the raw general-ledger primitives on one bank so that all
// five account types (Asset, Liability, Equity, Revenue, Expense) and a manual
// transaction + reversal appear in the data. Liability is already present via the
// bank's customer-deposit and suspense GL accounts; this adds the other four.
func (b *builder) glShowcase(p *payment.Participant, customer deposit.Account) {
	glID := must(p.Ledger.GetSubledger(p.CustomerSubledger)).LedgerID

	equitySub := must(p.Ledger.CreateSubledger(glID, "Equity"))
	shareCapital := must(p.Ledger.CreateAccount(equitySub.ID, "Share Capital", ledger.Equity))
	treasurySub := must(p.Ledger.CreateSubledger(glID, "Treasury"))
	vault := must(p.Ledger.CreateAccount(treasurySub.ID, "Vault Cash", ledger.Asset))
	incomeSub := must(p.Ledger.CreateSubledger(glID, "Income"))
	feeIncome := must(p.Ledger.CreateAccount(incomeSub.ID, "Fee Income", ledger.Revenue))
	expenseSub := must(p.Ledger.CreateSubledger(glID, "Expenses"))
	opex := must(p.Ledger.CreateAccount(expenseSub.ID, "Operating Expenses", ledger.Expense))

	// Capitalisation: Vault Cash (asset) up, Share Capital (equity) up.
	must(p.Ledger.PostTransaction(ledger.PostTransactionRequest{
		Description: "Initial share capital",
		Entries: []ledger.Entry{
			{AccountID: vault.ID, Amount: 100_000, Direction: ledger.Debit},
			{AccountID: shareCapital.ID, Amount: 100_000, Direction: ledger.Credit},
		},
	}))

	// Operating expense: Operating Expenses (expense) up, Vault Cash (asset) down.
	must(p.Ledger.PostTransaction(ledger.PostTransactionRequest{
		Description: "Office rent",
		Entries: []ledger.Entry{
			{AccountID: opex.ID, Amount: 2_000, Direction: ledger.Debit},
			{AccountID: vault.ID, Amount: 2_000, Direction: ledger.Credit},
		},
	}))

	// Monthly account fee: customer deposit (liability) down, Fee Income (revenue) up.
	customerGL := must(p.Deposit.GetAccount(customer.ID)).GLAccount
	fee := must(p.Ledger.PostTransaction(ledger.PostTransactionRequest{
		Description: "Monthly account fee",
		Entries: []ledger.Entry{
			{AccountID: customerGL, Amount: 500, Direction: ledger.Debit},
			{AccountID: feeIncome.ID, Amount: 500, Direction: ledger.Credit},
		},
	}))

	// Reverse the fee (goodwill waiver) to demonstrate a reversal.
	must(p.Ledger.ReverseTransaction(fee.ID, "Fee waived — goodwill"))
}
