// One typed function per backend route. Each returns the parsed DTO(s). Grows
// as milestones add coverage; see PLAN.md for the full endpoint → screen map.

import { request, qs } from "./client";
import type {
  Account,
  AuditEvent,
  Balance,
  BookBalance,
  CaptureHoldRequest,
  ClearingCycle,
  CreateAccountRequest,
  CreateHoldRequest,
  CreateMandateRequest,
  DepositAccount,
  DescriptionRequest,
  FundRequest,
  Hold,
  InitiatePaymentRequest,
  Ledger,
  Mandate,
  NameRequest,
  OpenCycleRequest,
  OpenDepositAccountRequest,
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
  Transaction,
} from "../types";

// --- Participants ---------------------------------------------------------

export function listParticipants(): Promise<Participant[]> {
  return request("GET", "/participants");
}

export function getParticipant(pid: string): Promise<Participant> {
  return request("GET", `/participants/${pid}`);
}

export function addParticipant(body: NameRequest): Promise<Participant> {
  return request("POST", "/participants", body);
}

// Funds a customer deposit account (and the bank's central-bank reserve in
// step). Returns the deposit account's new balance.
export function fundDeposit(pid: string, body: FundRequest): Promise<Balance> {
  return request("POST", `/participants/${pid}/deposits`, body);
}

// --- Schemes --------------------------------------------------------------

export function listSchemes(): Promise<Scheme[]> {
  return request("GET", "/schemes");
}

// --- Central bank ---------------------------------------------------------

export function listReserves(): Promise<Reserve[]> {
  return request("GET", "/central-bank/reserves");
}

export function getReserve(pid: string): Promise<Reserve> {
  return request("GET", `/central-bank/reserves/${pid}`);
}

export function centralBankAudit(): Promise<AuditEvent[]> {
  return request("GET", "/central-bank/audit");
}

// --- Ledger: ledgers / subledgers / accounts -----------------------------

export function listLedgers(pid: string): Promise<Ledger[]> {
  return request("GET", `/participants/${pid}/ledgers`);
}

export function createLedger(pid: string, body: NameRequest): Promise<Ledger> {
  return request("POST", `/participants/${pid}/ledgers`, body);
}

export function listSubledgers(
  pid: string,
  lid: string,
): Promise<Subledger[]> {
  return request("GET", `/participants/${pid}/ledgers/${lid}/subledgers`);
}

export function createSubledger(
  pid: string,
  lid: string,
  body: NameRequest,
): Promise<Subledger> {
  return request("POST", `/participants/${pid}/ledgers/${lid}/subledgers`, body);
}

export function listAccounts(pid: string, sid: string): Promise<Account[]> {
  return request("GET", `/participants/${pid}/subledgers/${sid}/accounts`);
}

export function createAccount(
  pid: string,
  sid: string,
  body: CreateAccountRequest,
): Promise<Account> {
  return request("POST", `/participants/${pid}/subledgers/${sid}/accounts`, body);
}

export function getBookBalance(
  pid: string,
  aid: string,
): Promise<BookBalance> {
  return request("GET", `/participants/${pid}/accounts/${aid}/balance`);
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
  return request("GET", `/participants/${pid}/transactions${qs({ account })}`);
}

export function getTransaction(
  pid: string,
  tid: string,
): Promise<Transaction> {
  return request("GET", `/participants/${pid}/transactions/${tid}`);
}

export function postTransaction(
  pid: string,
  body: PostTransactionRequest,
): Promise<Transaction> {
  return request("POST", `/participants/${pid}/transactions`, body);
}

export function reverseTransaction(
  pid: string,
  tid: string,
  body: DescriptionRequest,
): Promise<Transaction> {
  return request(
    "POST",
    `/participants/${pid}/transactions/${tid}/reversal`,
    body,
  );
}

// --- Ledger: audit --------------------------------------------------------

export function ledgerAudit(
  pid: string,
  entity?: string,
): Promise<AuditEvent[]> {
  return request("GET", `/participants/${pid}/audit${qs({ entity })}`);
}

// --- Deposit: accounts ----------------------------------------------------

export function listDepositAccounts(pid: string): Promise<DepositAccount[]> {
  return request("GET", `/participants/${pid}/deposit-accounts`);
}

export function getDepositAccount(
  pid: string,
  did: string,
): Promise<DepositAccount> {
  return request("GET", `/participants/${pid}/deposit-accounts/${did}`);
}

export function openDepositAccount(
  pid: string,
  body: OpenDepositAccountRequest,
): Promise<DepositAccount> {
  return request("POST", `/participants/${pid}/deposit-accounts`, body);
}

// The three-part deposit balance (book / holds / available), all in cents.
export function getDepositBalance(
  pid: string,
  did: string,
): Promise<Balance> {
  return request("GET", `/participants/${pid}/deposit-accounts/${did}/balance`);
}

// One URL drives all four lifecycle transitions via {action}.
export function setDepositStatus(
  pid: string,
  did: string,
  body: StatusRequest,
): Promise<DepositAccount> {
  return request(
    "POST",
    `/participants/${pid}/deposit-accounts/${did}/status`,
    body,
  );
}

// Close needs a zero balance; returns 204 (no body).
export function closeDepositAccount(pid: string, did: string): Promise<void> {
  return request("DELETE", `/participants/${pid}/deposit-accounts/${did}`);
}

// --- Deposit: holds -------------------------------------------------------

export function listHolds(pid: string, did: string): Promise<Hold[]> {
  return request("GET", `/participants/${pid}/deposit-accounts/${did}/holds`);
}

export function createHold(
  pid: string,
  did: string,
  body: CreateHoldRequest,
): Promise<Hold> {
  return request(
    "POST",
    `/participants/${pid}/deposit-accounts/${did}/holds`,
    body,
  );
}

export function getHold(pid: string, hid: string): Promise<Hold> {
  return request("GET", `/participants/${pid}/holds/${hid}`);
}

// Release cancels the authorization (204, nothing posted to the ledger).
export function releaseHold(pid: string, hid: string): Promise<void> {
  return request("POST", `/participants/${pid}/holds/${hid}/release`);
}

// Capture posts a real ledger transaction debiting the customer and crediting
// the counterparty GL account; returns that transaction.
export function captureHold(
  pid: string,
  hid: string,
  body: CaptureHoldRequest,
): Promise<Transaction> {
  return request("POST", `/participants/${pid}/holds/${hid}/capture`, body);
}

// --- Deposit: snapshots ---------------------------------------------------

export function listSnapshots(pid: string, did: string): Promise<Snapshot[]> {
  return request(
    "GET",
    `/participants/${pid}/deposit-accounts/${did}/snapshots`,
  );
}

// date is a calendar day "YYYY-MM-DD" (not RFC3339).
export function takeSnapshot(
  pid: string,
  did: string,
  body: SnapshotRequest,
): Promise<Snapshot> {
  return request(
    "POST",
    `/participants/${pid}/deposit-accounts/${did}/snapshots`,
    body,
  );
}

// --- Deposit: audit -------------------------------------------------------

export function depositAudit(pid: string): Promise<AuditEvent[]> {
  return request("GET", `/participants/${pid}/deposit-audit`);
}

// --- Payment: mandates ----------------------------------------------------

export function listMandates(): Promise<Mandate[]> {
  return request("GET", "/mandates");
}

export function getMandate(mid: string): Promise<Mandate> {
  return request("GET", `/mandates/${mid}`);
}

export function createMandate(body: CreateMandateRequest): Promise<Mandate> {
  return request("POST", "/mandates", body);
}

// Revoke takes no body; returns the updated (Revoked) mandate.
export function revokeMandate(mid: string): Promise<Mandate> {
  return request("POST", `/mandates/${mid}/revoke`);
}

// --- Payment: payments ----------------------------------------------------

export function listPayments(): Promise<Payment[]> {
  return request("GET", "/payments");
}

export function getPayment(payid: string): Promise<Payment> {
  return request("GET", `/payments/${payid}`);
}

export function initiatePayment(
  body: InitiatePaymentRequest,
): Promise<Payment> {
  return request("POST", "/payments", body);
}

// Reject (before settlement) and return (a completed payment) both take a
// reason and yield the updated payment.
export function rejectPayment(
  payid: string,
  body: ReasonRequest,
): Promise<Payment> {
  return request("POST", `/payments/${payid}/reject`, body);
}

export function returnPayment(
  payid: string,
  body: ReasonRequest,
): Promise<Payment> {
  return request("POST", `/payments/${payid}/return`, body);
}

// --- Payment: clearing cycles ---------------------------------------------

export function listCycles(): Promise<ClearingCycle[]> {
  return request("GET", "/cycles");
}

export function getCycle(cid: string): Promise<ClearingCycle> {
  return request("GET", `/cycles/${cid}`);
}

export function openCycle(body: OpenCycleRequest): Promise<ClearingCycle> {
  return request("POST", "/cycles", body);
}

// Close computes net positions; takes no body.
export function closeCycle(cid: string): Promise<ClearingCycle> {
  return request("POST", `/cycles/${cid}/close`);
}

// Settle moves reserves to clear the net positions; takes no body, returns the
// settlement.
export function settleCycle(cid: string): Promise<Settlement> {
  return request("POST", `/cycles/${cid}/settle`);
}

// --- Payment: settlements -------------------------------------------------

export function listSettlements(): Promise<Settlement[]> {
  return request("GET", "/settlements");
}

export function getSettlement(sid: string): Promise<Settlement> {
  return request("GET", `/settlements/${sid}`);
}
