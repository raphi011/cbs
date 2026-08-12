package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// handlePaymentAudit serves the CLEARING HOUSE's payment-scope trail: the
// members it admitted to its roster, the cut-offs it ran, and every payment it
// relayed and took into one.
func (s *surface) handlePaymentAudit(r *http.Request) ([]api.AuditEventDTO, error) {
	return api.AuditPage(r.Context(), s.network(), api.AuditFilterFrom(r, payment.ClearingHouseBook, ledger.ScopePayment))
}
