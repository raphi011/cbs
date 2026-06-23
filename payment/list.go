package payment

import "sort"

// ---------------------------------------------------------------------------
// Enumeration and lookup
//
// These read-only methods let callers browse the network (for example a UI),
// since most entities are otherwise only reachable by ID. They take a read
// lock and return value copies — except ListParticipants/GetParticipant, which
// return the live *Participant so callers can reach the bank's own Ledger and
// Deposit register (consistent with AddParticipant). Results are sorted for a
// stable order across calls.
// ---------------------------------------------------------------------------

// ListParticipants returns all participant banks, ordered by ID.
//
// The returned pointers are the network's live Participant values: callers may
// read their fields and use Ledger/Deposit, but must not mutate the struct
// fields directly. The Ledger and Deposit registers are themselves
// mutex-guarded and safe for concurrent use.
func (s *Network) ListParticipants() []*Participant {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Participant, 0, len(s.participants))
	for _, p := range s.participants {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

// GetParticipant returns the participant bank with the given ID.
//
// Like ListParticipants it returns the live *Participant so the caller can
// reach the bank's Ledger and Deposit register; do not mutate the struct
// fields directly. Returns ErrParticipantNotFound if no such participant
// exists.
func (s *Network) GetParticipant(id ParticipantID) (*Participant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.participants[id]
	if !ok {
		return nil, ErrParticipantNotFound
	}
	return p, nil
}

// ListPayments returns all payments, ordered by creation time then ID.
func (s *Network) ListPayments() []Payment {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Payment, 0, len(s.payments))
	for _, p := range s.payments {
		result = append(result, copyPayment(p))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

// ListMandates returns all direct-debit mandates, ordered by creation time
// then ID.
func (s *Network) ListMandates() []Mandate {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Mandate, 0, len(s.mandates))
	for _, m := range s.mandates {
		result = append(result, *m)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

// ListCycles returns all clearing cycles, ordered by open time then ID.
func (s *Network) ListCycles() []ClearingCycle {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ClearingCycle, 0, len(s.cycles))
	for _, c := range s.cycles {
		result = append(result, copyCycle(c))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OpenedAt.Equal(result[j].OpenedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].OpenedAt.Before(result[j].OpenedAt)
	})
	return result
}

// ListSettlements returns all settlements, ordered by settle time then ID.
func (s *Network) ListSettlements() []Settlement {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Settlement, 0, len(s.settlements))
	for _, st := range s.settlements {
		result = append(result, copySettlement(st))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SettledAt.Equal(result[j].SettledAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].SettledAt.Before(result[j].SettledAt)
	})
	return result
}

// GetSettlement returns the settlement with the given ID, or
// ErrSettlementNotFound if it does not exist.
func (s *Network) GetSettlement(id SettlementID) (Settlement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.settlements[id]
	if !ok {
		return Settlement{}, ErrSettlementNotFound
	}
	return copySettlement(st), nil
}

// ListSchemes returns all registered payment schemes, ordered by scheme ID.
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

func copySettlement(st *Settlement) Settlement {
	cp := *st
	cp.NetPositions = copyPositions(st.NetPositions)
	return cp
}
