package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// A Bank is one member bank: its own book, its own address, the two connections
// it dials out on, and everything it does with what it collects.
//
// It satisfies bank.Institution, which api/bank declares; see Deployment for why
// that is structural and what it buys.
//
// bic is the whole of its identity. A bank's ParticipantID IS its BIC (see
// payment.AsBank), so the conversion is total and lossless and this type keeps
// one value rather than two that could disagree.
//
// # The three roles one bank plays
//
// A credit transfer touches this type three times, and they are three different
// banks in the same code: the PAYER's bank submits (submit), which is the only
// half that moves money at initiation; the PAYEE's bank collects the pacs.008
// (receiveCreditTransfer) and answers yes or no; and the PAYER's bank collects
// the answer (receiveStatus), giving the money back if it was no.
//
// Which one is running is decided entirely by which bank's queue the file was
// in. That is what makes "who may know what, and when" a question with an answer
// here.
//
// A direct debit is the same three with the banks swapped and the money moving
// at a different moment. The PAYEE's bank submits, because a collection is the
// payee asking for what it is owed — and its submission moves NOTHING, because
// the account being collected from is at the other bank. The PAYER's bank
// collects the pacs.003, and that is the half that moves money and the only
// moment either the account or the funds behind it are in view, so AM04 comes
// from there and could come from nowhere else.
//
// So "the submitting bank posts" is a fact about a PUSH. The rule that covers
// both is that the DEBTOR's bank posts the debtor leg, and the direction decides
// whether that is the bank submitting or the bank answering.
//
// # A fourth and fifth role, played after the payment is final
//
// A RETURN is asked for by the bank that received the original instruction — the
// payee's bank on a push, the payer's on a pull — which is the one role here
// that is neither submitting nor answering. Its half MOVES MONEY: this bank
// posts its own customer leg BEFORE it composes the pacs.004, and that ordering
// is the whole of why a refusal binds. See returnPayment.
//
// The reserve reversal is central-bank money and no member bank may move it. A
// return's CUSTOMER legs are not reserves: each belongs to the bank whose
// customer it moves.
//
// The OTHER bank plays the fifth role: it collects the pacs.004 after finality
// (receiveReturn) and posts the leg the returning bank does not hold. It cannot
// refuse, because the reserves have already moved. So the two roles are one
// rule: a bank can refuse a leg only if it posts it before it uploads. A REFUSED
// return brings the fourth role back for one more act — the leg is unwound
// (receiveReturnStatus).
//
// # A sixth role, and the second that answers nothing
//
// At a cut-off — and on a return, which is a cut-off of one payment as far as
// the reserves are concerned — a member is TOLD what its reserve account did, in
// a camt.053, and books its own mirror leg from it (receiveStatement). It
// produces no pacs.002, because a statement is not an instruction. It is the
// only role in which this bank posts without any customer of its own being
// involved: what moves is its own position at the central bank.
type Bank struct {
	d *Deployment

	// net is this bank's whole view of the domain, and it has ONE caller:
	// Network, which api/bank's surface reads every request through. Everything
	// this file does goes through ops instead.
	net *payment.BankNetwork

	// ops is the same network NARROWED. A bank that called SettleCycle through
	// it does not compile; see ops.go for what that is worth and what it is not.
	ops bankOps

	bic iso20022.BIC

	// The two hosts this bank dials, and the whole of its address book. Nothing
	// is ever pushed at a member bank: a bank that never collects is a bank whose
	// customers are never told the fate of anything.
	csm *ebics.Client
	cb  *ebics.Client

	// hub is the instructions this bank has taken and not yet put in a file, in
	// the order its customers handed them in. It is the payment hub, and it is
	// what makes this network BULK: a submission does not travel, it waits, and
	// the cut-off is what turns a morning's worth of them into one file per
	// scheme.
	//
	// Payment IDS and not payments. A file is built out of the rows as they
	// stand at the cut-off, so what this holds is a list of what to look up
	// rather than a second copy of what the store already has.
	//
	// # In memory, and what a restart costs
	//
	// An instruction taken and not yet cut off is gone, so the payer's money sits
	// in this bank's clearing suspense against a payment no file will ever carry.
	// It stays Initiated for ever, no counterparty has seen it, and there is no
	// remedy from inside the flow — payment/recon is what makes it visible.
	//
	// It is the same defect the clearing house's held files and held returns
	// carry, and the answer to it is a table in the institution's own database.
	// This obligation is a member bank's, so it is a member bank's to fix: see
	// docs/adr/0003, which is that ruling and names this as the case it does not
	// close.
	//
	// The mutex is real work: submissions arrive on whichever HTTP goroutine took
	// them, and a cut-off runs either on a business day's goroutine or on an
	// operator's request.
	hubMu sync.Mutex
	hub   []payment.PaymentID
}

func (b *Bank) Network() *payment.BankNetwork { return b.net }
func (b *Bank) BIC() iso20022.BIC             { return b.bic }
func (b *Bank) Log() *slog.Logger             { return b.d.log }

// Submit runs this bank's own half of a customer's instruction and puts it in
// this bank's hub. Which bank's half that is belongs to the deployment, because
// on a collection it is not this one; see Deployment.Submit.
func (b *Bank) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	return b.d.Submit(ctx, req)
}

// Pending is what this bank's hub is holding: the instructions it has taken and
// will put in its next file, oldest first.
//
// It is the customer-visible half of accumulation. Without it a payer whose
// submission was answered 202 has no way to tell an instruction waiting for a
// cut-off from one the clearing house has lost, and both look like a payment
// that is Initiated and going nowhere.
func (b *Bank) Pending(ctx context.Context) ([]payment.Payment, error) {
	b.hubMu.Lock()
	ids := slices.Clone(b.hub)
	b.hubMu.Unlock()
	return b.pending(ctx, ids)
}

// Cutoff is this bank reaching its cut-off: everything waiting becomes one file
// per scheme, uploaded to the clearing house, and the order ids come back.
//
// The day engine reaches the same cut-off on every settlement day (see the bank
// cut-off phase in beforeClock); this is the operator asking for one out of turn, which is
// the same relationship POST /cycles/{cid}/close has with the clearing house's.
func (b *Bank) Cutoff(ctx context.Context) ([]ebics.OrderID, error) {
	ids, problems := b.cutoff(ctx)
	return ids, joinProblemDetails(problems)
}

// Lodge moves this bank's own vault cash onto its reserve at the central bank.
// The acting bank is this one and is not an argument.
func (b *Bank) Lodge(ctx context.Context, asset ledger.AssetCode, amount ledger.Amount) (payment.LodgementInstruction, error) {
	return b.lodge(ctx, asset, amount)
}

// RefreshDirectory replaces this bank's copy of the scheme's routing directory
// with the roster the clearing house publishes. It goes through the deployment
// because the roster is the clearing house's table in the clearing house's
// database and no bank may open it; what the deployment stands in for is the
// vendor delivering a file.
func (b *Bank) RefreshDirectory(ctx context.Context) ([]payment.DirectoryEntry, error) {
	return b.d.RefreshDirectory(ctx, b.bic)
}

// runEndOfDay is this bank's own end of day: overdraft interest accrued,
// facility interest accrued, arrears recomputed, in one unit of work.
//
// It runs on EVERY date the deployment advances through, settlement day or not.
// Interest accrues over a weekend — which is the entire reason day-count
// conventions exist — and a bank that only accrued on days the scheme cleared
// would understate a Friday balance by three days every week.
//
// It is the bank's own act rather than the scheme's, so it is not a phase of
// clearing and takes no notice of membership. POST /end-of-day on this bank's
// own port is the same call, which is what makes the day engine a caller of the
// ordinary API rather than a second way to do the same thing.
//
// The DATE is the day being closed and is passed in, because the clock has not
// moved yet when this runs: accruing against tomorrow would date every day's
// interest one day forward.
func (b *Bank) runEndOfDay(ctx context.Context, date time.Time) error {
	ctx = withActor(ctx, b.bic)

	p, err := b.ops.GetBank(ctx, payment.ParticipantID(b.bic))
	if err != nil {
		return fmt.Errorf("server: %s cannot read its own row to run its end of day: %w", b.bic, err)
	}
	if err := p.RunEndOfDay(ctx, date); err != nil {
		return fmt.Errorf("server: %s could not run its end of day for %s: %w", b.bic, date.Format(time.DateOnly), err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Uploading
// ---------------------------------------------------------------------------

// upload marshals an envelope and puts it on one of this bank's two
// connections.
//
// The split is where the domain stops. Structs never cross an institution
// boundary — if two institutions exchanged a *Pacs008 the message format would
// be decoration on a function call, malformed input would stop being a reachable
// failure mode, and the FF01 path would be untestable — so this is the only
// place a document becomes bytes.
//
// What comes back from the host is TECHNICAL: an order id means the file arrived
// and nothing more. What the receiver makes of the payments inside it comes back
// on a later download, which is the seam this transport exists to make visible.
//
// A failure means the file did NOT arrive and this bank still holds it. The
// upload has never been inside anybody's unit of work, so the posting that
// preceded it stands and the caller is told which half happened.
func (b *Bank) upload(ctx context.Context, to iso20022.BIC, c *ebics.Client, env iso20022.Envelope) (ebics.OrderID, error) {
	t, err := orderTypeOf(env.Document)
	if err != nil {
		return "", err
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("server: %s marshalling for %s: %w", b.bic, to, err)
	}
	id, err := c.Upload(ctx, t, raw)
	if err != nil {
		return "", fmt.Errorf("server: %s could not upload a %s to %s: %w", b.bic, t, to, err)
	}
	b.d.journal.file(FileMoved{From: b.bic, To: to, OrderType: t, OrderID: id})
	return id, nil
}

// submit is a bank taking its own customer's instruction into its hub.
//
// WHICH customer depends on the scheme. For a push it is the payer instructing
// their bank, and this half moves their money into the bank's clearing suspense.
// For a pull it is the payee instructing theirs, and this half moves nothing at
// all — the account the money will come out of belongs to another bank's
// customer, and this bank has never seen it.
//
// # Nothing is sent, and that is what a bulk network is
//
// The instruction joins the queue and waits for a cut-off. It is the one door in
// this package that ends without a file on a connection, and the reason is the
// one this whole transport is for: a member bank does not talk to a clearing
// house a payment at a time, it accumulates and uploads.
//
// There is ONE failure mode. A refused instruction moved nothing —
// payment.TakeInstruction posts the debtor leg and proves the payment can be put
// in a file in one unit of work, so an instruction this bank could never send is
// refused before its payer is debited. The half-happened outcome — a committed
// leg behind a file that would not go out — belongs to the cut-off, which is
// where the file is.
func (b *Bank) submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	// Everything below is this bank's work, and is recorded as this bank's. See
	// withActor.
	ctx = withActor(ctx, b.bic)

	p, err := b.ops.TakeInstruction(ctx, req)
	if err != nil {
		return payment.Payment{}, err
	}
	b.hubMu.Lock()
	b.hub = append(b.hub, p.ID)
	b.hubMu.Unlock()
	return p, nil
}

// queued is what the hub is holding, EMPTIED: the ids come out and the hub is
// left ready for the next batch.
//
// Emptying under the lock is what stops one cut-off and a submission arriving
// together from putting the same instruction in two files. A group that could
// not be uploaded goes back in — see cutoff.
func (b *Bank) queued() []payment.PaymentID {
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	ids := b.hub
	b.hub = nil
	return ids
}

// requeue puts instructions back at the FRONT of the hub, so a file that could
// not be uploaded is retried at the next cut-off ahead of whatever arrived while
// it was being tried. A hub is a queue, and a retry does not go to the back of
// one.
//
// It is called ONCE per cut-off, with every instruction that cut-off could not
// send, in the order it took them. Called per failed file it would put the
// second file's instructions ahead of the first's, and the hub's order is what
// batched promises to build the next files out of.
func (b *Bank) requeue(ids []payment.PaymentID) {
	if len(ids) == 0 {
		return
	}
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	b.hub = append(ids, b.hub...)
}

// clearHub throws away everything this bank has taken and not yet cut off.
//
// Reset's, and only Reset's: the instructions name payments whose rows are being
// deleted, and a hub that survived one would upload a file about a payment no
// institution holds. Nothing in the domain discards a hub — an instruction a
// bank has accepted is owed an outcome.
func (b *Bank) clearHub() {
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	b.hub = nil
}

// pending reads the rows behind a list of queued ids, in the order they were
// taken.
func (b *Bank) pending(ctx context.Context, ids []payment.PaymentID) ([]payment.Payment, error) {
	ctx = withActor(ctx, b.bic)

	out := make([]payment.Payment, 0, len(ids))
	for _, id := range ids {
		p, err := b.ops.GetPayment(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("server: %s cannot read %s out of its own hub: %w", b.bic, id, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// cutoff is this bank reaching its cut-off: the hub is emptied into one file per
// batch, and each file is uploaded to the clearing house.
//
// # What a batch is
//
// One SCHEME and one SETTLEMENT DATE, because both are elements of the file's
// own group header and a file asserts one of each. The scheme decides the
// message definition and the asset; IntrBkSttlmDt is the day the whole file
// settles on, and payment's groupSettlementDate refuses a file that would assert
// two. Instructions taken on a Friday and on the Saturday behind it have
// different value dates and are therefore different files, cut off together on
// the Monday — which is what a weekend does to a bulk hub and is worth being
// able to see.
//
// # A file that will not go out is KEPT
//
// The instructions go back in the hub and the next cut-off tries again. That is
// the property this transport made reachable for the first time: an upload can
// genuinely fail, and the uploading institution still holds its file. Nothing
// was posted here to unwind — the debtor legs committed at submission and are
// exactly as valid a moment later — so retrying is the whole remedy.
//
// A file that will not BUILD is kept too, and it is a defect rather than an
// outcome: every instruction in the hub was proved renderable at submission, so
// reaching this means a row changed underneath the hub. It is reported against
// this bank in the day's report, every day, until an operator acts. The
// instructions are not rejected wholesale, because the builder refuses the FILE
// and this bank cannot tell which of the instructions in it it refused.
func (b *Bank) cutoff(ctx context.Context) ([]ebics.OrderID, []Problem) {
	ctx = withActor(ctx, b.bic)

	ids := b.queued()
	if len(ids) == 0 {
		return nil, nil
	}
	ps, err := b.pending(ctx, ids)
	if err != nil {
		b.requeue(ids)
		return nil, []Problem{{Institution: b.bic, Detail: err.Error()}}
	}

	to := b.d.cfg.ClearingHouseBIC
	var orders []ebics.OrderID
	var problems []Problem
	// kept accumulates across the files rather than going back one at a time, so
	// two files that both failed return to the hub in the order this bank took
	// their instructions. See requeue.
	var kept []payment.PaymentID
	for _, batch := range batched(ps) {
		env, err := b.ops.InstructionMessage(ctx, batch, b.d.messageContext(b.bic, to))
		if err != nil {
			kept = append(kept, idsOf(batch)...)
			problems = append(problems, Problem{
				Institution: b.bic,
				Detail:      fmt.Sprintf("building the cut-off file for %s: %v", batch[0].Scheme, err),
			})
			continue
		}
		id, err := b.upload(ctx, to, b.csm, env)
		if err != nil {
			kept = append(kept, idsOf(batch)...)
			problems = append(problems, Problem{Institution: b.bic, Detail: err.Error()})
			continue
		}
		orders = append(orders, id)
	}
	b.requeue(kept)
	return orders, problems
}

// batched groups a hub's instructions into the files they will travel in: one
// per scheme and settlement date, in the order the instructions were taken. See
// cutoff for why those two.
//
// The date is keyed by its INSTANT rather than by the time.Time, because a
// time.Time carries a location and a monotonic reading that a map key would
// compare and payment's groupSettlementDate would not — so two payments that
// agree about the moment they settle would land in two files.
func batched(ps []payment.Payment) [][]payment.Payment {
	type key struct {
		scheme payment.SchemeID
		value  int64
	}
	var order []key
	files := map[key][]payment.Payment{}
	for _, p := range ps {
		k := key{scheme: p.Scheme, value: p.ValueDate.UnixNano()}
		if _, ok := files[k]; !ok {
			order = append(order, k)
		}
		files[k] = append(files[k], p)
	}
	out := make([][]payment.Payment, 0, len(order))
	for _, k := range order {
		out = append(out, files[k])
	}
	return out
}

func idsOf(ps []payment.Payment) []payment.PaymentID {
	out := make([]payment.PaymentID, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

// returnPayment is a bank sending a settled payment back: the R-transaction's
// first hop.
//
// It is submit's counterpart for a payment that is already final: this bank's
// own half posts, and then the file hands the rest of the work to another
// institution.
//
// # It posts BEFORE it uploads, and that is the return's one rule
//
// This bank posts the leg it owns — the clawback if it is the creditor's bank,
// the refund if it is the payer's — and payment.PostReturnLegTx decides which
// from the payment rather than from anything this method passes it. The reserve
// reversal is not among them.
//
// The ordering is what makes a refusal BIND. On a push this bank holds the
// clawback, so a payee who has already spent the money stops the return dead:
// nothing is posted, no pacs.004 is composed, and the caller is told AM04 about
// this bank's OWN customer. A check that was not a posting could be outrun by
// that customer between the check and the upload. On a pull this bank holds the
// refund, which is unconditional, so the forced leg is the other bank's.
//
// # The message is built first, and the seam is between the posting and the upload
//
// Building it can fail — an amount whose asset has no scale, a payment carrying
// no agent to name — and a build that failed AFTER the leg was posted would
// leave this bank's customer moved for a return that never left the building. So
// the message is composed first, and the reason the leg is described by is read
// back off it: both banks then derive that description from the same bytes
// rather than from two renderings of one intent.
//
// What remains is a posting that COMMITTED and an upload that failed, leaving
// this bank's money in its clearing suspense against a return nobody will
// answer. That is returned as an error naming both halves, so the operator can
// see which one happened.
//
// The remedy is to ASK AGAIN. The payment is still Settled — only the SECOND leg
// sets Returned — and payment.PostReturnLegTx makes a redelivered first leg a
// no-op, so the retry rebuilds the message and uploads it with nothing
// double-posted.
//
// # The guard, and why it is here rather than on the connection
//
// A payment that is not Settled cannot be returned — PostReturnLegTx says so —
// and this bank refuses it BEFORE the message exists. The guard is repeated here
// so that the caller is told WHAT the payment is instead of merely that the
// transition was illegal. That sentinel carries the empty code in payment's
// reasonTable, so an institution that collected one could only record it as a
// problem — a return uploaded for an unsettled payment would be answered by
// NOBODY, and the operator who asked would hear nothing. Refusals that CAN be
// answered are left to travel; see CentralBank.receiveReturn.
//
// The payment is read here rather than taken from Deployment.Return: the read is
// what makes the judgement this bank's own, where refusing on the router's
// snapshot would be refusing on hearsay.
func (b *Bank) returnPayment(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	// Everything below is this bank's work, and is recorded as this bank's. See
	// withActor.
	ctx = withActor(ctx, b.bic)

	p, err := b.ops.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != payment.Settled {
		return fmt.Errorf("server: %s cannot return %s, which this network records as %v: %w",
			b.bic, p.ID, p.Status, payment.ErrInvalidStateTransition)
	}

	to := b.d.cfg.ClearingHouseBIC
	env, err := b.ops.ReturnMessage(p, reason, text, b.d.messageContext(b.bic, to))
	if err != nil {
		return fmt.Errorf("server: %s could not build the return of %s: %w", b.bic, p.ID, err)
	}

	// This bank's own leg, described by what the message says. The refusal on a
	// push happens here and costs nothing: the pacs.004 is built and dropped.
	if _, err := b.ops.PostReturnLeg(ctx, p.ID, returnReasonOf(env)); err != nil {
		return fmt.Errorf("server: %s cannot fund its own leg of the return of %s: %w", b.bic, p.ID, err)
	}
	if _, err := b.upload(ctx, to, b.csm, env); err != nil {
		return fmt.Errorf("server: %s posted its leg of the return of %s and could not upload it: %w", b.bic, p.ID, err)
	}
	return nil
}

// returnReasonOf is what a return is described as in a bank's own ledger, read
// off the message that bank is about to upload.
//
// Off the MESSAGE and not off the code and text the caller gave, so that the
// returning bank's leg and the other bank's carry the same words: the other bank
// has nothing but the pacs.004 to read, and two renderings of one intent are two
// things that can drift. payment.ReturnReason is the single reading, and
// receiveReturn reaches it through payment.ReadReturn.
//
// The type assertion cannot fail: ReturnMessage builds a pacs.004 and this is
// called on what it returned. It is written as a comma-ok rather than a bare
// assertion because the alternative is a panic, and the fallback is not a guess:
// "returned" is the word payment.ReturnReason itself uses when a return carries
// no reason at all, so the two banks would still describe the leg identically.
// A leg described vaguely is a worse statement; a panicking process is a worse
// bank.
func returnReasonOf(env iso20022.Envelope) string {
	doc, ok := env.Document.(*iso20022.Pacs004)
	if !ok || len(doc.PmtRtr.TxInf) == 0 {
		return "returned"
	}
	return payment.ReturnReason(doc.PmtRtr.TxInf[0].RtrRsnInf)
}

// lodge is a bank moving its own vault cash onto reserve: the asking half of a
// lodgement, and the file that asks.
//
// It is submit's and returnPayment's shape — this bank's own posting, then a
// file handing the rest to another institution. What differs is the subject:
// submit carries a customer's money and this carries the bank's own.
//
// # Why the bank posts first, and why that is not returnPayment's reason
//
// returnPayment posts before it uploads so that a refusal BINDS. Here the reason
// is the MESSAGE SHAPE: a camt.025 carries no amount, so a bank cannot post its
// own leg from the answer, and the only alternative is holding the outstanding
// request until the answer is collected. That is the shape the clearing house's
// held returns have, and it costs a table in that institution's own database —
// which is the right price for a customer's money coming back and not for a
// reserve credit this bank can post from what it asked for.
//
// # The seam, and it is the same seam
//
// A posting that COMMITTED and an upload that failed leaves this bank's reserve
// mirror raised against a lodgement the central bank was never asked to make. It
// is returned as an error naming both halves rather than swallowed.
//
// The remedy is NOT to ask again, which is the difference from returnPayment's
// seam. payment.LodgeReservesTx keys its posting on the message id, and a retry
// through this method takes a NEW id — so asking again lodges a second time
// rather than completing the first. payment/recon is the instrument for a break
// that never became a file.
func (b *Bank) lodge(ctx context.Context, asset ledger.AssetCode, amount ledger.Amount) (payment.LodgementInstruction, error) {
	// Everything below is this bank's work, and is recorded as this bank's. See
	// withActor.
	ctx = withActor(ctx, b.bic)

	to := b.d.cfg.CentralBankBIC
	in, env, err := b.ops.LodgeReserves(ctx, asset, amount, b.d.messageContext(b.bic, to))
	if err != nil {
		return payment.LodgementInstruction{}, err
	}
	if _, err := b.upload(ctx, to, b.cb, env); err != nil {
		return in, fmt.Errorf("server: %s posted its lodgement %s and could not upload it: %w", b.bic, in.Ref, err)
	}
	return in, nil
}

// ---------------------------------------------------------------------------
// Collecting
// ---------------------------------------------------------------------------

// collect downloads everything waiting for this bank at one host and works
// through it, in the order the host queued it.
//
// It is the only way anything reaches a member bank. Nothing is pushed, so a
// bank that never called this would be a bank whose customers are never told the
// fate of anything — a real operational failure with no analogue in a system
// where results are delivered.
//
// # A file that cannot be dealt with is RECORDED rather than lost
//
// The uploader was told EBICS_OK and went away, so there is nobody to return an
// error to. The failure goes in the day's report instead: noted against the
// institution that could not process it, with the order id it arrived under. A
// report is a value rather than a string,
// because a report is a value an operator reads and a joined error string is
// not.
//
// # A file that will not PARSE is answered as well
//
// FF01, uploaded back to the clearing house, because the sender composed it and
// can fix it. Bytes from the SETTLEMENT AGENT that will not parse are answered
// by nobody: this bank never uploads a status to it, and inventing a connection
// to carry one would be inventing a conversation the topology does not have.
func (b *Bank) collect(ctx context.Context, host iso20022.BIC, c *ebics.Client, t ebics.OrderType) []Problem {
	ctx = withActor(ctx, b.bic)

	files, err := c.Download(ctx, t)
	if err != nil {
		return []Problem{{Institution: b.bic, Detail: fmt.Sprintf("collecting %s from %s: %v", t, host, err)}}
	}

	var problems []Problem
	for _, f := range files {
		if err := b.handle(ctx, host, f); err != nil {
			problems = append(problems, Problem{Institution: b.bic, OrderID: f.OrderID, Detail: err.Error()})
		}
	}
	return problems
}

// handle works through one collected file.
//
// The unmarshalling comes first and its failure is answerable, which is the
// whole reason the HOST is an argument rather than something read out of the
// header: the header is exactly what is unreadable, and the connection the file
// came down is what says who sent it.
//
// A message definition this bank has no arm for is a PROBLEM and not a shrug.
//
// The pacs.004 has an arm because in a real network it travels to the bank that
// did not ask for the return, and that bank moves its own customer's money
// itself. Which of the two banks collects one follows from the direction — the
// payer's bank on a push, the payee's bank on a pull — and neither this method
// nor the clearing house decides it: the domain refuses a bank that is not that
// leg's owner (payment.ErrNotAPartyToThisReturn).
//
// What is left for the default is the pacs.009: a settlement instruction is
// addressed to the settlement agent, names net positions between members and the
// central bank, and is the one message definition this system emits that a member
// bank has nothing to do with.
func (b *Bank) handle(ctx context.Context, host iso20022.BIC, f ebics.File) error {
	env, err := iso20022.Unmarshal(f.Payload)
	if err != nil {
		return b.answerUnreadable(ctx, host, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs008:
		return b.receiveCreditTransfer(ctx, host, env.AppHdr, doc)
	case *iso20022.Pacs003:
		return b.receiveDirectDebit(ctx, host, env.AppHdr, doc)
	case *iso20022.Pacs004:
		return b.receiveReturn(ctx, host, doc)
	case *iso20022.Pacs002:
		return b.receiveStatus(ctx, doc)
	case *iso20022.Camt053:
		return b.receiveStatement(ctx, host, doc)
	case *iso20022.Camt025:
		return b.receiveLodgementReceipt(host, doc)
	default:
		return fmt.Errorf("server: %s has no handler for %s", b.bic, env.AppHdr.MsgDefIdr)
	}
}

// answerUnreadable tells the clearing house that a file it queued for this bank
// could not be parsed. See unreadable, and collect's doc for the one host this
// bank cannot answer.
func (b *Bank) answerUnreadable(ctx context.Context, host iso20022.BIC, cause error) error {
	if host != b.d.cfg.ClearingHouseBIC {
		return fmt.Errorf("server: %s collected %d unreadable bytes from %s and has no connection to answer on: %w",
			b.bic, 0, host, cause)
	}
	env, err := unreadable(b.d.messageContext(b.bic, host), cause)
	if err != nil {
		return errors.Join(fmt.Errorf("server: %s could not build the FF01 for %s: %w", b.bic, host, err), cause)
	}
	_, err = b.upload(ctx, host, b.csm, env)
	return err
}

// receiveCreditTransfer is the PAYEE's bank applying a credit transfer that has
// ALREADY SETTLED.
//
// # There is nothing left to say no to
//
// The instruction reaches this bank only once the cycle carrying it is final, so
// the reserves have moved and the money is in this bank's clearing suspense
// before it has looked at a single line. That is the whole point of settling
// before release: a receiving bank never credits a customer against money that
// has not settled. What it costs is any answer saying no — a pacs.002 — and what
// replaces it is a pacs.004 saying "here it is back", which is what a real SEPA
// bank sends and why AC01 and AC04 are return reasons as well as rejection ones.
//
// # Two questions, and the order decides the code
//
// First: can this file be resolved to instructions at all? That is
// CreditTransferRequest, which resolves the CREDITOR of every transaction — this
// bank's own customer, the only party a pacs.008 routed here by CdtrAgt gives
// this bank any standing to look up — BY ADDRESS, IN THIS BANK'S OWN REGISTER.
// It does not resolve the DEBTOR, who is a customer at the sending bank and
// nothing this bank could ever confirm.
//
// The register searched is this bank's own, so AC01 fires whenever THIS bank
// does not hold the creditor's IBAN — which is what a MISDIRECTED file reaches,
// the counterparty's BIC having been asserted by the payer rather than derived.
//
// Second: does this bank's own half check out? That is AcceptInbound followed by
// SettleAtBank: the payee's account exists, is in the scheme's asset, is
// addressable, can take a credit — and then the money leaves the clearing
// suspense for it.
//
// A FILE that cannot be read at all is a problem in the day's report and nothing
// else. There is nobody to answer: the payments in it have settled, and a bank
// that cannot read the file cannot name the transactions to return either.
func (b *Bank) receiveCreditTransfer(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs008) error {
	txs, err := b.ops.CreditTransferRequest(ctx, doc)
	if err != nil {
		return fmt.Errorf("server: %s cannot read the settled file %s handed it: %w", b.bic, from, err)
	}
	return b.applyAll(ctx, txs)
}

// receiveDirectDebit is the PAYER's bank applying a collection that has already
// settled, and it is the mirror of receiveCreditTransfer in every way except the
// one that matters.
//
// The two questions are the same two. First, DirectDebitRequest resolves the
// DEBTOR of every collection — this bank's own customer, the party a pacs.003
// routed here by DbtrAgt gives this bank standing over — by address, in this
// bank's own register. Second, AcceptInbound.
//
// What differs is what the second question DOES. On a push it releases money
// this bank was handed; here it TAKES money, out of the payer's account and into
// the clearing suspense the settlement already emptied. So this bank has paid
// before it has collected, and AM04 — the payer's balance, which no other
// institution in the system can see — is discovered with the money already gone.
// That is exactly why an unfunded direct debit is a return in SEPA and never a
// reject, and it is the clearest case there is for settling before releasing:
// reverse the two and this bank would be refusing a collection whose reserves
// had not moved, which is a different and much easier system.
//
// Own-register resolution matters more on this side than on the push for the
// same reason: a collection addressed to the wrong bank must not resolve the
// payer and post the debit in the PAYER'S BANK'S BOOK.
func (b *Bank) receiveDirectDebit(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs003) error {
	txs, err := b.ops.DirectDebitRequest(ctx, doc)
	if err != nil {
		return fmt.Errorf("server: %s cannot read the settled file %s handed it: %w", b.bic, from, err)
	}
	return b.applyAll(ctx, txs)
}

// applyAll runs this bank's own half over every transaction in a released file,
// one at a time.
//
// One at a time is the point rather than a convenience: a single payee's closed
// account must not fail a whole batch, so each transaction succeeds, is returned,
// or is reported on its own, and the file's other customers are paid either way.
func (b *Bank) applyAll(ctx context.Context, txs []payment.InboundTransaction) error {
	var errs []error
	for _, in := range txs {
		if err := b.apply(ctx, in); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// apply is this bank's own half for ONE settled transaction: write the row, move
// the money, and send the payment back if it cannot.
//
// # Three outcomes, and only one of them is a failure
//
// APPLIED: the row is written and the leg is posted. On a push the payee is
// credited out of the clearing suspense; on a pull the payer is debited into it.
//
// RETURNED: this bank has a judgement to make about the instruction and the
// judgement is no. The money is parked — payment.ReceiveUnappliedTx says where
// and why — and a pacs.004 goes back up the connection the file came down.
//
// REPORTED: this bank's own bookkeeping failed, which is not a judgement about
// anything and is nobody's to be told. payment.Answerable is the discrimination,
// and it is load-bearing: a dropped connection turned into a return would send a
// customer's money back across the network because a database was busy.
//
// # A file delivered twice
//
// AcceptInbound answers a redelivery by doing nothing when the row is still
// Initiated, and refuses one that arrived after the payment moved on. Neither is
// a judgement about the sender's instruction — payment's reasonTable classifies
// ErrInvalidStateTransition with the empty code for exactly that reason — so it
// is reported rather than returned. Returning it would send back, on the wire, a
// payment this bank had in fact applied.
func (b *Bank) apply(ctx context.Context, in payment.InboundTransaction) error {
	// An address this bank could not resolve is this transaction's own refusal
	// and no other's; see payment.InboundTransaction.
	if in.Refusal != nil {
		return b.sendBack(ctx, in, in.Refusal)
	}
	if err := b.ops.AcceptInbound(ctx, in.ID, in.Request); err != nil {
		if !payment.Answerable(err) {
			return fmt.Errorf("server: %s cannot take on %s: %w", b.bic, in.ID, err)
		}
		return b.sendBack(ctx, in, err)
	}
	if _, err := b.ops.SettleAtBank(ctx, in.ID); err != nil {
		return fmt.Errorf("server: %s took on %s and could not apply it: %w", b.bic, in.ID, err)
	}
	return nil
}

// sendBack records a settled payment this bank cannot apply and returns it.
//
// The row comes first and it is not bookkeeping for its own sake: a return is an
// edge from Settled, so there has to BE a copy of this payment at this bank
// before there is anything to return — and the money has to be somewhere this
// bank can name, or its clearing suspense stands short with nothing explaining
// it. payment.ReceiveUnappliedTx does both in one unit of work.
//
// The reason travels as a RETURN code and not as the rejection code the same
// refusal would have carried an hour earlier. They are different external sets
// and a pacs.004 may only carry a member of its own; see payment.ReturnReasonFor.
//
// returnPayment is the ordinary first hop of any return, unchanged: this bank's
// own leg posts before the file is uploaded, which is what makes a refusal bind.
// Here the leg is the one ReceiveUnappliedTx left it — the unclaimed pool on a
// push, its own claim on a pull — so there is nothing for it to refuse, which is
// right: a bank cannot decline to hand back money it never paid out.
func (b *Bank) sendBack(ctx context.Context, in payment.InboundTransaction, cause error) error {
	if _, err := b.ops.ReceiveUnapplied(ctx, in.ID, in.Request); err != nil {
		return fmt.Errorf("server: %s cannot apply %s and cannot record holding it: %w",
			b.bic, in.ID, errors.Join(cause, err))
	}
	b.d.journal.outcome(TransactionOutcome{
		DecidedBy: b.bic,
		Payment:   in.ID,
		Status:    iso20022.TransactionStatusSettlementCompleted,
		Code:      payment.ReasonFor(cause),
		Text:      cause.Error(),
	})
	if err := b.returnPayment(ctx, in.ID, payment.ReturnReasonFor(cause), cause.Error()); err != nil {
		return fmt.Errorf("server: %s cannot apply %s and cannot send it back: %w",
			b.bic, in.ID, errors.Join(cause, err))
	}
	return nil
}

// receiveStatus is a bank learning what became of a payment it is party to.
//
// An ACCEPTANCE moves no money and is written down anyway. An ACCP is the
// clearing house saying it has taken the payment into a cycle; what it asks this
// bank to do is REMEMBER, because the rejection arm below refuses on this bank's
// own copy's status and there is no other copy for it to read. A bank that
// dropped the ACCP would reverse a live debit on the next pacs.002 to name this
// payment. See payment.AcceptAtBankTx.
//
// Only the bank the ACCP is addressed to gets one — the submitter, waiting for
// the answer to its instruction. The bank that ANSWERED that instruction is not
// told and keeps a copy that says Initiated until settlement.
//
// A SETTLEMENT COMPLETION is a different status about a different moment, and it
// is where the payee is finally paid: the reserves have moved at the central
// bank, and the creditor's bank releases the money out of its own clearing
// suspense into its own customer's account. Both banks are told and only one has
// that leg, so payment.ErrNotThisBanksPayment coming back is the ORDINARY case
// for one of the two recipients rather than a failure.
//
// A REJECTION is a decision both of the payment's banks record, and it is work
// for at most one of them:
//
//   - The PAYER's bank reverses the debit that put its customer's money into its
//     clearing suspense. That is the only work a rejection ever creates, and
//     only this bank can do it.
//   - The OTHER bank closes the copy it wrote when it answered the instruction.
//
// WHICH of the two this bank is, it does not decide: payment.RejectAtBankTx
// reads the acting bank off its own network and reverses only if that bank is
// the payer's. A bank that is party to NEITHER side is refused by the STORE — an
// institution that was never sent this payment holds no row for it.
//
// A status naming no transaction at all is skipped rather than refused: that is
// the FF01 a clearing house uploads when it could not parse a file, so there is
// nothing here to act on.
//
// # A status about a RETURN is a different message about a different thing
//
// The answer to a pacs.004 is collected here too, and everything above is wrong
// about it: a rejected return is not a rejected payment, the payment it names is
// still Settled, and the bank that asked is neither the payer's bank nor the
// submitter on a push. Read as a rejection it would be refused by this handler's
// own guards — correctly, since reversing a debtor leg is what must not happen —
// and the bank that asked would learn nothing. So it is told apart by what the
// status says it is ABOUT: see returnMsgDef and receiveReturnStatus.
func (b *Bank) receiveStatus(ctx context.Context, doc *iso20022.Pacs002) error {
	if isAbout(doc, returnMsgDef) {
		return b.receiveReturnStatus(ctx, doc)
	}
	_, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			continue
		}
		if r.Status == iso20022.TransactionStatusSettlementCompleted {
			// ACSC. Both recipients record it on their own copy, and only the
			// payee's bank posts anything — see payment.SettleAtBankTx.
			if _, err := b.ops.SettleAtBank(ctx, payment.PaymentID(r.TxID)); err != nil {
				return fmt.Errorf("server: %s could not settle its own half of %s: %w", b.bic, r.TxID, err)
			}
			continue
		}
		if r.Status == iso20022.TransactionStatusAccepted {
			// ACCP. Recorded and not acted on — see the note above on why a bank
			// that only READ this would be unable to refuse a rejection later.
			if _, err := b.ops.AcceptAtBank(ctx, payment.PaymentID(r.TxID)); err != nil {
				return fmt.Errorf("server: %s could not record the acceptance of %s: %w", b.bic, r.TxID, err)
			}
			continue
		}
		if r.Status != iso20022.TransactionStatusRejected {
			continue
		}
		// The decision goes onto this bank's OWN copy, and the payer's money comes
		// back in the same unit of work if this bank is the one holding it.
		//
		// The guard is the domain's rather than a status read here: the same
		// question asked where the answer and the posting cannot come apart, and
		// asked against the only status this bank can honestly cite — its own. See
		// payment.RejectAtBankTx.
		//
		// What is at stake if it comes apart: reversing on the message alone would
		// take a live debit back off a payment on its way to settlement, and the
		// money would simply be gone from the flow — this suspense is the PAYER's
		// bank's, so the debit is what funds that bank's own mirror leg when the
		// cut-off settles.
		if _, err := b.ops.RejectAtBank(ctx, payment.PaymentID(r.TxID), r.Code, rejectionText(r)); err != nil {
			return fmt.Errorf("server: %s could not record the rejection of %s: %w", b.bic, r.TxID, err)
		}
	}
	return nil
}

// receiveReturn is the OTHER bank's leg of a return: the one the bank that asked
// does not hold, posted from the pacs.004 the clearing house released.
//
// # It answers nothing, and the reason is receiveStatement's
//
// This is not an instruction. By the time it is collected the reserves have
// moved and the return is final — that is why the clearing house held it back
// until it had an ACSC (ClearingHouse.relayReturn) — so a bank answering "no"
// would be refusing something that has happened. There is no pacs.002 in this
// arm, and its absence is the message definition rather than an omission.
//
// What a failure produces instead is a PROBLEM in the day's report. That is a
// real price: the return is settled at the central bank and this bank's customer
// leg is missing, leaving the payment Settled for ever with one leg standing in
// the other bank's book. Nothing in this flow can put that back, so it is
// payment/recon's to find.
//
// # Which leg, and whose business it is
//
// Neither this handler nor the clearing house decides.
// payment.PostReturnLegTx reads the acting bank off its own network and works
// out from the PAYMENT which leg that bank owns, refusing a bank that is neither
// with payment.ErrNotAPartyToThisReturn.
//
// It cannot refuse the leg on its merits, which is the other half of the return's
// one rule: the returning bank posted before it uploaded and could say no; this
// bank posts after finality and cannot. On a pull that is the creditor's bank
// forcing a clawback against a biller who may be gone, with the shortfall landing
// in its own Returns Receivable.
//
// # It reads the message the same way the settlement agent does
//
// payment.ReadReturn, which is also what CentralBank.receiveReturn calls. This
// bank holds a payment row of its OWN and could read the reason off that; the
// message is what it reads, because the message is what a bank in a real network
// has and is the only source for anything the other bank decided.
func (b *Bank) receiveReturn(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs004) error {
	ins, err := payment.ReadReturn(doc)
	if err != nil {
		return fmt.Errorf("server: %s could not read the return %s sent it: %w", b.bic, from, err)
	}
	for _, in := range ins {
		if _, err := b.ops.PostReturnLeg(ctx, in.PaymentID, in.Reason); err != nil {
			return fmt.Errorf("server: %s could not post its leg of the return of %s: %w", b.bic, in.PaymentID, err)
		}
	}
	return nil
}

// receiveReturnStatus is the bank that asked for a return learning what the
// settlement agent did with it.
//
// # An ACSC needs nothing from this bank, and a REFUSAL does
//
// Both halves have flipped. This bank posts its own leg BEFORE it uploads the
// pacs.004 (returnPayment, payment.PostReturnLegTx), so:
//
//   - an ACSC finds the leg already posted and needs nothing further. The other
//     bank's leg is the other bank's, made from the pacs.004 the clearing house
//     releases on this same answer. What the message buys is that this bank KNOWS.
//   - an RJCT finds this bank holding a posting against a return that will not
//     happen — its customer's money moved for nothing — and unwinding it is the
//     work. payment.ReverseReturnLegTx is equal and opposite, through a ledger
//     reversal, so the original stays in the book marked Reversed.
//
// # The unwind refuses a return that COMPLETED, and that guard is the point
//
// ReverseReturnLegTx takes only a payment that is still Settled. A status that
// arrives late, or twice, would otherwise reverse one leg of a finished return
// and leave the other standing — the payee made whole while the payer keeps the
// refund, both postings individually balanced and nothing in the ledger to
// notice. This handler acts on a message and cannot be relied on to have
// checked; the domain is where that check belongs, and it answers
// ErrInvalidStateTransition. Here that surfaces as a problem in the day's
// report, which is the truthful outcome: an RJCT about a return this network
// completed is a message nobody can act on.
//
// The code is still logged, because the code is the one thing that arrives on
// the wire and is nowhere in the store.
func (b *Bank) receiveReturnStatus(ctx context.Context, doc *iso20022.Pacs002) error {
	_, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A status naming no transaction is skipped rather than refused, for
			// receiveStatus's reason: it is the FF01 a counterparty uploads when it
			// could not parse a file, and there is no leg it could be about.
			continue
		}
		if r.Status != iso20022.TransactionStatusRejected {
			// An ACSC, and it is WORK now where the doc above says it is not.
			//
			// Nothing else writes on this copy: the return's second customer leg
			// lands in the OTHER bank's database, so without this it would say
			// Settled for ever about a payment this bank itself returned.
			if _, err := b.ops.CompleteReturn(ctx, payment.PaymentID(r.TxID)); err != nil {
				return fmt.Errorf("server: %s could not record that its return of %s went through: %w",
					b.bic, r.TxID, err)
			}
			continue
		}
		b.d.log.Error("server: return refused",
			"bank", b.bic, "payment", r.TxID, "code", r.Code, "reason", r.Text)
		if err := b.ops.ReverseReturnLeg(ctx, payment.PaymentID(r.TxID), rejectionText(r)); err != nil {
			return fmt.Errorf("server: %s could not unwind its leg of the refused return of %s: %w", b.bic, r.TxID, err)
		}
	}
	return nil
}

// receiveStatement is a bank booking its own share of a cut-off, from the
// central bank's statement of its reserve account.
//
// # It answers nothing
//
// Every other document a bank collects from the clearing house produces a
// pacs.002. This produces none, and that is the message definition rather than
// an omission: a statement is not an instruction, the central bank has already
// settled, and a bank that answered "no" would be refusing something that has
// happened. It also arrives on the other connection, and a member bank uploads
// no status to the settlement agent at all.
//
// What a failure produces instead is a PROBLEM in the day's report, and NO
// advice row at all: payment.PostSettlementAdvice is one unit of work, so a
// mirror leg that fails takes the row with it. In the STORE the unreconciled
// position is a clearing suspense that has not returned to zero with no advice
// row against the cycle, which is payment/recon's to find.
//
// # One statement, one member
//
// This system's central bank queues a member a statement about that member's own
// reserve account and nothing else, so a document carrying several is one this
// handler has no rule for. It is refused whole rather than partially booked, for
// cycleOf's reason: booking the first and dropping the rest would move a reserve
// mirror by the wrong amount with nothing recording it.
//
// # It does not check WHO sent it, and that is deliberate
//
// `from` is used in the error messages and in no decision. What is checked is
// OWNERSHIP — the account the statement names must be this bank's own reserve
// account at the central bank — and that is the domain's question, because the
// domain holds the chart of accounts. See payment.PostSettlementAdviceTx and
// ErrStatementNotForThisBank.
//
// What ownership buys is NOT "only the settlement agent may advise me". That is
// a strictly stronger guarantee and this system does not make it: any file in
// this queue that named this bank's own settlement account would be booked here.
// What it does buy is that nobody can move this bank's mirror by advising it
// about somebody else's position, which is the failure that would cost money.
func (b *Bank) receiveStatement(ctx context.Context, from iso20022.BIC, doc *iso20022.Camt053) error {
	moves, err := payment.ReadStatement(doc)
	if err != nil {
		return fmt.Errorf("server: %s could not read the statement from %s: %w", b.bic, from, err)
	}
	if len(moves) != 1 {
		return fmt.Errorf("server: %s got a statement from %s carrying %d accounts; a member is told about its own",
			b.bic, from, len(moves))
	}
	if _, err := b.ops.PostSettlementAdvice(ctx, moves[0]); err != nil {
		return fmt.Errorf("server: %s could not book the settlement of %s: %w", b.bic, moves[0].Reference, err)
	}
	return nil
}

// receiveLodgementReceipt is the lodging bank being told what became of its
// request.
//
// There is nothing to post. This bank's leg committed before the camt.050 was
// uploaded, and the receipt carries no amount to post from even if there were —
// so what arrives is a CONFIRMATION, and the shape of this handler follows from
// that: it posts nothing and it answers nobody.
//
// # An accepted receipt is logged and nothing else
//
// The reserve mirror this bank raised is now matched by the central bank's own
// entry, which is exactly what this bank already assumed. Nothing in the store
// records the difference between "assumed" and "confirmed", because this system
// keeps no lodgement row — the durable trace is the idempotency key on each
// institution's own posting.
//
// # A REFUSED receipt is the interval nothing closes
//
// It means this bank's Reserve at Central Bank says more than the central bank's
// book does, and will go on saying it. That is stated here rather than handled,
// and the honest reason is that handling it needs something this system does not
// have: the amount to reverse is not on the receipt.
//
// What makes it unreachable rather than merely unhandled is the guard on the
// asking side. payment.LodgeReservesTx refuses a bank that cannot name its own
// settlement account, and nothing writes that reference before the central
// bank's account exists — so a bank able to ask is one the agent holds an
// account for. It is logged at ERROR for that reason: reaching it means the
// guard's premise is false, which is a defect in this system rather than news
// about the request.
func (b *Bank) receiveLodgementReceipt(from iso20022.BIC, doc *iso20022.Camt025) error {
	r, err := payment.ReadLodgementReceipt(doc)
	if err != nil {
		return fmt.Errorf("server: %s could not read the lodgement receipt %s sent it: %w", b.bic, from, err)
	}
	if r.Accepted() {
		b.d.log.Info("server: lodgement accepted",
			"bank", b.bic, "from", from, "lodgement", r.Ref)
		return nil
	}
	b.d.log.Error("server: lodgement refused, and this bank's reserve mirror is now overstated",
		"bank", b.bic, "from", from, "lodgement", r.Ref, "reason", r.Reason,
		"note", "LodgeReservesTx's guard is meant to make this unreachable; see Bank.receiveLodgementReceipt")
	return nil
}

// rejectionText is what the reversal is described as in the payer's bank's own
// ledger: the code, and the free text beside it when there is one. See
// payment.CodeAndText, and payment.TransactionStatusReport for why both are
// carried.
func rejectionText(r payment.TransactionStatusReport) string {
	return payment.CodeAndText(string(r.Code), r.Text, "rejected")
}
