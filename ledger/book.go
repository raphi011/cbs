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
// log live in a Store (store/sqlite); the Book keeps only the store handle, its
// BookID and its clock. All it contributes is validation and orchestration —
// which is the part that is stated against the Store interface and names no
// table, so that what a bank will accept is decided here and not by whatever is
// underneath.
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
// The derived reads — BookBalanceTx, ValueDateBalanceTx, SeriesTx — come in the
// same two forms, and for the same reason: a layer that has just posted inside a
// unit of work must be able to read the result of that posting without opening a
// second one. Each of them resolves the account's normal direction itself, so
// that the sign convention lives only in AccountType.NormalBalance() and a
// caller never has to name Debit or Credit to read a balance.
//
// Because Update is exclusive, a …Tx method must never be handed a Tx and then
// call a plain method: that would open a second unit of work inside the first,
// which the store refuses outright rather than allowing — see
// sqlite.ErrNestedTransaction for what it would otherwise cost.
//
// # Thread Safety
//
// All public methods on Book are safe for concurrent use; the Store provides
// the isolation.
//
// # Double-Entry Bookkeeping
//
// Every transaction posted through this book enforces the fundamental
// accounting equation: debits must equal credits *within each asset*. A book
// may hold accounts in several assets, and a total taken across all of them
// would be satisfied by legs that merely share an integer — so the sum is
// taken per asset. This guarantee is checked before any entries are applied to
// account balances.
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
//	store, _ := sqlite.Open(ctx, "", time.Now)
//	book := ledger.NewBook(store, "bank", time.Now)
//	l, _ := book.CreateLedger(ctx, "General Ledger")
//	sl, _ := book.CreateSubledger(ctx, l.ID, "Accounts Receivable")
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
// The account is denominated in asset, which must be a known asset (see
// LookupAsset), and starts with a zero balance.
//
// Returns ErrSubledgerNotFound if the parent subledger does not exist, or
// ErrAssetNotFound if the asset code is not one the system knows.
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
//
// It resolves by name on every call rather than caching an ID, which is the
// same choice payment.centralBankChartTx documents: a cached ID is wrong after
// a store reset, wrong for a process that did not create the row, and wrong the
// moment there are two processes. Resolving is a listing of a chart of accounts
// that has tens of rows, not millions.
//
// The name is the identity here, so two subledgers under one ledger may not
// share one. Nothing enforces that for subledgers created directly; Ensure
// simply takes the first match, in listing order.
func (s *Book) EnsureSubledgerTx(ctx context.Context, tx Tx, ledgerID LedgerID, name string) (Subledger, error) {
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
	return s.CreateSubledgerTx(ctx, tx, ledgerID, name)
}

// EnsureAccountTx returns the account with this name, type and asset in this
// subledger, creating it if it is not there.
//
// The match is on all three, not on the name alone. An account and its asset
// are inseparable, so "Interest Income" in euro and in dollars are two
// accounts; and matching a name across types would hand a caller asking for an
// Expense account a Revenue one, whose normal balance runs the other way — a
// mismatch that would surface only as a balance with the wrong sign.
func (s *Book) EnsureAccountTx(ctx context.Context, tx Tx, subledgerID SubledgerID, name string, accountType AccountType, asset AssetCode) (Account, error) {
	if err := ValidateText("name", name); err != nil {
		return Account{}, err
	}
	existing, err := tx.ListAccounts(ctx, s.id)
	if err != nil {
		return Account{}, err
	}
	for _, a := range existing {
		if a.SubledgerID == subledgerID && a.Name == name && a.Type == accountType && a.Asset == asset {
			return a, nil
		}
	}
	return s.CreateAccountTx(ctx, tx, subledgerID, name, accountType, asset)
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
//
// It exists for callers that need to resolve accounts referenced across a
// whole batch of results — rendering the entries of a transaction listing,
// for instance — rather than one at a time. GetAccount's single store.View
// per call is a full BEGIN…COMMIT; calling it once per entry across a listing
// of N transactions costs on the order of N of them, serialized, for what is
// fundamentally N cheap reads. GetAccounts
// opens exactly one store.View and issues one tx.GetAccount per distinct ID
// inside it, so the round-trip count stops depending on how many results are
// being rendered.
//
// Duplicate IDs are resolved once. Returns ErrAccountNotFound (via the same
// error GetAccount returns) at the first ID that does not exist, rather than
// resolving the rest and reporting a partial map — a caller-visible error
// should not depend on where in the batch the bad ID happened to fall.
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
	// for interest calculations and settlement. End-of-day snapshots use
	// BookingDate instead, not this field — see
	// deposit.Register.TakeEndOfDaySnapshotTx. If zero, the BookingDate
	// is used.
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
//  5. Debits must equal credits *within each asset*. A global total is not
//     enough: an Amount is an integer in its asset's minor units, so a global
//     sum is satisfied whenever the integers match. 10_000_000_000 debited
//     from a EUR account (€100M) against 10_000_000_000 credited to a BTC one
//     (100 BTC) nets to zero overall while inventing most of a hundred
//     million euro. See validateBalance.
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
// asset. This is the core invariant of double-entry bookkeeping, restated for
// a ledger that holds more than one asset.
//
// Checking globally would not do. An entry carries an Amount in its asset's
// minor units and nothing else, so a global sum is satisfied whenever the
// integers match, whatever they are worth: 10_000_000_000 debited from a EUR
// account (€100M) against 10_000_000_000 credited to a BTC one (100 BTC) nets
// to zero by the old rule and invents most of a hundred million euro. What
// breaks it is not that the legs differ in value — the check has no rate with
// which to notice that — but that equal integers in assets whose scales differ
// by a factor of a million are not equal amounts. Per asset, there is no rate
// to get wrong, which is why the ledger never has to know what anything is
// worth.
//
// An FX trade therefore cannot be one naive two-asset posting. Each asset
// balances through its own position account, and the bank's open exposure is
// the balance of those accounts.
//
// accounts must hold every account referenced by entries; the caller has
// already loaded them all. A missing key would read as the zero AssetCode
// rather than as an error, quietly bucketing unrelated entries into one asset
// and admitting a transaction that does not balance in any real one. Not
// reachable from the only caller today, which loads every referenced account
// before it calls.
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
		var err error
		out, err = s.BookBalanceTx(ctx, tx, accountID)
		return err
	})
	return out, err
}

// BookBalanceTx is BookBalance within a caller-supplied unit of work.
//
// It reads the account for its type, then asks the store to aggregate against
// that type's normal direction. A caller must not pass the direction in: which
// way an account's balance runs is a property of the account, and a caller that
// supplies it is asserting something it cannot check.
func (s *Book) BookBalanceTx(ctx context.Context, tx Tx, accountID AccountID) (Amount, error) {
	acct, err := tx.GetAccount(ctx, s.id, accountID)
	if err != nil {
		return 0, err
	}
	return tx.BookBalance(ctx, s.id, accountID, acct.Type.NormalBalance())
}

// ValueDateBalance computes an account's balance as of the end of asOf's day.
//
// The book balance answers "what has been recorded"; this answers "what has
// taken economic effect". They differ whenever a posting is value-dated away
// from its booking date, which an outbound payment's clearing leg always is.
//
// Entries value-dated on asOf itself count: a day's interest accrues on that
// day's closing balance.
//
// The interest engines consume SeriesTx rather than this, because they recompute
// a whole window day by day rather than asking about one day.
//
// Returns ErrAccountNotFound if the account does not exist.
func (s *Book) ValueDateBalance(ctx context.Context, accountID AccountID, asOf time.Time) (Amount, error) {
	var out Amount
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.ValueDateBalanceTx(ctx, tx, accountID, asOf)
		return err
	})
	return out, err
}

// ValueDateBalanceTx is ValueDateBalance within a caller-supplied unit of work.
//
// The exclusive upper bound the store wants is derived here, from asOf, so that
// no caller has to remember to snap it.
func (s *Book) ValueDateBalanceTx(ctx context.Context, tx Tx, accountID AccountID, asOf time.Time) (Amount, error) {
	acct, err := tx.GetAccount(ctx, s.id, accountID)
	if err != nil {
		return 0, err
	}
	return tx.ValueDateBalance(ctx, s.id, accountID, acct.Type.NormalBalance(), NextDay(asOf))
}

// SeriesTx is an account's value-dated movement history over [from, to], signed
// by the account's normal direction, within a caller-supplied unit of work.
//
// Where ValueDateBalanceTx answers "what had taken effect by this day", this
// returns each day's own figure, which is what lets an accrual re-derive a past
// day whose posting only reached the ledger afterwards.
//
// The bounds are snapped here: from is inclusive and to is exclusive of the day
// after to, so a window that is to accrue THROUGH to reads the day to falls in.
// Snapping in one place is the point — the store compares raw timestamps and
// truncates nothing itself, so a caller that snapped differently would get a
// silently different answer.
//
// There is no plain form. Every consumer is an interest engine already inside a
// unit of work, and an unused wrapper is surface with no caller to justify it.
func (s *Book) SeriesTx(ctx context.Context, tx Tx, accountID AccountID, from, to time.Time) (Series, error) {
	acct, err := tx.GetAccount(ctx, s.id, accountID)
	if err != nil {
		return Series{}, err
	}
	return tx.ValueDatedSeries(ctx, s.id, accountID, acct.Type.NormalBalance(),
		DayStart(from), NextDay(to))
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
