// The races: what the conformance suites cannot see.
//
// Every case in storetest.go, deposit.go, payment.go, product.go and lending.go
// runs one unit of work at a time. That is the right shape for a storage
// contract — what a store returns for a given call does not depend on a clock —
// but it means those suites are blind to the whole class of defect where two
// units of work are legal on their own and wrong together. Three money defects
// of exactly that class shipped into a branch and were found by hand, one probe
// each, which is why this file exists.
//
// # Four entry points, and the split is a property of what each race NEEDS
//
// Three of them race whole units of work against each other. Any store can run
// them, because each racer's transaction opens, runs and commits without waiting
// on another. They are three rather than one because a race belongs to whichever
// institution's database it happens in, and after Task 18d that is a real
// division rather than a way of reading the code:
//
//   - RunClearingHouseRaces and RunCentralBankRaces each take ONE institution's
//     store, because every statement in them is that institution's. They are
//     what "a store race" now means.
//   - RunSystemRaces takes the whole SET, because its two cases drive an
//     admission and a submission — acts that touch three databases and could not
//     be expressed against one. Nothing in it names a table or a dialect; what it
//     needs is N+2 stores that cannot see each other, which is the domain's
//     payment.Stores and not an implementation.
//
// RunConcurrentTxRaces needs several units of work OPEN at the same instant: it
// holds every racer at a barrier after BEGIN and before its first statement, so
// that the interleaving under test is the one that actually happens. A store
// that admits one unit of work at a time cannot reach the barrier with more than
// one racer, so it does not fail this suite — it stops, with no output. That is
// why the choice is a call the implementation's own test file makes, and not a
// skip inside the suite: a skip prints PASS for a suite that never ran.
//
// # What a passing race is worth
//
// A race that cannot fail on the store underneath it is not a defect in the
// test, and it is not evidence either. Every case below was established by
// breaking the code and running it rather than by reasoning about a lock, and
// each records what that gave — including, now, that the ordering guards two of
// them were written for no longer decide the outcome on store/sqlite, which
// admits one writer and re-runs a loser through Store.Update. That is recorded
// where the orderings are (payment.admissionSequenceTx, SubmitPaymentTx) and it
// is the reason these are regression guards rather than probes rather than a
// reason to delete them: the counter every one of these orderings allocates from
// is ledger.NetworkBook's, and Task 18 removes that book. Where its counter goes
// decides whether the ordering still spans the read it protects, and that is
// Task 18's question rather than one this file answers.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
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
//
// newStores must return a set with no state in any of its databases; the suite
// calls it once per subtest.
//
// These two used to sit beside the other two in one RunRaces, and the split is
// Task 18d's doing rather than a tidy-up. An admission is four units of work at
// three institutions and a submission reaches the payer's bank and the clearing
// house, so neither has ONE store to be a race in. What they still are is a
// store question — whether the orderings the domain arranged survive the store
// underneath — which is why they stay in this package and take an interface the
// domain declares.
func RunSystemRaces(t *testing.T, newStores func(*testing.T) payment.Stores) {
	t.Helper()

	// The central bank's chart of accounts is resolved by find-or-create *by
	// name*, and the schema deliberately does not close that with a constraint.
	// Two concurrent admissions could each observe "no Central Bank ledger" and
	// each create one, after which the two members would disagree about which
	// subledger holds reserves.
	//
	// What serializes it is each act's OWN first statement, and the name this
	// case used to carry hid that. It was TestConcurrentAddParticipantsAgreeOn-
	// OneCentralBank, and it argued that one transaction's first statement —
	// NextID(NetworkBook, "bank"), the bank's own act — locked a row on
	// id_sequences that held until that transaction ended, serializing every
	// later statement of the admission with it.
	//
	// An admission is not one transaction any more. It is four, at three
	// institutions, with messages between them, and Admit drives four separate
	// Updates per bank. So there is no single first statement, and what reaches
	// the find-or-create is payment.OpenSettlementAccountTx on its own — which
	// draws its own id first, through payment.admissionSequenceTx, for exactly
	// this reason. That function records what the act does without it: 60 runs
	// in 60 building the central bank a second chart of accounts.
	//
	// This case is what made the argument checkable rather than a claim in a
	// comment, and store/sqlite/schema/0001_init.sql's note on the absent UNIQUE
	// constraint points at it.
	//
	// What it is worth per store, each measured with admissionSequenceTx
	// returning nil. On store/pg one run passed and ten runs failed, on "central
	// bank ledgers: got 2, want 1" — so even there it was a regression guard
	// rather than a probe, and not the instrument to reach for when deliberately
	// measuring this race. On store/mem the same break passed five runs out of
	// five. On store/sqlite it passes ten runs out of ten, on the ephemeral store
	// and on a WAL file alike: the second creator is refused at its write and
	// Store.Update re-runs it, so it finds the ledger the first committed.
	//
	// So on the store this suite runs against today, this case cannot fail for
	// the reason it was written. It stays as a regression guard: the ordering it
	// pins is drawn from ledger.NetworkBook's counter, Task 18 removes that book,
	// and this case is what will notice if the replacement no longer orders the
	// find-or-create.
	t.Run("ConcurrentAdmissionsAgreeOnOneCentralBank", func(t *testing.T) {
		ctx := context.Background()
		stores := newStores(t)
		nets := payment.NewNetworks(stores, frozen)

		// One address each. The central bank keys its own member record by BIC,
		// so three banks on one BIC would be one member holding one reserve
		// account, and the per-participant loop below would compare that account
		// with itself.
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

		// Every member's reserve account must sit in the SAME subledger. This is
		// the assertion that would fail on a divergence: with two "Member
		// Reserves" subledgers each bank's reserves would be real, and invisible
		// to the other's settlement.
		//
		// The account numbers come off the CENTRAL BANK's own member rows, which
		// is the institution that opened them. They used to come off each bank's
		// row, read through a ListBanks at the clearing house — a crossing twice
		// over, and one the clearing house's schema no longer has a table for.
		var members []payment.SettlementMember
		assertNoError(t, stores.CentralBank().View(ctx, func(ctx context.Context, tx payment.Tx) error {
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

	// A client reference deduplicated by a read that was not atomic. This one
	// was live.
	//
	// SubmitPaymentTx refuses an EndToEndID it has already seen, and the schema
	// declines to enforce that with a UNIQUE index — it would answer a
	// constraint violation where the domain answers its sentinel, and it would be
	// the second unique index in a schema whose duplicate-key mapping cannot
	// survive one (see index 6 in store/sqlite/schema/0001_init.sql). That leaves
	// the application check, and under READ COMMITTED two submissions both read
	// "not there" and both wrote. Eight concurrent submissions of one reference
	// were accepted EIGHT times on store/pg and once on store/mem, and the payer
	// was debited eight times for one instruction.
	//
	// The fix was an ordering: SubmitPaymentTx's first store statement is
	// NextID(NetworkBook, "pay"), and writing the counter row makes that
	// transaction the database's writer, so every other caller waits there and
	// then sees what the winner committed. It is the admission acts' argument,
	// one operation over — see payment.admissionSequenceTx.
	//
	// Measured by moving the NextID call behind the duplicate check: store/pg
	// accepted 8 of 8 on the first run; store/mem passed five runs out of five;
	// store/sqlite passes ten runs out of ten, on the ephemeral store and on a
	// WAL file. So this case no longer fails loudly if the two statements are
	// swapped back, and a reader must not take a green run as evidence that the
	// ordering is still there.
	//
	// ConcurrentReadThenWriteOnOneKeyAgrees in storetest.go is the same shape
	// with no domain in it, written when this package could not name
	// payment.Network. It can now, so what that case is worth is the shape
	// stated once, at the store interface, for a rule the acts have not made
	// yet.
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

		alice, err := debtorBank.Deposit.OpenAccount(ctx, debtorBank.CustomerSubledger, "Alice", "EUR", debtorBank.ProductID, 0,
			deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-DUP-ALICE-0001"})
		assertNoError(t, err)
		bruno, err := creditorBank.Deposit.OpenAccount(ctx, creditorBank.CustomerSubledger, "Bruno", "EUR", creditorBank.ProductID, 0,
			deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-DUP-BRUNO-0001"})
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
		// It was the clearing house's while there was one store and one payment
		// row; there are three databases now and a submission has not reached the
		// clearing house — that takes a pacs.008, and this case sends none.
		payments, err := debtorNet.ListPayments(ctx)
		assertNoError(t, err)
		assertEqual(t, "payment rows", len(payments), 1)
	})
}

// RunClearingHouseRaces runs the races that happen entirely in the CLEARING
// HOUSE's database.
//
// newStore must return that institution's store with no state in it; the suite
// calls it once per subtest. It takes a payment.Store rather than the wider
// Store because the clearing house has no book of accounts to reach — its schema
// creates no ledger tables at all — so a wider handle would be a promise this
// institution cannot keep.
func RunClearingHouseRaces(t *testing.T, newStore func(*testing.T) payment.Store) {
	t.Helper()

	// Two admissions of one address, and they were live in the commit that split
	// admission into acts.
	//
	// The shape is the one above — a read the domain decides from, made binding
	// by an id allocated in front of it — and not the shape of the primitive
	// races in RunConcurrentTxRaces, which are closed by a row lock, a partial
	// unique index and a conditional UPDATE respectively.
	//
	// Every admission before the split went through AddParticipantTx, which
	// allocates the bank's id before anything else, so the rows admission writes
	// were decided under a lock nobody had to think about. The acts are
	// separately callable — an institution doing its own unit of work is the
	// whole point of them — and each of them reads one key and then writes it.
	// Measured before payment.admissionSequenceTx existed, over sixty runs of
	// this case and the next: 50 of 60 admitted two different admissions to one
	// address, and 60 of 60 lost one of two settlement accounts. Both were 0 of
	// 60 on store/mem.
	//
	// Measured, with admissionSequenceTx returning nil: store/pg accepted 8 of 8
	// on the first run, with seven applicants told they were admitted while the
	// roster routed to an eighth; store/mem passed five runs out of five;
	// store/sqlite passes ten runs out of ten, on the ephemeral store and on a
	// WAL file. The asymmetry between the first two was the conformance claim
	// while both existed. What it is now is a regression guard on an ordering
	// this store does not need — see the note at the top of this file.
	t.Run("ConcurrentAdmissionsOfOneBICAdmitOne", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		// One institution, one store, so NewNetwork rather than the factory: a
		// Networks over a single store is not a thing that can exist any more,
		// and this case never needed a second institution.
		csm := payment.NewNetwork(s, frozen, payment.AsClearingHouse())

		// Several applicants, one address, a DIFFERENT admission reference each
		// — which is the only case AdmitMemberTx refuses, and the case that must
		// not depend on which store is underneath.
		//
		// # What makes this guard sensitive is the CONNECTION POOL
		//
		// Two explanations were written here before either was measured
		// properly, and the second correction is the one worth keeping.
		//
		// The width was going to be eight "because two racers can win by luck".
		// False: with the sequence deleted, two racers detected it 55 of 60 and
		// eight detected it 53 of 60. Widening does nothing, because the race
		// turns on whether the second transaction reaches the read before the
		// first commits, and more contenders do not make that likelier.
		//
		// Rounds were credited next. They do work — five rounds detect 60 of 60
		// — but not by re-rolling one unreliable die. Detection PER ROUND, sixty
		// runs: round 1, 41 of 60; rounds 2, 3, 4 and 5, sixty of sixty each.
		// The first round is unreliable because a connection pool holds ONE idle
		// connection when a store has just been built, so the first racer takes
		// it and commits while the second is still opening a socket. By round 2
		// the pool holds two and the racers are genuinely concurrent. Warm the
		// pool by hand and ONE round detects 60 of 60.
		//
		// The rule that generalises is: never measure a store race on a cold
		// pool. The rounds stay because they warm it without this case having to
		// know how a pool grows, and because five rounds of eight cost about a
		// tenth of a second. The width stays at eight as a preference — it
		// models an operator console hitting one address — and not as
		// sensitivity, which it does not provide.
		//
		// With the sequence in place: 0 of 60, on store/pg and on store/mem.
		for _, bic := range []iso20022.BIC{"AURODEFFXXX", "VERDITMMXXX", "NORDSESSXXX", "SOLEFRPPXXX", "BANKESMMXXX"} {
			refs := make([]string, 8)
			for i := range refs {
				refs[i] = fmt.Sprintf("adm-%s-%d", bic[:4], i)
			}
			errs := runConcurrently(len(refs), func(i int) error {
				ack := payment.AdmissionAcknowledgement{
					BIC: bic,
					Ref: refs[i],
					Accounts: map[ledger.AssetCode]ledger.AccountID{
						"EUR": ledger.AccountID(fmt.Sprintf("200.100.00%d", i)),
					},
				}
				return s.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
					_, err := csm.AdmitMemberTx(ctx, tx, ack)
					return err
				})
			})

			// The roster must route to the admission it accepted, and to no
			// other.
			//
			// Asserted per applicant, and BEFORE the winner count below, which
			// is fatal. Counting the rows cannot do this job and a previous
			// version of this case tried: the roster is keyed by BIC, so it
			// holds exactly one row whether one of these eight writers won or
			// all eight did — measured at 0 of 60 runs with a row count other
			// than one, on a store with no sequence at all. What a lost race
			// produces is an applicant told it was admitted while the entry
			// names somebody else's admission, and that is what this reads.
			var entry payment.RosterEntry
			assertNoError(t, s.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
//
// newStore must return that institution's store with no state in it; the suite
// calls it once per subtest. See RunClearingHouseRaces on why the argument is a
// payment.Store — here the reason is the other way round: the settlement agent
// HAS a book, and the case below reaches it through payment.Tx, which embeds
// ledger.Tx.
func RunCentralBankRaces(t *testing.T, newStore func(*testing.T) payment.Store) {
	t.Helper()

	// The lost update the same ordering closes, on the settlement agent's own
	// row.
	//
	// One member asks for two assets at once. Both requests read the member row,
	// both write a map holding only their own account, and the account the loser
	// opened is left in the central bank's book with nothing pointing at it — a
	// reserve account the settlement agent cannot resolve, in its own ledger.
	//
	// Measured, with admissionSequenceTx returning nil: store/pg recorded one of
	// the two accounts on the first run; store/mem passed five runs out of five;
	// store/sqlite passes ten runs out of ten, on the ephemeral store and on a
	// WAL file.
	t.Run("ConcurrentSettlementAccountOpeningsKeepEveryAsset", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()
		cb := payment.NewNetwork(s, frozen, payment.AsCentralBank())

		assets := []ledger.AssetCode{"EUR", "USD"}
		errs := runConcurrently(len(assets), func(i int) error {
			return s.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
				_, err := cb.OpenSettlementAccountTx(ctx, tx, payment.AdmissionRequest{
					Name: "Aurora Bank", BIC: "AURODEFFXXX", Asset: assets[i], Ref: "adm-aurora",
				})
				return err
			})
		})
		for _, err := range errs {
			assertNoError(t, err)
		}

		// Both requests are legitimate and both must survive: one acmt.007 names
		// one currency, so this is how a two-currency bank is admitted.
		assertNoError(t, s.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
		assertNoError(t, s.View(ctx, func(ctx context.Context, tx payment.Tx) error {
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
//
// The precondition a store must meet to be handed to this is exactly that: two
// calls to Update can be inside their callbacks at the same time. A store that
// admits one at a time does not fail these cases, it stops in them — the barrier
// below waits for racers that cannot arrive — so the decision is the
// implementation's test file's to make, in code, where it is visible.
//
// Each case here is a store primitive rather than an act, which is the other
// difference from RunRaces: what they pin is a lock, a partial unique index and
// a conditional UPDATE, not an ordering the domain chose.
//
// Which of them bite on ONE run, measured on store/pg at Task 17.0 by breaking
// what each guards: the balance check and both idempotency cases failed on the
// first run; the reversal passed one run and failed within ten. The deadlock
// case was not broken to see it fail, and says so about itself. Those numbers
// are store/pg's; what the ephemeral store can and cannot show is recorded on
// the cases themselves and, for the balance check, in
// store/sqlite.LockAccounts.
func RunConcurrentTxRaces(t *testing.T, newStore func(*testing.T) Store) {
	t.Helper()

	// A balance check followed by a posting.
	//
	// Two withdrawals of 600 against an asset account holding 1000. Without
	// LockAccounts both transactions read 1000 under READ COMMITTED, both
	// conclude there are enough funds, and both post — leaving the account at
	// -200, which the ledger's own rules say is impossible.
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

		balance, err := book.BookBalance(ctx, cash)
		assertNoError(t, err)
		assertEqual(t, "cash balance after the race", balance, ledger.Amount(400))
	})

	// An idempotency key.
	//
	// ledger.Book checks the key before posting, and that check is exactly the
	// window a duplicate slips through: both transactions look, both find
	// nothing, both post. The partial unique index closes it, because there the
	// check and the write are the same statement.
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

	// The same key, at the store primitive. The case above drives the race
	// through Book.PostTransactionTx, which is the realistic path but NOT an
	// isolating one: PostTransactionTx allocates entry and transaction IDs
	// before it calls PutTransaction, and NextID's INSERT … ON CONFLICT DO
	// UPDATE takes a row lock on id_sequences. The two racers therefore
	// serialize on the counter, and the loser's pre-check runs after the winner
	// has committed and duly finds the row — so a check-then-insert
	// PutTransaction passes it. That was measured, not assumed.
	//
	// This one calls tx.PutTransaction directly with two pre-chosen IDs and one
	// shared key, so nothing allocates an ID and nothing serializes the racers
	// before they reach the index. It is the same isolation
	// ConcurrentMarkReversedOnlyOneWins needed, for the same reason.
	//
	// It kills both dangerous shapes: a check-then-insert that drops the
	// SQLSTATE mapping (the loser surfaces a raw 23505, a 500 where the API owes
	// a 409), and a check-then-insert with the unique index removed (both racers
	// win).
	t.Run("ConcurrentPutTransactionWithOneIdempotencyKey", func(t *testing.T) {
		s := newStore(t)
		ctx := context.Background()

		// Commit something into the book FIRST. Without this the racers
		// serialize before they ever reach the index: the first write to a book
		// inserts its books row, and a concurrent INSERT … ON CONFLICT DO
		// NOTHING blocks on that uncommitted tuple until the first transaction
		// commits. Measured — with this line removed, a check-then-insert
		// PutTransaction passes five runs out of five. Every serialization point
		// before the statement under test has to be paid off before the race is
		// a race.
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
	//
	// Posted -> Reversed happens at most once. A read-compare-write would let
	// two concurrent reversals both see Posted and both mark it, and the ledger
	// would then hold two counter-transactions for one original.
	//
	// This one drives tx.MarkReversed directly rather than going through
	// Book.ReverseTransaction, and the reason is worth recording.
	// ReverseTransactionTx allocates entry and transaction IDs before it marks
	// anything, and writing the counter row makes that transaction the database's
	// writer — so two reversals through the Book serialize on the counter long
	// before they reach MarkReversed, and a read-compare-write version passes
	// that test. It was checked: the mutation survived. What the conditional
	// UPDATE actually protects is the store primitive, which the shared suite
	// calls with no ID allocation at all, so that is what this calls too.
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
	//
	// Note what this case does and does not prove: Update retries a lock loss up
	// to a bounded number of times (SQLSTATE 40P01 on store/pg, SQLITE_BUSY and
	// SQLITE_LOCKED on store/sqlite), so an UNORDERED lock would still end up
	// green here — it would just get there by aborting and retrying rather than
	// by waiting. The ordering is what makes the wait the normal case instead of
	// the exception; the assertion is a regression guard on both. On
	// store/sqlite there is no lock to order at all — LockAccounts is a
	// documented no-op there, and its doc records what a locking version bought.
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
		balance, err := book.BookBalance(ctx, cash)
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
//
// The barrier is not decoration, and the thing it pays off is the CONNECTION
// POOL. Opening a connection takes milliseconds, so the goroutine that gets the
// pool's one warm connection finishes its whole transaction while the other is
// still shaking hands, and the two never interleave at all. That was measured,
// not assumed — a version of ConcurrentPostingsCannotBothSpendTheSameBalance
// without this barrier passed eight runs out of eight with LockAccounts stubbed
// out to a no-op.
//
// It is also what makes this suite unrunnable on a store that admits one unit of
// work at a time: the second racer never reaches arrive, so the first waits for
// it for ever. See RunConcurrentTxRaces.
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
