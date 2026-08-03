package mesh

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// csm is the clearing house as an actor.
//
// It sits between two banks that never address each other. Everything a bank
// learns about the far side arrives through here, and everything this type does
// falls out of that: it ROUTES an instruction onward, and it CLEARS the answer —
// taking the payment into a cycle, or rejecting it — and passes that answer back
// to the bank that started it.
//
// The routing is where the two flows differ and the clearing is where they do
// not. A credit transfer goes to the agent named as the creditor's, a collection
// to the agent named as the debtor's, because a push travels towards the money's
// destination and a pull towards its source. What comes back is a pacs.002
// either way, and this actor treats it the same way either way.
//
// # It also sits between the banks and the settlement agent
//
// Task 12 gave it a third job and a second counterparty. Reaching a cut-off nets
// the batch and then INSTRUCTS the central bank to discharge the positions, in a
// pacs.009, because moving reserves is not something a clearing house may do.
// The answer to that instruction comes back here rather than to any bank, and
// this actor turns it into per-payment news — which it can and the central bank
// cannot, because it is the one that knows which payments are in the batch. See
// closeCycle and receiveSettlementStatus.
//
// # And it carries returns, without being party to them
//
// Task 13 gave it a fourth job, and it is the one where this actor does least.
// A pacs.004 from a member bank is handed straight to the central bank, because
// a return moves reserves; the answer comes back here and is passed to the bank
// that asked. Nothing is cleared, nothing is netted, no cycle is touched — a
// return in this system is final the moment the settlement agent posts it. See
// relayReturn and receiveReturnStatus.
//
// It holds a csmOps: nothing about clearing moves money, and that is what makes
// clearing and settlement different jobs. What that does NOT amount to is a
// compile-time ban on posting — GetParticipant hands back live ledger and deposit
// handles bound to the bank it names, so a clearing house that wanted another
// bank's book has one. See the note on bankOps in ops.go for the whole of that
// hole. TestTheCSMTouchesOnlyTheNetworkBook is what actually holds this actor to
// it, and TestTheCSMStillTouchesOnlyTheNetworkBookWhenItSettles extends that
// over the cut-off and the settlement conversation.
type csm struct {
	m   *Mesh
	ops csmOps
	bic iso20022.BIC
}

// handle dispatches on the message that arrived. See bank.handle, which has the
// same shape and the same reason for taking the sender as an argument.
func (c *csm) handle(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return c.m.answerUnreadable(c.bic, from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs008:
		return c.relayCreditTransfer(from, env, doc)
	case *iso20022.Pacs003:
		return c.relayDirectDebit(from, env, doc)
	case *iso20022.Pacs004:
		return c.relayReturn(from, env, doc)
	case *iso20022.Pacs002:
		// Three kinds of status arrive here, and it takes two questions to tell
		// them apart.
		//
		// The SENDER separates a member bank's from the settlement agent's. A
		// bank's pacs.002 answers an instruction about one customer payment;
		// the central bank's answers something this actor asked IT for. By BIC
		// rather than by trying a lookup and seeing what happens, because the
		// central bank is not a member of this network: it holds no participant
		// row, it never submits and it is never a payment's agent, so the only
		// messages it ever sends this actor are answers to what this actor
		// asked it.
		//
		// What it was asked separates the settlement agent's two. A settlement
		// answer is about a whole CYCLE and its OrgnlTxId is a cycle id; a
		// return answer is about one PAYMENT. Reading either as the other would
		// look an identifier up in the wrong table. The message says which —
		// OrgnlMsgNmId, the definition it is answering — so this is read off the
		// wire rather than guessed at by trying both. See returnMsgDef.
		switch {
		case from != c.m.cfg.CentralBankBIC:
			return c.receiveStatus(ctx, from, doc)
		case isAbout(doc, returnMsgDef):
			return c.receiveReturnStatus(ctx, from, doc)
		default:
			return c.receiveSettlementStatus(ctx, from, doc)
		}
	default:
		return fmt.Errorf("mesh: %s has no handler for %s", c.bic, env.AppHdr.MsgDefIdr)
	}
}

// relayCreditTransfer hands a credit transfer on to the CREDITOR's agent: the
// bank that holds the payee, because a push travels towards the money's
// destination.
func (c *csm) relayCreditTransfer(from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs008) error {
	body := doc.FIToFICstmrCdtTrf
	ref := body.CdtTrfTxInf[0].PmtId
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: env.AppHdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	if n := len(body.CdtTrfTxInf); n != 1 {
		return c.refuseBulk(from, orig, ref, "CdtTrfTxInf", n)
	}
	return c.relay(from, env, doc, orig, ref, body.CdtTrfTxInf[0].CdtrAgt.FinInstnId.BICFI)
}

// relayDirectDebit hands a collection on to the DEBTOR's agent: the bank that
// holds the payer, because a pull travels towards the money's source.
//
// One element different from relayCreditTransfer, and it is the whole direction
// of the payment. Routing a pacs.003 by CdtrAgt would send the collection back
// to the bank that sent it, which would answer its own instruction — and the
// resolution inside DirectDebitRequest would succeed while it did, because both
// parties resolve by address whoever is asking.
func (c *csm) relayDirectDebit(from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs003) error {
	body := doc.FIToFICstmrDrctDbt
	ref := body.DrctDbtTxInf[0].PmtId
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: env.AppHdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	if n := len(body.DrctDbtTxInf); n != 1 {
		return c.refuseBulk(from, orig, ref, "DrctDbtTxInf", n)
	}
	return c.relay(from, env, doc, orig, ref, body.DrctDbtTxInf[0].DbtrAgt.FinInstnId.BICFI)
}

// relayReturn hands a return on to the SETTLEMENT AGENT.
//
// Not to a bank, and that is the whole routing decision of this flow. A return
// moves central-bank reserves back — payment.SettleReturnTx reverses the
// movement between the two banks' settlement accounts — and moving reserves is
// the settlement agent's act, as it is at a cut-off. So
// the destination is a fact about the MESSAGE DEFINITION rather than about
// anything inside the message, which is why this hop reads no element to route
// by and no store either. A pacs.008 or a pacs.003 names the agent it is for; a
// pacs.004 names no parties at all (see payment.ReturnMessage), and it does not
// need to.
//
// It still goes THROUGH the clearing house rather than from the bank to the
// central bank directly. A member bank in this mesh addresses the clearing
// house and nothing else — that is the shape every other flow has, and the
// routing table lives in one actor for the same reason RC01 can only be said
// here. What the clearing house does with a return is carry it: it takes it
// into no cycle, nets nothing, and posts nothing.
//
// A bulk return is NOT refused here, unlike a bulk pacs.008 or pacs.003, and
// the difference is what refuseBulk is actually about: several transactions in
// an instruction mean several counterparty agents and therefore several
// destinations, and this actor has one routing decision per message to make.
// Every return in one message has the same destination, so routing has no
// objection to make. The settlement agent has one, and makes it — see
// centralBank.receiveReturn.
func (c *csm) relayReturn(from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs004) error {
	body := doc.PmtRtr
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: env.AppHdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	// Unmarshal refuses a pacs.004 with no transactions (iso20022's
	// PaymentReturn.validate), so there is always a first one to refer back by.
	first := body.TxInf[0]
	ref := iso20022.PaymentIdentification{
		EndToEndId: first.OrgnlEndToEndId,
		TxId:       first.OrgnlTxId,
	}
	return c.relay(from, env, doc, orig, ref, c.m.cfg.CentralBankBIC)
}

// refuseBulk is this system's one-payment-per-message limit, stated to the
// sender.
//
// A bulk file names several counterparty agents and therefore several
// destinations, and this clearing house has one routing decision per message to
// make. Refused rather than split: sending the first of five would drop four.
func (c *csm) refuseBulk(from iso20022.BIC, orig payment.OriginalMessage, ref iso20022.PaymentIdentification, element string, n int) error {
	return c.answer(from, orig, ref, iso20022.StatusReasonNotSpecifiedAgentGenerated,
		fmt.Sprintf("this clearing house routes one payment per message; %s carries %d", element, n))
}

// relay forwards an instruction to the agent the message named, whichever
// direction it runs in.
//
// It reads no store, and that is a property worth keeping rather than an
// accident of how little there is to do. The address it routes by came out of
// the message — CdtrAgt for a push, DbtrAgt for a pull — so a clearing house
// that looked the payment up to decide where to send it would be one that could
// not route a message about a payment it does not hold. Which is every message,
// in a real network.
//
// A RETURN is routed by neither element, because a pacs.004 carries no agent
// and needs none: its destination is the settlement agent, which follows from
// the message DEFINITION. Its caller passes that address in rather than reading
// one out (see relayReturn), which keeps this function's property intact — the
// store is untouched either way.
//
// The DOCUMENT travels unchanged and only the header is replaced. That is what
// relaying is: the header says who is handing this to whom and is this hop's,
// the document is what the submitting bank said and is not the clearing house's
// to rewrite. Its GrpHdr/MsgId therefore stays that bank's, which is what the
// receiving bank quotes back as OrgnlMsgId and what lets the answer be matched
// to the original all the way home.
func (c *csm) relay(from iso20022.BIC, env iso20022.Envelope, doc iso20022.Document,
	orig payment.OriginalMessage, ref iso20022.PaymentIdentification, to iso20022.BIC) error {

	relayed := iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: c.m.nextMsgID(c.bic),
			MsgDefIdr: env.AppHdr.MsgDefIdr,
			CreDt:     iso20022.ISODateTime{Time: c.m.now()},
		},
		Document: doc,
	}
	err := c.m.send(c.bic, to, relayed)
	if errors.Is(err, ErrUnknownBIC) {
		// RC01, and it is this actor that says it because it is the only one
		// holding the routing table. A bank cannot know that a BIC is unroutable;
		// it can only know that its message was not answered.
		return c.answer(from, orig, ref, iso20022.StatusReasonBankIdentifierIncorrect, err.Error())
	}
	return err
}

// receiveStatus is the clearing house acting on what the payee's bank said, and
// then telling the payer's bank.
//
// This is where a payment becomes Accepted or Rejected, and both are the
// clearing house's act rather than either bank's:
//
//   - ACCP: the payment goes into the open cycle for its scheme. Which cycle a
//     payment clears in is not a bank's decision, so ErrCycleNotOpen is not a
//     bank's refusal either — it comes back as TM01, "invalid cut-off time",
//     which is exactly what "there is no window open" means.
//   - RJCT: the payment is rejected with the code the payee's bank chose, and
//     dropped from any cycle it had reached.
//
// # Who is told, and why it can be two banks
//
// The answer goes to the bank that SUBMITTED, looked up from the payment rather
// than taken from the message's sender — the sender is the bank that just
// decided, and the submitter is a third party neither of them addressed. Which
// bank submitted is the scheme's direction and nothing else: the payer's bank
// pushed, or the payee's bank pulled. See submitterOf.
//
// A REJECTION can need a second recipient, and only on a pull. By the time the
// clearing house refuses a collection, the payer's bank has already posted the
// debtor leg — that is what accepting a collection means — and that bank is not
// the one that submitted. A rejection that never reached it would leave the
// payer's money sitting in a clearing suspense against a payment this network
// records as rejected: nobody would ever settle it and nobody would ever give it
// back. So the payer's bank is told as well, and the condition is exactly the
// money: a posted debtor leg, at a bank that is not the submitter.
//
// On a push the two are the same bank and there is one message, unchanged from
// Task 10. On a pull the payer's bank refused ITSELF — AM04, which its funds
// check makes before the leg is posted — there is no leg to give back, so there
// is one message again, and TestARejectedCollectionIsAnsweredToThePayeesBank
// counts it.
//
// A refusal that never reached the payer's bank at all — an agent this mesh
// cannot route to, a bulk file — does not arrive here in the first place: both
// are answered by c.answer, from relay and refuseBulk, before any payment has
// been looked up.
//
// # This handler does two writes, and the second may not happen
//
// Rejecting is the clearing house's half; reversing the payer's debit is the
// payer's bank's, and it happens later, in another actor, in another unit of
// work. RejectAtCSMTx's doc names that seam and says the mesh is where it stops
// being hidden: if the reversal fails there is nobody to answer, so it becomes a
// dead letter and Drain returns it. The system fails a test rather than quietly
// telling a payer their money is back.
func (c *csm) receiveStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A status that names no transaction. Nothing in this flow produces
			// one for the clearing house — a bank's answer always quotes the
			// payment it is about — so it is a message this actor cannot act on
			// and has nobody to ask about.
			return fmt.Errorf("mesh: %s got a status from %s naming no transaction", c.bic, from)
		}
		id := payment.PaymentID(r.TxID)

		var decided payment.Payment
		var err error
		if r.Status == iso20022.TransactionStatusRejected {
			decided, err = c.ops.RejectAtCSM(ctx, id, r.Code, r.Text)
		} else {
			decided, r, err = c.clear(ctx, id, r)
		}
		if err != nil {
			// Already answered. The queue redelivers, and a second copy of an
			// acceptance arrives at a payment the clearing house has already
			// taken into a cycle — or a second rejection at one already rejected.
			// Either way this sentinel says the state machine forbids the move,
			// which is a statement about THIS system and not about the sender's
			// message: payment's reasonTable gives it the empty code for exactly
			// that reason, and ReasonFor would turn it into MS03 and reject a
			// payment that was in fact accepted. Dead letter, and no pacs.002.
			if errors.Is(err, payment.ErrInvalidStateTransition) {
				return fmt.Errorf("mesh: %s was told about %s again: %w", c.bic, id, err)
			}
			return err
		}

		if err := c.tell(ctx, decided, orig, r); err != nil {
			return err
		}
	}
	return nil
}

// tell sends the decision to every bank that has to have it. See the note on
// receiveStatus for which banks those are and why a rejected collection has two.
//
// # The MONEY message goes first, and neither is skipped because the other
// failed
//
// The two messages are not the same kind of news. The submitter's is the answer
// to a question it asked. The payer's bank's — sent only when a rejection
// leaves it holding a posted debtor leg it did not submit, which is a rejected
// collection and nothing else — is what makes it give a customer their money
// back. Sending the informational one first and returning on its error left the
// refund unattempted, and that is reachable: the send is answered with
// ErrUnknownBIC during Reset's ForgetBanks/JoinRoster window, and GetParticipant
// fails on a store error. A payer whose money is in a suspense against a
// payment this network records as Rejected has nobody left to notice.
//
// So the refund message is attempted first and both are attempted regardless,
// with the failures joined. Two banks that cannot be addressed produce two
// errors and one dead letter carrying both, which is more than the caller could
// have learnt before.
func (c *csm) tell(ctx context.Context, p payment.Payment, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	scheme, ok := c.ops.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("mesh: %s decided %s and holds no %q scheme to say who submitted it: %w",
			c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
	}
	submitterID := submitterOf(scheme, p.Debtor, p.Creditor).Participant

	var errs []error
	// The payer's bank, when it is holding money against a payment that has just
	// been rejected and is not the bank waiting for the answer. Only a pull
	// reaches this — on a push the two are one participant and one message.
	var refunded iso20022.BIC
	if r.Status == iso20022.TransactionStatusRejected && p.DebtorLegTx != "" && p.Debtor.Participant != submitterID {
		if debtor, err := c.ops.GetParticipant(ctx, p.Debtor.Participant); err != nil {
			errs = append(errs, fmt.Errorf("mesh: %s cannot address the payer's bank for %s: %w", c.bic, p.ID, err))
		} else {
			refunded = debtor.BIC
			if err := c.forward(debtor.BIC, orig, r); err != nil {
				errs = append(errs, err)
			}
		}
	}

	// The bank that submitted, which is always told. The BIC comparison is a
	// second guard behind the participant one above: two participants on one
	// address cannot be admitted (Mesh.AddBank refuses it), and a duplicate
	// pacs.002 would be a second acceptance at a bank that already has one.
	if submitter, err := c.ops.GetParticipant(ctx, submitterID); err != nil {
		errs = append(errs, fmt.Errorf("mesh: %s cannot address the bank that submitted %s: %w", c.bic, p.ID, err))
	} else if submitter.BIC != refunded {
		if err := c.forward(submitter.BIC, orig, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// clear takes an accepted payment into the open cycle for its scheme, and turns
// a refusal to do so into the rejection the submitting bank will be sent.
//
// The report comes back changed on that path, and that is the point: what the
// answering bank said was ACCP, and what the submitting bank must be told is
// RJCT with the clearing house's own reason. Relaying the acceptance while
// rejecting the payment would tell the bank that instructed this the opposite of
// what happened.
func (c *csm) clear(ctx context.Context, id payment.PaymentID, r payment.TransactionStatusReport) (payment.Payment, payment.TransactionStatusReport, error) {
	p, err := c.ops.AcceptAtCSM(ctx, id)
	if err == nil {
		return p, r, nil
	}
	if errors.Is(err, payment.ErrInvalidStateTransition) {
		return payment.Payment{}, r, err
	}
	// A separate unit of work, because the first one rolled back. The clearing
	// house could not clear it, so the clearing house rejects it, with the code
	// its own refusal maps to — TM01 for no open cycle.
	code := payment.ReasonFor(err)
	rejected, rerr := c.ops.RejectAtCSM(ctx, id, code, err.Error())
	if rerr != nil {
		return payment.Payment{}, r, errors.Join(err, rerr)
	}
	r.Status = iso20022.TransactionStatusRejected
	r.Code = code
	r.Text = err.Error()
	return rejected, r, nil
}

// forward sends the status on to one bank. Which bank, and how many of them, is
// tell's decision.
//
// It builds a NEW pacs.002 rather than relaying the one that arrived, and the
// difference is in one element: the originator. A status report says who
// DECIDED, and what this message reports is the clearing house's decision — this
// payment is in a cycle, or this payment is rejected and out of one. The
// answering bank's own decision travelled in its own pacs.002, to this actor,
// carrying its own originator. Each hop states what that hop decided, which is
// why relaying the bytes would be wrong here and right for an instruction.
//
// The ORIGINAL message it refers back to is unchanged: that is the submitting
// bank's pacs.008 or pacs.003, which is what every hop is about and the only
// thing that bank can match on.
//
// decidedBy is the one hop where the claim above is not true, and it is passed
// in rather than inferred. See forwardDecision.
func (c *csm) forward(to iso20022.BIC, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	return c.forwardDecision(to, "", orig, r)
}

// forwardDecision is forward for a status this actor is CARRYING rather than
// making: it names the institution that decided, so the pacs.002's Orgtr does
// not say the clearing house.
//
// There is exactly one such hop — the settlement agent's answer about a return,
// passed back to the bank that asked for one — and it is worth a second entry
// point rather than a bool. csm.receiveReturnStatus's own doc says this actor
// decides nothing there, and iso20022.StatusReasonInformation says Orgtr exists
// so that a receiver does not blame the relay for the refusal. Stamping this
// actor would have a returning bank investigating a clearing house that never
// looked at its request.
func (c *csm) forwardDecision(to, decidedBy iso20022.BIC, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	env, err := payment.StatusMessage(orig, []payment.TransactionStatusReport{r}, payment.MessageContext{
		From:      c.bic,
		To:        to,
		MsgID:     c.m.nextMsgID(c.bic),
		Now:       c.m.now(),
		DecidedBy: decidedBy,
	})
	if err != nil {
		return fmt.Errorf("mesh: %s could not build its pacs.002 for %s: %w", c.bic, to, err)
	}
	return c.m.send(c.bic, to, env)
}

// reject is the clearing house declining a payment because an operator said so.
//
// It is receiveStatus's rejection arm without the message that would have
// provoked it: the same RejectAtCSM, the same tell, the same banks told. Written
// as its own three statements rather than by faking a pacs.002 and feeding it to
// the handler, because a message this actor never received is not a thing to
// invent — and because what a caller from outside the mesh needs back is the
// payment, which a handler has no way to return.
//
// It runs synchronously on the CALLER's goroutine, and sends after the unit of
// work has committed, for the reasons Mesh.Submit and closeCycle both set out. A
// pacs.002 enqueued from inside the rejection would be one the payer's bank
// could act on against a rejection the store then rolled back — and the act in
// question is handing money back to a customer.
//
// tell is what fans it out, so the answer goes exactly where a counterparty's
// rejection would have gone: to the bank that submitted the payment, and — on a
// pull whose debtor leg has already posted — to the payer's bank as well, which
// is the one holding the money. Two banks, one decision, and the same code path
// that has been carrying that since Task 11.
func (c *csm) reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

	p, err := c.ops.RejectAtCSM(ctx, id, code, text)
	if err != nil {
		return payment.Payment{}, err
	}
	r := payment.TransactionStatusReport{
		EndToEndID: endToEndOf(p),
		TxID:       string(p.ID),
		Status:     iso20022.TransactionStatusRejected,
		Code:       code,
		Text:       text,
	}
	// The rejection is a fact and the send may still fail, so the payment comes
	// back BESIDE the error rather than being swallowed — closeCycle's shape, and
	// the same half-happened outcome: a payment that is Rejected and whose payer
	// has not been given their money back.
	if err := c.tell(ctx, p, payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided}, r); err != nil {
		return p, fmt.Errorf("mesh: %s rejected %s and could not say so: %w", c.bic, p.ID, err)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// The cut-off, and the settlement it instructs
// ---------------------------------------------------------------------------

// closeCycle is the clearing house reaching a cut-off: it nets the batch, and
// then asks the central bank to discharge it.
//
// Two steps, and the seam between them is the point of this task. Netting is
// the clearing house's own act and moves nothing — CloseCycleTx posts NOTHING
// at all, it transitions each payment to Cleared and writes the positions onto
// the cycle. Discharging those positions moves central-bank reserves, which no
// clearing house may do, so the second step is a MESSAGE to another institution
// and not a call. Before the mesh the two were separate console buttons on
// separate operators, with nothing between them; the pacs.009 is what was
// missing.
//
// It runs synchronously on the CALLER's goroutine, for Mesh.Submit's reason: an
// operator reaching a cut-off has an error to be told about, then and there. And
// like Submit, the send is OUTSIDE the unit of work — a settlement instruction
// enqueued from inside CloseCycleTx would be one the central bank could act on
// against a cut-off the store then rolled back.
//
// The two failure modes are not the same, and the signature keeps them apart. A
// refused cut-off netted nothing and is the caller's answer. A cycle that closed
// and whose instruction could not be sent is HALF-HAPPENED — the payments are
// Cleared and no settlement agent has been told — so the closed cycle comes back
// beside the error rather than being swallowed. It is the same seam
// RejectAtCSMTx documents, and the console is where it shows: a cycle that is
// Closed and has no settlement.
func (c *csm) closeCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

	closed, err := c.ops.CloseCycle(ctx, id)
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	if err := c.instructSettlement(ctx, closed); err != nil {
		return closed, fmt.Errorf("mesh: %s closed %s and could not instruct settlement: %w", c.bic, closed.ID, err)
	}
	return closed, nil
}

// settle is the clearing house sending a settlement instruction AGAIN, for a
// cycle the central bank refused.
//
// It is the way out of the only state in this system that had none. A cut-off
// whose net payer is short of reserves comes back RJCT/AM04, and
// receiveSettlementStatus tells nobody — correctly, because nothing about any
// payment changed. What is left is a cycle that is Closed with no settlement, a
// batch of payments that are Cleared, every payer debited into their own bank's
// clearing suspense and every payee unpaid. Every other route is shut by a
// guard that is right: CloseCycleTx wants an open cycle, RejectAtCSMTx takes
// only an Initiated or Accepted payment, ReturnPaymentTx wants a settled one.
// Until this method existed the operator's remedy — fund the short member and
// ask again — had nothing to ask.
//
// # It re-sends the instruction rather than settling
//
// The clearing house does not discharge positions and this does not start to.
// It rebuilds the same pacs.009 from the same stored net positions and hands it
// to the settlement agent, which decides again with the reserves as they are
// now. That is why the caller gets the CYCLE back rather than a settlement, and
// why the answer is 202 at api: whether it worked this time arrives later, at
// another actor, exactly as it does after a cut-off.
//
// # Double-settling is not reachable, and it is guarded in two places
//
// This refuses anything that is not Closed, so a cycle that has already settled
// is ErrCycleNotClosed here and no second instruction is built. Two calls that
// raced past that check would each send one, and the SECOND is refused at the
// settlement agent by SettleCycleTx's own CycleClosed guard — the same refusal a
// redelivered pacs.009 gets, dead-lettered rather than answered, because
// telling the clearing house RJCT about a cycle that in fact settled would be a
// lie. Behind both of those the central bank's posting carries the idempotency
// key "<cycle>:settle", so even a third arrangement that got past the state
// machine could not post the reserves twice.
// TestReSettlingASettledCycleIsRefused walks the first guard and
// TestASecondSettlementInstructionPostsNothing the other two.
func (c *csm) settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

	cycle, err := c.ops.GetCycle(ctx, id)
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	if cycle.Status != payment.CycleClosed {
		return payment.ClearingCycle{}, fmt.Errorf("mesh: %s is %v, and an instruction to settle is only re-sent for a closed one: %w",
			id, cycle.Status, payment.ErrCycleNotClosed)
	}
	// The cycle is a fact and the send may still fail, so it comes back BESIDE
	// the error rather than being swallowed — closeCycle's shape, for the same
	// reason and with the same half-happened outcome.
	if err := c.instructSettlement(ctx, cycle); err != nil {
		return cycle, fmt.Errorf("mesh: %s could not re-instruct settlement of %s: %w", c.bic, cycle.ID, err)
	}
	return cycle, nil
}

// instructSettlement sends the closed cycle's net positions to the central bank
// as a pacs.009.
//
// A pacs.009 and not a pacs.008 because both parties to every leg are banks:
// this moves a bank's own money, not a customer's. payment.SettlementMessage's
// doc has the whole of that distinction, and the compiler holds it — Dbtr and
// Cdtr in this message are agents and a customer cannot be put in one.
//
// # One instruction, one cycle, one asset
//
// A cycle belongs to one scheme and a scheme settles in one asset, so the asset
// is read off the scheme once and every leg carries it. Two cycles in two assets
// are therefore two cut-offs and two instructions, which is what
// TestOneSettlementInstructionPerAsset measures. Nothing here would stop a
// FUTURE caller putting two cycles in one message — payment.ReadSettlement
// supports it deliberately — but this system does not, and the central bank
// refuses a message that does; see cycleOf.
//
// # A cycle that nets to nothing instructs nothing
//
// An empty cycle, or one whose members' positions all cancel, has no leg to
// send, and a pacs.009 with no transaction is not a message this codec will
// build. Silence is correct there and is not the same as a failure: there is
// nothing for a settlement agent to discharge. Every credit-transfer test in
// this package closes an untouched direct-debit cycle for exactly that reason,
// so the path is walked constantly rather than reasoned about.
func (c *csm) instructSettlement(ctx context.Context, closed payment.ClearingCycle) error {
	scheme, ok := c.ops.Scheme(closed.Scheme)
	if !ok {
		return fmt.Errorf("mesh: %s closed %s and holds no %q scheme to settle it in: %w",
			c.bic, closed.ID, closed.Scheme, payment.ErrSchemeNotFound)
	}
	legs, err := c.settlementLegs(ctx, closed, scheme.Asset())
	if err != nil {
		return err
	}
	if len(legs) == 0 {
		return nil
	}

	to := c.m.cfg.CentralBankBIC
	env, err := payment.SettlementMessage(legs, payment.MessageContext{
		From:  c.bic,
		To:    to,
		MsgID: c.m.nextMsgID(c.bic),
		Now:   c.m.now(),
	})
	if err != nil {
		return fmt.Errorf("mesh: %s could not build the settlement instruction for %s: %w", c.bic, closed.ID, err)
	}
	return c.m.send(c.bic, to, env)
}

// settlementLegs turns a cycle's net positions into the legs of an instruction:
// one per member with something to discharge, each against the CENTRAL BANK.
//
// Against the central bank and not against another member, because a
// multilateral net position has no counterparty among the banks. Three banks
// that each paid and were paid net out to one figure apiece, and the sum of
// those figures is zero — but there is no pairing of payer to payee left in
// them, which is precisely what netting destroys. Every position is therefore a
// claim on or an obligation to the settlement agent, and the message says so:
// a net payer's leg runs from that bank TO the central bank, a net receiver's
// from the central bank to it.
//
// The reference on every leg is the CYCLE, which is what the central bank reads
// to know what it is being asked to discharge. See payment.SettlementLeg.
//
// Members are visited in sorted id order rather than map order. The legs are the
// message's transactions, so map iteration would put a different byte sequence
// on the wire on every run for no reason — the same argument settlementLegsTx
// makes one layer down for the entries of the stored transaction, arrived at
// independently because these two orders are not each other's.
//
// A position of zero is left out. It nets to nothing, and a leg for it would be
// an IntrBkSttlmAmt of zero, which the codec refuses (ActiveCurrencyAndAmount
// requires a positive amount) and which would say a bank was instructed to move
// nothing.
func (c *csm) settlementLegs(ctx context.Context, closed payment.ClearingCycle, asset ledger.AssetCode) ([]payment.SettlementLeg, error) {
	cb := c.m.cfg.CentralBankBIC
	legs := make([]payment.SettlementLeg, 0, len(closed.NetPositions))
	for _, pid := range slices.Sorted(maps.Keys(closed.NetPositions)) {
		net := closed.NetPositions[pid]
		if net == 0 {
			continue
		}
		p, err := c.ops.GetParticipant(ctx, pid)
		if err != nil {
			return nil, fmt.Errorf("mesh: %s closed %s and cannot name the bank behind %s: %w", c.bic, closed.ID, pid, err)
		}
		leg := payment.SettlementLeg{
			From:      p.BIC,
			To:        cb,
			Amount:    -net,
			Asset:     asset,
			Reference: string(closed.ID),
		}
		if net > 0 {
			leg.From, leg.To, leg.Amount = cb, p.BIC, net
		}
		legs = append(legs, leg)
	}
	return legs, nil
}

// receiveSettlementStatus is the clearing house acting on what the CENTRAL BANK
// said about a settlement instruction.
//
// The two outcomes are not symmetrical, and the asymmetry is a fact about who is
// waiting for what.
//
// ACSC is news every bank in the batch has been waiting for since it submitted,
// and it is fanned out per payment — see tellSettled. Settlement is the point of
// finality: it is the moment a payee's bank has actually been paid, and before
// the mesh a submitting bank could learn it only by reading the return value of
// somebody else's call.
//
// RJCT is told to NOBODY, and that is deliberate rather than an omission. The
// batch failed whole and nothing was posted in any book: the central bank checks
// each net payer's reserve before it posts anything of its own, and it advises no
// member unless it settles, so no bank was told and no bank booked. That leaves
// every payment in the cycle exactly where the cut-off left it: Cleared, with the
// payer's money still in its own bank's clearing suspense. A bank told "rejected"
// would try to reverse a debtor leg that must not be reversed, and
// bank.receiveStatus refuses to: it checks this network's
// own record of the payment and finds it Cleared rather than Rejected, so the
// message would become a dead letter at every recipient. There is nothing
// truthful to tell a bank here, because nothing about its payments changed.
//
// What DID change is the cycle, and the cycle is where the failure is visible:
// it stays Closed with no settlement against it. That is the state the central
// bank's console reads — the reason is recoverable from the net positions and
// the reserves, which is what the console puts side by side. The code is logged
// here as well, because the code is the one thing that arrives on the wire and
// is nowhere in the store.
//
// What the operator does about it is fund the short member and ask the clearing
// house to instruct settlement again: POST /cycles/{cid}/settle, which is
// csm.settle. That route is the whole of the remedy, and this handler is
// deliberately not part of it — a clearing house that retried by itself would
// re-present a batch against reserves nobody had changed.
func (c *csm) receiveSettlementStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A status naming no cycle. The central bank quotes the cycle it was
			// asked about even when it refuses, so this is a message this actor
			// cannot act on and has nobody to ask about.
			return fmt.Errorf("mesh: %s got a settlement status from %s naming no cycle", c.bic, from)
		}
		if r.Status != iso20022.TransactionStatusSettlementCompleted {
			c.m.log.Error("mesh: settlement refused",
				"clearing house", c.bic, "settlement agent", from,
				"cycle", r.TxID, "status", r.Status, "code", r.Code, "reason", r.Text)
			continue
		}
		if err := c.tellSettled(ctx, payment.CycleID(r.TxID), orig, r); err != nil {
			return err
		}
	}
	return nil
}

// tellSettled turns one settled CYCLE into per-PAYMENT advices, addressed to the
// banks that have something to do about each.
//
// # Two recipients, and they are not told the same thing for the same reason
//
// The SUBMITTER is waiting for the answer to its instruction: it sent a pacs.008
// or a pacs.003 naming one payment and has heard ACCP since, and ACSC is what
// closes it. That recipient is unchanged from Task 12, and it is chosen by the
// scheme's direction exactly as tell does — the payer's bank pushed, or the
// payee's bank pulled, and the other one never asked.
//
// The CREDITOR's bank has a LEG TO POST. Settlement moved the reserves; the
// payee is paid when its own bank releases the money out of its clearing
// suspense, and until Task 15 the central bank did that FOR it, in that bank's
// own book. This message is what replaces the reach.
//
// On a PULL they are the same institution and there is one message: the payee's
// bank submitted the collection and is also the creditor. On a PUSH they are two,
// and the second is new. Deduplicating rather than sending twice, because a bank
// that received the same advice twice would post nothing the second time — the
// ledger's idempotency key and PostCreditorLegTx's own Settled guard both see to
// that — but would still be told twice, and a system that says everything twice
// teaches nothing about who needs to hear what.
//
// # The central bank could not send these
//
// It is answering about a CYCLE, and settlementOps holds nothing that turns one
// into the payments inside it — SettleCycle answers with a Settlement and one
// statement per MEMBER, and neither GetCycle nor GetPayment is on that interface.
// It can act on a payment somebody else named to it, which is what a return is,
// and that is a different thing from being able to enumerate a batch. That was
// true before this task and it is why the fan-out lives here; what is new is that
// the message now causes a POSTING at the recipient rather than merely closing
// its instruction.
//
// The ORIGINAL message these refer back to is the SETTLEMENT INSTRUCTION, not
// the bank's own pacs.008, and that is a limitation worth naming. A bank matches
// on OrgnlTxId, which is the payment id and is right; but OrgnlMsgId names a
// message that bank never sent and has never seen. It is the honest one
// available — the clearing house does not keep each submitting bank's message id
// — and a real network would, so that every hop of a payment's answer quotes the
// instruction that started it.
func (c *csm) tellSettled(ctx context.Context, id payment.CycleID, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	cycle, err := c.ops.GetCycle(ctx, id)
	if err != nil {
		return fmt.Errorf("mesh: %s was told %s settled and cannot read it: %w", c.bic, id, err)
	}
	for _, pid := range cycle.PaymentIDs {
		p, err := c.ops.GetPayment(ctx, pid)
		if err != nil {
			return fmt.Errorf("mesh: %s was told %s settled and cannot read %s: %w", c.bic, id, pid, err)
		}
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			return fmt.Errorf("mesh: %s was told %s settled and holds no %q scheme to say who submitted %s: %w",
				c.bic, id, p.Scheme, p.ID, payment.ErrSchemeNotFound)
		}
		// The submitter first, so a push's two messages go out in the order the
		// banks have reason to expect them: the answer to the instruction, then
		// the advice to act on. On a pull the two ids are equal and there is one.
		recipients := []payment.ParticipantID{submitterOf(scheme, p.Debtor, p.Creditor).Participant}
		if creditor := p.Creditor.Participant; creditor != recipients[0] {
			recipients = append(recipients, creditor)
		}
		for _, recipient := range recipients {
			member, err := c.ops.GetParticipant(ctx, recipient)
			if err != nil {
				return fmt.Errorf("mesh: %s cannot address a bank %s settled for: %w", c.bic, p.ID, err)
			}
			if err := c.forward(member.BIC, orig, payment.TransactionStatusReport{
				EndToEndID: endToEndOf(p),
				TxID:       string(p.ID),
				Status:     r.Status,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// receiveReturnStatus is the clearing house passing the settlement agent's
// answer about a RETURN back to the bank that asked for one.
//
// It is the mirror of tellSettled and much the smaller half, because a return
// is answered per PAYMENT all the way down. A settlement answer is about a
// cycle and has to be turned into per-payment news by the only actor that knows
// what is in the batch; a return answer already names the one payment it is
// about, so this actor's whole job is to address it.
//
// Addressing it is the part that needs the store. The clearing house keeps no
// record of who sent it which message — it relays and forgets, which is what
// lets it route a message about a payment it does not hold — so "who asked for
// this return" is recomputed from the payment the answer names, by the same
// rule that chose the bank in the first place. See returnerOf.
//
// Both outcomes are forwarded, unchanged in substance. That is not the
// asymmetry receiveSettlementStatus has: a refused SETTLEMENT is news no bank
// can act on and some banks would act on wrongly, while a refused RETURN is the
// answer to a question one bank asked and is owed. What the bank does with it
// is nothing — see bank.receiveReturnStatus — but being told nothing and being
// told no are different things.
func (c *csm) receiveReturnStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A return status naming no payment. The settlement agent quotes
			// the payment it was asked about even when it refuses, so this is a
			// message this actor cannot act on and has nobody to ask about.
			return fmt.Errorf("mesh: %s got a return status from %s naming no payment", c.bic, from)
		}
		p, err := c.ops.GetPayment(ctx, payment.PaymentID(r.TxID))
		if err != nil {
			return fmt.Errorf("mesh: %s was told about the return of %s and cannot read it: %w", c.bic, r.TxID, err)
		}
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			return fmt.Errorf("mesh: %s was told about the return of %s and holds no %q scheme to say who asked: %w",
				c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
		}
		returner, err := c.ops.GetParticipant(ctx, returnerOf(scheme, p.Debtor, p.Creditor).Participant)
		if err != nil {
			return fmt.Errorf("mesh: %s cannot address the bank that returned %s: %w", c.bic, p.ID, err)
		}
		// The SETTLEMENT AGENT decided this, not the clearing house, and the
		// message says so. It is the one hop in this system where the sender and
		// the originator are different institutions; see forwardDecision.
		if err := c.forwardDecision(returner.BIC, from, orig, r); err != nil {
			return err
		}
	}
	return nil
}

// endToEndOf is the payer's own reference for a payment, or the EPC's
// convention where there is none.
//
// It repeats payment's unexported helper of the same name rather than reaching
// for it, because these two references must agree: a bank matching the answer to
// its instruction compares what it sent with what came back, and a status
// quoting an empty string against a pacs.008 carrying NOTPROVIDED would not
// match. See mesh.notProvided for the whole of that convention.
func endToEndOf(p payment.Payment) string {
	if p.EndToEndID == "" {
		return notProvided
	}
	return p.EndToEndID
}

// answer rejects a message the clearing house could not carry out, back to
// whoever sent it. No payment changes state: these are refusals of the MESSAGE —
// a bulk file, an agent this mesh cannot route to — decided before any payment
// was looked up.
func (c *csm) answer(to iso20022.BIC, orig payment.OriginalMessage, ref iso20022.PaymentIdentification, code iso20022.StatusReason, text string) error {
	return c.forward(to, orig, payment.TransactionStatusReport{
		EndToEndID: ref.EndToEndId,
		TxID:       ref.TxId,
		Status:     iso20022.TransactionStatusRejected,
		Code:       code,
		Text:       text,
	})
}
