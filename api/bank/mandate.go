package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// registerMandateRoutes is the CREDITOR bank's mandate console, and it is on a
// bank's surface because a mandate is that bank's own row.
//
// A mandate looks like network infrastructure and is not: in SEPA the creditor
// holds it, payment.SDD.ValidateMandate says so, and the bank that checks one at
// submission is the creditor's. On the clearing house's surface this console
// would be every member's authorisations over every other member's customers'
// accounts, listed on one page.
//
// There is no debtor-side route. A debtor's bank has no mandate row in this
// system and no message that would give it one, which is the limit
// SDD.ValidateMandate already names: a real debtor's bank keeps records of its
// own and can refuse a collection MD01, and this one cannot.
func (s *surface) registerMandateRoutes(mux *api.Router) {
	mux.HandleFunc("POST /mandates", s.handleCreateMandate)
	mux.HandleFunc("GET /mandates", s.handleListMandates)
	mux.HandleFunc("GET /mandates/{mid}", s.handleGetMandate)
	mux.HandleFunc("POST /mandates/{mid}/revoke", s.handleRevokeMandate)
}

func (s *surface) handleCreateMandate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateMandateRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	// No agent is asserted, because this request has no field for one: the
	// debtor's bank is derived from the debtor's address. See CreateMandateRequest.
	m, err := s.network().CreateMandate(r.Context(), "",
		req.Debtor.ToDomain(), req.Creditor.ToDomain(), ledger.Amount(req.MaxAmount))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToMandateDTO(m))
}

func (s *surface) handleListMandates(w http.ResponseWriter, r *http.Request) {
	mandates, err := s.network().ListMandates(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.MandateDTO, len(mandates))
	for i, m := range mandates {
		out[i] = api.ToMandateDTO(m)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleGetMandate(w http.ResponseWriter, r *http.Request) {
	m, err := s.network().GetMandate(r.Context(), payment.MandateID(r.PathValue("mid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToMandateDTO(m))
}

func (s *surface) handleRevokeMandate(w http.ResponseWriter, r *http.Request) {
	if err := s.network().RevokeMandate(r.Context(), payment.MandateID(r.PathValue("mid"))); err != nil {
		api.WriteError(w, err)
		return
	}
	m, err := s.network().GetMandate(r.Context(), payment.MandateID(r.PathValue("mid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToMandateDTO(m))
}
