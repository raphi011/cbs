package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// A CentralBank is the settlement agent: the third institution, the only one
// that moves reserves, and the EBICS host the clearing house and every member
// bank dial.
//
// It satisfies centralbank.Institution, which api/centralbank declares, and it
// carries two acts that are the OPERATOR's over a deployment rather than this
// institution's over its own books: listing every bank the deployment holds, and
// rebuilding them.
//
// It is the shortest set of handlers in this package, and the shortness is the
// whole distinction between clearing and settlement. The clearing house decides
// WHICH payments are in a batch and what each bank's net position is; this
// institution decides only whether those positions can be discharged, and
// discharges them. Across a CUT-OFF it never sees an individual payment: it is
// instructed about a cycle, and nothing in settlementOps turns one into the
// payments inside it.
//
// A RETURN names one settled payment because that is what a return is, and the
// bank that asked put the identifier in the message; this institution still
// cannot enumerate anything or find a payment it was not told about. What makes
// a return this institution's work is what makes a cut-off its work — reserves
// move.
//
// # It dials nobody
//
// Every other institution here holds at least one connection out. This one holds
// none, and that is the topology rather than an omission: the subscriber is
// always the client, so a member bank comes to collect its statements and the
// clearing house comes to collect its answers. There is no callback into either.
//
// # What it does NOT do
//
// The pacs.009 it collects carries the legs the clearing house computed, and
// this institution reads none of the amounts in them: it takes the CYCLE the
// legs name and calls SettleCycleTx, which recomputes the batch from the cycle's
// own stored net positions. A real settlement agent has no cycle row of its own
// — the ancillary system's positions arrive only in the file — so it settles
// what it was TOLD, and a leg that disagreed with the sender's own books would
// be the sender's problem.
//
// # It holds a settlementOps, which is three methods wide
//
// SettleCycle, SettleReturn and ReceiveLodgement are on that interface and on no
// other, so a bank's or a clearing house's handler cannot NAME any of them. That
// is not a ban on those handlers moving money, because these interfaces narrow
// by method and not by book; the recorder in books_test.go is what watches for
// that.
type CentralBank struct {
	d *Deployment

	// net is this institution's whole view, and it has ONE caller: Network,
	// which api/centralbank's surface reads every request through. Everything
	// else here goes through ops.
	net *payment.CentralBankNetwork
	ops settlementOps

	bic iso20022.BIC

	// host is the EBICS side the clearing house and every member bank dial: one
	// download queue apiece, and the log of the orders each has uploaded.
	host *ebics.Server
}

func (c *CentralBank) Network() *payment.CentralBankNetwork { return c.net }
func (c *CentralBank) Log() *slog.Logger                    { return c.d.log }

// EBICS is this institution's file-transfer endpoint, mounted on its own
// listener. See ebics.Server.ServeHTTP.
func (c *CentralBank) EBICS() http.Handler { return c.host }

// Reset is the deployment's, served here because this is where the operator's
// console is. See Deployment.Reset.
func (c *CentralBank) Reset(ctx context.Context) error { return c.d.Reset(ctx) }

// AdvanceDay is the deployment's too, and for the same reason: a business day
// drives all N+2, and a deployment is not an institution. See
// Deployment.AdvanceDay.
//
// It answers in the DTO rather than in the deployment's own DayReport, which is
// the one place in this package where a surface's shape reaches back into it.
// The reason is that the value has nowhere else to live: a business day is the
// deployment's act, the deployment is a composition root, and no package
// api/centralbank imports may name one. So the report is rendered here, where
// its type is known, rather than crossed as something the surface would have to
// name to map.
//
// The report comes back on the error path too. A day that failed at some phase
// still moved everything up to it, and a caller that was handed nothing would be
// unable to tell that from a day that did nothing at all.
func (c *CentralBank) AdvanceDay(ctx context.Context) (api.DayReportDTO, error) {
	report, err := c.d.AdvanceDay(ctx)
	return toDayReportDTO(report), err
}

// BusinessDate is what day this deployment is on, and why it is or is not a
// settlement day. See Deployment.BusinessDate.
func (c *CentralBank) BusinessDate() api.BusinessDateDTO {
	return toBusinessDateDTO(c.d.BusinessDate())
}

// Members answers every bank this deployment holds a database for, each read out
// of its own database, ascending by address.
//
// # No institution is asked, because none of them knows
//
// It is not the clearing house's read: the csm shape has no banks table, and the
// roster is not a substitute — a bank founded and not yet admitted has no roster
// entry and is precisely the bank this listing exists to show, its empty
// settlement references being what says so.
//
// payment.Stores.Banks is the question with an answer: every bank whose DATABASE
// exists. Its doc says nothing in the domain calls it and nothing should — an
// institution asking which other institutions exist is a crossing — and this is
// not the domain asking. It is the process asking what it has, and then asking
// each of those banks for its own row.
//
// # It reaches past this institution's own network, which is why it is here
//
// Every other method on this type goes through c.net. This one goes through the
// deployment, to N databases that are not the settlement agent's. It is the
// operator's act over a deployment rather than the settlement agent's over its
// own books, and the surface package's router is where that exception is written
// down.
//
// # The cost, stated rather than implied
//
// One call opens and reads every bank's database. It is the widest read in this
// process and there is no narrower version of it — a list of N banks is N banks'
// rows and each row is in a different file.
func (c *CentralBank) Members(ctx context.Context) ([]*payment.Bank, error) {
	bics, err := c.d.nets.Stores().Banks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*payment.Bank, 0, len(bics))
	for _, bic := range bics {
		id := payment.ParticipantID(bic)
		net, err := c.d.nets.Bank(ctx, id)
		if err != nil {
			return nil, err
		}
		p, err := net.GetBank(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Queueing, and working through what has arrived
// ---------------------------------------------------------------------------

// enqueue addresses one document to one subscriber by putting it in that
// subscriber's download queue. See ClearingHouse.enqueue, which is the same act
// one institution over.
func (c *CentralBank) enqueue(ctx context.Context, to iso20022.BIC, env iso20022.Envelope) error {
	t, err := orderTypeOf(env.Document)
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
	c.d.journal.file(FileMoved{From: c.bic, To: to, OrderType: t, OrderID: id})
	return nil
}

// work runs this institution through the orders uploaded to it and not yet
// answered, oldest first. See ClearingHouse.work, which has the same shape and
// the same reason for answering every order on its acknowledgement.
//
// Three arms, and every one of them moves central-bank money: a cut-off's
// positions being discharged, one settled payment being sent back, and a member
// lodging cash onto its own reserve.
//
// A file whose acknowledgement could not be written stays on this list and is
// worked again tomorrow, which is where the three arms' idempotency earns its
// place: a settlement is refused by SettleCycleTx's own state guard, a return by
// the idempotency key on its reserve reversal, and a lodgement by the key derived
// from the request's message id. None of the three can post twice.
func (c *CentralBank) work(ctx context.Context) []Problem {
	ctx = withActor(ctx, c.bic)

	pending, err := c.host.Pending(ctx)
	if err != nil {
		return []Problem{{Institution: c.bic, Detail: fmt.Sprintf("reading the orders waiting to be worked through: %v", err)}}
	}
	var problems []Problem
	for _, order := range pending {
		answer, detail := c.host.Processed, ""
		if err := c.handle(ctx, iso20022.BIC(order.Subscriber), order.Payload); err != nil {
			problems = append(problems, Problem{Institution: c.bic, OrderID: order.ID, Detail: err.Error()})
			answer, detail = c.host.Rejected, err.Error()
		}
		if err := answer(ctx, order.ID, detail); err != nil {
			problems = append(problems, Problem{Institution: c.bic, OrderID: order.ID,
				Detail: fmt.Sprintf("recording what became of this order: %v", err)})
		}
	}
	return problems
}

// handle dispatches on the document the bytes carry.
//
// The lodgement is the only one of the three a MEMBER asks for. A settlement
// instruction comes from the clearing house and a return comes from the clearing
// house on a bank's behalf; a camt.050 is an account holder asking its servicer
// to credit its account, which is a different relationship from either.
//
// A pacs.008 or a pacs.003 arriving here would be a customer payment uploaded to
// the settlement agent, which no institution in this deployment does and which
// this one could not act on; it becomes a line in the day's report rather than a
// shrug.
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
// See unreadable.
func (c *CentralBank) answerUnreadable(ctx context.Context, from iso20022.BIC, cause error) error {
	env, err := unreadable(c.d.messageContext(c.bic, from), cause)
	if err != nil {
		return errors.Join(fmt.Errorf("server: %s could not build the FF01 for %s: %w", c.bic, from, err), cause)
	}
	return c.enqueue(ctx, from, env)
}

// receiveSettlement is the central bank answering a settlement instruction:
// ACSC, or RJCT with the code its own refusal maps to.
//
// # AM04 is the answer this makes expressible
//
// A net payer whose reserve cannot cover its position is refused inside
// SettleCycleTx, and the whole batch fails with it — which is what a settlement
// window is. It is AM04 waiting in the clearing house's download queue,
// addressed to the party that can act on it: it holds the cycle, and it is the
// one that would re-present or unwind.
//
// "Nothing is posted anywhere" holds because the check runs ABOVE the netting
// transaction, so the central bank has written nothing of its own; and because
// advise runs only on the success path, so no member is queued a statement and
// the clearing house fans no ACSC out. There is nothing for a member to undo
// because no member was ever told.
//
// The code comes from payment.ReasonFor, which maps ledger.ErrInsufficientBalance
// to AM04 through borrowedReasons — the same route deposit.ErrInsufficientAvailable
// takes for a customer's empty account. Two layers, one code, and it is the
// right one both times: "the account cannot cover this".
//
// # One error is reported instead, and never answered
//
// A settlement instruction uploaded twice names a cycle this network has already
// settled, and SettleCycleTx refuses it with ErrCycleNotClosed — a statement
// about THIS system's state and not about the sender's file. payment's
// reasonTable gives it the EMPTY code for exactly that reason, and ReasonFor
// would turn it into MS03 and tell the clearing house that a cycle which in fact
// settled was rejected. So it goes in the day's report against the order id and
// is not answered.
func (c *CentralBank) receiveSettlement(from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs009, ctx context.Context) error {
	body := doc.FICdtTrf
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: hdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}

	legs, err := payment.ReadSettlement(doc)
	if err != nil {
		// A count that does not match what arrived. The file parsed, so this is not
		// FF01's case — but a leg lost in transit is a bank that does not get paid,
		// and settling the survivors as if they were the whole instruction is the
		// one thing a settlement agent must never do. It is answered against no
		// cycle, because the instruction cannot be trusted to name one.
		return c.answer(ctx, from, orig, notProvided, notProvided, iso20022.TransactionStatusRejected, err)
	}
	id, err := cycleOf(legs)
	if err != nil {
		return c.answer(ctx, from, orig, notProvided, notProvided, iso20022.TransactionStatusRejected, err)
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
//
// # Two callers, and everything below holds for both
//
// A CUT-OFF and a RETURN both end here, with one statement per member whose
// reserve account moved: two members at a cut-off that netted, and always
// exactly two on a return. The statements differ only in what they name — a
// cycle id or a payment id — which the receiving bank does not act on either.
//
// # Why the members and not the clearing house
//
// The clearing house is the party that INSTRUCTED and is the one this
// institution answers; the members are the parties whose accounts moved, and
// they are not parties to that conversation at all. Each gets a file about its
// own account and no other, which is the whole of what an account servicer tells
// an account holder.
//
// # After the unit of work, and before the answer
//
// After, for the reason every door has: a statement queued from inside
// SettleCycleTx would be one a bank could book against a settlement the store
// then rolled back.
//
// Before the answer, and that used to be load-bearing in a way it no longer can
// be. The two queues share NO ORDERING: a member's statement sits at the
// settlement agent and its ACSC sits at the clearing house, and nothing about
// the order they were written in survives the trip. What guarantees the mirror
// leg is booked before the creditor legs draw on it is the BANK's own collection
// order — the central bank first, then the clearing house — which is a decision
// each bank makes about its own operations. See AdvanceDay.
//
// The order here is kept anyway because it costs nothing and because the answer
// is the last thing a caller should see: an ACSC written before the statements
// would say the batch was complete while a queue write could still fail.
//
// # A failed enqueue is not a failed settlement
//
// The reserves have moved and the cycle is Settled: that is final, and this
// institution cannot unsay it. So a failure comes back as an error, which lands
// in the day's report, rather than being retried or swallowed.
//
// What it costs is narrower than it was. Writing to a queue cannot fail on the
// RECIPIENT's account — there is nobody to be unreachable — so the fan-out can
// no longer be truncated by one member being down. What is left is a member with
// no enrolment, which is a member of the roster this deployment never gave a
// queue, and that is a wiring fault rather than an outage.
func (c *CentralBank) advise(ctx context.Context, statements []payment.SettlementStatement) error {
	for _, st := range statements {
		env, err := payment.StatementMessage(st, c.d.messageContext(c.bic, st.Agent))
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
//
// # Why this institution and not a bank
//
// The reserve reversal between the two members' settlement accounts is
// central-bank money, so no member bank and no clearing house may make it.
// Returns settle immediately in this system rather than being cleared and netted
// in a later R-cycle, so a return IS a settlement act and belongs where
// settlement does.
//
// What is left here is the reserve reversal and nothing else: the pacs.004
// travels bank to bank as it does in a real network, each bank posts its own
// customer leg, and each books its reserve mirror from the camt.053 this handler
// queues.
//
// # It reads the parties off the MESSAGE
//
// payment.ReadReturn, and not a payment row: this institution holds none, never
// saw the payment clear, and could not look one up. Both agents and the amount
// come out of OrgnlTxRef.
//
// # The order: advise, then answer
//
// The camt.053 goes into BOTH banks' queues before the pacs.002 goes into the
// clearing house's, for advise's reason. What makes the other bank's refund draw
// on a funded suspense is that bank's own collection order rather than this one.
//
// # One return per file, and the sender's own count must agree
//
// A pacs.004 can carry many, and this system's returning banks send one. Two
// checks, stated separately because they refuse different things: the count that
// ARRIVED, and the count the sender CLAIMED — the same check
// payment.ReadSettlement makes on a settlement instruction, because a
// transaction lost in transit is a payer who never gets their money back. A file
// failing either is refused WHOLE, for cycleOf's reason.
//
// # What is answered, and what is reported
//
// A return uploaded twice names a payment this network has already settled the
// return of, and SettleReturn refuses it with ErrReturnAlreadySettled — a
// statement about THIS system's state and not about the sender's file.
// reasonTable gives it the empty code for that reason, and ReasonFor would turn
// it into MS03 and tell the returning bank that a return which in fact happened
// was rejected. It goes in the day's report instead. There is no payment row on
// this path, so the repeat is caught where the only durable trace of a settled
// return is: the idempotency key on the reserve reversal, in this bank's own
// ledger.
//
// Everything else the domain refuses is answered with the code ReasonFor maps it
// to, because a refusal a counterparty can act on is completed work. There are
// two, both about this institution's own book: a creditor's bank whose reserves
// cannot cover the reversal is ledger.ErrInsufficientBalance and therefore AM04,
// and an agent BIC naming no member is ErrParticipantNotFound. A file that
// cannot be READ is answered too, by the same rule.
//
// ErrSchemeUnsupportedReturn is not among them: SettleReturnTx reads no payment
// and no scheme, because whether a scheme's rule book allows returns is a
// question for the bank that composes the pacs.004 and not for the agent that
// moves the reserves after one has been sent.
//
// # A return that names no payment is answered, and dies one hop later
//
// returnedEndToEnd substitutes notProvided when there is no OrgnlEndToEndId, so
// the report always carries something to refer back by. What refuses the message
// is payment.ReadReturn, which will not read a transaction with no OrgnlTxId, so
// this institution answers RJCT quoting an empty transaction id because that is
// what it was given.
//
// Where it dies is one hop on: the clearing house turns an answer back into a
// payment by OrgnlTxId, which this message does not have, so the refusal becomes
// a line in the report THERE and the returning bank is told nothing. The limit
// is the clearing house's — it has no way to resolve a payment by its end-to-end
// reference.
//
// Why ReadReturn refuses it at all is about money: SettleReturnTx derives the
// reserve reversal's idempotency key from the payment id, so an empty one would
// move reserves between two real banks under a key every nameless return shares.
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
//
// Debit Settlement Assets / Credit Reserve: <member>. A member cannot make these
// entries itself: it uploads a camt.050, and this handler is the only thing in
// the system that posts them.
//
// It reads the parties off the message rather than off a row, because a
// settlement agent holds no roster and has never heard of this system's bank
// ids. See payment.ReadLodgement, and payment.ReceiveLodgementTx on why the
// quoted account number is a CHECK against its own row rather than a lookup.
//
// It takes the HEADER as well as the document, which no other reader here does:
// a camt.050 comes straight from the member, one hop, so the header and the body
// must agree — where a relayed message's Fr is the last hop rather than the
// originator and a comparison would refuse every legitimate one.
//
// # It answers camt.025 and NOT pacs.002
//
// A status report is about a payment transaction, and a lodgement is not one: it
// moves no customer's money, belongs to no scheme and no cycle, and has no
// OrgnlTxId to quote. The reason therefore travels as PROSE, because camt.025's
// StsCd is a code set nothing here can check and its Desc is free text — which
// is why reasonTable gives these sentinels the empty code.
//
// # Everything the domain refuses is answered, and everything else is reported
//
// A member it holds no account for, an asset it holds no account for that member
// in, and an account number that is not the one it holds are all answered with a
// refusing receipt: each is a judgement about the request that the sender can act
// on. lodgementRefusals is that list, by name.
//
// A REDELIVERED lodgement is not. payment.ReceiveLodgementTx keys the posting on
// the request's own message id, so the repeat is caught by the ledger rather
// than posted again, and answering it would tell the member its lodgement was
// refused when in fact it happened.
//
// NEITHER IS A STORE FAILURE, and this is the one place in this package where
// getting that wrong costs money that nothing can recover. Every other refusing
// handler answers a sender that has posted nothing. A lodging member has ALREADY
// COMMITTED ITS LEG — payment.LodgeReservesTx posts Debit Reserve / Credit Vault
// Cash before the camt.050 goes up, because a camt.025 carries no amount — so a
// refusal here is read as "this did not happen", and one sent because the
// agent's Store.Update exhausted its retry budget is a lie about money: the
// member's mirror stays raised, the agent's book never moved, and
// Bank.receiveLodgementReceipt cannot unwind it because the amount is not on the
// receipt.
//
// The discrimination is checkPartyTx's, one layer down and for the same reason.
// It is made BY NAME rather than through payment.ReasonFor, and that is forced
// rather than chosen: these sentinels carry the empty code, so ReasonFor cannot
// tell them from an error it has never heard of.
//
// So anything not on the list goes in the day's report and the member is told
// nothing. That leaves its mirror overstated too — the difference is that a
// reported failure is a visible break an operator can re-drive, and a false
// refusal is a break that looks like a completed conversation.
//
// # It answers the SENDER, and the sender is the member
//
// Unlike a settlement or a return, there is no third institution in this
// conversation. The clearing house is not a party to a member's liquidity
// management, routes nothing here, and would have nothing to do with the answer.
func (c *CentralBank) receiveLodgement(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Camt050) error {
	in, err := payment.ReadLodgement(hdr, doc)
	if err != nil {
		// Answered against the message id off the document rather than the reader's
		// output, because the commonest way to be here is that the reader refused
		// and produced nothing. A message carrying no id at all cannot be correlated
		// by the member that uploaded it, so it goes in the report instead.
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

// lodgementRefusals is everything a settlement agent may JUDGE about a lodgement,
// and receiveLodgement answers exactly these with a refusing camt.025.
//
// It is payment.ReceiveLodgementTx's "What it refuses, and why each is answerable"
// section as a list the compiler holds, and the two must agree: a sentinel that
// section adds and this list does not goes in the day's report, which is the
// safe direction to be wrong in but is still wrong. TestALodgementRefusalIsAJudgement
// is what stops the pair drifting.
//
// ErrInvalidPaymentAmount and the BIC check are on the list although
// payment.ReadLodgement refuses both before this handler's act ever runs. They are
// belt-and-braces on the domain call rather than dead code, and if a second reader
// ever reaches ReceiveLodgement — settlementOps exports it — they are judgements
// about the request, which is the only question this list asks.
//
// What is deliberately NOT here is anything the agent SUFFERED rather than
// decided: a store failure, a cancelled context, a retry budget that ran out. See
// receiveLodgement's doc for why answering one of those is a lie about money.
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
//
// The cause is NOT returned once it has been answered, for c.answer's reason: a
// refusal the counterparty was told about is completed work, and returning it as
// well would make every refused lodgement a line in the report too.
func (c *CentralBank) acknowledgeLodgement(ctx context.Context, to iso20022.BIC, r payment.LodgementReceipt) error {
	env, err := payment.LodgementReceiptMessage(r, c.d.messageContext(c.bic, to))
	if err != nil {
		return fmt.Errorf("server: %s could not build its camt.025 for %s: %w", c.bic, to, err)
	}
	return c.enqueue(ctx, to, env)
}

// returnedEndToEnd is the payer's own reference for a returned payment, as the
// RETURNING BANK quoted it, or the EPC's convention where there is none.
//
// Quoted back rather than derived, because that is what a bank matches its
// outstanding instruction against — endToEndOf makes the same convention on the
// other side of the network, and a status quoting the payment's id in this
// element would match nothing the returning bank ever sent.
func returnedEndToEnd(tx iso20022.ReturnTransaction) string {
	if tx.OrgnlEndToEndId == "" {
		return notProvided
	}
	return tx.OrgnlEndToEndId
}

// cycleOf is which closed cycle an instruction discharges, taken from the legs
// themselves.
//
// Every leg of one instruction must name the same cycle, and this REFUSES a file
// whose legs disagree rather than settling the first cycle it sees.
// payment.SettlementLeg carries its own reference precisely because a pacs.009
// is capable of carrying legs from several cycles at once, and a real settlement
// agent would settle each of them; this system's clearing house emits one cycle
// per instruction, so a file naming two is one this institution has no rule for.
// Discharging one and dropping the other would leave a closed cycle that nobody
// ever settles and nobody ever hears about.
//
// An instruction with no legs cannot occur on the wire — payment.SettlementMessage
// refuses to build one and iso20022's own validation refuses to parse one — so
// the empty case here is a guard on a caller, not a file.
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
//
// The transaction it reports on is the CYCLE, not a payment: that is what a
// pacs.009 instructs and what the central bank decided about. The clearing house
// is the party that turns it back into per-payment news, because it is the one
// that knows which payments are in the batch.
//
// Back to the SENDER, for Bank.answer's reason: the banks whose reserves just
// moved are not parties to this conversation and hold no expectation of a file
// from it.
//
// # Two references, not one
//
// e2e and txid are separate parameters because on the return path they are
// different values, and quoting the payment id as both broke the convention
// every other per-payment status follows (endToEndOf): a bank matches an answer
// to its instruction by comparing what it SENT with what came back, and a
// payment with no client reference travels as NOTPROVIDED, not as its own id. On
// the settlement path they are one value, because a CYCLE has no end-to-end
// reference at all and the clearing house matches on the transaction id.
//
// # A cause forces RJCT
//
// Bank.answer does the same. A cause passed beside SettlementCompleted would set
// a code and a text that statusReasonOf then drops, because a pacs.002 carries
// StsRsnInf only for a rejection — the file would go out saying everything was
// fine with the reason silently deleted.
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
	c.d.journal.outcome(TransactionOutcome{
		DecidedBy: c.bic,
		Payment:   payment.PaymentID(txid),
		Status:    report.Status,
		Code:      report.Code,
		Text:      report.Text,
	})
	env, err := payment.StatusMessage(orig, []payment.TransactionStatusReport{report}, c.d.messageContext(c.bic, to))
	if err != nil {
		return errors.Join(fmt.Errorf("server: %s could not build its pacs.002 for %s: %w", c.bic, to, err), cause)
	}
	// The cause is NOT returned once it has been answered, for the reason
	// Bank.answer gives: a refusal the counterparty was told about is completed
	// work, and returning it as well would make every AM04 a line in the report
	// too.
	return c.enqueue(ctx, to, env)
}
