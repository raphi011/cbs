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

// acceptedPaymentDTO is what a bank answers a customer's instruction with: an
// identifier to ask about, not an outcome.
type acceptedPaymentDTO struct {
	PaymentID string `json:"paymentId"`
}

// handleSubmitPayment accepts a payment instruction from this bank's own
// customer.
//
// A customer's client must never talk to the clearing house — it has no CSM
// connection in the real thing either — so submission lands on the bank, and
// the bank is what forwards it.
//
// The answer is 202 with an identifier rather than 201 with the payment, even
// though the handler is synchronous today. Sub-project 7b converts submission
// to exactly this shape, because a real CSM answers with a pacs.002 later and
// not by return value; a client built against a synchronous "payment created"
// response would have to be rewritten, and one built against "here is an
// identifier, ask again" will not be.
func (s *Server) handleSubmitPayment(w http.ResponseWriter, r *http.Request) {
	var req initiatePaymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	dom := req.toDomain()
	// A bank submits on behalf of its own customer and nobody else's. This is
	// scoping, not authorization: it says which instructions this listener is
	// for, and verifies nothing about who is calling it.
	//
	// Which bank may submit depends on the scheme's direction. A push is
	// submitted by the payer's bank; a PULL is submitted by the CREDITOR's
	// bank, because a direct debit is a collection the payee's bank initiates.
	//
	// Requiring the debtor unconditionally — which is what this did before —
	// was the wrong bank for every direct debit. It was invisible while one
	// process validated both ends regardless of who called.
	submitter := dom.Debtor.Participant
	if sc, ok := s.network().Scheme(dom.Scheme); ok && sc.Direction() == payment.Pull {
		submitter = dom.Creditor.Participant
	}
	if submitter != s.boundPID {
		writeUnprocessable(w, "this bank does not submit this payment: a credit transfer is submitted by the payer's bank and a direct debit by the payee's")
		return
	}
	// The submitting bank's half and nothing else. The payment comes back
	// Initiated and in no cycle; the counterparty's answer and the clearing
	// house's acceptance arrive later, which is exactly what the 202 below has
	// been promising since 6a.
	p, err := s.network().SubmitPayment(r.Context(), dom)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, acceptedPaymentDTO{PaymentID: string(p.ID)})
}
