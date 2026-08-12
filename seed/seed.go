package seed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/provision"
)

// BaseDate anchors the deterministic seed timeline, and is therefore where a
// deployment holding no business date of its own begins: everything this
// scenario builds is dated relative to it, and the days an operator advances
// afterwards run on from where it left off.
//
// It is a Monday, which is what makes the first advance a settlement day rather
// than a weekend.
var BaseDate = time.Date(2025, 9, 15, 9, 0, 0, 0, time.UTC)

// Dataset is the sample scenario together with the deployment clock it is built
// on.
//
// The clock is the DEPLOYMENT's and is passed in. Seeding drives it — rewound to
// BaseDate and advanced a day at a time while the scenario is built — and then
// leaves it wherever the timeline ended, which is where the deployment goes on
// from. Nothing switches it to wall time: a business date is advanced by an
// operator, and a dataset dated a year before the wall clock beside rows dated
// today is two timelines on one screen.
//
// Rebuilding after a store reset rewinds it, so a reset restores the dataset
// rather than a version of it shifted forward in time.
type Dataset struct{ clock *calendar.Clock }

// New returns a Dataset built on clock.
func New(clock *calendar.Clock) *Dataset { return &Dataset{clock: clock} }

// Populate builds the full sample scenario (see the package doc) into the
// network's store, provisioning its banks and giving each one its place in the
// deployment it is given.
//
// # Why it takes a deployment
//
// Writing a bank's three rows needs no deployment at all — see provision.Bank —
// but a bank with rows and no place in the network can neither send nor be sent
// to, and this scenario is payments: an opening deposit is lodged, a cycle is
// settled, a collection is answered. Every one of those is a conversation
// between two institutions.
//
// So the deployment must be RUNNING before this is called: it is built over an
// unseeded store, and what it reads there finds nothing because the banks it
// would find are the ones this is about to write.
//
// It is not optional and there is no nil path. A Populate with no deployment
// could write four banks' rows and would leave every one of them unreachable,
// which is a scenario in which no payment in it can be made — see
// builder.provision.
//
// It is idempotent: a store that already holds participants is left alone. That
// is what makes it safe to call on every boot — against a database that
// outlives the process, seeding unconditionally would stack a second copy of
// the scenario on top of the first at every restart.
//
// # Why the probe is "any participants", and not "the whole scenario"
//
// The probe asks whether the store has content of its own, not whether this
// exact dataset is complete, and the distinction matters because the two have
// different false positives. A completeness check — "are there settlements? are
// there mandates?" — would refuse to boot against a database file in which the
// user had created a couple of banks through the API and nothing else,
// which is an ordinary way to use this system and not a fault at all. There is
// no marker that separates "seeded and then edited" from "never seeded" without
// tagging rows, and a tag is a schema change in aid of a situation that should
// not arise.
//
// It should not arise: POST /admin/reset runs on a context the client cannot
// cancel, so it cannot truncate and then lose its context before it reseeds (see
// api.Server.handleReset). What remains is a process killed mid-seed, which
// leaves a partial dataset this probe will skip; the answer to that is to reset.
//
// The clock is REWOUND only on the path that builds, which is what makes the
// skip above safe against a store that outlives the process. A second process
// opening one finds the banks already there, builds nothing, and leaves the
// business date exactly where it opened it — which is the date the last process
// left, read back from beside the databases (calendar.OpenClock). Rewinding
// unconditionally would put a running deployment back at BaseDate on every boot,
// behind books that already hold entries dated months later.
//
// The scenario is hardcoded, so a failure while building it is a programming
// bug rather than a runtime condition, and the builder says so by panicking.
// Populate turns the builder's own panic back into an error at the package
// boundary, since its caller — the server's reset handler — has an error to
// report and a request to answer. Any other panic is re-raised with its stack
// intact; see recoverBuild.
func (d *Dataset) Populate(ctx context.Context, nets *payment.Networks, dep Deployment) (err error) {
	if dep == nil {
		return errors.New("seed: no deployment, so no bank in this scenario could be admitted to the scheme")
	}

	// "Has anything been built here already", asked of the DEPLOYMENT rather than
	// of an institution. It was the clearing house's ListBanks while there was one
	// store; that institution's schema has no banks table, and no institution has
	// the whole answer anyway — a bank founded and never admitted is in no roster
	// and in no settlement register, and it is still a bank this scenario built.
	//
	// payment.Stores.Banks is the set of DATABASES, which is the same question a
	// restart's listener plan asks. Founding the first bank is what creates the
	// first database.
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
	b := &builder{
		ctx: ctx, nets: nets, dep: dep, clock: d.clock,
		cats: map[payment.ParticipantID]catalogue{},
	}
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
//
// The distinction matters because the builder drives four packages: a nil map
// write or a nil dereference in payment, deposit, ledger or the store is a bug,
// and flattening it into "seed: runtime error: invalid memory address" —
// returned as a 500 from POST /admin/reset with the stack discarded — would
// hide exactly the failures worth seeing. Only the errors the builder chose to
// raise are turned into errors.
//
// It takes the recovered value rather than calling recover itself, because
// recover only works when called directly by a deferred function.
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

// There is no Network() convenience constructor, and the reason is worth
// recording rather than rediscovering.
//
// Building the scenario needs a DEPLOYMENT as well as a network — some acts need
// a table no single institution owns, and two need a file — and a deployment is a
// composition root, which a package it imports cannot name. So callers build the
// pieces themselves: cmd/server does, and so does this package's own test
// helper.

type builder struct {
	ctx context.Context
	// nets mints one payment.Network per institution, and the builder below
	// names which institution performs each act.
	//
	// One *payment.Network per institution: a member's mirror leg needs that
	// member's own network and a cut-off needs the central bank's. The composites
	// here — initiate, reject, returnPayment, settle — run several institutions'
	// halves back to back, one unit of work each, and each half says whose it is.
	nets *payment.Networks
	// dep is the running system: the acts the builder cannot perform for itself,
	// because each needs more than one institution. See Deployment.
	dep   Deployment
	clock *calendar.Clock
	// cats holds each bank's product IDs, keyed by participant: the
	// catalogue is book-scoped, so every bank has its own Basic and Premium and
	// the same name at two banks is two products.
	cats map[payment.ParticipantID]catalogue
}

// catalogue is the two product IDs the rest of the seed needs.
type catalogue struct{ basic, premium product.ID }

// bank, csm and cb are the three institutions' views, one call each.
//
// They are spelled out rather than cached because payment.Networks mints on
// demand and the seed builds one scenario once; a field per bank would be a
// second index over the banks this builder is in the middle of creating.
// bank is one member bank's own view, over that bank's own database, keyed by
// its ADDRESS.
//
// It took a payment.ParticipantID, which is the same value under the other type
// (see payment.AsBank), and it takes the BIC because that is what every caller
// here now holds: the seed reads a submitter, a receiver, a returner and a
// statement's member off agents and off the central bank's own records, all of
// which are BICs. Converting once here beats converting at eleven call sites.
//
// It panics on a failure to open the bank's store, which is must's rule and not
// a new one: every bank this builder names is one it founded itself, moments
// ago, in this process, so a database that will not open is a programming bug
// rather than a runtime condition.
func (b *builder) bank(bic iso20022.BIC) *payment.BankNetwork {
	return must(b.nets.Bank(b.ctx, payment.ParticipantID(bic)))
}
func (b *builder) csm() *payment.ClearingHouseNetwork { return b.nets.ClearingHouse() }
func (b *builder) cb() *payment.CentralBankNetwork    { return b.nets.CentralBank() }

// must returns v, panicking on a non-nil error. Seed data is hardcoded and
// deterministic, so any error is a programming bug that should fail loudly.
//
// The panic value is a seedErr rather than a string so that Populate can tell
// it apart from a genuine runtime panic and leave that one alone.
func must[T any](v T, err error) T {
	if err != nil {
		panic(seedErr{err})
	}
	return v
}

// must2 is must for a call returning two values and an error. SettleCycle is the
// only one, and a second generic beats spelling its error handling out.
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

// provision writes one bank's three rows — its own, its settlement account in
// the central bank's book, its roster entry in the clearing house's — and gives
// it its place in the network.
//
// The two steps are different kinds of thing and are spelled separately: the
// rows are what the three institutions hold, and the actor is an inbox on a bus.
// Neither is a message, so nothing here waits for an answer, and the ids the
// dataset comes out with are the ids of four calls made in order on one
// goroutine. TestDeterministicIDs is the assertion that two builds agree.
//
// The bank that comes back holds its settlement account, which the next thing
// this scenario does — pay an opening deposit in — needs.
func (b *builder) provision(name string, bic iso20022.BIC, country iban.Country, assets []ledger.AssetCode) *payment.Bank {
	bank := must(provision.Bank(b.ctx, b.nets, provision.BankSpec{
		Name: name, BIC: bic, Country: country, Assets: assets,
	}))
	check(b.dep.AddBank(b.ctx, bank))
	return bank
}

// subscribe is one bank pulling the scheme's routing directory, through the same
// door an operator's POST /directory/banks/refresh goes through.
//
// It is no part of provisioning a bank and is not a message: nothing is queued
// and nobody answers later. What it does is read the roster as it stands and
// replace this bank's copy with it. See Deployment.RefreshDirectory.
func (b *builder) subscribe(p *payment.Bank) {
	must(b.dep.RefreshDirectory(b.ctx, p.BIC))
}

// seedAsset is the asset the whole sample scenario is denominated in.
//
// The scenario is a SEPA one, and SEPA is a euro scheme, so every account it
// opens is a euro account. It is spelled out at each site rather than left to
// a default: the demo data is euro because the banks in it are euro banks, not
// because nobody said.
const seedAsset ledger.AssetCode = "EUR"

// products prices a bank's catalogue: the Basic Current Account its onboarding
// created, and a Premium one an account can be migrated to.
//
// Basic is the product founding already made — every bank gets one, because a
// bank with no product cannot open an account — and its first
// published version is interest-free, which is what a bank that has not yet
// decided a price has. This adds the two versions that give it one:
//
//   - 12% from day 1, the day after the bank opened.
//   - 14.9% from day 30, published now and effective then. That is the case no
//     per-account write can produce and the reason the catalogue exists: one
//     row moves the price for every account bound to it, and the accounts' own
//     rows are untouched.
//
// Both are forward-dated, which is the only direction PublishVersion allows.
func (b *builder) products(p *payment.Bank) {
	from := ledger.DayStart(b.clock.Now())

	b.publish(p, p.ProductID, from.AddDate(0, 0, 1), product.OverdraftPricing{
		Rate: 120_000, UnarrangedRate: 350_000, DayCount: interest.ACT365,
	})
	b.publish(p, p.ProductID, from.AddDate(0, 0, 30), product.OverdraftPricing{
		Rate: 149_000, UnarrangedRate: 350_000, DayCount: interest.ACT365,
	})

	premium := must(p.Catalogue.CreateProduct(b.ctx, "Premium Current Account", product.CurrentAccount))
	b.publish(p, premium.ID, from, product.OverdraftPricing{
		Rate: 70_000, UnarrangedRate: 250_000, DayCount: interest.ACT365,
	})

	b.cats[p.ID] = catalogue{basic: p.ProductID, premium: premium.ID}
}

// publish drafts and publishes in one step, which is what every seeded version
// wants: the draft state is a thing an operator passes through, not a thing the
// demo data should sit in.
func (b *builder) publish(p *payment.Bank, id product.ID, from time.Time, pricing product.OverdraftPricing) {
	must(p.Catalogue.DraftVersion(b.ctx, id, from, pricing))
	must(p.Catalogue.PublishVersion(b.ctx, id, from))
}

// open opens a customer account on the bank's Basic product and returns it.
//
// It goes through the register rather than p.OpenCustomerAccount because that
// helper opens from the participant's configured default, and this seed has
// retired that one in favour of a priced catalogue of its own.
//
// THE ADDRESS IS NOT AN ARGUMENT. It was, and every account in this file quoted
// a literal beside its holder's name. A bank issues its customers' addresses out
// of its own bank code, so the register mints one and the seed reads it back —
// which is also why the accounts below are numbered by the order they are opened
// in and not by a digit chosen to say which bank they are at.
func (b *builder) open(p *payment.Bank, name string) deposit.Account {
	return b.openOverdraft(p, name, 0)
}

// openOverdraft opens a customer account with an overdraft limit. The limit is
// per account and the PRICE is not: it comes from the Basic product, so the
// day-30 reprice above reaches every account opened here without touching one of
// them.
//
// The account comes back holding its own minted IBAN, which is what makes it
// resolvable through Register.ResolveIdentifier and what b.ref reads.
func (b *builder) openOverdraft(p *payment.Bank, name string, limit ledger.Amount) deposit.Account {
	return must(p.Deposit.OpenAccount(b.ctx, name, seedAsset, b.cats[p.ID].basic, limit))
}

// openLoan opens a term loan and disburses it in full into the borrower's own
// account, so the caller is left with a facility that has begun accruing.
func (b *builder) openLoan(p *payment.Bank, borrower deposit.Account, name string, principal ledger.Amount, rate interest.Rate, termMonths int, firstDue time.Time, description string) lending.Facility {
	loan := must(p.Lending.OpenTermLoan(b.ctx, name, seedAsset, principal, rate, interest.ACT365, lending.Annuity, termMonths))
	borrowerPos := must(p.Deposit.Position(b.ctx, borrower.ID))
	must(p.Lending.Disburse(b.ctx, loan.ID, borrowerPos, firstDue, description))
	return must(p.Lending.GetFacility(b.ctx, loan.ID))
}

// openLine opens a revolving line and draws it once into the borrower's own
// account, so the caller is left with a facility carrying a balance.
func (b *builder) openLine(p *payment.Bank, borrower deposit.Account, name string, limit ledger.Amount, rate interest.Rate, minPayment interest.Fraction, draw ledger.Amount, description string) lending.Facility {
	line := must(p.Lending.OpenRevolvingLine(b.ctx, name, seedAsset, limit, rate, interest.ACT365, minPayment))
	borrowerPos := must(p.Deposit.Position(b.ctx, borrower.ID))
	must(p.Lending.Draw(b.ctx, line.ID, borrowerPos, draw, description))
	return must(p.Lending.GetFacility(b.ctx, line.ID))
}

// day moves the business date on by one, and it is the only step this builder
// takes.
//
// A deployment's clock keeps the time of day (calendar.Clock.Advance), so
// everything the scenario writes on one date carries the SAME instant and its
// order is the order it was written in. That is the running system's own
// property, and a fixture that stepped an hour between two holds would be
// building a timeline no operator can produce.
func (b *builder) day() { must(b.clock.Advance()) }

// runDays advances the clock a day at a time, driving RunEndOfDay through each
// one — the same entry point payment.Bank exposes to the API — so the
// seed's accrual and arrears move exactly as a running day would produce them.
func (b *builder) runDays(p *payment.Bank, days int) {
	for i := 0; i < days; i++ {
		b.day()
		check(p.RunEndOfDay(b.ctx, b.clock.Now()))
	}
}

// ref builds a PartyRef for a customer deposit account from the account's own
// IBAN identifier, so the same account always produces an identical PartyRef.
//
// It took the account's BANK as well, to fill the ref's participant. A PartyRef
// names no bank now (see payment.PartyRef), and which bank an account is at
// travels beside the ref as an agent BIC — on the request's PartyDetails for a
// payment, and as CreateMandate's own argument for a mandate. The call sites say
// it once rather than twice.
func (b *builder) ref(acct deposit.Account) payment.PartyRef {
	ref := payment.PartyRef{Account: acct.ID}
	for _, ident := range acct.Identifiers {
		if ident.Scheme == deposit.IdentifierIBAN {
			ref.Identifier = ident
			break
		}
	}
	return ref
}

// fund credits a deposit account with cash, which the bank then holds as vault
// cash.
//
// It does not raise the bank's reserve. Cash paid in is one institution's act in
// one book; moving it onto reserve is a LODGEMENT, which is a conversation with
// the central bank and cannot happen inside a deposit. See lodge, which every
// funded bank has to run before this scenario can settle anything.
func (b *builder) fund(p *payment.Bank, acct deposit.Account, amount ledger.Amount) {
	check(b.bank(p.BIC).Deposit(b.ctx, p.ID, acct.ID, amount, "Opening deposit"))
}

// lodge moves one bank's vault cash onto its reserve at the central bank: the
// member's own swap, and the settlement agent's credit that matches it.
//
// # Why the seed has to do this at all
//
// Nothing else in this scenario funds a reserve, so without this every bank
// would reach the first cut-off with a zero reserve and settlement would fail
// for all of them.
//
// # Both halves, composed, exactly as initiate composes three
//
// A member cannot credit its own reserve account at the central bank; it asks,
// in a camt.050, and the answer arrives on a later download. Going through the
// deployment's door would therefore leave the file waiting in the settlement
// agent's queue until a business day ran through it — and this scenario settles
// a cycle several steps further down, against a reserve the agent would not yet
// have credited.
//
// So the seed plays both institutions, which is what it does for an initiation,
// a rejection, a return and a cut-off. What it gives up is that no camt.050 and
// no camt.025 exist in the built scenario; what it keeps is a fixed dataset that
// does not depend on when a day is advanced.
//
// The instruction the member's half renders is what the agent's half is handed,
// rather than one the seed assembles: the two must agree about the account
// number and the reference, and a second rendering is a second thing that can
// drift. See payment.LodgeReservesTx and payment.ReceiveLodgementTx.
func (b *builder) lodge(p *payment.Bank, amount ledger.Amount) {
	in, _ := must2(b.bank(p.BIC).LodgeReserves(b.ctx, seedAsset, amount, payment.MessageContext{
		From:  p.BIC,
		To:    b.dep.CentralBankBIC(),
		MsgID: fmt.Sprintf("seed-lodge-%s-%s", p.BIC, seedAsset),
		Now:   b.clock.Now(),
	}))
	must(b.cb().ReceiveLodgement(b.ctx, in))
}

// initiate runs all three halves of an initiation — the submitting bank's, the
// receiving bank's and the clearing house's — leaving the payment Accepted in
// its scheme's open cycle.
//
// The seed is one process building a scenario, so it plays every institution;
// the transport is what makes them separate. Composing the three halves here
// rather than calling one method that does all three is the point of the split:
// there is no such method, precisely so that no caller can validate both ends of
// a payment by accident.
//
// It survives for a reason no HTTP handler has: its whole job is to leave a
// FIXED scenario, and a conversation carried out over the transport would not
// finish until a business day ran — so the dataset would depend on when somebody
// advanced the clock.
//
// # It is THREE units of work now, and it has to be
//
// The three halves shared one Tx until the store split, and the doc here said
// what that bought: the whole initiation or none of it, which is exactly what
// three institutions with a database each do not have. There is no Tx to share. A unit of
// work is one database's, each institution has its own, and a statement that
// spanned two of them is the thing this task removed — so what the seed gives up
// is not a convenience but an impossibility.
//
// What that costs is the fixture's atomicity: a failure in the second half now
// leaves the first half's writes committed in the submitting bank's database.
// Nothing recovers from it and nothing tries to. The seed builds a hardcoded
// scenario and panics on any error, so a half-built one is a programming bug
// that fails startup — which is what it was before, one level of tidiness down.
// submitterOf names the bank that instructs, which the scheme's DIRECTION
// decides and nothing else: the payer's bank pushes and the payee's bank pulls.
//
// It is asked here twice — once to run that bank's half, once to tell it a
// rejection — and cmd/server's submitterOf is the same rule at the door. An
// unknown scheme falls back to the payer's bank rather than refusing, because
// every scheme this builder names is one it opened a cycle for moments ago; a
// missing one is a programming bug the SubmitPayment below reports with the
// error the caller can act on.
func (b *builder) submitterOf(scheme payment.SchemeID, debtorAgent, creditorAgent iso20022.BIC) iso20022.BIC {
	if s, ok := b.csm().Scheme(scheme); ok && s.Direction() == payment.Pull {
		return creditorAgent
	}
	return debtorAgent
}

func (b *builder) initiate(req payment.InitiatePaymentRequest) payment.Payment {
	submitter := b.submitterOf(req.Scheme, req.DebtorDetails.Agent, req.CreditorDetails.Agent)
	p := must(b.bank(submitter).SubmitPayment(b.ctx, req))

	// What the OTHER two institutions would have been sent, and it is the whole of
	// what they get. Each writes its own row from it, so the seed hands over an
	// instruction rather than an id — which is what a pacs.008 or a pacs.003 is,
	// minus the XML.
	//
	// The two DETAILS come off the payment rather than off the request: the
	// submitting bank overwrote its own side from its own register, and the
	// message it would have built carries what it wrote. Everything else is the
	// instruction unchanged, including both account ids — which is the seed being
	// the seed. A real receiving bank resolves its own side from the address and
	// has no way to know the other's, and cmd/server is where that is modelled;
	// this process holds every register and is building a fixed scenario.
	relayed := req
	relayed.DebtorDetails, relayed.CreditorDetails = p.DebtorDetails, p.CreditorDetails

	// The RECEIVING bank answers, and it is the other one. Naming it is what picks
	// the network AcceptInbound resolves the address in — its own register and no
	// other — and the seed is playing that bank as well as the submitting one.
	receiver := p.CreditorDetails.Agent
	if receiver == submitter {
		receiver = p.DebtorDetails.Agent
	}
	check(b.bank(receiver).AcceptInbound(b.ctx, p.ID, relayed))
	// And the CLEARING HOUSE, which has to be carrying the payment before it can
	// take it into a cycle. In the running system this is the moment it routes the
	// instruction on; here there is nothing to route, so the record stands alone.
	must(b.csm().RecordRelayed(b.ctx, []payment.InboundTransaction{{ID: p.ID, Request: relayed}}))
	accepted := must(b.csm().AcceptAtCSM(b.ctx, p.ID))

	// And the SUBMITTING bank is told, which is the act this composite was
	// missing. In the running system the clearing house answers the instruction
	// with an ACCP addressed to the bank that sent it, and that bank records
	// Accepted on its own copy (ClearingHouse.tell, payment.AcceptAtBankTx).
	// Without it a seeded bank would sit at Initiated where a carried one reaches
	// Accepted — the same payment with two different histories depending on which
	// built it, which is exactly the divergence a sample dataset must not have.
	//
	// Only the submitter. The other bank ANSWERED the instruction and is told
	// nothing further until the cut-off; its copy goes Initiated -> Settled and
	// is not wrong. Every assertion about a status has to say whose copy.
	must(b.bank(submitter).AcceptAtBank(b.ctx, p.ID))
	return accepted
}

// reject runs both halves of a rejection — the clearing house's transition and
// the submitting bank's reversal of its own leg — leaving the payment Rejected
// with the payer's money back in their account.
//
// Split for the same reason initiate is: there is no method that plays both
// actors, and the two cannot share a Tx — two institutions, two databases. So
// the fixture has the running system's shape here as well, and what it promises
// is the OUTCOME rather than the process.
//
// # ONE bank is told, and which one is the whole of settle-before-release
//
// The submitter, because before finality it is the only bank that has ever heard
// of the payment: the clearing house holds the receiving bank's share and
// releases it when the cycle settles, so a payment rejected out of an open cycle
// is one that bank never learns existed. Telling it would be addressing a
// rejection to an institution holding no copy to reject.
//
// On a push that bank is the payer's and has money to give back — the debtor leg
// posted at submission, reversed here in the one unit of work that also records
// the refusal (payment.RejectAtBankTx). On a pull it posted nothing and is being
// told the answer to its own instruction, with nothing to undo.
//
// ClearingHouse.tell decides the same thing in the running system, and decides it
// the same way.
func (b *builder) reject(id payment.PaymentID, code iso20022.StatusReason, reason string) {
	rejected := must(b.csm().RejectAtCSM(b.ctx, id, code, reason))
	submitter := b.submitterOf(rejected.Scheme, rejected.DebtorDetails.Agent, rejected.CreditorDetails.Agent)
	must(b.bank(submitter).RejectAtBank(b.ctx, id, code, reason))
}

// returnPayment runs all three institutions' halves of an R-transaction, leaving
// the payment Returned, both customers put back where they were and both banks'
// clearing suspense at zero:
//
//   - the RETURNING bank's own customer leg, posted before it would have sent
//     the pacs.004 — the clawback if that bank is the creditor's, the refund if
//     it is the payer's. Which bank that is is the scheme's direction and
//     nothing else (payment.ReturnerOf), and it is the only one of the four that
//     could still have said no;
//   - the settlement agent reversing the reserves between the two settlement
//     accounts, and stating both;
//   - each member booking its reserve mirror from the statement it would have
//     been sent, exactly as at a cut-off;
//   - the OTHER bank's customer leg, which is what takes the payment to
//     Returned.
//
// It is settle's argument applied to the return: no institution may post in
// another's book, so there is no one call that does all four halves. The seed
// can, because the seed is not an institution. See settle for the whole of that
// argument, and for what the fixture gives up by running the four halves
// together.
//
// The instruction handed to SettleReturn is built from the CLEARING HOUSE's
// payment row here, which is the one thing the running system does differently
// rather than merely faster: there both agents come off the pacs.004's OrgnlTxRef,
// because a settlement agent holds no payment rows. The values are identical —
// payment.ReturnMessage writes these same two fields and payment.ReadReturn
// reads them back — and the way they were obtained is the whole difference.
//
// The four halves are four units of work, one per database. See initiate.
func (b *builder) returnPayment(id payment.PaymentID, reason string) {
	p := must(b.csm().GetPayment(b.ctx, id))
	scheme, ok := b.csm().Scheme(p.Scheme)
	if !ok {
		check(fmt.Errorf("seed: no scheme %q to return %s under: %w", p.Scheme, id, payment.ErrSchemeNotFound))
	}
	returner := payment.ReturnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
	other := p.DebtorDetails.Agent
	if other == returner {
		other = p.CreditorDetails.Agent
	}
	must(b.bank(returner).PostReturnLeg(b.ctx, id, reason))
	statements := must(b.cb().SettleReturn(b.ctx, payment.ReturnInstruction{
		PaymentID:     p.ID,
		EndToEndID:    p.EndToEndID,
		DebtorAgent:   p.DebtorDetails.Agent,
		CreditorAgent: p.CreditorDetails.Agent,
		Amount:        p.Amount,
		Asset:         scheme.Asset(),
		Reason:        reason,
	}))
	b.advise(statements)
	// The OTHER bank's leg, which is the SECOND one and therefore the one that
	// takes that bank's copy to Returned. Each bank writes its own field on its own
	// copy, so which is second is POSITION IN THE CONVERSATION rather than
	// something readable off a row; see payment.PostReturnLegTx.
	must(b.bank(other).PostReturnLeg(b.ctx, id, reason))
	// So the returner's copy and the clearing house's are moved by what they are
	// TOLD, which in the running system is the settlement agent's ACSC relayed on. The seed
	// has no message and says the same thing directly.
	must(b.bank(returner).CompleteReturn(b.ctx, id))
	must(b.csm().CompleteReturn(b.ctx, id))
}

// advise books each member's mirror leg from the statement the settlement agent
// produced, in that member's own book and in a unit of work of its own.
//
// It is shared by returnPayment and settle because the two produce the same
// statements and do the same thing with them.
func (b *builder) advise(statements []payment.SettlementStatement) {
	for _, st := range statements {
		must(b.bank(st.Agent).PostSettlementAdvice(b.ctx, payment.AdvisedMovement{
			Account:        st.Account,
			Asset:          st.Asset,
			Movement:       st.Movement,
			ClosingBalance: st.ClosingBalance,
			Reference:      st.Reference,
			ValueDate:      st.ValueDate,
		}))
	}
}

// settle runs all three institutions' halves of a cut-off, leaving the cycle
// Settled, every payment Settled and every bank's clearing suspense back at
// zero:
//
//   - the settlement agent's netting transaction, in the central bank's book;
//   - each member's booking of the camt.053 it would have been sent, which is
//     the mirror leg in that member's own book;
//   - each payee's bank releasing its own customer's money out of its own
//     suspense, which is what that bank does when the clearing house advises it
//     that a payment settled.
//
// It is initiate's argument applied to settlement. There is no method that plays
// all three, deliberately: no institution can post in another's book. The seed
// can, because the seed is not an institution — it is one process building a
// fixed scenario before any actor exists, and a conversation carried out at
// startup could not promise a fixed outcome.
//
// What the seed gives up by doing so is exactly what the transport exists to
// model: there these halves are several units of work with an unreconciled
// interval between them. They are several here too — one database each, and no
// statement spans two. The fixture is the outcome, not the process.
func (b *builder) settle(id payment.CycleID) {
	// What the pacs.009 would have carried, built where the clearing house builds
	// it — off the CLOSED CYCLE, which is that institution's own row. The
	// settlement agent works from the instruction now rather than from the cycle,
	// because it holds no cycles table, so the seed has to hand it the same legs
	// the message would have. See payment.SettlementLegsOf.
	closed := must(b.csm().GetCycle(b.ctx, id))
	scheme, ok := b.csm().Scheme(closed.Scheme)
	if !ok {
		check(fmt.Errorf("seed: no scheme %q to settle %s under: %w", closed.Scheme, id, payment.ErrSchemeNotFound))
	}
	legs := payment.SettlementLegsOf(closed, scheme.Asset(), b.dep.CentralBankBIC())

	_, statements := must2(b.cb().SettleCycle(b.ctx, id, legs))
	b.advise(statements)

	// The CLEARING HOUSE's own copies move first, and they are also the payment
	// list — which the settlement does not carry: a settlement agent answers about
	// net positions per MEMBER and holds no way to enumerate the batch. That is
	// why the fan-out is the clearing house's in the running system, and why the seed asks
	// this institution rather than the statements above.
	for _, p := range must(b.csm().SettleAtCSM(b.ctx, id)) {
		// Then both banks, each on its own copy, and only the payee's bank pays
		// anybody. On a push those are two calls to two databases; on a pull the
		// payee's bank is also the submitter and there are still two, because the
		// payer's bank holds a row too. See payment.SettleAtBankTx.
		must(b.bank(p.CreditorDetails.Agent).SettleAtBank(b.ctx, p.ID))
		if other := p.DebtorDetails.Agent; other != p.CreditorDetails.Agent {
			must(b.bank(other).SettleAtBank(b.ctx, p.ID))
		}
	}
}

// initSCT submits a credit transfer. It is the SUBMITTING (debtor's) bank, so
// the request must name the counterparty: the NAME on the creditor's account,
// taken from the account the seed itself already built — this is the seed
// playing the payer who typed the payee's name in, not a lookup.
//
// The BIC too. SubmitPaymentTx cannot derive it — that row is the
// counterparty's, and a bank holds only its own — so the payer supplies it, as
// SEPA's payers did before 2016 and a cross-border payer still does. See
// payment.PartyDetails.Agent.
//
// The seed takes it off the bank it is about to address, which is the one place
// this fixture differs from the payer it is playing: a real payer reads the BIC
// off an invoice. What the seed must not do is take it off a LOOKUP, and it does
// not — cp is a bank this builder founded itself, in this process.
func (b *builder) sct(dp *payment.Bank, d deposit.Account, cp *payment.Bank, c deposit.Account, amount ledger.Amount, e2e, desc string) payment.InitiatePaymentRequest {
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      b.ref(d),
		Creditor:    b.ref(c),
		Amount:      amount,
		EndToEndID:  e2e,
		Description: desc,
		// BOTH sides, where an instruction from a customer would name only the
		// counterparty's. The submitting bank's own agent is discarded and
		// refilled from its own row either way (payment.SubmitPaymentTx), so
		// naming it changes nothing about the payment — what it buys is that the
		// seed's requests still say which bank submits, which is what initiate
		// reads and what cmd/server's Deployment.Submit on-us guard compares.
		DebtorDetails:   payment.PartyDetails{Agent: dp.BIC, Name: d.Name},
		CreditorDetails: payment.PartyDetails{Agent: cp.BIC, Name: c.Name},
	}
}

// sdd builds a direct debit. It is the SUBMITTING (creditor's) bank, so the
// request must name the counterparty: the name on the debtor's account, and the
// debtor's bank. See sct.
func (b *builder) sdd(dp *payment.Bank, d deposit.Account, cp *payment.Bank, c deposit.Account, amount ledger.Amount, mandate payment.MandateID, e2e, desc string) payment.InitiatePaymentRequest {
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPADD,
		Debtor:      b.ref(d),
		Creditor:    b.ref(c),
		Amount:      amount,
		MandateID:   mandate,
		EndToEndID:  e2e,
		Description: desc,
		// Both sides; see sct.
		DebtorDetails:   payment.PartyDetails{Agent: dp.BIC, Name: d.Name},
		CreditorDetails: payment.PartyDetails{Agent: cp.BIC, Name: c.Name},
	}
}

// submit hands an instruction to the deployment's own door, which is the other
// way a payment enters this scenario and the one that leaves a FILE behind it.
//
// # Why anything at all goes through the door
//
// A share of an output file is built when the clearing house takes an uploaded
// file in, and a share is what it hands to the bank that has to pay the payee
// when the cycle settles. Nothing composed leaves one: initiate hands the
// clearing house a row, so a payment seeded straight into a cycle is one the
// first business day would settle and could not deliver.
//
// So the two doors divide by what the scenario wants the payment to BE. A
// payment this build settles itself goes through initiate, where playing all
// three institutions is what makes the outcome fixed. A payment left IN FLIGHT
// goes through here, because the state it is left in is one only the transport
// can produce.
//
// Nothing has left the submitting bank when this returns; the instruction is in
// that bank's hub, and builder.build calls CarryToClearing once at the end to
// move every hub at once. See Deployment.
func (b *builder) submit(req payment.InitiatePaymentRequest) payment.Payment {
	return must(b.dep.Submit(b.ctx, req))
}

func (b *builder) build() {
	// --- Banks -------------------------------------------------------------
	// Each bank joins the network as a euro bank, and joining is three rows in
	// three databases: founding opens its suspense and reserve accounts in its
	// own book, the central bank opens its settlement account in the central
	// bank's, and the clearing house puts it in the roster. See
	// builder.provision.
	euro := []ledger.AssetCode{seedAsset}
	// Each bank issues addresses in the country its BIC names, under a bank code
	// of its country's own width — eight digits in Germany, five in Italy and
	// France, three in Sweden. That variety is the point: there is no offset at
	// which "the bank code" lives, so nothing can extract one without knowing
	// which country's structure to apply.
	//
	// Verde and Soleil are allocated the same code and are two different banks:
	// each country's registry allocates from its own range, and Italy's and
	// France's are both five digits wide. A bank code is unique within a COUNTRY
	// and nowhere else, which is why every table keyed by one is keyed by the pair.
	aurora := b.provision("Aurora Bank", "AURODEFFXXX", iban.DE, euro)
	verde := b.provision("Banca Verde", "VERDITMMXXX", iban.IT, euro)
	nord := b.provision("Nordhaven Bank", "NORDSESSXXX", iban.SE, euro)
	soleil := b.provision("Crédit Soleil", "SOLEFRPPXXX", iban.FR, euro)

	// --- Each bank subscribes to the routing directory ---------------------
	//
	// A separate act, and no part of provisioning a bank. Being in the roster is
	// what makes a bank REACHABLE; holding a copy of the roster is what makes it
	// able to reach anybody, and the two are separate because the copy is pulled
	// by each member on its own account. Nothing has told Aurora that Soleil
	// exists until Aurora asks.
	//
	// It runs after all four are written because this seed wants a scenario where
	// every bank can pay every other. Refreshing inside provision would give
	// Aurora a directory holding only itself, Verde one holding two, and a dataset
	// whose payments worked or did not depending on the order the banks joined —
	// which is real behaviour, and is measured in cmd/server rather than baked into the
	// fixture every other suite reads.
	for _, p := range []*payment.Bank{aurora, verde, nord, soleil} {
		b.subscribe(p)
	}

	// --- Each bank's catalogue ---------------------------------------------
	// Before any account, because every deposit account is opened FROM a
	// product: a floating terms row with no product would have nothing to
	// float to.
	for _, p := range []*payment.Bank{aurora, verde, nord, soleil} {
		b.products(p)
	}

	// --- Customer accounts (each bank mints its own addresses) -------------
	//
	// Serials restart at 1 per bank, because the bank code already says which
	// bank. Distinguishing them with a leading digit instead — 1001 at Aurora,
	// 2001 at Verde — would be a convention this seed invented and no address
	// anywhere has.
	alice := b.open(aurora, "Alice Andersson")
	aaron := b.open(aurora, "Aaron Apstorp")
	annie := b.open(aurora, "Annie Ahlberg")      // -> Dormant
	merchant := b.open(aurora, "Aurora Merchant") // hold-capture counterparty
	oldAcct := b.open(aurora, "Closed Account")   // -> Closed

	bruno := b.openOverdraft(verde, "Bruno Bianchi", 50_000) // 500.00 overdraft
	bella := b.open(verde, "Bella Bruno")
	bianca := b.open(verde, "Bianca Belli") // -> Frozen

	nora := b.open(nord, "Nora Nilsson")
	niklas := b.open(nord, "Niklas Nyborg")

	chloe := b.open(soleil, "Chloé Caron")
	claude := b.open(soleil, "Claude Clément")

	// --- Funding: cash in, which each bank holds as vault cash -------------
	b.fund(aurora, alice, 200_000)
	b.fund(aurora, aaron, 50_000)
	b.fund(aurora, annie, 30_000)
	b.fund(verde, bruno, 150_000)
	b.fund(verde, bella, 80_000)
	b.fund(verde, bianca, 40_000)
	b.fund(nord, nora, 300_000)
	b.fund(nord, niklas, 60_000)
	b.fund(soleil, chloe, 120_000)
	b.fund(soleil, claude, 90_000)

	// --- Lodgement: each bank places that cash on reserve -------------------
	//
	// The whole of it, per bank, which is what makes each bank's vault return to
	// zero and its reserve equal what its customers paid in. That is the state the
	// old Deposit left directly, and reproducing it exactly is deliberate: every
	// later phase of this scenario — the settled cycle, the netting, the returns —
	// was written against those reserve balances, so a seed that lodged only part
	// would change numbers this file's own tests and the web fixtures assert.
	//
	// A bank cannot settle out of vault cash, so this is what makes the cut-offs
	// below possible at all. See lodge.
	b.lodge(aurora, 280_000)
	b.lodge(verde, 270_000)
	b.lodge(nord, 360_000)
	b.lodge(soleil, 210_000)

	// --- Holds on Alice: active / released / captured ----------------------
	ctx := b.ctx
	must(aurora.Deposit.CreateHold(ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 10_000, Description: "Card pre-authorisation (hotel)",
	}))
	released := must(aurora.Deposit.CreateHold(ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 5_000, Description: "Cancelled online order",
	}))
	check(aurora.Deposit.ReleaseHold(ctx, released.ID))
	captured := must(aurora.Deposit.CreateHold(ctx, deposit.CreateHoldRequest{
		AccountID: alice.ID, Amount: 15_000, Description: "Card payment at Aurora Merchant",
	}))
	merchantPos := must(aurora.Deposit.Position(ctx, merchant.ID))
	must(aurora.Deposit.CaptureHold(ctx, captured.ID, merchantPos, 0, "Captured: card payment"))

	// --- End-of-day snapshots for Alice across two business days -----------
	must(aurora.Deposit.TakeEndOfDaySnapshot(ctx, alice.ID, b.clock.Now()))
	b.day()
	must(aurora.Deposit.TakeEndOfDaySnapshot(ctx, alice.ID, b.clock.Now()))

	// --- Account status lifecycle ------------------------------------------
	check(aurora.Deposit.MarkDormant(ctx, annie.ID)) // Active -> Dormant
	check(aurora.Deposit.Close(ctx, oldAcct.ID))     // zero balance -> Closed
	check(verde.Deposit.Freeze(ctx, bianca.ID))      // Active -> Frozen

	// --- Mandates for SEPA Direct Debit ------------------------------------
	//
	// Each is recorded at its CREDITOR's bank, which is the bank that holds the
	// mandate in SEPA and the only one this system lets record one. Reading the
	// second argument as "who may collect" is what says which bank() each call
	// goes through.
	m1 := must(b.bank(nord.BIC).CreateMandate(b.ctx, soleil.BIC, b.ref(chloe), b.ref(nora), 100_000))
	m2 := must(b.bank(aurora.BIC).CreateMandate(b.ctx, verde.BIC, b.ref(bruno), b.ref(aaron), 0))
	m3 := must(b.bank(soleil.BIC).CreateMandate(b.ctx, nord.BIC, b.ref(niklas), b.ref(claude), 25_000))
	check(b.bank(soleil.BIC).RevokeMandate(b.ctx, m3.ID)) // revoked, for display

	// --- Phase A: a fully settled SEPA Credit Transfer cycle ---------------
	sct1 := must(b.csm().OpenCycle(b.ctx, payment.SchemeSEPACT))
	b.initiate(b.sct(aurora, alice, nord, niklas, 25_000, "SCT-001", "Rent to N. Nyborg"))
	b.initiate(b.sct(nord, nora, verde, bella, 40_000, "SCT-002", "Invoice 2025-77"))
	b.initiate(b.sct(verde, bruno, soleil, chloe, 30_000, "SCT-003", "Consulting fee"))
	must(b.csm().CloseCycle(b.ctx, sct1.ID))
	b.settle(sct1.ID)

	b.day()

	// --- Phase B: a settled SEPA Direct Debit cycle (one will be returned) --
	sdd1 := must(b.csm().OpenCycle(b.ctx, payment.SchemeSEPADD))
	b.initiate(b.sdd(soleil, chloe, nord, nora, 20_000, m1.ID, "SDD-001", "Utility direct debit"))
	returned := b.initiate(b.sdd(verde, bruno, aurora, aaron, 12_000, m2.ID, "SDD-002", "Gym membership"))
	must(b.csm().CloseCycle(b.ctx, sdd1.ID))
	b.settle(sdd1.ID)

	// --- Phase C: return the settled direct debit (an R-transaction) --------
	b.returnPayment(returned.ID, "Debtor dispute — unauthorised collection")

	b.day()

	// --- Phase D: the two cycles this build LEAVES OPEN ---------------------
	//
	// One per scheme, and they are the cut-off windows the payments below are
	// taken into. A day validates its files against the open window BEFORE it
	// opens tomorrow's (see Deployment.AdvanceDay's ordering), so a deployment
	// handed none would refuse every payment in the first day's files with TM01.
	must(b.csm().OpenCycle(b.ctx, payment.SchemeSEPACT))
	must(b.csm().OpenCycle(b.ctx, payment.SchemeSEPADD))

	// --- Phase E: the payments in flight, through the deployment's door -----
	//
	// Submitted and not carried: each joins its bank's hub and goes no further
	// until phase G moves every hub at once, which is why the lending showcase
	// below can add one more without a cut-off having run in between. See
	// builder.submit for why these six do not go through initiate.
	b.submit(b.sct(aurora, aaron, soleil, claude, 8_000, "SCT-010", "Book order"))
	b.submit(b.sct(verde, bella, nord, niklas, 6_000, "SCT-011", "Shared dinner"))
	b.submit(b.sct(aurora, alice, verde, bella, 7_000, "SCT-020", "Birthday gift"))
	rejected := b.submit(b.sct(nord, nora, soleil, claude, 4_000, "SCT-021", "Deposit, flat viewing"))
	b.submit(b.sdd(soleil, chloe, nord, nora, 5_000, m1.ID, "SDD-010", "Monthly subscription"))
	b.submit(b.sdd(verde, bruno, aurora, aaron, 3_000, m2.ID, "SDD-011", "Gym membership"))

	// --- Lending: credit facilities across the network ---------------------
	b.lendingShowcase(aurora, verde, nord, alice, bruno, bella, niklas)

	// --- General-ledger primitives showcase on Aurora ----------------------
	b.glShowcase(aurora, aaron)

	// --- Phase G: the morning's files move, and nothing settles -------------
	//
	// The last act of the build, and it has to be: SCT-030 joins its bank's hub
	// from inside the lending showcase, so one call here carries everything above
	// and nothing is left behind in a hub.
	//
	// # What it leaves, and why THAT is the state to ship
	//
	// Every payment submitted in phase E is now Accepted, in the open cycle for
	// its scheme, with each receiving bank's share of the file it arrived in HELD
	// at the clearing house. The first business day anybody advances closes those
	// cycles, settles them at the agent and releases the shares — so the payee's
	// bank is handed its instructions and pays its customer, which is the whole
	// point and is what a reader watching the day's report gets to see happen.
	//
	// The alternative is a cycle no act in this system can advance. A payment put
	// into one without a file behind it has no share, so settling it is final at
	// the agent and reaches nobody — and refusing to settle it, which is what the
	// clearing house does, leaves the cut-off Closed with its payments Cleared and
	// nothing that moves either. Neither outcome is a dataset to hand a reader.
	// See ClearingHouse.unhandable and payment.ErrCycleNotReleasable.
	//
	// # The receiving banks hold nothing yet, and that is correct
	//
	// Before finality the submitting bank is the only bank that has heard of the
	// payment. So a bank's copy exists for what it submitted and for nothing it is
	// owed — which is settle-before-release read off the rows, and is why
	// payment/recon walks only payments from Cleared onwards.
	check(b.dep.CarryToClearing(b.ctx))

	// --- Phase H: an operator rejects one out of the open cycle -------------
	//
	// The last thing that happens to SCT-021, and it is a state only an open cycle
	// has: the clearing house drops the payment from the cut-off it was taken
	// into, and the share built for the payee's bank keeps the position — which is
	// cut out when the rest of the cycle settles, so that bank is handed the file
	// without it. A rendered file could not do that, which is the argument
	// csm/0001_init.sql's cycles statement makes and this is the fixture behind
	// it.
	//
	// An operator-initiated rejection carries no more specific reason code than
	// MS03 — the same choice the reject handler makes, for the same reason:
	// nothing here is the system detecting a condition of its own.
	//
	// A PUSH, so the bank told is the payer's own and has money to give back. On a
	// pull nothing has been posted yet and the rejection costs nobody anything;
	// see builder.reject.
	b.reject(rejected.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "Payee's account details could not be confirmed")
}

// lendingShowcase exercises every state a credit facility can be in: a
// priced overdraft, a term loan part-way through its schedule, a revolving
// line with a billed cycle, a delinquent loan, and an overdraft that is
// actually accruing rather than sitting at a limit that costs nothing. This is
// the data the web app's facility pages, its arrears badge, and Bruno's
// deposit page read.
func (b *builder) lendingShowcase(aurora, verde, nord *payment.Bank, alice, bruno, bella, niklas deposit.Account) {
	ctx := b.ctx

	// --- Bruno's overdraft, priced ------------------------------------------
	// He already has a 500.00 limit (openOverdraft, above); this is what makes
	// drawing on it cost him something rather than nothing: 15% arranged, 35%
	// on anything drawn beyond the limit.
	//
	// It is an OVERLAY rather than a product price, which is what it always
	// meant: a rate negotiated with Bruno, on his own timeline. Verde's product
	// is repriced separately below, and that reprice does not move him — which
	// is the whole point of the distinction and is visible on his account page.
	must(verde.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 150_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		b.clock.Now()))
	must(verde.Deposit.SetOverdraftLimit(ctx, bruno.ID, 50_000, b.clock.Now()))

	// --- Bella, migrated onto Premium ---------------------------------------
	// Effective a fortnight in. Her earlier days keep pricing at Basic's rate —
	// a migration is a forward-dated row, not a rewrite — which is what the
	// deposit page's terms history shows.
	//
	// The seeded data now holds all three cases side by side: floating through
	// the day-30 reprice (everyone else), negotiated and therefore unmoved by
	// it (Bruno), and migrated (Bella).
	must(verde.Deposit.ChangeProduct(ctx, bella.ID, b.cats[verde.ID].premium,
		b.clock.Now().AddDate(0, 0, 14)))

	// --- A term loan part-way through its schedule (Alice, Aurora) ----------
	// EUR 10,000, five years, 6%, annuity. Disbursed, then run day by day
	// through two monthly instalments paid on time, then a little further so a
	// fresh accrual is visible without reaching the third instalment. The
	// result: accrued interest, a partly-paid schedule, Current arrears.
	t1 := b.clock.Now()
	firstDue := t1.AddDate(0, 1, 0)
	loan := b.openLoan(aurora, alice, "Alice Home Loan", 1_000_000, 60_000, 60, firstDue, "Home loan payout")
	alicePos := must(aurora.Deposit.Position(ctx, alice.ID))

	b.runDays(aurora, int(firstDue.Sub(t1)/(24*time.Hour)))
	sched := must(aurora.Lending.Schedule(ctx, loan.ID))
	must(aurora.Lending.Repay(ctx, loan.ID, alicePos, sched[0].Total(), b.clock.Now(), "Instalment 1"))

	secondDue := t1.AddDate(0, 2, 0)
	b.runDays(aurora, int(secondDue.Sub(firstDue)/(24*time.Hour)))
	sched = must(aurora.Lending.Schedule(ctx, loan.ID))
	must(aurora.Lending.Repay(ctx, loan.ID, alicePos, sched[1].Total(), b.clock.Now(), "Instalment 2"))

	b.runDays(aurora, 10) // a fresh accrual builds up; the third instalment is not yet due

	// --- A delinquent loan (Niklas, Nordhaven) -------------------------------
	// A smaller loan than Alice's, disbursed and then left unpaid past two due
	// dates: one month plus twenty days past the first instalment, comfortably
	// inside the 30-59 bucket however the calendar months involved fall.
	t3 := b.clock.Now()
	niklasFirstDue := t3.AddDate(0, 1, 0)
	b.openLoan(nord, niklas, "Niklas Car Loan", 300_000, 90_000, 24, niklasFirstDue, "Car loan payout")
	target := t3.AddDate(0, 2, 20)
	b.runDays(nord, int(target.Sub(t3)/(24*time.Hour)))

	// --- Bruno, pushed into overdraft and accruing --------------------------
	// A card settlement pushes him into his priced overdraft. His accrual
	// window opens at ACCOUNT OPENING — the opening terms row every account
	// gets — and every end-of-day re-derives every day since, each at the
	// terms that were in force on it. Nothing has run Verde's end-of-day yet
	// (Niklas's story runs on Nordhaven's book, not Verde's), so this call
	// recomputes his whole life to date, and all of it accrues zero: the days
	// before SetOverdraftTerms ran carry the opening row's zero rate, and the
	// days between that and the SCT below recompute against a drawn balance of
	// zero, because he isn't overdrawn yet. The SCT
	// overdraws him immediately: its debtor leg posts in the debtor bank's half of
	// submission, so
	// the balance moves right away without its clearing cycle needing to
	// close or settle. Then 45 days pass and interest accrues, a charge
	// capitalizes it, a backdated repricing lands (see below), and 15 more
	// days build a fresh accrual on top of both.
	//
	// That is not the figure this phase ends on, though. RunEndOfDay drives a
	// participant's whole book, not one facility, and Bella's line below
	// shares this same book (Verde). Her own runDays(verde, 30) keeps Bruno's
	// overdraft accruing for another 30 days after this phase returns, so the
	// accrued interest the seed actually produces reflects 45 days
	// post-capitalization, all at the 18% the repricing below has already put
	// in force by then, not the 15 this phase runs on its own. The repricing
	// also reaches backwards: twenty-ONE of the days that already elapsed
	// before the charge — the spans ending 2026-03-14 through 2026-04-03
	// inclusive — accrued at 15% when they ran and are repriced to 18% by the
	// very next end-of-day. That is twenty-one, not the twenty-day gap it
	// looks like, because a span is named by its END date: the span ending on
	// the effective day itself is already priced at the new rate. Bruno's
	// final figure blends both rates over both balances it accrued on — EUR
	// 200.00 before the charge, EUR 203.78 after — for a total ≈ EUR 4.87, not
	// the single-rate ≈ EUR 3.77 a pure-15% run would give. Changing Bella's
	// 30-day span, or the repricing's twenty-day offset, changes Bruno's
	// final number.
	//
	// It goes through the DOOR, like the rest of the build's in-flight payments,
	// and joins Verde's hub rather than a cycle: the build's closing act carries
	// every hub at once, and this one is submitted from inside a showcase that
	// runs before it. So it ends Accepted in the open SCT cycle alongside SCT-020,
	// and the first business day settles and delivers it.
	check(verde.RunEndOfDay(ctx, b.clock.Now()))

	brunoBalance := must(verde.Deposit.GetBalance(ctx, bruno.ID))
	overdrawBy := ledger.Amount(20_000) // EUR 200 into the EUR 500 arranged limit
	b.submit(b.sct(verde, bruno, aurora, alice, brunoBalance.Book+overdrawBy, "SCT-030", "Card settlement"))

	b.runDays(verde, 45)
	must(verde.Deposit.ChargeOverdraftInterest(ctx, bruno.ID, b.clock.Now()))

	// --- Bruno, repriced mid-life -------------------------------------------
	// The arranged rate moves from 15% to 18%, effective TWENTY DAYS AGO — a
	// repricing agreed on one date and entered on another, which is the
	// ordinary case and the one mutable terms could not represent at all.
	//
	// This is the seed's demonstration of the whole change. The next end-of-day
	// re-derives twenty-two days at 18% — the spans ending 2026-03-14 through
	// 2026-04-04 — twenty-one of them previously charged at 15% and the
	// twenty-second (the span ending on the new day itself) never priced
	// before, and posts the difference as ordinary delta interest. Nothing is
	// rewritten and all three terms rows — the zero-rate opening row, 15%, and
	// 18% — stay on the timeline, which the account page now renders.
	//
	// Before terms were effective-dated this could not happen: the recompute
	// window would have been reset to today and the twenty-one days behind it
	// would have kept the old rate forever.
	must(verde.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 180_000, UnarrangedRate: 350_000, DayCount: interest.ACT365},
		b.clock.Now().AddDate(0, 0, -20)))

	b.runDays(verde, 15)

	// --- A revolving line, partly drawn and billed (Bella, Verde) -----------
	// EUR 2,500 limit, 18%, 2% minimum payment. Drawn EUR 1,000, accrued for a
	// month, then charged: the accrued interest capitalizes into principal and
	// the cycle's minimum payment is billed. This runs last among Verde's
	// stories, and nothing runs after it, so the billed cycle — due a month
	// out — stays Current rather than aging past due the way it would if it
	// ran before Bruno's own accrual instead of after.
	//
	// Its own 30-day accrual lands on that same book, though: RunEndOfDay
	// drives a participant's whole book, not one facility, so this call also
	// carries Bruno's overdraft forward another 30 days — which is where the
	// 45 days behind his final figure above come from, not the 15 his own
	// phase runs.
	line := b.openLine(verde, bella, "Bella Card Line", 250_000, 180_000, 20_000, 100_000, "Card line draw")
	b.runDays(verde, 30)
	must(verde.Lending.ChargeInterest(ctx, line.ID, b.clock.Now()))
}

// glShowcase exercises the raw general-ledger primitives on one bank so that all
// five account types (Asset, Liability, Equity, Revenue, Expense) and a manual
// transaction + reversal appear in the data. Liability is already present via the
// bank's customer-deposit and suspense GL accounts; this adds the other four.
func (b *builder) glShowcase(p *payment.Bank, customer deposit.Account) {
	ctx := b.ctx
	glID := must(p.Ledger.GetSubledger(ctx, p.CustomerSubledger)).LedgerID

	equitySub := must(p.Ledger.CreateSubledger(ctx, glID, "Equity"))
	shareCapital := must(p.Ledger.CreateAccount(ctx, equitySub.ID, "Share Capital", ledger.Equity, seedAsset))
	treasurySub := must(p.Ledger.CreateSubledger(ctx, glID, "Treasury"))
	vault := must(p.Ledger.CreateAccount(ctx, treasurySub.ID, "Vault Cash", ledger.Asset, seedAsset))
	incomeSub := must(p.Ledger.CreateSubledger(ctx, glID, "Income"))
	feeIncome := must(p.Ledger.CreateAccount(ctx, incomeSub.ID, "Fee Income", ledger.Revenue, seedAsset))
	expenseSub := must(p.Ledger.CreateSubledger(ctx, glID, "Expenses"))
	opex := must(p.Ledger.CreateAccount(ctx, expenseSub.ID, "Operating Expenses", ledger.Expense, seedAsset))

	// Capitalisation: Vault Cash (asset) up, Share Capital (equity) up.
	must(p.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Initial share capital",
		Entries: []ledger.Entry{
			{AccountID: vault.ID, Amount: 100_000, Direction: ledger.Debit},
			{AccountID: shareCapital.ID, Amount: 100_000, Direction: ledger.Credit},
		},
	}))

	// Operating expense: Operating Expenses (expense) up, Vault Cash (asset) down.
	must(p.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Office rent",
		Entries: []ledger.Entry{
			{AccountID: opex.ID, Amount: 2_000, Direction: ledger.Debit},
			{AccountID: vault.ID, Amount: 2_000, Direction: ledger.Credit},
		},
	}))

	// Monthly account fee: customer deposit (liability) down, Fee Income (revenue) up.
	customerPos := must(p.Deposit.Position(ctx, customer.ID))
	fee := must(p.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Monthly account fee",
		Entries: []ledger.Entry{
			{AccountID: customerPos.Account, Subsidiary: customerPos.Subsidiary, Amount: 500, Direction: ledger.Debit},
			{AccountID: feeIncome.ID, Amount: 500, Direction: ledger.Credit},
		},
	}))

	// Reverse the fee (goodwill waiver) to demonstrate a reversal.
	must(p.Ledger.ReverseTransaction(ctx, fee.ID, "Fee waived — goodwill"))
}
