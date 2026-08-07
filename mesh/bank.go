package mesh

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// bank is one member bank as an actor: what it does with a message, and what it
// does when its own customer instructs it.
//
// It holds a bankOps and not a *payment.Network, so a bank that tried to take a
// payment into a clearing cycle would not compile. See ops.go.
//
// # The three roles one bank plays
//
// A credit transfer touches this type three times, and they are three different
// banks in the same code: the PAYER's bank submits (submit), which is the only
// half that moves money at initiation; the PAYEE's bank receives the pacs.008
// (receiveCreditTransfer) and answers yes or no; and the PAYER's bank receives
// the answer (receiveStatus), giving the money back if it was no.
//
// Which one is running is decided entirely by which actor's inbox the message
// landed in. That is what makes "who may know what, and when" a question with an
// answer here.
//
// A direct debit is the same three with the banks swapped and the money moving
// at a different moment. The PAYEE's bank submits, because a collection is the
// payee asking for what it is owed — and its submission moves NOTHING, because
// the account being collected from is at the other bank. The PAYER's bank
// receives the pacs.003, and that is the half that moves money and the only
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
// The OTHER bank plays the fifth role: it is sent the pacs.004 after finality
// (receiveReturn) and posts the leg the returning bank does not hold. It cannot
// refuse, because the reserves have already moved. So the two roles are one
// rule: a bank can refuse a leg only if it posts it before it sends. A REFUSED
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
//
// # A seventh role, played once, before this actor was addressable
//
// A bank's own ADMISSION. Mesh.Admit founds the bank, registers this actor and
// sends the first acmt.007 on its behalf, so the flow that brings this type into
// existence is the one flow it does not begin inside a handler. It has handlers
// for the answer: receiveAdmission records the settlement account numbers the
// scheme opened, and receiveAdmissionRejection learns that it did not. Neither
// answers anything, for receiveStatement's reason.
type bank struct {
	m   *Mesh
	ops bankOps

	// bic is who this actor is, on the wire: what a counterparty addresses it by
	// and what it signs its own messages From. The mesh's index of banks is keyed
	// by ParticipantID instead, because that is what an instruction names; see
	// Mesh.banks.
	bic iso20022.BIC

	// There was one, and it was passed down to nine of the methods on bankOps —
	// which participant is posting this leg, whose register resolves this
	// address, which member this statement is about. Its own doc called it "a
	// loan and not the answer": Mesh.banks is keyed by ParticipantID, so the
	// actor was being handed back something the mesh already knew, and nothing
	// in payment refused an actor that named somebody else. The identity was the
	// caller's assertion, on every call, in the layer that would have to be
	// wrong for the domain's guards to matter.
	//
	// It is the ops field now. Each bank actor is built over that bank's own
	// payment.Network (see Mesh.nets), so which participant is acting is a
	// property of the handle rather than an argument, and there is no argument
	// left to name a different one with.
	//
	// What the actor still holds is its bic, because that is a fact about the
	// WIRE — what it signs messages with and what a counterparty addresses —
	// which the domain's identity is not.
}

// handle dispatches on the message that arrived.
//
// The unmarshalling comes first and its failure is answerable, which is the
// whole reason this takes the sender as an argument rather than reading it out
// of the header: the header is exactly what is unreadable.
//
// A message type this bank has no handler for is an ERROR and not a shrug.
//
// The pacs.004 has an arm because in a real network it travels to the bank that
// did not ask for the return, and that bank moves its own customer's money
// itself. Which of the two banks is sent one follows from the direction — the
// payer's bank on a push, the payee's bank on a pull — and neither this handler
// nor the clearing house decides it: the domain refuses a bank that is not that
// leg's owner (payment.ErrNotAPartyToThisReturn). See receiveReturn, and the
// return flow in the package doc.
//
// The two acmt arms are the answers to this bank's OWN admission, and the
// acmt.007 is deliberately not among them: a bank composes an application and is
// never sent one. Nothing routes one here — the clearing house relays them to
// the settlement agent — so it falls to the default like any other message
// addressed to the wrong institution.
//
// What is left for the default besides that is the pacs.009: a settlement
// instruction is addressed to the settlement agent, names net positions between
// members and the central bank, and is the one message definition this system
// emits that a member bank has nothing to do with. Nobody sends one here — the
// clearing house addresses them to the central bank — which is why
// TestAMessageAnActorHasNoHandlerForIsADeadLetter has to inject one.
func (b *bank) handle(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return b.m.answerUnreadable(b.bic, from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs008:
		return b.receiveCreditTransfer(ctx, from, env.AppHdr, doc)
	case *iso20022.Pacs003:
		return b.receiveDirectDebit(ctx, from, env.AppHdr, doc)
	case *iso20022.Pacs004:
		return b.receiveReturn(ctx, from, doc)
	case *iso20022.Pacs002:
		return b.receiveStatus(ctx, doc)
	case *iso20022.Camt053:
		return b.receiveStatement(ctx, from, doc)
	case *iso20022.Camt025:
		return b.receiveLodgementReceipt(from, doc)
	case *iso20022.Acmt010:
		return b.receiveAdmission(ctx, from, doc)
	case *iso20022.Acmt011:
		return b.receiveAdmissionRejection(from, doc)
	default:
		return fmt.Errorf("mesh: %s has no handler for %s", b.bic, env.AppHdr.MsgDefIdr)
	}
}

// submit is a bank taking its own customer's instruction: the submitting half,
// then the message that hands it to the clearing house.
//
// WHICH customer depends on the scheme. For a push it is the payer instructing
// their bank, and this half moves their money into the bank's clearing suspense.
// For a pull it is the payee instructing theirs, and this half moves nothing at
// all — the account the money will come out of belongs to another bank's
// customer, and this bank has never seen it. Mesh.Submit is what routes the
// instruction to the right one of the two.
//
// Two steps and two failure modes, and they are not the same. A refused
// instruction moved nothing and is the caller's answer — and "moved nothing" is
// a property of the ONE unit of work SubmitAndInstruct runs, which builds the
// message as well as posting the leg. It did not always: while the two were
// separate transactions, a payee this bank cannot address was a 422 with the
// payer already debited. A message that could not be SENT still leaves a
// payment Initiated that nobody will ever answer — half-happened, the same seam
// RejectAtCSMTx documents — so it is returned as an error with the payment
// beside it rather than swallowed.
func (b *bank) submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	// Everything below is this bank's work, and is recorded as this bank's. See
	// withActor.
	ctx = withActor(ctx, b.bic)

	to := b.m.cfg.ClearingHouseBIC
	p, env, err := b.ops.SubmitAndInstruct(ctx, req, payment.MessageContext{
		From:  b.bic,
		To:    to,
		MsgID: b.m.nextMsgID(b.bic),
		Now:   b.m.now(),
	})
	if err != nil {
		return payment.Payment{}, err
	}
	if err := b.m.send(b.bic, to, env); err != nil {
		return p, fmt.Errorf("mesh: %s submitted %s and could not send it: %w", b.bic, p.ID, err)
	}
	return p, nil
}

// returnPayment is a bank sending a settled payment back: the R-transaction's
// first hop.
//
// It is submit's counterpart for a payment that is already final: this bank's
// own half posts, and then the message hands the rest of the work to another
// institution.
//
// # It posts BEFORE it sends, and that is the return's one rule
//
// This bank posts the leg it owns — the clawback if it is the creditor's bank,
// the refund if it is the payer's — and payment.PostReturnLegTx decides which
// from the payment rather than from anything this handler passes it. The reserve
// reversal is not among them.
//
// The ordering is what makes a refusal BIND. On a push this bank holds the
// clawback, so a payee who has already spent the money stops the return dead:
// nothing is posted, no pacs.004 is composed, and the caller is told AM04 about
// this bank's OWN customer. A check that was not a posting could be outrun by
// that customer between the check and the send. On a pull this bank holds the
// refund, which is unconditional, so the forced leg is the other bank's. See
// TestAPayeeWhoSpentTheMoneyStopsTheReturnBeforeItIsSent.
//
// # The message is built first, and the seam is between the posting and the send
//
// Building it can fail — an amount whose asset has no scale, a payment carrying
// no agent to name — and a build that failed AFTER the leg was posted would
// leave this bank's customer moved for a return that never left the building. So
// the message is composed first, and the reason the leg is described by is read
// back off it: both banks then derive that description from the same bytes
// rather than from two renderings of one intent.
//
// What remains is a posting that COMMITTED and a send that failed, leaving this
// bank's money in its clearing suspense against a return nobody will answer.
// That is returned as an error naming both halves, so the operator can see which
// one happened.
//
// The remedy is to ASK AGAIN, and saying so is the point of naming the seam: the
// payment is still Settled — only the SECOND leg sets Returned — and
// payment.PostReturnLegTx makes a redelivered first leg a no-op, so the retry
// rebuilds the message and sends it with nothing double-posted. It does NOT come
// back with the payment beside it, as submit's does: nothing above this reads
// one, and the half-happened state is visible where it actually is, on the
// payment row.
//
// # The guard, and why it is here rather than on the wire
//
// A payment that is not Settled cannot be returned — PostReturnLegTx says so —
// and this bank refuses it BEFORE the message exists. The guard is repeated here
// so that the caller is told WHAT the payment is instead of merely that the
// transition was illegal. That sentinel carries the empty code in payment's
// reasonTable, so an actor that got one must dead-letter it — a return sent for
// an unsettled payment would be answered by NOBODY, and the operator who asked
// would hear nothing. Refusals that CAN be answered are left to travel; see
// centralBank.receiveReturn.
//
// The payment is read here rather than taken from Mesh.Return: the read is what
// makes the judgement this bank's own, where refusing on the router's snapshot
// would be refusing on hearsay.
func (b *bank) returnPayment(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	// Everything below is this bank's work, and is recorded as this bank's. See
	// withActor.
	ctx = withActor(ctx, b.bic)

	p, err := b.ops.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != payment.Settled {
		return fmt.Errorf("mesh: %s cannot return %s, which this network records as %v: %w",
			b.bic, p.ID, p.Status, payment.ErrInvalidStateTransition)
	}

	to := b.m.cfg.ClearingHouseBIC
	env, err := b.ops.ReturnMessage(p, reason, text, payment.MessageContext{
		From:  b.bic,
		To:    to,
		MsgID: b.m.nextMsgID(b.bic),
		Now:   b.m.now(),
	})
	if err != nil {
		return fmt.Errorf("mesh: %s could not build the return of %s: %w", b.bic, p.ID, err)
	}

	// This bank's own leg, described by what the message says. The refusal on a
	// push happens here and costs nothing: the pacs.004 is built and dropped.
	if _, err := b.ops.PostReturnLeg(ctx, p.ID, returnReasonOf(env)); err != nil {
		return fmt.Errorf("mesh: %s cannot fund its own leg of the return of %s: %w", b.bic, p.ID, err)
	}
	if err := b.m.send(b.bic, to, env); err != nil {
		return fmt.Errorf("mesh: %s posted its leg of the return of %s and could not send it: %w", b.bic, p.ID, err)
	}
	return nil
}

// returnReasonOf is what a return is described as in a bank's own ledger, read
// off the message that bank is about to send.
//
// Off the MESSAGE and not off the code and text the caller gave, so that the
// returning bank's leg and the other bank's carry the same words: the other
// bank has nothing but the pacs.004 to read, and two renderings of one intent
// are two things that can drift. payment.ReturnReason is the single reading,
// and receiveReturn reaches it through payment.ReadReturn.
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

// receiveCreditTransfer is the PAYEE's bank answering a credit transfer.
//
// Two questions, asked in this order, and the order is what decides the code the
// sender gets back.
//
// First: can this message be resolved to an instruction at all? That is
// CreditTransferRequest, which resolves the CREDITOR — this bank's own
// customer, the only party a pacs.008 routed here by CdtrAgt gives this bank
// any standing to look up — BY ADDRESS, IN THIS BANK'S OWN REGISTER. It is the
// question a real receiving bank asks first, because until it is answered the
// bank does not know the message is even for one of its customers. It does not
// resolve the DEBTOR — see payment.CreditTransferRequest and localPartyIn — so
// an unaddressable or unknown debtor IBAN, which names a customer at the
// SENDING bank and nothing this bank could ever confirm, is not refused here.
//
// # Own register, and the two tasks it took
//
// The register searched is the one belonging to this actor's own
// payment.Network, so AC01 fires whenever THIS bank does not hold the creditor's
// IBAN.
//
// That matters on the WRONGLY routed message rather than on the happy path. The
// counterparty's BIC is asserted by the payer rather than derived
// (payment.SubmitPaymentTx says why it has to be), so this handler is what a
// misdirected credit transfer reaches — and it holds no such address, so it
// answers AC01. See mesh/books_test.go's
// TestAWrongCounterpartyAgentIsRefusedByTheBankItNames.
//
// Second: does this bank's own half check out? That is AcceptInbound: the payee's
// account exists, is in the scheme's asset, is addressable, and can take a
// credit.
func (b *bank) receiveCreditTransfer(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs008) error {
	body := doc.FIToFICstmrCdtTrf
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: hdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	// Unmarshal refuses a pacs.008 with no transactions (iso20022's
	// FIToFICustomerCreditTransfer.validate), so there is always a first one to
	// refer back by. More than one is refused below, by CreditTransferRequest.
	ref := body.CdtTrfTxInf[0].PmtId

	req, err := b.ops.CreditTransferRequest(ctx, doc)
	if err != nil {
		return b.answer(from, orig, ref, err)
	}
	return b.accept(ctx, from, orig, ref, req)
}

// receiveDirectDebit is the PAYER's bank answering a collection, and it is the
// mirror of receiveCreditTransfer in every way except the one that matters.
//
// The two questions are the same two, in the same order. First, can this message
// be resolved to an instruction at all — DirectDebitRequest, which resolves the
// DEBTOR — this bank's own customer, the party a pacs.003 routed here by
// DbtrAgt gives this bank standing over — BY ADDRESS, in this bank's own
// register, for the whole of the reason receiveCreditTransfer's doc gives. The
// CREDITOR is the sending bank's customer and is not resolved, for the same
// reason receiveCreditTransfer's debtor is not — see that handler and
// payment.DirectDebitRequest. Second, does this bank's own half check out —
// AcceptInbound.
//
// What differs is what the second question DOES. On a push it is a check and
// nothing more; here it is the posting. The payer's money leaves their account
// for this bank's clearing suspense at the moment this handler says yes, because
// this is the first moment any actor in the system has been able to look at that
// account at all. So a refusal here is a refusal that no other party could have
// made: AM04 is the payer's balance, and the bank that submitted this collection
// has no way of knowing it.
//
// That is also why own-register resolution matters more on this side than on the
// push: a collection addressed to the wrong bank must not resolve the payer and
// post the debit in the PAYER'S BANK'S BOOK. The bank that was wrongly named
// holds no such address and answers AC01 before anything posts.
//
// The resolved request is discarded for the same reason receiveCreditTransfer
// discards its own — one store, one payment row, loaded by the identifier the
// message carries. See that handler for the whole of it.
func (b *bank) receiveDirectDebit(ctx context.Context, from iso20022.BIC, hdr iso20022.AppHdr, doc *iso20022.Pacs003) error {
	body := doc.FIToFICstmrDrctDbt
	orig := payment.OriginalMessage{
		MsgID:     body.GrpHdr.MsgId,
		MsgDefIdr: hdr.MsgDefIdr,
		CreDtTm:   body.GrpHdr.CreDtTm.Time,
	}
	// Unmarshal refuses a pacs.003 with no transactions (iso20022's
	// FIToFICustomerDirectDebit.validate), so there is always a first one to
	// refer back by. More than one is refused below, by DirectDebitRequest.
	ref := body.DrctDbtTxInf[0].PmtId

	req, err := b.ops.DirectDebitRequest(ctx, doc)
	if err != nil {
		return b.answer(from, orig, ref, err)
	}
	return b.accept(ctx, from, orig, ref, req)
}

// accept runs the receiving bank's own half and answers with the result. It is
// the second of the two questions both receive handlers ask, and it is shared
// because the direction changes what the half DOES and not what this actor does
// about it.
//
// The REQUEST goes through it: this bank has no row for the payment, so what the
// resolution produced IS the payment, written here under the id the message
// carries in PmtId/TxId. See payment.AcceptInboundTx.
//
// The id and the request come off the same message and are passed separately,
// which is worth one line: a request describes an instruction and carries no id,
// because the act that MINTS one is the submitting bank's and there is exactly
// one of those in the system (payment.SubmitPaymentTx).
func (b *bank) accept(ctx context.Context, from iso20022.BIC, orig payment.OriginalMessage,
	ref iso20022.PaymentIdentification, req payment.InitiatePaymentRequest) error {

	if err := b.ops.AcceptInbound(ctx, payment.PaymentID(ref.TxId), req); err != nil {
		// Already answered. A queue redelivers, so the same message can arrive
		// twice — and the second time the payment is no longer Initiated, which
		// is what this sentinel says. It must NOT become a rejection: payment's
		// reasonTable classifies it with the empty code precisely because it
		// describes a defect in this system rather than a judgement about the
		// sender's instruction, and ReasonFor would turn it into MS03 and reject,
		// on the wire, a payment this bank in fact accepted. A dead letter is the
		// right channel: nobody to answer, and visible in Drain.
		//
		// A redelivery that arrives while the payment is STILL Initiated does not
		// reach here at all: AcceptInboundTx's pull arm returns nil on a payment
		// that already has a debtor leg, so the collection is answered a second
		// time with the same yes rather than with the ledger's idempotency
		// refusal — which has no entry in reasonTable and would come back MS03.
		if errors.Is(err, payment.ErrInvalidStateTransition) {
			return fmt.Errorf("mesh: %s was sent %s again and it is no longer Initiated: %w", b.bic, ref.TxId, err)
		}
		return b.answer(from, orig, ref, err)
	}
	return b.answer(from, orig, ref, nil)
}

// answer sends the pacs.002 back to whoever handed the message over: accepted if
// cause is nil, rejected with the code cause maps to if not.
//
// Back to the SENDER and not to the bank that submitted, because those are
// different parties and this bank was never given the submitter's address to
// answer at. The clearing house relays; see csm.receiveStatus.
func (b *bank) answer(to iso20022.BIC, orig payment.OriginalMessage, ref iso20022.PaymentIdentification, cause error) error {
	report := payment.TransactionStatusReport{
		EndToEndID: ref.EndToEndId,
		TxID:       ref.TxId,
		Status:     iso20022.TransactionStatusAccepted,
	}
	if cause != nil {
		report.Status = iso20022.TransactionStatusRejected
		report.Code = payment.ReasonFor(cause)
		report.Text = cause.Error()
	}
	env, err := payment.StatusMessage(orig, []payment.TransactionStatusReport{report}, payment.MessageContext{
		From:  b.bic,
		To:    to,
		MsgID: b.m.nextMsgID(b.bic),
		Now:   b.m.now(),
	})
	if err != nil {
		return errors.Join(fmt.Errorf("mesh: %s could not build its pacs.002 for %s: %w", b.bic, to, err), cause)
	}
	// The cause is NOT returned once it has been answered. A rejection that
	// reached the counterparty is completed work, not a failure: the sender
	// knows, the code says why, and the flow carries on. Returning it as well
	// would make every AC01 a dead letter — which is the channel for what nobody
	// could be told.
	return b.m.send(b.bic, to, env)
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
// told and keeps a copy that says Initiated until settlement; see csm.tell.
//
// A SETTLEMENT COMPLETION is a different status about a different moment, and it
// is where the payee is finally paid: the reserves have moved at the central
// bank, and the creditor's bank releases the money out of its own clearing
// suspense into its own customer's account. Both banks are told and only one has
// that leg, so payment.ErrNotThisBanksPayment coming back is the ORDINARY case
// for one of the two recipients rather than a failure. See
// payment.SettleAtBankTx and csm.tellSettled.
//
// A REJECTION is a decision both of the payment's banks record, and it is work
// for at most one of them:
//
//   - The PAYER's bank reverses the debit that put its customer's money into its
//     clearing suspense. That is the only work a rejection ever creates, and
//     only this bank can do it.
//   - The OTHER bank closes the copy it wrote when it answered the instruction.
//     On a pull that is the payee's bank, which never held the money; on a push
//     it is the payee's bank again — the one that accepted the pacs.008 and was
//     then refused by the clearing house.
//
// WHICH of the two this bank is, it does not decide: payment.RejectAtBankTx
// reads the acting bank off its own network and reverses only if that bank is
// the payer's. A bank that is party to NEITHER side is refused by the STORE — an
// institution that was never sent this payment holds no row for it, so GetPayment
// below is ErrPaymentNotFound, which cannot be got past by a bank that knows the
// id. See TestABankRefusesAStatusAboutAnotherBanksPayment.
//
// A status naming no transaction at all is skipped rather than refused: that is
// the FF01 a clearing house sends when it could not parse a file, so there is
// nothing here to act on.
//
// # A status about a RETURN is a different message about a different thing
//
// The answer to a pacs.004 arrives here too, and everything above is wrong about
// it: a rejected return is not a rejected payment, the payment it names is still
// Settled, and the bank that asked is neither the payer's bank nor the submitter
// on a push. Read as a rejection it would be refused by this handler's own
// guards — correctly, since reversing a debtor leg is what must not happen — and
// the bank that asked would learn nothing. So it is told apart by what the
// status says it is ABOUT: see returnMsgDef and receiveReturnStatus.
func (b *bank) receiveStatus(ctx context.Context, doc *iso20022.Pacs002) error {
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
			// payee's bank posts anything — see payment.SettleAtBankTx. A bank that is
			// party to NEITHER side is refused, and mostly by the store: it has no row
			// for a payment it was never sent.
			if _, err := b.ops.SettleAtBank(ctx, payment.PaymentID(r.TxID)); err != nil {
				return fmt.Errorf("mesh: %s could not settle its own half of %s: %w", b.bic, r.TxID, err)
			}
			continue
		}
		if r.Status == iso20022.TransactionStatusAccepted {
			// ACCP. Recorded and not acted on — see the note above on why a bank
			// that only READ this would be unable to refuse a rejection later. A
			// redelivered acceptance is ErrInvalidStateTransition and becomes a
			// dead letter, which is the same answer the clearing house gives its
			// own redelivery one hop back.
			if _, err := b.ops.AcceptAtBank(ctx, payment.PaymentID(r.TxID)); err != nil {
				return fmt.Errorf("mesh: %s could not record the acceptance of %s: %w", b.bic, r.TxID, err)
			}
			continue
		}
		if r.Status != iso20022.TransactionStatusRejected {
			continue
		}
		// The decision goes onto this bank's OWN copy, and the payer's money comes
		// back in the same unit of work if this bank is the one holding it.
		//
		// The last of them was the whole safety argument — ReverseDebtorLegTx looks
		// at no status — and it worked because the CLEARING HOUSE had written
		// Rejected onto the row this bank was reading. It writes on its own row
		// now, so the guard read Initiated and refused every genuine rejection.
		// What replaces it is the domain refusing the TRANSITION, which is the same
		// question asked where the answer and the posting cannot come apart, and
		// asked against the only status this bank can honestly cite: its own. See
		// payment.RejectAtBankTx.
		//
		// What is at stake if it comes apart has not changed: reversing on the
		// message alone would take a live debit back off a payment on its way to
		// settlement, and the money would simply be gone from the flow — this
		// suspense is the PAYER's bank's, so the debit is what funds that bank's
		// own mirror leg when the cut-off settles.
		if _, err := b.ops.RejectAtBank(ctx, payment.PaymentID(r.TxID), r.Code, rejectionText(r)); err != nil {
			return fmt.Errorf("mesh: %s could not record the rejection of %s: %w", b.bic, r.TxID, err)
		}
	}
	return nil
}

// receiveReturn is the OTHER bank's leg of a return: the one the bank that asked
// does not hold, posted from the pacs.004 the clearing house relayed.
//
// # It answers nothing, and the reason is receiveStatement's
//
// This is not an instruction. By the time it arrives the reserves have moved and
// the return is final — that is why the clearing house held it back until it had
// an ACSC (csm.relayReturn) — so a bank answering "no" would be refusing
// something that has happened. There is no pacs.002 in this arm, and its absence
// is the message definition rather than an omission.
//
// What a failure produces instead is an ERROR, which this transport turns into a
// dead letter. That is a real price: the return is settled at the central bank
// and this bank's customer leg is missing, leaving the payment Settled for ever
// with one leg standing in the other bank's book. Nothing in this flow can put
// that back, so it is payment/recon's to find.
//
// # Which leg, and whose business it is
//
// Neither this handler nor the clearing house decides.
// payment.PostReturnLegTx reads the acting bank off its own network and works
// out from the PAYMENT which leg that bank owns, refusing a bank that is neither
// with payment.ErrNotAPartyToThisReturn. The acting bank is this actor and
// cannot be another, so a misrouted pacs.004 becomes a dead letter rather than a
// posting in somebody else's return.
//
// It cannot refuse the leg on its merits, which is the other half of the return's
// one rule: the returning bank posted before it sent and could say no; this bank
// posts after finality and cannot. On a pull that is the creditor's bank forcing
// a clawback against a biller who may be gone, with the shortfall landing in its
// own Returns Receivable.
//
// # It reads the message the same way the settlement agent does
//
// payment.ReadReturn, which is also what centralBank.receiveReturn calls. This
// bank holds a payment row of its OWN and could read the reason off that; the
// message is what it reads, because the message is what a bank in a real network
// has and is the only source for anything the other bank decided.
//
// The loop is over what the reader returns, written for what the reader can
// PRODUCE rather than for what this flow sends: ReadReturn does not refuse a
// bulk message, because a pacs.004 is shaped for several returns per file. One
// could not reach here anyway — the settlement agent refuses a bulk return WHOLE
// before any ACSC exists.
func (b *bank) receiveReturn(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs004) error {
	ins, err := payment.ReadReturn(doc)
	if err != nil {
		return fmt.Errorf("mesh: %s could not read the return %s sent it: %w", b.bic, from, err)
	}
	for _, in := range ins {
		if _, err := b.ops.PostReturnLeg(ctx, in.PaymentID, in.Reason); err != nil {
			return fmt.Errorf("mesh: %s could not post its leg of the return of %s: %w", b.bic, in.PaymentID, err)
		}
	}
	return nil
}

// receiveReturnStatus is the bank that asked for a return learning what the
// settlement agent did with it.
//
// # An ACSC needs nothing from this bank, and a REFUSAL does
//
// Both halves have flipped. This bank posts its own leg BEFORE it sends the
// pacs.004 (returnPayment, payment.PostReturnLegTx), so:
//
//   - an ACSC finds the leg already posted and needs nothing further. The other
//     bank's leg is the other bank's, made from the pacs.004 the clearing house
//     relays on this same answer. What the message buys is that this bank KNOWS.
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
// ErrInvalidStateTransition. Here that surfaces as a dead letter, which is the
// truthful outcome: an RJCT about a return this network completed is a message
// nobody can act on.
//
// The code is still logged, because the code is the one thing that arrives on
// the wire and is nowhere in the store.
//
// It is emphatically NOT a dead letter in the ordinary case. A dead letter is
// for what nobody could be told, and this bank was told: it asked, and it has
// its answer.
func (b *bank) receiveReturnStatus(ctx context.Context, doc *iso20022.Pacs002) error {
	_, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.TxID == "" {
			// A status naming no transaction is skipped rather than refused, for
			// receiveStatus's reason: it is the FF01 a counterparty sends when it
			// could not parse a file, and there is no leg it could be about.
			continue
		}
		if r.Status != iso20022.TransactionStatusRejected {
			// An ACSC, and it is WORK now where the doc above says it is not.
			//
			// Nothing else writes on this copy: the return's second customer leg lands
			// in the OTHER bank's database, so without this it would say Settled for
			// ever about a payment this bank itself returned.
			//
			// Nothing is posted here. See payment.CompleteReturnTx, and
			// PostReturnLegTx's note on why the returner is first by construction
			// and therefore cannot be the one that closes the return.
			if _, err := b.ops.CompleteReturn(ctx, payment.PaymentID(r.TxID)); err != nil {
				return fmt.Errorf("mesh: %s could not record that its return of %s went through: %w",
					b.bic, r.TxID, err)
			}
			continue
		}
		b.m.log.Error("mesh: return refused",
			"bank", b.bic, "payment", r.TxID, "code", r.Code, "reason", r.Text)
		if err := b.ops.ReverseReturnLeg(ctx, payment.PaymentID(r.TxID), rejectionText(r)); err != nil {
			return fmt.Errorf("mesh: %s could not unwind its leg of the refused return of %s: %w", b.bic, r.TxID, err)
		}
	}
	return nil
}

// receiveStatement is a bank booking its own share of a cut-off, from the
// central bank's statement of its reserve account.
//
// # It answers nothing
//
// Every other inbound message in this package produces a pacs.002. This produces
// none, and that is the message definition rather than an omission: a statement
// is not an instruction, the central bank has already settled, and a bank that
// answered "no" would be refusing something that has happened.
//
// What a failure produces instead is an ERROR, which this transport turns into a
// dead letter, and NO advice row at all: payment.PostSettlementAdvice is one unit
// of work, so a mirror leg that fails takes the row with it. In the STORE the
// unreconciled position is a clearing suspense that has not returned to zero
// with no advice row against the cycle, which is payment/recon's to find.
//
// # One statement, one member
//
// This system's central bank sends a member a statement about that member's own
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
// a strictly stronger guarantee and this system does not make it: any actor that
// named this bank's own settlement account would be booked here, because the row
// it produces is indistinguishable from a real one. What it does buy is that
// nobody can move this bank's mirror by advising it about somebody else's
// position, which is the failure that would cost money.
func (b *bank) receiveStatement(ctx context.Context, from iso20022.BIC, doc *iso20022.Camt053) error {
	moves, err := payment.ReadStatement(doc)
	if err != nil {
		return fmt.Errorf("mesh: %s could not read the statement from %s: %w", b.bic, from, err)
	}
	if len(moves) != 1 {
		return fmt.Errorf("mesh: %s got a statement from %s carrying %d accounts; a member is told about its own",
			b.bic, from, len(moves))
	}
	if _, err := b.ops.PostSettlementAdvice(ctx, moves[0]); err != nil {
		return fmt.Errorf("mesh: %s could not book the settlement of %s: %w", b.bic, moves[0].Reference, err)
	}
	return nil
}

// receiveAdmission is the joining bank learning that it is a member, and writing
// down the settlement account numbers it has just been told.
//
// It is the last hop of the only flow in this system that BRINGS AN ACTOR INTO
// EXISTENCE, and the only one whose first message was sent by a party nothing
// could address a moment earlier. Mesh.Admit founded this bank, registered it
// and sent the acmt.007; everything since has happened at two other institutions.
//
// # It answers nothing, and that is receiveStatement's reason again
//
// An acknowledgement is not an instruction: the account has been opened and the
// routing entry written before this message exists, so a bank answering "no"
// would be refusing something that has happened. A failure is an ERROR, which
// this transport turns into a dead letter, and the state it leaves is a real
// one rather than a corruption — a bank the scheme has admitted that has not
// recorded its own membership, which is what an operator re-drives.
//
// # The settlement account numbers arrive HERE and nowhere else
//
// They are the central bank's own account ids, and this is the account holder's
// note of them — the way a customer knows their IBAN without holding the bank's
// ledger. payment.DepositTx is what reads them back, and that read is legitimate
// for exactly this reason. See payment.RecordMembershipTx.
//
// # Which bank it is, is this actor's own identity and not the message's
//
// This actor's own payment.Network, as everywhere else in this type. That says
// which member is recording; it says nothing about which member the MESSAGE
// names, so the domain still checks that the address on the acknowledgement is
// this bank's own (payment.ErrNotThisBanksAdmission) — ErrStatementNotForThisBank's
// argument one flow over.
//
// A SECOND acknowledgement of one admission is ordinary rather than exceptional:
// one acmt.007 asks for one currency, so a bank joining in two assets is
// answered twice and the second answer is what tells it its second settlement
// account. The domain records or extends and refuses neither.
func (b *bank) receiveAdmission(ctx context.Context, from iso20022.BIC, doc *iso20022.Acmt010) error {
	ack, err := payment.ReadAdmissionAcknowledgement(doc)
	if err != nil {
		return fmt.Errorf("mesh: %s could not read the admission acknowledgement %s sent it: %w", b.bic, from, err)
	}
	if _, err := b.ops.RecordMembership(ctx, ack); err != nil {
		return fmt.Errorf("mesh: %s could not record its own admission: %w", b.bic, err)
	}
	return nil
}

// receiveAdmissionRejection is the joining bank being told its application was
// refused.
//
// There is nothing to write. The bank stays Founded — which is a working bank
// that can open customer accounts and take cash in, and cannot lodge it — and that is already what
// it is, because nothing about a bank changes when it applies. The
// state an operator re-drives is exactly the state this message leaves.
//
// So the refusal is LOGGED and nothing else, for the reason
// receiveReturnStatus logs a refused return's code: the reason is the one thing
// that arrives on the wire and is nowhere in the store. It is prose rather than
// a code, because References6 makes RjctnRsn a Max350Text and an
// account-management refusal is free text where a payment rejection is a code
// set (see iso20022.AccountRejectionReferences).
//
// It is emphatically not a dead letter. A dead letter is for what nobody could
// be told, and this bank has been told.
func (b *bank) receiveAdmissionRejection(from iso20022.BIC, doc *iso20022.Acmt011) error {
	b.m.log.Error("mesh: admission refused",
		"bank", b.bic, "from", from,
		"admission", doc.AcctReqRjctn.Refs.PrcId.Id,
		"reason", strings.Join(doc.AcctReqRjctn.Refs.RjctnRsn, "; "))
	return nil
}

// lodge is a bank moving its own vault cash onto reserve: the asking half of a
// lodgement, and the message that asks.
//
// It is submit's and returnPayment's shape — this bank's own posting, then a
// message handing the rest to another institution — and it is the third act in
// this package with that shape. What differs is the subject: submit carries a
// customer's money and this carries the bank's own.
//
// # Why the bank posts first, and why that is not returnPayment's reason
//
// returnPayment posts before it sends so that a refusal BINDS — a payee who has
// spent the money must not be outrun between a check and a send. Here the reason
// is the MESSAGE SHAPE: a camt.025 carries no amount, so a bank cannot post its
// own leg from the answer, and the only alternative is holding the outstanding
// request in this actor until the answer arrives. That is csm.held's shape, whose
// known defect is that it does not survive a restart, and a second one of those
// is not worth a reserve credit. See payment.LodgeReservesTx and
// iso20022.Camt025.
//
// # The seam, and it is the same seam
//
// A posting that COMMITTED and a send that failed leaves this bank's reserve
// mirror raised against a lodgement the central bank was never asked to make. It
// is returned as an error naming both halves, exactly as submit's is, rather than
// swallowed.
//
// The remedy is NOT to ask again, which is the difference from returnPayment's
// seam. payment.LodgeReservesTx keys its posting on the message id, and a retry
// through this method takes a NEW id from Mesh.nextMsgID — so asking again lodges
// a second time rather than completing the first. payment/recon is the instrument
// for a break that never became a message.
func (b *bank) lodge(ctx context.Context, asset ledger.AssetCode, amount ledger.Amount) (payment.LodgementInstruction, error) {
	// Everything below is this bank's work, and is recorded as this bank's. See
	// withActor.
	ctx = withActor(ctx, b.bic)

	to := b.m.cfg.CentralBankBIC
	in, env, err := b.ops.LodgeReserves(ctx, asset, amount, payment.MessageContext{
		From:  b.bic,
		To:    to,
		MsgID: b.m.nextMsgID(b.bic),
		Now:   b.m.now(),
	})
	if err != nil {
		return payment.LodgementInstruction{}, err
	}
	if err := b.m.send(b.bic, to, env); err != nil {
		return in, fmt.Errorf("mesh: %s posted its lodgement %s and could not send it: %w", b.bic, in.Ref, err)
	}
	return in, nil
}

// receiveLodgementReceipt is the lodging bank being told what became of its
// request.
//
// There is nothing to post. This bank's leg committed before the camt.050 was
// sent, and the receipt carries no amount to post from even if there were — so
// what arrives is a CONFIRMATION, and the shape of this handler follows from
// that: it is receiveAdmissionRejection's, not receiveStatus's.
//
// # An accepted receipt is logged and nothing else
//
// The reserve mirror this bank raised is now matched by the central bank's own
// entry, which is exactly what this bank already assumed. Nothing in the store
// records the difference between "assumed" and "confirmed", because this system
// keeps no lodgement row — the durable trace is the idempotency key on each
// institution's own posting. Logging it is what makes the confirmation visible at
// all; see receiveAdmissionRejection, which logs for the same reason.
//
// # A REFUSED receipt is the interval nothing closes
//
// It means this bank's Reserve at Central Bank says more than the central bank's
// book does, and will go on saying it. That is stated here rather than handled,
// and the honest reason is that handling it needs something 18a does not have:
// the amount to reverse is not on the receipt, so this handler cannot unwind the
// leg without a lodgement row to read it from.
//
// What makes it unreachable rather than merely unhandled is the guard on the
// asking side. payment.LodgeReservesTx refuses a bank that cannot name its own
// settlement account, and nothing writes that reference before the central bank's
// account exists — so a bank able to ask is one the agent holds an account for,
// and the refusals ReceiveLodgementTx can make are refusals a lodgement cannot
// provoke. It is logged at ERROR for that reason: reaching it means the guard's
// premise is false, which is a defect in this system rather than news about the
// request.
//
// It is not a dead letter either way. A dead letter is for what nobody could be
// told, and this bank has been told.
func (b *bank) receiveLodgementReceipt(from iso20022.BIC, doc *iso20022.Camt025) error {
	r, err := payment.ReadLodgementReceipt(doc)
	if err != nil {
		return fmt.Errorf("mesh: %s could not read the lodgement receipt %s sent it: %w", b.bic, from, err)
	}
	if r.Accepted() {
		b.m.log.Info("mesh: lodgement accepted",
			"bank", b.bic, "from", from, "lodgement", r.Ref)
		return nil
	}
	b.m.log.Error("mesh: lodgement refused, and this bank's reserve mirror is now overstated",
		"bank", b.bic, "from", from, "lodgement", r.Ref, "reason", r.Reason,
		"note", "LodgeReservesTx's guard is meant to make this unreachable; see bank.receiveLodgementReceipt")
	return nil
}

// rejectionText is what the reversal is described as in the payer's bank's own
// ledger: the code, and the free text beside it when there is one. See
// payment.CodeAndText, and payment.TransactionStatusReport for why both are
// carried.
func rejectionText(r payment.TransactionStatusReport) string {
	return payment.CodeAndText(string(r.Code), r.Text, "rejected")
}
