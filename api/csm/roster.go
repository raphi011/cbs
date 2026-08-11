package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// handleListRoster answers the clearing house's routing directory.
//
// It is the successor to GET /directory on this surface and it answers a
// different question, which is the whole finding: the question the sweep
// answered — "who holds this IBAN" — is one no institution here can answer, and
// the question this institution CAN answer is "who may be addressed". A bank
// absent from this list exists perfectly well; it is simply not somewhere this
// scheme will send anything. A bank present in it may be addressed and nothing
// about its customers is knowable from here.
func (s *surface) handleListRoster(w http.ResponseWriter, r *http.Request) {
	entries, err := s.network().ListRosterEntries(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.RosterEntryDTO, len(entries))
	for i, e := range entries {
		out[i] = api.ToRosterEntryDTO(e)
	}
	api.WriteJSON(w, http.StatusOK, out)
}
