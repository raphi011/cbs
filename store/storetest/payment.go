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

// RunPayment runs the payment-layer conformance suite against a store. Every
// payment.Store implementation must pass it identically.
//
// It talks only to payment.Store and payment.Tx — never to payment.Network — so
// what it pins is the storage contract: the not-found sentinels, the fact that
// a Bank's live handles are derived rather than stored, listing order,
// the open-cycle and end-to-end-id lookups, deep copying, and the three-layer
// rollback that payment.Tx embedding deposit.Tx embedding ledger.Tx exists to
// provide.
//
// newStore must return a store with no state in it; the suite calls it once per
// subtest and closes the result.
func RunPayment(t *testing.T, newStore func(*testing.T) payment.Store) {
	t.Helper()

	t.Run("BankRoundTripsAndDropsLiveHandles", func(t *testing.T) {
		s := openPayment(t, newStore)

		p := bankRow("bank_1", "Aurora Bank", early)
		// A Network hands the store a fully bound Bank. Ledger and
		// Deposit are handles over the store, not data: store/pg has no column
		// to put a *ledger.Book in, so store/mem must not keep them either —
		// otherwise code works in memory and breaks on Postgres.
		p.Ledger = ledger.NewBook(nil, "bank_1", nil)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutBank(ctx, p)
		})

		var got payment.Bank
		var listed []payment.Bank
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			var err error
			if got, err = tx.GetBank(ctx, "bank_1"); err != nil {
				return err
			}
			listed, err = tx.ListBanks(ctx)
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
		// Status is asserted on its own because it is the field whose default is
		// not safe. A Bank read back with Status "" is neither Founded nor a
		// Member, and both readers of it would take the wrong branch: a founded
		// bank that reads as a member is one the scheme thinks it can route to,
		// and a member that reads as founded is one that can no longer pay.
		// Asserted in the listing too, because a store can lose a column in one
		// query and not the other — the reason the BIC is asserted twice above.
		assertEqual(t, "status", string(got.Status), "Member")
		assertEqual(t, "status in listings", string(listed[0].Status), "Member")

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
		s := openPayment(t, newStore)

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
		// must not reach into the store, which store/mem could only get wrong
		// by handing out its own map.
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

	// SettlementMemberIsKeyedByBIC is the central bank's own record of a bank it
	// holds a settlement account for, and the point of the case is the key.
	//
	// The settlement agent holds no roster and no participant ids. What an
	// acmt.007 tells it is a BIC, so a lookup by anything else is a lookup it
	// could not make — which is why the store is asked for this row by BIC here
	// and never by a bank id.
	t.Run("SettlementMemberIsKeyedByBIC", func(t *testing.T) {
		s := openPayment(t, newStore)

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
	// under store/pg the map is a second table, so "the row came back" and "the
	// accounts came back" are two different claims about two different reads.
	t.Run("SettlementMemberKeepsOneAccountPerAsset", func(t *testing.T) {
		s := openPayment(t, newStore)

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
	t.Run("RosterEntryCarriesNoAccountIdentifiers", func(t *testing.T) {
		s := openPayment(t, newStore)

		allowed := map[string]bool{
			"BIC":          true,
			"Name":         true,
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
			if err := tx.PutRosterEntry(ctx, rosterEntry("AURODEFFXXX", "Aurora Bank", early)); err != nil {
				return err
			}
			return tx.PutRosterEntry(ctx, rosterEntry("VERDITMMXXX", "Banca Verde", early.Add(time.Hour)))
		})

		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			got, err := tx.GetRosterEntry(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "roster name", got.Name, "Aurora Bank")
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
	// The second is the one this case was written for. store/pg keyed this child
	// table by (bic, asset), so it refused with SQLSTATE 23505 a slice store/mem
	// stored verbatim — the single divergence between the two stores that this
	// suite exists to make impossible.
	//
	// No writer in the system reaches it, and this case is not about a writer.
	// It used to say the writer Task 17d adds would, by building the list from
	// an acmt.010's unbounded AccountForAction1; the writer turned out to be
	// payment.AdmitMemberTx at Task 17c, taking the assets from a map keyed by
	// asset and appending only the ones the entry does not already hold, so a
	// message that repeats a currency collapses before this table is reached.
	// The reader that message goes through has since landed too and refuses one
	// outright — payment.ReadAdmissionAcknowledgement will not read an
	// acknowledgement naming two accounts in one currency — so the repeat cannot
	// arrive from the wire either.
	// What is asserted here is the STORE's contract with the Go type it is
	// handed: Assets is a slice, a slice can repeat, and a store must hold what
	// a caller passes it whether or not any caller passes that.
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
		s := openPayment(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			e := rosterEntry("AURODEFFXXX", "Aurora Bank", early)
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

			// And through the listing, which is a second query in store/pg and
			// could order differently from the single read above.
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
			e := rosterEntry("AURODEFFXXX", "Aurora Bank", early)
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

	t.Run("GetOnMissingPaymentRowsReturnsSentinels", func(t *testing.T) {
		s := openPayment(t, newStore)

		// Seed one row of every kind, so the not-found path is exercised on a
		// populated store rather than only on an empty one.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutBank(ctx, bankRow("bank_1", "Aurora Bank", early)); err != nil {
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
			_, err := tx.GetBank(ctx, "bank_nope")
			assertErrorIs(t, "GetBank on an unknown bank", err, payment.ErrParticipantNotFound)

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
				if err := tx.PutBank(ctx, bankRow(payment.ParticipantID(p.id), p.id, p.at)); err != nil {
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

		var banks []payment.Bank
		var payments []payment.Payment
		var mandates []payment.Mandate
		var cycles []payment.ClearingCycle
		var settlements []payment.Settlement
		viewPayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			var err error
			if banks, err = tx.ListBanks(ctx); err != nil {
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

		assertOrder(t, "ListBanks", ids(banks, func(b payment.Bank) string { return string(b.ID) }),
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

		// A bank carries one too: the accounts it holds per asset.
		bank := bankRow("bank_1", "Aurora Bank", early)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutCycle(ctx, c); err != nil {
				return err
			}
			if err := tx.PutPayment(ctx, p); err != nil {
				return err
			}
			if err := tx.PutBank(ctx, bank); err != nil {
				return err
			}
			return tx.PutSettlement(ctx, st)
		})

		// Mutate the caller's copies after the write.
		c.PaymentIDs[0] = "pay_tampered"
		c.NetPositions["bank_1"] = 999
		p.Metadata["scheme"] = "tampered"
		st.NetPositions["bank_1"] = 999
		bank.Assets["EUR"] = payment.BankAccounts{Suspense: "tampered"}

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

			gotBank, err := tx.GetBank(ctx, "bank_1")
			if err != nil {
				return err
			}
			assertEqual(t, "bank suspense after caller mutation",
				string(gotBank.Assets["EUR"].Suspense), "200.200.001")

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

	t.Run("SettlementAdviceIsScopedToTheBankThatWasAdvised", func(t *testing.T) {
		settlementAdviceIsScopedToTheBankThatWasAdvised(t, openPayment(t, newStore))
	})

	t.Run("AdvicesAreKeyedByReferenceNotByCycle", func(t *testing.T) {
		advicesAreKeyedByReferenceNotByCycle(t, openPayment(t, newStore))
	})

	t.Run("PaymentRoundTripsPartyDetails", func(t *testing.T) {
		paymentRoundTripsPartyDetails(t, openPayment(t, newStore))
	})

	t.Run("PaymentRecordsWhereTheCreditorLegLanded", func(t *testing.T) {
		paymentRecordsWhereTheCreditorLegLanded(t, openPayment(t, newStore))
	})

	t.Run("PaymentRecordsBothReturnLegs", func(t *testing.T) {
		paymentRecordsBothReturnLegs(t, openPayment(t, newStore))
	})

	t.Run("UpdateRollsBackAllThreeLayersTogether", func(t *testing.T) {
		s := openPayment(t, newStore)

		// This is what the whole embedding chain exists for: SettleCycle writes
		// payment rows, posts through the ledger and reads the deposit layer in
		// one unit of work, so a failure must undo all of it.
		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			return tx.PutBank(ctx, bankRow("bank_1", "Aurora Bank", early))
		})

		boom := errors.New("storetest: deliberate failure")
		err := s.Update(context.Background(), func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutBank(ctx, bankRow("bank_2", "Banca Verde", early)); err != nil {
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
			_, err := tx.GetBank(ctx, "bank_2")
			assertErrorIs(t, "bank from the failed unit of work", err, payment.ErrParticipantNotFound)

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

			// The bank written before the failed unit of work survived:
			// a rollback undoes its own transaction, not the store.
			survivor, err := tx.GetBank(ctx, "bank_1")
			if err != nil {
				return err
			}
			assertEqual(t, "bank from the committed unit of work", survivor.Name, "Aurora Bank")
			return nil
		})
	})

	t.Run("ResetClearsPaymentState", func(t *testing.T) {
		s := openPayment(t, newStore)

		updatePayment(t, s, func(ctx context.Context, tx payment.Tx) error {
			if err := tx.PutBank(ctx, bankRow("bank_1", "Aurora Bank", early)); err != nil {
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
			// The other two rows admission writes. They are seeded here because
			// each is its own table in store/pg, so a table left out of the
			// truncation list is a row that survives a reset — and the whole
			// point of Reset is that a reset store behaves like a fresh one.
			if err := tx.PutSettlementMember(ctx, settlementMember("AURODEFFXXX", "Aurora Bank", early)); err != nil {
				return err
			}
			if err := tx.PutRosterEntry(ctx, rosterEntry("AURODEFFXXX", "Aurora Bank", early)); err != nil {
				return err
			}
			return tx.PutSettlement(ctx, settlement("set_1", "cyc_1", early))
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

			members, err := tx.ListSettlementMembers(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "settlement members after reset", len(members), 0)

			entries, err := tx.ListRosterEntries(ctx)
			if err != nil {
				return err
			}
			assertEqual(t, "roster entries after reset", len(entries), 0)

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

// bankRow is a bank's own record of itself, admitted: Member rather than
// Founded, because that is what every bank in this suite's other cases is and
// the status a store drops has to be a status it was given.
func bankRow(id payment.ParticipantID, name string, createdAt time.Time) payment.Bank {
	return payment.Bank{
		ID:                id,
		Name:              name,
		BIC:               "AURODEFFXXX",
		BookID:            ledger.BookID(id),
		CustomerSubledger: "100",
		ProductID:         "prd_basic",
		Status:            payment.BankMember,
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
// addressed to it, and which admission put it there.
func rosterEntry(bic iso20022.BIC, name string, admittedAt time.Time) payment.RosterEntry {
	return payment.RosterEntry{
		BIC:          bic,
		Name:         name,
		Assets:       []ledger.AssetCode{"EUR", "USD"},
		AdmissionRef: "adm-" + string(bic),
		AdmittedAt:   admittedAt,
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

// paymentRecordsWhereTheCreditorLegLanded pins that
// payment.Payment.CreditorLegAccount survives a round trip through both stores,
// in both of the states it has, and that the empty one is a value rather than a
// missing field.
//
// This is a MONEY column, not a trace. payment.PostReturnLegTx claws the funds
// back from the account named here, and the account is not the payee's whenever
// the creditor leg diverted to unclaimed balances. A store that dropped it would
// send a return to the payee's GL account — which for a diverted payment was
// never credited — and the ledger would post it happily: an overdrawn deposit is
// a Liability going negative, which nothing in the book refuses.
//
// Both states are asserted because both are written. The empty one is what every
// payment carries until its creditor leg posts, and it is stored in Postgres as
// ” under a NOT NULL DEFAULT ”, so a store that turned it into a NULL — or a
// CHECK that refused it — would refuse a write store/mem accepts.
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

	// And through the LISTING too, which is a different query in store/pg and
	// the same column list only because it is written down once.
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
// transaction ids survive a round trip through both stores, and that a payment
// carrying only ONE of them round-trips as one rather than as two or none.
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
// Both are stored in Postgres as ” under a NOT NULL DEFAULT ”, for
// creditor_leg_account's reason: a leg that has not been posted has no
// transaction, and an absent id and an empty one are the same fact here.
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

	// And through the LISTING, which is a different query in store/pg.
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
// what it was told about a cut-off is its own — under sub-project 8 it lives in
// that bank's store and nowhere else — and a key that omitted the book would
// make the second bank's advice overwrite the first's here and be unmigratable
// there.
func settlementAdviceIsScopedToTheBankThatWasAdvised(t *testing.T, st payment.Store) {
	ctx := context.Background()
	one := payment.SettlementAdvice{
		Book: "bank_2", Reference: "cyc_1", Asset: "EUR",
		Movement: -250000, ClosingBalance: 750000,
		Status: payment.AdviceAdvised, AdvisedAt: early,
	}
	two := payment.SettlementAdvice{
		Book: "bank_3", Reference: "cyc_1", Asset: "EUR",
		Movement: 250000, ClosingBalance: 250000,
		Status: payment.AdvicePosted, MirrorTx: "txn_9",
		AdvisedAt: early, PostedAt: early,
	}
	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		if err := tx.PutSettlementAdvice(ctx, one.Book, one); err != nil {
			return err
		}
		return tx.PutSettlementAdvice(ctx, two.Book, two)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice: %v", err)
	}

	var gotOne, gotTwo payment.SettlementAdvice
	var listed []payment.SettlementAdvice
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		var err error
		if gotOne, err = tx.GetSettlementAdvice(ctx, "bank_2", "cyc_1", "EUR"); err != nil {
			return err
		}
		if gotTwo, err = tx.GetSettlementAdvice(ctx, "bank_3", "cyc_1", "EUR"); err != nil {
			return err
		}
		listed, err = tx.ListSettlementAdvices(ctx, "bank_2")
		return err
	}); err != nil {
		t.Fatalf("reading advices: %v", err)
	}
	if gotOne != one {
		t.Errorf("bank_2's advice round-tripped as %+v, want %+v", gotOne, one)
	}
	if gotTwo != two {
		t.Errorf("bank_3's advice round-tripped as %+v, want %+v", gotTwo, two)
	}
	if len(listed) != 1 {
		t.Errorf("bank_2 lists %d advices, want 1 — the list is scoped to one book", len(listed))
	}

	// The ORDER, which the scoping assertion above could not reach: bank_2 held
	// exactly one row, so a listing that sorted by nothing at all passed.
	//
	// payment.Store documents this list as AdvisedAt then seq, like every other
	// listing in the interface, and the two stores arrive at it by different
	// means — store/pg with ORDER BY advised_at, seq and store/mem by sorting a
	// map, whose iteration order is deliberately randomised by the runtime. So a
	// single unordered read is exactly what this suite exists to catch, and this
	// is the only place it can be caught: a bank in one asset holds one advice
	// per cut-off, and it takes three cut-offs before order means anything.
	//
	// The last two share an instant, which is the half that matters. Ties are
	// broken by INSERTION sequence and not by cycle id — cyc_4 is written before
	// cyc_3 and must come back first — so a store that fell back to sorting by
	// key would pass on distinct timestamps and fail here.
	later := early.Add(time.Hour)
	for _, a := range []payment.SettlementAdvice{
		{Book: "bank_2", Reference: "cyc_4", Asset: "EUR", Movement: 40, ClosingBalance: 40,
			Status: payment.AdviceAdvised, AdvisedAt: later},
		{Book: "bank_2", Reference: "cyc_3", Asset: "EUR", Movement: 30, ClosingBalance: 30,
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
		ordered, err = tx.ListSettlementAdvices(ctx, "bank_2")
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
		t.Fatalf("bank_2 lists %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bank_2 lists %v, want %v — AdvisedAt ascending, ties by insertion sequence", got, want)
			break
		}
	}

	// A cycle this bank was never advised of is a sentinel, not a zero value: a
	// bank that read a zero advice would post a mirror leg of nothing and mark
	// a cut-off it never heard about as settled.
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		_, err := tx.GetSettlementAdvice(ctx, "bank_2", "cyc_nope", "EUR")
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
	// store/pg holds it by construction — its INSERT writes book_id from the
	// argument and never reads a.Book — so store/mem has to be made to agree, and
	// an advice whose field disagrees with the argument is the only thing that can
	// tell whether it still does.
	misfiled := payment.SettlementAdvice{
		Book: "bank_9", Reference: "cyc_2", Asset: "EUR",
		Movement: 100, ClosingBalance: 100,
		Status: payment.AdviceAdvised, AdvisedAt: early,
	}
	if err := st.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return tx.PutSettlementAdvice(ctx, "bank_2", misfiled)
	}); err != nil {
		t.Fatalf("PutSettlementAdvice with a mismatched Book: %v", err)
	}
	if err := st.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		got, err := tx.GetSettlementAdvice(ctx, "bank_2", "cyc_2", "EUR")
		if err != nil {
			return err
		}
		if got.Book != "bank_2" {
			t.Errorf("an advice put under bank_2 carrying Book %q read back as %q; "+
				"the argument chooses the book and the field records it", misfiled.Book, got.Book)
		}
		// And the field did not file it anywhere: bank_9 was never written to.
		if _, err := tx.GetSettlementAdvice(ctx, "bank_9", "cyc_2", "EUR"); !errors.Is(err, payment.ErrSettlementAdviceNotFound) {
			t.Errorf("bank_9 holds the advice its Book field named: got %v, want ErrSettlementAdviceNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("reading the misfiled advice: %v", err)
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
		Book: "bank_2", Reference: "cyc_1", Asset: "EUR",
		Movement: -250000, ClosingBalance: 750000,
		Status: payment.AdviceAdvised, AdvisedAt: early,
	}
	rtn := payment.SettlementAdvice{
		Book: "bank_2", Reference: "pay_9", Asset: "EUR",
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
		if gotCutOff, err = tx.GetSettlementAdvice(ctx, "bank_2", "cyc_1", "EUR"); err != nil {
			return err
		}
		if gotReturn, err = tx.GetSettlementAdvice(ctx, "bank_2", "pay_9", "EUR"); err != nil {
			return err
		}
		listed, err = tx.ListSettlementAdvices(ctx, "bank_2")
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
		t.Fatalf("bank_2 lists %d advices, want 2 — one referencing a cycle and one a payment", len(listed))
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
