package bank

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

func (s *surface) registerLendingRoutes(mux *api.Router) {
	mux.HandleFunc("POST /facilities", s.handleOpenFacility)
	mux.HandleFunc("GET /facilities", s.handleListFacilities)
	mux.HandleFunc("GET /facilities/{fid}", s.handleGetFacility)
	mux.HandleFunc("GET /facilities/{fid}/schedule", s.handleFacilitySchedule)
	mux.HandleFunc("POST /facilities/{fid}/disbursement", s.handleDisburse)
	mux.HandleFunc("POST /facilities/{fid}/draws", s.handleDraw)
	mux.HandleFunc("POST /facilities/{fid}/repayments", s.handleRepay)
	mux.HandleFunc("POST /facilities/{fid}/interest-charge", s.handleChargeInterest)
	mux.HandleFunc("POST /facilities/{fid}/interest-refunds", s.handleRefundInterest)
	mux.HandleFunc("DELETE /facilities/{fid}", s.handleCloseFacility)

	// Outstanding refunds are listed per BANK rather than per facility: the
	// question an operator has is "who do we still owe?", and a per-facility
	// route can only answer it by walking every facility. The per-facility
	// figure is already on FacilityDTO.
	mux.HandleFunc("GET /interest-refunds-payable", s.handleListRefundsPayable)

	mux.HandleFunc("POST /end-of-day", s.handleRunEndOfDay)
	mux.HandleFunc("GET /totals", s.handleTotals)
}

// handleOpenFacility opens a term loan or a revolving line, chosen by the
// request's Kind field — the same one-route-many-actions shape
// handleDepositStatus uses. Kind, DayCount and (for a term loan) Method are
// parsed from their string forms exactly as the ledger handlers parse an
// account type, so an unknown value is a 400 before the domain is ever called.
func (s *surface) handleOpenFacility(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.OpenFacilityRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	if req.Asset == "" {
		api.WriteBadRequest(w, "asset is required")
		return
	}
	kind, err := api.FacilityKindFromString(req.Kind)
	if err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	dc, err := api.DayCountFromString(req.DayCount)
	if err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}

	var f lending.Facility
	switch kind {
	case lending.TermLoan:
		method, err := api.AmortMethodFromString(req.Method)
		if err != nil {
			api.WriteBadRequest(w, err.Error())
			return
		}
		f, err = p.Lending.OpenTermLoan(r.Context(), req.Name, ledger.AssetCode(req.Asset),
			ledger.Amount(req.Commitment), interest.Rate(req.Rate), dc, method, req.TermMonths)
		if err != nil {
			api.WriteError(w, err)
			return
		}
	default: // RevolvingLine
		f, err = p.Lending.OpenRevolvingLine(r.Context(), req.Name, ledger.AssetCode(req.Asset),
			ledger.Amount(req.Commitment), interest.Rate(req.Rate), dc, interest.Fraction(req.MinPayment))
		if err != nil {
			api.WriteError(w, err)
			return
		}
	}

	// A freshly-opened facility is Pending: nothing has been advanced, nothing
	// has accrued and no correction can have overshot, so all three derived
	// figures are 0 without a further round trip to read the balances back.
	//
	// The rate reaches the response through the facility's opening terms row
	// rather than through the value returned above: it is a terms field now, and
	// reading it back is what keeps the created body and a later GET the same
	// shape.
	withTerms, err := p.Lending.GetFacilityWithTerms(r.Context(), f.ID)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	at, err := p.Lending.Positions(r.Context(), f.ID)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToFacilityDTO(withTerms.Facility, withTerms.Terms, at, 0, 0, 0))
}

func (s *surface) handleListFacilities(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	// ListFacilitiesWithTerms rather than ListFacilities: the rate is resolved
	// per facility from its timeline, and one unit of work resolves the whole
	// listing — resolving each facility through its own View would make a listing
	// N units of work over a store that refuses to nest them at all.
	facilities, err := p.Lending.ListFacilitiesWithTerms(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.FacilityDTO, len(facilities))
	for i, withTerms := range facilities {
		f := withTerms.Facility
		drawn, err := p.Lending.Drawn(r.Context(), f.ID)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		refund, err := p.Lending.RefundPayableFor(r.Context(), f.ID)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		// Accrued interest comes off the facility already in hand rather than
		// from Portfolio.AccruedInterest, which would re-read the same row in
		// its own store transaction to return exactly this. Drawn cannot be
		// had that way — it is the principal account's book balance, which is
		// not on the record at all, and nor can the refund payable.
		at, err := p.Lending.Positions(r.Context(), f.ID)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out[i] = api.ToFacilityDTO(f, withTerms.Terms, at, drawn, f.Accrued.Minor(), refund)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleGetFacility(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	withTerms, err := p.Lending.GetFacilityWithTerms(r.Context(), fid)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	f := withTerms.Facility
	drawn, err := p.Lending.Drawn(r.Context(), fid)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	refund, err := p.Lending.RefundPayableFor(r.Context(), fid)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	// f.Accrued.Minor() rather than Portfolio.AccruedInterest, which would
	// re-read the row already in hand — see handleListFacilities.
	at, err := p.Lending.Positions(r.Context(), fid)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToFacilityDTO(f, withTerms.Terms, at, drawn, f.Accrued.Minor(), refund))
}

func (s *surface) handleFacilitySchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	schedule, err := p.Lending.Schedule(r.Context(), lending.FacilityID(r.PathValue("fid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.InstallmentDTO, len(schedule))
	for i, inst := range schedule {
		out[i] = api.ToInstallmentDTO(inst)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleDisburse(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.DisburseFacilityRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	firstDue, err := time.Parse("2006-01-02", req.FirstDue)
	if err != nil {
		api.WriteBadRequest(w, "invalid firstDue (want YYYY-MM-DD)")
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	tx, err := p.Lending.Disburse(r.Context(), fid, ledger.AccountID(req.Counterparty).For(req.Subsidiary), firstDue, req.Description)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	assets, err := api.EntryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToTransactionDTO(tx, assets))
}

func (s *surface) handleDraw(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.DrawFacilityRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	tx, err := p.Lending.Draw(r.Context(), fid, ledger.AccountID(req.Counterparty).For(req.Subsidiary), ledger.Amount(req.Amount), req.Description)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	assets, err := api.EntryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToTransactionDTO(tx, assets))
}

// handleRepay applies a repayment from a customer's deposit account.
//
// It is the orchestration point the lending package deliberately does not have:
// lending takes a generic counterparty and knows nothing about deposit account
// status or available balance, so the check and the posting are driven here,
// through ONE Tx. Calling the two plain methods in sequence would let a
// repayment post after the funds check had already passed against a balance
// another request had since spent.
func (s *surface) handleRepay(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.RepayFacilityRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		api.WriteBadRequest(w, "invalid date (want YYYY-MM-DD)")
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))

	var out ledger.Transaction
	err = p.Deposit.Store().Update(r.Context(), func(ctx context.Context, tx deposit.Tx) error {
		acct, err := tx.GetDepositAccount(ctx, p.BookID, deposit.AccountID(req.AccountID))
		if err != nil {
			return err
		}
		if err := p.Deposit.CheckWithdrawalTx(ctx, tx, acct.ID, ledger.Amount(req.Amount)); err != nil {
			return err
		}
		lendingTx, ok := tx.(lending.Tx)
		if !ok {
			return fmt.Errorf("api: store transaction does not span the lending layer")
		}
		from, err := p.Deposit.PositionTx(ctx, tx, acct.ID)
		if err != nil {
			return err
		}
		out, err = p.Lending.RepayTx(ctx, lendingTx, fid, from,
			ledger.Amount(req.Amount), date, req.Description)
		return err
	})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	assets, err := api.EntryAssets(r.Context(), p.Ledger, []ledger.Transaction{out})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToTransactionDTO(out, assets))
}

// handleChargeInterest closes a revolving line's billing cycle. It returns
// ErrWrongFacilityKind for a term loan, which settles interest through its
// scheduled instalments instead, and 409 for a cycle already billed — see
// lending.Portfolio.ChargeInterest.
//
// The response is a ChargeDTO rather than a transaction, because a cycle can
// bill an instalment without posting anything: reachable by drawing and
// charging before the accrual has ticked a whole minor unit. Nothing accrued
// AND nothing drawn does neither, and that alone is 204 No Content — the same
// answer the deposit layer's charge endpoint gives, and what the rest of this
// API already uses for "the request was fine and there is nothing to say".
func (s *surface) handleChargeInterest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.ChargeFacilityInterestRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		api.WriteBadRequest(w, "invalid date (want YYYY-MM-DD)")
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	charge, err := p.Lending.ChargeInterest(r.Context(), fid, date)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if !charge.Billed() && !charge.Posted() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var assets map[ledger.AccountID]ledger.AssetCode
	if charge.Posted() {
		assets, err = api.EntryAssets(r.Context(), p.Ledger, []ledger.Transaction{charge.Transaction})
		if err != nil {
			api.WriteError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, api.ToChargeDTO(charge, assets))
}

// handleRefundInterest pays a borrower back interest the bank charged and never
// earned — the discharge half of what a backdated correction records. See
// lending.Portfolio.RefundInterest.
//
// It returns 422 for a facility the bank owes nothing on
// (ErrNoRefundOutstanding — the same status as its mirror
// ErrNothingOutstanding) and 400 for an amount that is not positive or that
// exceeds what is owed. It does NOT refuse a closed facility: the lending
// contract ending says nothing about a debt running the other way.
func (s *surface) handleRefundInterest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.RefundFacilityInterestRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		api.WriteBadRequest(w, "invalid date (want YYYY-MM-DD)")
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	tx, err := p.Lending.RefundInterest(r.Context(), fid, ledger.AccountID(req.Counterparty).For(req.Subsidiary),
		ledger.Amount(req.Amount), date, req.Description)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	assets, err := api.EntryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToTransactionDTO(tx, assets))
}

// handleListRefundsPayable serves GET
// /participants/{pid}/interest-refunds-payable: every borrower this bank still
// owes interest back to. An empty array is the ordinary answer.
//
// Closed facilities appear — see lending.ListRefundsPayable for why leaving them
// out would hide exactly the obligations nothing else surfaces.
func (s *surface) handleListRefundsPayable(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	payables, err := p.Lending.ListRefundsPayable(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.RefundPayableDTO, len(payables))
	for i, rp := range payables {
		out[i] = api.ToRefundPayableDTO(rp)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleCloseFacility(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	if err := p.Lending.Close(r.Context(), lending.FacilityID(r.PathValue("fid"))); err != nil {
		api.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRunEndOfDay calls Bank.RunEndOfDay and nothing else.
//
// It deliberately does not expose the deposit and lending batches separately:
// they run in one unit of work so that a bank cannot end up with a day of
// interest on its loans and none on its overdrafts, and a route per batch would
// hand a client exactly that.
func (s *surface) handleRunEndOfDay(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.EndOfDayRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		api.WriteBadRequest(w, "invalid date (want YYYY-MM-DD)")
		return
	}
	if err := p.RunEndOfDay(r.Context(), date); err != nil {
		api.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTotals serves GET /participants/{pid}/totals: the bank's
// customer-deposit position split into deposits and (derived) overdrafts, per
// asset. See deposit.Totals and TotalsDTO.
func (s *surface) handleTotals(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	totals, err := p.Deposit.Totals(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToTotalsDTOs(totals))
}
