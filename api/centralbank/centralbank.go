// Package centralbank is the settlement agent's HTTP surface: the reserves it
// holds, the settlements it discharged, and its own book's audit trail.
//
// # Two routes here are the OPERATOR's, not this institution's
//
// GET /members lists every bank the deployment holds a database for, and
// POST /admin/reset rebuilds them. Both reach past this institution to
// databases that are not the settlement agent's. They are one operator's acts
// over a DEPLOYMENT, and a deployment is not an institution; this listener is
// where the operator's console is served, and serving them here is not the claim
// that the settlement agent performs them.
//
// Institution below is declared here, by the package that needs it, so this
// package knows nothing about a mesh or a store set.
package centralbank

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/payment"
)

// An Institution is the settlement agent, as this surface needs it, plus the two
// deployment-wide acts the package doc names.
//
// There is no method that SETTLES, and its absence is the shape of what the mesh
// changed: a settlement is performed on INSTRUCTION — the clearing house reaches
// a cut-off, sends a pacs.009, and this institution's actor answers ACSC or
// RJCT/AM04. A method that let a human do it beside that would be a second way
// to settle the same cycle, racing the first.
type Institution interface {
	// Network is the settlement agent's own view: its reserve register, its
	// settlements, its own book.
	Network() *payment.Network

	// Members is every bank the deployment holds a database for, each read out of
	// its own database. The widest read in the process, and the operator's rather
	// than this institution's — no institution can answer it, because the csm
	// shape has no banks table and a roster omits the founded bank the listing
	// exists to show.
	Members(ctx context.Context) ([]*payment.Bank, error)

	// Reset clears the store and rebuilds the sample dataset. The deployment's
	// act, served here for the reason the package doc gives.
	Reset(ctx context.Context) error

	// Log is what the middleware chain and the reset route write through.
	Log() *slog.Logger
}

// surface is the handler receiver: one Institution, and nothing else.
type surface struct{ inst Institution }

func (s *surface) network() *payment.Network { return s.inst.Network() }
