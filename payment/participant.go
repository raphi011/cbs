package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// ParticipantAccounts are the internal accounts a participant needs for one
// asset:
//
//   - Suspense (Liability): an in-transit account holding funds that have left
//     a customer but not yet settled between banks. Returns to zero once a
//     cycle settles.
//   - Reserve (Asset): the bank's claim on the central bank. It mirrors the
//     bank's reserve account in the central-bank ledger and moves only at
//     settlement.
//   - Settlement: this participant's reserve account in the central-bank
//     ledger — the central bank's "vostro" view of the bank.
type ParticipantAccounts struct {
	Suspense   ledger.AccountID
	Reserve    ledger.AccountID
	Settlement ledger.AccountID
}

// Participant is a bank (or payment service provider) that takes part in the
// clearing and settlement system.
//
// Each participant keeps its OWN book of accounts (Ledger) and a deposit layer
// (Deposit) over it. Banks only meet at the central bank, where each holds a
// reserve account. This mirrors reality and is what makes the distinction
// between clearing (exchanging instructions) and settlement (moving
// central-bank reserves) concrete.
//
// The books all live in the network's single store, told apart by BookID —
// which is what lets one transaction span several banks — but chart-of-accounts
// numbers, ID counters and idempotency keys are per book, so each bank still
// numbers its accounts independently.
//
// The internal accounts each participant needs:
//
//   - Customer deposit accounts: demand-deposit accounts managed by the
//     Deposit register, each backed by a Liability GL account. Opened via
//     OpenCustomerAccount.
//   - Clearing Suspense (Liability): an in-transit account holding funds
//     that have left a customer but not yet settled between banks. It
//     returns to zero once a cycle settles.
//   - Reserve at Central Bank (Asset): the bank's claim on the central bank.
//     It mirrors the bank's reserve account in the central-bank ledger and
//     moves only at settlement.
//
// The last two, plus the bank's reserve account in the central-bank ledger,
// exist once per asset the bank operates in — see Assets.
type Participant struct {
	ID   ParticipantID
	Name string

	// BookID is this bank's book within the network's store. It is
	// ledger.BookID(ID) — the participant is its own book.
	BookID ledger.BookID

	CustomerSubledger ledger.SubledgerID

	// ProductID is the catalogue entry OpenCustomerAccount opens accounts from,
	// configured per participant exactly as CustomerSubledger is. A bank's
	// onboarding has to name a product, because every account is opened from
	// one — there is no such thing as an unpriced deposit account.
	ProductID product.ID

	// Assets holds one set of internal accounts per asset the participant
	// operates in, keyed by asset code.
	//
	// Keyed rather than flat because every one of those accounts is
	// denominated in exactly one asset: a bank clearing both euro and dollar
	// schemes needs two suspense accounts and two reserve accounts, not two
	// currencies inside one. Adding a scheme in a new asset is then a data
	// change rather than a code change.
	Assets map[ledger.AssetCode]ParticipantAccounts

	CreatedAt time.Time

	// Ledger, Deposit and Lending are live handles bound to BookID over the
	// network's store. They are NOT data and are not persisted: a Store
	// returns a Participant with all three nil, and the Network binds them on
	// the way out (see Network.bind). Treat them as derived, exactly like a
	// database row's association objects.
	//
	// json:"-" for the same reason: the participant.added audit payload is a
	// snapshot of the stored row, and a handle is neither data nor meaningful
	// once serialized.
	Ledger  *ledger.Book      `json:"-"`
	Deposit *deposit.Register `json:"-"`

	// Lending is a live handle like Ledger and Deposit, bound by Network.bind.
	//
	// It manages this bank's term loans and revolving lines. It does NOT
	// manage arranged overdrafts: those are priced on the deposit account
	// itself, because an overdrawn account's drawn amount is its own negative
	// balance viewed by sign and has no independent existence. See the
	// lending package doc.
	Lending *lending.Portfolio `json:"-"`

	// Catalogue is a live handle like Ledger, Deposit and Lending, bound by
	// Network.bind. It is this bank's product catalogue: products are
	// book-scoped, so the same ID in two banks is two products.
	Catalogue *product.Catalogue `json:"-"`
}

// AccountsFor returns the participant's internal accounts for an asset.
//
// Returns ErrParticipantAssetNotFound if the participant does not operate in
// that asset. There is deliberately no fallback to a base currency: settling a
// dollar cycle through a euro reserve account would be a silent accounting
// error rather than a visible failure.
func (p *Participant) AccountsFor(asset ledger.AssetCode) (ParticipantAccounts, error) {
	accts, ok := p.Assets[asset]
	if !ok {
		return ParticipantAccounts{}, fmt.Errorf("%w: %s in %s", ErrParticipantAssetNotFound, asset, p.Name)
	}
	return accts, nil
}

// OpenCustomerAccount opens a new customer deposit account at the bank,
// denominated in asset.
//
// Customer deposits are demand-deposit accounts managed by the bank's deposit
// layer; each is backed by a Liability GL account, since the money belongs to
// the customer and the bank owes it to them. The account is opened with no
// overdraft.
//
// The account is opened from the participant's configured ProductID, and is
// FLOATING: its price is the product's, so a later published version reprices
// it with no write to the account at all.
//
// This opens its own unit of work, so it must not be called from inside one.
// A caller already holding a Tx should drive p.Deposit.OpenAccountTx instead —
// which is also how to open an account with an overdraft limit, or from a
// product other than the participant's default.
func (p *Participant) OpenCustomerAccount(ctx context.Context, name string, asset ledger.AssetCode) (deposit.Account, error) {
	return p.Deposit.OpenAccount(ctx, p.CustomerSubledger, name, asset, p.ProductID, 0)
}

// RunEndOfDay runs this bank's end-of-day batches for one business date: the
// deposit layer accrues overdraft interest, the lending layer accrues facility
// interest and recomputes arrears.
//
// There are two batches because the two layers own different products and
// neither imports the other. They run in ONE unit of work, so a failure halfway
// cannot leave a bank with a day of interest on its loans and none on its
// overdrafts. This method is what the API exposes, so a caller cannot run one
// batch without the other by accident.
//
// It does not charge or capitalize interest. Both are monthly events on their
// own calendars.
func (p *Participant) RunEndOfDay(ctx context.Context, date time.Time) error {
	return p.Deposit.Store().Update(ctx, func(ctx context.Context, tx deposit.Tx) error {
		if err := p.Deposit.RunEndOfDayTx(ctx, tx, date); err != nil {
			return err
		}
		lendingTx, ok := tx.(lending.Tx)
		if !ok {
			return fmt.Errorf("payment: store transaction does not span the lending layer")
		}
		return p.Lending.RunEndOfDayTx(ctx, lendingTx, date)
	})
}

// glAccountTx resolves a customer deposit account ID to the backing GL account
// ID used for ledger postings, within a caller-supplied unit of work. It returns
// ErrAccountNotInParticipant if the deposit account does not exist at this
// participant.
func (p *Participant) glAccountTx(ctx context.Context, tx Tx, id deposit.AccountID) (ledger.AccountID, error) {
	acct, err := tx.GetDepositAccount(ctx, p.BookID, id)
	if err != nil {
		return "", ErrAccountNotInParticipant
	}
	return acct.GLAccount, nil
}
