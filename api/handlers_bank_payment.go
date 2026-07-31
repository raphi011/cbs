package api

import (
	"net/http"

	"github.com/raphi011/cbs/payment"
)

// A bank's view of the payment network: its own legs and nothing else.
//
// The single server could not express this. GET /payments listed every payment
// to every caller — competitors' customers, their counterparties and their
// amounts — because narrowing needs a caller identity and there was none. The
// port is that identity now.

// isParty reports whether this listener's bank is one end of the payment.
// Either end: a bank's own payments are the ones it sent AND the ones it
// received, not just the debits.
func (s *Server) isParty(p payment.Payment) bool {
	return p.Debtor.Participant == s.boundPID || p.Creditor.Participant == s.boundPID
}

func (s *Server) handleListBankPayments(w http.ResponseWriter, r *http.Request) {
	payments, err := s.network().ListPayments(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	schemes := s.network().ListSchemes()
	// Built with a zero length rather than len(payments) so a bank that is party
	// to nothing answers [] and not a list of empty objects.
	out := make([]paymentDTO, 0, len(payments))
	for _, p := range payments {
		if s.isParty(p) {
			out = append(out, toPaymentDTO(p, schemes))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetBankPayment answers 404 — not 403 — for a payment this bank is not
// party to. A payment it cannot see does not exist as far as its API is
// concerned, and a 403 would confirm that the id names something real.
func (s *Server) handleGetBankPayment(w http.ResponseWriter, r *http.Request) {
	p, err := s.network().GetPayment(r.Context(), payment.PaymentID(r.PathValue("payid")))
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.isParty(p) {
		writeError(w, payment.ErrPaymentNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentDTO(p, s.network().ListSchemes()))
}
