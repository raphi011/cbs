package mesh

import "github.com/raphi011/cbs/payment"

// The three narrowed views of *payment.Network, one per kind of actor.
//
// Each actor holds one of these rather than the whole *payment.Network, so a
// bank handler that calls SettleCycleTx does not COMPILE. That is cheap now and
// it is exactly the seam sub-project 8 needs: when each entity gets its own
// store, these interfaces are already the list of what each one may reach.
//
// # Two mechanisms, because neither alone is enough
//
// These narrow by METHOD. They cannot narrow by BOOK — a bank handler still
// could not be stopped from reading another bank's ledger through a method it
// legitimately holds, because every ledger.Tx method takes the book as an
// ordinary argument and any BookID is as valid as any other. That is what the
// recorder in books_test.go is for, and it is why the debtor-half/creditor-half
// split in payment is load-bearing rather than tidy.
//
// Conversely the recorder cannot do what these do: it observes a run, so it can
// only report a crossing that some test actually provoked, whereas an interface
// that lacks the method makes the crossing unwritable. Method and book, static
// and dynamic — one of each, because each is blind exactly where the other sees.
//
// # Why they are empty
//
// They are declared empty ON PURPOSE and grow method by method as Tasks 8-13
// discover what each handler needs. An interface written ahead of its callers is
// a guess, and a guess here is a wrong boundary that then looks authoritative —
// the worst of both, since every later reader takes it for a decision.
//
// The honest consequence, stated rather than glossed: while they are empty they
// constrain nothing, and the compile-time boundary is not real until Task 13 has
// filled them in. The recorder is the mechanism that bites in the meantime.
type bankOps interface {
	// Grown by Tasks 10, 11 and 13, as the bank handlers discover what they
	// need. Deliberately empty until then.
}

// csmOps is the clearing house's view: what a CSM handler may reach.
//
// The comment on bankOps applies to all three — empty on purpose, grown by the
// task that first needs a method.
type csmOps interface {
	// Grown by Tasks 10, 11 and 12.
}

// settlementOps is the central bank's view: what a settlement handler may
// reach.
//
// The comment on bankOps applies to all three — empty on purpose, grown by the
// task that first needs a method.
type settlementOps interface {
	// Grown by Tasks 12 and 13.
}

// *payment.Network satisfies all three today, and these assertions are what keep
// that true: a method added to one of the interfaces above that the Network does
// not have fails the build here rather than at the handler that wanted it.
//
// They assert nothing while the interfaces are empty. That is not an argument
// for leaving them out — they cost one line each and they are the check that
// starts working the moment Task 10 adds the first method.
var (
	_ bankOps       = (*payment.Network)(nil)
	_ csmOps        = (*payment.Network)(nil)
	_ settlementOps = (*payment.Network)(nil)
)
