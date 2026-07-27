package api

import (
	"net/http"

	"github.com/raphi011/cbs/payment"
)

func (s *Server) registerPaymentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mandates", s.handleCreateMandate)
	mux.HandleFunc("GET /mandates", s.handleListMandates)
	mux.HandleFunc("GET /mandates/{mid}", s.handleGetMandate)
	mux.HandleFunc("POST /mandates/{mid}/revoke", s.handleRevokeMandate)

	mux.HandleFunc("POST /payments", s.handleInitiatePayment)
	mux.HandleFunc("GET /payments", s.handleListPayments)
	mux.HandleFunc("GET /payments/{payid}", s.handleGetPayment)
	mux.HandleFunc("POST /payments/{payid}/reject", s.handleRejectPayment)
	mux.HandleFunc("POST /payments/{payid}/return", s.handleReturnPayment)

	mux.HandleFunc("POST /cycles", s.handleOpenCycle)
	mux.HandleFunc("GET /cycles", s.handleListCycles)
	mux.HandleFunc("GET /cycles/{cid}", s.handleGetCycle)
	mux.HandleFunc("POST /cycles/{cid}/close", s.handleCloseCycle)
	mux.HandleFunc("POST /cycles/{cid}/settle", s.handleSettleCycle)

	mux.HandleFunc("GET /settlements", s.handleListSettlements)
	mux.HandleFunc("GET /settlements/{sid}", s.handleGetSettlement)
}

func (s *Server) handleCreateMandate(w http.ResponseWriter, r *http.Request) {
	var req createMandateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	m, err := s.network().CreateMandate(r.Context(), req.Debtor.toDomain(), req.Creditor.toDomain(), req.MaxAmount)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toMandateDTO(m))
}

func (s *Server) handleListMandates(w http.ResponseWriter, r *http.Request) {
	mandates, err := s.network().ListMandates(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]mandateDTO, len(mandates))
	for i, m := range mandates {
		out[i] = toMandateDTO(m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetMandate(w http.ResponseWriter, r *http.Request) {
	m, err := s.network().GetMandate(r.Context(), payment.MandateID(r.PathValue("mid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMandateDTO(m))
}

func (s *Server) handleRevokeMandate(w http.ResponseWriter, r *http.Request) {
	if err := s.network().RevokeMandate(r.Context(), payment.MandateID(r.PathValue("mid"))); err != nil {
		writeError(w, err)
		return
	}
	m, err := s.network().GetMandate(r.Context(), payment.MandateID(r.PathValue("mid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMandateDTO(m))
}

func (s *Server) handleInitiatePayment(w http.ResponseWriter, r *http.Request) {
	var req initiatePaymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	p, err := s.network().InitiatePayment(r.Context(), req.toDomain())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPaymentDTO(p))
}

func (s *Server) handleListPayments(w http.ResponseWriter, r *http.Request) {
	payments, err := s.network().ListPayments(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]paymentDTO, len(payments))
	for i, p := range payments {
		out[i] = toPaymentDTO(p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	p, err := s.network().GetPayment(r.Context(), payment.PaymentID(r.PathValue("payid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentDTO(p))
}

func (s *Server) handleRejectPayment(w http.ResponseWriter, r *http.Request) {
	var req reasonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	p, err := s.network().RejectPayment(r.Context(), payment.PaymentID(r.PathValue("payid")), req.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentDTO(p))
}

func (s *Server) handleReturnPayment(w http.ResponseWriter, r *http.Request) {
	var req reasonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	p, err := s.network().ReturnPayment(r.Context(), payment.PaymentID(r.PathValue("payid")), req.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentDTO(p))
}

func (s *Server) handleOpenCycle(w http.ResponseWriter, r *http.Request) {
	var req openCycleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	c, err := s.network().OpenCycle(r.Context(), payment.SchemeID(req.Scheme))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toClearingCycleDTO(c))
}

func (s *Server) handleListCycles(w http.ResponseWriter, r *http.Request) {
	cycles, err := s.network().ListCycles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]clearingCycleDTO, len(cycles))
	for i, c := range cycles {
		out[i] = toClearingCycleDTO(c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetCycle(w http.ResponseWriter, r *http.Request) {
	c, err := s.network().GetCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClearingCycleDTO(c))
}

func (s *Server) handleCloseCycle(w http.ResponseWriter, r *http.Request) {
	c, err := s.network().CloseCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClearingCycleDTO(c))
}

func (s *Server) handleSettleCycle(w http.ResponseWriter, r *http.Request) {
	settlement, err := s.network().SettleCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettlementDTO(settlement))
}

func (s *Server) handleListSettlements(w http.ResponseWriter, r *http.Request) {
	settlements, err := s.network().ListSettlements(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]settlementDTO, len(settlements))
	for i, st := range settlements {
		out[i] = toSettlementDTO(st)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSettlement(w http.ResponseWriter, r *http.Request) {
	st, err := s.network().GetSettlement(r.Context(), payment.SettlementID(r.PathValue("sid")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettlementDTO(st))
}
