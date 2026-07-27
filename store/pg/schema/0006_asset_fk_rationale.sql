-- Why accounts.asset and deposit_accounts.asset have no foreign key.
--
-- 0003 and 0004 added the columns and are shipped, so the reasoning goes here
-- rather than into an edit of them. It is recorded in the database itself, next
-- to the columns it explains, because the absence of a constraint is invisible
-- otherwise: the next author reads the schema, sees a code column pointing at a
-- table keyed by exactly that code, and adds the "missing" reference.
--
-- The reference is missing on purpose, and putting it back would break the
-- conformance suite. See the comment text below.
COMMENT ON COLUMN accounts.asset IS
    'The asset this account is denominated in, fixed at creation. '
    'There is deliberately NO foreign key to assets (book_id, code). A store is '
    'a per-table key/value layer; "the asset must be registered" is a DOMAIN '
    'rule, enforced by ledger.Book.CreateAccountTx, which reads the registry '
    'before it creates an account — exactly where "the parent must exist" lives '
    'for ledgers, subledgers and accounts too. Postgres could express the rule '
    'and store/mem could not, so neither does: a constraint here would make '
    'store/pg refuse a write store/mem performs, and the two stores accepting '
    'and refusing the same writes is the property store/storetest exists to '
    'hold. The subtest is ParentReferencesAreNotEnforced in '
    'store/storetest/storetest.go, whose fixtures write accounts with no asset '
    'set at all. An earlier composite FK on subledgers (book_id, ledger_id) '
    'broke that same subtest and was removed for the same reason.';

COMMENT ON COLUMN deposit_accounts.asset IS
    'The asset this deposit account is denominated in, duplicated from its '
    'backing GL account — the one fact this schema stores twice on purpose, '
    'because the GL account''s asset is immutable and deriving it would turn '
    'every listing into a join. No foreign key to assets, for the reason given '
    'on accounts.asset: the registry check is a domain rule, and a constraint '
    'here would make store/pg refuse a write store/mem performs. See '
    'ParentReferencesAreNotEnforced in store/storetest/storetest.go.';
