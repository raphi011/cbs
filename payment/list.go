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

// ListParticipants returns all participant banks in registration order.
//
// The returned Participants carry live Ledger and Deposit handles bound to the
// network's store, so a caller can go straight from a listing to a bank's books.
// Their data fields are a snapshot; mutating them changes nothing.
func (s *Network) ListParticipants(ctx context.Context) ([]*Participant, error) {
	var out []*Participant
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		records, err := tx.ListParticipants(ctx)
		if err != nil {
			return err
		}
		out = make([]*Participant, len(records))
		for i, rec := range records {
			out[i] = s.bind(rec)
		}
		return nil
	})
	return out, err
}

// GetParticipant returns the participant bank with the given ID, with its
// Ledger and Deposit handles bound. Returns ErrParticipantNotFound if no such
// participant exists.
func (s *Network) GetParticipant(ctx context.Context, id ParticipantID) (*Participant, error) {
	var out *Participant
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.participantTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
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
