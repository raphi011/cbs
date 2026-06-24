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
