"use client";

import { useParams } from "next/navigation";

import { ErrorState } from "@/components/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { StatementTable } from "@/components/statement/statement-table";
import { useAssetLookup, useDepositAccount, useStatement } from "@/lib/api/hooks";

// The same projection the back office reads, framed for the person whose money
// it is: no contra accounts, no double entry, no reconciliation check. The
// statement is theirs; the bookkeeping behind it is the bank's.
export default function CustomerActivity() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const did = typeof params.did === "string" ? params.did : "";

  const { data: account, error: accountError } = useDepositAccount(pid, did);
  const { byCode } = useAssetLookup();
  const asset = account ? byCode.get(account.asset) : undefined;
  const { rows, book, isLoading, error, refetch } = useStatement(
    pid,
    did,
    account?.glAccount ?? "",
  );

  if (accountError || error) {
    return <ErrorState error={accountError ?? error} onRetry={() => refetch()} />;
  }
  if (!account || !asset || isLoading) return <Skeleton className="h-64 w-full" />;

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold tracking-tight">Activity</h1>
      <StatementTable
        retail
        rows={rows}
        book={book}
        glAccount={account.glAccount}
        pid={pid}
        asset={asset}
      />
      <p className="text-xs text-muted-foreground">
        Money held for a card authorisation is not here — it reduces what you can
        spend without moving your balance, and only appears once it is taken.
      </p>
    </div>
  );
}
