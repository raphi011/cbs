package payment

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/raphi011/cbs/iso20022"
)

// Identity is which institution a Network acts as.
//
// # Why this is constructor state and not a per-call argument
//
// As a per-call argument the caller asserts who it is and the act believes it,
// so the domain's own guard ("is this bank a party to this payment?") is the
// only thing between a handler and another member's book. As constructor state
// it is not asserted at all: a Network is one institution's handle on the system,
// the way a Book is one book's, and there is no call at which a different answer
// could be given. It is also what SELECTS the store, which a value arriving per
// call could not do.
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
// It is the only one of the three that carries a value, and ONE value is all
// there is to carry: a bank's ParticipantID, its BookID, its BIC and the name of
// its database are the same string. See Network.book and FoundBankTx.
//
// The type stays ParticipantID rather than iso20022.BIC because the two say
// different things at a call site — which participant is acting, versus which
// address a message is going to — and because iso20022.BIC is where the
// structural rule lives and this is not the layer that validates it.
func AsBank(pid ParticipantID) Identity { return Identity{role: roleBank, pid: pid} }

// AsClearingHouse is the CSM's identity: it clears, it nets, it routes, and it
// holds no book of accounts at all. See csmOps in the mesh, which is the list of
// what it may reach, and TestTheCSMTouchesOnlyItsOwnBook, which is the
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

// Networks mints one Network per institution, each over that institution's own
// store.
//
// It is the composition root's handle and the ONLY thing that holds more than
// one institution's view: cmd/server builds one, api takes one and binds each
// listener to the single Network its surface belongs to, and the mesh takes one
// and gives each actor its own. Nothing downstream of those two holds a second.
//
// The store is a property of the entity being asked for, which costs one thing:
// Bank takes a context and returns an error, because opening a database can
// fail. See Stores.
type Networks struct {
	stores Stores
	clock  func() time.Time

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
	// them: separate STORES are the point, separate scheme registries are not.
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

// NewNetworks builds the factory. It performs no I/O: opening a bank's database
// is Stores' job and happens when a bank is asked for.
func NewNetworks(stores Stores, clock func() time.Time) *Networks {
	return &Networks{stores: stores, clock: clock, schemes: newSchemeRegistry()}
}

// Bank returns the given member bank's own view of the system, over that bank's
// own database.
//
// # Why this one takes a context and returns an error and the other two do not
//
// Because it opens something. The clearing house's store and the central bank's
// exist before any bank does — they are configuration, not data — and Stores can
// hand either back without doing anything. A bank's database is named by that
// bank's BIC and may not have been opened yet in this process, so the first ask
// for it is I/O and I/O fails.
//
// Nothing here checks that the bank is a MEMBER, and nothing should: membership
// is the clearing house's roster, and a founding bank is deliberately not in it
// yet while it writes its own first rows. See Stores.
//
// A fresh Network per call, because one is cheap — a scheme map and four
// stateless views over a store Stores has already cached — and because a cache
// keyed by ParticipantID would have to answer what happens when that bank is
// deleted by a Reset. Callers hold the result for the lifetime of an actor or a
// listener, which is where the cost would be if there were one.
func (n *Networks) Bank(ctx context.Context, pid ParticipantID) (*Network, error) {
	store, err := n.stores.Bank(ctx, iso20022.BIC(pid))
	if err != nil {
		return nil, fmt.Errorf("payment: opening member bank %s's store: %w", pid, err)
	}
	return newNetwork(store, n.clock, AsBank(pid), n.schemes), nil
}

// ClearingHouse returns the CSM's view, over the clearing house's database.
func (n *Networks) ClearingHouse() *Network {
	return newNetwork(n.stores.ClearingHouse(), n.clock, AsClearingHouse(), n.schemes)
}

// CentralBank returns the settlement agent's view, which is the only one holding
// the central bank's book, over the central bank's database.
func (n *Networks) CentralBank() *Network {
	return newNetwork(n.stores.CentralBank(), n.clock, AsCentralBank(), n.schemes)
}

// Stores is the set of databases these networks are minted over, so a caller
// that has to reach all of them at once — clearing the system is the only such
// caller, and it is nobody's act in the domain — can do it without first
// choosing an institution to do it as.
func (n *Networks) Stores() Stores { return n.stores }
