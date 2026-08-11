-- centralbank/0001_init: the settlement agent's whole world, in one migration.
--
-- Thirteen tables. Six of them are a general ledger and are also in
-- bank/0001_init.sql, because a central bank keeps a book of accounts like any
-- other bank does; the other seven are what makes it a central bank.
--
-- What is absent is again the substance. THE CENTRAL BANK HAS NO CUSTOMERS: no
-- deposit register, no deposit account identifiers, no holds, no snapshots, no
-- overdraft terms, no product catalogue, no lending, and no slot_accounts —
-- that table says which account a customer-facing FLOW posts to, and every
-- posting here is the settlement agent's own act against an account it names
-- itself. Its account holders are
-- member banks and its accounts are their reserves, so a table of retail
-- deposit accounts would be a table it could never write a row into. AND IT
-- HOLDS NO PAYMENTS: there is no payments table and no cycles table here. It
-- settles from the instruction it is sent — payment.SettleCycleTx takes the net
-- positions off the message, and SettleReturnTx reverses a single settled
-- payment's reserves reading no payment row at all — and what it keeps
-- afterwards is a settlements row saying what it discharged.
--
-- That last point is the one a reader is most likely to expect otherwise, so it
-- is worth saying plainly: the settlement agent never holds the batch. It is
-- told a set of positions, it checks that every net payer can cover its own,
-- and it posts the whole thing or none of it. The batch is the clearing house's
-- and the payments in it are the banks'.
--
-- WHERE A COMMENT HAS TO GO
--
-- SQLite keeps the text of a statement in sqlite_master.sql, so a comment
-- INSIDE a statement's parentheses reaches a schema dump and a comment ABOVE
-- one is dropped silently. Every argument about something a schema does NOT do
-- therefore lives inside the statement it concerns, because an absent
-- constraint has no column to hang a comment from. This header is narrative
-- belonging to no statement and does not reach a dump; if you are adding a
-- reason, put it inside the parentheses.
--
-- WHICH OF THE THREE FILES AN ARGUMENT GOES IN
--
-- The six ledger tables here have the same DDL as the bank's and mostly not the
-- same comments. bank/0001_init.sql is the canonical home for the arguments
-- that span both — the seq allocation rule, the absent CHECK on an asset code,
-- the absent parent foreign key, and half of the absent UNIQUE on a ledger
-- name — because most of the columns each is about are there. This file names
-- them at the point they apply rather than restating them; copying is how one
-- fact ends up in nine places and then in three versions.
--
-- One argument goes the other way and it is the largest single one in the
-- original schema: THE FIND-OR-CREATE RACE BEHIND THE CHART OF ACCOUNTS IS
-- THIS INSTITUTION'S, so it is written on ledgers here and bank's copy names
-- it. That is the rule working as intended — an argument goes where the thing
-- it is about lives, not where the table happens to appear first.
--
-- CONVENTIONS
--
-- As in the other two files: amounts are INTEGER minor units, times are TEXT
-- (RFC3339, UTC, nine fractional digits, always), iota enums are INTEGER,
-- free-form maps are TEXT with a json_valid CHECK, listed tables carry a
-- monotonic seq allocated MAX(seq)+1 in the writing transaction, and every
-- table is STRICT. bank/0001_init.sql's header states each of those in full.

CREATE TABLE books (
    -- books exists so that book_id is a real key rather than a convention. Rows
    -- are created on demand by the first write that names a book (see
    -- tx.ensureBook), because a book is not an entity the domain creates — a
    -- BookID is a name a participant is written under.
    --
    -- IT HOLDS EXACTLY ONE ROW, the central bank's own, and the store is opened
    -- for that one BookID and refuses every other with
    -- sqlite.ErrNotThisStoresBook. books in bank/0001_init.sql carries the
    -- argument for why the column survives as a constant rather than being
    -- dropped.
    --
    -- There is one thing to add that is only true here. A process in which every
    -- payment.Network holds a ledger.Book over payment.CentralBankBook makes this
    -- book reachable from all of them, and the only thing then keeping a member
    -- bank's handler out is that bankOps in cmd/server names no method reaching it.
    -- Only this institution's network holds the book, and only this institution's
    -- DATABASE holds the rows — which is what makes the refusal a fact rather
    -- than a field being nil.
    id TEXT PRIMARY KEY
) STRICT;

-- ---------------------------------------------------------------------------
-- The general ledger
-- ---------------------------------------------------------------------------

CREATE TABLE ledgers (
    -- A note on what is NOT here: UNIQUE (book_id, name), and THIS is the shape
    -- the constraint keeps getting proposed for.
    --
    -- Half the argument is not this institution's and is written where it
    -- belongs: a name is not an identity in this domain, names are allowed to
    -- repeat, and a constraint asserting otherwise would refuse a write the
    -- domain has already decided to allow. See ledgers in bank/0001_init.sql.
    --
    -- The half that IS this institution's is the race, and it is the reason a
    -- unique constraint looks tempting at all. This chart of accounts is
    -- resolved by find-or-create BY NAME — payment.centralBankChartTx looks up
    -- "Central Bank", "Member Reserves" and the rest rather than caching an id,
    -- because a cached id does not survive a Store.Reset and is wrong the moment
    -- a second process opens the same database. Two concurrent admissions could
    -- each observe "no Central Bank ledger" and each create one, after which two
    -- members' reserves would sit in two subledgers and be invisible to each
    -- other's settlement.
    --
    -- What closes it is an ID ALLOCATION TAKEN BEFORE THE READ.
    -- payment.admissionSequenceTx writes id_sequences, and writing is what makes
    -- a transaction the database's writer, so a second concurrent caller waits
    -- there until the first commits and then sees the ledger the first created.
    -- The counter serializes the whole operation, not just the number it hands
    -- out.
    --
    -- THE COUNTER FOLLOWS THE ROW, and this table is why that rule has to hold
    -- for a store split to be possible at all. The ordering works here because
    -- opening a settlement account is the settlement agent's OWN act: it
    -- allocates from id_sequences in this database and reads this table in the
    -- same database and the same transaction. A counter every institution shared
    -- would put the allocation somewhere else, and nothing would span the two —
    -- two databases is two transactions, no retry can make one of them see the
    -- other, and the guard would have to become a message.
    --
    -- What that ordering is worth on SQLite is measured, and it is less than the
    -- rule above implies. With
    -- payment.admissionSequenceTx made to return nil, the concurrent-admissions
    -- race passes ten runs out of ten, on the ephemeral store and on a WAL file
    -- alike: SQLite admits one writer, so a loser is refused at its first write
    -- and Store.Update re-runs the unit of work, reading again after the
    -- winner's commit and finding the ledger. The ordering is a second guard
    -- rather than the only one, and no test in this repository can see it go.
    -- It stays because it costs nothing.
    --
    -- THERE IS NO CONSTRAINT BEHIND THE FIND-OR-CREATE, so any new caller of it
    -- that could reach it FIRST must draw an id from this database before it
    -- does, and a reader who does not know that will not learn it from this
    -- table. It was measured on the caller that did not:
    -- payment.OpenSettlementAccountTx, an institution's own unit of work reaching
    -- the find-or-create with no id drawn, built the central bank a second chart
    -- of accounts in 60 runs out of 60.
    --
    -- A MEMBER BANK cannot reach it at all, and that is the store split rather
    -- than a guard. Resolving a settlement-assets account here on a DEPOSIT would
    -- have a bank taking cash over the counter reach into this institution's
    -- chart of accounts; cash paid in becomes vault cash in the bank's own book,
    -- moving it onward is a lodgement, and a lodgement is a message. What is left
    -- of the exception is that it could not be written — a bank's store has no
    -- ledgers row of this institution's to find.
    --
    -- Also not here: a CHECK on the text columns. See ledgers in
    -- bank/0001_init.sql, which carries that one for both.
    book_id    TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id         TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TEXT,
    -- The row's monotonic insertion sequence, allocated MAX(seq)+1 inside the
    -- writing transaction. ledgers.seq in bank/0001_init.sql is the canonical
    -- statement of that rule and of why it is not AUTOINCREMENT; it applies here
    -- unchanged.
    seq        INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE TABLE subledgers (
    -- subledgers.ledger_id carries NO foreign key, and neither does
    -- accounts.subledger_id or entries.account_id. That is a rule about what
    -- this layer is for: the store is a per-table key/value layer, "the parent
    -- must exist" is a domain rule, and ledger.Book enforces it by reading the
    -- parent before it writes the child. Put the same rule in the schema as well
    -- and it is enforced twice, in two places that answer differently — the
    -- constraint fires first and it fires as a foreign-key violation where the
    -- domain would have said ErrLedgerNotFound.
    --
    -- subledgers in bank/0001_init.sql carries the argument in full, including
    -- the exemption that keeps the CHILD-table foreign keys: this shape has two
    -- of those, entries -> transactions below and settlement_positions ->
    -- settlements, and both are aggregates the store writes whole.
    --
    -- The settlement agent's chart of accounts is the one this matters least
    -- for and the one it was measured on: two subledgers under one ledger,
    -- Member Reserves and the balancing settlement assets, both created by
    -- find-or-create. See ledgers.
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
    -- Every account in THIS book is a member's reserve account or the balancing
    -- settlement-assets account for one asset, which is why a member clearing in
    -- two currencies holds two reserve accounts here and not one account with
    -- two balances.
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
    -- The asset this account is denominated in, fixed at creation and carrying
    -- no CHECK restricting it to the known codes. accounts.asset in
    -- bank/0001_init.sql is the canonical statement of why no asset column in
    -- any of the three schemas carries one: the set lives in Go, a CHECK would
    -- put it in two places with the database's copy deciding, and adding an
    -- asset would become a migration. settlement_member_accounts.asset below
    -- points at the same paragraph.
    asset        TEXT NOT NULL,
    -- Whether this account pools subsidiaries. accounts.control in
    -- bank/0001_init.sql carries the argument and the two posting refusals it
    -- turns on.
    --
    -- No account in THIS book is one, and that is the settlement agent's shape
    -- rather than a gap: it holds a reserve account per member per asset and
    -- has no customers at all, so there is no subsidiary for a line here to stand
    -- for. The column exists because the store is one implementation over three
    -- schemas and an account read back here must be the account that was
    -- written.
    control      INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT,
    seq          INTEGER NOT NULL,
    PRIMARY KEY (book_id, id)
) STRICT;

CREATE TABLE transactions (
    -- WHAT DAY IT IS IS NOT IN THIS DATABASE EITHER, and that matters most here.
    -- transactions in bank/0001_init.sql carries the argument; what is worth
    -- adding is that this is the institution a reader would expect to hold it.
    -- The settlement agent decides which days money moves on, so a business_date
    -- row here would look like the calendar's natural home — and it would make
    -- the date a fact this institution could change under the others. It is the
    -- DEPLOYMENT's, in a file beside these databases, and the calendar that says
    -- whether a date settles is a function rather than a table anybody owns.
    book_id         TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    id              TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    booking_date    TEXT,
    value_date      TEXT,
    status          INTEGER NOT NULL,
    description     TEXT NOT NULL,
    -- JSON, and the CHECK is the one constraint in this schema that no layer
    -- above the store duplicates: nothing in the domain asks whether a metadata
    -- document parses, so this is where an unparseable one is refused, and it is
    -- refused nowhere else. It admits NULL, which is a nil map — an empty one is
    -- '{}' and the two are kept apart because the API renders them differently.
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
        -- It is the ONLY unique index in THIS schema that is not a primary
        -- key's, and the store's error mapping rests on that. SQLite names no
        -- index in a constraint error, so a duplicate is identified by the
        -- extended code alone: SQLITE_CONSTRAINT_UNIQUE (2067) for a unique
        -- index and SQLITE_CONSTRAINT_PRIMARYKEY (1555) for a primary key. So
        -- 2067 means this index and nothing else, and store/sqlite maps it to
        -- ledger.ErrDuplicateIdempotencyKey.
        --
        -- The claim is per SHAPE and the test is three, because the mapping is
        -- one function over three schemas — see the same index in
        -- bank/0001_init.sql, which carries the argument, and the clearing
        -- house's schema, which holds no unique index at all.
        --
        -- What it protects here is worth naming: the settlement transaction. A
        -- cut-off is discharged whole or not at all, and a redelivered
        -- instruction must not discharge it twice. The key is derived from the
        -- cycle, so the second delivery collides here rather than posting a
        -- second netting.
        book_id, idempotency_key
    )
    WHERE idempotency_key <> '';

CREATE TABLE entries (
    -- entries.position is explicit because Transaction.Entries is an ordered
    -- slice and a table has no order. entries.account_id carries no foreign key,
    -- for the reason subledgers.ledger_id does not.
    --
    -- entries.value_date is the one date that is NOT a copy: the two legs of one
    -- event can legitimately take economic effect on different days. entries in
    -- bank/0001_init.sql carries that argument, where the interest computed from
    -- the column also lives; nothing in this book accrues interest, and the
    -- column is kept because the store is one implementation over three schemas
    -- and a transaction read back here must be the transaction that was written.
    --
    -- This table carries no `seq`: its order is the position column, which is
    -- data rather than an insertion artefact.
    book_id        TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    position       INTEGER NOT NULL,
    id             TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    -- What a leg belongs to within a control account.
    -- entries.subsidiary_id in bank/0001_init.sql carries the argument, and the
    -- point of it — the control figure being this column dropped from a WHERE
    -- clause rather than a stored total.
    --
    -- Every row here holds '', because no account in this book is a control
    -- account: see accounts.control above for why the settlement agent has no
    -- subsidiary to name. The column exists for that comment's reason.
    subsidiary_id  TEXT NOT NULL DEFAULT '',
    amount         INTEGER NOT NULL,
    direction      INTEGER NOT NULL,
    value_date     TEXT,
    PRIMARY KEY (book_id, transaction_id, position),
    FOREIGN KEY (book_id, transaction_id) REFERENCES transactions (book_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX entries_account_idx ON entries (
    -- The value_date suffix serves the value-dated balance and the per-day
    -- movement series. The (book_id, account_id) prefix is what BookBalance
    -- uses, and a reserve balance is the read this institution makes most.
    --
    -- subsidiary_id sits between the two so that one index serves all three
    -- shapes; the same index in bank/0001_init.sql says why that position and
    -- not the end.
    book_id, account_id, subsidiary_id, value_date
);

-- ---------------------------------------------------------------------------
-- The bank-code register
-- ---------------------------------------------------------------------------

CREATE TABLE bank_codes (
    -- bank_codes is the national registry's own book: which institution holds
    -- which bank code, in which country. It is the thing an IBAN's middle
    -- digits are a reference INTO, and the reason a payment can be addressed by
    -- account number alone.
    --
    -- THIS INSTITUTION IS FOUR REGISTRIES WEARING ONE HAT, and the fudge is
    -- worth naming here because the table is where it is visible. In the world
    -- a bank code is a NATIONAL allocation — the Bundesbank runs the
    -- Bankleitzahl file, the Banca d'Italia the ABI — and SEPA spans those
    -- countries precisely BECAUSE no one institution owns the mapping, which is
    -- why the real directory is a purchased, federated data product rather than
    -- a table anybody can ask for. One settlement agent standing in for four
    -- registries is what makes this system small enough to read.
    --
    -- NO BANK EVER READS IT. That is the whole point of the split between this
    -- table and the routing directory a bank holds: this is the ISSUER's record
    -- of what it gave out, and what a bank routes by is a COPY of the clearing
    -- house's published roster, pulled and possibly stale. The two answer
    -- different questions — who issued this address, and may this address be
    -- reached — and there is no method on any store that lets one institution
    -- ask the other's. payment/recon holds them against each other, and it is a
    -- harness rather than an actor for exactly that reason.
    --
    -- KEYED BY (country, code), and the composite is the substance rather than a
    -- convenience. A bank code is unique within ONE country and nowhere else:
    -- 99999 is an ABI in Italy and a code banque in France, and this system
    -- allocates both. A key on the code alone would refuse the second of those
    -- as a duplicate, which would be the store inventing a collision the domain
    -- does not have; a key on the BIC would allow it and lose the refusal that
    -- matters.
    --
    -- THIS KEY IS THE REFUSAL. Two banks on one code is the defect the whole
    -- routing directory would be built on top of — every address issued under
    -- the shared code would resolve to whichever bank the reader's copy named
    -- last — and it is refused here, by the issuer, at the moment of allocation.
    -- The clearing house refuses it a second time on its roster, which is
    -- belt-and-braces and earns its place: the roster is what every member
    -- copies, and the clearing house cannot see this table to check.
    country      TEXT NOT NULL,
    code         TEXT NOT NULL,
    -- The institution the allocation is to, and there is no UNIQUE on
    -- (country, bic) beside the key above. An allocation is never REASSIGNED —
    -- the invariant every subscriber's stale copy rests on, because a copy that
    -- is behind can then only be incomplete and never wrong — but "one code per
    -- bank per country" is a rule about the ACT and not about the row, and the
    -- act holds it: payment's OpenSettlementAccountTx reads this bank's
    -- allocation before it mints one, and a bank asking for a second asset gets
    -- the code it already has. That read-then-write is ordered by the id drawn
    -- before it from id_sequences in THIS database, the same way every other act
    -- in this file is ordered; see ledgers, and payment's admissionSequenceTx.
    --
    -- A second UNIQUE index would also be unaffordable rather than merely
    -- redundant: the store identifies a unique conflict by SQLite's extended
    -- error code alone, which is as targeted as an index name only while one
    -- unique index exists per database. See TestExactlyOneUniqueIndexPerShapeThatHasABook.
    bic          TEXT NOT NULL,
    -- No CHECK on the country, and none on the code's width or characters. Which
    -- countries exist, how wide each one's code is and which characters it
    -- admits are the iban package's country table — four countries today, and a
    -- fifth is one Go value — and a CHECK would state the same rule a second
    -- time in the place least able to change. It is the argument
    -- accounts.asset makes in bank/0001_init.sql, and it is stronger here: the
    -- rule is a table of segment widths and two national check-digit
    -- algorithms, and none of that is expressible in SQLite at all.
    allocated_at TEXT,
    seq          INTEGER NOT NULL,
    PRIMARY KEY (country, code)
) STRICT;

-- ---------------------------------------------------------------------------
-- The settlement register
-- ---------------------------------------------------------------------------

CREATE TABLE settlement_members (
    -- settlement_members is the CENTRAL BANK's own record of a member, keyed by
    -- the only identifier it is ever told. It is the second of the three rows
    -- one admission writes; banks in bank/0001_init.sql carries the argument for
    -- the split, and this is the only one of the three in this database.
    --
    -- Not keyed by a bank id: the settlement agent holds no roster and allocates
    -- no ids, and what an account-opening request carries is a BIC. A bank id
    -- here would be a foreign key into another database — which is now literally
    -- true rather than a prediction — and a value this institution could not
    -- have obtained even when everything shared one.
    --
    -- What it holds is a name to address a statement to and one account per
    -- asset, and nothing about how the member runs — not its book, not its
    -- subledgers, not its product. A central bank knows which account it holds
    -- for whom. It does not know what its members do with the money, and now it
    -- could not find out.
    bic       TEXT PRIMARY KEY,
    name      TEXT NOT NULL,
    opened_at TEXT,
    seq       INTEGER NOT NULL
) STRICT;

CREATE TABLE settlement_member_accounts (
    -- One settlement account per asset, in the central bank's own book.
    --
    -- A child table because an account is denominated in exactly one asset, so a
    -- member clearing in two currencies holds two accounts rather than one
    -- account with two balances. The account ids here are the central bank's
    -- own, allocated in its own book and present in the accounts table above —
    -- the difference from bank_assets.settlement in bank/0001_init.sql, which is
    -- the same account remembered by the customer, in the customer's database.
    -- Both records exist and neither is derivable from the other any more.
    --
    -- There is deliberately no foreign key from account to accounts (book_id,
    -- id), even though the parent is in this same database and the constraint
    -- would be writable. It is the rule subledgers states: "the parent must
    -- exist" is enforced by the act that creates both, and a constraint here
    -- would answer a foreign-key violation where the domain answers its own
    -- refusal.
    --
    -- The parent FOREIGN KEY on bic stays, under the exemption stated on
    -- subledgers: the store writes both sides of this within one statement
    -- sequence, so no caller can produce an orphan.
    bic     TEXT NOT NULL REFERENCES settlement_members (bic) ON DELETE CASCADE,
    -- Unconstrained, for the reason accounts.asset gives in bank/0001_init.sql.
    asset   TEXT NOT NULL,
    account TEXT NOT NULL,
    seq     INTEGER NOT NULL,
    PRIMARY KEY (bic, asset)
) STRICT;

CREATE TABLE settlements (
    -- What this institution discharged, and it is the record of an act rather
    -- than of a batch it held. The positions came in on an instruction, the
    -- reserve movements were posted in one transaction here, and this row says
    -- so afterwards.
    id            TEXT PRIMARY KEY,
    -- The clearing house's cut-off this settlement discharged, as an id and
    -- nothing more. THERE IS NO CYCLES TABLE IN THIS DATABASE and there is no
    -- foreign key that could be written. That is the point rather than a
    -- limitation: the settlement agent is told a set of positions and a
    -- reference, it checks the payers can cover them, and it is final when it
    -- commits — it does not, and must not, need to read the batch to do any of
    -- that.
    cycle_id      TEXT NOT NULL,
    -- What was settled IN, and it is this institution's own answer rather than
    -- a lookup. There is no CHECK against a known-assets table for the reason
    -- every other asset column in this schema has none, and no join to the
    -- cycle's scheme is possible: the scheme lives with the cycle, in the
    -- clearing house's database, and this one has neither table.
    --
    -- It is a column rather than a derivation because the derivation was a
    -- CROSSING. api's settlementAsset read settlement -> cycle -> scheme, which
    -- is one institution asking about another's database and answers "no such
    -- table" here. The agent never needed the trip: an instruction whose legs
    -- are not all in one asset is refused by payment.positionsIn, so the batch
    -- this row records has exactly one and this is it.
    asset         TEXT NOT NULL,
    -- The single netting transaction in this book that moved every reserve. One
    -- transaction and not one per member, because a settlement window is
    -- whole-or-nothing: a partial discharge is the state this design exists to
    -- make impossible. No foreign key to transactions, for the reason
    -- settlement_member_accounts.account has none.
    settlement_tx TEXT NOT NULL,
    value_date    TEXT,
    settled_at    TEXT,
    seq           INTEGER NOT NULL
) STRICT;

CREATE TABLE settlement_positions (
    -- What each member owed or was owed at this cut-off, one row apiece.
    --
    -- THIS IS THE CENTRAL BANK'S ROW AND A BANK HAS NO COPY OF IT. The question
    -- looks open — this table is a child of settlements, plainly this
    -- institution's, and a bank plausibly wants a settlement-position row per
    -- cycle — and what settles it is asking what reads one: every reader is this
    -- institution's or the operator console's, and nothing in cmd/server/bank.go names
    -- a position at all. What a bank holds is a settlement_advices row, its own
    -- record of the movement it was told about.
    --
    -- The member is named by BIC, which is what settlement_members is keyed on
    -- and the only identifier this institution is ever told. A bank id would be
    -- allocated somewhere else: it arrives on the instruction, computed by the
    -- clearing house from payments this institution never saw, and nothing here
    -- could resolve it, because turning a bank id into an address is a read of
    -- the bank's own row. A bank's id IS its BIC — see banks in
    -- bank/0001_init.sql — so the value that arrives and the key it has to line
    -- up with are the same thing.
    --
    -- There is still NO foreign key to settlement_members, and now that is a
    -- decision rather than an impossibility, which is worth the distinction. It
    -- could be written: the parent is in this database and the columns finally
    -- match. It stays out because a settlement must be recordable for the
    -- positions it was instructed with, and a member the instruction names that
    -- this institution holds no account for is a BREAK — something for the
    -- reconciliation to surface, not something to make the whole cut-off
    -- unrecordable at the last statement. The sufficiency check that decides
    -- whether a payer can cover its position is where that is refused, and it is
    -- in the domain, where it can say which member and why.
    --
    -- The parent FOREIGN KEY on settlement_id stays, under the exemption stated
    -- on subledgers in bank/0001_init.sql: the store writes both sides of this
    -- within one statement sequence, so no caller can produce an orphan.
    settlement_id TEXT NOT NULL REFERENCES settlements (id) ON DELETE CASCADE,
    bic           TEXT NOT NULL,
    amount        INTEGER NOT NULL,
    PRIMARY KEY (settlement_id, bic)
) STRICT;

-- ---------------------------------------------------------------------------
-- The audit log
-- ---------------------------------------------------------------------------

CREATE TABLE audit_events (
    -- THE AUDIT LOG IS PER INSTITUTION; audit_events in bank/0001_init.sql
    -- carries the argument. What this one holds is the settlement agent's own
    -- acts — an account opened, a lodgement credited, a cut-off discharged, a
    -- settled payment's reserves reversed — and GET /audit on this
    -- institution's port is what reads it.
    --
    -- seq is a total order over the WHOLE store, not per book and not per scope,
    -- which is what makes AuditFilter.Before a global cursor that every other
    -- predicate is applied alongside.
    --
    -- It has no foreign key to books: an audit event must be appendable whatever
    -- else is or is not in the database, and a log that can be blocked by a
    -- constraint is not a log.
    --
    -- This is the one seq in the schema that IS its table's primary key, so
    -- SQLite would let it be AUTOINCREMENT. It is not, and the reason is that a
    -- second allocation rule is a second thing to get wrong: every other seq
    -- here is MAX(seq)+1 in the writing transaction (see ledgers.seq in
    -- bank/0001_init.sql, which is the canonical statement), an automatic
    -- counter is not rolled back with the caller's transaction, and a log that
    -- skipped numbers on a failed append would make the cursor's gaps mean two
    -- different things.
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
    -- The two shapes every audit query has. Book+scope is what a Book's own log
    -- asks for; entity, in the index below, is what a "history of this reserve
    -- account" view asks for. Both carry seq so the cursor and the ordering come
    -- out of the index.
    book_id, scope, seq
);

CREATE INDEX audit_events_entity_idx ON audit_events (
    -- See audit_events_book_scope_idx.
    entity_id, seq
);

-- ---------------------------------------------------------------------------
-- Identity allocation
-- ---------------------------------------------------------------------------

CREATE TABLE id_sequences (
    -- id_sequences holds every counter this institution draws from, as ordinary
    -- rows, so that allocation rolls back with the caller's transaction and the
    -- counters stay gap-free. A database sequence would be wrong here: sequences
    -- are outside transactions on purpose, so a failed posting would burn a
    -- transaction number.
    --
    -- `name` is the counter, not the ID prefix: NextID keeps ONE counter per
    -- book shared by every prefix, so ldg_1, evt_2 and tx_3 interleave and the
    -- number doubles as a creation order.
    --
    -- THE COUNTER FOLLOWS THE ROW; id_sequences in bank/0001_init.sql carries
    -- the ruling. This table is the serialization point ledgers above depends
    -- on, and that dependency is the reason the ruling had to be made at all:
    -- opening a settlement account allocates here and then reads ledgers, and
    -- the ordering is worth something only because both are in this database.
    book_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    next_value INTEGER NOT NULL,
    PRIMARY KEY (book_id, name)
) STRICT;
