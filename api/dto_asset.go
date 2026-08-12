package api

import (
	"net/http"

	"github.com/raphi011/cbs/ledger"
)

// AssetDTO is one known asset. Class is a name ("Fiat", "Crypto") rather than
// the iota, matching how every other enum on the wire is rendered.
type AssetDTO struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Scale uint8  `json:"scale"`
	Class string `json:"class"`
}

func ToAssetDTO(a ledger.AssetDef) AssetDTO {
	return AssetDTO{Code: string(a.Code), Name: a.Name, Scale: a.Scale, Class: a.Class.String()}
}

// HandleListAssets serves GET /assets: every asset the system knows.
func HandleListAssets(w http.ResponseWriter, r *http.Request) {
	defs := ledger.Assets()
	out := make([]AssetDTO, len(defs))
	for i, a := range defs {
		out[i] = ToAssetDTO(a)
	}
	writeJSON(w, http.StatusOK, out)
}
