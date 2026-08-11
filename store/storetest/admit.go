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
//
// The composition is provision.Bank followed by provision.Subscribe, and the
// second is a FIFTH act that no provisioning performs. It is named here rather
// than hidden because of what it produces: a scheme where everybody pulled a
// moment ago, which is not a state the system maintains for anybody. A suite
// that wants to MEASURE staleness therefore cannot use this helper, and none
// does — the case lives in mesh, where a bank is provisioned and the others are
// left holding the copies they had.
//
// # Why the fixture fixes the country
//
// Every fixture bank applies in ONE country, and deliberately not its BIC's: the
// suites use addresses like BANKESMMXXX, in countries this system keeps no IBAN
// structure for. One country keeps the fixtures uniform and says nothing untrue.
//
// # Why it lives here
//
// Several suites need a bank in the scheme, and none of them is testing how one
// gets there. It is in this package rather than store/testenv because RunRaces
// needs it and this package must not import an implementation, since the
// implementation's tests import this package.
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
