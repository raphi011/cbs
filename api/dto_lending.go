package api

import (
	"fmt"
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
type facilityDTO struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Asset       string `json:"asset"`
	PrincipalGL string `json:"principalGlAccount"`
	InterestGL  string `json:"interestGlAccount"`

	Commitment int64 `json:"commitment"`
	// Drawn and AccruedInterest are derived, not stored — the balances of the
	// two GL accounts.
	Drawn           int64 `json:"drawn"`
	AccruedInterest int64 `json:"accruedInterest"`
	Outstanding     int64 `json:"outstanding"`

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

// toFacilityDTO renders a facility. drawn and accrued are the current balances
// of its two GL accounts — DERIVED, not stored on the Facility — resolved by
// the caller (Portfolio.Drawn, Portfolio.AccruedInterest) so this function does
// no I/O of its own, the same convention toTransactionDTO follows for entry
// assets.
//
// Method is rendered only for a term loan: AmortMethod's zero value (Annuity)
// is indistinguishable from an explicitly-set one, and a revolving line — which
// has no amortization method — would otherwise render a misleading "Annuity".
func toFacilityDTO(f lending.Facility, drawn, accrued ledger.Amount) facilityDTO {
	outstanding := drawn
	if accrued > 0 {
		outstanding += accrued
	}

	dto := facilityDTO{
		ID:          string(f.ID),
		Kind:        f.Kind.String(),
		Name:        f.Name,
		Asset:       string(f.Asset),
		PrincipalGL: string(f.PrincipalGL),
		InterestGL:  string(f.InterestGL),

		Commitment: int64(f.Commitment),

		Drawn:           int64(drawn),
		AccruedInterest: int64(accrued),
		Outstanding:     int64(outstanding),

		Rate:      int64(f.Rate),
		RateScale: interest.RateScale,
		DayCount:  f.DayCount.String(),

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

type installmentDTO struct {
	Seq           int       `json:"seq"`
	DueDate       time.Time `json:"dueDate"`
	Principal     int64     `json:"principal"`
	Interest      int64     `json:"interest"`
	PaidPrincipal int64     `json:"paidPrincipal"`
	PaidInterest  int64     `json:"paidInterest"`
	Outstanding   int64     `json:"outstanding"`
}

func toInstallmentDTO(i lending.Installment) installmentDTO {
	return installmentDTO{
		Seq:           i.Seq,
		DueDate:       i.DueDate,
		Principal:     int64(i.Principal),
		Interest:      int64(i.Interest),
		PaidPrincipal: int64(i.PaidPrincipal),
		PaidInterest:  int64(i.PaidInterest),
		Outstanding:   int64(i.Outstanding()),
	}
}

// totalsDTO is a bank's customer-deposit position split by the sign of each
// balance, per asset. The overdrafts figure is DERIVED — see deposit.Totals —
// and no posting anywhere produces it.
type totalsDTO struct {
	Asset      string `json:"asset"`
	Deposits   int64  `json:"deposits"`
	Overdrafts int64  `json:"overdrafts"`
}

// toTotalsDTOs renders deposit.Totals sorted by asset code, so the response is
// deterministic — a map iteration would reorder it between requests.
func toTotalsDTOs(t deposit.Totals) []totalsDTO {
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

	out := make([]totalsDTO, len(codes))
	for i, code := range codes {
		asset := ledger.AssetCode(code)
		out[i] = totalsDTO{
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

// openFacilityRequest opens either product behind one route: Kind selects
// which, the same dispatch-by-field convention statusRequest uses for a
// deposit account's lifecycle actions. Method and TermMonths apply only to a
// TermLoan (and Method is required there — the handler parses it exactly as
// it parses Kind and DayCount, so an unknown or absent value is a 400); a
// RevolvingLine reads only MinPayment, and neither Method nor TermMonths.
type openFacilityRequest struct {
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

type disburseFacilityRequest struct {
	// Counterparty is any GL account in the facility's asset — a customer's
	// current account, or the vault for a cash advance.
	Counterparty string `json:"counterparty"`
	FirstDue     string `json:"firstDue"`
	Description  string `json:"description"`
}

type drawFacilityRequest struct {
	Counterparty string `json:"counterparty"`
	Amount       int64  `json:"amount"`
	Description  string `json:"description"`
}

// repayFacilityRequest is a repayment from a customer's deposit account — see
// handleRepay, the one handler that spans the deposit and lending layers.
type repayFacilityRequest struct {
	AccountID   string `json:"accountId"`
	Amount      int64  `json:"amount"`
	Date        string `json:"date"`
	Description string `json:"description"`
}

type chargeFacilityInterestRequest struct {
	Date string `json:"date"`
}

// endOfDayRequest is shared by POST /participants/{pid}/end-of-day.
type endOfDayRequest struct {
	Date string `json:"date"`
}

// ---------------------------------------------------------------------------
// Enum parsing
// ---------------------------------------------------------------------------

func facilityKindFromString(s string) (lending.FacilityKind, error) {
	switch s {
	case "TermLoan":
		return lending.TermLoan, nil
	case "RevolvingLine":
		return lending.RevolvingLine, nil
	default:
		return 0, fmt.Errorf("invalid facility kind %q (want TermLoan or RevolvingLine)", s)
	}
}

func amortMethodFromString(s string) (lending.AmortMethod, error) {
	switch s {
	case "Annuity":
		return lending.Annuity, nil
	case "EqualPrincipal":
		return lending.EqualPrincipal, nil
	default:
		return 0, fmt.Errorf("invalid amortization method %q (want Annuity or EqualPrincipal)", s)
	}
}

// dayCountFromString is shared by the lending and deposit handlers: both
// products are priced under the same DayCount conventions.
func dayCountFromString(s string) (interest.DayCount, error) {
	switch s {
	case "ACT/365":
		return interest.ACT365, nil
	case "ACT/360":
		return interest.ACT360, nil
	case "30/360":
		return interest.Thirty360, nil
	default:
		return 0, fmt.Errorf("invalid day-count convention %q (want ACT/365, ACT/360, or 30/360)", s)
	}
}
