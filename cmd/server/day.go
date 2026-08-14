package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/node"
)

// A BusinessDate is the day this deployment stands on and whether anything
// clears on it.
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

// journal accumulates what this deployment has done since the last advance. It
// is the node.Journal every institution is handed, and the three slices are
// what a day report is made of.
type journal struct {
	mu       sync.Mutex
	files    []node.FileMoved
	outcomes []node.TransactionOutcome
	problems []node.Problem

	// watch is a second SUBSCRIBER, told as each event is recorded. It is not a
	// second consumer: take empties the journal below and this does not, so a
	// report and a broadcast never take events from each other.
	watch func(api.StreamEvent)
}

// tell is called under the lock, so a watcher is told in the order the journal
// was.
func (j *journal) tell(name string, data any) {
	if j.watch != nil {
		j.watch(api.StreamEvent{Name: name, Data: data})
	}
}

func (j *journal) File(f node.FileMoved) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.files = append(j.files, f)
	j.tell(api.EventFile, toFileMovedDTO(f))
}

func (j *journal) Outcome(o node.TransactionOutcome) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.outcomes = append(j.outcomes, o)
	j.tell(api.EventOutcome, toTransactionOutcomeDTO(o))
}

func (j *journal) problem(ps ...node.Problem) {
	if len(ps) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.problems = append(j.problems, ps...)
	for _, p := range ps {
		j.tell(api.EventProblem, toProblemDTO(p))
	}
}

// phase is told and never accumulated: a report already names the phase it is
// about, and what the phase moved is in the three slices. It is pushed because
// end of day moves no file, so nothing else would say the day had got past it.
func (j *journal) phase(p api.PhaseDTO) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.tell(api.EventPhase, p)
}

// take empties the journal and hands back everything in it. Only an act whose
// report REACHES a caller may take it: one that returns an error reports
// nothing, so it leaves what it moved for the report that comes next.
func (j *journal) take() ([]node.FileMoved, []node.TransactionOutcome, []node.Problem) {
	j.mu.Lock()
	defer j.mu.Unlock()

	files, outcomes, problems := j.files, j.outcomes, j.problems
	j.files, j.outcomes, j.problems = nil, nil, nil
	return files, outcomes, problems
}

// A DayReport is what a business day did: the day it ran on, the day it left
// the clock on, and everything that moved in between.
type DayReport struct {
	// Ran is the day that was worked, and Next is where the clock stands after it.
	Ran  BusinessDate
	Next BusinessDate

	// Phases is what THIS call ran, in order, which is not the whole day when
	// some of it had already been stepped.
	Phases []phase

	Files    []node.FileMoved
	Outcomes []node.TransactionOutcome
	Problems []node.Problem
}

// The deployment's own values, rendered onto the wire.
func toBusinessDateDTO(d BusinessDate) api.BusinessDateDTO {
	return api.BusinessDateDTO{
		Date:          d.Date.Format(time.DateOnly),
		SettlementDay: d.SettlementDay,
		Closure:       string(d.Holiday),
	}
}

func toDayReportDTO(r DayReport) api.DayReportDTO {
	return api.DayReportDTO{
		Ran:          toBusinessDateDTO(r.Ran),
		Next:         toBusinessDateDTO(r.Next),
		Phases:       phaseKeys(r.Phases),
		MovementsDTO: toMovementsDTO(r.Files, r.Outcomes, r.Problems),
	}
}

// toMovementsDTO renders what a run moved, whether that run was a whole day or
// one phase of one.
func toMovementsDTO(files []node.FileMoved, outcomes []node.TransactionOutcome,
	problems []node.Problem) api.MovementsDTO {

	// The three slices are made non-nil, so a quiet day is [] on the wire and
	// not null: a client that had to treat the two alike would be re-deriving
	// "nothing happened" from a JSON quirk.
	out := api.MovementsDTO{
		Files:    make([]api.FileMovedDTO, 0, len(files)),
		Outcomes: make([]api.TransactionOutcomeDTO, 0, len(outcomes)),
		Problems: make([]api.ProblemDTO, 0, len(problems)),
	}
	for _, f := range files {
		out.Files = append(out.Files, toFileMovedDTO(f))
	}
	for _, o := range outcomes {
		out.Outcomes = append(out.Outcomes, toTransactionOutcomeDTO(o))
	}
	for _, p := range problems {
		out.Problems = append(out.Problems, toProblemDTO(p))
	}
	return out
}

// The three shapes a report is made of, one at a time. A watcher is pushed
// these singly and a report carries them in lists, so each renders once here.
func toFileMovedDTO(f node.FileMoved) api.FileMovedDTO {
	return api.FileMovedDTO{
		From: string(f.From), To: string(f.To),
		OrderType: string(f.OrderType), OrderID: string(f.OrderID),
		Movement: string(f.Movement),
	}
}

func toTransactionOutcomeDTO(o node.TransactionOutcome) api.TransactionOutcomeDTO {
	return api.TransactionOutcomeDTO{
		DecidedBy: string(o.DecidedBy), Payment: string(o.Payment),
		Status: string(o.Status), Code: string(o.Code), Text: o.Text,
	}
}

func toProblemDTO(p node.Problem) api.ProblemDTO {
	return api.ProblemDTO{
		Institution: string(p.Institution), OrderID: string(p.OrderID), Detail: p.Detail,
	}
}

// ---------------------------------------------------------------------------
// The phases, and the sequences built from them
// ---------------------------------------------------------------------------

// A phaseID names one step of a business day.
type phaseID int

const (
	phasePublishRoster phaseID = iota
	phaseRefresh
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
	// holds anything before a cycle settles.
	phaseCollectClearingHouse
)

// A phase is one named step of a business day.
type phase struct {
	id   phaseID
	name string
	// settlementOnly marks a phase that runs only on a day the scheme settles
	// on. It is read by AdvanceDay alone: a derived sequence names the phases it
	// wants outright, its caller having already decided that it wants them.
	settlementOnly bool
	run            func(ctx context.Context, d *Deployment) []node.Problem
}

// beforeClock is the business day, in the order it runs, up to the clock move.
var beforeClock = []phase{
	{
		id: phasePublishRoster, name: "publish", settlementOnly: true,
		// The clearing house offers the scheme's routing table. BEFORE the refresh,
		// because a member collects what is published and a bank admitted since the
		// last advance is in this file rather than in yesterday's.
		run: func(ctx context.Context, d *Deployment) []node.Problem {
			if err := d.csm.PublishRoster(ctx); err != nil {
				return []node.Problem{{Institution: d.cfg.ClearingHouseBIC, Detail: err.Error()}}
			}
			return nil
		},
	},
	{
		id: phaseRefresh, name: "refresh", settlementOnly: true,
		// Every member collects it. It is the FIRST thing a member does, so a bank
		// admitted since the last advance can be addressed by its neighbours today
		// rather than after somebody remembers to call the route.
		run: func(ctx context.Context, d *Deployment) []node.Problem {
			var ps []node.Problem
			for _, b := range d.subscribers() {
				if _, err := b.RefreshDirectory(ctx); err != nil {
					ps = append(ps, node.Problem{Institution: b.BIC(), Detail: err.Error()})
				}
			}
			return ps
		},
	},
	{
		id: phaseBankCutoff, name: "bank cut-off", settlementOnly: true,
		// Every member reaches its own cut-off: the morning's instructions leave each
		// bank's hub as one file per scheme, uploaded to the clearing house.
		run: func(ctx context.Context, d *Deployment) []node.Problem {
			var ps []node.Problem
			for _, b := range d.subscribers() {
				_, problems := b.RunCutoff(ctx)
				ps = append(ps, problems...)
			}
			return ps
		},
	},
	{
		id: phaseClearing, name: "clearing", settlementOnly: true,
		// The clearing house works through what its members uploaded: each file is
		// recorded, each transaction validated and taken into the open cycle for its
		// scheme, each submitting bank answered per transaction — and each receiving
		// bank's share of the file BUILT AND HELD.
		run: func(ctx context.Context, d *Deployment) []node.Problem { return d.csm.Work(ctx) },
	},
	{
		id: phaseClearingHouseCutoff, name: "clearing-house cut-off", settlementOnly: true,
		// The clearing house's own cut-off: every open cycle is netted and its
		// positions uploaded to the settlement agent.
		run: func(ctx context.Context, d *Deployment) []node.Problem { return d.csm.CloseOpenCycles(ctx) },
	},
	{
		id: phaseDischarge, name: "discharge", settlementOnly: true,
		// And the cut-offs that instructed nothing are discharged where they stand.
		run: func(ctx context.Context, d *Deployment) []node.Problem { return d.csm.SettleUninstructed(ctx) },
	},
	{
		id: phaseSettlement, name: "settlement", settlementOnly: true,
		// The settlement agent works through everything uploaded to it: cut-offs
		// discharged, returns executed, lodgements credited. This is the only
		// phase in which central-bank reserves move.
		run: func(ctx context.Context, d *Deployment) []node.Problem { return d.cb.Work(ctx) },
	},
	{
		id: phaseRelease, name: "release", settlementOnly: true,
		// The clearing house collects the answers and RELEASES: each settled cycle
		// becomes the output files its receiving banks have been waiting for and an
		// ACSC apiece for the banks that submitted, and each settled RETURN becomes
		// the pacs.004 the other bank has been waiting for.
		run: func(ctx context.Context, d *Deployment) []node.Problem { return d.csm.Collect(ctx) },
	},
	{
		id: phaseCollection, name: "collection", settlementOnly: true,
		// Each member collects, THE SETTLEMENT AGENT FIRST. The order is load-bearing
		// and is the only thing that guarantees the mirror leg is booked before the
		// creditor legs draw on it — see CentralBank.advise, which argues it.
		run: func(ctx context.Context, d *Deployment) []node.Problem {
			var ps []node.Problem
			for _, b := range d.subscribers() {
				ps = append(ps, b.Collect(ctx, d.cfg.CentralBankBIC, ebics.C53)...)
				ps = append(ps, b.Collect(ctx, d.cfg.CentralBankBIC, ebics.BTD)...)
				ps = append(ps, b.Collect(ctx, d.cfg.ClearingHouseBIC, ebics.BTD)...)
			}
			return ps
		},
	},
	{
		id: phaseEndOfDay, name: "end of day",
		// Every bank's own end of day, on EVERY date and not only a settlement day:
		// interest accrues over a weekend, which is the entire reason day-count
		// conventions exist.
		run: func(ctx context.Context, d *Deployment) []node.Problem {
			var ps []node.Problem
			for _, b := range d.banksInOrder() {
				if err := b.RunEndOfDay(ctx, d.clock.Now()); err != nil {
					ps = append(ps, node.Problem{Institution: b.BIC(), Detail: err.Error()})
				}
			}
			return ps
		},
	},
}

// afterClock is what runs once the date has moved.
var afterClock = []phase{
	{
		id: phaseOpenCycles, name: "open cycles",
		// On a day that cleared, the cut-off left none open; on a day that did not,
		// the ones standing are still open and this is a no-op.
		run: func(ctx context.Context, d *Deployment) []node.Problem { return d.csm.OpenCycles(ctx) },
	},
}

// collectClearingHouseOnly is the day's collection with the two
// settlement-agent queues left out, and it is the one place a sequence holds a
// phase the day does not declare.
var collectClearingHouseOnly = phase{
	id: phaseCollectClearingHouse, name: "collection · clearing house BTD", settlementOnly: true,
	run: func(ctx context.Context, d *Deployment) []node.Problem {
		var ps []node.Problem
		for _, b := range d.subscribers() {
			ps = append(ps, b.Collect(ctx, d.cfg.ClearingHouseBIC, ebics.BTD)...)
		}
		return ps
	},
}

// only selects phases by name, in the order the day declares them.
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
func runPhases(ctx context.Context, d *Deployment, list []phase) []node.Problem {
	var ps []node.Problem
	for _, p := range list {
		ps = append(ps, p.run(ctx, d)...)
	}
	return ps
}

// carryToClearingPhases is the file-moving prefix of a day: every member's
// cut-off, the clearing house's work over what they uploaded, and each member
// collecting the answers from it.
var carryToClearingPhases = append(
	only(beforeClock, phaseBankCutoff, phaseClearing),
	collectClearingHouseOnly,
)

// subscribePhases is the day's opening pair on its own: what a deployment does
// once before anything can be routed at all.
var subscribePhases = only(beforeClock, phasePublishRoster, phaseRefresh)

// ---------------------------------------------------------------------------
// The doors: one per phase, so a day can be run a step at a time
// ---------------------------------------------------------------------------

// dayPhases is every phase a business day declares, in the order it runs them,
// and it is the whole list of doors. A derived sequence is not in it and neither
// is the narrowed collection — see the design record.
var dayPhases = append(slices.Clone(beforeClock), afterClock...)

// phaseKey is a phase's name spelt for a URL, which is the only identifier a
// door has: a phase is named, never parameterised.
func phaseKey(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", "-"))
}

// phaseDoors is every phase by its key. Building it at init is what makes two
// phases spelt alike a boot failure rather than a door that shadows another.
var phaseDoors = doorsOf(dayPhases)

func doorsOf(list []phase) map[string]phase {
	out := make(map[string]phase, len(list))
	for _, p := range list {
		key := phaseKey(p.name)
		if _, dup := out[key]; dup {
			panic(fmt.Sprintf("server: two phases of a business day are spelt %q", key))
		}
		out[key] = p
	}
	return out
}

// afterTheClock reports whether a phase runs once the date has moved.
func afterTheClock(p phase) bool {
	return slices.ContainsFunc(afterClock, func(q phase) bool { return q.id == p.id })
}

func toPhaseDTO(p phase, completed bool) api.PhaseDTO {
	return api.PhaseDTO{
		Key: phaseKey(p.name), Name: p.name,
		SettlementOnly: p.settlementOnly, AfterClock: afterTheClock(p),
		Completed: completed,
	}
}

func toPhaseReportDTO(r PhaseReport) api.PhaseReportDTO {
	return api.PhaseReportDTO{
		Phase:        toPhaseDTO(r.Phase, r.Completed),
		Phases:       phaseKeys(r.Phases),
		Ran:          toBusinessDateDTO(r.Ran),
		MovementsDTO: toMovementsDTO(r.Files, r.Outcomes, r.Problems),
	}
}

// phaseKeys is what a run took, in the order it took it.
func phaseKeys(list []phase) []string {
	keys := make([]string, 0, len(list))
	for _, p := range list {
		keys = append(keys, phaseKey(p.name))
	}
	return keys
}

// ---------------------------------------------------------------------------
// The cursor: how far the day the clock stands on has got
// ---------------------------------------------------------------------------

// The marker lives on the clock's own record, so the date and how far into it
// the deployment has got move in one write. See
// [the design record](../../docs/specs/2026-08-14-a-day-cursor-design.md).

// today is the phases the date the clock stands on will run before the roll.
// afterClock is not among them: those run on the date the roll landed on, so
// they cannot be progress through the one being left.
func (d *Deployment) today(on BusinessDate) []phase {
	return onSettlementDay(beforeClock, on.SettlementDay)
}

// reachedIn is where the marker sits in a sequence, or -1 for a sequence it
// names no phase of — which is how a day that has run nothing and a marker from
// a sequence this one is not walking both come out as "all of it still to run".
func reachedIn(list []phase, marker string) int {
	if marker == "" {
		return -1
	}
	return slices.IndexFunc(list, func(p phase) bool { return phaseKey(p.name) == marker })
}

// remaining is the part of a sequence the marker leaves to run.
func remaining(list []phase, marker string) []phase {
	return list[reachedIn(list, marker)+1:]
}

// reach records p as how far the day has got, and only when p is the phase the
// day would have run NEXT. A phase stepped out of turn moves nothing: a marker
// that jumped to it would let the day skip the phases before it, and the day
// runs it in its place instead.
func (d *Deployment) reach(p phase, on BusinessDate) error {
	todo := remaining(d.today(on), d.clock.Reached())
	if len(todo) == 0 || todo[0].id != p.id {
		return nil
	}
	if err := d.clock.Reach(phaseKey(p.name)); err != nil {
		return fmt.Errorf("server: %s ran on %s and how far the day has got could not be recorded: %w",
			p.name, on.Date.Format(time.DateOnly), err)
	}
	d.journal.phase(toPhaseDTO(p, true))
	return nil
}

// completed is the phases of one day the marker leaves behind it. A phase the
// day does not run is in no answer here: it cannot be progress through a
// sequence it is not part of.
func (d *Deployment) completed(on BusinessDate) map[phaseID]bool {
	today := d.today(on)
	ran := reachedIn(today, d.clock.Reached())
	done := make(map[phaseID]bool, ran+1)
	for _, p := range today[:ran+1] {
		done[p.id] = true
	}
	return done
}

// Phases is the business day as it is declared, each phase carrying whether the
// day the clock stands on has run it. It is the deployment's rather than the
// console's because the marker is.
func (d *Deployment) Phases() []api.PhaseDTO {
	done := d.completed(d.BusinessDate())

	out := make([]api.PhaseDTO, 0, len(dayPhases))
	for _, p := range dayPhases {
		out = append(out, toPhaseDTO(p, done[p.id]))
	}
	return out
}

// ---------------------------------------------------------------------------
// The day itself
// ---------------------------------------------------------------------------

// AdvanceDay runs what is left of this business day and moves the clock to the
// next. What is left is the phases after the marker, so a day some of whose
// phases were stepped by hand finishes rather than starting again.
func (d *Deployment) AdvanceDay(ctx context.Context) (DayReport, error) {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	report, err := d.advanceDay(ctx)
	if err != nil {
		return report, err
	}
	report.Files, report.Outcomes, report.Problems = d.journal.take()
	return report, nil
}

// advanceDay is AdvanceDay without the lock and without emptying the journal,
// which is what lets a SCENARIO run several days and report what all of them
// moved as one. Every caller holds resetMu.
func (d *Deployment) advanceDay(ctx context.Context) (DayReport, error) {
	ran := d.BusinessDate()
	report := DayReport{Ran: ran, Next: ran}

	for _, p := range remaining(d.today(ran), d.clock.Reached()) {
		d.journal.problem(p.run(ctx, d)...)
		report.Phases = append(report.Phases, p)
		// A marker that cannot be written is the end of the day: carrying on would
		// leave phases behind that nothing records, and the write that moves the
		// date is the same one.
		if err := d.reach(p, ran); err != nil {
			return report, err
		}
	}

	next, err := d.clock.Advance()
	if err != nil {
		err = fmt.Errorf("server: the business day ran on %s and the clock could not be moved past it: %w",
			ran.Date.Format(time.DateOnly), err)
		next = d.clock.Now()
	}
	report.Next = businessDateOf(next)

	after := onSettlementDay(afterClock, ran.SettlementDay)
	d.journal.problem(runPhases(ctx, d, after)...)
	report.Phases = append(report.Phases, after...)
	return report, err
}

// A PhaseReport is a run that stopped at a named phase, and everything it
// moved. It carries no next date: a phase is a step inside a day, and only
// advancing the day moves the clock.
type PhaseReport struct {
	// Phase is the door that was opened, and Phases is what running through it
	// took, in order — the outstanding phases before it and then it.
	Phase  phase
	Phases []phase
	Ran    BusinessDate

	// Completed says the day counts the named phase as behind it, which a phase
	// run out of turn is not: it ran, and the day will run it in its place.
	Completed bool

	Files    []node.FileMoved
	Outcomes []node.TransactionOutcome
	Problems []node.Problem
}

// through is what running the day as far as p takes: every phase of today still
// outstanding, up to and including p. A day holding p outstanding at all is the
// condition — a phase already behind the marker, and one today does not run,
// are each run ALONE, which is what makes asking twice run it twice.
func (d *Deployment) through(p phase, on BusinessDate) []phase {
	todo := remaining(d.today(on), d.clock.Reached())
	at := slices.IndexFunc(todo, func(q phase) bool { return q.id == p.id })
	if at < 0 {
		return []phase{p}
	}
	return todo[:at+1]
}

// RunThrough runs the business day as far as one named phase and hands back
// what the whole of that took. It runs the phase whatever day it is:
// settlementOnly is AdvanceDay's question, and a caller that named a phase has
// already decided it wants it.
//
// The stop is named from the day's own sequence and the sequence is the day's,
// which is why this is not a route that composes one. See
// [the design record](../../docs/specs/2026-08-14-a-day-cursor-design.md).
func (d *Deployment) RunThrough(ctx context.Context, key string) (PhaseReport, error) {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	report, err := d.runThrough(ctx, key)
	if err != nil {
		return report, err
	}
	report.Files, report.Outcomes, report.Problems = d.journal.take()
	return report, nil
}

// runThrough is RunThrough without the lock and without emptying the journal.
// See advanceDay, which is split for the same reason: a scenario steps the day
// and reports what all of its steps moved as one.
func (d *Deployment) runThrough(ctx context.Context, key string) (PhaseReport, error) {
	p, ok := phaseDoors[key]
	if !ok {
		return PhaseReport{}, api.BadRequest("no phase of a business day is spelt %q", key)
	}

	ran := d.BusinessDate()
	report := PhaseReport{Phase: p, Ran: ran}
	var err error
	for _, q := range d.through(p, ran) {
		d.journal.problem(q.run(ctx, d)...)
		report.Phases = append(report.Phases, q)
		// A marker that cannot be written stops the run, for AdvanceDay's reason:
		// carrying on would leave phases behind that nothing records.
		if err = d.reach(q, ran); err != nil {
			break
		}
	}

	report.Completed = d.completed(ran)[p.id]
	return report, err
}

// Subscribe is the clearing house publishing its routing table and every member
// collecting it, in that order. The order is the DEPLOYMENT's: neither
// institution knows the other performs half of it.
func (d *Deployment) Subscribe(ctx context.Context) error {
	return node.JoinProblemDetails(runPhases(ctx, d, subscribePhases))
}

// carryToClearing moves the morning's files and stops before anything settles:
// every member's cut-off, the clearing house's work over what they uploaded,
// and each member collecting the answers. It empties no journal, for
// advanceDay's reason: a scenario carries files several times and reports what
// all of it moved as one.
func (d *Deployment) carryToClearing(ctx context.Context) []node.Problem {
	return runPhases(ctx, d, carryToClearingPhases)
}
