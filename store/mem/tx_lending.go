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

// ---------------------------------------------------------------------------
// Effective-dated facility terms
// ---------------------------------------------------------------------------

// PutFacilityTerms upserts under (facility, effective day): a second row for
// the same day replaces the first, which is what makes "the terms in force on
// day D" unique by construction rather than by a validation rule.
func (t *tx) PutFacilityTerms(ctx context.Context, book ledger.BookID, row lending.FacilityTerms) error {
	if err := t.write(); err != nil {
		return err
	}
	key := facilityTermsKey{facility: row.FacilityID, dayKey: lending.TermsDayKey(row.EffectiveFrom)}
	t.state.insertSeq(book, kindFacilityTerms, facilityTermsSeqID(key))
	bucket(t.state.facilityTerms, book)[key] = row
	return nil
}

// ListFacilityTerms returns a facility's whole timeline, ascending by
// effective day. Ascending is load-bearing: lending.termsAt binary-searches
// the slice this returns.
func (t *tx) ListFacilityTerms(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.FacilityTerms, error) {
	out := make([]lending.FacilityTerms, 0)
	for key, row := range t.state.facilityTerms[book] {
		if key.facility == id {
			out = append(out, row)
		}
	}
	// The effective day is already a total order within a facility, so the
	// insertion sequence is the tie-break only for form, and to keep every
	// listing on one rule.
	sortRows(t.state, out, book, kindFacilityTerms, func(row lending.FacilityTerms) (time.Time, string) {
		return row.EffectiveFrom, facilityTermsSeqID(facilityTermsKey{
			facility: row.FacilityID, dayKey: lending.TermsDayKey(row.EffectiveFrom),
		})
	})
	return out, nil
}

// GetFacilityTermsAsOf is the row in force on a day: the greatest effective
// day not after it.
//
// It scans rather than binary-searching, because the map is unordered and
// building the ordered slice to search would cost more than the scan. Unlike
// an aggregate, this is not one: a facility with no rows before the day has no
// terms, and reporting a zero row would read as a real interest-free product
// rather than as an absence.
func (t *tx) GetFacilityTermsAsOf(ctx context.Context, book ledger.BookID, id lending.FacilityID, day time.Time) (lending.FacilityTerms, error) {
	want := lending.TermsDayKey(day)
	var (
		best  lending.FacilityTerms
		found bool
	)
	for key, row := range t.state.facilityTerms[book] {
		if key.facility != id || key.dayKey > want {
			continue
		}
		if !found || key.dayKey > lending.TermsDayKey(best.EffectiveFrom) {
			best, found = row, true
		}
	}
	if !found {
		return lending.FacilityTerms{}, lending.ErrTermsNotFound
	}
	return best, nil
}

// facilityTermsSeqID renders a terms row's composite key as the string
// sortRows and insertSeq identify rows by.
func facilityTermsSeqID(k facilityTermsKey) string { return string(k.facility) + "/" + k.dayKey }
