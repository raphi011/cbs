package payment

import (
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// The three kinds of institution a deployment holds, each as its own type.
//
// # What the type answers
//
// An act that belongs to one institution is a method on that institution's type
// and on no other, so a clearing house asked to discharge a cut-off does not
// COMPILE. Since the store split underneath, neither does a clearing house asked
// to read a deposit account: each type holds ITS OWN store, whose unit of work
// names only the tables that institution's schema creates.
//
// Identity still answers at runtime, and still has something to answer. Both
// guards left — Network.self and Network.clearingHouse — catch a handle whose
// methods and whose identity disagree, which is a shape only this package can
// assemble (see core below), and neither guard can see a book id passed as an
// ordinary argument.
//
// # What Network keeps
//
// Each type embeds Network, which carries what all three institutions have: the
// clock, the identity, the shared scheme registry, the audit trail, and the acts
// that read nothing at all — rendering a message from a payment, resolving a
// scheme.
//
// What it does NOT carry is a payment's row. Two institutions keep one and the
// settlement agent keeps none, so GetPayment and ListPayments are a method on
// each of the two rather than one on the core; CompleteReturnTx, which both
// perform identically, takes the shared capability as its argument.
//
// Networks is the only thing that mints these, and it mints one per institution
// over that institution's own database. Nothing downstream holds a second.
//
// # Why the embedded field is spelled core
//
// An embedded field takes the name of its type, and the identity lives on the
// core rather than on the three types above it. Spelled Network, a composite
// literal in any package could give a bank's handle the clearing house's core
// and get a handle whose methods and whose identity disagree — the one crossing
// the types would otherwise still permit, and the reason Network.self and
// Network.centralBankBook still refuse rather than merely fetch.
//
// Spelled core, only this package can assemble one. See export_test.go, which
// hands the four mis-wired pairings to the suites that measure those refusals
// and to nothing else.
type core = Network

// BankNetwork is one member bank's handle: the acts whose subject is this bank's
// own book, its own register, its own customers and its own half of a payment.
//
// The bank acting is the network's identity rather than an argument, so no
// method here names a participant to act AS. What still decides between two
// members is the domain refusing a bank that is not the payment's party — see
// ErrNotThisBanksPayment and ErrNotAPartyToThisReturn.
type BankNetwork struct {
	core

	// store is this bank's own database. The four views beside it are the same
	// database at the widths the Book, Register, Catalogue and Portfolio types
	// are written against, derived from it rather than injected next to it — so
	// every layer is guaranteed to address the same rows.
	store    BankStore
	ledgers  ledger.Store
	deposits deposit.Store
	lendings lending.Store
	products product.Store
}

// ClearingHouseNetwork is the CSM's handle: it clears, it nets, it routes, and
// it holds no book of accounts at all.
//
// Nothing on it posts, which is what makes the clearing house the one
// institution in this system that moves no money — see
// TestTheCSMTouchesOnlyItsOwnBook, which measures it rather than assuming it.
// What it does hold is the state that is an OBLIGATION rather than a record: the
// files and returns it has taken in and not yet handed over.
type ClearingHouseNetwork struct {
	core

	// store is the clearing house's own database, and there is no ledger view
	// beside it: its schema creates no books, no accounts and no entries.
	store ClearingHouseStore
}

// CentralBankNetwork is the settlement agent's handle, and the only one holding
// the central bank's book of accounts.
//
// Four acts move central-bank reserves — opening a member's reserve account,
// crediting one on a lodgement, discharging a cut-off's net positions, and
// reversing a settled payment's — and each is on this type and on no other.
//
// What this institution cannot do is ENUMERATE the payments of a batch. Nothing
// here turns a cycle into the payments inside it, which is what "the central
// bank never sees an individual payment" means at a cut-off; a return names one
// payment because the returning bank named it in the message.
type CentralBankNetwork struct {
	core

	// store is the settlement agent's own database, and ledgers is the same
	// database as the ledger.Store a Book takes — the narrower of the two ledger
	// views, since this schema creates no slot mapping.
	store   CentralBankStore
	ledgers ledger.Store

	// centralBank holds the members' reserve accounts. It is a handle over the
	// store, not state: its chart of accounts is resolved from the store on use
	// (see centralBankChartTx), so it survives a Reset and works against a
	// database an earlier process populated.
	//
	// It is nil on a handle this package's own tests assembled over another
	// institution's core, and centralBankBook is what refuses that.
	centralBank *ledger.Book
}

// The three constructors, for a caller building ONE institution over a store it
// already holds. Each takes that institution's own store and what its identity
// carries and no more, so there is no identity to pass wrongly: only a bank has
// a participant to name. Networks is what a deployment uses; these are for a
// caller that has one store and one institution in mind.
//
// Each registers the SEPA Credit Transfer and SEPA Direct Debit schemes, refuses
// an identity belonging to nobody, and performs no I/O: the central bank's chart
// of accounts is created on first use and looked up thereafter, so building one
// over a database that already holds a network is safe and idempotent.

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
