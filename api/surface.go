package api

import (
	"context"
	"net/http"

	"github.com/raphi011/cbs/payment"
)

// The three operator surfaces.
//
// One binary, one process by default, one listener per entity: one per bank —
// every bank, founded or admitted — one for the central bank, one for the
// clearing house. What a caller can
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
	cb := s.as(s.nets.CentralBank())
	return cb.withMiddleware(cb.centralBankRouter().mux)
}

// ClearingHouseRoutes is the CSM's surface: every payment in the network, the
// clearing cycles, the schemes, the mandates and the directory.
func (s *Server) ClearingHouseRoutes() http.Handler {
	ch := s.as(s.nets.ClearingHouse())
	return ch.withMiddleware(ch.clearingHouseRouter().mux)
}

// BankRoutes is one member bank's surface, bound to its identity.
//
// It takes a context and can fail where the two institutions' cannot, because it
// opens that bank's own database — the two institutions' were opened before any
// bank existed. See payment.Stores.
func (s *Server) BankRoutes(ctx context.Context, pid payment.ParticipantID) (http.Handler, error) {
	b, err := s.forBank(ctx, pid)
	if err != nil {
		return nil, err
	}
	return b.withMiddleware(b.bankRouter().mux), nil
}

func (s *Server) centralBankRouter() *router {
	mux := newRouter()
	mux.HandleFunc("GET /reserves", s.handleListReserves)
	mux.HandleFunc("GET /reserves/{bic}", s.handleGetReserve)
	mux.HandleFunc("GET /audit", s.handleCentralBankAudit)
	// Every bank this deployment holds a database for. It is here rather than at
	// the clearing house because the clearing house has no banks table — the csm shape
	// holds a roster and nothing else, and a roster deliberately omits the founded
	// bank this listing exists to show (see handleListParticipants).
	//
	// # These two routes are the OPERATOR's, not the settlement agent's
	//
	// Which is worth stating plainly, because everything else on this listener is
	// bound to one institution and these are not. Server.as binds the central
	// bank's Network to this router and Server's doc says nothing reachable from a
	// bound Server can act as anybody but the entity it was bound to; both of these
	// reach past that binding, through s.nets, to institutions that are not this
	// one.
	//
	// The POST is an exception to that: it founds a bank, which CREATES AND WRITES
	// that bank's entire database — its book, its chart, its accounts, its product
	// — through the mesh.
	//
	// What justifies it is the same thing that justifies POST /admin/reset sitting
	// here: founding the banks a network starts with, listing them, and rebuilding
	// them are one operator's acts over a DEPLOYMENT, and a deployment is not an
	// institution. The listener is where the operator's console is served; it is
	// not the claim that the settlement agent is performing them.
	mux.HandleFunc("GET /members", s.handleListParticipants)
	// There is no POST here, and its absence is the shape of what the mesh
	// changed. A settlement is performed on INSTRUCTION: the clearing house reaches
	// a cut-off, sends a pacs.009, and the central bank's actor answers ACSC or
	// RJCT/AM04 (mesh.centralBank). A route that let a human do it beside that
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
	// GET /members was here and is not. It answered ListBanks — every bank in the
	// network, founded ones included, each carrying its status — and the comment
	// that stood here argued for the placement: this is the console the network's
	// membership is watched from, and watching a bank become a member needs the
	// banks that are not one yet.
	//
	// The clearing house has a database with no banks table in it; what it holds is
	// roster_entries, written by payment.AdmitMemberTx from the settlement agent's
	// acknowledgement, and a roster is exactly the list that omits a founded bank.
	// So the two are separate: the roster is below, on GET /roster, and it is this
	// institution's own answer; the bank list is on the central bank's listener
	// beside the route that founds them, where it is the operator's read rather
	// than an institution's.
	//
	// This institution's share of admission is unchanged: relay a bank's
	// application, refuse an address it has already admitted somebody else to, and
	// write that routing entry from the answer (mesh.csm).
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
	// has a lookup in that bank's own register (api/handlers_directory.go); what a
	// clearing house has always actually had is the roster.
	mux.HandleFunc("GET /roster", s.handleListRoster)
	mux.HandleFunc("GET /assets", s.handleListAssets)
	mux.HandleFunc("GET /payments/audit", s.handlePaymentAudit)
	// The MANDATES are not here. A mandate is the creditor bank's own row — in
	// SEPA the creditor holds it, and the bank that checks one at submission is the
	// creditor's (payment.SDD.ValidateMandate) — so this console would hold every
	// member's authorisations over every other member's customers' accounts, and
	// render each by reading the DEBTOR bank's deposit register for its asset. The
	// csm shape has no deposit register. See registerMandateRoutes on the bank's
	// surface below, and payment.Mandate, which carries its own asset.
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
	// TWO DIRECTORIES, and a bank owns one of them.
	//
	// The first resolves an address in this bank's OWN register: which of this
	// bank's accounts holds it. A customer's own IBAN, an on-us payee, an operator
	// checking an address before issuing another one. A payee at ANOTHER bank is
	// not something this bank can confirm, and the honest answer is a not-found —
	// see payment.ResolveIdentifier.
	//
	// The second answers where to SEND, which is the question the first cannot
	// reach and which every payment out of here needs: an IBAN carries a bank
	// code, no arithmetic turns one into a BIC, so the pairing has to be
	// published and copied. What is under it is this bank's own snapshot of the
	// clearing house's roster, pulled by the refresh below and possibly behind.
	//
	// They share a prefix because they are the same act seen from two sides — an
	// address being turned into somewhere — and they are separate paths because
	// only one of them is answered out of a table this institution owns.
	mux.HandleFunc("GET /directory/accounts", s.handleResolveIdentifier)
	mux.HandleFunc("GET /directory/banks", s.handleResolveBankCode)
	// The subscription. It is a POST because it writes this bank's copy, and it is
	// on the BANK's port because subscribing is the subscriber's act: no route on
	// the clearing house's port pushes anything at anybody, which is what keeps
	// that institution a publisher rather than a delivery system.
	mux.HandleFunc("POST /directory/banks/refresh", s.handleRefreshDirectory)
	mux.HandleFunc("POST /deposits", s.handleFundDeposit)
	// The BOOK TRANSFER: two of this bank's own customers, one posting, no
	// scheme. It is beside POST /deposits and not under /payments because it is
	// the same kind of act — this institution's alone, finished when it returns
	// — and it is the remedy POST /payments names when it refuses an on-us
	// instruction. See handleTransfer.
	mux.HandleFunc("POST /transfers", s.handleTransfer)
	// The other half of taking cash in. Cash in is one institution's act and lands
	// in this bank's vault; moving it onto reserve is a conversation with the
	// central bank, so it is a second request and answers 202. See
	// handleLodgeReserves.
	mux.HandleFunc("POST /lodgements", s.handleLodgeReserves)
	mux.HandleFunc("GET /audit", s.handleLedgerAudit)
	mux.HandleFunc("GET /deposit-audit", s.handleDepositAudit)
	// A bank's own legs. The clearing house serves the same two patterns
	// unnarrowed, which is the split doing its job rather than a collision.
	mux.HandleFunc("GET /payments", s.handleListBankPayments)
	// This bank's own payment-scope log — its founding, its membership, its
	// mandates, its side of every payment. It shares a path prefix with the
	// route below and wins it, because a literal segment beats a wildcard in
	// the mux's own precedence rules. See handleBankPaymentAudit.
	mux.HandleFunc("GET /payments/audit", s.handleBankPaymentAudit)
	mux.HandleFunc("GET /payments/{payid}", s.handleGetBankPayment)
	// Where a customer's instruction lands. Never the clearing house: a
	// retail client has no CSM connection in the real thing either.
	mux.HandleFunc("POST /payments", s.handleSubmitPayment)

	// A creditor's mandates: the standing authorisations this bank's own
	// customers hold over other banks' customers' accounts. See
	// registerMandateRoutes.
	s.registerMandateRoutes(mux)

	// A bank checking its own books, and the only operator that can: the
	// reconciliation reads one database — this one — and the statements it was
	// sent. See registerReconciliationRoutes.
	s.registerReconciliationRoutes(mux)

	s.registerLedgerRoutes(mux)
	s.registerDepositRoutes(mux)
	s.registerProductRoutes(mux)
	s.registerLendingRoutes(mux)
	s.registerBankIdentifierRoutes(mux)
	return mux
}
