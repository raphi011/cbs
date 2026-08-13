package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/raphi011/cbs/ledger"
)

// tx is one unit of work: one SQLite transaction on one pooled connection.
type tx struct {
	store    *store
	tx       *sql.Tx
	readOnly bool

	// books remembers which books this transaction has already inserted a row
	// for, so the FK-parent insert happens once per book per transaction rather
	// than on every write.
	books map[ledger.BookID]struct{}
}

// compile-time check that tx satisfies the interface it is written against.
var _ ledger.Tx = (*tx)(nil)

// write reports whether this transaction may mutate. The SQLite transaction is
// opened read-only as well, but failing here first means the answer is a named
// sentinel rather than a driver error whose text is the driver's to change.
func (t *tx) write() error {
	if t.readOnly {
		return ErrReadOnly
	}
	return nil
}

// own reports whether this store answers for the given book, and it is called
// FIRST in every method that takes one — before the read-only check, before the
// books row is ensured, before any statement runs.
func (t *tx) own(book ledger.BookID) error {
	if book == t.store.book {
		return nil
	}
	return fmt.Errorf("%w: this is %s's store, and %q is somebody else's book",
		ErrNotThisStoresBook, t.store.book, book)
}

// ensureBook creates the books row every book-scoped table's foreign key points
// at.
func (t *tx) ensureBook(ctx context.Context, book ledger.BookID) error {
	if err := t.own(book); err != nil {
		return err
	}
	if _, ok := t.books[book]; ok {
		return nil
	}
	if _, err := t.tx.ExecContext(ctx,
		"INSERT INTO books (id) VALUES (?) ON CONFLICT (id) DO NOTHING", string(book),
	); err != nil {
		return fmt.Errorf("sqlite: ensure book %s: %w", book, err)
	}
	t.books[book] = struct{}{}
	return nil
}

// nextRowSeq is the expression every INSERT uses for its `seq` column.
func nextRowSeq(table string) string {
	return "(SELECT COALESCE(MAX(seq), 0) + 1 FROM " + table + ")"
}

// placeholders renders "?, ?, ?" for an IN list.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// ---------------------------------------------------------------------------
// Identity allocation
// ---------------------------------------------------------------------------

// nextSeq allocates the next value of one counter, gap-free.
func (t *tx) nextSeq(ctx context.Context, book ledger.BookID, name string) (int64, error) {
	if err := t.own(book); err != nil {
		return 0, err
	}
	if err := t.write(); err != nil {
		return 0, err
	}
	var next int64
	err := t.tx.QueryRowContext(ctx, `
		INSERT INTO id_sequences (book_id, name, next_value) VALUES (?, ?, 1)
		ON CONFLICT (book_id, name) DO UPDATE SET next_value = id_sequences.next_value + 1
		RETURNING next_value`, string(book), name).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("sqlite: next %s for %s: %w", name, book, err)
	}
	return next, nil
}

// NextID issues the next ID with the given prefix.
func (t *tx) NextID(ctx context.Context, book ledger.BookID, prefix string) (string, error) {
	if err := t.own(book); err != nil {
		return "", err
	}
	n, err := t.nextSeq(ctx, book, "id")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%d", prefix, n), nil
}

// NextSubledgerBlock issues the next chart-of-accounts block for a book: 100,
// 200, 300, …
func (t *tx) NextSubledgerBlock(ctx context.Context, book ledger.BookID) (int, error) {
	if err := t.own(book); err != nil {
		return 0, err
	}
	n, err := t.nextSeq(ctx, book, "subledger_block")
	if err != nil {
		return 0, err
	}
	return int(n) * 100, nil
}

// NextAccountSeq issues the next account number within one
// "<typeBlock>.<subledgerID>" branch of a book, restarting at 1 per branch.
func (t *tx) NextAccountSeq(ctx context.Context, book ledger.BookID, typeBlock int, subledger ledger.SubledgerID) (int, error) {
	if err := t.own(book); err != nil {
		return 0, err
	}
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
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	// seq is absent from the DO UPDATE list on purpose: an upsert keeps the row
	// where it already was in its listing.
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO ledgers (book_id, id, name, created_at, seq)
		VALUES (?, ?, ?, ?, `+nextRowSeq("ledgers")+`)
		ON CONFLICT (book_id, id) DO UPDATE SET name = EXCLUDED.name, created_at = EXCLUDED.created_at`,
		string(book), string(l.ID), l.Name, nullTime{l.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put ledger %s: %w", l.ID, err)
	}
	return nil
}

func (t *tx) GetLedger(ctx context.Context, book ledger.BookID, id ledger.LedgerID) (ledger.Ledger, error) {
	if err := t.own(book); err != nil {
		return ledger.Ledger{}, err
	}
	var l ledger.Ledger
	var createdAt nullTime
	err := t.tx.QueryRowContext(ctx,
		"SELECT id, name, created_at FROM ledgers WHERE book_id = ? AND id = ?",
		string(book), string(id)).Scan(&l.ID, &l.Name, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Ledger{}, ledger.ErrLedgerNotFound
	}
	if err != nil {
		return ledger.Ledger{}, fmt.Errorf("sqlite: get ledger %s: %w", id, err)
	}
	l.CreatedAt = createdAt.Time
	return l, nil
}

func (t *tx) ListLedgers(ctx context.Context, book ledger.BookID) ([]ledger.Ledger, error) {
	if err := t.own(book); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT id, name, created_at FROM ledgers WHERE book_id = ?
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list ledgers: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.Ledger, 0)
	for rows.Next() {
		var l ledger.Ledger
		var createdAt nullTime
		if err := rows.Scan(&l.ID, &l.Name, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: list ledgers: %w", err)
		}
		l.CreatedAt = createdAt.Time
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Subledgers
// ---------------------------------------------------------------------------

func (t *tx) PutSubledger(ctx context.Context, book ledger.BookID, sl ledger.Subledger) error {
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO subledgers (book_id, id, ledger_id, name, created_at, seq)
		VALUES (?, ?, ?, ?, ?, `+nextRowSeq("subledgers")+`)
		ON CONFLICT (book_id, id) DO UPDATE
		SET ledger_id = EXCLUDED.ledger_id, name = EXCLUDED.name, created_at = EXCLUDED.created_at`,
		string(book), string(sl.ID), string(sl.LedgerID), sl.Name, nullTime{sl.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put subledger %s: %w", sl.ID, err)
	}
	return nil
}

func (t *tx) GetSubledger(ctx context.Context, book ledger.BookID, id ledger.SubledgerID) (ledger.Subledger, error) {
	if err := t.own(book); err != nil {
		return ledger.Subledger{}, err
	}
	var sl ledger.Subledger
	var createdAt nullTime
	err := t.tx.QueryRowContext(ctx,
		"SELECT id, ledger_id, name, created_at FROM subledgers WHERE book_id = ? AND id = ?",
		string(book), string(id)).Scan(&sl.ID, &sl.LedgerID, &sl.Name, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Subledger{}, ledger.ErrSubledgerNotFound
	}
	if err != nil {
		return ledger.Subledger{}, fmt.Errorf("sqlite: get subledger %s: %w", id, err)
	}
	sl.CreatedAt = createdAt.Time
	return sl, nil
}

func (t *tx) ListSubledgers(ctx context.Context, book ledger.BookID) ([]ledger.Subledger, error) {
	if err := t.own(book); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT id, ledger_id, name, created_at FROM subledgers WHERE book_id = ?
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list subledgers: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.Subledger, 0)
	for rows.Next() {
		var sl ledger.Subledger
		var createdAt nullTime
		if err := rows.Scan(&sl.ID, &sl.LedgerID, &sl.Name, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: list subledgers: %w", err)
		}
		sl.CreatedAt = createdAt.Time
		out = append(out, sl)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

func (t *tx) PutAccount(ctx context.Context, book ledger.BookID, a ledger.Account) error {
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO accounts (book_id, id, subledger_id, name, type, asset, control, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("accounts")+`)
		ON CONFLICT (book_id, id) DO UPDATE
		SET subledger_id = EXCLUDED.subledger_id, name = EXCLUDED.name,
		    type = EXCLUDED.type, asset = EXCLUDED.asset, control = EXCLUDED.control,
		    created_at = EXCLUDED.created_at`,
		string(book), string(a.ID), string(a.SubledgerID), a.Name, int64(a.Type), string(a.Asset),
		a.Control, nullTime{a.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put account %s: %w", a.ID, err)
	}
	return nil
}

func (t *tx) GetAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) (ledger.Account, error) {
	if err := t.own(book); err != nil {
		return ledger.Account{}, err
	}
	var a ledger.Account
	var typ int64
	var createdAt nullTime
	err := t.tx.QueryRowContext(ctx,
		"SELECT id, subledger_id, name, type, asset, control, created_at FROM accounts WHERE book_id = ? AND id = ?",
		string(book), string(id)).Scan(&a.ID, &a.SubledgerID, &a.Name, &typ, &a.Asset, &a.Control, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Account{}, ledger.ErrAccountNotFound
	}
	if err != nil {
		return ledger.Account{}, fmt.Errorf("sqlite: get account %s: %w", id, err)
	}
	a.Type = ledger.AccountType(typ)
	a.CreatedAt = createdAt.Time
	return a, nil
}

func (t *tx) PutSlotAccount(ctx context.Context, book ledger.BookID, row ledger.SlotAccount) error {
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO slot_accounts (book_id, product, slot, asset, account_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (book_id, product, slot, asset) DO UPDATE
		SET account_id = EXCLUDED.account_id`,
		string(book), row.Product, row.Slot, string(row.Asset), string(row.Account))
	if err != nil {
		return fmt.Errorf("sqlite: put slot account %s/%s: %w", row.Slot, row.Asset, err)
	}
	return nil
}

func (t *tx) GetSlotAccount(ctx context.Context, book ledger.BookID, product, slot string, asset ledger.AssetCode) (ledger.AccountID, error) {
	if err := t.own(book); err != nil {
		return "", err
	}
	var account ledger.AccountID
	err := t.tx.QueryRowContext(ctx, `
		SELECT account_id FROM slot_accounts
		WHERE book_id = ? AND product = ? AND slot = ? AND asset = ?`,
		string(book), product, slot, string(asset)).Scan(&account)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s/%s for product %q", ledger.ErrSlotNotMapped, slot, asset, product)
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: get slot account %s/%s: %w", slot, asset, err)
	}
	return account, nil
}

func (t *tx) ListSlotAccounts(ctx context.Context, book ledger.BookID) ([]ledger.SlotAccount, error) {
	if err := t.own(book); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT product, slot, asset, account_id FROM slot_accounts WHERE book_id = ?
		ORDER BY slot, product, asset`, string(book))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list slot accounts: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.SlotAccount, 0)
	for rows.Next() {
		var row ledger.SlotAccount
		if err := rows.Scan(&row.Product, &row.Slot, &row.Asset, &row.Account); err != nil {
			return nil, fmt.Errorf("sqlite: list slot accounts: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list slot accounts: %w", err)
	}
	return out, nil
}

func (t *tx) ListAccounts(ctx context.Context, book ledger.BookID) ([]ledger.Account, error) {
	if err := t.own(book); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT id, subledger_id, name, type, asset, control, created_at FROM accounts WHERE book_id = ?
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list accounts: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.Account, 0)
	for rows.Next() {
		var a ledger.Account
		var typ int64
		var createdAt nullTime
		if err := rows.Scan(&a.ID, &a.SubledgerID, &a.Name, &typ, &a.Asset, &a.Control, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: list accounts: %w", err)
		}
		a.Type = ledger.AccountType(typ)
		a.CreatedAt = createdAt.Time
		out = append(out, a)
	}
	return out, rows.Err()
}

// LockAccounts is a no-op.
func (t *tx) LockAccounts(ctx context.Context, book ledger.BookID, ids []ledger.AccountID) error {
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------------

// transactionColumns is the projection every transaction read shares. Entries
// come from a LEFT JOIN so a transaction with no entries still appears.
const transactionColumns = `
	t.id, t.idempotency_key, t.booking_date, t.value_date, t.status,
	t.description, t.metadata, t.reversal_of, t.created_at,
	e.id, e.account_id, e.subsidiary_id, e.amount, e.direction, e.value_date`

// PutTransaction stores a transaction, its ordered entries and its idempotency
// claim.
func (t *tx) PutTransaction(ctx context.Context, book ledger.BookID, txn ledger.Transaction) error {
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	metadata, err := marshalStringMap(txn.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: put transaction %s: %w", txn.ID, err)
	}

	_, err = t.tx.ExecContext(ctx, `
		INSERT INTO transactions
			(book_id, id, idempotency_key, booking_date, value_date, status,
			 description, metadata, reversal_of, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("transactions")+`)
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
		nullTime{txn.BookingDate}, nullTime{txn.ValueDate}, int64(txn.Status),
		txn.Description, metadata, string(txn.ReversalOf), nullTime{txn.CreatedAt})
	if isUniqueViolation(err) {
		return ledger.ErrDuplicateIdempotencyKey
	}
	if err != nil {
		return fmt.Errorf("sqlite: put transaction %s: %w", txn.ID, err)
	}

	// The upsert replaces the whole entry list, so a re-put cannot leave a
	// stale leg behind.
	if _, err := t.tx.ExecContext(ctx,
		"DELETE FROM entries WHERE book_id = ? AND transaction_id = ?",
		string(book), string(txn.ID)); err != nil {
		return fmt.Errorf("sqlite: put transaction %s: %w", txn.ID, err)
	}
	for i, e := range txn.Entries {
		if _, err := t.tx.ExecContext(ctx, `
			INSERT INTO entries (book_id, transaction_id, position, id, account_id, subsidiary_id, amount, direction, value_date)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(book), string(txn.ID), i, string(e.ID), string(e.AccountID), e.Subsidiary,
			int64(e.Amount), int64(e.Direction), nullTime{e.ValueDate},
		); err != nil {
			return fmt.Errorf("sqlite: put transaction %s entry %d: %w", txn.ID, i, err)
		}
	}
	return nil
}

func (t *tx) GetTransaction(ctx context.Context, book ledger.BookID, id ledger.TransactionID) (ledger.Transaction, error) {
	if err := t.own(book); err != nil {
		return ledger.Transaction{}, err
	}
	out, err := t.queryTransactions(ctx, `
		SELECT `+transactionColumns+`
		FROM transactions t
		LEFT JOIN entries e ON e.book_id = t.book_id AND e.transaction_id = t.id
		WHERE t.book_id = ? AND t.id = ?
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
	if err := t.own(book); err != nil {
		return ledger.Transaction{}, err
	}
	// An empty key is an absent key, never an identity, so it is not even
	// looked up — the index does not contain it either.
	if key == "" {
		return ledger.Transaction{}, ledger.ErrTransactionNotFound
	}
	out, err := t.queryTransactions(ctx, `
		SELECT `+transactionColumns+`
		FROM transactions t
		LEFT JOIN entries e ON e.book_id = t.book_id AND e.transaction_id = t.id
		WHERE t.book_id = ? AND t.idempotency_key = ?
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
	if err := t.own(book); err != nil {
		return nil, err
	}
	return t.queryTransactions(ctx, `
		SELECT `+transactionColumns+`
		FROM transactions t
		LEFT JOIN entries e ON e.book_id = t.book_id AND e.transaction_id = t.id
		WHERE t.book_id = ?
		ORDER BY t.created_at ASC NULLS FIRST, t.seq, e.position`, string(book))
}

// ListTransactionsForPosition returns every transaction with a leg on the
// position — with ALL of its legs, not only the matching ones, which is why the
// position predicate is an EXISTS rather than a join condition.
func (t *tx) ListTransactionsForPosition(ctx context.Context, book ledger.BookID, pos ledger.Position) ([]ledger.Transaction, error) {
	if err := t.own(book); err != nil {
		return nil, err
	}
	clause := ""
	args := []any{string(book), string(pos.Account)}
	if pos.Subsidiary != "" {
		clause = " AND x.subsidiary_id = ?"
		args = append(args, pos.Subsidiary)
	}
	return t.queryTransactions(ctx, `
		SELECT `+transactionColumns+`
		FROM transactions t
		LEFT JOIN entries e ON e.book_id = t.book_id AND e.transaction_id = t.id
		WHERE t.book_id = ? AND EXISTS (
			SELECT 1 FROM entries x
			WHERE x.book_id = t.book_id AND x.transaction_id = t.id AND x.account_id = ?`+clause+`
		)
		ORDER BY t.created_at ASC NULLS FIRST, t.seq, e.position`, args...)
}

// queryTransactions runs a transactions-joined-to-entries query and folds the
// flattened rows back into transactions with ordered entry slices.
func (t *tx) queryTransactions(ctx context.Context, query string, args ...any) ([]ledger.Transaction, error) {
	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: query transactions: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.Transaction, 0)
	index := make(map[ledger.TransactionID]int)
	for rows.Next() {
		var (
			txn                    ledger.Transaction
			status                 int64
			booking, value, create nullTime
			metadata               []byte
			// The entry half of the LEFT JOIN is NULL for a transaction with no
			// entries, which is a legal transaction here rather than an anomaly.
			entryID, accountID, subsidiary sql.NullString
			amount, direction              sql.NullInt64
			entryValue                     nullTime
		)
		if err := rows.Scan(
			&txn.ID, &txn.IdempotencyKey, &booking, &value, &status,
			&txn.Description, &metadata, &txn.ReversalOf, &create,
			&entryID, &accountID, &subsidiary, &amount, &direction, &entryValue,
		); err != nil {
			return nil, fmt.Errorf("sqlite: query transactions: %w", err)
		}

		at, seen := index[txn.ID]
		if !seen {
			txn.Status = ledger.TransactionStatus(status)
			txn.BookingDate = booking.Time
			txn.ValueDate = value.Time
			txn.CreatedAt = create.Time
			if txn.Metadata, err = unmarshalStringMap(metadata); err != nil {
				return nil, fmt.Errorf("sqlite: transaction %s metadata: %w", txn.ID, err)
			}
			at = len(out)
			index[txn.ID] = at
			out = append(out, txn)
		}
		if entryID.Valid {
			out[at].Entries = append(out[at].Entries, ledger.Entry{
				ID:         ledger.EntryID(entryID.String),
				AccountID:  ledger.AccountID(accountID.String),
				Subsidiary: subsidiary.String,
				Amount:     ledger.Amount(amount.Int64),
				Direction:  ledger.Direction(direction.Int64),
				ValueDate:  entryValue.Time,
			})
		}
	}
	return out, rows.Err()
}

// MarkReversed flips a transaction from Posted to Reversed, or fails.
func (t *tx) MarkReversed(ctx context.Context, book ledger.BookID, id ledger.TransactionID) error {
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	res, err := t.tx.ExecContext(ctx,
		"UPDATE transactions SET status = ? WHERE book_id = ? AND id = ? AND status = ?",
		int64(ledger.Reversed), string(book), string(id), int64(ledger.Posted))
	if err != nil {
		return fmt.Errorf("sqlite: mark reversed %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: mark reversed %s: %w", id, err)
	}
	if affected > 0 {
		return nil
	}

	var exists bool
	if err := t.tx.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM transactions WHERE book_id = ? AND id = ?)",
		string(book), string(id)).Scan(&exists); err != nil {
		return fmt.Errorf("sqlite: mark reversed %s: %w", id, err)
	}
	if !exists {
		return ledger.ErrTransactionNotFound
	}
	return ledger.ErrTransactionAlreadyReversed
}

// subsidiaryClause is the subsidiary half of a position's entry predicate, and
// the one place in this store where "an empty Subsidiary means the whole
// account" is written down.
func subsidiaryClause(pos ledger.Position) (string, []any) {
	if pos.Subsidiary == "" {
		return "", nil
	}
	return " AND e.subsidiary_id = ?", []any{pos.Subsidiary}
}

// ScanEntries streams a position's entries. entries_account_idx serves the
// position and the value-date bounds narrow within it: every clause built here
// is a column of that index. See ledger.Tx for the contract.
func (t *tx) ScanEntries(ctx context.Context, book ledger.BookID, pos ledger.Position, f ledger.EntryFilter) iter.Seq2[ledger.Entry, error] {
	return func(yield func(ledger.Entry, error) bool) {
		if err := t.own(book); err != nil {
			yield(ledger.Entry{}, err)
			return
		}
		fail := func(err error) {
			yield(ledger.Entry{}, fmt.Errorf("sqlite: scan entries %s: %w", pos, err))
		}

		clause, extra := subsidiaryClause(pos)
		args := append([]any{string(book), string(pos.Account)}, extra...)
		// A NULL value date compares to neither bound, so an undated entry falls
		// out of a bounded scan and stays in an unbounded one.
		if !f.From.IsZero() {
			clause += " AND e.value_date >= ?"
			args = append(args, formatTime(f.From))
		}
		if !f.To.IsZero() {
			clause += " AND e.value_date < ?"
			args = append(args, formatTime(f.To))
		}

		// account_id is not selected: the WHERE clause fixed it, so reading it back
		// is one TEXT column per row to learn what the caller supplied.
		rows, err := t.tx.QueryContext(ctx, `
			SELECT e.id, e.subsidiary_id, e.amount, e.direction, e.value_date
			FROM entries e WHERE e.book_id = ? AND e.account_id = ?`+clause,
			args...)
		if err != nil {
			fail(err)
			return
		}
		defer rows.Close()

		for rows.Next() {
			var (
				e         ledger.Entry
				direction int64
				valueDate nullTime
			)
			if err := rows.Scan(&e.ID, &e.Subsidiary, &e.Amount, &direction, &valueDate); err != nil {
				fail(err)
				return
			}
			e.AccountID = pos.Account
			e.Direction = ledger.Direction(direction)
			e.ValueDate = valueDate.Time
			if !yield(e, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			fail(err)
		}
	}
}
