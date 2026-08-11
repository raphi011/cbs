package centralbank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// handleCentralBankAudit serves the central bank's own book — a real chart of
// accounts, so its events are ledger-scoped like any other bank's.
func (s *surface) handleCentralBankAudit(w http.ResponseWriter, r *http.Request) {
	api.WriteAudit(w, r, s.network(), api.AuditFilterFrom(r, payment.CentralBankBook, ledger.ScopeLedger))
}
