package centralbank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// Routes is the settlement layer's surface: reserves, its own audit, the
// deployment's bank list, and the reset that rebuilds the sample dataset.
func Routes(inst Institution, op Operator) *api.Router {
	s := &surface{inst: inst, op: op}
	mux := api.NewRouter()
	mux.HandleFunc("GET /reserves", api.Handle(http.StatusOK, s.handleListReserves))
	mux.HandleFunc("GET /reserves/{bic}", api.Handle(http.StatusOK, s.handleGetReserve))
	mux.HandleFunc("GET /audit", api.Handle(http.StatusOK, s.handleCentralBankAudit))
	// This institution's own half of every crossing, and one file at a time. The
	// mesh is not here: a counterparty's half is in the counterparty's database.
	mux.HandleFunc("GET /messages", api.Handle(http.StatusOK, s.handleListMessages))
	mux.HandleFunc("GET /messages/{seq}", api.Handle(http.StatusOK, s.handleGetMessage))
	// Every bank this deployment holds a database for.
	mux.HandleFunc("GET /members", api.Handle(http.StatusOK, s.handleListParticipants))
	// There is no POST here, and its absence is the shape of the transport.
	mux.HandleFunc("GET /settlements", api.Handle(http.StatusOK, s.handleListSettlements))
	mux.HandleFunc("GET /settlements/{sid}", api.Handle(http.StatusOK, s.handleGetSettlement))
	mux.HandleFunc("GET /assets", api.HandleListAssets)
	// Reset clears the store and reseeds it.
	s.registerAdminRoutes(mux)
	// The business date, the button that advances it, and a door per phase.
	s.registerClockRoutes(mux)
	return mux
}
