package main

import (
	"context"
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

// Problem is a file an institution could not process, and it is what replaces
// the dead letter.
//
// Under a transport that pushed, a handler with no caller had nowhere to put a
// failure and the bus accounted for it. Under pull, the institution that cannot
// process a file is running its own business day and this is where it puts it:
// against the order id the file arrived under, at the institution that could
// not get through it.
//
// OrderID is empty where the failure was not about one file — a download that
// could not be made at all, say — because there is no order to name.
type Problem struct {
	Institution iso20022.BIC
	OrderID     ebics.OrderID
	Detail      string
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
// It is strictly more than the transport it replaces could say. A drain hands
// back dead letters joined into a string; this is a value the operator console
// renders, the suite asserts against and the seed checks — and it is what makes
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
// The day itself
// ---------------------------------------------------------------------------

// AdvanceDay runs one business day and moves the clock to the next.
//
// # The order of the day IS this function
//
// Four flows are spread across three institutions, and the only place their
// order was ever legible was a package doc narrating them. Here the schedule is
// the code, in one list, which is also why moving a phase is a small change
// rather than a redesign.
//
// It does not interleave: each phase completes for every institution before the
// next begins. Real bulk clearing is exactly this batched, and a system that
// interleaved would be inventing concurrency it does not have. Nothing here runs
// on a goroutine of its own, and there is no goroutine anywhere below it.
//
// # What runs on a day nothing settles
//
// A weekend or a TARGET holiday advances the date and runs each bank's end of
// day — interest accrues over a weekend, which is the entire reason day-count
// conventions exist — and nothing else. No cut-off, no clearing, no settlement.
// The button still advances on such a day: nothing clears, the report says why,
// and that is the lesson rather than a state to be prevented.
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
// # A phase never stops the day
//
// Every phase collects problems and carries on. A file one bank cannot read
// must not stop another bank being paid, and an institution that abandoned the
// day halfway would leave the clock where it was with half the queues drained —
// a state no operator could reason about and no reset short of a rebuild could
// leave. What a failure costs is a line in the report.
func (d *Deployment) AdvanceDay(ctx context.Context) (DayReport, error) {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	ran := d.BusinessDate()
	if ran.SettlementDay {
		d.clear(ctx)
	}
	// Every bank's own end of day, on every date. It is the bank's act and not
	// the scheme's, so it is the bank INDEX that is iterated here and not the
	// subscriber list: a bank founded and never admitted still accrues interest
	// on its customers' overdrafts.
	for _, b := range d.banksInOrder() {
		if err := b.runEndOfDay(ctx, ran.Date); err != nil {
			d.journal.problem(Problem{Institution: b.bic, Detail: err.Error()})
		}
	}
	// And the cycles the next clearing day will accumulate into. On a day that
	// cleared, the cut-off above left none open; on a day that did not, the ones
	// standing are still open and this is a no-op.
	d.journal.problem(d.csm.openCycles(ctx)...)

	next, err := d.clock.Advance()
	if err != nil {
		err = fmt.Errorf("server: the business day ran on %s and the clock could not be moved past it: %w",
			ran.Date.Format(time.DateOnly), err)
		next = d.clock.Now()
	}

	files, outcomes, problems := d.journal.take()
	return DayReport{
		Ran:      ran,
		Next:     businessDateOf(next),
		Files:    files,
		Outcomes: outcomes,
		Problems: problems,
	}, err
}

// clear is the clearing half of a business day: the nine phases that only run
// on a day the scheme settles on.
//
// # Which banks each phase visits
//
// The phases that touch a QUEUE visit the subscribers — the banks the clearing
// house's roster names, which are the banks enrolment gave a queue. A bank
// without one is not a slow bank: nothing can be addressed to it, so every
// clearing phase would answer it EBICS_INVALID_USER_OR_USER_STATE, which is
// true, is not news, and would appear in every day's report for the life of the
// deployment.
//
// The directory refresh is the exception and visits every bank, because it
// touches no queue at all: it is the scheme's vendor delivering a file, and a
// bank waiting on admission is exactly the bank that needs the routing
// directory it is about to appear in.
func (d *Deployment) clear(ctx context.Context) {
	csm, cb := d.cfg.ClearingHouseBIC, d.cfg.CentralBankBIC

	// 0. Every member takes the published roster. It is the FIRST thing, so a
	// bank admitted since the last advance can be addressed by its neighbours
	// today rather than after somebody remembers to call the route.
	for _, b := range d.banksInOrder() {
		if _, err := b.RefreshDirectory(ctx); err != nil {
			d.journal.problem(Problem{Institution: b.bic, Detail: err.Error()})
		}
	}

	// 1. The clearing house works through the instructions and returns its
	// members uploaded: each is recorded, and routed to the agent it names.
	d.journal.problem(d.csm.work(ctx, ebics.CCT, ebics.CDD, ebics.CRT)...)

	// 2. Each receiving bank collects what was routed to it and answers per
	// transaction — ACCP or RJCT — in a status report it uploads.
	for _, b := range d.subscribers() {
		d.journal.problem(b.collect(ctx, csm, b.csm, ebics.BTD)...)
	}

	// 3. The clearing house works through those answers, taking each accepted
	// payment into the open cycle for its scheme.
	d.journal.problem(d.csm.work(ctx, ebics.CST)...)

	// 4. The cut-off: every open cycle is netted and its positions uploaded to
	// the settlement agent. A cycle the operator closed by hand is already
	// closed and is settled by this day's phase 5 rather than by this one.
	d.journal.problem(d.csm.closeOpenCycles(ctx)...)

	// 5. The settlement agent works through everything uploaded to it: cut-offs
	// discharged, returns executed, lodgements credited. This is the only phase
	// in which central-bank reserves move.
	d.journal.problem(d.cb.work(ctx)...)

	// 6. The clearing house collects the answers and turns each settled CYCLE
	// into per-payment news, and each settled RETURN into the pacs.004 the other
	// bank has been waiting for.
	d.journal.problem(d.csm.collect(ctx)...)

	// 7. Each member collects, THE SETTLEMENT AGENT FIRST. The order is
	// load-bearing and is the only thing that guarantees the mirror leg is
	// booked before the creditor legs draw on it — see CentralBank.advise, which
	// argues it where the guarantee used to live.
	for _, b := range d.subscribers() {
		d.journal.problem(b.collect(ctx, cb, b.cb, ebics.C53)...)
		d.journal.problem(b.collect(ctx, cb, b.cb, ebics.BTD)...)
		d.journal.problem(b.collect(ctx, csm, b.csm, ebics.BTD)...)
	}
}
