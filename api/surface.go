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
	// Admission opens the member's reserve and settlement accounts in the
	// central bank's own book, which is what makes it the central bank's act
	// and not the clearing house's.
	mux.HandleFunc("POST /members", s.handleAddParticipant)
	// Settling is the central bank's act; the clearing house keeps the
	// read side, because it needs to know whether the cycle it closed has
	// settled and reading is not doing.
	mux.HandleFunc("POST /settlements", s.handleSettleCycle)
	mux.HandleFunc("GET /assets", s.handleListAssets)
	// Reset clears the store and reseeds it. It belongs to one operator
	// because Server.resetMu serializes it per process, and the central bank
	// is the operator whose act "the system starts with these banks in it" is.
	s.registerAdminRoutes(mux)
	return mux
}

func (s *Server) clearingHouseRouter() *router {
	mux := newRouter()
	// The roster is the clearing house's because routing a payment is what
	// needs to know who is reachable. Admission is the central bank's, above.
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

	s.registerLedgerRoutes(mux)
	s.registerDepositRoutes(mux)
	s.registerProductRoutes(mux)
	s.registerLendingRoutes(mux)
	s.registerBankIdentifierRoutes(mux)
	return mux
}
