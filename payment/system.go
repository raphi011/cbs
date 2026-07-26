package payment

import (
	"context"
	"errors"
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

// centralBankChartTx returns the central bank's reserve subledger and its
// balancing settlement-asset account, creating the chart of accounts if this is
// the first time the store has been used.
//
// It resolves by name on every call rather than caching IDs on the Network. A
// cached ID is wrong in three situations that all occur in this system: after
// Store.Reset, in a second process opened against the same database, and in a
// process that constructed the Network before the data existed.
func (s *Network) centralBankChartTx(ctx context.Context, tx Tx) (ledger.SubledgerID, ledger.AccountID, error) {
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

	accounts, err := tx.ListAccounts(ctx, CentralBankBook)
	if err != nil {
		return "", "", err
	}
	var assets ledger.Account
	for _, a := range accounts {
		if a.SubledgerID == capital.ID && a.Name == cbAssetsName {
			assets = a
			break
		}
	}
	if assets.ID == "" {
		if assets, err = s.centralBank.CreateAccountTx(ctx, tx, capital.ID, cbAssetsName, ledger.Asset); err != nil {
			return "", "", err
		}
	}
	return reserves.ID, assets.ID, nil
}

// ---------------------------------------------------------------------------
// Participants
// ---------------------------------------------------------------------------

// AddParticipant registers a new bank. It builds the bank's own book of
// accounts and chart of accounts and opens a reserve account for it at the
// central bank.
//
// The new bank starts with zero reserves; fund it with Deposit.
func (s *Network) AddParticipant(ctx context.Context, name string) (*Participant, error) {
	var out *Participant
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.AddParticipantTx(ctx, tx, name)
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
func (s *Network) AddParticipantTx(ctx context.Context, tx Tx, name string) (*Participant, error) {
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
	suspense, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Clearing Suspense", ledger.Liability)
	if err != nil {
		return nil, err
	}
	reserve, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Reserve at Central Bank", ledger.Asset)
	if err != nil {
		return nil, err
	}

	// Open the bank's reserve account in the central-bank ledger.
	reserveSubledger, _, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	cbReserve, err := s.centralBank.CreateAccountTx(ctx, tx, reserveSubledger, "Reserve: "+name, ledger.Liability)
	if err != nil {
		return nil, err
	}

	p := Participant{
		ID:                ParticipantID(id),
		Name:              name,
		BookID:            bookID,
		CustomerSubledger: customers.ID,
		SuspenseAccount:   suspense.ID,
		ReserveAccount:    reserve.ID,
		SettlementAccount: cbReserve.ID,
		CreatedAt:         s.now(),
	}
	if err := tx.PutParticipant(ctx, p); err != nil {
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
	p, err := s.participantTx(ctx, tx, participant)
	if err != nil {
		return err
	}
	gl, err := p.glAccountTx(ctx, tx, account)
	if err != nil {
		return err
	}

	if _, err := p.Ledger.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		Entries: []ledger.Entry{
			{AccountID: p.ReserveAccount, Amount: amount, Direction: ledger.Debit},
			{AccountID: gl, Amount: amount, Direction: ledger.Credit},
		},
	}); err != nil {
		return err
	}

	_, assets, err := s.centralBankChartTx(ctx, tx)
	if err != nil {
		return err
	}
	_, err = s.centralBank.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Reserve credit: " + p.Name,
		Entries: []ledger.Entry{
			{AccountID: assets, Amount: amount, Direction: ledger.Debit},
			{AccountID: p.SettlementAccount, Amount: amount, Direction: ledger.Credit},
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
	if err := s.checkPartyTx(ctx, tx, debtor); err != nil {
		return Mandate{}, err
	}
	if err := s.checkPartyTx(ctx, tx, creditor); err != nil {
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
	return tx.PutMandate(ctx, m)
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
	}

	c.NetPositions = net
	c.Status = CycleClosed
	c.ClosedAt = s.now()
	if err := tx.PutCycle(ctx, c); err != nil {
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

	// 1. Central-bank settlement transaction: move netted reserves between
	//    participants. The net positions sum to zero, so this balances.
	//
	//    The participants are read in registration order so that both this
	//    transaction's entries and the mirror postings below are deterministic.
	legs, err := s.settlementLegsTx(ctx, tx, c)
	if err != nil {
		return Settlement{}, err
	}

	cbEntries := make([]ledger.Entry, 0, len(legs))
	for _, leg := range legs {
		if leg.net > 0 {
			cbEntries = append(cbEntries, ledger.Entry{AccountID: leg.participant.SettlementAccount, Amount: leg.net, Direction: ledger.Credit})
		} else {
			cbEntries = append(cbEntries, ledger.Entry{AccountID: leg.participant.SettlementAccount, Amount: -leg.net, Direction: ledger.Debit})
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
			p, net := leg.participant, leg.net
			var entries []ledger.Entry
			if net > 0 { // net receiver: reserve up, suspense down
				entries = []ledger.Entry{
					{AccountID: p.ReserveAccount, Amount: net, Direction: ledger.Debit},
					{AccountID: p.SuspenseAccount, Amount: net, Direction: ledger.Credit},
				}
			} else { // net payer: reserve down, suspense up
				entries = []ledger.Entry{
					{AccountID: p.SuspenseAccount, Amount: -net, Direction: ledger.Debit},
					{AccountID: p.ReserveAccount, Amount: -net, Direction: ledger.Credit},
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
				{AccountID: creditor.SuspenseAccount, Amount: p.Amount, Direction: ledger.Debit},
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
	return st, nil
}

// settlementLeg pairs a participant with its non-zero net position in a cycle.
type settlementLeg struct {
	participant *Participant
	net         ledger.Amount
}

// settlementLegsTx resolves a cycle's net positions to participants in
// registration order.
//
// Registration order rather than map order because these legs decide the entry
// order of the settlement transaction, which is persisted. Iterating the
// NetPositions map directly would produce a different stored transaction on
// every run.
func (s *Network) settlementLegsTx(ctx context.Context, tx Tx, c ClearingCycle) ([]settlementLeg, error) {
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
		legs = append(legs, settlementLeg{participant: s.bind(rec), net: net})
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
	if err := s.checkPartyTx(ctx, tx, req.Debtor); err != nil {
		return Payment{}, err
	}
	if err := s.checkPartyTx(ctx, tx, req.Creditor); err != nil {
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

	// Debtor leg: money leaves the payer into the bank's clearing suspense.
	// The deposit layer is the authority for the funds/status check (run in
	// Validate above); the GL posting here references the deposit account's
	// backing GL account.
	debtor, err := s.participantTx(ctx, tx, p.Debtor.Participant)
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
			{AccountID: debtor.SuspenseAccount, Amount: p.Amount, Direction: ledger.Credit},
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
			{AccountID: debtor.ReserveAccount, Amount: p.Amount, Direction: ledger.Debit},
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
			{AccountID: creditor.ReserveAccount, Amount: p.Amount, Direction: ledger.Credit},
		},
	}); err != nil {
		return Payment{}, err
	}

	// Central bank reverses the reserve movement.
	if _, err := s.centralBank.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		IdempotencyKey: string(p.ID) + ":return-settle",
		Description:    "Return settlement for payment " + string(p.ID),
		Entries: []ledger.Entry{
			{AccountID: creditor.SettlementAccount, Amount: p.Amount, Direction: ledger.Debit},
			{AccountID: debtor.SettlementAccount, Amount: p.Amount, Direction: ledger.Credit},
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

// ReserveBalance returns a participant's reserve book balance as held at the
// central bank. Central-bank settlement accounts are plain GL accounts with no
// deposit layer, so this is just the GL book balance.
func (s *Network) ReserveBalance(ctx context.Context, id ParticipantID) (ledger.Amount, error) {
	var out ledger.Amount
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		p, err := tx.GetParticipant(ctx, id)
		if err != nil {
			return err
		}
		acct, err := tx.GetAccount(ctx, CentralBankBook, p.SettlementAccount)
		if err != nil {
			return err
		}
		out, err = tx.BookBalance(ctx, CentralBankBook, p.SettlementAccount, acct.Type.NormalBalance())
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

// checkPartyTx verifies that a party's participant exists and its deposit
// account exists within that participant.
func (s *Network) checkPartyTx(ctx context.Context, tx Tx, ref PartyRef) error {
	p, err := tx.GetParticipant(ctx, ref.Participant)
	if err != nil {
		return ErrParticipantNotFound
	}
	if _, err := tx.GetDepositAccount(ctx, p.BookID, ref.Account); err != nil {
		return ErrAccountNotInParticipant
	}
	return nil
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
