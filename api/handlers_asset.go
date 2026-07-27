package api

import (
	"fmt"
	"net/http"

	"github.com/raphi011/cbs/ledger"
)

func (s *Server) registerAssetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /participants/{pid}/assets", s.handleCreateAsset)
	mux.HandleFunc("GET /participants/{pid}/assets", s.handleListAssets)
}

type assetDTO struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Scale uint8  `json:"scale"`
	Class string `json:"class"`
}

func newAssetDTO(a ledger.AssetDef) assetDTO {
	return assetDTO{Code: string(a.Code), Name: a.Name, Scale: a.Scale, Class: a.Class.String()}
}

// createAssetRequest carries class as a name ("Fiat", "Crypto"), matching how
// the rest of the package renders enums on the wire — accountDTO.Type and
// AccountType both go through String() rather than the iota, so an asset
// registered through this endpoint does not depend on AssetClass's numeric
// values either.
type createAssetRequest struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Scale uint8  `json:"scale"`
	Class string `json:"class"`
}

func assetClassFromString(s string) (ledger.AssetClass, error) {
	switch s {
	case "Fiat":
		return ledger.Fiat, nil
	case "Crypto":
		return ledger.Crypto, nil
	default:
		return 0, fmt.Errorf("invalid asset class %q (want Fiat or Crypto)", s)
	}
}

func (s *Server) handleCreateAsset(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req createAssetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	class, err := assetClassFromString(req.Class)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	a, err := p.Ledger.CreateAsset(r.Context(), ledger.AssetCode(req.Code), req.Name, req.Scale, class)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAssetDTO(a))
}

func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	assets, err := p.Ledger.ListAssets(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]assetDTO, len(assets))
	for i, a := range assets {
		out[i] = newAssetDTO(a)
	}
	writeJSON(w, http.StatusOK, out)
}
