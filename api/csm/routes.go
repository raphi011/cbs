package csm

import (
	"github.com/raphi011/cbs/api"
)

// Routes is the CSM's surface: every payment in the network, the clearing
// cycles, the schemes and the routing directory.
//
// It hands back the router rather than a handler so a caller can read the
// patterns off it, and so the middleware chain is one call (api.Router.Handler)
// that no surface package has to remember.
func Routes(inst Institution) *api.Router {
	s := &surface{inst: inst}
	mux := api.NewRouter()
	// GET /members is not here. It answers ListBanks — every bank in the network,
	// founded ones included, each carrying its status — and the reading that put
	// it here was that this is the console the network's membership is watched
	// from, and watching a bank become a member needs the banks that are not one
	// yet.
	//
	// The clearing house has a database with no banks table in it; what it holds is
	// roster_entries, written by payment.AdmitMemberTx from the settlement agent's
	// acknowledgement, and a roster is exactly the list that omits a founded bank.
	// So the two are separate: the roster is below, on GET /roster, and it is this
	// institution's own answer; the bank list is on the central bank's listener,
	// where it is the operator's read rather than an institution's.
	//
	// This institution's share of admission is one act: refuse an address it has
	// already admitted somebody else to, and write the routing entry from the
	// settlement agent's answer (payment.AdmitMemberTx).
	mux.HandleFunc("GET /schemes", s.handleListSchemes)
	// The ROUTING directory, and it is the one the paragraph above says this
	// institution genuinely owns: roster_entries, written by payment.AdmitMemberTx
	// from the settlement agent's acknowledgement. It answers "where may a message
	// addressed to this member be sent", which is a question about addresses and
	// not about accounts.
	//
	// GET /directory is not here. Resolving an IBAN across the whole network would
	// sweep every bank's register — a clearing house reading every member's deposit
	// accounts, which is not a thing this institution holds and, since the csm
	// shape has no deposit register, not a thing it could reach. The bank's port
	// has a lookup in that bank's own register (api/bank); what a clearing house
	// has always actually had is the roster.
	mux.HandleFunc("GET /roster", s.handleListRoster)
	mux.HandleFunc("GET /assets", api.HandleListAssets)
	mux.HandleFunc("GET /payments/audit", s.handlePaymentAudit)
	// The MANDATES are not here. A mandate is the creditor bank's own row — in
	// SEPA the creditor holds it, and the bank that checks one at submission is the
	// creditor's (payment.SDD.ValidateMandate) — so this console would hold every
	// member's authorisations over every other member's customers' accounts, and
	// render each by reading the DEBTOR bank's deposit register for its asset. The
	// csm shape has no deposit register. See api/bank's mandate routes, and
	// payment.Mandate, which carries its own asset.
	s.registerPaymentRoutes(mux)
	return mux
}
