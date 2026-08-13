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
// uploaded file: the transactions at the given positions, under the sender's
// own group header.
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

// The three parts a payment's two agents play, each named here for the act this
// package performs and each decided by payment.

func submitterOf(scheme payment.Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	return payment.SubmitterOf(scheme, debtorAgent, creditorAgent)
}

func receiverOf(scheme payment.Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	return payment.ReceiverOf(scheme, debtorAgent, creditorAgent)
}

func returnerOf(scheme payment.Scheme, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	return payment.ReturnerOf(scheme, debtorAgent, creditorAgent)
}

// returnMsgDef is the pacs.004's message name, which two institutions here
// dispatch a pacs.002 by.
var returnMsgDef = iso20022.Pacs004{}.MessageDefinitionIdentifier()

// isAbout reports whether a status answers a message of the given definition.
func isAbout(doc *iso20022.Pacs002, msgDef string) bool {
	orig, _ := payment.ReadStatus(doc)
	return orig.MsgDefIdr == msgDef
}

// notProvided is what a message says where a reference is genuinely
// unavailable.
const notProvided = "NOTPROVIDED"

// unreadable is the pacs.002 an institution answers a file it could not parse
// with, addressed back to the subscriber that uploaded it.
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
