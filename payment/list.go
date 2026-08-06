package payment

import (
	"context"
	"sort"

	"github.com/raphi011/cbs/iso20022"
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

// ListBanks returns every bank in the network, in registration order.
//
// Every bank, and not every member: founding is what writes the row, so a bank
// the scheme has not answered for is in this list carrying Status Founded.
// Callers depend on that rather than working around it — api's GET /members is
// how a console watches a bank become a member, and cmd/server builds its port
// table from this call at start-up, so a bank that was founded before the
// process started gets a listener whether or not the scheme ever answered for
// it. (A bank founded at RUNTIME gets none until the next restart, which is a
// fact about the static table rather than about this list.) GetRosterEntryByBIC
// below is the narrower question, answered from the table admission writes rather
// than from this one.
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

// GetBank returns the bank with the given ID — founded or admitted, for
// ListBanks's reason — with its Ledger and Deposit handles bound. Returns
// ErrParticipantNotFound if no such bank exists.
//
// A founded one is not an edge case here: Mesh.Admit's re-drive branch reads its
// bank through this method precisely because that bank is founded and the roster
// has no entry for it, so a lookup that answered only for members would break the
// one path out of an interrupted admission.
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
// GetRosterEntryByBIC below is the answer to the narrower question — "is this
// address a member, and what does the scheme hold about it" — and it is what the
// mesh asks. It exists because that question was being answered with this method.
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

// GetRosterEntryByBIC returns what the clearing house holds about the member at
// one ADDRESS: the assets that address clears in, the admission it was admitted
// under, and when. Nothing else — and in particular no NAME, which an acmt.010
// does not carry and this row therefore cannot hold; see RosterEntry.
//
// The admission reference is on that list rather than left off it as
// bookkeeping. It is what the clearing house's refusal of a second institution
// on a taken address is decided from, so a description of this row that stopped
// at "assets" would omit the field the refusal is made of.
//
// It is what a handler asking about SOMEBODY ELSE gets, and the whole of it.
// Nothing about it is a ledger handle, and until it existed the callers all got
// one — mesh/ops.go carried GetParticipant on two of its three interfaces and
// every caller of it took the BIC off a value carrying the named bank's live
// Ledger, Deposit and Catalogue.
//
// # There was a second method here, keyed by ParticipantID, and it was a crossing
//
// GetRosterEntry took a bank id, read that bank's own row to learn its address,
// and then read the roster. Under one store that is a read like any other; under
// Task 18's stores it is the clearing house reaching into a bank's database, and
// it had eight callers in the mesh, each pointing back at a paragraph here that
// said the crossing was Task 18's to close.
//
// What closed it is not a narrowing of the lookup. It is the ruling that a bank's
// ParticipantID IS its BIC (see AsBank): a caller that held an id already held an
// address, so the conversion had nothing left to convert. Seven of the eight
// callers stopped calling anything at all — they read the agent BIC that was
// already beside them on the payment, or the BIC a cycle's positions are now
// keyed by — and the eighth is Mesh.Submit checking that a counterparty it has
// been NAMED is a member, which is this method and was always this question.
//
// # One error it can return that GetParticipant could not
//
// A bank whose row exists and whose roster entry does not comes back
// ErrRosterEntryNotFound, where the method this replaced returned the bank. That
// is a state the domain can produce rather than a stated impossibility: a bank
// founded by FoundBankTx and not yet admitted has a row and no entry, which is a
// founded bank waiting for an admission — legitimate, and the whole point of
// splitting founding from joining. It is REACHED in production: mesh.Mesh.Admit
// founds a bank and the scheme answers later, so between the two commits this is
// what the clearing house's own row says about that address.
//
// On the admission relay it is the ORDINARY answer rather than a fault — an
// acmt.007 names its applicant by BIC and by nothing else, and a BIC in no roster
// is what an address nobody has been admitted on looks like.
func (s *Network) GetRosterEntryByBIC(ctx context.Context, bic iso20022.BIC) (RosterEntry, error) {
	var out RosterEntry
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetRosterEntry(ctx, bic)
		return err
	})
	if err != nil {
		return RosterEntry{}, err
	}
	return out, nil
}

// ListRosterEntries returns every member the clearing house routes to, oldest
// admission first.
//
// It is what says WHO IS A MEMBER, which is a different question from who has a
// bank row: a bank founded and not yet admitted has a row and no entry here.
// mesh.Mesh.joinRoster is the caller, and asking this rather than ListBanks is
// what stops a founded, unadmitted bank being given an actor at startup.
func (s *Network) ListRosterEntries(ctx context.Context) ([]RosterEntry, error) {
	var out []RosterEntry
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListRosterEntries(ctx)
		return err
	})
	return out, err
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

// ListMandates returns THIS bank's direct-debit mandates, oldest first: the
// ones whose creditor is its own customer.
//
// The filter is what makes moving GET /mandates onto a bank's port honest rather
// than a relocation. A mandate is the creditor's bank's row (see Mandate), so an
// unfiltered listing on a bank's console would be one member reading every other
// member's authorisations — which is the crossing this route was moved off the
// clearing house's port to avoid, arriving by a different door.
//
// It used to be a filter in Go — every mandate in the store, kept when its
// creditor was this participant — and Task 18b's version of this doc said the
// filter was temporary because "under Task 18d each bank's store holds its own
// mandates and there is nothing to filter". This is that: a mandate is the
// creditor's bank's row, it carries no creditor participant to filter ON (see
// Mandate.DebtorAgent, and the mandates statement in the bank schema, which
// argues the absent column), and the row set is this institution's by
// construction.
//
// It is a refusal on the other two institutions rather than an empty list,
// because a clearing house listing mandates is not an institution with none — it
// is an act that is not its. The `csm` shape has no mandates table at all.
//
// # By construction, and not yet
//
// The construction is the store split, and the store split is the wiring this
// task has not reached: until testenv and the composition root open one database
// per entity, every network still shares one, and this listing returns whatever
// mandates that shared store holds. That is a transitional gap and it is named
// here rather than papered over with a per-row probe of this bank's own register,
// which would be a workaround the next step deletes. What closes it is
// store/testenv opening N+2 stores, not another guard here.
func (s *Network) ListMandates(ctx context.Context) ([]Mandate, error) {
	if _, err := s.self(); err != nil {
		return nil, err
	}
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
	s.schemes.mu.RLock()
	defer s.schemes.mu.RUnlock()

	result := make([]Scheme, 0, len(s.schemes.m))
	for _, sc := range s.schemes.m {
		result = append(result, sc)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID() < result[j].ID()
	})
	return result
}
