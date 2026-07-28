package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

func (s *Server) registerLendingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /participants/{pid}/facilities", s.handleOpenFacility)
	mux.HandleFunc("GET /participants/{pid}/facilities", s.handleListFacilities)
	mux.HandleFunc("GET /participants/{pid}/facilities/{fid}", s.handleGetFacility)
	mux.HandleFunc("GET /participants/{pid}/facilities/{fid}/schedule", s.handleFacilitySchedule)
	mux.HandleFunc("POST /participants/{pid}/facilities/{fid}/disbursement", s.handleDisburse)
	mux.HandleFunc("POST /participants/{pid}/facilities/{fid}/draws", s.handleDraw)
	mux.HandleFunc("POST /participants/{pid}/facilities/{fid}/repayments", s.handleRepay)
	mux.HandleFunc("POST /participants/{pid}/facilities/{fid}/interest-charge", s.handleChargeInterest)
	mux.HandleFunc("DELETE /participants/{pid}/facilities/{fid}", s.handleCloseFacility)

	mux.HandleFunc("POST /participants/{pid}/end-of-day", s.handleRunEndOfDay)
	mux.HandleFunc("GET /participants/{pid}/totals", s.handleTotals)
}

// handleOpenFacility opens a term loan or a revolving line, chosen by the
// request's Kind field — the same one-route-many-actions shape
// handleDepositStatus uses. Kind, DayCount and (for a term loan) Method are
// parsed from their string forms exactly as the ledger handlers parse an
// account type, so an unknown value is a 400 before the domain is ever called.
func (s *Server) handleOpenFacility(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req openFacilityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	if req.Asset == "" {
		writeBadRequest(w, "asset is required")
		return
	}
	kind, err := facilityKindFromString(req.Kind)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	dc, err := dayCountFromString(req.DayCount)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}

	var f lending.Facility
	switch kind {
	case lending.TermLoan:
		method, err := amortMethodFromString(req.Method)
		if err != nil {
			writeBadRequest(w, err.Error())
			return
		}
		f, err = p.Lending.OpenTermLoan(r.Context(), p.CustomerSubledger, req.Name, ledger.AssetCode(req.Asset),
			ledger.Amount(req.Commitment), interest.Rate(req.Rate), dc, method, req.TermMonths)
		if err != nil {
			writeError(w, err)
			return
		}
	default: // RevolvingLine
		f, err = p.Lending.OpenRevolvingLine(r.Context(), p.CustomerSubledger, req.Name, ledger.AssetCode(req.Asset),
			ledger.Amount(req.Commitment), interest.Rate(req.Rate), dc, interest.Fraction(req.MinPayment))
		if err != nil {
			writeError(w, err)
			return
		}
	}

	// A freshly-opened facility is Pending: nothing has been advanced and
	// nothing has accrued, so the two derived figures are 0 without a further
	// round trip to read the balances back.
	writeJSON(w, http.StatusCreated, toFacilityDTO(f, 0, 0))
}

func (s *Server) handleListFacilities(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	facilities, err := p.Lending.ListFacilities(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]facilityDTO, len(facilities))
	for i, f := range facilities {
		drawn, err := p.Lending.Drawn(r.Context(), f.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		accrued, err := p.Lending.AccruedInterest(r.Context(), f.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		out[i] = toFacilityDTO(f, drawn, accrued)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetFacility(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	f, err := p.Lending.GetFacility(r.Context(), fid)
	if err != nil {
		writeError(w, err)
		return
	}
	drawn, err := p.Lending.Drawn(r.Context(), fid)
	if err != nil {
		writeError(w, err)
		return
	}
	accrued, err := p.Lending.AccruedInterest(r.Context(), fid)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toFacilityDTO(f, drawn, accrued))
}

func (s *Server) handleFacilitySchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	schedule, err := p.Lending.Schedule(r.Context(), lending.FacilityID(r.PathValue("fid")))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]installmentDTO, len(schedule))
	for i, inst := range schedule {
		out[i] = toInstallmentDTO(inst)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDisburse(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req disburseFacilityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	firstDue, err := time.Parse("2006-01-02", req.FirstDue)
	if err != nil {
		writeBadRequest(w, "invalid firstDue (want YYYY-MM-DD)")
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	tx, err := p.Lending.Disburse(r.Context(), fid, ledger.AccountID(req.Counterparty), firstDue, req.Description)
	if err != nil {
		writeError(w, err)
		return
	}
	assets, err := entryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTransactionDTO(tx, assets))
}

func (s *Server) handleDraw(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req drawFacilityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	tx, err := p.Lending.Draw(r.Context(), fid, ledger.AccountID(req.Counterparty), ledger.Amount(req.Amount), req.Description)
	if err != nil {
		writeError(w, err)
		return
	}
	assets, err := entryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTransactionDTO(tx, assets))
}

// handleRepay applies a repayment from a customer's deposit account.
//
// It is the orchestration point the lending package deliberately does not have:
// lending takes a generic counterparty and knows nothing about deposit account
// status or available balance, so the check and the posting are driven here,
// through ONE Tx. Calling the two plain methods in sequence would let a
// repayment post after the funds check had already passed against a balance
// another request had since spent.
func (s *Server) handleRepay(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req repayFacilityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		writeBadRequest(w, "invalid date (want YYYY-MM-DD)")
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
		out, err = p.Lending.RepayTx(ctx, lendingTx, fid, acct.GLAccount,
			ledger.Amount(req.Amount), date, req.Description)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	assets, err := entryAssets(r.Context(), p.Ledger, []ledger.Transaction{out})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTransactionDTO(out, assets))
}

// handleChargeInterest closes a revolving line's billing cycle. It returns
// ErrWrongFacilityKind for a term loan, which settles interest through its
// scheduled instalments instead — see lending.Portfolio.ChargeInterest.
//
// Nothing accrued and nothing drawn bills nothing: ChargeInterest returns a
// zero-value Transaction rather than an error, and this writes 200 with no
// body rather than rendering a Transaction with an empty ID as though it were
// a real posting.
func (s *Server) handleChargeInterest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req chargeFacilityInterestRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		writeBadRequest(w, "invalid date (want YYYY-MM-DD)")
		return
	}
	fid := lending.FacilityID(r.PathValue("fid"))
	tx, err := p.Lending.ChargeInterest(r.Context(), fid, date)
	if err != nil {
		writeError(w, err)
		return
	}
	if tx.ID == "" {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	assets, err := entryAssets(r.Context(), p.Ledger, []ledger.Transaction{tx})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTransactionDTO(tx, assets))
}

func (s *Server) handleCloseFacility(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	if err := p.Lending.Close(r.Context(), lending.FacilityID(r.PathValue("fid"))); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRunEndOfDay calls Participant.RunEndOfDay and nothing else.
//
// It deliberately does not expose the deposit and lending batches separately:
// they run in one unit of work so that a bank cannot end up with a day of
// interest on its loans and none on its overdrafts, and a route per batch would
// hand a client exactly that.
func (s *Server) handleRunEndOfDay(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req endOfDayRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		writeBadRequest(w, "invalid date (want YYYY-MM-DD)")
		return
	}
	if err := p.RunEndOfDay(r.Context(), date); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTotals serves GET /participants/{pid}/totals: the bank's
// customer-deposit position split into deposits and (derived) overdrafts, per
// asset. See deposit.Totals and totalsDTO.
func (s *Server) handleTotals(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	totals, err := p.Deposit.Totals(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTotalsDTOs(totals))
}
