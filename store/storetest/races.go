// The races: what the conformance suites cannot see.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// frozen is the clock the race suites read. The store handed in is built with
// the same instant by both implementations' test files, so every CreatedAt ties
// and an assertion that happens to depend on ordering has to say so.
func frozen() time.Time { return time.Unix(0, 0).UTC() }

// RunSystemRaces runs the two races that span institutions: an admission and a
// submission, each driven through payment's own acts across the whole set of
// stores.
func RunSystemRaces(t *testing.T, newStores func(*testing.T) payment.Stores) {
	t.Helper()

	// The central bank's chart of accounts is resolved by find-or-create *by
	// name*, and the schema deliberately does not close that with a constraint.
	t.Run("ConcurrentAdmissionsAgreeOnOneCentralBank", func(t *testing.T) {
		ctx := context.Background()
		stores := newStores(t)
		nets := payment.NewNetworks(stores, frozen)

		// One address each. The central bank keys its own member record by BIC, so
		// three banks on one BIC would be one member holding one reserve account, and
		// the per-participant loop below would compare that account with itself.
		names := []string{"Aurora Bank", "Banca Verde", "Nordkredit"}
		bics := []iso20022.BIC{"AURODEFFXXX", "VERDITMMXXX", "NORDSESSXXX"}
		errs := runConcurrently(len(names), func(i int) error {
			_, err := Admit(ctx, nets, names[i], bics[i], nil)
			return err
		})
		for _, err := range errs {
			assertNoError(t, err)
		}

		cb, err := nets.CentralBank().CentralBank()
		assertNoError(t, err)
		ledgers, err := cb.ListLedgers(ctx)
		assertNoError(t, err)
		assertEqual(t, "central bank ledgers", len(ledgers), 1)

		subledgers, err := cb.ListSubledgers(ctx, ledgers[0].ID)
		assertNoError(t, err)
		assertEqual(t, "central bank subledgers", len(subledgers), 2)

		// Every member's reserve account must sit in the SAME subledger.
		var members []payment.SettlementMember
		assertNoError(t, stores.CentralBank().View(ctx, func(ctx context.Context, tx payment.CentralBankTx) error {
			var err error
			members, err = tx.ListSettlementMembers(ctx)
			return err
		}))
		assertEqual(t, "settlement members", len(members), len(names))

		var reserves ledger.SubledgerID
		for _, m := range members {
			acct, err := cb.GetAccount(ctx, m.Accounts["EUR"])
			assertNoError(t, err)
			if reserves == "" {
				reserves = acct.SubledgerID
				continue
			}
			assertEqual(t, "reserve subledger for "+m.Name, acct.SubledgerID, reserves)
		}
	})

	// A client reference deduplicated by a read that was not atomic. This one was
	// live.
	t.Run("ConcurrentSubmissionsOfOneReferenceAcceptOne", func(t *testing.T) {
		ctx := context.Background()
		nets := payment.NewNetworks(newStores(t), frozen)

		debtorBank, err := Admit(ctx, nets, "Aurora Bank", "AURODEFFXXX", []ledger.AssetCode{"EUR"})
		assertNoError(t, err)
		creditorBank, err := Admit(ctx, nets, "Banca Verde", "VERDITMMXXX", []ledger.AssetCode{"EUR"})
		assertNoError(t, err)
		// The payer's bank, and the race below is eight of its own submissions.
		debtorNet, err := nets.Bank(ctx, debtorBank.ID)
		assertNoError(t, err)

		// Neither call names an address. A bank issues its customers' addresses
		// out of its own bank code, so the register mints one and it comes back
		// on the account — which is where the PartyRefs below read it from.
		alice, err := debtorBank.Deposit.OpenAccount(ctx, "Alice", "EUR", debtorBank.ProductID, 0)
		assertNoError(t, err)
		bruno, err := creditorBank.Deposit.OpenAccount(ctx, "Bruno", "EUR", creditorBank.ProductID, 0)
		assertNoError(t, err)

		const opening ledger.Amount = 1_000_000
		const amount ledger.Amount = 1_000
		assertNoError(t, debtorNet.Deposit(ctx, debtorBank.ID, alice.ID, opening, "opening"))

		req := payment.InitiatePaymentRequest{
			Scheme:          payment.SchemeSEPACT,
			Debtor:          payment.PartyRef{Account: alice.ID, Identifier: alice.Identifiers[0]},
			Creditor:        payment.PartyRef{Account: bruno.ID, Identifier: bruno.Identifiers[0]},
			Amount:          amount,
			EndToEndID:      "one-and-only",
			CreditorDetails: payment.PartyDetails{Agent: creditorBank.BIC, Name: bruno.Name},
		}

		errs := runConcurrently(8, func(int) error {
			_, err := debtorNet.SubmitPayment(ctx, req)
			return err
		})
		assertOneWinner(t, "SubmitPayment", errs, payment.ErrDuplicateEndToEndID)

		// The count of winners is not the assertion that matters. This is: the
		// payer was debited once, which is what a duplicate reference is for.
		balance, err := debtorBank.Deposit.GetBalance(ctx, alice.ID)
		assertNoError(t, err)
		assertEqual(t, "Alice's balance after eight submissions of one reference", balance.Book, opening-amount)

		// In the PAYER'S BANK's own database, which is where a submission writes.
		payments, err := debtorNet.ListPayments(ctx)
		assertNoError(t, err)
		assertEqual(t, "payment rows", len(payments), 1)
	})
}

// RunClearingHouseRaces runs the races that happen entirely in the CLEARING
// HOUSE's database.
func RunClearingHouseRaces(t *testing.T, newStore func(*testing.T) payment.ClearingHouseStore) {
	t.Helper()

	// Two admissions of one address, and they were live in the commit that split
	// admission into acts.
	t.Run("ConcurrentAdmissionsOfOneBICAdmitOne", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		// One institution, one store, so NewNetwork rather than the factory: a
		// Networks over a single store is not a thing that can exist any more,
		// and this case never needed a second institution.
		csm := payment.NewClearingHouseNetwork(s, frozen)

		// Several applicants, one address, a DIFFERENT admission reference each —
		// which is the only case AdmitMemberTx refuses, and the case that must not
		// depend on which store is underneath.
		for _, bic := range []iso20022.BIC{"AURODEFFXXX", "VERDITMMXXX", "NORDSESSXXX", "SOLEFRPPXXX", "BANKESMMXXX"} {
			refs := make([]string, 8)
			for i := range refs {
				refs[i] = fmt.Sprintf("adm-%s-%d", bic[:4], i)
			}
			errs := runConcurrently(len(refs), func(i int) error {
				ack := payment.AdmissionAcknowledgement{
					BIC: bic,
					// One allocation for all eight, because they are eight admissions of ONE
					// applicant and a code follows the bank rather than the conversation.
					Issuer: iban.Issuer{Country: FixtureCountry, BankCode: fixtureBankCode(bic)},
					Ref:    refs[i],
					Accounts: map[ledger.AssetCode]ledger.AccountID{
						"EUR": ledger.AccountID(fmt.Sprintf("200.100.00%d", i)),
					},
				}
				return s.Update(ctx, func(ctx context.Context, tx payment.CsmTx) error {
					_, err := csm.AdmitMemberTx(ctx, tx, ack)
					return err
				})
			})

			// The roster must route to the admission it accepted, and to no other.
			var entry payment.RosterEntry
			assertNoError(t, s.View(ctx, func(ctx context.Context, tx payment.CsmTx) error {
				var err error
				entry, err = tx.GetRosterEntry(ctx, bic)
				return err
			}))
			for i, err := range errs {
				if err == nil && entry.AdmissionRef != refs[i] {
					t.Errorf("admission %q was accepted on %s and the roster routes to %q instead",
						refs[i], bic, entry.AdmissionRef)
				}
			}
			assertOneWinner(t, "AdmitMemberTx", errs, payment.ErrBICAlreadyAdmitted)
		}
	})

}

// RunCentralBankRaces runs the races that happen entirely in the CENTRAL BANK's
// database.
func RunCentralBankRaces(t *testing.T, newStore func(*testing.T) payment.CentralBankStore) {
	t.Helper()

	// The lost update the same ordering closes, on the settlement agent's own row.
	t.Run("ConcurrentSettlementAccountOpeningsKeepEveryAsset", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		cb := payment.NewCentralBankNetwork(s, frozen)

		assets := []ledger.AssetCode{"EUR", "USD"}
		errs := runConcurrently(len(assets), func(i int) error {
			return s.Update(ctx, func(ctx context.Context, tx payment.CentralBankTx) error {
				_, _, err := cb.OpenSettlementAccountTx(ctx, tx, payment.AdmissionRequest{
					Name: "Aurora Bank", BIC: "AURODEFFXXX", Country: FixtureCountry,
					Asset: assets[i], Ref: "adm-aurora",
				})
				return err
			})
		})
		for _, err := range errs {
			assertNoError(t, err)
		}

		// Both requests are legitimate and both must survive: one request names one
		// currency, so this is how a two-currency bank is admitted.
		assertNoError(t, s.View(ctx, func(ctx context.Context, tx payment.CentralBankTx) error {
			member, err := tx.GetSettlementMember(ctx, "AURODEFFXXX")
			if err != nil {
				return err
			}
			assertEqual(t, "settlement accounts the central bank records", len(member.Accounts), 2)
			for _, asset := range assets {
				if member.Accounts[asset] == "" {
					t.Errorf("the member row names no %s account; the other request overwrote it", asset)
				}
			}
			return nil
		}))

		// And no account was opened that the row does not name. Counting them is
		// what catches the lost update from the other side: the row can name two
		// accounts while the book holds three.
		reserves := 0
		assertNoError(t, s.View(ctx, func(ctx context.Context, tx payment.CentralBankTx) error {
			accounts, err := tx.ListAccounts(ctx, payment.CentralBankBook)
			if err != nil {
				return err
			}
			for _, a := range accounts {
				if strings.Contains(a.Name, "Aurora Bank") {
					reserves++
				}
			}
			return nil
		}))
		assertEqual(t, "reserve accounts in the central bank's book", reserves, 2)
	})
}

// RunConcurrentTxRaces runs the races that need several units of work OPEN at
// the same instant.
func RunConcurrentTxRaces(t *testing.T, newStore func(*testing.T) ledger.Store) {
	t.Helper()

	// A balance check followed by a posting.
	t.Run("ConcurrentPostingsCannotBothSpendTheSameBalance", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		book := ledger.NewBook(s, "bank", frozen)

		cash, equity := fundedChart(t, book, 1_000)

		errs := raceInTransactions(t, s, 2, func(ctx context.Context, tx ledger.Tx, i int) error {
			_, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
				Description: "withdrawal",
				Entries: []ledger.Entry{
					{AccountID: equity, Amount: 600, Direction: ledger.Debit},
					{AccountID: cash, Amount: 600, Direction: ledger.Credit},
				},
			})
			return err
		})

		assertOneWinner(t, "concurrent withdrawals", errs, ledger.ErrInsufficientBalance)

		balance, err := book.BookBalance(ctx, cash.Total())
		assertNoError(t, err)
		assertEqual(t, "cash balance after the race", balance, ledger.Amount(400))
	})

	// An idempotency key.
	t.Run("ConcurrentPostsWithOneIdempotencyKeyCollide", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		book := ledger.NewBook(s, "bank", frozen)

		cash, equity := fundedChart(t, book, 10_000)

		errs := raceInTransactions(t, s, 2, func(ctx context.Context, tx ledger.Tx, i int) error {
			_, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
				IdempotencyKey: "same-key",
				Description:    "retry of one logical operation",
				Entries: []ledger.Entry{
					{AccountID: equity, Amount: 100, Direction: ledger.Debit},
					{AccountID: cash, Amount: 100, Direction: ledger.Credit},
				},
			})
			return err
		})

		assertOneWinner(t, "concurrent posts with one key", errs, ledger.ErrDuplicateIdempotencyKey)

		// Exactly one transaction was written, and it is the one holding the key.
		posted, err := book.GetTransactionByIdempotencyKey(ctx, "same-key")
		assertNoError(t, err)
		all, err := book.ListTransactions(ctx)
		assertNoError(t, err)
		// The opening capital posting from fundedChart, plus one of the two
		// racers.
		assertEqual(t, "transactions written", len(all), 2)
		assertEqual(t, "the surviving transaction is the keyed one", string(all[1].ID), string(posted.ID))
	})

	// The same key, at the store primitive.
	t.Run("ConcurrentPutTransactionWithOneIdempotencyKey", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		// Commit something into the book FIRST.
		if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutLedger(ctx, "bank", ledger.Ledger{ID: "ldg_1", Name: "GL", CreatedAt: frozen()})
		}); err != nil {
			t.Fatalf("seed book: %v", err)
		}

		ids := []ledger.TransactionID{"tx_a", "tx_b"}
		errs := raceInTransactions(t, s, 2, func(ctx context.Context, tx ledger.Tx, i int) error {
			return tx.PutTransaction(ctx, "bank", ledger.Transaction{
				ID: ids[i], IdempotencyKey: "same-key", Status: ledger.Posted, CreatedAt: frozen(),
				Entries: []ledger.Entry{
					{ID: ledger.EntryID(string(ids[i]) + "_1"), AccountID: "100.100.001", Amount: 100, Direction: ledger.Debit},
					{ID: ledger.EntryID(string(ids[i]) + "_2"), AccountID: "200.100.001", Amount: 100, Direction: ledger.Credit},
				},
			})
		})

		assertOneWinner(t, "concurrent PutTransaction with one key", errs, ledger.ErrDuplicateIdempotencyKey)

		// Exactly one row was written, and the key resolves to it.
		var all []ledger.Transaction
		var byKey ledger.Transaction
		if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			if all, err = tx.ListTransactions(ctx, "bank"); err != nil {
				return err
			}
			byKey, err = tx.GetTransactionByIdempotencyKey(ctx, "bank", "same-key")
			return err
		}); err != nil {
			t.Fatalf("read back: %v", err)
		}
		assertEqual(t, "transactions written", len(all), 1)
		assertEqual(t, "the key resolves to the surviving transaction", string(byKey.ID), string(all[0].ID))
	})

	// A reversal.
	t.Run("ConcurrentMarkReversedOnlyOneWins", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		const posted ledger.TransactionID = "tx_1"
		if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, "bank", ledger.Transaction{
				ID: posted, Status: ledger.Posted, CreatedAt: frozen(),
				Entries: []ledger.Entry{
					{ID: "ent_1", AccountID: "100.100.001", Amount: 250, Direction: ledger.Debit},
					{ID: "ent_2", AccountID: "300.100.001", Amount: 250, Direction: ledger.Credit},
				},
			})
		}); err != nil {
			t.Fatalf("seed transaction: %v", err)
		}

		errs := raceInTransactions(t, s, 2, func(ctx context.Context, tx ledger.Tx, i int) error {
			return tx.MarkReversed(ctx, "bank", posted)
		})

		assertOneWinner(t, "concurrent MarkReversed", errs, ledger.ErrTransactionAlreadyReversed)

		var got ledger.Transaction
		if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.GetTransaction(ctx, "bank", posted)
			return err
		}); err != nil {
			t.Fatalf("read back: %v", err)
		}
		assertEqual(t, "status", got.Status.String(), ledger.Reversed.String())
	})

	// LockAccounts sorts by id, so two postings over the same pair of accounts
	// take the locks in the same order and one simply waits.
	t.Run("OverlappingAccountSetsDoNotDeadlock", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		book := ledger.NewBook(s, "bank", frozen)

		cash, equity := fundedChart(t, book, 10_000)

		// The two directions name the same accounts in opposite order, which is
		// the classic deadlock shape.
		errs := raceInTransactions(t, s, 2, func(ctx context.Context, tx ledger.Tx, i int) error {
			entries := []ledger.Entry{
				{AccountID: equity, Amount: 100, Direction: ledger.Debit},
				{AccountID: cash, Amount: 100, Direction: ledger.Credit},
			}
			if i == 1 {
				entries[0], entries[1] = entries[1], entries[0]
			}
			_, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{Description: "overlap", Entries: entries})
			return err
		})

		for _, err := range errs {
			assertNoError(t, err)
		}
		balance, err := book.BookBalance(ctx, cash.Total())
		assertNoError(t, err)
		assertEqual(t, "cash balance after both postings", balance, ledger.Amount(9_800))
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fundedChart builds a minimal chart of accounts and funds the cash account,
// returning the Asset account under test and an Equity counterparty. Equity is
// not balance-checked, so it can absorb any leg the test needs.
func fundedChart(t *testing.T, book *ledger.Book, amount ledger.Amount) (cash, equity ledger.AccountID) {
	t.Helper()
	ctx := context.Background()

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	assets, err := book.CreateSubledger(ctx, gl.ID, "Bank Assets")
	assertNoError(t, err)
	capital, err := book.CreateSubledger(ctx, gl.ID, "Capital")
	assertNoError(t, err)

	cashAcct, err := book.CreateAccount(ctx, assets.ID, "Cash", ledger.Asset, "EUR")
	assertNoError(t, err)
	equityAcct, err := book.CreateAccount(ctx, capital.ID, "Share Capital", ledger.Equity, "EUR")
	assertNoError(t, err)

	_, err = book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "opening capital",
		Entries: []ledger.Entry{
			{AccountID: cashAcct.ID, Amount: amount, Direction: ledger.Debit},
			{AccountID: equityAcct.ID, Amount: amount, Direction: ledger.Credit},
		},
	})
	assertNoError(t, err)

	return cashAcct.ID, equityAcct.ID
}

// raceInTransactions runs fn in n concurrent units of work that are all
// guaranteed to be OPEN before any of them does its first statement.
func raceInTransactions(t *testing.T, s ledger.Store, n int, fn func(context.Context, ledger.Tx, int) error) []error {
	t.Helper()
	gate := newBarrier(n)
	return runConcurrently(n, func(i int) error {
		return s.Update(context.Background(), func(ctx context.Context, tx ledger.Tx) error {
			// BEGIN has been issued and a connection is held. Wait until every
			// racer is at the same point, then go.
			gate.arrive()
			return fn(ctx, tx, i)
		})
	})
}

// barrier releases its arrivals once n of them have shown up. arrive is
// idempotent per goroutine, because Update may retry its callback.
type barrier struct {
	mu        sync.Mutex
	remaining int
	released  chan struct{}
}

func newBarrier(n int) *barrier {
	return &barrier{remaining: n, released: make(chan struct{})}
}

func (b *barrier) arrive() {
	b.mu.Lock()
	if b.remaining > 0 {
		b.remaining--
		if b.remaining == 0 {
			close(b.released)
		}
	}
	b.mu.Unlock()
	<-b.released
}

// runConcurrently runs fn n times at once, released from a common starting gun
// so the calls genuinely overlap, and returns each call's error.
func runConcurrently(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			errs[i] = fn(i)
		}()
	}
	start.Done()
	done.Wait()
	return errs
}

// assertOneWinner asserts that exactly one of the concurrent calls succeeded and
// that every loser failed with the expected error — not merely that "something
// failed", which a broken connection would also satisfy.
func assertOneWinner(t *testing.T, label string, errs []error, loser error) {
	t.Helper()
	winners := 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, loser):
		default:
			t.Fatalf("%s: unexpected error %v, want nil or %v", label, err, loser)
		}
	}
	if winners != 1 {
		t.Fatalf("%s: %d of %d succeeded, want exactly 1 (errors: %v)", label, winners, len(errs), errs)
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fixtureBankCode is a distinct, well-formed German bank code per address, for
// the races above.
func fixtureBankCode(bic iso20022.BIC) iban.BankCode {
	var h uint32 = 2166136261
	for _, c := range []byte(bic) {
		h = (h ^ uint32(c)) * 16777619
	}
	return iban.BankCode(fmt.Sprintf("%08d", h%100_000_000))
}
