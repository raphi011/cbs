package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// A BusinessDate is the day this deployment stands on and whether anything
// clears on it.
//
// The date is the DEPLOYMENT's and is in none of the N+2 databases. A bank's
// database holding one would be a bank's opinion about what day it is, and the
// schema comments are explicit that what is absent from each shape is the
// substance.
//
// Holiday names the closure when there is one and is empty on a weekend: TARGET
// is shut on a Saturday for no reason that has a name, and a screen that said
// "closed, " with nothing after it would be worse than one that says only that
// it is closed.
type BusinessDate struct {
	Date          time.Time
	SettlementDay bool
	Holiday       calendar.Holiday
}

// businessDateOf reads the calendar's two questions about one instant.
func businessDateOf(t time.Time) BusinessDate {
	holiday, _ := calendar.HolidayOn(t)
	return BusinessDate{
		Date:          t,
		SettlementDay: calendar.IsSettlementDay(t),
		Holiday:       holiday,
	}
}

// BusinessDate is what day this deployment is on. It takes no lock: the clock
// is safe for concurrent use, and a reader that took the day's lock would block
// behind an advance rather than answering what the day was when it asked.
func (d *Deployment) BusinessDate() BusinessDate { return businessDateOf(d.clock.Now()) }

// ---------------------------------------------------------------------------
// The journal, and the report it becomes
// ---------------------------------------------------------------------------

// FileMoved is one file that left one institution for another: put on a
// connection, or put in a subscriber's download queue.
//
// The two are one record because a reader wants the same thing from both — who
// sent what to whom, under which order id — and because which of the two it was
// follows from the institutions: nothing is ever pushed at a member bank, so a
// file addressed to one was queued and a file addressed to a host was uploaded.
type FileMoved struct {
	From      iso20022.BIC
	To        iso20022.BIC
	OrderType ebics.OrderType
	OrderID   ebics.OrderID
}

// TransactionOutcome is one institution's decision about one payment: what it
// said, and what it said it in.
//
// DecidedBy is the institution that MADE the decision and not the one that
// carried it, which is the same distinction a pacs.002's Orgtr draws. Three
// institutions appear here for one payment on the ordinary path — the receiving
// bank accepts it, the clearing house clears it, and the clearing house reports
// it settled — and reading them in order is reading the payment's life.
type TransactionOutcome struct {
	DecidedBy iso20022.BIC
	Payment   payment.PaymentID
	Status    iso20022.TransactionStatus
	Code      iso20022.StatusReason
	Text      string
}

// Problem is a file an institution could not process.
//
// An institution that cannot process a file it downloaded has nobody to return
// the error to — the uploader was told EBICS_OK and went away. What it has
// instead is its own business day, which is running, and this is where the
// failure goes: against the order id the file arrived under, at the institution
// that could not get through it.
//
// OrderID is empty where the failure was not about one file — a download that
// could not be made at all, say — because there is no order to name.
type Problem struct {
	Institution iso20022.BIC
	OrderID     ebics.OrderID
	Detail      string
}

// joinProblemDetails renders a set of problems as one error, for the doors that
// reach a phase of the day out of turn and have a caller to tell.
//
// A day PUTS problems in its report because there is nobody to return them to; a
// request has somebody, so the same values become the answer to it. The
// institution is not repeated in the text — the caller is standing on that
// institution's own port.
func joinProblemDetails(ps []Problem) error {
	var errs []error
	for _, p := range ps {
		errs = append(errs, errors.New(p.Detail))
	}
	return errors.Join(errs...)
}

// journal accumulates what this deployment has done since the last advance.
//
// It is TAKEN at the end of a day rather than started at the beginning, and the
// difference is what a customer's submission between two advances is reported
// on. A file uploaded at four o'clock is carried by the day that runs at five,
// so the day that carries it is the day that reports it.
//
// The mutex is real work. A day runs on one goroutine, but the doors do not:
// every submission, cut-off and lodgement writes here from whichever HTTP
// goroutine it arrived on, and those run while no day is running.
type journal struct {
	mu       sync.Mutex
	files    []FileMoved
	outcomes []TransactionOutcome
	problems []Problem
}

func (j *journal) file(f FileMoved) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.files = append(j.files, f)
}

func (j *journal) outcome(o TransactionOutcome) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.outcomes = append(j.outcomes, o)
}

func (j *journal) problem(ps ...Problem) {
	if len(ps) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.problems = append(j.problems, ps...)
}

// take empties the journal and hands back everything in it.
//
// Emptying is the point: what the report carries is what happened SINCE the
// last one, so two consecutive advances do not report the same file twice.
func (j *journal) take() ([]FileMoved, []TransactionOutcome, []Problem) {
	j.mu.Lock()
	defer j.mu.Unlock()

	files, outcomes, problems := j.files, j.outcomes, j.problems
	j.files, j.outcomes, j.problems = nil, nil, nil
	return files, outcomes, problems
}

// A DayReport is what a business day did: the day it ran on, the day it left
// the clock on, and everything that moved in between.
//
// It is a VALUE rather than an error string, which is what lets the operator
// console render it, the suite assert against it and the seed check it — and it
// is what makes
// the day legible, because a learner can watch a payment move through the
// phases instead of observing that it has arrived.
type DayReport struct {
	// Ran is the day that was worked, and Next is where the clock stands after
	// it. Both carry their own calendar answers, because the day that just ran
	// and the day about to begin can differ in exactly the way that matters: a
	// Friday clears and leaves the clock on a Saturday that will not.
	Ran  BusinessDate
	Next BusinessDate

	Files    []FileMoved
	Outcomes []TransactionOutcome
	Problems []Problem
}

// toBusinessDateDTO and toDayReportDTO render the deployment's own values onto
// the wire.
//
// The mapping is here and not in api, which is the opposite of every other DTO
// in this system, and the reason is the direction of the dependency: api may not
// import a composition root, so a mapping written there would have nothing to
// map from. See CentralBank.AdvanceDay.
//
// The date is rendered day-granular. The clock carries a time of day — every
// instant written on one business date is the same one — and a browser handed it
// would render it in the reader's own zone, showing a day the deployment is not
// on. See api.BusinessDateDTO.
func toBusinessDateDTO(d BusinessDate) api.BusinessDateDTO {
	return api.BusinessDateDTO{
		Date:          d.Date.Format(time.DateOnly),
		SettlementDay: d.SettlementDay,
		Closure:       string(d.Holiday),
	}
}

func toDayReportDTO(r DayReport) api.DayReportDTO {
	// The three slices are made non-nil, so a quiet day is [] on the wire and
	// not null: a client that had to treat the two alike would be re-deriving
	// "nothing happened" from a JSON quirk.
	out := api.DayReportDTO{
		Ran:      toBusinessDateDTO(r.Ran),
		Next:     toBusinessDateDTO(r.Next),
		Files:    make([]api.FileMovedDTO, 0, len(r.Files)),
		Outcomes: make([]api.TransactionOutcomeDTO, 0, len(r.Outcomes)),
		Problems: make([]api.ProblemDTO, 0, len(r.Problems)),
	}
	for _, f := range r.Files {
		out.Files = append(out.Files, api.FileMovedDTO{
			From: string(f.From), To: string(f.To),
			OrderType: string(f.OrderType), OrderID: string(f.OrderID),
		})
	}
	for _, o := range r.Outcomes {
		out.Outcomes = append(out.Outcomes, api.TransactionOutcomeDTO{
			DecidedBy: string(o.DecidedBy), Payment: string(o.Payment),
			Status: string(o.Status), Code: string(o.Code), Text: o.Text,
		})
	}
	for _, p := range r.Problems {
		out.Problems = append(out.Problems, api.ProblemDTO{
			Institution: string(p.Institution), OrderID: string(p.OrderID), Detail: p.Detail,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// The phases, and the sequences built from them
// ---------------------------------------------------------------------------

// A phaseID names one step of a business day. The constants are the identity a
// sequence selects by; the ORDER phases run in is the order they are declared
// in beforeClock and afterClock, never the order of these constants and never
// their numeric value.
type phaseID int

const (
	phaseRefresh phaseID = iota
	phaseBankCutoff
	phaseClearing
	phaseClearingHouseCutoff
	phaseDischarge
	phaseSettlement
	phaseRelease
	phaseCollection
	phaseEndOfDay
	phaseOpenCycles

	// phaseCollectClearingHouse is the collection NARROWED to the one queue that
	// holds anything before a cycle settles. It is not a phase of a day — a day
	// runs phaseCollection, which is the whole of it — and it exists because
	// carrying to clearing needs that part alone. See carryToClearingPhases.
	phaseCollectClearingHouse
)

// A phase is one named step of a business day.
//
// It RETURNS what it could not do rather than journalling it. The runner is the
// only thing that writes a problem to the journal, which is what makes forgetting
// impossible: a phase has no journal to reach and no way to drop what it hands
// back. Whether those problems are journalled or returned to a caller is the
// runner's decision — see AdvanceDay, which journals, and CarryToClearing, which
// answers its caller.
type phase struct {
	id   phaseID
	name string
	// settlementOnly marks a phase that runs only on a day the scheme settles
	// on. It is read by AdvanceDay alone: a derived sequence names the phases it
	// wants outright, its caller having already decided that it wants them.
	settlementOnly bool
	run            func(ctx context.Context, d *Deployment) []Problem
}

// beforeClock is the business day, in the order it runs, up to the clock move.
//
// # This list IS the order of the day
//
// Four flows are spread across three institutions, and this is the one place
// their order is stated. Every other sequence in this package and its tests is
// DERIVED from this list by naming phases, so a subset cannot hold a phase the
// day does not have and cannot run two phases in an order the day does not.
//
// It does not interleave: each phase completes for every institution before the
// next begins. Real bulk clearing is exactly this batched, and a system that
// interleaved would be inventing concurrency it does not have. Nothing here runs
// on a goroutine of its own, and there is no goroutine anywhere below it.
//
// # Which banks each phase visits
//
// The phases that touch a QUEUE visit the subscribers — the banks the clearing
// house's roster names, which are the banks enrolment gave a queue. A bank
// without one is not a slow bank: nothing can be addressed to it, so every
// clearing phase would answer it EBICS_INVALID_USER_OR_USER_STATE, which is
// true, is not news, and would appear in every day's report for the life of the
// deployment. Its own hub therefore accumulates and is never cut off, which is
// the honest outcome — a bank the scheme has not admitted has nowhere to upload
// to.
//
// The refresh and the end of day are the exceptions and visit every bank. The
// refresh touches no queue at all: it is the scheme's vendor delivering a file,
// and a bank waiting on admission is exactly the bank that needs the routing
// directory it is about to appear in. The end of day is the bank's own act and
// not the scheme's: a bank founded and never admitted still accrues interest on
// its customers' overdrafts.
//
// # A phase never stops the day
//
// Every phase collects problems and carries on. A file one bank cannot read must
// not stop another bank being paid, and an institution that abandoned the day
// halfway would leave the clock where it was with half the queues drained — a
// state no operator could reason about and no reset short of a rebuild could
// leave. What a failure costs is a line in the report.
var beforeClock = []phase{
	{
		id: phaseRefresh, name: "refresh", settlementOnly: true,
		// Every member takes the published roster. It is the FIRST thing, so a
		// bank admitted since the last advance can be addressed by its
		// neighbours today rather than after somebody remembers to call the
		// route.
		run: func(ctx context.Context, d *Deployment) []Problem {
			var ps []Problem
			for _, b := range d.banksInOrder() {
				if _, err := b.RefreshDirectory(ctx); err != nil {
					ps = append(ps, Problem{Institution: b.bic, Detail: err.Error()})
				}
			}
			return ps
		},
	},
	{
		id: phaseBankCutoff, name: "bank cut-off", settlementOnly: true,
		// Every member reaches its own cut-off: the morning's instructions leave
		// each bank's hub as one file per scheme, uploaded to the clearing
		// house. Before the clearing house works, because a file uploaded after
		// it had worked would wait a whole day for the next one — and this is
		// where the day's payments come from, so it is the first thing that
		// carries anything.
		run: func(ctx context.Context, d *Deployment) []Problem {
			var ps []Problem
			for _, b := range d.subscribers() {
				_, problems := b.cutoff(ctx)
				ps = append(ps, problems...)
			}
			return ps
		},
	},
	{
		id: phaseClearing, name: "clearing", settlementOnly: true,
		// The clearing house works through what its members uploaded: each file
		// is recorded, each transaction validated and taken into the open cycle
		// for its scheme, each submitting bank answered per transaction — and
		// each receiving bank's share of the file BUILT AND HELD. Nothing is
		// queued for a receiving bank here; see the release.
		run: func(ctx context.Context, d *Deployment) []Problem { return d.csm.work(ctx) },
	},
	{
		id: phaseClearingHouseCutoff, name: "clearing-house cut-off", settlementOnly: true,
		// The clearing house's own cut-off: every open cycle is netted and its
		// positions uploaded to the settlement agent. Two different cut-offs
		// share the word and they are two phases apart — a bank's turns
		// instructions into a file, and this one turns a batch of validated
		// payments into net positions. A cycle the operator closed by hand is
		// already closed and is settled by this day's settlement rather than by
		// this one.
		run: func(ctx context.Context, d *Deployment) []Problem { return d.csm.closeOpenCycles(ctx) },
	},
	{
		id: phaseDischarge, name: "discharge", settlementOnly: true,
		// And the cut-offs that instructed nothing are discharged where they
		// stand. A cycle whose members' positions all cancel — or that took
		// nothing in at all — has no leg to send, so no answer is coming and the
		// release would never release it. Before the settlement rather than
		// after, because the settlement agent has nothing to do with it. See
		// ClearingHouse.settleUninstructed.
		run: func(ctx context.Context, d *Deployment) []Problem { return d.csm.settleUninstructed(ctx) },
	},
	{
		id: phaseSettlement, name: "settlement", settlementOnly: true,
		// The settlement agent works through everything uploaded to it: cut-offs
		// discharged, returns executed, lodgements credited. This is the only
		// phase in which central-bank reserves move.
		run: func(ctx context.Context, d *Deployment) []Problem { return d.cb.work(ctx) },
	},
	{
		id: phaseRelease, name: "release", settlementOnly: true,
		// The clearing house collects the answers and RELEASES: each settled
		// cycle becomes the output files its receiving banks have been waiting
		// for and an ACSC apiece for the banks that submitted, and each settled
		// RETURN becomes the pacs.004 the other bank has been waiting for.
		//
		// THE ORDER OF THE CLEARING-HOUSE CUT-OFF, THE SETTLEMENT AND THIS IS
		// THE DESIGN. The cycle settles before the output files are released, so
		// a receiving bank is handed its instructions only once the funds behind
		// them are final and never credits a customer against money that has not
		// settled. Reverse them and settlement risk is invented — and a bank
		// that could not apply a payment would be refusing one nobody had paid
		// for yet, which is a different and much easier system.
		run: func(ctx context.Context, d *Deployment) []Problem { return d.csm.collect(ctx) },
	},
	{
		id: phaseCollection, name: "collection", settlementOnly: true,
		// Each member collects, THE SETTLEMENT AGENT FIRST. The order is
		// load-bearing and is the only thing that guarantees the mirror leg is
		// booked before the creditor legs draw on it — see CentralBank.advise,
		// which argues it where the guarantee used to live.
		//
		// # Why one phase and not three
		//
		// A bank takes all three queues before the next bank takes any, and that
		// nesting is observable: it is the order files move in, and the chain
		// tests read it. Three phases would collect one queue across every bank
		// before starting the next, which reverses it. The guarantee above is
		// satisfied either way — it is about ONE bank's mirror against ONE
		// bank's creditor legs — so the nesting is not correctness, and changing
		// it is a domain decision rather than a consequence of naming phases.
		run: func(ctx context.Context, d *Deployment) []Problem {
			var ps []Problem
			for _, b := range d.subscribers() {
				ps = append(ps, b.collect(ctx, d.cfg.CentralBankBIC, b.cb, ebics.C53)...)
				ps = append(ps, b.collect(ctx, d.cfg.CentralBankBIC, b.cb, ebics.BTD)...)
				ps = append(ps, b.collect(ctx, d.cfg.ClearingHouseBIC, b.csm, ebics.BTD)...)
			}
			return ps
		},
	},
	{
		id: phaseEndOfDay, name: "end of day",
		// Every bank's own end of day, on EVERY date and not only a settlement
		// day: interest accrues over a weekend, which is the entire reason
		// day-count conventions exist. It is the one phase before the clock that
		// a weekend still runs, and the flag is what says so.
		//
		// The date is read from the clock rather than passed, and it is the day
		// being closed: this runs before the pivot, AdvanceDay holds resetMu
		// across both, and nothing else moves the clock.
		run: func(ctx context.Context, d *Deployment) []Problem {
			var ps []Problem
			for _, b := range d.banksInOrder() {
				if err := b.runEndOfDay(ctx, d.clock.Now()); err != nil {
					ps = append(ps, Problem{Institution: b.bic, Detail: err.Error()})
				}
			}
			return ps
		},
	},
}

// afterClock is what runs once the date has moved.
//
// A cycle is stamped with the instant it is opened and the day it accepts
// payments on is the one it should name, so opening one belongs on the far side
// of the pivot. Being in this list rather than the other IS that argument: there
// is nowhere else to put it.
var afterClock = []phase{
	{
		id: phaseOpenCycles, name: "open cycles",
		// On a day that cleared, the cut-off left none open; on a day that did
		// not, the ones standing are still open and this is a no-op. A clock
		// that could not be moved leaves them on the day that just ran, which is
		// still the day they will accumulate on.
		run: func(ctx context.Context, d *Deployment) []Problem { return d.csm.openCycles(ctx) },
	},
}

// collectClearingHouseOnly is the day's collection with the two settlement-agent
// queues left out, and it is the one place a sequence holds a phase the day does
// not declare.
//
// Nothing has settled when it runs, so those two queues are empty and the third
// holds the per-transaction answers. Taking the whole collection instead would
// be correct and would say something false: that a carry reaches the settlement
// agent at all.
var collectClearingHouseOnly = phase{
	id: phaseCollectClearingHouse, name: "collection · clearing house BTD", settlementOnly: true,
	run: func(ctx context.Context, d *Deployment) []Problem {
		var ps []Problem
		for _, b := range d.subscribers() {
			ps = append(ps, b.collect(ctx, d.cfg.ClearingHouseBIC, b.csm, ebics.BTD)...)
		}
		return ps
	},
}

// only selects phases by name, in the order the day declares them.
//
// It is how every sequence other than a full day is built, and the reason none
// of them can drift: a caller says WHICH phases it wants and never in what
// order, so a subset runs them in the day's own relative order or not at all.
// An id that names no phase in the list is a programming error and panics —
// every caller is a package-level variable, so it panics at init or never.
func only(list []phase, ids ...phaseID) []phase {
	want := make(map[phaseID]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]phase, 0, len(ids))
	for _, p := range list {
		if want[p.id] {
			out = append(out, p)
			delete(want, p.id)
		}
	}
	if len(want) != 0 {
		panic(fmt.Sprintf("server: %d phase(s) selected that the day does not declare", len(want)))
	}
	return out
}

// onSettlementDay drops the phases a day nothing settles on does not run.
//
// Only a full day asks. A derived sequence names its phases outright, its caller
// having already decided that it wants them — the seed carries files to clearing
// on whatever date it is building on.
func onSettlementDay(list []phase, settles bool) []phase {
	if settles {
		return list
	}
	out := make([]phase, 0, len(list))
	for _, p := range list {
		if !p.settlementOnly {
			out = append(out, p)
		}
	}
	return out
}

// runPhases runs a sequence and hands back everything it could not do.
//
// It is the only caller of phase.run, and the only place a phase's problems are
// collected — which is what makes a phase unable to lose them.
func runPhases(ctx context.Context, d *Deployment, list []phase) []Problem {
	var ps []Problem
	for _, p := range list {
		ps = append(ps, p.run(ctx, d)...)
	}
	return ps
}

// carryToClearingPhases is the file-moving prefix of a day: every member's
// cut-off, the clearing house's work over what they uploaded, and each member
// collecting the answers from it.
//
// The collection is the day's, taken out of turn and narrowed: nothing has
// settled, so that queue holds the per-transaction answers and nothing else —
// and a submitting bank that did not collect them would sit at Initiated where a
// carried payment reaches Accepted, which is the divergence a sample dataset
// must not have. It is appended rather than selected because it is the one step
// here that is not a phase of a day; see collectClearingHouseOnly.
var carryToClearingPhases = append(
	only(beforeClock, phaseBankCutoff, phaseClearing),
	collectClearingHouseOnly,
)

// ---------------------------------------------------------------------------
// The day itself
// ---------------------------------------------------------------------------

// AdvanceDay runs one business day and moves the clock to the next.
//
// The order it runs in is beforeClock's, then the clock, then afterClock's. This
// function chooses nothing about that order; it decides only what a day does
// with what the phases hand back, which is to journal it.
//
// # What runs on a day nothing settles
//
// A weekend or a TARGET holiday advances the date and runs each bank's end of
// day — interest accrues over a weekend, which is the entire reason day-count
// conventions exist — and nothing else. No cut-off, no clearing, no settlement.
// Which phases those are is not decided here either: it is settlementOnly on the
// phase, so the answer is readable off the day's own declaration. The button
// still advances on such a day: nothing clears, the report says why, and that is
// the lesson rather than a state to be prevented.
//
// # It takes the lock Reset takes
//
// Advancing and resetting are the two acts over the whole deployment rather than
// over one institution, and neither may run while the other does: a day working
// through the queues while the store is being emptied underneath it would post
// into tables the reset has already truncated.
//
// # The clock moves ONE CALENDAR DAY
//
// Not to the next settlement day. Skipping the weekend would mean a deployment
// that never has a Saturday, so a payment submitted on a Friday evening would
// appear to clear the same night and the whole point of a business calendar
// would be invisible.
//
// The clock is the PIVOT and not a phase. It hands back the next date, which the
// report carries, and an error the caller is told about rather than a problem a
// report lists — a day whose clock did not move is not a day with a bad line in
// it.
func (d *Deployment) AdvanceDay(ctx context.Context) (DayReport, error) {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	ran := d.BusinessDate()
	d.journal.problem(runPhases(ctx, d, onSettlementDay(beforeClock, ran.SettlementDay))...)

	next, err := d.clock.Advance()
	if err != nil {
		err = fmt.Errorf("server: the business day ran on %s and the clock could not be moved past it: %w",
			ran.Date.Format(time.DateOnly), err)
		next = d.clock.Now()
	}

	d.journal.problem(runPhases(ctx, d, onSettlementDay(afterClock, ran.SettlementDay))...)

	files, outcomes, problems := d.journal.take()
	return DayReport{
		Ran:      ran,
		Next:     businessDateOf(next),
		Files:    files,
		Outcomes: outcomes,
		Problems: problems,
	}, err
}

// CarryToClearing moves the morning's files and stops before anything settles:
// every member's cut-off, the clearing house's work over what they uploaded, and
// each member collecting the answers.
//
// # Who asks for it, and why a row would not do
//
// The seed, and nothing else in this system. A dataset that wants payments IN
// FLIGHT has to put them there the way the running system does, because a
// payment handed to the clearing house as a row leaves no SHARE behind it — and
// a share is what release hands to the bank that has to pay the payee. Composing
// the institutions' halves directly, which is how the seed builds everything it
// settles, cannot produce one; the file is what builds it. See
// ClearingHouse.takeRecorded, and ClearingHouse.unhandable for what a cycle
// without one costs.
//
// # Where it stops
//
// It stops before the clearing house's own cut-off, and the reason is the reason
// that cut-off, the settlement and the release are in that order: settling is
// what moves reserves, and a fixed dataset must not depend on when somebody
// advanced a clock. What it leaves behind is the state every bank in a bulk
// scheme is really in between its own cut-off and the next settlement. Which
// phases those are is carryToClearingPhases, and it is DERIVED from the day
// rather than written out, so this cannot come to run them in another order.
//
// # The journal is emptied rather than kept
//
// The phases hand their problems back rather than journalling them, so what is
// drained here is the FILES AND OUTCOMES the institutions record from inside
// those phases. Every other act the seed performs writes nothing to it: the
// composites go straight at each institution's own network. A first day's report
// carrying six uploads and nothing else would describe the build's last minute
// and none of the rest of it, dated on the day an operator happened to press the
// button. So what happened here is dropped, and what could not happen is
// returned — the scenario is hardcoded, so a problem in it is a build failure
// rather than a line in a report.
//
// It takes no lock. Reset holds resetMu across the rebuild this runs inside, and
// the only other caller is a boot with nothing serving yet.
func (d *Deployment) CarryToClearing(ctx context.Context) error {
	problems := runPhases(ctx, d, carryToClearingPhases)
	d.journal.take()
	return joinProblemDetails(problems)
}
