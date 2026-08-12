package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
)

// A bank checking its own books, on that bank's own listener and nowhere else.
func (s *surface) registerReconciliationRoutes(mux *api.Router) {
	// A POST, and it is the one act among the four.
	mux.HandleFunc("POST /reconciliation", api.Handle(http.StatusOK, s.handleReconcile))
	// The stored camt.053s, read back. This is where a break naming a reference
	// sends its reader.
	mux.HandleFunc("GET /settlement-advices", api.Handle(http.StatusOK, s.handleListSettlementAdvices))
	// The two in-transit accounts, aged. Reads, and they judge nothing that
	// running them again would change.
	mux.HandleFunc("GET /clearing-suspense/ageing", api.Handle(http.StatusOK, s.handleAgeClearingSuspense))
	mux.HandleFunc("GET /unclaimed-balances/ageing", api.Handle(http.StatusOK, s.handleAgeUnclaimedBalances))
}

// assetParam reads the asset a report is about off the query string.
func assetParam(r *http.Request) (ledger.AssetCode, error) {
	code := r.URL.Query().Get("asset")
	if code == "" {
		return "", api.BadRequest("asset is required: a bank holds one reserve and one clearing suspense per asset, and a report over all of them at once would add one money to another")
	}
	return ledger.AssetCode(code), nil
}

// handleReconcile runs this bank's own reconciliation for one asset.
func (s *surface) handleReconcile(r *http.Request) (api.ReconciliationDTO, error) {
	asset, err := assetParam(r)
	if err != nil {
		return api.ReconciliationDTO{}, err
	}
	rec, err := s.network().Reconcile(r.Context(), asset)
	if err != nil {
		return api.ReconciliationDTO{}, err
	}
	return api.ToReconciliationDTO(rec), nil
}

// handleListSettlementAdvices answers every statement this bank has been sent,
// oldest first, in every asset.
func (s *surface) handleListSettlementAdvices(r *http.Request) ([]api.SettlementAdviceDTO, error) {
	advices, err := s.network().ListSettlementAdvices(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.SettlementAdviceDTO, 0, len(advices))
	for _, a := range advices {
		out = append(out, api.ToSettlementAdviceDTO(a))
	}
	return out, nil
}

// handleAgeClearingSuspense decomposes this bank's clearing suspense by age.
func (s *surface) handleAgeClearingSuspense(r *http.Request) (api.AgeingReportDTO, error) {
	asset, err := assetParam(r)
	if err != nil {
		return api.AgeingReportDTO{}, err
	}
	rep, err := s.network().AgeClearingSuspense(r.Context(), asset)
	if err != nil {
		return api.AgeingReportDTO{}, err
	}
	return api.ToAgeingReportDTO(rep), nil
}

// handleAgeUnclaimedBalances decomposes this bank's unclaimed balances by age
// and says what may be done about each part.
func (s *surface) handleAgeUnclaimedBalances(r *http.Request) (api.AgeingReportDTO, error) {
	asset, err := assetParam(r)
	if err != nil {
		return api.AgeingReportDTO{}, err
	}
	rep, err := s.network().AgeUnclaimedBalances(r.Context(), asset)
	if err != nil {
		return api.AgeingReportDTO{}, err
	}
	return api.ToAgeingReportDTO(rep), nil
}
