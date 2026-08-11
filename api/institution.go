package api

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The three institutions a listener can be, one type each.
//
// Each satisfies the Institution interface its own surface package declares —
// api/bank.Institution, api/csm.Institution, api/centralbank.Institution — and
// satisfies it STRUCTURALLY, which is what lets this package import none of the
// three while still being what they are driven by. The dependency runs one way:
// the three import api.
//
// Every one of them is bound at construction and holds exactly one
// *payment.Network. There is no unbound state and nothing rebinds: a listener
// serving a bank's routes over the clearing house's network is not a mistake
// this package can make, because the value it would take does not exist.

// A Bank is one member bank's own view: its network, its address, and the three
// mesh doors a customer's or an operator's instruction goes through.
//
// bic is the whole of its identity. A bank's ParticipantID IS its BIC (see
// payment.AsBank), so the conversion is total and lossless and this type keeps
// one value rather than two that could disagree.
type Bank struct {
	d   *Deployment
	net *payment.Network
	bic iso20022.BIC
}

// Bank binds one member bank's surface, over THAT BANK's own network.
//
// A shared Network would make GET /directory/accounts on bank A's port resolve
// in whichever register the one Network belonged to — every bank reading one
// bank's customers, one layer above where the mesh's recorder can see it,
// because this layer is not an actor and nothing here records a book.
//
// Minting the bank's network needs no store READ: a bank IS its own book, so the
// participant is the whole of the identity. What it does need is the bank's own
// database, which is why this takes a context and can fail — the two
// institutions' networks were opened before any bank existed and theirs cannot.
func (d *Deployment) Bank(ctx context.Context, pid payment.ParticipantID) (*Bank, error) {
	net, err := d.nets.Bank(ctx, pid)
	if err != nil {
		return nil, err
	}
	return &Bank{d: d, net: net, bic: iso20022.BIC(pid)}, nil
}

func (b *Bank) Network() *payment.Network { return b.net }
func (b *Bank) BIC() iso20022.BIC         { return b.bic }
func (b *Bank) Log() *slog.Logger         { return b.d.log }

// Submit runs this bank's own half of a customer's instruction and sends. The
// mesh is what carries it past this institution; see mesh.Mesh.Submit.
func (b *Bank) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	return b.d.mesh.Submit(ctx, req)
}

// Lodge moves this bank's own vault cash onto its reserve at the central bank.
// The acting bank is this one and is not an argument.
func (b *Bank) Lodge(ctx context.Context, asset ledger.AssetCode, amount ledger.Amount) (payment.LodgementInstruction, error) {
	return b.d.mesh.Lodge(ctx, b.bic, asset, amount)
}

// RefreshDirectory replaces this bank's copy of the scheme's routing directory
// with the roster the clearing house publishes. It goes through the mesh because
// the roster is the clearing house's table in the clearing house's database and
// no bank may open it; what the mesh stands in for is the vendor delivering a
// file.
func (b *Bank) RefreshDirectory(ctx context.Context) ([]payment.DirectoryEntry, error) {
	return b.d.mesh.RefreshDirectory(ctx, b.bic)
}

// A ClearingHouse is the CSM's view: every payment in the network, the cycles,
// the schemes and the roster.
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

// A CentralBank is the settlement agent's view, plus the two acts that are the
// OPERATOR's over a deployment rather than this institution's over its own
// books: listing every bank the deployment holds, and rebuilding them.
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
// only such read: the seed's idempotency probe and cmd/server's listener plan ask
// Stores.Banks for the ADDRESSES alone and open nothing.
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
