package centralbank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

func (s *surface) handleListSettlements(w http.ResponseWriter, r *http.Request) {
	settlements, err := s.network().ListSettlements(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.SettlementDTO, len(settlements))
	for i, st := range settlements {
		out[i] = api.ToSettlementDTO(st)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleGetSettlement(w http.ResponseWriter, r *http.Request) {
	st, err := s.network().GetSettlement(r.Context(), payment.SettlementID(r.PathValue("sid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToSettlementDTO(st))
}
