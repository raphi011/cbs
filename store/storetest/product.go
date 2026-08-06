package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// RunProduct runs the catalogue suite against a store.
//
// It talks only to product.Store and product.Tx — never to product.Catalogue —
// so what it pins is the storage contract: book scoping, not-found sentinels,
// listing order, upsert identity, and the two things a store could get subtly
// wrong without any other test noticing, which are that the as-of lookup skips
// drafts and that a returned row's pricing is a copy.
func RunProduct(t *testing.T, newStore func(*testing.T) product.Store) {
	t.Helper()

	// Helpers local to this suite.
	version := func(id product.ID, from time.Time, rate interest.Rate, publishedAt time.Time) product.Version {
		v := product.Version{
			ProductID: id, EffectiveFrom: from,
			Overdraft:   product.OverdraftPricing{Rate: rate, DayCount: interest.ACT365},
			PublishedAt: publishedAt, CreatedAt: early,
		}
		v.Hash = v.ComputeHash()
		return v
	}

	t.Run("ProductRoundTrip", func(t *testing.T) {
		s := openProduct(t, newStore)

		want := product.Product{
			ID: "prd_1", Name: "Basic Current Account",
			Kind: product.CurrentAccount, Retired: false, CreatedAt: early,
		}
		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			return tx.PutProduct(ctx, bookA, want)
		})
		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			got, err := tx.GetProduct(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "product", got, want)
			return nil
		})

		// Retiring is an upsert, and it must not move the row in its listing:
		// seq is assigned on insert and left alone by the update branch.
		retired := want
		retired.Retired = true
		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if err := tx.PutProduct(ctx, bookA, product.Product{
				ID: "prd_2", Name: "Premium", Kind: product.CurrentAccount, CreatedAt: early,
			}); err != nil {
				return err
			}
			return tx.PutProduct(ctx, bookA, retired)
		})
		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			list, err := tx.ListProducts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "products", len(list), 2)
			assertEqual(t, "first is still prd_1", string(list[0].ID), "prd_1")
			assertEqual(t, "prd_1 is retired", list[0].Retired, true)
			return nil
		})
	})

	t.Run("GetOnMissingRowsReturnsSentinels", func(t *testing.T) {
		s := openProduct(t, newStore)

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if _, err := tx.GetProduct(ctx, bookA, "prd_nope"); !errors.Is(err, product.ErrProductNotFound) {
				t.Errorf("GetProduct: %v, want ErrProductNotFound", err)
			}
			if _, err := tx.GetProductVersionAsOf(ctx, bookA, "prd_nope", day(1)); !errors.Is(err, product.ErrVersionNotFound) {
				t.Errorf("GetProductVersionAsOf: %v, want ErrVersionNotFound", err)
			}
			// A listing of nothing is empty, not an error: it is a listing, and
			// ListProducts of an empty book is the same shape.
			list, err := tx.ListProductVersions(ctx, bookA, "prd_nope")
			if err != nil {
				return err
			}
			assertEqual(t, "versions of an unknown product", len(list), 0)
			return nil
		})
	})

	t.Run("VersionTimelineIsAscendingAndUpsertsByDay", func(t *testing.T) {
		s := openProduct(t, newStore)

		jan, mar, jun := day(1), day(60), day(150)

		// Written out of order on purpose: the ORDER BY is the contract, not
		// the insertion order.
		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			for _, v := range []product.Version{
				version("prd_1", jun, 300_000, jun),
				version("prd_1", jan, 100_000, jan),
				version("prd_1", mar, 200_000, mar),
			} {
				if err := tx.PutProductVersion(ctx, bookA, v); err != nil {
					return err
				}
			}
			// Same product ID, other book: two products, and neither read
			// below may see this.
			return tx.PutProductVersion(ctx, bookB, version("prd_1", jan, 999_000, jan))
		})

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "versions", len(rows), 3)
			assertEqual(t, "first", int64(rows[0].Overdraft.Rate), int64(100_000))
			assertEqual(t, "second", int64(rows[1].Overdraft.Rate), int64(200_000))
			assertEqual(t, "third", int64(rows[2].Overdraft.Rate), int64(300_000))

			other, err := tx.ListProductVersions(ctx, bookB, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "book B is its own product", len(other), 1)
			assertEqual(t, "book B rate", int64(other[0].Overdraft.Rate), int64(999_000))

			as, err := tx.GetProductVersionAsOf(ctx, bookA, "prd_1", day(59))
			if err != nil {
				return err
			}
			assertEqual(t, "the day before a repricing", int64(as.Overdraft.Rate), int64(100_000))

			as, err = tx.GetProductVersionAsOf(ctx, bookA, "prd_1", mar)
			if err != nil {
				return err
			}
			assertEqual(t, "on the boundary", int64(as.Overdraft.Rate), int64(200_000))

			if _, err := tx.GetProductVersionAsOf(ctx, bookA, "prd_1", day(0)); !errors.Is(err, product.ErrVersionNotFound) {
				t.Errorf("before the first version: %v, want ErrVersionNotFound", err)
			}
			return nil
		})

		// A second version for the same effective DAY replaces the first.
		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			return tx.PutProductVersion(ctx, bookA, version("prd_1", mar.Add(11*time.Hour), 250_000, mar))
		})
		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "still three versions", len(rows), 3)
			assertEqual(t, "the day's version was replaced", int64(rows[1].Overdraft.Rate), int64(250_000))
			return nil
		})
	})

	// A store that returned drafts from the as-of lookup would price accounts
	// from a version nobody published, and every other subtest here would still
	// pass. It is the one behaviour in this suite worth its own case.
	t.Run("AsOfSkipsDraftsAndListIncludesThem", func(t *testing.T) {
		s := openProduct(t, newStore)

		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if err := tx.PutProductVersion(ctx, bookA, version("prd_1", day(1), 100_000, day(1))); err != nil {
				return err
			}
			// Drafted, never published: PublishedAt is the zero time.
			return tx.PutProductVersion(ctx, bookA, version("prd_1", day(30), 900_000, time.Time{}))
		})

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the draft is listed", len(rows), 2)
			assertEqual(t, "and is recognisable as one", rows[1].Published(), false)

			as, err := tx.GetProductVersionAsOf(ctx, bookA, "prd_1", day(45))
			if err != nil {
				return err
			}
			assertEqual(t, "the published row before the draft stays in force",
				int64(as.Overdraft.Rate), int64(100_000))
			return nil
		})
	})

	// An in-Go store holds values in maps, so a reader could be handed the very
	// row a later writer mutates; a SQL store cannot do that at all. The rule is
	// the same either way and is free on one of them, which is exactly why it has
	// to be written down rather than left to whichever store is underneath.
	t.Run("ReadRowsAreCopies", func(t *testing.T) {
		s := openProduct(t, newStore)

		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			return tx.PutProductVersion(ctx, bookA, version("prd_1", day(1), 100_000, day(1)))
		})

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			rows[0].Overdraft.Rate = 42
			return nil
		})
		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the stored rate", int64(rows[0].Overdraft.Rate), int64(100_000))
			return nil
		})
	})

	t.Run("WritesRollBackWithTheLedgersOwn", func(t *testing.T) {
		s := openProduct(t, newStore)
		boom := errors.New("boom")

		err := s.Update(context.Background(), func(ctx context.Context, tx product.Tx) error {
			if err := tx.PutProduct(ctx, bookA, product.Product{
				ID: "prd_1", Name: "Basic", CreatedAt: early,
			}); err != nil {
				return err
			}
			if err := tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "200.cust.001", SubledgerID: "cust", Name: "Anna",
				Type: ledger.Liability, Asset: "EUR",
			}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("Update: %v, want boom", err)
		}

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if _, err := tx.GetProduct(ctx, bookA, "prd_1"); !errors.Is(err, product.ErrProductNotFound) {
				t.Errorf("product survived the rollback: %v", err)
			}
			if _, err := tx.GetAccount(ctx, bookA, "200.cust.001"); err == nil {
				t.Error("GL account survived the rollback")
			}
			return nil
		})
	})

	t.Run("ResetClearsCatalogueState", func(t *testing.T) {
		s := openProduct(t, newStore)

		updateProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			if err := tx.PutProduct(ctx, bookA, product.Product{
				ID: "prd_1", Name: "Basic", CreatedAt: early,
			}); err != nil {
				return err
			}
			return tx.PutProductVersion(ctx, bookA, version("prd_1", day(1), 100_000, day(1)))
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		viewProduct(t, s, func(ctx context.Context, tx product.Tx) error {
			list, err := tx.ListProducts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "products after reset", len(list), 0)
			rows, err := tx.ListProductVersions(ctx, bookA, "prd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "versions after reset", len(rows), 0)
			return nil
		})
	})
}

// openProduct, updateProduct and viewProduct mirror openDeposit/updateDeposit/
// viewDeposit in deposit.go: one store per subtest, closed when it ends, and a
// t.Fatalf on any error the subtest did not expect.
func openProduct(t *testing.T, newStore func(*testing.T) product.Store) product.Store {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func updateProduct(t *testing.T, s product.Store, fn func(context.Context, product.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func viewProduct(t *testing.T, s product.Store, fn func(context.Context, product.Tx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}
