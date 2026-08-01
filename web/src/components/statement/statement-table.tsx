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
import { AccountRef } from "@/components/account-ref";
import { Skeleton } from "@/components/ui/skeleton";
import { useAssetLookup } from "@/lib/api/hooks";
import { formatDate } from "@/lib/dates";
import { cn } from "@/lib/utils";
import type { ContraRef, StatementRow } from "@/lib/statement";
import type { HintKey } from "@/components/hint-content";
import type { Asset, Entry } from "@/lib/types";

// A leg's amount is denominated in its own account's asset, which can differ
// leg to leg within one transaction. `byCode` resolves each leg independently
// rather than assuming every leg shares the statement account's asset.
function EntryAmount({ entry, byCode }: { entry: Entry; byCode: Map<string, Asset> }) {
  const asset = byCode.get(entry.asset);
  return asset ? <Money amount={entry.amount} asset={asset} /> : <Skeleton className="h-4 w-16" />;
}

function ContraCell({ pid, contra }: { pid: string; contra: ContraRef }) {
  if (contra.kind === "split") {
    return <span className="text-xs text-muted-foreground">Split · {contra.count} legs</span>;
  }
  return <AccountRef pid={pid} id={contra.accountId} label={contra.label} />;
}

export function StatementTable({
  rows,
  book,
  glAccount,
  pid,
  asset,
  amountHintId = "statement-amount",
  retail = false,
}: {
  rows: StatementRow[];
  book?: number;
  glAccount: string;
  pid: string;
  // The asset of the account this statement is projected onto. `delta` and
  // `runningBalance` are always in this one asset (they're this account's
  // own balance); individual legs of the underlying transaction may differ
  // — see EntryAmount above.
  asset: Asset;
  amountHintId?: HintKey;
  // Retail framing: no contra column, no expandable GL detail, no reconciliation
  // banner. A customer's statement is dates, descriptions, amounts and a running
  // balance; the double entry behind it is the bank's business, and linking to it
  // would navigate out of the persona.
  retail?: boolean;
}) {
  const [openTx, setOpenTx] = useState<string | null>(null);
  const { byCode } = useAssetLookup();

  if (rows.length === 0) {
    return (
      <div className="rounded-lg border py-10 text-center text-sm text-muted-foreground">
        {retail
          ? "No activity yet. Money arriving or leaving this account will appear here."
          : "No transactions yet. Fund the account or post one to see it here."}
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
              {!retail && <TableHead>Contra</TableHead>}
              <TableHead className="text-right">
                <span className="inline-flex items-center gap-1.5">
                  Amount
                  <Hint id={amountHintId} />
                </span>
              </TableHead>
              <TableHead className="text-right">Balance</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <Fragment key={row.txId}>
                <TableRow
                  className={cn(!retail && "cursor-pointer")}
                  onClick={
                    retail
                      ? undefined
                      : () => setOpenTx((cur) => (cur === row.txId ? null : row.txId))
                  }
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
                  {!retail && (
                    <TableCell>
                      <ContraCell pid={pid} contra={row.contra} />
                    </TableCell>
                  )}
                  <TableCell>
                    <AmountCell amount={row.delta} asset={asset} signed />
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    <Money amount={row.runningBalance} asset={asset} />
                  </TableCell>
                </TableRow>
                {!retail && openTx === row.txId && (
                  <TableRow className="bg-muted/40 hover:bg-muted/40">
                    <TableCell colSpan={retail ? 4 : 5} className="p-0">
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
                                  <AccountRef pid={pid} id={e.accountId} />
                                  {isMine && (
                                    <span className="rounded bg-blue-100 px-1.5 py-0.5 text-[10px] text-blue-700 dark:bg-blue-900 dark:text-blue-200">
                                      this account
                                    </span>
                                  )}
                                </span>
                                <EntryAmount entry={e} byCode={byCode} />
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

      {!retail &&
        book != null &&
        (reconciles ? (
          <p className="rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-xs text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-300">
            ✓ Running balance reconciles to the book balance{" "}
            <Money amount={book} asset={asset} className="font-medium" />.
          </p>
        ) : (
          <p className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300">
            Running balance (<Money amount={rows[0].runningBalance} asset={asset} />) doesn&apos;t match the
            book balance (<Money amount={book} asset={asset} />) — usually the statement is still loading or a
            transaction is missing.
          </p>
        ))}
    </div>
  );
}
