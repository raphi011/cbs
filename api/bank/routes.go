package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// Routes is one member bank's surface, bound to its identity.
//
// It hands back the router rather than a handler so a caller can read the
// patterns off it — see api.Router — and so the middleware chain is one call
// (Router.Handler) that no surface package has to remember.
//
// Every route registers through handle, handleBody or their api counterparts,
// which is where a response is written; the status beside each pattern is the
// one a body gets, and this file is where it is chosen.
func Routes(inst Institution) *api.Router {
	s := &surface{inst: inst}
	mux := api.NewRouter()
	// GET /me is GET /participants/{pid} with the question already answered.
	// The handler is the same one: it resolves through s.participant, which on
	// this listener reads the bound identity instead of the path.
	mux.HandleFunc("GET /me", handle(s, http.StatusOK, s.handleGetParticipant))
	mux.HandleFunc("GET /assets", api.HandleListAssets)
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
	mux.HandleFunc("GET /directory/accounts", api.Handle(http.StatusOK, s.handleResolveIdentifier))
	mux.HandleFunc("GET /directory/banks", api.Handle(http.StatusOK, s.handleResolveBankCode))
	// The subscription. It is a POST because it writes this bank's copy, and it is
	// on the BANK's port because subscribing is the subscriber's act: no route on
	// the clearing house's port pushes anything at anybody, which is what keeps
	// that institution a publisher rather than a delivery system.
	mux.HandleFunc("POST /directory/banks/refresh", api.Handle(http.StatusOK, s.handleRefreshDirectory))
	mux.HandleFunc("POST /deposits", handleBody(s, http.StatusOK, s.handleFundDeposit))
	// The BOOK TRANSFER: two of this bank's own customers, one posting, no
	// scheme. It is beside POST /deposits and not under /payments because it is
	// the same kind of act — this institution's alone, finished when it returns
	// — and it is the remedy POST /payments names when it refuses an on-us
	// instruction. See handleTransfer.
	mux.HandleFunc("POST /transfers", handleBody(s, http.StatusOK, s.handleTransfer))
	// The other half of taking cash in. Cash in is one institution's act and lands
	// in this bank's vault; moving it onto reserve is a conversation with the
	// central bank, so it is a second request and answers 202. See
	// handleLodgeReserves.
	mux.HandleFunc("POST /lodgements", handleBody(s, http.StatusAccepted, s.handleLodgeReserves))
	mux.HandleFunc("GET /audit", handle(s, http.StatusOK, s.handleLedgerAudit))
	mux.HandleFunc("GET /deposit-audit", handle(s, http.StatusOK, s.handleDepositAudit))
	// A bank's own legs. The clearing house serves the same two patterns
	// unnarrowed, which is the split doing its job rather than a collision.
	mux.HandleFunc("GET /payments", api.Handle(http.StatusOK, s.handleListBankPayments))
	// This bank's own payment-scope log — its founding, its membership, its
	// mandates, its side of every payment. It shares a path prefix with the
	// route below and wins it, because a literal segment beats a wildcard in
	// the mux's own precedence rules. See handleBankPaymentAudit.
	mux.HandleFunc("GET /payments/audit", handle(s, http.StatusOK, s.handleBankPaymentAudit))
	// The hub: what this bank has taken and not yet sent, and the act that sends
	// it. Both are literal segments and beat the wildcard above them, by the same
	// precedence rule /payments/audit relies on.
	mux.HandleFunc("GET /payments/pending", api.Handle(http.StatusOK, s.handleListPendingPayments))
	mux.HandleFunc("POST /payments/cutoff", api.Handle(http.StatusAccepted, s.handleCutoff))
	mux.HandleFunc("GET /payments/{payid}", api.Handle(http.StatusOK, s.handleGetBankPayment))
	// Where a customer's instruction lands. Never the clearing house: a
	// retail client has no CSM connection in the real thing either.
	mux.HandleFunc("POST /payments", api.HandleBody(http.StatusAccepted, s.handleSubmitPayment))

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
