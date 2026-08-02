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
			// four accounts in the chart.
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
		// A Liability, because it is money the bank owes somebody it has not yet
		// identified — the same class as a customer's deposit, and specifically
		// not an asset of the bank's.
		unclaimed, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Unclaimed Balances ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return nil, err
		}
		cbReserve, err := s.centralBank.CreateAccountTx(ctx, tx, reserveSubledger, "Reserve: "+name+" ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return nil, err
		}
		accounts[asset] = ParticipantAccounts{
			Suspense:   suspense.ID,
			Reserve:    reserve.ID,
			Unclaimed:  unclaimed.ID,
			Settlement: cbReserve.ID,
		}
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
	debtorAcct, _, err := s.checkPartyTx(ctx, tx, "debtor", debtor)
	if err != nil {
		return Mandate{}, err
	}
	creditorAcct, _, err := s.checkPartyTx(ctx, tx, "creditor", creditor)
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
		//
		//    The mirror leg is the MEMBER's act, and PostSettlementAdviceTx is
		//    where it lives. Driving it from here is temporary: 15b.2 replaces
		//    this loop with a statement per member, sent, and each member calls
		//    the same method for itself. Calling it here first is what makes the
		//    extraction checkable — nothing observable moves.
		for _, leg := range legs {
			if _, err := s.PostSettlementAdviceTx(ctx, tx, leg.participant.ID, AdvisedMovement{
				Account: leg.accounts.Settlement,
				Asset:   asset,
				// ClosingBalance is not read here and is filled in 15b.2, when
				// this call moves to the bank and the balance comes off a
				// statement. Zero is honest: nothing has told this bank a
				// balance yet.
				Movement: leg.net,
				CycleID:  c.ID,
			}); err != nil {
				return Settlement{}, err
			}
		}
	}

	// 3. Post the creditor leg of every payment: the payee's bank releases
	//    the funds from its suspense to the payee's account.
	//
	//    The leg is the PAYEE's BANK's act, and PostCreditorLegTx is where it
	//    lives — including the CheckCreditTx this loop could not afford while it
	//    was the whole batch's unit of work. Driving it from here is temporary
	//    in the same way the mirror leg above is: 15b.3 moves it to a message.
	for _, pid := range c.PaymentIDs {
		p, err := tx.GetPayment(ctx, pid)
		if err != nil {
			return Settlement{}, err
		}
		if _, err := s.PostCreditorLegTx(ctx, tx, p.Creditor.Participant, pid); err != nil {
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

// PostSettlementAdviceTx is a member bank booking a cut-off it was told about:
// the mirror leg, in its OWN ledger, and the row that records that it did.
//
// # What the mirror leg is
//
// A bank's clearing suspense holds money that has left a customer and not yet
// settled between banks. Settlement is when it stops being in transit, so the
// suspense moves against the reserve: a net receiver's reserve goes up and its
// suspense down, a net payer's the reverse. Suspense returns to zero only if the
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
// SettleCycleTx still CALLS this, once per member, and 15b.2 is what replaces
// that call with a statement each member acts on for itself. Extracting it
// first, with every measurement unchanged, is what makes the move afterwards
// provably a move and not a rewrite.
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
// The idempotency key is the same "<cycle>:reserve:<participant>" the central
// bank used to post under, so a redelivered statement posts nothing; and the
// advice row is checked first, so it does not even try.
func (s *Network) PostSettlementAdviceTx(ctx context.Context, tx Tx, by ParticipantID, m AdvisedMovement) (SettlementAdvice, error) {
	p, err := s.participantTx(ctx, tx, by)
	if err != nil {
		return SettlementAdvice{}, err
	}
	accts, err := p.AccountsFor(m.Asset)
	if err != nil {
		return SettlementAdvice{}, err
	}
	if m.Account != accts.Settlement {
		return SettlementAdvice{}, fmt.Errorf("%w: %s is not %s's reserve account", ErrStatementNotForThisBank, m.Account, by)
	}

	switch existing, err := tx.GetSettlementAdvice(ctx, p.BookID, m.CycleID, m.Asset); {
	case err == nil && existing.Status == AdvicePosted:
		return existing, nil
	case err != nil && !errors.Is(err, ErrSettlementAdviceNotFound):
		return SettlementAdvice{}, err
	}

	now := s.now()
	advice := SettlementAdvice{
		Book:           p.BookID,
		CycleID:        m.CycleID,
		Asset:          m.Asset,
		Movement:       m.Movement,
		ClosingBalance: m.ClosingBalance,
		Status:         AdviceAdvised,
		AdvisedAt:      now,
	}
	// Written BEFORE the posting, so a posting that fails leaves the row at
	// Advised rather than leaving no trace. That row is the unreconciled
	// position, and it is the only thing in this system that can say a bank was
	// told and did not book.
	if err := tx.PutSettlementAdvice(ctx, p.BookID, advice); err != nil {
		return SettlementAdvice{}, err
	}

	var entries []ledger.Entry
	switch {
	case m.Movement > 0: // net receiver: reserve up, suspense down
		entries = []ledger.Entry{
			{AccountID: accts.Reserve, Amount: m.Movement, Direction: ledger.Debit},
			{AccountID: accts.Suspense, Amount: m.Movement, Direction: ledger.Credit},
		}
	case m.Movement < 0: // net payer: reserve down, suspense up
		entries = []ledger.Entry{
			{AccountID: accts.Suspense, Amount: -m.Movement, Direction: ledger.Debit},
			{AccountID: accts.Reserve, Amount: -m.Movement, Direction: ledger.Credit},
		}
	default:
		// A movement of nothing produces no leg, and the central bank sends no
		// statement for a position of zero. This arm is a guard on a caller.
		return advice, nil
	}
	posted, err := p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(m.CycleID) + ":reserve:" + string(p.ID),
		Description:    "Net settlement of cycle " + string(m.CycleID),
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
// reserves moved and the payee's bank has been paid. What is unsettled is which
// of that bank's own accounts holds the money, which is between the bank and its
// customer and is not a fact about the payment.
//
// # Only the payee's bank may call it
//
// On a push the clearing house tells both banks the payment settled: the payer's
// bank because it has been waiting for the answer to its instruction, the payee's
// bank because it has this leg to post. Only the second may post it. See
// ErrNotThisBanksPayment.
func (s *Network) PostCreditorLegTx(ctx context.Context, tx Tx, by ParticipantID, id PaymentID) (Payment, error) {
	p, err := tx.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	if p.Creditor.Participant != by {
		return Payment{}, fmt.Errorf("%w: %s is %s's creditor, not %s's", ErrNotThisBanksPayment, id, p.Creditor.Participant, by)
	}
	if p.Status == Settled {
		// A redelivered advice. The ledger's idempotency key would refuse the
		// second posting anyway; this refuses to transition twice, which
		// ErrInvalidStateTransition would otherwise report as a failure to a
		// handler that did nothing wrong.
		return p, nil
	}
	creditor, err := s.participantTx(ctx, tx, by)
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
	// The Agent on EITHER side is ignored by SubmitPaymentTx, which derives both
	// from the roster: see PartyDetails.Agent for why routing is never the
	// caller's to assert. The field is on the struct rather than removed from it
	// because this same type is what CreditTransferRequest and DirectDebitRequest
	// produce from a RECEIVED message, where the agent is the sender's assertion
	// and is genuinely carried. api's initiatePaymentRequest, which only ever
	// feeds the submitting path, has no agent field at all.
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
	// NextID is an INSERT … ON CONFLICT DO UPDATE on one row of id_sequences
	// (store/pg/tx_ledger.go), so it takes a row lock that is held until this
	// transaction ends. A second submission blocks there, and by the time it
	// gets past, the first has either committed — so the read below sees its
	// payment, under READ COMMITTED, and refuses the reference — or rolled back,
	// taking the id with it. The gap-free counter serializes the whole operation
	// and not merely the number it hands out, which is the argument
	// store/pg/pg_test.go already makes for AddParticipantTx.
	//
	// With the two the other way round, eight concurrent submissions of one
	// EndToEndID were accepted eight times on store/pg and once on store/mem —
	// which serializes every Update on one process-wide mutex and so could never
	// show it. The payer was debited eight times for one client reference. See
	// storetest's ConcurrentReadThenWriteOnOneKeyAgrees — a subtest of
	// TestConformance, not a test function of its own — which holds the two
	// stores to the same answer for this shape.
	id, err := tx.NextID(ctx, ledger.NetworkBook, "pay")
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
	counterpartyRef := p.Creditor
	if !push {
		counterparty = &p.DebtorDetails
		counterpartyRef = p.Debtor
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

	// The AGENT is DERIVED, and is the one thing on a payment that a payer is
	// never allowed to assert.
	//
	// It used to be taken from the instruction and checked for BIC FORMAT only,
	// which made it a routing decision handed to whoever filled in the form:
	// this agent goes on the wire as CdtrAgt/DbtrAgt (translate.go's partiesOf)
	// and the clearing house routes on exactly that element with no store read
	// of its own (mesh/csm.go's relayCreditTransfer and relayDirectDebit). A
	// push whose CreditorDetails.Agent named the payer's own bank came back to
	// its sender, which then answered its own instruction; a pull whose
	// DebtorDetails.Agent named the collector saw the COLLECTING bank post the
	// debit in the payer's bank's book. Both were measured — see
	// mesh/books_test.go's TestAWrongCounterpartyAgentDoesNotMisroute, which is
	// the pin.
	//
	// This is what a real SEPA originating bank does. SEPA has been IBAN-only
	// since February 2016: the payer supplies an IBAN and a name, and the
	// originating bank derives the routing itself rather than trusting a BIC
	// somebody typed. The payment already names which participant the
	// counterparty is at, and the roster is the authority on that participant's
	// BIC — "routing needs the bank, not the name".
	//
	// Reading the roster is NOT a read of the counterparty's book. Participants
	// are network-scoped rows: tx.GetParticipant takes no BookID and is
	// deliberately not one of the recorder's overrides in mesh/books_test.go, so
	// the submitting bank's measured set is unchanged by this call. The same
	// test asserts that, on both directions.
	//
	// A participant nobody has admitted is ErrParticipantNotFound, unwrapped
	// from the store's own sentinel rather than manufactured here — and every
	// other error is passed through as it arrived, for the reason checkPartyTx
	// and addressedPartyTx both set out at length: a dropped connection is not a
	// statement about the instruction, and RC01 "bank identifier incorrect" on
	// the wire would be a false one.
	counterpartyBank, err := tx.GetParticipant(ctx, counterpartyRef.Participant)
	if err != nil {
		return Payment{}, err
	}
	counterparty.Agent = counterpartyBank.BIC

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
		// The account and participant returned here are the RECEIVING bank's
		// own — the creditor's, for a push — and are deliberately discarded:
		// unlike SubmitPaymentTx, this half must not use them to overwrite
		// CreditorDetails. That field already holds what the payer asserted,
		// and the pacs.008 already sent carries exactly that name; rewriting it
		// here would desynchronise the stored payment from the message that
		// already went out.
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
//
// It also returns the account and the bound participant it checked, not for
// this function's own use but for the caller's: debtorSideTx runs from BOTH
// SubmitPaymentTx (a push, where the debtor is the SUBMITTING bank) and
// AcceptInboundTx (a pull, where the debtor is the RECEIVING bank), and only
// the submitting call may use what it returns to fill DebtorDetails from the
// register — see the comment at the call site in SubmitPaymentTx for why that
// must not happen here.
func (s *Network) debtorSideTx(ctx context.Context, tx Tx, scheme Scheme, p *Payment, sc SchemeContext) (deposit.Account, *Participant, error) {
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
func (s *Network) creditorSideTx(ctx context.Context, tx Tx, scheme Scheme, p *Payment, sc SchemeContext) (deposit.Account, *Participant, error) {
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
//
// # A KNOWN GAP: the refund is not checked against a creditable account
//
// creditorSideTx and DepositTx both call Deposit.CheckCreditTx before money
// lands in a customer's account, and both give the same reason: Close requires
// a zero balance, no withdrawal can reach a closed account afterwards, and
// Closed is terminal, so a credit into one strands for ever. This function
// credits debtorGL below with no such check.
//
// It is reachable and it was measured rather than reasoned about. A payer whose
// account is emptied and closed after a payment settles, whose payment is then
// returned, ends with 250,000 in an account whose status is Closed: the
// withdrawal check answers "account is closed", the credit check answers
// "account is closed", and closing again is an invalid status transition.
//
// It is NOT fixed by REFUSING, which was the only option this note used to
// weigh: refusing the return answers RJCT to the returning bank and leaves the
// disputed money with the payee permanently, since Settled is the only status
// ReturnPaymentTx accepts and there is no second attempt that would ever differ.
// One stranding is traded for another.
//
// # The settlement half of this gap is now closed, and this one is not
//
// This note used to cover SettleCycleTx too, which credited the payee with no
// check for the same reason. Both halves wanted the same thing — somewhere for
// unreachable money to go, an unclaimed-balances account at the receiving bank —
// which the system did not have. It has one now
// (ParticipantAccounts.Unclaimed), and PostCreditorLegTx uses it: a payee whose
// account is closed at the cut-off is credited to their bank's unclaimed
// balances and the batch settles. That is what
// TestASettlementIntoAClosedAccountGoesToUnclaimedBalances pins.
//
// The same move is available here — the payer's bank has an unclaimed-balances
// account too, and the refund could go to it — and this function does not make
// it. That is a gap in the RETURN path and no longer a missing account.
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
// account exists within that participant, returning both the account and the
// bound participant so callers that need more than existence (the account's
// Asset, GLAccount, ... or the participant's live Deposit/Ledger handles)
// don't have to fetch either again. Binding costs nothing beyond the fetch
// this function already makes — s.bind wraps the row it just read with live
// handles built from the Network's own stores, not a second round trip — so
// returning a bound participant here is free, and a caller re-fetching the
// same row with participantTx (as debtorSideTx used to) is not.
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
func (s *Network) checkPartyTx(ctx context.Context, tx Tx, field string, ref PartyRef) (deposit.Account, *Participant, error) {
	if err := validateParty(field, ref); err != nil {
		return deposit.Account{}, nil, err
	}
	rec, err := tx.GetParticipant(ctx, ref.Participant)
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
