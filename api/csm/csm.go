// Package csm is the clearing house's HTTP surface: every payment in the
// network, the clearing cycles, the schemes it publishes and the roster it
// keeps.
//
// It is the console the network is WATCHED from. What this institution does
// itself is narrow — record a relayed instruction, take one into a cycle,
// reject one, reach a cut-off and instruct settlement — and everything on this
// surface is one of those or a read of the rows they wrote.
//
// Institution below is declared here, by the package that needs it, so this
// package knows nothing about the transport or a deployment.
package csm

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// An Institution is the clearing house, as this surface needs it.
//
// Every method has a caller, and none of them posts: this institution holds no
// book. Four of the six are conversations with another institution — a
// submission handed to a member bank, a rejection and a return the member banks
// are told about, a cut-off the settlement agent is asked to discharge — and
// each answers with what THIS institution did rather than with the outcome,
// because the outcome arrives later and somewhere else.
type Institution interface {
	// Network is the clearing house's own view of the domain: the payments it
	// relays, the cycles it runs, the roster it publishes.
	Network() *payment.ClearingHouseNetwork

	// Submit hands an instruction to the member bank whose act it is. This
	// console is not any bank — see handleInitiatePayment, which reads the
	// submitting side off the roster this institution publishes.
	Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error)

	// Reject declines a payment this institution is holding, on an operator's
	// say-so rather than on a counterparty's. Half of a rejection; the payer's
	// refund is their own bank's act, told to it by the pacs.002 this sends.
	Reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error)

	// Return sends a settled payment back. The returning bank is neither named
	// here nor this operator: it is the bank that RECEIVED the original
	// instruction, worked out from the payment's own scheme.
	Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error

	// CloseCycle reaches the cut-off: net the batch, then instruct the settlement
	// agent to discharge the positions. Netting is this institution's own act and
	// moves nothing; discharging moves central-bank reserves, which no clearing
	// house may do, so the second half is a message.
	CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)

	// Settle is CloseCycle's second half on its own, and the way out of a
	// settlement the agent refused. See handleSettleCycle.
	Settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error)

	// Log is what the middleware chain writes through, and what the three
	// handlers that can commit and fail to send record the half-happened state on.
	Log() *slog.Logger
}

// surface is the handler receiver: one Institution, and nothing else.
type surface struct{ inst Institution }

func (s *surface) network() *payment.ClearingHouseNetwork { return s.inst.Network() }
