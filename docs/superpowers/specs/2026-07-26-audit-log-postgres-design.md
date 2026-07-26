# Audit log restored, on a Postgres backing store

**Date:** 2026-07-26
**Status:** Draft (pending review)

## Goal

Two changes that only make sense together:

1. **Restore the audit log**, removed in `81135a9` ("Remove the audit log feature").
2. **Make all state durable** in Postgres, with the current in-memory store kept as the
   zero-setup default.

## Why

`81135a9` deleted the audit log across every layer with an explicit rationale: it "was
irrelevant to the project's learning goals." That was a fair call *for an in-memory model* — an
append-only `[]*AuditEvent` slice that dies with the process teaches the shape of an audit trail
without any of the properties that make one matter. It is a log that cannot be audited.

Durability changes the calculus. An audit trail's whole purpose — regulatory retention, forensic
reconstruction, independent balance verification by replay — is a *storage* property, not a data
structure. Once state lives in Postgres, the audit log stops being a decorative slice and becomes
the thing the README's *Audit Trail* section actually describes. That is the justification for
reversing the removal, and it is why the two halves of this spec ship as one feature rather than
two.

Nothing about the original removal was wrong; it is simply superseded.

## Restoration strategy: revert, don't rewrite

`81135a9` is a clean, single-purpose removal (+30/−790 across 24 files), which makes it a
near-perfect inverse patch:

```bash
git revert --no-commit 81135a9e5b85ace1aad48748187be6a6bf04acc5
git checkout HEAD -- web/PLAN.md   # deliberately deleted; superseded by web/CLAUDE.md
```

This recovers `AuditEvent`/`AuditEventType`, the `auditLog` fields, `appendAudit` and every call
site, `GetAuditLog`/`GetAuditLogForEntity`, the three routes and handlers, `auditEventDTO` and its
adapters, both web pages, the central-bank audit section, nav tabs, hooks, endpoints, query keys,
types, the `audit-trail` hint, and the README section — faithfully, including call sites that are
easy to miss when re-deriving by hand.

Conflicts are expected only where work since 06-24 has touched the same files:
`web/src/components/hint-content.ts`, `web/src/lib/api/hooks.ts`,
`web/src/app/central-bank/page.tsx`, `web/src/app/participants/[pid]/layout.tsx`.

The restore lands as its own green commit **before** any storage work, so the Postgres diff is
readable on its own.

## Architecture

Interfaces are defined by the **consumer** (the domain packages) and implemented by sibling
packages, so only the store packages import the domain packages and there are no cycles.

| Package | Role |
|---|---|
| `ledger` | `Book`, domain types, **`ledger.Store` / `ledger.Tx`** |
| `deposit` | `Register`, `deposit.Store` / `deposit.Tx` (embeds `ledger.Tx`) |
| `payment` | `Network`, `payment.Store` / `payment.Tx` (embeds `deposit.Tx`) |
| `store/mem` | in-memory implementation — the maps currently on `Book`/`Register`/`Network` move here |
| `store/pg` | Postgres implementation |
| `store/storetest` | one exported conformance suite both implementations must pass |

The embedding chain is what makes cross-layer atomicity possible: because `payment.Tx` embeds
`deposit.Tx` embeds `ledger.Tx`, a single concrete `*pg.Tx` (or `*mem.Tx`) satisfies all three, so
an operation like `Register.CaptureHold` — which writes a hold row *and* posts a GL transaction —
runs in one unit of work.

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
- `Book` (`ledger/book.go:30`), `Register`, and `Network` keep **all** validation and
  orchestration but hold no maps and no `sync.RWMutex`. Concurrency moves to the store.
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
- **`BookBalance` moves into the store.** Today `computeBookBalance` replays every entry of every
  transaction in Go on every call. Postgres does it in SQL. The accounting rule stays in the
  domain — `AccountType.NormalBalance()` (`ledger/types.go`) picks the sign, SQL encodes it:

  ```sql
  SELECT COALESCE(SUM(CASE WHEN e.direction = $3 THEN e.amount ELSE -e.amount END), 0)
    FROM entries e
   WHERE e.book_id = $1 AND e.account_id = $2;
  ```

  All transactions count, `Reversed` ones included — the reversal's own entries are what cancel
  the original. That existing invariant is preserved deliberately.

### One store, scoped by `BookID`

Today each participant owns its own `*ledger.Book` with its own lock, which is exactly why
`payment/system.go` documents cross-ledger postings as non-atomic:

> Each participant bank and the central bank keep separate ledger.Book instances, each with its
> own lock. A single payment therefore touches several ledgers, and those postings cannot be one
> atomic transaction the way a real RTGS would guarantee.

Replace N stores with **one store instance scoped by `BookID`**. `SettleCycle` can then wrap every
participant's postings and the central bank's in a single `Update`, making settlement genuinely
atomic under both implementations. This is an intentional behaviour change, so three places that
promise the opposite need updating: the *Deliberate Simplifications* list in `README.md`, the
same-named list in `payment/doc.go`, and the *Multiple ledgers, no cross-ledger atomicity* block
in `payment/system.go`.

## Data model

### IDs are unique per `Book`, not globally — so keys are composite

The chart-of-accounts numbering spec
(`docs/superpowers/specs/2026-06-24-chart-of-accounts-numbering-design.md`) is explicit:

> **numbers are unique within a `Book`, not globally** — exactly the scope today's `acct_*` IDs
> already have (each bank's `Book` numbers its own accounts).

And `ledger/numbering_test.go`'s `TestAccountNumberingDeterministic` asserts that two freshly
built books produce **identical** account IDs. So `200.100.001` names Alice at Bank A *and*
someone else at Bank B, by design.

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

The numbering *scheme* is accounting policy and stays in `ledger` (`codeBlock()`, the
`%d.%s.%03d` format at `ledger/book.go:199`). The *counters* currently on `Book` become store
state, scoped per book so determinism survives:

| Current `Book` field | Becomes |
|---|---|
| `subledgerSeq int` (`ledger/book.go:48`) | `id_sequences(book_id, 'subledger')`, stepping 100 |
| `accountSeq map[string]int` (`:53`) | `id_sequences(book_id, '<typeBlock>.<subledgerID>')` |
| `nextID` counter (`:91`) — mints `ldg_`, `tx_`, `ent_` | `id_sequences(book_id, '<prefix>')` |

```sql
CREATE TABLE id_sequences (
  book_id    TEXT NOT NULL,
  prefix     TEXT NOT NULL,
  next_value BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (book_id, prefix)
);
```

Allocation is an upsert with `UPDATE … RETURNING` inside the caller's transaction, so numbering is
gap-free per book and rolls back with a failed operation. Network-scoped IDs (participants,
`pay_`, `cyc_`, `mnd_`, `stl_`) use a reserved `book_id` sentinel of `'network'`.

Note the audit log's own `evt_` prefix rejoins this counter — and per the CoA spec's accepted
"ID drift" decision, the exact numeric suffixes of `evt_`/`tx_`/`ent_` carry no meaning.

### Type mapping

| Go | Postgres |
|---|---|
| `ledger.Amount` (`int64`) | `BIGINT` — minor currency units, never `NUMERIC` |
| `time.Time` | `TIMESTAMPTZ` |
| `iota` enums (`AccountType`, `Direction`, `TransactionStatus`, `AccountStatus`, `HoldStatus`, `PaymentStatus`, `MandateStatus`, `CycleStatus`) | `SMALLINT` — the API keeps rendering `String()` values |
| `map[string]string` metadata | `JSONB` |
| `AuditEvent.Payload` | `JSONB` |

Tables: `books`, `ledgers`, `subledgers`, `accounts`, `transactions`, `entries`,
`deposit_accounts`, `holds`, `snapshots`, `participants`, `mandates`, `payments`, `cycles`,
`cycle_payments`, `settlements`, `settlement_positions`, `audit_events`, `id_sequences`,
`schema_migrations`.

`entries` needs an explicit `seq`, because a transaction's legs are an ordered slice
(`Transaction.Entries`) and row order is not guaranteed.

### The audit table

One unified table with a `scope` column, so the restored per-layer endpoints are each a filtered
read and a combined cross-layer view becomes possible later:

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
`seed/clock.go` pin it, so timestamps tie. A monotonic sequence gives the total order the
append-only slice provided for free.

Indexes:

```sql
CREATE INDEX        ON entries (book_id, account_id);                 -- balance aggregation
CREATE INDEX        ON transactions (book_id, created_at, id);        -- ListTransactions ordering
CREATE INDEX        ON audit_events (book_id, scope, seq);            -- the audit endpoints
CREATE INDEX        ON audit_events (book_id, entity_id);             -- GetAuditLogForEntity
CREATE INDEX        ON holds (book_id, deposit_account_id, status);   -- computeActiveHolds
CREATE UNIQUE INDEX ON transactions (book_id, idempotency_key) WHERE idempotency_key <> '';
```

`ListTransactionsForAccount` currently scans every transaction and every entry; in Postgres it
becomes a join on the `entries (book_id, account_id)` index.

### Migrations

`store/pg/schema/0001_init.sql`, embedded with `go:embed` and applied by a small migrator — a
`schema_migrations(version, applied_at)` table, files sorted by name, each unapplied file run in
its own transaction. No migration-tool dependency.

## Correctness hazards Postgres introduces

A single mutex made three read-then-write sequences safe for free.

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

2. **Idempotency.** Replace the check-then-insert against `idempotencyIndex` (`ledger/book.go:41`)
   with a plain insert, mapping SQLSTATE `23505` on the partial unique index to the existing
   `ErrDuplicateIdempotencyKey`. Check-then-insert has a window; the index does not.

3. **Double reversal.** `ReverseTransaction` (`ledger/book.go:483`) reads `Status`, compares, then
   writes. Make it conditional and check the row count:

   ```sql
   UPDATE transactions SET status = $3
    WHERE book_id = $1 AND id = $2 AND status = $4;  -- $4 = Posted
   ```

   Zero rows affected → `ErrTransactionAlreadyReversed`.

`mem` satisfies all three under its write lock but must return identical errors so the conformance
suite passes against both.

## Audit log hardening on the way back in

The restored code should not come back exactly as it left:

1. **Marshal `Payload` to JSON at append time.** It was `any`, and most call sites passed a live
   pointer — `appendAudit(EventAccountOpened, string(acct.ID), acct)` stored `*Account`, so
   freezing that account later retroactively rewrote the audit record of its opening. An
   "immutable" log with mutable entries is worse than none. `json.RawMessage` at append time fixes
   it and makes the `JSONB` write direct. `appendAudit` gains an error return.
2. **Stop dropping fields at the edge.** The old `auditEventDTO` carried only `id`, `timestamp`,
   `type`, `entityId`; `AuditEvent.Metadata` was declared and never populated. Add `payload` and
   `metadata` so the restored UI pages can render the event body.
3. **Audit the payment layer**, which never had a log:

   | Event | Emitted from |
   |---|---|
   | `participant.added` | `AddParticipant` |
   | `mandate.created`, `mandate.revoked` | `CreateMandate`, `RevokeMandate` |
   | `payment.initiated`, `payment.accepted`, `payment.rejected` | `InitiatePayment`, `RejectPayment` |
   | `payment.cleared`, `payment.settled`, `payment.returned` | `CloseCycle`, `SettleCycle`, `ReturnPayment` |
   | `cycle.opened`, `cycle.closed`, `cycle.settled` | `OpenCycle`, `CloseCycle`, `SettleCycle` |

   These are network-scoped: `book_id = 'network'`, `scope = 'payment'`. Expose them at
   `GET /payments/audit`, alongside a web page mirroring the restored participant audit page.
4. **Paginate.** A durable log is unbounded, so the audit endpoints take `?limit=` (default 100,
   max 1000), `?before=<seq>` as a cursor, `?type=`, and the already-restored `?entity=`. Each
   event gains a `seq` field to page on.

## Reset and seeding

Both are new since the removal and both interact with persistence.

- **`POST /admin/reset`** (`api/handlers_admin.go`) calls `Server.Reset()`, which swaps an
  `atomic.Pointer[payment.Network]` for a freshly built one via `newState func()`
  (`api/server.go`). With Postgres behind it, building a fresh in-memory graph resets nothing —
  the endpoint would return `200` while every row survives. Reset must route through
  `Store.Reset(ctx)`: fresh maps for `mem`, `TRUNCATE` in FK-safe order for `pg`.
- **`seed/`** populates a demo scenario at boot on a deterministic clock (`seed/clock.go`).
  Seeded entities will now emit audit events, and seeding must be idempotent against a database
  that already has rows — skip when `books` is non-empty, or run only as part of the
  reset/migrate path.

## Configuration

```go
dsn := *database // -database flag, default os.Getenv("DATABASE_URL")

var st store.Store
if dsn == "" {
    st = mem.New(time.Now)
    log.Info("using in-memory store; state resets on restart")
} else {
    st, err = pg.Open(ctx, dsn, time.Now) // connects, then applies embedded migrations
}
defer st.Close()
```

Plus a `docker-compose.yml` with one `postgres:16` service. Postgres is strictly optional:
`make dev`, `make run`, and the whole test suite work with no database.

## Impact / affected areas

- **`ledger/`, `deposit/`, `payment/`** — `ctx` threading, `Store`/`Tx` interfaces, `…Tx`
  siblings, maps and mutex removed, `BookID` added.
- **`store/mem`, `store/pg`, `store/storetest`** — new.
- **`api/`** — `ctx` at every call site; `auditEventDTO` restored in `api/dto.go` with the two new
  fields; adapters in `api/dto_ledger.go` / `api/dto_deposit.go`; audit routes restored in
  `handlers_ledger.go` / `handlers_deposit.go` / `handlers_participant.go`; `handlers_admin.go`
  reset path; `handlers_payment.go` gains `GET /payments/audit`.
- **`cmd/server/main.go`, `seed/`** — store selection, `ctx`, idempotent seeding.
- **`web/`** — restored `/audit` and `/deposit-audit` pages, central-bank audit section, nav tabs,
  hooks, endpoints, query keys, types, the `audit-trail` hint; a new payments-audit page; payload
  and metadata rendering.
- **Docs** — `README.md` audit section restored plus a new persistence section and an updated
  "dependency-free" claim; the *Deliberate Simplifications* lists in `README.md`,
  `payment/doc.go` and `payment/system.go`. Per `CLAUDE.md`, domain facts are mirrored in
  `README.md` (authoritative), `book/*.md`, `web/src/components/hint-content.ts`, and
  `web/src/lib/quiz/chapters/*.ts` — check all four for reinstated audit claims, and run
  `make book` if any `book/*.md` changes, since the EPUB and PDF are committed artifacts.
- **`go.mod`** — gains a `require` block and a `go.sum`; the module stops being dependency-free.

## Out of scope (deliberately)

- **Multi-currency** — removed on purpose in `6a65ceb`.
- **Authentication**, and therefore a populated `actor`. The column is added now, left null.
- **Changing the ID scheme.** The CoA numbers stay exactly as `58f065d` defined them; only where
  the counters live changes.
- **Read replicas, pooling tuning, sharding.**
- **The `Gross`-settlement and card-scheme work** under README *Next Work*.

## Testing

- **`store/storetest`** — one exported, table-driven conformance suite,
  `storetest.Run(t, func(t *testing.T) store.Store)`, covering every `Tx` method plus the
  behaviours the domain depends on: `List*` ordering (`CreatedAt` then `ID`), per-book numbering
  determinism, idempotency uniqueness, balance aggregation including reversed transactions,
  conditional reversal, `Reset`, and audit ordering by `seq`. Run from `store/mem` always and
  `store/pg` when `TEST_DATABASE_URL` is set (`t.Skip` otherwise). This is the main defence
  against the two implementations drifting.
- **Existing suites** — `ledger/book_test.go`, `ledger/list_test.go`, `ledger/numbering_test.go`,
  `deposit/register_test.go`, `payment/system_test.go`, `api/server_test.go`, `seed/seed_test.go`
  keep running against `mem` with `ctx` threaded through. An opt-in flag runs them against
  Postgres, which is where cross-layer atomicity actually gets exercised.
- **`ledger/numbering_test.go` must keep passing under both stores**, `TestAccountNumberingDeterministic`
  included — it is the regression gate for moving the counters without changing the scheme.
- **Postgres isolation** — each test creates a fresh schema and sets `search_path`, so tests stay
  parallel-safe without truncation ordering.
- **Keep existing conventions** — the shared `fixedTime` clock, hand-rolled `assertNoError` /
  `assertError` / `assertEqual[T comparable]`, `httptest` for the API layer. No third-party
  assertion library and no testcontainers; a plain `TEST_DATABASE_URL` keeps the dependency count
  at one.

Gate: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`, plus
`cd web && npm run typecheck && npm run lint && npm run build`.

## Prerequisite

`pgx/v5` v5.10.0 requires Go ≥ 1.25 while `go.mod` declares `go 1.24.7`. Either raise the `go`
directive and toolchain, or pin `pgx` to a v5.7.x release. Decide before the `store/pg` work
starts.
