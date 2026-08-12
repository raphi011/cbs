package seed

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// A Deployment is the running system this scenario is built into: the acts the
// builder cannot perform for itself.
type Deployment interface {
	// AddBank gives a provisioned bank its place in the network. A bank with rows
	// and no place can neither send nor be sent to.
	AddBank(context.Context, *payment.Bank) error

	// RefreshDirectory is one bank subscribing: it replaces that bank's copy of
	// the scheme's routing directory with the roster the clearing house
	// publishes. Two databases, and no bank may open the other's.
	RefreshDirectory(context.Context, iso20022.BIC) ([]payment.DirectoryEntry, error)

	// CentralBankBIC names the settlement agent, which has no roster row to be
	// read from: it is not a member of the scheme it settles. A settlement
	// instruction names it at one end of every leg.
	CentralBankBIC() iso20022.BIC

	// Submit hands a customer's instruction to the bank the scheme's direction
	// makes the submitter, which runs its own half and puts the payment in its
	// hub. Nothing has left that bank when this returns.
	Submit(context.Context, payment.InitiatePaymentRequest) (payment.Payment, error)

	// CarryToClearing reaches every member's cut-off, works the uploaded files
	// through the clearing house and collects each submitting bank's answers. It
	// settles nothing.
	CarryToClearing(context.Context) error
}
