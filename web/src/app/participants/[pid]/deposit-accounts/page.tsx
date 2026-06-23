"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { ChevronRight } from "lucide-react";

import { CopyId } from "@/components/copy-id";
import { EnumBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { Hint } from "@/components/hint";
import { Money } from "@/components/money";
import { Skeleton } from "@/components/ui/skeleton";
import { OpenDepositAccountForm } from "@/components/forms/open-deposit-account-form";
import { useDepositAccounts, useDepositBalance } from "@/lib/api/hooks";
import type { DepositAccount } from "@/lib/types";

// One row per deposit account. The available balance is fetched per-row (like
// the GL account rows) because the list endpoint returns only the account, not
// its balance.
function DepositAccountRow({
  pid,
  account,
}: {
  pid: string;
  account: DepositAccount;
}) {
  const { data } = useDepositBalance(pid, account.id);
  return (
    <Link
      href={`/participants/${pid}/deposit-accounts/${account.id}`}
      className="flex items-center justify-between gap-3 px-3 py-2.5 transition-colors hover:bg-muted/50"
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-sm font-medium">{account.name}</span>
        <EnumBadge value={account.status} />
        <CopyId id={account.id} />
      </span>
      <span className="flex items-center gap-3">
        <span className="text-right text-sm font-medium">
          <Money cents={data?.available ?? 0} />
          <span className="block text-xs font-normal text-muted-foreground">
            available
          </span>
        </span>
        <ChevronRight className="size-4 text-muted-foreground" />
      </span>
    </Link>
  );
}

export default function DepositAccountsPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const { data, isLoading, error, refetch } = useDepositAccounts(pid);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
          Deposit accounts
          <Hint id="balance-available" />
        </h2>
        <OpenDepositAccountForm pid={pid} />
      </div>

      {error ? (
        <ErrorState error={error} onRetry={() => refetch()} />
      ) : isLoading ? (
        <Skeleton className="h-32 w-full" />
      ) : data && data.length > 0 ? (
        <div className="divide-y rounded-lg border">
          {data.map((a) => (
            <DepositAccountRow key={a.id} pid={pid} account={a} />
          ))}
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          No deposit accounts yet. Open one, then fund it to start the money
          loop.
        </p>
      )}
    </div>
  );
}
