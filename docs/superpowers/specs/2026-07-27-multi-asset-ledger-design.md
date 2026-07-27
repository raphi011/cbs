# Multi-Asset Ledger Core — Design

Sub-project 1 of the expansion tracked in `docs/expansion-roadmap.md`. It gives
the general ledger an asset dimension so that lending, crypto and FX can be
built on top of it without re-plumbing the core.

## Goal

Today the system is implicitly single-currency: `ledger.Amount` is a bare
`int64` of minor units, no entity records *which* unit, and `Book.Post` balances
a transaction globally. After this change every GL account is denominated in
exactly one asset, every transaction must balance **per asset**, and the payment
schemes declare the asset they operate in.

Out of scope, deliberately: lending, crypto-specific custody, FX trades and
rates, cross-currency payments, and any customer/party master. Each is its own
sub-project.

## Decisions

These were settled during design; the reasoning matters more than the outcome,
because each one constrains the sub-projects that follow.

### An account is bound to exactly one asset

A EUR customer and a BTC customer have two GL accounts. A bank operating in
three currencies has three cash accounts. This is what real core banking systems
do — an account number and its currency are inseparable, which is why IBANs are
per-currency.

The alternative, an account holding a map of asset → balance, was rejected: it
turns every balance into a map, adds a dimension to snapshots and statements,
and — decisively — it does not actually buy simplicity. If an account can hold
anything, "debits equal credits" stops implying "no value was created", so a
per-asset check is needed anyway. The complexity is paid for nothing.

### A transaction must balance per asset

For each asset appearing in a transaction, debits in that asset must equal
credits in that asset.

The consequence is that an FX trade cannot be one naive two-asset posting.
Instead each currency's legs balance through an **FX position account** for that
asset: selling EUR for USD debits the customer's EUR account and credits the EUR
position; buying USD debits the USD position and credits the customer's USD
account. Both halves balance in their own asset, and the bank's open FX exposure
*is* the balance of those position accounts. This is how bank FX desks actually
account for their book, and it is what sub-project 4 will build on.

Rejected: value-balanced transactions that span assets and balance against a
supplied rate. That would make `ledger` depend on *prices* to validate a
posting — the core stops being self-contained, rate staleness becomes a
correctness problem inside the GL, and "no value was created" holds only if the
rate was right. Keeping rates out of the ledger mirrors how `ledger` is already
ignorant of holds and `deposit` is ignorant of schemes.

Also rejected: two linked transactions, one per asset. The core stays strict,
but atomicity moves out of the ledger to whoever writes both, and the trade can
no longer be reversed as a unit.

### `Amount` stays `int64`; asset scale is capped at 9

`int64` tops out near 9.22 × 10¹⁸. At 8 decimals the entire 21 M BTC supply is
2.1 × 10¹⁵ satoshis, so BTC, fiat and 6-decimal stablecoins are all exact. At 18
decimals (wei) `int64` holds **9.2 ETH** — not a tight fit but a total failure.
Native precision for 18-decimal assets and `int64` are mutually exclusive.

The scale field is therefore validated to `0..9`, and 18-decimal assets, if they
ever arrive, are held at gwei precision as a stated simplification. This keeps
the README's "integer minor units" lesson intact and keeps the whole change
additive — no arithmetic churn across five packages and their tests.

The hedge: `type Amount = int64` (an **alias**, so any `int64` flows in
silently) becomes `type Amount int64` (a **defined type**). This costs almost
nothing now, stops raw integers leaking in, and if a 128-bit amount ever becomes
necessary the compiler points at every site that must change instead of
accepting the old type silently. It does not avoid that churn — it makes it
mechanical rather than archaeological.

### No customer entity

There is no `Customer` anywhere in the codebase today — "customer" appears only
in names and doc comments, and `deposit.Account` *is* the customer-facing unit.
It stays that way: a customer holding EUR, USD and BTC has three deposit
accounts, each with its own IBAN, which is how most European retail banks
actually work.

A party master is a substantial teaching topic with nothing to do with assets.
Folding it in would roughly double this spec for unrelated reasons. If it is
wanted, it becomes its own sub-project.

### Schemes declare their asset

`payment` must reject a EUR account paying a BTC account. The per-asset ledger
invariant **cannot** catch this — each leg balances in its own asset, so the
posting is valid double-entry; it is merely meaningless. Per-asset balancing
guarantees no value is *created*, not that a payment is *coherent*. That check
belongs in `payment`.

## Design

### `ledger`

New types:

```go
type AssetCode string   // natural key: "EUR", "USD", "BTC"

type AssetClass int
const (
    Fiat AssetClass = iota
    Crypto
)

type Asset struct {
    Code  AssetCode
    Name  string
    Scale uint8   // decimal places; validated 0..9
    Class AssetClass
}
```

The asset registry is **book-scoped** — `Book.CreateAsset`, `GetAsset`,
`ListAssets`. Each participant already owns its own book, and a bank that does
not deal in BTC should not carry BTC in its chart of accounts.

`Account` gains `Asset AssetCode`, set at creation and immutable thereafter.
`CreateAccount` and `CreateAccountTx` grow an asset parameter. This is a
breaking signature change and that is correct: the compiler finds every call
site, and there is no sensible default — a silent EUR fallback is precisely the
bug to avoid.

`Entry` does **not** carry an asset. It is derived from the account it
references. Storing it would allow the two to disagree, and an entry's asset is
never anything other than its account's. Read models (API DTOs, statements)
surface the derived value; the stored row does not.

`validateBalance(entries)` becomes `validateBalance(entries, accounts)`. `Post`
already loads the accounts map before calling it, so nothing is re-fetched. It
sums debits and credits per asset code and fails on the first asset that does
not net to zero, with a new `ErrUnbalancedAsset` carrying the offending code.
The existing `ErrUnbalancedTransaction` remains for the degenerate one-sided
case.

`checkSufficientBalance` and `BookBalance` are unchanged: an account is
single-asset, so its balance stays a scalar `Amount`.

Account numbering is untouched. IDs stay `<typeBlock>.<subledgerID>.<NNN>` and
the asset is a field, not part of the key. Encoding currency in the account
number — which some real charts of accounts do — would put redundant information
in the ID format and break every existing ID.

### `deposit`

`OpenAccount` and `OpenAccountTx` gain an `asset ledger.AssetCode` parameter and
create the backing Liability GL account in that asset.

`deposit.Account` **stores** its `Asset`. This is a deliberate exception to the
derive-don't-duplicate rule applied to `Entry`: the GL account's asset is
immutable so the two cannot drift, and deriving it would mean `ListAccounts`
doing an N+1 lookup in `store/mem` and a join in `store/pg` — divergent
complexity in both stores for a value that cannot change. `storetest` asserts
the two always agree.

`Balance`, `Hold`, `OverdraftLimit` and `Snapshot` are unchanged. They are
scalars denominated in the account's asset.

`CaptureHold` takes a counterparty GL account and needs **no new check**: a
cross-asset capture would post a debit in one asset and a credit in another, and
the per-asset rule rejects it. A test pins this, because it is the invariant
paying for itself.

### `payment`

`Scheme` gains `Asset() ledger.AssetCode`. `SchemeSEPACT` and `SchemeSEPADD`
return `"EUR"` — a fact about the real schemes, not a simplification.

The payment path validates that the debtor's and creditor's deposit accounts are
both denominated in the scheme's asset, failing with `ErrAssetMismatch`.

`Participant`'s three flat account fields — `SuspenseAccount` (Liability),
`ReserveAccount` (Asset) and `SettlementAccount` (the central bank's
`Reserve: <bank>` liability) — become per-asset:

```go
type ParticipantAccounts struct {
    Suspense   ledger.AccountID
    Reserve    ledger.AccountID
    Settlement ledger.AccountID
}

// on Participant:
Assets map[ledger.AssetCode]ParticipantAccounts
```

They are populated at join time for the assets the participant declares,
defaulting to EUR. `SettleCycle` looks up the triple by the cycle's scheme
asset; a participant lacking that asset is an error, never a silent EUR
fallback. Netting gains no dimension — a cycle belongs to one scheme and
therefore one asset.

This is the only structurally invasive change in the payment layer, and it is
what makes adding a USD scheme later a data change rather than a code change.

### Stores

`ledger.Tx` gains `PutAsset`, `GetAsset`, `ListAssets`. Both `store/mem` and
`store/pg` implement them, and `store/storetest` covers them — plus the new
invariants:

- an account cannot reference an unknown asset;
- a cross-asset transaction is refused identically by both stores;
- `deposit.Account.Asset` always equals its backing GL account's asset;
- a participant missing the cycle's asset fails settlement identically.

### Schema

`migrate.go` applies `store/pg/schema/*.sql` in order, so this is
`0002_assets.sql`:

- `assets (book_id, code, name, scale, class)`, primary key `(book_id, code)`.
- `accounts` gains `asset TEXT`, foreign key `(book_id, asset) → assets`.
- `deposit_accounts` gains `asset TEXT`.
- `participants` loses its three account columns to a child table
  `participant_assets (book_id, participant_id, asset, suspense, reserve,
  settlement)`.
- Backfill: insert `EUR` per existing book, set `asset = 'EUR'` on existing
  accounts and deposit accounts, migrate the participant columns into the child
  table, then apply `NOT NULL`. Existing data is EUR by construction — there was
  never anything else.

Per `CLAUDE.md` the comments in the schema are domain content. They must explain
why the asset sits on the account and not on the entry, and why
`participant_assets` is a child table.

### API

Every ledger-touching route is already scoped under `/participants/{pid}/`,
which matches the book-scoped registry:

- `GET /participants/{pid}/assets` and `POST /participants/{pid}/assets`.
- `asset` added to the account, deposit-account, entry and payment DTOs.
- `asset` required in the create-account and open-account request bodies.
- the create-participant body gains an optional `assets` list, defaulting to
  `["EUR"]`, which drives the `participant_assets` rows created at join time.

### Web

`web/src/lib/money.ts` hardcodes `/ 100` in three places. It becomes
scale-driven: the formatter takes the asset and divides by `10^scale`, so BTC
renders at eight places. Account and balance views display the asset. This is
the only place where single-currency assumptions are baked into presentation.

### Seed

`seed` declares EUR in each book it builds and passes it explicitly wherever
accounts are opened. The seeded network stays EUR-only, so the demo data is
unchanged in substance.

## Documentation

Per `CLAUDE.md`, a domain change is a documentation change across every layer:

- **`README.md`** — a multi-asset section, plus edits to *Amounts and
  Precision* (the scale cap and why `int64` bounds it), *Chart of Accounts*
  (accounts are per-asset), and the payment sections (schemes are per-asset).
- **`web/src/components/hint-content.ts`** — new keys for the new concepts.
  Every `[[wiki-link]]` must resolve: an unresolved link throws under
  `RootLayout` and takes down every route in the dev app while leaving
  `next build` green.
- **Quiz chapter 16** on multi-asset accounting, held by
  `web/src/lib/quiz/diversity.test.ts` to 18–22 questions, at least 8 distinct
  `concept` tags, no tag more than 3×, and all three difficulty tiers. This is a
  meaningful slice of the work, not a footnote.

## Testing

- `ledger` unit tests: per-asset balancing accepts a valid two-asset posting
  through position accounts, rejects a cross-asset transfer, and reports the
  offending asset.
- `deposit`: cross-asset `CaptureHold` is refused by the ledger invariant.
- `payment`: a scheme rejects a payment whose accounts are not in its asset;
  settlement fails when a participant lacks the cycle's asset.
- `store/storetest`: the conformance cases listed above, so `store/mem` and
  `store/pg` cannot diverge.
- `go test ./...` green with no database, then `make test-pg` green against
  Postgres. Both, every time.
- `npm run test` in `web/`, which catches unresolved wiki-links and enforces
  quiz diversity.

## Risks

- **Breaking signatures.** `CreateAccount` and `OpenAccount` change shape, and
  every caller — `seed`, `api`, `payment`, tests — must pass an asset. This is
  wide but mechanical, and the compiler drives it.
- **The participant refactor** is the one change that touches settlement logic,
  the area with the most subtle existing behaviour. Its storetest coverage is
  what keeps it honest.
- **Documentation drift** is the likeliest failure: it is easy to land the code
  and leave the README, hints and quiz behind. The quiz chapter should be
  treated as part of the work, not as follow-up.
