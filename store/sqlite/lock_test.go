package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/store/sqlite"
)

// Two postings that cannot both spend the same balance, driven through
// ledger.Book on a FILE.
//
// storetest has this case and it runs on the ephemeral store, where it cannot
// fail for the reason it is about: on memdb a retry's read blocks until the
// winner commits, so the loser reaches the domain guard whatever the store does
// underneath — measured, ten runs out of ten with tx.LockAccounts emptied. Under
// WAL a reader runs past an uncommitted writer, so the loser can re-read a stale
// balance and pass the domain's check on it, and only converges once an attempt
// begins after the commit.
//
// So this is the same case on the configuration that can fail it, and it is the
// only test in the repository that drives the real posting path — GetAccount,
// LockAccounts, the balance check, NextID, PutTransaction — against a file. The
// synthetic pair in sqlite_test.go pins the retry and its budget on one table;
// this pins that the composition still lands on the domain's own refusal.
//
// The hold is what makes the winner keep its transaction open past the loser's
// first attempt. It is far shorter than the retry budget, deliberately: what is
// asserted here is that the loser is refused for the DOMAIN's reason, and the
// budget's own limit is TestTheRetryBudgetOutlastsASlowWriter's subject.
func TestConcurrentPostingsOnAFileReachTheDomainGuard(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "postings.db"), frozen)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	book := ledger.NewBook(s, "bank", frozen)
	cash, equity := fundedChart(t, book, 1_000)

	const hold = 50 * time.Millisecond
	const n = 2
	var opened sync.WaitGroup
	opened.Add(n)
	release := make(chan struct{})
	errs := make([]error, n)
	var done sync.WaitGroup

	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			first := true
			errs[i] = s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
				// Only the first attempt waits at the barrier. A retry must not,
				// or the retries wait for racers that have finished.
				if first {
					first = false
					opened.Done()
					<-release
				}
				_, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
					Description: "withdrawal",
					Entries: []ledger.Entry{
						{AccountID: equity, Amount: 600, Direction: ledger.Debit},
						{AccountID: cash, Amount: 600, Direction: ledger.Credit},
					},
				})
				if err != nil {
					return err
				}
				// The winner keeps the write lock, so the loser's first retry
				// reads a value that has not been committed yet.
				time.Sleep(hold)
				return nil
			})
		}()
	}
	opened.Wait()
	close(release)
	done.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ledger.ErrInsufficientBalance):
		default:
			t.Errorf("racer %d lost for the wrong reason: %v — the caller is owed the domain's refusal, not a lock error", i, err)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1", winners)
	}

	balance, err := book.BookBalance(ctx, cash)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if balance != 400 {
		t.Errorf("cash balance after the race = %d, want 400", balance)
	}
}

// fundedChart builds a minimal chart of accounts and funds the cash account,
// returning the Asset account under test and an Equity counterparty. Equity is
// not balance-checked, so it can absorb any leg the test needs. It is
// storetest's helper, which is unexported there and small enough to have here
// rather than to widen that package's surface for one caller.
func fundedChart(t *testing.T, book *ledger.Book, amount ledger.Amount) (cash, equity ledger.AccountID) {
	t.Helper()
	ctx := context.Background()

	gl, err := book.CreateLedger(ctx, "General Ledger")
	mustNot(t, err)
	assets, err := book.CreateSubledger(ctx, gl.ID, "Bank Assets")
	mustNot(t, err)
	capital, err := book.CreateSubledger(ctx, gl.ID, "Capital")
	mustNot(t, err)

	cashAcct, err := book.CreateAccount(ctx, assets.ID, "Cash", ledger.Asset, "EUR")
	mustNot(t, err)
	equityAcct, err := book.CreateAccount(ctx, capital.ID, "Share Capital", ledger.Equity, "EUR")
	mustNot(t, err)

	_, err = book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "opening capital",
		Entries: []ledger.Entry{
			{AccountID: cashAcct.ID, Amount: amount, Direction: ledger.Debit},
			{AccountID: equityAcct.ID, Amount: amount, Direction: ledger.Credit},
		},
	})
	mustNot(t, err)

	return cashAcct.ID, equityAcct.ID
}

func mustNot(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
