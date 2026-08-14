package main

import (
	"context"
	"slices"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/node"
)

// A scenario is the DEPLOYMENT's, which is why the registry is here and not
// under node/: each drives more than one institution, and no institution may
// know a deployment exists. See [the design record](../../docs/specs/2026-08-13-scenarios-and-a-blank-slate-design.md).

// A Scenario is a named thing an operator can make happen. Run drives the
// deployment's own doors, in order, as an operator would.
type Scenario struct {
	ID, Name, Description string
	Run                   func(context.Context, *Deployment) error
}

// scenarios is every one this deployment offers, in the order the picker lists
// them: the four that teach one thing each, and the demo network last because
// it is the largest and the least specific.
var scenarios = []Scenario{
	scenarioPayment,
	scenarioRejected,
	scenarioReturned,
	scenarioArrears,
	scenarioDemoNetwork,
}

// Scenarios is what an operator may trigger.
func (d *Deployment) Scenarios() []api.ScenarioDTO {
	out := make([]api.ScenarioDTO, 0, len(scenarios))
	for _, s := range scenarios {
		out = append(out, api.ScenarioDTO{ID: s.ID, Name: s.Name, Description: s.Description})
	}
	return out
}

// A ScenarioReport is what running one moved, and where it left the clock.
type ScenarioReport struct {
	Scenario  Scenario
	Ran, Next BusinessDate

	Files    []node.FileMoved
	Outcomes []node.TransactionOutcome
	Problems []node.Problem
}

// RunScenario drives one, under the same lock AdvanceDay and Reset take: there
// is one clock and one set of databases, so two scenarios run in sequence or not
// at all.
func (d *Deployment) RunScenario(ctx context.Context, id string) (ScenarioReport, error) {
	i := slices.IndexFunc(scenarios, func(s Scenario) bool { return s.ID == id })
	if i < 0 {
		return ScenarioReport{}, api.BadRequest("no scenario is named %q", id)
	}
	s := scenarios[i]

	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	ran := d.BusinessDate()
	err := s.Run(ctx, d)
	report := ScenarioReport{Scenario: s, Ran: ran, Next: d.BusinessDate()}
	// Taken whether or not it finished: a scenario that failed halfway still moved
	// everything up to where it stopped, and that is what the operator has.
	report.Files, report.Outcomes, report.Problems = d.journal.take()
	return report, err
}

func toScenarioReportDTO(r ScenarioReport) api.ScenarioReportDTO {
	return api.ScenarioReportDTO{
		Scenario: api.ScenarioDTO{
			ID: r.Scenario.ID, Name: r.Scenario.Name, Description: r.Scenario.Description,
		},
		Ran:          toBusinessDateDTO(r.Ran),
		Next:         toBusinessDateDTO(r.Next),
		MovementsDTO: toMovementsDTO(r.Files, r.Outcomes, r.Problems),
	}
}
