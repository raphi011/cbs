package storetest

import (
	"context"
	"maps"
	"slices"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// Admit puts one bank through the four acts of an admission, one unit of work
// each, with nothing carrying a message between them.
//
// # Why it lives here
//
// Several suites need a bank that is a Member, and none of them is testing the
// conversation: payment's own suites are about what each act does, mesh's
// operator and roster suites want a member to act on, and RunRaces in this
// package races the acts themselves. A helper each would be one copy per suite
// of a composition that must not drift.
//
// It moved here from store/testenv at Task 17.0, and the reason is a constraint
// rather than a preference: RunRaces needs it, and this package must not import
// an implementation, because the implementation's tests import this package.
// Nothing about the composition itself is backend-specific — it takes a
// payment.Networks and never names a store — which is why the move was a move
// and not a rewrite.
//
// The conversation itself is tested where it happens, in mesh: mesh's harness
// drives Mesh.Admit and drains, and the tests in mesh/admission_test.go are what
// assert on the messages and the states.
//
// # What it is NOT
//
// It is not what the system does. Admission is three institutions and four
// commits with an acmt.007, an acmt.010 and an acmt.011 between them, and the
// isolation those messages impose is the whole point of the split — see
// mesh.Mesh.Admit and payment's four acts. What this does is call each act in
// the order the messages would produce, on one goroutine, with no mesh at all.
// A test that needs the conversation must not use this.
//
// It is also the reason the payment suites still read as they did: every test
// that used to say "add a participant" says this instead, and what it gets back
// is the same Member bank the atomic call produced.
//
// # The admission reference
//
// Derived from the BIC rather than left empty. There is no process id because
// there is no process, and a value unique per admission is still what the
// clearing house's refusal compares — an empty one on every bank would make two
// banks on one address look like one admission, which is exactly the case
// payment.ErrBICAlreadyAdmitted exists for. A caller that wants two banks to
// clash on one address gets the refusal, which is the truth about that request.
// # It names which institution performs each act, and Task 18b is why
//
// One *payment.Network used to play all four. It cannot now: opening a reserve
// account needs the central bank's book and recording a membership needs the
// joining bank's own register, and neither is reachable from the other's
// network. So the acts below are spelled against the institution whose act each
// is — which is what the four MESSAGES say too, and the closest this stand-in
// gets to being honest about the conversation it is standing in for.
//
// Founding used to be the exception, and it is not one any more. It ran through
// the CLEARING HOUSE's network — because a bank with a counter-derived id had no
// handle of its own until the id existed — and that was a crossing carried here
// and on mesh.Mesh.Admit with "same fate at 18c" against it. The fate arrived: a
// bank's id is its BIC, so the applicant's own network can be minted from the
// address the caller already passed in, and every one of the four acts below is
// now performed by the institution whose act it is.
func Admit(ctx context.Context, nets *payment.Networks, name string, bic iso20022.BIC,
	assets []ledger.AssetCode) (*payment.Bank, error) {

	// The APPLICANT's own network, over the applicant's own database, which
	// asking for it is what creates. See payment.Stores.
	applicant, err := nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return nil, err
	}
	bank, err := applicant.FoundBank(ctx, name, bic, assets)
	if err != nil {
		return nil, err
	}
	// The joining default is payment's and is applied at founding, so the assets
	// to ask for are the ones the bank came out with rather than the ones the
	// caller named. One request per asset, because one acmt.007 asks for one
	// currency, and in asset order so that two identical admissions allocate the
	// central bank's account ids in the same sequence — Go randomises map
	// iteration, and a fixture whose ids moved between runs would be a fixture
	// nothing could assert on.
	var member payment.SettlementMember
	for _, asset := range slices.Sorted(maps.Keys(bank.Assets)) {
		if member, err = nets.CentralBank().OpenSettlementAccount(ctx, payment.AdmissionRequest{
			Name: name, BIC: bic, Asset: asset, Ref: admissionRef(bic),
		}); err != nil {
			return nil, err
		}
	}
	// Each answer carries the servicer's whole account set for the address, so
	// the last is the one the other two acts read — which is true of the real
	// conversation too: a bank's second acknowledgement lists both accounts.
	ack := payment.AdmissionAcknowledgement{
		BIC: bic, Accounts: member.Accounts, Ref: admissionRef(bic),
	}
	if _, err := nets.ClearingHouse().AdmitMember(ctx, ack); err != nil {
		return nil, err
	}
	return applicant.RecordMembership(ctx, ack)
}

// admissionRef is the process id this stand-in quotes. See Admit.
func admissionRef(bic iso20022.BIC) string { return "admitted-" + string(bic) }
