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
//
// Each institution keeps its own payment-scope log in its own database, so this
// answers for one of them and handleBankPaymentAudit answers for a member. A
// reader who wants the whole picture reads several and matches them up by the
// MESSAGES, which is what an auditor holding four banks' logs actually does —
// there is no cross-institution order for a route to serve.
func (s *surface) handlePaymentAudit(r *http.Request) ([]api.AuditEventDTO, error) {
	return api.AuditPage(r.Context(), s.network(), api.AuditFilterFrom(r, payment.ClearingHouseBook, ledger.ScopePayment))
}
