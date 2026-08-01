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
// Every book-scoped method reachable through a payment.Tx takes the book as its
// first argument after ctx, in every one of the four layers, which is what makes
// this a decorator and not a re-architecture: there is one place per method to
// record, and no plumbing.
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
// were. The parser walks the embedding chain to find them, so the next method
// added to any of the four is covered without anyone remembering to add it here.
//
// payment.Tx's OWN methods take no book: participants, payments, mandates,
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
// because an override that forgot either half would look wrapped and be
// worthless.

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
				"It takes a book, so a handler can reach any book through it.\n"+
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

// TestEveryRecordingTxMethodNotesItsBookThenDelegates pins the two halves the
// completeness test cannot see. A declared override that forgot to note records
// nothing — the same silent hole, one layer in — and one that forgot to delegate
// would hand every caller a zero value while looking perfectly wrapped.
//
// The check is on the SHAPE of the body, and the shape is deliberately uniform:
// note the book, then call through. No method here needs anything else, so a
// body that is not those two statements is a mistake rather than a variation,
// and this says so with the method's name in it.
func TestEveryRecordingTxMethodNotesItsBookThenDelegates(t *testing.T) {
	fset, file := parseMeshFile(t, "books_test.go")
	var checked int
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || receiverTypeName(fd) != "recordingTx" {
			continue
		}
		checked++
		recv := fd.Recv.List[0].Names[0].Name

		if fd.Body == nil || len(fd.Body.List) != 2 {
			t.Errorf("recordingTx.%s is not two statements; every override notes its book and delegates", fd.Name.Name)
			continue
		}
		if got, want := render(t, fset, fd.Body.List[0]), recv+".rec.note(book)"; got != want {
			t.Errorf("recordingTx.%s starts with %q, want %q — an override that does not note records nothing",
				fd.Name.Name, got, want)
		}
		want := "return " + recv + ".Tx." + fd.Name.Name + "(" + strings.Join(paramNames(fd.Type), ", ") + ")"
		if got := render(t, fset, fd.Body.List[1]); got != want {
			t.Errorf("recordingTx.%s ends with %q, want %q — an override that does not delegate returns a zero value",
				fd.Name.Name, got, want)
		}
	}
	if checked == 0 {
		t.Fatal("found no methods on recordingTx; the parser is wrong, not the decorator")
	}
}

// TestRecordingStoreRecordsTheBookOfEveryCall is the same claim as the two AST
// tests, made at RUN time and end to end: every book-scoped method, called
// through a recordingStore over the store the suite is configured with, leaves
// its book and no other in touched().
//
// The AST tests read source, so between them and reality sit Update, View, the
// embedding and note itself. This drives all three, and it drives every method
// rather than a chosen few, because "the wiring works" is a claim about the one
// method nobody thought to spot-check.
//
// The store's answers are discarded on purpose. The arguments are zero values,
// so most of these calls fail — which is fine and is the point: the book is
// noted BEFORE the call goes through, so what is under test is reachable without
// building a fixture per method.
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
		_ = rec.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
			fn := reflect.ValueOf(tx).MethodByName(m.Name)
			if !fn.IsValid() {
				t.Errorf("recordingTx has no method %s (%s.Tx)", m.Name, m.Pkg)
				return nil
			}
			args := make([]reflect.Value, fn.Type().NumIn())
			args[0] = reflect.ValueOf(ctx)
			args[1] = reflect.ValueOf(book)
			for i := 2; i < len(args); i++ {
				args[i] = reflect.Zero(fn.Type().In(i))
			}
			fn.Call(args)
			return nil
		})
		got := rec.touched()
		if len(got) != 1 || got[0] != book {
			t.Errorf("calling %s (%s.Tx) on book %q recorded %v, want exactly [%s]", m.Name, m.Pkg, book, got, book)
		}
	}
}

// TestRecordingTxReachesTheStoreUnderneath is the delegation half at run time.
//
// A decorator that noted and returned a zero value would satisfy every book
// assertion in this package while quietly detaching the mesh from its store —
// the failure mode where the boundary check passes because nothing happens at
// all. Three probes across two layers: NextID has to allocate from real state,
// GetLedger has to bring back the ledger's own not-found sentinel, and
// GetDepositAccount the deposit layer's — the layer this recorder did not cover
// in its first version, and the one whose absence nothing else here would show.
//
// The read legs also cover a thing the per-method test does not: View wraps its
// Tx as Update does. A read-only unit of work is exactly where a cross-entity
// read hides — reading another bank's register needs no write — so a View that
// handed back the bare Tx would leave the whole read side unrecorded.
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

// bookMethod is one book-scoped method and the layer that declares it.
type bookMethod struct {
	Pkg  string // the declaring package's directory name: ledger, product, …
	Name string
}

// bookScopedTxMethods is every book-scoped method reachable through payment.Tx,
// found by walking the interface embedding chain from payment/store.go.
//
// # What counts as book-scoped
//
// A method is book-scoped when its SECOND parameter — the one straight after
// ctx — is a ledger.BookID. That is the rule, and it is deliberately about the
// TYPE and the POSITION rather than the parameter's name: the name is
// documentation, the type is the fact, and the position is what every override
// relies on.
//
// The type is matched in either spelling, because both occur: bare `BookID`
// inside package ledger, `ledger.BookID` in product, deposit and lending.
//
// A method that takes a BookID somewhere OTHER than second is an error here, not
// a skip. Silently skipping is precisely how a method ends up unwrapped, and the
// uniform position is what lets every override be the same two lines.
//
// Methods with NO BookID are not book-scoped, and their absence is correct
// rather than an oversight. They are of two kinds. ledger's AppendAudit,
// ListAudit and Now are not per-book at all — the audit log is deliberately
// cross-book, which is why the audit table has no foreign key. All of
// payment.Tx's own methods — participants, payments, mandates, cycles,
// settlements — are network-scoped: they belong to no single bank and are stored
// under ledger.NetworkBook, so there is no book argument to record. Neither kind
// is a way to read another bank's books, which is what this recorder is for.
//
// # Why it walks rather than reading a list of files
//
// A hand-listed set of files is a second copy of the layering, and it drifts the
// same way a hand-listed set of methods would: the first version of this test
// read ledger/store.go alone and therefore called a decorator complete while
// deposit, product and lending were entirely unrecorded. Walking the chain means
// the test's idea of "everything payment.Tx can reach" is Go's.
func bookScopedTxMethods(t *testing.T) []bookMethod {
	t.Helper()
	module := modulePath(t)

	var out []bookMethod
	seen := map[string]bool{}

	var walk func(dir string)
	walk = func(dir string) {
		if seen[dir] {
			return // ledger.Tx is reached through product and through lending
		}
		seen[dir] = true
		iface, imports := findTxInterface(t, dir)
		if iface == nil {
			return
		}
		for _, f := range iface.Methods.List {
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
				path, ok := imports[qualifier.Name]
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
			name := f.Names[0].Name
			switch pos := bookParamIndex(fn); pos {
			case -1:
				continue // not book-scoped
			case 1:
				out = append(out, bookMethod{Pkg: filepath.Base(dir), Name: name})
			default:
				t.Errorf("%s.Tx.%s takes its book at argument %d, not straight after ctx.\n"+
					"Every override relies on that position; move it back, or teach this test the new rule.",
					filepath.Base(dir), name, pos)
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

// findTxInterface returns the `Tx` interface declared in a package directory,
// together with that file's imports so an embedded qualifier can be resolved.
func findTxInterface(t *testing.T, dir string) (*ast.InterfaceType, map[string]string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		_, file := parseMeshFile(t, path)
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				ts, ok := s.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "Tx" {
					continue
				}
				iface, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					t.Fatalf("%s: Tx is not an interface type", path)
				}
				return iface, fileImports(file)
			}
		}
	}
	t.Fatalf("no `type Tx interface` found in %s", dir)
	return nil, nil
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

// bookParamIndex is the position of the first ledger.BookID parameter in a
// flattened argument list, or -1. See bookScopedTxMethods for the rule.
func bookParamIndex(fn *ast.FuncType) int {
	i := -1
	for _, f := range fn.Params.List {
		if len(f.Names) == 0 {
			i++
			if isBookID(f.Type) {
				return i
			}
			continue
		}
		for range f.Names {
			i++
			if isBookID(f.Type) {
				return i
			}
		}
	}
	return -1
}

// isBookID reports whether a parameter's type is ledger.BookID, in either
// spelling: bare inside package ledger, qualified everywhere else.
func isBookID(e ast.Expr) bool {
	switch typ := e.(type) {
	case *ast.Ident:
		return typ.Name == "BookID"
	case *ast.SelectorExpr:
		pkg, ok := typ.X.(*ast.Ident)
		return ok && pkg.Name == "ledger" && typ.Sel.Name == "BookID"
	}
	return false
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

// paramNames flattens a signature's parameter names in order, so that
// `from, to time.Time` is two of them.
func paramNames(fn *ast.FuncType) []string {
	var out []string
	for _, f := range fn.Params.List {
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
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
