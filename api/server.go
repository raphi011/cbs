package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
)

// Server holds the application state. A bound Server holds ONE payment.Network
// — the one belonging to the institution whose listener it is — plus the factory
// that mints the others.
//
// # One network per listener, and Task 18b is why
//
// There used to be one Network for the whole process, shared by all three
// surfaces and by every bank's, with boundPID beside it telling the bank
// listeners apart. That worked only while a bank's identity travelled as an
// ARGUMENT: GET /directory on bank A's port and on bank B's port called the same
// object and passed different participants.
//
// The moment the identity moved into the Network, one shared Network would have
// meant both ports resolving in the same register — the crossing Task 18a closed,
// reopened one layer up, where the recorder cannot see it because api is not an
// actor. So the three surface methods each bind their own institution's Network
// and forBank binds the bank's, which is Task 18d's first bullet arriving early.
// It is a wiring change and not a split: the store underneath is still one.
//
// The Networks value is what holds more than one institution's view, and it is
// the only thing here that does. Nothing reachable from a bound Server can act
// as anybody but the entity it was bound to.
//
// The handles are fixed rather than swappable pointers, because a Network holds
// no data — every entity lives in the Store behind it. That is also what turns
// Reset from a pointer swap into a store operation, which is the only form of it
// that clears a database.
type Server struct {
	// nets mints one Network per institution. It is what the three surface
	// methods and forBank bind from, and what Reset clears the store through.
	nets *payment.Networks

	// net is THIS listener's own institution, and it is what every handler on
	// this Server reaches the domain through. On an unbound Server — one that
	// has been constructed but whose surface has not been chosen — it is nil,
	// and the surface methods below are what fill it in.
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
	//
	// It is the same id as net's identity and it is kept beside it rather than
	// derived from it, because the two answer different questions. This one is
	// what a HANDLER needs — which participant to name in a URL, in a DTO, in a
	// comparison against a payment's parties — and payment.Identity deliberately
	// does not hand out live handles or ids for that. forBank sets both from one
	// value, so they cannot disagree.
	boundPID payment.ParticipantID

	// populate rebuilds the sample dataset. It must be idempotent: the process
	// calls it at boot and Reset calls it again after clearing the store.
	//
	// It is handed the MESH as well as the networks, because admitting a bank is
	// now a conversation between three institutions and a reseed that founded its
	// banks without one would rebuild a network in which nobody can be paid. Reset
	// passes its own mesh rather than letting the builder close over one, so the
	// mesh a reseed admits into cannot be a different mesh from the one the reset
	// drained and forgot the old banks from.
	//
	// It takes the FACTORY and not this listener's own network, and that is the
	// one place in this package where more than one institution is legitimately
	// in view. Building the sample dataset is three institutions' work — banks
	// are founded, reserve accounts opened, cycles closed and settled — and the
	// seed says so act by act (see seed.builder). It is not an institution's act
	// and there is no institution to perform it as.
	populate func(context.Context, *payment.Networks, *mesh.Mesh) error

	// resetMu serializes Reset. See the method for why one unit of work cannot
	// do the job instead.
	//
	// It is a POINTER, and that is Task 18b's doing. It used to be a value, and
	// what kept there being exactly one of it was that forBank copied the Server
	// field by field and left this one behind — so the bank listeners had a zero
	// mutex nothing could reach, and the central bank's listener WAS the original
	// Server and so held the real one.
	//
	// Neither half of that survives: every listener is a copy now, including the
	// central bank's, so "the original" is not something any request reaches. A
	// value would give each copy a lock of its own, and two copies of the central
	// bank's surface would then reset concurrently — the exact race this mutex
	// exists for, reintroduced by a change that has nothing to do with resetting.
	// One lock allocated once and shared by pointer is what makes the count one
	// again, and it no longer depends on which copy a caller happens to hold.
	resetMu *sync.Mutex

	log *slog.Logger
}

// NewServer builds a Server over the process's networks and a running mesh.
//
// What comes back is UNBOUND: it has no institution and no handler on it will
// work, because s.net is nil until one of the three surface methods below has
// chosen one. That is deliberate rather than a stage to be tidied away — which
// entity a Server is depends on which surface is asked for, and there is no
// answer to give at construction. CentralBankRoutes, ClearingHouseRoutes and
// BankRoutes are the only things that produce a Server anything can be served
// from.
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
func NewServer(nets *payment.Networks, msh *mesh.Mesh, populate func(context.Context, *payment.Networks, *mesh.Mesh) error, log *slog.Logger) *Server {
	if msh == nil {
		panic("api: NewServer needs a mesh; without one this layer has no way to carry a payment past the bank it was handed to")
	}
	if log == nil {
		log = slog.Default()
	}
	if populate == nil {
		populate = func(context.Context, *payment.Networks, *mesh.Mesh) error { return nil }
	}
	return &Server{nets: nets, mesh: msh, populate: populate, log: log, resetMu: &sync.Mutex{}}
}

// network returns this listener's own institution's network. Cheap, lock-free,
// safe for concurrent use.
//
// It is nil on an unbound Server, and a handler reaching it there is a route
// registered on a surface that was never chosen — a wiring mistake that will
// panic at the first request rather than answer one quietly as somebody else.
func (s *Server) network() *payment.Network { return s.net }

// as returns a copy of this Server bound to one institution's network.
//
// The copy is shallow on purpose: every listener shares the one Networks, the
// one mesh, the one populate func, the one reset lock and the one log. What it
// does NOT share is which institution it acts as, which is the whole point.
func (s *Server) as(net *payment.Network) *Server {
	return &Server{
		nets:     s.nets,
		net:      net,
		mesh:     s.mesh,
		populate: s.populate,
		resetMu:  s.resetMu,
		log:      s.log,
	}
}

// Reset discards all persisted state and rebuilds the sample dataset. It must
// go through the store: swapping an in-memory object graph would leave every
// row in the database intact and still report success.
//
// Resets serialize. They are the one operation in this system that is NOT a
// single unit of work and cannot be made into one: the clear is its own unit of
// work, and the rebuild is dozens more,
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
// ForgetBanks runs BEFORE the truncate, which leaves a window in which the mesh
// routes to no member bank at all. That is the right window to leave: a
// submission during it is refused for a reason that is true — there is no bank
// actor, because the network is being replaced — where the alternative is a
// message delivered to an actor whose bank is being deleted underneath it.
//
// Neither institution is forgotten. The clearing house and the central bank have
// no participant row, so a reset does not touch them; they are the configuration
// rather than the data.
//
// # The reseed ends that window itself, and JoinRoster is gone from here
//
// It used to call mesh.JoinRoster last, because the reseed wrote member rows
// directly and the actors behind them had to be registered in a second step.
// Admission is a conversation now: populate is handed this mesh and admits
// through its door, so a reseeded bank gets its actor in the same call that
// founds it and the window closes bank by bank as the scenario is rebuilt.
//
// Calling JoinRoster after that would not merely be redundant, it would FAIL the
// reset. It registers the whole roster and expects to find no member bank
// already registered — the state ForgetBanks used to leave and the reseed no
// longer does — so it would refuse all-or-none on the first address, over work
// that had entirely succeeded, and answer 5xx for a system that was rebuilt.
//
// What that asks of populate is one thing, and it is worth stating because
// nothing here can check it: a reseed must make its banks REACHABLE, which means
// admitting them through the mesh it is given rather than writing their rows.
// One that founds them some other way leaves banks that answer every read and
// carry no payment, and no error anywhere says so.
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
	if err := s.nets.Store().Reset(ctx); err != nil {
		return err
	}
	// Last, and it is what gives the reseeded banks their actors: each one is
	// admitted through this mesh, so nothing has to re-register them afterwards.
	return s.populate(ctx, s.nets, s.mesh)
}

// forBank returns a view of this Server bound to one participant, over THAT
// BANK's own network.
//
// This used to hand every listener the one shared Network and tell them apart
// with boundPID alone, and Task 18b is what ended that. A bank's identity is
// constructor state on payment.Network now, so a shared Network would make
// GET /directory on bank A's port resolve in whichever register the one Network
// belonged to — every bank reading one bank's customers, which is the crossing
// Task 18a closed reappearing one layer above where the recorder can see it,
// because api is not an actor and nothing here records a book.
//
// Minting the bank's network needs no I/O and no store read: a bank IS its own
// book (payment.AsBank), so the participant is the whole of the identity. That
// is what makes pulling this forward from Task 18d a wiring change rather than
// a split — there is still one store behind all of them.
//
// The two values are set together and cannot disagree. boundPID is what the
// handlers name in URLs and DTOs; the network is what the domain acts through.
func (s *Server) forBank(pid payment.ParticipantID) *Server {
	b := s.as(s.nets.Bank(pid))
	b.boundPID = pid
	return b
}

// boundBIC is this listener's bank as an ADDRESS: the identifier a payment names
// its parties by, and what every comparison against a payment's two sides now
// needs.
//
// The conversion is total and lossless because the two are one value — a bank's
// ParticipantID is its BIC since Task 18, see payment.AsBank — and it is a method
// so that this layer says so in one place, exactly as payment.Network.selfBIC
// does one layer down. It is empty on the two institution surfaces, where
// boundPID is, and the handlers that use it are all a bank's own.
func (s *Server) boundBIC() iso20022.BIC { return iso20022.BIC(s.boundPID) }

// participant resolves the listener's own participant. On failure it writes the
// appropriate error response and returns false, so callers can simply `return`
// when ok is false.
//
// An unbound Server reaching here asks the network for the participant "",
// which is a clean not-found rather than some other bank's data — the failure
// mode worth having if a route is ever registered on the wrong surface.
func (s *Server) participant(w http.ResponseWriter, r *http.Request) (*payment.Bank, bool) {
	p, err := s.network().GetBank(r.Context(), s.boundPID)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return p, true
}
