# Multi-Asset Ledger Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the general ledger an asset dimension — every GL account is denominated in exactly one asset, every transaction balances per asset, and payment schemes declare the asset they operate in.

**Architecture:** Assets are a book-scoped registry in `ledger`. `Account` gains an immutable `Asset` field; `Entry` derives its asset from the account it references. `Book.Post`'s balance check moves from one global debit/credit comparison to one per asset. `deposit.Account` stores its asset (denormalized from an immutable source), and `payment` validates that both legs of a payment are in the scheme's asset — which the ledger cannot catch, because each leg balances in its own asset.

**Tech Stack:** Go (stdlib only in the domain packages, `pgx/v5` in `store/pg`), Postgres, Next.js + TypeScript in `web/`.

**Spec:** `docs/superpowers/specs/2026-07-27-multi-asset-ledger-design.md`

## Global Constraints

- **Both stores must stay green.** `go test ./...` with no `DATABASE_URL` (runs `store/mem`), then `make test-pg` (runs the same suites against `store/pg`). Every task ends with both.
- **`store/mem` and `store/pg` must never accept or refuse a write differently.** `store/storetest` is the enforcement; a new store method without a conformance case is an incomplete task.
- **Migrations are immutable.** `store/pg/migrate.go` keys the applied-set on filename with no checksum, so a shipped `.sql` file is never edited — a schema change is always a new file with the next number. This plan adds `0002` through `0005`.
- **Asset scale is validated `0..9`.** `ledger.Amount` is `int64`, which holds 9.2 ETH at 18 decimals. This cap is the reason the type can stay `int64`.
- **Schema comments are domain content**, not implementation notes (`CLAUDE.md`). Every new table and column gets a comment explaining the domain reason.
- **Every `[[wiki-link]]` must resolve** to a key in `web/src/components/hint-content.ts`. An unresolved link throws under `RootLayout` and takes down every route in the dev app while leaving `next build` green. `npm run test` catches it.
- **Existing data is EUR by construction.** There was never any other asset, so every backfill is `'EUR'`.
- **Go module path** is `github.com/raphi011/cbs`.

---

### Task 1: `Amount` becomes a defined type

Mechanical and isolated. Doing it first means every later task is written against the real type, and raw `int64`s can no longer leak in.

**Files:**
- Modify: `ledger/types.go:19`
- Modify: any call site the compiler flags (expected: `store/pg/tx_ledger.go`, `store/mem/tx.go`, `api/dto*.go`, tests)

**Interfaces:**
- Consumes: nothing.
- Produces: `type Amount int64` — a defined type. Untyped constants (`100`, `0`) still convert implicitly; an `int64` *variable* now needs an explicit `ledger.Amount(x)` conversion.

- [ ] **Step 1: Change the alias to a defined type**

In `ledger/types.go`, replace the declaration and extend the comment:

```go
// Amount represents a monetary value in the smallest unit of the asset
// (e.g., cents for USD, satoshis for BTC). This is the standard approach
// used by most payment systems and banks.
//
// It is a defined type rather than an alias, so a bare int64 cannot be
// passed where an Amount is expected. That matters more than it looks:
// if a 128-bit amount ever becomes necessary, the compiler points at
// every site that has to change instead of silently accepting the old
// type.
//
// The width is a real constraint, not an incidental one. int64 tops out
// near 9.22e18, so an asset with 18 decimal places (wei) would hold only
// 9.2 units. Asset.Scale is capped at 9 for exactly this reason.
type Amount int64
```

- [ ] **Step 2: Build and let the compiler find the call sites**

Run: `go build ./... && go vet ./...`
Expected: FAIL, listing every site passing an `int64` variable where an `Amount` is wanted (and the reverse — e.g. scanning a `BIGINT` in `store/pg`).

- [ ] **Step 3: Fix each flagged site with an explicit conversion**

For a pgx scan in `store/pg`, scan into `int64` and convert:

```go
var amount int64
// ... rows.Scan(..., &amount, ...)
e.Amount = ledger.Amount(amount)
```

For a write, convert the other way: `int64(e.Amount)`.

Do not change any behaviour — these are type conversions only.

- [ ] **Step 4: Verify the whole suite still passes**

Run: `go build ./... && go test ./...`
Expected: PASS, no test changes needed.

- [ ] **Step 5: Verify against Postgres**

Run: `make test-pg`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "Make ledger.Amount a defined type rather than an alias

An alias lets any int64 flow in unchecked. As a defined type the compiler
guards the boundary, and if a 128-bit amount is ever needed it will point
at every site that must change."
```

---

### Task 2: The `Asset` type and the book-scoped registry

**Files:**
- Modify: `ledger/types.go` (add `AssetCode`, `AssetClass`, `Asset`)
- Modify: `ledger/errors.go` (add sentinels)
- Modify: `ledger/store.go` (add three `Tx` methods)
- Modify: `ledger/book.go` (add `CreateAsset`, `CreateAssetTx`, `GetAsset`, `ListAssets`)
- Modify: `ledger/audit.go` (add `EventAssetCreated`)
- Create: `ledger/asset_test.go`
- Modify: `store/mem/mem.go` (state map), `store/mem/tx.go` (three methods)
- Create: `store/pg/schema/0002_assets.sql`
- Modify: `store/pg/tx_ledger.go` (three methods)
- Modify: `store/storetest/storetest.go` (conformance cases)

**Interfaces:**
- Consumes: `ledger.Amount` (Task 1).
- Produces:
  - `type AssetCode string`
  - `type AssetClass int` with `Fiat AssetClass = iota; Crypto`
  - `type AssetDef struct { Code AssetCode; Name string; Scale uint8; Class AssetClass }`
  - `func (s *Book) CreateAsset(ctx, code AssetCode, name string, scale uint8, class AssetClass) (AssetDef, error)`
  - `func (s *Book) CreateAssetTx(ctx, tx Tx, code AssetCode, name string, scale uint8, class AssetClass) (AssetDef, error)`
  - `func (s *Book) GetAsset(ctx, code AssetCode) (AssetDef, error)`
  - `func (s *Book) ListAssets(ctx) ([]AssetDef, error)`
  - `Tx.PutAsset(ctx, book BookID, a AssetDef) error`, `Tx.GetAsset(ctx, book BookID, code AssetCode) (AssetDef, error)`, `Tx.ListAssets(ctx, book BookID) ([]AssetDef, error)`
  - `ErrAssetNotFound`, `ErrDuplicateAsset`, `ErrInvalidScale`
  - `EventAssetCreated = "asset.created"`

- [ ] **Step 1: Write the failing tests**

Create `ledger/asset_test.go`. Follow the existing test style in `ledger/book_test.go` for constructing a `Book` over `store/mem`.

```go
package ledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

func TestCreateAssetRoundTrips(t *testing.T) {
	book := newBook(t)
	ctx := context.Background()

	want, err := book.CreateAsset(ctx, "EUR", "Euro", 2, ledger.Fiat)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if want.Code != "EUR" || want.Scale != 2 || want.Class != ledger.Fiat {
		t.Fatalf("CreateAsset returned %+v", want)
	}

	got, err := book.GetAsset(ctx, "EUR")
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if got != want {
		t.Errorf("GetAsset = %+v, want %+v", got, want)
	}
}

func TestCreateAssetRejectsScaleAboveNine(t *testing.T) {
	book := newBook(t)

	// 18 decimals is what an 18-decimal crypto asset would want. int64
	// holds 9.2 units at that scale, so the registry refuses it rather
	// than letting the overflow surface as a wrong balance later.
	_, err := book.CreateAsset(context.Background(), "ETH", "Ether", 18, ledger.Crypto)
	if !errors.Is(err, ledger.ErrInvalidScale) {
		t.Errorf("CreateAsset(scale 18) error = %v, want ErrInvalidScale", err)
	}
}

func TestCreateAssetRejectsDuplicateCode(t *testing.T) {
	book := newBook(t)
	ctx := context.Background()

	if _, err := book.CreateAsset(ctx, "EUR", "Euro", 2, ledger.Fiat); err != nil {
		t.Fatalf("first CreateAsset: %v", err)
	}
	_, err := book.CreateAsset(ctx, "EUR", "Euro again", 2, ledger.Fiat)
	if !errors.Is(err, ledger.ErrDuplicateAsset) {
		t.Errorf("duplicate CreateAsset error = %v, want ErrDuplicateAsset", err)
	}
}

func TestGetAssetUnknownCode(t *testing.T) {
	book := newBook(t)

	_, err := book.GetAsset(context.Background(), "BTC")
	if !errors.Is(err, ledger.ErrAssetNotFound) {
		t.Errorf("GetAsset error = %v, want ErrAssetNotFound", err)
	}
}
```

If `newBook(t)` does not already exist as a helper in the `ledger_test` package, add it next to the other helpers in `ledger/book_test.go`, matching however that file currently builds a `Book` over `store/mem`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./ledger/ -run TestCreateAsset -v`
Expected: FAIL — compilation error, `book.CreateAsset` undefined.

- [ ] **Step 3: Add the types**

In `ledger/types.go`, after the `AccountType` block:

```go
// AssetCode is the natural key of an asset: "EUR", "USD", "BTC".
type AssetCode string

// AssetClass groups assets by what kind of thing they are. It carries no
// behaviour today; it is what lets a chart of accounts and a UI tell a
// currency from a token without pattern-matching on the code.
type AssetClass int

const (
	Fiat AssetClass = iota
	Crypto
)

func (c AssetClass) String() string {
	switch c {
	case Fiat:
		return "Fiat"
	case Crypto:
		return "Crypto"
	default:
		return "Unknown"
	}
}

// MaxAssetScale is the largest number of decimal places an asset may have.
//
// Amount is an int64, which tops out near 9.22e18. At 9 decimal places that
// is still 9.2 billion whole units — more than enough for any fiat currency
// and for BTC, whose entire 21M supply is 2.1e15 satoshis at 8 places. At 18
// places (wei) an int64 would hold 9.2 units, so 18-decimal assets are held
// at reduced precision rather than being silently truncated at runtime.
const MaxAssetScale = 9

// AssetDef is a unit of value that accounts are denominated in.
//
// Every GL account is bound to exactly one asset, fixed at creation. That is
// how real core banking systems work — an account number and its currency
// are inseparable, which is why IBANs are per-currency — and it is what keeps
// a balance a scalar rather than a map.
//
// Named AssetDef rather than Asset because Asset is already taken: it is the
// AccountType constant for the asset side of the balance sheet. That constant
// keeps the name — it is the accounting term, and it appears throughout the
// README, the quiz and every chart of accounts.
type AssetDef struct {
	Code  AssetCode
	Name  string
	Scale uint8 // decimal places; 2 for EUR, 8 for BTC
	Class AssetClass
}
```

- [ ] **Step 4: Add the error sentinels**

In `ledger/errors.go`, inside the existing `var (...)` block:

```go
	// ErrAssetNotFound is returned when an asset code is not registered in
	// this book. Assets are per book: a bank that does not deal in BTC has
	// no BTC in its chart of accounts.
	ErrAssetNotFound = errors.New("asset not found")

	// ErrDuplicateAsset is returned when registering an asset code that the
	// book already has.
	ErrDuplicateAsset = errors.New("asset already exists")

	// ErrInvalidScale is returned when an asset's scale exceeds
	// MaxAssetScale. Amount is an int64 and cannot represent 18 decimal
	// places usefully.
	ErrInvalidScale = errors.New("asset scale exceeds the maximum supported decimal places")
```

- [ ] **Step 5: Add the store interface methods**

In `ledger/store.go`, in the `Tx` interface, above `PutLedger`:

```go
	// Assets are per book. PutAsset is an upsert, like every other Put here;
	// the duplicate check lives in Book.CreateAssetTx, so a store never has
	// to know the difference between registering and correcting an asset.
	PutAsset(ctx context.Context, book BookID, a AssetDef) error
	GetAsset(ctx context.Context, book BookID, code AssetCode) (AssetDef, error)
	ListAssets(ctx context.Context, book BookID) ([]AssetDef, error)
```

- [ ] **Step 6: Add the audit event**

In `ledger/audit.go`, next to `EventAccountCreated`:

```go
	EventAssetCreated = "asset.created"
```

Match the exact form of the surrounding constants — if they are grouped in a `const (...)` block with string values, add it there.

- [ ] **Step 7: Implement the Book methods**

In `ledger/book.go`, after the `CreateAccount` block:

```go
// CreateAsset registers an asset in this book.
//
// Assets are book-scoped rather than global because each participant owns its
// own book: a bank that does not deal in BTC should not carry BTC in its chart
// of accounts.
//
// Returns ErrDuplicateAsset if the code is already registered, and
// ErrInvalidScale if scale exceeds MaxAssetScale.
func (s *Book) CreateAsset(ctx context.Context, code AssetCode, name string, scale uint8, class AssetClass) (AssetDef, error) {
	var out AssetDef
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.CreateAssetTx(ctx, tx, code, name, scale, class)
		return err
	})
	return out, err
}

// CreateAssetTx is CreateAsset within a caller-supplied unit of work.
func (s *Book) CreateAssetTx(ctx context.Context, tx Tx, code AssetCode, name string, scale uint8, class AssetClass) (AssetDef, error) {
	if err := ValidateText("code", string(code)); err != nil {
		return AssetDef{}, err
	}
	if err := ValidateText("name", name); err != nil {
		return AssetDef{}, err
	}
	if scale > MaxAssetScale {
		return AssetDef{}, ErrInvalidScale
	}

	switch _, err := tx.GetAsset(ctx, s.id, code); {
	case err == nil:
		return AssetDef{}, ErrDuplicateAsset
	case !errors.Is(err, ErrAssetNotFound):
		return AssetDef{}, err
	}

	a := AssetDef{Code: code, Name: name, Scale: scale, Class: class}
	if err := tx.PutAsset(ctx, s.id, a); err != nil {
		return AssetDef{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ScopeLedger, EventAssetCreated, string(a.Code), a); err != nil {
		return AssetDef{}, err
	}
	return a, nil
}

// GetAsset retrieves an asset by its code.
// Returns ErrAssetNotFound if the asset is not registered in this book.
func (s *Book) GetAsset(ctx context.Context, code AssetCode) (AssetDef, error) {
	var out AssetDef
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetAsset(ctx, s.id, code)
		return err
	})
	return out, err
}

// ListAssets returns every asset registered in this book.
func (s *Book) ListAssets(ctx context.Context) ([]AssetDef, error) {
	var out []AssetDef
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListAssets(ctx, s.id)
		return err
	})
	return out, err
}
```

- [ ] **Step 8: Implement the mem store**

In `store/mem/mem.go`, add to the state struct next to `accounts`:

```go
	assets map[ledger.BookID]map[ledger.AssetCode]ledger.AssetDef
```

and to the constructor next to the other `make(...)` calls:

```go
		assets: make(map[ledger.BookID]map[ledger.AssetCode]ledger.AssetDef),
```

If `Reset` clears the maps explicitly rather than rebuilding the struct, clear `assets` the same way the neighbouring maps are cleared.

Add a `kindAsset` to the row-kind constants used by `insertSeq`/`sortRows`, following the existing `kindAccount`.

In `store/mem/tx.go`, above `PutAccount`:

```go
func (t *tx) PutAsset(ctx context.Context, book ledger.BookID, a ledger.AssetDef) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindAsset, string(a.Code))
	bucket(t.state.assets, book)[a.Code] = a
	return nil
}

func (t *tx) GetAsset(ctx context.Context, book ledger.BookID, code ledger.AssetCode) (ledger.AssetDef, error) {
	a, ok := t.state.assets[book][code]
	if !ok {
		return ledger.AssetDef{}, ledger.ErrAssetNotFound
	}
	return a, nil
}

func (t *tx) ListAssets(ctx context.Context, book ledger.BookID) ([]ledger.AssetDef, error) {
	out := make([]ledger.AssetDef, 0, len(t.state.assets[book]))
	for _, a := range t.state.assets[book] {
		out = append(out, a)
	}
	sortRows(t.state, out, book, kindAsset, func(a ledger.AssetDef) (time.Time, string) { return time.Time{}, string(a.Code) })
	return out, nil
}
```

`Asset` has no `CreatedAt`, so the zero time is passed and ordering falls to `seq` — which is the insertion order the suite requires.

- [ ] **Step 9: Add the Postgres migration**

Create `store/pg/schema/0002_assets.sql`:

```sql
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
```

- [ ] **Step 10: Implement the pg store**

In `store/pg/tx_ledger.go`, above `PutAccount`, following the surrounding style (`t.write()`, `t.ensureBook(...)`, `ON CONFLICT` upsert, wrapped errors):

```go
func (t *tx) PutAsset(ctx context.Context, book ledger.BookID, a ledger.AssetDef) error {
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO assets (book_id, code, name, scale, class) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (book_id, code) DO UPDATE
		SET name = EXCLUDED.name, scale = EXCLUDED.scale, class = EXCLUDED.class`,
		string(book), string(a.Code), a.Name, int16(a.Scale), int16(a.Class))
	if err != nil {
		return fmt.Errorf("pg: put asset %s: %w", a.Code, err)
	}
	return nil
}

func (t *tx) GetAsset(ctx context.Context, book ledger.BookID, code ledger.AssetCode) (ledger.AssetDef, error) {
	var a ledger.AssetDef
	var scale, class int16
	err := t.tx.QueryRow(ctx,
		`SELECT code, name, scale, class FROM assets WHERE book_id = $1 AND code = $2`,
		string(book), string(code),
	).Scan(&a.Code, &a.Name, &scale, &class)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.AssetDef{}, ledger.ErrAssetNotFound
	}
	if err != nil {
		return ledger.AssetDef{}, fmt.Errorf("pg: get asset %s: %w", code, err)
	}
	a.Scale = uint8(scale)
	a.Class = ledger.AssetClass(class)
	return a, nil
}

func (t *tx) ListAssets(ctx context.Context, book ledger.BookID) ([]ledger.AssetDef, error) {
	rows, err := t.tx.Query(ctx,
		`SELECT code, name, scale, class FROM assets WHERE book_id = $1 ORDER BY seq`,
		string(book))
	if err != nil {
		return nil, fmt.Errorf("pg: list assets: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.AssetDef, 0)
	for rows.Next() {
		var a ledger.AssetDef
		var scale, class int16
		if err := rows.Scan(&a.Code, &a.Name, &scale, &class); err != nil {
			return nil, fmt.Errorf("pg: scan asset: %w", err)
		}
		a.Scale = uint8(scale)
		a.Class = ledger.AssetClass(class)
		out = append(out, a)
	}
	return out, rows.Err()
}
```

If `store/pg`'s `Reset` lists tables to `TRUNCATE` explicitly, add `assets` to that list.

- [ ] **Step 11: Add the conformance cases**

In `store/storetest/storetest.go`, inside `RunLedger`, following the style of the existing subtests:

```go
	t.Run("AssetsAreScopedPerBook", func(t *testing.T) {
		s := open(t, newStore)

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutAsset(ctx, bookA, ledger.AssetDef{Code: "EUR", Name: "Euro", Scale: 2})
		})

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			if _, err := tx.GetAsset(ctx, bookA, "EUR"); err != nil {
				t.Errorf("GetAsset in bookA: %v", err)
			}
			// The interesting failure mode is an implementation that leaks
			// state across books.
			if _, err := tx.GetAsset(ctx, bookB, "EUR"); !errors.Is(err, ledger.ErrAssetNotFound) {
				t.Errorf("GetAsset in bookB = %v, want ErrAssetNotFound", err)
			}
			return nil
		})
	})

	t.Run("ListAssetsIsInsertionOrdered", func(t *testing.T) {
		s := open(t, newStore)

		codes := []ledger.AssetCode{"EUR", "BTC", "USD"}
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			for _, c := range codes {
				if err := tx.PutAsset(ctx, bookA, ledger.AssetDef{Code: c, Name: string(c), Scale: 2}); err != nil {
					return err
				}
			}
			return nil
		})

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			got, err := tx.ListAssets(ctx, bookA)
			if err != nil {
				return err
			}
			if len(got) != len(codes) {
				t.Fatalf("ListAssets returned %d assets, want %d", len(got), len(codes))
			}
			for i, want := range codes {
				if got[i].Code != want {
					t.Errorf("ListAssets[%d] = %s, want %s", i, got[i].Code, want)
				}
			}
			return nil
		})
	})

	t.Run("PutAssetUpsertsWithoutReissuingSeq", func(t *testing.T) {
		s := open(t, newStore)

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			if err := tx.PutAsset(ctx, bookA, ledger.AssetDef{Code: "EUR", Name: "Euro", Scale: 2}); err != nil {
				return err
			}
			return tx.PutAsset(ctx, bookA, ledger.AssetDef{Code: "BTC", Name: "Bitcoin", Scale: 8})
		})
		// Editing a row must not move it to the end of the listing.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutAsset(ctx, bookA, ledger.AssetDef{Code: "EUR", Name: "Euro (renamed)", Scale: 2})
		})

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			got, err := tx.ListAssets(ctx, bookA)
			if err != nil {
				return err
			}
			if len(got) != 2 || got[0].Code != "EUR" || got[1].Code != "BTC" {
				t.Errorf("ListAssets = %+v, want EUR then BTC", got)
			}
			if got[0].Name != "Euro (renamed)" {
				t.Errorf("upsert did not update name: %+v", got[0])
			}
			return nil
		})
	})
```

Use whatever the file's existing helpers are called — `open`, `update`, `view` here are the names used by the current suite; match them exactly rather than introducing new ones.

- [ ] **Step 12: Run the tests**

Run: `go test ./ledger/ ./store/... -v`
Expected: PASS

- [ ] **Step 13: Verify against Postgres**

Run: `make test-pg`
Expected: PASS

- [ ] **Step 14: Commit**

```bash
git add -A
git commit -m "Add a book-scoped asset registry to the ledger

Assets are per book because each participant owns its own book: a bank
that does not deal in BTC should not carry BTC in its chart of accounts.
Scale is capped at 9 decimal places, which is what an int64 amount can
carry — 18 places would hold 9.2 whole units."
```

---

### Task 3: Thread the asset through every layer that creates an account

Merged from what were three separate tasks. They cannot be split without
temporary hardcoded assets at the call sites in between: the moment
`CreateAccount` requires an asset, every caller — `deposit`, `payment`, `api`,
`seed` — must supply a real one. One commit, and the tree is green on both
sides of it.

Large, and deliberately so. The compiler drives most of it.

**Files:**
- Modify: `ledger/types.go` (`Account.Asset`), `ledger/book.go` (`CreateAccount`, `CreateAccountTx`)
- Modify: `deposit/types.go` (`Account.Asset`), `deposit/register.go` (`OpenAccount`, `OpenAccountTx`)
- Modify: `payment/participant.go` (`ParticipantAccounts`, `Participant.Assets`, `AccountsFor`, `OpenCustomerAccount`)
- Modify: `payment/system.go` (`AddParticipant`, funding, the payment path, `SettleCycle`), `payment/errors.go`, `payment/store.go`
- Modify: `api/handlers_ledger.go`, `api/handlers_deposit.go`, `api/dto_ledger.go`, `api/dto_deposit.go`
- Modify: `seed/seed.go`
- Create: `store/pg/schema/0003_account_asset.sql`, `0004_deposit_account_asset.sql`, `0005_participant_assets.sql`
- Modify: `store/pg/tx_ledger.go`, `store/pg/tx_deposit.go`, `store/pg/tx_payment.go`, `store/mem/tx_payment.go`
- Modify: `store/storetest/storetest.go`, `store/storetest/deposit.go`, `store/storetest/payment.go`
- Modify: `ledger/asset_test.go`, `deposit/register_test.go`, `payment/system_test.go`, `api/server_test.go`

**Interfaces:**
- Consumes: `ledger.AssetDef`, `Tx.GetAsset`, `Book.CreateAssetTx` (Task 2).
- Produces:
  - `Account` gains `Asset AssetCode`; `CreateAccount(ctx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error)` and the same for `CreateAccountTx`, returning `ErrAssetNotFound` for an unregistered asset.
  - `deposit.Account` gains `Asset ledger.AssetCode`; `OpenAccount(ctx, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, overdraftLimit ledger.Amount) (Account, error)` and the same for `OpenAccountTx`. The asset parameter goes *before* `overdraftLimit` so the two `ledger`-typed arguments are not adjacent and transposable.
  - `payment.ParticipantAccounts{Suspense, Reserve, Settlement ledger.AccountID}`; `Participant.Assets map[ledger.AssetCode]ParticipantAccounts` replacing the three flat fields; `(*Participant).AccountsFor(asset ledger.AssetCode) (ParticipantAccounts, error)`; `ErrParticipantAssetNotFound`.
  - `AddParticipant` gains `assets []ledger.AssetCode`; empty means `[]ledger.AssetCode{"EUR"}`.
  - `(*Participant).OpenCustomerAccount(ctx, name string, asset ledger.AssetCode) (deposit.Account, error)`.
  - `asset` required in the create-account and open-deposit-account API request bodies (400 when missing).

**Cross-asset behaviour is NOT part of this task.** Task 4 adds per-asset
balancing, so a cross-asset posting still succeeds after this task. Do not add
a guard for it here — that check belongs in `ledger`, not scattered across
callers.

- [ ] **Step 1: Write the failing tests**

Add to `ledger/asset_test.go`:

```go
func TestCreateAccountRecordsItsAsset(t *testing.T) {
	book := newBook(t)
	ctx := context.Background()

	if _, err := book.CreateAsset(ctx, "BTC", "Bitcoin", 8, ledger.Crypto); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	sl := newSubledger(t, book)

	acct, err := book.CreateAccount(ctx, sl.ID, "Crypto Custody", ledger.Asset, "BTC")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if acct.Asset != "BTC" {
		t.Errorf("account asset = %q, want BTC", acct.Asset)
	}

	got, err := book.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.Asset != "BTC" {
		t.Errorf("reloaded account asset = %q, want BTC", got.Asset)
	}
}

func TestCreateAccountRejectsUnregisteredAsset(t *testing.T) {
	book := newBook(t)
	sl := newSubledger(t, book)

	_, err := book.CreateAccount(context.Background(), sl.ID, "Dogecoin Custody", ledger.Asset, "DOGE")
	if !errors.Is(err, ledger.ErrAssetNotFound) {
		t.Errorf("CreateAccount with unregistered asset = %v, want ErrAssetNotFound", err)
	}
}
```

If `newSubledger(t, book)` does not exist as a helper, add one alongside `newBook` that creates a ledger and a subledger and returns the subledger.

Add to `deposit/register_test.go` — the deposit half (the cross-asset
`CaptureHold` case belongs to Task 4, which adds the rule that refuses it):

Add to `deposit/register_test.go`:

```go
func TestOpenAccountRecordsAssetMatchingItsGLAccount(t *testing.T) {
	reg, book := newRegister(t)
	ctx := context.Background()

	acct, err := reg.OpenAccount(ctx, subledgerFor(t, book), "Anna BTC", "BTC", 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if acct.Asset != "BTC" {
		t.Errorf("deposit account asset = %q, want BTC", acct.Asset)
	}

	gl, err := book.GetAccount(ctx, acct.GLAccount)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if gl.Asset != acct.Asset {
		t.Errorf("GL account asset %q != deposit account asset %q", gl.Asset, acct.Asset)
	}
}

Reuse whatever setup helpers `deposit/register_test.go` already has instead of
`newRegister`/`subledgerFor` if they exist under other names; register EUR and
BTC in the test book's setup.

Add to `payment/system_test.go`:

Add to `payment/system_test.go`:

```go
func TestParticipantHasAccountsPerAsset(t *testing.T) {
	net := newNetwork(t)
	ctx := context.Background()

	p, err := net.AddParticipant(ctx, "Alpha", []ledger.AssetCode{"EUR", "USD"})
	if err != nil {
		t.Fatalf("AddParticipant: %v", err)
	}

	for _, asset := range []ledger.AssetCode{"EUR", "USD"} {
		accts, err := p.AccountsFor(asset)
		if err != nil {
			t.Fatalf("AccountsFor(%s): %v", asset, err)
		}
		for name, id := range map[string]ledger.AccountID{
			"suspense": accts.Suspense, "reserve": accts.Reserve, "settlement": accts.Settlement,
		} {
			if id == "" {
				t.Errorf("%s account for %s is empty", name, asset)
				continue
			}
		}
		// Each of the three accounts must itself be denominated in that asset.
		gl, err := p.Ledger.GetAccount(ctx, accts.Suspense)
		if err != nil {
			t.Fatalf("GetAccount: %v", err)
		}
		if gl.Asset != asset {
			t.Errorf("suspense account for %s is denominated in %s", asset, gl.Asset)
		}
	}
}

func TestAccountsForUnknownAssetFails(t *testing.T) {
	net := newNetwork(t)

	p, err := net.AddParticipant(context.Background(), "Alpha", nil) // defaults to EUR
	if err != nil {
		t.Fatalf("AddParticipant: %v", err)
	}
	if _, err := p.AccountsFor("BTC"); !errors.Is(err, payment.ErrParticipantAssetNotFound) {
		t.Errorf("AccountsFor(BTC) = %v, want ErrParticipantAssetNotFound", err)
	}
}

// Settlement must never fall back to a base currency when a member does not
// hold the cycle's asset. Deleting a participant's asset entry simulates the
// state that a future non-EUR scheme would produce naturally.
func TestSettleCycleFailsWhenParticipantLacksTheAsset(t *testing.T) {
	net := newNetwork(t)
	ctx := context.Background()

	alpha := addParticipant(t, net, "Alpha")
	beta := addParticipant(t, net, "Beta")
	cycle := acceptedCycle(t, net, alpha, beta)

	// Take EUR away from a member that is about to settle.
	stored, err := net.GetParticipant(ctx, beta.ID)
	if err != nil {
		t.Fatalf("GetParticipant: %v", err)
	}
	delete(stored.Assets, "EUR")
	putParticipant(t, net, stored)

	if err := net.SettleCycle(ctx, cycle.ID); !errors.Is(err, payment.ErrParticipantAssetNotFound) {
		t.Errorf("SettleCycle = %v, want ErrParticipantAssetNotFound", err)
	}

	// And nothing was posted: the batch fails whole, exactly as it does for
	// a member that cannot cover its position.
	assertNoSettlementPostings(t, net, cycle.ID)
}
```

`acceptedCycle`, `putParticipant` and `assertNoSettlementPostings` stand in for whatever the existing end-to-end settlement test in this file already does to drive a cycle to the point of settlement, write a participant back, and assert that a failed batch posted nothing. Read that test first and reuse its helpers under their real names — do not add parallel ones.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./ledger/ ./deposit/ ./payment/ -run 'Asset|AccountsFor' -v`
Expected: FAIL — `CreateAccount` takes 4 arguments, `OpenAccount` takes 4, `AccountsFor` undefined.

- [ ] **Step 3: Confirm the naming is already settled**

`ledger.Asset` in the tests above is the `AccountType` constant, unchanged.
The registry row is `ledger.AssetDef`, named that way in Task 2 precisely
because the two cannot both be called `Asset` in one package.

Run: `go build ./...`
Expected: PASS — nothing to change here; this step exists so the distinction
is not discovered halfway through Step 5.

- [ ] **Step 4: Add the asset to `ledger.Account`**

In `ledger/types.go`, extend `Account`:

```go
// Account is a financial account within a subledger.
//
// Asset is fixed at creation and never changes: an account number and its
// asset are inseparable, the same way an IBAN is per-currency. That is what
// keeps a balance a scalar and what lets Post check the double-entry
// invariant per asset.
type Account struct {
	ID          AccountID
	SubledgerID SubledgerID
	Name        string
	Type        AccountType
	Asset       AssetCode
	CreatedAt   time.Time
}
```

In `ledger/book.go`, change both signatures and add the existence check after the subledger check in `CreateAccountTx`:

```go
	if _, err := tx.GetSubledger(ctx, s.id, subledgerID); err != nil {
		return Account{}, err
	}
	// The asset must already be registered in this book. There is no default:
	// silently falling back to a base currency is precisely the bug this
	// dimension exists to prevent.
	if _, err := tx.GetAsset(ctx, s.id, asset); err != nil {
		return Account{}, err
	}
```

and set `Asset: asset` in the `Account` literal.

- [ ] **Step 5: Add the asset to `deposit.Account`**

In `deposit/types.go`, extend `Account`:

```go
// Account is a customer demand-deposit account. It wraps a backing Liability
// account in the general ledger (GLAccount): the GL book balance of that
// account is the customer's money.
//
// Asset duplicates the backing GL account's asset. That is a deliberate
// exception to deriving rather than duplicating: the GL account's asset is
// immutable, so the two cannot drift, and deriving it would make ListAccounts
// an N+1 lookup in store/mem and a join in store/pg — divergent complexity in
// both stores for a value that cannot change. store/storetest asserts they
// always agree.
//
// A customer holding several assets holds several accounts, each with its own
// IBAN, which is how most European retail banks work.
type Account struct {
	ID             AccountID
	GLAccount      ledger.AccountID
	Name           string
	Asset          ledger.AssetCode
	Status         AccountStatus
	OverdraftLimit ledger.Amount
	CreatedAt      time.Time
}
```

- [ ] **Step 6: Thread the asset through `deposit.Register`**

In `deposit/register.go`, add `asset ledger.AssetCode` to both signatures, pass it straight through to `CreateAccountTx`, and set `Asset: asset` in the `Account` literal.

In `payment/participant.go`, `OpenCustomerAccount` gains the parameter and forwards it:

```go
func (p *Participant) OpenCustomerAccount(ctx context.Context, name string, asset ledger.AssetCode) (deposit.Account, error) {
	return p.Deposit.OpenAccount(ctx, p.CustomerSubledger, name, asset, 0)
}
```

- [ ] **Step 7: Restructure `Participant`**

In `payment/participant.go`, replace the three flat fields:

```go
// ParticipantAccounts are the internal accounts a participant needs for one
// asset:
//
//   - Suspense (Liability): an in-transit account holding funds that have left
//     a customer but not yet settled between banks. Returns to zero once a
//     cycle settles.
//   - Reserve (Asset): the bank's claim on the central bank. It mirrors the
//     bank's reserve account in the central-bank ledger and moves only at
//     settlement.
//   - Settlement: this participant's reserve account in the central-bank
//     ledger — the central bank's "vostro" view of the bank.
type ParticipantAccounts struct {
	Suspense   ledger.AccountID
	Reserve    ledger.AccountID
	Settlement ledger.AccountID
}
```

and on `Participant`:

```go
	// Assets holds one set of internal accounts per asset the participant
	// operates in, keyed by asset code.
	//
	// Keyed rather than flat because every one of those accounts is
	// denominated in exactly one asset: a bank clearing both euro and dollar
	// schemes needs two suspense accounts and two reserve accounts, not two
	// currencies inside one. Adding a scheme in a new asset is then a data
	// change rather than a code change.
	Assets map[ledger.AssetCode]ParticipantAccounts
```

Add the lookup:

```go
// AccountsFor returns the participant's internal accounts for an asset.
//
// Returns ErrParticipantAssetNotFound if the participant does not operate in
// that asset. There is deliberately no fallback to a base currency: settling a
// dollar cycle through a euro reserve account would be a silent accounting
// error rather than a visible failure.
func (p *Participant) AccountsFor(asset ledger.AssetCode) (ParticipantAccounts, error) {
	accts, ok := p.Assets[asset]
	if !ok {
		return ParticipantAccounts{}, fmt.Errorf("%w: %s in %s", ErrParticipantAssetNotFound, asset, p.Name)
	}
	return accts, nil
}
```

Add the sentinel to `payment/errors.go`:

```go
	// ErrParticipantAssetNotFound is returned when a participant does not
	// operate in an asset it is being asked to settle in.
	ErrParticipantAssetNotFound = errors.New("participant does not hold accounts in this asset")
```

- [ ] **Step 8: Create the participant's accounts per asset in `AddParticipant`**

In `payment/system.go`, around lines 283–309, wrap the three `CreateAccountTx` calls in a loop over the requested assets. Register the asset in both the participant's book and the central bank's book first — an account cannot reference an unregistered asset (Task 3):

```go
	if len(assets) == 0 {
		assets = []ledger.AssetCode{"EUR"}
	}

	accounts := make(map[ledger.AssetCode]ParticipantAccounts, len(assets))
	for _, asset := range assets {
		def, err := s.assetDef(asset)
		if err != nil {
			return Participant{}, err
		}
		// The asset has to exist in both books: the bank holds its own
		// suspense and reserve accounts, and the central bank holds the
		// matching vostro account.
		if err := ensureAsset(ctx, tx, bank, def); err != nil {
			return Participant{}, err
		}
		if err := ensureAsset(ctx, tx, s.centralBank, def); err != nil {
			return Participant{}, err
		}

		suspense, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Clearing Suspense ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return Participant{}, err
		}
		reserve, err := bank.CreateAccountTx(ctx, tx, interbank.ID, "Reserve at Central Bank ("+string(asset)+")", ledger.Asset, asset)
		if err != nil {
			return Participant{}, err
		}
		cbReserve, err := s.centralBank.CreateAccountTx(ctx, tx, reserveSubledger, "Reserve: "+name+" ("+string(asset)+")", ledger.Liability, asset)
		if err != nil {
			return Participant{}, err
		}
		accounts[asset] = ParticipantAccounts{Suspense: suspense.ID, Reserve: reserve.ID, Settlement: cbReserve.ID}
	}
```

Add the two helpers next to `AddParticipant`:

```go
// assetDef returns the definition for a well-known asset code. The network
// needs it because it creates accounts in books it does not otherwise
// populate, and an account cannot reference an unregistered asset.
func (s *Network) assetDef(code ledger.AssetCode) (ledger.AssetDef, error) {
	switch code {
	case "EUR":
		return ledger.AssetDef{Code: "EUR", Name: "Euro", Scale: 2, Class: ledger.Fiat}, nil
	case "USD":
		return ledger.AssetDef{Code: "USD", Name: "US Dollar", Scale: 2, Class: ledger.Fiat}, nil
	default:
		return ledger.AssetDef{}, fmt.Errorf("%w: %s", ledger.ErrAssetNotFound, code)
	}
}

// ensureAsset registers an asset in a book if it is not registered already.
// Idempotent, because several participants join the same central-bank book.
func ensureAsset(ctx context.Context, tx Tx, book *ledger.Book, def ledger.AssetDef) error {
	_, err := book.CreateAssetTx(ctx, tx, def.Code, def.Name, def.Scale, def.Class)
	if errors.Is(err, ledger.ErrDuplicateAsset) {
		return nil
	}
	return err
}
```

Naming the accounts with the asset in parentheses keeps them distinguishable in a chart of accounts that now holds several of each.

- [ ] **Step 9: Update every reader of the old flat participant fields**

Run: `go build ./...` and replace each `p.SuspenseAccount` / `p.ReserveAccount` / `p.SettlementAccount` with an `AccountsFor(asset)` call. The asset comes from:
- **funding** (`payment/system.go:326-371`): add an `asset ledger.AssetCode` parameter to the funding entry point, defaulting to `"EUR"` at its API/seed callers.
- **the payment path**: `scheme.Asset()`.
- **`SettleCycle`**: the cycle's scheme's asset, resolved once at the top and used for every participant in the batch. A participant missing it fails the whole batch — which is already how an underfunded member behaves.

- [ ] **Step 10: Update the remaining callers**

Run: `go build ./...` and fix what is left:

- `payment/participant.go` — `OpenCustomerAccount` gains the asset parameter and forwards it:

```go
func (p *Participant) OpenCustomerAccount(ctx context.Context, name string, asset ledger.AssetCode) (deposit.Account, error) {
	return p.Deposit.OpenAccount(ctx, p.CustomerSubledger, name, asset, 0)
}
```

- `seed/seed.go` — register EUR in each book it builds, immediately after that book's ledger is created and before any account, with `CreateAsset(ctx, "EUR", "Euro", 2, ledger.Fiat)`. Pass `"EUR"` at every account-opening site and `[]ledger.AssetCode{"EUR"}` to `AddParticipant`. The seeded network stays euro-only, so the demo data is unchanged in substance.
- `api/handlers_ledger.go` and `api/handlers_deposit.go` — `asset` becomes a **required** field on the create-account and open-deposit-account request bodies, returning 400 when absent. There is no default: silently falling back to a base currency is the bug this dimension exists to prevent. Add the field to the request structs in `api/dto_ledger.go` and `api/dto_deposit.go`.
- the remaining tests across `ledger`, `deposit`, `payment` and `api` — pass `"EUR"` and register the asset in each test's setup helper.

Add an API test to `api/server_test.go`, matching its existing helper names:

```go
func TestCreateAccountRequiresAsset(t *testing.T) {
	srv := newServer(t)

	res := srv.post(t, "/participants/alpha/accounts", map[string]any{
		"subledgerId": "cust", "name": "No Asset", "type": "Liability",
	})
	if res.Code != http.StatusBadRequest {
		t.Errorf("POST account without asset = %d, want 400", res.Code)
	}
}
```

- [ ] **Step 11: Add the three migrations**

Create `store/pg/schema/0003_account_asset.sql`:

```sql
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
ALTER TABLE accounts ADD COLUMN asset TEXT;

INSERT INTO assets (book_id, code, name, scale, class)
SELECT id, 'EUR', 'Euro', 2, 0 FROM books
ON CONFLICT (book_id, code) DO NOTHING;

UPDATE accounts SET asset = 'EUR' WHERE asset IS NULL;

ALTER TABLE accounts ALTER COLUMN asset SET NOT NULL;
ALTER TABLE accounts ADD CONSTRAINT accounts_asset_fkey
    FOREIGN KEY (book_id, asset) REFERENCES assets (book_id, code);
```

`class` is `0` because `Fiat` is the zero value of `AssetClass`.


Create `store/pg/schema/0004_deposit_account_asset.sql`:

```sql
-- A deposit account's asset, duplicated from its backing GL account.
--
-- This is the one place the schema stores a fact twice on purpose. The GL
-- account's asset is fixed at creation, so the two cannot drift, and deriving
-- it would turn every listing of deposit accounts into a join for a value that
-- can never change. store/storetest asserts the two always agree.
--
-- Existing rows are EUR by construction, like every other backfill in this
-- series of migrations.
ALTER TABLE deposit_accounts ADD COLUMN asset TEXT;

UPDATE deposit_accounts SET asset = 'EUR' WHERE asset IS NULL;

ALTER TABLE deposit_accounts ALTER COLUMN asset SET NOT NULL;
ALTER TABLE deposit_accounts ADD CONSTRAINT deposit_accounts_asset_fkey
    FOREIGN KEY (book_id, asset) REFERENCES assets (book_id, code);
```


Create `store/pg/schema/0005_participant_assets.sql`:

```sql
-- A participant's internal accounts, one set per asset it operates in.
--
-- These were three columns on participants. They move to a child table because
-- each of those accounts is denominated in exactly one asset: a bank clearing
-- both a euro and a dollar scheme needs two suspense accounts and two reserve
-- accounts, not two currencies inside one. Keying by (participant, asset) makes
-- adding a scheme in a new asset a data change rather than a schema change.
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
```

Check the real column names in `store/pg/schema/0001_init.sql` at the `participants` table (line 226) before writing this — they may be `suspense_account` or another spelling, and the `INSERT ... SELECT` must match exactly.

There is no foreign key from `participant_assets.asset` to `assets`, because a participant row is keyed by participant ID while assets are keyed by book. Note that in the comment, in the same spirit as the existing note on why the audit table has no foreign key.

- [ ] **Step 12: Update the pg store**

In `store/pg/tx_ledger.go`, add `asset` to `PutAccount`'s insert, update-set and both read queries:

```go
	_, err := t.tx.Exec(ctx, `
		INSERT INTO accounts (book_id, id, subledger_id, name, type, asset, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (book_id, id) DO UPDATE
		SET subledger_id = EXCLUDED.subledger_id, name = EXCLUDED.name,
		    type = EXCLUDED.type, asset = EXCLUDED.asset, created_at = EXCLUDED.created_at`,
		string(book), string(a.ID), string(a.SubledgerID), a.Name, int16(a.Type), string(a.Asset), nullTime(a.CreatedAt))
```

and add `asset` to the `SELECT` column lists and `Scan` targets in `GetAccount` and `ListAccounts`.

`store/mem` needs no change — it stores the whole `Account` value.


In `store/pg/tx_deposit.go`, add `asset` to `PutDepositAccount`'s insert and update-set, and to the `SELECT` lists and `Scan` targets of `GetDepositAccount` and `ListDepositAccounts`.


`store/pg/tx_payment.go`: `PutParticipant` writes the parent row then replaces the child rows (`DELETE` then `INSERT`, so an upsert cannot leave a stale asset behind); `GetParticipant` and `ListParticipants` load the child rows into the `Assets` map. Loading every participant's assets in one extra query keyed by participant ID avoids an N+1 in `ListParticipants`.

`store/mem/tx_payment.go`: `Participant` is stored whole, so the map travels with it — but **copy the map on write and on read**, or two callers will share one map and mutate each other's state. The rest of the mem store stores value types for exactly this reason.

- [ ] **Step 13: Update the mem store**

`store/mem` needs no change for accounts or deposit accounts — it stores those
whole values.

`Participant` is stored whole, so the map travels with it — but **copy the map on write and on read**, or two callers will share one map and mutate each other's state. The rest of the mem store stores value types for exactly this reason.

- [ ] **Step 14: Add the conformance cases**

In `store/storetest/storetest.go`, inside `RunLedger`:

```go
	t.Run("AccountRoundTripsItsAsset", func(t *testing.T) {
		s := open(t, newStore)

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			if err := tx.PutAsset(ctx, bookA, ledger.AssetDef{Code: "BTC", Name: "Bitcoin", Scale: 8, Class: ledger.Crypto}); err != nil {
				return err
			}
			return tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "100.custody.001", SubledgerID: "custody", Name: "Custody",
				Type: ledger.Asset, Asset: "BTC",
			})
		})

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			got, err := tx.GetAccount(ctx, bookA, "100.custody.001")
			if err != nil {
				return err
			}
			if got.Asset != "BTC" {
				t.Errorf("account asset = %q, want BTC", got.Asset)
			}
			list, err := tx.ListAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			if len(list) != 1 || list[0].Asset != "BTC" {
				t.Errorf("ListAccounts = %+v, want one BTC account", list)
			}
			return nil
		})
	})
```


In `store/storetest/deposit.go`, inside `RunDeposit`:

```go
	t.Run("DepositAccountAssetMatchesItsGLAccount", func(t *testing.T) {
		s := open(t, newStore)

		update(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutAsset(ctx, bookA, ledger.AssetDef{Code: "BTC", Name: "Bitcoin", Scale: 8, Class: ledger.Crypto}); err != nil {
				return err
			}
			if err := tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "200.cust.001", SubledgerID: "cust", Name: "Anna",
				Type: ledger.Liability, Asset: "BTC",
			}); err != nil {
				return err
			}
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", GLAccount: "200.cust.001", Name: "Anna", Asset: "BTC",
			})
		})

		view(t, s, func(ctx context.Context, tx deposit.Tx) error {
			dep, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			gl, err := tx.GetAccount(ctx, bookA, dep.GLAccount)
			if err != nil {
				return err
			}
			if dep.Asset != gl.Asset {
				t.Errorf("deposit asset %q != GL asset %q", dep.Asset, gl.Asset)
			}
			return nil
		})
	})
```

Match the helper names and `bookA` constant that `store/storetest/deposit.go` already uses.


In `store/storetest/payment.go`, inside `RunPayment`:

```go
	t.Run("ParticipantAssetsRoundTripAndReplaceOnUpsert", func(t *testing.T) {
		s := open(t, newStore)

		update(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutParticipant(ctx, payment.Participant{
				ID: "alpha", Name: "Alpha", BookID: "alpha",
				Assets: map[ledger.AssetCode]payment.ParticipantAccounts{
					"EUR": {Suspense: "200.ib.001", Reserve: "100.ib.001", Settlement: "200.res.001"},
					"USD": {Suspense: "200.ib.002", Reserve: "100.ib.002", Settlement: "200.res.002"},
				},
			})
		})

		view(t, s, func(ctx context.Context, tx payment.Tx) error {
			got, err := tx.GetParticipant(ctx, "alpha")
			if err != nil {
				return err
			}
			if len(got.Assets) != 2 {
				t.Fatalf("participant has %d assets, want 2", len(got.Assets))
			}
			if got.Assets["USD"].Reserve != "100.ib.002" {
				t.Errorf("USD reserve = %q, want 100.ib.002", got.Assets["USD"].Reserve)
			}
			return nil
		})

		// An upsert must replace the set, not merge into it: a stale asset
		// left behind would settle through an account the participant no
		// longer holds.
		update(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutParticipant(ctx, payment.Participant{
				ID: "alpha", Name: "Alpha", BookID: "alpha",
				Assets: map[ledger.AssetCode]payment.ParticipantAccounts{
					"EUR": {Suspense: "200.ib.001", Reserve: "100.ib.001", Settlement: "200.res.001"},
				},
			})
		})

		view(t, s, func(ctx context.Context, tx payment.Tx) error {
			got, err := tx.GetParticipant(ctx, "alpha")
			if err != nil {
				return err
			}
			if len(got.Assets) != 1 {
				t.Errorf("after upsert participant has %d assets, want 1", len(got.Assets))
			}
			return nil
		})
	})
```

- [ ] **Step 15: Run the tests**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 16: Verify against Postgres**

Run: `make test-pg`
Expected: PASS

- [ ] **Step 17: Run the app**

Run: `make run`, open the web app, and confirm a participant page loads with its
accounts and a payment can be initiated end to end. The seeded euro network must
behave exactly as it did before.

- [ ] **Step 18: Commit**

```bash
git add -A
git commit -m "Denominate accounts, deposit accounts and reserves in an asset

One commit because it cannot be split: the moment CreateAccount requires
an asset, every caller must supply a real one, and splitting would mean
temporary hardcoded currencies at the seams.

The asset lives on the account and not on the entry — an entry's asset is
always its account's. Participants hold one set of suspense, reserve and
settlement accounts per asset, so settlement resolves them by the cycle's
asset and fails when a member does not hold it, rather than falling back
to EUR. Existing rows backfill to EUR, which is exact: the system had no
other asset."
```

---

### Task 4: Transactions balance per asset

The invariant this whole sub-project exists for.

**Files:**
- Modify: `ledger/book.go` (`validateBalance`, its call site in `PostTx`)
- Modify: `ledger/errors.go` (`ErrUnbalancedAsset`)
- Modify: `ledger/book_test.go` or `ledger/asset_test.go`

**Interfaces:**
- Consumes: `Account.Asset` (Task 3).
- Produces:
  - `func validateBalance(entries []Entry, accounts map[AccountID]Account) error` (unexported)
  - `ErrUnbalancedAsset` — returned when one asset's debits and credits do not net to zero, returned wrapped **together with** `ErrUnbalancedTransaction` via `fmt.Errorf("%w: %w: %s", ErrUnbalancedTransaction, ErrUnbalancedAsset, code)` (Go 1.20+ multi-`%w`; this module is on Go 1.25). `errors.Is` therefore returns true for both sentinels and the message names the asset.

- [ ] **Step 1: Write the failing tests**

Add to `ledger/asset_test.go`:

```go
// A cross-asset transfer balances globally — 100 debit, 100 credit — and is
// still nonsense: it turns euros into bitcoin at an implied rate of 1. The
// per-asset rule is what rejects it.
func TestPostRejectsCrossAssetTransfer(t *testing.T) {
	book := newBook(t)
	ctx := context.Background()

	eur := newAccountIn(t, book, "EUR", ledger.Liability)
	btc := newAccountIn(t, book, "BTC", ledger.Liability)

	_, err := book.Post(ctx, ledger.PostRequest{
		Description: "turn euros into bitcoin",
		Entries: []ledger.Entry{
			{AccountID: eur.ID, Amount: 100, Direction: ledger.Debit},
			{AccountID: btc.ID, Amount: 100, Direction: ledger.Credit},
		},
	})
	if !errors.Is(err, ledger.ErrUnbalancedAsset) {
		t.Fatalf("cross-asset Post error = %v, want ErrUnbalancedAsset", err)
	}
	if !strings.Contains(err.Error(), "EUR") && !strings.Contains(err.Error(), "BTC") {
		t.Errorf("error %q does not name the offending asset", err)
	}
}

// The shape an FX trade takes under the per-asset rule: each asset balances
// through its own position account, so no rate is needed to validate it.
func TestPostAcceptsTwoAssetsBalancedThroughPositionAccounts(t *testing.T) {
	book := newBook(t)
	ctx := context.Background()

	eurCust := newAccountIn(t, book, "EUR", ledger.Liability)
	eurPos := newAccountIn(t, book, "EUR", ledger.Liability)
	btcCust := newAccountIn(t, book, "BTC", ledger.Liability)
	btcPos := newAccountIn(t, book, "BTC", ledger.Liability)

	if _, err := book.Post(ctx, ledger.PostRequest{
		Description: "FX: customer sells 100 EUR for 200 BTC units",
		Entries: []ledger.Entry{
			{AccountID: eurCust.ID, Amount: 100, Direction: ledger.Debit},
			{AccountID: eurPos.ID, Amount: 100, Direction: ledger.Credit},
			{AccountID: btcPos.ID, Amount: 200, Direction: ledger.Debit},
			{AccountID: btcCust.ID, Amount: 200, Direction: ledger.Credit},
		},
	}); err != nil {
		t.Fatalf("balanced two-asset Post: %v", err)
	}
}

// A capture that moves money into an account of a different asset needs no
// check in the deposit layer: the posting debits one asset and credits
// another, and the per-asset rule refuses it. This lives in deposit's tests
// because that is where the behaviour is observed, and it is the invariant
// paying for itself.
//
// Put this one in deposit/register_test.go, not here.
func TestCaptureHoldRejectsCrossAssetCounterparty(t *testing.T) {
	reg, book := newRegister(t)
	ctx := context.Background()
	sl := subledgerFor(t, book)

	acct, err := reg.OpenAccount(ctx, sl, "Anna EUR", "EUR", 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	fund(t, reg, book, acct, 10_000)

	btcGL, err := book.CreateAccount(ctx, sl, "Merchant BTC", ledger.Liability, "BTC")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	hold, err := reg.CreateHold(ctx, deposit.CreateHoldRequest{
		AccountID: acct.ID, Amount: 500, Description: "auth",
	})
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}

	_, err = reg.CaptureHold(ctx, hold.ID, btcGL.ID, 500, "capture into a BTC account")
	if !errors.Is(err, ledger.ErrUnbalancedAsset) {
		t.Errorf("cross-asset CaptureHold = %v, want ErrUnbalancedAsset", err)
	}
}

// The pre-existing single-asset failure must keep its own error.
func TestPostStillRejectsSingleAssetImbalance(t *testing.T) {
	book := newBook(t)
	ctx := context.Background()

	a := newAccountIn(t, book, "EUR", ledger.Liability)
	b := newAccountIn(t, book, "EUR", ledger.Liability)

	_, err := book.Post(ctx, ledger.PostRequest{
		Description: "lopsided",
		Entries: []ledger.Entry{
			{AccountID: a.ID, Amount: 100, Direction: ledger.Debit},
			{AccountID: b.ID, Amount: 90, Direction: ledger.Credit},
		},
	})
	if !errors.Is(err, ledger.ErrUnbalancedAsset) {
		t.Errorf("imbalanced Post error = %v, want ErrUnbalancedAsset", err)
	}
}
```

Add a helper `newAccountIn(t, book, asset, typ)` that registers the asset if it
is not registered yet (ignore `ErrDuplicateAsset`) with scale 2 for EUR and 8
for BTC, then creates an account in the test's subledger. Import `strings`.

The `CaptureHold` case goes in `deposit/register_test.go`, using that file's
existing setup helpers under their real names. Check `deposit.CreateHoldRequest`
in `deposit/register.go` around line 322 and `CaptureHold`'s signature at line
447 before writing the call.

Match `ledger.PostRequest`'s actual field names — check `ledger/book.go` around line 320 for the real struct.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./ledger/ ./deposit/ -run 'TestPost|TestCaptureHoldRejects' -v`
Expected: FAIL — `ErrUnbalancedAsset` undefined; the cross-asset case currently *passes* posting, which is the bug being fixed.

- [ ] **Step 3: Add the sentinel**

In `ledger/errors.go`:

```go
	// ErrUnbalancedAsset is returned when the debits and credits of one
	// asset within a transaction do not net to zero.
	//
	// This is the double-entry invariant restated for a multi-asset ledger.
	// A global check is not enough: a transaction debiting 100 EUR and
	// crediting 100 BTC balances by the old rule and still creates value out
	// of nothing. Balance has to hold per asset or it means nothing.
	//
	// It is returned wrapped with the offending asset code, so errors.Is
	// works and the message names the asset.
	ErrUnbalancedAsset = errors.New("transaction entries do not balance within an asset")
```

`ErrUnbalancedTransaction` is **not** removed and **not** left dangling — it
becomes the general case that `ErrUnbalancedAsset` specialises. Extend its
existing comment:

```go
	// ErrUnbalancedTransaction is returned when a transaction does not
	// balance. It is the general fact; ErrUnbalancedAsset names which asset
	// it failed in, and every per-asset failure wraps both, so a caller may
	// match on whichever level it cares about.
	//
	// It is not returned on its own. The empty case has its own sentinel
	// (ErrEmptyTransaction, guarded earlier in PostTx), and every other
	// imbalance is an imbalance within some asset.
	ErrUnbalancedTransaction = errors.New("transaction entries do not balance: total debits must equal total credits")
```

Do not delete it. It is exported, callers may already match on it, and it is
the name the README and the quiz use for the double-entry invariant.

- [ ] **Step 4: Rewrite the check**

In `ledger/book.go`, replace `validateBalance`:

```go
// validateBalance checks that total debits equal total credits within each
// asset. This is the core invariant of double-entry bookkeeping, restated for
// a ledger that holds more than one asset.
//
// Checking globally would not do. A transaction that debits 100 EUR and
// credits 100 BTC nets to zero by the old rule while turning euros into
// bitcoin at an implied rate of 1 — value created from nothing. Per asset,
// there is no rate to get wrong, which is why the ledger never has to know
// what anything is worth.
//
// An FX trade therefore cannot be one naive two-asset posting. Each asset
// balances through its own position account, and the bank's open exposure is
// the balance of those accounts.
func validateBalance(entries []Entry, accounts map[AccountID]Account) error {
	// net[asset] is debits minus credits in that asset.
	net := make(map[AssetCode]Amount, 2)
	// order preserves first-appearance order, so the error names the same
	// asset every time rather than whichever one the map iterated to first.
	order := make([]AssetCode, 0, 2)

	for _, e := range entries {
		asset := accounts[e.AccountID].Asset
		if _, seen := net[asset]; !seen {
			order = append(order, asset)
		}
		if e.Direction == Debit {
			net[asset] += e.Amount
		} else {
			net[asset] -= e.Amount
		}
	}

	for _, asset := range order {
		if net[asset] != 0 {
			return fmt.Errorf("%w: %w: %s", ErrUnbalancedTransaction, ErrUnbalancedAsset, asset)
		}
	}
	return nil
}
```

- [ ] **Step 5: Update the call site**

In `ledger/book.go`, in `PostTx`, the `// Validate: balanced.` block:

```go
	// Validate: balanced within every asset. accounts was already loaded
	// above for the sufficient-balance check, so this costs no extra reads.
	if err := validateBalance(req.Entries, accounts); err != nil {
		return Transaction{}, err
	}
```

- [ ] **Step 6: Fix `ledger/export_test.go` if it exports `validateBalance`**

Run: `go build ./... && go vet ./...`
Expected: PASS. If `export_test.go` re-exports `validateBalance`, update its signature to match.

- [ ] **Step 7: Run the tests**

Run: `go test ./...`
Expected: PASS. Existing single-asset tests asserting `ErrUnbalancedTransaction` keep passing unchanged, because the per-asset error wraps it — that is the point of the double wrap. Do **not** weaken the new check to make anything pass.

- [ ] **Step 8: Verify against Postgres**

Run: `make test-pg`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "Balance transactions per asset, not globally

A global debit/credit check passes a transaction that debits 100 EUR and
credits 100 BTC — value created from nothing at an implied rate of 1.
Per asset there is no rate to get wrong, which is how the ledger stays
ignorant of what anything is worth."
```

---

### Task 5: Schemes declare their asset

**Files:**
- Modify: `payment/scheme.go` (`Scheme` interface, both SEPA schemes)
- Modify: `payment/errors.go` (`ErrAssetMismatch`)
- Modify: `payment/system.go` (validation on the initiate/accept path)
- Modify: `payment/system_test.go`

**Interfaces:**
- Consumes: `deposit.Account.Asset` (Task 3).
- Produces:
  - `Scheme` gains `Asset() ledger.AssetCode`.
  - `ErrAssetMismatch` — a payment whose debtor or creditor account is not in the scheme's asset.
  - `SchemeSEPACT` and `SchemeSEPADD` both return `"EUR"`.

- [ ] **Step 1: Write the failing test**

Add to `payment/system_test.go`:

```go
// The ledger cannot catch this. A EUR debit and a BTC credit each balance
// within their own asset, so the posting is valid double-entry — it is merely
// meaningless. Per-asset balancing guarantees no value is created, not that a
// payment is coherent, so the scheme has to check.
func TestPaymentRejectsAccountNotInSchemeAsset(t *testing.T) {
	net := newNetwork(t)
	ctx := context.Background()

	alpha := addParticipant(t, net, "Alpha")
	beta := addParticipant(t, net, "Beta")

	registerAsset(t, beta, "BTC", 8, ledger.Crypto)

	from, err := alpha.OpenCustomerAccount(ctx, "Anna", "EUR")
	if err != nil {
		t.Fatalf("OpenCustomerAccount: %v", err)
	}
	to, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	if err != nil {
		t.Fatalf("OpenCustomerAccount: %v", err)
	}

	_, err = net.InitiatePayment(ctx, paymentRequest(t, payment.SchemeSEPACT, alpha, from, beta, to, 1_000))
	if !errors.Is(err, payment.ErrAssetMismatch) {
		t.Errorf("cross-asset payment = %v, want ErrAssetMismatch", err)
	}
}

func TestSEPASchemesAreEuroSchemes(t *testing.T) {
	for _, id := range []payment.SchemeID{payment.SchemeSEPACT, payment.SchemeSEPADD} {
		sc := schemeByID(t, id)
		if sc.Asset() != "EUR" {
			t.Errorf("%s asset = %q, want EUR", id, sc.Asset())
		}
	}
}
```

Read `payment/system_test.go` first and reuse its existing network/participant/payment-request helpers rather than the placeholder names above. `paymentRequest` must build whatever `InitiatePayment` actually takes — check `payment/system.go` for the real request type and `PartyRef` shape.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./payment/ -run 'TestPaymentRejectsAccountNotInSchemeAsset|TestSEPASchemesAreEuro' -v`
Expected: FAIL — `Asset()` undefined on `Scheme`, `ErrAssetMismatch` undefined.

- [ ] **Step 3: Extend the interface**

In `payment/scheme.go`, in the `Scheme` interface after `ID()`:

```go
	// Asset is the unit the scheme settles in. Both legs of a payment must be
	// denominated in it.
	//
	// This is a property of the scheme, not a limitation of the ledger: SEPA
	// is a euro scheme. A cross-currency payment is not one payment at all —
	// it is a payment plus an FX trade.
	Asset() ledger.AssetCode
```

Implement it on both SEPA scheme types, returning `"EUR"`.

- [ ] **Step 4: Add the sentinel**

In `payment/errors.go`:

```go
	// ErrAssetMismatch is returned when a payment's debtor or creditor
	// account is not denominated in the scheme's asset.
	//
	// The ledger cannot catch this: a EUR debit and a BTC credit each balance
	// within their own asset, so the posting is valid double-entry and simply
	// meaningless. Per-asset balancing proves no value was created, not that
	// a payment makes sense.
	ErrAssetMismatch = errors.New("payment accounts are not denominated in the scheme's asset")
```

- [ ] **Step 5: Validate on the payment path**

In `payment/system.go`, in the function that resolves both parties before a payment is accepted (the same place `validateFunds` is reached from), add — after both deposit accounts have been fetched:

```go
	if debtorAccount.Asset != scheme.Asset() || creditorAccount.Asset != scheme.Asset() {
		return ErrAssetMismatch
	}
```

If the creditor's account is fetched from a different participant's book, fetch it the same way the creditor leg already does at settlement; the check must cover **both** legs, not only the debtor's.

- [ ] **Step 6: Run the tests**

Run: `go test ./payment/ -v`
Expected: PASS

- [ ] **Step 7: Run the whole suite, both stores**

Run: `go test ./... && make test-pg`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "Give payment schemes an asset and enforce it on both legs

SEPA is a euro scheme — a fact about the scheme, not a simplification.
The ledger cannot catch a EUR account paying a BTC account, because each
leg balances within its own asset, so the check lives here."
```

---

### Task 6: API

**Files:**
- Create: `api/handlers_asset.go`
- Modify: `api/dto_ledger.go`, `api/dto_deposit.go`, `api/dto_payment.go`, `api/dto.go`
- Modify: `api/handlers_ledger.go`, `api/handlers_deposit.go`, `api/handlers_participant.go`
- Modify: `api/server.go` (route registration, if routes are registered centrally)
- Modify: `api/server_test.go`

**Interfaces:**
- Consumes: everything from Tasks 2–5.
- Produces:
  - `GET /participants/{pid}/assets` → `[]assetDTO`
  - `POST /participants/{pid}/assets` → `assetDTO`
  - `assetDTO { code, name, scale, class }`
  - `asset` field on the account, deposit-account, entry and payment DTOs
  - `asset` required in the create-account and open-deposit-account request bodies
  - optional `assets` array on the create-participant body, defaulting to `["EUR"]`

- [ ] **Step 1: Write the failing tests**

Add to `api/server_test.go`, following its existing request/response helpers:

```go
func TestCreateAndListAssets(t *testing.T) {
	srv := newServer(t)

	created := srv.post(t, "/participants/alpha/assets", map[string]any{
		"code": "BTC", "name": "Bitcoin", "scale": 8, "class": "Crypto",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("POST /assets = %d, want 201: %s", created.Code, created.Body)
	}

	list := srv.get(t, "/participants/alpha/assets")
	if !strings.Contains(list.Body.String(), `"BTC"`) {
		t.Errorf("GET /assets did not list BTC: %s", list.Body)
	}
}

func TestAccountResponseIncludesAsset(t *testing.T) {
	srv := newServer(t)

	res := srv.get(t, "/participants/alpha/accounts")
	if !strings.Contains(res.Body.String(), `"asset":"EUR"`) {
		t.Errorf("account listing has no asset field: %s", res.Body)
	}
}
```

Read `api/server_test.go` first — reuse its actual helper names and the actual seeded participant ID rather than the placeholder `alpha`, and match how it asserts on JSON.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./api/ -run 'TestCreateAndListAssets|TestAccountResponseIncludesAsset' -v`
Expected: FAIL — 404 on the assets routes, and no `asset` field in responses.

- [ ] **Step 3: Add the asset DTO and handlers**

Create `api/handlers_asset.go`, following the structure of `api/handlers_ledger.go` exactly — same route-registration function shape, same error mapping, same `respond` helpers:

```go
package api

import (
	"net/http"

	"github.com/raphi011/cbs/ledger"
)

func (s *Server) registerAssetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /participants/{pid}/assets", s.handleCreateAsset)
	mux.HandleFunc("GET /participants/{pid}/assets", s.handleListAssets)
}

type assetDTO struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Scale uint8  `json:"scale"`
	Class string `json:"class"`
}

func newAssetDTO(a ledger.AssetDef) assetDTO {
	return assetDTO{Code: string(a.Code), Name: a.Name, Scale: a.Scale, Class: a.Class.String()}
}

type createAssetRequest struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Scale uint8  `json:"scale"`
	Class string `json:"class"`
}
```

Map `class` by name — `"Fiat"` → `ledger.Fiat`, `"Crypto"` → `ledger.Crypto`, anything else a 400 — so the wire format does not depend on an iota's numeric value. Mirror how the existing DTOs render `AccountType`; if they use the numeric value, match that instead and stay consistent within the package.

Wire `registerAssetRoutes` into wherever `registerLedgerRoutes` and friends are called.

- [ ] **Step 4: Add `asset` to the existing DTOs**

`asset` on the account **response** DTO, the deposit-account DTO, the entry DTO (from its account) and the payment DTO (from its scheme). The request-side `asset` field already landed in Task 3; do not add it twice. `assets` becomes an optional array on the create-participant body, defaulting to `["EUR"]`, forwarded to `AddParticipant`.

- [ ] **Step 5: Run the tests**

Run: `go test ./api/ -v`
Expected: PASS

- [ ] **Step 6: Run the whole suite, both stores**

Run: `go test ./... && make test-pg`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "Expose assets over the API

Assets are registered and listed per participant, matching the book-scoped
registry, and asset becomes a required field wherever an account is
created rather than defaulting to EUR behind the caller's back."
```

---

### Task 7: Web

**Files:**
- Modify: `web/src/lib/money.ts`
- Modify: `web/src/lib/types.ts`
- Modify: the account, balance and statement views that call the money formatters
- Modify: `web/src/lib/statement.test.ts` and any `money` test file

**Interfaces:**
- Consumes: the `asset` field on API responses (Task 6).
- Produces: money formatters that take a scale instead of assuming 2.

- [ ] **Step 1: Write the failing test**

In the money test file (create `web/src/lib/money.test.ts` if there is none, matching how `statement.test.ts` is written):

```ts
import { describe, expect, it } from "vitest";
import { formatAmount } from "./money";

describe("formatAmount", () => {
  it("formats a 2-decimal asset", () => {
    expect(formatAmount(12345, { code: "EUR", scale: 2 })).toBe("123.45");
  });

  it("formats an 8-decimal asset", () => {
    // The bug this replaces: dividing by 100 renders 1 BTC as 1,000,000.00.
    expect(formatAmount(100_000_000, { code: "BTC", scale: 8 })).toBe("1.00000000");
  });

  it("formats a 0-decimal asset", () => {
    expect(formatAmount(500, { code: "JPY", scale: 0 })).toBe("500");
  });
});
```

Match the actual test runner and import style the repo uses — check `web/src/lib/statement.test.ts` and `web/package.json` before writing this.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npm run test -- money`
Expected: FAIL — `formatAmount` does not take an asset.

- [ ] **Step 3: Make the formatters scale-driven**

Rewrite `web/src/lib/money.ts`. The three `/ 100` sites become `/ 10 ** asset.scale`, and the fraction-digit count passed to `Intl.NumberFormat` becomes `asset.scale` rather than a hardcoded 2. Update the file's header comment: amounts are integer minor units *of an asset*, and the asset's scale is what converts them to major units — 2 for EUR, 8 for BTC.

Keep the existing exported function names where they still fit; only their signatures change. Every call site gets the asset from the account or balance it is rendering.

- [ ] **Step 4: Add `asset` to the TypeScript types**

In `web/src/lib/types.ts`, add `asset` to the account, deposit-account, entry and payment types, and add an `Asset` type mirroring `assetDTO`. Update the file header comment, which currently says amounts are integer **cents**.

- [ ] **Step 5: Show the asset in the UI**

Account and balance views display the asset code next to the amount. Follow the existing component patterns; do not restyle anything.

- [ ] **Step 6: Run the tests**

Run: `cd web && npm run test`
Expected: PASS

- [ ] **Step 7: Load a page**

Run: `make dev`, open the app, and confirm balances render correctly and no route throws.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "Render amounts at their asset's scale

money.ts divided by 100 in three places, which renders 1 BTC as
1,000,000.00. The divisor is now the asset's scale."
```

---

### Task 8: Documentation

Per `CLAUDE.md` the domain content is duplicated across layers by design, and a change to one is a change to all. This task is part of the work, not follow-up.

**Files:**
- Modify: `README.md`
- Modify: `web/src/components/hint-content.ts`
- Create: `web/src/lib/quiz/chapters/16-multi-asset-accounting.ts`
- Modify: the quiz chapter index/registry that lists the chapters
- Modify: `docs/expansion-roadmap.md`

**Interfaces:**
- Consumes: the behaviour built in Tasks 1–7.
- Produces: chapter 16, registered in the quiz index; new `hint-content.ts` keys for every new `[[wiki-link]]`.

- [ ] **Step 1: Update the README**

- A new **Assets** section under *Accounting Foundations*: assets are book-scoped, an account is bound to one asset for life, and that is why a balance is a scalar.
- *Amounts and Precision*: the scale cap and the arithmetic behind it — `int64` tops out near 9.22 × 10¹⁸, which is 9.2 ETH at 18 decimals, so scale is capped at 9 and 18-decimal assets are held at reduced precision.
- *Double-Entry Bookkeeping*: the invariant is per asset. Include the 100 EUR / 100 BTC example — it balances globally and creates value from nothing.
- *Chart of Accounts*: accounts are per asset, so a bank operating in three currencies has three cash accounts.
- The payment sections: SEPA is a euro scheme; a participant holds one set of internal accounts per asset; a cross-currency payment is a payment plus an FX trade and is not supported.
- *Deliberate Simplifications*: replace "**A single currency**, using `ledger.Amount` (integer minor units). No FX." with the multi-asset reality and the remaining limits — no FX, no rates, and the 9-decimal scale cap.
- *Persistence*: the `assets` table, why the asset is on `accounts` and not `entries`, and why `participant_assets` is a child table.

- [ ] **Step 2: Add the hint keys**

Every new `[[wiki-link]]` used in the README-derived hints and in chapter 16 needs a key in `web/src/components/hint-content.ts`. Expect at least: `asset`, `asset-scale`, `per-asset-balance`, `fx-position-account`, `scheme-asset`.

An unresolved link throws under `RootLayout` and takes down **every** route in the dev app, while `next build` stays green — so this cannot be verified by building.

- [ ] **Step 3: Write chapter 16**

Create `web/src/lib/quiz/chapters/16-multi-asset-accounting.ts` following the structure of an existing chapter exactly. `web/src/lib/quiz/diversity.test.ts` requires:
- 18–22 questions
- at least 8 distinct `concept` tags
- no tag used more than 3 times
- all three difficulty tiers present

Material with enough substance to fill it honestly: why an account is single-asset; why balancing per asset is not the same as balancing globally; the 100 EUR / 100 BTC transaction that passes a global check; why the ledger does not know exchange rates; how an FX trade balances through position accounts; what the balance of a position account means; why `int64` caps scale at 9; why SEPA is a euro scheme; why a cross-currency payment is two operations; why suspense and reserve accounts are per asset.

Register it in whatever index file lists the chapters.

- [ ] **Step 4: Run the web tests**

Run: `cd web && npm run test`
Expected: PASS — including the diversity test and the wiki-link check.

- [ ] **Step 5: Load a page**

Run: `make dev`, open the app, visit the quiz and a page carrying the new hints. An unresolved link fails here and nowhere else.

- [ ] **Step 6: Update the roadmap**

In `docs/expansion-roadmap.md`, set sub-project 1 to `done` and add a log row. Note anything learned that changes the shape of sub-projects 2–4 — in particular whether the FX position-account pattern still looks right now that per-asset balancing is real.

- [ ] **Step 7: Run everything**

Run: `go test ./... && make test-pg && cd web && npm run test`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "Document multi-asset accounting across every layer

README, hints and quiz chapter 16. The domain content is duplicated by
design, so a correction in one layer is a correction in all of them."
```

---

## Done When

- `go test ./...` passes with no `DATABASE_URL`.
- `make test-pg` passes against Postgres.
- `cd web && npm run test` passes.
- `make dev` serves the app with no route throwing, and the seeded euro network behaves exactly as it did before.
- A book can register EUR and BTC, hold accounts in both, refuse a transaction that mixes them, and accept one that balances both through position accounts.
- A SEPA payment between a EUR account and a BTC account is refused with `ErrAssetMismatch`.
- No temporary or hardcoded asset remains at any call site: every account is created with an asset its caller chose.
