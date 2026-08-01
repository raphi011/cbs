package mesh

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/testenv"
)

// ---------------------------------------------------------------------------
// The book-access recorder
// ---------------------------------------------------------------------------

// recordingStore wraps a payment.Store and records which ledger books each unit
// of work touched.
//
// It is the second of this package's two boundary mechanisms, and the one that
// bites today. The interfaces in ops.go narrow by METHOD: a bank handler that
// calls SettleCycleTx does not compile. They cannot narrow by BOOK — a bank
// handler still could not be stopped from reading another bank's ledger through
// a method it legitimately holds, because a BookID is an ordinary argument and
// one is as valid as another. This notices that.
//
// It is test-only, and deliberately so. In production the boundary is the
// interfaces plus, from sub-project 8, one store per entity; this exists to
// assert the boundary holds while the store is still shared, which is precisely
// the window in which nothing else would notice a crossing.
type recordingStore struct {
	inner payment.Store

	mu    sync.Mutex
	books map[ledger.BookID]bool
}

var _ payment.Store = (*recordingStore)(nil)

func newRecordingStore(inner payment.Store) *recordingStore {
	return &recordingStore{inner: inner, books: map[ledger.BookID]bool{}}
}

func (s *recordingStore) Update(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return s.inner.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return fn(ctx, &recordingTx{Tx: tx, rec: s})
	})
}

func (s *recordingStore) View(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return s.inner.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		return fn(ctx, &recordingTx{Tx: tx, rec: s})
	})
}

func (s *recordingStore) Reset(ctx context.Context) error { return s.inner.Reset(ctx) }
func (s *recordingStore) Close() error                    { return s.inner.Close() }

// note records one book access. It is called from actor goroutines, so it takes
// the lock — the mesh runs one goroutine per institution and they touch the same
// store concurrently.
//
// What it records is NOT rolled back with the unit of work it was called in. A
// handler that read another bank's book and then failed still read it, and a
// recorder that forgot such a read on rollback would hide exactly the crossings
// that go wrong.
func (s *recordingStore) note(book ledger.BookID) {
	s.mu.Lock()
	s.books[book] = true
	s.mu.Unlock()
}

// touched is the set of books accessed since the last reset, sorted so that an
// assertion on it is a stable string rather than a map's iteration order.
func (s *recordingStore) touched() []ledger.BookID {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ledger.BookID, 0, len(s.books))
	for b := range s.books {
		out = append(out, b)
	}
	slices.Sort(out)
	return out
}

func (s *recordingStore) reset() {
	s.mu.Lock()
	s.books = map[ledger.BookID]bool{}
	s.mu.Unlock()
}

// everyBook is what an access records when its book is EMPTY.
//
// It exists for ListAudit. An AuditFilter with no BookID is not a read of no
// book, it is a read of every book: store/mem/tx.go skips the comparison
// entirely (`if f.BookID != "" && e.BookID != f.BookID`) and store/pg's
// tx_audit.go omits the WHERE clause (`if f.BookID != "" { add("book_id = $%d",
// …) }`). Recording nothing there would leave open the widest crossing in the
// system — one call that reads every bank's audit trail, payloads included.
//
// It is a BookID no actor owns, so any assertion on an actor's own books fails
// on it and names it, which is the point: an unfiltered audit read is not a
// clean unit of work and must not look like one.
const everyBook ledger.BookID = "(every book)"

// bookOf is the book a struct-carried access touches.
//
// A book that arrives inside a struct can be empty, which a positional argument
// in this repository never is. For a filter, empty means every book — see
// everyBook. For a write it means an event belonging to no book, which is not
// the same thing, and mapping both to everyBook therefore OVER-reports the
// write. That is the deliberate direction: a guard that over-reports fails
// loudly and gets looked at, while one that under-reports passes silently, which
// is the failure this whole file exists to prevent. Nothing in this repository
// appends an audit event without a book, so the over-report is unreachable in
// practice.
func bookOf(book ledger.BookID) ledger.BookID {
	if book == "" {
		return everyBook
	}
	return book
}

// recordingTx is one unit of work over a recordingStore: a payment.Tx that notes
// the book of every book-scoped call before delegating.
//
// "Every book-scoped call" means all four layers, not just the ledger. A
// payment.Tx embeds deposit.Tx, which embeds product.Tx, which embeds ledger.Tx,
// and lending.Tx beside them — and every one of those layers is book-scoped in
// the same way, because a book IS a bank. Wrapping only ledger.Tx would have
// left a handler free to read another bank's deposit register, its holds, its
// catalogue or its loan book without touching a single recorded method, and the
// TestXTouchesOnly… assertions built on this would have read stronger than they
// were.
//
// It also means both SHAPES of book argument. Most methods take `book BookID`
// second; AppendAudit and ListAudit carry theirs inside AuditEvent.BookID and
// AuditFilter.BookID. The second shape was missed once, on the false claim that
// the audit log is not per-book — see structCarriedBooks, which is where a
// method of that shape is now decided rather than overlooked.
//
// payment.Tx's OWN methods are network-scoped: participants, payments, mandates,
// cycles and settlements belong to no single bank and live under
// ledger.NetworkBook. They are correctly absent from the overrides below, and
// the consequence is worth being straight about — writing a Payment row records
// nothing, so what puts ledger.NetworkBook into touched() is the network
// postings a handler makes, not the row it writes.
//
// It EMBEDS payment.Tx, so everything else is promoted untouched.
// TestRecordingTxOverridesEveryBookScopedMethod is what keeps the override set
// exactly right: one that went missing would be silently replaced by the
// promoted method, which records nothing.
type recordingTx struct {
	payment.Tx
	rec *recordingStore
}

var _ payment.Tx = (*recordingTx)(nil)

// The overrides, grouped by the layer that declares them. Each is the same two
// statements — note the book, then call through — and
// TestEveryRecordingTxMethodNotesItsBookThenDelegates holds them to that shape,
// deriving the expected note expression from each method's own parsed signature.

// --- ledger.Tx ---

func (r *recordingTx) NextID(ctx context.Context, book ledger.BookID, prefix string) (string, error) {
	r.rec.note(book)
	return r.Tx.NextID(ctx, book, prefix)
}

func (r *recordingTx) NextSubledgerBlock(ctx context.Context, book ledger.BookID) (int, error) {
	r.rec.note(book)
	return r.Tx.NextSubledgerBlock(ctx, book)
}

func (r *recordingTx) NextAccountSeq(ctx context.Context, book ledger.BookID, typeBlock int, subledger ledger.SubledgerID) (int, error) {
	r.rec.note(book)
	return r.Tx.NextAccountSeq(ctx, book, typeBlock, subledger)
}

func (r *recordingTx) PutLedger(ctx context.Context, book ledger.BookID, l ledger.Ledger) error {
	r.rec.note(book)
	return r.Tx.PutLedger(ctx, book, l)
}

func (r *recordingTx) GetLedger(ctx context.Context, book ledger.BookID, id ledger.LedgerID) (ledger.Ledger, error) {
	r.rec.note(book)
	return r.Tx.GetLedger(ctx, book, id)
}

func (r *recordingTx) ListLedgers(ctx context.Context, book ledger.BookID) ([]ledger.Ledger, error) {
	r.rec.note(book)
	return r.Tx.ListLedgers(ctx, book)
}

func (r *recordingTx) PutSubledger(ctx context.Context, book ledger.BookID, sl ledger.Subledger) error {
	r.rec.note(book)
	return r.Tx.PutSubledger(ctx, book, sl)
}

func (r *recordingTx) GetSubledger(ctx context.Context, book ledger.BookID, id ledger.SubledgerID) (ledger.Subledger, error) {
	r.rec.note(book)
	return r.Tx.GetSubledger(ctx, book, id)
}

func (r *recordingTx) ListSubledgers(ctx context.Context, book ledger.BookID) ([]ledger.Subledger, error) {
	r.rec.note(book)
	return r.Tx.ListSubledgers(ctx, book)
}

func (r *recordingTx) PutAccount(ctx context.Context, book ledger.BookID, a ledger.Account) error {
	r.rec.note(book)
	return r.Tx.PutAccount(ctx, book, a)
}

func (r *recordingTx) GetAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) (ledger.Account, error) {
	r.rec.note(book)
	return r.Tx.GetAccount(ctx, book, id)
}

func (r *recordingTx) ListAccounts(ctx context.Context, book ledger.BookID) ([]ledger.Account, error) {
	r.rec.note(book)
	return r.Tx.ListAccounts(ctx, book)
}

func (r *recordingTx) LockAccounts(ctx context.Context, book ledger.BookID, ids []ledger.AccountID) error {
	r.rec.note(book)
	return r.Tx.LockAccounts(ctx, book, ids)
}

func (r *recordingTx) PutTransaction(ctx context.Context, book ledger.BookID, txn ledger.Transaction) error {
	r.rec.note(book)
	return r.Tx.PutTransaction(ctx, book, txn)
}

func (r *recordingTx) GetTransaction(ctx context.Context, book ledger.BookID, id ledger.TransactionID) (ledger.Transaction, error) {
	r.rec.note(book)
	return r.Tx.GetTransaction(ctx, book, id)
}

func (r *recordingTx) GetTransactionByIdempotencyKey(ctx context.Context, book ledger.BookID, key string) (ledger.Transaction, error) {
	r.rec.note(book)
	return r.Tx.GetTransactionByIdempotencyKey(ctx, book, key)
}

func (r *recordingTx) ListTransactions(ctx context.Context, book ledger.BookID) ([]ledger.Transaction, error) {
	r.rec.note(book)
	return r.Tx.ListTransactions(ctx, book)
}

func (r *recordingTx) ListTransactionsForAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) ([]ledger.Transaction, error) {
	r.rec.note(book)
	return r.Tx.ListTransactionsForAccount(ctx, book, id)
}

func (r *recordingTx) MarkReversed(ctx context.Context, book ledger.BookID, id ledger.TransactionID) error {
	r.rec.note(book)
	return r.Tx.MarkReversed(ctx, book, id)
}

func (r *recordingTx) BookBalance(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction) (ledger.Amount, error) {
	r.rec.note(book)
	return r.Tx.BookBalance(ctx, book, id, normal)
}

func (r *recordingTx) ValueDateBalance(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction, before time.Time) (ledger.Amount, error) {
	r.rec.note(book)
	return r.Tx.ValueDateBalance(ctx, book, id, normal, before)
}

func (r *recordingTx) ValueDatedSeries(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction, from, to time.Time) (ledger.Series, error) {
	r.rec.note(book)
	return r.Tx.ValueDatedSeries(ctx, book, id, normal, from, to)
}

// The two whose book travels inside the argument. See structCarriedBooks.

func (r *recordingTx) AppendAudit(ctx context.Context, e ledger.AuditEvent) error {
	r.rec.note(bookOf(e.BookID))
	return r.Tx.AppendAudit(ctx, e)
}

func (r *recordingTx) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	r.rec.note(bookOf(f.BookID))
	return r.Tx.ListAudit(ctx, f)
}

// --- product.Tx ---

func (r *recordingTx) PutProduct(ctx context.Context, book ledger.BookID, p product.Product) error {
	r.rec.note(book)
	return r.Tx.PutProduct(ctx, book, p)
}

func (r *recordingTx) GetProduct(ctx context.Context, book ledger.BookID, id product.ID) (product.Product, error) {
	r.rec.note(book)
	return r.Tx.GetProduct(ctx, book, id)
}

func (r *recordingTx) ListProducts(ctx context.Context, book ledger.BookID) ([]product.Product, error) {
	r.rec.note(book)
	return r.Tx.ListProducts(ctx, book)
}

func (r *recordingTx) PutProductVersion(ctx context.Context, book ledger.BookID, v product.Version) error {
	r.rec.note(book)
	return r.Tx.PutProductVersion(ctx, book, v)
}

func (r *recordingTx) ListProductVersions(ctx context.Context, book ledger.BookID, id product.ID) ([]product.Version, error) {
	r.rec.note(book)
	return r.Tx.ListProductVersions(ctx, book, id)
}

func (r *recordingTx) GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id product.ID, day time.Time) (product.Version, error) {
	r.rec.note(book)
	return r.Tx.GetProductVersionAsOf(ctx, book, id, day)
}

// --- deposit.Tx ---

func (r *recordingTx) PutDepositAccount(ctx context.Context, book ledger.BookID, a deposit.Account) error {
	r.rec.note(book)
	return r.Tx.PutDepositAccount(ctx, book, a)
}

func (r *recordingTx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	r.rec.note(book)
	return r.Tx.GetDepositAccount(ctx, book, id)
}

func (r *recordingTx) ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]deposit.Account, error) {
	r.rec.note(book)
	return r.Tx.ListDepositAccounts(ctx, book)
}

func (r *recordingTx) ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident deposit.Identifier) ([]deposit.Account, error) {
	r.rec.note(book)
	return r.Tx.ListDepositAccountsByIdentifier(ctx, book, ident)
}

func (r *recordingTx) PutHold(ctx context.Context, book ledger.BookID, h deposit.Hold) error {
	r.rec.note(book)
	return r.Tx.PutHold(ctx, book, h)
}

func (r *recordingTx) GetHold(ctx context.Context, book ledger.BookID, id deposit.HoldID) (deposit.Hold, error) {
	r.rec.note(book)
	return r.Tx.GetHold(ctx, book, id)
}

func (r *recordingTx) ListHoldsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Hold, error) {
	r.rec.note(book)
	return r.Tx.ListHoldsForAccount(ctx, book, id)
}

func (r *recordingTx) ActiveHoldTotal(ctx context.Context, book ledger.BookID, id deposit.AccountID, now time.Time) (ledger.Amount, error) {
	r.rec.note(book)
	return r.Tx.ActiveHoldTotal(ctx, book, id, now)
}

func (r *recordingTx) PutSnapshot(ctx context.Context, book ledger.BookID, s deposit.Snapshot) error {
	r.rec.note(book)
	return r.Tx.PutSnapshot(ctx, book, s)
}

func (r *recordingTx) GetSnapshot(ctx context.Context, book ledger.BookID, id deposit.AccountID, dateKey string) (deposit.Snapshot, error) {
	r.rec.note(book)
	return r.Tx.GetSnapshot(ctx, book, id, dateKey)
}

func (r *recordingTx) ListSnapshotsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Snapshot, error) {
	r.rec.note(book)
	return r.Tx.ListSnapshotsForAccount(ctx, book, id)
}

func (r *recordingTx) PutOverdraftTerms(ctx context.Context, book ledger.BookID, terms deposit.OverdraftTerms) error {
	r.rec.note(book)
	return r.Tx.PutOverdraftTerms(ctx, book, terms)
}

func (r *recordingTx) ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.OverdraftTerms, error) {
	r.rec.note(book)
	return r.Tx.ListOverdraftTermsForAccount(ctx, book, id)
}

func (r *recordingTx) GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id deposit.AccountID, day time.Time) (deposit.OverdraftTerms, error) {
	r.rec.note(book)
	return r.Tx.GetOverdraftTermsAsOf(ctx, book, id, day)
}

// --- lending.Tx ---

func (r *recordingTx) PutFacility(ctx context.Context, book ledger.BookID, f lending.Facility) error {
	r.rec.note(book)
	return r.Tx.PutFacility(ctx, book, f)
}

func (r *recordingTx) GetFacility(ctx context.Context, book ledger.BookID, id lending.FacilityID) (lending.Facility, error) {
	r.rec.note(book)
	return r.Tx.GetFacility(ctx, book, id)
}

func (r *recordingTx) ListFacilities(ctx context.Context, book ledger.BookID) ([]lending.Facility, error) {
	r.rec.note(book)
	return r.Tx.ListFacilities(ctx, book)
}

func (r *recordingTx) PutInstallment(ctx context.Context, book ledger.BookID, i lending.Installment) error {
	r.rec.note(book)
	return r.Tx.PutInstallment(ctx, book, i)
}

func (r *recordingTx) ListInstallments(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.Installment, error) {
	r.rec.note(book)
	return r.Tx.ListInstallments(ctx, book, id)
}

func (r *recordingTx) PutFacilityTerms(ctx context.Context, book ledger.BookID, terms lending.FacilityTerms) error {
	r.rec.note(book)
	return r.Tx.PutFacilityTerms(ctx, book, terms)
}

func (r *recordingTx) ListFacilityTerms(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.FacilityTerms, error) {
	r.rec.note(book)
	return r.Tx.ListFacilityTerms(ctx, book, id)
}

func (r *recordingTx) GetFacilityTermsAsOf(ctx context.Context, book ledger.BookID, id lending.FacilityID, day time.Time) (lending.FacilityTerms, error) {
	r.rec.note(book)
	return r.Tx.GetFacilityTermsAsOf(ctx, book, id, day)
}

// ---------------------------------------------------------------------------
// The decision the parser cannot make
// ---------------------------------------------------------------------------

// structCarriedBook is what was decided about one method whose book arrives
// inside its argument rather than as its argument.
type structCarriedBook struct {
	// Scoping says whether that BookID SCOPES the operation — whether the store
	// uses it to choose which book's data is read or written — or is merely a
	// field on a row that lives somewhere else entirely.
	Scoping bool
	// Why is required when Scoping is false: the evidence for the exclusion, and
	// the name of the test that makes it falsifiable. An exclusion nobody can
	// disprove is how ListAudit stayed unrecorded for two rounds.
	Why string
}

// structCarriedBooks records the decision for EVERY method whose book travels
// inside a struct. The parser finds the candidates; this says what they mean.
//
// # Why a table and not a rule
//
// There are two shapes of book argument, and the parser can recognise both:
// `book BookID` in second position, and a second-position struct with a BookID
// field. What no signature reveals is what the store DOES with the second kind.
// Compare, both of them structs with a BookID field in second position:
//
//   - ledger.AppendAudit(ctx, e AuditEvent) writes the row under e.BookID, and
//     ListAudit(ctx, f AuditFilter) selects rows by f.BookID — store/mem/tx.go
//     compares e.BookID against f.BookID, store/pg's tx_audit.go writes book_id
//     and adds `book_id = $n`. The field IS the scope.
//   - payment.PutParticipant(ctx, p Participant) writes a network-scoped row
//     under ledger.NetworkBook whatever p.BookID says. p.BookID is a column on
//     that row — the name of the book this bank owns — not the book being
//     written to. Recording it would make a clearing-house handler that admits a
//     member look like it had reached into that member's ledger, and Task 10's
//     TestTheCSMTouchesOnlyTheNetworkBook would fail on a handler doing exactly
//     what it should.
//
// Those two are indistinguishable from the AST. So the parser's job is to make
// sure none is FORGOTTEN, and this table's job is to say which is which — the
// same division payment's reasonTable makes for its sentinels, and for the same
// reason: a decision that cannot be automated must at least be compulsory.
//
// TestEveryStructCarriedBookIsDecided fails on a candidate with no entry and on
// an entry with no candidate, so a new method of this shape cannot slip through
// as either.
var structCarriedBooks = map[string]structCarriedBook{
	"AppendAudit": {Scoping: true},
	"ListAudit":   {Scoping: true},
	"PutParticipant": {
		Scoping: false,
		Why: "payment/store.go: participants are network-scoped and stored under ledger.NetworkBook. " +
			"Participant.BookID names the book the bank owns; it does not scope this write. " +
			"TestWritingAParticipantTouchesNoBankBook is the evidence.",
	},
}

// ---------------------------------------------------------------------------
// The guard on the guard
// ---------------------------------------------------------------------------

// TestRecordingTxOverridesEveryBookScopedMethod is the guard on the guard. A
// decorator that missed a method would COMPILE — Go promotes the embedded one —
// and record nothing for it, so a handler could reach another entity's book
// through exactly the method nobody wrapped, and every book assertion in this
// package would stay green while it did.
//
// It WALKS the interface embedding chain from payment.Tx rather than reading a
// list of files, for the same reason it parses rather than holding a list of
// methods: a list is a second copy and a second copy drifts. Wrapping only
// ledger.Tx was the first version of this test, and it was wrong — deposit,
// product and lending are book-scoped in exactly the same way, so a handler
// reading another bank's deposit register passed it. Walking means a fifth layer
// embedded into payment.Tx tomorrow is covered by this test on the day it lands.
//
// It parses THIS file for the other half rather than using reflect, because
// reflect cannot tell a method a type declares from one it inherits — which is
// the only distinction that matters here.
func TestRecordingTxOverridesEveryBookScopedMethod(t *testing.T) {
	methods := bookScopedTxMethods(t)
	if len(methods) == 0 {
		t.Fatal("walked payment.Tx and found no book-scoped methods; the parser is wrong, not the decorator")
	}
	want := make([]string, 0, len(methods))
	for _, m := range methods {
		want = append(want, m.Name)
	}
	got := recordingTxMethods(t)

	for _, m := range methods {
		if !slices.Contains(got, m.Name) {
			t.Errorf("recordingTx does not declare %s (%s.Tx).\n"+
				"It carries a book, so a handler can reach any book through it.\n"+
				"Embedding promotes it silently and records nothing: declare the override.", m.Name, m.Pkg)
		}
	}
	for _, name := range got {
		if !slices.Contains(want, name) {
			t.Errorf("recordingTx declares %s, which is not a book-scoped method reachable through payment.Tx.\n"+
				"Either it was renamed in its own layer and this override is now dead, or it\n"+
				"shadows something it should not.", name)
		}
	}
}

// TestEveryStructCarriedBookIsDecided is what stops the second shape being
// forgotten the way it was forgotten once already.
//
// ListAudit went unrecorded for two rounds behind a comment claiming the audit
// log was "not per-book at all", which was false and which nothing could
// contradict, because the parser only knew about books in second position. Now
// the parser finds both shapes, and a method whose book travels inside its
// argument must be DECIDED — recorded as scoping, or excluded with evidence.
// Neither silence nor an unbacked assertion is available.
func TestEveryStructCarriedBookIsDecided(t *testing.T) {
	candidates := map[string]bookMethod{}
	for _, m := range txBookCandidates(t) {
		if m.Carry == bookInsideTheArg {
			candidates[m.Name] = m
		}
	}
	if len(candidates) == 0 {
		t.Fatal("found no struct-carried book arguments at all; the parser is wrong, not the table")
	}

	for name, m := range candidates {
		decision, ok := structCarriedBooks[name]
		if !ok {
			t.Errorf("%s (%s.Tx) carries a book in %s.%s and structCarriedBooks does not decide it.\n"+
				"Say whether that field scopes the operation — whether the store reads or writes\n"+
				"another book because of it — and if it does not, say why and back it with a test.",
				name, m.Pkg, m.Arg, m.Field)
			continue
		}
		if !decision.Scoping && strings.TrimSpace(decision.Why) == "" {
			t.Errorf("structCarriedBooks[%q] excludes the method without saying why.\n"+
				"An exclusion nobody can disprove is how ListAudit stayed unrecorded.", name)
		}
	}
	for name := range structCarriedBooks {
		if _, ok := candidates[name]; !ok {
			t.Errorf("structCarriedBooks decides %s, which no longer carries a book inside its argument.\n"+
				"Either it was renamed, or its signature changed and this entry is now stale.", name)
		}
	}
}

// TestWritingAParticipantTouchesNoBankBook is the evidence behind the one
// exclusion in structCarriedBooks, made falsifiable rather than asserted.
//
// The claim is that Participant.BookID is a column on a network-scoped row, not
// the scope of the write. So writing a participant that NAMES a bank's book must
// leave that book empty — and this reads the book back through the store to say
// so, rather than trusting the recorder that is itself under test.
//
// If PutParticipant ever did write into p.BookID, this fails and the entry in
// structCarriedBooks becomes wrong at the same moment, which is what makes the
// exclusion a claim about the code rather than about the author's confidence.
func TestWritingAParticipantTouchesNoBankBook(t *testing.T) {
	clock := func() time.Time { return testTime }
	rec := newRecordingStore(testenv.New(t, clock).Payment())
	ctx := context.Background()
	victim := ledger.BookID("bank_verde")

	if err := rec.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return tx.PutParticipant(ctx, payment.Participant{
			ID:        "p_aurora",
			Name:      "Aurora Bank",
			BIC:       "AURODEFFXXX",
			BookID:    victim,
			CreatedAt: testTime,
		})
	}); err != nil {
		t.Fatalf("PutParticipant: %v", err)
	}

	// Nothing landed in the book the row names.
	if err := rec.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		ledgers, err := tx.ListLedgers(ctx, victim)
		if err != nil {
			return err
		}
		if len(ledgers) != 0 {
			t.Errorf("writing a participant put %d ledgers in %s; it is supposed to be a network-scoped row", len(ledgers), victim)
		}
		accounts, err := tx.ListAccounts(ctx, victim)
		if err != nil {
			return err
		}
		if len(accounts) != 0 {
			t.Errorf("writing a participant put %d accounts in %s; it is supposed to be a network-scoped row", len(accounts), victim)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}

	// And the row itself is readable without naming any book at all, which is
	// what "network-scoped" means.
	if err := rec.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		p, err := tx.GetParticipant(ctx, "p_aurora")
		if err != nil {
			return err
		}
		if p.BookID != victim {
			t.Errorf("participant came back with BookID %q, want %q — the field is stored, it just does not scope the write", p.BookID, victim)
		}
		return nil
	}); err != nil {
		t.Fatalf("GetParticipant: %v", err)
	}
}

// TestEveryRecordingTxMethodNotesItsBookThenDelegates pins the two halves the
// completeness test cannot see. A declared override that forgot to note records
// nothing — the same silent hole, one layer in — and one that forgot to delegate
// would hand every caller a zero value while looking perfectly wrapped.
//
// The check is on the SHAPE of the body, and the shape is deliberately uniform:
// note the book, then call through. The note expression is derived from the
// method's own parsed signature and from which shape of book argument it has, so
// this is not a second hand-written list of anything. No method needs a third
// statement, so a body that is not those two is a mistake rather than a
// variation, and this says so with the method's name in it.
//
// A consequence worth naming: recordingTx may hold no helper methods, because
// every method on it is checked against this shape. That is deliberate — reach
// for a free function instead, as bookOf is.
func TestEveryRecordingTxMethodNotesItsBookThenDelegates(t *testing.T) {
	shape := map[string]bookMethod{}
	for _, m := range bookScopedTxMethods(t) {
		shape[m.Name] = m
	}

	fset, file := parseMeshFile(t, "books_test.go")
	var checked int
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || receiverTypeName(fd) != "recordingTx" {
			continue
		}
		checked++
		recv := fd.Recv.List[0].Names[0].Name
		names := paramNames(fd.Type)

		m, known := shape[fd.Name.Name]
		if !known {
			continue // the completeness test reports this one; nothing to shape-check
		}
		if len(names) < 2 {
			t.Errorf("recordingTx.%s takes no book argument", fd.Name.Name)
			continue
		}
		var wantNote string
		switch m.Carry {
		case bookIsTheArg:
			wantNote = recv + ".rec.note(" + names[1] + ")"
		case bookInsideTheArg:
			wantNote = recv + ".rec.note(bookOf(" + names[1] + "." + m.Field + "))"
		}

		if fd.Body == nil || len(fd.Body.List) != 2 {
			t.Errorf("recordingTx.%s is not two statements; every override notes its book and delegates", fd.Name.Name)
			continue
		}
		if got := render(t, fset, fd.Body.List[0]); got != wantNote {
			t.Errorf("recordingTx.%s starts with %q, want %q — an override that does not note records nothing",
				fd.Name.Name, got, wantNote)
		}
		want := "return " + recv + ".Tx." + fd.Name.Name + "(" + strings.Join(names, ", ") + ")"
		if got := render(t, fset, fd.Body.List[1]); got != want {
			t.Errorf("recordingTx.%s ends with %q, want %q — an override that does not delegate returns a zero value",
				fd.Name.Name, got, want)
		}
	}
	if checked == 0 {
		t.Fatal("found no methods on recordingTx; the parser is wrong, not the decorator")
	}
}

// errProbeDone unwinds the probe below without committing anything.
var errProbeDone = errors.New("probe finished")

// TestRecordingStoreRecordsTheBookOfEveryCall is the same claim as the two AST
// tests, made at RUN time and end to end: every book-scoped method, called
// through a recordingStore over the store the suite is configured with, leaves
// its book and no other in touched().
//
// The AST tests read source, so between them and reality sit Update, the
// embedding and note itself. This drives all three, and it drives every method
// rather than a chosen few, because "the wiring works" is a claim about the one
// method nobody thought to spot-check.
//
// Each method gets its OWN unit of work, and each ends by returning errProbeDone
// so the work rolls back. That is not tidiness: the arguments are zero values
// apart from the book, so many of these calls fail, and under store/pg a failed
// statement aborts the transaction. Rolling back deliberately means the probe
// asserts the SAME thing against both stores — that Update handed back exactly
// the error the callback returned — instead of discarding an error that only
// Postgres would ever have produced. The store's answer to the call itself is
// still discarded, on purpose: the book is noted BEFORE the call goes through,
// which is what makes every method reachable without a fixture apiece.
//
// What the recorder saw survives the rollback, because a read that happened and
// was then rolled back still happened.
func TestRecordingStoreRecordsTheBookOfEveryCall(t *testing.T) {
	clock := func() time.Time { return testTime }
	rec := newRecordingStore(testenv.New(t, clock).Payment())
	ctx := context.Background()

	methods := bookScopedTxMethods(t)
	if len(methods) == 0 {
		t.Fatal("no book-scoped methods parsed; the parser is wrong, not the recorder")
	}
	for _, m := range methods {
		rec.reset()
		book := ledger.BookID("book_" + m.Name)
		err := rec.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
			fn := reflect.ValueOf(tx).MethodByName(m.Name)
			if !fn.IsValid() {
				t.Errorf("recordingTx has no method %s (%s.Tx)", m.Name, m.Pkg)
				return errProbeDone
			}
			args := make([]reflect.Value, fn.Type().NumIn())
			args[0] = reflect.ValueOf(ctx)
			args[1] = bookArgument(t, fn.Type().In(1), m, book)
			for i := 2; i < len(args); i++ {
				args[i] = reflect.Zero(fn.Type().In(i))
			}
			fn.Call(args)
			return errProbeDone
		})
		if !errors.Is(err, errProbeDone) {
			t.Errorf("probing %s (%s.Tx): Update returned %v, want the callback's own error back", m.Name, m.Pkg, err)
		}
		got := rec.touched()
		if len(got) != 1 || got[0] != book {
			t.Errorf("calling %s (%s.Tx) on book %q recorded %v, want exactly [%s]", m.Name, m.Pkg, book, got, book)
		}
	}
}

// bookArgument builds the second argument of a book-scoped call: the book
// itself, or a zero struct carrying it in the field the parser found.
func bookArgument(t *testing.T, typ reflect.Type, m bookMethod, book ledger.BookID) reflect.Value {
	t.Helper()
	arg := reflect.New(typ).Elem()
	switch m.Carry {
	case bookIsTheArg:
		arg.Set(reflect.ValueOf(book))
	case bookInsideTheArg:
		f := arg.FieldByName(m.Field)
		if !f.IsValid() {
			t.Fatalf("%s: %s has no field %s", m.Name, typ, m.Field)
		}
		f.Set(reflect.ValueOf(book))
	}
	return arg
}

// TestACrossBookAuditReadIsRecorded is the crossing the first two rounds left
// wide open, written as a test rather than argued about in a comment.
//
// A handler holding a legitimate Tx can call ListAudit with any book's id and
// read that bank's entire audit trail, Payload included — an auditable history
// of every account opened, every hold taken, every posting made. Before the
// override existed this recorded NOTHING, so Task 10's
// TestABankHandlerTouchesOnlyItsOwnBook would have passed over it.
//
// The second half is the wider crossing still: an AuditFilter with no BookID
// reads every book at once. That must not look like a clean unit of work either,
// which is what everyBook is for.
func TestACrossBookAuditReadIsRecorded(t *testing.T) {
	clock := func() time.Time { return testTime }
	rec := newRecordingStore(testenv.New(t, clock).Payment())
	ctx := context.Background()
	victim := ledger.BookID("bank_verde")

	if err := rec.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		_, err := tx.ListAudit(ctx, ledger.AuditFilter{BookID: victim})
		return err
	}); err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if got := rec.touched(); !slices.Equal(got, []ledger.BookID{victim}) {
		t.Errorf("a unit of work that read %s's audit trail touched %v, want [%s]", victim, got, victim)
	}

	rec.reset()
	if err := rec.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		_, err := tx.ListAudit(ctx, ledger.AuditFilter{})
		return err
	}); err != nil {
		t.Fatalf("unfiltered ListAudit: %v", err)
	}
	if got := rec.touched(); !slices.Equal(got, []ledger.BookID{everyBook}) {
		t.Errorf("an unfiltered audit read touched %v, want [%s] — it reads every book, and must not pass for a quiet one", got, everyBook)
	}
}

// TestRecordingTxReachesTheStoreUnderneath is the delegation half at run time.
//
// A decorator that noted and returned a zero value would satisfy every book
// assertion in this package while quietly detaching the mesh from its store —
// the failure mode where the boundary check passes because nothing happens at
// all. Probes across three layers: NextID has to allocate from real state,
// GetLedger has to bring back the ledger's own not-found sentinel,
// GetDepositAccount the deposit layer's, and ListAudit has to come back from a
// store that really ran the query.
//
// The read legs also cover a thing the per-method probe does not: View wraps its
// Tx as Update does. A read-only unit of work is exactly where a cross-entity
// read hides — reading another bank's register or its audit trail needs no write
// — so a View that handed back the bare Tx would leave the whole read side
// unrecorded.
func TestRecordingTxReachesTheStoreUnderneath(t *testing.T) {
	clock := func() time.Time { return testTime }
	rec := newRecordingStore(testenv.New(t, clock).Payment())
	ctx := context.Background()
	// Two books, named so that "aurora" sorts before "verde": touched() is
	// asserted whole, order included.
	written := ledger.BookID("bank_aurora")
	read := ledger.BookID("bank_verde")

	err := rec.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		first, err := tx.NextID(ctx, written, "ldg")
		if err != nil {
			return err
		}
		second, err := tx.NextID(ctx, written, "ldg")
		if err != nil {
			return err
		}
		if first == "" || first == second {
			t.Errorf("NextID returned %q then %q; a delegating decorator allocates from the store's own counter", first, second)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	err = rec.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		if _, err := tx.GetLedger(ctx, read, "ldg_nope"); !errors.Is(err, ledger.ErrLedgerNotFound) {
			t.Errorf("GetLedger on a missing row returned %v, want ledger.ErrLedgerNotFound from the store underneath", err)
		}
		if _, err := tx.GetDepositAccount(ctx, read, "dep_nope"); !errors.Is(err, deposit.ErrAccountNotFound) {
			t.Errorf("GetDepositAccount on a missing row returned %v, want deposit.ErrAccountNotFound from the store underneath", err)
		}
		events, err := tx.ListAudit(ctx, ledger.AuditFilter{BookID: read})
		if err != nil {
			t.Errorf("ListAudit returned %v; a delegating decorator runs the store's query", err)
		}
		if len(events) != 0 {
			t.Errorf("ListAudit on an untouched book returned %d events, want none", len(events))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}

	if got := rec.touched(); !slices.Equal(got, []ledger.BookID{written, read}) {
		t.Errorf("touched() = %v, want [%s %s] — both units of work, sorted", got, written, read)
	}
}

// TestTheRecorderIsSafeForConcurrentActorsAndSortsWhatItSaw makes the two
// claims about the recorder's own bookkeeping falsifiable, neither of which any
// test above can see.
//
// Concurrency, because the mesh runs one goroutine per institution and they all
// drive the same store: an unlocked map here would be a data race in every flow
// test from Task 10 on, reported against the flow rather than against this.
// Under -race, dropping the mutex in note fails this.
//
// Sorting, because touched() is asserted whole. Five books rather than two, so
// that an unsorted result cannot pass by landing in order — Go's map iteration
// would have to hit one permutation in 120.
//
// It needs no store: nothing here goes through Update, which is why the inner
// store is nil.
func TestTheRecorderIsSafeForConcurrentActorsAndSortsWhatItSaw(t *testing.T) {
	rec := newRecordingStore(nil)

	var wg sync.WaitGroup
	for _, b := range []ledger.BookID{"e", "d", "c", "b", "a"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec.note(b)
		}()
	}
	// A reader beside the writers: a test that asserts on books while actors are
	// still running is the ordinary case, not an exotic one.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = rec.touched()
	}()
	wg.Wait()

	want := []ledger.BookID{"a", "b", "c", "d", "e"}
	if got := rec.touched(); !slices.Equal(got, want) {
		t.Errorf("touched() = %v, want %v — the set of books, ascending", got, want)
	}
}

// ---------------------------------------------------------------------------
// Parsing: walking the Tx embedding chain
// ---------------------------------------------------------------------------

// bookArg is HOW a method's second argument carries its book.
type bookArg int

const (
	noBook bookArg = iota
	// bookIsTheArg: `book ledger.BookID` — 50 of the 52.
	bookIsTheArg
	// bookInsideTheArg: a struct with a BookID field, as AuditEvent and
	// AuditFilter have.
	bookInsideTheArg
)

// bookMethod is one book-carrying method: where it is declared, how it carries
// its book, and — for the struct shape — under what name.
type bookMethod struct {
	Pkg   string // the declaring package's directory name: ledger, product, …
	Name  string
	Carry bookArg
	Arg   string // the second parameter's name in the interface declaration
	Field string // bookInsideTheArg only: the BookID field's name
}

// txBookCandidates is every method reachable through payment.Tx whose second
// argument carries a book, in either shape, found by walking the interface
// embedding chain from payment/store.go.
//
// # What counts as carrying a book
//
// The SECOND parameter — the one straight after ctx — either
//
//  1. IS a ledger.BookID, or
//  2. is a struct with a field of type ledger.BookID.
//
// Both shapes exist and the parser must know both, because a parser that knew
// only the first is precisely what let ListAudit go unrecorded behind a comment
// asserting it was not per-book. A shape the parser cannot see is a shape nobody
// is forced to think about, and the next one added would be missed the same way.
//
// Whether shape 2 actually SCOPES the operation is not visible from any
// signature — see structCarriedBooks, which is where that is decided. This
// function's job is only to make sure none is forgotten.
//
// Three deliberate properties of the rule:
//
//   - Type and position, not name. The parameter is called `book` everywhere
//     today, but a name is documentation and a type is a fact.
//   - Either spelling of the type, resolved properly: a bare `BookID` counts
//     only inside package ledger, and a qualified one is resolved through the
//     file's own import block rather than by assuming the qualifier is spelled
//     "ledger". A layer that wrote `import l "…/ledger"` is handled, which
//     matters because the embed walk already honours aliases and two halves of
//     one walk disagreeing is how a whole layer drops out silently.
//   - A book carried anywhere OTHER than second is an error, not a skip.
//     Silently skipping is how a method ends up unwrapped, and the uniform
//     position is what lets every override be the same two lines.
//
// Methods carrying no book at all are correctly absent: ledger's Now (the
// clock), and payment.Tx's readers and its writers for payments, mandates,
// cycles and settlements, which are network-scoped and live under
// ledger.NetworkBook.
//
// # Why it walks rather than reading a list of files
//
// A hand-listed set of files is a second copy of the layering, and it drifts the
// same way a hand-listed set of methods would: the first version of this test
// read ledger/store.go alone and therefore called a decorator complete while
// deposit, product and lending were entirely unrecorded. Walking the chain means
// the test's idea of "everything payment.Tx can reach" is Go's.
func txBookCandidates(t *testing.T) []bookMethod {
	t.Helper()
	module := modulePath(t)
	cache := map[string]*pkgAST{}

	var out []bookMethod
	seen := map[string]bool{}

	var walk func(dir string)
	walk = func(dir string) {
		if seen[dir] {
			return // ledger.Tx is reached through product and through lending
		}
		seen[dir] = true
		pkg := loadPackage(t, cache, dir, module)
		if pkg.iface == nil {
			t.Errorf("no `type Tx interface` found in %s", dir)
			return
		}
		for _, f := range pkg.iface.Methods.List {
			// An embedded interface has no name. Recurse into the package that
			// declares it, resolving the qualifier through the importing file's
			// own import block rather than assuming the alias is the directory.
			if len(f.Names) == 0 {
				sel, ok := f.Type.(*ast.SelectorExpr)
				if !ok {
					t.Errorf("%s/Tx embeds %T, which this walk does not understand", dir, f.Type)
					continue
				}
				qualifier, ok := sel.X.(*ast.Ident)
				if !ok || sel.Sel.Name != "Tx" {
					t.Errorf("%s/Tx embeds something other than a package's Tx", dir)
					continue
				}
				path, ok := pkg.ifaceImports[qualifier.Name]
				if !ok {
					t.Errorf("%s/Tx embeds %s.Tx but the file imports no %s", dir, qualifier.Name, qualifier.Name)
					continue
				}
				rel, inModule := strings.CutPrefix(path, module+"/")
				if !inModule {
					t.Errorf("%s/Tx embeds %s, which is outside this module", dir, path)
					continue
				}
				walk(filepath.Join("..", rel))
				continue
			}
			fn, ok := f.Type.(*ast.FuncType)
			if !ok {
				continue
			}
			m := classify(t, cache, module, pkg, f.Names[0].Name, fn)
			if m.Carry != noBook {
				out = append(out, m)
			}
		}
	}
	walk(filepath.Join("..", "payment"))

	// Names are unique across the chain — Go rejects an interface that embeds
	// two methods of one name — so a duplicate here means the walk visited
	// something twice and every count taken from it is wrong.
	byName := map[string]string{}
	for _, m := range out {
		if prev, dup := byName[m.Name]; dup {
			t.Errorf("%s found in both %s and %s; the walk is double-counting", m.Name, prev, m.Pkg)
		}
		byName[m.Name] = m.Pkg
	}
	return out
}

// bookScopedTxMethods is the subset of the candidates that recordingTx must
// override: every positional one, and every struct-carried one that
// structCarriedBooks says really scopes its operation.
func bookScopedTxMethods(t *testing.T) []bookMethod {
	t.Helper()
	var out []bookMethod
	for _, m := range txBookCandidates(t) {
		switch m.Carry {
		case bookIsTheArg:
			out = append(out, m)
		case bookInsideTheArg:
			decision, ok := structCarriedBooks[m.Name]
			if !ok {
				t.Errorf("%s carries a book in %s.%s and structCarriedBooks does not decide it; see TestEveryStructCarriedBookIsDecided",
					m.Name, m.Arg, m.Field)
				continue
			}
			if decision.Scoping {
				out = append(out, m)
			}
		}
	}
	return out
}

// classify decides how one interface method carries its book, and fails the
// test if it carries one somewhere the decorator could not reach uniformly.
func classify(t *testing.T, cache map[string]*pkgAST, module string, pkg *pkgAST, name string, fn *ast.FuncType) bookMethod {
	t.Helper()
	params := flatParams(fn)

	found := bookMethod{Pkg: filepath.Base(pkg.dir), Name: name}
	for i, p := range params {
		carry, field := carrierOf(t, cache, module, pkg, p.typ)
		if carry == noBook {
			continue
		}
		if i != 1 {
			t.Errorf("%s.Tx.%s carries its book at argument %d, not straight after ctx.\n"+
				"Every override relies on that position; move it back, or teach this test the new rule.",
				filepath.Base(pkg.dir), name, i)
			continue
		}
		found.Carry, found.Arg, found.Field = carry, p.name, field
	}
	return found
}

// carrierOf reports whether a parameter type carries a book, and how.
func carrierOf(t *testing.T, cache map[string]*pkgAST, module string, pkg *pkgAST, typ ast.Expr) (bookArg, string) {
	t.Helper()
	if isBookID(typ, pkg.ifaceImports, pkg.path, module) {
		return bookIsTheArg, ""
	}
	decl, ok := lookupStruct(t, cache, module, pkg, typ)
	if !ok {
		return noBook, ""
	}
	var fields []string
	for _, f := range decl.typ.Fields.List {
		if !isBookID(f.Type, decl.imports, decl.path, module) {
			continue
		}
		for _, n := range f.Names {
			fields = append(fields, n.Name)
		}
	}
	switch len(fields) {
	case 0:
		return noBook, ""
	case 1:
		return bookInsideTheArg, fields[0]
	default:
		t.Errorf("%s carries %d BookID fields (%s); the recorder would not know which one scopes the call",
			render(t, token.NewFileSet(), typ), len(fields), strings.Join(fields, ", "))
		return noBook, ""
	}
}

// lookupStruct resolves a parameter's type to a struct declaration, in this
// package or — following the file's imports — in another one inside the module.
func lookupStruct(t *testing.T, cache map[string]*pkgAST, module string, pkg *pkgAST, typ ast.Expr) (structDecl, bool) {
	t.Helper()
	switch e := typ.(type) {
	case *ast.Ident:
		d, ok := pkg.structs[e.Name]
		return d, ok
	case *ast.SelectorExpr:
		qualifier, ok := e.X.(*ast.Ident)
		if !ok {
			return structDecl{}, false
		}
		path, ok := pkg.ifaceImports[qualifier.Name]
		if !ok {
			return structDecl{}, false
		}
		rel, inModule := strings.CutPrefix(path, module+"/")
		if !inModule {
			return structDecl{}, false // stdlib and third-party carry no BookID
		}
		other := loadPackage(t, cache, filepath.Join("..", rel), module)
		d, ok := other.structs[e.Sel.Name]
		return d, ok
	}
	return structDecl{}, false
}

// isBookID reports whether a type expression is ledger.BookID.
//
// A bare `BookID` counts only inside package ledger, and a qualified one is
// resolved through the importing file's own imports — never by comparing the
// qualifier to the string "ledger", which would miss an aliased import and
// silently drop every method in that layer.
func isBookID(e ast.Expr, imports map[string]string, pkgPath, module string) bool {
	ledgerPath := module + "/ledger"
	switch typ := e.(type) {
	case *ast.Ident:
		return typ.Name == "BookID" && pkgPath == ledgerPath
	case *ast.SelectorExpr:
		qualifier, ok := typ.X.(*ast.Ident)
		if !ok || typ.Sel.Name != "BookID" {
			return false
		}
		return imports[qualifier.Name] == ledgerPath
	}
	return false
}

// ---------------------------------------------------------------------------
// Parsing: reading a package
// ---------------------------------------------------------------------------

// structDecl is one struct type declaration, with what it needs to have its own
// field types resolved.
type structDecl struct {
	typ     *ast.StructType
	imports map[string]string
	path    string // the declaring package's import path
}

// pkgAST is one package as this walk needs it: its Tx interface, the imports of
// the file that declares it, and every struct type it declares.
type pkgAST struct {
	dir          string
	path         string
	iface        *ast.InterfaceType
	ifaceImports map[string]string
	structs      map[string]structDecl
}

// loadPackage parses every non-test file in a package directory once.
func loadPackage(t *testing.T, cache map[string]*pkgAST, dir, module string) *pkgAST {
	t.Helper()
	if p, ok := cache[dir]; ok {
		return p
	}
	rel := strings.TrimPrefix(filepath.ToSlash(dir), "../")
	pkg := &pkgAST{dir: dir, path: module + "/" + rel, structs: map[string]structDecl{}}
	cache[dir] = pkg

	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		_, file := parseMeshFile(t, path)
		imports := fileImports(file)
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				ts, ok := s.(*ast.TypeSpec)
				if !ok {
					continue
				}
				switch typ := ts.Type.(type) {
				case *ast.InterfaceType:
					if ts.Name.Name == "Tx" {
						pkg.iface, pkg.ifaceImports = typ, imports
					}
				case *ast.StructType:
					pkg.structs[ts.Name.Name] = structDecl{typ: typ, imports: imports, path: pkg.path}
				}
			}
		}
	}
	return pkg
}

// fileImports maps the qualifier a file uses onto the import path behind it,
// honouring an explicit alias.
func fileImports(file *ast.File) map[string]string {
	out := make(map[string]string, len(file.Imports))
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = path
	}
	return out
}

// modulePath is this repository's module path, read from go.mod so that an
// import path can be turned into a directory without hard-coding it.
func modulePath(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	for line := range strings.Lines(string(b)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("go.mod declares no module path")
	return ""
}

// param is one flattened parameter: `from, to time.Time` is two of them.
type param struct {
	name string
	typ  ast.Expr
}

func flatParams(fn *ast.FuncType) []param {
	var out []param
	for _, f := range fn.Params.List {
		if len(f.Names) == 0 {
			out = append(out, param{typ: f.Type})
			continue
		}
		for _, n := range f.Names {
			out = append(out, param{name: n.Name, typ: f.Type})
		}
	}
	return out
}

// recordingTxMethods is every method declared DIRECTLY on recordingTx in this
// file. Declared, not promoted — which is the whole point, and the reason this
// reads the source instead of asking reflect.
func recordingTxMethods(t *testing.T) []string {
	t.Helper()
	_, file := parseMeshFile(t, "books_test.go")
	var out []string
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && receiverTypeName(fd) == "recordingTx" {
			out = append(out, fd.Name.Name)
		}
	}
	return out
}

// receiverTypeName is the name of a method's receiver type with any pointer
// stripped, or "" for a plain function.
func receiverTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return ""
	}
	typ := fd.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// paramNames flattens a signature's parameter names in order.
func paramNames(fn *ast.FuncType) []string {
	out := make([]string, 0, len(fn.Params.List))
	for _, p := range flatParams(fn) {
		out = append(out, p.name)
	}
	return out
}

func parseMeshFile(t *testing.T, path string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return fset, file
}

func render(t *testing.T, fset *token.FileSet, n ast.Node) string {
	t.Helper()
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, n); err != nil {
		t.Fatalf("printing %T: %v", n, err)
	}
	return b.String()
}
