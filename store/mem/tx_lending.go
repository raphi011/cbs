package mem

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// The lending half of tx. It is the same type that implements ledger.Tx, which
// is the point: a disbursement's facility write and its GL posting commit or
// roll back together.

// compile-time check that tx satisfies the lending interface too.
var _ lending.Tx = (*tx)(nil)

// ---------------------------------------------------------------------------
// Facilities
// ---------------------------------------------------------------------------

func (t *tx) PutFacility(ctx context.Context, book ledger.BookID, f lending.Facility) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindFacility, string(f.ID))
	bucket(t.state.facilities, book)[f.ID] = f
	return nil
}

func (t *tx) GetFacility(ctx context.Context, book ledger.BookID, id lending.FacilityID) (lending.Facility, error) {
	f, ok := t.state.facilities[book][id]
	if !ok {
		return lending.Facility{}, lending.ErrFacilityNotFound
	}
	return f, nil
}

func (t *tx) ListFacilities(ctx context.Context, book ledger.BookID) ([]lending.Facility, error) {
	out := make([]lending.Facility, 0, len(t.state.facilities[book]))
	for _, f := range t.state.facilities[book] {
		out = append(out, f)
	}
	sortRows(t.state, out, book, kindFacility, func(f lending.Facility) (time.Time, string) {
		return f.OpenedAt, string(f.ID)
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// Instalments
// ---------------------------------------------------------------------------

// PutInstallment upserts under (facility, seq): recording a payment against an
// instalment replaces it rather than appending a second row.
func (t *tx) PutInstallment(ctx context.Context, book ledger.BookID, i lending.Installment) error {
	if err := t.write(); err != nil {
		return err
	}
	key := installmentKey{facility: i.FacilityID, seq: i.Seq}
	t.state.insertSeq(book, kindInstallment, installmentSeqID(key))
	bucket(t.state.installments, book)[key] = i
	return nil
}

// ListInstallments returns a facility's schedule ordered by Seq — the one
// listing in this system not ordered by a timestamp, because Seq is the
// instalment's position in the contract and is already a total order within a
// facility. It is a number, not a counter-derived string, so it sorts.
func (t *tx) ListInstallments(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.Installment, error) {
	out := make([]lending.Installment, 0)
	for key, i := range t.state.installments[book] {
		if key.facility == id {
			out = append(out, i)
		}
	}
	slices.SortFunc(out, func(a, b lending.Installment) int { return a.Seq - b.Seq })
	return out, nil
}

// installmentSeqID renders an instalment's composite key as the string
// insertSeq identifies rows by.
func installmentSeqID(k installmentKey) string {
	return string(k.facility) + "/" + strconv.Itoa(k.seq)
}
