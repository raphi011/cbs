# Audit Log + Postgres Backing Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore the audit log removed in `81135a9` as one scope-discriminated event type across all three layers, and move all state behind a store abstraction with in-memory (default) and Postgres implementations.

**Architecture:** Consumer-defined `Store`/`Tx` interfaces in `ledger`, `deposit` and `payment`; implementations in `store/mem` and `store/pg`; a shared `store/storetest` conformance suite. `payment.Tx` embeds `deposit.Tx` embeds `ledger.Tx`, so one concrete `Tx` makes cross-layer operations atomic. `Book`/`Register`/`Network` keep all validation and orchestration and lose their maps and mutexes.

**Tech Stack:** Go (directive raised to 1.25 in Task 7), `github.com/jackc/pgx/v5`, Postgres 16, Next.js frontend with vitest.

**Design spec:** `docs/superpowers/specs/2026-07-26-audit-log-postgres-design.md`

## Global Constraints

- **Postgres is optional.** No database ⇒ `store/mem`. `make dev`, `make run` and `go test ./...` must work with zero setup, always.
- **IDs stay exactly as `58f065d` defined them** — subledgers `100`/`200`/`300`, accounts `<block>.<subledger>.<NNN>`, and `ldg_`/`tx_`/`ent_`/`evt_` from a shared counter. Only *where the counters live* changes.
- **`ledger/numbering_test.go` must pass unedited** except for mechanical `ctx` threading. It is the regression gate for this refactor.
- **IDs are unique per book, not globally.** Every book-scoped table uses composite `PRIMARY KEY (book_id, id)` and composite FKs. A single-column `id` PK is a bug.
- **`ctx context.Context` is the first parameter** of every public domain method.
- Each mutating method has an exported `…Tx(ctx, tx, …)` sibling; the plain method is a thin `Update` wrapper around it.
- **Test conventions:** the shared `fixedTime` clock, the hand-rolled `assertNoError` / `assertError` / `assertEqual[T comparable]` helpers in `ledger/book_test.go:63-77`, and `httptest` for the API layer. No third-party assertion library, no testcontainers.
- **Do not restore `web/PLAN.md`** — deliberately deleted in `81135a9`, superseded by `web/CLAUDE.md`.
- **Do not restore `book/`** — removed in `f97ef7f`, moved to the second-brain Field Guide. `81135a9` touched no `book/` files, so the revert in Task 1 will not resurrect it; verify.
- **Regression gate for every task:** `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`. Tasks touching `web/`: also `cd web && npm run typecheck && npm run lint && npm run test && npm run build`.

## File Structure

| File | Responsibility |
|---|---|
| `ledger/audit.go` (new) | `Scope`, `AuditEvent`, `AuditFilter`, event-type constants for all three layers |
| `ledger/store.go` (new) | `ledger.Store`, `ledger.Tx` |
| `ledger/book.go` | `Book` — validation + orchestration only, no maps, no mutex |
| `deposit/store.go` (new) | `deposit.Store`, `deposit.Tx` (embeds `ledger.Tx`) |
| `payment/store.go` (new) | `payment.Store`, `payment.Tx` (embeds `deposit.Tx`) |
| `store/mem/mem.go` (new) | in-memory `Store`; the maps from all three layers |
| `store/mem/tx.go` (new) | in-memory `Tx` — satisfies all three interfaces |
| `store/pg/pg.go` (new) | `pg.Open`, pool, `Update`/`View` with retry, `Reset` |
| `store/pg/tx_ledger.go`, `tx_deposit.go`, `tx_payment.go`, `tx_audit.go` (new) | `*pg.Tx` methods, split by layer |
| `store/pg/migrate.go`, `store/pg/schema/0001_init.sql` (new) | embedded migrator + schema |
| `store/storetest/storetest.go` (new) | exported conformance suite both stores must pass |
| `api/handlers_audit.go` (new) | the four audit endpoints + pagination parsing |
| `web/src/lib/quiz/chapters/15-persistence-and-durability.ts` (new) | quiz chapter 15 |

---

### Task 1: Restore the audit log by reverting `81135a9`

Land the feature back exactly as it was — green, in-memory — before any other change, so every later diff is readable on its own.

**Files:** the 24 touched by `81135a9`, minus `web/PLAN.md`.

**Interfaces:**
- Produces: `ledger.AuditEvent`, `ledger.AuditEventType`, `Book.GetAuditLog()`, `Book.GetAuditLogForEntity(entityID string)`, `deposit.AuditEvent`, `deposit.AuditEventType`, `Register.GetAuditLog()`, `api.auditEventDTO`. Task 2 replaces all of these — do not build on their exact shapes beyond Task 1.

- [ ] **Step 1: Apply the inverse patch**

```bash
git revert --no-commit 81135a9e5b85ace1aad48748187be6a6bf04acc5
git checkout HEAD -- web/PLAN.md
git status --short book/   # must print nothing; book/ stays deleted
```

- [ ] **Step 2: Resolve conflicts in the four files touched since 06-24**

`web/src/components/hint-content.ts`, `web/src/lib/api/hooks.ts`, `web/src/app/central-bank/page.tsx`, `web/src/app/participants/[pid]/layout.tsx`.

Keep the newer quiz/concept work **and** re-add the audit pieces alongside it. Do not clobber either side. In `hint-content.ts` the restored entry is keyed `"audit-trail"`; its `[[wiki-links]]` must resolve to keys that still exist in the current file.

- [ ] **Step 3: Run the regression gate**

Run: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`
Expected: PASS. `ledger/book_test.go` regains `TestAuditLog` and `TestGetAuditLogForEntity` from the revert; both must pass.

Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: PASS.

- [ ] **Step 4: Verify the restored HTTP surface by hand**

```bash
go run ./cmd/server &
curl -s localhost:8080/participants/bank_1/audit | head -c 400
curl -s localhost:8080/participants/bank_1/deposit-audit | head -c 400
curl -s localhost:8080/central-bank/audit | head -c 400
```
Expected: three JSON arrays of audit events, not 404s.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "Restore the audit log feature

Reverts 81135a9. The removal was correct for an in-memory slice; the
following commits make the log durable, which is what gives it the
properties the README and quiz chapter 14 already describe."
```

---

### Task 2: Unify and harden the audit event

Three defects, all still in-memory: the type is duplicated per layer, `Payload` stores a live pointer, and the DTO drops fields.

**Files:**
- Create: `ledger/audit.go`, `ledger/audit_test.go`
- Modify: `ledger/types.go` (delete the restored audit types), `ledger/book.go`, `deposit/types.go` (delete the restored audit types), `deposit/register.go`, `api/dto.go`, `api/dto_ledger.go`, `api/dto_deposit.go`, `api/server_test.go`

**Interfaces:**
- Consumes: the Task 1 restoration.
- Produces:
  - `ledger.Scope` (`ScopeLedger`/`ScopeDeposit`/`ScopePayment`), `ledger.AuditEvent`, `ledger.AuditFilter`
  - `ledger.BookID` (a `string` defined type; a placeholder value `""` is fine until Task 5 populates it)
  - event-type constants for all three layers as `string` consts
  - `Book.appendAudit(scope Scope, eventType, entityID string, payload any) error`
  - `Register.appendAudit(...)` with the same signature
  - `api.auditEventDTO` with `seq`, `payload`, `metadata` fields

- [ ] **Step 1: Write `ledger/audit.go`**

```go
package ledger

import (
	"encoding/json"
	"time"
)

// BookID identifies one participant's book of accounts. Chart-of-accounts IDs
// are unique within a book, not globally, so BookID scopes every lookup.
type BookID string

// NetworkBook is the sentinel BookID for network-scoped entities (participants,
// payments, mandates, cycles, settlements), which belong to no single bank.
const NetworkBook BookID = "network"

// Scope names the layer an audit event originated in.
type Scope string

const (
	ScopeLedger  Scope = "ledger"
	ScopeDeposit Scope = "deposit"
	ScopePayment Scope = "payment"
)

// Audit event types. Grouped by scope; the values are the wire format and
// must not change once released.
const (
	// ScopeLedger
	EventLedgerCreated       = "ledger.created"
	EventSubledgerCreated    = "subledger.created"
	EventAccountCreated      = "account.created"
	EventTransactionPosted   = "transaction.posted"
	EventTransactionReversed = "transaction.reversed"

	// ScopeDeposit
	EventAccountOpened      = "account.opened"
	EventAccountFrozen      = "account.frozen"
	EventAccountUnfrozen    = "account.unfrozen"
	EventAccountClosed      = "account.closed"
	EventAccountDormant     = "account.dormant"
	EventAccountReactivated = "account.reactivated"
	EventHoldCreated        = "hold.created"
	EventHoldReleased       = "hold.released"
	EventHoldCaptured       = "hold.captured"
	EventSnapshotTaken      = "snapshot.taken"
)

// AuditEvent is an immutable record of one mutation.
//
// Payload is marshalled at append time rather than held as a live reference.
// The original implementation stored the entity pointer itself, so mutating an
// account later retroactively rewrote the audit record of its own creation — an
// immutable log with mutable entries.
type AuditEvent struct {
	// Seq is a monotonic total order assigned by the store. It, not OccurredAt,
	// is the ordering and pagination key: the clock is injectable and both the
	// tests and seed/clock.go pin it, so timestamps tie.
	Seq        int64
	ID         string
	BookID     BookID
	Scope      Scope
	Type       string
	EntityID   string
	Payload    json.RawMessage
	Metadata   map[string]string
	Actor      string // empty until authentication exists
	OccurredAt time.Time
}

// AuditFilter narrows an audit log listing. Zero values mean "no filter".
type AuditFilter struct {
	BookID   BookID
	Scope    Scope
	Type     string
	EntityID string
	// Before pages backwards: return only events with Seq < Before.
	Before int64
	// Limit caps the result size. Zero means the caller's default.
	Limit int
}
```

- [ ] **Step 2: Write the failing payload-immutability test**

Create `ledger/audit_test.go`:

```go
package ledger

import (
	"encoding/json"
	"testing"
)

// The original audit log stored the live entity pointer in Payload, so mutating
// the entity afterwards rewrote history. Payload must be a snapshot.
func TestAuditPayloadIsSnapshotNotReference(t *testing.T) {
	book := NewBook()
	gl, err := book.CreateLedger("GL")
	assertNoError(t, err)
	sl, err := book.CreateSubledger(gl.ID, "Deposits")
	assertNoError(t, err)
	acct, err := book.CreateAccount(sl.ID, "Alice", Liability)
	assertNoError(t, err)

	events := book.GetAuditLogForEntity(string(acct.ID))
	if len(events) != 1 {
		t.Fatalf("got %d events for %s, want 1", len(events), acct.ID)
	}

	var before struct{ Name string }
	assertNoError(t, json.Unmarshal(events[0].Payload, &before))
	assertEqual(t, "payload name at append time", before.Name, "Alice")

	// Rename the account, then re-read the same audit event.
	assertNoError(t, book.RenameAccount(acct.ID, "Alice Renamed"))

	events = book.GetAuditLogForEntity(string(acct.ID))
	var after struct{ Name string }
	assertNoError(t, json.Unmarshal(events[0].Payload, &after))
	assertEqual(t, "payload name after mutation", after.Name, "Alice")
}
```

Note: `RenameAccount` does not exist. If adding it is undesirable scope, mutate through the store in a later task instead — but for this task, add a minimal `func (s *Book) RenameAccount(id AccountID, name string) error` to `ledger/book.go` that takes the lock, looks up the account, sets `Name`, and returns `ErrAccountNotFound` when missing. It is three lines and it makes the immutability guarantee testable, which is the point.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./ledger/ -run TestAuditPayloadIsSnapshotNotReference -v`
Expected: FAIL — either "undefined: RenameAccount", or once that exists, `payload name after mutation = "Alice Renamed", want "Alice"`.

- [ ] **Step 4: Replace both `appendAudit` implementations**

In `ledger/book.go`, delete the restored `appendAudit` and write:

```go
// appendAudit records an immutable event. payload is marshalled now, not held
// by reference, so later mutation of the entity cannot rewrite history.
func (s *Book) appendAudit(scope Scope, eventType, entityID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit %s: marshal payload: %w", eventType, err)
	}
	s.auditSeq++
	s.auditLog = append(s.auditLog, &AuditEvent{
		Seq:        s.auditSeq,
		ID:         s.nextID("evt"),
		BookID:     s.id,
		Scope:      scope,
		Type:       eventType,
		EntityID:   entityID,
		Payload:    raw,
		OccurredAt: s.now(),
	})
	return nil
}
```

Add `auditSeq int64` and `id BookID` fields to `Book`. Every call site now checks the error — in `CreateLedger`, `CreateSubledger`, `CreateAccount`, `PostTransaction`, `ReverseTransaction`, return the zero value and the error.

Do the same in `deposit/register.go`, passing `ScopeDeposit`, and delete the duplicated `AuditEventType`/`AuditEvent` from `deposit/types.go` and `ledger/types.go`. `deposit` refers to `ledger.AuditEvent` and the `ledger.Event*` constants.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./ledger/ -run TestAuditPayloadIsSnapshotNotReference -v`
Expected: PASS

- [ ] **Step 6: Widen the DTO**

In `api/dto.go`:

```go
// auditEventDTO is the wire shape of an audit event. All three layers render
// into it; scope says which one produced it.
type auditEventDTO struct {
	Seq       int64             `json:"seq"`
	ID        string            `json:"id"`
	Scope     string            `json:"scope"`
	Timestamp time.Time         `json:"timestamp"`
	Type      string            `json:"type"`
	EntityID  string            `json:"entityId"`
	Payload   json.RawMessage   `json:"payload,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}
```

Collapse `toLedgerAuditDTO` and `toDepositAuditDTO` into one `toAuditDTO(e ledger.AuditEvent) auditEventDTO` in `api/dto.go`, and delete the two per-layer adapters from `api/dto_ledger.go` and `api/dto_deposit.go`.

- [ ] **Step 7: Assert the new fields reach the wire**

Add to `api/server_test.go`, following the existing `httptest` pattern in that file:

```go
func TestAuditEndpointIncludesPayloadAndSeq(t *testing.T) {
	srv := newTestServer(t)

	var events []auditEventDTO
	getJSON(t, srv, "/participants/bank_1/audit", &events)

	if len(events) == 0 {
		t.Fatal("no audit events returned")
	}
	if events[0].Seq == 0 {
		t.Error("seq not populated on the wire")
	}
	if len(events[0].Payload) == 0 {
		t.Error("payload not populated on the wire")
	}
	if events[0].Scope == "" {
		t.Error("scope not populated on the wire")
	}
}
```

Use whatever the existing helpers in `api/server_test.go` are actually named for building a server and decoding JSON; match them rather than introducing new ones.

- [ ] **Step 8: Run the full gate**

Run: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "Unify and harden the audit event

One scope-discriminated type in ledger replaces the duplicated per-layer
types. Payload is marshalled at append time — it previously stored the live
entity pointer, so mutating an account rewrote the audit record of its own
creation. The DTO stops dropping seq, payload and metadata."
```

---

### Task 3: `ledger.Store` / `ledger.Tx`, `store/mem`, and the conformance suite

The pivot. `Book`'s maps move into `store/mem` behind an interface; behaviour stays identical.

**Files:**
- Create: `ledger/store.go`, `store/mem/mem.go`, `store/mem/tx.go`, `store/mem/mem_test.go`, `store/storetest/storetest.go`
- Modify: `ledger/book.go`, `ledger/list.go`, `ledger/book_test.go`, `ledger/list_test.go`, `ledger/numbering_test.go`, `ledger/audit_test.go`

**Interfaces:**
- Consumes: `ledger.AuditEvent`, `ledger.AuditFilter`, `ledger.BookID`, `ledger.Scope` from Task 2.
- Produces:
  - `ledger.Store` — `Update`, `View`, `Reset`, `Close`
  - `ledger.Tx` — full method set below
  - `mem.New(clock func() time.Time) *mem.Store`
  - `storetest.RunLedger(t *testing.T, newStore func(*testing.T) ledger.Store)`
  - `ledger.NewBook(store Store, id BookID, clock func() time.Time) *Book`
  - `Book.PostTransactionTx(ctx, tx, req)`, `Book.CreateAccountTx(ctx, tx, …)`, `Book.CreateLedgerTx`, `Book.CreateSubledgerTx`, `Book.ReverseTransactionTx` — the tx-scoped siblings `deposit` and `payment` compose with
  - `Book.appendAuditTx(ctx context.Context, tx Tx, scope Scope, eventType, entityID string, payload any) error` — Task 2's `appendAudit` becomes tx-scoped here, since an audit event must never outlive a rolled-back operation. `Register` and `Network` each grow the same method against their own `Tx`; the marshalling and error-return behaviour from Task 2 is unchanged.

- [ ] **Step 1: Define `ledger/store.go`**

```go
package ledger

import (
	"context"
	"time"
)

// Store owns all persistent state. Interfaces are declared here, by the
// consumer, and implemented by store/mem and store/pg — so the store packages
// import the domain packages and never the reverse.
type Store interface {
	// Update runs fn in one atomic unit of work, retrying on serialization
	// failure. mem takes the write lock; pg runs BEGIN … COMMIT.
	Update(ctx context.Context, fn func(context.Context, Tx) error) error

	// View runs fn in a read-only unit of work.
	View(ctx context.Context, fn func(context.Context, Tx) error) error

	// Reset discards all state: fresh maps for mem, TRUNCATE for pg.
	Reset(ctx context.Context) error

	Close() error
}

// Tx is one unit of work over the ledger layer. deposit.Tx and payment.Tx embed
// it, so a single concrete Tx spans all three layers and cross-layer operations
// commit or roll back together.
type Tx interface {
	// Identity allocation. Gap-free per book; rolls back with the transaction.
	NextID(ctx context.Context, book BookID, prefix string) (string, error)
	NextSubledgerBlock(ctx context.Context, book BookID) (int, error)
	NextAccountSeq(ctx context.Context, book BookID, typeBlock int, subledger SubledgerID) (int, error)

	PutLedger(ctx context.Context, book BookID, l Ledger) error
	GetLedger(ctx context.Context, book BookID, id LedgerID) (Ledger, error)
	ListLedgers(ctx context.Context, book BookID) ([]Ledger, error)

	PutSubledger(ctx context.Context, book BookID, sl Subledger) error
	GetSubledger(ctx context.Context, book BookID, id SubledgerID) (Subledger, error)
	ListSubledgers(ctx context.Context, book BookID) ([]Subledger, error)

	PutAccount(ctx context.Context, book BookID, a Account) error
	GetAccount(ctx context.Context, book BookID, id AccountID) (Account, error)
	ListAccounts(ctx context.Context, book BookID) ([]Account, error)

	// LockAccounts takes a write lock on the given accounts in a deterministic
	// order, so a balance check and the posting that follows it are one
	// serialized step. mem: a no-op under the write lock. pg: SELECT … FOR
	// UPDATE ordered by id, which also prevents deadlock between transactions
	// with overlapping account sets.
	LockAccounts(ctx context.Context, book BookID, ids []AccountID) error

	PutTransaction(ctx context.Context, book BookID, t Transaction) error
	GetTransaction(ctx context.Context, book BookID, id TransactionID) (Transaction, error)
	GetTransactionByIdempotencyKey(ctx context.Context, book BookID, key string) (Transaction, error)
	ListTransactions(ctx context.Context, book BookID) ([]Transaction, error)
	ListTransactionsForAccount(ctx context.Context, book BookID, id AccountID) ([]Transaction, error)

	// MarkReversed sets status to Reversed only if it is currently Posted, and
	// returns ErrTransactionAlreadyReversed otherwise. Conditional because a
	// read-compare-write would race once a mutex no longer covers it.
	MarkReversed(ctx context.Context, book BookID, id TransactionID) error

	// BookBalance aggregates entries rather than replaying them in Go. normal is
	// the account type's normal direction; entries in that direction add.
	BookBalance(ctx context.Context, book BookID, id AccountID, normal Direction) (Amount, error)

	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditEvent, error)

	Now() time.Time
}
```

- [ ] **Step 2: Write the conformance suite**

Create `store/storetest/storetest.go`. It is a normal package (not `_test.go`) so both store packages can import it. Cover, at minimum:

```go
package storetest

import (
	"context"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

// RunLedger runs the ledger-layer conformance suite against a store. Both
// store/mem and store/pg must pass it identically — this is the main defence
// against the two implementations drifting.
func RunLedger(t *testing.T, newStore func(*testing.T) ledger.Store) {
	t.Helper()

	t.Run("NextSubledgerBlockStepsBy100PerBook", func(t *testing.T) { /* 100, 200, 300; independent per book */ })
	t.Run("NextAccountSeqResetsPerTypeAndSubledger", func(t *testing.T) { /* (100,"100") and (200,"100") count independently */ })
	t.Run("SameIDsInDifferentBooks", func(t *testing.T) { /* PutAccount("200.100.001") in bookA and bookB both readable */ })
	t.Run("ListOrderingIsCreatedAtThenID", func(t *testing.T) { /* ties broken by ID */ })
	t.Run("DuplicateIdempotencyKeyRejected", func(t *testing.T) { /* second PutTransaction => ledger.ErrDuplicateIdempotencyKey */ })
	t.Run("EmptyIdempotencyKeyNotDeduplicated", func(t *testing.T) { /* many "" keys all succeed */ })
	t.Run("BookBalanceIncludesReversedTransactions", func(t *testing.T) { /* reversal entries cancel; status is informational */ })
	t.Run("MarkReversedIsConditional", func(t *testing.T) { /* second call => ledger.ErrTransactionAlreadyReversed */ })
	t.Run("AuditOrderedBySeq", func(t *testing.T) { /* strictly increasing under a frozen clock */ })
	t.Run("AuditFilterByScopeTypeEntityAndBefore", func(t *testing.T) { /* each filter narrows correctly */ })
	t.Run("ResetClearsEverything", func(t *testing.T) { /* all List* empty, counters restart */ })
	t.Run("UpdateRollsBackOnError", func(t *testing.T) { /* fn returns err => no rows written */ })
}
```

Write each subtest body out in full — the sketch above names them and states the assertion; do not leave the bodies as comments.

- [ ] **Step 3: Implement `store/mem` and run the suite against it**

`store/mem/mem.go` holds the maps currently on `Book` (`ledger/book.go:34-53`), keyed by `BookID` at the outer level, plus one `sync.RWMutex`. `Update` takes the write lock, `View` the read lock. `store/mem/tx.go` implements `ledger.Tx` against those maps; `LockAccounts` is a no-op with a comment saying why.

`store/mem/mem_test.go`:

```go
package mem_test

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/store/mem"
	"github.com/raphi011/cbs/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.RunLedger(t, func(t *testing.T) ledger.Store {
		return mem.New(func() time.Time { return time.Unix(0, 0).UTC() })
	})
}
```

Run: `go test ./store/... -v`
Expected: PASS

- [ ] **Step 4: Rewrite `Book` against the store**

`Book` keeps every validation in `PostTransaction` (`ledger/book.go:290-366`) — non-empty entries, positive amounts, accounts exist, idempotency, balanced, sufficient balance — and loses its maps, its mutex, and its counters. It gains `store Store`, `id BookID`, `clock func() time.Time`.

Each mutator splits in two. `PostTransactionTx` does the work; `PostTransaction` wraps it:

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

Inside `PostTransactionTx`, call `tx.LockAccounts(ctx, s.id, ids)` — with `ids` collected from the entries — **before** `checkSufficientBalance`, and have `checkSufficientBalance` read balances via `tx.BookBalance`. Drop `computeBookBalance` from `ledger/book.go` entirely; the aggregate lives in the store.

Thread `ctx` through `ledger/list.go` the same way, using `View`.

- [ ] **Step 5: Thread `ctx` through the ledger tests**

`ledger/book_test.go`, `list_test.go`, `numbering_test.go`, `audit_test.go`: mechanical — `ctx := context.Background()` at the top of each test, `NewBook(mem.New(fixedClock), "bank", fixedClock)` instead of `NewBook()`.

`numbering_test.go`'s assertions must not otherwise change. If a subledger or account ID comes out different, the counters were ported wrong — fix the store, not the test.

- [ ] **Step 6: Run the full gate**

Run: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`
Expected: PASS. `deposit`, `payment`, `api` and `seed` will not compile yet if they call the changed signatures — fix those call sites mechanically here so the tree stays green; the behavioural moves happen in Tasks 4 and 5.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "Move ledger state behind ledger.Store

Book keeps all validation and orchestration; the maps, the counters and
the mutex move into store/mem. BookBalance becomes a store aggregate
rather than a full replay in Go. storetest is the conformance suite both
implementations must pass."
```

---

### Task 4: `deposit` on the store

**Files:**
- Create: `deposit/store.go`
- Modify: `deposit/register.go`, `deposit/list.go`, `deposit/register_test.go`, `deposit/list_test.go`, `store/mem/mem.go`, `store/mem/tx.go`, `store/storetest/storetest.go`

**Interfaces:**
- Consumes: `ledger.Tx`, `ledger.Store`, `Book.PostTransactionTx`, `Book.CreateAccountTx` from Task 3.
- Produces:
  - `deposit.Store` (same four methods as `ledger.Store`, with `deposit.Tx`), `deposit.Tx` embedding `ledger.Tx`
  - `deposit.NewRegister(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time) *Register`
  - `Register.CreateHoldTx`, `Register.CaptureHoldTx`, `Register.OpenAccountTx`
  - `storetest.RunDeposit(t *testing.T, newStore func(*testing.T) deposit.Store)`

- [ ] **Step 1: Define `deposit/store.go`**

```go
package deposit

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds ledger.Tx so one concrete transaction spans both layers. That is
// what makes CaptureHold — a hold write plus a GL posting — a single unit of
// work rather than two that can half-fail.
type Tx interface {
	ledger.Tx

	// Named with a Deposit prefix because ledger.Tx, embedded above, already has
	// PutAccount/GetAccount/ListAccounts for GL accounts. Go rejects an interface
	// carrying two methods of the same name and different signatures, so the
	// ledger keeps the bare names and deposit prefixes.
	PutDepositAccount(ctx context.Context, book ledger.BookID, a Account) error
	GetDepositAccount(ctx context.Context, book ledger.BookID, id AccountID) (Account, error)
	ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]Account, error)

	PutHold(ctx context.Context, book ledger.BookID, h Hold) error
	GetHold(ctx context.Context, book ledger.BookID, id HoldID) (Hold, error)
	ListHoldsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]Hold, error)
	// ActiveHoldTotal sums active, non-expired holds as at `now`.
	ActiveHoldTotal(ctx context.Context, book ledger.BookID, id AccountID, now time.Time) (ledger.Amount, error)

	PutSnapshot(ctx context.Context, book ledger.BookID, s Snapshot) error
	GetSnapshot(ctx context.Context, book ledger.BookID, id AccountID, dateKey string) (Snapshot, error)
}
```

The same collision rule applies to `GetHold` versus anything the ledger later adds: when a name is already taken by an embedded interface, the outer layer prefixes. Nothing else in the set above collides today.

- [ ] **Step 2: Extend `store/mem` and `storetest`**

Add the deposit maps to `store/mem` (`accounts`, `holds`, `accountHolds`, `snapshots` from `deposit/register.go:34-45`, each keyed by `BookID` first). Add `storetest.RunDeposit` covering: hold totals exclude released, captured and expired holds; snapshot upsert by `(account, dateKey)` overwrites; deposit accounts list in `CreatedAt` then `ID` order.

- [ ] **Step 3: Write the failing cross-layer atomicity test**

In `deposit/register_test.go`:

```go
// CaptureHold writes a hold status and posts a GL transaction. They must commit
// together: if the posting fails, the hold must remain Active.
func TestCaptureHoldIsAtomic(t *testing.T) {
	ctx := context.Background()
	reg, book, acct := newFundedAccount(t) // helper: account with 1000 available

	h, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: acct.ID, Amount: 500})
	assertNoError(t, err)

	// Capture against a counterparty account that does not exist, so the GL
	// posting fails after the hold write.
	_, err = reg.CaptureHold(ctx, h.ID, ledger.AccountID("no.such.account"), 500, "boom")
	assertError(t, err, ledger.ErrAccountNotFound)

	got, err := reg.GetHold(ctx, h.ID)
	assertNoError(t, err)
	assertEqual(t, "hold status after failed capture", got.Status, HoldActive)
	_ = book
}
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./deposit/ -run TestCaptureHoldIsAtomic -v`
Expected: FAIL — `hold status after failed capture = Captured, want Active`, or a compile error until `CaptureHold` takes `ctx`.

- [ ] **Step 5: Rewrite `Register` against the store**

Drop the maps, the `sync.RWMutex` and `idCounter`. `Register` keeps two fields for the ledger below it: `gl *ledger.Book` (the orchestrator it composes with) and `bookID ledger.BookID` (the scope every store call needs). `CaptureHold` becomes one `Update` that calls `Book.PostTransactionTx` with the same `tx`:

```go
func (r *Register) CaptureHoldTx(ctx context.Context, tx Tx, id HoldID, counterparty ledger.AccountID, captureAmount ledger.Amount, description string) (ledger.Transaction, error) {
	h, err := tx.GetHold(ctx, r.bookID, id)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if h.Status != HoldActive {
		return ledger.Transaction{}, ErrHoldNotActive
	}
	acct, err := tx.GetDepositAccount(ctx, r.bookID, h.AccountID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if captureAmount <= 0 {
		captureAmount = h.Amount
	}

	// Same tx as the hold write below — both commit or neither does.
	glTx, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		Entries: []ledger.Entry{
			{AccountID: acct.GLAccount, Amount: captureAmount, Direction: ledger.Debit},
			{AccountID: counterparty, Amount: captureAmount, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return ledger.Transaction{}, err
	}

	h.Status = HoldCaptured
	if err := tx.PutHold(ctx, r.bookID, h); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, r.appendAuditTx(ctx, tx, ledger.EventHoldCaptured, string(h.ID), map[string]string{
		"hold_id":        string(h.ID),
		"transaction_id": string(glTx.ID),
	})
}
```

- [ ] **Step 6: Run it to verify it passes**

Run: `go test ./deposit/ -run TestCaptureHoldIsAtomic -v`
Expected: PASS

- [ ] **Step 7: Run the full gate**

Run: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "Move deposit state behind deposit.Store

deposit.Tx embeds ledger.Tx, so CaptureHold's hold write and GL posting
are one unit of work. A failed posting now leaves the hold Active instead
of half-capturing it."
```

---

### Task 5: `payment` on the store, `BookID` scoping, and atomic settlement

**Files:**
- Create: `payment/store.go`
- Modify: `payment/system.go`, `payment/participant.go`, `payment/list.go`, `payment/doc.go`, `payment/system_test.go`, `payment/list_test.go`, `store/mem/*`, `store/storetest/storetest.go`, `api/*.go`, `cmd/server/main.go`, `seed/seed.go`, `seed/seed_test.go`, `api/server.go`, `api/handlers_admin.go`, `api/server_test.go`

**Interfaces:**
- Consumes: `deposit.Tx`, `deposit.Store`, `Register.CaptureHoldTx` from Task 4.
- Produces:
  - `payment.Store`, `payment.Tx` embedding `deposit.Tx`
  - `payment.NewNetwork(store Store, clock func() time.Time) *Network`
  - `storetest.RunPayment(t *testing.T, newStore func(*testing.T) payment.Store)`
  - `Server.Reset(ctx context.Context) error`

- [ ] **Step 1: Define `payment/store.go`**

```go
package payment

import (
	"context"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds deposit.Tx, which embeds ledger.Tx. One concrete transaction
// therefore spans all three layers, which is what lets SettleCycle post across
// every participant's book and the central bank's as a single unit of work.
//
// Network-scoped entities — participants, payments, mandates, cycles,
// settlements — belong to no single bank and are stored under
// ledger.NetworkBook.
type Tx interface {
	deposit.Tx

	PutParticipant(ctx context.Context, p Participant) error
	GetParticipant(ctx context.Context, id ParticipantID) (Participant, error)
	ListParticipants(ctx context.Context) ([]Participant, error)

	PutPayment(ctx context.Context, p Payment) error
	GetPayment(ctx context.Context, id PaymentID) (Payment, error)
	GetPaymentByEndToEndID(ctx context.Context, endToEndID string) (Payment, error)
	ListPayments(ctx context.Context) ([]Payment, error)

	PutMandate(ctx context.Context, m Mandate) error
	GetMandate(ctx context.Context, id MandateID) (Mandate, error)
	ListMandates(ctx context.Context) ([]Mandate, error)

	PutCycle(ctx context.Context, c ClearingCycle) error
	GetCycle(ctx context.Context, id CycleID) (ClearingCycle, error)
	// GetOpenCycle returns the single open cycle for a scheme, or the existing
	// ErrCycleNotFound. Replaces the openCycle map on Network.
	GetOpenCycle(ctx context.Context, scheme SchemeID) (ClearingCycle, error)
	ListCycles(ctx context.Context) ([]ClearingCycle, error)

	PutSettlement(ctx context.Context, s Settlement) error
	GetSettlement(ctx context.Context, id SettlementID) (Settlement, error)
	ListSettlements(ctx context.Context) ([]Settlement, error)
}
```

These take no `BookID` parameter because they are all network-scoped; the implementation writes `ledger.NetworkBook` internally. Where a bare method name is already taken by an embedded interface, prefix with the layer name — nothing in the set above collides today.

- [ ] **Step 2: Introduce `BookID` and collapse onto one store**

`Network` stops constructing one `*ledger.Book` per participant with its own lock. It holds one `payment.Store` and mints a `ledger.BookID` per participant (`ledger.BookID(participantID)`), plus `ledger.BookID("central")` for the central bank. `Participant` holds its `BookID` and a `*ledger.Book` / `*deposit.Register` bound to that ID over the shared store. Network-scoped entities use `ledger.NetworkBook`.

- [ ] **Step 3: Write the failing settlement-atomicity test**

In `payment/system_test.go`:

```go
// SettleCycle posts across every participant's book and the central bank's. One
// store means one transaction: a failure partway must leave no postings at all.
func TestSettleCycleIsAtomic(t *testing.T) {
	ctx := context.Background()
	net, cycleID := newClosedCycleWithUnderfundedMember(t) // one member lacks reserves

	before := reserveBalances(t, ctx, net)

	_, err := net.SettleCycle(ctx, cycleID)
	if err == nil {
		t.Fatal("SettleCycle succeeded, want failure on the underfunded member")
	}

	after := reserveBalances(t, ctx, net)
	for id, want := range before {
		assertEqual(t, "reserve balance for "+string(id), after[id], want)
	}
}
```

Write `newClosedCycleWithUnderfundedMember` and `reserveBalances` out in full, following the existing fixtures in `payment/system_test.go`.

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./payment/ -run TestSettleCycleIsAtomic -v`
Expected: FAIL — some members' reserves moved before the failing one aborted the cycle.

- [ ] **Step 5: Wrap `SettleCycle` in one `Update`**

Every posting in the cycle — each participant's book, the central bank's — uses the same `tx`.

- [ ] **Step 6: Run it to verify it passes**

Run: `go test ./payment/ -run TestSettleCycleIsAtomic -v`
Expected: PASS

- [ ] **Step 7: Rewrite the three cross-ledger-atomicity claims**

They are now false. Rewrite, do not delete — each becomes an explanation of why a real RTGS needs a locked settlement window and how a database transaction supplies one:
- the `# Multiple ledgers, no cross-ledger atomicity` block at `payment/system.go:13-25`
- the *Deliberate Simplifications* list in `payment/doc.go`
- the same-named list in `README.md`

- [ ] **Step 8: Thread `ctx` through `api/`, `cmd/server/main.go` and `seed/`**

Mechanical. Handlers pass `r.Context()`.

- [ ] **Step 9: Make reset actually reset**

`Server.Reset()` (`api/server.go:41`) currently stores a freshly built `*payment.Network`, which under Postgres would clear nothing. Change to:

```go
// Reset discards all persisted state and rebuilds the sample dataset. It must
// go through the store: swapping an in-memory object graph would leave every
// row in the database intact and still report success.
func (s *Server) Reset(ctx context.Context) error {
	if err := s.store.Reset(ctx); err != nil {
		return err
	}
	return s.seed(ctx)
}
```

`handleReset` (`api/handlers_admin.go`) calls it with `r.Context()` and writes the error through `writeError` on failure.

Add to `api/server_test.go`:

```go
func TestResetEmptiesState(t *testing.T) {
	srv := newTestServer(t)

	// Mutate, reset, then assert the mutation is gone and the seed is back.
	post(t, srv, "/participants/bank_1/deposit-accounts", `{"name":"Temp","overdraftLimit":0}`)
	post(t, srv, "/admin/reset", "")

	var accounts []depositAccountDTO
	getJSON(t, srv, "/participants/bank_1/deposit-accounts", &accounts)
	for _, a := range accounts {
		if a.Name == "Temp" {
			t.Fatal("account survived reset")
		}
	}
}
```

- [ ] **Step 10: Make seeding idempotent**

`seed.Network` becomes `seed.Populate(ctx, store)`, which returns early when `ListParticipants` is non-empty. Otherwise every restart against a live database re-seeds.

- [ ] **Step 11: Run the full gate**

Run: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`
Expected: PASS

- [ ] **Step 12: Commit**

```bash
git add -A
git commit -m "Move payment state behind payment.Store; settlement is atomic

One store scoped by BookID replaces one ledger.Book per participant, so
SettleCycle wraps every participant's postings and the central bank's in a
single unit of work. The three passages documenting the opposite are
rewritten to explain why a real RTGS needs a locked settlement window.

POST /admin/reset now routes through Store.Reset instead of swapping an
in-memory graph, and seeding is idempotent."
```

---

### Task 6: Payment-layer audit events, pagination, and the audit endpoints

**Files:**
- Create: `api/handlers_audit.go`, `web/src/app/payments/audit/page.tsx`
- Modify: `ledger/audit.go` (payment event constants), `payment/system.go`, `api/handlers_ledger.go`, `api/handlers_deposit.go`, `api/handlers_participant.go` (move the audit routes out), `api/server_test.go`, `web/src/lib/api/endpoints.ts`, `hooks.ts`, `query-keys.ts`, `types.ts`, `web/src/app/participants/[pid]/audit/page.tsx`, `web/src/app/participants/[pid]/deposit-audit/page.tsx`

**Interfaces:**
- Consumes: `ledger.AuditFilter`, `ledger.NetworkBook`, `payment.Tx` from Tasks 2–5.
- Produces: `GET /payments/audit`; `?limit=`, `?before=`, `?type=`, `?entity=` on all four audit endpoints.

- [ ] **Step 1: Add the payment event constants to `ledger/audit.go`**

```go
	// ScopePayment
	EventParticipantAdded = "participant.added"
	EventMandateCreated   = "mandate.created"
	EventMandateRevoked   = "mandate.revoked"
	EventPaymentInitiated = "payment.initiated"
	EventPaymentAccepted  = "payment.accepted"
	EventPaymentRejected  = "payment.rejected"
	EventPaymentCleared   = "payment.cleared"
	EventPaymentSettled   = "payment.settled"
	EventPaymentReturned  = "payment.returned"
	EventCycleOpened      = "cycle.opened"
	EventCycleClosed      = "cycle.closed"
	EventCycleSettled     = "cycle.settled"
```

- [ ] **Step 2: Emit them**

`AddParticipant`, `CreateMandate`, `RevokeMandate`, `InitiatePayment`, `RejectPayment`, `ReturnPayment`, `OpenCycle`, `CloseCycle`, `SettleCycle` in `payment/system.go`. All network-scoped: `BookID: ledger.NetworkBook`, `Scope: ledger.ScopePayment`. Emitted on the operation's own `tx`, so an event never outlives a rolled-back operation.

`CloseCycle` emits `cycle.closed` once plus `payment.cleared` per payment in the cycle; `SettleCycle` emits `cycle.settled` once plus `payment.settled` per payment.

- [ ] **Step 3: Write the failing pagination test**

In `api/server_test.go`:

```go
func TestAuditPaginationByCursor(t *testing.T) {
	srv := newTestServer(t)

	var page1 []auditEventDTO
	getJSON(t, srv, "/payments/audit?limit=2", &page1)
	assertEqual(t, "page size", len(page1), 2)

	var page2 []auditEventDTO
	getJSON(t, srv, fmt.Sprintf("/payments/audit?limit=2&before=%d", page1[len(page1)-1].Seq), &page2)

	for _, a := range page1 {
		for _, b := range page2 {
			if a.Seq == b.Seq {
				t.Fatalf("seq %d appears on both pages", a.Seq)
			}
		}
	}
}

func TestAuditLimitIsCapped(t *testing.T) {
	srv := newTestServer(t)
	var events []auditEventDTO
	getJSON(t, srv, "/payments/audit?limit=99999", &events)
	if len(events) > 1000 {
		t.Fatalf("returned %d events, want <= 1000", len(events))
	}
}
```

- [ ] **Step 4: Run to verify they fail**

Run: `go test ./api/ -run 'TestAudit' -v`
Expected: FAIL — `/payments/audit` 404s.

- [ ] **Step 5: Write `api/handlers_audit.go`**

All four routes in one file, sharing one filter parser:

```go
// auditFilter parses the shared audit query parameters. A durable log is
// unbounded, so limit is defaulted and capped rather than optional.
func auditFilter(r *http.Request, book ledger.BookID, scope ledger.Scope) ledger.AuditFilter {
	const (
		defaultLimit = 100
		maxLimit     = 1000
	)
	f := ledger.AuditFilter{
		BookID:   book,
		Scope:    scope,
		Type:     r.URL.Query().Get("type"),
		EntityID: r.URL.Query().Get("entity"),
		Limit:    defaultLimit,
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		f.Limit = min(v, maxLimit)
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil && v > 0 {
		f.Before = v
	}
	return f
}
```

Register `GET /participants/{pid}/audit` (scope ledger), `GET /participants/{pid}/deposit-audit` (scope deposit), `GET /central-bank/audit` (scope ledger, central book), `GET /payments/audit` (scope payment, `ledger.NetworkBook`). Remove the audit routes from `handlers_ledger.go`, `handlers_deposit.go` and `handlers_participant.go` where Task 1's revert put them.

- [ ] **Step 6: Run to verify they pass**

Run: `go test ./api/ -run 'TestAudit' -v`
Expected: PASS

- [ ] **Step 7: Add the web surface**

`web/src/app/payments/audit/page.tsx` mirroring the restored `participants/[pid]/audit/page.tsx`, plus the `endpoints.ts` / `hooks.ts` / `query-keys.ts` / `types.ts` entries and a nav tab. Extend the `AuditEvent` TS type with `seq`, `scope`, `payload`, `metadata`, and render the payload as formatted JSON in a collapsible cell on all three audit pages.

- [ ] **Step 8: Run the full gate**

Run: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`
Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "Audit the payment layer; paginate every audit endpoint

The payment layer never had an audit log. It gets one, network-scoped,
covering participants, mandates, payments and cycles. A durable log is
unbounded, so all four endpoints take limit/before/type/entity."
```

---

### Task 7: `store/pg`

**Prerequisite:** raise the `go` directive in `go.mod` from `1.24.7` to `1.25` (`pgx/v5` v5.10 requires ≥1.25; the installed toolchain is 1.26). Record the change in the commit message.

**Files:**
- Create: `store/pg/pg.go`, `store/pg/migrate.go`, `store/pg/tx_ledger.go`, `store/pg/tx_deposit.go`, `store/pg/tx_payment.go`, `store/pg/tx_audit.go`, `store/pg/schema/0001_init.sql`, `store/pg/pg_test.go`
- Modify: `go.mod`, add `go.sum`

**Interfaces:**
- Consumes: `ledger.Store`/`Tx`, `deposit.Store`/`Tx`, `payment.Store`/`Tx`, `storetest.RunLedger`/`RunDeposit`/`RunPayment`.
- Produces: `pg.Open(ctx context.Context, dsn string, clock func() time.Time) (*pg.Store, error)` — connects, then applies embedded migrations.

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/jackc/pgx/v5
```

- [ ] **Step 2: Write `0001_init.sql`**

Composite `(book_id, id)` PKs and composite FKs throughout. `BIGINT` amounts, `TIMESTAMPTZ` times, `SMALLINT` enums, `JSONB` payload/metadata, explicit `entries.seq`. Tables: `books`, `ledgers`, `subledgers`, `accounts`, `transactions`, `entries`, `deposit_accounts`, `holds`, `snapshots`, `participants`, `mandates`, `payments`, `cycles`, `cycle_payments`, `settlements`, `settlement_positions`, `audit_events`, `id_sequences`, `schema_migrations`. Plus the six indexes and the partial unique index from the spec's *Indexes* section.

- [ ] **Step 3: Write the migrator**

`go:embed schema/*.sql`; a `schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ)` table; files sorted by name; each unapplied file run in its own transaction. ~40 lines, no migration-tool dependency.

- [ ] **Step 4: Implement `*pg.Tx` for all three layers**

One concrete type across `tx_ledger.go`, `tx_deposit.go`, `tx_payment.go`, `tx_audit.go`. Required specifics:

```sql
-- BookBalance: aggregate, never a replay
SELECT COALESCE(SUM(CASE WHEN e.direction = $3 THEN e.amount ELSE -e.amount END), 0)
  FROM entries e WHERE e.book_id = $1 AND e.account_id = $2;

-- LockAccounts: ordered, so overlapping account sets cannot deadlock
SELECT id FROM accounts WHERE book_id = $1 AND id = ANY($2) ORDER BY id FOR UPDATE;

-- MarkReversed: conditional, zero rows => ErrTransactionAlreadyReversed
UPDATE transactions SET status = $3 WHERE book_id = $1 AND id = $2 AND status = $4;

-- NextID: gap-free per book, rolls back with the caller's transaction
INSERT INTO id_sequences (book_id, prefix, next_value) VALUES ($1, $2, 1)
ON CONFLICT (book_id, prefix) DO UPDATE SET next_value = id_sequences.next_value + 1
RETURNING next_value;
```

Idempotency is a plain insert; map SQLSTATE `23505` on the partial unique index to `ledger.ErrDuplicateIdempotencyKey` — never check-then-insert, which has a window the index does not. `Update` retries on SQLSTATE `40001` and `40P01` with a bounded attempt count. `Reset` truncates all tables in FK-safe order.

- [ ] **Step 5: Run the conformance suite against Postgres**

`store/pg/pg_test.go`:

```go
func TestConformance(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres conformance tests")
	}
	frozen := func() time.Time { return time.Unix(0, 0).UTC() }
	newStore := func(t *testing.T) *pg.Store {
		// Fresh schema per test + search_path, so tests stay parallel-safe
		// without depending on truncation order.
		return openInFreshSchema(t, dsn, frozen)
	}
	storetest.RunLedger(t, func(t *testing.T) ledger.Store { return newStore(t) })
	storetest.RunDeposit(t, func(t *testing.T) deposit.Store { return newStore(t) })
	storetest.RunPayment(t, func(t *testing.T) payment.Store { return newStore(t) })
}
```

Write `openInFreshSchema(t *testing.T, dsn string, clock func() time.Time) *pg.Store` out in full: `CREATE SCHEMA test_<n>` (n from a package-level atomic counter), set `search_path` on the pool config, migrate, and `t.Cleanup` a `DROP SCHEMA … CASCADE` plus `Close`. Put it in a small internal test-helper package rather than a `_test.go` file — Step 6 imports it from the domain suites.

Run: `go test ./store/... -v` (skips without a database)
Run: `TEST_DATABASE_URL=postgres://... go test ./store/... -v`
Expected: PASS both ways.

- [ ] **Step 6: Run the existing domain suites against Postgres**

The conformance suite proves the two stores agree on the storage contract. It does not exercise the domain logic layered on top — and cross-layer atomicity only has teeth against a real transaction. Make the domain suites store-agnostic:

```go
// storeUnderTest returns the store the domain suites run against. Defaults to
// the in-memory one so `go test ./...` needs no setup; TEST_DATABASE_URL opts
// into Postgres, which is where cross-layer atomicity is genuinely tested.
func storeUnderTest(t *testing.T, clock func() time.Time) payment.Store {
	t.Helper()
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		return openInFreshSchema(t, dsn, clock)
	}
	return mem.New(clock)
}
```

Route the constructors in `ledger/book_test.go`, `deposit/register_test.go`, `payment/system_test.go`, `api/server_test.go` and `seed/seed_test.go` through it. `openInFreshSchema` lives in a small internal test-helper package both `store/pg` and the domain suites can import — putting it in `store/pg`'s `_test.go` files would make it unimportable.

Run: `go test ./...`
Expected: PASS, using `mem`.

Run: `TEST_DATABASE_URL=postgres://... go test ./...`
Expected: PASS, using `pg`. `TestCaptureHoldIsAtomic` and `TestSettleCycleIsAtomic` are the two that matter here — they pass trivially under a single mutex and non-trivially under a real transaction.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "Add the Postgres store

Raises the go directive to 1.25 — pgx/v5 v5.10 requires it. Balances are a
SUM aggregate, not a replay. Three read-then-write races the single mutex
hid are closed: ordered SELECT FOR UPDATE before a balance check, a partial
unique index for idempotency, and a conditional UPDATE for reversal."
```

---

### Task 8: Wiring, docs, and the curriculum sweep

**Files:**
- Create: `docker-compose.yml`, `web/src/lib/quiz/chapters/15-persistence-and-durability.ts`
- Modify: `cmd/server/main.go`, `Makefile`, `README.md`, `web/src/components/hint-content.ts`, `web/src/lib/quiz/index.ts`, `CLAUDE.md`

**Interfaces:**
- Consumes: `pg.Open`, `mem.New`, `seed.Populate` from Tasks 5 and 7.

- [ ] **Step 1: Store selection in `main.go`**

```go
database := flag.String("database", os.Getenv("DATABASE_URL"), "Postgres DSN; empty uses the in-memory store")
```

There is no shared `store.Store` type — the interfaces are per-layer and one concrete store satisfies all three. Hold the widest:

```go
var st payment.Store
if *database == "" {
	st = mem.New(time.Now)
	log.Info("using in-memory store; state resets on restart")
} else {
	pgStore, err := pg.Open(ctx, *database, time.Now) // connects, then migrates
	if err != nil {
		log.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	st = pgStore
	log.Info("using postgres store", "dsn", redact(*database))
}
defer st.Close()
```

Write `redact` so the password never reaches the log.

- [ ] **Step 2: `docker-compose.yml` and Makefile targets**

One `postgres:16` service. Add `db-up`, `db-down`, `test` and `test-pg` targets — the Makefile currently has no test target at all. Each needs a `## ` comment so `make help` picks it up.

- [ ] **Step 3: README**

Add a persistence section covering: the ledger→relational mapping, why a balance is an aggregate rather than a stored column, unit of work and why cross-layer atomicity needs one `Tx`, and the three races a single mutex was hiding. Qualify the dependency claim — the library core stays stdlib-only, `store/pg` is the exception. Confirm the *Audit Trail* section restored in Task 1 still matches the hardened behaviour (payload snapshotting, `seq` ordering, pagination).

- [ ] **Step 4: `hint-content.ts`**

Confirm the `audit-trail` key restored in Task 1 is present and its `[[wiki-links]]` resolve. Add keys for the new concepts — at minimum `unit-of-work`, `derived-balance`, `idempotency-race`. `HintKey` is `keyof typeof hintContent`, so new keys are picked up automatically and become usable as a question's `concept`.

- [ ] **Step 5: Write quiz chapter 15**

`web/src/lib/quiz/chapters/15-persistence-and-durability.ts`, following the exact shape of `14-snapshots-audit-and-statements.ts`:

```ts
import type { Chapter } from "../types";

export const chapter: Chapter = {
  slug: "15-persistence-and-durability",
  number: 15,
  part: "Part VI · Making It Durable",
  title: "Persistence, Transactions, and Durability",
  questions: [ /* ~20 */ ],
};
```

`diversity.test.ts` enforces, per chapter: 18–22 questions, at least 8 distinct `concept` tags, no tag used more than 3×, and all three `difficulty` tiers present. Chapter 14 already covers the audit trail, so 15 stays on storage: the relational mapping, derived vs stored balances, transaction boundaries and rollback, the three races, idempotency under concurrency, composite keys and why IDs are book-scoped. Question IDs are `ch15-q1`…; explanations use `[[wiki-links]]` to hint keys that exist.

Register it in `web/src/lib/quiz/index.ts` — the import, the `chapters` array, and update the `/** 1..14 */` comment on `Chapter.number` in `web/src/lib/quiz/types.ts` to `1..15`.

- [ ] **Step 6: Run the quiz tests**

Run: `cd web && npm run test`
Expected: PASS, including `diversity.test.ts` for the new chapter.

- [ ] **Step 7: Update `CLAUDE.md`**

Its "Domain knowledge stays consistent across layers" section lists `README.md`, `hint-content.ts` and `quiz/chapters/*.ts`. Add `store/pg/schema/0001_init.sql` as a place domain facts now live, and note that persistence claims must stay consistent with it.

- [ ] **Step 8: Full gate, both stores**

Run: `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`
Run: `TEST_DATABASE_URL=postgres://... go test ./... `
Run: `cd web && npm run typecheck && npm run lint && npm run test && npm run build`

Manual check against Postgres — this is the one that catches a reset that only appears to work:

```bash
make db-up
DATABASE_URL=postgres://... go run ./cmd/server &
curl -s localhost:8080/participants | head -c 200        # seeded
curl -sX POST localhost:8080/admin/reset
curl -s localhost:8080/participants | head -c 200        # re-seeded, not duplicated
psql "$DATABASE_URL" -c 'SELECT count(*) FROM audit_events;'
```

Restart the process and confirm state survives.

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "Wire up store selection, document persistence, add quiz chapter 15

Postgres stays optional: no DATABASE_URL means the in-memory store, so
make dev, make run and go test ./... need zero setup."
```
