package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

func (s *surface) handleListSchemes(w http.ResponseWriter, r *http.Request) {
	schemes := s.network().ListSchemes()
	out := make([]api.SchemeDTO, len(schemes))
	for i, sc := range schemes {
		out[i] = api.ToSchemeDTO(sc)
	}
	api.WriteJSON(w, http.StatusOK, out)
}
