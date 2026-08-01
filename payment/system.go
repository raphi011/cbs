package payment

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

	// ledgers, deposits and lendings are the same store seen through the
	// narrower interfaces the Book, Register and Portfolio types are written
	// against. They are derived from store rather than injected beside it, so
	// all layers are guaranteed to address the same data.
	ledgers  ledger.Store
	deposits deposit.Store
	lendings lending.Store
	products product.Store

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
		lendings:    lendingView{store},
		products:    productView{store},
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

// Scheme looks up a registered scheme, for callers outside this package that
// have to ask the scheme a question before they can act on a payment — which
// bank submits it, for one (see api's bank submit handler).
//
// It is a wrapper over the unexported scheme rather than a rename of it, so
// that the dozens of internal call sites stay as they are.
func (s *Network) Scheme(id SchemeID) (Scheme, bool) { return s.scheme(id) }

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
// own book of accounts, the deposit register and the lending portfolio over
// it, all scoped to its BookID within the network's store.
//
// The handles are stateless, so binding is cheap and a bound Participant is
// safe to hold; the record's data fields are a snapshot, as with every other
// value the store returns.
func (s *Network) bind(p Participant) *Participant {
	p.Ledger = ledger.NewBook(s.ledgers, p.BookID, s.clock)
	p.Deposit = deposit.NewRegister(s.deposits, p.Ledger, p.BookID, s.clock)
	p.Lending = lending.NewPortfolio(s.lendings, p.Ledger, p.BookID, s.clock)
	p.Catalogue = product.NewCatalogue(s.products, p.Ledger, p.BookID, s.clock)
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
// The lookup is by (capital subledger, name, asset). Matching on the asset as
// well as the name is what makes it idempotent per asset rather than per
// central bank — see cbAssetsAccountName for why the name carries the asset
// too.
func (s *Network) centralBankAssetsAccountTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (ledger.AccountID, error) {
	_, capital, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return "", err
	}
	accounts, err := tx.ListAccounts(ctx, CentralBankBook)
	if err != nil {
		return "", err
	}
	return s.centralBankAssetsAccountIn(ctx, tx, capital, accounts, asset)
}

// centralBankAssetsAccountIn is centralBankAssetsAccountTx against a chart the
// caller has already resolved and listed.
//
// It exists for AddParticipantTx, which needs one of these per asset a bank
// joins with. Neither the capital subledger nor the central bank's chart of
// accounts changes underneath that loop, so re-resolving both on every
// iteration was pure repetition — idempotent, and a full re-listing of the
// central bank's accounts each time round.
//
// `accounts` may be stale with respect to accounts the caller's own loop has
// created since. That is safe here because the match is on the asset and
// AddParticipantTx handles each asset exactly once; a caller that could ask
// twice for the same asset would create a second account and must list again.
func (s *Network) centralBankAssetsAccountIn(ctx context.Context, tx Tx, capital ledger.SubledgerID, accounts []ledger.Account, asset ledger.AssetCode) (ledger.AccountID, error) {
	for _, a := range accounts {
		if a.SubledgerID == capital && a.Name == cbAssetsAccountName(asset) && a.Asset == asset {
			return a.ID, nil
		}
	}
	created, err := s.centralBank.CreateAccountTx(ctx, tx, capital, cbAssetsAccountName(asset), ledger.Asset, asset)
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
func (s *Network) AddParticipant(ctx context.Context, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*Participant, error) {
	var out *Participant
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.AddParticipantTx(ctx, tx, name, bic, assets)
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
func (s *Network) AddParticipantTx(ctx context.Context, tx Tx, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*Participant, error) {
	if err := ledger.ValidateText("name", name); err != nil {
		return nil, err
	}
	// Validated at admission rather than at first use. A bank with a malformed
	// BIC is one the mesh cannot route to, and the moment to refuse it is when
	// it joins — not when the first payment addressed to it fails somewhere
	// else entirely.
	if err := bic.Validate(); err != nil {
		return nil, fmt.Errorf("bic: %w", err)
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
	// every other member's. Resolved once, with the chart of accounts listed
	// alongside it: the per-asset loop below needs both and neither moves while
	// it runs.
	reserveSubledger, capitalSubledger, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	cbAccounts, err := tx.ListAccounts(ctx, CentralBankBook)
	if err != nil {
		return nil, err
	}

	// One set of internal accounts per asset. Naming them with the asset in
	// parentheses keeps them apart in a chart of accounts that now holds
	// several of each.
	accounts := make(map[ledger.AssetCode]ParticipantAccounts, len(assets))
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
			// three accounts in the chart.
			continue
		}
		// The other side of every reserve credit in this asset.
		if _, err := s.centralBankAssetsAccountIn(ctx, tx, capitalSubledger, cbAccounts, asset); err != nil {
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

	// The bank's default deposit product, created here because a bank with no
	// product cannot open an account: every deposit account is opened FROM one.
	// It belongs with the chart of accounts for the same reason those are built
	// here — onboarding a bank produces a bank that works.
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

	p := Participant{
		ID:                ParticipantID(id),
		Name:              name,
		BIC:               bic,
		BookID:            bookID,
		CustomerSubledger: customers.ID,
		ProductID:         basic.ID,
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
	debtorAcct, err := s.checkPartyTx(ctx, tx, "debtor", debtor)
	if err != nil {
		return Mandate{}, err
	}
	creditorAcct, err := s.checkPartyTx(ctx, tx, "creditor", creditor)
	if err != nil {
		return Mandate{}, err
	}
	// Both ends of a mandate must be denominated in the same asset. A mandate
	// authorizes a future direct debit from the debtor to the creditor, and
	// MaxAmount is one integer — an integer that means one thing at the
	// debtor's scale and another at the creditor's is not a limit on anything.
	// Submission would refuse every payment such a mandate could authorize
	// (each leg is checked against the scheme's asset by its own bank, and
	// these two cannot both match it), so this only makes the refusal happen
	// where it can be understood instead of at first use.
	if debtorAcct.Asset != creditorAcct.Asset {
		return Mandate{}, fmt.Errorf("%w: debtor %s, creditor %s",
			ErrAssetMismatch, debtorAcct.Asset, creditorAcct.Asset)
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

// SubmitPayment is SubmitPaymentTx in its own unit of work.
func (s *Network) SubmitPayment(ctx context.Context, req InitiatePaymentRequest) (Payment, error) {
	var out Payment
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.SubmitPaymentTx(ctx, tx, req)
		return err
	})
	return out, err
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
	if req.EndToEndID != "" {
		switch _, err := tx.GetPaymentByEndToEndID(ctx, req.EndToEndID); {
		case err == nil:
			return Payment{}, ErrDuplicateEndToEndID
		case !errors.Is(err, ErrPaymentNotFound):
			return Payment{}, err
		}
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
		BookingDate: now,
		ValueDate:   now.Add(scheme.SettlementDelay()),
		Description: req.Description,
		Metadata:    req.Metadata,
		CreatedAt:   now,
	}

	sc := SchemeContext{Network: s, Tx: tx, Now: now}
	push := scheme.Direction() == Push
	if push {
		err = s.debtorSideTx(ctx, tx, scheme, &p, sc)
	} else {
		err = s.creditorSideTx(ctx, tx, scheme, &p, sc)
	}
	if err != nil {
		return Payment{}, err
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

// AcceptInboundTx is the RECEIVING bank's half: the half SubmitPaymentTx did
// not run, because the bank that ran that one could not see this side.
//
// For a push the receiver is the creditor's bank, and its half is a check —
// the account exists, is in the scheme's asset, is addressable, and can be
// credited at all. Nothing is posted: the payee is paid at settlement, out of
// the creditor bank's suspense, exactly as before.
//
// For a pull the receiver is the DEBTOR's bank, and its half is the one that
// moves money: it checks the account, the asset, the address and the funds,
// and posts the debtor leg. That posting is the whole reason a direct debit
// posts nothing at submission — until this runs, the payer's money has not
// moved and no actor has looked at their account.
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
		if err := s.creditorSideTx(ctx, tx, scheme, &p, sc); err != nil {
			return err
		}
	} else {
		if err := s.debtorSideTx(ctx, tx, scheme, &p, sc); err != nil {
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
func (s *Network) debtorSideTx(ctx context.Context, tx Tx, scheme Scheme, p *Payment, sc SchemeContext) error {
	account, err := s.checkPartyTx(ctx, tx, "debtor", p.Debtor)
	if err != nil {
		return err
	}
	if account.Asset != scheme.Asset() {
		return ErrAssetMismatch
	}
	address, err := addressFor(scheme, p.Debtor, account)
	if err != nil {
		return err
	}
	p.Debtor.Identifier = address
	// The funds check. It is the debtor bank's alone, which is why Scheme.Validate
	// is now only ever this: the receiving side of a pull and the submitting
	// side of a push are the same bank looking at the same account.
	return scheme.Validate(ctx, p, sc)
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
func (s *Network) creditorSideTx(ctx context.Context, tx Tx, scheme Scheme, p *Payment, sc SchemeContext) error {
	account, err := s.checkPartyTx(ctx, tx, "creditor", p.Creditor)
	if err != nil {
		return err
	}
	if account.Asset != scheme.Asset() {
		return ErrAssetMismatch
	}
	address, err := addressFor(scheme, p.Creditor, account)
	if err != nil {
		return err
	}
	p.Creditor.Identifier = address
	creditor, err := s.participantTx(ctx, tx, p.Creditor.Participant)
	if err != nil {
		return err
	}
	if err := creditor.Deposit.CheckCreditTx(ctx, tx, p.Creditor.Account); err != nil {
		return err
	}
	// The mandate, which in SEPA the CREDITOR holds — so it is checked by the
	// creditor's bank, and for a pull that means synchronously, at submission.
	return scheme.ValidateMandate(ctx, p, sc)
}

// postDebtorLegTx moves the payer's money out of their account and into their
// own bank's clearing suspense. Whoever runs it is the debtor's bank: the
// submitting bank for a push, the receiving bank for a pull.
func (s *Network) postDebtorLegTx(ctx context.Context, tx Tx, scheme Scheme, p *Payment) error {
	// The deposit layer is the authority for the funds/status check (run in
	// debtorSideTx); the GL posting here references the deposit account's
	// backing GL account.
	debtor, err := s.participantTx(ctx, tx, p.Debtor.Participant)
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
// The synchronous routes that still compose both halves — api's
// rejectWholePayment, the seed's reject — pass ONE transaction to both, so the
// gap is not open there and never has been: a failed reversal takes the
// transition down with it. TestAFailedReversalRollsBackTheWholeRejection is the
// pin.
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
// caller establishes that the payment is rejected: api's rejectWholePayment and
// the seed's reject by running the CSM's half first in the same unit of work,
// and in the mesh the debtor bank's handler, which runs this on a pacs.002 and
// only for an RJCT.
//
// Running it twice is refused rather than absorbed: the ledger flips the
// original to Reversed under a conditional store write, so a second reversal of
// the same leg answers ErrTransactionAlreadyReversed instead of paying the
// payer twice.
func (s *Network) ReverseDebtorLegTx(ctx context.Context, tx Tx, p Payment, reason string) error {
	if p.DebtorLegTx == "" {
		return nil
	}
	debtor, err := s.participantTx(ctx, tx, p.Debtor.Participant)
	if err != nil {
		return err
	}
	_, err = debtor.Ledger.ReverseTransactionTx(ctx, tx, p.DebtorLegTx, "Reject payment "+string(p.ID)+": "+reason)
	return err
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
		out, err = s.centralBank.BookBalanceTx(ctx, tx, accts.Settlement)
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
// with it. The identifier is stored on the payment rather than derived; the
// two ids are used as lookup keys, and a key that reaches store/pg with a
// control character in it raises a SQLSTATE rather than answering "no such
// row". See ledger.ValidateText.
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
// it names. It is the network's directory.
//
// A real network's directory is a service with an index; this is a sweep over
// the members, which is the honest shape at four banks and the boundary at
// which a proxy-alias registry would arrive. Aliases that are NOT bank-issued
// (a phone number, an email address) cannot be resolved this way at all, since
// no member can guarantee they are unique — which is why SEPA's Proxy Lookup
// Service and UPI are separate central services rather than a sweep like this.
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
// Two members holding the identifier is ErrIdentifierAmbiguous rather than the
// first one found. Uniqueness is enforced per bank — that is the widest scope a
// register can see — so a collision across banks is representable, and choosing
// between them here would route a payment to a bank on the strength of listing
// order.
func (s *Network) ResolveIdentifierTx(ctx context.Context, tx Tx, ident deposit.Identifier) (PartyRef, error) {
	if err := ident.Validate("identifier"); err != nil {
		return PartyRef{}, err
	}
	members, err := tx.ListParticipants(ctx)
	if err != nil {
		return PartyRef{}, err
	}
	var found PartyRef
	hits := 0
	for _, m := range members {
		holders, err := tx.ListDepositAccountsByIdentifier(ctx, m.BookID, ident)
		if err != nil {
			return PartyRef{}, err
		}
		hits += len(holders)
		if hits > 1 {
			return PartyRef{}, deposit.ErrIdentifierAmbiguous
		}
		if len(holders) == 1 {
			found = PartyRef{Participant: m.ID, Account: holders[0].ID, Identifier: ident}
		}
	}
	if hits == 0 {
		return PartyRef{}, deposit.ErrIdentifierNotFound
	}
	return found, nil
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
