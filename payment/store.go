package payment

import (
	"context"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// Store owns the payment layer's persistent state. Like ledger.Store and
// deposit.Store it is declared here, by the consumer, and implemented by
// store/mem and store/pg — so the store packages import the domain packages and
// never the reverse.
//
// It is the only store handle the Network takes. The narrower ledger.Store and
// deposit.Store views the Network needs for its books and registers are derived
// from it (see ledgerView and depositView), which is what makes it impossible to
// hand the network two different stores and silently split the layers apart.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds deposit.Tx — and, through it, ledger.Tx — plus lending.Tx, so one
// concrete transaction spans every layer a participant drives. That is what
// lets Bank.RunEndOfDay accrue an overdraft and a loan in a single unit
// of work: two batches, one commit, so a failure halfway cannot leave a bank
// with a day of interest on its loans and none on its overdrafts.
//
// Network-scoped entities — banks, payments, mandates, cycles, settlements —
// belong to no single bank and are stored under ledger.NetworkBook.
type Tx interface {
	deposit.Tx
	lending.Tx

	// The three rows admission writes, one per institution that acts in it.
	//
	// They are three rows and not one because each has exactly one writer and
	// each will live in a different store at Task 18: the bank's own record of
	// itself, the settlement agent's record of the account it opened, the
	// clearing house's record of where to send a message. What made that split
	// necessary is that the settlement agent used to have no record of its own
	// members at all — it read the account it was to post to off the clearing
	// house's row, which is a read no isolated institution could make.
	//
	// The two BIC-keyed rows are keyed that way because the BIC is the only
	// identifier that crosses an institutional boundary in this system. Neither
	// carries a ParticipantID: an id the network allocates is not something a
	// message ever tells anybody.
	PutBank(ctx context.Context, b Bank) error
	GetBank(ctx context.Context, id ParticipantID) (Bank, error)
	ListBanks(ctx context.Context) ([]Bank, error)

	PutSettlementMember(ctx context.Context, m SettlementMember) error
	GetSettlementMember(ctx context.Context, bic iso20022.BIC) (SettlementMember, error)
	ListSettlementMembers(ctx context.Context) ([]SettlementMember, error)

	// ListRosterEntries and ListSettlementMembers have no production caller
	// today; GetRosterEntry does — Network.GetRosterEntry, which is what the
	// mesh's handlers now ask instead of being handed a whole bank. The two
	// listings are declared with their siblings for the reason
	// ListSettlementAdvices below was: the rows are written now, and adding a
	// listing later would be a second occasion to get the ordering contract
	// wrong in two stores independently. storetest holds mem and pg to one
	// answer for them in the meantime.
	PutRosterEntry(ctx context.Context, e RosterEntry) error
	GetRosterEntry(ctx context.Context, bic iso20022.BIC) (RosterEntry, error)
	ListRosterEntries(ctx context.Context) ([]RosterEntry, error)

	PutPayment(ctx context.Context, p Payment) error
	GetPayment(ctx context.Context, id PaymentID) (Payment, error)
	GetPaymentByEndToEndID(ctx context.Context, endToEndID string) (Payment, error)
	ListPayments(ctx context.Context) ([]Payment, error)

	PutMandate(ctx context.Context, m Mandate) error
	GetMandate(ctx context.Context, id MandateID) (Mandate, error)
	ListMandates(ctx context.Context) ([]Mandate, error)

	PutCycle(ctx context.Context, c ClearingCycle) error
	GetCycle(ctx context.Context, id CycleID) (ClearingCycle, error)
	// GetOpenCycle returns the single open cycle for a scheme, or the existing
	// ErrCycleNotFound. Replaces the openCycle map on Network.
	GetOpenCycle(ctx context.Context, scheme SchemeID) (ClearingCycle, error)
	ListCycles(ctx context.Context) ([]ClearingCycle, error)

	PutSettlement(ctx context.Context, s Settlement) error
	GetSettlement(ctx context.Context, id SettlementID) (Settlement, error)
	ListSettlements(ctx context.Context) ([]Settlement, error)

	// The advice rows are BOOK-SCOPED, unlike every other method in this block.
	// Banks, payments, mandates, cycles and settlements belong to no
	// single bank and live under ledger.NetworkBook; an advice is one member
	// bank's record of what it was told, so it is keyed by that bank's book —
	// which is also what makes the recorder in mesh/books_test.go see a bank
	// reaching its own book when it books a settlement.
	//
	// ListSettlementAdvices has NO production caller — the only method in this
	// interface with none. It is Task 19's scaffolding, on the same footing as
	// SettlementAdvice.ClosingBalance, which no code reads either: a
	// reconciliation walks a bank's own advices against a clearing suspense that
	// has not returned to zero, and that walk is the reader. It is declared now
	// because the rows are written now, and a listing added later would be a
	// second occasion to get the ordering contract wrong in two stores.
	// storetest's SettlementAdviceIsScopedToTheBankThatWasAdvised is what holds
	// mem and pg to one answer in the meantime.
	PutSettlementAdvice(ctx context.Context, book ledger.BookID, a SettlementAdvice) error
	GetSettlementAdvice(ctx context.Context, book ledger.BookID, reference string, asset ledger.AssetCode) (SettlementAdvice, error)
	ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]SettlementAdvice, error)
}

// Contract notes for implementers. Each of these is asserted by
// storetest.RunPayment; the named subtest is what pins it.
//
//   - Not-found sentinels. GetBank -> ErrParticipantNotFound, GetPayment
//     and GetPaymentByEndToEndID -> ErrPaymentNotFound, GetMandate ->
//     ErrMandateNotFound, GetCycle and GetOpenCycle -> ErrCycleNotFound,
//     GetSettlement -> ErrSettlementNotFound. errors.Is is used, so wrapping is
//     fine. (GetOnMissingPaymentRowsReturnsSentinels.)
//
//     The three admission rows have a sentinel each and not one between them:
//     GetSettlementMember -> ErrSettlementMemberNotFound
//     (SettlementMemberIsKeyedByBIC), GetRosterEntry ->
//     ErrRosterEntryNotFound (RosterEntryCarriesNoAccountIdentifiers). A bank
//     the settlement agent holds no account for and a bank the clearing house
//     does not route to are different institutions' answers, and a founded bank
//     is legitimately both.
//
//   - Bank.Ledger and Bank.Deposit are NOT persisted. They are
//     live handles over the store, not data; store/pg has no column to put them
//     in, so store/mem must not keep them either — otherwise code that works in
//     memory breaks on Postgres. The Network rebinds them on the way out.
//     Bank.Status IS data and must survive: a bank read back with Status ""
//     is neither Founded nor a Member.
//     (BankRoundTripsAndDropsLiveHandles.)
//
//   - The BIC-keyed rows are keyed by BIC in both stores, and their collections
//     replace rather than merge on an upsert — SettlementMember.Accounts and
//     RosterEntry.Assets both, for the reason Bank.Assets does: a stale entry
//     is an account or an asset the institution has given up and would still be
//     acted on. (SettlementMemberKeepsOneAccountPerAsset.)
//
//   - Put* are upserts on the primary key, which is how a status change is
//     written, and they deep-copy: a caller that mutates the slice or map it
//     passed in must not change the stored row, and neither must a caller that
//     mutates what a Get returned. (PutIsAnUpsertAndDeepCopies.)
//
//   - GetPaymentByEndToEndID matches exactly, and an empty end-to-end id is
//     never an identity — it is always ErrPaymentNotFound, the same rule
//     ledger.Tx applies to an empty idempotency key.
//     (GetPaymentByEndToEndIDIsExactAndIgnoresEmpty.)
//
//   - GetOpenCycle returns the cycle whose Scheme matches and whose Status is
//     CycleOpen. The domain keeps at most one open per scheme; if more than one
//     is open the earliest is returned. Closing a cycle must make it invisible
//     here. (GetOpenCycleFindsTheOpenCycleForItsScheme.)
//
//   - Listing order is the creation instant ascending, ties broken by the row's
//     monotonic insertion sequence — never by ID. The creation instant is
//     Bank.CreatedAt, Payment.CreatedAt, Mandate.CreatedAt,
//     ClearingCycle.OpenedAt, Settlement.SettledAt, SettlementMember.OpenedAt
//     and RosterEntry.AdmittedAt.
//     (PaymentListOrderingIsCreatedAtThenSeq.)
//
//   - Rollback spans all three layers: a failed Update undoes payment rows,
//     deposit rows, ledger rows and audit appends written through the same Tx.
//     (UpdateRollsBackAllThreeLayersTogether.)
//
//   - GetSettlementAdvice -> ErrSettlementAdviceNotFound. The key is
//     (book, reference, asset), all three: two banks advised of one movement
//     hold two rows, a bank operating in two assets settles each separately, and
//     a bank holding one advice keyed by a cycle id and another by a payment id
//     in the same asset does not collide (AdvicesAreKeyedByReferenceNotByCycle).
//     ListSettlementAdvices is scoped to ONE book and ordered by AdvisedAt then
//     seq, like every other listing here.
//     (SettlementAdviceIsScopedToTheBankThatWasAdvised.)
//
//   - Reset clears the payment tables too. (ResetClearsPaymentState.)

// ---------------------------------------------------------------------------
// Narrower views of the same store
// ---------------------------------------------------------------------------

// ledgerView presents a payment.Store as a ledger.Store.
//
// A payment.Tx is a ledger.Tx — it embeds one, transitively — so the adapter
// only has to re-type the callback. It exists because Go allows a type one
// Update method, and the three Store interfaces declare Update with three
// different callback types.
//
// The point is that a Book built over this view and a Network built over the
// same Store cannot be talking to different databases: the view is derived from
// the Store rather than passed in beside it.
type ledgerView struct{ Store }

var _ ledger.Store = ledgerView{}

func (v ledgerView) Update(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return v.Store.Update(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx) })
}

func (v ledgerView) View(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return v.Store.View(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx) })
}

// depositView presents a payment.Store as a deposit.Store, for the same reason
// and in the same way as ledgerView.
type depositView struct{ Store }

var _ deposit.Store = depositView{}

func (v depositView) Update(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return v.Store.Update(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx) })
}

func (v depositView) View(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return v.Store.View(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx) })
}

// productView presents a payment.Store as a product.Store, for the same reason
// and in the same way as ledgerView and depositView.
type productView struct{ Store }

var _ product.Store = productView{}

func (v productView) Update(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return v.Store.Update(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx) })
}

func (v productView) View(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return v.Store.View(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx) })
}

// lendingView presents a payment.Store as a lending.Store, for the same reason
// and in the same way as ledgerView and depositView.
type lendingView struct{ Store }

var _ lending.Store = lendingView{}

func (v lendingView) Update(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return v.Store.Update(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx) })
}

func (v lendingView) View(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return v.Store.View(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, tx) })
}
