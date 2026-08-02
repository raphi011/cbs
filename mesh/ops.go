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
//
// # What Task 11 added, and why none of it widens the hole
//
// The pull flow needs four more methods and one of them deserves a note.
// Scheme answers "who submits this, and who receives it" — the question the
// direction decides and the one a handler cannot ask any other way. It reads a
// map in memory, takes no unit of work and names no book, so unlike
// GetParticipant it hands nothing over. GetMandate reads a network-scoped row:
// mandates belong to no single bank, exactly as payments and cycles do.
type bankOps interface {
	// The submitting bank's half, and the message it then sends. See
	// Mesh.Submit for why the send is not inside the unit of work.
	//
	// Two messages, because which one a submission produces is the scheme's
	// direction: a pacs.008 pushes money at the payee's bank, a pacs.003 asks
	// the payer's bank for it. The mandate travels with the second because it is
	// the only thing that makes the ask authorised.
	SubmitPayment(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error)
	CreditTransferMessage(ctx context.Context, p payment.Payment, mc payment.MessageContext) (iso20022.Envelope, error)
	DirectDebitMessage(ctx context.Context, p payment.Payment, m payment.Mandate, mc payment.MessageContext) (iso20022.Envelope, error)
	GetMandate(ctx context.Context, id payment.MandateID) (payment.Mandate, error)

	// The receiving bank's half. CreditTransferRequest and DirectDebitRequest
	// resolve the message by ADDRESS, which is the check that answers AC01;
	// AcceptInbound is the half SubmitPaymentTx did not run — a check for the
	// payee's bank on a push, and the posting of the debtor leg for the payer's
	// bank on a pull.
	CreditTransferRequest(ctx context.Context, doc *iso20022.Pacs008) (payment.InitiatePaymentRequest, error)
	DirectDebitRequest(ctx context.Context, doc *iso20022.Pacs003) (payment.InitiatePaymentRequest, error)
	AcceptInbound(ctx context.Context, id payment.PaymentID) error

	// The payer's bank's half of a rejection: give the payer their money back.
	// GetPayment is what establishes that there is a decision to act on — see
	// ReverseDebtorLegTx, which does not look at the payment's status itself and
	// says the caller must.
	//
	// Scheme is how a bank decides which of the two roles a status makes it play:
	// the submitter waiting for an answer, or the bank holding money it must
	// give back. For a push those are one bank and for a pull they are two.
	GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
	GetParticipant(ctx context.Context, id payment.ParticipantID) (*payment.Participant, error)
	Scheme(id payment.SchemeID) (payment.Scheme, bool)
	ReverseDebtorLeg(ctx context.Context, p payment.Payment, reason string) error
}

// csmOps is the clearing house's view: what a CSM handler may reach.
//
// The comment on bankOps applies to all three — empty on purpose, grown by the
// task that first needs a method.
//
// Seven methods for the whole of clearing: a clearing house accepts a payment
// into a cycle, rejects one, reaches the cut-off, asks which direction a payment
// runs in, and looks up where to send the answer. Posting is a bank's act or a
// central bank's, and no method here is one — with the same exception the note
// above records, since GetParticipant is on this interface too and hands over
// the same live handles. So the ban on posting is the recorder's and not the
// compiler's; TestTheCSMTouchesOnlyTheNetworkBook is what enforces it, and
// TestTheCSMStillTouchesOnlyTheNetworkBookWhenItSettles extends it over the
// cut-off and the settlement conversation Task 12 added.
type csmOps interface {
	AcceptAtCSM(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
	RejectAtCSM(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, reason string) (payment.Payment, error)

	// GetParticipant is how a status is addressed back to the bank that
	// submitted the payment. See the note on bankOps for what it also hands over.
	//
	// Scheme is what says which bank that is: the payer's for a push, the payee's
	// for a pull. Reading it off the payment's own scheme rather than off the
	// message means the clearing house answers the instructing agent even when
	// the message it is acting on came from the other one, which on a pull it
	// always does.
	GetParticipant(ctx context.Context, id payment.ParticipantID) (*payment.Participant, error)
	Scheme(id payment.SchemeID) (payment.Scheme, bool)

	// The cut-off, and what it now has to say afterwards.
	//
	// CloseCycle is the clearing house's own act: netting is what a clearing
	// house is for, and no bank and no central bank may reach it. It posts
	// nothing — see the note above the tests in books_test.go — so having it
	// here does not give this actor a way to move money.
	//
	// GetCycle and GetPayment are what the ACSC fan-out needs and are reached
	// only from it: a settlement is answered per CYCLE, and the banks waiting
	// for an answer are the ones that submitted its PAYMENTS, which is a
	// question about each payment's own direction. Both read network-scoped
	// rows, as payments, cycles and mandates all do.
	CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)
	GetCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)
	GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
}

// settlementOps is the central bank's view: what a settlement handler may
// reach.
//
// The comment on bankOps applies to all three — empty on purpose, grown by the
// task that first needs a method.
//
// One method, because settling a cycle is the only thing this institution does.
// It is on this interface and on no other, so a bank handler or a clearing-house
// handler cannot NAME it — which is what these interfaces narrow, and the whole
// of what they narrow.
//
// It is emphatically not a ban on those handlers moving money, for the reason
// the note on bankOps sets out at length: GetParticipant is on both of the other
// interfaces and returns a value carrying live ledger and deposit handles bound
// to whichever bank it names, so posting is reachable from either of them
// through a method each legitimately holds. The recorder in books_test.go is
// what watches for that, here as everywhere else in this package.
//
// And the method behind this one reaches further than the interface suggests.
// SettleCycleTx posts in EVERY participant's book as well as the central bank's
// — the mirror leg and the creditor leg are both postings in a member's ledger —
// so the central bank is the widest-reaching actor in this system rather than
// the most confined. TestWhichBooksTheCentralBankReachesWhenItSettles measures
// that rather than assuming it.
type settlementOps interface {
	SettleCycle(ctx context.Context, id payment.CycleID) (payment.Settlement, error)
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
