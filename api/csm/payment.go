package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// Every asset on this surface comes off the ROW it is rendered from, and no
// handler resolves one.
func (s *surface) registerPaymentRoutes(mux *api.Router) {
	mux.HandleFunc("POST /payments", api.HandleBody(http.StatusAccepted, s.handleInitiatePayment))
	mux.HandleFunc("GET /payments", api.Handle(http.StatusOK, s.handleListPayments))
	mux.HandleFunc("GET /payments/{payid}", api.Handle(http.StatusOK, s.handleGetPayment))
	mux.HandleFunc("POST /payments/{payid}/reject", api.HandleBody(http.StatusAccepted, s.handleRejectPayment))
	mux.HandleFunc("POST /payments/{payid}/return", api.HandleBody(http.StatusAccepted, s.handleReturnPayment))

	mux.HandleFunc("POST /cycles", api.HandleBody(http.StatusCreated, s.handleOpenCycle))
	mux.HandleFunc("GET /cycles", api.Handle(http.StatusOK, s.handleListCycles))
	mux.HandleFunc("GET /cycles/{cid}", api.Handle(http.StatusOK, s.handleGetCycle))
	mux.HandleFunc("POST /cycles/{cid}/close", api.Handle(http.StatusOK, s.handleCloseCycle))
	mux.HandleFunc("POST /cycles/{cid}/settle", api.Handle(http.StatusAccepted, s.handleSettleCycle))

	// GET /settlements is NOT here: this function is the clearing house's router,
	// and a settlement is the SETTLEMENT AGENT's row. The csm shape has no
	// settlements table.
}

// handleInitiatePayment hands a payment instruction to the bank whose act it
// is, and answers with what that bank did.
func (s *surface) handleInitiatePayment(r *http.Request, req api.InitiatePaymentRequest) (api.PaymentDTO, error) {
	dom := req.ToDomain()
	// Which bank's act this is, read out of the SUBMITTING side's address.
	sc, ok := s.network().Scheme(dom.Scheme)
	if !ok {
		return api.PaymentDTO{}, payment.ErrSchemeNotFound
	}
	submitting := &dom.DebtorDetails
	address := dom.Debtor.Identifier
	if sc.Direction() == payment.Pull {
		submitting, address = &dom.CreditorDetails, dom.Creditor.Identifier
	}
	if submitting.Agent == "" {
		agent, err := s.network().RosterAgentFor(r.Context(), address)
		if err != nil {
			return api.PaymentDTO{}, err
		}
		submitting.Agent = agent
	}
	p, err := s.op.Submit(r.Context(), dom)
	if err != nil {
		return api.PaymentDTO{}, err
	}
	return api.ToPaymentDTO(p, s.network().ListSchemes()), nil
}

func (s *surface) handleListPayments(r *http.Request) ([]api.PaymentDTO, error) {
	payments, err := s.network().ListPayments(r.Context())
	if err != nil {
		return nil, err
	}
	schemes := s.network().ListSchemes()
	out := make([]api.PaymentDTO, len(payments))
	for i, p := range payments {
		out[i] = api.ToPaymentDTO(p, schemes)
	}
	return out, nil
}

func (s *surface) handleGetPayment(r *http.Request) (api.PaymentDTO, error) {
	p, err := s.network().GetPayment(r.Context(), payment.PaymentID(r.PathValue("payid")))
	if err != nil {
		return api.PaymentDTO{}, err
	}
	return api.ToPaymentDTO(p, s.network().ListSchemes()), nil
}

// handleRejectPayment declines an in-flight payment on the operator's say-so.
func (s *surface) handleRejectPayment(r *http.Request, req api.ReasonRequest) (api.PaymentDTO, error) {
	// An operator-initiated rejection carries no more specific external status
	// reason than MS03: the API exposes no way for a caller to name a code, so
	// there is no more honest choice than the one that says exactly that.
	p, err := s.inst.Reject(r.Context(), payment.PaymentID(r.PathValue("payid")), iso20022.StatusReasonNotSpecifiedAgentGenerated, req.Reason)
	if err != nil {
		// ClearingHouse.Reject hands the payment back beside the error when its own
		// half committed and the pacs.002 did not go out — the payment is Rejected
		// and out of its cycle, and the payer's money is still in their bank's
		// suspense with nothing on its way to release it.
		if p.ID != "" {
			s.inst.Log().Error("api: a rejection committed and the bank that has to refund was not told",
				"payment", p.ID, "status", p.Status, "error", err)
		}
		return api.PaymentDTO{}, err
	}
	return api.ToPaymentDTO(p, s.network().ListSchemes()), nil
}

// handleReturnPayment sends a settled payment back.
func (s *surface) handleReturnPayment(r *http.Request, req api.ReasonRequest) (api.SubmittedPaymentDTO, error) {
	id := payment.PaymentID(r.PathValue("payid"))
	if err := s.op.Return(r.Context(), id, iso20022.ReturnReasonNotSpecifiedAgentGenerated, req.Reason); err != nil {
		return api.SubmittedPaymentDTO{}, err
	}
	return api.SubmittedPaymentDTO{PaymentID: string(id)}, nil
}

func (s *surface) handleOpenCycle(r *http.Request, req api.OpenCycleRequest) (api.ClearingCycleDTO, error) {
	c, err := s.network().OpenCycle(r.Context(), payment.SchemeID(req.Scheme))
	if err != nil {
		return api.ClearingCycleDTO{}, err
	}
	return api.ToClearingCycleDTO(c, s.network().ListSchemes()), nil
}

func (s *surface) handleListCycles(r *http.Request) ([]api.ClearingCycleDTO, error) {
	cycles, err := s.network().ListCycles(r.Context())
	if err != nil {
		return nil, err
	}
	schemes := s.network().ListSchemes()
	out := make([]api.ClearingCycleDTO, len(cycles))
	for i, c := range cycles {
		out[i] = api.ToClearingCycleDTO(c, schemes)
	}
	return out, nil
}

func (s *surface) handleGetCycle(r *http.Request) (api.ClearingCycleDTO, error) {
	c, err := s.network().GetCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		return api.ClearingCycleDTO{}, err
	}
	return api.ToClearingCycleDTO(c, s.network().ListSchemes()), nil
}

// handleCloseCycle reaches the cut-off, and is the only way a settlement is
// instructed in this system.
func (s *surface) handleCloseCycle(r *http.Request) (api.ClearingCycleDTO, error) {
	c, err := s.inst.CloseCycle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		// The cycle comes back beside the error when the cut-off committed and the
		// instruction could not be sent, and that half-happened state is exactly what
		// nobody would otherwise be told: the payments are Cleared, no settlement
		// agent has been asked, and the response is a 5xx that names an error rather
		// than a cycle.
		if c.ID != "" {
			s.inst.Log().Error("api: a cut-off committed and its settlement instruction did not go out",
				"cycle", c.ID, "status", c.Status, "error", err)
		}
		return api.ClearingCycleDTO{}, err
	}
	return api.ToClearingCycleDTO(c, s.network().ListSchemes()), nil
}

// handleSettleCycle asks the clearing house to instruct settlement of a closed
// cycle again, and is the way out of a refused settlement.
func (s *surface) handleSettleCycle(r *http.Request) (api.ClearingCycleDTO, error) {
	c, err := s.inst.Settle(r.Context(), payment.CycleID(r.PathValue("cid")))
	if err != nil {
		if c.ID != "" {
			s.inst.Log().Error("api: a settlement instruction could not be re-sent",
				"cycle", c.ID, "status", c.Status, "error", err)
		}
		return api.ClearingCycleDTO{}, err
	}
	return api.ToClearingCycleDTO(c, s.network().ListSchemes()), nil
}
