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
// none of them owns it. Served on this listener because the operator's console
// is here, and on this interface so the type system says whose acts they are.
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

	// Phases is the business day as it is declared, in order, and RunThrough runs
	// it as far as one of them without moving the clock. The stop is named, never
	// parameterised.
	Phases() []api.PhaseDTO
	RunThrough(ctx context.Context, phase string) (api.PhaseReportDTO, error)

	// Scenarios is what an operator may trigger, and RunScenario drives one. A
	// scenario advances the shared clock, so it takes the lock the day takes.
	Scenarios() []api.ScenarioDTO
	RunScenario(ctx context.Context, id string) (api.ScenarioReportDTO, error)

	// NetworkFlow is the mesh: every institution's traffic paired into the
	// crossings both ends observed. Limit bounds the crossings already delivered.
	NetworkFlow(ctx context.Context, limit int) (api.NetworkFlowDTO, error)

	// Watch is what the deployment does, as it does it. The channel closes when a
	// watcher has fallen too far behind to be told the truth, and release must be
	// called when it goes away.
	Watch() (events <-chan api.StreamEvent, release func())
}

// surface is the handler receiver: one institution and the operator whose
// console shares its listener.
type surface struct {
	inst Institution
	op   Operator
}

func (s *surface) network() *payment.CentralBankNetwork { return s.inst.Network() }
