package payment

import (
	"context"

	"github.com/raphi011/cbs/deposit"
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
// lets Participant.RunEndOfDay accrue an overdraft and a loan in a single unit
// of work: two batches, one commit, so a failure halfway cannot leave a bank
// with a day of interest on its loans and none on its overdrafts.
//
// Network-scoped entities — participants, payments, mandates, cycles,
// settlements — belong to no single bank and are stored under
// ledger.NetworkBook.
type Tx interface {
	deposit.Tx
	lending.Tx

	PutParticipant(ctx context.Context, p Participant) error
	GetParticipant(ctx context.Context, id ParticipantID) (Participant, error)
	ListParticipants(ctx context.Context) ([]Participant, error)

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
	// Participants, payments, mandates, cycles and settlements belong to no
	// single bank and live under ledger.NetworkBook; an advice is one member
	// bank's record of what it was told, so it is keyed by that bank's book —
	// which is also what makes the recorder in mesh/books_test.go see a bank
	// reaching its own book when it books a settlement.
	PutSettlementAdvice(ctx context.Context, book ledger.BookID, a SettlementAdvice) error
	GetSettlementAdvice(ctx context.Context, book ledger.BookID, cycle CycleID, asset ledger.AssetCode) (SettlementAdvice, error)
	ListSettlementAdvices(ctx context.Context, book ledger.BookID) ([]SettlementAdvice, error)
}

// Contract notes for implementers. Each of these is asserted by
// storetest.RunPayment; the named subtest is what pins it.
//
//   - Not-found sentinels. GetParticipant -> ErrParticipantNotFound, GetPayment
//     and GetPaymentByEndToEndID -> ErrPaymentNotFound, GetMandate ->
//     ErrMandateNotFound, GetCycle and GetOpenCycle -> ErrCycleNotFound,
//     GetSettlement -> ErrSettlementNotFound. errors.Is is used, so wrapping is
//     fine. (GetOnMissingPaymentRowsReturnsSentinels.)
//
//   - Participant.Ledger and Participant.Deposit are NOT persisted. They are
//     live handles over the store, not data; store/pg has no column to put them
//     in, so store/mem must not keep them either — otherwise code that works in
//     memory breaks on Postgres. The Network rebinds them on the way out.
//     (ParticipantRoundTripsAndDropsLiveHandles.)
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
//     Participant.CreatedAt, Payment.CreatedAt, Mandate.CreatedAt,
//     ClearingCycle.OpenedAt and Settlement.SettledAt.
//     (PaymentListOrderingIsCreatedAtThenSeq.)
//
//   - Rollback spans all three layers: a failed Update undoes payment rows,
//     deposit rows, ledger rows and audit appends written through the same Tx.
//     (UpdateRollsBackAllThreeLayersTogether.)
//
//   - GetSettlementAdvice -> ErrSettlementAdviceNotFound. The key is
//     (book, cycle, asset), all three: two banks advised of one cut-off hold two
//     rows, and a bank operating in two assets settles each separately.
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
