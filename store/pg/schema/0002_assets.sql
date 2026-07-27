-- Assets are the units of value that accounts are denominated in.
--
-- The table is keyed (book_id, code) rather than by code alone because assets
-- are per book, exactly like ledgers and accounts. Each participant bank owns
-- its own book, and a bank that does not deal in BTC should not carry BTC in
-- its chart of accounts.
--
-- scale is the number of decimal places. It is capped in the domain layer at 9
-- rather than here, because the reason for the cap is the width of Go's int64:
-- an amount is stored as BIGINT, which at 18 decimal places would hold only 9.2
-- whole units. The constraint below states the same bound so the database is
-- not merely trusting the application.
CREATE TABLE assets (
    book_id TEXT     NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    code    TEXT     NOT NULL,
    name    TEXT     NOT NULL,
    scale   SMALLINT NOT NULL CHECK (scale BETWEEN 0 AND 9),
    class   SMALLINT NOT NULL,
    seq     BIGSERIAL NOT NULL,
    PRIMARY KEY (book_id, code)
);
