package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// What every institution in this package needs and no institution owns: the
// order type a document travels under, the two rules that name a party, and the
// answer to a file that would not parse.

// orderTypeOf is the EBICS order type a document travels under.
//
// The mapping is total over what this deployment emits, and it is a fact about
// the DOCUMENT rather than about the hop: a pacs.008 is a CCT going up from a
// bank and a CCT coming down to the bank the clearing house routed it to. What
// changes between the two is which queue it sits in, and that is the receiving
// subscriber's identity rather than the type.
//
// A document with no type here is one no institution in this deployment builds,
// so the error is a wiring mistake reported at the send rather than a file put
// on a connection under a label the far side has no rule for.
func orderTypeOf(doc iso20022.Document) (ebics.OrderType, error) {
	switch doc.(type) {
	case *iso20022.Pacs008:
		return ebics.CCT, nil
	case *iso20022.Pacs003:
		return ebics.CDD, nil
	case *iso20022.Pacs004:
		return ebics.CRT, nil
	case *iso20022.Pacs002:
		return ebics.CST, nil
	case *iso20022.Pacs009:
		return ebics.CSI, nil
	case *iso20022.Camt050:
		return ebics.CLD, nil
	case *iso20022.Camt053:
		return ebics.C53, nil
	case *iso20022.Camt025:
		return ebics.C25, nil
	default:
		return "", fmt.Errorf("server: no order type carries a %T", doc)
	}
}

// creditTransferPart and directDebitPart are one destination's share of an
// uploaded file: the transactions at the given positions, under the sender's own
// group header.
//
// # What is copied, and the one element that is not
//
// The transactions travel byte for byte, because they are what the submitting
// bank said and are not the clearing house's to rewrite. So does GrpHdr/MsgId,
// which every answer refers back to all the way home. NbOfTxs is recomputed,
// because it is a claim about THIS file and this file is a share of the one that
// arrived — a receiving bank holds a sender to its own count
// (payment's checkNbOfTxs), and the sender of an output file is the clearing
// house.
//
// What that costs is stated rather than left to be discovered: NbOfTxs does not
// span the whole chain, so a clearing house that dropped a transaction while
// sorting would assert a count that matched what it sent. The check still catches
// a submitting bank's truncated upload, which is the hop the element exists for
// and the only one where sender and receiver are different institutions with
// nothing else in common.
//
// A file with ONE destination is returned unchanged, which is the ordinary case
// and is worth the branch: the common file then really is the submitter's own
// bytes, and only a fan-out rewrites anything.
func creditTransferPart(doc *iso20022.Pacs008, idx []int) iso20022.Document {
	if len(idx) == len(doc.FIToFICstmrCdtTrf.CdtTrfTxInf) {
		return doc
	}
	out := *doc
	out.FIToFICstmrCdtTrf.CdtTrfTxInf = pick(doc.FIToFICstmrCdtTrf.CdtTrfTxInf, idx)
	out.FIToFICstmrCdtTrf.GrpHdr.NbOfTxs = strconv.Itoa(len(idx))
	return &out
}

func directDebitPart(doc *iso20022.Pacs003, idx []int) iso20022.Document {
	if len(idx) == len(doc.FIToFICstmrDrctDbt.DrctDbtTxInf) {
		return doc
	}
	out := *doc
	out.FIToFICstmrDrctDbt.DrctDbtTxInf = pick(doc.FIToFICstmrDrctDbt.DrctDbtTxInf, idx)
	out.FIToFICstmrDrctDbt.GrpHdr.NbOfTxs = strconv.Itoa(len(idx))
	return &out
}

func pick[T any](all []T, idx []int) []T {
	out := make([]T, 0, len(idx))
	for _, i := range idx {
		out = append(out, all[i])
	}
	return out
}

// submitterOf is the party whose bank hands a payment to the clearing house.
//
// One rule, two directions, and every institution in this package that has to
// name the instructing agent uses it: Deployment.Submit to choose which bank
// runs the submitting half, and ClearingHouse.receiveStatus to choose whose
// queue the answer goes into. Written once because the two must agree — an
// answer addressed to a bank that did not submit is a file nobody was waiting
// for.
//
// It takes the two AGENTS rather than a Payment, because Submit has only a
// request and a request is not yet a payment. What all four callers want is a
// subscriber to enqueue for. See payment.PartyRef.
func submitterOf(scheme payment.Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	if scheme.Direction() == payment.Pull {
		return creditorAgent
	}
	return debtorAgent
}

// receiverOf is the bank a released output file is addressed to: the other one.
//
// Same rule read the other way round, and separate from submitterOf because the
// two answer different questions on the same payment — who is owed the ANSWER to
// an instruction, and who is owed the INSTRUCTION. Deriving one from the other at
// each call site is how they come to disagree.
func receiverOf(scheme payment.Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	if scheme.Direction() == payment.Pull {
		return debtorAgent
	}
	return creditorAgent
}

// returnerOf is the party whose bank sends a settled payment back: submitterOf's
// counterpart in both senses, the other party and the other role.
//
// The rule itself is payment.ReturnerOf, and that is where its reasoning lives.
// It moved out of this package when the domain acquired a second use for it —
// payment.PostReturnLegTx decides whether a bank may REFUSE its leg by asking
// whether that bank is the returner — and two copies would have been free to
// disagree about who the returner is. This stays as a delegation so that the
// call sites here, which read as deployment-local rules beside submitterOf, do
// not have to.
func returnerOf(scheme payment.Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	return payment.ReturnerOf(scheme, debtorAgent, creditorAgent)
}

// returnMsgDef is the pacs.004's message name, which two institutions here
// dispatch a pacs.002 by.
//
// A status report says which message definition it is ABOUT — OrgnlMsgNmId,
// written by payment.StatusMessage and read back by payment.ReadStatus — and
// that element is load-bearing rather than decorative. The clearing house is
// answered by the settlement agent about a CYCLE it instructed and about a
// PAYMENT whose return it forwarded, and the two answers are the same message
// definition arriving down the same connection; reading one as the other would
// look a cycle id up as a payment. A bank is answered about the instruction it
// submitted and about the return it asked for, and only the first is ever a
// reason to give a payer their money back.
//
// Taken from the codec rather than written out as a literal, so that it cannot
// drift from the identifier iso20022 actually puts on the wire.
var returnMsgDef = iso20022.Pacs004{}.MessageDefinitionIdentifier()

// isAbout reports whether a status answers a message of the given definition.
//
// It parses the document a second time, which is cheap and deliberate:
// payment.ReadStatus is pure, and the alternative is threading the parse
// through a dispatch that exists precisely to decide WHICH handler should do
// the reading.
func isAbout(doc *iso20022.Pacs002, msgDef string) bool {
	orig, _ := payment.ReadStatus(doc)
	return orig.MsgDefIdr == msgDef
}

// notProvided is what a message says where a reference is genuinely unavailable.
//
// It is the EPC's convention, already used by payment for a credit transfer with
// no end-to-end reference, and it is here for the one case that has no reference
// at all: a pacs.002 answering a file that did not parse. That report cannot
// quote the original's message id, its message name or the transaction's
// references, because the bytes carrying them were unreadable — and this
// package's pacs.002 makes all four mandatory (iso20022.OriginalGroupHeader and
// PaymentTransactionStatus, the latter needing at least one back-reference).
//
// A real network would not have the problem: an unreadable file is answered with
// admi.002 MessageReject, which refers back by nothing and carries a reason on
// its own. This system has no admi.002, so the FF01 goes in the message it does
// have, with the unavailable references named as unavailable rather than
// invented.
const notProvided = "NOTPROVIDED"

// unreadable is the pacs.002 an institution answers a file it could not parse
// with, addressed back to the subscriber that uploaded it.
//
// A file that arrived is answered TWICE and the two answers say different
// things. EBICS_OK already went back on the upload, so the uploader believes the
// file arrived — which it did. What the receiver later made of it goes on the
// order's acknowledgement (ebics.Server.Rejected, collected by HAC) and, for the
// benefit of a subscriber that is watching payments rather than orders, in this
// FF01 waiting on its next download.
//
// It is the one rejection every real receiver must be able to send, and it is
// expressible here only because a queue names its subscriber: the bytes hide
// their own header, so the sender is known from the connection the file came in
// on and from nothing inside it.
func unreadable(mc payment.MessageContext, cause error) (iso20022.Envelope, error) {
	return payment.StatusMessage(
		payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided},
		[]payment.TransactionStatusReport{{
			EndToEndID: notProvided,
			Status:     iso20022.TransactionStatusRejected,
			Code:       iso20022.StatusReasonInvalidFileFormat,
			Text:       cause.Error(),
		}},
		mc,
	)
}

// actorContextKey marks a unit of work as belonging to one institution's work.
//
// Nothing in the domain reads it — payment neither knows nor cares — but the
// book recorder in books_test.go does, and it is what lets a test say WHICH
// institution reached a book rather than only that the book was reached.
//
// A context value rather than an argument because it has to survive the trip
// through payment: an institution calls into the domain, which opens the unit of
// work several frames down, and no signature on that path has anywhere to put
// it.
type actorContextKey struct{}

// withActor marks a context as belonging to one institution's work. Every
// institution here does it at the top of each act it performs.
func withActor(ctx context.Context, bic iso20022.BIC) context.Context {
	return context.WithValue(ctx, actorContextKey{}, bic)
}

// actorOf reports which institution's work this context belongs to, if any. A
// context with no actor is work no institution did: a seed, a test fixture, an
// operator's read.
func actorOf(ctx context.Context) (iso20022.BIC, bool) {
	bic, ok := ctx.Value(actorContextKey{}).(iso20022.BIC)
	return bic, ok
}
