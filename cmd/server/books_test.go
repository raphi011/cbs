package main

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
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/sqlite"
	"github.com/raphi011/cbs/store/testenv"
)

// ---------------------------------------------------------------------------
// The book-access recorder
// ---------------------------------------------------------------------------

// recordingStores wraps the whole SET of stores and records which ledger books
// each unit of work touched, across every institution's database.
//
// The second of this package's two boundary mechanisms, and the pair is stronger
// than either: the interfaces in ops.go narrow by METHOD, store/sqlite's
// ErrNotThisStoresBook narrows by DATABASE, and neither can say WHICH books an act
// reached. What this catches is attribution — a unit of work reaching two books,
// an unfiltered audit read, an actor touching a book it has no business in even
// within its own store.
//
// It wraps the store SET rather than one store, so every handle it hands out
// shares this value's maps and "which books did this actor touch" has one answer.
type recordingStores struct {
	inner payment.Stores

	mu    sync.Mutex
	books map[ledger.BookID]bool
	// byActor is the same set, split by the institution whose unit of work made the
	// access. See withActor for where that identity comes from.
	//
	// A unit of work opened by nobody in particular — a fixture, a seed, an operator's
	// cut-off — is counted in books and in no actor's set: attributing it would put
	// work no actor did into that actor's ledger of crossings.
	byActor map[iso20022.BIC]map[ledger.BookID]bool
	// units is one entry per WRITING unit of work opened since the last reset, each
	// holding the books that unit reached. It answers what no set-of-actors can:
	// whether a single Update touched two books — the claim the N+2 split rests on —
	// and it needs no actor identity, which makes it askable of work no actor did.
	units []map[ledger.BookID]bool
	// updates counts the WRITING units of work opened since the last reset, which is
	// what lets a test say "both books moved together" rather than "both books
	// moved". Views are not counted: a read crosses nothing that has to commit.
	updates int
}

var (
	_ payment.Stores             = (*recordingStores)(nil)
	_ payment.BankStore          = (*recordingBankStore)(nil)
	_ payment.ClearingHouseStore = (*recordingClearingHouseStore)(nil)
	_ payment.CentralBankStore   = (*recordingCentralBankStore)(nil)
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
func (s *recordingStores) Bank(ctx context.Context, bic iso20022.BIC) (payment.BankStore, error) {
	inner, err := s.inner.Bank(ctx, bic)
	if err != nil {
		return nil, err
	}
	return &recordingBankStore{inner: inner, rec: s}, nil
}

func (s *recordingStores) Banks(ctx context.Context) ([]iso20022.BIC, error) {
	return s.inner.Banks(ctx)
}

func (s *recordingStores) ClearingHouse() payment.ClearingHouseStore {
	return &recordingClearingHouseStore{inner: s.inner.ClearingHouse(), rec: s}
}

func (s *recordingStores) CentralBank() payment.CentralBankStore {
	return &recordingCentralBankStore{inner: s.inner.CentralBank(), rec: s}
}

func (s *recordingStores) Reset(ctx context.Context) error { return s.inner.Reset(ctx) }
func (s *recordingStores) Close() error                    { return s.inner.Close() }

// The three recording stores: one institution's database each, seen through the
// recorder. None holds state of its own — every note goes to the set above. Three
// types because there are three store interfaces, each wrapping the transaction
// its own institution's unit of work hands out.
type recordingBankStore struct {
	inner payment.BankStore
	rec   *recordingStores
}

func (s *recordingBankStore) Update(ctx context.Context, fn func(context.Context, payment.BankTx) error) error {
	unit := s.rec.openUnit()
	return s.inner.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		return fn(ctx, &recordingBankTx{BankTx: tx, rec: s.rec.noterFor(ctx, unit)})
	})
}

func (s *recordingBankStore) View(ctx context.Context, fn func(context.Context, payment.BankTx) error) error {
	return s.inner.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		return fn(ctx, &recordingBankTx{BankTx: tx, rec: s.rec.noterFor(ctx, nil)})
	})
}

func (s *recordingBankStore) Reset(ctx context.Context) error { return s.inner.Reset(ctx) }
func (s *recordingBankStore) Close() error                    { return s.inner.Close() }

type recordingClearingHouseStore struct {
	inner payment.ClearingHouseStore
	rec   *recordingStores
}

func (s *recordingClearingHouseStore) Update(ctx context.Context, fn func(context.Context, payment.CsmTx) error) error {
	unit := s.rec.openUnit()
	return s.inner.Update(ctx, func(ctx context.Context, tx payment.CsmTx) error {
		return fn(ctx, &recordingCsmTx{CsmTx: tx, rec: s.rec.noterFor(ctx, unit)})
	})
}

func (s *recordingClearingHouseStore) View(ctx context.Context, fn func(context.Context, payment.CsmTx) error) error {
	return s.inner.View(ctx, func(ctx context.Context, tx payment.CsmTx) error {
		return fn(ctx, &recordingCsmTx{CsmTx: tx, rec: s.rec.noterFor(ctx, nil)})
	})
}

func (s *recordingClearingHouseStore) Reset(ctx context.Context) error { return s.inner.Reset(ctx) }
func (s *recordingClearingHouseStore) Close() error                    { return s.inner.Close() }

type recordingCentralBankStore struct {
	inner payment.CentralBankStore
	rec   *recordingStores
}

func (s *recordingCentralBankStore) Update(ctx context.Context, fn func(context.Context, payment.CentralBankTx) error) error {
	unit := s.rec.openUnit()
	return s.inner.Update(ctx, func(ctx context.Context, tx payment.CentralBankTx) error {
		return fn(ctx, &recordingCentralBankTx{CentralBankTx: tx, rec: s.rec.noterFor(ctx, unit)})
	})
}

func (s *recordingCentralBankStore) View(ctx context.Context, fn func(context.Context, payment.CentralBankTx) error) error {
	return s.inner.View(ctx, func(ctx context.Context, tx payment.CentralBankTx) error {
		return fn(ctx, &recordingCentralBankTx{CentralBankTx: tx, rec: s.rec.noterFor(ctx, nil)})
	})
}

func (s *recordingCentralBankStore) Reset(ctx context.Context) error { return s.inner.Reset(ctx) }
func (s *recordingCentralBankStore) Close() error                    { return s.inner.Close() }

// openUnit starts one WRITING unit of work: it counts it and returns the set its
// books go into. The three Updates above share it, which is what keeps "did one
// unit of work touch two books" one question over every database.
func (s *recordingStores) openUnit() map[ledger.BookID]bool {
	unit := map[ledger.BookID]bool{}
	s.mu.Lock()
	s.updates++
	s.units = append(s.units, unit)
	s.mu.Unlock()
	return unit
}

// bookNoter is what one unit of work notes into: the store, plus the identity of
// the actor that opened it. It exists so the recorders' overrides can stay two
// statements long — TestEveryRecordingTxMethodNotesItsBookThenDelegates holds them
// to that shape — so the actor cannot be an argument at the call site and has to
// travel with the thing being called.
type bookNoter struct {
	store *recordingStores
	actor iso20022.BIC
	// unit is the set this unit of work's own books go into, or nil for a View.
	// A read crosses nothing that has to commit, so only writes are units here.
	unit map[ledger.BookID]bool
}

func (n bookNoter) note(book ledger.BookID) { n.store.note(n.actor, n.unit, book) }

// noterFor reads the acting institution off the context the unit of work was
// opened with. A unit of work opened by nobody in particular has no actor, and
// its books still land in the whole-store set and in its own unit.
func (s *recordingStores) noterFor(ctx context.Context, unit map[ledger.BookID]bool) bookNoter {
	who, _ := actorOf(ctx)
	return bookNoter{store: s, actor: who, unit: unit}
}

// note records one book access, against the whole store and against the actor that
// made it. It takes the lock: the harness serves both hosts on HTTP listeners whose
// goroutines touch the same store concurrently.
//
// What it records is NOT rolled back with the unit of work. A handler that read
// another bank's book and then failed still read it.
func (s *recordingStores) note(actor iso20022.BIC, unit map[ledger.BookID]bool, book ledger.BookID) {
	s.mu.Lock()
	s.books[book] = true
	if unit != nil {
		unit[book] = true
	}
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
	s.units = nil
	s.mu.Unlock()
}

// crossings is every writing unit of work since the last reset that reached more
// than one book, each as its own sorted set. An empty answer is the claim the N+2
// split makes.
func (s *recordingStores) crossings() [][]ledger.BookID {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]ledger.BookID
	for _, u := range s.units {
		if len(u) > 1 {
			out = append(out, sortedBooks(u))
		}
	}
	return out
}

// everyBook is what an access records when its book is EMPTY.
//
// It exists for ListAudit: an AuditFilter with no BookID reads every book, since
// tx_audit.go omits the WHERE clause entirely, and recording nothing there would
// leave open the widest crossing in the system. It is a BookID no actor owns, so
// any assertion on an actor's own books fails on it and names it.
const everyBook ledger.BookID = "(every book)"

// bookOf is the book a struct-carried access touches.
//
// A book arriving inside a struct can be empty, which a positional argument never
// is. For a filter, empty means every book; for a write it means an event belonging
// to no book, so mapping both to everyBook OVER-reports the write. That is the
// deliberate direction: a guard that over-reports fails loudly, one that
// under-reports passes silently. The over-report is unreachable because every audit
// event the domain appends names a book, which
// TestEveryAuditEventTheDomainAppendsCarriesABook checks at the construction sites.
func bookOf(book ledger.BookID) ledger.BookID {
	if book == "" {
		return everyBook
	}
	return book
}

// recordingBankTx is one unit of work over a bank's store: a payment.BankTx that
// notes the book of every book-scoped call before delegating.
//
// "Every book-scoped call" means all four layers, not just the ledger. A
// payment.BankTx embeds deposit.Tx, which embeds product.Tx, which embeds
// ledger.Tx, with lending.Tx beside them, and every one is book-scoped because a
// book IS a bank. Wrapping only ledger.Tx would leave a handler free to read
// another bank's deposit register, holds, catalogue or loan book untouched.
//
// It also means both SHAPES of book argument: most methods take `book BookID`
// second, while AppendAudit and ListAudit carry theirs inside the struct — see
// structCarriedBooks, which is where a method of that shape is decided.
//
// A payment method that writes a row of its institution's own leaves a trace
// through the ID the row needed and the audit event it wrote, not through the row.
// The settlement advice is the exception: a member bank's own record of a cut-off
// it was told about, so it carries that bank's book and IS recorded.
//
// It EMBEDS payment.BankTx, so everything else is promoted untouched, and
// TestRecordingTxOverridesEveryBookScopedMethod keeps the override set exact — one
// that went missing would be silently replaced by the promoted method.
type recordingBankTx struct {
	payment.BankTx
	rec bookNoter
}

var _ payment.BankTx = (*recordingBankTx)(nil)

// The overrides, grouped by the layer that declares them. Each is the same two
// statements — note the book, then call through — and
// TestEveryRecordingTxMethodNotesItsBookThenDelegates derives the expected note
// expression from each method's own parsed signature.

// --- ledger.Tx ---

func (r *recordingBankTx) NextID(ctx context.Context, book ledger.BookID, prefix string) (string, error) {
	r.rec.note(book)
	return r.BankTx.NextID(ctx, book, prefix)
}

func (r *recordingBankTx) NextSubledgerBlock(ctx context.Context, book ledger.BookID) (int, error) {
	r.rec.note(book)
	return r.BankTx.NextSubledgerBlock(ctx, book)
}

func (r *recordingBankTx) NextAccountSeq(ctx context.Context, book ledger.BookID, typeBlock int, subledger ledger.SubledgerID) (int, error) {
	r.rec.note(book)
	return r.BankTx.NextAccountSeq(ctx, book, typeBlock, subledger)
}

func (r *recordingBankTx) PutLedger(ctx context.Context, book ledger.BookID, l ledger.Ledger) error {
	r.rec.note(book)
	return r.BankTx.PutLedger(ctx, book, l)
}

func (r *recordingBankTx) GetLedger(ctx context.Context, book ledger.BookID, id ledger.LedgerID) (ledger.Ledger, error) {
	r.rec.note(book)
	return r.BankTx.GetLedger(ctx, book, id)
}

func (r *recordingBankTx) ListLedgers(ctx context.Context, book ledger.BookID) ([]ledger.Ledger, error) {
	r.rec.note(book)
	return r.BankTx.ListLedgers(ctx, book)
}

func (r *recordingBankTx) PutSubledger(ctx context.Context, book ledger.BookID, sl ledger.Subledger) error {
	r.rec.note(book)
	return r.BankTx.PutSubledger(ctx, book, sl)
}

func (r *recordingBankTx) GetSubledger(ctx context.Context, book ledger.BookID, id ledger.SubledgerID) (ledger.Subledger, error) {
	r.rec.note(book)
	return r.BankTx.GetSubledger(ctx, book, id)
}

func (r *recordingBankTx) ListSubledgers(ctx context.Context, book ledger.BookID) ([]ledger.Subledger, error) {
	r.rec.note(book)
	return r.BankTx.ListSubledgers(ctx, book)
}

func (r *recordingBankTx) PutAccount(ctx context.Context, book ledger.BookID, a ledger.Account) error {
	r.rec.note(book)
	return r.BankTx.PutAccount(ctx, book, a)
}

func (r *recordingBankTx) GetAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) (ledger.Account, error) {
	r.rec.note(book)
	return r.BankTx.GetAccount(ctx, book, id)
}

func (r *recordingBankTx) ListAccounts(ctx context.Context, book ledger.BookID) ([]ledger.Account, error) {
	r.rec.note(book)
	return r.BankTx.ListAccounts(ctx, book)
}

func (r *recordingBankTx) SubsidiaryBalances(ctx context.Context, book ledger.BookID, account ledger.AccountID, normal ledger.Direction) ([]ledger.SubsidiaryBalance, error) {
	r.rec.note(book)
	return r.BankTx.SubsidiaryBalances(ctx, book, account, normal)
}

func (r *recordingBankTx) PutSlotAccount(ctx context.Context, book ledger.BookID, row ledger.SlotAccount) error {
	r.rec.note(book)
	return r.BankTx.PutSlotAccount(ctx, book, row)
}

func (r *recordingBankTx) GetSlotAccount(ctx context.Context, book ledger.BookID, product, slot string, asset ledger.AssetCode) (ledger.AccountID, error) {
	r.rec.note(book)
	return r.BankTx.GetSlotAccount(ctx, book, product, slot, asset)
}

func (r *recordingBankTx) ListSlotAccounts(ctx context.Context, book ledger.BookID) ([]ledger.SlotAccount, error) {
	r.rec.note(book)
	return r.BankTx.ListSlotAccounts(ctx, book)
}

func (r *recordingBankTx) LockAccounts(ctx context.Context, book ledger.BookID, ids []ledger.AccountID) error {
	r.rec.note(book)
	return r.BankTx.LockAccounts(ctx, book, ids)
}

func (r *recordingBankTx) PutTransaction(ctx context.Context, book ledger.BookID, txn ledger.Transaction) error {
	r.rec.note(book)
	return r.BankTx.PutTransaction(ctx, book, txn)
}

func (r *recordingBankTx) GetTransaction(ctx context.Context, book ledger.BookID, id ledger.TransactionID) (ledger.Transaction, error) {
	r.rec.note(book)
	return r.BankTx.GetTransaction(ctx, book, id)
}

func (r *recordingBankTx) GetTransactionByIdempotencyKey(ctx context.Context, book ledger.BookID, key string) (ledger.Transaction, error) {
	r.rec.note(book)
	return r.BankTx.GetTransactionByIdempotencyKey(ctx, book, key)
}

func (r *recordingBankTx) ListTransactions(ctx context.Context, book ledger.BookID) ([]ledger.Transaction, error) {
	r.rec.note(book)
	return r.BankTx.ListTransactions(ctx, book)
}

func (r *recordingBankTx) ListTransactionsForPosition(ctx context.Context, book ledger.BookID, pos ledger.Position) ([]ledger.Transaction, error) {
	r.rec.note(book)
	return r.BankTx.ListTransactionsForPosition(ctx, book, pos)
}

func (r *recordingBankTx) MarkReversed(ctx context.Context, book ledger.BookID, id ledger.TransactionID) error {
	r.rec.note(book)
	return r.BankTx.MarkReversed(ctx, book, id)
}

func (r *recordingBankTx) BookBalance(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction) (ledger.Amount, error) {
	r.rec.note(book)
	return r.BankTx.BookBalance(ctx, book, pos, normal)
}

func (r *recordingBankTx) ValueDateBalance(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction, before time.Time) (ledger.Amount, error) {
	r.rec.note(book)
	return r.BankTx.ValueDateBalance(ctx, book, pos, normal, before)
}

func (r *recordingBankTx) ValueDatedSeries(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction, from, to time.Time) (ledger.Series, error) {
	r.rec.note(book)
	return r.BankTx.ValueDatedSeries(ctx, book, pos, normal, from, to)
}

// The two whose book travels inside the argument. See structCarriedBooks.

func (r *recordingBankTx) AppendAudit(ctx context.Context, e ledger.AuditEvent) error {
	r.rec.note(bookOf(e.BookID))
	return r.BankTx.AppendAudit(ctx, e)
}

func (r *recordingBankTx) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	r.rec.note(bookOf(f.BookID))
	return r.BankTx.ListAudit(ctx, f)
}

// --- product.Tx ---

func (r *recordingBankTx) PutProduct(ctx context.Context, book ledger.BookID, p product.Product) error {
	r.rec.note(book)
	return r.BankTx.PutProduct(ctx, book, p)
}

func (r *recordingBankTx) GetProduct(ctx context.Context, book ledger.BookID, id product.ID) (product.Product, error) {
	r.rec.note(book)
	return r.BankTx.GetProduct(ctx, book, id)
}

func (r *recordingBankTx) ListProducts(ctx context.Context, book ledger.BookID) ([]product.Product, error) {
	r.rec.note(book)
	return r.BankTx.ListProducts(ctx, book)
}

func (r *recordingBankTx) PutProductVersion(ctx context.Context, book ledger.BookID, v product.Version) error {
	r.rec.note(book)
	return r.BankTx.PutProductVersion(ctx, book, v)
}

func (r *recordingBankTx) ListProductVersions(ctx context.Context, book ledger.BookID, id product.ID) ([]product.Version, error) {
	r.rec.note(book)
	return r.BankTx.ListProductVersions(ctx, book, id)
}

func (r *recordingBankTx) GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id product.ID, day time.Time) (product.Version, error) {
	r.rec.note(book)
	return r.BankTx.GetProductVersionAsOf(ctx, book, id, day)
}

// --- deposit.Tx ---

func (r *recordingBankTx) PutDepositAccount(ctx context.Context, book ledger.BookID, a deposit.Account) error {
	r.rec.note(book)
	return r.BankTx.PutDepositAccount(ctx, book, a)
}

func (r *recordingBankTx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	r.rec.note(book)
	return r.BankTx.GetDepositAccount(ctx, book, id)
}

func (r *recordingBankTx) ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]deposit.Account, error) {
	r.rec.note(book)
	return r.BankTx.ListDepositAccounts(ctx, book)
}

func (r *recordingBankTx) ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident deposit.Identifier) ([]deposit.Account, error) {
	r.rec.note(book)
	return r.BankTx.ListDepositAccountsByIdentifier(ctx, book, ident)
}

func (r *recordingBankTx) NextAddressSerial(ctx context.Context, book ledger.BookID) (uint64, error) {
	r.rec.note(book)
	return r.BankTx.NextAddressSerial(ctx, book)
}

func (r *recordingBankTx) PutHold(ctx context.Context, book ledger.BookID, h deposit.Hold) error {
	r.rec.note(book)
	return r.BankTx.PutHold(ctx, book, h)
}

func (r *recordingBankTx) GetHold(ctx context.Context, book ledger.BookID, id deposit.HoldID) (deposit.Hold, error) {
	r.rec.note(book)
	return r.BankTx.GetHold(ctx, book, id)
}

func (r *recordingBankTx) ListHoldsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Hold, error) {
	r.rec.note(book)
	return r.BankTx.ListHoldsForAccount(ctx, book, id)
}

func (r *recordingBankTx) ActiveHoldTotal(ctx context.Context, book ledger.BookID, id deposit.AccountID, now time.Time) (ledger.Amount, error) {
	r.rec.note(book)
	return r.BankTx.ActiveHoldTotal(ctx, book, id, now)
}

func (r *recordingBankTx) PutSnapshot(ctx context.Context, book ledger.BookID, s deposit.Snapshot) error {
	r.rec.note(book)
	return r.BankTx.PutSnapshot(ctx, book, s)
}

func (r *recordingBankTx) GetSnapshot(ctx context.Context, book ledger.BookID, id deposit.AccountID, dateKey string) (deposit.Snapshot, error) {
	r.rec.note(book)
	return r.BankTx.GetSnapshot(ctx, book, id, dateKey)
}

func (r *recordingBankTx) ListSnapshotsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Snapshot, error) {
	r.rec.note(book)
	return r.BankTx.ListSnapshotsForAccount(ctx, book, id)
}

func (r *recordingBankTx) PutOverdraftTerms(ctx context.Context, book ledger.BookID, terms deposit.OverdraftTerms) error {
	r.rec.note(book)
	return r.BankTx.PutOverdraftTerms(ctx, book, terms)
}

func (r *recordingBankTx) ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.OverdraftTerms, error) {
	r.rec.note(book)
	return r.BankTx.ListOverdraftTermsForAccount(ctx, book, id)
}

func (r *recordingBankTx) GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id deposit.AccountID, day time.Time) (deposit.OverdraftTerms, error) {
	r.rec.note(book)
	return r.BankTx.GetOverdraftTermsAsOf(ctx, book, id, day)
}

// --- lending.Tx ---

func (r *recordingBankTx) PutFacility(ctx context.Context, book ledger.BookID, f lending.Facility) error {
	r.rec.note(book)
	return r.BankTx.PutFacility(ctx, book, f)
}

func (r *recordingBankTx) GetFacility(ctx context.Context, book ledger.BookID, id lending.FacilityID) (lending.Facility, error) {
	r.rec.note(book)
	return r.BankTx.GetFacility(ctx, book, id)
}

func (r *recordingBankTx) ListFacilities(ctx context.Context, book ledger.BookID) ([]lending.Facility, error) {
	r.rec.note(book)
	return r.BankTx.ListFacilities(ctx, book)
}

func (r *recordingBankTx) PutInstallment(ctx context.Context, book ledger.BookID, i lending.Installment) error {
	r.rec.note(book)
	return r.BankTx.PutInstallment(ctx, book, i)
}

func (r *recordingBankTx) ListInstallments(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.Installment, error) {
	r.rec.note(book)
	return r.BankTx.ListInstallments(ctx, book, id)
}

func (r *recordingBankTx) PutFacilityTerms(ctx context.Context, book ledger.BookID, terms lending.FacilityTerms) error {
	r.rec.note(book)
	return r.BankTx.PutFacilityTerms(ctx, book, terms)
}

func (r *recordingBankTx) ListFacilityTerms(ctx context.Context, book ledger.BookID, id lending.FacilityID) ([]lending.FacilityTerms, error) {
	r.rec.note(book)
	return r.BankTx.ListFacilityTerms(ctx, book, id)
}

func (r *recordingBankTx) GetFacilityTermsAsOf(ctx context.Context, book ledger.BookID, id lending.FacilityID, day time.Time) (lending.FacilityTerms, error) {
	r.rec.note(book)
	return r.BankTx.GetFacilityTermsAsOf(ctx, book, id, day)
}

// --- payment.BankTx's own ---
//
// Only the settlement advice. Every other method a bank's transaction declares of
// its own is network-scoped and takes no book at all.

func (r *recordingBankTx) PutSettlementAdvice(ctx context.Context, book ledger.BookID, a payment.SettlementAdvice) error {
	r.rec.note(book)
	return r.BankTx.PutSettlementAdvice(ctx, book, a)
}

func (r *recordingBankTx) GetSettlementAdvice(ctx context.Context, book ledger.BookID, reference string, asset ledger.AssetCode) (payment.SettlementAdvice, error) {
	r.rec.note(book)
	return r.BankTx.GetSettlementAdvice(ctx, book, reference, asset)
}

func (r *recordingBankTx) ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]payment.SettlementAdvice, error) {
	r.rec.note(book)
	return r.BankTx.ListSettlementAdvices(ctx, book)
}

// recordingCsmTx and recordingCentralBankTx are the same decorator for the other
// two institutions. There are three because there are three transaction types: the
// clearing house's unit of work cannot name a deposit account, so there is no one
// interface left to write a single decorator against. Each declares the overrides
// ITS institution's chain reaches and no others. The clearing house's is three
// methods — an audit trail and id allocation, and no book of accounts at all.
type recordingCsmTx struct {
	payment.CsmTx
	rec bookNoter
}

var _ payment.CsmTx = (*recordingCsmTx)(nil)

func (r *recordingCsmTx) NextID(ctx context.Context, book ledger.BookID, prefix string) (string, error) {
	r.rec.note(book)
	return r.CsmTx.NextID(ctx, book, prefix)
}

func (r *recordingCsmTx) AppendAudit(ctx context.Context, e ledger.AuditEvent) error {
	r.rec.note(bookOf(e.BookID))
	return r.CsmTx.AppendAudit(ctx, e)
}

func (r *recordingCsmTx) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	r.rec.note(bookOf(f.BookID))
	return r.CsmTx.ListAudit(ctx, f)
}

// The settlement agent's is the ledger and the bank-code counter. No slot
// mapping, because slot_accounts is a bank's alone; no deposit, product or
// lending, because it has no customers.
type recordingCentralBankTx struct {
	payment.CentralBankTx
	rec bookNoter
}

var _ payment.CentralBankTx = (*recordingCentralBankTx)(nil)

func (r *recordingCentralBankTx) NextID(ctx context.Context, book ledger.BookID, prefix string) (string, error) {
	r.rec.note(book)
	return r.CentralBankTx.NextID(ctx, book, prefix)
}

func (r *recordingCentralBankTx) NextSubledgerBlock(ctx context.Context, book ledger.BookID) (int, error) {
	r.rec.note(book)
	return r.CentralBankTx.NextSubledgerBlock(ctx, book)
}

func (r *recordingCentralBankTx) NextAccountSeq(ctx context.Context, book ledger.BookID, typeBlock int, subledger ledger.SubledgerID) (int, error) {
	r.rec.note(book)
	return r.CentralBankTx.NextAccountSeq(ctx, book, typeBlock, subledger)
}

func (r *recordingCentralBankTx) PutLedger(ctx context.Context, book ledger.BookID, l ledger.Ledger) error {
	r.rec.note(book)
	return r.CentralBankTx.PutLedger(ctx, book, l)
}

func (r *recordingCentralBankTx) GetLedger(ctx context.Context, book ledger.BookID, id ledger.LedgerID) (ledger.Ledger, error) {
	r.rec.note(book)
	return r.CentralBankTx.GetLedger(ctx, book, id)
}

func (r *recordingCentralBankTx) ListLedgers(ctx context.Context, book ledger.BookID) ([]ledger.Ledger, error) {
	r.rec.note(book)
	return r.CentralBankTx.ListLedgers(ctx, book)
}

func (r *recordingCentralBankTx) PutSubledger(ctx context.Context, book ledger.BookID, sl ledger.Subledger) error {
	r.rec.note(book)
	return r.CentralBankTx.PutSubledger(ctx, book, sl)
}

func (r *recordingCentralBankTx) GetSubledger(ctx context.Context, book ledger.BookID, id ledger.SubledgerID) (ledger.Subledger, error) {
	r.rec.note(book)
	return r.CentralBankTx.GetSubledger(ctx, book, id)
}

func (r *recordingCentralBankTx) ListSubledgers(ctx context.Context, book ledger.BookID) ([]ledger.Subledger, error) {
	r.rec.note(book)
	return r.CentralBankTx.ListSubledgers(ctx, book)
}

func (r *recordingCentralBankTx) PutAccount(ctx context.Context, book ledger.BookID, a ledger.Account) error {
	r.rec.note(book)
	return r.CentralBankTx.PutAccount(ctx, book, a)
}

func (r *recordingCentralBankTx) GetAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) (ledger.Account, error) {
	r.rec.note(book)
	return r.CentralBankTx.GetAccount(ctx, book, id)
}

func (r *recordingCentralBankTx) ListAccounts(ctx context.Context, book ledger.BookID) ([]ledger.Account, error) {
	r.rec.note(book)
	return r.CentralBankTx.ListAccounts(ctx, book)
}

func (r *recordingCentralBankTx) SubsidiaryBalances(ctx context.Context, book ledger.BookID, account ledger.AccountID, normal ledger.Direction) ([]ledger.SubsidiaryBalance, error) {
	r.rec.note(book)
	return r.CentralBankTx.SubsidiaryBalances(ctx, book, account, normal)
}

func (r *recordingCentralBankTx) LockAccounts(ctx context.Context, book ledger.BookID, ids []ledger.AccountID) error {
	r.rec.note(book)
	return r.CentralBankTx.LockAccounts(ctx, book, ids)
}

func (r *recordingCentralBankTx) PutTransaction(ctx context.Context, book ledger.BookID, txn ledger.Transaction) error {
	r.rec.note(book)
	return r.CentralBankTx.PutTransaction(ctx, book, txn)
}

func (r *recordingCentralBankTx) GetTransaction(ctx context.Context, book ledger.BookID, id ledger.TransactionID) (ledger.Transaction, error) {
	r.rec.note(book)
	return r.CentralBankTx.GetTransaction(ctx, book, id)
}

func (r *recordingCentralBankTx) GetTransactionByIdempotencyKey(ctx context.Context, book ledger.BookID, key string) (ledger.Transaction, error) {
	r.rec.note(book)
	return r.CentralBankTx.GetTransactionByIdempotencyKey(ctx, book, key)
}

func (r *recordingCentralBankTx) ListTransactions(ctx context.Context, book ledger.BookID) ([]ledger.Transaction, error) {
	r.rec.note(book)
	return r.CentralBankTx.ListTransactions(ctx, book)
}

func (r *recordingCentralBankTx) ListTransactionsForPosition(ctx context.Context, book ledger.BookID, pos ledger.Position) ([]ledger.Transaction, error) {
	r.rec.note(book)
	return r.CentralBankTx.ListTransactionsForPosition(ctx, book, pos)
}

func (r *recordingCentralBankTx) MarkReversed(ctx context.Context, book ledger.BookID, id ledger.TransactionID) error {
	r.rec.note(book)
	return r.CentralBankTx.MarkReversed(ctx, book, id)
}

func (r *recordingCentralBankTx) BookBalance(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction) (ledger.Amount, error) {
	r.rec.note(book)
	return r.CentralBankTx.BookBalance(ctx, book, pos, normal)
}

func (r *recordingCentralBankTx) ValueDateBalance(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction, before time.Time) (ledger.Amount, error) {
	r.rec.note(book)
	return r.CentralBankTx.ValueDateBalance(ctx, book, pos, normal, before)
}

func (r *recordingCentralBankTx) ValueDatedSeries(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction, from, to time.Time) (ledger.Series, error) {
	r.rec.note(book)
	return r.CentralBankTx.ValueDatedSeries(ctx, book, pos, normal, from, to)
}

func (r *recordingCentralBankTx) AppendAudit(ctx context.Context, e ledger.AuditEvent) error {
	r.rec.note(bookOf(e.BookID))
	return r.CentralBankTx.AppendAudit(ctx, e)
}

func (r *recordingCentralBankTx) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	r.rec.note(bookOf(f.BookID))
	return r.CentralBankTx.ListAudit(ctx, f)
}

func (r *recordingCentralBankTx) NextBankCodeSerial(ctx context.Context, book ledger.BookID, country iban.Country) (uint64, error) {
	r.rec.note(book)
	return r.CentralBankTx.NextBankCodeSerial(ctx, book, country)
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
// No signature reveals what the store DOES with a second-position struct carrying
// a BookID. ledger.AppendAudit writes the row under e.BookID and ListAudit selects
// by f.BookID, so the field IS the scope. payment.PutBank writes under this
// institution's own book whatever b.BookID says — a column on that row, not the
// book being written to — and recording it would make a clearing-house handler
// that admits a member look like it had reached into that member's ledger.
//
// The two are indistinguishable from the AST, so the parser's job is that none is
// FORGOTTEN and this table's is to say which is which.
// TestEveryStructCarriedBookIsDecided fails on a candidate with no entry and on an
// entry with no candidate.
var structCarriedBooks = map[string]structCarriedBook{
	"AppendAudit": {Scoping: true},
	"ListAudit":   {Scoping: true},
	"PutBank": {
		Scoping: false,
		Why: "payment/store.go: a bank's row is its own bank's, in its own database, keyed by id alone. " +
			"Bank.BookID names the book the bank owns; it does not scope this write, and it is not " +
			"stored either — a bank's id IS its BIC and IS its book (payment.AsBank), so " +
			"store/sqlite derives the field on the way out and normalises whatever it was written with. " +
			"TestWritingAParticipantTouchesNoBankBook is the evidence for both halves. " +
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
// READ THIS BEFORE CHANGING ANY WANT-LIST BELOW
// ---------------------------------------------------------------------------
//
// A ROW-WRITE reaches touched() through ID ALLOCATION and AUDIT, never through a
// posting. The Put* methods take no book and record nothing themselves, but no row
// is written on its own: the domain allocates its id with NextID(ctx, s.book(), …)
// and writes an audit event under BookID: s.book(). Measured, not assumed —
// OpenCycle writes one ClearingCycle, posts nothing, and records exactly
// [clearing-house]; TestWritingANetworkRowRecordsTheNetworkBook is the pin.
//
// Each want-list names ONE institution and the sets do not overlap: every row has
// exactly one owner, each owner has a database, and payment.Network.book answers
// every "which book?".
//
// Nothing here EVER posts under an institution's row-book. ClearingHouseBook is
// not a chart of accounts and the csm schema has no ledger tables. Settlement
// posts in three places: the netting transaction in the CENTRAL BANK's book, and
// the mirror and creditor legs in each member's own, made by the member itself.
//
// This is a property of today's domain layer, not a structural invariant. What
// makes a row-write visible is that the domain happens to allocate an id and
// append an audit event, and nothing enforces that — a handler that skipped the
// event would record NOTHING and fail with an empty set, which reads exactly like
// an actor that did no work.

// ---------------------------------------------------------------------------
// What each actor reaches
// ---------------------------------------------------------------------------

// assertBooksTouched compares one actor's set of books against the expectation,
// whole and in order. Whole, not "contains": the claim is about the books an actor
// did NOT reach, so checking only for the expected ones would pass on a handler
// that reached every book in the network.
func assertBooksTouched(t *testing.T, who string, got, want []ledger.BookID) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s touched %v, want %v", who, got, want)
	}
}

// TestWhichBooksEachBankActuallyReaches measures which books a credit transfer
// reaches, per actor. The name is a question rather than a claim, because the name
// is the string a failure prints and a reader greps for.
//
// The SUBMITTING bank reaches its own book alone. Both the payment's id and its
// initiated event are drawn under s.book(), the bank whose database the row goes
// into. The CREDITOR's book is NOT in it: building a pacs.008 would otherwise mean
// reading the payee's deposit account out of the PAYEE'S BANK'S register for the
// name on it, on the happy path, every time. A real payer's bank knows the payee's
// name because the payer typed it, so both counterparty names are on the
// instruction and partiesOf reads nothing. The counterparty's BIC is derived from
// its address through this bank's own copy of the routing directory.
//
// The RECEIVING bank reaches its own book and no other: the register searched
// belongs to this actor's own payment.Network and there is no argument to pass a
// different one. That is not redundant with the interfaces in ops.go — an identity
// narrows which BANK a handler acts as and narrows no BookID, since every
// ledger.Tx method still takes the book as an ordinary argument.
func TestWhichBooksEachBankActuallyReaches(t *testing.T) {
	h := newHarness(t) // builds a seeded network over the recording stores

	h.rec.reset()
	h.settle(t, h.submitCreditTransfer(t))

	assertBooksTouched(t, "the payer's bank", h.booksTouchedBy(h.debtorBIC),
		[]ledger.BookID{h.debtorBook})
	assertBooksTouched(t, "the payee's bank", h.booksTouchedBy(h.creditorBIC),
		[]ledger.BookID{h.creditorBook})

	// Neither went near the central bank's book. Only settlement moves reserves and
	// no bank settles, which is the whole distinction between clearing and
	// settlement.
	for _, who := range []iso20022.BIC{h.debtorBIC, h.creditorBIC} {
		if slices.Contains(h.booksTouchedBy(who), payment.CentralBankBook) {
			t.Errorf("%s reached the central bank's book during a credit transfer", who)
		}
	}
}

// TestWhichBooksEachBankReachesInAPull is the same measurement for the other
// direction, and the result is the surprise: the sets are IDENTICAL to the push's,
// bank for bank.
//
// The SUBMITTING bank, here the payee's, reaches [creditorBook] — the payment's id
// and initiated event, the mandate it loads to build the message, and
// SubmitPaymentTx's creditor half, which for a pull is its own customer. The
// DEBTOR's book is absent for the mirror of the push-side reason.
//
// The RECEIVING bank, here the payer's, is the more informative half: it MOVES
// MONEY and its set is still ONE book. The debtor leg is posted here, and every id
// and audit event that posting needs is taken under this bank's own book, as are
// the payment row and its own id and event.
//
// ONE measurement, over a whole business day, taken once the payment is final. It
// has to run that far: the payer's bank is handed the collection only after the
// cycle settles, so a measurement stopping at the cut-off would find that bank
// untouched. A single set is a UNION over both roles, and neither way the pull arm
// can be got wrong survives it — routing the submission to the payer's bank leaves
// the payee's set empty, and relaying by CdtrAgt leaves the payer's empty. Each was
// watched failing.
//
// A two-phase version would be unsafe as well as unnecessary: Deployment.Submit
// sends to the clearing house BEFORE it returns, so the payer's bank may already be
// running its half concurrently with the reset. Drain means "nothing is in flight",
// and a phase boundary needs "nothing has started".
func TestWhichBooksEachBankReachesInAPull(t *testing.T) {
	h := newHarness(t)

	h.rec.reset()
	h.settle(t, h.submitDirectDebit(t))

	assertBooksTouched(t, "the payee's bank, submitting a collection", h.booksTouchedBy(h.creditorBIC),
		[]ledger.BookID{h.creditorBook})
	assertBooksTouched(t, "the payer's bank, answering a collection", h.booksTouchedBy(h.debtorBIC),
		[]ledger.BookID{h.debtorBook})

	// Clearing a collection costs the clearing house exactly what clearing a credit
	// transfer costs it, which is the point of relaying by an address the message
	// carries: no store read to route, and nothing but network rows to clear.
	assertBooksTouched(t, "the clearing house, clearing a collection", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
		[]ledger.BookID{payment.ClearingHouseBook})

	// And the payer's money moved without the central bank being involved. A
	// direct debit is the first flow in this package where a receiving bank
	// posts, so it is the first place that distinction could have been lost.
	for _, who := range []iso20022.BIC{h.debtorBIC, h.creditorBIC} {
		if slices.Contains(h.booksTouchedBy(who), payment.CentralBankBook) {
			t.Errorf("%s reached the central bank's book during a direct debit", who)
		}
	}
}

// THERE IS NO TEST HERE FOR A MISROUTED PAYMENT, and the absence is a result.
//
// An instruction carries an address and a name and has no field for a bank: the
// submitting side comes from the port, and the counterparty's is derived from the
// counterparty's IBAN through the submitting bank's own copy of the scheme's
// routing directory. There is nothing to type wrongly, so there is nothing to
// defend against.
//
// What such a case depended on is still true and measured elsewhere: a bank
// resolves an address in its OWN register and no other, so a message reaching the
// wrong institution has nothing to resolve.
// TestCreditTransferToAnUnknownAccountComesBackAsAC01 is that property from the
// reachable direction, and the book sets here say the answering bank touched
// nobody else's book while it said so.
//
// A payer CAN still name a bank, for an address no directory covers — a card PAN, a
// proxy alias. That door is payment.ErrCounterpartyAgentNotNamed's, and nothing in
// this scheme routes on it.

// TestTheCSMTouchesOnlyItsOwnBook: the clearing house's half writes the payment
// and the cycle and posts nothing.
//
// It reaches ClearingHouseBook and NOTHING else — not through a posting, this
// institution keeping no book of accounts, but through the id AcceptAtCSMTx's
// audit event needs and the event itself. A CSM half that skipped that event would
// record nothing and fail with an empty set, a different bug wearing the same
// failure.
//
// Relaying costs it nothing: the pacs.008 hop reads the creditor's agent out of the
// message and routes on it. A clearing house that looked a payment up to decide
// where to send it could not route a message about a payment it does not hold.
func TestTheCSMTouchesOnlyItsOwnBook(t *testing.T) {
	h := newHarness(t)
	h.rec.reset()
	h.submitCreditTransfer(t)
	h.work(t)

	assertBooksTouched(t, "clearing house", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
		[]ledger.BookID{payment.ClearingHouseBook})
}

// TestTheCSMStillTouchesOnlyItsOwnBookWhenItSettles extends the assertion above
// over a cut-off and the settlement conversation that follows it. Same set, and
// that is the finding: the clearing house nets a batch, builds a pacs.009, reads
// two members to name them in it, and fans the answer out — and none of it leaves
// this institution's own book, because none of it posts and every row it reads is
// its own. csmOps could not have expressed this: the method is on it, and must be.
func TestTheCSMStillTouchesOnlyItsOwnBookWhenItSettles(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)

	h.rec.reset()
	h.closeCycle(t)
	h.work(t)

	assertBooksTouched(t, "the clearing house, reaching a cut-off and instructing settlement",
		h.booksTouchedBy(h.cfg.ClearingHouseBIC), []ledger.BookID{payment.ClearingHouseBook})
}

// TestWhichBooksTheCentralBankReachesWhenItSettles is the per-actor claim about a
// cut-off. The name is a question rather than a claim, for
// TestWhichBooksEachBankActuallyReaches's reason.
//
// A settlement makes three postings and only one is the central bank's: the
// netting transaction in CentralBankBook, moving reserves between the members'
// settlement accounts; and each PARTICIPANT's book twice over, for the mirror leg
// and the creditor leg.
//
// Both member legs are the MEMBER's own acts — the central bank sends a camt.053
// and the member posts its own mirror leg, and the clearing house's ACSC fan-out
// reaches the creditor's bank — so no member's book is in this set at all.
// SettleCycleTx reads the cycle, the roster and its own book, and no payment.
//
// What that cost is stated at the domain call: the ledger's refusal of an overdrawn
// net payer came from the mirror leg, where "Reserve at Central Bank" is an Asset.
// A member's settlement account HERE is a Liability, which checkSufficientBalance
// does not guard, so SettleCycleTx refuses explicitly instead.
func TestWhichBooksTheCentralBankReachesWhenItSettles(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	h.rec.reset()
	h.closeCycle(t)
	h.work(t)

	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("the payment is %v, want Settled — this test measures a settlement that happened", got.Status)
	}
	assertBooksTouched(t, "the central bank, settling a cycle",
		h.booksTouchedBy(h.cfg.CentralBankBIC),
		[]ledger.BookID{payment.CentralBankBook})
}

// TestEachBankBooksItsOwnSettlementAndNoOtherBooks is the counterpart of the
// measurement above: across a cut-off each member bank reaches its OWN book and
// no other.
//
// TestWhichBooksEachBankActuallyReaches makes a claim about a credit transfer up
// to acceptance, and TestWhichBooksEachBankReachesInAPull about a collection.
// Neither says anything about settlement, which is the moment reserves actually
// move — so it is the point in the system's life where a bank reaching into the
// central bank's book, or into the other bank's, would be least visible and most
// wrong.
//
// # Settlement IS a bank's work
//
// Both banks post: the member that owns the book books its own mirror leg. The
// name is a question rather than a claim, for
// TestWhichBooksEachBankActuallyReaches's reason.
//
// # What each bank reaches, and what is ABSENT
//
// Its own book, and only its own. The payer's bank posts once, for the mirror
// leg — suspense against reserve, so the suspense returns to zero. The payee's
// bank posts that leg too and then a second one, the CREDITOR leg, releasing its
// own customer's funds out of that same suspense. Both also mark their OWN copy
// of the payment Settled.
//
// The central bank's book is absent from both, which is the sharpest claim here:
// a member books its own mirror leg from a statement, and never reads the
// account the statement is about.
//
// Both banks write a payment row at a cut-off — each its own, under its own book
// — so the sets are symmetric and neither has an entry that belongs to nobody.
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
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)

	h.rec.reset()
	h.closeCycle(t)
	h.work(t)

	assertBooksTouched(t, "the payer's bank, booking its own settlement",
		h.booksTouchedBy(h.debtorBIC), []ledger.BookID{h.debtorBook})
	assertBooksTouched(t, "the payee's bank, booking its own settlement and paying its customer",
		h.booksTouchedBy(h.creditorBIC), []ledger.BookID{h.creditorBook})
}

// TestWhichBooksAReturnReaches is the last flow's measurement.
//
// The CENTRAL BANK reaches [CentralBankBook] and nothing else.
// payment.SettleReturnTx makes one posting, reversing the movement between the two
// members' settlement accounts, and writes no row and appends no event: it reads
// the roster by BIC, posts once, and reads two balances back. Its durable trace is
// the idempotency key on that posting, which makes a redelivery
// ErrReturnAlreadySettled. A return composed in one unit of work would reach all
// four books, two of them member banks'.
//
// The CLEARING HOUSE reaches its OWN book. It runs two handlers and sends three
// messages and reads almost nothing; the entry is a WRITE, because it keeps its own
// copy of every payment it carries and payment.CompleteReturnTx marks that copy
// Returned. The claim is about BOOKS, not about what the actor learned — a
// clearing-house half that listed the returning bank's ledgers would come out
// holding that BANK's book. The book is not NAMED: a book id is a fact about the
// seed's arithmetic, not about whose book was reached.
//
// The two banks are in TestEachBankBooksItsOwnReturnAndNoOtherBooks. This measures
// over the RETURN only, resetting after the settlement it starts from has drained.
func TestWhichBooksAReturnReaches(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)

	h.rec.reset()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.work(t)

	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("the payment is %v, want Returned — this test measures a return that happened", got.Status)
	}
	assertBooksTouched(t, "the central bank, settling a return",
		h.booksTouchedBy(h.cfg.CentralBankBIC), []ledger.BookID{payment.CentralBankBook})
	assertBooksTouched(t, "the clearing house, carrying a return, its answer and the return again",
		h.booksTouchedBy(h.cfg.ClearingHouseBIC), []ledger.BookID{payment.ClearingHouseBook})
}

// TestEachBankBooksItsOwnReturnAndNoOtherBooks is the counterpart above: across a
// return each member bank reaches its OWN book and no other.
//
// The PAYEE's bank is the returner on a push — it posts the clawback out of its own
// customer's account into its own clearing suspense BEFORE it sends the pacs.004,
// and books its reserve mirror in the same book from the camt.053. The PAYER's bank
// posts the mirror leg from its own statement and then the refund out of that
// suspense. The central bank's book is absent from both: a bank moves its own
// money, and never reads the settlement account the statement is about.
//
// Both banks write a payment row over this window, each under its own book, so the
// sets are symmetric. The bank posting the SECOND customer leg takes the payment to
// Returned, which is a property of posting the finishing leg rather than of side.
//
// A bank that merely RE-READ the payment row would still show only its own book, a
// network-scoped row being invisible to this recorder on a read. The claim is that
// no bank reads or writes any book but its own, not that a bank learns nothing.
func TestEachBankBooksItsOwnReturnAndNoOtherBooks(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)

	h.rec.reset()
	h.returnPayment(t, p.ID, iso20022.ReturnReasonClosedAccountNumber, "account closed")
	h.work(t)

	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("the payment is %v, want Returned — this test measures a return that happened", got.Status)
	}
	assertBooksTouched(t, "the payee's bank, clawing its own customer back before it asks",
		h.booksTouchedBy(h.creditorBIC), []ledger.BookID{h.creditorBook})
	assertBooksTouched(t, "the payer's bank, refunding its own customer after finality",
		h.booksTouchedBy(h.debtorBIC), []ledger.BookID{h.debtorBook})
}

// TestWhichBooksProvisioningReaches is the one measurement here whose subject is
// not a payment. Provisioning a bank is four units of work at three institutions,
// and the claim is that no one of them reached two books:
//
//	founding                  the joining bank's own book
//	the settlement account    CentralBankBook
//	the roster entry          ClearingHouseBook
//	the membership            the joining bank's own book again
//
// It counts units of work rather than actors, and that is the point. A provisioner
// is nobody, so recordingStores attributes its work to no institution — and the
// claim left is the sharper one: "no single unit of work reached two books" is a
// fact about what can COMMIT, which is what the N+2 split rests on.
//
// The central bank's book is reached and not by accident: OpenSettlementAccountTx
// draws an id before the read its idempotency is decided from and appends
// settlement_account.opened afterwards. Neither half is optional — drop the event
// and the account exists in no immutable record; drop the id and the act loses the
// ordering that made its idempotency its own.
//
// It also rules out the two INCUMBENT banks being touched at all. They are in the
// fixture and are not party to this bank's arrival, and unlike the set equalities
// that assertion survives a third bank being added.
func TestWhichBooksProvisioningReaches(t *testing.T) {
	// After the fixture's own two banks, so what is measured is one arrival and
	// not three.
	h := newHarness(t)
	h.rec.reset()

	joiner := h.provision(t, "Nordhaven Bank", "NORDSESSXXX", euroOnly)

	// Every book the four units of work went near, and no more.
	assertBooksTouched(t, "provisioning one bank", h.rec.touched(),
		[]ledger.BookID{joiner.BookID, payment.CentralBankBook, payment.ClearingHouseBook})

	// The claim the split rests on: each unit of work committed against one
	// database, so none of them reached two books.
	if crossed := h.rec.crossings(); len(crossed) != 0 {
		t.Errorf("%d unit(s) of work reached two books: %v; no statement may span two databases", len(crossed), crossed)
	}

	// Four of them, one per act. A fifth would mean an act had split, and three
	// would mean two had merged — either way the composition above is stale.
	if n := h.rec.unitsOfWork(); n != 4 {
		t.Errorf("provisioning opened %d writing units of work, want 4 — one per act", n)
	}

	// And nobody went near the incumbents.
	for _, book := range []ledger.BookID{h.debtorBook, h.creditorBook} {
		if slices.Contains(h.rec.touched(), book) {
			t.Errorf("provisioning a bank reached %s, which is not a party to it", book)
		}
	}
}

// TestTakingCashInReachesOnlyTheBanksOwnBook pins the crossing a deposit would
// otherwise make: posting the funding bank's reserve mirror AND the central bank's
// matching leg in one unit of work. Cash paid in lands in the bank's own vault, and
// putting it on reserve is a LODGEMENT.
//
// No other instrument in this file could see it. The recorder attributes a book to
// the actor whose unit of work reached it, and taking cash in never becomes a
// message: it arrives at Network.Deposit from an operator or a fixture, with no
// institution behind it. So booksTouchedBy has nothing to narrow by, and every
// other measurement here is blind to this call by construction. It drives the
// recorder directly and reads the whole-store set.
//
// A book set alone would not be enough: a deposit that posted NOTHING would also
// touch one book and leave the central bank's alone, so the balance is asserted
// too. The transaction count in the central bank's book catches the halfway state
// where the posting is removed and the READ is not — a
// centralBankAssetsAccountTx call would still put CentralBankBook in the set.
//
// One unit of work: more than one would mean a deposit had acquired a second act,
// which is what a LODGEMENT is.
func TestTakingCashInReachesOnlyTheBanksOwnBook(t *testing.T) {
	h := newHarness(t)

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

// TestALodgementIsTwoBooksInTwoUnitsOfWork says the crossing was CLOSED rather than
// merely moved.
//
// TestTakingCashInReachesOnlyTheBanksOwnBook says the deposit does not reach the
// central bank's book, which on its own is satisfied by a system where reserves
// cannot be funded at all. This is the other side: reserves still get funded, the
// same two pairs of entries still land in the same two books, and they land in TWO
// UNITS OF WORK with a message between them.
//
// The first two assertions are per-ACTOR sets: the lodging bank reaches its OWN
// book and no other, and the central bank reaches its own as a separate actor doing
// its own posting because a camt.050 arrived. The third is the unit-of-work count,
// which is what would catch a "conversation" that was really still one transaction.
//
// The balances are asserted too, since a book set is satisfied by a lodgement that
// posts nothing: the vault down, the bank's own reserve mirror up, and — separately
// — the CENTRAL BANK's record of the same reserve up. That last is read from the
// settlement agent's own book, which is what settlement reads; a lodgement raising
// only the bank's mirror leaves a bank that believes it can settle and cannot.
func TestALodgementIsTwoBooksInTwoUnitsOfWork(t *testing.T) {
	h := newHarness(t)
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
	cbBefore, err := h.cb().ReserveBalance(ctx, h.debtor.BIC, "EUR")
	if err != nil {
		t.Fatalf("ReserveBalance: %v", err)
	}

	h.rec.reset()
	if _, err := h.dep.Lodge(ctx, h.debtor.BIC, "EUR", amount); err != nil {
		t.Fatalf("Lodge: %v", err)
	}
	h.work(t)
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
	cbAfter, err := h.cb().ReserveBalance(ctx, h.debtor.BIC, "EUR")
	if err != nil {
		t.Fatalf("ReserveBalance: %v", err)
	}
	if got, want := cbAfter-cbBefore, amount; got != want {
		t.Errorf("the central bank's record of the reserve rose by %d, want %d; without this the bank "+
			"believes it can settle and the book settlement reads says otherwise", got, want)
	}
}

// TestABankCannotLodgeCashItDoesNotHold is the ledger doing the work rather than a
// second copy of a rule it already states. Vault Cash is an Asset and ledger.Book
// guards Asset accounts against going negative, so a lodgement bigger than the
// vault is ledger.ErrInsufficientBalance — which borrowedReasons maps to AM04 — and
// LodgeReservesTx makes no check of its own. What this pins is that the refusal
// BINDS: nothing is posted, the reserve mirror does not move, and no camt.050 goes
// out. A bank that could lodge cash it did not hold would be creating central-bank
// money out of nothing.
func TestABankCannotLodgeCashItDoesNotHold(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	vault := h.vaultCash(t, h.debtor.ID)
	mirrorBefore := h.reserveMirror(t, h.debtor.ID)
	mark := h.messagesSeen()

	_, err := h.dep.Lodge(ctx, h.debtor.BIC, "EUR", vault+1)
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

// TestTakingCashInReachesNoOtherInstitution is the domain half of the same change.
// A bank's COUNTER has nothing to do with its central bank account: cash paid in
// over the desk is Debit Vault Cash, Credit the customer, one book, one
// institution, and what moves it onward is a LODGEMENT. So a deposit is refused for
// want of a settlement reference by nothing at all.
//
// It asserts the absence directly: no message leaves the network and the settlement
// agent's book does not move. The bank is a provisioned one, because an
// unprovisioned bank can be given no depositor — it mints its customers' addresses
// under a code the settlement agent allocates.
func TestTakingCashInReachesNoOtherInstitution(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	solo := h.provision(t, "Solo Bank", "SOLODEFFXXX", []ledger.AssetCode{"EUR"})
	h.work(t)
	solo = h.getBank(t, solo.ID)

	acct := h.openCustomer(t, solo, "Sole Depositor", "EUR", 0)
	before := h.centralBankTransactionCount(t)
	mark := h.messagesSeen()

	if err := h.bank(solo.BIC).Deposit(ctx, solo.ID, acct.ID, 250_00, "cash over the counter"); err != nil {
		t.Fatalf("Deposit: %v; a bank can open its doors and take money", err)
	}
	if got, want := h.balance(t, solo.ID, acct.ID), ledger.Amount(250_00); got != want {
		t.Errorf("the depositor's balance is %d, want %d", got, want)
	}
	if got, want := h.vaultCash(t, solo.ID), ledger.Amount(250_00); got != want {
		t.Errorf("the bank's vault cash is %d, want %d", got, want)
	}
	if got := h.centralBankTransactionCount(t) - before; got != 0 {
		t.Errorf("taking cash in posted %d transactions in the central bank's book, want none", got)
	}
	if got := h.messagesSeen() - mark; got != 0 {
		t.Errorf("taking cash in put %d messages on the wire, want none", got)
	}
}

// ---------------------------------------------------------------------------
// The guard on the guard
// ---------------------------------------------------------------------------

// TestRecordingTxOverridesEveryBookScopedMethod is the guard on the guard. A
// decorator that missed a method would COMPILE — Go promotes the embedded one — and
// record nothing, so a handler could reach another entity's book through exactly
// the method nobody wrapped while every book assertion here stayed green.
//
// It WALKS each institution's interface embedding chain rather than reading a list
// of files, for the reason it parses rather than holding a list of methods: a list
// is a second copy and a second copy drifts. Walking means a fifth layer embedded
// into a bank's transaction tomorrow is covered on the day it lands. It runs once
// per institution, and it parses THIS file rather than using reflect, which cannot
// tell a method a type declares from one it inherits.
func TestRecordingTxOverridesEveryBookScopedMethod(t *testing.T) {
	for _, in := range institutions {
		t.Run(in.entry, func(t *testing.T) {
			methods := bookScopedIn(t, walkOrFail(t, in.entry).methods)
			if len(methods) == 0 {
				t.Fatalf("walked %s and found no book-scoped methods; the parser is wrong, not the decorator", in.entry)
			}
			want := make([]string, 0, len(methods))
			for _, m := range methods {
				want = append(want, m.Name)
			}
			got := recordingTxMethods(t, in.recorder)

			for _, m := range methods {
				if !slices.Contains(got, m.Name) {
					t.Errorf("%s does not declare %s (%s).\n"+
						"It carries a book, so a handler can reach any book through it.\n"+
						"Embedding promotes it silently and records nothing: declare the override.",
						in.recorder, m.Name, m.Pkg)
				}
			}
			for _, name := range got {
				if !slices.Contains(want, name) {
					t.Errorf("%s declares %s, which is not a book-scoped method reachable through %s.\n"+
						"Either it was renamed in its own layer and this override is now dead, or it\n"+
						"shadows something it should not.", in.recorder, name, in.entry)
				}
			}
		})
	}
}

// TestEveryStructCarriedBookIsDecided stops the second shape being forgotten the
// way it was once: ListAudit went unrecorded behind a comment claiming the audit
// log was "not per-book at all", which nothing could contradict because the parser
// only knew about books in second position. A method whose book travels inside its
// argument must now be DECIDED — recorded as scoping, or excluded with evidence.
//
// PASSENGERS count too: a struct-carried book in a method that already takes its
// book positionally is decided here like any other. "The positional one is the
// scope" is precisely the claim being made, and a claim nobody has to write down is
// a claim nobody has to back.
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

// TestWritingAParticipantTouchesNoBankBook is the evidence behind the one exclusion
// in structCarriedBooks, made falsifiable rather than asserted: Bank.BookID is a
// COLUMN on the row, not the scope of the write, so writing a bank row that NAMES
// some other book must leave that book alone. It asks the store rather than
// trusting the recorder that is itself under test, so if PutBank ever did write
// into b.BookID the structCarriedBooks entry becomes wrong at the same moment.
//
// A bank's store answers for exactly one book, so asking it about another is
// sqlite.ErrNotThisStoresBook rather than an empty page — "not a question this
// database can be asked" rather than "nothing of yours here". The assertion is on
// the refusal, because a test still expecting an empty answer would pass for the
// wrong reason.
//
// Bank.BookID is not a column either: a bank's id IS its BIC and IS its book, so
// store/sqlite writes neither and scanBank derives both from the primary key. A
// disagreeing BookID is silently NORMALISED. Both halves are asserted because they
// fail apart — a store that scoped the write would put rows in the victim's book,
// one that stored the field verbatim would hand back a bank claiming a book nobody
// keeps.
func TestWritingAParticipantTouchesNoBankBook(t *testing.T) {
	clock := func() time.Time { return testTime }
	ctx := context.Background()
	rec := newRecordingStores(testenv.NewSet(t, clock))
	store, err := rec.Bank(ctx, "AURODEFFXXX")
	if err != nil {
		t.Fatalf("opening the bank's store: %v", err)
	}
	victim := ledger.BookID("VERDITMMXXX")

	if err := store.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		return tx.PutBank(ctx, payment.Bank{
			ID:        "AURODEFFXXX",
			Name:      "Aurora Bank",
			BIC:       "AURODEFFXXX",
			BookID:    victim,
			CreatedAt: testTime,
		})
	}); err != nil {
		t.Fatalf("PutBank: %v", err)
	}

	// The book the row names is not one this database answers for, so nothing
	// could have landed in it and the store says so rather than answering empty.
	if err := store.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		if _, err := tx.ListLedgers(ctx, victim); !errors.Is(err, sqlite.ErrNotThisStoresBook) {
			t.Errorf("listing ledgers in %s = %v, want ErrNotThisStoresBook; writing a bank row must not make its BookID reachable here", victim, err)
		}
		if _, err := tx.ListAccounts(ctx, victim); !errors.Is(err, sqlite.ErrNotThisStoresBook) {
			t.Errorf("listing accounts in %s = %v, want ErrNotThisStoresBook", victim, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}

	// And the row itself is readable without naming any book at all, carrying THIS
	// bank's book rather than the one it was written with — the field is derived
	// from the id, so it cannot name somebody else's book even by mistake.
	if err := store.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		p, err := tx.GetBank(ctx, "AURODEFFXXX")
		if err != nil {
			return err
		}
		if p.BookID != "AURODEFFXXX" {
			t.Errorf("bank came back with BookID %q, want its own %q — the field is derived from the id, not stored",
				p.BookID, ledger.BookID("AURODEFFXXX"))
		}
		return nil
	}); err != nil {
		t.Fatalf("GetBank: %v", err)
	}
}

// TestEveryRecordingTxMethodNotesItsBookThenDelegates pins the two halves the
// completeness test cannot see: an override that forgot to note records nothing,
// and one that forgot to delegate would hand every caller a zero value while
// looking perfectly wrapped.
//
// The check is on the SHAPE of the body, which is deliberately uniform: note the
// book, then call through. The note expression is derived from the method's own
// parsed signature, so this is not a second hand-written list. A consequence worth
// naming: a recorder may hold no helper methods — reach for a free function
// instead, as bookOf is.
func TestEveryRecordingTxMethodNotesItsBookThenDelegates(t *testing.T) {
	for _, in := range institutions {
		t.Run(in.entry, func(t *testing.T) {
			everyOverrideNotesThenDelegates(t, in.recorder, in.embedded)
		})
	}
}

func everyOverrideNotesThenDelegates(t *testing.T, recorder, embedded string) {
	t.Helper()
	shape := map[string]bookMethod{}
	for _, m := range bookScopedTxMethods(t) {
		shape[m.Name] = m
	}

	fset, file := parseSourceFile(t, "books_test.go")
	var checked int
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || receiverTypeName(fd) != recorder {
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
			t.Errorf("%s.%s takes no book argument", recorder, fd.Name.Name)
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
			t.Errorf("%s.%s is not two statements; every override notes its book and delegates", recorder, fd.Name.Name)
			continue
		}
		if got := render(t, fset, fd.Body.List[0]); got != wantNote {
			t.Errorf("%s.%s starts with %q, want %q — an override that does not note records nothing",
				recorder, fd.Name.Name, got, wantNote)
		}
		want := "return " + recv + "." + embedded + "." + fd.Name.Name + "(" + strings.Join(names, ", ") + ")"
		if got := render(t, fset, fd.Body.List[1]); got != want {
			t.Errorf("%s.%s ends with %q, want %q — an override that does not delegate returns a zero value",
				recorder, fd.Name.Name, got, want)
		}
	}
	if checked == 0 {
		t.Fatalf("found no methods on %s; the parser is wrong, not the decorator", recorder)
	}
}

// errProbeDone unwinds the probe below without committing anything.
var errProbeDone = errors.New("probe finished")

// TestRecordingStoreRecordsTheBookOfEveryCall is the same claim as the two AST
// tests, made at RUN time and end to end: every book-scoped method, called through
// its institution's recording store, leaves its book and no other in touched().
// Between the AST tests and reality sit Update, the embedding and note itself, and
// this drives all three over every method, because "the wiring works" is a claim
// about the one method nobody thought to spot-check.
//
// Each method gets its OWN unit of work and ends by returning errProbeDone so the
// work rolls back. The arguments are zero values apart from the book, so many calls
// fail, and a sentinel of the probe's own means Update hands back exactly the error
// the callback returned. The store's answer is discarded on purpose: the book is
// noted BEFORE the call goes through, which is what makes every method reachable
// without a fixture apiece, and what the recorder saw survives the rollback.
func TestRecordingStoreRecordsTheBookOfEveryCall(t *testing.T) {
	clock := func() time.Time { return testTime }
	recs := newRecordingStores(testenv.NewSet(t, clock))
	ctx := context.Background()

	// One probe per institution, each opening ITS store's unit of work and
	// handing the transaction over untyped. The three callbacks are the whole
	// difference between them; everything below is one loop.
	bank, err := recs.Bank(ctx, "AURODEFFXXX")
	if err != nil {
		t.Fatalf("opening the bank's store: %v", err)
	}
	probes := []struct {
		entry string
		probe func(func(any) error) error
	}{
		{"payment.BankTx", func(fn func(any) error) error {
			return bank.Update(ctx, func(ctx context.Context, tx payment.BankTx) error { return fn(tx) })
		}},
		{"payment.CsmTx", func(fn func(any) error) error {
			return recs.ClearingHouse().Update(ctx, func(ctx context.Context, tx payment.CsmTx) error { return fn(tx) })
		}},
		{"payment.CentralBankTx", func(fn func(any) error) error {
			return recs.CentralBank().Update(ctx, func(ctx context.Context, tx payment.CentralBankTx) error { return fn(tx) })
		}},
	}

	for _, in := range probes {
		t.Run(in.entry, func(t *testing.T) {
			methods := bookScopedIn(t, walkOrFail(t, in.entry).methods)
			if len(methods) == 0 {
				t.Fatalf("no book-scoped methods parsed for %s; the parser is wrong, not the recorder", in.entry)
			}
			for _, m := range methods {
				recs.reset()
				book := ledger.BookID("book_" + m.Name)
				err := in.probe(func(tx any) error {
					fn := reflect.ValueOf(tx).MethodByName(m.Name)
					if !fn.IsValid() {
						t.Errorf("the recorder over %s has no method %s (%s)", in.entry, m.Name, m.Pkg)
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
					t.Errorf("probing %s (%s): Update returned %v, want the callback's own error back", m.Name, m.Pkg, err)
				}
				got := recs.touched()
				if len(got) != 1 || got[0] != book {
					t.Errorf("calling %s (%s) on book %q recorded %v, want exactly [%s]", m.Name, m.Pkg, book, got, book)
				}
			}
		})
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

// TestACrossBookAuditReadIsRecorded: a handler holding a legitimate Tx can call
// ListAudit with any book's id and read that bank's entire audit trail, Payload
// included. Without the override this records NOTHING. The second half is wider
// still: an AuditFilter with no BookID reads every book at once, which must not
// look like a clean unit of work either — what everyBook is for.
//
// The read is made through ONE bank's store and names ANOTHER bank's book, which
// the split turns into a refusal, so the store's error is discarded and only what
// the recorder saw is asserted. The recorder must notice the attempt whether or not
// any database was ever going to answer; a version insisting on a successful read
// would be testing the schema.
func TestACrossBookAuditReadIsRecorded(t *testing.T) {
	clock := func() time.Time { return testTime }
	recs := newRecordingStores(testenv.NewSet(t, clock))
	ctx := context.Background()
	rec, err := recs.Bank(ctx, "AURODEFFXXX")
	if err != nil {
		t.Fatalf("opening the bank's store: %v", err)
	}
	victim := ledger.BookID("VERDITMMXXX")

	_ = rec.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		_, err := tx.ListAudit(ctx, ledger.AuditFilter{BookID: victim})
		return err
	})
	if got := recs.touched(); !slices.Equal(got, []ledger.BookID{victim}) {
		t.Errorf("a unit of work that reached for %s's audit trail touched %v, want [%s]", victim, got, victim)
	}

	recs.reset()
	_ = rec.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
		_, err := tx.ListAudit(ctx, ledger.AuditFilter{})
		return err
	})
	if got := recs.touched(); !slices.Equal(got, []ledger.BookID{everyBook}) {
		t.Errorf("an unfiltered audit read touched %v, want [%s] — it reads every book, and must not pass for a quiet one", got, everyBook)
	}
}

// TestWritingANetworkRowRecordsTheNetworkBook pins the note above. OpenCycle is the
// smallest institution-scoped write there is: one ClearingCycle row, no posting
// anywhere. It records the clearing house's book, because the cycle's id comes from
// NextID under it and its audit event is appended under it — which is what says a
// book arrives through more than postings.
func TestWritingANetworkRowRecordsTheNetworkBook(t *testing.T) {
	clock := func() time.Time { return testTime }
	rec := newRecordingStores(testenv.NewSet(t, clock))
	net := payment.NewNetworks(rec, clock).ClearingHouse()

	if _, err := net.OpenCycle(context.Background(), payment.SchemeSEPACT); err != nil {
		t.Fatalf("OpenCycle: %v", err)
	}
	if got := rec.touched(); !slices.Equal(got, []ledger.BookID{payment.ClearingHouseBook}) {
		t.Errorf("opening a cycle touched %v, want [%s].\n"+
			"A network-scoped write records its book through NextID and AppendAudit, not through a posting.",
			got, payment.ClearingHouseBook)
	}
}

// TestEveryAuditEventTheDomainAppendsCarriesABook turns bookOf's last sentence from
// a claim into a check. bookOf maps an empty book to everyBook, right for a filter
// and an over-report for an append, and the reason that over-report never fires is
// that every AuditEvent the domain constructs sets BookID. It reads the
// construction sites in the packages the chain walk visited, not a list kept by
// hand. store/storetest is out of range: it builds deliberately odd events to probe
// the store and is not a layer an institution's handler drives.
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

// TestRecordingTxReachesTheStoreUnderneath is the delegation half at run time. A
// decorator that noted and returned a zero value would satisfy every book assertion
// here while quietly detaching the institutions from the store. Probes across three
// layers: NextID has to allocate from real state, GetLedger and GetDepositAccount
// have to bring back their own not-found sentinels, and ListAudit has to come back
// from a store that really ran the query.
//
// The read legs also cover what the per-method probe does not: View wraps its Tx as
// Update does. A read-only unit of work is exactly where a cross-entity read hides,
// so a View handing back the bare Tx would leave the whole read side unrecorded.
// Each leg is its own bank's store reaching its own book, and the final assertion
// is that ONE recorder aggregates across institutions.
func TestRecordingTxReachesTheStoreUnderneath(t *testing.T) {
	clock := func() time.Time { return testTime }
	recs := newRecordingStores(testenv.NewSet(t, clock))
	ctx := context.Background()
	// Two books, and a bank IS its own book, so these are the two banks' addresses
	// — named so that "AURO" sorts before "VERD": touched() is asserted whole,
	// order included.
	written := ledger.BookID("AURODEFFXXX")
	read := ledger.BookID("VERDITMMXXX")
	writer, err := recs.Bank(ctx, "AURODEFFXXX")
	if err != nil {
		t.Fatalf("opening the writing bank's store: %v", err)
	}
	reader, err := recs.Bank(ctx, "VERDITMMXXX")
	if err != nil {
		t.Fatalf("opening the reading bank's store: %v", err)
	}

	err = writer.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
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

	err = reader.View(ctx, func(ctx context.Context, tx payment.BankTx) error {
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

	if got := recs.touched(); !slices.Equal(got, []ledger.BookID{written, read}) {
		t.Errorf("touched() = %v, want [%s %s] — both units of work, at two institutions, sorted", got, written, read)
	}
}

// TestTheRecorderIsSafeForConcurrentActorsAndSortsWhatItSaw makes three claims
// about the recorder's own bookkeeping falsifiable.
//
// Concurrency, because the listeners serve requests on their own goroutines and all
// drive the same recorder: an unlocked map would be a data race in every flow test,
// reported against the flow rather than against this. Sorting, because touched() is
// asserted whole — five books rather than two, so an unsorted result would have to
// hit one permutation in 120. Attribution, because a recorder that noted the book
// and dropped the actor would leave touchedBy empty for everyone, which is exactly
// what an actor that touched nothing looks like.
func TestTheRecorderIsSafeForConcurrentActorsAndSortsWhatItSaw(t *testing.T) {
	rec := newRecordingStores(nil)

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
			rec.note(w.actor, nil, w.book)
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
// Three holes have been found in this one mechanism and all three had the same
// shape: a category the parser could not see, described in a comment as a category
// that did not exist. So the parser REFUSES what it cannot analyse rather than
// skipping it, by name, with the shape spelled out. The three tests below construct
// each refusable shape in a throwaway module and assert the refusal — an unused
// branch cannot be tested, but an ERRORING branch can.
//
// What that buys is not that the parser handles every shape, but that a shape it
// does not handle becomes a decision somebody has to make.

// TestTheParserRefusesAnEmbeddedBookIDField constructs a filter struct that EMBEDS
// ledger.BookID instead of naming it. It is legal Go and compiles this repository
// unchanged, and the promoted field is still spelled f.BookID — but an embedded
// BookID also gives the struct BookID's own identity, and "scope of the call, or an
// embedding for some other purpose" is exactly what no signature answers. Without
// the refusal the method vanishes from the candidate set entirely, carrierOf
// iterating f.Names and an embedded field having none.
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
	w := walkTxChain(root, module, "probe.Tx")
	assertRefused(t, w, "embeds ledger.BookID")
	assertNoCandidate(t, w, "ListThings")
}

// TestTheParserRefusesABookBehindAPointerSliceOrMap constructs the three
// indirections a book could hide behind. Each is legal, each would make
// `note(f.BookID)` wrong or impossible, and each silently produces no candidate
// otherwise. One method per wrapper, so the refusal has to name all three.
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
	w := walkTxChain(root, module, "probe.Tx")
	// A fixed array is named as one rather than called a slice: the refusal
	// quotes the word back at whoever has to act on it.
	for _, want := range []string{"pointer", "slice", "map", "array"} {
		assertRefused(t, w, want)
	}
	for _, name := range []string{"ViaPointer", "ViaSlice", "ViaMap", "ViaArray"} {
		assertNoCandidate(t, w, name)
	}
}

// TestTheParserRefusesABookNestedInAStructField constructs a book one level deeper
// than the parser reads: an argument struct whose FIELD is a struct carrying the
// BookID. The recorder would have to write o.Inner.BookID, and this parser records
// only a top-level field. The detection is transitive and the refusal names the
// path; it deliberately does not invent the path, a book two levels down being the
// structCarriedBooks question again.
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
	w := walkTxChain(root, module, "probe.Tx")
	assertRefused(t, w, "carries a ledger.BookID inside it")
	assertNoCandidate(t, w, "Nested")
}

// TestTheParserRefusesABookPromotedThroughAnUnexportedEmbeddedType: Go promotes an
// unexported embedded type's EXPORTED fields, so `o.BookID` is readable from any
// package even though `inner` is not nameable from one. A version of exportedField
// that tested the embedded type's own name decides `inner` is out of reach and
// skips the field — a silent skip inside the very rule written to make silent skips
// impossible, and the fourth instance of a category the parser could not see.
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
	w := walkTxChain(root, module, "probe.Tx")
	assertRefused(t, w, "carries a ledger.BookID")
	assertNoCandidate(t, w, "Promoted")
}

// TestTheParserFollowsOnlyFieldsTheDecoratorCouldName is the shape that must NOT be
// refused. A book behind an unexported field is not a blind spot, it is out of
// reach — the recorders are written in package main and cannot name it. Refusing it
// would be expensive noise: payment.Bank reaches four books this way, through live
// handles whose types keep their book unexported.
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
	w := walkTxChain(root, module, "probe.Tx")
	if len(w.refusals) != 0 {
		t.Errorf("the parser refused a book behind an unexported field: %v.\n"+
			"It cannot be named from package main, so there is no decision to force.", w.refusals)
	}
	assertNoCandidate(t, w, "Hidden")
}

// TestTheProbeModuleItselfParsesCleanly is the control the three refusal tests
// need: without it they would all still pass if probeModule produced something the
// parser refused for an unrelated reason.
func TestTheProbeModuleItselfParsesCleanly(t *testing.T) {
	root, module := probeModule(t, `
type Tx interface {
	Ordinary(ctx context.Context, book ledger.BookID, id string) error
	NotBookScoped(ctx context.Context, id string) error
}
`)
	w := walkTxChain(root, module, "probe.Tx")
	if len(w.refusals) != 0 {
		t.Fatalf("the probe module refused an ordinary shape: %v", w.refusals)
	}
	if len(w.methods) != 1 || w.methods[0].Name != "Ordinary" || w.methods[0].Carry != bookIsTheArg {
		t.Fatalf("probe found %+v, want exactly Ordinary carrying its book positionally", w.methods)
	}
}

// probeModule writes a throwaway module laid out like this one — a ledger package
// declaring BookID, and a package declaring a Tx interface — so the parser's
// refusals can be tested on shapes that do not occur here. The files are only ever
// parsed, never compiled, and live in the test's temporary directory.
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
	// argument of a method that already takes `book ledger.BookID` at argument 1.
	// Recorded rather than ignored because "the row merely records the book the
	// argument already chose" is a claim about the STORE, which no signature makes.
	PassengerArg, PassengerField string
}

// chainWalk is one traversal of a Tx embedding chain: what it found, what it
// refused, and which packages it visited. It takes no *testing.T on purpose — the
// refusals are DATA, which is what lets the three tests above assert on the outcome
// instead of watching the suite fail.
type chainWalk struct {
	root   string // the directory holding go.mod
	module string

	methods  []bookMethod
	refusals []string
	dirs     []string // every package the chain reached, in visit order

	cache   map[string]*pkgAST
	seen    map[string]bool // one interface, by dir and name
	visited map[string]bool // one package, so dirs holds each once
}

// txBookCandidates is every method reachable through any of the three institutions'
// transaction types whose second argument carries a book, in either shape: it IS a
// ledger.BookID, or it is a struct with a field of that type. Both shapes exist and
// the parser must know both, a parser that knew only the first being precisely what
// let ListAudit go unrecorded behind a comment asserting it was not per-book.
//
// Whether shape 2 actually SCOPES the operation is not visible from any signature —
// see structCarriedBooks. This function's job is only that none is FORGOTTEN.
//
// Four deliberate properties of the rule:
//
//   - Type and position, not name. A name is documentation and a type is a fact.
//   - Either spelling of the type, resolved properly: a bare `BookID` counts only
//     inside package ledger, and a qualified one is resolved through the file's own
//     import block. The embed walk already honours aliases, and two halves of one
//     walk disagreeing is how a whole layer drops out silently.
//   - A book carried anywhere OTHER than second is refused, not skipped.
//   - A book carried in a shape this parser does not read is REFUSED.
//
// Methods carrying no book at all are correctly absent: ledger's Now, and the
// readers and writers keyed under their own institution's book.
//
// It walks rather than reading a list of files, because a hand-listed set is a
// second copy of the layering and drifts: reading ledger/store.go alone would call
// a decorator complete while deposit, product and lending were unrecorded.
func txBookCandidates(t *testing.T) []bookMethod {
	t.Helper()
	return realChainWalk(t).methods
}

// realChainWalk walks this repository's own chain and fails the test on any
// refusal. The refusals are only data to the walk; here is where they become
// failures.
func realChainWalk(t *testing.T) *chainWalk {
	t.Helper()
	entries := make([]string, 0, len(institutions))
	for _, in := range institutions {
		entries = append(entries, in.entry)
	}
	return walkOrFail(t, entries...)
}

// institutions is the three transaction chains and the recorder over each. There
// are three because there is one transaction type per institution: a clearing
// house's unit of work cannot name a deposit account, so there is no single chain
// to walk and no single decorator to write.
var institutions = []struct {
	entry    string // the package.Interface the chain starts at
	recorder string // the decorator over it, declared in this file
	embedded string // the field each override delegates through
}{
	{"payment.BankTx", "recordingBankTx", "BankTx"},
	{"payment.CsmTx", "recordingCsmTx", "CsmTx"},
	{"payment.CentralBankTx", "recordingCentralBankTx", "CentralBankTx"},
}

// walkOrFail walks the given entry points and turns every refusal into a
// failure. The refusals are only data to the walk; here is where they bite.
func walkOrFail(t *testing.T, entries ...string) *chainWalk {
	t.Helper()
	w := walkTxChain(repoRoot(t), modulePath(t), entries...)
	for _, r := range w.refusals {
		t.Error(r)
	}
	return w
}

// bookScopedTxMethods is the subset of the candidates a recorder must
// override: every positional one, and every struct-carried one that
// structCarriedBooks says really scopes its operation.
func bookScopedTxMethods(t *testing.T) []bookMethod {
	t.Helper()
	return bookScopedIn(t, txBookCandidates(t))
}

// bookScopedIn is bookScopedTxMethods over one institution's candidates.
func bookScopedIn(t *testing.T, candidates []bookMethod) []bookMethod {
	t.Helper()
	var out []bookMethod
	for _, m := range candidates {
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

// walkTxChain follows the transaction interfaces' embedding chain from one or more
// entry points, each written "package.Interface". It takes SEVERAL because an
// institution has its own transaction type and what the recorder must cover is the
// union. A capability two institutions share is reached down two chains and walked
// once, which is what keeps the duplicate check below meaningful.
func walkTxChain(root, module string, entries ...string) *chainWalk {
	w := &chainWalk{
		root:    root,
		module:  module,
		cache:   map[string]*pkgAST{},
		seen:    map[string]bool{},
		visited: map[string]bool{},
	}
	for _, entry := range entries {
		pkg, name, ok := strings.Cut(entry, ".")
		if !ok {
			w.refusef("entry %q is not written package.Interface", entry)
			continue
		}
		w.walk(filepath.Join(root, pkg), name)
	}

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

// walk reads one interface and recurses into everything it embeds. An embedded
// interface is named either bare — a capability declared beside its user — or
// qualified. Both are followed; anything else is REFUSED rather than skipped.
func (w *chainWalk) walk(dir, name string) {
	key := dir + "." + name
	if w.seen[key] {
		return // ledger.Tx is reached through product and through lending
	}
	w.seen[key] = true

	pkg := w.loadPackage(dir)
	if !w.visited[dir] {
		w.visited[dir] = true
		w.dirs = append(w.dirs, dir)
	}
	iface, ok := pkg.ifaces[name]
	if !ok {
		w.refusef("no `type %s interface` found in %s", name, dir)
		return
	}
	for _, f := range iface.typ.Methods.List {
		// An embedded interface has no name. Recurse into whatever declares it,
		// resolving a qualifier through the importing file's own import block
		// rather than assuming the alias is the directory.
		if len(f.Names) == 0 {
			switch embedded := f.Type.(type) {
			case *ast.Ident:
				w.walk(dir, embedded.Name)
			case *ast.SelectorExpr:
				qualifier, ok := embedded.X.(*ast.Ident)
				if !ok {
					w.refusef("%s/%s embeds %s, which this walk does not understand", dir, name, exprString(f.Type))
					continue
				}
				path, ok := iface.imports[qualifier.Name]
				if !ok {
					w.refusef("%s/%s embeds %s but the file imports no %s", dir, name, exprString(f.Type), qualifier.Name)
					continue
				}
				rel, inModule := strings.CutPrefix(path, w.module+"/")
				if !inModule {
					w.refusef("%s/%s embeds %s, which is outside this module", dir, name, path)
					continue
				}
				w.walk(filepath.Join(w.root, rel), embedded.Sel.Name)
			default:
				w.refusef("%s/%s embeds %s, which this walk does not understand", dir, name, exprString(f.Type))
			}
			continue
		}
		fn, ok := f.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		if m := w.classify(pkg, iface.imports, f.Names[0].Name, fn); m.Carry != noBook {
			w.methods = append(w.methods, m)
		}
	}
}

// classify decides how one interface method carries its book, refusing a book
// carried anywhere but straight after ctx.
//
// The one exception is PutSettlementAdvice's shape: a method that already takes
// `book ledger.BookID` at argument 1 and is then handed a ROW naming its own book.
// That is not a second position for the scope to hide in, so it is not refused —
// but it is not waved through either. Whether the row's field merely records the
// book the argument chose is a claim about the STORE that no signature makes, so it
// is recorded as a PASSENGER and must be decided in structCarriedBooks.
func (w *chainWalk) classify(pkg *pkgAST, imports map[string]string, name string, fn *ast.FuncType) bookMethod {
	found := bookMethod{Pkg: filepath.Base(pkg.dir), Name: name}
	for i, p := range flatParams(fn) {
		ref := typeRef{expr: p.typ, imports: imports, pkg: pkg}
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

	// A book behind an indirection. The recorder writes `note(f.BookID)`, which a
	// pointer could nil-dereference and a slice or map could hold several answers to.
	// Analysable in principle; refused rather than skipped.
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

		// An embedded field has no name. Its promoted selector happens to be spelled
		// BookID, so this could be handled — but an embedded BookID also hands the
		// struct that type's identity, and whether it is the scope of the call is
		// exactly the question no signature answers.
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
// pointers, slices, maps and in-module struct fields to any depth. It answers only
// "is one in there", never "where": knowing a book is hidden is enough to refuse,
// and working out the path would be guessing at intent.
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
// The traversal follows exported fields only, which is a statement about
// reachability rather than a convenience: the recorders are in package main and
// cannot name an unexported field of a type from anywhere else, so there is nothing
// to "decide".
//
// Nor is it a crossing left open, and payment.Bank proves it. Its Ledger, Deposit,
// Lending and Catalogue fields are live handles whose types keep the book in an
// unexported field, so the transitive scan reaches four books through them — but a
// handler using one would call through to the Tx, and every such method IS
// recorded. None caches anything, Network.bind builds all four over the same
// transaction, the recording stores wrap Update AND View, and the stores are
// separate packages. The Tx is the choke point; a live handle is a route TO it.
// That holds until some entity gets a handle answering from a cache.
//
// An EMBEDDED field is always followed, whatever its type's name: Go promotes an
// unexported embedded type's EXPORTED fields, so the book is reachable even though
// the thing holding it is not nameable. The recursion applies the same rule to the
// promoted struct's own fields.
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

// isBookID reports whether a type expression is ledger.BookID. A bare `BookID`
// counts only inside package ledger, and a qualified one is resolved through the
// writing file's own imports — never by comparing the qualifier to "ledger", which
// would miss an aliased import and silently drop every method in that layer.
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

// ifaceDecl is one interface type declaration, with the imports of the file that
// declares it — which is what resolves a qualifier in an embedded interface or in
// a method's arguments.
type ifaceDecl struct {
	typ     *ast.InterfaceType
	imports map[string]string
}

// pkgAST is one package as this walk needs it: every interface it declares, every
// struct type it declares, and its files.
type pkgAST struct {
	dir     string
	path    string
	ifaces  map[string]ifaceDecl
	structs map[string]structDecl
	files   []parsedFile
}

// loadPackage parses every non-test file in a package directory once. It publishes
// to the cache only when the package is FULLY built: nothing re-enters, Go
// forbidding import cycles, and a half-built entry that is safe only because nobody
// happens to touch it is what this file is meant not to contain.
func (w *chainWalk) loadPackage(dir string) *pkgAST {
	if p, ok := w.cache[dir]; ok {
		return p
	}
	rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(dir, w.root)), "/")
	pkg := &pkgAST{
		dir:     dir,
		path:    w.module + "/" + rel,
		ifaces:  map[string]ifaceDecl{},
		structs: map[string]structDecl{},
	}

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
					pkg.ifaces[ts.Name.Name] = ifaceDecl{typ: typ, imports: imports}
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

// repoRoot is the directory holding go.mod, found by walking up from the directory
// the test binary runs in. Found rather than spelled, because a relative depth is a
// fact about where this file sits: a ".." one level short reports a missing go.mod,
// which reads as a broken checkout rather than as a file that moved.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod at or above %s", dir)
		}
		dir = parent
	}
}

// modulePath is this repository's module path, read from go.mod so that an
// import path can be turned into a directory without hard-coding it.
func modulePath(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
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

// recordingTxMethods is every method declared DIRECTLY on one recorder in this
// file. Declared, not promoted — which is the whole point, and the reason this
// reads the source instead of asking reflect.
func recordingTxMethods(t *testing.T, recorder string) []string {
	t.Helper()
	_, file := parseSourceFile(t, "books_test.go")
	var out []string
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && receiverTypeName(fd) == recorder {
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

func parseSourceFile(t *testing.T, path string) (*token.FileSet, *ast.File) {
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
