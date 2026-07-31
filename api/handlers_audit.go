package api

import (
	"net/http"
	"strconv"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The four audit endpoints live together because they are one endpoint with
// four filters: the log is a single table spanning every book and every layer,
// and a route is just a (BookID, Scope) pair applied to it. Keeping them in one
// file is what makes the query parameters — and the limit cap — impossible to
// implement three-and-a-bit ways.

// Pagination bounds shared by every audit endpoint.
const (
	auditDefaultLimit = 100
	auditMaxLimit     = 1000
)

// auditFilter parses the shared audit query parameters. A durable log is
// unbounded, so limit is defaulted and capped rather than optional.
//
// book and scope come from the route, never from the client: they are what
// distinguishes one audit endpoint from another. Both matter to `before` too —
// Seq is a store-global total order, not a per-book or per-scope counter, so a
// cursor only means "the next page" when it is replayed against the filter that
// produced it. The store applies Before as one predicate among the rest and
// takes Limit last; see ledger.AuditFilter.
func auditFilter(r *http.Request, book ledger.BookID, scope ledger.Scope) ledger.AuditFilter {
	f := ledger.AuditFilter{
		BookID:   book,
		Scope:    scope,
		Type:     r.URL.Query().Get("type"),
		EntityID: r.URL.Query().Get("entity"),
		Limit:    auditDefaultLimit,
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		f.Limit = min(v, auditMaxLimit)
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil && v > 0 {
		f.Before = v
	}
	return f
}

// writeAudit runs one filter and renders the page. Every audit route ends here,
// so all four share the ordering, the DTO and the empty-page shape.
func (s *Server) writeAudit(w http.ResponseWriter, r *http.Request, f ledger.AuditFilter) {
	events, err := s.network().ListAudit(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]auditEventDTO, len(events))
	for i, e := range events {
		out[i] = toAuditDTO(e)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLedgerAudit serves one bank's general-ledger audit trail.
func (s *Server) handleLedgerAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	s.writeAudit(w, r, auditFilter(r, p.BookID, ledger.ScopeLedger))
}

// handleDepositAudit serves one bank's deposit-layer audit trail. Same book as
// the ledger trail above; only the scope differs.
func (s *Server) handleDepositAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	s.writeAudit(w, r, auditFilter(r, p.BookID, ledger.ScopeDeposit))
}

// handleCentralBankAudit serves the central bank's own book — a real chart of
// accounts, so its events are ledger-scoped like any other bank's.
func (s *Server) handleCentralBankAudit(w http.ResponseWriter, r *http.Request) {
	s.writeAudit(w, r, auditFilter(r, payment.CentralBankBook, ledger.ScopeLedger))
}

// handlePaymentAudit serves the network's own trail: participants, mandates,
// payments and clearing cycles, which belong to no single bank and so live
// under ledger.NetworkBook.
func (s *Server) handlePaymentAudit(w http.ResponseWriter, r *http.Request) {
	s.writeAudit(w, r, auditFilter(r, ledger.NetworkBook, ledger.ScopePayment))
}
