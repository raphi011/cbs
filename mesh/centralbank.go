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
// It never sees an individual payment, and nothing in settlementOps would let it
// look one up.
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
// # It holds a settlementOps, which is one method wide
//
// SettleCycle is on that interface and on no other, so a bank handler or a
// clearing-house handler cannot NAME it. That is what these interfaces narrow,
// and the whole of what they narrow — it is not a ban on those handlers moving
// money. GetParticipant is on both of the other two and returns a value carrying
// live ledger and deposit handles bound to whichever bank it names
// (Network.bind), and a member bank's ledger is exactly where the mirror and
// creditor legs below go, so posting them is reachable from either of those
// handlers through a method each legitimately holds. The recorder in
// books_test.go is what watches for that, here as everywhere else in this
// package; see the note on bankOps in ops.go for the whole of the hole.
//
// The method behind this interface also reaches much further than one method
// suggests. Settlement posts in every participant's book as well as the central
// bank's, which is what SettleCycleTx is; see
// TestWhichBooksTheCentralBankReachesWhenItSettles for the measurement.
type centralBank struct {
	m   *Mesh
	ops settlementOps
	bic iso20022.BIC
}

// handle dispatches on the message that arrived. See bank.handle, which has the
// same shape and the same reason for taking the sender as an argument.
//
// One arm, because one message definition is addressed to this institution. A
// pacs.008 or a pacs.003 arriving here would be a customer payment sent to the
// settlement agent, which no actor in this mesh does and which this actor could
// not act on; it becomes a dead letter rather than a shrug, for the reason
// bank.handle's default gives.
func (cb *centralBank) handle(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return cb.m.answerUnreadable(cb.bic, from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs009:
		return cb.receiveSettlement(ctx, from, env.AppHdr, doc)
	default:
		return fmt.Errorf("mesh: %s has no handler for %s", cb.bic, env.AppHdr.MsgDefIdr)
	}
}

// receiveSettlement is the central bank answering a settlement instruction:
// ACSC, or RJCT with the code its own refusal maps to.
//
// # AM04 is the answer this task exists to make expressible
//
// A net payer whose reserve cannot cover its position is refused by the ledger,
// inside SettleCycleTx's single unit of work, so nothing is posted anywhere and
// the whole batch fails — which is what a settlement window is. Before the mesh
// that refusal was a Go error returned to whoever clicked settle. It is now
// AM04 on the wire, addressed to the clearing house, which is the party that
// can act on it: it holds the cycle, and it is the one that would re-present or
// unwind.
//
// The code comes from payment.ReasonFor, which maps ledger.ErrInsufficientBalance
// to AM04 through borrowedReasons — the same route deposit.ErrInsufficientAvailable
// takes for a customer's empty account. Two layers, one code, and it is the
// right one both times: "the account cannot cover this".
//
// # Two errors are dead-lettered instead, and never answered
//
// A queue redelivers, so a settlement instruction can arrive twice. The second
// copy names a cycle this network has already settled, and SettleCycleTx refuses
// it with ErrCycleNotClosed — a statement about THIS system's state and not
// about the sender's message. payment's reasonTable gives it the EMPTY code for
// exactly that reason, and ReasonFor would turn it into MS03 and tell the
// clearing house that a cycle which in fact settled was rejected. The same goes
// for ErrInvalidStateTransition, which SettleCycleTx produces when a payment in
// the batch is not Cleared. Both become dead letters and neither is answered.
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
		return cb.answer(from, orig, notProvided, iso20022.TransactionStatusRejected, err)
	}
	id, err := cycleOf(legs)
	if err != nil {
		return cb.answer(from, orig, notProvided, iso20022.TransactionStatusRejected, err)
	}

	if _, err := cb.ops.SettleCycle(ctx, id); err != nil {
		if errors.Is(err, payment.ErrCycleNotClosed) || errors.Is(err, payment.ErrInvalidStateTransition) {
			return fmt.Errorf("mesh: %s was told to settle %s again: %w", cb.bic, id, err)
		}
		return cb.answer(from, orig, string(id), iso20022.TransactionStatusRejected, err)
	}
	return cb.answer(from, orig, string(id), iso20022.TransactionStatusSettlementCompleted, nil)
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
func (cb *centralBank) answer(to iso20022.BIC, orig payment.OriginalMessage, ref string,
	status iso20022.TransactionStatus, cause error) error {

	report := payment.TransactionStatusReport{
		EndToEndID: ref,
		TxID:       ref,
		Status:     status,
	}
	if cause != nil {
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
