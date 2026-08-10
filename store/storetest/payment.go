package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The two banks this suite's fixtures are about.
//
// They are BICs because a bank's ParticipantID IS its BIC: a fixture id that is
// not a well-formed address is a bank no message could reach. See bankRow, which
// fills a bank's three identifiers from this one value because the store writes
// one column and derives the rest.
const (
	auroraBIC iso20022.BIC = "AURODEFFXXX"
	verdeBIC  iso20022.BIC = "VERDITMMXXX"
)

// RunPayment runs the payment-layer cases whose rows are a MEMBER BANK's: its
// own record of itself, the mandates it holds as creditor bank, its copy of each
// payment it is a party to, and the advices it was sent.
//
// It talks only to payment.Store and payment.Tx — never to payment.Network,
// which the race suites in races.go do instead — so what it pins is the storage
// contract: the not-found sentinels, the fact that a Bank's live handles are
// derived rather than stored, listing order, the end-to-end-id lookup, deep
// copying, and the three-layer rollback that payment.Tx embedding deposit.Tx
// embedding ledger.Tx exists to provide.
//
// # It was one suite over one store, and it is three
//
// The other two are RunClearingHousePayment and RunCentralBankPayment. The split
// is the SCHEMA's: a bank's database has no roster, no cycles and no
// settlements, so every case about those would be refused by name on it — which
// is the intended way to find a case that has been put in the wrong suite.
//
// Two cases make a narrower claim than they look like they should. The rollback
// case cannot claim that a cycle rolls back with a payment, because the two are
// in two databases and no transaction spans them; and the listing-order case
// orders one institution's rows at a time.
//
// newStore must return a store answering for the given book with no state in it;
// the suite calls it once per subtest and closes the result. It takes a book
// because the advice cases need a SECOND bank, which means a second store.
func RunPayment(t *testing.T, newStore func(*testing.T, ledger.BookID) payment.Store) {
	t.Helper()

	t.Run("BankRoundTripsAndDropsLiveHandles", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		p := bankRow(auroraBIC, "Aurora Bank", early)
		// A Network hands the store a fully bound Bank. Ledger and Deposit are
		// handles over the store, not data: there is no column that could hold a
		// *ledger.Book, and a store that kept them would hand back a Bank wired
		// to whatever it was wired to when it was written.
		p.Ledger = ledger.NewBook(nil, ledger.BookID(auroraBIC), nil)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutBank(ctx, p)
		})

		var got payment.Bank
		var listed []payment.Bank
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		// The product OpenCustomerAccount opens from. It is data like the
		// subledger above, not a handle like Ledger below, so it has to survive
		// the round trip — a store that drops it leaves every bank pricing
		// accounts from a product id of "", which fails as "product not found"
		// several layers away from the store that lost it.
		assertEqual(t, "product id", string(got.ProductID), "prd_basic")
		assertEqual(t, "product id in listings", string(listed[0].ProductID), "prd_basic")
		// The BIC is what the mesh routes on, and it is DERIVED from the id rather
		// than stored beside it: a bank's id IS its address, so there is no bic
		// column and nothing for a store to drop. What is asserted is that the
		// derivation happens on both read paths — a store that filled it in GetBank
		// and not in ListBanks would leave every bank in a listing unroutable.
		assertEqual(t, "bic", string(got.BIC), string(auroraBIC))
		assertEqual(t, "bic in listings", string(listed[0].BIC), string(auroraBIC))
		assertEqual(t, "suspense account", string(got.Assets["EUR"].Suspense), "200.200.001")
		assertEqual(t, "reserve account", string(got.Assets["EUR"].Reserve), "100.200.001")
		assertEqual(t, "settlement account", string(got.Assets["EUR"].Settlement), "200.100.001")
		assertEqual(t, "created at", got.CreatedAt.Equal(early), true)
		// Status is asserted on its own because it is the field whose default is not
		// safe. A Bank read back with Status "" is neither Founded nor a Member, and
		// both readers of it would take the wrong branch. Asserted in the listing
		// too, because a store can lose a column in one query and not the other.
		assertEqual(t, "status", string(got.Status), "Member")
		assertEqual(t, "status in listings", string(listed[0].Status), "Member")
		// The admission this bank recorded a membership under, asserted for the
		// same reason and in both queries. It is what RecordMembershipTx compares
		// an arriving acknowledgement against, so a store that drops it leaves
		// every member accepting an acknowledgement from any admission at all —
		// which was measured to move a bank's settlement reference onto an
		// invented account. See payment.Bank.AdmissionRef.
		assertEqual(t, "admission reference", got.AdmissionRef, "adm-"+string(auroraBIC))
		assertEqual(t, "admission reference in listings", listed[0].AdmissionRef, "adm-"+string(auroraBIC))

		assertEqual(t, "Ledger is not persisted", got.Ledger == nil, true)
		assertEqual(t, "Deposit is not persisted", got.Deposit == nil, true)
		assertEqual(t, "banks listed", len(listed), 1)
		assertEqual(t, "Ledger is not persisted in listings", listed[0].Ledger == nil, true)

		// PutBank is an upsert on ID: renaming a bank must not create a
		// second one.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			renamed := got
			renamed.Name = "Aurora Bank AB"
			return tx.PutBank(ctx, renamed)
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutBank(ctx, payment.Bank{
				ID: "alpha", Name: "Alpha", BookID: "alpha", CreatedAt: early,
				Assets: map[ledger.AssetCode]payment.BankAccounts{
					"EUR": {Suspense: "200.ib.001", Reserve: "100.ib.001", ReturnsReceivable: "200.ib.003", Settlement: "200.res.001"},
					"USD": {Suspense: "200.ib.002", Reserve: "100.ib.002", ReturnsReceivable: "200.ib.004", Settlement: "200.res.002"},
				},
			})
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutBank(ctx, payment.Bank{
				ID: "alpha", Name: "Alpha", BookID: "alpha", CreatedAt: early,
				Assets: map[ledger.AssetCode]payment.BankAccounts{
					"EUR": {Suspense: "200.ib.001", Reserve: "100.ib.001", Settlement: "200.res.001"},
				},
			})
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			got, err := tx.GetBank(ctx, "alpha")
			if err != nil {
				return err
			}
			if len(got.Assets) != 1 {
				t.Errorf("after upsert bank has %d assets, want 1", len(got.Assets))
			}
			return nil
		})

		// And the map the store hands back is the caller's own: mutating it
		// must not reach into the store. A SQL store cannot get that wrong; an
		// in-Go one gets it wrong by handing out the map it stored, which is why
		// the rule is written down rather than assumed.
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutBank(ctx, bankRow(auroraBIC, "Aurora Bank", early)); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "e2e-1", early)); err != nil {
				return err
			}
			return tx.PutMandate(ctx, mandate("mnd_1", early))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		// the debtor side than on the creditor side. The store holds a PartyRef's
		// identifier as two columns per side (scheme, value), and two same-shaped
		// TEXT columns is exactly the case a transposed insert argument or scan
		// target would not fail on — it would just read back wrong. Asserting both
		// sides, on both entities, independently is what would catch that.
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
		s := openPayment(t, newStore, bookA)

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
		// key cycle_payments.cycle_id already carries, would refuse the write
		// this fixture makes — and without this case nothing would notice.
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
		s := openPayment(t, newStore, bookA)

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
		s := openPayment(t, newStore, bookA)

		late := early.Add(time.Hour)

		// The same three rules the ledger and deposit fixtures use, because the
		// same two mistakes are available here: a CreatedAt tie only the
		// insertion sequence can break, IDs whose lexicographic order disagrees
		// with insertion order, and the row inserted FIRST carrying the LATEST
		// creation instant. Ordering by (CreatedAt, ID) fails on this fixture, and
		// so does ordering by sequence alone.
		//
		// The banks cannot use the 9 -> 10 boundary the other four do, because a
		// bank's id is its BIC and a BIC has a fixed shape. Four addresses whose
		// alphabetical order is not their insertion order make the same two
		// mistakes available, which is all the boundary was ever for.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			p := samplePayment("pay_8", "e2e-pay_8", early)
			p.Status = payment.Rejected
			return tx.PutPayment(ctx, p)
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		s := openPayment(t, newStore, bookA)

		// The same rule the ledger applies to a re-keyed idempotency key. The
		// end-to-end id index is maintained by the store, so a store that only ever
		// adds to it goes on resolving a reference the payment no longer carries —
		// and then refuses the next payment that legitimately claims it.
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

	// The deep-copy rule for the rows a BANK holds. The cycle's, which carries a
	// slice AND a map, and the settlement's are asserted in the two institution
	// suites — where the tables are.
	t.Run("PutIsAnUpsertAndDeepCopies", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		// A payment carries a map and a bank carries one too: the accounts it
		// holds per asset. A store that keeps the caller's reference lets a later
		// mutation rewrite history — which a store serialising on the way in
		// never can, so the rule has to be stated rather than left to the
		// backend.
		p := samplePayment("pay_1", "SCT-001", early)
		p.Metadata = map[string]string{"scheme": "sepa.ct"}
		bank := bankRow(auroraBIC, "Aurora Bank", early)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutPayment(ctx, p); err != nil {
				return err
			}
			return tx.PutBank(ctx, bank)
		})

		// Mutate the caller's copies after the write.
		p.Metadata["scheme"] = "tampered"
		bank.Assets["EUR"] = payment.BankAccounts{Suspense: "tampered"}

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			gotPayment, err := tx.GetPayment(ctx, "pay_1")
			if err != nil {
				return err
			}
			assertEqual(t, "payment metadata after reader mutation", gotPayment.Metadata["scheme"], "sepa.ct")
			return nil
		})

		// The upsert: a rejected payment replaces the accepted one rather than
		// adding a second row, and the status change is what is read back.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			rejected := samplePayment("pay_1", "SCT-001", early)
			rejected.Status = payment.Rejected
			return tx.PutPayment(ctx, rejected)
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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

	t.Run("UpdateRollsBackAllThreeLayersTogether", func(t *testing.T) {
		s := openPayment(t, newStore, bookA)

		// This is what the whole embedding chain exists for: a bank's own act
		// writes payment rows, posts through the ledger and reads the deposit
		// layer in one unit of work, so a failure must undo all of it.
		//
		// The rows are this INSTITUTION's, and that is the split narrowing what
		// the case can claim rather than weakening it. The cycle and the
		// settlement it also wrote are in two other databases, and the honest
		// statement about those is the opposite one: they cannot roll back with
		// these, because no transaction spans two databases. That is what the
		// mesh models everywhere else.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutBank(ctx, bankRow(auroraBIC, "Aurora Bank", early))
		})

		boom := errors.New("storetest: deliberate failure")
		err := s.Update(context.Background(), func(ctx context.Context, tx payment.Tx) error {
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

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutBank(ctx, bankRow(auroraBIC, "Aurora Bank", early)); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, samplePayment("pay_1", "SCT-001", early)); err != nil {
				return err
			}
			if err := tx.PutMandate(ctx, mandate("mnd_1", early)); err != nil {
				return err
			}
			return nil
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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

			// The end-to-end index is state too: a reference claimed before the
			// reset must be free afterwards.
			_, err = tx.GetPaymentByEndToEndID(ctx, "SCT-001")
			assertErrorIs(t, "end-to-end id after reset", err, payment.ErrPaymentNotFound)
			return nil
		})
	})
}

// RunClearingHousePayment runs the payment-layer cases whose rows are the
// CLEARING HOUSE's: the roster it routes by, and the cycles it cuts.
//
// It is a suite of its own because the division is the schema's. The clearing
// house's database has no banks table, no mandates, no settlements and no book
// of accounts at all, so every case above would be refused by name on it — which
// is the intended way to find one that has been put in the wrong suite.
//
// newStore must return the clearing house's store with no state in it; the suite
// calls it once per subtest and closes the result.
func RunClearingHousePayment(t *testing.T, newStore func(*testing.T) payment.Store) {
	t.Helper()

	// The cycle sentinels, which are the clearing house's half of what
	// GetOnMissingPaymentRowsReturnsSentinels asserts for the bank.
	t.Run("GetOnMissingCycleRowsReturnsSentinels", func(t *testing.T) {
		s := openInstitution(t, newStore)

		// A row to make the not-found path run against a populated store.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleSettled, early))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
	// while one store held every row kind. The fixture is that case's: a
	// CreatedAt tie only the insertion sequence can break, ids whose
	// lexicographic order disagrees with insertion order, and the row inserted
	// FIRST carrying the latest instant.
	// A cycle carries a slice AND a map, which is what makes it the row this rule
	// is most worth asserting on. See PutIsAnUpsertAndDeepCopies for the bank's
	// half of the same rule.
	// Reset empties this institution's tables, which is its share of what
	// ResetClearsPaymentState asserts for the bank. Each row kind is its own
	// table, so a table left out of the clear is a row that survives a reset —
	// and the whole point of Reset is that a reset store behaves like a fresh
	// one.
	t.Run("ResetClearsTheClearingHousesState", func(t *testing.T) {
		s := openInstitution(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutRosterEntry(ctx, rosterEntry("AURODEFFXXX", early)); err != nil {
				return err
			}
			return tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleOpen, early))
		})
		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
			return nil
		})
	})

	t.Run("CycleIsAnUpsertAndDeepCopies", func(t *testing.T) {
		s := openInstitution(t, newStore)

		c := cycle("cyc_1", payment.SchemeSEPACT, payment.CycleClosed, early)
		c.PaymentIDs = []payment.PaymentID{"pay_1"}
		c.NetPositions = map[iso20022.BIC]ledger.Amount{auroraBIC: 100}

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutCycle(ctx, c)
		})

		// Mutate the caller's copy after the write.
		c.PaymentIDs[0] = "pay_tampered"
		c.NetPositions[auroraBIC] = 999

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutCycle(ctx, cycle("cyc_1", payment.SchemeSEPACT, payment.CycleSettled, early))
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			all, err := tx.ListCycles(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "cycles after an upsert", len(all), 1)
			assertEqual(t, "cycle status after an upsert", all[0].Status.String(), payment.CycleSettled.String())
			return nil
		})
	})

	t.Run("CycleListOrderingIsOpenedAtThenSeq", func(t *testing.T) {
		s := openInstitution(t, newStore)
		late := early.Add(time.Hour)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutCycle(ctx, cycle("cyc_8", payment.SchemeSEPACT, payment.CycleSettled, early))
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
	//
	// The clearing house routes. It has no business holding a bank's subledger,
	// its product, or its account at the central bank, and the way that stops
	// being a promise is a check on the STRUCT: the table below is the whole set
	// of fields a RosterEntry may have, so a field added to it fails this case by
	// name instead of passing silently.
	//
	// AdmissionRef is in the table and is not an account identifier. It is the
	// PrcId every message of one admission echoes — a correlator for a
	// conversation, naming no account in any book — and the clearing house's
	// refusal is what reads it. What this case exists to keep out is an
	// identifier that would let this institution reach into another's ledger; a
	// process id reaches nothing.
	//
	// Issuer is in the table and is the reason the row exists at all. A bank code
	// is a national registry's allocation with no computable relationship to a
	// BIC, so turning the address a payer quotes into the agent a message is sent
	// to takes a published pairing — and publishing it is what a clearing house
	// does. It names no account in anybody's book: it says which INSTITUTION
	// issued a range, which is routing in the most literal sense this table has.
	//
	// Name is NOT in the table. The acmt.010 this row is written from identifies
	// the account owner with an OrganisationIdentification29, which has a BIC and
	// no name element at all, so a name here could only be filled by the clearing
	// house remembering the application across the relay. This case is what makes
	// putting it back a failure rather than a quiet regression.
	t.Run("RosterEntryCarriesNoAccountIdentifiers", func(t *testing.T) {
		s := openInstitution(t, newStore)

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

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutRosterEntry(ctx, rosterEntry("AURODEFFXXX", early)); err != nil {
				return err
			}
			return tx.PutRosterEntry(ctx, rosterEntry("VERDITMMXXX", early.Add(time.Hour)))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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

	// RosterEntryAssetsAreAnOrderedList pins the two properties of Assets that
	// its Go type has and a set does not: the caller's ORDER survives, and a
	// REPEATED asset is stored rather than refused.
	//
	// The second is the one this case was written for: a store must hold what the
	// Go type it is handed can hold, and a child table keyed by (bic, asset) would
	// refuse a slice that repeats an asset.
	//
	// No writer in the system reaches it. payment.AdmitMemberTx takes the assets
	// from a map keyed by asset and appends only the ones the entry does not
	// already hold, so a message that repeats a currency collapses before this
	// table is reached — and payment.ReadAdmissionAcknowledgement will not read an
	// acknowledgement naming two accounts in one currency, so the repeat cannot
	// arrive from the wire either.
	//
	// What is asserted here is the STORE's contract with the Go type it is
	// handed: Assets is a slice, a slice can repeat, and a store must hold what a
	// caller passes it whether or not any caller passes that.
	//
	// What a store must NOT do is decide about it. Refusing a duplicate is a
	// judgement about the message that carried it, and it belongs to the
	// institution reading the message; a store that refused would make that
	// judgement in one store and not the other, which is how this started.
	//
	// The order is asserted with a fixture that is NOT alphabetical, on purpose.
	// The other roster case uses EUR, USD, which a store that sorted its child
	// rows would pass by accident.
	t.Run("RosterEntryAssetsAreAnOrderedList", func(t *testing.T) {
		s := openInstitution(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			e := rosterEntry("AURODEFFXXX", early)
			e.Assets = []ledger.AssetCode{"USD", "EUR", "USD"}
			return tx.PutRosterEntry(ctx, e)
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			e := rosterEntry("AURODEFFXXX", early)
			e.Assets = []ledger.AssetCode{"GBP"}
			return tx.PutRosterEntry(ctx, e)
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		s := openInstitution(t, newStore)

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
}

// RunCentralBankPayment runs the payment-layer cases whose rows are the CENTRAL
// BANK's: its own register of the members it holds settlement accounts for.
//
// See RunClearingHousePayment on why this is a suite of its own.
//
// newStore must return the central bank's store with no state in it; the suite
// calls it once per subtest and closes the result.
func RunCentralBankPayment(t *testing.T, newStore func(*testing.T) payment.Store) {
	t.Helper()

	// The settlement sentinel and the settlement listing's order, which were the
	// central bank's share of two cases that ran against one store.
	t.Run("GetOnAMissingSettlementReturnsTheSentinel", func(t *testing.T) {
		s := openInstitution(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutSettlement(ctx, settlement("set_1", "cyc_1", early))
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			_, err := tx.GetSettlement(ctx, "set_nope")
			assertErrorIs(t, "GetSettlement on an unknown settlement", err, payment.ErrSettlementNotFound)
			return nil
		})
	})

	// A settlement carries a map of net positions, and the deep-copy rule is the
	// same one PutIsAnUpsertAndDeepCopies states for the bank's rows.
	// See ResetClearsTheClearingHousesState for what this is a share of.
	t.Run("ResetClearsTheCentralBanksState", func(t *testing.T) {
		s := openInstitution(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutSettlementMember(ctx, settlementMember("AURODEFFXXX", "Aurora Bank", early)); err != nil {
				return err
			}
			return tx.PutSettlement(ctx, settlement("set_1", "cyc_1", early))
		})
		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		s := openInstitution(t, newStore)

		st := settlement("set_1", "cyc_1", early)
		st.NetPositions = map[iso20022.BIC]ledger.Amount{auroraBIC: 100}
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutSettlement(ctx, st)
		})
		st.NetPositions[auroraBIC] = 999

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			got, err := tx.GetSettlement(ctx, "set_1")
			if err != nil {
				return err
			}
			assertEqual(t, "settlement net position after caller mutation", got.NetPositions[auroraBIC], ledger.Amount(100))
			return nil
		})
	})

	t.Run("SettlementListOrderingIsSettledAtThenSeq", func(t *testing.T) {
		s := openInstitution(t, newStore)
		late := early.Add(time.Hour)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
	//
	// The settlement agent holds no roster and no participant ids. What an
	// acmt.007 tells it is a BIC, so a lookup by anything else is a lookup it
	// could not make — which is why the store is asked for this row by BIC here
	// and never by a bank id.
	t.Run("SettlementMemberIsKeyedByBIC", func(t *testing.T) {
		s := openInstitution(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutSettlementMember(ctx, settlementMember("AURODEFFXXX", "Aurora Bank", early)); err != nil {
				return err
			}
			return tx.PutSettlementMember(ctx, settlementMember("VERDITMMXXX", "Banca Verde", early.Add(time.Hour)))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			renamed := settlementMember("VERDITMMXXX", "Banca Verde SpA", early.Add(time.Hour))
			return tx.PutSettlementMember(ctx, renamed)
		})
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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

	// SettlementMemberKeepsOneAccountPerAsset pins that the map survives the
	// round trip with its keys.
	//
	// A member read back with an empty map settles nothing — the settlement
	// agent would have no account to post the net position of a cut-off to — and
	// the map is a second TABLE, so "the row came back" and "the accounts came
	// back" are two different claims about two different reads.
	t.Run("SettlementMemberKeepsOneAccountPerAsset", func(t *testing.T) {
		s := openInstitution(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutSettlementMember(ctx, payment.SettlementMember{
				BIC: "AURODEFFXXX", Name: "Aurora Bank", OpenedAt: early,
				Accounts: map[ledger.AssetCode]ledger.AccountID{
					"EUR": "200.100.001",
					"USD": "200.100.002",
				},
			})
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutSettlementMember(ctx, payment.SettlementMember{
				BIC: "AURODEFFXXX", Name: "Aurora Bank", OpenedAt: early,
				Accounts: map[ledger.AssetCode]ledger.AccountID{"EUR": "200.100.001"},
			})
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
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
}

// ---------------------------------------------------------------------------
// Payment helpers
// ---------------------------------------------------------------------------

// bankRow is one bank's own record of itself, admitted: Member rather than
// Founded, because that is what every bank in this suite's other cases is and
// the status a store drops has to be a status it was given.
//
// It is keyed by the only identifier it has: a bank's ParticipantID, its BIC and
// its BookID are one value, so this takes one and fills all three. A fixture
// that set them independently could not be round-tripped — the store writes the
// key alone and derives the other two back out of it.
func bankRow(bic iso20022.BIC, name string, createdAt time.Time) payment.Bank {
	id := payment.ParticipantID(bic)
	return payment.Bank{
		ID:                id,
		Name:              name,
		BIC:               bic,
		BookID:            ledger.BookID(bic),
		CustomerSubledger: "100",
		ProductID:         "prd_basic",
		Status:            payment.BankMember,
		AdmissionRef:      "adm-" + string(id),
		Assets: map[ledger.AssetCode]payment.BankAccounts{
			"EUR": {Suspense: "200.200.001", Reserve: "100.200.001", Settlement: "200.100.001"},
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
//
// It is stored rather than looked up because looking it up is a read of another
// bank's deposit register. A store that dropped these fields would send the
// name-reading code back.
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

// paymentRecordsWhereTheCreditorLegLanded pins that
// payment.Payment.CreditorLegAccount survives a round trip in both of the states
// it has, and that the empty one is a value rather than a missing field.
//
// This is a MONEY column, not a trace. payment.PostReturnLegTx claws the funds
// back from the account named here, and the account is not the payee's whenever
// the creditor leg diverted to unclaimed balances. A store that dropped it would
// send a return to the payee's GL account — which for a diverted payment was
// never credited — and the ledger would post it happily: an overdrawn deposit is
// a Liability going negative, which nothing in the book refuses.
//
// Both states are asserted because both are written. The empty one is what every
// payment carries until its creditor leg posts, and it is stored as ” under a
// NOT NULL DEFAULT ”, so a store that turned it into a NULL — or a CHECK that
// refused it — would refuse the ordinary case.
func paymentRecordsWhereTheCreditorLegLanded(t *testing.T, st payment.Store) {
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

	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
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
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
//
// The half-returned state is the one that matters, and it is not a corner case:
// it is what every return looks like between the two banks' acts. The returning
// bank posts its leg and sends; the other bank posts hours later. In between,
// exactly one of these columns is set, and it is the ONLY thing that tells the
// second bank it is the second — payment.PostReturnLegTx reads the other side's
// id to decide whether this leg takes the payment to Returned. A store that
// dropped either column, or that turned an unset one into anything other than
// the empty value, would make both banks think they were first: two clawbacks
// and no refund, or a payment stuck at Settled with the money in two suspenses.
//
// A leg that has not been posted has no transaction, and an absent id and an
// empty one are the same fact here.
func paymentRecordsBothReturnLegs(t *testing.T, st payment.Store) {
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
	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
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
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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

// settlementAdviceIsScopedToTheBankThatWasAdvised pins that an advice belongs to
// ONE bank's book and that two banks advised of the same cycle do not collide.
//
// The book is part of the key, not a column on it. A member bank's record of
// what it was told about a cut-off is its own — it lives in that bank's store
// and nowhere else — and a key that omitted the book would make the second
// bank's advice overwrite the first's.
func settlementAdviceIsScopedToTheBankThatWasAdvised(t *testing.T, st, other payment.Store) {
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
	// each bank holds its own database. The scoping this case is named for used
	// to be a book_id column in one store; it is two databases now, and the
	// listing below is scoped because there is nothing else in it.
	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return tx.PutSettlementAdvice(ctx, one.Book, one)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice: %v", err)
	}
	if err := other.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return tx.PutSettlementAdvice(ctx, two.Book, two)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice at the second bank: %v", err)
	}

	var gotOne, gotTwo payment.SettlementAdvice
	var listed []payment.SettlementAdvice
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		var err error
		if gotOne, err = tx.GetSettlementAdvice(ctx, bookA, "cyc_1", "EUR"); err != nil {
			return err
		}
		listed, err = tx.ListSettlementAdvices(ctx, bookA)
		return err
	}); err != nil {
		t.Fatalf("reading advices: %v", err)
	}
	if err := other.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
	//
	// payment.Store documents this list as AdvisedAt then seq, like every other
	// listing in the interface. A SQL store gets there with ORDER BY advised_at,
	// seq; an in-Go one had to sort a map whose iteration order the runtime
	// randomises, which is how a missing ORDER BY and a missing sort were both
	// reachable. This is the only place either could be caught: a bank in one
	// asset holds one advice per cut-off, and it takes three cut-offs before
	// order means anything.
	//
	// The last two share an instant, which is the half that matters. Ties are
	// broken by INSERTION sequence and not by cycle id — cyc_4 is written before
	// cyc_3 and must come back first — so a store that fell back to sorting by
	// key would pass on distinct timestamps and fail here.
	later := early.Add(time.Hour)
	for _, a := range []payment.SettlementAdvice{
		{Book: bookA, Reference: "cyc_4", Asset: "EUR", Movement: 40, ClosingBalance: 40,
			Status: payment.AdviceAdvised, AdvisedAt: later},
		{Book: bookA, Reference: "cyc_3", Asset: "EUR", Movement: 30, ClosingBalance: 30,
			Status: payment.AdviceAdvised, AdvisedAt: later},
	} {
		if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutSettlementAdvice(ctx, a.Book, a)
		}); err != nil {
			t.Fatalf("PutSettlementAdvice %s: %v", a.Reference, err)
		}
	}
	var ordered []payment.SettlementAdvice
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		_, err := tx.GetSettlementAdvice(ctx, bookA, "cyc_nope", "EUR")
		if !errors.Is(err, payment.ErrSettlementAdviceNotFound) {
			t.Errorf("got %v, want ErrSettlementAdviceNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}

	// The book ARGUMENT is the scope; the Book FIELD is the row's record of it.
	//
	// This is the only method in payment.Tx that carries a book twice, and
	// mesh/books_test.go's recorder relies on the two being the same thing: its
	// override notes the argument alone, and structCarriedBooks["PutSettlementAdvice"]
	// cites this subtest as the evidence that nothing else could be recorded.
	// The store holds it by construction — its INSERT writes book_id from the
	// argument and never reads a.Book — and an advice whose field disagrees with
	// the argument is the only thing that can tell whether that is still true.
	misfiled := payment.SettlementAdvice{
		Book: bookB, Reference: "cyc_2", Asset: "EUR",
		Movement: 100, ClosingBalance: 100,
		Status: payment.AdviceAdvised, AdvisedAt: early,
	}
	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return tx.PutSettlementAdvice(ctx, bookA, misfiled)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice with a mismatched Book: %v", err)
	}
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
	if err := other.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
// Reference names — a cycle id and a payment id — and neither collides with
// the other, in the same book and the same asset.
func advicesAreKeyedByReferenceNotByCycle(t *testing.T, st payment.Store) {
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
	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		if err := tx.PutSettlementAdvice(ctx, cutOff.Book, cutOff); err != nil {
			return err
		}
		return tx.PutSettlementAdvice(ctx, rtn.Book, rtn)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice: %v", err)
	}

	var gotCutOff, gotReturn payment.SettlementAdvice
	var listed []payment.SettlementAdvice
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
// subtest ends.
// openInstitution is openPayment for the two institution suites, whose factories
// take no book: there is exactly one clearing house and one central bank, each
// answering for a book that is a constant.
func openInstitution(t *testing.T, newStore func(*testing.T) payment.Store) payment.Store {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func openPayment(t *testing.T, newStore func(*testing.T, ledger.BookID) payment.Store, book ledger.BookID) payment.Store {
	t.Helper()
	s := newStore(t, book)
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
