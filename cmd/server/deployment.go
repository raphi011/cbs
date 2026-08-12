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
type Config struct {
	CentralBankBIC   iso20022.BIC
	ClearingHouseBIC iso20022.BIC

	CentralBankURL   string
	ClearingHouseURL string
}

// validate refuses a configuration no file could be routed under.
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

// Hosts is where the two institutions that are DIALLED keep their transport
// state: one download queue per enrolled subscriber, and the log of the orders
// each has uploaded.
type Hosts interface {
	ClearingHouseEBICS() ebics.Store
	CentralBankEBICS() ebics.Store
}

// A Deployment is every institution this process holds, the clock they all
// read, and the business day that drives them.
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

	// The two hosts, built once.
	csm *ClearingHouse
	cb  *CentralBank

	// seq numbers the messages this deployment emits. See nextMsgID.
	seq atomic.Uint64

	// journal accumulates what this deployment has done since the last advance:
	// every file moved, every decision made about a transaction, and every file an
	// institution could not process.
	journal journal

	// populate rebuilds the sample dataset. It must be idempotent: the process
	// calls it at boot and Reset calls it again after clearing the store.
	populate func(context.Context, *payment.Networks, seed.Deployment) error

	// mu guards banks. The two hosts are written once, in NewDeployment, and are
	// never replaced.
	mu sync.Mutex
	// banks is every member bank this process holds a database for, by ADDRESS. A
	// bank's ParticipantID is its BIC (payment.AsBank), so this is keyed by the
	// one identity a bank has.
	banks map[iso20022.BIC]*Bank

	// resetMu serializes Reset AND AdvanceDay.
	resetMu sync.Mutex
}

// NewDeployment builds every institution this process holds: the clearing
// house, the settlement agent, and one member bank per database the store set
// names.
func NewDeployment(ctx context.Context, nets *payment.Networks, hosts Hosts, clock *calendar.Clock, cfg Config,
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
		bic: cfg.CentralBankBIC, host: ebics.NewServer(hosts.CentralBankEBICS()),
	}
	d.csm = &ClearingHouse{
		d: d, net: nets.ClearingHouse(), ops: nets.ClearingHouse(),
		bic:  cfg.ClearingHouseBIC,
		host: ebics.NewServer(hosts.ClearingHouseEBICS()),
		cb:   ebics.NewClient(ebics.SubscriberID(cfg.ClearingHouseBIC), cfg.CentralBankURL),
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

// mintBank opens one bank's database and gives it its two connections — or
// hands back the Bank this deployment already holds for that address.
func (d *Deployment) mintBank(ctx context.Context, bic iso20022.BIC) (*Bank, error) {
	d.mu.Lock()
	held, ok := d.banks[bic]
	d.mu.Unlock()
	if ok {
		return held, nil
	}

	net, err := d.nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return nil, fmt.Errorf("server: opening %s's store: %w", bic, err)
	}
	b := &Bank{
		d: d, net: net, ops: net, bic: bic,
		csm: ebics.NewClient(ebics.SubscriberID(bic), d.cfg.ClearingHouseURL),
		cb:  ebics.NewClient(ebics.SubscriberID(bic), d.cfg.CentralBankURL),
	}
	// Under the lock a second time, because opening the database above is I/O and
	// two requests for a bank neither of them found may have run it at once. The
	// loser's Bank is discarded, so the invariant above holds however they raced.
	d.mu.Lock()
	defer d.mu.Unlock()
	if held, ok := d.banks[bic]; ok {
		return held, nil
	}
	d.banks[bic] = b
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
func (d *Deployment) CentralBankBIC() iso20022.BIC { return d.cfg.CentralBankBIC }

// Bank is one member bank's own view, over that bank's own database.
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

// now is the instant every header this process writes is stamped from, and it
// is the deployment's business date.
func (d *Deployment) now() time.Time { return d.clock.Now() }

// nextMsgID mints the identifier a message travels under: the sender's BIC and
// a number nobody else in this deployment will use.
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

// Submit runs the submitting bank's half synchronously and hands the
// instruction to that bank's hub.
func (d *Deployment) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	scheme, ok := d.csm.ops.Scheme(req.Scheme)
	if !ok {
		return payment.Payment{}, fmt.Errorf("server: no scheme %q, so no bank submits it: %w", req.Scheme, payment.ErrSchemeNotFound)
	}
	// A payment that never leaves one bank is not a payment this system carries.
	if req.DebtorDetails.Agent != "" && req.DebtorDetails.Agent == req.CreditorDetails.Agent {
		return payment.Payment{}, fmt.Errorf("server: %s is both the payer's bank and the payee's for this instruction: %w",
			req.DebtorDetails.Agent, payment.ErrOnUsPayment)
	}
	// And a payment one of whose banks the scheme has not admitted.
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
	// On-us, asked by ADDRESS, and this is the arm that fires for an instruction a
	// customer actually hands in.
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
func (d *Deployment) Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	// The routing question, and only that: which bank's instruction is this?
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
func (d *Deployment) member(bic iso20022.BIC) (*Bank, error) {
	d.mu.Lock()
	b, ok := d.banks[bic]
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("server: this deployment holds no bank at %s", bic)
	}
	return b, nil
}

// Reset discards all persisted state, rewinds the business date and rebuilds
// the sample dataset.
func (d *Deployment) Reset(ctx context.Context) error {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	for _, b := range d.banksInOrder() {
		b.clearHub()
	}
	d.csm.host.Reset()
	d.cb.host.Reset()
	d.cb.host.Enrol(ebics.SubscriberID(d.cfg.ClearingHouseBIC))
	d.journal.take()

	if err := d.nets.Stores().Reset(ctx); err != nil {
		return err
	}
	// Last, and it is what gives the reseeded banks their queues: each one is
	// admitted through this deployment, so nothing has to re-enrol them
	// afterwards.
	return d.populate(ctx, d.nets, d)
}
