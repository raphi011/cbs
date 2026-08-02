// One typed function per backend route. Each returns the parsed DTO(s). This
// file is the first of the three the data layer grows in — endpoints.ts →
// query-keys.ts → hooks.ts — one section per backend area; see web/CLAUDE.md.

import { request, qs } from "./client";
import { bank, cb, csm } from "./operator";
import type { OperatorStatus } from "./backend-url";
import type {
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
  Facility,
  FundRequest,
  Hold,
  InitiatePaymentRequest,
  Installment,
  Ledger,
  Mandate,
  AddParticipantRequest,
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
  Totals,
  Transaction,
} from "../types";

// --- Participants ---------------------------------------------------------

export function listParticipants(): Promise<Participant[]> {
  return request("GET", csm("/members"));
}

export function getParticipant(pid: string): Promise<Participant> {
  return request("GET", bank(pid, "/me"));
}

export function addParticipant(body: AddParticipantRequest): Promise<Participant> {
  return request("POST", cb("/members"), body);
}

// Funds a customer deposit account (and the bank's central-bank reserve in
// step). Returns the deposit account's new balance.
export function fundDeposit(pid: string, body: FundRequest): Promise<Balance> {
  return request("POST", bank(pid, `/deposits`), body);
}

// --- Schemes --------------------------------------------------------------

export function listSchemes(): Promise<Scheme[]> {
  return request("GET", csm("/schemes"));
}

// --- Directory ------------------------------------------------------------

// Resolving an address on the clearing house's listener: the operator whose job
// "which bank holds this?" is. 404 when nobody holds it, 409 when two banks do —
// an ambiguous address is an error rather than a first hit, following the
// settlement rule about not defaulting quietly.
//
// A customer's lookup is NOT this function. It goes to their own bank's listener
// (see resolveIdentifierAtBank), because a retail client has no CSM connection.
export function resolveIdentifierAtCsm(
  scheme: string,
  value: string,
): Promise<DirectoryEntry> {
  return request("GET", csm(`/directory${qs({ scheme, value })}`));
}

// The same question asked of a customer's own bank. A bank is a scheme
// participant with directory access, and validating a payee's address before
// accepting an instruction is what it uses that for. This is the one a customer's
// browser may call; the CSM's is an operator's.
export function resolveIdentifierAtBank(
  pid: string,
  scheme: string,
  value: string,
): Promise<DirectoryEntry> {
  return request("GET", bank(pid, `/directory${qs({ scheme, value })}`));
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

// The cycles the central bank is asked to settle, read from its own listener.
//
// A settlement instruction in the real thing IS a closed cycle and its net
// positions, so this is part of the act rather than a widening. What the central
// bank still cannot read is an individual payment — GET /payments is the
// clearing house's, and a real central bank does not see one.
export function centralBankCycles(): Promise<ClearingCycle[]> {
  return request("GET", cb("/cycles"));
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

export function listTransactions(
  pid: string,
  account?: string,
): Promise<Transaction[]> {
  return request("GET", bank(pid, `/transactions${qs({ account })}`));
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
// the counterparty GL account; returns that transaction.
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

export function listMandates(): Promise<Mandate[]> {
  return request("GET", csm("/mandates"));
}

export function getMandate(mid: string): Promise<Mandate> {
  return request("GET", csm(`/mandates/${mid}`));
}

export function createMandate(body: CreateMandateRequest): Promise<Mandate> {
  return request("POST", csm("/mandates"), body);
}

// Revoke takes no body; returns the updated (Revoked) mandate.
export function revokeMandate(mid: string): Promise<Mandate> {
  return request("POST", csm(`/mandates/${mid}/revoke`));
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
// and unseen by the payee's bank, because the mesh carries it the rest of the way
// afterwards. Read it again to learn what became of it.
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
// The one that used to exist is worth remembering for HOW it broke rather than
// why it went. Its path was cb("/settlements") — a string — so when the route
// was deleted, tsc, eslint and next build all stayed green and the console
// 404'd at runtime. Nothing on this side of the wire can catch that; only
// loading the page can, which is why csm() is used here rather than a
// hand-written path.

// Re-instruct settlement of a closed cycle. Takes no body; answers 202 and the
// cycle as the clearing house left it — still Closed, because whether the
// settlement agent discharged it this time arrives later. Read the cycle again.
export function settleCycle(cid: string): Promise<ClearingCycle> {
  return request("POST", csm(`/cycles/${cid}/settle`));
}

export function listSettlements(): Promise<Settlement[]> {
  return request("GET", csm("/settlements"));
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
  return request("GET", csm(`/settlements/${sid}`));
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

// --- Admin ----------------------------------------------------------------

// resetState wipes all backend data and reloads the built-in sample dataset.
// The request has no body and returns {status:"reset"} (ignored).
//
// Which store the backend runs on is not observable from here, and it matters:
// on store/pg the wipe is durable and a restart does not bring anything back.
// The copy in reset-button.tsx is worded to be true either way.
export function resetState(): Promise<void> {
  return request<void>("POST", cb("/admin/reset"));
}
