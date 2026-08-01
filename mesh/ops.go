package mesh

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The three narrowed views of *payment.Network, one per kind of actor.
//
// Each actor holds one of these rather than the whole *payment.Network, so a
// bank handler that calls SettleCycleTx does not COMPILE. That is cheap now and
// it is exactly the seam sub-project 8 needs: when each entity gets its own
// store, these interfaces are already the list of what each one may reach.
//
// # Two mechanisms, because neither alone is enough
//
// These narrow by METHOD. They cannot narrow by BOOK — a bank handler still
// could not be stopped from reading another bank's ledger through a method it
// legitimately holds, because every ledger.Tx method takes the book as an
// ordinary argument and any BookID is as valid as any other. That is what the
// recorder in books_test.go is for, and it is why the debtor-half/creditor-half
// split in payment is load-bearing rather than tidy.
//
// Conversely the recorder cannot do what these do: it observes a run, so it can
// only report a crossing that some test actually provoked, whereas an interface
// that lacks the method makes the crossing unwritable. Method and book, static
// and dynamic — one of each, because each is blind exactly where the other sees.
//
// # Why they are empty
//
// They are declared empty ON PURPOSE and grow method by method as Tasks 8-13
// discover what each handler needs. An interface written ahead of its callers is
// a guess, and a guess here is a wrong boundary that then looks authoritative —
// the worst of both, since every later reader takes it for a decision.
//
// The honest consequence, stated rather than glossed: while they are empty they
// constrain nothing, and the compile-time boundary is not real until Task 13 has
// filled them in. The recorder is the mechanism that bites in the meantime.
//
// # What Task 10 put in, and the hole it could not close
//
// Every method below is one a bank handler calls, and there are no others: a
// bank cannot settle a cycle, close one, or take a payment into one, because
// none of those is here. That much the compiler now enforces.
//
// GetParticipant is the hole, and it is worth naming rather than leaving to be
// found. It answers "which BIC is this participant", which is what the payer's
// bank needs to refuse a status about a payment whose payer banks somewhere else
// — but *payment.Network binds live handles onto what it returns (Network.bind),
// so the value carries another bank's Ledger, Deposit and Catalogue with it. A
// handler that wanted to reach another bank's book has that here, in a method it
// legitimately holds. This is exactly the crossing ops.go's own comment says
// interfaces cannot narrow, and exactly what the recorder in books_test.go is
// for; closing it needs a narrower return, which is payment's to give and
// sub-project 8's to want.
type bankOps interface {
	// The submitting bank's half, and the message it then sends. See
	// Mesh.Submit for why the send is not inside the unit of work.
	SubmitPayment(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error)
	CreditTransferMessage(ctx context.Context, p payment.Payment, mc payment.MessageContext) (iso20022.Envelope, error)

	// The receiving bank's half. CreditTransferRequest resolves the message by
	// ADDRESS, which is the check that answers AC01; AcceptInbound is the half
	// SubmitPaymentTx did not run.
	CreditTransferRequest(ctx context.Context, doc *iso20022.Pacs008) (payment.InitiatePaymentRequest, error)
	AcceptInbound(ctx context.Context, id payment.PaymentID) error

	// The payer's bank's half of a rejection: give the payer their money back.
	// GetPayment is what establishes that there is a decision to act on — see
	// ReverseDebtorLegTx, which does not look at the payment's status itself and
	// says the caller must.
	GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
	GetParticipant(ctx context.Context, id payment.ParticipantID) (*payment.Participant, error)
	ReverseDebtorLeg(ctx context.Context, p payment.Payment, reason string) error
}

// csmOps is the clearing house's view: what a CSM handler may reach.
//
// The comment on bankOps applies to all three — empty on purpose, grown by the
// task that first needs a method.
//
// Three methods for the whole of clearing, and the shortness is the point: a
// clearing house accepts a payment into a cycle, rejects one, and looks up where
// to send the answer. It cannot post, because posting is a bank's act and
// nothing here can reach a ledger. TestTheCSMTouchesOnlyTheNetworkBook is the
// dynamic half of the same claim.
type csmOps interface {
	AcceptAtCSM(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
	RejectAtCSM(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, reason string) (payment.Payment, error)

	// GetParticipant is how a status is addressed back to the payment's own
	// debtor bank. See the note on bankOps for what it also hands over.
	GetParticipant(ctx context.Context, id payment.ParticipantID) (*payment.Participant, error)
}

// settlementOps is the central bank's view: what a settlement handler may
// reach.
//
// The comment on bankOps applies to all three — empty on purpose, grown by the
// task that first needs a method.
type settlementOps interface {
	// Grown by Tasks 12 and 13.
}

// *payment.Network satisfies all three today, and these assertions are what keep
// that true: a method added to one of the interfaces above that the Network does
// not have fails the build here rather than at the handler that wanted it.
//
// They assert nothing while the interfaces are empty. That is not an argument
// for leaving them out — they cost one line each and they are the check that
// starts working the moment Task 10 adds the first method.
var (
	_ bankOps       = (*payment.Network)(nil)
	_ csmOps        = (*payment.Network)(nil)
	_ settlementOps = (*payment.Network)(nil)
)
