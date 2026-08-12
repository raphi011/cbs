// Package centralbank is the settlement agent's HTTP surface: the reserves it
// holds, the settlements it discharged, and its own book's audit trail.
package centralbank

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

// An Institution is the settlement agent, as this surface needs it, plus the
// two deployment-wide acts the package doc names.
type Institution interface {
	// Network is the settlement agent's own view: its reserve register, its
	// settlements, its own book.
	Network() *payment.CentralBankNetwork

	// Members is every bank the deployment holds a database for, each read out of
	// its own database.
	Members(ctx context.Context) ([]*payment.Bank, error)

	// Reset clears the store and rebuilds the sample dataset. The deployment's
	// act, served here for the reason the package doc gives.
	Reset(ctx context.Context) error

	// BusinessDate is what day the deployment is on, and AdvanceDay runs one
	// business day and moves it to the next.
	BusinessDate() api.BusinessDateDTO
	AdvanceDay(ctx context.Context) (api.DayReportDTO, error)

	// Log is what the middleware chain and the reset route write through.
	Log() *slog.Logger
}

// surface is the handler receiver: one Institution, and nothing else.
type surface struct{ inst Institution }

func (s *surface) network() *payment.CentralBankNetwork { return s.inst.Network() }
