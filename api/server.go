package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
)

// Server holds the application state: one payment.Network, the root of the
// whole object graph (each participant bank owns its own book of accounts and a
// deposit register over it).
//
// The Network is a fixed handle rather than a swappable pointer, because it no
// longer holds any data — every entity lives in the Store behind it. That is
// also what turns Reset from a pointer swap into a store operation, which is
// the only form of it that clears a database.
type Server struct {
	net *payment.Network

	// mesh is the message transport, and since sub-project 7b it is the only
	// thing in this process that runs a payment's choreography.
	//
	// What that replaces is worth naming, because it is the whole of this
	// layer's change. Four handlers here used to play several institutions at
	// once inside one unit of work — the submitting bank, the receiving bank and
	// the clearing house on POST /payments; the clearing house and the payer's
	// bank on a rejection — so that they could answer with a finished outcome.
	// They now hand the instruction to the actor whose act it is, and answer with
	// what that actor did and nothing more. See handleInitiatePayment.
	//
	// It is not optional. A Server built with no mesh has no way to carry a
	// payment past the first institution, so NewServer refuses one at
	// construction rather than leaving four handlers to fail on their first
	// request.
	mesh *mesh.Mesh

	// boundPID is the participant this listener belongs to, empty on the
	// central bank's and the clearing house's.
	//
	// It is what replaces the {pid} path segment: a bank's port already names
	// the bank, so a bank's routes have nowhere to put another bank's id. The
	// value lives on a shallow copy of the Server (see forBank) rather than in
	// the request context, which is what leaves all 58 bank handlers untouched
	// — they call s.participant(w, r) exactly as before.
	boundPID payment.ParticipantID

	// populate rebuilds the sample dataset. It must be idempotent: the process
	// calls it at boot and Reset calls it again after clearing the store.
	populate func(context.Context, *payment.Network) error

	// resetMu serializes Reset. See the method for why one unit of work cannot
	// do the job instead.
	resetMu sync.Mutex

	log *slog.Logger
}

// NewServer builds a Server over an existing network and a running mesh.
//
// populate is the sample-dataset builder, called again on every Reset; pass nil
// for a server that resets to an empty system. If log is nil, the default slog
// logger is used.
//
// msh may NOT be nil, and that is the one thing here that is refused rather than
// defaulted. A missing logger has an obvious stand-in and a missing populate
// means "reset to nothing", but a missing mesh has no honest substitute: the
// only alternative would be this layer running the choreography itself, which is
// precisely what sub-project 7b removed. It is a wiring mistake, not a runtime
// condition — no caller can recover from it and no request can be answered
// despite it — so it fails at construction, loudly, rather than at whichever
// request first tried to pay somebody.
//
// NewServer performs no I/O — the caller populates the network before serving —
// so a store that is unavailable fails where it can be reported rather than
// inside a constructor with no error to return.
func NewServer(net *payment.Network, msh *mesh.Mesh, populate func(context.Context, *payment.Network) error, log *slog.Logger) *Server {
	if msh == nil {
		panic("api: NewServer needs a mesh; without one this layer has no way to carry a payment past the bank it was handed to")
	}
	if log == nil {
		log = slog.Default()
	}
	if populate == nil {
		populate = func(context.Context, *payment.Network) error { return nil }
	}
	return &Server{net: net, mesh: msh, populate: populate, log: log}
}

// network returns the live network. Cheap, lock-free, safe for concurrent use.
func (s *Server) network() *payment.Network { return s.net }

// Reset discards all persisted state and rebuilds the sample dataset. It must
// go through the store: swapping an in-memory object graph would leave every
// row in the database intact and still report success.
//
// Resets serialize. They are the one operation in this system that is NOT a
// single unit of work and cannot be made into one: the clear is a TRUNCATE
// outside any transaction, and the rebuild is dozens of separate units of work,
// because the seed builder drives the ordinary public API. Two overlapping
// resets therefore interleave — the second clears over the first's half-built
// scenario and the first finishes writing on top of the second's — leaving
// several copies of some entities and none of others. Eight concurrent resets
// produced twelve participants where there should have been four.
//
// A mutex is the honest fix for that shape: the operation is rare, bounded, and
// idempotent, so a second caller waiting for the first and then redoing the work
// is exactly the right answer. Note this makes resets exclusive within ONE
// process; two servers sharing a database could still race, which is a property
// of a teaching tool that does not pretend to be an HA deployment.
//
// # It drains the mesh first, and the order is the point
//
// A reset that truncated with messages still in flight would leave actor
// goroutines writing into a store the reset had already emptied — a pacs.002
// landing on a payment row that no longer exists, a settlement posted into a
// chart of accounts that has been deleted — and then the reseed would race those
// same handlers for the tables it is rebuilding. The result is not an error, it
// is a scenario with extra rows in it, which is the failure mode this whole
// method already exists to prevent between two overlapping resets.
//
// Draining is the ONLY way an in-flight conversation finishes: mesh.Stop cuts
// them (see its doc), and there is nothing here to stop anyway — the mesh
// outlives the reset, exactly as the store does. Drain blocks until nothing is
// in flight and needs no deadline of its own, because handleReset already gives
// the whole operation one.
//
// # The dead letters are REPORTED, and the reset does not happen
//
// Drain hands back what handlers had nobody to report to, and this is the last
// moment anybody can be told: the store is about to be emptied, so a dead letter
// swallowed here is a failure whose evidence is deleted in the next statement.
// It is logged and returned, and the truncate does not run.
//
// Which makes the failure loud and still leaves the operator a way out, because
// the mesh TAKES its dead letters rather than accumulating them: the refused
// reset has already cleared them off the mesh, so calling it again drains a
// quiet mesh and goes through. One 5xx that names what went wrong, then a reset
// that works — rather than a reset that quietly succeeded over a payment that
// had failed halfway.
//
// It is also what makes the ordering testable. With the drain moved after the
// truncate, handlers write into an emptied store, fail, and are collected by
// that same drain — so a Reset that swallowed them would report success over
// exactly the damage this ordering exists to prevent, and no test could see it.
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
// been deleted. It was reachable in four HTTP calls — admit a bank, reset,
// admit the same BIC — and the 422 it produced said the new bank had no actor
// while the old one was still running and still routable.
//
// ForgetBanks runs BEFORE the truncate and JoinRoster AFTER the reseed, which
// leaves a window in which the mesh routes to no member bank at all. That is the
// right window to leave: a submission during it is refused for a reason that is
// true — there is no bank actor, because the network is being replaced — where
// the alternative is a message delivered to an actor whose bank is being deleted
// underneath it.
//
// Neither institution is forgotten. The clearing house and the central bank have
// no participant row, so a reset does not touch them; they are the configuration
// rather than the data.
func (s *Server) Reset(ctx context.Context) error {
	s.resetMu.Lock()
	defer s.resetMu.Unlock()

	if err := s.mesh.Drain(ctx); err != nil {
		s.log.Error("mesh: dead letters collected by a reset", "error", err)
		return fmt.Errorf("api: the mesh had unanswered work when the reset ran; it has been cleared, so try again: %w", err)
	}
	if err := s.mesh.ForgetBanks(ctx); err != nil {
		return fmt.Errorf("api: the mesh still holds banks the reset is about to delete: %w", err)
	}
	if err := s.net.Store().Reset(ctx); err != nil {
		return err
	}
	if err := s.populate(ctx, s.net); err != nil {
		return err
	}
	// Last, and not skippable: until this runs the reseeded banks are rows with
	// no actor, so the system would answer every read and carry no payment.
	return s.mesh.JoinRoster(ctx)
}

// forBank returns a view of this Server bound to one participant. The copy is
// shallow on purpose: every listener shares the one Network, the one populate
// func and the one log.
//
// It is built field by field rather than as *s because a sync.Mutex copied by
// value is a second, independent lock. resetMu therefore stays behind, on the
// original Server — which is the one the central bank's listener uses, and the
// central bank is the only operator with a reset route. So there is exactly one
// resetMu in the process, guarding the only surface that can reach it.
func (s *Server) forBank(pid payment.ParticipantID) *Server {
	return &Server{
		net:      s.net,
		mesh:     s.mesh,
		boundPID: pid,
		populate: s.populate,
		log:      s.log,
	}
}

// participant resolves the listener's own participant. On failure it writes the
// appropriate error response and returns false, so callers can simply `return`
// when ok is false.
//
// An unbound Server reaching here asks the network for the participant "",
// which is a clean not-found rather than some other bank's data — the failure
// mode worth having if a route is ever registered on the wrong surface.
func (s *Server) participant(w http.ResponseWriter, r *http.Request) (*payment.Participant, bool) {
	p, err := s.network().GetParticipant(r.Context(), s.boundPID)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return p, true
}
