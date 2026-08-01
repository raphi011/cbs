package mesh

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
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
// Every ledger.Tx method takes `book BookID` as its FIRST argument after ctx
// (ledger/store.go), which is what makes this a decorator and not a
// re-architecture: there is one place per method to record, and no plumbing.
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
// the book of every book-scoped ledger.Tx call before delegating.
//
// It EMBEDS payment.Tx, so everything else — the payment, deposit, lending and
// product methods, and ledger's three unbooked ones — is promoted untouched. The
// overrides below are exactly the book-scoped ledger.Tx methods, and
// TestRecordingTxOverridesEveryBookScopedMethod is what keeps that "exactly"
// true: an override that went missing would be silently replaced by the promoted
// method, which records nothing.
type recordingTx struct {
	payment.Tx
	rec *recordingStore
}

var _ payment.Tx = (*recordingTx)(nil)

// The overrides. Each is the same two statements — note the book, then call
// through — and TestEveryRecordingTxMethodNotesItsBookThenDelegates holds them
// to that shape, because an override that forgot either half would look wrapped
// and be worthless.

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

// ---------------------------------------------------------------------------
// The guard on the guard
// ---------------------------------------------------------------------------

// TestRecordingTxOverridesEveryBookScopedMethod is the guard on the guard. A
// decorator that missed a method would COMPILE — Go promotes the embedded one —
// and record nothing for it, so a handler could reach another entity's book
// through exactly the method nobody wrapped, and every book assertion in this
// package would stay green while it did.
//
// It parses ledger/store.go rather than holding a hand-written list, for the
// same reason payment's TestReasonTableCoversEverySentinel does: a list is a
// second copy, and a second copy drifts. It parses THIS file for the other half
// rather than using reflect, because reflect cannot tell a method a type
// declares from one it inherits — which is the only distinction that matters
// here.
func TestRecordingTxOverridesEveryBookScopedMethod(t *testing.T) {
	want := bookScopedLedgerMethods(t)
	if len(want) == 0 {
		t.Fatal("parsed ledger/store.go and found no book-scoped methods on Tx; the parser is wrong, not the decorator")
	}
	got := recordingTxMethods(t)

	for _, name := range want {
		if !slices.Contains(got, name) {
			t.Errorf("recordingTx does not declare %s.\n"+
				"ledger.Tx.%s takes a book, so a handler can reach any book through it.\n"+
				"Embedding promotes it silently and records nothing: declare the override.", name, name)
		}
	}
	for _, name := range got {
		if !slices.Contains(want, name) {
			t.Errorf("recordingTx declares %s, which is not a book-scoped method on ledger.Tx.\n"+
				"Either it was renamed in ledger/store.go and this override is now dead, or it\n"+
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

	names := bookScopedLedgerMethods(t)
	if len(names) == 0 {
		t.Fatal("no book-scoped methods parsed; the parser is wrong, not the recorder")
	}
	for _, name := range names {
		rec.reset()
		book := ledger.BookID("book_" + name)
		_ = rec.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
			m := reflect.ValueOf(tx).MethodByName(name)
			if !m.IsValid() {
				t.Errorf("recordingTx has no method %s", name)
				return nil
			}
			args := make([]reflect.Value, m.Type().NumIn())
			args[0] = reflect.ValueOf(ctx)
			args[1] = reflect.ValueOf(book)
			for i := 2; i < len(args); i++ {
				args[i] = reflect.Zero(m.Type().In(i))
			}
			m.Call(args)
			return nil
		})
		got := rec.touched()
		if len(got) != 1 || got[0] != book {
			t.Errorf("calling %s on book %q recorded %v, want exactly [%s]", name, book, got, book)
		}
	}
}

// TestRecordingTxReachesTheStoreUnderneath is the delegation half at run time.
//
// A decorator that noted and returned a zero value would satisfy every book
// assertion in this package while quietly detaching the mesh from its store —
// the failure mode where the boundary check passes because nothing happens at
// all. Two probes, one write and one read: NextID has to allocate from real
// state, and GetLedger has to bring back the store's own not-found sentinel.
//
// Both legs also cover a thing the per-method test does not: View wraps its Tx
// as Update does. A read-only unit of work is exactly where a cross-entity read
// hides — reading another bank's ledger needs no write — so a View that handed
// back the bare Tx would leave the whole read side unrecorded.
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
// Parsing
// ---------------------------------------------------------------------------

// bookScopedLedgerMethods is every method on ledger.Tx that takes a book, in
// declaration order.
//
// "Takes a book" means `book BookID` as the argument right after ctx, which is
// the property that makes the decorator a decorator and not a re-architecture:
// one place per method to record, and no plumbing. A method that took a BookID
// somewhere else would break that, so this fails on it rather than skipping it —
// silently skipping is how a method ends up unwrapped.
func bookScopedLedgerMethods(t *testing.T) []string {
	t.Helper()
	_, file := parseMeshFile(t, filepath.Join("..", "ledger", "store.go"))

	var out []string
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
			it, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				t.Fatal("ledger.Tx is not an interface type")
			}
			for _, m := range it.Methods.List {
				fn, ok := m.Type.(*ast.FuncType)
				if !ok || len(m.Names) != 1 {
					continue // an embedded interface, not a method
				}
				name := m.Names[0].Name
				switch pos := bookParamIndex(fn); pos {
				case -1:
					continue // AppendAudit, ListAudit, Now: not book-scoped
				case 1:
					out = append(out, name)
				default:
					t.Errorf("ledger.Tx.%s takes its book at argument %d, not straight after ctx.\n"+
						"The recorder relies on that position; move it back, or teach this test the new rule.", name, pos)
				}
			}
		}
	}
	return out
}

// bookParamIndex is the position of a `book BookID` parameter in a flattened
// argument list, or -1.
func bookParamIndex(fn *ast.FuncType) int {
	i := -1
	for _, f := range fn.Params.List {
		id, isIdent := f.Type.(*ast.Ident)
		if len(f.Names) == 0 {
			i++
			continue
		}
		for _, n := range f.Names {
			i++
			if n.Name == "book" && isIdent && id.Name == "BookID" {
				return i
			}
		}
	}
	return -1
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
