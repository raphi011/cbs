// Package pg is the Postgres implementation of ledger.Store, deposit.Store and
// payment.Store.
//
// store/mem is the reference implementation; this package has to match it, and
// store/storetest is the shared conformance suite that enforces the match. Where
// the two differ at all, mem is right by definition and this package is wrong.
//
// # What a real database adds
//
// Under store/mem a single process-wide mutex makes every unit of work atomic
// and isolated for free, which hides three read-then-write races that a real
// database has to close explicitly:
//
//   - A balance check followed by a posting. tx.LockAccounts takes
//     SELECT … FOR UPDATE on the accounts a transaction touches, ordered by id
//     so two postings over overlapping account sets cannot deadlock each other.
//   - An idempotency key. There is no check-then-insert; the insert itself is
//     the check, against a partial unique index, and SQLSTATE 23505 becomes
//     ledger.ErrDuplicateIdempotencyKey.
//   - A reversal. MarkReversed is one conditional UPDATE … WHERE status = Posted
//     whose row count decides the outcome, never a read-compare-write.
//
// # Books
//
// Every book-scoped table has a composite PRIMARY KEY (book_id, id).
// Chart-of-accounts numbers are unique within a book, not globally, so two
// participants both holding "200.100.001" is normal rather than a collision.
// The payment layer's own entities are network-scoped and keyed by id alone.
//
// # Ordering
//
// Every listing is ORDER BY created_at, seq. Never ORDER BY id: IDs are
// counter-derived strings, so "dep_10" sorts before "dep_8" and a customer list
// silently reorders itself the moment a counter crosses a power of ten. seq is a
// BIGSERIAL assigned on insert and left alone by the upsert branch, which is
// what keeps an edited row where it was.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// ErrReadOnly is returned when a write is attempted inside View, mirroring
// store/mem. The transaction is also opened READ ONLY, so Postgres would refuse
// the write anyway; failing in Go first makes the two stores return the same
// error rather than one domain sentinel and one SQLSTATE 25006.
var ErrReadOnly = errors.New("pg: write attempted in a read-only transaction")

// ErrNestedTransaction is returned when a unit of work is opened inside another
// one on the same store.
//
// store/mem refuses this because its mutex is not reentrant and nesting would
// deadlock. Here the hazard is different but no less bad: a nested Update would
// take a SECOND connection from the pool and run a SEPARATE transaction, so the
// inner writes would commit even when the outer ones roll back — and with
// enough nesting the pool deadlocks instead. Both stores therefore refuse it,
// with the same message, so the mistake behaves identically on either backend.
var ErrNestedTransaction = errors.New("pg: a unit of work is already open on this store (call the …Tx method, not its Update-wrapping sibling)")

// inUnitOfWork is the context key Update and View stamp into the context they
// hand their callback. Its value is the *Store, so two independent stores can
// still be driven from one context — only re-entering the same store is refused.
type inUnitOfWork struct{}

// maxAttempts bounds Update's retry loop. Retrying is not a substitute for
// correct locking; it exists because a correctly locked transaction can still
// lose a deadlock detection race or a serialization check, and those are
// transient by definition.
const maxAttempts = 5

// Store is a Postgres-backed ledger.Store. Call Open; the zero value is unusable.
type Store struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

// Open connects to dsn, applies the embedded migrations and returns a store
// reading time from clock.
func Open(ctx context.Context, dsn string, clock func() time.Time) (*Store, error) {
	return OpenSchema(ctx, dsn, "", clock)
}

// OpenSchema is Open confined to one Postgres schema, creating it if it does not
// exist. It is what lets a test suite give every test its own tables without
// truncating shared ones, and therefore without depending on truncation order.
//
// An empty schema means the connection's default search_path.
func OpenSchema(ctx context.Context, dsn, schema string, clock func() time.Time) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parse dsn: %w", err)
	}
	// Times are compared, formatted into business-date keys and round-tripped
	// through deposit.SnapshotDateKey, so the session must not be allowed to
	// shift them: everything is UTC, in and out.
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if schema != "" {
		if err := createSchema(ctx, cfg.ConnConfig, schema); err != nil {
			return nil, err
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, clock: clock}, nil
}

// createSchema opens a single throwaway connection on the default search_path
// and creates the schema, because a pool whose search_path names a schema that
// does not exist yet cannot create it ("no schema has been selected to create
// in").
func createSchema(ctx context.Context, cfg *pgx.ConnConfig, schema string) error {
	conn, err := pgx.ConnectConfig(ctx, cfg.Copy())
	if err != nil {
		return fmt.Errorf("pg: connect: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("pg: create schema %s: %w", schema, err)
	}
	return nil
}

// Update runs fn in one atomic unit of work: BEGIN, fn, COMMIT, or ROLLBACK if
// fn returns an error.
//
// It retries, up to maxAttempts, on the two transient failures a concurrent
// database produces and a correctly written transaction cannot avoid: a deadlock
// (SQLSTATE 40P01) and a serialization failure (40001). Both mean "this
// transaction was aborted so another could proceed", which is a reason to run it
// again rather than a reason to fail. Nothing else is retried — a duplicate
// idempotency key is an answer, not a hiccup.
func (s *Store) Update(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return s.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// View runs fn in a read-only unit of work. Writes through the Tx it provides
// fail with ErrReadOnly.
func (s *Store) View(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return s.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (s *Store) update(ctx context.Context, fn func(context.Context, *tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkNotNested(ctx); err != nil {
		return err
	}

	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = s.runInTx(ctx, pgx.TxOptions{}, false, fn)
		if err == nil || !isTransient(err) {
			return err
		}
	}
	return err
}

func (s *Store) view(ctx context.Context, fn func(context.Context, *tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkNotNested(ctx); err != nil {
		return err
	}
	return s.runInTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly}, true, fn)
}

// runInTx is one attempt: acquire a connection, BEGIN, run fn, COMMIT or
// ROLLBACK.
func (s *Store) runInTx(ctx context.Context, opts pgx.TxOptions, readOnly bool, fn func(context.Context, *tx) error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("pg: acquire connection: %w", err)
	}
	defer conn.Release()

	dbtx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("pg: begin: %w", err)
	}

	t := &tx{store: s, tx: dbtx, readOnly: readOnly, books: make(map[ledger.BookID]struct{})}
	if err := fn(s.mark(ctx), t); err != nil {
		// Roll back on the caller's behalf. The rollback error is deliberately
		// dropped: fn's error is why the unit of work failed, and a failed
		// rollback is a symptom of the same broken connection.
		_ = dbtx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: commit: %w", err)
	}
	return nil
}

// mark stamps the context handed to a callback so a unit of work opened from
// inside it can be recognised.
func (s *Store) mark(ctx context.Context) context.Context {
	return context.WithValue(ctx, inUnitOfWork{}, s)
}

// checkNotNested refuses to open a unit of work inside another one on this
// store. See ErrNestedTransaction.
func (s *Store) checkNotNested(ctx context.Context) error {
	if ctx.Value(inUnitOfWork{}) == s {
		return ErrNestedTransaction
	}
	return nil
}

// tables is every table Reset empties, and the order is irrelevant because
// TRUNCATE takes them all in one statement.
var tables = []string{
	"books", "ledgers", "subledgers", "accounts", "assets", "transactions", "entries",
	"deposit_accounts", "holds", "snapshots",
	"participants", "mandates", "payments", "cycles", "cycle_payments",
	"settlements", "settlement_positions",
	"audit_events", "id_sequences",
}

// Reset discards all state, so a reset store behaves exactly like a freshly
// migrated one. RESTART IDENTITY is part of that: the seq and audit sequences
// have to go back to 1 as well, the same way store/mem's counters do.
//
// The schema itself survives; only rows are removed.
func (s *Store) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stmt := "TRUNCATE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"
	if _, err := s.pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("pg: reset: %w", err)
	}
	return nil
}

// Close releases the connection pool.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

// ---------------------------------------------------------------------------
// Transient failures
// ---------------------------------------------------------------------------

// isTransient reports whether an error is one a retry can plausibly fix: only
// the two the server raises to break a tie between concurrent transactions.
//
// A unique violation is deliberately NOT in the list. The only unique constraint
// in the schema is the idempotency index, and PutTransaction translates that one
// into ledger.ErrDuplicateIdempotencyKey itself — retrying it would turn a
// definite answer into four more attempts at the same answer.
func isTransient(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40001", "40P01":
		return true
	default:
		return false
	}
}

// isUniqueViolationOn reports whether err is a unique violation on the named
// constraint or index.
func isUniqueViolationOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// ---------------------------------------------------------------------------
// The deposit- and payment-shaped views of the same store
// ---------------------------------------------------------------------------

// Deposit returns this store as a deposit.Store.
//
// It is an adapter rather than a second implementation for the same reason
// store/mem's is: Go allows one Update method per type, and the three Store
// interfaces declare Update with three different callback types. The adapter
// holds no state and hands the callback the very same *tx, so a Register and a
// Book built over one pg.Store share a database transaction.
func (s *Store) Deposit() deposit.Store { return depositStore{s} }

type depositStore struct{ *Store }

var _ deposit.Store = depositStore{}

func (d depositStore) Update(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return d.Store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (d depositStore) View(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return d.Store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// Payment returns this store as a payment.Store. It is the handle a
// payment.Network takes, and the only one it needs: the Network derives its own
// ledger and deposit views from it.
func (s *Store) Payment() payment.Store { return paymentStore{s} }

type paymentStore struct{ *Store }

var _ payment.Store = paymentStore{}

func (p paymentStore) Update(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return p.Store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (p paymentStore) View(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return p.Store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// compile-time check that the store satisfies the interface it is written
// against; the other two are checked on the adapters above.
var _ ledger.Store = (*Store)(nil)
