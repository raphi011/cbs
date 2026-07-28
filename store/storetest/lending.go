package storetest

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// RunLending runs the lending-layer conformance suite against a store. Every
// lending.Store implementation must pass it identically.
//
// Like the other suites it talks only to lending.Store and lending.Tx — never
// to lending.Portfolio — so what it pins is the storage contract: book scoping,
// the not-found sentinel, listing order, the composite instalment key, and the
// cross-layer rollback that lending.Tx embedding ledger.Tx exists to provide.
func RunLending(t *testing.T, newStore func(*testing.T) lending.Store) {
	t.Helper()

	t.Run("FacilityRoundTripsEveryField", func(t *testing.T) {
		s := openLending(t, newStore)

		// Every field, because most of them are the sort a store can silently
		// drop: a rate that reads back as zero is a loan that charges nothing,
		// and an accrued residue that reads back as zero is one that recomputes
		// its interest from scratch every day.
		want := lending.Facility{
			ID: "fac_1", Kind: lending.RevolvingLine, Name: "Bruno Line", Asset: "EUR",
			PrincipalGL: "100.loans.001", InterestGL: "100.accr.001",
			Commitment: 250_000, Rate: 180_000, DayCount: interest.ACT360,
			Method: lending.EqualPrincipal, TermMonths: 60, MinPayment: 20_000,
			Accrued: -356_180, LastAccrualDate: time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC),
			Arrears: lending.Arrears{
				DaysPastDue: 45, Bucket: lending.D30_59, NonPerforming: false,
				OldestUnpaidDue: time.Date(2025, 1, 18, 0, 0, 0, 0, time.UTC),
			},
			Status: lending.Active, OpenedAt: early,
			MaturityAt: time.Date(2030, 1, 15, 0, 0, 0, 0, time.UTC),
		}
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutFacility(ctx, bookA, want)
		})

		check := func(label string, got lending.Facility) {
			t.Helper()
			assertEqual(t, label+" kind", got.Kind, want.Kind)
			assertEqual(t, label+" asset", string(got.Asset), string(want.Asset))
			assertEqual(t, label+" principal gl", string(got.PrincipalGL), string(want.PrincipalGL))
			assertEqual(t, label+" interest gl", string(got.InterestGL), string(want.InterestGL))
			assertEqual(t, label+" commitment", got.Commitment, want.Commitment)
			assertEqual(t, label+" rate", got.Rate, want.Rate)
			assertEqual(t, label+" day count", got.DayCount, want.DayCount)
			assertEqual(t, label+" method", got.Method, want.Method)
			assertEqual(t, label+" term months", got.TermMonths, want.TermMonths)
			assertEqual(t, label+" min payment", got.MinPayment, want.MinPayment)
			// A negative residue is the ordinary state after a capitalization
			// that rounded up, so an unsigned column would corrupt it.
			assertEqual(t, label+" accrued", got.Accrued, want.Accrued)
			assertEqual(t, label+" days past due", got.Arrears.DaysPastDue, want.Arrears.DaysPastDue)
			assertEqual(t, label+" bucket", got.Arrears.Bucket, want.Arrears.Bucket)
			assertEqual(t, label+" non performing", got.Arrears.NonPerforming, want.Arrears.NonPerforming)
			assertEqual(t, label+" status", got.Status, want.Status)
			if !got.LastAccrualDate.Equal(want.LastAccrualDate) {
				t.Errorf("%s last accrual date: got %v, want %v", label, got.LastAccrualDate, want.LastAccrualDate)
			}
			if !got.Arrears.OldestUnpaidDue.Equal(want.Arrears.OldestUnpaidDue) {
				t.Errorf("%s oldest unpaid due: got %v, want %v", label, got.Arrears.OldestUnpaidDue, want.Arrears.OldestUnpaidDue)
			}
			if !got.MaturityAt.Equal(want.MaturityAt) {
				t.Errorf("%s maturity: got %v, want %v", label, got.MaturityAt, want.MaturityAt)
			}
		}

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			one, err := tx.GetFacility(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			check("GetFacility", one)

			all, err := tx.ListFacilities(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "facilities listed", len(all), 1)
			check("ListFacilities", all[0])
			return nil
		})

		// An open-ended line has no maturity, and a zero time must survive as
		// a zero time rather than as an epoch.
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutFacility(ctx, bookA, lending.Facility{
				ID: "fac_2", Kind: lending.RevolvingLine, Name: "Open ended", Asset: "EUR",
				Status: lending.Pending, OpenedAt: early,
			})
		})
		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			open, err := tx.GetFacility(ctx, bookA, "fac_2")
			if err != nil {
				return err
			}
			if !open.MaturityAt.IsZero() {
				t.Errorf("open-ended maturity = %v, want zero", open.MaturityAt)
			}
			if !open.LastAccrualDate.IsZero() {
				t.Errorf("never-accrued date = %v, want zero", open.LastAccrualDate)
			}
			return nil
		})
	})

	t.Run("GetOnAMissingFacilityReturnsTheSentinel", func(t *testing.T) {
		s := openLending(t, newStore)

		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutFacility(ctx, bookA, lending.Facility{
				ID: "fac_1", Name: "Loan", Asset: "EUR", OpenedAt: early,
			})
		})

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			_, err := tx.GetFacility(ctx, bookA, "fac_nope")
			assertErrorIs(t, "GetFacility on an unknown facility", err, lending.ErrFacilityNotFound)

			// The same ID in another book is equally not found: a lookup that
			// forgot to scope by book would return book-a's row here.
			_, err = tx.GetFacility(ctx, bookB, "fac_1")
			assertErrorIs(t, "GetFacility across books", err, lending.ErrFacilityNotFound)

			_, err = tx.GetFacility(ctx, "book-empty", "fac_1")
			assertErrorIs(t, "GetFacility in an empty book", err, lending.ErrFacilityNotFound)
			return nil
		})
	})

	t.Run("FacilitiesAreScopedByBook", func(t *testing.T) {
		s := openLending(t, newStore)

		const shared lending.FacilityID = "fac_1"
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			if err := tx.PutFacility(ctx, bookA, lending.Facility{
				ID: shared, Name: "Loan at A", Asset: "EUR", OpenedAt: early,
			}); err != nil {
				return err
			}
			return tx.PutFacility(ctx, bookB, lending.Facility{
				ID: shared, Name: "Loan at B", Asset: "EUR", OpenedAt: early,
			})
		})

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			inA, err := tx.GetFacility(ctx, bookA, shared)
			if err != nil {
				return err
			}
			assertEqual(t, "facility in book-a", inA.Name, "Loan at A")

			inB, err := tx.GetFacility(ctx, bookB, shared)
			if err != nil {
				return err
			}
			assertEqual(t, "facility in book-b", inB.Name, "Loan at B")

			listed, err := tx.ListFacilities(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "facilities listed for book-a", len(listed), 1)
			return nil
		})
	})

	t.Run("ListFacilitiesOrderingIsOpenedAtThenSeq", func(t *testing.T) {
		s := openLending(t, newStore)

		late := early.Add(time.Hour)
		// The row inserted FIRST carries the LATEST OpenedAt, and the IDs span
		// the 9 -> 10 boundary, so lexicographic ID order, insertion order and
		// timestamp order all disagree.
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			for _, f := range []lending.Facility{
				{ID: "fac_10", Name: "latest, inserted first", Asset: "EUR", OpenedAt: late},
				{ID: "fac_8", Name: "first", Asset: "EUR", OpenedAt: early},
				{ID: "fac_20", Name: "second", Asset: "EUR", OpenedAt: early},
				{ID: "fac_9", Name: "third", Asset: "EUR", OpenedAt: early},
			} {
				if err := tx.PutFacility(ctx, bookA, f); err != nil {
					return err
				}
			}
			return nil
		})

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			all, err := tx.ListFacilities(ctx, bookA)
			if err != nil {
				return err
			}
			assertOrder(t, "ListFacilities",
				ids(all, func(f lending.Facility) string { return string(f.ID) }),
				"fac_8", "fac_20", "fac_9", "fac_10")
			return nil
		})

		// An upsert keeps a row where it was: recording an accrual, or moving a
		// facility into arrears, must not move it to the end of the list.
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutFacility(ctx, bookA, lending.Facility{
				ID: "fac_8", Name: "renamed", Asset: "EUR", OpenedAt: early, Accrued: 999,
			})
		})
		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			all, err := tx.ListFacilities(ctx, bookA)
			if err != nil {
				return err
			}
			assertOrder(t, "ListFacilities after an upsert",
				ids(all, func(f lending.Facility) string { return string(f.ID) }),
				"fac_8", "fac_20", "fac_9", "fac_10")
			assertEqual(t, "facilities after an upsert", len(all), 4)
			return nil
		})
	})

	t.Run("InstallmentsAreKeyedByFacilityAndSeq", func(t *testing.T) {
		s := openLending(t, newStore)

		due := func(n int) time.Time {
			return time.Date(2025, time.February, 15, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
		}

		// Inserted out of order, and spanning the 9 -> 10 boundary so that a
		// store sorting on a stringified sequence would fail.
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			for _, seq := range []int{3, 1, 10, 2, 9} {
				if err := tx.PutInstallment(ctx, bookA, lending.Installment{
					FacilityID: "fac_1", Seq: seq, DueDate: due(seq),
					Principal: ledger.Amount(1000 + seq), Interest: ledger.Amount(seq),
				}); err != nil {
					return err
				}
			}
			// A second facility's schedule must not leak into the first's.
			return tx.PutInstallment(ctx, bookA, lending.Installment{
				FacilityID: "fac_2", Seq: 1, DueDate: due(1), Principal: 500,
			})
		})

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			got, err := tx.ListInstallments(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "instalments listed", len(got), 5)
			// Ordered by Seq, which is the contract order, not by insertion and
			// not by a stringified id.
			var seqs []string
			for _, i := range got {
				seqs = append(seqs, itoa(i.Seq))
			}
			assertOrder(t, "ListInstallments", seqs, "1", "2", "3", "9", "10")
			assertEqual(t, "first instalment principal", got[0].Principal, ledger.Amount(1001))

			other, err := tx.ListInstallments(ctx, bookA, "fac_2")
			if err != nil {
				return err
			}
			assertEqual(t, "the other facility's schedule", len(other), 1)

			// A facility with no schedule is an ordinary state, not an error.
			none, err := tx.ListInstallments(ctx, bookA, "fac_nope")
			if err != nil {
				return err
			}
			assertEqual(t, "no schedule", len(none), 0)

			// And instalments are book-scoped like everything else.
			acrossBooks, err := tx.ListInstallments(ctx, bookB, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "instalments in another book", len(acrossBooks), 0)
			return nil
		})

		// PutInstallment is an upsert on (book, facility, seq): recording a
		// payment against instalment 2 must update it, not append a sixth row.
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutInstallment(ctx, bookA, lending.Installment{
				FacilityID: "fac_1", Seq: 2, DueDate: due(2),
				Principal: 1002, Interest: 2, PaidPrincipal: 1002, PaidInterest: 2,
			})
		})
		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			got, err := tx.ListInstallments(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "instalments after an upsert", len(got), 5)
			assertEqual(t, "instalment 2 paid principal", got[1].PaidPrincipal, ledger.Amount(1002))
			assertEqual(t, "instalment 2 outstanding", got[1].Outstanding(), ledger.Amount(0))
			return nil
		})
	})

	t.Run("LendingAndLedgerWritesRollBackTogether", func(t *testing.T) {
		s := openLending(t, newStore)

		// The reason Tx embeds ledger.Tx: a disbursement is a facility write
		// and a GL posting, and a store where one survives the other's failure
		// would leave a loan with no money or money with no loan.
		boom := errors.New("storetest: deliberate failure")
		err := s.Update(context.Background(), func(ctx context.Context, tx lending.Tx) error {
			if err := tx.PutFacility(ctx, bookA, lending.Facility{
				ID: "fac_1", Name: "Loan", Asset: "EUR", OpenedAt: early,
			}); err != nil {
				return err
			}
			if err := tx.PutInstallment(ctx, bookA, lending.Installment{
				FacilityID: "fac_1", Seq: 1, DueDate: early, Principal: 100,
			}); err != nil {
				return err
			}
			if err := tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1")); err != nil {
				return err
			}
			return boom
		})
		assertErrorIs(t, "Update return", err, boom)

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			all, err := tx.ListFacilities(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "facilities after rollback", len(all), 0)

			sched, err := tx.ListInstallments(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "instalments after rollback", len(sched), 0)

			txs, err := tx.ListTransactions(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "transactions after rollback", len(txs), 0)
			return nil
		})
	})

	t.Run("ResetClearsLendingRows", func(t *testing.T) {
		s := openLending(t, newStore)

		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			if err := tx.PutFacility(ctx, bookA, lending.Facility{
				ID: "fac_1", Name: "Loan", Asset: "EUR", OpenedAt: early,
			}); err != nil {
				return err
			}
			return tx.PutInstallment(ctx, bookA, lending.Installment{
				FacilityID: "fac_1", Seq: 1, DueDate: early, Principal: 100,
			})
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			all, err := tx.ListFacilities(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "facilities after reset", len(all), 0)

			sched, err := tx.ListInstallments(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "instalments after reset", len(sched), 0)
			return nil
		})
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func openLending(t *testing.T, newStore func(*testing.T) lending.Store) lending.Store {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func updateLending(t *testing.T, s lending.Store, fn func(context.Context, lending.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func viewLending(t *testing.T, s lending.Store, fn func(context.Context, lending.Tx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}

// itoa keeps assertOrder's []string signature usable for the one listing whose
// order is a number rather than an ID.
func itoa(n int) string { return strconv.Itoa(n) }
