"use client";

import Link from "next/link";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { StatementTable } from "@/components/statement/statement-table";
import { useStatement } from "@/lib/api/hooks";
import type { Asset, DepositAccount } from "@/lib/types";

export function StatementCard({
  pid,
  did,
  account,
  asset,
}: {
  pid: string;
  did: string;
  account: DepositAccount;
  asset: Asset;
}) {
  const { rows, book, isLoading, error, refetch } = useStatement(pid, did, account.controlAccount);
  const recent = rows.slice(0, 10);
  const hasMore = rows.length > recent.length;
  const statementHref = `/bank/${pid}/deposit-accounts/${did}/statement`;

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
            <StatementTable
              rows={recent}
              book={book}
              account={account.controlAccount}
              subsidiary={account.id}
              pid={pid}
              asset={asset}
            />
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
              Holds reduce your <em>available</em> balance but don&apos;t appear here — they post nothing to the
              ledger until captured. See the Holds card above.
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}
