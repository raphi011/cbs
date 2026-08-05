package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// BankStatus is how far through admission a bank is.
//
// There are two values and there is no third. An Applied state between them
// would record that a request is out and nothing would read it — the
// field-nothing-reads this sub-project has already refused twice. What "the
// request is out" is worth knowing for is a stuck admission, which needs a
// reconciliation that walks both ends rather than a column on one of them.
type BankStatus string

const (
	// BankFounded is a bank with a licence, a book and a chart of accounts, and
	// no place in a scheme. It can open customer accounts; it cannot pay or be
	// paid, because no clearing house routes to it and no settlement agent holds
	// an account for it.
	//
	// It is a legitimate state and not a broken one — a bank exists before it
	// joins a scheme — and it is what an interrupted admission leaves behind.
	// Nothing writes it yet: today's AddParticipantTx admits a bank to the scheme
	// in the same unit of work that founds it, so every bank it writes is a
	// Member. Task 17d is what splits founding from joining and starts a bank
	// here.
	BankFounded BankStatus = "Founded"

	// BankMember is a bank the scheme has admitted: the settlement agent holds an
	// account for it and the clearing house routes to it.
	BankMember BankStatus = "Member"
)

// BankAccounts are the internal accounts a bank needs for one asset:
//
//   - Suspense (Liability): an in-transit account holding funds that have left
//     a customer but not yet settled between banks. Returns to zero once this
//     bank has booked BOTH its halves of a cut-off — the mirror leg from the
//     central bank's camt.053 and its creditor legs from the clearing house's
//     per-payment advices — which is AFTER settlement, not at it.
//   - Reserve (Asset): the bank's claim on the central bank. It mirrors the
//     bank's reserve account in the central-bank ledger, and it moves when this
//     bank books the statement it is sent — so the two sides are equal only once
//     it has, and the interval between is the unreconciled position.
//   - Unclaimed (Liability): where a credit goes when the payee's account will
//     not take it. Money the bank owes somebody it has not yet identified.
//   - ReturnsReceivable (Asset): a claim on a biller, opened when this bank
//     honours a refund it cannot fund out of the biller's account. The
//     opposite class from Unclaimed, on purpose: that one is owed BY the bank
//     to somebody it hasn't identified, this is owed TO the bank by somebody
//     it has.
//   - Settlement: this bank's reserve account in the central-bank ledger.
//
// The first four are accounts in this bank's own book. The fifth is an account
// in ANOTHER institution's book, and it is here as the account holder's record
// of its own account number — the way a customer knows their IBAN without
// holding the bank's ledger. What it is not, any more, is the only record of
// that account: the central bank now keeps its own SettlementMember row, which
// is the one a settlement agent asks.
//
// Both records exist and the readers have not moved yet. SettleCycleTx,
// SettleReturnTx and ReserveBalance still resolve the account through this
// field, and Task 17c and Task 17e are what point them at the central bank's
// own row instead. DepositTx is the one reader that stays here afterwards,
// because it is the account holder quoting its own account number.
type BankAccounts struct {
	Suspense ledger.AccountID
	Reserve  ledger.AccountID

	// Unclaimed is where a credit goes when the payee's account will not take
	// it — closed, and therefore terminal.
	//
	// A real bank has one. Money that arrives for an account that cannot receive
	// it does not vanish and does not sit in the payee's closed account: it is
	// held as a liability to whoever eventually claims it, and the bank has a
	// process for finding them. This system had nowhere for it to go, which is
	// why the gap at the return and at the cut-off was a ruling rather than a
	// line of code. Both are closed now: PostCreditorLegTx diverts a credit the
	// payee cannot take, and PostReturnLegTx diverts a refund the payer cannot
	// take.
	Unclaimed ledger.AccountID

	// ReturnsReceivable is a claim on a biller: money the bank paid out to
	// honour a refund (a SEPA direct debit's unconditional eight-week right,
	// today) when the biller's account could not fund it. An Asset — the bank
	// is owed this, by someone it has identified perfectly well — and the
	// mirror image of Unclaimed, which is owed BY the bank to someone it has
	// not identified.
	//
	// PostReturnLegTx is what posts to it, and only on a pull, and only when the
	// biller's account is CLOSED. A biller who has spent the money but still has
	// an account is simply overdrawn by it — the ledger does not refuse a
	// Liability going negative, and an overdrawn biller is a debt the bank
	// collects from a customer it still has. A closed account is the case with
	// nowhere on the account to put the debit, and it is the only one this
	// account is reached for.
	ReturnsReceivable ledger.AccountID

	Settlement ledger.AccountID
}

// Bank is one bank's own record of itself: its book, its chart of accounts, the
// product it opens customer accounts from, and how far through admission it is.
//
// It is the first of the three rows admission writes, and the only one this
// institution owns. The central bank writes a SettlementMember for the account
// it opens, and the clearing house writes a RosterEntry for the address it
// routes to; neither of those carries a bank id, because the identifier BETWEEN
// institutions is the BIC and only the BIC.
//
// Each bank keeps its OWN book of accounts (Ledger) and a deposit layer
// (Deposit) over it. Banks only meet at the central bank, where each holds a
// reserve account. This mirrors reality and is what makes the distinction
// between clearing (exchanging instructions) and settlement (moving
// central-bank reserves) concrete.
//
// Today every book still lives in ONE store, told apart by BookID, which is
// what still lets a single unit of work span several banks — and chart-of-
// accounts numbers, ID counters and idempotency keys are per book, so each bank
// already numbers its accounts independently. Task 18 is what removes the
// spanning: each entity gets its own store, and a BookID that is not this
// bank's stops being a lookup and becomes a refusal. Until then nothing but the
// recorder in mesh/books_test.go notices a handler that reaches across.
//
// The internal accounts each bank needs:
//
//   - Customer deposit accounts: demand-deposit accounts managed by the
//     Deposit register, each backed by a Liability GL account. Opened via
//     OpenCustomerAccount.
//   - Clearing Suspense (Liability): an in-transit account holding funds
//     that have left a customer but not yet settled between banks. It
//     returns to zero once this bank has booked both its halves of a cut-off,
//     which is after settlement rather than at it — see BankAccounts.
//   - Reserve at Central Bank (Asset): the bank's claim on the central bank.
//     It mirrors the bank's reserve account in the central-bank ledger and
//     moves when this bank books the camt.053 the central bank sends it.
//   - Unclaimed Balances (Liability): where a credit goes when the payee's
//     account cannot receive it. The bank still owes the money — to whoever
//     eventually claims it — so it is a liability like a deposit, and holding
//     it here is what lets one payment fail without stranding it.
//   - Returns Receivable (Asset): a claim on a biller, opened when this bank
//     honours a refund it cannot fund out of the biller's account. The
//     opposite class from Unclaimed Balances above, on purpose: that one is
//     money the bank owes to somebody it hasn't identified, this is money
//     owed TO the bank by somebody it has.
//
// The last four, plus the bank's reserve account in the central-bank ledger,
// exist once per asset the bank operates in — see Assets.
type Bank struct {
	ID   ParticipantID
	Name string

	// BIC is this bank's ISO 9362 business identifier code: what a
	// counterparty addresses it by, and what the mesh routes on.
	//
	// It is here rather than derived from ID or Name because a BIC is issued
	// by SWIFT, not chosen: "AURODEFFXXX" and "Aurora Bank" have no
	// computable relationship, and inventing one would teach that they do.
	//
	// Validated with iso20022.BIC on the way in. Structure only — this
	// repository has no directory to check membership against, exactly as it
	// has no register to check an IBAN's issuer against.
	//
	// It is also the only field of this row the other two institutions hold: a
	// SettlementMember and a RosterEntry are both keyed by it.
	BIC iso20022.BIC

	// BookID is this bank's book within the network's store. It is
	// ledger.BookID(ID) — the bank is its own book.
	BookID ledger.BookID

	CustomerSubledger ledger.SubledgerID

	// ProductID is the catalogue entry OpenCustomerAccount opens accounts from,
	// configured per bank exactly as CustomerSubledger is. A bank's onboarding
	// has to name a product, because every account is opened from one — there is
	// no such thing as an unpriced deposit account.
	ProductID product.ID

	// Status is how far through admission this bank is. See BankStatus: a
	// Founded bank can open customer accounts and cannot pay or be paid.
	//
	// The zero value is neither, which is why every writer sets it explicitly
	// and storetest asserts it survives the round trip: a bank read back with
	// Status "" is not a bank that has not applied, it is a bank whose store
	// dropped a column.
	Status BankStatus

	// Assets holds one set of internal accounts per asset the bank operates in,
	// keyed by asset code.
	//
	// Keyed rather than flat because every one of those accounts is
	// denominated in exactly one asset: a bank clearing both euro and dollar
	// schemes needs two suspense accounts and two reserve accounts, not two
	// currencies inside one. Adding a scheme in a new asset is then a data
	// change rather than a code change.
	Assets map[ledger.AssetCode]BankAccounts

	CreatedAt time.Time

	// Ledger, Deposit and Lending are live handles bound to BookID over the
	// network's store. They are NOT data and are not persisted: a Store
	// returns a Bank with all three nil, and the Network binds them on the way
	// out (see Network.bind). Treat them as derived, exactly like a database
	// row's association objects.
	//
	// They are the reason this row is a bank's own and is handed to nobody
	// else. A value of this type carries the ability to read and write the
	// named bank's ledger, so a lookup that returned one to another institution
	// would hand that ability over with it — which is what mesh/ops.go's
	// GetBank did until this row was split out of it.
	//
	// json:"-" for a second reason: the participant.added audit payload is a
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

// AccountsFor returns the bank's internal accounts for an asset.
//
// Returns ErrParticipantAssetNotFound if the bank does not operate in that
// asset. There is deliberately no fallback to a base currency: settling a
// dollar cycle through a euro reserve account would be a silent accounting
// error rather than a visible failure.
func (b *Bank) AccountsFor(asset ledger.AssetCode) (BankAccounts, error) {
	accts, ok := b.Assets[asset]
	if !ok {
		return BankAccounts{}, fmt.Errorf("%w: %s in %s", ErrParticipantAssetNotFound, asset, b.Name)
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
// The account is opened from the bank's configured ProductID, and is FLOATING:
// its price is the product's, so a later published version reprices it with no
// write to the account at all.
//
// This opens its own unit of work, so it must not be called from inside one.
// A caller already holding a Tx should drive b.Deposit.OpenAccountTx instead —
// which is also how to open an account with an overdraft limit, or from a
// product other than the bank's default.
func (b *Bank) OpenCustomerAccount(ctx context.Context, name string, asset ledger.AssetCode) (deposit.Account, error) {
	return b.Deposit.OpenAccount(ctx, b.CustomerSubledger, name, asset, b.ProductID, 0)
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
func (b *Bank) RunEndOfDay(ctx context.Context, date time.Time) error {
	return b.Deposit.Store().Update(ctx, func(ctx context.Context, tx deposit.Tx) error {
		if err := b.Deposit.RunEndOfDayTx(ctx, tx, date); err != nil {
			return err
		}
		lendingTx, ok := tx.(lending.Tx)
		if !ok {
			return fmt.Errorf("payment: store transaction does not span the lending layer")
		}
		return b.Lending.RunEndOfDayTx(ctx, lendingTx, date)
	})
}

// glAccountTx resolves a customer deposit account ID to the backing GL account
// ID used for ledger postings, within a caller-supplied unit of work. It returns
// ErrAccountNotInParticipant if the deposit account does not exist at this bank.
func (b *Bank) glAccountTx(ctx context.Context, tx Tx, id deposit.AccountID) (ledger.AccountID, error) {
	acct, err := tx.GetDepositAccount(ctx, b.BookID, id)
	if err != nil {
		return "", ErrAccountNotInParticipant
	}
	return acct.GLAccount, nil
}
