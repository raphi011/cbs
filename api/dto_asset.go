package api

import (
	"net/http"

	"github.com/raphi011/cbs/ledger"
)

// assetDTO is one known asset. Class is a name ("Fiat", "Crypto") rather than
// the iota, matching how every other enum on the wire is rendered.
type assetDTO struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Scale uint8  `json:"scale"`
	Class string `json:"class"`
}

func toAssetDTO(a ledger.AssetDef) assetDTO {
	return assetDTO{Code: string(a.Code), Name: a.Name, Scale: a.Scale, Class: a.Class.String()}
}

// handleListAssets serves GET /assets: every asset the system knows.
//
// Read-only and network-wide, the same shape as GET /schemes and for the same
// reason — both are defined in code rather than stored, so there is a list to
// read and nothing to create. It is the one endpoint that lets a client turn a
// minor-unit integer into a human-readable amount: an `asset` field elsewhere
// carries a code, and the scale that code implies lives only here.
func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) {
	defs := ledger.Assets()
	out := make([]assetDTO, len(defs))
	for i, a := range defs {
		out[i] = toAssetDTO(a)
	}
	writeJSON(w, http.StatusOK, out)
}
