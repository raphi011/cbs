package payment

import (
	"fmt"
	"sync"
	"time"
)

// Identity is which institution a Network acts as.
//
// # Why this is constructor state and not a per-call argument
//
// It was an argument until Task 18b, on nine methods, and every one of them
// spelled it `by`. The mesh's actors passed their own id — bank.pid, and its
// doc said in as many words that it was "a loan and not the answer" — because
// nothing in this package could check it. That is the shape a per-call identity
// has: the caller asserts who it is, the act believes it, and the guard behind
// it ("is this bank a party to this payment?") is the only thing standing
// between a handler and another member's book. Ten methods, ten assertions, and
// the assertion is made in the layer that would have to be wrong for the guard
// to matter.
//
// As constructor state it is not asserted at all. A Network is one institution's
// handle on the system, the way a Book is one book's; there is no call at which
// a different answer could be given, so there is no call at which the wrong one
// could be given. That is also the whole reason this is Task 18b and not part of
// 18d: once each entity has a store of its own, the identity is what SELECTS the
// store, and a value that arrives per call cannot select the thing the call is
// already running against.
//
// What it does not become is authorization. Nothing verifies that the process
// holding a bank's Network is that bank — the composition root hands it out (see
// Networks) exactly as api's listeners hand out ports, and the port is the claim.
// What it removes is the ability to act as somebody else THROUGH a handle you
// legitimately hold, which is what an argument leaves open by construction.
//
// The zero Identity is not a network with no opinion, it is a wiring mistake;
// NewNetwork refuses it. See NewNetwork.
type Identity struct {
	role role
	pid  ParticipantID
}

// role is the kind of institution, and there are exactly three because the mesh
// has exactly three actors and mesh/ops.go is their enumeration: bankOps,
// csmOps, settlementOps.
//
// It is unexported, and the three constructors below are the only way to name
// one, so a caller cannot build an identity this package has no acts for.
type role uint8

const (
	// roleUnset is the zero value and exists to be refused. See NewNetwork.
	roleUnset role = iota
	roleBank
	roleClearingHouse
	roleCentralBank
)

// AsBank is one member bank's identity: the participant whose book, whose
// deposit register and whose customers this network's acts are about.
//
// It is the only one of the three that carries a value, and what it carries is
// the ParticipantID alone. Its BookID is not stored beside it because a bank IS
// its own book — ledger.BookID(ID), fixed by FoundBankTx and documented on
// Bank.BookID — so a second copy here could only ever be the same answer or a
// wrong one. Its BIC is not stored either: every act that needs one reads it off
// the bank's own row, which it has already loaded to reach the book.
func AsBank(pid ParticipantID) Identity { return Identity{role: roleBank, pid: pid} }

// AsClearingHouse is the CSM's identity: it clears, it nets, it routes, and it
// holds no book of accounts at all. See csmOps in the mesh, which is the list of
// what it may reach, and TestTheCSMTouchesOnlyTheNetworkBook, which is the
// measurement that it reaches nothing else.
func AsClearingHouse() Identity { return Identity{role: roleClearingHouse} }

// AsCentralBank is the settlement agent's identity, and it is the only one whose
// Network holds the central bank's book. See Network.centralBank.
func AsCentralBank() Identity { return Identity{role: roleCentralBank} }

// Participant is the bank this identity is, and ok is false for the other two.
//
// It exists for the layers above that already carry the same id for their own
// reasons — api's bound listener, the mesh's index of actors — so that they can
// check the two agree rather than keep two answers to one question.
func (i Identity) Participant() (ParticipantID, bool) {
	return i.pid, i.role == roleBank
}

// String names the institution, for the refusals below. It is the whole of what
// an error message needs: which entity was asked to perform somebody else's act.
func (i Identity) String() string {
	switch i.role {
	case roleBank:
		return fmt.Sprintf("member bank %s", i.pid)
	case roleClearingHouse:
		return "the clearing house"
	case roleCentralBank:
		return "the central bank"
	default:
		return "no institution"
	}
}

// Networks mints one Network per institution over a shared store and clock.
//
// It is the composition root's handle and the ONLY thing that holds more than
// one institution's view: cmd/server builds one, api takes one and binds each
// listener to the single Network its surface belongs to, and the mesh takes one
// and gives each actor its own. Nothing downstream of those two holds a second.
//
// Today all N+2 Networks it mints share the one Store it was built with, so this
// type is pure wiring. That is the point of it landing here rather than in Task
// 18d: when each entity gets a store of its own, the store it opens is a
// property of the entity being asked for, which is a change INSIDE this type and
// nowhere above it.
type Networks struct {
	store Store
	clock func() time.Time

	// schemes is SHARED by every network this mints, and that is a decision
	// rather than an optimisation.
	//
	// A scheme is code and not data — RegisterScheme takes a Go value with
	// behaviour on it — so it is the one thing in this system that does not live
	// in a store and therefore the one thing per-entity networks could disagree
	// about. Two institutions disagreeing about which schemes exist is not a
	// difference in what each may DO, which is what this whole task is for; it
	// is one of them being unable to read a message the other can write. A
	// receiving bank whose network had never heard of the euro push scheme
	// answers ErrSchemeNotFound to a pacs.008 the payer's bank composed
	// perfectly well, and the two would be looking at the same payment row while
	// they did it.
	//
	// So registering a scheme on any network this mints registers it on all of
	// them, and that survives Task 18d unchanged: separate STORES are the point,
	// separate scheme registries never were. The mesh's usdCT fixture and
	// payment's dollarPush are what measure it.
	schemes *schemeRegistry
}

// schemeRegistry is the registered schemes and the lock over them.
//
// It is a type rather than two fields on Network because it is shared: see
// Networks.schemes for what sharing it is worth. A Network built by NewNetwork
// on its own gets a registry of its own.
type schemeRegistry struct {
	mu sync.RWMutex
	m  map[SchemeID]Scheme
}

func newSchemeRegistry() *schemeRegistry {
	return &schemeRegistry{m: make(map[SchemeID]Scheme)}
}

// NewNetworks builds the factory. It performs no I/O, for NewNetwork's reason.
func NewNetworks(store Store, clock func() time.Time) *Networks {
	return &Networks{store: store, clock: clock, schemes: newSchemeRegistry()}
}

// Bank returns the given member bank's own view of the system.
//
// A fresh Network per call, because one is cheap — no I/O, a scheme map and four
// stateless views — and because a cache keyed by ParticipantID would have to
// answer what happens when that bank is deleted by a Reset. Callers hold the
// result for the lifetime of an actor or a listener, which is where the cost
// would be if there were one.
func (n *Networks) Bank(pid ParticipantID) *Network {
	return newNetwork(n.store, n.clock, AsBank(pid), n.schemes)
}

// ClearingHouse returns the CSM's view.
func (n *Networks) ClearingHouse() *Network {
	return newNetwork(n.store, n.clock, AsClearingHouse(), n.schemes)
}

// CentralBank returns the settlement agent's view, which is the only one holding
// the central bank's book.
func (n *Networks) CentralBank() *Network {
	return newNetwork(n.store, n.clock, AsCentralBank(), n.schemes)
}

// Store is the store every network this mints shares, so a caller can open its
// own unit of work — or reset the whole system — without first choosing an
// institution to do it as. api's Reset is the caller: clearing the database is
// nobody's act in the domain.
func (n *Networks) Store() Store { return n.store }
