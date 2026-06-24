package deposit

import (
	"fmt"
	"sync"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Register is the demand-deposit layer over a general ledger. It manages
// customer deposit accounts, their status lifecycle, authorization holds, the
// available balance those holds reduce, and end-of-day snapshots.
//
// # Relationship to the ledger
//
// Every deposit account wraps a backing Liability account in the underlying
// ledger.Book. The Register never stores money itself: opening an account
// creates a GL account, and capturing a hold posts a real GL transaction.
// Holds and snapshots are operational state tracked only here.
//
// # Thread Safety
//
// All public methods on Register are safe for concurrent use. Internally, a
// read-write mutex protects the Register's own state; the backing ledger.Book
// has its own lock.
type Register struct {
	mu sync.RWMutex

	// book is the underlying general ledger backing the deposit accounts.
	book *ledger.Book

	// accounts stores deposit accounts, keyed by ID.
	accounts map[AccountID]*Account

	// holds stores holds, keyed by ID.
	holds map[HoldID]*Hold

	// accountHolds maps account IDs to their hold IDs, enabling efficient
	// lookup of holds per account.
	accountHolds map[AccountID][]HoldID

	// snapshots stores end-of-day balance snapshots.
	// Structure: accountID -> dateKey -> snapshot.
	snapshots map[AccountID]map[string]*Snapshot

	// auditLog is an append-only log of all mutations in this layer.
	auditLog []*AuditEvent

	// idCounter is a simple monotonic counter for generating unique IDs.
	idCounter int64

	// clock is the time source. Override in tests to control time.
	clock func() time.Time
}

// NewRegister creates a new deposit register backed by the given general
// ledger. It uses time.Now as its clock.
func NewRegister(book *ledger.Book) *Register {
	return NewRegisterWithClock(book, time.Now)
}

// NewRegisterWithClock creates a new deposit register that reads the current
// time from the provided clock function instead of time.Now. Share the clock
// with the backing ledger.Book so that audit timestamps and snapshot dates
// line up across layers.
func NewRegisterWithClock(book *ledger.Book, clock func() time.Time) *Register {
	return &Register{
		book:         book,
		accounts:     make(map[AccountID]*Account),
		holds:        make(map[HoldID]*Hold),
		accountHolds: make(map[AccountID][]HoldID),
		snapshots:    make(map[AccountID]map[string]*Snapshot),
		clock:        clock,
	}
}

// nextID generates a unique ID with the given prefix.
func (r *Register) nextID(prefix string) string {
	r.idCounter++
	return fmt.Sprintf("%s_%d", prefix, r.idCounter)
}

// now returns the current time using the register's clock.
func (r *Register) now() time.Time {
	return r.clock()
}

// appendAudit records an event in the immutable audit log.
func (r *Register) appendAudit(eventType AuditEventType, entityID string, payload any) {
	r.auditLog = append(r.auditLog, &AuditEvent{
		ID:        r.nextID("evt"),
		Timestamp: r.now(),
		Type:      eventType,
		EntityID:  entityID,
		Payload:   payload,
	})
}

// ---------------------------------------------------------------------------
// Account Management
// ---------------------------------------------------------------------------

// OpenAccount opens a new customer deposit account. It creates a backing
// Liability account in the general ledger under the given subledger, then
// records the deposit account in the Active state.
//
// overdraftLimit is a positive amount the account may go below zero by; 0
// means no overdraft is permitted.
//
// Returns any error from the underlying ledger (for example
// ledger.ErrSubledgerNotFound if the subledger does not exist).
func (r *Register) OpenAccount(subledger ledger.SubledgerID, name string, overdraftLimit ledger.Amount) (Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	gl, err := r.book.CreateAccount(subledger, name, ledger.Liability)
	if err != nil {
		return Account{}, err
	}

	acct := &Account{
		ID:             AccountID(r.nextID("dep")),
		GLAccount:      gl.ID,
		Name:           name,
		Status:         Active,
		OverdraftLimit: overdraftLimit,
		CreatedAt:      r.now(),
	}
	r.accounts[acct.ID] = acct
	r.appendAudit(EventAccountOpened, string(acct.ID), acct)
	return *acct, nil
}

// GetAccount retrieves a deposit account by its ID.
// Returns ErrAccountNotFound if the account does not exist.
func (r *Register) GetAccount(id AccountID) (Account, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	acct, ok := r.accounts[id]
	if !ok {
		return Account{}, ErrAccountNotFound
	}
	return *acct, nil
}

// ---------------------------------------------------------------------------
// Status Lifecycle
// ---------------------------------------------------------------------------

// Freeze transitions an account from Active to Frozen, blocking new holds and
// withdrawals until it is unfrozen.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrInvalidStatusTransition if the account is not Active.
func (r *Register) Freeze(id AccountID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	acct, ok := r.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}
	if acct.Status != Active {
		return ErrInvalidStatusTransition
	}
	acct.Status = Frozen
	r.appendAudit(EventAccountFrozen, string(acct.ID), acct)
	return nil
}

// Unfreeze transitions an account from Frozen back to Active.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrInvalidStatusTransition if the account is not Frozen.
func (r *Register) Unfreeze(id AccountID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	acct, ok := r.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}
	if acct.Status != Frozen {
		return ErrInvalidStatusTransition
	}
	acct.Status = Active
	r.appendAudit(EventAccountUnfrozen, string(acct.ID), acct)
	return nil
}

// MarkDormant transitions an account from Active to Dormant, reflecting a
// prolonged absence of customer activity.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrInvalidStatusTransition if the account is not Active.
func (r *Register) MarkDormant(id AccountID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	acct, ok := r.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}
	if acct.Status != Active {
		return ErrInvalidStatusTransition
	}
	acct.Status = Dormant
	r.appendAudit(EventAccountDormant, string(acct.ID), acct)
	return nil
}

// Reactivate transitions an account from Dormant back to Active.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrInvalidStatusTransition if the account is not Dormant.
func (r *Register) Reactivate(id AccountID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	acct, ok := r.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}
	if acct.Status != Dormant {
		return ErrInvalidStatusTransition
	}
	acct.Status = Active
	r.appendAudit(EventAccountReactivated, string(acct.ID), acct)
	return nil
}

// Close permanently closes an account. Closed is a terminal state.
//
// An account can only be closed when its backing GL book balance is zero;
// otherwise ErrAccountNotEmpty is returned. Closing is permitted from any
// non-Closed state.
//
// Returns ErrAccountNotFound if the account does not exist,
// ErrInvalidStatusTransition if the account is already Closed, or
// ErrAccountNotEmpty if its balance is non-zero.
func (r *Register) Close(id AccountID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	acct, ok := r.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}
	if acct.Status == Closed {
		return ErrInvalidStatusTransition
	}

	book, err := r.book.BookBalance(acct.GLAccount)
	if err != nil {
		return err
	}
	if book != 0 {
		return ErrAccountNotEmpty
	}

	acct.Status = Closed
	r.appendAudit(EventAccountClosed, string(acct.ID), acct)
	return nil
}

// ---------------------------------------------------------------------------
// Holds (Pending Authorizations)
// ---------------------------------------------------------------------------

// CreateHoldRequest contains the parameters needed to create a hold.
type CreateHoldRequest struct {
	// AccountID is the deposit account whose available balance will be reduced.
	AccountID AccountID

	// Amount is the positive hold amount in minor currency units.
	Amount ledger.Amount

	// ExpiresAt is when the hold automatically becomes void. Expired holds no
	// longer affect the available balance. If zero, the hold does not expire.
	ExpiresAt time.Time

	// Description is a human-readable description of the hold.
	Description string
}

// CreateHold places an authorization hold on a deposit account, reducing its
// available balance without affecting the book balance.
//
// Returns:
//   - ErrAccountNotFound if the account does not exist.
//   - ErrAccountFrozen if the account is frozen.
//   - ErrAccountClosed if the account is closed.
//   - ErrInvalidStatusTransition if the account is dormant.
//   - ErrInvalidAmount if the amount is not positive.
//   - ErrInsufficientAvailable if the hold would overdraw the available balance.
func (r *Register) CreateHold(req CreateHoldRequest) (Hold, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acct, ok := r.accounts[req.AccountID]
	if !ok {
		return Hold{}, ErrAccountNotFound
	}
	if err := r.requireActive(acct); err != nil {
		return Hold{}, err
	}
	if req.Amount <= 0 {
		return Hold{}, ErrInvalidAmount
	}

	available, err := r.availableLocked(acct)
	if err != nil {
		return Hold{}, err
	}
	if available-req.Amount < 0 {
		return Hold{}, ErrInsufficientAvailable
	}

	now := r.now()
	h := &Hold{
		ID:          HoldID(r.nextID("hld")),
		AccountID:   req.AccountID,
		Amount:      req.Amount,
		ExpiresAt:   req.ExpiresAt,
		Description: req.Description,
		Status:      HoldActive,
		CreatedAt:   now,
	}

	r.holds[h.ID] = h
	r.accountHolds[req.AccountID] = append(r.accountHolds[req.AccountID], h.ID)
	r.appendAudit(EventHoldCreated, string(h.ID), h)
	return *h, nil
}

// ReleaseHold cancels an active hold, restoring the available balance.
//
// Returns:
//   - ErrHoldNotFound if the hold does not exist.
//   - ErrHoldNotActive if the hold has already been released or captured.
func (r *Register) ReleaseHold(id HoldID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	h, ok := r.holds[id]
	if !ok {
		return ErrHoldNotFound
	}
	if h.Status != HoldActive {
		return ErrHoldNotActive
	}

	h.Status = HoldReleased
	r.appendAudit(EventHoldReleased, string(h.ID), h)
	return nil
}

// CaptureHold converts an active hold into a posted general-ledger
// transaction. The deposit account's GL account is a Liability; capturing
// (money leaving the customer) DEBITS that liability account and CREDITs the
// counterparty.
//
// If captureAmount is zero or negative, the hold amount is used. The hold is
// marked as Captured regardless of the amount.
//
// Returns:
//   - ErrHoldNotFound if the hold does not exist.
//   - ErrHoldNotActive if the hold has already been released or captured.
//   - ErrAccountNotFound if the deposit account no longer exists.
//   - any error from the underlying ledger posting.
func (r *Register) CaptureHold(id HoldID, counterparty ledger.AccountID, captureAmount ledger.Amount, description string) (ledger.Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	h, ok := r.holds[id]
	if !ok {
		return ledger.Transaction{}, ErrHoldNotFound
	}
	if h.Status != HoldActive {
		return ledger.Transaction{}, ErrHoldNotActive
	}
	acct, ok := r.accounts[h.AccountID]
	if !ok {
		return ledger.Transaction{}, ErrAccountNotFound
	}

	if captureAmount <= 0 {
		captureAmount = h.Amount
	}

	tx, err := r.book.PostTransaction(ledger.PostTransactionRequest{
		Description: description,
		Entries: []ledger.Entry{
			{AccountID: acct.GLAccount, Amount: captureAmount, Direction: ledger.Debit},
			{AccountID: counterparty, Amount: captureAmount, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return ledger.Transaction{}, err
	}

	h.Status = HoldCaptured
	r.appendAudit(EventHoldCaptured, string(h.ID), map[string]string{
		"hold_id":        string(h.ID),
		"transaction_id": string(tx.ID),
	})
	return tx, nil
}

// GetHold retrieves a hold by its ID.
// Returns ErrHoldNotFound if the hold does not exist.
func (r *Register) GetHold(id HoldID) (Hold, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	h, ok := r.holds[id]
	if !ok {
		return Hold{}, ErrHoldNotFound
	}
	return *h, nil
}

// ---------------------------------------------------------------------------
// Balance Queries
// ---------------------------------------------------------------------------

// GetBalance computes the current balance of a deposit account.
//
// The balance has three components:
//
//   - Book: the GL book balance of the backing Liability account.
//   - Holds: the sum of active, non-expired holds.
//   - Available: Book - Holds + OverdraftLimit.
//
// Returns ErrAccountNotFound if the account does not exist.
func (r *Register) GetBalance(id AccountID) (Balance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	acct, ok := r.accounts[id]
	if !ok {
		return Balance{}, ErrAccountNotFound
	}

	book, err := r.book.BookBalance(acct.GLAccount)
	if err != nil {
		return Balance{}, err
	}
	holds := r.computeActiveHolds(id)

	return Balance{
		Book:      book,
		Holds:     holds,
		Available: book - holds + acct.OverdraftLimit,
	}, nil
}

// CheckWithdrawal reports whether the account may currently support a
// withdrawal of amount. It is status-aware: a frozen account returns
// ErrAccountFrozen and a closed account returns ErrAccountClosed.
//
// The withdrawal is permitted only if Available - amount >= 0, where
// Available = Book - Holds + OverdraftLimit; otherwise
// ErrInsufficientAvailable is returned.
//
// Returns ErrAccountNotFound if the account does not exist.
func (r *Register) CheckWithdrawal(id AccountID, amount ledger.Amount) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	acct, ok := r.accounts[id]
	if !ok {
		return ErrAccountNotFound
	}
	if err := r.requireActive(acct); err != nil {
		return err
	}

	available, err := r.availableLocked(acct)
	if err != nil {
		return err
	}
	if available-amount < 0 {
		return ErrInsufficientAvailable
	}
	return nil
}

// requireActive returns a status-specific error if the account is not Active.
// The caller must hold r.mu.
func (r *Register) requireActive(acct *Account) error {
	switch acct.Status {
	case Active:
		return nil
	case Frozen:
		return ErrAccountFrozen
	case Closed:
		return ErrAccountClosed
	default:
		return ErrInvalidStatusTransition
	}
}

// availableLocked computes the available balance of an account:
// Book - Holds + OverdraftLimit. The caller must hold r.mu.
func (r *Register) availableLocked(acct *Account) (ledger.Amount, error) {
	book, err := r.book.BookBalance(acct.GLAccount)
	if err != nil {
		return 0, err
	}
	return book - r.computeActiveHolds(acct.ID) + acct.OverdraftLimit, nil
}

// computeActiveHolds sums all active, non-expired holds for an account.
// The caller must hold r.mu.
func (r *Register) computeActiveHolds(id AccountID) ledger.Amount {
	var total ledger.Amount
	now := r.now()

	for _, holdID := range r.accountHolds[id] {
		h := r.holds[holdID]
		if h.Status != HoldActive {
			continue
		}
		if r.expired(h, now) {
			continue
		}
		total += h.Amount
	}

	return total
}

// expired reports whether a hold's expiry has passed relative to now. A hold
// with a zero ExpiresAt never expires.
func (r *Register) expired(h *Hold, now time.Time) bool {
	return !h.ExpiresAt.IsZero() && h.ExpiresAt.Before(now)
}

// ---------------------------------------------------------------------------
// End-of-Day Balance Snapshots
// ---------------------------------------------------------------------------

// TakeEndOfDaySnapshot computes and stores the balance snapshot for a deposit
// account on a given business date. If a snapshot already exists for the same
// account/date, it is overwritten.
//
// Returns ErrAccountNotFound if the account does not exist.
func (r *Register) TakeEndOfDaySnapshot(id AccountID, date time.Time) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	acct, ok := r.accounts[id]
	if !ok {
		return Snapshot{}, ErrAccountNotFound
	}

	book, err := r.book.BookBalance(acct.GLAccount)
	if err != nil {
		return Snapshot{}, err
	}
	holds := r.computeActiveHolds(id)

	snap := &Snapshot{
		AccountID: id,
		Date:      date,
		Balance: Balance{
			Book:      book,
			Holds:     holds,
			Available: book - holds + acct.OverdraftLimit,
		},
		TakenAt: r.now(),
	}

	if r.snapshots[id] == nil {
		r.snapshots[id] = make(map[string]*Snapshot)
	}
	dateKey := date.Format("2006-01-02")
	r.snapshots[id][dateKey] = snap

	r.appendAudit(EventSnapshotTaken, string(id), snap)
	return *snap, nil
}

// GetSnapshot retrieves an end-of-day balance snapshot for an account and
// business date.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrSnapshotNotFound if no snapshot exists for the given parameters.
func (r *Register) GetSnapshot(id AccountID, date time.Time) (Snapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, ok := r.accounts[id]; !ok {
		return Snapshot{}, ErrAccountNotFound
	}

	dateKey := date.Format("2006-01-02")
	if byAccount, ok := r.snapshots[id]; ok {
		if snap, ok := byAccount[dateKey]; ok {
			return *snap, nil
		}
	}

	return Snapshot{}, ErrSnapshotNotFound
}

// ---------------------------------------------------------------------------
// Audit Trail
// ---------------------------------------------------------------------------

// GetAuditLog returns all audit events in this layer, ordered chronologically.
// Copies are returned to prevent external mutation.
func (r *Register) GetAuditLog() []AuditEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]AuditEvent, len(r.auditLog))
	for i, e := range r.auditLog {
		result[i] = *e
	}
	return result
}
