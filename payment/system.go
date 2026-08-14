package payment

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/internal/unit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// Network is one institution's handle on the payment network: initiation,
// clearing (netting), and settlement.
type Network struct {
	clock func() time.Time

	// id is which institution this network acts as, and it answers every
	// "whose book?" below. Networks mints one Network per entity.
	id Identity

	// common is this institution's database at the width EVERY institution has:
	// the audit trail and the id counters. The store an institution ACTS through
	// is on that institution's own type, because the acts differ.
	common CommonStore

	// messages is the same database at the width of the message log, which is
	// every institution's too. See MessageLogStore.
	messages MessageLogStore

	// schemes is the only thing a Network holds in memory, and it is SHARED by
	// every network one Networks mints. See schemeRegistry.
	schemes *schemeRegistry
}

// centralBankBook is the settlement agent's book of accounts, or a refusal.
func (s *CentralBankNetwork) centralBankBook() (*ledger.Book, error) {
	if s.centralBank == nil {
		return nil, fmt.Errorf("%w: this is %s, and the central bank's book of accounts is the settlement agent's alone",
			ErrNotThisInstitutionsAct, s.id)
	}
	return s.centralBank, nil
}

// self is the member bank this network acts as, or a refusal.
func (s *Network) self() (ParticipantID, error) {
	pid, ok := s.id.Participant()
	if !ok {
		return "", fmt.Errorf("%w: this is %s, and this act is a member bank's own",
			ErrNotThisInstitutionsAct, s.id)
	}
	return pid, nil
}

// selfBIC is the ADDRESS this member bank is reached at: self, as the
// identifier a message carries.
func (s *BankNetwork) selfBIC() (iso20022.BIC, error) {
	pid, err := s.self()
	if err != nil {
		return "", err
	}
	return iso20022.BIC(pid), nil
}

// clearingHouse refuses every institution that is not the clearing house.
func (s *Network) clearingHouse() error {
	if s.id.role != roleClearingHouse {
		return fmt.Errorf("%w: this is %s, and carrying another institution's payment is the clearing house's own act",
			ErrNotThisInstitutionsAct, s.id)
	}
	return nil
}

// CentralBankBook is the BookID of the central bank's own book of accounts, and
// also the book its own rows are keyed and sequenced under. See Network.book.
const CentralBankBook ledger.BookID = "central-bank"

// ClearingHouseBook is the BookID the clearing house's own rows are keyed and
// sequenced under.
const ClearingHouseBook ledger.BookID = "clearing-house"

// book is the BookID this institution's own rows are keyed and sequenced under.
func (s *Network) book() ledger.BookID {
	switch s.id.role {
	case roleBank:
		// A bank IS its own book, and that id is its BIC — so this is also its
		// address and the name of its database. See FoundBankTx.
		return ledger.BookID(s.id.pid)
	case roleClearingHouse:
		return ClearingHouseBook
	default:
		return CentralBankBook
	}
}

// The central bank's chart of accounts is identified by name rather than by a
// cached ID, because a cached ID does not survive Store.Reset and is wrong the
// moment a second process opens the same database.
const (
	cbLedgerName   = "Central Bank"
	cbReservesName = "Member Reserves"
	cbCapitalName  = "Central Bank Capital"
	cbAssetsName   = "Settlement Assets"
)

// cbAssetsAccountName is the balancing settlement-asset account's name in one
// asset. The asset is part of the name because there is one such account per
// asset and two rows both called "Settlement Assets" are unreadable.
func cbAssetsAccountName(asset ledger.AssetCode) string {
	return cbAssetsName + " (" + string(asset) + ")"
}

// newNetwork is the core every institution's handle embeds. The scheme registry
// is supplied so that Networks can hand one registry to every institution it
// mints; see institutions.go for the three assemblers that call this.
func newNetwork(common CommonStore, messages MessageLogStore, clock func() time.Time, id Identity, schemes *schemeRegistry) core {
	if id.role == roleUnset {
		panic("payment: a network needs an identity; a network belonging to no institution has no answer to whose book an act is about")
	}
	s := core{clock: clock, id: id, common: common, messages: messages, schemes: schemes}
	s.RegisterScheme(SCT{})
	s.RegisterScheme(SDD{})
	return s
}

// Identity is which institution this network acts as. See the type.
func (s *Network) Identity() Identity { return s.id }

// Now is the instant this network reads for everything it stamps: booking
// dates, value dates, audit timestamps.
func (s *Network) Now() time.Time { return s.clock() }

// now is Now, for this package's own use.
func (s *Network) now() time.Time { return s.Now() }

// Store returns the store every layer of this institution shares. One method per
// institution, each returning that institution's own width, because there is no
// store type all three have in common past the audit trail.
func (s *BankNetwork) Store() BankStore                   { return s.store }
func (s *ClearingHouseNetwork) Store() ClearingHouseStore { return s.store }
func (s *CentralBankNetwork) Store() CentralBankStore     { return s.store }

// RegisterScheme adds (or replaces) a scheme. The orchestration below is
// scheme-agnostic; supporting instant or card payments is a matter of
// registering a type that implements Scheme.
func (s *Network) RegisterScheme(sc Scheme) {
	s.schemes.mu.Lock()
	defer s.schemes.mu.Unlock()
	s.schemes.m[sc.ID()] = sc
}

// Scheme looks up a registered scheme, for callers outside this package that
// have to ask the scheme a question before they can act on a payment — which
// bank submits it, for one (see api's bank submit handler).
func (s *Network) Scheme(id SchemeID) (Scheme, bool) { return s.scheme(id) }

// scheme looks up a registered scheme.
func (s *Network) scheme(id SchemeID) (Scheme, bool) {
	s.schemes.mu.RLock()
	defer s.schemes.mu.RUnlock()
	sc, ok := s.schemes.m[id]
	return sc, ok
}

// CentralBank exposes the central-bank ledger for inspection (balances, audit
// trail). Treat it as read-only.
func (s *CentralBankNetwork) CentralBank() (*ledger.Book, error) { return s.centralBankBook() }

// bind attaches the live handles a Bank record needs: its own book of accounts,
// the deposit register, lending portfolio and catalogue over it, all scoped to
// the record's BookID.
func (s *BankNetwork) bind(p Bank) *Bank {
	p.Ledger = ledger.NewBook(s.ledgers, p.BookID, s.clock)
	p.Deposit = deposit.NewRegister(s.deposits, p.Ledger, p.BookID, s.clock, p.Issuer, p.CustomerSubledger)
	p.Lending = lending.NewPortfolio(s.lendings, p.Ledger, p.BookID, s.clock, p.CustomerSubledger)
	p.Catalogue = product.NewCatalogue(s.products, p.Ledger, p.BookID, s.clock)
	p.store = s.store
	return &p
}

// bankTx loads a participant and binds its live handles.
func (s *BankNetwork) bankTx(ctx context.Context, tx BankTx, id ParticipantID) (*Bank, error) {
	rec, err := tx.GetBank(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.bind(rec), nil
}

// selfBankTx loads THIS bank's own row and binds its live handles.
func (s *BankNetwork) selfBankTx(ctx context.Context, tx BankTx) (*Bank, error) {
	self, err := s.self()
	if err != nil {
		return nil, err
	}
	return s.bankTx(ctx, tx, self)
}

// centralBankChartTx returns the central bank's reserve and capital subledgers,
// creating the chart of accounts on first use.
func (s *CentralBankNetwork) centralBankChartTx(ctx context.Context, tx CentralBankTx) (ledger.SubledgerID, ledger.SubledgerID, error) {
	book, err := s.centralBankBook()
	if err != nil {
		return "", "", err
	}
	ledgers, err := tx.ListLedgers(ctx, CentralBankBook)
	if err != nil {
		return "", "", err
	}
	var cb ledger.Ledger
	for _, l := range ledgers {
		if l.Name == cbLedgerName {
			cb = l
			break
		}
	}
	if cb.ID == "" {
		if cb, err = book.CreateLedgerTx(ctx, tx, cbLedgerName); err != nil {
			return "", "", err
		}
	}

	subledgers, err := tx.ListSubledgers(ctx, CentralBankBook)
	if err != nil {
		return "", "", err
	}
	var reserves, capital ledger.Subledger
	for _, sl := range subledgers {
		switch sl.Name {
		case cbReservesName:
			reserves = sl
		case cbCapitalName:
			capital = sl
		}
	}
	// Created in this order on a fresh store so the chart-of-accounts blocks
	// come out as they always have: Member Reserves 100, Capital 200.
	if reserves.ID == "" {
		if reserves, err = book.CreateSubledgerTx(ctx, tx, cb.ID, cbReservesName); err != nil {
			return "", "", err
		}
	}
	if capital.ID == "" {
		if capital, err = book.CreateSubledgerTx(ctx, tx, cb.ID, cbCapitalName); err != nil {
			return "", "", err
		}
	}
	return reserves.ID, capital.ID, nil
}

// centralBankAssetsAccountTx returns the central bank's balancing
// settlement-asset account for one asset, creating it if it does not exist.
func (s *CentralBankNetwork) centralBankAssetsAccountTx(ctx context.Context, tx CentralBankTx, asset ledger.AssetCode) (ledger.AccountID, error) {
	book, err := s.centralBankBook()
	if err != nil {
		return "", err
	}
	_, capital, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return "", err
	}
	accounts, err := tx.ListAccounts(ctx, CentralBankBook)
	if err != nil {
		return "", err
	}
	for _, a := range accounts {
		if a.SubledgerID == capital && a.Name == cbAssetsAccountName(asset) && a.Asset == asset {
			return a.ID, nil
		}
	}
	created, err := book.CreateAccountTx(ctx, tx, capital, cbAssetsAccountName(asset), ledger.Asset, asset)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// settlementAccountTx is the settlement agent's own answer to "which account do
// I hold for this member, in this asset" — read from its own SettlementMember
// row and from no bank's.
func (s *CentralBankNetwork) settlementAccountTx(ctx context.Context, tx CentralBankTx, bic iso20022.BIC, asset ledger.AssetCode) (ledger.AccountID, error) {
	member, err := tx.GetSettlementMember(ctx, bic)
	if err != nil {
		return "", err
	}
	account, ok := member.Accounts[asset]
	if !ok {
		return "", fmt.Errorf("%w: the central bank holds no %s account for %s", ErrParticipantAssetNotFound, asset, bic)
	}
	return account, nil
}

// ---------------------------------------------------------------------------
// Admission

// AdmissionRequest is a bank asking an account servicer to open it an account:
// who is asking, in which asset, and which admission the question belongs to.
type AdmissionRequest struct {
	Name string
	BIC  iso20022.BIC
	// Country is which national registry the applicant is applying to for a bank
	// code, and the whole of what a request says about the allocation it wants.
	// The applicant proposes NO code: a code is the registry's to give.
	Country iban.Country

	Asset ledger.AssetCode
	Ref   string
}

// AdmissionAcknowledgement is an account servicer answering: these are the
// accounts I hold for you.
type AdmissionAcknowledgement struct {
	BIC iso20022.BIC
	// Issuer is the bank code the registry allocated and the country it came out
	// of, and this acknowledgement is the ONLY place either value ever travels.
	Issuer iban.Issuer

	Accounts map[ledger.AssetCode]ledger.AccountID
	Ref      string
}

// LodgementInstruction is a member bank asking its central bank to move cash
// onto the bank's own reserve account.
type LodgementInstruction struct {
	BIC     iso20022.BIC
	Agent   iso20022.BIC
	Account ledger.AccountID
	Asset   ledger.AssetCode
	Amount  ledger.Amount
	Ref     string
}

// LodgementReceipt is the central bank's answer to a LodgementInstruction: it
// credited the reserve, or it would not.
type LodgementReceipt struct {
	Ref    string
	Status iso20022.TransactionStatus
	Reason string
}

// Accepted reports whether the central bank credited the reserve. A method rather
// than a comparison at each call site, the two callers that branch on it being in
// different packages. See RequestHandling on why ACSC is reused here.
func (r LodgementReceipt) Accepted() bool {
	return r.Status == iso20022.TransactionStatusSettlementCompleted
}

// joiningAssets applies the joining default: a bank that names no assets joins
// with the euro.
func joiningAssets(assets []ledger.AssetCode) []ledger.AssetCode {
	if len(assets) == 0 {
		return []ledger.AssetCode{"EUR"}
	}
	return assets
}

// admissionSequenceTx takes the network's identity counter before an admission
// act reads the row it decides from.
func (s *Network) admissionSequenceTx(ctx context.Context, tx ledger.CommonTx) error {
	_, err := tx.NextID(ctx, s.book(), "adm")
	return err
}

// The four acts, each in its own unit of work.

// FoundBank is FoundBankTx in its own unit of work: the bank's own act, and the
// only one of the four its own operator drives directly.
func (s *BankNetwork) FoundBank(ctx context.Context, name string, bic iso20022.BIC, country iban.Country, assets []ledger.AssetCode) (*Bank, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (*Bank, error) {
		return s.FoundBankTx(ctx, tx, name, bic, country, assets)
	})
}

// OpenSettlementAccount is OpenSettlementAccountTx in its own unit of work: the
// settlement agent's act, on a request from the applicant.
func (s *CentralBankNetwork) OpenSettlementAccount(ctx context.Context, in AdmissionRequest) (SettlementMember, iban.Issuer, error) {
	return unit.Run2(ctx, s.store.Update, func(ctx context.Context, tx CentralBankTx) (SettlementMember, iban.Issuer, error) {
		return s.OpenSettlementAccountTx(ctx, tx, in)
	})
}

// AdmitMember is AdmitMemberTx in its own unit of work: the clearing house's
// act, on the settlement agent's acknowledgement.
func (s *ClearingHouseNetwork) AdmitMember(ctx context.Context, in AdmissionAcknowledgement) (RosterEntry, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CsmTx) (RosterEntry, error) {
		return s.AdmitMemberTx(ctx, tx, in)
	})
}

// RecordMembership is RecordMembershipTx in its own unit of work: the bank's
// second act, on the same acknowledgement the clearing house admitted it from.
func (s *BankNetwork) RecordMembership(ctx context.Context, in AdmissionAcknowledgement) (*Bank, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (*Bank, error) {
		return s.RecordMembershipTx(ctx, tx, in)
	})
}

// FoundBankTx is the bank's own act: a bank with a licence building itself.
func (s *BankNetwork) FoundBankTx(ctx context.Context, tx BankTx, name string, bic iso20022.BIC, country iban.Country, assets []ledger.AssetCode) (*Bank, error) {
	if err := ledger.ValidateText("name", name); err != nil {
		return nil, err
	}
	// Validated at founding rather than at first use: a bank with a malformed BIC is
	// one no file can be addressed to and one the other two institutions cannot key
	// their rows by, the BIC being the only identifier that crosses between them.
	if err := bic.Validate(); err != nil {
		return nil, fmt.Errorf("bic: %w", err)
	}
	// The market this bank means to operate in, and NOT a bank code: a code is a
	// national registry's to allocate and arrives on the acknowledgement, which is
	// why a founded bank can open no addressable account.
	if _, err := iban.BankCodeWidth(country); err != nil {
		return nil, fmt.Errorf("country of operation: %w", err)
	}
	assets = joiningAssets(assets)

	// NOTHING IS ALLOCATED HERE. THE BANK'S ID IS ITS BIC.
	pid, err := s.self()
	if err != nil {
		return nil, err
	}
	if ParticipantID(bic) != pid {
		return nil, fmt.Errorf("%w: this is %s's store and the application is %s's",
			ErrNotThisInstitutionsAct, pid, bic)
	}
	id := string(pid)
	bookID := ledger.BookID(id)
	bank := ledger.NewBook(s.ledgers, bookID, s.clock)

	gl, err := bank.CreateLedgerTx(ctx, tx, name+" GL")
	if err != nil {
		return nil, err
	}
	customers, err := bank.CreateSubledgerTx(ctx, tx, gl.ID, "Customer Deposits")
	if err != nil {
		return nil, err
	}
	interbank, err := bank.CreateSubledgerTx(ctx, tx, gl.ID, "Interbank")
	if err != nil {
		return nil, err
	}
	// Vault cash is not owed to a customer and is not a position against another
	// institution: it is the bank's own money, the one asset on this chart that is
	// nobody else's promise.
	treasury, err := bank.CreateSubledgerTx(ctx, tx, gl.ID, "Treasury")
	if err != nil {
		return nil, err
	}
	// And what its owners put in, which is the one block on this chart that is
	// neither an asset nor owed to anybody outside the bank.
	capital, err := bank.CreateSubledgerTx(ctx, tx, gl.ID, "Capital")
	if err != nil {
		return nil, err
	}

	// One set of internal accounts per asset. Naming them with the asset in
	// parentheses keeps them apart in a chart of accounts that now holds
	// several of each.
	accounts := make(map[ledger.AssetCode]BankAccounts, len(assets))
	for _, asset := range assets {
		// Reject an unknown code before writing anything, rather than letting the
		// first CreateAccountTx below fail with part of the chart already created.
		if _, err := ledger.LookupAsset(asset); err != nil {
			return nil, err
		}
		if _, seen := accounts[asset]; seen {
			// A repeated code would create a second set of accounts and then overwrite
			// the map entry pointing at the first, orphaning four accounts.
			continue
		}

		suspense, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Clearing Suspense ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return nil, err
		}
		reserve, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Reserve at Central Bank ("+string(asset)+")", ledger.Asset, asset)
		if err != nil {
			return nil, err
		}
		// A Liability, because it is money the bank owes somebody — the same class as
		// a customer's deposit, and specifically not an asset of the bank's.
		unclaimed, err := bank.CreateControlAccountTx(ctx, tx, interbank.ID, "Unclaimed Balances ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return nil, err
		}
		// An Asset, and the contrast with Unclaimed Balances is the point.
		returnsReceivable, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Returns Receivable ("+string(asset)+")", ledger.Asset, asset)
		if err != nil {
			return nil, err
		}
		// An Asset, and the one on this chart that is not a claim on anybody.
		vaultCash, err := bank.CreateAccountTx(ctx, tx, treasury.ID, "Vault Cash ("+string(asset)+")", ledger.Asset, asset)
		if err != nil {
			return nil, err
		}
		// Equity, and the only credit on this chart that is owed to nobody: a
		// depositor is owed their balance and a shareholder is not.
		shareCapital, err := bank.CreateAccountTx(ctx, tx, capital.ID, "Share Capital ("+string(asset)+")", ledger.Equity, asset)
		if err != nil {
			return nil, err
		}
		accounts[asset] = BankAccounts{
			Suspense:          suspense.ID,
			Reserve:           reserve.ID,
			Unclaimed:         unclaimed.ID,
			ReturnsReceivable: returnsReceivable.ID,
			VaultCash:         vaultCash.ID,
			ShareCapital:      shareCapital.ID,
		}
	}

	// The bank's default deposit product, created here because a bank with no
	// product cannot open an account: every deposit account is opened FROM one.
	catalogue := product.NewCatalogue(s.products, bank, bookID, s.clock)
	basic, err := catalogue.CreateProductTx(ctx, tx, "Basic Current Account", product.CurrentAccount)
	if err != nil {
		return nil, err
	}
	today := ledger.DayStart(s.now())
	if _, err := catalogue.DraftVersionTx(ctx, tx, basic.ID, today, product.OverdraftPricing{}); err != nil {
		return nil, err
	}
	if _, err := catalogue.PublishVersionTx(ctx, tx, basic.ID, today); err != nil {
		return nil, err
	}

	p := Bank{
		ID:                ParticipantID(id),
		Name:              name,
		BIC:               bic,
		Issuer:            iban.Issuer{Country: country},
		BookID:            bookID,
		CustomerSubledger: customers.ID,
		ProductID:         basic.ID,
		Assets:            accounts,
		CreatedAt:         s.now(),
	}
	if err := tx.PutBank(ctx, p); err != nil {
		return nil, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventParticipantAdded, string(p.ID), p); err != nil {
		return nil, err
	}
	return s.bind(p), nil
}

// OpenSettlementAccountTx is the settlement agent's own act: opening one
// account, in one asset, in its own book, and recording that it holds it. It
// writes nothing of the bank's.
func (s *CentralBankNetwork) OpenSettlementAccountTx(ctx context.Context, tx CentralBankTx, in AdmissionRequest) (SettlementMember, iban.Issuer, error) {
	// FIRST: this act returns early for an asset the agent already holds, so a guard
	// further down would be reachable only on the path that opens something, and a
	// repeated request on a clearing house's network would come back with no refusal.
	book, err := s.centralBankBook()
	if err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}
	if _, err := ledger.LookupAsset(in.Asset); err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}
	// Before the read below, and before centralBankChartTx's find-or-create. See
	// admissionSequenceTx.
	if err := s.admissionSequenceTx(ctx, tx); err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}
	// The registry's act, and it runs BEFORE the early return below rather than
	// beside the account, so the second currency of one admission is answered with
	// the code the first was given. Idempotent per (country, BIC).
	issuer, err := s.allocateBankCodeTx(ctx, tx, in.Country, in.BIC)
	if err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}

	member, err := tx.GetSettlementMember(ctx, in.BIC)
	switch {
	case err == nil:
		if _, held := member.Accounts[in.Asset]; held {
			return member, issuer, nil
		}
		if member.Accounts == nil {
			// This act never writes a member with no accounts, so a nil map here is a
			// row somebody else wrote. Assigning into one panics.
			member.Accounts = make(map[ledger.AssetCode]ledger.AccountID, 1)
		}
	case errors.Is(err, ErrSettlementMemberNotFound):
		member = SettlementMember{
			BIC:      in.BIC,
			Name:     in.Name,
			Accounts: make(map[ledger.AssetCode]ledger.AccountID, 1),
			OpenedAt: s.now(),
		}
	default:
		return SettlementMember{}, iban.Issuer{}, err
	}

	reserves, _, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}
	// The other side of every reserve credit in this asset, in the central bank's
	// own capital block. One per asset and shared by every member: a lookup on the
	// second admission in an asset and a creation on the first.
	if _, err := s.centralBankAssetsAccountTx(ctx, tx, in.Asset); err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}
	account, err := book.CreateAccountTx(ctx, tx, reserves,
		"Reserve: "+member.Name+" ("+string(in.Asset)+")", ledger.Liability, in.Asset)
	if err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}

	member.Accounts[in.Asset] = account.ID
	if err := tx.PutSettlementMember(ctx, member); err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventSettlementAccountOpened, string(member.BIC), member); err != nil {
		return SettlementMember{}, iban.Issuer{}, err
	}
	return member, issuer, nil
}

// allocateBankCodeTx is the NATIONAL REGISTRY's act: giving one institution the
// code its customers' addresses will carry, in one country, once.
func (s *CentralBankNetwork) allocateBankCodeTx(ctx context.Context, tx CentralBankTx, country iban.Country, bic iso20022.BIC) (iban.Issuer, error) {
	width, err := iban.BankCodeWidth(country)
	if err != nil {
		return iban.Issuer{}, fmt.Errorf("payment: %s applied to a register this system does not keep: %w", bic, err)
	}
	switch held, err := tx.GetBankCodeForBIC(ctx, country, bic); {
	case err == nil:
		return held.Issuer, nil
	case !errors.Is(err, ErrBankCodeNotAllocated):
		return iban.Issuer{}, err
	}

	serial, err := tx.NextBankCodeSerial(ctx, s.book(), country)
	if err != nil {
		return iban.Issuer{}, err
	}
	top := uint64(1)
	for range width {
		top *= 10
	}
	if serial == 0 || serial >= top {
		return iban.Issuer{}, fmt.Errorf("payment: %s allocates %d-digit bank codes and this register has issued all %d of them",
			country, width, top-1)
	}
	issuer := iban.Issuer{Country: country, BankCode: iban.BankCode(fmt.Sprintf("%0*d", width, top-serial))}

	// Whether anybody else already holds it.
	switch taken, err := tx.GetBankCode(ctx, issuer); {
	case err == nil && taken.BIC != bic:
		return iban.Issuer{}, fmt.Errorf("%w: %s in %s is already %s's",
			ErrBankCodeTaken, issuer.BankCode, country, taken.BIC)
	case err != nil && !errors.Is(err, ErrBankCodeNotAllocated):
		return iban.Issuer{}, err
	}
	if err := tx.PutBankCode(ctx, BankCodeAllocation{Issuer: issuer, BIC: bic, AllocatedAt: s.now()}); err != nil {
		return iban.Issuer{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventBankCodeAllocated, string(bic), issuer); err != nil {
		return iban.Issuer{}, err
	}
	return issuer, nil
}

// checkAcknowledgement refuses an acknowledgement neither act can act on.
func checkAcknowledgement(in AdmissionAcknowledgement) error {
	if err := in.BIC.Validate(); err != nil {
		return fmt.Errorf("payment: this acknowledgement names no account owner this system can address: %w", err)
	}
	if in.Ref == "" {
		return fmt.Errorf("%w: it is addressed to %s", ErrAdmissionNotIdentified, in.BIC)
	}
	// The allocation, structurally.
	if err := in.Issuer.Validate(); err != nil {
		return fmt.Errorf("payment: this acknowledgement gives %s no usable address range: %w", in.BIC, err)
	}
	if len(in.Accounts) == 0 {
		return fmt.Errorf("%w: it names none at all, and would admit %s to nothing",
			ErrAdmittedAccountUnusable, in.BIC)
	}
	for asset, account := range in.Accounts {
		if asset == "" {
			return fmt.Errorf("%w: an account for %s names no asset", ErrAdmittedAccountUnusable, in.BIC)
		}
		if account == "" {
			return fmt.Errorf("%w: the %s account for %s has no identifier",
				ErrAdmittedAccountUnusable, asset, in.BIC)
		}
	}
	return nil
}

// AdmitMemberTx is the clearing house's own act: writing down where to send a
// message addressed to this member.
func (s *ClearingHouseNetwork) AdmitMemberTx(ctx context.Context, tx CsmTx, in AdmissionAcknowledgement) (RosterEntry, error) {
	// The acknowledgement first, because this reads no store and one this act
	// cannot use should not cost an identity from the network's counter.
	if err := checkAcknowledgement(in); err != nil {
		return RosterEntry{}, err
	}
	// Then the id, before the read the refusal is decided from. See
	// admissionSequenceTx.
	if err := s.admissionSequenceTx(ctx, tx); err != nil {
		return RosterEntry{}, err
	}
	entry, err := tx.GetRosterEntry(ctx, in.BIC)
	switch {
	case err == nil:
		if entry.AdmissionRef != in.Ref {
			return RosterEntry{}, fmt.Errorf("%w: %s is admitted under %q and this acknowledgement quotes %q",
				ErrBICAlreadyAdmitted, in.BIC, entry.AdmissionRef, in.Ref)
		}
		// The second acknowledgement of one admission repeats the allocation, so
		// equal is the ordinary case and different is an entry being MOVED — see
		// ErrBankCodeReplaced for what that costs every member holding a copy.
		if entry.Issuer != in.Issuer {
			return RosterEntry{}, fmt.Errorf("%w: %s is published as %s %s and this acknowledgement says %s %s",
				ErrBankCodeReplaced, in.BIC, entry.Issuer.Country, entry.Issuer.BankCode,
				in.Issuer.Country, in.Issuer.BankCode)
		}
	case errors.Is(err, ErrRosterEntryNotFound):
		entry = RosterEntry{BIC: in.BIC, Issuer: in.Issuer, AdmissionRef: in.Ref, AdmittedAt: s.now()}
	default:
		return RosterEntry{}, err
	}
	// And whether anybody ELSE is already published under this allocation.
	switch other, err := tx.GetRosterEntryByIssuer(ctx, in.Issuer); {
	case err == nil && other.BIC != in.BIC:
		return RosterEntry{}, fmt.Errorf("%w: %s %s is published for %s",
			ErrBankCodeTaken, in.Issuer.Country, in.Issuer.BankCode, other.BIC)
	case err != nil && !errors.Is(err, ErrRosterEntryNotFound):
		return RosterEntry{}, err
	}

	// Sorted, and only the assets that are new.
	for _, asset := range slices.Sorted(maps.Keys(in.Accounts)) {
		if !slices.Contains(entry.Assets, asset) {
			entry.Assets = append(entry.Assets, asset)
		}
	}
	if err := tx.PutRosterEntry(ctx, entry); err != nil {
		return RosterEntry{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventMemberAdmitted, string(entry.BIC), entry); err != nil {
		return RosterEntry{}, err
	}
	return entry, nil
}

// RecordMembershipTx is the bank's second act: writing down what it was told.
func (s *BankNetwork) RecordMembershipTx(ctx context.Context, tx BankTx, in AdmissionAcknowledgement) (*Bank, error) {
	self, err := s.self()
	if err != nil {
		return nil, err
	}
	// The acknowledgement first, for AdmitMemberTx's reason: this reads no store,
	// and one this act cannot use should not cost an identity.
	if err := checkAcknowledgement(in); err != nil {
		return nil, err
	}
	// Then the id, before the read this act's write is derived from. See
	// admissionSequenceTx.
	if err := s.admissionSequenceTx(ctx, tx); err != nil {
		return nil, err
	}
	bank, err := s.bankTx(ctx, tx, self)
	if err != nil {
		return nil, err
	}
	// Whose acknowledgement this is. After this line everything the message says is
	// about this bank's own accounts at its own settlement agent.
	if bank.BIC != in.BIC {
		return nil, fmt.Errorf("%w: %s is addressed by %s and this acknowledgement is for %s",
			ErrNotThisBanksAdmission, self, bank.BIC, in.BIC)
	}
	// And WHICH ADMISSION, which the BIC above does not answer. A bank that has
	// recorded one accepts no acknowledgement from another; a bank that has
	// recorded none accepts the first that names it.
	if bank.AdmissionRef != "" && bank.AdmissionRef != in.Ref {
		return nil, fmt.Errorf("%w: %s recorded its membership under %q and this acknowledgement quotes %q",
			ErrBankAlreadyAdmitted, self, bank.AdmissionRef, in.Ref)
	}

	// And the ALLOCATION, the other thing this bank learns here and nowhere else:
	// the code its customers' addresses will carry.
	if in.Issuer.Country != bank.Issuer.Country {
		return nil, fmt.Errorf("%w: %s issues in %s and this acknowledgement allocates in %s",
			ErrBankCodeReplaced, self, bank.Issuer.Country, in.Issuer.Country)
	}
	if bank.Issuer.BankCode != "" && bank.Issuer.BankCode != in.Issuer.BankCode {
		return nil, fmt.Errorf("%w: %s issues under %s and this acknowledgement allocates %s",
			ErrBankCodeReplaced, self, bank.Issuer.BankCode, in.Issuer.BankCode)
	}
	bank.Issuer = in.Issuer

	// And WHICH ACCOUNTS. The guards above are about the message's identifiers;
	// these are about the STATE this act would leave behind.
	var usable int
	for _, asset := range slices.Sorted(maps.Keys(in.Accounts)) {
		accts, held := bank.Assets[asset]
		if !held {
			continue
		}
		account := in.Accounts[asset]
		if accts.Settlement != "" && accts.Settlement != account {
			return nil, fmt.Errorf("%w: %s settles %s through %s and this acknowledgement quotes %s",
				ErrSettlementAccountReplaced, self, asset, accts.Settlement, account)
		}
		accts.Settlement = account
		bank.Assets[asset] = accts
		usable++
	}
	if usable == 0 {
		return nil, fmt.Errorf("%w: it names %v and %s operates in none of them",
			ErrAdmittedAccountUnusable, slices.Sorted(maps.Keys(in.Accounts)), self)
	}
	bank.AdmissionRef = in.Ref
	if err := tx.PutBank(ctx, *bank); err != nil {
		return nil, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventMembershipRecorded, string(bank.ID), *bank); err != nil {
		return nil, err
	}
	// Bound AGAIN, from the row this act has just changed. A Bank's deposit
	// register is built from its Issuer, which was empty when this bank was read
	// above and is not now.
	return s.bind(*bank), nil
}

// Deposit takes cash in over the counter: the bank holds the notes, and it owes
// the depositor their balance.
func (s *BankNetwork) Deposit(ctx context.Context, participant ParticipantID, account deposit.AccountID, amount ledger.Amount, description string) error {
	return s.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		return s.DepositTx(ctx, tx, participant, account, amount, description)
	})
}

// DepositTx is Deposit within a caller-supplied unit of work. It names no account
// outside this bank's book and asks the central bank nothing; LodgeReservesTx is
// the act with a counterparty.
func (s *BankNetwork) DepositTx(ctx context.Context, tx BankTx, participant ParticipantID, account deposit.AccountID, amount ledger.Amount, description string) error {
	if amount <= 0 {
		return ErrInvalidPaymentAmount
	}
	if err := ledger.ValidateText("description", description); err != nil {
		return err
	}
	if err := ledger.ValidateText("account", string(account)); err != nil {
		return err
	}
	p, err := s.bankTx(ctx, tx, participant)
	if err != nil {
		return err
	}
	funded, err := tx.GetDepositAccount(ctx, p.BookID, account)
	if err != nil {
		return ErrAccountNotInParticipant
	}
	// A closed account is not somewhere money may land.
	if err := p.Deposit.CheckCreditTx(ctx, tx, account); err != nil {
		return err
	}
	pos, err := p.positionTx(ctx, tx, account)
	if err != nil {
		return err
	}
	accts, err := p.AccountsFor(funded.Asset)
	if err != nil {
		return err
	}
	// No guard on the settlement account: a bank's counter does not depend on its
	// central bank account, and a founded bank can take cash. LodgeReservesTx is
	// where an agent holding no account for this bank is refused.
	_, err = p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		Entries: []ledger.Entry{
			{AccountID: accts.VaultCash, Amount: amount, Direction: ledger.Debit},
			{AccountID: pos.Account, Subsidiary: pos.Subsidiary, Amount: amount, Direction: ledger.Credit},
		},
	})
	return err
}

// InjectCapital is a bank's owners paying money in, which is where a bank's own
// money comes from before it has a single depositor.
func (s *BankNetwork) InjectCapital(ctx context.Context, participant ParticipantID,
	asset ledger.AssetCode, amount ledger.Amount, ref string) error {

	return s.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		return s.InjectCapitalTx(ctx, tx, participant, asset, amount, ref)
	})
}

// InjectCapitalTx is InjectCapital within a caller-supplied unit of work: debit
// Vault Cash, credit Share Capital, in this bank's own book and naming no other
// institution. ref is the subscription this is payment for, and a second call
// quoting it is ledger.ErrDuplicateIdempotencyKey.
func (s *BankNetwork) InjectCapitalTx(ctx context.Context, tx BankTx, participant ParticipantID,
	asset ledger.AssetCode, amount ledger.Amount, ref string) error {

	if amount <= 0 {
		return ErrInvalidPaymentAmount
	}
	if err := ledger.ValidateText("ref", ref); err != nil {
		return err
	}
	p, err := s.bankTx(ctx, tx, participant)
	if err != nil {
		return err
	}
	accts, err := p.AccountsFor(asset)
	if err != nil {
		return err
	}
	// Cash the bank OWNS rather than owes, which is the whole contrast with
	// DepositTx: the same debit, and a credit to equity instead of to a customer.
	_, err = p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description:    "Capital subscription: " + string(asset),
		IdempotencyKey: "capital:" + ref + ":" + string(asset),
		Entries: []ledger.Entry{
			{AccountID: accts.VaultCash, Amount: amount, Direction: ledger.Debit},
			{AccountID: accts.ShareCapital, Amount: amount, Direction: ledger.Credit},
		},
	})
	return err
}

// ---------------------------------------------------------------------------
// The lodgement: cash becomes reserves, in two books, over a message
// ---------------------------------------------------------------------------

// LodgeReserves is a bank swapping vault cash for a claim on its central bank.
func (s *BankNetwork) LodgeReserves(ctx context.Context, asset ledger.AssetCode,
	amount ledger.Amount, mc MessageContext) (LodgementInstruction, iso20022.Envelope, error) {
	return unit.Run2(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (LodgementInstruction, iso20022.Envelope, error) {
		return s.LodgeReservesTx(ctx, tx, asset, amount, mc)
	})
}

// LodgeReservesTx is LodgeReserves within a caller-supplied unit of work.
func (s *BankNetwork) LodgeReservesTx(ctx context.Context, tx BankTx, asset ledger.AssetCode,
	amount ledger.Amount, mc MessageContext) (LodgementInstruction, iso20022.Envelope, error) {

	self, err := s.self()
	if err != nil {
		return LodgementInstruction{}, iso20022.Envelope{}, err
	}
	if amount <= 0 {
		return LodgementInstruction{}, iso20022.Envelope{}, ErrInvalidPaymentAmount
	}
	p, err := s.bankTx(ctx, tx, self)
	if err != nil {
		return LodgementInstruction{}, iso20022.Envelope{}, err
	}
	accts, err := p.AccountsFor(asset)
	if err != nil {
		return LodgementInstruction{}, iso20022.Envelope{}, err
	}
	if accts.Settlement == "" {
		return LodgementInstruction{}, iso20022.Envelope{}, fmt.Errorf(
			"%w: %s holds no reserve account it can name in %s, so it has nothing to lodge into",
			ErrSettlementMemberNotFound, p.BIC, asset)
	}

	in := LodgementInstruction{
		BIC:     p.BIC,
		Agent:   mc.To,
		Account: accts.Settlement,
		Asset:   asset,
		Amount:  amount,
		Ref:     mc.MsgID,
	}
	// Rendered inside the unit of work, before the posting is final, for the
	// reason this act's doc gives: a message that will not build must take the
	// leg down with it.
	env, err := LodgementMessage(in, mc)
	if err != nil {
		return LodgementInstruction{}, iso20022.Envelope{}, err
	}

	// The swap: a claim on the central bank replaces cash in the drawer. Both
	// accounts are this bank's own, in this bank's own book, which is what makes
	// this half of the lodgement something a member may do at all.
	if _, err := p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description:    "Lodgement to reserve: " + string(asset),
		IdempotencyKey: "lodge:" + in.Ref,
		Metadata:       map[string]string{MetadataLodgementRef: in.Ref},
		Entries: []ledger.Entry{
			{AccountID: accts.Reserve, Amount: amount, Direction: ledger.Debit},
			{AccountID: accts.VaultCash, Amount: amount, Direction: ledger.Credit},
		},
	}); err != nil {
		return LodgementInstruction{}, iso20022.Envelope{}, err
	}
	return in, env, nil
}

// ReceiveLodgement is the central bank's half: crediting a member's reserve
// account because the member asked it to.
func (s *CentralBankNetwork) ReceiveLodgement(ctx context.Context, in LodgementInstruction) (LodgementReceipt, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CentralBankTx) (LodgementReceipt, error) {
		return s.ReceiveLodgementTx(ctx, tx, in)
	})
}

// ReceiveLodgementTx is ReceiveLodgement within a caller-supplied unit of work.
func (s *CentralBankNetwork) ReceiveLodgementTx(ctx context.Context, tx CentralBankTx, in LodgementInstruction) (LodgementReceipt, error) {
	// First, for OpenSettlementAccountTx's reason.
	book, err := s.centralBankBook()
	if err != nil {
		return LodgementReceipt{}, err
	}
	if in.Amount <= 0 {
		return LodgementReceipt{}, ErrInvalidPaymentAmount
	}
	if err := in.BIC.Validate(); err != nil {
		return LodgementReceipt{}, fmt.Errorf("payment: the lodging member's address: %w", err)
	}
	// The agent's own record of the account it holds, and never the number the
	// message quoted. See the doc above.
	held, err := s.settlementAccountTx(ctx, tx, in.BIC, in.Asset)
	if err != nil {
		return LodgementReceipt{}, err
	}
	// An instruction quoting NO account is refused too: a camt.050 says which
	// account it means, and a member and its agent silently disagreeing about
	// which account a lodgement credits is what this comparison is for.
	if in.Account != held {
		return LodgementReceipt{}, fmt.Errorf(
			"%w: %s lodges %s into %q and this agent holds %s for it",
			ErrSettlementAccountReplaced, in.BIC, in.Asset, in.Account, held)
	}

	assets, err := s.centralBankAssetsAccountTx(ctx, tx, in.Asset)
	if err != nil {
		return LodgementReceipt{}, err
	}
	if _, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description:    "Reserve credit on lodgement: " + string(in.BIC),
		IdempotencyKey: "lodge:" + in.Ref,
		Entries: []ledger.Entry{
			{AccountID: assets, Amount: in.Amount, Direction: ledger.Debit},
			{AccountID: held, Amount: in.Amount, Direction: ledger.Credit},
		},
	}); err != nil {
		return LodgementReceipt{}, err
	}
	return LodgementReceipt{Ref: in.Ref, Status: iso20022.TransactionStatusSettlementCompleted}, nil
}

// ---------------------------------------------------------------------------
// Mandates (for direct debits)
// ---------------------------------------------------------------------------

// CreateMandate records a debtor's authorization for a creditor to collect
// funds via direct debit. A MaxAmount of 0 means unlimited.
func (s *BankNetwork) CreateMandate(ctx context.Context, assertedAgent iso20022.BIC, debtor, creditor PartyRef, maxAmount ledger.Amount) (Mandate, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (Mandate, error) {
		return s.CreateMandateTx(ctx, tx, assertedAgent, debtor, creditor, maxAmount)
	})
}

// CreateMandateTx is CreateMandate within a caller-supplied unit of work.
func (s *BankNetwork) CreateMandateTx(ctx context.Context, tx BankTx, assertedAgent iso20022.BIC, debtor, creditor PartyRef, maxAmount ledger.Amount) (Mandate, error) {
	if _, err := s.self(); err != nil {
		return Mandate{}, fmt.Errorf("%w: %w", ErrNotThisBanksMandate, err)
	}
	creditorAcct, _, err := s.checkPartyTx(ctx, tx, "creditor", creditor)
	if err != nil {
		return Mandate{}, err
	}
	// The DEBTOR is validated for shape and not for existence.
	if err := validateParty("debtor", debtor); err != nil {
		return Mandate{}, err
	}
	// ValidateText accepts the empty string — it refuses control characters and
	// invalid UTF-8 and nothing else — so the presence check is separate.
	if debtor.Account == "" {
		return Mandate{}, fmt.Errorf(
			"payment: a mandate names the account it authorises debits from; this one quotes account %q",
			debtor.Account)
	}
	// And the agent, out of this bank's routing directory. The refusals are a
	// submission's, so a mandate cannot be signed against a bank this one could not
	// then collect from.
	debtorAgent, err := s.routeTx(ctx, tx, debtor.Identifier, assertedAgent)
	if err != nil {
		return Mandate{}, err
	}

	id, err := tx.NextID(ctx, s.book(), "mnd")
	if err != nil {
		return Mandate{}, err
	}
	m := Mandate{
		ID:          MandateID(id),
		DebtorAgent: debtorAgent,
		Debtor:      debtor,
		Creditor:    creditor,
		Asset:       creditorAcct.Asset,
		MaxAmount:   maxAmount,
		Status:      MandateActive,
		CreatedAt:   s.now(),
	}
	if err := tx.PutMandate(ctx, m); err != nil {
		return Mandate{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventMandateCreated, string(m.ID), m); err != nil {
		return Mandate{}, err
	}
	return m, nil
}

// RevokeMandate marks a mandate as revoked. Future direct debits referencing
// it will be rejected.
func (s *BankNetwork) RevokeMandate(ctx context.Context, id MandateID) error {
	return s.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		return s.RevokeMandateTx(ctx, tx, id)
	})
}

// RevokeMandateTx is RevokeMandate within a caller-supplied unit of work.
func (s *BankNetwork) RevokeMandateTx(ctx context.Context, tx BankTx, id MandateID) error {
	if _, err := s.self(); err != nil {
		return fmt.Errorf("%w: %w", ErrNotThisBanksMandate, err)
	}
	m, err := tx.GetMandate(ctx, id)
	if err != nil {
		return err
	}
	m.Status = MandateRevoked
	if err := tx.PutMandate(ctx, m); err != nil {
		return err
	}
	return s.appendAuditTx(ctx, tx, ledger.EventMandateRevoked, string(m.ID), m)
}

// ---------------------------------------------------------------------------
// Clearing cycles
// ---------------------------------------------------------------------------

// OpenCycle opens a clearing cycle for a scheme. Payments submitted while it
// is open accumulate in it until CloseCycle computes their net positions.
func (s *ClearingHouseNetwork) OpenCycle(ctx context.Context, scheme SchemeID) (ClearingCycle, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CsmTx) (ClearingCycle, error) {
		return s.OpenCycleTx(ctx, tx, scheme)
	})
}

// OpenCycleTx is OpenCycle within a caller-supplied unit of work. The
// "already open?" check and the write are one step, so two concurrent callers
// cannot both open a cycle for the same scheme.
func (s *ClearingHouseNetwork) OpenCycleTx(ctx context.Context, tx CsmTx, scheme SchemeID) (ClearingCycle, error) {
	if _, ok := s.scheme(scheme); !ok {
		return ClearingCycle{}, ErrSchemeNotFound
	}
	switch _, err := tx.GetOpenCycle(ctx, scheme); {
	case err == nil:
		return ClearingCycle{}, ErrCycleAlreadyOpen
	case !errors.Is(err, ErrCycleNotFound):
		return ClearingCycle{}, err
	}

	id, err := tx.NextID(ctx, s.book(), "cyc")
	if err != nil {
		return ClearingCycle{}, err
	}
	c := ClearingCycle{
		ID:           CycleID(id),
		Scheme:       scheme,
		Status:       CycleOpen,
		NetPositions: map[iso20022.BIC]ledger.Amount{},
		OpenedAt:     s.now(),
	}
	if err := tx.PutCycle(ctx, c); err != nil {
		return ClearingCycle{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventCycleOpened, string(c.ID), c); err != nil {
		return ClearingCycle{}, err
	}
	return c, nil
}

// SettleAtCSM is the clearing house recording, on its OWN copies, that a cycle
// it instructed has settled — and handing back the payments so the caller can
// tell each one's banks.
func (s *ClearingHouseNetwork) SettleAtCSM(ctx context.Context, id CycleID) ([]Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CsmTx) ([]Payment, error) {
		return s.SettleAtCSMTx(ctx, tx, id)
	})
}

// SettleAtCSMTx is SettleAtCSM within a caller-supplied unit of work.
func (s *ClearingHouseNetwork) SettleAtCSMTx(ctx context.Context, tx CsmTx, id CycleID) ([]Payment, error) {
	if err := s.clearingHouse(); err != nil {
		return nil, err
	}
	c, err := tx.GetCycle(ctx, id)
	if err != nil {
		return nil, err
	}
	// The CYCLE first. The status is what the ACSC says, so it is written by the
	// institution the ACSC was sent to; the settlement id is not written at all —
	// see SettleCycleTx for what that costs.
	if c.Status == CycleClosed {
		c.Status = CycleSettled
		if err := tx.PutCycle(ctx, c); err != nil {
			return nil, err
		}
	}
	out := make([]Payment, 0, len(c.PaymentIDs))
	for _, pid := range c.PaymentIDs {
		p, err := tx.GetPayment(ctx, pid)
		if err != nil {
			return nil, err
		}
		if p.Status == Settled {
			out = append(out, p)
			continue
		}
		if err := s.transition(&p, Settled); err != nil {
			return nil, err
		}
		if err := tx.PutPayment(ctx, p); err != nil {
			return nil, err
		}
		if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentSettled, string(p.ID), p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// CloseCycle reaches the cut-off: it computes each participant's net position
// across the cycle's payments and marks the payments Cleared. No money moves
// yet — that happens at SettleCycle.
func (s *ClearingHouseNetwork) CloseCycle(ctx context.Context, id CycleID) (ClearingCycle, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CsmTx) (ClearingCycle, error) {
		return s.CloseCycleTx(ctx, tx, id)
	})
}

// CloseCycleTx is CloseCycle within a caller-supplied unit of work.
func (s *ClearingHouseNetwork) CloseCycleTx(ctx context.Context, tx CsmTx, id CycleID) (ClearingCycle, error) {
	c, err := tx.GetCycle(ctx, id)
	if err != nil {
		return ClearingCycle{}, err
	}
	if c.Status != CycleOpen {
		return ClearingCycle{}, ErrCycleNotOpen
	}

	// Keyed by the agent BICs the payments already carry, which is what the
	// settlement agent receiving these figures keys its own records by. See
	// ClearingCycle.NetPositions.
	net := map[iso20022.BIC]ledger.Amount{}
	for _, pid := range c.PaymentIDs {
		p, err := tx.GetPayment(ctx, pid)
		if err != nil {
			return ClearingCycle{}, err
		}
		// Money flows debtor -> creditor regardless of scheme direction.
		net[p.DebtorDetails.Agent] -= p.Amount
		net[p.CreditorDetails.Agent] += p.Amount
		if err := s.transition(&p, Cleared); err != nil {
			return ClearingCycle{}, err
		}
		if err := tx.PutPayment(ctx, p); err != nil {
			return ClearingCycle{}, err
		}
		if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentCleared, string(p.ID), p); err != nil {
			return ClearingCycle{}, err
		}
	}

	c.NetPositions = net
	c.Status = CycleClosed
	c.ClosedAt = s.now()
	if err := tx.PutCycle(ctx, c); err != nil {
		return ClearingCycle{}, err
	}
	// The per-payment events come first and cycle.closed last, so the log reads in the
	// order the work happened; all share this transaction, so a failed cut-off leaves
	// none of them behind.
	if err := s.appendAuditTx(ctx, tx, ledger.EventCycleClosed, string(c.ID), c); err != nil {
		return ClearingCycle{}, err
	}
	return c, nil
}

// SettleCycle settles a closed cycle: it moves each participant's net position
// across the members' reserve accounts at the central bank, in ONE transaction,
// in the central bank's own book.
func (s *CentralBankNetwork) SettleCycle(ctx context.Context, id CycleID, legs []SettlementLeg) (Settlement, []SettlementStatement, error) {
	return unit.Run2(ctx, s.store.Update, func(ctx context.Context, tx CentralBankTx) (Settlement, []SettlementStatement, error) {
		return s.SettleCycleTx(ctx, tx, id, legs)
	})
}

// SettleCycleTx is SettleCycle within a caller-supplied unit of work.
func (s *CentralBankNetwork) SettleCycleTx(ctx context.Context, tx CentralBankTx, id CycleID, instructed []SettlementLeg) (Settlement, []SettlementStatement, error) {
	book, err := s.centralBankBook()
	if err != nil {
		return Settlement{}, nil, err
	}
	// A redelivered instruction, refused before anything is read or checked, off
	// the agent's OWN settlement row against the cycle.
	switch _, err := tx.GetSettlementByCycle(ctx, id); {
	case err == nil:
		return Settlement{}, nil, ErrCycleAlreadySettled
	case !errors.Is(err, ErrSettlementNotFound):
		return Settlement{}, nil, err
	}

	// 1. Central-bank settlement transaction: move netted reserves between
	// participants. The net positions sum to zero, so this balances.
	positions, asset, err := positionsIn(id, instructed)
	if err != nil {
		return Settlement{}, nil, err
	}
	legs, err := s.settlementLegsTx(ctx, tx, positions, asset)
	if err != nil {
		return Settlement{}, nil, err
	}

	// The central bank's decision, and the whole of what it decides: can each net
	// payer cover its position out of the reserves it holds HERE?
	for _, leg := range legs {
		if leg.net >= 0 {
			continue
		}
		held, err := book.BookBalanceTx(ctx, tx, leg.settlement.Total())
		if err != nil {
			return Settlement{}, nil, err
		}
		if held+leg.net < 0 {
			return Settlement{}, nil, fmt.Errorf("%w: %s is short %d in %s",
				ledger.ErrInsufficientBalance, leg.bic, -(held + leg.net), asset)
		}
	}

	cbEntries := make([]ledger.Entry, 0, len(legs))
	for _, leg := range legs {
		if leg.net > 0 {
			cbEntries = append(cbEntries, ledger.Entry{AccountID: leg.settlement, Amount: leg.net, Direction: ledger.Credit})
		} else {
			cbEntries = append(cbEntries, ledger.Entry{AccountID: leg.settlement, Amount: -leg.net, Direction: ledger.Debit})
		}
	}

	var settlementTx ledger.TransactionID
	statements := make([]SettlementStatement, 0, len(legs))
	if len(cbEntries) > 0 {
		posted, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			IdempotencyKey: string(id) + ":settle",
			Description:    "Settlement of clearing cycle " + string(id),
			Entries:        cbEntries,
		})
		if err != nil {
			return Settlement{}, nil, err
		}
		settlementTx = posted.ID

		// 2. What each member is TOLD. The balance is read AFTER the posting and inside
		//    the same unit of work, which is what makes it a CLOSING balance: reading it
		//    before would produce an opening balance labelled CLBD.
		for _, leg := range legs {
			closing, err := book.BookBalanceTx(ctx, tx, leg.settlement.Total())
			if err != nil {
				return Settlement{}, nil, err
			}
			statements = append(statements, SettlementStatement{
				Agent:          leg.bic,
				Account:        leg.settlement,
				Asset:          asset,
				Reference:      string(id),
				Movement:       leg.net,
				ClosingBalance: closing,
				ValueDate:      s.now(),
			})
		}
	}

	settlementID, err := tx.NextID(ctx, s.book(), "set")
	if err != nil {
		return Settlement{}, nil, err
	}
	st := Settlement{
		ID:           SettlementID(settlementID),
		CycleID:      id,
		NetPositions: positions,
		Asset:        asset,
		SettlementTx: settlementTx,
		ValueDate:    s.now(),
		SettledAt:    s.now(),
	}
	if err := tx.PutSettlement(ctx, st); err != nil {
		return Settlement{}, nil, err
	}
	// The settlement's own id, which only exists once the row above has been
	// allocated one. It travels as Stmt/Id — the account servicer's reference for
	// the statement — so a member can quote it back at the central bank.
	for i := range statements {
		statements[i].StatementRef = string(st.ID)
	}

	// The CYCLE is not written here: it is the clearing house's own row, and it
	// marks it settled on the ACSC it is answered with — see SettleAtCSMTx.
	if err := s.appendAuditTx(ctx, tx, ledger.EventCycleSettled, string(id), st); err != nil {
		return Settlement{}, nil, err
	}
	return st, statements, nil
}

// positionsIn reads a settlement instruction back into the net positions it
// describes, and refuses one that is not about the cycle it was said to be.
func positionsIn(id CycleID, legs []SettlementLeg) (map[iso20022.BIC]ledger.Amount, ledger.AssetCode, error) {
	if len(legs) == 0 {
		return nil, "", fmt.Errorf("%w: %s names no legs", ErrInvalidSettlement, id)
	}
	asset := legs[0].Asset
	for _, leg := range legs {
		if leg.Reference != string(id) {
			return nil, "", fmt.Errorf("%w: a leg of %s references %s", ErrInvalidSettlement, id, leg.Reference)
		}
		if leg.Asset != asset {
			return nil, "", fmt.Errorf("%w: %s mixes %s and %s", ErrInvalidSettlement, id, asset, leg.Asset)
		}
	}

	agents := []iso20022.BIC{legs[0].From, legs[0].To}
	for _, leg := range legs[1:] {
		agents = slices.DeleteFunc(agents, func(bic iso20022.BIC) bool {
			return bic != leg.From && bic != leg.To
		})
	}
	if len(agents) != 1 {
		return nil, "", fmt.Errorf("%w: %s names %d addresses every leg has in common, want exactly one settlement agent",
			ErrInvalidSettlement, id, len(agents))
	}
	agent := agents[0]

	positions := make(map[iso20022.BIC]ledger.Amount, len(legs))
	for _, leg := range legs {
		// A net payer's leg runs FROM it to the agent; a net receiver's the
		// other way. See SettlementLegsOf, which is this rendered forwards.
		member, net := leg.From, -leg.Amount
		if member == agent {
			member, net = leg.To, leg.Amount
		}
		if _, seen := positions[member]; seen {
			return nil, "", fmt.Errorf("%w: %s names %s twice", ErrInvalidSettlement, id, member)
		}
		positions[member] = net
	}
	return positions, asset, nil
}

// settlementLeg pairs a MEMBER — named by its address, which is the only name
// this institution has for it — with its non-zero net position in a cycle, and
// with the account in the CENTRAL BANK's own book that the position moves
// through.
type settlementLeg struct {
	bic        iso20022.BIC
	settlement ledger.AccountID
	net        ledger.Amount
}

// settlementLegsTx resolves a cycle's net positions to the settlement agent's
// own members, in the order it opened their accounts, and each member to the
// account it holds for that member in the cycle's asset.
func (s *CentralBankNetwork) settlementLegsTx(ctx context.Context, tx CentralBankTx, netPositions map[iso20022.BIC]ledger.Amount, asset ledger.AssetCode) ([]settlementLeg, error) {
	members, err := tx.ListSettlementMembers(ctx)
	if err != nil {
		return nil, err
	}

	legs := make([]settlementLeg, 0, len(netPositions))
	matched := make(map[iso20022.BIC]bool, len(netPositions))
	for _, m := range members {
		net, ok := netPositions[m.BIC]
		if !ok || net == 0 {
			continue
		}
		settlement, err := s.settlementAccountTx(ctx, tx, m.BIC, asset)
		if err != nil {
			return nil, err
		}
		matched[m.BIC] = true
		legs = append(legs, settlementLeg{bic: m.BIC, settlement: settlement, net: net})
	}

	// Every non-zero position must have matched a member; one that matched nothing
	// would silently drop money out of the settlement. The unmatched are named sorted,
	// so the same cycle does not fail with a different message on every run.
	missing := make([]string, 0)
	for bic, net := range netPositions {
		if net != 0 && !matched[bic] {
			missing = append(missing, string(bic))
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("%w: no settlement account is held for %s",
			ErrSettlementMemberNotFound, strings.Join(missing, ", "))
	}
	return legs, nil
}

// PostSettlementAdvice is PostSettlementAdviceTx in its own unit of work, which
// is what a bank acting on a statement it has just been handed needs: the
// message is the whole of the input, so there is nothing else to commit with it.
func (s *BankNetwork) PostSettlementAdvice(ctx context.Context, m AdvisedMovement) (SettlementAdvice, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (SettlementAdvice, error) {
		return s.PostSettlementAdviceTx(ctx, tx, m)
	})
}

// PostSettlementAdviceTx is a member bank booking a cut-off it was told about:
// the mirror leg, in its OWN ledger, and the row that records that it did.
func (s *BankNetwork) PostSettlementAdviceTx(ctx context.Context, tx BankTx, m AdvisedMovement) (SettlementAdvice, error) {
	self, err := s.self()
	if err != nil {
		return SettlementAdvice{}, err
	}
	p, err := s.bankTx(ctx, tx, self)
	if err != nil {
		return SettlementAdvice{}, err
	}
	accts, err := p.AccountsFor(m.Asset)
	if err != nil {
		return SettlementAdvice{}, err
	}
	if m.Account != accts.Settlement {
		return SettlementAdvice{}, fmt.Errorf("%w: %s is not %s's reserve account", ErrStatementNotForThisBank, m.Account, self)
	}

	switch existing, err := tx.GetSettlementAdvice(ctx, p.BookID, m.Reference, m.Asset); {
	case err == nil && existing.Status == AdvicePosted:
		return existing, nil
	case err != nil && !errors.Is(err, ErrSettlementAdviceNotFound):
		return SettlementAdvice{}, err
	}

	now := s.now()
	advice := SettlementAdvice{
		Book:           p.BookID,
		Reference:      m.Reference,
		Asset:          m.Asset,
		Movement:       m.Movement,
		ClosingBalance: m.ClosingBalance,
		Status:         AdviceAdvised,
		AdvisedAt:      now,
	}
	// Written before the posting and committed WITH it, so the row and the mirror
	// leg stand or fall together.
	if err := tx.PutSettlementAdvice(ctx, p.BookID, advice); err != nil {
		return SettlementAdvice{}, err
	}

	var entries []ledger.Entry
	switch {
	case m.Movement > 0: // net receiver: reserve up, and the suspense up with it
		entries = []ledger.Entry{
			{AccountID: accts.Reserve, Amount: m.Movement, Direction: ledger.Debit},
			{AccountID: accts.Suspense, Amount: m.Movement, Direction: ledger.Credit},
		}
	case m.Movement < 0: // net payer: reserve down, and the suspense down with it
		entries = []ledger.Entry{
			{AccountID: accts.Suspense, Amount: -m.Movement, Direction: ledger.Debit},
			{AccountID: accts.Reserve, Amount: -m.Movement, Direction: ledger.Credit},
		}
	default:
		// A movement of nothing produces no leg, and the central bank sends no
		// statement for a position of zero. This arm is a guard on a caller.
		return advice, nil
	}
	// The description says "settlement of <reference>" and stops there, because
	// that is the whole of what this bank knows.
	posted, err := p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: m.Reference + ":reserve:" + string(p.ID),
		Description:    "Settlement of " + m.Reference,
		Entries:        entries,
	})
	if err != nil {
		return SettlementAdvice{}, err
	}

	advice.Status, advice.MirrorTx, advice.PostedAt = AdvicePosted, posted.ID, now
	if err := tx.PutSettlementAdvice(ctx, p.BookID, advice); err != nil {
		return SettlementAdvice{}, err
	}
	return advice, nil
}

// SettleAtBank is SettleAtBankTx in its own unit of work, which is what a bank
// acting on an advice it has just been handed needs: the message names one
// payment and there is nothing else to commit with it.
func (s *BankNetwork) SettleAtBank(ctx context.Context, id PaymentID) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (Payment, error) {
		return s.SettleAtBankTx(ctx, tx, id)
	})
}

// SettleAtBankTx is a member bank's half of settlement: it records on this
// bank's OWN copy that the payment settled, and — if this bank holds the payee
// — releases the money out of its clearing suspense into their account.
func (s *BankNetwork) SettleAtBankTx(ctx context.Context, tx BankTx, id PaymentID) (Payment, error) {
	self, err := s.selfBIC()
	if err != nil {
		return Payment{}, err
	}
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.Status == Settled {
		// A redelivered advice. The ledger's idempotency key would refuse the second
		// posting anyway; this refuses to transition twice, which would otherwise report
		// a failure to a handler that did nothing wrong.
		return p, nil
	}
	// The payer's bank. Its row moves and nothing else does; see the note above.
	if p.CreditorDetails.Agent != self {
		if p.DebtorDetails.Agent != self {
			return Payment{}, fmt.Errorf("%w: %s is between %s and %s, and this is %s",
				ErrNotThisBanksPayment, id, p.DebtorDetails.Agent, p.CreditorDetails.Agent, self)
		}
		if err := s.transition(&p, Settled); err != nil {
			return Payment{}, err
		}
		if err := tx.PutPayment(ctx, p); err != nil {
			return Payment{}, err
		}
		if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentSettled, string(p.ID), p); err != nil {
			return Payment{}, err
		}
		return p, nil
	}
	creditor, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return Payment{}, err
	}
	asset, err := s.assetOf(p)
	if err != nil {
		return Payment{}, err
	}
	accts, err := creditor.AccountsFor(asset)
	if err != nil {
		return Payment{}, err
	}

	// Not allowed to fail SOFTLY: positionTx collapses every error from its read
	// into ErrAccountNotInParticipant, so a dropped connection and a genuinely
	// absent account are one value by the time they arrive.
	target, err := creditor.positionTx(ctx, tx, p.Creditor.Account)
	if err != nil {
		return Payment{}, err
	}

	// Where the money goes: the payee's own position if the account can take it,
	// and the bank's unclaimed balances if it cannot.
	description := p.Description
	if err := creditor.Deposit.CheckCreditTx(ctx, tx, p.Creditor.Account); err != nil {
		if !errors.Is(err, deposit.ErrAccountClosed) {
			// ErrAccountClosed is the ONLY refusal CheckCreditTx makes, so anything else
			// is a STORE FAILURE and not a statement about the account: diverting money to
			// unclaimed balances because a connection dropped would be wrong.
			return Payment{}, err
		}
		target = accts.Unclaimed.For(unclaimedSubsidiary(p))
		description = "Unclaimed: " + p.Description
	}

	posted, err := creditor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":credit",
		Description:    description,
		ValueDate:      p.ValueDate,
		Metadata:       paymentMetadata(&p),
		Entries: []ledger.Entry{
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: target.Account, Subsidiary: target.Subsidiary, Amount: p.Amount, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return Payment{}, err
	}
	p.CreditorLegTx = posted.ID
	// Recorded in BOTH arms, not only the diverting one. A return has to claw the
	// money back from where it actually went, and it cannot ask this question
	// again later: see clawbackTx.
	p.CreditorLegAccount = target.Account
	if err := s.transition(&p, Settled); err != nil {
		return Payment{}, err
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentSettled, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Payments
// ---------------------------------------------------------------------------

// InitiatePaymentRequest describes a payment to submit.
type InitiatePaymentRequest struct {
	Scheme      SchemeID
	Debtor      PartyRef
	Creditor    PartyRef
	Amount      ledger.Amount
	MandateID   MandateID // required for pull schemes
	EndToEndID  string    // optional client reference; deduplicated if set
	Description string
	Metadata    map[string]string

	// DebtorDetails and CreditorDetails are what the instruction says about each
	// side. Only the COUNTERPARTY's NAME is required, and which side that is
	// depends on the scheme's direction.
	DebtorDetails   PartyDetails
	CreditorDetails PartyDetails
}

// SubmitPayment is SubmitPaymentTx in its own unit of work.
func (s *BankNetwork) SubmitPayment(ctx context.Context, req InitiatePaymentRequest) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (Payment, error) {
		return s.SubmitPaymentTx(ctx, tx, req)
	})
}

// TakeInstruction is the submitting bank's half AND the proof that what it took
// can be sent, in ONE unit of work. It is what a bank actor calls.
func (s *BankNetwork) TakeInstruction(ctx context.Context, req InitiatePaymentRequest) (Payment, error) {
	var p Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		if p, err = s.SubmitPaymentTx(ctx, tx, req); err != nil {
			return err
		}
		return s.instructableTx(ctx, tx, p)
	})
	if err != nil {
		return Payment{}, err
	}
	return p, nil
}

// newPayment is the row an institution writes when a payment first exists in
// its own database, and there are three callers because there are three
// databases.
func newPayment(id PaymentID, req InitiatePaymentRequest, scheme Scheme, now time.Time) Payment {
	return Payment{
		ID:              id,
		Scheme:          req.Scheme,
		Debtor:          req.Debtor,
		Creditor:        req.Creditor,
		Amount:          req.Amount,
		MandateID:       req.MandateID,
		EndToEndID:      req.EndToEndID,
		Status:          Initiated,
		BookingDate:     now,
		ValueDate:       now.Add(scheme.SettlementDelay()),
		Description:     req.Description,
		Metadata:        req.Metadata,
		CreatedAt:       now,
		DebtorDetails:   req.DebtorDetails,
		CreditorDetails: req.CreditorDetails,
	}
}

// validateInstruction is everything about an instruction that needs no book and
// no institution: the amount is positive, and every value that will be STORED
// or used as a lookup key is safe to write.
func validateInstruction(id PaymentID, req InitiatePaymentRequest) error {
	if id != "" {
		if err := ledger.ValidateText("paymentId", string(id)); err != nil {
			return err
		}
	}
	if req.Amount <= 0 {
		return ErrInvalidPaymentAmount
	}
	if err := validateParty("debtor", req.Debtor); err != nil {
		return err
	}
	if err := validateParty("creditor", req.Creditor); err != nil {
		return err
	}
	if err := ledger.ValidateText("endToEndId", req.EndToEndID); err != nil {
		return err
	}
	if err := ledger.ValidateText("description", req.Description); err != nil {
		return err
	}
	if err := ledger.ValidateTextMap("metadata", req.Metadata); err != nil {
		return err
	}
	return ledger.ValidateText("mandateId", string(req.MandateID))
}

// SubmitPaymentTx is the SUBMITTING bank's half of accepting a payment, and
// which half that is depends on the scheme's direction.
func (s *BankNetwork) SubmitPaymentTx(ctx context.Context, tx BankTx, req InitiatePaymentRequest) (Payment, error) {
	// Common validation, before the branch, so that a malformed instruction is
	// refused the same way whichever bank is submitting it. The id is empty because
	// this act is the one that MINTS one.
	scheme, ok := s.scheme(req.Scheme)
	if !ok {
		return Payment{}, ErrSchemeNotFound
	}
	if err := validateInstruction("", req); err != nil {
		return Payment{}, err
	}
	// The id comes BEFORE the duplicate check, and the order is what makes that
	// check atomic.
	self, err := s.selfBIC()
	if err != nil {
		return Payment{}, err
	}
	id, err := tx.NextID(ctx, s.book(), "pay_"+string(self))
	if err != nil {
		return Payment{}, err
	}
	if req.EndToEndID != "" {
		switch _, err := tx.GetPaymentByEndToEndID(ctx, req.EndToEndID); {
		case err == nil:
			return Payment{}, ErrDuplicateEndToEndID
		case !errors.Is(err, ErrPaymentNotFound):
			return Payment{}, err
		}
	}

	now := s.now()
	p := newPayment(PaymentID(id), req, scheme, now)

	sc := SchemeContext{Network: s, Tx: tx, Now: now}
	push := scheme.Direction() == Push

	// The counterparty is whichever side this bank is not. Checked BEFORE the
	// side call, so an instruction that names nobody is refused before the
	// debtor leg is posted rather than after.
	counterparty := &p.CreditorDetails
	counterpartyRef := p.Creditor
	if !push {
		counterparty = &p.DebtorDetails
		counterpartyRef = p.Debtor
	}

	// The NAME is asserted by the payer and there is nowhere else it could come from:
	// the account is at another bank and this one does not read that bank's register.
	// That is the whole of what an instruction says about the other side.
	if counterparty.Name == "" {
		return Payment{}, ErrCounterpartyNotNamed
	}
	if err := ledger.ValidateText("counterparty name", counterparty.Name); err != nil {
		return Payment{}, err
	}

	// The AGENT is DERIVED from the counterparty's ADDRESS, through this bank's
	// own copy of the scheme's routing directory.
	agent, err := s.routeTx(ctx, tx, counterpartyRef.Identifier, counterparty.Agent)
	if err != nil {
		return Payment{}, err
	}
	counterparty.Agent = agent

	// The submitting bank's own side comes from its own register, overwriting
	// anything the request supplied: a payer does not rename themselves on an
	// instruction.
	if push {
		account, part, err := s.debtorSideTx(ctx, tx, scheme, &p, sc)
		if err != nil {
			return Payment{}, err
		}
		p.DebtorDetails = PartyDetails{Agent: part.BIC, Name: account.Name}
	} else {
		account, part, err := s.creditorSideTx(ctx, tx, scheme, &p, sc)
		if err != nil {
			return Payment{}, err
		}
		p.CreditorDetails = PartyDetails{Agent: part.BIC, Name: account.Name}
	}

	// Two events, because initiation and acceptance are two different facts: the
	// instruction arrived and passed its submitting bank's checks, and — later,
	// once the clearing house has taken it into a cycle — the network took
	// responsibility for it.
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentInitiated, string(p.ID), p); err != nil {
		return Payment{}, err
	}

	if push {
		if err := s.postDebtorLegTx(ctx, tx, scheme, &p); err != nil {
			return Payment{}, err
		}
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// AcceptInbound is AcceptInboundTx in its own unit of work.
func (s *BankNetwork) AcceptInbound(ctx context.Context, id PaymentID, req InitiatePaymentRequest) error {
	return s.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		return s.AcceptInboundTx(ctx, tx, id, req)
	})
}

// AcceptInboundTx is the RECEIVING bank's half: the half SubmitPaymentTx did
// not run, because the bank that ran that one could not see this side.
func (s *BankNetwork) AcceptInboundTx(ctx context.Context, tx BankTx, id PaymentID, req InitiatePaymentRequest) error {
	if _, err := s.self(); err != nil {
		return err
	}
	scheme, ok := s.scheme(req.Scheme)
	if !ok {
		return ErrSchemeNotFound
	}
	if err := validateInstruction(id, req); err != nil {
		return err
	}

	// Has this bank answered already? The row is the witness — EXCEPT when this
	// bank SUBMITTED it, which is the on-us payment.
	push := scheme.Direction() == Push
	switch existing, err := tx.GetPayment(ctx, id); {
	case err == nil:
		if existing.Status != Initiated {
			return ErrInvalidStateTransition
		}
		if push || existing.DebtorDetails.Agent != existing.CreditorDetails.Agent ||
			existing.DebtorLegTx != "" {
			return nil
		}
	case !errors.Is(err, ErrPaymentNotFound):
		return err
	}

	now := s.now()
	p := newPayment(id, req, scheme, now)
	sc := SchemeContext{Network: s, Tx: tx, Now: now}
	if push {
		// The account and participant returned below are the RECEIVING bank's own and
		// are deliberately discarded: unlike SubmitPaymentTx, this half must not use
		// them to overwrite CreditorDetails.
		if _, _, err := s.creditorSideTx(ctx, tx, scheme, &p, sc); err != nil &&
			!errors.Is(err, deposit.ErrAccountClosed) {
			return err
		}
	} else {
		// See the push arm: the account and participant here are the RECEIVING
		// (debtor's) bank's own, and are discarded for the same reason.
		if _, _, err := s.debtorSideTx(ctx, tx, scheme, &p, sc); err != nil {
			return err
		}
		if err := s.postDebtorLegTx(ctx, tx, scheme, &p); err != nil {
			return err
		}
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return err
	}
	return s.appendAuditTx(ctx, tx, ledger.EventPaymentInitiated, string(p.ID), p)
}

// ReceiveUnapplied is ReceiveUnappliedTx in its own unit of work, which is what
// a bank working through a released output file needs: one transaction fails on
// its own, and the rest of the file is unaffected.
func (s *BankNetwork) ReceiveUnapplied(ctx context.Context, id PaymentID, req InitiatePaymentRequest) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (Payment, error) {
		return s.ReceiveUnappliedTx(ctx, tx, id, req)
	})
}

// ReceiveUnappliedTx is the receiving bank writing down a payment that has
// ALREADY SETTLED and that it cannot give to the customer the message names.
func (s *BankNetwork) ReceiveUnappliedTx(ctx context.Context, tx BankTx, id PaymentID, req InitiatePaymentRequest) (Payment, error) {
	if _, err := s.self(); err != nil {
		return Payment{}, err
	}
	scheme, ok := s.scheme(req.Scheme)
	if !ok {
		return Payment{}, ErrSchemeNotFound
	}
	if err := validateInstruction(id, req); err != nil {
		return Payment{}, err
	}
	switch existing, err := tx.GetPayment(ctx, id); {
	case err == nil:
		return existing, nil
	case !errors.Is(err, ErrPaymentNotFound):
		return Payment{}, err
	}
	bank, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return Payment{}, err
	}
	accts, err := bank.AccountsFor(scheme.Asset())
	if err != nil {
		return Payment{}, err
	}

	p := newPayment(id, req, scheme, s.now())
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentInitiated, string(p.ID), p); err != nil {
		return Payment{}, err
	}

	park := ledger.PostTransactionRequest{
		Description: "Unapplied: " + p.Description,
		ValueDate:   p.ValueDate,
		Metadata:    paymentMetadata(&p),
	}
	if scheme.Direction() == Push {
		held := accts.Unclaimed.For(unclaimedSubsidiary(p))
		// The same key SettleAtBankTx's credit carries, because this IS that leg:
		// one payment credits one destination in this bank's book once, and which
		// destination it was does not make it a different posting.
		park.IdempotencyKey = string(p.ID) + ":credit"
		park.Entries = []ledger.Entry{
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: held.Account, Subsidiary: held.Subsidiary, Amount: p.Amount, Direction: ledger.Credit},
		}
	} else {
		park.IdempotencyKey = string(p.ID) + ":uncollected"
		park.Entries = []ledger.Entry{
			{AccountID: accts.ReturnsReceivable, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Credit},
		}
	}
	posted, err := bank.Ledger.PostTransactionTx(ctx, tx, park)
	if err != nil {
		return Payment{}, err
	}
	if scheme.Direction() == Push {
		p.CreditorLegTx = posted.ID
		p.CreditorLegAccount = accts.Unclaimed
	}

	if err := s.transition(&p, Settled); err != nil {
		return Payment{}, err
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentSettled, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// unclaimedSubsidiary is the holder an unclaimed credit is booked under: the
// payee's own account where this bank resolved one, and the ADDRESS the message
// quoted where it could not.
func unclaimedSubsidiary(p Payment) string {
	if p.Creditor.Account != "" {
		return string(p.Creditor.Account)
	}
	return p.Creditor.Identifier.Value
}

// RecordRelayedCreditTransfer is the clearing house writing its own copy of a
// pacs.008 it is carrying, and RecordRelayedDirectDebit is the same for a
// pacs.003.
func (s *ClearingHouseNetwork) RecordRelayedCreditTransfer(ctx context.Context, doc *iso20022.Pacs008) ([]Payment, error) {
	txs, err := s.creditTransferIn(doc)
	if err != nil {
		return nil, err
	}
	return s.RecordRelayed(ctx, txs)
}

// RecordRelayedDirectDebit is RecordRelayedCreditTransfer's pull mirror.
func (s *ClearingHouseNetwork) RecordRelayedDirectDebit(ctx context.Context, doc *iso20022.Pacs003) ([]Payment, error) {
	txs, err := s.directDebitIn(doc)
	if err != nil {
		return nil, err
	}
	return s.RecordRelayed(ctx, txs)
}

// RecordRelayed is RecordRelayedTx over a file, in ONE unit of work.
func (s *ClearingHouseNetwork) RecordRelayed(ctx context.Context, txs []InboundTransaction) ([]Payment, error) {
	out := make([]Payment, 0, len(txs))
	err := s.store.Update(ctx, func(ctx context.Context, tx CsmTx) error {
		out = out[:0]
		for _, in := range txs {
			p, err := s.RecordRelayedTx(ctx, tx, in.ID, in.Request)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecordRelayedTx is the CLEARING HOUSE's copy of a payment: the row it has to
// hold before it can accept one into a cycle or reject one out of the flow.
func (s *ClearingHouseNetwork) RecordRelayedTx(ctx context.Context, tx CsmTx, id PaymentID, req InitiatePaymentRequest) (Payment, error) {
	if err := s.clearingHouse(); err != nil {
		return Payment{}, err
	}
	scheme, ok := s.scheme(req.Scheme)
	if !ok {
		return Payment{}, ErrSchemeNotFound
	}
	if err := validateInstruction(id, req); err != nil {
		return Payment{}, err
	}

	switch existing, err := tx.GetPayment(ctx, id); {
	case err == nil:
		if existing.Status != Initiated {
			return Payment{}, ErrInvalidStateTransition
		}
		return existing, nil
	case !errors.Is(err, ErrPaymentNotFound):
		return Payment{}, err
	}

	p := newPayment(id, req, scheme, s.now())
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentInitiated, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// AcceptAtBank is AcceptAtBankTx in its own unit of work, which is what a bank
// acting on a pacs.002 it has just been handed needs: the message names one
// payment and there is nothing else to commit with it.
func (s *BankNetwork) AcceptAtBank(ctx context.Context, id PaymentID) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (Payment, error) {
		return s.AcceptAtBankTx(ctx, tx, id)
	})
}

// AcceptAtBankTx is a member bank's half of an acceptance: it records on this
// bank's OWN copy that the clearing house has taken the payment into a cycle.
func (s *BankNetwork) AcceptAtBankTx(ctx context.Context, tx BankTx, id PaymentID) (Payment, error) {
	self, err := s.selfBIC()
	if err != nil {
		return Payment{}, err
	}
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.DebtorDetails.Agent != self && p.CreditorDetails.Agent != self {
		return Payment{}, fmt.Errorf("%w: %s is between %s and %s, and this is %s",
			ErrNotThisBanksPayment, id, p.DebtorDetails.Agent, p.CreditorDetails.Agent, self)
	}
	if err := s.transition(&p, Accepted); err != nil {
		return Payment{}, err
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentAccepted, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// AcceptAtCSM is AcceptAtCSMTx in its own unit of work.
func (s *ClearingHouseNetwork) AcceptAtCSM(ctx context.Context, id PaymentID) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CsmTx) (Payment, error) {
		return s.AcceptAtCSMTx(ctx, tx, id)
	})
}

// AcceptAtCSMTx is the CLEARING HOUSE's half: it takes a payment both banks
// have now looked at into the open cycle for its scheme, and only then is the
// payment Accepted.
func (s *ClearingHouseNetwork) AcceptAtCSMTx(ctx context.Context, tx CsmTx, id PaymentID) (Payment, error) {
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if err := s.bothBanksAreMembersTx(ctx, tx, p); err != nil {
		return Payment{}, err
	}
	cycle, err := tx.GetOpenCycle(ctx, p.Scheme)
	if errors.Is(err, ErrCycleNotFound) {
		return Payment{}, ErrCycleNotOpen
	} else if err != nil {
		return Payment{}, err
	}

	if err := s.transition(&p, Accepted); err != nil {
		return Payment{}, err
	}
	p.CycleID = cycle.ID
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	cycle.PaymentIDs = append(cycle.PaymentIDs, p.ID)
	if err := tx.PutCycle(ctx, cycle); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentAccepted, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// bothBanksAreMembersTx is the clearing house asking whether it clears for the
// two banks this payment is between.
func (s *ClearingHouseNetwork) bothBanksAreMembersTx(ctx context.Context, tx CsmTx, p Payment) error {
	scheme, ok := s.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("%w: %s, so nothing says which asset it clears in", ErrSchemeNotFound, p.Scheme)
	}
	for _, side := range []struct {
		role  string
		agent iso20022.BIC
	}{
		{"payer's bank", p.DebtorDetails.Agent},
		{"payee's bank", p.CreditorDetails.Agent},
	} {
		entry, err := tx.GetRosterEntry(ctx, side.agent)
		switch {
		case err == nil:
		case errors.Is(err, ErrRosterEntryNotFound):
			return fmt.Errorf("%w: the %s, %s, is not a member of %s",
				ErrBankNotAdmitted, side.role, side.agent, p.Scheme)
		default:
			return err
		}
		if !slices.Contains(entry.Assets, scheme.Asset()) {
			return fmt.Errorf("%w: the %s, %s, is a member of %s and clears in %v, not %s",
				ErrBankNotAdmitted, side.role, side.agent, p.Scheme, entry.Assets, scheme.Asset())
		}
	}
	return nil
}

// debtorSideTx is everything a payment's own debtor bank checks about it: the
// account is one of its own, denominated in the scheme's asset, addressable in
// the scheme's addressing scheme, and good for the money.
func (s *BankNetwork) debtorSideTx(ctx context.Context, tx BankTx, scheme Scheme, p *Payment, sc SchemeContext) (deposit.Account, *Bank, error) {
	account, part, err := s.checkPartyTx(ctx, tx, "debtor", p.Debtor)
	if err != nil {
		return deposit.Account{}, nil, err
	}
	if account.Asset != scheme.Asset() {
		return deposit.Account{}, nil, ErrAssetMismatch
	}
	address, err := addressFor(scheme, p.Debtor, account)
	if err != nil {
		return deposit.Account{}, nil, err
	}
	p.Debtor.Identifier = address
	// The funds check. It is the debtor bank's alone, which is why Scheme.Validate
	// is now only ever this: the receiving side of a pull and the submitting
	// side of a push are the same bank looking at the same account.
	if err := scheme.Validate(ctx, p, sc); err != nil {
		return deposit.Account{}, nil, err
	}
	return account, part, nil
}

// creditorSideTx is everything a payment's own creditor bank checks about it:
// the account is one of its own, denominated in the scheme's asset,
// addressable, able to receive a credit at all, and — for a pull — covered by a
// mandate it holds.
func (s *BankNetwork) creditorSideTx(ctx context.Context, tx BankTx, scheme Scheme, p *Payment, sc SchemeContext) (deposit.Account, *Bank, error) {
	account, part, err := s.checkPartyTx(ctx, tx, "creditor", p.Creditor)
	if err != nil {
		return deposit.Account{}, nil, err
	}
	if account.Asset != scheme.Asset() {
		return deposit.Account{}, nil, ErrAssetMismatch
	}
	address, err := addressFor(scheme, p.Creditor, account)
	if err != nil {
		return deposit.Account{}, nil, err
	}
	p.Creditor.Identifier = address
	if err := part.Deposit.CheckCreditTx(ctx, tx, p.Creditor.Account); err != nil {
		return deposit.Account{}, nil, err
	}
	// The mandate, which in SEPA the CREDITOR holds — so it is checked by the
	// creditor's bank, and for a pull that means synchronously, at submission.
	if err := scheme.ValidateMandate(ctx, p, sc); err != nil {
		return deposit.Account{}, nil, err
	}
	return account, part, nil
}

// postDebtorLegTx moves the payer's money out of their account and into their
// own bank's clearing suspense. Whoever runs it is the debtor's bank: the
// submitting bank for a push, the receiving bank for a pull.
func (s *BankNetwork) postDebtorLegTx(ctx context.Context, tx BankTx, scheme Scheme, p *Payment) error {
	// The deposit layer is the authority for the funds/status check (run in
	// debtorSideTx); the GL posting here names the payer's position in their
	// bank's customer-deposit control account.
	debtor, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return err
	}
	// The suspense account the money lands in is the one for the scheme's
	// asset: a euro scheme clears through the bank's euro suspense.
	debtorAccts, err := debtor.AccountsFor(scheme.Asset())
	if err != nil {
		return err
	}
	debtorPos, err := debtor.positionTx(ctx, tx, p.Debtor.Account)
	if err != nil {
		return err
	}
	// The two legs of this one event take economic effect on different days, which
	// is why an entry carries its own value date.
	now := s.now()
	posted, err := debtor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":debit",
		Description:    p.Description,
		BookingDate:    now,
		ValueDate:      p.ValueDate,
		Metadata:       paymentMetadata(p),
		Entries: []ledger.Entry{
			{AccountID: debtorPos.Account, Subsidiary: debtorPos.Subsidiary, Amount: p.Amount, Direction: ledger.Debit, ValueDate: now},
			{AccountID: debtorAccts.Suspense, Amount: p.Amount, Direction: ledger.Credit, ValueDate: p.ValueDate},
		},
	})
	if err != nil {
		return err
	}
	p.DebtorLegTx = posted.ID
	return nil
}

// RejectAtCSM is RejectAtCSMTx in its own unit of work.
func (s *ClearingHouseNetwork) RejectAtCSM(ctx context.Context, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CsmTx) (Payment, error) {
		return s.RejectAtCSMTx(ctx, tx, id, code, reason)
	})
}

// RejectAtCSMTx is the CLEARING HOUSE's half of a rejection: it transitions the
// payment to Rejected with the code and reason it was refused for, drops it
// from whatever cycle it had been taken into, and records the event.
func (s *ClearingHouseNetwork) RejectAtCSMTx(ctx context.Context, tx CsmTx, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
	if err := ledger.ValidateText("reason", reason); err != nil {
		return Payment{}, err
	}
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.Status != Initiated && p.Status != Accepted {
		return Payment{}, ErrInvalidStateTransition
	}

	if err := s.removeFromCycleTx(ctx, tx, p); err != nil {
		return Payment{}, err
	}

	if err := s.transition(&p, Rejected); err != nil {
		return Payment{}, err
	}
	p.RejectCode = code
	p.RejectReason = reason
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentRejected, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// RejectAtBank is RejectAtBankTx in its own unit of work.
func (s *BankNetwork) RejectAtBank(ctx context.Context, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (Payment, error) {
		return s.RejectAtBankTx(ctx, tx, id, code, reason)
	})
}

// RejectAtBankTx is a member bank's half of a rejection: it records on this
// bank's OWN copy that the payment was refused, and — if this bank is the one
// holding the payer's money — gives it back.
func (s *BankNetwork) RejectAtBankTx(ctx context.Context, tx BankTx, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
	self, err := s.selfBIC()
	if err != nil {
		return Payment{}, err
	}
	if err := ledger.ValidateText("reason", reason); err != nil {
		return Payment{}, err
	}
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.DebtorDetails.Agent != self && p.CreditorDetails.Agent != self {
		return Payment{}, fmt.Errorf("%w: %s is between %s and %s, and this is %s",
			ErrNotThisBanksPayment, id, p.DebtorDetails.Agent, p.CreditorDetails.Agent, self)
	}
	if err := s.transition(&p, Rejected); err != nil {
		return Payment{}, fmt.Errorf("%s cannot reject %s, which this network records as %v: %w",
			self, p.ID, p.Status, err)
	}
	p.RejectCode = code
	p.RejectReason = reason
	// The payer's money, which only the payer's bank is holding. The reversal is
	// inside the same unit of work as the status, so neither can happen alone.
	if p.DebtorDetails.Agent == self {
		if err := s.ReverseDebtorLegTx(ctx, tx, p, reason); err != nil {
			return Payment{}, err
		}
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentRejected, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// ReverseDebtorLeg is ReverseDebtorLegTx in its own unit of work.
func (s *BankNetwork) ReverseDebtorLeg(ctx context.Context, p Payment, reason string) error {
	return s.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		return s.ReverseDebtorLegTx(ctx, tx, p, reason)
	})
}

// ReverseDebtorLegTx is the DEBTOR BANK's half of a rejection: it gives the
// payer their money back, by reversing the transaction that moved it into this
// bank's clearing suspense.
func (s *BankNetwork) ReverseDebtorLegTx(ctx context.Context, tx BankTx, p Payment, reason string) error {
	if p.DebtorLegTx == "" {
		return nil
	}
	debtor, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return err
	}
	_, err = debtor.Ledger.ReverseTransactionTx(ctx, tx, p.DebtorLegTx, "Reject payment "+string(p.ID)+": "+reason)
	return err
}

// ---------------------------------------------------------------------------
// The return, as three acts
// ---------------------------------------------------------------------------

// SettleReturn is SettleReturnTx in its own unit of work, which is what a
// settlement agent acting on a pacs.004 it has just been handed needs: the
// message is the whole of the input, and there is nothing else to commit with
// it.
func (s *CentralBankNetwork) SettleReturn(ctx context.Context, in ReturnInstruction) ([]SettlementStatement, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CentralBankTx) ([]SettlementStatement, error) {
		return s.SettleReturnTx(ctx, tx, in)
	})
}

// SettleReturnTx is the settlement agent's whole part in a return: one
// transaction in its own book, moving the reserves back, and one statement per
// member telling each what happened to its account.
func (s *CentralBankNetwork) SettleReturnTx(ctx context.Context, tx CentralBankTx, in ReturnInstruction) ([]SettlementStatement, error) {
	// Before anything is read, because this is a check on the KEY the posting
	// below will carry rather than on any account.
	book, err := s.centralBankBook()
	if err != nil {
		return nil, err
	}
	if in.PaymentID == "" {
		return nil, fmt.Errorf("payment: a return instruction naming no payment cannot be settled; its reserve reversal would be keyed by nothing")
	}
	// No bank row is read: the accounts are the settlement agent's own, from its own
	// member rows, keyed by the addresses the message carries. A bank it holds no
	// account for is refused with ErrSettlementMemberNotFound.
	debtorSettlement, err := s.settlementAccountTx(ctx, tx, in.DebtorAgent, in.Asset)
	if err != nil {
		return nil, err
	}
	creditorSettlement, err := s.settlementAccountTx(ctx, tx, in.CreditorAgent, in.Asset)
	if err != nil {
		return nil, err
	}

	// The redelivery check comes BEFORE the funding check, and the order is not
	// cosmetic.
	key := string(in.PaymentID) + ":return-settle"
	switch _, err := tx.GetTransactionByIdempotencyKey(ctx, CentralBankBook, key); {
	case err == nil:
		return nil, fmt.Errorf("%w: %s", ErrReturnAlreadySettled, in.PaymentID)
	case !errors.Is(err, ledger.ErrTransactionNotFound):
		return nil, err
	}

	held, err := book.BookBalanceTx(ctx, tx, creditorSettlement.Total())
	if err != nil {
		return nil, err
	}
	if held < in.Amount {
		return nil, fmt.Errorf("%w: %s is short %d in %s",
			ledger.ErrInsufficientBalance, in.CreditorAgent, in.Amount-held, in.Asset)
	}

	// The key is checked twice, and the second one is the ledger's. The read above is
	// what makes the ANSWER right; this is what makes the refusal binding, because two
	// deliveries in flight at once both pass a read and only one may post.
	if _, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: key,
		Description:    "Return settlement for payment " + string(in.PaymentID),
		Entries: []ledger.Entry{
			{AccountID: creditorSettlement, Amount: in.Amount, Direction: ledger.Debit},
			{AccountID: debtorSettlement, Amount: in.Amount, Direction: ledger.Credit},
		},
	}); err != nil {
		if errors.Is(err, ledger.ErrDuplicateIdempotencyKey) {
			return nil, fmt.Errorf("%w: %s", ErrReturnAlreadySettled, in.PaymentID)
		}
		return nil, err
	}

	// What each member is TOLD. The balances are read AFTER the posting and inside
	// the same unit of work, which is what makes them CLOSING balances; reading
	// them before would produce opening balances labelled CLBD.
	now := s.now()
	statements := make([]SettlementStatement, 0, 2)
	for _, side := range []struct {
		agent    iso20022.BIC
		account  ledger.AccountID
		movement ledger.Amount
	}{
		{in.CreditorAgent, creditorSettlement, -in.Amount},
		{in.DebtorAgent, debtorSettlement, in.Amount},
	} {
		closing, err := book.BookBalanceTx(ctx, tx, side.account.Total())
		if err != nil {
			return nil, err
		}
		statements = append(statements, SettlementStatement{
			Agent:          side.agent,
			Account:        side.account,
			Asset:          in.Asset,
			Reference:      string(in.PaymentID),
			StatementRef:   string(in.PaymentID) + ":rtr",
			Movement:       side.movement,
			ClosingBalance: closing,
			ValueDate:      now,
		})
	}
	return statements, nil
}

// PostReturnLeg is PostReturnLegTx in its own unit of work, which is what a
// bank acting on a return needs: one message names one payment, and there is
// nothing else to commit with it.
func (s *BankNetwork) PostReturnLeg(ctx context.Context, id PaymentID, reason string) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (Payment, error) {
		return s.PostReturnLegTx(ctx, tx, id, reason)
	})
}

// PostReturnLegTx is a bank posting its own customer leg of a return, in its
// own book.
func (s *BankNetwork) PostReturnLegTx(ctx context.Context, tx BankTx, id PaymentID, reason string) (Payment, error) {
	self, err := s.selfBIC()
	if err != nil {
		return Payment{}, err
	}
	if err := ledger.ValidateText("reason", reason); err != nil {
		return Payment{}, err
	}
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	scheme, ok := s.scheme(p.Scheme)
	if !ok || !scheme.AllowsReturn() {
		return Payment{}, ErrSchemeUnsupportedReturn
	}
	if p.Status != Settled {
		return Payment{}, ErrInvalidStateTransition
	}
	bank, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return Payment{}, err
	}
	accts, err := bank.AccountsFor(scheme.Asset())
	if err != nil {
		return Payment{}, err
	}

	// Whether the CLAWBACK may be refused, as a property of the LEG rather than of
	// the actor.
	mayRefuse := scheme.Direction() == Push && ReturnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent) == self

	// Has this bank's leg posted, AND does that posting still STAND?
	var clawbackStands, refundStands bool
	if self == p.CreditorDetails.Agent {
		if clawbackStands, err = s.legStandsTx(ctx, tx, bank, p.ReturnClawbackTx); err != nil {
			return Payment{}, err
		}
	}
	if self == p.DebtorDetails.Agent {
		if refundStands, err = s.legStandsTx(ctx, tx, bank, p.ReturnRefundTx); err != nil {
			return Payment{}, err
		}
	}

	var posted ledger.Transaction
	switch {
	case self == p.CreditorDetails.Agent && !clawbackStands:
		posted, err = s.clawbackTx(ctx, tx, bank, accts, p, reason, mayRefuse, p.ReturnClawbackTx)
		if err != nil {
			return Payment{}, err
		}
		p.ReturnClawbackTx = posted.ID
	case self == p.DebtorDetails.Agent && !refundStands:
		posted, err = s.refundTx(ctx, tx, bank, accts, p, reason, p.ReturnRefundTx)
		if err != nil {
			return Payment{}, err
		}
		p.ReturnRefundTx = posted.ID
	case self == p.CreditorDetails.Agent || self == p.DebtorDetails.Agent:
		return p, nil
	default:
		return Payment{}, fmt.Errorf("%w: %s is neither %s's payer nor its payee", ErrNotAPartyToThisReturn, self, id)
	}

	p.RejectReason = reason
	// WHICH of the two legs this is, and therefore whether the return is over.
	onUs := p.DebtorDetails.Agent == p.CreditorDetails.Agent
	done := ReturnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent) != self
	if onUs {
		done = p.ReturnClawbackTx != "" && p.ReturnRefundTx != ""
	}
	if done {
		if err := s.transition(&p, Returned); err != nil {
			return Payment{}, err
		}
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if p.Status == Returned {
		if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentReturned, string(p.ID), p); err != nil {
			return Payment{}, err
		}
	}
	return p, nil
}

// clawbackTx is the creditor bank's leg: the money comes out of wherever its
// creditor leg put it and into its clearing suspense, on its way back across
// the network.
func (s *BankNetwork) clawbackTx(ctx context.Context, tx BankTx, creditor *Bank, accts BankAccounts,
	p Payment, reason string, mayRefuse bool, replacing ledger.TransactionID,
) (ledger.Transaction, error) {
	// Where the money actually is, READ OFF THE PAYMENT rather than resolved
	// again. Only SettleAtBankTx can know which account it credited, and only at
	// the moment it posted.
	from, description := p.CreditorLegAccount, "Return of payment "+string(p.ID)+": "+reason
	if from != accts.Unclaimed {
		err := creditor.Deposit.CheckWithdrawalTx(ctx, tx, p.Creditor.Account, p.Amount)
		switch {
		case err == nil:
		case mayRefuse:
			// The push side. Nothing is posted and no message is built, which
			// is the whole of the refusal.
			return ledger.Transaction{}, err
		case errors.Is(err, deposit.ErrAccountClosed):
			// The account cannot be posted to at all, so the bank funds the refund
			// itself and books what it is owed.
			from, description = accts.ReturnsReceivable, "Returns receivable: "+description
		case errors.Is(err, deposit.ErrInsufficientAvailable),
			errors.Is(err, deposit.ErrAccountDormant),
			errors.Is(err, deposit.ErrAccountFrozen):
			// The account can still be posted to, so it is: the biller goes overdrawn. A
			// freeze is a block on the CUSTOMER's withdrawals and not on the bank honouring
			// a scheme obligation, and a dormant account is one nobody has touched.
		default:
			// A store failure. It is not a statement about the account, and the two arms
			// above are choices about where a customer's money goes.
			return ledger.Transaction{}, err
		}
	}
	// Whose obligation is being released, on the two arms that reach a control
	// account.
	position := from.For(unclaimedSubsidiary(p))
	if from == accts.ReturnsReceivable {
		position = from.Total()
	}
	return creditor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: returnLegKey(p.ID, "return-claw", replacing),
		Description:    description,
		Metadata:       paymentMetadata(&p),
		Entries: []ledger.Entry{
			{AccountID: position.Account, Subsidiary: position.Subsidiary, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Credit},
		},
	})
}

// refundTx is the debtor bank's leg: the payer is paid back out of this bank's
// clearing suspense, which the reserves coming back from the central bank fill.
func (s *BankNetwork) refundTx(ctx context.Context, tx BankTx, debtor *Bank, accts BankAccounts,
	p Payment, reason string, replacing ledger.TransactionID,
) (ledger.Transaction, error) {
	description := "Return of payment " + string(p.ID) + ": " + reason
	// The payer names the subsidiary on both arms, and here that is the DEBTOR's
	// account rather than the creditor's: this is the payer's own bank, holding
	// either the payer's money or a refund their closed account would not take.
	to := accts.Unclaimed.For(string(p.Debtor.Account))

	// Unless the payer was never debited, which is a collection this bank SETTLED
	// and could not fund from its customer: it stood in for them
	// (ReceiveUnappliedTx) and what the return discharges is its own claim, not a
	// refund it owes anybody.
	if p.DebtorLegTx == "" {
		return debtor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			IdempotencyKey: returnLegKey(p.ID, "return-refund", replacing),
			Description:    "Returns receivable: " + description,
			Metadata:       paymentMetadata(&p),
			Entries: []ledger.Entry{
				{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Debit},
				{AccountID: accts.ReturnsReceivable, Amount: p.Amount, Direction: ledger.Credit},
			},
		})
	}

	err := debtor.Deposit.CheckCreditTx(ctx, tx, p.Debtor.Account)
	switch {
	case err == nil:
		// The payer's own position, resolved only once the register has said the
		// account can take the credit.
		if to, err = debtor.positionTx(ctx, tx, p.Debtor.Account); err != nil {
			return ledger.Transaction{}, err
		}
	case errors.Is(err, deposit.ErrAccountClosed):
		description = "Unclaimed: " + description
	default:
		return ledger.Transaction{}, err
	}
	return debtor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: returnLegKey(p.ID, "return-refund", replacing),
		Description:    description,
		Metadata:       paymentMetadata(&p),
		Entries: []ledger.Entry{
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: to.Account, Subsidiary: to.Subsidiary, Amount: p.Amount, Direction: ledger.Credit},
		},
	})
}

// legStandsTx reports whether a return leg id names a posting that is still
// standing in this bank's own book.
func (s *BankNetwork) legStandsTx(ctx context.Context, tx BankTx, bank *Bank, leg ledger.TransactionID) (bool, error) {
	if leg == "" {
		return false, nil
	}
	txn, err := tx.GetTransaction(ctx, bank.BookID, leg)
	if err != nil {
		return false, err
	}
	return txn.Status != ledger.Reversed, nil
}

// returnLegKey is the idempotency key one bank's return leg posts under.
func returnLegKey(id PaymentID, leg string, replacing ledger.TransactionID) string {
	key := string(id) + ":" + leg
	if replacing != "" {
		key += ":" + string(replacing)
	}
	return key
}

// CompleteReturn is CompleteReturnTx in its own unit of work, on each of the two
// institutions that keep a copy of the payment. See CompleteReturnTx for which
// two, and why the third needs nothing.
func (s *BankNetwork) CompleteReturn(ctx context.Context, id PaymentID) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) (Payment, error) {
		return s.CompleteReturnTx(ctx, tx, id)
	})
}

func (s *ClearingHouseNetwork) CompleteReturn(ctx context.Context, id PaymentID) (Payment, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx CsmTx) (Payment, error) {
		return s.CompleteReturnTx(ctx, tx, id)
	})
}

// CompleteReturnTx marks this institution's own copy Returned on being told the
// return settled. It posts nothing.
func (s *Network) CompleteReturnTx(ctx context.Context, tx partyTx, id PaymentID) (Payment, error) {
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.Status == Returned {
		return p, nil
	}
	if err := s.transition(&p, Returned); err != nil {
		return Payment{}, err
	}
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentReturned, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// ReverseReturnLeg is ReverseReturnLegTx in its own unit of work.
func (s *BankNetwork) ReverseReturnLeg(ctx context.Context, id PaymentID, reason string) error {
	return s.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		return s.ReverseReturnLegTx(ctx, tx, id, reason)
	})
}

// ReverseReturnLegTx unwinds a return leg this bank posted and that the network
// then refused.
func (s *BankNetwork) ReverseReturnLegTx(ctx context.Context, tx BankTx, id PaymentID, reason string) error {
	self, err := s.selfBIC()
	if err != nil {
		return err
	}
	if err := ledger.ValidateText("reason", reason); err != nil {
		return err
	}
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != Settled {
		return ErrInvalidStateTransition
	}
	var leg ledger.TransactionID
	switch self {
	case p.CreditorDetails.Agent:
		leg = p.ReturnClawbackTx
	case p.DebtorDetails.Agent:
		leg = p.ReturnRefundTx
	default:
		return fmt.Errorf("%w: %s is neither %s's payer nor its payee", ErrNotAPartyToThisReturn, self, id)
	}
	if leg == "" {
		return nil
	}
	bank, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return err
	}
	_, err = bank.Ledger.ReverseTransactionTx(ctx, tx, leg, "Rejected return of payment "+string(p.ID)+": "+reason)
	return err
}

// ---------------------------------------------------------------------------
// Read accessors
// ---------------------------------------------------------------------------

// GetPayment returns this institution's own copy of a payment. Like
// ListPayments it is one method per institution that keeps payment rows, and the
// settlement agent keeps none.
func (s *BankNetwork) GetPayment(ctx context.Context, id PaymentID) (Payment, error) {
	var out Payment
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = tx.GetPayment(ctx, id)
		return err
	})
	return out, err
}

func (s *ClearingHouseNetwork) GetPayment(ctx context.Context, id PaymentID) (Payment, error) {
	var out Payment
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		var err error
		out, err = tx.GetPayment(ctx, id)
		return err
	})
	return out, err
}

// GetCycle returns a clearing cycle by ID.
func (s *ClearingHouseNetwork) GetCycle(ctx context.Context, id CycleID) (ClearingCycle, error) {
	var out ClearingCycle
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		var err error
		out, err = tx.GetCycle(ctx, id)
		return err
	})
	return out, err
}

// GetMandate returns one of THIS bank's mandates by ID.
func (s *BankNetwork) GetMandate(ctx context.Context, id MandateID) (Mandate, error) {
	if _, err := s.self(); err != nil {
		return Mandate{}, fmt.Errorf("%w: %w", ErrNotThisBanksMandate, err)
	}
	var out Mandate
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = tx.GetMandate(ctx, id)
		return err
	})
	return out, err
}

// ReserveBalance returns a member's reserve book balance in one asset, as held
// at the central bank. Central-bank settlement accounts are plain GL accounts
// with no deposit layer, so this is the GL book balance.
func (s *CentralBankNetwork) ReserveBalance(ctx context.Context, bic iso20022.BIC, asset ledger.AssetCode) (ledger.Amount, error) {
	book, err := s.centralBankBook()
	if err != nil {
		return 0, err
	}
	var out ledger.Amount
	err = s.store.View(ctx, func(ctx context.Context, tx CentralBankTx) error {
		settlement, err := s.settlementAccountTx(ctx, tx, bic, asset)
		if err != nil {
			return err
		}
		out, err = book.BookBalanceTx(ctx, tx, settlement.Total())
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// transition moves THIS INSTITUTION's copy of a payment to a new status if the
// edge is legal for the institution making it.
func (s *Network) transition(p *Payment, to PaymentStatus) error {
	allowed := map[PaymentStatus][]PaymentStatus{
		Initiated: {Accepted, Rejected},
		Accepted:  {Cleared, Rejected},
		Cleared:   {Settled},
		Settled:   {Returned},
	}
	if s.id.role == roleBank {
		allowed = map[PaymentStatus][]PaymentStatus{
			Initiated: {Accepted, Rejected, Settled},
			Accepted:  {Rejected, Settled},
			Settled:   {Returned},
		}
	}
	for _, ok := range allowed[p.Status] {
		if ok == to {
			p.Status = to
			return nil
		}
	}
	return ErrInvalidStateTransition
}

// validateParty checks a party reference's text before anything is looked up
// with it.
func validateParty(field string, ref PartyRef) error {
	if err := ledger.ValidateText(field+".account", string(ref.Account)); err != nil {
		return err
	}
	// An empty identifier is legal — an internal transfer quotes no external
	// address — so validate only what is there.
	if ref.Identifier == (deposit.Identifier{}) {
		return nil
	}
	return ref.Identifier.Validate(field + ".identifier")
}

// ResolveIdentifier turns an external address — an IBAN today — into the party
// it names, WITHIN THIS BANK's register. The bank asking is this network's own
// identity, and the answer is about its own customers or there is no answer.
func (s *BankNetwork) ResolveIdentifier(ctx context.Context, ident deposit.Identifier) (PartyRef, error) {
	return unit.Run(ctx, s.store.View, func(ctx context.Context, tx BankTx) (PartyRef, error) {
		return s.ResolveIdentifierTx(ctx, tx, ident)
	})
}

// ResolveIdentifierTx is ResolveIdentifier within a caller-supplied unit of
// work.
func (s *BankNetwork) ResolveIdentifierTx(ctx context.Context, tx BankTx, ident deposit.Identifier) (PartyRef, error) {
	if err := ident.Validate("identifier"); err != nil {
		return PartyRef{}, err
	}
	bank, err := s.selfBankTx(ctx, tx)
	if err != nil {
		return PartyRef{}, err
	}
	holders, err := tx.ListDepositAccountsByIdentifier(ctx, bank.BookID, ident)
	if err != nil {
		return PartyRef{}, err
	}
	switch len(holders) {
	case 0:
		return PartyRef{}, deposit.ErrIdentifierNotFound
	case 1:
		// The bank is not on the answer, because a PartyRef names none and because
		// there would be nothing to learn from it: this searched THIS bank's register,
		// so the only bank it could name is the one that asked.
		return PartyRef{Account: holders[0].ID, Identifier: ident}, nil
	default:
		return PartyRef{}, deposit.ErrIdentifierAmbiguous
	}
}

// checkPartyTx verifies that a deposit account exists in THIS bank's register,
// returning both the account and the bound bank so callers that need more than
// existence — the account's Asset, the bank's live handles — do not have to
// fetch either again.
func (s *BankNetwork) checkPartyTx(ctx context.Context, tx BankTx, field string, ref PartyRef) (deposit.Account, *Bank, error) {
	if err := validateParty(field, ref); err != nil {
		return deposit.Account{}, nil, err
	}
	self, err := s.self()
	if err != nil {
		return deposit.Account{}, nil, err
	}
	rec, err := tx.GetBank(ctx, self)
	if errors.Is(err, ErrParticipantNotFound) {
		return deposit.Account{}, nil, fmt.Errorf("%w: %s", ErrParticipantNotFound, self)
	}
	if err != nil {
		return deposit.Account{}, nil, err
	}
	acct, err := tx.GetDepositAccount(ctx, rec.BookID, ref.Account)
	if errors.Is(err, deposit.ErrAccountNotFound) {
		return deposit.Account{}, nil, fmt.Errorf("%w: %s", ErrAccountNotInParticipant, ref.Account)
	}
	if err != nil {
		return deposit.Account{}, nil, err
	}
	return acct, s.bind(rec), nil
}

// addressFor settles which external address one leg of a payment records, and
// refuses the leg when the scheme cannot address its account at all.
func addressFor(scheme Scheme, ref PartyRef, acct deposit.Account) (deposit.Identifier, error) {
	want := scheme.AddressedBy()
	var inScheme []deposit.Identifier
	for _, ident := range acct.Identifiers {
		if ident.Scheme == want {
			inScheme = append(inScheme, ident)
		}
	}
	if len(inScheme) == 0 {
		return deposit.Identifier{}, ErrUnaddressableAccount
	}
	if ref.Identifier == (deposit.Identifier{}) {
		if len(inScheme) > 1 {
			return deposit.Identifier{}, ErrAmbiguousAddress
		}
		return inScheme[0], nil
	}
	// Matches and not ==, and the payment records the account's STORED form.
	for _, ident := range inScheme {
		if ident.Matches(ref.Identifier) {
			return ident, nil
		}
	}
	return deposit.Identifier{}, ErrIdentifierMismatch
}

// removeFromCycleTx drops a payment from its (open) clearing cycle.
func (s *ClearingHouseNetwork) removeFromCycleTx(ctx context.Context, tx CsmTx, p Payment) error {
	c, err := tx.GetCycle(ctx, p.CycleID)
	if errors.Is(err, ErrCycleNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	out := c.PaymentIDs[:0]
	for _, pid := range c.PaymentIDs {
		if pid != p.ID {
			out = append(out, pid)
		}
	}
	c.PaymentIDs = out
	return tx.PutCycle(ctx, c)
}

func paymentMetadata(p *Payment) map[string]string {
	md := map[string]string{
		"payment_id": string(p.ID),
		"scheme":     string(p.Scheme),
	}
	if p.EndToEndID != "" {
		md["end_to_end_id"] = p.EndToEndID
	}
	if p.MandateID != "" {
		md["mandate_id"] = string(p.MandateID)
	}
	return md
}

func copyPositions(in map[iso20022.BIC]ledger.Amount) map[iso20022.BIC]ledger.Amount {
	out := make(map[iso20022.BIC]ledger.Amount, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
