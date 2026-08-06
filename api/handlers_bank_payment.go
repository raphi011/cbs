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
// and no longer on the ref — see payment.PartyRef. The values are the same ones
// this comparison always used; what changed is which field holds them.
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
// The answer is 202 with an identifier rather than 201 with the payment, and as
// of sub-project 7b that is TRUE rather than anticipatory. 6a chose the shape
// ahead of the behaviour, because a real CSM answers with a pacs.002 later and
// not by return value; the mesh is what made it so. When this response is
// written the payment is Initiated, in no cycle, and the counterparty has not
// seen it. Ask again with the identifier.
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
	// A scheme nobody has registered cannot say which bank submits it, so it is
	// refused here rather than falling through to the push rule — which would
	// answer "a credit transfer is submitted by the payer's bank and a direct
	// debit by the payee's" about a scheme that is neither and does not exist.
	// SubmitPayment refuses it too; this is only about refusing it for the
	// right reason.
	sc, ok := s.network().Scheme(dom.Scheme)
	if !ok {
		writeError(w, payment.ErrSchemeNotFound)
		return
	}
	submitter := dom.DebtorDetails.Agent
	if sc.Direction() == payment.Pull {
		submitter = dom.CreditorDetails.Agent
	}
	// An OMITTED submitting agent is the ORDINARY case, and this used to refuse it
	// as a missing required field.
	//
	// The field it read was the submitter's own PARTICIPANT, which an instruction
	// had to name because that is what the payment recorded. A payment records
	// agents now (payment.PartyRef), and the submitting bank's own agent is
	// discarded and refilled from its own row — a payer does not get to rename
	// their own bank — so an instruction that leaves it out is a correct
	// instruction and refusing it would refuse every one a customer hands in.
	//
	// What the refusal was FOR has not gone anywhere; it has stopped needing to be
	// asked. The scoping is structural: this listener's network is its bank's, and
	// the submitting side's account is resolved in that bank's own register
	// (payment.checkPartyTx), so an instruction whose payer banks elsewhere finds
	// no account rather than being caught here.
	//
	// So the check survives only for a caller that DID name its own side — the
	// seed and this package's fixtures do — where the diagnosis is still worth
	// more than what the domain would say two frames further in.
	if submitter != "" && submitter != s.boundBIC() {
		writeUnprocessable(w, "this bank does not submit this payment: a credit transfer is submitted by the payer's bank and a direct debit by the payee's")
		return
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
