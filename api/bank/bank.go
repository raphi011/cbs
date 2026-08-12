// Package bank is one member bank's HTTP surface: its own books, its own
// customers, its own legs of every payment it is a party to.
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
type Institution interface {
	// Network is this bank's own view of the domain. Every read on this surface
	// goes through it, and it resolves in this bank's register and no other.
	Network() *payment.BankNetwork

	// BIC is this bank as an ADDRESS: the identifier a payment names its parties
	// by, and what every comparison against a payment's two sides needs.
	BIC() iso20022.BIC

	// Submit runs this bank's own half of a customer's instruction and puts it in
	// this bank's hub.
	Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error)

	// Pending is what the hub is holding: the instructions taken and not yet put
	// in a file, oldest first. It is what a 202 now means, made visible.
	Pending(ctx context.Context) ([]payment.Payment, error)

	// Cutoff empties the hub into one file per scheme and uploads them, answering
	// with the order id each arrived under. It names no bank for the reason
	// nothing here does: reaching a cut-off is this bank's own act.
	Cutoff(ctx context.Context) ([]ebics.OrderID, error)

	// Lodge moves this bank's own vault cash onto its reserve at the central bank.
	Lodge(ctx context.Context, asset ledger.AssetCode, amount ledger.Amount) (payment.LodgementInstruction, error)

	// RefreshDirectory replaces this bank's copy of the scheme's routing directory
	// with the roster the clearing house publishes.
	RefreshDirectory(ctx context.Context) ([]payment.DirectoryEntry, error)

	// Log is what the middleware chain writes through, and what the four handlers
	// that can commit a leg and fail to upload record the half-happened state on.
	Log() *slog.Logger
}

// surface is the handler receiver: one Institution, and nothing else.
type surface struct{ inst Institution }

// network is this bank's own institution's network. Cheap, lock-free, safe for
// concurrent use.
func (s *surface) network() *payment.BankNetwork { return s.inst.Network() }

// boundBIC is this listener's bank as an address. See Institution.BIC.
func (s *surface) boundBIC() iso20022.BIC { return s.inst.BIC() }

// participant resolves the listener's own bank.
func (s *surface) participant(ctx context.Context) (*payment.Bank, error) {
	return s.network().GetBank(ctx, payment.ParticipantID(s.boundBIC()))
}
