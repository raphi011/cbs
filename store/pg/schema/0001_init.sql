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
    accrued_interest  BIGINT NOT NULL DEFAULT 0,
    accrued_gross     BIGINT NOT NULL DEFAULT 0,
    last_accrual_date TIMESTAMPTZ,
    interest_gl       TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ,
    seq               BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

-- An account's external addresses: what a counterparty quotes to pay it. The
-- account's own id is never one of these.
--
-- Two things this table deliberately does NOT say.
--
-- First, there is no UNIQUE (book_id, scheme, value). "One bank issues an
-- address once" is a real domain rule and deposit.Register enforces it by
-- reading before it writes — but nothing serializes two concurrent adds the way
-- NextID's row lock serializes AddParticipantTx, so under READ COMMITTED a
-- constraint here would fire in Postgres and not in store/mem. That is the one
-- divergence this package must never introduce; see the note on UNIQUE
-- (book_id, name) above, which is the same argument. The primary key is
-- therefore widened with deposit_account_id so that it is a row identity rather
-- than the domain rule in disguise, and the lookup index is a plain index. The
-- residual duplicate is caught at READ time, ON THE ROUTING PATH:
-- Register.ResolveIdentifier and the network's sweep answer
-- ErrIdentifierAmbiguous rather than picking one, so no address ever routes
-- money to a bank or an account chosen by listing order. It is a claim about
-- resolution and nothing else — SubmitPaymentTx is handed an account id and
-- never resolves, so two accounts colliding on one address both stay payable by
-- id. That is correct: the accounts are distinct and real, and what is
-- ambiguous is the address.
-- storetest/IdentifierUniquenessIsNotEnforced pins all of this.
--
-- Second, there is no CHECK on scheme or value. The known schemes are Go
-- constants (deposit.IdentifierIBAN), the way assets and payment schemes are,
-- and the FORMAT of a value is deliberately unvalidated — no mod-97 check digit
-- — so that the seed's readable SE89-AURORA-1001 stays legal.
--
-- The parent FOREIGN KEY, unlike subledgers.ledger_id, DOES stay. It is the
-- exemption stated above for entries -> transactions: PutDepositAccount writes
-- both sides itself, within one statement sequence, so no caller can produce an
-- orphan. Identifiers are modelled as part of the account aggregate precisely
-- so that this holds.
CREATE TABLE deposit_account_identifiers (
    book_id            TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    deposit_account_id TEXT NOT NULL,
    scheme             TEXT NOT NULL,
    value              TEXT NOT NULL,
    PRIMARY KEY (book_id, deposit_account_id, scheme, value),
    FOREIGN KEY (book_id, deposit_account_id)
        REFERENCES deposit_accounts (book_id, id) ON DELETE CASCADE
);

-- ListDepositAccountsByIdentifier. Not UNIQUE, on purpose; see above.
--
-- Its third column does not serve an IBAN lookup, and that is worth stating
-- here rather than leaving someone to discover it from a query plan. An IBAN is
-- stored in its readable display form (SE89-AURORA-1001) and arrives from a
-- payment message compact (SE89AURORA1001); they are one address, so the lookup
-- compares both sides with the separators removed, and a predicate on
-- replace(value, ...) cannot use an index on value. The (book_id, scheme)
-- PREFIX still can, so the scan is over one bank's identifiers in one scheme
-- rather than the table — the index is narrowed here, not dead.
--
-- An index on the same expression would restore the third column; replace() is
-- IMMUTABLE, so a functional index is legal. It is not created because no
-- database is deployed and a bank here has a handful of accounts, so there is
-- nothing to measure it against. Normalising the stored value instead would
-- take the readable IBAN out of every statement, worked example and screenshot
-- in the repository, which is the trade this schema already refused when it
-- declined to enforce mod-97 check digits.
CREATE INDEX deposit_account_identifiers_lookup_idx
    ON deposit_account_identifiers (book_id, scheme, value);

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
-- repricing, appended rather than editing what an earlier day already said. A
-- second row for the SAME effective day does replace the first — the day is
-- part of the primary key below, and day_key's comment says why.
--
-- These four values used to be mutable columns on deposit_accounts, and that is
-- the one place this schema broke its own rule. Every financial calculation
-- here is a function of account state, event history and configuration; the
-- first two are immutable and replayable, and a configuration that could be
-- edited in place undermined that entirely, because "what did this account's
-- product say on 15 July 2027?" had no stable answer. It also bounded the
-- interest recompute window at the last repricing: a repricing closed the old
-- window out and opened a new one at itself, so a backdated posting landing
-- behind it was trued up only from the repricing forward, and the days between
-- where it took effect and the repricing kept the interest computed without it,
-- permanently.
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
    product_id      TEXT NOT NULL,
    overdraft_limit BIGINT NOT NULL,
    rate            BIGINT,
    unarranged_rate BIGINT,
    day_count       SMALLINT,
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
-- The product catalogue
-- ---------------------------------------------------------------------------

-- A catalogue entry: the named product an account is opened FROM. Separate from
-- its versions because a product needs a name before it has a price, and
-- because listing the catalogue should not mean grouping a version table.
--
-- product_versions.product_id carries no foreign key, for the reason
-- subledgers.ledger_id does not: "the parent must exist" is a domain rule, and
-- product.Catalogue enforces it. A constraint here would make store/pg reject
-- writes store/mem accepts.
CREATE TABLE products (
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id         TEXT NOT NULL,
    name       TEXT NOT NULL,
    kind       SMALLINT NOT NULL,
    retired    BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ,
    seq        BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, id)
);

-- What a product cost from one day onwards: one row per repricing, never
-- changed once published.
--
-- The primary key is (book_id, product_id, day_key), which is where the book's
-- non-overlapping-interval exclusion constraint went. Keying by DAY makes "the
-- version in force on D" unique by construction, so there is no interval to
-- exclude — and unlike a tstzrange with a GiST exclusion constraint, store/mem
-- can implement the same rule with a map key. It is the discipline
-- overdraft_terms and snapshots already follow.
CREATE TABLE product_versions (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    product_id      TEXT NOT NULL,
    day_key         TEXT NOT NULL,
    effective_from  TIMESTAMPTZ,
    rate            BIGINT NOT NULL,
    unarranged_rate BIGINT NOT NULL,
    day_count       SMALLINT NOT NULL,
    hash            TEXT NOT NULL,
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, product_id, day_key)
);

COMMENT ON COLUMN product_versions.published_at IS
    'NULL means DRAFT: the row is editable and is invisible to resolution, so the published version before it stays in force through it. Non-NULL freezes the row — product.Catalogue refuses a write to it, and the refusal is in the domain layer rather than a CHECK because store/mem must refuse exactly the same writes.';

COMMENT ON COLUMN product_versions.hash IS
    'sha256 over the identity and the pricing (see product.Version.ComputeHash), computed at publication and VERIFIED on every read that prices a day. It is the only control on the one hole a domain-layer refusal cannot cover: a direct UPDATE to a published row. A mismatch fails the accrual rather than pricing a day from a row nobody published.';

-- Index 4: the timeline read the accrual makes once per product per run, and
-- the bounded as-of lookup an operator view makes. Both filter on
-- (book_id, product_id) and order by day_key, so one index serves both. Not
-- redundant with the primary key for the reason Index 3 is not.
CREATE INDEX product_versions_product_idx ON product_versions (book_id, product_id, day_key);

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
-- Its terms live in overdraft_terms, and its Asset-side classification is an
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
    method            SMALLINT NOT NULL,
    term_months       INTEGER NOT NULL,
    min_payment       BIGINT NOT NULL,
    accrued_interest  BIGINT NOT NULL,
    accrued_gross     BIGINT NOT NULL DEFAULT 0,
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

-- A facility's credit terms from one day onwards: one row per repricing,
-- appended rather than editing what an earlier day already said, and replaced
-- only by a second row for the same effective day. The mirror of
-- overdraft_terms — see there for why terms are rows at all — with two
-- differences worth stating. There is no limit column, because
-- facilities.commitment is not effective-dated: drawing is refused against the
-- limit in force at the moment of the draw, and no past day's arithmetic
-- depends on what it used to be. And there is no method, term_months or
-- min_payment column, because those feed BuildSchedule rather than the
-- accrual: a term loan's instalments are generated once from the rate in force
-- at disbursement, so repricing one that already has a schedule is REFUSED
-- (lending.ErrScheduleWouldDiverge) rather than allowed to drift — and so is a
-- row effective after the day it is entered, which is a row the schedule pinned
-- at disbursement could not see but the accrual would still reach. A revolving
-- line may be repriced either way, having no schedule to diverge from.
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

-- Index 5: the timeline read that accrual makes once per facility per run, and
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

-- product_id is the catalogue entry this bank opens customer accounts from —
-- the Basic product AddParticipant creates for every member. It is data, not a
-- handle like the Ledger and Deposit fields the store deliberately drops, so it
-- has to be stored: a participant read back without it prices its accounts from
-- a product id of "", which surfaces as "product not found" several layers away
-- from the store that lost it.
CREATE TABLE participants (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL,
    bic                TEXT NOT NULL,
    book_id            TEXT NOT NULL,
    customer_subledger TEXT NOT NULL,
    product_id         TEXT NOT NULL,
    created_at         TIMESTAMPTZ,
    seq                BIGSERIAL NOT NULL
);

COMMENT ON COLUMN participants.bic IS
    'ISO 9362 business identifier code. NOT NULL because a bank the mesh '
    'cannot address is not a member: routing is by BICFI, so a participant '
    'without one is unreachable rather than merely undescribed. No CHECK on '
    'its shape — the structural rule lives in iso20022.BIC, and a constraint '
    'here would fire in Postgres and not in store/mem, which store/storetest '
    'exists to forbid. There is also no UNIQUE: two banks sharing a BIC is a '
    'domain error, and nothing serializes two concurrent admissions, so the '
    'same reasoning that kept a UNIQUE off deposit identifiers applies here.';

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
    -- Where a credit goes when the payee's account will not take it. A
    -- liability, because the bank still owes the money — to whoever eventually
    -- claims it — exactly as it owes a deposit.
    unclaimed      TEXT NOT NULL,
    settlement     TEXT NOT NULL,
    seq            BIGSERIAL NOT NULL,
    PRIMARY KEY (participant_id, asset)
);

-- debtor/creditor _identifier_scheme and _identifier_value are the PartyRef's
-- payment.PartyRef.Identifier: the external address (an IBAN today) that was
-- quoted for that side, STORED rather than looked up from
-- deposit_account_identifiers at read time, because identifiers are mutable
-- and a settled mandate or payment must keep saying what it actually used. A
-- blank pair (both "") means the party was addressed only by its ids, the same
-- "no CHECK, unvalidated format" stance deposit_account_identifiers takes on
-- scheme and value, for the same reason: the known schemes are Go constants,
-- not a database enum.
CREATE TABLE mandates (
    id                         TEXT PRIMARY KEY,
    debtor_participant         TEXT NOT NULL,
    debtor_account             TEXT NOT NULL,
    debtor_identifier_scheme   TEXT NOT NULL,
    debtor_identifier_value    TEXT NOT NULL,
    creditor_participant       TEXT NOT NULL,
    creditor_account           TEXT NOT NULL,
    creditor_identifier_scheme TEXT NOT NULL,
    creditor_identifier_value  TEXT NOT NULL,
    max_amount                 BIGINT NOT NULL,
    status                     SMALLINT NOT NULL,
    created_at                 TIMESTAMPTZ,
    seq                        BIGSERIAL NOT NULL
);

CREATE TABLE payments (
    id                         TEXT PRIMARY KEY,
    scheme                     TEXT NOT NULL,
    debtor_participant         TEXT NOT NULL,
    debtor_account             TEXT NOT NULL,
    debtor_identifier_scheme   TEXT NOT NULL,
    debtor_identifier_value    TEXT NOT NULL,
    creditor_participant       TEXT NOT NULL,
    creditor_account           TEXT NOT NULL,
    creditor_identifier_scheme TEXT NOT NULL,
    creditor_identifier_value  TEXT NOT NULL,
    debtor_agent               TEXT NOT NULL DEFAULT '',
    debtor_name                TEXT NOT NULL DEFAULT '',
    creditor_agent             TEXT NOT NULL DEFAULT '',
    creditor_name              TEXT NOT NULL DEFAULT '',
    amount                     BIGINT NOT NULL,
    mandate_id                 TEXT NOT NULL,
    end_to_end_id              TEXT NOT NULL,
    status                     SMALLINT NOT NULL,
    reject_reason              TEXT NOT NULL,
    reject_code                TEXT NOT NULL DEFAULT '',
    cycle_id                   TEXT NOT NULL,
    booking_date               TIMESTAMPTZ,
    value_date                 TIMESTAMPTZ,
    description                TEXT NOT NULL,
    metadata                   JSONB,
    created_at                 TIMESTAMPTZ,
    debtor_leg_tx              TEXT NOT NULL,
    creditor_leg_tx            TEXT NOT NULL,
    seq                        BIGSERIAL NOT NULL
);

COMMENT ON COLUMN payments.reject_code IS
    'The external status-reason code (AC01, AM04, MD01, ...) a rejection '
    'carries in a pacs.002, alongside the free text in reject_reason. Both, '
    'not either: the code is what makes the rejection machine-actionable and '
    'the text is what says the part no code can. DEFAULT '''' rather than NULL '
    'because a payment that was never rejected has no code, and an absent code '
    'and an empty one are the same fact here.';

COMMENT ON COLUMN payments.debtor_name IS
    'The account holder name as QUOTED ON THE INSTRUCTION, not as held in the '
    'register. It is stored rather than looked up because BUILDING A PAYMENT '
    'looks it up nowhere: no bank reads the counterparty bank''s deposit '
    'register to fill this column in. A real payer''s bank knows the payee''s '
    'name because the payer typed it onto the instruction. (A lookup does '
    'exist — GET /directory resolves an address across the network and does '
    'read the resolved account''s name off its own bank''s register — but its '
    'answer never reaches the payment: what lands here is what was typed, not '
    'what was resolved.) The two NAME columns are therefore not a cache: there '
    'is nothing to fall back to, and a NULL here would be an unsendable '
    'payment.';

COMMENT ON COLUMN payments.creditor_agent IS
    'The BIC of the bank holding this party''s account, and — unlike the two '
    'name columns beside it — NOT what the instruction said. Both agent '
    'columns are DERIVED at submission from the participants row for the '
    'party this payment already names (payment.SubmitPaymentTx), and whatever '
    'a caller supplied is discarded. The reason is what this element DOES: it '
    'goes out as CdtrAgt/DbtrAgt and the clearing house ROUTES on it without '
    'reading anything, so a payer allowed to assert it would be a payer '
    'choosing which bank received the payment. Real SEPA is the same shape — '
    'IBAN-only since 2016, the originating bank derives the routing. It is '
    'therefore, TODAY, a redundant copy of what participants.bic holds for '
    'creditor_participant — no operation in this system changes a BIC once a '
    'bank is admitted, so the two cannot yet disagree. It is stored anyway, '
    'and the reason is what the row is: a record of the message that WAS '
    'SENT, not a view onto the roster as it stands now. PutParticipant is an '
    'upsert (see store/storetest), so the day a BIC can be corrected is the '
    'day a join would silently rewrite the address on every payment already '
    'settled. There is no foreign key for the same reason. The parallel '
    'comment on debtor_agent is deliberately not repeated; the two columns '
    'are one rule.';

COMMENT ON COLUMN payments.debtor_agent IS
    'See creditor_agent: derived from the roster at submission, never taken '
    'from the instruction, and stored rather than joined because this row '
    'records the message that was sent.';

-- Index 6: GetPaymentByEndToEndID. Deliberately NOT unique. store/mem does not
-- reject a duplicate client reference — payment.Network does, in
-- SubmitPaymentTx — and a store that refused one where mem accepted it would
-- be the two implementations disagreeing.
--
-- That argument is only sound because the application check is ATOMIC here, and
-- it was not always. SubmitPaymentTx read this index and then allocated the
-- payment id; under READ COMMITTED two concurrent submissions of one reference
-- both read nothing and both wrote, and eight concurrent ones were accepted
-- eight times against store/mem's one — the payer debited eight times for a
-- single client reference. The order is now the other way round: NextID runs
-- first, its INSERT … ON CONFLICT DO UPDATE takes a row lock on id_sequences,
-- and the second submission blocks there until the first commits and its
-- payment row is visible to the read. Same serialization AddParticipantTx
-- relies on, for the same reason.
--
-- So this index stays non-unique, and the conformance argument above stands as
-- written: PutPayment is a store operation and mem's does not look at any other
-- row, so a UNIQUE index here would refuse a write mem accepts, and would
-- refuse it as a constraint violation rather than as ErrDuplicateEndToEndID.
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

-- Index 7: GetOpenCycle. Partial on the open status, which is the only one it
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

-- settlement_advices is a MEMBER BANK's record of a cut-off it was told about,
-- and it is the first payment-layer table keyed by book.
--
-- Every other table in this section — participants, payments, mandates, cycles,
-- settlements — is network-scoped: those rows belong to no single bank, so they
-- are keyed by their id alone. Note the exact claim, because a looser one is
-- false: participants DOES carry a book_id column (:539), but as DATA — which
-- book that bank owns — and not as part of its key. This is the first
-- payment-layer table where the book is part of the identity, and that
-- difference is the whole of sub-project 8. A cycle is the clearing house's; a
-- settlement is the central bank's; this is the member's, and when the stores
-- split it moves into that member's own database and the other two do not
-- follow it.
--
-- Two banks advised of one cut-off write two rows independently. That is not
-- redundancy: settlement is final at the central bank and participants catch up
-- afterwards, so "this bank has booked it and that one has not" is a state the
-- system must be able to be in — and it shows as one row present and the other
-- ABSENT.
--
-- What a row MEANS is that this bank booked this cut-off. No committed row says
-- status 0 (payment.AdviceAdvised) today: payment.PostSettlementAdviceTx writes
-- the row and posts the mirror leg in ONE unit of work, so a failed posting rolls
-- the row back with it and a successful one leaves status 1. That is deliberate —
-- the leg and the record of it must be atomic — and this comment used to claim
-- the opposite, that a row stuck at status 0 was the unreconciled position and
-- the only trace of a posting that failed. It never was. The unreconciled
-- position is the ABSENCE of a row against a clearing suspense that has not
-- returned to zero, and detecting it is Task 19's.
--
-- closing_balance is what the central bank said the reserve stands at. Nothing
-- reads it yet; Task 19 is the reconciliation that does. It is stored because it
-- arrives, and a balance discarded on receipt is one nobody can go back for.
--
-- No foreign key to cycles. A member bank HAS no cycles — after the split the
-- cycles table is not in its database at all — so a constraint here would encode
-- exactly the sharing this sub-project removes.
CREATE TABLE settlement_advices (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    cycle_id        TEXT NOT NULL,
    asset           TEXT NOT NULL,
    movement        BIGINT NOT NULL,
    closing_balance BIGINT NOT NULL,
    -- SMALLINT, like every other status column in this schema, because
    -- payment.AdviceStatus is an int enum and store/pg stores those as their
    -- ordinal. status 0 is payment.AdviceAdvised, 1 is payment.AdvicePosted.
    status          SMALLINT NOT NULL,
    mirror_tx       TEXT NOT NULL DEFAULT '',
    advised_at      TIMESTAMPTZ,
    posted_at       TIMESTAMPTZ,
    seq             BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, cycle_id, asset)
);

COMMENT ON COLUMN settlement_advices.movement IS
    'SIGNED: positive means this bank''s reserve went up. The statement it came '
    'from carries a magnitude and a CdtDbtInd, because the ISO 20022 money type '
    'cannot be negative; the sign is reconstructed on the way in and stored, '
    'because a mirror leg posted in the wrong direction is the most expensive '
    'way to be wrong about a settlement.';

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

-- Indexes 8 and 9: the two shapes every audit query has. Book+scope is what a
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
    'What this account has accrued over its WHOLE LIFE, same scale as '
    'accrued_interest. Overdraft interest is recomputed rather than '
    'incremented: every end-of-day re-derives every day since the account''s '
    'opening terms row from its value-dated balance, and accrued_interest '
    'moves by the change in this column. That is what makes a backdated '
    'posting correct itself — the days it takes effect over are re-derived '
    'with it in place, this figure moves, and the next run posts the '
    'difference. Unlike accrued_interest it is never decremented by '
    'capitalization, and unlike before terms became effective-dated it is '
    'never RESET: there is no window to restart, because every day is '
    're-derived at the terms that were actually in force on it (see the '
    'overdraft_terms table). A store that dropped this column would re-derive '
    'the account''s whole life as a fresh delta every night and charge the '
    'same interest over and over.';

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

COMMENT ON COLUMN overdraft_terms.product_id IS
    'The catalogue entry this account is on from this day. On the row rather '
    'than on deposit_accounts because it varies over the account life: '
    'migrating between products is an ordinary forward-dated row, and a column '
    'on the account would contradict the timeline the moment a future-dated '
    'migration was entered. It carries no foreign key to products, for the '
    'reason subledgers.ledger_id carries none: "the parent must exist" is a '
    'domain rule, enforced by deposit.Register, and a constraint here would '
    'make store/pg refuse a write store/mem accepts.';

COMMENT ON COLUMN overdraft_terms.rate IS
    'Annual interest rate on the arranged overdraft, in MILLIONTHS: 1000000 is '
    '100%, 150000 is 15% (interest.RateScale). Zero makes the WHOLE overdraft '
    'interest-free, which is a real product. unarranged_rate is the same scale '
    'and applies to any balance drawn beyond overdraft_limit; it is an optional '
    'SURCHARGE, so zero there means rate applies throughout rather than that '
    'the excess is free. NULL in all three pricing columns means the pricing '
    'FLOATS: resolve it from the product version in force on this day. All '
    'three set is a negotiated overlay for this one customer. NULL does NOT '
    'mean interest-free — a zero-rate overlay is a real interest-free product '
    'and a different, deliberate statement — and the mixed state is refused by '
    'deposit.OverdraftTerms.Validate rather than by a CHECK, because store/mem '
    'must refuse exactly the same rows. There is deliberately NO CHECK on any '
    'of the three, and none on day_count: a CHECK enumerating the valid '
    'day-count conventions would make store/pg refuse a write store/mem '
    'performs — which store/storetest exists to prevent — and would turn a '
    'one-line change to a Go constant into a migration. This is the same '
    'reasoning recorded on the four asset columns, applied to a new case, and '
    'it is recorded in the database because neither a missing constraint nor '
    'the meaning of NULL is visible in a schema dump.';

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
    'What this facility has accrued over its WHOLE LIFE, same scale as '
    'accrued_interest. Facility interest is recomputed rather than '
    'incremented: every end-of-day re-derives every day since the facility''s '
    'opening terms row from the VALUE-DATED balance of principal_gl, and '
    'accrued_interest moves by the change in this column. That is what makes a '
    'backdated repayment or advance correct itself — the days it takes effect '
    'over are re-derived with it in place, this figure moves, and the next run '
    'posts the difference. Unlike accrued_interest it is never decremented by a '
    'repayment or a capitalization, which settle the receivable rather than the '
    'window, and unlike before terms became effective-dated it is never RESET: '
    'there is no window to restart, because every day is re-derived at the '
    'terms that were actually in force on it (see the facility_terms table) and '
    'the days before the first advance re-derive to zero on their own. A store '
    'that dropped this column would re-derive the facility''s whole life as a '
    'fresh delta every night and charge the same interest over and over.';

COMMENT ON COLUMN facilities.min_payment IS
    'The share of drawn principal added to a revolving line''s minimum payment '
    'each cycle, on top of the interest charged. It is in MILLIONTHS, the same '
    'scale as facility_terms.rate, but it is NOT a rate: it is dimensionless '
    'and not per annum (interest.Fraction), and the Go types are distinct so '
    'the compiler refuses to swap them. It stays on this row rather than moving '
    'to facility_terms because it feeds the BILLING, not the accrual — see that '
    'table''s comment for why nothing BuildSchedule reads is effective-dated.';

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
    'representation. A row effective in the PAST is always allowed and is '
    'picked up by the next recompute the same way a backdated posting is. A row '
    'effective in the FUTURE — created_at earlier than effective_from — is '
    'inert until the runs reach it, which is scheduled repricing for free, but '
    'only for a product with no instalment schedule to diverge from, which on '
    'this table means a revolving line. A term loan REFUSES one '
    '(lending.ErrScheduleWouldDiverge), because its schedule is pinned at '
    'disbursement to the row in force on the disbursement day and a later row '
    'is one the schedule cannot see but the accrual would reach anyway; see '
    'this table''s own comment. Compare overdraft_terms.created_at, where both '
    'directions really are unconditional, an overdraft having no schedule at '
    'all.';

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
