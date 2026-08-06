package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// The deposit half of tx. It is the same type that implements ledger.Tx, and
// that is the whole point: a capture's hold write and its GL posting are two
// statements in one SQLite transaction, so they commit or roll back together.
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
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO deposit_accounts (
			book_id, id, gl_account, name, asset, status, accrued_interest,
			accrued_gross, last_accrual_date, interest_gl, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("deposit_accounts")+`)
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
		int64(a.Status), int64(a.Accrued), int64(a.AccruedGross),
		nullTime{a.LastAccrualDate}, string(a.InterestGL), nullTime{a.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put deposit account %s: %w", a.ID, err)
	}

	// Replace the identifier set. Delete-then-insert rather than a merge: the
	// account is one aggregate and PutDepositAccount is an upsert of all of it,
	// so a removed identifier has to actually disappear.
	if _, err := t.tx.ExecContext(ctx,
		`DELETE FROM deposit_account_identifiers WHERE book_id = ? AND deposit_account_id = ?`,
		string(book), string(a.ID)); err != nil {
		return fmt.Errorf("sqlite: clear identifiers for %s: %w", a.ID, err)
	}
	for _, ident := range a.Identifiers {
		if _, err := t.tx.ExecContext(ctx, `
			INSERT INTO deposit_account_identifiers (book_id, deposit_account_id, scheme, value)
			VALUES (?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			string(book), string(a.ID), string(ident.Scheme), ident.Value); err != nil {
			return fmt.Errorf("sqlite: put identifier %s/%s for %s: %w", ident.Scheme, ident.Value, a.ID, err)
		}
	}
	return nil
}

// depositAccountColumns is the select list both readers use. Keeping it in one
// place is what stops GetDepositAccount and ListDepositAccounts from scanning
// different column sets, which is a whole class of "it round-trips one way".
const depositAccountColumns = `
	id, gl_account, name, asset, status, accrued_interest,
	accrued_gross, last_accrual_date, interest_gl, created_at`

// scanDepositAccount reads one row of depositAccountColumns. Both *sql.Row
// (QueryRow) and *sql.Rows (Query) implement Scan(...any) error, so one function
// serves both readers.
func scanDepositAccount(row interface{ Scan(...any) error }) (deposit.Account, error) {
	var (
		a                      deposit.Account
		status                 int64
		accrued, gross         int64
		lastAccrual, createdAt nullTime
	)
	if err := row.Scan(&a.ID, &a.GLAccount, &a.Name, &a.Asset, &status,
		&accrued, &gross, &lastAccrual, &a.InterestGL, &createdAt); err != nil {
		return deposit.Account{}, err
	}
	a.Status = deposit.AccountStatus(status)
	a.Accrued = interest.Accrued(accrued)
	a.AccruedGross = interest.Accrued(gross)
	a.LastAccrualDate = lastAccrual.Time
	a.CreatedAt = createdAt.Time
	return a, nil
}

func (t *tx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	row := t.tx.QueryRowContext(ctx, `
		SELECT `+depositAccountColumns+`
		FROM deposit_accounts WHERE book_id = ? AND id = ?`,
		string(book), string(id))
	a, err := scanDepositAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return deposit.Account{}, deposit.ErrAccountNotFound
	}
	if err != nil {
		return deposit.Account{}, fmt.Errorf("sqlite: get deposit account %s: %w", id, err)
	}
	one := []deposit.Account{a}
	if err := t.hydrateIdentifiers(ctx, book, one); err != nil {
		return deposit.Account{}, err
	}
	return one[0], nil
}

func (t *tx) ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]deposit.Account, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT `+depositAccountColumns+`
		FROM deposit_accounts WHERE book_id = ?
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list deposit accounts: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.Account, 0)
	for rows.Next() {
		a, err := scanDepositAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list deposit accounts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, t.hydrateIdentifiers(ctx, book, out)
}

// hydrateIdentifiers fills Identifiers on the accounts given, in ONE query.
//
// One query and not one per account: the multi-asset rework found exactly this
// N+1 in two listing endpoints, and a bank with a page of customers would
// reintroduce it here.
//
// store/pg spelled the ordering `ORDER BY scheme COLLATE "C", value COLLATE "C"`,
// because a bare ORDER BY there sorts under the cluster's collation, which on a
// typical en_US.UTF-8 install ignores punctuation at the first level — so
// SE89-AURORA-1001 and SE89AURORA0999 would order one way in Postgres and the
// other way in store/mem, and which one depended on where the database was
// created. SQLite's default collation is BINARY, which IS byte order and is what
// store/mem's strings.Compare does, so the qualifier has nothing to correct here
// and is left off rather than written as a no-op.
func (t *tx) hydrateIdentifiers(ctx context.Context, book ledger.BookID, accounts []deposit.Account) error {
	if len(accounts) == 0 {
		return nil
	}
	args := make([]any, 0, len(accounts)+1)
	args = append(args, string(book))
	for _, a := range accounts {
		args = append(args, string(a.ID))
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT deposit_account_id, scheme, value
		FROM deposit_account_identifiers
		WHERE book_id = ? AND deposit_account_id IN (`+placeholders(len(accounts))+`)
		ORDER BY scheme, value`, args...)
	if err != nil {
		return fmt.Errorf("sqlite: list identifiers: %w", err)
	}
	defer rows.Close()

	byAccount := make(map[deposit.AccountID][]deposit.Identifier, len(accounts))
	for rows.Next() {
		var (
			id    deposit.AccountID
			ident deposit.Identifier
		)
		if err := rows.Scan(&id, &ident.Scheme, &ident.Value); err != nil {
			return fmt.Errorf("sqlite: list identifiers: %w", err)
		}
		byAccount[id] = append(byAccount[id], ident)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range accounts {
		accounts[i].Identifiers = byAccount[accounts[i].ID]
	}
	return nil
}

// ListDepositAccountsByIdentifier returns every account in the book holding
// this (scheme, value) pair. EXISTS rather than a JOIN, so that an account with
// the pair recorded twice — which nothing here prevents; see
// deposit_account_identifiers in the schema — still surfaces once and not twice.
//
// The scheme matches exactly and the VALUE matches under the scheme's own
// comparison rule, which for an IBAN means with display separators removed from
// both sides: the register stores SE89-AURORA-1001 and a pacs.008 carries
// SE89AURORA1001, and those are one address. See deposit.Identifier.MatchValue,
// which is where the rule and its reasons live.
//
// The predicate is chosen in Go from the scheme rather than written as a CASE,
// because only the IBAN arm may strip anything: an identifier scheme whose
// values legitimately contain hyphens would have two addresses silently merged.
// This SQL is the third place the separator set is written — the other two are
// deposit.ibanSeparators and iso20022.IBAN — and it is the one the compiler
// cannot check, so it is pinned by
// storetest/ListDepositAccountsByIdentifierMatchesAnIBANThroughItsSeparators,
// which runs against every store and fails if they disagree.
//
// For the IBAN arm it gives up the VALUE column of the (book_id, scheme, value)
// index, which is the honest cost of comparing a derived form; the
// (book_id, scheme) prefix still applies. See the index in the schema, where the
// alternatives and the reason neither was taken are recorded.
func (t *tx) ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident deposit.Identifier) ([]deposit.Account, error) {
	value := `i.value = ?`
	if ident.Scheme == deposit.IdentifierIBAN {
		value = `replace(replace(i.value, ' ', ''), '-', '') = ?`
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT `+depositAccountColumns+`
		FROM deposit_accounts a
		WHERE a.book_id = ? AND EXISTS (
			SELECT 1 FROM deposit_account_identifiers i
			WHERE i.book_id = a.book_id AND i.deposit_account_id = a.id
			  AND i.scheme = ? AND `+value+`)
		ORDER BY a.created_at ASC NULLS FIRST, a.seq`,
		string(book), string(ident.Scheme), ident.MatchValue())
	if err != nil {
		return nil, fmt.Errorf("sqlite: list deposit accounts by identifier: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.Account, 0)
	for rows.Next() {
		a, err := scanDepositAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list deposit accounts by identifier: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, t.hydrateIdentifiers(ctx, book, out)
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
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO holds (book_id, id, account_id, amount, expires_at, description, status, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("holds")+`)
		ON CONFLICT (book_id, id) DO UPDATE SET
			account_id  = EXCLUDED.account_id,
			amount      = EXCLUDED.amount,
			expires_at  = EXCLUDED.expires_at,
			description = EXCLUDED.description,
			status      = EXCLUDED.status,
			created_at  = EXCLUDED.created_at`,
		string(book), string(h.ID), string(h.AccountID), int64(h.Amount),
		nullTime{h.ExpiresAt}, h.Description, int64(h.Status), nullTime{h.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put hold %s: %w", h.ID, err)
	}
	return nil
}

func (t *tx) GetHold(ctx context.Context, book ledger.BookID, id deposit.HoldID) (deposit.Hold, error) {
	var (
		h                  deposit.Hold
		status             int64
		expiresAt, created nullTime
	)
	err := t.tx.QueryRowContext(ctx, `
		SELECT id, account_id, amount, expires_at, description, status, created_at
		FROM holds WHERE book_id = ? AND id = ?`,
		string(book), string(id)).Scan(&h.ID, &h.AccountID, &h.Amount, &expiresAt, &h.Description, &status, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return deposit.Hold{}, deposit.ErrHoldNotFound
	}
	if err != nil {
		return deposit.Hold{}, fmt.Errorf("sqlite: get hold %s: %w", id, err)
	}
	h.Status = deposit.HoldStatus(status)
	h.ExpiresAt = expiresAt.Time
	h.CreatedAt = created.Time
	return h, nil
}

func (t *tx) ListHoldsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Hold, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT id, account_id, amount, expires_at, description, status, created_at
		FROM holds WHERE book_id = ? AND account_id = ?
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list holds: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.Hold, 0)
	for rows.Next() {
		var (
			h                  deposit.Hold
			status             int64
			expiresAt, created nullTime
		)
		if err := rows.Scan(&h.ID, &h.AccountID, &h.Amount, &expiresAt, &h.Description, &status, &created); err != nil {
			return nil, fmt.Errorf("sqlite: list holds: %w", err)
		}
		h.Status = deposit.HoldStatus(status)
		h.ExpiresAt = expiresAt.Time
		h.CreatedAt = created.Time
		out = append(out, h)
	}
	return out, rows.Err()
}

// ActiveHoldTotal sums the holds that currently reduce an account's available
// balance: still Active, and not past their expiry as at now.
//
// NULL expires_at is a hold that never expires — the zero time.Time store/mem
// checks for — and it is admitted by an explicit IS NULL rather than by the
// comparison, which would be unknown. Expiry is strictly before now, so a hold
// expiring exactly at now still counts. Like BookBalance this is an aggregate:
// an unknown account is 0, not an error.
func (t *tx) ActiveHoldTotal(ctx context.Context, book ledger.BookID, id deposit.AccountID, now time.Time) (ledger.Amount, error) {
	var total ledger.Amount
	err := t.tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM holds
		WHERE book_id = ? AND account_id = ? AND status = ?
		  AND (expires_at IS NULL OR expires_at >= ?)`,
		string(book), string(id), int64(deposit.HoldActive), formatTime(now)).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sqlite: active hold total for %s: %w", id, err)
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
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO snapshots
			(book_id, account_id, date_key, business_date, book_balance, holds_balance, available_balance, taken_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("snapshots")+`)
		ON CONFLICT (book_id, account_id, date_key) DO UPDATE SET
			business_date     = EXCLUDED.business_date,
			book_balance      = EXCLUDED.book_balance,
			holds_balance     = EXCLUDED.holds_balance,
			available_balance = EXCLUDED.available_balance,
			taken_at          = EXCLUDED.taken_at`,
		string(book), string(s.AccountID), deposit.SnapshotDateKey(s.Date), nullTime{s.Date},
		int64(s.Balance.Book), int64(s.Balance.Holds), int64(s.Balance.Available), nullTime{s.TakenAt})
	if err != nil {
		return fmt.Errorf("sqlite: put snapshot %s: %w", s.AccountID, err)
	}
	return nil
}

func (t *tx) GetSnapshot(ctx context.Context, book ledger.BookID, id deposit.AccountID, dateKey string) (deposit.Snapshot, error) {
	var (
		s               deposit.Snapshot
		date, takenAt   nullTime
		bal, hld, avail ledger.Amount
	)
	err := t.tx.QueryRowContext(ctx, `
		SELECT account_id, business_date, book_balance, holds_balance, available_balance, taken_at
		FROM snapshots WHERE book_id = ? AND account_id = ? AND date_key = ?`,
		string(book), string(id), dateKey).Scan(&s.AccountID, &date, &bal, &hld, &avail, &takenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return deposit.Snapshot{}, deposit.ErrSnapshotNotFound
	}
	if err != nil {
		return deposit.Snapshot{}, fmt.Errorf("sqlite: get snapshot %s/%s: %w", id, dateKey, err)
	}
	s.Date = date.Time
	s.TakenAt = takenAt.Time
	s.Balance = deposit.Balance{Book: bal, Holds: hld, Available: avail}
	return s, nil
}

// ListSnapshotsForAccount orders by business date, which within an account is
// already a total order; the insertion sequence is the tie-break only so that
// every listing follows one rule.
func (t *tx) ListSnapshotsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Snapshot, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT account_id, business_date, book_balance, holds_balance, available_balance, taken_at
		FROM snapshots WHERE book_id = ? AND account_id = ?
		ORDER BY business_date ASC NULLS FIRST, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list snapshots: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.Snapshot, 0)
	for rows.Next() {
		var (
			s               deposit.Snapshot
			date, takenAt   nullTime
			bal, hld, avail ledger.Amount
		)
		if err := rows.Scan(&s.AccountID, &date, &bal, &hld, &avail, &takenAt); err != nil {
			return nil, fmt.Errorf("sqlite: list snapshots: %w", err)
		}
		s.Date = date.Time
		s.TakenAt = takenAt.Time
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
// with, and the same one GetOverdraftTermsAsOf compares against — so the stores
// agree on which day a repricing landed in by construction.
func (t *tx) PutOverdraftTerms(ctx context.Context, book ledger.BookID, row deposit.OverdraftTerms) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	// A floating row stores three NULLs rather than three zeros: zero is a real
	// interest-free price, and conflating the two would silently make every
	// floating account free.
	var rate, unarranged, dayCount any
	if row.Pricing != nil {
		rate = int64(row.Pricing.Rate)
		unarranged = int64(row.Pricing.UnarrangedRate)
		dayCount = int64(row.Pricing.DayCount)
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO overdraft_terms (
			book_id, account_id, day_key, effective_from, product_id, overdraft_limit,
			rate, unarranged_rate, day_count, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("overdraft_terms")+`)
		ON CONFLICT (book_id, account_id, day_key) DO UPDATE SET
			effective_from  = EXCLUDED.effective_from,
			product_id      = EXCLUDED.product_id,
			overdraft_limit = EXCLUDED.overdraft_limit,
			rate            = EXCLUDED.rate,
			unarranged_rate = EXCLUDED.unarranged_rate,
			day_count       = EXCLUDED.day_count,
			created_at      = EXCLUDED.created_at`,
		string(book), string(row.AccountID), deposit.TermsDayKey(row.EffectiveFrom),
		nullTime{row.EffectiveFrom}, string(row.ProductID), int64(row.OverdraftLimit),
		rate, unarranged, dayCount, nullTime{row.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put overdraft terms %s/%s: %w",
			row.AccountID, deposit.TermsDayKey(row.EffectiveFrom), err)
	}
	return nil
}

// overdraftTermsColumns is the select list both readers use, for the reason
// depositAccountColumns exists: it stops the two from scanning different
// column sets, which is a whole class of "it round-trips one way".
const overdraftTermsColumns = `
	account_id, effective_from, product_id, overdraft_limit, rate, unarranged_rate,
	day_count, created_at`

func scanOverdraftTerms(row interface{ Scan(...any) error }) (deposit.OverdraftTerms, error) {
	var (
		out                deposit.OverdraftTerms
		rate, unarranged   sql.NullInt64
		dayCount           sql.NullInt64
		effective, created nullTime
	)
	if err := row.Scan(&out.AccountID, &effective, &out.ProductID, &out.OverdraftLimit,
		&rate, &unarranged, &dayCount, &created); err != nil {
		return deposit.OverdraftTerms{}, err
	}
	// The overlay is reconstructed only when all three columns are present. The
	// mixed state cannot be written through deposit.OverdraftTerms.Validate, and
	// treating a partial row as an overlay would price an account from half a
	// price rather than failing where it can be seen.
	if rate.Valid && unarranged.Valid && dayCount.Valid {
		out.Pricing = &product.OverdraftPricing{
			Rate:           interest.Rate(rate.Int64),
			UnarrangedRate: interest.Rate(unarranged.Int64),
			DayCount:       interest.DayCount(dayCount.Int64),
		}
	}
	out.EffectiveFrom = effective.Time
	out.CreatedAt = created.Time
	return out, nil
}

// ListOverdraftTermsForAccount returns the whole timeline ascending by day_key,
// which is an ISO day and therefore lexicographically ordered. Ascending is
// load-bearing: deposit.termsAt binary-searches the slice this returns.
func (t *tx) ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.OverdraftTerms, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT `+overdraftTermsColumns+`
		FROM overdraft_terms WHERE book_id = ? AND account_id = ?
		ORDER BY day_key ASC, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list overdraft terms: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.OverdraftTerms, 0)
	for rows.Next() {
		row, err := scanOverdraftTerms(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list overdraft terms: %w", err)
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
	row := t.tx.QueryRowContext(ctx, `
		SELECT `+overdraftTermsColumns+`
		FROM overdraft_terms
		WHERE book_id = ? AND account_id = ? AND day_key <= ?
		ORDER BY day_key DESC
		LIMIT 1`, string(book), string(id), deposit.TermsDayKey(day))
	out, err := scanOverdraftTerms(row)
	if errors.Is(err, sql.ErrNoRows) {
		return deposit.OverdraftTerms{}, deposit.ErrTermsNotFound
	}
	if err != nil {
		return deposit.OverdraftTerms{}, fmt.Errorf("sqlite: overdraft terms for %s as of %s: %w",
			id, deposit.TermsDayKey(day), err)
	}
	return out, nil
}
