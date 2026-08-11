// TanStack Query hooks wrapping the endpoint functions. Queries use the key
// factory; mutations invalidate affected keys. Grows per milestone.

import { useMemo } from "react";

import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

import { buildKnownAccounts, projectStatement } from "@/lib/statement";
import type { StatementRow } from "@/lib/statement";
import type { AccountType } from "@/lib/enums";
import { backendFor } from "@/lib/identity";
import type { Asset, AuditQuery, CreateMandateRequest, DepositAccount, Participant } from "@/lib/types";

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

// --- Operators (Next-side, not a backend area) ----------------------------

// Which operators have a listener behind them, and a predicate over the answer.
//
// staleTime is Infinity because ports are static by design: a bank admitted at
// runtime gets no listener until the server restarts, so the answer cannot change
// under a running page. Re-probing six listeners on every mount would be waste.
export function useOperators() {
  return useQuery({
    queryKey: qk.operators(),
    queryFn: api.listOperators,
    staleTime: Infinity,
  });
}

// Answers `backendFor(identity)`. Optimistic while the probe is in flight and
// when it failed: an unknown answer must not make a working console look dead.
export function useIsProvisioned(): (operatorKey: string) => boolean {
  const { data } = useOperators();
  return (operatorKey: string) => {
    const row = data?.find((o) => o.operator === operatorKey);
    return row ? row.live : true;
  };
}

// --- Schemes --------------------------------------------------------------

export function useSchemes() {
  return useQuery({ queryKey: qk.schemes(), queryFn: api.listSchemes });
}

// --- Directory --------------------------------------------------------------

// The clearing house's routing directory: every address the scheme will send to.
export function useRoster() {
  return useQuery({ queryKey: qk.roster(), queryFn: api.listRoster });
}

// An address resolved in one bank's own register. `retry: false` because a 404
// here is an answer — this bank does not hold that IBAN — and retrying it three
// times only delays saying so.
export function useBankDirectory(pid: string, scheme: string, value: string) {
  return useQuery({
    queryKey: qk.bankDirectory(pid, scheme, value),
    queryFn: () => api.resolveIdentifierAtBank(pid, scheme, value),
    enabled: pid !== "" && scheme !== "" && value !== "",
    retry: false,
  });
}

// One bank's copy of the scheme's routing directory, every row carrying the
// instant of the pull that wrote it.
export function useRoutingDirectory(pid: string) {
  return useQuery({
    queryKey: qk.bankRouting(pid),
    queryFn: () => api.listRoutingDirectory(pid),
    enabled: pid !== "",
  });
}

// Where one address routes, out of this bank's copy. `retry: false` for the same
// reason as the register lookup: a 422 here is an answer — this copy holds no
// entry for that bank code — and retrying only delays saying so. A send form
// calls this once an IBAN's check digits pass, and it is deliberately the same
// read submission makes, so what the form shows and where the payment goes
// cannot disagree.
export function useAddressRoute(pid: string, iban: string) {
  return useQuery({
    queryKey: qk.bankRoutingFor(pid, iban),
    queryFn: () => api.routeForAddress(pid, iban),
    enabled: pid !== "" && iban !== "",
    retry: false,
  });
}

// The subscription. It invalidates the whole of this bank's routing subtree,
// so the console's list and any address a form has resolved are both re-read
// from the copy the pull just wrote.
export function useRefreshRoutingDirectory(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.refreshRoutingDirectory(pid),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.bankRouting(pid) });
    },
  });
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

export function useCentralBankCycles() {
  return useQuery({
    queryKey: qk.centralBankCycles(),
    queryFn: api.centralBankCycles,
  });
}

export function useCentralBankSettlements() {
  return useQuery({
    queryKey: qk.centralBankSettlements(),
    queryFn: api.centralBankSettlements,
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
  return { byCode, isLoading: q.isLoading, error: q.error, refetch: () => q.refetch() };
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

export function useTransactions(pid: string, account?: string, subsidiary?: string) {
  return useQuery({
    queryKey: qk.transactions(pid, account, subsidiary),
    queryFn: () => api.listTransactions(pid, account, subsidiary),
    enabled: pid !== "",
  });
}

// Who a control account is holding money for. Empty for a plain account, and
// empty is what the page renders as "this line stands in for nobody".
export function useSubsidiaries(pid: string, aid: string) {
  return useQuery({
    queryKey: qk.subsidiaries(pid, aid),
    queryFn: () => api.listSubsidiaries(pid, aid),
    enabled: pid !== "" && aid !== "",
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

export function useProducts(pid: string) {
  return useQuery({
    queryKey: qk.products(pid),
    queryFn: () => api.listProducts(pid),
    enabled: pid !== "",
  });
}

export function useProductVersions(pid: string, prid: string) {
  return useQuery({
    queryKey: qk.productVersions(pid, prid),
    queryFn: () => api.listProductVersions(pid, prid),
    enabled: pid !== "" && prid !== "",
  });
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
// well-known accounts into a ready-to-render statement.
//
// `controlAccount` must be a real account id — call this only once the deposit
// account has loaded — and the statement is that account's postings UNDER THIS
// CUSTOMER. Dropping the customer would render the whole bank's deposits as one
// customer's statement, which is what a control account without its subsidiary is.
export function useStatement(pid: string, did: string, controlAccount: string) {
  const txq = useTransactions(pid, controlAccount, did);
  const balq = useDepositBalance(pid, did);
  const partq = useParticipant(pid);

  const known = useMemo(() => buildKnownAccounts(partq.data), [partq.data]);
  const { rows, finalBalance } = useMemo(
    () =>
      projectStatement(txq.data ?? [], controlAccount, {
        type: "Liability",
        knownAccounts: known,
        subsidiary: did,
      }),
    [txq.data, controlAccount, did, known],
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

// Takes cash in: the customer's balance rises and the bank holds the cash.
//
// It invalidates this bank's deposits and NOTHING of the central bank's. A
// deposit reaches the bank's own vault and no institution but that bank, so
// invalidating the central bank's queries would be re-fetching data this call
// cannot have changed. useLodgeReserves is what moves them.
export function useFundDeposit(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").FundRequest) =>
      api.fundDeposit(pid, body),
    onSuccess: () => invalidateDeposits(qc, pid),
  });
}

// Moves money between two of one bank's own customers.
//
// It invalidates this bank's deposits and nothing else, and the list of what is
// NOT here is the point: no reserve, no cycle, no settlement, no payment. Nothing
// outside this institution changed, so there is nothing outside this institution
// to re-fetch — which is the same claim the reconciliation harness makes about a
// book transfer, expressed as a cache.
//
// Both accounts are in that one subtree, so the payee's balance is re-fetched
// too even though the response carries only the payer's.
export function useTransfer(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").TransferRequest) =>
      api.transfer(pid, body),
    onSuccess: () => invalidateDeposits(qc, pid),
  });
}

// Places the bank's vault cash on reserve, which is what actually moves a
// reserve — so this is where the central bank's queries are invalidated.
//
// # The invalidation reads a figure that has not moved yet, deliberately
//
// The route answers 202: the reserve credit is the central bank's to make, and
// it makes it when the business day next runs. The camt.050 is uploaded now and
// waits in the central bank's inbox, so the re-fetch triggered here reads the
// reserve BEFORE the central bank has posted — not as a race, but because that
// is genuinely where the money is.
//
// Invalidating anyway is right: the alternative is not invalidating, which leaves
// a stale figure on screen for ever, and the day's advance invalidates the same
// keys when the credit actually lands.
//
// qk.participant(pid) is the whole of this bank's subtree — ledger and deposit keys
// nest under it — and it is here because the bank's own Vault Cash and Reserve at
// Central Bank accounts both moved. One invalidate rather than one per account
// list, which is what that key layout is for.
export function useLodgeReserves(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").LodgementRequest) =>
      api.lodgeReserves(pid, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.reserves() });
      qc.invalidateQueries({ queryKey: qk.reserve(pid) });
      qc.invalidateQueries({ queryKey: qk.centralBankAudit() });
      qc.invalidateQueries({ queryKey: qk.participant(pid) });
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

// --- Deposit: overdraft terms -----------------------------------------------

export function useOverdraftTerms(pid: string, did: string) {
  return useQuery({
    queryKey: qk.overdraftTerms(pid, did),
    queryFn: () => api.listOverdraftTerms(pid, did),
    enabled: pid !== "" && did !== "",
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

// --- Lending: facilities ----------------------------------------------------

// Invalidate a participant's whole facilities subtree (list, every facility's
// detail, and every facility's schedule) — the same broad-but-always-correct
// approach invalidateDeposits takes.
function invalidateFacilities(
  qc: ReturnType<typeof useQueryClient>,
  pid: string,
) {
  qc.invalidateQueries({ queryKey: qk.facilities(pid) });
}

export function useFacilities(pid: string) {
  return useQuery({
    queryKey: qk.facilities(pid),
    queryFn: () => api.listFacilities(pid),
    enabled: pid !== "",
  });
}

export function useFacility(pid: string, fid: string) {
  return useQuery({
    queryKey: qk.facility(pid, fid),
    queryFn: () => api.getFacility(pid, fid),
    enabled: pid !== "" && fid !== "",
  });
}

export function useFacilitySchedule(pid: string, fid: string) {
  return useQuery({
    queryKey: qk.facilitySchedule(pid, fid),
    queryFn: () => api.getFacilitySchedule(pid, fid),
    enabled: pid !== "" && fid !== "",
  });
}

export function useOpenFacility(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").OpenFacilityRequest) =>
      api.openFacility(pid, body),
    onSuccess: () => invalidateFacilities(qc, pid),
  });
}

// Charging a revolving line's interest capitalizes it into drawn principal and
// bills a new instalment, so this refreshes the facility subtree; only a cycle
// that actually POSTED touched the ledger (see api.chargeFacilityInterest —
// billing and posting are independent), so the ledger is refreshed on that
// alone, the same way useCaptureHold does for a hold capture.
export function useChargeFacilityInterest(pid: string, fid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").ChargeFacilityInterestRequest) =>
      api.chargeFacilityInterest(pid, fid, body),
    onSuccess: (charge) => {
      invalidateFacilities(qc, pid);
      if (charge?.transaction) invalidateLedger(qc, pid);
    },
  });
}

// --- Lending: totals ---------------------------------------------------------

export function useTotals(pid: string) {
  return useQuery({
    queryKey: qk.totals(pid),
    queryFn: () => api.getTotals(pid),
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

export function useMandates(pid: string) {
  return useQuery({
    queryKey: qk.mandates(pid),
    queryFn: () => api.listMandates(pid),
    enabled: pid !== "",
  });
}

export function useMandate(pid: string, mid: string) {
  return useQuery({
    queryKey: qk.mandate(pid, mid),
    queryFn: () => api.getMandate(pid, mid),
    enabled: pid !== "" && mid !== "",
  });
}

export function useCreateMandate(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateMandateRequest) => api.createMandate(pid, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.mandates(pid) });
      qc.invalidateQueries({ queryKey: qk.paymentAudit() });
    },
  });
}

export function useRevokeMandate(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (mid: string) => api.revokeMandate(pid, mid),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.mandates(pid) });
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
  qc.invalidateQueries({ queryKey: qk.centralBankCycles() });
  qc.invalidateQueries({ queryKey: qk.centralBankSettlements() });
  qc.invalidateQueries({ queryKey: ["participants"] });
}

export function usePayments() {
  return useQuery({ queryKey: qk.payments(), queryFn: api.listPayments });
}

export function useBankPayments(pid: string) {
  return useQuery({
    queryKey: qk.bankPayments(pid),
    queryFn: () => api.bankPayments(pid),
    enabled: pid !== "",
  });
}

// The second half of a 202: ask about the identifier you were given. The wait is
// real — the bank answers with an identifier and the counterparty's answer
// arrives at another actor, as a pacs.002 — so this is where the outcome comes
// from and not a formality.
export function useBankPayment(pid: string, payid: string) {
  return useQuery({
    queryKey: qk.bankPayment(pid, payid),
    queryFn: () => api.bankPayment(pid, payid),
    enabled: pid !== "" && payid !== "",
  });
}

export function useSubmitPayment(pid: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: import("../types").InitiatePaymentRequest) =>
      api.submitPayment(pid, body),
    onSuccess: () => invalidateNetwork(qc),
  });
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

// Ask the clearing house to instruct settlement again, for a cycle the central
// bank refused. It is not a second way to settle — see api.settleCycle — and
// the 202 it answers says only that the pacs.009 went out. The network refetch
// is what surfaces the answer, which arrives at another actor.
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

// --- Identities -------------------------------------------------------------

// Every identity in the system, in one place: each member bank with its customer
// accounts. The lobby and the identity picker both need exactly this, so they
// share one set of queries rather than each fetching its own.
//
// The honest cost of the split is visible here. The bank list comes from the
// central bank's GET /members, and each bank's accounts come from that bank's
// own listener — so drawing one list touches five backends. That is why this is
// a shared hook rather than an optimisation.
//
// A bank admitted at runtime has a store row and no listener until the server
// restarts. Its query is not fired at all: it would 502 forever, and four
// failing requests plus a dead console is a worse answer than one row saying so.
export interface BankEntry {
  participant: Participant;
  accounts: DepositAccount[];
  provisioned: boolean;
}

export function useIdentityDirectory(): {
  banks: BankEntry[];
  isLoading: boolean;
  error: unknown;
} {
  const participants = useParticipants();
  const operators = useOperators();
  const isProvisioned = useIsProvisioned();
  const list = participants.data ?? [];

  // The probe has spoken when it has an answer or has failed. Waiting is the
  // only state that must not fire a query: an unprovisioned bank's fetch 502s
  // and, with retry: false, that error is cached for the life of the page. A
  // failed probe is not an answer, so it falls back to the same optimism
  // `isProvisioned` already applies — better a 502 on one bank than every
  // bank silently dark because the probe itself couldn't be reached.
  const probeSettled = operators.data !== undefined || operators.isError;

  const results = useQueries({
    queries: list.map((p) => ({
      queryKey: qk.depositAccounts(p.id),
      queryFn: () => api.listDepositAccounts(p.id),
      enabled:
        probeSettled && isProvisioned(backendFor({ persona: "bank", pid: p.id })),
    })),
  });

  const banks = list.map((participant, i) => ({
    participant,
    accounts: results[i]?.data ?? [],
    provisioned: isProvisioned(
      backendFor({ persona: "bank", pid: participant.id }),
    ),
  }));

  return {
    banks,
    // The probe is part of the load: until it answers, "provisioned" is a guess
    // and firing the per-bank queries on a guess is what this exists to avoid.
    isLoading:
      participants.isLoading || operators.isLoading || results.some((r) => r.isLoading),
    // The roster is the page: no roster, no cast to show. One bank's accounts
    // failing to load is not — that bank's card degrades on its own (an empty
    // customer list) and the rest of the lobby stands, so a per-bank fetch
    // error never becomes a page-level error here.
    error: participants.error ?? null,
  };
}

// Which day the deployment is on.
//
// Every shell reads it, so it is cached like any other query and refetched when
// a day is advanced or the data is reset — both of which move it.
export function useClock() {
  return useQuery({ queryKey: qk.clock(), queryFn: api.getClock });
}

// Advance the deployment by one business day, then invalidate every query.
//
// Every query, because a day moves almost everything: payments clear and settle,
// reserves move, statements are booked, interest accrues, and the date itself
// changes. Naming the subset would be naming the whole of the domain.
export function useAdvanceDay() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.advanceDay,
    onSuccess: () => qc.invalidateQueries(),
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
