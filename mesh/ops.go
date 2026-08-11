package mesh

import (
	"context"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The three narrowed views of *payment.Network, one per kind of actor.
//
// Each actor holds one of these rather than the whole *payment.Network, so a
// bank handler that calls SettleCycleTx does not COMPILE.
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
// # Every method below has a caller
//
// They grew method by method as each handler discovered what it needs. An
// interface written ahead of its callers is a guess, and a guess here is a wrong
// boundary that then looks authoritative.
//
// What all of that is worth is stated exactly, in each interface's own note and
// nowhere more widely: a handler cannot NAME a method its interface does not
// carry.
//
// # bankOps
//
// A bank cannot settle a cycle, close one, or take a payment into one, because
// none of those is here. Nothing on it returns a handle to a book this actor
// does not own, so posting another bank's leg by hand is unreachable — and what
// still stops a handler acting on somebody else's leg is what always did the
// real work: the domain refusing a bank that is not that leg's owner, and the
// recorder in books_test.go.
//
// PostSettlementAdvice and SettleAtBank are the two things here the settlement
// agent might look like it should do TO a bank. Both are postings in the
// member's own ledger, so both belong to the member; what arrives from the
// settlement agent is a camt.053 saying what the reserve account did, and what
// arrives from the clearing house is a pacs.002 saying one payment settled.
//
// No method takes an acting participant. The bank acting is the identity of the
// network behind this interface, so there is no argument to name somebody else
// with — see payment.Identity. The SUBJECT guards behind that are what decide
// between two member banks: a statement about anybody else's reserve account is
// payment.ErrStatementNotForThisBank, a payment this bank is no party to is
// payment.ErrNotThisBanksPayment.
type bankOps interface {
	// The submitting bank's half, and the message it then sends. See
	// Mesh.Submit for why the send is not inside the unit of work.
	//
	// ONE method and not two: building a message can fail — a payee the
	// instruction quoted no address for — and a refusal reported after the
	// debtor leg committed leaves the payer debited by a request the API
	// answered 422. Posting the leg and rendering the instruction commit or
	// roll back together; the send still happens after, which is the property
	// TestARolledBackSubmitSendsNothing pins.
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
	//
	// The address is resolved in this bank's own register and in no other: the
	// register searched belongs to the network these methods are called on.
	//
	// AcceptInbound takes the REQUEST as well as the id because this bank has no
	// row for the payment — the submitting bank's row is in the submitting
	// bank's database — so the request the resolution just produced is not a
	// check to be thrown away, it IS the payment. See payment.AcceptInboundTx.
	//
	// Both read a FILE and answer with one transaction per instruction in it,
	// each carrying the id the submitting bank minted. A file this bank cannot
	// read is refused entire — see payment.creditTransferIn.
	CreditTransferRequest(ctx context.Context, doc *iso20022.Pacs008) ([]payment.InboundTransaction, error)
	DirectDebitRequest(ctx context.Context, doc *iso20022.Pacs003) ([]payment.InboundTransaction, error)
	AcceptInbound(ctx context.Context, id payment.PaymentID, req payment.InitiatePaymentRequest) error

	// ResolveIdentifier is the on-us check, and it is the one method here the
	// mesh calls on the caller's goroutine rather than inside a handler: an
	// instruction naming a payee this same bank holds is not a payment this
	// transport carries, and the bank that would submit it is the only
	// institution that can say so. See Mesh.Submit and payment.ErrOnUsPayment.
	//
	// It reaches THIS bank's register and no other, and that is a property of
	// the network behind the interface rather than of what the caller passes.
	ResolveIdentifier(ctx context.Context, ident deposit.Identifier) (payment.PartyRef, error)

	// This bank's own copy of a payment, which is the only one it has and the
	// only one it may read. One handler asks for it — bank.returnPayment, which
	// has to establish there is a SETTLED payment to return before it builds a
	// pacs.004 — and no other: a handler acting on what it was told hands the id
	// to the act and lets the act read.
	//
	// There is no roster read here. A bank sent a decision about a payment it is
	// no party to holds no row for it, so the STORE refuses before any BIC is
	// looked at.
	GetPayment(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// The bank's half of a rejection: record it on this bank's own copy, and give
	// the payer their money back if this bank is the one holding it.
	//
	// ONE method and not a judgement made here, because ReverseDebtorLegTx looks
	// at no status of its own: the decision and the reversal are one act at the
	// institution that owns both. Which of the two roles a status makes this
	// bank play — the submitter waiting for an answer, or the bank holding money
	// it must give back — is the act's to decide, off the payment and its own
	// identity. See payment.RejectAtBankTx.
	RejectAtBank(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, reason string) (payment.Payment, error)

	// The bank's half of an acceptance, and the one of the three that posts
	// nothing. It is here because a bank that merely READ its ACCP could not
	// refuse the rejection above: RejectAtBank's guard is this bank's own copy's
	// status, and only this method ever puts Accepted on it. See
	// payment.AcceptAtBankTx, and Network.transition for what the state is worth.
	AcceptAtBank(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

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
	// Neither takes the acting participant, for PostSettlementAdvice's reason,
	// and the domain still refuses a bank that is neither side of the payment
	// (payment.ErrNotAPartyToThisReturn) — which is the guard that decides
	// between two members and the only one that could.
	PostReturnLeg(ctx context.Context, id payment.PaymentID, reason string) (payment.Payment, error)
	ReverseReturnLeg(ctx context.Context, id payment.PaymentID, reason string) error

	// The mirror leg. A member books it from the statement the settlement agent
	// sent; nothing else in this mesh may post in that book, and nothing on this
	// interface lets this bank post in anybody else's.
	PostSettlementAdvice(ctx context.Context, m payment.AdvisedMovement) (payment.SettlementAdvice, error)

	// The bank's half of settlement: record it on this bank's own copy, and — if
	// this bank holds the payee — release the payment out of its own clearing
	// suspense into its own customer's account.
	//
	// Both banks hold a copy only they can write, so the status is what both of
	// them do and the posting is what one of them does. See
	// payment.SettleAtBankTx, which refuses a bank that is party to neither
	// side.
	//
	// Which bank posts is decided by comparing the payment's creditor against the
	// acting bank, and the acting bank is the network's identity rather than an
	// argument. The domain decides rather than this package: which bank a
	// payment's creditor banks at is a fact about the payment, and a handler that
	// decided it would be asserting something it cannot check.
	SettleAtBank(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// The bank that ASKED for a return learning that it went through. It posts
	// nothing — its own leg went in before it sent the pacs.004 — and marks its
	// own copy Returned, which is the one fact about this payment it cannot learn
	// any other way now that the other bank's leg lands in the other bank's
	// database. See payment.CompleteReturnTx and payment.PostReturnLegTx's note on
	// position in the conversation.
	CompleteReturn(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// The bank's own liquidity management, and the second thing on this interface
	// whose subject is this bank rather than a payment.
	//
	// LodgeReserves swaps vault cash for a claim on the central bank: it posts
	// Debit Reserve at Central Bank / Credit Vault Cash in THIS bank's book and
	// renders the camt.050 that asks the central bank to make the matching entry
	// in its own. Both halves in one method for SubmitAndInstruct's reason — a
	// message that will not build must take the leg down with it — and the send
	// still happens after, outside the unit of work.
	//
	// The receiving half is ReceiveLodgement, on settlementOps and on no other,
	// so no bank handler can name it — which is the same shape SettleReturn has
	// and for the same reason: crediting a reserve account is the account
	// servicer's act, and no member may make it.
	//
	// The lodging member is the network's identity, as it is for the settlement
	// and return methods above. What the domain refuses is a bank that cannot
	// name its own reserve account (payment.ErrSettlementMemberNotFound).
	LodgeReserves(ctx context.Context, asset ledger.AssetCode,
		amount ledger.Amount, mc payment.MessageContext) (payment.LodgementInstruction, iso20022.Envelope, error)
}

// csmOps is the clearing house's view: what a CSM handler may reach.
//
// A clearing house accepts a payment into a cycle, rejects one, reaches the
// cut-off, asks which direction a payment runs in, and looks up where to send
// the answer. Posting is a bank's act or a central bank's, and no method here is
// one.
//
// That does not make the ban the compiler's. These interfaces narrow by method
// and never by book, so a clearing house that acquired a posting method would be
// stopped by nothing here; TestTheCSMTouchesOnlyItsOwnBook is what enforces it,
// and TestTheCSMStillTouchesOnlyItsOwnBookWhenItSettles extends it over the
// cut-off and the settlement conversation.
//
// Relaying a RETURN needs no method: the message names both agents itself
// (OrgnlTxRef) and the recipient is whichever of them the message did not come
// from. What that hop does need is state, and state is not an interface — see
// csm.held.
type csmOps interface {
	// The clearing house's own copy of an instruction it is carrying, written as
	// it routes one. AcceptAtCSM loads the payment by id, and every institution
	// keeps its own row, so without these the clearing house would be reaching
	// for one that was never written in its database.
	//
	// Recording is not accepting: membership, the open cycle and the direction
	// are all asked later and can all refuse — and being able to refuse with a
	// row in hand is what lets the refusal be ANSWERED. See
	// payment.RecordRelayedTx.
	//
	// They take the DOCUMENT, unlike a bank's pair, because this institution has
	// no register: there is no resolution for a caller to have made on its behalf,
	// so there is no reason to split the read from the write.
	RecordRelayedCreditTransfer(ctx context.Context, doc *iso20022.Pacs008) ([]payment.Payment, error)
	RecordRelayedDirectDebit(ctx context.Context, doc *iso20022.Pacs003) ([]payment.Payment, error)

	AcceptAtCSM(ctx context.Context, id payment.PaymentID) (payment.Payment, error)
	RejectAtCSM(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, reason string) (payment.Payment, error)

	// The two the clearing house needs to keep its OWN copies honest once the
	// decision is somebody else's to make.
	//
	// SettleAtCSM marks a settled cycle's payments Settled and hands them back, so
	// the per-payment fan-out reads what it just wrote rather than the batch a
	// second time. CompleteReturn does the same for one returned payment. Neither
	// posts: this institution holds no book, and both are recording news that
	// arrived from the settlement agent.
	//
	// See payment.SettleAtCSMTx.
	SettleAtCSM(ctx context.Context, id payment.CycleID) ([]payment.Payment, error)
	CompleteReturn(ctx context.Context, id payment.PaymentID) (payment.Payment, error)

	// Scheme is what says which bank a status goes back to: the payer's for a
	// push, the payee's for a pull. Reading it off the payment's own scheme rather
	// than off the message means the clearing house answers the instructing agent
	// even when the message it is acting on came from the other one, which on a
	// pull it always does.
	//
	// No roster read goes with it: the payment names both agents by BIC, so
	// scheme.Direction picks between two values the clearing house is already
	// holding.
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

	// GetRosterEntryByBIC is the one lookup on any of these three interfaces
	// that crosses nothing. Every other roster read here starts from a
	// ParticipantID and has to go through the bank's own row to reach an address
	// (see payment.Network.GetRosterEntry); this one starts from the BIC,
	// because an instruction names a counterparty's agent by address and by
	// nothing else. See Mesh.instruct, which asks it before either leg posts.
	GetRosterEntryByBIC(ctx context.Context, bic iso20022.BIC) (payment.RosterEntry, error)
}

// settlementOps is the central bank's view: what a settlement handler may
// reach.
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
// The limit these interfaces have and cannot lose: they narrow by METHOD, and
// every ledger.Tx method takes its book as an ordinary argument, so a handler
// holding any posting method at all can post in any book. The recorder in
// books_test.go is what watches for that.
//
// SettleCycleTx and SettleReturnTx each post in the central bank's own book and
// in no member's, and every customer leg on either path is made by the bank
// whose customer it moves, from a message.
// TestWhichBooksTheCentralBankReachesWhenItSettles and
// TestWhichBooksAReturnReaches measure both rather than assuming either.
//
// SettleReturn hands back statements for the same reason SettleCycle does, and
// they are the same kind of thing: what each member's reserve account did. What
// differs is only the reference they carry — a cycle id at a cut-off, a payment
// id here — which is why payment.SettlementAdvice is keyed by a reference
// rather than by a cycle.
type settlementOps interface {
	// SettleCycle takes the LEGS as well as the cycle id, which is the shape
	// SettleReturn below has and for the same reason: everything this
	// institution acts on comes off the message. The positions and the asset are
	// on the pacs.009 (payment.ReadSettlement), and "has this cut-off been
	// reached" is answered out of this agent's own settlement register — the
	// cycle is the clearing house's row, in a database this one does not hold.
	// See payment.SettleCycleTx and positionsIn.
	//
	// It hands back the STATEMENTS beside the settlement, because the closing
	// balance each carries is a claim about a moment inside the unit of work and
	// cannot be re-read after it. Sending them is this actor's, not the domain's:
	// see centralBank.advise.
	SettleCycle(ctx context.Context, id payment.CycleID, legs []payment.SettlementLeg) (payment.Settlement, []payment.SettlementStatement, error)

	// SettleReturn takes the INSTRUCTION rather than a payment id: everything it
	// acts on came off the pacs.004 (payment.ReadReturn), because a settlement
	// agent holds no payment rows and never saw the payment clear. Nothing on
	// this interface turns a payment id into a payment, here or at a cut-off.
	SettleReturn(ctx context.Context, in payment.ReturnInstruction) ([]payment.SettlementStatement, error)

	// ReceiveLodgement is the fourth: a member has asked this institution to
	// credit the member's own reserve account, and this institution posts Debit
	// Settlement Assets / Credit Reserve: <member> in its own book. It is on
	// this interface and on no other, so a bank handler cannot NAME it, and the
	// only way a member can cause this posting is by sending a camt.050.
	//
	// It takes the INSTRUCTION rather than a bank, for the reason
	// OpenSettlementAccount and SettleReturn both do: everything it acts on came
	// off the message (payment.ReadLodgement), because a settlement agent holds no
	// roster and has never heard of this system's bank ids. It is told a BIC, an
	// account number, an asset and an amount — and it trusts the BIC and the
	// amount and CHECKS the account number against its own row, which
	// payment.ReceiveLodgementTx sets out.
	ReceiveLodgement(ctx context.Context, in payment.LodgementInstruction) (payment.LodgementReceipt, error)
}

// *payment.Network satisfies all three, and these assertions are what keep that
// true: a method added to one of the interfaces above that the Network does not
// have fails the build here rather than at the handler that wanted it.
var (
	_ bankOps       = (*payment.Network)(nil)
	_ csmOps        = (*payment.Network)(nil)
	_ settlementOps = (*payment.Network)(nil)
)
