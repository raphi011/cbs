package main

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/payment"
)

// A CentralBank is the settlement agent's view, plus the two acts that are the
// OPERATOR's over a deployment rather than this institution's over its own
// books: listing every bank the deployment holds, and rebuilding them.
//
// It satisfies centralbank.Institution, which api/centralbank declares; see
// Deployment.
type CentralBank struct {
	d   *Deployment
	net *payment.Network
}

func (d *Deployment) CentralBank() *CentralBank {
	return &CentralBank{d: d, net: d.nets.CentralBank()}
}

func (c *CentralBank) Network() *payment.Network { return c.net }
func (c *CentralBank) Log() *slog.Logger         { return c.d.log }

// Reset is the deployment's, served here because this is where the operator's
// console is. See Deployment.Reset.
func (c *CentralBank) Reset(ctx context.Context) error { return c.d.Reset(ctx) }

// Members answers every bank this deployment holds a database for, each read out
// of its own database, ascending by address.
//
// # No institution is asked, because none of them knows
//
// It is not the clearing house's read: the csm shape has no banks table, and the
// roster is not a substitute — a bank founded and not yet admitted has no roster
// entry and is precisely the bank this listing exists to show, its empty
// settlement references being what says so.
//
// payment.Stores.Banks is the question with an answer: every bank whose DATABASE
// exists. Its doc says nothing in the domain calls it and nothing should — an
// institution asking which other institutions exist is a crossing — and this is
// not the domain asking. It is the process asking what it has, and then asking
// each of those banks for its own row.
//
// # It reaches past this institution's own network, which is why it is here
//
// Every other method on this type goes through c.net. This one goes through the
// deployment, to N databases that are not the settlement agent's. It is the
// operator's act over a deployment rather than the settlement agent's over its
// own books, and the surface package's router is where that exception is written
// down.
//
// # The cost, stated rather than implied
//
// One call opens and reads every bank's database. It is the widest read in this
// process and there is no narrower version of it — a list of N banks is N banks'
// rows and each row is in a different file. The consolation is that it is the
// only such read: the seed's idempotency probe and the listener plan beside this
// file ask Stores.Banks for the ADDRESSES alone and open nothing.
func (c *CentralBank) Members(ctx context.Context) ([]*payment.Bank, error) {
	bics, err := c.d.nets.Stores().Banks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*payment.Bank, 0, len(bics))
	for _, bic := range bics {
		id := payment.ParticipantID(bic)
		net, err := c.d.nets.Bank(ctx, id)
		if err != nil {
			return nil, err
		}
		p, err := net.GetBank(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
