package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/raphi011/cbs/ledger"
)

// tx is one unit of work: one Postgres transaction on one pooled connection.
// The same value implements ledger.Tx, deposit.Tx and payment.Tx, which is what
// lets a settlement post across several banks' books and record itself in a
// single BEGIN … COMMIT.
type tx struct {
	store    *Store
	tx       pgx.Tx
	readOnly bool

	// books remembers which books this transaction has already inserted a row
	// for, so the FK-parent insert happens once per book per transaction rather
	// than on every write.
	books map[ledger.BookID]struct{}
}

// compile-time check that tx satisfies the interface it is written against.
var _ ledger.Tx = (*tx)(nil)

// Now returns the store's current time.
func (t *tx) Now() time.Time { return t.store.clock() }

// write reports whether this transaction may mutate. The Postgres transaction is
// also READ ONLY, but failing here first means store/mem and store/pg return the
// same error for the same mistake.
func (t *tx) write() error {
	if t.readOnly {
		return ErrReadOnly
	}
	return nil
}

// ensureBook creates the books row every book-scoped table's foreign key points
// at. Books are not an entity the domain creates — a BookID is simply a name a
// participant is written under — so the row appears the first time something is
// written under it.
func (t *tx) ensureBook(ctx context.Context, book ledger.BookID) error {
	if _, ok := t.books[book]; ok {
		return nil
	}
	if _, err := t.tx.Exec(ctx,
		"INSERT INTO books (id) VALUES ($1) ON CONFLICT (id) DO NOTHING", string(book),
	); err != nil {
		return fmt.Errorf("pg: ensure book %s: %w", book, err)
	}
	t.books[book] = struct{}{}
	return nil
}

// ---------------------------------------------------------------------------
// Identity allocation
// ---------------------------------------------------------------------------

// nextSeq allocates the next value of one counter, gap-free.
//
// The counter is an ordinary row, so the allocation rolls back with the caller's
// transaction and two concurrent allocators serialize on the row lock. A
// Postgres SEQUENCE would be wrong: sequences deliberately survive a rollback,
// so a failed posting would burn a transaction number.
func (t *tx) nextSeq(ctx context.Context, book ledger.BookID, name string) (int64, error) {
	if err := t.write(); err != nil {
		return 0, err
	}
	var next int64
	err := t.tx.QueryRow(ctx, `
		INSERT INTO id_sequences (book_id, name, next_value) VALUES ($1, $2, 1)
		ON CONFLICT (book_id, name) DO UPDATE SET next_value = id_sequences.next_value + 1
		RETURNING next_value`, string(book), name).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("pg: next %s for %s: %w", name, book, err)
	}
	return next, nil
}

// NextID issues the next ID with the given prefix.
//
// One counter is shared by ALL prefixes within a book — the counter is named
// "id", not named after the prefix — so ldg_1, tx_2 and ent_3 interleave and the
// number doubles as a creation order, exactly as store/mem does it.
func (t *tx) NextID(ctx context.Context, book ledger.BookID, prefix string) (string, error) {
	n, err := t.nextSeq(ctx, book, "id")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%d", prefix, n), nil
}

// NextSubledgerBlock issues the next chart-of-accounts block for a book: 100,
// 200, 300, …
func (t *tx) NextSubledgerBlock(ctx context.Context, book ledger.BookID) (int, error) {
	n, err := t.nextSeq(ctx, book, "subledger_block")
	if err != nil {
		return 0, err
	}
	return int(n) * 100, nil
}

// NextAccountSeq issues the next account number within one
// "<typeBlock>.<subledgerID>" branch of a book, restarting at 1 per branch.
func (t *tx) NextAccountSeq(ctx context.Context, book ledger.BookID, typeBlock int, subledger ledger.SubledgerID) (int, error) {
	n, err := t.nextSeq(ctx, book, fmt.Sprintf("account:%d.%s", typeBlock, subledger))
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// ---------------------------------------------------------------------------
// Ledgers
// ---------------------------------------------------------------------------

func (t *tx) PutLedger(ctx context.Context, book ledger.BookID, l ledger.Ledger) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	// seq is absent from the DO UPDATE list on purpose: an upsert keeps the row
	// where it already was in its listing.
	_, err := t.tx.Exec(ctx, `
		INSERT INTO ledgers (book_id, id, name, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (book_id, id) DO UPDATE SET name = EXCLUDED.name, created_at = EXCLUDED.created_at`,
		string(book), string(l.ID), l.Name, nullTime(l.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put ledger %s: %w", l.ID, err)
	}
	return nil
}

func (t *tx) GetLedger(ctx context.Context, book ledger.BookID, id ledger.LedgerID) (ledger.Ledger, error) {
	var l ledger.Ledger
	var createdAt *time.Time
	err := t.tx.QueryRow(ctx,
		"SELECT id, name, created_at FROM ledgers WHERE book_id = $1 AND id = $2",
		string(book), string(id)).Scan(&l.ID, &l.Name, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Ledger{}, ledger.ErrLedgerNotFound
	}
	if err != nil {
		return ledger.Ledger{}, fmt.Errorf("pg: get ledger %s: %w", id, err)
	}
	l.CreatedAt = readTime(createdAt)
	return l, nil
}

func (t *tx) ListLedgers(ctx context.Context, book ledger.BookID) ([]ledger.Ledger, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT id, name, created_at FROM ledgers WHERE book_id = $1
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("pg: list ledgers: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.Ledger, 0)
	for rows.Next() {
		var l ledger.Ledger
		var createdAt *time.Time
		if err := rows.Scan(&l.ID, &l.Name, &createdAt); err != nil {
			return nil, fmt.Errorf("pg: list ledgers: %w", err)
		}
		l.CreatedAt = readTime(createdAt)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Subledgers
// ---------------------------------------------------------------------------

func (t *tx) PutSubledger(ctx context.Context, book ledger.BookID, sl ledger.Subledger) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO subledgers (book_id, id, ledger_id, name, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (book_id, id) DO UPDATE
		SET ledger_id = EXCLUDED.ledger_id, name = EXCLUDED.name, created_at = EXCLUDED.created_at`,
		string(book), string(sl.ID), string(sl.LedgerID), sl.Name, nullTime(sl.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put subledger %s: %w", sl.ID, err)
	}
	return nil
}

func (t *tx) GetSubledger(ctx context.Context, book ledger.BookID, id ledger.SubledgerID) (ledger.Subledger, error) {
	var sl ledger.Subledger
	var createdAt *time.Time
	err := t.tx.QueryRow(ctx,
		"SELECT id, ledger_id, name, created_at FROM subledgers WHERE book_id = $1 AND id = $2",
		string(book), string(id)).Scan(&sl.ID, &sl.LedgerID, &sl.Name, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Subledger{}, ledger.ErrSubledgerNotFound
	}
	if err != nil {
		return ledger.Subledger{}, fmt.Errorf("pg: get subledger %s: %w", id, err)
	}
	sl.CreatedAt = readTime(createdAt)
	return sl, nil
}

func (t *tx) ListSubledgers(ctx context.Context, book ledger.BookID) ([]ledger.Subledger, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT id, ledger_id, name, created_at FROM subledgers WHERE book_id = $1
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("pg: list subledgers: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.Subledger, 0)
	for rows.Next() {
		var sl ledger.Subledger
		var createdAt *time.Time
		if err := rows.Scan(&sl.ID, &sl.LedgerID, &sl.Name, &createdAt); err != nil {
			return nil, fmt.Errorf("pg: list subledgers: %w", err)
		}
		sl.CreatedAt = readTime(createdAt)
		out = append(out, sl)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

func (t *tx) PutAccount(ctx context.Context, book ledger.BookID, a ledger.Account) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO accounts (book_id, id, subledger_id, name, type, created_at) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (book_id, id) DO UPDATE
		SET subledger_id = EXCLUDED.subledger_id, name = EXCLUDED.name,
		    type = EXCLUDED.type, created_at = EXCLUDED.created_at`,
		string(book), string(a.ID), string(a.SubledgerID), a.Name, int16(a.Type), nullTime(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("pg: put account %s: %w", a.ID, err)
	}
	return nil
}

func (t *tx) GetAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) (ledger.Account, error) {
	var a ledger.Account
	var typ int16
	var createdAt *time.Time
	err := t.tx.QueryRow(ctx,
		"SELECT id, subledger_id, name, type, created_at FROM accounts WHERE book_id = $1 AND id = $2",
		string(book), string(id)).Scan(&a.ID, &a.SubledgerID, &a.Name, &typ, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Account{}, ledger.ErrAccountNotFound
	}
	if err != nil {
		return ledger.Account{}, fmt.Errorf("pg: get account %s: %w", id, err)
	}
	a.Type = ledger.AccountType(typ)
	a.CreatedAt = readTime(createdAt)
	return a, nil
}

func (t *tx) ListAccounts(ctx context.Context, book ledger.BookID) ([]ledger.Account, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT id, subledger_id, name, type, created_at FROM accounts WHERE book_id = $1
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("pg: list accounts: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.Account, 0)
	for rows.Next() {
		var a ledger.Account
		var typ int16
		var createdAt *time.Time
		if err := rows.Scan(&a.ID, &a.SubledgerID, &a.Name, &typ, &createdAt); err != nil {
			return nil, fmt.Errorf("pg: list accounts: %w", err)
		}
		a.Type = ledger.AccountType(typ)
		a.CreatedAt = readTime(createdAt)
		out = append(out, a)
	}
	return out, rows.Err()
}

// LockAccounts takes a row-level write lock on the given accounts.
//
// This is the first of the three races store/mem's single mutex hid. A balance
// check and the posting that depends on it must be one serialized step, or two
// concurrent transactions each see enough funds and both post.
//
// ORDER BY id is not cosmetic: it is what stops two transactions with
// overlapping account sets from each holding the row the other needs. Every
// transaction takes the locks in the same order, so one of them simply waits.
func (t *tx) LockAccounts(ctx context.Context, book ledger.BookID, ids []ledger.AccountID) error {
	if err := t.write(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = string(id)
	}
	rows, err := t.tx.Query(ctx,
		"SELECT id FROM accounts WHERE book_id = $1 AND id = ANY($2::text[]) ORDER BY id FOR UPDATE",
		string(book), keys)
	if err != nil {
		return fmt.Errorf("pg: lock accounts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		// The rows themselves are not wanted; draining the result set is what
		// makes the locks taken.
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("pg: lock accounts: %w", err)
		}
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------------

// transactionColumns is the projection every transaction read shares. Entries
// come from a LEFT JOIN so a transaction with no entries still appears.
const transactionColumns = `
	t.id, t.idempotency_key, t.booking_date, t.value_date, t.status,
	t.description, t.metadata, t.reversal_of, t.created_at,
	e.id, e.account_id, e.amount, e.direction`

// PutTransaction stores a transaction, its ordered entries and its idempotency
// claim.
//
// There is no check-then-insert. The partial unique index on
// (book_id, idempotency_key) is the check, and SQLSTATE 23505 on it becomes
// ledger.ErrDuplicateIdempotencyKey — which is the second of the three races:
// two concurrent postings with the same key cannot both pass a SELECT that
// found nothing, because there is no such SELECT.
func (t *tx) PutTransaction(ctx context.Context, book ledger.BookID, txn ledger.Transaction) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	metadata, err := marshalStringMap(txn.Metadata)
	if err != nil {
		return fmt.Errorf("pg: put transaction %s: %w", txn.ID, err)
	}

	_, err = t.tx.Exec(ctx, `
		INSERT INTO transactions
			(book_id, id, idempotency_key, booking_date, value_date, status,
			 description, metadata, reversal_of, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (book_id, id) DO UPDATE SET
			idempotency_key = EXCLUDED.idempotency_key,
			booking_date    = EXCLUDED.booking_date,
			value_date      = EXCLUDED.value_date,
			status          = EXCLUDED.status,
			description     = EXCLUDED.description,
			metadata        = EXCLUDED.metadata,
			reversal_of     = EXCLUDED.reversal_of,
			created_at      = EXCLUDED.created_at`,
		string(book), string(txn.ID), txn.IdempotencyKey,
		nullTime(txn.BookingDate), nullTime(txn.ValueDate), int16(txn.Status),
		txn.Description, metadata, string(txn.ReversalOf), nullTime(txn.CreatedAt))
	if isUniqueViolationOn(err, "transactions_idempotency_key_idx") {
		return ledger.ErrDuplicateIdempotencyKey
	}
	if err != nil {
		return fmt.Errorf("pg: put transaction %s: %w", txn.ID, err)
	}

	// The upsert replaces the whole entry list, so a re-put cannot leave a
	// stale leg behind.
	if _, err := t.tx.Exec(ctx,
		"DELETE FROM entries WHERE book_id = $1 AND transaction_id = $2",
		string(book), string(txn.ID)); err != nil {
		return fmt.Errorf("pg: put transaction %s: %w", txn.ID, err)
	}
	for i, e := range txn.Entries {
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO entries (book_id, transaction_id, position, id, account_id, amount, direction)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			string(book), string(txn.ID), i, string(e.ID), string(e.AccountID), e.Amount, int16(e.Direction),
		); err != nil {
			return fmt.Errorf("pg: put transaction %s entry %d: %w", txn.ID, i, err)
		}
	}
	return nil
}

func (t *tx) GetTransaction(ctx context.Context, book ledger.BookID, id ledger.TransactionID) (ledger.Transaction, error) {
	out, err := t.queryTransactions(ctx, `
		SELECT `+transactionColumns+`
		FROM transactions t
		LEFT JOIN entries e ON e.book_id = t.book_id AND e.transaction_id = t.id
		WHERE t.book_id = $1 AND t.id = $2
		ORDER BY e.position`, string(book), string(id))
	if err != nil {
		return ledger.Transaction{}, err
	}
	if len(out) == 0 {
		return ledger.Transaction{}, ledger.ErrTransactionNotFound
	}
	return out[0], nil
}

func (t *tx) GetTransactionByIdempotencyKey(ctx context.Context, book ledger.BookID, key string) (ledger.Transaction, error) {
	// An empty key is an absent key, never an identity, so it is not even
	// looked up — the index does not contain it either.
	if key == "" {
		return ledger.Transaction{}, ledger.ErrTransactionNotFound
	}
	out, err := t.queryTransactions(ctx, `
		SELECT `+transactionColumns+`
		FROM transactions t
		LEFT JOIN entries e ON e.book_id = t.book_id AND e.transaction_id = t.id
		WHERE t.book_id = $1 AND t.idempotency_key = $2
		ORDER BY e.position`, string(book), key)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if len(out) == 0 {
		return ledger.Transaction{}, ledger.ErrTransactionNotFound
	}
	return out[0], nil
}

func (t *tx) ListTransactions(ctx context.Context, book ledger.BookID) ([]ledger.Transaction, error) {
	return t.queryTransactions(ctx, `
		SELECT `+transactionColumns+`
		FROM transactions t
		LEFT JOIN entries e ON e.book_id = t.book_id AND e.transaction_id = t.id
		WHERE t.book_id = $1
		ORDER BY t.created_at ASC NULLS FIRST, t.seq, e.position`, string(book))
}

// ListTransactionsForAccount returns every transaction with a leg on the
// account — with ALL of its legs, not only the matching ones, which is why the
// account predicate is an EXISTS rather than a join condition.
func (t *tx) ListTransactionsForAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) ([]ledger.Transaction, error) {
	return t.queryTransactions(ctx, `
		SELECT `+transactionColumns+`
		FROM transactions t
		LEFT JOIN entries e ON e.book_id = t.book_id AND e.transaction_id = t.id
		WHERE t.book_id = $1 AND EXISTS (
			SELECT 1 FROM entries x
			WHERE x.book_id = t.book_id AND x.transaction_id = t.id AND x.account_id = $2
		)
		ORDER BY t.created_at ASC NULLS FIRST, t.seq, e.position`, string(book), string(id))
}

// queryTransactions runs a transactions-joined-to-entries query and folds the
// flattened rows back into transactions with ordered entry slices.
func (t *tx) queryTransactions(ctx context.Context, query string, args ...any) ([]ledger.Transaction, error) {
	rows, err := t.tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pg: query transactions: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.Transaction, 0)
	index := make(map[ledger.TransactionID]int)
	for rows.Next() {
		var (
			txn                    ledger.Transaction
			status                 int16
			booking, value, create *time.Time
			metadata               []byte
			entryID, accountID     *string
			amount                 *int64
			direction              *int16
		)
		if err := rows.Scan(
			&txn.ID, &txn.IdempotencyKey, &booking, &value, &status,
			&txn.Description, &metadata, &txn.ReversalOf, &create,
			&entryID, &accountID, &amount, &direction,
		); err != nil {
			return nil, fmt.Errorf("pg: query transactions: %w", err)
		}

		at, seen := index[txn.ID]
		if !seen {
			txn.Status = ledger.TransactionStatus(status)
			txn.BookingDate = readTime(booking)
			txn.ValueDate = readTime(value)
			txn.CreatedAt = readTime(create)
			if txn.Metadata, err = unmarshalStringMap(metadata); err != nil {
				return nil, fmt.Errorf("pg: transaction %s metadata: %w", txn.ID, err)
			}
			at = len(out)
			index[txn.ID] = at
			out = append(out, txn)
		}
		if entryID != nil {
			out[at].Entries = append(out[at].Entries, ledger.Entry{
				ID:        ledger.EntryID(*entryID),
				AccountID: ledger.AccountID(*accountID),
				Amount:    *amount,
				Direction: ledger.Direction(*direction),
			})
		}
	}
	return out, rows.Err()
}

// MarkReversed flips a transaction from Posted to Reversed, or fails.
//
// This is the third of the three races. The UPDATE carries its own precondition,
// so two concurrent reversals of the same transaction cannot both find it Posted
// and both win: whichever runs second affects zero rows. The follow-up SELECT
// only decides which error to report; it is not part of the decision.
func (t *tx) MarkReversed(ctx context.Context, book ledger.BookID, id ledger.TransactionID) error {
	if err := t.write(); err != nil {
		return err
	}
	tag, err := t.tx.Exec(ctx,
		"UPDATE transactions SET status = $3 WHERE book_id = $1 AND id = $2 AND status = $4",
		string(book), string(id), int16(ledger.Reversed), int16(ledger.Posted))
	if err != nil {
		return fmt.Errorf("pg: mark reversed %s: %w", id, err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	var exists bool
	if err := t.tx.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM transactions WHERE book_id = $1 AND id = $2)",
		string(book), string(id)).Scan(&exists); err != nil {
		return fmt.Errorf("pg: mark reversed %s: %w", id, err)
	}
	if !exists {
		return ledger.ErrTransactionNotFound
	}
	return ledger.ErrTransactionAlreadyReversed
}

// BookBalance aggregates an account's entries in SQL rather than replaying them
// in Go: entries in the account's normal direction add, the rest subtract.
//
// ALL transactions count, Reversed ones included. The Reversed status is
// informational — a reversal posts its own mirrored entries, and those are what
// cancel the original. Filtering reversed transactions out here would
// double-count the correction.
//
// Like every aggregate, an account with no entries is zero, including one that
// does not exist; callers wanting ErrAccountNotFound read the account first.
func (t *tx) BookBalance(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction) (ledger.Amount, error) {
	var balance ledger.Amount
	err := t.tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN e.direction = $3 THEN e.amount ELSE -e.amount END), 0)
		FROM entries e WHERE e.book_id = $1 AND e.account_id = $2`,
		string(book), string(id), int16(normal)).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("pg: book balance %s: %w", id, err)
	}
	return balance, nil
}
