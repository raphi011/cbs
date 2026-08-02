// The Postgres store's tests. Everything here skips without TEST_DATABASE_URL,
// so `go test ./...` needs no database.
//
// The conformance suites are the important part: store/pg has to behave exactly
// as store/mem does, and storetest is what says so. The tests below them are the
// ones that CANNOT be written against store/mem, because they are about two
// transactions running at once — which under a single process-wide mutex is not
// a thing that happens.
package pg_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/pg"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

// frozen is the clock every test store reads: a fixed instant, so CreatedAt ties
// everywhere and the ordering assertions have to rely on the seq column rather
// than on wall-clock luck.
func frozen() time.Time { return time.Unix(0, 0).UTC() }

// requireDSN skips a test unless a database was provided.
func requireDSN(t *testing.T) string {
	t.Helper()
	dsn := testenv.DSN()
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres tests")
	}
	return dsn
}

// newStore opens a store in a schema of its own, migrated and empty, dropped
// when the test ends.
func newStore(t *testing.T) *pg.Store {
	t.Helper()
	return testenv.OpenInFreshSchema(t, requireDSN(t), frozen)
}

// TestConformance runs all three shared conformance suites against Postgres.
// store/mem runs the identical suites; any behaviour the two could drift apart
// on belongs in storetest rather than here.
func TestConformance(t *testing.T) {
	requireDSN(t)

	storetest.RunLedger(t, func(t *testing.T) ledger.Store { return newStore(t) })
	storetest.RunDeposit(t, func(t *testing.T) deposit.Store { return newStore(t).Deposit() })
	storetest.RunProduct(t, func(t *testing.T) product.Store { return newStore(t).Product() })
	storetest.RunPayment(t, func(t *testing.T) payment.Store { return newStore(t).Payment() })
	storetest.RunLending(t, func(t *testing.T) lending.Store { return newStore(t).Lending() })
}

// A unit of work must never be opened inside another one on the same store.
//
// store/mem refuses this because its mutex would deadlock. Here the failure
// would be quieter and worse: the nested Update takes a SECOND connection and
// runs a SEPARATE transaction, so its writes would commit even when the outer
// ones roll back. Both stores refuse it, so the mistake behaves the same either
// way.
func TestNestedUnitOfWorkIsRefused(t *testing.T) {
	s := newStore(t)

	cases := map[string]func(context.Context) error{
		"UpdateInUpdate": func(ctx context.Context) error {
			return s.Update(ctx, func(ctx context.Context, _ ledger.Tx) error { return nil })
		},
		"ViewInUpdate": func(ctx context.Context) error {
			return s.View(ctx, func(ctx context.Context, _ ledger.Tx) error { return nil })
		},
		"DepositUpdateInUpdate": func(ctx context.Context) error {
			return s.Deposit().Update(ctx, func(ctx context.Context, _ deposit.Tx) error { return nil })
		},
		"PaymentUpdateInUpdate": func(ctx context.Context) error {
			return s.Payment().Update(ctx, func(ctx context.Context, _ payment.Tx) error { return nil })
		},
		"PaymentViewInUpdate": func(ctx context.Context) error {
			return s.Payment().View(ctx, func(ctx context.Context, _ payment.Tx) error { return nil })
		},
	}

	for name, nested := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.Update(context.Background(), func(ctx context.Context, _ ledger.Tx) error {
				return nested(ctx)
			})
			if !errors.Is(err, pg.ErrNestedTransaction) {
				t.Fatalf("nested %s: got error %v, want %v", name, err, pg.ErrNestedTransaction)
			}
		})
	}

	// A different store is not the same unit of work, so driving two stores from
	// one context stays legal.
	other := newStore(t)
	err := s.Update(context.Background(), func(ctx context.Context, _ ledger.Tx) error {
		return other.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutLedger(ctx, "book", ledger.Ledger{ID: "ldg_1"})
		})
	})
	if err != nil {
		t.Fatalf("unit of work on a second store: %v", err)
	}
}

// View opens a READ ONLY transaction, so a write through its Tx cannot be part
// of anything that commits. It is refused with the same shape of error store/mem
// uses.
func TestViewRejectsWrites(t *testing.T) {
	s := newStore(t)

	cases := map[string]func(context.Context, ledger.Tx) error{
		"NextID": func(ctx context.Context, tx ledger.Tx) error {
			_, err := tx.NextID(ctx, "book", "ldg")
			return err
		},
		"PutLedger": func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutLedger(ctx, "book", ledger.Ledger{ID: "ldg_1"})
		},
		"AppendAudit": func(ctx context.Context, tx ledger.Tx) error {
			return tx.AppendAudit(ctx, ledger.AuditEvent{ID: "evt_1", BookID: "book"})
		},
		"MarkReversed": func(ctx context.Context, tx ledger.Tx) error {
			return tx.MarkReversed(ctx, "book", "tx_1")
		},
	}

	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.View(context.Background(), write)
			if !errors.Is(err, pg.ErrReadOnly) {
				t.Fatalf("View(%s): got error %v, want %v", name, err, pg.ErrReadOnly)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The three races
// ---------------------------------------------------------------------------
//
// Each of the tests below runs two units of work at once and asserts that
// exactly one wins. None of them can fail against store/mem, because store/mem
// serializes every unit of work behind one lock — which is precisely why these
// races were invisible until there was a second implementation.

// Race 1: a balance check followed by a posting.
//
// Two withdrawals of 600 against an asset account holding 1000. Without
// LockAccounts both transactions read 1000 under READ COMMITTED, both conclude
// there are enough funds, and both post — leaving the account at -200, which the
// ledger's own rules say is impossible.
func TestConcurrentPostingsCannotBothSpendTheSameBalance(t *testing.T) {
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

	balance, err := book.BookBalance(ctx, cash)
	assertNoError(t, err)
	assertEqual(t, "cash balance after the race", balance, ledger.Amount(400))
}

// Race 2: an idempotency key.
//
// ledger.Book checks the key before posting, and that check is exactly the
// window a duplicate slips through: both transactions look, both find nothing,
// both post. The partial unique index closes it, because there the check and the
// write are the same statement.
func TestConcurrentPostsWithOneIdempotencyKeyCollide(t *testing.T) {
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
	// The opening capital posting from fundedChart, plus one of the two racers.
	assertEqual(t, "transactions written", len(all), 2)
	assertEqual(t, "the surviving transaction is the keyed one", string(all[1].ID), string(posted.ID))
}

// Race 2, at the store primitive. The test above drives the race through
// Book.PostTransactionTx, which is the realistic path but NOT an isolating one:
// PostTransactionTx allocates entry and transaction IDs before it calls
// PutTransaction, and NextID's INSERT … ON CONFLICT DO UPDATE takes a row lock
// on id_sequences. The two racers therefore serialize on the counter, and the
// loser's pre-check runs after the winner has committed and duly finds the row —
// so a check-then-insert PutTransaction passes it. That was measured, not
// assumed.
//
// This test calls tx.PutTransaction directly with two pre-chosen IDs and one
// shared key, so nothing allocates an ID and nothing serializes the racers
// before they reach the index. It is the same isolation
// TestConcurrentMarkReversedOnlyOneWins needed, for the same reason.
//
// It kills both dangerous shapes: a check-then-insert that drops the SQLSTATE
// mapping (the loser surfaces a raw 23505, a 500 where the API owes a 409), and
// a check-then-insert with the unique index removed (both racers win).
func TestConcurrentPutTransactionWithOneIdempotencyKey(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Commit something into the book FIRST. Without this the racers serialize
	// before they ever reach the index: the first write to a book inserts its
	// books row, and a concurrent INSERT … ON CONFLICT DO NOTHING blocks on that
	// uncommitted tuple until the first transaction commits. Measured — with
	// this line removed, a check-then-insert PutTransaction passes five runs out
	// of five. Every serialization point before the statement under test has to
	// be paid off before the race is a race.
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
}

// Race 3: a reversal.
//
// Posted -> Reversed happens at most once. A read-compare-write would let two
// concurrent reversals both see Posted and both mark it, and the ledger would
// then hold two counter-transactions for one original.
//
// This one drives tx.MarkReversed directly rather than going through
// Book.ReverseTransaction, and the reason is worth recording. ReverseTransactionTx
// allocates entry and transaction IDs before it marks anything, and NextID's
// INSERT … ON CONFLICT DO UPDATE takes a row lock on id_sequences — so two
// reversals through the Book serialize on the counter long before they reach
// MarkReversed, and a read-compare-write version passes that test. It was
// checked: the mutation survived. What the conditional UPDATE actually protects
// is the store primitive, which storetest calls with no ID allocation at all, so
// that is what this test calls too.
func TestConcurrentMarkReversedOnlyOneWins(t *testing.T) {
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
}

// LockAccounts sorts by id, so two postings over the same pair of accounts take
// the locks in the same order and one simply waits.
//
// Note what this test does and does not prove: Update retries a deadlock
// (SQLSTATE 40P01) up to a bounded number of times, so an UNORDERED lock would
// still end up green here — it would just get there by aborting and retrying
// rather than by waiting. The ordering is what makes the wait the normal case
// instead of the exception; the assertion is a regression guard on both.
func TestOverlappingAccountSetsDoNotDeadlock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	book := ledger.NewBook(s, "bank", frozen)

	cash, equity := fundedChart(t, book, 10_000)

	// The two directions name the same accounts in opposite order, which is the
	// classic deadlock shape.
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
	balance, err := book.BookBalance(ctx, cash)
	assertNoError(t, err)
	assertEqual(t, "cash balance after both postings", balance, ledger.Amount(9_800))
}

// The fourth race, and the one the schema deliberately does NOT solve with a
// constraint: the central bank's chart of accounts is resolved by find-or-create
// *by name*, so two concurrent AddParticipant calls could each observe "no
// Central Bank ledger" and each create one — after which the two participants
// would disagree about which subledger holds member reserves.
//
// It cannot happen, because AddParticipantTx's first statement is
// NextID(NetworkBook, "bank"), whose INSERT … ON CONFLICT DO UPDATE takes a row
// lock on id_sequences. The second call blocks there until the first commits and
// then sees everything it wrote. The gap-free counter serializes the whole
// operation, not merely the number it hands out.
//
// This test is what makes that argument checkable rather than a claim in a
// comment. It fails loudly if the ordering inside AddParticipantTx ever changes.
func TestConcurrentAddParticipantsAgreeOnOneCentralBank(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	net := payment.NewNetwork(s.Payment(), frozen)

	names := []string{"Aurora Bank", "Banca Verde", "Nordkredit"}
	errs := runConcurrently(len(names), func(i int) error {
		_, err := net.AddParticipant(ctx, names[i], "BANKDEFFXXX", nil)
		return err
	})
	for _, err := range errs {
		assertNoError(t, err)
	}

	cb := net.CentralBank()
	ledgers, err := cb.ListLedgers(ctx)
	assertNoError(t, err)
	assertEqual(t, "central bank ledgers", len(ledgers), 1)

	subledgers, err := cb.ListSubledgers(ctx, ledgers[0].ID)
	assertNoError(t, err)
	assertEqual(t, "central bank subledgers", len(subledgers), 2)

	// Every participant's reserve account must sit in the SAME subledger. This
	// is the assertion that would fail on a divergence: with two "Member
	// Reserves" subledgers each bank's reserves would be real, and invisible to
	// the other's settlement.
	participants, err := net.ListParticipants(ctx)
	assertNoError(t, err)
	assertEqual(t, "participants", len(participants), len(names))

	var reserves ledger.SubledgerID
	for _, p := range participants {
		acct, err := cb.GetAccount(ctx, p.Assets["EUR"].Settlement)
		assertNoError(t, err)
		if reserves == "" {
			reserves = acct.SubledgerID
			continue
		}
		assertEqual(t, "reserve subledger for "+p.Name, acct.SubledgerID, reserves)
	}
}

// The fifth race, and it was live: a client reference deduplicated by a read
// that was not atomic.
//
// SubmitPaymentTx refuses an EndToEndID it has already seen, and store/pg
// declines to enforce that with a UNIQUE index on conformance grounds — mem's
// PutPayment does not look at any other row, so an index that refused would be
// the two stores disagreeing (see index 6 in schema/0001_init.sql). That leaves
// the application check, and under READ COMMITTED two submissions both read
// "not there" and both wrote. Eight concurrent submissions of one reference
// were accepted EIGHT times here and once on store/mem, and the payer was
// debited eight times for one instruction — the exact disagreement the index
// comment set out to prevent.
//
// It cannot happen now, because SubmitPaymentTx's first store statement is
// NextID(NetworkBook, "pay"), whose INSERT … ON CONFLICT DO UPDATE takes a row
// lock on id_sequences. Every other caller blocks there and then sees what the
// winner committed. It is AddParticipantTx's argument, one operation over, and
// this test is what makes it checkable rather than a claim in a comment: it
// fails loudly if the two statements are ever swapped back.
//
// storetest's ConcurrentReadThenWriteOnOneKeyAgrees is the generic half — it
// holds BOTH stores to one answer for this shape, which a pg-only test cannot.
// This one is about SubmitPaymentTx's own ordering, which storetest cannot see.
func TestConcurrentSubmissionsOfOneReferenceAcceptOne(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	net := payment.NewNetwork(s.Payment(), frozen)

	debtorBank, err := net.AddParticipant(ctx, "Aurora Bank", "AURODEFFXXX", []ledger.AssetCode{"EUR"})
	assertNoError(t, err)
	creditorBank, err := net.AddParticipant(ctx, "Banca Verde", "VERDITMMXXX", []ledger.AssetCode{"EUR"})
	assertNoError(t, err)

	alice, err := debtorBank.Deposit.OpenAccount(ctx, debtorBank.CustomerSubledger, "Alice", "EUR", debtorBank.ProductID, 0,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-DUP-ALICE-0001"})
	assertNoError(t, err)
	bruno, err := creditorBank.Deposit.OpenAccount(ctx, creditorBank.CustomerSubledger, "Bruno", "EUR", creditorBank.ProductID, 0,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-DUP-BRUNO-0001"})
	assertNoError(t, err)

	const opening ledger.Amount = 1_000_000
	const amount ledger.Amount = 1_000
	assertNoError(t, net.Deposit(ctx, debtorBank.ID, alice.ID, opening, "opening"))

	req := payment.InitiatePaymentRequest{
		Scheme:     payment.SchemeSEPACT,
		Debtor:     payment.PartyRef{Participant: debtorBank.ID, Account: alice.ID, Identifier: alice.Identifiers[0]},
		Creditor:   payment.PartyRef{Participant: creditorBank.ID, Account: bruno.ID, Identifier: bruno.Identifiers[0]},
		Amount:     amount,
		EndToEndID: "one-and-only",
	}

	errs := runConcurrently(8, func(int) error {
		_, err := net.SubmitPayment(ctx, req)
		return err
	})
	assertOneWinner(t, "SubmitPayment", errs, payment.ErrDuplicateEndToEndID)

	// The count of winners is not the assertion that matters. This is: the
	// payer was debited once, which is what a duplicate reference is for.
	balance, err := debtorBank.Deposit.GetBalance(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "Alice's balance after eight submissions of one reference", balance.Book, opening-amount)

	payments, err := net.ListPayments(ctx)
	assertNoError(t, err)
	assertEqual(t, "payment rows", len(payments), 1)
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
//
// The barrier is not decoration. Without it these tests pass against a
// deliberately broken store: opening a Postgres connection takes milliseconds,
// so the goroutine that gets the pool's warm connection finishes its whole
// transaction while the other is still shaking hands, and the two never
// interleave at all. That was measured, not assumed — a version of
// TestConcurrentPostingsCannotBothSpendTheSameBalance without this barrier
// passed eight runs out of eight with LockAccounts stubbed out to a no-op.
func raceInTransactions(t *testing.T, s *pg.Store, n int, fn func(context.Context, ledger.Tx, int) error) []error {
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
	var start sync.WaitGroup
	var done sync.WaitGroup
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

func assertEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}
