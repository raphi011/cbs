package ledger

import "sort"

// ---------------------------------------------------------------------------
// Enumeration
//
// These read-only methods enumerate entities for callers that need to browse
// the ledger (for example a UI), since the maps are otherwise only reachable
// by ID. Like the other read methods they take a read lock and return value
// copies, never the internal pointers. Results are sorted by CreatedAt then ID
// so the unordered maps produce a stable order across calls.
// ---------------------------------------------------------------------------

// ListLedgers returns all ledgers, ordered by creation time then ID.
func (s *Book) ListLedgers() []Ledger {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Ledger, 0, len(s.ledgers))
	for _, l := range s.ledgers {
		result = append(result, *l)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

// ListSubledgers returns the subledgers belonging to the given ledger, ordered
// by creation time then ID. An unknown ledger yields an empty slice.
func (s *Book) ListSubledgers(ledgerID LedgerID) []Subledger {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Subledger, 0)
	for _, sl := range s.subledgers {
		if sl.LedgerID == ledgerID {
			result = append(result, *sl)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

// ListAccounts returns the accounts belonging to the given subledger, ordered
// by creation time then ID. An unknown subledger yields an empty slice.
func (s *Book) ListAccounts(subledgerID SubledgerID) []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Account, 0)
	for _, a := range s.accounts {
		if a.SubledgerID == subledgerID {
			result = append(result, *a)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

// ListTransactions returns all transactions, ordered by creation time then ID.
// Each transaction is deep-copied via copyTransaction, consistent with
// GetTransaction.
func (s *Book) ListTransactions() []Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Transaction, 0, len(s.transactions))
	for _, tx := range s.transactions {
		result = append(result, copyTransaction(tx))
	}
	sortTransactions(result)
	return result
}

// ListTransactionsForAccount returns all transactions that have at least one
// entry referencing the given account, ordered by creation time then ID.
func (s *Book) ListTransactionsForAccount(accountID AccountID) []Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Transaction, 0)
	for _, tx := range s.transactions {
		for _, e := range tx.Entries {
			if e.AccountID == accountID {
				result = append(result, copyTransaction(tx))
				break
			}
		}
	}
	sortTransactions(result)
	return result
}

func sortTransactions(txs []Transaction) {
	sort.Slice(txs, func(i, j int) bool {
		if txs[i].CreatedAt.Equal(txs[j].CreatedAt) {
			return txs[i].ID < txs[j].ID
		}
		return txs[i].CreatedAt.Before(txs[j].CreatedAt)
	})
}
