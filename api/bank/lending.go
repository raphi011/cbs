package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
)

func (s *surface) registerLendingRoutes(mux *api.Router) {
	mux.HandleFunc("POST /facilities", handleBody(s, http.StatusCreated, s.handleOpenFacility))
	mux.HandleFunc("GET /facilities", handle(s, http.StatusOK, s.handleListFacilities))
	mux.HandleFunc("GET /facilities/{fid}", handle(s, http.StatusOK, s.handleGetFacility))
	mux.HandleFunc("GET /facilities/{fid}/schedule", handle(s, http.StatusOK, s.handleFacilitySchedule))
	mux.HandleFunc("POST /facilities/{fid}/disbursement", handleBody(s, http.StatusCreated, s.handleDisburse))
	mux.HandleFunc("POST /facilities/{fid}/draws", handleBody(s, http.StatusCreated, s.handleDraw))
	mux.HandleFunc("POST /facilities/{fid}/repayments", handleBody(s, http.StatusCreated, s.handleRepay))
	mux.HandleFunc("POST /facilities/{fid}/interest-charge", handleBody(s, http.StatusOK, s.handleChargeInterest))
	mux.HandleFunc("POST /facilities/{fid}/interest-refunds", handleBody(s, http.StatusCreated, s.handleRefundInterest))
	mux.HandleFunc("DELETE /facilities/{fid}", handle(s, http.StatusNoContent, s.handleCloseFacility))

	// Outstanding refunds are listed per BANK rather than per facility: the
	// question an operator has is "who do we still owe?", and a per-facility route
	// can only answer it by walking every facility.
	mux.HandleFunc("GET /interest-refunds-payable", handle(s, http.StatusOK, s.handleListRefundsPayable))

	mux.HandleFunc("POST /end-of-day", handleBody(s, http.StatusNoContent, s.handleRunEndOfDay))
	mux.HandleFunc("GET /totals", handle(s, http.StatusOK, s.handleTotals))
}

// handleOpenFacility opens a term loan or a revolving line, chosen by the
// request's Kind field — the same one-route-many-actions shape
// handleDepositStatus uses.
func (s *surface) handleOpenFacility(r *http.Request, p *payment.Bank, req api.OpenFacilityRequest) (api.FacilityDTO, error) {
	if req.Asset == "" {
		return api.FacilityDTO{}, api.BadRequest("asset is required")
	}
	kind, err := api.FacilityKindFromString(req.Kind)
	if err != nil {
		return api.FacilityDTO{}, err
	}
	dc, err := api.DayCountFromString(req.DayCount)
	if err != nil {
		return api.FacilityDTO{}, err
	}

	var f lending.Facility
	switch kind {
	case lending.TermLoan:
		method, err := api.AmortMethodFromString(req.Method)
		if err != nil {
			return api.FacilityDTO{}, err
		}
		f, err = p.Lending.OpenTermLoan(r.Context(), req.Name, ledger.AssetCode(req.Asset),
			ledger.Amount(req.Commitment), interest.Rate(req.Rate), dc, method, req.TermMonths)
		if err != nil {
			return api.FacilityDTO{}, err
		}
	default: // RevolvingLine
		f, err = p.Lending.OpenRevolvingLine(r.Context(), req.Name, ledger.AssetCode(req.Asset),
			ledger.Amount(req.Commitment), interest.Rate(req.Rate), dc, interest.Fraction(req.MinPayment))
		if err != nil {
			return api.FacilityDTO{}, err
		}
	}

	// A freshly-opened facility is Pending: nothing has been advanced, nothing has
	// accrued and no correction can have overshot, so all three derived figures
	// are 0 without a further round trip to read the balances back.
	withTerms, err := p.Lending.GetFacilityWithTerms(r.Context(), f.ID)
	if err != nil {
		return api.FacilityDTO{}, err
	}
	at, err := p.Lending.Positions(r.Context(), f.ID)
	if err != nil {
		return api.FacilityDTO{}, err
	}
	return api.ToFacilityDTO(withTerms.Facility, withTerms.Terms, at, 0, 0, 0), nil
}

func (s *surface) handleListFacilities(r *http.Request, p *payment.Bank) ([]api.FacilityDTO, error) {
	// ListFacilitiesWithTerms rather than ListFacilities: the rate is resolved per
	// facility from its timeline, and one unit of work resolves the whole listing
	// — resolving each facility through its own View would make a listing N units
	// of work over a store that refuses to nest them at all.
	facilities, err := p.Lending.ListFacilitiesWithTerms(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.FacilityDTO, len(facilities))
	for i, withTerms := range facilities {
		out[i], err = facilityDTO(r, p, withTerms)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *surface) handleGetFacility(r *http.Request, p *payment.Bank) (api.FacilityDTO, error) {
	withTerms, err := p.Lending.GetFacilityWithTerms(r.Context(), lending.FacilityID(r.PathValue("fid")))
	if err != nil {
		return api.FacilityDTO{}, err
	}
	return facilityDTO(r, p, withTerms)
}

// facilityDTO resolves the three figures a facility's wire shape carries and is
// not stored with: what is drawn, what the bank owes back, and where the three
// accounts sit.
func facilityDTO(r *http.Request, p *payment.Bank, withTerms lending.FacilityWithTerms) (api.FacilityDTO, error) {
	f := withTerms.Facility
	drawn, err := p.Lending.Drawn(r.Context(), f.ID)
	if err != nil {
		return api.FacilityDTO{}, err
	}
	refund, err := p.Lending.RefundPayableFor(r.Context(), f.ID)
	if err != nil {
		return api.FacilityDTO{}, err
	}
	at, err := p.Lending.Positions(r.Context(), f.ID)
	if err != nil {
		return api.FacilityDTO{}, err
	}
	return api.ToFacilityDTO(f, withTerms.Terms, at, drawn, f.Accrued.Minor(), refund), nil
}

func (s *surface) handleFacilitySchedule(r *http.Request, p *payment.Bank) ([]api.InstallmentDTO, error) {
	schedule, err := p.Lending.Schedule(r.Context(), lending.FacilityID(r.PathValue("fid")))
	if err != nil {
		return nil, err
	}
	out := make([]api.InstallmentDTO, len(schedule))
	for i, inst := range schedule {
		out[i] = api.ToInstallmentDTO(inst)
	}
	return out, nil
}

func (s *surface) handleDisburse(r *http.Request, p *payment.Bank, req api.DisburseFacilityRequest) (api.TransactionDTO, error) {
	firstDue, err := api.ParseDay("firstDue", req.FirstDue)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	tx, err := p.Lending.Disburse(r.Context(), lending.FacilityID(r.PathValue("fid")),
		ledger.AccountID(req.Counterparty).For(req.Subsidiary), firstDue, req.Description)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, tx)
}

func (s *surface) handleDraw(r *http.Request, p *payment.Bank, req api.DrawFacilityRequest) (api.TransactionDTO, error) {
	tx, err := p.Lending.Draw(r.Context(), lending.FacilityID(r.PathValue("fid")),
		ledger.AccountID(req.Counterparty).For(req.Subsidiary), ledger.Amount(req.Amount), req.Description)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, tx)
}

// handleRepay applies a repayment from a customer's deposit account. The act
// spans the deposit and lending layers, so it belongs to the bank — see
// payment.Bank.Repay, which owns the unit of work both need.
func (s *surface) handleRepay(r *http.Request, p *payment.Bank, req api.RepayFacilityRequest) (api.TransactionDTO, error) {
	date, err := api.ParseDay("date", req.Date)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	out, err := p.Repay(r.Context(), lending.FacilityID(r.PathValue("fid")),
		deposit.AccountID(req.AccountID), ledger.Amount(req.Amount), date, req.Description)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, out)
}

// handleChargeInterest closes a revolving line's billing cycle.
func (s *surface) handleChargeInterest(r *http.Request, p *payment.Bank, req api.ChargeFacilityInterestRequest) (any, error) {
	date, err := api.ParseDay("date", req.Date)
	if err != nil {
		return nil, err
	}
	charge, err := p.Lending.ChargeInterest(r.Context(), lending.FacilityID(r.PathValue("fid")), date)
	if err != nil {
		return nil, err
	}
	if !charge.Billed() && !charge.Posted() {
		return nil, nil
	}
	return api.ResolveChargeDTO(r.Context(), p.Ledger, charge)
}

// handleRefundInterest pays a borrower back interest the bank charged and never
// earned — the discharge half of what a backdated correction records. See
// lending.Portfolio.RefundInterest.
func (s *surface) handleRefundInterest(r *http.Request, p *payment.Bank, req api.RefundFacilityInterestRequest) (api.TransactionDTO, error) {
	date, err := api.ParseDay("date", req.Date)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	tx, err := p.Lending.RefundInterest(r.Context(), lending.FacilityID(r.PathValue("fid")),
		ledger.AccountID(req.Counterparty).For(req.Subsidiary), ledger.Amount(req.Amount), date, req.Description)
	if err != nil {
		return api.TransactionDTO{}, err
	}
	return api.ResolveTransactionDTO(r.Context(), p.Ledger, tx)
}

// handleListRefundsPayable serves GET
// /participants/{pid}/interest-refunds-payable: every borrower this bank still
// owes interest back to. An empty array is the ordinary answer.
func (s *surface) handleListRefundsPayable(r *http.Request, p *payment.Bank) ([]api.RefundPayableDTO, error) {
	payables, err := p.Lending.ListRefundsPayable(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.RefundPayableDTO, len(payables))
	for i, rp := range payables {
		out[i] = api.ToRefundPayableDTO(rp)
	}
	return out, nil
}

func (s *surface) handleCloseFacility(r *http.Request, p *payment.Bank) (any, error) {
	return nil, p.Lending.Close(r.Context(), lending.FacilityID(r.PathValue("fid")))
}

// handleRunEndOfDay calls Bank.RunEndOfDay and nothing else.
func (s *surface) handleRunEndOfDay(r *http.Request, p *payment.Bank, req api.EndOfDayRequest) (any, error) {
	date, err := api.ParseDay("date", req.Date)
	if err != nil {
		return nil, err
	}
	return nil, p.RunEndOfDay(r.Context(), date)
}

// handleTotals serves GET /participants/{pid}/totals: the bank's
// customer-deposit position split into deposits and (derived) overdrafts, per
// asset. See deposit.Totals and TotalsDTO.
func (s *surface) handleTotals(r *http.Request, p *payment.Bank) ([]api.TotalsDTO, error) {
	totals, err := p.Deposit.Totals(r.Context())
	if err != nil {
		return nil, err
	}
	return api.ToTotalsDTOs(totals), nil
}
