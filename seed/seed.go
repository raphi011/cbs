package seed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/provision"
)

// BaseDate anchors the deterministic timeline, and is therefore where a
// deployment holding no business date of its own begins: the base state is
// dated on it, and the days an operator advances afterwards run on from it.
var BaseDate = time.Date(2025, 9, 15, 9, 0, 0, 0, time.UTC)

// Dataset is the base state together with the deployment clock it is built on.
type Dataset struct{ clock *calendar.Clock }

// New returns a Dataset built on clock.
func New(clock *calendar.Clock) *Dataset { return &Dataset{clock: clock} }

// Populate builds the base state (see the package doc) into the network's
// store, provisioning its banks and giving each one its place in the deployment
// it is given.
func (d *Dataset) Populate(ctx context.Context, nets *payment.Networks, dep Deployment) (err error) {
	if dep == nil {
		return errors.New("seed: no deployment, so no bank in this base state could be admitted to the scheme")
	}

	// "Has anything been built here already", asked of the DEPLOYMENT rather than
	// of an institution.
	existing, err := nets.Stores().Banks(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}

	defer func() {
		if e := recoverBuild(recover()); e != nil {
			err = e
		}
	}()

	if err := d.clock.Rewind(BaseDate); err != nil {
		return err
	}
	b := &builder{ctx: ctx, nets: nets, dep: dep, clock: d.clock}
	b.build()
	return nil
}

// seedErr marks a panic raised by must or check, so it can be told apart from
// every other panic that might unwind through the builder.
type seedErr struct{ err error }

func (e seedErr) Error() string { return e.err.Error() }
func (e seedErr) Unwrap() error { return e.err }

// recoverBuild converts the builder's own must/check panic into an error and
// re-panics anything else.
func recoverBuild(r any) error {
	if r == nil {
		return nil
	}
	se, ok := r.(seedErr)
	if !ok {
		panic(r)
	}
	return fmt.Errorf("seed: %w", se.err)
}

type builder struct {
	ctx context.Context
	// nets mints one payment.Network per institution, and the builder below names
	// which institution performs each act.
	nets *payment.Networks
	// dep is the running system: the acts the builder cannot perform for itself,
	// because each needs more than one institution. See Deployment.
	dep   Deployment
	clock *calendar.Clock
}

// bank, csm and cb are the three institutions' views, one call each.
func (b *builder) bank(bic iso20022.BIC) *payment.BankNetwork {
	return must(b.nets.Bank(b.ctx, payment.ParticipantID(bic)))
}
func (b *builder) cb() *payment.CentralBankNetwork { return b.nets.CentralBank() }

// must returns v, panicking on a non-nil error. The base state is hardcoded and
// deterministic, so any error is a programming bug that should fail loudly.
func must[T any](v T, err error) T {
	if err != nil {
		panic(seedErr{err})
	}
	return v
}

// must2 is must for a call returning two values and an error.
func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(seedErr{err})
	}
	return a, b
}

// check panics on a non-nil error from a call that returns only an error.
func check(err error) {
	if err != nil {
		panic(seedErr{err})
	}
}

// seedAsset is the asset the whole base state is denominated in.
const seedAsset ledger.AssetCode = "EUR"

// Capital is what each bank's owners subscribe, and Lodged is how much of it the
// bank places on reserve. The rest stays in the vault, because reserves and cash
// are not the same money and a base state showing both says so.
const (
	Capital ledger.Amount = 10_000_000
	Lodged  ledger.Amount = 8_000_000
)

// provisionBank writes one bank's three rows — its own, its settlement account
// in the central bank's book, its roster entry in the clearing house's — pays
// its capital up, and gives it its place in the network.
func (b *builder) provisionBank(name string, bic iso20022.BIC, country iban.Country) *payment.Bank {
	bank := must(provision.Bank(b.ctx, b.nets, provision.BankSpec{
		Name: name, BIC: bic, Country: country,
		Assets:  []ledger.AssetCode{seedAsset},
		Capital: Capital,
	}))
	check(b.dep.AddBank(b.ctx, bank))
	return bank
}

// products prices a bank's catalogue: the Basic Current Account its founding
// created, and a Premium one an account can be migrated to.
func (b *builder) products(p *payment.Bank) {
	from := ledger.DayStart(b.clock.Now())

	b.publish(p, p.ProductID, from.AddDate(0, 0, 1), product.OverdraftPricing{
		Rate: 120_000, UnarrangedRate: 350_000, DayCount: interest.ACT365,
	})
	b.publish(p, p.ProductID, from.AddDate(0, 0, 30), product.OverdraftPricing{
		Rate: 149_000, UnarrangedRate: 350_000, DayCount: interest.ACT365,
	})

	premium := must(p.Catalogue.CreateProduct(b.ctx, PremiumProduct, product.CurrentAccount))
	b.publish(p, premium.ID, from, product.OverdraftPricing{
		Rate: 70_000, UnarrangedRate: 250_000, DayCount: interest.ACT365,
	})
}

// PremiumProduct is the catalogue entry every bank is priced with beside the
// Basic one founding gives it. A scenario migrating an account names it.
const PremiumProduct = "Premium Current Account"

// publish drafts and publishes in one step, which is what every version here
// wants: the draft state is a thing an operator passes through, not a thing the
// base state should sit in.
func (b *builder) publish(p *payment.Bank, id product.ID, from time.Time, pricing product.OverdraftPricing) {
	must(p.Catalogue.DraftVersion(b.ctx, id, from, pricing))
	must(p.Catalogue.PublishVersion(b.ctx, id, from))
}

// lodge moves part of one bank's vault cash onto its reserve at the central
// bank: the member's own swap, and the settlement agent's credit that matches
// it.
func (b *builder) lodge(p *payment.Bank, amount ledger.Amount) {
	in, _ := must2(b.bank(p.BIC).LodgeReserves(b.ctx, seedAsset, amount, payment.MessageContext{
		From:  p.BIC,
		To:    b.dep.CentralBankBIC(),
		MsgID: fmt.Sprintf("seed-lodge-%s-%s", p.BIC, seedAsset),
		Now:   b.clock.Now(),
	}))
	must(b.cb().ReceiveLodgement(b.ctx, in))
}

func (b *builder) build() {
	// --- Banks -------------------------------------------------------------
	// Joining is three rows in three databases, and then the owners pay the
	// bank's capital up — which is where a bank holding no deposits gets money.
	// See provisionBank.
	//
	// Each issues addresses in the country its BIC names, under a bank code of
	// its country's own width: eight digits in Germany, five in Italy and
	// France, three in Sweden.
	banks := []*payment.Bank{
		b.provisionBank("Aurora Bank", "AURODEFFXXX", iban.DE),
		b.provisionBank("Banca Verde", "VERDITMMXXX", iban.IT),
		b.provisionBank("Nordhaven Bank", "NORDSESSXXX", iban.SE),
		b.provisionBank("Crédit Soleil", "SOLEFRPPXXX", iban.FR),
	}

	// --- Each bank collects the routing directory --------------------------
	check(b.dep.Subscribe(b.ctx))

	for _, p := range banks {
		// --- Each bank's catalogue ------------------------------------------
		// Before any account, because every deposit account is opened FROM a
		// product: a floating terms row with no product would have nothing to
		// float to.
		b.products(p)

		// --- And most of its cash placed on reserve --------------------------
		// A bank cannot settle out of vault cash, so a base state a payment can be
		// submitted into is one where this has already happened.
		b.lodge(p, Lodged)
	}

	// --- The clearing house's open window, one per scheme -------------------
	// A day opens tomorrow's after it has closed today's, so the FIRST one is
	// nobody's day: without it the first file uploaded reaches a clearing house
	// with nothing to take it into.
	csm := b.nets.ClearingHouse()
	for _, scheme := range csm.ListSchemes() {
		must(csm.OpenCycle(b.ctx, scheme.ID()))
	}
}
