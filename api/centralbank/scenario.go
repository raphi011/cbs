package centralbank

import (
	"context"
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
)

// scenarioTimeout bounds a detached run. It is the most generous deadline on
// this surface because a scenario may advance the business date many times, and
// the demo network advances it about a hundred and fifty.
const scenarioTimeout = 5 * time.Minute

func (s *surface) registerScenarioRoutes(mux *api.Router) {
	mux.HandleFunc("GET /scenarios", api.Handle(http.StatusOK, s.handleListScenarios))
	mux.HandleFunc("POST /scenarios/{id}", api.Handle(http.StatusOK, s.handleRunScenario))
}

// handleListScenarios is what an operator may trigger.
func (s *surface) handleListScenarios(r *http.Request) ([]api.ScenarioDTO, error) {
	return s.op.Scenarios(), nil
}

// handleRunScenario drives one and answers with what it moved. A scenario is
// named, never parameterised: what it does is decided where it is written.
func (s *surface) handleRunScenario(r *http.Request) (api.ScenarioReportDTO, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), scenarioTimeout)
	defer cancel()

	report, err := s.op.RunScenario(ctx, r.PathValue("id"))
	if err != nil {
		// The report comes back beside the error, for AdvanceDay's reason: a scenario
		// that stopped halfway still moved everything up to where it stopped.
		s.inst.Log().Error("scenario", "error", err, "scenario", r.PathValue("id"))
		return api.ScenarioReportDTO{}, err
	}
	s.inst.Log().Info("scenario",
		"scenario", report.Scenario.ID, "ran", report.Ran.Date, "next", report.Next.Date,
		"files", len(report.Files), "outcomes", len(report.Outcomes), "problems", len(report.Problems))
	return report, nil
}
