package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

// A bank's view of the payment network: its own legs and nothing else.
//
// Narrowing needs a caller identity, and the port is it. An unnarrowed
// GET /payments would list every payment to every caller — competitors'
// customers, their counterparties and their amounts — which is exactly what the
// clearing house's own surface serves, to the one operator entitled to it.

// isParty reports whether this listener's bank is one end of the payment.
// Either end: a bank's own payments are the ones it sent AND the ones it
// received, not just the debits.
//
// Compared against the AGENTS, because a payment names each side's bank there
// and not on the ref — see payment.PartyRef.
func (s *surface) isParty(p payment.Payment) bool {
	return p.DebtorDetails.Agent == s.boundBIC() || p.CreditorDetails.Agent == s.boundBIC()
}

func (s *surface) handleListBankPayments(r *http.Request) ([]api.PaymentDTO, error) {
	payments, err := s.network().ListPayments(r.Context())
	if err != nil {
		return nil, err
	}
	schemes := s.network().ListSchemes()
	// Built with a zero length rather than len(payments) so a bank that is party
	// to nothing answers [] and not a list of empty objects.
	out := make([]api.PaymentDTO, 0, len(payments))
	for _, p := range payments {
		if s.isParty(p) {
			out = append(out, api.ToPaymentDTO(p, schemes))
		}
	}
	return out, nil
}

// handleGetBankPayment answers 404 — not 403 — for a payment this bank is not
// party to. A payment it cannot see does not exist as far as its API is
// concerned, and a 403 would confirm that the id names something real.
func (s *surface) handleGetBankPayment(r *http.Request) (api.PaymentDTO, error) {
	p, err := s.network().GetPayment(r.Context(), payment.PaymentID(r.PathValue("payid")))
	if err != nil {
		return api.PaymentDTO{}, err
	}
	if !s.isParty(p) {
		return api.PaymentDTO{}, payment.ErrPaymentNotFound
	}
	return api.ToPaymentDTO(p, s.network().ListSchemes()), nil
}

// handleListPendingPayments answers what this bank's hub is holding: the
// instructions it has taken and will put in its next file.
//
// It is what makes a 202 legible. Every payment here is Initiated and so is
// every payment that has been uploaded and not yet answered, and the two are
// indistinguishable on a payment's own row — the difference is whether a file
// carrying it has left the building, which is a fact about the hub and about
// nothing in the store.
func (s *surface) handleListPendingPayments(r *http.Request) ([]api.PaymentDTO, error) {
	pending, err := s.inst.Pending(r.Context())
	if err != nil {
		return nil, err
	}
	schemes := s.network().ListSchemes()
	out := make([]api.PaymentDTO, 0, len(pending))
	for _, p := range pending {
		out = append(out, api.ToPaymentDTO(p, schemes))
	}
	return out, nil
}

// handleCutoff is the operator reaching this bank's cut-off out of turn.
//
// A business day reaches the same one on every settlement day, which is where a
// cut-off belongs — it is an event on a calendar. This route is what a bank does
// when it wants its morning's instructions to go now, and it is the same
// relationship POST /cycles/{cid}/close has with the clearing house's own
// cut-off.
//
// The answer is 202: the files are uploaded and the clearing house has not
// looked at them, so what comes back is a receipt and not an outcome.
func (s *surface) handleCutoff(r *http.Request) (api.CutoffDTO, error) {
	orders, err := s.inst.Cutoff(r.Context())
	if err != nil {
		// A cut-off HALF-HAPPENS when a bank operates two schemes and one file
		// went out: the order ids come back beside the error, and a 5xx has no
		// room for them. What the instructions behind the failed file did is
		// safe — they are back in the hub and the next cut-off tries again — so
		// what is lost is only the operator's receipt, which is logged here
		// because this is the last frame that holds both.
		if len(orders) > 0 {
			s.inst.Log().Error("api: part of a cut-off went out and part did not",
				"orders", orders, "error", err)
		}
		return api.CutoffDTO{}, err
	}
	out := api.CutoffDTO{OrderIDs: make([]string, 0, len(orders))}
	for _, id := range orders {
		out.OrderIDs = append(out.OrderIDs, string(id))
	}
	return out, nil
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
// Deployment.Submit runs this bank's own half synchronously, so what THIS bank decides
// is answerable here and now: no funds, an account that is not the payer's, a
// duplicate reference, a mandate that does not authorise the collection. Those
// are the 4xx below.
//
// What cannot come back is anything the far side decides. The payee's bank
// refusing the credit, the clearing house having no window open, the settlement
// agent short of reserves — each of those is another institution answering a
// file, long after this response was written, and each lands on the payment's
// own row instead. A failure that reached nobody at all is a line in the day's
// report.
//
// # What the 202 says, and what it does not
//
// The instruction is in this bank's hub. It has not left the building and will
// not until a cut-off, so the counterparty has not merely failed to answer yet —
// it has not been told. GET /payments/pending is what distinguishes the two.
func (s *surface) handleSubmitPayment(r *http.Request, req api.InitiatePaymentRequest) (api.SubmittedPaymentDTO, error) {
	dom := req.ToDomain()
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
		return api.SubmittedPaymentDTO{}, payment.ErrSchemeNotFound
	}
	// The submitting side's agent comes from the PORT and from nowhere else.
	//
	// There is no field on an instruction for it, so there is nothing here to
	// disagree with: a payer does not name their own bank any more than they name
	// the payee's, and SubmitPaymentTx refills this from the bank's own row a
	// moment later. What it is doing here is earlier than that — submitterOf picks
	// the ACTOR off this field before any bank's half runs, so an instruction that
	// reached the deployment without it came back "holds no bank at".
	//
	// The COUNTERPARTY's side stays empty, and stays that way on the wire: it
	// is derived from the counterparty's address inside the submitting bank's own
	// unit of work. See payment.Network.routeTx.
	if sc.Direction() == payment.Pull {
		dom.CreditorDetails.Agent = s.boundBIC()
	} else {
		dom.DebtorDetails.Agent = s.boundBIC()
	}
	// The submitting bank's half, and then the hub. Deployment.Submit runs it on
	// this goroutine and marks it as this bank's work, so the books it touches are
	// attributed to the bank and not to whoever called in; the file the
	// instruction travels in is built at the next cut-off, never here.
	p, err := s.inst.Submit(r.Context(), dom)
	if err != nil {
		return api.SubmittedPaymentDTO{}, err
	}
	return api.SubmittedPaymentDTO{PaymentID: string(p.ID)}, nil
}
