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
// Task 13 was the last new flow to add to them in that shape, and Tasks 15 and
// 17 are what have added since — by moving a posting from one institution to
// another, by narrowing a return, and finally by giving each of the three
// interfaces its own act of an ADMISSION, which is the fourth flow and the only
// one whose subject is a member rather than a payment. Either way every method
// below is one some handler in this package calls today and there are no
// others.
//
// Admission's four acts land three-and-one, and the one that is missing says
// something about the flow. RecordMembership is on bankOps, AdmitMember and
// GetRosterEntryByBIC on csmOps, OpenSettlementAccount on settlementOps —
// and FoundBankTx is on none of them, because it runs before the joining bank
// has an actor to hold an interface. It is reached through Mesh.Admit instead,
// on the caller's goroutine, exactly as the submitting half of a payment is.
//
// What all of that is worth is stated exactly, in each interface's own note and
// nowhere more widely: a handler cannot NAME a method its interface does not
// carry.
//
// # What Task 10 put in, the hole it could not close, and what closed it
//
// Every method below is one a bank handler calls, and there are no others: a
// bank cannot settle a cycle, close one, or take a payment into one, because
// none of those is here. That much the compiler enforces.
//
// GetParticipant used to be the hole in that, and this note used to say so at
// length. It answered "which BIC is this participant", which is what the
// payer's bank needs in order to refuse a status about a payment whose payer
// banks somewhere else — but *payment.Network binds live handles onto what it
// returned (Network.bind), so the value carried another bank's Ledger, Deposit
// and Catalogue with it. A handler that wanted to reach another bank's book had
// that here, in a method it legitimately held. The note recorded that closing
// it needed a narrower RETURN, which was payment's to give and sub-project 8's
// to want.
//
// Task 17 gave it. payment.Participant is dissolved into three rows, one per
// owning institution, and what a handler asking about somebody else now gets is
// the clearing house's: payment.RosterEntry, which is a BIC, a name, a set of
// assets and an admission reference. There is no handle on it to hand over,
// because live handles exist only on a bank's own record and nothing on these
// interfaces returns one.
//
// So the crossing this file said interfaces could not narrow was closed by
// narrowing a type instead. Two things follow and both are still true. The
// recorder in books_test.go is still what watches for a handler reaching into a
// book it does not own, because these interfaces still cannot narrow by BOOK.
// And a crossing remains at GetRosterEntry itself: a payment names its parties
// by ParticipantID, so payment reads the bank's own row to turn one into a BIC
// before it can read the roster. That read is inside payment and is named where
// it happens (payment.Network.GetRosterEntry); what closes it is a payment that
// carries BICs, which is Task 18's.
//
// # What Task 11 added, and why none of it widens the hole
//
// The pull flow needs more methods and one of them deserves a note. Scheme
// answers "who submits this, and who receives it" — the question the direction
// decides and the one a handler cannot ask any other way. It reads a map in
// memory, takes no unit of work and names no book, so it was the one method
// here that handed nothing over even while GetParticipant did.
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
// That was a statement about NAMING and not about capability, and it had to be
// made twice while GetParticipant was on this interface: a bank handler held a
// method that handed it another bank's live Ledger and Deposit, so posting the
// OTHER bank's leg by hand was reachable from here. It is not any more — no
// method below returns a handle to a book this actor does not own. What still
// stops a handler acting on somebody else's leg is what always did the real
// work: the domain refusing a bank that is not that leg's owner, and the
// recorder in books_test.go, which measures that each bank reaches its own book
// and no other (TestEachBankBooksItsOwnReturnAndNoOtherBooks). Neither of those
// was made redundant by the narrowing, because neither was ever the compiler's
// job.
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
	//
	// GetRosterEntry is what tells this bank whether a rejection it was sent is
	// about a payment whose payer banks somewhere else — a comparison of two
	// BICs, its own against the one the roster holds for the payment's payer.
	// The whole of what it needs is the address, and the whole of what it gets
	// is the address: see payment.RosterEntry, and the note above on the hole
	// this replaced.
	GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
	GetRosterEntry(ctx context.Context, id payment.ParticipantID) (payment.RosterEntry, error)
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

	// The bank's second act of its own admission: writing down the settlement
	// account numbers the acknowledgement told it, and becoming a Member.
	//
	// It is the only method here a bank calls about ITSELF rather than about a
	// payment, and it takes the acting participant for the reason the two
	// settlement methods above do: the actor passes its own id (see bank.pid) and
	// the domain refuses an acknowledgement addressed to anybody else
	// (payment.ErrNotThisBanksAdmission).
	//
	// The bank's FIRST act is not here and cannot be. FoundBankTx runs before
	// this bank has an actor at all — there is nothing to hold an interface — so
	// it is reached through mesh.Mesh.Admit, on the caller's goroutine, exactly
	// as the submitting half of a payment is.
	RecordMembership(ctx context.Context, by payment.ParticipantID, in payment.AdmissionAcknowledgement) (*payment.Bank, error)
}

// csmOps is the clearing house's view: what a CSM handler may reach.
//
// The comment on bankOps applies to all three — empty on purpose, grown by the
// task that first needs a method.
//
// Seven methods for the whole of clearing: a clearing house accepts a payment
// into a cycle, rejects one, reaches the cut-off, asks which direction a payment
// runs in, and looks up where to send the answer. Posting is a bank's act or a
// central bank's, and no method here is one — with no exception now. The note
// above records the one there used to be: GetParticipant was on this interface
// too and handed over a member's live ledger and deposit handles, which is a
// way to post. GetRosterEntry replaced it and hands over an address.
//
// That does not make the ban the compiler's. These interfaces narrow by method
// and never by book, so a clearing house that acquired a posting method would
// be stopped by nothing here; TestTheCSMTouchesOnlyTheNetworkBook is what
// enforces it, and TestTheCSMStillTouchesOnlyTheNetworkBookWhenItSettles
// extends it over the cut-off and the settlement conversation Task 12 added.
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

	// GetRosterEntry is how a status is addressed back to the bank that
	// submitted the payment, and it is the clearing house reading its OWN row —
	// the roster is this institution's record, which is what makes this the one
	// lookup here that survives the split intact.
	//
	// Scheme is what says which bank that is: the payer's for a push, the payee's
	// for a pull. Reading it off the payment's own scheme rather than off the
	// message means the clearing house answers the instructing agent even when
	// the message it is acting on came from the other one, which on a pull it
	// always does.
	GetRosterEntry(ctx context.Context, id payment.ParticipantID) (payment.RosterEntry, error)
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

	// Admission's two, and they are the first methods here whose subject is a
	// MEMBER rather than a payment.
	//
	// AdmitMember writes the routing entry from an acknowledgement this
	// institution did not originate, which is the ordering the domain has: scheme
	// membership follows the settlement account, so a bank the settlement agent
	// will not open an account for is not one this clearing house can route for.
	//
	// GetRosterEntryByBIC is the refusal's read, and it is the one lookup on any
	// of these three interfaces that crosses nothing. Every other roster read here
	// starts from a ParticipantID and has to go through the bank's own row to
	// reach an address (see payment.Network.GetRosterEntry); this one starts from
	// the BIC, because that is what an acmt.007 carries and the only identifier an
	// applicant has told this institution. See csm.relayAdmission.
	AdmitMember(ctx context.Context, in payment.AdmissionAcknowledgement) (payment.RosterEntry, error)
	GetRosterEntryByBIC(ctx context.Context, bic iso20022.BIC) (payment.RosterEntry, error)
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
// It is still not a ban on those handlers moving money, and the reason has
// changed. It used to be a method: GetParticipant was on both of the other
// interfaces and returned a value carrying live ledger and deposit handles
// bound to whichever bank it named, so posting was reachable from either of
// them through a method each legitimately held. Task 17 narrowed that return to
// payment.RosterEntry and there is no handle on either interface now.
//
// What remains is the limit these interfaces have always had and cannot lose:
// they narrow by METHOD, and every ledger.Tx method takes its book as an
// ordinary argument, so a handler holding any posting method at all can post in
// any book. The recorder in books_test.go is what watches for that, here as
// everywhere else in this package.
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

	// OpenSettlementAccount is the third thing this institution does, and the
	// first that is not a movement: an account holder has asked it to open an
	// account, and it opens one in its own book and records that it holds it.
	//
	// It takes the REQUEST rather than a bank, and that is the same property
	// SettleReturn has for the same reason: everything it acts on came off the
	// acmt.007 (payment.ReadAdmissionRequest), because a settlement agent holds
	// no roster and has never heard of this system's bank ids. What it is told is
	// a BIC, a name and one currency, and a BIC is what it keys its own row by.
	OpenSettlementAccount(ctx context.Context, in payment.AdmissionRequest) (payment.SettlementMember, error)
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
