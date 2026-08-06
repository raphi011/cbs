package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/interest"
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
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO facilities (
			book_id, id, kind, name, asset, principal_gl, interest_gl, refund_gl,
			commitment, method, term_months, min_payment,
			accrued_interest, accrued_gross,
			last_accrual_date, days_past_due, arrears_bucket,
			non_performing, oldest_unpaid_due, status, opened_at, maturity_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("facilities")+`)
		ON CONFLICT (book_id, id) DO UPDATE SET
			kind              = EXCLUDED.kind,
			name              = EXCLUDED.name,
			asset             = EXCLUDED.asset,
			principal_gl      = EXCLUDED.principal_gl,
			interest_gl       = EXCLUDED.interest_gl,
			refund_gl         = EXCLUDED.refund_gl,
			commitment        = EXCLUDED.commitment,
			method            = EXCLUDED.method,
			term_months       = EXCLUDED.term_months,
			min_payment       = EXCLUDED.min_payment,
			accrued_interest  = EXCLUDED.accrued_interest,
			accrued_gross     = EXCLUDED.accrued_gross,
			last_accrual_date = EXCLUDED.last_accrual_date,
			days_past_due     = EXCLUDED.days_past_due,
			arrears_bucket    = EXCLUDED.arrears_bucket,
			non_performing    = EXCLUDED.non_performing,
			oldest_unpaid_due = EXCLUDED.oldest_unpaid_due,
			status            = EXCLUDED.status,
			opened_at         = EXCLUDED.opened_at,
			maturity_at       = EXCLUDED.maturity_at`,
		string(book), string(f.ID), int64(f.Kind), f.Name, string(f.Asset),
		string(f.PrincipalGL), string(f.InterestGL), string(f.RefundGL),
		int64(f.Commitment), int64(f.Method), int64(f.TermMonths), int64(f.MinPayment),
		int64(f.Accrued), int64(f.AccruedGross),
		nullTime{f.LastAccrualDate}, int64(f.Arrears.DaysPastDue), int64(f.Arrears.Bucket),
		f.Arrears.NonPerforming, nullTime{f.Arrears.OldestUnpaidDue}, int64(f.Status),
		nullTime{f.OpenedAt}, nullTime{f.MaturityAt})
	if err != nil {
		return fmt.Errorf("sqlite: put facility %s: %w", f.ID, err)
	}
	return nil
}

// facilityColumns is the select list both readers use. Keeping it in one place
// is what stops GetFacility and ListFacilities from scanning different column
// sets, which is a whole class of "it round-trips one way".
const facilityColumns = `
	id, kind, name, asset, principal_gl, interest_gl, refund_gl,
	commitment, method, term_months, min_payment,
	accrued_interest, accrued_gross,
	last_accrual_date, days_past_due, arrears_bucket,
	non_performing, oldest_unpaid_due, status, opened_at, maturity_at`

// scanFacility reads one row of facilityColumns. Both *sql.Row (QueryRow) and
// *sql.Rows (Query) implement Scan(...any) error, so one function serves both
// readers.
func scanFacility(row interface{ Scan(...any) error }) (lending.Facility, error) {
	var (
		f                         lending.Facility
		kind, method              int64
		minPayment, accrued       int64
		gross                     int64
		bucket, status            int64
		lastAccrual, oldestUnpaid nullTime
		openedAt, maturityAt      nullTime
	)
	if err := row.Scan(&f.ID, &kind, &f.Name, &f.Asset, &f.PrincipalGL, &f.InterestGL, &f.RefundGL,
		&f.Commitment, &method, &f.TermMonths, &minPayment,
		&accrued, &gross,
		&lastAccrual, &f.Arrears.DaysPastDue, &bucket,
		&f.Arrears.NonPerforming, &oldestUnpaid, &status, &openedAt, &maturityAt); err != nil {
		return lending.Facility{}, err
	}
	f.Kind = lending.FacilityKind(kind)
	f.Method = lending.AmortMethod(method)
	f.MinPayment = interest.Fraction(minPayment)
	f.Accrued = interest.Accrued(accrued)
	f.AccruedGross = interest.Accrued(gross)
	f.LastAccrualDate = lastAccrual.Time
	f.Arrears.Bucket = lending.Bucket(bucket)
	f.Arrears.OldestUnpaidDue = oldestUnpaid.Time
	f.Status = lending.FacilityStatus(status)
	f.OpenedAt = openedAt.Time
	f.MaturityAt = maturityAt.Time
	return f, nil
}

func (t *tx) GetFacility(ctx context.Context, book ledger.BookID, id lending.FacilityID) (lending.Facility, error) {
	row := t.tx.QueryRowContext(ctx, `
		SELECT `+facilityColumns+`
		FROM facilities WHERE book_id = ? AND id = ?`,
		string(book), string(id))
	f, err := scanFacility(row)
	if errors.Is(err, sql.ErrNoRows) {
		return lending.Facility{}, lending.ErrFacilityNotFound
	}
	if err != nil {
		return lending.Facility{}, fmt.Errorf("sqlite: get facility %s: %w", id, err)
	}
	return f, nil
}

func (t *tx) ListFacilities(ctx context.Context, book ledger.BookID) ([]lending.Facility, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT `+facilityColumns+`
		FROM facilities WHERE book_id = ?
		ORDER BY opened_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list facilities: %w", err)
	}
	defer rows.Close()

	out := make([]lending.Facility, 0)
	for rows.Next() {
		f, err := scanFacility(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list facilities: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Instalments
// ---------------------------------------------------------------------------

// PutInstallment upserts under (book, facility, seq_no): recording a payment
// against an instalment replaces it rather than appending a second row.
func (t *tx) PutInstallment(ctx context.Context, book ledger.BookID, i lending.Installment) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO installments (
			book_id, facility_id, seq_no, due_date, principal, interest,
			paid_principal, paid_interest, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("installments")+`)
		ON CONFLICT (book_id, facility_id, seq_no) DO UPDATE SET
			due_date       = EXCLUDED.due_date,
			principal      = EXCLUDED.principal,
			interest       = EXCLUDED.interest,
			paid_principal = EXCLUDED.paid_principal,
			paid_interest  = EXCLUDED.paid_interest`,
		string(book), string(i.FacilityID), int64(i.Seq), nullTime{i.DueDate},
		int64(i.Principal), int64(i.Interest), int64(i.PaidPrincipal), int64(i.PaidInterest))
	if err != nil {
		return fmt.Errorf("sqlite: put instalment %s/%d: %w", i.FacilityID, i.Seq, err)
	}
	return nil
}

// installmentColumns is the select list ListInstallments uses. There is no
// GetInstallment, but the constant still keeps the column list next to the
// scan that reads it — the same discipline facilityColumns follows.
const installmentColumns = `
	facility_id, seq_no, due_date, principal, interest, paid_principal, paid_interest`

func scanInstallment(row interface{ Scan(...any) error }) (lending.Installment, error) {
	var (
		i       lending.Installment
		dueDate nullTime
	)
	if err := row.Scan(&i.FacilityID, &i.Seq, &dueDate, &i.Principal, &i.Interest,
		&i.PaidPrincipal, &i.PaidInterest); err != nil {
		return lending.Installment{}, err
	}
	i.DueDate = dueDate.Time
	return i, nil
}

// ListInstallments returns a facility's schedule ordered by seq_no — the one
// listing in this system not ordered by a timestamp, because seq_no is the
// instalment's position in the contract and is already a total order within a
// facility. It is book- and facility-scoped like every other listing here.
func (t *tx) ListInstallments(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.Installment, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT `+installmentColumns+`
		FROM installments WHERE book_id = ? AND facility_id = ?
		ORDER BY seq_no`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list instalments: %w", err)
	}
	defer rows.Close()

	out := make([]lending.Installment, 0)
	for rows.Next() {
		i, err := scanInstallment(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list instalments: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Effective-dated facility terms
// ---------------------------------------------------------------------------

// PutFacilityTerms upserts under (facility, effective day). The day key is
// derived with lending.TermsDayKey — the same function GetFacilityTermsAsOf
// compares against — so the write and the as-of read cannot disagree about which
// day a repricing landed in. Nothing here truncates a date; see that function.
func (t *tx) PutFacilityTerms(ctx context.Context, book ledger.BookID, row lending.FacilityTerms) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO facility_terms (
			book_id, facility_id, day_key, effective_from, rate, day_count, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("facility_terms")+`)
		ON CONFLICT (book_id, facility_id, day_key) DO UPDATE SET
			effective_from = EXCLUDED.effective_from,
			rate           = EXCLUDED.rate,
			day_count      = EXCLUDED.day_count,
			created_at     = EXCLUDED.created_at`,
		string(book), string(row.FacilityID), lending.TermsDayKey(row.EffectiveFrom),
		nullTime{row.EffectiveFrom}, int64(row.Rate), int64(row.DayCount), nullTime{row.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put facility terms %s/%s: %w",
			row.FacilityID, lending.TermsDayKey(row.EffectiveFrom), err)
	}
	return nil
}

// facilityTermsColumns is the select list both readers use, for the reason
// facilityColumns exists: it stops the two from scanning different column
// sets, which is a whole class of "it round-trips one way".
const facilityTermsColumns = `
	facility_id, effective_from, rate, day_count, created_at`

func scanFacilityTerms(row interface{ Scan(...any) error }) (lending.FacilityTerms, error) {
	var (
		out                lending.FacilityTerms
		rate               int64
		dayCount           int64
		effective, created nullTime
	)
	if err := row.Scan(&out.FacilityID, &effective, &rate, &dayCount, &created); err != nil {
		return lending.FacilityTerms{}, err
	}
	out.Rate = interest.Rate(rate)
	out.DayCount = interest.DayCount(dayCount)
	out.EffectiveFrom = effective.Time
	out.CreatedAt = created.Time
	return out, nil
}

// ListFacilityTerms returns the whole timeline ascending by day_key, which is
// an ISO day and therefore lexicographically ordered. Ascending is
// load-bearing: lending.termsAt binary-searches the slice this returns.
func (t *tx) ListFacilityTerms(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.FacilityTerms, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT `+facilityTermsColumns+`
		FROM facility_terms WHERE book_id = ? AND facility_id = ?
		ORDER BY day_key ASC, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list facility terms: %w", err)
	}
	defer rows.Close()

	out := make([]lending.FacilityTerms, 0)
	for rows.Next() {
		row, err := scanFacilityTerms(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list facility terms: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetFacilityTermsAsOf is the row in force on a day. It compares day_key
// rather than effective_from so that the bound is a DAY on both sides — the
// caller's instant is truncated by lending.TermsDayKey in Go, and the column it
// is compared against was written the same way, so no timestamp arithmetic
// happens in the database at all.
func (t *tx) GetFacilityTermsAsOf(ctx context.Context, book ledger.BookID, id lending.FacilityID, day time.Time) (lending.FacilityTerms, error) {
	row := t.tx.QueryRowContext(ctx, `
		SELECT `+facilityTermsColumns+`
		FROM facility_terms
		WHERE book_id = ? AND facility_id = ? AND day_key <= ?
		ORDER BY day_key DESC
		LIMIT 1`, string(book), string(id), lending.TermsDayKey(day))
	out, err := scanFacilityTerms(row)
	if errors.Is(err, sql.ErrNoRows) {
		return lending.FacilityTerms{}, lending.ErrTermsNotFound
	}
	if err != nil {
		return lending.FacilityTerms{}, fmt.Errorf("sqlite: facility terms for %s as of %s: %w",
			id, lending.TermsDayKey(day), err)
	}
	return out, nil
}
