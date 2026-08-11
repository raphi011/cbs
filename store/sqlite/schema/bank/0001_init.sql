-- bank/0001_init: one member bank's whole world, in one migration.
--
-- There are three of these files and this is the largest. Each is one
-- institution's schema and one database at run time: a bank's, the clearing
-- house's, the settlement agent's. What a shape leaves OUT is the point of
-- having three — a bank has no cycles, no settlement rows and no roster,
-- because a bank does not know the network exists. It knows its customers, its
-- counterparties as they arrive in messages, and where to send one.
--
-- Split from a single 1729-line schema at Task 18. That file had all 31 tables
-- in one database and the boundary between institutions was a convention the
-- book recorder in mesh/books_test.go measured; here it is the DDL, so a
-- crossing is a table that is not there rather than a row nobody should have
-- read.
--
-- WHERE A COMMENT HAS TO GO, AND WHY IT IS NOT WHERE IT WAS
--
-- SQLite keeps the text of a statement in sqlite_master.sql, so a comment
-- INSIDE a statement's parentheses reaches `.schema` and a schema dump. A
-- comment ABOVE a statement does not: it is outside the span and is dropped.
-- Verified on modernc's compiled-in SQLite 3.53.3 before this file was written,
-- for a CREATE TABLE's column list and a CREATE INDEX's alike.
--
-- The Postgres original kept 595 of its 612 comment lines above statements,
-- with 42 COMMENT ON COLUMN for most of the rest. Under Postgres that was free,
-- because COMMENT ON is itself stored. Here it is not, and what would be lost is
-- the half that matters most — an ABSENT constraint has no column to hang a
-- comment from, so every argument about something this schema deliberately does
-- not do lived at column 0.
--
-- So: every argument about something the schema does NOT do lives inside the
-- statement it concerns. An absent constraint's reasoning goes in the
-- parentheses of the table it is absent from, an index's in its column list.
-- What stays out here is narrative belonging to no statement — this header —
-- and it does not reach a dump. If you are adding a reason, put it inside the
-- parentheses.
--
-- AND WHICH OF THE THREE FILES IT GOES IN
--
-- Six tables in this file are also in centralbank/0001_init.sql, because a
-- settlement agent keeps a book of accounts too, and one is also in
-- csm/0001_init.sql. Their DDL repeats; their ARGUMENTS do not. An argument
-- that spans two shapes is written once, in the shape whose absent constraint
-- it is about, and the other names it rather than restating it — copying is how
-- one fact ends up in nine places and then in three versions.
--
-- This file is the canonical home for most of them, because most of the columns
-- an argument is about are here: the seq allocation rule (ledgers.seq), the
-- absent CHECK on an asset code (accounts.asset), the absent parent foreign key
-- (subledgers), the absent UNIQUE on a name (ledgers), and the three rows one
-- admission writes (banks). The central bank's copy of the find-or-create race
-- is the one argument that goes the other way, because the chart of accounts it
-- is about is the central bank's; ledgers names it.
--
-- CONVENTIONS, APPLIED EVERYWHERE
--
--   * Book-scoped tables have a composite PRIMARY KEY (book_id, id). See books
--     for what that column means now that a database holds one book.
--   * Amounts are INTEGER in minor units. Never REAL: a ledger deals in whole
--     cents, and a rounding mode is not a thing a ledger may have.
--   * Times are TEXT and nullable — RFC3339, UTC, nine fractional digits,
--     always. The fixed width and the fixed zone are what make a string
--     comparison a chronological one; see store/sqlite/time.go. NULL is Go's
--     zero time.Time, which several fields use as "unset", and SQLite sorts
--     NULL first in ASC, which is what the listings want.
--   * iota enums are INTEGER, holding the Go constant's value.
--   * Free-form maps and the audit payload are TEXT with a json_valid CHECK.
--     It is the only constraint in this file that no layer above the store
--     duplicates: nothing in the domain asks whether a document parses, so an
--     unparseable one is refused here or nowhere.
--   * Every listed table carries a monotonic `seq`, allocated MAX(seq)+1 on
--     insert and left alone by the upsert branch, so editing a row does not
--     move it to the end of its list. Listings are ORDER BY created_at, seq —
--     never ORDER BY id, because IDs are counter-derived strings and "dep_10"
--     sorts before "dep_8". See ledgers.seq for why the allocation is explicit.
--   * Every table is STRICT. SQLite enforces no declared type otherwise, and a
--     schema that enforces nothing is worse than one with a coarser vocabulary.
--     STRICT admits INT, INTEGER, REAL, TEXT, BLOB and ANY, so BIGINT,
--     SMALLINT, TIMESTAMPTZ, BOOLEAN and JSONB are gone as spellings; what each
--     of them was saying is in a comment or a CHECK instead.

CREATE TABLE books (
    -- books exists so that book_id is a real key rather than a convention. Rows
    -- are created on demand by the first write that names a book (see
    -- tx.ensureBook), because a book is not an entity the domain creates — a
    -- BookID is a name a participant is written under.
    --
    -- IT HOLDS EXACTLY ONE ROW, and that is the whole of Task 18 seen from the
    -- smallest table in the schema. A bank's database is a bank's book; the
    -- store is opened for one BookID and refuses every other with
    -- sqlite.ErrNotThisStoresBook, so the only row that can ever appear here is
    -- the one this bank was opened as. Two participants both holding an account
    -- numbered 200.100.001 is still true and is still why chart-of-accounts ids
    -- are not global — but what keeps them apart is now two databases, not two
    -- values in this column.
    --
    -- The column therefore survives as a CONSTANT, and keeping it is a decision
    -- rather than inertia. Dropping it would rewrite the primary key of every
    -- table below and every statement in store/sqlite that names one, to remove
    -- a column whose value the store already refuses to get wrong; and
    -- id_sequences and audit_events are keyed by book in all three shapes, so
    -- the column could not go everywhere in any case. What it costs is a reader
    -- meeting (book_id, id) and inferring that a second book could appear here.
    -- It cannot. This row is the guarantee.
    id TEXT PRIMARY KEY
) STRICT;

-- ---------------------------------------------------------------------------
-- The general ledger
-- ---------------------------------------------------------------------------

CREATE TABLE ledgers (
    -- A note on what is NOT here: UNIQUE (book_id, name), and this is the
    -- canonical statement of why. centralbank/0001_init.sql's ledgers carries
    -- the half that is about the settlement agent's own chart of accounts and
    -- names this one for the rest.
    --
    -- The domain does not hold that invariant. A ledger, a subledger and an
    -- account are identified by their generated ID; names are labels and are
    -- allowed to repeat, because two customers called John Smith at one bank is
    -- not an error and neither is a bank filing two subledgers under one
    -- heading. ledger/numbering_test.go's TestSubledgerNumbering creates three
    -- subledgers all called "S" in one book precisely to check the numbering.
    -- So a constraint asserting that a name is an identity would refuse a write
    -- the domain has already decided to allow.
    --
    -- This used to be argued as a divergence — a constraint one store could hold
    -- and a Go map could not — and that reading expired with the second store.
    -- What is left is the reason underneath it, which never needed one.
    --
    -- What is NOT the reason, in this shape, is a race. Nothing in a bank's book
    -- is resolved by find-or-create on a name: a bank's chart of accounts is
    -- built once, by payment.FoundBankTx, in the act that creates the bank, and
    -- every account after that is created with an id the bank allocated. The
    -- find-or-create this constraint keeps getting proposed for is the CENTRAL
    -- BANK's, it runs in another institution's database, and the ordering that
    -- closes it is argued there.
    --
    -- Also not here: a CHECK on the text columns.
    --
    -- Under Postgres this table said that the database refuses some text of its
    -- own accord — a NUL byte is SQLSTATE 22021 in a TEXT column, as is any byte
    -- sequence that is not valid UTF-8 — while store/mem, a map of Go strings,
    -- refused none of it, and that ledger.ValidateText exists to close the
    -- asymmetry in the domain rather than in either store. SQLite refuses
    -- NEITHER: measured on this driver, "before\0after" and the four bytes
    -- 41 FF FE 42 both go into a STRICT TEXT column and come back
    -- byte-identical. So the divergence that prompted the rule is gone here and
    -- the rule stays, for the reason README:1271 already gives — a rule that can
    -- only be stated by naming a database is not a domain rule. What the system
    -- will accept is a domain question; these columns just hold the answer.
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id         TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TEXT,
    -- The row's monotonic insertion sequence, and the canonical statement of how
    -- every other `seq` in this schema is allocated.
    --
    -- It is not AUTOINCREMENT and could not be: SQLite's only automatic counter
    -- rides on an INTEGER PRIMARY KEY, and seq is not the key — book-scoped
    -- tables are keyed (book_id, id). So the value is allocated MAX(seq)+1
    -- inside the writing transaction, which is where README already argues every
    -- other counter in this system belongs: identity counters are ordinary rows,
    -- not database sequences, because a sequence survives a rollback on purpose
    -- and this must not. Postgres spelled this column BIGSERIAL and was the one
    -- place that rule was not applied; it is now, and seq is gap-free.
    -- id_sequences holds the same rule for the counters the domain draws IDs
    -- from.
    --
    -- What makes MAX+1 safe without further thought is that SQLite admits one
    -- writer: two transactions cannot both be allocating.
    seq        INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE TABLE subledgers (
    -- subledgers.ledger_id carries NO foreign key, and neither does
    -- accounts.subledger_id or entries.account_id. That is a rule about what
    -- this layer is for, not an oversight.
    --
    -- The store is a per-table key/value layer. "The parent must exist" is a
    -- domain rule, and ledger.Book enforces it: CreateSubledgerTx reads the
    -- ledger first, CreateAccountTx reads the subledger, PostTransactionTx reads
    -- every account it is about to touch. Put the same rule in the schema as
    -- well and it is enforced twice, in two places that answer differently: the
    -- constraint fires first, and it fires as a foreign-key violation where the
    -- domain would have said ErrLedgerNotFound.
    --
    -- This one used to be a composite FK, and while there were two stores what
    -- it cost was visible as a disagreement:
    --
    --   PutSubledger{ID: "100", LedgerID: "ldg_nope"}
    --     store/mem = nil
    --     store/pg  = a foreign-key violation
    --
    -- and this store, with foreign_keys on in the DSN, answers as store/pg did.
    -- There is no second store to disagree with now, so what keeps the FK out is
    -- the rule above and storetest/ParentReferencesAreNotEnforced, which writes
    -- a dangling LedgerID and requires the store to take it.
    --
    -- The child-table FKs are a different case and stay: the store writes both
    -- sides of those itself, within one statement sequence, so there is no
    -- caller who could ever produce an orphan. This shape has two of them —
    -- entries -> transactions and deposit_account_identifiers ->
    -- deposit_accounts — and the other two shapes have one each, cycle_payments
    -- -> cycles at the clearing house and settlement_positions -> settlements at
    -- the central bank. They name this paragraph rather than repeating it, and
    -- the exemption is the same in all three: an aggregate the store writes
    -- whole.
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id         TEXT NOT NULL,
    ledger_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TEXT,
    seq        INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE TABLE accounts (
    -- Every account is denominated in exactly one asset, fixed at creation.
    --
    -- The asset column is here and NOT on entries. An entry's asset is always
    -- its account's, so storing it twice would only create the possibility of
    -- the two disagreeing. PostTransaction derives it when it checks that debits
    -- equal credits within each asset.
    --
    -- There is no table of assets for it to reference either. An asset
    -- definition is a fact about the world rather than per-bank state — "BTC has
    -- 8 decimal places" is true in every book — so the known assets are a list
    -- in Go (ledger.LookupAsset), the same way payment schemes are Go types
    -- rather than rows.
    book_id      TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id           TEXT NOT NULL,
    subledger_id TEXT NOT NULL,
    name         TEXT NOT NULL,
    type         INTEGER NOT NULL,
    -- The asset this account is denominated in, fixed at creation, and the
    -- canonical statement of why NO asset column in ANY of the three shapes
    -- carries a constraint. deposit_accounts.asset, bank_assets.asset,
    -- facilities.asset and mandates.asset point here rather than repeat it, and
    -- so do settlement_member_accounts.asset in centralbank/0001_init.sql and
    -- roster_entry_assets.asset in csm/0001_init.sql. Three schemas is three
    -- occasions to state one rule differently; it is stated once.
    --
    -- It is recorded here, in the database, and not only in the file that built
    -- the database, because the ABSENCE of a constraint is what needs saying and
    -- an absence is invisible in a schema dump: the next author reads four TEXT
    -- columns holding "EUR" and "BTC" and adds the "missing" CHECK.
    --
    -- There is deliberately no CHECK restricting it to the known codes, and this
    -- is the canonical statement of why no column in this file enumerates a set
    -- that lives in Go. Every other absent CHECK points here.
    --
    -- Two reasons, and the second is now the whole of it. A store is a per-table
    -- key/value layer; "the asset must be one the system knows" is a DOMAIN
    -- rule, enforced by ledger.Book.CreateAccountTx against the list in
    -- ledger.LookupAsset — exactly where "the parent must exist" lives for
    -- ledgers, subledgers and accounts. And a CHECK would make adding an asset,
    -- which is a one-line change to a Go slice, a MIGRATION: the set would live
    -- in two places, and the database's copy would be the one that decided.
    --
    -- The first reason used to be argued the other way round — a SQL store could
    -- express the rule and a Go map could not, so neither did, because two
    -- implementations accepting and refusing different writes was the one thing
    -- store/storetest existed to forbid. That argument had a second store inside
    -- it and expired with it. The second never mentioned one, which is why it is
    -- the one left standing.
    --
    -- What holds the decision is still a test: ParentReferencesAreNotEnforced in
    -- store/storetest/storetest.go writes accounts with no asset set at all, so
    -- a CHECK added here fails it. An earlier composite FK on
    -- subledgers (book_id, ledger_id) broke that same subtest and was removed for
    -- the same reason.
    asset        TEXT NOT NULL,
    -- Whether this account pools subsidiaries: one chart-of-accounts line standing
    -- for many customers, with entries.subsidiary_id saying which. Fixed at
    -- creation, like type and asset, and for a stronger reason than either —
    -- flipping it would strand every leg already posted on the wrong side of
    -- both refusals below, and nothing afterwards could tell which pool they
    -- had belonged to.
    --
    -- The two refusals are the substance and neither is expressible here, since
    -- a control account and a plain one differ only in which entries they will
    -- ACCEPT. ledger.Book.PostTransactionTx refuses an entry against a control
    -- account that names no subsidiary — money in the pool belonging to nobody,
    -- with the control figure right and every detail under it wrong — and
    -- refuses one against a plain account that names one, which writes a
    -- dimension nothing will ever read. Both post cleanly under a CHECK-less
    -- schema, both keep debits equal to credits, and neither is recoverable
    -- afterwards.
    --
    -- What this column is NOT is a stored control figure. A control account's
    -- balance is still an aggregation over entries — see entries.subsidiary_id,
    -- which carries that argument.
    control      INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT,
    seq          INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE TABLE slot_accounts (
    -- Which account a posting flow writes to: the line a customer's money pools
    -- in, the receivable an accrual debits, the revenue it credits. A slot is
    -- the ROLE and this table says which account fills it, so the answer is a
    -- row an operator can read rather than a string literal in two Go packages
    -- that must not import each other.
    --
    -- What is ABSENT is a foreign key to accounts, and a CHECK on any of these
    -- columns. account_id carries none for the reason given on accounts.asset;
    -- the constraints that matter here are not expressible in SQL anyway. A slot
    -- declares the account TYPE it requires and whether that account must pool
    -- subsidiaries, both of which live in Go (ledger.Slot), and
    -- ledger.Book.MapSlotTx checks them at the write — which is the point of
    -- checking there at all. A mapping is configuration, so the alternative to
    -- refusing a wrong account now is a posting that fails weeks later, at a
    -- moment nobody connects to the change that caused it.
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    -- The product this row is for, EMPTY on the bank-wide row that every flow
    -- resolves unless a product overrides it. It is an opaque string here and
    -- in the ledger both: the general ledger does not know what a product is,
    -- exactly as it does not know what a customer is (entries.subsidiary_id).
    --
    -- A product may only override a slot that carries NO BALANCE — an income or
    -- expense line. That rule is ledger.Slot.ByProduct and it is not written
    -- here because it is a property of the slot rather than of the row: a
    -- product-scoped balance line would leave what a customer has already
    -- posted in the old account while every later posting went to the new one,
    -- and the balance anybody read would be the second half only. Moving a
    -- balance between control accounts is a reclassification journal, which
    -- this system does not have.
    product    TEXT NOT NULL DEFAULT '',
    slot       TEXT NOT NULL,
    asset      TEXT NOT NULL,
    account_id TEXT NOT NULL,
    -- The key is the whole row bar the account, so pointing a slot somewhere
    -- else is an upsert rather than a second answer to a question that has one.
    PRIMARY KEY (book_id, product, slot, asset)
) STRICT;

CREATE TABLE transactions (
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id              TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    booking_date    TEXT,
    value_date      TEXT,
    status          INTEGER NOT NULL,
    description     TEXT NOT NULL,
    -- JSON, and the CHECK is the first constraint in this schema that no layer
    -- above the store duplicates: nothing in the domain asks whether a metadata
    -- document parses, so this is where an unparseable one is refused, and it is
    -- refused nowhere else.
    -- It admits NULL, which is a nil map — an empty one is '{}' and the
    -- two are kept apart because the API renders them differently.
    metadata        TEXT CHECK (json_valid(metadata)),
    reversal_of     TEXT NOT NULL,
    created_at      TEXT,
    seq             INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE UNIQUE INDEX transactions_idempotency_key_idx
    ON transactions (
        -- The idempotency index. Partial, because an empty key is an absent key
        -- rather than an identity, and unique, because that is the only way to
        -- make a duplicate impossible: a check-then-insert in Go has a window
        -- between the two statements that this index does not.
        --
        -- It is also the ONLY unique index in this schema that is not a primary
        -- key's, and the store's error mapping rests on that being true. SQLite
        -- names no index in a constraint error, so a duplicate is identified by
        -- the extended code alone: a unique-index conflict answers
        -- SQLITE_CONSTRAINT_UNIQUE (2067) and a primary-key conflict answers
        -- SQLITE_CONSTRAINT_PRIMARYKEY (1555), both measured on this driver. So
        -- 2067 means this index and nothing else, and store/sqlite maps it to
        -- ledger.ErrDuplicateIdempotencyKey — as targeted as store/pg matching
        -- the index name, with no message parsing.
        --
        -- Adding a second unique index would silently make that sentinel the
        -- answer to an unrelated collision. TestExactlyOneUniqueIndex reads
        -- sqlite_master and fails if one appears.
        --
        -- The claim is now per SHAPE and the test is three, because the mapping
        -- is one function over three schemas. This shape and the central bank's
        -- each hold exactly this index and no other; the clearing house's holds
        -- NONE, having no transactions table, so SQLITE_CONSTRAINT_UNIQUE cannot
        -- be raised in that database at all and the assertion there is zero.
        -- That is worth asserting rather than leaving implied: a unique index
        -- added to the csm shape would make a duplicate-key sentinel reachable
        -- in an institution that has no idempotency key to duplicate.
        book_id, idempotency_key
    )
    WHERE idempotency_key <> '';

CREATE TABLE entries (
    -- entries.position is explicit because Transaction.Entries is an ordered
    -- slice and a table has no order. entries.account_id carries no foreign key,
    -- for the reason subledgers.ledger_id does not.
    --
    -- entries.value_date is the one date that is NOT a copy. An entry's asset is
    -- always its account's, which is why accounts.asset exists and entries.asset
    -- does not — a second copy could only disagree. An entry's value date is a
    -- different case: the two legs of one event can legitimately take economic
    -- effect on different days. An outbound transfer debits the payer's account
    -- on the day it is debited, while the credit into the bank's clearing
    -- suspense carries the interbank settlement date. Storing only
    -- transactions.value_date would force one of those two to be wrong, and
    -- interest is computed from this column.
    --
    -- This table carries no `seq`: its order is the position column, which is
    -- data rather than an insertion artefact.
    book_id        TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    position       INTEGER NOT NULL,
    id             TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    -- What this leg belongs to within a control account: a deposit
    -- account, a facility. It is '' on an account that pools nothing, and
    -- accounts.control decides which of the two an account is — an account
    -- never holds both kinds of leg, which is what lets a reader take the
    -- absence of this column from a WHERE clause as "the whole pool".
    --
    -- It carries no foreign key and there is no table of subsidiaries, for the
    -- reason account_id carries none: the ledger does not know what a customer
    -- is, and the layer that does supplies a string.
    --
    -- What is ABSENT is the second entries table. The classic arrangement keeps
    -- the customer detail in a subledger system, writes the control figure into
    -- the general ledger independently, and reconciles the two nightly because
    -- they can drift. Here the control figure IS this column dropped from the
    -- WHERE clause, so Σ(detail) == control is one statement read two ways.
    -- That is the whole reason a customer account can leave the chart of
    -- accounts without a stored total taking its place.
    subsidiary_id  TEXT NOT NULL DEFAULT '',
    amount         INTEGER NOT NULL,
    direction      INTEGER NOT NULL,
    value_date     TEXT,
    PRIMARY KEY (book_id, transaction_id, position),
    FOREIGN KEY (book_id, transaction_id) REFERENCES transactions (book_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX entries_account_idx ON entries (
    -- The value_date suffix serves the value-dated balance and the per-day
    -- movement series. The (book_id, account_id) prefix is unchanged, so
    -- BookBalance keeps the index it always had.
    --
    -- subsidiary_id sits BETWEEN account_id and value_date so that prefix
    -- survives: a control account's own balance reads the whole pool and must
    -- not pay for a dimension it is not filtering on, while one subsidiary's
    -- balance and one subsidiary's day series both get a full prefix match.
    book_id, account_id, subsidiary_id, value_date
);

-- ---------------------------------------------------------------------------
-- The deposit layer
-- ---------------------------------------------------------------------------

CREATE TABLE deposit_accounts (
    -- What is ABSENT here is the column that made a customer a line in the chart
    -- of accounts. There is no gl_account: a customer's money is the balance of
    -- the bank's customer-deposit CONTROL account for this row's asset, taken
    -- with this row's id in the WHERE clause (entries.subsidiary_id). A bank
    -- with fifty thousand customers therefore has one chart-of-accounts row for
    -- all of them, per asset, and a hundred thousand rows here.
    --
    -- No stored control figure takes the column's place, which is the point: the
    -- pool's balance is the same sum with the id dropped, so Σ(detail) ==
    -- control cannot drift and there is nothing to reconcile nightly.
    --
    -- Which control account it is follows from the asset below, resolved by
    -- NAME in deposit.Register: nothing in this table points into the chart of
    -- accounts, which is what "a customer account is not one of its rows" means
    -- when it is written as a schema.
    book_id           TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id                TEXT NOT NULL,
    name              TEXT NOT NULL,
    status            INTEGER NOT NULL,
    -- The asset this deposit account is denominated in, and the whole of what
    -- decides where its money is pooled. It is the account's own fact rather
    -- than a copy of anything, and it is fixed for life exactly as a GL
    -- account's asset is: an account whose asset changed would have its history
    -- in one control account and its balance in another. Unconstrained, for the
    -- reason given on accounts.asset.
    asset             TEXT NOT NULL,
    -- Interest earned and not yet charged, in MICRO-MINOR-UNITS: the asset's
    -- minor unit multiplied by 1e6 (interest.AccruedScale). It is not a money
    -- column and must never be compared with, or summed alongside, one. The
    -- scale exists because a day's interest on a small balance is mostly
    -- fraction — 50 EUR overdrawn at 15% accrues 2.054794 cents a day, and
    -- rounding that to 2 daily discards 0.054794 cents a day: 20.0 cents a year
    -- against 750 cents of annual interest, a 2.67% error. The general ledger
    -- holds the rounded figure in the bank's accrued-interest receivable under
    -- this row's id; this column holds the residue an integer of minor units
    -- cannot represent, which is the same reason holds live outside the ledger.
    -- Recorded here because a scale carried in an integer column is invisible in
    -- a schema dump.
    accrued_interest  INTEGER NOT NULL DEFAULT 0,
    -- What this account has accrued over its WHOLE LIFE, same scale as
    -- accrued_interest. Overdraft interest is recomputed rather than
    -- incremented: every end-of-day re-derives every day since the account's
    -- opening terms row from its value-dated balance, and accrued_interest moves
    -- by the change in this column. That is what makes a backdated posting
    -- correct itself — the days it takes effect over are re-derived with it in
    -- place, this figure moves, and the next run posts the difference. Unlike
    -- accrued_interest it is never decremented by capitalization, and unlike
    -- before terms became effective-dated it is never RESET: there is no window
    -- to restart, because every day is re-derived at the terms that were
    -- actually in force on it (see the overdraft_terms table). A store that
    -- dropped this column would re-derive the account's whole life as a fresh
    -- delta every night and charge the same interest over and over.
    accrued_gross     INTEGER NOT NULL DEFAULT 0,
    last_accrual_date TEXT,
    created_at        TEXT,
    seq               INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE TABLE deposit_account_identifiers (
    -- An account's external addresses: what a counterparty quotes to pay it. The
    -- account's own id is never one of these.
    --
    -- Two things this table deliberately does NOT say.
    --
    -- First, there is no UNIQUE (book_id, scheme, value). "One bank issues an
    -- address once" is a real domain rule and deposit.Register enforces it by
    -- reading before it writes — but nothing serializes two concurrent adds the
    -- way an id allocation serializes an admission's acts, so the constraint and
    -- the domain rule would not refuse the same writes: the constraint would fire
    -- on a race the register lets through, and it would fire as a constraint
    -- violation rather than as deposit.ErrIdentifierTaken. A rule enforced in two
    -- places that disagree about when is enforced in neither. See the note on
    -- UNIQUE (book_id, name) on ledgers, which is the same argument. The
    -- primary key is therefore widened with deposit_account_id so that it is a
    -- row identity rather than the domain rule in disguise, and the lookup index
    -- is a plain index. The residual duplicate is caught at READ time, ON THE
    -- ROUTING PATH: Register.ResolveIdentifier answers ErrIdentifierAmbiguous
    -- rather than picking one, so no address ever routes money to an account
    -- chosen by listing order. It is a claim about
    -- resolution and nothing else — SubmitPaymentTx is handed an account id and
    -- never resolves, so two accounts colliding on one address both stay payable
    -- by id. That is correct: the accounts are distinct and real, and what is
    -- ambiguous is the address.
    -- storetest/IdentifierUniquenessIsNotEnforced pins all of this.
    --
    -- Second, there is no CHECK on scheme or value, and the value half of that
    -- is where the argument for an absent constraint is at its strongest in this
    -- file. The known schemes are Go constants (deposit.IdentifierIBAN), the way
    -- assets and payment schemes are. The FORMAT of a value IS validated, and it
    -- is validated in Go: an IBAN is ISO 13616, so the rule is a table of country
    -- structures plus mod-97-10 plus Italy's CIN plus France's clé RIB, none of
    -- which SQLite can express. A CHECK could at best restate a length, which
    -- would be one country's length written in the place least able to change
    -- and no help at all against a transposed digit. See the canonical absent-
    -- CHECK argument on accounts.asset; this is the same rule with a rule too
    -- large to copy rather than merely too located elsewhere.
    --
    -- What this table therefore holds is any string a caller could persuade the
    -- register to write. That is not a gap: deposit.Identifier.Validate refuses a
    -- malformed IBAN on the way in, a bank MINTS its customers' addresses rather
    -- than accepting them, and a row here that fails the check digits is one no
    -- code path can produce.
    --
    -- WHERE THE MINTING AUTHORITY COMES FROM is one column over and one
    -- institution away: the (country, bank_code) on this bank's own row in banks,
    -- allocated by a national registry at admission and delivered on the
    -- acmt.010. A register can only mint under the allocation it was given, which
    -- is what makes the bank code inside a value here a true statement about who
    -- holds the account rather than a claim the caller made — and it is what
    -- every other member routes on, out of its own copy of the published
    -- directory (see routing_directory). A bank with no allocation mints nothing
    -- and has no rows here at all.
    --
    -- The parent FOREIGN KEY, unlike subledgers.ledger_id, DOES stay. It is the
    -- exemption stated on subledgers: PutDepositAccount writes both sides
    -- itself, within one statement sequence, so no caller can produce an orphan.
    -- Identifiers are modelled as part of the account aggregate precisely so
    -- that this holds.
    book_id            TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    deposit_account_id TEXT NOT NULL,
    scheme             TEXT NOT NULL,
    value              TEXT NOT NULL,
    PRIMARY KEY (book_id, deposit_account_id, scheme, value),
    FOREIGN KEY (book_id, deposit_account_id)
        REFERENCES deposit_accounts (book_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX deposit_account_identifiers_lookup_idx
    ON deposit_account_identifiers (
        -- ListDepositAccountsByIdentifier. Not UNIQUE, on purpose; see the
        -- table.
        --
        -- Its third column does not serve an IBAN lookup, and that is worth
        -- stating here rather than leaving someone to discover it from a query
        -- plan. A stored address is already canonical, so it is not the ROW that
        -- forces a derived comparison — it is the QUERY: an IBAN read off a
        -- statement is grouped in fours and may be lower-cased, and those are
        -- the same address. The lookup therefore normalises both sides, and a
        -- predicate on upper(replace(value, …)) cannot use an index on value.
        -- The (book_id, scheme) PREFIX still can, so the scan is over one bank's
        -- identifiers in one scheme rather than the table — the index is
        -- narrowed here, not dead.
        --
        -- Normalising the query alone would restore the column and would be
        -- WRONG rather than merely narrow: the store is handed rows and cannot
        -- assume every one of them came from the minter, and a comparison rule
        -- that held only for canonical rows is not the rule
        -- deposit.Identifier.MatchValue states.
        --
        -- An index on the same expression would restore the third column;
        -- upper() and replace() are deterministic, so an expression index is
        -- legal here as it was in Postgres — measured on this driver. It is not
        -- created because no database is deployed and a bank here has a handful
        -- of accounts, so there is nothing to measure it against.
        book_id, scheme, value
    );

CREATE TABLE holds (
    book_id     TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id          TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    amount      INTEGER NOT NULL,
    expires_at  TEXT,
    description TEXT NOT NULL,
    status      INTEGER NOT NULL,
    created_at  TEXT,
    seq         INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE INDEX holds_account_idx ON holds (
    -- Index 2: ActiveHoldTotal and ListHoldsForAccount.
    book_id, account_id
);

CREATE TABLE snapshots (
    -- A snapshot is identified by (account, business date) at day granularity,
    -- so the date key is part of the primary key and a second snapshot for the
    -- same day replaces the first. date_key is the value of
    -- deposit.SnapshotDateKey, and it is a day string for the reason
    -- overdraft_terms.day_key is one.
    book_id           TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    account_id        TEXT NOT NULL,
    date_key          TEXT NOT NULL,
    business_date     TEXT,
    book_balance      INTEGER NOT NULL,
    holds_balance     INTEGER NOT NULL,
    available_balance INTEGER NOT NULL,
    taken_at          TEXT,
    seq               INTEGER NOT NULL,
    PRIMARY KEY (book_id, account_id, date_key)
) STRICT;

CREATE TABLE overdraft_terms (
    -- An account's arranged overdraft terms from one day onwards: one row per
    -- repricing, appended rather than editing what an earlier day already said.
    -- A second row for the SAME effective day does replace the first — the day
    -- is part of the primary key below, and day_key's comment says why.
    --
    -- These four values used to be mutable columns on deposit_accounts, and that
    -- is the one place this schema broke its own rule. Every financial
    -- calculation here is a function of account state, event history and
    -- configuration; the first two are immutable and replayable, and a
    -- configuration that could be edited in place undermined that entirely,
    -- because "what did this account's product say on 15 July 2027?" had no
    -- stable answer. It also bounded the interest recompute window at the last
    -- repricing: a repricing closed the old window out and opened a new one at
    -- itself, so a backdated posting landing behind it was trued up only from
    -- the repricing forward, and the days between where it took effect and the
    -- repricing kept the interest computed without it, permanently.
    --
    -- These are PER-INSTANCE terms: one timeline per account, not a catalogue
    -- shared across them. There is no product/product_version table, no content
    -- hash, no pinned-versus-floating parameter binding and no overlays. Those
    -- are the full machinery a real product engine needs and are far beyond this
    -- schema's scope; the effective-dated record is not.
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    account_id      TEXT NOT NULL,
    -- The UTC calendar day these terms first apply, as YYYY-MM-DD, and part of
    -- the primary key: a terms row is identified by (account, DAY), so a second
    -- row entered for the same effective day replaces the first and "the terms
    -- in force on day D" is unique by construction rather than by a validation
    -- rule. Go does the truncating (deposit.TermsDayKey over ledger.DayStart)
    -- and this column is what both the listing and the as-of lookup order and
    -- compare on — an ISO day is lexicographically ordered, so a text compare is
    -- a day compare, which is the same property this schema's TEXT timestamps
    -- are chosen for. The key is a day rather than a timestamp because accrual
    -- iterates whole UTC days: terms changing part-way through a day would have
    -- no well-defined meaning, since the day is the unit the arithmetic is
    -- expressed in. The store truncates nothing, because date arithmetic in a
    -- dialect is one DST-adjacent edge case away from disagreeing with
    -- ledger.DayStart about which day a repricing landed in — and which day it
    -- landed in is a domain answer. Compare snapshots.date_key, which is the
    -- same pattern for the same reason.
    day_key         TEXT NOT NULL,
    -- The same day as day_key, as a timestamp, and the value Go reads back. It
    -- is stored beside the key rather than derived from it so that a reader of
    -- this table sees a date rather than a string, and so that ORDER BY
    -- effective_from and ORDER BY day_key can never disagree.
    effective_from  TEXT,
    -- The catalogue entry this account is on from this day. On the row rather
    -- than on deposit_accounts because it varies over the account life:
    -- migrating between products is an ordinary forward-dated row, and a column
    -- on the account would contradict the timeline the moment a future-dated
    -- migration was entered. It carries no foreign key to products, for the
    -- reason subledgers.ledger_id carries none: "the parent must exist" is a
    -- domain rule, enforced by deposit.Register, and a constraint here would
    -- enforce it a second time in a place that answers a constraint violation
    -- instead of the domain's refusal.
    product_id      TEXT NOT NULL,
    overdraft_limit INTEGER NOT NULL,
    -- Annual interest rate on the arranged overdraft, in MILLIONTHS: 1000000 is
    -- 100%, 150000 is 15% (interest.RateScale). Zero makes the WHOLE overdraft
    -- interest-free, which is a real product. unarranged_rate is the same scale
    -- and applies to any balance drawn beyond overdraft_limit; it is an optional
    -- SURCHARGE, so zero there means rate applies throughout rather than that
    -- the excess is free. NULL in all three pricing columns means the pricing
    -- FLOATS: resolve it from the product version in force on this day. All
    -- three set is a negotiated overlay for this one customer. NULL does NOT
    -- mean interest-free — a zero-rate overlay is a real interest-free product
    -- and a different, deliberate statement — and the mixed state is refused by
    -- deposit.OverdraftTerms.Validate rather than by a CHECK, so that a caller
    -- is told which combination is wrong instead of being handed a constraint
    -- violation. There is deliberately NO CHECK on any of the three, and none on
    -- day_count: a CHECK enumerating the valid day-count conventions would turn
    -- a one-line change to a Go constant into a migration, and would put the set
    -- in two places with the database's copy deciding. This is the reasoning
    -- recorded on accounts.asset, applied to a new case, and it is
    -- recorded in the database because neither a missing constraint nor the
    -- meaning of NULL is visible in a schema dump.
    rate            INTEGER,
    unarranged_rate INTEGER,
    day_count       INTEGER,
    -- When this repricing was ENTERED, as against effective_from, which is when
    -- it takes economic effect. The pair is the booking-date/value-date
    -- distinction applied to configuration, for exactly the reasons the README
    -- gives for money: a repricing agreed on the 1st and entered on the 15th is
    -- the ordinary case, and refusing it would leave the agreed date with no
    -- representation. Both directions are allowed — a row effective in the past
    -- is picked up by the next recompute the same way a backdated posting is,
    -- and one effective next month is inert until the runs reach it, which is
    -- scheduled repricing for free.
    created_at      TEXT,
    seq             INTEGER NOT NULL,
    PRIMARY KEY (book_id, account_id, day_key)
) STRICT;

CREATE INDEX overdraft_terms_account_idx ON overdraft_terms (
    -- Index 3: the timeline read that accrual makes once per account per run,
    -- and the bounded as-of lookup balanceTx makes on every withdrawal check.
    -- Both filter on (book_id, account_id) and order by day_key, so one index
    -- serves both — the as-of lookup is an ORDER BY day_key DESC LIMIT 1 over
    -- the same prefix. It is not redundant with the primary key: the PK's
    -- leading columns are the same, so the planner can in fact serve both from
    -- it, and this index is declared anyway so that a future change to the
    -- primary key (an id column, say) does not silently turn every accrual into
    -- a full scan.
    book_id, account_id, day_key
);

-- ---------------------------------------------------------------------------
-- The product catalogue
-- ---------------------------------------------------------------------------

CREATE TABLE products (
    -- A catalogue entry: the named product an account is opened FROM. Separate
    -- from its versions because a product needs a name before it has a price,
    -- and because listing the catalogue should not mean grouping a version
    -- table.
    --
    -- product_versions.product_id carries no foreign key, for the reason
    -- subledgers.ledger_id does not: "the parent must exist" is a domain rule,
    -- and product.Catalogue enforces it. A constraint here would enforce it a
    -- second time and answer a constraint violation rather than the domain's
    -- refusal. See accounts.asset.
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id         TEXT NOT NULL,
    name       TEXT NOT NULL,
    kind       INTEGER NOT NULL,
    -- Postgres spelled this BOOLEAN. Under STRICT it is INTEGER holding 0 or 1,
    -- which is what SQLite stored underneath the spelling anyway, and what the
    -- driver binds a Go bool to.
    retired    INTEGER NOT NULL,
    created_at TEXT,
    seq        INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE TABLE product_versions (
    -- What a product cost from one day onwards: one row per repricing, never
    -- changed once published.
    --
    -- The primary key is (book_id, product_id, day_key), which is where the
    -- book's non-overlapping-interval exclusion constraint went. Keying by DAY
    -- makes "the version in force on D" unique by construction, so there is no
    -- interval to exclude. That decision was originally defended by saying a Go
    -- map could hold the same rule where a range type and an exclusion
    -- constraint could not; the reason has got stronger rather than weaker,
    -- because SQLite has no range exclusion either, so it is no longer "one
    -- backend could not" but "no backend can". It is the discipline
    -- overdraft_terms and snapshots already follow.
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    product_id      TEXT NOT NULL,
    day_key         TEXT NOT NULL,
    effective_from  TEXT,
    rate            INTEGER NOT NULL,
    unarranged_rate INTEGER NOT NULL,
    day_count       INTEGER NOT NULL,
    -- sha256 over the identity and the pricing (see product.Version.ComputeHash),
    -- computed at publication and VERIFIED on every read that prices a day. It
    -- is the only control on the one hole a domain-layer refusal cannot cover: a
    -- direct UPDATE to a published row. A mismatch fails the accrual rather than
    -- pricing a day from a row nobody published.
    hash            TEXT NOT NULL,
    -- NULL means DRAFT: the row is editable and is invisible to resolution, so
    -- the published version before it stays in force through it. Non-NULL
    -- freezes the row — product.Catalogue refuses a write to it, and the refusal
    -- is in the domain layer rather than a CHECK because a CHECK could not
    -- express it: what makes the write illegal is the row's PREVIOUS state, and
    -- a CHECK sees only the row being written. The hash column above is the one
    -- control that does survive a direct UPDATE.
    published_at    TEXT,
    created_at      TEXT,
    seq             INTEGER NOT NULL,
    PRIMARY KEY (book_id, product_id, day_key)
) STRICT;

CREATE INDEX product_versions_product_idx ON product_versions (
    -- Index 4: the timeline read the accrual makes once per product per run, and
    -- the bounded as-of lookup an operator view makes. Both filter on
    -- (book_id, product_id) and order by day_key, so one index serves both. Not
    -- redundant with the primary key for the reason Index 3 is not.
    book_id, product_id, day_key
);

-- ---------------------------------------------------------------------------
-- The lending layer
-- ---------------------------------------------------------------------------

CREATE TABLE facilities (
    -- A credit facility: a term loan or a revolving credit line. The mirror of a
    -- deposit account, and no more a line in the chart of accounts than one:
    -- what is ABSENT here is principal_gl, interest_gl and refund_gl. A
    -- facility is a SUBSIDIARY under three control accounts — drawn principal,
    -- accrued interest receivable, and interest the bank owes back — and its own
    -- id is the value in entries.subsidiary_id that says which of them is
    -- whose. A bank lending to ten thousand borrowers has three
    -- chart-of-accounts rows for the loan book and ten thousand rows here.
    --
    -- The refunds payable is the line that most looks like it should be per
    -- borrower, and it is the sharpest case for the dimension rather than an
    -- exception to it: pooled with no subsidiary, one balance cannot say who is
    -- owed what, and a refund against it could pay one borrower out of
    -- another's money and still balance, a Liability never being caught by the
    -- sufficiency check. What answers both is the subsidiary on the entry.
    --
    -- There is no row here for an arranged overdraft, and that is the design
    -- rather than an omission. An overdrawn current account's drawn amount IS
    -- the negative balance of its own Liability account viewed by sign; it has
    -- no independent existence, so a facility row for it would store a number
    -- that already exists. Its terms live in overdraft_terms, and its Asset-side
    -- classification is an aggregation (deposit.Totals). See README.md,
    -- "A Control Account, and Still an Aggregation".
    book_id           TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id                TEXT NOT NULL,
    kind              INTEGER NOT NULL,
    name              TEXT NOT NULL,
    -- The asset this facility is denominated in, and the whole of what decides
    -- which three control accounts it posts to — the same role
    -- deposit_accounts.asset plays. Fixed for life: a facility whose asset
    -- changed would have its history under one set of lines and its balance
    -- under another. Unconstrained, for the reason given on accounts.asset.
    asset             TEXT NOT NULL,
    -- What the bank has committed: a term loan's original principal, a revolving
    -- line's limit. One column rather than two because it plays the same role in
    -- both — the amount beyond which drawing is refused. The amount actually
    -- DRAWN is not stored: it is the balance of the loan-principal control
    -- account under this row's id, derived from the entries like every other
    -- balance here.
    commitment        INTEGER NOT NULL,
    method            INTEGER NOT NULL,
    term_months       INTEGER NOT NULL,
    -- The share of drawn principal added to a revolving line's minimum payment
    -- each cycle, on top of the interest charged. It is in MILLIONTHS, the same
    -- scale as facility_terms.rate, but it is NOT a rate: it is dimensionless
    -- and not per annum (interest.Fraction), and the Go types are distinct so
    -- the compiler refuses to swap them. It stays on this row rather than moving
    -- to facility_terms because it feeds the BILLING, not the accrual — see that
    -- table's comment for why nothing BuildSchedule reads is effective-dated.
    min_payment       INTEGER NOT NULL,
    -- Interest earned and not yet settled, in MICRO-MINOR-UNITS: the asset's
    -- minor unit multiplied by 1e6 (interest.AccruedScale). Not a money column;
    -- never sum it alongside one. It is SIGNED and routinely negative: a
    -- capitalization charges the rounded receivable, which can exceed what was
    -- earned, and the residue is absorbed by the next day's accrual. The general
    -- ledger holds the rounded figure under this row's id in the
    -- accrued-interest receivable. Recorded here because a
    -- scale carried in an integer column is invisible in a schema dump, and
    -- because a reader who saw the negative values would otherwise read them as
    -- corruption.
    accrued_interest  INTEGER NOT NULL,
    -- What this facility has accrued over its WHOLE LIFE, same scale as
    -- accrued_interest. Facility interest is recomputed rather than incremented:
    -- every end-of-day re-derives every day since the facility's opening terms
    -- row from this facility's VALUE-DATED drawn balance, and accrued_interest
    -- moves by the change in this column. That is what makes a backdated
    -- repayment or advance correct itself — the days it takes effect over are
    -- re-derived with it in place, this figure moves, and the next run posts the
    -- difference. Unlike accrued_interest it is never decremented by a repayment
    -- or a capitalization, which settle the receivable rather than the window,
    -- and unlike before terms became effective-dated it is never RESET: there is
    -- no window to restart, because every day is re-derived at the terms that
    -- were actually in force on it (see the facility_terms table) and the days
    -- before the first advance re-derive to zero on their own. A store that
    -- dropped this column would re-derive the facility's whole life as a fresh
    -- delta every night and charge the same interest over and over.
    accrued_gross     INTEGER NOT NULL DEFAULT 0,
    last_accrual_date TEXT,
    -- The calendar-day age of the oldest instalment still due and unpaid. This
    -- column, arrears_bucket, non_performing and oldest_unpaid_due are a CACHE:
    -- all four are a pure function of this facility's installments rows and a
    -- date (lending.ArrearsFor), recomputed at end of day and after every
    -- repayment, never accumulated from a stream of missed-payment events.
    -- Stored anyway because they are what the API and the web layer read, and
    -- recomputing four columns on every listing would make a delinquency report
    -- a join over every schedule in the book. A stale value is therefore a stale
    -- cache and not a lost fact: re-running the recompute repairs it. Days are
    -- ALWAYS actual calendar days, whatever day_count this facility accrues
    -- interest under — a 30/360 loan is not 33/360 days late.
    days_past_due     INTEGER NOT NULL,
    -- The band days_past_due falls in: Current, 1-29, 30-59, 60-89, 90+. Part of
    -- the arrears cache described on days_past_due.
    arrears_bucket    INTEGER NOT NULL,
    -- Set at 90+ days past due. It MARKS ONLY and changes no accounting.
    -- Non-accrual — where a non-performing loan stops recognizing interest into
    -- income — and expected-credit-loss provisioning are recorded as future work
    -- in docs/expansion-roadmap.md. Part of the arrears cache described on
    -- days_past_due. INTEGER holding 0 or 1, for products.retired's reason.
    non_performing    INTEGER NOT NULL,
    -- The due date days_past_due is measured from, NULL when the facility is
    -- current. Part of the arrears cache described on days_past_due.
    oldest_unpaid_due TEXT,
    status            INTEGER NOT NULL,
    opened_at         TEXT,
    maturity_at       TEXT,
    seq               INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE TABLE installments (
    -- One scheduled payment. A term loan's rows are generated in full at
    -- disbursement; a revolving line appends one per billing cycle, being that
    -- cycle's minimum payment, which is how a revolving facility actually falls
    -- into arrears.
    --
    -- principal and interest are the PLAN. What a repayment allocates to
    -- interest is the accrual, which under ACT/365 differs from a scheduled
    -- twelfth — see lending.Portfolio.Repay.
    book_id        TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    facility_id    TEXT NOT NULL,
    -- The instalment's position in the contract, 1-based, and part of its
    -- primary key. Distinct from the `seq` column, which is the row's monotonic
    -- insertion sequence used to break ordering ties everywhere else in this
    -- schema. ListInstallments orders by seq_no, not by seq or by due_date:
    -- seq_no is already a total order within a facility and a due date is not.
    seq_no         INTEGER NOT NULL,
    due_date       TEXT,
    principal      INTEGER NOT NULL,
    interest       INTEGER NOT NULL,
    paid_principal INTEGER NOT NULL,
    paid_interest  INTEGER NOT NULL,
    seq            INTEGER NOT NULL,
    PRIMARY KEY (book_id, facility_id, seq_no)
) STRICT;

CREATE TABLE facility_terms (
    -- A facility's credit terms from one day onwards: one row per repricing,
    -- appended rather than editing what an earlier day already said, and
    -- replaced only by a second row for the same effective day. The mirror of
    -- overdraft_terms — see there for why terms are rows at all — with two
    -- differences worth stating. There is no limit column, because
    -- facilities.commitment is not effective-dated: drawing is refused against
    -- the limit in force at the moment of the draw, and no past day's arithmetic
    -- depends on what it used to be. And there is no method, term_months or
    -- min_payment column, because those feed BuildSchedule rather than the
    -- accrual: a term loan's instalments are generated once from the rate in
    -- force at disbursement, so repricing one that already has a schedule is
    -- REFUSED (lending.ErrScheduleWouldDiverge) rather than allowed to drift —
    -- and so is a row effective after the day it is entered, which is a row the
    -- schedule pinned at disbursement could not see but the accrual would still
    -- reach. A revolving line may be repriced either way, having no schedule to
    -- diverge from.
    book_id        TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    facility_id    TEXT NOT NULL,
    -- The UTC calendar day these terms first apply, as YYYY-MM-DD, and part of
    -- the primary key: a terms row is identified by (facility, DAY), so a second
    -- row entered for the same effective day replaces the first and "the terms
    -- in force on day D" is unique by construction rather than by a validation
    -- rule. Go does the truncating (lending.TermsDayKey over ledger.DayStart)
    -- and this column is what both the listing and the as-of lookup order and
    -- compare on — an ISO day is lexicographically ordered, so a text compare is
    -- a day compare. The key is a day rather than a timestamp because accrual
    -- iterates whole UTC days: terms changing part-way through a day would have
    -- no well-defined meaning, since the day is the unit the arithmetic is
    -- expressed in. No store truncates for itself, which is one DST-adjacent
    -- edge case away from disagreeing with ledger.DayStart about which day a
    -- repricing landed in — and which day it landed in is a domain answer.
    -- Compare overdraft_terms.day_key, which is the same pattern for the same
    -- reason.
    day_key        TEXT NOT NULL,
    -- The same day as day_key, as a timestamp, and the value Go reads back. It
    -- is stored beside the key rather than derived from it so that a reader of
    -- this table sees a date rather than a string, and so that ORDER BY
    -- effective_from and ORDER BY day_key can never disagree.
    effective_from TEXT,
    -- Annual interest rate in MILLIONTHS: 1000000 is 100%, 60000 is 6%
    -- (interest.RateScale). Zero makes the WHOLE facility interest-free, which
    -- is a real product. There is deliberately NO CHECK on this column, and none
    -- on day_count: a CHECK enumerating the valid day-count conventions would
    -- turn a one-line change to a Go constant into a migration, and would put
    -- the set in two places with the database's copy deciding. This is the
    -- reasoning recorded on overdraft_terms.rate and on accounts.asset, applied
    -- to a new case, and it is recorded in the
    -- database because a missing constraint is invisible in a schema dump.
    rate           INTEGER NOT NULL,
    day_count      INTEGER NOT NULL,
    -- When this repricing was ENTERED, as against effective_from, which is when
    -- it takes economic effect. The pair is the booking-date/value-date
    -- distinction applied to configuration, for exactly the reasons the README
    -- gives for money: a repricing agreed on the 1st and entered on the 15th is
    -- the ordinary case, and refusing it would leave the agreed date with no
    -- representation. A row effective in the PAST is always allowed and is
    -- picked up by the next recompute the same way a backdated posting is. A row
    -- effective in the FUTURE — created_at earlier than effective_from — is
    -- inert until the runs reach it, which is scheduled repricing for free, but
    -- only for a product with no instalment schedule to diverge from, which on
    -- this table means a revolving line. A term loan REFUSES one
    -- (lending.ErrScheduleWouldDiverge), because its schedule is pinned at
    -- disbursement to the row in force on the disbursement day and a later row
    -- is one the schedule cannot see but the accrual would reach anyway; see
    -- this table's own comment. Compare overdraft_terms.created_at, where both
    -- directions really are unconditional, an overdraft having no schedule at
    -- all.
    created_at     TEXT,
    seq            INTEGER NOT NULL,
    PRIMARY KEY (book_id, facility_id, day_key)
) STRICT;

CREATE INDEX facility_terms_facility_idx ON facility_terms (
    -- Index 5: the timeline read that accrual makes once per facility per run,
    -- and the bounded as-of lookup a draw check makes. Both filter on
    -- (book_id, facility_id) and order by day_key, so one index serves both —
    -- the same reasoning as overdraft_terms_account_idx, applied to this table's
    -- own primary key.
    book_id, facility_id, day_key
);

-- ---------------------------------------------------------------------------
-- The payment layer
-- ---------------------------------------------------------------------------

CREATE TABLE banks (
    -- banks is the bank's own record of itself, and it holds EXACTLY ONE ROW.
    --
    -- ADMISSION WRITES THREE ROWS, ONE PER INSTITUTION, and this is the
    -- canonical statement of that. banks is the bank's record of itself;
    -- settlement_members is the central bank's record of an account it opened;
    -- roster_entries is the clearing house's record of where to send a message.
    -- Each has exactly one writer, and both of the others name this comment
    -- rather than repeat it.
    --
    -- The argument used to be made here because the other two tables were in
    -- this same file and their being separate rows was the only thing keeping
    -- them apart. It is made here now because they are in different DATABASES
    -- and this is the one a reader of a bank's schema will reach. That is the
    -- difference Task 18 makes to this paragraph: the claim did not change, the
    -- thing enforcing it did.
    --
    -- They were one row until admission became a conversation, and the reason
    -- they had to come apart is what the settlement agent used to do: it read
    -- the account it was to post to off the CLEARING HOUSE's row. That is a read
    -- across an institutional boundary on the settlement path, so a settlement
    -- agent given a database of its own would have had nothing to settle from.
    -- Splitting the row is what gives each institution a record it could hold
    -- alone — and each now HAS one, so the read that forced the split is no
    -- longer merely wrong, it is a table that is not there.
    --
    -- The two rows that are not the bank's are keyed by BIC. Neither institution
    -- allocates or is ever told a bank id — what a message carries is a BIC — so
    -- a bank id in either of them would be an identifier its owner has no way to
    -- have learnt and no way to check.
    --
    -- WHY ONE ROW AND NOT A LIST. There is no ListBanks in this shape's world.
    -- A bank knows itself; it learns a counterparty from the message that names
    -- one and never from a register, and the roster that says who exists is the
    -- clearing house's table in the clearing house's database. Every sweep this
    -- table used to serve — resolving an address across every member, reading a
    -- counterparty's BIC off its row, listing participants for an operator —
    -- was closed at Task 18a or moved to the clearing house at Task 18d, and
    -- what makes those closures hold is that there is nothing here to sweep.
    --
    -- THE ID IS THE BIC. It is also this bank's BookID (see books), and the name
    -- of this database. One identifier, and nothing allocates it.
    --
    -- It used to be a counter-derived string, bank_1, drawn from a counter every
    -- institution shared. Task 18 deleted that counter, and what it exposed is
    -- that the id had been doing two jobs: this bank's own name for itself, and
    -- THE NETWORK'S ADDRESS FOR IT. Only the first survives isolation. Eight
    -- readers in mesh turned a participant id into a BIC by reading a bank's row
    -- first — the clearing house resolving a roster entry, a bank deciding whose
    -- payment it had been sent — and every one of them is a read into a database
    -- its caller does not hold. A message has never carried a participant id, so
    -- there was no source for the value those readers started from either.
    --
    -- Collapsing the two is not a narrowing. Once the network's address is the
    -- BIC everywhere, a separate id would distinguish nothing: this table holds
    -- one row, so within this database the value is a constant, and the only
    -- thing that ever needs to tell banks apart is the composition root, whose
    -- identifier for a bank is the BIC already. A field whose value is always the
    -- same string is not an identity, it is ceremony.
    --
    -- What it also removes is a chicken and egg. A bank's database is named by
    -- its id; a counter-derived id would have to be allocated from a counter
    -- inside that database, which is the one place that cannot name it. That
    -- knot is why FoundBank was called through the CLEARING HOUSE's handle right
    -- up to this task — the joining bank had no handle of its own yet — and it
    -- dissolves rather than being worked around, because a joining bank arrives
    -- already knowing its BIC.
    --
    -- There is deliberately no UNIQUE and no CHECK. UNIQUE would be vacuous over
    -- one row. What actually refuses two banks on one address is
    -- roster_entries' primary key at the clearing house, which answers
    -- ErrBICAlreadyAdmitted; see csm/0001_init.sql. The structural rule for the
    -- code itself lives in iso20022.BIC, and a CHECK here would state it a second
    -- time in a dialect that cannot express it as well — see accounts.asset.
    -- The ISO 9362 business identifier code, which is this bank's id, its BookID
    -- and the name of its database. THREE COLUMNS BECAME ONE HERE and that is
    -- worth stating, because two of them are gone rather than renamed and a
    -- reader comparing this against an older dump will look for them.
    --
    -- `bic` was a second column holding this same value. It existed because the
    -- id was a counter-derived string and the BIC was the address the other two
    -- institutions held this bank by; with the id BEING the address there is one
    -- fact and it is stored once. What Go still has is two TYPES over it —
    -- payment.ParticipantID and iso20022.BIC — and that is not duplication: the
    -- structural rule for the code lives in iso20022.BIC.Validate, which is also
    -- why there is no CHECK here. A CHECK would state it a second time in a
    -- dialect that cannot express it as well; see accounts.asset.
    --
    -- `book_id` was a third, holding which book this bank owns. A bank IS its own
    -- book — that was already true and the column was already a copy — and now
    -- the book is fixed when the store is opened and refused every other value
    -- (sqlite.ErrNotThisStoresBook), so a stored copy could only ever be the same
    -- answer or a wrong one. The column also used to carry an argument that no
    -- longer has a subject: it was DATA rather than part of the key, and the
    -- distinction between payment-layer tables keyed by book and payment-layer
    -- tables keyed by id alone was the whole of sub-project 8. There is no
    -- network left for the second kind to belong to.
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL,
    customer_subledger TEXT NOT NULL,
    -- The catalogue entry this bank opens customer accounts from — the Basic
    -- product founding creates for every bank, before any scheme has heard of it
    -- (payment.FoundBankTx). It is data, not a handle like the Ledger and
    -- Deposit fields the store deliberately drops, so it has to be stored: a
    -- bank read back without it prices its accounts from a product id of "",
    -- which surfaces as "product not found" several layers away from the store
    -- that lost it.
    product_id         TEXT NOT NULL,
    -- There is NO column here saying how far through provisioning this bank is.
    -- bank_assets.settlement is empty until the settlement agent has opened an
    -- account and the bank has been told its number, and a status beside it would
    -- say the same thing a second time. What that emptiness means is a bank with
    -- a licence, a book and a chart of accounts and no place in a scheme: it can
    -- open customer accounts and take cash in, which lands in its own vault_cash,
    -- and it cannot LODGE that cash, because putting it on reserve needs the
    -- central bank to credit an account in the central bank's own book and no
    -- settlement agent holds one for it. A true state and not a broken one, and
    -- what an interrupted provisioning leaves behind — which is what makes
    -- re-driving one safe.
    -- The failure worth catching is not visible from inside this database at all:
    -- a bank quoting a settlement account no agent holds, or an agent holding one
    -- for a bank the clearing house does not route to, is three institutions'
    -- rows disagreeing. Finding those means walking all three against each other,
    -- which is payment/recon and not a column here.
    --
    -- The reference of the admission this bank recorded a membership
    -- under: what it accepted, and not what any other institution says about it.
    -- NOT NULL and empty until it has recorded one, because it has accepted
    -- nothing yet.
    -- It is the only thing that can refuse an acknowledgement belonging to
    -- another admission. One application asks for one currency, so a bank joining
    -- in two assets is answered twice and RecordMembershipTx has to record or
    -- extend rather than refuse a second answer — which without this column
    -- leaves it unable to tell the second answer of THIS admission from an
    -- acknowledgement of a different one. Measured: a message naming a member's
    -- own BIC and quoting an admission it had never heard of moved that bank's
    -- settlement reference onto an invented account, leaving this row
    -- disagreeing with settlement_members about which account the bank holds.
    -- It is NOT roster_entries.admission_ref duplicated. That one decides
    -- between two institutions contending for an address, in a row this
    -- institution does not own and which is now in another database entirely;
    -- this is a bank comparing a message against its own memory, and needs
    -- nobody else's table to do it. The two columns having survived into two
    -- schemas is the check on that: neither could be replaced by a join.
    admission_ref      TEXT NOT NULL,
    -- The allocation this bank issues its customers' addresses under: a country
    -- and a bank code, from that country's registry.
    --
    -- IT IS A SECOND IDENTIFIER AND NOT A RESTATEMENT OF THE ROW'S KEY. The id
    -- above is a BIC, which is what a MESSAGE is addressed to; this is what an
    -- ACCOUNT's address carries. Neither computes the other — AURODEFFXXX and
    -- 99900001 have no arithmetic between them — which is exactly why a scheme
    -- has to publish a directory instead of letting every bank derive one.
    --
    -- Two columns and not one, because a bank code is unique only within a
    -- country: 99991 is Banca Verde in Italy and Crédit Soleil in France, and
    -- they are different banks. Anything keyed by an allocation is keyed by the
    -- pair.
    --
    -- No CHECK on either, for the reason accounts.asset gives above: the rule is
    -- the country structure table in Go, which knows widths, character classes
    -- and two national check-digit algorithms, and none of that is expressible
    -- here. What SQLite could state — "three characters" — would be Sweden's rule
    -- written in the one place least able to change when a fifth country
    -- arrives.
    --
    -- Empty on a bank that has not been allocated one, which is a real state and
    -- not a broken row: a bank exists before any registry has heard of it. Such a
    -- bank can open no customer accounts at all, because every account is opened
    -- with an address minted under this pair (deposit.ErrNoIssuer).
    country            TEXT NOT NULL DEFAULT '',
    bank_code          TEXT NOT NULL DEFAULT '',
    created_at         TEXT,
    seq                INTEGER NOT NULL
) STRICT;

CREATE TABLE bank_assets (
    -- A bank's internal plumbing accounts, one set per asset it operates in.
    --
    -- These are a child row rather than a column apiece on the bank because each
    -- of those accounts is denominated in exactly one asset: a bank clearing
    -- both a euro and a dollar scheme needs two suspense accounts and two
    -- reserve accounts, not two currencies inside one. Keying by (bank, asset)
    -- makes adding a scheme in a new asset a data change rather than a schema
    -- change.
    --
    -- It also makes adding a KIND of account cheap, which has already happened:
    -- returns_receivable joined this row when the return path needed somewhere
    -- to book a clawback a biller's closed account could not fund. One column
    -- here is one account per asset, automatically; the same account hung off
    -- banks would have needed one column per asset the bank could ever operate
    -- in.
    --
    -- These rows are written when the bank is FOUNDED, and the set is never
    -- extended afterwards. That is the reason the asset registry which used to
    -- sit beside this table is gone: an asset the bank was not founded in has no
    -- row here at all, so registering one afterwards produced customer accounts
    -- that could never settle. What a bank operates in is these rows; what an
    -- asset *is* is code.
    --
    -- Only one column arrives later. Every account named here but settlement is
    -- created in the bank's own book by the act that writes the row; settlement
    -- is another institution's account, and it is filled in when the scheme
    -- answers the bank's application. So a row with an empty settlement is a
    -- founded bank rather than a broken one — and an acknowledgement naming an
    -- asset with no row here is passed over rather than allowed to create one,
    -- which is what keeps "the set is never extended" true from the admission
    -- side as well as this one.
    bank_id            TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    -- One row per asset this bank operates in, holding every plumbing account
    -- that asset needs — the ones in this bank's own book, plus its settlement
    -- account in the central bank's. Deliberately not counted here: the set has
    -- already grown once (returns_receivable), and a count in a comment goes
    -- stale while a description of what the row is for does not. Unconstrained,
    -- for the reason given on accounts.asset.
    asset              TEXT NOT NULL,
    suspense           TEXT NOT NULL,
    reserve            TEXT NOT NULL,
    -- Where a credit goes when the payee's account will not take it. A
    -- liability, because the bank still owes the money — to whoever eventually
    -- claims it — exactly as it owes a deposit.
    unclaimed          TEXT NOT NULL,
    -- The GL account for a claim on a biller: opened when this bank is forced to
    -- honour a direct-debit refund it cannot fund out of the biller's own
    -- account. It is an ASSET, and the contrast with unclaimed, immediately
    -- above it, is the point, not an accident. Unclaimed is money this bank OWES
    -- to a payee it has not identified — a customer it cannot name. This is
    -- money OWED TO this bank by a biller it has identified perfectly well,
    -- whose account simply could not cover the clawback the payer's eight-week
    -- right made unconditional. Same kind of event — a credit reversed after the
    -- bank already paid out — landing on opposite sides of the balance sheet
    -- depending on whether the bank knows who owes it money or owes money to
    -- someone unknown. Booking it as a liability, like unclaimed, would still
    -- balance and would say the exact opposite of what happened.
    returns_receivable TEXT NOT NULL,
    -- The cash this bank is holding, and the only account in this row that is
    -- nobody else's promise. reserve is a claim on the central bank, suspense is
    -- money owed to a counterparty's customer, unclaimed is owed to somebody
    -- unidentified and returns_receivable is owed by a biller. This is money.
    --
    -- It is where cash paid in over the counter lands, and it is the second
    -- column this table has gained — which is the point the row's own comment
    -- makes about why adding a KIND of account is cheap here.
    --
    -- Why it exists is a claim about what a bank IS rather than about storage. A
    -- deposit used to debit reserve and post the matching credit in the central
    -- bank's own ledger, so paying cash in was modelled as placing it on reserve;
    -- that made a bank with no settlement account unable to take money at all,
    -- which is false about banking, and it made a member bank write in another
    -- institution's book — a crossing this schema can no longer express at all,
    -- because the central bank's ledger is not in this database and there is
    -- nothing here to post the other leg to. Task 18a closed it while one store
    -- was still underneath, which is why that step went first: the recorder
    -- could still measure it. Cash in now debits this and
    -- credits the customer, in one book. Moving it onward is a LODGEMENT — a
    -- camt.050 to the central bank and its camt.025 back — which is two postings
    -- in two databases with a message between them.
    --
    -- So a non-zero balance here is not a way-station: it is how much of what
    -- this bank's customers paid in has not been placed on reserve, and a bank
    -- that never lodges cannot settle with it.
    vault_cash         TEXT NOT NULL,
    -- This bank's reserve account at the central bank: an account in ANOTHER
    -- institution's book, which is why it is the one column in this table naming
    -- an id this table's owner did not allocate. Every other account here was
    -- created in the bank's own book and numbered by it.
    -- It is legitimate as the account holder's record of its own account
    -- number — a customer knows their IBAN without holding the bank's ledger —
    -- and it is not the record a settlement agent reads: that one is
    -- settlement_members, in the central bank's own database, which nothing in
    -- this shape can reach — so the two copies are no longer a duplication one
    -- reader could resolve by picking the other, which is what they were while
    -- both tables sat in one file. What quotes this one is a
    -- LODGEMENT, which is the account holder asking for a credit to its own
    -- account and is the honest reader to leave behind. Taking a DEPOSIT used to
    -- quote it too and no longer does — cash in reaches vault_cash and no
    -- institution but this one — so the readers left here are the two acts that
    -- genuinely address the central bank: asking it for a reserve credit, and
    -- checking that an arriving statement is about the account this bank holds.
    settlement         TEXT NOT NULL,
    seq                INTEGER NOT NULL,
    PRIMARY KEY (bank_id, asset)
) STRICT;

CREATE TABLE routing_directory (
    -- THIS BANK'S OWN COPY of the scheme's published routing directory: which
    -- institution answers for which bank code, in which country. It is what
    -- turns an IBAN into an agent, and it is the only table in this schema whose
    -- rows are about other institutions.
    --
    -- IT IS A COPY AND THAT IS THE DESIGN. The original is the clearing house's
    -- roster, in the clearing house's database, which nothing here can open. A
    -- member SUBSCRIBES: it asks for a snapshot, replaces this table wholesale
    -- with what it was given, and routes from what it holds. That is what every
    -- real routing directory is — the EPC's Register of Participants is a file a
    -- bank downloads, not a service it queries per payment — and it is why SEPA
    -- can be IBAN-only without any bank being able to read another's register.
    --
    -- WHICH MAKES STALENESS REAL, and it is a behaviour rather than a defect. A
    -- bank admitted this morning cannot be paid by a member that refreshed
    -- yesterday: the payer's bank finds no row, refuses with
    -- payment.ErrBankCodeUnknown, and a refresh makes the same payment work. The
    -- refusal is safe only because AN ALLOCATION IS NEVER REASSIGNED — see
    -- bank_codes in centralbank/0001_init.sql — so a copy that is behind is
    -- INCOMPLETE and never WRONG. The failure mode is "I cannot route this yet"
    -- and never "I routed it to the wrong bank", and nothing in this system may
    -- introduce a path that gives a code back to be issued again.
    --
    -- Nothing checks this table against the roster it came from, because
    -- disagreeing with it is legal. payment/recon REPORTS the difference — "this
    -- bank's directory is two entries behind" — and passes, and a reconciliation
    -- that FAILED on one would assert the opposite of the design.
    --
    -- KEYED BY (country, bank_code), for the reason every table keyed by an
    -- allocation is: a code is unique within one country and nowhere else, so
    -- 99999 names Banca Verde in Italy and Crédit Soleil in France and this copy
    -- holds both. The pair is also the whole of the uniqueness rule here, which
    -- is why there is no second unique index; see
    -- TestExactlyOneUniqueIndexPerShapeThatHasABook.
    country      TEXT NOT NULL,
    bank_code    TEXT NOT NULL,
    -- The institution to send to, and the only thing this row says about it.
    --
    -- THERE IS NO NAME HERE, and the absence is domain content rather than a
    -- column somebody forgot. The roster has none because an acmt.010 delivers
    -- none, so a copy of the roster can have none either. The consequence lands
    -- exactly where a payer feels it: a send form that resolves an IBAN can show
    -- AURODEFFXXX and cannot show "Aurora Bank". Confirming the payee's NAME is a
    -- different question with a different message pair behind it, and this scheme
    -- does not ask it.
    --
    -- NO ASSETS EITHER, and that one is a refusal deliberately not made. This
    -- copy could carry which currencies a member clears in and refuse a payment
    -- early — and it would then refuse, from data that may be behind, a payment
    -- the clearing house would have accepted. The asset check has an owner that
    -- reads the live roster: payment's bothBanksAreMembersTx, at the clearing
    -- house. A stale copy may fail to route; it may not decide membership.
    bic          TEXT NOT NULL,
    -- When the snapshot this row came from was taken. Per row rather than per
    -- table because there is no table to hang it on, and every row of one refresh
    -- carries the same instant — a snapshot is one act, not a merge of many.
    --
    -- It is the whole of the staleness story a console can show: "14 banks,
    -- refreshed 3 days ago" teaches the subscription model in one line, and the
    -- payment that will not route teaches the rest of it.
    refreshed_at TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    -- The order the snapshot arrived in, which is the roster's own publication
    -- order — oldest member first. A refresh deletes every row and writes the
    -- whole list again, so seq restarts from the top of the table each time and
    -- carries no history; the ordering rule ledgers.seq states is about a table
    -- rows are APPENDED to, and this is a table rows are REPLACED in.
    PRIMARY KEY (country, bank_code)
) STRICT;

CREATE TABLE mandates (
    -- A MANDATE IS THE CREDITOR'S BANK'S ROW, and it is in this file and in no
    -- other for that reason. In SEPA the creditor holds the mandate: the biller
    -- collects it, the biller's bank vets the biller and carries the refund risk
    -- for eight weeks, and payment.SDD.ValidateMandate says so. The debtor's
    -- bank is RECORDED here, by BIC, and holds nothing.
    --
    -- It was a network-level resource until Task 18b — POST /mandates on the
    -- clearing house's port, a row keyed by id in a shared store, and
    -- CreateMandateTx reading both parties' registers to check the two accounts
    -- agreed on an asset. That read is what could not survive: the debtor's
    -- account is in another bank's database. The check did not disappear, it
    -- MOVED — a mismatched mandate is now created and refused at its first
    -- collection, by the debtor's bank, which is the only institution that can
    -- see both sides. See asset below, and CreateMandateTx.
    --
    -- debtor/creditor _identifier_scheme and _identifier_value are the PartyRef's
    -- payment.PartyRef.Identifier: the external address (an IBAN today) that was
    -- quoted for that side, STORED rather than looked up from
    -- deposit_account_identifiers at read time, because identifiers are mutable
    -- and a settled mandate or payment must keep saying what it actually used. A
    -- blank pair (both "") means the party was addressed only by its ids, the
    -- same "no CHECK, unvalidated format" stance deposit_account_identifiers
    -- takes on scheme and value, for the same reason: the known schemes are Go
    -- constants, not a database enum.
    id                         TEXT PRIMARY KEY,
    -- The BIC of the DEBTOR's bank: who to collect from. It is the whole of what
    -- this row records about the other side, and it is recorded rather than
    -- resolved, because that bank's register is in that bank's database.
    --
    -- It was debtor_participant, a bank id, and under Task 18 an id and a BIC are
    -- the same value — see banks. The column is renamed rather than merely
    -- retyped because what it holds is an ADDRESS the creditor's bank sends a
    -- collection to, and calling it a participant suggested a party this
    -- institution could look up.
    debtor_agent               TEXT NOT NULL,
    debtor_account             TEXT NOT NULL,
    debtor_identifier_scheme   TEXT NOT NULL,
    debtor_identifier_value    TEXT NOT NULL,
    -- There is no creditor_agent beside it and there was no creditor_participant
    -- either, once this row became the creditor bank's own. A MANDATE IS THE
    -- CREDITOR'S BANK'S ROW, so the creditor's bank is always this one, and a
    -- column holding the same value in every row of every mandate this
    -- institution ever writes records nothing. CreateMandateTx refuses a mandate
    -- whose creditor is not this bank's customer (ErrNotThisBanksMandate), which
    -- is the guard the column looked like it was supporting and never was.
    creditor_account           TEXT NOT NULL,
    creditor_identifier_scheme TEXT NOT NULL,
    creditor_identifier_value  TEXT NOT NULL,
    -- What max_amount is denominated in, and the CREDITOR's account's asset
    -- because this row is the creditor's bank's.
    --
    -- Stored rather than joined, and the join it replaces is the argument. It
    -- used to be read at display time off the DEBTOR's deposit account, which
    -- is a row in another bank's book: no table here can reach it once each
    -- institution keeps its own database, and even within one bank a later read
    -- reports today's asset for an authorisation granted under a different one.
    -- Same stance as payments.debtor_name and the four identifier columns above
    -- — a row records what it was granted with.
    --
    -- No CHECK and no foreign key to a currency table, for accounts.asset's
    -- reason: the known assets are Go constants in ledger, not a database enum,
    -- and a column that admitted only today's list would have to be migrated to
    -- add one.
    --
    -- It is NOT compared against the debtor's account's asset, here or anywhere.
    -- That account is at another bank, so the comparison has no reader; a
    -- mismatched mandate is refused at its first collection, by the debtor's
    -- bank, which is the only institution that can see both. See
    -- payment.CreateMandateTx.
    asset                      TEXT NOT NULL DEFAULT '',
    max_amount                 INTEGER NOT NULL,
    status                     INTEGER NOT NULL,
    created_at                 TEXT,
    seq                        INTEGER NOT NULL
) STRICT;

CREATE TABLE payments (
    -- THIS BANK's row about a payment it is a party to, and one payment is now
    -- three of these in three databases.
    --
    -- It was one row until Task 18. The payer's bank, the clearing house and the
    -- payee's bank all read and wrote the same row, and a status was a single
    -- value they took turns setting — which is why mesh/doc.go could say a
    -- receiving bank "reads the payment row and trusts it over the message it
    -- just received" and mean something faintly alarming by it. Now the row a
    -- bank reads is its OWN, so what it trusts is its own prior belief, and the
    -- three copies are ALLOWED TO DISAGREE. That is not a defect to be
    -- reconciled away: settlement is final at the central bank and the
    -- participants catch up afterwards, so "the clearing house has cleared this
    -- and the payee's bank has not heard yet" is a state the system must be able
    -- to be in, and here it is two rows with two statuses.
    --
    -- csm/0001_init.sql has the other shape of this table. Compare the two
    -- rather than assuming they match; three columns differ and each difference
    -- is a claim:
    --
    --   * cycle_id is NOT here. A BANK HAS NO CYCLES. It is not that the column
    --     would be empty — nothing in mesh/bank.go names a cycle at all, and the
    --     cycles table is in another institution's database. What a bank learns
    --     about a cut-off is a settlement_advices row quoting a reference it
    --     cannot resolve, and that table says so in as many words.
    --   * debtor_leg_tx, creditor_leg_tx, creditor_leg_account,
    --     return_clawback_tx and return_refund_tx are here and NOT at the
    --     clearing house, which posts nothing and holds no book of accounts.
    --   * The two agent columns mean something narrower here; see debtor_agent.
    --
    -- TWO COLUMNS ARE GONE FROM BOTH SHAPES: debtor_participant and
    -- creditor_participant. They named each party's bank as a ParticipantID
    -- beside an agent column naming the same bank as a BIC, and Task 18 made
    -- those one value — see banks above. Two columns that cannot differ are not
    -- two facts. The agent columns are the survivors because they are what goes
    -- on the wire as DbtrAgt/CdtrAgt, and payment.PartyRef loses its Participant
    -- field with them.
    --
    -- One consequence is worth naming rather than discovering: a WRONG
    -- counterparty agent and a wrong counterparty are no longer distinguishable,
    -- because there is one value to be wrong. That does not weaken anything —
    -- the payment is still delivered to the bank the instruction names, which
    -- resolves the address in its own register and answers AC01 — but
    -- mesh/books_test.go's TestAWrongCounterpartyAgentIsRefusedByTheBankItNames
    -- is about a disagreement between the two, and there is no longer one to
    -- construct.
    --
    -- The primary key is the payment id alone and not (book_id, id), unlike
    -- every ledger table above. A payment id is minted by the bank that submits
    -- the payment and travels on the wire; the receiving bank stores the id it
    -- was SENT rather than one of its own, which is what lets a pacs.002 name a
    -- payment both banks recognise. So the id is already globally meaningful in
    -- a way a chart-of-accounts number is not, and adding the constant book
    -- column would buy nothing. settlement_advices is the counter-example
    -- immediately below and explains itself.
    id                         TEXT PRIMARY KEY,
    scheme                     TEXT NOT NULL,
    debtor_account             TEXT NOT NULL,
    debtor_identifier_scheme   TEXT NOT NULL,
    debtor_identifier_value    TEXT NOT NULL,
    creditor_account           TEXT NOT NULL,
    creditor_identifier_scheme TEXT NOT NULL,
    creditor_identifier_value  TEXT NOT NULL,
    -- See creditor_agent, which carries the rule for both. One of the two is
    -- this bank's OWN and is taken off its own row; the other is the
    -- counterparty's and is taken off the INSTRUCTION. Which is which depends on
    -- the scheme's direction — the submitting bank is the payer's on a push and
    -- the payee's on a pull — so neither column can be described on its own.
    debtor_agent               TEXT NOT NULL DEFAULT '',
    -- The account holder name as QUOTED ON THE INSTRUCTION, not as held in the
    -- register. It is stored rather than looked up because BUILDING A PAYMENT
    -- looks it up nowhere: no bank reads the counterparty bank's deposit
    -- register to fill this column in. A real payer's bank knows the payee's
    -- name because the payer typed it onto the instruction. This used to grant an
    -- exception — GET /directory resolved an address ACROSS THE NETWORK and read
    -- the resolved account's name, though its answer never reached the payment —
    -- and Task 18a removed it: that lookup answers out of one bank's own register,
    -- so no reader in this system spans two banks' registers and there is no
    -- cross-bank name that could reach this column even in principle. The two NAME
    -- columns are therefore not a cache: there
    -- is nothing to fall back to, and a NULL here would be an unsendable
    -- payment.
    debtor_name                TEXT NOT NULL DEFAULT '',
    -- The BIC of the bank holding this party's account. The rule for both agent
    -- columns is here; debtor_agent points at it rather than restating it.
    --
    -- THE SUBMITTING BANK'S OWN SIDE IS DERIVED AND THE COUNTERPARTY'S IS
    -- ASSERTED, and the asymmetry is the whole of Task 14. This bank is the
    -- authority on its own customer, so its own agent comes off its own banks
    -- row at submission and whatever the request supplied is discarded — a payer
    -- does not get to rename their own bank on an instruction. The
    -- COUNTERPARTY's agent is what the payer typed, validated for FORMAT and for
    -- nothing else (payment.SubmitPaymentTx, ErrCounterpartyAgentNotNamed), and
    -- it is not checked against anything, because there is nothing in this
    -- database to check it against: the counterparty's bank row is in the
    -- counterparty's own store.
    --
    -- This paragraph said the opposite until Task 18 — that BOTH columns were
    -- derived from the banks row for the party this payment names — and that was
    -- true when banks held every member. It stopped being true at Task 14, when
    -- the counterparty's details started travelling on the request, and the
    -- sentence outlived the code by four tasks. What makes it unrepeatable now
    -- is that the sweep it described cannot be written: ListBanks over other
    -- members does not exist in this shape.
    --
    -- What the element DOES is why the own side is not asserted either: it goes
    -- out as CdtrAgt/DbtrAgt and the clearing house ROUTES on it without reading
    -- anything, so a payer allowed to assert it would be a payer choosing which
    -- bank received the payment. Real SEPA is the same shape — IBAN-only since
    -- 2016, the originating bank derives the routing. A WRONG counterparty agent
    -- is therefore not refused at submission: the message is delivered to the
    -- bank it names, which resolves the address in its own register, does not
    -- find it and answers AC01. See mesh/books_test.go's
    -- TestAWrongCounterpartyAgentIsRefusedByTheBankItNames.
    --
    -- Stored rather than joined even for the own side, and the reason is what
    -- the row is: a record of the message that WAS SENT, not a view onto the
    -- banks table as it stands now. PutBank is an upsert (see store/storetest),
    -- so the day a BIC can be corrected is the day a join would silently rewrite
    -- the address on every payment already settled. There is no foreign key for
    -- the same reason — and now also because for one of the two columns the row
    -- it would reference is in another database.
    creditor_agent             TEXT NOT NULL DEFAULT '',
    creditor_name              TEXT NOT NULL DEFAULT '',
    amount                     INTEGER NOT NULL,
    mandate_id                 TEXT NOT NULL,
    end_to_end_id              TEXT NOT NULL,
    status                     INTEGER NOT NULL,
    reject_reason              TEXT NOT NULL,
    -- The external status-reason code (AC01, AM04, MD01, …) a rejection carries
    -- in a pacs.002, alongside the free text in reject_reason. Both, not either:
    -- the code is what makes the rejection machine-actionable and the text is
    -- what says the part no code can. DEFAULT '' rather than NULL because a
    -- payment that was never rejected has no code, and an absent code and an
    -- empty one are the same fact here.
    reject_code                TEXT NOT NULL DEFAULT '',
    booking_date               TEXT,
    value_date                 TEXT,
    description                TEXT NOT NULL,
    metadata                   TEXT CHECK (json_valid(metadata)),
    created_at                 TEXT,
    debtor_leg_tx              TEXT NOT NULL,
    creditor_leg_tx            TEXT NOT NULL,
    -- The account in the CREDITOR BANK's book that the creditor leg actually
    -- credited: normally the payee's own GL account, and that bank's
    -- unclaimed-balances account when the payee's account would not take the
    -- credit. Written by payment.PostCreditorLegTx, empty until that leg posts.
    -- It is STORED rather than derived because it records a MOMENT that no later
    -- reading recovers. A return has to claw the money back from where it
    -- landed, and the only other way to find out is to re-ask whether the
    -- payee's account is creditable — which answers a question about now, not
    -- about the cut-off. A payee open at settlement and closed afterwards is
    -- indistinguishable from one closed at settlement, so a return that
    -- re-derived would debit the wrong account in exactly the case this column
    -- exists for, and nothing would catch it: an overdrawn deposit is a
    -- Liability going negative, which the ledger does not refuse. That was
    -- measured — the payee's closed account at minus the amount, the unclaimed
    -- liability never released, and the reserves paid back out anyway. No
    -- foreign key to any account table, for the same reason the two agent
    -- columns have none: this row records what WAS done, not a view onto the
    -- chart of accounts as it stands now. DEFAULT '' rather than NULL because a
    -- payment whose creditor leg has not been posted has no such account, and an
    -- absent one and an empty one are the same fact here.
    creditor_leg_account       TEXT NOT NULL DEFAULT '',
    -- The transaction that took the money back at the CREDITOR's bank. Written
    -- by payment.PostReturnLegTx, empty until that bank posts its leg. It does
    -- NOT say which account moved, and must not be read as though
    -- creditor_leg_account answered that: the debit lands on that account in the
    -- ordinary case, and on the bank's own returns-receivable account when the
    -- account creditor_leg_account names has CLOSED since — a posting into a
    -- closed account strands, so the bank funds the refund itself and books a
    -- claim on the biller instead. Which of the two it was is in the
    -- transaction's own entries and nowhere else.
    -- A non-empty value says that bank ATTEMPTED this leg. It does NOT say the
    -- leg still stands, and reading it as though it did is a return that repays
    -- nobody. When the settlement agent refuses a return, the bank that had
    -- already posted unwinds by REVERSING its transaction: the id stays here and
    -- the transaction is marked reversed, because this column records what the
    -- bank did and the ledger records whether it holds. It is deliberately not
    -- cleared — the retry's idempotency key is derived from it, which is what
    -- lets a bank post the leg again without colliding with the attempt it is
    -- replacing, and without a column or a counter to say which attempt this is.
    -- So anything deciding whether this bank still owes the posting must read
    -- the transaction's status. See payment.ReverseReturnLegTx and
    -- payment.PostReturnLegTx.
    return_clawback_tx         TEXT NOT NULL DEFAULT '',
    -- The transaction that gave the money back at the DEBTOR's bank, into the
    -- payer's account or — when that account will no longer take a credit — into
    -- that bank's unclaimed balances, which is the same reading caveat the
    -- column above carries. Written by payment.PostReturnLegTx, empty until that
    -- bank posts its leg — and, like the column above and for the same reasons,
    -- a non-empty value says an attempt was MADE and not that it still stands. A
    -- refused return leaves a reversed transaction with its id still written
    -- here.
    -- EXACTLY ONE OF THE TWO IS EVER FILLED IN THIS DATABASE, and which one
    -- depends on the scheme's direction: the returning bank posts the clawback
    -- and the other bank posts the refund, and the returning bank is the payee's
    -- on a push and the payer's on a pull. So a bank's row carries the leg IT
    -- posted and says nothing about the other.
    --
    -- That is what Task 18 changed here, and the previous version of this
    -- paragraph predicted the change without being able to answer it. The pair
    -- used to sit in ONE row both banks read, and it decided which leg was the
    -- SECOND: the leg that found the other side's id already written was the one
    -- arriving last, and it was the one that took the payment to Returned. That
    -- reading is gone, because the other side's id is in another bank's database
    -- and no statement here can reach it.
    --
    -- What replaces it is that THERE IS NO SECOND LEG TO DETECT. Each bank takes
    -- ITS OWN row to Returned when it posts its own leg, because its own row is
    -- the only one it can move, and the three copies of this payment are allowed
    -- to disagree while a return is in flight exactly as they are while a
    -- settlement is — see this table's own comment. The state the marker existed
    -- to produce, one row reaching Returned once both legs were down, is the
    -- clearing house's to hold on its copy: it is the institution that told both
    -- banks, and the only one with a reason to know that both were told.
    --
    -- A non-empty value still says this bank ATTEMPTED its leg and still does
    -- NOT say the leg stands. When the settlement agent refuses a return, the
    -- bank that had already posted unwinds by REVERSING its transaction and the
    -- id stays here, because this column records what the bank did and the
    -- ledger records whether it holds — so anything deciding whether the posting
    -- is still owed reads the transaction's status, and the retry's idempotency
    -- key is derived from the id, which is what lets the leg be posted again
    -- without a counter to say which attempt this is. Both are DEFAULT ''
    -- rather than NULL, for creditor_leg_account's reason: a leg that has not
    -- been posted has no transaction, and an absent id and an empty one are the
    -- same fact.
    --
    -- Still no foreign key to transactions, and the reason has had to change.
    -- It used to be that the two ids were in DIFFERENT BOOKS, so a constraint
    -- across them could not be written at all. The id this bank fills in now IS
    -- a transaction in this bank's own book in this same database, so the
    -- constraint has become writable — and it stays out for the reason the agent
    -- columns give, which never depended on the other one: this row records what
    -- WAS done, not a view onto the ledger as it stands now.
    return_refund_tx           TEXT NOT NULL DEFAULT '',
    seq                        INTEGER NOT NULL
) STRICT;

CREATE INDEX payments_end_to_end_idx ON payments (
    -- Index 6: GetPaymentByEndToEndID. Deliberately NOT unique.
    --
    -- Refusing a duplicate client reference is payment.Network's job, in
    -- SubmitPaymentTx. A UNIQUE index here would answer the same request with a
    -- constraint violation instead of ErrDuplicateEndToEndID, and a caller
    -- handling the sentinel does not handle that. It would also be the SECOND
    -- unique index in this schema, which transactions_idempotency_key_idx
    -- explains is the one thing the store's duplicate-key mapping cannot
    -- survive.
    --
    -- That leaves the application check carrying the rule, which is sound only
    -- because it is ATOMIC, and it was not always. SubmitPaymentTx read this
    -- index and THEN allocated the payment id; measured on store/pg under READ
    -- COMMITTED, two concurrent submissions of one reference both read nothing
    -- and both wrote, and eight concurrent ones were accepted eight times where
    -- store/mem accepted one — the payer debited eight times for a single client
    -- reference. The order is the other way round now: NextID runs first and
    -- writes id_sequences, and the second submission waits there until the first
    -- commits and its payment row is visible to the read.
    --
    -- THE COUNTER FOLLOWS THE ROW, which is the ruling Task 18 needed before it
    -- could delete ledger.NetworkBook. The id this ordering rests on used to be
    -- allocated under that book — a counter every institution shared — and the
    -- ordering was only ever worth anything because the counter row and the row
    -- being decided from were in one database. They still are, and that is not
    -- luck: a submission is one bank's act, it allocates from ITS OWN book's
    -- counter in id_sequences below, and it reads this index in the same
    -- database and the same transaction. Had the allocation stayed anywhere
    -- else, nothing would span the two — two databases is two transactions, and
    -- no retry can make one of them see the other.
    --
    -- What that ordering is worth HERE is measured, and it is not what it was on
    -- store/pg. With the two statements swapped back, the duplicate-reference
    -- race passes ten runs out of ten, on the ephemeral store and on a WAL file:
    -- SQLite admits one writer, so the loser is refused at its first write and
    -- Store.Update re-runs the unit of work against the winner's committed row.
    -- The ordering is a second guard rather than the only one, and no test in
    -- this repository can now see it go.
    end_to_end_id
)
    WHERE end_to_end_id <> '';

CREATE TABLE settlement_advices (
    -- settlement_advices is a MEMBER BANK's record of a reserve movement it was
    -- told about: a cut-off's net settlement, or a single return.
    --
    -- IT USED TO BE THE ONLY PAYMENT-LAYER TABLE KEYED BY BOOK, and that is
    -- worth recording because the distinction it was the sole example of has
    -- gone. Every other payment-layer table — banks, payments, mandates, cycles,
    -- settlements — was network-scoped: keyed by id alone and sequenced under
    -- ledger.NetworkBook, belonging to no single institution because one
    -- database held them all. This one was keyed by book, and it was keyed by
    -- book because it was one member's own record, and sub-project 8 was
    -- entirely about that difference. There is nothing left for the difference
    -- to distinguish. Cycles and settlements are in other databases; banks holds
    -- one row; payments holds this bank's own copy. The whole file is the
    -- member's now, and this table stopped being the exception by everything
    -- else becoming it.
    --
    -- So the book column here is not what it was either. It is the same constant
    -- every other book_id in this file is (see books), and it stays in the
    -- primary key because the key is (reference, asset) and widening it costs
    -- nothing, not because it distinguishes this table from its neighbours.
    --
    -- Two banks advised of one movement write two rows independently. That is
    -- not redundancy: settlement is final at the central bank and participants
    -- catch up afterwards, so "this bank has booked it and that one has not" is a
    -- state the system must be able to be in — and it shows as one row present
    -- and the other ABSENT.
    --
    -- What a row MEANS is that this bank booked this movement. No committed row
    -- says status 0 (payment.AdviceAdvised) today: payment.PostSettlementAdviceTx
    -- writes the row and posts the mirror leg in ONE unit of work, so a failed
    -- posting rolls the row back with it and a successful one leaves status 1.
    -- That is deliberate — the leg and the record of it must be atomic — and
    -- this comment used to claim the opposite, that a row stuck at status 0 was
    -- the unreconciled position and the only trace of a posting that failed. It
    -- never was. The unreconciled position is the ABSENCE of a row against a
    -- clearing suspense that has not returned to zero, and what reports it is
    -- payment.Network.Reconcile, one bank over its own database — as a POSITION
    -- with an age on it and never as a break, because the mirror leg that will
    -- discharge that suspense is netted and names no payment, so nothing in here
    -- can prove an old suspense wrong.
    --
    -- closing_balance is what the central bank said the reserve stands at, and
    -- payment.Network.ReconcileTx is what reads it: each advised movement against
    -- the RUNNING balance of the leg booked from it. That catches a mirror leg
    -- posted at the wrong figure, a reserve moved with no statement behind it,
    -- and a statement missed before one that did arrive. What it cannot catch is
    -- a statement that never arrived at all, because a closing balance only ever
    -- arrives on a statement the bank holds. It is stored because it arrives, and
    -- a balance discarded on receipt is one nobody can go back for.
    --
    -- reference is the account servicer's own reference for the entry that moved
    -- this bank's reserve, and it arrives by two routes: a cycle id when a
    -- cut-off settled (payment.SettleCycleTx), a payment id when a single settled
    -- payment was returned (payment.SettleReturnTx). Both are statements of the
    -- same kind about the same account, which is why one column takes both.
    --
    -- There is no `kind` column beside it to say which, and that is a decision
    -- rather than an omission. Ids do not collide, so nothing is ambiguous; a
    -- member can resolve NEITHER kind — a cycle is the clearing house's row in
    -- the clearing house's database, and a payment id here is the id of a
    -- payment SETTLED AND RETURNED, which this bank does hold a copy of but
    -- learns nothing from that the advice has not already told it — so knowing
    -- which it is buys the member nothing it could act on; and the
    -- reconciliation that reads these rows
    -- asks one question, "did this bank book what it was told", which is one
    -- shape and not two. A discriminator nothing branches on is a field that can
    -- only drift out of step with the id beside it.
    --
    -- The consequence worth stating for a reader who arrived from the cut-off
    -- path: a RETURN leaves an unreconciled position exactly as a cut-off does.
    -- The settlement agent is final when it commits the reserve reversal, each
    -- bank is then told and books its own mirror leg, and until it has, that
    -- bank's clearing suspense has not returned to zero and there is no row here
    -- against the payment id.
    --
    -- No foreign key on reference, and the reason has become a fact rather than
    -- a policy. A MEMBER BANK HAS NO CYCLES: the cycles table is not in this
    -- database, so a constraint naming it could not be written. When the
    -- reference is a payment id there IS a payments row here it could name, and
    -- it still stays out — the advice is a statement the central bank made about
    -- this bank's reserve account, and it has to be recordable whether or not
    -- this bank holds a copy of the payment it mentions. Constraining it would
    -- make a member's ability to file what it was told depend on what else it
    -- happens to know, which is the sharing this sub-project removes, arriving
    -- by the back door.
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    reference       TEXT NOT NULL,
    asset           TEXT NOT NULL,
    -- SIGNED: positive means this bank's reserve went up. The statement it came
    -- from carries a magnitude and a CdtDbtInd, because the ISO 20022 money type
    -- cannot be negative; the sign is reconstructed on the way in and stored,
    -- because a mirror leg posted in the wrong direction is the most expensive
    -- way to be wrong about a settlement.
    movement        INTEGER NOT NULL,
    closing_balance INTEGER NOT NULL,
    -- INTEGER, like every other iota enum in this schema, because
    -- payment.AdviceStatus is an int enum and a store keeps those as their
    -- ordinal. status 0 is payment.AdviceAdvised, 1 is payment.AdvicePosted.
    status          INTEGER NOT NULL,
    mirror_tx       TEXT NOT NULL DEFAULT '',
    advised_at      TEXT,
    posted_at       TEXT,
    seq             INTEGER NOT NULL,
    PRIMARY KEY (book_id, reference, asset)
) STRICT;

-- ---------------------------------------------------------------------------
-- The audit log
-- ---------------------------------------------------------------------------

CREATE TABLE audit_events (
    -- THE AUDIT LOG IS PER INSTITUTION, and this table is in all three shapes
    -- for that reason rather than because the DDL was convenient to repeat.
    -- What a bank's log holds is what this bank did and was told; the clearing
    -- house's holds its clearing decisions and the central bank's its
    -- settlements, in their own databases. There is no cross-entity view and
    -- there never was one to lose: GET /audit has been bound per listener since
    -- the api gained one port per institution, so a bank's port reads this
    -- table, the central bank's reads its own and the clearing house's reads
    -- /payments/audit against its own. The split made the endpoints honest
    -- rather than changing them.
    --
    -- seq is a total order over the WHOLE store, not per book and not per scope,
    -- which is what makes AuditFilter.Before a global cursor that every other
    -- predicate is applied alongside. "The whole store" is now one
    -- institution's, so the cursor no longer interleaves three institutions'
    -- events and a page of it is one actor's history rather than a slice
    -- through everybody's.
    --
    -- It has no foreign key to books: an audit event must be appendable whatever
    -- else is or is not in the database, and a log that can be blocked by a
    -- constraint is not a log.
    --
    -- This is the one seq in the schema that IS its table's primary key, so
    -- SQLite would let it be AUTOINCREMENT. It is not, and the reason is that a
    -- second allocation rule is a second thing to get wrong: every other seq
    -- here is MAX(seq)+1 in the writing transaction (see ledgers.seq), an
    -- automatic counter is not rolled back with the caller's transaction, and a
    -- log that skipped numbers on a failed append would make the cursor's gaps
    -- mean two different things.
    seq         INTEGER PRIMARY KEY,
    id          TEXT NOT NULL,
    book_id     TEXT NOT NULL,
    scope       TEXT NOT NULL,
    type        TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    payload     TEXT CHECK (json_valid(payload)),
    metadata    TEXT CHECK (json_valid(metadata)),
    actor       TEXT NOT NULL,
    occurred_at TEXT
) STRICT;

CREATE INDEX audit_events_book_scope_idx ON audit_events (
    -- Indexes 8 and 9: the two shapes every audit query has. Book+scope is what
    -- a Book's own log and the participant endpoints ask for; entity, in the
    -- index below, is what a "history of this account" view asks for. Both carry
    -- seq so the cursor and the ordering come out of the index.
    book_id, scope, seq
);
CREATE INDEX audit_events_entity_idx ON audit_events (
    -- Index 9; see audit_events_book_scope_idx.
    entity_id, seq
);

-- ---------------------------------------------------------------------------
-- Identity allocation
-- ---------------------------------------------------------------------------

CREATE TABLE id_sequences (
    -- id_sequences holds every counter the domain draws from, as ordinary rows,
    -- so that allocation rolls back with the caller's transaction and the
    -- counters stay gap-free. A database sequence would be wrong here: sequences
    -- are outside transactions on purpose, so a failed posting would burn a
    -- transaction number. ledgers.seq applies the same rule to this schema's own
    -- insertion counters.
    --
    -- `name` is the counter, not the ID prefix: NextID keeps ONE counter per
    -- book shared by every prefix, so ldg_1, evt_2 and tx_3 interleave and the
    -- number doubles as a creation order.
    --
    -- THE COUNTER FOLLOWS THE ROW. This bank's ids are allocated here, from this
    -- bank's own book, in the same database and the same transaction as whatever
    -- they are about — and that is the ruling Task 18 had to make before it
    -- could delete ledger.NetworkBook, which every read-then-write ordering in
    -- this system used to allocate from. An act allocates from the store it is
    -- about to write. Where an act could not satisfy that it was a crossing and
    -- had to become a message, which is this sub-project's thesis rather than an
    -- exception to it: the four orderings that mattered — a payment id before
    -- the duplicate-reference check, and the three admission acts before their
    -- find-or-creates — each turned out to belong to exactly one institution, so
    -- each kept its counter and its read in one database and none of them had to
    -- be replaced.
    --
    -- So this table is still the serialization point the other comments in this
    -- schema point at. A caller that writes it before it reads is ordered
    -- against every other caller that does — within THIS institution, which is
    -- as far as ordering has ever actually reached, and now as far as it claims
    -- to. See payments_end_to_end_idx, and ledgers in
    -- centralbank/0001_init.sql.
    --
    -- One consequence is worth stating because a reader will otherwise find it
    -- surprising: ids REPEAT across institutions. Every bank's first ledger is
    -- ldg_1, because each counts from one in a database of its own. Nothing
    -- compares an id across a boundary — what crosses is a BIC and a payment id
    -- the submitting bank minted — so the collisions are unobservable, and
    -- making them impossible would mean a counter somebody shared, which is
    -- exactly what was deleted.
    book_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    next_value INTEGER NOT NULL,
    PRIMARY KEY (book_id, name)
) STRICT;
