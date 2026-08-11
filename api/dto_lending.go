package api

import (
	"sort"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// Wire format for the lending layer: facilities, their schedules, and their
// requests.
//
// Rates cross the wire as their millionths integer — 150000 is 15% — and carry
// `RateScale` alongside, for the same reason a balance carries its asset: an
// integer whose scale a client has to know from documentation is an integer a
// client will render wrong. Amounts follow the existing convention and are the
// asset's minor units.
//
// Rate and DayCount are RESOLVED AS OF TODAY from the facility's effective-dated
// terms timeline rather than read off the facility row — the facility has no such
// columns any more. So this is what the credit costs today and nothing else: a
// future-dated repricing is not visible here until it takes effect, and a past
// one is not visible here at all. lending.Portfolio.FacilityTermsHistory is what
// shows the whole timeline; there is no HTTP route onto it yet.
type FacilityDTO struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Asset string `json:"asset"`

	// The three control accounts this facility's money sits in: what it has
	// drawn, what it owes in interest, and what the bank owes it back. The
	// subsidiary under each is this facility's own ID, above, which is why there
	// is no fourth field for it — see lending.FacilityPositions.
	PrincipalAccount string `json:"principalAccount"`
	InterestAccount  string `json:"interestAccount"`
	RefundAccount    string `json:"refundAccount"`

	Commitment int64 `json:"commitment"`
	// Drawn and AccruedInterest are derived, not stored: Drawn is the balance of
	// the principal control account under this facility; AccruedInterest is
	// Minor() of the facility's own stored accrued figure, which that
	// facility's share of the receivable always equals but is not read from.
	Drawn           int64 `json:"drawn"`
	AccruedInterest int64 `json:"accruedInterest"`
	Outstanding     int64 `json:"outstanding"`
	// RefundPayable is interest the bank owes THIS borrower back, and it is not
	// part of Outstanding: it runs the other way. Outstanding is what the
	// borrower owes the bank, and netting a debt the bank owes into it would
	// report a smaller loan rather than an obligation.
	//
	// Derived, like Drawn: the book balance of RefundGL, and 0 when there is no
	// such account. See lending.RefundPayableFor.
	RefundPayable int64 `json:"refundPayable"`

	Rate       int64  `json:"rate"`
	RateScale  int64  `json:"rateScale"`
	DayCount   string `json:"dayCount"`
	Method     string `json:"method,omitempty"`
	TermMonths int    `json:"termMonths,omitempty"`
	MinPayment int64  `json:"minPayment,omitempty"`

	DaysPastDue   int    `json:"daysPastDue"`
	ArrearsBucket string `json:"arrearsBucket"`
	NonPerforming bool   `json:"nonPerforming"`

	Status     string    `json:"status"`
	OpenedAt   time.Time `json:"openedAt"`
	MaturityAt time.Time `json:"maturityAt,omitempty"`
}

// ToFacilityDTO renders a facility. t is the terms in force today, and drawn,
// accrued and refund are resolved by the caller so this function does no I/O of
// its own, the same convention ToTransactionDTO follows for entry assets: drawn
// is the principal GL account's book balance (Portfolio.Drawn), genuinely
// derived; accrued is f.Accrued.Minor(), the facility's own stored figure, not a
// GL read — it only agrees with the interest GL account's balance because the
// system maintains that as an invariant; refund is the refunds-payable account's
// balance (Portfolio.RefundPayableFor), 0 when there is no such account.
//
// Method is rendered only for a term loan: AmortMethod's zero value (Annuity)
// is indistinguishable from an explicitly-set one, and a revolving line — which
// has no amortization method — would otherwise render a misleading "Annuity".
func ToFacilityDTO(f lending.Facility, t lending.FacilityTerms, at lending.FacilityPositions, drawn, accrued, refund ledger.Amount) FacilityDTO {
	dto := FacilityDTO{
		ID:    string(f.ID),
		Kind:  f.Kind.String(),
		Name:  f.Name,
		Asset: string(f.Asset),

		PrincipalAccount: string(at.Principal.Account),
		InterestAccount:  string(at.Receivable.Account),
		RefundAccount:    string(at.Payable.Account),

		Commitment: int64(f.Commitment),

		Drawn:           int64(drawn),
		AccruedInterest: int64(accrued),
		Outstanding:     int64(lending.OutstandingOf(drawn, accrued)),
		RefundPayable:   int64(refund),

		Rate:      int64(t.Rate),
		RateScale: interest.RateScale,
		DayCount:  t.DayCount.String(),

		DaysPastDue:   f.Arrears.DaysPastDue,
		ArrearsBucket: f.Arrears.Bucket.String(),
		NonPerforming: f.Arrears.NonPerforming,

		Status:     f.Status.String(),
		OpenedAt:   f.OpenedAt,
		MaturityAt: f.MaturityAt,
	}

	switch f.Kind {
	case lending.TermLoan:
		dto.Method = f.Method.String()
		dto.TermMonths = f.TermMonths
	case lending.RevolvingLine:
		dto.MinPayment = int64(f.MinPayment)
	}

	return dto
}

// ChargeDTO is the outcome of billing one of a revolving line's cycles, and it
// carries BOTH halves because they are independent — see lending.Charge.
//
// A cycle whose accrued interest has not yet reached a whole minor unit posts
// no transaction and still bills an instalment; a cycle on an undrawn line that
// owes nothing does neither. A bare transaction cannot express the difference,
// so a client rendering one would report "nothing happened" while the schedule
// below it gained a row.
//
// Both fields are pointers rather than zero values: an absent posting is
// genuinely absent, and a `transaction` rendered with an empty id would read as
// a real posting the client failed to parse.
type ChargeDTO struct {
	// Transaction is the capitalization posting, absent when nothing posted.
	Transaction *TransactionDTO `json:"transaction,omitempty"`
	// Installment is the cycle that was billed, absent when no cycle was.
	Installment *InstallmentDTO `json:"installment,omitempty"`
}

// ToChargeDTO renders a charge. assets is the pre-resolved account-to-asset map
// the transaction half needs (see EntryAssets), so this does no I/O of its own,
// the same convention ToTransactionDTO and ToFacilityDTO follow.
func ToChargeDTO(c lending.Charge, assets map[ledger.AccountID]ledger.AssetCode) ChargeDTO {
	var out ChargeDTO
	if c.Posted() {
		tx := ToTransactionDTO(c.Transaction, assets)
		out.Transaction = &tx
	}
	if c.Billed() {
		inst := ToInstallmentDTO(c.Installment)
		out.Installment = &inst
	}
	return out
}

type InstallmentDTO struct {
	Seq           int       `json:"seq"`
	DueDate       time.Time `json:"dueDate"`
	Principal     int64     `json:"principal"`
	Interest      int64     `json:"interest"`
	PaidPrincipal int64     `json:"paidPrincipal"`
	PaidInterest  int64     `json:"paidInterest"`
	Outstanding   int64     `json:"outstanding"`
}

func ToInstallmentDTO(i lending.Installment) InstallmentDTO {
	return InstallmentDTO{
		Seq:           i.Seq,
		DueDate:       i.DueDate,
		Principal:     int64(i.Principal),
		Interest:      int64(i.Interest),
		PaidPrincipal: int64(i.PaidPrincipal),
		PaidInterest:  int64(i.PaidInterest),
		Outstanding:   int64(i.Outstanding()),
	}
}

// TotalsDTO is a bank's customer-deposit position split by the sign of each
// balance, per asset. The overdrafts figure is DERIVED — see deposit.Totals —
// and no posting anywhere produces it.
type TotalsDTO struct {
	Asset      string `json:"asset"`
	Deposits   int64  `json:"deposits"`
	Overdrafts int64  `json:"overdrafts"`
}

// ToTotalsDTOs renders deposit.Totals sorted by asset code, so the response is
// deterministic — a map iteration would reorder it between requests.
func ToTotalsDTOs(t deposit.Totals) []TotalsDTO {
	seen := make(map[ledger.AssetCode]bool, len(t.Deposits)+len(t.Overdrafts))
	for asset := range t.Deposits {
		seen[asset] = true
	}
	for asset := range t.Overdrafts {
		seen[asset] = true
	}
	codes := make([]string, 0, len(seen))
	for asset := range seen {
		codes = append(codes, string(asset))
	}
	sort.Strings(codes)

	out := make([]TotalsDTO, len(codes))
	for i, code := range codes {
		asset := ledger.AssetCode(code)
		out[i] = TotalsDTO{
			Asset:      code,
			Deposits:   int64(t.Deposits[asset]),
			Overdrafts: int64(t.Overdrafts[asset]),
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

// OpenFacilityRequest opens either product behind one route: Kind selects
// which, the same dispatch-by-field convention StatusRequest uses for a
// deposit account's lifecycle actions. Method and TermMonths apply only to a
// TermLoan (and Method is required there — the handler parses it exactly as
// it parses Kind and DayCount, so an unknown or absent value is a 400); a
// RevolvingLine reads only MinPayment, and neither Method nor TermMonths.
type OpenFacilityRequest struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Asset      string `json:"asset"`
	Commitment int64  `json:"commitment"`
	Rate       int64  `json:"rate"`
	DayCount   string `json:"dayCount"`
	Method     string `json:"method,omitempty"`
	TermMonths int    `json:"termMonths,omitempty"`
	MinPayment int64  `json:"minPayment,omitempty"`
}

type DisburseFacilityRequest struct {
	// Counterparty is any account in the facility's asset — the control account
	// a customer's current account is pooled in, or the vault for a cash
	// advance — and Subsidiary is which one within it, a deposit account's id on
	// the first and empty on the second. The two travel together everywhere
	// money moves; see CaptureHoldRequest, where the same pair is documented.
	Counterparty string `json:"counterparty"`
	Subsidiary   string `json:"subsidiary,omitempty"`
	FirstDue     string `json:"firstDue"`
	Description  string `json:"description"`
}

type DrawFacilityRequest struct {
	Counterparty string `json:"counterparty"`
	Subsidiary   string `json:"subsidiary,omitempty"`
	Amount       int64  `json:"amount"`
	Description  string `json:"description"`
}

// RepayFacilityRequest is a repayment from a customer's deposit account — see
// handleRepay, the one handler that spans the deposit and lending layers.
type RepayFacilityRequest struct {
	AccountID   string `json:"accountId"`
	Amount      int64  `json:"amount"`
	Date        string `json:"date"`
	Description string `json:"description"`
}

type ChargeFacilityInterestRequest struct {
	Date string `json:"date"`
}

// RefundPayableDTO is one outstanding interest refund: what the bank owes one
// borrower back because a backdated correction showed it charged interest the
// borrower had already paid and never owed. See lending.RefundPayable.
//
// FacilityStatus is rendered because it may be `Closed` and a client should not
// read that as stale data. A refund outlives the lending contract — closing a
// facility is a statement about what the BORROWER owes — so a settled loan with
// an outstanding refund is an ordinary row here, not a contradiction.
type RefundPayableDTO struct {
	FacilityID     string `json:"facilityId"`
	Name           string `json:"name"`
	Asset          string `json:"asset"`
	Account        string `json:"account"`
	Amount         int64  `json:"amount"`
	FacilityStatus string `json:"facilityStatus"`
}

func ToRefundPayableDTO(r lending.RefundPayable) RefundPayableDTO {
	return RefundPayableDTO{
		FacilityID:     string(r.FacilityID),
		Name:           r.Name,
		Asset:          string(r.Asset),
		Account:        string(r.Account),
		Amount:         int64(r.Amount),
		FacilityStatus: r.FacilityStatus.String(),
	}
}

// RefundFacilityInterestRequest pays an interest refund out to a GL account.
//
// Counterparty is a GL account rather than a deposit account id, which is what
// DisburseFacilityRequest and DrawFacilityRequest also take and the opposite of
// RepayFacilityRequest. The asymmetry is real: a repayment has to be checked
// against a deposit account's available balance and status before it can post,
// so that route spans both layers; money going OUT to the customer has no such
// check to make, and the lending layer does not know what a deposit account is.
//
// Amount is required and bounded by what is owed — a partial refund is fine, an
// over-refund is a 400. There is no "pay it all" default: an amount the caller
// did not state is an amount the caller did not check.
type RefundFacilityInterestRequest struct {
	Counterparty string `json:"counterparty"`
	Subsidiary   string `json:"subsidiary,omitempty"`
	Amount       int64  `json:"amount"`
	Date         string `json:"date"`
	Description  string `json:"description"`
}

// EndOfDayRequest is shared by POST /participants/{pid}/end-of-day.
type EndOfDayRequest struct {
	Date string `json:"date"`
}

// ---------------------------------------------------------------------------
// Enum parsing
// ---------------------------------------------------------------------------

func FacilityKindFromString(s string) (lending.FacilityKind, error) {
	switch s {
	case "TermLoan":
		return lending.TermLoan, nil
	case "RevolvingLine":
		return lending.RevolvingLine, nil
	default:
		return 0, BadRequest("invalid facility kind %q (want TermLoan or RevolvingLine)", s)
	}
}

func AmortMethodFromString(s string) (lending.AmortMethod, error) {
	switch s {
	case "Annuity":
		return lending.Annuity, nil
	case "EqualPrincipal":
		return lending.EqualPrincipal, nil
	default:
		return 0, BadRequest("invalid amortization method %q (want Annuity or EqualPrincipal)", s)
	}
}

// DayCountFromString is shared by the lending and deposit handlers: both
// products are priced under the same DayCount conventions.
func DayCountFromString(s string) (interest.DayCount, error) {
	switch s {
	case "ACT/365":
		return interest.ACT365, nil
	case "ACT/360":
		return interest.ACT360, nil
	case "30/360":
		return interest.Thirty360, nil
	default:
		return 0, BadRequest("invalid day-count convention %q (want ACT/365, ACT/360, or 30/360)", s)
	}
}
