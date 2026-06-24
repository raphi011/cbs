import type { Direction, TransactionStatus } from "@/lib/enums";
import { NORMAL_BALANCE } from "@/lib/enums";
import type { AccountType } from "@/lib/enums";
import type { Participant, Transaction } from "@/lib/types";

// One statement line is one leg of a GL transaction, projected onto the backing
// account. The tagged `kind` leaves room for future non-GL lines (interest,
// fees) without disturbing this path.
export type ContraRef =
  | { kind: "single"; accountId: string; label?: string }
  | { kind: "split"; count: number };

export type StatementRow = {
  kind: "gl-leg";
  txId: string;
  date: string; // valueDate
  description?: string;
  direction: Direction;
  delta: number; // signed cents: Credit +amount, Debit −amount
  runningBalance: number; // cents after this row, signed by the account's normal balance
  contra: ContraRef;
  status: TransactionStatus;
  isReversed: boolean;
  reversalOf?: string;
  transaction: Transaction; // full legs, for the inline expand
};

export type Statement = { rows: StatementRow[]; finalBalance: number };

// Map a participant's well-known accounts to friendly contra labels, so a
// suspense/reserve/settlement leg reads as a word instead of an opaque ID.
export function buildKnownAccounts(participant?: Participant): Record<string, string> {
  if (!participant) return {};
  return {
    [participant.suspenseAccount]: "suspense",
    [participant.reserveAccount]: "reserve",
    [participant.settlementAccount]: "settlement",
  };
}

// Project the General Ledger onto a single backing account. Rows are returned
// newest-first; the running balance is accumulated over the FULL ordered history
// (oldest→newest) so the newest row reconciles to the account's book balance.
// Amounts are signed by the account's normal balance (Debit+ for Asset/Expense,
// Credit+ for Liability/Equity/Revenue); `type` defaults to "Liability".
export function projectStatement(
  txs: Transaction[],
  accountId: string,
  opts: { type?: AccountType; knownAccounts?: Record<string, string> } = {},
): Statement {
  const { type = "Liability", knownAccounts = {} } = opts;
  const increases = NORMAL_BALANCE[type]; // the direction that increases this account

  const touching = txs.filter((t) => t.entries.some((e) => e.accountId === accountId));

  const ordered = [...touching].sort((a, b) => {
    if (a.valueDate !== b.valueDate) return a.valueDate < b.valueDate ? -1 : 1;
    if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
    return 0;
  });

  let running = 0;
  const rows: StatementRow[] = ordered.map((t) => {
    const mine = t.entries.filter((e) => e.accountId === accountId);
    const others = t.entries.filter((e) => e.accountId !== accountId);

    const delta = mine.reduce(
      (sum, e) => sum + (e.direction === increases ? e.amount : -e.amount),
      0,
    );
    running += delta;

    const contra: ContraRef =
      others.length === 1
        ? { kind: "single", accountId: others[0].accountId, label: knownAccounts[others[0].accountId] }
        : { kind: "split", count: others.length };

    return {
      kind: "gl-leg",
      txId: t.id,
      date: t.valueDate,
      description: t.description,
      // A deposit account touches a transaction with exactly one leg in
      // practice, so this is that leg's direction. `delta` (above) sums all
      // `mine` legs and is what the signed amount renders from, so the running
      // balance stays correct even in the exotic multi-leg-same-account case.
      direction: mine[0].direction,
      delta,
      runningBalance: running,
      contra,
      status: t.status,
      isReversed: t.status === "Reversed",
      reversalOf: t.reversalOf,
      transaction: t,
    };
  });

  return { rows: rows.reverse(), finalBalance: running };
}
