// Package flow drives a conversation between institutions, for a caller that IS
// the deployment: the seed and the suites. No institution may call it, and
// nothing on the transport path does.
package flow

import (
	"context"
	"errors"
	"fmt"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// Every act here is the institution's own, in its own unit of work. This
// package opens no transaction, holds no store and takes no lock; what it owns
// is the ORDER, which is otherwise written out by each caller that drives it.

// Initiate is a payment's route into a cycle: the submitting bank's leg, the
// receiving bank's, the clearing house's copy and its decision, and the
// submitting bank told what was decided.
func Initiate(ctx context.Context, nets *payment.Networks, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	csm := nets.ClearingHouse()
	scheme, ok := csm.Scheme(req.Scheme)
	if !ok {
		return payment.Payment{}, fmt.Errorf("flow: no scheme %q to say which bank submits: %w",
			req.Scheme, payment.ErrSchemeNotFound)
	}
	submitterBIC := payment.SubmitterOf(scheme, req.DebtorDetails.Agent, req.CreditorDetails.Agent)
	submitter, err := bank(ctx, nets, submitterBIC)
	if err != nil {
		return payment.Payment{}, err
	}
	p, err := submitter.SubmitPayment(ctx, req)
	if err != nil {
		return payment.Payment{}, err
	}

	// What the OTHER two institutions are sent, and the whole of what they get.
	// Taken from the PAYMENT rather than the request: the payer's address was
	// back-filled before the instruction went out, so it travels with it.
	relayed := relayedFrom(p)

	// The receiving bank resolves an address in its OWN register, so which bank it
	// is has to be named here rather than looked up there.
	receiver, err := bank(ctx, nets, payment.CounterpartyOf(submitterBIC, p.DebtorDetails.Agent, p.CreditorDetails.Agent))
	if err != nil {
		return payment.Payment{}, err
	}
	if err := receiver.AcceptInbound(ctx, p.ID, relayed); err != nil {
		return payment.Payment{}, err
	}

	// The clearing house has to be CARRYING the payment before it can take it into
	// a cycle. In a deployment that is the moment it routes the instruction on;
	// here there is nothing to route, so the record stands alone.
	if _, err := csm.RecordRelayed(ctx, []payment.InboundTransaction{{ID: p.ID, Request: relayed}}); err != nil {
		return payment.Payment{}, err
	}
	out, err := csm.AcceptAtCSM(ctx, p.ID)
	if err != nil {
		return payment.Payment{}, err
	}

	// And the submitting bank is TOLD, which is the ACCP a deployment sends it and
	// the act that takes its own copy off Initiated.
	if _, err := submitter.AcceptAtBank(ctx, p.ID); err != nil {
		return payment.Payment{}, err
	}
	return out, nil
}

// Reject is the clearing house's refusal and each party's record of it. Only
// the payer's bank gives money back, and a bank holding no copy is skipped: a
// refusal before the relay leaves the far bank with nothing.
func Reject(ctx context.Context, nets *payment.Networks, id payment.PaymentID,
	code iso20022.StatusReason, reason string) (payment.Payment, error) {

	out, err := nets.ClearingHouse().RejectAtCSM(ctx, id, code, reason)
	if err != nil {
		return payment.Payment{}, err
	}
	for _, bic := range agentsOf(out) {
		b, err := bank(ctx, nets, bic)
		if err != nil {
			return payment.Payment{}, err
		}
		if _, err := b.GetPayment(ctx, id); errors.Is(err, payment.ErrPaymentNotFound) {
			continue
		}
		if _, err := b.RejectAtBank(ctx, id, code, reason); err != nil {
			return payment.Payment{}, err
		}
	}
	return out, nil
}

// relayedFrom is the instruction the other two institutions are sent about a
// payment its own bank has just submitted.
func relayedFrom(p payment.Payment) payment.InitiatePaymentRequest {
	return payment.InitiatePaymentRequest{
		Scheme:          p.Scheme,
		Debtor:          p.Debtor,
		Creditor:        p.Creditor,
		Amount:          p.Amount,
		MandateID:       p.MandateID,
		EndToEndID:      p.EndToEndID,
		Description:     p.Description,
		Metadata:        p.Metadata,
		DebtorDetails:   p.DebtorDetails,
		CreditorDetails: p.CreditorDetails,
	}
}

// agentsOf is a payment's two agents, or the one bank that is both of them.
func agentsOf(p payment.Payment) []iso20022.BIC {
	if p.DebtorDetails.Agent == p.CreditorDetails.Agent {
		return []iso20022.BIC{p.DebtorDetails.Agent}
	}
	return []iso20022.BIC{p.DebtorDetails.Agent, p.CreditorDetails.Agent}
}

// bank is one member's handle, over that member's own database.
func bank(ctx context.Context, nets *payment.Networks, bic iso20022.BIC) (*payment.BankNetwork, error) {
	return nets.Bank(ctx, payment.ParticipantID(bic))
}
