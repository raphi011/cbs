package deposit

import "sort"

// ---------------------------------------------------------------------------
// Enumeration
//
// These read-only methods enumerate deposit-layer entities for callers that
// need to browse the register (for example a UI). They take a read lock and
// return value copies. To keep enumeration simple they are total: an unknown
// account yields an empty slice rather than an error. Callers that want a 404
// can pre-validate with GetAccount. Results are sorted by CreatedAt then ID so
// the unordered maps produce a stable order across calls.
// ---------------------------------------------------------------------------

// ListAccounts returns all deposit accounts, ordered by creation time then ID.
func (r *Register) ListAccounts() []Account {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Account, 0, len(r.accounts))
	for _, a := range r.accounts {
		result = append(result, *a)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

// ListHolds returns all holds for the given account, ordered by creation time
// then ID. An unknown account yields an empty slice.
func (r *Register) ListHolds(accountID AccountID) []Hold {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := r.accountHolds[accountID]
	result := make([]Hold, 0, len(ids))
	for _, id := range ids {
		if h, ok := r.holds[id]; ok {
			result = append(result, *h)
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

// ListSnapshots returns all end-of-day snapshots for the given account, ordered
// by business date. An unknown account yields an empty slice.
func (r *Register) ListSnapshots(accountID AccountID) []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	byDate := r.snapshots[accountID]
	result := make([]Snapshot, 0, len(byDate))
	for _, snap := range byDate {
		result = append(result, *snap)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date.Before(result[j].Date)
	})
	return result
}
