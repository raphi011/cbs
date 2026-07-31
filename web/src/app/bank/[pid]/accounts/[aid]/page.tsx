"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { ArrowLeft } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { IdText } from "@/components/id-text";
import { Money } from "@/components/money";
import { AccountTypeBadge } from "@/components/enum-badge";
import { ErrorState } from "@/components/error-state";
import { StatementTable } from "@/components/statement/statement-table";
import {
  useAccountStatement,
  useAssetLookup,
  useDepositAccounts,
  useGLAccount,
  useParticipant,
} from "@/lib/api/hooks";
import { buildKnownAccounts } from "@/lib/statement";

export default function AccountDetailPage() {
  const params = useParams();
  const pid = typeof params.pid === "string" ? params.pid : "";
  const aid = typeof params.aid === "string" ? params.aid : "";

  const { account, isLoading: accLoading, error: accError, refetch } = useGLAccount(pid, aid);
  const statement = useAccountStatement(pid, aid, account?.type);
  const { data: deposits } = useDepositAccounts(pid);
  const { data: participant } = useParticipant(pid);
  const { byCode, isLoading: assetsLoading } = useAssetLookup();
  const asset = account ? byCode.get(account.asset) : undefined;

  const back = `/bank/${pid}/ledger`;
  const backingDeposit = deposits?.find((d) => d.glAccount === aid);
  const role = participant ? buildKnownAccounts(participant)[aid] : undefined;

  return (
    <div className="space-y-5">
      <Link href={back} className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" /> Chart of accounts
      </Link>

      {accError ? (
        <ErrorState error={accError} onRetry={() => refetch()} />
      ) : accLoading ? (
        <Skeleton className="h-10 w-64" />
      ) : !account ? (
        <ErrorState error={new Error(`Account ${aid} not found in the chart of accounts.`)} onRetry={() => refetch()} />
      ) : assetsLoading ? (
        <Skeleton className="h-10 w-64" />
      ) : !asset ? (
        <ErrorState
          error={
            new Error(
              `This account is denominated in "${account.asset}", which the system has no definition for, so its amounts cannot be rendered at a known scale.`,
            )
          }
          onRetry={() => refetch()}
        />
      ) : (
        <>
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight">{account.name}</h2>
              <AccountTypeBadge type={account.type} />
              <IdText id={account.id} />
            </div>
            <p className="text-sm text-muted-foreground">
              {account.ledgerName} · {account.subledgerName}
              {role && (
                <>
                  {" · "}the participant&apos;s <span className="font-medium">{role}</span> account
                </>
              )}
            </p>
            {backingDeposit && (
              <p className="text-sm text-muted-foreground">
                Backing account for deposit{" "}
                <Link
                  href={`/bank/${pid}/deposit-accounts/${backingDeposit.id}`}
                  className="underline hover:text-foreground"
                >
                  {backingDeposit.name}
                </Link>
                .
              </p>
            )}
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">Balance</CardTitle>
            </CardHeader>
            <CardContent>
              {statement.book == null ? (
                <Skeleton className="h-6 w-24" />
              ) : (
                <div className="text-lg font-semibold tabular-nums">
                  <Money amount={statement.book} asset={asset} />
                </div>
              )}
            </CardContent>
          </Card>

          <div className="space-y-2">
            <h3 className="text-sm font-medium text-muted-foreground">Account ledger</h3>
            {statement.error ? (
              <ErrorState error={statement.error} onRetry={() => statement.refetch()} />
            ) : statement.isLoading ? (
              <Skeleton className="h-64 w-full" />
            ) : (
              <StatementTable
                rows={statement.rows}
                book={statement.book}
                glAccount={aid}
                pid={pid}
                asset={asset}
                amountHintId="normal-balance"
              />
            )}
          </div>
        </>
      )}
    </div>
  );
}
