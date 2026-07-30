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
			book_id, id, gl_account, name, asset, status, accrued_interest,
			accrued_gross, last_accrual_date, interest_gl, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (book_id, id) DO UPDATE SET
			gl_account        = EXCLUDED.gl_account,
			name              = EXCLUDED.name,
			asset             = EXCLUDED.asset,
			status            = EXCLUDED.status,
			accrued_interest  = EXCLUDED.accrued_interest,
			accrued_gross     = EXCLUDED.accrued_gross,
			last_accrual_date = EXCLUDED.last_accrual_date,
			interest_gl       = EXCLUDED.interest_gl,
			created_at        = EXCLUDED.created_at`,
		string(book), string(a.ID), string(a.GLAccount), a.Name, string(a.Asset),
		int16(a.Status), int64(a.Accrued), int64(a.AccruedGross),
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
	id, gl_account, name, asset, status, accrued_interest,
	accrued_gross, last_accrual_date, interest_gl, created_at`

// scanDepositAccount reads one row of depositAccountColumns. Both pgx.Row
// (QueryRow) and pgx.Rows (Query) implement Scan(...any) error, so one function
// serves both readers.
func scanDepositAccount(row interface{ Scan(...any) error }) (deposit.Account, error) {
	var (
		a                      deposit.Account
		status                 int16
		accrued, gross         int64
		lastAccrual, createdAt *time.Time
	)
	if err := row.Scan(&a.ID, &a.GLAccount, &a.Name, &a.Asset, &status,
		&accrued, &gross, &lastAccrual, &a.InterestGL, &createdAt); err != nil {
		return deposit.Account{}, err
	}
	a.Status = deposit.AccountStatus(status)
	a.Accrued = interest.Accrued(accrued)
	a.AccruedGross = interest.Accrued(gross)
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

// ---------------------------------------------------------------------------
// Effective-dated overdraft terms
// ---------------------------------------------------------------------------

// PutOverdraftTerms upserts under (account, effective day). The day key is
// derived with deposit.TermsDayKey — the same function store/mem keys its map
// with, and the same one GetOverdraftTermsAsOf compares against — so the two
// stores agree on which day a repricing landed in by construction.
func (t *tx) PutOverdraftTerms(ctx context.Context, book ledger.BookID, row deposit.OverdraftTerms) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO overdraft_terms (
			book_id, account_id, day_key, effective_from, overdraft_limit,
			rate, unarranged_rate, day_count, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (book_id, account_id, day_key) DO UPDATE SET
			effective_from  = EXCLUDED.effective_from,
			overdraft_limit = EXCLUDED.overdraft_limit,
			rate            = EXCLUDED.rate,
			unarranged_rate = EXCLUDED.unarranged_rate,
			day_count       = EXCLUDED.day_count,
			created_at      = EXCLUDED.created_at`,
		string(book), string(row.AccountID), deposit.TermsDayKey(row.EffectiveFrom),
		nullTime(row.EffectiveFrom), row.OverdraftLimit,
		int64(row.Rate), int64(row.UnarrangedRate), int16(row.DayCount), nullTime(row.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put overdraft terms %s/%s: %w",
			row.AccountID, deposit.TermsDayKey(row.EffectiveFrom), err)
	}
	return nil
}

// overdraftTermsColumns is the select list both readers use, for the reason
// depositAccountColumns exists: it stops the two from scanning different
// column sets, which is a whole class of "it round-trips one way".
const overdraftTermsColumns = `
	account_id, effective_from, overdraft_limit, rate, unarranged_rate,
	day_count, created_at`

func scanOverdraftTerms(row interface{ Scan(...any) error }) (deposit.OverdraftTerms, error) {
	var (
		out                deposit.OverdraftTerms
		rate, unarranged   int64
		dayCount           int16
		effective, created *time.Time
	)
	if err := row.Scan(&out.AccountID, &effective, &out.OverdraftLimit,
		&rate, &unarranged, &dayCount, &created); err != nil {
		return deposit.OverdraftTerms{}, err
	}
	out.Rate = interest.Rate(rate)
	out.UnarrangedRate = interest.Rate(unarranged)
	out.DayCount = interest.DayCount(dayCount)
	out.EffectiveFrom = readTime(effective)
	out.CreatedAt = readTime(created)
	return out, nil
}

// ListOverdraftTermsForAccount returns the whole timeline ascending by day_key,
// which is an ISO day and therefore lexicographically ordered. Ascending is
// load-bearing: deposit.termsAt binary-searches the slice this returns.
func (t *tx) ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.OverdraftTerms, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT `+overdraftTermsColumns+`
		FROM overdraft_terms WHERE book_id = $1 AND account_id = $2
		ORDER BY day_key ASC, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("pg: list overdraft terms: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.OverdraftTerms, 0)
	for rows.Next() {
		row, err := scanOverdraftTerms(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list overdraft terms: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetOverdraftTermsAsOf is the row in force on a day. It compares day_key
// rather than effective_from so that the bound is a DAY on both sides — the
// caller's instant is truncated by deposit.TermsDayKey in Go, and the column it
// is compared against was written the same way, so no timestamp arithmetic
// happens in the database at all.
func (t *tx) GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id deposit.AccountID, day time.Time) (deposit.OverdraftTerms, error) {
	row := t.tx.QueryRow(ctx, `
		SELECT `+overdraftTermsColumns+`
		FROM overdraft_terms
		WHERE book_id = $1 AND account_id = $2 AND day_key <= $3
		ORDER BY day_key DESC
		LIMIT 1`, string(book), string(id), deposit.TermsDayKey(day))
	out, err := scanOverdraftTerms(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.OverdraftTerms{}, deposit.ErrTermsNotFound
	}
	if err != nil {
		return deposit.OverdraftTerms{}, fmt.Errorf("pg: overdraft terms for %s as of %s: %w",
			id, deposit.TermsDayKey(day), err)
	}
	return out, nil
}
