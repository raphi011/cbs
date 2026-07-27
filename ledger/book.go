package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Book is the central component of the general ledger. It manages the full
// lifecycle of ledgers, subledgers, accounts, transactions, and the audit
// trail.
//
// # Where the state lives
//
// A Book owns no state of its own. Every entity, every counter and the audit
// log live in a Store (store/mem in-process, store/pg on Postgres); the Book
// keeps only the store handle, its BookID and its clock. All it contributes is
// validation and orchestration — which is exactly the part that must not differ
// between the two backends.
//
// # Units of work
//
// Every mutating method comes in two forms. The plain form (PostTransaction)
// wraps a single Store.Update, so the whole operation — the entity writes, the
// counters it burned and the audit event it emitted — commits or rolls back
// together. The exported …Tx form (PostTransactionTx) takes a caller-supplied
// Tx instead, so the deposit and payment layers can compose several operations
// across layers into one atomic unit of work.
//
// Because Update is exclusive, a …Tx method must never be handed a Tx and then
// call a plain method: that would open a second unit of work inside the first
// and, on store/mem, deadlock on the store's write lock.
//
// # Thread Safety
//
// All public methods on Book are safe for concurrent use; the Store provides
// the isolation.
//
// # Double-Entry Bookkeeping
//
// Every transaction posted through this book enforces the fundamental
// accounting equation: total debits must equal total credits. This guarantee is
// checked before any entries are applied to account balances.
//
// # ID Generation
//
// The book uses simple monotonic counters, allocated by the store and scoped to
// the BookID, for ID generation. In a production system, you would replace this
// with UUIDs or another globally unique ID scheme.
type Book struct {
	// store owns all persistent state.
	store Store

	// id is this book's identity. Chart-of-accounts IDs are unique within a
	// book, not globally, so every store call is scoped by it.
	id BookID

	// clock is the time source. Override in tests to control time.
	clock func() time.Time
}

// NewBook creates a general ledger over the given store, identified by id.
//
// The clock is injected rather than read from time.Now so that several Books
// can share a single deterministic time source — for example, the payment
// package runs one ledger per bank plus a central-bank ledger and drives them
// all from one clock so that booking dates, value dates, and audit timestamps
// line up across ledgers.
//
// Example:
//
//	book := ledger.NewBook(mem.New(time.Now), "bank", time.Now)
//	l, _ := book.CreateLedger(ctx, "General Ledger")
//	sl, _ := book.CreateSubledger(ctx, l.ID, "Accounts Receivable")
//	book.CreateAsset(ctx, "EUR", "Euro", 2, ledger.Fiat)
//	acct, _ := book.CreateAccount(ctx, sl.ID, "Customer A", ledger.Asset, "EUR")
func NewBook(store Store, id BookID, clock func() time.Time) *Book {
	return &Book{store: store, id: id, clock: clock}
}

// ID returns this book's identity within the store.
func (s *Book) ID() BookID { return s.id }

// Store returns the underlying store, so a caller that needs to span several
// layers in one unit of work can open the Update itself and then drive the
// …Tx methods of each layer with the resulting Tx.
func (s *Book) Store() Store { return s.store }

// now returns the current time using the book's clock.
func (s *Book) now() time.Time { return s.clock() }

// appendAuditTx records an immutable event through the transaction, so an audit
// event never outlives an operation that rolled back.
//
// payload is marshalled now, not held by reference, so later mutation of the
// entity cannot rewrite history. The event's Seq is assigned by the store.
func (s *Book) appendAuditTx(ctx context.Context, tx Tx, scope Scope, eventType, entityID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit %s: marshal payload: %w", eventType, err)
	}
	id, err := tx.NextID(ctx, s.id, "evt")
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, AuditEvent{
		ID:         id,
		BookID:     s.id,
		Scope:      scope,
		Type:       eventType,
		EntityID:   entityID,
		Payload:    raw,
		OccurredAt: s.now(),
	})
}

// ---------------------------------------------------------------------------
// Ledger & Subledger Management
// ---------------------------------------------------------------------------

// CreateLedger creates a new top-level ledger. A ledger is the highest
// level of organization in the chart of accounts, typically representing
// a book of accounts (e.g., "General Ledger", "Trading Book").
//
// Returns the created ledger.
func (s *Book) CreateLedger(ctx context.Context, name string) (Ledger, error) {
	var out Ledger
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.CreateLedgerTx(ctx, tx, name)
		return err
	})
	return out, err
}

// CreateLedgerTx is CreateLedger within a caller-supplied unit of work.
func (s *Book) CreateLedgerTx(ctx context.Context, tx Tx, name string) (Ledger, error) {
	if err := ValidateText("name", name); err != nil {
		return Ledger{}, err
	}

	id, err := tx.NextID(ctx, s.id, "ldg")
	if err != nil {
		return Ledger{}, err
	}

	l := Ledger{
		ID:        LedgerID(id),
		Name:      name,
		CreatedAt: s.now(),
	}
	if err := tx.PutLedger(ctx, s.id, l); err != nil {
		return Ledger{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ScopeLedger, EventLedgerCreated, string(l.ID), l); err != nil {
		return Ledger{}, err
	}
	return l, nil
}

// GetLedger retrieves a ledger by its ID.
// Returns ErrLedgerNotFound if the ledger does not exist.
func (s *Book) GetLedger(ctx context.Context, id LedgerID) (Ledger, error) {
	var out Ledger
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetLedger(ctx, s.id, id)
		return err
	})
	return out, err
}

// CreateSubledger creates a new subledger under an existing ledger.
// Subledgers provide a second level of grouping for accounts
// (e.g., "Accounts Receivable", "Checking Accounts", "Loan Portfolio").
//
// Returns ErrLedgerNotFound if the parent ledger does not exist.
func (s *Book) CreateSubledger(ctx context.Context, ledgerID LedgerID, name string) (Subledger, error) {
	var out Subledger
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.CreateSubledgerTx(ctx, tx, ledgerID, name)
		return err
	})
	return out, err
}

// CreateSubledgerTx is CreateSubledger within a caller-supplied unit of work.
func (s *Book) CreateSubledgerTx(ctx context.Context, tx Tx, ledgerID LedgerID, name string) (Subledger, error) {
	if err := ValidateText("name", name); err != nil {
		return Subledger{}, err
	}
	if err := ValidateText("ledgerId", string(ledgerID)); err != nil {
		return Subledger{}, err
	}
	if _, err := tx.GetLedger(ctx, s.id, ledgerID); err != nil {
		return Subledger{}, err
	}

	// Subledgers are identified by their chart-of-accounts block (100, 200, …),
	// issued book-wide.
	block, err := tx.NextSubledgerBlock(ctx, s.id)
	if err != nil {
		return Subledger{}, err
	}

	sl := Subledger{
		ID:        SubledgerID(strconv.Itoa(block)),
		LedgerID:  ledgerID,
		Name:      name,
		CreatedAt: s.now(),
	}
	if err := tx.PutSubledger(ctx, s.id, sl); err != nil {
		return Subledger{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ScopeLedger, EventSubledgerCreated, string(sl.ID), sl); err != nil {
		return Subledger{}, err
	}
	return sl, nil
}

// GetSubledger retrieves a subledger by its ID.
// Returns ErrSubledgerNotFound if the subledger does not exist.
func (s *Book) GetSubledger(ctx context.Context, id SubledgerID) (Subledger, error) {
	var out Subledger
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetSubledger(ctx, s.id, id)
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Account Management
// ---------------------------------------------------------------------------

// CreateAccount creates a new financial account within a subledger.
//
// In the chart of accounts, every account has a type that determines its
// normal balance direction:
//   - Asset and Expense accounts have a normal debit balance (debits increase them)
//   - Liability, Equity, and Revenue accounts have a normal credit balance (credits increase them)
//
// The account is denominated in asset, which must already be registered in this
// book, and starts with a zero balance.
//
// Returns ErrSubledgerNotFound if the parent subledger does not exist, or
// ErrAssetNotFound if the asset is not registered in this book.
func (s *Book) CreateAccount(ctx context.Context, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error) {
	var out Account
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.CreateAccountTx(ctx, tx, subledgerID, name, accountType, asset)
		return err
	})
	return out, err
}

// CreateAccountTx is CreateAccount within a caller-supplied unit of work.
func (s *Book) CreateAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error) {
	if err := ValidateText("name", name); err != nil {
		return Account{}, err
	}
	if err := ValidateText("subledgerId", string(subledgerID)); err != nil {
		return Account{}, err
	}
	if err := ValidateText("asset", string(asset)); err != nil {
		return Account{}, err
	}
	if _, err := tx.GetSubledger(ctx, s.id, subledgerID); err != nil {
		return Account{}, err
	}
	// The asset must already be registered in this book. There is no default:
	// silently falling back to a base currency is precisely the bug this
	// dimension exists to prevent.
	if _, err := tx.GetAsset(ctx, s.id, asset); err != nil {
		return Account{}, err
	}

	// Account numbers reset per (type, subledger) — the type-first
	// chart-of-accounts convention: <typeBlock>.<subledgerID>.<NNN>.
	block := accountType.codeBlock()
	seq, err := tx.NextAccountSeq(ctx, s.id, block, subledgerID)
	if err != nil {
		return Account{}, err
	}

	acct := Account{
		ID:          AccountID(fmt.Sprintf("%d.%s.%03d", block, subledgerID, seq)),
		SubledgerID: subledgerID,
		Name:        name,
		Type:        accountType,
		Asset:       asset,
		CreatedAt:   s.now(),
	}
	if err := tx.PutAccount(ctx, s.id, acct); err != nil {
		return Account{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ScopeLedger, EventAccountCreated, string(acct.ID), acct); err != nil {
		return Account{}, err
	}
	return acct, nil
}

// GetAccount retrieves an account by its ID.
// Returns ErrAccountNotFound if the account does not exist.
func (s *Book) GetAccount(ctx context.Context, id AccountID) (Account, error) {
	var out Account
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetAccount(ctx, s.id, id)
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Asset Registry
// ---------------------------------------------------------------------------

// CreateAsset registers an asset in this book.
//
// Assets are book-scoped rather than global because each participant owns its
// own book: a bank that does not deal in BTC should not carry BTC in its chart
// of accounts.
//
// Returns ErrDuplicateAsset if the code is already registered, and
// ErrInvalidScale if scale exceeds MaxAssetScale.
func (s *Book) CreateAsset(ctx context.Context, code AssetCode, name string, scale uint8, class AssetClass) (AssetDef, error) {
	var out AssetDef
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.CreateAssetTx(ctx, tx, code, name, scale, class)
		return err
	})
	return out, err
}

// CreateAssetTx is CreateAsset within a caller-supplied unit of work.
func (s *Book) CreateAssetTx(ctx context.Context, tx Tx, code AssetCode, name string, scale uint8, class AssetClass) (AssetDef, error) {
	if err := ValidateText("code", string(code)); err != nil {
		return AssetDef{}, err
	}
	if err := ValidateText("name", name); err != nil {
		return AssetDef{}, err
	}
	if scale > MaxAssetScale {
		return AssetDef{}, ErrInvalidScale
	}

	switch _, err := tx.GetAsset(ctx, s.id, code); {
	case err == nil:
		return AssetDef{}, ErrDuplicateAsset
	case !errors.Is(err, ErrAssetNotFound):
		return AssetDef{}, err
	}

	a := AssetDef{Code: code, Name: name, Scale: scale, Class: class}
	if err := tx.PutAsset(ctx, s.id, a); err != nil {
		return AssetDef{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ScopeLedger, EventAssetCreated, string(a.Code), a); err != nil {
		return AssetDef{}, err
	}
	return a, nil
}

// GetAsset retrieves an asset by its code.
// Returns ErrAssetNotFound if the asset is not registered in this book.
func (s *Book) GetAsset(ctx context.Context, code AssetCode) (AssetDef, error) {
	var out AssetDef
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetAsset(ctx, s.id, code)
		return err
	})
	return out, err
}

// ListAssets returns every asset registered in this book.
func (s *Book) ListAssets(ctx context.Context) ([]AssetDef, error) {
	var out []AssetDef
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListAssets(ctx, s.id)
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Transaction Posting
// ---------------------------------------------------------------------------

// PostTransactionRequest contains all the parameters needed to post a
// new multi-legged transaction.
type PostTransactionRequest struct {
	// IdempotencyKey is an optional client-supplied key that prevents
	// duplicate postings. If a transaction with the same key has already
	// been posted, ErrDuplicateIdempotencyKey is returned. Idempotency
	// keys are useful when clients might retry requests — the system
	// guarantees that a given key produces at most one transaction.
	IdempotencyKey string

	// Entries is the set of debit and credit legs that make up this
	// transaction. The total debit amounts must equal the total credit
	// amounts.
	//
	// Each entry specifies:
	//   - AccountID: which account to debit or credit
	//   - Amount: the positive amount in minor currency units
	//   - Direction: Debit or Credit
	Entries []Entry

	// BookingDate is the date/time when the transaction is recorded in
	// the system. If zero, the current time is used. This is the date
	// that appears in system reports and audit trails.
	BookingDate time.Time

	// ValueDate is the date when the transaction takes economic effect.
	// This determines which business day the transaction "belongs to"
	// for interest calculations, settlement, and end-of-day snapshots.
	// If zero, the BookingDate is used.
	ValueDate time.Time

	// Description is a human-readable description of the transaction.
	Description string

	// Metadata is optional key-value pairs for storing additional
	// context (e.g., reference numbers, originating system IDs).
	Metadata map[string]string
}

// PostTransaction records a new multi-legged accounting transaction.
//
// The transaction goes through the following validation steps:
//  1. At least one entry is required.
//  2. All entry amounts must be positive (direction determines sign).
//  3. All referenced accounts must exist.
//  4. If an idempotency key is provided, it must not already be used.
//  5. Total debits must equal total credits.
//  6. Asset and Expense accounts must have sufficient book balance.
//
// If all validations pass, the entries are atomically applied to the
// account balances and the transaction is recorded.
//
// # Balance Impact
//
// The effect of an entry on an account's book balance depends on the
// account's type and the entry direction:
//
//   - A debit to an Asset/Expense account increases its balance.
//   - A credit to an Asset/Expense account decreases its balance.
//   - A credit to a Liability/Equity/Revenue account increases its balance.
//   - A debit to a Liability/Equity/Revenue account decreases its balance.
//
// Internally, balances are stored as signed values where positive means
// a balance in the account's normal direction.
func (s *Book) PostTransaction(ctx context.Context, req PostTransactionRequest) (Transaction, error) {
	var out Transaction
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.PostTransactionTx(ctx, tx, req)
		return err
	})
	return out, err
}

// PostTransactionTx is PostTransaction within a caller-supplied unit of work.
// It is the composition point for the layers above: a deposit capture or a
// payment leg posts through this method with the same Tx it uses for its own
// state, so the GL posting and the layer's own bookkeeping commit together.
func (s *Book) PostTransactionTx(ctx context.Context, tx Tx, req PostTransactionRequest) (Transaction, error) {
	// Validate: non-empty entries.
	if len(req.Entries) == 0 {
		return Transaction{}, ErrEmptyTransaction
	}

	// Validate: amounts.
	for _, e := range req.Entries {
		if e.Amount <= 0 {
			return Transaction{}, ErrInvalidAmount
		}
	}

	// Validate: text. Every one of these is stored, and the account ids are
	// used as lookup keys below — see ValidateText for why that is a domain
	// rule rather than something either store enforces for itself.
	if err := ValidateText("idempotencyKey", req.IdempotencyKey); err != nil {
		return Transaction{}, err
	}
	if err := ValidateText("description", req.Description); err != nil {
		return Transaction{}, err
	}
	if err := ValidateTextMap("metadata", req.Metadata); err != nil {
		return Transaction{}, err
	}
	for _, e := range req.Entries {
		if err := ValidateText("accountId", string(e.AccountID)); err != nil {
			return Transaction{}, err
		}
	}

	// Validate: all referenced accounts exist. The accounts are kept because
	// the sufficient-balance check below needs each one's type, and the
	// distinct IDs because LockAccounts needs them.
	accounts := make(map[AccountID]Account, len(req.Entries))
	ids := make([]AccountID, 0, len(req.Entries))
	for _, e := range req.Entries {
		if _, seen := accounts[e.AccountID]; seen {
			continue
		}
		acct, err := tx.GetAccount(ctx, s.id, e.AccountID)
		if err != nil {
			return Transaction{}, err
		}
		accounts[e.AccountID] = acct
		ids = append(ids, e.AccountID)
	}

	// Validate: idempotency key.
	if req.IdempotencyKey != "" {
		_, err := tx.GetTransactionByIdempotencyKey(ctx, s.id, req.IdempotencyKey)
		switch {
		case err == nil:
			return Transaction{}, ErrDuplicateIdempotencyKey
		case !errors.Is(err, ErrTransactionNotFound):
			return Transaction{}, err
		}
	}

	// Validate: balanced.
	if err := validateBalance(req.Entries); err != nil {
		return Transaction{}, err
	}

	// Take the write locks before reading balances, so the check and the
	// posting that depends on it are one serialized step: without this, two
	// concurrent transactions could each see enough funds and both post.
	if err := tx.LockAccounts(ctx, s.id, ids); err != nil {
		return Transaction{}, err
	}

	// Validate: sufficient balance for Asset and Expense accounts.
	if err := s.checkSufficientBalance(ctx, tx, accounts, req.Entries); err != nil {
		return Transaction{}, err
	}

	// Set defaults for dates.
	now := s.now()
	bookingDate := req.BookingDate
	if bookingDate.IsZero() {
		bookingDate = now
	}
	valueDate := req.ValueDate
	if valueDate.IsZero() {
		valueDate = bookingDate
	}

	// Assign IDs to entries.
	entries := make([]Entry, len(req.Entries))
	for i, e := range req.Entries {
		id, err := tx.NextID(ctx, s.id, "ent")
		if err != nil {
			return Transaction{}, err
		}
		e.ID = EntryID(id)
		entries[i] = e
	}

	txID, err := tx.NextID(ctx, s.id, "tx")
	if err != nil {
		return Transaction{}, err
	}

	posted := Transaction{
		ID:             TransactionID(txID),
		IdempotencyKey: req.IdempotencyKey,
		Entries:        entries,
		BookingDate:    bookingDate,
		ValueDate:      valueDate,
		Status:         Posted,
		Description:    req.Description,
		Metadata:       req.Metadata,
		CreatedAt:      now,
	}

	if err := tx.PutTransaction(ctx, s.id, posted); err != nil {
		return Transaction{}, err
	}
	if err := s.appendAuditTx(ctx, tx, ScopeLedger, EventTransactionPosted, string(posted.ID), posted); err != nil {
		return Transaction{}, err
	}
	return posted, nil
}

// validateBalance checks that total debits equal total credits.
// This is the core invariant of double-entry bookkeeping.
func validateBalance(entries []Entry) error {
	var debits, credits Amount

	for _, e := range entries {
		if e.Direction == Debit {
			debits += e.Amount
		} else {
			credits += e.Amount
		}
	}

	if debits != credits {
		return ErrUnbalancedTransaction
	}

	return nil
}

// checkSufficientBalance verifies that the entries would not cause any
// Asset or Expense account's book balance to go below zero.
// Liability, Equity, and Revenue accounts are not checked.
//
// accounts must hold every account referenced by entries; the caller has
// already loaded and locked them.
func (s *Book) checkSufficientBalance(ctx context.Context, tx Tx, accounts map[AccountID]Account, entries []Entry) error {
	// Compute the net balance impact per account.
	impact := make(map[AccountID]Amount)
	for _, e := range entries {
		acct := accounts[e.AccountID]
		if e.Direction == acct.Type.NormalBalance() {
			impact[e.AccountID] += e.Amount
		} else {
			impact[e.AccountID] -= e.Amount
		}
	}

	for accountID, delta := range impact {
		acct := accounts[accountID]
		if acct.Type != Asset && acct.Type != Expense {
			continue
		}
		// Only check when the transaction decreases the balance.
		if delta >= 0 {
			continue
		}
		available, err := tx.BookBalance(ctx, s.id, accountID, acct.Type.NormalBalance())
		if err != nil {
			return err
		}
		if available+delta < 0 {
			return ErrInsufficientBalance
		}
	}
	return nil
}

// GetTransaction retrieves a transaction by its ID.
// Returns ErrTransactionNotFound if the transaction does not exist.
func (s *Book) GetTransaction(ctx context.Context, id TransactionID) (Transaction, error) {
	var out Transaction
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetTransaction(ctx, s.id, id)
		return err
	})
	return out, err
}

// GetTransactionByIdempotencyKey retrieves a transaction by its idempotency key.
// Returns ErrTransactionNotFound if no transaction with that key exists.
func (s *Book) GetTransactionByIdempotencyKey(ctx context.Context, key string) (Transaction, error) {
	var out Transaction
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetTransactionByIdempotencyKey(ctx, s.id, key)
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Transaction Reversal
// ---------------------------------------------------------------------------

// ReverseTransaction creates a new counter-transaction that exactly offsets
// the original transaction. Every debit entry becomes a credit and every
// credit entry becomes a debit, with the same amounts and currencies.
//
// The original transaction is marked as Reversed and cannot be reversed
// again. The reversal transaction references the original via its
// ReversalOf field.
//
// # When to Use Reversal
//
// In banking, transactions are never deleted — the audit trail must be
// preserved. Instead, a correction is made by posting a reversal that
// cancels out the effect of the original. This maintains the integrity
// of the ledger while allowing errors to be corrected.
//
// Returns:
//   - ErrTransactionNotFound if the original does not exist.
//   - ErrTransactionAlreadyReversed if the original was already reversed.
func (s *Book) ReverseTransaction(ctx context.Context, txID TransactionID, description string) (Transaction, error) {
	var out Transaction
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ReverseTransactionTx(ctx, tx, txID, description)
		return err
	})
	return out, err
}

// ReverseTransactionTx is ReverseTransaction within a caller-supplied unit of
// work.
func (s *Book) ReverseTransactionTx(ctx context.Context, tx Tx, txID TransactionID, description string) (Transaction, error) {
	if err := ValidateText("description", description); err != nil {
		return Transaction{}, err
	}
	if err := ValidateText("transactionId", string(txID)); err != nil {
		return Transaction{}, err
	}

	original, err := tx.GetTransaction(ctx, s.id, txID)
	if err != nil {
		return Transaction{}, err
	}
	if original.Status == Reversed {
		return Transaction{}, ErrTransactionAlreadyReversed
	}

	// Build reversal entries: flip every direction.
	now := s.now()
	entries := make([]Entry, len(original.Entries))
	for i, e := range original.Entries {
		id, err := tx.NextID(ctx, s.id, "ent")
		if err != nil {
			return Transaction{}, err
		}
		entries[i] = Entry{
			ID:        EntryID(id),
			AccountID: e.AccountID,
			Amount:    e.Amount,
			Direction: e.Direction.Opposite(),
		}
	}

	reversalID, err := tx.NextID(ctx, s.id, "tx")
	if err != nil {
		return Transaction{}, err
	}

	reversal := Transaction{
		ID:          TransactionID(reversalID),
		Entries:     entries,
		BookingDate: now,
		ValueDate:   original.ValueDate,
		Status:      Posted,
		Description: description,
		ReversalOf:  original.ID,
		CreatedAt:   now,
	}

	// Conditional in the store: it flips Posted -> Reversed or fails, so two
	// concurrent reversals cannot both succeed.
	if err := tx.MarkReversed(ctx, s.id, original.ID); err != nil {
		return Transaction{}, err
	}
	if err := tx.PutTransaction(ctx, s.id, reversal); err != nil {
		return Transaction{}, err
	}

	if err := s.appendAuditTx(ctx, tx, ScopeLedger, EventTransactionReversed, string(original.ID), map[string]string{
		"original_id": string(original.ID),
		"reversal_id": string(reversal.ID),
	}); err != nil {
		return Transaction{}, err
	}

	return reversal, nil
}

// ---------------------------------------------------------------------------
// Balance Queries
// ---------------------------------------------------------------------------

// BookBalance computes the current book balance of an account.
//
// The book balance is the net effect of all posted transactions on this
// account, aggregated by the store from its entries:
//
//   - For Asset/Expense accounts, debits increase and credits decrease.
//   - For Liability/Equity/Revenue accounts, credits increase and debits decrease.
//
// Note: ALL transactions are included, including those marked as Reversed.
// The Reversed status is informational — the corresponding reversal
// transaction's entries are what actually cancel out the original's balance
// impact. This preserves the full audit trail, and every Store implementation
// must aggregate the same way.
//
// Returns ErrAccountNotFound if the account does not exist.
func (s *Book) BookBalance(ctx context.Context, accountID AccountID) (Amount, error) {
	var out Amount
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		acct, err := tx.GetAccount(ctx, s.id, accountID)
		if err != nil {
			return err
		}
		out, err = tx.BookBalance(ctx, s.id, accountID, acct.Type.NormalBalance())
		return err
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Audit Trail
// ---------------------------------------------------------------------------

// GetAuditLog returns this book's ledger-scope audit events, ordered by Seq.
//
// The audit log is an append-only, immutable record of every mutation
// that has occurred in the system. It provides:
//
//   - Compliance: Full traceability of who did what and when.
//   - Debugging: Ability to replay the exact sequence of operations.
//   - Reconciliation: Independent verification of account balances
//     by replaying events.
//
// The deposit and payment layers write into the same log under their own
// Scope; this method deliberately narrows to ScopeLedger so a Book reports
// only the mutations it made.
func (s *Book) GetAuditLog(ctx context.Context) ([]AuditEvent, error) {
	return s.listAudit(ctx, AuditFilter{BookID: s.id, Scope: ScopeLedger})
}

// GetAuditLogForEntity returns this book's ledger-scope audit events related to
// a specific entity, identified by its ID.
func (s *Book) GetAuditLogForEntity(ctx context.Context, entityID string) ([]AuditEvent, error) {
	return s.listAudit(ctx, AuditFilter{BookID: s.id, Scope: ScopeLedger, EntityID: entityID})
}

func (s *Book) listAudit(ctx context.Context, f AuditFilter) ([]AuditEvent, error) {
	var out []AuditEvent
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListAudit(ctx, f)
		return err
	})
	return out, err
}
