// Package api is the HTTP layer's shared plumbing: the router, the middleware
// chain, the error mapping, the response writers, and every DTO the wire format
// is made of.
//
// # It serves nothing, and that is the shape
//
// The handlers are in api/bank, api/csm and api/centralbank, one package per
// institution. Each declares the interface IT is driven by — see
// bank.Institution — and this package imports none of the three. The dependency
// runs one way, so a DTO or an error mapping cannot come to depend on which
// operator is asking.
//
// What this package also holds is the Deployment: every institution the process
// has, and the three bound values (Bank, ClearingHouse, CentralBank) that
// satisfy the three surfaces' interfaces. Each is bound at construction and
// never rebound, which is what makes "a listener acts as exactly one
// institution" a fact about the types rather than a convention.
//
// # One listener per entity, and the port is the claim
//
// One binary, one process by default, one listener per entity: one per bank —
// every bank, founded or admitted — one for the central bank, one for the
// clearing house. What a caller can reach is decided by which port they are
// talking to, and a bank's routes have nowhere to name another bank, which is
// why a bank's handlers read the identity off their Institution rather than out
// of the path.
//
// This is scoping, not authorization. Nothing verifies that the caller on a
// bank's port is that bank; the port is the claim. What it removes is the
// ability to reach another operator's data by editing a URL, because that URL
// does not exist on the port you are talking to.
//
// # No business logic anywhere in the three
//
// Handlers decode request DTOs, call the domain methods, and encode response
// DTOs; the DTO layer renders the domain's integer enums as strings and keeps
// monetary amounts as integer minor units. Domain sentinel errors are mapped to
// HTTP status codes by errorStatus, here, once.
//
// It is built entirely on the standard library (net/http with the Go 1.22+
// method+path ServeMux patterns), keeping the module dependency-free.
package api
