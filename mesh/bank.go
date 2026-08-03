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
// # A fourth role, played after the payment is final
//
// A RETURN is asked for by the bank that received the original instruction —
// the payee's bank on a push, the payer's bank on a pull — and that is the one
// role in this type which is neither submitting nor answering. It comes in from
// outside the mesh, like a submission, but about a payment that has already
// settled, and its half moves nothing at all: the three compensating postings
// are the settlement agent's, because reserves move. So this bank builds a
// pacs.004, sends it to the clearing house, and waits (returnPayment,
// receiveReturnStatus).
//
// # A fifth role, and the only one that answers nothing
//
// At a cut-off a member is TOLD what its reserve account did, in a camt.053 from
// the settlement agent, and books its own mirror leg from it
// (receiveStatement). It is the one inbound message in this type that produces
// no pacs.002, because a statement is not an instruction: the settlement has
// already happened and there is nothing to accept or refuse. It is also the only
// role in which this bank posts without any customer of its own being involved —
// what moves is its own position at the central bank.
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
// A message type this bank has no handler for is an ERROR and not a shrug, and
// after Task 13 the pacs.004 is the one that stays that way. A bank in this
// system SENDS a return and is never sent one: the message goes to the
// settlement agent, which posts all three of a return's legs — including the
// refund into the payer's bank's own book — in one unit of work. In a real
// network the debtor's bank receives the pacs.004 and credits its customer
// itself, and this handler would have an arm for it. Here it does not, so one
// arriving is a bug in whoever sent it, and swallowing it would make a half
// this system does not have look like one it does. See the return flow in the
// package doc.
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
// It is submit's counterpart for a payment that is already final, and the
// difference between them is the whole of what a return is. A submission runs
// this bank's own half and MOVES MONEY on a push; this one posts nothing at
// all, in either direction. The three compensating transactions — the payer
// refunded, the payee clawed back, the reserves reversed — are one unit of work
// at the SETTLEMENT AGENT, because the middle of them moves central-bank money
// and no member bank may do that. What this bank does is state the reason, and
// the message is the whole of its half.
//
// # The guard, and why it is here rather than on the wire
//
// A payment that is not Settled cannot be returned — ReturnPaymentTx says so,
// with ErrInvalidStateTransition — and this bank refuses it BEFORE the message
// exists. That is not defensiveness about a check the settlement agent makes
// anyway; it is the only way the caller is ever told. That sentinel is
// classified in payment's reasonTable with the empty code because it describes
// a defect in this system rather than a judgement about anyone's instruction,
// so an actor that got one must dead-letter it — which means a return sent for
// an unsettled payment would be answered by NOBODY, and the operator who asked
// for it would hear nothing at all. Refusals that CAN be answered are left to
// travel; see centralBank.receiveReturn for the ones that do.
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
	return b.m.send(b.bic, to, env)
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
		return b.receiveReturnStatus(doc)
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

// receiveReturnStatus is the bank that asked for a return learning what the
// settlement agent did with it.
//
// Neither outcome gives this bank anything to post, and the reason is the same
// one that sent the pacs.004 to the settlement agent in the first place. A
// return that WENT THROUGH was three postings in one unit of work — the payer
// refunded, the payee clawed back, the reserves reversed — and two of those
// three landed in member banks' books, including this one's. There is no second
// write left for the bank that asked. What the message buys is that it KNOWS,
// which before the mesh it could learn only from the return value of the call
// that did the returning.
//
// A REFUSED return is logged and nothing else, for csm.receiveSettlementStatus's
// reason: nothing was posted anywhere, so the payment is exactly where it was —
// Settled, the payee still holding the money — and there is no state here to put
// back. The code is logged because the code is the one thing that arrives on the
// wire and is nowhere in the store; the payment's own row is where the failure
// shows, by still saying Settled.
//
// It is emphatically NOT a dead letter. A dead letter is for what nobody could
// be told, and this bank was told: it asked, and it has its answer.
func (b *bank) receiveReturnStatus(doc *iso20022.Pacs002) error {
	_, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.Status == iso20022.TransactionStatusRejected {
			b.m.log.Error("mesh: return refused",
				"bank", b.bic, "payment", r.TxID, "code", r.Code, "reason", r.Text)
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
		return fmt.Errorf("mesh: %s could not book the settlement of %s: %w", b.bic, moves[0].CycleID, err)
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
