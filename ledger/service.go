package ledger

import (
	"fmt"
	"sync"
	"time"
)

// Book is the central component of the in-memory general ledger.
// It manages the full lifecycle of ledgers, subledgers, accounts,
// transactions, and the audit trail.
//
// # Thread Safety
//
// All public methods on Book are safe for concurrent use.
// Internally, a read-write mutex protects all state mutations.
//
// # Double-Entry Bookkeeping
//
// Every transaction posted through this book enforces the fundamental
// accounting equation: total debits must equal total credits. This guarantee is checked before any entries
// are applied to account balances.
//
// # ID Generation
//
// The book uses a simple monotonic counter for ID generation. In a
// production system, you would replace this with UUIDs or another
// globally unique ID scheme.
type Book struct {
	mu sync.RWMutex

	// Entity storage, keyed by ID.
	ledgers      map[LedgerID]*Ledger
	subledgers   map[SubledgerID]*Subledger
	accounts     map[AccountID]*Account
	transactions map[TransactionID]*Transaction

	// idempotencyIndex maps idempotency keys to transaction IDs.
	// This allows the system to detect and reject duplicate postings.
	idempotencyIndex map[string]TransactionID

	// auditLog is an append-only log of all mutations. Once appended,
	// entries are never modified or removed.
	auditLog []*AuditEvent

	// idCounter is a simple monotonic counter for generating unique IDs.
	idCounter int64

	// clock is the time source. Override in tests to control time.
	clock func() time.Time
}

// NewBook creates a new general ledger with empty state.
//
// Example:
//
//	book := ledger.NewBook()
//	l, _ := book.CreateLedger("General Ledger")
//	sl, _ := book.CreateSubledger(l.ID, "Accounts Receivable")
//	acct, _ := book.CreateAccount(sl.ID, "Customer A", ledger.Asset)
func NewBook() *Book {
	return NewBookWithClock(time.Now)
}

// NewBookWithClock creates a new general ledger that reads the current
// time from the provided clock function instead of time.Now.
//
// This is useful when several Books must share a single, deterministic
// time source — for example, the payment package runs one ledger per bank
// plus a central-bank ledger and drives them all from one clock so that
// booking dates, value dates, and audit timestamps line up across ledgers.
func NewBookWithClock(clock func() time.Time) *Book {
	return &Book{
		ledgers:          make(map[LedgerID]*Ledger),
		subledgers:       make(map[SubledgerID]*Subledger),
		accounts:         make(map[AccountID]*Account),
		transactions:     make(map[TransactionID]*Transaction),
		idempotencyIndex: make(map[string]TransactionID),
		clock:            clock,
	}
}

// nextID generates a unique ID with the given prefix.
func (s *Book) nextID(prefix string) string {
	s.idCounter++
	return fmt.Sprintf("%s_%d", prefix, s.idCounter)
}

// now returns the current time using the book's clock.
func (s *Book) now() time.Time {
	return s.clock()
}

// appendAudit records an event in the immutable audit log.
func (s *Book) appendAudit(eventType AuditEventType, entityID string, payload any) {
	s.auditLog = append(s.auditLog, &AuditEvent{
		ID:        s.nextID("evt"),
		Timestamp: s.now(),
		Type:      eventType,
		EntityID:  entityID,
		Payload:   payload,
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
func (s *Book) CreateLedger(name string) (Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l := &Ledger{
		ID:        LedgerID(s.nextID("ldg")),
		Name:      name,
		CreatedAt: s.now(),
	}
	s.ledgers[l.ID] = l
	s.appendAudit(EventLedgerCreated, string(l.ID), l)
	return *l, nil
}

// GetLedger retrieves a ledger by its ID.
// Returns ErrLedgerNotFound if the ledger does not exist.
func (s *Book) GetLedger(id LedgerID) (Ledger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	l, ok := s.ledgers[id]
	if !ok {
		return Ledger{}, ErrLedgerNotFound
	}
	return *l, nil
}

// CreateSubledger creates a new subledger under an existing ledger.
// Subledgers provide a second level of grouping for accounts
// (e.g., "Accounts Receivable", "Checking Accounts", "Loan Portfolio").
//
// Returns ErrLedgerNotFound if the parent ledger does not exist.
func (s *Book) CreateSubledger(ledgerID LedgerID, name string) (Subledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.ledgers[ledgerID]; !ok {
		return Subledger{}, ErrLedgerNotFound
	}

	sl := &Subledger{
		ID:        SubledgerID(s.nextID("sub")),
		LedgerID:  ledgerID,
		Name:      name,
		CreatedAt: s.now(),
	}
	s.subledgers[sl.ID] = sl
	s.appendAudit(EventSubledgerCreated, string(sl.ID), sl)
	return *sl, nil
}

// GetSubledger retrieves a subledger by its ID.
// Returns ErrSubledgerNotFound if the subledger does not exist.
func (s *Book) GetSubledger(id SubledgerID) (Subledger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sl, ok := s.subledgers[id]
	if !ok {
		return Subledger{}, ErrSubledgerNotFound
	}
	return *sl, nil
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
// The account starts with a zero balance.
//
// Returns ErrSubledgerNotFound if the parent subledger does not exist.
func (s *Book) CreateAccount(subledgerID SubledgerID, name string, accountType AccountType) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.subledgers[subledgerID]; !ok {
		return Account{}, ErrSubledgerNotFound
	}

	acct := &Account{
		ID:          AccountID(s.nextID("acct")),
		SubledgerID: subledgerID,
		Name:        name,
		Type:        accountType,
		CreatedAt:   s.now(),
	}
	s.accounts[acct.ID] = acct
	s.appendAudit(EventAccountCreated, string(acct.ID), acct)
	return *acct, nil
}

// GetAccount retrieves an account by its ID.
// Returns ErrAccountNotFound if the account does not exist.
func (s *Book) GetAccount(id AccountID) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	acct, ok := s.accounts[id]
	if !ok {
		return Account{}, ErrAccountNotFound
	}
	return *acct, nil
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
func (s *Book) PostTransaction(req PostTransactionRequest) (Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	// Validate: all referenced accounts exist.
	for _, e := range req.Entries {
		if _, ok := s.accounts[e.AccountID]; !ok {
			return Transaction{}, ErrAccountNotFound
		}
	}

	// Validate: idempotency key.
	if req.IdempotencyKey != "" {
		if _, ok := s.idempotencyIndex[req.IdempotencyKey]; ok {
			return Transaction{}, ErrDuplicateIdempotencyKey
		}
	}

	// Validate: balanced.
	if err := validateBalance(req.Entries); err != nil {
		return Transaction{}, err
	}

	// Validate: sufficient balance for Asset and Expense accounts.
	if err := s.checkSufficientBalance(req.Entries); err != nil {
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
		e.ID = EntryID(s.nextID("ent"))
		entries[i] = e
	}

	tx := &Transaction{
		ID:             TransactionID(s.nextID("tx")),
		IdempotencyKey: req.IdempotencyKey,
		Entries:        entries,
		BookingDate:    bookingDate,
		ValueDate:      valueDate,
		Status:         Posted,
		Description:    req.Description,
		Metadata:       req.Metadata,
		CreatedAt:      now,
	}

	s.transactions[tx.ID] = tx
	if req.IdempotencyKey != "" {
		s.idempotencyIndex[req.IdempotencyKey] = tx.ID
	}

	s.appendAudit(EventTransactionPosted, string(tx.ID), tx)
	return copyTransaction(tx), nil
}

// copyTransaction returns a deep copy of a Transaction, including its
// Entries slice and Metadata map.
func copyTransaction(tx *Transaction) Transaction {
	cp := *tx
	cp.Entries = make([]Entry, len(tx.Entries))
	copy(cp.Entries, tx.Entries)
	if tx.Metadata != nil {
		cp.Metadata = make(map[string]string, len(tx.Metadata))
		for k, v := range tx.Metadata {
			cp.Metadata[k] = v
		}
	}
	return cp
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
func (s *Book) checkSufficientBalance(entries []Entry) error {
	// Compute the net balance impact per account.
	impact := make(map[AccountID]Amount)
	for _, e := range entries {
		acct := s.accounts[e.AccountID]
		if e.Direction == acct.Type.NormalBalance() {
			impact[e.AccountID] += e.Amount
		} else {
			impact[e.AccountID] -= e.Amount
		}
	}

	for accountID, delta := range impact {
		acct := s.accounts[accountID]
		if acct.Type != Asset && acct.Type != Expense {
			continue
		}
		// Only check when the transaction decreases the balance.
		if delta >= 0 {
			continue
		}
		available := s.computeBookBalance(accountID, acct.Type)
		if available+delta < 0 {
			return ErrInsufficientBalance
		}
	}
	return nil
}

// GetTransaction retrieves a transaction by its ID.
// Returns ErrTransactionNotFound if the transaction does not exist.
func (s *Book) GetTransaction(id TransactionID) (Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tx, ok := s.transactions[id]
	if !ok {
		return Transaction{}, ErrTransactionNotFound
	}
	return copyTransaction(tx), nil
}

// GetTransactionByIdempotencyKey retrieves a transaction by its idempotency key.
// Returns ErrTransactionNotFound if no transaction with that key exists.
func (s *Book) GetTransactionByIdempotencyKey(key string) (Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	txID, ok := s.idempotencyIndex[key]
	if !ok {
		return Transaction{}, ErrTransactionNotFound
	}
	return copyTransaction(s.transactions[txID]), nil
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
func (s *Book) ReverseTransaction(txID TransactionID, description string) (Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	original, ok := s.transactions[txID]
	if !ok {
		return Transaction{}, ErrTransactionNotFound
	}
	if original.Status == Reversed {
		return Transaction{}, ErrTransactionAlreadyReversed
	}

	// Build reversal entries: flip every direction.
	now := s.now()
	entries := make([]Entry, len(original.Entries))
	for i, e := range original.Entries {
		entries[i] = Entry{
			ID:        EntryID(s.nextID("ent")),
			AccountID: e.AccountID,
			Amount:    e.Amount,
			Direction: e.Direction.Opposite(),
		}
	}

	reversal := &Transaction{
		ID:          TransactionID(s.nextID("tx")),
		Entries:     entries,
		BookingDate: now,
		ValueDate:   original.ValueDate,
		Status:      Posted,
		Description: description,
		ReversalOf:  original.ID,
		CreatedAt:   now,
	}

	original.Status = Reversed
	s.transactions[reversal.ID] = reversal

	s.appendAudit(EventTransactionReversed, string(original.ID), map[string]string{
		"original_id": string(original.ID),
		"reversal_id": string(reversal.ID),
	})

	return copyTransaction(reversal), nil
}

// ---------------------------------------------------------------------------
// Balance Queries
// ---------------------------------------------------------------------------

// BookBalance computes the current book balance of an account.
//
// The book balance is the net effect of all posted transactions on this
// account, calculated by replaying all entries:
//
//   - For Asset/Expense accounts, debits increase and credits decrease.
//   - For Liability/Equity/Revenue accounts, credits increase and debits decrease.
//
// Returns ErrAccountNotFound if the account does not exist.
func (s *Book) BookBalance(accountID AccountID) (Amount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	acct, ok := s.accounts[accountID]
	if !ok {
		return 0, ErrAccountNotFound
	}

	return s.computeBookBalance(accountID, acct.Type), nil
}

// computeBookBalance calculates the net book balance for an account by
// replaying all posted transaction entries.
//
// The sign convention:
//   - Entries in the account's normal direction add to the balance.
//   - Entries opposite to the normal direction subtract from the balance.
//
// For example, for an Asset account (normal = Debit):
//   - Debit entry of 100 -> balance += 100
//   - Credit entry of 30  -> balance -= 30
//   - Net: 70
//
// Note: ALL transactions are included, including those marked as Reversed.
// The Reversed status is informational — the corresponding reversal
// transaction's entries are what actually cancel out the original's
// balance impact. This preserves the full audit trail.
func (s *Book) computeBookBalance(accountID AccountID, accountType AccountType) Amount {
	var balance Amount
	normal := accountType.NormalBalance()

	for _, tx := range s.transactions {
		for _, e := range tx.Entries {
			if e.AccountID != accountID {
				continue
			}
			if e.Direction == normal {
				balance += e.Amount
			} else {
				balance -= e.Amount
			}
		}
	}

	return balance
}

// ---------------------------------------------------------------------------
// Audit Trail
// ---------------------------------------------------------------------------

// GetAuditLog returns all audit events, ordered chronologically.
//
// The audit log is an append-only, immutable record of every mutation
// that has occurred in the system. It provides:
//
//   - Compliance: Full traceability of who did what and when.
//   - Debugging: Ability to replay the exact sequence of operations.
//   - Reconciliation: Independent verification of account balances
//     by replaying events.
//
// In a production system, the audit log would typically be stored in a
// separate, write-once data store with strict access controls.
func (s *Book) GetAuditLog() []AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return copies to prevent external mutation.
	result := make([]AuditEvent, len(s.auditLog))
	for i, e := range s.auditLog {
		result[i] = *e
	}
	return result
}

// GetAuditLogForEntity returns all audit events related to a specific
// entity, identified by its ID.
func (s *Book) GetAuditLogForEntity(entityID string) []AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []AuditEvent
	for _, e := range s.auditLog {
		if e.EntityID == entityID {
			result = append(result, *e)
		}
	}
	return result
}
