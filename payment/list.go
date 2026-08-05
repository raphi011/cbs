package payment

import (
	"context"
	"sort"
)

// ---------------------------------------------------------------------------
// Enumeration and lookup
//
// These read-only methods let callers browse the network (for example a UI),
// since most entities are otherwise only reachable by ID. Each runs in one
// read-only unit of work and returns snapshots the store has already copied.
//
// Ordering is the store's, not this package's: creation instant ascending,
// ties broken by the row's insertion sequence. It is deliberately never the ID
// — IDs here are unpadded counters, so "pay_10" sorts before "pay_8". See the
// contract notes in store.go.
// ---------------------------------------------------------------------------

// ListBanks returns all member banks in registration order.
//
// The returned Banks carry live Ledger and Deposit handles bound to the
// network's store, so a caller can go straight from a listing to a bank's books.
// Their data fields are a snapshot; mutating them changes nothing.
func (s *Network) ListBanks(ctx context.Context) ([]*Bank, error) {
	var out []*Bank
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		records, err := tx.ListBanks(ctx)
		if err != nil {
			return err
		}
		out = make([]*Bank, len(records))
		for i, rec := range records {
			out[i] = s.bind(rec)
		}
		return nil
	})
	return out, err
}

// GetBank returns the member bank with the given ID, with its Ledger and
// Deposit handles bound. Returns ErrParticipantNotFound if no such bank exists.
//
// What it hands back is a bank's own record, live handles and all, so a caller
// that is not that bank gets the ability to read and write its books. Two of
// the five callers in api read another bank's deposit register through the
// handle this binds, and they are two different institutions doing it: the
// directory's name lookup, served on a bank's own port, which is crossing 2
// (ResolveIdentifierTx), open and Task 18's; and the mandate listing's asset
// lookup, served on the clearing house's port alone, which reaches every debtor
// bank's books directly off the id on the mandate and is not on the spec's
// table at all. See Bank.Ledger, which sets both out.
//
// GetRosterEntry below is the answer to the narrower question — "who is this
// bank, so I can address it" — and it is what the mesh asks. It exists because
// that question was being answered with this method.
func (s *Network) GetBank(ctx context.Context, id ParticipantID) (*Bank, error) {
	var out *Bank
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.bankTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetRosterEntry returns what the clearing house holds about one member: its
// address, its name, and the assets it clears in. Nothing else.
//
// It is what a handler asking about SOMEBODY ELSE gets, and the whole of it. A
// bank deciding whether a rejection is about a payment whose payer banks here
// needs a BIC; a clearing house addressing a status back to the bank that
// submitted a payment needs a BIC. Neither needs a ledger handle, and until
// this existed both got one — mesh/ops.go carried GetParticipant on two of its
// three interfaces and every caller of it took the BIC off a value carrying the
// named bank's live Ledger, Deposit and Catalogue.
//
// # It is keyed by the network's id and the store's method is keyed by the BIC
//
// The two are not the same lookup and the difference is a crossing this task
// does NOT close. A payment names its parties by ParticipantID, so a caller
// holding one has to turn it into a BIC before the roster can be asked — and
// the only thing that knows the mapping is the bank's own row. So this reads a
// bank's row to learn its address and then reads the roster.
//
// Under one store that is a read like any other. Under Task 18's stores it is a
// clearing house reading a bank's database, and it does not survive: what
// closes it is a payment that carries BICs rather than ids, which is that
// task's to do because it is the task where a payment row stops being one row
// the whole network shares. It is written down here rather than laundered
// behind a method name, and each of the seven call sites in mesh points back at
// this paragraph — the call site is where the next reader will be standing.
//
// # Two rows read, and one error it can return that GetParticipant could not
//
// A bank whose row exists and whose roster entry does not comes back
// ErrRosterEntryNotFound, where the method this replaced returned the bank. That
// is now a state the domain can produce rather than a stated impossibility: a
// bank founded by FoundBankTx and not yet admitted has a row and no entry, which
// is a founded bank waiting for an admission — legitimate, and the whole point
// of splitting founding from joining. No PRODUCTION caller reaches it: every
// path that founds a bank goes on to admit it in the same unit of work (see
// AddParticipantTx), and the tests that call FoundBankTx on its own are how the
// state is measured at all. Task 17d is where the two stop being one call.
func (s *Network) GetRosterEntry(ctx context.Context, id ParticipantID) (RosterEntry, error) {
	var out RosterEntry
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		bank, err := tx.GetBank(ctx, id)
		if err != nil {
			return err
		}
		out, err = tx.GetRosterEntry(ctx, bank.BIC)
		return err
	})
	if err != nil {
		return RosterEntry{}, err
	}
	return out, nil
}

// ListPayments returns all payments, oldest first.
func (s *Network) ListPayments(ctx context.Context) ([]Payment, error) {
	var out []Payment
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListPayments(ctx)
		return err
	})
	return out, err
}

// ListMandates returns all direct-debit mandates, oldest first.
func (s *Network) ListMandates(ctx context.Context) ([]Mandate, error) {
	var out []Mandate
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListMandates(ctx)
		return err
	})
	return out, err
}

// ListCycles returns all clearing cycles, oldest first by the time they opened.
func (s *Network) ListCycles(ctx context.Context) ([]ClearingCycle, error) {
	var out []ClearingCycle
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListCycles(ctx)
		return err
	})
	return out, err
}

// ListSettlements returns all settlements, oldest first by the time they
// settled.
func (s *Network) ListSettlements(ctx context.Context) ([]Settlement, error) {
	var out []Settlement
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListSettlements(ctx)
		return err
	})
	return out, err
}

// GetSettlement returns the settlement with the given ID, or
// ErrSettlementNotFound if it does not exist.
func (s *Network) GetSettlement(ctx context.Context, id SettlementID) (Settlement, error) {
	var out Settlement
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetSettlement(ctx, id)
		return err
	})
	return out, err
}

// ListSchemes returns all registered payment schemes, ordered by scheme ID.
//
// Schemes are code rather than data — they are registered at startup and live
// in memory — so this is the one listing that does not touch the store and the
// one that is still ordered by ID.
func (s *Network) ListSchemes() []Scheme {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Scheme, 0, len(s.schemes))
	for _, sc := range s.schemes {
		result = append(result, sc)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID() < result[j].ID()
	})
	return result
}
