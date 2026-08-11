package main

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// A Bank is one member bank's own view: its network, its address, and the three
// mesh doors a customer's or an operator's instruction goes through.
//
// It satisfies bank.Institution, which api/bank declares; see Deployment for why
// that is structural and what it buys.
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
