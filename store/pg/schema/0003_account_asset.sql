-- Every account is denominated in exactly one asset, fixed at creation.
--
-- The column is on accounts and NOT on entries. An entry's asset is always its
-- account's, so storing it twice would only create the possibility of the two
-- disagreeing. Post derives it when it checks that debits equal credits within
-- each asset.
--
-- Existing rows are EUR by construction: before this migration the system had
-- no asset dimension at all, and every book it has ever held was a euro book.
-- The backfill is therefore exact, not a guess, which is why the column can go
-- straight to NOT NULL.
--
-- There is no foreign key to assets, for the same reason there is none from
-- subledgers to ledgers: "the asset must be registered" is a DOMAIN rule, and
-- ledger.Book enforces it by reading the registry before it creates an
-- account. A foreign key here would make store/pg refuse a write store/mem
-- performs, and the two stores accepting and refusing the same writes is the
-- property store/storetest exists to hold. An earlier FK on
-- subledgers (book_id, ledger_id) broke exactly this and was removed.
ALTER TABLE accounts ADD COLUMN asset TEXT;

INSERT INTO assets (book_id, code, name, scale, class)
SELECT id, 'EUR', 'Euro', 2, 0 FROM books
ON CONFLICT (book_id, code) DO NOTHING;

UPDATE accounts SET asset = 'EUR' WHERE asset IS NULL;

ALTER TABLE accounts ALTER COLUMN asset SET NOT NULL;
