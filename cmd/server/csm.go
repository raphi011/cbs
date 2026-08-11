package main

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// A ClearingHouse is the CSM's view: every payment in the network, the cycles,
// the schemes and the roster.
//
// It satisfies csm.Institution, which api/csm declares; see Deployment.
type ClearingHouse struct {
	d   *Deployment
	net *payment.Network
}

func (d *Deployment) ClearingHouse() *ClearingHouse {
	return &ClearingHouse{d: d, net: d.nets.ClearingHouse()}
}

func (c *ClearingHouse) Network() *payment.Network { return c.net }
func (c *ClearingHouse) Log() *slog.Logger         { return c.d.log }

func (c *ClearingHouse) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	return c.d.mesh.Submit(ctx, req)
}

func (c *ClearingHouse) Reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error) {
	return c.d.mesh.Reject(ctx, id, code, text)
}

func (c *ClearingHouse) Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	return c.d.mesh.Return(ctx, id, reason, text)
}

func (c *ClearingHouse) CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	return c.d.mesh.CloseCycle(ctx, id)
}

func (c *ClearingHouse) Settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	return c.d.mesh.Settle(ctx, id)
}
