package storetest

import (
	"context"
	"maps"
	"slices"

	"github.com/raphi011/cbs/iban"
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
// It lives here rather than in store/testenv because RunRaces needs it and this
// package must not import an implementation, since the implementation's tests
// import this package. Nothing about the composition is backend-specific: it
// takes a payment.Networks and never names a store.
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
// Every test that wants "a bank in the scheme" says this, and what it gets back
// is a Member bank.
//
// # The bank code is really allocated
//
// The settlement agent allocates it, exactly as it does in the conversation, and
// the acknowledgement below carries it to the other two acts. There used to be a
// stand-in here that hashed the BIC, because no act allocated anything; what
// that could not do is refuse a collision, which is precisely what a registry is
// for. Every fixture bank is admitted in ONE country, and deliberately not its
// BIC's — the suites use addresses like BANKESMMXXX, in countries this system
// keeps no IBAN structure for, and an account's country is not its bank's in any
// case (see iban.Country). One country keeps the fixtures uniform and says
// nothing untrue.
//
// # The admission reference
//
// Derived from the BIC rather than left empty. There is no process id because
// there is no process, and a value unique per admission is still what the
// clearing house's refusal compares — an empty one on every bank would make two
// banks on one address look like one admission, which is exactly the case
// payment.ErrBICAlreadyAdmitted exists for. A caller that wants two banks to
// clash on one address gets the refusal, which is the truth about that request.
//
// # It names which institution performs each act
//
// One *payment.Network cannot play all four: opening a reserve account needs the
// central bank's book and recording a membership needs the joining bank's own
// register, and neither is reachable from the other's network. So the acts below
// are spelled against the institution whose act each is — which is what the four
// MESSAGES say too.
//
// Founding is no exception. A bank's id is its BIC, so the applicant's own
// network is minted from the address the caller already passed in.
func Admit(ctx context.Context, nets *payment.Networks, name string, bic iso20022.BIC,
	assets []ledger.AssetCode) (*payment.Bank, error) {

	// The APPLICANT's own network, over the applicant's own database, which
	// asking for it is what creates. See payment.Stores.
	applicant, err := nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return nil, err
	}
	bank, err := applicant.FoundBank(ctx, name, bic, FixtureCountry, assets)
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
	var (
		member payment.SettlementMember
		issuer iban.Issuer
	)
	for _, asset := range slices.Sorted(maps.Keys(bank.Assets)) {
		if member, issuer, err = nets.CentralBank().OpenSettlementAccount(ctx, payment.AdmissionRequest{
			Name: name, BIC: bic, Country: FixtureCountry, Asset: asset, Ref: admissionRef(bic),
		}); err != nil {
			return nil, err
		}
	}
	// Each answer carries the servicer's whole account set for the address and
	// the one allocation it made, so the last is the one the other two acts read
	// — which is true of the real conversation too: a bank's second
	// acknowledgement lists both accounts and repeats the code.
	ack := payment.AdmissionAcknowledgement{
		BIC: bic, Issuer: issuer, Accounts: member.Accounts, Ref: admissionRef(bic),
	}
	if _, err := nets.ClearingHouse().AdmitMember(ctx, ack); err != nil {
		return nil, err
	}
	return applicant.RecordMembership(ctx, ack)
}

// admissionRef is the process id this stand-in quotes. See Admit.
func admissionRef(bic iso20022.BIC) string { return "admitted-" + string(bic) }

// FixtureCountry is the register every fixture bank applies to. See Admit.
const FixtureCountry = iban.DE
