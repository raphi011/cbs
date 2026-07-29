package deposit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/raphi011/cbs/interest"
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

// receivableSubledgerName is where per-account accrued-interest receivables are
// filed. They are deliberately not in the customer-deposit subledger: that
// folder is the one a bank's total customer deposits is an aggregation over,
// and an Asset account sitting in it would be a permanent invitation to sum the
// wrong set of rows.
const receivableSubledgerName = "Accrued Interest"

// incomeSubledgerName is where interest income is filed.
const incomeSubledgerName = "Income"

// interestIncomeName is the revenue account overdraft interest is earned into,
// one per asset. The asset is in the name because an account and its asset are
// inseparable, and a chart of accounts holding several of each needs to tell
// them apart.
func interestIncomeName(asset ledger.AssetCode) string {
	return "Interest Income (" + string(asset) + ")"
}

// SetOverdraftTerms sets an account's overdraft limit and credit terms.
//
// It is the only way to change a limit after opening; OpenAccount takes one for
// convenience but nothing else did until now.
//
// limit is a positive amount the balance may go below zero by, rate is the
// annual rate charged on the drawn balance up to that limit, and unarranged is
// the rate on anything beyond it. A zero rate means the facility is
// interest-free, which is a real product and is what every account opened
// before this method existed has.
//
// A zero unarranged rate does NOT mean the same thing for the excess. It is a
// surcharge, and its absence means rate applies to the whole drawn balance,
// inside the limit and beyond it alike — never that the part beyond the limit
// is free, which would make exceeding a limit cheaper than respecting it. Hence
// the one combination refused below: an unarranged rate with no arranged one,
// which would price only the excess.
//
// The first non-zero rate creates the account's own accrued-interest-receivable
// GL account. Setting terms again reuses it, including when the rate is set
// back to zero: the account may already hold accrued interest, and discarding
// the receivable would strand it.
//
// Returns ErrAccountNotFound, ErrAccountClosed, ErrInvalidAmount for a negative
// limit, and ErrInvalidRate for a negative rate or an unarranged rate with no
// arranged one.
func (r *Register) SetOverdraftTerms(ctx context.Context, id AccountID, limit ledger.Amount, rate, unarranged interest.Rate, dc interest.DayCount) (Account, error) {
	var out Account
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.SetOverdraftTermsTx(ctx, tx, id, limit, rate, unarranged, dc)
		return err
	})
	return out, err
}

// SetOverdraftTermsTx is SetOverdraftTerms within a caller-supplied unit of work.
func (r *Register) SetOverdraftTermsTx(ctx context.Context, tx Tx, id AccountID, limit ledger.Amount, rate, unarranged interest.Rate, dc interest.DayCount) (Account, error) {
	if limit < 0 {
		return Account{}, ErrInvalidAmount
	}
	if rate < 0 || unarranged < 0 {
		return Account{}, ErrInvalidRate
	}
	if rate == 0 && unarranged > 0 {
		return Account{}, ErrInvalidRate
	}

	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return Account{}, err
	}
	if acct.Status == Closed {
		return Account{}, ErrAccountClosed
	}

	if rate > 0 && acct.InterestGL == "" {
		receivable, err := r.ensureReceivableTx(ctx, tx, acct)
		if err != nil {
			return Account{}, err
		}
		acct.InterestGL = receivable
	}

	acct.OverdraftLimit = limit
	acct.Rate = rate
	acct.UnarrangedRate = unarranged
	acct.DayCount = dc

	// A repricing starts a new recompute window. Prior accrual stays exactly
	// where it is — Accrued is untouched — and only days from here are
	// recomputed, so no past day is ever re-derived at a rate that was not in
	// force on it.
	now := r.now()
	acct.TermsEffectiveFrom = now
	acct.AccruedGross = 0
	acct.LastAccrualDate = now

	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return Account{}, err
	}
	if err := r.appendAuditTx(ctx, tx, ledger.EventOverdraftTermsSet, string(acct.ID), acct); err != nil {
		return Account{}, err
	}
	return acct, nil
}

// customerLedgerIDTx resolves the ledger an account's backing GL account
// lives under, by way of its customer subledger. ensureReceivableTx and
// interestIncomeTx both need this ledger ID to file a sibling subledger next
// to the customer's own — resolving it is the one step they share before
// diverging on which subledger and account to ensure.
func (r *Register) customerLedgerIDTx(ctx context.Context, tx Tx, acct Account) (ledger.LedgerID, error) {
	gl, err := tx.GetAccount(ctx, r.bookID, acct.GLAccount)
	if err != nil {
		return "", err
	}
	customerSub, err := tx.GetSubledger(ctx, r.bookID, gl.SubledgerID)
	if err != nil {
		return "", err
	}
	return customerSub.LedgerID, nil
}

// ensureReceivableTx creates this account's accrued-interest-receivable GL
// account, in its own subledger and its own asset.
//
// One per deposit account, not one shared receivable per bank. A shared account
// would be a stored total whose per-customer detail lives in Account.Accrued —
// a control account, and the duplication this codebase is built without. See
// docs/deposit-accounts-vs-subledger.md.
func (r *Register) ensureReceivableTx(ctx context.Context, tx Tx, acct Account) (ledger.AccountID, error) {
	ledgerID, err := r.customerLedgerIDTx(ctx, tx, acct)
	if err != nil {
		return "", err
	}
	sub, err := r.gl.EnsureSubledgerTx(ctx, tx, ledgerID, receivableSubledgerName)
	if err != nil {
		return "", err
	}
	created, err := r.gl.CreateAccountTx(ctx, tx, sub.ID,
		"Accrued Interest: "+acct.Name+" ("+string(acct.Asset)+")", ledger.Asset, acct.Asset)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// interestIncomeTx resolves the bank's interest-income account for an asset,
// creating it and its subledger on first use.
func (r *Register) interestIncomeTx(ctx context.Context, tx Tx, acct Account) (ledger.AccountID, error) {
	ledgerID, err := r.customerLedgerIDTx(ctx, tx, acct)
	if err != nil {
		return "", err
	}
	sub, err := r.gl.EnsureSubledgerTx(ctx, tx, ledgerID, incomeSubledgerName)
	if err != nil {
		return "", err
	}
	income, err := r.gl.EnsureAccountTx(ctx, tx, sub.ID, interestIncomeName(acct.Asset), ledger.Revenue, acct.Asset)
	if err != nil {
		return "", err
	}
	return income.ID, nil
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
// An account can only be closed when it owes nothing in EITHER direction: its
// backing GL book balance must be zero, and so must the receivable holding its
// accrued overdraft interest. Otherwise ErrAccountNotEmpty is returned. Closing
// is permitted from any non-Closed state.
//
// # Why the receivable counts
//
// An account that was overdrawn, accrued interest and then repaid to zero has a
// zero book balance and a non-zero receivable: interest already recognized as
// income and sitting as a debit in an Asset account. Closing there would strand
// it forever — accrual afterwards skips a closed account and
// ChargeOverdraftInterest refuses one — so the money could never be collected
// and the Asset could never be cleared. The flow is charge, then repay, then
// close. lending.CloseTx applies exactly the same rule to a facility's own
// receivable, for exactly this reason.
//
// The test is the receivable's own book balance, not Accrued.Minor(). A
// capitalization residue is bounded by half a minor unit in either direction
// and is not collectable — Minor() of it rounds to zero, except at an EXACT
// half, where Minor() rounds away from zero to ±1 even though the receivable
// itself is already fully cleared (see ChargeOverdraftInterestTx). Testing
// the record there would lock such an account shut forever: once the balance
// stops moving, further accrual adds nothing and the residue never resolves.
// The receivable's ledger balance is what actually must be settled before
// closing; the record may legitimately disagree with it by a sub-minor-unit
// amount, which is the entire reason Accrued exists at higher precision.
//
// Returns ErrAccountNotFound if the account does not exist,
// ErrInvalidStatusTransition if the account is already Closed, or
// ErrAccountNotEmpty if its balance or its receivable is non-zero.
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
	// An account that never had a rate set has no receivable to settle: there
	// is nothing to read a balance for.
	if acct.InterestGL != "" {
		receivable, err := r.bookBalanceTx(ctx, tx, acct.InterestGL)
		if err != nil {
			return err
		}
		if receivable != 0 {
			return ErrAccountNotEmpty
		}
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

// valueDatedSeriesTx is the value-dated movement history of a GL account over
// [from, to], signed by the account's normal direction. It is the same two
// steps bookBalanceTx takes — read the account for its type, then aggregate —
// done through the caller's Tx.
func (r *Register) valueDatedSeriesTx(ctx context.Context, tx Tx, id ledger.AccountID, from, to time.Time) (ledger.Series, error) {
	gl, err := tx.GetAccount(ctx, r.bookID, id)
	if err != nil {
		return ledger.Series{}, err
	}
	return tx.ValueDatedSeries(ctx, r.bookID, id, gl.Type.NormalBalance(),
		ledger.DayStart(from), ledger.NextDay(to))
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
// Overdraft Accrual
// ---------------------------------------------------------------------------

// AccrueOverdraft accrues interest on an overdrawn account up to a business
// date, and posts the day's income to the general ledger.
//
// # What is posted
//
// The record holds exact interest in micro-minor-units; the ledger holds
// Accrued.Minor() of it in the account's receivable. So the posting is the
// CHANGE in the rounded value, not the period's exact interest:
//
//	day 1   exact 2.0548   rounded 2   post 2
//	day 2   exact 4.1096   rounded 4   post 2
//	day 3   exact 6.1644   rounded 6   post 2
//
// A day on which the rounding does not tick posts nothing at all, which is why
// this returns no transaction: most days there is one, some days there is not,
// and a caller that had to distinguish them would learn nothing useful.
//
// Income is recognized daily rather than at capitalization because accrued
// interest is a real asset, and one that existed only on this record between
// charge dates would understate both assets and income on every date in
// between.
//
// # The accrual base
//
// The overdrawn magnitude of each day's own VALUE-DATED book balance — not the
// available balance, and not today's balance applied backwards. A hold is not
// borrowed money. The base is tiered: the arranged rate up to OverdraftLimit,
// the unarranged rate on anything beyond it.
//
// A gap of several days is therefore exact rather than approximate: every day
// in the span accrues on the balance that was actually in force on it, which is
// what a bank does and what a missed end-of-day used to get wrong.
//
// # Idempotency, and how a backdated posting is corrected
//
// LastAccrualDate never moves backwards, so re-running an end-of-day for a date
// already covered is a no-op rather than a second charge.
//
// That is also why a posting which arrives backdated is trued up by the NEXT
// day's run rather than by rewinding this one. Each run recomputes the whole
// terms window from the value-dated balance, so the day the posting takes
// effect on is re-derived with it in place; the difference between the window's
// new total and its old one is what gets posted. Interest that turns out never
// to have been owed comes back as a correction — a new event, not a reversal:
// the original accrual was a correct statement of what the ledger knew then.
//
// Returns ErrAccountNotFound.
func (r *Register) AccrueOverdraft(ctx context.Context, id AccountID, date time.Time) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.AccrueOverdraftTx(ctx, tx, id, date)
	})
}

// AccrueOverdraftTx is AccrueOverdraft within a caller-supplied unit of work.
func (r *Register) AccrueOverdraftTx(ctx context.Context, tx Tx, id AccountID, date time.Time) error {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	return r.accrueOverdraftAccountTx(ctx, tx, acct, date)
}

// accrueOverdraftAccountTx is AccrueOverdraftTx against an account the caller
// has already loaded. RunEndOfDay lists every account and would otherwise read
// each one a second time.
//
// The accrual is a recomputation rather than an increment. Every run re-derives
// the whole terms window from the account's value-dated balance and posts the
// change in the rounded value — which is the same delta the incremental version
// posted, arrived at differently. The difference shows when a posting lands
// backdated: the day it takes effect on is recomputed with it in place, gross
// moves, and the delta trues up the interest that was charged on the old
// figure. No accrual is ever reversed and no date is ever rewound.
func (r *Register) accrueOverdraftAccountTx(ctx context.Context, tx Tx, acct Account, date time.Time) error {
	if acct.Rate <= 0 || acct.Status == Closed {
		return nil
	}
	// No priced overdraft has been set, so there is no window to accrue over.
	if acct.TermsEffectiveFrom.IsZero() {
		return nil
	}
	if acct.DayCount.Days(acct.LastAccrualDate, date) <= 0 {
		return nil
	}

	series, err := r.valueDatedSeriesTx(ctx, tx, acct.GLAccount, acct.TermsEffectiveFrom, date)
	if err != nil {
		return err
	}
	terms := acct
	gross := interest.AccrueSeries(series, acct.TermsEffectiveFrom, date,
		func(balance ledger.Amount, from, to time.Time) interest.Accrued {
			return dailyOverdraftAccrual(balance, terms, from, to)
		})

	before := acct.Accrued.Minor()
	acct.Accrued += gross - acct.AccruedGross
	acct.AccruedGross = gross
	acct.LastAccrualDate = date
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}

	delta := acct.Accrued.Minor() - before
	if delta == 0 {
		// The rounding did not tick. There is nothing to post, and a
		// zero-amount entry is refused by the ledger anyway.
		return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrued, string(acct.ID), acct)
	}

	income, err := r.interestIncomeTx(ctx, tx, acct)
	if err != nil {
		return err
	}
	if delta > 0 {
		if _, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
			Description: "Overdraft interest accrued: " + acct.Name,
			BookingDate: date,
			ValueDate:   date,
			Entries: []ledger.Entry{
				{AccountID: acct.InterestGL, Amount: delta, Direction: ledger.Debit},
				{AccountID: income, Amount: delta, Direction: ledger.Credit},
			},
		}); err != nil {
			return err
		}
		return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrued, string(acct.ID), acct)
	}
	return r.correctOverdraftAccrualTx(ctx, tx, acct, income, -delta, date)
}

// correctOverdraftAccrualTx gives back interest that a backdated posting has
// shown was never owed. amount is positive.
//
// It is not a reversal. The original accrual was a correct statement of what
// the ledger knew at the time, and reversing it would say otherwise; this is a
// new, linked event that posts what actually changed.
//
// The credit goes to the receivable as far as the receivable can absorb it. If
// interest has already been capitalised out of it, the rest is money the
// customer has actually paid, so it is refunded to their account rather than
// driving an Asset balance negative — which the ledger would refuse, inside an
// end-of-day batch, taking the whole book's run down with it.
func (r *Register) correctOverdraftAccrualTx(ctx context.Context, tx Tx, acct Account, income ledger.AccountID, amount ledger.Amount, date time.Time) error {
	receivable, err := r.bookBalanceTx(ctx, tx, acct.InterestGL)
	if err != nil {
		return err
	}
	absorbed := amount
	if absorbed > receivable {
		absorbed = receivable
	}
	if absorbed < 0 {
		absorbed = 0
	}

	entries := []ledger.Entry{{AccountID: income, Amount: amount, Direction: ledger.Debit}}
	if absorbed > 0 {
		entries = append(entries, ledger.Entry{AccountID: acct.InterestGL, Amount: absorbed, Direction: ledger.Credit})
	}
	if refund := amount - absorbed; refund > 0 {
		entries = append(entries, ledger.Entry{AccountID: acct.GLAccount, Amount: refund, Direction: ledger.Credit})
	}

	if _, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Overdraft interest corrected: " + acct.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries:     entries,
	}); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrualCorrected, string(acct.ID), acct)
}

// dailyOverdraftAccrual is overdraftAccrual applied one day at a time across a
// run of constant balance. It is the interest.Period this product accrues by.
//
// AccrueSeries hands a Period a whole run at once, and overdraftAccrual over an
// N-day span is NOT the sum of its N single days: interest.Accrue divides once
// per call, so one call over N days truncates once where N calls truncate N
// times. That difference is the whole reason this walk exists. Overdraft
// interest has always been accrued a business day at a time, and the receivable
// every account carries was built up that way; recomputing a window with one
// long call would restate the record of every account the first night it ran.
// Walking the days is what makes the recompute reproduce the increment it
// replaces, which is the only thing that makes it a safe swap.
//
// It matters more than truncation under Thirty360, where a day is not always a
// day: the 31st collapses onto the 30th and accrues nothing. What "the interest
// over this window" comes to there depends on how the window is cut, so the
// only answer worth reproducing is the one that was actually charged — day by
// day.
//
// The cost is arithmetic, not I/O: AccrueSeries still reads the window in one
// query, and this loop adds no round trips.
func dailyOverdraftAccrual(balance ledger.Amount, acct Account, from, to time.Time) interest.Accrued {
	var total interest.Accrued
	end := ledger.DayStart(to)
	for d := ledger.DayStart(from); d.Before(end); d = d.AddDate(0, 0, 1) {
		total += overdraftAccrual(balance, acct, d, d.AddDate(0, 0, 1))
	}
	return total
}

// overdraftAccrual is the interest earned on an account's overdrawn balance
// over one accrual period, tiered at the arranged limit.
//
// An account can be drawn beyond its limit despite CheckWithdrawal: a direct GL
// posting does not pass through this layer, and capitalizing interest on a
// fully-drawn overdraft pushes it over by itself.
//
// An unarranged rate is an optional SURCHARGE, so an account without one
// accrues the excess at the arranged rate rather than at zero. Skipping the
// excess entirely — which is what a plain `UnarrangedRate > 0` guard does —
// would make the money drawn beyond the limit interest-FREE, and so literally
// cheaper than the money drawn inside it. That is the exact opposite of what a
// limit is for, and it is the configuration most accounts here are opened with.
//
// It takes from and to explicitly rather than reading the account's
// LastAccrualDate, so that AccrueSeries can call it once per run of constant
// balance within one accrual window — through dailyOverdraftAccrual, which is
// what a Period for this product has to be.
func overdraftAccrual(book ledger.Amount, acct Account, from, to time.Time) interest.Accrued {
	drawn := -book
	if drawn <= 0 {
		return 0
	}
	arranged := drawn
	if arranged > acct.OverdraftLimit {
		arranged = acct.OverdraftLimit
	}
	total := interest.Accrue(arranged, acct.Rate, acct.DayCount, from, to)
	if excess := drawn - arranged; excess > 0 {
		rate := acct.UnarrangedRate
		if rate == 0 {
			rate = acct.Rate
		}
		total += interest.Accrue(excess, rate, acct.DayCount, from, to)
	}
	return total
}

// ChargeOverdraftInterest capitalizes accrued interest into the account,
// clearing the receivable.
//
// This is the monthly event a customer actually sees. Charging the interest to
// the account is also what makes an overdraft compound: the balance the next
// period accrues on now includes this period's interest.
//
//	Dr  customer account (Liability)   62
//	  Cr accrued interest receivable    62
//
// The amount charged is Accrued.Minor() — the receivable's balance — rather
// than the exact accrual, because the ledger holds whole minor units. Charging
// a rounded-up figure leaves the record NEGATIVE by up to half a minor unit:
// 30 days accrue 61.64382 cents and 62 are charged, leaving −0.35618. That is
// correct, not a leak. The residue is bounded by half a minor unit and Minor()
// of it rounds to zero — except at an EXACT half, where Minor() rounds away
// from zero to ±1 even though the receivable itself is already back to zero.
// That is why CloseTx tests the receivable's own ledger balance rather than
// the record: ordinarily the next day's accrual absorbs the residue as the
// balance moves again, but a residue frozen at exactly half a minor unit never
// would. Truncating instead would give away a fraction on every cycle.
//
// Nothing accrued means nothing posted, and a zero-value Transaction is
// returned rather than an error: an end-of-month over a portfolio in credit is
// an ordinary outcome, not a failure.
//
// Unlike RunEndOfDay's per-account accrual, a closed account is refused rather
// than skipped: this is an explicitly-invoked single-account operation (a
// caller asked to charge THIS account), and posting a debit to it would
// reopen a balance on an account CloseTx only let through at zero.
//
// Returns ErrAccountNotFound, ErrAccountClosed.
func (r *Register) ChargeOverdraftInterest(ctx context.Context, id AccountID, date time.Time) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.ChargeOverdraftInterestTx(ctx, tx, id, date)
		return err
	})
	return out, err
}

// ChargeOverdraftInterestTx is ChargeOverdraftInterest within a caller-supplied
// unit of work.
func (r *Register) ChargeOverdraftInterestTx(ctx context.Context, tx Tx, id AccountID, date time.Time) (ledger.Transaction, error) {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if acct.Status == Closed {
		return ledger.Transaction{}, ErrAccountClosed
	}
	charge := acct.Accrued.Minor()
	if charge <= 0 || acct.InterestGL == "" {
		return ledger.Transaction{}, nil
	}

	glTx, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Overdraft interest charged: " + acct.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries: []ledger.Entry{
			{AccountID: acct.GLAccount, Amount: charge, Direction: ledger.Debit},
			{AccountID: acct.InterestGL, Amount: charge, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return ledger.Transaction{}, err
	}

	acct.Accrued -= interest.FromMinor(charge)
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return ledger.Transaction{}, err
	}
	if err := r.appendAuditTx(ctx, tx, ledger.EventOverdraftInterestCharged, string(acct.ID), map[string]any{
		"account_id":     string(acct.ID),
		"amount":         charge,
		"transaction_id": string(glTx.ID),
		"residue":        int64(acct.Accrued),
	}); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, nil
}

// RunEndOfDay accrues overdraft interest on every account in the book for one
// business date.
//
// It does not capitalize. Charging is a monthly event on its own cycle, and
// which day of the month is a product decision this layer has no opinion about;
// a caller runs ChargeOverdraftInterest when its calendar says to.
//
// Accounts with no rate, accounts in credit and closed accounts are skipped
// rather than errored — over a real portfolio most accounts are all three.
func (r *Register) RunEndOfDay(ctx context.Context, date time.Time) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.RunEndOfDayTx(ctx, tx, date)
	})
}

// RunEndOfDayTx is RunEndOfDay within a caller-supplied unit of work, so a
// participant can run its deposit and lending batches in one transaction.
func (r *Register) RunEndOfDayTx(ctx context.Context, tx Tx, date time.Time) error {
	accounts, err := tx.ListDepositAccounts(ctx, r.bookID)
	if err != nil {
		return err
	}
	for _, acct := range accounts {
		if err := r.accrueOverdraftAccountTx(ctx, tx, acct, date); err != nil {
			return err
		}
	}
	return nil
}

// Totals aggregates every customer account in the book into deposits and
// overdrafts, per asset.
//
// See the Totals type for why the Asset-side figure is computed here rather
// than posted anywhere.
func (r *Register) Totals(ctx context.Context) (Totals, error) {
	var out Totals
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.TotalsTx(ctx, tx)
		return err
	})
	return out, err
}

// TotalsTx is Totals within a caller-supplied unit of work.
func (r *Register) TotalsTx(ctx context.Context, tx Tx) (Totals, error) {
	accounts, err := tx.ListDepositAccounts(ctx, r.bookID)
	if err != nil {
		return Totals{}, err
	}
	out := Totals{
		Deposits:   make(map[ledger.AssetCode]ledger.Amount),
		Overdrafts: make(map[ledger.AssetCode]ledger.Amount),
	}
	for _, acct := range accounts {
		balance, err := r.bookBalanceTx(ctx, tx, acct.GLAccount)
		if err != nil {
			return Totals{}, err
		}
		switch {
		case balance > 0:
			out.Deposits[acct.Asset] += balance
		case balance < 0:
			out.Overdrafts[acct.Asset] += -balance
		}
	}
	return out, nil
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
