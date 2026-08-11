package centralbank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

func (s *surface) handleListSettlements(r *http.Request) ([]api.SettlementDTO, error) {
	settlements, err := s.network().ListSettlements(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.SettlementDTO, len(settlements))
	for i, st := range settlements {
		out[i] = api.ToSettlementDTO(st)
	}
	return out, nil
}

func (s *surface) handleGetSettlement(r *http.Request) (api.SettlementDTO, error) {
	st, err := s.network().GetSettlement(r.Context(), payment.SettlementID(r.PathValue("sid")))
	if err != nil {
		return api.SettlementDTO{}, err
	}
	return api.ToSettlementDTO(st), nil
}
