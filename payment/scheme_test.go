package payment_test

import (
	"testing"

	"github.com/raphi011/cbs/iso20022"
	. "github.com/raphi011/cbs/payment"
)

// The parts a payment's two agents play, over the two schemes there are and the
// deployment where one bank is both sides. Five call sites read this rule and
// none of them may answer it differently.
func TestTheAgentsPlayTheSamePartsAtEveryCallSite(t *testing.T) {
	const dr, cr iso20022.BIC = "BANKDEFFXXX", "BANKGB2LXXX"

	cases := []struct {
		name                                       string
		scheme                                     Scheme
		debtorAgent, creditorAgent                 iso20022.BIC
		submitter, receiver, returner, counterpart iso20022.BIC
	}{
		// A push is submitted by the payer's bank, so the payee's answers it and
		// is the one that would send it back.
		{"a credit transfer between two banks", SCT{}, dr, cr, dr, cr, cr, cr},
		// A pull is the mirror: the collecting bank submits and the payer's bank
		// answers.
		{"a collection between two banks", SDD{}, dr, cr, cr, dr, dr, dr},
		// One bank on both sides plays every part itself, which is what an on-us
		// payment through the scheme is.
		{"a credit transfer inside one bank", SCT{}, dr, dr, dr, dr, dr, dr},
		{"a collection inside one bank", SDD{}, dr, dr, dr, dr, dr, dr},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SubmitterOf(c.scheme, c.debtorAgent, c.creditorAgent); got != c.submitter {
				t.Errorf("the submitting side = %s, want %s", got, c.submitter)
			}
			if got := ReceiverOf(c.scheme, c.debtorAgent, c.creditorAgent); got != c.receiver {
				t.Errorf("the receiving side = %s, want %s", got, c.receiver)
			}
			if got := ReturnerOf(c.scheme, c.debtorAgent, c.creditorAgent); got != c.returner {
				t.Errorf("the returning side = %s, want %s", got, c.returner)
			}
			got := CounterpartyOf(SubmitterOf(c.scheme, c.debtorAgent, c.creditorAgent), c.debtorAgent, c.creditorAgent)
			if got != c.counterpart {
				t.Errorf("the submitter's counterparty = %s, want %s", got, c.counterpart)
			}
		})
	}
}

// The party that receives a payment is the party whose bank sends it back, so
// the two acts read the same rule and a change to one is a change to both.
func TestTheReceivingSideIsTheReturningSide(t *testing.T) {
	const dr, cr iso20022.BIC = "BANKDEFFXXX", "BANKGB2LXXX"
	for _, scheme := range []Scheme{SCT{}, SDD{}} {
		receiver := ReceiverOf(scheme, dr, cr)
		if returner := ReturnerOf(scheme, dr, cr); returner != receiver {
			t.Errorf("%s: the returning side is %s and the receiving side is %s", scheme.ID(), returner, receiver)
		}
	}
}
