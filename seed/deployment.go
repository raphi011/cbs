package seed

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
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

	// Lodge is a member moving its own vault cash onto reserve at the settlement
	// agent. Two databases and a camt.050 between them, so the deployment holds
	// it for the same reason it holds Subscribe.
	Lodge(ctx context.Context, bic iso20022.BIC, asset ledger.AssetCode,
		amount ledger.Amount) (payment.LodgementInstruction, error)
}
