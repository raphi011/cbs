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
// It is what the settlement agent used to do without. Every reserve movement in
// this system resolved its account through the BANK's row —
// Bank.Assets[asset].Settlement — so a settlement agent given its own database
// would have had nothing to settle from. That is the deeper half of dissolving
// the participant row, and it is why this is a row rather than a lookup.
//
// # What reads it
//
// settlementAccountTx, which is the only way anything in this package now turns
// an address and an asset into a settlement account. Through it: SettleCycleTx
// and SettleReturnTx, which are the settlement agent posting in its own book,
// and ReserveBalance, which is the operator console asking the central bank
// about that book.
//
// Two readers deliberately stay on the bank's own row, because the question is
// the account holder's rather than the servicer's: DepositTx quoting its own
// account number to fund a deposit, and PostSettlementAdviceTx checking that an
// arriving statement is about the account this bank holds. See BankAccounts.
//
// The row was written a task before it was read. A single task that both
// introduced it and re-pointed the readers would have had to backfill every
// existing member in the same change, and the split would have been invisible
// until then.
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
// Scheme membership follows the settlement account rather than the other way
// round: a bank the central bank will not open an account for is not a bank
// this clearing house can route a settlement instruction for. That is why
// AdmitMemberTx, the clearing house's act, writes this row from an
// acknowledgement it did not originate.
//
// The acknowledgement is a value rather than a message today, built by
// AddParticipantTx from what the settlement agent's act returned, in the same
// unit of work as everything else. Task 17d is what makes it an acmt.010 that
// arrived from another institution. The row is the same either way and so is
// its writer; what changes is where the writer's argument came from.
type RosterEntry struct {
	BIC  iso20022.BIC
	Name string

	// Assets is the assets this member clears in. A slice and not a map because
	// there is nothing to key it by: the clearing house holds no account per
	// asset, which is the difference between this row and SettlementMember
	// above.
	//
	// Being a slice, it is ORDERED and it can REPEAT, and both stores must
	// answer the same way about both — store/pg keys the child table by
	// position for exactly that reason, and storetest's
	// RosterEntryAssetsAreAnOrderedList holds them to it.
	//
	// Whose order it is depends on who wrote it. The writer is AdmitMemberTx,
	// which sorts the assets an acknowledgement names and appends the ones this
	// entry does not already have: the sort is because map iteration is random
	// and a row whose child rows came out in a different order on every
	// identical write would be a store answering differently each time, and the
	// append is because an extension must not reorder what a member was already
	// admitted for.
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
	// AdmitMemberTx is what writes it and what reads it to refuse; Task 17d is
	// what puts a message behind it. AddParticipantTx composes no messages and
	// has no process id to echo, so the rows an admission driven through it
	// carry "" — which is honest rather than a placeholder: those admissions
	// were not conversations. Two of them on one address therefore quote the
	// same empty reference and the second extends the first's entry, which is
	// what that call has always done with a repeated BIC.
	AdmissionRef string

	// AdmittedAt is when the scheme admitted this bank, which is when this row
	// was written rather than when the bank was founded. A bank exists before it
	// joins one.
	AdmittedAt time.Time
}
