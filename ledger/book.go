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
func (s *Book) appendAuditTx(ctx context.Context, tx CommonTx, scope Scope, eventType, entityID string, payload any) error {
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

// CreateLedger creates a new top-level ledger. A ledger is the highest level of
// organization in the chart of accounts, typically representing a book of
// accounts (e.g., "General Ledger", "Trading Book").
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

// CreateSubledger creates a new subledger under an existing ledger. Subledgers
// provide a second level of grouping for accounts (e.g., "Accounts Receivable",
// "Checking Accounts", "Loan Portfolio").
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
func (s *Book) CreateAccount(ctx context.Context, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error) {
	var out Account
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.CreateAccountTx(ctx, tx, subledgerID, name, accountType, asset)
		return err
	})
	return out, err
}

// CreateAccountTx is CreateAccount within a caller-supplied unit of work. The
// account is a plain one and refuses an entry that names a subsidiary; see
// CreateControlAccountTx for the other kind.
func (s *Book) CreateAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error) {
	return s.createAccountTx(ctx, tx, subledgerID, name, accountType, asset, false)
}

// CreateControlAccountTx creates an account that pools subsidiaries: one chart-
// accounts line standing for many, with each entry against it naming which.
func (s *Book) CreateControlAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error) {
	return s.createAccountTx(ctx, tx, subledgerID, name, accountType, asset, true)
}

func (s *Book) createAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode, control bool) (Account, error) {
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
	// The asset must be one the system knows. There is no default: silently
	// falling back to a base currency is precisely the bug this dimension
	// exists to prevent.
	if _, err := LookupAsset(asset); err != nil {
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
		Control:     control,
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

// EnsureSubledgerTx returns the subledger with this name under this ledger,
// creating it if it is not there.
func (s *Book) EnsureSubledgerTx(ctx context.Context, tx Tx, ledgerID LedgerID, name string) (Subledger, error) {
	found, err := s.findSubledgerTx(ctx, tx, ledgerID, name)
	if err == nil {
		return found, nil
	}
	if !errors.Is(err, ErrSubledgerNotFound) {
		return Subledger{}, err
	}
	return s.CreateSubledgerTx(ctx, tx, ledgerID, name)
}

// findSubledgerTx resolves the subledger with this name under this ledger, or
// ErrSubledgerNotFound. It is the half of EnsureSubledgerTx that does not write.
func (s *Book) findSubledgerTx(ctx context.Context, tx Tx, ledgerID LedgerID, name string) (Subledger, error) {
	if err := ValidateText("name", name); err != nil {
		return Subledger{}, err
	}
	existing, err := tx.ListSubledgers(ctx, s.id)
	if err != nil {
		return Subledger{}, err
	}
	for _, sl := range existing {
		if sl.LedgerID == ledgerID && sl.Name == name {
			return sl, nil
		}
	}
	return Subledger{}, fmt.Errorf("%w: %s under ledger %s", ErrSubledgerNotFound, name, ledgerID)
}

// EnsureAccountTx returns the plain account with this name, type and asset in
// this subledger, creating it if it is not there.
func (s *Book) EnsureAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error) {
	return s.ensureAccountTx(ctx, tx, subledgerID, name, accountType, asset, false)
}

// EnsureControlAccountTx is EnsureAccountTx for an account that pools subsidiaries.
// See CreateControlAccountTx for why the two are separate methods.
func (s *Book) EnsureControlAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error) {
	return s.ensureAccountTx(ctx, tx, subledgerID, name, accountType, asset, true)
}

func (s *Book) ensureAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode, control bool) (Account, error) {
	found, err := s.findAccountTx(ctx, tx, subledgerID, name, accountType, asset, control)
	if err == nil {
		return found, nil
	}
	if !errors.Is(err, ErrAccountNotFound) {
		return Account{}, err
	}
	return s.createAccountTx(ctx, tx, subledgerID, name, accountType, asset, control)
}

func (s *Book) findAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode, control bool) (Account, error) {
	if err := ValidateText("name", name); err != nil {
		return Account{}, err
	}
	existing, err := tx.ListAccounts(ctx, s.id)
	if err != nil {
		return Account{}, err
	}
	for _, a := range existing {
		if a.SubledgerID == subledgerID && a.Name == name && a.Type == accountType && a.Asset == asset && a.Control == control {
			return a, nil
		}
	}
	return Account{}, fmt.Errorf("%w: %s in subledger %s", ErrAccountNotFound, name, subledgerID)
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

// GetAccounts retrieves several accounts in one unit of work.
func (s *Book) GetAccounts(ctx context.Context, ids []AccountID) (map[AccountID]Account, error) {
	out := make(map[AccountID]Account, len(ids))
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		for _, id := range ids {
			if _, ok := out[id]; ok {
				continue
			}
			acct, err := tx.GetAccount(ctx, s.id, id)
			if err != nil {
				return err
			}
			out[id] = acct
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Transaction Posting
// ---------------------------------------------------------------------------

// PostTransactionRequest contains all the parameters needed to post a
// new multi-legged transaction.
type PostTransactionRequest struct {
	// IdempotencyKey is an optional client-supplied key that prevents duplicate
	// postings. If a transaction with the same key has already been posted,
	// ErrDuplicateIdempotencyKey is returned.
	IdempotencyKey string

	// Entries is the set of debit and credit legs that make up this transaction.
	// The total debit amounts must equal the total credit amounts.
	Entries []Entry

	// BookingDate is the date/time when the transaction is recorded in
	// the system. If zero, the current time is used. This is the date
	// that appears in system reports and audit trails.
	BookingDate time.Time

	// ValueDate is the date when the transaction takes economic effect. This
	// determines which business day the transaction "belongs to" for interest
	// calculations and settlement.
	ValueDate time.Time

	// Description is a human-readable description of the transaction.
	Description string

	// Metadata is optional key-value pairs for storing additional
	// context (e.g., reference numbers, originating system IDs).
	Metadata map[string]string
}

// PostTransaction records a new multi-legged accounting transaction.
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
	// rule rather than something a store enforces for itself.
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
		if err := ValidateText("subsidiary", e.Subsidiary); err != nil {
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

	// Validate: the dimension matches the account, both ways round.
	for _, e := range req.Entries {
		switch {
		case accounts[e.AccountID].Control && e.Subsidiary == "":
			return Transaction{}, fmt.Errorf("%w: %s", ErrSubsidiaryRequired, e.AccountID)
		case !accounts[e.AccountID].Control && e.Subsidiary != "":
			return Transaction{}, fmt.Errorf("%w: %s", ErrSubsidiaryNotAllowed, e.AccountID)
		}
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

	// Validate: balanced within every asset. accounts was already loaded
	// above for the sufficient-balance check, so this costs no extra reads.
	if err := validateBalance(req.Entries, accounts); err != nil {
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

	// Assign IDs to entries, and resolve each leg's value date.
	entries := make([]Entry, len(req.Entries))
	for i, e := range req.Entries {
		id, err := tx.NextID(ctx, s.id, "ent")
		if err != nil {
			return Transaction{}, err
		}
		e.ID = EntryID(id)
		if e.ValueDate.IsZero() {
			e.ValueDate = valueDate
		}
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

// validateBalance checks that total debits equal total credits within each
// asset. This is the core invariant of double-entry bookkeeping, restated for a
// ledger that holds more than one asset.
func validateBalance(entries []Entry, accounts map[AccountID]Account) error {
	// net[asset] is debits minus credits in that asset.
	net := make(map[AssetCode]Amount, 2)
	// order preserves first-appearance order, so the error names the same
	// asset every time rather than whichever one the map iterated to first.
	order := make([]AssetCode, 0, 2)

	for _, e := range entries {
		asset := accounts[e.AccountID].Asset
		if _, seen := net[asset]; !seen {
			order = append(order, asset)
		}
		if e.Direction == Debit {
			net[asset] += e.Amount
		} else {
			net[asset] -= e.Amount
		}
	}

	for _, asset := range order {
		if net[asset] != 0 {
			return fmt.Errorf("%w: %w: %s", ErrUnbalancedTransaction, ErrUnbalancedAsset, asset)
		}
	}
	return nil
}

// checkSufficientBalance verifies that the entries would not cause any Asset or
// Expense POSITION's book balance to go below zero. Liability, Equity, and
// Revenue accounts are not checked.
func (s *Book) checkSufficientBalance(ctx context.Context, tx Tx, accounts map[AccountID]Account, entries []Entry) error {
	// Compute the net balance impact per position.
	impact := make(map[Position]Amount)
	for _, e := range entries {
		acct := accounts[e.AccountID]
		pos := e.AccountID.For(e.Subsidiary)
		if e.Direction == acct.Type.NormalBalance() {
			impact[pos] += e.Amount
		} else {
			impact[pos] -= e.Amount
		}
	}

	for pos, delta := range impact {
		acct := accounts[pos.Account]
		if acct.Type != Asset && acct.Type != Expense {
			continue
		}
		// Only check when the transaction decreases the balance.
		if delta >= 0 {
			continue
		}
		available, err := BookBalance(ctx, tx, s.id, pos, acct.Type.NormalBalance())
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

// ReverseTransaction creates a new counter-transaction that exactly offsets the
// original transaction. Every debit entry becomes a credit and every credit
// entry becomes a debit, with the same amounts and currencies.
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
			// The original leg's subsidiary, not the pool: a reversal that
			// credited the control account unqualified would leave the pool
			// square and one customer permanently short.
			Subsidiary: e.Subsidiary,
			Amount:     e.Amount,
			Direction:  e.Direction.Opposite(),
			// Mirrors the original leg's value date, not the reversal
			// transaction's: a value-dated balance nets a reversal against the
			// original only if the two legs land on the same day.
			ValueDate: e.ValueDate,
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

// signed is one entry's effect on a balance kept in normal's direction, and the
// one place that rule is written: an entry in the normal direction raises the
// balance and the opposite one lowers it.
func signed(e Entry, normal Direction) Amount {
	if e.Direction == normal {
		return e.Amount
	}
	return -e.Amount
}

// BookBalance sums a position's entries, signed by normal. An account nothing
// was posted to is zero, and an empty subsidiary is the whole account — a
// balance is a fold over entries, never a join to a chart of accounts.
func BookBalance(ctx context.Context, tx Tx, book BookID, pos Position, normal Direction) (Amount, error) {
	var balance Amount
	for e, err := range tx.ScanEntries(ctx, book, pos, EntryFilter{}) {
		if err != nil {
			return 0, err
		}
		balance += signed(e, normal)
	}
	return balance, nil
}

// BookBalance computes the current book balance of a position.
func (s *Book) BookBalance(ctx context.Context, pos Position) (Amount, error) {
	var out Amount
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.BookBalanceTx(ctx, tx, pos)
		return err
	})
	return out, err
}

// BookBalanceTx is BookBalance within a caller-supplied unit of work.
func (s *Book) BookBalanceTx(ctx context.Context, tx Tx, pos Position) (Amount, error) {
	acct, err := tx.GetAccount(ctx, s.id, pos.Account)
	if err != nil {
		return 0, err
	}
	return BookBalance(ctx, tx, s.id, pos, acct.Type.NormalBalance())
}

// ValueDateBalance computes an account's balance as of the end of asOf's day.
func (s *Book) ValueDateBalance(ctx context.Context, pos Position, asOf time.Time) (Amount, error) {
	var out Amount
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ValueDateBalanceTx(ctx, tx, pos, asOf)
		return err
	})
	return out, err
}

// ValueDateBalanceTx is ValueDateBalance within a caller-supplied unit of work.
func (s *Book) ValueDateBalanceTx(ctx context.Context, tx Tx, pos Position, asOf time.Time) (Amount, error) {
	acct, err := tx.GetAccount(ctx, s.id, pos.Account)
	if err != nil {
		return 0, err
	}
	return tx.ValueDateBalance(ctx, s.id, pos, acct.Type.NormalBalance(), NextDay(asOf))
}

// SeriesTx is an account's value-dated movement history over [from, to], signed
// by the account's normal direction, within a caller-supplied unit of work.
func (s *Book) SeriesTx(ctx context.Context, tx Tx, pos Position, from, to time.Time) (Series, error) {
	acct, err := tx.GetAccount(ctx, s.id, pos.Account)
	if err != nil {
		return Series{}, err
	}
	return tx.ValueDatedSeries(ctx, s.id, pos, acct.Type.NormalBalance(),
		DayStart(from), NextDay(to))
}

// ---------------------------------------------------------------------------
// Audit Trail
// ---------------------------------------------------------------------------

// GetAuditLog returns this book's ledger-scope audit events, ordered by Seq.
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
