// Command server runs the core-banking REST API.
//
// It opens a store, builds a payment.Network per institution over it, seeds it
// with a comprehensive sample dataset (multiple banks, accounts, payments,
// clearing cycles and settlements) and serves it over HTTP. It can also be reset
// to the sample dataset at runtime via POST /admin/reset, and advanced a
// business day at a time via POST /clock/day. This is a learning and prototyping
// tool, not a production service.
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
// The business date lives beside them, in a small file that belongs to no
// institution (calendar.Clock). With no directory it starts at seed.BaseDate
// every time, which is the same bargain the ephemeral store makes.
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
// # The three institutions
//
// bank.go, csm.go and centralbank.go, over the narrowed views in ops.go, with
// the composition root in deployment.go and the business day in day.go. They are
// this deployment's orchestration rather than a library some other system could
// use, which is what puts them in a command.
//
// They exist to make the FILE the interface. A bank does not learn the fate of a
// payment by calling a function that returns it; no institution here is told
// that a payment was accepted except by collecting a pacs.002 saying so.
// Everything else below follows from taking that seriously.
//
// The transport underneath — order types, order ids, return codes, per-subscriber
// download queues, an http.Handler and a client — is package ebics, which
// carries bytes and knows nothing about what any of them say. This doc names it
// where it bears on a flow and does not restate it.
//
// Every institution has a database of its own, so there is no row a bank could
// read to learn what a counterparty decided, and a handler reaching for one is
// refused by name rather than answered. A bank still reads its own copy —
// Bank.receiveStatus does, and trusts it over the message it just received — but
// that copy is a record of what this bank was told. See "N+2 databases" below.
//
// # The subscriber is the client, and that is the whole topology
//
//	member bank  --> clearing house    (CCT, CDD, CRT up; pacs.002, pacs.004 down)
//	member bank  --> settlement agent  (CLD up; camt.053, camt.025 down)
//	clearing house --> settlement agent (CSI, CRT up; pacs.002 down)
//
// The settlement agent dials nobody. NOTHING IS EVER PUSHED AT A MEMBER BANK: a
// file reaches one by being put in that bank's download queue, and the bank
// collects. Three base URLs are the entire address book, and there is no actor
// table to keep in step with the roster — enrolment is what creates a queue, and
// a queue is the routing table.
//
// So "a bank that never collects" is a real operational failure with a real
// remedy, and it has no analogue in a system where results are delivered.
//
// # The business day
//
// There are NO GOROUTINES under this process but the listeners' own. HTTP
// requests queue work — a customer's instruction joins their bank's hub, an
// operator's cut-off becomes a settlement instruction uploaded to the settlement
// agent — and Deployment.AdvanceDay drains all of it synchronously, in the order
// a business day runs in. Bulk clearing is store-and-forward against a cut-off
// clock, so this is more faithful than a set of actors AND simpler, which is not
// usually the same direction.
//
// # Files, not payments
//
// A member bank does not hand the clearing house a payment. It ACCUMULATES its
// customers' instructions in a hub and, at its cut-off, uploads one file per
// scheme carrying all of them; the clearing house validates that file and
// answers it with ONE pacs.002 carrying a decision per transaction, whose group
// status is PART when they did not all go the same way; and it sorts the file by
// creditor agent into one share per receiving bank, which it holds until the
// cycle carrying them has SETTLED.
//
// So the counts along a chain are 1 in, 1 answered, M out, and none of the four
// institutions sees the same file. That fan-out is what a clearing house is FOR,
// and it is invisible in a network where every message carries one payment.
//
// # Settle, then release
//
// A receiving bank is handed its instructions only once the funds behind them
// are final. Nothing reaches it before the cut-off has settled at the settlement
// agent, and nothing reaches it at all if that cut-off is refused.
//
// Reverse those two and settlement risk is invented: a bank would be crediting
// customers against a batch that might still fail. What the order costs is the
// receiving bank's ability to say NO — by the time it looks at the payment the
// money is in its clearing suspense — so its refusals are RETURNS. AC01 for an
// address it does not hold and AM04 for a payer it cannot collect from are
// pacs.004s, not pacs.002s, which is what they are in SEPA and for this reason.
//
// The only institution that rejects anything is therefore the clearing house,
// and it does so before any money has moved: an unroutable destination (RC01), a
// member the roster does not admit for the scheme's asset, a scheme with no
// cut-off window open (TM01).
//
// day.go's schedule is the four flows below in one list. Two things it
// deliberately does not do: it does not run a cycle on a day TARGET is shut, and
// it does not interleave — each phase completes for every institution before the
// next begins.
//
// A day that is shut still advances the date and still runs each bank's end of
// day, because interest accrues over a weekend and that is the entire reason
// day-count conventions exist.
//
// # Bytes, not structs
//
// An institution marshals an iso20022.Envelope and hands the BYTES to the
// transport, which is the whole of what the transport carries. If two
// institutions exchanged a *Pacs008 the message format would be decoration on a
// function call: malformed input would stop being a reachable failure mode, and
// FF01 — the rejection a receiver sends when it cannot parse what it was given —
// would be untestable. Because the bytes really are parsed on arrival, it is
// testable, and the QUEUE names its subscriber, so a file whose header cannot be
// read can still be answered.
//
// # The credit transfer flow
//
// A SEPA credit transfer is one file up, one answer back, and one file out —
// and the cut-off sits between the second and the third:
//
//	payer's bank  --pacs.008-->  clearing house
//	payer's bank  <--pacs.002--  clearing house      (ACCP or RJCT, per transaction)
//	                             clearing house  ==  the cycle settles  ==>
//	                             clearing house  --pacs.008-->  payee's bank
//	payer's bank  <--pacs.002--  clearing house      (ACSC, per payment)
//
// Reading that down:
//
//   - The PAYER'S BANK takes the instruction. Deployment.Submit runs that half
//     synchronously, so a customer whose instruction fails their own bank's
//     checks is told then and there — and then the instruction WAITS, in that
//     bank's hub, until the cut-off. Nothing has been sent when the API answers
//     202.
//   - The PAYER'S BANK reaches its CUT-OFF, as the first phase of a clearing
//     day, and uploads one file per scheme. What the upload answers is
//     TECHNICAL: an order id means the file arrived. What the far side thinks
//     arrives on a later download.
//   - The CLEARING HOUSE records the file, takes each transaction into the open
//     cycle for its scheme, and answers the submitter per transaction. It also
//     sorts the file by the creditor agent each transaction names and BUILDS
//     each receiving bank's share — which it does not send. It reads no store to
//     sort: a clearing house that had to look a payment up to decide where to
//     send it could not route a file about a payment it does not hold, which in
//     a real network is every file.
//   - The CYCLE settles, and only then does the CLEARING HOUSE put each share in
//     its bank's download queue. A transaction an operator rejected in the
//     meantime is cut out of the share; a cut-off the settlement agent refuses
//     releases nothing at all.
//   - The PAYEE'S BANK resolves each transaction by ADDRESS — that is what
//     produces AC01 for an account number nobody holds — writes its own copy and
//     credits its customer out of the clearing suspense the statement filled.
//     What it cannot apply it HOLDS and sends back, in a pacs.004.
//
// Which institution runs which half is decided entirely by which queue the file
// was in. That, rather than the XML, is what this transport is for.
//
// A rejection therefore HALF-HAPPENS for an interval: the clearing house has
// rejected the payment and the payer's money is still in their own bank's
// clearing suspense, until that bank collects, in a second unit of work, and
// reverses it. payment.RejectAtCSMTx documents that seam; nothing here hides it
// — a reversal that fails has nobody to answer, so it becomes a line in the
// day's report.
//
// # The direct debit flow
//
// A SEPA direct debit is the same three hops in the same order, with the two
// banks swapped and the money moving at a different point in the chain:
//
//	payee's bank  --pacs.003-->  clearing house
//	payee's bank  <--pacs.002--  clearing house      (ACCP or RJCT, per transaction)
//	                             clearing house  ==  the cycle settles  ==>
//	                             clearing house  --pacs.003-->  payer's bank
//	payee's bank  <--pacs.002--  clearing house      (ACSC, per payment)
//
// The PAYEE'S BANK submits, because a collection is the payee asking for what it
// is owed, and its submission MOVES NOTHING: the account being collected from is
// another bank's customer's, and this bank has never seen it. What it checks is
// its own half — the payee's account — plus the MANDATE, because in SEPA the
// creditor holds the mandate. A revoked one is refused there and then, at the
// payee's own bank, as an error to the caller and not as a file; MD01 is
// therefore a code this system never REJECTS with, and a real payer's bank keeps
// mandate records of its own and can. It does reach the wire the other way —
// iso20022.ReturnReasonNoMandate is the same MD01 in the return code set, sent
// by a debtor's bank about a settled collection its customer disputes, and the
// two are separate Go types for exactly this reason.
// payment.SDD.ValidateMandate says so at the layer that owns the limit.
//
// The CLEARING HOUSE sorts it by the DEBTOR's agent, the element that names the
// bank holding the payer, because a pull travels towards the money's source
// while a push travels towards its destination. That one element is the whole
// difference in this institution.
//
// The PAYER'S BANK collects it after the cut-off has settled, and this is the
// half that moves money. It resolves by address exactly as a payee's bank does
// on a push, and then posts the debtor leg — because this is the first moment
// any institution in the system has been able to look at the account being
// collected from.
//
// This is the clearest case there is for settling before releasing. The payer's
// bank was net-debited at the cut-off whatever its customer's balance turns out
// to be, so AM04 — a shortfall no other institution can see — is discovered with
// the money already gone. It cannot refuse; it stands in for the payer out of
// its own pocket (payment.ReceiveUnappliedTx books the claim) and asks for the
// money back with a pacs.004.
//
// The rule that covers both flows, and that neither one on its own would tell
// you, is that the DEBTOR's bank posts the debtor leg. Which bank that is stays
// the same; whether it is submitting or answering is what the direction decides.
//
// # Settlement
//
// The third institution, and the only one that moves reserves. A cut-off is two
// files between the institutions, one STATEMENT into the queue of each member
// whose position moved, and then one status per payment into the banks' queues:
//
//	clearing house  --pacs.009-->  settlement agent
//	settlement agent --camt.053--> each member whose net position moved
//	clearing house  <--pacs.002--  settlement agent
//	clearing house  --pacs.008-->  each receiving bank, its share of a file
//	clearing house  --pacs.002-->  the submitter, per payment
//
// Those fan-outs are what a member's half of a cut-off is made of, and between
// them nothing of it is left in the settlement agent's unit of work: the
// camt.053 carries the MIRROR leg and the released instruction carries the
// CREDITOR leg (Bank.apply, payment.SettleAtBankTx).
//
// Two banks hear different things and each hears what it needs. The submitter
// asked a question and gets ACSC, the answer to it. The receiving bank asked
// nothing and gets the instruction itself, which is both its news and its work.
//
// The camt.053 is what makes the MIRROR LEG the member's own act. A bank's
// clearing suspense holds money that has left a customer and not yet settled
// between banks; the mirror leg is that suspense moving against the bank's
// reserve, and it is a posting in the bank's own ledger. The settlement agent
// states the movement and the closing balance, and the member books it
// (Bank.receiveStatement, payment.PostSettlementAdviceTx).
//
// A statement is not an instruction, so it is ANSWERED BY NOTHING: the
// settlement agent has already settled, and there is nothing left to accept or
// refuse. A member that cannot book what it was told produces a problem in the
// day's report and NO advice row, because the row commits with the leg — so the
// unreconciled position is a clearing suspense that has not returned to zero
// with no advice row against the cycle, which looks the same in the store as
// never having been told.
//
// # Which queue a member collects from first
//
// The mirror leg has to be booked before the creditor legs draw on it, and the
// two files that carry them sit in DIFFERENT queues at DIFFERENT institutions —
// the statement at the settlement agent, the released instruction at the
// clearing house. Two connections share no ordering, so nothing about the order
// they were written in survives.
//
// What guarantees it is the BANK's own collection order: the settlement agent
// first, then the clearing house. That is more honest than what it replaces. The
// ordering was never a property of the network; it was a property of there being
// one queue. Now it is a decision a bank makes about its own operations, in one
// place — see CentralBank.advise and Deployment.clear's last phase — and a bank
// that made
// the other decision would post creditor legs against a suspense whose mirror
// leg had not yet moved.
//
// Two cut-offs share the word and are a phase apart. A BANK's turns its hub into
// files (Bank.Cutoff); the CLEARING HOUSE's turns a batch of validated payments
// into net positions. Neither arrives in a queue: each comes in from outside a
// business day the way a customer's instruction does, so ClearingHouse.CloseCycle
// runs the clearing house's half synchronously and then uploads. The day engine
// reaches both on every settlement day; the two routes are the operator asking
// out of turn.
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
// The SETTLEMENT AGENT discharges them, whole or not at all. SettleCycleTx is
// one unit of work holding every member's reserve position at once — the
// settlement accounts in the agent's OWN book, which is where that position is
// recorded on this side — and that is what a settlement window is. A net payer
// whose reserve cannot cover its position aborts the batch, answered AM04, the
// same code a debtor's bank sends about a customer's empty account, said about a
// bank instead of about a customer. That is the agent declining to extend
// uncollateralised intraday credit, which is the decision a settlement agent
// exists to make.
//
// What that window does NOT hold is any member's own book. Each member's mirror
// leg and creditor legs are that member's own units of work, made on advice and
// afterwards. That is what makes settlement FINAL rather than simultaneous: when
// this unit of work commits the reserves have moved, and a member that has not
// yet booked its half has an unreconciled position rather than a claim on
// anyone. See payment.SettleCycle.
//
// The CLEARING HOUSE turns the acceptance into per-payment news, and the
// settlement agent could not: it answers about a CYCLE and holds no way to look
// a payment up, which is the shape of "a central bank never sees an individual
// payment".
//
// One recipient for the ACSC — the bank that SUBMITTED, whose instruction it
// closes. The other bank is handed the instruction itself in the same breath,
// and that file is both its news and the leg it has to post. See
// ClearingHouse.tellSettled.
//
// A REFUSAL releases NOTHING and is fanned out to nobody, and that asymmetry is
// the one thing here worth stating twice. Nothing was posted, so every payment is
// exactly where the cut-off left it — Cleared, with the payer's money still in
// its own bank's clearing suspense — the receiving banks' shares are still held
// at the clearing house, and no bank has been asked to credit anybody. There is
// nothing truthful to tell a bank, because nothing about its payments changed.
// What changed is the cycle, and the cycle is where the failure shows: still
// Closed, with no settlement against it. The remedy is the operator's: fund the
// short member, and ask the clearing house to instruct settlement again.
//
// # The return
//
// The R-transaction: a payment that has already settled, sent back. It is the
// only flow here that starts at the bank which ANSWERED rather than the one that
// submitted, and the only one in which a message a BANK composed is carried past
// the clearing house — the document travels unchanged, with only the header
// replaced, as every hop through that institution does.
//
//	payee's bank  --pacs.004-->  clearing house  --pacs.004-->  settlement agent
//	                             both banks      <--camt.053--  settlement agent
//	payee's bank  <--pacs.002--  clearing house  <--pacs.002--  settlement agent
//	payer's bank  <--pacs.004--  clearing house
//
// Seven files. Two of them are statements and one is the relayed return, and
// each exists because a posting belongs to the bank whose book it is in.
//
// The bank that RECEIVED the original instruction asks for it, which is the SEPA
// rule book's own division: the beneficiary bank returns a credit transfer it
// cannot apply, and the debtor bank returns a collection its customer disputes.
// So it is the payee's bank on a push and the payer's on a pull — exactly the
// opposite end from the one that submitted, in both directions.
//
// Most returns in this system are asked for by that bank the moment it is handed
// the instruction, because that is when it discovers it cannot apply it: an
// address it does not hold, a payer it cannot collect from. An operator asking
// for one afterwards is the same act down the same route — see
// Deployment.Return.
//
// Its half MOVES MONEY. The returning bank posts the leg it owns BEFORE it
// composes the message — the clawback if it is the creditor's bank, the refund
// if it is the payer's — and that ordering is the whole of the return's one
// rule: A BANK CAN REFUSE A LEG ONLY IF IT POSTS IT BEFORE IT SENDS. On a push
// it holds the clawback, so a payee who has spent the money stops the return
// dead, as an error to the caller and no file at all. On a pull it holds the
// refund, which is unconditional, so it cannot refuse and the forced leg is the
// other bank's.
//
// The SETTLEMENT AGENT reverses the reserve movement between the two banks'
// settlement accounts, which is central-bank money and which no member bank and
// no clearing house may move. payment/doc.go records the consequence: returns
// settle immediately in this system rather than being netted in a later R-cycle,
// so a return IS a settlement act and belongs where settlement does. It reads
// the parties off the MESSAGE (payment.ReadReturn) and not off a payment row,
// because a settlement agent holds none. It then states both accounts in a
// camt.053 apiece, exactly as at a cut-off, and each bank books its own reserve
// mirror.
//
// The CLEARING HOUSE carries it, and now holds it. Going out it takes it into no
// cycle and reads no store: a return's first destination follows from the
// message definition. Coming back it addresses the answer to the bank that asked
// — three reads, the same three the settlement fan-out makes — and then queues
// the pacs.004 ONWARD for the other bank, routed by the agents OrgnlTxRef
// carries. It holds that message until it has an ACSC, because a bank that
// posted its customer leg against a return the settlement agent then refused
// would have moved money for nothing. That is the only state any institution in
// this package keeps between files, it is in memory, and ClearingHouse.relayReturn
// records what a restart costs.
//
// A bank both sends and receives returns, posting its own leg from the one it
// collects (Bank.receiveReturn). That handler answers nothing, for the reason
// Bank.receiveStatement answers nothing: the return is already final by the time
// the file arrives.
//
// The refusals are split by whether anyone could be TOLD, and there are three
// kinds. A payment the returning bank has never been handed does not exist as
// far as that bank is concerned, and one it has already returned cannot be
// returned again; both are refused before the message exists — because
// ErrInvalidStateTransition is classified as never reaching a counterparty, so a
// pacs.004 uploaded for one would be answered by nobody and the operator who
// asked would hear nothing at all. A push clawback the returning bank cannot fund is refused the same way,
// to the same caller, and is the one refusal in this system a beneficiary bank
// makes about its own customer. Everything the settlement agent can answer, it
// answers: a message carrying more than one return, a count that disagrees with
// what arrived, a message it cannot read, or a creditor's bank whose reserves
// cannot cover the reversal, all come back RJCT to the bank that sent it — and
// that bank UNWINDS the leg it posted (Bank.receiveReturnStatus). A REDELIVERED
// return reaches the settlement agent, where its own ledger refuses the reserve
// reversal a second time, and is reported for the same reason the bank's own
// guard exists.
//
// # A bank the roster does not name
//
// Routing IS the roster now, because enrolment is what creates a download queue
// and provisioning is what enrols. A bank in no roster has no queue at either
// host, so a file addressed to it has nowhere to go and the clearing house
// answers RC01 off a row it owns — one refusal rather than two facts that could
// disagree.
//
// What that cost while reachability and membership were separate is the argument
// for the two refusals that survive: a payment addressed to a non-member was
// relayed, accepted, taken into a cycle and reached Cleared, and failed at the
// CUT-OFF, wide — ClearingHouse.settlementLegs turns each net position into a
// BIC through the roster, so one non-member in the batch means the pacs.009
// cannot be built at all and the whole cycle stays Closed with no settlement
// against it, every other member's payments included.
//
// The two directions are refused in two different places, and neither is the
// transport. BEING PAID is refused by the payer's own bank: a bank in no roster
// is in no member's copy of one, so an address under its code resolves to
// nothing and SubmitPaymentTx answers payment.ErrBankCodeUnknown before either
// leg posts. PAYING is payment.ErrBankNotAdmitted, in two places:
// Deployment.Submit refuses at the door, beside payment.ErrOnUsPayment and for
// that guard's reason (Submit is synchronous, so a refusal any later has a
// committed debtor leg to unwind), and AcceptAtCSMTx refuses again from the
// clearing house's own roster row, which is the institution whose judgement it
// is. payment's TestTheClearingHouseWillNotClearForANonMember pins the second
// and says why the first is not enough on its own.
//
// Nothing here ADMITS anybody. The four acts that put a bank in the scheme are
// payment's, they are called directly by package provision, and no file carries
// any of them — so a deployment provisions its banks and then gives each of them
// its place, in that order.
//
// # No test here waits for a duration, and the business day is why
//
// A test says "submit, advance a day, assert" and means it: AdvanceDay is
// synchronous, so nothing is in flight when it returns. There is no quiescence
// loop, no deadline, no poll and no non-deterministic assertion anywhere in this
// package.
//
// # What this transport is not
//
// It carries an HTTP status. The store-and-forward transports under real bulk
// clearing (SWIFT FIN, FileAct) carry delivery guarantees and non-repudiation,
// and this carries neither.
//
// The subscriber identity is a request header, which is not authentication and
// ebics/doc.go says so by naming what a real one has: A006 signs the order, E002
// encrypts the payload, X002 authenticates the request, and enrolment is INI/HIA
// with a hand-signed paper letter carrying the key hashes. This is the same
// posture the ports take — the port is the claim — and stating it once is what
// keeps a reader from inferring that file transfer between banks is
// unauthenticated by nature. It is in fact the most heavily authenticated hop in
// the whole chain, and this system models none of it.
//
// What IS reachable, and was not while a bus delivered: an upload that genuinely
// fails, a bank that never downloads, and a file that arrives and cannot be
// processed. The uploader is told EBICS_OK — it arrived — and the business
// refusal comes back on a later download, or does not come back at all.
//
// The clock is literally one, and it is the DEPLOYMENT's: every header this
// command stamps is dated from calendar.Clock, the same source the payments
// themselves are booked from, and time.Now appears nowhere in the running
// system. Two banks in a real network do not share a clock, and every timestamp
// comparison between them is a comparison across two, which is why real cut-offs
// are stated in a named time zone and enforced with a tolerance. None of that is
// modelled here.
//
// # N+2 databases, and four mechanisms rather than two
//
// The store is N+2 databases — one per member bank, the clearing house's, the
// settlement agent's — so much of the boundary is simply GIVEN. What the four
// mechanisms narrow, each differently:
//
//   - ops.go narrows by METHOD. A bank handler that calls SettleCycleTx does not
//     compile, because bankOps does not name it.
//   - payment.Network's IDENTITY narrows by INSTITUTION. Each institution is
//     built over the network of the institution it IS, so a bank cannot act as
//     another bank through a handle it legitimately holds.
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
// A download queue is one institution holding bytes addressed to another, and it
// is not a fifth crossing: the bytes are opaque to the holder, which is why this
// package's institutions carry bytes rather than structs and why the queues hold
// no rows at all.
//
// A fifth mechanism is not a boundary and belongs beside them anyway. Every one
// above is about what an institution may REACH; payment/recon is about whether
// the institutions that reached nothing of each other's still agree — a bank's
// reserve against the settlement agent's liability to it, a cycle against the
// settlement that discharged it. recon_test.go is where it is calibrated.
package main
