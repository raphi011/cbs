# Customer Account Statement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give a customer deposit account a real "bank statement" — every GL transaction that touched its backing account, projected onto that one leg, with a running balance that reconciles to the account's book balance.

**Architecture:** A pure `projectStatement()` function turns the GL transactions already returned by `useTransactions(pid, glAccount)` into ordered statement rows + running balances. A composing `useStatement()` hook wires that to the balance/participant queries. A bespoke `StatementTable` renders rows with inline-expandable double-entry; a `StatementCard` (recent 10) goes on the deposit detail page and a sub-route shows the full history. Frontend only — no backend change.

**Tech Stack:** Next.js 16 (App Router, client pages) · React 19 · TanStack Query · Tailwind v4 · shadcn `Table` primitives · **Vitest** (added here for the pure projection).

## Global Constraints

- All monetary amounts are **integer cents**. Never floats. Use `Money`/`AmountCell` to render.
- Backing account is a **liability**: a `Credit` raises the customer balance (`+`), a `Debit` lowers it (`−`).
- Path alias `@/*` → `web/src/*`. All work is under `web/`.
- Enums are string-literal unions: `Direction = "Debit" | "Credit"`, `TransactionStatus = "Posted" | "Reversed"`.
- Next 16: dynamic client pages read route params via `useParams()` (no `params` prop).
- No new runtime dependencies; the only new dev dependency is `vitest`.
- Every task ends green on `npm run typecheck` and `npm run lint`; the final task also runs `npm run build`. Commands run from `web/`.
- Reuse existing primitives — `Money`, `AmountCell`, `IdText`, `DirectionBadge`, `Hint`, `Card`, `ErrorState`, `Skeleton`, `formatDate`, shadcn `Table`. Do not rebuild them.

---

### Task 1: Test harness + statement projection

**Files:**
- Create: `web/vitest.config.mts`
- Modify: `web/package.json` (add `test` script + `vitest` devDependency)
- Create: `web/src/lib/statement.ts`
- Test: `web/src/lib/statement.test.ts`

**Interfaces:**
- Consumes: `Transaction`, `Entry`, `Participant` from `@/lib/types`; `Direction`, `TransactionStatus` from `@/lib/enums`.
- Produces:
  - `type ContraRef = { kind: "single"; accountId: string; label?: string } | { kind: "split"; count: number }`
  - `type StatementRow = { kind: "gl-leg"; txId: string; date: string; description?: string; direction: Direction; delta: number; runningBalance: number; contra: ContraRef; status: TransactionStatus; isReversed: boolean; reversalOf?: string; transaction: Transaction }`
  - `type Statement = { rows: StatementRow[]; finalBalance: number }`
  - `function projectStatement(txs: Transaction[], glAccount: string, knownAccounts?: Record<string, string>): Statement`
  - `function buildKnownAccounts(participant?: Participant): Record<string, string>`

- [ ] **Step 1: Install Vitest**

Run (from `web/`):
```bash
npm install -D vitest@^3
```
Expected: `package.json` gains `"vitest"` under `devDependencies`; `package-lock.json` updates.

- [ ] **Step 2: Add the test script**

Edit `web/package.json` `scripts` to add (keep the existing entries):
```json
    "test": "vitest run"
```

- [ ] **Step 3: Create the Vitest config**

Create `web/vitest.config.mts`:
```ts
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// Pure-function tests only (node env, no DOM). The alias mirrors tsconfig's
// "@/*" → "./src/*" so test files import the same way app code does.
export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
});
```

- [ ] **Step 4: Write the failing test**

Create `web/src/lib/statement.test.ts`:
```ts
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

  it("labels a single contra leg and collapses multi-leg contras to a split", () => {
    const single = tx({ id: "s", entries: [leg(A, 100, "Credit"), leg("acct_03", 100, "Debit")] });
    const split = tx({ id: "m", entries: [leg(A, 100, "Credit"), leg("acct_03", 60, "Debit"), leg("acct_07", 40, "Debit")] });

    const { rows } = projectStatement([single, split], A, { acct_03: "suspense" });
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
});
```

- [ ] **Step 5: Run the test to verify it fails**

Run: `npm test`
Expected: FAIL — `Failed to resolve import "@/lib/statement"` / module not found (file doesn't exist yet).

- [ ] **Step 6: Implement the projection**

Create `web/src/lib/statement.ts`:
```ts
import type { Direction, TransactionStatus } from "@/lib/enums";
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
  runningBalance: number; // cents after this row, liability convention
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
export function projectStatement(
  txs: Transaction[],
  glAccount: string,
  knownAccounts: Record<string, string> = {},
): Statement {
  const touching = txs.filter((t) => t.entries.some((e) => e.accountId === glAccount));

  const ordered = [...touching].sort((a, b) => {
    if (a.valueDate !== b.valueDate) return a.valueDate < b.valueDate ? -1 : 1;
    if (a.createdAt !== b.createdAt) return a.createdAt < b.createdAt ? -1 : 1;
    return 0;
  });

  let running = 0;
  const rows: StatementRow[] = ordered.map((t) => {
    const mine = t.entries.filter((e) => e.accountId === glAccount);
    const others = t.entries.filter((e) => e.accountId !== glAccount);

    const delta = mine.reduce(
      (sum, e) => sum + (e.direction === "Credit" ? e.amount : -e.amount),
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
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `npm test`
Expected: PASS — 5 tests across 2 suites.

- [ ] **Step 8: Typecheck and lint**

Run: `npm run typecheck && npm run lint`
Expected: both clean. If lint flags `vitest.config.mts`, add `"vitest.config.mts"` to the `ignores` array in `web/eslint.config.mjs` and re-run.

- [ ] **Step 9: Commit**

```bash
git add web/package.json web/package-lock.json web/vitest.config.mts web/src/lib/statement.ts web/src/lib/statement.test.ts
git commit -m "Add statement projection + Vitest harness"
```

---

### Task 2: `useStatement` composing hook

**Files:**
- Modify: `web/src/lib/api/hooks.ts` (add `useMemo` import + the hook; append after `useDepositBalance`)

**Interfaces:**
- Consumes: `projectStatement`, `buildKnownAccounts` from `@/lib/statement`; existing `useTransactions`, `useDepositBalance`, `useParticipant` in this file.
- Produces: `function useStatement(pid: string, did: string, glAccount: string): { rows: StatementRow[]; finalBalance: number; book: number | undefined; isLoading: boolean; error: unknown; refetch: () => void }`

> No unit test: this is React-Query wiring over the already-tested `projectStatement`. It is verified by typecheck/build here and in-browser in Task 6. Callers must pass a **non-empty** `glAccount` (mount only once the account has loaded) — `useTransactions` is gated on `pid` only, so an empty filter would fetch the full unfiltered ledger.

- [ ] **Step 1: Ensure `useMemo` is imported**

At the top of `web/src/lib/api/hooks.ts`, add `useMemo` to the React import (create the import if React isn't imported yet):
```ts
import { useMemo } from "react";
```

- [ ] **Step 2: Add the import for the projection**

Near the other `@/lib` imports in `web/src/lib/api/hooks.ts`:
```ts
import { buildKnownAccounts, projectStatement } from "@/lib/statement";
import type { StatementRow } from "@/lib/statement";
```

- [ ] **Step 3: Add the hook**

Append after the `useDepositBalance` function in `web/src/lib/api/hooks.ts`:
```ts
// Composes the GL transactions, the deposit balance, and the participant's
// well-known accounts into a ready-to-render statement. `glAccount` must be a
// real account id — call this only once the deposit account has loaded.
export function useStatement(pid: string, did: string, glAccount: string) {
  const txq = useTransactions(pid, glAccount);
  const balq = useDepositBalance(pid, did);
  const partq = useParticipant(pid);

  const known = useMemo(() => buildKnownAccounts(partq.data), [partq.data]);
  const { rows, finalBalance } = useMemo(
    () => projectStatement(txq.data ?? [], glAccount, known),
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
```

- [ ] **Step 4: Typecheck and lint**

Run: `npm run typecheck && npm run lint`
Expected: both clean.

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/api/hooks.ts
git commit -m "Add useStatement composing hook"
```

---

### Task 3: Statement hints + `StatementTable`

**Files:**
- Modify: `web/src/components/hint-content.ts` (add two entries to `hintContent`)
- Create: `web/src/components/statement/statement-table.tsx`

**Interfaces:**
- Consumes: `StatementRow`, `ContraRef` from `@/lib/statement`; primitives (`Money`, `AmountCell`, `IdText`, `DirectionBadge`, `Hint`, shadcn `Table`, `formatDate`, `cn`); hint keys `statement`, `statement-amount`.
- Produces: `function StatementTable({ rows, book, glAccount }: { rows: StatementRow[]; book?: number; glAccount: string }): JSX.Element`

> No unit test (no component-test setup; the repo verifies UI in-browser). Verified by typecheck/lint here and visually in Task 6.

- [ ] **Step 1: Add the hint entries**

In `web/src/components/hint-content.ts`, add two entries inside the `hintContent` object (place them alphabetically or at the end, before the closing `}`):
```ts
  statement: {
    title: "Account statement",
    body: `A deposit account has **no ledger of its own**. Its statement is *derived* — every [[double-entry]] GL transaction that touches the account's backing GL account, projected onto that one leg, oldest→newest, with a running balance.

The running balance reconciles to the account's **book** balance: a built-in correctness check. Holds never appear here — they post nothing to the ledger until captured.`,
  },
  "statement-amount": {
    title: "Why credits add",
    body: `Your deposit is a **liability** to the bank — money it owes you. Its [[normal-balance]] is credit, so a **Credit increases** your balance (shown \`+\`) and a **Debit decreases** it (shown \`−\`).

Expand a row to see the full balanced transaction: your line is one leg; the contra account is where the money came from or went to.`,
  },
```

- [ ] **Step 2: Verify the new hint keys typecheck**

Run: `npm run typecheck`
Expected: clean (`HintKey` now includes `"statement"` and `"statement-amount"`).

- [ ] **Step 3: Create the table component**

Create `web/src/components/statement/statement-table.tsx`:
```tsx
"use client";

import { Fragment, useState } from "react";

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { AmountCell, Money } from "@/components/money";
import { IdText } from "@/components/id-text";
import { DirectionBadge } from "@/components/enum-badge";
import { Hint } from "@/components/hint";
import { formatDate } from "@/lib/dates";
import { cn } from "@/lib/utils";
import type { ContraRef, StatementRow } from "@/lib/statement";

function ContraCell({ contra }: { contra: ContraRef }) {
  if (contra.kind === "split") {
    return <span className="text-xs text-muted-foreground">Split · {contra.count} legs</span>;
  }
  return (
    <span className="inline-flex items-center gap-1.5">
      <IdText id={contra.accountId} />
      {contra.label && <span className="text-xs text-muted-foreground">· {contra.label}</span>}
    </span>
  );
}

export function StatementTable({
  rows,
  book,
  glAccount,
}: {
  rows: StatementRow[];
  book?: number;
  glAccount: string;
}) {
  const [openTx, setOpenTx] = useState<string | null>(null);

  if (rows.length === 0) {
    return (
      <div className="rounded-lg border py-10 text-center text-sm text-muted-foreground">
        No transactions yet. Fund the account or post one to see it here.
      </div>
    );
  }

  const reconciles = book != null && rows[0].runningBalance === book;

  return (
    <div className="space-y-2">
      <div className="overflow-x-auto rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Date</TableHead>
              <TableHead>Description</TableHead>
              <TableHead>Contra</TableHead>
              <TableHead className="text-right">
                <span className="inline-flex items-center gap-1.5">
                  Amount
                  <Hint id="statement-amount" />
                </span>
              </TableHead>
              <TableHead className="text-right">Balance</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <Fragment key={row.txId}>
                <TableRow
                  className="cursor-pointer"
                  onClick={() => setOpenTx((cur) => (cur === row.txId ? null : row.txId))}
                >
                  <TableCell className="whitespace-nowrap">{formatDate(row.date)}</TableCell>
                  <TableCell>
                    <span className="inline-flex flex-wrap items-center gap-2">
                      {row.description || "—"}
                      {row.isReversed && (
                        <span className="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-800 dark:bg-amber-950 dark:text-amber-300">
                          Reversed
                        </span>
                      )}
                      {row.reversalOf && (
                        <span className="inline-flex items-center gap-1 rounded bg-amber-50 px-1.5 py-0.5 text-[10px] text-amber-700 dark:bg-amber-950/50 dark:text-amber-300">
                          reverses <IdText id={row.reversalOf} className="text-[10px]" />
                        </span>
                      )}
                    </span>
                  </TableCell>
                  <TableCell>
                    <ContraCell contra={row.contra} />
                  </TableCell>
                  <TableCell>
                    <AmountCell cents={row.delta} signed />
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    <Money cents={row.runningBalance} />
                  </TableCell>
                </TableRow>
                {openTx === row.txId && (
                  <TableRow className="bg-muted/40 hover:bg-muted/40">
                    <TableCell colSpan={5} className="p-0">
                      <div className="space-y-2 px-4 py-3">
                        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                          Underlying GL transaction <IdText id={row.txId} /> — one balanced double entry
                        </div>
                        <div className="divide-y rounded-md border bg-background">
                          {row.transaction.entries.map((e, i) => {
                            const isMine = e.accountId === glAccount;
                            return (
                              <div
                                key={e.id ?? i}
                                className={cn(
                                  "flex items-center justify-between gap-2 px-3 py-2",
                                  isMine && "bg-blue-50 dark:bg-blue-950/40",
                                )}
                              >
                                <span className="inline-flex items-center gap-2">
                                  <DirectionBadge direction={e.direction} />
                                  <IdText id={e.accountId} />
                                  {isMine && (
                                    <span className="rounded bg-blue-100 px-1.5 py-0.5 text-[10px] text-blue-700 dark:bg-blue-900 dark:text-blue-200">
                                      this account
                                    </span>
                                  )}
                                </span>
                                <Money cents={e.amount} />
                              </div>
                            );
                          })}
                        </div>
                      </div>
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            ))}
          </TableBody>
        </Table>
      </div>

      {book != null &&
        (reconciles ? (
          <p className="rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-xs text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-300">
            ✓ Running balance reconciles to the book balance <Money cents={book} className="font-medium" />.
          </p>
        ) : (
          <p className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300">
            Running balance (<Money cents={rows[0].runningBalance} />) doesn’t match the book balance
            (<Money cents={book} />) — usually the statement is still loading or a transaction is missing.
          </p>
        ))}
    </div>
  );
}
```

- [ ] **Step 4: Typecheck and lint**

Run: `npm run typecheck && npm run lint`
Expected: both clean.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/hint-content.ts web/src/components/statement/statement-table.tsx
git commit -m "Add statement hints and StatementTable"
```

---

### Task 4: `StatementCard` + wire into the deposit detail page

**Files:**
- Create: `web/src/components/statement/statement-card.tsx`
- Modify: `web/src/app/participants/[pid]/deposit-accounts/[did]/page.tsx` (import + render after `SnapshotsCard`)

**Interfaces:**
- Consumes: `useStatement` (Task 2); `StatementTable` (Task 3); `Card`/`CardHeader`/`CardTitle`/`CardContent`, `Skeleton`, `ErrorState`, `Hint`, `Link`; hint key `statement`; `DepositAccount` from `@/lib/types`.
- Produces: `function StatementCard({ pid, did, account }: { pid: string; did: string; account: DepositAccount }): JSX.Element`

> Verified visually in Task 6.

- [ ] **Step 1: Create the card**

Create `web/src/components/statement/statement-card.tsx`:
```tsx
"use client";

import Link from "next/link";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { StatementTable } from "@/components/statement/statement-table";
import { useStatement } from "@/lib/api/hooks";
import type { DepositAccount } from "@/lib/types";

export function StatementCard({
  pid,
  did,
  account,
}: {
  pid: string;
  did: string;
  account: DepositAccount;
}) {
  const { rows, book, isLoading, error, refetch } = useStatement(pid, did, account.glAccount);
  const recent = rows.slice(0, 10);
  const hasMore = rows.length > recent.length;
  const statementHref = `/participants/${pid}/deposit-accounts/${did}/statement`;

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-1.5 text-base">
          Statement
          <Hint id="statement" />
        </CardTitle>
        <Link href={statementHref} className="text-sm text-muted-foreground hover:text-foreground">
          View full statement →
        </Link>
      </CardHeader>
      <CardContent className="space-y-2">
        {error ? (
          <ErrorState error={error} onRetry={() => refetch()} />
        ) : isLoading ? (
          <Skeleton className="h-40 w-full" />
        ) : (
          <>
            <StatementTable rows={recent} book={book} glAccount={account.glAccount} />
            {hasMore && (
              <p className="text-xs text-muted-foreground">
                Showing the {recent.length} most recent of {rows.length} transactions.{" "}
                <Link href={statementHref} className="underline hover:text-foreground">
                  View all
                </Link>
                .
              </p>
            )}
            <p className="text-xs text-muted-foreground">
              Holds reduce your <em>available</em> balance but don’t appear here — they post nothing to the
              ledger until captured. See the Holds card above.
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}
```

- [ ] **Step 2: Import the card in the detail page**

In `web/src/app/participants/[pid]/deposit-accounts/[did]/page.tsx`, add to the imports:
```tsx
import { StatementCard } from "@/components/statement/statement-card";
```

- [ ] **Step 3: Render the card after `SnapshotsCard`**

In the same file, in the loaded branch, change:
```tsx
          <BalanceCard pid={pid} did={did} />
          <HoldsCard pid={pid} did={did} />
          <SnapshotsCard pid={pid} did={did} />
```
to:
```tsx
          <BalanceCard pid={pid} did={did} />
          <HoldsCard pid={pid} did={did} />
          <SnapshotsCard pid={pid} did={did} />
          <StatementCard pid={pid} did={did} account={account} />
```
(`account` is the loaded `DepositAccount` already in scope in that branch.)

- [ ] **Step 4: Typecheck and lint**

Run: `npm run typecheck && npm run lint`
Expected: both clean.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/statement/statement-card.tsx "web/src/app/participants/[pid]/deposit-accounts/[did]/page.tsx"
git commit -m "Add StatementCard to deposit detail page"
```

---

### Task 5: Statement sub-route page

**Files:**
- Create: `web/src/app/participants/[pid]/deposit-accounts/[did]/statement/page.tsx`

**Interfaces:**
- Consumes: `useDepositAccount`, `useStatement`; `StatementTable`; `Skeleton`, `ErrorState`, `IdText`, `Link`, `ArrowLeft`, `useParams`; `DepositAccount` from `@/lib/types`.
- Produces: a default-exported Next client page at `…/[did]/statement`.

> Verified visually in Task 6. The inner `StatementBody` is mounted only once the account has loaded, so `useStatement` always receives a real `glAccount`.

- [ ] **Step 1: Create the page**

Create `web/src/app/participants/[pid]/deposit-accounts/[did]/statement/page.tsx`:
```tsx
"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { ArrowLeft } from "lucide-react";

import { Skeleton } from "@/components/ui/skeleton";
import { IdText } from "@/components/id-text";
import { ErrorState } from "@/components/error-state";
import { StatementTable } from "@/components/statement/statement-table";
import { useDepositAccount, useStatement } from "@/lib/api/hooks";
import type { DepositAccount } from "@/lib/types";

function StatementBody({
  pid,
  did,
  account,
}: {
  pid: string;
  did: string;
  account: DepositAccount;
}) {
  const { rows, book, isLoading, error, refetch } = useStatement(pid, did, account.glAccount);
  if (error) return <ErrorState error={error} onRetry={() => refetch()} />;
  if (isLoading) return <Skeleton className="h-64 w-full" />;
  return <StatementTable rows={rows} book={book} glAccount={account.glAccount} />;
}

export default function StatementPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, isLoading, error, refetch } = useDepositAccount(pid, did);
  const back = `/participants/${pid}/deposit-accounts/${did}`;

  return (
    <div className="space-y-5">
      <Link href={back} className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" /> Account
      </Link>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : isLoading || !account ? (
        <Skeleton className="h-10 w-64" />
      ) : (
        <>
          <div className="flex items-center gap-2">
            <h2 className="text-xl font-semibold tracking-tight">{account.name} — statement</h2>
            <IdText id={account.id} />
          </div>
          <StatementBody pid={pid} did={did} account={account} />
        </>
      )}
    </div>
  );
}
```

- [ ] **Step 2: Typecheck and lint**

Run: `npm run typecheck && npm run lint`
Expected: both clean.

- [ ] **Step 3: Commit**

```bash
git add "web/src/app/participants/[pid]/deposit-accounts/[did]/statement/page.tsx"
git commit -m "Add full statement sub-route"
```

---

### Task 6: End-to-end browser verification

**Files:** none (verification + final gates).

- [ ] **Step 1: Final static gates**

Run (from `web/`): `npm test && npm run typecheck && npm run lint && npm run build`
Expected: tests pass; typecheck/lint clean; production build succeeds.

- [ ] **Step 2: Start backend and frontend**

```bash
# repo root, separate shell:
go run ./cmd/server      # :8080, in-memory, resets on restart
# web/, separate shell:
npm run dev              # http://localhost:3000
```

- [ ] **Step 3: Seed data via the UI**

In the browser: create a participant → open a deposit account → **fund** it (e.g. €500) → post a couple of transactions touching the account → **reverse** one of them (use the transactions page's Reverse action).

- [ ] **Step 4: Verify the StatementCard on the deposit detail page**

Navigate to the deposit account detail page. Confirm:
- A "Statement" card appears after Snapshots with a `?` hint and a "View full statement →" link.
- Rows are **newest-first**; amounts are signed and colored (green `+`, red `−`); each row shows a contra account (well-known ones labeled suspense/reserve/settlement).
- The top row's **Balance equals the Balance card's Book** value, and the green reconciliation banner shows.
- The reversed original shows a `Reversed` badge; the reversal row shows `reverses {id}`.
- Clicking a row expands the full double-entry with `Dr`/`Cr` badges and the highlighted "this account" leg.
- The holds footnote is present; placing a hold does **not** add a statement row.

- [ ] **Step 5: Verify the sub-route**

Click "View full statement →". Confirm the dedicated page lists the full history with the same formatting and the back link returns to the account.

- [ ] **Step 6: Verify edge states**

- A brand-new account (no transactions) shows the empty message.
- Close an account with zero balance (or view an already-closed one) and confirm its statement history still renders.

- [ ] **Step 7: Final commit (if any verification fix was needed)**

```bash
git add -A
git commit -m "Polish statement view after verification"
```

---

## Self-Review

- **Spec coverage:** projection + tagged-union row (Task 1); placement card + sub-route sharing one projection/table (Tasks 3–5); refined-hybrid amount display with contra + Amount `?` hint (Task 3); inline double-entry reveal with highlighted leg (Task 3); badged reversals (Task 3); holds excluded + footnote (Tasks 3–4); ordering valueDate→createdAt + newest-first + book reconciliation (Tasks 1, 3); states empty/loading/error/closed + no pagination/recent-10 slice (Tasks 3–6); Vitest TDD for the projection (Task 1). The spec's separate `StatementView` component is consolidated into the sub-route page (`StatementBody`) — same behavior, less indirection (DRY/YAGNI).
- **Placeholder scan:** none — every code/command step is concrete.
- **Type consistency:** `projectStatement`/`buildKnownAccounts`/`StatementRow`/`ContraRef`/`Statement` (Task 1) are consumed unchanged by `useStatement` (Task 2), `StatementTable` (Task 3), `StatementCard` (Task 4), and the sub-route (Task 5). `StatementTable` signature `{ rows, book, glAccount }` matches all call sites.
