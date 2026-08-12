package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// tx is one unit of work: one SQLite transaction on one pooled connection.
//
// ONE value implements every transaction interface in this system — ledger.Tx
// and ledger.BankTx, deposit.Tx, product.Tx, lending.Tx, ebics.Tx, and all three
// of payment.BankTx, payment.CsmTx and payment.CentralBankTx. Which of them a
// caller may NAME is decided by the store it was handed (see BankStore and the
// two beside it); what it is handed is always this, so a bank's deposit write
// and the GL posting under it are one BEGIN … COMMIT.
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

// Now returns the store's current time.
func (t *tx) Now() time.Time { return t.store.clock() }

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
//
// The ordering is the whole value of it. A foreign book otherwise produces a
// silent not-found: the statement runs, matches nothing, and the caller gets an
// empty listing or a "ledger not found" several layers away from the institution
// that had no business asking. Putting the guard after anything at all leaves a
// path on which that still happens — after write(), a View still sweeps another
// institution's book; after ensureBook, the store has already INSERTED a row for
// it, which is a foreign book being created rather than refused.
//
// See ErrNotThisStoresBook for what this catches and what stays the recorder's.
func (t *tx) own(book ledger.BookID) error {
	if book == t.store.book {
		return nil
	}
	return fmt.Errorf("%w: this is %s's store, and %q is somebody else's book",
		ErrNotThisStoresBook, t.store.book, book)
}

// inShape reports whether this store's schema holds the named table, and it is
// called first in every method whose table is not in all three shapes.
//
// The table name is passed rather than derived, because a method can name more
// than one and the one worth refusing on is the one it is ABOUT: PutCycle writes
// cycles and cycle_payments, and a store without cycles has neither, so naming
// the parent is enough and naming both would be a list to keep in step.
//
// See ErrNotInThisShape for why this is a sentinel rather than the "no such
// table" the driver would otherwise produce.
func (t *tx) inShape(table string) error {
	if _, ok := t.store.shape.holds[table]; ok {
		return nil
	}
	return fmt.Errorf("%w: this is the %s shape and it has no %s table",
		ErrNotInThisShape, t.store.shape, table)
}

// ensureBook creates the books row every book-scoped table's foreign key points
// at. Books are not an entity the domain creates — a BookID is simply a name a
// participant is written under — so the row appears the first time something is
// written under it.
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
//
// SQLite's only automatic counter rides on an INTEGER PRIMARY KEY, and seq is
// not the key on any book-scoped table, so the allocation is explicit — and
// explicit is what the schema wanted anyway: MAX+1 inside the writing
// transaction rolls back with it, where a sequence deliberately would not. See
// ledgers.seq, where the argument is recorded.
//
// It goes in the VALUES list and never in the ON CONFLICT branch, which is what
// keeps an upsert from moving an edited row to the end of its listing.
//
// MAX+1 is safe without further thought because SQLite admits one writer: two
// transactions cannot both be allocating. It is also why Reset needs no
// RESTART IDENTITY — the counter is derived from the rows, so emptying the table
// restarts it.
func nextRowSeq(table string) string {
	return "(SELECT COALESCE(MAX(seq), 0) + 1 FROM " + table + ")"
}

// placeholders renders "?, ?, ?" for an IN list.
//
// SQLite has no array type, so an IN list is built to fit the argument count.
// Every caller here builds it from a slice it owns rather than from anything a
// request supplied.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// ---------------------------------------------------------------------------
// Identity allocation
// ---------------------------------------------------------------------------

// nextSeq allocates the next value of one counter, gap-free.
//
// The counter is an ordinary row, so the allocation rolls back with the caller's
// transaction. It is also the serialization point several comments in the schema
// point at: writing it makes this transaction the database's writer, so a second
// allocator waits here.
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
//
// One counter is shared by ALL prefixes within a book — the counter is named
// "id", not named after the prefix — so ldg_1, tx_2 and ent_3 interleave and the
// number doubles as a creation order. storetest's NextIDSharesOneCounterPerBook
// is what says so: a store keying its counter by (book, prefix) would still hand
// out unique ids, and nothing else would notice.
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
	if err := t.inShape("subledgers"); err != nil {
		return 0, err
	}
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
	if err := t.inShape("accounts"); err != nil {
		return 0, err
	}
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
	if err := t.inShape("ledgers"); err != nil {
		return err
	}
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
	if err := t.inShape("ledgers"); err != nil {
		return ledger.Ledger{}, err
	}
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
	if err := t.inShape("ledgers"); err != nil {
		return nil, err
	}
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
	if err := t.inShape("subledgers"); err != nil {
		return err
	}
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
	if err := t.inShape("subledgers"); err != nil {
		return ledger.Subledger{}, err
	}
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
	if err := t.inShape("subledgers"); err != nil {
		return nil, err
	}
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
	if err := t.inShape("accounts"); err != nil {
		return err
	}
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
	if err := t.inShape("accounts"); err != nil {
		return ledger.Account{}, err
	}
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
	if err := t.inShape("slot_accounts"); err != nil {
		return err
	}
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
	if err := t.inShape("slot_accounts"); err != nil {
		return "", err
	}
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
	if err := t.inShape("slot_accounts"); err != nil {
		return nil, err
	}
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
	if err := t.inShape("accounts"); err != nil {
		return nil, err
	}
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
//
// ledger.Store asks that after this returns, the balance check and the posting
// that depends on it are one serialized step. Here that is a property of the
// unit of work rather than something to buy: SQLite admits one writer, so the
// exclusion a SELECT … FOR UPDATE would arrange is already in force for the
// whole transaction.
//
// SQLite admits one writer, and a transaction that read at one snapshot and then
// tries to write after somebody else has committed is REFUSED rather than
// allowed through — so a check-then-post pair either sees a consistent snapshot
// and commits, or is turned away and re-run by Store.Update with fresh reads.
// That is what FOR UPDATE was emulating with locks, arrived at by detecting the
// conflict rather than by preventing it.
//
// # A write-taking version was written and measured at zero
//
// The plausible port is a statement that changes nothing and makes this
// transaction the writer from here on — UPDATE accounts SET seq = seq WHERE …
// so that the balance is read under the write lock. It was implemented and
// raced against this no-op through ledger.Book.PostTransactionTx on a WAL FILE,
// two withdrawals of 600 against 1000, with the winner holding its transaction
// open for 0, 50ms, 300ms and 2.5s; three runs of each.
//
// The outcomes are identical, at every hold. One winner and a balance of 400 in
// every run; the loser reaches ErrInsufficientBalance while the hold is inside
// the retry budget and surfaces SQLITE_BUSY once it is past it, with and without
// the write alike. The only thing that changed was WHICH statement named the
// lock error — "lock accounts" instead of "next id".
//
// It buys nothing because the loser is refused at its first write whichever
// statement that is, and the retry re-reads either way. So the statement is not
// written: a lock that changes no outcome is a cost and a claim, and the claim
// would be false.
//
// # What this is not
//
// The ephemeral store cannot see any of this. storetest's
// ConcurrentPostingsCannotBothSpendTheSameBalance passes ten runs out of ten
// with this method emptied, because on memdb a retry's read BLOCKS until the
// winner commits and the loser therefore reaches the domain guard however the
// method behaves. The suite is not what holds this; the measurement above is,
// and it had to open a file to be worth anything.
func (t *tx) LockAccounts(ctx context.Context, book ledger.BookID, ids []ledger.AccountID) error {
	if err := t.inShape("accounts"); err != nil {
		return err
	}
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
//
// There is no check-then-insert. The partial unique index on
// (book_id, idempotency_key) is the check, and SQLITE_CONSTRAINT_UNIQUE becomes
// ledger.ErrDuplicateIdempotencyKey: two concurrent postings with the same key
// cannot both pass a SELECT that found nothing, because there is no such SELECT.
//
// # No savepoint, and none is needed
//
// One statement and no savepoint: SQLite rolls back the failed STATEMENT and
// leaves the transaction usable, so a caller who catches the sentinel can carry
// on. A database that aborted the whole transaction on any error would need one.
//
// The cost the rule was really about does reappear one level up, at the unit of
// work — see isTransient and Store.inUpdate, where a retry pays it.
//
// The upsert branch rewrites idempotency_key, so re-putting a transaction under
// a new key frees the old one: the key lives in the row, not in an index that
// could outlive it.
func (t *tx) PutTransaction(ctx context.Context, book ledger.BookID, txn ledger.Transaction) error {
	if err := t.inShape("transactions"); err != nil {
		return err
	}
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
	if err := t.inShape("transactions"); err != nil {
		return ledger.Transaction{}, err
	}
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
	if err := t.inShape("transactions"); err != nil {
		return ledger.Transaction{}, err
	}
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
	if err := t.inShape("transactions"); err != nil {
		return nil, err
	}
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
// position predicate is an EXISTS rather than a join condition. Over a control
// account that means one subsidiary's statement carries the other side of each of
// its own transactions, and none of another subsidiary's.
//
// The EXISTS names the entries alias `x` rather than `e`, so subsidiaryClause,
// which is written against `e`, cannot be used here.
func (t *tx) ListTransactionsForPosition(ctx context.Context, book ledger.BookID, pos ledger.Position) ([]ledger.Transaction, error) {
	if err := t.inShape("transactions"); err != nil {
		return nil, err
	}
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
//
// The UPDATE carries its own precondition, so two concurrent reversals of the
// same transaction cannot both find it Posted and both win: whichever runs
// second affects zero rows. The follow-up SELECT only decides which error to
// report; it is not part of the decision.
func (t *tx) MarkReversed(ctx context.Context, book ledger.BookID, id ledger.TransactionID) error {
	if err := t.inShape("transactions"); err != nil {
		return err
	}
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

// subsidiaryClause is the subsidiary half of a position's entry predicate, and the
// one place in this store where "an empty Subsidiary means the whole account"
// is written down.
//
// An empty one adds NO predicate rather than `subsidiary_id = ”`, so a control
// account's own balance is the identical aggregate with the subsidiary dropped.
// That is what makes Σ(detail) == control a property of these three queries
// instead of a figure to reconcile. It is unambiguous because
// ledger.Book.PostTransactionTx leaves no account holding qualified and
// unqualified legs at once.
//
// It is a fragment rather than a whole predicate because the two value-dated
// callers need it BEFORE their date bounds, and a helper that returned the
// whole WHERE clause would have to know about those too.
func subsidiaryClause(pos ledger.Position) (string, []any) {
	if pos.Subsidiary == "" {
		return "", nil
	}
	return " AND e.subsidiary_id = ?", []any{pos.Subsidiary}
}

// BookBalance aggregates a position's entries in SQL rather than replaying them
// in Go: entries in the account's normal direction add, the rest subtract.
//
// ALL transactions count, Reversed ones included. The Reversed status is
// informational — a reversal posts its own mirrored entries, and those are what
// cancel the original. Filtering reversed transactions out here would
// double-count the correction.
//
// Like every aggregate, a position with no entries is zero, including an
// account that does not exist and a subsidiary that never had a posting; callers
// wanting ErrAccountNotFound read the account first.
func (t *tx) BookBalance(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction) (ledger.Amount, error) {
	if err := t.inShape("entries"); err != nil {
		return 0, err
	}
	if err := t.own(book); err != nil {
		return 0, err
	}
	clause, extra := subsidiaryClause(pos)
	args := append([]any{int64(normal), string(book), string(pos.Account)}, extra...)
	var balance ledger.Amount
	err := t.tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN e.direction = ? THEN e.amount ELSE -e.amount END), 0)
		FROM entries e WHERE e.book_id = ? AND e.account_id = ?`+clause,
		args...).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("sqlite: book balance %s: %w", pos, err)
	}
	return balance, nil
}

// SubsidiaryBalances groups a control account's entries by subsidiary. See
// ledger.Tx for the contract.
//
// HAVING rather than a filter in Go: a subsidiary whose entries net to zero is a
// customer who has repaid, and a listing of what a bank owes should not carry a
// row of nothing. The empty subsidiary cannot appear on a control account —
// PostTransactionTx refuses an unqualified entry against one — so no predicate
// excludes it here.
func (t *tx) SubsidiaryBalances(ctx context.Context, book ledger.BookID, account ledger.AccountID, normal ledger.Direction) ([]ledger.SubsidiaryBalance, error) {
	if err := t.inShape("entries"); err != nil {
		return nil, err
	}
	if err := t.own(book); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT e.subsidiary_id,
		       SUM(CASE WHEN e.direction = ? THEN e.amount ELSE -e.amount END) AS balance
		FROM entries e WHERE e.book_id = ? AND e.account_id = ?
		GROUP BY e.subsidiary_id HAVING balance != 0
		ORDER BY e.subsidiary_id`,
		int64(normal), string(book), string(account))
	if err != nil {
		return nil, fmt.Errorf("sqlite: subsidiary balances %s: %w", account, err)
	}
	defer rows.Close()

	out := make([]ledger.SubsidiaryBalance, 0)
	for rows.Next() {
		var row ledger.SubsidiaryBalance
		if err := rows.Scan(&row.Subsidiary, &row.Balance); err != nil {
			return nil, fmt.Errorf("sqlite: subsidiary balances %s: %w", account, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: subsidiary balances %s: %w", account, err)
	}
	return out, nil
}

// ValueDateBalance is BookBalance restricted to entries whose value date falls
// strictly before the bound. See ledger.Tx for the contract.
//
// A zero ValueDate is stored as NULL (see nullTime), and NULL < ? is unknown
// rather than true, so such an entry is excluded here without a separate
// IS NOT NULL check. That is deliberate rather than incidental — ledger.Tx makes
// the exclusion part of the contract, and storetest's
// ValueDateBalanceExcludesZeroValueDateEntries is what pins it, because reading
// this query it looks like something the SQL happens to do.
//
// The bound is formatted rather than passed as a nullTime, because it is a
// COMPARAND and not a stored value: a zero bound must compare as the earliest
// instant, and NULL would make the predicate unknown for every row.
func (t *tx) ValueDateBalance(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction, before time.Time) (ledger.Amount, error) {
	if err := t.inShape("entries"); err != nil {
		return 0, err
	}
	if err := t.own(book); err != nil {
		return 0, err
	}
	clause, extra := subsidiaryClause(pos)
	args := append([]any{int64(normal), string(book), string(pos.Account)}, extra...)
	args = append(args, formatTime(before))
	var balance ledger.Amount
	err := t.tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN e.direction = ? THEN e.amount ELSE -e.amount END), 0)
		FROM entries e
		WHERE e.book_id = ? AND e.account_id = ?`+clause+` AND e.value_date < ?`,
		args...).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("sqlite: value date balance %s: %w", pos, err)
	}
	return balance, nil
}

// ValueDatedSeries buckets an account's entries by value date in SQL. See
// ledger.Tx for the contract.
//
// The day is the first ten characters of the stored timestamp, and that is the
// whole of the date arithmetic: the column is already UTC and fixed-width by
// store/sqlite/time.go, so its first ten characters ARE its UTC day. It is the
// same property the schema chose TEXT timestamps for, used a second time.
//
// A zero ValueDate is stored as NULL, and substr(NULL) is NULL, so such an entry
// falls out of every group rather than landing in one for year 1. That is the
// same exclusion ValueDateBalance makes, arrived at the same free way, and
// storetest's ValueDatedSeriesExcludesZeroValueDateEntries pins it separately
// because the buckets are built by different code from the balance.
func (t *tx) ValueDatedSeries(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction, from, to time.Time) (ledger.Series, error) {
	if err := t.inShape("entries"); err != nil {
		return ledger.Series{}, err
	}
	if err := t.own(book); err != nil {
		return ledger.Series{}, err
	}
	opening, err := t.ValueDateBalance(ctx, book, pos, normal, from)
	if err != nil {
		return ledger.Series{}, err
	}

	clause, extra := subsidiaryClause(pos)
	args := append([]any{int64(normal), string(book), string(pos.Account)}, extra...)
	args = append(args, formatTime(from), formatTime(to))
	rows, err := t.tx.QueryContext(ctx, `
		SELECT substr(e.value_date, 1, 10) AS day,
		       SUM(CASE WHEN e.direction = ? THEN e.amount ELSE -e.amount END)
		FROM entries e
		WHERE e.book_id = ? AND e.account_id = ?`+clause+`
		  AND e.value_date >= ? AND e.value_date < ?
		GROUP BY 1
		ORDER BY 1`,
		args...)
	if err != nil {
		return ledger.Series{}, fmt.Errorf("sqlite: value dated series %s: %w", pos, err)
	}
	defer rows.Close()

	out := ledger.Series{Opening: opening, Movements: make([]ledger.DayMovement, 0)}
	for rows.Next() {
		var (
			day    string
			amount ledger.Amount
		)
		if err := rows.Scan(&day, &amount); err != nil {
			return ledger.Series{}, fmt.Errorf("sqlite: value dated series %s: %w", pos, err)
		}
		at, err := time.Parse(time.DateOnly, day)
		if err != nil {
			return ledger.Series{}, fmt.Errorf("sqlite: value dated series %s: day %q: %w", pos, day, err)
		}
		out.Movements = append(out.Movements, ledger.DayMovement{
			Day:    ledger.DayStart(at.UTC()),
			Amount: amount,
		})
	}
	if err := rows.Err(); err != nil {
		return ledger.Series{}, fmt.Errorf("sqlite: value dated series %s: %w", pos, err)
	}
	return out, nil
}
