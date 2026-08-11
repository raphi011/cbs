// Package bank is one member bank's HTTP surface: its own books, its own
// customers, its own legs of every payment it is a party to.
//
// # The port is the identity
//
// One listener per bank, and a bank's routes have nowhere to name another bank —
// the port carries the identity, which is why participant reads it off the
// Institution rather than out of the path. Package api states what that is and
// is not worth.
//
// # What it is driven by, and why that is an interface
//
// Institution below is declared HERE, by the package that needs it, and is
// satisfied by whatever the process builds its banks out of. So this package
// knows nothing about the transport, a store set or a deployment, and its
// handlers can be driven by a fake that has none of them.
package bank

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// An Institution is one member bank, as this surface needs it.
//
// # Every method has a caller
//
// It grew handler by handler, as cmd/server/ops.go's three interfaces did and for the
// same reason: an interface written ahead of its callers is a guess, and a guess
// here is a wrong boundary that then looks authoritative.
//
// # Nothing on it names a bank
//
// Not one method takes a participant or a BIC, because the bank acting IS this
// value. Submit, Lodge and RefreshDirectory are this bank's own acts; Network is
// this bank's own network, whose identity is constructor state (see
// payment.Identity). A handler on this surface therefore has no argument it
// could name somebody else with.
type Institution interface {
	// Network is this bank's own view of the domain. Every read on this surface
	// goes through it, and it resolves in this bank's register and no other.
	Network() *payment.Network

	// BIC is this bank as an ADDRESS: the identifier a payment names its parties
	// by, and what every comparison against a payment's two sides needs.
	//
	// It is the whole of the identity. A bank's ParticipantID is its BIC (see
	// payment.AsBank), so there is one value here rather than two that could
	// disagree, and participant below converts rather than asking twice.
	BIC() iso20022.BIC

	// Submit runs this bank's own half of a customer's instruction and puts it in
	// this bank's hub. Synchronous, which is what lets POST /payments answer 422
	// rather than 202 followed by a rejection nobody can be told about — and it
	// sends nothing, because a bulk network accumulates until a cut-off.
	Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error)

	// Pending is what the hub is holding: the instructions taken and not yet put
	// in a file, oldest first. It is what a 202 now means, made visible.
	Pending(ctx context.Context) ([]payment.Payment, error)

	// Cutoff empties the hub into one file per scheme and uploads them, answering
	// with the order id each arrived under. It names no bank for the reason
	// nothing here does: reaching a cut-off is this bank's own act.
	Cutoff(ctx context.Context) ([]ebics.OrderID, error)

	// Lodge moves this bank's own vault cash onto its reserve at the central
	// bank. It names no bank for the reason above, and it DOES name an asset,
	// because a bank operating in two of them holds two vaults and nothing about
	// "move cash onto reserve" says which.
	Lodge(ctx context.Context, asset ledger.AssetCode, amount ledger.Amount) (payment.LodgementInstruction, error)

	// RefreshDirectory replaces this bank's copy of the scheme's routing
	// directory with the roster the clearing house publishes. It is the
	// subscriber's act and reaches a table this bank does not own, which is why
	// it is here rather than on Network.
	RefreshDirectory(ctx context.Context) ([]payment.DirectoryEntry, error)

	// Log is what the middleware chain writes through, and what the four handlers
	// that can commit a leg and fail to upload record the half-happened state on.
	Log() *slog.Logger
}

// surface is the handler receiver: one Institution, and nothing else.
//
// It exists so a handler can say s.network() rather than s.inst.Network() at
// ninety call sites, and so the participant lookup below has somewhere to live.
type surface struct{ inst Institution }

// network is this bank's own institution's network. Cheap, lock-free, safe for
// concurrent use.
func (s *surface) network() *payment.Network { return s.inst.Network() }

// boundBIC is this listener's bank as an address. See Institution.BIC.
func (s *surface) boundBIC() iso20022.BIC { return s.inst.BIC() }

// participant resolves the listener's own bank.
//
// It reads the identity off the Institution and never out of the path: this
// port already names the bank, so a bank's routes have nowhere to put another
// bank's id. handle and handleBody are what call it, once per request, so no
// handler on this surface asks the question itself.
func (s *surface) participant(ctx context.Context) (*payment.Bank, error) {
	return s.network().GetBank(ctx, payment.ParticipantID(s.boundBIC()))
}
