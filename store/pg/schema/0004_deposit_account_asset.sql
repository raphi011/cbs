-- A deposit account's asset, duplicated from its backing GL account.
--
-- This is the one place the schema stores a fact twice on purpose. The GL
-- account's asset is fixed at creation, so the two cannot drift, and deriving
-- it would turn every listing of deposit accounts into a join for a value that
-- can never change. store/storetest asserts the two always agree.
--
-- Existing rows are EUR by construction, like every other backfill in this
-- series of migrations. No foreign key to assets, for the reason given in
-- 0003: the registry check is a domain rule, and a constraint here would make
-- store/pg refuse a write store/mem performs.
ALTER TABLE deposit_accounts ADD COLUMN asset TEXT;

UPDATE deposit_accounts SET asset = 'EUR' WHERE asset IS NULL;

ALTER TABLE deposit_accounts ALTER COLUMN asset SET NOT NULL;
