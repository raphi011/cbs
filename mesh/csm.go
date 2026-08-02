package mesh

import (
	"context"
	"errors"
	"fmt"

	"github.com/raphi011/cbs/iso20022"
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
// It holds a csmOps, which is four methods wide: nothing about clearing moves
// money, and that is what makes clearing and settlement different jobs. What
// that does NOT amount to is a compile-time ban on posting — GetParticipant
// hands back live ledger and deposit handles bound to the bank it names, so a
// clearing house that wanted another bank's book has one. See the note on
// bankOps in ops.go for the whole of that hole. TestTheCSMTouchesOnlyTheNetworkBook
// is what actually holds this actor to it.
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
	case *iso20022.Pacs002:
		return c.receiveStatus(ctx, from, doc)
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
// Task 10. On a pull refused BEFORE the payer's bank answered — an unroutable
// agent, a bulk file — there is no leg, so there is one message again.
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
func (c *csm) tell(ctx context.Context, p payment.Payment, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	scheme, ok := c.ops.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("mesh: %s decided %s and holds no %q scheme to say who submitted it: %w",
			c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
	}
	submitter, err := c.ops.GetParticipant(ctx, submitterOf(scheme, p.Debtor, p.Creditor).Participant)
	if err != nil {
		return fmt.Errorf("mesh: %s cannot address the bank that submitted %s: %w", c.bic, p.ID, err)
	}
	if err := c.forward(submitter.BIC, orig, r); err != nil {
		return err
	}
	// The payer's bank, when it is holding money against a payment that has just
	// been rejected and is not the bank waiting for the answer. Only a pull
	// reaches this.
	if r.Status != iso20022.TransactionStatusRejected || p.DebtorLegTx == "" {
		return nil
	}
	debtor, err := c.ops.GetParticipant(ctx, p.Debtor.Participant)
	if err != nil {
		return fmt.Errorf("mesh: %s cannot address the payer's bank for %s: %w", c.bic, p.ID, err)
	}
	if debtor.BIC == submitter.BIC {
		return nil
	}
	return c.forward(debtor.BIC, orig, r)
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
func (c *csm) forward(to iso20022.BIC, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	env, err := payment.StatusMessage(orig, []payment.TransactionStatusReport{r}, payment.MessageContext{
		From:  c.bic,
		To:    to,
		MsgID: c.m.nextMsgID(c.bic),
		Now:   c.m.now(),
	})
	if err != nil {
		return fmt.Errorf("mesh: %s could not build its pacs.002 for %s: %w", c.bic, to, err)
	}
	return c.m.send(c.bic, to, env)
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
