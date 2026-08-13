package node

import (
	"context"
	"fmt"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// What every institution needs and no institution owns: the order type a
// document travels under, the answer to a file that would not parse, and the
// mark that says whose work a unit of work is.

// OrderTypeOf is the EBICS order type a document travels under.
func OrderTypeOf(doc iso20022.Document) (ebics.OrderType, error) {
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

// ReturnMsgDef is the pacs.004's message name, which two institutions dispatch
// a pacs.002 by.
var ReturnMsgDef = iso20022.Pacs004{}.MessageDefinitionIdentifier()

// IsAbout reports whether a status answers a message of the given definition.
func IsAbout(doc *iso20022.Pacs002, msgDef string) bool {
	orig, _ := payment.ReadStatus(doc)
	return orig.MsgDefIdr == msgDef
}

// NotProvided is what a message says where a reference is genuinely
// unavailable.
const NotProvided = "NOTPROVIDED"

// Unreadable is the pacs.002 an institution answers a file it could not parse
// with, addressed back to the subscriber that uploaded it.
func Unreadable(mc payment.MessageContext, cause error) (iso20022.Envelope, error) {
	return payment.StatusMessage(
		payment.OriginalMessage{MsgID: NotProvided, MsgDefIdr: NotProvided},
		[]payment.TransactionStatusReport{{
			EndToEndID: NotProvided,
			Status:     iso20022.TransactionStatusRejected,
			Code:       iso20022.StatusReasonInvalidFileFormat,
			Text:       cause.Error(),
		}},
		mc,
	)
}

// actorContextKey marks a unit of work as belonging to one institution's work.
type actorContextKey struct{}

// WithActor marks a context as belonging to one institution's work. Every
// institution does it at the top of each act it performs.
func WithActor(ctx context.Context, bic iso20022.BIC) context.Context {
	return context.WithValue(ctx, actorContextKey{}, bic)
}

// ActorOf reports which institution's work this context belongs to, if any. A
// context with no actor is work no institution did: a seed, a test fixture, an
// operator's read.
func ActorOf(ctx context.Context) (iso20022.BIC, bool) {
	bic, ok := ctx.Value(actorContextKey{}).(iso20022.BIC)
	return bic, ok
}
