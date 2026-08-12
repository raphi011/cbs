package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// handleLedgerAudit serves one bank's general-ledger audit trail.
func (s *surface) handleLedgerAudit(r *http.Request, p *payment.Bank) ([]api.AuditEventDTO, error) {
	return api.AuditPage(r.Context(), s.network(), api.AuditFilterFrom(r, p.BookID, ledger.ScopeLedger))
}

// handleDepositAudit serves one bank's deposit-layer audit trail. Same book as
// the ledger trail above; only the scope differs.
func (s *surface) handleDepositAudit(r *http.Request, p *payment.Bank) ([]api.AuditEventDTO, error) {
	return api.AuditPage(r.Context(), s.network(), api.AuditFilterFrom(r, p.BookID, ledger.ScopeDeposit))
}

// handleBankPaymentAudit serves ONE bank's payment-scope trail, in its own
// book: its founding, the membership it recorded, the mandates it holds, and
// its own side of every payment it is a party to.
func (s *surface) handleBankPaymentAudit(r *http.Request, p *payment.Bank) ([]api.AuditEventDTO, error) {
	return api.AuditPage(r.Context(), s.network(), api.AuditFilterFrom(r, p.BookID, ledger.ScopePayment))
}
