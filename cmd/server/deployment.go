package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
)

// A Deployment is every institution this process holds, and the only thing a
// surface can be built from.
//
// # It is not a request handler and answers nothing
//
// The three surfaces are api/bank, api/csm and api/centralbank, each over an
// interface that package declares. What this type does is hand out the ONE
// institution a listener is bound to — Bank, ClearingHouse, CentralBank, one
// file apiece beside this one — and each of those is bound at construction and
// never rebound. That is the whole shape of the split: a listener holds an
// institution, not a deployment with a field naming which institution it is
// currently being.
//
// The three satisfy their surface's interface STRUCTURALLY, so the dependency
// runs one way and none of the three surface packages names anything here. That
// is what keeps them a library and this an orchestration: api/bank could be
// driven by some other system's bank, and this one is simply the bank this
// deployment has.
//
// # One network per institution, and never a shared one
//
// A bank's identity is constructor state on payment.Network, so one shared
// Network would mean every bank's port resolving in one register — one bank
// reading another bank's customers, one layer above where the mesh's recorder
// can see it, because this layer is not an actor. Each of the three constructors
// binds its own institution's Network, and nothing reachable from one of them
// can act as anybody else.
//
// The handles are fixed rather than swappable pointers, because a Network holds
// no data — every entity lives in the Store behind it. That is also what turns
// Reset from a pointer swap into a store operation, which is the only form of it
// that clears a database.
type Deployment struct {
	// nets mints one Network per institution. It is what the three constructors
	// below bind from, and what Reset clears the store through.
	nets *payment.Networks

	// mesh is the message transport, and the only thing in this process that runs
	// a payment's choreography.
	//
	// No handler on any of the three surfaces plays more than one institution.
	// Each hands the instruction to the actor whose act it is and answers with
	// what that actor did and nothing more.
	//
	// It is not optional. A deployment with no mesh has no way to carry a payment
	// past the first institution, so NewDeployment refuses one at construction
	// rather than leaving four handlers to fail on their first request.
	mesh *mesh.Mesh

	// populate rebuilds the sample dataset. It must be idempotent: the process
	// calls it at boot and Reset calls it again after clearing the store.
	//
	// It is handed the MESH as well as the networks, because admitting a bank is
	// a conversation between three institutions and a reseed that founded its
	// banks without one would rebuild a network in which nobody can be paid. Reset
	// passes its own mesh rather than letting the builder close over one, so the
	// mesh a reseed admits into cannot be a different mesh from the one the reset
	// drained and forgot the old banks from.
	//
	// It takes the FACTORY and not one institution's network, and that is why it
	// is here rather than on any of the three: building the sample dataset is
	// three institutions' work — banks are founded, reserve accounts opened,
	// cycles closed and settled — and the seed says so act by act (see
	// seed.builder). It is not an institution's act and there is no institution to
	// perform it as.
	populate func(context.Context, *payment.Networks, *mesh.Mesh) error

	// resetMu serializes Reset. See the method for why one unit of work cannot
	// do the job instead. There is one Deployment behind every listener, so one
	// lock is what every surface takes.
	resetMu sync.Mutex

	log *slog.Logger
}

// NewDeployment builds a Deployment over the process's networks and a running
// mesh.
//
// populate is the sample-dataset builder, called again on every Reset; pass nil
// for a deployment that resets to an empty system. If log is nil, the default
// slog logger is used.
//
// msh may NOT be nil, and that is the one thing here that is refused rather than
// defaulted. A missing logger has an obvious stand-in and a missing populate
// means "reset to nothing", but a missing mesh has no honest substitute: the only
// alternative would be this layer running the choreography itself. It is a
// wiring mistake, not a runtime condition, so it fails at construction.
//
// NewDeployment performs no I/O — the caller populates the network before
// serving — so a store that is unavailable fails where it can be reported rather
// than inside a constructor with no error to return.
func NewDeployment(nets *payment.Networks, msh *mesh.Mesh, populate func(context.Context, *payment.Networks, *mesh.Mesh) error, log *slog.Logger) *Deployment {
	if msh == nil {
		panic("server: a Deployment needs a mesh; without one nothing here has a way to carry a payment past the bank it was handed to")
	}
	if log == nil {
		log = slog.Default()
	}
	if populate == nil {
		populate = func(context.Context, *payment.Networks, *mesh.Mesh) error { return nil }
	}
	return &Deployment{nets: nets, mesh: msh, populate: populate, log: log}
}

// Log is the logger every surface's middleware chain writes through.
func (d *Deployment) Log() *slog.Logger { return d.log }

// Reset discards all persisted state and rebuilds the sample dataset. It must go
// through the store: swapping an in-memory object graph would leave every row in
// the database intact and still report success.
//
// It is the DEPLOYMENT's act and not the settlement agent's, which is why it is
// here and not on CentralBank — that surface serves it because the operator's
// console is there, and CentralBank.Reset says so where it delegates.
//
// Resets serialize. They are the one operation in this system that is NOT a
// single unit of work and cannot be made into one — the clear is its own unit of
// work and the rebuild is dozens more, because the seed builder drives the
// ordinary public API. Two overlapping resets interleave: the second clears over
// the first's half-built scenario and the first finishes writing on top of the
// second's, leaving several copies of some entities and none of others. Eight
// concurrent resets produced twelve participants where there should have been
// four.
//
// A mutex is the honest fix for that shape: the operation is rare, bounded and
// idempotent, so a second caller waiting for the first and then redoing the work
// is the right answer. It makes resets exclusive within ONE process; two servers
// sharing a database could still race, which is a property of a teaching tool.
//
// # It drains the mesh first, and the order is the point
//
// A reset that truncated with messages still in flight would leave actor
// goroutines writing into a store the reset had already emptied, and then the
// reseed would race those same handlers for the tables it is rebuilding. The
// result is not an error, it is a scenario with extra rows in it.
//
// Draining is the ONLY way an in-flight conversation finishes: mesh.Stop cuts
// them, and there is nothing here to stop anyway — the mesh outlives the reset,
// exactly as the store does. Drain needs no deadline of its own, because the
// admin route already gives the whole operation one.
//
// # The dead letters are REPORTED, and the reset does not happen
//
// Drain hands back what handlers had nobody to report to, and this is the last
// moment anybody can be told: the store is about to be emptied, so a dead letter
// swallowed here is a failure whose evidence is deleted in the next statement.
// It is logged and returned, and the truncate does not run.
//
// That leaves the operator a way out, because the mesh TAKES its dead letters
// rather than accumulating them: the refused reset has already cleared them off,
// so calling it again drains a quiet mesh and goes through. One 5xx that names
// what went wrong, then a reset that works.
//
// It is also what makes the ordering testable. With the drain moved after the
// truncate, handlers write into an emptied store, fail, and are collected by
// that same drain — so a Reset that swallowed them would report success over
// exactly the damage this ordering exists to prevent.
// TestResetDrainsBeforeTruncating fails on the swap because of this.
//
// # The mesh is rebuilt too, and forgetting comes before the truncate
//
// A reset replaces the network. Every participant is deleted and the reseed
// creates new ones, and the mesh is NOT restarted — Start ran once, at boot, and
// its roster read is long past. So the actor table has to be reconciled here or
// it never is, and what it describes otherwise is a network that no longer
// exists: a BIC no operator can ever admit again, because an actor still answers
// to it, and an entry in the bank index pointing at a goroutine whose bank has
// been deleted.
func (d *Deployment) Reset(ctx context.Context) error {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	if err := d.mesh.Drain(ctx); err != nil {
		d.log.Error("mesh: dead letters collected by a reset", "error", err)
		return fmt.Errorf("server: the mesh had unanswered work when the reset ran; it has been cleared, so try again: %w", err)
	}
	if err := d.mesh.ForgetBanks(ctx); err != nil {
		return fmt.Errorf("server: the mesh still holds banks the reset is about to delete: %w", err)
	}
	if err := d.nets.Stores().Reset(ctx); err != nil {
		return err
	}
	// Last, and it is what gives the reseeded banks their actors: each one is
	// admitted through this mesh, so nothing has to re-register them afterwards.
	return d.populate(ctx, d.nets, d.mesh)
}
