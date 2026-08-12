package ledger

import "context"

// ---------------------------------------------------------------------------
// Enumeration

// ListLedgers returns all ledgers, ordered by creation time then insertion
// order.
func (s *Book) ListLedgers(ctx context.Context) ([]Ledger, error) {
	var out []Ledger
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListLedgers(ctx, s.id)
		return err
	})
	return out, err
}

// ListSubledgers returns the subledgers belonging to the given ledger, ordered
// by creation time then insertion order. An unknown ledger yields an empty slice.
func (s *Book) ListSubledgers(ctx context.Context, ledgerID LedgerID) ([]Subledger, error) {
	out := make([]Subledger, 0)
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		all, err := tx.ListSubledgers(ctx, s.id)
		if err != nil {
			return err
		}
		for _, sl := range all {
			if sl.LedgerID == ledgerID {
				out = append(out, sl)
			}
		}
		return nil
	})
	return out, err
}

// ListAccounts returns the accounts belonging to the given subledger, ordered
// by creation time then insertion order. An unknown subledger yields an empty slice.
func (s *Book) ListAccounts(ctx context.Context, subledgerID SubledgerID) ([]Account, error) {
	out := make([]Account, 0)
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		all, err := tx.ListAccounts(ctx, s.id)
		if err != nil {
			return err
		}
		for _, a := range all {
			if a.SubledgerID == subledgerID {
				out = append(out, a)
			}
		}
		return nil
	})
	return out, err
}

// ListTransactions returns all transactions, ordered by creation time then
// insertion order.
func (s *Book) ListTransactions(ctx context.Context) ([]Transaction, error) {
	var out []Transaction
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListTransactions(ctx, s.id)
		return err
	})
	return out, err
}

// ListTransactionsForPosition returns all transactions that have at least one
// entry referencing the given position, ordered by creation time then insertion
// order.
func (s *Book) ListTransactionsForPosition(ctx context.Context, pos Position) ([]Transaction, error) {
	var out []Transaction
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListTransactionsForPosition(ctx, s.id, pos)
		return err
	})
	return out, err
}
