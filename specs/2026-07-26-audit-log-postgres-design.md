# Audit log restored, on a Postgres backing store

**Date:** 2026-07-26
**Status:** Draft (pending review)

## Goal

Restore the audit log removed in `81135a9`, and move all state behind a store abstraction with
two implementations: the current in-memory one (the zero-setup default) and Postgres.

## Why

`81135a9` deleted the audit log with an explicit rationale — it "was irrelevant to the project's
learning goals." That was a fair call for an in-memory `[]*AuditEvent` that dies with the process:
retention, forensic reconstruction and replay-based verification are storage properties, not
properties of a slice. It was a log that could not be audited.

Durability changes that. Once state lives in Postgres the audit log becomes the thing the README's
*Audit Trail* section describes, which is why the two halves ship as one feature rather than two.

There is also a live inconsistency to repair. `81135a9` scrubbed audit from the README, the code
and the `audit-trail` hint — but quiz chapter 14 is literally named
`14-snapshots-audit-and-statements.ts` and still teaches that "an immutable audit trail ensures no
event is ever silently removed or altered" and that it "independently derives what every balance
should be from first principles." The curriculum currently asserts a feature the code does not
have. Restoring it fixes the contradiction in the direction the teaching material already points.

The purpose of the Postgres half is **curriculum, not plumbing**: how a double-entry ledger maps to
relational tables, why a balance is an aggregate rather than a stored column, and what a single
process-wide mutex was quietly doing for correctness. That framing is why two store implementations
are worth maintaining — the contrast between them is the lesson.

## Architecture

Interfaces are declared by the **consumer** (the domain packages) and implemented by sibling
packages, so only the store packages import the domain packages and there are no cycles.

| Package | Role |
|---|---|
| `ledger` | `Book`, domain types, **`ledger.Store` / `ledger.Tx`** |
| `deposit` | `Register`, `deposit.Store` / `deposit.Tx` (embeds `ledger.Tx`) |
| `payment` | `Network`, `payment.Store` / `payment.Tx` (embeds `deposit.Tx`) |
| `store/mem` | in-memory implementation — the maps currently on `Book`/`Register`/`Network` move here |
| `store/pg` | Postgres implementation |
| `store/storetest` | one exported conformance suite both implementations must pass |

The embedding chain is what buys cross-layer atomicity: because `payment.Tx` embeds `deposit.Tx`
embeds `ledger.Tx`, a single concrete `*pg.Tx` (or `*mem.Tx`) satisfies all three, so an operation
like `Register.CaptureHold` — which writes a hold row *and* posts a GL transaction — runs as one
unit of work.

### Unit of work

```go
// package ledger
type Store interface {
	// Update runs fn in one atomic unit of work, retrying on serialization
	// failure. mem: takes the write lock. pg: BEGIN … COMMIT.
	Update(ctx context.Context, fn func(context.Context, Tx) error) error

	// View runs fn in a read-only unit of work.
	View(ctx context.Context, fn func(context.Context, Tx) error) error

	// Reset discards all state. mem: fresh maps. pg: truncate all tables.
	Reset(ctx context.Context) error

	Close() error
}
```

Consequences:

- **Every public domain method gains `ctx context.Context` as its first parameter** — a wide,
  mechanical change across `api/handlers_*.go`, `seed/`, and every test file.
- `Book` (`ledger/book.go:30`), `Register` (`deposit/register.go:27`) and `Network`
  (`payment/system.go:31`) keep **all** validation and orchestration but hold no maps and no
  `sync.RWMutex`. Concurrency moves to the store.
- Each mutating method gains an exported tx-scoped sibling; the plain method becomes a wrapper:

  ```go
  func (s *Book) PostTransaction(ctx context.Context, req PostTransactionRequest) (Transaction, error) {
      var out Transaction
      err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
          var err error
          out, err = s.PostTransactionTx(ctx, tx, req)
          return err
      })
      return out, err
  }
  ```

  The `…Tx` form must be exported because `deposit` and `payment` are separate packages that need
  to compose postings into their own transaction.

- **`BookBalance` moves into the store.** `computeBookBalance` (`ledger/book.go:565`) currently
  replays every entry of every transaction in Go on every call. Postgres does it in SQL. The
  accounting rule stays in the domain — `AccountType.NormalBalance()` (`ledger/types.go:50`) picks
  the sign, SQL encodes it:

  ```sql
  SELECT COALESCE(SUM(CASE WHEN e.direction = $3 THEN e.amount ELSE -e.amount END), 0)
    FROM entries e
   WHERE e.book_id = $1 AND e.account_id = $2;
  ```

  All transactions count, `Reversed` ones included — the reversal's own entries are what cancel the
  original. That existing invariant is preserved deliberately.

### One store, scoped by `BookID`

Today each participant owns its own `*ledger.Book` with its own lock, which is exactly why
`payment/system.go:15` documents cross-ledger postings as non-atomic:

> Each participant bank and the central bank keep separate ledger.Book instances, each with its own
> lock. A single payment therefore touches several ledgers, and those postings cannot be one atomic
> transaction the way a real RTGS would guarantee.

Replace N stores with **one store instance scoped by `BookID`**. `SettleCycle` can then wrap every
participant's postings and the central bank's in a single `Update`, making settlement genuinely
atomic under both implementations.

This is an intentional behaviour change, so three passages that currently promise the opposite are
rewritten — not deleted. They stop reading "here is a simplification we accept" and start reading
"here is why a real RTGS needs a locked settlement window, and how a database transaction provides
one": the *Deliberate Simplifications* list in `README.md`, the same-named list in `payment/doc.go`,
and the *Multiple ledgers, no cross-ledger atomicity* block in `payment/system.go`.

## Data model

### IDs are unique per `Book`, not globally — so keys are composite

The chart-of-accounts numbering spec
(`docs/superpowers/specs/2026-06-24-chart-of-accounts-numbering-design.md`) is explicit:

> **numbers are unique within a `Book`, not globally** — exactly the scope today's `acct_*` IDs
> already have (each bank's `Book` numbers its own accounts).

And `ledger/numbering_test.go`'s `TestAccountNumberingDeterministic` asserts that two freshly built
books produce **identical** account IDs. So `200.100.001` names Alice at Bank A *and* someone else
at Bank B, by design.

Therefore every book-scoped table uses a **composite primary key `(book_id, id)`** with composite
foreign keys. A single-column `id TEXT PRIMARY KEY` would break the moment a second participant
exists:

```sql
CREATE TABLE accounts (
  book_id      TEXT NOT NULL REFERENCES books(id),
  id           TEXT NOT NULL,
  subledger_id TEXT NOT NULL,
  name         TEXT NOT NULL,
  type         SMALLINT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (book_id, id),
  FOREIGN KEY (book_id, subledger_id) REFERENCES subledgers (book_id, id)
);
```

### Numbering counters move to the store, policy stays in the domain

The numbering *scheme* is accounting policy and stays in `ledger` (`codeBlock()` at
`ledger/types.go:61`, the `%d.%s.%03d` format at `ledger/book.go:199`). The *counters* currently on
`Book` become store state, scoped per book so determinism survives:

| Current `Book` field | Becomes |
|---|---|
| `subledgerSeq int` (`ledger/book.go:48`) | `id_sequences(book_id, 'subledger')`, stepping 100 |
| `accountSeq map[string]int` (`:53`) | `id_sequences(book_id, '<typeBlock>.<subledgerID>')` |
| `idCounter int64` (`:44`) — mints `ldg_`, `tx_`, `ent_` | `id_sequences(book_id, '<prefix>')` |

```sql
CREATE TABLE id_sequences (
  book_id    TEXT NOT NULL,
  prefix     TEXT NOT NULL,
  next_value BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (book_id, prefix)
);
```

Allocation is an upsert with `UPDATE … RETURNING` inside the caller's transaction, so numbering is
gap-free per book and rolls back with a failed operation. Network-scoped IDs (participants, `pay_`,
`cyc_`, `mnd_`, `stl_`) use a reserved `book_id` sentinel of `'network'`.

The audit log's `evt_` prefix rejoins this counter. Per the CoA spec's accepted "ID drift"
decision, the exact numeric suffixes of `evt_`/`tx_`/`ent_` carry no meaning.

### Type mapping

| Go | Postgres |
|---|---|
| `ledger.Amount` (`int64`) | `BIGINT` — minor currency units, never `NUMERIC` |
| `time.Time` | `TIMESTAMPTZ` |
| `iota` enums (`AccountType`, `Direction`, `TransactionStatus`, `AccountStatus`, `HoldStatus`, `PaymentStatus`, `MandateStatus`, `CycleStatus`) | `SMALLINT` — the API keeps rendering `String()` values |
| `map[string]string` metadata | `JSONB` |
| `AuditEvent.Payload` | `JSONB` |

Tables: `books`, `ledgers`, `subledgers`, `accounts`, `transactions`, `entries`, `deposit_accounts`,
`holds`, `snapshots`, `participants`, `mandates`, `payments`, `cycles`, `cycle_payments`,
`settlements`, `settlement_positions`, `audit_events`, `id_sequences`, `schema_migrations`.

`entries` needs an explicit `seq` column, because a transaction's legs are an ordered slice
(`Transaction.Entries`) and row order is not guaranteed.

### Ordering: `CreatedAt` then `seq`, never then `id`

Every book-scoped table carries a monotonic `seq BIGSERIAL`, and every `List*` orders by
`created_at, seq`. Tie-breaking on `id` is wrong, and not subtly so: IDs embed an unpadded
counter, so `dep_8` sorts after `dep_20` and `tx_100` sorts between `tx_68` and `tx_72`. The seed
clock is frozen within each build phase, so every account at a bank shares a `CreatedAt` and the
tie-break is what actually decides the order — the first customer rendered last in the account
list. Postgres reproduces this exactly under `ORDER BY created_at, id`, so the fix belongs in the
contract, not in either implementation.

`seq` is per-book and assigned on insert. It is the same mechanism `audit_events` already uses for
its total order, so the two agree rather than each inventing an ordering rule. Zero-padding the ID
counter was the cheaper alternative and is rejected: it changes every visible ID and reintroduces
the same defect at whatever width is chosen.

### Migrations

`store/pg/schema/0001_init.sql`, embedded with `go:embed` and applied by a small migrator: a
`schema_migrations(version, applied_at)` table, files sorted by name, each unapplied file run in its
own transaction. No migration-tool dependency.

## The audit log

### One event type, three scopes

The old code duplicated `AuditEventType` and `AuditEvent` in `ledger` and `deposit`, and `payment`
had none. Replace that with a single type in `ledger` — the lowest layer, which the others already
import — discriminated by scope:

```go
type Scope string

const (
	ScopeLedger  Scope = "ledger"
	ScopeDeposit Scope = "deposit"
	ScopePayment Scope = "payment"
)

type AuditEvent struct {
	Seq        int64             // total order; assigned by the store
	ID         string            // evt_N, from the shared per-book counter
	BookID     BookID            // or the "network" sentinel
	Scope      Scope
	Type       string            // "account.opened", "payment.settled", …
	EntityID   string
	Payload    json.RawMessage
	Metadata   map[string]string
	Actor      string            // empty until authentication exists
	OccurredAt time.Time
}
```

One table, one store method; each restored endpoint is a filtered read, and a combined cross-layer
timeline becomes possible — which is what an auditor actually wants.

```sql
CREATE TABLE audit_events (
  seq         BIGSERIAL PRIMARY KEY,  -- total order => stable chronological listing
  id          TEXT NOT NULL,
  book_id     TEXT NOT NULL REFERENCES books(id),
  scope       TEXT NOT NULL,          -- 'ledger' | 'deposit' | 'payment'
  type        TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  payload     JSONB,
  metadata    JSONB,
  actor       TEXT,                   -- nullable; unpopulated until auth exists
  occurred_at TIMESTAMPTZ NOT NULL
);
```

`seq`, not `occurred_at`, is the ordering key: the clock is injectable and both the tests and
`seed/clock.go` pin it, so timestamps tie. A monotonic sequence restores the total order the
append-only slice provided for free.

### Two defects fixed on the way back in

1. **`Payload` was a live pointer.** `appendAudit(EventAccountOpened, string(acct.ID), acct)` stored
   a `*Account` in a field typed `any`, so freezing that account later retroactively rewrote the
   audit record of its opening. An "immutable" log with mutable entries is worse than none. Marshal
   to `json.RawMessage` at append time — which also makes the `JSONB` write direct. `appendAudit`
   gains an error return.

2. **The wire type dropped fields.** `auditEventDTO` carried only `id`, `timestamp`, `type`,
   `entityId`; `AuditEvent.Metadata` was declared and never populated. Add `payload`, `metadata` and
   `seq` so the restored UI pages can render an event body and page on a cursor.

### Payment-layer events

The payment layer never had an audit log. It gets one, network-scoped (`book_id = 'network'`,
`scope = 'payment'`):

| Event | Emitted from |
|---|---|
| `participant.added` | `AddParticipant` |
| `mandate.created`, `mandate.revoked` | `CreateMandate`, `RevokeMandate` |
| `payment.initiated`, `payment.accepted`, `payment.rejected` | `InitiatePayment`, `RejectPayment` |
| `payment.cleared`, `payment.settled`, `payment.returned` | `CloseCycle`, `SettleCycle`, `ReturnPayment` |
| `cycle.opened`, `cycle.closed`, `cycle.settled` | `OpenCycle`, `CloseCycle`, `SettleCycle` |

### Endpoints

Restored: `GET /participants/{pid}/audit`, `GET /participants/{pid}/deposit-audit`,
`GET /central-bank/audit`. New: `GET /payments/audit`.

A durable log is unbounded, so all four take `?limit=` (default 100, max 1000), `?before=<seq>` as a
cursor, `?type=`, and `?entity=`.

### Indexes

```sql
CREATE INDEX        ON entries (book_id, account_id);                 -- balance aggregation
CREATE INDEX        ON transactions (book_id, created_at, id);        -- ListTransactions ordering
CREATE INDEX        ON audit_events (book_id, scope, seq);            -- the audit endpoints
CREATE INDEX        ON audit_events (book_id, entity_id);             -- filtering by entity
CREATE INDEX        ON holds (book_id, deposit_account_id, status);   -- computeActiveHolds
CREATE UNIQUE INDEX ON transactions (book_id, idempotency_key) WHERE idempotency_key <> '';
```

`ListTransactionsForAccount` currently scans every transaction and every entry; in Postgres it
becomes a join on the `entries (book_id, account_id)` index.

## Correctness hazards Postgres introduces

A single mutex made three read-then-write sequences safe for free. Each of these is also a README
passage — they are the most transferable part of the persistence lesson.

1. **Balance-check race.** `checkSufficientBalance` (`ledger/book.go:406`) reads a balance then
   writes. Under `READ COMMITTED` two concurrent postings against the same Asset account can both
   pass and jointly overdraw it. Lock the affected rows first, in a deterministic order:

   ```sql
   SELECT id FROM accounts
    WHERE book_id = $1 AND id = ANY($2)
    ORDER BY id
      FOR UPDATE;
   ```

   Sorting by `id` prevents deadlock between transactions with overlapping account sets. `Update`
   additionally retries on SQLSTATE `40001` and `40P01` with a bounded attempt count.

2. **Idempotency.** Replace the check-then-insert against `idempotencyIndex` (`ledger/book.go:41`,
   `:314`) with a plain insert, mapping SQLSTATE `23505` on the partial unique index to the existing
   `ErrDuplicateIdempotencyKey`. Check-then-insert has a window; the index does not.

3. **Double reversal.** `ReverseTransaction` (`ledger/book.go:483`) reads `Status`, compares, then
   writes. Make it conditional and check the row count:

   ```sql
   UPDATE transactions SET status = $3
    WHERE book_id = $1 AND id = $2 AND status = $4;  -- $4 = Posted
   ```

   Zero rows affected → `ErrTransactionAlreadyReversed`.

`mem` satisfies all three under its write lock but must return **identical errors**, so the
conformance suite passes against both.

## Reset and seeding

Both are new since the audit log was removed, and both break under persistence.

- **`POST /admin/reset`** (`api/handlers_admin.go`) calls `Server.Reset()`, which swaps an
  `atomic.Pointer[payment.Network]` for a freshly built one via `newState func()`
  (`api/server.go:41`). With Postgres behind it, building a fresh in-memory graph resets nothing —
  the endpoint would return `200` while every row survives. Reset must route through
  `Store.Reset(ctx)`: fresh maps for `mem`, `TRUNCATE` in FK-safe order for `pg`.
- **`seed/`** populates a demo scenario at boot on a deterministic clock (`seed/clock.go`). Seeded
  entities now emit audit events, and seeding must be idempotent against a database that already has
  rows — skip when `books` is non-empty.

## Configuration

There is no shared `store.Store` type to hold the selected implementation in: the interfaces are
declared per layer, and a single concrete store satisfies all three. `payment.Store` is the widest,
so it is what `main` holds and what `payment.NewNetwork` takes; `Network` passes the same value down
when it builds each `Register` and `Book`.

```go
dsn := *database // -database flag, default os.Getenv("DATABASE_URL")

var st payment.Store // widest of the three interfaces; mem and pg each satisfy all
if dsn == "" {
    st = mem.New(time.Now)
    log.Info("using in-memory store; state resets on restart")
} else {
    st, err = pg.Open(ctx, dsn, time.Now) // connects, then applies embedded migrations
}
defer st.Close()
```

Plus a `docker-compose.yml` with one `postgres:16` service. Postgres is strictly optional: `make
dev`, `make run`, and the whole test suite work with no database.

## Curriculum

Per `CLAUDE.md`, domain facts are mirrored across `README.md` (authoritative),
`web/src/components/hint-content.ts` and `web/src/lib/quiz/chapters/*.ts`. All three change.

- **`README.md`** — a new persistence section (the ledger→relational mapping, why a balance is an
  aggregate rather than a stored column, the three races above, unit of work and why cross-layer
  atomicity needs one `Tx`), the restored *Audit Trail* section, the three rewritten
  cross-ledger-atomicity passages, and a qualified dependency claim: the library core stays
  stdlib-only, `store/pg` is the exception.
- **`hint-content.ts`** — restore the `audit-trail` hint that `81135a9` scrubbed, along with its
  wiki-links; add keys for the new persistence concepts so the web UI's side panel explains them.
- **Quiz chapter 15 on persistence** — ~20 questions meeting the rubric `diversity.test.ts` already
  enforces: 18–22 questions, at least 8 distinct concept tags, no tag used more than 3×, and all
  three difficulty tiers present. Chapter 14 already covers the audit trail, so 15 stays focused on
  storage: the relational mapping, derived vs stored balances, transaction boundaries, the three
  races, idempotency under concurrency. Registered in `web/src/lib/quiz/index.ts` under its part.

## Out of scope (deliberately)

- **Multi-currency** — removed on purpose in `6a65ceb`.
- **Authentication**, and therefore a populated `Actor`. The column is added now, left null.
- **Changing the ID scheme.** The CoA numbers stay exactly as `58f065d` defined them; only where the
  counters live changes.
- **Read replicas, connection-pool tuning, sharding.**
- **The `Gross`-settlement and card-scheme work** under README *Next Work*.

## Testing

- **`store/storetest`** — one exported, table-driven conformance suite,
  `storetest.Run(t, func(t *testing.T) store.Store)`, covering every `Tx` method plus the behaviours
  the domain depends on: `List*` ordering (`CreatedAt` then `Seq` — see *Ordering* below),
  per-book numbering determinism,
  idempotency uniqueness, balance aggregation including reversed transactions, conditional reversal,
  `Reset`, and audit ordering by `seq`. Run from `store/mem` always and `store/pg` when
  `TEST_DATABASE_URL` is set (`t.Skip` otherwise). This is the main defence against the two
  implementations drifting.
- **Existing suites** — `ledger/book_test.go`, `ledger/list_test.go`, `ledger/numbering_test.go`,
  `deposit/register_test.go`, `deposit/list_test.go`, `payment/system_test.go`,
  `payment/list_test.go`, `api/server_test.go`, `seed/seed_test.go` keep running against `mem` with
  `ctx` threaded through. An opt-in flag runs them against Postgres, which is where cross-layer
  atomicity actually gets exercised.
- **`ledger/numbering_test.go` must keep passing unedited**, `TestAccountNumberingDeterministic`
  included — it is the regression gate for moving the counters without changing the scheme.
- **Payload immutability** — a test that appends an event, mutates the entity, and asserts the
  stored payload is unchanged. This is the defect from the original implementation; it needs a
  guard, not just a fix.
- **Postgres isolation** — each test creates a fresh schema and sets `search_path`, so tests stay
  parallel-safe without truncation ordering.
- **Keep existing conventions** — the shared `fixedTime` clock, hand-rolled `assertNoError` /
  `assertError` / `assertEqual[T comparable]`, `httptest` for the API layer. No third-party
  assertion library and no testcontainers; a plain `TEST_DATABASE_URL` keeps the dependency count at
  one.

Gate: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`, plus
`cd web && npm run typecheck && npm run lint && npm run build`.

## Prerequisite

`pgx/v5` v5.10 requires Go ≥ 1.25 while `go.mod` declares `go 1.24.7`. Raise the `go` directive to
1.25 rather than pinning `pgx` backwards; the installed toolchain is already 1.26.
