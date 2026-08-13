// One typed function per backend route. Each returns the parsed DTO(s). This
// file is the first of the three the data layer grows in — endpoints.ts →
// query-keys.ts → hooks.ts — one section per backend area; see web/CLAUDE.md.

import { request, qs } from "./client";
import { bank, cb, csm } from "./operator";
import type { OperatorStatus } from "./backend-url";
import type {
  BusinessDate,
  DayReport,
  NetworkFlow,
  Phase,
  PhaseReport,
  Account,
  AcceptedPayment,
  Asset,
  AuditEvent,
  AuditQuery,
  Balance,
  BookBalance,
  CaptureHoldRequest,
  Charge,
  ChargeFacilityInterestRequest,
  ClearingCycle,
  CreateAccountRequest,
  CreateHoldRequest,
  CreateMandateRequest,
  DepositAccount,
  DraftVersionRequest,
  CreateProductRequest,
  ProductVersion,
  Product,
  DescriptionRequest,
  DirectoryEntry,
  RosterEntry,
  RoutingEntry,
  Facility,
  FundRequest,
  Lodgement,
  LodgementRequest,
  Hold,
  InitiatePaymentRequest,
  Installment,
  Ledger,
  Mandate,
  Message,
  MessageDocument,
  MessageQuery,
  NameRequest,
  OpenCycleRequest,
  OpenDepositAccountRequest,
  OpenFacilityRequest,
  OverdraftTerms,
  Participant,
  Payment,
  PostTransactionRequest,
  ReasonRequest,
  Reserve,
  Scheme,
  Settlement,
  Snapshot,
  SnapshotRequest,
  StatusRequest,
  Subledger,
  SubsidiaryBalance,
  Totals,
  Transaction,
  Transfer,
  TransferRequest,
} from "../types";

// --- Participants ---------------------------------------------------------

// Every bank in the network, on the CENTRAL BANK's listener beside the route
// that founds them.
//
// Not the clearing house's: that institution's database has no banks table, and
// the ROSTER of addresses it does hold is exactly the list that omits the
// founded-and-not-yet-admitted bank this read exists to show. Both routes on the
// central bank's port are the OPERATOR's rather than the settlement agent's; see
// the api package's centralBankRouter.
export function listParticipants(): Promise<Participant[]> {
  return request("GET", cb("/members"));
}

export function getParticipant(pid: string): Promise<Participant> {
  return request("GET", bank(pid, "/me"));
}

// Takes cash in over the counter: credits a customer deposit account, and leaves
// the bank holding the cash. Returns the account's new balance.
//
// It does NOT raise the bank's central-bank reserve. A deposit reaches the bank's
// own vault and no institution but that bank, because a bank cannot write in the
// central bank's ledger. lodgeReserves below is the other half.
export function fundDeposit(pid: string, body: FundRequest): Promise<Balance> {
  return request("POST", bank(pid, `/deposits`), body);
}

// The book transfer: two customers of one bank, one posting, nobody told.
//
// It is the route a submitted payment is refused in favour of when both parties
// bank at the same institution. Answers 200 and the payer's new balance, because
// the act is finished when it returns — one institution, one posting, and nobody
// else to ask. An address this bank does not hold is a 404: it was a payment all
// along.
export function transfer(pid: string, body: TransferRequest): Promise<Transfer> {
  return request("POST", bank(pid, `/transfers`), body);
}

// Places a bank's vault cash on reserve at the central bank: the lodgement.
//
// Answers 202 with the INSTRUCTION, not a balance. The reserve credit is the
// central bank's to make and travels as a camt.050, with a camt.025 receipt back,
// so the reserve is not up when this resolves. What HAS happened is the bank's own
// leg: its vault is down and its own reserve mirror is up.
//
// This is how reserves — which start at 0 — are seeded. A bank cannot settle out
// of cash in its own drawer.
export function lodgeReserves(
  pid: string,
  body: LodgementRequest,
): Promise<Lodgement> {
  return request("POST", bank(pid, `/lodgements`), body);
}

// --- Schemes --------------------------------------------------------------

export function listSchemes(): Promise<Scheme[]> {
  return request("GET", csm("/schemes"));
}

// --- Directory ------------------------------------------------------------

// The clearing house's ROUTING directory: every address the scheme will send a
// message to. It is a list and not a lookup, because the question this
// institution can answer is "who may be addressed" and not "who holds this
// IBAN".
//
// There is no way to ask the clearing house the second question: answering it
// would mean sweeping every bank's deposit register, and that institution holds
// none — see api/surface.go and payment.ResolveIdentifier. Nothing in this system
// answers "which bank holds this IBAN". What it does answer is where an address
// ROUTES, and each bank answers that from its own copy of this list.
export function listRoster(): Promise<RosterEntry[]> {
  return request("GET", csm("/roster"));
}

// An address resolved in ONE bank's own register, on that bank's listener. 404
// when this bank does not hold it — including when another bank does, which is
// the narrowing — and 409 when two of its own accounts do.
//
// This is the one a customer's browser may call. What it is not, any more, is a
// way to find out whose IBAN this is: it answers only about the bank being
// asked.
export function resolveIdentifierAtBank(
  pid: string,
  scheme: string,
  value: string,
): Promise<DirectoryEntry> {
  return request("GET", bank(pid, `/directory/accounts${qs({ scheme, value })}`));
}

// The sibling question, on the same bank's listener: which institution does this
// address route to. Out of THIS bank's copy of the scheme's published directory
// and nothing else, which is why it is a bank's route and not the clearing
// house's.
//
// The whole ADDRESS goes over, rather than the allocation inside it, and that is
// not laziness. Reading a bank code out of an IBAN takes the country structure
// table — variable-length codes at country-dependent offsets — and that table is
// the backend's; a second copy here with nothing holding the two together is
// exactly what `lib/iban.ts` refuses to be. So this client keeps mod-97, which
// needs no table, and asks for the rest.
//
// 422 on a miss, and the refusal cannot say whether no such bank is in the
// scheme or this copy is simply behind — telling those apart would mean asking
// the clearing house, which is the lookup the subscription replaces. 422 as well
// for an address that does not parse, which is why a caller runs the check
// digits before it gets here.
export function routeForAddress(pid: string, iban: string): Promise<RoutingEntry> {
  return request("GET", bank(pid, `/directory/banks${qs({ iban })}`));
}

// The same route unnarrowed: the whole copy this bank holds, every row carrying
// the instant of the pull that wrote it.
export function listRoutingDirectory(pid: string): Promise<RoutingEntry[]> {
  return request("GET", bank(pid, "/directory/banks"));
}

// This bank subscribing: pull the scheme's published roster and replace this
// bank's copy with it, wholesale. 200 with the new copy rather than 202, and
// that is the difference between this and every other act here that reaches two
// institutions — nothing is sent, nobody answers later, and by the time it
// resolves the copy is the one the next payment will route from.
export function refreshRoutingDirectory(pid: string): Promise<RoutingEntry[]> {
  return request("POST", bank(pid, "/directory/banks/refresh"));
}

// --- Central bank ---------------------------------------------------------

export function listReserves(): Promise<Reserve[]> {
  return request("GET", cb("/reserves"));
}

// One row per asset the bank operates in, so a euro bank returns a list of one.
export function getReserve(pid: string): Promise<Reserve[]> {
  return request("GET", cb(`/reserves/${pid}`));
}

export function centralBankAudit(q: AuditQuery = {}): Promise<AuditEvent[]> {
  return request("GET", cb(`/audit${qs({ ...q })}`));
}

// The cycles the central bank is asked to settle.
//
// On the CLEARING HOUSE's listener, because a cycle is that institution's row
// and the settlement agent's database has no cycles table. It was cb("/cycles")
// while one store served both, which is precisely the mistake the note further
// down this file warns about — a path that goes stale with nothing on this side
// of the wire able to notice.
//
// What the central bank's own console has instead is its settlements, and a
// settlement carries the cut-off's id, its net positions and its asset. The one
// thing that read is FOR and cannot be got from them is a closed cycle with NO
// settlement against it — an instruction this agent refused — and that is
// visible here, where the refusal is recorded as a status that never advanced.
export function centralBankCycles(): Promise<ClearingCycle[]> {
  return request("GET", csm("/cycles"));
}

// --- Assets -----------------------------------------------------------

// Every asset the system knows. Network-wide and read-only, like listSchemes:
// an asset definition is defined in Go (ledger.LookupAsset), not stored, so
// there is a list to read and nothing to create.
export function listAssets(): Promise<Asset[]> {
  return request("GET", csm("/assets"));
}

// --- Ledger: ledgers / subledgers / accounts -----------------------------

export function listLedgers(pid: string): Promise<Ledger[]> {
  return request("GET", bank(pid, `/ledgers`));
}

export function createLedger(pid: string, body: NameRequest): Promise<Ledger> {
  return request("POST", bank(pid, `/ledgers`), body);
}

export function listSubledgers(
  pid: string,
  lid: string,
): Promise<Subledger[]> {
  return request("GET", bank(pid, `/ledgers/${lid}/subledgers`));
}

export function createSubledger(
  pid: string,
  lid: string,
  body: NameRequest,
): Promise<Subledger> {
  return request("POST", bank(pid, `/ledgers/${lid}/subledgers`), body);
}

export function listAccounts(pid: string, sid: string): Promise<Account[]> {
  return request("GET", bank(pid, `/subledgers/${sid}/accounts`));
}

export function createAccount(
  pid: string,
  sid: string,
  body: CreateAccountRequest,
): Promise<Account> {
  return request("POST", bank(pid, `/subledgers/${sid}/accounts`), body);
}

// asOf is an RFC 3339 timestamp and governs only `valueDateBalance` — the
// booking-date `balance` is always as of now. Omitted, the backend defaults it
// to now, so the two agree unless something is value-dated away from its
// booking date.
export function getBookBalance(
  pid: string,
  aid: string,
  asOf?: string,
): Promise<BookBalance> {
  return request("GET", bank(pid, `/accounts/${aid}/balance${qs({ asOf })}`), );
}

// A GL account flattened with its ledger/subledger names, for pickers.
export interface AccountWithContext extends Account {
  ledgerName: string;
  subledgerName: string;
}

// Walks the whole chart of accounts (ledgers → subledgers → accounts) into one
// flat list. There's no single "all accounts" endpoint, so this fans out the
// reads; the result is cached by TanStack Query (see useAllAccounts).
export async function getAllAccounts(
  pid: string,
): Promise<AccountWithContext[]> {
  const ledgers = await listLedgers(pid);
  const perLedger = await Promise.all(
    ledgers.map(async (l) => {
      const subs = await listSubledgers(pid, l.id);
      const perSub = await Promise.all(
        subs.map(async (s) => {
          const accts = await listAccounts(pid, s.id);
          return accts.map((a) => ({
            ...a,
            ledgerName: l.name,
            subledgerName: s.name,
          }));
        }),
      );
      return perSub.flat();
    }),
  );
  return perLedger.flat();
}

// --- Ledger: transactions -------------------------------------------------

// `account` alone is the WHOLE account — on a control line, every subsidiary's
// postings. `subsidiary` narrows it to one of them, which is what a customer's
// statement is: a customer is a position, not an account.
export function listTransactions(
  pid: string,
  account?: string,
  subsidiary?: string,
): Promise<Transaction[]> {
  return request("GET", bank(pid, `/transactions${qs({ account, subsidiary })}`));
}

// Who a control account is holding money for, and how much of the line is
// each one's. Empty for a plain account, which pools nobody.
export function listSubsidiaries(
  pid: string,
  aid: string,
): Promise<SubsidiaryBalance[]> {
  return request("GET", bank(pid, `/accounts/${aid}/subsidiaries`));
}

export function getTransaction(
  pid: string,
  tid: string,
): Promise<Transaction> {
  return request("GET", bank(pid, `/transactions/${tid}`));
}

export function postTransaction(
  pid: string,
  body: PostTransactionRequest,
): Promise<Transaction> {
  return request("POST", bank(pid, `/transactions`), body);
}

export function reverseTransaction(
  pid: string,
  tid: string,
  body: DescriptionRequest,
): Promise<Transaction> {
  return request("POST", bank(pid, `/transactions/${tid}/reversal`), body, );
}

// --- Ledger: audit --------------------------------------------------------

export function ledgerAudit(
  pid: string,
  q: AuditQuery = {},
): Promise<AuditEvent[]> {
  return request("GET", bank(pid, `/audit${qs({ ...q })}`));
}

// --- Deposit: accounts ----------------------------------------------------

// --- Product catalogue ----------------------------------------------------

// listProducts returns the whole catalogue, retired entries included: a
// withdrawn product is still the product some accounts are on.
export function listProducts(pid: string): Promise<Product[]> {
  return request("GET", bank(pid, `/products`));
}

export function getProduct(pid: string, prid: string): Promise<Product> {
  return request("GET", bank(pid, `/products/${prid}`));
}

export function createProduct(
  pid: string,
  body: CreateProductRequest,
): Promise<Product> {
  return request("POST", bank(pid, `/products`), body);
}

export function retireProduct(pid: string, prid: string): Promise<Product> {
  return request("POST", bank(pid, `/products/${prid}/retire`));
}

// listProductVersions returns the whole timeline, drafts included, oldest
// first — an operator view has to be able to see what is queued.
export function listProductVersions(
  pid: string,
  prid: string,
): Promise<ProductVersion[]> {
  return request("GET", bank(pid, `/products/${prid}/versions`));
}

export function draftProductVersion(
  pid: string,
  prid: string,
  body: DraftVersionRequest,
): Promise<ProductVersion> {
  return request("POST", bank(pid, `/products/${prid}/versions`), body);
}

// The effective day is in the PATH because it is the version's identity: "2026-07-30".
export function publishProductVersion(
  pid: string,
  prid: string,
  day: string,
): Promise<ProductVersion> {
  return request("POST", bank(pid, `/products/${prid}/versions/${day}/publish`), );
}

export function listDepositAccounts(pid: string): Promise<DepositAccount[]> {
  return request("GET", bank(pid, `/deposit-accounts`));
}

export function getDepositAccount(
  pid: string,
  did: string,
): Promise<DepositAccount> {
  return request("GET", bank(pid, `/deposit-accounts/${did}`));
}

export function openDepositAccount(
  pid: string,
  body: OpenDepositAccountRequest,
): Promise<DepositAccount> {
  return request("POST", bank(pid, `/deposit-accounts`), body);
}

// The three-part deposit balance (book / holds / available), all integers in
// the account's asset's minor units — the asset comes back on the response.
export function getDepositBalance(
  pid: string,
  did: string,
): Promise<Balance> {
  return request("GET", bank(pid, `/deposit-accounts/${did}/balance`));
}

// One URL drives all four lifecycle transitions via {action}.
export function setDepositStatus(
  pid: string,
  did: string,
  body: StatusRequest,
): Promise<DepositAccount> {
  return request("POST", bank(pid, `/deposit-accounts/${did}/status`), body, );
}

// Close needs a zero balance; returns 204 (no body).
export function closeDepositAccount(pid: string, did: string): Promise<void> {
  return request("DELETE", bank(pid, `/deposit-accounts/${did}`));
}

// --- Deposit: holds -------------------------------------------------------

export function listHolds(pid: string, did: string): Promise<Hold[]> {
  return request("GET", bank(pid, `/deposit-accounts/${did}/holds`));
}

export function createHold(
  pid: string,
  did: string,
  body: CreateHoldRequest,
): Promise<Hold> {
  return request("POST", bank(pid, `/deposit-accounts/${did}/holds`), body, );
}

export function getHold(pid: string, hid: string): Promise<Hold> {
  return request("GET", bank(pid, `/holds/${hid}`));
}

// Release cancels the authorization (204, nothing posted to the ledger).
export function releaseHold(pid: string, hid: string): Promise<void> {
  return request("POST", bank(pid, `/holds/${hid}/release`));
}

// Capture posts a real ledger transaction debiting the customer and crediting
// the counterparty position; returns that transaction.
export function captureHold(
  pid: string,
  hid: string,
  body: CaptureHoldRequest,
): Promise<Transaction> {
  return request("POST", bank(pid, `/holds/${hid}/capture`), body);
}

// --- Deposit: overdraft terms ----------------------------------------------

export function listOverdraftTerms(
  pid: string,
  did: string,
): Promise<OverdraftTerms[]> {
  return request("GET", bank(pid, `/deposit-accounts/${did}/overdraft-terms`), );
}

// --- Deposit: snapshots ---------------------------------------------------

export function listSnapshots(pid: string, did: string): Promise<Snapshot[]> {
  return request("GET", bank(pid, `/deposit-accounts/${did}/snapshots`), );
}

// date is a calendar day "YYYY-MM-DD" (not RFC3339).
export function takeSnapshot(
  pid: string,
  did: string,
  body: SnapshotRequest,
): Promise<Snapshot> {
  return request("POST", bank(pid, `/deposit-accounts/${did}/snapshots`), body, );
}

// --- Deposit: audit -------------------------------------------------------

export function depositAudit(
  pid: string,
  q: AuditQuery = {},
): Promise<AuditEvent[]> {
  return request("GET", bank(pid, `/deposit-audit${qs({ ...q })}`));
}

// --- Lending: facilities ----------------------------------------------------

export function listFacilities(pid: string): Promise<Facility[]> {
  return request("GET", bank(pid, `/facilities`));
}

export function getFacility(pid: string, fid: string): Promise<Facility> {
  return request("GET", bank(pid, `/facilities/${fid}`));
}

export function openFacility(
  pid: string,
  body: OpenFacilityRequest,
): Promise<Facility> {
  return request("POST", bank(pid, `/facilities`), body);
}

export function getFacilitySchedule(
  pid: string,
  fid: string,
): Promise<Installment[]> {
  return request("GET", bank(pid, `/facilities/${fid}/schedule`));
}

// chargeFacilityInterest closes a revolving line's billing cycle.
//
// The result is a Charge, not a Transaction, because the two outcomes are
// independent: a cycle whose accrued interest has not yet reached a whole
// minor unit bills an instalment and posts nothing. A cycle on a line that is
// undrawn and owes nothing does neither, and the backend answers 204 — so the
// return type includes `undefined`. request()'s text-then-JSON.parse (it never
// calls response.json()) already yields `undefined` for an empty body rather
// than throwing; naming it here is what stops a caller treating the result as
// a posted transaction without checking.
export function chargeFacilityInterest(
  pid: string,
  fid: string,
  body: ChargeFacilityInterestRequest,
): Promise<Charge | undefined> {
  return request("POST", bank(pid, `/facilities/${fid}/interest-charge`), body, );
}

// --- Lending: totals ---------------------------------------------------------

// A bank's customer-deposit position split into deposits and (derived)
// overdrafts, per asset.
export function getTotals(pid: string): Promise<Totals[]> {
  return request("GET", bank(pid, `/totals`));
}

// --- Payment: mandates ----------------------------------------------------
//
// On a BANK's listener, all of them. A mandate is the CREDITOR's bank's row — in
// SEPA the creditor holds
// the mandate, and the bank that checks one at submission is the creditor's —
// so the pid here is the collecting bank's, and what it lists is its own
// customers' authorisations rather than every member's on one page.

export function listMandates(pid: string): Promise<Mandate[]> {
  return request("GET", bank(pid, "/mandates"));
}

export function getMandate(pid: string, mid: string): Promise<Mandate> {
  return request("GET", bank(pid, `/mandates/${mid}`));
}

export function createMandate(pid: string, body: CreateMandateRequest): Promise<Mandate> {
  return request("POST", bank(pid, "/mandates"), body);
}

// Revoke takes no body; returns the updated (Revoked) mandate.
export function revokeMandate(pid: string, mid: string): Promise<Mandate> {
  return request("POST", bank(pid, `/mandates/${mid}/revoke`));
}

// --- Payment: payments ----------------------------------------------------

export function listPayments(): Promise<Payment[]> {
  return request("GET", csm("/payments"));
}

// One bank's own legs: the payments it sent and the ones it received, and
// nothing else. Same pattern as the clearing house's GET /payments, different
// operator, different answer — the port is the caller identity the single server
// did not have, which is why it listed every payment to everybody.
//
// GET /payments/{id} on a bank answers 404, not 403, for a payment it is not
// party to: a 403 would confirm that the id names something real.
export function bankPayments(pid: string): Promise<Payment[]> {
  return request("GET", bank(pid, "/payments"));
}

export function bankPayment(pid: string, payid: string): Promise<Payment> {
  return request("GET", bank(pid, `/payments/${payid}`));
}

// A customer's instruction, submitted to their own bank. The answer is 202 with
// an identifier rather than the payment: the outcome comes from asking again.
export function submitPayment(
  pid: string,
  body: InitiatePaymentRequest,
): Promise<AcceptedPayment> {
  return request("POST", bank(pid, "/payments"), body);
}

export function getPayment(payid: string): Promise<Payment> {
  return request("GET", csm(`/payments/${payid}`));
}

// The operator's console initiating on a bank's behalf. Like the retail route
// above, the answer is 202 — the payment it hands back is Initiated, in no cycle
// and unseen by the payee's bank, because it is waiting in that bank's hub for
// the next cut-off. Advancing the day is what carries it; read it again
// afterwards to learn what became of it.
export function initiatePayment(
  body: InitiatePaymentRequest,
): Promise<Payment> {
  return request("POST", csm("/payments"), body);
}

// Reject (before settlement) and return (a completed payment) both take a reason,
// and both are 202: what they start finishes at another institution.
//
// A rejection yields the payment as the CLEARING HOUSE left it — Rejected, out
// of its cycle — and the payer's refund is their own bank's act, posted when the
// pacs.002 reaches it. A return yields no payment at all: the returning bank
// posts nothing, so there is no intermediate state to describe, and the money
// moves at the settlement agent four hops later. Both are read back rather than
// awaited.
export function rejectPayment(
  payid: string,
  body: ReasonRequest,
): Promise<Payment> {
  return request("POST", csm(`/payments/${payid}/reject`), body);
}

export function returnPayment(
  payid: string,
  body: ReasonRequest,
): Promise<AcceptedPayment> {
  return request("POST", csm(`/payments/${payid}/return`), body);
}

// --- Payment: audit -------------------------------------------------------

// The network's own audit trail: participants, mandates, payments and clearing
// cycles. It is network-scoped rather than per-bank, which is why it hangs off
// /payments rather than /participants/{pid}.
export function paymentAudit(q: AuditQuery = {}): Promise<AuditEvent[]> {
  return request("GET", csm(`/payments/audit${qs({ ...q })}`));
}

// --- The message log ------------------------------------------------------

// The CLEARING HOUSE's own record of the files it sent and received. All three
// listeners answer these two routes and each answers about itself alone, so a
// screen reading another institution's log gets a function of its own rather
// than an operator parameter here — the same rule ledgerAudit and
// centralBankAudit already follow.
export function clearingHouseMessages(q: MessageQuery = {}): Promise<Message[]> {
  return request("GET", csm(`/messages${qs({ ...q })}`));
}

// One document, and the only read in this file that is handed a file's bytes:
// every listing leaves the payload unread and carries its size instead. The seq
// is one this institution's own listing named.
export function clearingHouseMessage(seq: number): Promise<MessageDocument> {
  return request("GET", csm(`/messages/${seq}`));
}

// --- Payment: clearing cycles ---------------------------------------------

export function listCycles(): Promise<ClearingCycle[]> {
  return request("GET", csm("/cycles"));
}

export function getCycle(cid: string): Promise<ClearingCycle> {
  return request("GET", csm(`/cycles/${cid}`));
}

export function openCycle(body: OpenCycleRequest): Promise<ClearingCycle> {
  return request("POST", csm("/cycles"), body);
}

// Close computes net positions; takes no body.
export function closeCycle(cid: string): Promise<ClearingCycle> {
  return request("POST", csm(`/cycles/${cid}/close`));
}

// --- Payment: settlements -------------------------------------------------
//
// Nothing here SETTLES, and that is a decision rather than a gap. Settling is
// performed on INSTRUCTION: the clearing house reaches a cut-off, sends a
// pacs.009 carrying the closed cycle's net positions, and the central bank
// answers ACSC — or RJCT/AM04 when a net payer's reserve cannot cover. A
// function that POSTed a settlement to the central bank would be a second way
// to settle the same cycle, racing the first, and the backend does not serve
// that route.
//
// What settleCycle below does is ASK AGAIN, on the clearing house's own port.
// It exists because a refusal was otherwise terminal: the cycle sits Closed with
// no settlement, every payer debited into their own bank's clearing suspense
// and every payee unpaid, with no transition out through any other route. The
// operator funds the short member and calls this; the clearing house rebuilds
// the same pacs.009 and the settlement agent decides again.
//
// A deleted route is worth remembering for HOW it breaks: a path is a string, so
// tsc, eslint and next build all stay green and the console 404s at runtime.
//
// csm() below buys nothing against that, and saying otherwise would be the same
// kind of claim this file is warning about. cb and csm are one-line template
// helpers (see operator.ts) that pick the OPERATOR PREFIX; the route that broke
// used cb() and broke anyway, because the part that went stale was the path
// after it. What they buy is that a request cannot be sent to the wrong
// listener by forgetting a prefix — a real class of mistake, and not this one.
// Nothing on this side of the wire can catch a deleted route. Only loading the
// page can.

// Re-instruct settlement of a closed cycle. Takes no body; answers 202 and the
// cycle as the clearing house left it — still Closed, because whether the
// settlement agent discharged it this time arrives later. Read the cycle again.
export function settleCycle(cid: string): Promise<ClearingCycle> {
  return request("POST", csm(`/cycles/${cid}/settle`));
}

// The settlements, on the SETTLEMENT AGENT's listener — which is the only one
// that serves them, so this and centralBankSettlements below are now one route
// under two names. A settlement is that institution's record of its own act, in
// its own database, and the clearing house has no table for one.
//
// What the clearing house learns settlement from instead is its OWN cycle,
// whose status the agent's ACSC moved. See centralBankCycles.
//
// # The clearing house's settlements PAGE is left where it is, and that is a
// question rather than an answer
//
// /clearing-house/settlements still exists and now reads the central bank's
// port, which is the one thing this file says a console should not do. The
// alternative is moving the page under /central-bank, and that is a navigation
// change with a nav manifest, an identity test and two quiz chapters linking at
// it — an information-architecture decision, not a routing fix. It is written
// down here so the next reader meets it as a decision that was deferred rather
// than as an inconsistency nobody noticed.
export function listSettlements(): Promise<Settlement[]> {
  return request("GET", cb("/settlements"));
}

// The settlements the CENTRAL BANK performed, read from its own listener.
//
// The same rows listSettlements returns, and deliberately a second function
// rather than a parameter on the first: an operator's console reads its own
// port. A central bank asking the clearing house what the central bank did
// would be exactly the confusion the operator split exists to remove.
export function centralBankSettlements(): Promise<Settlement[]> {
  return request("GET", cb("/settlements"));
}

export function getSettlement(sid: string): Promise<Settlement> {
  return request("GET", cb(`/settlements/${sid}`));
}

// --- Operators (Next-side, not a backend route) ----------------------------

export type { OperatorStatus } from "./backend-url";

// Which operators have a listener behind them. Served by the Next app itself:
// deployment topology is not domain data, and no backend knows where its
// siblings are bound. See app/api/operators/route.ts, which shares the type.
//
// It is the one path here that is not built with cb()/csm()/bank(), because it
// names no operator — it is the question "which of them are there?".
export function listOperators(): Promise<OperatorStatus[]> {
  return request("GET", "/operators");
}

// --- The business day -------------------------------------------------------

// getClock reads which day the deployment is on. advanceDay runs one business
// day and moves it to the next, answering with what that day did.
//
// Both are on the SETTLEMENT AGENT's listener, and both are one operator's acts
// over a DEPLOYMENT rather than over an institution — the same argument that
// puts GET /members and POST /admin/reset there. Spreading the clock across all
// N+2 surfaces would buy nothing once the button beside it is dialling the
// settlement agent anyway.
export function getClock(): Promise<BusinessDate> {
  return request("GET", cb("/clock"));
}

export function advanceDay(): Promise<DayReport> {
  return request("POST", cb("/clock/day"));
}

// listPhases is the day as it is declared, in the order it runs. runPhase opens
// one of those doors and answers with what that phase moved — the clock stays
// where it stood, which is the difference between stepping a day and advancing
// one.
//
// A phase is named, never parameterised, so the key is the whole of what a door
// takes and a caller composes derived runs around it rather than splicing.
export function listPhases(): Promise<Phase[]> {
  return request("GET", cb("/clock/phases"));
}

export function runPhase(key: string): Promise<PhaseReport> {
  return request("POST", cb(`/clock/phases/${encodeURIComponent(key)}`));
}

// --- The network mesh -------------------------------------------------------

// Every institution, every wire between them, and every file that crossed one.
// It is SERVED rather than fanned out across the N+2 listeners here, because
// "may see everything" is the deployment's standing and belongs in its own type
// system rather than in a browser that merged what nobody may read together.
//
// `limit` bounds the crossings that were DELIVERED. A file still resting on a
// wire is never paged out: a queue nobody has come for is what this view exists
// to show.
export function networkFlow(limit?: number): Promise<NetworkFlow> {
  return request("GET", cb(`/network/flow${qs({ limit })}`));
}

// networkEventsPath is the same mesh as it happens, and is not a `request`: it
// is opened by an EventSource rather than fetched, so what this exports is the
// URL. See useNetworkEvents, which is the one subscriber.
export const networkEventsPath = `/api${cb("/network/flow/events")}`;

// --- Admin ----------------------------------------------------------------

// resetState wipes all backend data and reloads the built-in sample dataset.
// The request has no body and returns {status:"reset"} (ignored).
//
// Whether the backend is on an ephemeral database or a file is not observable
// from here, and it matters: against a file the wipe is durable and a restart
// does not bring anything back. The copy in reset-button.tsx is worded to be
// true either way.
export function resetState(): Promise<void> {
  return request<void>("POST", cb("/admin/reset"));
}
