package payment

import (
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// SettlementMember is the CENTRAL BANK's own record of a bank it holds a
// settlement account for. It lives in the central bank's database and in no
// other, which is what stopped the settlement agent borrowing the clearing
// house's records.
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
// # It is keyed by BIC and by nothing else
//
// The settlement agent holds no roster and allocates no bank ids. What an
// acmt.007 tells it is a BIC, so a BIC is what it can key by; a bank id here
// would be an identifier this institution has no way to have been told and no
// way to check.
//
// # One account per asset, and no more of the bank than that
//
// It carries a name and one account per asset, and nothing about how the bank
// runs: not its book, not its subledgers, not its product. A central bank knows
// which account it holds for whom. It does not know what its members do with the
// money.
//
// The NAME is here and not on the clearing house's row, and the difference is
// which message each institution writes its row from. An acmt.007 names the
// applicant in Org/FullLglNm, so this institution is told one and uses it: the
// reserve account it opens is called "Reserve: <name> (<asset>)", which is what
// an account servicer names an account after. An acmt.010 names nobody, so the
// clearing house is told none — see RosterEntry.
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
	// book.
	Accounts map[ledger.AssetCode]ledger.AccountID

	// OpenedAt is when this institution opened the accounts, which is not
	// necessarily when the scheme admitted the bank: the clearing house writes
	// its own row from the acknowledgement this act produces, so its timestamp
	// is the later of the two.
	OpenedAt time.Time
}

// RosterEntry is the CLEARING HOUSE's record of one member: where to send a
// message addressed to it. It lives in the clearing house's database and in no
// other.
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
// # There is no NAME on it, because the message that writes it has none
//
// AccountRequestAcknowledgementV03 identifies the account owner with an
// OrganisationIdentification29 — a BIC, an LEI, generic identifiers — and
// carries no legal name, no country and no address anywhere; the REQUEST names
// the applicant with an Organisation33 and the answer does not. A name here
// would assert something no message delivered.
//
// A bank's legal name lives where it was told to somebody: on the bank's own
// row, and on the settlement agent's SettlementMember, which learns it from the
// acmt.007's Org/FullLglNm and names the account it opens after it.
type RosterEntry struct {
	BIC iso20022.BIC

	// Assets is the assets this member clears in. A slice and not a map because
	// there is nothing to key it by: the clearing house holds no account per
	// asset, which is the difference between this row and SettlementMember
	// above.
	//
	// Its reader is Network.bothBanksAreMembersTx, from AcceptAtCSMTx: the
	// clearing house will not take a payment into a cycle unless both banks are
	// admitted in the scheme's asset. The case that makes it load-bearing is the
	// PARTLY-ADMITTED bank: one acmt.007 asks for one currency, so a two-asset
	// admission commits twice, and an agent that answers one and refuses the
	// other leaves a Member with internal accounts in both assets and a
	// settlement account in one. Nothing else would refuse its payments in the
	// other asset, and the cut-off could not build their pacs.009.
	//
	// Being a slice, it is ORDERED and it can REPEAT, and the store must answer
	// for both — its child table is keyed by POSITION for exactly that reason,
	// since a key on (bic, asset) would refuse a row this type can hold, and
	// storetest's RosterEntryAssetsAreAnOrderedList holds it to that.
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
	// written without it. Mesh.Admit claims the address at the mesh before
	// anything is written or sent, so an impostor never gets a message onto the
	// wire; what CAN arrive on a BIC already in this roster is the same bank
	// asking again — its second currency, since the schema carries one per
	// acmt.007, or an operator re-driving an interrupted admission. A refusal
	// keyed on "is this BIC in the roster" would refuse exactly those and never
	// fire on the impostor it exists for. Keyed on this field it separates them:
	// same admission, extend; different admission, refuse.
	//
	// This act meets the second acknowledgement of a two-currency admission every
	// time, which is what makes the extension arm ordinary rather than defensive.
	// The clearing house's refusal of a REQUEST is keyed on the same field and
	// meets that case only in a transport that can delay or reorder — see
	// mesh.csm's relayAdmission, which measures why.
	//
	// AdmitMemberTx is what writes it and what reads it to refuse, and
	// mesh.Mesh.Admit is what mints the value: one process id per admission,
	// echoed by every message of it.
	//
	// It cannot be EMPTY, and that is a refusal rather than a convention.
	// AdmitMemberTx will not write an entry from an acknowledgement quoting no
	// admission (checkAcknowledgement, ErrAdmissionNotIdentified), because ""
	// compares equal to every other "": two institutions on one address both
	// quoting nothing would extend one entry instead of the second being
	// refused, which is the one case this field exists to catch. The bank's own
	// Bank.AdmissionRef is the same value guarded the same way for the same
	// reason, from the other end.
	//
	// So every caller supplies one. mesh.Mesh.Admit mints a process id per
	// admission; the seed and the test suites compose no messages and have no
	// process to name, so they derive a reference from the BIC (see
	// store/storetest.Admit) — which also means two of THEIR admissions on one
	// address quote one reference and extend a single entry, which is why a
	// fixture whose banks settle gives each of them an address of its own.
	AdmissionRef string

	// AdmittedAt is when the scheme admitted this bank, which is when this row
	// was written rather than when the bank was founded. A bank exists before it
	// joins one.
	AdmittedAt time.Time
}
