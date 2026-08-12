package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// registerMandateRoutes is the CREDITOR bank's mandate console, and it is on a
// bank's surface because a mandate is that bank's own row.
func (s *surface) registerMandateRoutes(mux *api.Router) {
	mux.HandleFunc("POST /mandates", api.HandleBody(http.StatusCreated, s.handleCreateMandate))
	mux.HandleFunc("GET /mandates", api.Handle(http.StatusOK, s.handleListMandates))
	mux.HandleFunc("GET /mandates/{mid}", api.Handle(http.StatusOK, s.handleGetMandate))
	mux.HandleFunc("POST /mandates/{mid}/revoke", api.Handle(http.StatusOK, s.handleRevokeMandate))
}

func (s *surface) handleCreateMandate(r *http.Request, req api.CreateMandateRequest) (api.MandateDTO, error) {
	// No agent is asserted, because this request has no field for one: the
	// debtor's bank is derived from the debtor's address. See CreateMandateRequest.
	m, err := s.network().CreateMandate(r.Context(), "",
		req.Debtor.ToDomain(), req.Creditor.ToDomain(), ledger.Amount(req.MaxAmount))
	if err != nil {
		return api.MandateDTO{}, err
	}
	return api.ToMandateDTO(m), nil
}

func (s *surface) handleListMandates(r *http.Request) ([]api.MandateDTO, error) {
	mandates, err := s.network().ListMandates(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.MandateDTO, len(mandates))
	for i, m := range mandates {
		out[i] = api.ToMandateDTO(m)
	}
	return out, nil
}

func (s *surface) handleGetMandate(r *http.Request) (api.MandateDTO, error) {
	m, err := s.network().GetMandate(r.Context(), payment.MandateID(r.PathValue("mid")))
	if err != nil {
		return api.MandateDTO{}, err
	}
	return api.ToMandateDTO(m), nil
}

// handleRevokeMandate answers with the row re-read rather than with the request
// echoed, so a caller sees the revocation's own timestamp — and it re-reads
// through the handler beside it, which resolves the same path value from the
// same request.
func (s *surface) handleRevokeMandate(r *http.Request) (api.MandateDTO, error) {
	if err := s.network().RevokeMandate(r.Context(), payment.MandateID(r.PathValue("mid"))); err != nil {
		return api.MandateDTO{}, err
	}
	return s.handleGetMandate(r)
}
