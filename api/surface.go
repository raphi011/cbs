package api

import (
	"net/http"

	"github.com/raphi011/cbs/payment"
)

// The three operator surfaces.
//
// One binary, one process by default, one listener per entity: one per member
// bank, one for the central bank, one for the clearing house. What a caller can
// reach is decided by which port they are talking to, and a bank's routes have
// nowhere to name another bank — the port carries the identity, which is why
// s.participant reads it off the Server rather than out of the path.
//
// This is scoping, not authorization. Nothing verifies that the caller on a
// bank's port is that bank; the port is the claim. What it removes is the
// ability to reach another operator's data by editing a URL, because that URL
// does not exist on the port you are talking to.

// CentralBankRoutes is the settlement layer's surface: reserves, its own audit,
// admission, and the reset that rebuilds the sample dataset.
func (s *Server) CentralBankRoutes() http.Handler {
	return s.withMiddleware(s.centralBankRouter().mux)
}

// ClearingHouseRoutes is the CSM's surface: every payment in the network, the
// clearing cycles, the schemes, the mandates and the directory.
func (s *Server) ClearingHouseRoutes() http.Handler {
	return s.withMiddleware(s.clearingHouseRouter().mux)
}

// BankRoutes is one member bank's surface, bound to its identity.
func (s *Server) BankRoutes(pid payment.ParticipantID) http.Handler {
	b := s.forBank(pid)
	return b.withMiddleware(b.bankRouter().mux)
}

func (s *Server) centralBankRouter() *router {
	mux := newRouter()
	mux.HandleFunc("GET /reserves", s.handleListReserves)
	mux.HandleFunc("GET /reserves/{pid}", s.handleGetReserve)
	mux.HandleFunc("GET /audit", s.handleCentralBankAudit)
	// This route FOUNDS a bank and applies to the scheme for it. What it writes
	// is the new bank's own book, its chart of accounts, one set of internal
	// accounts per asset and the deposit product every customer account is
	// opened from — every one of those accounts in that bank's own book — and
	// then one acmt.007 goes out per asset, addressed to the CLEARING HOUSE,
	// which relays it. Hence the
	// 202: what the applicant gets back is a Founded bank, and the answer is two
	// other institutions' to give (mesh.Mesh.Admit).
	//
	// So it opens nothing in the book this listener belongs to. The settlement
	// account is this central bank's, and this central bank opens it later, in
	// its own handler, when the relayed application reaches its actor
	// (mesh.centralBank.receiveAdmission, payment.OpenSettlementAccountTx).
	// What puts the route here is therefore not an act on the central bank's
	// book — it is the same thing that puts /admin/reset here: founding the
	// banks a network starts with is one operator's act, and this is that
	// operator's console.
	mux.HandleFunc("POST /members", s.handleAddParticipant)
	// There is no POST here, and its absence is the shape of what the mesh
	// changed. Settling used to be an operator's act — a human opened the
	// central bank's console and pressed a button on a cycle somebody else had
	// closed, with nothing between the two consoles but the operators. A
	// settlement is now performed on INSTRUCTION: the clearing house reaches a
	// cut-off, sends a pacs.009, and the central bank's actor answers ACSC or
	// RJCT/AM04 (mesh.centralBank). A route that let a human do it beside that
	// would be a second way to settle the same cycle, racing the first.
	//
	// So the four reads below are what is left, and they are the whole point of
	// keeping them: the console no longer drives settlement, it WATCHES it. A
	// closed cycle with no settlement against it is an instruction the central
	// bank refused, and the net positions beside the reserves are why.
	//
	// What it still cannot reach is an individual payment: GET /payments is the
	// clearing house's, and a real central bank does not see one.
	mux.HandleFunc("GET /cycles", s.handleListCycles)
	mux.HandleFunc("GET /cycles/{cid}", s.handleGetCycle)
	mux.HandleFunc("GET /settlements", s.handleListSettlements)
	mux.HandleFunc("GET /settlements/{sid}", s.handleGetSettlement)
	mux.HandleFunc("GET /assets", s.handleListAssets)
	// Reset clears the store and reseeds it. It belongs to one operator
	// because Server.resetMu serializes it per process, and the central bank
	// is the operator whose act "the system starts with these banks in it" is.
	s.registerAdminRoutes(mux)
	return mux
}

func (s *Server) clearingHouseRouter() *router {
	mux := newRouter()
	// This is NOT the roster, whatever the name suggests: handleListParticipants
	// answers ListBanks, so every bank in the network is in it — founded ones
	// included, each carrying its status. The routing directory this institution
	// genuinely owns is roster_entries, written by payment.AdmitMemberTx from the
	// settlement agent's acknowledgement, and it is on no surface at all; the mesh
	// reads it and nothing in this package does. What puts the bank list on this
	// listener is that this is the console the network's membership is watched
	// from, and watching a bank become a member needs the banks that are not one
	// yet.
	//
	// Founding is above, on the central bank's surface. Admission itself is no
	// single institution's act, and this one's share of it is to relay a bank's
	// application, refuse an address it has already admitted somebody else to, and
	// write that routing entry from the answer (mesh.csm).
	mux.HandleFunc("GET /members", s.handleListParticipants)
	mux.HandleFunc("GET /schemes", s.handleListSchemes)
	mux.HandleFunc("GET /directory", s.handleResolveIdentifier)
	mux.HandleFunc("GET /assets", s.handleListAssets)
	mux.HandleFunc("GET /payments/audit", s.handlePaymentAudit)
	s.registerPaymentRoutes(mux)
	return mux
}

func (s *Server) bankRouter() *router {
	mux := newRouter()
	// GET /me is GET /participants/{pid} with the question already answered.
	// The handler is the same one: it resolves through s.participant, which on
	// this listener reads the bound identity instead of the path.
	mux.HandleFunc("GET /me", s.handleGetParticipant)
	mux.HandleFunc("GET /assets", s.handleListAssets)
	// A bank is a scheme participant with directory access, and validating a
	// payee's address before accepting an instruction is what it uses that for.
	// The alternative — a customer's browser querying the CSM — would give a
	// retail app a clearing-house connection no retail app has.
	mux.HandleFunc("GET /directory", s.handleResolveIdentifier)
	mux.HandleFunc("POST /deposits", s.handleFundDeposit)
	mux.HandleFunc("GET /audit", s.handleLedgerAudit)
	mux.HandleFunc("GET /deposit-audit", s.handleDepositAudit)
	// A bank's own legs. The clearing house serves the same two patterns
	// unnarrowed, which is the split doing its job rather than a collision.
	mux.HandleFunc("GET /payments", s.handleListBankPayments)
	mux.HandleFunc("GET /payments/{payid}", s.handleGetBankPayment)
	// Where a customer's instruction lands. Never the clearing house: a
	// retail client has no CSM connection in the real thing either.
	mux.HandleFunc("POST /payments", s.handleSubmitPayment)

	s.registerLedgerRoutes(mux)
	s.registerDepositRoutes(mux)
	s.registerProductRoutes(mux)
	s.registerLendingRoutes(mux)
	s.registerBankIdentifierRoutes(mux)
	return mux
}
