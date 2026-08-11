package seed

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// A Deployment is the running system this scenario is built into: the five acts
// the builder cannot perform for itself.
//
// Everything else the seed does it does directly, one institution's half at a
// time, through payment.Networks. What is left here is what needs MORE THAN ONE
// institution to be running and talking — an admission, a directory pull, a
// lodgement — plus the wait that says the talking has finished.
//
// It is an interface because the deployment that satisfies it is a composition
// root and not a library: cmd/server holds the institutions, and a package it
// imports cannot name them. So the seed declares what it needs and is handed
// something that has it, which is the same shape api/bank's Institution takes
// one layer up.
//
// A nil pointer inside a non-nil interface is NOT caught by Populate's guard,
// and there is no defensive check for it here. The one field in this process
// that holds a Deployment refuses a nil at construction, so the value reaching
// this is either absent — which the guard does catch — or real.
type Deployment interface {
	// AddBank gives a provisioned bank its place in the network. A bank with rows
	// and no place can neither send nor be sent to.
	AddBank(context.Context, *payment.Bank) error

	// RefreshDirectory is one bank subscribing: it replaces that bank's copy of
	// the scheme's routing directory with the roster the clearing house
	// publishes. Two databases, and no bank may open the other's.
	RefreshDirectory(context.Context, iso20022.BIC) ([]payment.DirectoryEntry, error)

	// Lodge moves one bank's own vault cash onto its reserve at the central bank.
	// What comes back is the instruction; the central bank's half lands later.
	Lodge(context.Context, iso20022.BIC, ledger.AssetCode, ledger.Amount) (payment.LodgementInstruction, error)

	// Drain blocks until nothing the seed started is still in flight. It is what
	// makes "the scenario is built" a fact rather than a hope, and it is why no
	// step here waits for a duration.
	Drain(context.Context) error

	// CentralBankBIC names the settlement agent, which has no roster row to be
	// read from: it is not a member of the scheme it settles. A settlement
	// instruction names it at one end of every leg.
	CentralBankBIC() iso20022.BIC
}
