package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// The deposit half of tx. It is the same type that implements ledger.Tx, and
// that is the whole point: a capture's hold write and its GL posting are two
// statements in one Postgres transaction, so they commit or roll back together.
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
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO deposit_accounts (
			book_id, id, gl_account, name, asset, status, overdraft_limit,
			overdraft_rate, unarranged_rate, day_count, accrued_interest,
			last_accrual_date, interest_gl, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (book_id, id) DO UPDATE SET
			gl_account        = EXCLUDED.gl_account,
			name              = EXCLUDED.name,
			asset             = EXCLUDED.asset,
			status            = EXCLUDED.status,
			overdraft_limit   = EXCLUDED.overdraft_limit,
			overdraft_rate    = EXCLUDED.overdraft_rate,
			unarranged_rate   = EXCLUDED.unarranged_rate,
			day_count         = EXCLUDED.day_count,
			accrued_interest  = EXCLUDED.accrued_interest,
			last_accrual_date = EXCLUDED.last_accrual_date,
			interest_gl       = EXCLUDED.interest_gl,
			created_at        = EXCLUDED.created_at`,
		string(book), string(a.ID), string(a.GLAccount), a.Name, string(a.Asset),
		int16(a.Status), a.OverdraftLimit,
		int64(a.Rate), int64(a.UnarrangedRate), int16(a.DayCount), int64(a.Accrued),
		nullTime(a.LastAccrualDate), string(a.InterestGL), nullTime(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put deposit account %s: %w", a.ID, err)
	}
	return nil
}

// depositAccountColumns is the select list both readers use. Keeping it in one
// place is what stops GetDepositAccount and ListDepositAccounts from scanning
// different column sets, which is a whole class of "it round-trips one way".
const depositAccountColumns = `
	id, gl_account, name, asset, status, overdraft_limit,
	overdraft_rate, unarranged_rate, day_count, accrued_interest,
	last_accrual_date, interest_gl, created_at`

// scanDepositAccount reads one row of depositAccountColumns. Both pgx.Row
// (QueryRow) and pgx.Rows (Query) implement Scan(...any) error, so one function
// serves both readers.
func scanDepositAccount(row interface{ Scan(...any) error }) (deposit.Account, error) {
	var (
		a                      deposit.Account
		status, dayCount       int16
		rate, unarranged       int64
		accrued                int64
		lastAccrual, createdAt *time.Time
	)
	if err := row.Scan(&a.ID, &a.GLAccount, &a.Name, &a.Asset, &status, &a.OverdraftLimit,
		&rate, &unarranged, &dayCount, &accrued, &lastAccrual, &a.InterestGL, &createdAt); err != nil {
		return deposit.Account{}, err
	}
	a.Status = deposit.AccountStatus(status)
	a.Rate = interest.Rate(rate)
	a.UnarrangedRate = interest.Rate(unarranged)
	a.DayCount = interest.DayCount(dayCount)
	a.Accrued = interest.Accrued(accrued)
	a.LastAccrualDate = readTime(lastAccrual)
	a.CreatedAt = readTime(createdAt)
	return a, nil
}

func (t *tx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	row := t.tx.QueryRow(ctx, `
		SELECT `+depositAccountColumns+`
		FROM deposit_accounts WHERE book_id = $1 AND id = $2`,
		string(book), string(id))
	a, err := scanDepositAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.Account{}, deposit.ErrAccountNotFound
	}
	if err != nil {
		return deposit.Account{}, fmt.Errorf("pg: get deposit account %s: %w", id, err)
	}
	return a, nil
}

func (t *tx) ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]deposit.Account, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT `+depositAccountColumns+`
		FROM deposit_accounts WHERE book_id = $1
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("pg: list deposit accounts: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.Account, 0)
	for rows.Next() {
		a, err := scanDepositAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list deposit accounts: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Holds
// ---------------------------------------------------------------------------

func (t *tx) PutHold(ctx context.Context, book ledger.BookID, h deposit.Hold) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO holds (book_id, id, account_id, amount, expires_at, description, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (book_id, id) DO UPDATE SET
			account_id  = EXCLUDED.account_id,
			amount      = EXCLUDED.amount,
			expires_at  = EXCLUDED.expires_at,
			description = EXCLUDED.description,
			status      = EXCLUDED.status,
			created_at  = EXCLUDED.created_at`,
		string(book), string(h.ID), string(h.AccountID), h.Amount,
		nullTime(h.ExpiresAt), h.Description, int16(h.Status), nullTime(h.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put hold %s: %w", h.ID, err)
	}
	return nil
}

func (t *tx) GetHold(ctx context.Context, book ledger.BookID, id deposit.HoldID) (deposit.Hold, error) {
	var (
		h                  deposit.Hold
		status             int16
		expiresAt, created *time.Time
	)
	err := t.tx.QueryRow(ctx, `
		SELECT id, account_id, amount, expires_at, description, status, created_at
		FROM holds WHERE book_id = $1 AND id = $2`,
		string(book), string(id)).Scan(&h.ID, &h.AccountID, &h.Amount, &expiresAt, &h.Description, &status, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.Hold{}, deposit.ErrHoldNotFound
	}
	if err != nil {
		return deposit.Hold{}, fmt.Errorf("pg: get hold %s: %w", id, err)
	}
	h.Status = deposit.HoldStatus(status)
	h.ExpiresAt = readTime(expiresAt)
	h.CreatedAt = readTime(created)
	return h, nil
}

func (t *tx) ListHoldsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Hold, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT id, account_id, amount, expires_at, description, status, created_at
		FROM holds WHERE book_id = $1 AND account_id = $2
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("pg: list holds: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.Hold, 0)
	for rows.Next() {
		var (
			h                  deposit.Hold
			status             int16
			expiresAt, created *time.Time
		)
		if err := rows.Scan(&h.ID, &h.AccountID, &h.Amount, &expiresAt, &h.Description, &status, &created); err != nil {
			return nil, fmt.Errorf("pg: list holds: %w", err)
		}
		h.Status = deposit.HoldStatus(status)
		h.ExpiresAt = readTime(expiresAt)
		h.CreatedAt = readTime(created)
		out = append(out, h)
	}
	return out, rows.Err()
}

// ActiveHoldTotal sums the holds that currently reduce an account's available
// balance: still Active, and not past their expiry as at now.
//
// NULL expires_at is a hold that never expires — the zero time.Time store/mem
// checks for. Expiry is strictly before now, so a hold expiring exactly at now
// still counts. Like BookBalance this is an aggregate: an unknown account is 0,
// not an error.
func (t *tx) ActiveHoldTotal(ctx context.Context, book ledger.BookID, id deposit.AccountID, now time.Time) (ledger.Amount, error) {
	var total ledger.Amount
	err := t.tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM holds
		WHERE book_id = $1 AND account_id = $2 AND status = $3
		  AND (expires_at IS NULL OR expires_at >= $4)`,
		string(book), string(id), int16(deposit.HoldActive), now.UTC()).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("pg: active hold total for %s: %w", id, err)
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// End-of-day snapshots
// ---------------------------------------------------------------------------

// PutSnapshot upserts a snapshot under (account, business date): a second
// snapshot for the same date replaces the first. The date key is derived with
// deposit.SnapshotDateKey, which is also what GetSnapshot is handed, so the two
// agree by construction.
func (t *tx) PutSnapshot(ctx context.Context, book ledger.BookID, s deposit.Snapshot) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO snapshots
			(book_id, account_id, date_key, business_date, book_balance, holds_balance, available_balance, taken_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (book_id, account_id, date_key) DO UPDATE SET
			business_date     = EXCLUDED.business_date,
			book_balance      = EXCLUDED.book_balance,
			holds_balance     = EXCLUDED.holds_balance,
			available_balance = EXCLUDED.available_balance,
			taken_at          = EXCLUDED.taken_at`,
		string(book), string(s.AccountID), deposit.SnapshotDateKey(s.Date), nullTime(s.Date),
		s.Balance.Book, s.Balance.Holds, s.Balance.Available, nullTime(s.TakenAt))
	if err != nil {
		return fmt.Errorf("pg: put snapshot %s: %w", s.AccountID, err)
	}
	return nil
}

func (t *tx) GetSnapshot(ctx context.Context, book ledger.BookID, id deposit.AccountID, dateKey string) (deposit.Snapshot, error) {
	var (
		s               deposit.Snapshot
		date, takenAt   *time.Time
		bal, hld, avail ledger.Amount
	)
	err := t.tx.QueryRow(ctx, `
		SELECT account_id, business_date, book_balance, holds_balance, available_balance, taken_at
		FROM snapshots WHERE book_id = $1 AND account_id = $2 AND date_key = $3`,
		string(book), string(id), dateKey).Scan(&s.AccountID, &date, &bal, &hld, &avail, &takenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.Snapshot{}, deposit.ErrSnapshotNotFound
	}
	if err != nil {
		return deposit.Snapshot{}, fmt.Errorf("pg: get snapshot %s/%s: %w", id, dateKey, err)
	}
	s.Date = readTime(date)
	s.TakenAt = readTime(takenAt)
	s.Balance = deposit.Balance{Book: bal, Holds: hld, Available: avail}
	return s, nil
}

// ListSnapshotsForAccount orders by business date, which within an account is
// already a total order; the insertion sequence is the tie-break only so that
// every listing follows one rule.
func (t *tx) ListSnapshotsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Snapshot, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT account_id, business_date, book_balance, holds_balance, available_balance, taken_at
		FROM snapshots WHERE book_id = $1 AND account_id = $2
		ORDER BY business_date ASC NULLS FIRST, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("pg: list snapshots: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.Snapshot, 0)
	for rows.Next() {
		var (
			s               deposit.Snapshot
			date, takenAt   *time.Time
			bal, hld, avail ledger.Amount
		)
		if err := rows.Scan(&s.AccountID, &date, &bal, &hld, &avail, &takenAt); err != nil {
			return nil, fmt.Errorf("pg: list snapshots: %w", err)
		}
		s.Date = readTime(date)
		s.TakenAt = readTime(takenAt)
		s.Balance = deposit.Balance{Book: bal, Holds: hld, Available: avail}
		out = append(out, s)
	}
	return out, rows.Err()
}
