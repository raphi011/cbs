// Package bank is one member bank: its own book, its own address, the two
// connections it dials out on, and everything it does with what it collects.
package bank

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/node"
	"github.com/raphi011/cbs/payment"
)

// A Bank is one member bank. It knows the environment it runs in and nothing
// about the deployment that built it.
type Bank struct {
	env node.Env

	// net is this bank's whole view of the domain, and it has ONE caller:
	// Network, which api/bank's surface reads every request through. Everything
	// this file does goes through ops instead.
	net *payment.BankNetwork

	// ops is the same network NARROWED. A bank that called SettleCycle through
	// it does not compile; see ops.go for what that is worth and what it is not.
	ops ops

	bic iso20022.BIC

	// The two hosts this bank dials, and the whole of its address book. Nothing
	// is ever pushed at a member bank: a bank that never collects is a bank whose
	// customers are never told the fate of anything.
	csm *ebics.Client
	cb  *ebics.Client

	// hub is the instructions this bank has taken and not yet put in a file, in
	// the order its customers handed them in.
	hubMu sync.Mutex
	hub   []payment.PaymentID
}

// New builds one member bank over its own network, with the two connections it
// dials out on.
func New(env node.Env, net *payment.BankNetwork, bic iso20022.BIC, csm, cb *ebics.Client) *Bank {
	return &Bank{env: env, net: net, ops: net, bic: bic, csm: csm, cb: cb}
}

func (b *Bank) Network() *payment.BankNetwork { return b.net }
func (b *Bank) BIC() iso20022.BIC             { return b.bic }
func (b *Bank) Log() *slog.Logger             { return b.env.Log }

// Pending is what this bank's hub is holding: the instructions it has taken and
// will put in its next file, oldest first.
func (b *Bank) Pending(ctx context.Context) ([]payment.Payment, error) {
	b.hubMu.Lock()
	ids := slices.Clone(b.hub)
	b.hubMu.Unlock()
	return b.pending(ctx, ids)
}

// Cutoff is this bank reaching its cut-off: everything waiting becomes one file
// per scheme, uploaded to the clearing house, and the order ids come back.
func (b *Bank) Cutoff(ctx context.Context) ([]ebics.OrderID, error) {
	ids, problems := b.RunCutoff(ctx)
	return ids, node.JoinProblemDetails(problems)
}

// Lodge moves this bank's own vault cash onto its reserve at the central bank:
// the asking half of a lodgement, and the file that asks. The acting bank is
// this one and is not an argument.
func (b *Bank) Lodge(ctx context.Context, asset ledger.AssetCode, amount ledger.Amount) (payment.LodgementInstruction, error) {
	ctx = node.WithActor(ctx, b.bic)

	to := b.env.CentralBankBIC
	in, env, err := b.ops.LodgeReserves(ctx, asset, amount, b.env.MessageContext(b.bic, to))
	if err != nil {
		return payment.LodgementInstruction{}, err
	}
	if _, err := b.upload(ctx, to, b.cb, env); err != nil {
		return in, fmt.Errorf("server: %s posted its lodgement %s and could not upload it: %w", b.bic, in.Ref, err)
	}
	return in, nil
}

// routes reports whether this bank's own copy of the routing directory names an
// address. A stale directory is a real condition to be in, and it is the one a
// member bank actually acts on: nothing here reads the clearing house's rows.
func (b *Bank) routes(ctx context.Context, bic iso20022.BIC) (bool, error) {
	entries, err := b.ops.ListDirectory(ctx)
	if err != nil {
		return false, fmt.Errorf("server: %s cannot read its own routing directory: %w", b.bic, err)
	}
	for _, e := range entries {
		if e.BIC == bic {
			return true, nil
		}
	}
	return false, nil
}

// ErrNoRoutingTable is a clearing house that is publishing none. The subscriber
// keeps the copy it has: a stale directory is a state this system models and an
// empty one is not one a host that has not published yet should cause.
var ErrNoRoutingTable = errors.New("server: the clearing house publishes no routing table")

// RefreshDirectory is this bank subscribing: it collects the routing table the
// clearing house publishes and replaces its own copy with it. Nothing here reads
// the clearing house's rows.
func (b *Bank) RefreshDirectory(ctx context.Context) ([]payment.DirectoryEntry, error) {
	ctx = node.WithActor(ctx, b.bic)

	from := b.env.ClearingHouseBIC
	files, err := b.csm.Download(ctx, ebics.HRD)
	if err != nil {
		return nil, fmt.Errorf("server: %s could not collect the routing table from %s: %w", b.bic, from, err)
	}
	if len(files) != 1 {
		return nil, fmt.Errorf("server: %s collected %d routing tables from %s: %w", b.bic, len(files), from, ErrNoRoutingTable)
	}
	table, err := payment.ReadRosterFile(files[0].Payload)
	if err != nil {
		return nil, fmt.Errorf("server: %s could not read the routing table %s publishes: %w", b.bic, from, err)
	}
	return b.ops.RefreshDirectory(ctx, table.Members)
}

// RunEndOfDay is this bank's own end of day: overdraft interest accrued,
// facility interest accrued, arrears recomputed, in one unit of work.
func (b *Bank) RunEndOfDay(ctx context.Context, date time.Time) error {
	ctx = node.WithActor(ctx, b.bic)

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
func (b *Bank) upload(ctx context.Context, to iso20022.BIC, c *ebics.Client, env iso20022.Envelope) (ebics.OrderID, error) {
	t, err := node.OrderTypeOf(env.Document)
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
	b.env.Journal.File(node.FileMoved{From: b.bic, To: to, OrderType: t, OrderID: id})
	return id, nil
}

// Submit is a bank taking its own customer's instruction into its hub. Every
// question it asks is answered from THIS bank's own rows, which is the whole of
// what a bank has: its scheme registry, its routing directory, its register.
func (b *Bank) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	ctx = node.WithActor(ctx, b.bic)

	scheme, ok := b.ops.Scheme(req.Scheme)
	if !ok {
		return payment.Payment{}, fmt.Errorf("server: %s has not joined %q: %w", b.bic, req.Scheme, payment.ErrSchemeNotFound)
	}
	// A payment that never leaves one bank is not a payment this system carries.
	if req.DebtorDetails.Agent != "" && req.DebtorDetails.Agent == req.CreditorDetails.Agent {
		return payment.Payment{}, fmt.Errorf("server: %s is both the payer's bank and the payee's for this instruction: %w",
			req.DebtorDetails.Agent, payment.ErrOnUsPayment)
	}
	// And a payment one of whose banks this bank's directory does not route to.
	for _, side := range []struct {
		role  string
		agent iso20022.BIC
	}{
		{"payer's bank", req.DebtorDetails.Agent},
		{"payee's bank", req.CreditorDetails.Agent},
	} {
		// A side naming no bank is skipped rather than refused, exactly as the
		// on-us guard above skips it: "not a member" is not the truth about a
		// party the request did not name.
		if side.agent == "" {
			continue
		}
		routed, err := b.routes(ctx, side.agent)
		if err != nil {
			return payment.Payment{}, err
		}
		if !routed {
			return payment.Payment{}, fmt.Errorf("server: %s's directory does not route to the %s, %s, under %s: %w",
				b.bic, side.role, side.agent, req.Scheme, payment.ErrBankNotAdmitted)
		}
	}
	// And an instruction this scheme has the OTHER side send. On a collection the
	// submitting bank is the payee's, so this is half the schemes rather than a
	// corner: a console that wants to submit as another bank posts to that bank.
	if submitter := payment.SubmitterOf(scheme, req.DebtorDetails.Agent, req.CreditorDetails.Agent); submitter != b.bic {
		return payment.Payment{}, fmt.Errorf("server: %s was handed an instruction %s submits under %s: %w",
			b.bic, submitter, req.Scheme, payment.ErrNotTheSubmittingAgent)
	}
	// On-us, asked by ADDRESS, and this is the arm that fires for an instruction a
	// customer actually hands in.
	counterparty := req.Creditor
	if scheme.Direction() == payment.Pull {
		counterparty = req.Debtor
	}
	if counterparty.Identifier != (deposit.Identifier{}) {
		switch _, err := b.ops.ResolveIdentifier(ctx, counterparty.Identifier); {
		case err == nil:
			return payment.Payment{}, fmt.Errorf("server: %s holds both the payer's account and the payee's for this instruction: %w",
				b.bic, payment.ErrOnUsPayment)
		case errors.Is(err, deposit.ErrIdentifierNotFound):
			// The ordinary case: the payee is somebody else's customer, which is
			// the only thing this bank can conclude and the only thing it needs to.
		default:
			return payment.Payment{}, err
		}
	}

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
func (b *Bank) queued() []payment.PaymentID {
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	ids := b.hub
	b.hub = nil
	return ids
}

// requeue puts instructions back at the FRONT of the hub, so a file that could
// not be uploaded is retried at the next cut-off ahead of whatever arrived
// while it was being tried.
func (b *Bank) requeue(ids []payment.PaymentID) {
	if len(ids) == 0 {
		return
	}
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	b.hub = append(ids, b.hub...)
}

// ClearHub throws away everything this bank has taken and not yet cut off.
func (b *Bank) ClearHub() {
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	b.hub = nil
}

// pending reads the rows behind a list of queued ids, in the order they were taken.
func (b *Bank) pending(ctx context.Context, ids []payment.PaymentID) ([]payment.Payment, error) {
	ctx = node.WithActor(ctx, b.bic)

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

// RunCutoff is this bank reaching its cut-off: the hub is emptied into one file
// per batch, and each file is uploaded to the clearing house.
func (b *Bank) RunCutoff(ctx context.Context) ([]ebics.OrderID, []node.Problem) {
	ctx = node.WithActor(ctx, b.bic)

	ids := b.queued()
	if len(ids) == 0 {
		return nil, nil
	}
	ps, err := b.pending(ctx, ids)
	if err != nil {
		b.requeue(ids)
		return nil, []node.Problem{{Institution: b.bic, Detail: err.Error()}}
	}

	to := b.env.ClearingHouseBIC
	var orders []ebics.OrderID
	var problems []node.Problem
	// kept accumulates across the files rather than going back one at a time, so
	// two files that both failed return to the hub in the order this bank took
	// their instructions. See requeue.
	var kept []payment.PaymentID
	for _, batch := range batched(ps) {
		env, err := b.ops.InstructionMessage(ctx, batch, b.env.MessageContext(b.bic, to))
		if err != nil {
			kept = append(kept, idsOf(batch)...)
			problems = append(problems, node.Problem{
				Institution: b.bic,
				Detail:      fmt.Sprintf("building the cut-off file for %s: %v", batch[0].Scheme, err),
			})
			continue
		}
		id, err := b.upload(ctx, to, b.csm, env)
		if err != nil {
			kept = append(kept, idsOf(batch)...)
			problems = append(problems, node.Problem{Institution: b.bic, Detail: err.Error()})
			continue
		}
		orders = append(orders, id)
	}
	b.requeue(kept)
	return orders, problems
}

// batched groups a hub's instructions into the files they will travel in: one
// per scheme and settlement date, in the order the instructions were taken. See
// RunCutoff for why those two.
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

// Return is a bank sending a settled payment back: the R-transaction's first
// hop.
func (b *Bank) Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	ctx = node.WithActor(ctx, b.bic)

	p, err := b.ops.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != payment.Settled {
		return fmt.Errorf("server: %s cannot return %s, which this network records as %v: %w",
			b.bic, p.ID, p.Status, payment.ErrInvalidStateTransition)
	}

	to := b.env.ClearingHouseBIC
	env, err := b.ops.ReturnMessage(p, reason, text, b.env.MessageContext(b.bic, to))
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
func returnReasonOf(env iso20022.Envelope) string {
	doc, ok := env.Document.(*iso20022.Pacs004)
	if !ok || len(doc.PmtRtr.TxInf) == 0 {
		return "returned"
	}
	return payment.ReturnReason(doc.PmtRtr.TxInf[0].RtrRsnInf)
}

// ---------------------------------------------------------------------------
// Collecting
// ---------------------------------------------------------------------------

// Collect downloads everything waiting for this bank at one of the two hosts it
// dials and works through it, in the order the host queued it.
func (b *Bank) Collect(ctx context.Context, host iso20022.BIC, t ebics.OrderType) []node.Problem {
	ctx = node.WithActor(ctx, b.bic)

	c, err := b.dial(host)
	if err != nil {
		return []node.Problem{{Institution: b.bic, Detail: err.Error()}}
	}
	files, err := c.Download(ctx, t)
	if err != nil {
		return []node.Problem{{Institution: b.bic, Detail: fmt.Sprintf("collecting %s from %s: %v", t, host, err)}}
	}

	var problems []node.Problem
	for _, f := range files {
		if err := b.handle(ctx, host, f); err != nil {
			problems = append(problems, node.Problem{Institution: b.bic, OrderID: f.OrderID, Detail: err.Error()})
		}
	}
	return problems
}

// dial is this bank's connection to one of the two hosts, and the whole of its
// address book: an address it has no connection to is not somewhere it can go.
func (b *Bank) dial(host iso20022.BIC) (*ebics.Client, error) {
	switch host {
	case b.env.ClearingHouseBIC:
		return b.csm, nil
	case b.env.CentralBankBIC:
		return b.cb, nil
	default:
		return nil, fmt.Errorf("server: %s dials the clearing house and the settlement agent, and %s is neither", b.bic, host)
	}
}

// handle works through one collected file.
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
// could not be parsed. See node.Unreadable, and Collect's doc for the one host
// this bank cannot answer.
func (b *Bank) answerUnreadable(ctx context.Context, host iso20022.BIC, cause error) error {
	if host != b.env.ClearingHouseBIC {
		return fmt.Errorf("server: %s collected %d unreadable bytes from %s and has no connection to answer on: %w",
			b.bic, 0, host, cause)
	}
	env, err := node.Unreadable(b.env.MessageContext(b.bic, host), cause)
	if err != nil {
		return errors.Join(fmt.Errorf("server: %s could not build the FF01 for %s: %w", b.bic, host, err), cause)
	}
	_, err = b.upload(ctx, host, b.csm, env)
	return err
}

// receiveCreditTransfer is the PAYEE's bank applying a credit transfer that has
// ALREADY SETTLED.
func (b *Bank) receiveCreditTransfer(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs008) error {
	txs, err := b.ops.CreditTransferRequest(ctx, doc)
	if err != nil {
		return fmt.Errorf("server: %s cannot read the settled file %s handed it: %w", b.bic, from, err)
	}
	return b.applyAll(ctx, txs)
}

// receiveDirectDebit is the PAYER's bank applying a collection that has already
// settled, and it is the mirror of receiveCreditTransfer in every way except
// the one that matters.
func (b *Bank) receiveDirectDebit(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs003) error {
	txs, err := b.ops.DirectDebitRequest(ctx, doc)
	if err != nil {
		return fmt.Errorf("server: %s cannot read the settled file %s handed it: %w", b.bic, from, err)
	}
	return b.applyAll(ctx, txs)
}

// applyAll runs this bank's own half over every transaction in a released file,
// one at a time.
func (b *Bank) applyAll(ctx context.Context, txs []payment.InboundTransaction) error {
	var errs []error
	for _, in := range txs {
		if err := b.apply(ctx, in); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// apply is this bank's own half for ONE settled transaction: write the row,
// move the money, and send the payment back if it cannot.
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
func (b *Bank) sendBack(ctx context.Context, in payment.InboundTransaction, cause error) error {
	if _, err := b.ops.ReceiveUnapplied(ctx, in.ID, in.Request); err != nil {
		return fmt.Errorf("server: %s cannot apply %s and cannot record holding it: %w",
			b.bic, in.ID, errors.Join(cause, err))
	}
	b.env.Journal.Outcome(node.TransactionOutcome{
		DecidedBy: b.bic,
		Payment:   in.ID,
		Status:    iso20022.TransactionStatusSettlementCompleted,
		Code:      payment.ReasonFor(cause),
		Text:      cause.Error(),
	})
	if err := b.Return(ctx, in.ID, payment.ReturnReasonFor(cause), cause.Error()); err != nil {
		return fmt.Errorf("server: %s cannot apply %s and cannot send it back: %w",
			b.bic, in.ID, errors.Join(cause, err))
	}
	return nil
}

// receiveStatus is a bank learning what became of a payment it is party to.
func (b *Bank) receiveStatus(ctx context.Context, doc *iso20022.Pacs002) error {
	if node.IsAbout(doc, node.ReturnMsgDef) {
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
		if _, err := b.ops.RejectAtBank(ctx, payment.PaymentID(r.TxID), r.Code, rejectionText(r)); err != nil {
			return fmt.Errorf("server: %s could not record the rejection of %s: %w", b.bic, r.TxID, err)
		}
	}
	return nil
}

// receiveReturn is the OTHER bank's leg of a return: the one the bank that
// asked does not hold, posted from the pacs.004 the clearing house released.
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
			if _, err := b.ops.CompleteReturn(ctx, payment.PaymentID(r.TxID)); err != nil {
				return fmt.Errorf("server: %s could not record that its return of %s went through: %w",
					b.bic, r.TxID, err)
			}
			continue
		}
		b.env.Log.Error("server: return refused",
			"bank", b.bic, "payment", r.TxID, "code", r.Code, "reason", r.Text)
		if err := b.ops.ReverseReturnLeg(ctx, payment.PaymentID(r.TxID), rejectionText(r)); err != nil {
			return fmt.Errorf("server: %s could not unwind its leg of the refused return of %s: %w", b.bic, r.TxID, err)
		}
	}
	return nil
}

// receiveStatement is a bank booking its own share of a cut-off, from the
// central bank's statement of its reserve account.
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
func (b *Bank) receiveLodgementReceipt(from iso20022.BIC, doc *iso20022.Camt025) error {
	r, err := payment.ReadLodgementReceipt(doc)
	if err != nil {
		return fmt.Errorf("server: %s could not read the lodgement receipt %s sent it: %w", b.bic, from, err)
	}
	if r.Accepted() {
		b.env.Log.Info("server: lodgement accepted",
			"bank", b.bic, "from", from, "lodgement", r.Ref)
		return nil
	}
	b.env.Log.Error("server: lodgement refused, and this bank's reserve mirror is now overstated",
		"bank", b.bic, "from", from, "lodgement", r.Ref, "reason", r.Reason,
		"note", "LodgeReservesTx's guard is meant to make this unreachable; see Bank.receiveLodgementReceipt")
	return nil
}

// rejectionText is what the reversal is described as in the payer's bank's own
// ledger: the code, and the free text beside it when there is one.
func rejectionText(r payment.TransactionStatusReport) string {
	return payment.CodeAndText(string(r.Code), r.Text, "rejected")
}
