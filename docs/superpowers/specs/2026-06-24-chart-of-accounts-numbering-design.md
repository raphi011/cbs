# Chart-of-accounts numbering

**Date:** 2026-06-24
**Status:** Draft (pending review)

## Goal

Replace the opaque, counter-based identifiers for **accounts** and **subledgers**
with meaningful, hierarchical chart-of-accounts numbers, and use those numbers
directly as the entities' IDs. Ledger IDs are unchanged.

Today:

```
Subledger  Customer Deposits   sub_3
Account    Alice Andersson     acct_11   (Liability)
```

After:

```
Subledger  Customer Deposits   100
Account    Alice Andersson     200.100.001   (Liability)
```

## Why

A real general-ledger account number encodes the account's **classification** in
its digits — the leading digit tells you whether it's an asset, liability,
equity, revenue, or expense account. The current `acct_<n>` IDs are opaque
surrogate keys carrying no meaning.

For a *chart of accounts* specifically, the natural-key approach (the number *is*
the identity) is idiomatic rather than risky: CoA codes are permanent by
accounting convention — accounts are never renumbered, and reclassification means
opening a new account and closing the old one, so the identity never churns.
Using one self-describing identifier (instead of an opaque ID plus a parallel
code) removes the "which identifier do I reference?" confusion.

## Numbering scheme

An account number has three dot-separated segments: **type . subledger .
sequence**.

```
Account ID = <typeBlock>.<subledgerBlock>.<seq>      e.g. 200.100.001
```

| Segment | Meaning | Values |
|---|---|---|
| `typeBlock` | the account's type (leading = classic CoA meaning) | 100 Asset · 200 Liability · 300 Equity · 400 Revenue · 500 Expense |
| `subledgerBlock` | which subledger the account lives in (= the subledger's own ID) | 100, 200, 300, … (book-level, creation order) |
| `seq` | the account's position within its (type, subledger) | 001, 002, … (zero-padded to 3 digits) |

A **subledger's ID** is its block alone (`100`, `200`, …).

The number is encoded purely from information available inside the `ledger`
package (the account's type and its subledger). It deliberately does **not**
encode the owning bank/participant — that concept lives in the `payment` layer,
and reaching for it would break the ledger core's layering. Consequence:
**numbers are unique within a `Book`, not globally** — exactly the scope today's
`acct_*` IDs already have (each bank's `Book` numbers its own accounts; `acct_5`
already exists in many books).

### Worked example (current seed)

| Account | Type | Subledger (block) | Number |
|---|---|---|---|
| Reserve at Central Bank | Asset | Interbank (200) | `100.200.001` |
| Clearing Suspense | Liability | Interbank (200) | `200.200.001` |
| Alice Andersson | Liability | Customer Deposits (100) | `200.100.001` |
| Aaron Apstorp | Liability | Customer Deposits (100) | `200.100.002` |
| Share Capital | Equity | Equity (300) | `300.300.001` |
| Vault Cash | Asset | Treasury (400) | `100.400.001` |
| Fee Income | Revenue | Income (500) | `400.500.001` |
| Operating Expenses | Expense | Expenses (600) | `500.600.001` |

The mixed-type **Interbank** subledger (block 200) demonstrates the design: its
asset and liability accounts share the middle segment but differ in the leading
block (`100.200.*` vs `200.200.*`), so the type is readable at a glance. The two
`200`s in `200.200.001` mean different things by position (type vs subledger) —
unambiguous, accepted as the chosen aesthetic.

### Sequence scoping

`seq` is per **(typeBlock, subledgerBlock)** pair, each starting at `001`. So in
Interbank, Clearing Suspense (Liability) is `200.200.001` and Reserve (Asset) is
`100.200.001` — both `001`, disambiguated by the type block.

`seq` is zero-padded to 3 digits (supports 999 accounts per type per subledger —
ample for this model). Subledger blocks step by 100 with no fixed cap (the 10th
subledger is `1000`).

## Data model & implementation

No Go *type* changes: `AccountID` and `SubledgerID` remain `string`; only the
*values* produced change. All assignment is centralized in two `Book` methods.

### `AccountType` → block helper (`ledger/types.go`)

Add an unexported helper:

```go
// codeBlock returns the chart-of-accounts leading block for the type.
func (t AccountType) codeBlock() int  // Asset→100, Liability→200, …, Expense→500
```

### `Book` state (`ledger/book.go`)

Add two deterministic counters to `Book`:

- `subledgerSeq int` — the last subledger block issued (starts at 0; each
  `CreateSubledger` does `subledgerSeq += 100` and uses that as the block/ID).
- `accountSeq map[string]int` — next account sequence per `"<typeBlock>.<subledgerBlock>"`
  key.

These replace the use of the shared `nextID` counter **for accounts and
subledgers only**. `nextID` stays in use for ledgers, audit events,
transactions, etc.

### `CreateSubledger` (`ledger/book.go`)

```go
sl := &Subledger{
    ID:        SubledgerID(strconv.Itoa(s.nextSubledgerBlock())), // 100, 200, …
    LedgerID:  ledgerID,
    Name:      name,
    CreatedAt: s.now(),
}
```

`nextSubledgerBlock` increments `subledgerSeq` by 100 and returns it.

### `CreateAccount` (`ledger/book.go`)

The passed `subledgerID` *is* the subledger block. The number is then:

```go
block := accountType.codeBlock()                 // e.g. 200
key := fmt.Sprintf("%d.%s", block, subledgerID)  // e.g. "200.100"
s.accountSeq[key]++
id := fmt.Sprintf("%d.%s.%03d", block, subledgerID, s.accountSeq[key]) // 200.100.001
acct := &Account{ID: AccountID(id), SubledgerID: subledgerID, Name: name, Type: accountType, ...}
```

`ErrSubledgerNotFound` behavior is unchanged (still validated before assignment).

### Determinism

The scheme is fully deterministic given creation order, so the seed (built on a
deterministic clock) still produces reproducible IDs — and they're now *more*
stable and readable than before (the old shared counter interleaved
`evt_`/`ldg_`/`sub_`/`acct_`, so account numbers jumped). Removing accounts and
subledgers from the shared `nextID` counter will shift the numeric suffixes of
the *other* `nextID`-based IDs (`evt_`, `txn_`, `ldg_`); this is cosmetic and
only affects tests/seed output that assert those exact strings.

## Impact / affected areas

- **`ledger/book.go`, `ledger/types.go`** — the change itself (above).
- **Tests** — any assertion on a literal ID string updates to the new format.
  Known hardcoded literals to fix:
  - `deposit/register_test.go:389` — builds `"acct_" + itoa(id)`; rework to the
    new numbering (or capture the ID from creation instead).
  - `payment/system_test.go:381` — uses `"acct_999"` as a deliberately-missing
    account; still functions as "not found" but update to a new-format sentinel
    (e.g. `"999.999.999"`) for clarity.
  - Any other exact-ID assertions surfaced by running `go test ./...`.
- **`seed/seed.go`** — captures IDs from creation calls, so it adapts with no
  code change; verify `seed/seed_test.go` has no exact-ID assertions.
- **API** — `accountDTO.ID` / `subledgerDTO.ID` are strings; the wire *schema*
  is unchanged, only values differ. `api/server_test.go` captures IDs from
  responses, so it should adapt; verify no hardcoded IDs.
- **Web (`web/`)** — treats IDs as opaque strings (`id-text.tsx` renders them
  monospace; nothing parses a prefix). **No functional change.** Optional: update
  the `id-text.tsx` comment that cites `acct_9` as an example.

## Out of scope (deliberately)

- **Renumbering ledgers** — ledger IDs stay `ldg_*`. The ledger isn't encoded in
  account numbers, so renumbering buys nothing and churns references.
- **Caller-specified numbers** — numbers are auto-assigned from type + subledger;
  the create API/UI gains no new inputs. (A future change could let an operator
  assign codes explicitly, with range/uniqueness validation.)
- **Global (cross-book) uniqueness** — would require encoding the bank, which the
  ledger core can't see. Per-book scope is retained.
- **The control-account / subsidiary-ledger restructure** discussed separately
  (keeping the GL small with one control account per group) — a much larger,
  independent change.

## Testing

After implementation, the existing suite is the regression gate; it must pass
once exact-ID assertions are updated to the new format. Add focused unit tests in
`ledger` for the numbering itself:

- `CreateSubledger` issues `100`, `200`, `300` in order.
- `CreateAccount` issues `<typeBlock>.<subledgerBlock>.001` and increments `seq`
  per (type, subledger) — including the mixed-type case (Asset + Liability in one
  subledger each start at `001` under their own block).
- Determinism: two freshly-seeded books produce identical account IDs.

Gate: `go build ./... && go vet ./... && go test ./... && gofmt -l .`
