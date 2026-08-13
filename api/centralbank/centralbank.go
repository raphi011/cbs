// Package centralbank is the settlement agent's HTTP surface: the reserves it
// holds, the settlements it discharged, and its own book's audit trail.
package centralbank

import (
	"context"
	"log/slog"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

// An Institution is the settlement agent, as this surface needs it.
type Institution interface {
	// Network is the settlement agent's own view: its reserve register, its
	// settlements, its own book.
	Network() *payment.CentralBankNetwork

	// Log is what the middleware chain and the reset route write through.
	Log() *slog.Logger
}

// An Operator is the DEPLOYMENT: a business day drives all N+2 institutions and
// none of them owns it. These are served on this listener because that is where
// the operator's console lives, and on this interface because the type system
// should say whose acts they are.
type Operator interface {
	// Members is every bank the deployment holds a database for, each read out of
	// its own database.
	Members(ctx context.Context) ([]*payment.Bank, error)

	// Reset clears the store and rebuilds the sample dataset.
	Reset(ctx context.Context) error

	// BusinessDate is what day the deployment is on, and AdvanceDay runs one
	// business day and moves it to the next.
	BusinessDate() api.BusinessDateDTO
	AdvanceDay(ctx context.Context) (api.DayReportDTO, error)

	// Phases is the business day as it is declared, in order, and RunPhase runs
	// one of them without moving the clock. A phase is named, never parameterised.
	Phases() []api.PhaseDTO
	RunPhase(ctx context.Context, phase string) (api.PhaseReportDTO, error)
}

// surface is the handler receiver: one institution and the operator whose
// console shares its listener.
type surface struct {
	inst Institution
	op   Operator
}

func (s *surface) network() *payment.CentralBankNetwork { return s.inst.Network() }
