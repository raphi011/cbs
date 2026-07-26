// Central query-key factory. Mutations invalidate by these keys so caches stay
// consistent (e.g. funding a participant invalidates that participant's
// balances). Keys grow as milestones add screens.

export const qk = {
  participants: () => ["participants"] as const,
  participant: (pid: string) => ["participants", pid] as const,

  schemes: () => ["schemes"] as const,

  reserves: () => ["central-bank", "reserves"] as const,
  reserve: (pid: string) => ["central-bank", "reserves", pid] as const,
  centralBankAudit: () => ["central-bank", "audit"] as const,

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
  ledgerAudit: (pid: string, entity?: string) =>
    entity
      ? (["participants", pid, "audit", { entity }] as const)
      : (["participants", pid, "audit"] as const),

  // Deposit layer. Balances, holds and snapshots nest under the account so a
  // single invalidate of ["participants", pid, "deposit-accounts"] refreshes
  // the whole subtree — handy when a release/capture only gives us a hold id.
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
  depositAudit: (pid: string) =>
    ["participants", pid, "deposit-audit"] as const,

  // Payment network (global — each object spans two participants).
  mandates: () => ["mandates"] as const,
  mandate: (mid: string) => ["mandates", mid] as const,
  payments: () => ["payments"] as const,
  payment: (payid: string) => ["payments", payid] as const,
  cycles: () => ["cycles"] as const,
  cycle: (cid: string) => ["cycles", cid] as const,
  settlements: () => ["settlements"] as const,
  settlement: (sid: string) => ["settlements", sid] as const,
};
