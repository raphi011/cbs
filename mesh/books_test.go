package mesh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/testenv"
)

// ---------------------------------------------------------------------------
// The book-access recorder
// ---------------------------------------------------------------------------

// recordingStores wraps the whole SET of stores and records which ledger books
// each unit of work touched, across every institution's database.
//
// It is the second of this package's two boundary mechanisms, and it did not
// stop biting when the stores split — the two answer different questions and the
// pair is stronger than either. The interfaces in ops.go narrow by METHOD: a
// bank handler that calls SettleCycleTx does not compile. store/sqlite's
// ErrNotThisStoresBook narrows by DATABASE: a store handed a book it does not
// answer for refuses. Neither can say WHICH books an act reached, and "did this
// bank's act touch exactly its own book" is the question this file exists to
// answer.
//
// What it lost with the split is the crossing it was invented for — one bank
// reading another's ledger through a method it legitimately holds — because
// there is no longer a database in which that read could succeed. What it kept
// is everything about attribution: a unit of work reaching two books, an
// unfiltered audit read, an actor touching a book it has no business in even
// within its own store.
//
// It is test-only, and deliberately so. The production boundary is the
// interfaces plus one store per entity; this is what says the SHAPE of each act
// is what the flows in doc.go claim.
//
// It is the store SET rather than one store because there is no longer one store
// to wrap. Every handle it hands out shares this value's maps, so "which books
// did this actor touch" is still one question with one answer.
type recordingStores struct {
	inner payment.Stores

	mu    sync.Mutex
	books map[ledger.BookID]bool
	// byActor is the same set, split by the institution whose unit of work made
	// the access. See Mesh.withActor for where that identity comes from, and
	// touchedBy for what it is worth: "which books did this bank reach" is the
	// question this whole file exists to answer, and it cannot be asked of one
	// global set.
	//
	// A unit of work opened by nobody in particular — a test fixture, a seed, an
	// operator's cut-off — is counted in books and in no actor's set. That is the
	// right default: attributing it to an institution would put work no actor did
	// into that actor's ledger of crossings.
	byActor map[iso20022.BIC]map[ledger.BookID]bool
	// updates counts the WRITING units of work opened since the last reset.
	//
	// It is what lets a test say "both books moved together" rather than "both
	// books moved", which for a crossing is the whole of the claim: two entities
	// with a database each cannot commit as one, so an act that reaches two books
	// inside one Update is an act that has to become a message before they split.
	// Views are not counted — a read crosses nothing that has to commit.
	updates int
}

var (
	_ payment.Stores = (*recordingStores)(nil)
	_ payment.Store  = (*recordingStore)(nil)
)

func newRecordingStores(inner payment.Stores) *recordingStores {
	return &recordingStores{
		inner:   inner,
		books:   map[ledger.BookID]bool{},
		byActor: map[iso20022.BIC]map[ledger.BookID]bool{},
	}
}

// The three institutions' stores, each wrapped so that whatever it is asked
// about is noted against this one recorder.
func (s *recordingStores) Bank(ctx context.Context, bic iso20022.BIC) (payment.Store, error) {
	inner, err := s.inner.Bank(ctx, bic)
	if err != nil {
		return nil, err
	}
	return &recordingStore{inner: inner, rec: s}, nil
}

func (s *recordingStores) Banks(ctx context.Context) ([]iso20022.BIC, error) {
	return s.inner.Banks(ctx)
}

func (s *recordingStores) ClearingHouse() payment.Store {
	return &recordingStore{inner: s.inner.ClearingHouse(), rec: s}
}

func (s *recordingStores) CentralBank() payment.Store {
	return &recordingStore{inner: s.inner.CentralBank(), rec: s}
}

func (s *recordingStores) Reset(ctx context.Context) error { return s.inner.Reset(ctx) }
func (s *recordingStores) Close() error                    { return s.inner.Close() }

// recordingStore is one institution's store, seen through the recorder. It holds
// no state of its own: every note goes to the set above, so an assertion about
// which books an actor touched is one question over every database.
type recordingStore struct {
	inner payment.Store
	rec   *recordingStores
}

func (s *recordingStore) Update(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	s.rec.mu.Lock()
	s.rec.updates++
	s.rec.mu.Unlock()
	return s.inner.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return fn(ctx, &recordingTx{Tx: tx, rec: s.rec.noterFor(ctx)})
	})
}

func (s *recordingStore) View(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return s.inner.View(ctx, func(ctx context.Context, tx payment.Tx) error {
		return fn(ctx, &recordingTx{Tx: tx, rec: s.rec.noterFor(ctx)})
	})
}

func (s *recordingStore) Reset(ctx context.Context) error { return s.inner.Reset(ctx) }
func (s *recordingStore) Close() error                    { return s.inner.Close() }

// bookNoter is what one unit of work notes into: the store, plus the identity of
// the actor that opened it.
//
// It exists so that recordingTx's overrides can stay two statements long. Every
// one of them is `r.rec.note(book)` and TestEveryRecordingTxMethodNotesItsBookThenDelegates
// holds them to exactly that shape, so the actor cannot be an argument at the
// call site; it has to travel with the thing being called. A value, not a
// pointer: it is two words and it is never mutated.
type bookNoter struct {
	store *recordingStores
	actor iso20022.BIC
}

func (n bookNoter) note(book ledger.BookID) { n.store.note(n.actor, book) }

// noterFor reads the acting institution off the context the unit of work was
// opened with.
func (s *recordingStores) noterFor(ctx context.Context) bookNoter {
	who, _ := actorOf(ctx)
	return bookNoter{store: s, actor: who}
}

// note records one book access, against the whole store and against the actor
// that made it. It is called from actor goroutines, so it takes the lock — the
// mesh runs one goroutine per institution and they touch the same store
// concurrently.
//
// What it records is NOT rolled back with the unit of work it was called in. A
// handler that read another bank's book and then failed still read it, and a
// recorder that forgot such a read on rollback would hide exactly the crossings
// that go wrong.
func (s *recordingStores) note(actor iso20022.BIC, book ledger.BookID) {
	s.mu.Lock()
	s.books[book] = true
	if actor != "" {
		if s.byActor[actor] == nil {
			s.byActor[actor] = map[ledger.BookID]bool{}
		}
		s.byActor[actor][book] = true
	}
	s.mu.Unlock()
}

// touched is the set of books accessed since the last reset, sorted so that an
// assertion on it is a stable string rather than a map's iteration order.
func (s *recordingStores) touched() []ledger.BookID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedBooks(s.books)
}

// touchedBy is touched, narrowed to one institution.
func (s *recordingStores) touchedBy(actor iso20022.BIC) []ledger.BookID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedBooks(s.byActor[actor])
}

func sortedBooks(set map[ledger.BookID]bool) []ledger.BookID {
	out := make([]ledger.BookID, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	slices.Sort(out)
	return out
}

// unitsOfWork is how many writing units of work have been opened since the last
// reset. See recordingStores.updates.
func (s *recordingStores) unitsOfWork() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

func (s *recordingStores) reset() {
	s.mu.Lock()
	s.books = map[ledger.BookID]bool{}
	s.byActor = map[iso20022.BIC]map[ledger.BookID]bool{}
	s.updates = 0
	s.mu.Unlock()
}

// everyBook is what an access records when its book is EMPTY.
//
// It exists for ListAudit. An AuditFilter with no BookID is not a read of no
// book, it is a read of every book: store/sqlite's tx_audit.go omits the WHERE
// clause entirely (`if f.BookID != "" { … }`). Recording nothing there would
// leave open the widest crossing in the system — one call that reads every
// bank's audit trail, payloads included.
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
// is the failure this whole file exists to prevent.
//
// The over-report is unreachable in practice because every audit event the
// domain layers append names a book — and that is checked rather than asserted:
// TestEveryAuditEventTheDomainAppendsCarriesABook reads the construction sites.
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
// Almost all of payment.Tx's OWN methods are network-scoped: participants,
// payments, mandates, cycles and settlements belong to no single bank and live
// under ledger.NetworkBook. They are correctly absent from the overrides below,
// and they still leave a trace — not through the row, but through the ID the row
// needed and the audit event it wrote, both of which are taken under
// NetworkBook. See the block above the tests, which spells that out for Task 10
// and pins it with a test, because an earlier version of this comment claimed
// the exact opposite.
//
// The settlement advice is the exception, and it is the first one: it is a
// member bank's own record of a cut-off it was told about, so it carries that
// bank's book and IS recorded, exactly like a ledger or deposit row.
//
// It EMBEDS payment.Tx, so everything else is promoted untouched.
// TestRecordingTxOverridesEveryBookScopedMethod is what keeps the override set
// exactly right: one that went missing would be silently replaced by the
// promoted method, which records nothing.
type recordingTx struct {
	payment.Tx
	rec bookNoter
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

// --- payment.Tx ---
//
// Only the settlement advice. Every other method payment.Tx declares is
// network-scoped — see the comment above recordingTx — and takes no book at all.

func (r *recordingTx) PutSettlementAdvice(ctx context.Context, book ledger.BookID, a payment.SettlementAdvice) error {
	r.rec.note(book)
	return r.Tx.PutSettlementAdvice(ctx, book, a)
}

func (r *recordingTx) GetSettlementAdvice(ctx context.Context, book ledger.BookID, reference string, asset ledger.AssetCode) (payment.SettlementAdvice, error) {
	r.rec.note(book)
	return r.Tx.GetSettlementAdvice(ctx, book, reference, asset)
}

func (r *recordingTx) ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]payment.SettlementAdvice, error) {
	r.rec.note(book)
	return r.Tx.ListSettlementAdvices(ctx, book)
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
//     ListAudit(ctx, f AuditFilter) selects rows by f.BookID — store/sqlite's
//     tx_audit.go writes book_id from the event and adds `book_id = ?` to the
//     filter. The field IS the scope.
//   - payment.PutBank(ctx, b Bank) writes a network-scoped row
//     under ledger.NetworkBook whatever b.BookID says. b.BookID is a column on
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
	"PutBank": {
		Scoping: false,
		Why: "payment/store.go: banks are network-scoped and stored under ledger.NetworkBook. " +
			"Bank.BookID names the book the bank owns; it does not scope this write. " +
			"TestWritingAParticipantTouchesNoBankBook is the evidence. " +
			"The other two rows admission writes are not candidates at all and that is worth knowing " +
			"rather than rediscovering: SettlementMember and RosterEntry carry no BookID, because " +
			"neither the settlement agent nor the clearing house holds a book of the bank's.",
	},
	// A PASSENGER, and the only one: the book argument scopes this write and
	// SettlementAdvice.Book is the row's record of where it landed.
	"PutSettlementAdvice": {
		Scoping: false,
		Why: "payment/store.go: the book ARGUMENT scopes this write. The store writes book_id from that " +
			"argument and never reads a.Book at all, so it cannot answer with a book the caller did " +
			"not name. storetest's SettlementAdviceIsScopedToTheBankThatWasAdvised is the evidence: " +
			"it puts an advice whose Book field disagrees with the argument and pins that the store " +
			"files it under the argument's book and reads the field back as that book.",
	},
}

// ---------------------------------------------------------------------------
// READ THIS BEFORE WRITING TestTheCSMTouchesOnlyTheNetworkBook (Task 10)
// ---------------------------------------------------------------------------
//
// ledger.NetworkBook reaches touched() through ID ALLOCATION and AUDIT, never
// through a posting.
//
// The Put* methods for network-scoped rows — PutPayment, PutCycle,
// PutSettlement, PutMandate, PutBank — take no book and record nothing
// themselves. But no network row is written on its own: the domain allocates its
// id first, with NextID(ctx, ledger.NetworkBook, …), and writes an audit event
// under BookID: ledger.NetworkBook. Both of those ARE recorded — NextID
// positionally, AppendAudit through its struct — so a handler that only writes
// network rows records touched = [network].
//
// Measured, not assumed: OpenCycle writes one ClearingCycle, posts nothing, and
// records exactly [network]. TestWritingANetworkRowRecordsTheNetworkBook is the
// pin. The allocation sites are payment/system.go NextID(…, NetworkBook, …) for
// "bank", "mnd", "cyc", "set" and "pay", and payment/audit.go, which takes an
// "evt" id under NetworkBook and then appends the event under it.
//
// So the brief's
//
//	assertBooksTouched(t, "clearing house", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
//	    []ledger.BookID{ledger.NetworkBook})
//
// is satisfied by a CSM handler that moves a cycle along — OpenCycle,
// CloseCycle, and now AcceptAtCSMTx — with no posting required. Task 8 is what
// separated the CSM's half out: initiation used to post the debtor leg in the
// same call that took the payment into a cycle, so one handler recorded both
// the bank's book and this one. The clearing house's half now writes the
// payment and the cycle and posts nothing, which is exactly the shape this
// assertion wants — and it keeps its audit event, which is the ONLY reason
// those network rows are visible here at all.
//
// # A BANK's expected set is [NetworkBook, its own book], not [its own book]
//
// The brief's draft — TestABankHandlerTouchesOnlyItsOwnBook, now
// TestWhichBooksEachBankActuallyReaches, and see there for why it was renamed —
// wants []ledger.BookID{h.debtorBook} for the submitting bank. That is not
// satisfiable and never was: every payment id is allocated network-scoped —
// payment/system.go's NextID(ctx, ledger.NetworkBook, "pay") — and submission
// appends payment.initiated under NetworkBook. No version of creating a
// payment avoids either. Correct the draft's want list rather than the domain.
//
// The invariant that actually makes this sub-project real is narrower and does
// hold: NO BANK REACHES ANOTHER BANK'S BOOK, and the CSM reaches only this one.
// NetworkBook is not another bank's book; it is the label for rows that belong
// to no single bank, and a bank that creates or advances a payment necessarily
// touches it.
//
// And nothing in this repository EVER posts under NetworkBook. It labels
// entities that belong to no single bank; it is not a chart of accounts
// (payment/system.go, on CentralBankBook, says so), and no ledger.NewBook is
// ever bound to it. Clearing posts nothing at all. Settlement does, and in three
// places — the netting transaction in the CENTRAL BANK's book (CentralBankBook),
// the mirror leg in each participant's own book, and each creditor leg in the
// creditor's book. Those three are still the postings a cut-off makes; what
// changed at Task 15b.2 is WHO makes the second, which is now the member itself
// on the statement it was sent. None of them is NetworkBook, so a handler that
// settles contributes those books and not this one. Do not go looking for a
// NetworkBook posting: there is none to find.
//
// # This is a property of today's domain layer, not a structural invariant
//
// What makes a network-scoped write visible is that the domain happens to
// allocate an id and append an audit event under NetworkBook. Nothing enforces
// that. If Task 8's or Task 10's new CSM-side cycle-add writes its rows without
// an audit event, it records NOTHING, and TestTheCSMTouchesOnlyTheNetworkBook
// fails with exactly the empty set the OLD note wrongly predicted — for an
// entirely different reason. Whoever hits that failure needs both stories: the
// recorder is not blind to network rows, but it sees them only through the id
// and the audit event, so a network-scoped write must keep its audit event or
// the recorder cannot see it at all.
//
// A previous version of this note claimed the exact reverse — that NetworkBook
// arrived only through postings — and would have sent Task 10 hunting for a
// posting that cannot exist. It is left recorded here rather than quietly
// replaced, because the reason it was wrong is the reason this file distrusts
// unpinned prose.

// ---------------------------------------------------------------------------
// What each actor reaches
// ---------------------------------------------------------------------------

// assertBooksTouched compares one actor's set of books against the expectation,
// whole and in order.
//
// Whole, and not "contains": the claim these tests make is about the books an
// actor did NOT reach, so an assertion that only checked for the expected ones
// would pass on a handler that reached every book in the network.
func assertBooksTouched(t *testing.T, who string, got, want []ledger.BookID) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s touched %v, want %v", who, got, want)
	}
}

// TestWhichBooksEachBankActuallyReaches is sub-project 8's specification,
// measured against the code that now exists.
//
// Under a shared store, nothing stops a handler reading another entity's books;
// this is what notices. When each entity gets its own store, these assertions
// become the definition of the split rather than a check on it.
//
// It was drafted as TestABankHandlerTouchesOnlyItsOwnBook, and the rename is
// part of the finding rather than tidying — or was. As first measured, a bank
// did NOT touch only its own book: both of them below reached a book that was
// not theirs, for two separate reasons set out further down. Task 14.3 closed
// one of those two (see "# The creditor's book USED to be in it too" below);
// the other — the receiving bank's directory sweep — is still live, so the old
// name is still wrong, just for half the reason it used to be, and this stays
// TestWhichBooksEachBankActuallyReaches rather than reverting.
//
// # The submitting bank, and why NetworkBook is in its set
//
// The draft this test comes from wanted []ledger.BookID{h.debtorBook} alone.
// That is not satisfiable and never was: every payment id is allocated
// network-scoped — payment/system.go's NextID(ctx, ledger.NetworkBook, "pay") —
// and submission appends payment.initiated under NetworkBook. No version of
// creating a payment avoids either. NetworkBook is not another bank's book; it
// is the label for rows that belong to no single bank, and a bank that creates a
// payment necessarily touches it.
//
// # The creditor's book USED to be in it too, and Task 14.3 is why it no longer is
//
// This was the finding the recorder was built for, and it was NOT predicted.
// The payer's bank builds the pacs.008 it sends, and a pacs.008 names the
// payee: payment.partyTx used to read the payee's deposit account out of the
// PAYEE'S BANK'S register to get the name on it (payment/translate.go,
// partiesOf — which called itself "the ONLY part of building an outbound
// message that touches the store"). So the submitting bank read a book that
// was not its own, on the happy path, every time.
//
// It was asserted rather than papered over because it was true, and because
// what would close it was named here as a domain change and not a mesh one: a
// real payer's bank knows the payee's name because the payer typed it in, so
// InitiatePaymentRequest would have to carry the two counterparty NAMES and
// CreditTransferMessage take them from the payment instead of from the
// directory. Task 14.2 did the carrying: Payment gained DebtorDetails and
// CreditorDetails, filled at submission — the bank's own side from its own
// register, the counterparty's NAME from the instruction and, AT THE TIME, its
// BIC from the roster. Task 18a reversed that second half: the BIC comes from
// the instruction too, because a bank holds only its own row and there is
// nothing left to derive it from. See
// TestAWrongCounterpartyAgentIsRefusedByTheBankItNames, which is this
// paragraph's old pin reversed and is what makes asserting it safe.
// SubmitPaymentTx refuses an unnamed counterparty either way. Task 14.3 did the
// taking:
// partiesOf now reads nothing at all, partyTx is deleted, and
// CreditTransferMessage/DirectDebitMessage take neither a context nor a Tx —
// there is no I/O left in either to need one for. The submitting bank's set
// below lost the creditor's book the same day. What this section predicted
// would eventually close its own prediction now records that it did.
//
// # The receiving bank USED to reach every bank's book, and Task 18a is why it
// no longer does
//
// This was the second of the two crossings this test was built to find, and it
// outlived the first by four tasks. The payee's bank resolves the message BY
// ADDRESS — which is the point, and what produces AC01 for an IBAN it does not
// hold — and that resolution was a SWEEP: ResolveIdentifierTx called
// ListDepositAccountsByIdentifier once per member, so asking "whose IBAN is
// this" read every member's register, on the happy path, every time.
//
// Task 14 narrowed which PARTY was resolved and could not narrow the sweep,
// because narrowing it needs the bank to know which register is its own and
// payment.Network had no identity to answer with. Task 18a passed the identity
// in as an argument rather than waiting for it, and the receiving bank's set
// dropped to its own book alone. Task 18b is what it was waiting for: the
// register is the one belonging to this actor's own payment.Network, and there
// is no argument left (payment.ResolveIdentifierTx).
//
// It is neither NetworkBook nor the central bank's, and the absence of the first
// is worth a sentence: its half writes the payment row, which is network-scoped,
// but it appends no audit event — see AcceptInboundTx on why the payment's
// lifecycle has two facts and not three — so nothing in it allocates a network
// id.
//
// This set no longer rests on a bank being TOLD which register is its own. It
// did until Task 18b — nothing in payment refused a caller that passed somebody
// else's id, and the recorder was the only thing that would have seen it, right
// here. The identity is constructor state now (payment.Identity), so the
// register a handler can reach is fixed before any message arrives.
//
// That does not make this assertion redundant, and the distinction is the one
// ops.go draws between the two mechanisms. An identity narrows which BANK a
// handler acts as; it narrows no BookID, because every ledger.Tx method still
// takes the book as an ordinary argument. A handler that posted into another
// member's book through a method it legitimately holds would still fail here and
// nowhere else.
func TestWhichBooksEachBankActuallyReaches(t *testing.T) {
	h := newMeshHarness(t) // builds a seeded network + mesh over a recordingStore

	h.rec.reset()
	h.submitCreditTransfer(t)
	h.drain(t)

	assertBooksTouched(t, "the payer's bank", h.booksTouchedBy(h.debtorBIC),
		[]ledger.BookID{h.debtorBook, ledger.NetworkBook})
	assertBooksTouched(t, "the payee's bank", h.booksTouchedBy(h.creditorBIC),
		[]ledger.BookID{h.creditorBook})

	// Neither of them went near the central bank's book. Only settlement moves
	// reserves, and no bank settles: that is the whole distinction between
	// clearing and settlement, and it is the one book both banks must be clear of
	// at this stage.
	for _, who := range []iso20022.BIC{h.debtorBIC, h.creditorBIC} {
		if slices.Contains(h.booksTouchedBy(who), payment.CentralBankBook) {
			t.Errorf("%s reached the central bank's book during a credit transfer", who)
		}
	}
}

// TestWhichBooksEachBankReachesInAPull is the same measurement for the other
// direction, and its result is the surprise: the sets are IDENTICAL to the
// push's, bank for bank, and every reason behind them is mirrored.
//
// Measured before it was written down, and worth setting out member by member
// because "the same" is the sort of claim that is easy to state and easy to get
// wrong.
//
// # The SUBMITTING bank, which here is the payee's
//
// [creditorBook, NetworkBook] — the same two the payer's bank reaches when it
// submits a credit transfer, arrived at from the other side:
//
//   - NetworkBook, because submitting allocates the payment's id under it
//     (payment/system.go's NextID(ctx, ledger.NetworkBook, "pay")) and appends
//     payment.initiated under it. The mandate it loads to build the message is a
//     network-scoped row too, and it contributes nothing further.
//   - Its OWN book, because SubmitPaymentTx runs the creditor half for a pull:
//     the payee's account, its asset, its address, and whether it can take a
//     credit at all.
//
// A third entry used to belong here: the DEBTOR's book, which is not its own,
// mirroring the genuine crossing TestWhichBooksEachBankActuallyReaches found
// pointing the other way. Building the pacs.003 named the payer, and
// payment.partyTx read the payer's deposit account out of the PAYER'S BANK'S
// register to get the name on it. Same cause, opposite direction, and it closed
// for the same reason its push-side twin did: Task 14.2 put both counterparty
// names on the payment at submission and Task 14.3 made partiesOf read nothing,
// so DirectDebitMessage has had no store call to make this crossing with since.
//
// # The RECEIVING bank, which here is the payer's, and which POSTS
//
// Its own book, and neither NetworkBook nor the central bank's — again the same
// as the push's receiver, and this one is the more informative half. Its
// resolution is the same resolution: DirectDebitRequest resolves the DEBTOR —
// this bank's own customer, the only party a pacs.003 routed here gives it
// standing over — by address, in this bank's own register.
//
// It was every bank's book until Task 18a, for the same reason the push's
// receiver was: ResolveIdentifierTx asked every member in turn even for that one
// lookup. See TestWhichBooksEachBankActuallyReaches for the whole of why that
// took two tasks to close, and payment.DirectDebitRequest for what a wrongly
// addressed collection now meets instead.
//
// What is new is that this half MOVES MONEY and NetworkBook still does not
// appear. The debtor leg is posted here, and every id and audit event that
// posting needs is taken under the payer's bank's own book. That is the note
// above the tests made falsifiable: NetworkBook reaches the recorder through
// network-scoped id allocation and audit events and never through a posting, so
// the one handler in this package that posts on receipt is the one that proves
// it. AcceptInboundTx deliberately appends no audit event of its own — the
// payment's lifecycle has two facts and not three — which is why nothing in this
// half allocates a network id either.
// # ONE measurement, after the drain, and it distinguishes both wrong flows
//
// It is a single set per actor over the whole chain, taken after Drain, which is
// the only barrier this package has and a real one: nothing is in flight when it
// returns.
//
// The obvious worry about a single set is that it is a UNION over both roles, so
// a bank that played both might produce a union that collides with a legitimate
// one. Measured, neither of the two ways the pull arm can be got wrong survives —
// but they are caught by DIFFERENT assertions, for different reasons, and the
// difference matters more than the result:
//
//   - Route the submission to the payer's bank and it submits and then answers
//     itself. Both banks' sets give it away. The payer's union gains NetworkBook,
//     which only the submitting half ever reaches (the payment's id and its
//     initiated event), so it comes out [debtor, creditor, network] against a
//     want of [debtor, creditor]; and the payee's bank comes out empty.
//   - Relay the pacs.003 by CdtrAgt and the payee's bank answers itself. Here the
//     union really does collide: the payee's set is [debtor, creditor, network],
//     IDENTICAL to the correct flow, and that assertion passes. What catches it is
//     the other bank — the payer's never runs, and its set is empty.
//
// So there is no single mechanism behind the two. One is caught by a set that no
// single role produces, the other only by a bank that did nothing at all, and a
// third way of getting this wrong is not covered merely because these two are.
//
// Each was watched failing against the file as it stands: the first at both of
// the assertions below, the second at the payer's alone. Round 1 watched the same
// two mutations against an earlier shape of this test and of this file.
//
// # The two-phase version that was tried, and why it is not here
//
// An earlier version reset the recorder BETWEEN the submission and the drain to
// attribute each half separately, on the stated grounds that one combined
// measurement could not tell the two banks apart. That justification was FALSE —
// the single-phase measurement had already been watched failing under both
// mutations, in this task's own first round — and it is recorded rather than
// quietly dropped, because a false rationale sitting in the test that is supposed
// to BE the pin is worse than a weak assertion.
//
// The split was also unsafe as written. Mesh.Submit sends to the clearing house
// BEFORE it returns, so the moment submitDirectDebit hands back, the clearing
// house may already be relaying and the payer's bank may already be running its
// half — concurrently with the reset and with any assertion beside it. The mesh
// offers no barrier for that: Drain means "nothing is in flight", and what a
// phase boundary needs is "nothing has started".
//
// A test COULD build one, and it is worth naming rather than claiming
// impossibility. Mesh.dispatch calls m.tap before it calls the handler, and the
// harness installs the tap per test, so a tap that blocked the clearing house on
// the first pacs.003 would hold the chain at exactly that boundary. It was not
// worth doing, for the reason in the next paragraph — the only thing it would
// gate is an assertion this system is entitled to violate — and it would buy
// nothing against the first mutation either, since there the submitting half runs
// synchronously inside Submit and the payer's own set below already catches it.
//
// And the invariant the split wanted is not one this system has. "The payer's
// bank had touched nothing at the instant Submit returned" is a claim about
// TIME in a concurrent mesh, and it is not true in general — the payer's bank is
// entitled to have answered already. The claim that IS true is about
// ATTRIBUTION, which is what the union above measures.
func TestWhichBooksEachBankReachesInAPull(t *testing.T) {
	h := newMeshHarness(t)

	h.rec.reset()
	h.submitDirectDebit(t)
	h.drain(t)

	assertBooksTouched(t, "the payee's bank, submitting a collection", h.booksTouchedBy(h.creditorBIC),
		[]ledger.BookID{h.creditorBook, ledger.NetworkBook})
	assertBooksTouched(t, "the payer's bank, answering a collection", h.booksTouchedBy(h.debtorBIC),
		[]ledger.BookID{h.debtorBook})

	// Clearing a collection costs the clearing house exactly what clearing a
	// credit transfer costs it, which is the point of relaying by an address the
	// message carries: no store read to route, and nothing but network rows to
	// clear.
	assertBooksTouched(t, "the clearing house, clearing a collection", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
		[]ledger.BookID{ledger.NetworkBook})

	// And the payer's money moved without the central bank being involved. A
	// direct debit is the first flow in this package where a receiving bank
	// posts, so it is the first place that distinction could have been lost.
	for _, who := range []iso20022.BIC{h.debtorBIC, h.creditorBIC} {
		if slices.Contains(h.booksTouchedBy(who), payment.CentralBankBook) {
			t.Errorf("%s reached the central bank's book during a direct debit", who)
		}
	}
}

// TestAWrongCounterpartyAgentIsRefusedByTheBankItNames is the ROUTING half of
// the two measurements above, and it is Task 14's routing test REVERSED.
//
// # What it used to assert, and why that is gone
//
// It used to be TestAWrongCounterpartyAgentDoesNotMisroute, and what it pinned
// was a DERIVATION: SubmitPaymentTx read the counterparty's own Bank row for its
// BIC and discarded whatever the instruction carried, so a payer who typed the
// wrong bank was overruled rather than obeyed. That was measured against two
// real failures and both are worth keeping written down, because they are what a
// reader will expect this test to still be about:
//
//   - PUSH, with the payee's agent set to the PAYER's own bank. The pacs.008
//     came back to its sender, which then ANSWERED ITS OWN INSTRUCTION: the
//     payer's bank's set became [debtor, creditor, network] and the payee's bank
//     was handed nothing.
//   - PULL, with the payer's agent set to the COLLECTOR's own bank. The
//     collecting bank posted the debit in the PAYER'S BANK'S BOOK, because
//     AcceptInboundTx posts into the book of the payment's debtor participant.
//
// The derivation is gone at Task 18a, and not because the hazard stopped being
// one. It read `tx.GetBank(counterparty)` — the counterparty's own row, in the
// counterparty's own store from Task 18c — and a bank that holds only its own
// row has no answer to give. There is nowhere else to derive it FROM either: the
// roster is keyed by the BIC being derived and belongs to the clearing house, and
// this network has no IBAN-to-BIC directory service. So the address on an
// instruction is an IBAN and a BIC, which is what SEPA was before it went
// IBAN-only in 2016 and what a cross-border transfer still is today.
//
// # What makes an asserted agent safe, and it is the OTHER change in this commit
//
// Both failures above have one mechanism: the bank the message reached could
// resolve a payee it does not hold. ResolveIdentifierTx swept every member's
// register, so the wrong bank looked the creditor's IBAN up, found it at the
// RIGHT bank, and acted on somebody else's customer. Task 18a narrows that sweep
// to the resolving bank's own register — and the moment it does, a misdirected
// message has nothing to resolve and the bank that receives it says so.
//
// So the two subtests below assert the refusal rather than the correction. A
// payer who names the wrong bank does not get their payment routed for them;
// they get it back, with AC01, from the bank they named. That is the honest
// answer and it is the one a real network gives.
//
// The book sets are asserted again for one specific reason: the wrong bank must
// touch its OWN book and no other. Under the sweep it touched every bank's, which
// is precisely the crossing that made the misroute dangerous.
func TestAWrongCounterpartyAgentIsRefusedByTheBankItNames(t *testing.T) {
	t.Run("push", func(t *testing.T) {
		h := newMeshHarness(t)
		req := h.creditTransferRequest(t)
		// A payer naming their OWN bank as the payee's. Any form that asks a
		// payer to type the payee's BIC is a form that can be handed this.
		req.CreditorDetails.Agent = h.debtorBIC

		h.rec.reset()
		p, err := h.mesh.Submit(context.Background(), req)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		h.drain(t)

		// Routed as addressed. The clearing house relays on CdtrAgt with no store
		// read of its own, so the message goes where the instruction said.
		if n := h.messagesSentTo(h.debtorBIC, "pacs.008.001.08"); n != 1 {
			t.Errorf("the bank the payer named was handed %d credit transfers, want 1", n)
		}
		if n := h.messagesSentTo(h.creditorBIC, "pacs.008.001.08"); n != 0 {
			t.Errorf("the payee's real bank was handed %d credit transfers, want 0 — nothing addressed it", n)
		}

		// And refused there, because that bank does not hold the payee's IBAN.
		got := h.payment(t, p.ID)
		if got.Status != payment.Rejected {
			t.Fatalf("status = %v, want Rejected", got.Status)
		}
		if got.RejectCode != iso20022.StatusReasonIncorrectAccountNumber {
			t.Errorf("reject code = %q, want AC01 — the named bank holds no such address", got.RejectCode)
		}
		// The payer got their money back. A misdirected payment that left the
		// payer short would be worse than one that was quietly re-routed.
		if bal := h.suspense(t, h.debtorPID); bal != 0 {
			t.Errorf("clearing suspense = %d after a refusal, want 0", bal)
		}

		// The bank that was wrongly named answered out of its OWN register and
		// reached no other book. Under the sweep this set was every bank's.
		assertBooksTouched(t, "the bank the payer wrongly named", h.booksTouchedBy(h.debtorBIC),
			[]ledger.BookID{h.debtorBook, ledger.NetworkBook})

		// And the agent is RECORDED rather than overruled: what the payment says
		// about the counterparty's bank is what the instruction said.
		if got := got.CreditorDetails.Agent; got != h.debtorBIC {
			t.Errorf("stored creditor agent = %q, want the instruction's %q — an asserted agent is recorded, not replaced", got, h.debtorBIC)
		}
	})

	t.Run("pull", func(t *testing.T) {
		h := newMeshHarness(t)
		req := h.directDebitRequest(t)
		// A collector naming ITSELF as the payer's bank.
		req.DebtorDetails.Agent = h.creditorBIC

		h.rec.reset()
		p, err := h.mesh.Submit(context.Background(), req)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		h.drain(t)

		if n := h.messagesSentTo(h.creditorBIC, "pacs.003.001.08"); n != 1 {
			t.Errorf("the bank the collector named — itself — was handed %d collections, want 1", n)
		}
		if n := h.messagesSentTo(h.debtorBIC, "pacs.003.001.08"); n != 0 {
			t.Errorf("the payer's real bank was handed %d collections, want 0 — nothing addressed it", n)
		}

		got := h.payment(t, p.ID)
		if got.Status != payment.Rejected {
			t.Fatalf("status = %v, want Rejected", got.Status)
		}
		if got.RejectCode != iso20022.StatusReasonIncorrectAccountNumber {
			t.Errorf("reject code = %q, want AC01 — the named bank holds no such address", got.RejectCode)
		}
		// Nothing was collected. On a pull the receiving bank is the one that
		// posts, so a collection answered by the wrong bank must leave no debit
		// anywhere — least of all in the payer's bank's book, which is where the
		// sweep used to put it.
		if bal := h.suspense(t, h.debtorPID); bal != 0 {
			t.Errorf("the payer's bank's clearing suspense = %d, want 0 — it never saw this collection", bal)
		}

		assertBooksTouched(t, "the collector, having addressed the collection to itself",
			h.booksTouchedBy(h.creditorBIC), []ledger.BookID{h.creditorBook, ledger.NetworkBook})

		if got := got.DebtorDetails.Agent; got != h.creditorBIC {
			t.Errorf("stored debtor agent = %q, want the instruction's %q — an asserted agent is recorded, not replaced", got, h.creditorBIC)
		}
	})
}

// TestTheCSMTouchesOnlyTheNetworkBook is the assertion the note above was
// written for, and it holds: the clearing house's half writes the payment and
// the cycle and posts nothing.
//
// It reaches NetworkBook and NOTHING else — not through a posting, because
// nothing in this repository ever posts under NetworkBook, but through the id
// AcceptAtCSMTx's audit event needs and the event itself. A CSM half that
// skipped that event would record nothing at all and fail this with an empty
// set, which is a different bug wearing the same failure.
//
// Relaying costs it nothing: the pacs.008 hop reads the creditor's agent out of
// the message and routes on it, with no store read at all. A clearing house that
// looked a payment up to decide where to send it would be one that could not
// route a message about a payment it does not hold.
func TestTheCSMTouchesOnlyTheNetworkBook(t *testing.T) {
	h := newMeshHarness(t)
	h.rec.reset()
	h.submitCreditTransfer(t)
	h.drain(t)

	assertBooksTouched(t, "clearing house", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
		[]ledger.BookID{ledger.NetworkBook})
}

// TestTheCSMStillTouchesOnlyTheNetworkBookWhenItSettles extends the assertion
// above over the two things Task 12 gave this actor: reaching a cut-off, and
// the settlement conversation that follows it.
//
// Same set, and that is the finding. The clearing house now nets a batch, builds
// a pacs.009, reads two members to name them in it, and fans the answer out
// to the bank that submitted each payment — and none of that leaves NetworkBook,
// because none of it posts and every row it reads belongs to no single bank.
//
// The member lookup is the one that could have gone the other way, and until
// Task 17 it did more than it needed to: GetParticipant returned a value with
// the named bank's Ledger, Deposit and Catalogue handles attached
// (Network.bind), so a handler holding it could reach that bank's book through
// a method it legitimately had. It is GetRosterEntry now and there is no handle
// on what comes back. This measurement did not move, and that is the point: it
// was already measuring that this handler took the BIC and nothing else, which
// is why the narrowing changed no expectation in this file.
//
// That is the whole reason the recorder exists beside the interfaces in ops.go.
// csmOps could not have expressed this: the method is on it, and must be.
func TestTheCSMStillTouchesOnlyTheNetworkBookWhenItSettles(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)

	h.rec.reset()
	h.closeCycle(t)
	h.drain(t)

	assertBooksTouched(t, "the clearing house, reaching a cut-off and instructing settlement",
		h.booksTouchedBy(h.cfg.ClearingHouseBIC), []ledger.BookID{ledger.NetworkBook})
}

// TestWhichBooksTheCentralBankReachesWhenItSettles is the test Task 10 deferred,
// and the tripwire it left is what this replaces.
//
// Task 10 wrote TestClosingACycleSendsNoMessageAndTouchesNoBankBook and said in
// its own doc that it was written to FAIL the day settlement landed: the moment
// a closed cycle emits a pacs.009 the message count stops being zero, and the
// moment the central bank's actor settles, CentralBankBook appears. Both
// happened, both failures were watched, and this is what stands in its place —
// the per-actor claim that was not available then, because the cut-off ran on a
// bare context attributed to no actor and every per-actor set was empty by
// construction. Now an actor does the work, so the question has an answer.
//
// # The drafted name said the opposite of the measurement
//
// Task 10 deferred it as TestTheCentralBankTouchesOnlyTheCentralBankBook. That
// name is false, and it is renamed here for the reason
// TestWhichBooksEachBankActuallyReaches was renamed: an assertion whose name
// claims more than it measures is worse than no name, because the name is the
// string a failure prints and a reader greps for.
//
// The central bank used to reach EVERY book in this network when it settled, and
// Task 10's own note said why without following the sentence through: settlement
// posted in three places, and only one of them was the central bank's.
//
//   - CentralBankBook, for the netting transaction — the one posting that is
//     genuinely this institution's own, moving reserves between the members'
//     settlement accounts in its own chart of accounts.
//   - Each PARTICIPANT's book, twice over. The mirror leg moves that bank's
//     suspense against its reserve so the suspense returns to zero, and the
//     creditor leg releases each payee's funds out of its bank's suspense into
//     the payee's account. Both were postings in a member bank's ledger, made by
//     the central bank's handler.
//   - NetworkBook, for the settlement row's id and the audit events — the same
//     route every network-scoped write takes here, and never through a posting.
//     See the note above the tests.
//
// # BOTH member legs have left, and that is what this set now measures
//
// Task 15b.2 turned the mirror leg into a camt.053: the central bank sends each
// member a statement of its own reserve account, and the member posts its own
// mirror leg from it (payment.PostSettlementAdviceTx, bank.receiveStatement).
//
// Task 15b.3 took the CREDITOR leg the same way. The clearing house's ACSC
// fan-out now reaches the creditor's bank as well as the submitter, and that
// bank releases its own customer's funds out of its own suspense
// (payment.PostCreditorLegTx, bank.receiveStatus). So no member's book is in
// this set at all: SettleCycleTx reads the cycle, the roster and its own book,
// and no payment.
//
// # What that measurement is evidence FOR
//
// The two books that remain are the central bank's own and the network's. The
// claim that a settlement agent posts in a member's ledger is gone entirely,
// and it is gone by measurement rather than by assertion — this expectation was
// written at Task 10 to fail the day it stopped being true, and it did.
//
// What it cost is stated at the domain call rather than hidden here: the ledger's
// refusal of an overdrawn net payer came from the mirror leg, in the member's own
// book, where "Reserve at Central Bank" is an Asset account. A member's
// settlement account HERE is a Liability, which ledger.Book.checkSufficientBalance
// does not guard, so moving the leg without moving the check would have settled a
// cycle whose net payer was short. SettleCycleTx now refuses that explicitly; see
// TestANetPayerWhoCannotCoverIsRejectedOnTheInstruction, which is the measurement
// that would fail if it were ever deleted.
func TestWhichBooksTheCentralBankReachesWhenItSettles(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	h.rec.reset()
	h.closeCycle(t)
	h.drain(t)

	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("the payment is %v, want Settled — this test measures a settlement that happened", got.Status)
	}
	assertBooksTouched(t, "the central bank, settling a cycle",
		h.booksTouchedBy(h.cfg.CentralBankBIC),
		[]ledger.BookID{payment.CentralBankBook, ledger.NetworkBook})
}

// TestEachBankBooksItsOwnSettlementAndNoOtherBooks is the counterpart of the
// measurement above: across a cut-off each member bank reaches its OWN book and
// no other.
//
// TestWhichBooksEachBankActuallyReaches makes a claim about a credit transfer up
// to acceptance, and TestWhichBooksEachBankReachesInAPull about a collection.
// Neither could say anything about settlement, because at Task 10 there was
// none. Now there is, and it is the moment at which reserves actually move — so
// it is the point in the system's life where a bank reaching into the central
// bank's book, or into the other bank's, would be least visible and most wrong.
//
// # The claim this used to make, and why it stopped being true
//
// It was TestNeitherBankTouchesABookWhileTheCycleSettles, and it asserted the
// empty set for both banks: "settlement is not a bank's work". That was a true
// measurement of a system in which the settlement agent posted every member's
// mirror leg inside its own unit of work — the banks touched nothing because
// nothing was left for them to do. Task 15b.2 moved the mirror leg to the member
// that owns the book, so both banks now post, and the old name claims the exact
// opposite of what this measures. It is renamed rather than adjusted in place for
// the reason TestWhichBooksEachBankActuallyReaches was renamed: the name is the
// string a failure prints and a reader greps for, and one that outran its code is
// worse than no name at all.
//
// # What each bank reaches, and what is ABSENT
//
// Its own book, and only its own. The payer's bank posts once, for the mirror
// leg — suspense against reserve, so the suspense returns to zero. The payee's
// bank posts that leg too and then a second one: Task 15b.3 made the CREDITOR
// leg the payee's bank's act, so it also releases its own customer's funds out
// of that same suspense.
//
// The central bank's book is absent from both, which is the claim the old test
// made most sharply and this one still makes: a member books its own mirror leg
// from a statement, and never reads the account the statement is about.
//
// # The asymmetry in NetworkBook is a measurement, not noise
//
// NetworkBook is in the payee's bank's set and not the payer's, and the reason
// is which of them writes a PAYMENT row. PostSettlementAdviceTx writes a
// book-scoped advice row and appends no audit event, so a member's mirror leg
// allocates no network id — that is why the payer's bank's set is its own book
// alone. PostCreditorLegTx transitions the payment to Settled and appends
// payment.settled, and a payment is a network-scoped row, so the payee's bank
// reaches NetworkBook through the id that event needed (see the note above the
// tests).
//
// So the asymmetry says the payment row is still ONE row shared by the whole
// network rather than each institution's own record. Task 18 is where it
// becomes per-entity, and then both banks will touch their own and neither this
// one. Until then, a NetworkBook that appeared in the PAYER's set would mean a
// bank had written a network row it has no business writing; read what touched
// it before changing this expectation.
//
// # What these sets do NOT rule out, measured rather than assumed
//
// A bank that merely RE-READ the payment row would still show only its own book,
// and the reason is the recorder's and not this test's: a network-scoped row
// reaches touched() through the id its write allocated and the audit event that
// write appended, never through the read itself (see the note above the tests).
// So GetPayment and GetRosterEntry record nothing at all. The claim here is
// about BOOKS — no bank reads or writes any book but its own while a cycle settles —
// and it is not a claim that a bank learns nothing.
//
// It measures over the cut-off ONLY, resetting after the submission has drained,
// so a book reached earlier can neither satisfy nor spoil it.
func TestEachBankBooksItsOwnSettlementAndNoOtherBooks(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)

	h.rec.reset()
	h.closeCycle(t)
	h.drain(t)

	assertBooksTouched(t, "the payer's bank, booking its own settlement",
		h.booksTouchedBy(h.debtorBIC), []ledger.BookID{h.debtorBook})
	assertBooksTouched(t, "the payee's bank, booking its own settlement and paying its customer",
		h.booksTouchedBy(h.creditorBIC), []ledger.BookID{h.creditorBook, ledger.NetworkBook})
}

// TestWhichBooksAReturnReaches is the last flow's measurement, and it has just
// stopped being the most lopsided set in this file.
//
// Measured, then written down. Every sentence below is about the run this test
// makes and no wider.
//
// # It used to say ONE actor reached every book, and that is the reversal
//
// The set asserted here was h.allBooks(): the central bank reached all four,
// two of which were member banks'. That was an accurate measurement of
// payment.ReturnPaymentTx, which ran the whole return in one unit of work —
// both customer legs and both reserve mirrors among them — and which Task 16e
// deleted. What is left at this actor is payment.SettleReturnTx: one posting,
// reversing the movement between the two members' settlement accounts, in the
// central bank's own book.
//
// # The central bank reaches ONE book, and the absence of NetworkBook is the
// second measurement
//
// [CentralBankBook], and nothing else. NetworkBook is not there and its absence
// was not predicted — the plan for this task guessed it would be, by analogy
// with the cut-off, and the measurement says otherwise. It is worth the
// sentence because the reason is exact: a network-scoped row reaches this
// recorder through the id its write ALLOCATED and the audit event that write
// APPENDED, never through a read (see the note above the tests), and
// SettleReturnTx writes no row and appends no event. It reads the roster by
// BIC, posts once, and reads two balances back.
//
// TestWhichBooksTheCentralBankReachesWhenItSettles measures [CentralBankBook,
// NetworkBook] for the cut-off, and the difference between the two is the whole
// of the point: a cut-off writes a Settlement row and a return has none to
// write. The return's own durable trace is the idempotency key on the posting
// above, which is what makes a redelivery ErrReturnAlreadySettled — see
// payment.SettleReturnTx, which records that it needs no row.
//
// # The clearing house reaches nothing, and that is a real measurement
//
// It runs TWO handlers over this window and now sends THREE messages — it
// carries the pacs.004 to the settlement agent, addresses the answer back to the
// bank that asked, and relays the pacs.004 on to the other bank — and still comes
// out empty. The first hop reads no store at all: a return's first destination
// follows from the message DEFINITION. The third reads none either: the other
// bank is whichever of OrgnlTxRef's two agents the message did not come from,
// which is on the message. The middle one reads the payment, its scheme and a
// roster entry, and none of those is a book access: payments are network-scoped
// rows, and a read records nothing. The member lookup is the one that could have
// gone the other way — until Task 17 it handed back the named bank's live Ledger
// and Deposit handles — and this was already measuring that the handler took the
// BIC off it and nothing else, which is why the narrowing to GetRosterEntry left
// this expectation where it was.
//
// What an empty set here does NOT say is that the clearing house learned
// nothing: it read the payment, and it is now the only actor in this package
// that remembers anything between messages (csm.held). The claim is about BOOKS.
//
// An actor that DID reach into a bank's ledger over this window is not
// invisible to these assertions, and that is measured rather than assumed: a
// clearing-house half that listed the returning bank's ledgers before
// forwarding the answer comes out holding that BANK's book and fails the second
// assertion below.
//
// It used to name the book — "comes out [bank_3]" — and that had gone stale
// twice over: the harness's second bank is numbered differently every time an
// allocation moves in the network's shared id counter, most recently when
// admission became four acts. A book id here is a fact about the seed's
// arithmetic and not about what this test measures, which is WHOSE book was
// reached.
//
// The two banks have moved to TestEachBankBooksItsOwnReturnAndNoOtherBooks,
// which is where the assertion that they touch nothing turned into an assertion
// that each touches its own.
//
// It measures over the RETURN only, resetting after the settlement it starts
// from has drained, so no book reached while the payment was being carried can
// satisfy or spoil it.
func TestWhichBooksAReturnReaches(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	h.rec.reset()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.drain(t)

	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("the payment is %v, want Returned — this test measures a return that happened", got.Status)
	}
	assertBooksTouched(t, "the central bank, settling a return",
		h.booksTouchedBy(h.cfg.CentralBankBIC), []ledger.BookID{payment.CentralBankBook})
	assertBooksTouched(t, "the clearing house, carrying a return, its answer and the return again",
		h.booksTouchedBy(h.cfg.ClearingHouseBIC), nil)
}

// TestEachBankBooksItsOwnReturnAndNoOtherBooks is the counterpart of the
// measurement above: across a return each member bank reaches its OWN book and
// no other.
//
// # The claim this used to make, and why it stopped being true
//
// These two assertions lived inside TestWhichBooksAReturnReaches and said the
// two banks touched NOTHING: a returning bank posted no leg, and the payer's
// bank was handed no message at all. Both banks now post, so those assertions
// have moved out under a name that says what they measure. It is
// TestEachBankBooksItsOwnSettlementAndNoOtherBooks's rename for the same reason
// — a name that claims the opposite of its measurement is worse than no test —
// and it is the second time this package has hit it on the same kind of change.
//
// # What each bank reaches, and which one carries NetworkBook
//
// The PAYEE's bank is the returner on a push. It reaches [creditorBook] and only
// that: it posts the clawback out of its own customer's account into its own
// clearing suspense BEFORE it sends the pacs.004 (bank.returnPayment), and books
// its reserve mirror in the same book from the camt.053. The central bank's book
// is absent, which is the claim the old assertion made most sharply and this one
// still makes: a bank moves its own money, and never reads the settlement
// account the statement is about.
//
// The PAYER's bank reaches [debtorBook, NetworkBook]. Its own book for the same
// two reasons — the mirror leg from the statement, then the refund out of that
// suspense into its customer's account — and NetworkBook for the reason the
// settlement measurement records: it posts the SECOND customer leg, which is
// what takes the payment to Returned, and a status transition writes the payment
// row and appends payment.returned. A network row is reached through the id that
// write allocated and the event it appended.
//
// So which bank carries NetworkBook is not a property of being the payer's bank.
// It is a property of posting the leg that finishes the return, and on a PULL
// those are the other way round. The asymmetry says the payment row is still ONE
// row shared by the whole network; Task 18 is where it becomes per-entity, and
// then neither bank will reach it.
//
// # What these sets do NOT rule out
//
// A bank that merely RE-READ the payment row would still show only its own book,
// because a network-scoped row is invisible to this recorder on a read. The
// claim is about BOOKS — no bank reads or writes any book but its own while a
// return runs — and it is not a claim that a bank learns nothing.
//
// It measures over the RETURN only, for the reason above.
func TestEachBankBooksItsOwnReturnAndNoOtherBooks(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)

	h.rec.reset()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.drain(t)

	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("the payment is %v, want Returned — this test measures a return that happened", got.Status)
	}
	assertBooksTouched(t, "the payee's bank, clawing its own customer back before it asks",
		h.booksTouchedBy(h.creditorBIC), []ledger.BookID{h.creditorBook})
	assertBooksTouched(t, "the payer's bank, refunding its own customer after finality",
		h.booksTouchedBy(h.debtorBIC), []ledger.BookID{h.debtorBook, ledger.NetworkBook})
}

// TestWhichBooksAdmissionReaches is the counterpart the sub-project's Tasks
// table asks for, and the one measurement in this file whose subject is not a
// payment.
//
// One call used to reach three books: the bank's own, the central bank's and the
// network's, all inside a single unit of work at a caller that was no
// institution at all. Three institutions reach three sets now, and the point is
// not that the total is the same — it is that no actor is in more than one
// bank's book, and that the two institutions that used to be reached INTO now
// reach their own.
//
// # The measured want-lists, and where the plan for this task was wrong
//
// The plan predicted:
//
//	the joining bank    [its own book, NetworkBook]
//	the central bank    [CentralBankBook]
//	the clearing house  [NetworkBook]
//
// Two of the three are what the recorder says. The CENTRAL BANK's is not: it is
// [CentralBankBook, NetworkBook], and the reason is the note above the tests in
// this file. A network-scoped write reaches this recorder through the id it
// ALLOCATED and the audit event it APPENDED, never through the row itself — and
// OpenSettlementAccountTx does both: it draws an id before the read its
// idempotency is decided from (payment.admissionSequenceTx) and appends
// settlement_account.opened afterwards. Neither is optional. Drop the event and
// the settlement account exists in no immutable record; drop the id and the act
// loses the ordering that made its idempotency its own rather than the store's —
// see payment.admissionSequenceTx, which measures what that is worth here.
//
// So the correction is the plan's and not the domain's, exactly as Task 16's
// return measurement was — that one predicted [CentralBankBook, NetworkBook] and
// measured [CentralBankBook], for the mirror-image reason. Both times the
// question is the same one: does this act write a row of the network's, or not.
//
// # What each set says
//
// The JOINING BANK reaches its own book and NetworkBook. Its own, because
// founding builds a chart of accounts, four internal accounts per asset and a
// product; NetworkBook, because the bank's id and two audit events are drawn
// there. That is Mesh.Admit's synchronous half plus the handler that records the
// acknowledgement, and the two are the same actor.
//
// The CENTRAL BANK reaches CentralBankBook and NetworkBook, and never a bank's.
// It opens a Liability in its own book and writes its own member row; the
// settlement reference it produces reaches the bank as a MESSAGE, which is the
// whole of what changed.
//
// The CLEARING HOUSE reaches NetworkBook alone. It writes one roster row and
// posts nothing, which is what it does on every other flow in this package.
//
// # And no institution reaches another bank's book
//
// Asserted separately, because that is the invariant the sub-project is for and
// the set equalities above do not state it in a form that survives a third bank
// in the fixture.
func TestWhichBooksAdmissionReaches(t *testing.T) {
	h := newMeshHarness(t)

	// After the fixture's own two admissions, so what is measured is one
	// admission and not three.
	h.rec.reset()
	joiner, err := h.mesh.Admit(context.Background(), "Nordhaven Bank", joinerBIC, euroOnly)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.drain(t)
	if got := h.getBank(t, joiner.ID); got.Status != payment.BankMember {
		t.Fatalf("the bank is %q; this test measures an admission that finished", got.Status)
	}

	assertBooksTouched(t, "the joining bank, founding itself and recording what it was told",
		h.booksTouchedBy(joinerBIC), []ledger.BookID{joiner.BookID, ledger.NetworkBook})
	assertBooksTouched(t, "the central bank, opening a settlement account in its own book",
		h.booksTouchedBy(h.cfg.CentralBankBIC), []ledger.BookID{payment.CentralBankBook, ledger.NetworkBook})
	assertBooksTouched(t, "the clearing house, writing a routing entry",
		h.booksTouchedBy(h.cfg.ClearingHouseBIC), []ledger.BookID{ledger.NetworkBook})

	// No institution went near a bank's book but that bank itself. The two
	// incumbents are in the fixture and are not party to this admission at all,
	// so their books are the sharpest form of the claim.
	for _, who := range []iso20022.BIC{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, joinerBIC} {
		for _, book := range []ledger.BookID{h.debtorBook, h.creditorBook} {
			if slices.Contains(h.booksTouchedBy(who), book) {
				t.Errorf("%s reached %s during an admission it is not a party to", who, book)
			}
		}
	}
	if slices.Contains(h.booksTouchedBy(joinerBIC), payment.CentralBankBook) {
		t.Error("the joining bank reached the central bank's book; the settlement account is opened for it, not by it")
	}
}

// TestTakingCashInReachesOnlyTheBanksOwnBook is the successor to
// TestFundingAReserveReachesTwoBooks, and it is that measurement turned inside
// out.
//
// The old one pinned crossing 6 as a fact: Network.DepositTx posted the funding
// bank's reserve mirror AND the central bank's matching leg in one unit of work,
// because a deposit WAS the bank placing cash on reserve, and there was no vault
// and no lodgement to route it through. It was the one measurement in this file
// expected to pass then and to fail the day the stores split. Task 18a is where
// it fails, and it failed on exactly the two assertions that named the crossing —
// the book set shrank to the bank's own, and no transaction landed in the central
// bank's book — which is why it is rewritten here rather than edited.
//
// What replaces it asserts the crossing is GONE, and asserts it three ways,
// because "reaches one book" is a claim two different bugs could satisfy.
//
// # No instrument this sub-project had could have found the original
//
// That reason is unchanged and still worth keeping, because it is why this test
// is shaped as it is. The recorder attributes a book to the actor whose unit of
// work reached it, and taking cash in never becomes a message: it arrives at
// Network.Deposit from an operator or from a fixture, with no institution behind
// it. So booksTouchedBy has nothing to narrow by, and every assertion built on it
// — every other measurement in this file — is blind to this call by construction.
// It drives the recorder directly instead, the way
// TestWritingAParticipantTouchesNoBankBook does, and reads the whole-store set.
//
// # Why a book set alone would not be enough
//
// A read touches a book exactly as a write does, and the converse trap is the one
// that matters now: a deposit that posted NOTHING AT ALL would also touch one
// book and would also leave the central bank's alone. So the balance is asserted
// too. Vault cash rising by the amount paid in is what says the money went
// somewhere, and it is the entry that did not exist before this task — the debit
// used to be to Reserve at Central Bank.
//
// The transaction count in the central bank's book stays, with its sense
// reversed. It is what catches the halfway state where the posting is removed and
// the READ is not: centralBankAssetsAccountTx used to run a line earlier and
// would still have put CentralBankBook in the set. It is gone, and a zero here is
// what says so.
//
// One unit of work, still, and it now means the opposite of what it used to.
// There it said the two books move together, and two units would have meant the
// crossing was already split. Here there is one book to move, and more than one
// unit of work would mean a deposit had acquired a second act — which is what a
// LODGEMENT is, and a lodgement is not this call.
func TestTakingCashInReachesOnlyTheBanksOwnBook(t *testing.T) {
	h := newMeshHarness(t)

	// Both read before the recorder is cleared, so this fixture's own reads are
	// not part of the measurement.
	before := h.centralBankTransactionCount(t)
	vaultBefore := h.vaultCash(t, h.debtor.ID)

	// The fixture's payer, funded again. Which account it is does not matter to
	// what this measures — every deposit takes the same two legs — and reusing
	// the fixture's keeps the call under test the only thing in the measurement.
	const amount = ledger.Amount(100_000)
	h.rec.reset()
	if err := h.bank(h.debtor.BIC).Deposit(context.Background(), h.debtor.ID, h.debtorAcct.ID, amount, "cash in"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// Both taken before anything else reads the store, for the same reason.
	touched, units := h.rec.touched(), h.rec.unitsOfWork()

	assertBooksTouched(t, "taking cash in", touched, []ledger.BookID{h.debtorBook})
	if got := h.centralBankTransactionCount(t) - before; got != 0 {
		t.Errorf("taking cash in wrote %d transactions in the central bank's book, want 0; "+
			"a customer handing over notes is not a movement on a reserve account", got)
	}
	if units != 1 {
		t.Errorf("taking cash in opened %d units of work, want 1; moving the cash onward is a "+
			"LODGEMENT and a separate act, but paying it in is one institution's own posting", units)
	}
	// The book set and the counts above are all satisfied by a deposit that posts
	// nothing whatsoever. This is what says the money arrived, and it names the
	// account that makes the deposit one institution's act.
	if got, want := h.vaultCash(t, h.debtor.ID)-vaultBefore, amount; got != want {
		t.Errorf("taking cash in raised vault cash by %d, want %d; cash paid in over the counter "+
			"is cash the bank is holding", got, want)
	}
}

// TestALodgementIsTwoBooksInTwoUnitsOfWork is the other half of the crossing, and
// it is the measurement that says the crossing was CLOSED rather than merely
// moved.
//
// TestTakingCashInReachesOnlyTheBanksOwnBook says the deposit stopped reaching the
// central bank's book. On its own that is satisfied by a system in which reserves
// can no longer be funded at all, which is not a fix. This is the other side:
// reserves still get funded, the same two pairs of entries still land in the same
// two books, and what changed is that they land in TWO UNITS OF WORK with a
// message between them.
//
// # The three assertions, and why each is a different claim
//
// The recorder attributes a book to the actor whose unit of work reached it, so
// the first two are per-ACTOR sets and this is what a crossing looks like when it
// has become a conversation:
//
//   - The lodging bank reaches its OWN book and no other. Before this task it
//     reached the central bank's too, inside its own transaction, and that WAS
//     crossing 6.
//   - The central bank reaches its own book. It is a separate actor now, doing its
//     own posting on its own goroutine, because a camt.050 arrived.
//
// The third is the unit-of-work count, and it is the one that would catch a
// "conversation" that was really still one transaction. TestFundingAReserveReaches-
// TwoBooks asserted units == 1 and called that the crossing; here more than one is
// the POINT, and exactly one would mean the split had not happened.
//
// # The balances are asserted too, for the deposit measurement's reason
//
// A book set is satisfied by a lodgement that posts nothing. So this checks that
// the vault went down, the bank's own reserve mirror went up, and — separately —
// that the CENTRAL BANK's record of the same reserve went up. The last is the one
// that matters most: it is read from the settlement agent's own book, which is
// what settlement reads, and it is the whole point of the message having been
// sent. A lodgement that raised only the bank's mirror would leave a bank that
// believes it can settle and cannot.
func TestALodgementIsTwoBooksInTwoUnitsOfWork(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	// A fresh deposit, so there is cash in the vault that has not been lodged.
	// The fixture has already lodged its own funding, which is what every other
	// test in this package needs; this one wants an unlodged balance to move.
	const amount = ledger.Amount(75_000)
	if err := h.bank(h.debtor.BIC).Deposit(ctx, h.debtor.ID, h.debtorAcct.ID, amount, "cash in"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	vaultBefore := h.vaultCash(t, h.debtor.ID)
	mirrorBefore := h.reserveMirror(t, h.debtor.ID)
	cbBefore, err := h.cb().ReserveBalance(ctx, h.debtor.ID, "EUR")
	if err != nil {
		t.Fatalf("ReserveBalance: %v", err)
	}

	h.rec.reset()
	if _, err := h.mesh.Lodge(ctx, h.debtor.ID, "EUR", amount); err != nil {
		t.Fatalf("Lodge: %v", err)
	}
	h.drain(t)
	units := h.rec.unitsOfWork()

	assertBooksTouched(t, "the lodging bank", h.booksTouchedBy(h.debtorBIC),
		[]ledger.BookID{h.debtorBook})
	assertBooksTouched(t, "the central bank, crediting a reserve on a lodgement",
		h.booksTouchedBy(h.cfg.CentralBankBIC),
		[]ledger.BookID{payment.CentralBankBook})
	if units < 2 {
		t.Errorf("a lodgement opened %d units of work, want at least 2; the crossing closing IS that "+
			"the two books no longer move together, and one unit of work would mean they still do", units)
	}

	if got, want := vaultBefore-h.vaultCash(t, h.debtor.ID), amount; got != want {
		t.Errorf("the lodgement took %d out of the vault, want %d", got, want)
	}
	if got, want := h.reserveMirror(t, h.debtor.ID)-mirrorBefore, amount; got != want {
		t.Errorf("the lodgement raised the bank's own reserve mirror by %d, want %d", got, want)
	}
	cbAfter, err := h.cb().ReserveBalance(ctx, h.debtor.ID, "EUR")
	if err != nil {
		t.Fatalf("ReserveBalance: %v", err)
	}
	if got, want := cbAfter-cbBefore, amount; got != want {
		t.Errorf("the central bank's record of the reserve rose by %d, want %d; without this the bank "+
			"believes it can settle and the book settlement reads says otherwise", got, want)
	}
}

// TestABankCannotLodgeCashItDoesNotHold is the ledger doing the work rather than
// a guard in payment, and it is here because the alternative was a second copy of
// a rule the ledger already states.
//
// Vault Cash is an Asset, and ledger.Book guards Asset accounts against going
// negative. So a lodgement bigger than the vault is refused with
// ledger.ErrInsufficientBalance — which payment's borrowedReasons already maps to
// AM04, "the account cannot cover this" — and payment.LodgeReservesTx deliberately
// makes no check of its own.
//
// What this pins is that the refusal BINDS: nothing is posted, so the bank's
// reserve mirror does not move and no camt.050 goes out. A bank that could lodge
// cash it did not hold would be creating central-bank money out of nothing.
func TestABankCannotLodgeCashItDoesNotHold(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	vault := h.vaultCash(t, h.debtor.ID)
	mirrorBefore := h.reserveMirror(t, h.debtor.ID)
	mark := h.messagesSeen()

	_, err := h.mesh.Lodge(ctx, h.debtor.ID, "EUR", vault+1)
	if !errors.Is(err, ledger.ErrInsufficientBalance) {
		t.Fatalf("lodging more than the vault holds = %v, want ledger.ErrInsufficientBalance", err)
	}
	if got := h.vaultCash(t, h.debtor.ID); got != vault {
		t.Errorf("the vault holds %d after a refused lodgement, want %d unchanged", got, vault)
	}
	if got := h.reserveMirror(t, h.debtor.ID); got != mirrorBefore {
		t.Errorf("the reserve mirror moved to %d on a refused lodgement, want %d unchanged", got, mirrorBefore)
	}
	if sent := h.messagesSeen() - mark; sent != 0 {
		t.Errorf("a refused lodgement put %d messages on the wire, want none", sent)
	}
}

// TestAFoundedBankCanTakeCashBeforeItHasJoinedAnything is the domain half of the
// same change, and it is a reversal rather than a new rule.
//
// DepositTx used to refuse a bank whose settlement reference was empty, with
// ErrSettlementMemberNotFound and the sentence "is founded and not yet admitted,
// so it has no reserve to fund". That was an accurate description of the code —
// the posting it was about named that account — and a false statement about
// banking. A bank's counter has nothing to do with its central bank account.
//
// So this drives a bank that has been FOUNDED and admitted to nothing, and pays
// cash in. It is the sharpest form of the claim available: there is no settlement
// account anywhere in the system for this bank, so a deposit that still wanted
// one could not possibly succeed by accident.
//
// It deliberately does NOT drain. admit returns a Founded bank because the
// scheme's answer arrives as a message, so leaving the queue alone is what keeps
// this bank in the state the test is about.
func TestAFoundedBankCanTakeCashBeforeItHasJoinedAnything(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	founded := h.admit(t, "Solo Bank", "SOLODEFFXXX", []ledger.AssetCode{"EUR"})
	if accts, err := founded.AccountsFor("EUR"); err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	} else if accts.Settlement != "" {
		t.Fatalf("Settlement = %q, want it empty; a founded bank has no settlement account", accts.Settlement)
	}

	acct := h.openCustomer(t, founded, "Sole Depositor", "EUR", 0, "DE89370400440532013099")
	if err := h.bank(founded.BIC).Deposit(ctx, founded.ID, acct.ID, 250_00, "cash over the counter"); err != nil {
		t.Fatalf("Deposit at a founded bank: %v; a founded bank can open its doors and take money", err)
	}
	if got, want := h.balance(t, founded.ID, acct.ID), ledger.Amount(250_00); got != want {
		t.Errorf("the depositor's balance is %d, want %d", got, want)
	}
	if got, want := h.vaultCash(t, founded.ID), ledger.Amount(250_00); got != want {
		t.Errorf("the bank's vault cash is %d, want %d", got, want)
	}
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
//
// PASSENGERS count too: a struct-carried book in a method that already takes its
// book positionally (PutSettlementAdvice) is decided here like any other. It
// looks harmless — the override notes argument 1 and that is the scope — but
// "and that is the scope" is precisely the claim being made, and a claim nobody
// has to write down is a claim nobody has to back.
func TestEveryStructCarriedBookIsDecided(t *testing.T) {
	candidates := map[string]bookMethod{}
	for _, m := range txBookCandidates(t) {
		if m.Carry == bookInsideTheArg || m.PassengerField != "" {
			candidates[m.Name] = m
		}
	}
	if len(candidates) == 0 {
		t.Fatal("found no struct-carried book arguments at all; the parser is wrong, not the table")
	}

	for name, m := range candidates {
		arg, field := m.Arg, m.Field
		if m.PassengerField != "" {
			arg, field = m.PassengerArg, m.PassengerField
		}
		decision, ok := structCarriedBooks[name]
		if !ok {
			t.Errorf("%s (%s.Tx) carries a book in %s.%s and structCarriedBooks does not decide it.\n"+
				"Say whether that field scopes the operation — whether the store reads or writes\n"+
				"another book because of it — and if it does not, say why and back it with a test.",
				name, m.Pkg, arg, field)
			continue
		}
		if !decision.Scoping && strings.TrimSpace(decision.Why) == "" {
			t.Errorf("structCarriedBooks[%q] excludes the method without saying why.\n"+
				"An exclusion nobody can disprove is how ListAudit stayed unrecorded.", name)
		}
		// A passenger that SCOPES would mean the method has two scopes and the
		// override records one of them. There is no correct decorator for that
		// shape, so it is not a decision this table may express.
		if m.PassengerField != "" && decision.Scoping {
			t.Errorf("structCarriedBooks[%q] says %s.%s scopes the operation, but %s already takes its\n"+
				"book at argument 1 and the override records only that one. If the field really chooses\n"+
				"a book, this method has two scopes and no two-statement override can be right.",
				name, arg, field, name)
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
// The claim is that Bank.BookID is a column on a network-scoped row, not
// the scope of the write. So writing a participant that NAMES a bank's book must
// leave that book empty — and this reads the book back through the store to say
// so, rather than trusting the recorder that is itself under test.
//
// If PutBank ever did write into p.BookID, this fails and the entry in
// structCarriedBooks becomes wrong at the same moment, which is what makes the
// exclusion a claim about the code rather than about the author's confidence.
func TestWritingAParticipantTouchesNoBankBook(t *testing.T) {
	clock := func() time.Time { return testTime }
	rec := newRecordingStore(testenv.New(t, clock).Payment())
	ctx := context.Background()
	victim := ledger.BookID("bank_verde")

	if err := rec.Update(ctx, func(ctx context.Context, tx payment.Tx) error {
		return tx.PutBank(ctx, payment.Bank{
			ID:        "p_aurora",
			Name:      "Aurora Bank",
			BIC:       "AURODEFFXXX",
			BookID:    victim,
			CreatedAt: testTime,
		})
	}); err != nil {
		t.Fatalf("PutBank: %v", err)
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
		p, err := tx.GetBank(ctx, "p_aurora")
		if err != nil {
			return err
		}
		if p.BookID != victim {
			t.Errorf("participant came back with BookID %q, want %q — the field is stored, it just does not scope the write", p.BookID, victim)
		}
		return nil
	}); err != nil {
		t.Fatalf("GetBank: %v", err)
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
// apart from the book, so many of these calls fail, and returning a sentinel of
// the probe's own means Update hands back exactly the error the callback
// returned — asserted below — rather than whatever the store made of a bad
// write. Under store/pg the reason was sharper still, because a failed statement
// there aborted the transaction and every later one with it; under SQLite the
// statement rolls back and the transaction survives, so the discipline is now
// about what this test READS rather than about what it would otherwise break.
// The store's answer to the call itself is still discarded, on purpose: the book
// is noted BEFORE the call goes through, which is what makes every method
// reachable without a fixture apiece.
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
// TestWhichBooksEachBankActuallyReaches would have passed over it.
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

// TestWritingANetworkRowRecordsTheNetworkBook pins the note above, which the
// previous round got backwards.
//
// OpenCycle is the smallest network-scoped write there is: one ClearingCycle
// row, no posting anywhere. If NetworkBook only arrived through postings — as
// the old note claimed — this would record nothing. It records [network],
// because the cycle's id comes from NextID under NetworkBook and its audit event
// is appended under it.
func TestWritingANetworkRowRecordsTheNetworkBook(t *testing.T) {
	clock := func() time.Time { return testTime }
	rec := newRecordingStore(testenv.New(t, clock).Payment())
	net := payment.NewNetworks(rec, clock).ClearingHouse()

	if _, err := net.OpenCycle(context.Background(), payment.SchemeSEPACT); err != nil {
		t.Fatalf("OpenCycle: %v", err)
	}
	if got := rec.touched(); !slices.Equal(got, []ledger.BookID{ledger.NetworkBook}) {
		t.Errorf("opening a cycle touched %v, want [%s].\n"+
			"A network-scoped write records its book through NextID and AppendAudit, not through a posting.",
			got, ledger.NetworkBook)
	}
}

// TestEveryAuditEventTheDomainAppendsCarriesABook turns bookOf's last sentence
// from a claim into a check.
//
// bookOf maps an empty book to everyBook, which is exactly right for a filter
// and an over-report for an append. The reason that over-report never fires is
// that every AuditEvent the domain constructs sets BookID — so this reads the
// construction sites and says so, in the packages payment.Tx actually spans.
// Those packages are the ones the chain walk visited, not a list kept by hand.
//
// store/storetest is deliberately out of range. It builds deliberately odd
// events to probe the store, and it is not a layer a mesh handler drives.
func TestEveryAuditEventTheDomainAppendsCarriesABook(t *testing.T) {
	w := realChainWalk(t)
	var found int
	for _, dir := range w.dirs {
		pkg := w.cache[dir]
		for _, pf := range pkg.files {
			ast.Inspect(pf.file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isAuditEventType(w, pkg, pf.imports, lit.Type) {
					return true
				}
				found++
				var keyed, hasBook bool
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					keyed = true
					if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "BookID" {
						hasBook = true
					}
				}
				// A positional literal sets every field, BookID included; only a
				// keyed one can leave it out.
				if keyed && !hasBook {
					t.Errorf("%s builds a ledger.AuditEvent without a BookID.\n"+
						"bookOf would record it as %q — an append that names no book. Give it one,\n"+
						"or change bookOf and the reasoning on it.", filepath.Base(dir), everyBook)
				}
				if len(lit.Elts) == 0 {
					t.Errorf("%s builds an empty ledger.AuditEvent, which names no book", filepath.Base(dir))
				}
				return true
			})
		}
	}
	// The domain builds one of these per layer. A scan that found none would
	// pass silently, which is the shape of failure this file exists to refuse.
	if found < 5 {
		t.Errorf("found only %d ledger.AuditEvent literals across %v; the scan is wrong, not the domain", found, w.dirs)
	}
}

// isAuditEventType reports whether a composite literal's type is
// ledger.AuditEvent, in either spelling and resolving the qualifier through the
// writing file's own imports — the same rule isBookID follows.
func isAuditEventType(w *chainWalk, pkg *pkgAST, imports map[string]string, typ ast.Expr) bool {
	switch t := typ.(type) {
	case *ast.Ident:
		return t.Name == "AuditEvent" && pkg.path == w.module+"/ledger"
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		return ok && t.Sel.Name == "AuditEvent" && imports[id.Name] == w.module+"/ledger"
	}
	return false
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

// TestTheRecorderIsSafeForConcurrentActorsAndSortsWhatItSaw makes the three
// claims about the recorder's own bookkeeping falsifiable, none of which any
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
// Attribution, because every TestXTouchesOnly… assertion is a claim about ONE
// institution's set. A recorder that noted the book and dropped the actor would
// leave touchedBy empty for everyone, and an assertion that an actor touched
// nothing is exactly what an empty set looks like.
//
// It needs no store: nothing here goes through Update, which is why the inner
// store is nil.
func TestTheRecorderIsSafeForConcurrentActorsAndSortsWhatItSaw(t *testing.T) {
	rec := newRecordingStore(nil)

	// Two actors and an unattributed writer, all at once. The books are named so
	// that each actor's own set is out of order on the way in.
	work := []struct {
		actor iso20022.BIC
		book  ledger.BookID
	}{
		{"AURODEFFXXX", "e"}, {"AURODEFFXXX", "d"}, {"AURODEFFXXX", "c"},
		{"AURODEFFXXX", "b"}, {"AURODEFFXXX", "a"},
		{"VERDITMMXXX", "b"}, {"VERDITMMXXX", "a"},
		{"", "z"},
	}
	var wg sync.WaitGroup
	for _, w := range work {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec.note(w.actor, w.book)
		}()
	}
	// A reader beside the writers: a test that asserts on books while actors are
	// still running is the ordinary case, not an exotic one.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = rec.touched()
		_ = rec.touchedBy("AURODEFFXXX")
	}()
	wg.Wait()

	want := []ledger.BookID{"a", "b", "c", "d", "e", "z"}
	if got := rec.touched(); !slices.Equal(got, want) {
		t.Errorf("touched() = %v, want %v — the set of books, ascending", got, want)
	}
	if got := rec.touchedBy("AURODEFFXXX"); !slices.Equal(got, []ledger.BookID{"a", "b", "c", "d", "e"}) {
		t.Errorf("touchedBy(AURODEFFXXX) = %v, want [a b c d e]", got)
	}
	if got := rec.touchedBy("VERDITMMXXX"); !slices.Equal(got, []ledger.BookID{"a", "b"}) {
		t.Errorf("touchedBy(VERDITMMXXX) = %v, want [a b] — one actor's set is not another's", got)
	}
	// The unattributed write is in the whole-store set and in nobody's.
	for _, who := range []iso20022.BIC{"AURODEFFXXX", "VERDITMMXXX"} {
		if slices.Contains(rec.touchedBy(who), "z") {
			t.Errorf("touchedBy(%s) contains a book no actor wrote", who)
		}
	}
}

// ---------------------------------------------------------------------------
// The shapes the parser refuses
// ---------------------------------------------------------------------------
//
// Three holes have now been found in this one mechanism, and all three had the
// same shape: a category the parser could not see, described in a comment as a
// category that did not exist. First it saw only ledger.Tx, and deposit, product
// and lending were unrecorded. Then it saw only a book in second position, and
// the audit trail was unrecorded. Both times the comment said the missing thing
// was not a thing.
//
// So the parser no longer SKIPS what it cannot analyse — it REFUSES it, by name,
// with the shape spelled out. The three tests below construct each refusable
// shape in a throwaway module and assert the refusal, which is the answer to the
// objection that an unused branch cannot be tested: an unused branch cannot, but
// an ERRORING branch can, and this is how.
//
// What that buys is not that the parser handles every shape. It is that a shape
// it does not handle becomes a decision somebody has to make — the same move
// structCarriedBooks makes one level up, where the parser can see a thing but
// cannot know what it means.

// TestTheParserRefusesAnEmbeddedBookIDField constructs the shape the re-review
// found: a filter struct that EMBEDS ledger.BookID instead of naming it.
//
// It is legal Go, and it compiles this repository unchanged. The promoted field
// is still spelled f.BookID, so the recorder could in principle handle it — but
// an embedded BookID also gives the struct BookID's own identity, and "is this
// the scope of the call or an embedding for some other purpose" is exactly the
// question no signature answers. Refusing puts it in front of a person.
//
// Before this refusal existed the method vanished from the candidate set
// entirely, because carrierOf iterated f.Names and an embedded field has none.
func TestTheParserRefusesAnEmbeddedBookIDField(t *testing.T) {
	root, module := probeModule(t, `
type Filter struct {
	ledger.BookID
	Scope string
}

type Tx interface {
	ListThings(ctx context.Context, f Filter) error
}
`)
	w := walkTxChain(root, module, "probe")
	assertRefused(t, w, "embeds ledger.BookID")
	assertNoCandidate(t, w, "ListThings")
}

// TestTheParserRefusesABookBehindAPointerSliceOrMap constructs the three
// indirections a book could hide behind.
//
// Each is legal, each would make the recorder's `note(f.BookID)` wrong or
// impossible — a nil pointer, a slice of several books, a map of them — and each
// silently produced no candidate before. One method per wrapper, so the refusal
// has to name all three rather than stopping at the first.
func TestTheParserRefusesABookBehindAPointerSliceOrMap(t *testing.T) {
	root, module := probeModule(t, `
type Filter struct {
	BookID ledger.BookID
}

type Tx interface {
	ViaPointer(ctx context.Context, f *Filter) error
	ViaSlice(ctx context.Context, fs []Filter) error
	ViaMap(ctx context.Context, m map[string]Filter) error
	ViaArray(ctx context.Context, fs [2]Filter) error
}
`)
	w := walkTxChain(root, module, "probe")
	// A fixed array is named as one rather than called a slice: the refusal
	// quotes the word back at whoever has to act on it.
	for _, want := range []string{"pointer", "slice", "map", "array"} {
		assertRefused(t, w, want)
	}
	for _, name := range []string{"ViaPointer", "ViaSlice", "ViaMap", "ViaArray"} {
		assertNoCandidate(t, w, name)
	}
}

// TestTheParserRefusesABookNestedInAStructField constructs a book one level
// deeper than the parser reads: an argument struct whose FIELD is a struct
// carrying the BookID.
//
// The recorder would have to write o.Inner.BookID, and this parser records only
// a top-level field. The detection is transitive and the refusal names the path,
// so the person who hits it knows what the parser saw; what it deliberately does
// not do is invent the path, because a book two levels down may be a scope or
// may be a copy of one, and that is the structCarriedBooks question again.
func TestTheParserRefusesABookNestedInAStructField(t *testing.T) {
	root, module := probeModule(t, `
type Inner struct {
	BookID ledger.BookID
}

type Outer struct {
	Inner Inner
	Note  string
}

type Tx interface {
	Nested(ctx context.Context, o Outer) error
}
`)
	w := walkTxChain(root, module, "probe")
	assertRefused(t, w, "carries a ledger.BookID inside it")
	assertNoCandidate(t, w, "Nested")
}

// TestTheParserRefusesABookPromotedThroughAnUnexportedEmbeddedType constructs
// the hole the second re-review found in the fix for the first one.
//
// Go promotes an unexported embedded type's EXPORTED fields, so `o.BookID` is
// readable from any package even though `inner` is not nameable from one. The
// first version of exportedField tested the embedded type's own name, decided
// `inner` was out of reach, and skipped the field — giving `refusals=[]
// methods=[]`, a silent skip inside the very rule written to make silent skips
// impossible.
//
// This is the fourth instance of one failure: a category the parser could not
// see. It is a test rather than a comment for that exact reason.
func TestTheParserRefusesABookPromotedThroughAnUnexportedEmbeddedType(t *testing.T) {
	root, module := probeModule(t, `
type inner struct {
	BookID ledger.BookID
}

type Outer struct {
	inner
	Note string
}

type Tx interface {
	Promoted(ctx context.Context, o Outer) error
}
`)
	w := walkTxChain(root, module, "probe")
	assertRefused(t, w, "carries a ledger.BookID")
	assertNoCandidate(t, w, "Promoted")
}

// TestTheParserFollowsOnlyFieldsTheDecoratorCouldName is the other side of the
// refusals: the shape that must NOT be refused.
//
// A book behind an unexported field is not a blind spot, it is out of reach —
// recordingTx is written in package mesh and cannot name it. Refusing it would
// be noise, and expensive noise: payment.Bank reaches four books this
// way, through the live handles whose types keep their book unexported, and a
// parser that refused those would refuse the real repository on every run.
//
// See exportedField for why that is safe as well as necessary.
func TestTheParserFollowsOnlyFieldsTheDecoratorCouldName(t *testing.T) {
	root, module := probeModule(t, `
type Inner struct {
	BookID ledger.BookID
}

type Outer struct {
	hidden Inner
	Note   string
}

type Tx interface {
	Hidden(ctx context.Context, o Outer) error
}
`)
	w := walkTxChain(root, module, "probe")
	if len(w.refusals) != 0 {
		t.Errorf("the parser refused a book behind an unexported field: %v.\n"+
			"It cannot be named from package mesh, so there is no decision to force.", w.refusals)
	}
	assertNoCandidate(t, w, "Hidden")
}

// TestTheProbeModuleItselfParsesCleanly is the control the three refusal tests
// need. Without it they would all still pass if probeModule produced something
// the parser refused for an unrelated reason — a fixture that always fails is no
// evidence about the shape it was meant to isolate.
func TestTheProbeModuleItselfParsesCleanly(t *testing.T) {
	root, module := probeModule(t, `
type Tx interface {
	Ordinary(ctx context.Context, book ledger.BookID, id string) error
	NotBookScoped(ctx context.Context, id string) error
}
`)
	w := walkTxChain(root, module, "probe")
	if len(w.refusals) != 0 {
		t.Fatalf("the probe module refused an ordinary shape: %v", w.refusals)
	}
	if len(w.methods) != 1 || w.methods[0].Name != "Ordinary" || w.methods[0].Carry != bookIsTheArg {
		t.Fatalf("probe found %+v, want exactly Ordinary carrying its book positionally", w.methods)
	}
}

// probeModule writes a throwaway module laid out like this one — a ledger
// package declaring BookID, and a package declaring a Tx interface — and returns
// its root and module path.
//
// It exists so the parser's refusals can be tested on shapes that do not occur
// in this repository. The files are only ever parsed, never compiled, and they
// live in the test's own temporary directory, so they are invisible to the
// build.
func probeModule(t *testing.T, probe string) (root, module string) {
	t.Helper()
	root = t.TempDir()
	module = "example.test/probe"

	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	write("go.mod", "module "+module+"\n\ngo 1.25\n")
	write("ledger/ledger.go", "package ledger\n\ntype BookID string\n")
	write("probe/probe.go", "package probe\n\nimport (\n\t\"context\"\n\n\t\""+
		module+"/ledger\"\n)\n\nvar _ = context.Background\n"+probe)
	return root, module
}

func assertRefused(t *testing.T, w *chainWalk, substring string) {
	t.Helper()
	for _, r := range w.refusals {
		if strings.Contains(r, substring) {
			return
		}
	}
	t.Errorf("the parser did not refuse a shape mentioning %q; it reported %v.\n"+
		"A shape it cannot analyse must be refused, not skipped: skipping is how the\n"+
		"audit methods stayed invisible for two rounds.", substring, w.refusals)
}

func assertNoCandidate(t *testing.T, w *chainWalk, name string) {
	t.Helper()
	for _, m := range w.methods {
		if m.Name == name {
			t.Errorf("%s was accepted as a %v candidate; the parser cannot record that shape and must refuse it instead", name, m.Carry)
		}
	}
}

// ---------------------------------------------------------------------------
// Parsing: walking the Tx embedding chain
// ---------------------------------------------------------------------------

// bookArg is HOW a method's second argument carries its book.
type bookArg int

const (
	noBook bookArg = iota
	// bookIsTheArg: `book ledger.BookID` — 50 of the 53 candidates.
	bookIsTheArg
	// bookInsideTheArg: a struct with a BookID field, as AuditEvent,
	// AuditFilter and Bank have.
	bookInsideTheArg
)

func (c bookArg) String() string {
	switch c {
	case bookIsTheArg:
		return "positional"
	case bookInsideTheArg:
		return "struct-carried"
	}
	return "not book-carrying"
}

// bookMethod is one book-carrying method: where it is declared, how it carries
// its book, and — for the struct shape — under what name.
type bookMethod struct {
	Pkg   string // the declaring package's directory name: ledger, product, …
	Name  string
	Carry bookArg
	Arg   string // the second parameter's name in the interface declaration
	Field string // bookInsideTheArg only: the BookID field's name

	// PassengerArg and PassengerField are a SECOND book, riding inside a later
	// argument of a method that already takes `book ledger.BookID` at argument 1
	// — PutSettlementAdvice(ctx, book, a) where a.Book exists.
	//
	// It is recorded rather than ignored because "the row merely records the
	// book the argument already chose" is a claim about the STORE, which no
	// signature makes. It goes to structCarriedBooks like every other
	// struct-carried book; see TestEveryStructCarriedBookIsDecided.
	PassengerArg, PassengerField string
}

// chainWalk is one traversal of a Tx embedding chain: what it found, what it
// refused, and which packages it visited.
//
// It takes no *testing.T on purpose. The refusals are DATA, which is what lets
// the three tests above construct a refusable shape and assert on the outcome
// instead of watching the suite fail.
type chainWalk struct {
	root   string // the directory holding go.mod
	module string

	methods  []bookMethod
	refusals []string
	dirs     []string // every package the chain reached, in visit order

	cache map[string]*pkgAST
	seen  map[string]bool
}

// txBookCandidates is every method reachable through payment.Tx whose second
// argument carries a book, in either shape.
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
// asserting it was not per-book.
//
// Whether shape 2 actually SCOPES the operation is not visible from any
// signature — see structCarriedBooks, which is where that is decided. This
// function's job is only to make sure none is FORGOTTEN.
//
// Four deliberate properties of the rule:
//
//   - Type and position, not name. The parameter is called `book` everywhere
//     today, but a name is documentation and a type is a fact.
//   - Either spelling of the type, resolved properly: a bare `BookID` counts
//     only inside package ledger, and a qualified one is resolved through the
//     file's own import block rather than by assuming the qualifier is spelled
//     "ledger". A layer that wrote `import l "…/ledger"` is handled, which
//     matters because the embed walk already honours aliases and two halves of
//     one walk disagreeing is how a whole layer drops out silently.
//   - A book carried anywhere OTHER than second is refused, not skipped.
//   - A book carried in a shape this parser does not read — embedded rather than
//     named, behind a pointer or slice or map, or nested one struct deeper — is
//     REFUSED. See the three tests above, which construct each shape.
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
	return realChainWalk(t).methods
}

// realChainWalk walks this repository's own chain and fails the test on any
// refusal. The refusals are only data to the walk; here is where they become
// failures.
func realChainWalk(t *testing.T) *chainWalk {
	t.Helper()
	w := walkTxChain("..", modulePath(t), "payment")
	for _, r := range w.refusals {
		t.Error(r)
	}
	return w
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

// walkTxChain follows the Tx interface embedding chain from one entry package.
func walkTxChain(root, module, entry string) *chainWalk {
	w := &chainWalk{
		root:   root,
		module: module,
		cache:  map[string]*pkgAST{},
		seen:   map[string]bool{},
	}
	w.walk(filepath.Join(root, entry))

	// Names are unique across the chain — Go rejects an interface that embeds
	// two methods of one name — so a duplicate here means the walk visited
	// something twice and every count taken from it is wrong.
	byName := map[string]string{}
	for _, m := range w.methods {
		if prev, dup := byName[m.Name]; dup {
			w.refusef("%s found in both %s and %s; the walk is double-counting", m.Name, prev, m.Pkg)
		}
		byName[m.Name] = m.Pkg
	}
	return w
}

func (w *chainWalk) refusef(format string, args ...any) {
	w.refusals = append(w.refusals, fmt.Sprintf(format, args...))
}

func (w *chainWalk) walk(dir string) {
	if w.seen[dir] {
		return // ledger.Tx is reached through product and through lending
	}
	w.seen[dir] = true
	w.dirs = append(w.dirs, dir)

	pkg := w.loadPackage(dir)
	if pkg.iface == nil {
		w.refusef("no `type Tx interface` found in %s", dir)
		return
	}
	for _, f := range pkg.iface.Methods.List {
		// An embedded interface has no name. Recurse into the package that
		// declares it, resolving the qualifier through the importing file's own
		// import block rather than assuming the alias is the directory.
		if len(f.Names) == 0 {
			sel, ok := f.Type.(*ast.SelectorExpr)
			if !ok {
				w.refusef("%s/Tx embeds %s, which this walk does not understand", dir, exprString(f.Type))
				continue
			}
			qualifier, ok := sel.X.(*ast.Ident)
			if !ok || sel.Sel.Name != "Tx" {
				w.refusef("%s/Tx embeds %s, which is not a package's Tx", dir, exprString(f.Type))
				continue
			}
			path, ok := pkg.ifaceImports[qualifier.Name]
			if !ok {
				w.refusef("%s/Tx embeds %s.Tx but the file imports no %s", dir, qualifier.Name, qualifier.Name)
				continue
			}
			rel, inModule := strings.CutPrefix(path, w.module+"/")
			if !inModule {
				w.refusef("%s/Tx embeds %s, which is outside this module", dir, path)
				continue
			}
			w.walk(filepath.Join(w.root, rel))
			continue
		}
		fn, ok := f.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		if m := w.classify(pkg, f.Names[0].Name, fn); m.Carry != noBook {
			w.methods = append(w.methods, m)
		}
	}
}

// classify decides how one interface method carries its book, refusing a book
// carried anywhere but straight after ctx.
//
// The one exception is the shape PutSettlementAdvice introduced: a method that
// already takes `book ledger.BookID` at argument 1 and is then handed a ROW that
// names its own book. That is not a second position for the scope to hide in —
// the override still notes argument 1 — so it is not refused.
//
// It is not waved through either. Whether the row's field is merely a record of
// the book the argument chose, or a second book the store actually reads, is a
// claim about the STORE that no signature makes; it is the same question
// AppendAudit and PutBank pose, and it gets the same answer: it is
// recorded as a PASSENGER and must be decided in structCarriedBooks with
// evidence. Waving it through here is what would turn that registry into a rule
// nobody consults, and the next PutX(ctx, book, x) of this shape would be
// accepted silently.
func (w *chainWalk) classify(pkg *pkgAST, name string, fn *ast.FuncType) bookMethod {
	found := bookMethod{Pkg: filepath.Base(pkg.dir), Name: name}
	for i, p := range flatParams(fn) {
		ref := typeRef{expr: p.typ, imports: pkg.ifaceImports, pkg: pkg}
		carry, field := w.carrierOf(ref, fmt.Sprintf("%s.Tx.%s argument %d (%s)", filepath.Base(pkg.dir), name, i, p.name))
		if carry == noBook {
			continue
		}
		if i != 1 {
			if found.Carry == bookIsTheArg && carry == bookInsideTheArg {
				found.PassengerArg, found.PassengerField = p.name, field
				continue
			}
			w.refusef("%s.Tx.%s carries its book at argument %d, not straight after ctx. "+
				"Every override relies on that position; move it back, or teach this parser the new rule.",
				filepath.Base(pkg.dir), name, i)
			continue
		}
		found.Carry, found.Arg, found.Field = carry, p.name, field
	}
	return found
}

// typeRef is a type expression together with what it takes to resolve it: the
// imports of the file it was written in, and the package that file belongs to.
type typeRef struct {
	expr    ast.Expr
	imports map[string]string
	pkg     *pkgAST
}

// carrierOf reports whether a parameter type carries a book and how, refusing
// every shape it can see a book in but cannot record.
func (w *chainWalk) carrierOf(ref typeRef, where string) (bookArg, string) {
	if w.isBookID(ref) {
		return bookIsTheArg, ""
	}

	// A book behind an indirection. The recorder writes `note(f.BookID)`, which
	// a pointer could nil-dereference and a slice or map could hold several
	// different answers to. Analysable in principle; not analysed here; refused
	// rather than skipped.
	if elems, wrapper := unwrap(ref.expr); wrapper != "" {
		for _, e := range elems {
			if w.carriesBook(typeRef{expr: e, imports: ref.imports, pkg: ref.pkg}, map[string]bool{}) {
				w.refusef("%s is %s of %s, which carries a ledger.BookID. "+
					"This parser records a book that is the argument or a top-level field of it, "+
					"so decide what this one means and teach it the shape.", where, wrapper, exprString(ref.expr))
				return noBook, ""
			}
		}
		return noBook, ""
	}

	decl, ok := w.lookupStruct(ref)
	if !ok {
		return noBook, ""
	}

	var fields []string
	for _, f := range decl.typ.Fields.List {
		if !exportedField(f) {
			continue // see exportedField
		}
		fieldRef := typeRef{expr: f.Type, imports: decl.imports, pkg: decl.pkg}
		book := w.isBookID(fieldRef)

		// An embedded field has no name. Its promoted selector happens to be
		// spelled BookID, so this could be handled — but an embedded BookID also
		// hands the struct that type's identity, and whether it is the scope of
		// the call is exactly the question no signature answers.
		if len(f.Names) == 0 {
			switch {
			case book:
				w.refusef("%s: %s embeds ledger.BookID rather than naming it. "+
					"Name the field, or decide what the embedding means and teach this parser.",
					where, exprString(ref.expr))
			case w.carriesBook(fieldRef, map[string]bool{}):
				w.refusef("%s: %s embeds %s, which carries a ledger.BookID inside it.",
					where, exprString(ref.expr), exprString(f.Type))
			}
			continue
		}
		if book {
			for _, n := range f.Names {
				fields = append(fields, n.Name)
			}
			continue
		}
		if w.carriesBook(fieldRef, map[string]bool{}) {
			for _, n := range f.Names {
				w.refusef("%s: %s.%s carries a ledger.BookID inside it, one level deeper than this parser reads. "+
					"Decide whether that book scopes the call, and teach this parser the path if it does.",
					where, exprString(ref.expr), n.Name)
			}
		}
	}

	switch len(fields) {
	case 0:
		return noBook, ""
	case 1:
		return bookInsideTheArg, fields[0]
	default:
		w.refusef("%s: %s carries %d BookID fields (%s); the recorder would not know which one scopes the call",
			where, exprString(ref.expr), len(fields), strings.Join(fields, ", "))
		return noBook, ""
	}
}

// carriesBook reports whether a book is reachable from a type at all, following
// pointers, slices, maps and in-module struct fields to any depth.
//
// It answers only "is one in there", never "where". That asymmetry is the whole
// design: knowing a book is hidden is enough to refuse, and refusing is what
// turns a blind spot into somebody's decision. Working out the path would be
// guessing at intent, which is what structCarriedBooks exists to stop.
func (w *chainWalk) carriesBook(ref typeRef, visited map[string]bool) bool {
	if w.isBookID(ref) {
		return true
	}
	if elems, wrapper := unwrap(ref.expr); wrapper != "" {
		for _, e := range elems {
			if w.carriesBook(typeRef{expr: e, imports: ref.imports, pkg: ref.pkg}, visited) {
				return true
			}
		}
		return false
	}
	decl, ok := w.lookupStruct(ref)
	if !ok {
		return false
	}
	key := decl.pkg.path + "." + exprString(ref.expr)
	if visited[key] {
		return false // a recursive type; it has been asked already
	}
	visited[key] = true
	for _, f := range decl.typ.Fields.List {
		if !exportedField(f) {
			continue
		}
		if w.carriesBook(typeRef{expr: f.Type, imports: decl.imports, pkg: decl.pkg}, visited) {
			return true
		}
	}
	return false
}

// exportedField reports whether a struct field is one the decorator could name.
//
// The traversal follows exported fields only, and that is a statement about
// reachability rather than a convenience. recordingTx is written in package mesh
// and cannot name an unexported field of a type from anywhere else, so a book
// reachable only through one is not a path the recorder could ever take — there
// is nothing to "decide", which is what a refusal is for.
//
// Nor is it a crossing left open, and payment.Bank is the case that
// proves it. Its Ledger, Deposit, Lending and Catalogue fields are live handles
// whose types keep the book in an unexported field (ledger/book.go: `id
// BookID`), so the transitive scan reaches four books through them. But a
// handler that used one of those handles to read a book would do it by calling
// through to the Tx — PutTransaction, BookBalance, ListAudit — and every one of
// those IS recorded. None of the four caches anything; Network.bind builds all
// four over the same payment.Tx through the view adapters; recordingStore wraps
// Update AND View; and the stores are separate packages, so an unexported book
// cannot scope a store operation from outside its own. The Tx is the choke
// point; a live handle is a route TO it, not around it.
//
// That is a statement about the code as it stands, and sub-project 8 must
// revisit it rather than inherit it: the moment any entity gets a handle that
// answers from a cache, a materialised view, or anything else that does not go
// through the Tx, this exclusion stops holding and a book behind an unexported
// field becomes reachable without being recorded.
//
// An EMBEDDED field is always followed, whatever its type's name. Go promotes an
// unexported embedded type's EXPORTED fields, so `type Outer struct { inner }`
// with `inner` carrying an exported BookID makes o.BookID readable from any
// package — the book is reachable even though the thing holding it is not
// nameable. An earlier version of this function tested the embedded type's own
// name and therefore skipped that shape silently, which is precisely the failure
// the refusals exist to end: a category the parser could not see. The recursion
// applies this same rule to the promoted struct's own fields, so an unexported
// field one level down is still correctly out of reach.
func exportedField(f *ast.Field) bool {
	if len(f.Names) == 0 {
		return true
	}
	for _, n := range f.Names {
		if n.IsExported() {
			return true
		}
	}
	return false
}

// lookupStruct resolves a type to a struct declaration, in its own package or —
// following the file's imports — in another one inside the module.
func (w *chainWalk) lookupStruct(ref typeRef) (structDecl, bool) {
	switch e := ref.expr.(type) {
	case *ast.Ident:
		d, ok := ref.pkg.structs[e.Name]
		return d, ok
	case *ast.SelectorExpr:
		qualifier, ok := e.X.(*ast.Ident)
		if !ok {
			return structDecl{}, false
		}
		path, ok := ref.imports[qualifier.Name]
		if !ok {
			return structDecl{}, false
		}
		rel, inModule := strings.CutPrefix(path, w.module+"/")
		if !inModule {
			return structDecl{}, false // stdlib and third-party carry no BookID
		}
		other := w.loadPackage(filepath.Join(w.root, rel))
		d, ok := other.structs[e.Sel.Name]
		return d, ok
	}
	return structDecl{}, false
}

// isBookID reports whether a type expression is ledger.BookID.
//
// A bare `BookID` counts only inside package ledger, and a qualified one is
// resolved through the writing file's own imports — never by comparing the
// qualifier to the string "ledger", which would miss an aliased import and
// silently drop every method in that layer.
func (w *chainWalk) isBookID(ref typeRef) bool {
	ledgerPath := w.module + "/ledger"
	switch typ := ref.expr.(type) {
	case *ast.Ident:
		return typ.Name == "BookID" && ref.pkg.path == ledgerPath
	case *ast.SelectorExpr:
		qualifier, ok := typ.X.(*ast.Ident)
		if !ok || typ.Sel.Name != "BookID" {
			return false
		}
		return ref.imports[qualifier.Name] == ledgerPath
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
	pkg     *pkgAST
}

// parsedFile is one source file and the imports it was written with.
type parsedFile struct {
	file    *ast.File
	imports map[string]string
}

// pkgAST is one package as this walk needs it: its Tx interface, the imports of
// the file that declares it, every struct type it declares, and its files.
type pkgAST struct {
	dir          string
	path         string
	iface        *ast.InterfaceType
	ifaceImports map[string]string
	structs      map[string]structDecl
	files        []parsedFile
}

// loadPackage parses every non-test file in a package directory once.
//
// It publishes to the cache only when the package is FULLY built. An earlier
// version cached the empty shell first, as a re-entry guard; nothing re-enters —
// Go forbids import cycles, so no package's struct fields can lead back to a
// package still being read — and a half-built entry that is safe only because
// nobody happens to touch it is the kind of thing this file is meant not to
// contain.
func (w *chainWalk) loadPackage(dir string) *pkgAST {
	if p, ok := w.cache[dir]; ok {
		return p
	}
	rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(dir, w.root)), "/")
	pkg := &pkgAST{dir: dir, path: w.module + "/" + rel, structs: map[string]structDecl{}}

	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		w.refusef("listing %s: %v", dir, err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parseFile(path)
		if err != nil {
			w.refusef("parsing %s: %v", path, err)
			continue
		}
		imports := fileImports(file)
		pkg.files = append(pkg.files, parsedFile{file: file, imports: imports})
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
					pkg.structs[ts.Name.Name] = structDecl{typ: typ, imports: imports, pkg: pkg}
				}
			}
		}
	}
	w.cache[dir] = pkg
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

// unwrap peels one pointer, slice, array, map or variadic off a type and names
// what it peeled. A map yields both its key and its value.
func unwrap(e ast.Expr) ([]ast.Expr, string) {
	switch t := e.(type) {
	case *ast.StarExpr:
		return []ast.Expr{t.X}, "a pointer"
	case *ast.ArrayType:
		// Len is nil for a slice and set for a fixed array. They are different
		// types and the refusal quotes this word back at whoever reads it.
		if t.Len != nil {
			return []ast.Expr{t.Elt}, "an array"
		}
		return []ast.Expr{t.Elt}, "a slice"
	case *ast.MapType:
		return []ast.Expr{t.Key, t.Value}, "a map"
	case *ast.Ellipsis:
		return []ast.Expr{t.Elt}, "a variadic"
	}
	return nil, ""
}

// exprString renders a type expression for a message, without needing the
// FileSet the expression was parsed with.
func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.BasicLit:
		return t.Value
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprString(t.Elt)
		}
		return "[" + exprString(t.Len) + "]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	case *ast.InterfaceType:
		return "interface{…}"
	}
	return fmt.Sprintf("%T", e)
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

func parseFile(path string) (*ast.File, error) {
	return parser.ParseFile(token.NewFileSet(), path, nil, 0)
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
