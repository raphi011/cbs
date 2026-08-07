-- csm/0001_init: the clearing house's whole world, in one migration.
--
-- Seven tables, and the shortest of the three files by a wide margin. What is
-- absent is the substance of it: THE CLEARING HOUSE HAS NO LEDGER. No books, no
-- accounts, no transactions, no entries, no deposit register, no products, no
-- lending. It holds no money and posts nothing, so it has nowhere to post.
--
-- That was true before this file existed and it was invisible. One database
-- held every table, every institution's payment.Network reached all of them,
-- and the only thing asserting that the clearing house stayed out of the books
-- was a test — TestTheCSMTouchesOnlyTheNetworkBook, one measurement in
-- mesh/books_test.go, which would have gone on passing if the assertion had
-- been deleted. Now it is a fact about the schema: a statement naming accounts
-- in this database is not a policy violation, it is a syntax error against a
-- table that does not exist.
--
-- WHAT IT DOES HOLD is a routing table and a batch. roster_entries says who is
-- a member and in which assets, which is what lets it refuse to clear for
-- somebody who is not; cycles and cycle_payments are the batch it accumulates
-- between cut-offs; payments is its own copy of each payment it carries. That
-- is a clearing house: it knows where to send a message and which payments are
-- in the current window.
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
-- payments is also in bank/0001_init.sql, in a different shape, and three of
-- this file's arguments are stated there rather than here — the absent CHECK on
-- an asset code, the child-table foreign-key exemption, and the seq allocation
-- rule. They are named at the point they apply and not restated: copying is how
-- one fact ends up in nine places and then in three versions. bank's file is
-- the canonical home for most of them because most of the columns they are
-- about are there.
--
-- CONVENTIONS
--
-- As in the other two files: amounts are INTEGER minor units, times are TEXT
-- (RFC3339, UTC, nine fractional digits, always), iota enums are INTEGER,
-- free-form maps are TEXT with a json_valid CHECK, listed tables carry a
-- monotonic seq allocated MAX(seq)+1 in the writing transaction, and every
-- table is STRICT. bank/0001_init.sql's header states each of those in full;
-- ledgers.seq there is the canonical statement of the seq rule.

CREATE TABLE roster_entries (
    -- roster_entries is the CLEARING HOUSE's record of a member: where to send a
    -- message addressed to it. It is the third of the three rows one admission
    -- writes; banks in bank/0001_init.sql carries the argument for the split,
    -- and this is now the only one of the three in this database.
    --
    -- It carries NO account identifier of any kind — no account, no subledger,
    -- no product, no book. A clearing house holding one would be holding the
    -- means to reach into a bank's ledger, and under one shared store nothing
    -- but a test would notice it doing so. That is not hypothetical: the row
    -- this table replaced carried the central bank's account ids, and four
    -- readers in three institutions resolved their postings through it.
    --
    -- What has changed since that argument was written is that it is no longer
    -- the argument doing the work. An account id here would name an account in
    -- a database this institution cannot open. The rule and its enforcement have
    -- stopped being the same sentence, which is what the split was for.
    --
    -- Keyed by BIC, and it is the only key this institution has. A clearing
    -- house routes what a message addresses, and a message addresses a BIC.
    --
    -- That used to be a narrower claim than it reads. This table was keyed by BIC
    -- and half the readers reached it by participant id anyway, through a
    -- GetBank on the bank's own row — eight sites in mesh, each turning an id
    -- into an address by reading a database it did not hold. Task 18 made the id
    -- BE the address (see banks in bank/0001_init.sql), so there is no
    -- conversion left to perform and no table here that could perform one.
    --
    -- This key is also the refusal that keeps two banks off one address, which
    -- banks in bank/0001_init.sql defers to: a second admission on a BIC already
    -- here is answered ErrBICAlreadyAdmitted rather than extending the entry,
    -- unless it quotes the same admission_ref. See that column.
    --
    -- It carries no NAME either, and that is the newer half of the same
    -- principle. The row had one until the message that writes it was read:
    -- AccountRequestAcknowledgementV03 identifies the account owner with an
    -- OrganisationIdentification29 — a BIC, an LEI, generic identifiers — and
    -- has no legal name, country or address anywhere on it. So a name here could
    -- only be filled by the clearing house remembering the application across
    -- the relay, and nothing read it: every reader of this row in mesh takes the
    -- BIC and touches nothing else, and the operator console lists banks from
    -- their own rows. A member's legal name lives where a message delivered
    -- one — on the bank's row, and on settlement_members, which learns it from
    -- the acmt.007's Org/FullLglNm and names the reserve account after it.
    bic           TEXT PRIMARY KEY,
    -- The process id every message of one admission echoes, and the clearing
    -- house's only way to tell two admissions apart. The acknowledgement that
    -- causes this row carries no back-reference to the request, so there is
    -- nothing else to correlate on.
    -- It is what makes a refusal implementable at all: the address is reserved
    -- before anything is sent, so the only requests that can arrive on a BIC
    -- already in this table are the same bank asking for a second asset — one
    -- currency per request, so a two-currency bank really does ask twice — and
    -- an operator re-driving an interrupted admission. A refusal keyed on the
    -- BIC alone would refuse exactly those two and never fire on the impostor it
    -- exists for.
    -- NOT NULL, and never empty in a row any act writes: payment's
    -- AdmitMemberTx refuses an acknowledgement quoting no admission, because ""
    -- compares equal to every other "" and two institutions on one address would
    -- then extend a single entry instead of the second being refused.
    -- mesh.Mesh.Admit mints one process id per admission and every message of it
    -- echoes that value; the seed and the test suites compose no messages and
    -- derive a reference from the BIC instead (see store/storetest.Admit), which
    -- means two of THEIR admissions on one address share one reference and
    -- extend one entry — why a fixture whose banks settle gives each of them an
    -- address of its own. The column stays unconstrained rather than
    -- CHECK-constrained for the reason accounts.asset gives in
    -- bank/0001_init.sql: the rule lives in Go, and a CHECK would state it a
    -- second time, in the place least able to change.
    --
    -- The read-then-write behind this refusal is ordered by an id allocated
    -- before it, from id_sequences in THIS database — see that table, and see
    -- ledgers in centralbank/0001_init.sql for the same shape at the settlement
    -- agent. Admitting a member is the clearing house's own act, so its counter
    -- and the row it decides from are in one database and one transaction. That
    -- is the ruling the whole split turned on and it cost nothing here.
    admission_ref TEXT NOT NULL,
    admitted_at   TEXT,
    seq           INTEGER NOT NULL
) STRICT;

CREATE TABLE roster_entry_assets (
    -- The assets a member clears in, one row each.
    --
    -- It carries no account, which is the whole difference between this child
    -- table and the two the other institutions keep: the clearing house holds no
    -- account for any asset, because it holds no book. It knows which schemes a
    -- member is in so that it can refuse to clear one it is not in, and that is
    -- all it knows.
    --
    -- That refusal is real and it is where the reader is: payment's
    -- bothBanksAreMembersTx, from AcceptAtCSMTx, will not take a payment into a
    -- cycle unless both banks' entries name the scheme's asset. What had no
    -- reader until then was the VALUE and not the rows: these are loaded with
    -- the parent on every read of a roster entry, single or list, because an
    -- entry handed back without its assets would not be the entry that was
    -- written — and nothing outside the store's own shared suite then ASKED
    -- what they said. That is the shape that deleted the name column from the
    -- parent table, and it is recorded here because a value carried faithfully
    -- and consulted by nobody is not visible in a schema dump either.
    --
    -- Unconstrained asset code, for the reason accounts.asset gives in
    -- bank/0001_init.sql. That argument is about columns in all three schemas
    -- and is written once.
    --
    -- Keyed by POSITION and not by asset, which is the same decision
    -- cycle_payments made for ClearingCycle.PaymentIDs and is made here for the
    -- same two reasons. RosterEntry.Assets is an ordered slice, so the position
    -- is data rather than a surrogate; and a slice can repeat a value, so a key
    -- on (bic, asset) would REFUSE a row the Go type can hold. This table had
    -- that key, and while there were two stores it showed up as one refusing
    -- what the other stored — which is how it was found. What makes it wrong now
    -- is the same fact without the comparison: a store's contract is with the
    -- TYPE it is handed. storetest's RosterEntryAssetsAreAnOrderedList is what
    -- holds it to that.
    --
    -- What this constraint is NOT about is any writer's behaviour, and the
    -- reason it says so is that it used to. It predicted that Task 17d would
    -- build this list from an acmt.010's unbounded AccountForAction1 and that a
    -- repeat would arrive that way. The writer arrived at Task 17c instead —
    -- payment's AdmitMemberTx — and it cannot produce one from either end: it
    -- takes the assets from a map keyed by asset, and it appends only the ones
    -- the entry does not already hold. A message repeating a currency collapses
    -- in that map before this table is reached, and the reader between the wire
    -- and that writer refuses one outright:
    -- payment.ReadAdmissionAcknowledgement will not read an acknowledgement
    -- naming two accounts in one currency. The key is still position, because
    -- PutRosterEntry must store whatever slice a caller passes whether or not
    -- any caller passes that one.
    --
    -- Whether a repeated asset is a message worth refusing is a question about
    -- the message and belongs to the institution reading it, not to the store.
    --
    -- The order the position preserves is AdmitMemberTx's: the assets of one
    -- acknowledgement sorted, because they come out of a map and Go randomises
    -- map iteration, appended after the ones the member was already admitted
    -- for. So an extension leaves the earlier assets where they were, and this
    -- column is what lets that be true without a schema change.
    --
    -- The parent FOREIGN KEY stays, under the exemption stated on subledgers in
    -- bank/0001_init.sql: the store writes both sides of this within one
    -- statement sequence, so no caller can produce an orphan.
    bic      TEXT NOT NULL REFERENCES roster_entries (bic) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    asset    TEXT NOT NULL,
    PRIMARY KEY (bic, position)
) STRICT;

CREATE TABLE payments (
    -- THE CLEARING HOUSE's row about a payment it is carrying, and one payment
    -- is now three of these in three databases.
    --
    -- bank/0001_init.sql has the other shape of this table and carries the
    -- argument for why there are two. Read it first; what follows is only the
    -- difference, which is that this institution DOES NOT POST AND HAS NO
    -- CYCLE-BLIND COUNTERPARTY.
    --
    -- What is here and not at a bank:
    --
    --   * cycle_id. A cut-off is this institution's whole function. It is
    --     nullable in the sense of being empty until AcceptAtCSMTx takes the
    --     payment into the open cycle, and it is the one column that makes this
    --     row a member of a batch. A BANK HAS NO CYCLES and its copy of this
    --     table has no such column.
    --
    -- What is at a bank and not here:
    --
    --   * debtor_leg_tx, creditor_leg_tx and creditor_leg_account. A clearing
    --     house posts nothing, so there is no transaction of its own to record
    --     and no account for a credit to have landed in. These are not columns
    --     it would leave empty; they name a ledger this database does not have.
    --   * return_clawback_tx and return_refund_tx, for the same reason.
    --
    -- ONE READER HAD TO CHANGE FOR THAT TO BE TRUE, and it is worth naming
    -- rather than leaving to be discovered. mesh's csm.tell decided whether to
    -- send the payer's bank a refund instruction by reading debtor_leg_tx off
    -- this row — "has the payer's bank debited?" answered out of another
    -- institution's column. It cannot be, and it did not need to be: the
    -- clearing house knows the scheme's direction and knows what status it
    -- carried this payment to, which is the same question asked of facts it
    -- owns.
    --
    -- The two agent columns mean something different here than at a bank as
    -- well. This institution derives neither: both arrive on the message it is
    -- relaying, and it ROUTES on them without reading anything else. That is not
    -- a weakness — it is what a clearing house is — and the check that keeps it
    -- honest is roster_entry_assets, which decides whether the payment is
    -- carried at all.
    id                         TEXT PRIMARY KEY,
    scheme                     TEXT NOT NULL,
    debtor_account             TEXT NOT NULL,
    debtor_identifier_scheme   TEXT NOT NULL,
    debtor_identifier_value    TEXT NOT NULL,
    creditor_account           TEXT NOT NULL,
    creditor_identifier_scheme TEXT NOT NULL,
    creditor_identifier_value  TEXT NOT NULL,
    -- Both agent columns are what the message said, stored because this row
    -- records the message that was carried. Unlike at a bank, NEITHER is
    -- derived: see this table's own comment. No foreign key to anything naming
    -- a bank, and here it is not a decision — there is no banks table in this
    -- database and there is not going to be one. The roster is what this
    -- institution knows about members, and it is keyed by BIC, not by the
    -- participant ids in the columns above.
    debtor_agent               TEXT NOT NULL DEFAULT '',
    debtor_name                TEXT NOT NULL DEFAULT '',
    creditor_agent             TEXT NOT NULL DEFAULT '',
    creditor_name              TEXT NOT NULL DEFAULT '',
    amount                     INTEGER NOT NULL,
    -- The mandate a direct debit was collected under, as an id and nothing more.
    -- THE MANDATE ITSELF IS NOT IN THIS DATABASE: it is the creditor bank's row,
    -- for the reason mandates in bank/0001_init.sql gives, and this column is
    -- the id travelling on the message. The clearing house cannot validate it
    -- and does not try — vetting the creditor is the creditor bank's job,
    -- because the creditor bank carries the refund risk.
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
    -- The cut-off this payment is in, empty until AcceptAtCSMTx puts it in one.
    -- There is deliberately NO foreign key to cycles, even though the parent is
    -- in this same database and the constraint would therefore be writable. It
    -- is the same reason the reference column on a bank's settlement_advices
    -- gives: the row must be recordable before it is batched, and a payment
    -- refused before any cycle was open would otherwise be unrecordable. The
    -- membership that IS constrained is cycle_payments, which the store writes
    -- as part of the cycle aggregate.
    cycle_id                   TEXT NOT NULL,
    booking_date               TEXT,
    value_date                 TEXT,
    description                TEXT NOT NULL,
    metadata                   TEXT CHECK (json_valid(metadata)),
    created_at                 TEXT,
    seq                        INTEGER NOT NULL
) STRICT;

CREATE INDEX payments_end_to_end_idx ON payments (
    -- GetPaymentByEndToEndID. Deliberately NOT unique, and here that is a
    -- stronger statement than it is at a bank.
    --
    -- Refusing a duplicate client reference is the SUBMITTING BANK's job, in
    -- payment.SubmitPaymentTx, against its own copy of this table — see
    -- payments_end_to_end_idx in bank/0001_init.sql, which carries the argument
    -- and the ordering that makes it atomic. This institution carries whatever
    -- reference a member sent it. Two members may legitimately use the same
    -- one: a client reference is unique to the client that issued it, not to
    -- the network, and there has never been anything in this system claiming
    -- otherwise. A UNIQUE index here would invent that claim and refuse the
    -- second bank's perfectly good payment.
    --
    -- It is also the case that this schema has NO unique index at all — this
    -- one is not, and cycles_open_idx is not — so SQLITE_CONSTRAINT_UNIQUE (2067)
    -- cannot be raised in this database. store/sqlite maps that code to
    -- ledger.ErrDuplicateIdempotencyKey with no index name to check, which is
    -- safe here by vacuity and safe in the other two shapes because each holds
    -- exactly one. TestExactlyOneUniqueIndex is three tests now and the number
    -- it asserts here is zero. Adding a unique index to this shape would make a
    -- duplicate-key sentinel reachable in an institution that has no
    -- idempotency key to duplicate.
    end_to_end_id
)
    WHERE end_to_end_id <> '';

CREATE TABLE cycles (
    -- A clearing cycle: the batch this institution accumulates between
    -- cut-offs, and the reason it exists. There is no cycles table in either of
    -- the other two schemas. A bank has no cycles and the central bank settles
    -- one it is instructed about rather than holding it.
    id            TEXT PRIMARY KEY,
    scheme        TEXT NOT NULL,
    status        INTEGER NOT NULL,
    -- A JSON column rather than a child table, so that a nil map and an empty
    -- one still round-trip differently: an open cycle carries an empty
    -- NetPositions and a closed one carries the computed positions. NULL is the
    -- nil map, '{}' the empty one, and json_valid is what keeps a third thing
    -- out. Nothing above the store checks that this column parses.
    --
    -- The positions are keyed by BIC, computed by this institution from the
    -- payments above and sent on to the settlement agent, which keys its own copy
    -- in settlement_positions the same way.
    --
    -- They were keyed by ParticipantID and the difference was invisible while
    -- one database held both a roster and a banks table to convert between them.
    -- It is not invisible now: the settlement agent receiving these has
    -- settlement_members keyed by BIC and nothing else, so a position naming a
    -- participant id would have been a set of figures about banks it could not
    -- identify. Task 18 made the id BE the BIC, so the column and the key it has
    -- to line up with are the same value; see banks in bank/0001_init.sql.
    net_positions TEXT CHECK (json_valid(net_positions)),
    opened_at     TEXT,
    closed_at     TEXT,
    -- There is NO settlement_id column, and the absence is the finding rather
    -- than an omission.
    --
    -- There was one, holding "the settlement this cut-off produced, as an id and
    -- nothing more", on the argument that the settlements table is the CENTRAL
    -- BANK's so there is no foreign key to write but the id itself is still worth
    -- keeping. The id is not something this institution can learn. It is the
    -- settlement agent's own row number, allocated inside that agent's own unit
    -- of work in its own database, and what comes back on the wire is a pacs.002
    -- quoting the CYCLE — because the cycle is what this institution asked
    -- about. Nothing in the conversation ever carries the other number, so the
    -- column could only ever have been empty.
    --
    -- What the clearing house does know is that it was told, and that is the
    -- cycle's own status: CycleSettled. The link in the other direction is real
    -- and lives where it can be kept — settlements.cycle_id in the central bank's
    -- schema, which is that agent's own row pointing at what it settled.
    seq           INTEGER NOT NULL
) STRICT;

CREATE INDEX cycles_open_idx ON cycles (
    -- GetOpenCycle. Partial on the open status, which is the only one it ever
    -- asks for. status 0 is payment.CycleOpen.
    scheme
) WHERE status = 0;

CREATE TABLE cycle_payments (
    -- ClearingCycle.PaymentIDs is an ordered slice, so like a transaction's
    -- entries it gets an explicit position column, and for the reason
    -- roster_entry_assets spells out: a slice can repeat a value, so keying on
    -- the value would refuse a row the Go type can hold.
    --
    -- The parent FOREIGN KEY stays, under the exemption stated on subledgers in
    -- bank/0001_init.sql: the store writes both sides of this within one
    -- statement sequence, so no caller can produce an orphan. payment_id carries
    -- none, and that is the same rule from its other side — the payments row is
    -- in this database, so the constraint could be written, and it stays out
    -- because a cycle must be recordable whatever else is in the database. See
    -- payments.cycle_id, which is the mirror of this decision.
    cycle_id   TEXT NOT NULL REFERENCES cycles (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    payment_id TEXT NOT NULL,
    PRIMARY KEY (cycle_id, position)
) STRICT;

-- ---------------------------------------------------------------------------
-- The audit log
-- ---------------------------------------------------------------------------

CREATE TABLE audit_events (
    -- THE AUDIT LOG IS PER INSTITUTION; audit_events in bank/0001_init.sql
    -- carries the argument. What this one holds is the clearing house's own
    -- decisions — a payment accepted into a cycle, a cut-off closed, a member
    -- admitted — and GET /payments/audit on this institution's port is what
    -- reads it.
    --
    -- seq is a total order over the WHOLE store, not per book and not per scope,
    -- which is what makes AuditFilter.Before a global cursor that every other
    -- predicate is applied alongside.
    --
    -- book_id is here, and it is the only book-shaped column in a schema with no
    -- books table. That is not a leftover. The audit filter and the id counter
    -- are written against ledger.BookID in all three shapes, and this
    -- institution answers for exactly one value of it — the clearing house's own
    -- — which store/sqlite is opened with and refuses to see any other value of
    -- (sqlite.ErrNotThisStoresBook). It used to be ledger.NetworkBook, a
    -- sentinel meaning "belongs to no single bank", and there is no network left
    -- for anything to belong to: the rows here belong to this institution, the
    -- way a bank's belong to it.
    --
    -- It has no foreign key to books, which in the other two schemas is a
    -- decision — a log that can be blocked by a constraint is not a log — and
    -- here is also an impossibility.
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
    -- and the participant endpoints ask for; entity, in the index below, is what
    -- a "history of this payment" view asks for. Both carry seq so the cursor
    -- and the ordering come out of the index.
    --
    -- The book column is a constant in this schema (see the table), so the
    -- leading column of this index has one value and does no selecting. It is
    -- kept rather than narrowed to (scope, seq) because the filter the query is
    -- built from still names a book in all three shapes, and an index that
    -- matches the predicate is worth more than one column of selectivity.
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
    -- are outside transactions on purpose, so a failed act would burn a number.
    --
    -- `name` is the counter, not the ID prefix: NextID keeps ONE counter per
    -- book shared by every prefix, so cyc_1, evt_2 and pay_3 interleave and the
    -- number doubles as a creation order.
    --
    -- THE COUNTER FOLLOWS THE ROW; id_sequences in bank/0001_init.sql carries
    -- the ruling and what it cost. The instance of it that matters here is
    -- admitting a member: roster_entries' refusal is a read the clearing house
    -- decides from, it is ordered by an id drawn from this table before the
    -- read, and both are in this database because admitting a member is this
    -- institution's own act. Had the counter stayed anywhere else, nothing would
    -- have spanned the two.
    book_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    next_value INTEGER NOT NULL,
    PRIMARY KEY (book_id, name)
) STRICT;
