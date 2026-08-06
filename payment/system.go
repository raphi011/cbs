package payment

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// Network is the payment processor. It owns one book of accounts per
// participant bank plus a central-bank book, and orchestrates payments through
// their full lifecycle: initiation, clearing (netting), and settlement.
//
// # One store, one settlement window
//
// Every book — each participant's and the central bank's — lives in the same
// Store, distinguished by its ledger.BookID, and the network's own entities
// (participants, payments, mandates, cycles, settlements) live there too under
// ledger.NetworkBook. Because payment.Tx embeds deposit.Tx embeds ledger.Tx, one
// transaction can reach all of them, which is what makes an operation spanning
// several books a single unit of work.
//
// SettleCycle used to be that operation at its widest: it moved reserves at the
// central bank, posted every member's mirror leg and paid out every creditor
// inside one Update. It no longer does either of the last two. What is one unit
// of work is what ONE institution does — the central bank's netting transaction,
// whole or not at all — and each member books its own halves afterwards, on
// advice, in units of work of its own. See SettleCycle on why the interval
// between them is now the thing being modelled rather than something to hide.
//
// A settlement window is still what the central bank's half is: an interval
// during which the settlement agent holds the participants' reserve accounts,
// checks that every net payer can cover its position, and posts the whole batch
// or none of it. The database transaction is what supplies that window here.
//
// # Where the state lives
//
// A Network owns no state of its own beyond the registered schemes, which are
// code rather than data. Everything else is in the Store, so two processes
// sharing a database see the same network, and a restart loses nothing.
//
// # Thread safety
//
// All public methods are safe for concurrent use; the Store provides the
// isolation.
type Network struct {
	clock func() time.Time

	// id is which institution this network acts as, and it is the answer to
	// every "whose book?" the acts below used to take as an argument. See
	// Identity, which carries the argument for why it is here rather than on
	// each call, and Networks, which is what mints one per entity.
	id Identity

	// store backs every book the network creates and every network-scoped
	// entity it records.
	store Store

	// ledgers, deposits and lendings are the same store seen through the
	// narrower interfaces the Book, Register and Portfolio types are written
	// against. They are derived from store rather than injected beside it, so
	// all layers are guaranteed to address the same data.
	ledgers  ledger.Store
	deposits deposit.Store
	lendings lending.Store
	products product.Store

	// schemes is the only thing a Network holds in memory, and it is SHARED by
	// every network one Networks mints. See schemeRegistry.
	schemes *schemeRegistry

	// centralBank holds the participants' reserve accounts. It is a handle over
	// the store, not state: its chart of accounts is resolved from the store on
	// use (see centralBankChartTx), so it survives a Store.Reset and works
	// against a database that was populated by an earlier process.
	//
	// It is NIL on every network but the settlement agent's, and that is Task
	// 18b's second half. Until then every Network held one, which meant every
	// institution in this system held a handle on the book where central-bank
	// money lives — a bank's, a clearing house's, a test fixture's — and the
	// only thing keeping a bank handler out of it was that bankOps names no
	// method that reaches it. That is a fact about the mesh rather than about
	// this package: SettleCycleTx, SettleReturnTx, OpenSettlementAccountTx,
	// ReceiveLodgementTx and ReserveBalance are all exported, and any of them
	// called on a bank's network would have posted in the central bank's book
	// and been right to, because the book was there.
	//
	// Now it is not, and centralBankBook is what every one of those five goes
	// through. See it for what the refusal is worth and what it is not.
	centralBank *ledger.Book
}

// centralBankBook is the settlement agent's book of accounts, or a refusal.
//
// The five acts that reach it are the whole of settlementOps plus ReserveBalance,
// and each is an act only the central bank performs: opening a member's reserve
// account, crediting one on a lodgement, discharging a cut-off's net positions,
// reversing a settled payment's, and reading what a member holds. A network that
// is not the central bank's has no book to perform them in, so it is refused
// here rather than in five places.
//
// # What this catches, and what it does not
//
// It catches an act of the settlement agent's reached through any other
// institution's handle — which, since these methods are exported, is every
// caller outside the mesh: api, seed, a test fixture, and the reconciliation
// harness Task 18e adds. Inside the mesh the compiler already caught it, because
// settlementOps carries the five and bankOps and csmOps carry none of them.
//
// It does NOT catch the central bank posting in the wrong PLACE within its own
// book, and it never could: a BookID is an ordinary argument and one is as valid
// as another. That is still the recorder's job in mesh/books_test.go, and Task
// 18c's one-book rule is what finally makes it the store's.
func (s *Network) centralBankBook() (*ledger.Book, error) {
	if s.centralBank == nil {
		return nil, fmt.Errorf("%w: this is %s, and the central bank's book of accounts is the settlement agent's alone",
			ErrNotThisInstitutionsAct, s.id)
	}
	return s.centralBank, nil
}

// self is the member bank this network acts as, or a refusal.
//
// It is what nine methods in the mesh's bankOps used to take as a `by` argument
// and what ResolveIdentifier took as one from Task 18a. Every act it guards is a
// posting or a read in one member's own book, so a network that is not a
// member's has nothing to perform it against.
//
// The refusal is the point rather than a side effect. A per-call `by` could be
// any participant and the domain's own guards were what decided — a payment this
// bank is not a party to, a statement about another member's reserve account, an
// acknowledgement addressed to somebody else. Those guards all stay, and they
// are still what answers a bank asking about a payment that is not its own. What
// this adds is the case none of them covered: an act of a member bank's
// performed through the clearing house's or the central bank's handle, which
// used to be indistinguishable from the member performing it itself.
func (s *Network) self() (ParticipantID, error) {
	pid, ok := s.id.Participant()
	if !ok {
		return "", fmt.Errorf("%w: this is %s, and this act is a member bank's own",
			ErrNotThisInstitutionsAct, s.id)
	}
	return pid, nil
}

// CentralBankBook is the BookID of the central bank's own book of accounts, and
// also the book its own rows are keyed and sequenced under. See Network.book.
const CentralBankBook ledger.BookID = "central-bank"

// ClearingHouseBook is the BookID the clearing house's own rows are keyed and
// sequenced under.
//
// It is NOT a chart of accounts and there is none to be had: the clearing house
// keeps no book of accounts, which csm/0001_init.sql now states by having no
// ledger tables at all. What it names is the audit log and the id counters,
// which are book-keyed in every shape because ledger.Tx is one interface over
// three schemas.
//
// It replaces ledger.NetworkBook, and the replacement is not a rename. That
// constant meant "belongs to no single institution", and it was the book under
// which participants, payments, mandates, cycles and settlements were all
// sequenced — one counter, one audit stream, shared by everybody, in the one
// database that held everything. There is no network left for anything to
// belong to. Each of those rows turned out to have exactly one owner, each owner
// now has a database, and this is the clearing house's name for its own.
const ClearingHouseBook ledger.BookID = "clearing-house"

// book is the BookID this institution's own rows are keyed and sequenced under,
// and the answer to every "which book?" that used to be ledger.NetworkBook.
//
// THE COUNTER FOLLOWS THE ROW. Every read-then-write ordering in this package
// allocates an id before the read it decides from, and the ordering is worth
// something only while the counter row and the row being decided from are in one
// database — two databases is two transactions, and no retry can make one of
// them see the other. So an act allocates from the store it is about to write,
// which is this one, because a Network is one institution's handle and its store
// is that institution's.
//
// That the four orderings survived unchanged is a finding rather than a design:
// each of them — a payment id before the duplicate-reference check, and the
// three admission acts before their find-or-creates — turned out to belong to
// exactly one institution already. Had any of them spanned two, it would have
// had to become a message.
//
// There is no zero case. NewNetwork refuses an identity-less network, so every
// Network reaching this has one of the three roles.
func (s *Network) book() ledger.BookID {
	switch s.id.role {
	case roleBank:
		// A bank IS its own book: ledger.BookID(ID), fixed by FoundBankTx and
		// documented on Bank.BookID. Since Task 18 that id is the bank's BIC, so
		// this is also its address and the name of its database — one identifier
		// doing what three used to. See FoundBankTx.
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
// asset. The asset is part of the name, the way it is for a bank's reserve and
// suspense accounts, because there is one such account per asset and a chart
// of accounts listing two rows both called "Settlement Assets" is unreadable.
//
// The name used to be stable across assets: a book written before the asset
// dimension existed had one such account, backfilled to EUR, and a name that
// did not mention the asset was what let it be found rather than duplicated.
// The migration series that produced those books has since been folded away —
// there is one migration, and it creates accounts.asset from the start — so
// there is no longer any book whose account is named without its asset.
func cbAssetsAccountName(asset ledger.AssetCode) string {
	return cbAssetsName + " (" + string(asset) + ")"
}

// NewNetwork creates one institution's view of a payment network, with the SEPA
// Credit Transfer and SEPA Direct Debit schemes registered.
//
// Every book the network creates lives in the given store and reads time from
// the given clock, so that booking dates, value dates and audit timestamps line
// up across all of them.
//
// id says WHICH institution this is, and it is what every act below that used to
// take a participant argument now reads instead. Most callers do not name one
// here: Networks is the factory, it is what api, the mesh and cmd/server hold,
// and it is where "which entity" and "which store" become one question in Task
// 18d. See Identity for why this is constructor state.
//
// # The zero Identity panics
//
// A network belonging to nobody is what this parameter exists to remove, so
// accepting one would leave the shape it replaces reachable — and reachable by
// omission, which is the easiest way there is to reach something. It is a wiring
// mistake in the same class as api.NewServer's missing mesh: no caller can
// recover from it, no request can be answered despite it, and every act would
// fail far from the cause with a refusal naming "no institution". So it fails
// here, loudly, at the line that got it wrong.
//
// The constructor performs no I/O: the central bank's chart of accounts is
// created on first use and looked up thereafter, so calling NewNetwork against
// a store that already holds a network is safe and idempotent.
func NewNetwork(store Store, clock func() time.Time, id Identity) *Network {
	return newNetwork(store, clock, id, newSchemeRegistry())
}

// newNetwork is NewNetwork with the scheme registry supplied, so that Networks
// can hand one registry to every institution it mints. See schemeRegistry.
func newNetwork(store Store, clock func() time.Time, id Identity, schemes *schemeRegistry) *Network {
	if id.role == roleUnset {
		panic("payment: NewNetwork needs an identity; a network belonging to no institution has no answer to whose book an act is about")
	}
	ledgers := ledgerView{store}
	s := &Network{
		clock:    clock,
		id:       id,
		store:    store,
		ledgers:  ledgers,
		deposits: depositView{store},
		lendings: lendingView{store},
		products: productView{store},
		schemes:  schemes,
	}
	if id.role == roleCentralBank {
		s.centralBank = ledger.NewBook(ledgers, CentralBankBook, clock)
	}
	s.RegisterScheme(SCT{})
	s.RegisterScheme(SDD{})
	return s
}

// Identity is which institution this network acts as. See the type.
func (s *Network) Identity() Identity { return s.id }

// Now is the instant this network reads for everything it stamps: booking
// dates, value dates, audit timestamps.
//
// It is exported because a layer built OVER a network has to timestamp too, and
// a second clock beside this one would be a second answer to the same question.
// The mesh is the case that forced it: a message header carries a creation time
// (AppHdr/CreDt), so a mesh with a clock of its own could emit a pacs.008 dated
// after the payment it carries — under the frozen clock the tests run on, days
// after. There is one clock in this system, and this is how anything above the
// payment layer reads it. See the mesh package doc, which says so.
func (s *Network) Now() time.Time { return s.clock() }

// now is Now, for this package's own use. Both exist because the exported one
// is a promise to callers outside and the unexported one is what a hundred call
// sites in here already spell.
func (s *Network) now() time.Time { return s.Now() }

// Store returns the store every layer of this network shares, so a caller can
// open its own unit of work — or reset the whole system — against the same
// data the network reads.
func (s *Network) Store() Store { return s.store }

// RegisterScheme adds (or replaces) a scheme. Adding support for instant or
// card payments is a matter of registering a type that implements Scheme — the
// orchestration below is scheme-agnostic.
//
// # The one thing that is not scheme-agnostic: INBOUND translation
//
// An interbank message names no scheme. Its message definition says which
// DIRECTION the payment runs in and its currency says which asset it settles
// in, and schemeSettling turns that pair into one registered scheme or refuses
// the message. So two schemes with the SAME direction and the SAME asset are
// ambiguous, and every inbound message that could be either is refused with
// ErrAssetMismatch — including the ones that were arriving perfectly well
// before the second scheme was registered.
//
// SEPA Instant is exactly that case, and it is the example this comment used to
// offer, so it is worth naming rather than leaving to be discovered: it is a
// push scheme settling in euro, as SEPA Credit Transfer is, so registering one
// alongside SCT stops this network being able to receive a euro pacs.008 at
// all. What decides it is direction and asset and nothing else — a card scheme
// in euro that pushed would collide the same way, and one in another asset, or
// pulling, would not.
//
// See schemeSettling for why that is a refusal rather than a rule: nothing in a
// pacs.008 could break the tie, and what a real network has here is the clearing
// arrangement the message arrived over, which this system does not model.
//
// Nothing else is affected. Submitting under either scheme, clearing it and
// settling it are all driven by the payment's own SchemeID and do not care how
// many schemes share an asset; it is only the translation of a message BACK
// into a request that has a question to answer.
func (s *Network) RegisterScheme(sc Scheme) {
	s.schemes.mu.Lock()
	defer s.schemes.mu.Unlock()
	s.schemes.m[sc.ID()] = sc
}

// Scheme looks up a registered scheme, for callers outside this package that
// have to ask the scheme a question before they can act on a payment — which
// bank submits it, for one (see api's bank submit handler).
//
// It is a wrapper over the unexported scheme rather than a rename of it, so
// that the dozens of internal call sites stay as they are.
func (s *Network) Scheme(id SchemeID) (Scheme, bool) { return s.scheme(id) }

// scheme looks up a registered scheme.
func (s *Network) scheme(id SchemeID) (Scheme, bool) {
	s.schemes.mu.RLock()
	defer s.schemes.mu.RUnlock()
	sc, ok := s.schemes.m[id]
	return sc, ok
}

// CentralBank exposes the central-bank ledger for inspection (balances,
// audit trail). Treat it as read-only.
//
// It returns an error rather than a nil book on every other institution's
// network, which is the difference between a refusal and a panic three frames
// away in ledger. See centralBankBook, which is the same refusal on the acts.
func (s *Network) CentralBank() (*ledger.Book, error) { return s.centralBankBook() }

// bind attaches the live handles a Bank record needs to be usable: its
// own book of accounts, the deposit register and the lending portfolio over
// it, all scoped to its BookID within the network's store.
//
// The handles are stateless, so binding is cheap and a bound Bank is
// safe to hold; the record's data fields are a snapshot, as with every other
// value the store returns.
func (s *Network) bind(p Bank) *Bank {
	p.Ledger = ledger.NewBook(s.ledgers, p.BookID, s.clock)
	p.Deposit = deposit.NewRegister(s.deposits, p.Ledger, p.BookID, s.clock)
	p.Lending = lending.NewPortfolio(s.lendings, p.Ledger, p.BookID, s.clock)
	p.Catalogue = product.NewCatalogue(s.products, p.Ledger, p.BookID, s.clock)
	return &p
}

// bankTx loads a participant and binds its live handles.
func (s *Network) bankTx(ctx context.Context, tx Tx, id ParticipantID) (*Bank, error) {
	rec, err := tx.GetBank(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.bind(rec), nil
}

// bankByBICTx finds the member a BIC addresses and binds its live
// handles. It is bankTx over the identifier a MESSAGE carries rather
// than the one this system numbers its members by.
//
// A sweep over the BANK ROWS — tx.ListBanks, and not the clearing house's
// roster, which is keyed by BIC and would be the obvious index for this. It
// cannot be used: this returns a bank with its live handles bound, and a roster
// entry carries no id to bind them from, which is the crossing
// Network.GetRosterEntry records pointing the other way. So the sweep is over
// the only table that answers, and BIC carries no uniqueness constraint there
// (see the banks.bic column comment, which records why). At four members that
// is honest; a real settlement agent's directory is a service with an index,
// exactly as ResolveIdentifier's is.
//
// The FIRST match wins, and that is a limit worth naming rather than
// discovering: two members registered under one BIC are indistinguishable to a
// message, and this returns whichever the store lists first. It is not
// ErrIdentifierAmbiguous's situation — that one refuses, because an ambiguous
// ADDRESS would route a customer's payment to a bank on the strength of listing
// order — because two bank rows under one BIC is a registration this system
// should never have accepted, and refusing every return in the network on
// account of it would be a worse answer than picking. What removes the limit is
// a unique index on the column, which is a schema decision nobody has taken.
func (s *Network) bankByBICTx(ctx context.Context, tx Tx, bic iso20022.BIC) (*Bank, error) {
	members, err := tx.ListBanks(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.BIC == bic {
			return s.bind(m), nil
		}
	}
	return nil, fmt.Errorf("%w: no member is addressed by %s", ErrParticipantNotFound, bic)
}

// centralBankChartTx returns the central bank's reserve and capital
// subledgers, creating the chart of accounts if this is the first time the
// store has been used.
//
// It resolves by name on every call rather than caching IDs on the Network. A
// cached ID is wrong in three situations that all occur in this system: after
// Store.Reset, in a second process opened against the same database, and in a
// process that constructed the Network before the data existed.
func (s *Network) centralBankChartTx(ctx context.Context, tx Tx) (ledger.SubledgerID, ledger.SubledgerID, error) {
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
//
// There is one per asset, because the account is an asset account like any
// other and an account is denominated in exactly one thing: the euro reserves
// the central bank has issued are not backed by the dollars it has issued.
//
// The lookup is by (capital subledger, name, asset). Matching on the asset as
// well as the name is what makes it idempotent per asset rather than per
// central bank — see cbAssetsAccountName for why the name carries the asset
// too.
//
// It lists the central bank's accounts on every call, immediately before the one
// lookup it makes, so nothing it matches against can be stale. There used to be
// a second form of this taking a chart and a listing the caller had already
// made, for the loop the deleted AddParticipantTx ran over a bank's assets; its
// doc warned that a caller able to ask twice for one asset would open a second
// account against a stale listing and must list again. OpenSettlementAccountTx
// is exactly that caller — it is idempotent per (BIC, asset) and re-entered on a
// retried admission — and it asks about ONE asset, because one acmt.007 asks for
// one currency, so the loop the pre-listed form saved work in no longer exists.
// Deleting it removes the hazard rather than arguing about it.
func (s *Network) centralBankAssetsAccountTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (ledger.AccountID, error) {
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
//
// It is what the row added by Task 17b exists for. Every reserve movement in
// this system used to resolve its account through Bank.Assets[asset].Settlement,
// which is the settlement agent reading a bank's record of the settlement
// agent's own book; under Task 18's stores that read is not available at all.
//
// A member the agent holds nothing for is ErrSettlementMemberNotFound, which is
// the true state of a bank that is founded and not yet admitted. A member it
// holds accounts for but not in this asset is ErrParticipantAssetNotFound, the
// same sentinel Bank.AccountsFor gives for the same question asked of the bank's
// own accounts: settling a dollar position through a euro reserve is the error
// both refusals exist to prevent.
func (s *Network) settlementAccountTx(ctx context.Context, tx Tx, bic iso20022.BIC, asset ledger.AssetCode) (ledger.AccountID, error) {
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
//
// Four acts, and each of them is one institution's unit of work: the bank
// founds itself, the settlement agent opens it an account in its own book, the
// clearing house writes the routing entry, and the bank records what it was
// told. No act writes another institution's row.
//
// They do not compose themselves, and nothing in this package composes them
// either. What runs them in order is a CONVERSATION: mesh.Mesh.Admit founds the
// bank and sends one acmt.007 per asset, and the other three acts run in the
// handlers of the messages that reach the institutions whose acts they are. The
// transitional call that ran all four in one transaction is gone, and with it
// the guarantee it bought — that a bank could never exist without the accounts
// it needs. That guarantee is what isolation takes away, and no real admission
// ever had it: a bank is licensed and built before any scheme has heard of it,
// and what follows is a request that can be refused.
//
// Each act appends ONE audit event, which is the whole of what remains of the
// atomic call's record. Four events across four commits, in three institutions'
// names — see appendAuditTx, and ledger's EventParticipantAdded and the three
// beside it.
// ---------------------------------------------------------------------------

// AdmissionRequest is a bank asking an account servicer to open it an account:
// who is asking, in which asset, and which admission the question belongs to.
//
// ONE asset, not a list, because it is what one acmt.007 says: the schema makes
// Acct/Ccy minOccurs="1" maxOccurs="1", so a bank clearing a euro scheme and a
// dollar scheme asks twice rather than once for two currencies. See
// iso20022.RequestedAccount.
//
// Ref is the admission's process id — acmt Refs/PrcId, echoed by every message
// of one admission. It is the conversation's only correlator, because the
// acknowledgement carries no back-reference to the request that caused it.
// OpenSettlementAccountTx does not read it — an account servicer opens what it
// is asked for; what reads it is the clearing house's refusal, against the
// reference on the roster entry (see AdmitMemberTx).
//
// What carries it from here into the acknowledgement is the SETTLEMENT AGENT,
// one hop on. OpenSettlementAccountTx answers with a SettlementMember, which has
// no reference on it, so the handler that holds the request and the answer at
// once is what copies the two together — mesh.centralBank.receiveAdmission,
// which builds the acmt.010's PrcId out of the acmt.007's.
//
// It is a plain struct and not a message. ReadAdmissionRequest is what builds
// one of these from an acmt.007, and AdmissionMessage is what renders one; both
// are in translate.go, because the acts below know nothing about the wire.
type AdmissionRequest struct {
	Name  string
	BIC   iso20022.BIC
	Asset ledger.AssetCode
	Ref   string
}

// AdmissionAcknowledgement is an account servicer answering: these are the
// accounts I hold for you.
//
// It carries EVERY account, where the request carries one asset, and the
// asymmetry is the schema's rather than this system's: acmt.010's
// AccountForAction1 is unbounded, so one acknowledgement lists the servicer's
// whole account set for that BIC. Both readers below rely on that — the clearing
// house learns which assets a member clears in from this map, and the bank
// records every account number it is told at once.
//
// Ref is the same process id the request carried. It is what tells the clearing
// house whether an acknowledgement arriving on a BIC it already routes to is the
// same admission going on or a different bank on a taken address.
//
// It names NO ONE, and that is the schema rather than an omission here. An
// acmt.010 identifies the account owner with an OrganisationIdentification29 —
// a BIC, an LEI, generic identifiers — and carries no legal name, no country
// and no address; the REQUEST names the applicant with an Organisation33 and
// the answer does not. So this type has a BIC and no name, which is what the
// message can be read into, and both of its readers get on without one:
// AdmitMemberTx writes routing, and RecordMembershipTx is a bank writing its own
// row, which knows its own name.
//
// It carried a Name briefly, filled by the clearing house from the application
// it had relayed, so that AdmitMemberTx could put one on the roster entry. Both
// are gone. See RosterEntry, which has the whole of that reversal.
type AdmissionAcknowledgement struct {
	BIC      iso20022.BIC
	Accounts map[ledger.AssetCode]ledger.AccountID
	Ref      string
}

// LodgementInstruction is a member bank asking its central bank to move cash
// onto the bank's own reserve account.
//
// It is the input to the receiving half and the output of the asking half, which
// is the shape AdmissionRequest has and for the same reason: the settlement agent
// holds no roster, has never heard of this system's bank ids, and can act only on
// what the message told it. Everything here comes off a camt.050.
//
// # Why the account and the BIC are both on it
//
// Account is the reserve account to credit, quoted by the bank that holds it —
// the number it learned from its admission acknowledgement, the way an account
// holder knows its own IBAN. BIC is the member asking. They are not redundant:
// the account is what the central bank POSTS to and the BIC is what it checks
// that account against, so a lodgement quoting another member's account number
// is refused rather than executed. See ReceiveLodgementTx.
//
// Agent is the central bank being asked, and it is here so that the receiving
// half can refuse a message addressed to a different servicer — the same check
// ReadLodgement makes against the header. A system with one central bank does not
// need it to route; it needs it to be able to say no.
//
// Ref is the request's own message identifier, and it is what the receipt quotes
// back. A camt.025 carries no amount and no account, so this is the whole of the
// correlation between the two halves; see Camt025.
//
// It is a plain struct and not a message. ReadLodgement builds one from a
// camt.050 and LodgementMessage renders one, both in translate.go, because the
// acts below know nothing about the wire.
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
//
// Status is one of two iso20022.TransactionStatus values and Reason is prose. The
// prose is the family's doing rather than this system's — camt.025's StsCd is a
// code set nothing here can check and its Desc is Max140Text — which is why
// payment's reasonTable gives the lodgement's refusals the empty code, exactly as
// it does the admission's. See RequestHandling.
//
// It carries NO amount and no account, because the message does not. That is not
// a narrowing: it is what forces the asking bank to post its own leg BEFORE it
// sends, since a receipt cannot tell it what to post. See LodgeReservesTx.
type LodgementReceipt struct {
	Ref    string
	Status iso20022.TransactionStatus
	Reason string
}

// Accepted reports whether the central bank credited the reserve.
//
// A method rather than a comparison at each call site, because the two callers
// that branch on it are in different packages and the code it compares against
// is one this system chose rather than one a schema pins. See RequestHandling on
// why ACSC is reused here from the payment family.
func (r LodgementReceipt) Accepted() bool {
	return r.Status == iso20022.TransactionStatusSettlementCompleted
}

// joiningAssets applies the joining default: a bank that names no assets joins
// with the euro.
//
// It is the default for the SET of assets a bank joins with and not for the
// asset of any account: every account created below is created with an asset
// somebody named. Two callers apply it — the bank's own act, and the
// composition that has to ask for one settlement account per asset in the same
// order — which is why it is a function rather than a line in each.
func joiningAssets(assets []ledger.AssetCode) []ledger.AssetCode {
	if len(assets) == 0 {
		return []ledger.AssetCode{"EUR"}
	}
	return assets
}

// admissionSequenceTx takes the network's identity counter before an admission
// act reads the row it decides from.
//
// # Without it the refusals were the store's and not the act's
//
// Every act that decides something from a read calls this first, which is all of
// them except FoundBankTx — that one allocates the bank's own id before it
// touches anything, which is the same lock under a different name. What each of
// them decides, and what it did without this:
//
//   - AdmitMemberTx reads the roster entry, compares its admission reference and
//     writes. Two DIFFERENT admissions of one address at once both read nothing
//     and both write, and the entry ends up naming whichever committed last.
//     60 runs in 60.
//
//   - OpenSettlementAccountTx reads the member row to decide whether it has
//     already opened an account for (BIC, asset). Two requests for one member in
//     two assets at once both read the same row, and each writes a map holding
//     only its own account — so the central bank opens two reserve accounts in
//     its own book and records one, leaving a liability account nothing points
//     at. 60 runs in 60.
//
//   - RecordMembershipTx reads the bank row and writes the settlement account
//     numbers onto it. Two recordings of one bank at once both read the row as
//     it was and both write it, so the loser's account numbers are lost — the
//     classic lost update, and it is what this act has instead of a state guard
//     since it stopped refusing a bank that is already a Member. 60 runs in 60,
//     measured when the guard was still there and both recordings were accepted.
//
// # Every one of those numbers needs a connection pool with two connections in it
//
// This is worth more than the numbers, because the next person to measure it
// will get the wrong answer the way the first attempt here did.
//
// pgxpool opens connections on demand and keeps them, so a store that has just
// been built and used once has exactly ONE idle connection. Two racers started
// against it are not concurrent at all: the first takes the connection and
// commits while the second is still opening a TCP connection, and the second
// then reads what the first wrote. Measured that way, RecordMembershipTx
// diverges 0 times in 60 and looks like the act that does not need this call.
// Hold four reads open at once first, so the pool really has two connections,
// and the same probe gives 60 in 60. AdmitMemberTx's clash is the same story
// less completely — between 41 and 55 of 60 across four cold samples, and 60 of
// 60 warm. Only OpenSettlementAccountTx's lost update shows 60 in 60 either way,
// because both racers do enough work to still be in flight together.
//
// An earlier version of this comment credited store/pg's deadlock retry for
// RecordMembershipTx holding — PutBank's upsert plus its per-asset child rows,
// losers deadlocking, Store.Update running the callback again against a bank
// that is Member by then. That does not happen. The callback ran exactly ONCE
// in all 240 Postgres runs measured across the four configurations, and the
// cold-pool loser simply ran its SELECT after the winner's COMMIT. store/pg's
// retry — its 40P01/40001 arm — was real and was never reached. The lesson
// survives the store: a retry that would explain an outcome has to be observed
// firing, not assumed from its existence.
//
// One more question rides along with the second: centralBankChartTx, which
// OpenSettlementAccountTx calls, resolves the central bank's chart of accounts
// find-or-create BY NAME, with no unique constraint behind it. Two callers that
// both find no Central Bank ledger both create one, and the members underneath
// them disagree about which subledger holds reserves — 60 runs in 60. The
// schema is where the absent constraint is argued (the ledgers table in
// store/sqlite/schema/0001_init.sql), and it used to close this by pointing at
// the first statement of the deleted AddParticipantTx, which drew an id before
// anything else ran. Nothing composes the four acts in one transaction
// any more — they are four commits with messages between them — so this call is
// what every one of them reaches the find-or-create behind. Both places point
// here.
//
// # What it is worth on the store that is left, measured
//
// Every number above is store/pg's, and store/pg is gone. store/mem serialized
// every Update on one process-wide mutex, so all of it was atomic there whatever
// the caller did — 0 in 60 on every case above, warm pool or cold, which is the
// whole difference — and store/pg ran READ COMMITTED, where two transactions
// both read "not there" and both write. A domain refusal that held on one store
// and not the other was not a refusal, and closing that is what this function
// was for.
//
// store/sqlite is neither of them. It admits ONE WRITER, so the loser of a
// read-then-write pair is refused at its write rather than let through, and
// Store.Update re-runs the unit of work — reading again, after the winner has
// committed, and meeting the domain's guard. Measured, with this function made
// to return nil: storetest's RunRaces passes ten runs out of ten, all four
// cases, on the ephemeral store and on a WAL file alike. So the ordering below
// is a SECOND guard here rather than the only one, and no test in this
// repository can see it go.
//
// It stays for two reasons. It costs one row write per act and holds the
// property without depending on the retry budget; and Task 18 has to decide
// where the counter it draws from lives, because ledger.NetworkBook — which
// this function allocates from — disappears with the split. If each entity's
// counter moves into that entity's own store, the ordering survives unchanged:
// each act's allocation and the row it decides from are still one database
// apart from nothing. If they land in two, neither the ordering NOR the retry
// spans them, because two databases is two transactions. Removing this now
// would decide that question by default.
//
// # Why an id allocation is the lock
//
// It is the ordering this repository already depends on twice — SubmitPaymentTx
// allocates before it reads the end-to-end index, FoundBankTx allocates before
// the composition touches anything — and it is the one store/storetest's
// ConcurrentReadThenWriteOnOneKeyAgrees states at the store interface.
//
// What makes an allocation a lock is that it WRITES. Under Postgres, NextID's
// INSERT … ON CONFLICT DO UPDATE took a row lock on id_sequences held until the
// transaction ended. Under SQLite the write is what makes this transaction the
// database's writer, so a second allocator waits there and then reads what the
// first committed — see store/sqlite's nextSeq, which is the same ordering
// arrived at from the database admitting one writer rather than from a row being
// locked.
//
// The number is discarded, and that is the whole of the difference from those
// two callers. Neither row an admission writes is keyed by an id — the
// identifier between these institutions is the BIC — so what is wanted here is
// the lock and not the number.
//
// # It leaves gaps in the network's numbering, and they are visible
//
// One counter serves every prefix within a book (see store/sqlite's NextID), so this
// advances the same counter that numbers banks, payments, mandates and cycles.
// What a euro-only admission draws from it is: the bank's own id, one here per
// act that decides from a read — one per settlement account asked for, one for
// the roster entry, one for the recording — and one per audit event, of which an
// admission appends four, one per act.
//
// So the gaps between consecutive banks are wide, and they widen again whenever
// an act is added or an act starts writing to the log. That is what the counter
// has always meant, since it interleaves every prefix in the book and doubles as
// a creation order, and it is why api's mesh tests resolve the ids they use from
// the seed's IBANs instead of naming them.
//
// A number is deliberately not quoted here. This paragraph used to name the
// seed's four banks — and did so twice, each time correctly when it was written
// and wrongly by the next task, because every one of the changes above moves
// them. What a reader wants is the list of things that draw an id, which is
// above and which stays true; what the seed's banks are numbered on any given
// day is a `ListBanks` away.
//
// Every one of these calls is now load-bearing rather than redundant. While one
// transaction composed the four acts, FoundBankTx's own allocation had already
// taken the lock before the other three ran, so their calls did nothing. Each act
// is its own commit at its own institution now, driven by a message, and there is
// no earlier allocation to ride on.
func (s *Network) admissionSequenceTx(ctx context.Context, tx Tx) error {
	_, err := tx.NextID(ctx, s.book(), "adm")
	return err
}

// The four acts, each in its own unit of work.
//
// One wrapper per act and no wrapper over all four, which is the shape of the
// split rather than an omission. Each act is ONE institution's work, and what
// composes them is a conversation between three of them: mesh.Mesh.Admit founds
// the bank and sends, and the other three run in the handlers of the messages
// that reach the institutions whose acts they are. A wrapper spanning the four
// would be a single unit of work again, which is exactly what an admission
// across a store boundary cannot be.
//
// They exist because a message handler holds no transaction. Everything else in
// this package that a handler calls has the same pair — SubmitPayment,
// AcceptInbound, SettleCycle — and for the same reason.

// FoundBank is FoundBankTx in its own unit of work: the bank's own act, and the
// only one of the four its own operator drives directly.
func (s *Network) FoundBank(ctx context.Context, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*Bank, error) {
	var out *Bank
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.FoundBankTx(ctx, tx, name, bic, assets)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OpenSettlementAccount is OpenSettlementAccountTx in its own unit of work: the
// settlement agent's act, driven by an acmt.007.
func (s *Network) OpenSettlementAccount(ctx context.Context, in AdmissionRequest) (SettlementMember, error) {
	var out SettlementMember
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.OpenSettlementAccountTx(ctx, tx, in)
		return err
	})
	return out, err
}

// AdmitMember is AdmitMemberTx in its own unit of work: the clearing house's
// act, driven by an acmt.010.
func (s *Network) AdmitMember(ctx context.Context, in AdmissionAcknowledgement) (RosterEntry, error) {
	var out RosterEntry
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.AdmitMemberTx(ctx, tx, in)
		return err
	})
	return out, err
}

// RecordMembership is RecordMembershipTx in its own unit of work: the bank's
// second act, driven by the acmt.010 the clearing house forwards.
func (s *Network) RecordMembership(ctx context.Context, in AdmissionAcknowledgement) (*Bank, error) {
	var out *Bank
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.RecordMembershipTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FoundBankTx is the bank's own act: a bank with a licence building itself.
//
// It creates the bank's book, its chart of accounts, one set of internal
// accounts per asset it means to operate in, and the deposit product every
// customer account is opened from — because there is no such thing as an
// unpriced deposit account, and a bank that cannot open one is not yet a bank.
//
// The bank comes out Founded, and that is a whole state rather than half of
// one. Its own book is unrestricted — it opens customer accounts, publishes
// products, adds ledgers. What it cannot do is FUND one: DepositTx raises the
// bank's reserve at the central bank in the same unit of work, and no settlement
// agent holds an account for it to raise, so the deposit is refused by name
// (ErrSettlementMemberNotFound). Nor is it in any routing directory, so nothing
// it takes part in can settle. That is what a bank is between its licence and
// its scheme membership, and it is what an interrupted admission leaves behind —
// which is why the guarantee the deleted atomic call gave up is one no real
// admission ever had.
//
// This used to say "it can take deposits", which is the one thing it cannot do,
// and "no clearing house routes to it", which is not a refusal anybody makes:
// mesh routes on its actor table rather than on the roster. mesh/doc.go's
// admission section records what that costs, measured.
//
// Assets[asset].Settlement is EMPTY on every set of accounts it writes. The
// settlement account is another institution's to open and its number is
// something this bank has to be told; RecordMembershipTx is where it lands.
//
// What it writes outside the bank's own BOOK is network-scoped storage rather
// than another institution's record: the id allocation, the Bank row itself —
// which names a book without being scoped to one, as
// TestWritingAParticipantTouchesNoBankBook pins — and one participant.added audit
// event, the audit log being network-scoped in this system by construction (see
// appendAuditTx).
//
// That event is about the FOUNDING and is silent about everything after it: the
// bank it carries is Founded and its settlement references are empty, because at
// the moment it is written no settlement agent has opened one. The other three
// acts each append their own, which is what makes an admission four events in
// the log rather than one — see the note above this block, and
// TestEachActOfAnAdmissionLeavesItsOwnAuditEvent.
func (s *Network) FoundBankTx(ctx context.Context, tx Tx, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*Bank, error) {
	if err := ledger.ValidateText("name", name); err != nil {
		return nil, err
	}
	// Validated at founding rather than at first use. A bank with a malformed
	// BIC is one the mesh cannot route to and one the other two institutions
	// cannot key their rows by — the BIC is the only identifier that crosses
	// between them — and the moment to refuse it is when the bank is built, not
	// when the first payment addressed to it fails somewhere else entirely.
	if err := bic.Validate(); err != nil {
		return nil, fmt.Errorf("bic: %w", err)
	}
	assets = joiningAssets(assets)

	// NOTHING IS ALLOCATED HERE. THE BANK'S ID IS ITS BIC.
	//
	// It used to be NextID(ledger.NetworkBook, "bank"), drawn from a counter every
	// institution shared, and that counter is gone. What replaced it is not
	// another counter, and the reason is worth having at the line that used to
	// draw one.
	//
	// A counter-derived id could not work at all. A bank's database is named by
	// its id, and the counter an id is drawn from is a row in id_sequences inside
	// that database — so allocating it would mean opening the store whose name is
	// the value being allocated. That knot is why FoundBank was called through the
	// CLEARING HOUSE's handle right up to this task: the joining bank had no
	// handle of its own yet. mesh.Mesh.Admit and store/storetest.Admit both did
	// it and both were recorded as deferrals.
	//
	// Siting the counter somewhere else would have been the wrong fix, because
	// the id had stopped meaning anything. It was doing two jobs — this bank's own
	// name for itself, and the NETWORK'S ADDRESS for it — and only the first
	// survives isolation: eight readers in mesh turned a participant id into a BIC
	// by reading a bank's row, and every one of them is a read into a database its
	// caller does not hold. Take the second job away and the first distinguishes
	// nothing, because a bank's database holds one bank. An id that is a constant
	// is not an identity.
	//
	// So there is one identifier and a joining bank arrives already knowing it.
	// s.self() is this network's identity, which is the BIC, which is this store's
	// one BookID — the value it was opened for and refuses every other of. See
	// banks in store/sqlite/schema/bank/0001_init.sql, where the argument is
	// recorded in the database.
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
	// A third subledger, and the reason it is not one of the two above is the
	// whole of what Task 18a's deposit change is about. Vault cash is not owed to
	// a customer, so it is not a Customer Deposit; and it is not a position
	// against another institution, so it is not Interbank. It is the bank's own
	// money, in its own hands — the one asset on this chart that is nobody else's
	// promise.
	//
	// It is created AFTER the other two on purpose. Subledgers are numbered in
	// creation order, so appending leaves every account number in the two
	// existing subledgers exactly where it was; inserting it above would
	// renumber a chart of accounts that fixtures and golden files already quote.
	treasury, err := bank.CreateSubledgerTx(ctx, tx, gl.ID, "Treasury")
	if err != nil {
		return nil, err
	}

	// One set of internal accounts per asset. Naming them with the asset in
	// parentheses keeps them apart in a chart of accounts that now holds
	// several of each.
	accounts := make(map[ledger.AssetCode]BankAccounts, len(assets))
	for _, asset := range assets {
		// Reject an unknown code before writing anything, rather than letting
		// the first CreateAccountTx below fail after part of the chart of
		// accounts already exists.
		if _, err := ledger.LookupAsset(asset); err != nil {
			return nil, err
		}
		if _, seen := accounts[asset]; seen {
			// A repeated code would otherwise create a second set of accounts
			// and then overwrite the map entry pointing at the first, orphaning
			// four accounts in the chart.
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
		// A Liability, because it is money the bank owes somebody it has not yet
		// identified — the same class as a customer's deposit, and specifically
		// not an asset of the bank's.
		unclaimed, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Unclaimed Balances ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return nil, err
		}
		// An Asset, and the contrast with Unclaimed Balances above is the
		// point. Unclaimed is money the bank OWES to somebody it has not
		// identified; this is money OWED TO the bank by somebody it has
		// identified perfectly well — a biller whose account could not fund a
		// refund the bank was obliged to honour anyway. Same event, opposite
		// sides of the balance sheet.
		returnsReceivable, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Returns Receivable ("+string(asset)+")", ledger.Asset, asset)
		if err != nil {
			return nil, err
		}
		// An Asset, and the one on this chart that is not a claim on anybody. A
		// reserve is a claim on the central bank and Returns Receivable is a claim
		// on a biller; this is cash the bank is holding, and it is where cash paid
		// in over the counter lands. See DepositTx, and BankAccounts.VaultCash on
		// why a founded bank has one before it has a settlement account.
		vaultCash, err := bank.CreateAccountTx(ctx, tx, treasury.ID, "Vault Cash ("+string(asset)+")", ledger.Asset, asset)
		if err != nil {
			return nil, err
		}
		accounts[asset] = BankAccounts{
			Suspense:          suspense.ID,
			Reserve:           reserve.ID,
			Unclaimed:         unclaimed.ID,
			ReturnsReceivable: returnsReceivable.ID,
			VaultCash:         vaultCash.ID,
		}
	}

	// The bank's default deposit product, created here because a bank with no
	// product cannot open an account: every deposit account is opened FROM one.
	// It belongs with the chart of accounts for the same reason those are built
	// here — founding a bank produces a bank that works.
	//
	// Its opening version is INTEREST-FREE, which is a real product and not an
	// absence: a bank that has not decided a price has not decided a price, and
	// pricing the overdraft is a later PublishVersion that reaches every account
	// already sold from it. A caller wanting something else creates its own
	// product through the Catalogue and passes it to OpenAccountTx.
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
		BookID:            bookID,
		CustomerSubledger: customers.ID,
		ProductID:         basic.ID,
		Status:            BankFounded,
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
// account, in one asset, in its own book, and recording that it holds it.
//
// It writes nothing of the bank's. What it produces is the central bank's
// SettlementMember row — the record whose absence would leave a settlement agent
// with its own database unable to post anything at all — and the account itself,
// a Liability, because a reserve is money the central bank owes its member.
//
// # Idempotent per (BIC, asset), not per BIC
//
// A request for an asset it has already opened returns the accounts it holds and
// opens none. A request for an asset it has not returns a member extended by
// one account. Both halves are needed and for different reasons: one acmt.007
// names one currency, so a bank in two schemes asks twice and the second ask
// must not be swallowed as a repeat; and an operator re-driving an admission
// that failed after the accounts were opened must not be given a second account
// that the first one's balance is already sitting in. That re-drive is the route
// out of a half-happened admission this system documents, so the second half is
// reachable in practice and not only in principle.
//
// The idempotency is this act's own and not a store's: it reads the member row
// and then writes it, which without something ordering the two is two callers
// both reading "not there". What orders them is the id drawn before the read —
// see admissionSequenceTx, which measures what the act does without it, on this
// store and on the two that came before.
//
// The name on the row is the one it was first opened under. An account servicer
// names an account after the member it opened it for, and a second request that
// renamed the row would leave the accounts under it named two different things.
//
// This is the ONLY institution besides the bank itself that holds a member's
// legal name, and it holds one because a message gave it one: an acmt.007 names
// the applicant in Org/FullLglNm. The clearing house holds none, because the
// acmt.010 its row is written from names nobody — see RosterEntry.
//
// # What it refuses, and what it does not
//
// An unknown asset code, because it is about to create an account denominated
// in it. Not the BIC and not the name: whether an address is well formed is the
// applicant's own act to establish (see FoundBankTx) and whether an applicant
// may hold this address at all is the clearing house's decision, made before a
// request is relayed. An account servicer asked twice for the same address by
// two different institutions cannot tell them apart, and this system's answer to
// that is a refusal one institution earlier rather than a weaker test here — see
// ErrBICAlreadyAdmitted.
//
// That leaves something for the relay to guarantee rather than nothing, and it
// is worth naming because this act is reached from a message. The BIC it is
// handed becomes the primary key of the settlement agent's own row —
// settlement_members.bic — so a request carrying a malformed or
// empty address writes a member row keyed by one, which no later message can
// address and no reader can find. What must not reach here is an address
// nothing validated: iso20022.BIC.Validate is what the reader of an acmt.007
// runs, in the actor that receives it, before this act is called. See
// ReadAdmissionRequest, which does exactly that and says so.
//
// # It appends one audit event, and only when it opens something
//
// settlement_account.opened, under the member's BIC, carrying the whole row.
// The audit log is this system's only immutable record and an admission is now
// four commits at three institutions, so each act writes its own — see the note
// above this block. A request for an asset already held returns above without
// an event, because nothing happened: an event there would make a redelivered
// acmt.007 indistinguishable in the log from a second account.
func (s *Network) OpenSettlementAccountTx(ctx context.Context, tx Tx, in AdmissionRequest) (SettlementMember, error) {
	// FIRST, and the placement is the fix rather than a style: this act returns
	// early for an asset the agent already holds an account in, so a guard
	// further down was reachable only on the path that opens something. A
	// redelivered acmt.007 answered a clearing house's network with a member row
	// and no refusal at all. Who is acting is not a question about how much work
	// is left.
	book, err := s.centralBankBook()
	if err != nil {
		return SettlementMember{}, err
	}
	if _, err := ledger.LookupAsset(in.Asset); err != nil {
		return SettlementMember{}, err
	}
	// Before the read below, and before centralBankChartTx's find-or-create.
	// See admissionSequenceTx: without it this act's idempotency is whatever the
	// store underneath happens to give it rather than something the act arranged
	// — which on store/mem was everything, on store/pg was nothing, and on
	// store/sqlite is measured.
	if err := s.admissionSequenceTx(ctx, tx); err != nil {
		return SettlementMember{}, err
	}

	member, err := tx.GetSettlementMember(ctx, in.BIC)
	switch {
	case err == nil:
		if _, held := member.Accounts[in.Asset]; held {
			return member, nil
		}
		if member.Accounts == nil {
			// This act never writes a member with no accounts, so a nil map here
			// is a row somebody else wrote. Assigning into one panics, and a
			// panic is a bad answer to a row this act can simply fill in.
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
		return SettlementMember{}, err
	}

	reserves, _, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return SettlementMember{}, err
	}
	// The other side of every reserve credit in this asset, in the central
	// bank's own capital block. One per asset and shared by every member, so
	// this is a lookup on the second admission in an asset and a creation on the
	// first.
	if _, err := s.centralBankAssetsAccountTx(ctx, tx, in.Asset); err != nil {
		return SettlementMember{}, err
	}
	account, err := book.CreateAccountTx(ctx, tx, reserves,
		"Reserve: "+member.Name+" ("+string(in.Asset)+")", ledger.Liability, in.Asset)
	if err != nil {
		return SettlementMember{}, err
	}

	member.Accounts[in.Asset] = account.ID
	if err := tx.PutSettlementMember(ctx, member); err != nil {
		return SettlementMember{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventSettlementAccountOpened, string(member.BIC), member); err != nil {
		return SettlementMember{}, err
	}
	return member, nil
}

// checkAcknowledgement refuses an acknowledgement neither act can act on.
//
// # It is ReadAdmissionAcknowledgement's refusals, made again in the acts
//
// That is the whole specification, and it is stated as a correspondence rather
// than as a list this function invented: everything the reader will not read off
// an acmt.010, the acts will not act on. The reader is the only guard a message
// meets, and the acts are separately callable — an institution doing its own
// unit of work is the point of the split — so a reader's guard that is the only
// line is not defence in depth. It is the rule Task 16e arrived at for
// ReadReturn and SettleReturnTx after an implementer found the hole outside its
// brief.
//
// The correspondence, refusal for refusal:
//
//	ReadAdmissionAcknowledgement          here
//	------------------------------------  ------------------------------------
//	OrgId/AnyBIC absent                   in.BIC.Validate
//	OrgId/AnyBIC malformed                in.BIC.Validate
//	Refs/PrcId/Id absent                  ErrAdmissionNotIdentified
//	AcctId empty                          ErrAdmittedAccountUnusable
//	AcctId[i]/Ccy absent                  ErrAdmittedAccountUnusable
//	AcctId[i]/Id/Othr/Id absent           ErrAdmittedAccountUnusable
//	two accounts in one currency          unrepresentable: Accounts is a map
//
// TestAnUnusableAcknowledgementIsRefusedByBothActs and
// TestAnAcknowledgementQuotingNoAdmissionIsRefusedByBothActs are what hold the
// right-hand column; TestReadAdmissionAcknowledgementRefusesAnAccountItCannotFile
// holds the left. A refusal added to the reader and not here is a row of this
// table with an empty right-hand side, which is how each of the three holes this
// function has grown to cover got in.
//
// # Why each of them, and what "" means
//
// The BIC and the reference are both values that a guard elsewhere reads as
// ABSENCE, which is what made them dangerous rather than merely untidy:
//
//   - an empty Bank.AdmissionRef means "this bank has accepted nothing yet", so
//     an acknowledgement quoting none resets that guard and hands the next
//     message — any message — the bank's settlement references. An empty
//     RosterEntry.AdmissionRef compares equal to every other empty one, so two
//     institutions on one address would extend a single entry.
//   - an empty or malformed BIC is the KEY of the roster row AdmitMemberTx
//     writes. Measured: `AdmitMemberTx bic="" -> entry={BIC: Assets:[EUR] …}` and
//     the same for "nonsense" — rows nothing can address and no reader can find.
//     RecordMembershipTx was already covered by its own comparison, since a
//     bank's own BIC is validated at founding and cannot equal either; the
//     clearing house had nothing.
//
// The accounts are the acknowledgement's whole content, and an empty list is the
// value that makes the per-account arms below not execute at all. Measured, it
// WEDGES both institutions: a Member that settles through no account and a
// roster entry that clears in no scheme, and then the true acknowledgement is
// refused for ever by the very guards those two rows now carry.
//
// # It is one function and it runs before the id
//
// One function and not lines in each act, because the two would otherwise refuse
// different things — on an empty currency the clearing house would write a
// member clearing in the empty asset and the bank would silently skip the
// account; on an empty reference the clearing house would merge two institutions
// and the bank would forget it had been admitted. Same message, two answers, and
// in each pair only one of them visible.
//
// It reads no store, so it runs BEFORE admissionSequenceTx rather than after: a
// message this system cannot act on should not cost an identity from the
// network's counter, and there is nothing here for that lock to protect.
//
// What it does NOT check is which bank an acknowledgement is FOR. That is
// RecordMembershipTx's own comparison against the bank's own address
// (ErrNotThisBanksAdmission), and it is a different question — whether the
// message is actionable at all, against whose message it is.
//
// Nor does it check what an act would WRITE. This table's rows are all about the
// message, and a message can be perfectly readable and still leave one of the two
// acts wrong: an acknowledgement in an asset the bank operates in none of, or one
// moving a settlement account the bank already holds, are both refused inside
// RecordMembershipTx and belong to no row here. Adding them would make this table
// stop being the correspondence it claims to be — and, since the clearing house
// legitimately records assets the bank has no accounts in, would refuse the
// clearing house for the bank's reason.
func checkAcknowledgement(in AdmissionAcknowledgement) error {
	if err := in.BIC.Validate(); err != nil {
		return fmt.Errorf("payment: this acknowledgement names no account owner this system can address: %w", err)
	}
	if in.Ref == "" {
		return fmt.Errorf("%w: it is addressed to %s", ErrAdmissionNotIdentified, in.BIC)
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
//
// It writes the entry from an acknowledgement it did not originate, and that is
// the ordering the domain has rather than an accident of who holds the message.
// Scheme membership follows the settlement account: a bank the settlement agent
// will not open an account for is not a bank this clearing house can route a
// settlement instruction for. So the assets it records are the assets the
// servicer says it opened accounts in, and nothing here asks the bank what it
// wanted.
//
// # It writes or extends, and refuses only a different admission
//
// Two things legitimately arrive on a BIC already in the roster: this same
// bank's next currency, and an operator re-driving an interrupted admission.
// Both echo the process id the admission started with, so both extend the entry
// they find. An acknowledgement quoting a different reference is a second
// institution on a taken address, which is the one case this refuses. See
// ErrBICAlreadyAdmitted, and RosterEntry.AdmissionRef, which is what makes the
// distinction possible at all.
//
// It refuses before it writes, so a refused acknowledgement leaves routing
// pointing where it pointed — and it draws an id before it reads, which is what
// makes the refusal binding rather than a race two callers can both win. See
// admissionSequenceTx.
//
// An acknowledgement this act cannot use is refused before any of the above, by
// checkAcknowledgement, which is ReadAdmissionAcknowledgement's refusals made
// again in the act. Two of them decide this act's own guards rather than merely
// tidying the row: an empty reference, because two institutions on one BIC both
// quoting "" compare equal and the refusal above would never fire; and an empty
// or malformed BIC, because this row is KEYED by it — measured at
// `entry={BIC: Assets:[EUR] …}`, a row nothing can address.
//
// # What it can leave behind, enumerated
//
// RecordMembershipTx needed two guards beyond the message's because the state it
// writes is not the message it reads; this act was enumerated the same way and
// needed none, which is worth recording rather than leaving as an absence.
// checkAcknowledgement guarantees a non-empty account list in which every asset
// and every identifier is non-empty, so the loop below always appends at least
// one asset and this row can never be written clearing in nothing. It only
// APPENDS, so no value already on the entry is replaced and there is no
// equivalent of the moved-account case. The reference and the address are the
// row's other two fields and both are refused empty before this runs.
//
// What it CAN leave is an entry naming an asset the bank holds no internal
// accounts in, and that is deliberate rather than unguarded: the assets are the
// servicer's answer about its own book, and this institution has no business
// asking the bank what it wanted. The bank's own act declines such an account
// for itself. The consequence is real and it is a clearing house routing a
// scheme its member cannot settle in, which is why AcceptAtCSMTx reads
// RosterEntry.Assets before it takes a payment into a cycle — see
// bothBanksAreMembersTx.
//
// # It writes an ADDRESS and not a description
//
// Everything on the entry comes off the acknowledgement, and there is nothing on
// the acknowledgement this act declines to use. That was not true while the row
// carried a NAME: acmt.010 identifies the account owner by BIC and carries no
// legal name, so the name had to be kept by the institution driving this act and
// handed in beside the message. Both the field and the keeping are gone — see
// RosterEntry, which records the whole of that reversal — and what is left is a
// row a clearing house can write from one message and nothing else.
//
// # It appends one audit event
//
// member.admitted, under the member's BIC, carrying the whole entry. It is
// appended on an EXTENSION as well as on a creation, because an extension is a
// real change to what this institution routes: a second asset admitted is a
// second scheme this member clears in.
func (s *Network) AdmitMemberTx(ctx context.Context, tx Tx, in AdmissionAcknowledgement) (RosterEntry, error) {
	// The message first, because this reads no store and a message this act
	// cannot use should not cost an identity from the network's counter.
	if err := checkAcknowledgement(in); err != nil {
		return RosterEntry{}, err
	}
	// Then the id, before the read the refusal is decided from. See
	// admissionSequenceTx: without it, two different admissions of one address
	// were both accepted on store/pg and the entry recorded whichever committed
	// last — the impostor half the time — and that is what this ordering was put
	// here for.
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
	case errors.Is(err, ErrRosterEntryNotFound):
		entry = RosterEntry{BIC: in.BIC, AdmissionRef: in.Ref, AdmittedAt: s.now()}
	default:
		return RosterEntry{}, err
	}

	// Sorted, and only the assets that are new. The sort is this writer's rather
	// than the row's contract: Accounts is a map, Go randomises its iteration,
	// and an unsorted append would make two identical acknowledgements store two
	// different orders. Appending rather than replacing is what makes an
	// extension an extension — the entry keeps the order it was admitted in and
	// the new asset goes on the end.
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
//
// The bank learns its settlement account numbers here and nowhere else. They are
// another institution's account ids, and this is the account holder's note of
// them — the way a customer knows their own IBAN without holding the bank's
// ledger. The bank becomes a Member in the same write, because being told the
// account exists is exactly what being admitted consists of.
//
// # The bank is this network's identity, and the domain still has to check
//
// The bank claiming the acknowledgement is no longer something the caller says;
// it is which member's network this is. That closes one hole and leaves the
// original one exactly where it was. A bank that recorded whatever arrived would
// write another member's account numbers onto its own row, and every reserve
// movement it made afterwards would name an account it does not hold — so the
// check below still compares the message's address against this bank's own BIC.
// It has to: the identity says which member is acting, and nothing about the
// MESSAGE follows from that. This is ErrStatementNotForThisBank's argument one
// flow over, and the check is the same one.
//
// What the identity removes is the case that check could never make: the
// acknowledgement recorded by an institution that is not a member bank at all.
//
// # It records or extends, and refuses only a different admission
//
// A bank that is already a Member is EXTENDED rather than refused, and this used
// to be the other way round: the act took only a Founded bank, and a second
// acknowledgement was ErrBankNotFounded. That refusal was measured wrong. One
// acmt.007 asks for one currency, so a two-asset admission produces two
// acknowledgements; the first takes the bank to Member and the second met a
// Member and was refused — leaving the bank a Member with a dollar settlement
// reference it never learned, while the central bank held a dollar reserve for
// it. DepositTx in dollars then failed against a reserve the operator console
// cheerfully reported, because the console reads the central bank's row.
//
// So the state guard went, and for one round nothing replaced it. That was
// wrong, and the argument for it was worse than the omission: it said a bank has
// no contender, because the acknowledgement has already been checked to name
// this bank's own BIC. The BIC answers WHICH BANK. It does not answer WHICH
// ADMISSION, and two admissions can quote one BIC — which is the entire premise
// of RosterEntry.AdmissionRef, in roster.go.
//
// Measured: an acknowledgement naming this bank's own BIC, quoting an admission
// reference this bank had never heard of, and carrying an invented account,
// moved a Member bank's euro settlement reference off the central bank's real
// account and onto the forged one. The bank's row then disagreed with the
// settlement agent's about which account it holds, and DepositTx reads the
// bank's.
//
// What refuses it is Bank.AdmissionRef: the reference this bank itself accepted,
// compared against the one on the message. It is not the clearing house's guard
// duplicated. That one decides between two INSTITUTIONS contending for an
// address, from a row this institution does not own and which Task 18 moves to
// another database; this one is a bank comparing a message against its own
// memory, and it needs nobody else's store to make it. Today the only thing
// stopping a forged acknowledgement reaching this act is AdmitMemberTx one
// institution earlier — which is exactly the guard the split takes away.
//
// A bank that is still Founded accepts whatever reference arrives and records
// it, because it has accepted none. That is what makes an operator re-driving an
// interrupted admission work: Mesh.Admit mints a new process id, and a bank with
// nothing recorded has nothing to disagree with.
//
// What record-or-extend buys, and what the guard does not take back, is that the
// act is idempotent in the way messages require: a redelivered acknowledgement
// writes the same row, and a second asset's acknowledgement — same admission,
// same reference — adds a reference the bank did not have. The id drawn before
// the read is here for the reason it is in the other two acts: two recordings of
// one bank racing on store/pg both read the row and both wrote it, and the
// loser's account numbers were lost. See admissionSequenceTx, which is where
// what that ordering is worth today is measured.
//
// # An account for an asset this bank does not operate in is not recorded —
// unless that is all of them
//
// The acknowledgement lists every account the servicer holds for the address,
// which can be more than this bank has internal accounts for. There is nowhere
// to put such a number — a settlement reference lives on the set of internal
// accounts for its asset, and there is no such set — and it is not an error
// either, because the servicer is answering about its own book rather than
// about this bank's. What the bank records is what it can use.
//
// What it CANNOT be is all of them. An acknowledgement every account of which is
// skipped that way would still take the bank to Member and still burn its
// AdmissionRef, leaving a member that settles through nothing and refuses its own
// true acknowledgement for ever — which is exactly the wedge checkAcknowledgement
// refuses an EMPTY account list for, reached with a non-empty one. So the act
// counts what it filed and refuses zero, with the same sentinel, because it is
// the same fact about the message: it names no account this bank can use.
//
// # The two guards below are about the STATE, not the message
//
// checkAcknowledgement is a correspondence table against
// ReadAdmissionAcknowledgement and is complete against it, refusal for refusal.
// Neither of these is a message that reader would refuse, and neither is
// derivable from that table: they are what this ACT would leave behind. Written
// by enumerating the outcomes rather than the inputs — a Member with a settlement
// reference it never learned, and a Member with a reference moved off the account
// the settlement agent actually holds — because deriving from the other side's
// list is what left the first three holes this act has had.
//
// The moved-account arm is ErrSettlementAccountReplaced, and it compares rather
// than forbids: an acmt.010 lists every account the servicer holds for the
// address, so a redelivery and a second currency's answer both repeat accounts
// already recorded, and equal is an extension. See that sentinel for what a
// different value cost when nothing compared it.
//
// An acknowledgement this act cannot use at all is refused before either, by
// checkAcknowledgement — which both acts driven from an acknowledgement run, and
// which is what keeps AdmissionRef's empty value meaning one thing.
//
// # It appends one audit event
//
// membership.recorded, under the bank's own id, carrying the bank as it now
// stands. It is the pair to the participant.added FoundBankTx wrote, and the
// reason that one can be silent about everything the founding did not know: the
// settlement account numbers are on this event and on no other.
func (s *Network) RecordMembershipTx(ctx context.Context, tx Tx, in AdmissionAcknowledgement) (*Bank, error) {
	self, err := s.self()
	if err != nil {
		return nil, err
	}
	// The message first, for AdmitMemberTx's reason: this reads no store, and a
	// message this act cannot use should not cost an identity.
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
	// Whose acknowledgement this is. A bank handed somebody else's message has
	// been misrouted, and after this line everything the message says is about
	// this bank's own accounts at its own settlement agent.
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

	// And WHICH ACCOUNTS. The two guards above are about the message's
	// identifiers; these two are about the STATE this act would leave behind,
	// which is not the same list and was twice found not to be. In asset order,
	// so that an acknowledgement conflicting in two assets is refused about the
	// same one on every run — Accounts is a map and Go randomises it.
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
	bank.Status = BankMember
	bank.AdmissionRef = in.Ref
	if err := tx.PutBank(ctx, *bank); err != nil {
		return nil, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventMembershipRecorded, string(bank.ID), *bank); err != nil {
		return nil, err
	}
	return bank, nil
}

// Deposit takes cash in over the counter: the bank holds the notes, and it owes
// the depositor their balance.
//
// One book, one pair of entries, one institution:
//
//	bank ledger:    Debit  Vault Cash (asset)  / Credit customer (liability)
//
// Which vault moves is decided by the funded account's own asset, read here
// rather than chosen by the caller. Cash paid into a dollar account raises the
// bank's dollar vault; there is nothing for a caller to pick and therefore
// nothing to default, and the two legs cannot end up in different assets.
//
// # This used to reach the central bank's ledger, and that was crossing 6
//
// A deposit was modelled as the bank placing the cash on reserve, which took two
// postings in two books inside one unit of work — Debit Reserve at Central Bank /
// Credit customer here, and Debit Settlement Assets / Credit Reserve: <bank>
// there. The second was a member bank writing in the settlement agent's ledger,
// and it was the last crossing in sub-project 8's table that never became a
// message: funding arrives from an operator or a fixture, with no institution
// behind it, so no recorder assertion built on booksTouchedBy could see it.
//
// What was wrong with it is not only the store. It said that cash cannot be paid
// in at a bank that has not joined a payment scheme, which is false about banking
// — a bank's counter has nothing to do with its central bank account — and the
// refusal it produced (ErrSettlementMemberNotFound) was a settlement agent's
// sentinel answering a question about a customer's money.
//
// Both halves are fixed by the same change. Cash becomes VAULT CASH, which every
// founded bank has from FoundBankTx; and moving it onto reserve becomes a
// LODGEMENT, which is a camt.050 to the central bank and its camt.025 back. See
// LodgeReservesTx, and Camt050 on why a lodgement is a conversation and a deposit
// is not.
func (s *Network) Deposit(ctx context.Context, participant ParticipantID, account deposit.AccountID, amount ledger.Amount, description string) error {
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return s.DepositTx(ctx, tx, participant, account, amount, description)
	})
}

// DepositTx is Deposit within a caller-supplied unit of work.
//
// It names no account outside this bank's book and asks the central bank nothing.
// The read that used to be here — Bank.Assets[asset].Settlement, the bank's own
// record of its settlement account number — went with the posting it was for:
// there is no leg at the central bank to resolve an account for. What still reads
// that field is LodgeReservesTx, which is where quoting one's own account number
// belongs, because that is the act with a counterparty.
func (s *Network) DepositTx(ctx context.Context, tx Tx, participant ParticipantID, account deposit.AccountID, amount ledger.Amount, description string) error {
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
	// A closed account is not somewhere money may land. Without this the cash
	// posts cleanly and strands: Close required a zero balance, no withdrawal
	// can reach the credit afterwards, and closing again cannot clear it because
	// Closed is terminal. Checked in this same Tx as the postings below, so an
	// account cannot close between the check and the credit.
	if err := p.Deposit.CheckCreditTx(ctx, tx, account); err != nil {
		return err
	}
	gl, asset := funded.GLAccount, funded.Asset
	accts, err := p.AccountsFor(asset)
	if err != nil {
		return err
	}
	// There is no guard on the settlement account here, and its absence is the
	// change rather than an oversight.
	//
	// This used to refuse a bank whose Assets[asset].Settlement was empty, with
	// ErrSettlementMemberNotFound and the sentence "is founded and not yet
	// admitted, so it has no reserve to fund". It was a true description of what
	// the code then did — the second posting named that account, so an empty one
	// had to be caught before the central bank's ledger answered "account not
	// found" about what reads as the customer's account — and it was a false
	// statement about banking. A bank's counter does not depend on its central
	// bank account. A founded bank can take cash.
	//
	// The guard is not deleted so much as MOVED, to the act it was always true
	// about: LodgeReservesTx cannot place cash on reserve at an agent that holds
	// no account for this bank, and refuses there with that sentinel. The
	// imprecision the old sentence carried — it said "founded and not yet
	// admitted" of a bank that may be a Member in a different asset — is gone with
	// the sentence rather than fixed in place, which is what closes it as a
	// defect.
	_, err = p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		Entries: []ledger.Entry{
			{AccountID: accts.VaultCash, Amount: amount, Direction: ledger.Debit},
			{AccountID: gl, Amount: amount, Direction: ledger.Credit},
		},
	})
	return err
}

// ---------------------------------------------------------------------------
// The lodgement: cash becomes reserves, in two books, over a message
// ---------------------------------------------------------------------------

// LodgeReserves is a bank swapping vault cash for a claim on its central bank.
//
// It is the asking half, and it is the bank's own posting plus the instruction it
// then sends:
//
//	bank ledger:  Debit  Reserve at Central Bank (asset)  / Credit Vault Cash (asset)
//
// The matching pair is the CENTRAL BANK's and lands in the central bank's own
// book, from the camt.050 this returns — see ReceiveLodgementTx. Two books, two
// units of work, one message each way, which is what closes crossing 6.
//
// # It posts and instructs together, for SubmitAndInstruct's reason
//
// One act and not two. Building the instruction can fail — a bank that cannot
// name its own settlement account — and a refusal reported after the leg was
// posted would leave the bank's reserve mirror raised for a lodgement nobody was
// ever asked to perform. Posting the leg and rendering the message now commit or
// roll back together; the SEND still happens after, which is the property
// TestARolledBackSubmitSendsNothing pins for a payment and
// TestARolledBackLodgementSendsNothing pins here.
//
// # Why the bank posts before it is answered, and what that costs
//
// Because a camt.025 carries no amount. The receipt names the request and says
// what became of it, and there is no element on it for how much was moved, so a
// bank cannot post its own leg FROM the answer. The two ways out are to post
// first, or to remember the outstanding request in the actor until the answer
// arrives — and the second is the shape csm.held already has, whose known defect
// is that it does not survive a restart. So: post first.
//
// What that costs is an interval in which the bank's own Reserve at Central Bank
// says more than the central bank's book does. That is not a new class of defect
// here; it is the unreconciled position this system already models and already
// documents (see BankAccounts), the same interval a cut-off opens between the
// reserve moving and the member booking its camt.053. What is different is that a
// REFUSED lodgement never closes it, and nothing in 18a detects that. The guard
// below is what makes it unreachable rather than merely unlikely, and Task 18e's
// reconciliation harness is the instrument that would find it if it happened.
func (s *Network) LodgeReserves(ctx context.Context, asset ledger.AssetCode,
	amount ledger.Amount, mc MessageContext) (LodgementInstruction, iso20022.Envelope, error) {

	var (
		in  LodgementInstruction
		env iso20022.Envelope
	)
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		in, env, err = s.LodgeReservesTx(ctx, tx, asset, amount, mc)
		return err
	})
	if err != nil {
		return LodgementInstruction{}, iso20022.Envelope{}, err
	}
	return in, env, nil
}

// LodgeReservesTx is LodgeReserves within a caller-supplied unit of work.
//
// # The guard the deposit used to make
//
// A bank whose Assets[asset].Settlement is empty is refused here with
// ErrSettlementMemberNotFound. That check used to live in DepositTx, where it
// said a founded bank could not take cash — true of the code and false of banking
// — and this is the act it was always correct about: a bank that cannot name its
// own reserve account cannot ask for a credit to it, and the central bank has no
// account to post to.
//
// It also makes the central bank's own refusal unreachable in practice, which is
// what keeps the interval described above closed. Nothing writes a bank's
// settlement reference before the agent's account exists — RecordMembershipTx
// runs on the acknowledgement, and the account is opened before that
// acknowledgement is built — so a bank able to pass this guard is one the agent
// certainly holds an account for. The disagreement is one-directional, and this
// is the direction that is safe.
//
// # It does not refuse a bank with too little cash, and does not need to
//
// Vault Cash is an Asset, and ledger.Book guards Asset accounts against going
// negative, so lodging more than the vault holds is refused by the ledger with
// ErrInsufficientBalance — which borrowedReasons already maps to AM04, "the
// account cannot cover this". A guard here would be a second copy of a rule the
// ledger states better, and it would have to be kept in step with it.
func (s *Network) LodgeReservesTx(ctx context.Context, tx Tx, asset ledger.AssetCode,
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
//
// It is the receiving half of the same conversation, and it posts in the central
// bank's own book and in no member's:
//
//	central bank:  Debit  Settlement Assets (asset)  / Credit Reserve: <member> (asset)
//
// That is the same pair the deposit used to post here from inside the BANK's unit
// of work. What has changed is who posts it and on whose instruction — which is
// the whole of the crossing, and the reason this method is on settlementOps and
// on no other interface.
func (s *Network) ReceiveLodgement(ctx context.Context, in LodgementInstruction) (LodgementReceipt, error) {
	var out LodgementReceipt
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ReceiveLodgementTx(ctx, tx, in)
		return err
	})
	if err != nil {
		return LodgementReceipt{}, err
	}
	return out, nil
}

// ReceiveLodgementTx is ReceiveLodgement within a caller-supplied unit of work.
//
// # It reads its OWN member row, not the bank's
//
// The instruction quotes an account number, and this act does not trust it. It
// asks its own settlement_members row which account it holds for this BIC in this
// asset (settlementAccountTx) and posts to THAT, then checks the quoted number
// against it. A servicer that posted to whatever account a message named would
// credit another member's reserve on request, which is the one thing an account
// servicer must never do.
//
// So the quoted account is a CHECK and not a lookup, and the difference is worth
// stating because the field looks like a lookup key. A mismatch means the member
// and the agent disagree about which account this is, and that disagreement is
// refused rather than resolved in either direction.
//
// # What it refuses, and why each is answerable
//
// A BIC it holds no account for is ErrSettlementMemberNotFound; an asset it holds
// no account for that member in is ErrParticipantAssetNotFound. Both come from
// settlementAccountTx, and both are the true state of an agent asked to credit an
// account it does not keep. A quoted account that is not the one it holds is
// ErrSettlementAccountReplaced, reused from admission because it is the same
// disagreement about the same field.
//
// Each becomes a REFUSING camt.025 rather than an error to the caller, and the
// prose it travels as is the receipt's Desc. See mesh.centralBank.receiveLodgement.
//
// # It is idempotent on the request's reference
//
// A queue redelivers, so a lodgement can arrive twice. The idempotency key is the
// asking bank's own message identifier, which is the only durable trace of a
// lodgement anywhere in this system: there is no lodgement row, here or at the
// bank. That is ErrReturnAlreadySettled's shape — a redelivered act caught by the
// key on the posting it would repeat, in the book of the institution that made it
// — and it is why this act needs no table of its own.
func (s *Network) ReceiveLodgementTx(ctx context.Context, tx Tx, in LodgementInstruction) (LodgementReceipt, error) {
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
	// An instruction quoting NO account is refused, and the `in.Account != "" &&`
	// that used to precede this comparison is what made the doc above false about
	// its own check: an empty quoted account walked past the guard that exists to
	// compare it. That is the shape of the escape ErrBICAlreadyAdmitted's note
	// records — a guard closing a hole and leaving its own "does not apply" value
	// open — and here the value was reachable, because ReceiveLodgement is
	// exported on settlementOps and callable with an instruction nobody parsed.
	//
	// Nothing was ever misdirected by it: the account posted to is this agent's
	// own row and never the quoted one, which is the whole point of reading
	// settlementAccountTx first. What was lost is the ASSERTION. A camt.050 says
	// which account it means, ReadLodgement refuses one that does not
	// (CdtrAcct/Id/Othr/Id), and a member and its agent silently disagreeing
	// about which account a lodgement credits is exactly what this comparison is
	// for.
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
func (s *Network) CreateMandate(ctx context.Context, debtor, creditor PartyRef, maxAmount ledger.Amount) (Mandate, error) {
	var out Mandate
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.CreateMandateTx(ctx, tx, debtor, creditor, maxAmount)
		return err
	})
	return out, err
}

// CreateMandateTx is CreateMandate within a caller-supplied unit of work.
//
// # It is the CREDITOR's bank's act, and it reads one register
//
// It used to run checkPartyTx for BOTH parties, which is one unit of work
// reading two banks' deposit registers — a crossing the spec's table never
// listed and 18a did not close. What replaces it is the shape Task 14 and 18a
// gave the payment path: this bank checks its OWN customer and records what it
// is told about the other.
//
// Which bank that is follows from the domain rather than from convenience. In
// SEPA the CREDITOR holds the mandate; SDD.ValidateMandate has said so since the
// pull flow landed and it is the creditor's bank that checks one at submission,
// so a mandate stored anywhere else would be a record its own validator has to
// reach across an entity boundary to read. A network that is not the creditor's
// bank is ErrNotThisBanksMandate.
//
// # The asset check moved, and it is a real loss of earliness
//
// Both ends of a mandate must be denominated in the same asset: MaxAmount is one
// integer, and an integer that means one thing at the debtor's scale and another
// at the creditor's is not a limit on anything. This act used to compare the two
// accounts here — which is exactly the read it no longer has, because the
// debtor's account is at another bank.
//
// It is not unchecked, it is checked later and by the bank that can. The old
// comment already said what happens without it: submission refuses every payment
// such a mandate could authorise, because each leg is checked against the
// SCHEME's asset by its own bank and these two cannot both match it. That
// refusal is the debtor's bank's, on the pacs.003, in AcceptInboundTx's pull arm
// — which is where a real MD01 would come from too.
//
// So what is lost is the moment, not the guard: a mismatched mandate is created
// and refuses its first collection, instead of being refused at creation. That
// is worse for an operator and it is the honest price of the boundary, and it is
// worth naming rather than discovering — this doc used to argue the opposite way
// round, that checking here "only makes the refusal happen where it can be
// understood instead of at first use".
func (s *Network) CreateMandateTx(ctx context.Context, tx Tx, debtor, creditor PartyRef, maxAmount ledger.Amount) (Mandate, error) {
	self, err := s.self()
	if err != nil {
		return Mandate{}, err
	}
	if creditor.Participant != self {
		return Mandate{}, fmt.Errorf("%w: %s is asked to record a mandate whose creditor banks at %s",
			ErrNotThisBanksMandate, self, creditor.Participant)
	}
	creditorAcct, _, err := s.checkPartyTx(ctx, tx, "creditor", creditor)
	if err != nil {
		return Mandate{}, err
	}
	// The DEBTOR is validated for shape and not for existence. Its account is at
	// another bank, so "does it exist" is a question this one cannot ask; what it
	// can insist on is that the reference names a participant and an account at
	// all, which is what a mandate needs to be comparable against a payment
	// (PartyRef.SameParty). See ErrNotThisBanksMandate.
	if err := validateParty("debtor", debtor); err != nil {
		return Mandate{}, err
	}
	// ValidateText accepts the empty string — it refuses control characters and
	// invalid UTF-8 and nothing else — so the presence check is separate and
	// explicit. A mandate whose debtor names neither a bank nor an account
	// authorises debits from nothing, and SameParty would match it against any
	// payment quoting the same emptiness.
	if debtor.Participant == "" || debtor.Account == "" {
		return Mandate{}, fmt.Errorf(
			"payment: a mandate names the account it authorises debits from; this one quotes participant %q and account %q",
			debtor.Participant, debtor.Account)
	}

	id, err := tx.NextID(ctx, s.book(), "mnd")
	if err != nil {
		return Mandate{}, err
	}
	m := Mandate{
		ID:        MandateID(id),
		Debtor:    debtor,
		Creditor:  creditor,
		Asset:     creditorAcct.Asset,
		MaxAmount: maxAmount,
		Status:    MandateActive,
		CreatedAt: s.now(),
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
func (s *Network) RevokeMandate(ctx context.Context, id MandateID) error {
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return s.RevokeMandateTx(ctx, tx, id)
	})
}

// RevokeMandateTx is RevokeMandate within a caller-supplied unit of work.
//
// Only the creditor's bank may revoke, for the reason only it may create: the
// row is its own. A real debtor revokes a mandate by telling their creditor, or
// their own bank, which then tells the creditor's — a conversation this system
// does not model and which would be a message rather than a second writer of one
// row. See ErrNotThisBanksMandate.
func (s *Network) RevokeMandateTx(ctx context.Context, tx Tx, id MandateID) error {
	self, err := s.self()
	if err != nil {
		return err
	}
	m, err := tx.GetMandate(ctx, id)
	if err != nil {
		return err
	}
	if m.Creditor.Participant != self {
		return fmt.Errorf("%w: %s is asked to revoke a mandate whose creditor banks at %s",
			ErrNotThisBanksMandate, self, m.Creditor.Participant)
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
func (s *Network) OpenCycle(ctx context.Context, scheme SchemeID) (ClearingCycle, error) {
	var out ClearingCycle
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.OpenCycleTx(ctx, tx, scheme)
		return err
	})
	return out, err
}

// OpenCycleTx is OpenCycle within a caller-supplied unit of work. The
// "already open?" check and the write are one step, so two concurrent callers
// cannot both open a cycle for the same scheme.
func (s *Network) OpenCycleTx(ctx context.Context, tx Tx, scheme SchemeID) (ClearingCycle, error) {
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
		NetPositions: map[ParticipantID]ledger.Amount{},
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

// CloseCycle reaches the cut-off: it computes each participant's net position
// across the cycle's payments and marks the payments Cleared. No money moves
// yet — that happens at SettleCycle.
func (s *Network) CloseCycle(ctx context.Context, id CycleID) (ClearingCycle, error) {
	var out ClearingCycle
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.CloseCycleTx(ctx, tx, id)
		return err
	})
	return out, err
}

// CloseCycleTx is CloseCycle within a caller-supplied unit of work.
func (s *Network) CloseCycleTx(ctx context.Context, tx Tx, id CycleID) (ClearingCycle, error) {
	c, err := tx.GetCycle(ctx, id)
	if err != nil {
		return ClearingCycle{}, err
	}
	if c.Status != CycleOpen {
		return ClearingCycle{}, ErrCycleNotOpen
	}

	net := map[ParticipantID]ledger.Amount{}
	for _, pid := range c.PaymentIDs {
		p, err := tx.GetPayment(ctx, pid)
		if err != nil {
			return ClearingCycle{}, err
		}
		// Money flows debtor -> creditor regardless of scheme direction.
		net[p.Debtor.Participant] -= p.Amount
		net[p.Creditor.Participant] += p.Amount
		if err := transition(&p, Cleared); err != nil {
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
	// The per-payment events come first and cycle.closed last, so the log reads
	// in the order the work happened; all of them share this transaction, so a
	// cut-off that fails leaves none of them behind.
	if err := s.appendAuditTx(ctx, tx, ledger.EventCycleClosed, string(c.ID), c); err != nil {
		return ClearingCycle{}, err
	}
	return c, nil
}

// SettleCycle settles a closed cycle: it moves each participant's net position
// across the members' reserve accounts at the central bank, in ONE transaction,
// in the central bank's own book.
//
// That is the whole of what it posts. Neither of the two legs in a member's own
// book is here any more:
//
//   - the MIRROR leg — a member's suspense moving against its own reserve — left
//     in Task 15b.2, and what this returns beside the settlement is the
//     STATEMENTS that tell each member what to post. See PostSettlementAdviceTx.
//   - the CREDITOR leg — a payee's funds released out of that payee's bank's
//     suspense — left in Task 15b.3, on the clearing house's per-payment advice.
//     See PostCreditorLegTx.
//
// So this reads a cycle, the bank rows, its own SettlementMember rows and its
// own book, and no payment at all, which is the whole of what a settlement agent
// has.
//
// What it takes off the BANK rows is not the settlement account number, and this
// sentence used to say it was. That number is the agent's own and comes off the
// agent's own row, keyed by BIC, through settlementAccountTx — which is the read
// Task 17b's SettlementMember exists to make possible, and the reason a
// settlement agent given its own database still has something to settle from.
// What the bank rows answer is the IDENTIFIER: a cycle's net positions are keyed
// by ParticipantID, the agent's own records are keyed by BIC, and a bank's row is
// the only thing in the system that holds the mapping between the two. The
// clearing house's roster could not have answered it either — it is keyed by BIC
// as well, and starts from the address rather than arriving at it.
//
// The second thing read off those rows is a check and not a lookup: AccountsFor,
// whether the member operates in the cycle's asset at all. Both reads are
// settlementLegsTx's, and its doc is where the one that survives Task 18 is
// separated from the one that does not.
//
// # The settlement window, and what stopped being true about it
//
// This doc used to argue that ALL of settlement was one unit of work and that
// "the interval in which the books are inconsistent is not observable". That was
// a true description of the code that carried it and the right thing to want: a
// net payer that cannot cover its position must abort the batch, not leave the
// other members paid and the central bank's books moved. The batch is still
// atomic in exactly that sense, and that much is unchanged — see the reserve
// check above the postings, and TestSettleCycleIsAtomic.
//
// What was wrong was the SCOPE. One process cannot hold every institution's
// books inside a window, because they are not one process's to hold, and this
// system has stopped pretending otherwise. What is atomic is what this
// institution does: the central bank posts one transaction, in its own book, and
// is FINAL either way. What each member does afterwards is that member's own, on
// advice, in its own unit of work, and it can fail on its own.
//
// The interval between is therefore not merely observable — it is the thing
// being modelled. It is the UNRECONCILED POSITION: the reserves have moved and a
// member has been told and has not yet booked. Where it is visible is that
// member's CLEARING SUSPENSE, which has not returned to zero, with no
// SettlementAdvice row against the cycle — the row is written only by a member
// that books, and it commits with the mirror leg. In the EU that gap is not a
// modelling convenience but
// a directive: the Settlement Finality Directive is about exactly this moment,
// when a transfer order becomes irrevocable regardless of what any participant
// does next.
//
// # A redelivered instruction posts nothing
//
// Which is what makes finality safe to publish over a lossy transport. This
// refuses a cycle that is not CycleClosed with ErrCycleNotClosed, so a second
// settlement instruction for a cycle already Settled is a refusal rather than a
// second batch; and the central bank's posting carries the idempotency key
// "<cycle>:settle", so even a caller that reached the posting would move
// nothing twice.
//
// # Ordering
//
// Participants are visited in registration order, not in map order, so the
// entries of the central bank's settlement transaction come out the same on
// every run. That order is persisted — the store gives each entry an explicit
// position column, because a table has no order of its own — so leaving it to
// Go's randomised map iteration would make the stored transaction differ from
// run to run for no reason. The statements come out in
// the same order, so the messages a caller sends do too.
func (s *Network) SettleCycle(ctx context.Context, id CycleID) (Settlement, []SettlementStatement, error) {
	var out Settlement
	var statements []SettlementStatement
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, statements, err = s.SettleCycleTx(ctx, tx, id)
		return err
	})
	return out, statements, err
}

// SettleCycleTx is SettleCycle within a caller-supplied unit of work.
//
// It returns the STATEMENTS beside the settlement because the closing balances
// are a claim about a moment. A caller that re-read them after the commit would
// be quoting whatever the accounts stand at then, and a statement asserting the
// wrong balance is worse than none: the balance is the only thing a member can
// check its own posting against.
func (s *Network) SettleCycleTx(ctx context.Context, tx Tx, id CycleID) (Settlement, []SettlementStatement, error) {
	book, err := s.centralBankBook()
	if err != nil {
		return Settlement{}, nil, err
	}
	c, err := tx.GetCycle(ctx, id)
	if err != nil {
		return Settlement{}, nil, err
	}
	if c.Status != CycleClosed {
		return Settlement{}, nil, ErrCycleNotClosed
	}

	// The cycle settles in its scheme's asset, resolved once here and used for
	// every participant in the batch. A member that does not hold that asset
	// fails the whole batch, exactly as an underfunded member does — there is
	// no reserve account to fall back to.
	scheme, ok := s.scheme(c.Scheme)
	if !ok {
		return Settlement{}, nil, ErrSchemeNotFound
	}
	asset := scheme.Asset()

	// 1. Central-bank settlement transaction: move netted reserves between
	//    participants. The net positions sum to zero, so this balances.
	//
	//    The participants are read in registration order so that both this
	//    transaction's entries and the statements below are deterministic.
	legs, err := s.settlementLegsTx(ctx, tx, c, asset)
	if err != nil {
		return Settlement{}, nil, err
	}

	// The central bank's decision, and the whole of what it decides: can each net
	// payer cover its position out of the reserves it holds HERE?
	//
	// It is checked explicitly because the ledger will not check it. A member's
	// settlement account in this book is a LIABILITY — the central bank owes the
	// member its reserve — and Book.checkSufficientBalance only guards Asset and
	// Expense accounts. Until this task the refusal came from the MIRROR leg in
	// the member's own book, where "Reserve at Central Bank" is an Asset; moving
	// that leg to the bank would have taken AM04 with it and settled a cycle
	// whose net payer was short, leaving the shortfall to surface at the bank as
	// a dead letter.
	//
	// Refusing to take a member's reserve below zero is the central bank
	// declining to extend uncollateralised intraday credit, which is the decision
	// a settlement agent exists to make. ledger.ErrInsufficientBalance is
	// returned rather than a new sentinel so that ReasonFor's borrowedReasons
	// keeps mapping it to AM04 — same code, same layer, same meaning.
	for _, leg := range legs {
		if leg.net >= 0 {
			continue
		}
		held, err := book.BookBalanceTx(ctx, tx, leg.settlement)
		if err != nil {
			return Settlement{}, nil, err
		}
		if held+leg.net < 0 {
			return Settlement{}, nil, fmt.Errorf("%w: %s is short %d in %s",
				ledger.ErrInsufficientBalance, leg.participant.ID, -(held + leg.net), asset)
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
			IdempotencyKey: string(c.ID) + ":settle",
			Description:    "Settlement of clearing cycle " + string(c.ID),
			Entries:        cbEntries,
		})
		if err != nil {
			return Settlement{}, nil, err
		}
		settlementTx = posted.ID

		// 2. What each member is TOLD, in place of the mirror leg this used to
		//    post in its book. The balance is read AFTER the posting and inside
		//    the same unit of work, which is what makes it a CLOSING balance:
		//    reading it before would produce an opening balance labelled CLBD,
		//    which is the exact error closingBalanceIn refuses on the other side.
		for _, leg := range legs {
			closing, err := book.BookBalanceTx(ctx, tx, leg.settlement)
			if err != nil {
				return Settlement{}, nil, err
			}
			statements = append(statements, SettlementStatement{
				Member:         leg.participant.ID,
				Agent:          leg.participant.BIC,
				Account:        leg.settlement,
				Asset:          asset,
				Reference:      string(c.ID),
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
		CycleID:      c.ID,
		NetPositions: copyPositions(c.NetPositions),
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

	c.Status = CycleSettled
	c.SettlementID = st.ID
	if err := tx.PutCycle(ctx, c); err != nil {
		return Settlement{}, nil, err
	}
	// One cycle.settled, and that is the whole of this unit of work's audit
	// trail. It used to be one payment.settled per payment as well; those are
	// appended by each payee's bank now, when it posts its own creditor leg.
	if err := s.appendAuditTx(ctx, tx, ledger.EventCycleSettled, string(c.ID), st); err != nil {
		return Settlement{}, nil, err
	}
	return st, statements, nil
}

// settlementLeg pairs a participant with its non-zero net position in a cycle,
// together with the account in the CENTRAL BANK's own book that the position
// moves through — the settlement agent's own record of what it holds for that
// member, not the member's record of it.
type settlementLeg struct {
	participant *Bank
	settlement  ledger.AccountID
	net         ledger.Amount
}

// settlementLegsTx resolves a cycle's net positions to participants in
// registration order, and each participant to the account the settlement agent
// holds for it in the cycle's asset.
//
// Registration order rather than map order because these legs decide the entry
// order of the settlement transaction, which is persisted. Iterating the
// NetPositions map directly would produce a different stored transaction on
// every run.
//
// Three failures land here, before anything is posted, and they are not one
// failure with three causes. A member whose own row says it does not operate in
// the cycle's asset is ErrParticipantAssetNotFound. A member the central bank
// holds an account for, but not in this asset, is the same sentinel from the
// other side. A member the central bank holds NO account for at all is
// ErrSettlementMemberNotFound — a founded bank that was never admitted, which
// is a state the domain can be in since founding and joining became two commits
// with a message between them, and which is not a statement about the asset.
// See settlementAccountTx, and ReserveBalance, which makes the same distinction
// for the same reason.
//
// # The bank's row is read twice: one crossing, and one check that belongs here
//
// A cycle's net positions are keyed by ParticipantID and the settlement agent's
// own records are keyed by BIC, so the id has to become an address before the
// agent can ask itself anything — and the only thing that knows the mapping is
// the bank's own row. That is a settlement agent reading a bank's database, and
// under Task 18's stores it is not a read it can make.
//
// Task 17 does not close it, and the reason it does not is that the fix is not
// here. The pacs.009 the settlement agent is sent already carries both legs'
// BICs, so closing this means settling from the MESSAGE rather than from the
// cycle row — which is the settlement agent holding no cycles at all, the gap
// mesh's centralBank records against itself and Task 18 owns. Narrowing the read
// here would leave the same crossing in a smaller shape and hide it.
//
// The second read on that row is AccountsFor, and it is not part of that
// crossing: it is a check rather than a lookup, and the thing it checks is the
// member's own statement about itself — whether this bank operates in the
// cycle's asset at all. The central bank holding an account the member never
// asked about would not answer the same question.
// TestSettleCycleFailsWhenParticipantLacksTheAsset is what pins it. It stays
// where it is; what moves at Task 18 is the id-to-BIC read above it.
func (s *Network) settlementLegsTx(ctx context.Context, tx Tx, c ClearingCycle, asset ledger.AssetCode) ([]settlementLeg, error) {
	participants, err := tx.ListBanks(ctx)
	if err != nil {
		return nil, err
	}

	legs := make([]settlementLeg, 0, len(c.NetPositions))
	for _, rec := range participants {
		net, ok := c.NetPositions[rec.ID]
		if !ok || net == 0 {
			continue
		}
		p := s.bind(rec)
		if _, err := p.AccountsFor(asset); err != nil {
			return nil, err
		}
		settlement, err := s.settlementAccountTx(ctx, tx, p.BIC, asset)
		if err != nil {
			return nil, err
		}
		legs = append(legs, settlementLeg{participant: p, settlement: settlement, net: net})
	}

	// Every non-zero position must have matched a participant; a position that
	// matched nothing would silently drop money out of the settlement.
	nonZero := 0
	for _, net := range c.NetPositions {
		if net != 0 {
			nonZero++
		}
	}
	if len(legs) != nonZero {
		return nil, ErrParticipantNotFound
	}
	return legs, nil
}

// PostSettlementAdvice is PostSettlementAdviceTx in its own unit of work, which
// is what a bank acting on a statement it has just been handed needs: the
// message is the whole of the input, so there is nothing else to commit with it.
func (s *Network) PostSettlementAdvice(ctx context.Context, m AdvisedMovement) (SettlementAdvice, error) {
	var out SettlementAdvice
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.PostSettlementAdviceTx(ctx, tx, m)
		return err
	})
	return out, err
}

// PostSettlementAdviceTx is a member bank booking a cut-off it was told about:
// the mirror leg, in its OWN ledger, and the row that records that it did.
//
// # What the mirror leg is
//
// A bank's clearing suspense holds money that has left a customer and not yet
// settled between banks. Settlement is when it stops being in transit, so the
// suspense is the contra to the reserve: one entry Debit, one Credit. A net
// receiver's reserve goes UP and its suspense goes UP with it; a net payer's
// reserve goes DOWN and its suspense goes DOWN.
//
// These three comments used to say a net receiver's suspense went down and a net
// payer's up, and they were wrong about the code they sit on. Contra does not
// mean opposite BALANCES here: suspense is a ledger.Liability (see
// FoundBankTx, which opens it), so the receiver's Credit RAISES it and the payer's Debit
// LOWERS it. Measured in the seed — Verde, a net receiver, credits its suspense
// 10000; Aurora, a net payer, debits its suspense 25000 to zero.
//
// Not a typo worth passing over, because the whole ordering argument for sending
// the camt.053 BEFORE the ACSC rests on the receiver's suspense going UP first,
// so that its creditor legs have something to draw on. A reader who trusted the
// old wording would conclude that requirement was backwards. See
// mesh.centralBank.advise, mesh's package doc and
// TestTheMessagesACutOffPutsOnTheWire, which state it the right way round and
// always did.
//
// Suspense returns to zero only if the
// central bank's reserve movement and the clearing house's payment list agree,
// which is the reconciliation this whole conversation is for and which needs no
// cross-store read — that is what makes it legal under isolation.
//
// # It is the BANK's act
//
// It used to be the central bank's, inline: SettleCycleTx posted every member's
// mirror leg inside its own unit of work, which is a posting in another
// institution's book. TestWhichBooksTheCentralBankReachesWhenItSettles measured
// exactly that. A settlement agent has no access to a member's ledger and no
// business in it; what it has is a statement to send.
//
// It no longer calls this at all. SettleCycleTx returns one SettlementStatement
// per member, the settlement agent sends each as a camt.053, and the member that
// receives it calls this for itself — see mesh's centralBank.advise and
// bank.receiveStatement. TestEachBankBooksItsOwnSettlementAndNoOtherBooks is what
// measures that the posting is now made in the acting bank's own book and in no
// other.
//
// # The statement is checked before it is booked
//
// The account the statement names must be THIS bank's reserve account at the
// central bank. A bank that booked whatever arrived would move its reserve
// mirror on another member's position, and under isolation there is no second
// reader to notice. See ErrStatementNotForThisBank.
//
// # Booking twice is not reachable
//
// The idempotency key is derived from the statement's own reference —
// "<reference>:reserve:<participant>" — so a redelivered statement's posting
// request lands on the same key in THIS bank's own ledger, and the ledger
// refuses it; and the advice row is checked first, so it does not even try.
func (s *Network) PostSettlementAdviceTx(ctx context.Context, tx Tx, m AdvisedMovement) (SettlementAdvice, error) {
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
	// Written before the posting and committed WITH it. This is one unit of work,
	// so the row and the mirror leg stand or fall together: a posting that fails
	// takes this write back with it and leaves nothing at all.
	//
	// That is the right shape and not a limitation. Booking the leg and recording
	// that you booked it must be atomic, or a bank can post and fail to record —
	// and a bank whose own store claims a booking it did not make is worse off
	// than one with no row. The ordering therefore buys nothing observable: the
	// second Put below is the only version any reader outside this transaction
	// ever sees.
	//
	// This comment used to say the opposite — that a failed posting left the row
	// at Advised, and that such a row was the unreconciled position. It was never
	// true of this code. The store issues a ROLLBACK, so there is no half of this
	// that can survive.
	// What the row actually is: this bank's own durable record that it BOOKED
	// this cut-off, which is what makes a redelivered statement a no-op (the
	// GetSettlementAdvice check above) and what Task 19's reconciliation reads.
	// The unreconciled position is the ABSENCE of a row against a clearing
	// suspense that has not returned to zero.
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
	// that is the whole of what this bank knows. The reference is a cycle id on
	// the cut-off path and a payment id on the return path, and a member bank
	// holds neither kind of row — see SettlementAdvice, which records that there
	// is deliberately no field saying which it is. This used to read "Net
	// settlement of cycle …", which was true of every statement that existed
	// when it was written and became a false claim the moment a return could
	// produce one. No customer reads it — the leg is Suspense against Reserve in
	// the bank's own book — but it is what an operator reconciling that bank's
	// suspense has to go on, and a reconciliation told to look for a cycle that
	// does not exist is worse off than one told nothing.
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

// PostCreditorLeg is PostCreditorLegTx in its own unit of work, which is what a
// bank acting on an advice it has just been handed needs: the message names one
// payment and there is nothing else to commit with it.
//
// One payment at a time is the point rather than a convenience. While this ran
// inside the cut-off's unit of work a single payee's closed account could fail
// the whole batch; now each bank's each payment succeeds or fails alone.
func (s *Network) PostCreditorLeg(ctx context.Context, id PaymentID) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.PostCreditorLegTx(ctx, tx, id)
		return err
	})
	return out, err
}

// PostCreditorLegTx is the payee's bank releasing one settled payment out of its
// clearing suspense into the payee's account.
//
// # The check that could not be made before
//
// creditorSideTx and DepositTx both call Deposit.CheckCreditTx before money lands
// in a customer's account. This never did, and its own doc said why: settlement
// was one unit of work over the whole batch, so a check that failed took the
// entire cut-off down for one retail customer who closed an account — and a
// Cleared payment has no route out of the cycle it is in. Refusing was worse than
// stranding, so it stranded, and the ruling was recorded rather than fixed.
//
// The split is what makes the check affordable. One payment at one bank now fails
// on its own, and the residual has somewhere to go: the bank's unclaimed-balances
// account, which is what a real bank does with money that arrives for an account
// that cannot receive it. The payment still reaches Settled, because it did — the
// reserves moved and the payee's bank has been paid. What is left open is whether
// the CUSTOMER has been paid, which is between the bank and its customer.
//
// # The diversion is recorded on the payment
//
// It has to be, and the first version of this was not: a return of a diverted
// payment debited the payee's closed account to minus the amount and left the
// unclaimed liability standing, because the return had nothing to read.
// CreditorLegAccount is written HERE, in both arms, because the account the
// credit went to is a fact about a moment that no later reading recovers. See
// Payment.CreditorLegAccount for why re-deriving it is unsafe, and
// TestReturningAPaymentThatSettledIntoUnclaimedBalancesReleasesTheLiability for
// the numbers.
//
// # Only the payee's bank may call it
//
// On a push the clearing house tells both banks the payment settled: the payer's
// bank because it has been waiting for the answer to its instruction, the payee's
// bank because it has this leg to post. Only the second may post it. See
// ErrNotThisBanksPayment.
func (s *Network) PostCreditorLegTx(ctx context.Context, tx Tx, id PaymentID) (Payment, error) {
	self, err := s.self()
	if err != nil {
		return Payment{}, err
	}
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.Creditor.Participant != self {
		return Payment{}, fmt.Errorf("%w: %s is %s's creditor, not %s's", ErrNotThisBanksPayment, id, p.Creditor.Participant, self)
	}
	if p.Status == Settled {
		// A redelivered advice. The ledger's idempotency key would refuse the
		// second posting anyway; this refuses to transition twice, which
		// ErrInvalidStateTransition would otherwise report as a failure to a
		// handler that did nothing wrong.
		return p, nil
	}
	creditor, err := s.bankTx(ctx, tx, self)
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

	// Resolving the payee's account is not allowed to fail SOFTLY here, and the
	// reason is one line of glAccountTx: it collapses every error from its read
	// into ErrAccountNotInParticipant, so a dropped connection, a scan error and
	// a genuinely absent account are one value by the time they arrive. There is
	// no shape of failure this caller could tell apart, which means there is no
	// failure it may route money on.
	//
	// So it fails the cut-off, which is retriable, rather than diverting. That
	// costs nothing real: this bank RESOLVED the payee's account once already,
	// when it accepted the payment, so a read that cannot answer now is a fault
	// in the reading and not news about the account.
	glAccount, err := creditor.glAccountTx(ctx, tx, p.Creditor.Account)
	if err != nil {
		return Payment{}, err
	}

	// Where the money goes: the payee's account if it can take it, and the
	// unclaimed-balances account if it cannot. Both are this bank's own.
	target, description := glAccount, p.Description
	if err := creditor.Deposit.CheckCreditTx(ctx, tx, p.Creditor.Account); err != nil {
		if !errors.Is(err, deposit.ErrAccountClosed) {
			// ErrAccountClosed is the ONLY refusal CheckCreditTx makes —
			// deposit.requireCreditable checks Closed and nothing else — so
			// anything else from here is a STORE FAILURE and not a statement
			// about the account. Diverting money to unclaimed balances because a
			// database connection dropped would be the settlement-time twin of
			// the defect Task 14 fixed in checkPartyTx, where a dropped
			// connection reported AC01 to another bank.
			return Payment{}, err
		}
		target, description = accts.Unclaimed, "Unclaimed: "+p.Description
	}

	posted, err := creditor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":credit",
		Description:    description,
		ValueDate:      p.ValueDate,
		Metadata:       paymentMetadata(&p),
		Entries: []ledger.Entry{
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: target, Amount: p.Amount, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return Payment{}, err
	}
	p.CreditorLegTx = posted.ID
	// Recorded in BOTH arms, not only the diverting one. A return has to claw
	// the money back from where it actually went, and it cannot ask this
	// question again later: see Payment.CreditorLegAccount and clawbackTx.
	p.CreditorLegAccount = target
	if err := transition(&p, Settled); err != nil {
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
	// side. Only the COUNTERPARTY's NAME is required — and which side that is
	// depends on the scheme's direction, exactly as everything else here does.
	// The submitting bank's own side is filled from its own register and
	// anything supplied for it is ignored, because a payer does not get to
	// rename themselves on an instruction.
	//
	// The COUNTERPARTY's Agent is required, and this paragraph used to say the
	// opposite — that the agent on either side is ignored because SubmitPaymentTx
	// derives both from the roster, and that routing is never the caller's to
	// assert. Task 18a reversed it: a bank holds only its own row from Task 18c,
	// so there is nothing left to derive the counterparty's BIC FROM, and
	// SubmitPaymentTx refuses an instruction that names no agent
	// (ErrCounterpartyAgentNotNamed). The address here is an IBAN and a BIC, which
	// is what SEPA was before 2016. See the long note in SubmitPaymentTx for what
	// makes asserting it safe — narrowing the resolution, in the same commit.
	//
	// The SUBMITTING bank's own agent is still ignored, for the reason its name is:
	// this bank is the authority on itself and fills its own side from its own
	// register.
	//
	// The field is on the struct for a second reason as well, and that one is
	// unchanged: this same type is what CreditTransferRequest and
	// DirectDebitRequest produce from a RECEIVED message, where the agent is the
	// sender's assertion and is genuinely carried.
	//
	// api's initiatePaymentRequest used to have no agent field at all, on the
	// strength of the claim above; it has one again, and its doc records the
	// there-then-not-then-here-again in full.
	DebtorDetails   PartyDetails
	CreditorDetails PartyDetails
}

// SubmitPayment is SubmitPaymentTx in its own unit of work.
//
// It runs the bank's half and nothing else, which is why it is NOT what a bank
// actor calls: a submission that commits and then cannot be turned into a
// message leaves the payer debited against an instruction nobody will ever
// answer. SubmitAndInstruct is the pair that cannot do that.
func (s *Network) SubmitPayment(ctx context.Context, req InitiatePaymentRequest) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.SubmitPaymentTx(ctx, tx, req)
		return err
	})
	return out, err
}

// SubmitAndInstruct is the submitting bank's half AND the interbank message it
// travels on, in ONE unit of work.
//
// # Why they cannot be two
//
// They were, and it was a money bug. SubmitPayment committed the debtor leg,
// the caller then built the pacs.008, and building one can FAIL — a payee whose
// address the instruction never quoted is ErrUnaddressableAccount, which api
// answers 422. The payer was debited by a request the API reported as refused,
// and no message existed, so not even a dead letter recorded it; a client that
// retried drained the account. Two of the refusals
// TestPaymentAddressingRefusalsAre422 drives were of exactly that shape.
//
// The fix is the ordering, not a new check: everything the instruction needs is
// resolved while the transaction is still open, so a counterparty this bank
// cannot address rolls the debit back with it. Nothing is DUPLICATED — there is
// still one address check and it is the message builder's, which is the only
// place that knows what a pacs.008 must carry.
//
// # The send is still outside
//
// This returns the envelope rather than sending it, because sending inside a
// unit of work is the other half of the same mistake: a message the clearing
// house could act on against a submission the store then rolled back. The
// caller sends after this returns. TestARolledBackSubmitSendsNothing is the pin
// on that half, and mesh.bank.submit is the caller.
func (s *Network) SubmitAndInstruct(ctx context.Context, req InitiatePaymentRequest, mc MessageContext) (Payment, iso20022.Envelope, error) {
	var p Payment
	var env iso20022.Envelope
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		if p, err = s.SubmitPaymentTx(ctx, tx, req); err != nil {
			return err
		}
		env, err = s.InstructionTx(ctx, tx, p, mc)
		return err
	})
	if err != nil {
		return Payment{}, iso20022.Envelope{}, err
	}
	return p, env, nil
}

// InstructionTx builds the interbank message a submission travels on: a pacs.008
// for a push, a pacs.003 for a pull.
//
// They are two message definitions and not one with a flag because they say
// different things. A pacs.008 accompanies money that has already left the
// payer; a pacs.003 asks for money that has not moved, which is why it must
// carry the MANDATE — the debtor's standing authority for this creditor to
// collect, and the only element that distinguishes a collection from a demand.
//
// The mandate is loaded here rather than carried on the payment because a
// payment holds its MandateID and nothing else of it; the message needs the
// document's own terms. It is a network-scoped row, like the payment itself: in
// this system mandates live once, in the network's store, which is the
// simplification SDD.ValidateMandate names.
func (s *Network) InstructionTx(ctx context.Context, tx Tx, p Payment, mc MessageContext) (iso20022.Envelope, error) {
	scheme, ok := s.scheme(p.Scheme)
	if !ok {
		return iso20022.Envelope{}, fmt.Errorf("%w: %s", ErrSchemeNotFound, p.Scheme)
	}
	if scheme.Direction() != Pull {
		return s.CreditTransferMessage(p, mc)
	}
	mandate, err := tx.GetMandate(ctx, p.MandateID)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	return s.DirectDebitMessage(p, mandate, mc)
}

// SubmitPaymentTx is the SUBMITTING bank's half of what used to be
// InitiatePaymentTx, and which half that is depends on the scheme's direction.
//
// For a push (SCT) the debtor's bank submits, so the debtor half runs: the
// account, the asset, the address, the funds, and the debtor leg posted. For a
// pull (SDD) the CREDITOR's bank submits, so the creditor half runs and
// NOTHING is posted — the debtor's bank posts the leg when it accepts the
// collection, which is the only moment either the funds or the account are in
// view.
//
// The roadmap called this "the creditor-account check moving out of
// InitiatePaymentTx". That is true for a credit transfer and backwards for a
// direct debit. The rule that covers both is the one above.
//
// The payment is left Initiated and in NO cycle. Adding it to one is the
// clearing house's act, on receiving the counterparty's ACCP, because clearing
// is what a clearing house does — see AcceptAtCSMTx, which is where
// ErrCycleNotOpen went.
//
// What the far side gets here is TEXT validation and nothing else: its ids are
// stored and used as lookup keys, so they must be safe to write, but whether
// the account behind them exists is not a question this bank can ask.
// TestSubmitDoesNotCheckTheCreditorAccount is the pin.
func (s *Network) SubmitPaymentTx(ctx context.Context, tx Tx, req InitiatePaymentRequest) (Payment, error) {
	// Common validation: everything that needs neither book. It runs before the
	// branch so that a malformed instruction is refused the same way whichever
	// bank is submitting it.
	scheme, ok := s.scheme(req.Scheme)
	if !ok {
		return Payment{}, ErrSchemeNotFound
	}
	if req.Amount <= 0 {
		return Payment{}, ErrInvalidPaymentAmount
	}
	if err := validateParty("debtor", req.Debtor); err != nil {
		return Payment{}, err
	}
	if err := validateParty("creditor", req.Creditor); err != nil {
		return Payment{}, err
	}
	if err := ledger.ValidateText("endToEndId", req.EndToEndID); err != nil {
		return Payment{}, err
	}
	if err := ledger.ValidateText("description", req.Description); err != nil {
		return Payment{}, err
	}
	if err := ledger.ValidateTextMap("metadata", req.Metadata); err != nil {
		return Payment{}, err
	}
	if err := ledger.ValidateText("mandateId", string(req.MandateID)); err != nil {
		return Payment{}, err
	}
	// The id comes BEFORE the duplicate check, and the order is the whole of
	// what makes that check atomic.
	//
	// NextID writes one row of id_sequences (store/sqlite's nextSeq), and writing
	// is what makes this transaction the database's writer. A second submission
	// waits there, and by the time it gets past, the first has either committed —
	// so the read below sees its payment and refuses the reference — or rolled
	// back, taking the id with it. The gap-free counter serializes the whole
	// operation and not merely the number it hands out, which is the argument
	// store/storetest's races.go already makes for the admission acts.
	//
	// With the two the other way round, eight concurrent submissions of one
	// EndToEndID were accepted eight times on store/pg, under READ COMMITTED, and
	// once on store/mem, which serialized every Update on one process-wide mutex
	// and so could never show it. The payer was debited eight times for one client
	// reference.
	//
	// What the ordering is worth on the store that is left is measured, and it is
	// smaller: with the two statements swapped back, storetest's
	// Races/ConcurrentSubmissionsOfOneReferenceAcceptOne passes ten runs out of
	// ten, on the ephemeral store and on a WAL file. SQLite admits one writer, so
	// the loser is refused at its first write and Store.Update re-runs the unit of
	// work against the winner's committed row — the retry closes the same hole a
	// second way, and no test here can now see this ordering go. See
	// admissionSequenceTx, which records the same finding for the admission acts,
	// and storetest's ConcurrentReadThenWriteOnOneKeyAgrees — a subtest of
	// TestConformance, not a test function of its own — which states the shape at
	// the store interface.
	id, err := tx.NextID(ctx, s.book(), "pay")
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
	p := Payment{
		ID:              PaymentID(id),
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

	sc := SchemeContext{Network: s, Tx: tx, Now: now}
	push := scheme.Direction() == Push

	// The counterparty is whichever side this bank is not. Checked BEFORE the
	// side call, so an instruction that names nobody is refused before the
	// debtor leg is posted rather than after.
	counterparty := &p.CreditorDetails
	if !push {
		counterparty = &p.DebtorDetails
	}

	// The NAME is asserted by the payer and there is nowhere else it could come
	// from: the account is at another bank and this one does not read that
	// bank's register. That is the whole of what an instruction says about the
	// other side.
	if counterparty.Name == "" {
		return Payment{}, ErrCounterpartyNotNamed
	}
	if err := ledger.ValidateText("counterparty name", counterparty.Name); err != nil {
		return Payment{}, err
	}

	// The AGENT is ASSERTED, and Task 18a is where it went back to being so.
	//
	// # What it was, and why the derivation could not survive the split
	//
	// It was DERIVED: `tx.GetBank(counterpartyRef.Participant)`, the counterparty
	// bank's own row, with whatever the instruction carried discarded. That was
	// Task 14's fix for a real routing defect — this element goes out as
	// CdtrAgt/DbtrAgt (translate.go's partiesOf) and the clearing house relays on
	// exactly it with no store read of its own, so a payer who typed the wrong
	// bank chose the bank.
	//
	// The row it read is the counterparty's, and from Task 18c a bank holds only
	// its own. There is no second source to move the derivation to. The roster is
	// keyed by the BIC being derived, and it is the CLEARING HOUSE's row rather
	// than a bank's; and this network has no IBAN-to-BIC directory service, which
	// is the thing a real originating bank actually derives from. SEPA is
	// IBAN-only because every bank subscribes to such a table, not because the
	// routing is computable from the address.
	//
	// So the address on an instruction here is an IBAN AND a BIC — which is what
	// SEPA was before February 2016, and what a cross-border transfer still is.
	//
	// # What makes that safe, and it is the same commit's other half
	//
	// Both measured failures of the asserted agent had one mechanism: the bank
	// the message reached could resolve a payee it does not hold, because
	// ResolveIdentifierTx swept every member's register. A push naming the
	// payer's own bank came back to its sender, which found the payee at the real
	// bank and answered its own instruction; a pull naming the collector saw the
	// COLLECTING bank post the debit in the payer's bank's book.
	//
	// Task 18a narrows that resolution to the resolving bank's own register. A
	// misdirected message now has nothing to resolve, and the bank that receives
	// it answers AC01 — which is the true statement about what it was sent, and
	// what a real bank does with a payment for an IBAN it does not hold. The
	// payer's debit is reversed by the rejection. See mesh/books_test.go's
	// TestAWrongCounterpartyAgentIsRefusedByTheBankItNames, which is the pin and
	// is the old test reversed.
	//
	// # What is still refused here
	//
	// The FORMAT, and nothing else. An unnamed agent is refused by name, because
	// the message cannot be built without CdtrAgt/DbtrAgt and a submission that
	// committed the payer's debit before discovering that would be the money bug
	// SubmitAndInstruct exists to prevent. A malformed one is refused for the
	// reason FoundBankTx validates a BIC at founding rather than at first use:
	// the mesh cannot route to it, so the refusal belongs where the payer can
	// still fix it.
	if counterparty.Agent == "" {
		return Payment{}, ErrCounterpartyAgentNotNamed
	}
	if err := counterparty.Agent.Validate(); err != nil {
		return Payment{}, fmt.Errorf("%w: %w", ErrCounterpartyAgentNotNamed, err)
	}

	// The submitting bank's own side comes from its own register, overwriting
	// anything the request supplied: a payer does not rename themselves on an
	// instruction, and this bank is the authority on its own customer. This
	// runs HERE, on the account and participant debtorSideTx/creditorSideTx
	// just checked, and not inside those two functions — they also run from
	// AcceptInboundTx, where the bank executing them is the RECEIVING bank for
	// that direction, not the submitting one. Filling the details there would
	// overwrite the counterparty's asserted name with the receiving bank's own
	// record, after that name has already gone out on the wire in the message
	// SubmitAndInstruct built from it. Only SubmitPaymentTx knows unambiguously
	// which side is its own.
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
	// once the counterparty has answered and the clearing house has taken it
	// into a cycle — the network took responsibility for it. A refused
	// instruction rolls back with the transaction, so neither event is ever
	// recorded for one.
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
func (s *Network) AcceptInbound(ctx context.Context, id PaymentID) error {
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return s.AcceptInboundTx(ctx, tx, id)
	})
}

// resolveOwnPartyTx replaces a party ref with the one THIS bank resolves for the
// address on it. It is AcceptInboundTx's half of the sweep's removal — see that
// function's doc for why the row's ref cannot be trusted for the far side.
//
// # An unaddressed party is left as it stands, and that is a fallback with a life
// expectancy
//
// A ref with no identifier cannot be resolved, and the arm is reachable only
// from a caller that never built a message: an instruction quoting no address
// for the far side fails CreditTransferMessage with ErrUnaddressableAccount, so
// nothing that goes on the wire reaches here without one. What does reach it is
// the seed and the domain suites, which drive SubmitPaymentTx and this function
// directly.
//
// So for those callers the submitting bank's ref stands, which is exactly the
// trust this change exists to remove — and it survives only because ONE payment
// row is shared. Task 18d gives each entity its own, at which point the
// receiving bank writes its row from the message and there is no ref to fall
// back to; the fallback goes with the shared row rather than being tightened
// here.
func (s *Network) resolveOwnPartyTx(ctx context.Context, tx Tx, ref *PartyRef) error {
	if ref.Identifier == (deposit.Identifier{}) {
		return nil
	}
	resolved, err := s.addressedPartyTx(ctx, tx, ref.Identifier)
	if err != nil {
		return err
	}
	*ref = resolved
	return nil
}

// AcceptInboundTx is the RECEIVING bank's half: the half SubmitPaymentTx did
// not run, because the bank that ran that one could not see this side.
//
// For a push the receiver is the creditor's bank, and its half is a check —
// the account exists, is in the scheme's asset, is addressable, and can be
// credited at all. Nothing is posted: the payee is paid AFTER the cut-off has
// settled, out of the creditor bank's suspense, by that bank's own
// PostCreditorLegTx on the clearing house's per-payment advice. This comment
// used to say "at settlement", which was true while the settlement agent posted
// every member's legs inside its own unit of work.
//
// For a pull the receiver is the DEBTOR's bank, and its half is the one that
// moves money: it checks the account, the asset, the address and the funds,
// and posts the debtor leg. That posting is the whole reason a direct debit
// posts nothing at submission — until this runs, the payer's money has not
// moved and no actor has looked at their account.
//
// # It resolves its own party rather than trusting the row's, and Task 18a is
// why
//
// The payment row names both parties by (participant, account) — internal ids —
// and until Task 18a this half used the ones the SUBMITTING bank wrote. That
// worked for one reason: ResolveIdentifierTx swept every member's register, so
// the submitting bank could look the payee up in the payee's bank's book and
// write down its account id. That sweep is crossing 2 and it is gone; a payer's
// bank cannot know another bank's internal key, and in life it never could.
//
// So this half resolves the counterparty's ADDRESS — the IBAN the message
// carries — in its OWN register, and overwrites the ref. It is the resolution
// mesh/bank.go's receive handlers already made and then discarded, moved to
// where its answer is used. What the submitting bank writes for the far side is
// now a guess this bank corrects, and 18d makes it not even a guess: with a
// payment row per entity the receiving bank writes its own row from the message
// and there is nothing to correct.
//
// A not-found is ErrAccountNotInParticipant and becomes AC01, which is the same
// answer the discarded resolution produced and for the same reason — see
// addressedPartyTx, and mesh's
// TestAWrongCounterpartyAgentIsRefusedByTheBankItNames for the case that makes
// it fire on the happy-looking path.
//
// The bank answering is this network's identity, and it reaches this act in one
// place only — the resolution of its OWN party, through resolveOwnPartyTx and
// ResolveIdentifierTx. That is why the refusal is the first line here rather
// than a consequence of the resolution failing: a network with no register is
// not a bank that resolves nothing, it is an institution with no business
// answering a pacs.008 at all, and on a push where the payment quotes no
// identifier the resolution is skipped entirely and would never have fired.
//
// It takes an ID and LOADS the payment, as AcceptAtCSMTx does, and it refuses
// anything that is not still Initiated.
//
// Both halves of that are load-bearing, and taking the payment by value instead
// was a genuine lost update rather than a theoretical one: submit, reject —
// which reverses the debtor leg and RejectAtCSMTx accepts an Initiated
// payment, so this is an ordinary sequence — then answer with the copy the
// submitting call returned, and the rejected payment came back Initiated with
// DebtorLegTx still naming the reversed transaction, ready for the clearing
// house to accept and settle. In the mesh the two acts are two actors and an
// arbitrary interval apart, so the caller's copy is stale by construction.
// TestAcceptInboundRefusesAPaymentThatIsNoLongerInitiated is the pin.
//
// # The same message twice
//
// A message queue redelivers, so the same pacs.003 can arrive twice while the
// payment is still Initiated — and the status guard above cannot tell that
// apart from a first delivery. DebtorLegTx is the exact witness that it can:
// nothing but this half sets it on a pull, so a pull payment that has one has
// already been answered, and the pull arm returns without doing anything. The
// address back-fill it would otherwise redo was persisted by the first run.
//
// Without that witness a redelivered collection reached postDebtorLegTx again
// and came back with the ledger's ErrDuplicateIdempotencyKey. No money was ever
// at risk — the key is the payment's own id, so the second debit could not post
// — but it is a wrong ANSWER: that error has no entry in reasonTable, so
// ReasonFor falls through to MS03 and the bank would reject, on the wire, a
// collection it had in fact accepted. The push arm needs no such guard, because
// the receiving bank posts nothing there: re-running it re-checks the payee's
// account, back-fills an address that is already the stored one, and the
// equality check below returns without a write.
//
// It writes the payment back only when it changed something (the debtor leg
// for a pull, a back-filled far address for either), and without an audit
// event. The payment's lifecycle has two facts, not three — the submitting
// bank's initiation and the clearing house's acceptance — and inventing a third
// here would put a second payment.initiated in every payment's trail. Nothing
// this half produces is lost by the omission: the CSM's payment.accepted event
// carries the whole payment as its payload, back-filled address, DebtorLegTx
// and all. The absence is pinned, not merely current — adding an event here
// fails the four exact-sequence assertions at payment/audit_test.go:75, :169,
// :245 and api/server_test.go:1090.
func (s *Network) AcceptInboundTx(ctx context.Context, tx Tx, id PaymentID) error {
	if _, err := s.self(); err != nil {
		return err
	}
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	// Initiated and nothing else: a payment the clearing house has already
	// taken into a cycle does not need answering, and one that was rejected
	// must not be revived by an answer that was in flight when it died.
	if p.Status != Initiated {
		return ErrInvalidStateTransition
	}
	scheme, ok := s.scheme(p.Scheme)
	if !ok {
		return ErrSchemeNotFound
	}
	before := p
	sc := SchemeContext{Network: s, Tx: tx, Now: s.now()}
	if scheme.Direction() == Push {
		// WHICH ACCOUNT of this bank's the payment is for, decided here and not
		// taken from the row. See the note above on why this half resolves.
		if err := s.resolveOwnPartyTx(ctx, tx, &p.Creditor); err != nil {
			return err
		}
		// The account and participant returned below are the RECEIVING bank's
		// own — the creditor's, for a push — and are deliberately discarded:
		// unlike SubmitPaymentTx, this half must not use them to overwrite
		// CreditorDetails. That field already holds what the payer asserted,
		// and the pacs.008 already sent carries exactly that name; rewriting it
		// here would desynchronise the stored payment from the message that
		// already went out.
		//
		// The REF and the DETAILS are opposite cases and the difference is whose
		// fact each is. Which of this bank's accounts an address names is this
		// bank's own answer and nobody else's; what the payee is called is the
		// payer's assertion and this bank has no standing to correct it.
		if _, _, err := s.creditorSideTx(ctx, tx, scheme, &p, sc); err != nil {
			return err
		}
	} else {
		// A collection this bank has already answered. See the witness note
		// above: the leg is posted, so this half has run, and running it again
		// would answer a duplicate pacs.003 with the ledger's idempotency
		// refusal — which ReasonFor cannot name, so the mesh would return MS03
		// for a collection it in fact accepted.
		if p.DebtorLegTx != "" {
			return nil
		}
		if err := s.resolveOwnPartyTx(ctx, tx, &p.Debtor); err != nil {
			return err
		}
		// See the push arm above: the account and participant here are the
		// RECEIVING (debtor's) bank's own, and are discarded for the same
		// reason — DebtorDetails already holds what the submitting creditor
		// bank asserted.
		if _, _, err := s.debtorSideTx(ctx, tx, scheme, &p, sc); err != nil {
			return err
		}
		if err := s.postDebtorLegTx(ctx, tx, scheme, &p); err != nil {
			return err
		}
	}
	if p.Debtor == before.Debtor && p.Creditor == before.Creditor && p.DebtorLegTx == before.DebtorLegTx {
		return nil
	}
	return tx.PutPayment(ctx, p)
}

// AcceptAtCSM is AcceptAtCSMTx in its own unit of work.
func (s *Network) AcceptAtCSM(ctx context.Context, id PaymentID) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.AcceptAtCSMTx(ctx, tx, id)
		return err
	})
	return out, err
}

// AcceptAtCSMTx is the CLEARING HOUSE's half: it takes a payment both banks
// have now looked at into the open cycle for its scheme, and only then is the
// payment Accepted.
//
// This is where ErrCycleNotOpen went. Which cycle a payment clears in is not
// something either bank decides — a bank that refused its own customer's
// instruction because the CSM had no cut-off window open would be answering a
// question it was never asked. The clearing house owns the cut-off, so the
// clearing house owns the refusal, and on the wire it is a pacs.002 carrying
// TM01 rather than an error returned to a customer.
//
// It writes network rows (the payment, the cycle) and appends the acceptance
// event, which is what makes the act visible to the mesh's book recorder at
// all: network-scoped writes reach it only through the id allocation and the
// audit event. See the note in mesh/books_test.go.
//
// "On receiving the counterparty's ACCP" is a statement about WHEN the clearing
// house runs this, not a precondition it checks. A payment record does not
// distinguish an Initiated payment the far side has accepted from one it has
// not yet seen, and this will take either into a cycle. That invariant lives in
// the message flow — the CSM calls this from the pacs.002 handler and nowhere
// else — so Tasks 10 and 11 own it, and the composition sites that stand in for
// the mesh today own it by calling the halves in order.
func (s *Network) AcceptAtCSMTx(ctx context.Context, tx Tx, id PaymentID) (Payment, error) {
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

	if err := transition(&p, Accepted); err != nil {
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
//
// It is asked of the PARTIES' agents and not of the submitter, for
// mesh.ErrOnUsPayment's reason: which bank submits flips with the scheme's
// direction, and both directions of this defect were reachable — a founded bank
// paying and a founded bank being paid. Both BICs are on the payment already,
// derived rather than asserted (see SubmitPaymentTx, which fills
// CreditorDetails.Agent from the counterparty's own row and the submitting side
// from the bank that ran it), so this reads the roster and nothing else. That is
// the clearing house's own key and its own table, which is what makes this the
// one of the two membership guards that survives Task 18.
//
// ErrRosterEntryNotFound is turned into ErrBankNotAdmitted rather than passed
// through, because the two say different things to the layer above: the lookup
// coming back empty is not by itself a refusal, and only here is it one. Every
// other error is passed through as it arrived, for the reason SubmitPaymentTx's
// agent derivation gives at length — a dropped connection is not a statement
// about the instruction.
//
// A payment refused here has already had its debtor leg posted, and getting the
// customer's money back is csm.clear's job rather than this one's: it rejects
// the payment and the pacs.002 makes the payer's bank reverse. That works in one
// of the two directions and not the other, which is what Mesh.Submit's door
// guard is for. ErrBankNotAdmitted sets both out.
func (s *Network) bothBanksAreMembersTx(ctx context.Context, tx Tx, p Payment) error {
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
//
// The asset check now runs per LEG rather than across both.
//
// Sub-project 1 put it in InitiatePaymentTx precisely because that was the one
// moment both ends were in view. The mesh removes that moment: no single
// actor sees both accounts. Each bank therefore checks its own leg against the
// scheme's asset, which is strictly weaker — two banks could each hold a
// conforming account in a scheme neither is entitled to — and strictly what a
// real bank can do. The stronger check that remains is the ledger's, at
// settlement, exactly as sub-project 1's log describes.
//
// The address comes BACK and is written onto the payment's own ref: a caller
// that quoted nothing gets the account's address filled in rather than a
// stored payment with an empty one.
//
// It also returns the account and the bound participant it checked, not for
// this function's own use but for the caller's: debtorSideTx runs from BOTH
// SubmitPaymentTx (a push, where the debtor is the SUBMITTING bank) and
// AcceptInboundTx (a pull, where the debtor is the RECEIVING bank), and only
// the submitting call may use what it returns to fill DebtorDetails from the
// register — see the comment at the call site in SubmitPaymentTx for why that
// must not happen here.
func (s *Network) debtorSideTx(ctx context.Context, tx Tx, scheme Scheme, p *Payment, sc SchemeContext) (deposit.Account, *Bank, error) {
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
// addressable, able to receive a credit at all, and — for a pull — covered by
// a mandate it holds.
//
// See debtorSideTx for why the asset check is per leg and weaker than it was.
//
// The creditable check is NEW, and it is here rather than at settlement
// because this is the moment the payee's bank first looks at the payee's
// account. Without it a payment reaches settlement and credits a closed
// account, where the money strands: Close needs a zero balance, no withdrawal
// can reach the credit afterwards, and Closed is terminal. Network.DepositTx
// has refused a closed account for the same reason since cash first landed in
// one.
//
// It also returns the account and the bound participant it checked. See
// debtorSideTx's mirror note: creditorSideTx runs from both SubmitPaymentTx (a
// pull, where the creditor is the SUBMITTING bank) and AcceptInboundTx (a
// push, where the creditor is the RECEIVING bank), and only the submitting
// call may use what it returns to fill CreditorDetails from the register.
func (s *Network) creditorSideTx(ctx context.Context, tx Tx, scheme Scheme, p *Payment, sc SchemeContext) (deposit.Account, *Bank, error) {
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
func (s *Network) postDebtorLegTx(ctx context.Context, tx Tx, scheme Scheme, p *Payment) error {
	// The deposit layer is the authority for the funds/status check (run in
	// debtorSideTx); the GL posting here references the deposit account's
	// backing GL account.
	debtor, err := s.bankTx(ctx, tx, p.Debtor.Participant)
	if err != nil {
		return err
	}
	// The suspense account the money lands in is the one for the scheme's
	// asset: a euro scheme clears through the bank's euro suspense.
	debtorAccts, err := debtor.AccountsFor(scheme.Asset())
	if err != nil {
		return err
	}
	debtorGL, err := debtor.glAccountTx(ctx, tx, p.Debtor.Account)
	if err != nil {
		return err
	}
	// The two legs of this one event take economic effect on different days,
	// which is why an entry carries its own value date.
	//
	// The customer's leg value-dates to the debit itself: PSD2 Art. 87(2) puts
	// the payer's debit value date no earlier than the moment the amount leaves
	// the account, and the money is gone from the moment this posts. Value-dating
	// it to settlement instead would hand the payer the settlement delay's worth
	// of interest-free credit, which is precisely what the article forbids.
	//
	// The clearing-suspense leg carries the settlement date, because that is
	// when the bank's position against the scheme actually settles.
	now := s.now()
	posted, err := debtor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":debit",
		Description:    p.Description,
		BookingDate:    now,
		ValueDate:      p.ValueDate,
		Metadata:       paymentMetadata(p),
		Entries: []ledger.Entry{
			{AccountID: debtorGL, Amount: p.Amount, Direction: ledger.Debit, ValueDate: now},
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
func (s *Network) RejectAtCSM(ctx context.Context, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.RejectAtCSMTx(ctx, tx, id, code, reason)
		return err
	})
	return out, err
}

// RejectAtCSMTx is the CLEARING HOUSE's half of a rejection: it transitions the
// payment to Rejected with the code and reason it was refused for, drops it
// from whatever cycle it had been taken into, and records the event.
//
// It touches no bank's book. The money a rejected push payment's payer has
// already parted with is in that payer's own bank's clearing suspense, and
// nobody but that bank may post in it — which is ReverseDebtorLegTx, the other
// half, run by the debtor's bank on receiving the pacs.002.
//
// # This half can happen without the other
//
// This is the FIRST operation in this repository that can half-happen, and it
// is written down here rather than left to be discovered: between the two
// halves the payment is Rejected and the customer's money is still in suspense.
// Every earlier operation either committed whole or rolled back whole, because
// one process held one transaction across the lot.
//
// The mesh does not hide it. Once Tasks 10 and 11 put this flow on the wire, a
// pacs.002 whose reversal fails at the debtor's bank has nobody to answer, so
// it becomes a dead letter and mesh.Drain returns it — the system fails a test
// rather than quietly telling the payer their money is back. Closing the gap
// for real needs a way to carry an unreconciled position, the concept the
// roadmap assigns to sub-project 8; 7b's job is to expose the seam honestly,
// not to close it.
//
// One caller still composes both halves in ONE transaction, so the gap is not
// open there: the seed, which builds a fixed scenario before any actor is
// running and has no mesh to send anything through. A failed reversal takes the
// transition down with it, pinned by TestSeedRejectIsOneUnitOfWork; this
// package's own reject test helper has the same shape, pinned by
// TestAFailedReversalRollsBackTheWholeRejection.
//
// api no longer does. Its rejection goes through mesh.Reject, so the gap above
// is open on that path and is measured rather than described:
// TestARejectionWhoseRefundFailsStandsAndIsDeadLettered forces the reversal to
// fail and asserts that the rejection stands and the failure comes back as a
// dead letter.
//
// The reason text is validated HERE and not in the other half because this is
// the half that stores it: RejectReason is persisted on the payment and copied
// into the audit event's payload, so an unprintable byte in it would be written
// to the store. The reversal half puts the same text in a ledger description,
// and ledger.ReverseTransactionTx validates that itself.
func (s *Network) RejectAtCSMTx(ctx context.Context, tx Tx, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
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

	if err := transition(&p, Rejected); err != nil {
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

// ReverseDebtorLeg is ReverseDebtorLegTx in its own unit of work.
func (s *Network) ReverseDebtorLeg(ctx context.Context, p Payment, reason string) error {
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return s.ReverseDebtorLegTx(ctx, tx, p, reason)
	})
}

// ReverseDebtorLegTx is the DEBTOR BANK's half of a rejection: it gives the
// payer their money back, by reversing the transaction that moved it into this
// bank's clearing suspense.
//
// A payment with no posted leg is a clean no-op — a collection the clearing
// house refused before the payer's bank ever answered it never took the payer's
// money, and there is nothing to give back.
//
// It takes the payment BY VALUE, and unlike AcceptInboundTx that is right here
// rather than the lost update sub-project 7b fixed there. That half wrote a
// caller-supplied copy of the payment back to the payment store, so a stale
// copy resurrected dead fields. This one writes nothing to the payment store at
// all: it posts in the debtor bank's own ledger and returns. The three fields
// it reads — ID, Debtor.Participant and DebtorLegTx — are fixed when the leg is
// posted and never change afterwards, so a copy of the payment taken at any
// point after that names the same transaction.
//
// What it therefore relies on the caller for is the DECISION. It does not load
// the payment and does not look at its status, so nothing here would stop it
// reversing the live debit of a payment that is on its way to settlement. The
// caller establishes that the payment is rejected: the seed's reject by running
// the CSM's half first in the same unit of work, and in the mesh the debtor
// bank's handler, which runs this on a pacs.002 and only for an RJCT — which is
// the path api takes now.
//
// Running it twice is refused rather than absorbed: the ledger flips the
// original to Reversed under a conditional store write, so a second reversal of
// the same leg answers ErrTransactionAlreadyReversed instead of paying the
// payer twice.
func (s *Network) ReverseDebtorLegTx(ctx context.Context, tx Tx, p Payment, reason string) error {
	if p.DebtorLegTx == "" {
		return nil
	}
	debtor, err := s.bankTx(ctx, tx, p.Debtor.Participant)
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
// message is the whole of the input, and there is nothing else to commit
// with it.
func (s *Network) SettleReturn(ctx context.Context, in ReturnInstruction) ([]SettlementStatement, error) {
	var out []SettlementStatement
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.SettleReturnTx(ctx, tx, in)
		return err
	})
	return out, err
}

// SettleReturnTx is the settlement agent's whole part in a return: one
// transaction in its own book, moving the reserves back, and one statement per
// member telling each what happened to its account.
//
// # It reads no payment, and that is the point
//
// Everything it needs is on the instruction, because everything it needs is on
// the message: the two agents' BICs, the amount, the asset. A settlement agent
// under sub-project 8 holds no payment rows at all — it never saw the payment
// clear — so a return it could only execute by looking one up is a return it
// could not execute. That is why iso20022.OriginalTransactionReference had to
// exist before this function could: the pacs.004 this system used to send named
// no parties, and there was nothing else for a bank without a payment row to
// resolve accounts from. See ReadReturn, which is what turns the message into
// the argument here.
//
// # The accounts it moves are its own records', and the ids on the statements are not
//
// Which account each side's reserves sit in is read from the settlement agent's
// own SettlementMember rows, keyed by the two BICs the message carries. It used
// to be read off each bank's row, which was the agent asking its members where
// its own money is.
//
// It still reads the two bank rows, and for one thing only: a SettlementStatement
// names its addressee by ParticipantID, and the message names it by BIC. So the
// BICs are swept over the bank rows rather than indexed — see bankByBICTx, which
// records what that costs — and that read is a crossing this task does not
// close. It closes when a statement's addressee is an address rather than an id,
// which is the same change that lets the settlement agent stop holding rows
// about members at all: Task 18's.
//
// # The one decision it makes
//
// Can the CREDITOR's bank cover the reversal out of the reserves it holds here?
// That is SettleCycleTx's net-payer check on a batch of one, and it is checked
// explicitly for the same reason: a member's settlement account in this book is
// a ledger.Liability — the central bank owes the member its reserve — and
// Book.checkSufficientBalance guards only Asset and Expense accounts, so
// nothing else in the system will refuse it. Refusing to take a member's
// reserve below zero is the central bank declining to extend uncollateralised
// intraday credit, which is the decision a settlement agent exists to make.
// ledger.ErrInsufficientBalance rather than a new sentinel, so that ReasonFor's
// borrowedReasons keeps mapping it to AM04.
//
// It is the creditor's bank that is checked in BOTH directions, because the
// clawback is always at the creditor's bank: which of the two banks is the
// RETURNER flips with the scheme's direction and which of them is paying the
// reserves back does not. See ReturnerOf and PostReturnLegTx.
//
// # A redelivery is caught in the ledger
//
// There is no row here saying "this return has been settled", because there is
// no payment row to write it on. What there is is the idempotency key on the
// reserve reversal, which is derived from the payment's own id, so a second
// instruction naming the same payment is refused by this bank's own ledger. See
// ErrReturnAlreadySettled.
//
// # Which is why the payment id is required, and required HERE
//
// The key is the only record this actor keeps of anything, so an instruction
// with no payment id is not a cosmetic defect: the reversal would move reserves
// between two real banks under ":return-settle", and every later nameless
// return would come back ErrReturnAlreadySettled having settled nothing. The
// first costs money and the rest are silently wrong.
//
// ReadReturn refuses such a message before an instruction exists, and that is
// the guard mesh actually hits. This one is not a second copy of it: ReadReturn
// is a READER, and a ReturnInstruction built any other way — a future caller, a
// test, a second transport — would reopen the hole with nothing in the way. It
// is the argument ReverseReturnLegTx's doc already makes about its own Settled
// check: a guard on the money belongs next to the money, not in whichever
// handler happens to be the only caller today.
func (s *Network) SettleReturnTx(ctx context.Context, tx Tx, in ReturnInstruction) ([]SettlementStatement, error) {
	// Before anything is read, because this is a check on the KEY the posting
	// below will carry rather than on any account. See the note above.
	//
	// A plain error and not a sentinel: it is a judgement about the INSTRUCTION,
	// so ReasonFor's fallback turning it into MS03 on the wire is the right
	// answer rather than a hazard, and it is the same shape cycleOf uses for a
	// settlement instruction that names no cycle. ledger.ValidateText would not
	// catch it — that one refuses control characters and invalid UTF-8, and the
	// empty string is neither.
	// First, for OpenSettlementAccountTx's reason.
	book, err := s.centralBankBook()
	if err != nil {
		return nil, err
	}
	if in.PaymentID == "" {
		return nil, fmt.Errorf("payment: a return instruction naming no payment cannot be settled; its reserve reversal would be keyed by nothing")
	}
	debtor, err := s.bankByBICTx(ctx, tx, in.DebtorAgent)
	if err != nil {
		return nil, err
	}
	creditor, err := s.bankByBICTx(ctx, tx, in.CreditorAgent)
	if err != nil {
		return nil, err
	}
	// The accounts are the settlement agent's own, read from its own member rows
	// by the addresses the message carries. Nothing about which account moves
	// comes from a bank here.
	debtorSettlement, err := s.settlementAccountTx(ctx, tx, in.DebtorAgent, in.Asset)
	if err != nil {
		return nil, err
	}
	creditorSettlement, err := s.settlementAccountTx(ctx, tx, in.CreditorAgent, in.Asset)
	if err != nil {
		return nil, err
	}

	// The redelivery check comes BEFORE the funding check, and the order is not
	// cosmetic. A return that has already settled has already taken the
	// reserves out of the creditor's bank, so on the second delivery that bank
	// is very often short by exactly this amount — and answering AM04 would
	// tell the returning bank its counterparty could not fund a return that in
	// fact completed. Asked in this order, "you have already sent me this" is
	// what comes back, which is the answer to dead-letter.
	key := string(in.PaymentID) + ":return-settle"
	switch _, err := tx.GetTransactionByIdempotencyKey(ctx, CentralBankBook, key); {
	case err == nil:
		return nil, fmt.Errorf("%w: %s", ErrReturnAlreadySettled, in.PaymentID)
	case !errors.Is(err, ledger.ErrTransactionNotFound):
		return nil, err
	}

	held, err := book.BookBalanceTx(ctx, tx, creditorSettlement)
	if err != nil {
		return nil, err
	}
	if held < in.Amount {
		return nil, fmt.Errorf("%w: %s is short %d in %s",
			ledger.ErrInsufficientBalance, creditor.ID, in.Amount-held, in.Asset)
	}

	// The key is checked twice, and the second one is the ledger's. The read
	// above is what makes the ANSWER right; this is what makes the refusal
	// binding, because two deliveries in flight at once both pass a read and
	// only one may post. It is the same pairing PostSettlementAdviceTx makes
	// between its advice row and its idempotency key.
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

	// What each member is TOLD. The balances are read AFTER the posting and
	// inside the same unit of work, which is what makes them CLOSING balances.
	// SettleCycleTx's reason, unchanged: reading them before the posting would
	// produce opening balances labelled CLBD, and the balance is the only thing
	// a member can check its own posting against.
	//
	// The order is the posting's own: the creditor's bank pays the reserves
	// back, the debtor's bank receives them. Fixed rather than incidental,
	// because a caller turns these into messages and a pair whose order came out
	// of map iteration would put a different sequence on the wire each run.
	//
	// StatementRef is "<payment>:rtr" and is deliberately not any row's key:
	// there is no settlement row on this path to lend it one, and a member has
	// no way to check it against anything it holds either way. See
	// SettlementStatement and AdvisedMovement, which both say so.
	now := s.now()
	statements := make([]SettlementStatement, 0, 2)
	for _, side := range []struct {
		member   *Bank
		account  ledger.AccountID
		movement ledger.Amount
	}{
		{creditor, creditorSettlement, -in.Amount},
		{debtor, debtorSettlement, in.Amount},
	} {
		closing, err := book.BookBalanceTx(ctx, tx, side.account)
		if err != nil {
			return nil, err
		}
		statements = append(statements, SettlementStatement{
			Member:         side.member.ID,
			Agent:          side.member.BIC,
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
func (s *Network) PostReturnLeg(ctx context.Context, id PaymentID, reason string) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.PostReturnLegTx(ctx, tx, id, reason)
		return err
	})
	return out, err
}

// PostReturnLegTx is a bank posting its own customer leg of a return, in its
// own book.
//
// # Two legs, and which bank owns each never changes
//
// The CLAWBACK is always at the CREDITOR's bank, out of the account the
// creditor leg actually credited (Payment.CreditorLegAccount). The REFUND is
// always at the DEBTOR's bank, into the payer's account. So which leg this call
// posts follows from which side THIS network's bank is on, and neither the
// caller nor the message chooses: a bank on neither side is
// ErrNotAPartyToThisReturn, and an institution that is no bank at all is
// ErrNotThisInstitutionsAct one step earlier.
//
// What flips with the scheme's direction is only which of the two the RETURNING
// bank is holding — the payee's bank on a push, the payer's bank on a pull —
// and that decides one thing: whether the CLAWBACK may be refused.
//
// # A bank can refuse a leg only if it posts it before it sends
//
// The returning bank posts first and sends afterwards, so its refusal costs
// nothing: no pacs.004 is composed, no reserves move, and the caller is told.
// On a PUSH that bank holds the clawback, so a payee who has spent the money
// stops the return dead — no bank force-takes money back off a customer, and
// the beneficiary bank's answer is a local error rather than a message. See
// TestAPayeeWhoSpentTheMoneyStopsTheReturnBeforeItIsSent.
//
// That makes refusability a property of the LEG and not of the actor: the
// clawback is refusable when the scheme is a push AND this bank is the returner,
// and the refund never is. Both conjuncts, because a bank that is both parties
// is the returner on both legs, and "is this bank the returner" would then make
// the refund refusable too — the returning bank turning down its own customer's
// eight-week right. See the mayRefuse computation below and
// TestAnOnUsPaymentIsRefusedBeforeItReachesAClearingHouse.
//
// The other bank posts AFTER finality and cannot refuse, because there is
// nothing left to refuse: the reserves have moved. On a PULL that is the
// creditor's bank holding the clawback, and the payer's eight-week refund right
// is unconditional, so it forces the posting and carries the shortfall itself.
// That is why creditor banks vet their creditors.
//
// A check that is not a posting would be outrun by the customer between the
// check and the credit, which is why this refuses by NOT POSTING rather than by
// reporting.
//
// # Where a forced clawback lands
//
// Against an open account, on the account: the biller goes overdrawn, which the
// ledger does not refuse — a deposit is a ledger.Liability and
// checkSufficientBalance guards only Asset and Expense — and should not, since
// an overdrawn biller is a debt the bank collects from a customer it still has.
// Against a CLOSED one there is nowhere on the account to put it: a posting into
// a closed account strands, for CloseTx's reason. That is the case
// BankAccounts.ReturnsReceivable exists for, and its only reachable one.
// A store failure is neither, and returns: see the refund below for why that
// discrimination is not optional.
//
// When the holding account is the bank's own unclaimed balances there is no
// customer to check and the money is demonstrably there, so no check is made at
// all — the bank is releasing an obligation it took on, not taking money off
// anybody.
//
// # The refund closes a gap that was a ruling for two tasks
//
// ReturnPaymentTx's doc — deleted with the function at Task 16e — recorded at
// length that a refund into a payer's closed account stranded for ever, and that
// refusing would only trade one stranding for another. It could not be fixed
// while a return was one unit of work over three institutions. It is fixed here
// the same way PostCreditorLegTx fixed the settlement-side twin: divert to this
// bank's unclaimed balances.
//
// The diversion happens on deposit.ErrAccountClosed and on NOTHING else, and
// the check runs BEFORE the payer's GL account is resolved so that a store
// failure reaches this discrimination rather than being collapsed on the way.
// glAccountTx turns every failure of its read into ErrAccountNotInParticipant,
// so a guard written the other way round could not tell a dropped connection
// from a closed account — and money must not be routed on a failure nobody can
// tell apart. That is the defect the first review round on Task 15 found on the
// creditor side. See
// TestAReturnStoreFailureDoesNotRouteTheRefundToUnclaimedBalances.
//
// # The SECOND leg is what sets Returned
//
// One row takes one transition, and the transition is about the last customer
// leg, which is PostCreditorLegTx's shape reused. The second leg is recognised
// by finding the OTHER side's transaction id already on the payment; each leg
// writes its own as it posts. Neither id can be the marker on its own, because
// which leg goes first flips with the direction.
//
// This works because one payment is one row that both banks read. Under Task
// 18's store split it is two rows in two stores and neither bank can see the
// other's, so the second leg will have to be recognised from the message a bank
// receives against the status its own row is already at. That is a real limit
// of this task and not an oversight; see Payment.ReturnClawbackTx.
//
// A bank that is BOTH sides — a payment from one bank to itself — holds both
// legs and would post them one call at a time, clawback first, because the guard
// is written as "my leg, not standing" rather than as a choice between two
// parties. No such payment reaches this function any more: Mesh.Submit refuses
// one, because two customers of one bank paying each other is a book transfer
// and not a clearing payment. The ordering is kept rather than turned into a
// refusal here, for the reason ReadReturn's id guard and SettleReturnTx's are
// both kept — a guard at the boundary is one caller's, and this is the domain.
//
// The same guard makes a redelivered first leg a no-op, which is what
// PostCreditorLegTx does with a redelivered advice and for the same reason: the
// ledger's idempotency key would refuse the second posting anyway, and
// reporting a failure to a handler that did nothing wrong is worse than
// answering with the payment.
//
// # A leg that was UNWOUND is not a leg that was posted
//
// The guard is "not standing" and not "not set", and that is the difference
// between a retried return that repays the payer and one that does not. An RJCT
// leaves this bank's leg Reversed in its own book with the id still on the
// payment (ReverseReturnLegTx says why), so a return asked again arrives here
// with the field non-empty and nothing standing behind it. Read as "already
// posted", the retry answered success without posting, the pacs.004 went out
// anyway, the reserves reversed, the other bank clawed its customer back — and
// the payer got nothing while the amount sat in the returning bank's clearing
// suspense for ever.
//
// So the ledger is consulted (legStandsTx), and the retry is posted under a key
// derived from the attempt it replaces (returnLegKey), because the first
// attempt's key is spent. AM04 is the retriable refusal — a bank short of
// reserves at one moment is not a payer who has lost their refund right — and
// asking again is this system's documented route out of it. See
// TestAReturnRetriedAfterAnUnwindRepaysThePayer.
func (s *Network) PostReturnLegTx(ctx context.Context, tx Tx, id PaymentID, reason string) (Payment, error) {
	self, err := s.self()
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
	bank, err := s.bankTx(ctx, tx, self)
	if err != nil {
		return Payment{}, err
	}
	accts, err := bank.AccountsFor(scheme.Asset())
	if err != nil {
		return Payment{}, err
	}

	// Whether the CLAWBACK may be refused, as a property of the LEG rather than
	// of the actor.
	//
	// Refusing costs nothing only for the bank that posts BEFORE it sends, which
	// is the returner and nobody else — and only on a PUSH is the returner the
	// bank holding the clawback. On a pull the returner holds the REFUND, which
	// is the payer's unconditional eight-week right and is never refusable, and
	// the clawback is then the other bank's, posted after finality with nothing
	// left to refuse. So the clawback is refusable on exactly one combination of
	// direction and role, and it takes both conjuncts to say so.
	//
	// Asking only "is this bank the returner" was true of BOTH legs when one
	// bank is both parties, which made the returning bank refuse its own
	// customer's refund on a pull — the exact inversion of the rule above. That
	// arrangement is now refused where a payment enters the mesh (see
	// Mesh.Submit), and this is stated correctly anyway: a rule that holds
	// because a caller elsewhere never builds the counter-example is a rule
	// nobody is keeping.
	//
	// Written through ReturnerOf rather than as "the creditor's bank on a push"
	// so that who the returner is stays stated once. The two are the same bank;
	// two spellings would be free to disagree.
	mayRefuse := scheme.Direction() == Push && ReturnerOf(scheme, p.Debtor, p.Creditor).Participant == self

	// Has this bank's leg posted, AND does that posting still STAND?
	//
	// The id on the payment answers the first question and not the second, and
	// the difference is a return that repays nobody. ReverseReturnLegTx leaves
	// the id in place deliberately — it records what this bank DID — so after an
	// RJCT the field is non-empty and names a Reversed transaction. Read as
	// "already posted", a retry fell through to the redelivery arm below and
	// answered success without posting, while the rest of the conversation ran
	// to completion around a leg that did not exist.
	//
	// So the LEDGER is asked, which is where "it no longer stands" is recorded.
	// Only ever about THIS bank's own leg: the other side's id names a
	// transaction in the other bank's book, and reaching into it is what
	// TestEachBankBooksItsOwnReturnAndNoOtherBooks forbids.
	var clawbackStands, refundStands bool
	if self == p.Creditor.Participant {
		if clawbackStands, err = s.legStandsTx(ctx, tx, bank, p.ReturnClawbackTx); err != nil {
			return Payment{}, err
		}
	}
	if self == p.Debtor.Participant {
		if refundStands, err = s.legStandsTx(ctx, tx, bank, p.ReturnRefundTx); err != nil {
			return Payment{}, err
		}
	}

	var posted ledger.Transaction
	switch {
	case self == p.Creditor.Participant && !clawbackStands:
		posted, err = s.clawbackTx(ctx, tx, bank, accts, p, reason, mayRefuse, p.ReturnClawbackTx)
		if err != nil {
			return Payment{}, err
		}
		p.ReturnClawbackTx = posted.ID
	case self == p.Debtor.Participant && !refundStands:
		posted, err = s.refundTx(ctx, tx, bank, accts, p, reason, p.ReturnRefundTx)
		if err != nil {
			return Payment{}, err
		}
		p.ReturnRefundTx = posted.ID
	case self == p.Creditor.Participant || self == p.Debtor.Participant:
		return p, nil
	default:
		return Payment{}, fmt.Errorf("%w: %s is neither %s's payer nor its payee", ErrNotAPartyToThisReturn, self, id)
	}

	p.RejectReason = reason
	if p.ReturnClawbackTx != "" && p.ReturnRefundTx != "" {
		if err := transition(&p, Returned); err != nil {
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
//
// Both destinations the creditor leg had are that bank's own liabilities, so
// the direction does not branch — debiting a liability discharges it — but what
// it MEANS does. Against the payee's account it is the bank taking money back
// off a customer who was paid. Against unclaimed balances there is no customer
// to take it from: the payee never received this money, and the bank is
// releasing the obligation it took on when it could not pay them, to the only
// other party with a claim on it.
//
// Against Returns Receivable it is neither, and that is a third thing: the bank
// pays the refund out of its own pocket and books a claim on the biller. See
// PostReturnLegTx for when each is reached.
//
// `replacing` is the id of this bank's own previous attempt at this leg, if it
// has one and it was unwound. See returnLegKey.
func (s *Network) clawbackTx(ctx context.Context, tx Tx, creditor *Bank, accts BankAccounts,
	p Payment, reason string, mayRefuse bool, replacing ledger.TransactionID,
) (ledger.Transaction, error) {
	// Where the money actually is, READ OFF THE PAYMENT rather than resolved
	// again. Only PostCreditorLegTx can know which account it credited, and only
	// at the moment it posted — see Payment.CreditorLegAccount. A Settled
	// payment always carries it, and there is deliberately no fallback to the
	// payee's GL account: that is exactly the wrong guess in the case the field
	// exists for.
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
			// The account cannot be posted to at all, so the bank funds the
			// refund itself and books what it is owed. The description says so
			// for the same reason the refund's diversion does: the money is in a
			// pooled account with nobody's name on it, and the entry is the only
			// place the reason can be read months later.
			from, description = accts.ReturnsReceivable, "Returns receivable: "+description
		case errors.Is(err, deposit.ErrInsufficientAvailable),
			errors.Is(err, deposit.ErrAccountDormant),
			errors.Is(err, deposit.ErrAccountFrozen):
			// The account can still be posted to, so it is: the biller goes
			// overdrawn. A freeze is a block on the CUSTOMER's withdrawals and
			// not on the bank honouring a scheme obligation, and a dormant
			// account is one nobody has touched, not one that has gone.
		default:
			// A store failure. It is not a statement about the account, and the
			// two arms above are choices about where a customer's money goes,
			// so this must not reach either of them.
			return ledger.Transaction{}, err
		}
	}
	return creditor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: returnLegKey(p.ID, "return-claw", replacing),
		Description:    description,
		Entries: []ledger.Entry{
			{AccountID: from, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Credit},
		},
	})
}

// refundTx is the debtor bank's leg: the payer is paid back out of this bank's
// clearing suspense, which the reserves coming back from the central bank fill.
//
// See PostReturnLegTx for why the creditable check runs before the payer's GL
// account is resolved, and why deposit.ErrAccountClosed is the only error that
// may send the money somewhere other than the payer.
//
// `replacing` is the id of this bank's own previous attempt at this leg, if it
// has one and it was unwound. See returnLegKey.
func (s *Network) refundTx(ctx context.Context, tx Tx, debtor *Bank, accts BankAccounts,
	p Payment, reason string, replacing ledger.TransactionID,
) (ledger.Transaction, error) {
	description := "Return of payment " + string(p.ID) + ": " + reason
	to := accts.Unclaimed
	err := debtor.Deposit.CheckCreditTx(ctx, tx, p.Debtor.Account)
	switch {
	case err == nil:
		// The payer's own account, resolved only once the register has said it
		// can take the credit.
		if to, err = debtor.glAccountTx(ctx, tx, p.Debtor.Account); err != nil {
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
		Entries: []ledger.Entry{
			{AccountID: accts.Suspense, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: to, Amount: p.Amount, Direction: ledger.Credit},
		},
	})
}

// legStandsTx reports whether a return leg id names a posting that is still
// standing in this bank's own book.
//
// It is the question PostReturnLegTx's switch means when it looks at
// Payment.ReturnClawbackTx or Payment.ReturnRefundTx, and the field alone
// cannot answer it: the id OUTLIVES the posting. ReverseReturnLegTx leaves it
// there on purpose and marks the transaction Reversed, so the ledger is the only
// place the difference is recorded.
//
// An empty id is "never posted" and needs no read. A transaction the payment
// names and the book does not have is an ERROR and not a false: a payment
// pointing at nothing is a broken row, and answering "post it again" to one
// would post a leg keyed off an id nobody can resolve. Money is not routed on a
// read this system cannot make sense of — the same discrimination clawbackTx
// makes between a closed account and a dropped connection.
func (s *Network) legStandsTx(ctx context.Context, tx Tx, bank *Bank, leg ledger.TransactionID) (bool, error) {
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
//
// The first attempt is keyed by the payment and the leg — "<payment>:return-claw"
// in the creditor's bank's book, "<payment>:return-refund" in the debtor's —
// which is what makes a REDELIVERY of the same instruction refuse itself in the
// ledger.
//
// A RETRY after an unwind is a different posting and must not collide with the
// attempt it replaces: the ledger refuses a second posting under one key, which
// is exactly what would leave a retried return owing its customer money. It must
// not be keyless either, or two deliveries of one retry would pay twice. So the
// key is extended by the id of the reversed attempt, which is already on the
// payment and is unique to that attempt. No column and no counter: a second
// unwind leaves the SECOND attempt's id in the field, so a third attempt keys
// off that, and the chain continues for as long as the scheme lets a bank ask
// again.
func returnLegKey(id PaymentID, leg string, replacing ledger.TransactionID) string {
	key := string(id) + ":" + leg
	if replacing != "" {
		key += ":" + string(replacing)
	}
	return key
}

// ReverseReturnLeg is ReverseReturnLegTx in its own unit of work.
func (s *Network) ReverseReturnLeg(ctx context.Context, id PaymentID, reason string) error {
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return s.ReverseReturnLegTx(ctx, tx, id, reason)
	})
}

// ReverseReturnLegTx unwinds a return leg this bank posted and that the network
// then refused.
//
// It exists because of the ordering the return runs in: the returning bank
// posts its own leg BEFORE it sends the pacs.004, so by the time the settlement
// agent's answer arrives that bank has already moved its customer's money. An
// RJCT — the settlement agent could not cover the reversal, the message named a
// payment it could not resolve — leaves that posting standing against a return
// that will not happen, and the customer looking at a balance nobody can
// explain.
//
// Equal and opposite, through ledger reversal rather than a hand-written
// counter-posting, so the original stays in the book marked Reversed and the
// two are linked. Reversing twice answers ledger.ErrTransactionAlreadyReversed
// rather than paying anybody twice — ReverseDebtorLegTx's guarantee, from the
// same mechanism.
//
// A bank with no leg posted is a no-op rather than an error, which is what
// ReverseDebtorLegTx does with an unposted debtor leg: the caller is a handler
// reacting to a status it did not choose, and "there was nothing to undo" is
// the answer, not a failure.
//
// # A COMPLETED return is refused, and that guard is not defensiveness
//
// This undoes ONE leg, and one leg is only ever the whole of a return while the
// return has stopped. Once both banks have posted there are two legs standing in
// two books, and reversing either alone leaves the other: reverse the clawback
// on a completed push and the payee is made whole while the payer keeps the
// refund, with the amount out of the returning bank's own suspense and the row
// still saying Returned. Nothing in the ledger would notice — both postings are
// individually balanced.
//
// So it refuses anything that is not still Settled. The caller this guard exists
// for is mesh's bank.receiveReturnStatus, which acts on a MESSAGE: a status
// arriving late, or twice, is exactly the shape that would otherwise unwind half
// of a return that finished, and a handler cannot be relied on to have checked a
// status it was not sent. There it surfaces as a dead letter, which is the
// truthful outcome for an RJCT about a return this network completed. ErrInvalidStateTransition rather than a new sentinel —
// it is this package's word for an operation a payment's status does not permit,
// and reasonTable already classifies it as a defect here rather than a judgement
// to answer a counterparty with.
//
// # The transaction id is left on the payment, and it means something narrower
//
// It is not cleared. It records what this bank DID, and it did post; the ledger
// is where the fact that it no longer stands is recorded, on the transaction
// itself. Leaving it is also what makes a RETRY postable at all — returnLegKey
// derives the retry's idempotency key from the reversed attempt's id, so the
// field is the only record of which attempt this is.
//
// What that costs is a field whose meaning is "this bank has an attempt at this
// leg", NOT "this bank's leg stands". An earlier version of this paragraph said
// there was no reader for whom a stale id decides anything. There was:
// PostReturnLegTx's switch asked "is the id empty?" to decide whether to post,
// so a return asked again after an unwind fell through to the idempotent-
// redelivery arm and answered success having posted nothing — while the rest of
// the conversation ran to completion, the other bank clawed its customer back,
// and the payer was never repaid. That reader now asks the LEDGER (legStandsTx),
// which is the only place that can answer it.
//
// A reader that has this id and wants to know whether the leg holds must do the
// same. The one place that does NOT is PostReturnLegTx's transition to Returned,
// which reads both ids and can read only one book: it relies on a leg being
// reversible only while the OTHER side is still unposted, which the Settled
// guard above is what makes true.
func (s *Network) ReverseReturnLegTx(ctx context.Context, tx Tx, id PaymentID, reason string) error {
	self, err := s.self()
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
	case p.Creditor.Participant:
		leg = p.ReturnClawbackTx
	case p.Debtor.Participant:
		leg = p.ReturnRefundTx
	default:
		return fmt.Errorf("%w: %s is neither %s's payer nor its payee", ErrNotAPartyToThisReturn, self, id)
	}
	if leg == "" {
		return nil
	}
	bank, err := s.bankTx(ctx, tx, self)
	if err != nil {
		return err
	}
	_, err = bank.Ledger.ReverseTransactionTx(ctx, tx, leg, "Rejected return of payment "+string(p.ID)+": "+reason)
	return err
}

// ---------------------------------------------------------------------------
// Read accessors
// ---------------------------------------------------------------------------

// GetPayment returns a payment by ID.
func (s *Network) GetPayment(ctx context.Context, id PaymentID) (Payment, error) {
	var out Payment
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetPayment(ctx, id)
		return err
	})
	return out, err
}

// GetCycle returns a clearing cycle by ID.
func (s *Network) GetCycle(ctx context.Context, id CycleID) (ClearingCycle, error) {
	var out ClearingCycle
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetCycle(ctx, id)
		return err
	})
	return out, err
}

// GetMandate returns one of THIS bank's mandates by ID, and refuses another
// bank's with ErrNotThisBanksMandate rather than answering it.
//
// A not-found and a somebody-else's are deliberately different answers here,
// where on a payment they would not be. A mandate id is this bank's own
// sequence, so an id that resolves to another bank's row is a reader that has
// crossed a boundary rather than a client that guessed; saying so is more useful
// than a 404 and it is what Task 18d makes automatic, when the row is simply not
// in this bank's store.
func (s *Network) GetMandate(ctx context.Context, id MandateID) (Mandate, error) {
	self, err := s.self()
	if err != nil {
		return Mandate{}, err
	}
	var out Mandate
	err = s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		m, err := tx.GetMandate(ctx, id)
		if err != nil {
			return err
		}
		if m.Creditor.Participant != self {
			return fmt.Errorf("%w: %s asked for a mandate whose creditor banks at %s",
				ErrNotThisBanksMandate, self, m.Creditor.Participant)
		}
		out = m
		return nil
	})
	return out, err
}

// ReserveBalance returns a participant's reserve book balance in one asset, as
// held at the central bank. Central-bank settlement accounts are plain GL
// accounts with no deposit layer, so this is just the GL book balance.
//
// It takes an asset because a bank holds one reserve account per asset, and a
// single number across several of them would be an addition of unlike things.
//
// Two different answers mean two different things, and this task makes the first
// of them reachable for the first time. A bank the central bank holds NO account
// for is ErrSettlementMemberNotFound — the true state of a bank that is founded
// and not yet admitted, which is not an error about the asset at all. A bank it
// holds accounts for but not in this asset is ErrParticipantAssetNotFound. See
// settlementAccountTx, which is where both come from.
//
// The account is read from the CENTRAL BANK's own member row. This is the
// operator console asking the central bank about the central bank's book, and
// the answer should not depend on what the bank being asked about wrote down —
// a console that read the bank's note of its account number would report a
// balance the bank had chosen the account for.
//
// The id-to-BIC step is the same crossing settlementLegsTx records: the caller
// holds a ParticipantID and the central bank's records are keyed by address, so
// the bank's own row is read to translate. The console is outside the entity
// boundary by construction — it is the one caller in this system that holds
// every institution's store — so it is the one place that translation is not a
// crossing to close.
func (s *Network) ReserveBalance(ctx context.Context, id ParticipantID, asset ledger.AssetCode) (ledger.Amount, error) {
	book, err := s.centralBankBook()
	if err != nil {
		return 0, err
	}
	var out ledger.Amount
	err = s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		p, err := tx.GetBank(ctx, id)
		if err != nil {
			return err
		}
		settlement, err := s.settlementAccountTx(ctx, tx, p.BIC, asset)
		if err != nil {
			return err
		}
		out, err = book.BookBalanceTx(ctx, tx, settlement)
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// transition moves a payment to a new status if the edge is legal.
func transition(p *Payment, to PaymentStatus) error {
	allowed := map[PaymentStatus][]PaymentStatus{
		Initiated: {Accepted, Rejected},
		Accepted:  {Cleared, Rejected},
		Cleared:   {Settled},
		Settled:   {Returned},
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
// with it. The identifier is stored on the payment rather than derived, and the
// two ids are used as lookup keys — so a control character in one names nothing
// this system ever generated, and what a caller is owed is the domain's refusal
// rather than whatever the store makes of a key like that. Out of store/pg it
// was a raw SQLSTATE. See ledger.ValidateText.
func validateParty(field string, ref PartyRef) error {
	if err := ledger.ValidateText(field+".participant", string(ref.Participant)); err != nil {
		return err
	}
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
//
// # It used to sweep, and the narrowing is Task 18a's
//
// It called tx.ListBanks and asked every member's register in turn, which made
// it "the network's directory" — a directory assembled by reading every bank's
// book, on the happy path of every received message. That is crossing 2 in the
// db-per-entity design's table, and it is the read a bank cannot make once it
// holds only its own register: ListBanks has no answer and neither has
// another member's BookID.
//
// The narrowing is not a reduction in what this system can honestly answer. A
// bank has never had the standing to say whether an address at ANOTHER bank
// exists — that is the other bank's register and the other bank's customer — and
// the sweep's answers about them were the only thing that made an asserted
// counterparty BIC dangerous (see mesh's
// TestAWrongCounterpartyAgentIsRefusedByTheBankItNames). What is genuinely lost
// is the network-wide lookup, and it is lost because this network has no
// directory SERVICE to replace it with. A real one does: SEPA banks resolve an
// IBAN's bank out of a subscribed IBAN-to-BIC table, and aliases that are not
// bank-issued (a phone number, an email address) go to a separate central
// service — the EPC's Proxy Lookup Service, or UPI — precisely because no bank
// can guarantee they are unique. Neither is a sweep, and neither is something
// one bank can build out of the others' books.
//
// So the question this answers is "is this address one of MINE", which is the
// one a receiving bank actually has to answer, and the one that produces AC01
// for anything else.
//
// Which bank is asking was an ARGUMENT for exactly one task. Task 18a narrowed
// the sweep and had nowhere to read the asking bank from, so it took it from the
// caller and said in this doc that Task 18b would take it away again. This is
// that: the register searched is this network's own, and a network that is not a
// member bank's has no register to search. See Network.self.
//
// The two are not the same guard wearing different clothes. With the argument,
// "in its own register" was a claim about every caller — three handlers in the
// mesh and one route in api, each passing an id it had for its own reasons — and
// a fifth caller passing somebody else's would have swept nothing and resolved
// perfectly well in another bank's register. There is no such caller now, and
// there is no way to write one.
func (s *Network) ResolveIdentifier(ctx context.Context, ident deposit.Identifier) (PartyRef, error) {
	var out PartyRef
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ResolveIdentifierTx(ctx, tx, ident)
		return err
	})
	return out, err
}

// ResolveIdentifierTx is ResolveIdentifier within a caller-supplied unit of work.
//
// Two accounts at this bank holding the identifier is ErrIdentifierAmbiguous
// rather than the first one found. Uniqueness is not enforced at write time —
// deposit.Register.AddIdentifier says why — so a collision is representable, and
// choosing between two of this bank's own accounts would route a payment on the
// strength of listing order.
//
// It used to be able to find that collision ACROSS banks as well, which was the
// widest scope a sweep could see. That scope is gone with the sweep and nothing
// replaces it: two banks issuing one IBAN is a fact neither of them can now
// observe, and in life it is the IBAN's issuer registry that stops it happening
// rather than anybody's directory noticing afterwards.
func (s *Network) ResolveIdentifierTx(ctx context.Context, tx Tx, ident deposit.Identifier) (PartyRef, error) {
	if err := ident.Validate("identifier"); err != nil {
		return PartyRef{}, err
	}
	self, err := s.self()
	if err != nil {
		return PartyRef{}, err
	}
	bank, err := s.bankTx(ctx, tx, self)
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
		return PartyRef{Participant: bank.ID, Account: holders[0].ID, Identifier: ident}, nil
	default:
		return PartyRef{}, deposit.ErrIdentifierAmbiguous
	}
}

// checkPartyTx verifies that a party's participant exists and its deposit
// account exists within that participant, returning both the account and the
// bound participant so callers that need more than existence (the account's
// Asset, GLAccount, ... or the participant's live Deposit/Ledger handles)
// don't have to fetch either again. Binding costs nothing beyond the fetch
// this function already makes — s.bind wraps the row it just read with live
// handles built from the Network's own stores, not a second round trip — so
// returning a bound participant here is free, and a caller re-fetching the
// same row with bankTx (as debtorSideTx used to) is not.
//
// # Only a NOT-FOUND becomes a domain error
//
// The same discipline addressedPartyTx keeps on the inbound side, and for the
// same reason — this one is on the MONEY path. It is reached from
// AcceptInboundTx through creditorSideTx/debtorSideTx, so a receiving bank runs
// it on every message it answers, and mesh/bank.go's answer turns whatever comes
// back into a pacs.002 through ReasonFor. `if err != nil { return
// ErrAccountNotInParticipant }` — which is what this was — makes AC01 "incorrect
// account number" the answer to a dropped database connection, so a transient
// fault at the RECEIVING bank tells the SENDING bank its customer's IBAN is
// wrong. On a push the payer's debit is then reversed, and a fault that would
// have cleared on a retry has become a permanent rejection carrying a false
// reason. ErrParticipantNotFound is the same shape one element up: RC01, "bank
// identifier incorrect", about a bank that is fine.
//
// So a genuine not-found — the store's own sentinel, the one contract note in
// store.go guarantees — maps to the domain sentinel and everything else is
// returned unchanged, to fall through ReasonFor's default to MS03: this agent
// could not carry the instruction out, which is the only true thing there is to
// say. TestAcceptInboundDoesNotBlameTheSenderForAStoreFailure is the pin, on
// both halves.
func (s *Network) checkPartyTx(ctx context.Context, tx Tx, field string, ref PartyRef) (deposit.Account, *Bank, error) {
	if err := validateParty(field, ref); err != nil {
		return deposit.Account{}, nil, err
	}
	rec, err := tx.GetBank(ctx, ref.Participant)
	if errors.Is(err, ErrParticipantNotFound) {
		return deposit.Account{}, nil, fmt.Errorf("%w: %s", ErrParticipantNotFound, ref.Participant)
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
//
// It returns the address rather than merely approving one, which is the whole
// point: without that, a caller who quoted nothing — the ordinary case, since
// the API's identifier field is optional — settled a payment whose stored
// debtor and creditor addresses were both empty, and "a payment records the
// address it was sent to" was true only when the caller volunteered it.
//
// Three outcomes, in the order they are decided:
//
//   - The account holds no identifier in the scheme's addressing scheme:
//     ErrUnaddressableAccount. An account with no IBAN is not a SEPA party, and
//     nothing here can invent one for it.
//   - Nothing quoted, and exactly one candidate: that candidate, back-filled.
//     Several candidates: ErrAmbiguousAddress, because choosing between them
//     would stamp an address onto a settled payment on the strength of slice
//     order — the same refusal ResolveIdentifier makes for the same reason.
//   - Something quoted: it must be one of the account's identifiers IN THE
//     SCHEME'S SCHEME. The ids route the payment and the address records how it
//     was reached; the two disagreeing means one of them is wrong, and this
//     layer does not get to choose which.
//
// That last "in the scheme's scheme" is why the loop scans inScheme rather than
// the whole set. Scanning the whole set asks only whether the account holds the
// quoted address somewhere, which is not the question: an account holding both
// an IBAN and a card PAN would have a sepa.ct payment accepted — and stored —
// quoting the PAN. It is unreachable while IdentifierIBAN is the only scheme
// shipped, which is precisely the argument for fixing it now: the design's
// load-bearing claim is that a card PAN drops in as a constant, and the first
// day it does, an address bound to the scheme would silently stop being bound
// to it. AddressedBy() is decorative unless it decides this answer.
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
	//
	// The two differ for exactly one reason today: an IBAN is stored in its
	// readable display form and quoted on the wire compact, and those are one
	// address (deposit.Identifier.MatchValue). A payment translated out of a
	// received pacs.008 quotes what the message carried, so == would refuse a
	// party this bank had just successfully resolved BY that address — the
	// directory and this check would disagree about what an address is, which is
	// the shape of bug that only shows up once a real message arrives.
	//
	// Returning the stored identifier rather than the quoted one keeps the
	// invariant that a payment's recorded address is one the account actually
	// holds, in the form this bank writes it. The quoted form is not lost — it
	// is what the message says, and the message is what the counterparty keeps.
	// TestCreditTransferRoundTripsThroughTheWireForSeedShapedAddresses drives
	// both halves.
	for _, ident := range inScheme {
		if ident.Matches(ref.Identifier) {
			return ident, nil
		}
	}
	return deposit.Identifier{}, ErrIdentifierMismatch
}

// removeFromCycleTx drops a payment from its (open) clearing cycle.
func (s *Network) removeFromCycleTx(ctx context.Context, tx Tx, p Payment) error {
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

func copyPositions(in map[ParticipantID]ledger.Amount) map[ParticipantID]ledger.Amount {
	out := make(map[ParticipantID]ledger.Amount, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
