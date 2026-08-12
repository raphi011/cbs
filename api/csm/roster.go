package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// handleListRoster answers the clearing house's routing directory.
func (s *surface) handleListRoster(r *http.Request) ([]api.RosterEntryDTO, error) {
	entries, err := s.network().ListRosterEntries(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.RosterEntryDTO, len(entries))
	for i, e := range entries {
		out[i] = api.ToRosterEntryDTO(e)
	}
	return out, nil
}
