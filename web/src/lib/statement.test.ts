import { describe, expect, it } from "vitest";

import { buildKnownAccounts, projectStatement } from "@/lib/statement";
import type { Direction, TransactionStatus } from "@/lib/enums";
import type { Entry, Participant, Transaction } from "@/lib/types";

const A = "acct_11"; // backing account (a liability)

function leg(accountId: string, amount: number, direction: Direction): Entry {
  return { accountId, amount, direction };
}

function tx(p: {
  id: string;
  entries: Entry[];
  valueDate?: string;
  createdAt?: string;
  status?: TransactionStatus;
  description?: string;
  reversalOf?: string;
}): Transaction {
  const valueDate = p.valueDate ?? "2026-06-01";
  return {
    id: p.id,
    entries: p.entries,
    bookingDate: valueDate,
    valueDate,
    status: p.status ?? "Posted",
    description: p.description,
    reversalOf: p.reversalOf,
    createdAt: p.createdAt ?? `${valueDate}T00:00:00.000Z`,
  };
}

describe("projectStatement", () => {
  it("signs by liability convention, accumulates a running balance, returns newest-first", () => {
    const txs = [
      tx({ id: "t1", valueDate: "2026-06-01", entries: [leg(A, 50000, "Credit"), leg("acct_02", 50000, "Debit")] }),
      tx({ id: "t2", valueDate: "2026-06-12", entries: [leg(A, 12000, "Credit"), leg("acct_03", 12000, "Debit")] }),
      tx({ id: "t3", valueDate: "2026-06-15", entries: [leg(A, 4550, "Debit"), leg("acct_03", 4550, "Credit")] }),
    ];

    const { rows, finalBalance } = projectStatement(txs, A);

    expect(finalBalance).toBe(57450);
    expect(rows.map((r) => r.txId)).toEqual(["t3", "t2", "t1"]); // newest first
    expect(rows[0]).toMatchObject({ delta: -4550, direction: "Debit", runningBalance: 57450 });
    expect(rows[1]).toMatchObject({ delta: 12000, direction: "Credit", runningBalance: 62000 });
    expect(rows[2]).toMatchObject({ delta: 50000, direction: "Credit", runningBalance: 50000 });
  });

  it("returns an empty statement for no transactions", () => {
    expect(projectStatement([], A)).toEqual({ rows: [], finalBalance: 0 });
  });

  it("orders by valueDate then createdAt", () => {
    const txs = [
      tx({ id: "late", valueDate: "2026-06-10", createdAt: "2026-06-10T09:00:00.000Z", entries: [leg(A, 100, "Credit"), leg("acct_02", 100, "Debit")] }),
      tx({ id: "early", valueDate: "2026-06-10", createdAt: "2026-06-10T08:00:00.000Z", entries: [leg(A, 200, "Credit"), leg("acct_02", 200, "Debit")] }),
    ];

    const { rows } = projectStatement(txs, A);

    // newest-first: the later createdAt comes first; running balances reflect early→late accumulation
    expect(rows.map((r) => r.txId)).toEqual(["late", "early"]);
    expect(rows[0].runningBalance).toBe(300);
    expect(rows[1].runningBalance).toBe(200);
  });

  it("flags reversed originals and reversal rows; the pair nets to zero", () => {
    const txs = [
      tx({ id: "orig", valueDate: "2026-06-15", status: "Reversed", entries: [leg(A, 4550, "Debit"), leg("acct_03", 4550, "Credit")] }),
      tx({ id: "rev", valueDate: "2026-06-20", reversalOf: "orig", entries: [leg(A, 4550, "Credit"), leg("acct_03", 4550, "Debit")] }),
    ];

    const { rows, finalBalance } = projectStatement(txs, A);

    expect(finalBalance).toBe(0);
    const orig = rows.find((r) => r.txId === "orig")!;
    const rev = rows.find((r) => r.txId === "rev")!;
    expect(orig.isReversed).toBe(true);
    expect(rev.reversalOf).toBe("orig");
  });

  it("signs by the account's normal balance — an asset rises on a Debit", () => {
    const R = "acct_reserve"; // an asset (normal balance = Debit)
    const txs = [
      tx({ id: "f1", valueDate: "2026-06-01", entries: [leg(R, 50000, "Debit"), leg("acct_11", 50000, "Credit")] }),
      tx({ id: "f2", valueDate: "2026-06-05", entries: [leg(R, 20000, "Credit"), leg("acct_11", 20000, "Debit")] }),
    ];

    const { rows, finalBalance } = projectStatement(txs, R, { type: "Asset" });

    // Asset: Debit +, Credit − (the opposite of the liability convention)
    expect(finalBalance).toBe(30000);
    expect(rows.map((r) => r.txId)).toEqual(["f2", "f1"]); // newest first
    expect(rows[0]).toMatchObject({ delta: -20000, direction: "Credit", runningBalance: 30000 });
    expect(rows[1]).toMatchObject({ delta: 50000, direction: "Debit", runningBalance: 50000 });
  });

  it("defaults to the liability convention when no type is given", () => {
    const txs = [tx({ id: "t1", entries: [leg(A, 100, "Credit"), leg("acct_02", 100, "Debit")] })];
    expect(projectStatement(txs, A).rows[0].delta).toBe(100); // Credit increases a liability
  });

  it("labels a single contra leg and collapses multi-leg contras to a split", () => {
    const single = tx({ id: "s", entries: [leg(A, 100, "Credit"), leg("acct_03", 100, "Debit")] });
    const split = tx({ id: "m", entries: [leg(A, 100, "Credit"), leg("acct_03", 60, "Debit"), leg("acct_07", 40, "Debit")] });

    const { rows } = projectStatement([single, split], A, { knownAccounts: { acct_03: "suspense" } });
    const sRow = rows.find((r) => r.txId === "s")!;
    const mRow = rows.find((r) => r.txId === "m")!;

    expect(sRow.contra).toEqual({ kind: "single", accountId: "acct_03", label: "suspense" });
    expect(mRow.contra).toEqual({ kind: "split", count: 2 });
  });
});

describe("buildKnownAccounts", () => {
  it("maps a participant's well-known accounts to friendly labels", () => {
    const p: Participant = {
      id: "bank_1",
      name: "Bank",
      customerSubledger: "sub_1",
      suspenseAccount: "acct_03",
      reserveAccount: "acct_02",
      settlementAccount: "acct_09",
    };
    expect(buildKnownAccounts(p)).toEqual({ acct_03: "suspense", acct_02: "reserve", acct_09: "settlement" });
    expect(buildKnownAccounts(undefined)).toEqual({});
  });

  it("combines roles when one account serves several (reserve === settlement)", () => {
    const p: Participant = {
      id: "bank_1",
      name: "Bank",
      customerSubledger: "sub_1",
      suspenseAccount: "acct_07",
      reserveAccount: "acct_09",
      settlementAccount: "acct_09",
    };
    // acct_09 is both reserve and settlement — the label must keep both, not
    // silently drop one (an object literal would clobber to just "settlement").
    expect(buildKnownAccounts(p)).toEqual({ acct_07: "suspense", acct_09: "reserve / settlement" });
  });
});
