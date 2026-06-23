// Package api exposes the core-banking library over a JSON/HTTP REST interface.
//
// The payment.Network is the application root: each participant bank owns its
// own ledger.Book and deposit.Register, so ledger and deposit operations are
// routed under a participant (/participants/{pid}/...), while mandates,
// payments, clearing cycles, settlements, and the central bank are
// network-level resources.
//
// The package contains no business logic. Handlers decode request DTOs, call
// the domain methods, and encode response DTOs; the DTO layer renders the
// domain's integer enums as strings and keeps monetary amounts as integer minor
// units. Domain sentinel errors are mapped to HTTP status codes by errorStatus.
//
// It is built entirely on the standard library (net/http with the Go 1.22+
// method+path ServeMux patterns), keeping the module dependency-free.
package api
