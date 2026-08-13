package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// handleListMessages serves the CLEARING HOUSE's own half of every crossing it
// was an end of. It is not the mesh: the other half of each is in the
// counterparty's database, which this institution may not read.
func (s *surface) handleListMessages(r *http.Request) ([]api.MessageDTO, error) {
	return api.MessagePage(r, s.network())
}

// handleGetMessage serves one of them with the document it carried.
func (s *surface) handleGetMessage(r *http.Request) (api.MessageDocumentDTO, error) {
	return api.MessageDocument(r, s.network())
}
