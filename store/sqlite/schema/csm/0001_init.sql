-- csm/0001_init: the clearing house's whole world, in one migration.
--
-- Ten tables, and the shortest of the three files by a wide margin. What is
-- absent is the substance of it: THE CLEARING HOUSE HAS NO LEDGER. No books, no
-- accounts, no transactions, no entries, no deposit register, no products, no
-- lending. It holds no money and posts nothing, so it has nowhere to post.
--
-- One database holding every table makes that claim invisible: every
-- institution's payment.Network reaches every book, and the only thing asserting
-- that the clearing house stays out of them is a measurement in
-- cmd/server/books_test.go, which goes on passing if the assertion is deleted. Here it
-- is a fact about the schema — a statement naming accounts in this database is
-- not a policy violation, it is a syntax error against a table that does not
-- exist.
--
-- WHAT IT DOES HOLD is a routing table, a batch, and what it owes. roster_entries
-- says who is a member and in which assets, which is what lets it refuse to
-- clear for somebody who is not; cycles and cycle_payments are the batch it
-- accumulates between cut-offs; payments is its own copy of each payment it
-- carries; held_files and held_returns are the instructions it has taken in and
-- not yet handed over. That is a clearing house: it knows where to send a
-- message, which payments are in the current window, and what it still owes
-- whom.
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
    -- this table replaces carried the central bank's account ids, and four
    -- readers in three institutions resolved their postings through it.
    --
    -- It is not that argument doing the work here, though. An account id in this
    -- table would name an account in a database this institution cannot open, so
    -- the rule and its enforcement are two different things — which is what the
    -- split was for.
    --
    -- NOR IS THE DOWNLOAD QUEUE HERE, and that is the more interesting absence
    -- now. Routing a file to this member means putting it in that member's
    -- queue, so the queue is the routing table and this row is the membership —
    -- two facts that could disagree if either were derived from the other. What
    -- keeps them together is that enrolment is what creates a queue and
    -- provisioning is what enrols, both outside this database.
    --
    -- The queue itself is a list of byte slices in the host's memory, and it is
    -- deliberately not a table. One institution holding bytes addressed to
    -- another is not a crossing while the bytes are OPAQUE to the holder; a
    -- table of them is a store this institution reads, and the first query
    -- anybody writes against it is a clearing house looking inside a file it is
    -- only carrying. A restart empties every queue, which loses whatever had not
    -- been collected — see cycles below, where the same absence costs more.
    --
    -- Keyed by BIC, and it is the only key this institution has. A clearing
    -- house routes what a message addresses, and a message addresses a BIC.
    --
    -- The claim is narrower than it reads unless a bank's ID IS its address,
    -- which it is (see banks in bank/0001_init.sql). Otherwise a reader holding a
    -- participant id reaches this table by converting one first, off the bank's
    -- own row — a read into a database it does not hold. There is no conversion
    -- to perform here and no table that could perform one.
    --
    -- This key is also the refusal that keeps two banks off one address, which
    -- banks in bank/0001_init.sql defers to: a second admission on a BIC already
    -- here is answered ErrBICAlreadyAdmitted rather than extending the entry,
    -- unless it quotes the same admission_ref. See that column.
    --
    -- It carries no NAME either, and that is the same principle one step on.
    -- What writes this row identifies the account owner by BIC and delivers no
    -- legal name, so a name here could only be the clearing house remembering
    -- something nobody told it — and nothing would read it: every reader of this
    -- row takes the BIC and touches nothing else, and the operator
    -- console lists banks from their own rows. A member's legal name lives where
    -- it was actually given: on the bank's row, and on settlement_members, which
    -- is told it by the application and names the reserve account after it.
    bic           TEXT PRIMARY KEY,
    -- The allocation this member issues its customers' addresses under, learned
    -- from the same acknowledgement that writes this row and published to every
    -- member that asks for a copy. It is what makes this table a ROUTING DIRECTORY and
    -- not merely a list of addresses the scheme will talk to.
    --
    -- WHY IT HAS TO BE PUBLISHED AT ALL: a payer quotes an IBAN and nothing
    -- else, the bank code inside it is a national registry's allocation, and no
    -- arithmetic turns one into a BIC. Aurora is AURODEFFXXX and 99999999. So
    -- somebody has to write the pairing down where every member can read it, and
    -- SEPA is IBAN-only because somebody does — not because routing is
    -- computable from an address.
    --
    -- TWO COLUMNS, because a bank code is unique within ONE country: 99999 is an
    -- ABI in Italy and a code banque in France, and this scheme has members in
    -- both. Every table keyed by an allocation is keyed by the pair, here and in
    -- the settlement agent's register and in each member's copy.
    --
    -- There is no name beside them, for the reason this row carries none at all:
    -- the acknowledgement identifies its owner by BIC and delivers no legal
    -- name. So a
    -- member's copy of this table answers a BIC and cannot answer "Banca Verde",
    -- which is where a payer meets the absence.
    --
    -- WHAT IS NOT HERE is a UNIQUE on (country, bank_code), and its absence is
    -- the same division every refusal in this schema keeps. Two members on one
    -- code would make one address ambiguous for the whole scheme, and it IS
    -- refused — by payment's AdmitMemberTx, which reads this table before it
    -- writes and answers ErrBankCodeTaken. The read-then-write is ordered by the
    -- id drawn before it from id_sequences in this database, exactly as the
    -- admission_ref refusal below is. A unique index would state the rule a
    -- second time and answer a constraint violation where the domain answers a
    -- sentinel, and the store could not afford a second one in any case: see
    -- TestExactlyOneUniqueIndexPerShapeThatHasABook.
    --
    -- The clearing house refusing this at all is belt-and-braces: the settlement
    -- agent guarantees uniqueness at allocation, in a register keyed by
    -- (country, code). It earns its place because this row is the one every
    -- member COPIES, and this institution cannot see that register to check.
    country       TEXT NOT NULL,
    bank_code     TEXT NOT NULL,
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
    -- The reference is DERIVED FROM THE BIC (see provision.Ref), which makes
    -- re-provisioning one bank idempotent and makes two admissions on one address
    -- share one reference and extend one entry — which is why a deployment whose
    -- banks settle gives each of them an address of its own. The column stays unconstrained rather than
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
    -- on (bic, asset) would REFUSE a row the Go type can hold, and a store's
    -- contract is with the TYPE it is handed. storetest's
    -- RosterEntryAssetsAreAnOrderedList is what holds it to that.
    --
    -- What this constraint is NOT about is any writer's behaviour. The one writer
    -- there is, payment's AdmitMemberTx, cannot produce a repeat from either end:
    -- it takes the assets from a map keyed by asset, and it appends only the ones
    -- the entry does not already hold. The key is position anyway, because
    -- PutRosterEntry must store whatever slice a caller passes whether or not any
    -- caller passes that one.
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
    -- THE READER A CLEARING HOUSE IS TEMPTED INTO is worth naming rather than
    -- leaving to be discovered: whether to tell the payer's bank to refund could
    -- be decided by reading debtor_leg_tx off this row — "has the payer's bank
    -- debited?" answered out of another institution's column. It cannot be, and
    -- it does not need to be: the clearing house knows the scheme's direction and
    -- knows what status it carried this payment to, which is the same question
    -- asked of facts it owns.
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
    -- Keying them by ParticipantID makes no visible difference while one database
    -- holds both a roster and a banks table to convert between them, and it is a
    -- real one here: the settlement agent receiving these has settlement_members
    -- keyed by BIC and nothing else, so a position naming a participant id is a
    -- set of figures about banks it cannot identify. A bank's id IS its BIC, so
    -- the column and the key it has to line up with are the same value; see banks
    -- in bank/0001_init.sql.
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
    --
    -- THE OUTPUT FILES THIS CYCLE IS HOLDING ARE NOT HERE EITHER, and that one
    -- is a pointer rather than an absence: they are in held_files, two tables
    -- down.
    --
    -- Between a cut-off and its settlement this institution has sorted every
    -- submitted file by receiving agent and is holding each bank's share,
    -- releasing nothing until the cycle is final. There are M shares per cycle
    -- and each is a whole file, so no column on this row could hold them; and
    -- what is held is not the file that will travel, because a payment an
    -- operator rejects out of an open cycle has to be cut out of the share when
    -- the rest of it settles.
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
-- What this institution is holding and has not handed over
-- ---------------------------------------------------------------------------

CREATE TABLE held_files (
    -- ONE RECEIVING BANK's share of an uploaded instruction file, built when the
    -- file is taken in and released when the cut-off carrying it settles. It is
    -- the settle-before-release ruling made into a table: a share exists from
    -- ingestion and reaches nobody until the settlement agent has discharged the
    -- batch.
    --
    -- Nothing in the other two schemas is like it. A bank holds no share of
    -- anybody's file and the settlement agent has no payments at all; sorting one
    -- submitted file into M addressed ones is the act no member could perform for
    -- itself, and this is where the M wait in between.
    --
    -- THE ROW IS AN OBLIGATION, and that is what makes it a table rather than a
    -- map. Reserves move at settlement, and a receiving bank never handed its
    -- instructions holds the money in clearing suspense with no act in this
    -- system able to move it. A process that ended between the two cost exactly
    -- that, for a whole batch at a time.
    --
    -- There is NO foreign key to cycles, even though the parent is in this same
    -- database and the constraint would therefore be writable. It is
    -- payments.cycle_id's reason from the other side: this row is what this
    -- institution owes a bank, and it must be readable and removable whatever
    -- became of the batch row that names it.
    cycle_id    TEXT NOT NULL,
    -- Build order, and shares are released in it. Allocated MAX(seq)+1 over the
    -- whole table by the canonical rule (ledgers.seq in bank/0001_init.sql), so
    -- it orders every share this institution holds rather than one cycle's. The
    -- cycle leads the key because every read is one cut-off's.
    seq         INTEGER NOT NULL,
    -- The receiving bank's BIC, which is also its subscriber id: enrolment is
    -- what creates a queue, so a destination with no enrolment has nowhere for
    -- this share to go and the transactions were refused RC01 before a row was
    -- ever written. No foreign key to roster_entries — a member admitted while a
    -- share of its own is standing here would be constrained by nothing, and one
    -- that LEAVES the roster is still owed the instructions behind reserves that
    -- have already moved.
    destination TEXT NOT NULL,
    -- The SUBMITTING BANK's file, exactly as it arrived, and not the file that
    -- will travel.
    --
    -- Two things follow from that. Which transactions travel is not settled until
    -- the cut-off is, so what is kept has to be cuttable — a rendered output file
    -- could not have a rejected payment taken back out of it. And a bare document
    -- is not something the codec renders: iso20022.Marshal takes an ENVELOPE, so
    -- what is stored is the whole uploaded file, header and all. That header is
    -- the submitter's and is dropped at release, which builds this institution's
    -- own; what is read back out of these bytes is the document.
    --
    -- A file addressed to three banks is stored three times, once per share. The
    -- share is the unit that is built, cut and released, and normalising the
    -- bytes out from under it would save space in a table whose rows live for the
    -- length of one cut-off.
    --
    -- No msg_def_idr column beside it. The definition is on the stored envelope's
    -- header AND on the document it wraps, and iso20022 refuses a file where
    -- those two disagree, so a third copy could only ever drift from them.
    file        BLOB NOT NULL,
    PRIMARY KEY (cycle_id, seq)
) STRICT;

CREATE TABLE held_file_transactions (
    -- WHICH transactions of the file above are this share's, as POSITIONS in it
    -- rather than as copies, and which payment each position is.
    --
    -- Positions because two things are indexed by them — the document's own
    -- transaction list and this institution's payments rows — and an index is
    -- what keeps the two in step without either being rebuilt. The PAIR is what
    -- release needs: it asks the payment id whether the cut-off really settled
    -- it, and cuts the document by the positions that survive.
    --
    -- The parent FOREIGN KEY stays, under the exemption stated on subledgers in
    -- bank/0001_init.sql and for cycle_payments' reason: the store writes both
    -- sides within one statement sequence, so no caller can produce an orphan.
    -- It is also what makes releasing a cut-off one DELETE. payment_id carries
    -- none, which is the same rule from its other side — the payments row is in
    -- this database, so the constraint could be written, and it stays out because
    -- a share must be recordable whatever else is in the database.
    cycle_id   TEXT NOT NULL,
    seq        INTEGER NOT NULL,
    position   INTEGER NOT NULL,
    payment_id TEXT NOT NULL,
    PRIMARY KEY (cycle_id, seq, position),
    FOREIGN KEY (cycle_id, seq) REFERENCES held_files (cycle_id, seq) ON DELETE CASCADE
) STRICT;

CREATE TABLE held_returns (
    -- One pacs.004 uploaded to the settlement agent and not yet answered: the
    -- SECOND hop of a return, waiting for finality.
    --
    -- A return is a conversation with three participants, two of which never
    -- address each other, so the file that makes the second bank post the leg the
    -- returning bank does not hold has to be carried by the institution that has
    -- seen both ends. It cannot be queued before the agent says ACSC — a bank
    -- that had posted against a return the agent then refused would have moved a
    -- customer's money for nothing, with no file in the flow that would ever tell
    -- it — so it waits here.
    --
    -- Keyed by the PAYMENT the return names, which is the only thing the message
    -- and the answer to it have in common: the answer quotes the payment and
    -- nothing else. A return naming no payment is uploaded and never held,
    -- because nothing could ever match it.
    --
    -- One row wide where a share is two tables, because there is nothing to cut:
    -- a return is one settled payment coming back and it travels whole.
    --
    -- NO seq, and it is the one table here without one. The convention in this
    -- file's header applies to LISTED tables; nothing lists these, because a held
    -- return is looked up by the payment an arriving answer names, one at a time.
    payment_id  TEXT PRIMARY KEY,
    -- The RETURNING bank, and it is what the second hop is routed against: the
    -- other bank is whichever of the message's two agents this one is not. Kept
    -- rather than re-derived, because it is a fact about the connection this
    -- institution observed and the message does not record it — re-deriving it
    -- would mean asking which bank OUGHT to have uploaded a file whose subscriber
    -- was seen.
    returned_by TEXT NOT NULL,
    -- The returning bank's file as it arrived; held_files.file carries the
    -- argument. What differs is that the document travels UNCHANGED — it is what
    -- that bank said and is not this institution's to rewrite — so release
    -- rebuilds the header and nothing else.
    file        BLOB NOT NULL
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
    -- (sqlite.ErrNotThisStoresBook). A sentinel meaning "belongs to no single
    -- bank" is what the value would otherwise be, and there is no network for
    -- anything to belong to: the rows here belong to this institution, the way a
    -- bank's belong to it.
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
