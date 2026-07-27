-- A participant's internal accounts, one set per asset it operates in.
--
-- These were three columns on participants. They move to a child table because
-- each of those accounts is denominated in exactly one asset: a bank clearing
-- both a euro and a dollar scheme needs two suspense accounts and two reserve
-- accounts, not two currencies inside one. Keying by (participant, asset) makes
-- adding a scheme in a new asset a data change rather than a schema change.
--
-- There is no foreign key from asset to assets, for the same reason the audit
-- table has none: a participant row is keyed by participant id, while an asset
-- is keyed by the book it is registered in. The same code appears in two books
-- here — the bank's and the central bank's — so there is no single row for it
-- to point at.
--
-- The existing three columns are migrated into a single EUR row per
-- participant, then dropped. As everywhere in this series, EUR is exact: the
-- system had no other asset.
CREATE TABLE participant_assets (
    participant_id TEXT NOT NULL REFERENCES participants (id) ON DELETE CASCADE,
    asset          TEXT NOT NULL,
    suspense       TEXT NOT NULL,
    reserve        TEXT NOT NULL,
    settlement     TEXT NOT NULL,
    seq            BIGSERIAL NOT NULL,
    PRIMARY KEY (participant_id, asset)
);

INSERT INTO participant_assets (participant_id, asset, suspense, reserve, settlement)
SELECT id, 'EUR', suspense_account, reserve_account, settlement_account FROM participants;

ALTER TABLE participants DROP COLUMN suspense_account;
ALTER TABLE participants DROP COLUMN reserve_account;
ALTER TABLE participants DROP COLUMN settlement_account;
