// Package provision writes the rows that put a bank in the scheme.
package provision

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// BankSpec is everything a deployment decides about one bank.
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
func Bank(ctx context.Context, nets *payment.Networks, spec BankSpec) (*payment.Bank, error) {
	// The APPLICANT's own network, over the applicant's own database, which
	// asking for it is what creates. A bank's id is its BIC, so the network is
	// minted from the address the spec already carries. See payment.Stores.
	applicant, err := nets.Bank(ctx, payment.ParticipantID(spec.BIC))
	if err != nil {
		return nil, err
	}
	if err := refuseAnotherBankOnThisAddress(ctx, applicant, spec); err != nil {
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

// Subscribe delivers the clearing house's roster to every member in it, so each
// can route to the others. IN PROCESS, for a caller with no host to publish on:
// a deployment has one, and there a member collects the file for itself.
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

// ErrAddressTaken is a spec whose BIC already belongs to a different bank.
var ErrAddressTaken = errors.New("provision: another bank already holds this address")

// refuseAnotherBankOnThisAddress is ErrAddressTaken's read: the bank already at
// this address, out of its own book, or nothing.
func refuseAnotherBankOnThisAddress(ctx context.Context, applicant *payment.BankNetwork, spec BankSpec) error {
	held, err := applicant.GetBank(ctx, payment.ParticipantID(spec.BIC))
	if errors.Is(err, payment.ErrParticipantNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if held.Name != spec.Name {
		return fmt.Errorf("%w: %s is %s, and this deployment lists %s on it",
			ErrAddressTaken, spec.BIC, held.Name, spec.Name)
	}
	return nil
}

// Ref is the admission reference a provisioned bank is recorded under.
func Ref(bic iso20022.BIC) string { return "admitted-" + string(bic) }
