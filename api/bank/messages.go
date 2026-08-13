package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// handleListMessages serves THIS bank's record of the files it sent and
// received. A member bank hosts nothing, so this log is the only record it has
// of either half.
func (s *surface) handleListMessages(r *http.Request) ([]api.MessageDTO, error) {
	return api.MessagePage(r, s.network())
}

// handleGetMessage serves one of them with the document it carried.
func (s *surface) handleGetMessage(r *http.Request) (api.MessageDocumentDTO, error) {
	return api.MessageDocument(r, s.network())
}
