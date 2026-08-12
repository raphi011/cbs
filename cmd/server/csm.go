package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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
	// It is one of the two things an institution in this package keeps between
	// files — Bank.hub is the other — and it is deliberate rather than
	// convenient: see relayReturn for why the message cannot be queued onward
	// before finality, and for what is lost if this map is.
	//
	// No lock. Only relayReturn and receiveReturnStatus touch it, and both are
	// reached only from a business day, which runs on one goroutine under the
	// deployment's own lock. The three methods that run on a REQUEST's goroutine
	// (closeCycle, settle, reject) must never read it, and none does.
	held map[payment.PaymentID]heldReturn

	// output is the receiving banks' shares of every file taken in and not yet
	// settled, keyed by the cycle each share's transactions are clearing in.
	//
	// It is the settle-before-release ruling made into a data structure: a share
	// exists from the moment the file is taken and reaches nobody until the
	// settlement agent has discharged the cut-off that carries it. See
	// takeRecorded and releaseFiles.
	//
	// The durability caveat is held's, and worse in proportion to the batch: what
	// a restart costs is a whole cycle's instructions, which the receiving banks
	// then never get and never answer, against reserves that may already have
	// moved. A real CSM stores its output files.
	//
	// No lock, for held's reason: only takeRecorded and releaseFiles touch it and
	// both are reached only from a business day.
	output map[payment.CycleID][]pendingFile
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
// This is the whole of routing. There is no URL to look up and nothing that can
// fall out of step with the roster: a party with no enrolment has no queue, and a
// file addressed to it has nowhere to go. That is where RC01 comes from — one
// refusal, rather than a reachability answer and a membership answer that could
// disagree.
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

// ---------------------------------------------------------------------------
// Working through what has arrived
// ---------------------------------------------------------------------------

// work runs this institution through everything its members have uploaded and
// not yet been answered about, oldest first.
//
// ONE pass, over every order type, and that is a consequence of settling before
// releasing: a file taken in here reaches no bank until the cycle carrying it
// has settled, so there is no answer to a file this same pass could have routed
// a moment earlier. What used to need two phases a day apart now needs none —
// the ordering that mattered is between this phase and the cut-off, not inside
// it.
//
// Every order is answered on its acknowledgement, which is the seam this
// transport exists for: the uploader was told EBICS_OK and went away, so
// "processed" and "rejected" are what a subscriber's HAC download tells it
// later, and neither says anything about the payments inside the file.
func (c *ClearingHouse) work(ctx context.Context) []Problem {
	ctx = withActor(ctx, c.bic)

	var problems []Problem
	for _, order := range c.host.Pending() {
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
		return c.takeCreditTransfer(ctx, from, env, doc)
	case *iso20022.Pacs003:
		return c.takeDirectDebit(ctx, from, env, doc)
	case *iso20022.Pacs004:
		return c.relayReturn(ctx, from, env, doc)
	case *iso20022.Pacs002:
		// Three kinds of status arrive here, and it takes two questions to tell
		// them apart.
		//
		// The SENDER separates a member bank's from the settlement agent's. A
		// bank's pacs.002 says only that a file it was handed would not parse; the
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
			return c.receiveUnreadable(from, doc)
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

// takeCreditTransfer takes a credit transfer into the network: this
// institution's own copy of every payment in it, each transaction into the open
// cycle for its scheme, and one output file per CREDITOR's agent — the bank
// that holds the payee, because a push travels towards the money's destination.
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
// Everything after the sort goes through takeRecorded, which is where the order
// of the record and the clearing is argued and where the output files wait.
func (c *ClearingHouse) takeCreditTransfer(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs008) error {
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
	return c.takeRecorded(ctx, from, env, orig, ps, groups, func(idx []int) iso20022.Document {
		return creditTransferPart(doc, idx)
	})
}

// takeDirectDebit sorts a collection by the DEBTOR's agent: the bank that holds
// the payer, because a pull travels towards the money's source.
//
// One element different from takeCreditTransfer, and it is the whole direction
// of the payment. Routing a pacs.003 by CdtrAgt would send the collection back
// to the bank that uploaded it, which would answer its own instruction — and the
// resolution inside DirectDebitRequest would succeed while it did, because both
// parties resolve by address whoever is asking.
func (c *ClearingHouse) takeDirectDebit(ctx context.Context, from iso20022.BIC, env iso20022.Envelope, doc *iso20022.Pacs003) error {
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
	return c.takeRecorded(ctx, from, env, orig, ps, groups, func(idx []int) iso20022.Document {
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

// A pendingFile is one receiving bank's share of an uploaded instruction file,
// built at ingestion and waiting for the cycle carrying it to settle.
//
// It holds POSITIONS in the submitting bank's document rather than a document of
// its own, because which of them travel is not settled until the cut-off is: a
// payment an operator rejects out of an open cycle must not reach the bank that
// would have been paid. releaseFiles is where the survivors are cut.
//
// def is the message definition the share travels under, which is the uploaded
// file's own. The header is otherwise this institution's and is built at release,
// so that its creation time is when the file was really handed over.
type pendingFile struct {
	to   iso20022.BIC
	def  string
	ids  []payment.PaymentID
	idx  []int
	part func(idx []int) iso20022.Document
}

// takeRecorded is the second half of taking a file in: every transaction into
// the cycle it will settle in, the submitting bank told what became of each, and
// one output file per receiving bank built and HELD.
//
// # The record comes BEFORE anything else, and the order is decided rather than
// incidental
//
// Recording comes FIRST, because the row is also this institution's DUPLICATE
// CHECK. Recording last would mean a replayed instruction is cleared before it
// is refused, and what it would be answered with is a status naming a payment
// this clearing house is holding live in a cycle. A real CSM checks for a
// duplicate at ingestion, before it validates or forwards anything, and this is
// that check.
//
// It is recorded for the WHOLE file in one unit of work and decided per
// transaction, which is the asymmetry worth naming: the file is one instruction
// as far as the submitting bank is concerned, and M as far as the network is.
//
// # Nothing is queued for a receiving bank here, and that is the whole ruling
//
// The cycle settles before the output files are released. A receiving bank is
// handed its instructions only once the funds behind them are final, so it never
// credits a customer against money that has not settled — reverse the two and
// settlement risk is invented. That is why this builds the shares and stops:
// tellSettled is what puts them in the queues.
//
// So the only refusals reachable at this point are the ones this institution can
// make on its own — a destination it holds no queue for (RC01), a bank the
// roster does not admit, a scheme with no open cut-off window (TM01) — and every
// one of them is a REJECT, in a pacs.002, before any money has moved. A problem
// the receiving bank finds later cannot be one of these; it is a return.
//
// # One answer per file, carrying one report per transaction
//
// The submitting bank uploaded a file and a bulk network answers a file with a
// file. What varies inside it is the transaction: some accepted, some refused,
// which is where GrpSts PART comes from and the one place in this system a mixed
// answer is now built.
func (c *ClearingHouse) takeRecorded(ctx context.Context, from iso20022.BIC, env iso20022.Envelope,
	orig payment.OriginalMessage, ps []payment.Payment, groups []destination,
	part func(idx []int) iso20022.Document) error {

	var errs []error
	reports := make([]payment.TransactionStatusReport, 0, len(ps))

	for _, g := range groups {
		// A bank this deployment holds no queue for. RC01 —
		// BankIdentifierIncorrect — and it is this institution that says it,
		// because enrolment is what creates a queue and the roster is its own
		// table. A bank cannot know that a BIC is unroutable; it can only know
		// that its file was not answered.
		//
		// The whole group shares one answer: they were all addressed to the same
		// unreachable bank.
		if !c.host.Enrolled(ebics.SubscriberID(g.to)) {
			for _, i := range g.idx {
				reports = append(reports, c.refuse(ctx, ps[i], iso20022.StatusReasonBankIdentifierIncorrect,
					fmt.Sprintf("no route to %s", g.to), &errs))
			}
			continue
		}
		var cycle payment.CycleID
		var taken []int
		var ids []payment.PaymentID
		for _, i := range g.idx {
			accepted, err := c.ops.AcceptAtCSM(ctx, ps[i].ID)
			if err != nil {
				// The clearing house could not clear it, so the clearing house
				// rejects it, with the code its own refusal maps to — TM01 for no
				// open cycle, MS03 for a bank the roster does not admit. A separate
				// unit of work, because the first one rolled back.
				reports = append(reports, c.refuse(ctx, ps[i], payment.ReasonFor(err), err.Error(), &errs))
				continue
			}
			cycle = accepted.CycleID
			taken = append(taken, i)
			ids = append(ids, accepted.ID)
			c.d.journal.outcome(TransactionOutcome{
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
		c.output[cycle] = append(c.output[cycle], pendingFile{
			to: g.to, def: env.AppHdr.MsgDefIdr, ids: ids, idx: taken, part: part,
		})
	}

	if len(reports) > 0 {
		errs = append(errs, c.forwardDecision(from, "", orig, reports))
	}
	return errors.Join(errs...)
}

// refuse is this institution rejecting one transaction of an uploaded file and
// saying so in the report the submitting bank will be handed.
//
// The rejection is a WRITE and the report is a value, and they are one call
// because the two must agree: a clearing house whose own copy said Initiated
// while its answer said RJCT would be holding a payment it had told a bank it
// would not carry — and that bank, acting on the message, will have reversed its
// customer's debit.
//
// A rejection that could not be recorded is appended to errs and STILL reported,
// which is the lesser of two wrongs: the file's other transactions have answers
// owed to them, and a day that dropped the whole report to keep one row honest
// would leave every payer in the batch waiting.
func (c *ClearingHouse) refuse(ctx context.Context, p payment.Payment,
	code iso20022.StatusReason, text string, errs *[]error) payment.TransactionStatusReport {

	if _, err := c.ops.RejectAtCSM(ctx, p.ID, code, text); err != nil {
		*errs = append(*errs, fmt.Errorf("server: %s answered %s for %s and could not record it: %w",
			c.bic, code, p.ID, err))
	}
	c.d.journal.outcome(TransactionOutcome{
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
//
// This is the moment the design is named for. The instructions reach the banks
// that have to act on them only now, and what makes them safe to act on is that
// the reserves behind them have already moved: a receiving bank cannot be asked
// to credit a customer against a cut-off that might still fail.
//
// # The share is CUT again here
//
// Only the transactions that actually settled travel. A payment an operator
// rejected out of the open cycle after this file was built is not in the batch
// the settlement agent discharged, and a bank handed it would credit a payee
// against money nobody moved. So the positions are filtered against what
// SettleAtCSM said, and a share with nothing left in it is not sent at all.
//
// The DOCUMENT is the submitting bank's own, carrying the transactions this
// destination is to be handed and nothing else. What the split rewrites is
// NbOfTxs, because that element is a claim about the file it sits in and a file
// sorted by creditor agent is a different file. The transactions themselves
// travel unchanged, and so does GrpHdr/MsgId — which is what lets a later answer
// be matched to the original all the way home. See creditTransferPart.
//
// The header is replaced outright: it says who is handing this to whom, and that
// is this hop's rather than the submitting bank's.
//
// A file that could not be queued is joined rather than returned, for the reason
// a day carries on past one failure: a destination this institution could not
// reach must not stop the others being handed their share.
func (c *ClearingHouse) releaseFiles(cycle payment.CycleID, settled []payment.Payment) error {
	files := c.output[cycle]
	delete(c.output, cycle)

	final := make(map[payment.PaymentID]bool, len(settled))
	for _, p := range settled {
		final[p.ID] = true
	}
	// Which of them a file was built for, whatever became of the queueing. A
	// share this institution could not enqueue is reported as that, once, by the
	// error below; counting it here as well would report one lost file twice and
	// send its reader looking for a second fault.
	inAFile := make(map[payment.PaymentID]bool, len(settled))

	var errs []error
	for _, f := range files {
		idx := make([]int, 0, len(f.idx))
		for i, id := range f.ids {
			if final[id] {
				idx = append(idx, f.idx[i])
				inAFile[id] = true
			}
		}
		if len(idx) == 0 {
			continue
		}
		errs = append(errs, c.enqueue(f.to, iso20022.Envelope{
			AppHdr: iso20022.AppHdr{
				Fr:        iso20022.NewAgent(c.bic),
				To:        iso20022.NewAgent(f.to),
				BizMsgIdr: c.d.nextMsgID(c.bic),
				MsgDefIdr: f.def,
				CreDt:     iso20022.ISODateTime{Time: c.d.now()},
			},
			Document: f.part(idx),
		}))
	}
	return errors.Join(append(errs, c.unhanded(cycle, settled, inAFile)...)...)
}

// unhanded is every payment this cut-off settled that no receiving bank was
// handed a file for.
//
// # Why the case exists at all
//
// A share is built when a FILE is worked (takeRecorded) and released when the
// cycle carrying it settles. Anything that puts a payment in a cycle without
// working a file therefore settles it with no share behind it — the seed, which
// plays every institution and uploads nothing, and a restart, which empties the
// map the shares are held in. Both end the same way: reserves have moved, and
// the bank that has to act on the instruction is never told it exists.
//
// # Why it has to be said out loud
//
// Because it is otherwise indistinguishable from a cut-off with nothing to
// release, which is an ordinary day — a cycle that took nothing in has no share
// either, and settleUninstructed drives exactly that case. Silence there is
// correct and silence here is a payee who is never paid, so the difference has
// to be drawn from the payments rather than from the shares.
//
// Nothing is retried and nothing is unwound. The money is final at the
// settlement agent, this institution cannot rebuild a file it never held, and a
// day that stopped here would leave the rest of the batch unreported. What is
// owed to the reader is the exact loss, per payment, in the day's report;
// payment/recon is what finds it later in the books.
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
			c.bic, p.ID, cycle, receiverOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)))
	}
	return errs
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

// receiveUnreadable is a member bank saying it could not parse a file this
// institution handed it.
//
// It is the only pacs.002 a member uploads here. A receiving bank makes no
// judgement about an instruction any more — the instruction reaches it only
// after the cycle carrying it is final — so there is nothing here to act on and
// nothing to relay onward.
//
// What it costs is exact and is why it is recorded rather than shrugged at: the
// payments in that file have settled, the money is in the receiving bank's
// clearing suspense, and a bank that cannot read the file will not apply them
// and will not return them either. That is the class payment/recon exists to
// surface, and this is the line in the day's report that names the file.
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
//
// One recipient, and that is a consequence of settling before release: the only
// decisions this institution makes before finality are its own, and before
// finality the submitting bank is the only bank that has ever heard of the
// payment. The receiving bank learns of it when its output file is released,
// which happens after settlement or not at all.
//
// The submitter is looked up from the PAYMENT rather than taken from the file it
// arrived in, because those coincide on the ordinary path and not on the
// operator's: Reject has no file at all. Which bank submitted is the scheme's
// direction and nothing else. See submitterOf.
func (c *ClearingHouse) tell(p payment.Payment, orig payment.OriginalMessage,
	r payment.TransactionStatusReport) error {

	scheme, ok := c.ops.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("server: %s decided %s and holds no %q scheme to say who submitted it: %w",
			c.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
	}
	return c.forward(submitterOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent), orig, r)
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
// It is takeRecorded's refusal arm without the file that would have provoked it:
// the same RejectAtCSM, the same tell, the same bank told. Written as its own
// three statements rather than by faking a pacs.002 and feeding it to the
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
// # What it can and cannot catch
//
// Only a payment that has not yet settled, because RejectAtCSMTx takes an
// Initiated or Accepted one and no other. The output file carrying it is still
// held here, so the bank that would receive it has heard nothing and needs to
// hear nothing; releaseFiles cuts a rejected transaction out of the share when
// the rest of its cycle settles. After finality there is no rejection left to
// make and the remedy is a return.
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
	if err := c.tell(p, payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided}, r); err != nil {
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
//
// Silence is not the END of such a cycle, though, and that is settleUninstructed's
// job: no answer will ever come back for a file that was never sent, and the
// payments inside one are owed the same finality as any other batch.
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

// settleUninstructed discharges the cut-offs no settlement agent will ever
// answer for.
//
// A closed cycle that nets to nothing was never instructed — there was no leg to
// send — so nothing will arrive to release it, and everything settle-before-
// release gates is gated on an answer. Without this the payments in such a cycle
// stay Cleared for ever: every payer's money sits in its own bank's clearing
// suspense, and every receiving bank's share of the file waits in c.output for a
// pacs.002 that no institution owes anyone.
//
// # It is the clearing house settling, and that is not a crossing
//
// No reserve moves and none needs to: every member's position is zero, so the
// suspense each of them filled at submission is emptied by the payments it
// receives in the same batch. What this institution writes is its OWN copies and
// nothing else — SettleAtCSM touches no book at all — and the settlement agent
// is not silent about this cut-off so much as never asked about it. There is no
// settlement row anywhere, which is what payment.NetsToNothing tells the
// reconciliation harness.
//
// # It sweeps rather than following the close
//
// Every closed cycle, not the ones this day's cut-off closed, because an
// operator can close one out of turn through the API and the same cycle would
// then wait for an answer that is not coming. A cycle with a position to
// discharge is left alone here whatever became of its instruction: one the agent
// refused is the operator's to re-instruct — see Settle — and one this sweep
// settled instead would release its files against reserves nobody moved.
//
// The sweep terminates because SettleAtCSM marks the cycle Settled, so it is
// each such cut-off's last day of being looked at.
func (c *ClearingHouse) settleUninstructed(ctx context.Context) []Problem {
	ctx = withActor(ctx, c.bic)

	cycles, err := c.ops.ListCycles(ctx)
	if err != nil {
		return []Problem{{Institution: c.bic, Detail: fmt.Sprintf("reading the cycles nothing was instructed for: %v", err)}}
	}
	var problems []Problem
	for _, cycle := range cycles {
		if cycle.Status != payment.CycleClosed || !payment.NetsToNothing(cycle) {
			continue
		}
		// The original message is named as unavailable, by Reject's convention and
		// for its reason: there is no settlement instruction to refer back to, and
		// inventing a message id no institution sent would be worse than saying so.
		if err := c.tellSettled(ctx, cycle.ID,
			payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided},
			payment.TransactionStatusReport{
				TxID:   string(cycle.ID),
				Status: iso20022.TransactionStatusSettlementCompleted,
			}); err != nil {
			problems = append(problems, Problem{Institution: c.bic, Detail: err.Error()})
		}
	}
	return problems
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

// tellSettled is what finality releases: the output files the receiving banks
// have been waiting for, and an ACSC per payment for the banks that submitted
// them.
//
// # The files go FIRST, and that is the ruling this design is named for
//
// A receiving bank gets its instructions only once the funds behind them are
// final, so it never credits a customer against money that has not settled.
// Everything queued here is therefore queued after the settlement agent has
// answered, and nothing at all is queued when it refuses.
//
// # ONE recipient for the advice, and it is the submitter
//
// The submitter is waiting for the answer to its instruction and ACSC is what
// closes it; the recipient is chosen by the scheme's direction, exactly as tell
// does. The other bank needs no advice, because the output file it is being
// handed in the same breath IS its news: it writes its own copy and applies it,
// under a rule that never has to ask whether the money is there.
//
// # The central bank could not send any of this
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
	// The files first, and what could not be released does NOT stop the advices.
	//
	// They are two different banks' news about the same batch. A share this
	// institution could not hand over is a fault the day's report has to carry,
	// and a submitting bank still waiting for the answer to its instruction is a
	// second fault — one that would be caused here, by returning early, on every
	// payment in the cycle including the ones that were released cleanly.
	released := c.releaseFiles(id, settled)
	for _, p := range settled {
		scheme, ok := c.ops.Scheme(p.Scheme)
		if !ok {
			return errors.Join(released, fmt.Errorf("server: %s was told %s settled and holds no %q scheme to say who submitted %s: %w",
				c.bic, id, p.Scheme, p.ID, payment.ErrSchemeNotFound))
		}
		c.d.journal.outcome(TransactionOutcome{DecidedBy: c.bic, Payment: p.ID, Status: r.Status})
		if err := c.forward(submitterOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent), orig,
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
