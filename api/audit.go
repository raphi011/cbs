package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/raphi011/cbs/ledger"
)

// The audit plumbing, shared by all three surfaces because there is one audit
// endpoint with several filters: the log is a single table spanning every book
// and every layer, and a route is just a (BookID, Scope) pair applied to it.
//
// It is here rather than three times over — a bank's ledger and deposit trails,
// the clearing house's payment trail, the central bank's own book — because
// three copies of the query parameters and the limit cap would be three
// versions of them.

// Pagination bounds shared by every audit endpoint.
const (
	auditDefaultLimit = 100
	auditMaxLimit     = 1000
)

// AuditFilterFrom parses the shared audit query parameters. A durable log is
// unbounded, so limit is defaulted and capped rather than optional.
//
// book and scope come from the route, never from the client: they are what
// distinguishes one audit endpoint from another. Both matter to `before` too —
// Seq is a store-global total order, not a per-book or per-scope counter, so a
// cursor only means "the next page" when it is replayed against the filter that
// produced it. The store applies Before as one predicate among the rest and
// takes Limit last; see ledger.AuditFilter.
func AuditFilterFrom(r *http.Request, book ledger.BookID, scope ledger.Scope) ledger.AuditFilter {
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

// AuditReader is the one method this file needs, and it is named here rather
// than taking an institution's network because all three kinds have an audit
// trail and no two of them are the same type. Reading one's own events is the
// rare act that is every institution's.
type AuditReader interface {
	ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error)
}

// AuditPage runs one filter against one institution's network and renders the
// page. Every audit route on every surface ends here, so all of them share the
// ordering, the DTO and the empty-page shape.
func AuditPage(ctx context.Context, net AuditReader, f ledger.AuditFilter) ([]AuditEventDTO, error) {
	events, err := net.ListAudit(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]AuditEventDTO, len(events))
	for i, e := range events {
		out[i] = ToAuditDTO(e)
	}
	return out, nil
}
