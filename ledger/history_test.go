package ledger_test

import (
	"context"
	"testing"
	"time"

	. "github.com/raphi011/cbs/ledger"
)

// ---------------------------------------------------------------------------
// One account's history
// ---------------------------------------------------------------------------

// day is a booking date n days before the test clock, so that an age asserted
// below is a number a reader can check against the fixture rather than against
// an arithmetic they have to redo.
func day(n int) time.Time { return testClock().AddDate(0, 0, -n) }

// post books one two-legged transaction on a given day and returns it.
func post(t *testing.T, book *Book, on time.Time, desc string, md map[string]string, entries ...Entry) Transaction {
	t.Helper()
	tr, err := book.PostTransaction(context.Background(), PostTransactionRequest{
		BookingDate: on,
		Description: desc,
		Metadata:    md,
		Entries:     entries,
	})
	assertNoError(t, err)
	return tr
}

// TestAccountHistoryRunsInBookOrderAndEndsAtTheBookBalance is the control the
// rest of this file rests on. If the running balance did not arrive at the same
// figure BookBalance does, every check built on a running balance would be
// checking a statement against a number the book does not hold.
func TestAccountHistoryRunsInBookOrderAndEndsAtTheBookBalance(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	post(t, book, day(3), "paid in", nil,
		Entry{AccountID: cash.ID, Amount: 1000, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 1000, Direction: Credit})
	post(t, book, day(2), "withdrew", nil,
		Entry{AccountID: alice.ID, Amount: 400, Direction: Debit},
		Entry{AccountID: cash.ID, Amount: 400, Direction: Credit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)

	// Alice is a Liability, so a credit RAISES her. A history that signed by the
	// entry's direction rather than by the account's normal balance would report
	// this account running backwards and would still sum to something.
	assertEqual(t, "normal direction", hist.Normal, Credit)
	assertEqual(t, "rows", len(hist.Rows), 2)
	assertEqual(t, "first movement", hist.Rows[0].Movement, 1000)
	assertEqual(t, "first running", hist.Rows[0].Running, 1000)
	assertEqual(t, "second movement", hist.Rows[1].Movement, -400)
	assertEqual(t, "second running", hist.Rows[1].Running, 600)

	balance, err := book.BookBalance(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "closing against BookBalance", hist.Closing, balance)
}

// TestAccountHistoryCountsAReversalsOwnEntries pins the rule this inherits from
// BookBalance rather than re-deciding it.
//
// A reversal does not delete anything: it posts mirrored entries of its own, and
// those are what cancel the original. So a history over a reversed transaction
// has TWO rows and nets to zero — and a history that filtered Reversed out would
// have one row and a closing balance the book does not hold, which is the one
// case an auditor is reading the history for.
func TestAccountHistoryCountsAReversalsOwnEntries(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	original := post(t, book, day(1), "paid in", nil,
		Entry{AccountID: cash.ID, Amount: 1000, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 1000, Direction: Credit})
	_, err := book.ReverseTransaction(ctx, original.ID, "posted in error")
	assertNoError(t, err)

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "rows", len(hist.Rows), 2)
	assertEqual(t, "closing", hist.Closing, 0)
	assertEqual(t, "the reversal's own running balance", hist.Rows[1].Running, 0)
}

// TestAccountHistoryNetsATransactionsEntriesOnOneAccount fixes the shape rather
// than leaving it to be discovered.
//
// Nothing in this system posts two entries on one account in one transaction,
// but the ledger permits it, and a reader that had to sum two rows to learn what
// one event did would be reading the wrong shape — a row is a transaction.
func TestAccountHistoryNetsATransactionsEntriesOnOneAccount(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	post(t, book, day(1), "two legs, one account", nil,
		Entry{AccountID: cash.ID, Amount: 1000, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 1500, Direction: Credit},
		Entry{AccountID: alice.ID, Amount: 500, Direction: Debit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "rows", len(hist.Rows), 1)
	assertEqual(t, "netted movement", hist.Rows[0].Movement, 1000)
}

// TestAccountHistoryRefusesAnAccountThatIsNotThere asserts the choice rather
// than the alternative: an empty history would answer "no movements" to a
// caller whose account id is wrong, and that is a bug reported as a fact.
func TestAccountHistoryRefusesAnAccountThatIsNotThere(t *testing.T) {
	_, err := testBook(t).AccountHistory(context.Background(), AccountID("nope"))
	assertError(t, err, ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Ageing
// ---------------------------------------------------------------------------

// TestAgeingDecomposesABalanceIntoDatedLots is the shape of every ageing report
// built on this: the balance is one number and the lots are what it is made of.
func TestAgeingDecomposesABalanceIntoDatedLots(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	post(t, book, day(10), "old money", nil,
		Entry{AccountID: cash.ID, Amount: 300, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 300, Direction: Credit})
	post(t, book, day(2), "new money", nil,
		Entry{AccountID: cash.ID, Amount: 700, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 700, Direction: Credit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)
	ageing := hist.AgeAt(testClock())

	assertEqual(t, "balance", ageing.Balance, 1000)
	assertEqual(t, "lots", len(ageing.Lots), 2)
	assertEqual(t, "oldest lot amount", ageing.Lots[0].Amount, 300)
	assertEqual(t, "oldest lot age", ageing.Lots[0].Days, 10)
	assertEqual(t, "newest lot age", ageing.Lots[1].Days, 2)

	oldest, any := ageing.Oldest()
	assertEqual(t, "there is something to age", any, true)
	assertEqual(t, "oldest", oldest, 10)
	assertEqual(t, "older than a week", len(ageing.OlderThan(7)), 1)
}

// TestANettedMovementDischargesTheOldestLotsFirst is the rule that makes this
// usable on a clearing suspense at all.
//
// A member bank's settlement mirror leg covers a whole cut-off in ONE figure and
// names no payment — deliberately, since a member holds no cycle — so nothing
// can attribute it to the payments it discharged. FIFO attributes it by the only
// fact available, which is order. Here 500 arrives ten days ago, 500 two days
// ago, and a netted 600 discharges the first entirely and bites 100 out of the
// second: what is left is 400, and it is TWO days old and not ten.
//
// An implementation that aged the residue by the oldest posting still on the
// account would report a four-hundred-unit balance sitting for ten days and send
// somebody looking for a payment that settled a week ago.
func TestANettedMovementDischargesTheOldestLotsFirst(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	post(t, book, day(10), "first payment in", nil,
		Entry{AccountID: cash.ID, Amount: 500, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 500, Direction: Credit})
	post(t, book, day(2), "second payment in", nil,
		Entry{AccountID: cash.ID, Amount: 500, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 500, Direction: Credit})
	post(t, book, day(1), "netted settlement of both", nil,
		Entry{AccountID: alice.ID, Amount: 600, Direction: Debit},
		Entry{AccountID: cash.ID, Amount: 600, Direction: Credit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)
	ageing := hist.AgeAt(testClock())

	assertEqual(t, "balance", ageing.Balance, 400)
	assertEqual(t, "lots", len(ageing.Lots), 1)
	assertEqual(t, "what is left came in two days ago", ageing.Lots[0].Days, 2)
	assertEqual(t, "and it is what is left of the second", ageing.Lots[0].Amount, 400)
}

// TestAgeingCarriesTheMetadataOfTheLotItLeftBehind is what makes the report
// stronger on the accounts that can support it.
//
// A clearing suspense's residue is a FIFO lot and nothing more, because the leg
// that discharged it named no payment. An unclaimed-balances credit IS one
// payment's diverted creditor leg and carries its id, so the same walk answers
// "how old" and "which payment" on that account and only "how old" on the other.
// The difference is a fact about the postings, not about the report.
func TestAgeingCarriesTheMetadataOfTheLotItLeftBehind(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	post(t, book, day(5), "Unclaimed: transfer", map[string]string{"payment_id": "pay_7"},
		Entry{AccountID: cash.ID, Amount: 300, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 300, Direction: Credit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)
	lots := hist.AgeAt(testClock()).Lots

	assertEqual(t, "lots", len(lots), 1)
	assertEqual(t, "the payment that put it there", lots[0].Metadata["payment_id"], "pay_7")
	assertEqual(t, "and what it was for", lots[0].Description, "Unclaimed: transfer")
}

// TestAgeingASettledAccountHasNothingToAge is the other control. An account back
// at zero has no lots at all — not one lot of zero — because a decomposition of
// nothing is empty, and a report that printed a zero-aged line would put a
// finding in front of somebody for an account that is finished.
func TestAgeingASettledAccountHasNothingToAge(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	post(t, book, day(4), "in", nil,
		Entry{AccountID: cash.ID, Amount: 900, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 900, Direction: Credit})
	post(t, book, day(1), "out", nil,
		Entry{AccountID: alice.ID, Amount: 900, Direction: Debit},
		Entry{AccountID: cash.ID, Amount: 900, Direction: Credit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)
	ageing := hist.AgeAt(testClock())

	assertEqual(t, "balance", ageing.Balance, 0)
	assertEqual(t, "lots", len(ageing.Lots), 0)
	_, any := ageing.Oldest()
	assertEqual(t, "nothing to age", any, false)
}

// TestAgeingABalanceThatWentTheOtherWayKeepsItsLotsOneSigned is the invariant
// stated as a case: lots sum to the balance, including when the balance has run
// past zero and out the other side.
//
// A liability account debited beyond what it holds is an overdrawn customer, and
// this repository does not refuse one — checkSufficientBalance guards Asset and
// Expense accounts only. So the case is reachable, and a decomposition that left
// a spent lot standing beside the new negative one would report lots pointing
// both ways and sum to the right figure by cancellation, which is a
// decomposition of nothing.
func TestAgeingABalanceThatWentTheOtherWayKeepsItsLotsOneSigned(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, feeIncome := setupChartOfAccounts(t, book)

	post(t, book, day(6), "in", nil,
		Entry{AccountID: cash.ID, Amount: 200, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 200, Direction: Credit})
	// The contra is Fee Income and not Cash, because Cash is an Asset and the
	// ledger refuses one below zero. Overdrawing the CUSTOMER is what this test
	// is about, and it is a Liability, which the ledger permits.
	post(t, book, day(3), "charged past what she holds", nil,
		Entry{AccountID: alice.ID, Amount: 500, Direction: Debit},
		Entry{AccountID: feeIncome.ID, Amount: 500, Direction: Credit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)
	ageing := hist.AgeAt(testClock())

	assertEqual(t, "balance", ageing.Balance, -300)
	assertEqual(t, "lots", len(ageing.Lots), 1)
	assertEqual(t, "the overdrawn lot", ageing.Lots[0].Amount, -300)
	assertEqual(t, "dated by what overdrew it", ageing.Lots[0].Days, 3)

	var sum Amount
	for _, l := range ageing.Lots {
		sum += l.Amount
	}
	assertEqual(t, "lots sum to the balance", sum, ageing.Balance)
}

// TestAgeingAMovementThatExactlyClearsALotOpensNothing is the boundary the
// consuming loop is written around: equal magnitudes discharge the lot and leave
// no remainder, so nothing is appended. It is a separate case because zero is
// opposite in sign to everything, and a loop without that guard consumes the
// whole queue with a movement that had already been absorbed.
func TestAgeingAMovementThatExactlyClearsALotOpensNothing(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	post(t, book, day(8), "first in", nil,
		Entry{AccountID: cash.ID, Amount: 100, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 100, Direction: Credit})
	post(t, book, day(7), "second in", nil,
		Entry{AccountID: cash.ID, Amount: 250, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 250, Direction: Credit})
	post(t, book, day(5), "clears the first exactly", nil,
		Entry{AccountID: alice.ID, Amount: 100, Direction: Debit},
		Entry{AccountID: cash.ID, Amount: 100, Direction: Credit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)
	ageing := hist.AgeAt(testClock())

	assertEqual(t, "balance", ageing.Balance, 250)
	assertEqual(t, "lots", len(ageing.Lots), 1)
	assertEqual(t, "the survivor is the second", ageing.Lots[0].Days, 7)
}

// TestAgeAtSetsTheAgesAndDoesNotCutTheHistoryOff pins the parameter's meaning.
//
// asOf is when the question is asked, not what the balance is read as at. A
// caller wanting the latter wants a value-dated read (Book.SeriesTx), and an
// AgeAt that quietly truncated would answer a different question in the same
// shape — the most expensive kind of wrong.
func TestAgeAtSetsTheAgesAndDoesNotCutTheHistoryOff(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	post(t, book, day(2), "recent", nil,
		Entry{AccountID: cash.ID, Amount: 400, Direction: Debit},
		Entry{AccountID: alice.ID, Amount: 400, Direction: Credit})

	hist, err := book.AccountHistory(ctx, alice.ID)
	assertNoError(t, err)

	// Asked about a day BEFORE the posting: the balance is the book's, and the
	// age has not started running.
	early := hist.AgeAt(day(5))
	assertEqual(t, "balance is not truncated", early.Balance, 400)
	assertEqual(t, "lots", len(early.Lots), 1)
	assertEqual(t, "an age that has not started", early.Lots[0].Days, -3)

	later := hist.AgeAt(day(0).AddDate(0, 0, 30))
	assertEqual(t, "the same lot, thirty days on", later.Lots[0].Days, 32)
}
