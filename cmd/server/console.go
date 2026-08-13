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

// bankConsole is one member bank's surface plus the one door the DEPLOYMENT
// still performs on that bank's behalf: a refresh reads the clearing house's
// rows in process. That is a recorded breach, not the design; see the design
// record.
type bankConsole struct {
	*bank.Bank
	d *Deployment
}

var _ bankapi.Institution = bankConsole{}

func (c bankConsole) RefreshDirectory(ctx context.Context) ([]payment.DirectoryEntry, error) {
	return c.d.RefreshDirectory(ctx, c.BIC())
}
