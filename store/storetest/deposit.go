package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// RunDeposit runs the deposit-layer suite against a store.
//
// It talks only to deposit.Store and deposit.Tx — never to deposit.Register — so
// what it pins is the storage contract: book scoping, not-found sentinels,
// listing order, the active-hold aggregate, snapshot upsert identity, and the
// cross-layer rollback that deposit.Tx embedding ledger.Tx exists to provide.
//
// newStore must return a store with no state in it; the suite calls it once per
// subtest and closes the result.
func RunDeposit(t *testing.T, newStore func(*testing.T, ledger.BookID) deposit.Store) {
	t.Helper()

	// A deposit account stores its asset even though its backing GL account
	// already carries it — the one duplicated fact in the schema. Duplication
	// is only safe while the two agree, so the suite says so out loud.
	t.Run("DepositAccountAssetMatchesItsGLAccount", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
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

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
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
			listed, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			if len(listed) != 1 || listed[0].Asset != "BTC" {
				t.Errorf("ListDepositAccounts = %+v, want one BTC account", listed)
			}
			return nil
		})
	})

	t.Run("DepositAccountRoundTripsAndIsBookScoped", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		// The same deposit account ID in two books is two different accounts,
		// exactly as in the ledger — and the two books are two banks' DATABASES, so
		// this is a store answering about a book it does not hold.
		const shared deposit.AccountID = "dep_1"
		other := openDeposit(t, newStore, bookB)
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: shared, GLAccount: "200.100.001", Name: "Alice at A",
				Status: deposit.Active, CreatedAt: early,
			})
		})
		updateDeposit(t, other, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookB, deposit.Account{
				ID: shared, GLAccount: "200.100.001", Name: "Bob at B",
				Status: deposit.Frozen, CreatedAt: early,
			})
		})

		var inA, inB deposit.Account
		var listedA []deposit.Account
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			var err error
			if inA, err = tx.GetDepositAccount(ctx, bookA, shared); err != nil {
				return err
			}
			listedA, err = tx.ListDepositAccounts(ctx, bookA)
			return err
		})
		viewDeposit(t, other, func(ctx context.Context, tx deposit.Tx) error {
			var err error
			inB, err = tx.GetDepositAccount(ctx, bookB, shared)
			return err
		})

		assertEqual(t, "account in book-a", inA.Name, "Alice at A")
		assertEqual(t, "account in book-b", inB.Name, "Bob at B")
		// Every field round-trips, not just the name.
		assertEqual(t, "gl account", string(inA.GLAccount), "200.100.001")
		assertEqual(t, "status", inA.Status.String(), deposit.Active.String())
		assertEqual(t, "created at", inA.CreatedAt.Equal(early), true)
		assertEqual(t, "book-b status is its own", inB.Status.String(), deposit.Frozen.String())

		// Listing one book must not show the other book's rows.
		assertEqual(t, "accounts listed for book-a", len(listedA), 1)

		// PutDepositAccount is an upsert on ID, which is how a status change is
		// written.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			a := inA
			a.Status = deposit.Closed
			return tx.PutDepositAccount(ctx, bookA, a)
		})
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			a, err := tx.GetDepositAccount(ctx, bookA, shared)
			if err != nil {
				return err
			}
			assertEqual(t, "status after upsert", a.Status.String(), deposit.Closed.String())
			all, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "upsert did not add a row", len(all), 1)
			return nil
		})
	})

	// Identifiers ride on the account aggregate: PutDepositAccount writes them
	// and both readers bring them back. If they did not, the register's
	// uniqueness check would pass against a store that had silently dropped
	// the very rows it was checking.
	//
	// TWO identifiers, written out of order: a set of one round-trips through any
	// ordering rule at all. The order is ascending by (scheme, value) — a stated
	// rule rather than whatever the store's index happens to give, which is what
	// makes it checkable.
	t.Run("IdentifiersSurviveAccountRead", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		lower := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}
		higher := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE90999000010000000002"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", GLAccount: "200.cust.001", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{higher, lower},
			})
		})

		want := []deposit.Identifier{lower, higher}
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			if !slices.Equal(got.Identifiers, want) {
				t.Fatalf("GetDepositAccount identifiers = %#v, want %#v", got.Identifiers, want)
			}
			list, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			if len(list) != 1 || !slices.Equal(list[0].Identifiers, want) {
				t.Fatalf("ListDepositAccounts identifiers = %#v, want %#v", list, want)
			}
			byIdent, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, higher)
			if err != nil {
				return err
			}
			if len(byIdent) != 1 || !slices.Equal(byIdent[0].Identifiers, want) {
				t.Fatalf("ListDepositAccountsByIdentifier identifiers = %#v, want %#v", byIdent, want)
			}
			return nil
		})
	})

	// An account with no identifiers reads back with a NIL set, not an empty
	// non-nil one. A SQL store has no rows to return and cannot produce anything
	// else; an in-Go one is handed whatever the caller built, and
	// api/handlers_deposit.go builds make([]Identifier, 0) from an absent JSON
	// field — so the two answers were both reachable. Callers compare with
	// reflect.DeepEqual and encoders distinguish null from [], so this is a real
	// difference, not a cosmetic one.
	t.Run("NoIdentifiersReadsBackNil", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Plumbing", Asset: "EUR",
				Identifiers: []deposit.Identifier{},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			if got.Identifiers != nil {
				t.Fatalf("identifiers = %#v, want nil", got.Identifiers)
			}
			return nil
		})
	})

	// One account carrying the same pair twice collapses to one. The store
	// cannot do otherwise — (book, account, scheme, value) is the primary key of
	// deposit_account_identifiers and the insert is ON CONFLICT DO NOTHING — and
	// this case is what makes that a CONTRACT rather than a consequence of the
	// current key.
	//
	// This is NOT in tension with IdentifierUniquenessIsNotEnforced below. That
	// one is about two ACCOUNTS sharing a value, which is a domain rule with no
	// constraint behind it. This is one account listing one address twice, which
	// is not a domain question at all: it is the same row written twice.
	t.Run("DuplicateIdentifiersOnOneAccountCollapse", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		iban := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{iban, iban},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			if !slices.Equal(got.Identifiers, []deposit.Identifier{iban}) {
				t.Fatalf("identifiers = %#v, want exactly one %#v", got.Identifiers, iban)
			}
			// And the account surfaces once from the lookup, not twice.
			hits, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, iban)
			if err != nil {
				return err
			}
			if len(hits) != 1 {
				t.Fatalf("lookup returned %d accounts, want 1", len(hits))
			}
			return nil
		})
	})

	// A read hands back a COPY: mutating what a reader was given must not reach
	// stored state. A SQL store cannot be mutated through its return values at all,
	// which is exactly why the rule needs saying.
	t.Run("MutatingReadIdentifiersDoesNotReachTheStore", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		iban := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{iban},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			got.Identifiers[0] = deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE90999000010000000002"}
			return nil
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			again, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			if !slices.Equal(again.Identifiers, []deposit.Identifier{iban}) {
				t.Fatalf("identifiers after a reader mutated its copy = %#v, want [%#v]", again.Identifiers, iban)
			}
			return nil
		})
	})

	// The mirror image: a WRITER that keeps its argument must not be able to
	// rewrite what it stored afterwards. A SQL store copies the values into the
	// database and could not honour such a rewrite if it wanted to.
	t.Run("MutatingWrittenIdentifiersDoesNotReachTheStore", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		iban := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}
		mine := []deposit.Identifier{iban}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR", Identifiers: mine,
			})
		})
		mine[0] = deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE90999000010000000002"}

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			if !slices.Equal(got.Identifiers, []deposit.Identifier{iban}) {
				t.Fatalf("identifiers after the writer mutated its slice = %#v, want [%#v]", got.Identifiers, iban)
			}
			return nil
		})
	})

	// An upsert replaces the set rather than merging into it. PutDepositAccount
	// is an upsert of the whole aggregate everywhere else; identifiers must not
	// be the one part of it that accumulates.
	t.Run("IdentifiersAreReplacedByAnUpsert", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		first := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}
		second := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE90999000010000000002"}

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

	// The SCHEME half of the pair is matched exactly, and the lookup is scoped
	// by book like every other method here. Two banks holding the same value is
	// a legal state, and each book sees only its own.
	//
	// The value half is matched under the scheme's own rule, which for an IBAN is
	// not literal — see
	// ListDepositAccountsByIdentifierMatchesAnIBANThroughItsSeparators.
	t.Run("ListDepositAccountsByIdentifierMatchesTheSchemeExactlyAndIsBookScoped", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		iban := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}

		// The second bank's account, in the second bank's database. One IBAN at
		// two banks is what the book scoping is about, and it is now two
		// databases rather than two book_id values.
		otherBank := openDeposit(t, newStore, bookB)
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{iban},
			})
		})
		updateDeposit(t, otherBank, func(ctx context.Context, tx deposit.Tx) error {
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
			other := deposit.Identifier{Scheme: deposit.IdentifierScheme("PAN"), Value: "DE20999000010000000001"}
			none, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, other)
			if err != nil {
				return err
			}
			if len(none) != 0 {
				t.Fatalf("wrong-scheme lookup = %#v, want none", none)
			}

			// A miss is an empty slice and a nil error, not a sentinel.
			missing := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE63999000010000000003"}
			gone, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, missing)
			if err != nil {
				return err
			}
			if len(gone) != 0 {
				t.Fatalf("missing lookup = %#v, want none", gone)
			}
			return nil
		})

		// And the other bank's database holds its own account under the very
		// same IBAN, which is the whole of what "book-scoped" is protecting.
		viewDeposit(t, otherBank, func(ctx context.Context, tx deposit.Tx) error {
			inB, err := tx.ListDepositAccountsByIdentifier(ctx, bookB, iban)
			if err != nil {
				return err
			}
			if len(inB) != 1 || inB[0].ID != "dep_2" {
				t.Fatalf("book B lookup = %#v, want just dep_2", inB)
			}
			return nil
		})
	})

	// An IBAN matches through its display separators, in BOTH directions.
	//
	// This is the case that makes a customer's own address findable. A bank
	// mints and stores the canonical compact form, and what a person types is
	// whatever their statement prints — grouped in fours, sometimes hyphenated —
	// so a store comparing raw values answers "no such account" to somebody
	// holding the account. The rule is deposit.Identifier.MatchValue, and a store
	// expressing it in SQL is re-implementing a Go function: the case below is
	// what says the two agree.
	//
	// BOTH directions, because a store must compact its ROWS and not only its
	// query. A canonical query against a separated row is not a state the
	// register writes, and the store may not assume that: it is handed rows, and
	// a comparison that held only when one side was canonical would be a rule
	// with a precondition nothing states.
	t.Run("ListDepositAccountsByIdentifierMatchesAnIBANThroughItsSeparators", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		const pan = deposit.IdentifierScheme("PAN")
		stored := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}
		grouped := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20 9990 0001 0000 0000 01"}
		hyphenated := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20-9990-0001-0000-0000-01"}

		// dep_2 is a SECOND BANK's account, in a second bank's database, because
		// a store answers for one book. It could have been a second account in
		// this one; keeping it at the other bank costs nothing and keeps the
		// pair of directions being read off two independent stores.
		otherBank := openDeposit(t, newStore, bookB)
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			// dep_1 holds the canonical form, dep_2 a separated one. Two accounts
			// so that each direction is tested against a row stored the other
			// way, rather than both times against the same one.
			if err := tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{stored},
			}); err != nil {
				return err
			}
			// A scheme with no display form, holding a value that carries a
			// hyphen anyway.
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_3", Name: "Cara", Asset: "EUR",
				Identifiers: []deposit.Identifier{{Scheme: pan, Value: "4111-1111"}},
			})
		})
		updateDeposit(t, otherBank, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookB, deposit.Account{
				ID: "dep_2", Name: "Bruno", Asset: "EUR",
				Identifiers: []deposit.Identifier{grouped},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			// Grouped query, canonical row: the direction a customer arrives from.
			hit, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, grouped)
			if err != nil {
				return err
			}
			if len(hit) != 1 || hit[0].ID != "dep_1" {
				t.Fatalf("grouped lookup of a canonical row = %#v, want just dep_1", hit)
			}
			// Hyphens are separators too. A form field is where they come from.
			hit, err = tx.ListDepositAccountsByIdentifier(ctx, bookA, hyphenated)
			if err != nil {
				return err
			}
			if len(hit) != 1 || hit[0].ID != "dep_1" {
				t.Fatalf("hyphenated lookup of a canonical row = %#v, want just dep_1", hit)
			}
			// What must NOT happen: the separators are removed, not treated as
			// wildcards, and a different account number stays a different one.
			other := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE90999000010000000002"}
			none, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, other)
			if err != nil {
				return err
			}
			if len(none) != 0 {
				t.Fatalf("lookup of a different IBAN = %#v, want none", none)
			}
			// And the rule is the IBAN's, not every scheme's. A card PAN whose
			// value happens to carry a hyphen is a different address from one
			// without: nothing outside IdentifierIBAN has a display form, and a
			// store stripping punctuation from arbitrary identifiers would merge
			// two addresses a scheme keeps apart.
			held, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, deposit.Identifier{Scheme: pan, Value: "4111-1111"})
			if err != nil {
				return err
			}
			if len(held) != 1 || held[0].ID != "dep_3" {
				t.Fatalf("exact PAN lookup = %#v, want just dep_3", held)
			}
			stripped, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, deposit.Identifier{Scheme: pan, Value: "41111111"})
			if err != nil {
				return err
			}
			if len(stripped) != 0 {
				t.Fatalf("PAN lookup with the hyphen removed = %#v, want none — only an IBAN has a display form", stripped)
			}
			return nil
		})

		// Canonical query, separated row: the reverse direction, which a store
		// that compacted only the QUERY would fail. It is the other bank's
		// database, which is where the separated row lives.
		viewDeposit(t, otherBank, func(ctx context.Context, tx deposit.Tx) error {
			hit, err := tx.ListDepositAccountsByIdentifier(ctx, bookB, stored)
			if err != nil {
				return err
			}
			if len(hit) != 1 || hit[0].ID != "dep_2" {
				t.Fatalf("canonical lookup of a separated row = %#v, want just dep_2", hit)
			}
			return nil
		})
	})

	// CASE is folded too, and on the ROW as well as the query.
	//
	// It is a separate case because it is a separate half of iban.Compact, and a
	// store that stripped separators without folding case passes every direction
	// above: an address is upper-cased everywhere it is minted, so nothing else
	// here writes a row that would notice. The query side is folded before it
	// arrives — it is MatchValue's output — so a store folding only what it was
	// given would look correct and would answer "no such account" to the one row
	// that needed it.
	//
	// A lower-cased row is not a state the register writes. It is exactly the
	// state this contract has to cover anyway: the store is handed rows, and a
	// comparison rule with an unstated precondition on its inputs is not the rule
	// deposit.Identifier.MatchValue states.
	t.Run("ListDepositAccountsByIdentifierFoldsCaseOnAnIBAN", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		lowered := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "de20 9990 0001 0000 0000 01"}
		canonical := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{lowered},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, quoted := range []deposit.Identifier{canonical, lowered} {
				hit, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, quoted)
				if err != nil {
					return err
				}
				if len(hit) != 1 || hit[0].ID != "dep_1" {
					t.Fatalf("lookup of %q against a lower-cased row = %#v, want just dep_1", quoted.Value, hit)
				}
			}
			return nil
		})
	})

	// The store does NOT enforce uniqueness, and this test is what keeps it
	// that way.
	//
	// It is the same job ParentReferencesAreNotEnforced does. "One bank issues
	// an address once" is a domain rule that deposit.Register enforces by
	// reading before it writes; a UNIQUE constraint would fire on the race that
	// read-then-write leaves open, which the register does not, and would fire as
	// a constraint violation rather than as ErrIdentifierTaken. The resulting
	// ambiguity is caught at READ time instead, by Register.ResolveIdentifier.
	t.Run("IdentifierUniquenessIsNotEnforced", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		iban := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}

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

	// Neither store enforces uniqueness ACROSS SPELLINGS either, and this is the
	// case that keeps the write-side rule where it belongs.
	//
	// deposit.Register.checkIdentifierFreeTx refuses a second account taking an
	// address another one at the bank already holds, and since the lookup became
	// canonical that refusal covers both spellings of one IBAN. The refusal is a
	// READ followed by a write, in the domain layer, with nothing behind it —
	// and it has to stay there. A UNIQUE index on the compacted value would be
	// the obvious way to make the schema enforce the same thing, and it would
	// refuse under the race that has no lock over it — where the register does
	// not — and refuse as a constraint violation rather than as the domain's
	// sentinel.
	//
	// So the store must ACCEPT the pair the register refuses, and one lookup
	// must return both accounts — which is what makes the resulting ambiguity
	// visible at read time (Register.ResolveIdentifier, and the network sweep
	// above it) rather than lost in a constraint violation.
	t.Run("IdentifierUniquenessIsNotEnforcedAcrossSpellings", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)
		canonical := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20999000010000000001"}
		grouped := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "DE20 9990 0001 0000 0000 01"}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_1", Name: "Alice", Asset: "EUR",
				Identifiers: []deposit.Identifier{canonical},
			}); err != nil {
				return err
			}
			// The write a race gets past the register. It must succeed on both
			// stores; a constraint refusing it here is the divergence.
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_2", Name: "Aaron", Asset: "EUR",
				Identifiers: []deposit.Identifier{grouped},
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, quoted := range []deposit.Identifier{canonical, grouped} {
				both, err := tx.ListDepositAccountsByIdentifier(ctx, bookA, quoted)
				if err != nil {
					return err
				}
				if len(both) != 2 {
					t.Fatalf("lookup of %q returned %d accounts, want both spellings of the one address — "+
						"the ambiguity has to be visible at read time, because no constraint stopped the write",
						quoted.Value, len(both))
				}
			}
			return nil
		})
	})

	t.Run("GetOnMissingDepositRowsReturnsSentinels", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		// A store that reports "not found" as anything but the domain sentinel
		// turns every 404 in the deposit API into a 500.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_1", early)); err != nil {
				return err
			}
			if err := tx.PutHold(ctx, bookA, hold("hld_1", "dep_1", 100, deposit.HoldActive, early, time.Time{})); err != nil {
				return err
			}
			return tx.PutSnapshot(ctx, bookA, snapshot("dep_1", day(15)))
		})

		// Unknown IDs in a book that holds a row of every kind.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			_, err := tx.GetDepositAccount(ctx, bookA, "dep_nope")
			assertErrorIs(t, "GetDepositAccount on an unknown account", err, deposit.ErrAccountNotFound)

			_, err = tx.GetHold(ctx, bookA, "hld_nope")
			assertErrorIs(t, "GetHold on an unknown hold", err, deposit.ErrHoldNotFound)

			_, err = tx.GetSnapshot(ctx, bookA, "dep_1", deposit.SnapshotDateKey(day(99)))
			assertErrorIs(t, "GetSnapshot on a date with no snapshot", err, deposit.ErrSnapshotNotFound)

			_, err = tx.GetSnapshot(ctx, bookA, "dep_nope", deposit.SnapshotDateKey(day(15)))
			assertErrorIs(t, "GetSnapshot on an unknown account", err, deposit.ErrSnapshotNotFound)
			return nil
		})

		// The same IDs in another bank's database are equally not found.
		other := openDeposit(t, newStore, bookB)
		viewDeposit(t, other, func(ctx context.Context, tx deposit.Tx) error {
			_, err := tx.GetDepositAccount(ctx, bookB, "dep_1")
			assertErrorIs(t, "GetDepositAccount across books", err, deposit.ErrAccountNotFound)

			_, err = tx.GetHold(ctx, bookB, "hld_1")
			assertErrorIs(t, "GetHold across books", err, deposit.ErrHoldNotFound)

			_, err = tx.GetSnapshot(ctx, bookB, "dep_1", deposit.SnapshotDateKey(day(15)))
			assertErrorIs(t, "GetSnapshot across books", err, deposit.ErrSnapshotNotFound)
			return nil
		})

		// And in a book that has never been written to at all — a third store,
		// for the reason above.
		empty := openDeposit(t, newStore, "book-empty")
		viewDeposit(t, empty, func(ctx context.Context, tx deposit.Tx) error {
			_, err := tx.GetDepositAccount(ctx, "book-empty", "dep_1")
			assertErrorIs(t, "GetDepositAccount in an empty book", err, deposit.ErrAccountNotFound)

			_, err = tx.GetHold(ctx, "book-empty", "hld_1")
			assertErrorIs(t, "GetHold in an empty book", err, deposit.ErrHoldNotFound)

			_, err = tx.GetSnapshot(ctx, "book-empty", "dep_1", deposit.SnapshotDateKey(day(15)))
			assertErrorIs(t, "GetSnapshot in an empty book", err, deposit.ErrSnapshotNotFound)
			return nil
		})
	})

	t.Run("DepositListOrderingIsCreatedAtThenSeq", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		late := early.Add(time.Hour)

		// Same shape and same three rules as the ledger's ordering fixture: a
		// CreatedAt tie, IDs spanning the 9 -> 10 boundary so lexicographic ID
		// order disagrees with insertion order, and the row inserted FIRST
		// carrying the LATEST CreatedAt. Ordering by (CreatedAt, ID) fails here,
		// and so does ordering by sequence alone.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, a := range []deposit.Account{
				{ID: "dep_10", Name: "latest, inserted first", CreatedAt: late},
				{ID: "dep_8", Name: "first", CreatedAt: early},
				{ID: "dep_20", Name: "second", CreatedAt: early},
				{ID: "dep_9", Name: "third", CreatedAt: early},
			} {
				if err := tx.PutDepositAccount(ctx, bookA, a); err != nil {
					return err
				}
			}
			// hld_z belongs to another account, so the per-account listing keeps
			// the same trap rather than accidentally being in ID order.
			for _, h := range []deposit.Hold{
				{ID: "hld_10", AccountID: "dep_8", Amount: 10, CreatedAt: late},
				{ID: "hld_8", AccountID: "dep_8", Amount: 10, CreatedAt: early},
				{ID: "hld_z", AccountID: "dep_20", Amount: 10, CreatedAt: early},
				{ID: "hld_20", AccountID: "dep_8", Amount: 10, CreatedAt: early},
				{ID: "hld_9", AccountID: "dep_8", Amount: 10, CreatedAt: early},
			} {
				if err := tx.PutHold(ctx, bookA, h); err != nil {
					return err
				}
			}
			// Snapshots order by business date, and are inserted backwards.
			for _, d := range []int{17, 15, 16} {
				if err := tx.PutSnapshot(ctx, bookA, snapshot("dep_8", day(d))); err != nil {
					return err
				}
			}
			return tx.PutSnapshot(ctx, bookA, snapshot("dep_20", day(1)))
		})

		var accounts []deposit.Account
		var holds []deposit.Hold
		var snaps []deposit.Snapshot
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			var err error
			if accounts, err = tx.ListDepositAccounts(ctx, bookA); err != nil {
				return err
			}
			if holds, err = tx.ListHoldsForAccount(ctx, bookA, "dep_8"); err != nil {
				return err
			}
			snaps, err = tx.ListSnapshotsForAccount(ctx, bookA, "dep_8")
			return err
		})

		assertOrder(t, "ListDepositAccounts", ids(accounts, func(a deposit.Account) string { return string(a.ID) }),
			"dep_8", "dep_20", "dep_9", "dep_10")

		assertOrder(t, "ListHoldsForAccount", ids(holds, func(h deposit.Hold) string { return string(h.ID) }),
			"hld_8", "hld_20", "hld_9", "hld_10")

		assertOrder(t, "ListSnapshotsForAccount", ids(snaps, func(sn deposit.Snapshot) string {
			return deposit.SnapshotDateKey(sn.Date)
		}), "2025-01-15", "2025-01-16", "2025-01-17")

		// An upsert keeps a row where it was: freezing a customer's account must
		// not move them to the bottom of the list.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_8", Name: "first", Status: deposit.Frozen, CreatedAt: early,
			})
		})
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			reordered, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertOrder(t, "ListDepositAccounts after an upsert", ids(reordered, func(a deposit.Account) string { return string(a.ID) }),
				"dep_8", "dep_20", "dep_9", "dep_10")
			return nil
		})

		// Unknown accounts enumerate to empty rather than erroring; the deposit
		// listings are total.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			hs, err := tx.ListHoldsForAccount(ctx, bookA, "dep_nope")
			if err != nil {
				return err
			}
			assertEqual(t, "holds for an unknown account", len(hs), 0)
			sn, err := tx.ListSnapshotsForAccount(ctx, bookA, "dep_nope")
			if err != nil {
				return err
			}
			assertEqual(t, "snapshots for an unknown account", len(sn), 0)
			return nil
		})
	})

	t.Run("ActiveHoldTotalExcludesReleasedCapturedAndExpired", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		now := early.Add(12 * time.Hour)
		yesterday := early.Add(-24 * time.Hour)
		tomorrow := early.Add(48 * time.Hour)

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, h := range []deposit.Hold{
				// Counted: active, and either never expiring or expiring later.
				hold("hld_1", "dep_1", 100, deposit.HoldActive, early, time.Time{}),
				hold("hld_2", "dep_1", 200, deposit.HoldActive, early, tomorrow),

				hold("hld_3", "dep_1", 400, deposit.HoldReleased, early, time.Time{}),
				hold("hld_4", "dep_1", 800, deposit.HoldCaptured, early, time.Time{}),
				// Not counted: expired before now.
				hold("hld_5", "dep_1", 1600, deposit.HoldActive, early, yesterday),
				// Not counted: another account.
				hold("hld_6", "dep_2", 3200, deposit.HoldActive, early, time.Time{}),
			} {
				if err := tx.PutHold(ctx, bookA, h); err != nil {
					return err
				}
			}
			return nil
		})
		// Another bank's dep_1, in another bank's database, holding a much
		// larger amount: the aggregate must not reach it.
		other := openDeposit(t, newStore, bookB)
		updateDeposit(t, other, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutHold(ctx, bookB, hold("hld_7", "dep_1", 6400, deposit.HoldActive, early, time.Time{}))
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			total, err := tx.ActiveHoldTotal(ctx, bookA, "dep_1", now)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total", total, ledger.Amount(300))

			// The other account in the same book has only its own hold.
			other, err := tx.ActiveHoldTotal(ctx, bookA, "dep_2", now)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total for the other account", other, ledger.Amount(3200))

			// Like BookBalance this is an aggregate: an unknown account is 0,
			// not an error.
			unknown, err := tx.ActiveHoldTotal(ctx, bookA, "dep_nope", now)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total for an unknown account", unknown, ledger.Amount(0))
			return nil
		})

		// Expiry is evaluated against the `now` passed in, not against the
		// store's clock: rewind past hld_5's expiry and it counts again.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			total, err := tx.ActiveHoldTotal(ctx, bookA, "dep_1", yesterday.Add(-time.Hour))
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total before every expiry", total, ledger.Amount(1900))
			return nil
		})

		// A hold expiring exactly at `now` has not expired yet — expiry is
		// strictly before now, the same boundary the Register used.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			total, err := tx.ActiveHoldTotal(ctx, bookA, "dep_1", tomorrow)
			if err != nil {
				return err
			}
			assertEqual(t, "hold expiring exactly at now still counts", total, ledger.Amount(300))
			return nil
		})
	})

	t.Run("SnapshotUpsertsByAccountAndDate", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		first := deposit.Snapshot{
			AccountID: "dep_1",
			Date:      day(15),
			Balance:   deposit.Balance{Book: 1000, Holds: 100, Available: 900},
			TakenAt:   early,
		}
		second := deposit.Snapshot{
			AccountID: "dep_1",
			// Same business day, later in the day: the same identity, so this
			// replaces the first rather than adding a row.
			Date:    day(15).Add(23 * time.Hour),
			Balance: deposit.Balance{Book: 2000, Holds: 0, Available: 2000},
			TakenAt: early.Add(23 * time.Hour),
		}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutSnapshot(ctx, bookA, first); err != nil {
				return err
			}
			// A different account on the same date is a different snapshot.
			if err := tx.PutSnapshot(ctx, bookA, deposit.Snapshot{AccountID: "dep_2", Date: day(15)}); err != nil {
				return err
			}
			// And the same account on a different date.
			if err := tx.PutSnapshot(ctx, bookA, deposit.Snapshot{AccountID: "dep_1", Date: day(16)}); err != nil {
				return err
			}
			return tx.PutSnapshot(ctx, bookA, second)
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetSnapshot(ctx, bookA, "dep_1", deposit.SnapshotDateKey(day(15)))
			if err != nil {
				return err
			}
			assertEqual(t, "overwritten book balance", got.Balance.Book, ledger.Amount(2000))
			assertEqual(t, "overwritten holds", got.Balance.Holds, ledger.Amount(0))
			assertEqual(t, "overwritten available", got.Balance.Available, ledger.Amount(2000))

			// Two rows for dep_1: 2025-01-15 (overwritten once) and 2025-01-16.
			all, err := tx.ListSnapshotsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "snapshots for dep_1", len(all), 2)

			// dep_2's snapshot on the same date is untouched.
			other, err := tx.ListSnapshotsForAccount(ctx, bookA, "dep_2")
			if err != nil {
				return err
			}
			assertEqual(t, "snapshots for dep_2", len(other), 1)
			return nil
		})
	})

	t.Run("UpdateRollsBackDepositAndLedgerWritesTogether", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		// The reason deposit.Tx embeds ledger.Tx: a capture writes a hold and
		// posts a GL transaction, and a failure must undo both. Seed one
		// committed hold, then fail a unit of work that touches both layers.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_1", early)); err != nil {
				return err
			}
			return tx.PutHold(ctx, bookA, hold("hld_1", "dep_1", 500, deposit.HoldActive, early, time.Time{}))
		})

		boom := errors.New("storetest: deliberate failure")
		err := s.Update(context.Background(), func(ctx context.Context, tx deposit.Tx) error {
			// Deposit-layer writes.
			captured := hold("hld_1", "dep_1", 500, deposit.HoldCaptured, early, time.Time{})
			if err := tx.PutHold(ctx, bookA, captured); err != nil {
				return err
			}
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_2", early)); err != nil {
				return err
			}
			if err := tx.PutSnapshot(ctx, bookA, snapshot("dep_1", day(15))); err != nil {
				return err
			}
			// Ledger-layer writes through the very same Tx.
			if err := tx.PutTransaction(ctx, bookA, transaction("tx_1", "")); err != nil {
				return err
			}
			if err := tx.AppendAudit(ctx, ledger.AuditEvent{
				ID: "evt_1", BookID: bookA, Scope: ledger.ScopeDeposit, Type: ledger.EventHoldCaptured,
			}); err != nil {
				return err
			}
			return boom
		})
		assertErrorIs(t, "Update return", err, boom)

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			// The hold is back to Active: the status write did not survive.
			h, err := tx.GetHold(ctx, bookA, "hld_1")
			if err != nil {
				return err
			}
			assertEqual(t, "hold status after rollback", h.Status.String(), deposit.HoldActive.String())

			_, err = tx.GetDepositAccount(ctx, bookA, "dep_2")
			assertErrorIs(t, "account written by the failed unit of work", err, deposit.ErrAccountNotFound)

			_, err = tx.GetSnapshot(ctx, bookA, "dep_1", deposit.SnapshotDateKey(day(15)))
			assertErrorIs(t, "snapshot written by the failed unit of work", err, deposit.ErrSnapshotNotFound)

			// And the ledger side rolled back with it.
			_, err = tx.GetTransaction(ctx, bookA, "tx_1")
			assertErrorIs(t, "GL transaction from the failed unit of work", err, ledger.ErrTransactionNotFound)

			events, err := tx.ListAudit(ctx, ledger.AuditFilter{})
			if err != nil {
				return err
			}
			assertEqual(t, "audit events after rollback", len(events), 0)
			return nil
		})
	})

	t.Run("ResetClearsDepositState", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_1", early)); err != nil {
				return err
			}
			if err := tx.PutHold(ctx, bookA, hold("hld_1", "dep_1", 500, deposit.HoldActive, early, time.Time{})); err != nil {
				return err
			}
			if err := tx.PutSnapshot(ctx, bookA, snapshot("dep_1", day(15))); err != nil {
				return err
			}
			// Floating: no overlay, priced by the product. The overlay shape is
			// exercised by OverdraftTermsTimeline and by the round-trip subtest.
			return tx.PutOverdraftTerms(ctx, bookA, deposit.OverdraftTerms{
				AccountID: "dep_1", EffectiveFrom: day(1), ProductID: "prd_basic",
				OverdraftLimit: 500, CreatedAt: early,
			})
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			accounts, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "deposit accounts after reset", len(accounts), 0)

			holds, err := tx.ListHoldsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "holds after reset", len(holds), 0)

			snaps, err := tx.ListSnapshotsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "snapshots after reset", len(snaps), 0)

			total, err := tx.ActiveHoldTotal(ctx, bookA, "dep_1", early)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total after reset", total, ledger.Amount(0))

			terms, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "overdraft terms after reset", len(terms), 0)
			return nil
		})
	})

	// The accrual state is the set of fields on a deposit account that a store
	// could plausibly drop without any other subtest noticing: nothing else in
	// this suite writes them, and an accrual that silently starts from zero
	// every day looks like a working system that charges no interest.
	//
	// The credit terms are not among them — they are rows in their own table,
	// covered by OverdraftTermsTimeline below. What is left on the account is what
	// an accrual carries FORWARD rather than what prices it.
	t.Run("AccrualStateRoundTrip", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		accrual := time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)
		want := deposit.Account{
			ID: "dep_1", GLAccount: "200.cust.001", Name: "Bruno", Asset: "EUR",
			Status:  deposit.Active,
			Accrued: 61_643_835, AccruedGross: 123_287_670,
			LastAccrualDate: accrual,
			InterestGL:      "100.accr.001", CreatedAt: early,
		}
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, want)
		})

		check := func(label string, got deposit.Account) {
			t.Helper()
			assertEqual(t, label+" accrued", got.Accrued, want.Accrued)
			// A store that drops accrued_gross silently re-derives the whole of
			// the account's life as a fresh delta every night and charges the
			// same interest over and over, which no other subtest would notice.
			assertEqual(t, label+" accrued gross", got.AccruedGross, want.AccruedGross)
			assertEqual(t, label+" interest gl", string(got.InterestGL), string(want.InterestGL))
			if !got.LastAccrualDate.Equal(want.LastAccrualDate) {
				t.Errorf("%s last accrual date: got %v, want %v", label, got.LastAccrualDate, want.LastAccrualDate)
			}
		}

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			one, err := tx.GetDepositAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			check("GetDepositAccount", one)

			listed, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "accounts listed", len(listed), 1)
			check("ListDepositAccounts", listed[0])
			return nil
		})

		// An account that has never accrued keeps zero state and no receivable
		// account, rather than an empty one nothing will ever post to.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: "dep_2", GLAccount: "200.cust.002", Name: "Bella", Asset: "EUR",
				Status: deposit.Active, CreatedAt: early,
			})
		})
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			plain, err := tx.GetDepositAccount(ctx, bookA, "dep_2")
			if err != nil {
				return err
			}
			assertEqual(t, "no-facility accrued", plain.Accrued, interest.Accrued(0))
			assertEqual(t, "no-facility accrued gross", plain.AccruedGross, interest.Accrued(0))
			assertEqual(t, "no-facility interest gl", string(plain.InterestGL), "")
			if !plain.LastAccrualDate.IsZero() {
				t.Errorf("no-facility last accrual date = %v, want zero", plain.LastAccrualDate)
			}
			return nil
		})
	})

	// The terms timeline. Everything the accrual depends on is here: ordering
	// (termsAt binary-searches the slice List hands it), the day-granular
	// upsert identity, and the four positions the as-of lookup has to answer
	// for. A store that got any of them wrong would produce interest figures
	// nobody could reproduce, and no other subtest would notice.
	t.Run("OverdraftTermsTimeline", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		jan := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		mar := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
		jun := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

		// A NEGOTIATED row, so that the assertions below stay about the store's
		// ordering and identity rather than about a catalogue this suite does not
		// build. The floating shape is exercised by ResetClearsDepositState and by
		// OverdraftTermsPricingOverlayRoundTrip.
		row := func(from time.Time, rate interest.Rate) deposit.OverdraftTerms {
			return deposit.OverdraftTerms{
				AccountID: "dep_1", EffectiveFrom: from, ProductID: "prd_basic",
				OverdraftLimit: 50_000,
				Pricing: &product.OverdraftPricing{
					Rate: rate, UnarrangedRate: rate * 2, DayCount: interest.Thirty360,
				},
				CreatedAt: early,
			}
		}

		// Written out of order on purpose: the store owns the ordering, and a
		// caller may enter a backdated repricing at any time.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, r := range []deposit.OverdraftTerms{row(jun, 300_000), row(jan, 100_000), row(mar, 200_000)} {
				if err := tx.PutOverdraftTerms(ctx, bookA, r); err != nil {
					return err
				}
			}
			return nil
		})
		// A second bank's rows must be invisible to the first, and they are in a
		// second bank's database.
		otherBank := openDeposit(t, newStore, bookB)
		updateDeposit(t, otherBank, func(ctx context.Context, tx deposit.Tx) error {
			return tx.PutOverdraftTerms(ctx, bookB, deposit.OverdraftTerms{
				AccountID: "dep_1", EffectiveFrom: jan, ProductID: "prd_basic",
				Pricing:   &product.OverdraftPricing{Rate: 999_000},
				CreatedAt: early,
			})
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "timeline length", len(rows), 3)
			assertEqual(t, "first row rate", rows[0].Pricing.Rate, interest.Rate(100_000))
			assertEqual(t, "second row rate", rows[1].Pricing.Rate, interest.Rate(200_000))
			assertEqual(t, "third row rate", rows[2].Pricing.Rate, interest.Rate(300_000))
			for i := 1; i < len(rows); i++ {
				if !rows[i-1].EffectiveFrom.Before(rows[i].EffectiveFrom) {
					t.Fatalf("timeline not ascending at %d: %v then %v",
						i, rows[i-1].EffectiveFrom, rows[i].EffectiveFrom)
				}
			}
			// Every field round-trips, not just the rate: a dropped day count
			// is a product silently repriced onto another convention.
			assertEqual(t, "limit", rows[0].OverdraftLimit, ledger.Amount(50_000))
			assertEqual(t, "unarranged", rows[0].Pricing.UnarrangedRate, interest.Rate(200_000))
			assertEqual(t, "day count", rows[0].Pricing.DayCount, interest.Thirty360)
			assertEqual(t, "product", string(rows[0].ProductID), "prd_basic")
			assertEqual(t, "account id", string(rows[0].AccountID), "dep_1")
			if !rows[0].CreatedAt.Equal(early) {
				t.Errorf("created at: got %v, want %v", rows[0].CreatedAt, early)
			}
			if !rows[0].EffectiveFrom.Equal(jan) {
				t.Errorf("effective from: got %v, want %v", rows[0].EffectiveFrom, jan)
			}

			return nil
		})
		viewDeposit(t, otherBank, func(ctx context.Context, tx deposit.Tx) error {
			other, err := tx.ListOverdraftTermsForAccount(ctx, bookB, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "book-b timeline length", len(other), 1)
			assertEqual(t, "book-b rate is its own", other[0].Pricing.Rate, interest.Rate(999_000))
			return nil
		})

		// The four as-of positions.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			_, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1", time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC))
			if !errors.Is(err, deposit.ErrTermsNotFound) {
				t.Errorf("before the first row: got %v, want ErrTermsNotFound", err)
			}

			onBoundary, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1", mar)
			if err != nil {
				return err
			}
			assertEqual(t, "on a boundary", onBoundary.Pricing.Rate, interest.Rate(200_000))

			between, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1",
				time.Date(2025, 4, 15, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return err
			}
			assertEqual(t, "between rows takes the earlier", between.Pricing.Rate, interest.Rate(200_000))

			after, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1",
				time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return err
			}
			assertEqual(t, "after the last row", after.Pricing.Rate, interest.Rate(300_000))

			// An account with no rows at all is ErrTermsNotFound, not a zero
			// row that would read as a real interest-free product.
			if _, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_missing", mar); !errors.Is(err, deposit.ErrTermsNotFound) {
				t.Errorf("unknown account: got %v, want ErrTermsNotFound", err)
			}
			return nil
		})

		// Upsert on the same (account, effective DAY): the later row wins and
		// the timeline does not grow. The second write carries a time of day,
		// which must land on the same row — the identity is a day, not a moment.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			repriced := row(mar.Add(17*time.Hour), 250_000)
			return tx.PutOverdraftTerms(ctx, bookA, repriced)
		})
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "timeline length after upsert", len(rows), 3)
			assertEqual(t, "upserted rate", rows[1].Pricing.Rate, interest.Rate(250_000))
			return nil
		})
	})

	// The overlay is the deposit layer's only pointer field, and the only place
	// a store can conflate "float from the product" with "interest-free". Both
	// stores must round-trip the distinction, and neither may hand a reader a
	// pointer into its own state.
	t.Run("OverdraftTermsPricingOverlayRoundTrip", func(t *testing.T) {
		s := openDeposit(t, newStore, bookA)

		overlay := product.OverdraftPricing{Rate: 90_000, UnarrangedRate: 350_000, DayCount: interest.Thirty360}
		free := product.OverdraftPricing{}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, row := range []deposit.OverdraftTerms{
				{AccountID: "dep_1", EffectiveFrom: day(1), ProductID: "prd_basic", OverdraftLimit: 500},
				{AccountID: "dep_1", EffectiveFrom: day(10), ProductID: "prd_basic", OverdraftLimit: 500, Pricing: &overlay},
				{AccountID: "dep_1", EffectiveFrom: day(20), ProductID: "prd_basic", OverdraftLimit: 500, Pricing: &free},
			} {
				if err := tx.PutOverdraftTerms(ctx, bookA, row); err != nil {
					return err
				}
			}
			return nil
		})

		// A writer that keeps its argument must not be able to rewrite a stored
		// price afterwards.
		overlay.Rate = 7

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "rows", len(rows), 3)

			if rows[0].Pricing != nil {
				t.Error("a floating row came back with a pricing")
			}
			assertEqual(t, "product", string(rows[0].ProductID), "prd_basic")

			if rows[1].Pricing == nil {
				t.Fatal("the overlay was dropped")
			}
			assertEqual(t, "overlay rate", int64(rows[1].Pricing.Rate), int64(90_000))
			assertEqual(t, "overlay unarranged", int64(rows[1].Pricing.UnarrangedRate), int64(350_000))
			assertEqual(t, "overlay day count", int(rows[1].Pricing.DayCount), int(interest.Thirty360))

			if rows[2].Pricing == nil {
				t.Fatal("a zero-rate overlay came back as floating; free and floating are different")
			}
			assertEqual(t, "free overlay rate", int64(rows[2].Pricing.Rate), int64(0))

			// Mutating what a reader was handed must not change stored state.
			rows[1].Pricing.Rate = 1
			return nil
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the stored overlay rate", int64(rows[1].Pricing.Rate), int64(90_000))

			// The as-of reader is the one balanceTx uses, and it must copy too.
			asOf, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1", day(15))
			if err != nil {
				return err
			}
			asOf.Pricing.Rate = 2
			again, err := tx.GetOverdraftTermsAsOf(ctx, bookA, "dep_1", day(15))
			if err != nil {
				return err
			}
			assertEqual(t, "the stored overlay rate, as of", int64(again.Pricing.Rate), int64(90_000))
			return nil
		})
	})
}

// ---------------------------------------------------------------------------
// Deposit helpers
// ---------------------------------------------------------------------------

// early is the base instant for the deposit fixtures, matching the one the
// ledger suite uses.
var early = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// day builds a business date in January 2025, the granularity snapshots are
// identified at.
func day(n int) time.Time { return time.Date(2025, 1, n, 0, 0, 0, 0, time.UTC) }

func account(id deposit.AccountID, createdAt time.Time) deposit.Account {
	return deposit.Account{
		ID:        id,
		GLAccount: "200.100.001",
		Name:      string(id),
		Status:    deposit.Active,
		CreatedAt: createdAt,
	}
}

func hold(id deposit.HoldID, acct deposit.AccountID, amount ledger.Amount, status deposit.HoldStatus, createdAt, expiresAt time.Time) deposit.Hold {
	return deposit.Hold{
		ID:        id,
		AccountID: acct,
		Amount:    amount,
		ExpiresAt: expiresAt,
		Status:    status,
		CreatedAt: createdAt,
	}
}

func snapshot(acct deposit.AccountID, date time.Time) deposit.Snapshot {
	return deposit.Snapshot{
		AccountID: acct,
		Date:      date,
		Balance:   deposit.Balance{Book: 1000, Holds: 100, Available: 900},
		TakenAt:   early,
	}
}

// openDeposit builds a fresh store for one subtest and closes it when the
// subtest ends.
func openDeposit(t *testing.T, newStore func(*testing.T, ledger.BookID) deposit.Store, book ledger.BookID) deposit.Store {
	t.Helper()
	s := newStore(t, book)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// updateDeposit runs a unit of work that is expected to succeed.
func updateDeposit(t *testing.T, s deposit.Store, fn func(context.Context, deposit.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// viewDeposit runs a read-only unit of work that is expected to succeed.
func viewDeposit(t *testing.T, s deposit.Store, fn func(context.Context, deposit.Tx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}
