package payment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
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
// ledger.NetworkBook. Because payment.Tx embeds deposit.Tx embeds ledger.Tx,
// one transaction reaches all of them, so an operation that touches several
// banks is a single unit of work: SettleCycle moves reserves at the central
// bank, mirrors the movement in every participant's book and pays out every
// creditor inside one Update, and a failure anywhere leaves none of it behind.
//
// This is what a real RTGS calls a settlement window: an interval during which
// the settlement agent holds the participants' accounts, checks that every net
// payer can cover its position, and posts the whole batch or none of it. The
// database transaction is what supplies the window here. See SettleCycle.
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

	// store backs every book the network creates and every network-scoped
	// entity it records.
	store Store

	// ledgers and deposits are the same store seen through the narrower
	// interfaces the Book and Register types are written against. They are
	// derived from store rather than injected beside it, so all three layers
	// are guaranteed to address the same data.
	ledgers  ledger.Store
	deposits deposit.Store

	// mu guards schemes, the only thing a Network holds in memory. Schemes are
	// registered at startup and read on every payment.
	mu      sync.RWMutex
	schemes map[SchemeID]Scheme

	// centralBank holds the participants' reserve accounts. It is a handle over
	// the store, not state: its chart of accounts is resolved from the store on
	// use (see centralBankChartTx), so it survives a Store.Reset and works
	// against a database that was populated by an earlier process.
	centralBank *ledger.Book
}

// CentralBankBook is the BookID of the central bank's own book of accounts. It
// is a real chart of accounts, unlike ledger.NetworkBook, which labels the
// network-scoped entities that belong to no single bank.
const CentralBankBook ledger.BookID = "central-bank"

// The central bank's chart of accounts is identified by name rather than by a
// cached ID, because a cached ID does not survive Store.Reset and is wrong the
// moment a second process opens the same database.
const (
	cbLedgerName   = "Central Bank"
	cbReservesName = "Member Reserves"
	cbCapitalName  = "Central Bank Capital"
	cbAssetsName   = "Settlement Assets"
)

// NewNetwork creates a payment network with the SEPA Credit Transfer and SEPA
// Direct Debit schemes registered.
//
// Every book the network creates — the central bank's and one per participant —
// lives in the given store and reads time from the given clock, so that booking
// dates, value dates and audit timestamps line up across all of them.
//
// The constructor performs no I/O: the central bank's chart of accounts is
// created on first use and looked up thereafter, so calling NewNetwork against
// a store that already holds a network is safe and idempotent.
func NewNetwork(store Store, clock func() time.Time) *Network {
	ledgers := ledgerView{store}
	s := &Network{
		clock:       clock,
		store:       store,
		ledgers:     ledgers,
		deposits:    depositView{store},
		schemes:     make(map[SchemeID]Scheme),
		centralBank: ledger.NewBook(ledgers, CentralBankBook, clock),
	}
	s.RegisterScheme(SCT{})
	s.RegisterScheme(SDD{})
	return s
}

func (s *Network) now() time.Time { return s.clock() }

// Store returns the store every layer of this network shares, so a caller can
// open its own unit of work — or reset the whole system — against the same
// data the network reads.
func (s *Network) Store() Store { return s.store }

// RegisterScheme adds (or replaces) a scheme. Adding support for instant or
// card payments is just a matter of registering a type that implements
// Scheme — the orchestration below is scheme-agnostic.
func (s *Network) RegisterScheme(sc Scheme) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schemes[sc.ID()] = sc
}

// scheme looks up a registered scheme.
func (s *Network) scheme(id SchemeID) (Scheme, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, ok := s.schemes[id]
	return sc, ok
}

// CentralBank exposes the central-bank ledger for inspection (balances,
// audit trail). Treat it as read-only.
func (s *Network) CentralBank() *ledger.Book { return s.centralBank }

// bind attaches the live handles a Participant record needs to be usable: its
// own book of accounts and the deposit register over it, both scoped to its
// BookID within the network's store.
//
// The handles are stateless, so binding is cheap and a bound Participant is
// safe to hold; the record's data fields are a snapshot, as with every other
// value the store returns.
func (s *Network) bind(p Participant) *Participant {
	p.Ledger = ledger.NewBook(s.ledgers, p.BookID, s.clock)
	p.Deposit = deposit.NewRegister(s.deposits, p.Ledger, p.BookID, s.clock)
	return &p
}

// participantTx loads a participant and binds its live handles.
func (s *Network) participantTx(ctx context.Context, tx Tx, id ParticipantID) (*Participant, error) {
	rec, err := tx.GetParticipant(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.bind(rec), nil
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
		if cb, err = s.centralBank.CreateLedgerTx(ctx, tx, cbLedgerName); err != nil {
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
		if reserves, err = s.centralBank.CreateSubledgerTx(ctx, tx, cb.ID, cbReservesName); err != nil {
			return "", "", err
		}
	}
	if capital.ID == "" {
		if capital, err = s.centralBank.CreateSubledgerTx(ctx, tx, cb.ID, cbCapitalName); err != nil {
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
// The lookup is by (capital subledger, name, asset) rather than by name alone.
// Keeping the name stable across assets is what lets a book written before the
// asset dimension existed — whose account was backfilled to EUR — still be
// found rather than duplicated.
func (s *Network) centralBankAssetsAccountTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (ledger.AccountID, error) {
	_, capital, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return "", err
	}
	accounts, err := tx.ListAccounts(ctx, CentralBankBook)
	if err != nil {
		return "", err
	}
	for _, a := range accounts {
		if a.SubledgerID == capital && a.Name == cbAssetsName && a.Asset == asset {
			return a.ID, nil
		}
	}
	created, err := s.centralBank.CreateAccountTx(ctx, tx, capital, cbAssetsName, ledger.Asset, asset)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// ---------------------------------------------------------------------------
// Participants
// ---------------------------------------------------------------------------

// AddParticipant registers a new bank. It builds the bank's own book of
// accounts and chart of accounts and opens a reserve account for it at the
// central bank, once per asset the bank operates in.
//
// An empty assets list means []ledger.AssetCode{"EUR"}. That is a default for
// the *set of assets a bank joins with*, not for the asset of any individual
// account: every account below is created with an asset its caller named.
//
// The new bank starts with zero reserves; fund it with Deposit.
func (s *Network) AddParticipant(ctx context.Context, name string, assets []ledger.AssetCode) (*Participant, error) {
	var out *Participant
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.AddParticipantTx(ctx, tx, name, assets)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AddParticipantTx is AddParticipant within a caller-supplied unit of work. The
// bank's chart of accounts, its reserve account at the central bank and the
// participant record are all written through the same Tx, so a bank can never
// exist without the accounts it needs.
func (s *Network) AddParticipantTx(ctx context.Context, tx Tx, name string, assets []ledger.AssetCode) (*Participant, error) {
	if err := ledger.ValidateText("name", name); err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		assets = []ledger.AssetCode{"EUR"}
	}

	// The bank gets its own book within the shared store, identified by its
	// participant ID, so its chart of accounts is numbered independently of
	// every other bank's.
	id, err := tx.NextID(ctx, ledger.NetworkBook, "bank")
	if err != nil {
		return nil, err
	}
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
	// The bank's reserve accounts live in the central-bank ledger, alongside
	// every other member's.
	reserveSubledger, _, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return nil, err
	}

	// One set of internal accounts per asset. Naming them with the asset in
	// parentheses keeps them apart in a chart of accounts that now holds
	// several of each.
	accounts := make(map[ledger.AssetCode]ParticipantAccounts, len(assets))
	for _, asset := range assets {
		def, err := s.assetDef(asset)
		if err != nil {
			return nil, err
		}
		// The asset has to exist in both books: the bank holds its own
		// suspense and reserve accounts, and the central bank holds the
		// matching vostro account.
		if err := ensureAsset(ctx, tx, bank, def); err != nil {
			return nil, err
		}
		if err := ensureAsset(ctx, tx, s.centralBank, def); err != nil {
			return nil, err
		}
		// The other side of every reserve credit in this asset.
		if _, err := s.centralBankAssetsAccountTx(ctx, tx, asset); err != nil {
			return nil, err
		}

		suspense, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Clearing Suspense ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return nil, err
		}
		reserve, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Reserve at Central Bank ("+string(asset)+")", ledger.Asset, asset)
		if err != nil {
			return nil, err
		}
		cbReserve, err := s.centralBank.CreateAccountTx(ctx, tx, reserveSubledger, "Reserve: "+name+" ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return nil, err
		}
		accounts[asset] = ParticipantAccounts{Suspense: suspense.ID, Reserve: reserve.ID, Settlement: cbReserve.ID}
	}

	p := Participant{
		ID:                ParticipantID(id),
		Name:              name,
		BookID:            bookID,
		CustomerSubledger: customers.ID,
		Assets:            accounts,
		CreatedAt:         s.now(),
	}
	if err := tx.PutParticipant(ctx, p); err != nil {
		return nil, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventParticipantAdded, string(p.ID), p); err != nil {
		return nil, err
	}
	return s.bind(p), nil
}

// assetDef returns the definition for a well-known asset code. The network
// needs it because it creates accounts in books it does not otherwise
// populate, and an account cannot reference an unregistered asset.
func (s *Network) assetDef(code ledger.AssetCode) (ledger.AssetDef, error) {
	switch code {
	case "EUR":
		return ledger.AssetDef{Code: "EUR", Name: "Euro", Scale: 2, Class: ledger.Fiat}, nil
	case "USD":
		return ledger.AssetDef{Code: "USD", Name: "US Dollar", Scale: 2, Class: ledger.Fiat}, nil
	default:
		return ledger.AssetDef{}, fmt.Errorf("%w: %s", ledger.ErrAssetNotFound, code)
	}
}

// ensureAsset registers an asset in a book if it is not registered already.
// Idempotent, because several participants join the same central-bank book.
func ensureAsset(ctx context.Context, tx Tx, book *ledger.Book, def ledger.AssetDef) error {
	_, err := book.CreateAssetTx(ctx, tx, def.Code, def.Name, def.Scale, def.Class)
	if errors.Is(err, ledger.ErrDuplicateAsset) {
		return nil
	}
	return err
}

// Deposit funds a customer deposit account with cash, modelled as the bank
// placing the cash on reserve at the central bank.
//
// Two books move in step, keeping the reserve mirror intact:
//
//	bank ledger:    Debit  Reserve at Central Bank (asset)  / Credit customer (liability)
//	central bank:   Debit  Settlement Assets (asset)        / Credit Reserve: <bank> (liability)
//
// Which reserve moves is decided by the funded account's own asset, read here
// rather than chosen by the caller. Cash paid into a dollar account raises the
// bank's dollar reserve; there is nothing for a caller to pick and therefore
// nothing to default, and the two legs cannot end up in different assets.
//
// Both postings go through one Tx, so the mirror can never be half-written.
func (s *Network) Deposit(ctx context.Context, participant ParticipantID, account deposit.AccountID, amount ledger.Amount, description string) error {
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return s.DepositTx(ctx, tx, participant, account, amount, description)
	})
}

// DepositTx is Deposit within a caller-supplied unit of work.
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
	p, err := s.participantTx(ctx, tx, participant)
	if err != nil {
		return err
	}
	funded, err := tx.GetDepositAccount(ctx, p.BookID, account)
	if err != nil {
		return ErrAccountNotInParticipant
	}
	gl, asset := funded.GLAccount, funded.Asset
	accts, err := p.AccountsFor(asset)
	if err != nil {
		return err
	}

	if _, err := p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		Entries: []ledger.Entry{
			{AccountID: accts.Reserve, Amount: amount, Direction: ledger.Debit},
			{AccountID: gl, Amount: amount, Direction: ledger.Credit},
		},
	}); err != nil {
		return err
	}

	cbAssets, err := s.centralBankAssetsAccountTx(ctx, tx, asset)
	if err != nil {
		return err
	}
	_, err = s.centralBank.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Reserve credit: " + p.Name,
		Entries: []ledger.Entry{
			{AccountID: cbAssets, Amount: amount, Direction: ledger.Debit},
			{AccountID: accts.Settlement, Amount: amount, Direction: ledger.Credit},
		},
	})
	return err
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
func (s *Network) CreateMandateTx(ctx context.Context, tx Tx, debtor, creditor PartyRef, maxAmount ledger.Amount) (Mandate, error) {
	if _, err := s.checkPartyTx(ctx, tx, "debtor", debtor); err != nil {
		return Mandate{}, err
	}
	if _, err := s.checkPartyTx(ctx, tx, "creditor", creditor); err != nil {
		return Mandate{}, err
	}

	id, err := tx.NextID(ctx, ledger.NetworkBook, "mnd")
	if err != nil {
		return Mandate{}, err
	}
	m := Mandate{
		ID:        MandateID(id),
		Debtor:    debtor,
		Creditor:  creditor,
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
func (s *Network) RevokeMandateTx(ctx context.Context, tx Tx, id MandateID) error {
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

	id, err := tx.NextID(ctx, ledger.NetworkBook, "cyc")
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

// SettleCycle settles a closed cycle. It moves each participant's net
// position across reserve accounts at the central bank, mirrors that movement
// in each bank's own reserve account (clearing its suspense to zero), and
// posts the creditor leg of every payment so the payees receive their funds.
//
// # The settlement window
//
// All of it is one unit of work. That is the whole point: a net payer that
// cannot cover its position must abort the batch, not leave the other members
// paid and the central bank's books moved. Under store/mem the Update holds the
// write lock for the duration; under store/pg it is one BEGIN … COMMIT, with
// every touched account row locked. Either way the interval in which the books
// are inconsistent is not observable, which is what a real RTGS buys with a
// locked settlement window.
//
// # Ordering
//
// Participants are visited in registration order, not in map order, so the
// entries of the central bank's settlement transaction come out the same on
// every run. That order is persisted — store/pg gives each entry an explicit
// seq — so leaving it to Go's randomised map iteration would make the stored
// transaction differ from run to run for no reason.
func (s *Network) SettleCycle(ctx context.Context, id CycleID) (Settlement, error) {
	var out Settlement
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.SettleCycleTx(ctx, tx, id)
		return err
	})
	return out, err
}

// SettleCycleTx is SettleCycle within a caller-supplied unit of work.
func (s *Network) SettleCycleTx(ctx context.Context, tx Tx, id CycleID) (Settlement, error) {
	c, err := tx.GetCycle(ctx, id)
	if err != nil {
		return Settlement{}, err
	}
	if c.Status != CycleClosed {
		return Settlement{}, ErrCycleNotClosed
	}

	// The cycle settles in its scheme's asset, resolved once here and used for
	// every participant in the batch. A member that does not hold that asset
	// fails the whole batch, exactly as an underfunded member does — there is
	// no reserve account to fall back to.
	scheme, ok := s.scheme(c.Scheme)
	if !ok {
		return Settlement{}, ErrSchemeNotFound
	}
	asset := scheme.Asset()

	// 1. Central-bank settlement transaction: move netted reserves between
	//    participants. The net positions sum to zero, so this balances.
	//
	//    The participants are read in registration order so that both this
	//    transaction's entries and the mirror postings below are deterministic.
	legs, err := s.settlementLegsTx(ctx, tx, c, asset)
	if err != nil {
		return Settlement{}, err
	}

	cbEntries := make([]ledger.Entry, 0, len(legs))
	for _, leg := range legs {
		if leg.net > 0 {
			cbEntries = append(cbEntries, ledger.Entry{AccountID: leg.accounts.Settlement, Amount: leg.net, Direction: ledger.Credit})
		} else {
			cbEntries = append(cbEntries, ledger.Entry{AccountID: leg.accounts.Settlement, Amount: -leg.net, Direction: ledger.Debit})
		}
	}

	var settlementTx ledger.TransactionID
	if len(cbEntries) > 0 {
		posted, err := s.centralBank.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			IdempotencyKey: string(c.ID) + ":settle",
			Description:    "Settlement of clearing cycle " + string(c.ID),
			Entries:        cbEntries,
		})
		if err != nil {
			return Settlement{}, err
		}
		settlementTx = posted.ID

		// 2. Mirror each net movement in the participant's own ledger,
		//    moving funds between its suspense and reserve so suspense
		//    returns to zero and its reserve asset tracks the central bank.
		for _, leg := range legs {
			p, accts, net := leg.participant, leg.accounts, leg.net
			var entries []ledger.Entry
			if net > 0 { // net receiver: reserve up, suspense down
				entries = []ledger.Entry{
					{AccountID: accts.Reserve, Amount: net, Direction: ledger.Debit},
					{AccountID: accts.Suspense, Amount: net, Direction: ledger.Credit},
				}
			} else { // net payer: reserve down, suspense up
				entries = []ledger.Entry{
					{AccountID: accts.Suspense, Amount: -net, Direction: ledger.Debit},
					{AccountID: accts.Reserve, Amount: -net, Direction: ledger.Credit},
				}
			}
			if _, err := p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
				IdempotencyKey: string(c.ID) + ":reserve:" + string(p.ID),
				Description:    "Net settlement of cycle " + string(c.ID),
				Entries:        entries,
			}); err != nil {
				return Settlement{}, err
			}
		}
	}

	// 3. Post the creditor leg of every payment: the payee's bank releases
	//    the funds from its suspense to the payee's account.
	for _, pid := range c.PaymentIDs {
		p, err := tx.GetPayment(ctx, pid)
		if err != nil {
			return Settlement{}, err
		}
		creditor, err := s.participantTx(ctx, tx, p.Creditor.Participant)
		if err != nil {
			return Settlement{}, err
		}
		creditorAccts, err := creditor.AccountsFor(asset)
		if err != nil {
			return Settlement{}, err
		}
		creditorGL, err := creditor.glAccountTx(ctx, tx, p.Creditor.Account)
		if err != nil {
			return Settlement{}, err
		}
		posted, err := creditor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			IdempotencyKey: string(p.ID) + ":credit",
			Description:    p.Description,
			ValueDate:      p.ValueDate,
			Metadata:       paymentMetadata(&p),
			Entries: []ledger.Entry{
				{AccountID: creditorAccts.Suspense, Amount: p.Amount, Direction: ledger.Debit},
				{AccountID: creditorGL, Amount: p.Amount, Direction: ledger.Credit},
			},
		})
		if err != nil {
			return Settlement{}, err
		}
		p.CreditorLegTx = posted.ID
		if err := transition(&p, Settled); err != nil {
			return Settlement{}, err
		}
		if err := tx.PutPayment(ctx, p); err != nil {
			return Settlement{}, err
		}
		if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentSettled, string(p.ID), p); err != nil {
			return Settlement{}, err
		}
	}

	settlementID, err := tx.NextID(ctx, ledger.NetworkBook, "set")
	if err != nil {
		return Settlement{}, err
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
		return Settlement{}, err
	}

	c.Status = CycleSettled
	c.SettlementID = st.ID
	if err := tx.PutCycle(ctx, c); err != nil {
		return Settlement{}, err
	}
	// One payment.settled per payment (above) plus one cycle.settled, all on
	// this transaction — the batch is atomic, so its audit trail is too.
	if err := s.appendAuditTx(ctx, tx, ledger.EventCycleSettled, string(c.ID), st); err != nil {
		return Settlement{}, err
	}
	return st, nil
}

// settlementLeg pairs a participant with its non-zero net position in a cycle,
// together with the internal accounts that position moves through in the
// cycle's asset.
type settlementLeg struct {
	participant *Participant
	accounts    ParticipantAccounts
	net         ledger.Amount
}

// settlementLegsTx resolves a cycle's net positions to participants in
// registration order, and each participant to its accounts in the cycle's
// asset.
//
// Registration order rather than map order because these legs decide the entry
// order of the settlement transaction, which is persisted. Iterating the
// NetPositions map directly would produce a different stored transaction on
// every run.
//
// A member with a position but no accounts in the asset fails here, before
// anything is posted, with ErrParticipantAssetNotFound.
func (s *Network) settlementLegsTx(ctx context.Context, tx Tx, c ClearingCycle, asset ledger.AssetCode) ([]settlementLeg, error) {
	participants, err := tx.ListParticipants(ctx)
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
		accts, err := p.AccountsFor(asset)
		if err != nil {
			return nil, err
		}
		legs = append(legs, settlementLeg{participant: p, accounts: accts, net: net})
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
}

// InitiatePayment validates and accepts a payment into the open clearing
// cycle for its scheme. It immediately posts the debtor leg — the payer's
// money leaves their account into the bank's clearing suspense — value-dated
// to the scheme's settlement date. The creditor is paid later, at settlement.
func (s *Network) InitiatePayment(ctx context.Context, req InitiatePaymentRequest) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.InitiatePaymentTx(ctx, tx, req)
		return err
	})
	return out, err
}

// InitiatePaymentTx is InitiatePayment within a caller-supplied unit of work.
// The funds check, the debtor-leg posting and the payment record share one Tx,
// so two concurrent payments cannot both spend the same balance.
func (s *Network) InitiatePaymentTx(ctx context.Context, tx Tx, req InitiatePaymentRequest) (Payment, error) {
	scheme, ok := s.scheme(req.Scheme)
	if !ok {
		return Payment{}, ErrSchemeNotFound
	}
	if req.Amount <= 0 {
		return Payment{}, ErrInvalidPaymentAmount
	}
	debtorAccount, err := s.checkPartyTx(ctx, tx, "debtor", req.Debtor)
	if err != nil {
		return Payment{}, err
	}
	creditorAccount, err := s.checkPartyTx(ctx, tx, "creditor", req.Creditor)
	if err != nil {
		return Payment{}, err
	}
	// Both legs must be denominated in the scheme's asset. This runs for every
	// scheme unconditionally — unlike a check tucked inside a scheme's own
	// Validate, it cannot be skipped by a scheme (e.g. a future card scheme)
	// whose Validate does something other than call validateFunds.
	if debtorAccount.Asset != scheme.Asset() || creditorAccount.Asset != scheme.Asset() {
		return Payment{}, ErrAssetMismatch
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
	if req.EndToEndID != "" {
		switch _, err := tx.GetPaymentByEndToEndID(ctx, req.EndToEndID); {
		case err == nil:
			return Payment{}, ErrDuplicateEndToEndID
		case !errors.Is(err, ErrPaymentNotFound):
			return Payment{}, err
		}
	}

	cycle, err := tx.GetOpenCycle(ctx, req.Scheme)
	if errors.Is(err, ErrCycleNotFound) {
		return Payment{}, ErrCycleNotOpen
	} else if err != nil {
		return Payment{}, err
	}

	id, err := tx.NextID(ctx, ledger.NetworkBook, "pay")
	if err != nil {
		return Payment{}, err
	}

	now := s.now()
	p := Payment{
		ID:          PaymentID(id),
		Scheme:      req.Scheme,
		Debtor:      req.Debtor,
		Creditor:    req.Creditor,
		Amount:      req.Amount,
		MandateID:   req.MandateID,
		EndToEndID:  req.EndToEndID,
		Status:      Initiated,
		CycleID:     cycle.ID,
		BookingDate: now,
		ValueDate:   now.Add(scheme.SettlementDelay()),
		Description: req.Description,
		Metadata:    req.Metadata,
		CreatedAt:   now,
	}

	if err := scheme.Validate(ctx, &p, SchemeContext{Network: s, Tx: tx, Now: now}); err != nil {
		return Payment{}, err
	}
	// Two events, because initiation and acceptance are two different facts: the
	// instruction arrived and passed scheme validation, and — below, once the
	// payer's funds are in suspense and the payment has joined a cycle — the
	// network took responsibility for it. A rejected instruction rolls back with
	// the transaction, so neither event is ever recorded for one.
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentInitiated, string(p.ID), p); err != nil {
		return Payment{}, err
	}

	// Debtor leg: money leaves the payer into the bank's clearing suspense.
	// The deposit layer is the authority for the funds/status check (run in
	// Validate above); the GL posting here references the deposit account's
	// backing GL account.
	debtor, err := s.participantTx(ctx, tx, p.Debtor.Participant)
	if err != nil {
		return Payment{}, err
	}
	// The suspense account the money lands in is the one for the scheme's
	// asset: a euro scheme clears through the bank's euro suspense.
	debtorAccts, err := debtor.AccountsFor(scheme.Asset())
	if err != nil {
		return Payment{}, err
	}
	debtorGL, err := debtor.glAccountTx(ctx, tx, p.Debtor.Account)
	if err != nil {
		return Payment{}, err
	}
	posted, err := debtor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":debit",
		Description:    p.Description,
		BookingDate:    now,
		ValueDate:      p.ValueDate,
		Metadata:       paymentMetadata(&p),
		Entries: []ledger.Entry{
			{AccountID: debtorGL, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: debtorAccts.Suspense, Amount: p.Amount, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return Payment{}, err
	}
	p.DebtorLegTx = posted.ID

	if err := transition(&p, Accepted); err != nil {
		return Payment{}, err
	}
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

// RejectPayment rejects a payment before it has cleared, reversing the debtor
// leg if one was posted and removing it from its clearing cycle.
func (s *Network) RejectPayment(ctx context.Context, id PaymentID, reason string) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.RejectPaymentTx(ctx, tx, id, reason)
		return err
	})
	return out, err
}

// RejectPaymentTx is RejectPayment within a caller-supplied unit of work.
func (s *Network) RejectPaymentTx(ctx context.Context, tx Tx, id PaymentID, reason string) (Payment, error) {
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

	if p.DebtorLegTx != "" {
		debtor, err := s.participantTx(ctx, tx, p.Debtor.Participant)
		if err != nil {
			return Payment{}, err
		}
		if _, err := debtor.Ledger.ReverseTransactionTx(ctx, tx, p.DebtorLegTx, "Reject payment "+string(p.ID)+": "+reason); err != nil {
			return Payment{}, err
		}
	}
	if err := s.removeFromCycleTx(ctx, tx, p); err != nil {
		return Payment{}, err
	}

	if err := transition(&p, Rejected); err != nil {
		return Payment{}, err
	}
	p.RejectReason = reason
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentRejected, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
}

// ReturnPayment returns a settled payment (a SEPA R-transaction). It posts
// compensating transactions that move the funds back from the creditor to the
// debtor across the central bank, undoing the original flow.
func (s *Network) ReturnPayment(ctx context.Context, id PaymentID, reason string) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ReturnPaymentTx(ctx, tx, id, reason)
		return err
	})
	return out, err
}

// ReturnPaymentTx is ReturnPayment within a caller-supplied unit of work. All
// three compensating postings — debtor's bank, creditor's bank, central bank —
// commit together or not at all.
func (s *Network) ReturnPaymentTx(ctx context.Context, tx Tx, id PaymentID, reason string) (Payment, error) {
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

	debtor, err := s.participantTx(ctx, tx, p.Debtor.Participant)
	if err != nil {
		return Payment{}, err
	}
	creditor, err := s.participantTx(ctx, tx, p.Creditor.Participant)
	if err != nil {
		return Payment{}, err
	}
	// A return runs the original flow backwards, so it moves through the same
	// accounts: the scheme's asset on both sides.
	asset := scheme.Asset()
	debtorAccts, err := debtor.AccountsFor(asset)
	if err != nil {
		return Payment{}, err
	}
	creditorAccts, err := creditor.AccountsFor(asset)
	if err != nil {
		return Payment{}, err
	}
	debtorGL, err := debtor.glAccountTx(ctx, tx, p.Debtor.Account)
	if err != nil {
		return Payment{}, err
	}
	creditorGL, err := creditor.glAccountTx(ctx, tx, p.Creditor.Account)
	if err != nil {
		return Payment{}, err
	}

	// Debtor's bank refunds the payer, funded by reserves coming back in.
	if _, err := debtor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":return-debit",
		Description:    "Return of payment " + string(p.ID) + ": " + reason,
		Entries: []ledger.Entry{
			{AccountID: debtorAccts.Reserve, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: debtorGL, Amount: p.Amount, Direction: ledger.Credit},
		},
	}); err != nil {
		return Payment{}, err
	}

	// Creditor's bank claws the funds back from the payee, paying out reserves.
	if _, err := creditor.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":return-credit",
		Description:    "Return of payment " + string(p.ID) + ": " + reason,
		Entries: []ledger.Entry{
			{AccountID: creditorGL, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: creditorAccts.Reserve, Amount: p.Amount, Direction: ledger.Credit},
		},
	}); err != nil {
		return Payment{}, err
	}

	// Central bank reverses the reserve movement.
	if _, err := s.centralBank.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":return-settle",
		Description:    "Return settlement for payment " + string(p.ID),
		Entries: []ledger.Entry{
			{AccountID: creditorAccts.Settlement, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: debtorAccts.Settlement, Amount: p.Amount, Direction: ledger.Credit},
		},
	}); err != nil {
		return Payment{}, err
	}

	if err := transition(&p, Returned); err != nil {
		return Payment{}, err
	}
	p.RejectReason = reason
	if err := tx.PutPayment(ctx, p); err != nil {
		return Payment{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventPaymentReturned, string(p.ID), p); err != nil {
		return Payment{}, err
	}
	return p, nil
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

// GetMandate returns a mandate by ID.
func (s *Network) GetMandate(ctx context.Context, id MandateID) (Mandate, error) {
	var out Mandate
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetMandate(ctx, id)
		return err
	})
	return out, err
}

// ReserveBalance returns a participant's reserve book balance in one asset, as
// held at the central bank. Central-bank settlement accounts are plain GL
// accounts with no deposit layer, so this is just the GL book balance.
//
// It takes an asset because a bank holds one reserve account per asset, and a
// single number across several of them would be an addition of unlike things.
// Returns ErrParticipantAssetNotFound if the bank does not operate in it.
func (s *Network) ReserveBalance(ctx context.Context, id ParticipantID, asset ledger.AssetCode) (ledger.Amount, error) {
	var out ledger.Amount
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		p, err := tx.GetParticipant(ctx, id)
		if err != nil {
			return err
		}
		accts, err := p.AccountsFor(asset)
		if err != nil {
			return err
		}
		acct, err := tx.GetAccount(ctx, CentralBankBook, accts.Settlement)
		if err != nil {
			return err
		}
		out, err = tx.BookBalance(ctx, CentralBankBook, accts.Settlement, acct.Type.NormalBalance())
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
// with it. The IBAN is stored on the payment; the two ids are used as lookup
// keys, and a key that reaches store/pg with a control character in it raises a
// SQLSTATE rather than answering "no such row". See ledger.ValidateText.
func validateParty(field string, ref PartyRef) error {
	if err := ledger.ValidateText(field+".participant", string(ref.Participant)); err != nil {
		return err
	}
	if err := ledger.ValidateText(field+".account", string(ref.Account)); err != nil {
		return err
	}
	return ledger.ValidateText(field+".iban", ref.IBAN)
}

// checkPartyTx verifies that a party's participant exists and its deposit
// account exists within that participant, returning the account so callers
// that need more than existence (its Asset, its GLAccount, ...) don't have to
// fetch it again.
func (s *Network) checkPartyTx(ctx context.Context, tx Tx, field string, ref PartyRef) (deposit.Account, error) {
	if err := validateParty(field, ref); err != nil {
		return deposit.Account{}, err
	}
	p, err := tx.GetParticipant(ctx, ref.Participant)
	if err != nil {
		return deposit.Account{}, ErrParticipantNotFound
	}
	acct, err := tx.GetDepositAccount(ctx, p.BookID, ref.Account)
	if err != nil {
		return deposit.Account{}, ErrAccountNotInParticipant
	}
	return acct, nil
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
