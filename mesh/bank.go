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
type bank struct {
	m   *Mesh
	ops bankOps

	// bic is who this actor is, on the wire: what a counterparty addresses it by
	// and what it signs its own messages From. The mesh's index of banks is keyed
	// by ParticipantID instead, because that is what an instruction names; see
	// Mesh.banks.
	bic iso20022.BIC
}

// handle dispatches on the message that arrived.
//
// The unmarshalling comes first and its failure is answerable, which is the
// whole reason this takes the sender as an argument rather than reading it out
// of the header: the header is exactly what is unreadable.
//
// A message type this bank has no handler for is an ERROR and not a shrug. Tasks
// 11 and 13 add the two arms that are missing (pacs.003 collections and pacs.004
// returns), and until they do, a bank that answered one with silence would make
// the missing half look like a working one.
func (b *bank) handle(ctx context.Context, from iso20022.BIC, raw []byte) error {
	env, err := iso20022.Unmarshal(raw)
	if err != nil {
		return b.m.answerUnreadable(b.bic, from, err)
	}
	switch doc := env.Document.(type) {
	case *iso20022.Pacs008:
		return b.receiveCreditTransfer(ctx, from, env.AppHdr, doc)
	case *iso20022.Pacs002:
		return b.receiveStatus(ctx, doc)
	default:
		return fmt.Errorf("mesh: %s has no handler for %s", b.bic, env.AppHdr.MsgDefIdr)
	}
}

// submit is the payer's own bank taking its customer's instruction: the
// submitting half, then the pacs.008 that hands it to the clearing house.
//
// Two steps and two failure modes, and they are not the same. A refused
// instruction moved nothing and is the caller's answer. A message that could not
// be built or sent leaves a payment Initiated that nobody will ever answer —
// half-happened, the same seam RejectAtCSMTx documents — so it is returned as an
// error with the payment beside it rather than swallowed.
func (b *bank) submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	// Everything below is this bank's work, and is recorded as this bank's. See
	// withActor.
	ctx = withActor(ctx, b.bic)

	p, err := b.ops.SubmitPayment(ctx, req)
	if err != nil {
		return payment.Payment{}, err
	}

	to := b.m.cfg.ClearingHouseBIC
	env, err := b.ops.CreditTransferMessage(ctx, p, payment.MessageContext{
		From:  b.bic,
		To:    to,
		MsgID: b.m.nextMsgID(b.bic),
		Now:   b.m.now(),
	})
	if err != nil {
		return p, fmt.Errorf("mesh: %s submitted %s and could not build its pacs.008: %w", b.bic, p.ID, err)
	}
	if err := b.m.send(b.bic, to, env); err != nil {
		return p, fmt.Errorf("mesh: %s submitted %s and could not send it: %w", b.bic, p.ID, err)
	}
	return p, nil
}

// receiveCreditTransfer is the PAYEE's bank answering a credit transfer.
//
// Two questions, asked in this order, and the order is what decides the code the
// sender gets back.
//
// First: can this message be resolved to an instruction at all? That is
// CreditTransferRequest, which resolves both parties BY ADDRESS — a sweep of the
// network's directory for whoever holds the IBAN — and it is the check that
// produces AC01 for an account number nobody holds. It is the question a real
// receiving bank asks first, because until it is answered the bank does not know
// the message is even for one of its customers.
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
// shared store, and closing it is sub-project 8's whole subject.
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
		return b.answerCreditTransfer(from, orig, ref, err)
	}
	if err := b.ops.AcceptInbound(ctx, payment.PaymentID(ref.TxId)); err != nil {
		// Already answered. A queue redelivers, so the same pacs.008 can arrive
		// twice — and the second time the payment is no longer Initiated, which
		// is what this sentinel says. It must NOT become a rejection: payment's
		// reasonTable classifies it with the empty code precisely because it
		// describes a defect in this system rather than a judgement about the
		// sender's instruction, and ReasonFor would turn it into MS03 and reject,
		// on the wire, a payment this bank in fact accepted. A dead letter is the
		// right channel: nobody to answer, and visible in Drain.
		if errors.Is(err, payment.ErrInvalidStateTransition) {
			return fmt.Errorf("mesh: %s was sent %s again and it is no longer Initiated: %w", b.bic, ref.TxId, err)
		}
		return b.answerCreditTransfer(from, orig, ref, err)
	}
	return b.answerCreditTransfer(from, orig, ref, nil)
}

// answerCreditTransfer sends the pacs.002 back to whoever handed the message
// over: accepted if cause is nil, rejected with the code cause maps to if not.
//
// Back to the SENDER and not to the payer's bank, because those are different
// parties and this bank was never given the payer's bank's address to answer at.
// The clearing house relays; see csm.receiveStatus.
func (b *bank) answerCreditTransfer(to iso20022.BIC, orig payment.OriginalMessage, ref iso20022.PaymentIdentification, cause error) error {
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

// receiveStatus is the payer's bank learning what became of its instruction.
//
// An ACCEPTANCE needs nothing from it. That is not an omission: the payment's
// acceptance is the clearing house's act and the clearing house records it, so
// there is no second write for this bank to make. What the message buys is that
// the bank KNOWS — which, before the mesh, it could only learn by reading the
// return value of the call that did the accepting.
//
// A REJECTION is where this bank has work: it reverses the debit that put the
// payer's money into its clearing suspense. Two things are checked first, and
// both are the caller-side decision ReverseDebtorLegTx says its caller must
// make, because that function looks at neither.
//
// A status naming no transaction at all is skipped rather than refused. That is
// the FF01 a clearing house sends when it could not parse a file: it names no
// payment because it could not read one, so there is nothing here to act on. The
// sender's operator sees it in the message; this bank's books are not involved.
func (b *bank) receiveStatus(ctx context.Context, doc *iso20022.Pacs002) error {
	_, reports := payment.ReadStatus(doc)
	for _, r := range reports {
		if r.Status != iso20022.TransactionStatusRejected || r.TxID == "" {
			continue
		}
		p, err := b.ops.GetPayment(ctx, payment.PaymentID(r.TxID))
		if err != nil {
			return fmt.Errorf("mesh: %s was told %s was rejected: %w", b.bic, r.TxID, err)
		}
		// Whose payer is this? ReverseDebtorLegTx posts in the book of the
		// payment's OWN debtor bank, whoever runs it, so a bank that acted on a
		// misrouted rejection would reverse a debit in somebody else's ledger.
		// The clearing house addresses a status to that bank and no other, which
		// makes this unreachable through the flow; it is here so that the
		// property belongs to the receiver as well as to the router.
		debtor, err := b.ops.GetParticipant(ctx, p.Debtor.Participant)
		if err != nil {
			return fmt.Errorf("mesh: %s cannot tell whose payment %s is: %w", b.bic, p.ID, err)
		}
		if debtor.BIC != b.bic {
			return fmt.Errorf("mesh: %s was sent a rejection of %s, whose payer banks at %s", b.bic, p.ID, debtor.BIC)
		}
		// And is it really rejected? A pacs.002 is not on its own a decision:
		// this network's record of the payment is. Reversing on the message
		// alone would take a live debit back off a payment on its way to
		// settlement — the payee is paid at settlement out of this suspense, so
		// the money would simply be gone from the flow.
		if p.Status != payment.Rejected {
			return fmt.Errorf("mesh: %s was told to reverse %s, which this network records as %v", b.bic, p.ID, p.Status)
		}
		if err := b.ops.ReverseDebtorLeg(ctx, p, rejectionText(r)); err != nil {
			return fmt.Errorf("mesh: %s could not give the payer of %s their money back: %w", b.bic, p.ID, err)
		}
	}
	return nil
}

// rejectionText is what the reversal is described as in the payer's bank's own
// ledger: the code, and the free text beside it when there is one.
//
// Both, because they say different things — the code is what makes the reversal
// machine-actionable in a statement or an exception queue, and the text is the
// part no code can say. See payment.TransactionStatusReport.
func rejectionText(r payment.TransactionStatusReport) string {
	switch {
	case r.Code == "" && r.Text == "":
		return "rejected"
	case r.Text == "":
		return string(r.Code)
	case r.Code == "":
		return r.Text
	default:
		return string(r.Code) + ": " + r.Text
	}
}
