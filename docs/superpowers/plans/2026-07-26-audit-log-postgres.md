# Audit Log + Postgres Backing Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore the audit log removed in `81135a9`, and move all state behind a store
abstraction with two implementations — in-memory (default) and Postgres.

**Architecture:** Consumer-defined `Store`/`Tx` interfaces in `ledger`, `deposit` and `payment`;
implementations in `store/mem` and `store/pg`; a shared `store/storetest` conformance suite.
`payment.Tx` embeds `deposit.Tx` embeds `ledger.Tx`, so one concrete `Tx` makes cross-layer
operations atomic. `Book`/`Register`/`Network` keep all validation and lose their maps and mutex.

**Tech Stack:** Go 1.24.7 (see Task 6 prerequisite), `github.com/jackc/pgx/v5`, Postgres 16,
Next.js 16 frontend.

**Design spec:** `docs/superpowers/specs/2026-07-26-audit-log-postgres-design.md`

## Global Constraints

- **Postgres is optional.** No database ⇒ `store/mem`. `make dev`, `make run` and `go test ./...`
  must work with zero setup, always.
- **IDs stay exactly as the CoA spec defines them** (`200.100.001`, subledgers `100`/`200`,
  `ldg_`/`tx_`/`ent_`/`evt_` via the shared counter). Only *where the counters live* changes.
  `ledger/numbering_test.go` is the regression gate and must not be edited to accommodate the
  refactor.
- **IDs are unique per book, not globally.** Every book-scoped table uses a composite
  `PRIMARY KEY (book_id, id)` and composite FKs. A single-column `id` PK is a bug.
- **`ctx context.Context` is the first parameter** of every public domain method.
- Each mutating method has an exported `…Tx(ctx, tx, …)` sibling; the plain method is a thin
  `Update` wrapper around it.
- Regression gate for every task from Task 1 on:
  `go build ./... && go vet ./... && go test ./... && test -z "$(gofmt -l .)"`.
  Tasks touching `web/`: also `cd web && npm run typecheck && npm run lint && npm run build`.
- Do **not** restore `web/PLAN.md` — deliberately deleted, superseded by `web/CLAUDE.md`.

---

### Task 1: Restore the audit log by reverting `81135a9`

Land the feature back exactly as it was, green, before any storage work — so the Postgres diff
stays readable.

**Files:** the 24 touched by `81135a9`, minus `web/PLAN.md`.

- [ ] **Step 1: Apply the inverse patch**

```bash
git revert --no-commit 81135a9e5b85ace1aad48748187be6a6bf04acc5
git checkout HEAD -- web/PLAN.md
```

- [ ] **Step 2: Resolve conflicts in the four files touched since 06-24**

`web/src/components/hint-content.ts`, `web/src/lib/api/hooks.ts`,
`web/src/app/central-bank/page.tsx`, `web/src/app/participants/[pid]/layout.tsx`. Keep the newer
quiz/concept work and re-add the audit pieces alongside it — do not clobber either side.

- [ ] **Step 3: Verify the restored surface**

`ledger/book_test.go` audit tests pass; `GET /participants/{pid}/audit`,
`GET /participants/{pid}/deposit-audit`, `GET /central-bank/audit` respond; both web pages render
and the nav tabs appear.

- [ ] **Step 4: Commit** — "Restore the audit log feature", noting it reverts `81135a9` and why.

---

### Task 2: Harden the restored audit log

Fix the three defects the original had, still entirely in-memory.

**Files:** Modify `ledger/types.go`, `ledger/book.go`, `deposit/types.go`, `deposit/register.go`,
`api/dto.go`, `api/dto_ledger.go`, `api/dto_deposit.go`.

**Interfaces:**
- `AuditEvent.Payload` becomes `json.RawMessage` (was `any`).
- `appendAudit(eventType, entityID string, payload any) error` — marshals, so it can fail.

- [ ] **Step 1:** Marshal `Payload` at append time. The old code stored a live `*Account` /
  `*Transaction`, so a later mutation retroactively rewrote the "immutable" record. Add a test that
  appends an event, mutates the entity, and asserts the stored payload is unchanged.
- [ ] **Step 2:** Add `payload` and `metadata` to `auditEventDTO`; assert both appear in
  `api/server_test.go`.
- [ ] **Step 3:** Commit.

---

### Task 3: `ledger.Store` / `ledger.Tx` + `store/mem`

The pivot. Move `Book`'s maps into `store/mem` behind the interface, keeping behaviour identical.

**Files:**
- Create: `ledger/store.go` (interfaces), `store/mem/mem.go`, `store/mem/mem_test.go`,
  `store/storetest/storetest.go`
- Modify: `ledger/book.go` (drop maps + mutex, take a `Store`, thread `ctx`, add `…Tx` siblings),
  `ledger/list.go`, `ledger/book_test.go`, `ledger/list_test.go`, `ledger/numbering_test.go`

**Interfaces:**
- `Store` — `Update`, `View`, `Reset`, `Close` as in the design spec.
- `Tx` — ledger/subledger/account/transaction CRUD, `NextID(prefix)`, `NextSubledgerBlock()`,
  `NextAccountSeq(typeBlock, subledgerID)`, `BookBalance(accountID, accountType)`,
  `MarkReversed(txID)` (conditional), `AppendAudit`, `ListAudit(filter)`.

- [ ] **Step 1:** Define `ledger.Store` and `ledger.Tx`.
- [ ] **Step 2:** Implement `store/mem` — the maps from `ledger/book.go:34-53`, one `sync.RWMutex`,
  the `subledgerSeq`/`accountSeq`/`nextID` counters keyed by `BookID`.
- [ ] **Step 3:** Write `store/storetest` covering every `Tx` method, `List*` ordering, numbering
  determinism, idempotency uniqueness, balance aggregation over reversed transactions, conditional
  reversal, `Reset`, audit ordering. Run it from `store/mem`.
- [ ] **Step 4:** Rewrite `Book` against the store: `ctx` everywhere, `…Tx` siblings,
  `BookBalance` delegated. `ledger/numbering_test.go` must pass unedited.
- [ ] **Step 5:** Commit.

---

### Task 4: `deposit` and `payment` on the store, and `BookID` scoping

**Files:** Modify `deposit/register.go`, `deposit/list.go`, `payment/system.go`,
`payment/participant.go`, `payment/doc.go`, `api/*.go`, `cmd/server/main.go`, `seed/seed.go`,
and every affected test.

- [ ] **Step 1:** `deposit.Store`/`deposit.Tx` embedding `ledger.Tx`; move `accounts`, `holds`,
  `accountHolds`, `snapshots`, `auditLog` into `store/mem`. Rewrite `CaptureHold` to do the hold
  write and the GL posting in **one** `Update` via `Book.PostTransactionTx`.
- [ ] **Step 2:** `payment.Store`/`payment.Tx`; move `participants`, `payments`, `mandates`,
  `cycles`, `settlements`, `openCycle`, `endToEndIndex` into `store/mem`.
- [ ] **Step 3:** Introduce `BookID`; collapse the per-participant books onto one store instance.
  Wrap `SettleCycle` in a single `Update` so settlement becomes atomic.
- [ ] **Step 4:** Update the three now-false "no cross-ledger atomicity" claims — `README.md`
  *Deliberate Simplifications*, the same list in `payment/doc.go`, and the block comment in
  `payment/system.go`.
- [ ] **Step 5:** Thread `ctx` through `api/`, `cmd/server/main.go` and `seed/`.
- [ ] **Step 6:** Route `Server.Reset()` (`api/server.go`) through `Store.Reset(ctx)` so
  `POST /admin/reset` actually clears storage rather than swapping an in-memory graph. Add an
  `api/server_test.go` case asserting emptiness after reset.
- [ ] **Step 7:** Make `seed/` idempotent — skip when `books` is non-empty.
- [ ] **Step 8:** Commit.

---

### Task 5: Payment-layer audit events

**Files:** Modify `payment/types.go` (new `AuditEventType`), `payment/system.go`,
`api/handlers_payment.go`, `api/dto_payment.go`; create
`web/src/app/payments/audit/page.tsx` plus hook/endpoint/query-key entries.

- [ ] **Step 1:** Add the event constants and emit them: `participant.added`,
  `mandate.created/revoked`, `payment.initiated/accepted/rejected/cleared/settled/returned`,
  `cycle.opened/closed/settled`. Network-scoped ⇒ `book_id = 'network'`, `scope = 'payment'`.
- [ ] **Step 2:** Add `GET /payments/audit` and the web page, mirroring the restored participant
  audit page.
- [ ] **Step 3:** Add `?limit=` (default 100, max 1000), `?before=<seq>`, `?type=` to all audit
  endpoints; expose `seq` on each event for cursor paging.
- [ ] **Step 4:** Commit.

---

### Task 6: `store/pg`

**Prerequisite (resolve first):** `pgx/v5` v5.10.0 needs Go ≥ 1.25 but `go.mod` says `go 1.24.7`.
Either raise the `go` directive and toolchain, or pin `pgx` to v5.7.x. Record the choice in the
commit message.

**Files:** Create `store/pg/pg.go`, `store/pg/tx.go`, `store/pg/migrate.go`,
`store/pg/schema/0001_init.sql`, `store/pg/pg_test.go`; modify `go.mod`, add `go.sum`.

- [ ] **Step 1:** Add the dependency. Note `README.md:` the "dependency-free" claim now needs
  qualifying — the library core stays stdlib-only, `store/pg` is the exception.
- [ ] **Step 2:** Write `0001_init.sql`. Composite `(book_id, id)` PKs and FKs throughout;
  `BIGINT` amounts; `TIMESTAMPTZ`; `SMALLINT` enums; `JSONB` payload/metadata; `entries.seq`;
  `audit_events` with `BIGSERIAL seq`, `scope`, nullable `actor`; `id_sequences`; the six indexes
  and the partial unique index on `(book_id, idempotency_key)`.
- [ ] **Step 3:** `go:embed` migrator — `schema_migrations(version, applied_at)`, files sorted by
  name, each unapplied file in its own transaction.
- [ ] **Step 4:** Implement `Tx` for all three layers on one `*pg.Tx`. Required specifics:
  - `BookBalance` as the `SUM(CASE WHEN direction = normal THEN amount ELSE -amount END)` aggregate.
  - `SELECT … FROM accounts WHERE book_id=$1 AND id = ANY($2) ORDER BY id FOR UPDATE` before any
    posting, to close the `checkSufficientBalance` race.
  - Insert-and-map `23505` → `ErrDuplicateIdempotencyKey`, no check-then-insert.
  - `MarkReversed` as `UPDATE … WHERE status = Posted` with a row-count check →
    `ErrTransactionAlreadyReversed`.
  - `Update` retries on SQLSTATE `40001` / `40P01`, bounded.
  - `Reset` truncates all tables in FK-safe order.
- [ ] **Step 5:** Run `store/storetest` against `pg`, skipped unless `TEST_DATABASE_URL` is set;
  fresh schema + `search_path` per test.
- [ ] **Step 6:** Commit.

---

### Task 7: Wiring, docs, and the domain-consistency sweep

**Files:** Modify `cmd/server/main.go`, `Makefile`, `README.md`,
`web/src/components/hint-content.ts`, `web/src/lib/quiz/chapters/*.ts`, `book/*.md` if needed;
create `docker-compose.yml`.

- [ ] **Step 1:** `-database` flag defaulting to `DATABASE_URL`; `mem` when empty, `pg.Open`
  otherwise (migrating on connect); `defer st.Close()`.
- [ ] **Step 2:** `docker-compose.yml` with one `postgres:16` service; optional `make db-up` /
  `make test-pg` targets (the Makefile currently has no test or lint target).
- [ ] **Step 3:** `README.md` — a persistence section covering both modes, the qualified
  dependency claim, and the restored *Audit Trail* section.
- [ ] **Step 4:** Per `CLAUDE.md`, domain facts are mirrored across `README.md` (authoritative),
  `book/*.md`, `web/src/components/hint-content.ts` and `web/src/lib/quiz/chapters/*.ts`. Sweep all
  four for reinstated audit claims and for the now-atomic settlement. **If any `book/*.md` changes,
  run `make book`** — the EPUB and PDF are committed artifacts and must not go stale.
- [ ] **Step 5:** Full gate, both stores. Manual check:
  `curl -X POST localhost:8080/admin/reset` then re-list under Postgres — state must actually be
  empty.
- [ ] **Step 6:** Commit.
