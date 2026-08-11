// Command server runs the core-banking REST API.
//
// It opens a store, builds a payment.Network over it, seeds it with a
// comprehensive sample dataset (multiple banks, accounts, payments, clearing
// cycles and settlements) and serves it over HTTP. It can also be reset to the
// sample dataset at runtime via POST /admin/reset. This is a learning and
// prototyping tool, not a production service.
//
// # Which stores
//
// Without DATABASE_URL (or -database) the server runs on ephemeral SQLite
// databases: zero setup, and every restart starts from the seeded scenario
// again. With one, -database is a DIRECTORY and the data outlives the process.
//
// A DIRECTORY, because there is no one database to name. Each institution holds
// its own — the clearing house's, the central bank's, and one per member bank
// named by that bank's BIC — so the flag names the place they live rather than
// the file. The set of banks is the set of files in it, which is why a restart
// needs no counter and no registry: see store/sqlite.Set.
//
// Neither the server nor the developer needs a database server — nothing in
// `make dev`, `make run` or `go test ./...` ever did, and now nothing anywhere
// does.
//
// Seeding is idempotent, so the same wiring serves both: Populate creates the
// scenario against an empty store and returns without touching a populated one,
// which is what makes a restart against a file a no-op rather than a second copy
// of every bank.
//
// # The interbank network
//
// The three institutions and the flows between them are here, in bank.go,
// csm.go, centralbank.go and ops.go, with the composition root in mesh.go and
// deployment.go. They are this deployment's orchestration rather than a library
// some other system could use, which is what puts them in a command.
//
// It exists to make the message the interface. A bank does not learn the fate of
// a payment by calling a function that returns it; no actor here is TOLD that a
// payment was accepted except by receiving a pacs.002 saying so. Everything else
// below follows from taking that seriously.
//
// The DELIVERY underneath — inboxes, goroutines, the in-flight counter, Drain
// and the dead letters — is mesh/wire, which carries bytes and knows nothing
// about what any of them say. What it guarantees and what it deliberately does
// not is stated there, once; this doc names it where it bears on a flow and does
// not restate it.
//
// Every institution has a database of its own, so there is no row a bank could
// read to learn what a counterparty decided, and a handler reaching for one is
// refused by name rather than answered. A bank still reads its own copy —
// bank.receiveStatus does, and trusts it over the message it just received — but
// that copy is a record of what this bank was told. See "Five databases,
// likewise" below.
// # Actors
//
// A mesh owns two institutions and one actor per bank it has been told about.
// Each is a goroutine with an unbounded inbox, and only that goroutine ever runs
// that actor's handler — which is what makes a bank's own state safe to touch
// inside a handler, and what makes "which institution did this" a question with
// an answer.
//
// Which banks those are depends on how the mesh learned them, and the two
// answers differ. joinRoster — what Start and Reset run — reads the CLEARING
// HOUSE's roster, so a bank whose provisioning never finished comes back from a
// restart with NO actor, and only members are addressable. AddBank takes one bank
// at a time and asks nothing about membership, so a deployment can give an actor
// to a bank the roster does not name. Both are deliberate and they are not the
// same rule; what stops such a bank being paid is payment.ErrBankNotAdmitted, at
// the door and at the clearing house, and not the actor table. The two
// institutions have no store row at all and are named in Config.
//
// # The flows, listed once
//
// A push, a pull, a cut-off and a return — the sections below, in that order.
// They are listed here and counted nowhere: what each flow IS does not change
// when another arrives.
//
// # Bytes, not structs
//
// Mesh.send takes an iso20022.Envelope, marshals it, and hands the BYTES to the
// transport, which is the whole of what the transport carries. Nothing but bytes
// crosses an actor boundary. If two actors exchanged a *Pacs008 the message
// format would be decoration on a function call: malformed input would stop
// being a reachable failure mode, and FF01 — the rejection a receiver sends when
// it cannot parse what it was given — would be untestable. Because the bytes
// really are parsed on arrival, it is testable, and the inbox carries the sender
// beside them so that a message whose header cannot be read can still be
// answered.
//
// That split is the seam between the two packages: marshalling is the mesh's
// last act, and everything past it is delivery.
//
// # The credit transfer flow
//
// A SEPA credit transfer is four messages and three decisions, and no two of
// them are made by the same institution:
//
//	payer's bank  --pacs.008-->  clearing house  --pacs.008-->  payee's bank
//	payer's bank  <--pacs.002--  clearing house  <--pacs.002--  payee's bank
//
// Reading that clockwise:
//
//   - The PAYER'S BANK submits. Mesh.Submit runs that half synchronously, so a
//     customer whose instruction fails their own bank's checks is told then and
//     there — and then sends, after the unit of work has committed. What it does
//     not answer is what the far side thinks; that arrives later, as a message.
//   - The CLEARING HOUSE routes the instruction on, by the creditor agent named
//     in it. It reads no store to do that: a clearing house that had to look a
//     payment up to decide where to send it could not route a message about a
//     payment it does not hold, which in a real network is every message.
//   - The PAYEE'S BANK resolves the message by ADDRESS — that is what produces
//     AC01 for an account number nobody holds — checks its own half, and answers
//     ACCP or RJCT.
//   - The CLEARING HOUSE clears the answer: an acceptance goes into the open
//     cycle for its scheme, and a payment with no window open is refused with
//     TM01, because the cut-off is the clearing house's and no bank refuses its
//     own customer's instruction on account of it. Then it tells the payer's
//     bank.
//   - The PAYER'S BANK, on a rejection, gives the payer their money back.
//
// Which institution runs which half is decided entirely by whose inbox the
// message landed in. That, rather than the XML, is what the mesh is for.
//
// A rejection therefore HALF-HAPPENS for an interval: the clearing house has
// rejected the payment and the payer's money is still in their own bank's
// clearing suspense, until a second actor, in a second unit of work, reverses
// it. payment.RejectAtCSMTx documents that seam; the mesh does not hide it — a
// reversal that fails has nobody to answer, so it becomes a dead letter and
// Drain returns it.
//
// # The direct debit flow
//
// A SEPA direct debit is the same four messages and the same three decisions,
// made by the same institutions in the same order — with the two banks swapped
// and the money moving at a different point in the chain:
//
//	payee's bank  --pacs.003-->  clearing house  --pacs.003-->  payer's bank
//	payee's bank  <--pacs.002--  clearing house  <--pacs.002--  payer's bank
//
// The PAYEE'S BANK submits, because a collection is the payee asking for what it
// is owed, and its submission MOVES NOTHING: the account being collected from is
// another bank's customer's, and this bank has never seen it. What it checks is
// its own half — the payee's account — plus the MANDATE, because in SEPA the
// creditor holds the mandate. A revoked one is refused there and then, at the
// payee's own bank, as an error to the caller and not as a message; MD01 is
// therefore a code this system never REJECTS with, and a real payer's bank
// keeps mandate records of its own and can. It does reach the wire the other
// way — iso20022.ReturnReasonNoMandate is the same MD01 in the return code set,
// sent by a debtor's bank about a settled collection its customer disputes, and
// the two are separate Go types for exactly this reason.
// payment.SDD.ValidateMandate says so at the layer that owns the limit.
//
// The CLEARING HOUSE routes it to the DEBTOR's agent, the element that names the
// bank holding the payer, because a pull travels towards the money's source
// while a push travels towards its destination. That one element is the whole
// difference in this actor.
//
// The PAYER'S BANK receives it, and this is the half that moves money. It
// resolves by address exactly as a payee's bank does on a push, and then posts
// the debtor leg — because this is the first moment any actor in the system has
// been able to look at the account being collected from. AM04, an account that
// cannot cover the amount, can only ever be said here.
//
// The rule that covers both flows, and that neither one on its own would tell
// you, is that the DEBTOR's bank posts the debtor leg. Which bank that is stays
// the same; whether it is submitting or answering is what the direction decides.
//
// One consequence is worth stating because it has no push counterpart. When the
// clearing house rejects a collection the payer's bank has already accepted, the
// bank waiting for an answer and the bank holding the money are two DIFFERENT
// banks — so the rejection goes to both, and the condition for the second is
// exactly that there is money to give back. See csm's receiveStatus.
//
// # Settlement
//
// The third institution, and the only one that moves reserves. A cut-off is two
// messages between the institutions, one STATEMENT out to each member whose
// position moved, and then one status per payment out to the banks:
//
//	clearing house  --pacs.009-->  central bank
//	central bank    --camt.053-->  each member whose net position moved
//	clearing house  <--pacs.002--  central bank
//	clearing house  --pacs.002-->  the submitter and the payee's bank, per payment
//
// Those two fan-outs are what a member's half of a cut-off is made of, and
// between them nothing of it is left in the settlement agent's unit of work: the
// camt.053 carries the MIRROR leg and the per-payment ACSC carries the CREDITOR
// leg (bank.receiveStatus, payment.SettleAtBankTx).
//
// The camt.053 is what makes the MIRROR LEG the member's own act. A bank's
// clearing suspense holds money that has left a customer and not yet settled
// between banks; the mirror leg is that suspense moving against the bank's
// reserve, and it is a posting in the bank's own ledger. The settlement agent
// states the movement and the closing balance, and the member books it
// (bank.receiveStatement, payment.PostSettlementAdviceTx).
//
// A statement is not an instruction, so it is ANSWERED BY NOTHING: the central
// bank has already settled, and there is nothing left to accept or refuse. A
// member that cannot book what it was told produces a dead letter and NO advice
// row, because the row commits with the leg — so the unreconciled position is a
// clearing suspense that has not returned to zero with no advice row against the
// cycle, which looks the same in the store as never having been told.
//
// It goes out BEFORE the answer, and that ordering is load-bearing: the CREDITOR
// leg is posted from the clearing house's ACSC fan-out, which is derived from
// the answer, so sending the statement first puts it in the member's inbox
// before the ACSC is even built. It is a happens-before chain rather than a
// race, because a send pushes onto the target's queue synchronously and one
// goroutine pops each queue in order. A RETURN has the identical requirement one
// hop longer. See centralBank.advise for which bank it bears on and what the
// other order would say about that bank's balance sheet, and
// TestTheMessagesACutOffPutsOnTheWire and TestTheMessagesAReturnPutsOnTheWire,
// which assert the pairs.
// # What an undelivered statement suppresses
// centralBank.advise returns on the FIRST send it cannot make, and the cost is
// wider than the bank it could not reach: that member is never advised, every
// member after it in the statement order is never advised either, and the ACSC
// is never sent — so EVERY bank in the cycle, including the ones that were
// advised, is left holding an instruction it believes outstanding on a payment
// already marked Settled. On a RETURN the third costs more, because the clearing
// house's answer is what releases the pacs.004 it is holding for the other bank:
// a customer leg never posted, against reserves that are final.
//
// None of it is reachable here, for the reason "What this mesh is not" gives
// below: delivery is exactly-once and in order, and a send to a live actor
// always succeeds. It becomes reachable in any transport that can lose a
// message. The settlement is FINAL in all these cases, so nothing may be unsaid,
// and there is deliberately no retry rather than untested machinery for an
// unreachable failure. What is missing is the ability to NOTICE: an absent
// advice row against a suspense that has not returned to zero, and a cycle
// Settled whose banks were never told, are the same shape in the store — which
// is why noticing needs the CLOSING BALANCE the statement carried rather than
// the row's status. See centralBank.advise, which enumerates the three.
// and answers AM04. That is the central bank declining to extend uncollateralised
// intraday credit, which is the decision a settlement agent exists to make.
//
// A cut-off does not arrive in an inbox. It comes in from outside the mesh the
// way a customer's instruction does, so Mesh.CloseCycle runs the clearing
// house's half synchronously and then sends, exactly as Mesh.Submit does for a
// bank's.
//
// The CLEARING HOUSE nets the batch. That moves nothing at all — CloseCycleTx
// posts nowhere, it marks each payment Cleared and writes the positions onto the
// cycle — and then it INSTRUCTS, because discharging those positions moves
// central-bank reserves and no clearing house may do that. The instruction is a
// pacs.009 and not a pacs.008 because both parties to every leg are banks. Each
// leg runs between one member and the settlement agent rather than between two
// members: netting destroys the pairing of payer to payee, so what is left is
// every bank's single claim on, or obligation to, the central bank.
//
// The CENTRAL BANK discharges them, whole or not at all. SettleCycleTx is one
// unit of work holding every member's reserve position at once — the settlement
// accounts in the central bank's OWN book, which is where that position is
// recorded on this side — and that is what a settlement window is. A net payer
// whose reserve cannot cover its position aborts the batch, answered AM04, the
// same code a debtor's bank sends about a customer's empty account, said about a
// bank instead of about a customer. Before the mesh that refusal was a Go error
// returned to whoever pressed settle, which is not something a clearing house
// can act on.
//
// What that window does NOT hold is any member's own book. Each member's mirror
// leg and creditor legs are that member's own units of work, made on advice and
// afterwards. That is what makes settlement FINAL rather than simultaneous: when
// this unit of work commits the reserves have moved, and a member that has not
// yet booked its half has an unreconciled position rather than a claim on
// anyone. See payment.SettleCycle.
//
// The CLEARING HOUSE fans the acceptance out, per payment, to the bank that
// submitted it AND to the payee's bank. The central bank could not: it answers
// about a CYCLE and holds no way to look a payment up, which is the shape of
// "a central bank never sees an individual payment".
//
// Two recipients because they are told it for two reasons. The submitter has an
// instruction outstanding and this closes it; the CREDITOR's bank has a leg to
// post, and posting it is what pays the payee. On a pull they are the same
// institution and there is one message. Only the creditor's bank may act —
// payment.ErrNotThisBanksPayment refuses the other, and on a push that refusal
// is the ordinary case rather than a fault.
//
// A REFUSAL is fanned out to nobody, and that asymmetry is the one thing here
// worth stating twice. Nothing was posted, so every payment is exactly where the
// cut-off left it — Cleared, with the payer's money still in its own bank's
// clearing suspense — and a bank told "rejected" would try to reverse a debtor
// leg that must not be reversed. There is nothing truthful to tell a bank,
// because nothing about its payments changed. What changed is the cycle, and the
// cycle is where the failure shows: still Closed, with no settlement against it.
//
// # The return
//
// The R-transaction: a payment that has already settled, sent back. It is the
// only flow here that starts at the bank which ANSWERED rather than the one that
// submitted, and the only one in which a message a BANK composed is carried past
// the clearing house — the document travels unchanged, with only the header
// replaced, as csm.relay always does.
//
//	payee's bank  --pacs.004-->  clearing house  --pacs.004-->  central bank
//	                             both banks      <--camt.053--  central bank
//	payee's bank  <--pacs.002--  clearing house  <--pacs.002--  central bank
//	payer's bank  <--pacs.004--  clearing house
//
// Seven messages. Two of them are statements and one is the relayed return, and
// each exists because a posting belongs to the bank whose book it is in.
//
// The bank that RECEIVED the original instruction asks for it, which is the
// SEPA rule book's own division: the beneficiary bank returns a credit transfer
// it cannot apply, and the debtor bank returns a collection its customer
// disputes. So it is the payee's bank on a push and the payer's on a pull —
// exactly the opposite end from the one that submitted, in both directions.
//
// Its half MOVES MONEY. The returning bank posts the leg it owns BEFORE it
// composes the message — the
// clawback if it is the creditor's bank, the refund if it is the payer's — and
// that ordering is the whole of the return's one rule: A BANK CAN REFUSE A LEG
// ONLY IF IT POSTS IT BEFORE IT SENDS. On a push it holds the clawback, so a
// payee who has spent the money stops the return dead, as an error to the caller
// and no message at all. On a pull it holds the refund, which is unconditional,
// so it cannot refuse and the forced leg is the other bank's.
//
// The CENTRAL BANK reverses the reserve movement between the two banks'
// settlement accounts, which is central-bank money and which no member bank and
// no clearing house may move. payment/doc.go records the consequence: returns
// settle immediately in this system rather than being netted in a later
// R-cycle, so a return IS a settlement act and belongs where settlement does.
// It reads the parties off the MESSAGE (payment.ReadReturn) and not off a
// payment row, because a settlement agent holds none. It then states both
// accounts in a camt.053 apiece, exactly as at a cut-off, and each bank books
// its own reserve mirror.
//
// The CLEARING HOUSE carries it, and now holds it. Going out it takes it into
// no cycle and reads no store: a return's first destination follows from the
// message definition. Coming back it addresses the answer to the bank that
// asked — three reads, the same three the settlement fan-out makes — and then
// relays the pacs.004 ONWARD to the other bank, routed by the agents OrgnlTxRef
// carries. It holds that message until it has an ACSC, because a bank that
// posted its customer leg against a return the settlement agent then refused
// would have moved money for nothing. That is the only state any actor in this
// package keeps between messages, it is in memory, and csm.relayReturn records
// what a restart costs.
//
// A bank both sends and receives returns, posting its own leg from the one it
// receives (bank.receiveReturn). That handler answers nothing, for the reason
// bank.receiveStatement answers nothing: the return is already final by the time
// the message arrives.
//
// The refusals are split by whether anyone could be TOLD, and there are now
// three kinds. A payment that has not settled cannot be returned, and the
// returning bank refuses that before the message exists — because
// ErrInvalidStateTransition is classified as never reaching a counterparty, so
// a pacs.004 sent for one would be dead-lettered by the settlement agent and the
// operator who asked would hear nothing at all. A push clawback the returning
// bank cannot fund is refused the same way, to the same caller, and is the one
// refusal in this system a beneficiary bank makes about its own customer.
// Everything the settlement agent can answer, it answers: a message carrying
// more than one return, a count that disagrees with what arrived, a message it
// cannot read, or a creditor's bank whose reserves cannot cover the reversal,
// all come back RJCT to the bank that sent it — and that bank UNWINDS the leg it
// posted (bank.receiveReturnStatus). A REDELIVERED return reaches the settlement
// agent, where its own ledger refuses the reserve reversal a second time, and is
// dead-lettered for the same reason the bank's own guard exists.
//
// # A bank the roster does not name, and what this transport does not refuse
//
// Routing here is the ACTOR TABLE, not the roster: a send resolves a BIC against
// the transport's actors, and AddBank gives a bank one whether or not any scheme
// has admitted it. So such a bank is perfectly REACHABLE by this transport, and
// nothing about a send says otherwise. What it cost when nothing else said
// otherwise either is the argument for every refusal below: a payment addressed
// to a non-member was relayed, accepted, taken into a cycle and reached Cleared,
// and failed at the CUT-OFF, wide — csm.settlementLegs turns each net position
// into a BIC through the roster, so one non-member in the batch means the
// pacs.009 cannot be built at all and the whole cycle stays Closed with no
// settlement against it, every other member's payments included.
//
// The two directions are refused in two different places, and neither is here.
// BEING PAID is refused by the payer's own bank: a bank in no roster is in no
// member's copy of one, so an address under its code resolves to nothing and
// SubmitPaymentTx answers payment.ErrBankCodeUnknown before either leg posts —
// earlier than this door, and by an institution that never has to ask anybody.
// PAYING is payment.ErrBankNotAdmitted, in two places: Mesh.Submit refuses at
// the door, beside payment.ErrOnUsPayment and for that guard's reason (Submit is
// synchronous, so a refusal any later has a committed debtor leg to unwind), and
// AcceptAtCSMTx refuses again from the clearing house's own roster row, which is
// the institution whose judgement it is. payment's
// TestTheClearingHouseWillNotClearForANonMember pins the second and says why the
// first is not enough on its own.
//
// The clearing house's refusal alone is not enough for the PAYING direction,
// measured with the door guard removed: it rejects the payment, and csm.tell
// addresses the pacs.002 through the roster too, so the answer that would
// reverse the debtor leg dead-letters at the bank that has no roster row and the
// money stays in its clearing suspense. That is the argument for refusing at the
// door as well.
//
// What makes such a bank unreachable is losing its actor, which a restart does:
// joinRoster builds actors from the ROSTER
// (TestStartGivesAHalfProvisionedBankNoActor). That is a property of this
// transport rather than a refusal any institution makes, which is the
// distinction this section exists to draw.
//
// Nothing here ADMITS anybody. The four acts that put a bank in the scheme are
// payment's, they are called directly by package provision, and no message
// carries any of them — so a deployment provisions its banks and then gives each
// of them an actor, in that order, and this transport never sees the difference.
//
// # No test here waits for a duration, and Drain is why
//
// A test says "submit, drain, assert" and means it: Drain blocks until no
// message is in flight and then returns the dead letters, joined. Durations
// appear only as deadlines — the moment at which a wedged handler stops being
// worth waiting for. The mechanism, and the one non-deterministic assertion in
// either package, are wire's.
//
// # What this transport is not
//
// Delivery is exactly-once and in order, because the transport is a queue
// inside one process, and nothing here applies backpressure. Both are
// properties of THIS system rather than of a payment network, and wire's doc
// says what a real one does instead. What follows for the flows above is that
// "the counterparty is down" is not expressible: a send to a live actor always
// succeeds, so RC01 remains reachable — a BIC can fail to resolve against the
// actor table — and a timeout does not.
//
// Nor is the network a boundary in any other sense. The actors share one process
// and one clock; there is no serialisation cost, no authentication, no
// signature, and no cut-off enforced by anything but the clearing cycle's own
// state. What the mesh models is the SHAPE of interbank messaging — who may know
// what, and when — not its infrastructure. They do NOT share a store; see below.
//
// The clock is literally one: every header this command stamps is dated from
// payment.Network.Now, the same source the payments themselves are booked from
// (see Mesh.now). A mesh with a clock of its own would be a second answer to
// what time it is, and under the frozen clock the tests run on the two would be
// a year apart — a pacs.008 dated after the payment it carries. Two banks in a
// real network do not share a clock, and every timestamp comparison between them
// is a comparison across two, which is why real cut-offs are stated in a named
// time zone and enforced with a tolerance. None of that is modelled here.
//
// # Five databases, likewise, and four mechanisms rather than two
//
// The store is N+2 databases — one per member bank, the clearing house's, the
// settlement agent's — so much of the boundary is simply GIVEN. What the four
// mechanisms narrow, each differently:
//
//   - ops.go narrows by METHOD. A bank handler that calls SettleCycleTx does not
//     compile, because bankOps does not name it.
//   - payment.Network's IDENTITY narrows by INSTITUTION. Each actor is built over
//     the network of the institution it IS (see Mesh.nets), so a bank cannot act
//     as another bank through a handle it legitimately holds.
//   - the STORE narrows by DATABASE. A store handed a book it does not answer for
//     refuses with sqlite.ErrNotThisStoresBook, and a method reaching for a table
//     its institution's schema does not create is refused with
//     sqlite.ErrNotInThisShape. This is the one that makes the crossing the
//     recorder was invented for — one bank reading another's ledger through a
//     method it holds — have no database left in which it could succeed.
//   - the RECORDER in books_test.go watches which books each unit of work
//     actually reached, which is the question none of the other three answers.
//     "Did this bank's act touch exactly its own book" is still worth asking
//     inside one institution's own database, and
//     TestWhichBooksEachBankActuallyReaches is where the answers are.
//
// A fifth is not a boundary at all and belongs beside them anyway. Every
// mechanism above is about what one institution may REACH; payment/recon is
// about whether the institutions that reached nothing of each other's still
// agree — a bank's reserve against the central bank's liability to it, a cycle
// against the settlement that discharged it. recon_test.go is where it is
// calibrated.
package main
