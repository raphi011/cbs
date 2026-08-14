package centralbank

import (
	"context"
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
)

// dayTimeout bounds a detached run of the business day, whole or one phase.
const dayTimeout = 60 * time.Second

func (s *surface) registerClockRoutes(mux *api.Router) {
	mux.HandleFunc("GET /clock", api.Handle(http.StatusOK, s.handleGetClock))
	mux.HandleFunc("POST /clock/day", api.Handle(http.StatusOK, s.handleAdvanceDay))
	// A door per phase, so a reader can run the day as far as the clearing and
	// stop. The day's own declaration is the list of them and nothing here
	// composes a sequence.
	mux.HandleFunc("GET /clock/phases", api.Handle(http.StatusOK, s.handleListPhases))
	mux.HandleFunc("POST /clock/phases/{phase}", api.Handle(http.StatusOK, s.handleRunThrough))
}

// handleGetClock is what day this deployment is on.
func (s *surface) handleGetClock(r *http.Request) (api.BusinessDateDTO, error) {
	return s.op.BusinessDate(), nil
}

// handleAdvanceDay runs one business day and moves the clock to the next.
func (s *surface) handleAdvanceDay(r *http.Request) (api.DayReportDTO, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), dayTimeout)
	defer cancel()

	report, err := s.op.AdvanceDay(ctx)
	if err != nil {
		// The report comes back beside the error, because a day that failed at some
		// phase still moved everything up to it.
		s.inst.Log().Error("business day", "error", err, "ran", report.Ran.Date, "problems", len(report.Problems))
		return api.DayReportDTO{}, err
	}
	s.inst.Log().Info("business day",
		"ran", report.Ran.Date, "next", report.Next.Date,
		"settled", report.Ran.SettlementDay,
		"files", len(report.Files), "outcomes", len(report.Outcomes), "problems", len(report.Problems))
	return report, nil
}

// handleListPhases is the business day as it is declared, in the order it runs.
func (s *surface) handleListPhases(r *http.Request) ([]api.PhaseDTO, error) {
	return s.op.Phases(), nil
}

// handleRunThrough runs the day as far as one phase and answers with what that
// moved. The clock stays where it stood, which is the whole difference between
// stepping a day and advancing one.
func (s *surface) handleRunThrough(r *http.Request) (api.PhaseReportDTO, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), dayTimeout)
	defer cancel()

	report, err := s.op.RunThrough(ctx, r.PathValue("phase"))
	if err != nil {
		s.inst.Log().Error("business day phase", "error", err, "phase", r.PathValue("phase"))
		return api.PhaseReportDTO{}, err
	}
	s.inst.Log().Info("business day phase",
		"phase", report.Phase.Key, "took", len(report.Phases), "ran", report.Ran.Date,
		"files", len(report.Files), "outcomes", len(report.Outcomes), "problems", len(report.Problems))
	return report, nil
}
