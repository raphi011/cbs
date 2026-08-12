package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

// A bank's view of the payment network: its own legs and nothing else.

// isParty reports whether this listener's bank is one end of the payment.
// Either end: a bank's own payments are the ones it sent AND the ones it
// received, not just the debits.
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
func (s *surface) handleCutoff(r *http.Request) (api.CutoffDTO, error) {
	orders, err := s.inst.Cutoff(r.Context())
	if err != nil {
		// A cut-off HALF-HAPPENS when a bank operates two schemes and one file went
		// out: the order ids come back beside the error, and a 5xx has no room for
		// them.
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
func (s *surface) handleSubmitPayment(r *http.Request, req api.InitiatePaymentRequest) (api.SubmittedPaymentDTO, error) {
	dom := req.ToDomain()
	// A bank submits on behalf of its own customer and nobody else's, and this
	// listener IS that bank. This is scoping, not authorization: it says which
	// instructions this port is for, and verifies nothing about who is calling it.
	sc, ok := s.network().Scheme(dom.Scheme)
	if !ok {
		return api.SubmittedPaymentDTO{}, payment.ErrSchemeNotFound
	}
	// The submitting side's agent comes from the PORT and from nowhere else.
	if sc.Direction() == payment.Pull {
		dom.CreditorDetails.Agent = s.boundBIC()
	} else {
		dom.DebtorDetails.Agent = s.boundBIC()
	}
	// The submitting bank's half, and then the hub.
	p, err := s.inst.Submit(r.Context(), dom)
	if err != nil {
		return api.SubmittedPaymentDTO{}, err
	}
	return api.SubmittedPaymentDTO{PaymentID: string(p.ID)}, nil
}
