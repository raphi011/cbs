package mem

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

// The deposit half of tx. It is the same type that implements ledger.Tx, which
// is the whole point: one transaction spans both layers, so a hold write and
// the GL posting that captures it commit or roll back together.
//
// The method names carry a Deposit prefix where ledger.Tx already claimed the
// bare one (PutAccount, GetAccount, ListAccounts); see deposit.Tx for why.

// compile-time check that tx satisfies the deposit interface too.
var _ deposit.Tx = (*tx)(nil)

// ---------------------------------------------------------------------------
// Deposit accounts
// ---------------------------------------------------------------------------

func (t *tx) PutDepositAccount(ctx context.Context, book ledger.BookID, a deposit.Account) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindDepositAccount, string(a.ID))
	bucket(t.state.depositAccounts, book)[a.ID] = copyDepositAccount(a)
	return nil
}

// copyDepositAccount returns a with its identifier set detached from the
// caller's and normalised to what store/pg would have stored.
//
// DETACHED, because Identifiers is the deposit account's only slice field and
// this store hands Go values straight to its callers. Without the copy, a
// mutation inside a View would change stored state, and a mutation inside an
// Update that then returned an error would survive the rollback — neither of
// which store/pg can do at all, which is exactly the divergence
// store/storetest exists to prevent, and which is what clone()'s one-level-deep
// snapshot relies on not happening. It is applied on the way IN and on the way
// OUT, like detachPricing and copyPayment, because those are two different
// aliases into the same row.
//
// NORMALISED, because store/pg normalises whether it means to or not, and "the
// same write reads back differently on the two stores" is the same class of
// divergence as a constraint one of them holds:
//
//   - deposit_account_identifiers has (book, account, scheme, value) as its
//     primary key and is inserted ON CONFLICT DO NOTHING, so writing the same
//     pair twice on one account collapses to one row there. A Go slice keeps
//     both. Sorting first makes slices.Compact enough to do the same here.
//   - It is read back ordered by (scheme, value), so the set comes out sorted
//     whatever order it went in, where a Go slice preserves insertion order.
//     strings.Compare is byte order, which is why hydrateIdentifiers spells its
//     ORDER BY with COLLATE "C" rather than leaving it to the cluster's locale.
//   - An account with no identifiers has no rows, so pg's hydrate leaves the
//     field nil. A caller that passed a non-nil empty slice — which
//     api/handlers_deposit.go does, since make([]T, 0) is what it builds from an
//     absent JSON field — would otherwise read back non-nil here and nil there.
//
// The domain layer refuses a duplicate before it ever reaches the store
// (Register.OpenAccountTx and checkIdentifierFreeTx); this is the store holding
// the same line for a caller that goes around it, which storetest does on
// purpose.
func copyDepositAccount(a deposit.Account) deposit.Account {
	if len(a.Identifiers) == 0 {
		a.Identifiers = nil
		return a
	}
	idents := slices.Clone(a.Identifiers)
	slices.SortFunc(idents, func(x, y deposit.Identifier) int {
		if c := strings.Compare(string(x.Scheme), string(y.Scheme)); c != 0 {
			return c
		}
		return strings.Compare(x.Value, y.Value)
	})
	a.Identifiers = slices.Compact(idents)
	return a
}

func (t *tx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	a, ok := t.state.depositAccounts[book][id]
	if !ok {
		return deposit.Account{}, deposit.ErrAccountNotFound
	}
	return copyDepositAccount(a), nil
}

func (t *tx) ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]deposit.Account, error) {
	out := make([]deposit.Account, 0, len(t.state.depositAccounts[book]))
	for _, a := range t.state.depositAccounts[book] {
		out = append(out, copyDepositAccount(a))
	}
	sortRows(t.state, out, book, kindDepositAccount, func(a deposit.Account) (time.Time, string) { return a.CreatedAt, string(a.ID) })
	return out, nil
}

// The whole Account — identifiers included — is the map value, so
// PutDepositAccount and both readers need no change at all. Only the lookup is
// new, and in a map it is a scan; there are four banks.
func (t *tx) ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident deposit.Identifier) ([]deposit.Account, error) {
	out := make([]deposit.Account, 0)
	for _, a := range t.state.depositAccounts[book] {
		for _, got := range a.Identifiers {
			if got == ident {
				out = append(out, copyDepositAccount(a))
				break
			}
		}
	}
	sortRows(t.state, out, book, kindDepositAccount, func(a deposit.Account) (time.Time, string) { return a.CreatedAt, string(a.ID) })
	return out, nil
}

// ---------------------------------------------------------------------------
// Holds
// ---------------------------------------------------------------------------

func (t *tx) PutHold(ctx context.Context, book ledger.BookID, h deposit.Hold) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindHold, string(h.ID))
	bucket(t.state.holds, book)[h.ID] = h
	return nil
}

func (t *tx) GetHold(ctx context.Context, book ledger.BookID, id deposit.HoldID) (deposit.Hold, error) {
	h, ok := t.state.holds[book][id]
	if !ok {
		return deposit.Hold{}, deposit.ErrHoldNotFound
	}
	return h, nil
}

// ListHoldsForAccount scans the book's holds rather than keeping an
// account-to-holds index. The index would be a second thing to keep in sync
// inside the store, and store/pg has none either — it filters on account_id, the
// same way ListTransactionsForAccount already works here.
func (t *tx) ListHoldsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Hold, error) {
	out := make([]deposit.Hold, 0)
	for _, h := range t.state.holds[book] {
		if h.AccountID == id {
			out = append(out, h)
		}
	}
	sortRows(t.state, out, book, kindHold, func(h deposit.Hold) (time.Time, string) { return h.CreatedAt, string(h.ID) })
	return out, nil
}

// ActiveHoldTotal sums the holds that currently reduce an account's available
// balance: those still Active and not past their expiry.
//
// A zero ExpiresAt means the hold never expires. Like BookBalance this is an
// aggregate, so an account with no holds — including one that does not exist —
// totals zero rather than erroring.
func (t *tx) ActiveHoldTotal(ctx context.Context, book ledger.BookID, id deposit.AccountID, now time.Time) (ledger.Amount, error) {
	var total ledger.Amount
	for _, h := range t.state.holds[book] {
		if h.AccountID != id || h.Status != deposit.HoldActive {
			continue
		}
		if !h.ExpiresAt.IsZero() && h.ExpiresAt.Before(now) {
			continue
		}
		total += h.Amount
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// End-of-day snapshots
// ---------------------------------------------------------------------------

// PutSnapshot upserts a snapshot under (account, business date): taking a second
// snapshot for the same date replaces the first.
func (t *tx) PutSnapshot(ctx context.Context, book ledger.BookID, s deposit.Snapshot) error {
	if err := t.write(); err != nil {
		return err
	}
	key := snapshotKey{account: s.AccountID, dateKey: deposit.SnapshotDateKey(s.Date)}
	t.state.insertSeq(book, kindSnapshot, snapshotSeqID(key))
	bucket(t.state.snapshots, book)[key] = s
	return nil
}

func (t *tx) GetSnapshot(ctx context.Context, book ledger.BookID, id deposit.AccountID, dateKey string) (deposit.Snapshot, error) {
	s, ok := t.state.snapshots[book][snapshotKey{account: id, dateKey: dateKey}]
	if !ok {
		return deposit.Snapshot{}, deposit.ErrSnapshotNotFound
	}
	return s, nil
}

func (t *tx) ListSnapshotsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Snapshot, error) {
	out := make([]deposit.Snapshot, 0)
	for key, s := range t.state.snapshots[book] {
		if key.account == id {
			out = append(out, s)
		}
	}
	// Snapshots are identified by their business date, so within an account the
	// date is already a total order; the insertion sequence is the tie-break only
	// for form, and to keep every listing on one rule.
	sortRows(t.state, out, book, kindSnapshot, func(s deposit.Snapshot) (time.Time, string) {
		return s.Date, snapshotSeqID(snapshotKey{account: s.AccountID, dateKey: deposit.SnapshotDateKey(s.Date)})
	})
	return out, nil
}

// snapshotSeqID renders a snapshot's composite primary key as the string
// sortRows and insertSeq identify rows by.
func snapshotSeqID(k snapshotKey) string { return string(k.account) + "/" + k.dateKey }

// ---------------------------------------------------------------------------
// Effective-dated overdraft terms
// ---------------------------------------------------------------------------

// PutOverdraftTerms upserts under (account, effective day): a second row for
// the same day replaces the first, which is what makes "the terms in force on
// day D" unique by construction rather than by a validation rule.
func (t *tx) PutOverdraftTerms(ctx context.Context, book ledger.BookID, row deposit.OverdraftTerms) error {
	if err := t.write(); err != nil {
		return err
	}
	key := termsKey{account: row.AccountID, dayKey: deposit.TermsDayKey(row.EffectiveFrom)}
	t.state.insertSeq(book, kindOverdraftTerms, termsSeqID(key))
	bucket(t.state.overdraftTerms, book)[key] = detachPricing(row)
	return nil
}

// detachPricing returns row with its pricing overlay pointing at a fresh copy.
//
// The overlay is the deposit layer's only pointer field, and this store hands
// Go values straight to its callers. Without the copy a writer that kept its
// argument could rewrite a stored price afterwards, and a reader could mutate
// stored history through the pointer it was handed — neither of which store/pg
// can do at all, which is exactly the divergence store/storetest exists to
// prevent. It is applied on the way IN and on the way OUT, because those are
// two different aliases into the same row.
func detachPricing(row deposit.OverdraftTerms) deposit.OverdraftTerms {
	if row.Pricing != nil {
		pricing := *row.Pricing
		row.Pricing = &pricing
	}
	return row
}

// ListOverdraftTermsForAccount returns an account's whole timeline, ascending
// by effective day. Ascending is load-bearing: deposit.termsAt binary-searches
// the slice this returns.
func (t *tx) ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.OverdraftTerms, error) {
	out := make([]deposit.OverdraftTerms, 0)
	for key, row := range t.state.overdraftTerms[book] {
		if key.account == id {
			out = append(out, detachPricing(row))
		}
	}
	// The effective day is already a total order within an account, so the
	// insertion sequence is the tie-break only for form, and to keep every
	// listing on one rule.
	sortRows(t.state, out, book, kindOverdraftTerms, func(row deposit.OverdraftTerms) (time.Time, string) {
		return row.EffectiveFrom, termsSeqID(termsKey{
			account: row.AccountID, dayKey: deposit.TermsDayKey(row.EffectiveFrom),
		})
	})
	return out, nil
}

// GetOverdraftTermsAsOf is the row in force on a day: the greatest effective
// day not after it.
//
// It scans rather than binary-searching, because the map is unordered and
// building the ordered slice to search would cost more than the scan. Unlike
// ActiveHoldTotal this is not an aggregate: an account with no rows before the
// day has no terms, and reporting a zero row would read as a real
// interest-free product rather than as an absence.
func (t *tx) GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id deposit.AccountID, day time.Time) (deposit.OverdraftTerms, error) {
	want := deposit.TermsDayKey(day)
	var (
		best  deposit.OverdraftTerms
		found bool
	)
	for key, row := range t.state.overdraftTerms[book] {
		if key.account != id || key.dayKey > want {
			continue
		}
		if !found || key.dayKey > deposit.TermsDayKey(best.EffectiveFrom) {
			best, found = row, true
		}
	}
	if !found {
		return deposit.OverdraftTerms{}, deposit.ErrTermsNotFound
	}
	return detachPricing(best), nil
}

// termsSeqID renders a terms row's composite key as the string sortRows and
// insertSeq identify rows by.
func termsSeqID(k termsKey) string { return string(k.account) + "/" + k.dayKey }
