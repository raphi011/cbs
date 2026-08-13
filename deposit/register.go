package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// Register is the demand-deposit layer over a general ledger: customer deposit
// accounts, their status lifecycle, authorization holds, the available balance
// those holds reduce, and end-of-day snapshots.
type Register struct {
	store Store

	// gl is the general ledger this register composes with. Only its …Tx
	// methods are used from inside a unit of work.
	gl *ledger.Book

	// bookID scopes every store call, and stamps every audit event this layer
	// writes.
	bookID ledger.BookID

	clock func() time.Time

	// issuer is what this register mints addresses under.
	issuer iban.Issuer

	// customers is the subledger this register files its control lines in,
	// CONSTRUCTOR state for the reason issuer is.
	customers ledger.SubledgerID
}

// NewRegister creates a deposit register over the given store, layered on the
// given general ledger.
func NewRegister(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time, issuer iban.Issuer, customers ledger.SubledgerID) *Register {
	return &Register{store: store, gl: book, bookID: id, clock: clock, issuer: issuer, customers: customers}
}

// Issuer returns what this register mints addresses under.
func (r *Register) Issuer() iban.Issuer { return r.issuer }

// Store returns the underlying store, so a caller holding a register can open a
// unit of work and drive the …Tx methods with it directly.
func (r *Register) Store() Store { return r.store }

// BookID returns the book this register is scoped to.
func (r *Register) BookID() ledger.BookID { return r.bookID }

func (r *Register) now() time.Time { return r.clock() }

// appendAuditTx records an immutable deposit-scope event through the
// transaction, so an audit event never outlives an operation that rolled back.
// payload is marshalled now, not held by reference, so later mutation of the
// entity cannot rewrite history; the event carries the register's BookID, so a
// deposit event is attributable to the bank that made it rather than landing in
// the log unscoped.
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

// OpenAccount opens a new customer deposit account in the Active state.
func (r *Register) OpenAccount(ctx context.Context, name string, asset ledger.AssetCode, productID product.ID, overdraftLimit ledger.Amount, identifiers ...Identifier) (Account, error) {
	var out Account
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.OpenAccountTx(ctx, tx, name, asset, productID, overdraftLimit, identifiers...)
		return err
	})
	return out, err
}

// OpenAccountTx is OpenAccount within a caller-supplied unit of work.
func (r *Register) OpenAccountTx(ctx context.Context, tx Tx, name string, asset ledger.AssetCode, productID product.ID, overdraftLimit ledger.Amount, identifiers ...Identifier) (Account, error) {
	if err := ledger.ValidateText("name", name); err != nil {
		return Account{}, err
	}
	if err := r.checkOpenableProductTx(ctx, tx, productID); err != nil {
		return Account{}, err
	}

	for i, ident := range identifiers {
		if err := ident.Validate("identifier"); err != nil {
			return Account{}, err
		}
		// This bank issues its own customers' IBANs; see ErrIBANIsIssued. The
		// refusal is here as well as at AddIdentifier because opening is the other
		// door, and the one the API's request body reaches.
		if ident.Scheme == IdentifierIBAN {
			return Account{}, ErrIBANIsIssued
		}
		// Siblings in THIS call, not only accounts already in the store:
		// checkIdentifierFreeTx reads the register, which does not yet hold the
		// account being opened, so `identifiers: [X, X]` — which the API accepts
		// verbatim from a request body — would sail past it.
		if slices.ContainsFunc(identifiers[:i], ident.Matches) {
			return Account{}, ErrIdentifierTaken
		}
		if err := r.checkIdentifierFreeTx(ctx, tx, "", ident); err != nil {
			return Account{}, err
		}
	}

	// Minted BEFORE the chart lines, so a register with no bank code refuses
	// having created nothing. Both are in this Tx and roll back together.
	address, err := r.mintAddressTx(ctx, tx)
	if err != nil {
		return Account{}, err
	}

	// This is also where an unknown asset is refused: every ensure below falls
	// through to a create when the line is not there, and a create validates the
	// code.
	if err := r.ensureChartTx(ctx, tx, asset); err != nil {
		return Account{}, err
	}

	id, err := tx.NextID(ctx, r.bookID, "dep")
	if err != nil {
		return Account{}, err
	}

	acct := Account{
		ID:        AccountID(id),
		Name:      name,
		Asset:     asset,
		Status:    Active,
		CreatedAt: r.now(),
		// The minted address FIRST, the caller's other schemes after it. Nothing
		// depends on the order — addressFor picks by scheme — but an account's own
		// IBAN leading the list is what a statement and a console both show.
		Identifiers: append([]Identifier{address}, identifiers...),
	}
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return Account{}, err
	}

	// Every account gets a terms row from birth, carrying the limit it was opened
	// with and NO overlay.
	opening := OverdraftTerms{
		AccountID:      acct.ID,
		EffectiveFrom:  ledger.DayStart(acct.CreatedAt),
		ProductID:      productID,
		OverdraftLimit: overdraftLimit,
		CreatedAt:      acct.CreatedAt,
	}
	if err := opening.Validate(); err != nil {
		return Account{}, err
	}
	if err := tx.PutOverdraftTerms(ctx, r.bookID, opening); err != nil {
		return Account{}, err
	}

	if err := r.appendAuditTx(ctx, tx, ledger.EventAccountOpened, string(acct.ID), acct); err != nil {
		return Account{}, err
	}
	return acct, nil
}

// checkOpenableProductTx is the product validation OpenAccount and
// ChangeProduct share: it must exist, be on sale, be the right kind, and have a
// price today.
func (r *Register) checkOpenableProductTx(ctx context.Context, tx Tx, id product.ID) error {
	p, err := tx.GetProduct(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	if p.Retired {
		return fmt.Errorf("%w: %s", product.ErrProductRetired, id)
	}
	if p.Kind != product.CurrentAccount {
		return fmt.Errorf("%w: %s is a %s", product.ErrKindMismatch, id, p.Kind)
	}
	v, err := tx.GetProductVersionAsOf(ctx, r.bookID, id, ledger.DayStart(r.now()))
	if err != nil {
		return err
	}
	return v.VerifyHash()
}

// receivableSubledgerName is where accrued-interest receivables are filed. Not
// receivableSubledgerName is where accrued-interest receivables are filed.
const receivableSubledgerName = "Accrued Interest"

// incomeSubledgerName is where interest income is filed.
const incomeSubledgerName = "Income"

// The three slots this layer posts to: where a customer's money pools, where
// what they owe in interest sits, and where the bank earns it.
var (
	principalSlot  = ledger.Slot{Key: "deposit.principal", Type: ledger.Liability, Control: true}
	receivableSlot = ledger.Slot{Key: "deposit.interest_receivable", Type: ledger.Asset, Control: true}
	incomeSlot     = ledger.Slot{Key: "deposit.interest_income", Type: ledger.Revenue, ByProduct: true}
)

// The names the first account in an asset opens those three lines under.
func customerDepositsName(asset ledger.AssetCode) string {
	return "Customer Deposits (" + string(asset) + ")"
}

func accruedInterestName(asset ledger.AssetCode) string {
	return "Accrued Interest (" + string(asset) + ")"
}

func interestIncomeName(asset ledger.AssetCode) string {
	return "Interest Income (" + string(asset) + ")"
}

// SetOverdraftLimit changes what this customer may go overdrawn by, from a day.
func (r *Register) SetOverdraftLimit(ctx context.Context, id AccountID, limit ledger.Amount, effectiveFrom time.Time) (OverdraftTerms, error) {
	var out OverdraftTerms
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.SetOverdraftLimitTx(ctx, tx, id, limit, effectiveFrom)
		return err
	})
	return out, err
}

// SetOverdraftLimitTx is SetOverdraftLimit within a caller-supplied unit of work.
func (r *Register) SetOverdraftLimitTx(ctx context.Context, tx Tx, id AccountID, limit ledger.Amount, effectiveFrom time.Time) (OverdraftTerms, error) {
	return r.appendTermsTx(ctx, tx, id, effectiveFrom, ledger.EventOverdraftLimitSet,
		func(row *OverdraftTerms) { row.OverdraftLimit = limit })
}

// SetOverdraftPricingOverlay gives this customer a negotiated price instead of
// the product's, from a day — or clears one, putting the account back on the
// product.
func (r *Register) SetOverdraftPricingOverlay(ctx context.Context, id AccountID, pricing *product.OverdraftPricing, effectiveFrom time.Time) (OverdraftTerms, error) {
	var out OverdraftTerms
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.SetOverdraftPricingOverlayTx(ctx, tx, id, pricing, effectiveFrom)
		return err
	})
	return out, err
}

// SetOverdraftPricingOverlayTx is SetOverdraftPricingOverlay within a
// caller-supplied unit of work.
func (r *Register) SetOverdraftPricingOverlayTx(ctx context.Context, tx Tx, id AccountID, pricing *product.OverdraftPricing, effectiveFrom time.Time) (OverdraftTerms, error) {
	return r.appendTermsTx(ctx, tx, id, effectiveFrom, ledger.EventOverdraftPricingOverlaid,
		func(row *OverdraftTerms) {
			if pricing == nil {
				row.Pricing = nil
				return
			}
			// Copied, so a caller keeping its argument cannot rewrite a stored
			// row's price afterwards.
			p := *pricing
			row.Pricing = &p
		})
}

// ChangeProduct migrates an account onto another product, from a day.
func (r *Register) ChangeProduct(ctx context.Context, id AccountID, productID product.ID, effectiveFrom time.Time) (OverdraftTerms, error) {
	var out OverdraftTerms
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.ChangeProductTx(ctx, tx, id, productID, effectiveFrom)
		return err
	})
	return out, err
}

// ChangeProductTx is ChangeProduct within a caller-supplied unit of work.
func (r *Register) ChangeProductTx(ctx context.Context, tx Tx, id AccountID, productID product.ID, effectiveFrom time.Time) (OverdraftTerms, error) {
	if err := r.checkOpenableProductTx(ctx, tx, productID); err != nil {
		return OverdraftTerms{}, err
	}
	return r.appendTermsTx(ctx, tx, id, effectiveFrom, ledger.EventAccountProductChanged,
		func(row *OverdraftTerms) { row.ProductID = productID })
}

// appendTermsTx is the one write the three setters share: read the row in force
// on the effective day, change the one thing this caller changes, and append
// the result as a new row.
func (r *Register) appendTermsTx(ctx context.Context, tx Tx, id AccountID, effectiveFrom time.Time, event string, change func(*OverdraftTerms)) (OverdraftTerms, error) {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return OverdraftTerms{}, err
	}
	if acct.Status == Closed {
		return OverdraftTerms{}, ErrAccountClosed
	}

	now := r.now()
	if effectiveFrom.IsZero() {
		effectiveFrom = now
	}
	day := ledger.DayStart(effectiveFrom)
	row, err := tx.GetOverdraftTermsAsOf(ctx, r.bookID, id, day)
	if err != nil {
		return OverdraftTerms{}, err
	}
	row.EffectiveFrom = day
	row.CreatedAt = now
	change(&row)

	if err := row.Validate(); err != nil {
		return OverdraftTerms{}, err
	}
	if err := tx.PutOverdraftTerms(ctx, r.bookID, row); err != nil {
		return OverdraftTerms{}, err
	}
	if err := r.appendAuditTx(ctx, tx, event, string(id), row); err != nil {
		return OverdraftTerms{}, err
	}
	return row, nil
}

// ledgerIDTx resolves the ledger the register's own subledger lives under, so
// that ensureChartTx can file the two interest folders as siblings of it.
func (r *Register) ledgerIDTx(ctx context.Context, tx Tx) (ledger.LedgerID, error) {
	sub, err := tx.GetSubledger(ctx, r.bookID, r.customers)
	if err != nil {
		return "", err
	}
	return sub.LedgerID, nil
}

// ensureChartTx opens the three lines an asset's deposit accounts post to and
// maps this layer's slots onto them, on the first account opened in that asset.
func (r *Register) ensureChartTx(ctx context.Context, tx Tx, asset ledger.AssetCode) error {
	switch _, err := r.gl.SlotAccountTx(ctx, tx, "", principalSlot, asset); {
	case err == nil:
		return nil
	case !errors.Is(err, ledger.ErrSlotNotMapped):
		return err
	}

	principal, err := r.gl.EnsureControlAccountTx(ctx, tx, r.customers, customerDepositsName(asset), ledger.Liability, asset)
	if err != nil {
		return err
	}
	if err := r.gl.MapSlotTx(ctx, tx, "", principalSlot, asset, principal.ID); err != nil {
		return err
	}
	ledgerID, err := r.ledgerIDTx(ctx, tx)
	if err != nil {
		return err
	}
	receivables, err := r.gl.EnsureSubledgerTx(ctx, tx, ledgerID, receivableSubledgerName)
	if err != nil {
		return err
	}
	receivable, err := r.gl.EnsureControlAccountTx(ctx, tx, receivables.ID, accruedInterestName(asset), ledger.Asset, asset)
	if err != nil {
		return err
	}
	if err := r.gl.MapSlotTx(ctx, tx, "", receivableSlot, asset, receivable.ID); err != nil {
		return err
	}
	incomeSub, err := r.gl.EnsureSubledgerTx(ctx, tx, ledgerID, incomeSubledgerName)
	if err != nil {
		return err
	}
	income, err := r.gl.EnsureAccountTx(ctx, tx, incomeSub.ID, interestIncomeName(asset), ledger.Revenue, asset)
	if err != nil {
		return err
	}
	return r.gl.MapSlotTx(ctx, tx, "", incomeSlot, asset, income.ID)
}

// depositControlTx resolves the control account this bank's customer money in
// an asset is pooled in: one read of the mapping, and no account name anywhere
// on the path.
func (r *Register) depositControlTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (ledger.AccountID, error) {
	return r.gl.SlotAccountTx(ctx, tx, "", principalSlot, asset)
}

// positionTx is where an account's money is: the account filling the principal
// slot for its asset, under the account's own id.
func (r *Register) positionTx(ctx context.Context, tx Tx, acct Account) (ledger.Position, error) {
	return r.gl.SlotPositionTx(ctx, tx, "", principalSlot, acct.Asset, string(acct.ID))
}

// Position is where a deposit account's money is in the general ledger, for a
// layer above that has to post to it.
func (r *Register) Position(ctx context.Context, id AccountID) (ledger.Position, error) {
	var out ledger.Position
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.PositionTx(ctx, tx, id)
		return err
	})
	return out, err
}

// ControlAccount is the chart-of-accounts line an asset's customer money is
// pooled in — the account half of every Position this register hands out.
func (r *Register) ControlAccount(ctx context.Context, asset ledger.AssetCode) (ledger.AccountID, error) {
	var out ledger.AccountID
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.depositControlTx(ctx, tx, asset)
		return err
	})
	return out, err
}

// PositionTx is Position within a caller-supplied unit of work.
func (r *Register) PositionTx(ctx context.Context, tx Tx, id AccountID) (ledger.Position, error) {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return ledger.Position{}, err
	}
	return r.positionTx(ctx, tx, acct)
}

// interestAccounts is where one account's overdraft interest moves between: the
// customer's own position, and their share of the bank's accrued-interest
// receivable.
type interestAccounts struct {
	Customer   ledger.Position
	Receivable ledger.Position
}

// interestAccountsTx resolves both for one account. ensureChartTx mapped the
// slots when the first account in the asset was opened, so ErrSlotNotMapped here
// is a chart that has been tampered with rather than a first use.
func (r *Register) interestAccountsTx(ctx context.Context, tx Tx, acct Account) (interestAccounts, error) {
	customer, err := r.positionTx(ctx, tx, acct)
	if err != nil {
		return interestAccounts{}, err
	}
	receivable, err := r.gl.SlotPositionTx(ctx, tx, "", receivableSlot, acct.Asset, string(acct.ID))
	if err != nil {
		return interestAccounts{}, err
	}
	return interestAccounts{Customer: customer, Receivable: receivable}, nil
}

// interestIncomeTx resolves the revenue line an account's overdraft interest is
// earned into, for the product pricing the day being accrued.
func (r *Register) interestIncomeTx(ctx context.Context, tx Tx, productID product.ID, asset ledger.AssetCode) (ledger.AccountID, error) {
	return r.gl.SlotAccountTx(ctx, tx, string(productID), incomeSlot, asset)
}

// GetAccount retrieves a deposit account by its ID; ErrAccountNotFound if absent.
func (r *Register) GetAccount(ctx context.Context, id AccountID) (Account, error) {
	var out Account
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetDepositAccount(ctx, r.bookID, id)
		return err
	})
	return out, err
}

// OverdraftTermsHistory returns an account's whole terms timeline, oldest
// first. It is the point of making terms effective-dated: the history is
// inspectable rather than merely recoverable by replaying the audit log.
func (r *Register) OverdraftTermsHistory(ctx context.Context, id AccountID) ([]OverdraftTerms, error) {
	var out []OverdraftTerms
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetDepositAccount(ctx, r.bookID, id); err != nil {
			return err
		}
		var err error
		out, err = tx.ListOverdraftTermsForAccount(ctx, r.bookID, id)
		return err
	})
	return out, err
}

// GetAccountWithTerms returns an account alongside what its overdraft costs
// today: the RESOLVED merge of its own row and its product's version, not the
// raw row.
func (r *Register) GetAccountWithTerms(ctx context.Context, id AccountID) (AccountWithTerms, error) {
	var out AccountWithTerms
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
		if err != nil {
			return err
		}
		rows, err := tx.ListOverdraftTermsForAccount(ctx, r.bookID, id)
		if err != nil {
			return err
		}
		cache := versionCache{}
		if err := r.loadForTerms(ctx, tx, rows, cache); err != nil {
			return err
		}
		terms, err := Resolve(rows, cache, ledger.DayStart(r.now()))
		if err != nil {
			return err
		}
		out = AccountWithTerms{Account: acct, Terms: terms}
		return nil
	})
	return out, err
}

// ListAccountsWithTerms is GetAccountWithTerms over the whole book, in ONE unit
// of work: resolving each account through its own View would make a listing N
// units of work over a store that refuses to nest them at all.
func (r *Register) ListAccountsWithTerms(ctx context.Context) ([]AccountWithTerms, error) {
	var out []AccountWithTerms
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		accounts, err := tx.ListDepositAccounts(ctx, r.bookID)
		if err != nil {
			return err
		}
		today := ledger.DayStart(r.now())
		cache := versionCache{}
		out = make([]AccountWithTerms, 0, len(accounts))
		for _, acct := range accounts {
			rows, err := tx.ListOverdraftTermsForAccount(ctx, r.bookID, acct.ID)
			if err != nil {
				return err
			}
			if err := r.loadForTerms(ctx, tx, rows, cache); err != nil {
				return err
			}
			terms, err := Resolve(rows, cache, today)
			if err != nil {
				return err
			}
			out = append(out, AccountWithTerms{Account: acct, Terms: terms})
		}
		return nil
	})
	return out, err
}

// checkIdentifierFreeTx refuses an identifier another account at this bank
// already holds. owner, when non-empty, is the account it is being added TO:
// that account already holding it is a no-op rather than a collision, which is
// what makes a retried AddIdentifier succeed twice.
func (r *Register) checkIdentifierFreeTx(ctx context.Context, tx Tx, owner AccountID, ident Identifier) error {
	holders, err := tx.ListDepositAccountsByIdentifier(ctx, r.bookID, ident)
	if err != nil {
		return err
	}
	for _, h := range holders {
		if h.ID != owner {
			return ErrIdentifierTaken
		}
	}
	return nil
}

// AddIdentifier gives an existing account another external address, in a scheme
// somebody else issues.
func (r *Register) AddIdentifier(ctx context.Context, id AccountID, ident Identifier) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.AddIdentifierTx(ctx, tx, id, ident)
	})
}

// AddIdentifierTx is AddIdentifier within a caller-supplied unit of work.
func (r *Register) AddIdentifierTx(ctx context.Context, tx Tx, id AccountID, ident Identifier) error {
	if err := ident.Validate("identifier"); err != nil {
		return err
	}
	if ident.Scheme == IdentifierIBAN {
		return ErrIBANIsIssued
	}
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	if err := r.checkIdentifierFreeTx(ctx, tx, id, ident); err != nil {
		return err
	}
	// Matches and not ==: an account holds an ADDRESS, and a scheme with two
	// spellings of one would leave it holding what looks like two.
	for _, got := range acct.Identifiers {
		if got.Matches(ident) {
			return nil // already held by this account: a no-op, not an error
		}
	}
	acct.Identifiers = append(acct.Identifiers, ident)
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventIdentifierAdded, string(id), ident)
}

// RemoveIdentifier withdraws an external address. Removing one that is not held
// is a no-op, for the same reason adding one twice is.
func (r *Register) RemoveIdentifier(ctx context.Context, id AccountID, ident Identifier) error {
	return r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return r.RemoveIdentifierTx(ctx, tx, id, ident)
	})
}

// RemoveIdentifierTx is RemoveIdentifier within a caller-supplied unit of work.
func (r *Register) RemoveIdentifierTx(ctx context.Context, tx Tx, id AccountID, ident Identifier) error {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	kept := make([]Identifier, 0, len(acct.Identifiers))
	var removed Identifier
	for _, got := range acct.Identifiers {
		if got.Matches(ident) {
			removed = got
			continue
		}
		kept = append(kept, got)
	}
	if removed == (Identifier{}) {
		return nil
	}
	acct.Identifiers = kept
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventIdentifierRemoved, string(id), removed)
}

// ResolveIdentifier returns the account this bank addresses by ident.
func (r *Register) ResolveIdentifier(ctx context.Context, ident Identifier) (Account, error) {
	var out Account
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.ResolveIdentifierTx(ctx, tx, ident)
		return err
	})
	return out, err
}

// ResolveIdentifierTx is ResolveIdentifier within a caller-supplied unit of work.
func (r *Register) ResolveIdentifierTx(ctx context.Context, tx Tx, ident Identifier) (Account, error) {
	holders, err := tx.ListDepositAccountsByIdentifier(ctx, r.bookID, ident)
	if err != nil {
		return Account{}, err
	}
	switch len(holders) {
	case 0:
		return Account{}, ErrIdentifierNotFound
	case 1:
		return holders[0], nil
	default:
		return Account{}, ErrIdentifierAmbiguous
	}
}

// ---------------------------------------------------------------------------
// Status Lifecycle
// ---------------------------------------------------------------------------

// Freeze transitions an account from Active to Frozen, blocking new holds and
// withdrawals until it is unfrozen.
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
// in the expected one, and records the event.
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

// Close permanently closes an account. Closed is terminal, and closing is
// permitted from any other state.
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

	at, err := r.interestAccountsTx(ctx, tx, acct)
	if err != nil {
		return err
	}
	book, err := r.gl.BookBalanceTx(ctx, tx, at.Customer)
	if err != nil {
		return err
	}
	if book != 0 {
		return ErrAccountNotEmpty
	}
	// This account's share of the receivable, never the receivable's own
	// balance: the pool holds what every other customer owes as well, and a
	// customer who owes nothing may not be held open by them.
	receivable, err := r.gl.BookBalanceTx(ctx, tx, at.Receivable)
	if err != nil {
		return err
	}
	if receivable != 0 {
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

	Description string
}

// CreateHold places an authorization hold on a deposit account, reducing its
// available balance without affecting the book balance.
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

// ReleaseHold cancels an active hold, restoring the available balance. Returns
// ErrHoldNotFound, or ErrHoldNotActive if it is already released or captured.
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

// CaptureHold converts an active hold into a posted general-ledger transaction.
// Customer money is a Liability; capturing (money leaving the customer) DEBITS
// the customer's position in the control account and CREDITs the counterparty.
func (r *Register) CaptureHold(ctx context.Context, id HoldID, counterparty ledger.Position, captureAmount ledger.Amount, description string) (ledger.Transaction, error) {
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
func (r *Register) CaptureHoldTx(ctx context.Context, tx Tx, id HoldID, counterparty ledger.Position, captureAmount ledger.Amount, description string) (ledger.Transaction, error) {
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
	pos, err := r.positionTx(ctx, tx, acct)
	if err != nil {
		return ledger.Transaction{}, err
	}

	// Same tx as the hold write below — both commit or neither does. Note
	// PostTransactionTx, not PostTransaction: the latter would open a second
	// unit of work inside this one.
	glTx, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		Entries: []ledger.Entry{
			{AccountID: pos.Account, Subsidiary: pos.Subsidiary, Amount: captureAmount, Direction: ledger.Debit},
			{AccountID: counterparty.Account, Subsidiary: counterparty.Subsidiary, Amount: captureAmount, Direction: ledger.Credit},
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

// GetHold retrieves a hold by its ID; ErrHoldNotFound if it does not exist.
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

// GetBalance computes the current balance of a deposit account: the Book
// balance of the backing Liability account, the sum of active non-expired
// Holds, and Available = Book - Holds + the overdraft limit in force today,
// resolved from the account's effective-dated terms timeline.
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

// CheckCredit reports whether the account may currently RECEIVE money. It is
// the counterpart of CheckWithdrawal and refuses only a Closed account, with
// ErrAccountClosed; see requireCreditable for why Dormant and Frozen do not.
func (r *Register) CheckCredit(ctx context.Context, id AccountID) error {
	return r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		return r.CheckCreditTx(ctx, tx, id)
	})
}

// CheckCreditTx is CheckCredit within a caller-supplied unit of work, so a
// caller can check the status and post the credit atomically. Checking in one
// unit of work and posting in another would let an account close in between.
func (r *Register) CheckCreditTx(ctx context.Context, tx Tx, id AccountID) error {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return err
	}
	return requireCreditable(acct)
}

// CheckWithdrawal reports whether the account may currently support a
// withdrawal of amount.
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
// It guards money going OUT — a withdrawal or a new hold — which is why every
// status other than Active fails it, dormancy included.
func requireActive(acct Account) error {
	switch acct.Status {
	case Active:
		return nil
	case Dormant:
		return ErrAccountDormant
	case Frozen:
		return ErrAccountFrozen
	case Closed:
		return ErrAccountClosed
	default:
		return ErrInvalidStatusTransition
	}
}

// requireCreditable returns an error if the account may not RECEIVE money. It
// is requireActive's counterpart and deliberately far more permissive, because
// the two questions are not symmetric.
func requireCreditable(acct Account) error {
	if acct.Status == Closed {
		return ErrAccountClosed
	}
	return nil
}

// balanceTx computes an account's three balances within a unit of work.
func (r *Register) balanceTx(ctx context.Context, tx Tx, acct Account) (Balance, error) {
	pos, err := r.positionTx(ctx, tx, acct)
	if err != nil {
		return Balance{}, err
	}
	book, err := r.gl.BookBalanceTx(ctx, tx, pos)
	if err != nil {
		return Balance{}, err
	}
	holds, err := ActiveHoldTotal(ctx, tx, r.bookID, acct.ID, r.now())
	if err != nil {
		return Balance{}, err
	}
	terms, err := tx.GetOverdraftTermsAsOf(ctx, r.bookID, acct.ID, ledger.DayStart(r.now()))
	if err != nil {
		return Balance{}, err
	}
	return Balance{
		Book:      book,
		Holds:     holds,
		Available: book - holds + terms.OverdraftLimit,
	}, nil
}

// availableTx computes the available balance of an account:
// Book - Holds + the overdraft limit in force today.
func (r *Register) availableTx(ctx context.Context, tx Tx, acct Account) (ledger.Amount, error) {
	bal, err := r.balanceTx(ctx, tx, acct)
	if err != nil {
		return 0, err
	}
	return bal.Available, nil
}

// ---------------------------------------------------------------------------
// End-of-Day Balance Snapshots
// ---------------------------------------------------------------------------

// TakeEndOfDaySnapshot computes and stores the balance snapshot for a deposit
// account on a given business date, overwriting any snapshot for the same
// account and date. Returns ErrAccountNotFound.
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

// GetSnapshot retrieves an end-of-day balance snapshot for an account and business
// date. Returns ErrAccountNotFound, or ErrSnapshotNotFound.
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
	// A fresh cache: one account, so there is nothing to share. RunEndOfDayTx
	// is where the type earns its place.
	return r.accrueOverdraftAccountTx(ctx, tx, acct, date, versionCache{})
}

// accrueOverdraftAccountTx is AccrueOverdraftTx against an account the caller
// has already loaded. RunEndOfDay lists every account and would otherwise read
// each one a second time.
func (r *Register) accrueOverdraftAccountTx(ctx context.Context, tx Tx, acct Account, date time.Time, cache versionCache) error {
	if acct.Status == Closed {
		return nil
	}

	// The whole timeline, in one read, resolved per day in Go below.
	rows, err := tx.ListOverdraftTermsForAccount(ctx, r.bookID, acct.ID)
	if err != nil {
		return err
	}
	if err := r.loadForTerms(ctx, tx, rows, cache); err != nil {
		return err
	}
	if !anyPriced(rows, cache) {
		return nil
	}

	// The window opens at the earliest row, which is the opening row, which is
	// inception. Every nightly run therefore re-derives every day the account has
	// had: O(days) per account per night, accepted deliberately at this scale.
	window := rows[0].EffectiveFrom

	// The advancement guard resolves its day count on `date`, there being no
	// single DayCount to ask: it is a terms field, and the conventions genuinely
	// disagree about whether a window advanced.
	current, err := Resolve(rows, cache, date)
	if errors.Is(err, ErrTermsNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Pricing.DayCount.Days(acct.LastAccrualDate, date) <= 0 {
		return nil
	}

	at, err := r.interestAccountsTx(ctx, tx, acct)
	if err != nil {
		return err
	}
	// One customer's series and not the pool's: the control account nets every
	// other customer's credit balance against this one's overdraft, so a series
	// read there would accrue interest on a number nobody owes.
	series, err := r.gl.SeriesTx(ctx, tx, at.Customer, window, date)
	if err != nil {
		return err
	}
	// A Period cannot return an error, so a resolution failure inside the walk is
	// captured and checked before anything is applied.
	var resolveErr error
	next, delta := interest.Recompute(series, window, date,
		interest.State{Accrued: acct.Accrued, Gross: acct.AccruedGross},
		func(balance ledger.Amount, from, to time.Time) interest.Accrued {
			// perDay has already cut the window to single days before any perDay has
			// already cut the window to single days before any Period runs, so this
			// closure is a function of the DAY as well as the balance, which is what a
			// Period is for.
			day, err := Resolve(rows, cache, to)
			if err != nil {
				// A day before the account existed is not a failure: the window
				// opens at the opening row, so it cannot arise. Anything else is.
				if !errors.Is(err, ErrTermsNotFound) && resolveErr == nil {
					resolveErr = err
				}
				return 0
			}
			return overdraftAccrual(balance, day, from, to)
		})
	if resolveErr != nil {
		return resolveErr
	}

	acct.Accrued = next.Accrued
	acct.AccruedGross = next.Gross
	acct.LastAccrualDate = date

	if delta == 0 {
		// The rounding did not tick. There is nothing to post, and a
		// zero-amount entry is refused by the ledger anyway.
		if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
			return err
		}
		return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrued, string(acct.ID), acct)
	}

	// The revenue line for the product pricing THIS DAY — `current` is the row
	// the walk above priced its last span at.
	income, err := r.interestIncomeTx(ctx, tx, current.ProductID, acct.Asset)
	if err != nil {
		return err
	}

	// A correction can settle part of the record in cash, which moves Accrued
	// again, so it owns the write the way ChargeOverdraftInterestTx does.
	if delta < 0 {
		return r.correctOverdraftAccrualTx(ctx, tx, &acct, at, income, -delta, date)
	}

	if _, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Overdraft interest accrued: " + acct.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries: []ledger.Entry{
			{AccountID: at.Receivable.Account, Subsidiary: at.Receivable.Subsidiary, Amount: delta, Direction: ledger.Debit},
			{AccountID: income, Amount: delta, Direction: ledger.Credit},
		},
	}); err != nil {
		return err
	}
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrued, string(acct.ID), acct)
}

// correctOverdraftAccrualTx gives back interest that a backdated posting has
// shown was never owed. amount is positive.
func (r *Register) correctOverdraftAccrualTx(ctx context.Context, tx Tx, acct *Account, at interestAccounts, income ledger.AccountID, amount ledger.Amount, date time.Time) error {
	// What THIS customer's interest can be credited back out of. The pool holds
	// every other customer's accrual too, and absorbing against that would give
	// this one their money back out of somebody else's receivable.
	receivable, err := r.gl.BookBalanceTx(ctx, tx, at.Receivable)
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
	refund := amount - absorbed

	entries := []ledger.Entry{{AccountID: income, Amount: amount, Direction: ledger.Debit}}
	if absorbed > 0 {
		entries = append(entries, ledger.Entry{AccountID: at.Receivable.Account, Subsidiary: at.Receivable.Subsidiary, Amount: absorbed, Direction: ledger.Credit})
	}
	if refund > 0 {
		entries = append(entries, ledger.Entry{AccountID: at.Customer.Account, Subsidiary: at.Customer.Subsidiary, Amount: refund, Direction: ledger.Credit})
	}

	glTx, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Overdraft interest corrected: " + acct.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries:     entries,
	})
	if err != nil {
		return err
	}

	acct.Accrued += interest.FromMinor(refund)
	if err := tx.PutDepositAccount(ctx, r.bookID, *acct); err != nil {
		return err
	}
	return r.appendAuditTx(ctx, tx, ledger.EventOverdraftAccrualCorrected, string(acct.ID), map[string]any{
		"account_id":     string(acct.ID),
		"amount":         amount,
		"absorbed":       absorbed,
		"refund":         refund,
		"transaction_id": string(glTx.ID),
		"residue":        int64(acct.Accrued),
	})
}

// overdraftAccrual is the interest earned on an account's overdrawn balance
// over one accrual period, tiered at the arranged limit.
func overdraftAccrual(book ledger.Amount, t EffectiveTerms, from, to time.Time) interest.Accrued {
	drawn := -book
	if drawn <= 0 {
		return 0
	}
	arranged := drawn
	if arranged > t.Limit {
		arranged = t.Limit
	}
	total := interest.Accrue(arranged, t.Pricing.Rate, t.Pricing.DayCount, from, to)
	if excess := drawn - arranged; excess > 0 {
		rate := t.Pricing.UnarrangedRate
		if rate == 0 {
			rate = t.Pricing.Rate
		}
		total += interest.Accrue(excess, rate, t.Pricing.DayCount, from, to)
	}
	return total
}

// versionCache is one accrual run's product timelines, loaded on first use and
// reused for every account after it: a book of ten thousand accounts on three
// products does three reads for the whole run rather than ten thousand.
type versionCache map[product.ID][]product.Version

// loadForTerms fills the cache with every product the given rows name.
func (r *Register) loadForTerms(ctx context.Context, tx Tx, rows []OverdraftTerms, cache versionCache) error {
	for _, row := range rows {
		if _, ok := cache[row.ProductID]; ok {
			continue
		}
		versions, err := tx.ListProductVersions(ctx, r.bookID, row.ProductID)
		if err != nil {
			return err
		}
		cache[row.ProductID] = versions
	}
	return nil
}

// ChargeOverdraftInterest capitalizes accrued interest into the account,
// clearing the receivable.
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
	if charge <= 0 {
		return ledger.Transaction{}, nil
	}
	at, err := r.interestAccountsTx(ctx, tx, acct)
	if err != nil {
		return ledger.Transaction{}, err
	}

	// Value-dated at date, which means the day ENDING on it is re-priced at the
	// capitalised balance: the recompute walks the value-dated series, and this
	// debit is in the series from date onwards, so the span [date-1, date] is
	// derived over a balance that already includes the charge.
	glTx, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Overdraft interest charged: " + acct.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries: []ledger.Entry{
			{AccountID: at.Customer.Account, Subsidiary: at.Customer.Subsidiary, Amount: charge, Direction: ledger.Debit},
			{AccountID: at.Receivable.Account, Subsidiary: at.Receivable.Subsidiary, Amount: charge, Direction: ledger.Credit},
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
	// One cache for the whole run: a book of ten thousand accounts on three
	// products reads three product timelines, not ten thousand.
	cache := versionCache{}
	for _, acct := range accounts {
		if err := r.accrueOverdraftAccountTx(ctx, tx, acct, date, cache); err != nil {
			return err
		}
	}
	return nil
}

// Totals aggregates every customer account in the book into deposits and
// overdrafts, per asset. See the Totals type for why the Asset-side figure is
// computed here rather than posted anywhere.
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
	// One balance per account and not one per control line, because the split is
	// by the SIGN of each customer's own balance and the pool has one sign.
	for _, acct := range accounts {
		pos, err := r.positionTx(ctx, tx, acct)
		if err != nil {
			return Totals{}, err
		}
		balance, err := r.gl.BookBalanceTx(ctx, tx, pos)
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
func (r *Register) GetAuditLog(ctx context.Context) ([]ledger.AuditEvent, error) {
	var out []ledger.AuditEvent
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListAudit(ctx, ledger.AuditFilter{BookID: r.bookID, Scope: ledger.ScopeDeposit})
		return err
	})
	return out, err
}
