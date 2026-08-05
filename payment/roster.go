package payment

import (
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// SettlementMember is the CENTRAL BANK's own record of a bank it holds a
// settlement account for. It moves into the central bank's store at Task 18,
// and it exists as a row of its own before then because that is the only way
// the settlement agent stops borrowing the clearing house's records.
//
// It is what the settlement agent used not to have. Every reserve movement in
// this system resolved its account through the roster's Bank row — through
// another institution's record — so a settlement agent given its own database
// would have had nothing to settle from. That is the deeper half of dissolving
// the participant row, and it is why this is a row rather than a lookup.
//
// # It is keyed by BIC and by nothing else
//
// The settlement agent holds no roster and allocates no bank ids. What an
// acmt.007 tells it is a BIC, so a BIC is what it can key by; a bank id here
// would be an identifier this institution has no way to have been told and no
// way to check.
//
// # One account per asset, and no more of the bank than that
//
// It carries the name for a statement's addressee and one account per asset,
// and nothing about how the bank runs: not its book, not its subledgers, not
// its product. A central bank knows which account it holds for whom. It does
// not know what its members do with the money.
type SettlementMember struct {
	// BIC is the key. See the note above on why it is the only one this
	// institution could have.
	BIC  iso20022.BIC
	Name string

	// Accounts is this member's settlement account per asset, in the CENTRAL
	// BANK's own book. One per asset because a reserve in euro says nothing
	// about a reserve in dollars and the two must never be added up — the same
	// reason Bank.Assets is keyed rather than flat.
	//
	// The account ids in it are the central bank's own, allocated in its own
	// book. This is the record whose absence would leave a split-out settlement
	// agent unable to post.
	Accounts map[ledger.AssetCode]ledger.AccountID

	// OpenedAt is when this institution opened the accounts, which is not
	// necessarily when the scheme admitted the bank: the clearing house writes
	// its own row from the acknowledgement this act produces, so its timestamp
	// is the later of the two.
	OpenedAt time.Time
}

// RosterEntry is the CLEARING HOUSE's record of one member: where to send a
// message addressed to it. It moves into the clearing house's store at Task 18.
//
// It is routing and nothing else. There is no account identifier of any kind on
// it — no subledger, no product, no book — because a clearing house that held
// one would be holding the means to reach into a bank's ledger, which is the
// crossing this whole sub-project is about closing. storetest's
// RosterEntryCarriesNoAccountIdentifiers is what keeps that true: a field added
// here fails that case by name.
//
// The clearing house writes this row from an acknowledgement it did not
// originate — the acmt.010 the settlement agent sends back — because scheme
// membership follows the settlement account rather than the other way round. A
// bank the central bank will not open an account for is not a bank this
// clearing house can route a settlement instruction for.
type RosterEntry struct {
	BIC  iso20022.BIC
	Name string

	// Assets is the set of assets this member clears in, in the order the
	// acknowledgement listed them. A slice and not a map because there is
	// nothing to key: the clearing house holds no account per asset, which is
	// the difference between this row and SettlementMember above.
	Assets []ledger.AssetCode

	// AdmissionRef is the acmt Refs/PrcId that every message of ONE admission
	// echoes — the conversation's only correlator, because the acknowledgement
	// carries no back-reference to the request that caused it.
	//
	// Its reader is the clearing house's refusal, and the refusal cannot be
	// written without it. Mesh.Admit reserves the address at the mesh before
	// anything is written or sent, so an impostor never gets a message onto the
	// wire; the only requests that can reach this institution on a BIC already
	// in its roster are the SAME bank asking for a second asset — the schema
	// carries one currency per acmt.007, so a two-currency bank really does ask
	// twice — and an operator re-driving an interrupted admission. A refusal
	// keyed on "is this BIC in the roster" would refuse exactly those two and
	// never fire on the impostor it exists for. Keyed on this field it separates
	// them: same admission, relay; different admission, acmt.011 before
	// relaying.
	//
	// Task 17d is what writes it from a message and what reads it to refuse.
	// Today's atomic admission composes no messages and has no process id to
	// echo, so the rows it writes carry "" — which is honest rather than a
	// placeholder: those admissions were not conversations.
	AdmissionRef string

	// AdmittedAt is when the scheme admitted this bank, which is when this row
	// was written rather than when the bank was founded. A bank exists before it
	// joins one.
	AdmittedAt time.Time
}
