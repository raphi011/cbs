package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// BankStatus is how far through admission a bank is.
//
// There are two values and there is no third. An Applied state between them
// would record that a request is out and nothing would read it, which is the
// field-nothing-reads shape this repository refuses. What "the
// request is out" is worth knowing for is a stuck admission, which needs a
// reconciliation that walks both ends rather than a column on one of them.
//
// That walk is payment/recon: an admission writes three rows in three
// databases, each institution can see exactly one of them, and the harness is
// what holds the three against each other. A bank calling itself a Member that
// no settlement agent holds an account for is what a half-happened admission
// looks like from outside all three, and it is not visible from inside any.
type BankStatus string

const (
	// BankFounded is a bank with a licence, a book, a chart of accounts and a
	// product, and no place in a scheme. It has applied to a national registry
	// for the bank code its customers' addresses would carry and has not been
	// answered, so it can open no customer account at all: every deposit account
	// here is opened with an address, an address is minted under a bank code, and
	// this bank has no range to give one out of (deposit.ErrNoIssuer).
	//
	// That is what a bank between its licence and its allocation really is, and
	// it is stricter than it sounds only because the two arrive together here —
	// one message asks a settlement agent for a reserve account and a registry for
	// a code, and one answer carries both. So a founded bank cannot lodge cash
	// either, and would have none to lodge: no settlement agent holds an account
	// for it (LodgeReservesTx, ErrSettlementMemberNotFound) and it has no
	// customer who could have paid any in.
	//
	// It is in no routing directory either, so nothing it takes part in can
	// settle — what a transport does and does not enforce about that is measured
	// in mesh/doc.go's admission section rather than asserted here.
	//
	// It is a legitimate state and not a broken one — a bank exists before it
	// joins a scheme — and it is what an interrupted admission leaves behind.
	//
	// FoundBankTx is what writes it, and it commits before the other three acts
	// run. So this state exists on every admission and not only on an interrupted
	// one — which is the whole difference between founding and joining being one
	// commit and being four.
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
//     not take it. Money the bank owes the holder of an account that can no
//     longer receive it, and the one of these accounts that pools holders —
//     each leg names whose the money is.
//   - ReturnsReceivable (Asset): a claim on a biller, opened when this bank
//     honours a refund it cannot fund out of the biller's account. The
//     opposite class from Unclaimed, on purpose: that one is owed BY the bank,
//     this is owed TO it.
//   - VaultCash (Asset): the cash the bank is holding. The only account here
//     that is not somebody's promise, and where money paid in over the counter
//     lands.
//   - Settlement: this bank's reserve account in the central-bank ledger.
//
// The first five are accounts in this bank's own book. The sixth is an account
// in ANOTHER institution's book, and it is here as the account holder's record
// of its own account number — the way a customer knows their IBAN without
// holding the bank's ledger. What it is not is the only record of that account:
// the central bank keeps a SettlementMember row of its own, and that is the one
// the settlement agent reads.
//
// Which reader reads which is decided by whose question it is, and the readers
// split two ways. SettleCycleTx, SettleReturnTx and ReserveBalance read the
// central bank's row: those are the settlement agent posting in its own book, and
// the operator console asking it about that book. LodgeReservesTx and
// PostSettlementAdviceTx read THIS field, because both are the account holder
// using its own account number — one quoting it to ask for a reserve credit, the
// other checking that an arriving statement is about the account it holds and not
// another member's.
//
// Taking cash in is one institution's act in one book — see VaultCash — so it
// names no account in anybody else's. What quotes this number is the LODGEMENT
// that moves the cash onward, and that is a message rather than a posting.
//
// It is empty on a founded bank. The number is something the bank has to be
// told, and RecordMembershipTx is where being told lands.
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
	// line of code. Both are closed now: SettleAtBankTx diverts a credit the
	// payee cannot take, and PostReturnLegTx diverts a refund the payer cannot
	// take.
	//
	// It is a CONTROL account, and the only one among this bank's own. Each leg
	// names the deposit account the money was meant for, which is known at every
	// site that posts here — the account is closed, not absent. So the balance
	// answers "what does the bank owe THIS holder" and not only "what is
	// unclaimed", which is the question a release has to answer before it pays
	// anybody.
	Unclaimed ledger.AccountID

	// ReturnsReceivable is a claim on a biller: money the bank paid out to
	// honour a refund (a SEPA direct debit's unconditional eight-week right,
	// today) when the biller's account could not fund it. An Asset — the bank
	// is owed this, by someone it has identified perfectly well — and the
	// mirror image of Unclaimed, which is owed BY the bank.
	//
	// PostReturnLegTx is what posts to it, and only on a pull, and only when the
	// biller's account is CLOSED. A biller who has spent the money but still has
	// an account is simply overdrawn by it — the ledger does not refuse a
	// Liability going negative, and an overdrawn biller is a debt the bank
	// collects from a customer it still has. A closed account is the case with
	// nowhere on the account to put the debit, and it is the only one this
	// account is reached for.
	ReturnsReceivable ledger.AccountID

	// VaultCash is the cash this bank is holding: notes in a drawer, and the
	// asset side of a deposit paid in over the counter.
	//
	// An Asset, and the only account on a bank's chart that is nobody else's
	// promise. A reserve is a claim on the central bank, a suspense balance is
	// money owed to a counterparty's customer, Unclaimed is owed to a holder who
	// cannot be paid and ReturnsReceivable is owed by a biller. This is money,
	// held.
	//
	// # It is why taking cash in reaches no other institution
	//
	// It exists from FoundBankTx, per asset, alongside the other four — before any
	// settlement account is opened and independently of whether one ever is. Cash
	// paid in over the counter lands here, so a deposit is refused for want of a
	// settlement reference by nothing at all.
	//
	// Cash paid in debits this and credits the customer, in this book and nowhere
	// else. Nothing about it involves the central bank, because nothing about a
	// customer handing over notes does: the bank holds that money as cash until it
	// chooses to do something else with it. mesh's
	// TestTakingCashInReachesNoOtherInstitution is what measures the absence.
	//
	// # What moves it onward is a conversation
	//
	// LodgeReservesTx is the something else: Debit Reserve at Central Bank, Credit
	// this — the bank swapping cash for a claim on the central bank — matched by
	// the central bank crediting the bank's reserve account in ITS book, from a
	// camt.050 this bank sends. Two books, two units of work, and a message
	// between them instead of one bank writing in the other's ledger.
	//
	// The balance here is therefore real and meaningful rather than a way-station:
	// it is how much of what this bank's customers paid in has not been placed on
	// reserve. A bank that takes deposits and never lodges accumulates vault cash
	// and cannot settle with it, which is the true consequence and the one the
	// seed avoids by lodging after it funds.
	VaultCash ledger.AccountID

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
// Every book is in its OWN DATABASE, so no unit of work can span two banks and a
// BookID that is not this bank's is a refusal rather than a lookup
// (sqlite.ErrNotThisStoresBook). Chart-of-accounts numbers, ID counters and
// idempotency keys are per book because a book is a database.
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

	// Issuer is the country and bank code this bank mints its customers'
	// addresses under, and it is a SECOND identifier that has nothing to do with
	// the BIC above.
	//
	// The two answer different questions and neither computes the other. A BIC
	// is what a MESSAGE is addressed to; a bank code is what an ACCOUNT's
	// address carries, allocated by a national registry, and unique only within
	// its country. Aurora is AURODEFFXXX and 99900001, and there is no
	// arithmetic between them — which is the whole reason a scheme has to
	// publish a directory rather than letting every bank derive.
	//
	// It reaches deposit.NewRegister as this bank's Issuer and is what stops one
	// bank opening an account at another bank's address: a register can only
	// mint under the code it was given.
	Issuer iban.Issuer

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
	// Founded bank can open customer accounts and take cash in, and cannot lodge
	// that cash on reserve.
	//
	// The zero value is neither, which is why every writer sets it explicitly
	// and storetest asserts it survives the round trip: a bank read back with
	// Status "" is not a bank that has not applied, it is a bank whose store
	// dropped a column.
	Status BankStatus

	// AdmissionRef is the reference of the admission this bank recorded a
	// membership under: what it accepted, not what anybody else says about it.
	//
	// # It is the only thing that can refuse a second admission's acknowledgement
	//
	// RecordMembershipTx records-or-extends, because one request asks for one
	// currency and a bank joining in two assets is answered twice. What that
	// gives up, without this field, is any way to tell the second answer of THIS
	// admission from an acknowledgement belonging to another one — and the two
	// are not the same thing at all: the first adds a settlement reference the
	// bank did not have, and the second overwrites one it did.
	//
	// It was measured rather than reasoned about. Without this field, an
	// acknowledgement naming this bank's own BIC, quoting an admission reference
	// it had never heard of and carrying an invented account, moved a Member
	// bank's euro settlement reference from the central bank's real account to
	// the forged one — leaving the bank's own row disagreeing with the settlement
	// agent's about which account it holds, and DepositTx reads the bank's.
	//
	// The argument this replaces said a bank has no contender, because the
	// acknowledgement has already been checked to name this bank's own BIC. That
	// check answers WHICH BANK and not WHICH ADMISSION, and RosterEntry.AdmissionRef
	// exists, in roster.go, precisely because two admissions can quote one BIC.
	//
	// # It is not a duplicate of the roster's
	//
	// The clearing house's is a registry's: it decides between two institutions
	// contending for an address, in a row that belongs to a different institution
	// and a different database. This one is the bank's own record of what it itself
	// accepted, and comparing against it needs nobody else's store. That the two
	// agree in a healthy system is a consequence rather than
	//
	// Empty on a Founded bank, because nothing has been accepted yet.
	// RecordMembershipTx is the only writer.
	AdmissionRef string

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
	// They are the reason this row is a bank's own, and the reason handing one
	// over is a crossing rather than a lookup: a value of this type carries the
	// ability to read and write the named bank's ledger, so whoever holds it can
	// reach that bank's book through a method they legitimately have.
	//
	// Network.GetBank still returns one, still bound, and NOT all of its callers
	// are the bank itself. Five call it, and they are three different readers:
	//
	//   - api/server.go, the bank bound to the listener, reading its own row, and
	//     api/handlers_participant.go, the operator console reading a bank's
	//     reserves. Neither is another institution.
	//   - api/handlers_directory.go, on a BANK's port, resolving an address in that
	//     bank's OWN register. It reaches no other bank's book.
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
// layer; their money pools in one Liability control account per asset, since it
// belongs to the customers and the bank owes it to them. The account is opened
// with no overdraft.
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
	return b.Deposit.OpenAccount(ctx, name, asset, b.ProductID, 0)
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

// positionTx resolves a customer deposit account ID to the position its money
// occupies in this bank's ledger — the customer-deposit control account for its
// asset, under the account's own id — within a caller-supplied unit of work. It
// returns ErrAccountNotInParticipant if the deposit account does not exist at
// this bank, and if the bank holds no control line for its asset, which is a
// chart of accounts this bank cannot post the account's money into either way.
func (b *Bank) positionTx(ctx context.Context, tx Tx, id deposit.AccountID) (ledger.Position, error) {
	pos, err := b.Deposit.PositionTx(ctx, tx, id)
	if err != nil {
		return ledger.Position{}, ErrAccountNotInParticipant
	}
	return pos, nil
}
