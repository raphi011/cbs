package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
)

// Config names the two institutions this process plays and the two hosts a
// subscriber dials.
//
// The BICs are configured rather than read, because neither institution has a
// participant row: member banks are admitted and carry their own, and the
// central bank and the clearing house ARE the configuration.
//
// THREE BASE URLS ARE THE WHOLE ADDRESS BOOK. A member bank holds both, the
// clearing house holds the central bank's, and the settlement agent holds none
// because it dials nobody. There is no actor table to keep in step with the
// roster: a file reaches a subscriber by being put in that subscriber's download
// queue, and enrolment is what creates one.
type Config struct {
	CentralBankBIC   iso20022.BIC
	ClearingHouseBIC iso20022.BIC

	CentralBankURL   string
	ClearingHouseURL string
}

// validate refuses a configuration no file could be routed under.
//
// The two identities must be present, structurally valid, and DIFFERENT. The
// last is the one worth stating: one BIC in both fields is not a deployment with
// two institutions that happen to share an address, it is one subscriber
// identity for two hosts — and a bank enrolling at the clearing house would find
// itself enrolled at the settlement agent, in a system whose whole point is that
// clearing and settlement are different jobs.
func (c Config) validate() error {
	if err := c.CentralBankBIC.Validate(); err != nil {
		return fmt.Errorf("server: central bank BIC: %w", err)
	}
	if err := c.ClearingHouseBIC.Validate(); err != nil {
		return fmt.Errorf("server: clearing house BIC: %w", err)
	}
	if c.CentralBankBIC == c.ClearingHouseBIC {
		return fmt.Errorf("server: %s is configured as both the central bank and the clearing house", c.CentralBankBIC)
	}
	if c.CentralBankURL == "" || c.ClearingHouseURL == "" {
		return errors.New("server: both hosts need a URL; a subscriber with nowhere to dial can neither send nor collect")
	}
	return nil
}

// A Deployment is every institution this process holds, the clock they all read,
// and the business day that drives them.
//
// # It is not a request handler and answers nothing
//
// The three surfaces are api/bank, api/csm and api/centralbank, each over an
// interface that package declares. What this type does is hand out the ONE
// institution a listener is bound to — Bank, ClearingHouse, CentralBank, one
// file apiece beside this one — and each of those is bound at construction and
// never rebound.
//
// The three satisfy their surface's interface STRUCTURALLY, so the dependency
// runs one way and none of the three surface packages names anything here.
//
// # One network per institution, and never a shared one
//
// A bank's identity is constructor state on payment.Network, so one shared
// Network would mean every bank's port resolving in one register — one bank
// reading another bank's customers, one layer above where the book recorder can
// see it. Each institution binds its own.
//
// # There are no goroutines
//
// Not one. HTTP requests queue work — a customer's instruction joins their
// bank's hub, an operator's cut-off becomes a settlement instruction uploaded to
// the settlement agent — and AdvanceDay drains all of it synchronously, on the
// caller's goroutine, in the order a business day runs in.
// Bulk clearing is store-and-forward against a cutoff clock, so this is more
// faithful than a set of actors AND simpler, which is not usually the same
// direction.
type Deployment struct {
	// nets mints one Network per institution. It is what the institutions below
	// bind from, and what Reset clears the store through.
	nets *payment.Networks

	// clock is the deployment's business date, and the ONE time source under
	// this process: the stores, the networks and every message header read it,
	// and time.Now appears nowhere below it. See calendar.Clock, and AdvanceDay.
	clock *calendar.Clock

	cfg Config
	log *slog.Logger

	// The two hosts, built once. Each holds an ebics.Server, which is the whole
	// of what it means to be dialled: a download queue per enrolled subscriber
	// and a log of the orders they have uploaded.
	csm *ClearingHouse
	cb  *CentralBank

	// seq numbers the messages this deployment emits. See nextMsgID.
	seq atomic.Uint64

	// journal accumulates what this deployment has done since the last advance:
	// every file moved, every decision made about a transaction, and every file
	// an institution could not process. AdvanceDay takes it and hands it back as
	// the day's report.
	journal journal

	// populate rebuilds the sample dataset. It must be idempotent: the process
	// calls it at boot and Reset calls it again after clearing the store.
	//
	// It is handed a seed.Deployment as well as the networks, because admitting a
	// bank is a conversation between three institutions and a reseed that founded
	// its banks without one would rebuild a network in which nobody can be paid.
	//
	// It takes the FACTORY and not one institution's network, and that is why it
	// is here rather than on any of the three: building the sample dataset is
	// three institutions' work, and it is not an institution's act.
	populate func(context.Context, *payment.Networks, seed.Deployment) error

	// mu guards banks. The two hosts are written once, in NewDeployment, and are
	// never replaced.
	mu sync.Mutex
	// banks is every member bank this process holds a database for, by ADDRESS.
	// A bank's ParticipantID is its BIC (payment.AsBank), so this is keyed by the
	// one identity a bank has.
	banks map[iso20022.BIC]*Bank

	// resetMu serializes Reset AND AdvanceDay. They are the two acts over the
	// whole deployment rather than over one institution, and neither may run
	// while the other does: a day working through the queues while the store is
	// being emptied underneath it would post into tables the reset has already
	// truncated.
	resetMu sync.Mutex
}

// NewDeployment builds every institution this process holds: the clearing house,
// the settlement agent, and one member bank per database the store set names.
//
// populate is the sample-dataset builder, called again on every Reset; pass nil
// for a deployment that resets to an empty system. If log is nil, the default
// slog logger is used.
//
// It performs I/O — a bank's network is minted over that bank's own database,
// and the roster read is what says which of them are members — so it takes a
// context and can fail. The alternative is a constructor that leaves N handlers
// to fail on their first request.
//
// # Enrolment follows the ROSTER, and the roster alone
//
// Every bank with a database gets a Bank, because a bank founded and not yet
// admitted still has a book and customers to serve; only the banks the clearing
// house's roster names are ENROLLED at the two hosts, because enrolment is what
// creates a download queue and a queue is what makes a bank reachable. That is
// the same fact stated once instead of an actor table kept in step with a roster
// by hand.
//
// The clearing house enrols at the settlement agent for the same reason a bank
// does: it uploads settlement instructions and collects the answers.
func NewDeployment(ctx context.Context, nets *payment.Networks, clock *calendar.Clock, cfg Config,
	populate func(context.Context, *payment.Networks, seed.Deployment) error, log *slog.Logger) (*Deployment, error) {

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	if populate == nil {
		populate = func(context.Context, *payment.Networks, seed.Deployment) error { return nil }
	}

	d := &Deployment{
		nets:     nets,
		clock:    clock,
		cfg:      cfg,
		log:      log,
		populate: populate,
		banks:    map[iso20022.BIC]*Bank{},
	}
	d.cb = &CentralBank{
		d: d, net: nets.CentralBank(), ops: nets.CentralBank(),
		bic: cfg.CentralBankBIC, host: ebics.NewServer(),
	}
	d.csm = &ClearingHouse{
		d: d, net: nets.ClearingHouse(), ops: nets.ClearingHouse(),
		bic:  cfg.ClearingHouseBIC,
		host: ebics.NewServer(),
		cb:   ebics.NewClient(ebics.SubscriberID(cfg.ClearingHouseBIC), cfg.CentralBankURL),
		held: map[payment.PaymentID]heldReturn{},
	}
	d.cb.host.Enrol(ebics.SubscriberID(cfg.ClearingHouseBIC))

	bics, err := nets.Stores().Banks(ctx)
	if err != nil {
		return nil, fmt.Errorf("server: listing the banks this deployment holds: %w", err)
	}
	members, err := d.memberBICs(ctx)
	if err != nil {
		return nil, err
	}
	for _, bic := range bics {
		if _, err := d.mintBank(ctx, bic); err != nil {
			return nil, err
		}
		if members[bic] {
			d.enrol(bic)
		}
	}
	return d, nil
}

// memberBICs is every address the clearing house's roster names.
//
// The roster is the membership list, and membership is what enrolment stands
// for. A bank founded and never admitted is absent here and present in the bank
// index above, which is exactly the difference between having a book and having
// a place in a scheme.
func (d *Deployment) memberBICs(ctx context.Context) (map[iso20022.BIC]bool, error) {
	entries, err := d.csm.ops.ListRosterEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("server: reading the clearing house's roster: %w", err)
	}
	out := make(map[iso20022.BIC]bool, len(entries))
	for _, e := range entries {
		out[e.BIC] = true
	}
	return out, nil
}

// enrol gives one bank a download queue at each host. Enrolling twice is
// nothing, so a reseed of a bank that is already a subscriber is not a special
// case.
func (d *Deployment) enrol(bic iso20022.BIC) {
	d.csm.host.Enrol(ebics.SubscriberID(bic))
	d.cb.host.Enrol(ebics.SubscriberID(bic))
}

// mintBank opens one bank's database and gives it its two connections.
func (d *Deployment) mintBank(ctx context.Context, bic iso20022.BIC) (*Bank, error) {
	net, err := d.nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return nil, fmt.Errorf("server: opening %s's store: %w", bic, err)
	}
	b := &Bank{
		d: d, net: net, ops: net, bic: bic,
		csm: ebics.NewClient(ebics.SubscriberID(bic), d.cfg.ClearingHouseURL),
		cb:  ebics.NewClient(ebics.SubscriberID(bic), d.cfg.CentralBankURL),
	}
	d.mu.Lock()
	d.banks[bic] = b
	d.mu.Unlock()
	return b, nil
}

// Log is the logger every surface's middleware chain writes through.
func (d *Deployment) Log() *slog.Logger { return d.log }

// ClearingHouse and CentralBank are the two institutions this process
// configures. Each is built once, so a listener holds the same value the day
// engine drives.
func (d *Deployment) ClearingHouse() *ClearingHouse { return d.csm }
func (d *Deployment) CentralBank() *CentralBank     { return d.cb }

// CentralBankBIC is the address settlement instructions are uploaded to.
//
// It is configured rather than discovered — the settlement agent has no roster
// row, because it is not a member of the scheme it settles — so a caller that
// needs to name it has nowhere else to read it from. The seed is the caller: it
// plays every institution in one process and uploads nothing, and a settlement
// instruction names the agent at one end of every leg.
func (d *Deployment) CentralBankBIC() iso20022.BIC { return d.cfg.CentralBankBIC }

// Bank is one member bank's own view, over that bank's own database.
//
// It is the same value every time, because a Bank holds connections: two clients
// minted afresh per request would be two connection pools per request. A bank
// this deployment did not know about at construction is opened and remembered
// here, which is what a bank founded through the API into a running deployment
// gets — a book and a console, and no listener until the next restart.
func (d *Deployment) Bank(ctx context.Context, pid payment.ParticipantID) (*Bank, error) {
	bic := iso20022.BIC(pid)
	d.mu.Lock()
	b, ok := d.banks[bic]
	d.mu.Unlock()
	if ok {
		return b, nil
	}
	return d.mintBank(ctx, bic)
}

// banksInOrder is every bank this deployment holds, ascending by address.
//
// Sorted rather than in map order, because a business day visits them one at a
// time and two runs that visited them in different orders would produce
// different files for the same instructions. What a real network has instead is
// N institutions running concurrently; what this has is one goroutine and a
// stated order.
func (d *Deployment) banksInOrder() []*Bank {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*Bank, 0, len(d.banks))
	for _, bic := range slices.Sorted(maps.Keys(d.banks)) {
		out = append(out, d.banks[bic])
	}
	return out
}

// subscribers is every bank enrolled at the clearing house, ascending by
// address: the members, and the only banks a clearing day has anything to say
// about.
//
// A bank that is not one still has a book, still accrues interest and still runs
// its end of day. It has no download queue, so every clearing phase would answer
// it EBICS_INVALID_USER_OR_USER_STATE — a refusal that is true, is not news, and
// would appear in every day's report for the life of the deployment.
func (d *Deployment) subscribers() []*Bank {
	var out []*Bank
	for _, b := range d.banksInOrder() {
		if d.csm.host.Enrolled(ebics.SubscriberID(b.bic)) {
			out = append(out, b)
		}
	}
	return out
}

// AddBank gives a provisioned bank its place in the network: its own view, its
// two connections, and a download queue at each host.
//
// It is what makes a bank REACHABLE, and reachability is the whole of what it
// says. A bank with no queue at the clearing house is not a slow bank: nothing
// can be addressed to it, because addressing IS enqueueing, and the clearing
// house refuses to route to a BIC it holds no queue for.
//
// It runs after a bank has been provisioned, because there is nothing to give a
// place to before that: provisioning writes the roster entry that says this bank
// is a member, and this is that fact made operational.
func (d *Deployment) AddBank(ctx context.Context, p *payment.Bank) error {
	if err := p.BIC.Validate(); err != nil {
		return fmt.Errorf("server: %q: %w", p.Name, err)
	}
	if _, err := d.mintBank(ctx, p.BIC); err != nil {
		return err
	}
	d.enrol(p.BIC)
	return nil
}

// now is the instant every header this process writes is stamped from, and it is
// the deployment's business date.
//
// One clock, read by the stores, the networks and the messages alike. A second
// answer to what time it is would put a pacs.008 dated today against a payment
// booked on the business date, years apart.
func (d *Deployment) now() time.Time { return d.clock.Now() }

// nextMsgID mints the identifier a message travels under: the sender's BIC and a
// number nobody else in this deployment will use.
//
// Per-deployment and not per-sender, which is a simplification worth naming. A
// real BizMsgIdr is unique within the sender and nowhere else, so two banks
// legitimately emit the same one; a receiver that assumed otherwise would
// deduplicate one bank's message against another's. One counter is strictly
// stronger than the standard requires and cannot therefore hide a bug the
// standard would allow. What it buys is that a message id is unique under a
// clock that only moves a day at a time, which a timestamp-derived one would not
// be.
func (d *Deployment) nextMsgID(from iso20022.BIC) string {
	return fmt.Sprintf("%s-%d", from, d.seq.Add(1))
}

// messageContext is the header every message this deployment builds carries: who
// to, from whom, under which id, at what business date.
func (d *Deployment) messageContext(from, to iso20022.BIC) payment.MessageContext {
	return payment.MessageContext{From: from, To: to, MsgID: d.nextMsgID(from), Now: d.now()}
}

// ---------------------------------------------------------------------------
// The doors: everything that comes into this system from outside a business day
// ---------------------------------------------------------------------------
//
// A file is collected from a queue and dealt with by an institution, inside
// AdvanceDay. Nothing below is a file. A customer instructs their bank, an
// operator reaches a cut-off or rejects a payment, a bank moves cash onto
// reserve — each is an instruction from outside the day, so each runs the
// instructing institution's own half SYNCHRONOUSLY, on the caller's goroutine.
//
// Two properties are shared by every one of them. The synchronous half is what
// lets a caller be told "no" — a customer whose instruction fails their own
// bank's checks hears it then and there, which is why api can answer 422 rather
// than 202 followed by a rejection nobody can be told about. And WHERE A FILE
// FOLLOWS, IT IS SENT OUTSIDE THE UNIT OF WORK: uploading while holding a
// transaction would put a file on a connection against uncommitted state, or
// against state a rollback removed.
//
// Submit is the door with NO file at all. A customer's instruction joins its
// bank's hub and travels at that bank's next cut-off, which is what makes this a
// bulk network; see Bank.submit.
//
// What none of them answers is what the far side thinks. That arrives on a later
// download, when a business day runs.

// Submit runs the submitting bank's half synchronously and hands the instruction
// to that bank's hub.
//
// It returns an Initiated payment and nothing more, which is why api answers 202
// rather than 201 — and the 202 now says something narrower than it used to: not
// "sent and unanswered" but "taken and not yet sent". GET /payments/pending is
// the difference.
//
// # Which bank is handed the instruction
//
// The scheme's DIRECTION decides, and it is asked rather than assumed. A credit
// transfer goes to the payer's bank, because the payer is instructing their own
// bank to push; a direct debit goes to the PAYEE's bank, because a collection is
// the payee asking for money it is owed.
//
// Taking the debtor unconditionally is the wrong bank for every direct debit,
// and it is invisible until a pull exists: the payment goes through, the books
// balance, and the only thing wrong is that the wrong institution did the work
// and signed the file.
//
// # And which payments there is no bank to hand it to at all
//
// An ON-US payment — one bank at both ends — is refused before any of that.
// Nothing leaves the institution, so no reserves move, no position nets and no
// settlement agent has anything to settle. That refusal is this system declining
// a ROUTE and not the payment; a book transfer between two customers of one bank
// is a real product and a different one, performed by the bank's own register.
// See payment.ErrOnUsPayment.
//
// A payment to or from a bank the scheme has NOT ADMITTED is refused in the same
// place — see payment.ErrBankNotAdmitted.
func (d *Deployment) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	scheme, ok := d.csm.ops.Scheme(req.Scheme)
	if !ok {
		return payment.Payment{}, fmt.Errorf("server: no scheme %q, so no bank submits it: %w", req.Scheme, payment.ErrSchemeNotFound)
	}
	// A payment that never leaves one bank is not a payment this system carries.
	//
	// Both customers bank at the same institution, so the movement is between two
	// of that bank's own deposit accounts: no interbank obligation exists, so
	// there is nothing to clear and nothing to settle, and a real bank books it
	// internally without a scheme ever hearing about it.
	//
	// Refused HERE, at the one door every submission comes through, and before
	// the submitting bank's half runs. Submit is synchronous, so a guard placed
	// any later would have to unwind a committed debtor leg rather than decline
	// it.
	//
	// It is asked of the two PARTIES and not of the submitter. Which bank submits
	// flips with the scheme's direction, and on-us is precisely the case where
	// both answers are the same institution; a guard that read the submitter
	// would be comparing a bank with itself.
	//
	// The two AGENTS are compared where an instruction names both, and the
	// counterparty's address is RESOLVED against the submitter's own register
	// further down. Two guards for one rule, and the reason is that they cover
	// different instructions rather than the same one twice: an instruction from
	// a customer names its own side's bank nowhere, because the submitting bank
	// fills that in from its own row (payment.SubmitPaymentTx), so this
	// comparison cannot fire on one. What it catches is a caller that names both
	// — the seed, and this package's fixtures.
	if req.DebtorDetails.Agent != "" && req.DebtorDetails.Agent == req.CreditorDetails.Agent {
		return payment.Payment{}, fmt.Errorf("server: %s is both the payer's bank and the payee's for this instruction: %w",
			req.DebtorDetails.Agent, payment.ErrOnUsPayment)
	}
	// And a payment one of whose banks the scheme has not admitted.
	//
	// Asked HERE, at the same door and of the same two parties, because the
	// answer that matters is the one given before the submitting bank's half
	// runs: that half posts the debtor leg, and every refusal downstream of it
	// has a committed posting to unwind. The clearing house asks it again from
	// its own row — payment.Network.bothBanksAreMembersTx — and that one is the
	// judgement; this one is the door declining to carry the instruction at all,
	// which is what makes the API answer a 422 rather than a 202 followed by a
	// rejection nobody can be told about.
	//
	// A BIC nobody has been admitted on is not a member, which is a 422 and
	// ErrBankNotAdmitted rather than a 404. The clearing house cannot tell "no
	// such institution" from "an institution I have not admitted", and answering
	// 404 would be it reporting a bank row it should not be reading.
	//
	// In practice it fires on the SUBMITTING side only, and the other side is
	// covered by something stronger: an instruction names no counterparty bank,
	// so the submitting bank derives one from the counterparty's address out of
	// its own copy of this same roster, and an address at a non-member resolves
	// to nothing before the debtor leg posts (payment.ErrBankCodeUnknown). The
	// loop still walks both, because a caller that names both is a caller this
	// can answer precisely.
	for _, side := range []struct {
		role  string
		agent iso20022.BIC
	}{
		{"payer's bank", req.DebtorDetails.Agent},
		{"payee's bank", req.CreditorDetails.Agent},
	} {
		// A side naming no bank is skipped rather than refused, exactly as the
		// on-us guard above skips it: "not a member" is not the truth about a
		// party the request did not name.
		if side.agent == "" {
			continue
		}
		if _, err := d.csm.ops.GetRosterEntryByBIC(ctx, side.agent); err != nil {
			if errors.Is(err, payment.ErrRosterEntryNotFound) {
				return payment.Payment{}, fmt.Errorf("server: the %s, %s, is not a member of %s: %w",
					side.role, side.agent, req.Scheme, payment.ErrBankNotAdmitted)
			}
			return payment.Payment{}, err
		}
	}

	b, err := d.member(submitterOf(scheme, req.DebtorDetails.Agent, req.CreditorDetails.Agent))
	if err != nil {
		return payment.Payment{}, err
	}
	// On-us, asked by ADDRESS, and this is the arm that fires for an instruction
	// a customer actually hands in.
	//
	// It RESOLVES rather than comparing BICs. A derived agent answers at
	// INSTITUTION granularity, out of a copy that pairs a bank code with a BIC —
	// so "the counterparty's agent is this bank" says the payee's address was
	// issued under this bank's code, and not that this bank holds the account.
	// Those differ for exactly the case that matters: an address under the right
	// bank's code that the bank does not hold, which is somebody else's customer
	// or nobody's. What IS a fact this bank holds is whether the address resolves
	// in its own register.
	//
	// It reads this bank's OWN register and no other, which is what makes it a
	// question this institution may ask at all. Marked as this bank's work so the
	// recorder attributes it here rather than to nobody.
	counterparty := req.Creditor
	if scheme.Direction() == payment.Pull {
		counterparty = req.Debtor
	}
	if counterparty.Identifier != (deposit.Identifier{}) {
		switch _, err := b.ops.ResolveIdentifier(withActor(ctx, b.bic), counterparty.Identifier); {
		case err == nil:
			return payment.Payment{}, fmt.Errorf("server: %s holds both the payer's account and the payee's for this instruction: %w",
				b.bic, payment.ErrOnUsPayment)
		case errors.Is(err, deposit.ErrIdentifierNotFound):
			// The ordinary case: the payee is somebody else's customer, which is
			// the only thing this bank can conclude and the only thing it needs to.
		default:
			return payment.Payment{}, err
		}
	}
	return b.submit(ctx, req)
}

// Return sends a settled payment back: the R-transaction, and the last thing
// that can happen to a payment.
//
// # Which bank is handed the instruction
//
// The bank that RECEIVED the original instruction, which is never the one that
// submitted it. On a push that is the payee's bank, which was credited and has
// discovered it cannot apply the money; on a pull it is the payer's bank, whose
// customer disputes the collection. Both are the far side of the file that
// started the payment, which is what makes a return the only flow here that
// begins at the bank that answered. See returnerOf.
//
// # It answers with an error and nothing else
//
// The returning bank's half can refuse, which is why an error is the whole of
// what a caller needs: on a push a payee who has spent the money comes back AM04
// and nothing was uploaded.
//
// A PAYMENT would be no use. The row the caller could read is still Settled —
// one leg is posted, and a return is not finished until the other bank posts on
// a later day — so handing it back would say less than the caller already knows.
func (d *Deployment) Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	// The routing question, and only that: which bank's instruction is this? It
	// is asked here rather than inside the bank for the reason Submit asks the
	// scheme here — the answer is what CHOOSES the institution, so no institution
	// can have made it. The read costs no book: a payment is a network-scoped row,
	// and reading one records nothing at all (see books_test.go).
	p, err := d.csm.ops.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	scheme, ok := d.csm.ops.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("server: no scheme %q, so no bank returns %s: %w", p.Scheme, p.ID, payment.ErrSchemeNotFound)
	}
	b, err := d.member(returnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent))
	if err != nil {
		return err
	}
	return b.returnPayment(ctx, id, reason, text)
}

// Lodge is a member bank moving its own vault cash onto reserve at the central
// bank, named by its ADDRESS.
//
// It exists beside Bank.Lodge because the seed and the operator hold an address
// where a bank's own listener holds an identity. See Bank.lodge, which is the
// act itself.
func (d *Deployment) Lodge(ctx context.Context, bic iso20022.BIC, asset ledger.AssetCode,
	amount ledger.Amount) (payment.LodgementInstruction, error) {

	b, err := d.member(bic)
	if err != nil {
		return payment.LodgementInstruction{}, err
	}
	return b.lodge(ctx, asset, amount)
}

// RefreshDirectory is one member bank subscribing: it takes the roster the
// clearing house publishes and replaces that bank's own copy with it.
//
// # It is not a file on a connection, and that is why it is here
//
// Every other hop between institutions is a document in a download queue. This
// one is a FILE being delivered by the scheme's vendor — the shape a real
// routing directory arrives in, since the EPC's Register of Participants is
// downloaded and not queried per payment — so there is no envelope, no order id
// and no queue to wait on. What the deployment is standing in for is the vendor,
// and the deployment is the only thing here that holds both institutions.
//
// The two reads cannot be one unit of work and must not look like one. The
// roster is read at the clearing house and committed there before the copy is
// written at the bank, so a member admitted between the two shows up on the next
// refresh — which is the staleness this design is built out of, arriving at the
// smallest scale it has.
//
// # Not a timer, and not a push
//
// A background poller would need a goroutine, and this deployment has none. A
// push would make the clearing house hold a subscriber list and a retry policy,
// which is a delivery system rather than a publisher — and the real vendor does
// not know who is listening. What it has instead is a CADENCE: every business
// day begins with each member pulling it, and this route is the operator asking
// for one out of turn.
func (d *Deployment) RefreshDirectory(ctx context.Context, bic iso20022.BIC) ([]payment.DirectoryEntry, error) {
	published, err := d.csm.ops.ListRosterEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("server: reading the published roster for %s: %w", bic, err)
	}
	subscriber, err := d.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return nil, err
	}
	return subscriber.ops.RefreshDirectory(withActor(ctx, bic), published)
}

// member is the bank this deployment holds for an address, refusing one it does
// not.
//
// The refusal is what Submit and Return both need before any half runs: a bank
// this process has no database for could neither post nor upload, so an
// instruction handed to it would sit Initiated for ever.
func (d *Deployment) member(bic iso20022.BIC) (*Bank, error) {
	d.mu.Lock()
	b, ok := d.banks[bic]
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("server: this deployment holds no bank at %s", bic)
	}
	return b, nil
}

// Reset discards all persisted state, rewinds the business date and rebuilds the
// sample dataset. It must go through the store: swapping an in-memory object
// graph would leave every row in the database intact and still report success.
//
// It is the DEPLOYMENT's act and not the settlement agent's, which is why it is
// here and not on CentralBank — that surface serves it because the operator's
// console is there.
//
// Resets serialize, and they take the same lock AdvanceDay takes. They are the
// one operation in this system that is NOT a single unit of work and cannot be
// made into one — the clear is its own unit of work and the rebuild is dozens
// more, because the seed builder drives the ordinary public API. Two overlapping
// resets interleave: the second clears over the first's half-built scenario and
// the first finishes writing on top of the second's, leaving several copies of
// some entities and none of others. Eight concurrent resets produced twelve
// participants where there should have been four.
//
// A mutex is the honest fix for that shape: the operation is rare, bounded and
// idempotent. It makes resets exclusive within ONE process; two servers sharing
// a database could still race, which is a property of a teaching tool.
//
// # The queues go with the rows, and so do the hubs
//
// A download queue holds bytes addressed to a subscriber, and the payments those
// bytes describe are about to be deleted. Both hosts are therefore rebuilt
// empty, and every reseeded bank is enrolled again as the seed admits it. A
// queue that survived would deliver a file about a payment no institution holds
// a row for, on the first day after the reset.
//
// A bank's payment hub goes the same way and needs no line of its own: the hub
// is state on the Bank, and every Bank is discarded above. An instruction taken
// and not yet cut off would otherwise be uploaded, after the reset, about a
// payment nobody holds.
//
// The seed's own timeline is replayed from BaseDate, which is seed.Populate's
// act rather than this one's: a reset restores the dataset, and a dataset dated
// from wherever the operator had advanced to would not be the fixed reference
// the tests and the UI describe.
func (d *Deployment) Reset(ctx context.Context) error {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	d.mu.Lock()
	d.banks = map[iso20022.BIC]*Bank{}
	d.mu.Unlock()

	d.csm.host = ebics.NewServer()
	d.cb.host = ebics.NewServer()
	d.cb.host.Enrol(ebics.SubscriberID(d.cfg.ClearingHouseBIC))
	d.csm.held = map[payment.PaymentID]heldReturn{}
	d.journal.take()

	if err := d.nets.Stores().Reset(ctx); err != nil {
		return err
	}
	// Last, and it is what gives the reseeded banks their queues: each one is
	// admitted through this deployment, so nothing has to re-enrol them
	// afterwards.
	return d.populate(ctx, d.nets, d)
}
