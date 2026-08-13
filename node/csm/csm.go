// Package csm is the clearing house: every payment in the network, the cycles,
// the schemes, the roster, and the EBICS host every member bank dials.
package csm

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

// A ClearingHouse is the CSM. It knows the environment it runs in and nothing
// about the deployment that built it.
type ClearingHouse struct {
	env node.Env

	// net is this institution's whole view, and it has ONE caller: Network,
	// which api/csm's surface reads every request through. Everything else here
	// goes through ops.
	net *payment.ClearingHouseNetwork
	ops ops

	bic iso20022.BIC

	// host is the EBICS side every member bank dials: one download queue per
	// enrolled subscriber, and the log of what each has uploaded.
	host *ebics.Server

	// cb is this institution's own connection to the settlement agent, on which
	// it uploads settlement instructions and returns and collects the answers.
	// The clearing house is a subscriber there, exactly as a member bank is.
	cb *ebics.Client

	// WHAT THIS INSTITUTION IS HOLDING is in its DATABASE, and no field here.
}

// New builds the clearing house over its own network, with the host its members
// dial and its own connection to the settlement agent.
func New(env node.Env, net *payment.ClearingHouseNetwork, bic iso20022.BIC, host *ebics.Server, cb *ebics.Client) *ClearingHouse {
	return &ClearingHouse{env: env, net: net, ops: net, bic: bic, host: host, cb: cb}
}

func (c *ClearingHouse) Network() *payment.ClearingHouseNetwork { return c.net }
func (c *ClearingHouse) BIC() iso20022.BIC                      { return c.bic }
func (c *ClearingHouse) Log() *slog.Logger                      { return c.env.Log }

// EBICS is this institution's file-transfer endpoint, mounted on its own
// listener. It is one URL that everything POSTs to, which is the protocol's own
// shape; see ebics.Server.ServeHTTP.
func (c *ClearingHouse) EBICS() http.Handler { return c.host }

// Host is the transport state itself: who is enrolled, what is queued for each
// of them, and what each has uploaded.
func (c *ClearingHouse) Host() *ebics.Server { return c.host }

// ---------------------------------------------------------------------------
// Queueing
// ---------------------------------------------------------------------------

// enqueue addresses one document to one subscriber by putting it in that
// subscriber's download queue.
func (c *ClearingHouse) enqueue(ctx context.Context, to iso20022.BIC, env iso20022.Envelope) error {
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

// upload puts a document on this institution's own connection to the settlement
// agent. See bank.Bank.upload, which is the same act one institution over.
func (c *ClearingHouse) upload(ctx context.Context, env iso20022.Envelope) error {
	to := c.env.CentralBankBIC
	t, err := node.OrderTypeOf(env.Document)
	if err != nil {
		return err
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		return fmt.Errorf("server: %s marshalling for %s: %w", c.bic, to, err)
	}
	id, err := c.cb.Upload(ctx, t, raw)
	if err != nil {
		return fmt.Errorf("server: %s could not upload a %s to %s: %w", c.bic, t, to, err)
	}
	c.env.Journal.File(node.FileMoved{From: c.bic, To: to, OrderType: t, OrderID: id})
	return nil
}

// ---------------------------------------------------------------------------
// Working through what has arrived
// ---------------------------------------------------------------------------

// Work runs this institution through everything its members have uploaded and
// not yet been answered about, oldest first.
func (c *ClearingHouse) Work(ctx context.Context) []node.Problem {
	ctx = node.WithActor(ctx, c.bic)

	pending, err := c.host.Pending(ctx)
	if err != nil {
		return []node.Problem{{Institution: c.bic, Detail: fmt.Sprintf("reading the orders waiting to be worked through: %v", err)}}
	}
	var problems []node.Problem
	for _, order := range pending {
		answer, detail := c.host.Processed, ""
		if err := c.handle(ctx, order); err != nil {
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

// Collect downloads the settlement agent's answers and works through them.
func (c *ClearingHouse) Collect(ctx context.Context) []node.Problem {
	ctx = node.WithActor(ctx, c.bic)

	from := c.env.CentralBankBIC
	files, err := c.cb.Download(ctx, ebics.BTD)
	if err != nil {
		return []node.Problem{{Institution: c.bic, Detail: fmt.Sprintf("collecting from %s: %v", from, err)}}
	}
	var problems []node.Problem
	for _, f := range files {
		if err := c.handleFile(ctx, from, f.Payload); err != nil {
			problems = append(problems, node.Problem{Institution: c.bic, OrderID: f.OrderID, Detail: err.Error()})
		}
	}
	return problems
}

// handle works through one order a member uploaded here.
func (c *ClearingHouse) handle(ctx context.Context, order ebics.Order) error {
	return c.handleFile(ctx, iso20022.BIC(order.Subscriber), order.Payload)
}

// handleFile dispatches on the document the bytes carry.
func (c *ClearingHouse) handleFile(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return c.answerUnreadable(ctx, from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs008:
		return c.takeCreditTransfer(ctx, from, env, raw, doc)
	case *iso20022.Pacs003:
		return c.takeDirectDebit(ctx, from, env, raw, doc)
	case *iso20022.Pacs004:
		return c.relayReturn(ctx, from, env, raw, doc)
	case *iso20022.Pacs002:
		// Three kinds of status arrive here, and it takes two questions to tell them
		// apart.
		switch {
		case from != c.env.CentralBankBIC:
			return c.receiveUnreadable(from, doc)
		case node.IsAbout(doc, node.ReturnMsgDef):
			return c.receiveReturnStatus(ctx, from, doc)
		default:
			return c.receiveSettlementStatus(ctx, from, doc)
		}
	default:
		return fmt.Errorf("server: %s has no handler for %s", c.bic, env.AppHdr.MsgDefIdr)
	}
}

// answerUnreadable queues an FF01 for the subscriber whose file would not parse.
// See node.Unreadable.
func (c *ClearingHouse) answerUnreadable(ctx context.Context, from iso20022.BIC, cause error) error {
	env, err := node.Unreadable(c.env.MessageContext(c.bic, from), cause)
	if err != nil {
		return errors.Join(fmt.Errorf("server: %s could not build the FF01 for %s: %w", c.bic, from, err), cause)
	}
	return c.enqueue(ctx, from, env)
}

// takeCreditTransfer takes a credit transfer into the network: this
// institution's own copy of every payment in it, each transaction into the open
// cycle for its scheme, and one output file per CREDITOR's agent — the bank
// that holds the payee, because a push travels towards the money's destination.
func (c *ClearingHouse) takeCreditTransfer(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, raw []byte, doc *iso20022.Pacs008) error {
	body := doc.FIToFICstmrCdtTrf
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: env.AppHdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	ps, err := c.ops.RecordRelayedCreditTransfer(ctx, doc)
	if err != nil {
		return fmt.Errorf("server: %s cannot carry the file %s uploaded: %w", c.bic, from, err)
	}
	groups := groupedBy(body.CdtTrfTxInf, func(tx iso20022.CreditTransferTransaction) iso20022.BIC {
		return tx.CdtrAgt.FinInstnId.BICFI
	})
	return c.takeRecorded(ctx, from, raw, orig, ps, groups)
}

// takeDirectDebit sorts a collection by the DEBTOR's agent: the bank that holds
// the payer, because a pull travels towards the money's source.
func (c *ClearingHouse) takeDirectDebit(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, raw []byte, doc *iso20022.Pacs003) error {
	body := doc.FIToFICstmrDrctDbt
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: env.AppHdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	ps, err := c.ops.RecordRelayedDirectDebit(ctx, doc)
	if err != nil {
		return fmt.Errorf("server: %s cannot carry the file %s uploaded: %w", c.bic, from, err)
	}
	groups := groupedBy(body.DrctDbtTxInf, func(tx iso20022.DirectDebitTransactionInformation) iso20022.BIC {
		return tx.DbtrAgt.FinInstnId.BICFI
	})
	return c.takeRecorded(ctx, from, raw, orig, ps, groups)
}

// A destination is one receiving bank and the transactions of an uploaded file
// that are addressed to it, by their position in that file.
type destination struct {
	to  iso20022.BIC
	idx []int
}

// groupedBy sorts a file's transactions by the agent each names, keeping the
// destinations in the order the sender first named them.
func groupedBy[T any](txs []T, agent func(T) iso20022.BIC) []destination {
	var out []destination
	at := map[iso20022.BIC]int{}
	for i, tx := range txs {
		to := agent(tx)
		j, ok := at[to]
		if !ok {
			j = len(out)
			at[to] = j
			out = append(out, destination{to: to})
		}
		out[j].idx = append(out[j].idx, i)
	}
	return out
}

// takeRecorded is the second half of taking a file in: every transaction into
// the cycle it will settle in, the submitting bank told what became of each,
// and one output file per receiving bank built and HELD.
func (c *ClearingHouse) takeRecorded(ctx context.Context, from iso20022.BIC, raw []byte,
	orig payment.OriginalMessage, ps []payment.Payment, groups []destination) error {

	var errs []error
	reports := make([]payment.TransactionStatusReport, 0, len(ps))

	for _, g := range groups {
		// A bank this deployment holds no queue for.
		if !c.host.Enrolled(ebics.SubscriberID(g.to)) {
			for _, i := range g.idx {
				reports = append(reports, c.refuse(ctx, ps[i], iso20022.StatusReasonBankIdentifierIncorrect,
					fmt.Sprintf("no route to %s", g.to), &errs))
			}
			continue
		}
		var cycle payment.CycleID
		var taken []payment.HeldTransaction
		for _, i := range g.idx {
			accepted, err := c.ops.AcceptAtCSM(ctx, ps[i].ID)
			if err != nil {
				// The clearing house could not clear it, so the clearing house rejects it,
				// with the code its own refusal maps to — TM01 for no open cycle, MS03 for
				// a bank the roster does not admit.
				reports = append(reports, c.refuse(ctx, ps[i], payment.ReasonFor(err), err.Error(), &errs))
				continue
			}
			cycle = accepted.CycleID
			taken = append(taken, payment.HeldTransaction{Position: i, PaymentID: accepted.ID})
			c.env.Journal.Outcome(node.TransactionOutcome{
				DecidedBy: c.bic, Payment: accepted.ID,
				Status: iso20022.TransactionStatusAccepted,
			})
			reports = append(reports, payment.TransactionStatusReport{
				EndToEndID: endToEndOf(accepted),
				TxID:       string(accepted.ID),
				Status:     iso20022.TransactionStatusAccepted,
			})
		}
		if len(taken) == 0 {
			continue
		}
		if err := c.ops.HoldFile(ctx, payment.HeldFile{
			CycleID: cycle, Destination: g.to, File: raw, Transactions: taken,
		}); err != nil {
			// The transactions are accepted and the share is not, the one state
			// unhandable exists to catch: the cut-off will refuse to settle rather than
			// move reserves nobody could be told about.
			errs = append(errs, fmt.Errorf("server: %s took %s's file into %s and could not hold %s's share of it: %w",
				c.bic, from, cycle, g.to, err))
		}
	}

	if len(reports) > 0 {
		errs = append(errs, c.forwardDecision(ctx, from, "", orig, reports))
	}
	return errors.Join(errs...)
}

// refuse is this institution rejecting one transaction of an uploaded file and
// saying so in the report the submitting bank will be handed.
func (c *ClearingHouse) refuse(ctx context.Context, p payment.Payment,
	code iso20022.StatusReason, text string, errs *[]error) payment.TransactionStatusReport {

	if _, err := c.ops.RejectAtCSM(ctx, p.ID, code, text); err != nil {
		*errs = append(*errs, fmt.Errorf("server: %s answered %s for %s and could not record it: %w",
			c.bic, code, p.ID, err))
	}
	c.env.Journal.Outcome(node.TransactionOutcome{
		DecidedBy: c.bic, Payment: p.ID,
		Status: iso20022.TransactionStatusRejected, Code: code, Text: text,
	})
	return payment.TransactionStatusReport{
		EndToEndID: endToEndOf(p),
		TxID:       string(p.ID),
		Status:     iso20022.TransactionStatusRejected,
		Code:       code,
		Text:       text,
	}
}

// releaseFiles puts one settled cycle's output files in the receiving banks'
// download queues.
func (c *ClearingHouse) releaseFiles(ctx context.Context, cycle payment.CycleID, settled []payment.Payment) error {
	files, err := c.ops.ListHeldFiles(ctx, cycle)
	if err != nil {
		return fmt.Errorf("server: %s settled %s and cannot read the files it is holding for it: %w", c.bic, cycle, err)
	}

	final := make(map[payment.PaymentID]bool, len(settled))
	for _, p := range settled {
		final[p.ID] = true
	}
	// Which of them a file was built for, whatever became of the queueing.
	inAFile := make(map[payment.PaymentID]bool, len(settled))

	var errs []error
	for _, f := range files {
		idx := make([]int, 0, len(f.Transactions))
		for _, h := range f.Transactions {
			if final[h.PaymentID] {
				idx = append(idx, h.Position)
				inAFile[h.PaymentID] = true
			}
		}
		// A share with nothing left in it is owed to nobody, and dropping it is
		// the whole of what releasing it means.
		if len(idx) == 0 {
			errs = append(errs, c.drop(ctx, f))
			continue
		}
		part, err := shareOf(f.File, idx)
		if err != nil {
			errs = append(errs, fmt.Errorf("server: %s settled %s and cannot cut %s's share out of the file it is holding: %w",
				c.bic, cycle, f.Destination, err))
			continue
		}
		if err := c.enqueue(ctx, f.Destination, iso20022.Envelope{
			AppHdr: iso20022.AppHdr{
				Fr:        iso20022.NewAgent(c.bic),
				To:        iso20022.NewAgent(f.Destination),
				BizMsgIdr: c.env.NextMsgID(c.bic),
				MsgDefIdr: part.MessageDefinitionIdentifier(),
				CreDt:     iso20022.ISODateTime{Time: c.env.Now()},
			},
			Document: part,
		}); err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, c.drop(ctx, f))
	}
	errs = append(errs, c.unhanded(cycle, settled, inAFile)...)
	return errors.Join(errs...)
}

// drop discharges one share this institution is no longer holding anything for.
// Only ever after the hand-over, and never for a share the hand-over failed on:
// see payment.DropHeldFile, which is where that order is argued.
func (c *ClearingHouse) drop(ctx context.Context, f payment.HeldFile) error {
	if err := c.ops.DropHeldFile(ctx, f.CycleID, f.Seq); err != nil {
		return fmt.Errorf("server: %s handed %s its share of %s and could not discard it: %w",
			c.bic, f.Destination, f.CycleID, err)
	}
	return nil
}

// shareOf cuts one destination's transactions out of a held file.
func shareOf(file []byte, idx []int) (iso20022.Document, error) {
	env, err := iso20022.Unmarshal(file)
	if err != nil {
		return nil, err
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs008:
		return creditTransferPart(doc, idx), nil
	case *iso20022.Pacs003:
		return directDebitPart(doc, idx), nil
	default:
		return nil, fmt.Errorf("server: a %T was held as an output file, and no share is cut from one", doc)
	}
}

// unhanded is every payment this cut-off settled that no receiving bank was
// handed a file for.
func (c *ClearingHouse) unhanded(cycle payment.CycleID, settled []payment.Payment, inAFile map[payment.PaymentID]bool) []error {
	var errs []error
	for _, p := range settled {
		if inAFile[p.ID] {
			continue
		}
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			errs = append(errs, fmt.Errorf("server: %s settled %s in cycle %s and holds no %q scheme to say which bank was owed the instruction: %w",
				c.bic, p.ID, cycle, p.Scheme, payment.ErrSchemeNotFound))
			continue
		}
		errs = append(errs, fmt.Errorf(
			"server: %s settled %s in cycle %s and built no file for %s, which is the bank that has to apply it: "+
				"the reserves behind it are final and that bank has not been told the payment exists",
			c.bic, p.ID, cycle, payment.ReceiverOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)))
	}
	return errs
}

// relayReturn uploads a return to the SETTLEMENT AGENT, and keeps a copy for
// the bank that has not heard about it yet.
func (c *ClearingHouse) relayReturn(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, raw []byte, doc *iso20022.Pacs004) error {
	body := doc.PmtRtr
	// Unmarshal refuses a pacs.004 with no transactions (iso20022's
	// PaymentReturn.validate), so there is always a first one to refer back by.
	first := body.TxInf[0]
	// Held under the payment the answer will name, which is the only thing the two
	// messages have in common.
	if first.OrgnlTxId != "" {
		if err := c.ops.HoldReturn(ctx, payment.HeldReturn{
			PaymentID: payment.PaymentID(first.OrgnlTxId), ReturnedBy: from, File: raw,
		}); err != nil {
			return fmt.Errorf("server: %s cannot hold the return %s uploaded for %s: %w",
				c.bic, from, first.OrgnlTxId, err)
		}
	}
	return c.upload(ctx, iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(c.env.CentralBankBIC),
			BizMsgIdr: c.env.NextMsgID(c.bic),
			MsgDefIdr: env.AppHdr.MsgDefIdr,
			CreDt:     iso20022.ISODateTime{Time: c.env.Now()},
		},
		// The document travels unchanged, for relay's reason: it is what the
		// returning bank said and is not this institution's to rewrite.
		Document: doc,
	})
}

// releaseReturn queues the held pacs.004 for the bank that did not ask for the
// return, now that the settlement agent has made it final.
func (c *ClearingHouse) releaseReturn(ctx context.Context, held payment.HeldReturn, id payment.PaymentID) error {
	env, err := iso20022.Unmarshal(held.File)
	if err != nil {
		return fmt.Errorf("server: %s settled the return of %s and cannot read the message it is holding: %w", c.bic, id, err)
	}
	doc, ok := env.Document.(*iso20022.Pacs004)
	if !ok {
		return fmt.Errorf("server: %s is holding a %T as the return of %s, and a return is a pacs.004", c.bic, env.Document, id)
	}
	ref := doc.PmtRtr.TxInf[0].OrgnlTxRef
	if ref == nil || ref.DbtrAgt == nil || ref.CdtrAgt == nil {
		return fmt.Errorf("server: %s settled the return of %s and the message names no agents to release it to", c.bic, id)
	}
	to := ref.DbtrAgt.FinInstnId.BICFI
	if to == held.ReturnedBy {
		to = ref.CdtrAgt.FinInstnId.BICFI
	}
	return c.enqueue(ctx, to, iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: c.env.NextMsgID(c.bic),
			MsgDefIdr: node.ReturnMsgDef,
			CreDt:     iso20022.ISODateTime{Time: c.env.Now()},
		},
		Document: doc,
	})
}

// receiveUnreadable is a member bank saying it could not parse a file this
// institution handed it.
func (c *ClearingHouse) receiveUnreadable(from iso20022.BIC, doc *iso20022.Pacs002) error {
	_, reports := payment.ReadStatus(doc)
	var errs []error
	for _, r := range reports {
		errs = append(errs, fmt.Errorf("server: %s could not read a file %s handed it: %s %s",
			from, c.bic, r.Code, r.Text))
	}
	if len(errs) == 0 {
		return fmt.Errorf("server: %s uploaded a status to %s that reports nothing", from, c.bic)
	}
	return errors.Join(errs...)
}

// tell queues one decision this institution made for the bank that SUBMITTED
// the payment it is about.
func (c *ClearingHouse) tell(ctx context.Context, p payment.Payment, orig payment.OriginalMessage,
	r payment.TransactionStatusReport) error {

	scheme, ok := c.ops.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("server: %s decided %s and holds no %q scheme to say who submitted it: %w",
			c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
	}
	return c.forward(ctx, payment.SubmitterOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent), orig, r)
}

// forward queues the status for one bank. Which bank, and how many of them, is
// tell's decision.
func (c *ClearingHouse) forward(ctx context.Context, to iso20022.BIC, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	return c.forwardDecision(ctx, to, "", orig, []payment.TransactionStatusReport{r})
}

// forwardDecision is forward for a status this institution is CARRYING rather
// than making: it names the institution that decided, so the pacs.002's Orgtr
// does not say the clearing house.
func (c *ClearingHouse) forwardDecision(ctx context.Context, to, decidedBy iso20022.BIC, orig payment.OriginalMessage, rs []payment.TransactionStatusReport) error {
	mc := c.env.MessageContext(c.bic, to)
	mc.DecidedBy = decidedBy
	env, err := payment.StatusMessage(orig, rs, mc)
	if err != nil {
		return fmt.Errorf("server: %s could not build its pacs.002 for %s: %w", c.bic, to, err)
	}
	return c.enqueue(ctx, to, env)
}

// Reject is the clearing house declining a payment because an operator said so.
func (c *ClearingHouse) Reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error) {
	ctx = node.WithActor(ctx, c.bic)

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
	c.env.Journal.Outcome(node.TransactionOutcome{DecidedBy: c.bic, Payment: p.ID, Status: r.Status, Code: code, Text: text})
	// The rejection is a fact and the queueing may still fail, so the payment
	// comes back BESIDE the error rather than being swallowed — CloseCycle's
	// shape, and the same half-happened outcome: a payment that is Rejected and
	// whose payer has not been given their money back.
	if err := c.tell(ctx, p, payment.OriginalMessage{MsgID: node.NotProvided, MsgDefIdr: node.NotProvided}, r); err != nil {
		return p, fmt.Errorf("server: %s rejected %s and could not say so: %w", c.bic, p.ID, err)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// The cut-off, and the settlement it instructs
// ---------------------------------------------------------------------------

// CloseCycle is the clearing house reaching a cut-off: it nets the batch, and
// then asks the central bank to discharge it.
func (c *ClearingHouse) CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	ctx = node.WithActor(ctx, c.bic)

	closed, err := c.ops.CloseCycle(ctx, id)
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	if err := c.instructSettlement(ctx, closed); err != nil {
		return closed, fmt.Errorf("server: %s closed %s and could not instruct settlement: %w", c.bic, closed.ID, err)
	}
	return closed, nil
}

// Settle is the clearing house uploading a settlement instruction AGAIN, for a
// cycle the central bank refused.
func (c *ClearingHouse) Settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	ctx = node.WithActor(ctx, c.bic)

	cycle, err := c.ops.GetCycle(ctx, id)
	if err != nil {
		return payment.ClearingCycle{}, err
	}
	if cycle.Status != payment.CycleClosed {
		return payment.ClearingCycle{}, fmt.Errorf("server: %s is %v, and an instruction to settle is only re-sent for a closed one: %w",
			id, cycle.Status, payment.ErrCycleNotClosed)
	}
	// The cycle is a fact and the upload may still fail, so it comes back BESIDE
	// the error rather than being swallowed — CloseCycle's shape.
	if err := c.instructSettlement(ctx, cycle); err != nil {
		return cycle, fmt.Errorf("server: %s could not re-instruct settlement of %s: %w", c.bic, cycle.ID, err)
	}
	return cycle, nil
}

// CloseOpenCycles is the clearing house reaching its cut-off on a business day:
// every cycle standing open is netted, and its positions are uploaded.
func (c *ClearingHouse) CloseOpenCycles(ctx context.Context) []node.Problem {
	ctx = node.WithActor(ctx, c.bic)

	cycles, err := c.ops.ListCycles(ctx)
	if err != nil {
		return []node.Problem{{Institution: c.bic, Detail: fmt.Sprintf("reading the cycles to cut off: %v", err)}}
	}
	var problems []node.Problem
	for _, cycle := range cycles {
		if cycle.Status != payment.CycleOpen {
			continue
		}
		if _, err := c.CloseCycle(ctx, cycle.ID); err != nil {
			problems = append(problems, node.Problem{Institution: c.bic, Detail: err.Error()})
		}
	}
	return problems
}

// OpenCycles gives every registered scheme a cycle for the next clearing day.
func (c *ClearingHouse) OpenCycles(ctx context.Context) []node.Problem {
	ctx = node.WithActor(ctx, c.bic)

	var problems []node.Problem
	for _, scheme := range c.ops.ListSchemes() {
		switch _, err := c.ops.OpenCycle(ctx, scheme.ID()); {
		case err == nil, errors.Is(err, payment.ErrCycleAlreadyOpen):
		default:
			problems = append(problems, node.Problem{
				Institution: c.bic,
				Detail:      fmt.Sprintf("opening a cycle for %s: %v", scheme.ID(), err),
			})
		}
	}
	return problems
}

// instructSettlement uploads the closed cycle's net positions to the central
// bank as a pacs.009.
func (c *ClearingHouse) instructSettlement(ctx context.Context, closed payment.ClearingCycle) error {
	scheme, ok := c.ops.Scheme(closed.Scheme)
	if !ok {
		return fmt.Errorf("server: %s closed %s and holds no %q scheme to settle it in: %w",
			c.bic, closed.ID, closed.Scheme, payment.ErrSchemeNotFound)
	}
	// BEFORE the legs are counted, because a cut-off with no leg is still
	// discharged: SettleUninstructed does it, and everything a settlement releases
	// is released then too.
	if err := c.unhandable(ctx, closed); err != nil {
		return err
	}
	legs := c.settlementLegs(closed, scheme.Asset())
	if len(legs) == 0 {
		return nil
	}
	env, err := payment.SettlementMessage(legs, c.env.MessageContext(c.bic, c.env.CentralBankBIC))
	if err != nil {
		return fmt.Errorf("server: %s could not build the settlement instruction for %s: %w", c.bic, closed.ID, err)
	}
	return c.upload(ctx, env)
}

// unhandable refuses a cut-off this institution could not release, and it is
// unhanded's guard: the same question, asked while the answer is still free.
func (c *ClearingHouse) unhandable(ctx context.Context, cycle payment.ClearingCycle) error {
	files, err := c.ops.ListHeldFiles(ctx, cycle.ID)
	if err != nil {
		return fmt.Errorf("server: %s cannot read the files it is holding for %s: %w", c.bic, cycle.ID, err)
	}
	held := make(map[payment.PaymentID]bool)
	for _, f := range files {
		for _, h := range f.Transactions {
			held[h.PaymentID] = true
		}
	}

	var missing []payment.PaymentID
	for _, id := range cycle.PaymentIDs {
		if !held[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("server: %s holds no output file for %v in %s, so settling it would move reserves no receiving bank could be told about: %w",
		c.bic, missing, cycle.ID, payment.ErrCycleNotReleasable)
}

// settlementLegs turns a cycle's net positions into the legs of an instruction:
// one per member with something to discharge, each against the CENTRAL BANK.
func (c *ClearingHouse) settlementLegs(closed payment.ClearingCycle, asset ledger.AssetCode) []payment.SettlementLeg {
	return payment.SettlementLegsOf(closed, asset, c.env.CentralBankBIC)
}

// SettleUninstructed discharges the cut-offs no settlement agent will ever
// answer for.
func (c *ClearingHouse) SettleUninstructed(ctx context.Context) []node.Problem {
	ctx = node.WithActor(ctx, c.bic)

	cycles, err := c.ops.ListCycles(ctx)
	if err != nil {
		return []node.Problem{{Institution: c.bic, Detail: fmt.Sprintf("reading the cycles nothing was instructed for: %v", err)}}
	}
	var problems []node.Problem
	for _, cycle := range cycles {
		if cycle.Status != payment.CycleClosed || !payment.NetsToNothing(cycle) {
			continue
		}
		// The same refusal instructSettlement makes, for a cut-off that moves no
		// reserve at all.
		if err := c.unhandable(ctx, cycle); err != nil {
			problems = append(problems, node.Problem{Institution: c.bic, Detail: err.Error()})
			continue
		}
		// The original message is named as unavailable, by Reject's convention and
		// for its reason: there is no settlement instruction to refer back to, and
		// inventing a message id no institution sent would be worse than saying so.
		if err := c.tellSettled(ctx, cycle.ID,
			payment.OriginalMessage{MsgID: node.NotProvided, MsgDefIdr: node.NotProvided},
			payment.TransactionStatusReport{
				TxID:   string(cycle.ID),
				Status: iso20022.TransactionStatusSettlementCompleted,
			}); err != nil {
			problems = append(problems, node.Problem{Institution: c.bic, Detail: err.Error()})
		}
	}
	return problems
}

// receiveSettlementStatus is the clearing house acting on what the CENTRAL BANK
// said about a settlement instruction. The two outcomes are not symmetrical,
// and the asymmetry is a fact about who is waiting for what.
func (c *ClearingHouse) receiveSettlementStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A status naming no cycle. The central bank quotes the cycle it was
			// asked about even when it refuses, so this is a file this institution
			// cannot act on and has nobody to ask about.
			return fmt.Errorf("server: %s got a settlement status from %s naming no cycle", c.bic, from)
		}
		if r.Status != iso20022.TransactionStatusSettlementCompleted {
			c.env.Log.Error("server: settlement refused",
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

// tellSettled is what finality releases: the output files the receiving banks
// have been waiting for, and an ACSC per payment for the banks that submitted
// them.
func (c *ClearingHouse) tellSettled(ctx context.Context, id payment.CycleID, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	// This institution's OWN copies move first, in one unit of work, and what comes
	// back is what they say; a cycle whose copies could not all be marked tells
	// nobody anything. See payment.SettleAtCSMTx.
	settled, err := c.ops.SettleAtCSM(ctx, id)
	if err != nil {
		return fmt.Errorf("server: %s was told %s settled and cannot record it: %w", c.bic, id, err)
	}
	// The files first, and what could not be released does NOT stop the advices:
	// they are two different banks' news about the same batch.
	released := c.releaseFiles(ctx, id, settled)
	for _, p := range settled {
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			return errors.Join(released, fmt.Errorf("server: %s was told %s settled and holds no %q scheme to say who submitted %s: %w",
				c.bic, id, p.Scheme, p.ID, payment.ErrSchemeNotFound))
		}
		c.env.Journal.Outcome(node.TransactionOutcome{DecidedBy: c.bic, Payment: p.ID, Status: r.Status})
		if err := c.forward(ctx, payment.SubmitterOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent), orig,
			payment.TransactionStatusReport{
				EndToEndID: endToEndOf(p),
				TxID:       string(p.ID),
				Status:     r.Status,
			}); err != nil {
			return errors.Join(released, err)
		}
	}
	return released
}

// receiveReturnStatus is the clearing house acting on the settlement agent's
// answer about a RETURN: it tells the bank that asked, and on a yes it releases
// the message to the bank that did not.
func (c *ClearingHouse) receiveReturnStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A return status naming no payment. The settlement agent quotes the
			// payment it was asked about even when it refuses, so this is a file this
			// institution cannot act on and has nobody to ask about.
			return fmt.Errorf("server: %s got a return status from %s naming no payment", c.bic, from)
		}
		id := payment.PaymentID(r.TxID)
		p, err := c.ops.GetPayment(ctx, id)
		if err != nil {
			return fmt.Errorf("server: %s was told about the return of %s and cannot read it: %w", c.bic, r.TxID, err)
		}
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			return fmt.Errorf("server: %s was told about the return of %s and holds no %q scheme to say who asked: %w",
				c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
		}
		returner := payment.ReturnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
		// The SETTLEMENT AGENT decided this, not the clearing house, and the message
		// says so. It is the one hop in this system where the sender and the
		// originator are different institutions; see forwardDecision.
		errs := []error{c.forwardDecision(ctx, returner, from, orig, []payment.TransactionStatusReport{r})}

		held, err := c.ops.GetHeldReturn(ctx, id)
		holding := err == nil
		switch {
		case holding:
			if err := c.ops.DropHeldReturn(ctx, id); err != nil {
				errs = append(errs, fmt.Errorf("server: %s was told about the return of %s and could not drop the message it held: %w",
					c.bic, id, err))
			}
		case !errors.Is(err, payment.ErrHeldReturnNotFound):
			errs = append(errs, fmt.Errorf("server: %s was told about the return of %s and cannot read what it is holding: %w",
				c.bic, id, err))
		}
		if holding && r.Status == iso20022.TransactionStatusSettlementCompleted {
			errs = append(errs, c.releaseReturn(ctx, held, id))
		}
		if r.Status == iso20022.TransactionStatusSettlementCompleted {
			// This institution's own copy, which nothing else writes: the return's two
			// customer legs land in the two BANKS' databases.
			if _, err := c.ops.CompleteReturn(ctx, id); err != nil {
				errs = append(errs, fmt.Errorf("server: %s could not record the return of %s: %w", c.bic, id, err))
			}
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
	return nil
}

// endToEndOf is the payer's own reference for a payment, or the EPC's
// convention where there is none.
func endToEndOf(p payment.Payment) string {
	if p.EndToEndID == "" {
		return node.NotProvided
	}
	return p.EndToEndID
}
