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
//
// Compared against the AGENTS, because a payment names each side's bank there
// and not on the ref — see payment.PartyRef.
func (s *Server) isParty(p payment.Payment) bool {
	return p.DebtorDetails.Agent == s.boundBIC() || p.CreditorDetails.Agent == s.boundBIC()
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

// submittedPaymentDTO is what a bank answers a customer's instruction with: an
// identifier to ask about, not an outcome.
//
// Named for submission and not for acceptance, because since the split the two
// are different things and this is the first: the 202 is HTTP's "I have taken
// this in", and the payment it names is Initiated, in no cycle, and not yet
// seen by the counterparty. Calling the type accepted — as it was while
// submission produced an Accepted payment — now reads as a claim the response
// does not make.
//
// handleReturnPayment answers with it too, for the same reason and with less
// choice: a return's outcome is decided four hops away and there is no
// intermediate resource to describe at all.
type submittedPaymentDTO struct {
	PaymentID string `json:"paymentId"`
}

// handleSubmitPayment accepts a payment instruction from this bank's own
// customer.
//
// A customer's client must never talk to the clearing house — it has no CSM
// connection in the real thing either — so submission lands on the bank, and
// the bank is what forwards it.
//
// The answer is 202 with an identifier rather than 201 with the payment,
// because a real CSM answers with a pacs.002 later and not by return value. When
// this response is written the payment is Initiated, in no cycle, and the
// counterparty has not seen it. Ask again with the identifier.
//
// # Which failures reach the caller
//
// Mesh.Submit runs this bank's own half synchronously, so what THIS bank decides
// is answerable here and now: no funds, an account that is not the payer's, a
// duplicate reference, a mandate that does not authorise the collection. Those
// are the 4xx below.
//
// What cannot come back is anything the far side decides. The payee's bank
// refusing the credit, the clearing house having no window open, the settlement
// agent short of reserves — each of those is another institution answering a
// message, long after this response was written, and each lands on the payment's
// own row instead. A failure that reached nobody at all is a mesh dead letter,
// which is the point of Drain returning them.
func (s *Server) handleSubmitPayment(w http.ResponseWriter, r *http.Request) {
	var req initiatePaymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	dom := req.toDomain()
	// A bank submits on behalf of its own customer and nobody else's, and this
	// listener IS that bank. This is scoping, not authorization: it says which
	// instructions this port is for, and verifies nothing about who is calling it.
	//
	// Which side is this bank's depends on the scheme's direction. A push is
	// submitted by the payer's bank; a PULL is submitted by the CREDITOR's bank,
	// because a direct debit is a collection the payee's bank initiates.
	//
	// A scheme nobody has registered cannot say which side that is, so it is
	// refused here rather than falling through to the push rule — which would
	// answer "a credit transfer is submitted by the payer's bank and a direct
	// debit by the payee's" about a scheme that is neither and does not exist.
	// SubmitPayment refuses it too; this is only about refusing it for the right
	// reason.
	sc, ok := s.network().Scheme(dom.Scheme)
	if !ok {
		writeError(w, payment.ErrSchemeNotFound)
		return
	}
	// The submitting side's agent comes from the PORT and from nowhere else.
	//
	// There is no field on an instruction for it, so there is nothing here to
	// disagree with: a payer does not name their own bank any more than they name
	// the payee's, and SubmitPaymentTx refills this from the bank's own row a
	// moment later. What it is doing here is earlier than that — submitterOf picks
	// the ACTOR off this field before any bank's half runs, so an instruction that
	// reached the mesh without it came back "no bank actor for".
	//
	// The COUNTERPARTY's side stays empty, and stays that way through the mesh: it
	// is derived from the counterparty's address inside the submitting bank's own
	// unit of work. See payment.Network.routeTx.
	if sc.Direction() == payment.Pull {
		dom.CreditorDetails.Agent = s.boundBIC()
	} else {
		dom.DebtorDetails.Agent = s.boundBIC()
	}
	// The submitting bank's half, and then the send. Mesh.Submit runs it on this
	// goroutine and marks it as this bank's work, so the books it touches are
	// attributed to the bank and not to whoever called in; the pacs.008 or
	// pacs.003 goes out after the unit of work has committed, never inside it.
	p, err := s.mesh.Submit(r.Context(), dom)
	if err != nil {
		// See handleInitiatePayment: the payment comes back beside the error
		// when the submission committed and the message did not go out, and a
		// 5xx carries no room for it.
		if p.ID != "" {
			s.log.Error("api: a submission committed and its instruction did not go out",
				"payment", p.ID, "status", p.Status, "error", err)
		}
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, submittedPaymentDTO{PaymentID: string(p.ID)})
}
