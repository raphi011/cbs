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
//
// # One store type per institution
//
// There is no payment.Store and no payment.Tx, and their absence is the design.
// One interface over three schemas is what let the clearing house's transaction
// carry GetDepositAccount and PutFacility: every institution's store had to
// implement every method, and two thirds of them had no table underneath. What
// refused the crossing was a runtime sentinel, on a store whose schema was
// chosen by a value passed to Open.
//
// Now an act that belongs to one institution cannot be NAMED through another's
// handle, which is [ADR-0006]'s ruling applied to the layer underneath it. A
// bank reaches 74 methods, the clearing house 30, the settlement agent 46, and
// nothing reaches a table its schema does not create.
//
// What no type can express is which COLUMNS a shared table has: a bank's
// payments row carries the legs it posted, the clearing house's carries the
// cut-off it was cleared in. See PaymentRowsTx.
//
// [ADR-0006]: ../docs/adr/0006-one-type-per-institution.md

// CommonStore is the unit of work every institution can open, whatever tables it
// has. It is what Network keeps, because the audit trail and the id counters are
// in all three schemas and nothing an institution does with them can be a
// crossing.
//
// The three stores below are what an institution's own acts run in. Each is the
// SAME database seen at its own width; a network holds one of them and this one,
// derived from it rather than passed beside it.
type CommonStore interface {
	Update(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error
	View(ctx context.Context, fn func(context.Context, ledger.CommonTx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// BankStore owns one member bank's database: its ledger, its deposit register,
// its products, its loans, its own row in banks, its copy of the routing
// directory, the mandates it holds as creditor bank, its copy of each payment it
// is a party to, and the advices it was sent.
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
//
// # Why the set is an interface and not a map
//
// Because a bank's database is opened, and opening can fail. A map would have to
// be complete before the first act, which is the thing this system cannot
// promise: banks are founded while the process runs, and the set of them is the
// set of databases rather than a list somebody keeps. Bank therefore takes a
// context and returns an error.
//
// # Asking for a bank is what makes it exist
//
// There is no Provision and nothing mints an id. A bank's database is named by
// its BIC — see AsBank — so a joining bank arrives already knowing the name of
// the file it is about to write, and Bank opens it or creates it. That is not a
// hole in the admission rules: WHO IS A MEMBER is the clearing house's roster
// and nothing else, and an unadmitted database is a file nobody routes to. The
// alternative — a set that refuses to open a database for a bank the roster does
// not name — would make founding impossible, since a founding bank is
// deliberately not in the roster yet.
//
// # It is one process's set, not the system's
//
// Nothing about this type says the databases are on one machine or reachable
// from one another. The point of the split is that no statement can span two of
// them; holding the handles in one value is what a composition root does, and
// api hands each listener exactly one institution's view out of it, exactly as
// it hands out ports.
type Stores interface {
	// Bank returns the store holding one member bank's database, opening it on
	// the first ask and returning the same handle thereafter. Two calls for one
	// BIC must return stores that see each other's rows — otherwise a bank would
	// forget what it wrote between two acts.
	Bank(ctx context.Context, bic iso20022.BIC) (BankStore, error)

	// Banks is every member bank this set holds a database for, ascending by
	// address so that two calls agree and a restart plans the same listeners.
	//
	// It answers "every bank whose database exists", which INCLUDES the founded
	// and unadmitted bank the roster deliberately omits — a bank with a licence,
	// a book and customers that no scheme has admitted. cmd/server's listener
	// plan is the caller and that bank is the one it must not drop: a founded
	// bank still has an operator, and its own console is the way in.
	//
	// Nothing in the domain calls it and nothing should. An institution asking
	// which OTHER institutions exist is the crossing this whole task removes;
	// this is the process asking which databases it has, which is a question
	// about deployment.
	Banks(ctx context.Context) ([]iso20022.BIC, error)

	// ClearingHouse and CentralBank are the two institutions that are
	// configuration rather than data: there is exactly one of each, it exists
	// before any bank does, and neither can fail to be there. That is why
	// neither takes a context or returns an error where Bank does both.
	ClearingHouse() ClearingHouseStore
	CentralBank() CentralBankStore

	// Reset empties every store in the set, so the system behaves like a freshly
	// migrated one.
	//
	// It is on the SET rather than on a Store because clearing the system is
	// nobody's act in the domain — no institution can empty another's database,
	// and that is the whole point — while api's POST /admin/reset is one request
	// that has to empty all of them. See api.Server.Reset, its only caller
	// outside tests.
	Reset(ctx context.Context) error

	// Close closes every store in the set. It is idempotent, for the reason
	// Store.Close is: a test closes what it hands to a suite that closes it too.
	Close() error
}

// ---------------------------------------------------------------------------
// The capabilities two institutions share
// ---------------------------------------------------------------------------

// PaymentRowsTx is a party's own copy of the payments it is on: a bank's and the
// clearing house's, and no third institution's.
//
// The two keep DIFFERENT COLUMNS in the same table. A bank's row carries the legs
// it posted and no cut-off; the clearing house's carries the cut-off it was
// cleared in and no legs. That is finer than any interface can say, so it is the
// one thing here the store still decides — see store/sqlite's Shape, whose
// paymentLegs and paymentCycle choose the column list.
//
// The settlement agent has no payments table at all. It discharges cut-offs, and
// a cut-off names no payment.
type PaymentRowsTx interface {
	PutPayment(ctx context.Context, p Payment) error
	GetPayment(ctx context.Context, id PaymentID) (Payment, error)
	GetPaymentByEndToEndID(ctx context.Context, endToEndID string) (Payment, error)
	ListPayments(ctx context.Context) ([]Payment, error)
}

// partyTx is what an institution that is a PARTY to a payment can reach: its own
// copy of the row, and the audit trail every institution has.
//
// It is the parameter type of the few acts a bank and the clearing house perform
// identically — see Network.CompleteReturnTx. Unexported because it is a
// composition of two exported capabilities and not a fourth kind of institution.
type partyTx interface {
	PaymentRowsTx
	ledger.CommonTx
}

// ---------------------------------------------------------------------------
// One transaction type per institution
// ---------------------------------------------------------------------------

// BankTx is one member bank's unit of work. It spans every layer a bank drives —
// deposit and, through it, product and the ledger; lending; the payment rows —
// so Bank.RunEndOfDay accrues an overdraft and a loan in a single commit: two
// batches, one unit of work, so a failure halfway cannot leave a bank with a day
// of interest on its loans and none on its overdrafts.
//
// Every row it reaches is this bank's own, keyed and sequenced under this bank's
// book. See Network.book.
type BankTx interface {
	deposit.Tx
	lending.Tx
	PaymentRowsTx

	// A bank's own record of ITSELF, and the only row in this table. The
	// settlement agent's record of the account it opened and the clearing house's
	// record of where to send a message are the other two rows admission writes,
	// and each lives in that institution's own database — see
	// SettlementMember and RosterEntry.
	//
	// It is keyed by BIC because the BIC is the only identifier that crosses an
	// institutional boundary in this system. It carries no ParticipantID: an id
	// the network allocates is not something a message ever tells anybody.
	PutBank(ctx context.Context, b Bank) error
	GetBank(ctx context.Context, id ParticipantID) (Bank, error)
	ListBanks(ctx context.Context) ([]Bank, error)

	// A MEMBER BANK's copy of the clearing house's roster, in that bank's own
	// database. See DirectoryEntry.
	//
	// ReplaceRoutingDirectory is a REPLACE and not an upsert, and it is the only
	// method on this interface that is. Every other Put here writes one row an
	// institution decided about; this one takes delivery of a whole file, and a
	// row that has left the roster has to leave the copy — a merge would make a
	// subscriber's directory the union of every snapshot it ever pulled, which is
	// not a copy of anything. It is also why there is no delete: a directory is
	// replaced wholesale or not at all.
	//
	// GetDirectoryEntry answers ErrBankCodeUnknown on a miss, which is a
	// subscriber's answer about ITSELF and not the registry's about an
	// allocation; the two sentinels' docs set out why neither can stand in for
	// the other.
	//
	// ListDirectoryEntries has two readers and they want the same thing for
	// different reasons: a bank's own console, showing what it holds and when it
	// pulled it, and payment/recon, holding it against the published roster in
	// order to REPORT a difference it must not fail on.
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
	// interface. Every other row here belongs to the ONE institution whose
	// database it is in and is keyed under that institution's book; an advice is
	// one member bank's record of what it was told, and the book is part of its
	// identity rather than a scope over rows that could have been somebody
	// else's. Two banks advised of one movement write two rows in two databases —
	// see SettlementAdvice, where that is the whole design — and it is also what
	// makes the recorder in cmd/server/books_test.go see a bank reaching its own
	// book when it books a settlement.
	//
	// ListSettlementAdvices has two KINDS of reader and they see different
	// amounts.
	//
	// payment/recon, the reconciliation harness, holds every institution's
	// advices against the settlement agent's own register, so that a movement the
	// agent made and a member never booked can be told apart from a member's
	// books simply being wrong. That comparison is the one thing no institution
	// in this system may make, which is why that reader is a harness.
	//
	// The others are one member bank reading its OWN, which is an act that bank
	// may perform: Network.ReconcileTx, which is what reads
	// SettlementAdvice.ClosingBalance — every advised movement against the
	// running balance of the leg booked from it — and
	// Network.ListSettlementAdvicesTx, which shows the same rows unchecked, for
	// the operator a break sends looking. What neither can see is a statement
	// that never arrived, because a closing balance only ever arrives on a
	// statement the bank holds.
	PutSettlementAdvice(ctx context.Context, book ledger.BookID, a SettlementAdvice) error
	GetSettlementAdvice(ctx context.Context, book ledger.BookID, reference string, asset ledger.AssetCode) (SettlementAdvice, error)
	ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]SettlementAdvice, error)
}

// CsmTx is the clearing house's unit of work.
//
// It has NO ledger and no chart of accounts, which is why it embeds
// ledger.CommonTx and not ledger.Tx: the clearing house keeps an audit trail and
// allocates ids, and it moves no money. It has no customers, no products and no
// loans either — the whole of what it holds is below.
type CsmTx interface {
	ledger.CommonTx
	PaymentRowsTx

	// The cycles this clearing house opens, closes and settles.
	//
	// GetOpenCycle returns the single open cycle for a scheme, or
	// ErrCycleNotFound. It is what the network asks instead of remembering.
	PutCycle(ctx context.Context, c ClearingCycle) error
	GetCycle(ctx context.Context, id CycleID) (ClearingCycle, error)
	GetOpenCycle(ctx context.Context, scheme SchemeID) (ClearingCycle, error)
	ListCycles(ctx context.Context) ([]ClearingCycle, error)

	// WHO IS A MEMBER, which is this institution's register and nobody else's. A
	// member bank keeps a COPY of it as its routing directory — see
	// DirectoryEntry — and the two are held against each other by payment/recon
	// and by no institution.
	//
	// PutRosterEntry and GetRosterEntry are called by AdmitMemberTx and by
	// Network.GetRosterEntryByBIC, which is what a caller asking whether an
	// address is a member gets instead of a whole bank. ListRosterEntries is
	// asked by the deployment at startup and by package provision, and it asks
	// WHO IS A MEMBER rather than which banks exist, so that a founded and
	// unadmitted bank is enrolled as a subscriber nowhere.
	//
	// GetRosterEntryByIssuer is the roster's secondary lookup, and it exists for
	// one refusal: two members published under one bank code would make one
	// address ambiguous for every member that copies this table. Same shape as
	// GetPaymentByEndToEndID — the row is keyed by the identifier this
	// institution routes on, and the question arrives quoting a different one —
	// and the same sentinel as the primary, ErrRosterEntryNotFound, because "no
	// member holds this code" is the answer either lookup gives when it misses.
	PutRosterEntry(ctx context.Context, e RosterEntry) error
	GetRosterEntry(ctx context.Context, bic iso20022.BIC) (RosterEntry, error)
	GetRosterEntryByIssuer(ctx context.Context, issuer iban.Issuer) (RosterEntry, error)
	ListRosterEntries(ctx context.Context) ([]RosterEntry, error)

	// The clearing house's unfinished business: see HeldFile and HeldReturn.
	// There is no institution in which both a book of accounts and a held file
	// exist.
	//
	// AddHeldFile is the one write on this interface that is not an upsert. A
	// share has no key its caller knows — it is the Nth file built for a cut-off,
	// and the store allocates that N — so there is nothing for a second call to
	// conflict with, and two identical shares are two obligations to the same
	// bank rather than one recorded twice.
	//
	// DeleteHeldFile takes ONE share and names it by the N above, which
	// ListHeldFiles reports for exactly this reason. A share is discharged when
	// the bank it is addressed to has been handed it, and the hand-over is a
	// write to another table that this transaction may not touch — so the two
	// happen one share at a time and in that order. Deleting a cut-off's shares
	// together would discharge the ones that never left.
	AddHeldFile(ctx context.Context, f HeldFile) error
	ListHeldFiles(ctx context.Context, id CycleID) ([]HeldFile, error)
	DeleteHeldFile(ctx context.Context, id CycleID, seq int64) error

	PutHeldReturn(ctx context.Context, r HeldReturn) error
	GetHeldReturn(ctx context.Context, id PaymentID) (HeldReturn, error)
	DeleteHeldReturn(ctx context.Context, id PaymentID) error
}

// CentralBankTx is the settlement agent's unit of work.
//
// It embeds ledger.Tx and not deposit.Tx: the settlement agent keeps a book of
// accounts — the members' reserves are in it — and has no customers, no
// products, no loans and no slot mapping. It keeps no payments either, which is
// what "the central bank never sees an individual payment" means at the store.
type CentralBankTx interface {
	ledger.Tx

	// The settlement agent's record of the account it opened for a member, keyed
	// by BIC for the reason a bank's own row is: the BIC is the only identifier
	// that crosses an institutional boundary here.
	//
	// PutSettlementMember and GetSettlementMember are called by
	// OpenSettlementAccountTx and by settlementAccountTx — which is every reserve
	// movement in the system, since SettleCycleTx, SettleReturnTx and
	// ReserveBalance all resolve their account through it.
	//
	// ListSettlementMembers has two callers: settlementLegsTx walks it to turn a
	// cycle's net positions into legs, in the order the agent opened the
	// accounts, and api's GET /reserves reports one row per (member, asset) from
	// it. Its ordering contract is load-bearing because it decides the entry
	// order of a settlement transaction that is persisted.
	PutSettlementMember(ctx context.Context, m SettlementMember) error
	GetSettlementMember(ctx context.Context, bic iso20022.BIC) (SettlementMember, error)
	ListSettlementMembers(ctx context.Context) ([]SettlementMember, error)

	// The settlement agent's SECOND register, and the one no bank ever reads:
	// which institution holds which bank code. See BankCodeAllocation.
	//
	// There are two Gets because there are two questions and each has one
	// caller. GetBankCode asks "is this code taken", which is the refusal that
	// stops two banks issuing addresses nobody can tell apart;
	// GetBankCodeForBIC asks "what has this bank already been allocated", which
	// is what makes a two-asset admission allocate once. Neither is derivable
	// from the other, and the row is keyed by the first: (country, code), never
	// by the code alone, because a code is unique within one country.
	//
	// NextBankCodeSerial counts one country's allocations, in the agent's own
	// id_sequences and not in the shared "id" counter, for the reason
	// NextAddressSerial has one: a bank code is a number people quote, and one
	// drawn from a counter every act in the database shares would have gaps in
	// it saying how much unrelated work happened in between.
	//
	// ListBankCodes has one reader and it is not an institution: payment/recon,
	// which holds the register against every bank's own addresses and against
	// the clearing house's published roster. No actor in this system may make
	// either comparison.
	PutBankCode(ctx context.Context, a BankCodeAllocation) error
	GetBankCode(ctx context.Context, issuer iban.Issuer) (BankCodeAllocation, error)
	GetBankCodeForBIC(ctx context.Context, country iban.Country, bic iso20022.BIC) (BankCodeAllocation, error)
	ListBankCodes(ctx context.Context) ([]BankCodeAllocation, error)
	NextBankCodeSerial(ctx context.Context, book ledger.BookID, country iban.Country) (uint64, error)

	// The cut-offs this agent has discharged.
	//
	// GetSettlementByCycle is its answer to "have I already discharged this
	// cut-off", and it is a secondary lookup for the same reason
	// GetPaymentByEndToEndID is: the row is keyed by an id this institution
	// allocated, and the question arrives quoting somebody else's.
	//
	// A redelivered pacs.009 is the reachable case and it must not settle twice;
	// the ledger's idempotency key would refuse the second POSTING, but only
	// after the reserve check had already run against reserves the first
	// settlement moved, which turns a duplicate into a spurious AM04. Same
	// sentinel shape as the rest of this block: ErrSettlementNotFound.
	PutSettlement(ctx context.Context, s Settlement) error
	GetSettlement(ctx context.Context, id SettlementID) (Settlement, error)
	GetSettlementByCycle(ctx context.Context, id CycleID) (Settlement, error)
	ListSettlements(ctx context.Context) ([]Settlement, error)
}

// Contract notes for implementers. Each of these is asserted by one of the three
// payment suites in store/storetest — RunPayment for a bank's rows,
// RunClearingHousePayment for the roster and the cycles, RunCentralBankPayment
// for the settlement register — and the named subtest is what pins it.
//
//   - Not-found sentinels. GetBank -> ErrParticipantNotFound, GetPayment
//     and GetPaymentByEndToEndID -> ErrPaymentNotFound, GetMandate ->
//     ErrMandateNotFound, GetCycle and GetOpenCycle -> ErrCycleNotFound,
//     GetSettlement -> ErrSettlementNotFound. errors.Is is used, so wrapping is
//     fine. (GetOnMissingPaymentRowsReturnsSentinels.)
//
//     The three admission rows have a sentinel each and not one between them:
//     GetSettlementMember -> ErrSettlementMemberNotFound
//     (SettlementMemberIsKeyedByBIC), GetRosterEntry ->
//     ErrRosterEntryNotFound (RosterEntryCarriesNoAccountIdentifiers). A bank
//     the settlement agent holds no account for and a bank the clearing house
//     does not route to are different institutions' answers, and a founded bank
//     is legitimately both.
//
//   - Bank.Ledger and Bank.Deposit are NOT persisted. They are live handles over
//     the store, not data, and there is no column that could hold a *ledger.Book
//     — a store that kept them would be handing back a Bank wired to whatever it
//     was wired to when it was written. The Network rebinds them on the way out.
//     Bank.AdmissionRef and the settlement account numbers ARE data and must
//     survive: they are what a bank's own row says about its admission, and a
//     store that drops either hands back a bank that has recorded nothing.
//     (BankRoundTripsAndDropsLiveHandles.)
//
//   - The BIC-keyed rows are keyed by BIC, and their collections replace rather
//     than merge on an upsert — SettlementMember.Accounts and
//     RosterEntry.Assets both, for the reason Bank.Assets does: a stale entry
//     is an account or an asset the institution has given up and would still be
//     acted on. (SettlementMemberKeepsOneAccountPerAsset.)
//
//   - Put* are upserts on the primary key, which is how a status change is
//     written, and they deep-copy: a caller that mutates the slice or map it
//     passed in must not change the stored row, and neither must a caller that
//     mutates what a Get returned. (PutIsAnUpsertAndDeepCopies.)
//
//   - GetPaymentByEndToEndID matches exactly, and an empty end-to-end id is
//     never an identity — it is always ErrPaymentNotFound, the same rule
//     ledger.Tx applies to an empty idempotency key.
//     (GetPaymentByEndToEndIDIsExactAndIgnoresEmpty.)
//
//   - GetOpenCycle returns the cycle whose Scheme matches and whose Status is
//     CycleOpen. The domain keeps at most one open per scheme; if more than one
//     is open the earliest is returned. Closing a cycle must make it invisible
//     here. (GetOpenCycleFindsTheOpenCycleForItsScheme.)
//
//   - Listing order is the creation instant ascending, ties broken by the row's
//     monotonic insertion sequence — never by ID. The creation instant is
//     Bank.CreatedAt, Payment.CreatedAt, Mandate.CreatedAt,
//     ClearingCycle.OpenedAt, Settlement.SettledAt, SettlementMember.OpenedAt
//     and RosterEntry.AdmittedAt.
//     (PaymentListOrderingIsCreatedAtThenSeq.)
//
//   - Rollback spans all three layers: a failed Update undoes payment rows,
//     deposit rows, ledger rows and audit appends written through the same Tx.
//     It spans one INSTITUTION and no more: a unit of work is one database's.
//     (UpdateRollsBackAllThreeLayersTogether.)
//
//   - GetSettlementAdvice -> ErrSettlementAdviceNotFound. The key is
//     (book, reference, asset), all three: two banks advised of one movement
//     hold two rows, a bank operating in two assets settles each separately, and
//     a bank holding one advice keyed by a cycle id and another by a payment id
//     in the same asset does not collide (AdvicesAreKeyedByReferenceNotByCycle).
//     ListSettlementAdvices is scoped to ONE book and ordered by AdvisedAt then
//     seq, like every other listing here.
//     (SettlementAdviceIsScopedToTheBankThatWasAdvised.)
//
//   - GetHeldReturn -> ErrHeldReturnNotFound. ListHeldFiles answers an empty
//     slice for a cut-off nothing was built for, because "no share" and "no such
//     cycle" are the same fact to the caller: a cycle this institution took no
//     file into is exactly a cycle with nothing to release.
//
//     Their listing order is the BUILD order and not a creation instant, which
//     is the one place this schema departs from the rule below. A share carries
//     no timestamp — it is not an event, it is an obligation — so the store's own
//     monotonic seq is the whole of the order, and it is load-bearing: the banks
//     are handed their files in the order the files arrived.
//     (HeldFilesSurviveTheirCycleAndReleaseInBuildOrder.)
//
//   - Reset clears the payment tables too — this INSTITUTION's, which is the only
//     thing it could now mean. (ResetClearsPaymentState, and its two
//     institution halves, ResetClearsTheClearingHousesState and
//     ResetClearsTheCentralBanksState.)

// ---------------------------------------------------------------------------
// Narrower views of the same store
// ---------------------------------------------------------------------------

// Every view below re-types a store's callback and holds no state of its own.
// They exist because Go allows a type one Update method and each Store interface
// declares it with a callback of its own; each view hands the callback the very
// same transaction.
//
// The point is that a Book built over a view and a Network built over the same
// store cannot be talking to different databases: the view is DERIVED from the
// store rather than passed in beside it.

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

// ledgerView presents a bank's store as the ledger.Store a Book takes.
//
// A Book is the ledger BOTH institutions that keep one have, so it is the narrow
// interface even here: the slot mapping is reached through the transaction a
// deposit or lending act already holds, never through a store.
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
