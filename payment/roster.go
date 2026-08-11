package payment

import (
	"time"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// BankCodeAllocation is one row of the NATIONAL REGISTRY's book: which
// institution holds which bank code, in which country. It lives in the
// settlement agent's database, and no bank ever reads it.
//
// It is what an IBAN's middle digits are a reference into, and it is the fact
// that makes an address routable at all. A bank code has no computable
// relationship to a BIC — Aurora is AURODEFFXXX and 99999999 — so somebody has
// to write the pairing down, and this is that somebody.
//
// # It is not the routing directory, and the difference is the whole design
//
// This is the ISSUER's record of what it gave out. What a bank routes by is a
// COPY of the clearing house's published roster, pulled by that bank and
// possibly behind. The two answer different questions — who issued this address,
// and may this address be reached — which is why they are two tables in two
// institutions rather than one table read twice, and why nothing in the domain
// can reach across from one to the other.
//
// # An allocation is never reassigned
//
// That is the invariant every subscriber's stale copy rests on. A directory that
// is behind is then INCOMPLETE and never WRONG: the failure mode is "I cannot
// route this yet", never "I routed it to the wrong bank". Nothing in this system
// may introduce a path that gives a code back to be issued again.
//
// The settlement agent standing in for four national registries is a fudge, and
// it is named where the table is: store/sqlite/schema/centralbank/0001_init.sql.
type BankCodeAllocation struct {
	// Issuer is the allocation itself: the country whose register it came out
	// of, and the code. Both, because a code is unique within one country and
	// nowhere else.
	Issuer iban.Issuer

	// BIC is the institution it was allocated to, and it is the only thing this
	// row says about that institution. A registry knows which code belongs to
	// whom; it does not hold the bank's name, its assets or its accounts — those
	// are on SettlementMember, one register over in the same database, keyed by
	// this same BIC and written by the same act.
	BIC iso20022.BIC

	AllocatedAt time.Time
}

// DirectoryEntry is one row of a MEMBER BANK's own copy of the scheme's routing
// directory: which institution answers for a bank code, and when that answer was
// last refreshed. It lives in that bank's database, one copy per member.
//
// It is the third of the three tables an address is routed through, and the only
// one a bank holds. BankCodeAllocation is the issuer's record of what it gave
// out; RosterEntry is the clearing house's published pairing; this is a snapshot
// of the second, pulled by a subscriber and used to derive a counterparty's agent
// from the counterparty's IBAN. SEPA is IBAN-only because every bank holds one of
// these, not because routing is computable from an address.
//
// # A copy, and therefore possibly behind
//
// RefreshDirectoryTx replaces the whole set — a directory is a file a subscriber
// downloads, not a delta feed — so between two refreshes this bank routes from
// what it was given last. A member admitted in between cannot be paid, and
// ErrBankCodeUnknown says so. That is the behaviour of every real routing
// directory rather than a defect being tolerated, and it is safe because an
// allocation is never reassigned: a copy that is behind is INCOMPLETE and never
// WRONG.
//
// Nothing holds it against the roster it came from. payment/recon reports the
// difference and passes.
type DirectoryEntry struct {
	// Issuer is the allocation this row resolves: the country, and the code.
	// Both, because a code is unique within one country and this copy holds
	// members in several.
	Issuer iban.Issuer

	// BIC is the institution to send to, and the whole of what this row says
	// about it.
	//
	// No name beside it, because the roster carries none, because the
	// acknowledgement the roster is written from carries none. That absence
	// arrives at the moment a payer most expects a name: an address resolves, and
	// what comes back is AURODEFFXXX.
	//
	// No assets either, and that one is a refusal deliberately not made — an
	// early "this member does not clear in euro", computed from a copy that may
	// be behind, would refuse a payment the clearing house would have taken. The
	// asset check belongs to whoever reads the live roster; see
	// Network.bothBanksAreMembersTx.
	BIC iso20022.BIC

	// RefreshedAt is when the snapshot this row came from was taken, and every
	// row of one refresh carries the same instant. It is what a console shows to
	// make the subscription visible — "14 banks, refreshed 3 days ago" — and it
	// is the only thing here that is about the COPY rather than about the member.
	RefreshedAt time.Time
}

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
// The settlement agent holds no roster and allocates no bank ids. What the
// request tells it is a BIC, so a BIC is what it can key by; a bank id here
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
// which of the two each institution writes its row from. The REQUEST names the
// applicant, so this institution is told one and uses it: the reserve account it
// opens is called "Reserve: <name> (<asset>)", which is what an account servicer
// names an account after. The ACKNOWLEDGEMENT names nobody, so the clearing
// house is told none — see RosterEntry.
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
// crossing the database split exists to close. storetest's
// RosterEntryCarriesNoAccountIdentifiers is what keeps that true: a field added
// here fails that case by name.
//
// Scheme membership follows the settlement account rather than the other way
// round: a bank the central bank will not open an account for is not a bank
// this clearing house can route a settlement instruction for. That is why
// AdmitMemberTx, the clearing house's act, writes this row from an
// acknowledgement it did not originate.
//
// # There is no NAME on it, because the acknowledgement that writes it has none
//
// An account servicer answering about an address it has already been told about
// has nothing to add to it: it names the accounts it holds, and no legal name, no
// country and no address. The REQUEST names the applicant and the answer does
// not. A name here would assert something nobody delivered.
//
// A bank's legal name lives where it was told to somebody: on the bank's own
// row, and on the settlement agent's SettlementMember, which learns it from the
// request and names the account it opens after it.
type RosterEntry struct {
	BIC iso20022.BIC

	// Issuer is the country and bank code this member issues its customers'
	// addresses under, learned from the same acknowledgement that writes this row.
	//
	// It is what makes this table a ROUTING DIRECTORY rather than a list of
	// addresses the scheme will talk to. A payer quotes an IBAN and nothing else;
	// the bank code inside it is a national registry's allocation with no
	// computable relationship to a BIC, so turning one into the other takes a
	// published pairing, and this is where this scheme publishes it. Every member
	// copies these rows and derives from its copy — see the routing directory in
	// store/sqlite/schema/bank/0001_init.sql.
	//
	// The clearing house refuses a second member on a code already here, which
	// is the settlement agent's refusal made again in another database. It is
	// belt-and-braces and it earns its place: this row is the one every member
	// COPIES, so a duplicate would make one address ambiguous for the whole
	// scheme, and this institution cannot see the registry to check.
	//
	// It carries no name beside it, for the reason this row carries none at all.
	// A member's copy of this table therefore answers a BIC and cannot answer
	// "Banca Verde", which is the documented absence arriving at the moment a
	// payer most expects a name.
	Issuer iban.Issuer

	// Assets is the assets this member clears in. A slice and not a map because
	// there is nothing to key it by: the clearing house holds no account per
	// asset, which is the difference between this row and SettlementMember
	// above.
	//
	// Its reader is Network.bothBanksAreMembersTx, from AcceptAtCSMTx: the
	// clearing house will not take a payment into a cycle unless both banks are
	// admitted in the scheme's asset. The case that makes it load-bearing is the
	// PARTLY-ADMITTED bank: one request asks for one currency, so a two-asset
	// admission commits twice, and an agent that answers one and refuses the
	// other leaves a bank with internal accounts in both assets and a settlement
	// account in one. Nothing else would refuse its payments in the
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

	// AdmissionRef is the identifier every act of ONE admission quotes, and the
	// only thing that tells two admissions on one address apart.
	//
	// Its reader is the clearing house's refusal, and the refusal cannot be
	// written without it. What legitimately arrives on a BIC already in this
	// roster is the same bank again — its second asset, since one request asks for
	// one currency, or an operator re-driving an interrupted admission. A refusal
	// keyed on "is this BIC in the roster" would refuse exactly those and never
	// fire on the second institution it exists for. Keyed on this field it
	// separates them: same admission, extend; different admission, refuse.
	//
	// AdmitMemberTx is what writes it and what reads it to refuse; what mints the
	// value is whoever composes the four acts.
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
	// So every caller supplies one. provision.Ref derives it from the BIC, which
	// makes provisioning one deployment twice quote the same reference and write
	// nothing new. It also means two DIFFERENT banks listed on one address quote
	// one reference and extend a single entry instead of the second being refused
	// — which is what provision.ErrAddressTaken is for, made against the
	// deployment's own spec before any act runs.
	AdmissionRef string

	// AdmittedAt is when the scheme admitted this bank, which is when this row
	// was written rather than when the bank was founded. A bank exists before it
	// joins one.
	AdmittedAt time.Time
}
