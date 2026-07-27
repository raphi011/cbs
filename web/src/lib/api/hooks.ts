// TanStack Query hooks wrapping the endpoint functions. Queries use the key
// factory; mutations invalidate affected keys. Grows per milestone.

import { useMemo } from "react";

import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

import { buildKnownAccounts, projectStatement } from "@/lib/statement";
import type { StatementRow } from "@/lib/statement";
import type { AccountType } from "@/lib/enums";
import type { Asset, AuditQuery } from "@/lib/types";

import * as api from "./endpoints";
import { qk } from "./query-keys";

// --- Participants ---------------------------------------------------------

export function useParticipants() {
  return useQuery({
    queryKey: qk.participants(),
    queryFn: api.listParticipants,
  });
}

export function useParticipant(pid: string) {
  return useQuery({
    queryKey: qk.participant(pid),
    queryFn: () => api.getParticipant(pid),
    enabled: pid !== "",
  });
}

export function useAddParticipant() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.addParticipant,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.participants() });
      // participant.added is a network-scope audit event.
      qc.invalidateQueries({ queryKey: qk.paymentAudit() });
    },
  });
}

// --- Schemes --------------------------------------------------------------

export function useSchemes() {
  return useQuery({ queryKey: qk.schemes(), queryFn: api.listSchemes });
}

// --- Central bank ---------------------------------------------------------

export function useReserves() {
  return useQuery({ queryKey: qk.reserves(), queryFn: api.listReserves });
}

export function useReserve(pid: string) {
  return useQuery({
    queryKey: qk.reserve(pid),
    queryFn: () => api.getReserve(pid),
    enabled: pid !== "",
  });
}

export function useCentralBankAudit(q: AuditQuery = {}) {
  return useQuery({
    queryKey: qk.centralBankAudit(q),
    queryFn: () => api.centralBankAudit(q),
  });
}

// --- Assets -----------------------------------------------------------

export function useAssets() {
  return useQuery({
    queryKey: qk.assets(),
    queryFn: () => api.listAssets(),
    // An asset definition cannot change while the app is open: the list is
    // compiled into the backend. Fetch it once and reuse it everywhere.
    staleTime: Infinity,
  });
}

// Resolves asset codes to their full definition (name, scale, class).
//
// The list is network-wide because an asset definition is a fact about the
// world rather than per-bank state — "BTC has 8 decimal places" is true in
// every book — so it is one query, shared by every caller through one query
// key, rather than one per participant.
//
// `byCode.get(code)` is undefined while the list is still loading and for a
// code the system does not know; callers must not substitute a guessed scale
// in that case (that is exactly the bug the asset dimension exists to
// prevent — see web/src/lib/money.ts).
export function useAssetLookup() {
  const q = useAssets();
  const byCode = useMemo(() => {
    const m = new Map<string, Asset>();
    for (const a of q.data ?? []) m.set(a.code, a);
    return m;
  }, [q.data]);
  return { byCode, isLoading: q.isLoading, error: q.error };
}

// --- Ledger: accounts tree ------------------------------------------------

export function useLedgers(pid: string) {
  return useQuery({
    queryKey: qk.ledgers(pid),
    queryFn: () => api.listLedgers(pid),
    enabled: pid !== "",
  });
}

export function useCreateLedger(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string }) => api.createLedger(pid, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.ledgers(pid) }),
  });
}

export function useSubledgers(pid: string, lid: string) {
  return useQuery({
    queryKey: qk.subledgers(pid, lid),
    queryFn: () => api.listSubledgers(pid, lid),
    enabled: pid !== "" && lid !== "",
  });
}

export function useCreateSubledger(pid: string, lid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string }) =>
      api.createSubledger(pid, lid, body),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: qk.subledgers(pid, lid) }),
  });
}

export function useAccounts(pid: string, sid: string) {
  return useQuery({
    queryKey: qk.accounts(pid, sid),
    queryFn: () => api.listAccounts(pid, sid),
    enabled: pid !== "" && sid !== "",
  });
}

export function useCreateAccount(pid: string, sid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      name: string;
      type: import("../enums").AccountType;
      asset: string;
    }) => api.createAccount(pid, sid, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.accounts(pid, sid) }),
  });
}

export function useAccountBalance(pid: string, aid: string) {
  return useQuery({
    queryKey: qk.accountBalance(pid, aid),
    queryFn: () => api.getBookBalance(pid, aid),
    enabled: pid !== "" && aid !== "",
  });
}

// Flattened chart of accounts (ledgers → subledgers → accounts) for GL account
// pickers. Refetches on mount, so opening a form picks up new accounts.
export function useAllAccounts(pid: string) {
  return useQuery({
    queryKey: qk.allAccounts(pid),
    queryFn: () => api.getAllAccounts(pid),
    enabled: pid !== "",
  });
}

// A single GL account with its ledger/subledger context, derived from the
// cached chart of accounts (there's no per-account GET endpoint). Returns
// undefined for an unknown id once loading settles → the page shows not-found.
export function useGLAccount(pid: string, aid: string) {
  const q = useAllAccounts(pid);
  const account = useMemo(() => q.data?.find((a) => a.id === aid), [q.data, aid]);
  return {
    account,
    isLoading: q.isLoading,
    error: q.error,
    refetch: () => q.refetch(),
  };
}

// The General Ledger projected onto ANY account, signed by its normal balance,
// with the account's book balance for reconciliation. The deposit-specific
// useStatement is the Liability sibling of this; here `type` comes from the
// resolved account (pass undefined while it loads — rows aren't shown yet).
export function useAccountStatement(pid: string, aid: string, type: AccountType | undefined) {
  const txq = useTransactions(pid, aid);
  const balq = useAccountBalance(pid, aid);
  const partq = useParticipant(pid);

  const known = useMemo(() => buildKnownAccounts(partq.data), [partq.data]);
  const { rows, finalBalance } = useMemo(
    () => projectStatement(txq.data ?? [], aid, { type: type ?? "Liability", knownAccounts: known }),
    [txq.data, aid, type, known],
  );

  return {
    rows: rows as StatementRow[],
    finalBalance,
    book: balq.data?.balance,
    isLoading: txq.isLoading || balq.isLoading,
    error: txq.error ?? balq.error,
    refetch: () => txq.refetch(),
  };
}

// --- Ledger: transactions -------------------------------------------------

export function useTransactions(pid: string, account?: string) {
  return useQuery({
    queryKey: qk.transactions(pid, account),
    queryFn: () => api.listTransactions(pid, account),
    enabled: pid !== "",
  });
}

export function useTransaction(pid: string, tid: string) {
  return useQuery({
    queryKey: qk.transaction(pid, tid),
    queryFn: () => api.getTransaction(pid, tid),
    enabled: pid !== "" && tid !== "",
  });
}

// Invalidate the participant's transactions, account balances and audit after
// any posting.
function invalidateLedger(
  qc: ReturnType<typeof useQueryClient>,
  pid: string,
) {
  qc.invalidateQueries({ queryKey: ["participants", pid, "transactions"] });
  qc.invalidateQueries({ queryKey: ["participants", pid, "accounts"] });
  qc.invalidateQueries({ queryKey: ["participants", pid, "audit"] });
}

export function usePostTransaction(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").PostTransactionRequest) =>
      api.postTransaction(pid, body),
    onSuccess: () => invalidateLedger(qc, pid),
  });
}

export function useReverseTransaction(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { tid: string; description: string }) =>
      api.reverseTransaction(pid, vars.tid, { description: vars.description }),
    onSuccess: () => invalidateLedger(qc, pid),
  });
}

export function useLedgerAudit(pid: string, q: AuditQuery = {}) {
  return useQuery({
    queryKey: qk.ledgerAudit(pid, q),
    queryFn: () => api.ledgerAudit(pid, q),
    enabled: pid !== "",
  });
}

// --- Deposit: accounts ----------------------------------------------------

// Invalidate the participant's whole deposit subtree (every account, balance,
// hold list and snapshot list) plus the deposit audit log. Used after any
// deposit mutation — broad, but the subtree is small and it's always correct,
// even for release/capture where we only have a hold id, not its account.
function invalidateDeposits(
  qc: ReturnType<typeof useQueryClient>,
  pid: string,
) {
  qc.invalidateQueries({ queryKey: qk.depositAccounts(pid) });
  qc.invalidateQueries({ queryKey: qk.depositAudit(pid) });
}

export function useDepositAccounts(pid: string) {
  return useQuery({
    queryKey: qk.depositAccounts(pid),
    queryFn: () => api.listDepositAccounts(pid),
    enabled: pid !== "",
  });
}

export function useDepositAccount(pid: string, did: string) {
  return useQuery({
    queryKey: qk.depositAccount(pid, did),
    queryFn: () => api.getDepositAccount(pid, did),
    enabled: pid !== "" && did !== "",
  });
}

export function useDepositBalance(pid: string, did: string) {
  return useQuery({
    queryKey: qk.depositBalance(pid, did),
    queryFn: () => api.getDepositBalance(pid, did),
    enabled: pid !== "" && did !== "",
  });
}

// Composes the GL transactions, the deposit balance, and the participant's
// well-known accounts into a ready-to-render statement. `glAccount` must be a
// real account id — call this only once the deposit account has loaded.
export function useStatement(pid: string, did: string, glAccount: string) {
  const txq = useTransactions(pid, glAccount);
  const balq = useDepositBalance(pid, did);
  const partq = useParticipant(pid);

  const known = useMemo(() => buildKnownAccounts(partq.data), [partq.data]);
  const { rows, finalBalance } = useMemo(
    () => projectStatement(txq.data ?? [], glAccount, { type: "Liability", knownAccounts: known }),
    [txq.data, glAccount, known],
  );

  return {
    rows: rows as StatementRow[],
    finalBalance,
    book: balq.data?.book,
    isLoading: txq.isLoading || balq.isLoading,
    error: txq.error ?? balq.error,
    refetch: () => txq.refetch(),
  };
}

export function useOpenDepositAccount(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").OpenDepositAccountRequest) =>
      api.openDepositAccount(pid, body),
    onSuccess: () => invalidateDeposits(qc, pid),
  });
}

export function useSetDepositStatus(pid: string, did: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").StatusRequest) =>
      api.setDepositStatus(pid, did, body),
    onSuccess: () => invalidateDeposits(qc, pid),
  });
}

export function useCloseDepositAccount(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (did: string) => api.closeDepositAccount(pid, did),
    onSuccess: () => invalidateDeposits(qc, pid),
  });
}

// Funds a deposit account and raises the bank's central-bank reserve in step,
// so this also refreshes reserves and the central-bank audit log.
export function useFundDeposit(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").FundRequest) =>
      api.fundDeposit(pid, body),
    onSuccess: () => {
      invalidateDeposits(qc, pid);
      qc.invalidateQueries({ queryKey: qk.reserves() });
      qc.invalidateQueries({ queryKey: qk.reserve(pid) });
      qc.invalidateQueries({ queryKey: qk.centralBankAudit() });
    },
  });
}

// --- Deposit: holds -------------------------------------------------------

export function useHolds(pid: string, did: string) {
  return useQuery({
    queryKey: qk.holds(pid, did),
    queryFn: () => api.listHolds(pid, did),
    enabled: pid !== "" && did !== "",
  });
}

export function useCreateHold(pid: string, did: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").CreateHoldRequest) =>
      api.createHold(pid, did, body),
    onSuccess: () => invalidateDeposits(qc, pid),
  });
}

export function useReleaseHold(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (hid: string) => api.releaseHold(pid, hid),
    onSuccess: () => invalidateDeposits(qc, pid),
  });
}

// Capturing posts a real ledger transaction, so it also refreshes the ledger
// (transactions, account balances, ledger audit).
export function useCaptureHold(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: {
      hid: string;
      body: import("../types").CaptureHoldRequest;
    }) => api.captureHold(pid, vars.hid, vars.body),
    onSuccess: () => {
      invalidateDeposits(qc, pid);
      invalidateLedger(qc, pid);
    },
  });
}

// --- Deposit: snapshots ---------------------------------------------------

export function useSnapshots(pid: string, did: string) {
  return useQuery({
    queryKey: qk.snapshots(pid, did),
    queryFn: () => api.listSnapshots(pid, did),
    enabled: pid !== "" && did !== "",
  });
}

export function useTakeSnapshot(pid: string, did: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").SnapshotRequest) =>
      api.takeSnapshot(pid, did, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.snapshots(pid, did) });
      qc.invalidateQueries({ queryKey: qk.depositAudit(pid) });
    },
  });
}

// --- Deposit: audit -------------------------------------------------------

export function useDepositAudit(pid: string, q: AuditQuery = {}) {
  return useQuery({
    queryKey: qk.depositAudit(pid, q),
    queryFn: () => api.depositAudit(pid, q),
    enabled: pid !== "",
  });
}

// --- Payment: audit -------------------------------------------------------

export function usePaymentAudit(q: AuditQuery = {}) {
  return useQuery({
    queryKey: qk.paymentAudit(q),
    queryFn: () => api.paymentAudit(q),
  });
}

// --- Payment: mandates ----------------------------------------------------

export function useMandates() {
  return useQuery({ queryKey: qk.mandates(), queryFn: api.listMandates });
}

export function useMandate(mid: string) {
  return useQuery({
    queryKey: qk.mandate(mid),
    queryFn: () => api.getMandate(mid),
    enabled: mid !== "",
  });
}

export function useCreateMandate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createMandate,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.mandates() });
      qc.invalidateQueries({ queryKey: qk.paymentAudit() });
    },
  });
}

export function useRevokeMandate() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (mid: string) => api.revokeMandate(mid),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.mandates() });
      qc.invalidateQueries({ queryKey: qk.paymentAudit() });
    },
  });
}

// --- Payment: payments / cycles / settlements -----------------------------

// A payment, clearing or settlement touches money across participants (deposit
// balances, reserves) and links payments↔cycles↔settlements. Rather than thread
// every affected id, invalidate the whole network plus all participant-scoped
// data — the teaching dataset is tiny and this is always correct.
function invalidateNetwork(qc: ReturnType<typeof useQueryClient>) {
  // qk.payments() is a prefix of qk.paymentAudit(), so the network's own audit
  // trail is refreshed by the first line here.
  qc.invalidateQueries({ queryKey: qk.payments() });
  qc.invalidateQueries({ queryKey: qk.cycles() });
  qc.invalidateQueries({ queryKey: qk.settlements() });
  qc.invalidateQueries({ queryKey: qk.reserves() });
  qc.invalidateQueries({ queryKey: qk.centralBankAudit() });
  qc.invalidateQueries({ queryKey: ["participants"] });
}

export function usePayments() {
  return useQuery({ queryKey: qk.payments(), queryFn: api.listPayments });
}

export function usePayment(payid: string) {
  return useQuery({
    queryKey: qk.payment(payid),
    queryFn: () => api.getPayment(payid),
    enabled: payid !== "",
  });
}

export function useInitiatePayment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.initiatePayment,
    onSuccess: () => invalidateNetwork(qc),
  });
}

export function useRejectPayment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { payid: string; reason: string }) =>
      api.rejectPayment(vars.payid, { reason: vars.reason }),
    onSuccess: () => invalidateNetwork(qc),
  });
}

export function useReturnPayment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { payid: string; reason: string }) =>
      api.returnPayment(vars.payid, { reason: vars.reason }),
    onSuccess: () => invalidateNetwork(qc),
  });
}

export function useCycles() {
  return useQuery({ queryKey: qk.cycles(), queryFn: api.listCycles });
}

export function useCycle(cid: string) {
  return useQuery({
    queryKey: qk.cycle(cid),
    queryFn: () => api.getCycle(cid),
    enabled: cid !== "",
  });
}

export function useOpenCycle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.openCycle,
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.cycles() }),
  });
}

export function useCloseCycle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (cid: string) => api.closeCycle(cid),
    onSuccess: () => invalidateNetwork(qc),
  });
}

export function useSettleCycle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (cid: string) => api.settleCycle(cid),
    onSuccess: () => invalidateNetwork(qc),
  });
}

export function useSettlements() {
  return useQuery({
    queryKey: qk.settlements(),
    queryFn: api.listSettlements,
  });
}

export function useSettlement(sid: string) {
  return useQuery({
    queryKey: qk.settlement(sid),
    queryFn: () => api.getSettlement(sid),
    enabled: sid !== "",
  });
}

// Reset the whole backend to the sample dataset, then invalidate every query so
// the UI refetches the fresh state.
export function useResetState() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.resetState,
    onSuccess: () => qc.invalidateQueries(),
  });
}
