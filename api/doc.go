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
// What SATISFIES those three interfaces is not here either: the institutions are
// this deployment's, in cmd/server, and they satisfy them structurally. So
// nothing in the HTTP layer names a transport, a store set or a composition
// root, and what a handler can reach is exactly what its own package asked for.
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
// # No business logic anywhere in the three, and no writing either
//
// A handler calls the domain methods and RETURNS a response DTO and an error;
// Handle is what decodes, maps the error, chooses the status and encodes. The
// writers are unexported, so that is not a convention the three keep — it is the
// only way they can answer at all. Domain sentinel errors become HTTP status
// codes in errorStatus, here, once.
//
// The DTO layer renders the domain's integer enums as strings and keeps monetary
// amounts as integer minor units, and it does no I/O: a To…DTO is a pure
// function of the values handed to it. What a response needs a read for is in
// transaction.go and audit.go.
//
// It is built entirely on the standard library (net/http with the Go 1.22+
// method+path ServeMux patterns), keeping the module dependency-free.
package api
