package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// A ClearingHouse is the CSM: every payment in the network, the cycles, the
// schemes, the roster, and the EBICS host every member bank dials.
//
// It satisfies csm.Institution, which api/csm declares; see Deployment.
//
// It sits between two banks that never address each other. Everything a bank
// learns about the far side is collected from here: it ROUTES an instruction
// onward by putting it in the receiving bank's download queue, and it CLEARS the
// answer — taking the payment into a cycle, or rejecting it — and puts that
// answer in the queue of the bank that started it.
//
// The routing is where the two flows differ and the clearing is where they do
// not. A credit transfer goes to the agent named as the creditor's, a collection
// to the agent named as the debtor's, because a push travels towards the money's
// destination and a pull towards its source. What comes back is a pacs.002
// either way, and this institution treats it the same way either way.
//
// # It also sits between the banks and the settlement agent
//
// Reaching a cut-off nets the batch and then INSTRUCTS the settlement agent to
// discharge the positions, in a pacs.009, because moving reserves is not
// something a clearing house may do. It is a subscriber there exactly as a
// member bank is: it uploads the instruction and collects the answer on a later
// download. The answer comes back here rather than to any bank, and this
// institution turns it into per-payment news — which it can and the central bank
// cannot, because it is the one that knows which payments are in the batch.
//
// # And it carries returns, HOLDING one end of the conversation
//
// A RETURN is a conversation with THREE participants, two of which never address
// each other, so the file that makes the second bank move its customer's money
// has to be carried by the institution that has seen both ends. This institution
// holds the pacs.004 until the settlement agent has said ACSC and only then
// queues it onward — see relayReturn and receiveReturnStatus. It clears nothing
// and nets nothing: a return is final the moment the settlement agent posts it.
//
// It holds a csmOps: nothing about clearing moves money, and that is what makes
// clearing and settlement different jobs. That is not a compile-time ban on
// posting — these interfaces narrow by method and never by book — so the ban
// stays the recorder's: TestTheCSMTouchesOnlyItsOwnBook holds it to that.
type ClearingHouse struct {
	d *Deployment

	// net is this institution's whole view, and it has ONE caller: Network,
	// which api/csm's surface reads every request through. Everything else here
	// goes through ops.
	net *payment.Network
	ops csmOps

	bic iso20022.BIC

	// host is the EBICS side every member bank dials: one download queue per
	// enrolled subscriber, and the log of what each has uploaded. The queues ARE
	// the routing table — there is no address to resolve and nothing that can
	// disagree with the roster, because enrolment is what creates one.
	host *ebics.Server

	// cb is this institution's own connection to the settlement agent, on which
	// it uploads settlement instructions and returns and collects the answers.
	// The clearing house is a subscriber there, exactly as a member bank is.
	cb *ebics.Client

	// held is the returns this institution has uploaded to the settlement agent
	// and has not yet heard about, keyed by the payment each names.
	//
	// It is the only state any institution in this package keeps between files,
	// and it is deliberate rather than convenient: see relayReturn for why the
	// message cannot be queued onward before finality, and for what is lost if
	// this map is.
	//
	// No lock. Only relayReturn and receiveReturnStatus touch it, and both are
	// reached only from a business day, which runs on one goroutine under the
	// deployment's own lock. The three methods that run on a REQUEST's goroutine
	// (closeCycle, settle, reject) must never read it, and none does.
	held map[payment.PaymentID]heldReturn
}

// heldReturn is one pacs.004 in flight: the document itself, and who uploaded it.
//
// The document rather than the bytes, because releasing it is rebuilding the
// envelope around an unchanged document — see releaseReturn — and re-parsing
// what this institution has already parsed would be a second answer to what the
// message says.
//
// from is the RETURNING bank, and it is what the second hop is routed against:
// the other bank is whichever of OrgnlTxRef's two agents this is not. Kept here
// rather than re-derived, because it is a fact about the connection this
// institution observed, and re-deriving it would mean asking the store which
// bank ought to have uploaded a file it can see the subscriber of.
type heldReturn struct {
	doc  *iso20022.Pacs004
	from iso20022.BIC
}

func (c *ClearingHouse) Network() *payment.Network { return c.net }
func (c *ClearingHouse) Log() *slog.Logger         { return c.d.log }

// EBICS is this institution's file-transfer endpoint, mounted on its own
// listener. It is one URL that everything POSTs to, which is the protocol's own
// shape; see ebics.Server.ServeHTTP.
func (c *ClearingHouse) EBICS() http.Handler { return c.host }

func (c *ClearingHouse) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	return c.d.Submit(ctx, req)
}

func (c *ClearingHouse) Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	return c.d.Return(ctx, id, reason, text)
}

// ---------------------------------------------------------------------------
// Queueing
// ---------------------------------------------------------------------------

// enqueue addresses one document to one subscriber by putting it in that
// subscriber's download queue.
//
// This is the whole of routing. There is no URL to look up and no actor table
// that can fall out of step with the roster: a party with no enrolment has no
// queue, and a file addressed to it has nowhere to go — which is where RC01
// comes from now, one refusal instead of two facts that could disagree.
//
// Writing to a queue cannot fail on the RECIPIENT's account, which is the one
// improvement worth naming: a statement fan-out can no longer be truncated by an
// unreachable bank, because there is nothing to reach.
func (c *ClearingHouse) enqueue(to iso20022.BIC, env iso20022.Envelope) error {
	t, err := orderTypeOf(env.Document)
	if err != nil {
		return err
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		return fmt.Errorf("server: %s marshalling for %s: %w", c.bic, to, err)
	}
	id, err := c.host.Enqueue(ebics.SubscriberID(to), t, raw)
	if err != nil {
		return fmt.Errorf("server: %s cannot address a %s to %s: %w", c.bic, t, to, err)
	}
	c.d.journal.file(FileMoved{From: c.bic, To: to, OrderType: t, OrderID: id})
	return nil
}

// upload puts a document on this institution's own connection to the settlement
// agent. See Bank.upload, which is the same act one institution over.
func (c *ClearingHouse) upload(ctx context.Context, env iso20022.Envelope) error {
	to := c.d.cfg.CentralBankBIC
	t, err := orderTypeOf(env.Document)
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
	c.d.journal.file(FileMoved{From: c.bic, To: to, OrderType: t, OrderID: id})
	return nil
}

// notEnrolled reports whether an enqueue failed because the subscriber has no
// queue here.
//
// It is the transport's one membership answer, and the clearing house turns it
// into RC01 — BankIdentifierIncorrect — which is exactly what it is. A bank
// cannot know that a BIC is unroutable; it can only know that its file was not
// answered.
func notEnrolled(err error) bool {
	return ebics.CodeOf(err) == ebics.InvalidUserOrUserState
}

// ---------------------------------------------------------------------------
// Working through what has arrived
// ---------------------------------------------------------------------------

// work runs this institution through the orders its members have uploaded and
// not yet been answered about, oldest first, taking only the types the caller
// names.
//
// The types are the caller's because a business day is ORDERED: instructions are
// routed in one phase and the banks' answers are cleared in a later one, and a
// single pass over everything pending would clear an answer to a file it had
// just routed, in the same breath, which is the interleaving a batch system does
// not do.
//
// Every order is answered on its acknowledgement, which is the seam this
// transport exists for: the uploader was told EBICS_OK and went away, so
// "processed" and "rejected" are what a subscriber's HAC download tells it
// later, and neither says anything about the payments inside the file.
func (c *ClearingHouse) work(ctx context.Context, types ...ebics.OrderType) []Problem {
	ctx = withActor(ctx, c.bic)

	var problems []Problem
	for _, order := range c.host.Pending() {
		if !slices.Contains(types, order.Type) {
			continue
		}
		err := c.handle(ctx, order)
		if err != nil {
			problems = append(problems, Problem{Institution: c.bic, OrderID: order.ID, Detail: err.Error()})
			_ = c.host.Rejected(order.ID, err.Error())
			continue
		}
		_ = c.host.Processed(order.ID, "")
	}
	return problems
}

// collect downloads the settlement agent's answers and works through them.
//
// This institution is a subscriber there, so its answers arrive the way a bank's
// do: nothing is pushed, and a clearing house that never collected would leave
// every member holding a Cleared payment whose reserves have in fact moved.
func (c *ClearingHouse) collect(ctx context.Context) []Problem {
	ctx = withActor(ctx, c.bic)

	from := c.d.cfg.CentralBankBIC
	files, err := c.cb.Download(ctx, ebics.BTD)
	if err != nil {
		return []Problem{{Institution: c.bic, Detail: fmt.Sprintf("collecting from %s: %v", from, err)}}
	}
	var problems []Problem
	for _, f := range files {
		if err := c.handleFile(ctx, from, f.Payload); err != nil {
			problems = append(problems, Problem{Institution: c.bic, OrderID: f.OrderID, Detail: err.Error()})
		}
	}
	return problems
}

// handle works through one order a member uploaded here.
func (c *ClearingHouse) handle(ctx context.Context, order ebics.Order) error {
	return c.handleFile(ctx, iso20022.BIC(order.Subscriber), order.Payload)
}

// handleFile dispatches on the document the bytes carry.
//
// The unmarshalling comes first and its failure is answerable, which is the
// whole reason `from` is an argument rather than something read out of the
// header: the header is exactly what is unreadable, and the subscriber the file
// arrived from is what the transport knows.
func (c *ClearingHouse) handleFile(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return c.answerUnreadable(from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs008:
		return c.relayCreditTransfer(ctx, from, env, doc)
	case *iso20022.Pacs003:
		return c.relayDirectDebit(ctx, from, env, doc)
	case *iso20022.Pacs004:
		return c.relayReturn(ctx, from, env, doc)
	case *iso20022.Pacs002:
		// Three kinds of status arrive here, and it takes two questions to tell
		// them apart.
		//
		// The SENDER separates a member bank's from the settlement agent's. A
		// bank's pacs.002 answers an instruction about one customer payment; the
		// central bank's answers something this institution asked IT for, and it
		// arrives down the other connection entirely. By BIC rather than by trying
		// a lookup and seeing what happens, because the central bank is not a
		// member of this network: it holds no participant row, it never submits and
		// it is never a payment's agent.
		//
		// What it was asked separates the settlement agent's two. A settlement
		// answer is about a whole CYCLE and its OrgnlTxId is a cycle id; a return
		// answer is about one PAYMENT. Reading either as the other would look an
		// identifier up in the wrong table. The message says which — OrgnlMsgNmId,
		// the definition it is answering — so this is read off the wire rather than
		// guessed at by trying both.
		switch {
		case from != c.d.cfg.CentralBankBIC:
			return c.receiveStatus(ctx, from, doc)
		case isAbout(doc, returnMsgDef):
			return c.receiveReturnStatus(ctx, from, doc)
		default:
			return c.receiveSettlementStatus(ctx, from, doc)
		}
	default:
		return fmt.Errorf("server: %s has no handler for %s", c.bic, env.AppHdr.MsgDefIdr)
	}
}

// answerUnreadable queues an FF01 for the subscriber whose file would not parse.
// See unreadable.
func (c *ClearingHouse) answerUnreadable(from iso20022.BIC, cause error) error {
	env, err := unreadable(c.d.messageContext(c.bic, from), cause)
	if err != nil {
		return errors.Join(fmt.Errorf("server: %s could not build the FF01 for %s: %w", c.bic, from, err), cause)
	}
	return c.enqueue(from, env)
}

// relayCreditTransfer hands a credit transfer on to the CREDITOR's agent: the
// bank that holds the payee, because a push travels towards the money's
// destination — and records this institution's own copy of it.
//
// # One file in, M files out, and that fan-out is what a clearing house is for
//
// A submitting bank's file carries whatever its customers handed in that
// morning, addressed to every bank in the scheme. This institution sorts it by
// creditor agent and hands each receiving bank the transactions that are for it
// and no others — which is the one act in the whole system that no bank could
// perform for itself, and it is invisible in a network where every message
// carries one payment.
//
// Everything after the sort goes through relayRecorded, which is where the order
// of the record and the relay is argued.
func (c *ClearingHouse) relayCreditTransfer(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs008) error {
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
	return c.relayRecorded(ctx, from, env, orig, ps, groups, func(idx []int) iso20022.Document {
		return creditTransferPart(doc, idx)
	})
}

// relayDirectDebit hands a collection on to the DEBTOR's agent: the bank that
// holds the payer, because a pull travels towards the money's source.
//
// One element different from relayCreditTransfer, and it is the whole direction
// of the payment. Routing a pacs.003 by CdtrAgt would send the collection back
// to the bank that uploaded it, which would answer its own instruction — and the
// resolution inside DirectDebitRequest would succeed while it did, because both
// parties resolve by address whoever is asking.
func (c *ClearingHouse) relayDirectDebit(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs003) error {
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
	return c.relayRecorded(ctx, from, env, orig, ps, groups, func(idx []int) iso20022.Document {
		return directDebitPart(doc, idx)
	})
}

// A destination is one receiving bank and the transactions of an uploaded file
// that are addressed to it, by their position in that file.
//
// Positions and not copies, because two things are indexed by them — the
// document's own transaction list and the payments this institution recorded —
// and an index is what keeps the two in step without either being rebuilt.
type destination struct {
	to  iso20022.BIC
	idx []int
}

// groupedBy sorts a file's transactions by the agent each names, keeping the
// destinations in the order the sender first named them.
//
// The order is a property of the FILE, so two runs over the same file produce
// the same output files in the same sequence — which is what a test asserting on
// a fan-out needs, and what a deployment with one goroutine can offer instead of
// N institutions running concurrently.
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

// relayRecorded is the second half of carrying a file: one output file per
// receiving bank, and this institution's own copies agreeing with what it
// queued.
//
// # The record comes BEFORE the routing, and the order is decided rather than
// incidental
//
// Recording comes FIRST, because the row is also this institution's DUPLICATE
// CHECK. Recording last would mean a replayed instruction is routed and answered
// before it is refused, and what it would be answered with is a status naming a
// payment this clearing house is holding live in a cycle — a bank acting on that
// reverses a debit that is funding a settlement (payment.RejectAtBankTx, which
// cannot tell the difference and says so). A real CSM checks for a duplicate at
// ingestion, before it validates or forwards anything, and this is that check.
//
// It is recorded for the WHOLE file in one unit of work and routed per
// destination, which is the asymmetry worth naming: the file is one instruction
// as far as the submitting bank is concerned, and M as far as the network is.
//
// An RC01 leaves a row, and the row is REJECTED in the same breath, so this
// institution's copy says what its message said: a record that it refused to
// carry this payment, which is the fact an operator asking "what happened to it"
// needs. One unroutable destination refuses only the transactions addressed to
// it; the rest of the file goes on to the banks that can be reached.
//
// The seam moves rather than closing. A record that succeeded and a relay that
// could not be QUEUED leaves a row for a file in nobody's queue. Every failure
// is joined rather than returned, for the reason a day carries on past one: a
// destination this institution could not reach must not stop the others being
// handed their share.
func (c *ClearingHouse) relayRecorded(ctx context.Context, from iso20022.BIC, env iso20022.Envelope,
	orig payment.OriginalMessage, ps []payment.Payment, groups []destination,
	part func(idx []int) iso20022.Document) error {

	var errs []error
	for _, g := range groups {
		routed, err := c.relay(env, part(g.idx), orig, refsOf(ps, g.idx), from, g.to)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if routed {
			continue
		}
		// The RC01 is already in the sender's queue and this institution said it.
		// Its own copies have to say the same thing, or it is a clearing house
		// holding Initiated payments it has told the submitting bank it will not
		// carry — and that bank, acting on the message, will have reversed its
		// customers' debits.
		for _, i := range g.idx {
			if _, err := c.ops.RejectAtCSM(ctx, ps[i].ID, iso20022.StatusReasonBankIdentifierIncorrect,
				fmt.Sprintf("no route to %s", g.to)); err != nil {
				errs = append(errs, fmt.Errorf("server: %s answered RC01 for %s and could not record it: %w", c.bic, ps[i].ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// refsOf is how a group of transactions is referred back to, read off the
// payments this institution recorded rather than off the document.
//
// Off the RECORD because a status has to quote both references and only the
// record carries the EPC's convention for a payment whose payer supplied none —
// see endToEndOf. The two agree by construction: the row was written from the
// message a moment earlier.
func refsOf(ps []payment.Payment, idx []int) []iso20022.PaymentIdentification {
	out := make([]iso20022.PaymentIdentification, 0, len(idx))
	for _, i := range idx {
		out = append(out, iso20022.PaymentIdentification{
			EndToEndId: endToEndOf(ps[i]),
			TxId:       string(ps[i].ID),
		})
	}
	return out
}

// relay forwards an instruction to the agent the message named, whichever
// direction it runs in.
//
// It reads no store, and that is a property worth keeping: the address it routes
// by came out of the message — CdtrAgt for a push, DbtrAgt for a pull — so a
// clearing house that looked the payment up to decide where to send it could not
// route a file about a payment it does not hold, which is every file in a real
// network.
//
// A RETURN's FIRST hop is routed by neither element, because its destination
// follows from the message DEFINITION, and it goes UP this institution's own
// connection rather than into a queue. Its SECOND hop does read an element and
// does not come through here — see releaseReturn.
//
// The DOCUMENT is the submitting bank's own, carrying the transactions this
// destination is to be handed and nothing else. What the split rewrites is
// NbOfTxs, because that element is a claim about the file it sits in and a file
// sorted by creditor agent is a different file. The transactions themselves
// travel unchanged, and so does GrpHdr/MsgId — which is what the receiving bank
// quotes back as OrgnlMsgId and what lets the answer be matched to the original
// all the way home. See creditTransferPart.
//
// The header is replaced outright: it says who is handing this to whom, and that
// is this hop's rather than the submitting bank's.
//
// # It says whether it routed
//
// The RC01 arm answers the sender and returns no error, because a refusal the
// sender was told about is completed work (Bank.answer's rule). Without the
// boolean its two instruction callers cannot tell a file that was queued from
// one that came back as a rejection, and would RECORD payments the clearing
// house had just told a bank it would not carry.
func (c *ClearingHouse) relay(env iso20022.Envelope, doc iso20022.Document,
	orig payment.OriginalMessage, refs []iso20022.PaymentIdentification, from, to iso20022.BIC) (bool, error) {

	relayed := iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: c.d.nextMsgID(c.bic),
			MsgDefIdr: env.AppHdr.MsgDefIdr,
			CreDt:     iso20022.ISODateTime{Time: c.d.now()},
		},
		Document: doc,
	}
	err := c.enqueue(to, relayed)
	if notEnrolled(err) {
		// RC01, and it is this institution that says it because it is the only one
		// holding the queues.
		// One status covers the whole group: they were all addressed to the same
		// unreachable bank, so they all get the same answer.
		return false, c.answer(from, orig, refs, iso20022.StatusReasonBankIdentifierIncorrect, err.Error())
	}
	return err == nil, err
}

// relayReturn uploads a return to the SETTLEMENT AGENT, and keeps a copy for the
// bank that has not heard about it yet.
//
// # The first hop
//
// Not to a bank, and that is the whole routing decision of the way OUT. A return
// moves central-bank reserves back, and moving reserves is the settlement
// agent's act — so the destination is a fact about the MESSAGE DEFINITION rather
// than about anything inside the message, and this hop reads no element to route
// by. It still goes THROUGH the clearing house rather than bank to central bank
// directly because a member bank uploading a pacs.004 to the settlement agent
// would be a conversation the scheme does not have.
//
// A bulk return is NOT refused here, unlike a bulk pacs.008 or pacs.003: several
// transactions in an instruction mean several destinations, and every return in
// one file has the same first destination, so routing has no objection to make.
// The settlement agent has one and makes it.
//
// # The second hop, which is why this institution keeps state
//
// A pacs.004 carries OrgnlTxRef, so it names both agents, and the message has a
// SECOND destination: the bank that did not ask for the return and has to post
// the leg the returning bank does not hold — whichever of the two this
// institution did not collect the file from.
//
// It cannot be queued now. The returning bank may still be refused, and a bank
// that had already posted its customer leg against a refused return would have
// moved a customer's money for nothing, with no file in the flow that would ever
// tell it. So the message waits here until an ACSC is collected, and
// receiveReturnStatus is what releases it.
//
// # Where this state lives, and what is lost when it is lost
//
// In memory, in one map. What a restart costs is exact: a return uploaded but
// not yet answered is gone, so the second bank is never queued the pacs.004 and
// never posts its leg. The reserves have moved and are final, the returning
// bank's customer has been debited or credited, and the payment stays Settled
// for ever with half a return standing in one book. There is no remedy from
// inside this flow; payment/recon is what makes it visible.
//
// Queueing immediately and having the other bank tolerate a later refusal is not
// an alternative, because there is no such tolerating: that bank posts after
// finality and cannot refuse. Holding trades a durability gap for a correctness
// one, and the correctness one is on a customer's account.
func (c *ClearingHouse) relayReturn(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs004) error {
	body := doc.PmtRtr
	// Unmarshal refuses a pacs.004 with no transactions (iso20022's
	// PaymentReturn.validate), so there is always a first one to refer back by.
	first := body.TxInf[0]
	// Held under the payment the answer will name, which is the only thing the two
	// messages have in common. A return that names no payment is uploaded and NOT
	// held: the answer to it names none either, so nothing could ever match it and
	// an entry under the empty key would sit here for ever. That message dies at
	// the settlement agent's answer instead.
	if first.OrgnlTxId != "" {
		c.held[payment.PaymentID(first.OrgnlTxId)] = heldReturn{doc: doc, from: from}
	}
	return c.upload(ctx, iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(c.d.cfg.CentralBankBIC),
			BizMsgIdr: c.d.nextMsgID(c.bic),
			MsgDefIdr: env.AppHdr.MsgDefIdr,
			CreDt:     iso20022.ISODateTime{Time: c.d.now()},
		},
		// The document travels unchanged, for relay's reason: it is what the
		// returning bank said and is not this institution's to rewrite.
		Document: doc,
	})
}

// releaseReturn queues the held pacs.004 for the bank that did not ask for the
// return, now that the settlement agent has made it final.
//
// # It is routed by the message and not by the store
//
// The two agents are on OrgnlTxRef and the returning bank is the one this
// institution took the file FROM, so the recipient is a subtraction rather than
// a lookup. receiveReturnStatus does read the store to address the ANSWER — it
// has only a payment id to go on there — but the message being released carries
// its own parties.
//
// A payment between two accounts at ONE bank has both agents equal, so the
// subtraction leaves that same bank and the file goes back to it. That is
// correct and not a degenerate case: that bank holds both legs and
// payment.PostReturnLegTx posts them one call at a time.
//
// # A held return with no agents cannot be released
//
// It also cannot happen: payment.ReadReturn refuses a pacs.004 whose OrgnlTxRef
// names no agents, and the settlement agent answers that RJCT, so an ACSC
// implies the document is readable. The guard is here anyway because the cost of
// being wrong is a nil dereference, which takes the day down; an error is a line
// in the report.
func (c *ClearingHouse) releaseReturn(held heldReturn, id payment.PaymentID) error {
	ref := held.doc.PmtRtr.TxInf[0].OrgnlTxRef
	if ref == nil || ref.DbtrAgt == nil || ref.CdtrAgt == nil {
		return fmt.Errorf("server: %s settled the return of %s and the message names no agents to release it to", c.bic, id)
	}
	to := ref.DbtrAgt.FinInstnId.BICFI
	if to == held.from {
		to = ref.CdtrAgt.FinInstnId.BICFI
	}
	return c.enqueue(to, iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(c.bic),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: c.d.nextMsgID(c.bic),
			MsgDefIdr: returnMsgDef,
			CreDt:     iso20022.ISODateTime{Time: c.d.now()},
		},
		Document: held.doc,
	})
}

// receiveStatus is the clearing house acting on what the payee's bank said, and
// then telling the payer's bank.
//
// This is where a payment becomes Accepted or Rejected, and both are the
// clearing house's act rather than either bank's:
//
//   - ACCP: the payment goes into the open cycle for its scheme. Which cycle a
//     payment clears in is not a bank's decision, so ErrCycleNotOpen is not a
//     bank's refusal either — it comes back as TM01, "invalid cut-off time".
//   - RJCT: the payment is rejected with the code the payee's bank chose, and
//     dropped from any cycle it had reached.
//
// # Who is told, and why it can be two banks
//
// The answer goes to the bank that SUBMITTED, looked up from the payment rather
// than taken from the file's subscriber — the subscriber is the bank that just
// decided, and the submitter is a third party neither of them addressed. Which
// bank submitted is the scheme's direction and nothing else. See submitterOf.
//
// A REJECTION can need a second recipient, and only on a pull. By the time the
// clearing house refuses a collection the payer's bank has already posted the
// debtor leg, and that bank is not the submitter — a rejection that never
// reached it would leave the payer's money in a clearing suspense against a
// payment this network records as rejected: nobody would settle it and nobody
// would give it back.
//
// On a push the two are the same bank and there is one file. On a pull where the
// payer's bank refused ITSELF there is no leg to give back, so there is one file
// again.
//
// # This handler does two writes, and the second may not happen
//
// Rejecting is the clearing house's half; reversing the payer's debit is the
// payer's bank's, on a later download and in another unit of work. If the
// queueing fails there is nobody to answer, so it becomes a problem in the day's
// report — the system fails a test rather than quietly telling a payer their
// money is back.
func (c *ClearingHouse) receiveStatus(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs002) error {
	orig, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A status that names no transaction. Nothing in this flow produces one
			// for the clearing house — a bank's answer always quotes the payment it
			// is about — so it is a file this institution cannot act on and has
			// nobody to ask about.
			return fmt.Errorf("server: %s got a status from %s naming no transaction", c.bic, from)
		}
		id := payment.PaymentID(r.TxID)

		// What the ANSWERING BANK said, kept before this institution decides
		// anything, because clear rewrites r on the path where the clearing house
		// turns an acceptance into its own rejection. An ACCP is what says that bank
		// wrote its own copy of this payment — and, on a pull, posted the debtor leg
		// with it; see tell.
		answeringBankAccepted := r.Status == iso20022.TransactionStatusAccepted

		var decided payment.Payment
		var err error
		if r.Status == iso20022.TransactionStatusRejected {
			decided, err = c.ops.RejectAtCSM(ctx, id, r.Code, r.Text)
		} else {
			decided, r, err = c.clear(ctx, id, r)
		}
		if err != nil {
			// Already answered. A second copy of an acceptance about a payment the
			// clearing house has already taken into a cycle — or a second rejection
			// at one already rejected. Either way this sentinel says the state machine
			// forbids the move, which is a statement about THIS system and not about
			// the sender's file: payment's reasonTable gives it the empty code for
			// exactly that reason, and ReasonFor would turn it into MS03 and reject a
			// payment that was in fact accepted.
			if errors.Is(err, payment.ErrInvalidStateTransition) {
				return fmt.Errorf("server: %s was told about %s again: %w", c.bic, id, err)
			}
			return err
		}
		c.d.journal.outcome(TransactionOutcome{
			DecidedBy: c.bic,
			Payment:   id,
			Status:    r.Status,
			Code:      r.Code,
			Text:      r.Text,
		})

		if err := c.tell(ctx, decided, orig, r, answeringBankAccepted); err != nil {
			return err
		}
	}
	return nil
}

// tell queues the decision for every bank that has to have it. See receiveStatus
// for which banks those are and why a rejected payment has two.
//
// # The MONEY message goes first, and neither is skipped because the other failed
//
// The two messages are not the same kind of news. The submitter's is the answer
// to a question it asked. The other bank's — queued only on a rejection, and
// only when that bank had answered ACCP — is what closes a payment it has
// already written down and, if it is the payer's bank, what makes it give a
// customer their money back. Queueing the informational one first and returning
// on its error leaves the refund unattempted.
//
// So the other bank's message is attempted first and both are attempted
// regardless, with the failures joined.
//
// # Which bank the second message is for
//
// THE OTHER BANK — the one that is not the submitter — which on a push is the
// creditor's agent and on a pull the payer's. Not the payer's bank alone: both
// banks keep a copy of this payment and neither can read the other's, so a bank
// that is never told stays Initiated for ever about a payment this network has
// decided.
//
// # The conjunct that survives: did that bank ever accept
//
// A bank that answered RJCT rolled its own row back — AcceptInboundTx and the
// answer are one unit of work — so there is no copy at that bank to close, and a
// pacs.002 naming a payment it holds no row for would be a file it cannot act
// on.
//
// It is passed in rather than derived here, because the two callers WITNESS it
// differently and neither witness is available to the other: receiveStatus has
// the answering bank's own pacs.002 in hand, and reject has no file at all and
// reads its own copy's status instead. This institution's own copy cannot serve
// the first case: clear rejects after AcceptAtCSM has rolled back, so the copy
// still says Initiated while the answering bank's row stands.
func (c *ClearingHouse) tell(ctx context.Context, p payment.Payment, orig payment.OriginalMessage,
	r payment.TransactionStatusReport, answeringBankAccepted bool) error {

	scheme, ok := c.ops.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("server: %s decided %s and holds no %q scheme to say who submitted it: %w",
			c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
	}
	submitter := submitterOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)

	var errs []error
	// Both addresses below are read off the PAYMENT. A payment names both agents
	// by BIC; see payment.PartyRef.
	var told iso20022.BIC
	other := p.CreditorDetails.Agent
	if other == submitter {
		other = p.DebtorDetails.Agent
	}
	if r.Status == iso20022.TransactionStatusRejected &&
		answeringBankAccepted &&
		other != submitter {
		told = other
		if err := c.forward(told, orig, r); err != nil {
			errs = append(errs, err)
		}
	}

	// The bank that submitted, which is always told. The comparison against
	// `told` is what stops an ON-US payment, where the two agents are one bank,
	// being queued the same status twice.
	if submitter != told {
		if err := c.forward(submitter, orig, r); err != nil {
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
func (c *ClearingHouse) clear(ctx context.Context, id payment.PaymentID, r payment.TransactionStatusReport) (payment.Payment, payment.TransactionStatusReport, error) {
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

// forward queues the status for one bank. Which bank, and how many of them, is
// tell's decision.
//
// It builds a NEW pacs.002 rather than relaying the one that arrived, and the
// difference is in one element: the originator. A status report says who
// DECIDED, and what this message reports is the clearing house's decision — this
// payment is in a cycle, or this payment is rejected and out of one. The
// answering bank's own decision travelled in its own pacs.002, carrying its own
// originator. Each hop states what that hop decided, which is why relaying the
// bytes would be wrong here and right for an instruction.
//
// The ORIGINAL message it refers back to is unchanged: that is the submitting
// bank's pacs.008 or pacs.003, which is what every hop is about and the only
// thing that bank can match on.
func (c *ClearingHouse) forward(to iso20022.BIC, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	return c.forwardDecision(to, "", orig, []payment.TransactionStatusReport{r})
}

// forwardDecision is forward for a status this institution is CARRYING rather
// than making: it names the institution that decided, so the pacs.002's Orgtr
// does not say the clearing house.
//
// There is exactly one such hop — the settlement agent's answer about a return,
// passed back to the bank that asked for one — and it is worth a second entry
// point rather than a bool. receiveReturnStatus's own doc says this institution
// decides nothing there, and iso20022.StatusReasonInformation says Orgtr exists
// so that a receiver does not blame the relay for the refusal.
func (c *ClearingHouse) forwardDecision(to, decidedBy iso20022.BIC, orig payment.OriginalMessage, rs []payment.TransactionStatusReport) error {
	mc := c.d.messageContext(c.bic, to)
	mc.DecidedBy = decidedBy
	env, err := payment.StatusMessage(orig, rs, mc)
	if err != nil {
		return fmt.Errorf("server: %s could not build its pacs.002 for %s: %w", c.bic, to, err)
	}
	return c.enqueue(to, env)
}

// Reject is the clearing house declining a payment because an operator said so.
//
// It is receiveStatus's rejection arm without the file that would have provoked
// it: the same RejectAtCSM, the same tell, the same banks told. Written as its
// own three statements rather than by faking a pacs.002 and feeding it to the
// handler, because a file this institution never received is not a thing to
// invent — and because what a caller from outside a business day needs back is
// the payment, which a handler has no way to return.
//
// It runs synchronously on the CALLER's goroutine, and queues after the unit of
// work has committed, for the reason every door in deployment.go sets out. A
// pacs.002 queued from inside the rejection would be one the payer's bank could
// act on against a rejection the store then rolled back — and the act in
// question is handing money back to a customer.
//
// tell is what fans it out, so the answer goes exactly where a counterparty's
// rejection would have gone: to the bank that submitted the payment, and — on a
// pull whose debtor leg has already posted — to the payer's bank as well.
//
// # The message it refers back to
//
// A pacs.002 says which message it is about, and there ISN'T one — no bank
// uploaded anything that provoked this. So the original is named as unavailable,
// by the NOTPROVIDED convention: inventing a message id the payer's bank never
// sent would be worse than saying there was none. Nothing downstream needs it,
// because a bank matches a status to a payment by the transaction reference.
func (c *ClearingHouse) Reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

	// Read BEFORE rejecting, because what tell needs is the status this payment
	// was at when the operator refused it, and RejectAtCSM overwrites exactly
	// that. Accepted means the bank that RECEIVED the instruction has already
	// answered it, so that bank holds a copy of this payment — and on a pull a
	// posted debtor leg with it, which is the money tell has to make sure reaches
	// somebody. Initiated means it has not.
	//
	// Two reads of one row in two units of work, and a status that changed between
	// them would be another institution rejecting or accepting this payment
	// concurrently — in which case RejectAtCSM below fails the transition and
	// there is nothing to tell anybody.
	before, err := c.ops.GetPayment(ctx, id)
	if err != nil {
		return payment.Payment{}, err
	}
	answeringBankAccepted := before.Status == payment.Accepted

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
	c.d.journal.outcome(TransactionOutcome{DecidedBy: c.bic, Payment: p.ID, Status: r.Status, Code: code, Text: text})
	// The rejection is a fact and the queueing may still fail, so the payment
	// comes back BESIDE the error rather than being swallowed — closeCycle's
	// shape, and the same half-happened outcome: a payment that is Rejected and
	// whose payer has not been given their money back.
	if err := c.tell(ctx, p, payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided}, r, answeringBankAccepted); err != nil {
		return p, fmt.Errorf("server: %s rejected %s and could not say so: %w", c.bic, p.ID, err)
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// The cut-off, and the settlement it instructs
// ---------------------------------------------------------------------------

// CloseCycle is the clearing house reaching a cut-off: it nets the batch, and
// then asks the central bank to discharge it.
//
// Two steps, and the seam between them is the point. Netting is the clearing
// house's own act and moves nothing — CloseCycleTx posts NOTHING at all, it
// transitions each payment to Cleared and writes the positions onto the cycle.
// Discharging those positions moves central-bank reserves, which no clearing
// house may do, so the second step is a FILE uploaded to another institution and
// not a call.
//
// It runs synchronously on the CALLER's goroutine, for Deployment.Submit's
// reason: an operator reaching a cut-off has an error to be told about, then and
// there. And like Submit, the upload is OUTSIDE the unit of work.
//
// What it does NOT answer is whether the cycle settled. It returns the cycle as
// the clearing house left it — Closed, with net positions — and the settlement
// agent works through its own queue when the next business day runs.
//
// The two failure modes are not the same, and the signature keeps them apart. A
// refused cut-off netted nothing and is the caller's answer. A cycle that closed
// and whose instruction could not be uploaded is HALF-HAPPENED — the payments
// are Cleared and no settlement agent has been told — so the closed cycle comes
// back beside the error rather than being swallowed. The console is where it
// shows: a cycle that is Closed and has no settlement.
//
// The day engine reaches the same cut-off on every settlement day (see
// AdvanceDay); this route is the operator asking for one out of turn.
func (c *ClearingHouse) CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

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
//
// It is the way out of the only state in this system that had none. A cut-off
// whose net payer is short of reserves comes back RJCT/AM04, and
// receiveSettlementStatus tells nobody — correctly, because nothing about any
// payment changed. What is left is a cycle that is Closed with no settlement, a
// batch of payments that are Cleared, every payer debited into their own bank's
// clearing suspense and every payee unpaid. Every other route is shut by a guard
// that is right: CloseCycleTx wants an open cycle, RejectAtCSMTx takes only an
// Initiated or Accepted payment, PostReturnLegTx wants a settled one.
//
// # It re-uploads the instruction rather than settling
//
// The clearing house does not discharge positions and this does not start to. It
// rebuilds the same pacs.009 from the same stored net positions and puts it on
// the connection, and the settlement agent decides again with the reserves as
// they are on the day it collects. That is why the caller gets the CYCLE back
// rather than a settlement, and why the answer is 202 at api.
//
// # Double-settling is not reachable, and it is guarded in two places
//
// This refuses anything that is not Closed, so a cycle that has already settled
// is ErrCycleNotClosed here and no second instruction is built. Two calls that
// raced past that check would each upload one, and the SECOND is refused at the
// settlement agent by SettleCycleTx's own CycleClosed guard. Behind both of
// those the central bank's posting carries the idempotency key "<cycle>:settle",
// so even a third arrangement that got past the state machine could not post the
// reserves twice.
func (c *ClearingHouse) Settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	// Everything below is the clearing house's work, and is recorded as the
	// clearing house's. See withActor.
	ctx = withActor(ctx, c.bic)

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

// closeOpenCycles is the clearing house reaching its cut-off on a business day:
// every cycle standing open is netted, and its positions are uploaded.
//
// EVERY open cycle, and not the one that has payments in it. A cycle belongs to
// one scheme and a deployment runs one per scheme, so a day on which only credit
// transfers were submitted still cuts off the direct-debit cycle — which nets to
// nothing and instructs nothing, and is the path instructSettlement's "a cycle
// that nets to nothing" section describes.
//
// A cycle that will not close is a line in the day's report and stops no other
// cycle closing. The failure is per scheme, and one scheme's cut-off is no
// reason for another scheme's members to go unpaid.
func (c *ClearingHouse) closeOpenCycles(ctx context.Context) []Problem {
	ctx = withActor(ctx, c.bic)

	cycles, err := c.ops.ListCycles(ctx)
	if err != nil {
		return []Problem{{Institution: c.bic, Detail: fmt.Sprintf("reading the cycles to cut off: %v", err)}}
	}
	var problems []Problem
	for _, cycle := range cycles {
		if cycle.Status != payment.CycleOpen {
			continue
		}
		if _, err := c.CloseCycle(ctx, cycle.ID); err != nil {
			problems = append(problems, Problem{Institution: c.bic, Detail: err.Error()})
		}
	}
	return problems
}

// openCycles gives every registered scheme a cycle for the next clearing day.
//
// A scheme with no open cycle accepts nothing: AcceptAtCSM refuses with
// ErrCycleNotOpen, which the clearing house answers TM01. So this is what makes
// the next day work at all, and it runs on every date rather than only on
// settlement days — a cycle standing open over a weekend accumulates nothing,
// because nothing is routed on one.
//
// A scheme that already has one open is left alone rather than refused. That is
// the ordinary case on a non-settlement day and after an operator opened one by
// hand, and neither is news.
func (c *ClearingHouse) openCycles(ctx context.Context) []Problem {
	ctx = withActor(ctx, c.bic)

	var problems []Problem
	for _, scheme := range c.ops.ListSchemes() {
		switch _, err := c.ops.OpenCycle(ctx, scheme.ID()); {
		case err == nil, errors.Is(err, payment.ErrCycleAlreadyOpen):
		default:
			problems = append(problems, Problem{
				Institution: c.bic,
				Detail:      fmt.Sprintf("opening a cycle for %s: %v", scheme.ID(), err),
			})
		}
	}
	return problems
}

// instructSettlement uploads the closed cycle's net positions to the central
// bank as a pacs.009.
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
// are therefore two cut-offs and two instructions. Nothing here would stop a
// FUTURE caller putting two cycles in one file — payment.ReadSettlement supports
// it deliberately — but this system does not, and the central bank refuses a
// file that does; see cycleOf.
//
// # A cycle that nets to nothing instructs nothing
//
// An empty cycle, or one whose members' positions all cancel, has no leg to
// send, and a pacs.009 with no transaction is not a message this codec will
// build. Silence is correct there and is not the same as a failure: there is
// nothing for a settlement agent to discharge. Every credit-transfer test in
// this package closes an untouched direct-debit cycle for exactly that reason,
// so the path is walked constantly rather than reasoned about.
func (c *ClearingHouse) instructSettlement(ctx context.Context, closed payment.ClearingCycle) error {
	scheme, ok := c.ops.Scheme(closed.Scheme)
	if !ok {
		return fmt.Errorf("server: %s closed %s and holds no %q scheme to settle it in: %w",
			c.bic, closed.ID, closed.Scheme, payment.ErrSchemeNotFound)
	}
	legs := c.settlementLegs(closed, scheme.Asset())
	if len(legs) == 0 {
		return nil
	}
	env, err := payment.SettlementMessage(legs, c.d.messageContext(c.bic, c.d.cfg.CentralBankBIC))
	if err != nil {
		return fmt.Errorf("server: %s could not build the settlement instruction for %s: %w", c.bic, closed.ID, err)
	}
	return c.upload(ctx, env)
}

// settlementLegs turns a cycle's net positions into the legs of an instruction:
// one per member with something to discharge, each against the CENTRAL BANK.
//
// Against the central bank and not against another member, because a
// multilateral net position has no counterparty among the banks. Three banks
// that each paid and were paid net out to one figure apiece, and the sum of
// those figures is zero — but there is no pairing of payer to payee left in
// them, which is precisely what netting destroys. Every position is therefore a
// claim on or an obligation to the settlement agent, and the message says so.
//
// The rendering itself is payment.SettlementLegsOf. It lives there because the
// settlement agent works from the instruction rather than from the cycle: the
// seed plays every institution and uploads nothing, so it has to produce exactly
// what this institution would have put on the connection, and two renderings of
// one intent are two things that can drift.
func (c *ClearingHouse) settlementLegs(closed payment.ClearingCycle, asset ledger.AssetCode) []payment.SettlementLeg {
	return payment.SettlementLegsOf(closed, asset, c.d.cfg.CentralBankBIC)
}

// receiveSettlementStatus is the clearing house acting on what the CENTRAL BANK
// said about a settlement instruction.
//
// The two outcomes are not symmetrical, and the asymmetry is a fact about who is
// waiting for what.
//
// ACSC is news every bank in the batch has been waiting for since it submitted,
// and it is fanned out per payment — see tellSettled. Settlement is the point of
// finality: it is the moment a payee's bank has actually been paid.
//
// RJCT is told to NOBODY, and that is deliberate rather than an omission. The
// batch failed whole and nothing was posted in any book: the central bank checks
// each net payer's reserve before it posts anything of its own, and it advises
// no member unless it settles. That leaves every payment in the cycle exactly
// where the cut-off left it: Cleared, with the payer's money still in its own
// bank's clearing suspense. A bank told "rejected" would try to reverse a debtor
// leg that must not be reversed, and Bank.receiveStatus refuses to. There is
// nothing truthful to tell a bank here, because nothing about its payments
// changed.
//
// What DID change is the cycle, and the cycle is where the failure is visible:
// it stays Closed with no settlement against it. The code is logged here as
// well, because the code is the one thing that arrives on the wire and is
// nowhere in the store.
//
// What the operator does about it is fund the short member and ask the clearing
// house to instruct settlement again: POST /cycles/{cid}/settle. That route is
// the whole of the remedy, and this handler is deliberately not part of it — a
// clearing house that retried by itself would re-present a batch against
// reserves nobody had changed.
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
			c.d.log.Error("server: settlement refused",
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

// tellSettled turns one settled CYCLE into per-PAYMENT advices, addressed to
// BOTH of the payment's banks.
//
// # Three reasons to be told, and every payment has two banks with one each
//
// The SUBMITTER is waiting for the answer to its instruction, and ACSC is what
// closes it. That recipient is chosen by the scheme's direction, exactly as tell
// does.
//
// The CREDITOR's bank has a LEG TO POST: settlement moved the reserves, and the
// payee is paid when its own bank releases the money out of its clearing
// suspense.
//
// The bank that is NEITHER — the payer's bank on a pull — has a ROW TO CLOSE. It
// holds its own copy and cannot read anybody's, so left out it would keep a copy
// saying Initiated for ever about a payment whose reserves have moved, and would
// then refuse to return it, because a return is an edge from Settled.
//
// So the recipient list is the two agents plus the submitter, deduplicated: two
// files either way. Deduplicating rather than queueing twice, because a bank
// that collected the same advice twice would post nothing the second time — the
// ledger's idempotency key and SettleAtBankTx's Settled guard both see to that —
// but a system that says everything twice teaches nothing about who needs to
// hear what.
//
// # The central bank could not send these
//
// It is answering about a CYCLE, and settlementOps holds nothing that turns one
// into the payments inside it. It can act on a payment somebody else named to
// it, which is what a return is, and that is a different thing from being able
// to enumerate a batch.
//
// The ORIGINAL message these refer back to is the SETTLEMENT INSTRUCTION rather
// than the bank's own pacs.008, which is a limitation worth naming. A bank
// matches on OrgnlTxId, which is the payment id and is right; OrgnlMsgId names a
// message that bank never sent. It is the honest one available — the clearing
// house does not keep each submitting bank's message id — and a real network
// would keep it.
func (c *ClearingHouse) tellSettled(ctx context.Context, id payment.CycleID, orig payment.OriginalMessage, r payment.TransactionStatusReport) error {
	// This institution's OWN copies move first, in one unit of work, and what
	// comes back is what they say. See payment.SettleAtCSMTx.
	//
	// A cycle whose copies could not all be marked tells nobody anything.
	settled, err := c.ops.SettleAtCSM(ctx, id)
	if err != nil {
		return fmt.Errorf("server: %s was told %s settled and cannot record it: %w", c.bic, id, err)
	}
	for _, p := range settled {
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			return fmt.Errorf("server: %s was told %s settled and holds no %q scheme to say who submitted %s: %w",
				c.bic, id, p.Scheme, p.ID, payment.ErrSchemeNotFound)
		}
		c.d.journal.outcome(TransactionOutcome{DecidedBy: c.bic, Payment: p.ID, Status: r.Status})
		// The submitter first, so a push's two files go into the banks' queues in
		// the order they have reason to expect them: the answer to the instruction,
		// then the advice to act on. The two agents follow, and whichever of them is
		// the submitter drops out.
		recipients := []iso20022.BIC{submitterOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)}
		for _, agent := range []iso20022.BIC{p.CreditorDetails.Agent, p.DebtorDetails.Agent} {
			if agent != recipients[0] {
				recipients = append(recipients, agent)
			}
		}
		for _, recipient := range recipients {
			if err := c.forward(recipient, orig, payment.TransactionStatusReport{
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

// receiveReturnStatus is the clearing house acting on the settlement agent's
// answer about a RETURN: it tells the bank that asked, and on a yes it releases
// the message to the bank that did not.
//
// It is the mirror of tellSettled. A return answer already names the one payment
// it is about, so this institution enumerates nothing — but it does have to
// reach two banks with two different things: one is waiting for an answer and
// the other has a LEG TO POST.
//
// Addressing the answer is the part that needs the store. The clearing house
// keeps no record of who uploaded which file — it relays and forgets, which is
// what lets it route a file about a payment it does not hold — so "who asked for
// this return" is recomputed from the payment the answer names, by the rule that
// chose the bank in the first place (returnerOf). Releasing the pacs.004 needs
// no lookup: the message carries its own parties.
//
// # The two outcomes are not symmetrical
//
// Both are forwarded — a refused RETURN is the answer to a question one bank
// asked and is owed — but what they carry differs:
//
//   - ACSC: the answer goes to the bank that asked, and the held pacs.004 into
//     the other bank's queue, where it posts the leg the returner does not hold.
//     The ANSWER goes first, so the bank that asked hears the outcome before the
//     other bank starts moving money on it.
//   - RJCT: only the answer goes, and the held message is DROPPED. That is the
//     whole point of holding it.
//
// The delete runs BEFORE the release, so a release that fails takes the message
// with it and no later answer can recover it. That is deliberate — an entry kept
// on failure would be retried by nothing and swept by nothing — and its cost is
// the one relayReturn records for a restart.
//
// An entry is otherwise dropped only when an answer REACHES the delete, and two
// things can stop it: a return the settlement agent could not process is never
// answered at all, and an answer this handler cannot act on returns above the
// delete. Both leak one entry apiece, both are bounded by the number of returns
// a process carries, and both are recorded rather than swept.
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
		returner := returnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
		// The SETTLEMENT AGENT decided this, not the clearing house, and the message
		// says so. It is the one hop in this system where the sender and the
		// originator are different institutions; see forwardDecision.
		errs := []error{c.forwardDecision(returner, from, orig, []payment.TransactionStatusReport{r})}

		held, holding := c.held[id]
		delete(c.held, id)
		if holding && r.Status == iso20022.TransactionStatusSettlementCompleted {
			errs = append(errs, c.releaseReturn(held, id))
		}
		if r.Status == iso20022.TransactionStatusSettlementCompleted {
			// This institution's own copy, which nothing else writes: the return's two
			// customer legs land in the two BANKS' databases. See
			// payment.CompleteReturnTx.
			//
			// After the release rather than before it, so the file that makes the
			// other bank post is not held up by this institution's bookkeeping; and
			// its error is joined rather than returned, for the reason the whole block
			// joins.
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

// endToEndOf is the payer's own reference for a payment, or the EPC's convention
// where there is none.
//
// It repeats payment's unexported helper of the same name rather than reaching
// for it, because these two references must agree: a bank matching the answer to
// its instruction compares what it sent with what came back, and a status
// quoting an empty string against a pacs.008 carrying NOTPROVIDED would not
// match. See notProvided for the whole of that convention.
func endToEndOf(p payment.Payment) string {
	if p.EndToEndID == "" {
		return notProvided
	}
	return p.EndToEndID
}

// answer rejects the part of a file the clearing house could not carry, back to
// whoever uploaded it: one status report per transaction, in one message.
//
// One message and not one per transaction, because the sender uploaded a file
// and a bulk network answers a file with a file. What varies inside it is the
// transaction reference; the code and the text are the group's, because the only
// refusal that reaches here is about a DESTINATION — an agent this deployment
// holds no queue for — and every transaction addressed to it shares one. No
// payment changes state: this is decided before any of them is looked up.
func (c *ClearingHouse) answer(to iso20022.BIC, orig payment.OriginalMessage,
	refs []iso20022.PaymentIdentification, code iso20022.StatusReason, text string) error {

	rs := make([]payment.TransactionStatusReport, 0, len(refs))
	for _, ref := range refs {
		rs = append(rs, payment.TransactionStatusReport{
			EndToEndID: ref.EndToEndId,
			TxID:       ref.TxId,
			Status:     iso20022.TransactionStatusRejected,
			Code:       code,
			Text:       text,
		})
	}
	return c.forwardDecision(to, "", orig, rs)
}
