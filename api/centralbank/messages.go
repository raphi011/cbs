package centralbank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// handleListMessages serves the SETTLEMENT AGENT's own traffic. It names no
// payment: a settlement instruction identifies the cut-off it discharges, and
// the payments netted into one are the clearing house's.
func (s *surface) handleListMessages(r *http.Request) ([]api.MessageDTO, error) {
	return api.MessagePage(r, s.network())
}

// handleGetMessage serves one of them with the document it carried.
func (s *surface) handleGetMessage(r *http.Request) (api.MessageDocumentDTO, error) {
	return api.MessageDocument(r, s.network())
}
