// One typed function per backend route. Each returns the parsed DTO(s). This
// file is the first of the three the data layer grows in — endpoints.ts →
// query-keys.ts → hooks.ts — one section per backend area; see web/CLAUDE.md.

import { request, qs } from "./client";
import { bank, cb, csm } from "./operator";
import type { OperatorStatus } from "./backend-url";
import type {
  Account,
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

export function getPayment(payid: string): Promise<Payment> {
  return request("GET", csm(`/payments/${payid}`));
}

export function initiatePayment(
  body: InitiatePaymentRequest,
): Promise<Payment> {
  return request("POST", csm("/payments"), body);
}

// Reject (before settlement) and return (a completed payment) both take a
// reason and yield the updated payment.
export function rejectPayment(
  payid: string,
  body: ReasonRequest,
): Promise<Payment> {
  return request("POST", csm(`/payments/${payid}/reject`), body);
}

export function returnPayment(
  payid: string,
  body: ReasonRequest,
): Promise<Payment> {
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

// Settle moves reserves to clear the net positions and returns the settlement.
//
// It is the CENTRAL BANK's route, not the clearing house's: the reserves move
// in the central bank's own book, and a clearing house that could do that would
// be a central bank. The cycle is the input and the settlement is the resource
// created, which is why it is a body rather than a path segment.
export function settleCycle(cid: string): Promise<Settlement> {
  return request("POST", cb("/settlements"), { cycleId: cid });
}

// --- Payment: settlements -------------------------------------------------

export function listSettlements(): Promise<Settlement[]> {
  return request("GET", csm("/settlements"));
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
