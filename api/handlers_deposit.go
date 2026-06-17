package api

import (
	"net/http"
	"time"

	"github.com/raphi011/ledger"
	"github.com/raphi011/ledger/deposit"
)

func (s *Server) registerDepositRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /participants/{pid}/deposit-accounts", s.handleOpenDepositAccount)
	mux.HandleFunc("GET /participants/{pid}/deposit-accounts", s.handleListDepositAccounts)
	mux.HandleFunc("GET /participants/{pid}/deposit-accounts/{did}", s.handleGetDepositAccount)
	mux.HandleFunc("GET /participants/{pid}/deposit-accounts/{did}/balance", s.handleDepositBalance)
	mux.HandleFunc("POST /participants/{pid}/deposit-accounts/{did}/status", s.handleDepositStatus)
	mux.HandleFunc("DELETE /participants/{pid}/deposit-accounts/{did}", s.handleCloseDepositAccount)

	mux.HandleFunc("POST /participants/{pid}/deposit-accounts/{did}/holds", s.handleCreateHold)
	mux.HandleFunc("GET /participants/{pid}/deposit-accounts/{did}/holds", s.handleListHolds)
	mux.HandleFunc("GET /participants/{pid}/holds/{hid}", s.handleGetHold)
	mux.HandleFunc("POST /participants/{pid}/holds/{hid}/release", s.handleReleaseHold)
	mux.HandleFunc("POST /participants/{pid}/holds/{hid}/capture", s.handleCaptureHold)

	mux.HandleFunc("POST /participants/{pid}/deposit-accounts/{did}/snapshots", s.handleTakeSnapshot)
	mux.HandleFunc("GET /participants/{pid}/deposit-accounts/{did}/snapshots", s.handleGetSnapshots)

	mux.HandleFunc("GET /participants/{pid}/deposit-audit", s.handleDepositAudit)
}

func (s *Server) handleOpenDepositAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req openDepositAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	acct, err := p.Deposit.OpenAccount(p.CustomerSubledger, req.Name, req.OverdraftLimit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDepositAccountDTO(acct))
}

func (s *Server) handleListDepositAccounts(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	accts := p.Deposit.ListAccounts()
	out := make([]depositAccountDTO, len(accts))
	for i, a := range accts {
		out[i] = toDepositAccountDTO(a)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetDepositAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	acct, err := p.Deposit.GetAccount(deposit.AccountID(r.PathValue("did")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDepositAccountDTO(acct))
}

func (s *Server) handleDepositBalance(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	bal, err := p.Deposit.GetBalance(deposit.AccountID(r.PathValue("did")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBalanceDTO(bal))
}

// handleDepositStatus dispatches a lifecycle transition based on the request's
// "action" field. This keeps the four transitions behind one URL, which suits a
// frontend status dropdown.
func (s *Server) handleDepositStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req statusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	did := deposit.AccountID(r.PathValue("did"))
	var err error
	switch req.Action {
	case "freeze":
		err = p.Deposit.Freeze(did)
	case "unfreeze":
		err = p.Deposit.Unfreeze(did)
	case "markDormant":
		err = p.Deposit.MarkDormant(did)
	case "reactivate":
		err = p.Deposit.Reactivate(did)
	default:
		writeBadRequest(w, "invalid action (want freeze, unfreeze, markDormant, or reactivate)")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	acct, err := p.Deposit.GetAccount(did)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDepositAccountDTO(acct))
}

func (s *Server) handleCloseDepositAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	if err := p.Deposit.Close(deposit.AccountID(r.PathValue("did"))); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateHold(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req createHoldRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	holdReq := deposit.CreateHoldRequest{
		AccountID:   deposit.AccountID(r.PathValue("did")),
		Amount:      req.Amount,
		Description: req.Description,
	}
	if req.ExpiresAt != nil {
		holdReq.ExpiresAt = *req.ExpiresAt
	}
	hold, err := p.Deposit.CreateHold(holdReq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toHoldDTO(hold))
}

func (s *Server) handleListHolds(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	holds := p.Deposit.ListHolds(deposit.AccountID(r.PathValue("did")))
	out := make([]holdDTO, len(holds))
	for i, h := range holds {
		out[i] = toHoldDTO(h)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetHold(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	hold, err := p.Deposit.GetHold(deposit.HoldID(r.PathValue("hid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toHoldDTO(hold))
}

func (s *Server) handleReleaseHold(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	if err := p.Deposit.ReleaseHold(deposit.HoldID(r.PathValue("hid"))); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCaptureHold(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req captureHoldRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	tx, err := p.Deposit.CaptureHold(
		deposit.HoldID(r.PathValue("hid")),
		ledger.AccountID(req.Counterparty),
		req.Amount,
		req.Description,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTransactionDTO(tx))
}

func (s *Server) handleTakeSnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req snapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		writeBadRequest(w, "invalid date (want YYYY-MM-DD)")
		return
	}
	snap, err := p.Deposit.TakeEndOfDaySnapshot(deposit.AccountID(r.PathValue("did")), date)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSnapshotDTO(snap))
}

// handleGetSnapshots returns one snapshot when ?date=YYYY-MM-DD is given, or all
// snapshots for the account otherwise.
func (s *Server) handleGetSnapshots(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	did := deposit.AccountID(r.PathValue("did"))
	if dateStr := r.URL.Query().Get("date"); dateStr != "" {
		date, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			writeBadRequest(w, "invalid date (want YYYY-MM-DD)")
			return
		}
		snap, err := p.Deposit.GetSnapshot(did, date)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toSnapshotDTO(snap))
		return
	}
	snaps := p.Deposit.ListSnapshots(did)
	out := make([]snapshotDTO, len(snaps))
	for i, snap := range snaps {
		out[i] = toSnapshotDTO(snap)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDepositAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	events := p.Deposit.GetAuditLog()
	out := make([]auditEventDTO, len(events))
	for i, e := range events {
		out[i] = toDepositAuditDTO(e)
	}
	writeJSON(w, http.StatusOK, out)
}
