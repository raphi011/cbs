package csm

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// ops is the clearing house's view NARROWED: what a CSM handler may reach.
type ops interface {
	// The clearing house's own copy of an instruction it is carrying, written as
	// it routes one.
	RecordRelayedCreditTransfer(ctx context.Context, doc *iso20022.Pacs008) ([]payment.Payment, error)
	RecordRelayedDirectDebit(ctx context.Context, doc *iso20022.Pacs003) ([]payment.Payment, error)

	AcceptAtCSM(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
	RejectAtCSM(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, reason string) (payment.Payment, error)

	// The two the clearing house needs to keep its OWN copies honest once the
	// decision is somebody else's to make.
	SettleAtCSM(ctx context.Context, id payment.CycleID) ([]payment.Payment, error)
	CompleteReturn(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// Scheme is what says which bank a status goes back to: the payer's for a
	// push, the payee's for a pull.
	Scheme(id payment.SchemeID) (payment.Scheme, bool)

	// The cut-off, and what it has to say afterwards.
	CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)
	GetCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)
	GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// The two ends of a clearing day, and the pair that makes the cut-off an event
	// on a calendar rather than a route somebody remembers to call.
	ListCycles(ctx context.Context) ([]payment.ClearingCycle, error)
	OpenCycle(ctx context.Context, scheme payment.SchemeID) (payment.ClearingCycle, error)

	// ListSchemes is which cycles there are to open. The scheme registry is a map
	// in memory rather than a table, which is why this takes no context.
	ListSchemes() []payment.Scheme

	// GetRosterEntryByBIC is the one lookup on this interface that crosses
	// nothing.
	GetRosterEntryByBIC(ctx context.Context, bic iso20022.BIC) (payment.RosterEntry, error)

	// ListRosterEntries is the roster PUBLISHED: every member, as the directory a
	// subscriber pulls.
	ListRosterEntries(ctx context.Context) ([]payment.RosterEntry, error)

	// WHAT THIS INSTITUTION HAS TAKEN IN AND NOT YET HANDED OVER, which is the
	// only state in this package that is an obligation rather than a record. See
	// payment.HeldFile and payment.HeldReturn.
	HoldFile(ctx context.Context, f payment.HeldFile) error
	ListHeldFiles(ctx context.Context, id payment.CycleID) ([]payment.HeldFile, error)
	DropHeldFile(ctx context.Context, id payment.CycleID, seq int64) error

	HoldReturn(ctx context.Context, r payment.HeldReturn) error
	GetHeldReturn(ctx context.Context, id payment.PaymentID) (payment.HeldReturn, error)
	DropHeldReturn(ctx context.Context, id payment.PaymentID) error
}

// The clearing house's type satisfies the clearing house's interface, and this
// assertion is what keeps that true: a method moved to the wrong institution in
// payment fails the build here, naming both.
var _ ops = (*payment.ClearingHouseNetwork)(nil)
