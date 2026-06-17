package api

import (
	"net/http"

	"github.com/raphi011/ledger/deposit"
	"github.com/raphi011/ledger/payment"
)

func (s *Server) registerParticipantRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /participants", s.handleAddParticipant)
	mux.HandleFunc("GET /participants", s.handleListParticipants)
	mux.HandleFunc("GET /participants/{pid}", s.handleGetParticipant)
	mux.HandleFunc("POST /participants/{pid}/deposits", s.handleFundDeposit)

	mux.HandleFunc("GET /schemes", s.handleListSchemes)

	mux.HandleFunc("GET /central-bank/reserves", s.handleListReserves)
	mux.HandleFunc("GET /central-bank/reserves/{pid}", s.handleGetReserve)
	mux.HandleFunc("GET /central-bank/audit", s.handleCentralBankAudit)
}

func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	p, err := s.net.AddParticipant(req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toParticipantDTO(p))
}

func (s *Server) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	parts := s.net.ListParticipants()
	out := make([]participantDTO, len(parts))
	for i, p := range parts {
		out[i] = toParticipantDTO(p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetParticipant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toParticipantDTO(p))
}

func (s *Server) handleFundDeposit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req fundRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	if err := s.net.Deposit(p.ID, deposit.AccountID(req.Account), req.Amount, req.Description); err != nil {
		writeError(w, err)
		return
	}
	bal, err := p.Deposit.GetBalance(deposit.AccountID(req.Account))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBalanceDTO(bal))
}

func (s *Server) handleListSchemes(w http.ResponseWriter, r *http.Request) {
	schemes := s.net.ListSchemes()
	out := make([]schemeDTO, len(schemes))
	for i, sc := range schemes {
		out[i] = toSchemeDTO(sc)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListReserves(w http.ResponseWriter, r *http.Request) {
	parts := s.net.ListParticipants()
	out := make([]reserveDTO, 0, len(parts))
	for _, p := range parts {
		bal, err := s.net.ReserveBalance(p.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, reserveDTO{Participant: string(p.ID), Reserve: bal})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetReserve(w http.ResponseWriter, r *http.Request) {
	pid := payment.ParticipantID(r.PathValue("pid"))
	bal, err := s.net.ReserveBalance(pid)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reserveDTO{Participant: string(pid), Reserve: bal})
}

func (s *Server) handleCentralBankAudit(w http.ResponseWriter, r *http.Request) {
	events := s.net.CentralBank().GetAuditLog()
	out := make([]auditEventDTO, len(events))
	for i, e := range events {
		out[i] = toLedgerAuditDTO(e)
	}
	writeJSON(w, http.StatusOK, out)
}
