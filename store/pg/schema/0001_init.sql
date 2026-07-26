-- 0001_init: the whole schema, in one migration.
--
-- Conventions, applied everywhere:
--
--   * Book-scoped tables have a composite PRIMARY KEY (book_id, id). A
--     single-column id would be wrong: chart-of-accounts numbers are unique
--     within a book, not globally, so two participants legitimately both hold
--     an account "200.100.001".
--   * Amounts are BIGINT in minor units. Never NUMERIC and never a float: a
--     ledger deals in whole cents, and a rounding mode is not a thing a ledger
--     may have.
--   * Times are TIMESTAMPTZ and nullable. NULL is Go's zero time.Time, which
--     several fields use as "unset" (a hold that never expires, a cycle that
--     has not closed). Listings therefore say NULLS FIRST, because store/mem
--     sorts the zero time first.
--   * iota enums are SMALLINT, holding the Go constant's value.
--   * Free-form maps and the audit payload are JSONB.
--   * Every listed table carries a monotonic `seq`. Listings are
--     ORDER BY created_at, seq — never ORDER BY id, because IDs are
--     counter-derived strings and "dep_10" sorts before "dep_8". seq is
--     assigned on INSERT and deliberately left alone by the upsert branch, so
--     editing a row does not move it to the end of its list.

-- ---------------------------------------------------------------------------
-- Books
-- ---------------------------------------------------------------------------

-- books exists so that book_id is a real key rather than a convention. Rows are
-- created on demand by the first write that names a book (see tx.ensureBook).
CREATE TABLE books (
    id TEXT PRIMARY KEY
);

-- ---------------------------------------------------------------------------
-- The general ledger
-- ---------------------------------------------------------------------------

-- A note on what is NOT here: UNIQUE (book_id, name).
--
-- It is tempting, because the payment layer resolves the central bank's chart of
-- accounts by find-or-create *by name*, and a unique constraint would turn a
-- lost race into a constraint violation instead of silent divergence. It cannot
-- go in, for two reasons.
--
-- First, the domain does not hold that invariant. A ledger, a subledger and an
-- account are identified by their generated ID; names are labels and are allowed
-- to repeat. ledger/numbering_test.go's TestSubledgerNumbering creates three
-- subledgers all called "S" in one book precisely to check the numbering, and
-- store/mem accepts it. A constraint here would make store/pg reject writes
-- store/mem accepts, which is the one thing this package must never do.
--
-- Second, the race is already closed, one layer up. AddParticipantTx's very
-- first statement is NextID(NetworkBook, "bank"), which takes a row lock on
-- id_sequences; a second concurrent AddParticipant therefore blocks there until
-- the first commits, and then sees the Central Bank ledger the first created.
-- The gap-free counter serializes the whole operation, not just the number.
-- store/pg's TestConcurrentAddParticipantsAgreeOnOneCentralBank pins that.
--
-- The consequence to watch: if AddParticipantTx ever stops drawing a
-- network-scoped ID first, the find-or-create becomes racy again and will need
-- its own lock, because there is no constraint behind it.
CREATE TABLE ledgers (
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id         TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ,
    seq        BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

-- subledgers.ledger_id carries NO foreign key, and neither does
-- accounts.subledger_id or entries.account_id. That is a rule about what this
-- layer is for, not an oversight.
--
-- The store is a per-table key/value layer. "The parent must exist" is a domain
-- rule, and ledger.Book enforces it: CreateSubledgerTx reads the ledger first,
-- CreateAccountTx reads the subledger, PostTransactionTx reads every account it
-- is about to touch. Put the same rule in the schema and store/pg starts
-- rejecting writes store/mem accepts — a divergence between the two
-- implementations, which is the single thing this package must never introduce.
--
-- This one used to be a composite FK, and it was wrong for exactly that reason:
--
--   PutSubledger{ID: "100", LedgerID: "ldg_nope"}
--     store/mem = nil
--     store/pg  = SQLSTATE 23503, violates subledgers_book_id_ledger_id_fkey
--
-- storetest/ParentReferencesAreNotEnforced now pins the answer, so the rule is
-- enforced by the suite rather than remembered.
--
-- The child-table FKs below (entries -> transactions, cycle_payments -> cycles,
-- settlement_positions -> settlements) are a different case and stay: the store
-- writes both sides of those itself, within one statement sequence, so there is
-- no caller who could ever produce an orphan.
CREATE TABLE subledgers (
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id         TEXT NOT NULL,
    ledger_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ,
    seq        BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

CREATE TABLE accounts (
    book_id      TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id           TEXT NOT NULL,
    subledger_id TEXT NOT NULL,
    name         TEXT NOT NULL,
    type         SMALLINT NOT NULL,
    created_at   TIMESTAMPTZ,
    seq          BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

CREATE TABLE transactions (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id              TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    booking_date    TIMESTAMPTZ,
    value_date      TIMESTAMPTZ,
    status          SMALLINT NOT NULL,
    description     TEXT NOT NULL,
    metadata        JSONB,
    reversal_of     TEXT NOT NULL,
    created_at      TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

-- The idempotency index. Partial, because an empty key is an absent key rather
-- than an identity, and unique, because that is the only way to make a
-- duplicate impossible: a check-then-insert in Go has a window between the two
-- statements that this index does not. store/pg inserts and maps SQLSTATE 23505
-- to ledger.ErrDuplicateIdempotencyKey.
CREATE UNIQUE INDEX transactions_idempotency_key_idx
    ON transactions (book_id, idempotency_key)
    WHERE idempotency_key <> '';

-- entries.position is explicit because Transaction.Entries is an ordered slice
-- and a table has no order. entries.account_id carries no foreign key, for the
-- same reason accounts.subledger_id does not.
CREATE TABLE entries (
    book_id        TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    position       INTEGER NOT NULL,
    id             TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    amount         BIGINT NOT NULL,
    direction      SMALLINT NOT NULL,
    PRIMARY KEY (book_id, transaction_id, position),
    FOREIGN KEY (book_id, transaction_id) REFERENCES transactions (book_id, id) ON DELETE CASCADE
);

-- Index 1: BookBalance and ListTransactionsForAccount both start here.
CREATE INDEX entries_account_idx ON entries (book_id, account_id);

-- ---------------------------------------------------------------------------
-- The deposit layer
-- ---------------------------------------------------------------------------

CREATE TABLE deposit_accounts (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id              TEXT NOT NULL,
    gl_account      TEXT NOT NULL,
    name            TEXT NOT NULL,
    status          SMALLINT NOT NULL,
    overdraft_limit BIGINT NOT NULL,
    created_at      TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

CREATE TABLE holds (
    book_id     TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id          TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    amount      BIGINT NOT NULL,
    expires_at  TIMESTAMPTZ,
    description TEXT NOT NULL,
    status      SMALLINT NOT NULL,
    created_at  TIMESTAMPTZ,
    seq         BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

-- Index 2: ActiveHoldTotal and ListHoldsForAccount.
CREATE INDEX holds_account_idx ON holds (book_id, account_id);

-- A snapshot is identified by (account, business date) at day granularity, so
-- the date key is part of the primary key and a second snapshot for the same
-- day replaces the first. date_key is the value of deposit.SnapshotDateKey.
CREATE TABLE snapshots (
    book_id           TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    account_id        TEXT NOT NULL,
    date_key          TEXT NOT NULL,
    business_date     TIMESTAMPTZ,
    book_balance      BIGINT NOT NULL,
    holds_balance     BIGINT NOT NULL,
    available_balance BIGINT NOT NULL,
    taken_at          TIMESTAMPTZ,
    seq               BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, account_id, date_key)
);

-- ---------------------------------------------------------------------------
-- The payment layer
-- ---------------------------------------------------------------------------
--
-- These entities are network-scoped: a payment belongs to no single bank, so
-- unlike everything above they are keyed by their id alone. They are sequenced
-- under ledger.NetworkBook, which is why they carry no book_id column.

CREATE TABLE participants (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL,
    book_id            TEXT NOT NULL,
    customer_subledger TEXT NOT NULL,
    suspense_account   TEXT NOT NULL,
    reserve_account    TEXT NOT NULL,
    settlement_account TEXT NOT NULL,
    created_at         TIMESTAMPTZ,
    seq                BIGSERIAL NOT NULL
);

CREATE TABLE mandates (
    id                   TEXT PRIMARY KEY,
    debtor_participant   TEXT NOT NULL,
    debtor_account       TEXT NOT NULL,
    debtor_iban          TEXT NOT NULL,
    creditor_participant TEXT NOT NULL,
    creditor_account     TEXT NOT NULL,
    creditor_iban        TEXT NOT NULL,
    max_amount           BIGINT NOT NULL,
    status               SMALLINT NOT NULL,
    created_at           TIMESTAMPTZ,
    seq                  BIGSERIAL NOT NULL
);

CREATE TABLE payments (
    id                   TEXT PRIMARY KEY,
    scheme               TEXT NOT NULL,
    debtor_participant   TEXT NOT NULL,
    debtor_account       TEXT NOT NULL,
    debtor_iban          TEXT NOT NULL,
    creditor_participant TEXT NOT NULL,
    creditor_account     TEXT NOT NULL,
    creditor_iban        TEXT NOT NULL,
    amount               BIGINT NOT NULL,
    mandate_id           TEXT NOT NULL,
    end_to_end_id        TEXT NOT NULL,
    status               SMALLINT NOT NULL,
    reject_reason        TEXT NOT NULL,
    cycle_id             TEXT NOT NULL,
    booking_date         TIMESTAMPTZ,
    value_date           TIMESTAMPTZ,
    description          TEXT NOT NULL,
    metadata             JSONB,
    created_at           TIMESTAMPTZ,
    debtor_leg_tx        TEXT NOT NULL,
    creditor_leg_tx      TEXT NOT NULL,
    seq                  BIGSERIAL NOT NULL
);

-- Index 3: GetPaymentByEndToEndID. Deliberately NOT unique. store/mem does not
-- reject a duplicate client reference — payment.Network does, in
-- InitiatePaymentTx — and a store that refused one where mem accepted it would
-- be the two implementations disagreeing.
CREATE INDEX payments_end_to_end_idx ON payments (end_to_end_id)
    WHERE end_to_end_id <> '';

CREATE TABLE cycles (
    id            TEXT PRIMARY KEY,
    scheme        TEXT NOT NULL,
    status        SMALLINT NOT NULL,
    -- A JSONB column rather than a child table, so that a nil map and an empty
    -- one still round-trip differently: an open cycle carries an empty
    -- NetPositions and a closed one carries the computed positions.
    net_positions JSONB,
    opened_at     TIMESTAMPTZ,
    closed_at     TIMESTAMPTZ,
    settlement_id TEXT NOT NULL,
    seq           BIGSERIAL NOT NULL
);

-- Index 4: GetOpenCycle. Partial on the open status, which is the only one it
-- ever asks for. status 0 is payment.CycleOpen.
CREATE INDEX cycles_open_idx ON cycles (scheme) WHERE status = 0;

-- ClearingCycle.PaymentIDs is an ordered slice, so like entries it gets an
-- explicit position column.
CREATE TABLE cycle_payments (
    cycle_id   TEXT NOT NULL REFERENCES cycles (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    payment_id TEXT NOT NULL,
    PRIMARY KEY (cycle_id, position)
);

CREATE TABLE settlements (
    id            TEXT PRIMARY KEY,
    cycle_id      TEXT NOT NULL,
    settlement_tx TEXT NOT NULL,
    value_date    TIMESTAMPTZ,
    settled_at    TIMESTAMPTZ,
    seq           BIGSERIAL NOT NULL
);

CREATE TABLE settlement_positions (
    settlement_id  TEXT NOT NULL REFERENCES settlements (id) ON DELETE CASCADE,
    participant_id TEXT NOT NULL,
    amount         BIGINT NOT NULL,
    PRIMARY KEY (settlement_id, participant_id)
);

-- ---------------------------------------------------------------------------
-- The audit log
-- ---------------------------------------------------------------------------

-- seq is a BIGSERIAL and a total order over the WHOLE store, not per book and
-- not per scope, which is what makes AuditFilter.Before a global cursor that
-- every other predicate is applied alongside.
--
-- It has no foreign key to books: an audit event must be appendable whatever
-- else is or is not in the database, and a log that can be blocked by a
-- constraint is not a log.
CREATE TABLE audit_events (
    seq         BIGSERIAL PRIMARY KEY,
    id          TEXT NOT NULL,
    book_id     TEXT NOT NULL,
    scope       TEXT NOT NULL,
    type        TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    payload     JSONB,
    metadata    JSONB,
    actor       TEXT NOT NULL,
    occurred_at TIMESTAMPTZ
);

-- Indexes 5 and 6: the two shapes every audit query has. Book+scope is what a
-- Book's own log and the participant endpoints ask for; entity is what a
-- "history of this account" view asks for. Both carry seq so the cursor and the
-- ordering come out of the index.
CREATE INDEX audit_events_book_scope_idx ON audit_events (book_id, scope, seq);
CREATE INDEX audit_events_entity_idx ON audit_events (entity_id, seq);

-- ---------------------------------------------------------------------------
-- Identity allocation
-- ---------------------------------------------------------------------------

-- id_sequences holds every counter the domain draws from, as ordinary rows, so
-- that allocation rolls back with the caller's transaction and the counters
-- stay gap-free. A Postgres SEQUENCE would be wrong here: sequences are outside
-- transactions on purpose, so a failed posting would burn a transaction number.
--
-- `name` is the counter, not the ID prefix: NextID keeps ONE counter per book
-- shared by every prefix, so ldg_1, evt_2 and tx_3 interleave and the number
-- doubles as a creation order.
CREATE TABLE id_sequences (
    book_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    next_value BIGINT NOT NULL,
    PRIMARY KEY (book_id, name)
);
