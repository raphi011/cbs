package ledger

import (
	"fmt"
	"sync"
	"time"
)

// Service is the central component of the in-memory banking system.
// It manages the full lifecycle of ledgers, subledgers, accounts,
// transactions, holds, balance snapshots, and the audit trail.
//
// # Thread Safety
//
// All public methods on Service are safe for concurrent use.
// Internally, a read-write mutex protects all state mutations.
//
// # Double-Entry Bookkeeping
//
// Every transaction posted through this service enforces the fundamental
// accounting equation: total debits must equal total credits. This guarantee is checked before any entries
// are applied to account balances.
//
// # ID Generation
//
// The service uses a simple monotonic counter for ID generation. In a
// production system, you would replace this with UUIDs or another
// globally unique ID scheme.
type Service struct {
	mu sync.RWMutex

	// Entity storage, keyed by ID.
	ledgers      map[LedgerID]*Ledger
	subledgers   map[SubledgerID]*Subledger
	accounts     map[AccountID]*Account
	transactions map[TransactionID]*Transaction
	holds        map[HoldID]*Hold

	// idempotencyIndex maps idempotency keys to transaction IDs.
	// This allows the system to detect and reject duplicate postings.
	idempotencyIndex map[string]TransactionID

	// accountHolds maps account IDs to their active hold IDs.
	// This index enables efficient lookup of holds per account.
	accountHolds map[AccountID][]HoldID

	// snapshots stores end-of-day balance snapshots.
	// Structure: accountID -> dateKey -> snapshot.
	snapshots map[AccountID]map[string]*BalanceSnapshot

	// auditLog is an append-only log of all mutations. Once appended,
	// entries are never modified or removed.
	auditLog []*AuditEvent

	// idCounter is a simple monotonic counter for generating unique IDs.
	idCounter int64

	// clock is the time source. Override in tests to control time.
	clock func() time.Time
}

// NewService creates a new banking service with empty state.
//
// Example:
//
//	svc := ledger.NewService()
//	l, _ := svc.CreateLedger("General Ledger")
//	sl, _ := svc.CreateSubledger(l.ID, "Accounts Receivable")
//	acct, _ := svc.CreateAccount(sl.ID, "Customer A", ledger.Asset)
func NewService() *Service {
	return &Service{
		ledgers:          make(map[LedgerID]*Ledger),
		subledgers:       make(map[SubledgerID]*Subledger),
		accounts:         make(map[AccountID]*Account),
		transactions:     make(map[TransactionID]*Transaction),
		holds:            make(map[HoldID]*Hold),
		idempotencyIndex: make(map[string]TransactionID),
		accountHolds:     make(map[AccountID][]HoldID),
		snapshots:        make(map[AccountID]map[string]*BalanceSnapshot),
		clock:            time.Now,
	}
}

// nextID generates a unique ID with the given prefix.
func (s *Service) nextID(prefix string) string {
	s.idCounter++
	return fmt.Sprintf("%s_%d", prefix, s.idCounter)
}

// now returns the current time using the service's clock.
func (s *Service) now() time.Time {
	return s.clock()
}

// appendAudit records an event in the immutable audit log.
func (s *Service) appendAudit(eventType AuditEventType, entityID string, payload any) {
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
func (s *Service) CreateLedger(name string) (Ledger, error) {
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
func (s *Service) GetLedger(id LedgerID) (Ledger, error) {
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
func (s *Service) CreateSubledger(ledgerID LedgerID, name string) (Subledger, error) {
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
func (s *Service) GetSubledger(id SubledgerID) (Subledger, error) {
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
func (s *Service) CreateAccount(subledgerID SubledgerID, name string, accountType AccountType) (Account, error) {
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
func (s *Service) GetAccount(id AccountID) (Account, error) {
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
func (s *Service) PostTransaction(req PostTransactionRequest) (Transaction, error) {
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

// GetTransaction retrieves a transaction by its ID.
// Returns ErrTransactionNotFound if the transaction does not exist.
func (s *Service) GetTransaction(id TransactionID) (Transaction, error) {
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
func (s *Service) GetTransactionByIdempotencyKey(key string) (Transaction, error) {
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
func (s *Service) ReverseTransaction(txID TransactionID, description string) (Transaction, error) {
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
// Holds (Pending Authorizations)
// ---------------------------------------------------------------------------

// CreateHoldRequest contains all the parameters needed to create a hold.
type CreateHoldRequest struct {
	// AccountID is the account whose available balance will be reduced.
	AccountID AccountID

	// Amount is the positive hold amount in minor currency units.
	Amount Amount

	// ExpiresAt is when the hold automatically becomes void. Expired
	// holds no longer affect the available balance. If zero, the hold
	// does not expire automatically.
	ExpiresAt time.Time

	// Description is a human-readable description of the hold.
	Description string
}

// CreateHold places an authorization hold on an account.
//
// A hold reduces the account's available balance without affecting the
// book balance. This is commonly used in payment processing:
//
//  1. A card authorization creates a hold for the transaction amount.
//  2. The available balance is reduced immediately.
//  3. Later, the hold is either captured (converted to a posted transaction)
//     or released (cancelled, restoring the available balance).
//
// Returns:
//   - ErrAccountNotFound if the account does not exist.
//   - ErrInvalidAmount if the amount is not positive.
func (s *Service) CreateHold(req CreateHoldRequest) (Hold, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.accounts[req.AccountID]; !ok {
		return Hold{}, ErrAccountNotFound
	}
	if req.Amount <= 0 {
		return Hold{}, ErrInvalidAmount
	}

	now := s.now()
	h := &Hold{
		ID:          HoldID(s.nextID("hld")),
		AccountID:   req.AccountID,
		Amount:      req.Amount,
		ExpiresAt:   req.ExpiresAt,
		Description: req.Description,
		Status:      HoldActive,
		CreatedAt:   now,
	}

	s.holds[h.ID] = h
	s.accountHolds[req.AccountID] = append(s.accountHolds[req.AccountID], h.ID)
	s.appendAudit(EventHoldCreated, string(h.ID), h)
	return *h, nil
}

// ReleaseHold cancels an active hold, restoring the available balance.
//
// This is used when an authorization is voided — for example, when a
// customer cancels a pending payment or when a hold expires and is
// manually cleaned up.
//
// Returns:
//   - ErrHoldNotFound if the hold does not exist.
//   - ErrHoldNotActive if the hold has already been released or captured.
func (s *Service) ReleaseHold(holdID HoldID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.holds[holdID]
	if !ok {
		return ErrHoldNotFound
	}
	if h.Status != HoldActive {
		return ErrHoldNotActive
	}

	h.Status = HoldReleased
	s.appendAudit(EventHoldReleased, string(h.ID), h)
	return nil
}

// CaptureHold converts an active hold into a posted transaction.
//
// This is the final step in the authorization-capture flow:
//
//  1. Authorization: CreateHold reduces the available balance.
//  2. Capture: CaptureHold converts the hold into a real transaction,
//     moving the amount from available to the book balance.
//
// The captureAmount may differ from the original hold amount (partial
// capture). The hold is marked as Captured regardless of the amount.
//
// Parameters:
//   - holdID: the hold to capture.
//   - counterpartyAccountID: the other side of the double-entry.
//   - captureAmount: the final amount to post (may be <= hold amount).
//   - description: description for the resulting transaction.
//
// Returns the posted transaction and any error. The hold is marked as
// Captured. If captureAmount is 0, the hold amount is used.
//
// Returns:
//   - ErrHoldNotFound if the hold does not exist.
//   - ErrHoldNotActive if the hold has already been released or captured.
//   - ErrAccountNotFound if the counterparty account does not exist.
func (s *Service) CaptureHold(holdID HoldID, counterpartyAccountID AccountID, captureAmount Amount, description string) (Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.holds[holdID]
	if !ok {
		return Transaction{}, ErrHoldNotFound
	}
	if h.Status != HoldActive {
		return Transaction{}, ErrHoldNotActive
	}

	if _, ok := s.accounts[counterpartyAccountID]; !ok {
		return Transaction{}, ErrAccountNotFound
	}

	if captureAmount <= 0 {
		captureAmount = h.Amount
	}

	// Determine debit/credit based on the account type of the hold account.
	// For an Asset account (e.g., checking), a capture means money leaves,
	// so we credit the held account and debit the counterparty.
	acct := s.accounts[h.AccountID]
	holdDirection := acct.Type.NormalBalance().Opposite()
	counterDirection := holdDirection.Opposite()

	now := s.now()
	entries := []Entry{
		{
			ID:        EntryID(s.nextID("ent")),
			AccountID: h.AccountID,
			Amount:    captureAmount,
			Direction: holdDirection,
		},
		{
			ID:        EntryID(s.nextID("ent")),
			AccountID: counterpartyAccountID,
			Amount:    captureAmount,
			Direction: counterDirection,
		},
	}

	tx := &Transaction{
		ID:          TransactionID(s.nextID("tx")),
		Entries:     entries,
		BookingDate: now,
		ValueDate:   now,
		Status:      Posted,
		Description: description,
		CreatedAt:   now,
	}

	h.Status = HoldCaptured
	s.transactions[tx.ID] = tx

	s.appendAudit(EventHoldCaptured, string(h.ID), map[string]string{
		"hold_id":        string(h.ID),
		"transaction_id": string(tx.ID),
	})
	s.appendAudit(EventTransactionPosted, string(tx.ID), tx)

	return copyTransaction(tx), nil
}

// GetHold retrieves a hold by its ID.
// Returns ErrHoldNotFound if the hold does not exist.
func (s *Service) GetHold(id HoldID) (Hold, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	h, ok := s.holds[id]
	if !ok {
		return Hold{}, ErrHoldNotFound
	}
	return *h, nil
}

// ---------------------------------------------------------------------------
// Balance Queries
// ---------------------------------------------------------------------------

// GetBalance computes the current balance of an account.
//
// The balance has three components:
//
//   - Book Balance: The net effect of all posted transactions on this
//     account. Calculated by replaying all entries.
//     For Asset/Expense accounts, debits increase and credits decrease.
//     For Liability/Equity/Revenue accounts, credits increase and debits decrease.
//
//   - Holds: The sum of all active (non-expired) holds on this account.
//
//   - Available Balance: Book Balance minus Holds. This represents
//     the amount that can actually be used for new transactions.
//
// Returns ErrAccountNotFound if the account does not exist.
func (s *Service) GetBalance(accountID AccountID) (Balance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	acct, ok := s.accounts[accountID]
	if !ok {
		return Balance{}, ErrAccountNotFound
	}

	book := s.computeBookBalance(accountID, acct.Type)
	holds := s.computeActiveHolds(accountID)

	return Balance{
		Book:      book,
		Holds:     holds,
		Available: book - holds,
	}, nil
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
func (s *Service) computeBookBalance(accountID AccountID, accountType AccountType) Amount {
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

// computeActiveHolds sums all active, non-expired holds for an account.
func (s *Service) computeActiveHolds(accountID AccountID) Amount {
	var total Amount
	now := s.now()

	for _, holdID := range s.accountHolds[accountID] {
		h := s.holds[holdID]
		if h.Status != HoldActive {
			continue
		}
		if !h.ExpiresAt.IsZero() && h.ExpiresAt.Before(now) {
			continue
		}
		total += h.Amount
	}

	return total
}

// ---------------------------------------------------------------------------
// End-of-Day Balance Snapshots
// ---------------------------------------------------------------------------

// TakeEndOfDaySnapshot computes and stores the balance snapshot for an
// account on a given business date.
//
// End-of-day snapshots serve several purposes in banking:
//
//   - Interest Calculation: Interest is typically accrued on the end-of-day
//     balance. Snapshots provide the authoritative balance for this.
//
//   - Statement Generation: Monthly/quarterly statements are built from
//     end-of-day snapshots rather than replaying all transactions.
//
//   - Regulatory Reporting: Many regulatory reports require end-of-day
//     position data.
//
//   - Performance: Instead of replaying all entries from the beginning of
//     time, balance queries for historical dates can use snapshots.
//
// If a snapshot already exists for the same account/date, it is
// overwritten (useful for end-of-day recalculation after late postings).
//
// Returns ErrAccountNotFound if the account does not exist.
func (s *Service) TakeEndOfDaySnapshot(accountID AccountID, date time.Time) (BalanceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	acct, ok := s.accounts[accountID]
	if !ok {
		return BalanceSnapshot{}, ErrAccountNotFound
	}

	book := s.computeBookBalance(accountID, acct.Type)
	holds := s.computeActiveHolds(accountID)

	snap := &BalanceSnapshot{
		AccountID: accountID,
		Date:      date,
		Balance: Balance{
			Book:      book,
			Holds:     holds,
			Available: book - holds,
		},
		TakenAt: s.now(),
	}

	// Store in nested map structure.
	if s.snapshots[accountID] == nil {
		s.snapshots[accountID] = make(map[string]*BalanceSnapshot)
	}
	dateKey := date.Format("2006-01-02")
	s.snapshots[accountID][dateKey] = snap

	s.appendAudit(EventSnapshotTaken, string(accountID), snap)
	return *snap, nil
}

// GetSnapshot retrieves an end-of-day balance snapshot for an account
// and business date.
//
// Returns ErrSnapshotNotFound if no snapshot exists for the given parameters.
// Returns ErrAccountNotFound if the account does not exist.
func (s *Service) GetSnapshot(accountID AccountID, date time.Time) (BalanceSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.accounts[accountID]; !ok {
		return BalanceSnapshot{}, ErrAccountNotFound
	}

	dateKey := date.Format("2006-01-02")
	if byAccount, ok := s.snapshots[accountID]; ok {
		if snap, ok := byAccount[dateKey]; ok {
			return *snap, nil
		}
	}

	return BalanceSnapshot{}, ErrSnapshotNotFound
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
func (s *Service) GetAuditLog() []AuditEvent {
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
func (s *Service) GetAuditLogForEntity(entityID string) []AuditEvent {
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
