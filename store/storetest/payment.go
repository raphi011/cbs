package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// RunPayment runs the payment-layer conformance suite against a store. Every
// payment.Store implementation must pass it identically.
//
// It talks only to payment.Store and payment.Tx — never to payment.Network — so
// what it pins is the storage contract: the not-found sentinels, the fact that
// a Participant's live handles are derived rather than stored, listing order,
// the open-cycle and end-to-end-id lookups, deep copying, and the three-layer
// rollback that payment.Tx embedding deposit.Tx embedding ledger.Tx exists to
// provide.
//
// newStore must return a store with no state in it; the suite calls it once per
// subtest and closes the result.
func RunPayment(t *testing.T, newStore func(*testing.T) payment.Store) {
	t.Helper()

	t.Run("ParticipantRoundTripsAndDropsLiveHandles", func(t *testing.T) {
		s := openPayment(t, newStore)

		p := participant("bank_1", "Aurora Bank", early)
		// A Network hands the store a fully bound Participant. Ledger and
		// Deposit are handles over the store, not data: store/pg has no column
		// to put a *ledger.Book in, so store/mem must not keep them either —
		// otherwise code works in memory and breaks on Postgres.
		p.Ledger = ledger.NewBook(nil, "bank_1", nil)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutParticipant(ctx, p)
		})

		var got payment.Participant
		var listed []payment.Participant
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			var err error
			if got, err = tx.GetParticipant(ctx, "bank_1"); err != nil {
				return err
			}
			listed, err = tx.ListParticipants(ctx)
			return err
		})

		assertEqual(t, "name", got.Name, "Aurora Bank")
		assertEqual(t, "book id", string(got.BookID), "bank_1")
		assertEqual(t, "customer subledger", string(got.CustomerSubledger), "100")
		// The product OpenCustomerAccount opens from. It is data like the
		// subledger above, not a handle like Ledger below, so it has to survive
		// the round trip — a store that drops it leaves every bank pricing
		// accounts from a product id of "", which fails as "product not found"
		// several layers away from the store that lost it.
		assertEqual(t, "product id", string(got.ProductID), "prd_basic")
		assertEqual(t, "product id in listings", string(listed[0].ProductID), "prd_basic")
		// The BIC is what the mesh routes on. A store that drops it leaves
		// every bank unreachable, and the failure surfaces as an unroutable
		// message rather than as a store that lost a column — which is why it
		// is asserted here and in the listing, not only here.
		assertEqual(t, "bic", string(got.BIC), "AURODEFFXXX")
		assertEqual(t, "bic in listings", string(listed[0].BIC), "AURODEFFXXX")
		assertEqual(t, "suspense account", string(got.Assets["EUR"].Suspense), "200.200.001")
		assertEqual(t, "reserve account", string(got.Assets["EUR"].Reserve), "100.200.001")
		assertEqual(t, "settlement account", string(got.Assets["EUR"].Settlement), "200.100.001")
		assertEqual(t, "created at", got.CreatedAt.Equal(early), true)

		assertEqual(t, "Ledger is not persisted", got.Ledger == nil, true)
		assertEqual(t, "Deposit is not persisted", got.Deposit == nil, true)
		assertEqual(t, "participants listed", len(listed), 1)
		assertEqual(t, "Ledger is not persisted in listings", listed[0].Ledger == nil, true)

		// PutParticipant is an upsert on ID: renaming a bank must not create a
		// second one.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			renamed := got
			renamed.Name = "Aurora Bank AB"
			return tx.PutParticipant(ctx, renamed)
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			all, err := tx.ListParticipants(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "participants after an upsert", len(all), 1)
			assertEqual(t, "name after an upsert", all[0].Name, "Aurora Bank AB")
			assertEqual(t, "product id after an upsert", string(all[0].ProductID), "prd_basic")
			return nil
		})
	})

	t.Run("ParticipantAssetsRoundTripAndReplaceOnUpsert", func(t *testing.T) {
		s := openPayment(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutParticipant(ctx, payment.Participant{
				ID: "alpha", Name: "Alpha", BookID: "alpha", CreatedAt: early,
				Assets: map[ledger.AssetCode]payment.ParticipantAccounts{
					"EUR": {Suspense: "200.ib.001", Reserve: "100.ib.001", Settlement: "200.res.001"},
					"USD": {Suspense: "200.ib.002", Reserve: "100.ib.002", Settlement: "200.res.002"},
				},
			})
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
			// A listing must carry them too, not just a single Get — the
			// listing is the path SettleCycle resolves every member through.
			listed, err := tx.ListParticipants(ctx)
			if err != nil {
				return err
			}
			if len(listed) != 1 || len(listed[0].Assets) != 2 {
				t.Errorf("ListParticipants = %+v, want one participant with two assets", listed)
			}
			return nil
		})

		// An upsert must replace the set, not merge into it: a stale asset
		// left behind would settle through an account the participant no
		// longer holds.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutParticipant(ctx, payment.Participant{
				ID: "alpha", Name: "Alpha", BookID: "alpha", CreatedAt: early,
				Assets: map[ledger.AssetCode]payment.ParticipantAccounts{
					"EUR": {Suspense: "200.ib.001", Reserve: "100.ib.001", Settlement: "200.res.001"},
				},
			})
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			got, err := tx.GetParticipant(ctx, "alpha")
			if err != nil {
				return err
			}
			if len(got.Assets) != 1 {
				t.Errorf("after upsert participant has %d assets, want 1", len(got.Assets))
			}
			return nil
		})

		// And the map the store hands back is the caller's own: mutating it
		// must not reach into the store, which store/mem could only get wrong
		// by handing out its own map.
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			got, err := tx.GetParticipant(ctx, "alpha")
			if err != nil {
				return err
			}
			delete(got.Assets, "EUR")
			again, err := tx.GetParticipant(ctx, "alpha")
			if err != nil {
				return err
			}
			if len(again.Assets) != 1 {
				t.Errorf("mutating a returned Assets map changed the store: %d assets left", len(again.Assets))
			}
			return nil
		})
	})

	t.Run("GetOnMissingPaymentRowsReturnsSentinels", func(t *testing.T) {
		s := openPayment(t, newStore)

		// Seed one row of every kind, so the not-found path is exercised on a
		// populated store rather than only on an empty one.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutParticipant(ctx, participant("bank_1", "Aurora Bank", early)); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "e2e-1", early)); err != nil {
				return err
			}
			if err := tx.PutMandate(ctx, mandate("mnd_1", early)); err != nil {
				return err
			}
			if err := tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleSettled, early)); err != nil {
				return err
			}
			return tx.PutSettlement(ctx, settlement("set_1", "cyc_1", early))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			_, err := tx.GetParticipant(ctx, "bank_nope")
			assertErrorIs(t, "GetParticipant on an unknown bank", err, payment.ErrParticipantNotFound)

			_, err = tx.GetPayment(ctx, "pay_nope")
			assertErrorIs(t, "GetPayment on an unknown payment", err, payment.ErrPaymentNotFound)

			_, err = tx.GetPaymentByEndToEndID(ctx, "e2e-nope")
			assertErrorIs(t, "GetPaymentByEndToEndID on an unknown reference", err, payment.ErrPaymentNotFound)

			_, err = tx.GetMandate(ctx, "mnd_nope")
			assertErrorIs(t, "GetMandate on an unknown mandate", err, payment.ErrMandateNotFound)

			_, err = tx.GetCycle(ctx, "cyc_nope")
			assertErrorIs(t, "GetCycle on an unknown cycle", err, payment.ErrCycleNotFound)

			// The seeded cycle is Settled, so no cycle is open for its scheme.
			_, err = tx.GetOpenCycle(ctx, payment.SchemeSEPACT)
			assertErrorIs(t, "GetOpenCycle with nothing open", err, payment.ErrCycleNotFound)

			_, err = tx.GetOpenCycle(ctx, "no.such.scheme")
			assertErrorIs(t, "GetOpenCycle for an unknown scheme", err, payment.ErrCycleNotFound)

			_, err = tx.GetSettlement(ctx, "set_nope")
			assertErrorIs(t, "GetSettlement on an unknown settlement", err, payment.ErrSettlementNotFound)
			return nil
		})
	})

	t.Run("PartyRefIdentifierRoundTrips", func(t *testing.T) {
		s := openPayment(t, newStore)

		// samplePayment and mandate each quote a DIFFERENT non-empty identifier
		// on the debtor side than on the creditor side. That is deliberate:
		// store/pg holds a PartyRef's identifier as two columns per side
		// (scheme, value), split out of what used to be one free-form IBAN
		// column — and two same-shaped TEXT columns is exactly the case a
		// transposed insert argument or scan target would not fail on, it
		// would just read back wrong. Asserting both sides, on both entities,
		// independently is what would catch that.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "e2e-1", early)); err != nil {
				return err
			}
			return tx.PutMandate(ctx, mandate("mnd_1", early))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			gotPayment, err := tx.GetPayment(ctx, "pay_1")
			if err != nil {
				return err
			}
			assertEqual(t, "payment debtor identifier", gotPayment.Debtor.Identifier,
				deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"})
			assertEqual(t, "payment creditor identifier", gotPayment.Creditor.Identifier,
				deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"})

			gotMandate, err := tx.GetMandate(ctx, "mnd_1")
			if err != nil {
				return err
			}
			assertEqual(t, "mandate debtor identifier", gotMandate.Debtor.Identifier,
				deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"})
			assertEqual(t, "mandate creditor identifier", gotMandate.Creditor.Identifier,
				deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"})
			return nil
		})
	})

	t.Run("SubmittedPaymentRoundTripsWithNoCycleAndNoDebtorLeg", func(t *testing.T) {
		s := openPayment(t, newStore)

		// The shape the split invented, and the one every payment now passes
		// through: Initiated, in NO cycle, and — for a pull — with no debtor
		// leg either. Before it, a payment reached PutPayment only once it was
		// already Accepted and already in a cycle, which is what every other
		// fixture in this file still writes.
		//
		// Empty is a value here, not a missing one. payments.cycle_id is TEXT
		// NOT NULL with no CHECK and no foreign key, so "" round-trips today;
		// this case is what makes that a property of the CONTRACT rather than
		// of the current DDL. A later CHECK (cycle_id <> ''), or the foreign
		// key cycle_payments.cycle_id already carries, would make store/pg
		// refuse a write store/mem accepts — and without this case the
		// conformance suite would stay green while it did.
		p := samplePayment("pay_1", "e2e-1", early)
		p.Status = payment.Initiated
		p.CycleID = ""
		p.DebtorLegTx = ""
		p.CreditorLegTx = ""

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutPayment(ctx, p)
		})

		var got payment.Payment
		var listed []payment.Payment
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			var err error
			if got, err = tx.GetPayment(ctx, p.ID); err != nil {
				return err
			}
			listed, err = tx.ListPayments(ctx)
			return err
		})

		assertEqual(t, "status", got.Status, payment.Initiated)
		assertEqual(t, "cycle", string(got.CycleID), "")
		assertEqual(t, "debtor leg", string(got.DebtorLegTx), "")
		assertEqual(t, "status in listings", listed[0].Status, payment.Initiated)
		assertEqual(t, "cycle in listings", string(listed[0].CycleID), "")

		// And it is still found by the reference it claimed: an uncycled
		// payment is a payment, so the duplicate check at submission — the
		// only thing standing between a customer and paying twice — has to see
		// it.
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			byRef, err := tx.GetPaymentByEndToEndID(ctx, "e2e-1")
			if err != nil {
				return err
			}
			assertEqual(t, "end-to-end lookup", string(byRef.ID), "pay_1")
			return nil
		})
	})

	t.Run("RejectedPaymentKeepsItsCodeAndItsText", func(t *testing.T) {
		s := openPayment(t, newStore)

		p := samplePayment("pay_1", "e2e-1", early)
		p.Status = payment.Rejected
		p.RejectReason = "creditor account is closed"
		// A code AND free text, not one or the other. The code is what makes a
		// rejection machine-actionable; the text is what a human reads. A store
		// that keeps only the text silently turns every rejection back into the
		// string it was before this sub-project.
		p.RejectCode = "AC04"

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutPayment(ctx, p)
		})

		var got payment.Payment
		var listed []payment.Payment
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			var err error
			if got, err = tx.GetPayment(ctx, p.ID); err != nil {
				return err
			}
			listed, err = tx.ListPayments(ctx)
			return err
		})

		assertEqual(t, "reject code", string(got.RejectCode), "AC04")
		assertEqual(t, "reject reason", got.RejectReason, "creditor account is closed")
		// paymentColumns is shared between the get and list queries, so a
		// positional bug would likely bite both alike — but the BIC case
		// asserts both paths for the same reason, and a future list query
		// that builds its own column list is exactly what only this second
		// assertion would catch.
		assertEqual(t, "reject code in listings", string(listed[0].RejectCode), "AC04")
		assertEqual(t, "reject reason in listings", listed[0].RejectReason, "creditor account is closed")
	})

	t.Run("PaymentListOrderingIsCreatedAtThenSeq", func(t *testing.T) {
		s := openPayment(t, newStore)

		late := early.Add(time.Hour)

		// The same three rules the ledger and deposit fixtures use, because the
		// same two mistakes are available here: a CreatedAt tie only the
		// insertion sequence can break, IDs spanning the 9 -> 10 boundary so
		// lexicographic ID order disagrees with insertion order, and the row
		// inserted FIRST carrying the LATEST creation instant. Ordering by
		// (CreatedAt, ID) fails on this fixture, and so does ordering by
		// sequence alone.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			for _, p := range []struct {
				id string
				at time.Time
			}{{"bank_10", late}, {"bank_8", early}, {"bank_20", early}, {"bank_9", early}} {
				if err := tx.PutParticipant(ctx, participant(payment.ParticipantID(p.id), p.id, p.at)); err != nil {
					return err
				}
			}
			for _, p := range []struct {
				id string
				at time.Time
			}{{"pay_10", late}, {"pay_8", early}, {"pay_20", early}, {"pay_9", early}} {
				if err := tx.PutPayment(ctx, samplePayment(payment.PaymentID(p.id), "e2e-"+p.id, p.at)); err != nil {
					return err
				}
			}
			for _, m := range []struct {
				id string
				at time.Time
			}{{"mnd_10", late}, {"mnd_8", early}, {"mnd_20", early}, {"mnd_9", early}} {
				if err := tx.PutMandate(ctx, mandate(payment.MandateID(m.id), m.at)); err != nil {
					return err
				}
			}
			for _, c := range []struct {
				id string
				at time.Time
			}{{"cyc_10", late}, {"cyc_8", early}, {"cyc_20", early}, {"cyc_9", early}} {
				if err := tx.PutCycle(ctx, cycle(payment.CycleID(c.id), payment.SchemeSEPACT, payment.CycleSettled, c.at)); err != nil {
					return err
				}
			}
			for _, st := range []struct {
				id string
				at time.Time
			}{{"set_10", late}, {"set_8", early}, {"set_20", early}, {"set_9", early}} {
				if err := tx.PutSettlement(ctx, settlement(payment.SettlementID(st.id), "cyc_8", st.at)); err != nil {
					return err
				}
			}
			return nil
		})

		var participants []payment.Participant
		var payments []payment.Payment
		var mandates []payment.Mandate
		var cycles []payment.ClearingCycle
		var settlements []payment.Settlement
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			var err error
			if participants, err = tx.ListParticipants(ctx); err != nil {
				return err
			}
			if payments, err = tx.ListPayments(ctx); err != nil {
				return err
			}
			if mandates, err = tx.ListMandates(ctx); err != nil {
				return err
			}
			if cycles, err = tx.ListCycles(ctx); err != nil {
				return err
			}
			settlements, err = tx.ListSettlements(ctx)
			return err
		})

		assertOrder(t, "ListParticipants", ids(participants, func(p payment.Participant) string { return string(p.ID) }),
			"bank_8", "bank_20", "bank_9", "bank_10")
		assertOrder(t, "ListPayments", ids(payments, func(p payment.Payment) string { return string(p.ID) }),
			"pay_8", "pay_20", "pay_9", "pay_10")
		assertOrder(t, "ListMandates", ids(mandates, func(m payment.Mandate) string { return string(m.ID) }),
			"mnd_8", "mnd_20", "mnd_9", "mnd_10")
		assertOrder(t, "ListCycles", ids(cycles, func(c payment.ClearingCycle) string { return string(c.ID) }),
			"cyc_8", "cyc_20", "cyc_9", "cyc_10")
		assertOrder(t, "ListSettlements", ids(settlements, func(st payment.Settlement) string { return string(st.ID) }),
			"set_8", "set_20", "set_9", "set_10")

		// An upsert keeps a row where it was: settling a cycle or rejecting a
		// payment must not move it to the bottom of the list.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			p := samplePayment("pay_8", "e2e-pay_8", early)
			p.Status = payment.Rejected
			if err := tx.PutPayment(ctx, p); err != nil {
				return err
			}
			c := cycle("cyc_8", payment.SchemeSEPACT, payment.CycleSettled, early)
			c.SettlementID = "set_8"
			return tx.PutCycle(ctx, c)
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			reordered, err := tx.ListPayments(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "ListPayments after an upsert", ids(reordered, func(p payment.Payment) string { return string(p.ID) }),
				"pay_8", "pay_20", "pay_9", "pay_10")
			cs, err := tx.ListCycles(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "ListCycles after an upsert", ids(cs, func(c payment.ClearingCycle) string { return string(c.ID) }),
				"cyc_8", "cyc_20", "cyc_9", "cyc_10")
			return nil
		})
	})

	t.Run("GetOpenCycleFindsTheOpenCycleForItsScheme", func(t *testing.T) {
		s := openPayment(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			// A settled cycle for the same scheme, an open cycle for a
			// different scheme, and the one that should be found.
			if err := tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleSettled, early)); err != nil {
				return err
			}
			if err := tx.PutCycle(ctx, cycle("cyc_2", payment.SchemeSEPADD, payment.CycleOpen, early)); err != nil {
				return err
			}
			return tx.PutCycle(ctx, cycle("cyc_3", payment.SchemeSEPACT, payment.CycleOpen, early))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			ct, err := tx.GetOpenCycle(ctx, payment.SchemeSEPACT)
			if err != nil {
				return err
			}
			assertEqual(t, "open SCT cycle", string(ct.ID), "cyc_3")

			dd, err := tx.GetOpenCycle(ctx, payment.SchemeSEPADD)
			if err != nil {
				return err
			}
			assertEqual(t, "open SDD cycle", string(dd.ID), "cyc_2")
			return nil
		})

		// Closing it makes it invisible here — that is what lets OpenCycle
		// enforce one open cycle per scheme.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutCycle(ctx, cycle("cyc_3", payment.SchemeSEPACT, payment.CycleClosed, early))
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			_, err := tx.GetOpenCycle(ctx, payment.SchemeSEPACT)
			assertErrorIs(t, "GetOpenCycle after closing", err, payment.ErrCycleNotFound)
			return nil
		})
	})

	t.Run("GetPaymentByEndToEndIDIsExactAndIgnoresEmpty", func(t *testing.T) {
		s := openPayment(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "SCT-001", early)); err != nil {
				return err
			}
			// Two payments with no end-to-end id at all. An empty reference is
			// not an identity, so they must not deduplicate against each other
			// — the same rule the ledger applies to an empty idempotency key.
			if err := tx.PutPayment(ctx, samplePayment("pay_2", "", early)); err != nil {
				return err
			}
			return tx.PutPayment(ctx, samplePayment("pay_3", "", early))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			got, err := tx.GetPaymentByEndToEndID(ctx, "SCT-001")
			if err != nil {
				return err
			}
			assertEqual(t, "payment found by end-to-end id", string(got.ID), "pay_1")

			// Exact match only: no prefix or case folding.
			_, err = tx.GetPaymentByEndToEndID(ctx, "SCT-00")
			assertErrorIs(t, "GetPaymentByEndToEndID on a prefix", err, payment.ErrPaymentNotFound)
			_, err = tx.GetPaymentByEndToEndID(ctx, "sct-001")
			assertErrorIs(t, "GetPaymentByEndToEndID on a different case", err, payment.ErrPaymentNotFound)

			_, err = tx.GetPaymentByEndToEndID(ctx, "")
			assertErrorIs(t, "GetPaymentByEndToEndID on the empty reference", err, payment.ErrPaymentNotFound)

			all, err := tx.ListPayments(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "payments stored", len(all), 3)
			return nil
		})
	})

	t.Run("RePuttingAPaymentReleasesItsOldEndToEndID", func(t *testing.T) {
		s := openPayment(t, newStore)

		// The same rule the ledger applies to a re-keyed idempotency key. The
		// end-to-end id index is maintained by the store, so a store that only
		// ever adds to it goes on resolving a reference the payment no longer
		// carries — and the two implementations then disagree about which
		// references are still free.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutPayment(ctx, samplePayment("pay_1", "SCT-001", early))
		})
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutPayment(ctx, samplePayment("pay_1", "SCT-002", early))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			_, err := tx.GetPaymentByEndToEndID(ctx, "SCT-001")
			assertErrorIs(t, "lookup by the reference that was replaced", err, payment.ErrPaymentNotFound)

			got, err := tx.GetPaymentByEndToEndID(ctx, "SCT-002")
			if err != nil {
				return err
			}
			assertEqual(t, "payment behind the new reference", string(got.ID), "pay_1")
			assertEqual(t, "the payment's stored reference", got.EndToEndID, "SCT-002")

			all, err := tx.ListPayments(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "payments stored", len(all), 1)
			return nil
		})

		// Clearing the reference releases it as well.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutPayment(ctx, samplePayment("pay_1", "", early))
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			_, err := tx.GetPaymentByEndToEndID(ctx, "SCT-002")
			assertErrorIs(t, "lookup by a reference that was cleared", err, payment.ErrPaymentNotFound)
			return nil
		})
	})

	t.Run("PutIsAnUpsertAndDeepCopies", func(t *testing.T) {
		s := openPayment(t, newStore)

		// Cycles and settlements carry a slice and a map, payments a map. A
		// store that keeps the caller's reference lets a later mutation rewrite
		// history — and store/pg, which serialises on the way in, never would.
		c := cycle("cyc_1", payment.SchemeSEPACT, payment.CycleClosed, early)
		c.PaymentIDs = []payment.PaymentID{"pay_1"}
		c.NetPositions = map[payment.ParticipantID]ledger.Amount{"bank_1": 100}

		p := samplePayment("pay_1", "SCT-001", early)
		p.Metadata = map[string]string{"scheme": "sepa.ct"}

		st := settlement("set_1", "cyc_1", early)
		st.NetPositions = map[payment.ParticipantID]ledger.Amount{"bank_1": 100}

		// A participant carries one too: the accounts it holds per asset.
		bank := participant("bank_1", "Aurora Bank", early)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutCycle(ctx, c); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, p); err != nil {
				return err
			}
			if err := tx.PutParticipant(ctx, bank); err != nil {
				return err
			}
			return tx.PutSettlement(ctx, st)
		})

		// Mutate the caller's copies after the write.
		c.PaymentIDs[0] = "pay_tampered"
		c.NetPositions["bank_1"] = 999
		p.Metadata["scheme"] = "tampered"
		st.NetPositions["bank_1"] = 999
		bank.Assets["EUR"] = payment.ParticipantAccounts{Suspense: "tampered"}

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			gotCycle, err := tx.GetCycle(ctx, "cyc_1")
			if err != nil {
				return err
			}
			assertEqual(t, "cycle payment id after caller mutation", string(gotCycle.PaymentIDs[0]), "pay_1")
			assertEqual(t, "cycle net position after caller mutation", gotCycle.NetPositions["bank_1"], ledger.Amount(100))

			gotPayment, err := tx.GetPayment(ctx, "pay_1")
			if err != nil {
				return err
			}
			assertEqual(t, "payment metadata after caller mutation", gotPayment.Metadata["scheme"], "sepa.ct")

			gotParticipant, err := tx.GetParticipant(ctx, "bank_1")
			if err != nil {
				return err
			}
			assertEqual(t, "participant suspense after caller mutation",
				string(gotParticipant.Assets["EUR"].Suspense), "200.200.001")

			gotSettlement, err := tx.GetSettlement(ctx, "set_1")
			if err != nil {
				return err
			}
			assertEqual(t, "settlement net position after caller mutation", gotSettlement.NetPositions["bank_1"], ledger.Amount(100))

			// And the other direction: mutating what a Get returned must not
			// reach back into the store.
			gotCycle.PaymentIDs[0] = "pay_tampered"
			gotCycle.NetPositions["bank_1"] = 999
			gotPayment.Metadata["scheme"] = "tampered"
			return nil
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			gotCycle, err := tx.GetCycle(ctx, "cyc_1")
			if err != nil {
				return err
			}
			assertEqual(t, "cycle payment id after reader mutation", string(gotCycle.PaymentIDs[0]), "pay_1")
			assertEqual(t, "cycle net position after reader mutation", gotCycle.NetPositions["bank_1"], ledger.Amount(100))

			gotPayment, err := tx.GetPayment(ctx, "pay_1")
			if err != nil {
				return err
			}
			assertEqual(t, "payment metadata after reader mutation", gotPayment.Metadata["scheme"], "sepa.ct")
			return nil
		})

		// The upsert: a settled cycle replaces the closed one rather than
		// adding a second row, and the status change is what is read back.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			settled := cycle("cyc_1", payment.SchemeSEPACT, payment.CycleSettled, early)
			settled.SettlementID = "set_1"
			return tx.PutCycle(ctx, settled)
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			all, err := tx.ListCycles(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "cycles after an upsert", len(all), 1)
			assertEqual(t, "cycle status after an upsert", all[0].Status.String(), payment.CycleSettled.String())
			assertEqual(t, "settlement id after an upsert", string(all[0].SettlementID), "set_1")
			return nil
		})
	})

	t.Run("PaymentRoundTripsPartyDetails", func(t *testing.T) {
		paymentRoundTripsPartyDetails(t, openPayment(t, newStore))
	})

	t.Run("UpdateRollsBackAllThreeLayersTogether", func(t *testing.T) {
		s := openPayment(t, newStore)

		// This is what the whole embedding chain exists for: SettleCycle writes
		// payment rows, posts through the ledger and reads the deposit layer in
		// one unit of work, so a failure must undo all of it.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutParticipant(ctx, participant("bank_1", "Aurora Bank", early))
		})

		boom := errors.New("storetest: deliberate failure")
		err := s.Update(context.Background(), func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutParticipant(ctx, participant("bank_2", "Banca Verde", early)); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "SCT-001", early)); err != nil {
				return err
			}
			if err := tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleOpen, early)); err != nil {
				return err
			}
			if err := tx.PutSettlement(ctx, settlement("set_1", "cyc_1", early)); err != nil {
				return err
			}
			if err := tx.PutMandate(ctx, mandate("mnd_1", early)); err != nil {
				return err
			}
			// The deposit layer.
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_1", early)); err != nil {
				return err
			}
			// The ledger layer.
			if err := tx.PutTransaction(ctx, bookA, transaction("tx_1", "")); err != nil {
				return err
			}
			if err := tx.AppendAudit(ctx, ledger.AuditEvent{ID: "evt_1", BookID: bookA, Scope: ledger.ScopeLedger, Type: "test"}); err != nil {
				return err
			}
			return boom
		})
		assertErrorIs(t, "Update return", err, boom)

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			_, err := tx.GetParticipant(ctx, "bank_2")
			assertErrorIs(t, "participant from the failed unit of work", err, payment.ErrParticipantNotFound)

			_, err = tx.GetPayment(ctx, "pay_1")
			assertErrorIs(t, "payment from the failed unit of work", err, payment.ErrPaymentNotFound)

			// The end-to-end index rolled back with the row it pointed at,
			// otherwise the reference stays claimed by a payment that no
			// longer exists.
			_, err = tx.GetPaymentByEndToEndID(ctx, "SCT-001")
			assertErrorIs(t, "end-to-end id from the failed unit of work", err, payment.ErrPaymentNotFound)

			_, err = tx.GetCycle(ctx, "cyc_1")
			assertErrorIs(t, "cycle from the failed unit of work", err, payment.ErrCycleNotFound)

			_, err = tx.GetSettlement(ctx, "set_1")
			assertErrorIs(t, "settlement from the failed unit of work", err, payment.ErrSettlementNotFound)

			_, err = tx.GetMandate(ctx, "mnd_1")
			assertErrorIs(t, "mandate from the failed unit of work", err, payment.ErrMandateNotFound)

			// And the two layers below rolled back with it.
			_, err = tx.GetDepositAccount(ctx, bookA, "dep_1")
			assertErrorIs(t, "deposit account from the failed unit of work", err, deposit.ErrAccountNotFound)

			_, err = tx.GetTransaction(ctx, bookA, "tx_1")
			assertErrorIs(t, "GL transaction from the failed unit of work", err, ledger.ErrTransactionNotFound)

			events, err := tx.ListAudit(ctx, ledger.AuditFilter{})
			if err != nil {
				return err
			}
			assertEqual(t, "audit events after rollback", len(events), 0)

			// The participant written before the failed unit of work survived:
			// a rollback undoes its own transaction, not the store.
			survivor, err := tx.GetParticipant(ctx, "bank_1")
			if err != nil {
				return err
			}
			assertEqual(t, "participant from the committed unit of work", survivor.Name, "Aurora Bank")
			return nil
		})
	})

	t.Run("ResetClearsPaymentState", func(t *testing.T) {
		s := openPayment(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutParticipant(ctx, participant("bank_1", "Aurora Bank", early)); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "SCT-001", early)); err != nil {
				return err
			}
			if err := tx.PutMandate(ctx, mandate("mnd_1", early)); err != nil {
				return err
			}
			if err := tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleOpen, early)); err != nil {
				return err
			}
			return tx.PutSettlement(ctx, settlement("set_1", "cyc_1", early))
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			participants, err := tx.ListParticipants(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "participants after reset", len(participants), 0)

			payments, err := tx.ListPayments(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "payments after reset", len(payments), 0)

			mandates, err := tx.ListMandates(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "mandates after reset", len(mandates), 0)

			cycles, err := tx.ListCycles(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "cycles after reset", len(cycles), 0)

			settlements, err := tx.ListSettlements(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "settlements after reset", len(settlements), 0)

			// The end-to-end index is state too: a reference claimed before the
			// reset must be free afterwards.
			_, err = tx.GetPaymentByEndToEndID(ctx, "SCT-001")
			assertErrorIs(t, "end-to-end id after reset", err, payment.ErrPaymentNotFound)

			// And so is the open-cycle query.
			_, err = tx.GetOpenCycle(ctx, payment.SchemeSEPACT)
			assertErrorIs(t, "open cycle after reset", err, payment.ErrCycleNotFound)
			return nil
		})
	})
}

// ---------------------------------------------------------------------------
// Payment helpers
// ---------------------------------------------------------------------------

func participant(id payment.ParticipantID, name string, createdAt time.Time) payment.Participant {
	return payment.Participant{
		ID:                id,
		Name:              name,
		BIC:               "AURODEFFXXX",
		BookID:            ledger.BookID(id),
		CustomerSubledger: "100",
		ProductID:         "prd_basic",
		Assets: map[ledger.AssetCode]payment.ParticipantAccounts{
			"EUR": {Suspense: "200.200.001", Reserve: "100.200.001", Settlement: "200.100.001"},
		},
		CreatedAt: createdAt,
	}
}

func samplePayment(id payment.PaymentID, endToEndID string, createdAt time.Time) payment.Payment {
	return payment.Payment{
		ID:     id,
		Scheme: payment.SchemeSEPACT,
		Debtor: payment.PartyRef{Participant: "bank_1", Account: "dep_1",
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}},
		Creditor: payment.PartyRef{Participant: "bank_2", Account: "dep_2",
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"}},
		Amount:      2500,
		EndToEndID:  endToEndID,
		Status:      payment.Accepted,
		CycleID:     "cyc_1",
		BookingDate: createdAt,
		ValueDate:   createdAt.Add(24 * time.Hour),
		Description: string(id),
		CreatedAt:   createdAt,
	}
}

// paymentRoundTripsPartyDetails pins that what a MESSAGE says about each side —
// the agent's BIC and the account holder's name — survives a round trip through
// both stores.
//
// It is stored rather than looked up because looking it up is a read of another
// bank's deposit register, which is the crossing sub-project 8 exists to remove.
// A store that dropped these fields would send the name-reading code back.
func paymentRoundTripsPartyDetails(t *testing.T, st payment.Store) {
	ctx := context.Background()
	p := samplePayment("pay_details", "e2e-details", early)
	p.DebtorDetails = payment.PartyDetails{Agent: "AURODEFFXXX", Name: "Ada Lovelace"}
	p.CreditorDetails = payment.PartyDetails{Agent: "BRVODEFFXXX", Name: "Grace Hopper"}

	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return tx.PutPayment(ctx, p)
	}); err != nil {
		t.Fatalf("PutPayment: %v", err)
	}

	var got payment.Payment
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		var err error
		got, err = tx.GetPayment(ctx, p.ID)
		return err
	}); err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if got.DebtorDetails != p.DebtorDetails {
		t.Errorf("debtor details round-tripped as %+v, want %+v", got.DebtorDetails, p.DebtorDetails)
	}
	if got.CreditorDetails != p.CreditorDetails {
		t.Errorf("creditor details round-tripped as %+v, want %+v", got.CreditorDetails, p.CreditorDetails)
	}
}

func mandate(id payment.MandateID, createdAt time.Time) payment.Mandate {
	return payment.Mandate{
		ID: id,
		Debtor: payment.PartyRef{Participant: "bank_1", Account: "dep_1",
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}},
		Creditor: payment.PartyRef{Participant: "bank_2", Account: "dep_2",
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"}},
		MaxAmount: 100000,
		Status:    payment.MandateActive,
		CreatedAt: createdAt,
	}
}

func cycle(id payment.CycleID, scheme payment.SchemeID, status payment.CycleStatus, openedAt time.Time) payment.ClearingCycle {
	return payment.ClearingCycle{
		ID:           id,
		Scheme:       scheme,
		Status:       status,
		NetPositions: map[payment.ParticipantID]ledger.Amount{},
		OpenedAt:     openedAt,
	}
}

func settlement(id payment.SettlementID, cycleID payment.CycleID, settledAt time.Time) payment.Settlement {
	return payment.Settlement{
		ID:           id,
		CycleID:      cycleID,
		NetPositions: map[payment.ParticipantID]ledger.Amount{},
		SettlementTx: "tx_1",
		ValueDate:    settledAt,
		SettledAt:    settledAt,
	}
}

// openPayment builds a fresh store for one subtest and closes it when the
// subtest ends.
func openPayment(t *testing.T, newStore func(*testing.T) payment.Store) payment.Store {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// updatePayment runs a unit of work that is expected to succeed.
func updatePayment(t *testing.T, s payment.Store, fn func(context.Context, payment.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// viewPayment runs a read-only unit of work that is expected to succeed.
func viewPayment(t *testing.T, s payment.Store, fn func(context.Context, payment.Tx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}
