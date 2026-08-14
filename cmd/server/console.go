package main

import (
	"context"

	"github.com/raphi011/cbs/api"
	bankapi "github.com/raphi011/cbs/api/bank"
	cbapi "github.com/raphi011/cbs/api/centralbank"
	csmapi "github.com/raphi011/cbs/api/csm"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/node/bank"
	"github.com/raphi011/cbs/payment"
)

// The operator's console. Everything here is the DEPLOYMENT's act, wearing an
// institution's listener because that is where an operator sits; no node
// package holds any of it.

// operator is this deployment as the two hosts' consoles see it. Each api
// package declares the half it serves and this one type is both.
type operator struct{ d *Deployment }

var (
	_ csmapi.Operator = operator{}
	_ cbapi.Operator  = operator{}
)

// Submit and Return are the clearing house's console: a real clearing house
// submits nothing and returns nothing on its own say-so.
func (o operator) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	return o.d.Submit(ctx, req)
}

func (o operator) Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	return o.d.Return(ctx, id, reason, text)
}

// Members, Reset, BusinessDate and AdvanceDay are the settlement agent's
// console: a business day drives all N+2 institutions and none of them owns it.
func (o operator) Members(ctx context.Context) ([]*payment.Bank, error) { return o.d.Members(ctx) }

func (o operator) Reset(ctx context.Context) error { return o.d.Reset(ctx) }

func (o operator) BusinessDate() api.BusinessDateDTO {
	return toBusinessDateDTO(o.d.BusinessDate())
}

func (o operator) AdvanceDay(ctx context.Context) (api.DayReportDTO, error) {
	report, err := o.d.AdvanceDay(ctx)
	return toDayReportDTO(report), err
}

// Phases and RunThrough are the same day a step at a time: every phase the day
// declares is a door of its own, so a reader can run as far as the clearing and
// stop. The listing says which of them today has already run.
func (o operator) Phases() []api.PhaseDTO { return o.d.Phases() }

func (o operator) RunThrough(ctx context.Context, phase string) (api.PhaseReportDTO, error) {
	report, err := o.d.RunThrough(ctx, phase)
	if err != nil {
		return api.PhaseReportDTO{}, err
	}
	return toPhaseReportDTO(report), nil
}

// NetworkFlow and Watch are the mesh: every institution at once, which is the
// deployment's to answer because no institution may — and then the same thing
// as it happens.
func (o operator) NetworkFlow(ctx context.Context, limit int) (api.NetworkFlowDTO, error) {
	return o.d.NetworkFlow(ctx, limit)
}

func (o operator) Watch() (<-chan api.StreamEvent, func()) { return o.d.hub.watch() }

// A member bank's own surface needs no console: every act on it is that bank's,
// and it is a *bank.Bank that serves them.
var _ bankapi.Institution = (*bank.Bank)(nil)
