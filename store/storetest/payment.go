package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The two banks this suite's fixtures are about.
const (
	auroraBIC iso20022.BIC = "AURODEFFXXX"
	verdeBIC  iso20022.BIC = "VERDITMMXXX"
)

// RunPayment runs the payment-layer cases whose rows are a MEMBER BANK's: its
// own record of itself, the mandates it holds as creditor bank, its copy of
// each payment it is a party to, and the advices it was sent.
func RunPayment(t *testing.T, newStore func(*testing.T, ledger.BookID) payment.BankStore) {
	t.Helper()

	t.Run("BankRoundTripsAndDropsLiveHandles", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		p := bankRow(auroraBIC, "Aurora Bank", early)
		// A Network hands the store a fully bound Bank.
		p.Ledger = ledger.NewBook(nil, ledger.BookID(auroraBIC), nil)

		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutBank(ctx, p)
		})

		var got payment.Bank
		var listed []payment.Bank
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			var err error
			if got, err = tx.GetBank(ctx, payment.ParticipantID(auroraBIC)); err != nil {
				return err
			}
			listed, err = tx.ListBanks(ctx)
			return err
		})

		assertEqual(t, "name", got.Name, "Aurora Bank")
		assertEqual(t, "book id", string(got.BookID), string(auroraBIC))
		assertEqual(t, "customer subledger", string(got.CustomerSubledger), "100")
		// The product OpenCustomerAccount opens from.
		assertEqual(t, "product id", string(got.ProductID), "prd_basic")
		assertEqual(t, "product id in listings", string(listed[0].ProductID), "prd_basic")
		// The BIC is what a file is addressed by, and it is DERIVED from the id
		// rather than stored beside it: a bank's id IS its address, so there is no
		// bic column and nothing for a store to drop.
		assertEqual(t, "bic", string(got.BIC), string(auroraBIC))
		assertEqual(t, "bic in listings", string(listed[0].BIC), string(auroraBIC))
		assertEqual(t, "suspense account", string(got.Assets["EUR"].Suspense), "200.200.001")
		assertEqual(t, "reserve account", string(got.Assets["EUR"].Reserve), "100.200.001")
		// The two sides of a capital subscription, which is the only act that funds
		// a bank holding no depositors.
		assertEqual(t, "vault cash account", string(got.Assets["EUR"].VaultCash), "100.300.001")
		assertEqual(t, "share capital account", string(got.Assets["EUR"].ShareCapital), "300.400.001")
		assertEqual(t, "share capital account in listings", string(listed[0].Assets["EUR"].ShareCapital), "300.400.001")
		// The settlement account number, in both queries, because a store can lose a
		// column in one and not the other.
		assertEqual(t, "settlement account", string(got.Assets["EUR"].Settlement), "200.100.001")
		assertEqual(t, "settlement account in listings", string(listed[0].Assets["EUR"].Settlement), "200.100.001")
		assertEqual(t, "created at", got.CreatedAt.Equal(early), true)
		// The admission it recorded that account under, asserted for the same reason
		// and in both queries.
		assertEqual(t, "admission reference", got.AdmissionRef, "adm-"+string(auroraBIC))
		assertEqual(t, "admission reference in listings", listed[0].AdmissionRef, "adm-"+string(auroraBIC))

		assertEqual(t, "Ledger is not persisted", got.Ledger == nil, true)
		assertEqual(t, "Deposit is not persisted", got.Deposit == nil, true)
		assertEqual(t, "banks listed", len(listed), 1)
		assertEqual(t, "Ledger is not persisted in listings", listed[0].Ledger == nil, true)

		// PutBank is an upsert on ID: renaming a bank must not create a
		// second one.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			renamed := got
			renamed.Name = "Aurora Bank AB"
			return tx.PutBank(ctx, renamed)
		})
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			all, err := tx.ListBanks(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "banks after an upsert", len(all), 1)
			assertEqual(t, "name after an upsert", all[0].Name, "Aurora Bank AB")
			assertEqual(t, "product id after an upsert", string(all[0].ProductID), "prd_basic")
			return nil
		})
	})

	t.Run("BankAssetsRoundTripAndReplaceOnUpsert", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutBank(ctx, payment.Bank{
				ID: "alpha", Name: "Alpha", BookID: "alpha", CreatedAt: early,
				Assets: map[ledger.AssetCode]payment.BankAccounts{
					"EUR": {Suspense: "200.ib.001", Reserve: "100.ib.001", ReturnsReceivable: "200.ib.003", Settlement: "200.res.001"},
					"USD": {Suspense: "200.ib.002", Reserve: "100.ib.002", ReturnsReceivable: "200.ib.004", Settlement: "200.res.002"},
				},
			})
		})

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			got, err := tx.GetBank(ctx, "alpha")
			if err != nil {
				return err
			}
			if len(got.Assets) != 2 {
				t.Fatalf("bank has %d assets, want 2", len(got.Assets))
			}
			if got.Assets["USD"].Reserve != "100.ib.002" {
				t.Errorf("USD reserve = %q, want 100.ib.002", got.Assets["USD"].Reserve)
			}
			// returns_receivable is the newest of the four plumbing accounts, and
			// the one most recently at risk of being silently dropped by an
			// INSERT column list that forgot it.
			if got.Assets["EUR"].ReturnsReceivable != "200.ib.003" {
				t.Errorf("EUR returns receivable = %q, want 200.ib.003", got.Assets["EUR"].ReturnsReceivable)
			}
			if got.Assets["USD"].ReturnsReceivable != "200.ib.004" {
				t.Errorf("USD returns receivable = %q, want 200.ib.004", got.Assets["USD"].ReturnsReceivable)
			}
			// A listing must carry them too, not just a single Get — the
			// listing is the path SettleCycle resolves every member through.
			listed, err := tx.ListBanks(ctx)
			if err != nil {
				return err
			}
			if len(listed) != 1 || len(listed[0].Assets) != 2 {
				t.Errorf("ListBanks = %+v, want one bank with two assets", listed)
			}
			return nil
		})

		// An upsert must replace the set, not merge into it: a stale asset
		// left behind would settle through an account the bank no
		// longer holds.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutBank(ctx, payment.Bank{
				ID: "alpha", Name: "Alpha", BookID: "alpha", CreatedAt: early,
				Assets: map[ledger.AssetCode]payment.BankAccounts{
					"EUR": {Suspense: "200.ib.001", Reserve: "100.ib.001", Settlement: "200.res.001"},
				},
			})
		})

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			got, err := tx.GetBank(ctx, "alpha")
			if err != nil {
				return err
			}
			if len(got.Assets) != 1 {
				t.Errorf("after upsert bank has %d assets, want 1", len(got.Assets))
			}
			return nil
		})

		// And the map the store hands back is the caller's own: mutating it must not
		// reach into the store.
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			got, err := tx.GetBank(ctx, "alpha")
			if err != nil {
				return err
			}
			delete(got.Assets, "EUR")
			again, err := tx.GetBank(ctx, "alpha")
			if err != nil {
				return err
			}
			if len(again.Assets) != 1 {
				t.Errorf("mutating a returned Assets map changed the store: %d assets left", len(again.Assets))
			}
			return nil
		})
	})

	// The sentinels for the rows a BANK holds. The cycle's and the settlement's
	// are in the two institution suites below, because a bank's database holds
	// neither table.
	t.Run("GetOnMissingPaymentRowsReturnsSentinels", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		// Seed one row of every kind, so the not-found path is exercised on a
		// populated store rather than only on an empty one.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			if err := tx.PutBank(ctx, bankRow(auroraBIC, "Aurora Bank", early)); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "e2e-1", early)); err != nil {
				return err
			}
			return tx.PutMandate(ctx, mandate("mnd_1", early))
		})

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			_, err := tx.GetBank(ctx, "bank_nope")
			assertErrorIs(t, "GetBank on an unknown bank", err, payment.ErrParticipantNotFound)

			_, err = tx.GetPayment(ctx, "pay_nope")
			assertErrorIs(t, "GetPayment on an unknown payment", err, payment.ErrPaymentNotFound)

			_, err = tx.GetPaymentByEndToEndID(ctx, "e2e-nope")
			assertErrorIs(t, "GetPaymentByEndToEndID on an unknown reference", err, payment.ErrPaymentNotFound)

			_, err = tx.GetMandate(ctx, "mnd_nope")
			assertErrorIs(t, "GetMandate on an unknown mandate", err, payment.ErrMandateNotFound)
			return nil
		})
	})

	t.Run("PartyRefIdentifierRoundTrips", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		// samplePayment and mandate each quote a DIFFERENT non-empty identifier on
		// the debtor side than on the creditor side.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "e2e-1", early)); err != nil {
				return err
			}
			return tx.PutMandate(ctx, mandate("mnd_1", early))
		})

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
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
		s := openPayment(t, newStore, bookA)

		// The shape the split invented, and the one every payment now passes through:
		// Initiated, in NO cycle, and — for a pull — with no debtor leg either.
		p := samplePayment("pay_1", "e2e-1", early)
		p.Status = payment.Initiated
		p.CycleID = ""
		p.DebtorLegTx = ""
		p.CreditorLegTx = ""

		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutPayment(ctx, p)
		})

		var got payment.Payment
		var listed []payment.Payment
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
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

		// And it is still found by the reference it claimed: an uncycled payment is a
		// payment, so the duplicate check at submission — the only thing standing
		// between a customer and paying twice — has to see it.
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			byRef, err := tx.GetPaymentByEndToEndID(ctx, "e2e-1")
			if err != nil {
				return err
			}
			assertEqual(t, "end-to-end lookup", string(byRef.ID), "pay_1")
			return nil
		})
	})

	t.Run("RejectedPaymentKeepsItsCodeAndItsText", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		p := samplePayment("pay_1", "e2e-1", early)
		p.Status = payment.Rejected
		p.RejectReason = "creditor account is closed"
		// A code AND free text, not one or the other. The code is what makes a
		// rejection machine-actionable; the text is what a human reads. A store
		// that keeps only the text silently turns every rejection back into prose.
		p.RejectCode = "AC04"

		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutPayment(ctx, p)
		})

		var got payment.Payment
		var listed []payment.Payment
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			var err error
			if got, err = tx.GetPayment(ctx, p.ID); err != nil {
				return err
			}
			listed, err = tx.ListPayments(ctx)
			return err
		})

		assertEqual(t, "reject code", string(got.RejectCode), "AC04")
		assertEqual(t, "reject reason", got.RejectReason, "creditor account is closed")
		// paymentColumns is shared between the get and list queries, so a positional
		// bug would likely bite both alike — but the BIC case asserts both paths for
		// the same reason, and a future list query that builds its own column list is
		// exactly what only this second assertion would catch.
		assertEqual(t, "reject code in listings", string(listed[0].RejectCode), "AC04")
		assertEqual(t, "reject reason in listings", listed[0].RejectReason, "creditor account is closed")
	})

	t.Run("PaymentListOrderingIsCreatedAtThenSeq", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		late := early.Add(time.Hour)

		// The same three rules the ledger and deposit fixtures use, because the same
		// two mistakes are available here: a CreatedAt tie only the insertion
		// sequence can break, IDs whose lexicographic order disagrees with insertion
		// order, and the row inserted FIRST carrying the LATEST creation instant.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			for _, p := range []struct {
				id string
				at time.Time
			}{{"ZULUDEFFXXX", late}, {"NORDSESSXXX", early}, {"AURODEFFXXX", early}, {"VERDITMMXXX", early}} {
				if err := tx.PutBank(ctx, bankRow(iso20022.BIC(p.id), p.id, p.at)); err != nil {
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
			return nil
		})

		var banks []payment.Bank
		var payments []payment.Payment
		var mandates []payment.Mandate
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			var err error
			if banks, err = tx.ListBanks(ctx); err != nil {
				return err
			}
			if payments, err = tx.ListPayments(ctx); err != nil {
				return err
			}
			mandates, err = tx.ListMandates(ctx)
			return err
		})

		assertOrder(t, "ListBanks", ids(banks, func(b payment.Bank) string { return string(b.ID) }),
			"NORDSESSXXX", "AURODEFFXXX", "VERDITMMXXX", "ZULUDEFFXXX")
		assertOrder(t, "ListPayments", ids(payments, func(p payment.Payment) string { return string(p.ID) }),
			"pay_8", "pay_20", "pay_9", "pay_10")
		assertOrder(t, "ListMandates", ids(mandates, func(m payment.Mandate) string { return string(m.ID) }),
			"mnd_8", "mnd_20", "mnd_9", "mnd_10")

		// An upsert keeps a row where it was: rejecting a payment must not move
		// it to the bottom of the list. The cycle's half of this claim is
		// CycleListOrderingIsOpenedAtThenSeq, in the clearing house's suite.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			p := samplePayment("pay_8", "e2e-pay_8", early)
			p.Status = payment.Rejected
			return tx.PutPayment(ctx, p)
		})
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			reordered, err := tx.ListPayments(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "ListPayments after an upsert", ids(reordered, func(p payment.Payment) string { return string(p.ID) }),
				"pay_8", "pay_20", "pay_9", "pay_10")
			return nil
		})
	})

	t.Run("GetPaymentByEndToEndIDIsExactAndIgnoresEmpty", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
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

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
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
		s := openPayment(t, newStore, bookA)

		// The same rule the ledger applies to a re-keyed idempotency key.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutPayment(ctx, samplePayment("pay_1", "SCT-001", early))
		})
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutPayment(ctx, samplePayment("pay_1", "SCT-002", early))
		})

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
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
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutPayment(ctx, samplePayment("pay_1", "", early))
		})
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			_, err := tx.GetPaymentByEndToEndID(ctx, "SCT-002")
			assertErrorIs(t, "lookup by a reference that was cleared", err, payment.ErrPaymentNotFound)
			return nil
		})
	})

	// The deep-copy rule for the rows a BANK holds. The cycle's, which carries a
	// slice AND a map, and the settlement's are asserted in the two institution
	// suites — where the tables are.
	t.Run("PutIsAnUpsertAndDeepCopies", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		// A payment carries a map and a bank carries one too: the accounts it holds
		// per asset.
		p := samplePayment("pay_1", "SCT-001", early)
		p.Metadata = map[string]string{"scheme": "sepa.ct"}
		bank := bankRow(auroraBIC, "Aurora Bank", early)

		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			if err := tx.PutPayment(ctx, p); err != nil {
				return err
			}
			return tx.PutBank(ctx, bank)
		})

		// Mutate the caller's copies after the write.
		p.Metadata["scheme"] = "tampered"
		bank.Assets["EUR"] = payment.BankAccounts{Suspense: "tampered"}

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			gotPayment, err := tx.GetPayment(ctx, "pay_1")
			if err != nil {
				return err
			}
			assertEqual(t, "payment metadata after caller mutation", gotPayment.Metadata["scheme"], "sepa.ct")

			gotBank, err := tx.GetBank(ctx, payment.ParticipantID(auroraBIC))
			if err != nil {
				return err
			}
			assertEqual(t, "bank suspense after caller mutation",
				string(gotBank.Assets["EUR"].Suspense), "200.200.001")

			// And the other direction: mutating what a Get returned must not
			// reach back into the store.
			gotPayment.Metadata["scheme"] = "tampered"
			return nil
		})

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			gotPayment, err := tx.GetPayment(ctx, "pay_1")
			if err != nil {
				return err
			}
			assertEqual(t, "payment metadata after reader mutation", gotPayment.Metadata["scheme"], "sepa.ct")
			return nil
		})

		// The upsert: a rejected payment replaces the accepted one rather than
		// adding a second row, and the status change is what is read back.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			rejected := samplePayment("pay_1", "SCT-001", early)
			rejected.Status = payment.Rejected
			return tx.PutPayment(ctx, rejected)
		})
		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			all, err := tx.ListPayments(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "payments after an upsert", len(all), 1)
			assertEqual(t, "payment status after an upsert", all[0].Status.String(), payment.Rejected.String())
			return nil
		})
	})

	t.Run("SettlementAdviceIsScopedToTheBankThatWasAdvised", func(t *testing.T) {
		settlementAdviceIsScopedToTheBankThatWasAdvised(t,
			openPayment(t, newStore, bookA), openPayment(t, newStore, bookB))
	})

	t.Run("AdvicesAreKeyedByReferenceNotByCycle", func(t *testing.T) {
		advicesAreKeyedByReferenceNotByCycle(t, openPayment(t, newStore, bookA))
	})

	t.Run("PaymentRoundTripsPartyDetails", func(t *testing.T) {
		paymentRoundTripsPartyDetails(t, openPayment(t, newStore, bookA))
	})

	t.Run("PaymentRecordsWhereTheCreditorLegLanded", func(t *testing.T) {
		paymentRecordsWhereTheCreditorLegLanded(t, openPayment(t, newStore, bookA))
	})

	t.Run("PaymentRecordsBothReturnLegs", func(t *testing.T) {
		paymentRecordsBothReturnLegs(t, openPayment(t, newStore, bookA))
	})

	// RoutingDirectoryIsReplacedWholesale pins the one write on payment.BankTx
	// that is not an upsert, and the reason it is not one.
	t.Run("RoutingDirectoryIsReplacedWholesale", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		de := iban.Issuer{Country: iban.DE, BankCode: "99999999"}
		it := iban.Issuer{Country: iban.IT, BankCode: "99999"}
		fr := iban.Issuer{Country: iban.FR, BankCode: "99999"}

		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.ReplaceRoutingDirectory(ctx, []payment.DirectoryEntry{
				{Issuer: de, BIC: auroraBIC, RefreshedAt: early},
				{Issuer: it, BIC: verdeBIC, RefreshedAt: early},
				{Issuer: fr, BIC: "SOLEFRPPXXX", RefreshedAt: early},
			})
		})

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			// One code, two countries, two banks.
			got, err := tx.GetDirectoryEntry(ctx, it)
			if err != nil {
				return err
			}
			assertEqual(t, "the bank answering for IT 99999", string(got.BIC), string(verdeBIC))
			assertEqual(t, "when that answer was refreshed", got.RefreshedAt.Equal(early), true)

			got, err = tx.GetDirectoryEntry(ctx, fr)
			if err != nil {
				return err
			}
			assertEqual(t, "the bank answering for FR 99999", string(got.BIC), "SOLEFRPPXXX")

			_, err = tx.GetDirectoryEntry(ctx, iban.Issuer{Country: iban.SE, BankCode: "999"})
			assertErrorIs(t, "GetDirectoryEntry on a code this copy has no entry for",
				err, payment.ErrBankCodeUnknown)

			// Listed in the order the snapshot was written, which is the order the
			// publisher had them in.
			entries, err := tx.ListDirectoryEntries(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "routing directory", ids(entries, func(e payment.DirectoryEntry) string {
				return string(e.BIC)
			}), string(auroraBIC), string(verdeBIC), "SOLEFRPPXXX")
			return nil
		})

		// A second delivery, later, with Verde no longer in it.
		later := early.Add(48 * time.Hour)
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.ReplaceRoutingDirectory(ctx, []payment.DirectoryEntry{
				{Issuer: de, BIC: auroraBIC, RefreshedAt: later},
				{Issuer: fr, BIC: "SOLEFRPPXXX", RefreshedAt: later},
			})
		})

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			_, err := tx.GetDirectoryEntry(ctx, it)
			assertErrorIs(t, "GetDirectoryEntry on a code the new snapshot omits",
				err, payment.ErrBankCodeUnknown)

			entries, err := tx.ListDirectoryEntries(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "routing directory after the second delivery",
				ids(entries, func(e payment.DirectoryEntry) string { return string(e.BIC) }),
				string(auroraBIC), "SOLEFRPPXXX")
			assertEqual(t, "refreshed at, after the second delivery",
				entries[0].RefreshedAt.Equal(later), true)
			return nil
		})
	})

	t.Run("UpdateRollsBackAllThreeLayersTogether", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		// This is what the whole embedding chain exists for: a bank's own act writes
		// payment rows, posts through the ledger and reads the deposit layer in one
		// unit of work, so a failure must undo all of it.
		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutBank(ctx, bankRow(auroraBIC, "Aurora Bank", early))
		})

		boom := errors.New("storetest: deliberate failure")
		err := s.Update(context.Background(), func(ctx context.Context, tx payment.BankTx) error {
			if err := tx.PutBank(ctx, bankRow(verdeBIC, "Banca Verde", early)); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "SCT-001", early)); err != nil {
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

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			_, err := tx.GetBank(ctx, payment.ParticipantID(verdeBIC))
			assertErrorIs(t, "bank from the failed unit of work", err, payment.ErrParticipantNotFound)

			_, err = tx.GetPayment(ctx, "pay_1")
			assertErrorIs(t, "payment from the failed unit of work", err, payment.ErrPaymentNotFound)

			// The end-to-end index rolled back with the row it pointed at,
			// otherwise the reference stays claimed by a payment that no
			// longer exists.
			_, err = tx.GetPaymentByEndToEndID(ctx, "SCT-001")
			assertErrorIs(t, "end-to-end id from the failed unit of work", err, payment.ErrPaymentNotFound)

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

			// The bank written before the failed unit of work survived:
			// a rollback undoes its own transaction, not the store.
			survivor, err := tx.GetBank(ctx, payment.ParticipantID(auroraBIC))
			if err != nil {
				return err
			}
			assertEqual(t, "bank from the committed unit of work", survivor.Name, "Aurora Bank")
			return nil
		})
	})

	t.Run("ResetClearsPaymentState", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		updateBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			if err := tx.PutBank(ctx, bankRow(auroraBIC, "Aurora Bank", early)); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "SCT-001", early)); err != nil {
				return err
			}
			if err := tx.PutMandate(ctx, mandate("mnd_1", early)); err != nil {
				return err
			}
			return tx.ReplaceRoutingDirectory(ctx, []payment.DirectoryEntry{
				{Issuer: iban.Issuer{Country: iban.DE, BankCode: "99999999"}, BIC: auroraBIC, RefreshedAt: early},
			})
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		viewBank(t, s, func(ctx context.Context, tx payment.BankTx) error {
			banks, err := tx.ListBanks(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "banks after reset", len(banks), 0)

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

			// A directory a bank pulled is this bank's state like any other, and a
			// reset leaves it holding nothing rather than holding a snapshot of a
			// roster that no longer exists.
			entries, err := tx.ListDirectoryEntries(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "routing directory after reset", len(entries), 0)

			// The end-to-end index is state too: a reference claimed before the
			// reset must be free afterwards.
			_, err = tx.GetPaymentByEndToEndID(ctx, "SCT-001")
			assertErrorIs(t, "end-to-end id after reset", err, payment.ErrPaymentNotFound)
			return nil
		})
	})

	// The message log, which is the only record a member bank keeps of any file:
	// it hosts nothing, so it has neither queue nor order log.
	runMessageLog(t, func(t *testing.T) messageLog {
		return bankMessageLog(openPayment(t, newStore, bookA))
	})
}

// RunClearingHousePayment runs the payment-layer cases whose rows are the
// CLEARING HOUSE's: the roster it routes by, and the cycles it cuts.
func RunClearingHousePayment(t *testing.T, newStore func(*testing.T) payment.ClearingHouseStore) {
	t.Helper()

	// The cycle sentinels, which are the clearing house's half of what
	// GetOnMissingPaymentRowsReturnsSentinels asserts for the bank.
	t.Run("GetOnMissingCycleRowsReturnsSentinels", func(t *testing.T) {
		s := openClearingHouse(t, newStore)

		// A row to make the not-found path run against a populated store.
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleSettled, early))
		})

		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			_, err := tx.GetCycle(ctx, "cyc_nope")
			assertErrorIs(t, "GetCycle on an unknown cycle", err, payment.ErrCycleNotFound)

			// The seeded cycle is Settled, so no cycle is open for its scheme.
			_, err = tx.GetOpenCycle(ctx, payment.SchemeSEPACT)
			assertErrorIs(t, "GetOpenCycle with nothing open", err, payment.ErrCycleNotFound)

			_, err = tx.GetOpenCycle(ctx, "no.such.scheme")
			assertErrorIs(t, "GetOpenCycle for an unknown scheme", err, payment.ErrCycleNotFound)
			return nil
		})
	})

	// Cycle listing order, which was part of PaymentListOrderingIsCreatedAtThenSeq
	// while one store held every row kind.
	t.Run("ResetClearsTheClearingHousesState", func(t *testing.T) {
		s := openClearingHouse(t, newStore)

		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			if err := tx.PutRosterEntry(ctx, rosterEntry("AURODEFFXXX", early)); err != nil {
				return err
			}
			if err := tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleOpen, early)); err != nil {
				return err
			}
			// What this institution has taken in and not yet handed over, which is the
			// state a reset most has to take with it: a share that survived one would be
			// released into a bank's queue by the next cycle to be given the same id,
			// since the id counters restart with the rows.
			if err := tx.AddHeldFile(ctx, payment.HeldFile{
				CycleID: "cyc_1", Destination: auroraBIC, File: []byte("<f/>"),
				Transactions: []payment.HeldTransaction{{Position: 0, PaymentID: "pay_1"}},
			}); err != nil {
				return err
			}
			return tx.PutHeldReturn(ctx, payment.HeldReturn{
				PaymentID: "pay_2", ReturnedBy: auroraBIC, File: []byte("<r/>"),
			})
		})
		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			entries, err := tx.ListRosterEntries(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "roster entries after reset", len(entries), 0)

			cycles, err := tx.ListCycles(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "cycles after reset", len(cycles), 0)

			// The open-cycle query is state too.
			_, err = tx.GetOpenCycle(ctx, payment.SchemeSEPACT)
			assertErrorIs(t, "open cycle after reset", err, payment.ErrCycleNotFound)

			held, err := tx.ListHeldFiles(ctx, "cyc_1")
			if err != nil {
				return err
			}
			assertEqual(t, "held files after reset", len(held), 0)

			_, err = tx.GetHeldReturn(ctx, "pay_2")
			assertErrorIs(t, "held return after reset", err, payment.ErrHeldReturnNotFound)
			return nil
		})
	})

	t.Run("CycleIsAnUpsertAndDeepCopies", func(t *testing.T) {
		s := openClearingHouse(t, newStore)

		c := cycle("cyc_1", payment.SchemeSEPACT, payment.CycleClosed, early)
		c.PaymentIDs = []payment.PaymentID{"pay_1"}
		c.NetPositions = map[iso20022.BIC]ledger.Amount{auroraBIC: 100}

		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.PutCycle(ctx, c)
		})

		// Mutate the caller's copy after the write.
		c.PaymentIDs[0] = "pay_tampered"
		c.NetPositions[auroraBIC] = 999

		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.GetCycle(ctx, "cyc_1")
			if err != nil {
				return err
			}
			assertEqual(t, "cycle payment id after caller mutation", string(got.PaymentIDs[0]), "pay_1")
			assertEqual(t, "cycle net position after caller mutation", got.NetPositions[auroraBIC], ledger.Amount(100))

			// And the other direction: mutating what a Get returned must not
			// reach back into the store.
			got.PaymentIDs[0] = "pay_tampered"
			got.NetPositions[auroraBIC] = 999
			return nil
		})
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.GetCycle(ctx, "cyc_1")
			if err != nil {
				return err
			}
			assertEqual(t, "cycle payment id after reader mutation", string(got.PaymentIDs[0]), "pay_1")
			assertEqual(t, "cycle net position after reader mutation", got.NetPositions[auroraBIC], ledger.Amount(100))
			return nil
		})

		// The upsert: a settled cycle replaces the closed one rather than
		// adding a second row, and the status change is what is read back.
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleSettled, early))
		})
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			all, err := tx.ListCycles(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "cycles after an upsert", len(all), 1)
			assertEqual(t, "cycle status after an upsert", all[0].Status.String(), payment.CycleSettled.String())
			return nil
		})
	})

	// What this institution has taken in and not yet handed over: the two things
	// it stores that are an OBLIGATION rather than a record.
	t.Run("HeldFilesSurviveTheirCycleAndReleaseInBuildOrder", func(t *testing.T) {
		s := openClearingHouse(t, newStore)

		files := []payment.HeldFile{
			{CycleID: "cyc_1", Destination: auroraBIC, File: []byte("<first/>"), Transactions: []payment.HeldTransaction{
				{Position: 0, PaymentID: "pay_1"}, {Position: 2, PaymentID: "pay_3"},
			}},
			{CycleID: "cyc_1", Destination: verdeBIC, File: []byte("<first/>"), Transactions: []payment.HeldTransaction{
				{Position: 1, PaymentID: "pay_2"},
			}},
			{CycleID: "cyc_2", Destination: auroraBIC, File: []byte("<second/>"), Transactions: []payment.HeldTransaction{
				{Position: 0, PaymentID: "pay_4"},
			}},
		}
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			for _, f := range files {
				if err := tx.AddHeldFile(ctx, f); err != nil {
					return err
				}
			}
			return nil
		})

		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.ListHeldFiles(ctx, "cyc_1")
			if err != nil {
				return err
			}
			assertEqual(t, "shares held for cyc_1", len(got), 2)
			assertOrder(t, "ListHeldFiles",
				ids(got, func(f payment.HeldFile) string { return string(f.Destination) }),
				string(auroraBIC), string(verdeBIC))
			assertEqual(t, "the first share's file", string(got[0].File), "<first/>")
			assertEqual(t, "transactions in the first share", len(got[0].Transactions), 2)
			assertEqual(t, "the second transaction's position", got[0].Transactions[1].Position, 2)
			assertEqual(t, "the second transaction's payment", string(got[0].Transactions[1].PaymentID), "pay_3")

			// A cut-off nothing was taken into: an empty listing, not a sentinel.
			// "No share" and "no such cycle" are the same fact to the caller.
			none, err := tx.ListHeldFiles(ctx, "cyc_nope")
			if err != nil {
				return err
			}
			assertEqual(t, "shares held for a cycle nothing was taken into", len(none), 0)
			return nil
		})

		// The append: the same value written again is a SECOND share. Nothing here
		// upserts, because two files addressed to one bank in one cut-off is what a
		// bank uploading twice before the cut-off produces.
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.AddHeldFile(ctx, files[0])
		})
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.ListHeldFiles(ctx, "cyc_1")
			if err != nil {
				return err
			}
			assertEqual(t, "shares held for cyc_1 after a second identical file", len(got), 3)
			return nil
		})

		// The release: the ONE share named goes, its neighbours in the same cut-off
		// stay, and the positions go with it rather than being left behind.
		var first payment.HeldFile
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			held, err := tx.ListHeldFiles(ctx, "cyc_1")
			if err != nil {
				return err
			}
			first = held[0]
			return nil
		})
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.DeleteHeldFile(ctx, "cyc_1", first.Seq)
		})
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			left, err := tx.ListHeldFiles(ctx, "cyc_1")
			if err != nil {
				return err
			}
			assertEqual(t, "shares held for cyc_1 after one was discharged", len(left), 2)
			for _, f := range left {
				if f.Seq == first.Seq {
					t.Errorf("share %d of cyc_1 is still held after being discharged", first.Seq)
				}
			}
			// The one that remains is whole: a discharge takes one share's
			// positions and no other's.
			assertEqual(t, "transactions still in cyc_1's second share", len(left[0].Transactions), 1)

			kept, err := tx.ListHeldFiles(ctx, "cyc_2")
			if err != nil {
				return err
			}
			assertEqual(t, "shares held for cyc_2 after one of cyc_1's was discharged", len(kept), 1)
			assertEqual(t, "transactions still in cyc_2's share", len(kept[0].Transactions), 1)
			return nil
		})
	})

	t.Run("HeldReturnsAreKeyedByThePaymentTheAnswerNames", func(t *testing.T) {
		s := openClearingHouse(t, newStore)

		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.PutHeldReturn(ctx, payment.HeldReturn{
				PaymentID: "pay_1", ReturnedBy: auroraBIC, File: []byte("<rtn/>"),
			})
		})
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.GetHeldReturn(ctx, "pay_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the bank that returned", string(got.ReturnedBy), string(auroraBIC))
			assertEqual(t, "the held message", string(got.File), "<rtn/>")

			_, err = tx.GetHeldReturn(ctx, "pay_nope")
			assertErrorIs(t, "GetHeldReturn for a payment nothing is held for", err, payment.ErrHeldReturnNotFound)
			return nil
		})

		// An upsert, unlike a share: the key is the payment, and one payment has at
		// most one return in flight — the second hop of a conversation that has
		// already had its first.
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.PutHeldReturn(ctx, payment.HeldReturn{
				PaymentID: "pay_1", ReturnedBy: verdeBIC, File: []byte("<rtn2/>"),
			})
		})
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.GetHeldReturn(ctx, "pay_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the bank that returned, after an upsert", string(got.ReturnedBy), string(verdeBIC))
			assertEqual(t, "the held message, after an upsert", string(got.File), "<rtn2/>")
			return tx.DeleteHeldReturn(ctx, "pay_1")
		})
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			_, err := tx.GetHeldReturn(ctx, "pay_1")
			assertErrorIs(t, "GetHeldReturn after the answer arrived", err, payment.ErrHeldReturnNotFound)
			return nil
		})
	})

	t.Run("CycleListOrderingIsOpenedAtThenSeq", func(t *testing.T) {
		s := openClearingHouse(t, newStore)
		late := early.Add(time.Hour)

		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			for _, c := range []struct {
				id string
				at time.Time
			}{{"cyc_10", late}, {"cyc_8", early}, {"cyc_20", early}, {"cyc_9", early}} {
				if err := tx.PutCycle(ctx, cycle(payment.CycleID(c.id), payment.SchemeSEPACT, payment.CycleSettled, c.at)); err != nil {
					return err
				}
			}
			return nil
		})

		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			cycles, err := tx.ListCycles(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "ListCycles", ids(cycles, func(c payment.ClearingCycle) string { return string(c.ID) }),
				"cyc_8", "cyc_20", "cyc_9", "cyc_10")
			return nil
		})

		// An upsert keeps a row where it was: settling a cycle must not move it
		// to the bottom of the list.
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.PutCycle(ctx, cycle("cyc_8", payment.SchemeSEPACT, payment.CycleSettled, early))
		})
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			cs, err := tx.ListCycles(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "ListCycles after an upsert", ids(cs, func(c payment.ClearingCycle) string { return string(c.ID) }),
				"cyc_8", "cyc_20", "cyc_9", "cyc_10")
			return nil
		})
	})

	// RosterEntryCarriesNoAccountIdentifiers is the case that makes the split a
	// claim about the code rather than about the plan.
	t.Run("RosterEntryCarriesNoAccountIdentifiers", func(t *testing.T) {
		s := openClearingHouse(t, newStore)

		allowed := map[string]bool{
			"BIC":          true,
			"Issuer":       true,
			"Assets":       true,
			"AdmissionRef": true,
			"AdmittedAt":   true,
		}
		typ := reflect.TypeOf(payment.RosterEntry{})
		for i := range typ.NumField() {
			name := typ.Field(i).Name
			if !allowed[name] {
				t.Errorf("RosterEntry carries %s %s.\n"+
					"The clearing house's row is routing and nothing else. If this field is an account "+
					"identifier, a subledger or a product it belongs on the bank's own row; if it is "+
					"genuinely routing, add it to the table above and say why here.",
					name, typ.Field(i).Type)
			}
		}
		for name := range allowed {
			if _, ok := typ.FieldByName(name); !ok {
				t.Errorf("the allowed-field table names %s, which RosterEntry no longer has", name)
			}
		}

		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			if err := tx.PutRosterEntry(ctx, rosterEntry("AURODEFFXXX", early)); err != nil {
				return err
			}
			return tx.PutRosterEntry(ctx, rosterEntry("VERDITMMXXX", early.Add(time.Hour)))
		})

		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.GetRosterEntry(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "roster admission reference", got.AdmissionRef, "adm-AURODEFFXXX")
			assertEqual(t, "roster admitted at", got.AdmittedAt.Equal(early), true)
			assertOrder(t, "roster assets", ids(got.Assets, func(a ledger.AssetCode) string {
				return string(a)
			}), "EUR", "USD")

			_, err = tx.GetRosterEntry(ctx, "NORDSESSXXX")
			assertErrorIs(t, "GetRosterEntry on an unadmitted BIC", err, payment.ErrRosterEntryNotFound)

			entries, err := tx.ListRosterEntries(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "roster entries", ids(entries, func(e payment.RosterEntry) string {
				return string(e.BIC)
			}), "AURODEFFXXX", "VERDITMMXXX")
			if len(entries[0].Assets) != 2 {
				t.Errorf("listed roster entry has %d assets, want 2", len(entries[0].Assets))
			}

			// The slice handed back is the caller's own, for the reason every
			// other row here deep-copies: a stored row a caller can mutate in
			// place is not stored.
			got.Assets[0] = "GBP"
			again, err := tx.GetRosterEntry(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "roster asset after a reader mutation", string(again.Assets[0]), "EUR")
			return nil
		})
	})

	// RosterEntryAssetsAreAnOrderedList pins the two properties of Assets that its
	// Go type has and a set does not: the caller's ORDER survives, and a REPEATED
	// asset is stored rather than refused.
	t.Run("RosterEntryAssetsAreAnOrderedList", func(t *testing.T) {
		s := openClearingHouse(t, newStore)

		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			e := rosterEntry("AURODEFFXXX", early)
			e.Assets = []ledger.AssetCode{"USD", "EUR", "USD"}
			return tx.PutRosterEntry(ctx, e)
		})

		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.GetRosterEntry(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertOrder(t, "roster assets as written", ids(got.Assets, func(a ledger.AssetCode) string {
				return string(a)
			}), "USD", "EUR", "USD")

			// And through the listing, which is a second query and could order
			// differently from the single read above.
			listed, err := tx.ListRosterEntries(ctx)
			if err != nil {
				return err
			}
			if len(listed) != 1 {
				t.Fatalf("ListRosterEntries returned %d entries, want 1", len(listed))
			}
			assertOrder(t, "roster assets in listings", ids(listed[0].Assets, func(a ledger.AssetCode) string {
				return string(a)
			}), "USD", "EUR", "USD")
			return nil
		})

		// An upsert replaces the list wholesale, duplicates and all, so a
		// shorter list does not leave the tail of the longer one behind.
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			e := rosterEntry("AURODEFFXXX", early)
			e.Assets = []ledger.AssetCode{"GBP"}
			return tx.PutRosterEntry(ctx, e)
		})

		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			got, err := tx.GetRosterEntry(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertOrder(t, "roster assets after an upsert", ids(got.Assets, func(a ledger.AssetCode) string {
				return string(a)
			}), "GBP")
			return nil
		})
	})

	t.Run("GetOpenCycleFindsTheOpenCycleForItsScheme", func(t *testing.T) {
		s := openClearingHouse(t, newStore)

		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
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

		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
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
		updateCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			return tx.PutCycle(ctx, cycle("cyc_3", payment.SchemeSEPACT, payment.CycleClosed, early))
		})
		viewCsm(t, s, func(ctx context.Context, tx payment.CsmTx) error {
			_, err := tx.GetOpenCycle(ctx, payment.SchemeSEPACT)
			assertErrorIs(t, "GetOpenCycle after closing", err, payment.ErrCycleNotFound)
			return nil
		})
	})

	// The message log, which at the clearing house is most of the traffic in the
	// system.
	runMessageLog(t, func(t *testing.T) messageLog {
		return clearingHouseMessageLog(openClearingHouse(t, newStore))
	})
}

// RunCentralBankPayment runs the payment-layer cases whose rows are the CENTRAL
// BANK's: its own register of the members it holds settlement accounts for.
func RunCentralBankPayment(t *testing.T, newStore func(*testing.T) payment.CentralBankStore) {
	t.Helper()

	// The settlement sentinel and the settlement listing's order, which were the
	// central bank's share of two cases that ran against one store.
	t.Run("GetOnAMissingSettlementReturnsTheSentinel", func(t *testing.T) {
		s := openCentralBank(t, newStore)

		updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			return tx.PutSettlement(ctx, settlement("set_1", "cyc_1", early))
		})
		viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			_, err := tx.GetSettlement(ctx, "set_nope")
			assertErrorIs(t, "GetSettlement on an unknown settlement", err, payment.ErrSettlementNotFound)
			return nil
		})
	})

	// A settlement carries a map of net positions, and the deep-copy rule is the
	// same one PutIsAnUpsertAndDeepCopies states for the bank's rows.
	// See ResetClearsTheClearingHousesState for what this is a share of.
	t.Run("ResetClearsTheCentralBanksState", func(t *testing.T) {
		s := openCentralBank(t, newStore)

		updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			if err := tx.PutSettlementMember(ctx, settlementMember("AURODEFFXXX", "Aurora Bank", early)); err != nil {
				return err
			}
			return tx.PutSettlement(ctx, settlement("set_1", "cyc_1", early))
		})
		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			members, err := tx.ListSettlementMembers(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "settlement members after reset", len(members), 0)

			settlements, err := tx.ListSettlements(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "settlements after reset", len(settlements), 0)
			return nil
		})
	})

	t.Run("SettlementDeepCopiesItsNetPositions", func(t *testing.T) {
		s := openCentralBank(t, newStore)

		st := settlement("set_1", "cyc_1", early)
		st.NetPositions = map[iso20022.BIC]ledger.Amount{auroraBIC: 100}
		updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			return tx.PutSettlement(ctx, st)
		})
		st.NetPositions[auroraBIC] = 999

		viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			got, err := tx.GetSettlement(ctx, "set_1")
			if err != nil {
				return err
			}
			assertEqual(t, "settlement net position after caller mutation", got.NetPositions[auroraBIC], ledger.Amount(100))
			return nil
		})
	})

	t.Run("SettlementListOrderingIsSettledAtThenSeq", func(t *testing.T) {
		s := openCentralBank(t, newStore)
		late := early.Add(time.Hour)

		updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
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
		viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			settlements, err := tx.ListSettlements(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "ListSettlements", ids(settlements, func(st payment.Settlement) string { return string(st.ID) }),
				"set_8", "set_20", "set_9", "set_10")
			return nil
		})
	})

	// SettlementMemberIsKeyedByBIC is the central bank's own record of a bank it
	// holds a settlement account for, and the point of the case is the key.
	t.Run("SettlementMemberIsKeyedByBIC", func(t *testing.T) {
		s := openCentralBank(t, newStore)

		updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			if err := tx.PutSettlementMember(ctx, settlementMember("AURODEFFXXX", "Aurora Bank", early)); err != nil {
				return err
			}
			return tx.PutSettlementMember(ctx, settlementMember("VERDITMMXXX", "Banca Verde", early.Add(time.Hour)))
		})

		viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			got, err := tx.GetSettlementMember(ctx, "VERDITMMXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "member name", got.Name, "Banca Verde")
			assertEqual(t, "member bic", string(got.BIC), "VERDITMMXXX")
			assertEqual(t, "member opened at", got.OpenedAt.Equal(early.Add(time.Hour)), true)

			// A BIC no bank answers to is the sentinel, not an empty row: the
			// central bank asked to settle for a member it has never opened an
			// account for must fail rather than post to "".
			_, err = tx.GetSettlementMember(ctx, "NORDSESSXXX")
			assertErrorIs(t, "GetSettlementMember on a BIC with no member", err, payment.ErrSettlementMemberNotFound)

			members, err := tx.ListSettlementMembers(ctx)
			if err != nil {
				return err
			}
			assertOrder(t, "settlement members", ids(members, func(m payment.SettlementMember) string {
				return string(m.BIC)
			}), "AURODEFFXXX", "VERDITMMXXX")
			return nil
		})

		// The upsert is on the BIC, which is what makes re-driving an admission
		// safe: the same bank asking twice must not become two members.
		updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			renamed := settlementMember("VERDITMMXXX", "Banca Verde SpA", early.Add(time.Hour))
			return tx.PutSettlementMember(ctx, renamed)
		})
		viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			members, err := tx.ListSettlementMembers(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "members after an upsert", len(members), 2)
			got, err := tx.GetSettlementMember(ctx, "VERDITMMXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "member name after an upsert", got.Name, "Banca Verde SpA")
			return nil
		})
	})

	// SettlementMemberKeepsOneAccountPerAsset pins that the map survives the round
	// trip with its keys.
	t.Run("SettlementMemberKeepsOneAccountPerAsset", func(t *testing.T) {
		s := openCentralBank(t, newStore)

		updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			return tx.PutSettlementMember(ctx, payment.SettlementMember{
				BIC: "AURODEFFXXX", Name: "Aurora Bank", OpenedAt: early,
				Accounts: map[ledger.AssetCode]ledger.AccountID{
					"EUR": "200.100.001",
					"USD": "200.100.002",
				},
			})
		})

		viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			got, err := tx.GetSettlementMember(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "accounts held", len(got.Accounts), 2)
			assertEqual(t, "EUR settlement account", string(got.Accounts["EUR"]), "200.100.001")
			assertEqual(t, "USD settlement account", string(got.Accounts["USD"]), "200.100.002")

			// The listing carries them too. A settlement agent walking its
			// members to settle a cut-off reads the listing, not a Get per BIC.
			listed, err := tx.ListSettlementMembers(ctx)
			if err != nil {
				return err
			}
			if len(listed) != 1 || len(listed[0].Accounts) != 2 {
				t.Errorf("ListSettlementMembers = %+v, want one member with two accounts", listed)
			}
			return nil
		})

		// An upsert replaces the set rather than merging into it: an account for
		// an asset the member no longer holds would be settled through after the
		// member gave it up.
		updateCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			return tx.PutSettlementMember(ctx, payment.SettlementMember{
				BIC: "AURODEFFXXX", Name: "Aurora Bank", OpenedAt: early,
				Accounts: map[ledger.AssetCode]ledger.AccountID{"EUR": "200.100.001"},
			})
		})

		viewCentralBank(t, s, func(ctx context.Context, tx payment.CentralBankTx) error {
			got, err := tx.GetSettlementMember(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "accounts after an upsert", len(got.Accounts), 1)

			// And the map handed back is the caller's own.
			delete(got.Accounts, "EUR")
			again, err := tx.GetSettlementMember(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "accounts after a reader mutation", len(again.Accounts), 1)
			return nil
		})
	})

	// The message log, which is the settlement agent's own traffic. It carries
	// payment ids this institution holds no row for, which is the one thing its
	// copy of the join has that the other two do not.
	runMessageLog(t, func(t *testing.T) messageLog {
		return centralBankMessageLog(openCentralBank(t, newStore))
	})
}

// ---------------------------------------------------------------------------
// Payment helpers
// ---------------------------------------------------------------------------

// bankRow is one bank's own record of itself, admitted: it carries a settlement
// reference and the admission it recorded one under, because a column a store
// drops has to be a column it was given something to hold.
func bankRow(bic iso20022.BIC, name string, createdAt time.Time) payment.Bank {
	id := payment.ParticipantID(bic)
	return payment.Bank{
		ID:                id,
		Name:              name,
		BIC:               bic,
		BookID:            ledger.BookID(bic),
		CustomerSubledger: "100",
		ProductID:         "prd_basic",
		AdmissionRef:      "adm-" + string(id),
		Assets: map[ledger.AssetCode]payment.BankAccounts{
			"EUR": {
				Suspense: "200.200.001", Reserve: "100.200.001",
				VaultCash: "100.300.001", ShareCapital: "300.400.001",
				Settlement: "200.100.001",
			},
		},
		CreatedAt: createdAt,
	}
}

// settlementMember is the central bank's row for one bank, keyed by the only
// identifier the settlement agent is ever told: the BIC.
func settlementMember(bic iso20022.BIC, name string, openedAt time.Time) payment.SettlementMember {
	return payment.SettlementMember{
		BIC:      bic,
		Name:     name,
		Accounts: map[ledger.AssetCode]ledger.AccountID{"EUR": "200.100.001"},
		OpenedAt: openedAt,
	}
}

// rosterEntry is the clearing house's row for one bank: where to send a message
// addressed to it, and which admission put it there. It takes no name, because
// the row has none — see RosterEntryCarriesNoAccountIdentifiers.
func rosterEntry(bic iso20022.BIC, admittedAt time.Time) payment.RosterEntry {
	return payment.RosterEntry{
		BIC:          bic,
		Assets:       []ledger.AssetCode{"EUR", "USD"},
		AdmissionRef: "adm-" + string(bic),
		AdmittedAt:   admittedAt,
	}
}

func samplePayment(id payment.PaymentID, endToEndID string, createdAt time.Time) payment.Payment {
	return payment.Payment{
		ID:     id,
		Scheme: payment.SchemeSEPACT,
		Debtor: payment.PartyRef{Account: "dep_1",
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}},
		Creditor: payment.PartyRef{Account: "dep_2",
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"}},
		// Which bank each party is at, which a PartyRef does not say.
		DebtorDetails:   payment.PartyDetails{Agent: auroraBIC, Name: "Alice"},
		CreditorDetails: payment.PartyDetails{Agent: verdeBIC, Name: "Bruno"},
		Amount:          2500,
		EndToEndID:      endToEndID,
		Status:          payment.Accepted,
		CycleID:         "cyc_1",
		BookingDate:     createdAt,
		ValueDate:       createdAt.Add(24 * time.Hour),
		Description:     string(id),
		CreatedAt:       createdAt,
	}
}

// paymentRoundTripsPartyDetails pins that what a MESSAGE says about each side —
// the agent's BIC and the account holder's name — survives a round trip through
// the store.
func paymentRoundTripsPartyDetails(t *testing.T, st payment.BankStore) {
	ctx := context.Background()
	p := samplePayment("pay_details", "e2e-details", early)
	p.DebtorDetails = payment.PartyDetails{Agent: "AURODEFFXXX", Name: "Ada Lovelace"}
	p.CreditorDetails = payment.PartyDetails{Agent: "BRVODEFFXXX", Name: "Grace Hopper"}

	if err := st.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutPayment(ctx, p)
	}); err != nil {
		t.Fatalf("PutPayment: %v", err)
	}

	var got payment.Payment
	if err := st.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
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

// paymentRecordsWhereTheCreditorLegLanded pins that
// payment.Payment.CreditorLegAccount survives a round trip in both of the
// states it has, and that the empty one is a value rather than a missing field.
func paymentRecordsWhereTheCreditorLegLanded(t *testing.T, st payment.BankStore) {
	ctx := context.Background()

	// The ordinary settlement: the payee's own GL account.
	paid := samplePayment("pay_paid", "e2e-paid", early)
	paid.Status = payment.Settled
	paid.CreditorLegTx = "txn_paid"
	paid.CreditorLegAccount = "acc_bob"

	// The diversion: the CREDITOR BANK's unclaimed-balances account, because the
	// payee's account would not take the credit.
	diverted := samplePayment("pay_diverted", "e2e-diverted", early)
	diverted.Status = payment.Settled
	diverted.CreditorLegTx = "txn_diverted"
	diverted.CreditorLegAccount = "acc_unclaimed"

	// And a payment whose creditor leg has not been posted at all.
	pending := samplePayment("pay_pending", "e2e-pending", early)
	pending.Status = payment.Cleared
	pending.CreditorLegTx = ""
	pending.CreditorLegAccount = ""

	if err := st.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		for _, p := range []payment.Payment{paid, diverted, pending} {
			if err := tx.PutPayment(ctx, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("PutPayment: %v", err)
	}

	var listed []payment.Payment
	got := map[payment.PaymentID]payment.Payment{}
	if err := st.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		for _, id := range []payment.PaymentID{paid.ID, diverted.ID, pending.ID} {
			p, err := tx.GetPayment(ctx, id)
			if err != nil {
				return err
			}
			got[id] = p
		}
		var err error
		listed, err = tx.ListPayments(ctx)
		return err
	}); err != nil {
		t.Fatalf("GetPayment: %v", err)
	}

	for _, want := range []payment.Payment{paid, diverted, pending} {
		if acct := got[want.ID].CreditorLegAccount; acct != want.CreditorLegAccount {
			t.Errorf("%s round-tripped its creditor-leg account as %q, want %q",
				want.ID, acct, want.CreditorLegAccount)
		}
	}

	// And through the LISTING too, which is a different query and shares a column
	// list with the single read only because it is written down once.
	for _, p := range listed {
		want := map[payment.PaymentID]ledger.AccountID{
			paid.ID: "acc_bob", diverted.ID: "acc_unclaimed", pending.ID: "",
		}[p.ID]
		if p.CreditorLegAccount != want {
			t.Errorf("%s listed its creditor-leg account as %q, want %q", p.ID, p.CreditorLegAccount, want)
		}
	}
}

// paymentRecordsBothReturnLegs pins that payment.Payment's two return-leg
// transaction ids survive a round trip, and that a payment carrying only ONE of
// them round-trips as one rather than as two or none.
func paymentRecordsBothReturnLegs(t *testing.T, st payment.BankStore) {
	ctx := context.Background()

	// A return that has completed: both banks have posted.
	returned := samplePayment("pay_returned", "e2e-returned", early)
	returned.Status = payment.Returned
	returned.CreditorLegAccount = "acc_bob"
	returned.ReturnClawbackTx = "txn_claw"
	returned.ReturnRefundTx = "txn_refund"

	// A push in flight: the payee's bank has clawed back and sent, and the
	// payer's bank has not been told yet.
	halfway := samplePayment("pay_halfway", "e2e-halfway", early)
	halfway.Status = payment.Settled
	halfway.CreditorLegAccount = "acc_bob"
	halfway.ReturnClawbackTx = "txn_claw_only"

	// And a settled payment nobody has returned.
	settled := samplePayment("pay_settled", "e2e-settled", early)
	settled.Status = payment.Settled
	settled.CreditorLegAccount = "acc_bob"

	all := []payment.Payment{returned, halfway, settled}
	if err := st.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		for _, p := range all {
			if err := tx.PutPayment(ctx, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("PutPayment: %v", err)
	}

	got := map[payment.PaymentID]payment.Payment{}
	var listed []payment.Payment
	if err := st.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		for _, want := range all {
			p, err := tx.GetPayment(ctx, want.ID)
			if err != nil {
				return err
			}
			got[want.ID] = p
		}
		var err error
		listed, err = tx.ListPayments(ctx)
		return err
	}); err != nil {
		t.Fatalf("GetPayment: %v", err)
	}

	check := func(where string, p, want payment.Payment) {
		if p.ReturnClawbackTx != want.ReturnClawbackTx {
			t.Errorf("%s %s its clawback leg as %q, want %q", want.ID, where, p.ReturnClawbackTx, want.ReturnClawbackTx)
		}
		if p.ReturnRefundTx != want.ReturnRefundTx {
			t.Errorf("%s %s its refund leg as %q, want %q", want.ID, where, p.ReturnRefundTx, want.ReturnRefundTx)
		}
	}
	for _, want := range all {
		check("round-tripped", got[want.ID], want)
	}

	// And through the LISTING, which is a different query.
	byID := map[payment.PaymentID]payment.Payment{}
	for _, p := range listed {
		byID[p.ID] = p
	}
	for _, want := range all {
		check("listed", byID[want.ID], want)
	}
}

// settlementAdviceIsScopedToTheBankThatWasAdvised pins that an advice belongs
// to ONE bank's book and that two banks advised of the same cycle do not
// collide.
func settlementAdviceIsScopedToTheBankThatWasAdvised(t *testing.T, st, other payment.BankStore) {
	ctx := context.Background()
	one := payment.SettlementAdvice{
		Book: bookA, Reference: "cyc_1", Asset: "EUR",
		Movement: -250000, ClosingBalance: 750000,
		Status: payment.AdviceAdvised, AdvisedAt: early,
	}
	two := payment.SettlementAdvice{
		Book: bookB, Reference: "cyc_1", Asset: "EUR",
		Movement: 250000, ClosingBalance: 250000,
		Status: payment.AdvicePosted, MirrorTx: "txn_9",
		AdvisedAt: early, PostedAt: early,
	}
	// TWO STORES, because an advice is one bank's record of what it was told and
	// each bank holds its own database.
	if err := st.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutSettlementAdvice(ctx, one.Book, one)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice: %v", err)
	}
	if err := other.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutSettlementAdvice(ctx, two.Book, two)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice at the second bank: %v", err)
	}

	var gotOne, gotTwo payment.SettlementAdvice
	var listed []payment.SettlementAdvice
	if err := st.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		var err error
		if gotOne, err = tx.GetSettlementAdvice(ctx, bookA, "cyc_1", "EUR"); err != nil {
			return err
		}
		listed, err = tx.ListSettlementAdvices(ctx, bookA)
		return err
	}); err != nil {
		t.Fatalf("reading advices: %v", err)
	}
	if err := other.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		var err error
		gotTwo, err = tx.GetSettlementAdvice(ctx, bookB, "cyc_1", "EUR")
		return err
	}); err != nil {
		t.Fatalf("reading the second bank's advice: %v", err)
	}
	if gotOne != one {
		t.Errorf("the advised bank's advice round-tripped as %+v, want %+v", gotOne, one)
	}
	if gotTwo != two {
		t.Errorf("the second bank's advice round-tripped as %+v, want %+v", gotTwo, two)
	}
	if len(listed) != 1 {
		t.Errorf("the advised bank lists %d advices, want 1 — the list is scoped to one book", len(listed))
	}

	// The ORDER, which the scoping assertion above could not reach: bank_2 held
	// exactly one row, so a listing that sorted by nothing at all passed.
	later := early.Add(time.Hour)
	for _, a := range []payment.SettlementAdvice{
		{Book: bookA, Reference: "cyc_4", Asset: "EUR", Movement: 40, ClosingBalance: 40,
			Status: payment.AdviceAdvised, AdvisedAt: later},
		{Book: bookA, Reference: "cyc_3", Asset: "EUR", Movement: 30, ClosingBalance: 30,
			Status: payment.AdviceAdvised, AdvisedAt: later},
	} {
		if err := st.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
			return tx.PutSettlementAdvice(ctx, a.Book, a)
		}); err != nil {
			t.Fatalf("PutSettlementAdvice %s: %v", a.Reference, err)
		}
	}
	var ordered []payment.SettlementAdvice
	if err := st.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		var err error
		ordered, err = tx.ListSettlementAdvices(ctx, bookA)
		return err
	}); err != nil {
		t.Fatalf("ListSettlementAdvices: %v", err)
	}
	var got []string
	for _, a := range ordered {
		got = append(got, a.Reference)
	}
	want := []string{"cyc_1", "cyc_4", "cyc_3"}
	if len(got) != len(want) {
		t.Fatalf("the advised bank lists %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("the advised bank lists %v, want %v — AdvisedAt ascending, ties by insertion sequence", got, want)
			break
		}
	}

	// A cycle this bank was never advised of is a sentinel, not a zero value: a
	// bank that read a zero advice would post a mirror leg of nothing and mark
	// a cut-off it never heard about as settled.
	if err := st.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		_, err := tx.GetSettlementAdvice(ctx, bookA, "cyc_nope", "EUR")
		if !errors.Is(err, payment.ErrSettlementAdviceNotFound) {
			t.Errorf("got %v, want ErrSettlementAdviceNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}

	// The book ARGUMENT is the scope; the Book FIELD is the row's record of it.
	misfiled := payment.SettlementAdvice{
		Book: bookB, Reference: "cyc_2", Asset: "EUR",
		Movement: 100, ClosingBalance: 100,
		Status: payment.AdviceAdvised, AdvisedAt: early,
	}
	if err := st.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutSettlementAdvice(ctx, bookA, misfiled)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice with a mismatched Book: %v", err)
	}
	if err := st.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		got, err := tx.GetSettlementAdvice(ctx, bookA, "cyc_2", "EUR")
		if err != nil {
			return err
		}
		if got.Book != bookA {
			t.Errorf("an advice put under %s carrying Book %q read back as %q; "+
				"the argument chooses the book and the field records it", bookA, misfiled.Book, got.Book)
		}
		return nil
	}); err != nil {
		t.Fatalf("reading the misfiled advice: %v", err)
	}
	// And the field filed it nowhere: the OTHER bank, whose book the field
	// named, holds no such row — in a database this store cannot write to.
	if err := other.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		if _, err := tx.GetSettlementAdvice(ctx, bookB, "cyc_2", "EUR"); !errors.Is(err, payment.ErrSettlementAdviceNotFound) {
			t.Errorf("the second bank holds the advice the Book field named: got %v, want ErrSettlementAdviceNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("reading the second bank: %v", err)
	}
}

// advicesAreKeyedByReferenceNotByCycle pins that a bank's record of having
// booked a reserve movement is the same row whether the movement discharged a
// cut-off or a single return: the two put here differ only in what their
// Reference names — a cycle id and a payment id — and neither collides with the
// other, in the same book and the same asset.
func advicesAreKeyedByReferenceNotByCycle(t *testing.T, st payment.BankStore) {
	ctx := context.Background()
	cutOff := payment.SettlementAdvice{
		Book: bookA, Reference: "cyc_1", Asset: "EUR",
		Movement: -250000, ClosingBalance: 750000,
		Status: payment.AdviceAdvised, AdvisedAt: early,
	}
	rtn := payment.SettlementAdvice{
		Book: bookA, Reference: "pay_9", Asset: "EUR",
		Movement: 5000, ClosingBalance: 755000,
		Status: payment.AdvicePosted, MirrorTx: "txn_5",
		AdvisedAt: early, PostedAt: early,
	}
	if err := st.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		if err := tx.PutSettlementAdvice(ctx, cutOff.Book, cutOff); err != nil {
			return err
		}
		return tx.PutSettlementAdvice(ctx, rtn.Book, rtn)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice: %v", err)
	}

	var gotCutOff, gotReturn payment.SettlementAdvice
	var listed []payment.SettlementAdvice
	if err := st.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		var err error
		if gotCutOff, err = tx.GetSettlementAdvice(ctx, bookA, "cyc_1", "EUR"); err != nil {
			return err
		}
		if gotReturn, err = tx.GetSettlementAdvice(ctx, bookA, "pay_9", "EUR"); err != nil {
			return err
		}
		listed, err = tx.ListSettlementAdvices(ctx, bookA)
		return err
	}); err != nil {
		t.Fatalf("reading advices: %v", err)
	}
	if gotCutOff != cutOff {
		t.Errorf("the cycle-referenced advice round-tripped as %+v, want %+v", gotCutOff, cutOff)
	}
	if gotReturn != rtn {
		t.Errorf("the payment-referenced advice round-tripped as %+v, want %+v", gotReturn, rtn)
	}
	if len(listed) != 2 {
		t.Fatalf("the bank lists %d advices, want 2 — one referencing a cycle and one a payment", len(listed))
	}
}

func mandate(id payment.MandateID, createdAt time.Time) payment.Mandate {
	return payment.Mandate{
		ID: id,
		// The debtor's bank is an address on the row and the creditor's is not
		// stored at all: a mandate is the creditor bank's own. See
		// payment.Mandate.DebtorAgent.
		DebtorAgent: auroraBIC,
		Debtor: payment.PartyRef{Account: "dep_1",
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}},
		Creditor: payment.PartyRef{Account: "dep_2",
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
		NetPositions: map[iso20022.BIC]ledger.Amount{},
		OpenedAt:     openedAt,
	}
}

func settlement(id payment.SettlementID, cycleID payment.CycleID, settledAt time.Time) payment.Settlement {
	return payment.Settlement{
		ID:           id,
		CycleID:      cycleID,
		NetPositions: map[iso20022.BIC]ledger.Amount{},
		SettlementTx: "tx_1",
		ValueDate:    settledAt,
		SettledAt:    settledAt,
	}
}

// openPayment builds a fresh store for one subtest and closes it when the
// subtest ends. The three suites' open-and-close helpers, one set per
// institution.

// openPayment builds a fresh bank store for one subtest and closes it when the
// subtest ends. It takes a book because the advice cases need a SECOND bank,
// which means a second store.
func openPayment(t *testing.T, newStore func(*testing.T, ledger.BookID) payment.BankStore, book ledger.BookID) payment.BankStore {
	t.Helper()
	s := newStore(t, book)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// openClearingHouse and openCentralBank take no book: there is exactly one of
// each institution, and each answers for a book that is a constant.
func openClearingHouse(t *testing.T, newStore func(*testing.T) payment.ClearingHouseStore) payment.ClearingHouseStore {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func openCentralBank(t *testing.T, newStore func(*testing.T) payment.CentralBankStore) payment.CentralBankStore {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// update… and view… run a unit of work that is expected to succeed, one pair per
// institution.
func updateBank(t *testing.T, s payment.BankStore, fn func(context.Context, payment.BankTx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func viewBank(t *testing.T, s payment.BankStore, fn func(context.Context, payment.BankTx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}

func updateCsm(t *testing.T, s payment.ClearingHouseStore, fn func(context.Context, payment.CsmTx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func viewCsm(t *testing.T, s payment.ClearingHouseStore, fn func(context.Context, payment.CsmTx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}

func updateCentralBank(t *testing.T, s payment.CentralBankStore, fn func(context.Context, payment.CentralBankTx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func viewCentralBank(t *testing.T, s payment.CentralBankStore, fn func(context.Context, payment.CentralBankTx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}
