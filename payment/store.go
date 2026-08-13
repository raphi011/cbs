package payment

import (
	"context"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// The payment layer's persistent state, declared here by the consumer and
// implemented by store/sqlite — so the store package imports the domain
// packages and never the reverse.

// CommonStore is the unit of work every institution can open, whatever tables
// it has.
type CommonStore interface {
	Update(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error
	View(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// BankStore owns one member bank's database: its ledger, its deposit register,
// its products, its loans, its own row in banks, its copy of the routing
// directory, the mandates it holds as creditor bank, its copy of each payment
// it is a party to, and the advices it was sent.
type BankStore interface {
	Update(ctx context.Context, fn func(context.Context, BankTx) error) error
	View(ctx context.Context, fn func(context.Context, BankTx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// ClearingHouseStore owns the clearing house's database: the roster, the cycles,
// its copy of each payment it carries, and the files and returns it has taken in
// and not yet handed over. No book of accounts of any kind.
type ClearingHouseStore interface {
	Update(ctx context.Context, fn func(context.Context, CsmTx) error) error
	View(ctx context.Context, fn func(context.Context, CsmTx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// CentralBankStore owns the settlement agent's database: the ledger holding the
// members' reserve accounts, its member register, the bank codes it allocates
// and the settlements it discharged. No customers and no payments.
type CentralBankStore interface {
	Update(ctx context.Context, fn func(context.Context, CentralBankTx) error) error
	View(ctx context.Context, fn func(context.Context, CentralBankTx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Stores is the SET of databases one process holds: one per member bank, the
// clearing house's, and the central bank's. It is what Networks is built over.
type Stores interface {
	// Bank returns the store holding one member bank's database, opening it on the
	// first ask and returning the same handle thereafter.
	Bank(ctx context.Context, bic iso20022.BIC) (BankStore, error)

	// Banks is every member bank this set holds a database for, ascending by
	// address so that two calls agree and a restart plans the same listeners.
	Banks(ctx context.Context) ([]iso20022.BIC, error)

	// ClearingHouse and CentralBank are the two institutions that are
	// configuration rather than data: there is exactly one of each, it exists
	// before any bank does, and neither can fail to be there.
	ClearingHouse() ClearingHouseStore
	CentralBank() CentralBankStore

	// Reset empties every store in the set, so the system behaves like a freshly
	// migrated one.
	Reset(ctx context.Context) error

	// Close closes every store in the set. It is idempotent, for the reason
	// Store.Close is: a test closes what it hands to a suite that closes it too.
	Close() error
}

// ---------------------------------------------------------------------------
// The capabilities two institutions share
// ---------------------------------------------------------------------------

// PaymentRowsTx is a party's own copy of the payments it is on: a bank's and
// the clearing house's, and no third institution's.
type PaymentRowsTx interface {
	PutPayment(ctx context.Context, p Payment) error
	GetPayment(ctx context.Context, id PaymentID) (Payment, error)
	GetPaymentByEndToEndID(ctx context.Context, endToEndID string) (Payment, error)
	ListPayments(ctx context.Context) ([]Payment, error)
}

// partyTx is what an institution that is a PARTY to a payment can reach: its
// own copy of the row, and the audit trail every institution has.
type partyTx interface {
	PaymentRowsTx
	ledger.CommonTx
}

// ---------------------------------------------------------------------------
// One transaction type per institution
// ---------------------------------------------------------------------------

// BankTx is one member bank's unit of work.
type BankTx interface {
	deposit.Tx
	lending.Tx
	PaymentRowsTx
	MessageLogTx

	// A bank's own record of ITSELF, and the only row in this table.
	PutBank(ctx context.Context, b Bank) error
	GetBank(ctx context.Context, id ParticipantID) (Bank, error)
	ListBanks(ctx context.Context) ([]Bank, error)

	// A MEMBER BANK's copy of the clearing house's roster, in that bank's own
	// database. See DirectoryEntry.
	ReplaceRoutingDirectory(ctx context.Context, entries []DirectoryEntry) error
	GetDirectoryEntry(ctx context.Context, issuer iban.Issuer) (DirectoryEntry, error)
	ListDirectoryEntries(ctx context.Context) ([]DirectoryEntry, error)

	// The mandates this bank holds as CREDITOR bank. A mandate is the creditor's
	// bank's row (see Mandate), so the row set is this institution's by
	// construction and there is no creditor to filter on.
	PutMandate(ctx context.Context, m Mandate) error
	GetMandate(ctx context.Context, id MandateID) (Mandate, error)
	ListMandates(ctx context.Context) ([]Mandate, error)

	// The advice rows are BOOK-SCOPED, unlike every other method in this
	// interface.
	PutSettlementAdvice(ctx context.Context, book ledger.BookID, a SettlementAdvice) error
	GetSettlementAdvice(ctx context.Context, book ledger.BookID, reference string, asset ledger.AssetCode) (SettlementAdvice, error)
	ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]SettlementAdvice, error)
}

// CsmTx is the clearing house's unit of work.
type CsmTx interface {
	ledger.CommonTx
	PaymentRowsTx
	MessageLogTx

	// The cycles this clearing house opens, closes and settles.
	PutCycle(ctx context.Context, c ClearingCycle) error
	GetCycle(ctx context.Context, id CycleID) (ClearingCycle, error)
	GetOpenCycle(ctx context.Context, scheme SchemeID) (ClearingCycle, error)
	ListCycles(ctx context.Context) ([]ClearingCycle, error)

	// WHO IS A MEMBER, which is this institution's register and nobody else's.
	PutRosterEntry(ctx context.Context, e RosterEntry) error
	GetRosterEntry(ctx context.Context, bic iso20022.BIC) (RosterEntry, error)
	GetRosterEntryByIssuer(ctx context.Context, issuer iban.Issuer) (RosterEntry, error)
	ListRosterEntries(ctx context.Context) ([]RosterEntry, error)

	// The clearing house's unfinished business: see HeldFile and HeldReturn. There
	// is no institution in which both a book of accounts and a held file exist.
	AddHeldFile(ctx context.Context, f HeldFile) error
	ListHeldFiles(ctx context.Context, id CycleID) ([]HeldFile, error)
	DeleteHeldFile(ctx context.Context, id CycleID, seq int64) error

	PutHeldReturn(ctx context.Context, r HeldReturn) error
	GetHeldReturn(ctx context.Context, id PaymentID) (HeldReturn, error)
	DeleteHeldReturn(ctx context.Context, id PaymentID) error
}

// CentralBankTx is the settlement agent's unit of work.
type CentralBankTx interface {
	ledger.Tx
	MessageLogTx

	// The settlement agent's record of the account it opened for a member, keyed
	// by BIC for the reason a bank's own row is: the BIC is the only identifier
	// that crosses an institutional boundary here.
	PutSettlementMember(ctx context.Context, m SettlementMember) error
	GetSettlementMember(ctx context.Context, bic iso20022.BIC) (SettlementMember, error)
	ListSettlementMembers(ctx context.Context) ([]SettlementMember, error)

	// The settlement agent's SECOND register, and the one no bank ever reads:
	// which institution holds which bank code. See BankCodeAllocation.
	PutBankCode(ctx context.Context, a BankCodeAllocation) error
	GetBankCode(ctx context.Context, issuer iban.Issuer) (BankCodeAllocation, error)
	GetBankCodeForBIC(ctx context.Context, country iban.Country, bic iso20022.BIC) (BankCodeAllocation, error)
	ListBankCodes(ctx context.Context) ([]BankCodeAllocation, error)
	NextBankCodeSerial(ctx context.Context, book ledger.BookID, country iban.Country) (uint64, error)

	// The cut-offs this agent has discharged.
	PutSettlement(ctx context.Context, s Settlement) error
	GetSettlement(ctx context.Context, id SettlementID) (Settlement, error)
	GetSettlementByCycle(ctx context.Context, id CycleID) (Settlement, error)
	ListSettlements(ctx context.Context) ([]Settlement, error)
}

// Contract notes for implementers.

// ---------------------------------------------------------------------------
// Narrower views of the same store
// ---------------------------------------------------------------------------

// Every view below re-types a store's callback and holds no state of its own.

// bankCommon and the two beside it present an institution's store as the
// CommonStore the Network core keeps — the audit trail and the counters, which
// every schema holds.
type bankCommon struct{ BankStore }

var _ CommonStore = bankCommon{}

func (v bankCommon) Update(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error {
	return v.BankStore.Update(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

func (v bankCommon) View(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error {
	return v.BankStore.View(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

type clearingHouseCommon struct{ ClearingHouseStore }

var _ CommonStore = clearingHouseCommon{}

func (v clearingHouseCommon) Update(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error {
	return v.ClearingHouseStore.Update(ctx, func(ctx context.Context, tx CsmTx) error { return fn(ctx, tx) })
}

func (v clearingHouseCommon) View(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error {
	return v.ClearingHouseStore.View(ctx, func(ctx context.Context, tx CsmTx) error { return fn(ctx, tx) })
}

type centralBankCommon struct{ CentralBankStore }

var _ CommonStore = centralBankCommon{}

func (v centralBankCommon) Update(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error {
	return v.CentralBankStore.Update(ctx, func(ctx context.Context, tx CentralBankTx) error { return fn(ctx, tx) })
}

func (v centralBankCommon) View(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error {
	return v.CentralBankStore.View(ctx, func(ctx context.Context, tx CentralBankTx) error { return fn(ctx, tx) })
}

// bankMessages and the two beside it present an institution's store as the
// MessageLogStore the Network core keeps. Three adapters rather than one,
// because Go allows one Update method per type.
type bankMessages struct{ BankStore }

var _ MessageLogStore = bankMessages{}

func (v bankMessages) Update(ctx context.Context, fn func(context.Context, MessageLogTx) error) error {
	return v.BankStore.Update(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

func (v bankMessages) View(ctx context.Context, fn func(context.Context, MessageLogTx) error) error {
	return v.BankStore.View(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

type clearingHouseMessages struct{ ClearingHouseStore }

var _ MessageLogStore = clearingHouseMessages{}

func (v clearingHouseMessages) Update(ctx context.Context, fn func(context.Context, MessageLogTx) error) error {
	return v.ClearingHouseStore.Update(ctx, func(ctx context.Context, tx CsmTx) error { return fn(ctx, tx) })
}

func (v clearingHouseMessages) View(ctx context.Context, fn func(context.Context, MessageLogTx) error) error {
	return v.ClearingHouseStore.View(ctx, func(ctx context.Context, tx CsmTx) error { return fn(ctx, tx) })
}

type centralBankMessages struct{ CentralBankStore }

var _ MessageLogStore = centralBankMessages{}

func (v centralBankMessages) Update(ctx context.Context, fn func(context.Context, MessageLogTx) error) error {
	return v.CentralBankStore.Update(ctx, func(ctx context.Context, tx CentralBankTx) error { return fn(ctx, tx) })
}

func (v centralBankMessages) View(ctx context.Context, fn func(context.Context, MessageLogTx) error) error {
	return v.CentralBankStore.View(ctx, func(ctx context.Context, tx CentralBankTx) error { return fn(ctx, tx) })
}

// ledgerView presents a bank's store as the ledger.Store a Book takes.
type ledgerView struct{ BankStore }

var _ ledger.Store = ledgerView{}

func (v ledgerView) Update(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return v.BankStore.Update(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

func (v ledgerView) View(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return v.BankStore.View(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

// centralBankLedgerView presents the settlement agent's store as a ledger.Store,
// for the Book holding the members' reserve accounts.
type centralBankLedgerView struct{ CentralBankStore }

var _ ledger.Store = centralBankLedgerView{}

func (v centralBankLedgerView) Update(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return v.CentralBankStore.Update(ctx, func(ctx context.Context, tx CentralBankTx) error { return fn(ctx, tx) })
}

func (v centralBankLedgerView) View(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return v.CentralBankStore.View(ctx, func(ctx context.Context, tx CentralBankTx) error { return fn(ctx, tx) })
}

// depositView presents a bank's store as a deposit.Store.
type depositView struct{ BankStore }

var _ deposit.Store = depositView{}

func (v depositView) Update(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return v.BankStore.Update(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

func (v depositView) View(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return v.BankStore.View(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

// productView presents a bank's store as a product.Store.
type productView struct{ BankStore }

var _ product.Store = productView{}

func (v productView) Update(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return v.BankStore.Update(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

func (v productView) View(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return v.BankStore.View(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

// lendingView presents a bank's store as a lending.Store.
type lendingView struct{ BankStore }

var _ lending.Store = lendingView{}

func (v lendingView) Update(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return v.BankStore.Update(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}

func (v lendingView) View(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return v.BankStore.View(ctx, func(ctx context.Context, tx BankTx) error { return fn(ctx, tx) })
}
