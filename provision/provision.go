// Package provision writes the rows that put a bank in the scheme.
//
// A bank in this system is three rows in three databases: its own, in its own
// book; a settlement account in the central bank's; a roster entry in the
// clearing house's. Each is one institution's own act over its own network, and
// no statement spans two databases. What is missing is anything to carry between
// them, and a provisioner is what supplies it.
//
// # It is not an institution
//
// It has the same standing as payment/recon: it opens all N+2 databases
// precisely because no actor in the system may. What it does NOT do is write in
// anybody's book on their behalf — it calls each institution's own act against
// that institution's own network, one unit of work each. A component that
// reached across two would be the thing the split exists to refuse.
//
// # It is not a payment network either
//
// It puts nothing on a wire, gives no bank an actor and opens no listener. A
// deployment that wants a bank other banks can reach provisions first and starts
// second, because what makes a bank reachable is a port and an inbox, and
// neither is a row. See mesh.Mesh.AddBank and cmd/server.
//
// # The gap between the acts
//
// RecordMembership needs the account numbers the central bank allocated, so it
// cannot commit until the central bank has: the joining bank's own book commits
// twice, with another institution's commit in between. A crash in that gap
// leaves a bank with rows in one book and not the others. That is a provisioning
// failure to retry and a payment/recon finding — it is not a state the domain
// names, and nothing here exposes it as one.
package provision

import (
	"context"
	"maps"
	"slices"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// BankSpec is everything a deployment decides about one bank.
//
// It is the whole input: no id, no bank code, no account number. A bank's id is
// its BIC, and the code and the accounts are the settlement agent's to allocate
// — a spec that named them would be a deployment choosing what a registry is for.
//
// Country is the register the bank applies to, and it is not derived from the
// BIC: an institution addressed in one country can hold a code in another, and
// an account's country is not its bank's in any case (see iban.Country).
type BankSpec struct {
	Name    string
	BIC     iso20022.BIC
	Country iban.Country
	// Assets the bank asks for a settlement account in. Empty takes payment's
	// joining default, which founding applies.
	Assets []ledger.AssetCode
}

// Bank puts one bank through the four acts, one unit of work each, and returns
// the bank's own row as its own book holds it at the end.
//
// # The order is load-bearing
//
// Founding, then the settlement account, then the roster entry, then the
// membership. The settlement account comes before the routing entry because a
// scheme will not route to a member that cannot settle: an entry published for a
// bank with no reserve would be an address other members could send to and the
// cycle could not pay out of. The membership comes last because it is the
// joining bank's note of what the other two allocated, and until they have, there
// is nothing to note.
//
// # One request per asset, in asset order
//
// One settlement account is one asset's, so a bank in two assets applies twice.
// The order is sorted rather than the map's, because Go randomises map iteration
// and the central bank allocates account ids in the sequence it is asked — a
// deployment whose ids moved between builds would be one nothing could assert on.
//
// The assets asked for are the ones founding produced rather than the ones the
// spec named, so that the joining default is applied in one place.
//
// # The acknowledgement carries the allocation
//
// Each answer carries the servicer's whole account set for the address and the
// one allocation it made, so the last answer is the one the other two acts read.
//
// # It does not subscribe
//
// A routing directory is a copy each member pulls for itself, and provisioning
// fills nobody's. A bank provisioned after its neighbours last pulled is
// unreachable from them until they pull again, which is the behaviour the
// directory design is for. See Subscribe.
func Bank(ctx context.Context, nets *payment.Networks, spec BankSpec) (*payment.Bank, error) {
	// The APPLICANT's own network, over the applicant's own database, which
	// asking for it is what creates. A bank's id is its BIC, so the network is
	// minted from the address the spec already carries. See payment.Stores.
	applicant, err := nets.Bank(ctx, payment.ParticipantID(spec.BIC))
	if err != nil {
		return nil, err
	}
	bank, err := applicant.FoundBank(ctx, spec.Name, spec.BIC, spec.Country, spec.Assets)
	if err != nil {
		return nil, err
	}
	var (
		member payment.SettlementMember
		issuer iban.Issuer
	)
	for _, asset := range slices.Sorted(maps.Keys(bank.Assets)) {
		if member, issuer, err = nets.CentralBank().OpenSettlementAccount(ctx, payment.AdmissionRequest{
			Name: spec.Name, BIC: spec.BIC, Country: spec.Country, Asset: asset, Ref: Ref(spec.BIC),
		}); err != nil {
			return nil, err
		}
	}
	ack := payment.AdmissionAcknowledgement{
		BIC: spec.BIC, Issuer: issuer, Accounts: member.Accounts, Ref: Ref(spec.BIC),
	}
	if _, err := nets.ClearingHouse().AdmitMember(ctx, ack); err != nil {
		return nil, err
	}
	return applicant.RecordMembership(ctx, ack)
}

// Subscribe delivers the clearing house's published roster to every member in
// it, so that each of them can route to the others.
//
// It is an act of the SUBSCRIBERS and no part of provisioning a bank: a member
// asks for a snapshot and routes from what it holds, so a bank provisioned after
// its neighbours last pulled is unreachable from them until they pull again
// (payment.ErrBankCodeUnknown), and a refresh makes the same payment work.
//
// It refreshes EVERY member rather than the ones a caller names, because a
// directory is published to the whole scheme and a caller choosing which banks
// hold a correct copy would be choosing the answer to whatever it then asserts.
// A caller that wants one bank behind the others drives the refresh itself.
//
// What it leaves behind is a scheme where everybody pulled a moment ago, which
// is not a state the system maintains for anybody.
func Subscribe(ctx context.Context, nets *payment.Networks) error {
	published, err := nets.ClearingHouse().ListRosterEntries(ctx)
	if err != nil {
		return err
	}
	for _, e := range published {
		subscriber, err := nets.Bank(ctx, payment.ParticipantID(e.BIC))
		if err != nil {
			return err
		}
		if _, err := subscriber.RefreshDirectory(ctx, published); err != nil {
			return err
		}
	}
	return nil
}

// Ref is the admission reference a provisioned bank is recorded under.
//
// It is derived from the BIC rather than left empty. There is no process id
// because there is no process, and a value unique per bank is still what the
// clearing house's refusal compares — an empty one on every bank would make two
// banks on one address look like one admission, which is exactly the case
// payment.ErrBICAlreadyAdmitted exists for. A deployment that lists two banks on
// one address gets the refusal, which is the truth about that deployment.
func Ref(bic iso20022.BIC) string { return "admitted-" + string(bic) }
