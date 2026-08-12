package storetest

import (
	"context"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/provision"
)

// Admit provisions one fixture bank and then has every member pull the routing
// directory, so that a suite about something else gets banks which can address
// each other.
func Admit(ctx context.Context, nets *payment.Networks, name string, bic iso20022.BIC,
	assets []ledger.AssetCode) (*payment.Bank, error) {

	bank, err := provision.Bank(ctx, nets, provision.BankSpec{
		Name: name, BIC: bic, Country: FixtureCountry, Assets: assets,
	})
	if err != nil {
		return nil, err
	}
	if err := provision.Subscribe(ctx, nets); err != nil {
		return nil, err
	}
	return bank, nil
}

// FixtureCountry is the register every fixture bank applies to. See Admit.
const FixtureCountry = iban.DE
