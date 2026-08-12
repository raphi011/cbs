package payment

import (
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// The three kinds of institution a deployment holds, each as its own type.
type core = Network

// BankNetwork is one member bank's handle: the acts whose subject is this
// bank's own book, its own register, its own customers and its own half of a
// payment.
type BankNetwork struct {
	core

	// store is this bank's own database.
	store    BankStore
	ledgers  ledger.Store
	deposits deposit.Store
	lendings lending.Store
	products product.Store
}

// ClearingHouseNetwork is the CSM's handle: it clears, it nets, it routes, and
// it holds no book of accounts at all.
type ClearingHouseNetwork struct {
	core

	// store is the clearing house's own database, and there is no ledger view
	// beside it: its schema creates no books, no accounts and no entries.
	store ClearingHouseStore
}

// CentralBankNetwork is the settlement agent's handle, and the only one holding
// the central bank's book of accounts.
type CentralBankNetwork struct {
	core

	// store is the settlement agent's own database, and ledgers is the same
	// database as the ledger.Store a Book takes — the narrower of the two ledger
	// views, since this schema creates no slot mapping.
	store   CentralBankStore
	ledgers ledger.Store

	// centralBank holds the members' reserve accounts.
	centralBank *ledger.Book
}

// The three constructors, for a caller building ONE institution over a store it
// already holds.

func NewBankNetwork(store BankStore, clock func() time.Time, pid ParticipantID) *BankNetwork {
	return newBankNetwork(store, clock, pid, newSchemeRegistry())
}

func NewClearingHouseNetwork(store ClearingHouseStore, clock func() time.Time) *ClearingHouseNetwork {
	return newClearingHouseNetwork(store, clock, newSchemeRegistry())
}

func NewCentralBankNetwork(store CentralBankStore, clock func() time.Time) *CentralBankNetwork {
	return newCentralBankNetwork(store, clock, newSchemeRegistry())
}

// The three assemblers, with the scheme registry supplied. Networks hands one
// registry to every institution it mints; see schemeRegistry.

func newBankNetwork(store BankStore, clock func() time.Time, pid ParticipantID, schemes *schemeRegistry) *BankNetwork {
	return &BankNetwork{
		core:     newNetwork(bankCommon{store}, clock, AsBank(pid), schemes),
		store:    store,
		ledgers:  ledgerView{store},
		deposits: depositView{store},
		lendings: lendingView{store},
		products: productView{store},
	}
}

func newClearingHouseNetwork(store ClearingHouseStore, clock func() time.Time, schemes *schemeRegistry) *ClearingHouseNetwork {
	return &ClearingHouseNetwork{
		core:  newNetwork(clearingHouseCommon{store}, clock, AsClearingHouse(), schemes),
		store: store,
	}
}

func newCentralBankNetwork(store CentralBankStore, clock func() time.Time, schemes *schemeRegistry) *CentralBankNetwork {
	ledgers := centralBankLedgerView{store}
	return &CentralBankNetwork{
		core:        newNetwork(centralBankCommon{store}, clock, AsCentralBank(), schemes),
		store:       store,
		ledgers:     ledgers,
		centralBank: ledger.NewBook(ledgers, CentralBankBook, clock),
	}
}
