package api

import (
	"net/http"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

func (s *Server) registerParticipantRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /participants", s.handleAddParticipant)
	mux.HandleFunc("GET /participants", s.handleListParticipants)
	mux.HandleFunc("GET /participants/{pid}", s.handleGetParticipant)
	mux.HandleFunc("POST /participants/{pid}/deposits", s.handleFundDeposit)

	mux.HandleFunc("GET /schemes", s.handleListSchemes)

	mux.HandleFunc("GET /central-bank/reserves", s.handleListReserves)
	mux.HandleFunc("GET /central-bank/reserves/{pid}", s.handleGetReserve)
}

func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	p, err := s.network().AddParticipant(r.Context(), req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toParticipantDTO(p))
}

func (s *Server) handleListParticipants(w http.ResponseWriter, r *http.Request) {
	parts, err := s.network().ListParticipants(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
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
	if err := s.network().Deposit(r.Context(), p.ID, deposit.AccountID(req.Account), ledger.Amount(req.Amount), req.Description); err != nil {
		writeError(w, err)
		return
	}
	bal, err := p.Deposit.GetBalance(r.Context(), deposit.AccountID(req.Account))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBalanceDTO(bal))
}

func (s *Server) handleListSchemes(w http.ResponseWriter, r *http.Request) {
	schemes := s.network().ListSchemes()
	out := make([]schemeDTO, len(schemes))
	for i, sc := range schemes {
		out[i] = toSchemeDTO(sc)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListReserves(w http.ResponseWriter, r *http.Request) {
	parts, err := s.network().ListParticipants(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]reserveDTO, 0, len(parts))
	for _, p := range parts {
		bal, err := s.network().ReserveBalance(r.Context(), p.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		out = append(out, reserveDTO{Participant: string(p.ID), Reserve: int64(bal)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetReserve(w http.ResponseWriter, r *http.Request) {
	pid := payment.ParticipantID(r.PathValue("pid"))
	bal, err := s.network().ReserveBalance(r.Context(), pid)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reserveDTO{Participant: string(pid), Reserve: int64(bal)})
}
