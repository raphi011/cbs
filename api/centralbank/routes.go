package centralbank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// Routes is the settlement layer's surface: reserves, its own audit, the
// deployment's bank list, and the reset that rebuilds the sample dataset.
//
// It hands back the router rather than a handler so a caller can read the
// patterns off it, and so the middleware chain is one call (api.Router.Handler)
// that no surface package has to remember.
func Routes(inst Institution) *api.Router {
	s := &surface{inst: inst}
	mux := api.NewRouter()
	mux.HandleFunc("GET /reserves", api.Handle(http.StatusOK, s.handleListReserves))
	mux.HandleFunc("GET /reserves/{bic}", api.Handle(http.StatusOK, s.handleGetReserve))
	mux.HandleFunc("GET /audit", api.Handle(http.StatusOK, s.handleCentralBankAudit))
	// Every bank this deployment holds a database for. It is here rather than at
	// the clearing house because the clearing house has no banks table — the csm shape
	// holds a roster and nothing else, and a roster deliberately omits the founded
	// bank this listing exists to show (see handleListParticipants).
	//
	// # This route is the OPERATOR's, not the settlement agent's
	//
	// Which is worth stating plainly, because everything else on this listener
	// reads the settlement agent's own register and this reads N other databases.
	// What justifies it is the same thing that justifies POST /admin/reset sitting
	// here: listing the banks a deployment holds and rebuilding them are one
	// operator's acts over a DEPLOYMENT, and a deployment is not an institution.
	// The listener is where the operator's console is served; it is not the claim
	// that the settlement agent is performing them.
	mux.HandleFunc("GET /members", api.Handle(http.StatusOK, s.handleListParticipants))
	// There is no POST here, and its absence is the shape of what the mesh
	// changed. A settlement is performed on INSTRUCTION: the clearing house reaches
	// a cut-off, sends a pacs.009, and the central bank's actor answers ACSC or
	// RJCT/AM04 (cmd/server's centralBank). A route that let a human do it beside that
	// would be a second way to settle the same cycle, racing the first.
	//
	// So the two reads below are what is left, and they are the point of keeping
	// them: the console does not drive settlement, it WATCHES it. A settlement row
	// is this institution's own record of a cut-off it discharged, and the net
	// positions on it, beside the reserves, are what it moved.
	//
	// # GET /cycles and GET /cycles/{cid} are not here
	//
	// There were four reads, and the two on CYCLES were the clearing house's
	// rows read on this listener — which was invisible while one store held both
	// and is a missing table now. It is not a loss the console feels: a
	// settlement carries the cut-off's id, its net positions and, since this
	// change, its asset, so everything those two routes were being read FOR is
	// on the row this institution wrote itself. What is genuinely gone is the
	// closed cycle with NO settlement against it — an instruction this agent
	// refused — and that is visible where the refusal happened, on the clearing
	// house's own GET /cycles.
	//
	// What it still cannot reach is an individual payment: GET /payments is the
	// clearing house's, and a real central bank does not see one.
	mux.HandleFunc("GET /settlements", api.Handle(http.StatusOK, s.handleListSettlements))
	mux.HandleFunc("GET /settlements/{sid}", api.Handle(http.StatusOK, s.handleGetSettlement))
	mux.HandleFunc("GET /assets", api.HandleListAssets)
	// Reset clears the store and reseeds it. It belongs to one operator because
	// the deployment behind every listener serializes it per process, and the
	// central bank is the operator whose act "the system starts with these banks
	// in it" is.
	s.registerAdminRoutes(mux)
	// The business date, and the button that advances it. Both are the
	// deployment's acts and neither is the settlement agent's; see
	// handleAdvanceDay for why they are served here and why POST /end-of-day on a
	// bank's own port survives beside them.
	s.registerClockRoutes(mux)
	return mux
}
