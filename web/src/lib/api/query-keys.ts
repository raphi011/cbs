// Central query-key factory. Mutations invalidate by these keys so caches stay
// consistent (e.g. funding a participant invalidates that participant's
// balances). Keys grow as milestones add screens.

import type { AuditQuery, MessageQuery } from "../types";

// narrowedKey appends the filter to a log's key only when there is one, so the
// unfiltered key stays a prefix of every filtered one and a single
// invalidateQueries refreshes every page of a log.
function narrowedKey(base: readonly string[], q?: AuditQuery | MessageQuery) {
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
    narrowedKey(["central-bank", "audit"], q),
  // Keyed under the central bank rather than shared with the clearing house's
  // cycles(): the same rows read from a different listener, which can be
  // individually down, and for a different reason.
  centralBankCycles: () => ["central-bank", "cycles"] as const,
  // And the settlements it performed, likewise read from its own listener
  // rather than the clearing house's. Same rows, different operator: the
  // central bank's console must not be reading another operator's port to find
  // out what the central bank did.
  centralBankSettlements: () => ["central-bank", "settlements"] as const,

  // Network-wide: assets are defined in code, not per book.
  assets: () => ["assets"] as const,

  // The deployment's business date. Not keyed under the central bank although
  // it is read from that listener: the clock belongs to no institution, and a
  // key that said otherwise would invite a second one per operator.
  clock: () => ["clock"] as const,

  // The day's declared steps. Not keyed by date: which phases a day HAS is
  // fixed before the process starts, and only which of them run varies.
  phases: () => ["clock", "phases"] as const,

  // What an operator may trigger. Fixed before the process starts — a scenario
  // is a Go function in a registry, not data — so this is fetched once and
  // never invalidated by anything that happens.
  scenarios: () => ["scenarios"] as const,

  // The mesh, keyed under nobody. It is the deployment's read of every
  // institution at once, so keying it under the listener that serves it would
  // say it was the settlement agent's — which is the one thing it is not.
  networkFlow: (limit?: number) =>
    limit === undefined ? (["network-flow"] as const) : (["network-flow", limit] as const),

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
  subsidiaries: (pid: string, aid: string) =>
    ["participants", pid, "accounts", aid, "subsidiaries"] as const,
  transactions: (pid: string, account?: string, subsidiary?: string) =>
    account
      ? (["participants", pid, "transactions", { account, subsidiary }] as const)
      : (["participants", pid, "transactions"] as const),
  transaction: (pid: string, tid: string) =>
    ["participants", pid, "transaction", tid] as const,
  ledgerAudit: (pid: string, q?: AuditQuery) =>
    narrowedKey(["participants", pid, "audit"], q),

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
    narrowedKey(["participants", pid, "deposit-audit"], q),

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

  // The clearing house's routing directory. Not keyed by an address: it is the
  // whole list, because "who may be addressed" has no argument.
  roster: () => ["clearing-house", "roster"] as const,
  bankPayment: (pid: string, payid: string) =>
    ["participants", pid, "payments", payid] as const,
  bankDirectory: (pid: string, scheme: string, value: string) =>
    ["participants", pid, "directory", scheme, value] as const,

  // One bank's COPY of the scheme's directory. Nested under the participant
  // because it is that bank's table and two banks' copies are two different
  // answers — one may be behind the other, which is the whole subscription
  // model. A refresh invalidates the subtree, so the list and every resolved
  // bank code refetch together.
  bankRouting: (pid: string) => ["participants", pid, "routing"] as const,
  bankRoutingFor: (pid: string, iban: string) =>
    ["participants", pid, "routing", "iban", iban] as const,

  // Payment network (global — each object spans two participants).
  // Keyed by BANK, because a mandate is one bank's row and two banks' listings
  // are two different answers rather than one cache entry.
  mandates: (pid: string) => ["bank", pid, "mandates"] as const,
  mandate: (pid: string, mid: string) => ["bank", pid, "mandates", mid] as const,
  payments: () => ["payments"] as const,
  payment: (payid: string) => ["payments", payid] as const,
  // Nested under the participant, because these are that bank's legs and not
  // the network's list filtered.
  bankPayments: (pid: string) => ["participants", pid, "payments"] as const,
  cycles: () => ["cycles"] as const,
  cycle: (cid: string) => ["cycles", cid] as const,
  paymentAudit: (q?: AuditQuery) => narrowedKey(["payments", "audit"], q),

  // The clearing house's own message log. Keyed under that institution because
  // a log is one institution's record of its own traffic: two institutions'
  // logs are two different answers and a seq means nothing across them. The
  // unfiltered key is a prefix of every narrowed one, so one invalidate
  // refreshes a payment's files and the whole listing alike.
  messages: (q?: MessageQuery) => narrowedKey(["clearing-house", "messages"], q),

  // A document, keyed outside that prefix on purpose: a log row is written once
  // and never changed, so the one read that carries the bytes must not be
  // refetched by every invalidate of the listing above it.
  //
  // Keyed by the INSTITUTION as well as the seq, because a seq counts one
  // institution's own traffic: the same number names a different file at every
  // other listener, and one cache entry for both would hand a reader the wrong
  // document.
  messageDocument: (holder: string, seq: number) =>
    ["message-document", holder, seq] as const,
  settlements: () => ["settlements"] as const,
  settlement: (sid: string) => ["settlements", sid] as const,
};
