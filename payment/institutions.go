package payment

import "time"

// The three kinds of institution a deployment holds, each as its own type.
//
// # What the type answers
//
// An act that belongs to one institution is a method on that institution's type
// and on no other, so a clearing house asked to discharge a cut-off does not
// COMPILE. Identity answers the same question at runtime and still does — see
// Network.self, Network.clearingHouse and Network.centralBankBook — because the
// acts that reach those guards are not the only ones an institution can get
// wrong: a table its schema never created refuses at the store instead
// (sqlite.ErrNotInThisShape), and neither guard can see a book id passed as an
// ordinary argument.
//
// # What Network keeps
//
// Each type embeds Network, which carries what all three do: the store, the
// clock, the shared scheme registry, and the acts whose subject is a PAYMENT
// rather than an institution — reading one, listing them, rendering a message
// from one. Every institution keeps its own copy of a payment's row, so "what is
// this payment's status" has one answer here rather than three that could
// disagree.
//
// Networks is the only thing that mints these, and it mints one per institution
// over that institution's own database. Nothing downstream holds a second.

// BankNetwork is one member bank's handle: the acts whose subject is this bank's
// own book, its own register, its own customers and its own half of a payment.
//
// The bank acting is the network's identity rather than an argument, so no
// method here names a participant to act AS. What still decides between two
// members is the domain refusing a bank that is not the payment's party — see
// ErrNotThisBanksPayment and ErrNotAPartyToThisReturn.
type BankNetwork struct{ Network }

// ClearingHouseNetwork is the CSM's handle: it clears, it nets, it routes, and
// it holds no book of accounts at all.
//
// Nothing on it posts, which is what makes the clearing house the one
// institution in this system that moves no money — see
// TestTheCSMTouchesOnlyItsOwnBook, which measures it rather than assuming it.
// What it does hold is the state that is an OBLIGATION rather than a record: the
// files and returns it has taken in and not yet handed over.
type ClearingHouseNetwork struct{ Network }

// CentralBankNetwork is the settlement agent's handle, and the only one holding
// the central bank's book of accounts.
//
// Four acts move central-bank reserves — opening a member's reserve account,
// crediting one on a lodgement, discharging a cut-off's net positions, and
// reversing a settled payment's — and each is on this type and on no other.
//
// What this institution cannot do is ENUMERATE the payments of a batch. Nothing
// here turns a cycle into the payments inside it, which is what "the central
// bank never sees an individual payment" means at a cut-off; a return names one
// payment because the returning bank named it in the message.
type CentralBankNetwork struct{ Network }

// The three constructors, for a caller building ONE institution over a store it
// already holds. Each takes what that institution's identity carries and no
// more, so there is no identity to pass wrongly: only a bank has a participant
// to name. Networks is what a deployment uses; these are for a caller that has
// one store and one institution in mind.
//
// See NewNetwork for what they share — the registered schemes, the refusal of an
// identity belonging to nobody, and the absence of any I/O.

func NewBankNetwork(store Store, clock func() time.Time, pid ParticipantID) *BankNetwork {
	return &BankNetwork{Network: *NewNetwork(store, clock, AsBank(pid))}
}

func NewClearingHouseNetwork(store Store, clock func() time.Time) *ClearingHouseNetwork {
	return &ClearingHouseNetwork{Network: *NewNetwork(store, clock, AsClearingHouse())}
}

func NewCentralBankNetwork(store Store, clock func() time.Time) *CentralBankNetwork {
	return &CentralBankNetwork{Network: *NewNetwork(store, clock, AsCentralBank())}
}
