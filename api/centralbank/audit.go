package centralbank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// handleCentralBankAudit serves the central bank's own book — a real chart of
// accounts, so its events are ledger-scoped like any other bank's.
func (s *surface) handleCentralBankAudit(r *http.Request) ([]api.AuditEventDTO, error) {
	return api.AuditPage(r.Context(), s.network(), api.AuditFilterFrom(r, payment.CentralBankBook, ledger.ScopeLedger))
}
