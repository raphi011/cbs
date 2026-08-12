package centralbank

import (
	"context"
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
)

// dayTimeout bounds a detached business day.
const dayTimeout = 60 * time.Second

func (s *surface) registerClockRoutes(mux *api.Router) {
	mux.HandleFunc("GET /clock", api.Handle(http.StatusOK, s.handleGetClock))
	mux.HandleFunc("POST /clock/day", api.Handle(http.StatusOK, s.handleAdvanceDay))
}

// handleGetClock is what day this deployment is on.
func (s *surface) handleGetClock(r *http.Request) (api.BusinessDateDTO, error) {
	return s.inst.BusinessDate(), nil
}

// handleAdvanceDay runs one business day and moves the clock to the next.
func (s *surface) handleAdvanceDay(r *http.Request) (api.DayReportDTO, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), dayTimeout)
	defer cancel()

	report, err := s.inst.AdvanceDay(ctx)
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
