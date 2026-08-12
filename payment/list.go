package payment

import (
	"context"
	"sort"

	"github.com/raphi011/cbs/iso20022"
)

// ---------------------------------------------------------------------------
// Enumeration and lookup

// ListBanks returns every bank in THIS network's own database, in registration
// order — which, on a bank's own store, is that bank and nothing else.
func (s *BankNetwork) ListBanks(ctx context.Context) ([]*Bank, error) {
	var out []*Bank
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
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
func (s *BankNetwork) GetBank(ctx context.Context, id ParticipantID) (*Bank, error) {
	var out *Bank
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
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
// under, and when.
func (s *ClearingHouseNetwork) GetRosterEntryByBIC(ctx context.Context, bic iso20022.BIC) (RosterEntry, error) {
	var out RosterEntry
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
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
func (s *ClearingHouseNetwork) ListRosterEntries(ctx context.Context) ([]RosterEntry, error) {
	var out []RosterEntry
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		var err error
		out, err = tx.ListRosterEntries(ctx)
		return err
	})
	return out, err
}

// ListSettlementMembers returns every bank the CENTRAL BANK holds a settlement
// account for, oldest account first.
func (s *CentralBankNetwork) ListSettlementMembers(ctx context.Context) ([]SettlementMember, error) {
	var out []SettlementMember
	err := s.store.View(ctx, func(ctx context.Context, tx CentralBankTx) error {
		var err error
		out, err = tx.ListSettlementMembers(ctx)
		return err
	})
	return out, err
}

// GetSettlementMember returns what the central bank holds for one address: the
// name it opened the accounts under, one account per asset, and when.
func (s *CentralBankNetwork) GetSettlementMember(ctx context.Context, bic iso20022.BIC) (SettlementMember, error) {
	var out SettlementMember
	err := s.store.View(ctx, func(ctx context.Context, tx CentralBankTx) error {
		var err error
		out, err = tx.GetSettlementMember(ctx, bic)
		return err
	})
	return out, err
}

// ListPayments returns this institution's own copy of every payment it is a
// party to, oldest first.
func (s *BankNetwork) ListPayments(ctx context.Context) ([]Payment, error) {
	var out []Payment
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = tx.ListPayments(ctx)
		return err
	})
	return out, err
}

func (s *ClearingHouseNetwork) ListPayments(ctx context.Context) ([]Payment, error) {
	var out []Payment
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		var err error
		out, err = tx.ListPayments(ctx)
		return err
	})
	return out, err
}

// ListMandates returns THIS bank's direct-debit mandates, oldest first: the
// ones whose creditor is its own customer.
func (s *BankNetwork) ListMandates(ctx context.Context) ([]Mandate, error) {
	if _, err := s.self(); err != nil {
		return nil, err
	}
	var out []Mandate
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = tx.ListMandates(ctx)
		return err
	})
	return out, err
}

// ListCycles returns all clearing cycles, oldest first by the time they opened.
func (s *ClearingHouseNetwork) ListCycles(ctx context.Context) ([]ClearingCycle, error) {
	var out []ClearingCycle
	err := s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		var err error
		out, err = tx.ListCycles(ctx)
		return err
	})
	return out, err
}

// ListSettlements returns all settlements, oldest first by the time they
// settled.
func (s *CentralBankNetwork) ListSettlements(ctx context.Context) ([]Settlement, error) {
	var out []Settlement
	err := s.store.View(ctx, func(ctx context.Context, tx CentralBankTx) error {
		var err error
		out, err = tx.ListSettlements(ctx)
		return err
	})
	return out, err
}

// GetSettlement returns the settlement with the given ID, or
// ErrSettlementNotFound if it does not exist.
func (s *CentralBankNetwork) GetSettlement(ctx context.Context, id SettlementID) (Settlement, error) {
	var out Settlement
	err := s.store.View(ctx, func(ctx context.Context, tx CentralBankTx) error {
		var err error
		out, err = tx.GetSettlement(ctx, id)
		return err
	})
	return out, err
}

// GetSettlementByCycle returns the settlement this agent made for a cycle, or
// ErrSettlementNotFound if it made none.
func (s *CentralBankNetwork) GetSettlementByCycleID(ctx context.Context, id CycleID) (Settlement, error) {
	var out Settlement
	err := s.store.View(ctx, func(ctx context.Context, tx CentralBankTx) error {
		var err error
		out, err = tx.GetSettlementByCycle(ctx, id)
		return err
	})
	return out, err
}

// ListSchemes returns all registered payment schemes, ordered by scheme ID.
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
