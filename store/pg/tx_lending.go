package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

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
	_, err := t.tx.Exec(ctx, `
		INSERT INTO facilities (
			book_id, id, kind, name, asset, principal_gl, interest_gl, refund_gl,
			commitment, rate, day_count, method, term_months, min_payment,
			accrued_interest, accrued_gross, terms_effective_from,
			last_accrual_date, days_past_due, arrears_bucket,
			non_performing, oldest_unpaid_due, status, opened_at, maturity_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
		ON CONFLICT (book_id, id) DO UPDATE SET
			kind                 = EXCLUDED.kind,
			name                 = EXCLUDED.name,
			asset                = EXCLUDED.asset,
			principal_gl         = EXCLUDED.principal_gl,
			interest_gl          = EXCLUDED.interest_gl,
			refund_gl            = EXCLUDED.refund_gl,
			commitment           = EXCLUDED.commitment,
			rate                 = EXCLUDED.rate,
			day_count            = EXCLUDED.day_count,
			method               = EXCLUDED.method,
			term_months          = EXCLUDED.term_months,
			min_payment          = EXCLUDED.min_payment,
			accrued_interest     = EXCLUDED.accrued_interest,
			accrued_gross        = EXCLUDED.accrued_gross,
			terms_effective_from = EXCLUDED.terms_effective_from,
			last_accrual_date    = EXCLUDED.last_accrual_date,
			days_past_due        = EXCLUDED.days_past_due,
			arrears_bucket       = EXCLUDED.arrears_bucket,
			non_performing       = EXCLUDED.non_performing,
			oldest_unpaid_due    = EXCLUDED.oldest_unpaid_due,
			status               = EXCLUDED.status,
			opened_at            = EXCLUDED.opened_at,
			maturity_at          = EXCLUDED.maturity_at`,
		string(book), string(f.ID), int16(f.Kind), f.Name, string(f.Asset),
		string(f.PrincipalGL), string(f.InterestGL), string(f.RefundGL),
		f.Commitment, int64(f.Rate), int16(f.DayCount), int16(f.Method), f.TermMonths, int64(f.MinPayment),
		int64(f.Accrued), int64(f.AccruedGross), nullTime(f.TermsEffectiveFrom),
		nullTime(f.LastAccrualDate), f.Arrears.DaysPastDue, int16(f.Arrears.Bucket),
		f.Arrears.NonPerforming, nullTime(f.Arrears.OldestUnpaidDue), int16(f.Status),
		nullTime(f.OpenedAt), nullTime(f.MaturityAt))
	if err != nil {
		return fmt.Errorf("pg: put facility %s: %w", f.ID, err)
	}
	return nil
}

// facilityColumns is the select list both readers use. Keeping it in one place
// is what stops GetFacility and ListFacilities from scanning different column
// sets, which is a whole class of "it round-trips one way".
const facilityColumns = `
	id, kind, name, asset, principal_gl, interest_gl, refund_gl,
	commitment, rate, day_count, method, term_months, min_payment,
	accrued_interest, accrued_gross, terms_effective_from,
	last_accrual_date, days_past_due, arrears_bucket,
	non_performing, oldest_unpaid_due, status, opened_at, maturity_at`

// scanFacility reads one row of facilityColumns. Both pgx.Row (QueryRow) and
// pgx.Rows (Query) implement Scan(...any) error, so one function serves both
// readers.
func scanFacility(row interface{ Scan(...any) error }) (lending.Facility, error) {
	var (
		f                         lending.Facility
		kind, dayCount, method    int16
		rate, minPayment, accrued int64
		gross                     int64
		bucket, status            int16
		termsFrom                 *time.Time
		lastAccrual, oldestUnpaid *time.Time
		openedAt, maturityAt      *time.Time
	)
	if err := row.Scan(&f.ID, &kind, &f.Name, &f.Asset, &f.PrincipalGL, &f.InterestGL, &f.RefundGL,
		&f.Commitment, &rate, &dayCount, &method, &f.TermMonths, &minPayment,
		&accrued, &gross, &termsFrom,
		&lastAccrual, &f.Arrears.DaysPastDue, &bucket,
		&f.Arrears.NonPerforming, &oldestUnpaid, &status, &openedAt, &maturityAt); err != nil {
		return lending.Facility{}, err
	}
	f.Kind = lending.FacilityKind(kind)
	f.DayCount = interest.DayCount(dayCount)
	f.Method = lending.AmortMethod(method)
	f.Rate = interest.Rate(rate)
	f.MinPayment = interest.Fraction(minPayment)
	f.Accrued = interest.Accrued(accrued)
	f.AccruedGross = interest.Accrued(gross)
	f.TermsEffectiveFrom = readTime(termsFrom)
	f.LastAccrualDate = readTime(lastAccrual)
	f.Arrears.Bucket = lending.Bucket(bucket)
	f.Arrears.OldestUnpaidDue = readTime(oldestUnpaid)
	f.Status = lending.FacilityStatus(status)
	f.OpenedAt = readTime(openedAt)
	f.MaturityAt = readTime(maturityAt)
	return f, nil
}

func (t *tx) GetFacility(ctx context.Context, book ledger.BookID, id lending.FacilityID) (lending.Facility, error) {
	row := t.tx.QueryRow(ctx, `
		SELECT `+facilityColumns+`
		FROM facilities WHERE book_id = $1 AND id = $2`,
		string(book), string(id))
	f, err := scanFacility(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return lending.Facility{}, lending.ErrFacilityNotFound
	}
	if err != nil {
		return lending.Facility{}, fmt.Errorf("pg: get facility %s: %w", id, err)
	}
	return f, nil
}

func (t *tx) ListFacilities(ctx context.Context, book ledger.BookID) ([]lending.Facility, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT `+facilityColumns+`
		FROM facilities WHERE book_id = $1
		ORDER BY opened_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("pg: list facilities: %w", err)
	}
	defer rows.Close()

	out := make([]lending.Facility, 0)
	for rows.Next() {
		f, err := scanFacility(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list facilities: %w", err)
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
	_, err := t.tx.Exec(ctx, `
		INSERT INTO installments (
			book_id, facility_id, seq_no, due_date, principal, interest,
			paid_principal, paid_interest)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (book_id, facility_id, seq_no) DO UPDATE SET
			due_date       = EXCLUDED.due_date,
			principal      = EXCLUDED.principal,
			interest       = EXCLUDED.interest,
			paid_principal = EXCLUDED.paid_principal,
			paid_interest  = EXCLUDED.paid_interest`,
		string(book), string(i.FacilityID), i.Seq, nullTime(i.DueDate),
		i.Principal, i.Interest, i.PaidPrincipal, i.PaidInterest)
	if err != nil {
		return fmt.Errorf("pg: put instalment %s/%d: %w", i.FacilityID, i.Seq, err)
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
		dueDate *time.Time
	)
	if err := row.Scan(&i.FacilityID, &i.Seq, &dueDate, &i.Principal, &i.Interest,
		&i.PaidPrincipal, &i.PaidInterest); err != nil {
		return lending.Installment{}, err
	}
	i.DueDate = readTime(dueDate)
	return i, nil
}

// ListInstallments returns a facility's schedule ordered by seq_no — the one
// listing in this system not ordered by a timestamp, because seq_no is the
// instalment's position in the contract and is already a total order within a
// facility. It is book- and facility-scoped like every other listing here.
func (t *tx) ListInstallments(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.Installment, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT `+installmentColumns+`
		FROM installments WHERE book_id = $1 AND facility_id = $2
		ORDER BY seq_no`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("pg: list instalments: %w", err)
	}
	defer rows.Close()

	out := make([]lending.Installment, 0)
	for rows.Next() {
		i, err := scanInstallment(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list instalments: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
