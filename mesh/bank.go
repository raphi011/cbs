package mesh

import (
	"context"
	"errors"
	"fmt"

	"github.com/raphi011/cbs/iso20022"
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
// banks in the same code:
//
//   - the PAYER's bank submits (submit), which is the only half that moves money
//     at initiation;
//   - the PAYEE's bank receives the pacs.008 (receiveCreditTransfer) and answers
//     yes or no;
//   - the PAYER's bank receives the answer (receiveStatus), and gives the money
//     back if it was no.
//
// Which one is running is decided entirely by which actor's inbox the message
// landed in. That is what makes "who may know what, and when" a question with an
// answer here and not in the single-process version.
//
// # The same three roles, played by the other bank
//
// A direct debit is the same three, with the banks swapped and the money moving
// at a different moment:
//
//   - the PAYEE's bank submits, because a collection is the payee asking for
//     what it is owed — and its submission moves NOTHING. It cannot: the account
//     being collected from is at the other bank and this one has never seen it.
//   - the PAYER's bank receives the pacs.003 (receiveDirectDebit). This is the
//     half that moves money, and it is the only moment either the account or the
//     funds behind it are in view. AM04 comes from here and could come from
//     nowhere else.
//   - the PAYEE's bank receives the answer, and has nothing to undo, because it
//     never had the money.
//
// So "the submitting bank posts" is a fact about a PUSH and not about this
// system. The rule that covers both is that the DEBTOR's bank posts the debtor
// leg, and the direction decides whether that is the bank submitting or the bank
// answering. payment.SubmitPaymentTx and payment.AcceptInboundTx are the two
// halves that say so.
//
// # A fourth and fifth role, played after the payment is final
//
// A RETURN is asked for by the bank that received the original instruction —
// the payee's bank on a push, the payer's bank on a pull — and that is the one
// role in this type which is neither submitting nor answering. It comes in from
// outside the mesh, like a submission, but about a payment that has already
// settled. Its half MOVES MONEY: this bank posts its own customer leg — the
// clawback if it is the creditor's bank, the refund if it is the payer's —
// BEFORE it composes the pacs.004, and that ordering is the whole of why a
// refusal binds. See returnPayment and payment.PostReturnLegTx.
//
// This paragraph used to say the opposite: that a returning bank's half moved
// nothing at all, because every posting a return made was the settlement
// agent's. Half of that is still exactly right — the reserve reversal is
// central-bank money and no member bank may move it — and the half that was
// wrong is that a return's CUSTOMER legs were ever the settlement agent's to
// make. Each belongs to the bank whose customer it moves, and Task 16e gave
// each bank its own.
//
// The OTHER bank plays the fifth role and it is the counterpart of the fourth:
// it is sent the pacs.004 after finality (receiveReturn) and posts the leg the
// returning bank does not hold. It cannot refuse, because there is nothing left
// to refuse — the reserves have already moved. So the two roles are one rule: a
// bank can refuse a leg only if it posts it before it sends.
//
// A REFUSED return brings the fourth role back for one more act. This bank has
// already posted, so an RJCT is not news with nothing behind it: the leg is
// unwound (receiveReturnStatus, payment.ReverseReturnLegTx).
//
// # A sixth role, and the second that answers nothing
//
// At a cut-off — and on a return, which is a cut-off of one payment as far as
// the reserves are concerned — a member is TOLD what its reserve account did, in
// a camt.053 from the settlement agent, and books its own mirror leg from it
// (receiveStatement). It produces no pacs.002, because a statement is not an
// instruction: the settlement has already happened and there is nothing to
// accept or refuse. It used to be the ONE inbound message here with that
// property; receiveReturn is the second, for a related reason — see its own
// doc. It is still the only role in which this bank posts without any customer
// of its own being involved: what moves is its own position at the central bank.
type bank struct {
	m   *Mesh
	ops bankOps

	// bic is who this actor is, on the wire: what a counterparty addresses it by
	// and what it signs its own messages From. The mesh's index of banks is keyed
	// by ParticipantID instead, because that is what an instruction names; see
	// Mesh.banks.
	bic iso20022.BIC

	// pid is which participant this actor IS, which is the question a settlement
	// advice asks: a statement is about one bank's reserve account and a payment
	// advice is about one bank's customer, and the answer must be this actor's
	// own identity rather than a lookup.
	//
	// This is NOT the bank identity Task 18 owes payment.Network. That one is
	// about narrowing ResolveIdentifierTx's sweep to "this bank's register", and
	// it needs the DOMAIN layer to know whose register is whose. This is the
	// mesh's own index turned around: Mesh.banks is already keyed by
	// ParticipantID, so the actor is being told something the mesh knew when it
	// built it. Nothing here narrows a sweep.
	pid payment.ParticipantID
}

// handle dispatches on the message that arrived.
//
// The unmarshalling comes first and its failure is answerable, which is the
// whole reason this takes the sender as an argument rather than reading it out
// of the header: the header is exactly what is unreadable.
//
// A message type this bank has no handler for is an ERROR and not a shrug. The
// pacs.004 used to be one of them: a bank in this system SENT a return and was
// never sent one, because the settlement agent made every one of a return's
// postings in one unit of work, including the refund into the payer's bank's own
// book.
//
// It has an arm now, and that arm is the substance of Task 16e rather than a
// completeness exercise. In a real network the pacs.004 travels to the bank that
// did not ask for the return, and that bank moves its own customer's money
// itself; here it does too. Which of the two banks is sent one follows from the
// direction — the payer's bank on a push, the payee's bank on a pull — and
// neither this handler nor the clearing house decides it: the domain refuses a
// bank that is not that leg's owner (payment.ErrNotAPartyToThisReturn). See
// receiveReturn, and the return flow in the package doc.
//
// What is left for the default is the pacs.009: a settlement instruction is
// addressed to the settlement agent, names net positions between members and the
// central bank, and is the one message definition this system emits that a
// member bank has nothing to do with. Nobody sends one here — the clearing house
// addresses them to the central bank — which is why
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
// It is submit's counterpart for a payment that is already final, and the two
// now have the same shape: this bank's own half posts, and then the message
// hands the rest of the work to another institution.
//
// # It posts BEFORE it sends, and that is the return's one rule
//
// The paragraph this replaces said the opposite — "this one posts nothing at
// all, in either direction" — and gave a reason that was half right: the
// reserve reversal moves central-bank money and no member bank may make it. It
// does not follow, and it used to be treated as though it did, that a return's
// CUSTOMER legs are the settlement agent's. This bank posts the leg it owns:
// the clawback if it is the creditor's bank, the refund if it is the payer's.
// payment.PostReturnLegTx decides which, from the payment rather than from
// anything this handler passes it.
//
// The ordering is what makes a refusal BIND. On a push this bank is the payee's
// bank and holds the clawback, so a payee who has already spent the money stops
// the return dead: nothing is posted, no pacs.004 is composed, and the caller is
// told — deposit.ErrInsufficientAvailable, which ReasonFor maps to AM04, about
// this bank's OWN customer. A check that was not a posting could be outrun by
// that customer between the check and the send, and after finality there is
// nothing left to refuse. On a pull this bank is the payer's and holds the
// refund, which is unconditional, so there is nothing here to refuse and the
// forced leg is the other bank's. See payment.PostReturnLegTx, which states the
// rule, and TestAPayeeWhoSpentTheMoneyStopsTheReturnBeforeItIsSent.
//
// # The message is built first, and the seam is between the posting and the send
//
// Building it can fail — an amount whose asset has no scale, a payment carrying
// no agent to name — and a build that failed AFTER the leg was posted would
// leave this bank's customer moved for a return that never left the building.
// So the message is composed first, when nothing has happened yet, and the
// reason the leg is described by is read back off it: both banks then derive
// that description from the same bytes rather than from two renderings of one
// intent.
//
// What remains is a posting that COMMITTED and a send that failed, which leaves
// this bank's money in its clearing suspense against a return nobody will
// answer. That is the seam submit documents, and it is returned as an error
// rather than swallowed, naming both halves so the operator can see which one
// happened. It does NOT come back with the payment beside it, as submit's does:
// nothing above this reads one — Mesh.Return answers with an error alone and
// api's handler with 202 — and a value nothing reads is the defect this
// repository keeps finding. The half-happened state is visible where it
// actually is, on the payment row, which carries this bank's leg id with the
// status still Settled.
//
// # The guard, and why it is here rather than on the wire
//
// A payment that is not Settled cannot be returned — PostReturnLegTx says so,
// with ErrInvalidStateTransition — and this bank refuses it BEFORE the message
// exists. The guard is repeated here anyway, and not out of defensiveness: it
// is the only place the caller is told WHAT the payment is instead of merely
// that the transition was illegal, and the status is the whole of what an
// operator needs to know. That sentinel is classified in payment's reasonTable
// with the empty code because it describes a defect in this system rather than
// a judgement about anyone's instruction, so an actor that got one must
// dead-letter it — which means a return sent for an unsettled payment would be
// answered by NOBODY, and the operator who asked for it would hear nothing at
// all. Refusals that CAN be answered are left to travel; see
// centralBank.receiveReturn for the ones that do.
//
// The payment is read here rather than taken from Mesh.Return, which already
// holds one. The read is what makes the judgement this bank's own: the router
// looked the payment up to decide whose instruction this is, and a bank that
// refused on somebody else's snapshot would be refusing on hearsay. It costs no
// book — a payment is a network-scoped row.
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
	if _, err := b.ops.PostReturnLeg(ctx, b.pid, p.ID, returnReasonOf(env)); err != nil {
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
// any standing to look up — BY ADDRESS. The SWEEP that address is checked
// against is not this bank's own register: ResolveIdentifierTx still lists
// every participant and reads every register, exactly as it did before this
// narrowing (see payment.localPartyIn), so AC01 fires only when nobody in the
// WHOLE NETWORK holds the creditor's IBAN — a creditor address some other bank
// happens to hold still resolves. What changed is which PARTY is put through
// that sweep, not which registers the sweep reaches; narrowing the sweep
// itself needs the bank's own identity, which is a later sub-project's, not
// this one's. It is the question a real receiving bank asks first, because
// until it is answered the bank does not know the message is even for one of
// its customers. It no longer sweeps the directory for the DEBTOR too — see
// payment.CreditTransferRequest and localPartyIn — so an unaddressable or
// unknown debtor IBAN, which names a customer at the SENDING bank and nothing
// this bank could ever confirm, is not refused here.
//
// Second: does this bank's own half check out? That is AcceptInbound: the payee's
// account exists, is in the scheme's asset, is addressable, and can take a
// credit.
//
// The request the first question produces is deliberately discarded. In a real
// network it would BE the payment — the receiving bank has no record of one until
// this message arrives, so it would create its own from what the message says.
// Here both banks read one payment row out of one store, so the second half loads
// it by the identifier the message carries (PmtId/TxId) and there is nothing for
// the request to become. That gap is not a defect in this handler; it is the
// shared store, and closing it is sub-project 8's whole subject. The narrowing
// above sharpens the loss rather than closing it: what is discarded is now only
// this bank's own resolved half plus the debtor's asserted details, never a
// resolution of the debtor's account this bank had no business making.
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

	if _, err := b.ops.CreditTransferRequest(ctx, doc); err != nil {
		return b.answer(from, orig, ref, err)
	}
	return b.accept(ctx, from, orig, ref)
}

// receiveDirectDebit is the PAYER's bank answering a collection, and it is the
// mirror of receiveCreditTransfer in every way except the one that matters.
//
// The two questions are the same two, in the same order. First, can this message
// be resolved to an instruction at all — DirectDebitRequest, which resolves the
// DEBTOR — this bank's own customer, the party a pacs.003 routed here by
// DbtrAgt gives this bank standing over — BY ADDRESS. As on the push side, the
// sweep that address is checked against is network-wide and not narrowed to
// this bank's own register — see receiveCreditTransfer's doc for the whole of
// that point — so AC01 fires only when nobody in the WHOLE NETWORK holds the
// debtor's IBAN. The CREDITOR is the sending bank's customer and is not
// resolved, for the same reason receiveCreditTransfer's debtor is not — see
// that handler and payment.DirectDebitRequest. Second, does this bank's own
// half check out — AcceptInbound.
//
// What differs is what the second question DOES. On a push it is a check and
// nothing more; here it is the posting. The payer's money leaves their account
// for this bank's clearing suspense at the moment this handler says yes, because
// this is the first moment any actor in the system has been able to look at that
// account at all. So a refusal here is a refusal that no other party could have
// made: AM04 is the payer's balance, and the bank that submitted this collection
// has no way of knowing it.
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

	if _, err := b.ops.DirectDebitRequest(ctx, doc); err != nil {
		return b.answer(from, orig, ref, err)
	}
	return b.accept(ctx, from, orig, ref)
}

// accept runs the receiving bank's own half and answers with the result. It is
// the second of the two questions both receive handlers ask, and it is shared
// because the direction changes what the half DOES and not what this actor does
// about it.
func (b *bank) accept(ctx context.Context, from iso20022.BIC, orig payment.OriginalMessage, ref iso20022.PaymentIdentification) error {
	if err := b.ops.AcceptInbound(ctx, payment.PaymentID(ref.TxId)); err != nil {
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
// An ACCEPTANCE needs nothing from it, and that is not an omission: an ACCP is
// the clearing house saying it has taken the payment into a cycle, which is the
// clearing house's own act and which the clearing house records. No money has
// moved yet, so there is no second write for this bank to make. What the message
// buys is that the bank KNOWS — which, before the mesh, it could only learn by
// reading the return value of the call that did the accepting.
//
// A SETTLEMENT COMPLETION is a different status about a different moment, and it
// is where the payee is finally paid. The reserves have moved at the central
// bank; the creditor's bank now releases the money out of its own clearing
// suspense into its own customer's account, which is a posting only that bank
// can make in only that bank's book. See payment.PostCreditorLegTx.
//
// Both banks are told, and only one of them has that leg. On a push the
// clearing house sends the same ACSC to the payer's bank, which is waiting for
// the answer to the instruction it sent and has nothing to post; the domain is
// what tells the two apart, and payment.ErrNotThisBanksPayment coming back is
// the ORDINARY case for one of the two recipients rather than a failure. On a
// pull there is one recipient, because the bank that submitted the collection
// is the creditor's bank. See csm.tellSettled.
//
// A REJECTION is where there may be work, and whether there is depends on which
// bank this is:
//
//   - The PAYER's bank reverses the debit that put its customer's money into its
//     clearing suspense. That is the only work a rejection ever creates, and only
//     this bank can do it — ReverseDebtorLegTx posts in the payment's own debtor
//     bank's book, whoever calls it.
//   - The bank that SUBMITTED, when that is somebody else, is being told the
//     answer to its instruction and has nothing to undo. On a pull that is the
//     payee's bank, which never held the money: its submission posted nothing,
//     which is exactly what makes a collection a collection.
//
// For a push those are one bank and one message. For a pull they are two banks,
// and the clearing house sends to both; see csm.receiveStatus for why the second
// message exists and when it does not.
//
// Anything else is REFUSED. A bank that is neither the payer's bank nor the
// submitter has been sent a decision about a payment that is none of its
// business, and acting on one would mean reaching into another bank's ledger.
// Nothing in the flow produces it, because the clearing house addresses a status
// to those banks and no other; this is what makes the property the receiver's as
// well as the router's.
//
// A status naming no transaction at all is skipped rather than refused. That is
// the FF01 a clearing house sends when it could not parse a file: it names no
// payment because it could not read one, so there is nothing here to act on. The
// sender's operator sees it in the message; this bank's books are not involved.
//
// # A status about a RETURN is a different message about a different thing
//
// The answer to a pacs.004 arrives here too, and everything above is wrong
// about it: a rejected return is not a rejected payment, the payment it names
// is still Settled rather than Rejected, and the bank that asked for it is
// neither the payer's bank nor the submitter on a push. Read as a rejection it
// would be refused by this handler's own guards — correctly, since reversing a
// debtor leg is exactly what must not happen — and the bank that asked would
// learn nothing. So it is told apart by what the status says it is ABOUT, which
// is the element that exists for it: see returnMsgDef, and receiveReturnStatus
// for what happens instead.
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
			// ACSC. This bank posts its creditor leg if the payee is its
			// customer, and does nothing at all if it is not — which on a push
			// is the payer's bank, hearing the answer to the instruction it
			// sent. The domain decides which this bank is;
			// ErrNotThisBanksPayment is the ordinary case for one of the two
			// recipients and is not a failure.
			if _, err := b.ops.PostCreditorLeg(ctx, b.pid, payment.PaymentID(r.TxID)); err != nil {
				if errors.Is(err, payment.ErrNotThisBanksPayment) {
					continue
				}
				return fmt.Errorf("mesh: %s could not pay its customer for %s: %w", b.bic, r.TxID, err)
			}
			continue
		}
		if r.Status != iso20022.TransactionStatusRejected {
			continue
		}
		p, err := b.ops.GetPayment(ctx, payment.PaymentID(r.TxID))
		if err != nil {
			return fmt.Errorf("mesh: %s was told %s was rejected: %w", b.bic, r.TxID, err)
		}
		scheme, ok := b.ops.Scheme(p.Scheme)
		if !ok {
			return fmt.Errorf("mesh: %s was told %s was rejected and holds no %q scheme: %w",
				b.bic, p.ID, p.Scheme, payment.ErrSchemeNotFound)
		}
		// Whose payer is this? A bank that acted on a misrouted rejection would
		// reverse a debit in somebody else's ledger, so the answer decides
		// everything below.
		debtor, err := b.ops.GetParticipant(ctx, p.Debtor.Participant)
		if err != nil {
			return fmt.Errorf("mesh: %s cannot tell whose payment %s is: %w", b.bic, p.ID, err)
		}
		if debtor.BIC != b.bic {
			// Not the payer's bank. The only other party with any business
			// receiving this is the one that submitted and is waiting for an
			// answer, and it has nothing to give back.
			submitter, err := b.ops.GetParticipant(ctx, submitterOf(scheme, p.Debtor, p.Creditor).Participant)
			if err != nil {
				return fmt.Errorf("mesh: %s cannot tell who submitted %s: %w", b.bic, p.ID, err)
			}
			if submitter.BIC != b.bic {
				return fmt.Errorf("mesh: %s was sent a rejection of %s, whose payer banks at %s and which %s submitted",
					b.bic, p.ID, debtor.BIC, submitter.BIC)
			}
			continue
		}
		// And is it really rejected? A pacs.002 is not on its own a decision:
		// this network's record of the payment is. Reversing on the message
		// alone would take a live debit back off a payment on its way to
		// settlement, and the money would simply be gone from the flow: this
		// suspense is the PAYER's bank's, so the debit is what funds that bank's
		// own mirror leg when the cut-off settles. (This comment used to say the
		// payee is paid out of this suspense. The payee is paid out of the
		// PAYEE's bank's suspense, by that bank, after settlement — a different
		// account in a different book. The mistake made the consequence sound
		// smaller than it is.)
		if p.Status != payment.Rejected {
			return fmt.Errorf("mesh: %s was told to reverse %s, which this network records as %v", b.bic, p.ID, p.Status)
		}
		if err := b.ops.ReverseDebtorLeg(ctx, p, rejectionText(r)); err != nil {
			return fmt.Errorf("mesh: %s could not give the payer of %s their money back: %w", b.bic, p.ID, err)
		}
	}
	return nil
}

// receiveReturn is the OTHER bank's leg of a return: the one the bank that
// asked does not hold, posted from the pacs.004 the clearing house relayed.
//
// # It answers nothing, and the reason is receiveStatement's
//
// Like a statement, this is not an instruction. By the time it arrives the
// reserves have already moved and the return is final — that is exactly why the
// clearing house held it back until it had an ACSC, see csm.relayReturn — so a
// bank answering "no" would be refusing something that has happened. There is no
// pacs.002 in this arm and its absence is the message definition rather than an
// omission. Said explicitly, because "no answer" and "an answer nobody wrote
// yet" look the same from outside.
//
// What a failure produces instead is an ERROR, which this transport turns into a
// dead letter. That is the whole of the price of not answering, and it is a real
// one: the return is settled at the central bank and this bank's customer leg
// is missing, which leaves the payment Settled for ever with one leg standing in
// the other bank's book. Nothing in this flow can put that back — the reserves
// are final — so it is a case for Task 19's reconciliation and not for a
// message. See centralBank.advise, which records the same shape one hop up.
//
// # Which leg, and whose business it is
//
// Neither this handler nor the clearing house decides. payment.PostReturnLegTx
// takes the acting participant and works out from the PAYMENT which of the two
// legs that bank owns — the clawback at the creditor's bank, the refund at the
// payer's — and refuses a bank that is neither with
// payment.ErrNotAPartyToThisReturn. This bank passes its own id (see bank.pid),
// so a misrouted pacs.004 becomes a dead letter rather than a posting in
// somebody else's return.
//
// It cannot refuse the leg on its merits, and that is the other half of the
// return's one rule: the returning bank posted before it sent and could say no;
// this bank is posting after finality and cannot. On a pull that is the
// creditor's bank forcing a clawback against a biller who may be gone, with the
// shortfall landing in its own Returns Receivable.
//
// # It reads the message the same way the settlement agent does
//
// payment.ReadReturn, which is also what centralBank.receiveReturn calls. This
// bank HAS the payment row today and could read the reason off it instead, but
// the row is the shared store showing through and the message is what will be
// left when it is gone.
//
// The loop is over what the reader returns, and that is written for what the
// reader can PRODUCE rather than for what this flow sends. ReadReturn holds a
// sender to its own count and refuses a transaction it cannot resolve, but it
// does not refuse a bulk message — a pacs.004 is shaped for several returns per
// file. This system's does not send one, and one could not reach here anyway:
// the settlement agent refuses a bulk return WHOLE before any ACSC exists, and
// the clearing house relays nothing until it has one.
func (b *bank) receiveReturn(ctx context.Context, from iso20022.BIC, doc *iso20022.Pacs004) error {
	ins, err := payment.ReadReturn(doc)
	if err != nil {
		return fmt.Errorf("mesh: %s could not read the return %s sent it: %w", b.bic, from, err)
	}
	for _, in := range ins {
		if _, err := b.ops.PostReturnLeg(ctx, b.pid, in.PaymentID, in.Reason); err != nil {
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
// That is the reverse of what this doc used to say, and the reversal is the
// whole of Task 16e in one handler. It used to read: neither outcome gives this
// bank anything to post, because a return that went through was made entirely at
// the settlement agent — this bank's own customer leg among them — and a refused
// return had posted nothing anywhere, so the payment was exactly where it was.
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
		if r.Status != iso20022.TransactionStatusRejected || r.TxID == "" {
			// A status naming no transaction is skipped rather than refused, for
			// receiveStatus's reason: it is the FF01 a counterparty sends when it
			// could not parse a file, and there is no leg it could be about.
			continue
		}
		b.m.log.Error("mesh: return refused",
			"bank", b.bic, "payment", r.TxID, "code", r.Code, "reason", r.Text)
		if err := b.ops.ReverseReturnLeg(ctx, b.pid, payment.PaymentID(r.TxID), rejectionText(r)); err != nil {
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
// is not an instruction and there is nothing to accept or refuse. The central
// bank has already settled — finality is the premise of this whole conversation —
// so a bank that answered "no" would be refusing something that has happened.
//
// What a failure produces instead is an ERROR, which this transport turns into a
// dead letter, and NO advice row at all: payment.PostSettlementAdvice is one unit
// of work, so a mirror leg that fails takes the row with it. The dead letter is
// the only trace in this PROCESS; in the STORE the unreconciled position is a
// clearing suspense that has not returned to zero with no advice row against the
// cycle. Task 19 is the reconciliation that makes that visible from inside the
// system rather than only in Drain.
//
// # One statement, one member
//
// This system's central bank sends a member a statement about that member's own
// reserve account and about nothing else, so a document carrying several is one
// this handler has no rule for. It is refused whole rather than partially
// booked, for the reason cycleOf gives: booking the first and dropping the rest
// would move a reserve mirror by the wrong amount with nothing recording it.
//
// The check that the account is THIS bank's is the domain's, not this handler's
// — see payment.PostSettlementAdviceTx and ErrStatementNotForThisBank — because
// it is a question about this bank's chart of accounts and the handler holds no
// chart.
//
// # It does not check WHO sent it, and that is deliberate
//
// `from` is used in the error messages and in no decision. What is checked is
// OWNERSHIP: the account the statement names must be this bank's own reserve
// account at the central bank, and that is the domain's question because the
// domain is what holds the chart of accounts. Repeating it here as a sender
// check would be a second answer to a question already answered, by the layer
// with less to answer it with.
//
// What ownership buys is NOT "only the settlement agent may advise me", and the
// difference is worth stating rather than leaving to be inferred from an
// absence. That is a strictly stronger guarantee and this system does not make
// it: any actor that named this bank's own settlement account would be booked
// here, because the row it produces is indistinguishable from a real one. What
// it does buy is that nobody can move this bank's mirror by advising it about
// somebody else's position, which is the failure that would actually cost money.
// Nothing in this mesh sends a camt.053 but the settlement agent; and under one
// shared store the two properties are not separable anyway, since every account
// id is in one table. Sub-project 8 is where a sender becomes something a
// receiver could meaningfully insist on.
func (b *bank) receiveStatement(ctx context.Context, from iso20022.BIC, doc *iso20022.Camt053) error {
	moves, err := payment.ReadStatement(doc)
	if err != nil {
		return fmt.Errorf("mesh: %s could not read the statement from %s: %w", b.bic, from, err)
	}
	if len(moves) != 1 {
		return fmt.Errorf("mesh: %s got a statement from %s carrying %d accounts; a member is told about its own",
			b.bic, from, len(moves))
	}
	if _, err := b.ops.PostSettlementAdvice(ctx, b.pid, moves[0]); err != nil {
		return fmt.Errorf("mesh: %s could not book the settlement of %s: %w", b.bic, moves[0].Reference, err)
	}
	return nil
}

// rejectionText is what the reversal is described as in the payer's bank's own
// ledger: the code, and the free text beside it when there is one. See
// payment.CodeAndText, and payment.TransactionStatusReport for why both are
// carried.
func rejectionText(r payment.TransactionStatusReport) string {
	return payment.CodeAndText(string(r.Code), r.Text, "rejected")
}
