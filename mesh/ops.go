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
// # Why they started empty, and what they are now
//
// They were declared empty ON PURPOSE and grew method by method as Tasks 10-13
// discovered what each handler needs. An interface written ahead of its callers
// is a guess, and a guess here is a wrong boundary that then looks
// authoritative — the worst of both, since every later reader takes it for a
// decision. While they were empty they constrained nothing, and only the
// recorder bit.
//
// Task 13 was the last new FLOW, and Task 15 is what has added to them since —
// by moving a posting from one institution to another rather than by inventing
// work. Either way every method below is one some handler in this package calls
// today and there are no others. What that is worth is stated
// exactly, in each interface's own note and nowhere more widely: a handler
// cannot NAME a method its interface does not carry. It is not a ban on the
// operation — see the note on GetParticipant below, which two of these three
// carry and which hands back another bank's live handles.
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
// The pull flow needs more methods and one of them deserves a note. Scheme
// answers "who submits this, and who receives it" — the question the direction
// decides and the one a handler cannot ask any other way. It reads a map in
// memory, takes no unit of work and names no book, so unlike GetParticipant it
// hands nothing over.
//
// Task 11 also gave a bank a way to load a MANDATE, because a pacs.003 carries
// one. That method is gone again, and its absence is the C1 fix rather than a
// narrowing for its own sake: the mandate is now read inside the submitting
// bank's own unit of work, by payment.InstructionTx, because the message has to
// be built there or not at all.
//
// # What Task 13 added, and what it deliberately did not
//
// One method, ReturnMessage, and it is the whole of a bank's half of a return.
// A returning bank posts NOTHING — the three compensating transactions are the
// settlement agent's, because the middle one moves reserves — so what it needs
// is a way to say so and no way to do it. ReturnPayment is on settlementOps and
// on no other interface, so no bank handler can name it.
//
// That is a statement about NAMING and not about capability, and the note above
// on GetParticipant is why it has to be said twice: a bank handler already
// holds a method that hands it another bank's live Ledger and Deposit, so
// posting a return's legs by hand is reachable from here. What stops it is the
// recorder in books_test.go, which measures that a returning bank touches no
// book at all (TestWhichBooksAReturnReaches).
//
// # What Task 15 added, and why it is a bank's method at all
//
// One method, PostSettlementAdvice, and it is the first thing on this interface
// that the settlement agent used to do TO a bank rather than something a bank
// does. The mirror leg is a posting in the member's own ledger, so it belongs to
// the member; what arrives from the settlement agent is a camt.053 saying what
// the reserve account did.
//
// It takes the acting participant as an argument, as the two halves of a payment
// do, and the domain refuses a statement about anybody else's reserve account
// (payment.ErrStatementNotForThisBank). The actor passes its own id — see
// bank.pid — so the interface cannot be used to book another member's cut-off
// even though nothing in the SIGNATURE stops a caller naming one.
type bankOps interface {
	// The submitting bank's half, and the message it then sends. See
	// Mesh.Submit for why the send is not inside the unit of work.
	//
	// ONE method and not two, and that is the fix for a money bug rather than
	// tidying. This was SubmitPayment plus a message built afterwards, and
	// building one can fail — a payee the instruction quoted no address for —
	// so a refusal the API answered 422 left the payer debited by a request it
	// had reported as refused. Posting the leg and rendering the instruction
	// now commit or roll back together; the send still happens after, which is
	// the property TestARolledBackSubmitSendsNothing pins.
	//
	// Which message it produces is the scheme's direction, and payment is what
	// decides: a pacs.008 pushes money at the payee's bank, a pacs.003 asks the
	// payer's bank for it and carries the mandate that makes the ask
	// authorised. See payment.InstructionTx.
	SubmitAndInstruct(ctx context.Context, req payment.InitiatePaymentRequest, mc payment.MessageContext) (payment.Payment, iso20022.Envelope, error)

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

	// The returning bank's half, which is this message and nothing else. It
	// takes no context because it reads no store — the amount's scale comes
	// from the scheme registry, which is a map in memory, and a pacs.004 names
	// no parties to look up. GetPayment above is what establishes there is a
	// settled payment to return; see bank.returnPayment for why that judgement
	// is made here rather than left to the settlement agent.
	ReturnMessage(p payment.Payment, reason iso20022.ReturnReason, text string, mc payment.MessageContext) (iso20022.Envelope, error)

	// The bank's half of settlement, and it is the half that used to be done TO
	// it. A member books its own mirror leg from the statement the settlement
	// agent sent; nothing else in this mesh may post in that book, and nothing
	// on this interface lets this bank post in anybody else's.
	PostSettlementAdvice(ctx context.Context, by payment.ParticipantID, m payment.AdvisedMovement) (payment.SettlementAdvice, error)
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
//
// Task 13 added NOTHING here, and that is worth a sentence because it is the
// shape of the flow rather than a coincidence. Carrying a return needs no store
// at all on the way out — the destination is decided by the message definition
// — and the answer coming back needs exactly the three the ACSC fan-out already
// needed: the payment the status names, its scheme, and the participant to
// address. Same three questions, asked about one payment instead of a batch.
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
// Two methods, and they are the two ways central-bank reserves move in this
// system: a cut-off's net positions being discharged, and a settled payment
// being sent back. Neither is on any other interface, so a bank handler or a
// clearing-house handler cannot NAME either — which is what these interfaces
// narrow, and the whole of what they narrow.
//
// The second is the one that qualifies "the central bank never sees an
// individual payment". At a cut-off it does not: SettleCycle names a cycle, and
// nothing here turns one into the payments inside it. A RETURN names exactly
// one payment, because that is what a return is — one settled payment coming
// back — and the returning bank named it in the message. What this institution
// still cannot do is ENUMERATE the payments of a batch, which is why the
// settlement fan-out is the clearing house's; see csm.tellSettled.
//
// It is emphatically not a ban on those handlers moving money, for the reason
// the note on bankOps sets out at length: GetParticipant is on both of the other
// interfaces and returns a value carrying live ledger and deposit handles bound
// to whichever bank it names, so posting is reachable from either of them
// through a method each legitimately holds. The recorder in books_test.go is
// what watches for that, here as everywhere else in this package.
//
// And both methods behind it still reach further than two methods suggest.
// SettleCycleTx no longer posts the mirror leg — that is the member's own, made
// from the statement this call hands back — but it does still post every
// CREDITOR leg, which is a posting in the payee's bank's ledger, and
// ReturnPaymentTx posts in three books, two of which belong to member banks. So
// the central bank remains the widest-reaching actor in this system rather than
// the most confined. TestWhichBooksTheCentralBankReachesWhenItSettles and
// TestWhichBooksAReturnReaches measure that rather than assuming it.
type settlementOps interface {
	// SettleCycle hands back the STATEMENTS beside the settlement, because the
	// closing balance each carries is a claim about a moment inside the unit of
	// work and cannot be re-read after it. Sending them is this actor's, not the
	// domain's: see centralBank.advise.
	SettleCycle(ctx context.Context, id payment.CycleID) (payment.Settlement, []payment.SettlementStatement, error)
	ReturnPayment(ctx context.Context, id payment.PaymentID, reason string) (payment.Payment, error)
}

// *payment.Network satisfies all three today, and these assertions are what keep
// that true: a method added to one of the interfaces above that the Network does
// not have fails the build here rather than at the handler that wanted it.
//
// They asserted nothing while the interfaces were empty, which was not an
// argument for leaving them out: they cost one line each and they started
// working the moment Task 10 added the first method.
var (
	_ bankOps       = (*payment.Network)(nil)
	_ csmOps        = (*payment.Network)(nil)
	_ settlementOps = (*payment.Network)(nil)
)
