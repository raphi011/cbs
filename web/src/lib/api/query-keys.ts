// Central query-key factory. Mutations invalidate by these keys so caches stay
// consistent (e.g. funding a participant invalidates that participant's
// balances). Keys grow as milestones add screens.

import type { AuditQuery } from "../types";

// auditKey appends the filter to an audit key only when there is one, so the
// unfiltered key stays a prefix of every filtered one and a single
// invalidateQueries refreshes every page of a log.
function auditKey(base: readonly string[], q?: AuditQuery) {
  return q && Object.values(q).some((v) => v !== undefined)
    ? ([...base, q] as const)
    : (base as readonly string[]);
}

export const qk = {
  participants: () => ["participants"] as const,
  participant: (pid: string) => ["participants", pid] as const,

  schemes: () => ["schemes"] as const,

  reserves: () => ["central-bank", "reserves"] as const,
  reserve: (pid: string) => ["central-bank", "reserves", pid] as const,
  centralBankAudit: (q?: AuditQuery) =>
    auditKey(["central-bank", "audit"], q),
  // Keyed under the central bank rather than shared with the clearing house's
  // cycles(): the same rows read from a different listener, which can be
  // individually down, and for a different reason.
  centralBankCycles: () => ["central-bank", "cycles"] as const,

  // Network-wide: assets are defined in code, not per book.
  assets: () => ["assets"] as const,

  // Next-side, not a backend area: which listeners are actually there.
  operators: () => ["operators"] as const,

  // Ledger layer (all nested under the participant so a post can invalidate
  // a whole subtree at once).
  ledgers: (pid: string) => ["participants", pid, "ledgers"] as const,
  subledgers: (pid: string, lid: string) =>
    ["participants", pid, "ledgers", lid, "subledgers"] as const,
  accounts: (pid: string, sid: string) =>
    ["participants", pid, "subledgers", sid, "accounts"] as const,
  accountBalance: (pid: string, aid: string) =>
    ["participants", pid, "accounts", aid, "balance"] as const,
  // Flattened chart of accounts for pickers (refetched on mount, so a freshly
  // created account shows up next time a form opens).
  allAccounts: (pid: string) =>
    ["participants", pid, "all-accounts"] as const,
  transactions: (pid: string, account?: string) =>
    account
      ? (["participants", pid, "transactions", { account }] as const)
      : (["participants", pid, "transactions"] as const),
  transaction: (pid: string, tid: string) =>
    ["participants", pid, "transaction", tid] as const,
  ledgerAudit: (pid: string, q?: AuditQuery) =>
    auditKey(["participants", pid, "audit"], q),

  // Deposit layer. Balances, holds and snapshots nest under the account so a
  // single invalidate of ["participants", pid, "deposit-accounts"] refreshes
  // the whole subtree — handy when a release/capture only gives us a hold id.
  // Product catalogue. Versions nest under the product, which nests under the
  // list, so one invalidate of products(pid) refreshes a timeline too.
  products: (pid: string) => ["participants", pid, "products"] as const,
  product: (pid: string, prid: string) =>
    ["participants", pid, "products", prid] as const,
  productVersions: (pid: string, prid: string) =>
    ["participants", pid, "products", prid, "versions"] as const,

  depositAccounts: (pid: string) =>
    ["participants", pid, "deposit-accounts"] as const,
  depositAccount: (pid: string, did: string) =>
    ["participants", pid, "deposit-accounts", did] as const,
  depositBalance: (pid: string, did: string) =>
    ["participants", pid, "deposit-accounts", did, "balance"] as const,
  holds: (pid: string, did: string) =>
    ["participants", pid, "deposit-accounts", did, "holds"] as const,
  hold: (pid: string, hid: string) =>
    ["participants", pid, "holds", hid] as const,
  snapshots: (pid: string, did: string) =>
    ["participants", pid, "deposit-accounts", did, "snapshots"] as const,
  overdraftTerms: (pid: string, did: string) =>
    ["participants", pid, "deposit-accounts", did, "overdraft-terms"] as const,
  depositAudit: (pid: string, q?: AuditQuery) =>
    auditKey(["participants", pid, "deposit-audit"], q),

  // Lending layer. The schedule nests under the facility, which nests under
  // the list, so one invalidate of facilities(pid) refreshes a facility's
  // detail page and its schedule too — the same trick depositAccounts(pid)
  // plays for holds and snapshots.
  facilities: (pid: string) => ["participants", pid, "facilities"] as const,
  facility: (pid: string, fid: string) =>
    ["participants", pid, "facilities", fid] as const,
  facilitySchedule: (pid: string, fid: string) =>
    ["participants", pid, "facilities", fid, "schedule"] as const,
  totals: (pid: string) => ["participants", pid, "totals"] as const,

  // Keyed by the listener that answered as well as the address: the same
  // question asked of the clearing house and of a bank are two different
  // requests, and only one of them is a customer's to make.
  csmDirectory: (scheme: string, value: string) =>
    ["clearing-house", "directory", scheme, value] as const,

  // Payment network (global — each object spans two participants).
  mandates: () => ["mandates"] as const,
  mandate: (mid: string) => ["mandates", mid] as const,
  payments: () => ["payments"] as const,
  payment: (payid: string) => ["payments", payid] as const,
  cycles: () => ["cycles"] as const,
  cycle: (cid: string) => ["cycles", cid] as const,
  paymentAudit: (q?: AuditQuery) => auditKey(["payments", "audit"], q),
  settlements: () => ["settlements"] as const,
  settlement: (sid: string) => ["settlements", sid] as const,
};
