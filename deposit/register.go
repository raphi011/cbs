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

// Register is the demand-deposit layer over a general ledger. It manages
// customer deposit accounts, their status lifecycle, authorization holds, the
// available balance those holds reduce, and end-of-day snapshots.
//
// # Relationship to the ledger
//
// No deposit account is a row in the chart of accounts. One Liability CONTROL
// account per asset holds every customer's money, and each posting names whose
// it is; a customer's balance is that account's balance under their id, and the
// bank's total customer deposits is the same sum with the id dropped. The
// Register never stores money itself: opening an account creates no GL account
// at all, and capturing a hold posts a real GL transaction.
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
// that would open a second unit of work inside the first, which the store
// refuses outright — see sqlite.ErrNestedTransaction for what it would cost if
// it did not. This is why CaptureHoldTx calls Book.PostTransactionTx rather than
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

	// issuer is what this register mints addresses under.
	//
	// It is CONSTRUCTOR state rather than a per-call argument, for the reason
	// payment.Identity is: as an argument, every caller asserts which bank's
	// addresses it is issuing and the register believes it, and a caller that
	// got it wrong opens an account at another bank's address. As constructor
	// state there is no call at which a different answer could be given.
	//
	// It is not an institution, and this layer still does not know what one is.
	// It is two strings a register was told, exactly as a BookID is. Where they
	// come from is the payment layer's business.
	issuer iban.Issuer

	// customers is the subledger this register files its control lines in.
	//
	// It is CONSTRUCTOR state for the reason issuer is, and the cost of getting
	// it wrong is larger: as a per-call argument a caller could file the second
	// account of an asset in another folder, which would create a SECOND
	// customer-deposit control account for the same pool. The two would each
	// hold part of the money and neither would be the bank's deposits.
	customers ledger.SubledgerID
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
// issuer is what customer addresses are minted under; see Issuer. The zero
// value is legal to construct and refuses to open an account (ErrNoIssuer),
// because a bank that has not been allocated a bank code has no address to give
// anybody — which is a real state, between founding and admission.
//
// customers is the subledger the register's control lines are filed in; the
// first account opened in an asset creates them there.
//
// Example:
//
//	s, _ := sqlite.Open(ctx, "", time.Now)
//	book := ledger.NewBook(s, "bank", time.Now)
//	reg := deposit.NewRegister(s.Deposit(), book, "bank", time.Now,
//		iban.Issuer{Country: iban.DE, BankCode: "99900001"}, customers.ID)
func NewRegister(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time, issuer iban.Issuer, customers ledger.SubledgerID) *Register {
	return &Register{store: store, gl: book, bookID: id, clock: clock, issuer: issuer, customers: customers}
}

// Issuer returns what this register mints addresses under.
func (r *Register) Issuer() iban.Issuer { return r.issuer }

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

// OpenAccount opens a new customer deposit account in the Active state.
//
// It adds nothing to the chart of accounts. The account's money will sit in the
// bank's customer-deposit control account for its asset, which the first account
// opened in that asset creates along with the two interest lines that go with it
// (see ensureChartTx). The hundred-thousandth account creates nothing at all.
//
// asset is the unit the account is denominated in; it must already be
// registered in the underlying book. A customer holding two assets holds two
// accounts.
//
// productID is the catalogue entry the account is priced by. Every account has
// one: a floating terms row with no product would have nothing to float to, and
// making that unreachable is cheaper than handling it. The product must exist,
// must not be Retired, must be of Kind CurrentAccount, and must have a published
// version in force today — an account opened from an unpriced product could not
// resolve a single day, and refusing here is what stops that surfacing as an
// accrual failure weeks later.
//
// overdraftLimit is a positive amount the account may go below zero by; 0
// means no overdraft is permitted. It is NOT part of the product: a limit is an
// underwriting decision about this customer, so it is passed per account and
// stays on the account's own timeline for life. The asset comes before it so
// that the two ledger-typed arguments are not adjacent and transposable.
//
// identifiers are the account's external addresses OTHER than its IBAN, which
// this bank issues rather than accepts: an account always comes out of here with
// one, minted under the register's Issuer, and a caller supplying one is refused
// ErrIBANIsIssued. Zero others is legal and normal — a card PAN is the only kind
// this system has a constant for, and most accounts have none.
//
// Each must be unique within THIS bank; a collision is ErrIdentifierTaken.
// Uniqueness is not checked across banks, and does not need to be: a bank-issued
// identifier carries its own issuer (an IBAN its bank code, a PAN its BIN), so
// two banks cannot collide without one of them issuing addresses it was never
// allocated — which is now something this layer refuses rather than something it
// relies on.
//
// Returns product.ErrProductNotFound, product.ErrProductRetired,
// product.ErrKindMismatch, product.ErrVersionNotFound, and any error from the
// underlying ledger (for example ledger.ErrSubledgerNotFound if the register's
// subledger does not exist, or ledger.ErrAssetNotFound if the asset is not
// registered).
func (r *Register) OpenAccount(ctx context.Context, name string, asset ledger.AssetCode, productID product.ID, overdraftLimit ledger.Amount, identifiers ...Identifier) (Account, error) {
	var out Account
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.OpenAccountTx(ctx, tx, name, asset, productID, overdraftLimit, identifiers...)
		return err
	})
	return out, err
}

// OpenAccountTx is OpenAccount within a caller-supplied unit of work. The
// asset's control lines, the deposit account and the account's first terms row
// are created through the same Tx, so an account can never exist in one layer
// without the other — which is what deposit.Tx embedding product.Tx exists for.
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
		// refusal is here as well as at AddIdentifier because opening is the
		// other door, and it is the one the API's request body reaches.
		if ident.Scheme == IdentifierIBAN {
			return Account{}, ErrIBANIsIssued
		}
		// Siblings in THIS call, not only accounts already in the store.
		// checkIdentifierFreeTx reads the register, and the account being opened
		// is not in it yet, so `identifiers: [X, X]` — which the API accepts
		// verbatim from a request body — would sail past it. It must not: the
		// store's identifier rows carry (scheme, value) in their primary key, so
		// the list means ONE address once it is written and two while it is in a
		// Go slice. Refusing beats collapsing silently: a caller who listed one
		// address twice either meant two different addresses and mistyped one,
		// or is sending a list it has not deduplicated, and both are worth being
		// told about.
		//
		// ContainsFunc over Matches, which for everything reaching here is ==:
		// the loop above has already refused the only scheme with a second
		// spelling. Matches anyway, because "the same address" is one rule and
		// Identifier.Matches is where it lives — comparing literally would be a
		// second definition of it, correct only until some other scheme grows a
		// display form.
		if slices.ContainsFunc(identifiers[:i], ident.Matches) {
			return Account{}, ErrIdentifierTaken
		}
		if err := r.checkIdentifierFreeTx(ctx, tx, "", ident); err != nil {
			return Account{}, err
		}
	}

	// Minted BEFORE the chart lines, so a register with no bank code refuses
	// having created nothing. The order costs nothing — both are in this Tx and
	// roll back together — and it keeps the refusal cheap to reason about.
	address, err := r.mintAddressTx(ctx, tx)
	if err != nil {
		return Account{}, err
	}

	// This is also where an unknown asset is refused: every ensure below falls
	// through to a create when the line is not there, and a create validates the
	// code. So the tenth EUR account resolves three existing rows and the first
	// DOGE account is turned away with nothing written.
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
		// The minted address FIRST, and the caller's other schemes after it.
		// Nothing depends on the order — addressFor picks by scheme, not by
		// position — but an account's own IBAN leading the list is what a
		// statement and a console both show.
		Identifiers: append([]Identifier{address}, identifiers...),
	}
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return Account{}, err
	}

	// Every account gets a terms row from birth, carrying the limit it was
	// opened with and NO overlay.
	//
	// This is cleaner than treating "no rows" as a state the resolver has to
	// model: it makes the recompute window start uniform, and it means the
	// timeline answers for every day the account has existed.
	//
	// The row is FLOATING — its pricing resolves from the product version in
	// force on each day — which is what makes a later product-wide reprice reach
	// this account without a write to it.
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
//
// Retired is checked HERE and never at resolution, which is the distinction that
// lets a product go off sale without the accounts sold from it losing their
// price — see product.ErrProductRetired.
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
// in the customer-deposit folder, which holds what the bank OWES its customers:
// interest a customer owes the bank is an Asset, and a folder holding both sides
// of the balance sheet is one nothing can be summed out of.
const receivableSubledgerName = "Accrued Interest"

// incomeSubledgerName is where interest income is filed.
const incomeSubledgerName = "Income"

// The three lines an asset's deposit accounts post to. One set per asset, and
// one set per BANK: what makes them one line rather than one per customer is the
// obligor named on every entry against the first two. The asset is in each name
// because an account and its asset are inseparable, and a chart of accounts
// holding several of each needs to tell them apart.
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
//
// It is the PINNED half of the old SetOverdraftTerms: a limit is an underwriting
// decision about one customer and never comes from the catalogue. The pricing
// and the product are carried forward from the row in force on effectiveFrom,
// because each row is a complete statement of the account's own terms from its
// day — dropping the overlay here would silently reprice a customer who was
// promised a rate.
//
// # effectiveFrom, and the two directions it may point
//
// effectiveFrom is the day the change takes economic effect, day-truncated here;
// CreatedAt on the returned row is when it was entered. A ZERO effectiveFrom
// means today on the register's clock, exactly as a zero BookingDate does in
// ledger.PostTransactionRequest. Both directions are allowed, because both
// happen:
//
//   - A BACKDATED row is picked up by the next end-of-day exactly as a backdated
//     posting is. A backdated limit does not move interest by itself, but it does
//     change which part of a drawn balance was inside the limit on those days, so
//     the difference is trued up as ordinary delta interest. Nothing is rewritten.
//   - A FUTURE-DATED row is inert until the end-of-day runs reach its day, which
//     is scheduled repricing for free.
//
// The value returned is the row that was WRITTEN, which is not necessarily the
// row in force now. Use GetAccountWithTerms for what applies today.
//
// Returns ErrAccountNotFound, ErrAccountClosed, ErrInvalidAmount, and
// ErrTermsNotFound for a day before the account existed.
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
//
// pricing nil means FLOAT, not free. An account cleared back onto its product
// pays whatever the product costs by then, not what it cost when the overlay was
// set; a genuinely interest-free account is a zero-rate overlay, which is a
// different and deliberate statement.
//
// This is where retroactivity lives. A backdated overlay MOVES INTEREST THAT HAS
// ALREADY BEEN CHARGED TO ONE CUSTOMER, and the delta is posted rather than the
// history rewritten; the audit log is the only control on it, and every call
// appends an EventOverdraftPricingOverlaid event carrying the row, effective date
// and entry date alike. The catalogue refuses the same thing outright
// (product.ErrRetroactivePublish) precisely because its blast radius is every
// account on the product rather than one named customer.
//
// A zero effectiveFrom means today, as it does for SetOverdraftLimit.
//
// Returns ErrAccountNotFound, ErrAccountClosed, ErrInvalidRate, and
// ErrTermsNotFound.
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
//
// It is a forward-dated row like any other, which is the point: the days before
// it still resolve against the product that priced them, so "what did this
// account's product say on 15 July 2027?" survives a migration. A migration is
// not a rewrite.
//
// A negotiated overlay is carried forward untouched. A rate promised to a
// customer is a promise, and dropping it as a side effect of a migration would
// reprice them without a decision; clearing it is an explicit
// SetOverdraftPricingOverlay(nil, day) call, so each method changes one thing.
//
// Returns ErrAccountNotFound, ErrAccountClosed, ErrTermsNotFound, and the
// product refusals checkOpenableProductTx makes.
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
// on the effective day, change the one thing this caller changes, and append the
// result as a new row.
//
// Carrying the in-force row forward is what makes each row a complete statement
// of the account's terms rather than a diff — which is what lets termsAt answer
// with one row and no accumulation, and what makes an out-of-order sequence of
// backdated changes mean something definite.
//
// It appends and never edits: the row it read stays exactly as it was, so a past
// day still re-derives at the terms that were in force on it.
//
// No setter creates the receivable any more. A floating account's rate lives in
// the catalogue, so no register call knows it; the receivable is created by the
// accrual, on the first day that actually accrues.
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

// ensureChartTx creates the three lines an asset's deposit accounts post to, on
// the first account opened in that asset.
//
// All three at once, including the income account, although an account that
// never goes overdrawn will never post to it. A bank that takes deposits in an
// asset earns interest in it, and the line is a statement about the bank rather
// than about any customer; creating them together is also what lets every other
// path here RESOLVE rather than ensure, so no read is one caller away from a
// write. The receivable is one of the three and not one per customer, because a
// shared one duplicates nothing: the per-customer detail is the entries under
// the dimension rather than a figure that would have to be stored beside it.
func (r *Register) ensureChartTx(ctx context.Context, tx Tx, asset ledger.AssetCode) error {
	if _, err := r.gl.EnsureControlAccountTx(ctx, tx, r.customers, customerDepositsName(asset), ledger.Liability, asset); err != nil {
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
	if _, err := r.gl.EnsureControlAccountTx(ctx, tx, receivables.ID, accruedInterestName(asset), ledger.Asset, asset); err != nil {
		return err
	}
	income, err := r.gl.EnsureSubledgerTx(ctx, tx, ledgerID, incomeSubledgerName)
	if err != nil {
		return err
	}
	_, err = r.gl.EnsureAccountTx(ctx, tx, income.ID, interestIncomeName(asset), ledger.Revenue, asset)
	return err
}

// depositControlTx resolves the control account this bank's customer money in
// an asset is pooled in.
//
// Resolved by NAME on every call, which is what a chart of accounts bounded by
// the institution buys: the listing behind it is one control line per asset plus
// the bank's own positions — tens of rows, not one per customer, which is the
// condition ledger.EnsureSubledgerTx states this idiom is cheap under. The
// alternative is an id on the account row, and that is a pointer into the chart
// of accounts, which is precisely what a customer account does not have.
func (r *Register) depositControlTx(ctx context.Context, tx Tx, asset ledger.AssetCode) (ledger.AccountID, error) {
	acct, err := r.gl.FindControlAccountTx(ctx, tx, r.customers, customerDepositsName(asset), ledger.Liability, asset)
	if err != nil {
		return "", err
	}
	return acct.ID, nil
}

// positionTx is where an account's money is: the control account for its asset,
// under the account's own id.
func (r *Register) positionTx(ctx context.Context, tx Tx, acct Account) (ledger.Position, error) {
	control, err := r.depositControlTx(ctx, tx, acct.Asset)
	if err != nil {
		return ledger.Position{}, err
	}
	return control.For(string(acct.ID)), nil
}

// Position is where a deposit account's money is in the general ledger, for a
// layer above that has to post to it.
//
// It hands back both halves in one value because a caller carrying an account
// and an obligor apart would eventually pair one customer's account with
// another's id — and on a control account that posting balances, passes every
// check, and pays one customer out of another's money.
//
// Returns ErrAccountNotFound, and ledger.ErrAccountNotFound if the bank holds no
// control line for the account's asset.
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
//
// It exists for a caller rendering MANY accounts at once: the pair is per
// account and the line is per asset, so resolving it once is what stops a
// listing asking the chart of accounts the same question for every customer on
// it. One account's answer is Position.
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

// interestAccounts is where one account's overdraft interest moves: the
// customer's own position, their share of the bank's accrued-interest
// receivable, and the income line the bank earns it into.
//
// Two positions and one bare account, which is this whole arrangement in one
// type: what is owed BY a named customer carries their id, and what the bank has
// earned is the bank's own and carries nobody's.
type interestAccounts struct {
	Customer   ledger.Position
	Receivable ledger.Position
	Income     ledger.AccountID
}

// interestAccountsTx resolves all three for one account. ensureChartTx created
// them when the first account in the asset was opened, so a missing one here is
// a chart that has been tampered with rather than a first use.
func (r *Register) interestAccountsTx(ctx context.Context, tx Tx, acct Account) (interestAccounts, error) {
	customer, err := r.positionTx(ctx, tx, acct)
	if err != nil {
		return interestAccounts{}, err
	}
	ledgerID, err := r.ledgerIDTx(ctx, tx)
	if err != nil {
		return interestAccounts{}, err
	}
	receivables, err := r.gl.FindSubledgerTx(ctx, tx, ledgerID, receivableSubledgerName)
	if err != nil {
		return interestAccounts{}, err
	}
	receivable, err := r.gl.FindControlAccountTx(ctx, tx, receivables.ID, accruedInterestName(acct.Asset), ledger.Asset, acct.Asset)
	if err != nil {
		return interestAccounts{}, err
	}
	income, err := r.gl.FindSubledgerTx(ctx, tx, ledgerID, incomeSubledgerName)
	if err != nil {
		return interestAccounts{}, err
	}
	incomeAcct, err := r.gl.FindAccountTx(ctx, tx, income.ID, interestIncomeName(acct.Asset), ledger.Revenue, acct.Asset)
	if err != nil {
		return interestAccounts{}, err
	}
	return interestAccounts{
		Customer:   customer,
		Receivable: receivable.ID.For(string(acct.ID)),
		Income:     incomeAcct.ID,
	}, nil
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

// OverdraftTermsHistory returns an account's whole terms timeline, oldest
// first. It is the point of making terms effective-dated: the history is
// inspectable rather than merely recoverable by replaying the audit log.
//
// These are the RAW rows, not resolved terms: an overlay that was later cleared
// is exactly what a reader of a history wants to see, and so is the day the
// account changed product. What a day actually cost is GetAccountWithTerms.
//
// Returns ErrAccountNotFound.
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
// raw row. A floating account's rate is in the catalogue, so the raw row would
// answer "nil" to the question the caller is asking.
//
// Returns ErrAccountNotFound, ErrTermsNotFound only for an account that somehow
// has no opening row, product.ErrVersionNotFound for a floating account whose
// product has no published price today, and product.ErrHashMismatch for a
// version edited in the database.
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
// of work. Resolving each account through its own View would make a listing N
// units of work over a store that refuses to nest them at all.
//
// One version cache serves the whole listing, so a book of ten thousand accounts
// on three products reads three product timelines rather than ten thousand — the
// same dividend the accrual run takes.
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
// already holds. owner, when non-empty, is the account the identifier is being
// added TO: it already holding the identifier is a no-op rather than a
// collision, which is what makes a retried AddIdentifier succeed twice.
//
// "Already holds" is Identifier.Matches, because the lookup this delegates to
// is. No IBAN reaches here — a minted address is not checked against the
// register, because a serial is handed out once, and every other door refuses
// the scheme — so the two comparisons coincide on everything this actually
// sees. It is written as the shared rule regardless: uniqueness and routing
// disagreeing about what counts as one address is the defect this whole cluster
// of comparisons exists to prevent.
//
// The check is a read followed by a write with no constraint behind it and no
// lock above it, so two concurrent adds can both pass. That is deliberate. A
// UNIQUE index would fire on the race this lets through and would fire as a
// constraint violation rather than as ErrIdentifierTaken — a rule enforced in
// two places that disagree about WHEN is enforced in neither — so the residual
// duplicate is caught at read time instead, by ResolveIdentifier, which refuses
// rather than guesses. storetest/IdentifierUniquenessIsNotEnforcedAcrossSpellings
// pins that the store accepts the pair of spellings this refuses, so the refusal
// stays this layer's.
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
//
// Adding rather than replacing is the point of the plural: a customer keeps
// their IBAN and gains a card PAN, and reissuing a card is a remove plus an add
// against an account whose balance and history do not move.
//
// It refuses an IBAN (ErrIBANIsIssued). That address is this bank's to allocate,
// not a caller's to assert, and the act that replaces one is ReissueIdentifier —
// which mints and withdraws together, because remove-then-add no longer
// composes when the add is refused.
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
	// spellings of one would otherwise leave the account holding what looks like
	// two. Nothing downstream would report that, because both resolve to it, but
	// a payment quoting neither becomes ErrAmbiguousAddress — the account would
	// have lost the ability to be paid without an address being named. See
	// addressFor in the payment package. No scheme reaching here has two
	// spellings today; the one that did is the one this method refuses.
	//
	// The stored form is the one KEPT. A caller re-adding an address the account
	// already holds has told this bank nothing new, and rewriting the stored
	// value would edit what a statement shows and what every earlier payment
	// recorded, on the strength of a no-op.
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
//
// It withdraws the address in EITHER spelling: a caller quoting
// DE20 9990 0001 0000 0000 01 withdraws the account's DE20999000010000000001,
// because they are one address and the rest of the system already treats them as
// one. The alternative is worse than inconsistent — removal of an unheld
// identifier is a no-op by design, so a literal comparison would leave a bank
// that quoted the grouped form believing it had withdrawn an address that is
// still live and still payable, with no error to say otherwise. See
// TestRemoveIdentifierWithdrawsTheAddressInEitherSpelling.
//
// What the audit event records is the identifier as STORED, not as quoted. The
// trail says what happened to the account, and what happened is that the
// account's own address was withdrawn.
//
// Historical payments are unaffected: a payment stores the address it was sent
// to, so removing it here cannot rewrite what a settled payment says.
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
//
// Zero matches is ErrIdentifierNotFound; more than one is
// ErrIdentifierAmbiguous, never the first hit. An address that resolves to two
// accounts is not an address, and this is the layer that decides where money
// goes — the same reason settlement refuses to default a cycle's asset rather
// than settle it in the wrong money.
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
// own book balance must be zero, and so must its share of the receivable holding
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
// transaction. Customer money is a Liability; capturing (money leaving the
// customer) DEBITS the customer's position in the control account and CREDITs
// the counterparty.
//
// counterparty is a Position because it may be another customer of this bank,
// whose money is a position and not an account. A plain account of the bank's
// own is named with Total().
//
// If captureAmount is zero or negative, the hold amount is used. The hold is
// marked as Captured regardless of the amount.
//
// Returns:
//   - ErrHoldNotFound if the hold does not exist.
//   - ErrHoldNotActive if the hold has already been released or captured.
//   - ErrAccountNotFound if the deposit account no longer exists.
//   - any error from the underlying ledger posting.
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
//
// This is the method the whole store split exists for: the hold write and the
// GL posting go through one Tx, so a posting that fails leaves the hold Active
// instead of half-capturing it, and a caller composing this with its own writes
// gets all of them or none.
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
//   - Available: Book - Holds + the overdraft limit in force today, resolved
//     from the account's effective-dated terms timeline.
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

// CheckCredit reports whether the account may currently RECEIVE money. It is
// the counterpart of CheckWithdrawal and refuses only a Closed account, with
// ErrAccountClosed; see requireCreditable for why Dormant and Frozen do not.
//
// There is no amount and no funds test, because a credit cannot fail for want
// of money — the only question a credit can answer is whether this account is
// still somewhere money may land.
//
// It exists because this layer has no credit method of its own. Money reaches a
// deposit account's GL account from the layers ABOVE — a bank funding a customer,
// a settlement's creditor leg, a lending counterparty — each posting straight
// into the general ledger, which knows nothing about account status by design.
// So the check has to be callable rather than enforced from in here, exactly as
// CheckWithdrawalTx is for the other direction.
//
// Returns ErrAccountNotFound if the account does not exist.
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
// withdrawal of amount. It is status-aware: a dormant account returns
// ErrAccountDormant, a frozen account ErrAccountFrozen and a closed account
// ErrAccountClosed.
//
// The withdrawal is permitted only if Available - amount >= 0, where
// Available = Book - Holds + the limit in force today; otherwise
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
// It guards money going OUT — a withdrawal or a new hold — which is why every
// status other than Active fails it, dormancy included.
//
// Dormant names itself rather than falling through to
// ErrInvalidStatusTransition: that error is about changing a status, and a
// refused withdrawal is not changing one.
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

// requireCreditable returns an error if the account may not RECEIVE money. It is
// requireActive's counterpart, and it is deliberately far more permissive,
// because the two questions are not symmetric.
//
//   - Dormant accepts credits. An incoming payment is precisely what revives a
//     dormant account; refusing it would strand a salary run for want of a
//     customer login.
//   - Frozen accepts credits. A freeze here is a DEBIT block — the garnishment
//     and fraud-investigation case, where money owed to the customer keeps
//     arriving while they cannot take any out. A sanctions freeze does block
//     credits too, and this single status cannot express both; see the Account
//     States table in README.md, which says as much.
//   - Closed accepts nothing. Close requires a zero balance, so a credit
//     afterwards leaves a Closed account holding money that no withdrawal can
//     reach (requireActive refuses it), that closing again cannot clear (Closed
//     is terminal), and that contradicts the very invariant CloseTx enforced.
//     That is not a restriction, it is stranded money, and it is the one case
//     this function exists for.
func requireCreditable(acct Account) error {
	if acct.Status == Closed {
		return ErrAccountClosed
	}
	return nil
}

// balanceTx computes an account's three balances within a unit of work.
//
// The overdraft limit is RESOLVED from the account's terms timeline rather than
// read off the row, because it is effective-dated like the rate beside it: what
// a customer could spend last March is as much a fact about that March as what
// they were charged for it.
//
// It is the bounded as-of lookup rather than the whole timeline, because this
// runs on every withdrawal check and should not pay for history —
// ActiveHoldTotal above is a bounded aggregate for the same reason.
//
// And it needs NO catalogue lookup at all, which is the second dividend from
// pinning the limit to the account: the limit is on the account's own row for
// every day of its life, so the path that runs constantly still answers in one
// read. The reverse design — a limit that floated with the product — would put
// a product read on every withdrawal check in the system.
//
// ErrTermsNotFound is propagated rather than treated as a zero limit. Every
// account gets an opening row at OpenAccount, so the only way to miss is to ask
// about a day before the account existed, and silently reporting a spendable
// balance of Book - Holds for an account that has a facility is the kind of
// wrong answer that reads as a working system.
func (r *Register) balanceTx(ctx context.Context, tx Tx, acct Account) (Balance, error) {
	pos, err := r.positionTx(ctx, tx, acct)
	if err != nil {
		return Balance{}, err
	}
	book, err := r.gl.BookBalanceTx(ctx, tx, pos)
	if err != nil {
		return Balance{}, err
	}
	holds, err := tx.ActiveHoldTotal(ctx, r.bookID, acct.ID, r.now())
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
//
// The whole Balance is resolved as of NOW rather than as of date — the book
// balance and the holds always were, and so is the overdraft limit inside
// Available, which balanceTx reads from the terms row in force TODAY. Now that
// terms are a timeline, "the limit on day D" is a cheap question and a reader
// may reasonably expect Snapshot.Date to govern it; it does not. Resolving only
// the limit as of date would be a third answer rather than a fix, so the
// inconsistency is recorded here rather than half-closed: reconstructing a past
// day's balance belongs with checkpointing, the named successor for snapshots
// nothing reads back yet.
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
// this returns no transaction.
//
// Income is recognized daily rather than at capitalization because accrued
// interest is a real asset, and one that existed only on this record between
// charge dates would understate both assets and income on every date between.
//
// # The accrual base
//
// The overdrawn magnitude of each day's own VALUE-DATED book balance — not the
// available balance, and not today's balance applied backwards. A hold is not
// borrowed money. The base is tiered: the arranged rate up to the limit in force
// on that day, the unarranged rate beyond it. Both, and the limit, come from the
// terms row in force on the day being accrued, so a gap of several days is exact
// rather than approximate.
//
// # Idempotency, and how a backdated posting is corrected
//
// LastAccrualDate never moves backwards, so re-running an end-of-day for a date
// already covered is a no-op — and it is a no-op by arithmetic too, since the
// same date over the same history produces the same gross and a zero delta.
//
// A posting which arrives backdated is trued up by the NEXT day's run rather
// than by rewinding this one. Each run recomputes the whole of the account's
// LIFE from the value-dated balance, so the days the posting takes effect over
// are re-derived with it in place and the difference is what gets posted.
// Interest that turns out never to have been owed comes back as a correction — a
// new event, not a reversal: the original accrual was a correct statement of
// what the ledger knew then.
//
// Because the window opens at inception rather than at the last repricing, that
// holds WHEREVER the posting lands, including on days before a repricing: each
// is re-derived at the terms that were actually in force on it.
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
	// A fresh cache: one account, so there is nothing to share. RunEndOfDayTx
	// is where the type earns its place.
	return r.accrueOverdraftAccountTx(ctx, tx, acct, date, versionCache{})
}

// accrueOverdraftAccountTx is AccrueOverdraftTx against an account the caller
// has already loaded. RunEndOfDay lists every account and would otherwise read
// each one a second time.
//
// The accrual is a recomputation rather than an increment. Every run re-derives
// the whole of the account's life from its value-dated balance and posts the
// change in the rounded value — which is the same delta the incremental version
// posted, arrived at differently. The difference shows when a posting lands
// backdated: the days it takes effect over are recomputed with it in place,
// gross moves, and the delta trues up the interest that was charged on the old
// figure. No accrual is ever reversed and no date is ever rewound.
//
// The terms are resolved PER DAY from the account's timeline rather than read
// off the account, which is what lets the window reach back past a repricing
// without re-deriving an earlier day at a rate that was never in force on it.
func (r *Register) accrueOverdraftAccountTx(ctx context.Context, tx Tx, acct Account, date time.Time, cache versionCache) error {
	if acct.Status == Closed {
		return nil
	}

	// The whole timeline, in one read, resolved per day in Go below. The three
	// guards below are separate because lumping them together is how this would
	// acquire a bug:
	//
	//   - Status == Closed is unchanged, above.
	//   - TermsEffectiveFrom.IsZero() meant "no window", and there is always a
	//     window now: the opening row. It is replaced by "no terms row in force
	//     on this day", below.
	//   - Rate <= 0 cannot survive as an early return, because an early return
	//     skips the whole run and a zero rate is now a property of a DAY. An
	//     account unpriced for its first year and priced thereafter is a case
	//     the previous model could not express at all. Two things replace it:
	//     the closure returns zero for a day whose resolved rate is zero, and
	//     the run is skipped entirely when NO row carries a non-zero rate —
	//     a scan over rows already in memory, which is what keeps a
	//     never-priced account from reading a series every night.
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
	// inception. Every nightly run therefore re-derives every day the account
	// has had: O(days) per account per night, accepted deliberately at this
	// scale. The cost is arithmetic rather than I/O — the series is still one
	// query over the window — and checkpointing is the named successor.
	window := rows[0].EffectiveFrom

	// The advancement guard resolves its day count on `date`, because after
	// this change there is no single DayCount to ask: it is a terms field, and
	// the conventions genuinely disagree about whether a window advanced. Under
	// Thirty360 the 31st collapses onto the 30th, so Days(30th, 31st) is zero
	// while ACT365 says one — a run on the 31st is a no-op under one convention
	// and a real day under the other.
	//
	// `date` is exactly the right day, and for a sharper reason than "the day
	// being accrued": a span is named by its END (see the closure below), so the
	// last span this run adds is [date-1, date), which is the span NAMED date.
	// termsAt(rows, date) is therefore the very row that will price that span —
	// the guard asks its question of the same row the walk will answer it with,
	// rather than of a neighbouring day's.
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
	// A Period cannot return an error, so a resolution failure inside the walk
	// is captured and checked before anything is applied. It must abort the run
	// rather than accrue zero for the day: a tampered version and a free day are
	// not the same event, and posting the second for the first would hide it.
	var resolveErr error
	next, delta := interest.Recompute(series, window, date,
		interest.State{Accrued: acct.Accrued, Gross: acct.AccruedGross},
		func(balance ledger.Amount, from, to time.Time) interest.Accrued {
			// perDay has already cut the window to single days before any
			// Period runs, so this closure is a function of the DAY as well as
			// the balance, which is what a Period is for. The day is `to`, not
			// `from`: interest.AccrueSeries names a span by its END date,
			// because a movement value-dated V ends the preceding run at V-1
			// and so first bites on [V-1, V). That is why the pre-existing
			// TestOverdraftAccrualCorrectsABackdatedDebit calls that span
			// "day 3" for a debit value-dated day 3.
			//
			// Resolving terms on `to` is what puts the rate on the same day
			// axis as the balance it is charged against. On `from` they would
			// be a day apart — day D's rate applied to day D+1's balance — and
			// a repricing effective day 30 would not bite until day 31.
			day, err := Resolve(rows, cache, to)
			if err != nil {
				// A day before the account existed is not a failure: the window
				// opens at the opening row, so it cannot arise, and returning
				// zero is what the pre-catalogue walk did. Anything else is.
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

	// A correction can settle part of the record in cash, which moves Accrued
	// again, so it owns the write the way ChargeOverdraftInterestTx does.
	if delta < 0 {
		return r.correctOverdraftAccrualTx(ctx, tx, &acct, at, -delta, date)
	}

	if _, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: "Overdraft interest accrued: " + acct.Name,
		BookingDate: date,
		ValueDate:   date,
		Entries: []ledger.Entry{
			{AccountID: at.Receivable.Account, Subsidiary: at.Receivable.Subsidiary, Amount: delta, Direction: ledger.Debit},
			{AccountID: at.Income, Amount: delta, Direction: ledger.Credit},
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
//
// # Why the refund comes back off the record
//
// The refunded part is settled: it has left in cash and the receivable never
// held it. Leaving it in Accrued would break the invariant every caller here
// maintains — that the receivable's balance is Minor() of the record — and the
// account would carry a permanent negative that the customer gets the benefit
// of a second time, because the next interest genuinely owed is swallowed
// paying it off before a cent reaches the receivable. So Accrued is credited
// back by exactly the refund, and by exactly the refund: the absorbed part
// stays on the record, because the receivable it came out of tracks it.
//
// It takes acct by pointer for that reason, and writes the account itself —
// the same shape as ChargeOverdraftInterestTx, which also posts, moves Accrued
// by what settled, and persists. Only this function knows the split.
func (r *Register) correctOverdraftAccrualTx(ctx context.Context, tx Tx, acct *Account, at interestAccounts, amount ledger.Amount, date time.Time) error {
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

	entries := []ledger.Entry{{AccountID: at.Income, Amount: amount, Direction: ledger.Debit}}
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
// LastAccrualDate, so that it can be called once per day across the account's
// life. It is the interest.Period this product accrues by; interest.Recompute
// is what applies it a day at a time across each run of constant balance.
//
// It takes the TERMS IN FORCE ON THE DAY it is accruing rather than the
// account, because the limit and both rates are effective-dated: the day it is
// called for is the day whose terms apply, and the caller resolves them from
// the account's timeline AND its product's before each call. EffectiveTerms is
// that merge — the limit from the account always, the pricing from its overlay
// or from the product version in force — so this function never has to know
// which of the two sources priced the day.
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
// reused for every account after it.
//
// A book of ten thousand accounts on three products therefore does three reads
// for the whole run rather than ten thousand. It is passed in rather than held
// on the Register because a Register owns no state — the same reason its clock
// is a function and not a time.
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
	// derived over a balance that already includes the charge. That is interest
	// on interest earned the same day. lending does the same at its own
	// capitalisation, and it is sub-minor per cycle, so it is recorded here
	// rather than corrected: value-dating to the NEXT day would be the fix.
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
//
// It does not capitalize. Charging is a monthly event on its own cycle, and
// which day of the month is a product decision this layer has no opinion about;
// a caller runs ChargeOverdraftInterest when its calendar says to.
//
// Accounts never priced at all, accounts in credit and closed accounts are
// skipped rather than errored — over a real portfolio most accounts are all
// three. "Never priced" is a property of the whole timeline, not of a column:
// an account priced only from next month still has a run tonight, and it
// accrues nothing because the day it is accruing carries a zero rate.
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
	// One balance per account and not one per control line, because the split is
	// by the SIGN of each customer's own balance and the pool has one sign. Its
	// balance is what the two figures net to, which is the number this type
	// exists to take apart.
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
