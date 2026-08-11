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
//
// Read-only and network-wide, the same shape as GET /schemes and for the same
// reason — both are defined in code rather than stored, so there is a list to
// read and nothing to create. It is the one endpoint that lets a client turn a
// minor-unit integer into a human-readable amount: an `asset` field elsewhere
// carries a code, and the scale that code implies lives only here.
//
// It is the one handler in this package, and the one piece of genuine sharing in
// the three-way split: all three surfaces register it, none of them owns it, and
// it reaches no institution at all — ledger.Assets is a compiled-in list, so
// there is no state for a caller's identity to narrow.
func HandleListAssets(w http.ResponseWriter, r *http.Request) {
	defs := ledger.Assets()
	out := make([]AssetDTO, len(defs))
	for i, a := range defs {
		out[i] = ToAssetDTO(a)
	}
	writeJSON(w, http.StatusOK, out)
}
