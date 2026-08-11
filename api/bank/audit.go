package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/ledger"
)

// handleLedgerAudit serves one bank's general-ledger audit trail.
func (s *surface) handleLedgerAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	api.WriteAudit(w, r, s.network(), api.AuditFilterFrom(r, p.BookID, ledger.ScopeLedger))
}

// handleDepositAudit serves one bank's deposit-layer audit trail. Same book as
// the ledger trail above; only the scope differs.
func (s *surface) handleDepositAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	api.WriteAudit(w, r, s.network(), api.AuditFilterFrom(r, p.BookID, ledger.ScopeDeposit))
}

// handleBankPaymentAudit serves ONE bank's payment-scope trail, in its own book:
// its founding, the membership it recorded, the mandates it holds, and its own
// side of every payment it is a party to.
//
// It is on the bank's surface because those events are in the bank's database
// and in no other. A bank asking the clearing house's route above gets the
// clearing house's log, which does not mention its mandates at all.
func (s *surface) handleBankPaymentAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	api.WriteAudit(w, r, s.network(), api.AuditFilterFrom(r, p.BookID, ledger.ScopePayment))
}
