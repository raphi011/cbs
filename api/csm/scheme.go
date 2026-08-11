package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

func (s *surface) handleListSchemes(r *http.Request) ([]api.SchemeDTO, error) {
	schemes := s.network().ListSchemes()
	out := make([]api.SchemeDTO, len(schemes))
	for i, sc := range schemes {
		out[i] = api.ToSchemeDTO(sc)
	}
	return out, nil
}
