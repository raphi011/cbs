# Account Addressing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give a deposit account a set of external identifiers — `(scheme, value)`, with `IBAN` the one scheme shipped — so an IBAN can be resolved to the account it addresses, and make the payment scheme declare which identifier addresses it.

**Architecture:** Identifiers are part of the `deposit.Account` aggregate, written by the existing `PutDepositAccount` and read back with it. Uniqueness within a bank is a domain rule in `deposit.Register`, deliberately **not** a database constraint. `payment.Network.ResolveIdentifier` sweeps member banks and refuses an ambiguous answer. `payment.Scheme.AddressedBy()` names the identifier scheme, and `InitiatePaymentTx` enforces it in the same place the cross-asset check already runs.

**Tech Stack:** Go 1.x (standard library plus `pgx/v5`), Postgres (optional), Next.js + TypeScript for the web client.

**Spec:** `docs/superpowers/specs/2026-07-31-account-addressing-design.md`

## Global Constraints

- **Both stores must stay indistinguishable.** `go test ./...` with **no** `DATABASE_URL` runs everything on `store/mem`; `TEST_DATABASE_URL=… go test ./...` (or `make test-pg`) runs the same suites against `store/pg`. Both must be green at the end of every task that touches a store.
- **`store/pg` must never reject a write `store/mem` accepts.** This is why no `UNIQUE (book_id, scheme, value)` is added. See `store/pg/schema/0001_init.sql:38-61` and `:83-105`.
- **One migration.** All schema changes are folded into `store/pg/schema/0001_init.sql`. No `0002_*`. No database is deployed.
- **Domain knowledge is duplicated across four layers by design.** `README.md` is authoritative; `web/src/components/hint-content.ts`, `web/src/lib/quiz/chapters/*.ts` and the comments in `store/pg/schema/0001_init.sql` must move with it. See `CLAUDE.md`.
- **A `[[wiki-link]]` to a key that is not in `hint-content.ts` throws at runtime** under `RootLayout` and takes every route in the dev app down, while `next build` stays green. `cd web && npm run test` catches it. Run it, and load a page.
- **All new user-supplied text goes through `ledger.ValidateText`** before it reaches a store. A control character reaching `store/pg` raises a SQLSTATE instead of answering "no such row".
- **Identifier format is deliberately unvalidated.** No mod-97 / ISO 7064 check digit, no length rule, no country-code rule. The seed's readable `SE89-AURORA-1001` must stay legal.
- **Audit events:** every register mutation appends through `Register.appendAuditTx` (`deposit/register.go:102`), inside the same `Tx`.
- Commit messages follow the repository's existing style (`feat(deposit): …`, `fix(payment): …`, `docs: …`). End each with:
  ```
  Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
  ```

---

## File Structure

**Created:**
- `deposit/identifier.go` — the `IdentifierScheme` / `Identifier` value types and their validation. One responsibility: what an external address *is*.
- `deposit/identifier_test.go` — unit tests for the above.
- `deposit/register_identifier_test.go` — register-level tests for open/add/remove/resolve.
- `api/handlers_directory.go` — the `GET /directory` endpoint and the per-account identifier endpoints.

**Modified:**
- `deposit/types.go:71-77` — `Account` gains `Identifiers []Identifier`.
- `deposit/errors.go` — three new sentinels.
- `deposit/store.go:26-60` — `Tx` gains one read method; contract notes for it.
- `deposit/register.go:152-220` — `OpenAccount`/`OpenAccountTx` take variadic identifiers; new `AddIdentifier`, `RemoveIdentifier`, `ResolveIdentifier` (+ `…Tx` forms).
- `ledger/audit.go:38-43` — two new event-type constants.
- `store/mem/tx_deposit.go:25-49` — the new read; `Identifiers` round-trips for free (the whole `Account` is the map value).
- `store/pg/tx_deposit.go:31-130` — `PutDepositAccount` writes the child table; both readers hydrate; the new read.
- `store/pg/schema/0001_init.sql:207-221` — the child table, its index, and the `COMMENT ON TABLE` recording the two absent constraints.
- `store/storetest/deposit.go` — four new conformance subtests.
- `payment/types.go:163-167` — `PartyRef.IBAN` → `PartyRef.Identifier`.
- `payment/scheme.go:17-51, 97-130` — `Scheme.AddressedBy()`; `SCT` and `SDD` answer it.
- `payment/system.go:973-995, 1363-1375` — enforcement in `InitiatePaymentTx`; `validateParty`; new `ResolveIdentifier`.
- `payment/errors.go` — two new sentinels.
- `api/dto_payment.go:76-92` — `partyRefDTO`.
- `api/dto_deposit.go:27-58` — `depositAccountDTO` gains `identifiers`.
- `api/handlers_deposit.go:15-39` — three new routes.
- `api/errors.go:26-80` — five new mappings.
- `seed/seed.go:104-105, 160-165, 248-260, 292-293` — identifiers at open; the `ibans` map deleted.
- `web/src/lib/types.ts:300-304` — `PartyRef`.
- `web/src/components/forms/party-ref-fields.tsx:50-62` — the IBAN input.
- `README.md:1002` and a new subsection under *The Multi-Bank Model*.
- `payment/doc.go:64`.
- `web/src/components/hint-content.ts` — a new `account-addressing` key.

---

### Task 1: The `Identifier` value type

**Files:**
- Create: `deposit/identifier.go`
- Create: `deposit/identifier_test.go`
- Modify: `deposit/types.go:71-77` (the `Account` struct)
- Modify: `deposit/errors.go`

**Interfaces:**
- Consumes: `ledger.ValidateText(field, s string) error`.
- Produces: `deposit.IdentifierScheme` (defined string type), the constant `deposit.IdentifierIBAN`, `deposit.Identifier{Scheme IdentifierScheme; Value string}` with method `Validate(field string) error`, the field `deposit.Account.Identifiers []Identifier`, and the sentinels `deposit.ErrIdentifierTaken`, `deposit.ErrIdentifierNotFound`, `deposit.ErrIdentifierAmbiguous`.

- [ ] **Step 1: Write the failing test**

Create `deposit/identifier_test.go`:

```go
package deposit

import (
	"errors"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

func TestIdentifierValidate(t *testing.T) {
	cases := []struct {
		name    string
		ident   Identifier
		wantErr bool
	}{
		{"ok", Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}, false},
		{"empty scheme", Identifier{Value: "SE89-AURORA-1001"}, true},
		{"empty value", Identifier{Scheme: IdentifierIBAN}, true},
		{"control character in value", Identifier{Scheme: IdentifierIBAN, Value: "SE89\x00"}, true},
		{"control character in scheme", Identifier{Scheme: IdentifierScheme("IB\x00AN"), Value: "x"}, true},
		// Format is deliberately unvalidated: no check digit, no length rule.
		// The seed's readable pseudo-IBANs must stay legal.
		{"not a real IBAN but accepted", Identifier{Scheme: IdentifierIBAN, Value: "hello world"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ident.Validate("debtor.identifier")
			if c.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestIdentifierValidateReportsTheField(t *testing.T) {
	err := Identifier{Scheme: IdentifierIBAN, Value: "x\x00y"}.Validate("creditor.identifier")
	if err == nil {
		t.Fatal("Validate() = nil, want an error")
	}
	if !errors.Is(err, ledger.ErrInvalidText) {
		t.Fatalf("Validate() = %v, want it to wrap ledger.ErrInvalidText", err)
	}
}

func TestIdentifierEquality(t *testing.T) {
	// Identifier is a comparable struct on purpose: Register.RemoveIdentifier
	// and the uniqueness check both compare with ==, and a slice or a map in
	// this type would break that.
	a := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	b := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	if a != b {
		t.Fatal("identical identifiers compare unequal")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./deposit/ -run TestIdentifier -v`
Expected: FAIL — `undefined: Identifier`, `undefined: IdentifierIBAN`.

If `ledger.ErrInvalidText` does not exist, run `grep -n "ErrInvalidText\|func ValidateText" -A 6 ledger/*.go` and use whatever sentinel `ValidateText` actually returns; adjust the test's `errors.Is` target to match rather than inventing one.

- [ ] **Step 3: Write the implementation**

Create `deposit/identifier.go`:

```go
package deposit

import "github.com/raphi011/cbs/ledger"

// IdentifierScheme names a way of addressing an account from outside the bank.
//
// It is deliberately not the same thing as a payment scheme. "SEPA Credit
// Transfer" is a product; "IBAN" is the kind of address that product carries,
// and both SEPA schemes carry the same one.
type IdentifierScheme string

// IdentifierIBAN is the only scheme this system issues today.
//
// Others exist in the world and drop in as constants: a UK sort code plus
// account number, a US routing number plus account number, a card PAN (which is
// an alias for a funding account and nothing more), and the proxy aliases —
// phone, email — that instant schemes resolve through a central service. Only
// the last kind cannot live here; see the comment on Register.AddIdentifier.
const IdentifierIBAN IdentifierScheme = "IBAN"

// Identifier is one external address for a deposit account: the thing a
// counterparty quotes when it wants to pay it. An account's own AccountID is
// never one of these — that is the bank's internal key and is not quoted.
//
// This mirrors ISO 20022's CashAccountIdentification, which is a choice between
// an IBAN and a generic (identification, scheme name, issuer) triple rather
// than an IBAN field. Modelling it as a pair is what makes a card PAN a new
// constant instead of a schema change.
//
// The struct is comparable: it holds two strings and nothing else, so == is the
// identity test, which is what RemoveIdentifier and the uniqueness check use.
type Identifier struct {
	Scheme IdentifierScheme
	Value  string
}

// Validate checks that both halves are present and free of control characters.
//
// It does NOT check the format of the value. There is no mod-97 check digit on
// an IBAN here and no length rule, because enforcing one would make the seed's
// readable SE89-AURORA-1001 illegal and replace it with opaque digits in every
// worked example in the repository. Format is a per-scheme concern this system
// deliberately does not implement; see the design spec.
func (i Identifier) Validate(field string) error {
	if err := ledger.ValidateText(field+".scheme", string(i.Scheme)); err != nil {
		return err
	}
	if i.Scheme == "" {
		return ledger.ValidateText(field+".scheme", "")
	}
	if i.Value == "" {
		return ledger.ValidateText(field+".value", "")
	}
	return ledger.ValidateText(field+".value", i.Value)
}
```

If `ledger.ValidateText` **accepts** the empty string (check with `grep -n "func ValidateText" -A 15 ledger/*.go`), replace the two `if … == ""` branches with an explicit error instead:

```go
	if i.Scheme == "" || i.Value == "" {
		return fmt.Errorf("%s: an identifier needs both a scheme and a value: %w", field, ledger.ErrInvalidText)
	}
```

and import `fmt`. Pick one form, do not ship both.

- [ ] **Step 4: Add the field to `Account`**

In `deposit/types.go`, inside the `Account` struct (after `Status`, before `Accrued`):

```go
	// Identifiers are this account's external addresses — what a counterparty
	// quotes to pay it. Zero is normal: an account nobody pays from outside the
	// bank needs no address at all.
	//
	// They are part of the account aggregate rather than a sibling entity, and
	// that is load-bearing in store/pg: PutDepositAccount writes both sides
	// itself, which is the one condition under which the schema allows a real
	// FOREIGN KEY (see store/pg/schema/0001_init.sql).
	Identifiers []Identifier
```

- [ ] **Step 5: Add the sentinels**

In `deposit/errors.go`, following the existing comment-per-error style:

```go
	// ErrIdentifierTaken is returned when an account is given an identifier
	// another account at the SAME bank already holds. The check spans one
	// register, because that is the widest scope a register can see — and it is
	// also the right scope: a bank-issued identifier is globally unique by
	// construction (an IBAN carries its own bank code, a PAN its BIN), so two
	// banks cannot collide without one of them issuing addresses it was never
	// allocated.
	ErrIdentifierTaken = errors.New("identifier already in use at this bank")

	// ErrIdentifierNotFound is returned when no account holds the identifier.
	ErrIdentifierNotFound = errors.New("identifier not found")

	// ErrIdentifierAmbiguous is returned when more than one account holds it.
	//
	// This is refused rather than resolved to the first hit, for the reason
	// settlement refuses to default a cycle's asset: an address that resolves
	// to two accounts is not an address, and guessing quietly in the layer that
	// decides where money goes is the failure worth having a sentinel for. It
	// is also what closes the within-bank race that comes with enforcing
	// uniqueness in the domain rather than with a constraint.
	ErrIdentifierAmbiguous = errors.New("identifier resolves to more than one account")
```

- [ ] **Step 6: Run the tests**

Run: `go test ./deposit/ -run TestIdentifier -v`
Expected: PASS, all subtests.

Then `go build ./...` — expected to succeed; the new `Account` field is additive and no call site sets it yet.

- [ ] **Step 7: Commit**

```bash
git add deposit/identifier.go deposit/identifier_test.go deposit/types.go deposit/errors.go
git commit -m "$(cat <<'EOF'
feat(deposit): add the account identifier value type

An external address is a (scheme, value) pair, not an iban field. ISO
20022 models it the same way — CashAccountIdentification is a choice
between an IBAN and a generic triple — and it is what makes a later card
PAN a new constant rather than a schema change.

Format is deliberately unvalidated. A mod-97 check digit would make the
seed's readable SE89-AURORA-1001 illegal and put opaque digits in every
worked example in the repository, which costs more teaching than the
check buys.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Persistence, in both stores, with conformance

**Files:**
- Modify: `deposit/store.go:26-60` (the `Tx` interface and the contract notes below it)
- Modify: `store/mem/tx_deposit.go:25-49`
- Modify: `store/pg/tx_deposit.go:31-130`
- Modify: `store/pg/schema/0001_init.sql:207-221`
- Test: `store/storetest/deposit.go`

**Interfaces:**
- Consumes: `deposit.Identifier`, `deposit.Account.Identifiers` (Task 1).
- Produces: `deposit.Tx.ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident Identifier) ([]Account, error)` — every account in the book holding that exact `(scheme, value)`, ordered `created_at, seq` like every other listing. Zero matches is an empty slice and a nil error, **not** a sentinel: resolving to exactly one account is a domain rule and this is a key/value layer.

- [ ] **Step 1: Write the failing conformance tests**

Append to `store/storetest/deposit.go`, inside `RunDeposit`, after the existing `DepositAccountRoundTripsAndIsBookScoped` subtest. Read that subtest first and reuse its helpers (`openDeposit`, `updateDeposit`, `viewDeposit`, `bookA`, `bookB`) exactly as it does — do not introduce new ones.

```go
	// Identifiers ride on the account aggregate: PutDepositAccount writes them
	// and both readers bring them back. If they did not, the register's
	// uniqueness check would pass against a store that had silently dropped
	// the very rows it was checking.
	t.Run("IdentifiersSurviveAccountRead", func(t *testing.T) {
		s := openDeposit(t, newStore)
		iban := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", GLAccount: "200.cust.001", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{iban},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			if len(got.Identifiers) != 1 || got.Identifiers[0] != iban {
				t.Fatalf("GetDepositAccount identifiers = %#v, want [%#v]", got.Identifiers, iban)
			}
			list, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			if len(list) != 1 || len(list[0].Identifiers) != 1 || list[0].Identifiers[0] != iban {
				t.Fatalf("ListDepositAccounts identifiers = %#v, want [%#v]", list, iban)
			}
			return nil
		})
	})

	// An upsert replaces the set rather than merging into it. PutDepositAccount
	// is an upsert of the whole aggregate everywhere else; identifiers must not
	// be the one part of it that accumulates.
	t.Run("IdentifiersAreReplacedByAnUpsert", func(t *testing.T) {
		s := openDeposit(t, newStore)
		first := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}
		second := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-9999"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{first},
			})
		})
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{second},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			if len(got.Identifiers) != 1 || got.Identifiers[0] != second {
				t.Fatalf("after upsert identifiers = %#v, want [%#v]", got.Identifiers, second)
			}
			return nil
		})
	})

	// The lookup is exact on both halves of the pair and scoped by book, like
	// every other method here. Two banks holding the same value is a legal
	// state, and each book sees only its own.
	t.Run("ListDepositAccountsByIdentifierIsExactAndBookScoped", func(t *testing.T) {
		s := openDeposit(t, newStore)
		iban := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SHARED-0001"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{iban},
			}); err != nil {
				return err
			}
			return tx.PutDepositAccount(ctx, bookB, deposit.Account{
				ID: "dep_2", Name: "Bruno", Asset: "EUR",
				Identifiers: []deposit.Identifier{iban},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			inA, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, iban)
			if err != nil {
				return err
			}
			if len(inA) != 1 || inA[0].ID != "dep_1" {
				t.Fatalf("book A lookup = %#v, want just dep_1", inA)
			}

			// Same value, different scheme: no match.
			other := deposit.Identifier{Scheme: deposit.IdentifierScheme("PAN"), Value: "SHARED-0001"}
			none, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, other)
			if err != nil {
				return err
			}
			if len(none) != 0 {
				t.Fatalf("wrong-scheme lookup = %#v, want none", none)
			}

			// A miss is an empty slice and a nil error, not a sentinel.
			missing := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "NOPE"}
			gone, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, missing)
			if err != nil {
				return err
			}
			if len(gone) != 0 {
				t.Fatalf("missing lookup = %#v, want none", gone)
			}
			return nil
		})
	})

	// The store does NOT enforce uniqueness, and this test is what keeps it
	// that way.
	//
	// It is the same job ParentReferencesAreNotEnforced does. "One bank issues
	// an address once" is a domain rule that deposit.Register enforces by
	// reading before it writes; a UNIQUE constraint in store/pg would make
	// Postgres reject a write the Go map accepts, under a race no lock closes,
	// which is the one divergence the store layer must never introduce. The
	// resulting ambiguity is caught at READ time by Register.ResolveIdentifier.
	t.Run("IdentifierUniquenessIsNotEnforced", func(t *testing.T) {
		s := openDeposit(t, newStore)
		iban := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{iban},
			}); err != nil {
				return err
			}
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_2", Name: "Aaron", Asset: "EUR",
				Identifiers: []deposit.Identifier{iban},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			both, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, iban)
			if err != nil {
				return err
			}
			if len(both) != 2 {
				t.Fatalf("duplicate lookup returned %d accounts, want 2 — the store must not enforce uniqueness", len(both))
			}
			return nil
		})
	})
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./store/... -run 'TestMem|TestPG' -v 2>&1 | head -40`
Expected: compile failure — `tx.ListDepositAccountsByIdentifier undefined`.

- [ ] **Step 3: Declare the method on `deposit.Tx`**

In `deposit/store.go`, in the `Tx` interface next to the other deposit-account methods:

```go
	// ListDepositAccountsByIdentifier returns every account in the book holding
	// this exact (scheme, value) pair.
	//
	// It returns a slice rather than one account and a not-found sentinel
	// because "an address resolves to exactly one account" is a DOMAIN rule and
	// this layer holds none — the same division that keeps parent-existence out
	// of the schema. Register.ResolveIdentifier turns zero, one and more than
	// one into ErrIdentifierNotFound, the account, and ErrIdentifierAmbiguous.
	ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident Identifier) ([]Account, error)
```

Then append to the "Contract notes for implementers" block at the foot of the file:

```go
//   - PutDepositAccount writes Account.Identifiers as part of the aggregate and
//     REPLACES the stored set; both readers bring it back. Identifiers are not
//     separately writable, which is the condition under which store/pg is
//     allowed a real FOREIGN KEY on them.
//   - ListDepositAccountsByIdentifier matches both halves of the pair exactly,
//     is book-scoped like everything else here, orders created_at then seq, and
//     returns an empty slice — never a sentinel — when nothing matches. It must
//     NOT enforce uniqueness: storetest/IdentifierUniquenessIsNotEnforced pins
//     that two accounts in one book may hold the same identifier, because the
//     rule against it lives in deposit.Register and a constraint in only one
//     store would make the two implementations disagree.
```

- [ ] **Step 4: Implement in `store/mem`**

In `store/mem/tx_deposit.go`, after `ListDepositAccounts`:

```go
// The whole Account — identifiers included — is the map value, so
// PutDepositAccount and both readers need no change at all. Only the lookup is
// new, and in a map it is a scan; there are four banks.
func (t *tx) ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident deposit.Identifier) ([]deposit.Account, error) {
	out := make([]deposit.Account, 0)
	for _, a := range t.state.depositAccounts[book] {
		for _, got := range a.Identifiers {
			if got == ident {
				out = append(out, a)
				break
			}
		}
	}
	sortRows(t.state, out, book, kindDepositAccount, func(a deposit.Account) (time.Time, string) { return a.CreatedAt, string(a.ID) })
	return out, nil
}
```

- [ ] **Step 5: Run the mem half**

Run: `go test ./store/mem/ -v 2>&1 | grep -i identifier`
Expected: all four subtests PASS.

- [ ] **Step 6: Add the table to the schema**

In `store/pg/schema/0001_init.sql`, directly after the `deposit_accounts` table (`:207-221`):

```sql
-- An account's external addresses: what a counterparty quotes to pay it. The
-- account's own id is never one of these.
--
-- Two things this table deliberately does NOT say.
--
-- First, there is no UNIQUE (book_id, scheme, value). "One bank issues an
-- address once" is a real domain rule and deposit.Register enforces it by
-- reading before it writes — but nothing serializes two concurrent adds the way
-- NextID's row lock serializes AddParticipantTx, so under READ COMMITTED a
-- constraint here would fire in Postgres and not in store/mem. That is the one
-- divergence this package must never introduce; see the note on UNIQUE
-- (book_id, name) above, which is the same argument. The primary key is
-- therefore widened with deposit_account_id so that it is a row identity rather
-- than the domain rule in disguise, and the lookup index is a plain index. The
-- residual duplicate is caught at READ time: Register.ResolveIdentifier answers
-- ErrIdentifierAmbiguous rather than picking one.
-- storetest/IdentifierUniquenessIsNotEnforced pins all of this.
--
-- Second, there is no CHECK on scheme or value. The known schemes are Go
-- constants (deposit.IdentifierIBAN), the way assets and payment schemes are,
-- and the FORMAT of a value is deliberately unvalidated — no mod-97 check digit
-- — so that the seed's readable SE89-AURORA-1001 stays legal.
--
-- The parent FOREIGN KEY, unlike subledgers.ledger_id, DOES stay. It is the
-- exemption stated above for entries -> transactions: PutDepositAccount writes
-- both sides itself, within one statement sequence, so no caller can produce an
-- orphan. Identifiers are modelled as part of the account aggregate precisely
-- so that this holds.
CREATE TABLE deposit_account_identifiers (
    book_id            TEXT NOT NULL REFERENCES books (id) ON DELETE CASCADE,
    deposit_account_id TEXT NOT NULL,
    scheme             TEXT NOT NULL,
    value              TEXT NOT NULL,
    PRIMARY KEY (book_id, deposit_account_id, scheme, value),
    FOREIGN KEY (book_id, deposit_account_id)
        REFERENCES deposit_accounts (book_id, id) ON DELETE CASCADE
);

-- ListDepositAccountsByIdentifier. Not UNIQUE, on purpose; see above.
CREATE INDEX deposit_account_identifiers_lookup_idx
    ON deposit_account_identifiers (book_id, scheme, value);
```

- [ ] **Step 7: Implement in `store/pg`**

In `store/pg/tx_deposit.go`, extend `PutDepositAccount` (after the existing `INSERT … ON CONFLICT`, before `return nil`) with a delete-then-insert of the child rows, so the upsert replaces the set:

```go
	// Replace the identifier set. Delete-then-insert rather than a merge: the
	// account is one aggregate and PutDepositAccount is an upsert of all of it,
	// so a removed identifier has to actually disappear.
	if _, err := t.tx.Exec(ctx,
		`DELETE FROM deposit_account_identifiers WHERE book_id = $1 AND deposit_account_id = $2`,
		string(book), string(a.ID)); err != nil {
		return fmt.Errorf("pg: clear identifiers for %s: %w", a.ID, err)
	}
	for _, ident := range a.Identifiers {
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO deposit_account_identifiers (book_id, deposit_account_id, scheme, value)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`,
			string(book), string(a.ID), string(ident.Scheme), ident.Value); err != nil {
			return fmt.Errorf("pg: put identifier %s/%s for %s: %w", ident.Scheme, ident.Value, a.ID, err)
		}
	}
```

`ON CONFLICT DO NOTHING` is there because the same identifier listed twice in one slice is the caller's sloppiness, not a storage error — and `store/mem`'s map-of-account would accept it silently.

Add a hydration helper below `scanDepositAccount`:

```go
// hydrateIdentifiers fills Identifiers on the accounts given, in ONE query.
//
// One query and not one per account: the multi-asset rework found exactly this
// N+1 in two listing endpoints, and a bank with a page of customers would
// reintroduce it here.
func (t *tx) hydrateIdentifiers(ctx context.Context, book ledger.BookID, accounts []deposit.Account) error {
	if len(accounts) == 0 {
		return nil
	}
	ids := make([]string, len(accounts))
	for i, a := range accounts {
		ids[i] = string(a.ID)
	}
	rows, err := t.tx.Query(ctx, `
		SELECT deposit_account_id, scheme, value
		FROM deposit_account_identifiers
		WHERE book_id = $1 AND deposit_account_id = ANY($2)
		ORDER BY scheme, value`, string(book), ids)
	if err != nil {
		return fmt.Errorf("pg: list identifiers: %w", err)
	}
	defer rows.Close()

	byAccount := make(map[deposit.AccountID][]deposit.Identifier, len(accounts))
	for rows.Next() {
		var (
			id    deposit.AccountID
			ident deposit.Identifier
		)
		if err := rows.Scan(&id, &ident.Scheme, &ident.Value); err != nil {
			return fmt.Errorf("pg: list identifiers: %w", err)
		}
		byAccount[id] = append(byAccount[id], ident)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range accounts {
		accounts[i].Identifiers = byAccount[accounts[i].ID]
	}
	return nil
}
```

Call it at the end of `GetDepositAccount` (wrapping the single account in a one-element slice) and of `ListDepositAccounts` (on the whole page), and add the new reader:

```go
func (t *tx) ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident deposit.Identifier) ([]deposit.Account, error) {
	rows, err := t.tx.Query(ctx, `
		SELECT `+depositAccountColumns+`
		FROM deposit_accounts a
		WHERE a.book_id = $1 AND EXISTS (
			SELECT 1 FROM deposit_account_identifiers i
			WHERE i.book_id = a.book_id AND i.deposit_account_id = a.id
			  AND i.scheme = $2 AND i.value = $3)
		ORDER BY a.created_at ASC NULLS FIRST, a.seq`,
		string(book), string(ident.Scheme), ident.Value)
	if err != nil {
		return nil, fmt.Errorf("pg: list deposit accounts by identifier: %w", err)
	}
	defer rows.Close()

	out := make([]deposit.Account, 0)
	for rows.Next() {
		a, err := scanDepositAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: list deposit accounts by identifier: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, t.hydrateIdentifiers(ctx, book, out)
}
```

`depositAccountColumns` is an unqualified select list (`id, gl_account, …`). If prefixing the table alias is needed for the query above to parse, alias the table as `a` and leave the constant alone by selecting `SELECT ` + depositAccountColumns + ` FROM deposit_accounts a` — unqualified column names resolve against the only table in the FROM list, so this parses as written. Verify by running the test rather than by reading.

- [ ] **Step 8: Run both stores**

Run: `go test ./... 2>&1 | tail -20`
Expected: all PASS.

Run: `TEST_DATABASE_URL=<your url> go test ./store/... -v 2>&1 | grep -i identifier`
Expected: the same four subtests PASS against Postgres. If no Postgres is available, `make test-pg` and note in the commit that the pg half was not exercised — do not claim it passed.

- [ ] **Step 9: Commit**

```bash
git add deposit/store.go store/mem/tx_deposit.go store/pg/tx_deposit.go store/pg/schema/0001_init.sql store/storetest/deposit.go
git commit -m "$(cat <<'EOF'
feat(store): persist account identifiers in both stores

Identifiers ride on the account aggregate: PutDepositAccount writes them
and both readers hydrate them in one query, not one per account.

No UNIQUE constraint. "One bank issues an address once" is a domain rule
deposit.Register enforces by reading before it writes, and nothing
serializes two concurrent adds — so a constraint would fire in Postgres
and not in store/mem, which is the one divergence this package must never
introduce. IdentifierUniquenessIsNotEnforced pins it, the way
ParentReferencesAreNotEnforced already does.

The parent FOREIGN KEY does stay: the store writes both sides itself, so
no caller can produce an orphan. That is why identifiers are part of the
aggregate rather than separately writable.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: The register — open with, add, remove, resolve

**Files:**
- Modify: `deposit/register.go:152-220`
- Modify: `ledger/audit.go:38-43`
- Test: `deposit/register_identifier_test.go` (create)

**Interfaces:**
- Consumes: `deposit.Identifier`, the sentinels (Task 1); `Tx.ListDepositAccountsByIdentifier` (Task 2); `Register.appendAuditTx(ctx, tx, eventType, entityID string, payload any) error` (`deposit/register.go:102`).
- Produces:
  - `Register.OpenAccount(ctx, subledger, name, asset, productID, overdraftLimit, identifiers ...Identifier) (Account, error)` and the matching `OpenAccountTx`. **Variadic**, so all 74 existing call sites compile untouched.
  - `Register.AddIdentifier(ctx, id AccountID, ident Identifier) error` + `AddIdentifierTx(ctx, tx, id, ident)`
  - `Register.RemoveIdentifier(ctx, id AccountID, ident Identifier) error` + `RemoveIdentifierTx(ctx, tx, id, ident)`
  - `Register.ResolveIdentifier(ctx, ident Identifier) (Account, error)` + `ResolveIdentifierTx(ctx, tx, ident)`
  - `ledger.EventIdentifierAdded = "identifier.added"`, `ledger.EventIdentifierRemoved = "identifier.removed"`

- [ ] **Step 1: Write the failing tests**

Create `deposit/register_identifier_test.go`. The fixture is `deposit/register_test.go:61`'s `newTestRegister(t) (*Register, *ledger.Book, ledger.SubledgerID, product.ID)` — four returns, the second of which these tests do not need — plus the package-level `testAsset`. Do not invent a new fixture.

```go
package deposit

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

func TestOpenAccountWithIdentifier(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}

	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if len(acct.Identifiers) != 1 || acct.Identifiers[0] != iban {
		t.Fatalf("identifiers = %#v, want [%#v]", acct.Identifiers, iban)
	}

	got, err := reg.ResolveIdentifier(ctx, iban)
	if err != nil {
		t.Fatalf("ResolveIdentifier: %v", err)
	}
	if got.ID != acct.ID {
		t.Fatalf("resolved %s, want %s", got.ID, acct.ID)
	}
}

func TestOpenAccountWithoutIdentifierIsNormal(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Plumbing", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if len(acct.Identifiers) != 0 {
		t.Fatalf("identifiers = %#v, want none", acct.Identifiers)
	}
}

func TestAddAndRemoveIdentifier(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}

	if err := reg.AddIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("AddIdentifier: %v", err)
	}
	if _, err := reg.ResolveIdentifier(ctx, iban); err != nil {
		t.Fatalf("ResolveIdentifier after add: %v", err)
	}

	if err := reg.RemoveIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("RemoveIdentifier: %v", err)
	}
	if _, err := reg.ResolveIdentifier(ctx, iban); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier after remove = %v, want ErrIdentifierNotFound", err)
	}
}

func TestAddIdentifierRefusesADuplicateAtTheSameBank(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}

	if _, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	other, err := reg.OpenAccount(ctx, sub, "Aaron", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if err := reg.AddIdentifier(ctx, other.ID, iban); !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("AddIdentifier = %v, want ErrIdentifierTaken", err)
	}
	// And the same rule at open, which is a different code path.
	if _, err := reg.OpenAccount(ctx, sub, "Annie", testAsset, prd, 0, iban); !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("OpenAccount with a taken identifier = %v, want ErrIdentifierTaken", err)
	}
}

func TestAddIdentifierIsIdempotentForTheSameAccount(t *testing.T) {
	// Re-adding an identifier the account already holds is not a collision with
	// somebody else — it is a no-op. Refusing it would make a retried request
	// fail on its second delivery.
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if err := reg.AddIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("AddIdentifier (repeat) = %v, want nil", err)
	}
	got, err := reg.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(got.Identifiers) != 1 {
		t.Fatalf("identifiers = %#v, want exactly one", got.Identifiers)
	}
}

func TestIdentifierMutationsAreAudited(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	if err := reg.AddIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("AddIdentifier: %v", err)
	}
	if err := reg.RemoveIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("RemoveIdentifier: %v", err)
	}

	// ledger.Tx.ListAudit takes a filter (ledger/store.go:124,
	// ledger/audit.go:152), so the account's own events come back directly.
	var types []string
	if err := reg.Store().View(ctx, func(ctx context.Context, tx Tx) error {
		events, err := tx.ListAudit(ctx, ledger.AuditFilter{
			BookID:   reg.BookID(),
			Scope:    ledger.ScopeDeposit,
			EntityID: string(acct.ID),
		})
		if err != nil {
			return err
		}
		for _, e := range events {
			types = append(types, e.Type)
		}
		return nil
	}); err != nil {
		t.Fatalf("reading the audit trail: %v", err)
	}

	wantBoth := map[string]bool{ledger.EventIdentifierAdded: false, ledger.EventIdentifierRemoved: false}
	for _, got := range types {
		if _, ok := wantBoth[got]; ok {
			wantBoth[got] = true
		}
	}
	for want, seen := range wantBoth {
		if !seen {
			t.Fatalf("audit types = %v, want it to include %s", types, want)
		}
	}
}

func TestResolveIdentifierIsAmbiguousWhenTwoAccountsHoldIt(t *testing.T) {
	// Reachable only by going around the register — which is exactly what a
	// concurrent add does, since uniqueness has no constraint behind it. The
	// answer must be a refusal, not the first hit.
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	first, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	second, err := reg.OpenAccount(ctx, sub, "Aaron", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	_ = first

	// Write the duplicate straight through the store, past the register's check.
	if err := reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		a, err := tx.GetDepositAccount(ctx, reg.BookID(), second.ID)
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers, iban)
		return tx.PutDepositAccount(ctx, reg.BookID(), a)
	}); err != nil {
		t.Fatalf("seeding the duplicate: %v", err)
	}

	if _, err := reg.ResolveIdentifier(ctx, iban); !errors.Is(err, ErrIdentifierAmbiguous) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierAmbiguous", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./deposit/ -run 'Identifier' -v 2>&1 | head -30`
Expected: compile failure — `reg.AddIdentifier undefined`, `ledger.EventIdentifierAdded undefined`.

- [ ] **Step 3: Add the audit event constants**

In `ledger/audit.go`, in the deposit-scope block alongside `EventAccountOpened`:

```go
	EventIdentifierAdded   = "identifier.added"
	EventIdentifierRemoved = "identifier.removed"
```

- [ ] **Step 4: Make `OpenAccount` take identifiers**

In `deposit/register.go`, change both signatures to end with `identifiers ...Identifier`:

```go
func (r *Register) OpenAccount(ctx context.Context, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, productID product.ID, overdraftLimit ledger.Amount, identifiers ...Identifier) (Account, error)
func (r *Register) OpenAccountTx(ctx context.Context, tx Tx, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, productID product.ID, overdraftLimit ledger.Amount, identifiers ...Identifier) (Account, error)
```

`OpenAccount` forwards `identifiers...`. Variadic and last so that every existing call site compiles unchanged — there are 74 in the test suites alone, and a mechanical `, nil` on all of them would bury this change's actual diff.

Extend the doc comment on `OpenAccount`:

```go
// identifiers are the account's external addresses — an IBAN, later a card PAN.
// Zero is legal and normal: an account nobody pays from outside the bank needs
// no address. Each must be unique within THIS bank; a collision is
// ErrIdentifierTaken. Uniqueness is not checked across banks, and does not need
// to be: a bank-issued identifier carries its own issuer (an IBAN its bank
// code, a PAN its BIN), so two banks cannot collide without one of them issuing
// addresses it was never allocated.
```

In `OpenAccountTx`, after the `checkOpenableProductTx` call and before the GL account is created (so nothing is written when the check fails):

```go
	for _, ident := range identifiers {
		if err := ident.Validate("identifier"); err != nil {
			return Account{}, err
		}
		if err := r.checkIdentifierFreeTx(ctx, tx, "", ident); err != nil {
			return Account{}, err
		}
	}
```

and set the field on the `Account` literal:

```go
		Identifiers: identifiers,
```

- [ ] **Step 5: Add the register methods**

In `deposit/register.go`, after the account-management section:

```go
// checkIdentifierFreeTx refuses an identifier another account at this bank
// already holds. owner, when non-empty, is the account the identifier is being
// added TO: it already holding the identifier is a no-op rather than a
// collision, which is what makes a retried AddIdentifier succeed twice.
//
// The check is a read followed by a write with no constraint behind it and no
// lock above it, so two concurrent adds can both pass. That is deliberate — a
// UNIQUE in store/pg would reject writes store/mem accepts — and the resulting
// duplicate is caught by ResolveIdentifier, which refuses rather than guesses.
func (r *Register) checkIdentifierFreeTx(ctx context.Context, tx Tx, owner AccountID, ident Identifier) error {
	holders, err := tx.ListDepositAccountsByIdentifier(ctx, r.bookID, ident)
	if err != nil {
		return err
	}
	for _, h := range holders {
		if h.ID != owner {
			return ErrIdentifierTaken
		}
	}
	return nil
}

// AddIdentifier gives an existing account another external address.
//
// Adding rather than replacing is the point of the plural: a customer keeps
// their IBAN and gains a card PAN, and reissuing a card is a remove plus an add
// against an account whose balance and history do not move.
func (r *Register) AddIdentifier(ctx context.Context, id AccountID, ident Identifier) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.AddIdentifierTx(ctx, tx, id, ident)
	})
}

// AddIdentifierTx is AddIdentifier within a caller-supplied unit of work.
func (r *Register) AddIdentifierTx(ctx context.Context, tx Tx, id AccountID, ident Identifier) error {
	if err := ident.Validate("identifier"); err != nil {
		return err
	}
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	if err := r.checkIdentifierFreeTx(ctx, tx, id, ident); err != nil {
		return err
	}
	for _, got := range acct.Identifiers {
		if got == ident {
			return nil // already held by this account: a no-op, not an error
		}
	}
	acct.Identifiers = append(acct.Identifiers, ident)
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventIdentifierAdded, string(id), ident)
}

// RemoveIdentifier withdraws an external address. Removing one that is not held
// is a no-op, for the same reason adding one twice is.
//
// Historical payments are unaffected: a payment stores the address it was sent
// to, so removing it here cannot rewrite what a settled payment says.
func (r *Register) RemoveIdentifier(ctx context.Context, id AccountID, ident Identifier) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.RemoveIdentifierTx(ctx, tx, id, ident)
	})
}

// RemoveIdentifierTx is RemoveIdentifier within a caller-supplied unit of work.
func (r *Register) RemoveIdentifierTx(ctx context.Context, tx Tx, id AccountID, ident Identifier) error {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	kept := make([]Identifier, 0, len(acct.Identifiers))
	found := false
	for _, got := range acct.Identifiers {
		if got == ident {
			found = true
			continue
		}
		kept = append(kept, got)
	}
	if !found {
		return nil
	}
	acct.Identifiers = kept
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventIdentifierRemoved, string(id), ident)
}

// ResolveIdentifier returns the account this bank addresses by ident.
//
// Zero matches is ErrIdentifierNotFound; more than one is
// ErrIdentifierAmbiguous, never the first hit. An address that resolves to two
// accounts is not an address, and this is the layer that decides where money
// goes — the same reason settlement refuses to default a cycle's asset rather
// than settle it in the wrong money.
func (r *Register) ResolveIdentifier(ctx context.Context, ident Identifier) (Account, error) {
	var out Account
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.ResolveIdentifierTx(ctx, tx, ident)
		return err
	})
	return out, err
}

// ResolveIdentifierTx is ResolveIdentifier within a caller-supplied unit of work.
func (r *Register) ResolveIdentifierTx(ctx context.Context, tx Tx, ident Identifier) (Account, error) {
	holders, err := tx.ListDepositAccountsByIdentifier(ctx, r.bookID, ident)
	if err != nil {
		return Account{}, err
	}
	switch len(holders) {
	case 0:
		return Account{}, ErrIdentifierNotFound
	case 1:
		return holders[0], nil
	default:
		return Account{}, ErrIdentifierAmbiguous
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./deposit/ -v 2>&1 | tail -30`
Expected: all PASS, including the pre-existing suites (the variadic signature must not have broken any of the 74 call sites).

Then `go build ./...` and `go test ./... 2>&1 | tail -10` — expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add deposit/register.go deposit/register_identifier_test.go ledger/audit.go
git commit -m "$(cat <<'EOF'
feat(deposit): open, add, remove and resolve account identifiers

Uniqueness is checked within the bank, which is the widest scope a
register can see and also the right one: a bank-issued address carries its
own issuer, so two banks cannot collide without one of them issuing
addresses it was never allocated.

Resolution refuses an ambiguous answer rather than taking the first hit.
That is what closes the race the missing UNIQUE constraint leaves open,
and it is the same rule settlement applies when a cycle's asset cannot be
resolved: guessing quietly in the layer that decides where money goes is
the failure worth refusing.

identifiers is variadic and last, so all 74 existing OpenAccount call
sites compile untouched.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Seed the sample network with IBANs

**Files:**
- Modify: `seed/seed.go:248-260` (the `open` / `openOverdraft` helpers)

**Interfaces:**
- Consumes: `Register.OpenAccount(…, identifiers ...deposit.Identifier)` (Task 3).
- Produces: every seeded customer account holds `{IBAN, <its existing IBAN string>}`. The `b.ibans` map stays for now — `PartyRef.IBAN` still exists until Task 6 — and is deleted there.

- [ ] **Step 1: Write the failing test**

Append to `seed/seed_test.go`:

```go
func TestSeededAccountsCarryTheirIBAN(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	for _, p := range listParticipants(t, ctx, net) {
		accounts, err := p.Deposit.ListAccounts(ctx)
		if err != nil {
			t.Fatalf("ListAccounts: %v", err)
		}
		for _, a := range accounts {
			if len(a.Identifiers) != 1 || a.Identifiers[0].Scheme != deposit.IdentifierIBAN {
				t.Fatalf("%s/%s identifiers = %#v, want exactly one IBAN",
					p.Name, a.Name, a.Identifiers)
			}
			// Resolving it must come back to the same account: this is the
			// property the customer send form depends on.
			got, err := p.Deposit.ResolveIdentifier(ctx, a.Identifiers[0])
			if err != nil {
				t.Fatalf("ResolveIdentifier(%s): %v", a.Identifiers[0].Value, err)
			}
			if got.ID != a.ID {
				t.Fatalf("resolved %s, want %s", got.ID, a.ID)
			}
		}
	}
}
```

`testNetwork(t) *payment.Network` is `seed/seed_test.go:24`; `listParticipants(t, ctx, net) []*payment.Participant` is `:374`. Confirm the deposit-listing method's real name (`grep -n "func (r \*Register) List" deposit/register.go`) before relying on `ListAccounts`.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./seed/ -run TestSeededAccountsCarryTheirIBAN -v`
Expected: FAIL — `identifiers = [], want exactly one IBAN`.

- [ ] **Step 3: Pass the identifier at open**

In `seed/seed.go`, in `openOverdraft`:

```go
func (b *builder) openOverdraft(p *payment.Participant, name, iban string, limit ledger.Amount) deposit.Account {
	ident := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: iban}
	a := must(p.Deposit.OpenAccount(b.ctx, p.CustomerSubledger, name, seedAsset, b.cats[p.ID].basic, limit, ident))
	b.ibans[a.ID] = iban
	return a
}
```

Update its doc comment: the IBAN is now the account's own identifier, and `b.ibans` is a lookaside that exists only until `PartyRef` carries an identifier.

- [ ] **Step 4: Run the tests**

Run: `go test ./seed/ -v 2>&1 | tail -20`
Expected: PASS, including the pre-existing seed suites.

- [ ] **Step 5: Commit**

```bash
git add seed/seed.go seed/seed_test.go
git commit -m "$(cat <<'EOF'
feat(seed): give every seeded account its IBAN as an identifier

The IBANs are the same readable strings they always were — SE89-AURORA-1001
and friends — so every worked example, screenshot and doc reference still
reads the same. They are now resolvable rather than decorative.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `Network.ResolveIdentifier` — the directory

**Files:**
- Modify: `payment/system.go` (a new method near `checkPartyTx`, `:1376`)
- Test: `payment/system_test.go`

**Interfaces:**
- Consumes: `Tx.ListDepositAccountsByIdentifier` (Task 2), `deposit.ErrIdentifierNotFound` / `ErrIdentifierAmbiguous` (Task 1), `Tx.ListParticipants()` (`payment/store.go:43`).
- Produces: `Network.ResolveIdentifier(ctx, ident deposit.Identifier) (PartyRef, error)` and `ResolveIdentifierTx(ctx, tx, ident)`. Returns `deposit.ErrIdentifierNotFound` on a miss and `deposit.ErrIdentifierAmbiguous` when two banks hold it. The returned `PartyRef` names the participant, the account and the identifier used.

- [ ] **Step 1: Write the failing test**

Append to `payment/system_test.go`, reusing the file's existing network fixture:

```go
func TestResolveIdentifierAcrossBanks(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	_ = openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	ref, err := net.ResolveIdentifier(ctx, deposit.Identifier{
		Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001",
	})
	if err != nil {
		t.Fatalf("ResolveIdentifier: %v", err)
	}
	if ref.Participant != aurora.ID || ref.Account != alice.ID {
		t.Fatalf("resolved %s/%s, want %s/%s", ref.Participant, ref.Account, aurora.ID, alice.ID)
	}
}

func TestResolveIdentifierNotFound(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	addParticipant(t, ctx, net, "Aurora Bank")

	_, err := net.ResolveIdentifier(ctx, deposit.Identifier{
		Scheme: deposit.IdentifierIBAN, Value: "NOBODY-0001",
	})
	if !errors.Is(err, deposit.ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierNotFound", err)
	}
}

func TestResolveIdentifierRefusesACrossBankCollision(t *testing.T) {
	// Per-bank uniqueness makes this reachable, so the network must not pick
	// one. Two banks claiming one address is not an address.
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")

	openCustomer(t, ctx, aurora, "Alice", "SHARED-0001")
	openCustomer(t, ctx, verde, "Bruno", "SHARED-0001")

	_, err := net.ResolveIdentifier(ctx, deposit.Identifier{
		Scheme: deposit.IdentifierIBAN, Value: "SHARED-0001",
	})
	if !errors.Is(err, deposit.ErrIdentifierAmbiguous) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierAmbiguous", err)
	}
}
```

Add a small `openCustomer(t, ctx, p *payment.Participant, name, iban string) deposit.Account` helper in the test file that calls `p.Deposit.OpenAccount(...)` with the identifier, mirroring how the file's other helpers are written.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./payment/ -run TestResolveIdentifier -v 2>&1 | head -20`
Expected: compile failure — `net.ResolveIdentifier undefined`.

- [ ] **Step 3: Implement the sweep**

In `payment/system.go`, next to `checkPartyTx`:

```go
// ResolveIdentifier turns an external address — an IBAN today — into the party
// it names. It is the network's directory.
//
// A real network's directory is a service with an index; this is a sweep over
// the members, which is the honest shape at four banks and the boundary at
// which a proxy-alias registry would arrive. Aliases that are NOT bank-issued
// (a phone number, an email address) cannot be resolved this way at all, since
// no member can guarantee they are unique — which is why SEPA's Proxy Lookup
// Service and UPI are separate central services rather than a sweep like this.
func (s *Network) ResolveIdentifier(ctx context.Context, ident deposit.Identifier) (PartyRef, error) {
	var out PartyRef
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ResolveIdentifierTx(ctx, tx, ident)
		return err
	})
	return out, err
}

// ResolveIdentifierTx is ResolveIdentifier within a caller-supplied unit of work.
//
// Two members holding the identifier is ErrIdentifierAmbiguous rather than the
// first one found. Uniqueness is enforced per bank — that is the widest scope a
// register can see — so a collision across banks is representable, and choosing
// between them here would route a payment to a bank on the strength of listing
// order.
func (s *Network) ResolveIdentifierTx(ctx context.Context, tx Tx, ident deposit.Identifier) (PartyRef, error) {
	if err := ident.Validate("identifier"); err != nil {
		return PartyRef{}, err
	}
	members, err := tx.ListParticipants(ctx)
	if err != nil {
		return PartyRef{}, err
	}
	var found PartyRef
	hits := 0
	for _, m := range members {
		holders, err := tx.ListDepositAccountsByIdentifier(ctx, m.BookID, ident)
		if err != nil {
			return PartyRef{}, err
		}
		hits += len(holders)
		if hits > 1 {
			return PartyRef{}, deposit.ErrIdentifierAmbiguous
		}
		if len(holders) == 1 {
			found = PartyRef{Participant: m.ID, Account: holders[0].ID, Identifier: ident}
		}
	}
	if hits == 0 {
		return PartyRef{}, deposit.ErrIdentifierNotFound
	}
	return found, nil
}
```

`PartyRef.Identifier` does not exist yet; Task 6 introduces it. Write the line **without** it here — `PartyRef{Participant: m.ID, Account: holders[0].ID}` — and let Task 6 fill the field in. That keeps this task's diff about the sweep and Task 6's about the rename. Do not reach for the existing `IBAN` field as a stopgap; it is about to be deleted.

- [ ] **Step 4: Run the tests**

Run: `go test ./payment/ -v 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add payment/system.go payment/system_test.go
git commit -m "$(cat <<'EOF'
feat(payment): resolve an identifier to the party it names

The network's directory: a sweep over the members, which is the honest
shape at four banks. Two members holding one address is refused, not
resolved to the first hit — uniqueness is enforced per bank, so a
cross-bank collision is representable, and choosing between them would
route a payment on the strength of listing order.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `PartyRef.IBAN` → `PartyRef.Identifier`

**Files:**
- Modify: `payment/types.go:163-167`
- Modify: `payment/system.go:1363-1375` (`validateParty`) and the `ResolveIdentifierTx` written in Task 5
- Modify: `seed/seed.go:160-165, 248-260, 292-293`
- Modify: `api/dto_payment.go:76-92`
- Modify: `web/src/lib/types.ts:300-304`
- Modify: `web/src/components/forms/party-ref-fields.tsx:50-62`
- Test: `payment/system_test.go`, `api/server_test.go`

**Interfaces:**
- Consumes: `deposit.Identifier` (Task 1).
- Produces: `payment.PartyRef{Participant ParticipantID; Account deposit.AccountID; Identifier deposit.Identifier}`. Wire shape: `{"participant": "...", "account": "...", "identifier": {"scheme": "IBAN", "value": "SE89-AURORA-1001"}}`, with `identifier` omitted when empty.

This task changes no behaviour. It is a rename plus a wire-shape change, done on its own so a reviewer can check it as one.

- [ ] **Step 1: Change the type**

In `payment/types.go`:

```go
// PartyRef identifies one side of a payment: a customer deposit account at a
// specific participant bank, and the external address that was quoted to reach
// it.
//
// The identifier is STORED rather than derived, because identifiers are
// mutable: an account that later has its IBAN withdrawn must not retroactively
// change what a settled payment says it was sent to. What a payment records is
// the address actually used.
type PartyRef struct {
	Participant ParticipantID
	Account     deposit.AccountID // the customer deposit account within that bank
	Identifier  deposit.Identifier
}
```

- [ ] **Step 2: Update `validateParty`**

In `payment/system.go`, replace the trailing `ledger.ValidateText(field+".iban", ref.IBAN)`:

```go
	// An empty identifier is legal — an internal transfer quotes no external
	// address — so validate only what is there.
	if ref.Identifier == (deposit.Identifier{}) {
		return nil
	}
	return ref.Identifier.Validate(field + ".identifier")
```

Update the function's doc comment: the identifier is stored on the payment; the two ids are the lookup keys.

- [ ] **Step 3: Finish Task 5's sweep**

In `ResolveIdentifierTx`, set the field:

```go
			found = PartyRef{Participant: m.ID, Account: holders[0].ID, Identifier: ident}
```

- [ ] **Step 4: Update the seed and drop the lookaside map**

In `seed/seed.go`:

```go
// ref builds a PartyRef for a customer deposit account from the account's own
// IBAN identifier, so the same account always produces an identical PartyRef.
func (b *builder) ref(p *payment.Participant, acct deposit.Account) payment.PartyRef {
	ref := payment.PartyRef{Participant: p.ID, Account: acct.ID}
	for _, ident := range acct.Identifiers {
		if ident.Scheme == deposit.IdentifierIBAN {
			ref.Identifier = ident
			break
		}
	}
	return ref
}
```

Delete the `ibans` field from `builder` (`:164`), its initialisation (`:105`), its doc comment (`:160-163`), and the `b.ibans[a.ID] = iban` assignment in `openOverdraft`. The IBAN now lives on the account, so a second copy of it is a fact that can drift.

- [ ] **Step 5: Update the API DTO**

In `api/dto_payment.go`:

```go
type identifierDTO struct {
	Scheme string `json:"scheme"`
	Value  string `json:"value"`
}

type partyRefDTO struct {
	Participant string `json:"participant"`
	Account     string `json:"account"`
	// Identifier is the external address quoted for this party — an IBAN today.
	// Absent for a party addressed only by its ids.
	Identifier *identifierDTO `json:"identifier,omitempty"`
}

func toPartyRefDTO(r payment.PartyRef) partyRefDTO {
	out := partyRefDTO{Participant: string(r.Participant), Account: string(r.Account)}
	if r.Identifier != (deposit.Identifier{}) {
		out.Identifier = &identifierDTO{Scheme: string(r.Identifier.Scheme), Value: r.Identifier.Value}
	}
	return out
}

func (r partyRefDTO) toDomain() payment.PartyRef {
	out := payment.PartyRef{
		Participant: payment.ParticipantID(r.Participant),
		Account:     deposit.AccountID(r.Account),
	}
	if r.Identifier != nil {
		out.Identifier = deposit.Identifier{
			Scheme: deposit.IdentifierScheme(r.Identifier.Scheme),
			Value:  r.Identifier.Value,
		}
	}
	return out
}
```

- [ ] **Step 6: Update the web types and form**

`web/src/lib/types.ts`:

```ts
export interface AccountIdentifier {
  scheme: string;
  value: string;
}

export interface PartyRef {
  participant: string;
  account: string;
  // The external address quoted for this party — an IBAN today. Absent when
  // the party was addressed only by its ids.
  identifier?: AccountIdentifier;
}
```

`web/src/components/forms/party-ref-fields.tsx` — the IBAN input now writes an identifier:

```tsx
      <div className="space-y-1.5">
        <FieldLabel htmlFor={`${idBase}-iban`}>IBAN (optional)</FieldLabel>
        <Input
          id={`${idBase}-iban`}
          value={value.identifier?.value ?? ""}
          placeholder="DE89…"
          className="font-mono"
          onChange={(e) =>
            onChange({
              ...value,
              identifier: e.target.value
                ? { scheme: "IBAN", value: e.target.value }
                : undefined,
            })
          }
        />
      </div>
```

- [ ] **Step 7: Fix the remaining compile errors and run everything**

Run: `go build ./... 2>&1 | head -30`, fix each reported `.IBAN` reference by reading its surroundings, then:

Run: `go test ./... 2>&1 | tail -20`
Expected: PASS.

Run: `cd web && npm run test 2>&1 | tail -20 && npx tsc --noEmit 2>&1 | head -20`
Expected: PASS, no type errors.

- [ ] **Step 8: Commit**

```bash
git add payment/types.go payment/system.go seed/seed.go api/dto_payment.go web/src/lib/types.ts web/src/components/forms/party-ref-fields.tsx
git commit -m "$(cat <<'EOF'
refactor(payment): a party quotes an identifier, not a free-form IBAN

PartyRef.IBAN was a label with a comment saying so. It becomes a
deposit.Identifier — still stored rather than derived, because
identifiers are mutable and a settled payment must keep saying what it was
actually sent to.

The seed's ibans lookaside map goes with it: the IBAN lives on the account
now, and a second copy of a fact is a fact that can drift.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: The scheme declares what addresses it, and initiation enforces it

**Files:**
- Modify: `payment/scheme.go:17-51` (the interface), `:97-107` (`SCT`), `:120-130` (`SDD`)
- Modify: `payment/system.go:973-995` (`InitiatePaymentTx`)
- Modify: `payment/errors.go`
- Test: `payment/system_test.go`

**Interfaces:**
- Consumes: `deposit.IdentifierScheme`, `deposit.IdentifierIBAN` (Task 1); `PartyRef.Identifier` (Task 6).
- Produces: `Scheme.AddressedBy() deposit.IdentifierScheme`; `payment.ErrUnaddressableAccount`, `payment.ErrIdentifierMismatch`.

- [ ] **Step 1: Write the failing tests**

Append to `payment/system_test.go`:

```go
func TestInitiateRefusesAnAccountWithNoIdentifierInTheSchemesScheme(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")
	openCycle(t, ctx, net, payment.SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	// Bruno has no IBAN at all: an SCT cannot address him.
	bruno := openCustomerWithoutIdentifier(t, ctx, verde, "Bruno")

	_, err := net.InitiatePayment(ctx, payment.InitiatePaymentRequest{
		Scheme:   payment.SchemeSEPACT,
		Debtor:   payment.PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor: payment.PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:   10_00,
	})
	if !errors.Is(err, payment.ErrUnaddressableAccount) {
		t.Fatalf("InitiatePayment = %v, want ErrUnaddressableAccount", err)
	}
}

func TestInitiateRefusesAQuotedIdentifierTheAccountDoesNotHold(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")
	openCycle(t, ctx, net, payment.SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	_, err := net.InitiatePayment(ctx, payment.InitiatePaymentRequest{
		Scheme: payment.SchemeSEPACT,
		Debtor: payment.PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor: payment.PartyRef{
			Participant: verde.ID, Account: bruno.ID,
			// Somebody else's address, pointing at Bruno's account.
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"},
		},
		Amount: 10_00,
	})
	if !errors.Is(err, payment.ErrIdentifierMismatch) {
		t.Fatalf("InitiatePayment = %v, want ErrIdentifierMismatch", err)
	}
}

func TestSchemesDeclareTheirIdentifierScheme(t *testing.T) {
	if got := (payment.SCT{}).AddressedBy(); got != deposit.IdentifierIBAN {
		t.Fatalf("SCT.AddressedBy() = %q, want %q", got, deposit.IdentifierIBAN)
	}
	if got := (payment.SDD{}).AddressedBy(); got != deposit.IdentifierIBAN {
		t.Fatalf("SDD.AddressedBy() = %q, want %q", got, deposit.IdentifierIBAN)
	}
}
```

Add `openCustomerWithoutIdentifier` beside the Task 5 helper. Reuse `openCycle` / `fundAccount` if the file already has equivalents under other names — read it and match.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./payment/ -run 'Unaddressable|IdentifierMismatch|AddressedBy' -v 2>&1 | head -20`
Expected: compile failure — `AddressedBy undefined`, `ErrUnaddressableAccount undefined`.

- [ ] **Step 3: Extend the `Scheme` interface**

In `payment/scheme.go`, after `Asset()`:

```go
	// AddressedBy is the kind of external address this scheme routes on. Both
	// legs of a payment must carry an identifier in it.
	//
	// It is a property of the scheme, exactly as Asset is: SEPA routes on
	// IBANs, a card scheme routes on a PAN. Putting it here rather than on the
	// account is what keeps a euro-area retail standard out of the deposit
	// layer — an account has addresses, and the scheme decides which kind it
	// reads.
	AddressedBy() deposit.IdentifierScheme
```

Add the import of `deposit`, then implement on both schemes:

```go
func (SCT) AddressedBy() deposit.IdentifierScheme { return deposit.IdentifierIBAN }
```
```go
func (SDD) AddressedBy() deposit.IdentifierScheme { return deposit.IdentifierIBAN }
```

- [ ] **Step 4: Add the sentinels**

In `payment/errors.go`:

```go
	// ErrUnaddressableAccount is returned when a party's account carries no
	// identifier in the scheme's identifier scheme — an account with no IBAN
	// cannot be a leg of a SEPA payment.
	ErrUnaddressableAccount = errors.New("account has no identifier in the scheme's addressing scheme")

	// ErrIdentifierMismatch is returned when a party quotes an identifier the
	// named account does not hold. The ids route the payment and the identifier
	// records the address used; the two disagreeing means one of them is wrong,
	// and the system does not get to choose which.
	ErrIdentifierMismatch = errors.New("quoted identifier does not belong to the named account")
```

- [ ] **Step 5: Enforce at initiation**

In `payment/system.go`, in `InitiatePaymentTx`, directly after the existing asset check:

```go
	// Both legs must be addressable in the scheme's addressing scheme, and a
	// quoted address must belong to the account it is quoted for.
	//
	// This sits beside the asset check and for the same reason: it is the one
	// moment both ends are in view and neither is written, and running it here
	// rather than inside a scheme's Validate means it applies to every scheme
	// rather than only the ones whose Validate calls validateFunds.
	if err := checkAddressable(scheme, req.Debtor, debtorAccount); err != nil {
		return Payment{}, err
	}
	if err := checkAddressable(scheme, req.Creditor, creditorAccount); err != nil {
		return Payment{}, err
	}
```

and the helper, next to `checkPartyTx`:

```go
// checkAddressable confirms an account can be addressed by the scheme, and that
// any address the request quoted is really one of the account's.
func checkAddressable(scheme Scheme, ref PartyRef, acct deposit.Account) error {
	want := scheme.AddressedBy()
	held := false
	for _, ident := range acct.Identifiers {
		if ident.Scheme == want {
			held = true
			break
		}
	}
	if !held {
		return ErrUnaddressableAccount
	}
	if ref.Identifier == (deposit.Identifier{}) {
		return nil
	}
	for _, ident := range acct.Identifiers {
		if ident == ref.Identifier {
			return nil
		}
	}
	return ErrIdentifierMismatch
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./payment/ -v 2>&1 | tail -30`
Expected: PASS. Several **pre-existing** payment tests will now fail if their fixtures open accounts without an IBAN — that is the check working. Fix each by giving the fixture account an identifier, never by relaxing the check.

Run: `go test ./... 2>&1 | tail -20`
Expected: PASS. `seed` should already be fine — Task 4 gave every seeded account an IBAN.

- [ ] **Step 7: Commit**

```bash
git add payment/scheme.go payment/system.go payment/errors.go payment/system_test.go
git commit -m "$(cat <<'EOF'
feat(payment): schemes declare what addresses them, and initiation enforces it

AddressedBy sits beside Asset for the same reason: SEPA routes on IBANs
the way SEPA settles in euro, and both are properties of the scheme rather
than of the account. Putting it here is what keeps a euro-area retail
standard out of the deposit layer.

The check runs in InitiatePaymentTx, beside the asset check — the one
moment both ends are in view and neither is written — so it applies to
every scheme rather than only those whose Validate happens to call
validateFunds. Without it AddressedBy would be a declaration nothing
reads.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: The API — identifier endpoints and the directory

**Files:**
- Create: `api/handlers_directory.go`
- Modify: `api/handlers_deposit.go:15-39` (route registration)
- Modify: `api/dto_deposit.go:27-58`
- Modify: `api/errors.go:26-80`
- Test: `api/server_test.go`

**Interfaces:**
- Consumes: `Register.AddIdentifier` / `RemoveIdentifier` (Task 3), `Network.ResolveIdentifier` (Task 5), `identifierDTO` (Task 6).
- Produces:
  - `GET /directory?scheme=IBAN&value=…` → `200 {"participant","account","name","asset","identifier":{...}}`
  - `POST /participants/{pid}/deposit-accounts/{did}/identifiers` body `{"scheme","value"}` → `204`
  - `DELETE /participants/{pid}/deposit-accounts/{did}/identifiers/{scheme}/{value}` → `204`
  - `depositAccountDTO.Identifiers []identifierDTO` on the JSON key `identifiers`
  - status mappings: `deposit.ErrIdentifierNotFound` → 404, `deposit.ErrIdentifierTaken` → 409, `deposit.ErrIdentifierAmbiguous` → 409, `payment.ErrUnaddressableAccount` → 422, `payment.ErrIdentifierMismatch` → 422

- [ ] **Step 1: Write the failing tests**

Append to `api/server_test.go`, following the file's existing request helpers exactly:

```go
func TestDirectoryResolvesAnIBAN(t *testing.T) {
	// newServer(t, populate) takes the reseed function, so build the fixture
	// there: one participant, one deposit account opened with the identifier
	// {IBAN, "SE89-AURORA-1001"}. someAccount below should be written over the
	// same populate func so both tests share one fixture.
	srv := newServer(t, func(ctx context.Context, net *payment.Network) error {
		p, err := net.AddParticipant(ctx, "Aurora Bank", nil)
		if err != nil {
			return err
		}
		_, err = p.Deposit.OpenAccount(ctx, p.CustomerSubledger, "Alice", "EUR", p.ProductID, 0,
			deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"})
		return err
	}).Routes()

	var got struct {
		Participant string `json:"participant"`
		Account     string `json:"account"`
		Name        string `json:"name"`
		Asset       string `json:"asset"`
	}
	getJSON(t, srv, "/directory?scheme=IBAN&value=SE89-AURORA-1001", &got)
	if got.Participant == "" || got.Account == "" || got.Name == "" || got.Asset == "" {
		t.Fatalf("directory response = %#v, want it fully populated", got)
	}
}

func TestDirectoryUnknownIBANIs404(t *testing.T) {
	srv := newTestServer(t)
	doJSON(t, srv, "GET", "/directory?scheme=IBAN&value=NOBODY-0001", "", http.StatusNotFound)
}

func TestDirectoryMissingParamsIs400(t *testing.T) {
	srv := newTestServer(t)
	doJSON(t, srv, "GET", "/directory?scheme=IBAN", "", http.StatusBadRequest)
	doJSON(t, srv, "GET", "/directory?value=X", "", http.StatusBadRequest)
}

func TestAddAndRemoveIdentifierEndpoints(t *testing.T) {
	srv := newTestServer(t)
	pid, did := someAccount(t, srv)

	base := "/participants/" + pid + "/deposit-accounts/"
	doJSON(t, srv, "POST", base+did+"/identifiers",
		`{"scheme":"IBAN","value":"XX00-TEST-0001"}`, http.StatusNoContent)

	// Now resolvable.
	doJSON(t, srv, "GET", "/directory?scheme=IBAN&value=XX00-TEST-0001", "", http.StatusOK)

	// A second account at the same bank cannot take it.
	other := anotherAccountAtSameBank(t, srv, pid)
	doJSON(t, srv, "POST", base+other+"/identifiers",
		`{"scheme":"IBAN","value":"XX00-TEST-0001"}`, http.StatusConflict)

	doJSON(t, srv, "DELETE", base+did+"/identifiers/IBAN/XX00-TEST-0001", "", http.StatusNoContent)
	doJSON(t, srv, "GET", "/directory?scheme=IBAN&value=XX00-TEST-0001", "", http.StatusNotFound)
}

func TestDepositAccountDTOCarriesIdentifiers(t *testing.T) {
	srv := newTestServer(t)
	pid, did := someAccount(t, srv)
	var got struct {
		Identifiers []struct {
			Scheme string `json:"scheme"`
			Value  string `json:"value"`
		} `json:"identifiers"`
	}
	getJSON(t, srv, "/participants/"+pid+"/deposit-accounts/"+did, &got)
	if len(got.Identifiers) == 0 {
		t.Fatal("depositAccountDTO carried no identifiers")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./api/ -run 'Directory|Identifier' -v 2>&1 | head -20`
Expected: 404s on the new routes and a missing `identifiers` key.

- [ ] **Step 3: Add the DTO field**

In `api/dto_deposit.go`, on `depositAccountDTO`, after `Status`:

```go
	// Identifiers are the account's external addresses. Empty is normal.
	Identifiers []identifierDTO `json:"identifiers"`
```

and populate it wherever the DTO is built (grep for `depositAccountDTO{`):

```go
		Identifiers: toIdentifierDTOs(a.Identifiers),
```

with, in `api/dto_payment.go` beside `identifierDTO`:

```go
func toIdentifierDTOs(idents []deposit.Identifier) []identifierDTO {
	out := make([]identifierDTO, 0, len(idents))
	for _, i := range idents {
		out = append(out, identifierDTO{Scheme: string(i.Scheme), Value: i.Value})
	}
	return out
}
```

A non-nil empty slice so the JSON key is `[]` and not `null` — the web client renders a list.

- [ ] **Step 4: Write the handlers**

Create `api/handlers_directory.go`:

```go
package api

import (
	"net/http"

	"github.com/raphi011/cbs/deposit"
)

// Account addressing: the network directory, and the per-account identifier
// endpoints that populate it.
//
// GET /directory is network-scoped rather than participant-scoped on purpose —
// resolving an address is exactly the question "which bank?", so a route that
// already named the bank would answer nothing.
func (s *Server) registerDirectoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /directory", s.handleResolveIdentifier)
	mux.HandleFunc("POST /participants/{pid}/deposit-accounts/{did}/identifiers", s.handleAddIdentifier)
	mux.HandleFunc("DELETE /participants/{pid}/deposit-accounts/{did}/identifiers/{scheme}/{value}", s.handleRemoveIdentifier)
}

type directoryEntryDTO struct {
	Participant string        `json:"participant"`
	Account     string        `json:"account"`
	Name        string        `json:"name"`
	Asset       string        `json:"asset"`
	Identifier  identifierDTO `json:"identifier"`
}

func (s *Server) handleResolveIdentifier(w http.ResponseWriter, r *http.Request) {
	scheme := r.URL.Query().Get("scheme")
	value := r.URL.Query().Get("value")
	if scheme == "" || value == "" {
		writeBadRequest(w, "scheme and value are both required")
		return
	}
	ident := deposit.Identifier{Scheme: deposit.IdentifierScheme(scheme), Value: value}
	ref, err := s.network().ResolveIdentifier(r.Context(), ident)
	if err != nil {
		writeError(w, err)
		return
	}
	// The name and asset need the account itself; the ref only names it.
	p, err := s.network().GetParticipant(r.Context(), ref.Participant)
	if err != nil {
		writeError(w, err)
		return
	}
	acct, err := p.Deposit.GetAccount(r.Context(), ref.Account)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, directoryEntryDTO{
		Participant: string(ref.Participant),
		Account:     string(ref.Account),
		Name:        acct.Name,
		Asset:       string(acct.Asset),
		Identifier:  identifierDTO{Scheme: scheme, Value: value},
	})
}

func (s *Server) handleAddIdentifier(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req identifierDTO
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	err := p.Deposit.AddIdentifier(r.Context(), deposit.AccountID(r.PathValue("did")),
		deposit.Identifier{Scheme: deposit.IdentifierScheme(req.Scheme), Value: req.Value})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveIdentifier(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	err := p.Deposit.RemoveIdentifier(r.Context(), deposit.AccountID(r.PathValue("did")),
		deposit.Identifier{
			Scheme: deposit.IdentifierScheme(r.PathValue("scheme")),
			Value:  r.PathValue("value"),
		})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Match `s.network()`, `s.participant(w, r)`, `writeJSON`, `writeError`, `writeBadRequest` and `decodeJSON` to what `api/handlers_deposit.go` and `api/handlers_payment.go` actually use — read them and copy the idiom rather than the names above if they differ. Wire `registerDirectoryRoutes` in wherever the other `register*Routes` functions are called (grep `registerDepositRoutes`).

- [ ] **Step 5: Map the errors**

In `api/errors.go`, add to the existing groups:

- 404: `deposit.ErrIdentifierNotFound`
- 409: `deposit.ErrIdentifierTaken`, and `deposit.ErrIdentifierAmbiguous` with a comment — an address claimed by two accounts is a conflict in the data, not a malformed request, and 409 is what tells a client the answer exists but is contested
- 422: `payment.ErrUnaddressableAccount`, `payment.ErrIdentifierMismatch`

- [ ] **Step 6: Run the tests**

Run: `go test ./api/ -v 2>&1 | tail -30`
Expected: PASS.

Run: `go test ./... 2>&1 | tail -10` — expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add api/handlers_directory.go api/handlers_deposit.go api/dto_deposit.go api/dto_payment.go api/errors.go api/server_test.go
git commit -m "$(cat <<'EOF'
feat(api): expose the directory and per-account identifiers

GET /directory is network-scoped rather than participant-scoped, because
resolving an address is exactly the question "which bank?" — a route that
already named the bank would answer nothing.

An ambiguous address is 409 rather than 500: the answer exists and is
contested, which is a conflict in the data and something a client can act
on.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Documentation, across every layer

**Files:**
- Modify: `README.md:1002` and a new subsection under *The Multi-Bank Model* (`:835`)
- Modify: `payment/doc.go:64`
- Modify: `web/src/components/hint-content.ts`
- Test: `web/src/components/concept-links.test.ts` (existing; must stay green)

**Interfaces:**
- Consumes: nothing. Produces the hint key `account-addressing`, linkable as `[[account-addressing]]`.

- [ ] **Step 1: Replace the false claim in the README**

`README.md:1002` currently reads:

> - **No IBAN or BIC validation.** Routing is by explicit `ParticipantID`; IBANs are free-form labels.

Replace with:

```markdown
- **No identifier format validation.** An IBAN's mod-97 check digit is not
  verified and neither is its length or country code; a BIC is not modelled at
  all. Addresses resolve by exact lookup, not by parsing. The seed's readable
  `SE89-AURORA-1001` is the reason: a real check digit would put opaque digits
  in every worked example in this document.
```

- [ ] **Step 2: Add the addressing subsection**

Under *The Multi-Bank Model*, after the participant-accounts table, add roughly 300 words covering, in this order:

1. An account has an **internal number** — `deposit.AccountID`, the bank's own key, never quoted to a counterparty — and a set of **external identifiers**, which are what a counterparty quotes.
2. The set is plural and the pair is `(scheme, value)`: an IBAN in SEPA, a sort code plus account number in the UK, a routing number plus account number in US ACH, a proxy alias in the instant schemes, and a **card PAN**, which is an alias for a funding account and nothing more. ISO 20022 models this directly — `CashAccountIdentification` is a choice between `IBAN` and a generic triple.
3. **The scheme decides which kind addresses it**, `Scheme.AddressedBy()`, exactly as `Scheme.Asset()` decides what it settles in. `InitiatePaymentTx` refuses a leg with no identifier in that scheme.
4. **Uniqueness stops at the bank.** A register spans one book. It is also the right line: a bank-issued identifier is globally unique by construction (an IBAN carries its bank code, a PAN its BIN), while a proxy alias carries no issuer — which is why SEPA's Proxy Lookup Service and UPI are *separate central services*, and why this system has no proxy aliases.
5. **`Network.ResolveIdentifier` refuses ambiguity.** Two banks holding one address is refused, not resolved to the first hit, for the same reason settlement refuses to default a cycle's asset.
6. One sentence: the uniqueness rule is enforced in `deposit.Register` and deliberately has no `UNIQUE` constraint behind it, because a constraint only Postgres could hold would make the two stores disagree.

- [ ] **Step 3: Fix `payment/doc.go`**

`payment/doc.go:64` carries the same retired claim. Replace it with a two-line version of the README bullet and a pointer to `deposit.Identifier`.

- [ ] **Step 4: Add the hint**

In `web/src/components/hint-content.ts`, add an `account-addressing` entry distilled from the README subsection — the internal-number/external-identifier split, the scheme deciding, and the uniqueness boundary. Link it from the existing payment and deposit-account hints with `[[account-addressing]]`.

Check whether the existing hints or quiz explanations assert anything the change makes false:

Run: `grep -rn "IBAN\|iban" web/src/components/hint-content.ts web/src/lib/quiz/chapters/`

The two README IBAN mentions at `:132` and `:337` are about **assets** (an account number and its currency are inseparable) and stay true; verify the same is true of every hit here before leaving one alone.

- [ ] **Step 5: Run the web tests and load a page**

Run: `cd web && npm run test 2>&1 | tail -20`
Expected: PASS — `concept-links.test.ts` checks `[[wiki-link]]` targets in hint bodies **and** quiz explanations, which the runtime guard does not scan.

Then `make dev` (or `cd web && npm run dev`), open the app, and load at least one page. A missing hint key throws under `RootLayout` and takes every route down while `next build` stays green, so the test alone is not proof.

- [ ] **Step 6: Commit**

```bash
git add README.md payment/doc.go web/src/components/hint-content.ts
git commit -m "$(cat <<'EOF'
docs: account addressing, across every layer

"IBANs are free-form labels" was true and is not any more. What replaces
it is narrower and still true: no format validation, no check digit, no
BIC — addresses resolve by exact lookup, and the seed's readable
SE89-AURORA-1001 is why.

The new subsection carries the part that is worth teaching: an account has
one internal number and a set of external addresses, the scheme decides
which kind it reads, and uniqueness stops at the bank because a
bank-issued address carries its own issuer and a proxy alias does not.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Close out the sub-project

**Files:**
- Modify: `docs/expansion-roadmap.md` (sub-project 5's status and a log row)

- [ ] **Step 1: Run the whole suite, both stores**

Run: `go test ./... 2>&1 | tail -20`
Expected: PASS.

Run: `TEST_DATABASE_URL=<url> go test ./... 2>&1 | tail -20` (or `make test-pg`)
Expected: PASS. If Postgres was unavailable, say so in the roadmap entry rather than implying it ran.

Run: `cd web && npm run test && npx tsc --noEmit`
Expected: PASS, no type errors.

- [ ] **Step 2: Update the roadmap**

Change sub-project 5's status from `spec` to `done`, add the plan link beside the spec link, and append a log row dated the day of completion recording: what shipped, anything the implementation sharpened or contradicted in the spec, and any arithmetic or worked example that turned out wrong. The roadmap's existing rows are the model — they are candid about what the spec got wrong, and that is the part worth imitating.

- [ ] **Step 3: Commit**

```bash
git add docs/expansion-roadmap.md
git commit -m "$(cat <<'EOF'
docs: sub-project 5 done

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Notes for the implementer

- **The fixtures named in this plan are the real ones**, verified against the test files: `newTestRegister` (`deposit/register_test.go:61`, four returns), `testNetwork` (`payment/system_test.go:28` and `seed/seed_test.go:24`), `setupTwoBanks` (`payment/system_test.go:48`), `listParticipants` (`seed/seed_test.go:374`), `newTestServer` (`api/server_test.go:25`), and the API request helpers `do`, `doJSON`, `doJSONArray`, `getJSON` (`api/server_test.go:48-110`).

  Four small helpers this plan asks you to **write**, because they do not exist: `openCustomer` and `openCustomerWithoutIdentifier` in `payment/system_test.go`, and `someAccount` / `anotherAccountAtSameBank` in `api/server_test.go`. Build them out of what is already in each file — `setupTwoBanks` is the model for the payment pair — and keep them in the test file, not in a new one. Everything else: read the neighbouring file and reuse. Inventing a parallel fixture is worse than a slightly awkward reuse.
- **When a pre-existing test starts failing in Task 7, the check is working.** Give the fixture account an identifier. Do not relax the check, and do not make `AddressedBy` advisory — a declaration nothing reads is the thing this task exists to avoid.
- **Do not add a `UNIQUE` constraint** at any point, however tempting it looks when writing the duplicate test. The reasoning is in the schema comment and in `storetest/IdentifierUniquenessIsNotEnforced`; if you disagree with it, raise it rather than routing around it.
- **Do not add IBAN format validation.** Same.
