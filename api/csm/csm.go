// Package csm is the clearing house's HTTP surface: every payment in the
// network, the clearing cycles, the schemes it publishes and the roster it
// keeps.
package csm

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// An Institution is the clearing house, as this surface needs it.
type Institution interface {
	// Network is the clearing house's own view of the domain: the payments it
	// relays, the cycles it runs, the roster it publishes.
	Network() *payment.ClearingHouseNetwork

	// Reject declines a payment this institution is holding, on an operator's
	// say-so rather than on a counterparty's. Half of a rejection; the payer's
	// refund is their own bank's act, told to it by the pacs.002 this sends.
	Reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error)

	// CloseCycle reaches the cut-off: net the batch, then instruct the settlement
	// agent to discharge the positions.
	CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)

	// Settle is CloseCycle's second half on its own, and the way out of a
	// settlement the agent refused. See handleSettleCycle.
	Settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)

	// Log is what the middleware chain writes through, and what the three
	// handlers that can commit and fail to send record the half-happened state on.
	Log() *slog.Logger
}

// An Operator is the DEPLOYMENT, and neither of these two acts is a clearing
// house's: a real one submits nothing and returns nothing on its own say-so.
// They are served here because this is where the operator's console lives.
type Operator interface {
	// Submit hands an instruction to the member bank whose act it is. This
	// console is not any bank — see handleInitiatePayment, which reads the
	// submitting side off the roster the clearing house publishes.
	Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error)

	// Return sends a settled payment back. The returning bank is neither named
	// here nor this operator: it is the bank that RECEIVED the original
	// instruction, worked out from the payment's own scheme.
	Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error
}

// surface is the handler receiver: one institution and the operator whose
// console shares its listener.
type surface struct {
	inst Institution
	op   Operator
}

func (s *surface) network() *payment.ClearingHouseNetwork { return s.inst.Network() }
