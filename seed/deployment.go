package seed

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// A Deployment is the running system the base state is built into: the acts the
// builder cannot perform for itself.
type Deployment interface {
	// AddBank gives a provisioned bank its place in the network. A bank with rows
	// and no place can neither send nor be sent to.
	AddBank(context.Context, *payment.Bank) error

	// Subscribe is the clearing house publishing its routing table and every
	// member collecting it, in that order. Two databases, and no bank may open
	// the other's.
	Subscribe(context.Context) error

	// CentralBankBIC names the settlement agent, which has no roster row to be
	// read from: it is not a member of the scheme it settles. A lodgement names
	// it at one end.
	CentralBankBIC() iso20022.BIC
}
