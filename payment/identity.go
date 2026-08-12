package payment

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/raphi011/cbs/iso20022"
)

// Identity is which institution a Network acts as.
type Identity struct {
	role role
	pid  ParticipantID
}

// role is the kind of institution, and there are exactly three because a
// deployment holds exactly three kinds.
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
func AsBank(pid ParticipantID) Identity { return Identity{role: roleBank, pid: pid} }

// AsClearingHouse is the CSM's identity: it clears, it nets, it routes, and it
// holds no book of accounts at all.
func AsClearingHouse() Identity { return Identity{role: roleClearingHouse} }

// AsCentralBank is the settlement agent's identity, and it is the only one whose
// Network holds the central bank's book. See Network.centralBank.
func AsCentralBank() Identity { return Identity{role: roleCentralBank} }

// Participant is the bank this identity is, and ok is false for the other two.
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
type Networks struct {
	stores Stores
	clock  func() time.Time

	// schemes is SHARED by every network this mints, and that is a decision rather
	// than an optimisation.
	schemes *schemeRegistry
}

// schemeRegistry is the registered schemes and the lock over them.
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
func (n *Networks) Bank(ctx context.Context, pid ParticipantID) (*BankNetwork, error) {
	store, err := n.stores.Bank(ctx, iso20022.BIC(pid))
	if err != nil {
		return nil, fmt.Errorf("payment: opening member bank %s's store: %w", pid, err)
	}
	return newBankNetwork(store, n.clock, pid, n.schemes), nil
}

// ClearingHouse returns the CSM's view, over the clearing house's database.
func (n *Networks) ClearingHouse() *ClearingHouseNetwork {
	return newClearingHouseNetwork(n.stores.ClearingHouse(), n.clock, n.schemes)
}

// CentralBank returns the settlement agent's view, which is the only one holding
// the central bank's book, over the central bank's database.
func (n *Networks) CentralBank() *CentralBankNetwork {
	return newCentralBankNetwork(n.stores.CentralBank(), n.clock, n.schemes)
}

// Stores is the set of databases these networks are minted over, so a caller
// that has to reach all of them at once — clearing the system is the only such
// caller, and it is nobody's act in the domain — can do it without first
// choosing an institution to do it as.
func (n *Networks) Stores() Stores { return n.stores }
