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
// today and there are no others. What that is worth is stated exactly, in each
// interface's own note and nowhere more widely: a handler cannot NAME a method
// its interface does not carry. It is not a ban on the operation — see the note
// on GetParticipant below, which two of these three carry and which hands back
// another bank's live handles.
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
// # What Task 13 added, what it deliberately did not, and what 16e reversed
//
// Task 13 added one method, ReturnMessage, and it was the whole of a bank's
// half of a return. The reason recorded here was that a returning bank posts
// NOTHING, because every posting a return made was the settlement agent's — so
// what a bank needed was a way to say so and no way to do it.
//
// That is no longer true and the reason it gave was never the whole one. The
// reserve reversal is central-bank money and no member may move it, which is
// still exactly right; but a return's CUSTOMER legs are not reserves, and each
// of them belongs to the bank whose customer it moves. Task 16e gave each bank
// the leg it owns, so PostReturnLeg and ReverseReturnLeg are on this interface
// beside ReturnMessage, and SettleReturn — the reserve half, and only that — is
// on settlementOps and on no other, so no bank handler can name it.
//
// That is a statement about NAMING and not about capability, and the note above
// on GetParticipant is why it has to be said twice: a bank handler already
// holds a method that hands it another bank's live Ledger and Deposit, so
// posting the OTHER bank's leg by hand is reachable from here. What stops it is
// the domain refusing a bank that is not that leg's owner, and the recorder in
// books_test.go, which measures that each bank reaches its own book and no
// other (TestEachBankBooksItsOwnReturnAndNoOtherBooks).
//
// # What Task 15 added, and why they are a bank's methods at all
//
// Two methods, PostSettlementAdvice and PostCreditorLeg, and they are the first
// things on this interface that the settlement agent used to do TO a bank rather
// than things a bank does. Both are postings in the member's own ledger, so both
// belong to the member; what arrives from the settlement agent is a camt.053
// saying what the reserve account did, and what arrives from the clearing house
// is a pacs.002 saying one payment settled.
//
// Each takes the acting participant as an argument, as the two halves of a
// payment do, and the domain refuses the one that is not this bank's business:
// a statement about anybody else's reserve account
// (payment.ErrStatementNotForThisBank), a payment whose creditor banks somewhere
// else (payment.ErrNotThisBanksPayment). The actor passes its own id — see
// bank.pid — so neither can be used to post another member's half even though
// nothing in the SIGNATURES stops a caller naming one.
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

	// The returning bank's message. It takes no context because it reads no
	// store — the amount's scale comes from the scheme registry, which is a map
	// in memory, and everything it puts in OrgnlTxRef is on the payment it is
	// handed. GetPayment above is what establishes there is a settled payment to
	// return; see bank.returnPayment for why that judgement is made here rather
	// than left to the settlement agent.
	ReturnMessage(p payment.Payment, reason iso20022.ReturnReason, text string, mc payment.MessageContext) (iso20022.Envelope, error)

	// The two halves of a return that are a BANK's, one for each moment a bank
	// acts in one.
	//
	// PostReturnLeg is this bank's own customer leg — the clawback if this bank
	// is the creditor's, the refund if it is the payer's, and never the other,
	// because which leg a bank holds is a fact about the payment rather than a
	// choice the caller or the message makes. It is called from two handlers,
	// and the difference between them is the whole of the return's one rule:
	// the RETURNING bank calls it before it sends the pacs.004 and may be
	// refused there (bank.returnPayment), and the other bank calls it after
	// finality and cannot be (bank.receiveReturn). See payment.PostReturnLegTx,
	// which decides which of the two this bank is and whether it may refuse.
	//
	// ReverseReturnLeg is the first of those undone, when the settlement agent
	// refuses the return the leg was posted for. It exists BECAUSE the posting
	// comes before the send: a bank that had only checked would have nothing to
	// unwind. See bank.receiveReturnStatus.
	//
	// Both take the acting participant for PostSettlementAdvice's reason, and
	// the domain refuses a bank that is neither side of the payment
	// (payment.ErrNotAPartyToThisReturn). The actor passes its own id — see
	// bank.pid — so neither can be used to post another member's leg even
	// though nothing in the signatures stops a caller naming one.
	PostReturnLeg(ctx context.Context, by payment.ParticipantID, id payment.PaymentID, reason string) (payment.Payment, error)
	ReverseReturnLeg(ctx context.Context, by payment.ParticipantID, id payment.PaymentID, reason string) error

	// The bank's half of settlement, and it is the half that used to be done TO
	// it. A member books its own mirror leg from the statement the settlement
	// agent sent; nothing else in this mesh may post in that book, and nothing
	// on this interface lets this bank post in anybody else's.
	PostSettlementAdvice(ctx context.Context, by payment.ParticipantID, m payment.AdvisedMovement) (payment.SettlementAdvice, error)

	// The payee's bank's half of settlement: release one payment out of its own
	// clearing suspense into its own customer's account.
	//
	// It takes the ACTING participant because both banks are told a payment
	// settled and only one may post it — see payment.ErrNotThisBanksPayment. The
	// domain refuses the other, rather than this package deciding: which bank a
	// payment's creditor banks at is a fact about the payment, and a handler that
	// decided it would be asserting something it cannot check.
	PostCreditorLeg(ctx context.Context, by payment.ParticipantID, id payment.PaymentID) (payment.Payment, error)
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
//
// Task 16e added nothing either, and that is the more surprising of the two.
// The clearing house gained a whole second hop on this flow — it now relays the
// pacs.004 onward to the bank that did not ask for the return — and it needs no
// method for it, because the message names both agents itself (OrgnlTxRef) and
// the recipient is whichever of them the message did not come from. What the
// hop DID need is state, and state is not an interface: see csm.held.
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
// The two methods behind it now reach the SAME distance, and that is what Tasks
// 15 and 16 between them did. This interface used to carry ReturnPayment, which
// posted in three books of which two were member banks', and the note here said
// the settlement agent was still the widest-reaching actor in the system on that
// one flow. It is not any more: SettleCycleTx and SettleReturnTx each post in
// the central bank's own book and in no member's, and every customer leg on
// either path is made by the bank whose customer it moves, from a message.
// TestWhichBooksTheCentralBankReachesWhenItSettles and
// TestWhichBooksAReturnReaches measure both rather than assuming either.
//
// SettleReturn hands back statements for the same reason SettleCycle does, and
// they are the same kind of thing: what each member's reserve account did. What
// differs is only the reference they carry — a cycle id at a cut-off, a payment
// id here — which is why payment.SettlementAdvice is keyed by a reference
// rather than by a cycle.
type settlementOps interface {
	// SettleCycle hands back the STATEMENTS beside the settlement, because the
	// closing balance each carries is a claim about a moment inside the unit of
	// work and cannot be re-read after it. Sending them is this actor's, not the
	// domain's: see centralBank.advise.
	SettleCycle(ctx context.Context, id payment.CycleID) (payment.Settlement, []payment.SettlementStatement, error)

	// SettleReturn takes the INSTRUCTION rather than a payment id, and that is
	// the whole of what sub-project 8 needed from this flow: everything it acts
	// on came off the pacs.004 (payment.ReadReturn), because a settlement agent
	// holds no payment rows and never saw the payment clear. Nothing on this
	// interface turns a payment id into a payment, here or at a cut-off.
	SettleReturn(ctx context.Context, in payment.ReturnInstruction) ([]payment.SettlementStatement, error)
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
