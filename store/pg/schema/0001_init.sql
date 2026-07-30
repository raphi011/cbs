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

-- Also not here: a CHECK on the text columns.
--
-- Postgres does refuse some text on its own — a NUL is SQLSTATE 22021 in a TEXT
-- column and 22P05 inside JSONB, and so is any byte sequence that is not valid
-- UTF-8 — and store/mem, being a map of Go strings, refuses none of it. That is
-- the same asymmetry as a UNIQUE constraint, pointing the other way, and it is
-- closed the same way: in the domain, by ledger.ValidateText, which every layer
-- calls before a string reaches a store. Restating the rule as a CHECK here
-- would only make store/pg refuse a *different* set of strings from store/mem
-- than it already does. What the system will accept is a domain question; these
-- columns just hold the answer.
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

-- Every account is denominated in exactly one asset, fixed at creation.
--
-- The column is on accounts and NOT on entries. An entry's asset is always its
-- account's, so storing it twice would only create the possibility of the two
-- disagreeing. PostTransaction derives it when it checks that debits equal
-- credits within each asset.
--
-- There is no table of assets for it to reference, and no CHECK enumerating the
-- codes either. An asset definition is a fact about the world rather than
-- per-bank state — "BTC has 8 decimal places" is true in every book — so the
-- known assets are a list in Go (ledger.LookupAsset), the same way payment
-- schemes are Go types rather than rows. See the COMMENT at the foot of this
-- file for why the rule is not restated here as a constraint.
CREATE TABLE accounts (
    book_id      TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id           TEXT NOT NULL,
    subledger_id TEXT NOT NULL,
    name         TEXT NOT NULL,
    type         SMALLINT NOT NULL,
    asset        TEXT NOT NULL,
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
--
-- entries.value_date is the one date that is NOT a copy. An entry's asset is
-- always its account's, which is why accounts.asset exists and entries.asset
-- does not — a second copy could only disagree. An entry's value date is a
-- different case: the two legs of one event can legitimately take economic
-- effect on different days. An outbound transfer debits the payer's account on
-- the day it is debited, while the credit into the bank's clearing suspense
-- carries the interbank settlement date. Storing only transactions.value_date
-- would force one of those two to be wrong, and interest is computed from this
-- column.
CREATE TABLE entries (
    book_id        TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    position       INTEGER NOT NULL,
    id             TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    amount         BIGINT NOT NULL,
    direction      SMALLINT NOT NULL,
    value_date     TIMESTAMPTZ,
    PRIMARY KEY (book_id, transaction_id, position),
    FOREIGN KEY (book_id, transaction_id) REFERENCES transactions (book_id, id) ON DELETE CASCADE
);

-- The value_date suffix serves the value-dated balance and the per-day movement
-- series. The (book_id, account_id) prefix is unchanged, so BookBalance keeps
-- the index it always had.
CREATE INDEX entries_account_idx ON entries (book_id, account_id, value_date);

-- ---------------------------------------------------------------------------
-- The deposit layer
-- ---------------------------------------------------------------------------

-- deposit_accounts.asset is duplicated from the backing GL account.
--
-- This is the one place the schema stores a fact twice on purpose. The GL
-- account's asset is fixed at creation, so the two cannot drift, and deriving
-- it would turn every listing of deposit accounts into a join for a value that
-- can never change. store/storetest asserts the two always agree.
CREATE TABLE deposit_accounts (
    book_id           TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id                TEXT NOT NULL,
    gl_account        TEXT NOT NULL,
    name              TEXT NOT NULL,
    status            SMALLINT NOT NULL,
    asset             TEXT NOT NULL,
    overdraft_limit   BIGINT NOT NULL,
    overdraft_rate    BIGINT NOT NULL DEFAULT 0,
    unarranged_rate   BIGINT NOT NULL DEFAULT 0,
    day_count         SMALLINT NOT NULL DEFAULT 0,
    accrued_interest  BIGINT NOT NULL DEFAULT 0,
    accrued_gross     BIGINT NOT NULL DEFAULT 0,
    terms_effective_from TIMESTAMPTZ,
    last_accrual_date TIMESTAMPTZ,
    interest_gl       TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ,
    seq               BIGSERIAL NOT NULL,
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

-- An account's arranged overdraft terms from one day onwards: one row per
-- repricing, never overwritten.
--
-- These four values used to be mutable columns on deposit_accounts, and that is
-- the one place this schema broke its own rule. Every financial calculation
-- here is a function of account state, event history and configuration; the
-- first two are immutable and replayable, and a configuration that could be
-- edited in place undermined that entirely, because "what did this account's
-- product say on 15 July 2027?" had no stable answer. It also bounded the
-- interest recompute window at the last repricing, so a backdated posting
-- landing before it was silently never trued up.
--
-- These are PER-INSTANCE terms: one timeline per account, not a catalogue
-- shared across them. There is no product/product_version table, no content
-- hash, no pinned-versus-floating parameter binding and no overlays. Those are
-- the full machinery a real product engine needs and are far beyond this
-- schema's scope; the effective-dated record is not.
CREATE TABLE overdraft_terms (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    account_id      TEXT NOT NULL,
    day_key         TEXT NOT NULL,
    effective_from  TIMESTAMPTZ,
    overdraft_limit BIGINT NOT NULL,
    rate            BIGINT NOT NULL,
    unarranged_rate BIGINT NOT NULL,
    day_count       SMALLINT NOT NULL,
    created_at      TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, account_id, day_key)
);

-- Index 3: the timeline read that accrual makes once per account per run, and
-- the bounded as-of lookup balanceTx makes on every withdrawal check. Both
-- filter on (book_id, account_id) and order by day_key, so one index serves
-- both — the as-of lookup is an ORDER BY day_key DESC LIMIT 1 over the same
-- prefix. It is not redundant with the primary key: the PK's leading columns
-- are the same, so Postgres can in fact serve both from it, and this index is
-- declared anyway so that a future change to the primary key (an id column,
-- say) does not silently turn every accrual into a sequential scan.
CREATE INDEX overdraft_terms_account_idx ON overdraft_terms (book_id, account_id, day_key);

-- ---------------------------------------------------------------------------
-- The lending layer
-- ---------------------------------------------------------------------------

-- A credit facility: a term loan or a revolving credit line. The mirror of a
-- deposit account — it wraps two Asset GL accounts and stores no money itself.
--
-- There is no row here for an arranged overdraft, and that is the design rather
-- than an omission. An overdrawn current account's drawn amount IS the negative
-- balance of its own Liability account viewed by sign; it has no independent
-- existence, so a facility row for it would store a number that already exists.
-- Its terms live on deposit_accounts, and its Asset-side classification is an
-- aggregation (deposit.Totals). See docs/deposit-accounts-vs-subledger.md.
CREATE TABLE facilities (
    book_id           TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id                TEXT NOT NULL,
    kind              SMALLINT NOT NULL,
    name              TEXT NOT NULL,
    asset             TEXT NOT NULL,
    principal_gl      TEXT NOT NULL,
    interest_gl       TEXT NOT NULL,
    refund_gl         TEXT NOT NULL DEFAULT '',
    commitment        BIGINT NOT NULL,
    rate              BIGINT NOT NULL,
    day_count         SMALLINT NOT NULL,
    method            SMALLINT NOT NULL,
    term_months       INTEGER NOT NULL,
    min_payment       BIGINT NOT NULL,
    accrued_interest  BIGINT NOT NULL,
    accrued_gross     BIGINT NOT NULL DEFAULT 0,
    terms_effective_from TIMESTAMPTZ,
    last_accrual_date TIMESTAMPTZ,
    days_past_due     INTEGER NOT NULL,
    arrears_bucket    SMALLINT NOT NULL,
    non_performing    BOOLEAN NOT NULL,
    oldest_unpaid_due TIMESTAMPTZ,
    status            SMALLINT NOT NULL,
    opened_at         TIMESTAMPTZ,
    maturity_at       TIMESTAMPTZ,
    seq               BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

-- One scheduled payment. A term loan's rows are generated in full at
-- disbursement; a revolving line appends one per billing cycle, being that
-- cycle's minimum payment, which is how a revolving facility actually falls
-- into arrears.
--
-- principal and interest are the PLAN. What a repayment allocates to interest
-- is the accrual, which under ACT/365 differs from a scheduled twelfth — see
-- lending.Portfolio.Repay.
CREATE TABLE installments (
    book_id        TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    facility_id    TEXT NOT NULL,
    seq_no         INTEGER NOT NULL,
    due_date       TIMESTAMPTZ,
    principal      BIGINT NOT NULL,
    interest       BIGINT NOT NULL,
    paid_principal BIGINT NOT NULL,
    paid_interest  BIGINT NOT NULL,
    seq            BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, facility_id, seq_no)
);

-- A facility's credit terms from one day onwards: one row per repricing, never
-- overwritten. The mirror of overdraft_terms — see there for why terms are rows
-- at all — with two differences worth stating. There is no limit column,
-- because facilities.commitment is not effective-dated: drawing is refused
-- against the limit in force at the moment of the draw, and no past day's
-- arithmetic depends on what it used to be. And there is no method, term_months
-- or min_payment column, because those feed BuildSchedule rather than the
-- accrual: a term loan's instalments are generated once from the rate in force
-- at disbursement, so repricing one that already has a schedule is REFUSED
-- (lending.ErrScheduleWouldDiverge) rather than allowed to drift.
CREATE TABLE facility_terms (
    book_id        TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    facility_id    TEXT NOT NULL,
    day_key        TEXT NOT NULL,
    effective_from TIMESTAMPTZ,
    rate           BIGINT NOT NULL,
    day_count      SMALLINT NOT NULL,
    created_at     TIMESTAMPTZ,
    seq            BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, facility_id, day_key)
);

-- Index 4: the timeline read that accrual makes once per facility per run, and
-- the bounded as-of lookup a draw check makes. Both filter on (book_id,
-- facility_id) and order by day_key, so one index serves both — the same
-- reasoning as overdraft_terms_account_idx, applied to this table's own
-- primary key.
CREATE INDEX facility_terms_facility_idx ON facility_terms (book_id, facility_id, day_key);

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
    created_at         TIMESTAMPTZ,
    seq                BIGSERIAL NOT NULL
);

-- A participant's internal plumbing accounts, one set per asset it operates in.
--
-- These are a child table rather than three columns on participants because
-- each of those accounts is denominated in exactly one asset: a bank clearing
-- both a euro and a dollar scheme needs two suspense accounts and two reserve
-- accounts, not two currencies inside one. Keying by (participant, asset) makes
-- adding a scheme in a new asset a data change rather than a schema change.
--
-- The set is fixed when the bank joins the network, which is the reason the
-- asset registry that used to sit beside this table is gone: an asset a bank
-- did not join with has no suspense, reserve or settlement account here, so
-- registering one afterwards produced customer accounts that could never
-- settle. What a bank operates in is these rows; what an asset *is* is code.
CREATE TABLE participant_assets (
    participant_id TEXT NOT NULL REFERENCES participants (id) ON DELETE CASCADE,
    asset          TEXT NOT NULL,
    suspense       TEXT NOT NULL,
    reserve        TEXT NOT NULL,
    settlement     TEXT NOT NULL,
    seq            BIGSERIAL NOT NULL,
    PRIMARY KEY (participant_id, asset)
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

-- Index 5: GetPaymentByEndToEndID. Deliberately NOT unique. store/mem does not
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

-- Index 6: GetOpenCycle. Partial on the open status, which is the only one it
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

-- Indexes 6 and 7: the two shapes every audit query has. Book+scope is what a
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

-- ---------------------------------------------------------------------------
-- Why the asset columns carry no constraint
-- ---------------------------------------------------------------------------
--
-- Recorded in the database rather than only in this file, because the absence
-- of a constraint is invisible in a schema dump: the next author reads four
-- TEXT columns holding "EUR" and "BTC" and adds the "missing" CHECK.

COMMENT ON COLUMN accounts.asset IS
    'The asset this account is denominated in, fixed at creation. There is '
    'deliberately NO constraint restricting it to the known codes. A store is '
    'a per-table key/value layer; "the asset must be one the system knows" is '
    'a DOMAIN rule, enforced by ledger.Book.CreateAccountTx against the list '
    'in ledger.LookupAsset — exactly where "the parent must exist" lives for '
    'ledgers, subledgers and accounts too. Postgres could express the rule and '
    'store/mem could not, so neither does: a constraint here would make '
    'store/pg refuse a write store/mem performs, and the two stores accepting '
    'and refusing the same writes is the property store/storetest exists to '
    'hold. The subtest is ParentReferencesAreNotEnforced in '
    'store/storetest/storetest.go, whose fixtures write accounts with no asset '
    'set at all. An earlier composite FK on subledgers (book_id, ledger_id) '
    'broke that same subtest and was removed for the same reason. A CHECK '
    'would also make adding an asset — a one-line change to a Go slice — a '
    'migration.';

COMMENT ON COLUMN deposit_accounts.asset IS
    'The asset this deposit account is denominated in, duplicated from its '
    'backing GL account — the one fact this schema stores twice on purpose, '
    'because the GL account''s asset is immutable and deriving it would turn '
    'every listing into a join. Unconstrained, for the reason given on '
    'accounts.asset.';

COMMENT ON COLUMN participant_assets.asset IS
    'One row per asset this bank operates in, holding the three plumbing '
    'accounts that asset needs. Unconstrained, for the reason given on '
    'accounts.asset.';

COMMENT ON COLUMN facilities.asset IS
    'The asset this facility is denominated in, duplicated from the GL '
    'accounts named by principal_gl, interest_gl and refund_gl — every one of '
    'which is created in it and cannot change asset afterwards, so they '
    'cannot drift. Duplicated for the same reason deposit_accounts.asset is: '
    'deriving it would turn every listing of facilities into a join for a '
    'value that can never change, and store/storetest asserts the copies '
    'always agree (FacilityAssetMatchesItsGLAccounts). Unconstrained, for the '
    'reason given on accounts.asset.';

COMMENT ON COLUMN facilities.refund_gl IS
    'The Liability account holding interest this bank charged on THIS '
    'facility and never earned, and so owes the borrower back. Unlike '
    'principal_gl and interest_gl it is empty on almost every row: the '
    'account is created lazily, only when a backdated posting cuts accrued '
    'interest below what the borrower has already settled in cash and '
    'neither the receivable nor the drawn principal can absorb the whole '
    'correction. Empty therefore means no correction has ever overshot, and '
    'is read as a zero obligation rather than as a missing account — which '
    'is why the read path checks this column before touching the ledger. '
    'It is per facility rather than one pooled account per asset (which is '
    'what interest income is) because the balance answers "what does the '
    'bank owe THIS borrower": pooled, one balance cannot say who is owed '
    'what, and a refund against it could pay one borrower out of another''s '
    'money and still balance, since a Liability is never caught by the '
    'sufficiency check. The Payables subledger''s total is the control '
    'figure over these subsidiary rows. Stored as an ID rather than resolved '
    'by account name because name is a mutable column on this row and a '
    'rename would otherwise orphan the obligation.';

COMMENT ON COLUMN deposit_accounts.accrued_interest IS
    'Interest earned and not yet charged, in MICRO-MINOR-UNITS: the asset''s '
    'minor unit multiplied by 1e6 (interest.AccruedScale). It is not a money '
    'column and must never be compared with, or summed alongside, one. The '
    'scale exists because a day''s interest on a small balance is mostly '
    'fraction — 50 EUR overdrawn at 15% accrues 2.054794 cents a day, and '
    'rounding that to 2 daily discards 0.054794 cents a day: 20.0 cents a '
    'year against 750 cents of annual interest, a 2.67% error. The '
    'general ledger holds the rounded figure in the account named by '
    'interest_gl; this column holds the residue an integer of minor units '
    'cannot represent, which is the same reason holds live outside the ledger. '
    'Recorded here because a scale carried in an integer column is invisible '
    'in a schema dump.';

COMMENT ON COLUMN deposit_accounts.accrued_gross IS
    'What the CURRENT terms window has accrued in total, same scale as '
    'accrued_interest. Overdraft interest is recomputed rather than '
    'incremented: every end-of-day re-derives the whole window from the '
    'account''s value-dated balance, and accrued_interest moves by the change '
    'in this column. That is what makes a backdated posting correct itself — '
    'the day it takes effect on is re-derived with it in place, this figure '
    'moves, and the next run posts the difference. Unlike accrued_interest it '
    'is never decremented by capitalization, and it resets to zero whenever '
    'terms are set, because that is where the window starts. A store that '
    'dropped this column would re-derive the whole window as a fresh delta '
    'every night and charge the same interest over and over.';

COMMENT ON COLUMN deposit_accounts.terms_effective_from IS
    'The start of the recompute window: when the current terms took effect. '
    'The window is bounded here rather than at account opening because the '
    'terms are mutable columns on this very row — recomputing across a '
    'repricing would re-derive every earlier day at today''s rate and post the '
    'difference, rewriting accrual history on every repricing. Prior accrual '
    'is frozen instead; widening this window to account inception is what an '
    'effective-dated terms table would buy. NULL means the account has no '
    'priced overdraft and accrues nothing.';

COMMENT ON COLUMN deposit_accounts.overdraft_rate IS
    'Annual interest rate on the arranged overdraft, in MILLIONTHS: 1000000 is '
    '100%, 150000 is 15% (interest.RateScale). Basis points would be too '
    'coarse — retail rates are quoted in eighths of a percent. Zero means the '
    'account accrues no interest, the same convention overdraft_limit already '
    'uses for the facility itself. unarranged_rate is the same scale and '
    'applies to any balance drawn beyond overdraft_limit.';

COMMENT ON COLUMN deposit_accounts.interest_gl IS
    'This account''s own accrued-interest-receivable GL account, an Asset. '
    'Empty until a non-zero rate is first set. It is per deposit account, not '
    'one shared receivable per bank, because a shared one would be a stored '
    'total whose detail lives in accrued_interest — a control account, and the '
    'duplication this schema exists without. There is deliberately NO foreign '
    'key to accounts, for the reason given on accounts.asset.';

COMMENT ON COLUMN overdraft_terms.day_key IS
    'The UTC calendar day these terms first apply, as YYYY-MM-DD, and part of '
    'the primary key: a terms row is identified by (account, DAY), so a second '
    'row entered for the same effective day replaces the first and "the terms '
    'in force on day D" is unique by construction rather than by a validation '
    'rule. Go does the truncating (deposit.TermsDayKey over ledger.DayStart) '
    'and this column is what both the listing and the as-of lookup order and '
    'compare on — an ISO day is lexicographically ordered, so a text compare '
    'is a day compare. The key is a day rather than a timestamp because '
    'accrual iterates whole UTC days: terms changing part-way through a day '
    'would have no well-defined meaning, since the day is the unit the '
    'arithmetic is expressed in. Neither store truncates for itself, which is '
    'one DST-adjacent edge case away from store/pg and store/mem disagreeing '
    'about which day a repricing landed in. Compare snapshots.date_key, which '
    'is the same pattern for the same reason.';

COMMENT ON COLUMN overdraft_terms.effective_from IS
    'The same day as day_key, as a timestamp, and the value Go reads back. It '
    'is stored beside the key rather than derived from it so that a reader of '
    'this table sees a date rather than a string, and so that ORDER BY '
    'effective_from and ORDER BY day_key can never disagree.';

COMMENT ON COLUMN overdraft_terms.created_at IS
    'When this repricing was ENTERED, as against effective_from, which is when '
    'it takes economic effect. The pair is the booking-date/value-date '
    'distinction applied to configuration, for exactly the reasons the README '
    'gives for money: a repricing agreed on the 1st and entered on the 15th is '
    'the ordinary case, and refusing it would leave the agreed date with no '
    'representation. Both directions are allowed — a row effective in the past '
    'is picked up by the next recompute the same way a backdated posting is, '
    'and one effective next month is inert until the runs reach it, which is '
    'scheduled repricing for free.';

COMMENT ON COLUMN overdraft_terms.rate IS
    'Annual interest rate on the arranged overdraft, in MILLIONTHS: 1000000 is '
    '100%, 150000 is 15% (interest.RateScale). Zero makes the WHOLE overdraft '
    'interest-free, which is a real product. unarranged_rate is the same scale '
    'and applies to any balance drawn beyond overdraft_limit; it is an optional '
    'SURCHARGE, so zero there means rate applies throughout rather than that '
    'the excess is free. There is deliberately NO CHECK on either column, and '
    'none on day_count: a CHECK enumerating the valid day-count conventions '
    'would make store/pg refuse a write store/mem performs — which '
    'store/storetest exists to prevent — and would turn a one-line change to a '
    'Go constant into a migration. This is the same reasoning recorded on the '
    'four asset columns, applied to a new case, and it is recorded in the '
    'database because a missing constraint is invisible in a schema dump.';

COMMENT ON COLUMN facilities.accrued_interest IS
    'Interest earned and not yet settled, in MICRO-MINOR-UNITS: the asset''s '
    'minor unit multiplied by 1e6 (interest.AccruedScale). Not a money column; '
    'never sum it alongside one. It is SIGNED and routinely negative: a '
    'capitalization charges the rounded receivable, which can exceed what was '
    'earned, and the residue is absorbed by the next day''s accrual. The '
    'general ledger holds the rounded figure in interest_gl. Recorded here '
    'because a scale carried in an integer column is invisible in a schema '
    'dump, and because a reader who saw the negative values would otherwise '
    'read them as corruption.';

COMMENT ON COLUMN facilities.accrued_gross IS
    'What the CURRENT terms window has accrued in total, same scale as '
    'accrued_interest. Facility interest is recomputed rather than '
    'incremented: every end-of-day re-derives the whole window from the '
    'VALUE-DATED balance of principal_gl, and accrued_interest moves by the '
    'change in this column. That is what makes a backdated repayment or '
    'advance correct itself — the days it takes effect over are re-derived '
    'with it in place, this figure moves, and the next run posts the '
    'difference. Unlike accrued_interest it is never decremented by a '
    'repayment or a capitalization, which settle the receivable rather than '
    'the window. A store that dropped this column would re-derive the whole '
    'window as a fresh delta every night and charge the same interest over '
    'and over.';

COMMENT ON COLUMN facilities.terms_effective_from IS
    'The start of the recompute window: where the current terms took effect, '
    'which for a facility is its FIRST ADVANCE — money not yet paid out earns '
    'nothing, so there is nothing earlier to re-derive. It is bounded there '
    'rather than at origination for the same reason '
    'deposit_accounts.terms_effective_from is bounded at the last repricing: '
    'rate and day_count are mutable columns on this very row, and a window '
    'reaching back past a change to them would re-derive past days at a rate '
    'that was not in force on them. NULL means nothing has been advanced and '
    'the facility accrues nothing.';

COMMENT ON COLUMN facilities.rate IS
    'Annual interest rate in MILLIONTHS: 1000000 is 100%, 60000 is 6% '
    '(interest.RateScale). min_payment is the same scale but is NOT a rate — '
    'it is a dimensionless share of drawn principal added to a revolving '
    'line''s minimum payment each cycle (interest.Fraction), and the Go types '
    'are distinct so the compiler refuses to swap them.';

COMMENT ON COLUMN facilities.commitment IS
    'What the bank has committed: a term loan''s original principal, a '
    'revolving line''s limit. One column rather than two because it plays the '
    'same role in both — the amount beyond which drawing is refused. The '
    'amount actually DRAWN is not stored: it is the book balance of '
    'principal_gl, derived from the entries like every other balance here.';

COMMENT ON COLUMN facilities.days_past_due IS
    'The calendar-day age of the oldest instalment still due and unpaid. This '
    'column, arrears_bucket, non_performing and oldest_unpaid_due are a CACHE: '
    'all four are a pure function of this facility''s installments rows and a '
    'date (lending.ArrearsFor), recomputed at end of day and after every '
    'repayment, never accumulated from a stream of missed-payment events. '
    'Stored anyway because they are what the API and the web layer read, and '
    'recomputing four columns on every listing would make a delinquency report '
    'a join over every schedule in the book. A stale value is therefore a '
    'stale cache and not a lost fact: re-running the recompute repairs it. '
    'Days are ALWAYS actual calendar days, whatever day_count this facility '
    'accrues interest under — a 30/360 loan is not 33/360 days late.';

COMMENT ON COLUMN facilities.arrears_bucket IS
    'The band days_past_due falls in: Current, 1-29, 30-59, 60-89, 90+. Part '
    'of the arrears cache described on days_past_due.';

COMMENT ON COLUMN facilities.oldest_unpaid_due IS
    'The due date days_past_due is measured from, NULL when the facility is '
    'current. Part of the arrears cache described on days_past_due.';

COMMENT ON COLUMN facilities.non_performing IS
    'Set at 90+ days past due. It MARKS ONLY and changes no accounting. '
    'Non-accrual — where a non-performing loan stops recognizing interest into '
    'income — and expected-credit-loss provisioning are recorded as future '
    'work in docs/expansion-roadmap.md. Part of the arrears cache described on '
    'days_past_due.';

COMMENT ON COLUMN facility_terms.day_key IS
    'The UTC calendar day these terms first apply, as YYYY-MM-DD, and part of '
    'the primary key: a terms row is identified by (facility, DAY), so a second '
    'row entered for the same effective day replaces the first and "the terms '
    'in force on day D" is unique by construction rather than by a validation '
    'rule. Go does the truncating (lending.TermsDayKey over ledger.DayStart) '
    'and this column is what both the listing and the as-of lookup order and '
    'compare on — an ISO day is lexicographically ordered, so a text compare '
    'is a day compare. The key is a day rather than a timestamp because '
    'accrual iterates whole UTC days: terms changing part-way through a day '
    'would have no well-defined meaning, since the day is the unit the '
    'arithmetic is expressed in. Neither store truncates for itself, which is '
    'one DST-adjacent edge case away from store/pg and store/mem disagreeing '
    'about which day a repricing landed in. Compare overdraft_terms.day_key, '
    'which is the same pattern for the same reason.';

COMMENT ON COLUMN facility_terms.effective_from IS
    'The same day as day_key, as a timestamp, and the value Go reads back. It '
    'is stored beside the key rather than derived from it so that a reader of '
    'this table sees a date rather than a string, and so that ORDER BY '
    'effective_from and ORDER BY day_key can never disagree.';

COMMENT ON COLUMN facility_terms.created_at IS
    'When this repricing was ENTERED, as against effective_from, which is when '
    'it takes economic effect. The pair is the booking-date/value-date '
    'distinction applied to configuration, for exactly the reasons the README '
    'gives for money: a repricing agreed on the 1st and entered on the 15th is '
    'the ordinary case, and refusing it would leave the agreed date with no '
    'representation. Both directions are allowed — a row effective in the past '
    'is picked up by the next recompute the same way a backdated posting is, '
    'and one effective next month is inert until the runs reach it, which is '
    'scheduled repricing for free.';

COMMENT ON COLUMN facility_terms.rate IS
    'Annual interest rate in MILLIONTHS: 1000000 is 100%, 60000 is 6% '
    '(interest.RateScale). Zero makes the WHOLE facility interest-free, which '
    'is a real product. There is deliberately NO CHECK on this column, and '
    'none on day_count: a CHECK enumerating the valid day-count conventions '
    'would make store/pg refuse a write store/mem performs — which '
    'store/storetest exists to prevent — and would turn a one-line change to a '
    'Go constant into a migration. This is the same reasoning recorded on '
    'overdraft_terms.rate and on the four asset columns, applied to a new '
    'case, and it is recorded in the database because a missing constraint is '
    'invisible in a schema dump.';

COMMENT ON COLUMN installments.seq_no IS
    'The instalment''s position in the contract, 1-based, and part of its '
    'primary key. Distinct from the `seq` column, which is the row''s '
    'monotonic insertion sequence used to break ordering ties everywhere else '
    'in this schema. ListInstallments orders by seq_no, not by seq or by '
    'due_date: seq_no is already a total order within a facility and a due '
    'date is not.';
