// Package centralbank is the settlement agent: the only institution that moves
// reserves, and the EBICS host the clearing house and every member bank dial.
package centralbank

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/node"
	"github.com/raphi011/cbs/payment"
)

// A CentralBank is the settlement agent. It knows the environment it runs in
// and nothing about the deployment that built it.
type CentralBank struct {
	env node.Env

	// net is this institution's whole view, and it has ONE caller: Network,
	// which api/centralbank's surface reads every request through. Everything
	// else here goes through ops.
	net *payment.CentralBankNetwork
	ops ops

	bic iso20022.BIC

	// host is the EBICS side the clearing house and every member bank dial: one
	// download queue apiece, and the log of the orders each has uploaded.
	host *ebics.Server
}

// New builds the settlement agent over its own network, with the host its
// subscribers dial.
func New(env node.Env, net *payment.CentralBankNetwork, bic iso20022.BIC, host *ebics.Server) *CentralBank {
	return &CentralBank{env: env, net: net, ops: net, bic: bic, host: host}
}

func (c *CentralBank) Network() *payment.CentralBankNetwork { return c.net }
func (c *CentralBank) BIC() iso20022.BIC                    { return c.bic }
func (c *CentralBank) Log() *slog.Logger                    { return c.env.Log }

// EBICS is this institution's file-transfer endpoint, mounted on its own
// listener. See ebics.Server.ServeHTTP.
func (c *CentralBank) EBICS() http.Handler { return c.host }

// Host is the transport state itself: who is enrolled, what is queued for each
// of them, and what each has uploaded.
func (c *CentralBank) Host() *ebics.Server { return c.host }

// ---------------------------------------------------------------------------
// Queueing, and working through what has arrived
// ---------------------------------------------------------------------------

// enqueue addresses one document to one subscriber by putting it in that
// subscriber's download queue. See csm.ClearingHouse.enqueue, which is the same
// act one institution over.
func (c *CentralBank) enqueue(ctx context.Context, to iso20022.BIC, env iso20022.Envelope) error {
	t, err := node.OrderTypeOf(env.Document)
	if err != nil {
		return err
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		return fmt.Errorf("server: %s marshalling for %s: %w", c.bic, to, err)
	}
	id, err := c.host.Enqueue(ctx, ebics.SubscriberID(to), t, raw)
	if err != nil {
		return fmt.Errorf("server: %s cannot address a %s to %s: %w", c.bic, t, to, err)
	}
	c.env.Journal.File(node.FileMoved{From: c.bic, To: to, OrderType: t, OrderID: id})
	return nil
}

// Work runs this institution through the orders uploaded to it and not yet
// answered, oldest first. See csm.ClearingHouse.Work, which has the same shape
// and the same reason for answering every order on its acknowledgement.
func (c *CentralBank) Work(ctx context.Context) []node.Problem {
	ctx = node.WithActor(ctx, c.bic)

	pending, err := c.host.Pending(ctx)
	if err != nil {
		return []node.Problem{{Institution: c.bic, Detail: fmt.Sprintf("reading the orders waiting to be worked through: %v", err)}}
	}
	var problems []node.Problem
	for _, order := range pending {
		answer, detail := c.host.Processed, ""
		if err := c.handle(ctx, iso20022.BIC(order.Subscriber), order.Payload); err != nil {
			problems = append(problems, node.Problem{Institution: c.bic, OrderID: order.ID, Detail: err.Error()})
			answer, detail = c.host.Rejected, err.Error()
		}
		if err := answer(ctx, order.ID, detail); err != nil {
			problems = append(problems, node.Problem{Institution: c.bic, OrderID: order.ID,
				Detail: fmt.Sprintf("recording what became of this order: %v", err)})
		}
	}
	return problems
}

// handle dispatches on the document the bytes carry.
func (c *CentralBank) handle(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return c.answerUnreadable(ctx, from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs009:
		return c.receiveSettlement(from, env.AppHdr, doc, ctx)
	case *iso20022.Pacs004:
		return c.receiveReturn(ctx, from, env.AppHdr, doc)
	case *iso20022.Camt050:
		return c.receiveLodgement(ctx, from, env.AppHdr, doc)
	default:
		return fmt.Errorf("server: %s has no handler for %s", c.bic, env.AppHdr.MsgDefIdr)
	}
}

// answerUnreadable queues an FF01 for the subscriber whose file would not parse.
// See node.Unreadable.
func (c *CentralBank) answerUnreadable(ctx context.Context, from iso20022.BIC, cause error) error {
	env, err := node.Unreadable(c.env.MessageContext(c.bic, from), cause)
	if err != nil {
		return errors.Join(fmt.Errorf("server: %s could not build the FF01 for %s: %w", c.bic, from, err), cause)
	}
	return c.enqueue(ctx, from, env)
}

// receiveSettlement is the central bank answering a settlement instruction:
// ACSC, or RJCT with the code its own refusal maps to.
func (c *CentralBank) receiveSettlement(from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs009, ctx context.Context) error {
	body := doc.FICdtTrf
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: hdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}

	legs, err := payment.ReadSettlement(doc)
	if err != nil {
		// A count that does not match what arrived.
		return c.answer(ctx, from, orig, node.NotProvided, node.NotProvided, iso20022.TransactionStatusRejected, err)
	}
	id, err := cycleOf(legs)
	if err != nil {
		return c.answer(ctx, from, orig, node.NotProvided, node.NotProvided, iso20022.TransactionStatusRejected, err)
	}

	// The legs travel on, because they ARE the instruction: this institution
	// settles what it was asked to settle rather than re-deriving a batch out of
	// the clearing house's cycle row, which it holds no table for.
	_, statements, err := c.ops.SettleCycle(ctx, id, legs)
	if err != nil {
		if errors.Is(err, payment.ErrCycleAlreadySettled) {
			return fmt.Errorf("server: %s was told to settle %s again: %w", c.bic, id, err)
		}
		return c.answer(ctx, from, orig, string(id), string(id), iso20022.TransactionStatusRejected, err)
	}
	if err := c.advise(ctx, statements); err != nil {
		return err
	}
	return c.answer(ctx, from, orig, string(id), string(id), iso20022.TransactionStatusSettlementCompleted, nil)
}

// advise puts each member's own reserve statement in that member's queue.
func (c *CentralBank) advise(ctx context.Context, statements []payment.SettlementStatement) error {
	for _, st := range statements {
		env, err := payment.StatementMessage(st, c.env.MessageContext(c.bic, st.Agent))
		if err != nil {
			return fmt.Errorf("server: %s could not build the statement for %s: %w", c.bic, st.Agent, err)
		}
		if err := c.enqueue(ctx, st.Agent, env); err != nil {
			return fmt.Errorf("server: %s settled %s and could not tell %s: %w", c.bic, st.Reference, st.Agent, err)
		}
	}
	return nil
}

// receiveReturn is the central bank executing a return: the R-transaction, and
// the second of the two things that move reserves here.
func (c *CentralBank) receiveReturn(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs004) error {
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
		return c.answer(ctx, from, orig, returnedEndToEnd(first), first.OrgnlTxId, iso20022.TransactionStatusRejected,
			fmt.Errorf("this settlement agent returns one payment per message; TxInf carries %d", n))
	}
	if said := body.GrpHdr.NbOfTxs; said != "1" {
		return c.answer(ctx, from, orig, returnedEndToEnd(first), first.OrgnlTxId, iso20022.TransactionStatusRejected,
			fmt.Errorf("GrpHdr/NbOfTxs says %q and one transaction arrived; a return lost in transit is a payer who is not repaid", said))
	}

	// The whole of the input, read off the message. The count above has already
	// established there is exactly one instruction to act on.
	id := payment.PaymentID(first.OrgnlTxId)
	ins, err := payment.ReadReturn(doc)
	if err != nil {
		return c.answer(ctx, from, orig, returnedEndToEnd(first), string(id), iso20022.TransactionStatusRejected, err)
	}

	statements, err := c.ops.SettleReturn(ctx, ins[0])
	if err != nil {
		if errors.Is(err, payment.ErrReturnAlreadySettled) {
			return fmt.Errorf("server: %s was told to return %s again: %w", c.bic, id, err)
		}
		return c.answer(ctx, from, orig, returnedEndToEnd(first), string(id), iso20022.TransactionStatusRejected, err)
	}
	// Before the answer. See advise, and the note above on the order.
	if err := c.advise(ctx, statements); err != nil {
		return err
	}
	return c.answer(ctx, from, orig, returnedEndToEnd(first), string(id), iso20022.TransactionStatusSettlementCompleted, nil)
}

// receiveLodgement is the central bank crediting a member's reserve account
// because the member asked it to: the fourth thing this institution does.
func (c *CentralBank) receiveLodgement(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Camt050) error {
	in, err := payment.ReadLodgement(hdr, doc)
	if err != nil {
		// Answered against the message id off the document rather than the reader's
		// output, because the commonest way to be here is that the reader refused and
		// produced nothing.
		ref := doc.LqdtyCdtTrf.MsgHdr.MsgId
		if ref == "" {
			return fmt.Errorf("server: %s was sent a lodgement with no message id by %s, so no receipt could name it: %w",
				c.bic, from, err)
		}
		return c.acknowledgeLodgement(ctx, from, payment.LodgementReceipt{
			Ref:    ref,
			Status: iso20022.TransactionStatusRejected,
			Reason: err.Error(),
		})
	}

	receipt, err := c.ops.ReceiveLodgement(ctx, in)
	if err != nil {
		if errors.Is(err, ledger.ErrDuplicateIdempotencyKey) {
			return fmt.Errorf("server: %s was told to lodge %s again: %w", c.bic, in.Ref, err)
		}
		if !isLodgementRefusal(err) {
			return fmt.Errorf(
				"server: %s could not carry out %s's lodgement %s and did not refuse it, so the member's reserve mirror is now overstated: %w",
				c.bic, from, in.Ref, err)
		}
		return c.acknowledgeLodgement(ctx, from, payment.LodgementReceipt{
			Ref:    in.Ref,
			Status: iso20022.TransactionStatusRejected,
			Reason: err.Error(),
		})
	}
	return c.acknowledgeLodgement(ctx, from, receipt)
}

// lodgementRefusals is everything a settlement agent may JUDGE about a
// lodgement, and receiveLodgement answers exactly these with a refusing
// camt.025.
var lodgementRefusals = []error{
	payment.ErrInvalidPaymentAmount,
	payment.ErrSettlementMemberNotFound,
	payment.ErrParticipantAssetNotFound,
	payment.ErrSettlementAccountReplaced,
}

// isLodgementRefusal unwraps, because payment wraps its sentinels with the BIC and
// the account they are about and the prose is what travels as the receipt's Desc.
func isLodgementRefusal(err error) bool {
	for _, sentinel := range lodgementRefusals {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// acknowledgeLodgement queues the camt.025 for the member that asked.
func (c *CentralBank) acknowledgeLodgement(ctx context.Context, to iso20022.BIC, r payment.LodgementReceipt) error {
	env, err := payment.LodgementReceiptMessage(r, c.env.MessageContext(c.bic, to))
	if err != nil {
		return fmt.Errorf("server: %s could not build its camt.025 for %s: %w", c.bic, to, err)
	}
	return c.enqueue(ctx, to, env)
}

// returnedEndToEnd is the payer's own reference for a returned payment, as the
// RETURNING BANK quoted it, or the EPC's convention where there is none.
func returnedEndToEnd(tx iso20022.ReturnTransaction) string {
	if tx.OrgnlEndToEndId == "" {
		return node.NotProvided
	}
	return tx.OrgnlEndToEndId
}

// cycleOf is which closed cycle an instruction discharges, taken from the legs
// themselves.
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

// answer queues the pacs.002 for whoever uploaded the instruction.
func (c *CentralBank) answer(ctx context.Context, to iso20022.BIC, orig payment.OriginalMessage, e2e, txid string,
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
	c.env.Journal.Outcome(node.TransactionOutcome{
		DecidedBy: c.bic,
		Payment:   payment.PaymentID(txid),
		Status:    report.Status,
		Code:      report.Code,
		Text:      report.Text,
	})
	env, err := payment.StatusMessage(orig, []payment.TransactionStatusReport{report}, c.env.MessageContext(c.bic, to))
	if err != nil {
		return errors.Join(fmt.Errorf("server: %s could not build its pacs.002 for %s: %w", c.bic, to, err), cause)
	}
	// The cause is NOT returned once it has been answered, for the reason
	// bank.Bank.answer gives: a refusal the counterparty was told about is
	// completed work, and returning it as well would make every AM04 a line in
	// the report too.
	return c.enqueue(ctx, to, env)
}
