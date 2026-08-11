package seed

import (
	"context"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// A Deployment is the running system this scenario is built into: the three
// acts the builder cannot perform for itself.
//
// Everything else the seed does it does directly, one institution's half at a
// time, through payment.Networks. What is left here is what needs a table no
// single institution owns — a bank's place in the network, the roster the
// clearing house publishes, and the address of the agent that settles.
//
// # Nothing here waits, because nothing here is in flight
//
// Every act below is synchronous and complete when it returns. A file uploaded
// to another institution would not be — it is worked through on a business day
// — and the seed uploads none: it composes both halves of every conversation
// itself, which is what lets a fixed scenario be built without a business day
// running through it.
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

	// CentralBankBIC names the settlement agent, which has no roster row to be
	// read from: it is not a member of the scheme it settles. A settlement
	// instruction names it at one end of every leg.
	CentralBankBIC() iso20022.BIC
}
