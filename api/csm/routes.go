package csm

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// Routes is the CSM's surface: every payment in the network, the clearing
// cycles, the schemes and the routing directory.
func Routes(inst Institution, op Operator) *api.Router {
	s := &surface{inst: inst, op: op}
	mux := api.NewRouter()
	// GET /members is not here.
	mux.HandleFunc("GET /schemes", api.Handle(http.StatusOK, s.handleListSchemes))
	// The ROUTING directory, and it is the one the paragraph above says this
	// institution genuinely owns: roster_entries, written by payment.AdmitMemberTx
	// from the settlement agent's acknowledgement.
	mux.HandleFunc("GET /roster", api.Handle(http.StatusOK, s.handleListRoster))
	mux.HandleFunc("GET /assets", api.HandleListAssets)
	mux.HandleFunc("GET /payments/audit", api.Handle(http.StatusOK, s.handlePaymentAudit))
	// The MANDATES are not here.
	s.registerPaymentRoutes(mux)
	return mux
}
