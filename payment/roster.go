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
type BankCodeAllocation struct {
	// Issuer is the allocation itself: the country whose register it came out
	// of, and the code. Both, because a code is unique within one country and
	// nowhere else.
	Issuer iban.Issuer

	// BIC is the institution it was allocated to, and it is the only thing this
	// row says about that institution.
	BIC iso20022.BIC

	AllocatedAt time.Time
}

// DirectoryEntry is one row of a MEMBER BANK's own copy of the scheme's routing
// directory: which institution answers for a bank code, and when that answer
// was last refreshed. It lives in that bank's database, one copy per member.
type DirectoryEntry struct {
	// Issuer is the allocation this row resolves: the country, and the code.
	// Both, because a code is unique within one country and this copy holds
	// members in several.
	Issuer iban.Issuer

	// BIC is the institution to send to, and the whole of what this row says about
	// it.
	BIC iso20022.BIC

	// RefreshedAt is when the snapshot this row came from was taken, and every row
	// of one refresh carries the same instant.
	RefreshedAt time.Time
}

// SettlementMember is the CENTRAL BANK's own record of a bank it holds a
// settlement account for.
type SettlementMember struct {
	// BIC is the key. See the note above on why it is the only one this
	// institution could have.
	BIC  iso20022.BIC
	Name string

	// Accounts is this member's settlement account per asset, in the CENTRAL
	// BANK's own book.
	Accounts map[ledger.AssetCode]ledger.AccountID

	// OpenedAt is when this institution opened the accounts, which is not
	// necessarily when the scheme admitted the bank: the clearing house writes its
	// own row from the acknowledgement this act produces, so its timestamp is the
	// later of the two.
	OpenedAt time.Time
}

// RosterEntry is the CLEARING HOUSE's record of one member: where to send a
// message addressed to it. It lives in the clearing house's database and in no
// other.
type RosterEntry struct {
	BIC iso20022.BIC `json:"bic"`

	// Issuer is the country and bank code this member issues its customers'
	// addresses under, learned from the same acknowledgement that writes this row.
	Issuer iban.Issuer `json:"issuer"`

	// Assets is the assets this member clears in. A slice and not a map because
	// there is nothing to key it by: the clearing house holds no account per
	// asset, which is the difference between this row and SettlementMember above.
	Assets []ledger.AssetCode `json:"assets"`

	// AdmissionRef is the identifier every act of ONE admission quotes, and the
	// only thing that tells two admissions on one address apart.
	AdmissionRef string `json:"admissionRef"`

	// AdmittedAt is when the scheme admitted this bank, which is when this row
	// was written rather than when the bank was founded. A bank exists before it
	// joins one.
	AdmittedAt time.Time `json:"admittedAt"`
}
