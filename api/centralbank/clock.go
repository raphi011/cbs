package centralbank

import (
	"context"
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
)

// dayTimeout bounds a detached business day.
//
// A day is N+2 institutions working through their queues in nine phases, and it
// is generous for the same reason resetTimeout is: the deadline exists to stop a
// wedged connection holding a goroutine forever, not to police the work.
const dayTimeout = 60 * time.Second

func (s *surface) registerClockRoutes(mux *api.Router) {
	mux.HandleFunc("GET /clock", api.Handle(http.StatusOK, s.handleGetClock))
	mux.HandleFunc("POST /clock/day", api.Handle(http.StatusOK, s.handleAdvanceDay))
}

// handleGetClock is what day this deployment is on.
//
// It is a read of the deployment and takes no lock, so it answers while a day is
// running — with the date that day started on, which is the truth until the
// advance commits.
func (s *surface) handleGetClock(r *http.Request) (api.BusinessDateDTO, error) {
	return s.inst.BusinessDate(), nil
}

// handleAdvanceDay runs one business day and moves the clock to the next.
//
// # Why it is on this listener
//
// It is the same argument GET /members and POST /admin/reset are served here by:
// advancing the day drives all N+2 institutions, which is one operator's act
// over a DEPLOYMENT, and a deployment is not an institution. The settlement
// agent's console is where the operator stands, and serving it here is not the
// claim that the settlement agent performs it.
//
// POST /end-of-day on a bank's own port survives beside it and is that bank's
// own act. The day engine calls the same batches through the same entry point,
// so there is one accrual path rather than two.
//
// # Why the work outlives the request
//
// A day is durable at every step: files leave queues, payments change state,
// reserves move, and the clock advances at the end. A request cancelled halfway
// would leave the queues drained and the date unmoved, which is a state no
// operator can reason about and no route can finish. So the day is detached from
// the request's cancellation and given a deadline of its own — handleReset's
// shape, and for the same reason.
//
// # It answers 200 and not 202
//
// The work is finished when this returns. That is the whole of what the business
// day bought: there is nothing in flight to wait for, so the answer is the day's
// report rather than an acknowledgement that something has been started.
func (s *surface) handleAdvanceDay(r *http.Request) (api.DayReportDTO, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), dayTimeout)
	defer cancel()

	report, err := s.inst.AdvanceDay(ctx)
	if err != nil {
		// The report comes back beside the error, because a day that failed at
		// some phase still moved everything up to it. It is logged rather than
		// returned: what a caller can act on is the failure, and the files that did
		// move are in the log with it.
		s.inst.Log().Error("business day", "error", err, "ran", report.Ran.Date, "problems", len(report.Problems))
		return api.DayReportDTO{}, err
	}
	s.inst.Log().Info("business day",
		"ran", report.Ran.Date, "next", report.Next.Date,
		"settled", report.Ran.SettlementDay,
		"files", len(report.Files), "outcomes", len(report.Outcomes), "problems", len(report.Problems))
	return report, nil
}
