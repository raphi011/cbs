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

// RunLending runs the lending-layer suite against a store.
//
// Like the other suites it talks only to lending.Store and lending.Tx — never
// to lending.Portfolio — so what it pins is the storage contract: book scoping,
// the not-found sentinel, listing order, the composite instalment key, and the
// cross-layer rollback that lending.Tx embedding ledger.Tx exists to provide.
func RunLending(t *testing.T, newStore func(*testing.T, ledger.BookID) lending.Store) {
	t.Helper()

	// A facility names no account in the chart of accounts, so its asset is the
	// only thing that says which three control lines its money is under. A store
	// that dropped it would leave every balance unaskable.
	t.Run("FacilityAssetSurvivesTheRoundTrip", func(t *testing.T) {
		s := openLending(t, newStore, bookA)

		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutFacility(ctx, bookA, lending.Facility{
				ID: "fac_1", Kind: lending.TermLoan, Name: "Bruno Loan", Asset: "BTC",
				Status: lending.Active, OpenedAt: early,
			})
		})

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			f, err := tx.GetFacility(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "asset", string(f.Asset), "BTC")
			listed, err := tx.ListFacilities(ctx, bookA)
			if err != nil {
				return err
			}
			if len(listed) != 1 || listed[0].Asset != "BTC" {
				t.Errorf("ListFacilities = %+v, want one BTC facility", listed)
			}
			return nil
		})
	})

	t.Run("FacilityRoundTripsEveryField", func(t *testing.T) {
		s := openLending(t, newStore, bookA)

		// Every field, because most of them are the sort a store can silently
		// drop: a minimum-payment share that reads back as zero is a line that
		// bills nothing, and an accrued residue that reads back as zero is a
		// facility that recomputes its interest from scratch every day. The rate
		// and the day count are not here — they are facility_terms rows now, and
		// FacilityTermsTimeline below is where they round-trip.
		want := lending.Facility{
			ID: "fac_1", Kind: lending.RevolvingLine, Name: "Bruno Line", Asset: "EUR",
			Commitment: 250_000,
			Method:     lending.EqualPrincipal, TermMonths: 60, MinPayment: 20_000,
			Accrued: -356_180, AccruedGross: 1_479_452_040,
			LastAccrualDate: time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC),
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
			assertEqual(t, label+" commitment", got.Commitment, want.Commitment)
			assertEqual(t, label+" method", got.Method, want.Method)
			assertEqual(t, label+" term months", got.TermMonths, want.TermMonths)
			assertEqual(t, label+" min payment", got.MinPayment, want.MinPayment)
			// A negative residue is the ordinary state after a capitalization
			// that rounded up, so an unsigned column would corrupt it.
			assertEqual(t, label+" accrued", got.Accrued, want.Accrued)
			// AccruedGross is the whole-life recompute's running total, and a
			// store that dropped it would re-derive that whole life as a fresh
			// delta every night and charge the same interest over and over.
			// Nothing else in this suite would notice: it is not derivable from
			// any other column, and Accrued alone round-trips fine without it.
			assertEqual(t, label+" accrued gross", got.AccruedGross, want.AccruedGross)
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
			// A facility that has never been accrued carries the zero time, and
			// accrueFacilityTx hands it straight to DayCount.Days as the start of
			// the advancement guard's span. A store that read it back as an epoch
			// would still advance the guard; one that read it back as anything in
			// the FUTURE would refuse every run the facility ever has.
			if !open.LastAccrualDate.IsZero() {
				t.Errorf("never-accrued date = %v, want zero", open.LastAccrualDate)
			}
			assertEqual(t, "undrawn accrued gross", open.AccruedGross, interest.Accrued(0))
			return nil
		})
	})

	t.Run("GetOnAMissingFacilityReturnsTheSentinel", func(t *testing.T) {
		s := openLending(t, newStore, bookA)

		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutFacility(ctx, bookA, lending.Facility{
				ID: "fac_1", Name: "Loan", Asset: "EUR", OpenedAt: early,
			})
		})

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			_, err := tx.GetFacility(ctx, bookA, "fac_nope")
			assertErrorIs(t, "GetFacility on an unknown facility", err, lending.ErrFacilityNotFound)

			return nil
		})

		// The same ID in another bank's database is equally not found. This was
		// a second book in one store and the defect it caught was a lookup that
		// forgot to scope by book; what it catches now is a second bank's store
		// answering with the first bank's row.
		other := openLending(t, newStore, bookB)
		viewLending(t, other, func(ctx context.Context, tx lending.Tx) error {
			_, err := tx.GetFacility(ctx, bookB, "fac_1")
			assertErrorIs(t, "GetFacility across books", err, lending.ErrFacilityNotFound)
			return nil
		})
		empty := openLending(t, newStore, "book-empty")
		viewLending(t, empty, func(ctx context.Context, tx lending.Tx) error {
			_, err := tx.GetFacility(ctx, "book-empty", "fac_1")
			assertErrorIs(t, "GetFacility in an empty book", err, lending.ErrFacilityNotFound)
			return nil
		})
	})

	t.Run("FacilitiesAreScopedByBook", func(t *testing.T) {
		s := openLending(t, newStore, bookA)

		const shared lending.FacilityID = "fac_1"
		other := openLending(t, newStore, bookB)
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutFacility(ctx, bookA, lending.Facility{
				ID: shared, Name: "Loan at A", Asset: "EUR", OpenedAt: early,
			})
		})
		updateLending(t, other, func(ctx context.Context, tx lending.Tx) error {
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

			listed, err := tx.ListFacilities(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "facilities listed for book-a", len(listed), 1)
			return nil
		})
		viewLending(t, other, func(ctx context.Context, tx lending.Tx) error {
			inB, err := tx.GetFacility(ctx, bookB, shared)
			if err != nil {
				return err
			}
			assertEqual(t, "facility in book-b", inB.Name, "Loan at B")
			return nil
		})
	})

	t.Run("ListFacilitiesOrderingIsOpenedAtThenSeq", func(t *testing.T) {
		s := openLending(t, newStore, bookA)

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
		s := openLending(t, newStore, bookA)

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

			// Every field, for every row — not just the first. Interest and
			// DueDate are exactly the sort of field a store can silently drop
			// or mis-map (a wrong time zone, say) without any other assertion
			// here noticing, since ordering is by Seq and neither is read back
			// anywhere else.
			for _, i := range got {
				assertEqual(t, "instalment "+itoa(i.Seq)+" principal", i.Principal, ledger.Amount(1000+i.Seq))
				assertEqual(t, "instalment "+itoa(i.Seq)+" interest", i.Interest, ledger.Amount(i.Seq))
				if !i.DueDate.Equal(due(i.Seq)) {
					t.Errorf("instalment %d due date: got %v, want %v", i.Seq, i.DueDate, due(i.Seq))
				}
			}

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

			return nil
		})

		// And instalments are book-scoped like everything else, which is to say
		// they are in one bank's database and no other's.
		otherBank := openLending(t, newStore, bookB)
		viewLending(t, otherBank, func(ctx context.Context, tx lending.Tx) error {
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
			assertEqual(t, "instalment 2 paid interest", got[1].PaidInterest, ledger.Amount(2))
			assertEqual(t, "instalment 2 outstanding", got[1].Outstanding(), ledger.Amount(0))
			if !got[1].DueDate.Equal(due(2)) {
				t.Errorf("instalment 2 due date after upsert: got %v, want %v", got[1].DueDate, due(2))
			}

			// A row the upsert never touched must keep its own fields. Interest
			// is checked again here — not just before the upsert — because a
			// store that rebuilds every row on any write to the same facility
			// would pass the check above and still fail this one. PaidPrincipal
			// and PaidInterest are checked on a row that was NEVER put with a
			// non-zero payment, which is the only way to catch a store that
			// drops those two columns on ordinary inserts: the rewritten row
			// (Seq 2) would pass even with dropped columns if PutInstallment
			// zero-filled them by coincidence, but an untouched row would not.
			untouched := got[0] // Seq 1, never re-put.
			assertEqual(t, "untouched instalment interest", untouched.Interest, ledger.Amount(1))
			assertEqual(t, "untouched instalment paid principal", untouched.PaidPrincipal, ledger.Amount(0))
			assertEqual(t, "untouched instalment paid interest", untouched.PaidInterest, ledger.Amount(0))
			return nil
		})
	})

	t.Run("LendingAndLedgerWritesRollBackTogether", func(t *testing.T) {
		s := openLending(t, newStore, bookA)

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

	// The terms timeline. Everything the accrual depends on is here: ordering
	// (termsAt binary-searches the slice List hands it), the day-granular
	// upsert identity, and the four positions the as-of lookup has to answer
	// for. A store that got any of them wrong would produce interest figures
	// nobody could reproduce, and no other subtest would notice.
	t.Run("FacilityTermsTimeline", func(t *testing.T) {
		s := openLending(t, newStore, bookA)

		jan := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		mar := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
		jun := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

		row := func(from time.Time, rate interest.Rate) lending.FacilityTerms {
			return lending.FacilityTerms{
				FacilityID: "fac_1", EffectiveFrom: from,
				Rate: rate, DayCount: interest.ACT360,
				CreatedAt: early,
			}
		}

		// Written out of order on purpose: the store owns the ordering, and a
		// caller may enter a backdated repricing at any time.
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			for _, r := range []lending.FacilityTerms{row(jun, 120_000), row(jan, 60_000), row(mar, 90_000)} {
				if err := tx.PutFacilityTerms(ctx, bookA, r); err != nil {
					return err
				}
			}
			return nil
		})
		// A second bank's rows must be invisible to the first, and they are in a
		// second bank's database.
		otherBank := openLending(t, newStore, bookB)
		updateLending(t, otherBank, func(ctx context.Context, tx lending.Tx) error {
			return tx.PutFacilityTerms(ctx, bookB, lending.FacilityTerms{
				FacilityID: "fac_1", EffectiveFrom: jan, Rate: 999_000, CreatedAt: early,
			})
		})

		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			rows, err := tx.ListFacilityTerms(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "timeline length", len(rows), 3)
			assertEqual(t, "first row rate", rows[0].Rate, interest.Rate(60_000))
			assertEqual(t, "second row rate", rows[1].Rate, interest.Rate(90_000))
			assertEqual(t, "third row rate", rows[2].Rate, interest.Rate(120_000))
			for i := 1; i < len(rows); i++ {
				if !rows[i-1].EffectiveFrom.Before(rows[i].EffectiveFrom) {
					t.Fatalf("timeline not ascending at %d: %v then %v",
						i, rows[i-1].EffectiveFrom, rows[i].EffectiveFrom)
				}
			}
			// Every field round-trips, not just the rate: a dropped day count
			// is a product silently repriced onto another convention.
			assertEqual(t, "day count", rows[0].DayCount, interest.ACT360)
			assertEqual(t, "facility id", string(rows[0].FacilityID), "fac_1")
			if !rows[0].CreatedAt.Equal(early) {
				t.Errorf("created at: got %v, want %v", rows[0].CreatedAt, early)
			}
			if !rows[0].EffectiveFrom.Equal(jan) {
				t.Errorf("effective from: got %v, want %v", rows[0].EffectiveFrom, jan)
			}

			return nil
		})
		viewLending(t, otherBank, func(ctx context.Context, tx lending.Tx) error {
			other, err := tx.ListFacilityTerms(ctx, bookB, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "book-b timeline length", len(other), 1)
			assertEqual(t, "book-b rate is its own", other[0].Rate, interest.Rate(999_000))
			return nil
		})

		// The four as-of positions.
		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			_, err := tx.GetFacilityTermsAsOf(ctx, bookA, "fac_1", time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC))
			if !errors.Is(err, lending.ErrTermsNotFound) {
				t.Errorf("before the first row: got %v, want ErrTermsNotFound", err)
			}

			onBoundary, err := tx.GetFacilityTermsAsOf(ctx, bookA, "fac_1", mar)
			if err != nil {
				return err
			}
			assertEqual(t, "on a boundary", onBoundary.Rate, interest.Rate(90_000))

			between, err := tx.GetFacilityTermsAsOf(ctx, bookA, "fac_1",
				time.Date(2025, 4, 15, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return err
			}
			assertEqual(t, "between rows takes the earlier", between.Rate, interest.Rate(90_000))

			after, err := tx.GetFacilityTermsAsOf(ctx, bookA, "fac_1",
				time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return err
			}
			assertEqual(t, "after the last row", after.Rate, interest.Rate(120_000))

			// An unknown facility is ErrTermsNotFound, not a zero row that would
			// read as a real interest-free product.
			if _, err := tx.GetFacilityTermsAsOf(ctx, bookA, "fac_missing", mar); !errors.Is(err, lending.ErrTermsNotFound) {
				t.Errorf("unknown facility: got %v, want ErrTermsNotFound", err)
			}
			return nil
		})

		// Upsert on the same (facility, effective DAY): the later row wins and
		// the timeline does not grow. The second write carries a time of day,
		// which must land on the same row — the identity is a day, not a moment.
		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			repriced := row(mar.Add(17*time.Hour), 100_000)
			return tx.PutFacilityTerms(ctx, bookA, repriced)
		})
		viewLending(t, s, func(ctx context.Context, tx lending.Tx) error {
			rows, err := tx.ListFacilityTerms(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "timeline length after upsert", len(rows), 3)
			assertEqual(t, "upserted rate", rows[1].Rate, interest.Rate(100_000))
			return nil
		})
	})

	t.Run("ResetClearsLendingRows", func(t *testing.T) {
		s := openLending(t, newStore, bookA)

		updateLending(t, s, func(ctx context.Context, tx lending.Tx) error {
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
			return tx.PutFacilityTerms(ctx, bookA, lending.FacilityTerms{
				FacilityID: "fac_1", EffectiveFrom: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				Rate: 90_000, DayCount: interest.ACT360, CreatedAt: early,
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

			terms, err := tx.ListFacilityTerms(ctx, bookA, "fac_1")
			if err != nil {
				return err
			}
			assertEqual(t, "facility terms after reset", len(terms), 0)
			return nil
		})
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func openLending(t *testing.T, newStore func(*testing.T, ledger.BookID) lending.Store, book ledger.BookID) lending.Store {
	t.Helper()
	s := newStore(t, book)
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
