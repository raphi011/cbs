package mesh

import (
	"context"
	"errors"
	"fmt"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// centralBank is the settlement agent as an actor: the third institution, and
// the only one that moves reserves.
//
// It is the shortest handler in this package, and the shortness is the whole
// distinction between clearing and settlement. The clearing house decides WHICH
// payments are in a batch and what each bank's net position is; this actor
// decides only whether those positions can be discharged, and discharges them.
// Across a CUT-OFF it never sees an individual payment: it is instructed about a
// cycle, and nothing in settlementOps turns one into the payments inside it.
//
// A RETURN is the exception, and it is not a leak in that rule. It names one
// settled payment because that is what a return is, and the bank that asked for
// it put the identifier in the message; this actor still cannot enumerate
// anything or find a payment it was not told about. What makes a return this
// institution's work is the same thing that makes a cut-off its work — reserves
// move — and no member bank may move those. See receiveReturn.
//
// # What it does NOT do, and why that is not a shortcut
//
// The pacs.009 it receives carries the legs the clearing house computed, and
// this handler reads none of the amounts in them. It takes the CYCLE the legs
// name and calls SettleCycleTx, which recomputes the whole batch from the
// cycle's own stored net positions.
//
// That is a consequence of the shared store, exactly as receiveCreditTransfer's
// discarded request is: the clearing house and the central bank read one cycle
// row, so a settlement agent that trusted the message would be trusting a copy
// of the row it is about to read anyway. A real settlement agent has no such
// row — the ancillary system's positions arrive only in the message — so it
// settles what it was TOLD, and a leg that disagreed with the sender's own books
// would be the sender's problem. Closing that gap is sub-project 8's, which is
// where the entities stop sharing a store; until then this handler names the
// gap rather than hiding it, and the message is a real instruction over a real
// wire in every other respect.
//
// # It holds a settlementOps, which is two methods wide
//
// SettleCycle and ReturnPayment are on that interface and on no other, so a
// bank handler or a clearing-house handler cannot NAME either. That is what
// these interfaces narrow, and the whole of what they narrow — it is not a ban
// on those handlers moving money. GetParticipant is on both of the other two and
// returns a value carrying live ledger and deposit handles bound to whichever
// bank it names (Network.bind), and a member bank's ledger is exactly where a
// return's compensating legs go, so posting in one is reachable from either of
// those handlers through a method each legitimately holds. The recorder in
// books_test.go is what watches for that, here as everywhere else in this
// package; see the note on bankOps in ops.go for the whole of the hole.
//
// The two methods behind this interface no longer reach the same distance.
// SETTLEMENT posts its netting transaction in the central bank's own book and in
// no member's: the mirror leg is the member's, booked from the statement advise
// sends, and the creditor leg is the payee's bank's, booked from the clearing
// house's per-payment advice. A RETURN still posts in three books of which two
// are member banks'. So this institution is the widest-reaching actor in this
// system on one flow and one of the narrowest on the other. See
// TestWhichBooksTheCentralBankReachesWhenItSettles and
// TestWhichBooksAReturnReaches for the measurements.
type centralBank struct {
	m   *Mesh
	ops settlementOps
	bic iso20022.BIC
}

// handle dispatches on the message that arrived. See bank.handle, which has the
// same shape and the same reason for taking the sender as an argument.
//
// Two arms, and they are the two ways reserves move: a cut-off's positions
// being discharged, and one settled payment being sent back. A pacs.008 or a
// pacs.003 arriving here would be a customer payment sent to the settlement
// agent, which no actor in this mesh does and which this actor could not act
// on; it becomes a dead letter rather than a shrug, for the reason bank.handle's
// default gives.
func (cb *centralBank) handle(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return cb.m.answerUnreadable(cb.bic, from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs009:
		return cb.receiveSettlement(ctx, from, env.AppHdr, doc)
	case *iso20022.Pacs004:
		return cb.receiveReturn(ctx, from, env.AppHdr, doc)
	default:
		return fmt.Errorf("mesh: %s has no handler for %s", cb.bic, env.AppHdr.MsgDefIdr)
	}
}

// receiveSettlement is the central bank answering a settlement instruction:
// ACSC, or RJCT with the code its own refusal maps to.
//
// # AM04 is the answer this task exists to make expressible
//
// A net payer whose reserve cannot cover its position is refused inside
// SettleCycleTx, and the whole batch fails with it — which is what a settlement
// window is. Before the mesh that refusal was a Go error returned to whoever
// clicked settle. It is now AM04 on the wire, addressed to the clearing house,
// which is the party that can act on it: it holds the cycle, and it is the one
// that would re-present or unwind.
//
// "Nothing is posted anywhere" is still true and no longer true for the reason it
// used to be. It once held because one unit of work spanned every book and a
// failure rolled all of them back. It holds now because the check runs ABOVE the
// netting transaction, so the central bank has written nothing of its own; and
// because advise runs only on the success path, so no member is sent a statement
// and the clearing house fans no ACSC out. There is nothing for a member to undo
// because no member was ever told.
//
// The code comes from payment.ReasonFor, which maps ledger.ErrInsufficientBalance
// to AM04 through borrowedReasons — the same route deposit.ErrInsufficientAvailable
// takes for a customer's empty account. Two layers, one code, and it is the
// right one both times: "the account cannot cover this".
//
// # One error is dead-lettered instead, and never answered
//
// A queue redelivers, so a settlement instruction can arrive twice. The second
// copy names a cycle this network has already settled, and SettleCycleTx refuses
// it with ErrCycleNotClosed — a statement about THIS system's state and not
// about the sender's message. payment's reasonTable gives it the EMPTY code for
// exactly that reason, and ReasonFor would turn it into MS03 and tell the
// clearing house that a cycle which in fact settled was rejected. So it becomes
// a dead letter and is not answered.
//
// ErrInvalidStateTransition used to be caught here beside it, because
// SettleCycleTx transitioned every payment in the batch to Settled and refused a
// batch holding one that was not Cleared. It no longer touches a payment at all
// — that leg is the payee's bank's, on the clearing house's advice — so the
// sentinel is no longer reachable from this call and the arm that caught it is
// gone with the claim.
func (cb *centralBank) receiveSettlement(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs009) error {
	body := doc.FICdtTrf
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: hdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}

	legs, err := payment.ReadSettlement(doc)
	if err != nil {
		// A count that does not match what arrived. The file parsed, so this is
		// not FF01's case — but a leg lost in transit is a bank that does not
		// get paid, and settling the survivors as if they were the whole
		// instruction is the one thing a settlement agent must never do. It is
		// answered against no cycle, because the instruction cannot be trusted
		// to name one.
		return cb.answer(from, orig, notProvided, notProvided, iso20022.TransactionStatusRejected, err)
	}
	id, err := cycleOf(legs)
	if err != nil {
		return cb.answer(from, orig, notProvided, notProvided, iso20022.TransactionStatusRejected, err)
	}

	_, statements, err := cb.ops.SettleCycle(ctx, id)
	if err != nil {
		if errors.Is(err, payment.ErrCycleNotClosed) {
			return fmt.Errorf("mesh: %s was told to settle %s again: %w", cb.bic, id, err)
		}
		return cb.answer(from, orig, string(id), string(id), iso20022.TransactionStatusRejected, err)
	}
	if err := cb.advise(statements); err != nil {
		return err
	}
	return cb.answer(from, orig, string(id), string(id), iso20022.TransactionStatusSettlementCompleted, nil)
}

// advise sends each member the statement of its own reserve account.
//
// # Why the members and not the clearing house
//
// The clearing house is the party that INSTRUCTED and is the one this actor
// answers; the members are the parties whose accounts moved, and they are not
// parties to that conversation at all. Each gets a message about its own account
// and about no other, which is the whole of what an account servicer tells an
// account holder.
//
// # After the unit of work, and before the answer
//
// After, for Mesh.Submit's reason: a statement enqueued from inside SettleCycleTx
// would be one a bank could book against a settlement the store then rolled back.
//
// Before the answer, and since 15b.3 that is load-bearing rather than tidy. The
// CREDITOR leg is now posted by the payee's bank, from the per-payment advice the
// clearing house derives FROM the ACSC this answer produces. Sending the
// statement first is what puts it in that bank's inbox before the ACSC is even
// built — see TestTheMessagesACutOffPutsOnTheWire, which states the
// happens-before chain and asserts the pair.
//
// It matters for a NET RECEIVER, and that is the case to state precisely rather
// than generally. A net receiver's mirror leg CREDITS its clearing suspense, and
// its creditor legs then draw on that suspense; so for that bank the order
// decides whether the money is there when it pays. It does not arise for the
// other two shapes: a net PAYER's mirror leg debits its suspense rather than
// funding it, and a member whose position nets to zero is sent no statement at
// all — either way that bank's suspense was funded by its own customers'
// debtor legs, long before the cut-off.
//
// The other order is not a corruption, and saying why is the point of stating
// this at all. Suspense is a Liability and the ledger does not guard those
// against going negative, so a net receiver that paid its customer first would
// simply commit, with its suspense overdrawn until the statement arrived. For
// that interval its own books would say it had lent its customer the money,
// which is a claim about this bank's balance sheet that nothing in the cut-off
// justifies. The ordering is what stops it being said.
//
// # A failed send is not a failed settlement, and it suppresses three things
//
// The reserves have moved and the cycle is Settled: that is final, and this
// actor cannot unsay it. So a send that fails comes back as an error to the
// caller — which in this transport reaches Drain as a dead letter — rather than
// being retried or swallowed. Deliberately no retry: see below on why the path
// is unreachable, and untested machinery for an unreachable failure is worse
// than a stated limitation.
//
// What the failure costs is wider than the bank that could not be reached, and
// all three parts belong in one place because a reader who finds only the first
// will under-estimate the second and third:
//
//  1. The unreachable member is never advised, so its advice row is ABSENT — not
//     even at Advised, since nothing at that bank ever ran. That is the
//     unreconciled position in its most visible form.
//  2. This returns on the FIRST failing send, so every member AFTER it in the
//     statement order is never advised either, for a reason that has nothing to
//     do with that member.
//  3. cb.answer never runs, so the clearing house is never told ACSC and
//     csm.tellSettled's per-payment fan-out never happens. Every bank in the
//     cycle — INCLUDING the ones successfully advised — is left holding an
//     instruction it believes outstanding, on a payment the domain has already
//     marked Settled. That is the largest of the three and the least visible.
//
// None of it is reachable in this transport. Mesh.send fails in exactly three
// ways — a message that will not marshal, a BIC with no actor, and an actor that
// has been stopped — and none of them occurs for a member of a live roster whose
// statement the domain has just built. The mesh's own doc says why more
// generally: delivery here is exactly-once and in order, because the transport
// is a queue inside one process. It becomes reachable the moment the transport
// can lose a message, which is every real one. Task 19's reconciliation is what
// makes any of the three detectable from inside the system.
func (cb *centralBank) advise(statements []payment.SettlementStatement) error {
	for _, st := range statements {
		env, err := payment.StatementMessage(st, payment.MessageContext{
			From:  cb.bic,
			To:    st.Agent,
			MsgID: cb.m.nextMsgID(cb.bic),
			Now:   cb.m.now(),
		})
		if err != nil {
			return fmt.Errorf("mesh: %s could not build the statement for %s: %w", cb.bic, st.Agent, err)
		}
		if err := cb.m.send(cb.bic, st.Agent, env); err != nil {
			return fmt.Errorf("mesh: %s settled %s and could not tell %s: %w", cb.bic, st.CycleID, st.Agent, err)
		}
	}
	return nil
}

// receiveReturn is the central bank executing a return: the R-transaction, and
// the second of the two things that move reserves here.
//
// # Why this actor and not a bank
//
// ReturnPaymentTx posts THREE compensating transactions in one unit of work —
// the payer refunded at their own bank, the payee clawed back at theirs, and
// the reserve movement reversed between the two settlement accounts here — and
// they commit together or not at all. The middle one is central-bank money, so
// no member bank could make it; and splitting the three across three actors
// would mean a payer refunded against a payee who was not debited, which is the
// atomicity a settlement window exists to provide. payment/doc.go already
// records the consequence: returns settle immediately in this system rather
// than being cleared and netted in a later R-cycle. A return therefore IS a
// settlement act, and it belongs where settlement does.
//
// The price is the same one receiveSettlement pays and it is worth naming
// again: two of those three postings land in member banks' books, made by this
// handler. In a real network the pacs.004 would travel bank to bank and each
// would post its own leg. Under one store this actor does all three, and
// TestWhichBooksAReturnReaches measures exactly that rather than assuming it.
//
// # One return per message, and the sender's own count must agree
//
// A pacs.004 can carry many, and this system's returning banks send one. Two
// checks, stated separately because they refuse different things and a bank
// reading the answer should be able to tell which. A message failing either is
// refused WHOLE rather than half-executed, for cycleOf's reason one message
// definition over: returning the first and dropping the rest would leave
// payments somebody was told had been sent back and never were, with nothing
// anywhere recording it.
//
// The first is the count that ARRIVED: more than one return is more than this
// actor acts on. The second is the count the sender CLAIMED — the same check
// payment.ReadSettlement makes on a settlement instruction, and here for the
// same reason. A transaction lost in transit is a payer who never gets their
// money back, and a receiver that acted on the survivors would be answering for
// a message it did not have.
//
// # What is answered, and what is dead-lettered
//
// A redelivered return names a payment this network has already returned, and
// ReturnPaymentTx refuses it with ErrInvalidStateTransition — a statement about
// THIS system's state and not about the sender's message. payment's reasonTable
// gives it the empty code for exactly that reason, and ReasonFor would turn it
// into MS03 and tell the returning bank that a return which in fact happened
// was rejected. Dead letter, and no pacs.002; it is the same discrimination
// receiveSettlement makes.
//
// Everything else the domain refuses is answered, with the code ReasonFor maps
// it to, because a refusal a counterparty can act on is completed work rather
// than a defect. ErrSchemeUnsupportedReturn is the one that belongs to this
// operation alone — a scheme whose rule book has no return — and payment's
// reasonTable maps it to MS03, "unspecified", which is what the external code
// set has for a condition the scheme does not contemplate. No scheme in this
// repository forbids returns (payment.SCT.AllowsReturn and SDD.AllowsReturn
// both report true), so nothing here provokes it.
//
// With one exception, which is a limit of this handler rather than a decision:
// a return that names no payment cannot be answered at all. OrgnlTxId is
// optional in the schema — iso20022's ReturnTransaction.validate accepts a
// transaction that refers back by OrgnlEndToEndId alone — and the pacs.002 this
// actor would send quotes only what it was given, so a report with neither
// reference fails to marshal and the refusal becomes a dead letter. No actor in
// this mesh sends such a message; TestAReturnThatNamesNoPaymentCannotBeAnswered
// injects one and pins what happens rather than leaving it to be discovered.
func (cb *centralBank) receiveReturn(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs004) error {
	body := doc.PmtRtr
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: hdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	// Unmarshal refuses a pacs.004 with no transactions (iso20022's
	// PaymentReturn.validate), so there is always a first one to answer about.
	first := body.TxInf[0]
	if n := len(body.TxInf); n != 1 {
		return cb.answer(from, orig, returnedEndToEnd(first), first.OrgnlTxId, iso20022.TransactionStatusRejected,
			fmt.Errorf("this settlement agent returns one payment per message; TxInf carries %d", n))
	}
	if said := body.GrpHdr.NbOfTxs; said != "1" {
		return cb.answer(from, orig, returnedEndToEnd(first), first.OrgnlTxId, iso20022.TransactionStatusRejected,
			fmt.Errorf("GrpHdr/NbOfTxs says %q and one transaction arrived; a return lost in transit is a payer who is not repaid", said))
	}

	id := payment.PaymentID(first.OrgnlTxId)
	if _, err := cb.ops.ReturnPayment(ctx, id, returnReason(first.RtrRsnInf)); err != nil {
		if errors.Is(err, payment.ErrInvalidStateTransition) {
			return fmt.Errorf("mesh: %s was told to return %s again: %w", cb.bic, id, err)
		}
		return cb.answer(from, orig, returnedEndToEnd(first), string(id), iso20022.TransactionStatusRejected, err)
	}
	return cb.answer(from, orig, returnedEndToEnd(first), string(id), iso20022.TransactionStatusSettlementCompleted, nil)
}

// returnedEndToEnd is the payer's own reference for a returned payment, as the
// RETURNING BANK quoted it, or the EPC's convention where there is none.
//
// Quoted back rather than derived, because that is what a bank matches its
// outstanding instruction against — csm.endToEndOf makes the same convention on
// the other side of the mesh, and a status quoting the payment's id in this
// element would match nothing the returning bank ever sent.
func returnedEndToEnd(tx iso20022.ReturnTransaction) string {
	if tx.OrgnlEndToEndId == "" {
		return notProvided
	}
	return tx.OrgnlEndToEndId
}

// returnReason is what a return is described as where a CUSTOMER's money moves:
// the reason the returning bank gave, code and text.
//
// Two of the three postings, not three. payment.ReturnPaymentTx writes this
// into the payer's refund and the payee's clawback, and describes the reserve
// reversal between the two banks as the settlement it is — a bank's own
// position moving carries no customer's reason.
// TestTheReturnsReasonTravelsFromTheAskingBankToTheLedgers asserts both halves
// of that, including the one the reason does not reach.
//
// Both arms of the choice are read, because both are legal — iso20022's
// ReturnReasonChoice requires exactly one of a code and a proprietary text, and
// refuses a return that has neither, so what arrives is one or the other. The
// nil case is a caller's guard rather than a message: RtrRsnInf is mandatory in
// a pacs.004 that has been through Unmarshal.
//
// It is a separate function from rejectionText, over a shared join, because the
// two read different external code sets — iso20022.ReturnReason and
// iso20022.StatusReason — which pacs004.go keeps as separate types precisely so
// that a rejection reason cannot be used as a return reason with nothing to
// notice.
func returnReason(info *iso20022.ReturnReasonInformation) string {
	if info == nil {
		return "returned"
	}
	var code string
	switch {
	case info.Rsn.Cd != nil:
		code = string(*info.Rsn.Cd)
	case info.Rsn.Prtry != nil:
		code = *info.Rsn.Prtry
	}
	return codeAndText(code, info.AddtlInf, "returned")
}

// cycleOf is which closed cycle an instruction discharges, taken from the legs
// themselves.
//
// Every leg of one instruction must name the same cycle, and this REFUSES a
// message whose legs disagree rather than settling the first cycle it sees.
// payment.SettlementLeg carries its own reference precisely because a pacs.009
// is capable of carrying legs from several cycles at once, and a real settlement
// agent would settle each of them; this system's clearing house emits one cycle
// per instruction (see csm.instructSettlement), so an instruction naming two is
// one this actor has no rule for. Discharging one and dropping the other would
// leave a closed cycle that nobody ever settles and nobody ever hears about.
//
// An instruction with no legs cannot occur on the wire — payment.SettlementMessage
// refuses to build one and iso20022's own validation refuses to parse one — so
// the empty case here is a guard on a caller, not a message.
func cycleOf(legs []payment.SettlementLeg) (payment.CycleID, error) {
	if len(legs) == 0 {
		return "", fmt.Errorf("payment: a settlement instruction with no legs names no cycle")
	}
	id := payment.CycleID(legs[0].Reference)
	for _, leg := range legs[1:] {
		if payment.CycleID(leg.Reference) != id {
			return "", fmt.Errorf("payment: this settlement instruction names both %s and %s; one instruction discharges one cycle",
				id, leg.Reference)
		}
	}
	return id, nil
}

// answer sends the pacs.002 back to whoever sent the instruction.
//
// The transaction it reports on is the CYCLE, not a payment: that is what a
// pacs.009 instructs and what the central bank decided about. The clearing house
// is the party that turns it back into per-payment news, because it is the one
// that knows which payments are in the batch — see csm.receiveSettlementStatus.
//
// Back to the SENDER, for bank.answer's reason: the banks whose reserves just
// moved are not parties to this conversation and were never given this actor's
// address to expect a message from.
//
// # Two references, not one
//
// e2e and txid are separate parameters because on the return path they are
// different values, and quoting the payment id as both broke the convention
// every other per-payment status in this mesh follows (csm.endToEndOf): a bank
// matches an answer to its instruction by comparing what it SENT with what came
// back, and a payment with no client reference travels as NOTPROVIDED, not as
// its own id. On the settlement path they are one value, because a CYCLE has no
// end-to-end reference at all and the clearing house matches on the transaction
// id.
//
// # A cause forces RJCT
//
// bank.answer does the same, and the drift was a trap rather than a live
// defect: a cause passed beside SettlementCompleted set a code and a text that
// statusReasonOf then dropped, because a pacs.002 carries StsRsnInf only for a
// rejection. The message would have gone out saying everything was fine with
// the reason silently deleted. There is no caller that does it; there is now no
// way to.
func (cb *centralBank) answer(to iso20022.BIC, orig payment.OriginalMessage, e2e, txid string,
	status iso20022.TransactionStatus, cause error) error {

	report := payment.TransactionStatusReport{
		EndToEndID: e2e,
		TxID:       txid,
		Status:     status,
	}
	if cause != nil {
		report.Status = iso20022.TransactionStatusRejected
		report.Code = payment.ReasonFor(cause)
		report.Text = cause.Error()
	}
	env, err := payment.StatusMessage(orig, []payment.TransactionStatusReport{report}, payment.MessageContext{
		From:  cb.bic,
		To:    to,
		MsgID: cb.m.nextMsgID(cb.bic),
		Now:   cb.m.now(),
	})
	if err != nil {
		return errors.Join(fmt.Errorf("mesh: %s could not build its pacs.002 for %s: %w", cb.bic, to, err), cause)
	}
	// The cause is NOT returned once it has been answered, for the reason
	// bank.answer gives: a refusal the counterparty was told about is completed
	// work, and returning it as well would make every AM04 a dead letter too.
	return cb.m.send(cb.bic, to, env)
}
