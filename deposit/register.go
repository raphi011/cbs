package deposit

import (
	"context"
	"encoding/json"
	"fmt"
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
// # Where the state lives
//
// A Register owns no state of its own. Accounts, holds, snapshots and the audit
// log live in a Store, exactly as the ledger's do — and in the same store, since
// deposit.Tx embeds ledger.Tx. The Register keeps only the store handle, the
// ledger.Book it composes with, the BookID both are scoped to, and its clock.
//
// # Units of work
//
// Every mutating method comes in two forms. The plain form (CaptureHold) wraps a
// single Store.Update. The exported …Tx form (CaptureHoldTx) takes a
// caller-supplied Tx, so the payment layer can drive a deposit operation and its
// own bookkeeping in one atomic unit of work.
//
// A …Tx method must never call a plain method on the Register or on the Book:
// that would open a second unit of work inside the first, which store/mem
// refuses (and, without the guard, would deadlock on its write lock). This is
// why CaptureHoldTx calls Book.PostTransactionTx rather than
// Book.PostTransaction.
//
// # Thread Safety
//
// All public methods on Register are safe for concurrent use; the Store
// provides the isolation.
type Register struct {
	// store owns all persistent state of this layer.
	store Store

	// gl is the general ledger this register composes with. Only its …Tx
	// methods are used from inside a unit of work.
	gl *ledger.Book

	// bookID is the book both layers are scoped to. Every store call carries
	// it, and every audit event this layer writes is stamped with it.
	bookID ledger.BookID

	// clock is the time source. Override in tests to control time.
	clock func() time.Time
}

// NewRegister creates a deposit register over the given store, layered on the
// given general ledger.
//
// id must be book.ID(): the register's rows and the book's rows are scoped by
// the same BookID, which is what lets one Tx read both.
//
// Share the clock with the backing ledger.Book so that audit timestamps and
// snapshot dates line up across layers.
//
// Example:
//
//	s := mem.New(time.Now)
//	book := ledger.NewBook(s, "bank", time.Now)
//	reg := deposit.NewRegister(s.Deposit(), book, "bank", time.Now)
func NewRegister(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time) *Register {
	return &Register{store: store, gl: book, bookID: id, clock: clock}
}

// Store returns the underlying store, so a caller that needs to span several
// layers in one unit of work can open the Update itself and then drive the …Tx
// methods of each layer with the resulting Tx.
func (r *Register) Store() Store { return r.store }

// BookID returns the book this register is scoped to.
func (r *Register) BookID() ledger.BookID { return r.bookID }

// now returns the current time using the register's clock.
func (r *Register) now() time.Time { return r.clock() }

// appendAuditTx records an immutable deposit-scope event through the
// transaction, so an audit event never outlives an operation that rolled back.
//
// payload is marshalled now, not held by reference, so later mutation of the
// entity cannot rewrite history. The event's Seq is assigned by the store, and
// its BookID is the register's, so a deposit event is attributable to the bank
// that made it rather than landing in the log unscoped.
func (r *Register) appendAuditTx(ctx context.Context, tx Tx, eventType, entityID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit %s: marshal payload: %w", eventType, err)
	}
	id, err := tx.NextID(ctx, r.bookID, "evt")
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, ledger.AuditEvent{
		ID:         id,
		BookID:     r.bookID,
		Scope:      ledger.ScopeDeposit,
		Type:       eventType,
		EntityID:   entityID,
		Payload:    raw,
		OccurredAt: r.now(),
	})
}

// ---------------------------------------------------------------------------
// Account Management
// ---------------------------------------------------------------------------

// OpenAccount opens a new customer deposit account. It creates a backing
// Liability account in the general ledger under the given subledger, then
// records the deposit account in the Active state.
//
// asset is the unit the account is denominated in; it must already be
// registered in the underlying book. A customer holding two assets holds two
// accounts.
//
// overdraftLimit is a positive amount the account may go below zero by; 0
// means no overdraft is permitted. The asset comes before it so that the two
// ledger-typed arguments are not adjacent and transposable.
//
// Returns any error from the underlying ledger (for example
// ledger.ErrSubledgerNotFound if the subledger does not exist, or
// ledger.ErrAssetNotFound if the asset is not registered).
func (r *Register) OpenAccount(ctx context.Context, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, overdraftLimit ledger.Amount) (Account, error) {
	var out Account
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.OpenAccountTx(ctx, tx, subledger, name, asset, overdraftLimit)
		return err
	})
	return out, err
}

// OpenAccountTx is OpenAccount within a caller-supplied unit of work. The GL
// account and the deposit account are created through the same Tx, so an
// account can never exist in one layer without the other.
func (r *Register) OpenAccountTx(ctx context.Context, tx Tx, subledger ledger.SubledgerID, name string, asset ledger.AssetCode, overdraftLimit ledger.Amount) (Account, error) {
	if err := ledger.ValidateText("name", name); err != nil {
		return Account{}, err
	}

	gl, err := r.gl.CreateAccountTx(ctx, tx, subledger, name, ledger.Liability, asset)
	if err != nil {
		return Account{}, err
	}

	id, err := tx.NextID(ctx, r.bookID, "dep")
	if err != nil {
		return Account{}, err
	}

	acct := Account{
		ID:             AccountID(id),
		GLAccount:      gl.ID,
		Name:           name,
		Asset:          gl.Asset,
		Status:         Active,
		OverdraftLimit: overdraftLimit,
		CreatedAt:      r.now(),
	}
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return Account{}, err
	}
	if err := r.appendAuditTx(ctx, tx, ledger.EventAccountOpened, string(acct.ID), acct); err != nil {
		return Account{}, err
	}
	return acct, nil
}

// GetAccount retrieves a deposit account by its ID.
// Returns ErrAccountNotFound if the account does not exist.
func (r *Register) GetAccount(ctx context.Context, id AccountID) (Account, error) {
	var out Account
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetDepositAccount(ctx, r.bookID, id)
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Status Lifecycle
// ---------------------------------------------------------------------------

// Freeze transitions an account from Active to Frozen, blocking new holds and
// withdrawals until it is unfrozen.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrInvalidStatusTransition if the account is not Active.
func (r *Register) Freeze(ctx context.Context, id AccountID) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.FreezeTx(ctx, tx, id)
	})
}

// FreezeTx is Freeze within a caller-supplied unit of work.
func (r *Register) FreezeTx(ctx context.Context, tx Tx, id AccountID) error {
	return r.transitionTx(ctx, tx, id, Active, Frozen, ledger.EventAccountFrozen)
}

// Unfreeze transitions an account from Frozen back to Active.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrInvalidStatusTransition if the account is not Frozen.
func (r *Register) Unfreeze(ctx context.Context, id AccountID) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.UnfreezeTx(ctx, tx, id)
	})
}

// UnfreezeTx is Unfreeze within a caller-supplied unit of work.
func (r *Register) UnfreezeTx(ctx context.Context, tx Tx, id AccountID) error {
	return r.transitionTx(ctx, tx, id, Frozen, Active, ledger.EventAccountUnfrozen)
}

// MarkDormant transitions an account from Active to Dormant, reflecting a
// prolonged absence of customer activity.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrInvalidStatusTransition if the account is not Active.
func (r *Register) MarkDormant(ctx context.Context, id AccountID) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.MarkDormantTx(ctx, tx, id)
	})
}

// MarkDormantTx is MarkDormant within a caller-supplied unit of work.
func (r *Register) MarkDormantTx(ctx context.Context, tx Tx, id AccountID) error {
	return r.transitionTx(ctx, tx, id, Active, Dormant, ledger.EventAccountDormant)
}

// Reactivate transitions an account from Dormant back to Active.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrInvalidStatusTransition if the account is not Dormant.
func (r *Register) Reactivate(ctx context.Context, id AccountID) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.ReactivateTx(ctx, tx, id)
	})
}

// ReactivateTx is Reactivate within a caller-supplied unit of work.
func (r *Register) ReactivateTx(ctx context.Context, tx Tx, id AccountID) error {
	return r.transitionTx(ctx, tx, id, Dormant, Active, ledger.EventAccountReactivated)
}

// transitionTx moves an account from one status to another if it is currently
// in the expected one, and records the event. The four simple lifecycle edges
// differ only in their from/to states and event type.
func (r *Register) transitionTx(ctx context.Context, tx Tx, id AccountID, from, to AccountStatus, eventType string) error {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	if acct.Status != from {
		return ErrInvalidStatusTransition
	}
	acct.Status = to
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, eventType, string(acct.ID), acct)
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
func (r *Register) Close(ctx context.Context, id AccountID) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.CloseTx(ctx, tx, id)
	})
}

// CloseTx is Close within a caller-supplied unit of work.
func (r *Register) CloseTx(ctx context.Context, tx Tx, id AccountID) error {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	if acct.Status == Closed {
		return ErrInvalidStatusTransition
	}

	book, err := r.bookBalanceTx(ctx, tx, acct.GLAccount)
	if err != nil {
		return err
	}
	if book != 0 {
		return ErrAccountNotEmpty
	}

	acct.Status = Closed
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventAccountClosed, string(acct.ID), acct)
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
func (r *Register) CreateHold(ctx context.Context, req CreateHoldRequest) (Hold, error) {
	var out Hold
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.CreateHoldTx(ctx, tx, req)
		return err
	})
	return out, err
}

// CreateHoldTx is CreateHold within a caller-supplied unit of work. The
// available-balance check and the hold write happen through the same Tx, so two
// concurrent holds cannot both see the same funds.
func (r *Register) CreateHoldTx(ctx context.Context, tx Tx, req CreateHoldRequest) (Hold, error) {
	if err := ledger.ValidateText("description", req.Description); err != nil {
		return Hold{}, err
	}
	if err := ledger.ValidateText("accountId", string(req.AccountID)); err != nil {
		return Hold{}, err
	}

	acct, err := tx.GetDepositAccount(ctx, r.bookID, req.AccountID)
	if err != nil {
		return Hold{}, err
	}
	if err := requireActive(acct); err != nil {
		return Hold{}, err
	}
	if req.Amount <= 0 {
		return Hold{}, ErrInvalidAmount
	}

	available, err := r.availableTx(ctx, tx, acct)
	if err != nil {
		return Hold{}, err
	}
	if available-req.Amount < 0 {
		return Hold{}, ErrInsufficientAvailable
	}

	id, err := tx.NextID(ctx, r.bookID, "hld")
	if err != nil {
		return Hold{}, err
	}

	h := Hold{
		ID:          HoldID(id),
		AccountID:   req.AccountID,
		Amount:      req.Amount,
		ExpiresAt:   req.ExpiresAt,
		Description: req.Description,
		Status:      HoldActive,
		CreatedAt:   r.now(),
	}
	if err := tx.PutHold(ctx, r.bookID, h); err != nil {
		return Hold{}, err
	}
	if err := r.appendAuditTx(ctx, tx, ledger.EventHoldCreated, string(h.ID), h); err != nil {
		return Hold{}, err
	}
	return h, nil
}

// ReleaseHold cancels an active hold, restoring the available balance.
//
// Returns:
//   - ErrHoldNotFound if the hold does not exist.
//   - ErrHoldNotActive if the hold has already been released or captured.
func (r *Register) ReleaseHold(ctx context.Context, id HoldID) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.ReleaseHoldTx(ctx, tx, id)
	})
}

// ReleaseHoldTx is ReleaseHold within a caller-supplied unit of work.
func (r *Register) ReleaseHoldTx(ctx context.Context, tx Tx, id HoldID) error {
	h, err := tx.GetHold(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	if h.Status != HoldActive {
		return ErrHoldNotActive
	}

	h.Status = HoldReleased
	if err := tx.PutHold(ctx, r.bookID, h); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventHoldReleased, string(h.ID), h)
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
func (r *Register) CaptureHold(ctx context.Context, id HoldID, counterparty ledger.AccountID, captureAmount ledger.Amount, description string) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.CaptureHoldTx(ctx, tx, id, counterparty, captureAmount, description)
		return err
	})
	if err != nil {
		return ledger.Transaction{}, err
	}
	return out, nil
}

// CaptureHoldTx is CaptureHold within a caller-supplied unit of work.
//
// This is the method the whole store split exists for: the hold write and the
// GL posting go through one Tx, so a posting that fails leaves the hold Active
// instead of half-capturing it, and a caller composing this with its own writes
// gets all of them or none.
func (r *Register) CaptureHoldTx(ctx context.Context, tx Tx, id HoldID, counterparty ledger.AccountID, captureAmount ledger.Amount, description string) (ledger.Transaction, error) {
	h, err := tx.GetHold(ctx, r.bookID, id)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if h.Status != HoldActive {
		return ledger.Transaction{}, ErrHoldNotActive
	}
	acct, err := tx.GetDepositAccount(ctx, r.bookID, h.AccountID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if captureAmount <= 0 {
		captureAmount = h.Amount
	}

	// Same tx as the hold write below — both commit or neither does. Note
	// PostTransactionTx, not PostTransaction: the latter would open a second
	// unit of work inside this one.
	glTx, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
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
	if err := tx.PutHold(ctx, r.bookID, h); err != nil {
		return ledger.Transaction{}, err
	}
	if err := r.appendAuditTx(ctx, tx, ledger.EventHoldCaptured, string(h.ID), map[string]string{
		"hold_id":        string(h.ID),
		"transaction_id": string(glTx.ID),
	}); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, nil
}

// GetHold retrieves a hold by its ID.
// Returns ErrHoldNotFound if the hold does not exist.
func (r *Register) GetHold(ctx context.Context, id HoldID) (Hold, error) {
	var out Hold
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetHold(ctx, r.bookID, id)
		return err
	})
	return out, err
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
func (r *Register) GetBalance(ctx context.Context, id AccountID) (Balance, error) {
	var out Balance
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
		if err != nil {
			return err
		}
		out, err = r.balanceTx(ctx, tx, acct)
		return err
	})
	return out, err
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
func (r *Register) CheckWithdrawal(ctx context.Context, id AccountID, amount ledger.Amount) error {
	return r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		return r.CheckWithdrawalTx(ctx, tx, id, amount)
	})
}

// CheckWithdrawalTx is CheckWithdrawal within a caller-supplied unit of work,
// so a caller can check the funds and post the withdrawal atomically.
func (r *Register) CheckWithdrawalTx(ctx context.Context, tx Tx, id AccountID, amount ledger.Amount) error {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	if err := requireActive(acct); err != nil {
		return err
	}

	available, err := r.availableTx(ctx, tx, acct)
	if err != nil {
		return err
	}
	if available-amount < 0 {
		return ErrInsufficientAvailable
	}
	return nil
}

// requireActive returns a status-specific error if the account is not Active.
func requireActive(acct Account) error {
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

// balanceTx computes an account's three balances within a unit of work.
func (r *Register) balanceTx(ctx context.Context, tx Tx, acct Account) (Balance, error) {
	book, err := r.bookBalanceTx(ctx, tx, acct.GLAccount)
	if err != nil {
		return Balance{}, err
	}
	holds, err := tx.ActiveHoldTotal(ctx, r.bookID, acct.ID, r.now())
	if err != nil {
		return Balance{}, err
	}
	return Balance{
		Book:      book,
		Holds:     holds,
		Available: book - holds + acct.OverdraftLimit,
	}, nil
}

// availableTx computes the available balance of an account:
// Book - Holds + OverdraftLimit.
func (r *Register) availableTx(ctx context.Context, tx Tx, acct Account) (ledger.Amount, error) {
	bal, err := r.balanceTx(ctx, tx, acct)
	if err != nil {
		return 0, err
	}
	return bal.Available, nil
}

// bookBalanceTx reads the GL book balance of a backing account within a unit of
// work. It reads the GL account first for its normal direction — the same two
// steps Book.BookBalance takes, done here through the caller's Tx rather than by
// opening a second one.
func (r *Register) bookBalanceTx(ctx context.Context, tx Tx, id ledger.AccountID) (ledger.Amount, error) {
	gl, err := tx.GetAccount(ctx, r.bookID, id)
	if err != nil {
		return 0, err
	}
	return tx.BookBalance(ctx, r.bookID, id, gl.Type.NormalBalance())
}

// ---------------------------------------------------------------------------
// End-of-Day Balance Snapshots
// ---------------------------------------------------------------------------

// TakeEndOfDaySnapshot computes and stores the balance snapshot for a deposit
// account on a given business date. If a snapshot already exists for the same
// account/date, it is overwritten.
//
// Returns ErrAccountNotFound if the account does not exist.
func (r *Register) TakeEndOfDaySnapshot(ctx context.Context, id AccountID, date time.Time) (Snapshot, error) {
	var out Snapshot
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.TakeEndOfDaySnapshotTx(ctx, tx, id, date)
		return err
	})
	return out, err
}

// TakeEndOfDaySnapshotTx is TakeEndOfDaySnapshot within a caller-supplied unit
// of work, so an end-of-day run can snapshot every account atomically.
func (r *Register) TakeEndOfDaySnapshotTx(ctx context.Context, tx Tx, id AccountID, date time.Time) (Snapshot, error) {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return Snapshot{}, err
	}

	bal, err := r.balanceTx(ctx, tx, acct)
	if err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{
		AccountID: id,
		Date:      date,
		Balance:   bal,
		TakenAt:   r.now(),
	}
	if err := tx.PutSnapshot(ctx, r.bookID, snap); err != nil {
		return Snapshot{}, err
	}
	if err := r.appendAuditTx(ctx, tx, ledger.EventSnapshotTaken, string(id), snap); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// GetSnapshot retrieves an end-of-day balance snapshot for an account and
// business date.
//
// Returns ErrAccountNotFound if the account does not exist, or
// ErrSnapshotNotFound if no snapshot exists for the given parameters.
func (r *Register) GetSnapshot(ctx context.Context, id AccountID, date time.Time) (Snapshot, error) {
	var out Snapshot
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetDepositAccount(ctx, r.bookID, id); err != nil {
			return err
		}
		var err error
		out, err = tx.GetSnapshot(ctx, r.bookID, id, SnapshotDateKey(date))
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Audit Trail
// ---------------------------------------------------------------------------

// GetAuditLog returns this register's deposit-scope audit events, ordered by
// Seq.
//
// The ledger below writes into the same log under ScopeLedger; this method
// deliberately narrows to ScopeDeposit and to the register's own book, so it
// reports only the mutations this layer made.
func (r *Register) GetAuditLog(ctx context.Context) ([]ledger.AuditEvent, error) {
	var out []ledger.AuditEvent
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListAudit(ctx, ledger.AuditFilter{BookID: r.bookID, Scope: ledger.ScopeDeposit})
		return err
	})
	return out, err
}
