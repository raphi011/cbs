package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// Routes is one member bank's surface, bound to its identity.
func Routes(inst Institution) *api.Router {
	s := &surface{inst: inst}
	mux := api.NewRouter()
	// GET /me is GET /participants/{pid} with the question already answered.
	// The handler is the same one: it resolves through s.participant, which on
	// this listener reads the bound identity instead of the path.
	mux.HandleFunc("GET /me", handle(s, http.StatusOK, s.handleGetParticipant))
	mux.HandleFunc("GET /assets", api.HandleListAssets)
	// TWO DIRECTORIES, and a bank owns one of them.
	mux.HandleFunc("GET /directory/accounts", api.Handle(http.StatusOK, s.handleResolveIdentifier))
	mux.HandleFunc("GET /directory/banks", api.Handle(http.StatusOK, s.handleResolveBankCode))
	// The subscription.
	mux.HandleFunc("POST /directory/banks/refresh", api.Handle(http.StatusOK, s.handleRefreshDirectory))
	mux.HandleFunc("POST /deposits", handleBody(s, http.StatusOK, s.handleFundDeposit))
	// The BOOK TRANSFER: two of this bank's own customers, one posting, no scheme.
	mux.HandleFunc("POST /transfers", handleBody(s, http.StatusOK, s.handleTransfer))
	// The other half of taking cash in. Cash in is one institution's act and lands
	// in this bank's vault; moving it onto reserve is a conversation with the
	// central bank, so it is a second request and answers 202.
	mux.HandleFunc("POST /lodgements", handleBody(s, http.StatusAccepted, s.handleLodgeReserves))
	mux.HandleFunc("GET /audit", handle(s, http.StatusOK, s.handleLedgerAudit))
	mux.HandleFunc("GET /deposit-audit", handle(s, http.StatusOK, s.handleDepositAudit))
	// What this bank sent and received, and one file at a time. The listing is an
	// index and carries no bytes; the second route is where a document is read.
	mux.HandleFunc("GET /messages", api.Handle(http.StatusOK, s.handleListMessages))
	mux.HandleFunc("GET /messages/{seq}", api.Handle(http.StatusOK, s.handleGetMessage))
	// A bank's own legs. The clearing house serves the same two patterns
	// unnarrowed, which is the split doing its job rather than a collision.
	mux.HandleFunc("GET /payments", api.Handle(http.StatusOK, s.handleListBankPayments))
	// This bank's own payment-scope log — its founding, its membership, its
	// mandates, its side of every payment.
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
